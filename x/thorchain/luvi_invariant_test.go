package thorchain

import (
	"errors"
	"fmt"
	"math/big"

	sdkmath "cosmossdk.io/math"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// These tests drive the real pool-mutating code paths (swapper, add/withdraw
// liquidity, gas manager, slasher, network rewards, donate) and check how the
// synth-aware LUVI indices respond, in every synth regime:
//
//   - legitimate operations never lower G beyond integer rounding
//   - routine protocol costs (gas, slash) lower it by a small, bounded amount
//   - losses (theft, free synths, over-minted units) always lower it
//   - the unit ledger explains every LP-unit change the handlers make
//
// See x/thorchain/types/type_luvi.go for the definitions.

type LuviInvariantSuite struct{}

var _ = Suite(&LuviInvariantSuite{})

// luviRegime is a synth supply expressed as a percentage of asset depth.
type luviRegime struct {
	name string
	pct  uint64
}

var luviTestRegimes = []luviRegime{
	{"no synths", 0},
	{"light synths", 40},
	{"heavy synths", 180},
	{"boundary S=2A", 200},
	{"overhang", 300},
	{"deep overhang", 700},
}

// flatTol is the largest per-step |dG/sqrt(AR)| that integer rounding in the
// handlers can produce on the test pool (depths >= 1e12 base units).
var flatTol = sdkmath.LegacyMustNewDecFromStr("0.000000001") // 1e-9

const (
	luviPoolRune  uint64 = 1_000_000 * common.One
	luviPoolAsset uint64 = 10_000 * common.One
)

type luviEnv struct {
	c        *C
	ctx      cosmos.Context
	mgr      *Mgrs
	asset    common.Asset
	lpAddr   common.Address
	vault    Vault
	mult     int64
	baseline types2.LuviState
}

func newLuviEnv(c *C, r luviRegime) *luviEnv {
	return newLuviEnvFor(c, r, common.ETHAsset)
}

// newLuviEnvFor builds the test pool for any asset on the ETH chain.
func newLuviEnvFor(c *C, r luviRegime, asset common.Asset) *luviEnv {
	ctx, mgr := setupManagerForTest(c)
	e := &luviEnv{c: c, ctx: ctx, mgr: mgr, asset: asset, lpAddr: GetRandomRUNEAddress(), mult: 10_000}

	pool := NewPool()
	pool.Asset = e.asset
	pool.BalanceRune = cosmos.NewUint(luviPoolRune)
	pool.BalanceAsset = cosmos.NewUint(luviPoolAsset)
	pool.LPUnits = cosmos.NewUint(luviPoolRune)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset:        e.asset,
		RuneAddress:  e.lpAddr,
		AssetAddress: GetRandomETHAddress(),
		Units:        pool.LPUnits,
	})

	// module balances backing the pool, rewards and gas
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 100*luviPoolRune)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 100*luviPoolRune)

	e.vault = GetRandomVault()
	e.vault.Type = AsgardVault
	e.vault.Status = types2.VaultStatus_ActiveVault
	e.vault.Coins = common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(100*luviPoolAsset)))
	c.Assert(mgr.Keeper().SetVault(ctx, e.vault), IsNil)

	// an active validator: POL, pending-liquidity commits and pool cycling
	// sign their internal messages with one
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)

	if r.pct > 0 {
		e.mintSynths(cosmos.NewUint(luviPoolAsset * r.pct / 100))
	}
	e.baseline = e.state()
	return e
}

func (e *luviEnv) mintSynths(amt cosmos.Uint) {
	coin := common.NewCoin(e.asset.GetSyntheticAsset(), amt)
	e.c.Assert(e.mgr.Keeper().MintToModule(e.ctx, ModuleName, coin), IsNil)
	e.c.Assert(e.mgr.Keeper().SendFromModuleToModule(e.ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)
}

func (e *luviEnv) pool() Pool {
	p, err := e.mgr.Keeper().GetPool(e.ctx, e.asset)
	e.c.Assert(err, IsNil)
	return p
}

func (e *luviEnv) synthSupply() cosmos.Uint {
	return e.mgr.Keeper().GetTotalSupply(e.ctx, e.asset.GetSyntheticAsset())
}

func (e *luviEnv) state() types2.LuviState {
	return types2.NewLuviState(e.pool(), e.synthSupply())
}

// step runs op and returns the LUVI step it caused.
func (e *luviEnv) step(op func()) types2.LuviStep {
	t, err := types2.NewLuviTracker(e.state())
	e.c.Assert(err, IsNil)
	op()
	s, err := t.Observe(e.state())
	e.c.Assert(err, IsNil)
	return s
}

// ----------------------------------------------------------------------------
// operations, all through real code paths
// ----------------------------------------------------------------------------

func (e *luviEnv) swap(coin common.Coin, target common.Asset, dest common.Address) (cosmos.Uint, error) {
	chain := coin.Asset.GetChain()
	if coin.Asset.IsSyntheticAsset() || coin.Asset.IsRune() {
		chain = common.THORChain
	}
	tx := common.NewTx(GetRandomTxHash(), dest, dest, common.NewCoins(coin), common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}, "")
	tx.Chain = chain
	out, _, _, err := newSwapper().Swap(e.ctx, e.mgr.Keeper(), tx, target, dest, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, e.mult, e.mgr, nil, nil)
	return out, err
}

func (e *luviEnv) swapRuneToAsset(amt uint64) {
	_, err := e.swap(common.NewCoin(common.RuneAsset(), cosmos.NewUint(amt)), e.asset, GetRandomETHAddress())
	e.c.Assert(err, IsNil)
}

func (e *luviEnv) swapAssetToRune(amt uint64) {
	_, err := e.swap(common.NewCoin(e.asset, cosmos.NewUint(amt)), common.RuneAsset(), GetRandomTHORAddress())
	e.c.Assert(err, IsNil)
}

// mintSynth swaps RUNE for synths and returns the synths emitted.
func (e *luviEnv) mintSynth(amt uint64) cosmos.Uint {
	out, err := e.swap(common.NewCoin(common.RuneAsset(), cosmos.NewUint(amt)), e.asset.GetSyntheticAsset(), GetRandomTHORAddress())
	e.c.Assert(err, IsNil)
	return out
}

// burnSynth swaps existing synths back to RUNE. The synths are taken from the
// supply already outstanding, so the burn is a real reduction of S.
func (e *luviEnv) burnSynth(amt cosmos.Uint) cosmos.Uint {
	dest := GetRandomTHORAddress()
	tx := common.NewTx(GetRandomTxHash(), dest, dest, common.NewCoins(common.NewCoin(e.asset.GetSyntheticAsset(), amt)), common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}, "")
	tx.Chain = common.THORChain
	out, _, _, err := newSwapper().Swap(e.ctx, e.mgr.Keeper(), tx, common.RuneAsset(), dest, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, e.mult, e.mgr, nil, nil)
	e.c.Assert(err, IsNil)
	return out
}

// addLiquidity adds through the real handler and returns the units the
// add_liquidity event reported.
func (e *luviEnv) addLiquidity(runeAmt, assetAmt uint64) cosmos.Uint {
	units, err := e.tryAddLiquidity(runeAmt, assetAmt)
	e.c.Assert(err, IsNil)
	return units
}

// tryAddLiquidity is addLiquidity returning the handler error instead of
// asserting on it.
func (e *luviEnv) tryAddLiquidity(runeAmt, assetAmt uint64) (cosmos.Uint, error) {
	e.ctx = e.ctx.WithEventManager(cosmos.NewEventManager())
	h := NewAddLiquidityHandler(e.mgr)
	err := h.addLiquidity(e.ctx, e.asset, cosmos.NewUint(runeAmt), cosmos.NewUint(assetAmt),
		GetRandomRUNEAddress(), GetRandomETHAddress(), GetRandomTxHash(), false,
		constants.GetConstantValues(GetCurrentVersion()))
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	units := cosmos.ZeroUint()
	for _, ev := range e.ctx.EventManager().Events() {
		if ev.Type != types2.AddLiquidityEventType {
			continue
		}
		for _, attr := range ev.Attributes {
			if attr.Key == "liquidity_provider_units" {
				units = units.Add(cosmos.NewUintFromString(attr.Value))
			}
		}
	}
	return units, nil
}

// withdraw withdraws bps of the test LP's units through withdraw() and returns
// the units burned from the LP record.
func (e *luviEnv) withdraw(bps uint64, to common.Asset) cosmos.Uint {
	before, err := e.mgr.Keeper().GetLiquidityProvider(e.ctx, e.asset, e.lpAddr)
	e.c.Assert(err, IsNil)
	msg := MsgWithdrawLiquidity{
		WithdrawAddress: e.lpAddr,
		BasisPoints:     cosmos.NewUint(bps),
		Asset:           e.asset,
		Tx:              common.Tx{ID: GetRandomTxHash()},
		WithdrawalAsset: to,
		Signer:          GetRandomBech32Addr(),
	}
	_, _, _, _, err = withdraw(e.ctx, msg, e.mgr)
	e.c.Assert(err, IsNil)
	after, err := e.mgr.Keeper().GetLiquidityProvider(e.ctx, e.asset, e.lpAddr)
	e.c.Assert(err, IsNil)
	return before.Units.Sub(after.Units)
}

func (e *luviEnv) payReward(amt uint64) {
	nm := newNetworkMgr(e.mgr.Keeper(), e.mgr.TxOutStore(), e.mgr.EventMgr())
	e.c.Assert(nm.payPoolRewards(e.ctx, []cosmos.Uint{cosmos.NewUint(amt)}, Pools{e.pool()}), IsNil)
}

func (e *luviEnv) donate(runeAmt, assetAmt uint64) {
	signer := GetRandomBech32Addr()
	tx := common.NewTx(GetRandomTxHash(), GetRandomETHAddress(), GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(runeAmt)), common.NewCoin(e.asset, cosmos.NewUint(assetAmt))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}, "DONATE:"+e.asset.String())
	msg := NewMsgDonate(tx, e.asset, cosmos.NewUint(runeAmt), cosmos.NewUint(assetAmt), signer)
	e.c.Assert(NewDonateHandler(e.mgr).handle(e.ctx, *msg), IsNil)
}

func (e *luviEnv) reimburseGas(amt uint64) {
	gm := e.mgr.GasMgr()
	gm.AddGasAsset(e.asset, common.Gas{common.NewCoin(e.asset, cosmos.NewUint(amt))}, true)
	gm.EndBlock(e.ctx, e.mgr.Keeper(), e.mgr.EventMgr())
}

// slashVault steals amt of the asset from a vault owned by a node with the
// given bond, and runs the real slasher.
func (e *luviEnv) slashVault(amt uint64, bond cosmos.Uint) {
	node := GetRandomValidatorNode(NodeActive)
	node.Bond = bond
	e.c.Assert(e.mgr.Keeper().SetNodeAccount(e.ctx, node), IsNil)
	FundModule(e.c, e.ctx, e.mgr.Keeper(), BondName, node.Bond.Uint64())
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = types2.VaultStatus_ActiveVault
	v.PubKey = node.PubKeySet.Secp256k1
	v.Membership = []string{node.PubKeySet.Secp256k1.String()}
	v.Coins = common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(2*amt)))
	e.c.Assert(e.mgr.Keeper().SetVault(e.ctx, v), IsNil)
	e.c.Assert(e.mgr.Slasher().SlashVault(e.ctx, v.PubKey, common.NewCoins(common.NewCoin(e.asset, cosmos.NewUint(amt))), e.mgr), IsNil)
}

func (e *luviEnv) setPool(mutate func(p *Pool)) {
	p := e.pool()
	mutate(&p)
	e.c.Assert(e.mgr.Keeper().SetPool(e.ctx, p), IsNil)
}

// ----------------------------------------------------------------------------
// assertions
// ----------------------------------------------------------------------------

func (e *luviEnv) assertFlat(s types2.LuviStep, what string) {
	e.c.Check(s.NormalizedDeltaG.Abs().LTE(flatTol), Equals, true, Commentf("%s: dG/sqrt(AR) = %s", what, s.NormalizedDeltaG))
}

func (e *luviEnv) assertNotDown(s types2.LuviStep, what string) {
	e.c.Check(s.NormalizedDeltaG.GTE(flatTol.Neg()), Equals, true, Commentf("%s: dG/sqrt(AR) = %s", what, s.NormalizedDeltaG))
}

func (e *luviEnv) assertUp(s types2.LuviStep, what string) {
	e.c.Check(s.NormalizedDeltaG.GT(flatTol), Equals, true, Commentf("%s: dG/sqrt(AR) = %s", what, s.NormalizedDeltaG))
}

func (e *luviEnv) assertDown(s types2.LuviStep, what string) {
	e.c.Check(s.NormalizedDeltaG.LT(flatTol.Neg()), Equals, true, Commentf("%s: dG/sqrt(AR) = %s", what, s.NormalizedDeltaG))
}

// assertBetween checks lo <= dG/sqrt(AR) <= hi.
func (e *luviEnv) assertBetween(s types2.LuviStep, lo, hi cosmos.Dec, what string) {
	v := s.NormalizedDeltaG
	e.c.Check(v.GTE(lo) && v.LTE(hi), Equals, true, Commentf("%s: dG/sqrt(AR) = %s not in [%s, %s]", what, v, lo, hi))
}

func decf(f float64) cosmos.Dec { return sdkmath.LegacyMustNewDecFromStr(fmt.Sprintf("%.18f", f)) }

// ----------------------------------------------------------------------------
// swaps
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestSwapsAreFlat(c *C) {
	for _, r := range luviTestRegimes {
		for _, frac := range []uint64{100_000, 1_000, 100, 10} { // 0.001% .. 10% of depth
			e := newLuviEnv(c, r)
			name := fmt.Sprintf("%s, 1/%d of depth", r.name, frac)
			e.assertFlat(e.step(func() { e.swapRuneToAsset(luviPoolRune / frac) }), name+" RUNE->asset")
			e.assertFlat(e.step(func() { e.swapAssetToRune(luviPoolAsset / frac) }), name+" asset->RUNE")
		}
	}
}

func (s *LuviInvariantSuite) TestSwapsWithMinSlipAreFlat(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		e.mgr.Keeper().SetMimir(e.ctx, constants.L1SlipMinBps.String(), 25)
		e.assertFlat(e.step(func() { e.swapRuneToAsset(luviPoolRune / 10_000) }), r.name+" min-slip RUNE->asset")
		e.assertFlat(e.step(func() { e.swapAssetToRune(luviPoolAsset / 10_000) }), r.name+" min-slip asset->RUNE")
	}
}

func (s *LuviInvariantSuite) TestManySwapsStayFlat(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		t, err := types2.NewLuviTracker(e.state())
		c.Assert(err, IsNil)
		for i := 0; i < 50; i++ {
			if i%2 == 0 {
				e.swapRuneToAsset(luviPoolRune / uint64(50+i))
			} else {
				e.swapAssetToRune(luviPoolAsset / uint64(50+i))
			}
			step, err := t.Observe(e.state())
			c.Assert(err, IsNil)
			e.assertFlat(step, fmt.Sprintf("%s swap %d", r.name, i))
		}
		// LUVI* must not drift either while below overhang (it is exactly 0 at
		// S = 2A, where a relative drift is undefined)
		if !e.baseline.IsOverhang() && !e.state().IsOverhang() {
			star := t.LuviStar()
			start := e.baseline.LPEquityPerUnit()
			diff := star.Sub(start).Abs().Quo(start)
			c.Check(diff.LTE(flatTol), Equals, true, Commentf("%s LUVI* drift %s", r.name, diff))
		}
	}
}

// ----------------------------------------------------------------------------
// synths
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestSynthMintAndBurnNeverLowerG(c *C) {
	for _, r := range luviTestRegimes {
		for _, frac := range []uint64{100_000, 1_000, 100, 10} {
			e := newLuviEnv(c, r)
			name := fmt.Sprintf("%s, 1/%d", r.name, frac)
			var minted cosmos.Uint
			e.assertNotDown(e.step(func() { minted = e.mintSynth(luviPoolRune / frac) }), name+" mint")
			e.assertNotDown(e.step(func() { e.burnSynth(minted) }), name+" burn")
		}
	}
}

func (s *LuviInvariantSuite) TestSynthRoundTripMatchesLPResult(c *C) {
	// mint then burn the same synths: A and S return to their start, so the
	// LPs' real result is the change in sqrt(A*R). G must agree in sign and
	// closely in size, at every virtual depth.
	for _, mult := range []int64{10_000, 15_000, 20_000} {
		for _, r := range luviTestRegimes {
			for _, frac := range []uint64{10_000, 100, 10} {
				e := newLuviEnv(c, r)
				e.mult = mult
				start := e.state()
				t, err := types2.NewLuviTracker(start)
				c.Assert(err, IsNil)
				minted := e.mintSynth(luviPoolRune / frac)
				_, err = t.Observe(e.state())
				c.Assert(err, IsNil)
				e.burnSynth(minted)
				end := e.state()
				_, err = t.Observe(end)
				c.Assert(err, IsNil)
				cm := Commentf("mult %d %s 1/%d: G=%s", mult, r.name, frac, t.G())
				c.Assert(end.SynthSupply.Equal(start.SynthSupply), Equals, true, cm)
				c.Assert(end.BalanceAsset.Equal(start.BalanceAsset), Equals, true, cm)
				truth := end.SqrtAR().Sub(start.SqrtAR())
				g := t.G()
				if truth.Abs().LT(cosmos.NewDec(1)) {
					// below one unit of sqrt(A*R): only rounding is left
					c.Check(g.Abs().LT(cosmos.NewDec(2)), Equals, true, cm)
					continue
				}
				c.Check(g.IsPositive(), Equals, truth.IsPositive(), cm)
				ratio := g.Quo(truth)
				c.Check(ratio.GTE(decf(0.98)) && ratio.LTE(decf(1.06)), Equals, true, Commentf("%s ratio %s", cm.CheckCommentString(), ratio))
			}
		}
	}
}

// ----------------------------------------------------------------------------
// liquidity
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestAddLiquidityNeverLowersGAndMatchesLedger(c *C) {
	type add struct {
		name      string
		rune, ast uint64
	}
	adds := []add{
		{"symmetric", luviPoolRune / 100, luviPoolAsset / 100},
		{"rune only", luviPoolRune / 100, 0},
		{"asset only", 0, luviPoolAsset / 100},
		{"lopsided", luviPoolRune / 10, luviPoolAsset / 1000},
		{"tiny", 1_000, 10},
	}
	for _, r := range luviTestRegimes {
		for _, a := range adds {
			e := newLuviEnv(c, r)
			name := fmt.Sprintf("%s add %s", r.name, a.name)
			if e.state().IsOverhang() {
				// the LUVIOverhangGuard (on by default) rejects the add and
				// leaves the pool as it was; turn it off to check the math
				_, err := e.tryAddLiquidity(a.rune, a.ast)
				c.Check(errors.Is(err, errAddLiquidityPoolOverhang), Equals, true, Commentf("%s: %v", name, err))
				c.Check(e.state(), DeepEquals, e.baseline, Commentf(name))
				e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
			}
			before := e.pool().LPUnits
			var units cosmos.Uint
			step := e.step(func() { units = e.addLiquidity(a.rune, a.ast) })
			e.assertNotDown(step, name)
			if a.name == "symmetric" {
				e.assertFlat(step, name)
			}
			c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, []cosmos.Uint{units}, nil), IsNil, Commentf(name))
		}
	}
}

func (s *LuviInvariantSuite) TestWithdrawNeverLowersGAndMatchesLedger(c *C) {
	for _, r := range luviTestRegimes {
		for _, to := range []common.Asset{common.EmptyAsset, common.RuneAsset(), common.ETHAsset} {
			for _, bps := range []uint64{1, 100, 5_000} {
				e := newLuviEnv(c, r)
				before := e.pool().LPUnits
				var burned cosmos.Uint
				step := e.step(func() { burned = e.withdraw(bps, to) })
				name := fmt.Sprintf("%s withdraw %d bps to %q", r.name, bps, to)
				e.assertNotDown(step, name)
				if to.IsEmpty() {
					e.assertFlat(step, name)
				}
				c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, nil, []cosmos.Uint{burned}), IsNil, Commentf(name))
			}
		}
	}
}

// ----------------------------------------------------------------------------
// income
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestRewardsAndDonationsRaiseG(c *C) {
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		e.assertUp(e.step(func() { e.payReward(luviPoolRune / 10_000) }), r.name+" block reward")
		e.assertUp(e.step(func() { e.donate(luviPoolRune/10_000, luviPoolAsset/10_000) }), r.name+" donation")
	}
}

// ----------------------------------------------------------------------------
// routine protocol costs: small, bounded, expected drops
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestGasReimbursementIsSecondOrder(c *C) {
	// the Reserve pays spot RUNE for spent gas: sqrt(A*R) changes by
	// sqrt(1 - (g/A)^2) - 1, about -(g/A)^2/2, whatever the synths
	for _, r := range luviTestRegimes {
		for _, frac := range []uint64{1_000_000, 10_000, 1_000} {
			e := newLuviEnv(c, r)
			g := luviPoolAsset / frac
			step := e.step(func() { e.reimburseGas(g) })
			x := float64(g) / float64(luviPoolAsset)
			expect := -x * x / 2
			e.assertBetween(step, decf(expect*1.1-1e-12), decf(0), fmt.Sprintf("%s gas 1/%d", r.name, frac))
		}
	}
}

func (s *LuviInvariantSuite) TestSlashCoveredByBondIsSecondOrder(c *C) {
	// the pool loses the stolen asset and receives its spot RUNE value from the
	// node's bond: sqrt(A*R) falls by about (g/A)^2/2
	for _, r := range luviTestRegimes {
		for _, frac := range []uint64{10_000, 100, 20} {
			e := newLuviEnv(c, r)
			g := luviPoolAsset / frac
			bond := cosmos.NewUint(luviPoolRune) // far more than any stolen value here
			step := e.step(func() { e.slashVault(g, bond) })
			x := float64(g) / float64(luviPoolAsset)
			expect := -x * x / 2
			e.assertBetween(step, decf(expect*1.1-1e-12), decf(expect*0.9+1e-12), fmt.Sprintf("%s slash 1/%d", r.name, frac))
		}
	}
}

func (s *LuviInvariantSuite) TestSlashWithBondShortfallIsFirstOrder(c *C) {
	// when the bond can't cover the stolen value the pool is only partly repaid:
	// a first-order loss, far larger than the covered case
	for _, r := range luviTestRegimes {
		e := newLuviEnv(c, r)
		g := luviPoolAsset / 100 // 1% of the asset, worth 10,000 RUNE
		step := e.step(func() { e.slashVault(g, cosmos.NewUint(100*common.One)) })
		x := float64(g) / float64(luviPoolAsset)
		e.assertBetween(step, decf(-x), decf(-10*x*x), r.name+" slash with bond shortfall")
	}
}

// ----------------------------------------------------------------------------
// losses
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestLossesLowerG(c *C) {
	for _, r := range luviTestRegimes {
		for _, frac := range []uint64{10_000_000, 10_000, 100} {
			name := fmt.Sprintf("%s 1/%d", r.name, frac)

			e := newLuviEnv(c, r)
			e.assertDown(e.step(func() {
				e.setPool(func(p *Pool) { p.BalanceAsset = p.BalanceAsset.QuoUint64(frac).MulUint64(frac - 1) })
			}), name+" asset stolen")

			e = newLuviEnv(c, r)
			e.assertDown(e.step(func() { e.setPool(func(p *Pool) { p.BalanceRune = p.BalanceRune.QuoUint64(frac).MulUint64(frac - 1) }) }), name+" RUNE stolen")

			e = newLuviEnv(c, r)
			e.assertDown(e.step(func() { e.mintSynths(cosmos.NewUint(luviPoolAsset / frac)) }), name+" synths minted for free")

			e = newLuviEnv(c, r)
			before := e.pool().LPUnits
			step := e.step(func() { e.setPool(func(p *Pool) { p.LPUnits = p.LPUnits.Add(p.LPUnits.QuoUint64(frac)) }) })
			if e.state().IsOverhang() {
				// blind spot: the clamp makes over-minted units nearly worthless
				// until the pool leaves overhang -- the ledger catches it instead
				e.assertFlat(step, name+" over-mint in overhang")
			} else {
				e.assertDown(step, name+" LP units over-minted")
			}
			c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, nil, nil), NotNil, Commentf(name))
		}
	}
}

func (s *LuviInvariantSuite) TestOverhangOverMintIsCaughtByLedgerNotG(c *C) {
	// An attacker over-mints LP units while the pool is in overhang, waits for
	// synth holders to burn the pool back out, then withdraws. G never drops;
	// the unit ledger flags the mint.
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	c.Assert(e.state().IsOverhang(), Equals, true)

	attacker := GetRandomRUNEAddress()
	before := e.pool().LPUnits
	extra := before.MulUint64(10)
	t, err := types2.NewLuviTracker(e.state())
	c.Assert(err, IsNil)

	e.setPool(func(p *Pool) { p.LPUnits = p.LPUnits.Add(extra) })
	e.mgr.Keeper().SetLiquidityProvider(e.ctx, LiquidityProvider{
		Asset: e.asset, RuneAddress: attacker, AssetAddress: GetRandomETHAddress(), Units: extra,
	})
	step, err := t.Observe(e.state())
	c.Assert(err, IsNil)
	e.assertFlat(step, "over-mint in overhang")
	c.Check(types2.CheckLPUnitLedger(before, e.pool().LPUnits, nil, nil), NotNil)

	// synth holders redeem until the pool is out of overhang
	for i := 0; e.state().IsOverhang() && i < 100; i++ {
		e.burnSynth(e.synthSupply().QuoUint64(10))
		step, err = t.Observe(e.state())
		c.Assert(err, IsNil)
		e.assertNotDown(step, fmt.Sprintf("burn %d", i))
	}
	c.Assert(e.state().IsOverhang(), Equals, false)

	// the attacker now withdraws real value, and G still sees no drop
	runeBefore := e.pool().BalanceRune
	e.lpAddr = attacker
	e.withdraw(10_000, common.EmptyAsset)
	step, err = t.Observe(e.state())
	c.Assert(err, IsNil)
	e.assertFlat(step, "attacker withdrawal")
	taken := runeBefore.Sub(e.pool().BalanceRune)
	c.Check(taken.GT(runeBefore.QuoUint64(1_000)), Equals, true, Commentf("attacker took %s of %s RUNE", taken, runeBefore))
	c.Check(t.G().IsNegative(), Equals, false, Commentf("G=%s", t.G()))
}

func (s *LuviInvariantSuite) TestOverhangDepositMultipliesUnits(c *C) {
	// In overhang the node mints units in proportion to the clamped pool units
	// P = L*(1+S), so a symmetric deposit of fraction f multiplies L by
	// 1+(1+S)*f. G reads the deposit as flat -- nothing leaves the pool -- even
	// though the depositor's units can redeem only ~1/(1+S) of the pool.
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	st := e.state()
	c.Assert(st.IsOverhang(), Equals, true)
	// this measures the unguarded math, so turn the LUVIOverhangGuard off
	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
	f := uint64(10_000) // deposit 1/10,000 of each side
	var units cosmos.Uint
	step := e.step(func() { units = e.addLiquidity(luviPoolRune/f, luviPoolAsset/f) })
	e.assertFlat(step, "deposit into overhang")
	// expected units: P * f (P = L + L*S)
	expect := st.PoolUnits.QuoUint64(f)
	diff := new(big.Int).Sub(units.BigInt(), expect.BigInt())
	c.Check(diff.CmpAbs(new(big.Int).Quo(expect.BigInt(), big.NewInt(1_000_000))) <= 0, Equals, true,
		Commentf("units %s, expected about %s", units, expect))
	growth := e.pool().LPUnits.Quo(st.LPUnits)
	c.Check(growth.GT(st.SynthSupply.QuoUint64(f)), Equals, true, Commentf("LP units grew %sx", growth))
}

// ----------------------------------------------------------------------------
// long random sessions across regimes and boundary crossings
// ----------------------------------------------------------------------------

func (s *LuviInvariantSuite) TestRandomSessionsNeverDropWithoutACause(c *C) {
	// a deterministic mix of legitimate operations; synths start near 2A so
	// price moves carry the pool in and out of overhang
	rng := newDetRand(42)
	for _, r := range []luviRegime{{"near boundary", 199}, {"light synths", 40}, {"overhang", 210}} {
		e := newLuviEnv(c, r)
		t, err := types2.NewLuviTracker(e.state())
		c.Assert(err, IsNil)
		crossings := 0
		for i := 0; i < 300; i++ {
			prevOverhang := e.state().IsOverhang()
			var what string
			switch rng.next() % 7 {
			case 0:
				// price swings of ~1-3% carry a pool near S = 2A across the boundary
				what = "swap RUNE->asset"
				e.swapRuneToAsset(luviPoolRune / (30 + rng.next()%70))
			case 1:
				what = "swap asset->RUNE"
				e.swapAssetToRune(luviPoolAsset / (30 + rng.next()%70))
			case 2:
				what = "synth mint"
				e.mintSynth(luviPoolRune / (500 + rng.next()%5_000))
			case 3:
				what = "synth burn"
				if !e.synthSupply().IsZero() {
					e.burnSynth(e.synthSupply().QuoUint64(500 + rng.next()%5_000))
				}
			case 4:
				what = "add"
				if e.state().IsOverhang() {
					// deposits into an overhang pool multiply LP units by ~1+S*f,
					// which this session isn't about; see
					// TestOverhangDepositMultipliesUnits
					what = "add skipped (overhang)"
					break
				}
				e.addLiquidity(luviPoolRune/(1_000+rng.next()%10_000), luviPoolAsset/(1_000+rng.next()%10_000))
			case 5:
				what = "withdraw"
				e.withdraw(1+rng.next()%200, []common.Asset{common.EmptyAsset, common.RuneAsset(), common.ETHAsset}[rng.next()%3])
			case 6:
				what = "reward"
				e.payReward(luviPoolRune / (10_000 + rng.next()%100_000))
			}
			step, err := t.Observe(e.state())
			c.Assert(err, IsNil)
			e.assertNotDown(step, fmt.Sprintf("%s step %d %s", r.name, i, what))
			if prevOverhang != e.state().IsOverhang() {
				crossings++
			}
		}
		c.Logf("%s: %d overhang crossings, G=%s", r.name, crossings, t.G())
		if r.name == "near boundary" {
			c.Check(crossings > 0, Equals, true, Commentf("session never crossed the overhang boundary"))
		}
	}
}

// detRand is a tiny deterministic generator so the session is reproducible.
type detRand struct{ s *big.Int }

func newDetRand(seed int64) *detRand { return &detRand{s: big.NewInt(seed)} }

func (d *detRand) next() uint64 {
	// 64-bit LCG (Knuth MMIX)
	d.s.Mul(d.s, big.NewInt(6364136223846793005))
	d.s.Add(d.s, big.NewInt(1442695040888963407))
	d.s.And(d.s, new(big.Int).SetUint64(^uint64(0)))
	return new(big.Int).Rsh(d.s, 33).Uint64()
}
