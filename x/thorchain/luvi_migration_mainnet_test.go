//go:build mainnet
// +build mainnet

package thorchain

import (
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// LuviMigrateMainnetGateSuite runs the pool-moving steps of the mainnet
// migration bodies through the LUVI migration gate (see luvi_migration_test.go)
// on their existing fixtures, with the pool depths set to mainnet's
// (2026-10-25) where the fixture's are arbitrary. Migrate19to20's mainnet body
// is gated off until its spec is re-pinned (mainnetDonationSpecComplete), so
// its steps run one by one in the migration's scope. The suite name contains
// "Migrate", so make test-network-specific's mainnet migration job runs it.
type LuviMigrateMainnetGateSuite struct{}

var _ = Suite(&LuviMigrateMainnetGateSuite{})

// luviSetMainnetDepth sets a fixture pool to mainnet depths, keeping the
// fixture's unaccounted vault asset (vault holdings above the pool depth,
// which the slash-refund solvency gates read) and funding the extra RUNE.
func luviSetMainnetDepth(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, runeDepth, assetDepth, lpUnits uint64) {
	k := mgr.Keeper()
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(pool.IsEmpty(), Equals, false)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	for _, v := range vaults {
		held := v.GetCoin(asset).Amount
		if held.IsZero() {
			continue
		}
		free := common.SafeSub(held, pool.BalanceAsset)
		v.SubFundsWithClamping(common.NewCoins(common.NewCoin(asset, held)))
		v.AddFunds(common.NewCoins(common.NewCoin(asset, free.AddUint64(assetDepth))))
		c.Assert(k.SetVault(ctx, v), IsNil)
		break
	}
	if extra := cosmos.NewUint(runeDepth); extra.GT(pool.BalanceRune) {
		FundModule(c, ctx, k, AsgardName, extra.Sub(pool.BalanceRune).Uint64())
	}
	pool.BalanceRune = cosmos.NewUint(runeDepth)
	pool.BalanceAsset = cosmos.NewUint(assetDepth)
	pool.LPUnits = cosmos.NewUint(lpUnits)
	c.Assert(k.SetPool(ctx, pool), IsNil)
}

// luviGateStep runs one migration step through the gate, checks it moved the
// pools it should (so the gate is not passing on a skipped step) and that it
// moved no LP value without a declaration.
func luviGateStep(c *C, ctx cosmos.Context, mgr *Mgrs, scope string, step func(sdk.Context) error, moved ...common.Asset) {
	k := mgr.Keeper()
	scopeCtx, s := beginLuviMigrationScope(ctx, k, scope)
	c.Assert(s, NotNil)
	c.Assert(step(scopeCtx), IsNil)
	changes := s.changes(scopeCtx, k)
	s.endMigration(scopeCtx, k)
	seen := map[string]bool{}
	for _, ch := range changes {
		seen[ch.Pool.String()] = true
		c.Logf("%s %s: normalized dG %s (rune %s -> %s, asset %s -> %s, units %s -> %s, synth %s -> %s)",
			scope, ch.Pool, ch.NormalizedDeltaG, ch.Pre.BalanceRune, ch.Post.BalanceRune,
			ch.Pre.BalanceAsset, ch.Post.BalanceAsset, ch.Pre.LPUnits, ch.Post.LPUnits, ch.Pre.SynthSupply, ch.Post.SynthSupply)
	}
	for _, a := range moved {
		c.Check(seen[a.String()], Equals, true, Commentf("%s did not change %s: the step was skipped", scope, a))
	}
	failures := luviMigrationGateFailures(scope, changes)
	c.Check(failures, HasLen, 0, Commentf("migration moved LP value without a luviDeclaredWriteDowns entry:\n%s", strings.Join(failures, "\n")))
}

func luviRunStep(run func(cosmos.Context, *Mgrs), mgr *Mgrs) func(sdk.Context) error {
	return func(ctx sdk.Context) error {
		run(ctx, mgr)
		return nil
	}
}

func (s *LuviMigrateMainnetGateSuite) TestMigrate19to20BTCSlashRefund(c *C) {
	ctx, mgr := slashSetup(c, slashAsgardToReserve, mainnetSlashRefunds19to20Total, slashPoolBTC+slashBTCCredit)
	luviSetMainnetDepth(c, ctx, mgr, common.BTCAsset, 2_325_345_610_724_736, 18_128_301_820, 196_412_898_527_083)
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(19), luviRunStep(runSlashRefund19to20, mgr), common.BTCAsset)
}

func (s *LuviMigrateMainnetGateSuite) TestMigrate19to20ZECSlashRefund(c *C) {
	ctx, mgr := zecSlashSetup(c)
	zecAddParkedOutbound(c, ctx, mgr, ctx.BlockHeight(), "thorpub1addwnpepqt3t6xzve6mukamkg6c70g64zy27x2606r74s950prsryg037j6gqr5qzgg")
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	luviSetMainnetDepth(c, ctx, mgr, zec, 4_390_134_862_351, 2_268_254_475, 3_956_605_864_403)
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(19), luviRunStep(runZecSlashRefund19to20, mgr), zec)
}

func (s *LuviMigrateMainnetGateSuite) TestMigrate19to20TRONSlashRefund(c *C) {
	ctx, mgr := tronSlashSetup(c)
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	trx, err := common.NewAsset(tronSlashTrxAsset)
	c.Assert(err, IsNil)
	luviSetMainnetDepth(c, ctx, mgr, usdt, 38_970_451_446_326, 24_342_349_603_401, 30_178_588_059_789)
	luviSetMainnetDepth(c, ctx, mgr, trx, 21_629_793_003_734, 40_830_349_468_649, 16_143_600_810_404)
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(19), luviRunStep(runTronSlashRefund19to20, mgr), usdt, trx)
}

func (s *LuviMigrateMainnetGateSuite) TestMigrate19to20SOLSlashRefund(c *C) {
	ctx, mgr := setupManagerForTest(c)
	sol, _ := solSlashPoolSetup(c, ctx, mgr, true)
	luviSetMainnetDepth(c, ctx, mgr, sol, 10_033_614_603_975, 56_763_884_528, 10_066_407_435_912)
	rFund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(solSlashBondRefundTotal))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, rFund), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(rFund)), IsNil)
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(19), luviRunStep(runSolSlashRefund19to20, mgr), sol)
}

func (s *LuviMigrateMainnetGateSuite) TestMigrate19to20EthThorRestore(c *C) {
	ctx, mgr, asset := etSetup(c)
	etAddVault(c, ctx, mgr, asset, etHeld(0))
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(19), luviRunStep(runEthThorRestore19to20, mgr), asset)
}

func (s *LuviMigrateMainnetGateSuite) TestMigrate19to20LowSynthBurn(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610)
	pool.BalanceRune = cosmos.NewUint(5_192_443_039_012)
	pool.LPUnits = cosmos.NewUint(10_955_814_979_723)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	coin := common.NewCoin(asset.GetSyntheticAsset(), cosmos.NewUint(17_932_761_109)) // the pinned supply
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(coin)), IsNil)
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(19), luviRunStep(runLowSynthBurn19to20, mgr), asset)
}

// TestMigrate20to21GaiaWritedown runs the whole registered Migrate20to21 on
// the GAIA write-down fixture (mainnet depths at h28,297,751).
func (s *LuviMigrateMainnetGateSuite) TestMigrate20to21GaiaWritedown(c *C) {
	f := setupGaiaWritedown20to21(c, gwVaultBooks, gwPoolRune, gwReserve)
	luviGateStep(c, f.ctx, f.mgr, luviMigrationScopeName(20), NewMigrator(f.mgr).Migrate20to21, common.ATOMAsset)
}
