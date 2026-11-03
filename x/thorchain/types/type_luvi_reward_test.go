package types

import (
	"math/big"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type LuviRewardSuite struct{}

var _ = Suite(&LuviRewardSuite{})

func luviRewardState(rune, asset, units, synths uint64) LuviState {
	p := NewPool()
	p.BalanceRune = cosmos.NewUint(rune)
	p.BalanceAsset = cosmos.NewUint(asset)
	p.LPUnits = cosmos.NewUint(units)
	return NewLuviState(p, cosmos.NewUint(synths))
}

func withRune(s LuviState, rune cosmos.Uint) LuviState {
	s.BalanceRune = rune
	return s
}

func (s *LuviRewardSuite) TestRewardWithoutSynths(c *C) {
	pre := luviRewardState(1_000_000_00000000, 500_000_00000000, 1_000_000_00000000, 0)
	r := cosmos.NewUint(1_234_56789012)
	m, err := MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r)), r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, true)
	c.Check(m.DeltaG.IsPositive(), Equals, true)
	c.Check(m.ExpectedLPShareBps, Equals, int64(10_000))
	c.Check(m.LPShareBps, Equals, int64(10_000))

	// the expected dG is sqrt(A(R+r)) - sqrt(AR), independent of an exact check
	exp := new(big.Int).Sqrt(new(big.Int).Mul(big.NewInt(500_000_00000000), big.NewInt(1_000_000_00000000+1_234_56789012)))
	exp.Sub(exp, new(big.Int).Sqrt(new(big.Int).Mul(big.NewInt(500_000_00000000), big.NewInt(1_000_000_00000000))))
	c.Check(m.ExpectedDeltaG.TruncateInt().BigInt().Int64()-exp.Int64() <= 1, Equals, true)
}

func (s *LuviRewardSuite) TestRewardWithSynths(c *C) {
	for _, pct := range []uint64{10, 50, 100, 150} {
		asset := uint64(500_000_00000000)
		synths := asset * pct / 100
		pre := luviRewardState(1_000_000_00000000, asset, 1_000_000_00000000, synths)
		r := cosmos.NewUint(987_65432101)
		m, err := MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r)), r)
		c.Assert(err, IsNil)
		c.Check(m.OK, Equals, true, Commentf("pct %d", pct))
		// 1 - S/(2A)
		want := int64(10_000 - 10_000*pct/200)
		c.Check(m.ExpectedLPShareBps, Equals, want)
		c.Check(m.LPShareBps < 10_000, Equals, true)
		c.Check(m.LPShareBps, Equals, want, Commentf("pct %d", pct))

		// the hedged dG does not depend on the synths
		free := luviRewardState(1_000_000_00000000, asset, 1_000_000_00000000, 0)
		mf, err := MeasureLuviReward(free, withRune(free, free.BalanceRune.Add(r)), r)
		c.Assert(err, IsNil)
		c.Check(m.ExpectedDeltaG.Equal(mf.ExpectedDeltaG), Equals, true)
	}
}

func (s *LuviRewardSuite) TestRewardInOverhang(c *C) {
	// S = 3A: LP equity is negative and the reward makes it more so
	asset := uint64(100_000_00000000)
	pre := luviRewardState(200_000_00000000, asset, 200_000_00000000, 3*asset)
	r := cosmos.NewUint(10_00000000)
	m, err := MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r)), r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, true)
	c.Check(m.ExpectedLPShareBps, Equals, int64(-5_000))
	c.Check(m.LPShareBps, Equals, int64(-5_000))
}

func (s *LuviRewardSuite) TestRewardMisallocated(c *C) {
	pre := luviRewardState(1_000_000_00000000, 500_000_00000000, 1_000_000_00000000, 100_000_00000000)
	r := cosmos.NewUint(1_000_00000000)

	// never landed
	m, err := MeasureLuviReward(pre, pre, r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)
	c.Check(m.DeltaG.IsZero(), Equals, true)
	c.Check(m.LPShareBps, Equals, int64(0))

	// half landed
	m, err = MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r.QuoUint64(2))), r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)
	c.Check(m.DeltaG.IsPositive(), Equals, true)

	// one base unit short of the reward
	m, err = MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r).SubUint64(1)), r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)

	// twice the reward (another pool's reward credited here)
	m, err = MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r.MulUint64(2))), r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)

	// a synth mint in the same step
	post := withRune(pre, pre.BalanceRune.Add(r))
	post.SynthSupply = post.SynthSupply.AddUint64(1_00000000)
	m, err = MeasureLuviReward(pre, post, r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)

	// the pool lost its depth
	post = pre
	post.BalanceAsset = cosmos.ZeroUint()
	m, err = MeasureLuviReward(pre, post, r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)
	c.Check(m.ExpectedDeltaG.IsPositive(), Equals, true)

	// an invalid pre-state cannot be measured
	_, err = MeasureLuviReward(LuviState{BalanceRune: cosmos.ZeroUint(), BalanceAsset: cosmos.ZeroUint(), LPUnits: cosmos.ZeroUint(), PoolUnits: cosmos.ZeroUint(), SynthSupply: cosmos.ZeroUint()}, pre, r)
	c.Check(err, NotNil)

	// a zero reward with no change: dG is not positive
	m, err = MeasureLuviReward(pre, pre, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)
}

func (s *LuviRewardSuite) TestRewardRoundingTolerance(c *C) {
	// the smallest reward in a deep pool still matches and still fails when absent
	pre := luviRewardState(100_000_000_00000000, 7_000_000_00000000, 100_000_000_00000000, 3_000_000_00000000)
	r := cosmos.NewUint(1)
	m, err := MeasureLuviReward(pre, withRune(pre, pre.BalanceRune.Add(r)), r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, true)
	m, err = MeasureLuviReward(pre, pre, r)
	c.Assert(err, IsNil)
	c.Check(m.OK, Equals, false)
	c.Check(luviExpectedLPShareBps(LuviState{BalanceAsset: cosmos.ZeroUint(), SynthSupply: cosmos.ZeroUint()}), Equals, int64(0))
}
