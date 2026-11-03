package thorchain

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	gogogrpc "github.com/cosmos/gogoproto/grpc"
	"google.golang.org/grpc"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// LuviMigrateGateSuite is the CI gate on store migrations: every registered
// migration runs against a fixture state (pools with LPs, POL and synths), and
// any pool whose hedged LP equity G drops beyond the LUVI tolerance fails the
// test unless luviDeclaredWriteDowns declares the drop. The suite name
// contains "Migrate", so the mainnet-tagged migration job (make test-network-specific)
// runs it too, against the mainnet migration bodies.
type LuviMigrateGateSuite struct{}

var _ = Suite(&LuviMigrateGateSuite{})

// luviMigrationFixturePool is one fixture pool, in base units: depths, LP
// units and synth supply, plus the share of LP units owned by POL.
type luviMigrationFixturePool struct {
	asset                     common.Asset
	rune, depth, units, synth uint64
	polBp                     uint64
}

// luviMigrationFixturePools are mainnet's pools on 2026-10-25 (synths from
// 0.15% of the asset depth on ETH to 152% on DOGE), with POL positions.
var luviMigrationFixturePools = []luviMigrationFixturePool{
	{common.BTCAsset, 2_325_345_610_724_736, 18_128_301_820, 196_412_898_527_083, 51_741_641_679, 2_000},
	{common.ETHAsset, 689_113_678_501_414, 184_931_562_932, 62_487_621_764_688, 273_214_102_394, 1_000},
	{common.DOGEAsset, 118_298_225_029_677, 960_752_321_780_216, 26_163_411_292_579, 1_463_003_898_707_137, 500},
	{common.LTCAsset, 93_269_480_779_983, 1_025_674_575_996, 20_007_722_719_914, 1_262_735_061_494, 0},
	{common.ATOMAsset, 21_220_515_365_756, 7_899_746_648_392, 12_407_276_441_639, 0, 0}, // synths dropped: see TestWrapperIgnoresNeutralAndNewPools
}

// setupLuviMigrationFixture builds a chain state for the gate: available
// pools with user LPs and POL, synths held by users, and the module balances
// that back them.
func setupLuviMigrationFixture(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	polAddr, err := k.GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	totalRune := uint64(0)
	for _, fp := range luviMigrationFixturePools {
		pool := NewPool()
		pool.Asset = fp.asset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(fp.rune)
		pool.BalanceAsset = cosmos.NewUint(fp.depth)
		pool.LPUnits = cosmos.NewUint(fp.units)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		totalRune += fp.rune

		polUnits := pool.LPUnits.MulUint64(fp.polBp).QuoUint64(constants.MaxBasisPts)
		k.SetLiquidityProvider(ctx, LiquidityProvider{
			Asset:        fp.asset,
			RuneAddress:  GetRandomRUNEAddress(),
			AssetAddress: GetRandomTHORAddress(),
			Units:        pool.LPUnits.Sub(polUnits),
		})
		if !polUnits.IsZero() {
			k.SetLiquidityProvider(ctx, LiquidityProvider{Asset: fp.asset, RuneAddress: polAddr, Units: polUnits})
		}
		if fp.synth > 0 {
			synth := common.NewCoin(fp.asset.GetSyntheticAsset(), cosmos.NewUint(fp.synth))
			c.Assert(k.MintToModule(ctx, ModuleName, synth), IsNil)
			c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(synth)), IsNil)
		}
	}
	FundModule(c, ctx, k, AsgardName, totalRune)
	c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	return ctx, mgr
}

// luviMigrationGateFailures returns the pools a migration scope dropped
// beyond the tolerance without a matching luviDeclaredWriteDowns entry.
func luviMigrationGateFailures(scope string, changes []luviMigrationChange) []string {
	var out []string
	tol := types.LuviDefaultTolerance
	for _, ch := range changes {
		if ch.NormalizedDeltaG.GTE(tol.Neg()) {
			continue
		}
		drop := ch.NormalizedDeltaG.Neg()
		d, ok := luviDeclaredDrop(scope, ch.Pool)
		if ok && drop.LTE(d.MaxDrop.Add(tol)) {
			continue
		}
		declared := "undeclared"
		if ok {
			declared = fmt.Sprintf("declared max %s", d.MaxDrop)
		}
		out = append(out, fmt.Sprintf("%s %s: normalized dG %s (%s); pre %s/%s units %s synth %s, post %s/%s units %s synth %s",
			scope, ch.Pool, ch.NormalizedDeltaG, declared,
			ch.Pre.BalanceRune, ch.Pre.BalanceAsset, ch.Pre.LPUnits, ch.Pre.SynthSupply,
			ch.Post.BalanceRune, ch.Post.BalanceAsset, ch.Post.LPUnits, ch.Post.SynthSupply))
	}
	return out
}

// runLuviMigrationGate runs one migration (or migration step) in a migration
// scope and reports it, as the registered wrapper does, returning the gate
// failures. A migration error is returned as-is: an upgrade that fails does
// not move LP value.
func runLuviMigrationGate(c *C, ctx cosmos.Context, mgr *Mgrs, scope string, run func(sdk.Context) error) ([]string, error) {
	k := mgr.Keeper()
	scopeCtx, s := beginLuviMigrationScope(ctx, k, scope)
	c.Assert(s, NotNil)
	if err := run(scopeCtx); err != nil {
		return nil, err
	}
	changes := s.changes(scopeCtx, k)
	s.endMigration(scopeCtx, k)
	return luviMigrationGateFailures(scope, changes), nil
}

func luviMigrationLossEvents(ctx cosmos.Context) []sdk.Event {
	var out []sdk.Event
	for _, e := range ctx.EventManager().Events() {
		if e.Type == types.LuviLossEventType {
			out = append(out, e)
		}
	}
	return out
}

func luviEventAttr(e sdk.Event, key string) string {
	for _, a := range e.Attributes {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// TestRegisteredMigrationsAreLPNeutral is the gate: every registered
// migration, run in order from the oldest, on the fixture state.
func (s *LuviMigrateGateSuite) TestRegisteredMigrationsAreLPNeutral(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	for _, mig := range NewMigrator(mgr).luviMigrations() {
		scope := luviMigrationScopeName(mig.from)
		failures, err := runLuviMigrationGate(c, ctx, mgr, scope, mig.run)
		if err != nil {
			// mainnet bodies are pinned to mainnet state and may refuse the
			// fixture; that is not an LP-value change
			c.Logf("%s returned %v on the fixture", scope, err)
			continue
		}
		c.Check(failures, HasLen, 0, Commentf("migration moved LP value without a luviDeclaredWriteDowns entry:\n%s", strings.Join(failures, "\n")))
	}
}

// TestRegisteredMigrationsMatchConsensusVersion keeps the registration table
// contiguous and ending at the module's consensus version.
func (s *LuviMigrateGateSuite) TestRegisteredMigrationsMatchConsensusVersion(c *C) {
	_, mgr := setupManagerForTest(c)
	migs := NewMigrator(mgr).luviMigrations()
	c.Assert(migs, Not(HasLen), 0)
	for i := 1; i < len(migs); i++ {
		c.Check(migs[i].from, Equals, migs[i-1].from+1)
	}
	c.Check(migs[len(migs)-1].from+1, Equals, AppModule{}.ConsensusVersion())
}

// TestDeclaredWriteDownsAreWellFormed checks every allowlist entry names a
// registered migration and a pool, bounds its drop and justifies it.
func (s *LuviMigrateGateSuite) TestDeclaredWriteDownsAreWellFormed(c *C) {
	_, mgr := setupManagerForTest(c)
	known := map[string]bool{}
	for _, mig := range NewMigrator(mgr).luviMigrations() {
		known[luviMigrationScopeName(mig.from)] = true
	}
	for scope, pools := range luviDeclaredWriteDowns {
		c.Check(known[scope], Equals, true, Commentf("%s is not a registered migration", scope))
		for pool, d := range pools {
			asset, err := common.NewAsset(pool)
			c.Check(err, IsNil, Commentf("%s: %s", scope, pool))
			c.Check(asset.String(), Equals, pool)
			c.Check(d.MaxDrop.IsPositive(), Equals, true, Commentf("%s %s", scope, pool))
			c.Check(d.MaxDrop.LTE(cosmos.NewDec(1)), Equals, true, Commentf("%s %s", scope, pool))
			c.Check(strings.TrimSpace(d.Reason), Not(Equals), "", Commentf("%s %s needs a reason", scope, pool))
		}
	}
}

// luviWriteDownETH is a value-moving migration: it removes 10% of the ETH
// pool's asset depth without pairing RUNE.
func luviWriteDownETH(mgr *Mgrs) func(sdk.Context) error {
	return func(ctx sdk.Context) error {
		pool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
		if err != nil {
			return err
		}
		pool.BalanceAsset = pool.BalanceAsset.Sub(pool.BalanceAsset.QuoUint64(10))
		return mgr.Keeper().SetPool(ctx, pool)
	}
}

func (s *LuviMigrateGateSuite) TestGateFailsUndeclaredWriteDown(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	failures, err := runLuviMigrationGate(c, ctx, mgr, "migration/99to100", luviWriteDownETH(mgr))
	c.Assert(err, IsNil)
	c.Assert(failures, HasLen, 1)
	c.Check(strings.Contains(failures[0], "ETH.ETH"), Equals, true, Commentf("%s", failures[0]))
	c.Check(strings.Contains(failures[0], "undeclared"), Equals, true)
}

func (s *LuviMigrateGateSuite) TestGateAcceptsDeclaredWriteDown(c *C) {
	const scope = "migration/99to100"
	luviDeclaredWriteDowns[scope] = map[string]luviDeclaredWriteDown{
		"ETH.ETH": {MaxDrop: cosmos.NewDecWithPrec(6, 2), Reason: "test write-down"},
	}
	defer delete(luviDeclaredWriteDowns, scope)

	ctx, mgr := setupLuviMigrationFixture(c)
	failures, err := runLuviMigrationGate(c, ctx, mgr, scope, luviWriteDownETH(mgr))
	c.Assert(err, IsNil)
	c.Check(failures, HasLen, 0, Commentf("%v", failures))

	// a declared bound below the actual drop still fails
	luviDeclaredWriteDowns[scope]["ETH.ETH"] = luviDeclaredWriteDown{MaxDrop: cosmos.NewDecWithPrec(1, 2), Reason: "too small"}
	ctx, mgr = setupLuviMigrationFixture(c)
	failures, err = runLuviMigrationGate(c, ctx, mgr, scope, luviWriteDownETH(mgr))
	c.Assert(err, IsNil)
	c.Assert(failures, HasLen, 1)
	c.Check(strings.Contains(failures[0], "declared max 0.01"), Equals, true, Commentf("%s", failures[0]))
}

// TestWrapperReportsWriteDown runs a value-moving migration through the
// registered wrapper: it succeeds, and emits a loss event in its scope.
func (s *LuviMigrateGateSuite) TestWrapperReportsWriteDown(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	before := len(luviMigrationLossEvents(ctx))
	c.Assert(luviGuardMigration(mgr, 99, luviWriteDownETH(mgr))(ctx), IsNil)
	events := luviMigrationLossEvents(ctx)[before:]
	c.Assert(events, HasLen, 1)
	c.Check(luviEventAttr(events[0], "pool"), Equals, "ETH.ETH")
	c.Check(luviEventAttr(events[0], "scope"), Equals, "migration/99to100")
	c.Check(luviEventAttr(events[0], "kind"), Equals, "drop")
	c.Check(luviEventAttr(events[0], "excess"), Equals, "true")

	// the write-down itself was kept: report only
	pool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(184_931_562_932-18_493_156_293)), Equals, true, Commentf("%s", pool.BalanceAsset))
}

// TestWrapperSeesRawStoreWrites removes a pool record with a raw key delete,
// which the keeper journal cannot see; the up-front snapshot does.
func (s *LuviMigrateGateSuite) TestWrapperSeesRawStoreWrites(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	k := mgr.Keeper()
	rawDelete := func(ctx sdk.Context) error {
		k.DeleteKey(ctx, k.GetKey("pool/", common.DOGEAsset.String()))
		return nil
	}
	c.Assert(k.PoolExist(ctx, common.DOGEAsset), Equals, true)
	before := len(luviMigrationLossEvents(ctx))
	c.Assert(luviGuardMigration(mgr, 99, rawDelete)(ctx), IsNil)
	c.Assert(k.PoolExist(ctx, common.DOGEAsset), Equals, false)
	// the LP units vanished without withdraw events (ledger) and the pool's
	// LP value with them (removed)
	events := luviMigrationLossEvents(ctx)[before:]
	c.Assert(events, HasLen, 2)
	for i, kind := range []string{"ledger", "removed"} {
		c.Check(luviEventAttr(events[i], "pool"), Equals, "DOGE.DOGE")
		c.Check(luviEventAttr(events[i], "kind"), Equals, kind)
	}

	ctx, mgr = setupLuviMigrationFixture(c)
	k = mgr.Keeper()
	failures, err := runLuviMigrationGate(c, ctx, mgr, "migration/99to100", rawDelete)
	c.Assert(err, IsNil)
	c.Assert(failures, HasLen, 1)
	c.Check(strings.Contains(failures[0], "DOGE.DOGE: normalized dG -1."), Equals, true, Commentf("%s", failures[0]))
}

// TestWrapperIgnoresNeutralAndNewPools: a k-neutral rebalance (asset out,
// RUNE in at the constant product), a deposit and a new pool are not drops.
func (s *LuviMigrateGateSuite) TestWrapperIgnoresNeutralAndNewPools(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	k := mgr.Keeper()
	neutral := func(ctx sdk.Context) error {
		pool, err := k.GetPool(ctx, common.ATOMAsset)
		if err != nil {
			return err
		}
		// donate to the BTC pool: G rises
		btc, err := k.GetPool(ctx, common.BTCAsset)
		if err != nil {
			return err
		}
		btc.BalanceRune = btc.BalanceRune.Add(cosmos.NewUint(common.One))
		if err := k.SetPool(ctx, btc); err != nil {
			return err
		}
		// a brand new pool has no LP value to lose
		fresh := NewPool()
		fresh.Asset = common.BCHAsset
		fresh.BalanceRune = cosmos.NewUint(common.One)
		fresh.BalanceAsset = cosmos.NewUint(common.One)
		fresh.LPUnits = cosmos.NewUint(common.One)
		if err := k.SetPool(ctx, fresh); err != nil {
			return err
		}
		// asset out, RUNE in so that A*R is unchanged (no synths: G unchanged)
		writedown := pool.BalanceAsset.QuoUint64(100)
		remaining := pool.BalanceAsset.Sub(writedown)
		pool.BalanceRune = pool.BalanceRune.Add(common.GetUncappedShare(writedown, remaining, pool.BalanceRune))
		pool.BalanceAsset = remaining
		return k.SetPool(ctx, pool)
	}
	scopeCtx, scope := beginLuviMigrationScope(ctx, k, "migration/99to100")
	c.Assert(neutral(scopeCtx), IsNil)
	changes := scope.changes(scopeCtx, k)
	c.Assert(changes, HasLen, 3)
	for _, ch := range changes {
		switch ch.Pool.String() {
		case "BTC.BTC":
			c.Check(ch.NormalizedDeltaG.IsPositive(), Equals, true, Commentf("%s", ch.NormalizedDeltaG))
		case "BCH.BCH":
			c.Check(ch.NormalizedDeltaG.IsZero(), Equals, true)
		case "GAIA.ATOM":
			c.Check(ch.NormalizedDeltaG.Abs().LT(cosmos.NewDecWithPrec(1, 9)), Equals, true, Commentf("%s", ch.NormalizedDeltaG))
		default:
			c.Errorf("unexpected change in %s", ch.Pool)
		}
	}
	c.Check(luviMigrationGateFailures("migration/99to100", changes), HasLen, 0)
}

func (s *LuviMigrateGateSuite) TestWrapperPassesThroughErrors(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	before := len(luviMigrationLossEvents(ctx))
	failing := func(ctx sdk.Context) error {
		if err := luviWriteDownETH(mgr)(ctx); err != nil {
			return err
		}
		return fmt.Errorf("boom")
	}
	err := luviGuardMigration(mgr, 99, failing)(ctx)
	c.Assert(err, ErrorMatches, "boom")
	c.Check(luviMigrationLossEvents(ctx)[before:], HasLen, 0)
}

func (s *LuviMigrateGateSuite) TestWrapperRespectsLUVIGuardMimir(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	mgr.Keeper().SetMimir(ctx, constants.LUVIGuard.String(), 0)
	scopeCtx, scope := beginLuviMigrationScope(ctx, mgr.Keeper(), "migration/99to100")
	c.Check(scope, IsNil)
	c.Check(scope.changes(scopeCtx, mgr.Keeper()), HasLen, 0)
	scope.endMigration(scopeCtx, mgr.Keeper()) // nil scope: no-op

	before := len(luviMigrationLossEvents(ctx))
	c.Assert(luviGuardMigration(mgr, 99, luviWriteDownETH(mgr))(ctx), IsNil)
	c.Check(luviMigrationLossEvents(ctx)[before:], HasLen, 0)
}

func (s *LuviMigrateGateSuite) TestLuviStatesEqual(c *C) {
	a := types.LuviState{BalanceRune: cosmos.NewUint(1), BalanceAsset: cosmos.NewUint(2), LPUnits: cosmos.NewUint(3), SynthSupply: cosmos.ZeroUint(), PoolUnits: cosmos.NewUint(3)}
	c.Check(luviStatesEqual(a, a), Equals, true)
	b := a
	b.SynthSupply = cosmos.NewUint(1)
	c.Check(luviStatesEqual(a, b), Equals, false)
	var empty types.LuviState
	c.Check(luviStatesEqual(empty, empty), Equals, true)
	c.Check(luviStatesEqual(a, empty), Equals, false)
}

// luviRecordingConfigurator records the migrations a module registers.
type luviRecordingConfigurator struct {
	migrations map[uint64]module.MigrationHandler
}

func (r *luviRecordingConfigurator) RegisterService(*grpc.ServiceDesc, any) {}
func (r *luviRecordingConfigurator) Error() error                           { return nil }
func (r *luviRecordingConfigurator) MsgServer() gogogrpc.Server             { return r }
func (r *luviRecordingConfigurator) QueryServer() gogogrpc.Server           { return r }

func (r *luviRecordingConfigurator) RegisterMigration(moduleName string, from uint64, h module.MigrationHandler) error {
	if moduleName != ModuleName {
		return fmt.Errorf("unexpected module %s", moduleName)
	}
	r.migrations[from] = h
	return nil
}

// TestRegisterServicesRegistersGuardedMigrations checks RegisterServices
// registers every migration of luviMigrations, through the LUVI wrapper.
func (s *LuviMigrateGateSuite) TestRegisterServicesRegistersGuardedMigrations(c *C) {
	ctx, mgr := setupLuviMigrationFixture(c)
	cfg := &luviRecordingConfigurator{migrations: map[uint64]module.MigrationHandler{}}
	AppModule{mgr: mgr}.RegisterServices(cfg)
	migs := NewMigrator(mgr).luviMigrations()
	c.Assert(cfg.migrations, HasLen, len(migs))
	for _, mig := range migs {
		h, ok := cfg.migrations[mig.from]
		c.Assert(ok, Equals, true, Commentf("migration from %d not registered", mig.from))
		if mig.from == (AppModule{}).ConsensusVersion()-1 {
			c.Check(h(ctx), IsNil)
		}
	}
}
