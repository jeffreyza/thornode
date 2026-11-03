//go:build mainnet

package thorchain

import (
	"strconv"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type EthThorRestoreSuite struct{}

var _ = Suite(&EthThorRestoreSuite{})

// TestLowSynthBurnRetires proves the low-synth retirement: a low-synth-% pool's entire synth
// supply is reclaimed and burned and the pool's synth units are cleared.
func (s *EthThorRestoreSuite) TestLowSynthBurnRetires(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	// Real BSC.USDC asset depth so the pinned supply sits at ~0.26% utilization
	// (within the 1% eligibility bound) — the retire path.
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	pool.SynthUnits = cosmos.NewUint(5_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	holder := GetRandomBech32Addr()
	// exactly the pinned supply → within the ceiling.
	coin := common.NewCoin(synth, cosmos.NewUint(17_932_761_109))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, holder, common.NewCoins(coin)), IsNil)
	c.Assert(k.GetTotalSupply(ctx, synth).Equal(cosmos.NewUint(17_932_761_109)), Equals, true)

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	c.Check(k.GetTotalSupply(ctx, synth).IsZero(), Equals, true)
	p, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(p.SynthUnits.IsZero(), Equals, true)
	c.Check(k.GetBalanceOfModule(ctx, ModuleName, synth.Native()).IsZero(), Equals, true)
}

// TestLowSynthBurnSkipsAboveBoundByTruncation proves the exact (non-truncating) utilization
// gate (70942fc5bd): a pool whose live utilization is strictly above the 1% bound but by
// less than one basis point (here ~100.746 bps) must be SKIPPED. Dividing first floors the
// bps to 100 and the old `.GT(100)` check wrongly passed it, burning a pool above the bound.
func (s *EthThorRestoreSuite) TestLowSynthBurnSkipsAboveBoundByTruncation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	// supply 17_932_761_109 / (2·890_000_000_000) = 100.746 bps: above the 100-bps (1%)
	// bound, but it FLOORS to 100 — the truncation bug would burn it.
	pool.BalanceAsset = cosmos.NewUint(890_000_000_000)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	pool.SynthUnits = cosmos.NewUint(5_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Exactly the pinned supply, so the pinned-ceiling guard passes and only the
	// utilization bound decides.
	coin := common.NewCoin(synth, cosmos.NewUint(17_932_761_109))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(coin)), IsNil)

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	// Above the bound → skipped, supply untouched.
	c.Check(k.GetTotalSupply(ctx, synth).Equal(cosmos.NewUint(17_932_761_109)), Equals, true)
}

// TestLowSynthBurnSkipsNonAvailablePool proves the status gate (Itz T6/c): a pool that
// is not Available (here Staged, like mainnet ETH.YFI) is NOT burned, even within the
// pct + supply bounds and with no saver/queued-swap claim.
func (s *EthThorRestoreSuite) TestLowSynthBurnSkipsNonAvailablePool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolStaged // not Available → must be skipped
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	pool.SynthUnits = cosmos.NewUint(5_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	coin := common.NewCoin(synth, cosmos.NewUint(17_932_761_109)) // == pinned, within bounds
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(coin)), IsNil)

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	// supply untouched — a non-Available pool is not burned.
	c.Check(k.GetTotalSupply(ctx, synth).Equal(cosmos.NewUint(17_932_761_109)), Equals, true)
}

// TestLowSynthBurnSkipsRagnarokMarkerPool proves the ragnarok gate (e1aa374b): an
// Available pool still carrying a ragnarok marker makes GetTotalSupply read 0, so the
// supply + utilization guards pass against 0 — but BurnEntireSynthSupply destroys the
// real bank balances. The pool must be skipped. Asserted via the holder's real balance,
// since GetTotalSupply reads 0 here and cannot be used.
func (s *EthThorRestoreSuite) TestLowSynthBurnSkipsRagnarokMarkerPool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	// Available, but still carries a ragnarok marker → GetTotalSupply reads 0.
	k.SetPoolRagnarokStart(ctx, asset)

	// Real supply ABOVE the pinned ceiling (~18.11e9), pct ~59bps (<1%): absent the
	// ragnarok-zero bug the ceiling guard would skip it; with the bug GetTotalSupply=0
	// bypasses the guards and the real balance is burned.
	holder := GetRandomBech32Addr()
	amt := cosmos.NewUint(40_000_000_000)
	coin := common.NewCoin(synth, amt)
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, holder, common.NewCoins(coin)), IsNil)
	c.Assert(k.GetTotalSupply(ctx, synth).IsZero(), Equals, true) // marker makes supply read 0

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	// real balance untouched — the ragnarok-marked pool was skipped.
	c.Check(k.GetBalance(ctx, holder).AmountOf(synth.Native()).String(), Equals, amt.String(),
		Commentf("ragnarok-marked pool's synth was burned despite the marker"))
}

// TestLowSynthBurnSkipsPoolWithSaver proves the liability guard: a pool whose
// synth still has a saver record is NOT burned (its backing would be stranded).
func (s *EthThorRestoreSuite) TestLowSynthBurnSkipsPoolWithSaver(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	// in-bounds depth (~0.26% utilization) so ONLY the saver guard explains the skip —
	// a shallow pool would skip via the utilization bound regardless of the saver check.
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// mint synth supply that would otherwise be burned.
	coin := common.NewCoin(synth, cosmos.NewUint(17_932_761_109))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(coin)), IsNil)

	// a saver record on the synth asset → pool must be skipped.
	saver := LiquidityProvider{
		Asset:        synth,
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.NewUint(1_000),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, saver)

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	// supply untouched — the pool with a saver was skipped.
	c.Check(k.GetTotalSupply(ctx, synth).Equal(cosmos.NewUint(17_932_761_109)), Equals, true)
}

// TestLowSynthBurnSkipsDriftedPct proves the eligibility bound: a pool whose
// live synth utilization has drifted above 1% (here a shallow pool makes the
// pinned supply ~9%) is NOT burned, even with no saver and supply within the
// pinned ceiling.
func (s *EthThorRestoreSuite) TestLowSynthBurnSkipsDriftedPct(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	// Shallow asset depth → the pinned supply sits at ~8.97% utilization (> 1%).
	pool.BalanceAsset = cosmos.NewUint(100_000_000_000)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	coin := common.NewCoin(synth, cosmos.NewUint(17_932_761_109)) // == pinned → ceiling OK; pct is the trigger
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(coin)), IsNil)

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	// supply untouched — utilization above the eligibility bound.
	c.Check(k.GetTotalSupply(ctx, synth).Equal(cosmos.NewUint(17_932_761_109)), Equals, true)
}

// TestLowSynthBurnSkipsSupplyAbovePinned proves the ceiling bound is independent
// of the utilization bound: a pool within the 1% utilization bound but whose live
// supply has grown beyond the pinned ceiling (a completed purchase, no
// saver/swap trace) is NOT burned.
func (s *EthThorRestoreSuite) TestLowSynthBurnSkipsSupplyAbovePinned(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := asset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	// Real asset depth → ~0.27% utilization even at the grown supply (pct OK).
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// grown ~1.5% beyond the pinned 17_932_761_109 (ceiling is pinned + 1%).
	grown := cosmos.NewUint(18_200_000_000)
	coin := common.NewCoin(synth, grown)
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, GetRandomBech32Addr(), common.NewCoins(coin)), IsNil)

	c.Assert(applyLowSynthBurn19to20(ctx, mgr), IsNil)

	// supply untouched — live supply above the pinned ceiling.
	c.Check(k.GetTotalSupply(ctx, synth).Equal(grown), Equals, true)
}

// realistic snapshot magnitudes (1e8)
const (
	etPoolAsset = uint64(347_455_789_350_009) // 3,474,557.89 THOR
	etPoolRune  = uint64(25_680_109_935_069)  // 256,801.10 RUNE (pool at the pinned 0.073909 RUNE/THOR)
	etPoolUnits = uint64(17_133_622_931_493)
	etTrade     = uint64(45_858_713_692_198) // 458,587.14 THOR trade depth
	etSecured   = uint64(7_317_924_180_902)  // 73,179.24 THOR secured depth
)

func etSetup(c *C) (cosmos.Context, *Mgrs, common.Asset) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset(ethThorAssetStr)
	c.Assert(err, IsNil)

	lp := LiquidityProvider{
		Asset: asset, RuneAddress: common.Address(ethThorSecuredOps),
		Units: cosmos.NewUint(15_111_114_597_793), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		RuneDepositValue: cosmos.NewUint(ethThorRefundToNDT), AssetDepositValue: cosmos.NewUint(ethThorSurplus),
	}
	k.SetLiquidityProvider(ctx, lp)

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(etPoolAsset)
	pool.BalanceRune = cosmos.NewUint(etPoolRune)
	pool.LPUnits = cosmos.NewUint(etPoolUnits)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	k.SetTradeUnit(ctx, TradeUnit{Asset: asset.GetTradeAsset(), Depth: cosmos.NewUint(etTrade)})
	k.SetSecuredAsset(ctx, SecuredAsset{Asset: asset.GetSecuredAsset(), Depth: cosmos.NewUint(etSecured)})

	FundModule(c, ctx, k, AsgardName, 1_000_000_00000000)  // ample Asgard RUNE
	FundModule(c, ctx, k, ReserveName, 5_000_000_00000000) // ample Reserve
	return ctx, mgr, asset
}

// held so that unaccounted (held - pool - trade - secured) == surplus exactly.
func etHeld(delta int64) cosmos.Uint {
	base := etPoolAsset + ethThorSurplus + etTrade + etSecured
	if delta < 0 {
		return cosmos.NewUint(base - uint64(-delta))
	}
	return cosmos.NewUint(base + uint64(delta))
}

func etAddVault(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, held cosmos.Uint) {
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Coins = common.Coins{common.NewCoin(asset, held)}
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
}

func (s *EthThorRestoreSuite) TestEthThorRestoreHappyPath(c *C) {
	ctx, mgr, asset := etSetup(c)
	etAddVault(c, ctx, mgr, asset, etHeld(0)) // unaccounted == surplus
	k := mgr.Keeper()

	c.Assert(applyEthThorRestore19to20(ctx, mgr), IsNil)

	// LP donated.
	lp, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	c.Check(lp.Units.IsZero(), Equals, true)

	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	// asset credited by the full pinned surplus.
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset+ethThorSurplus)), Equals, true)
	// units reduced by the donated LP.
	c.Check(pool.LPUnits.Equal(cosmos.NewUint(etPoolUnits-15_111_114_597_793)), Equals, true)
	// RUNE depth lands at the pinned RUNE/THOR target (integer-exact).
	finalAsset := cosmos.NewUint(etPoolAsset + ethThorSurplus)
	wantRune := finalAsset.MulUint64(ethThorPinnedRuneTor).QuoUint64(100_000_000)
	c.Check(pool.BalanceRune.Equal(wantRune), Equals, true)
	c.Check(pool.BalanceRune.GT(cosmos.NewUint(etPoolRune)), Equals, true)

	// a pool_balance_change event is emitted so Midgard tracks the credited depth.
	found := false
	for _, e := range ctx.EventManager().Events() {
		if e.Type == "pool_balance_change" {
			found = true
		}
	}
	c.Check(found, Equals, true)

	// a withdraw event is emitted for the donated SecOps units so Midgard subtracts the
	// removed position (SetPool/RemoveLiquidityProvider change units silently).
	c.Check(ethThorWithdrawUnits(ctx, asset), Equals, "15111114597793",
		Commentf("no withdraw event for the donated SecOps units"))
	// Midgard pool-unit invariant: the stored LPUnits drop equals Σwithdraw. Ties the
	// unit mutation to the event so Midgard's pool units stay consistent (= Σstake − Σwithdraw).
	c.Check(cosmos.NewUint(etPoolUnits).Sub(pool.LPUnits).String(), Equals, ethThorWithdrawUnits(ctx, asset),
		Commentf("ΔpoolLPUnits must equal Σwithdraw"))
}

// ethThorWithdrawUnits returns the total liquidity_provider_units across all withdraw
// events emitted for the given pool (as a decimal string; "0" if none).
func ethThorWithdrawUnits(ctx cosmos.Context, pool common.Asset) string {
	total := cosmos.ZeroUint()
	for _, e := range ctx.EventManager().Events() {
		if e.Type != "withdraw" {
			continue
		}
		var p, u string
		for _, a := range e.Attributes {
			switch a.Key {
			case "pool":
				p = a.Value
			case "liquidity_provider_units":
				u = a.Value
			}
		}
		if p == pool.String() && u != "" {
			total = total.Add(cosmos.NewUintFromString(u))
		}
	}
	return total.String()
}

// The pin is a FIXED recovery price above market, so there is no live-price band; the Reserve
// pairing is CLAMPED at a hard cap (huginn 0c3a3071d1), not aborted. A pool whose BalanceAsset
// was inflated (or that simply grew) raises runeToAdd above the cap, so it is clamped and the
// restore STILL proceeds — the pool lands below the pin, but the Reserve pays exactly the cap.
// Here the asset depth is doubled (vault held raised so unaccounted still == surplus and the
// credit guard passes), so the uncapped runeToAdd (~509k) is clamped to 375k.
func (s *EthThorRestoreSuite) TestEthThorRestoreClampsRunePairingToCap(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	extra := cosmos.NewUint(etPoolAsset) // double the asset depth → uncapped runeToAdd > cap
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = pool.BalanceAsset.Add(extra)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	etAddVault(c, ctx, mgr, asset, etHeld(0).Add(extra)) // keep unaccounted == surplus

	c.Assert(applyEthThorRestore19to20(ctx, mgr), IsNil)

	// restore proceeded; RUNE paired is exactly the cap (not the uncapped ~509k).
	p, gErr := k.GetPool(ctx, asset)
	c.Assert(gErr, IsNil)
	c.Check(p.BalanceRune.Equal(cosmos.NewUint(etPoolRune).Add(cosmos.NewUint(ethThorMaxRunePairing))), Equals, true,
		Commentf("Reserve pairing clamped to exactly the cap"))
	// asset depth still credited by the pinned surplus.
	c.Check(p.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset).Add(extra).Add(cosmos.NewUint(ethThorSurplus))), Equals, true)
}

// If the pool's current RUNE already meets or exceeds the pinned target (RUNE "too strong"
// relative to the fixed pin), runeToAdd would be zero/negative — the step skips rather than
// removing RUNE. Here pool RUNE is set above finalAsset×pin.
func (s *EthThorRestoreSuite) TestEthThorRestoreSkipsWhenRuneTooStrong(c *C) {
	ctx, mgr, asset := etSetup(c)
	etAddVault(c, ctx, mgr, asset, etHeld(0))
	k := mgr.Keeper()
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	pool.BalanceRune = cosmos.NewUint(60_000_000_000_000) // 600,000 RUNE > finalAsset×pin (~494.5k)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	c.Assert(applyEthThorRestore19to20(ctx, mgr), ErrorMatches, ".*RUNE too strong.*")
}

// A zero-unit pending-only record (the first leg of a staged symmetric deposit)
// made after the snapshot must NOT abort the restore — it owns no pool units, so
// the uncapped-share concern does not apply.
func (s *EthThorRestoreSuite) TestEthThorRestoreAllowsPendingOnlyLateLP(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	pending := LiquidityProvider{
		Asset: asset, RuneAddress: GetRandomRUNEAddress(),
		Units: cosmos.ZeroUint(), PendingRune: cosmos.NewUint(1_000_000), PendingAsset: cosmos.ZeroUint(),
		LastAddHeight: ethThorSnapshotHeight + 1,
	}
	k.SetLiquidityProvider(ctx, pending)
	etAddVault(c, ctx, mgr, asset, etHeld(0))

	// must not abort on the ownership guard.
	c.Assert(applyEthThorRestore19to20(ctx, mgr), IsNil)

	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset+ethThorSurplus)), Equals, true)
	// the pending record is untouched.
	got, err := k.GetLiquidityProvider(ctx, asset, pending.RuneAddress)
	c.Assert(err, IsNil)
	c.Check(got.Units.IsZero(), Equals, true)
	c.Check(got.PendingRune.Equal(cosmos.NewUint(1_000_000)), Equals, true)
}

// Mirror the pinned consts inside applySlashRefund19to20 (function-local, so not
// importable). Kept in sync by TestSlashRefundHappyPath's assertions.
const (
	slashBTCCredit       = uint64(53_257_717)        // +0.53257717 BTC into the pool (send only; 1000-sat gas left debited)
	slashRuneDebit       = uint64(5_677_363_556_220) // -56,773.63556220 RUNE from the pool
	slashAsgardToReserve = uint64(5_677_363_556_220) // freed RUNE moved Asgard->Reserve
	slashPoolBTC         = uint64(500_000_000)       // arbitrary base BTC pool depth
)

func slashSetup(c *C, asgardFund, reserveFund, vaultBTC uint64) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	// Zero any pre-seeded module RUNE so the funded amounts are exact (the test
	// setup otherwise leaves a baseline that makes an additive "short" fund pass).
	for _, m := range []string{AsgardName, ReserveName} {
		if bal := k.GetRuneBalanceOfModule(ctx, m); !bal.IsZero() {
			c.Assert(k.SendFromModuleToModule(ctx, m, ModuleName,
				common.NewCoins(common.NewCoin(common.RuneAsset(), bal))), IsNil)
		}
	}

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(slashPoolBTC)
	pool.BalanceRune = cosmos.NewUint(slashRuneDebit + 100*common.One) // margin above the debit
	pool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(vaultBTC))}
	c.Assert(k.SetVault(ctx, v), IsNil)

	FundModule(c, ctx, k, AsgardName, asgardFund)
	FundModule(c, ctx, k, ReserveName, reserveFund)
	return ctx, mgr
}

// TestSlashRefundHappyPath proves the slash reversal: BTC credited / RUNE debited
// in the pool, the freed RUNE moved Asgard->Reserve, and the 218 refunds paid —
// gated by the same unaccounted-BTC solvency check as the other credits.
func (s *EthThorRestoreSuite) TestSlashRefundHappyPath(c *C) {
	total := mainnetSlashRefunds19to20Total
	ctx, mgr := slashSetup(c, slashAsgardToReserve, total, slashPoolBTC+slashBTCCredit) // free BTC == the credit
	k := mgr.Keeper()

	free, err := unaccountedVaultAsset19to20(ctx, mgr, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(free.Equal(cosmos.NewUint(slashBTCCredit)), Equals, true)

	reserve0 := k.GetRuneBalanceOfModule(ctx, ReserveName)
	asgard0 := k.GetRuneBalanceOfModule(ctx, AsgardName)

	c.Assert(applySlashRefund19to20(ctx, mgr), IsNil)

	pool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(slashPoolBTC+slashBTCCredit)), Equals, true)
	c.Check(pool.BalanceRune.Equal(cosmos.NewUint(100*common.One)), Equals, true)
	// Reserve: +asgardToReserve from the move, -total paid out.
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve0.AddUint64(slashAsgardToReserve).SubUint64(total)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgard0.SubUint64(slashAsgardToReserve)), Equals, true)
	// first + last recipient paid exactly (not all 218).
	for _, idx := range []int{0, len(mainnetSlashRefunds19to20) - 1} {
		r := mainnetSlashRefunds19to20[idx]
		to, err := cosmos.AccAddressFromBech32(r.address)
		c.Assert(err, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).Uint64(), Equals, r.amount)
	}
}

// TestSlashRefundRollsBackOnFailedMove proves the all-or-nothing wrapper: the
// Asgard->Reserve move fails (Asgard short) AFTER the BTC pool has already been
// credited/debited, so the whole step's cache is discarded — pool, module
// balances and recipients all untouched.
func (s *EthThorRestoreSuite) TestSlashRefundRollsBackOnFailedMove(c *C) {
	total := mainnetSlashRefunds19to20Total
	// Asgard one short of the move amount → SendFromModuleToModule fails post-mutation.
	ctx, mgr := slashSetup(c, slashAsgardToReserve-1, total, slashPoolBTC+slashBTCCredit)
	k := mgr.Keeper()

	pool0, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	reserve0 := k.GetRuneBalanceOfModule(ctx, ReserveName)
	asgard0 := k.GetRuneBalanceOfModule(ctx, AsgardName)
	r0 := mainnetSlashRefunds19to20[0]
	to0, err := cosmos.AccAddressFromBech32(r0.address)
	c.Assert(err, IsNil)

	runSlashRefund19to20(ctx, mgr) // wrapper swallows the error + discards the cache

	pool1, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool1.BalanceAsset.Equal(pool0.BalanceAsset), Equals, true)
	c.Check(pool1.BalanceRune.Equal(pool0.BalanceRune), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve0), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgard0), Equals, true)
	c.Check(k.GetBalance(ctx, to0).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)
}

// TestSlashRefundRefusesUnaccountedBTC proves the solvency gate: if the active
// vaults do not hold the reversed BTC free, the step aborts rather than claim BTC
// the vaults don't have while the Reserve still pays out.
func (s *EthThorRestoreSuite) TestSlashRefundRefusesUnaccountedBTC(c *C) {
	total := mainnetSlashRefunds19to20Total
	ctx, mgr := slashSetup(c, slashAsgardToReserve, total, slashPoolBTC) // vault holds only the pool's BTC → zero free
	c.Assert(applySlashRefund19to20(ctx, mgr), ErrorMatches, ".*unaccounted BTC.*")
}

// TestEthThorRestoreCapsPostDrainLP proves the value cap: the later entrant
// (thor1aff7) is capped to the RUNE value of min(contributed, pre-upgrade redeemable)
// — keeping its own RUNE — and the rest accrues to the earlier LPs.
func (s *EthThorRestoreSuite) TestEthThorRestoreCapsPostDrainLP(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	affUnits := cosmos.NewUint(1_067_052_797_842)
	// thor1aff7 is a real later LP record inside the pre-existing pool units. It is
	// TWO-SIDED — it contributed both THOR and RUNE — which is exactly why the cap is on
	// VALUE, not contributed asset alone: an asset-only cap pins the THOR side and lets
	// the RUNE side fall out at the restored (RUNE-cheap) price, stripping the LP's own
	// RUNE contribution. The value cap holds it to the RUNE value of min(contributed,
	// pre-upgrade redeemable) so its RUNE is kept.
	const affRuneDeposit = uint64(1_451_833_000_000) // 14,518.33 RUNE
	aff := LiquidityProvider{
		Asset: asset, RuneAddress: common.Address(postDrainLPAddr),
		Units: affUnits, PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		AssetDepositValue: cosmos.NewUint(postDrainLPContribAsset),
		RuneDepositValue:  cosmos.NewUint(affRuneDeposit), LastAddHeight: ethThorSnapshotHeight + 1,
	}
	k.SetLiquidityProvider(ctx, aff)
	etAddVault(c, ctx, mgr, asset, etHeld(0))

	c.Assert(applyEthThorRestore19to20(ctx, mgr), IsNil)

	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	got, err := k.GetLiquidityProvider(ctx, asset, common.Address(postDrainLPAddr))
	c.Assert(err, IsNil)
	// capped below its original units.
	c.Check(got.Units.LT(affUnits), Equals, true)
	// The cap only removes units; it never touches pool asset or RUNE depth.
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset+ethThorSurplus)), Equals, true)

	// The value cap holds the capped position to target = min(contributed value,
	// pre-upgrade redeemable value), both in RUNE at the FINAL pool price. Mirror the
	// production math exactly; pre-depths are the LIVE pre-migration pool (etSetup).
	contribVal := cosmos.NewUint(postDrainLPContribAsset).Mul(pool.BalanceRune).Quo(pool.BalanceAsset).Add(cosmos.NewUint(affRuneDeposit))
	preShareAsset := cosmos.NewUint(etPoolAsset).Mul(affUnits).Quo(cosmos.NewUint(etPoolUnits))
	preShareRune := cosmos.NewUint(etPoolRune).Mul(affUnits).Quo(cosmos.NewUint(etPoolUnits))
	beforeVal := preShareAsset.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).Add(preShareRune)
	target := contribVal
	if beforeVal.LT(target) {
		target = beforeVal
	}
	claimVal := pool.BalanceRune.MulUint64(2).Mul(got.Units).Quo(pool.LPUnits)
	// No windfall: value claim never exceeds the target.
	c.Check(claimVal.LTE(target), Equals, true,
		Commentf("value claim %s exceeds target %s", claimVal, target))
	// Not stripped: the cap binds AT the target (integer truncation only lands it a few
	// base units below), so the two-sided LP keeps its RUNE value — unlike the asset-only
	// cap, whose target would have been 2*contribAsset*price (RUNE deposit dropped).
	shortfall := common.SafeSub(target, claimVal)
	c.Check(shortfall.LT(cosmos.NewUint(1_000_000)), Equals, true,
		Commentf("value claim %s too far below target %s", claimVal, target))

	// Every unit removed from the capped LP leaves the pool: final pool units equal the
	// other LPs' units (post-donation pool units − aff7's original units) + aff7's new.
	uOther := cosmos.NewUint(etPoolUnits - 15_111_114_597_793).Sub(affUnits)
	c.Check(pool.LPUnits.Equal(uOther.Add(got.Units)), Equals, true,
		Commentf("pool units %s != uOther %s + capped %s", pool.LPUnits, uOther, got.Units))

	// Keeps its own RUNE: the value cap leaves this two-sided LP strictly MORE units than
	// the old asset-only cap would have. Asset-only pinned the THOR side to the full
	// contributed asset (claim worth 2*cBasis*price — half of it RUNE it never deposited,
	// a 2x over-credit for a pure-asset LP) while here the LP's own RUNE deposit pushes
	// its value target above cBasis*price, so it retains more units than that formula.
	cBasis := cosmos.NewUint(postDrainLPContribAsset)
	assetOnly := cBasis.Mul(uOther).Quo(cosmos.NewUint(etPoolAsset + ethThorSurplus).Sub(cBasis))
	c.Check(got.Units.GT(assetOnly), Equals, true,
		Commentf("value-cap units %s not greater than asset-only cap %s (RUNE deposit not credited)", got.Units, assetOnly))
}

// TestEthThorRestoreRejectsOverCredit proves the insolvency guard: when the
// unaccounted surplus is even 1 base unit below the pinned amount (trade/secured
// drifted), the step must skip and change NOTHING.
func (s *EthThorRestoreSuite) TestEthThorRestoreRejectsOverCredit(c *C) {
	ctx, mgr, asset := etSetup(c)
	etAddVault(c, ctx, mgr, asset, etHeld(-1)) // unaccounted == surplus-1  → must reject
	k := mgr.Keeper()

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	runEthThorRestore19to20(ctx, mgr) // soft-skip wrapper: on guard-fail, atomic no-op

	// Nothing changed.
	lp, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	c.Check(lp.Units.Equal(cosmos.NewUint(15_111_114_597_793)), Equals, true)
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset)), Equals, true)
	c.Check(pool.BalanceRune.Equal(cosmos.NewUint(etPoolRune)), Equals, true)
	c.Check(pool.LPUnits.Equal(cosmos.NewUint(etPoolUnits)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

// etRuneToAdd is the deterministic RUNE the restore pairs into the pool.
func etRuneToAdd() cosmos.Uint {
	finalAsset := cosmos.NewUint(etPoolAsset + ethThorSurplus)
	wantRune := finalAsset.MulUint64(ethThorPinnedRuneTor).QuoUint64(100_000_000)
	return wantRune.Sub(cosmos.NewUint(etPoolRune))
}

// TestEthThorRestorePaysLiveRefund proves the NDT repayment uses the LIVE
// RuneDepositValue, not the pinned constant: a lower live value (pre-upgrade
// withdrawal) is paid as-is, so the Reserve is debited runeToAdd + liveRefund.
func (s *EthThorRestoreSuite) TestEthThorRestorePaysLiveRefund(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	liveRefund := cosmos.NewUint(ethThorRefundToNDT).QuoUint64(2) // withdrew half
	lp, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	lp.RuneDepositValue = liveRefund
	k.SetLiquidityProvider(ctx, lp)
	etAddVault(c, ctx, mgr, asset, etHeld(0))

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(applyEthThorRestore19to20(ctx, mgr), IsNil)
	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)

	// Reserve debit == pairing RUNE + the LIVE refund (NOT the pinned refund).
	debit := reserveBefore.Sub(reserveAfter)
	c.Check(debit.Equal(etRuneToAdd().Add(liveRefund)), Equals, true,
		Commentf("debit %s != runeToAdd+liveRefund %s", debit, etRuneToAdd().Add(liveRefund)))
	c.Check(debit.Equal(etRuneToAdd().Add(cosmos.NewUint(ethThorRefundToNDT))), Equals, false) // not the pinned amount
}

// TestEthThorRestoreRefusesOverPay proves the one-directional tripwire: a live
// RuneDepositValue grown beyond the pinned expectation + tolerance aborts the
// whole step (never over-pay), changing nothing.
func (s *EthThorRestoreSuite) TestEthThorRestoreRefusesOverPay(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	lp, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	lp.RuneDepositValue = cosmos.NewUint(ethThorRefundToNDT).MulUint64(2) // far above pinned
	k.SetLiquidityProvider(ctx, lp)
	etAddVault(c, ctx, mgr, asset, etHeld(0))

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	runEthThorRestore19to20(ctx, mgr) // soft-skip: atomic no-op on tripwire

	stillThere, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	c.Check(stillThere.Units.Equal(cosmos.NewUint(15_111_114_597_793)), Equals, true)
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

// TestCapPostDrainLPSoleLPAborts proves the uOther==0 edge: if the capped
// entrant is the only remaining LP (earlier LPs withdrew), the cap aborts
// rather than zeroing it.
func (s *EthThorRestoreSuite) TestCapPostDrainLPSoleLPAborts(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset(ethThorAssetStr)
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(postDrainLPContribAsset).MulUint64(2)
	pool.BalanceRune = cosmos.NewUint(1_000_000_000)
	pool.LPUnits = cosmos.NewUint(1_067_052_797_842) // equals the aff LP → sole LP
	c.Assert(k.SetPool(ctx, pool), IsNil)
	aff := LiquidityProvider{
		Asset: asset, RuneAddress: common.Address(postDrainLPAddr),
		Units: cosmos.NewUint(1_067_052_797_842), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		AssetDepositValue: cosmos.NewUint(postDrainLPContribAsset), LastAddHeight: ethThorSnapshotHeight + 1,
	}
	k.SetLiquidityProvider(ctx, aff)

	c.Check(capLateLPs19to20(ctx, mgr, asset, ethThorPinnedLateLP, pool.BalanceAsset, pool.BalanceRune, pool.LPUnits), NotNil) // sole LP → abort, not zeroed
}

// TestEthThorRestoreAbortsUnpinnedLateLP proves R11's "keep abort for real
// inconsistency": a late LP that is NOT in the pinned set (incomplete pin) cannot be
// classified, so the step soft-skips (atomic no-op) rather than risk stripping a
// pre-snapshot owner or over-crediting a brand-new one. (A KNOWN/pinned late add is
// capped and proceeds — see the two-entrant test.)
func (s *EthThorRestoreSuite) TestEthThorRestoreAbortsUnpinnedLateLP(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	intruder := LiquidityProvider{
		Asset: asset, RuneAddress: GetRandomTHORAddress(), // NOT in ethThorPinnedLateLP
		Units: cosmos.NewUint(500_000_000_000), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		AssetDepositValue: cosmos.NewUint(1_000_000), LastAddHeight: ethThorSnapshotHeight + 10,
	}
	k.SetLiquidityProvider(ctx, intruder)
	etAddVault(c, ctx, mgr, asset, etHeld(0))

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	runEthThorRestore19to20(ctx, mgr) // soft-skip wrapper: unpinned late LP → atomic no-op

	// Nothing changed.
	secops, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	c.Check(secops.Units.Equal(cosmos.NewUint(15_111_114_597_793)), Equals, true)
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(etPoolAsset)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

// TestCapLateLPsFloorsToppedUpPreSnapshot proves the pinned-floor path: a
// pre-snapshot LP that topped up after the snapshot is capped DOWN to its pinned
// snapshot units (keeping its pre-snapshot position), not to its deposit value.
func (s *EthThorRestoreSuite) TestCapLateLPsFloorsToppedUpPreSnapshot(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	// SecOps is donated before the cap in the real flow; drop it here so this
	// direct capLateLPs call exercises only the floored LP.
	secops, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	k.RemoveLiquidityProvider(ctx, secops)
	addr := GetRandomTHORAddress()
	const floor = uint64(300_000_000_000)
	topped := LiquidityProvider{
		Asset: asset, RuneAddress: addr,
		Units: cosmos.NewUint(500_000_000_000), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		AssetDepositValue: cosmos.NewUint(5_000_000_000), LastAddHeight: ethThorSnapshotHeight + 5,
	}
	k.SetLiquidityProvider(ctx, topped)
	etAddVault(c, ctx, mgr, asset, etHeld(0))

	pinned := map[string]ethThorLatePin{addr.String(): {floorUnits: floor}}
	pre, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(capLateLPs19to20(ctx, mgr, asset, pinned, pre.BalanceAsset, pre.BalanceRune, pre.LPUnits), IsNil)

	got, err := k.GetLiquidityProvider(ctx, asset, addr)
	c.Assert(err, IsNil)
	c.Check(got.Units.Equal(cosmos.NewUint(floor)), Equals, true,
		Commentf("floored units %s != pinned floor %d", got.Units, floor))
}

// TestCapLateLPsTwoEntrantsBoundedClaims proves the joint cap (R11 re-review A + the
// closed-form fix): two pinned brand-new entrants with NON-DUST targets (each ~2.3% of
// 2*poolRune, so Σ targets ≈ 4.6% — strong coupling that made the old Gauss-Seidel loop
// return "did not converge" and abort the whole restore) are BOTH capped jointly, the
// step succeeds, and each one's FINAL value claim (against the final pool units, after
// both caps) binds AT its target, not merely below some loose bound.
func (s *EthThorRestoreSuite) TestCapLateLPsTwoEntrantsBoundedClaims(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	secops, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	k.RemoveLiquidityProvider(ctx, secops) // donated before the cap in the real flow

	const u0 = uint64(400_000_000_000)
	pinned := map[string]ethThorLatePin{}
	addrs := []common.Address{}
	// Huge contributed asset so the binding target is the pre-upgrade redeem value
	// (beforeVal), a non-dust fraction of the pool — the coupling the old loop choked on.
	for i := 0; i < 2; i++ {
		addr := GetRandomTHORAddress()
		lp := LiquidityProvider{
			Asset: asset, RuneAddress: addr,
			Units: cosmos.NewUint(u0), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
			AssetDepositValue: cosmos.NewUint(20_000_000_000_000), LastAddHeight: ethThorSnapshotHeight + 7,
		}
		k.SetLiquidityProvider(ctx, lp)
		pinned[addr.String()] = ethThorLatePin{floorUnits: 0} // pinned brand-new
		addrs = append(addrs, addr)
	}

	pre, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(capLateLPs19to20(ctx, mgr, asset, pinned, pre.BalanceAsset, pre.BalanceRune, pre.LPUnits), IsNil)

	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	twoR := pool.BalanceRune.MulUint64(2)
	// target = beforeVal: pre-upgrade redeem of u0 units at the final price (both
	// entrants identical). Non-dust (~2.3% of 2R).
	preShareAsset := pre.BalanceAsset.Mul(cosmos.NewUint(u0)).Quo(pre.LPUnits)
	preShareRune := pre.BalanceRune.Mul(cosmos.NewUint(u0)).Quo(pre.LPUnits)
	target := preShareAsset.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).Add(preShareRune)
	c.Check(target.GT(twoR.QuoUint64(100)), Equals, true, Commentf("target %s should be non-dust (>1%% of 2R %s)", target, twoR))
	capped := 0
	for _, addr := range addrs {
		lp, err := k.GetLiquidityProvider(ctx, asset, addr)
		c.Assert(err, IsNil)
		c.Check(lp.Units.LT(cosmos.NewUint(u0)), Equals, true)
		claimVal := twoR.Mul(lp.Units).Quo(pool.LPUnits)
		c.Check(claimVal.LTE(target), Equals, true, Commentf("claim %s exceeds target %s", claimVal, target))
		// binds AT the target (joint solve exact; integer floor lands it just below).
		c.Check(common.SafeSub(target, claimVal).LT(cosmos.NewUint(1_000_000)), Equals, true,
			Commentf("claim %s not bound at target %s", claimVal, target))
		capped++
	}
	c.Check(capped, Equals, 2)
}

// TestCapLateLPsAsymmetricEntrantsRounding proves the closed-form cap + the
// rounding-tolerant post-condition for ASYMMETRIC entrants (different units + targets):
// flooring each one's units independently lowers the shared denominator unevenly, which
// could nudge an entrant's integer claim a hair over its target — the post-condition
// tolerates that bounded rounding instead of wrongly aborting a valid restore.
func (s *EthThorRestoreSuite) TestCapLateLPsAsymmetricEntrantsRounding(c *C) {
	ctx, mgr, asset := etSetup(c)
	k := mgr.Keeper()
	secops, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	c.Assert(err, IsNil)
	k.RemoveLiquidityProvider(ctx, secops)

	// Override the pool so the two entrants are capped down to a small fraction of their
	// units: that is the regime where independent integer flooring of each entrant's
	// units, which shrinks the SHARED final denominator L, pushes one entrant's integer
	// claim strictly OVER its target (a pure rounding artifact). A price-1 pool (R==A)
	// keeps the arithmetic easy to follow: contributed value == asset_deposit_value.
	const poolR, poolA, poolU = uint64(1_000_000_000), uint64(1_000_000_000), uint64(10_000_000)
	pool, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	pool.BalanceRune = cosmos.NewUint(poolR)
	pool.BalanceAsset = cosmos.NewUint(poolA)
	pool.LPUnits = cosmos.NewUint(poolU)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	type ent struct {
		units uint64
		cv    uint64 // asset_deposit_value == contributed RUNE value at price 1
	}
	// Asymmetric, non-dust: entrant 0 is the one whose integer claim overshoots its
	// target by a few units once L is floored (verified: claims 240000088 vs target
	// 240000060, +28). entrant 1 undershoots — asymmetry is the point.
	entrants := []ent{{4_000_001, 240_000_060}, {5_500_003, 660_000_360}}
	pinned := map[string]ethThorLatePin{}
	type want struct {
		units, cv uint64
	}
	byAddr := map[string]want{}
	for _, e := range entrants {
		addr := GetRandomTHORAddress()
		lp := LiquidityProvider{
			Asset: asset, RuneAddress: addr,
			Units: cosmos.NewUint(e.units), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
			AssetDepositValue: cosmos.NewUint(e.cv), RuneDepositValue: cosmos.ZeroUint(),
			LastAddHeight: ethThorSnapshotHeight + 7,
		}
		k.SetLiquidityProvider(ctx, lp)
		pinned[addr.String()] = ethThorLatePin{floorUnits: 0} // brand-new: value-cap path
		byAddr[addr.String()] = want(e)
	}

	pre, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	// Must NOT abort: the rounding-tolerant post-condition accepts the bounded overshoot.
	c.Assert(capLateLPs19to20(ctx, mgr, asset, pinned, pre.BalanceAsset, pre.BalanceRune, pre.LPUnits), IsNil)

	pool, err = k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	twoR := pool.BalanceRune.MulUint64(2)
	// Mirror the production bound so the assertion tracks the code, not a magic slack.
	roundTol := twoR.MulUint64(uint64(len(entrants))).Quo(pool.LPUnits).AddUint64(1)
	anyStrictOver := false
	totalRemoved := cosmos.ZeroUint()
	for addrStr, w := range byAddr {
		lp, err := k.GetLiquidityProvider(ctx, asset, common.Address(addrStr))
		c.Assert(err, IsNil)
		c.Check(lp.Units.LT(cosmos.NewUint(w.units)), Equals, true) // reduced
		totalRemoved = totalRemoved.Add(cosmos.NewUint(w.units).Sub(lp.Units))
		preShareAsset := pre.BalanceAsset.Mul(cosmos.NewUint(w.units)).Quo(pre.LPUnits)
		preShareRune := pre.BalanceRune.Mul(cosmos.NewUint(w.units)).Quo(pre.LPUnits)
		before := preShareAsset.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).Add(preShareRune)
		contrib := cosmos.NewUint(w.cv).Mul(pool.BalanceRune).Quo(pool.BalanceAsset)
		target := contrib
		if before.LT(target) {
			target = before
		}
		claim := twoR.Mul(lp.Units).Quo(pool.LPUnits)
		if claim.GT(target) {
			anyStrictOver = true // the exact case roundTol=0 would wrongly reject
		}
		c.Check(claim.LTE(target.Add(roundTol)), Equals, true,
			Commentf("entrant %s claim %s exceeds target %s + tol %s", addrStr, claim, target, roundTol))
	}
	// Positive control: at least one entrant's integer claim strictly exceeds its target,
	// so this test actually exercises the tolerance (not a no-op where claim<=target).
	c.Check(anyStrictOver, Equals, true)

	// Every capped entrant must emit a withdraw event for its unit reduction, so
	// Midgard's member-position accounting tracks the mutation (SetLiquidityProvider
	// alone emits nothing). The summed withdraw units equal the total units removed.
	c.Check(ethThorWithdrawUnits(ctx, asset), Equals, totalRemoved.String(),
		Commentf("withdraw events must sum to the total capped units"))

	// The load-bearing Midgard invariant: pool units = Σstake − Σwithdraw. The stored
	// LPUnits drop across the cap must equal the summed withdraw units, or Midgard's
	// pool-unit view drifts. This also fails loud if any LP's units ROSE (which would
	// need a stake event, not a withdraw) — here every entrant is capped down.
	c.Check(pre.LPUnits.Sub(pool.LPUnits).String(), Equals, ethThorWithdrawUnits(ctx, asset),
		Commentf("ΔpoolLPUnits must equal Σwithdraw (Midgard pool-unit invariant)"))
}

// -------------------------------------------------------------------------------------
// Step 5 — erroneous ZEC slash refund (issue 3393)
// -------------------------------------------------------------------------------------

// zecSlashSetup builds the pre-reversal state WITHOUT the parked outbound: the ZEC pool
// with the slash RUNE to remove, and Asgard/Reserve funded so the Asgard->Reserve move
// and the per-provider Reserve payouts succeed. The parked outbound is added separately
// (zecAddParkedOutbound) so tests place it at a height inside the cancel's bounded scan
// window (near ctx.BlockHeight()) — or omit it.
func zecSlashSetup(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	for _, m := range []string{AsgardName, ReserveName} {
		if bal := k.GetRuneBalanceOfModule(ctx, m); !bal.IsZero() {
			c.Assert(k.SendFromModuleToModule(ctx, m, ModuleName,
				common.NewCoins(common.NewCoin(common.RuneAsset(), bal))), IsNil)
		}
	}
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = zec
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(900_000_000)
	pool.BalanceRune = cosmos.NewUint(zecSlashPoolRuneDebit + 100*common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	FundModule(c, ctx, k, AsgardName, zecSlashAsgardToReserve+100*common.One)
	FundModule(c, ctx, k, ReserveName, zecSlashBondRefundTotal+100*common.One)
	// An active Asgard vault holding exactly pool depth + the slash's double-debited coin:
	// the erroneous slash under-booked the pool by zecSlashPoolAssetCredit while the vault
	// still paid one outbound, so the solvency gate sees that headroom and passes. Tests
	// that exercise the retry-also-paid skip lower this below pool depth.
	zecVault := GetRandomVault()
	zecVault.Type = AsgardVault
	zecVault.Status = ActiveVault
	zecVault.Coins = common.NewCoins(common.NewCoin(zec, pool.BalanceAsset.AddUint64(zecSlashPoolAssetCredit)))
	c.Assert(k.SetVault(ctx, zecVault), IsNil)
	// HaltSigningZEC is the load-bearing precondition the refund now enforces.
	k.SetMimir(ctx, "HaltSigningZEC", 1)
	return ctx, mgr
}

// zecAddParkedOutbound schedules the already-paid ZEC outbound (empty OutHash) at the
// given height + vault, so the cancel's in_hash scan has something to find.
func zecAddParkedOutbound(c *C, ctx cosmos.Context, mgr *Mgrs, height int64, vaultPK string) {
	k := mgr.Keeper()
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	block := TxOut{Height: height, TxArray: []TxOutItem{{
		Chain:       common.Chain("ZEC"),
		ToAddress:   common.Address(zecSlashToAddress),
		VaultPubKey: common.PubKey(vaultPK),
		Coin:        common.NewCoin(zec, cosmos.NewUint(3_909_313)),
		InHash:      common.TxID(zecSlashInHash),
		Memo:        "OUT:" + zecSlashInHash,
	}}}
	c.Assert(k.SetTxOut(ctx, &block), IsNil)
}

func (s *EthThorRestoreSuite) TestZecSlashRefundHappyPath(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	parkHeight := ctx.BlockHeight() // inside the bounded cancel window
	zecAddParkedOutbound(c, ctx, mgr, parkHeight, "thorpub1addwnpepqt3t6xzve6mukamkg6c70g64zy27x2606r74s950prsryg037j6gqr5qzgg")
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	poolBefore, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	asgard0 := k.GetRuneBalanceOfModule(ctx, AsgardName)
	reserve0 := k.GetRuneBalanceOfModule(ctx, ReserveName)

	runZecSlashRefund19to20(ctx, mgr) // wrapper: cancel (own cacheCtx) + refund (own cacheCtx)

	// pool deltas reversed: coin-only ZEC credit (gas stays with the pool), RUNE removed.
	pool, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(poolBefore.BalanceAsset.Add(cosmos.NewUint(zecSlashPoolAssetCredit))), Equals, true)
	c.Check(pool.BalanceRune.Equal(common.SafeSub(poolBefore.BalanceRune, cosmos.NewUint(zecSlashPoolRuneDebit))), Equals, true)
	// Asgard->Reserve freed the pool-reimburse half; Reserve then paid out the total.
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(common.SafeSub(asgard0, cosmos.NewUint(zecSlashAsgardToReserve))), Equals, true)
	expReserve := common.SafeSub(reserve0.Add(cosmos.NewUint(zecSlashAsgardToReserve)), cosmos.NewUint(zecSlashBondRefundTotal))
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(expReserve), Equals, true)
	// every affected bond provider received exactly its share as liquid RUNE.
	for _, r := range zecSlashBondRefunds19to20 {
		to, addrErr := cosmos.AccAddressFromBech32(r.address)
		c.Assert(addrErr, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).String(), Equals, cosmos.NewUint(r.amount).String(), Commentf("bp %s", r.address))
	}
	// parked outbound cancelled.
	block, err := k.GetTxOut(ctx, parkHeight)
	c.Assert(err, IsNil)
	c.Check(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
}

// A txout batch in the window that cannot be decoded (store corruption) must ABORT the
// whole step: the scan is incomplete, so an uninspected height could hold a settled retry
// or another live copy. The cancel cache must not commit and the financial refund must not
// run. Pre-fix, the read error was swallowed as an empty height, the live copy was
// cancelled, and the refund proceeded against a partially-scanned window.
func (s *EthThorRestoreSuite) TestZecSlashScanErrorSkipsRefund(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	// A live, still-unsent parked copy at the HIGHER height so the downward scan reaches it
	// FIRST and blanks it into the cancel cache — then the corrupt height below aborts. This
	// proves the cache is actually DISCARDED (the blank is dropped), not merely left empty.
	parkHeight := ctx.BlockHeight() + 200
	zecAddParkedOutbound(c, ctx, mgr, parkHeight, "thorpub1addwnpepqt3t6xzve6mukamkg6c70g64zy27x2606r74s950prsryg037j6gqr5qzgg")
	// Corrupt an undecodable TxOut record at a LOWER height inside the scan window (hit after
	// parkHeight is blanked): GetTxOut returns an Unmarshal error there, aborting the scan.
	corruptHeight := ctx.BlockHeight() + 100
	key := k.GetKey("txout/", strconv.FormatInt(corruptHeight, 10))
	c.Assert(mgr.storeService.OpenKVStore(ctx).Set(key, []byte{0xff, 0xff, 0xff, 0xff}), IsNil)

	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	poolBefore, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	asgard0 := k.GetRuneBalanceOfModule(ctx, AsgardName)
	reserve0 := k.GetRuneBalanceOfModule(ctx, ReserveName)

	runZecSlashRefund19to20(ctx, mgr)

	// Refund skipped: pool, Asgard, Reserve all unchanged, and no bond provider paid.
	pool, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(poolBefore.BalanceAsset), Equals, true)
	c.Check(pool.BalanceRune.Equal(poolBefore.BalanceRune), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgard0), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve0), Equals, true)
	for _, r := range zecSlashBondRefunds19to20 {
		to, aErr := cosmos.AccAddressFromBech32(r.address)
		c.Assert(aErr, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true, Commentf("bp %s must not be paid", r.address))
	}
	// Cancel cache discarded: the live parked copy is still unsent (not blanked).
	blk, err := k.GetTxOut(ctx, parkHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.IsEmpty(), Equals, true, Commentf("live copy must remain uncancelled when the scan aborts"))
}

// The cancel must find the outbound even after LackSigning rescheduled it to a new
// height AND a new vault (signing halted) — the scan is by in_hash across a bounded
// window, not a fixed height/vault. A superseded copy (already blanked to BlankTxID)
// sits ABOVE it, so the downward scan hits the superseded one FIRST and must skip it
// (OutHash.IsEmpty() is false for BlankTxID) rather than stopping there and leaving the
// live, still-signable copy uncancelled.
func (s *EthThorRestoreSuite) TestZecSlashRefundFindsRescheduledItem(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	// Superseded copy, higher height (scanned first), already BlankTxID — must be skipped.
	supersededHeight := ctx.BlockHeight() + 300
	zecAddParkedOutbound(c, ctx, mgr, supersededHeight, GetRandomPubKeySet().Secp256k1.String())
	sup, err := k.GetTxOut(ctx, supersededHeight)
	c.Assert(err, IsNil)
	sup.TxArray[0].OutHash = common.BlankTxID
	c.Assert(k.SetTxOut(ctx, sup), IsNil)

	reschedHeight := ctx.BlockHeight() + 137 // live copy: moved forward, different vault, empty OutHash
	zecAddParkedOutbound(c, ctx, mgr, reschedHeight, GetRandomPubKeySet().Secp256k1.String())

	runZecSlashRefund19to20(ctx, mgr)

	block, err := k.GetTxOut(ctx, reschedHeight)
	c.Assert(err, IsNil)
	c.Check(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true,
		Commentf("live rescheduled item (new height/vault) not cancelled — scan stopped at the superseded copy?"))
}

// zecAssertRefundSkipped checks the financial refund did not apply: pool, modules and
// providers are all unchanged.
func zecAssertRefundSkipped(c *C, ctx cosmos.Context, mgr *Mgrs, poolBefore Pool, asgard0, reserve0 cosmos.Uint) {
	k := mgr.Keeper()
	pool, err := k.GetPool(ctx, poolBefore.Asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(poolBefore.BalanceAsset), Equals, true, Commentf("pool ZEC credited"))
	c.Check(pool.BalanceRune.Equal(poolBefore.BalanceRune), Equals, true, Commentf("pool RUNE debited"))
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgard0), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve0), Equals, true)
	for _, r := range zecSlashBondRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		c.Assert(err, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true, Commentf("bp %s", r.address))
	}
}

func zecRefundSnapshot(c *C, ctx cosmos.Context, mgr *Mgrs) (Pool, cosmos.Uint, cosmos.Uint) {
	k := mgr.Keeper()
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	pool, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	return pool, k.GetRuneBalanceOfModule(ctx, AsgardName), k.GetRuneBalanceOfModule(ctx, ReserveName)
}

// If the parked outbound is gone (or outside the scan window) nothing proves a retry did
// not settle, and a settled retry makes the slash's pool debit real. The cancel is a
// no-op and the refund soft-skips rather than crediting the pool unbacked.
func (s *EthThorRestoreSuite) TestZecSlashRefundSkipsWhenOutboundGone(c *C) {
	ctx, mgr := zecSlashSetup(c) // no parked outbound added
	poolBefore, asgard0, reserve0 := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	zecAssertRefundSkipped(c, ctx, mgr, poolBefore, asgard0, reserve0)
}

// A retry of the parked outbound that was signed and observed before the upgrade debited
// the vault a second time; the pool must NOT be credited back and providers not refunded.
// The settled copy carries a real OutHash; an older superseded copy is BlankTxID.
func (s *EthThorRestoreSuite) TestZecSlashRefundSkipsWhenRetrySettled(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	supersededHeight := ctx.BlockHeight() + 300
	zecAddParkedOutbound(c, ctx, mgr, supersededHeight, GetRandomPubKeySet().Secp256k1.String())
	sup, err := k.GetTxOut(ctx, supersededHeight)
	c.Assert(err, IsNil)
	sup.TxArray[0].OutHash = common.BlankTxID
	c.Assert(k.SetTxOut(ctx, sup), IsNil)

	settledHeight := ctx.BlockHeight() + 137
	zecAddParkedOutbound(c, ctx, mgr, settledHeight, GetRandomPubKeySet().Secp256k1.String())
	settledHash := GetRandomTxHash()
	blk, err := k.GetTxOut(ctx, settledHeight)
	c.Assert(err, IsNil)
	blk.TxArray[0].OutHash = settledHash
	c.Assert(k.SetTxOut(ctx, blk), IsNil)
	poolBefore, asgard0, reserve0 := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	zecAssertRefundSkipped(c, ctx, mgr, poolBefore, asgard0, reserve0)
	// the settled copy is untouched.
	blk, err = k.GetTxOut(ctx, settledHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.Equals(settledHash), Equals, true)
}

// Harden (Itz b1): a settled retry can sit at a LOWER height than the live, unsent
// copy. A scan that returned at the first live copy (scanned from the top) would miss
// the settled one below and wrongly refund. The full-window scan must find the settled
// copy and skip the refund, while STILL cancelling the live copy so it can never re-sign.
func (s *EthThorRestoreSuite) TestZecSlashRefundSkipsWhenSettledCopyBelowLive(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	// live, unsent copy at a HIGHER height (scanned first, from the top of the window).
	liveHeight := ctx.BlockHeight() + 200
	zecAddParkedOutbound(c, ctx, mgr, liveHeight, GetRandomPubKeySet().Secp256k1.String())
	// settled retry (real OutHash) at a LOWER height than the live copy (scanned later).
	settledHeight := ctx.BlockHeight() + 50
	zecAddParkedOutbound(c, ctx, mgr, settledHeight, GetRandomPubKeySet().Secp256k1.String())
	settledHash := GetRandomTxHash()
	sb, err := k.GetTxOut(ctx, settledHeight)
	c.Assert(err, IsNil)
	sb.TxArray[0].OutHash = settledHash
	c.Assert(k.SetTxOut(ctx, sb), IsNil)
	poolBefore, asgard0, reserve0 := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	// refund skipped — the full scan saw the settled copy below the live one.
	zecAssertRefundSkipped(c, ctx, mgr, poolBefore, asgard0, reserve0)
	// the live copy was still cancelled (never re-signs).
	lb, err := k.GetTxOut(ctx, liveHeight)
	c.Assert(err, IsNil)
	c.Check(lb.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true, Commentf("live copy not cancelled"))
	// the settled copy is untouched.
	sb, err = k.GetTxOut(ctx, settledHeight)
	c.Assert(err, IsNil)
	c.Check(sb.TxArray[0].OutHash.Equals(settledHash), Equals, true)
}

// Harden (deeafc1a19): the backward scan bound must cover SigningTransactionPeriod, not
// just TxOutDelayMax — a still-unsent copy stays signable for the signing period. With
// TxOutDelayMax mimir'd below it, a live copy inside the signing window but outside the
// TxOutDelayMax window must still be found and cancelled.
func (s *EthThorRestoreSuite) TestZecSlashRefundCancelCoversSigningPeriod(c *C) {
	ctx, mgr := zecSlashSetup(c)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.TxOutDelayMax.String(), 100) // < SigningTransactionPeriod (300)
	liveHeight := int64(800)                               // h-200: outside TxOutDelayMax(100), inside signing period(300)
	zecAddParkedOutbound(c, ctx, mgr, liveHeight, GetRandomPubKeySet().Secp256k1.String())

	runZecSlashRefund19to20(ctx, mgr)

	block, err := k.GetTxOut(ctx, liveHeight)
	c.Assert(err, IsNil)
	c.Check(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true,
		Commentf("live copy within signing period but outside TxOutDelayMax not cancelled"))
}

// A drift (pool RUNE below the pinned credit) soft-skips the FINANCIAL refund, but the
// cancel still commits (own cache context) so the already-paid outbound can never
// re-sign — the double-pay fix must survive a refund skip.
func (s *EthThorRestoreSuite) TestZecSlashRefundCancelSurvivesRefundSkip(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	parkHeight := ctx.BlockHeight()
	zecAddParkedOutbound(c, ctx, mgr, parkHeight, "thorpub1addwnpepqt3t6xzve6mukamkg6c70g64zy27x2606r74s950prsryg037j6gqr5qzgg")
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	pool, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	pool.BalanceRune = common.SafeSub(cosmos.NewUint(zecSlashPoolRuneDebit), cosmos.NewUint(1)) // drift: below the debit
	c.Assert(k.SetPool(ctx, pool), IsNil)
	asgard0 := k.GetRuneBalanceOfModule(ctx, AsgardName)
	reserve0 := k.GetRuneBalanceOfModule(ctx, ReserveName)

	runZecSlashRefund19to20(ctx, mgr)

	// refund skipped: modules + providers unchanged.
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgard0), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve0), Equals, true)
	for _, r := range zecSlashBondRefunds19to20 {
		to, addrErr := cosmos.AccAddressFromBech32(r.address)
		c.Assert(addrErr, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)
	}
	// but the cancel committed regardless: outbound can never re-sign.
	block, err := k.GetTxOut(ctx, parkHeight)
	c.Assert(err, IsNil)
	c.Check(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true,
		Commentf("cancel must survive the refund soft-skip"))
}

// f6ca75b859: the ZEC pool credit is gated on the same canonical solvency check as step 1.
// If the double-debit headroom is gone — e.g. a retry was ALSO paid, draining the vault —
// the gate skips the credit even though the live parked copy is still found and cancelled.
func (s *EthThorRestoreSuite) TestZecSlashRefundSkipsWhenVaultLacksUnaccounted(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	zec, err := common.NewAsset("ZEC.ZEC")
	c.Assert(err, IsNil)
	pool, err := k.GetPool(ctx, zec)
	c.Assert(err, IsNil)
	// Drain the active vault's ZEC down to pool depth: freeZEC == 0 < credit.
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(len(vaults) >= 1, Equals, true)
	v := vaults[0]
	v.Coins = common.NewCoins(common.NewCoin(zec, pool.BalanceAsset))
	c.Assert(k.SetVault(ctx, v), IsNil)
	parkHeight := ctx.BlockHeight()
	zecAddParkedOutbound(c, ctx, mgr, parkHeight, v.PubKey.String())
	poolBefore, asgard0, reserve0 := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	zecAssertRefundSkipped(c, ctx, mgr, poolBefore, asgard0, reserve0)
	// The cancel still lands (double-pay fix is independent of the financial refund).
	blk, err := k.GetTxOut(ctx, parkHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true,
		Commentf("live copy must be cancelled even when the refund is solvency-skipped"))
}

// 3bcd7f0c23: with HaltSigningZEC set, the parked item is never rescheduled forward and
// sits at its ORIGINAL scheduled height, ~30k blocks below the ±TxOutDelayMax window. The
// cancel extends its backward floor to the inbound voter's OutboundHeight so the stuck item
// is still reached, cancelled, and the refund proceeds.
func (s *EthThorRestoreSuite) TestZecSlashRefundFindsItemBelowWindowViaVoter(c *C) {
	ctx, mgr := zecSlashSetup(c)
	ctx = ctx.WithBlockHeight(100_000)
	k := mgr.Keeper()
	stuckHeight := int64(50_000) // below the back-window floor 100000-17280=82720
	zecAddParkedOutbound(c, ctx, mgr, stuckHeight, GetRandomPubKeySet().Secp256k1.String())
	k.SetObservedTxInVoter(ctx, ObservedTxVoter{TxID: common.TxID(zecSlashInHash), OutboundHeight: stuckHeight})
	poolBefore, _, _ := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	pool, err := k.GetPool(ctx, poolBefore.Asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(poolBefore.BalanceAsset.AddUint64(zecSlashPoolAssetCredit)), Equals, true,
		Commentf("refund must proceed once the voter floor reaches the stuck item"))
	blk, err := k.GetTxOut(ctx, stuckHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true,
		Commentf("voter floor must reach + cancel the stuck item"))
}

// Positive control for the voter floor: with no voter (OutboundHeight 0) the height window
// cannot reach the stuck item, so it is missed and the refund is skipped.
func (s *EthThorRestoreSuite) TestZecSlashRefundMissesItemBelowWindowWithoutVoter(c *C) {
	ctx, mgr := zecSlashSetup(c)
	ctx = ctx.WithBlockHeight(100_000)
	k := mgr.Keeper()
	stuckHeight := int64(50_000)
	zecAddParkedOutbound(c, ctx, mgr, stuckHeight, GetRandomPubKeySet().Secp256k1.String())
	poolBefore, asgard0, reserve0 := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	zecAssertRefundSkipped(c, ctx, mgr, poolBefore, asgard0, reserve0)
	blk, err := k.GetTxOut(ctx, stuckHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.IsEmpty(), Equals, true, Commentf("unreached item stays unsent"))
}

// -------------------------------------------------------------------------------------
// Step 6 — erroneous TRON slash refund (board 942, issue 3393)
// -------------------------------------------------------------------------------------

func tronSlashSetup(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	for _, m := range []string{AsgardName, ReserveName} {
		if bal := k.GetRuneBalanceOfModule(ctx, m); !bal.IsZero() {
			c.Assert(k.SendFromModuleToModule(ctx, m, ModuleName,
				common.NewCoins(common.NewCoin(common.RuneAsset(), bal))), IsNil)
		}
	}
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	trx, err := common.NewAsset(tronSlashTrxAsset)
	c.Assert(err, IsNil)

	usdtPool := NewPool()
	usdtPool.Asset = usdt
	usdtPool.Status = PoolAvailable
	usdtPool.BalanceAsset = cosmos.NewUint(5_000_000_000_000) // 50,000 USDT
	usdtPool.BalanceRune = cosmos.NewUint(tronSlashUsdtRuneDebit + 100*common.One)
	usdtPool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, usdtPool), IsNil)

	trxPool := NewPool()
	trxPool.Asset = trx
	trxPool.Status = PoolAvailable
	trxPool.BalanceAsset = cosmos.NewUint(100_000_000_000)
	trxPool.BalanceRune = cosmos.NewUint(tronSlashTrxRuneDebit + 100*common.One)
	trxPool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, trxPool), IsNil)

	FundModule(c, ctx, k, AsgardName, tronSlashAsgardToReserve+100*common.One)
	FundModule(c, ctx, k, ReserveName, tronSlashBondRefundTotal+100*common.One)

	// Active Asgard vault holding pool depth + the slash's double-debited USDT coin, so the
	// solvency gate sees exactly tronSlashUsdtAssetCredit of unaccounted USDT and passes.
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Coins = common.NewCoins(common.NewCoin(usdt, usdtPool.BalanceAsset.AddUint64(tronSlashUsdtAssetCredit)))
	c.Assert(k.SetVault(ctx, v), IsNil)
	// HaltSigningTRON is the load-bearing precondition the refund now enforces.
	k.SetMimir(ctx, "HaltSigningTRON", 1)
	return ctx, mgr
}

func tronSnapshot(c *C, ctx cosmos.Context, mgr *Mgrs) (Pool, Pool, cosmos.Uint, cosmos.Uint) {
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	trx, err := common.NewAsset(tronSlashTrxAsset)
	c.Assert(err, IsNil)
	up, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	tp, err := k.GetPool(ctx, trx)
	c.Assert(err, IsNil)
	return up, tp, k.GetRuneBalanceOfModule(ctx, AsgardName), k.GetRuneBalanceOfModule(ctx, ReserveName)
}

func (s *EthThorRestoreSuite) TestTronSlashRefundHappyPath(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdtBefore, trxBefore, asgard0, reserve0 := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	usdt, trx, asgard1, reserve1 := tronSnapshot(c, ctx, mgr)
	// USDT pool: coin credited back, slash RUNE removed.
	c.Check(usdt.BalanceAsset.Equal(usdtBefore.BalanceAsset.AddUint64(tronSlashUsdtAssetCredit)), Equals, true, Commentf("usdt coin restored"))
	c.Check(usdt.BalanceRune.Equal(common.SafeSub(usdtBefore.BalanceRune, cosmos.NewUint(tronSlashUsdtRuneDebit))), Equals, true, Commentf("usdt rune removed"))
	// TRX pool: RUNE removed, coin (gas) left debited.
	c.Check(trx.BalanceAsset.Equal(trxBefore.BalanceAsset), Equals, true, Commentf("trx coin unchanged (gas left debited)"))
	c.Check(trx.BalanceRune.Equal(common.SafeSub(trxBefore.BalanceRune, cosmos.NewUint(tronSlashTrxRuneDebit))), Equals, true, Commentf("trx rune removed"))
	// Asgard->Reserve freed RUNE; Reserve then paid the total.
	c.Check(asgard1.Equal(common.SafeSub(asgard0, cosmos.NewUint(tronSlashAsgardToReserve))), Equals, true)
	expReserve := common.SafeSub(reserve0.Add(cosmos.NewUint(tronSlashAsgardToReserve)), cosmos.NewUint(tronSlashBondRefundTotal))
	c.Check(reserve1.Equal(expReserve), Equals, true)
	// every affected bond provider received exactly its share.
	for _, r := range tronSlashBondRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		c.Assert(err, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).String(), Equals, cosmos.NewUint(r.amount).String(), Commentf("bp %s", r.address))
	}
}

// The USDT pool credit is solvency-gated: if the double-debit headroom is gone (a retry
// also paid), the whole refund is skipped.
func (s *EthThorRestoreSuite) TestTronSlashRefundSkipsWhenVaultLacksUnaccounted(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	up, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(len(vaults) >= 1, Equals, true)
	v := vaults[0]
	v.Coins = common.NewCoins(common.NewCoin(usdt, up.BalanceAsset)) // no headroom
	c.Assert(k.SetVault(ctx, v), IsNil)
	usdtBefore, trxBefore, asgard0, reserve0 := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	up2, tp2, asgard1, reserve1 := tronSnapshot(c, ctx, mgr)
	c.Check(up2.BalanceAsset.Equal(usdtBefore.BalanceAsset), Equals, true, Commentf("usdt pool unchanged"))
	c.Check(up2.BalanceRune.Equal(usdtBefore.BalanceRune), Equals, true)
	c.Check(tp2.BalanceRune.Equal(trxBefore.BalanceRune), Equals, true, Commentf("trx pool unchanged"))
	c.Check(asgard1.Equal(asgard0), Equals, true)
	c.Check(reserve1.Equal(reserve0), Equals, true)
	for _, r := range tronSlashBondRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		c.Assert(err, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true, Commentf("bp %s must not be paid", r.address))
	}
}

// Still-live (unsent) reschedule copies of the paid outbound are blanked so they can never pay
// a second time. The copies sit on the fixture's ACTUAL active vault (zlyzol): the solvency
// walk only counts a scheduled outbound whose vault it can resolve, so an unstored random
// pubkey would be skipped and mask the behaviour. With the real vault the duplicates ARE
// counted as a liability, so the refund correctly SKIPS this migration (crediting the pool
// would double-count backing still committed to the duplicate) — but the defensive blank
// still commits, removing the duplicates so a later re-pin can refund cleanly. A second copy
// at the previous height exercises the full-window scan and the cancel-vs-refund ordering.
// Pool / module / provider balances all stay unchanged.
func (s *EthThorRestoreSuite) TestTronSlashRefundBlanksLiveCopiesOnRealVault(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(len(vaults) >= 1, Equals, true)
	vpk := vaults[0].PubKey // a REAL vault, so the solvency walk sees the duplicate
	hiHeight := ctx.BlockHeight()
	loHeight := ctx.BlockHeight() - 1
	for _, h := range []int64{hiHeight, loHeight} {
		blk := TxOut{Height: h, TxArray: []TxOutItem{{
			Chain: common.Chain("TRON"), ToAddress: common.Address(tronSlashToAddress),
			VaultPubKey: vpk, Coin: common.NewCoin(usdt, cosmos.NewUint(tronSlashUsdtAssetCredit)),
			InHash: common.TxID(tronSlashInHash), Memo: "OUT:" + tronSlashInHash,
		}}}
		c.Assert(k.SetTxOut(ctx, &blk), IsNil)
	}
	usdtBefore, trxBefore, asgard0, reserve0 := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	// both live copies blanked (defensive blank commits even though the refund skips).
	for _, h := range []int64{hiHeight, loHeight} {
		blk, gErr := k.GetTxOut(ctx, h)
		c.Assert(gErr, IsNil)
		c.Check(blk.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true, Commentf("live copy at %d must be blanked", h))
	}
	// refund skipped: the duplicates are counted liabilities → solvency gate fails → nothing
	// credited or paid.
	up2, tp2, asgard1, reserve1 := tronSnapshot(c, ctx, mgr)
	c.Check(up2.BalanceAsset.Equal(usdtBefore.BalanceAsset), Equals, true, Commentf("usdt pool unchanged"))
	c.Check(up2.BalanceRune.Equal(usdtBefore.BalanceRune), Equals, true)
	c.Check(tp2.BalanceRune.Equal(trxBefore.BalanceRune), Equals, true, Commentf("trx pool unchanged"))
	c.Check(asgard1.Equal(asgard0), Equals, true)
	c.Check(reserve1.Equal(reserve0), Equals, true)
	for _, r := range tronSlashBondRefunds19to20 {
		to, aErr := cosmos.AccAddressFromBech32(r.address)
		c.Assert(aErr, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true, Commentf("bp %s must not be paid", r.address))
	}
}

// huginn Major (unconditional blank): the defensive blank of a still-live duplicate outbound
// MUST land even when the refund itself is skipped. Here the refund fails because the vault's
// unaccounted-USDT headroom is gone (as if a retry also paid) — exactly the case where a
// re-signable live copy is most dangerous. The prior code committed the blank only on refund
// success, so a skip left the copy live and payable again; now the blank commits regardless,
// while the refund stays fully rolled back.
func (s *EthThorRestoreSuite) TestTronSlashRefundBlanksEvenWhenRefundFails(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)

	// A live, unsent reschedule copy in-window → will be blanked.
	liveHeight := ctx.BlockHeight()
	block := TxOut{Height: liveHeight, TxArray: []TxOutItem{{
		Chain: common.Chain("TRON"), ToAddress: common.Address(tronSlashToAddress),
		VaultPubKey: GetRandomPubKeySet().Secp256k1, Coin: common.NewCoin(usdt, cosmos.NewUint(tronSlashUsdtAssetCredit)),
		InHash: common.TxID(tronSlashInHash), Memo: "OUT:" + tronSlashInHash,
	}}}
	c.Assert(k.SetTxOut(ctx, &block), IsNil)

	// Strip the vault's unaccounted-USDT headroom so the refund's solvency gate fails.
	up, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(len(vaults) >= 1, Equals, true)
	v := vaults[0]
	v.Coins = common.NewCoins(common.NewCoin(usdt, up.BalanceAsset)) // no headroom → refund skips
	c.Assert(k.SetVault(ctx, v), IsNil)

	usdtBefore, trxBefore, asgard0, reserve0 := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	// Blank committed despite the refund being skipped.
	blk, err := k.GetTxOut(ctx, liveHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true, Commentf("live copy must be blanked even when the refund fails"))

	// Refund fully rolled back: pools, modules, and providers all untouched.
	up2, tp2, asgard1, reserve1 := tronSnapshot(c, ctx, mgr)
	c.Check(up2.BalanceAsset.Equal(usdtBefore.BalanceAsset), Equals, true, Commentf("usdt pool unchanged"))
	c.Check(up2.BalanceRune.Equal(usdtBefore.BalanceRune), Equals, true)
	c.Check(tp2.BalanceRune.Equal(trxBefore.BalanceRune), Equals, true, Commentf("trx pool unchanged"))
	c.Check(asgard1.Equal(asgard0), Equals, true)
	c.Check(reserve1.Equal(reserve0), Equals, true)
	for _, r := range tronSlashBondRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		c.Assert(err, IsNil)
		c.Check(k.GetBalance(ctx, to).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true, Commentf("bp %s must not be paid", r.address))
	}
}

// S1: the ZEC refund now ENFORCES HaltSigningZEC (the only guard against a signed-but-
// unobserved second pay). With it cleared, the step skips BEFORE the cancel, so the parked
// copy is left unsent and nothing moves.
func (s *EthThorRestoreSuite) TestZecSlashRefundSkipsWhenSigningNotHalted(c *C) {
	ctx, mgr := zecSlashSetup(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, "HaltSigningZEC", 0) // not halted
	parkHeight := ctx.BlockHeight()
	zecAddParkedOutbound(c, ctx, mgr, parkHeight, GetRandomPubKeySet().Secp256k1.String())
	poolBefore, asgard0, reserve0 := zecRefundSnapshot(c, ctx, mgr)

	runZecSlashRefund19to20(ctx, mgr)

	zecAssertRefundSkipped(c, ctx, mgr, poolBefore, asgard0, reserve0)
	// skipped before the cancel: the live copy is untouched (still empty OutHash).
	blk, err := k.GetTxOut(ctx, parkHeight)
	c.Assert(err, IsNil)
	c.Check(blk.TxArray[0].OutHash.IsEmpty(), Equals, true, Commentf("must skip before cancelling when signing is not halted"))
}

// S1 for TRON: the refund enforces HaltSigningTRON.
func (s *EthThorRestoreSuite) TestTronSlashRefundSkipsWhenSigningNotHalted(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, "HaltSigningTRON", 0)
	usdtBefore, _, asgard0, reserve0 := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	up, _, asgard1, reserve1 := tronSnapshot(c, ctx, mgr)
	c.Check(up.BalanceAsset.Equal(usdtBefore.BalanceAsset), Equals, true, Commentf("usdt pool unchanged"))
	c.Check(asgard1.Equal(asgard0), Equals, true)
	c.Check(reserve1.Equal(reserve0), Equals, true)
}

// #2: a SECOND copy carrying a real OutHash other than the completed send = a settled retry
// (the vault paid twice). The TRON refund must skip (not credit an unbacked pool).
func (s *EthThorRestoreSuite) TestTronSlashRefundSkipsWhenSettledRetry(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	// A retry copy with a real OutHash different from tronSlashCompletedOutHash, in-window.
	retryHeight := ctx.BlockHeight() + 50
	block := TxOut{Height: retryHeight, TxArray: []TxOutItem{{
		Chain: common.Chain("TRON"), ToAddress: common.Address(tronSlashToAddress),
		VaultPubKey: GetRandomPubKeySet().Secp256k1, Coin: common.NewCoin(usdt, cosmos.NewUint(tronSlashUsdtAssetCredit)),
		InHash: common.TxID(tronSlashInHash), Memo: "OUT:" + tronSlashInHash, OutHash: GetRandomTxHash(),
	}}}
	c.Assert(k.SetTxOut(ctx, &block), IsNil)
	usdtBefore, _, _, _ := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	up, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	c.Check(up.BalanceAsset.Equal(usdtBefore.BalanceAsset), Equals, true, Commentf("refund must skip when a settled retry exists"))
}

// dup-copy extension (Itz): when a settled retry (real OutHash) AND a still-live empty-OutHash
// copy both exist, the refund is skipped (double-paid) but the live empty copy is STILL
// blanked — a settled retry does not make the live duplicate safe; left signable it could pay
// a THIRD time. The settled copy itself (real OutHash) is left untouched.
func (s *EthThorRestoreSuite) TestTronSlashRefundBlanksLiveCopyWhenSettledRetry(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	// Live empty-OutHash copy (must be blanked).
	liveHeight := ctx.BlockHeight()
	liveBlock := TxOut{Height: liveHeight, TxArray: []TxOutItem{{
		Chain: common.Chain("TRON"), ToAddress: common.Address(tronSlashToAddress),
		VaultPubKey: GetRandomPubKeySet().Secp256k1, Coin: common.NewCoin(usdt, cosmos.NewUint(tronSlashUsdtAssetCredit)),
		InHash: common.TxID(tronSlashInHash), Memo: "OUT:" + tronSlashInHash,
	}}}
	c.Assert(k.SetTxOut(ctx, &liveBlock), IsNil)
	// Settled retry copy with a real OutHash != the completed send.
	retryHeight := ctx.BlockHeight() + 50
	retryOut := GetRandomTxHash()
	retryBlock := TxOut{Height: retryHeight, TxArray: []TxOutItem{{
		Chain: common.Chain("TRON"), ToAddress: common.Address(tronSlashToAddress),
		VaultPubKey: GetRandomPubKeySet().Secp256k1, Coin: common.NewCoin(usdt, cosmos.NewUint(tronSlashUsdtAssetCredit)),
		InHash: common.TxID(tronSlashInHash), Memo: "OUT:" + tronSlashInHash, OutHash: retryOut,
	}}}
	c.Assert(k.SetTxOut(ctx, &retryBlock), IsNil)
	usdtBefore, _, _, _ := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	// live empty copy blanked despite the settled retry.
	lb, err := k.GetTxOut(ctx, liveHeight)
	c.Assert(err, IsNil)
	c.Check(lb.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true, Commentf("live empty copy must be blanked even when a settled retry exists"))
	// settled retry copy left untouched (keeps its real OutHash).
	rb, err := k.GetTxOut(ctx, retryHeight)
	c.Assert(err, IsNil)
	c.Check(rb.TxArray[0].OutHash.Equals(retryOut), Equals, true, Commentf("settled retry copy must be left untouched"))
	// refund skipped (pool unchanged).
	up2, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	c.Check(up2.BalanceAsset.Equal(usdtBefore.BalanceAsset), Equals, true, Commentf("refund must skip on a settled retry"))
}

// A copy carrying the KNOWN completed send hash is NOT a retry — the legitimate refund still
// proceeds (the carve-out).
func (s *EthThorRestoreSuite) TestTronSlashRefundProceedsWithCompletedCopy(c *C) {
	ctx, mgr := tronSlashSetup(c)
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	c.Assert(err, IsNil)
	completedHeight := ctx.BlockHeight() + 20
	block := TxOut{Height: completedHeight, TxArray: []TxOutItem{{
		Chain: common.Chain("TRON"), ToAddress: common.Address(tronSlashToAddress),
		VaultPubKey: GetRandomPubKeySet().Secp256k1, Coin: common.NewCoin(usdt, cosmos.NewUint(tronSlashUsdtAssetCredit)),
		InHash: common.TxID(tronSlashInHash), Memo: "OUT:" + tronSlashInHash, OutHash: common.TxID(tronSlashCompletedOutHash),
	}}}
	c.Assert(k.SetTxOut(ctx, &block), IsNil)
	usdtBefore, _, _, _ := tronSnapshot(c, ctx, mgr)

	runTronSlashRefund19to20(ctx, mgr)

	up, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	c.Check(up.BalanceAsset.Equal(usdtBefore.BalanceAsset.AddUint64(tronSlashUsdtAssetCredit)), Equals, true, Commentf("completed copy is carved out; refund proceeds"))
}

// BTC step-1 gas (Itz: subtract): the rzxslmgc slash was a re-observed REAL spend, so the
// pool credit is the send amount only (53,257,717 sats), leaving the 1,000-sat gas debited
// — pool depth == vault backing, ZEC/TRON parity. This asserts the credited amount is the
// send, not send+gas.
func (s *EthThorRestoreSuite) TestBtcSlashRefundCreditExcludesGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	for _, m := range []string{AsgardName, ReserveName} {
		if bal := k.GetRuneBalanceOfModule(ctx, m); !bal.IsZero() {
			c.Assert(k.SendFromModuleToModule(ctx, m, ModuleName,
				common.NewCoins(common.NewCoin(common.RuneAsset(), bal))), IsNil)
		}
	}
	const btcCreditSendOnly = uint64(53_257_717) // send only; the slashed total was 53_258_717 (send + 1000 gas)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100_000_000_000) // 1000 BTC depth
	pool.BalanceRune = cosmos.NewUint(5_677_363_556_220 + 100_000*common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	// Active vault holding pool depth + the reversed send, so the solvency gate passes.
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, pool.BalanceAsset.AddUint64(btcCreditSendOnly)))
	c.Assert(k.SetVault(ctx, v), IsNil)
	FundModule(c, ctx, k, AsgardName, 5_677_363_556_220+100_000*common.One)
	FundModule(c, ctx, k, ReserveName, mainnetSlashRefunds19to20Total+100_000*common.One)

	c.Assert(applySlashRefund19to20(ctx, mgr), IsNil)

	p, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(p.BalanceAsset.Equal(cosmos.NewUint(100_000_000_000+btcCreditSendOnly)), Equals, true,
		Commentf("BTC credit must be the send (53,257,717), not send+gas (53,258,717)"))
}
