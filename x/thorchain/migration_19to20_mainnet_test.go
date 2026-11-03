//go:build mainnet
// +build mainnet

package thorchain

import (
	"github.com/blang/semver"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ConsensusVersion() bumps to 20 and RegisterServices registers Migrate19to20
// unconditionally, but mainnetDonationSpecComplete gates the migration to a
// log-and-skip until the release cut. If a >=3.21.0 build ships with the flag
// still false, the upgrade records module version 20 while the migration
// no-ops, permanently stranding the 218 bond-provider refunds, the donations,
// the ETH.THOR restore, the synth retirement and the h28181638 ZEC slash refund
// behind a future 20->21 migration. This tripwire fails CI on any >=3.21.0 build whose spec is not
// complete; it passes below 3.21.0, so pre-cut development is unaffected. At
// the cut: re-pin the amounts from live and set mainnetDonationSpecComplete=true.
func (s *Migration19to20Suite) TestMainnetDonationSpecCompleteForRelease(c *C) {
	if GetCurrentVersion().GTE(semver.MustParse("3.21.0")) {
		c.Assert(mainnetDonationSpecComplete, Equals, true,
			Commentf("re-pin the Migrate19to20 amounts from live and set mainnetDonationSpecComplete=true before the 3.21.0 cut"))
	}
}

// Until every TODO in the mainnet donation spec is filled and
// mainnetDonationSpecComplete is set, the migration logs and SKIPS (returns nil,
// applies nothing) rather than halting — so a pre-cut build that reaches an
// upgrade height no-ops instead of halting the chain.
func (s *Migration19to20Suite) TestMainnetDonationSpecGuard(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	// Seed state that a real step would move if the guard were off: a low-synth pool
	// (step 4 burns these) that is Available and within the pinned bounds, plus
	// Reserve/Asgard RUNE (steps 1/3/5 move these). An empty-TxArray check can't tell
	// gated from ungated — no step schedules an outbound — so assert these concrete
	// balances are untouched, which only holds while the spec-complete gate suppresses
	// every step.
	synthAsset, err := common.NewAsset("BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	c.Assert(err, IsNil)
	synth := synthAsset.GetSyntheticAsset()
	pool := NewPool()
	pool.Asset = synthAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(3_389_958_456_610) // pinned supply ~0.26% utilization (in-bounds)
	pool.BalanceRune = cosmos.NewUint(100_000_000_000)
	pool.LPUnits = cosmos.NewUint(100_000_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	holder := GetRandomBech32Addr()
	synthCoin := common.NewCoin(synth, cosmos.NewUint(17_932_761_109)) // == pinned → ungated this burns
	c.Assert(k.MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, holder, common.NewCoins(synthCoin)), IsNil)
	FundModule(c, ctx, k, ReserveName, 1_000_000_00000000)
	FundModule(c, ctx, k, AsgardName, 1_000_000_00000000)
	reserve0 := k.GetRuneBalanceOfModule(ctx, ReserveName)
	asgard0 := k.GetRuneBalanceOfModule(ctx, AsgardName)
	synth0 := k.GetTotalSupply(ctx, synth)

	err = NewMigrator(mgr).Migrate19to20(ctx)
	c.Assert(err, IsNil)
	if mainnetDonationSpecComplete {
		return
	}
	// skip path: the spec-complete gate suppressed every step — nothing moved.
	c.Check(k.GetTotalSupply(ctx, synth).Equal(synth0), Equals, true, Commentf("low-synth pool burned despite the gate"))
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve0), Equals, true, Commentf("Reserve moved despite the gate"))
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgard0), Equals, true, Commentf("Asgard moved despite the gate"))
	out, gErr := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(gErr, IsNil)
	if out != nil {
		c.Check(out.TxArray, HasLen, 0)
	}
}

// The filled donations must parse and name only the Secured Ops LP records.
func (s *Migration19to20Suite) TestMainnetDonationSpecDonations(c *C) {
	spec, err := mainnetDonationSpec19to20()
	c.Assert(err, IsNil)
	// ETH.THOR is donated + restored in its own step now, so this spec carries
	// the three remaining Secured Ops positions (BSC.USDC/USDT + BASE.ETH).
	c.Assert(spec.Donations, HasLen, 3)
	wantUnits := map[string]uint64{
		"USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D": 38747498669916,
		"USDT-0X55D398326F99059FF775485246999027B3197955": 9273374739774,
		"ETH": 12450028028812,
	}
	for _, d := range spec.Donations {
		c.Assert(d.Address.String(), Equals, "thor1gkvvzfnkyqv8dfhk0593tfhgr03a3ndgjqdalf")
		c.Assert(d.Asset.IsEmpty(), Equals, false)
		c.Assert(d.Asset.Symbol.String(), Not(Equals), "THOR-0XA5F2211B9B8170F694421F2046281775E8468044")
		// Each donation pins its exact LP units (the price-independent drift signal).
		c.Assert(d.Units.Uint64(), Equals, wantUnits[d.Asset.Symbol.String()], Commentf("units for %s", d.Asset))
	}
	c.Assert(spec.Pairings, HasLen, 0)

	// One Reserve payment to New Dawn Treasury, bound to all three donations.
	c.Assert(spec.Payments, HasLen, 1)
	p := spec.Payments[0]
	_, err = cosmos.AccAddressFromBech32(p.To)
	c.Assert(err, IsNil)
	c.Assert(p.To, Equals, "thor1lhufh0mwasa0lk9udppdegmvnkgqt08f0m9p5g")
	c.Assert(p.Amount.Uint64(), Equals, uint64(24_762_229_462_483))
	c.Assert(p.ValueOf, HasLen, 3)
	for _, d := range p.ValueOf {
		c.Assert(d.Asset.Symbol.String(), Not(Equals), "THOR-0XA5F2211B9B8170F694421F2046281775E8468044")
	}
	// The payment is one-sided (min(pinned, live)); position drift is gated by Units, so the
	// ValueOf donations carry the same pinned units as the Donations list.
	c.Assert(p.ValueOf[0].Units.IsZero(), Equals, false)
}
