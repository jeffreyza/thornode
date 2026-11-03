package types

import (
	"math/big"
	"math/rand"
	"strings"

	sdkmath "cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type LuviFairSuite struct{}

var _ = Suite(&LuviFairSuite{})

func mustPrice(c *C, s string) LuviPrice {
	p, err := NewLuviPriceFromDec(sdkmath.LegacyMustNewDecFromStr(s))
	c.Assert(err, IsNil)
	return p
}

func mustFair(c *C, s LuviState, p LuviPrice) LuviFairValue {
	fv, err := s.FairValue(p)
	c.Assert(err, IsNil)
	return fv
}

// k-neutral constant-product swap of dx asset into the pool (dx < 0 sells
// RUNE for asset); the fee goes to the Reserve, so A*R is unchanged up to
// truncation.
func kSwap(s LuviState, dx int64) LuviState {
	k := new(big.Int).Mul(s.BalanceAsset.BigInt(), s.BalanceRune.BigInt())
	a := new(big.Int).Add(s.BalanceAsset.BigInt(), big.NewInt(dx))
	r := new(big.Int).Quo(k, a)
	return luviState(toUint(r), toUint(a), s.LPUnits, s.SynthSupply)
}

func (LuviFairSuite) TestParseUSDPrices(c *C) {
	p, err := NewLuviPriceFromUSD("100000", "2")
	c.Assert(err, IsNil)
	c.Check(p.Dec().String(), Equals, "50000.000000000000000000")

	p, err = NewLuviPriceFromUSD("1.", ".5")
	c.Assert(err, IsNil)
	c.Check(p.Dec().String(), Equals, "2.000000000000000000")

	// 1/3 truncates at LuviPrecision, Dec truncates to 18 places
	p, err = NewLuviPriceFromUSD("1", "3")
	c.Assert(err, IsNil)
	c.Check(p.Dec().String(), Equals, "0.333333333333333333")

	for _, bad := range []string{"", ".", "-1", "1e5", "1.2.3", " 1", "0x10", "NaN", strings.Repeat("1", luviMaxPriceLen+1)} {
		_, err = NewLuviPriceFromUSD(bad, "1")
		c.Check(err, NotNil, Commentf("asset %q", bad))
		_, err = NewLuviPriceFromUSD("1", bad)
		c.Check(err, NotNil, Commentf("rune %q", bad))
	}
	_, err = NewLuviPriceFromUSD("0", "1")
	c.Check(err, NotNil)
	_, err = NewLuviPriceFromUSD("1", "0.000")
	c.Check(err, NotNil)

	_, err = NewLuviPriceFromDec(sdkmath.LegacyMustNewDecFromStr("-1"))
	c.Check(err, NotNil)
	_, err = NewLuviPriceFromDec(sdkmath.LegacyDec{})
	c.Check(err, NotNil)
	c.Check(LuviPrice{}.IsZero(), Equals, true)
	c.Check(LuviPrice{}.Dec().IsZero(), Equals, true)
}

func (LuviFairSuite) TestInvalidInputs(c *C) {
	good := luviState(u(4e12), u(1e12), u(1e12), u(0))
	_, err := good.FairValue(LuviPrice{})
	c.Check(err, NotNil)
	_, err = good.FairValue(mustPrice(c, "0"))
	c.Check(err, NotNil)
	_, err = good.FairValue(LuviPrice{v: big.NewInt(-1)})
	c.Check(err, NotNil)

	for _, s := range []LuviState{
		luviState(u(0), u(1e12), u(1e12), u(0)),
		luviState(u(4e12), u(0), u(1e12), u(0)),
		luviState(u(4e12), u(1e12), u(0), u(0)),
	} {
		_, err = s.FairValue(mustPrice(c, "4"))
		c.Check(err, NotNil)
	}
	c.Check(luviState(u(4e12), u(0), u(1e12), u(0)).SpotPrice().IsZero(), Equals, true)
}

func (LuviFairSuite) TestOracleAtSpotEqualsSpotValue(c *C) {
	// A = 1e12, R = 4e12, L = 1e12: P = 4, sqrt(A*R) = 2e12
	for _, tc := range []struct {
		synth                  uint64
		unitRune, unitAsset    string
		fairTotal, spotTotal   string
		overhang, hasDeviation bool
	}{
		{0, "8.000000000000000000", "2.000000000000000000", "8000000000000.000000000000000000", "8000000000000.000000000000000000", false, true},
		// S = A/2: liability S*P = 2e12
		{500_000_000_000, "6.000000000000000000", "1.500000000000000000", "6000000000000.000000000000000000", "6000000000000.000000000000000000", false, true},
	} {
		s := luviState(u(4e12), u(1e12), u(1e12), u(tc.synth))
		c.Check(s.SpotPrice().Dec().String(), Equals, "4.000000000000000000")
		fv := mustFair(c, s, s.SpotPrice())
		c.Check(fv.OraclePriceRune.String(), Equals, "4.000000000000000000")
		c.Check(fv.UnitFairValueRune.String(), Equals, tc.unitRune)
		c.Check(fv.UnitSpotValueRune.String(), Equals, tc.unitRune)
		c.Check(fv.UnitFairValueAsset.String(), Equals, tc.unitAsset)
		c.Check(fv.FairValueRune.String(), Equals, tc.fairTotal)
		c.Check(fv.SpotValueRune.String(), Equals, tc.spotTotal)
		c.Check(fv.DeviationBps.String(), Equals, "0")
		c.Check(fv.HasDeviation, Equals, tc.hasDeviation)
		c.Check(fv.Overhang, Equals, tc.overhang)
	}

	// random states: the oracle at spot reproduces the spot value up to rounding
	rng := rand.New(rand.NewSource(7)) // #nosec G404 deterministic test data
	for i := 0; i < 500; i++ {
		s := randState(rng, regime{"below overhang", 0, 180})
		fv := mustFair(c, s, s.SpotPrice())
		diff := fv.UnitFairValueRune.Sub(fv.UnitSpotValueRune).Abs()
		tol := fv.UnitSpotValueRune.Quo(cosmos.NewDec(1_000_000_000_000)).Add(sdkmath.LegacySmallestDec().MulInt64(10))
		c.Check(diff.LTE(tol), Equals, true, Commentf("%+v: fair %s spot %s", s, fv.UnitFairValueRune, fv.UnitSpotValueRune))
		c.Check(fv.DeviationBps.Abs().LTE(sdkmath.OneInt()), Equals, true, Commentf("%+v: %s", s, fv.DeviationBps))
	}
}

func (LuviFairSuite) TestSwapMovesSpotValueButNotFairValue(c *C) {
	for _, synth := range []uint64{0, 500_000_000_000} {
		s := luviState(u(4e12), u(1e12), u(1e12), u(synth))
		oracle := s.SpotPrice()
		before := mustFair(c, s, oracle)

		// exact: A*R = 4e24 -> A' = 2e12, R' = 2e12
		after := mustFair(c, kSwap(s, 1_000_000_000_000), oracle)
		c.Check(after.UnitFairValueRune.String(), Equals, before.UnitFairValueRune.String())
		c.Check(after.FairValueRune.String(), Equals, before.FairValueRune.String())
		// spot: 2R' - S*R'/A' = 4e12 - S
		want := cosmos.NewDec(4e12).Sub(cosmos.NewDec(int64(synth))).QuoInt64(1e12)
		c.Check(after.UnitSpotValueRune.String(), Equals, want.String())
		c.Check(after.DeviationBps.IsNegative(), Equals, true)
		if synth == 0 {
			c.Check(after.DeviationBps.String(), Equals, "-5000")
		}
	}

	// random sandwiches in both directions: fair value stays put up to rounding
	rng := rand.New(rand.NewSource(11)) // #nosec G404 deterministic test data
	for i := 0; i < 300; i++ {
		s := randState(rng, regime{"below overhang", 0, 150})
		oracle := s.SpotPrice()
		before := mustFair(c, s, oracle)
		a := s.BalanceAsset.BigInt().Int64()
		dx := a/2 - rng.Int63n(a/4+1) // up to +50% / -25% of the asset depth
		if rng.Intn(2) == 0 {
			dx = -dx / 2
		}
		mid := kSwap(s, dx)
		during := mustFair(c, mid, oracle)
		c.Check(during.UnitSpotValueRune.Equal(before.UnitSpotValueRune), Equals, false)
		// R' is truncated to an integer: a relative error of up to 1/R'
		minDepth := cosmos.MinUint(cosmos.MinUint(s.BalanceRune, s.BalanceAsset), cosmos.MinUint(mid.BalanceRune, mid.BalanceAsset))
		tol := before.UnitFairValueRune.MulInt64(2).QuoInt(sdkmath.NewIntFromBigInt(minDepth.BigInt())).Add(sdkmath.LegacySmallestDec().MulInt64(10))
		c.Check(during.UnitFairValueRune.Sub(before.UnitFairValueRune).Abs().LTE(tol), Equals, true,
			Commentf("%+v -> %+v: %s vs %s", s, mid, before.UnitFairValueRune, during.UnitFairValueRune))
	}
}

func (LuviFairSuite) TestFairValueOffOracle(c *C) {
	// without synths fair = 2R*sqrt(P/p), and by AM-GM it never exceeds the
	// pool's holdings valued at the oracle, R + A*P: an arbitrageur would take
	// the difference
	s := luviState(u(4e12), u(1e12), u(1e12), u(0))
	for _, tc := range []struct {
		p    string
		sign int
	}{{"1", 1}, {"3.9", 1}, {"4.1", -1}, {"16", -1}} {
		fv := mustFair(c, s, mustPrice(c, tc.p))
		c.Check(fv.DeviationBps.Sign(), Equals, tc.sign, Commentf("P=%s", tc.p))
		holdings := cosmos.NewDec(4e12).Add(sdkmath.LegacyMustNewDecFromStr(tc.p).MulInt64(1e12))
		c.Check(fv.FairValueRune.LTE(holdings), Equals, true, Commentf("P=%s", tc.p))
	}
	// P = 16: fair = 2*2e12*4 = 1.6e13 -> 16 per unit, 1 asset per unit
	fv := mustFair(c, s, mustPrice(c, "16"))
	c.Check(fv.UnitFairValueRune.String(), Equals, "16.000000000000000000")
	c.Check(fv.UnitFairValueAsset.String(), Equals, "1.000000000000000000")
	c.Check(fv.DeviationBps.String(), Equals, "-5000")
	fv = mustFair(c, s, mustPrice(c, "1"))
	c.Check(fv.UnitFairValueRune.String(), Equals, "4.000000000000000000")
	c.Check(fv.DeviationBps.String(), Equals, "10000")
}

func (LuviFairSuite) TestSynthLiability(c *C) {
	p := mustPrice(c, "4")
	base := mustFair(c, luviState(u(4e12), u(1e12), u(1e12), u(0)), p)
	for _, synth := range []int64{1, 1_000, 250_000_000_000, 1_000_000_000_000} {
		fv := mustFair(c, luviState(u(4e12), u(1e12), u(1e12), u(uint64(synth))), p)
		// each synth costs the LP side exactly P RUNE
		c.Check(base.FairValueRune.Sub(fv.FairValueRune).String(), Equals, cosmos.NewDec(synth*4).String())
	}
}

func (LuviFairSuite) TestOverhang(c *C) {
	p := mustPrice(c, "4")
	// S = 2A: fair value exactly zero at spot
	fv := mustFair(c, luviState(u(4e12), u(1e12), u(1e12), u(2e12)), p)
	c.Check(fv.FairValueRune.IsZero(), Equals, true)
	c.Check(fv.Overhang, Equals, true)
	c.Check(fv.HasDeviation, Equals, false)
	c.Check(fv.DeviationBps.IsZero(), Equals, true)
	c.Check(fv.UnitFairValueRune.IsZero(), Equals, true)
	c.Check(fv.UnitFairValueAsset.IsZero(), Equals, true)

	// S = 3A: negative fair and spot value, clamped per unit
	fv = mustFair(c, luviState(u(4e12), u(1e12), u(1e12), u(3e12)), p)
	c.Check(fv.FairValueRune.String(), Equals, "-4000000000000.000000000000000000")
	c.Check(fv.SpotValueRune.String(), Equals, "-4000000000000.000000000000000000")
	c.Check(fv.UnitFairValueRune.IsZero(), Equals, true)
	c.Check(fv.UnitSpotValueRune.IsZero(), Equals, true)
	c.Check(fv.Overhang, Equals, true)
	c.Check(fv.HasDeviation, Equals, false)

	// below chain overhang but the oracle says the liability exceeds the pool
	fv = mustFair(c, luviState(u(4e12), u(1e12), u(1e12), u(1_500_000_000_000)), mustPrice(c, "100"))
	c.Check(fv.Overhang, Equals, true)
	c.Check(fv.UnitFairValueRune.IsZero(), Equals, true)
	c.Check(fv.UnitSpotValueRune.String(), Equals, "2.000000000000000000")

	// fair value of a few 1e-36 RUNE: deviation too large to report
	huge := luviState(cosmos.NewUintFromString("1"+strings.Repeat("0", 76)), u(1), u(1), u(0))
	fv = mustFair(c, huge, LuviPrice{v: big.NewInt(1)})
	c.Check(fv.Overhang, Equals, false)
	c.Check(fv.HasDeviation, Equals, false)
}
