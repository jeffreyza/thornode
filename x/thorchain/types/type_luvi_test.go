package types

import (
	"math/big"
	"math/rand"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type LuviSuite struct{}

var _ = Suite(&LuviSuite{})

// ----------------------------------------------------------------------------
// independent reference implementation (256-bit floats)
// ----------------------------------------------------------------------------

const refPrec = 256

func rf(u cosmos.Uint) *big.Float { return new(big.Float).SetPrec(refPrec).SetInt(u.BigInt()) }

func rfDec(d cosmos.Dec) *big.Float {
	f, _, err := big.ParseFloat(d.String(), 10, refPrec, big.ToNearestEven)
	if err != nil {
		panic(err)
	}
	return f
}

func rsqrt(x *big.Float) *big.Float { return new(big.Float).SetPrec(refPrec).Sqrt(x) }

func rmul(a, b *big.Float) *big.Float { return new(big.Float).SetPrec(refPrec).Mul(a, b) }

func rquo(a, b *big.Float) *big.Float { return new(big.Float).SetPrec(refPrec).Quo(a, b) }

func radd(a, b *big.Float) *big.Float { return new(big.Float).SetPrec(refPrec).Add(a, b) }

func rsub(a, b *big.Float) *big.Float { return new(big.Float).SetPrec(refPrec).Sub(a, b) }

// refQ is sqrt(A*R) * (2A - S) / (2A) with no on-chain rounding.
func refQ(s LuviState) *big.Float {
	twoA := rmul(rf(s.BalanceAsset), big.NewFloat(2))
	return rquo(rmul(rsqrt(rmul(rf(s.BalanceAsset), rf(s.BalanceRune))), rsub(twoA, rf(s.SynthSupply))), twoA)
}

func refSqrtP(s LuviState) *big.Float { return rsqrt(rquo(rf(s.BalanceRune), rf(s.BalanceAsset))) }

func refSqrtAR(s LuviState) *big.Float { return rsqrt(rmul(rf(s.BalanceAsset), rf(s.BalanceRune))) }

// refDeltaG is the reference step of G.
func refDeltaG(prev, next LuviState) *big.Float {
	dQ := rsub(refQ(next), refQ(prev))
	rep := rquo(rmul(radd(rf(prev.SynthSupply), rf(next.SynthSupply)), rsub(refSqrtP(next), refSqrtP(prev))), big.NewFloat(4))
	dL := rsub(rf(next.LPUnits), rf(prev.LPUnits))
	chain := rquo(refSqrtAR(prev), rf(prev.PoolUnits))
	return rsub(radd(dQ, rep), rmul(dL, chain))
}

func absF(x *big.Float) *big.Float { return new(big.Float).SetPrec(refPrec).Abs(x) }

// closeTo checks |got - want| <= tol.
func closeTo(c *C, got cosmos.Dec, want *big.Float, tol *big.Float, comment CommentInterface) {
	diff := absF(rsub(rfDec(got), want))
	c.Check(diff.Cmp(tol) <= 0, Equals, true, comment)
}

// ----------------------------------------------------------------------------
// state builders
// ----------------------------------------------------------------------------

func u(v uint64) cosmos.Uint { return cosmos.NewUint(v) }

func toUint(x *big.Int) cosmos.Uint { return cosmos.NewUintFromBigInt(x) }

// luviState builds a state whose PoolUnits come from the real CalcUnits.
func luviState(runeDepth, assetDepth, lpUnits, synthSupply cosmos.Uint) LuviState {
	p := NewPool()
	p.Asset = common.BTCAsset
	p.BalanceRune = runeDepth
	p.BalanceAsset = assetDepth
	p.LPUnits = lpUnits
	return NewLuviState(p, synthSupply)
}

// regime describes a range of S/A ratios, in percent.
type regime struct {
	name   string
	lo, hi int64
}

var luviRegimes = []regime{
	{"no synths", 0, 0},
	{"light synths", 1, 50},
	{"heavy synths", 150, 199},
	{"overhang", 201, 350},
	{"deep overhang", 400, 1000},
}

func randDepth(rng *rand.Rand) *big.Int {
	// 1e6 .. 1e18 base units
	exp := 6 + rng.Intn(13)
	x := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
	return x.Mul(x, big.NewInt(1+rng.Int63n(9)))
}

func randState(rng *rand.Rand, r regime) LuviState {
	a := randDepth(rng)
	rr := randDepth(rng)
	l := new(big.Int).Mul(rr, big.NewInt(1+rng.Int63n(20)))
	l.Quo(l, big.NewInt(10))
	if l.Sign() == 0 {
		l.SetInt64(1)
	}
	pct := r.lo
	if r.hi > r.lo {
		pct += rng.Int63n(r.hi - r.lo + 1)
	}
	s := new(big.Int).Mul(a, big.NewInt(pct))
	s.Quo(s, big.NewInt(100))
	return luviState(toUint(rr), toUint(a), toUint(l), toUint(s))
}

func mustTracker(c *C, s LuviState) *LuviTracker {
	t, err := NewLuviTracker(s)
	c.Assert(err, IsNil)
	return t
}

func mustObserve(c *C, t *LuviTracker, s LuviState) LuviStep {
	step, err := t.Observe(s)
	c.Assert(err, IsNil)
	return step
}

// dustTol is the tolerance for identities that on-chain rounding of synth
// units (CalcUnits floors SU) can perturb: |x| * 1/PoolUnits plus fixed-point
// truncation.
func dustTol(scale *big.Float, s LuviState) *big.Float {
	t := rquo(scale, rf(s.PoolUnits))
	return radd(absF(t), big.NewFloat(1e-15))
}

// ----------------------------------------------------------------------------
// basics
// ----------------------------------------------------------------------------

func (LuviSuite) TestSqrtARExact(c *C) {
	// perfect squares must come out exact
	s := luviState(u(4_000_000), u(9_000_000), u(1_000), u(0))
	c.Check(s.SqrtAR().Equal(cosmos.NewDec(6_000_000)), Equals, true, Commentf("%s", s.SqrtAR()))
	s = luviState(u(1), u(1), u(1), u(0))
	c.Check(s.SqrtAR().Equal(cosmos.NewDec(1)), Equals, true)
}

func (LuviSuite) TestOverhangBoundary(c *C) {
	a := u(1_000_000)
	c.Check(luviState(u(5_000_000), a, u(1_000), u(1_999_999)).IsOverhang(), Equals, false)
	c.Check(luviState(u(5_000_000), a, u(1_000), u(2_000_000)).IsOverhang(), Equals, true)
	c.Check(luviState(u(5_000_000), a, u(1_000), u(2_000_001)).IsOverhang(), Equals, true)
	c.Check(luviState(u(5_000_000), a, u(1_000), u(0)).IsOverhang(), Equals, false)

	c.Check(luviState(u(5), a, u(1), u(0)).SynthPerPoolDepthBps().Uint64(), Equals, uint64(0))
	c.Check(luviState(u(5), a, u(1), u(1_000_000)).SynthPerPoolDepthBps().Uint64(), Equals, uint64(5_000))
	c.Check(luviState(u(5), a, u(1), u(2_000_000)).SynthPerPoolDepthBps().Uint64(), Equals, uint64(10_000))
	c.Check(luviState(u(5), a, u(1), u(3_000_000)).SynthPerPoolDepthBps().Uint64(), Equals, uint64(15_000))
}

func (LuviSuite) TestZeroGuards(c *C) {
	zero := LuviState{
		BalanceRune: cosmos.ZeroUint(), BalanceAsset: cosmos.ZeroUint(), LPUnits: cosmos.ZeroUint(),
		SynthSupply: cosmos.ZeroUint(), PoolUnits: cosmos.ZeroUint(),
	}
	c.Check(zero.IsValid(), Equals, false)
	c.Check(zero.SqrtAR().IsZero(), Equals, true)
	c.Check(zero.LPEquity().IsZero(), Equals, true)
	c.Check(zero.LPEquityPerUnit().IsZero(), Equals, true)
	c.Check(zero.ChainLuvi().IsZero(), Equals, true)
	c.Check(zero.SynthPerPoolDepthBps().IsZero(), Equals, true)
	c.Check(zero.IsOverhang(), Equals, true) // 0 >= 0; harmless, the state is invalid anyway

	_, err := NewLuviTracker(zero)
	c.Check(err, NotNil)

	valid := luviState(u(100), u(100), u(100), u(0))
	t := mustTracker(c, valid)
	for _, bad := range []LuviState{
		luviState(u(0), u(100), u(100), u(0)),
		luviState(u(100), u(0), u(100), u(0)),
		luviState(u(100), u(100), u(0), u(0)),
	} {
		_, err = t.Observe(bad)
		c.Check(err, NotNil)
	}
	// a rejected observation must not change the tracker
	c.Check(t.G().IsZero(), Equals, true)
	c.Check(t.C().IsZero(), Equals, true)
}

func (LuviSuite) TestNewLuviStateDoesNotMutatePool(c *C) {
	p := NewPool()
	p.Asset = common.BTCAsset
	p.BalanceRune = u(1_000_000)
	p.BalanceAsset = u(1_000)
	p.LPUnits = u(1_000_000)
	p.SynthUnits = u(7)
	s := NewLuviState(p, u(500))
	c.Check(p.SynthUnits.Uint64(), Equals, uint64(7))
	// SU = L*S/(2A-S) = 1e6*500/1500 = 333333
	c.Check(s.PoolUnits.Uint64(), Equals, uint64(1_333_333))
}

// ----------------------------------------------------------------------------
// state identities, every regime
// ----------------------------------------------------------------------------

func (LuviSuite) TestStateValuesMatchReference(c *C) {
	rng := rand.New(rand.NewSource(1)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 400; i++ {
			s := randState(rng, r)
			cm := Commentf("%s #%d %+v", r.name, i, s)
			tol := big.NewFloat(1e-17)
			closeTo(c, s.SqrtAR(), refSqrtAR(s), tol, cm)
			closeTo(c, s.LPEquity(), refQ(s), tol, cm)
			closeTo(c, s.LPEquityPerUnit(), rquo(refQ(s), rf(s.LPUnits)), tol, cm)
			closeTo(c, s.ChainLuvi(), rquo(refSqrtAR(s), rf(s.PoolUnits)), tol, cm)
			// sign of LP equity follows the regime
			if s.IsOverhang() && s.SynthSupply.GT(s.BalanceAsset.MulUint64(2)) {
				c.Check(s.LPEquity().IsNegative(), Equals, true, cm)
			} else {
				c.Check(s.LPEquity().IsNegative(), Equals, false, cm)
			}
		}
	}
}

func (LuviSuite) TestNoSynthsIsClassicLuvi(c *C) {
	rng := rand.New(rand.NewSource(2)) // #nosec G404 deterministic test data
	for i := 0; i < 500; i++ {
		s := randState(rng, luviRegimes[0])
		classic := rquo(refSqrtAR(s), rf(s.LPUnits))
		cm := Commentf("#%d %+v", i, s)
		closeTo(c, s.LPEquityPerUnit(), classic, big.NewFloat(1e-17), cm)
		closeTo(c, s.ChainLuvi(), classic, big.NewFloat(1e-17), cm)
	}
}

func (LuviSuite) TestBelowOverhangEquityEqualsChainValue(c *C) {
	// Q/L == sqrt(AR)/P exactly when SU is exact; CalcUnits floors SU, which
	// can move P by < 1 unit, i.e. the value by < sqrt(AR)/P^2.
	rng := rand.New(rand.NewSource(3)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes[:3] {
		for i := 0; i < 400; i++ {
			s := randState(rng, r)
			v := rquo(refSqrtAR(s), rf(s.PoolUnits))
			closeTo(c, s.LPEquityPerUnit(), v, dustTol(v, s), Commentf("%s #%d %+v", r.name, i, s))
		}
	}
}

func (LuviSuite) TestOverhangEquityIsNegativeAndChainValueIsClamped(c *C) {
	rng := rand.New(rand.NewSource(4)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes[3:] {
		for i := 0; i < 300; i++ {
			s := randState(rng, r)
			cm := Commentf("%s #%d %+v", r.name, i, s)
			c.Check(s.IsOverhang(), Equals, true, cm)
			c.Check(s.LPEquity().IsNegative(), Equals, true, cm)
			// clamped: PoolUnits = L + L*S, so all LP units together are worth
			// sqrt(AR)/(1+S) -- essentially nothing
			c.Check(s.PoolUnits.Equal(s.LPUnits.Add(s.LPUnits.Mul(s.SynthSupply))), Equals, true, cm)
			allUnits := rmul(rfDec(s.ChainLuvi()), rf(s.LPUnits))
			bound := radd(rquo(refSqrtAR(s), radd(rf(s.SynthSupply), big.NewFloat(1))), big.NewFloat(1e-15))
			c.Check(allUnits.Cmp(bound) <= 0, Equals, true, cm)
		}
	}
}

// ----------------------------------------------------------------------------
// step behaviour, every regime
// ----------------------------------------------------------------------------

// hyperbolaPair returns two states with exactly the same A*R (a pure price
// move, as a fee-to-Reserve swap produces), same S and L.
func hyperbolaPair(rng *rand.Rand, r regime) (LuviState, LuviState) {
	x, y := randDepth(rng), randDepth(rng)
	z, w := randDepth(rng), randDepth(rng)
	a0, r0 := new(big.Int).Mul(x, y), new(big.Int).Mul(z, w)
	a1, r1 := new(big.Int).Mul(x, w), new(big.Int).Mul(z, y)
	l := new(big.Int).Quo(r0, big.NewInt(3))
	l.Add(l, big.NewInt(1))
	pct := r.lo
	if r.hi > r.lo {
		pct += rng.Int63n(r.hi - r.lo + 1)
	}
	s := new(big.Int).Mul(a0, big.NewInt(pct))
	s.Quo(s, big.NewInt(100))
	return luviState(toUint(r0), toUint(a0), toUint(l), toUint(s)), luviState(toUint(r1), toUint(a1), toUint(l), toUint(s))
}

func (LuviSuite) TestPriceMovesAreFlat(c *C) {
	rng := rand.New(rand.NewSource(5)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 300; i++ {
			s0, s1 := hyperbolaPair(rng, r)
			t := mustTracker(c, s0)
			star0 := t.LuviStar()
			step := mustObserve(c, t, s1)
			cm := Commentf("%s #%d %+v -> %+v", r.name, i, s0, s1)
			// G: exactly flat up to fixed-point truncation
			closeTo(c, step.NormalizedDeltaG, big.NewFloat(0), big.NewFloat(1e-16), cm)
			// LUVI*: flat (meaningful below overhang, but the algebra holds everywhere)
			closeTo(c, t.LuviStar(), rfDec(star0), big.NewFloat(1e-15), cm)
			// C absorbs exactly the synth repricing: dC == -dQ/L
			dQ := rsub(refQ(s1), refQ(s0))
			closeTo(c, step.DeltaC, rquo(new(big.Float).Neg(dQ), rf(s1.LPUnits)), big.NewFloat(1e-15), cm)
		}
	}
}

// symDeposit returns the state after a fair symmetric deposit that grows the
// pool by num/den - 1, minting units = P * (num-den)/den as calculatePoolUnits
// does for a proportional add.
func symDeposit(s LuviState, num, den int64) LuviState {
	scale := func(x cosmos.Uint) cosmos.Uint {
		y := new(big.Int).Mul(x.BigInt(), big.NewInt(num))
		return toUint(y.Quo(y, big.NewInt(den)))
	}
	du := new(big.Int).Mul(s.PoolUnits.BigInt(), big.NewInt(num-den))
	du.Quo(du, big.NewInt(den))
	return luviState(scale(s.BalanceRune), scale(s.BalanceAsset), toUint(du.Add(du, s.LPUnits.BigInt())), s.SynthSupply)
}

// symWithdraw burns num/den of the LP units and pays out claim/P of each side,
// as calculateWithdraw does for a symmetric withdrawal.
func symWithdraw(s LuviState, num, den int64) LuviState {
	claim := new(big.Int).Mul(s.LPUnits.BigInt(), big.NewInt(num))
	claim.Quo(claim, big.NewInt(den))
	share := func(x cosmos.Uint) cosmos.Uint {
		y := new(big.Int).Mul(x.BigInt(), claim)
		return x.Sub(toUint(y.Quo(y, s.PoolUnits.BigInt())))
	}
	return luviState(share(s.BalanceRune), share(s.BalanceAsset), toUint(new(big.Int).Sub(s.LPUnits.BigInt(), claim)), s.SynthSupply)
}

func (LuviSuite) TestProportionalFlowsAreFlat(c *C) {
	rng := rand.New(rand.NewSource(6)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 300; i++ {
			s0 := randState(rng, r)
			if s0.LPUnits.LT(u(1_000)) {
				continue
			}
			var nexts []LuviState
			for _, f := range [][2]int64{{11, 10}, {3, 2}, {101, 100}} {
				nexts = append(nexts, symDeposit(s0, f[0], f[1]))
			}
			for _, f := range [][2]int64{{1, 10}, {1, 2}, {99, 100}} {
				nexts = append(nexts, symWithdraw(s0, f[0], f[1]))
			}
			for k, s1 := range nexts {
				if !s1.IsValid() {
					continue
				}
				t := mustTracker(c, s0)
				step := mustObserve(c, t, s1)
				cm := Commentf("%s #%d flow %d %+v -> %+v", r.name, i, k, s0, s1)
				// integer truncation of depths, units and SU only
				tol := radd(rquo(big.NewFloat(4), rf(s1.LPUnits)), rquo(big.NewFloat(4), rf(s1.BalanceAsset)))
				tol = radd(tol, rquo(big.NewFloat(4), rf(s1.BalanceRune)))
				closeTo(c, step.NormalizedDeltaG, big.NewFloat(0), tol, cm)
			}
		}
	}
}

func (LuviSuite) TestIncomeRaisesG(c *C) {
	rng := rand.New(rand.NewSource(7)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 300; i++ {
			s0 := randState(rng, r)
			bump := new(big.Int).Quo(s0.BalanceRune.BigInt(), big.NewInt(1+rng.Int63n(10_000)))
			bump.Add(bump, big.NewInt(1))
			cm := Commentf("%s #%d %+v", r.name, i, s0)

			// block reward: RUNE only
			reward := luviState(s0.BalanceRune.Add(toUint(bump)), s0.BalanceAsset, s0.LPUnits, s0.SynthSupply)
			t := mustTracker(c, s0)
			step := mustObserve(c, t, reward)
			c.Check(step.DeltaG.IsPositive(), Equals, true, cm)
			closeTo(c, step.DeltaG, refDeltaG(s0, reward), big.NewFloat(1e-15), cm)

			// donation: both sides
			abump := new(big.Int).Quo(s0.BalanceAsset.BigInt(), big.NewInt(1+rng.Int63n(10_000)))
			abump.Add(abump, big.NewInt(1))
			don := luviState(s0.BalanceRune.Add(toUint(bump)), s0.BalanceAsset.Add(toUint(abump)), s0.LPUnits, s0.SynthSupply)
			t = mustTracker(c, s0)
			step = mustObserve(c, t, don)
			c.Check(step.DeltaG.IsPositive(), Equals, true, cm)
		}
	}
}

func (LuviSuite) TestLossesLowerG(c *C) {
	rng := rand.New(rand.NewSource(8)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 300; i++ {
			s0 := randState(rng, r)
			cm := Commentf("%s #%d %+v", r.name, i, s0)
			frac := int64(1_000 + rng.Intn(1_000_000)) // lose 1/frac
			partOf := func(x cosmos.Uint, f int64) cosmos.Uint {
				y := new(big.Int).Quo(x.BigInt(), big.NewInt(f))
				if y.Sign() == 0 {
					y.SetInt64(1)
				}
				return toUint(y)
			}
			part := func(x cosmos.Uint) cosmos.Uint { return partOf(x, frac) }
			// an ADR-028 style write-down: dA of asset and dR = dA*R/A of RUNE,
			// derived from dA so the price holds
			dA := part(s0.BalanceAsset)
			dR := toUint(new(big.Int).Quo(new(big.Int).Mul(dA.BigInt(), s0.BalanceRune.BigInt()), s0.BalanceAsset.BigInt()))
			cases := map[string]LuviState{
				"steal asset":  luviState(s0.BalanceRune, s0.BalanceAsset.Sub(part(s0.BalanceAsset)), s0.LPUnits, s0.SynthSupply),
				"steal rune":   luviState(s0.BalanceRune.Sub(part(s0.BalanceRune)), s0.BalanceAsset, s0.LPUnits, s0.SynthSupply),
				"free synths":  luviState(s0.BalanceRune, s0.BalanceAsset, s0.LPUnits, s0.SynthSupply.Add(part(s0.BalanceAsset))),
				"steal both":   luviState(s0.BalanceRune.Sub(partOf(s0.BalanceRune, 2*frac)), s0.BalanceAsset.Sub(part(s0.BalanceAsset)), s0.LPUnits, s0.SynthSupply),
				"write-down":   luviState(s0.BalanceRune.Sub(dR), s0.BalanceAsset.Sub(dA), s0.LPUnits, s0.SynthSupply),
				"over-minting": luviState(s0.BalanceRune, s0.BalanceAsset, s0.LPUnits.Add(part(s0.LPUnits)), s0.SynthSupply),
			}
			for name, s1 := range cases {
				if !s1.IsValid() {
					continue
				}
				t := mustTracker(c, s0)
				step := mustObserve(c, t, s1)
				cmc := Commentf("%s: %s", name, cm.CheckCommentString())
				closeTo(c, step.DeltaG, refDeltaG(s0, s1), radd(absF(rmul(refDeltaG(s0, s1), big.NewFloat(1e-12))), big.NewFloat(1e-15)), cmc)
				if name == "over-minting" && s0.IsOverhang() {
					// blind spot: over-minted units are worth at most dL/P of the pool
					bound := radd(rquo(rsub(rf(s1.LPUnits), rf(s0.LPUnits)), rf(s0.PoolUnits)), big.NewFloat(1e-15))
					c.Check(absF(rfDec(step.NormalizedDeltaG)).Cmp(bound) <= 0, Equals, true, cmc)
					// ...which the unit ledger catches
					c.Check(CheckLPUnitLedger(s0.LPUnits, s1.LPUnits, nil, nil), NotNil, cmc)
					continue
				}
				c.Check(step.DeltaG.IsNegative(), Equals, true, cmc)
			}
		}
	}
}

// ----------------------------------------------------------------------------
// relations between the indices
// ----------------------------------------------------------------------------

// randomWalk applies a mix of changes to a state and returns the sequence.
func randomWalk(rng *rand.Rand, s LuviState, n int, allowSynths bool) []LuviState {
	out := []LuviState{s}
	cur := s
	for len(out) <= n {
		r, a, l, sy := cur.BalanceRune.BigInt(), cur.BalanceAsset.BigInt(), cur.LPUnits.BigInt(), cur.SynthSupply.BigInt()
		step := func(x *big.Int, maxDiv int64) *big.Int {
			d := new(big.Int).Quo(x, big.NewInt(10+rng.Int63n(maxDiv)))
			return d.Add(d, big.NewInt(1))
		}
		nr, na, nl, ns := new(big.Int).Set(r), new(big.Int).Set(a), new(big.Int).Set(l), new(big.Int).Set(sy)
		switch rng.Intn(6) {
		case 0: // price up
			nr.Add(nr, step(r, 1000))
			na.Sub(na, step(a, 1000))
		case 1: // price down
			nr.Sub(nr, step(r, 1000))
			na.Add(na, step(a, 1000))
		case 2: // LP flow in
			nl.Add(nl, step(l, 1000))
			nr.Add(nr, step(r, 1000))
			na.Add(na, step(a, 1000))
		case 3: // LP flow out
			nl.Sub(nl, step(l, 1000))
			nr.Sub(nr, step(r, 1000))
			na.Sub(na, step(a, 1000))
		case 4: // synths change
			if allowSynths {
				if rng.Intn(2) == 0 {
					ns.Add(ns, step(a, 1000))
				} else if ns.Sign() > 0 {
					ns.Sub(ns, new(big.Int).Quo(ns, big.NewInt(10)))
				}
			}
		case 5: // income
			nr.Add(nr, step(r, 100000))
		}
		next := luviState(toUint(nr), toUint(na), toUint(nl), toUint(ns))
		if !next.IsValid() || nr.Sign() <= 0 || na.Sign() <= 0 || nl.Sign() <= 0 {
			continue
		}
		out = append(out, next)
		cur = next
	}
	return out
}

func (LuviSuite) TestDeltaGMatchesReference(c *C) {
	rng := rand.New(rand.NewSource(9)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 40; i++ {
			seq := randomWalk(rng, randState(rng, r), 25, r.hi > 0)
			t := mustTracker(c, seq[0])
			for j := 1; j < len(seq); j++ {
				step := mustObserve(c, t, seq[j])
				want := refDeltaG(seq[j-1], seq[j])
				tol := radd(absF(rmul(want, big.NewFloat(1e-12))), big.NewFloat(1e-15))
				closeTo(c, step.DeltaG, want, tol, Commentf("%s walk %d step %d", r.name, i, j))
			}
		}
	}
}

func (LuviSuite) TestBelowOverhangDeltaGEqualsLTimesDeltaLuviStar(c *C) {
	rng := rand.New(rand.NewSource(10)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes[:3] {
		for i := 0; i < 40; i++ {
			seq := randomWalk(rng, randState(rng, r), 25, r.hi > 0)
			t := mustTracker(c, seq[0])
			prevStar := rfDec(t.LuviStar())
			for j := 1; j < len(seq); j++ {
				if seq[j].IsOverhang() {
					break
				}
				step := mustObserve(c, t, seq[j])
				star := rfDec(t.LuviStar())
				want := rmul(rf(seq[j].LPUnits), rsub(star, prevStar))
				// CalcUnits flooring makes LUVI_chain differ from Q/L by < sqrt(AR)/P^2
				tol := rmul(absF(rsub(rf(seq[j].LPUnits), rf(seq[j-1].LPUnits))), dustTol(rquo(refSqrtAR(seq[j-1]), rf(seq[j-1].PoolUnits)), seq[j-1]))
				tol = radd(tol, rmul(rf(seq[j].LPUnits), big.NewFloat(1e-17)))
				closeTo(c, step.DeltaG, want, tol, Commentf("%s walk %d step %d", r.name, i, j))
				prevStar = star
			}
		}
	}
}

func (LuviSuite) TestNoSynthsGEqualsLTimesDeltaClassicLuvi(c *C) {
	rng := rand.New(rand.NewSource(11)) // #nosec G404 deterministic test data
	for i := 0; i < 60; i++ {
		seq := randomWalk(rng, randState(rng, luviRegimes[0]), 25, false)
		t := mustTracker(c, seq[0])
		for j := 1; j < len(seq); j++ {
			step := mustObserve(c, t, seq[j])
			classicPrev := rquo(refSqrtAR(seq[j-1]), rf(seq[j-1].LPUnits))
			classic := rquo(refSqrtAR(seq[j]), rf(seq[j].LPUnits))
			want := rmul(rf(seq[j].LPUnits), rsub(classic, classicPrev))
			closeTo(c, step.DeltaG, want, radd(absF(rmul(want, big.NewFloat(1e-12))), big.NewFloat(1e-14)), Commentf("walk %d step %d", i, j))
			c.Check(step.DeltaC.IsZero(), Equals, true)
			// LUVI* is classic LUVI when there have never been synths
			closeTo(c, step.LuviStar, classic, big.NewFloat(1e-17), Commentf("walk %d step %d", i, j))
		}
	}
}

// ----------------------------------------------------------------------------
// synth round trips and boundary crossings
// ----------------------------------------------------------------------------

// cpMint returns the state after minting synths with x RUNE at constant
// product on real depth (virtual depth 1x, fee included in the emission).
func cpMint(s LuviState, x *big.Int) LuviState {
	r, a := s.BalanceRune.BigInt(), s.BalanceAsset.BigInt()
	emit := new(big.Int).Mul(x, a)
	emit.Quo(emit, new(big.Int).Add(r, x))
	return luviState(toUint(new(big.Int).Add(r, x)), s.BalanceAsset, s.LPUnits, s.SynthSupply.Add(toUint(emit)))
}

// cpBurn burns y synths for RUNE at constant product (virtual depth 1x).
func cpBurn(s LuviState, y *big.Int) LuviState {
	r, a := s.BalanceRune.BigInt(), s.BalanceAsset.BigInt()
	out := new(big.Int).Mul(y, r)
	out.Quo(out, new(big.Int).Add(a, y))
	return luviState(toUint(new(big.Int).Sub(r, out)), s.BalanceAsset, s.LPUnits, s.SynthSupply.Sub(toUint(y)))
}

func (LuviSuite) TestSynthRoundTripMatchesTruth(c *C) {
	// mint then burn the same synths: A and S return to where they started,
	// so the LPs' true result is the change in sqrt(A*R). Raw G must agree.
	rng := rand.New(rand.NewSource(12)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		for i := 0; i < 200; i++ {
			s0 := randState(rng, r)
			x := new(big.Int).Quo(s0.BalanceRune.BigInt(), big.NewInt(2+rng.Int63n(10_000)))
			if x.Sign() == 0 {
				continue
			}
			s1 := cpMint(s0, x)
			minted := new(big.Int).Sub(s1.SynthSupply.BigInt(), s0.SynthSupply.BigInt())
			if minted.Sign() == 0 {
				continue
			}
			s2 := cpBurn(s1, minted)
			t := mustTracker(c, s0)
			mustObserve(c, t, s1)
			mustObserve(c, t, s2)
			truth := rsub(refSqrtAR(s2), refSqrtAR(s0))
			g := rfDec(t.G())
			cm := Commentf("%s #%d x=%s %+v g=%s truth=%s", r.name, i, x, s0, g.String(), truth.String())
			if truth.Sign() == 0 {
				continue
			}
			// constant-product trading keeps some slip in the pool: LPs gain
			c.Check(truth.Sign() > 0, Equals, true, cm)
			c.Check(g.Sign(), Equals, truth.Sign(), cm)
			ratio := rquo(g, truth)
			lo, hi := big.NewFloat(0.95), big.NewFloat(1.2)
			c.Check(ratio.Cmp(lo) >= 0 && ratio.Cmp(hi) <= 0, Equals, true, cm)
		}
	}
}

func (LuviSuite) TestCrossingTheOverhangBoundaryByPrice(c *C) {
	// hold A*R, S and L fixed and walk the price so S/2A crosses 1 both ways
	a0 := big.NewInt(1_000_000_000)
	r0 := big.NewInt(4_000_000_000)
	k := new(big.Int).Mul(a0, r0)
	l := u(3_000_000_000)
	syn := u(1_990_000_000) // S/2A = 0.995 at the start
	prev := luviState(toUint(r0), toUint(a0), l, syn)
	t := mustTracker(c, prev)
	crossings := 0
	for _, pct := range []int64{99, 98, 97, 99, 101, 102, 100, 99, 97, 103, 105, 100} {
		a := new(big.Int).Mul(a0, big.NewInt(pct))
		a.Quo(a, big.NewInt(100))
		r := new(big.Int).Quo(k, a)
		next := luviState(toUint(r), toUint(a), l, syn)
		step := mustObserve(c, t, next)
		if prev.IsOverhang() != next.IsOverhang() {
			crossings++
		}
		// integer division of k by a moves A*R by < A per step
		tol := rquo(rf(next.BalanceAsset), rmul(refSqrtAR(prev), refSqrtAR(prev)))
		closeTo(c, step.NormalizedDeltaG, big.NewFloat(0), radd(tol, big.NewFloat(1e-16)), Commentf("pct %d", pct))
		prev = next
	}
	c.Check(crossings >= 2, Equals, true)
}

// ----------------------------------------------------------------------------
// unit ledger
// ----------------------------------------------------------------------------

func (LuviSuite) TestCheckLPUnitLedger(c *C) {
	c.Check(CheckLPUnitLedger(u(100), u(100), nil, nil), IsNil)
	c.Check(CheckLPUnitLedger(u(100), u(150), []cosmos.Uint{u(50)}, nil), IsNil)
	c.Check(CheckLPUnitLedger(u(100), u(70), nil, []cosmos.Uint{u(30)}), IsNil)
	c.Check(CheckLPUnitLedger(u(100), u(95), []cosmos.Uint{u(20), u(5)}, []cosmos.Uint{u(30)}), IsNil)
	c.Check(CheckLPUnitLedger(u(100), u(101), nil, nil), NotNil)
	c.Check(CheckLPUnitLedger(u(100), u(150), []cosmos.Uint{u(49)}, nil), NotNil)
	c.Check(CheckLPUnitLedger(u(100), u(0), nil, []cosmos.Uint{u(100)}), IsNil)
	// more withdrawn than existed can never match a Uint
	c.Check(CheckLPUnitLedger(u(100), u(0), nil, []cosmos.Uint{u(101)}), NotNil)
}

// ----------------------------------------------------------------------------
// extremes and determinism
// ----------------------------------------------------------------------------

func (LuviSuite) TestExtremeValues(c *C) {
	huge := cosmos.NewUintFromString("100000000000000000000000000000000000000") // 1e38
	one := u(1)
	cases := []LuviState{
		luviState(huge, huge, huge, cosmos.ZeroUint()),
		luviState(huge, one, huge, cosmos.ZeroUint()),
		luviState(one, huge, one, huge),
		luviState(one, one, one, one),
		luviState(huge, huge, one, huge.MulUint64(3)),
	}
	for i, s := range cases {
		cm := Commentf("case %d %+v", i, s)
		// any panic here fails the test
		_ = s.SqrtAR()
		_ = s.LPEquity()
		_ = s.LPEquityPerUnit()
		_ = s.ChainLuvi()
		t := mustTracker(c, s)
		mustObserve(c, t, s)
		c.Check(t.G().IsZero(), Equals, true, cm)
	}
}

func (LuviSuite) TestDeterministic(c *C) {
	rng := rand.New(rand.NewSource(13)) // #nosec G404 deterministic test data
	for _, r := range luviRegimes {
		seq := randomWalk(rng, randState(rng, r), 50, r.hi > 0)
		run := func() (string, string, string) {
			t := mustTracker(c, seq[0])
			for _, s := range seq[1:] {
				mustObserve(c, t, s)
			}
			return t.G().String(), t.C().String(), t.LuviStar().String()
		}
		g1, c1, s1 := run()
		g2, c2, s2 := run()
		c.Check(g1, Equals, g2)
		c.Check(c1, Equals, c2)
		c.Check(s1, Equals, s2)
	}
}
