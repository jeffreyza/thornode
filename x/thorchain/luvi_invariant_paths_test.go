package thorchain

import (
	"fmt"
	"math/big"

	sdkmath "cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Coverage for the remaining pool-mutating code paths, the headline identities
// checked against real handler sequences, exact loss magnitudes, and
// block-level (multi-operation) observation. Builds on luvi_invariant_test.go.

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// sqrtARf returns sqrt(A*R) of a state as a big.Float for tolerances.
func sqrtARf(s types2.LuviState) *big.Float {
	f, _, err := big.ParseFloat(s.SqrtAR().String(), 10, 256, big.ToNearestEven)
	if err != nil {
		panic(err)
	}
	return f
}

func decToFloat(d cosmos.Dec) *big.Float {
	f, _, err := big.ParseFloat(d.String(), 10, 256, big.ToNearestEven)
	if err != nil {
		panic(err)
	}
	return f
}

// normDiff returns |a - b| / sqrt(A*R) of s.
func normDiff(a, b cosmos.Dec, s types2.LuviState) *big.Float {
	d := new(big.Float).SetPrec(256).Sub(decToFloat(a), decToFloat(b))
	d.Abs(d)
	return d.Quo(d, sqrtARf(s))
}

func lessThan(x *big.Float, f float64) bool { return x.Cmp(big.NewFloat(f)) <= 0 }

// addEventUnits sums liquidity_provider_units over add_liquidity events.
func addEventUnits(ctx cosmos.Context) cosmos.Uint {
	units := cosmos.ZeroUint()
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type != types2.AddLiquidityEventType {
			continue
		}
		for _, attr := range ev.Attributes {
			if attr.Key == "liquidity_provider_units" {
				units = units.Add(cosmos.NewUintFromString(attr.Value))
			}
		}
	}
	return units
}

// realOp is one legitimate operation through a real code path.
type realOp struct {
	name string
	run  func(e *luviEnv)
}

// legitOps returns a mix of legitimate operations. Deposits are omitted in
// overhang (see TestOverhangDepositMultipliesUnits).
func legitOps(withSynths bool) []realOp {
	ops := []realOp{
		{"swap RUNE->asset", func(e *luviEnv) { e.swapRuneToAsset(luviPoolRune / 300) }},
		{"swap asset->RUNE", func(e *luviEnv) { e.swapAssetToRune(luviPoolAsset / 250) }},
		{"add symmetric", func(e *luviEnv) {
			if !e.state().IsOverhang() {
				e.addLiquidity(luviPoolRune/1_000, luviPoolAsset/1_000)
			}
		}},
		{"add RUNE only", func(e *luviEnv) {
			if !e.state().IsOverhang() {
				e.addLiquidity(luviPoolRune/2_000, 0)
			}
		}},
		{"add asset only", func(e *luviEnv) {
			if !e.state().IsOverhang() {
				e.addLiquidity(0, luviPoolAsset/2_000)
			}
		}},
		{"withdraw symmetric", func(e *luviEnv) { e.withdraw(50, common.EmptyAsset) }},
		{"withdraw to RUNE", func(e *luviEnv) { e.withdraw(30, common.RuneAsset()) }},
		{"withdraw to asset", func(e *luviEnv) { e.withdraw(30, common.ETHAsset) }},
		{"block reward", func(e *luviEnv) { e.payReward(luviPoolRune / 50_000) }},
		{"donation", func(e *luviEnv) { e.donate(luviPoolRune/100_000, luviPoolAsset/100_000) }},
	}
	if withSynths {
		ops = append(ops,
			realOp{"synth mint", func(e *luviEnv) { e.mintSynth(luviPoolRune / 2_000) }},
			realOp{"synth burn", func(e *luviEnv) {
				if !e.synthSupply().IsZero() {
					e.burnSynth(e.synthSupply().QuoUint64(200))
				}
			}},
		)
	}
	return ops
}

// ----------------------------------------------------------------------------
// headline identities against real handler sequences
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestWithoutSynthsLuviStarIsClassicLuviThroughRealOps(c *C) {
	// Without synths, LUVI* must be exactly classic LUVI = sqrt(A*R)/L, C must
	// stay 0, and every step must satisfy dG = L_new * dLUVI.
	e := newLuviEnv(c, luviTestRegimes[0])
	t, err := types2.NewLuviTracker(e.state())
	c.Assert(err, IsNil)
	ops := legitOps(false)
	for i := 0; i < 3*len(ops); i++ {
		op := ops[i%len(ops)]
		prev := e.state()
		op.run(e)
		next := e.state()
		step, err := t.Observe(next)
		c.Assert(err, IsNil)
		cm := Commentf("step %d %s", i, op.name)
		c.Check(t.C().IsZero(), Equals, true, cm)
		c.Check(step.DeltaC.IsZero(), Equals, true, cm)
		// LUVI* == classic LUVI (the chain value with no synth units)
		c.Check(lessThan(normDiff(t.LuviStar(), next.ChainLuvi(), next), 1e-15), Equals, true, Commentf("%s: LUVI* %s classic %s", cm.CheckCommentString(), t.LuviStar(), next.ChainLuvi()))
		// dG == L_new * dLUVI
		want := next.ChainLuvi().Sub(prev.ChainLuvi()).MulInt(sdkmath.NewIntFromBigInt(next.LPUnits.BigInt()))
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-12), Equals, true, Commentf("%s: dG %s L*dLUVI %s", cm.CheckCommentString(), step.DeltaG, want))
		e.assertNotDown(step, cm.CheckCommentString())
	}
}

func (s *LuviInvariantSuite) TestWithSynthsGEqualsLTimesLuviStarThroughRealOps(c *C) {
	// Below overhang, every step must satisfy dG = L_new * dLUVI* (on-chain
	// flooring of synth units is the only difference, and it is tiny).
	for _, r := range []luviRegime{{"light synths", 40}, {"heavy synths", 180}} {
		e := newLuviEnv(c, r)
		t, err := types2.NewLuviTracker(e.state())
		c.Assert(err, IsNil)
		prevStar := t.LuviStar()
		ops := legitOps(true)
		for i := 0; i < 3*len(ops); i++ {
			op := ops[i%len(ops)]
			prev := e.state()
			op.run(e)
			next := e.state()
			if next.IsOverhang() {
				break
			}
			step, err := t.Observe(next)
			c.Assert(err, IsNil)
			star := t.LuviStar()
			want := star.Sub(prevStar).MulInt(sdkmath.NewIntFromBigInt(next.LPUnits.BigInt()))
			cm := Commentf("%s step %d %s: dG %s L*dLUVI* %s", r.name, i, op.name, step.DeltaG, want)
			c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-12), Equals, true, cm)
			e.assertNotDown(step, cm.CheckCommentString())
			prevStar = star
		}
	}
}

func (s *LuviInvariantSuite) TestLuviStarIsStableWhileClassicLuviSwingsWithPrice(c *C) {
	// The point of the fix: with synths, a pure price move leaves LUVI* where it
	// was while classic LUVI moves. Without synths, both stay put.
	for _, r := range []luviRegime{{"no synths", 0}, {"light synths", 40}, {"heavy synths", 180}} {
		e := newLuviEnv(c, r)
		t, err := types2.NewLuviTracker(e.state())
		c.Assert(err, IsNil)
		classic0 := e.state().ChainLuvi()
		star0 := t.LuviStar()
		e.swapRuneToAsset(luviPoolRune / 5) // push the asset price up ~44%
		_, err = t.Observe(e.state())
		c.Assert(err, IsNil)
		classicMove := e.state().ChainLuvi().Sub(classic0).Quo(classic0).Abs()
		starMove := t.LuviStar().Sub(star0).Quo(star0).Abs()
		cm := Commentf("%s: classic moved %s, LUVI* moved %s", r.name, classicMove, starMove)
		c.Check(starMove.LTE(flatTol), Equals, true, cm)
		if r.pct == 0 {
			c.Check(classicMove.LTE(flatTol), Equals, true, cm)
		} else {
			c.Check(classicMove.GT(decf(0.01)), Equals, true, cm)
		}
	}
}

// ----------------------------------------------------------------------------
// exact loss magnitudes
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestLossMagnitudesAreExact(c *C) {
	// dG = d sqrt(AR) - (avg sqrt(p)/2) * dS - dL * LUVI_chain(prev), so each
	// kind of loss must move G by exactly its own term.
	for _, r := range luviTestRegimes {
		// stolen asset: dG = d sqrt(AR)
		e := newLuviEnv(c, r)
		prev := e.state()
		step := e.step(func() {
			e.setPool(func(p *Pool) { p.BalanceAsset = p.BalanceAsset.Sub(p.BalanceAsset.QuoUint64(1_000)) })
		})
		next := e.state()
		want := next.SqrtAR().Sub(prev.SqrtAR())
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s asset theft: dG %s want %s", r.name, step.DeltaG, want))

		// stolen RUNE: dG = d sqrt(AR)
		e = newLuviEnv(c, r)
		prev = e.state()
		step = e.step(func() { e.setPool(func(p *Pool) { p.BalanceRune = p.BalanceRune.Sub(p.BalanceRune.QuoUint64(1_000)) }) })
		next = e.state()
		want = next.SqrtAR().Sub(prev.SqrtAR())
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s RUNE theft: dG %s want %s", r.name, step.DeltaG, want))

		// free synths: dG = -sqrt(p)/2 * dS (price unchanged)
		e = newLuviEnv(c, r)
		prev = e.state()
		minted := cosmos.NewUint(luviPoolAsset / 1_000)
		step = e.step(func() { e.mintSynths(minted) })
		sqrtP := prev.SqrtAR().QuoInt(sdkmath.NewIntFromBigInt(prev.BalanceAsset.BigInt()))
		want = sqrtP.MulInt(sdkmath.NewIntFromBigInt(minted.BigInt())).QuoInt64(2).Neg()
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s free synths: dG %s want %s", r.name, step.DeltaG, want))

		// over-minted units: dG = -dL * LUVI_chain(prev)
		e = newLuviEnv(c, r)
		prev = e.state()
		extra := prev.LPUnits.QuoUint64(1_000)
		step = e.step(func() { e.setPool(func(p *Pool) { p.LPUnits = p.LPUnits.Add(extra) }) })
		want = prev.ChainLuvi().MulInt(sdkmath.NewIntFromBigInt(extra.BigInt())).Neg()
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s over-mint: dG %s want %s", r.name, step.DeltaG, want))
	}
}

// ----------------------------------------------------------------------------
// remaining real code paths
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestOutboundFeeIsFlat(c *C) {
	// the outbound fee is taken in the asset and swapped to RUNE for the
	// Reserve at constant product (manager_txout_current.go)
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		txOut := newTxOutStorage(e.mgr.Keeper(), e.mgr.GetConstants(), e.mgr.EventMgr(), e.mgr.GasMgr())
		fee, err := txOut.gasManager.GetAssetOutboundFee(e.ctx, e.asset, false)
		c.Assert(err, IsNil)
		c.Assert(fee.IsZero(), Equals, false)
		var ok bool
		step := e.step(func() {
			ok, err = txOut.TryAddTxOutItem(e.ctx, e.mgr, TxOutItem{
				Chain:     common.ETHChain,
				ToAddress: GetRandomETHAddress(),
				InHash:    GetRandomTxHash(),
				Coin:      common.NewCoin(e.asset, cosmos.NewUint(10*common.One)),
			}, cosmos.ZeroUint())
		})
		c.Assert(err, IsNil)
		c.Assert(ok, Equals, true)
		c.Check(e.pool().BalanceAsset.GT(e.baseline.BalanceAsset), Equals, true, Commentf("%s: fee did not reach the pool", r.name))
		e.assertFlat(step, r.name+" outbound fee")
	}
}

func (s *LuviInvariantSuite) TestDoubleSwapIsFlatInBothPools(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		btc := NewPool()
		btc.Asset = common.BTCAsset
		btc.BalanceRune = cosmos.NewUint(luviPoolRune)
		btc.BalanceAsset = cosmos.NewUint(luviPoolAsset / 50)
		btc.LPUnits = cosmos.NewUint(luviPoolRune)
		btc.Status = PoolAvailable
		c.Assert(e.mgr.Keeper().SetPool(e.ctx, btc), IsNil)
		btcState := func() types2.LuviState {
			p, err := e.mgr.Keeper().GetPool(e.ctx, common.BTCAsset)
			c.Assert(err, IsNil)
			return types2.NewLuviState(p, e.mgr.Keeper().GetTotalSupply(e.ctx, common.BTCAsset.GetSyntheticAsset()))
		}
		tEth, err := types2.NewLuviTracker(e.state())
		c.Assert(err, IsNil)
		tBtc, err := types2.NewLuviTracker(btcState())
		c.Assert(err, IsNil)
		_, err = e.swap(common.NewCoin(e.asset, cosmos.NewUint(luviPoolAsset/100)), common.BTCAsset, GetRandomBTCAddress())
		c.Assert(err, IsNil)
		stepEth, err := tEth.Observe(e.state())
		c.Assert(err, IsNil)
		stepBtc, err := tBtc.Observe(btcState())
		c.Assert(err, IsNil)
		e.assertFlat(stepEth, r.name+" double swap, ETH pool")
		e.assertFlat(stepBtc, r.name+" double swap, BTC pool")
	}
}

func (s *LuviInvariantSuite) TestTradeAssetSwapsAreFlat(c *C) {
	// trade-asset swaps settle against the same L1 pool
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		owner := GetRandomBech32Addr()
		trade := e.asset.GetTradeAsset()
		amt := cosmos.NewUint(luviPoolAsset / 100)
		_, err := e.mgr.TradeAccountManager().Deposit(e.ctx, trade, amt, owner, common.NoAddress, common.BlankTxID)
		c.Assert(err, IsNil)
		step := e.step(func() {
			tx := common.NewTx(GetRandomTxHash(), common.Address(owner.String()), GetRandomTHORAddress(),
				common.NewCoins(common.NewCoin(trade, amt)), common.Gas{}, "")
			tx.Chain = common.THORChain
			_, _, _, err = newSwapper().Swap(e.ctx, e.mgr.Keeper(), tx, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, e.mult, e.mgr, nil, nil)
		})
		c.Assert(err, IsNil)
		c.Check(e.pool().BalanceAsset.GT(e.baseline.BalanceAsset), Equals, true, Commentf("%s: trade swap did not reach the pool", r.name))
		e.assertFlat(step, r.name+" trade asset -> RUNE")
	}
}

func (s *LuviInvariantSuite) TestSixDecimalAssetNeverLowersG(c *C) {
	// RoundToDecimal truncates the emitted asset to the pool's decimals, which
	// leaves the remainder in the pool
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		e.setPool(func(p *Pool) { p.Decimals = 6 })
		up := 0
		for i, frac := range []uint64{100_000, 12_345, 1_000, 333, 100} {
			step := e.step(func() { e.swapRuneToAsset(luviPoolRune/frac + 7) })
			e.assertNotDown(step, fmt.Sprintf("%s 6-decimal swap %d", r.name, i))
			if step.NormalizedDeltaG.IsPositive() {
				up++
			}
		}
		c.Check(up > 0, Equals, true, Commentf("%s: truncation never reached the pool", r.name))
	}
}

func (s *LuviInvariantSuite) TestWithdrawWithMinSlipNeverLowersG(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		e.mgr.Keeper().SetMimir(e.ctx, constants.L1SlipMinBps.String(), 50)
		for _, to := range []common.Asset{common.RuneAsset(), common.ETHAsset} {
			e.assertNotDown(e.step(func() { e.withdraw(500, to) }), fmt.Sprintf("%s min-slip withdraw to %s", r.name, to))
		}
	}
}

func (s *LuviInvariantSuite) TestPOLAddAndRemove(c *C) {
	// POL deposits are single-sided RUNE adds; POL withdrawals waive the slip
	// fee, so they are exactly flat. Deposits in overhang are not exercised
	// (see TestOverhangDepositMultipliesUnits).
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		polAddr, err := e.mgr.Keeper().GetModuleAddress(ReserveName)
		c.Assert(err, IsNil)
		asgardAddr, err := e.mgr.Keeper().GetModuleAddress(AsgardName)
		c.Assert(err, IsNil)
		nas, err := e.mgr.Keeper().ListActiveValidators(e.ctx)
		c.Assert(err, IsNil)
		signer := nas[0].NodeAddress
		nm := newNetworkMgr(e.mgr.Keeper(), e.mgr.TxOutStore(), e.mgr.EventMgr())
		move := cosmos.NewUint(10_000) // 1% of the RUNE depth per call

		if !e.state().IsOverhang() {
			e.ctx = e.ctx.WithEventManager(cosmos.NewEventManager())
			before := e.pool().LPUnits
			step := e.step(func() {
				c.Assert(nm.addPOLLiquidity(e.ctx, e.pool(), polAddr, asgardAddr, signer, move, cosmos.NewUint(10_000), cosmos.ZeroUint(), e.mgr, nil), IsNil)
			})
			e.assertNotDown(step, r.name+" POL add")
			units := addEventUnits(e.ctx)
			c.Check(units.IsZero(), Equals, false, Commentf("%s: POL add minted no units", r.name))
			c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, []cosmos.Uint{units}, nil), IsNil, Commentf(r.name+" POL add ledger"))
		} else {
			e.SetLiquidityProviderUnits(polAddr, e.pool().LPUnits.QuoUint64(10))
		}

		lp, err := e.mgr.Keeper().GetLiquidityProvider(e.ctx, e.asset, polAddr)
		c.Assert(err, IsNil)
		before := e.pool().LPUnits
		step := e.step(func() {
			c.Assert(nm.removePOLLiquidity(e.ctx, e.pool(), polAddr, asgardAddr, signer, move, cosmos.ZeroUint(), cosmos.NewUint(10_000), e.mgr), IsNil)
		})
		after, err := e.mgr.Keeper().GetLiquidityProvider(e.ctx, e.asset, polAddr)
		c.Assert(err, IsNil)
		c.Check(after.Units.LT(lp.Units), Equals, true, Commentf("%s: POL remove burned no units", r.name))
		e.assertFlat(step, r.name+" POL remove")
		c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, nil, []cosmos.Uint{lp.Units.Sub(after.Units)}), IsNil, Commentf(r.name+" POL remove ledger"))
	}
}

// SetLiquidityProviderUnits gives addr an LP position carved out of the
// existing units (the pool's LP units are unchanged).
func (e *luviEnv) SetLiquidityProviderUnits(addr common.Address, units cosmos.Uint) {
	lp, err := e.mgr.Keeper().GetLiquidityProvider(e.ctx, e.asset, e.lpAddr)
	e.c.Assert(err, IsNil)
	lp.Units = lp.Units.Sub(units)
	e.mgr.Keeper().SetLiquidityProvider(e.ctx, lp)
	e.mgr.Keeper().SetLiquidityProvider(e.ctx, LiquidityProvider{Asset: e.asset, RuneAddress: addr, Units: units})
}

func (s *LuviInvariantSuite) TestPendingLiquidityCommitNeverLowersG(c *C) {
	// aged pending liquidity is committed as an internal single-sided add
	for _, r := range luviTestRegimes {
		if r.pct >= 200 {
			continue // deposits into overhang: see TestOverhangDepositMultipliesUnits
		}
		e := newLuviEnv(c, r)
		pending := cosmos.NewUint(luviPoolRune / 1_000)
		e.setPool(func(p *Pool) { p.PendingInboundRune = p.PendingInboundRune.Add(pending) })
		e.mgr.Keeper().SetLiquidityProvider(e.ctx, LiquidityProvider{
			Asset: e.asset, RuneAddress: GetRandomRUNEAddress(), AssetAddress: GetRandomETHAddress(),
			Units: cosmos.ZeroUint(), PendingRune: pending, PendingAsset: cosmos.ZeroUint(), LastAddHeight: 1,
		})
		age := e.mgr.Keeper().GetConfigInt64(e.ctx, constants.PendingLiquidityAgeLimit)
		e.ctx = e.ctx.WithBlockHeight(age + 10).WithEventManager(cosmos.NewEventManager())
		before := e.pool().LPUnits
		pm := newPoolMgr()
		step := e.step(func() { c.Assert(pm.commitPendingLiquidity(e.ctx, e.pool(), e.mgr), IsNil) })
		units := addEventUnits(e.ctx)
		c.Check(units.IsZero(), Equals, false, Commentf("%s: pending liquidity was not committed", r.name))
		e.assertNotDown(step, r.name+" pending commit")
		c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, []cosmos.Uint{units}, nil), IsNil, Commentf(r.name+" pending commit ledger"))
	}
}

func (s *LuviInvariantSuite) TestStagedPoolCostLowersGByItsSize(c *C) {
	// a staged pool pays a fixed RUNE fee to the Reserve each cycle: a known
	// drop of exactly d sqrt(AR). Gas-asset pools are exempt (cyclePools skips
	// them), so this uses a token pool.
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	for _, r := range luviTestRegimes {
		e := newLuviEnvFor(c, r, usdc)
		e.setPool(func(p *Pool) { p.Status = PoolStaged })
		prev := e.state()
		cost := int64(10 * common.One)
		step := e.step(func() { c.Assert(newPoolMgr().cyclePools(e.ctx, 0, 0, cost, e.mgr), IsNil) })
		next := e.state()
		c.Check(prev.BalanceRune.Sub(next.BalanceRune).Uint64(), Equals, uint64(cost), Commentf(r.name))
		want := next.SqrtAR().Sub(prev.SqrtAR())
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s staged cost: dG %s want %s", r.name, step.DeltaG, want))
		e.assertDown(step, r.name+" staged pool cost")
	}
}

func (s *LuviInvariantSuite) TestSaverYieldWithoutRewardLowersG(c *C) {
	// Saver yield mints synths to savers; on its own (no matching RUNE entering
	// the pool) it is a transfer from LPs to savers and must read as a drop of
	// sqrt(p)/2 per synth. Off on mainnet (SynthYieldBasisPoints = 0).
	for _, r := range luviTestRegimes {
		if r.pct == 0 {
			continue // no savers without synths
		}
		e := newLuviEnv(c, r)
		saver := NewPool()
		saver.Asset = e.asset.GetSyntheticAsset()
		saver.BalanceAsset = e.synthSupply()
		saver.LPUnits = saver.BalanceAsset
		saver.Status = PoolAvailable
		c.Assert(e.mgr.Keeper().SetPool(e.ctx, saver), IsNil)
		e.mgr.Keeper().SetMimir(e.ctx, constants.SynthYieldBasisPoints.String(), 5_000)
		e.mgr.Keeper().SetMimir(e.ctx, constants.MaxSynthsForSaversYield.String(), 100_000)
		nm := newNetworkMgr(e.mgr.Keeper(), e.mgr.TxOutStore(), e.mgr.EventMgr())
		prev := e.state()
		step := e.step(func() { c.Assert(nm.paySaverYield(e.ctx, e.asset, cosmos.NewUint(luviPoolRune/1_000)), IsNil) })
		next := e.state()
		minted := next.SynthSupply.Sub(prev.SynthSupply)
		c.Assert(minted.IsZero(), Equals, false, Commentf("%s: no saver yield paid", r.name))
		sqrtP := prev.SqrtAR().QuoInt(sdkmath.NewIntFromBigInt(prev.BalanceAsset.BigInt()))
		want := sqrtP.MulInt(sdkmath.NewIntFromBigInt(minted.BigInt())).QuoInt64(2).Neg()
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s saver yield: dG %s want %s", r.name, step.DeltaG, want))
	}
}

// ----------------------------------------------------------------------------
// block-level observation (what an external monitor sees)
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestBlockLevelObservationWithoutLPFlows(c *C) {
	// Many operations between two observations: with no LP-unit change inside
	// the step, G is still never lowered by legitimate activity.
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		step := e.step(func() {
			e.swapRuneToAsset(luviPoolRune / 40)
			e.swapAssetToRune(luviPoolAsset / 70)
			if !e.state().IsOverhang() || r.pct > 0 {
				e.mintSynth(luviPoolRune / 3_000)
			}
			e.payReward(luviPoolRune / 100_000)
			e.swapAssetToRune(luviPoolAsset / 90)
		})
		e.assertNotDown(step, r.name+" block of swaps, mint and reward")
	}
}

func (s *LuviInvariantSuite) TestBlockLevelObservationWithLPFlowNeedsPerBlockReads(c *C) {
	// An LP flow credited at the per-unit value from before an earlier price
	// move in the same step can read slightly down. Observing every operation
	// never does; the block-level error is bounded by |dL| * |dLUVI_chain|.
	for _, r := range []luviRegime{{"light synths", 40}, {"heavy synths", 180}} {
		e := newLuviEnv(c, r)
		start := e.state()
		t, err := types2.NewLuviTracker(start)
		c.Assert(err, IsNil)

		// per-operation observation
		e.swapRuneToAsset(luviPoolRune / 20)
		mid := e.state()
		s1, err := t.Observe(mid)
		c.Assert(err, IsNil)
		e.assertNotDown(s1, r.name+" per-op: price move")
		e.withdraw(2_000, common.RuneAsset())
		end := e.state()
		s2, err := t.Observe(end)
		c.Assert(err, IsNil)
		e.assertNotDown(s2, r.name+" per-op: withdrawal")

		// the same two operations seen as one step
		tb, err := types2.NewLuviTracker(start)
		c.Assert(err, IsNil)
		sb, err := tb.Observe(end)
		c.Assert(err, IsNil)
		dL := new(big.Float).SetInt(new(big.Int).Sub(end.LPUnits.BigInt(), start.LPUnits.BigInt()))
		dL.Abs(dL)
		dv := new(big.Float).Sub(decToFloat(mid.ChainLuvi()), decToFloat(start.ChainLuvi()))
		dv.Abs(dv)
		bound := new(big.Float).Mul(dL, dv)
		bound.Quo(bound, sqrtARf(start))
		bound.Add(bound, big.NewFloat(1e-9))
		drop := decToFloat(sb.NormalizedDeltaG)
		c.Check(drop.Cmp(new(big.Float).Neg(bound)) >= 0, Equals, true, Commentf("%s block-level %s, bound %s", r.name, sb.NormalizedDeltaG, bound.String()))
	}
}

// ----------------------------------------------------------------------------
// errata (reorged inbounds), through the real errata handler
// ----------------------------------------------------------------------------

// errata records tx as a finalised observed inbound and has the only active
// validator attest an errata for it.
func (e *luviEnv) errata(tx common.Tx) {
	obs := common.ObservedTx{Tx: tx}
	e.mgr.Keeper().SetObservedTxInVoter(e.ctx, ObservedTxVoter{TxID: tx.ID, Tx: obs, Txs: ObservedTxs{obs}, Height: 1})
	nas, err := e.mgr.Keeper().ListActiveValidators(e.ctx)
	e.c.Assert(err, IsNil)
	e.c.Assert(nas, HasLen, 1)
	_, err = NewErrataTxHandler(e.mgr).handle(e.ctx, *NewMsgErrataTx(tx.ID, tx.Chain, nas[0].NodeAddress))
	e.c.Assert(err, IsNil)
}

func (s *LuviInvariantSuite) TestErrataOfSwapInboundIsAnExactLoss(c *C) {
	// The swap already paid out; reversing its inbound removes that asset from
	// the pool with no units burned: a first-order loss of exactly d sqrt(AR).
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		amt := luviPoolAsset / 200
		from := GetRandomETHAddress()
		tx := common.NewTx(GetRandomTxHash(), from, GetRandomETHAddress(),
			common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(amt))), common.Gas{common.NewCoin(e.asset, cosmos.NewUint(1))},
			"SWAP:THOR.RUNE:"+GetRandomTHORAddress().String())
		tx.Chain = common.ETHChain
		_, err := e.swap(common.NewCoin(e.asset, cosmos.NewUint(amt)), common.RuneAsset(), GetRandomTHORAddress())
		c.Assert(err, IsNil)

		prev := e.state()
		step := e.step(func() { e.errata(tx) })
		next := e.state()
		c.Check(prev.BalanceAsset.Sub(next.BalanceAsset).Uint64(), Equals, amt, Commentf("%s: errata did not reverse the inbound", r.name))
		want := next.SqrtAR().Sub(prev.SqrtAR())
		c.Check(lessThan(normDiff(step.DeltaG, want, prev), 1e-15), Equals, true, Commentf("%s swap errata: dG %s want %s", r.name, step.DeltaG, want))
		e.assertDown(step, r.name+" swap errata")
	}
}

func (s *LuviInvariantSuite) TestErrataOfAddInboundUndoesTheDeposit(c *C) {
	// Reversing a single-sided add removes the deposit and zeroes the LP's
	// units, so the pool returns exactly to where it started. Step by step:
	// the deposit reads up (the other LPs keep its slip) and the errata reads
	// down (they give it back) -- with synths it can read slightly up instead,
	// see below. G is path-dependent across LP flows: the
	// deposit's units are credited at the per-unit value before it (v0) and
	// burned at the value after it (v1), so the two steps net to exactly
	// u * (v1 - v0) rather than zero. LUVI* per unit, a state function when
	// there are no synths, returns exactly to its start.
	for _, r := range luviTestRegimes {
		if r.pct >= 200 {
			continue // deposits into overhang: see TestOverhangDepositMultipliesUnits
		}
		e := newLuviEnv(c, r)
		from := GetRandomETHAddress()
		amt := luviPoolAsset / 500
		start := e.state()
		t, err := types2.NewLuviTracker(start)
		c.Assert(err, IsNil)
		star0 := t.LuviStar()

		// asset-only deposit keyed by the sender's asset address
		h := NewAddLiquidityHandler(e.mgr)
		c.Assert(h.addLiquidity(e.ctx, e.asset, cosmos.ZeroUint(), cosmos.NewUint(amt), common.NoAddress, from,
			GetRandomTxHash(), false, constants.GetConstantValues(GetCurrentVersion())), IsNil)
		mid := e.state()
		sAdd, err := t.Observe(mid)
		c.Assert(err, IsNil)
		e.assertUp(sAdd, r.name+" deposit keeps its slip in the pool")

		tx := common.NewTx(GetRandomTxHash(), from, GetRandomETHAddress(),
			common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(amt))), common.Gas{common.NewCoin(e.asset, cosmos.NewUint(1))},
			"ADD:"+e.asset.String())
		tx.Chain = common.ETHChain
		e.errata(tx)
		end := e.state()
		sErr, err := t.Observe(end)
		c.Assert(err, IsNil)
		if r.pct == 0 {
			// without synths the errata gives exactly the deposit's slip back
			e.assertDown(sErr, r.name+" errata gives the slip back")
		}
		// With synths the deposit lowered the price, repricing the synth
		// liability, so the units the errata burns are worth more than the
		// asset it returns: the step can read slightly up. An errata can only
		// reverse an external-chain deposit, which always lowers the price,
		// so it never reads as a false drop beyond the slip.
		e.assertBetween(sErr, sAdd.NormalizedDeltaG.Neg().Sub(flatTol), decf(1), r.name+" errata bounded below by the slip")

		cm := Commentf("%s: start %+v end %+v G %s", r.name, start, end, t.G())
		c.Check(end.BalanceAsset.Equal(start.BalanceAsset), Equals, true, cm)
		c.Check(end.BalanceRune.Equal(start.BalanceRune), Equals, true, cm)
		c.Check(end.LPUnits.Equal(start.LPUnits), Equals, true, cm)
		c.Check(end.SynthSupply.Equal(start.SynthSupply), Equals, true, cm)

		// the loop residual is exactly u * (v1 - v0)
		u := mid.LPUnits.Sub(start.LPUnits)
		want := mid.ChainLuvi().Sub(start.ChainLuvi()).MulInt(sdkmath.NewIntFromBigInt(u.BigInt()))
		c.Check(lessThan(normDiff(t.G(), want, start), 1e-15), Equals, true, Commentf("%s: G %s, u*(v1-v0) %s", r.name, t.G(), want))
		if r.pct == 0 {
			c.Check(lessThan(normDiff(t.LuviStar(), star0, start), 1e-15), Equals, true, Commentf("%s: LUVI* %s start %s", r.name, t.LuviStar(), star0))
		}
	}
}
