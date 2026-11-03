package common

import (
	"errors"
	"math/big"
	"slices"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MathSuite struct{}

var _ = Suite(&MathSuite{})

func (s *MathSuite) TestMax(c *C) {
	c.Assert(Max(1, 2), Equals, 2)
	c.Assert(Max(2, 1), Equals, 2)
	c.Assert(Max(1, 1), Equals, 1)

	c.Assert(Max(int64(1), int64(2)), Equals, int64(2))
	c.Assert(Max(int64(2), int64(1)), Equals, int64(2))
	c.Assert(Max(int64(1), int64(1)), Equals, int64(1))

	c.Assert(Max(uint(1), uint(2)), Equals, uint(2))
	c.Assert(Max(uint(2), uint(1)), Equals, uint(2))
	c.Assert(Max(uint(1), uint(1)), Equals, uint(1))
}

func (s *MathSuite) TestMin(c *C) {
	c.Assert(Min(1, 2), Equals, 1)
	c.Assert(Min(2, 1), Equals, 1)
	c.Assert(Min(1, 1), Equals, 1)

	c.Assert(Min(int64(1), int64(2)), Equals, int64(1))
	c.Assert(Min(int64(2), int64(1)), Equals, int64(1))
	c.Assert(Min(int64(1), int64(1)), Equals, int64(1))

	c.Assert(Min(uint(1), uint(2)), Equals, uint(1))
	c.Assert(Min(uint(2), uint(1)), Equals, uint(1))
	c.Assert(Min(uint(1), uint(1)), Equals, uint(1))
}

func (s *MathSuite) TestAbs(c *C) {
	c.Assert(Abs(1), Equals, 1)
	c.Assert(Abs(-1), Equals, 1)
	c.Assert(Abs(0), Equals, 0)

	c.Assert(Abs(int64(1)), Equals, int64(1))
	c.Assert(Abs(int64(-1)), Equals, int64(1))
	c.Assert(Abs(int64(0)), Equals, int64(0))
}

func (s *MathSuite) TestWeightedMean(c *C) {
	vals := []cosmos.Uint{cosmos.NewUint(10), cosmos.NewUint(20), cosmos.NewUint(30)}
	weights := []cosmos.Uint{cosmos.NewUint(1), cosmos.NewUint(2), cosmos.NewUint(3)}
	expectedMean := cosmos.NewUint(140 / 6) // (10*1 + 20*2 + 30*3) / (1+2+3)
	mean, err := WeightedMean(vals, weights)
	c.Assert(err, IsNil)
	c.Assert(mean.String(), Equals, expectedMean.String())
}

func (s *MathSuite) TestWeightedMeanErrors(c *C) {
	// mismatched values and weights
	valsMismatch := []cosmos.Uint{cosmos.NewUint(10), cosmos.NewUint(20)}
	weightsMismatch := []cosmos.Uint{cosmos.NewUint(1), cosmos.NewUint(2), cosmos.NewUint(3)}
	testFn := func() { _, _ = WeightedMean(valsMismatch, weightsMismatch) }
	c.Assert(testFn, PanicMatches, "number of values and weights do not match")

	// zero total weight
	valsZero := []cosmos.Uint{cosmos.NewUint(10), cosmos.NewUint(20), cosmos.NewUint(30)}
	weightsZero := []cosmos.Uint{cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()}
	_, errZero := WeightedMean(valsZero, weightsZero)
	c.Assert(errZero, DeepEquals, errors.New("total weight is zero"))
}

func (s *MathSuite) TestMedianAverageDeviation(c *C) {
	testCases := []struct {
		Values    []float64
		Deviation string
		Median    string
		Fail      bool
	}{
		{
			Values:    []float64{4.43, 5.35, 1.26, 7.62, 7.11, 1.24, 7.01, 8.18, 9.82, 8.77},
			Deviation: "1.71",
			Median:    "7.06",
		},
		{
			Values:    []float64{4.43, 5.35},
			Deviation: "0.46",
			Median:    "4.89",
		},
		{
			Values:    []float64{4.43},
			Deviation: "0.00",
			Median:    "4.43",
		},
		{
			Values:    []float64{0, 0, 0},
			Deviation: "0.00",
			Median:    "0.00",
		},
		{
			Values: []float64{},
			Fail:   true,
		},
		{
			Values:    []float64{100003, 100006},
			Deviation: "1.50",
			Median:    "100004.50",
		},
	}

	for _, tc := range testCases {
		values := make([]*big.Float, len(tc.Values))
		for i, value := range tc.Values {
			values[i] = big.NewFloat(value)
		}

		deviation, median, err := MedianAbsoluteDeviation(values)

		if tc.Fail {
			c.Assert(err, NotNil)
			continue
		}

		c.Assert(err, IsNil)
		c.Assert(deviation.Text('f', 2), Equals, tc.Deviation)
		c.Assert(median.Text('f', 2), Equals, tc.Median)
	}
}

// TestMedianAbsoluteDeviationOperands checks what callers see beyond the
// values: the input slice ends up stably sorted (among equal values the same
// *big.Float is the median), no input is modified, and the deviation keeps
// the precision, mode and accuracy of |value - median| computed into fresh
// Floats.
func (s *MathSuite) TestMedianAbsoluteDeviationOperands(c *C) {
	// few distinct values, so many ties, each object at its own precision
	values := make([]*big.Float, 31)
	for i := range values {
		values[i] = new(big.Float).SetPrec(uint(64 + i)).SetInt64(int64(i*7%5) - 2)
	}
	before := map[*big.Float]string{}
	for _, v := range values {
		before[v] = v.Text('p', 0)
	}
	want := slices.Clone(values)
	sort.SliceStable(want, func(i, j int) bool { return want[i].Cmp(want[j]) < 0 })
	wantMedian := want[len(want)/2]
	refs := make([]*big.Float, len(want))
	for i, v := range want {
		refs[i] = new(big.Float).Abs(new(big.Float).Sub(v, wantMedian))
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Cmp(refs[j]) < 0 })
	wantMAD := refs[len(refs)/2]

	deviation, median, err := MedianAbsoluteDeviation(values)
	c.Assert(err, IsNil)

	c.Assert(median == wantMedian, Equals, true)
	for i := range values {
		c.Assert(values[i] == want[i], Equals, true, Commentf("index %d", i))
	}
	for v, text := range before {
		c.Assert(v.Text('p', 0), Equals, text)
	}
	c.Assert(deviation.Text('p', 0), Equals, wantMAD.Text('p', 0))
	c.Assert(deviation.Prec(), Equals, wantMAD.Prec())
	c.Assert(deviation.Mode(), Equals, wantMAD.Mode())
	c.Assert(deviation.Acc(), Equals, wantMAD.Acc())
}
