package tss

import (
	. "gopkg.in/check.v1"
)

// Each keyshare encryption runs scrypt twice (encrypt, then the round-trip
// decrypt) at 2^18, about 0.5-1s per call and several times that under
// -race. The tests exercise the same code with a smaller work factor; the
// decrypt side reads the factor from the age header.
func init() {
	ageWorkFactor = 10
}

type AgeWorkFactorSuite struct{}

var _ = Suite(&AgeWorkFactorSuite{})

func (AgeWorkFactorSuite) TestProductionWorkFactor(c *C) {
	c.Assert(AgeWorkFactor, Equals, 18)
}
