package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type Migration19to20Suite struct{}

var _ = Suite(&Migration19to20Suite{})

type donationFixture struct {
	ctx      cosmos.Context
	mgr      *Mgrs
	asset    common.Asset
	donor    LiquidityProvider
	stayer   LiquidityProvider
	payee    cosmos.AccAddress
	vault    Vault
	reserve0 cosmos.Uint
}

const (
	fixPoolRune  = 1_000 * common.One
	fixPoolAsset = 10_000 * common.One
	fixExtraHeld = 500 * common.One // asset in the vault, not counted in the pool
	fixReserve   = 10_000 * common.One
)

func newDonationFixture(c *C) donationFixture {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("ETH.THOR-0XA5F2211B9B8170F694421F2046281775E8468044")
	c.Assert(err, IsNil)

	donor := LiquidityProvider{Asset: asset, RuneAddress: GetRandomRUNEAddress(), Units: cosmos.NewUint(100), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint()}
	stayer := LiquidityProvider{Asset: asset, RuneAddress: GetRandomRUNEAddress(), Units: cosmos.NewUint(300), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint()}
	k.SetLiquidityProvider(ctx, donor)
	k.SetLiquidityProvider(ctx, stayer)

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(fixPoolRune)
	pool.BalanceAsset = cosmos.NewUint(fixPoolAsset)
	pool.LPUnits = cosmos.NewUint(400)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{common.NewCoin(asset, cosmos.NewUint(fixPoolAsset+fixExtraHeld))}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	FundModule(c, ctx, k, AsgardName, fixPoolRune)
	FundModule(c, ctx, k, ReserveName, fixReserve)

	return donationFixture{
		ctx: ctx, mgr: mgr, asset: asset, donor: donor, stayer: stayer,
		payee: GetRandomBech32Addr(), vault: vault, reserve0: k.GetRuneBalanceOfModule(ctx, ReserveName),
	}
}

func (f donationFixture) spec() donationMigrationSpec {
	return donationMigrationSpec{
		Donations: []lpDonation{{Asset: f.asset, Address: f.donor.RuneAddress, Units: f.donor.Units}},
		Payments:  []reservePayment{{To: f.payee.String(), Amount: cosmos.NewUint(50 * common.One), Reason: "test"}},
		Pairings:  []vaultAssetPairing{{Asset: f.asset, Amount: cosmos.NewUint(fixExtraHeld)}},
	}
}

func brokenInvariants(ctx cosmos.Context, mgr *Mgrs, routes ...string) map[string]bool {
	out := map[string]bool{}
	for _, r := range mgr.Keeper().InvariantRoutes() {
		for _, want := range routes {
			if r.Route == want {
				_, broken := r.Invariant(ctx)
				out[r.Route] = broken
			}
		}
	}
	return out
}

// Donation removes the LP record and its pool units without touching depth; the
// pairing adds the vault's unaccounted asset with RUNE from the Reserve at the pool
// price; the payment is paid from the Reserve. The Reserve is debited exactly the
// paired plus paid RUNE, and the pool/asgard invariants still hold.
func (s *Migration19to20Suite) TestDonationMigration(c *C) {
	f := newDonationFixture(c)
	k := f.mgr.Keeper()
	before := brokenInvariants(f.ctx, f.mgr, "pools", "asgard")
	asgard0 := k.GetRuneBalanceOfModule(f.ctx, AsgardName)
	poolBefore, err := k.GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)

	c.Assert(applyDonationMigration(f.ctx, f.mgr, f.spec()), IsNil)

	// LP donated: record gone, units removed, the other LP untouched.
	gone, err := k.GetLiquidityProvider(f.ctx, f.asset, f.donor.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(gone.Units.IsZero(), Equals, true)
	stayer, err := k.GetLiquidityProvider(f.ctx, f.asset, f.stayer.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(stayer.Units.Equal(cosmos.NewUint(300)), Equals, true)

	// Pool: units shrink by the donation, depth grows only by the pairing.
	pairedRune := uint64(fixExtraHeld / (fixPoolAsset / fixPoolRune)) // price 0.1 RUNE/asset
	pool, err := k.GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(pool.LPUnits.Equal(cosmos.NewUint(300)), Equals, true)
	// Midgard pool-unit invariant: the donated units must appear as a withdraw event,
	// or Midgard (units = Σstake − Σwithdraw) keeps the donated position. The stored
	// LPUnits drop equals the summed withdraw units for the pool.
	c.Assert(poolBefore.LPUnits.Sub(pool.LPUnits).String(), Equals, donationWithdrawUnits(f.ctx, f.asset),
		Commentf("ΔpoolLPUnits must equal Σwithdraw for the donated LP"))
	c.Assert(pool.BalanceAsset.Equal(cosmos.NewUint(fixPoolAsset+fixExtraHeld)), Equals, true)
	c.Assert(pool.BalanceRune.Equal(cosmos.NewUint(fixPoolRune+pairedRune)), Equals, true)
	// Pool price unchanged by the pairing.
	c.Assert(pool.BalanceRune.MulUint64(fixPoolAsset).Equal(pool.BalanceAsset.MulUint64(fixPoolRune)), Equals, true)

	// Reserve debit == paired + paid RUNE; asgard gains the paired RUNE; payee paid.
	paid := uint64(50 * common.One)
	c.Assert(k.GetRuneBalanceOfModule(f.ctx, ReserveName).Equal(f.reserve0.SubUint64(pairedRune+paid)), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(f.ctx, AsgardName).Equal(asgard0.AddUint64(pairedRune)), Equals, true)
	c.Assert(k.GetBalance(f.ctx, f.payee).AmountOf(common.RuneNative.Native()).Uint64(), Equals, paid)

	// Remaining LP's redeemable share: all of the pool now.
	c.Assert(stayer.Units.Equal(pool.LPUnits), Equals, true)

	c.Assert(brokenInvariants(f.ctx, f.mgr, "pools", "asgard"), DeepEquals, before)
	c.Assert(before["pools"], Equals, false)

	// the pairing credit emits a pool_balance_change event (Midgard depth tracking).
	found := false
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type == "pool_balance_change" {
			found = true
		}
	}
	c.Assert(found, Equals, true)
}

// TestDonationPairingPreservesPriceAboveDepth proves the pairing uses the UNCAPPED share
// (be75219e10): a pairing larger than the pool's current asset depth still supplies RUNE in
// proportion so the pool price is preserved. GetSafeShare would clamp the RUNE to the pool's
// smaller asset depth and shift the price, breaking the pairing's contract.
func (s *Migration19to20Suite) TestDonationPairingPreservesPriceAboveDepth(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("ETH.THOR-0XA5F2211B9B8170F694421F2046281775E8468044")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One) // price 1
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Pair 200 asset into a pool holding only 100: larger than the depth, so GetSafeShare
	// would clamp. Vault holds pool + 300 surplus so the unaccounted-vault guard clears.
	pairAmt := cosmos.NewUint(200 * common.One)
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{common.NewCoin(asset, pool.BalanceAsset.Add(cosmos.NewUint(300*common.One)))}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	FundModule(c, ctx, k, ReserveName, 1000*common.One)

	spec := donationMigrationSpec{Pairings: []vaultAssetPairing{{Asset: asset, Amount: pairAmt}}}
	c.Assert(applyDonationMigration(ctx, mgr, spec), IsNil)

	p, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	// Uncapped: +200 asset AND +200 RUNE → price still 1 (300/300). Clamped would add only
	// 100 RUNE → 200/300, a price shift.
	c.Check(p.BalanceAsset.Equal(cosmos.NewUint(300*common.One)), Equals, true)
	c.Check(p.BalanceRune.Equal(cosmos.NewUint(300*common.One)), Equals, true)
}

// donationWithdrawUnits sums liquidity_provider_units across all "withdraw" events
// emitted for the given pool. Kept local to the untagged build (the ETH.THOR helper
// is mainnet-tagged); CI unit-tests build -tags=mocknet, where that helper is absent.
func donationWithdrawUnits(ctx cosmos.Context, pool common.Asset) string {
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

// Every mismatch between the spec and state is an error, never a guess.
func (s *Migration19to20Suite) TestDonationMigrationRejects(c *C) {
	cases := map[string]func(f *donationFixture, spec *donationMigrationSpec){
		"no such LP": func(f *donationFixture, spec *donationMigrationSpec) {
			spec.Donations[0].Address = GetRandomRUNEAddress()
		},
		"pending liquidity": func(f *donationFixture, spec *donationMigrationSpec) {
			lp := f.donor
			lp.PendingRune = cosmos.NewUint(1)
			f.mgr.Keeper().SetLiquidityProvider(f.ctx, lp)
		},
		"pairing above unaccounted vault balance": func(f *donationFixture, spec *donationMigrationSpec) {
			spec.Pairings[0].Amount = cosmos.NewUint(fixExtraHeld + 1)
		},
		"zero payment": func(f *donationFixture, spec *donationMigrationSpec) {
			spec.Payments[0].Amount = cosmos.ZeroUint()
		},
		"reserve too small": func(f *donationFixture, spec *donationMigrationSpec) {
			spec.Payments[0].Amount = f.reserve0.AddUint64(1)
		},
		"bad payment address": func(f *donationFixture, spec *donationMigrationSpec) {
			spec.Payments[0].To = "not-an-address"
		},
	}
	for name, mutate := range cases {
		f := newDonationFixture(c)
		spec := f.spec()
		mutate(&f, &spec)
		c.Check(applyDonationMigration(f.ctx, f.mgr, spec), NotNil, Commentf(name))
	}
}

// unaccountedVaultAsset19to20 delegates to calculateNetworkSolvency, so the free
// surplus nets out every liability — pool balance, trade/secured depth, queued
// swaps — and refuses to compute (errors "churn in progress") while any Asgard
// vault is retiring, rather than crediting against a mid-churn snapshot.
func (s *Migration19to20Suite) TestUnaccountedVaultAssetNetsLiabilities(c *C) {
	f := newDonationFixture(c)
	k := f.mgr.Keeper()

	// Baseline: one ActiveVault holds pool+extra; no other liabilities.
	base, err := unaccountedVaultAsset19to20(f.ctx, f.mgr, f.asset)
	c.Assert(err, IsNil)
	c.Assert(base.Equal(cosmos.NewUint(fixExtraHeld)), Equals, true)

	// Trade + secured depth are liabilities → subtracted from the surplus.
	tradeDepth := uint64(fixExtraHeld / 5)
	securedDepth := uint64(fixExtraHeld / 4)
	k.SetTradeUnit(f.ctx, TradeUnit{Asset: f.asset.GetTradeAsset(), Depth: cosmos.NewUint(tradeDepth)})
	k.SetSecuredAsset(f.ctx, SecuredAsset{Asset: f.asset.GetSecuredAsset(), Depth: cosmos.NewUint(securedDepth)})
	got, err := unaccountedVaultAsset19to20(f.ctx, f.mgr, f.asset)
	c.Assert(err, IsNil)
	c.Assert(got.Equal(cosmos.NewUint(fixExtraHeld-tradeDepth-securedDepth)), Equals, true)

	// A queued swap whose source coin is the asset is an in-flight liability → subtracted.
	swapAmt := uint64(fixExtraHeld / 10)
	swapTx := common.NewTx(GetRandomTxHash(), GetRandomETHAddress(), GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(f.asset, cosmos.NewUint(swapAmt))), nil, "swap")
	qmsg := MsgSwap{Tx: swapTx, TargetAsset: common.RuneAsset()}
	c.Assert(k.SetSwapQueueItem(f.ctx, qmsg, 0), IsNil)
	got, err = unaccountedVaultAsset19to20(f.ctx, f.mgr, f.asset)
	c.Assert(err, IsNil)
	c.Assert(got.Equal(cosmos.NewUint(fixExtraHeld-tradeDepth-securedDepth-swapAmt)), Equals, true)

	// The pairing (fixExtraHeld) now exceeds the netted surplus → migration rejects it.
	c.Assert(applyDonationMigration(f.ctx, f.mgr, f.spec()), ErrorMatches, ".*exceeds unaccounted vault balance.*")

	// A RetiringVault (churn in progress) → the solvency calc refuses to run.
	rv := GetRandomVault()
	rv.Type = AsgardVault
	rv.Status = RetiringVault
	rv.Coins = common.Coins{common.NewCoin(f.asset, cosmos.NewUint(777*common.One))}
	c.Assert(k.SetVault(f.ctx, rv), IsNil)
	_, err = unaccountedVaultAsset19to20(f.ctx, f.mgr, f.asset)
	c.Assert(err, ErrorMatches, ".*churn in progress.*")
}

// A donation that would remove the pool's ONLY LP must abort: leaving positive
// BalanceAsset/BalanceRune with zero LPUnits lets the next (dust) AddLiquidity
// mint 100% of the units and claim the stranded depth.
func (s *Migration19to20Suite) TestDonationMigrationRefusesSoleLPDonation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset, err := common.NewAsset("ETH.THOR-0XA5F2211B9B8170F694421F2046281775E8468044")
	c.Assert(err, IsNil)

	donor := LiquidityProvider{
		Asset: asset, RuneAddress: GetRandomRUNEAddress(),
		Units: cosmos.NewUint(100), PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, donor)

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10_000 * common.One)
	pool.LPUnits = cosmos.NewUint(100) // donor is the sole LP
	c.Assert(k.SetPool(ctx, pool), IsNil)

	spec := donationMigrationSpec{Donations: []lpDonation{{Asset: asset, Address: donor.RuneAddress, Units: donor.Units}}}
	c.Assert(applyDonationMigration(ctx, mgr, spec), ErrorMatches, ".*no LP units.*")

	// error returns before SetPool/RemoveLiquidityProvider → pool + LP intact.
	p, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(p.LPUnits.Equal(cosmos.NewUint(100)), Equals, true)
	got, err := k.GetLiquidityProvider(ctx, asset, donor.RuneAddress)
	c.Assert(err, IsNil)
	c.Check(got.Units.Equal(cosmos.NewUint(100)), Equals, true)
}

// A payment bound to the positions it repays is valued at twice their RUNE share
// (here 2 x 100/400 x 1,000 = 500 RUNE) before donation. The binding is ONE-SIDED
// (effectivePayment): the Reserve pays min(pinned, live) and NEVER aborts on an upper
// value drift — a market price move that lifts the live value above the pin just pays the
// pinned amount, it is not a state change. Genuine position drift is caught by the Units
// pin, exercised separately. Here the donated units always match, so every row proceeds.
func (s *Migration19to20Suite) TestDonationPaymentOneSidedBinding(c *C) {
	for _, tc := range []struct {
		amount uint64 // pinned payment (RUNE)
		paid   uint64 // expected RUNE paid = min(pinned, 500 live)
	}{
		{500, 500}, // exact
		{600, 500}, // pinned above live → pay live, never over-pay
		{505, 500}, // pinned just above → pay live
		{496, 496}, // pinned just below live → pay pinned
		{400, 400}, // pinned well below live (price moved up) → pay pinned, NO abort any more
		{100, 100}, // deep under-pin → pay pinned (Reserve-safe; guarded by a CI recompute test)
	} {
		f := newDonationFixture(c)
		spec := f.spec()
		spec.Payments[0].Amount = cosmos.NewUint(tc.amount * common.One)
		spec.Payments[0].ValueOf = spec.Donations
		err := applyDonationMigration(f.ctx, f.mgr, spec)
		c.Check(err, IsNil, Commentf("amount %d", tc.amount))
		paid := f.mgr.Keeper().GetBalance(f.ctx, f.payee).AmountOf(common.RuneNative.Native()).Uint64()
		c.Check(paid, Equals, tc.paid*common.One, Commentf("amount %d paid %d", tc.amount, paid))
	}
}

// The per-record Units pin is the drift gate: an EXACT mismatch (add, withdraw, removal)
// aborts the whole step, while a market price move does not. min(pinned, live) payment is
// unchanged.
func (s *Migration19to20Suite) TestDonationUnitsDriftGate(c *C) {
	for _, tc := range []struct {
		name  string
		units cosmos.Uint // pinned units (donor live units = 100)
		ok    bool
	}{
		{"exact", cosmos.NewUint(100), true},
		{"units+1 (topped up since pin)", cosmos.NewUint(101), false},
		{"units-1 (withdrew since pin)", cosmos.NewUint(99), false},
		{"wildly off (re-pin error)", cosmos.NewUint(999999), false},
	} {
		f := newDonationFixture(c)
		spec := f.spec()
		spec.Donations[0].Units = tc.units
		spec.Payments[0].ValueOf = spec.Donations
		err := applyDonationMigration(f.ctx, f.mgr, spec)
		if tc.ok {
			c.Check(err, IsNil, Commentf(tc.name))
			continue
		}
		c.Check(err, ErrorMatches, ".*units .* != pinned .*", Commentf(tc.name))
		// Aborted before anything moved.
		lp, getErr := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.donor.RuneAddress)
		c.Assert(getErr, IsNil)
		c.Check(lp.Units.Equal(f.donor.Units), Equals, true)
		c.Check(f.mgr.Keeper().GetBalance(f.ctx, f.payee).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)
	}
}

// A record that no longer exists reads as zero units and aborts the step loudly.
func (s *Migration19to20Suite) TestDonationUnitsGateMissingRecord(c *C) {
	f := newDonationFixture(c)
	spec := f.spec()
	// Donate a never-seeded record: GetLiquidityProvider returns zero units.
	spec.Donations[0].Address = GetRandomRUNEAddress()
	spec.Payments[0].ValueOf = spec.Donations
	err := applyDonationMigration(f.ctx, f.mgr, spec)
	c.Check(err, NotNil)
}

// A spec that no longer matches state is skipped whole: no donation, no payment,
// and the upgrade carries on. A matching spec is applied.
func (s *Migration19to20Suite) TestRunDonationMigrationSkipsOnMismatch(c *C) {
	f := newDonationFixture(c)
	spec := f.spec()
	spec.Payments[0].ValueOf = spec.Donations
	spec.Donations[0].Units = cosmos.NewUint(999) // live units 100 != pinned 999 → units-drift abort
	spec.Payments[0].ValueOf = spec.Donations
	runDonationMigration(f.ctx, f.mgr, spec)
	k := f.mgr.Keeper()
	lp, err := k.GetLiquidityProvider(f.ctx, f.asset, f.donor.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Equal(f.donor.Units), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(f.ctx, ReserveName).Equal(f.reserve0), Equals, true)
	c.Assert(k.GetBalance(f.ctx, f.payee).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)

	// A mismatch found after the donation was written must not leave it behind. Restore the
	// correct units pin so only the bad payee address fails this run.
	spec.Donations[0].Units = f.donor.Units
	spec.Payments[0].ValueOf = spec.Donations
	spec.Payments[0].Amount = cosmos.NewUint(500 * common.One)
	payee := spec.Payments[0].To
	spec.Payments[0].To = "not-an-address"
	runDonationMigration(f.ctx, f.mgr, spec)
	lp, err = k.GetLiquidityProvider(f.ctx, f.asset, f.donor.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Equal(f.donor.Units), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(f.ctx, ReserveName).Equal(f.reserve0), Equals, true)

	spec.Payments[0].To = payee
	runDonationMigration(f.ctx, f.mgr, spec)
	lp, err = k.GetLiquidityProvider(f.ctx, f.asset, f.donor.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.IsZero(), Equals, true)
	c.Assert(k.GetBalance(f.ctx, f.payee).AmountOf(common.RuneNative.Native()).Uint64(), Equals, uint64(500*common.One))
	// The withdraw event emitted on the inner cacheCtx must reach the OUTER ctx via
	// writeCache (the committed production path), or Midgard never sees it. Only this
	// final run committed; the two aborted runs forwarded nothing.
	c.Assert(donationWithdrawUnits(f.ctx, f.asset), Equals, f.donor.Units.String(),
		Commentf("committed donation withdraw event must propagate to the outer ctx"))
}
