//go:build mocknet
// +build mocknet

package constants

import (
	"fmt"
	"sync"
	"testing"

	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type ConstantsSuite struct{}

var _ = Suite(&ConstantsSuite{})

func (s *ConstantsSuite) Test010(c *C) {
	consts := NewConstantValue()
	c.Check(consts.GetInt64Value(PoolCycle), Equals, int64(43200))
}

func (s *ConstantsSuite) TestCamelToSnakeUpper(c *C) {
	c.Check(CamelToSnakeUpper("PoolCycle"), Equals, "POOL_CYCLE")
	c.Check(CamelToSnakeUpper("L1SlipMinBps"), Equals, "L1_SLIP_MIN_BPS")
	c.Check(CamelToSnakeUpper("TNSRegisterFee"), Equals, "TNS_REGISTER_FEE")
	c.Check(CamelToSnakeUpper("MaxNodeToChurnOutForLowVersion"), Equals, "MAX_NODE_TO_CHURN_OUT_FOR_LOW_VERSION")
}

func (s *ConstantsSuite) TestCamelToSnakeUpperCacheBounded(c *C) {
	for i := 0; i < camelToSnakeCacheMax+100; i++ {
		key := fmt.Sprintf("QueryKey%d", i)
		c.Assert(CamelToSnakeUpper(key), Equals, fmt.Sprintf("QUERY_KEY%d", i))
	}
	c.Check(camelToSnakeCacheLen.Load(), Equals, int64(camelToSnakeCacheMax))
	n := 0
	camelToSnakeCache.Range(func(_, _ any) bool { n++; return true })
	c.Check(n, Equals, camelToSnakeCacheMax)
	// past the bound, conversions are still correct (just not memoized)
	c.Check(CamelToSnakeUpper("PoolCycle"), Equals, "POOL_CYCLE")
	c.Check(CamelToSnakeUpper("QueryKeyPastBound"), Equals, "QUERY_KEY_PAST_BOUND")
}

func (s *ConstantsSuite) TestCamelToSnakeUpperCacheBoundedConcurrent(c *C) {
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < camelToSnakeCacheMax; i++ {
				_ = CamelToSnakeUpper(fmt.Sprintf("Concurrent%dKey%d", g, i))
			}
		}(g)
	}
	wg.Wait()
	n := 0
	camelToSnakeCache.Range(func(_, _ any) bool { n++; return true })
	c.Check(n, Equals, camelToSnakeCacheMax)
	c.Check(camelToSnakeCacheLen.Load(), Equals, int64(camelToSnakeCacheMax))
}
