package keeperv1

import (
	"sort"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

type KeeperLastHeightSuite struct{}

var _ = Suite(&KeeperLastHeightSuite{})

func (s *KeeperLastHeightSuite) TestLastHeight_EmptyKeeper(c *C) {
	ctx, k := setupKeeperForTest(c)

	last, err := k.GetLastSignedHeight(ctx)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(0))

	last, err = k.GetLastChainHeight(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(0))
}

func (s *KeeperLastHeightSuite) TestLastHeight_SetKeeperSingleChain(c *C) {
	ctx, k := setupKeeperForTest(c)

	err := k.SetLastSignedHeight(ctx, 12)
	c.Assert(err, IsNil)
	last, err := k.GetLastSignedHeight(ctx)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(12))
	c.Check(k.SetLastSignedHeight(ctx, 10), IsNil)

	err = k.SetLastChainHeight(ctx, common.ETHChain, 14)
	c.Assert(err, IsNil)
	last, err = k.GetLastChainHeight(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(14))
}

func (s *KeeperLastHeightSuite) TestLastHeight_SetKeeperMultipleChains(c *C) {
	ctx, k := setupKeeperForTest(c)
	err := k.SetLastChainHeight(ctx, common.BTCChain, 23)
	c.Assert(err, IsNil)
	err = k.SetLastChainHeight(ctx, common.ETHChain, 14)
	c.Assert(err, IsNil)
	last, err := k.GetLastChainHeight(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(23))
	last, err = k.GetLastChainHeight(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(14))
	c.Check(k.SetLastChainHeight(ctx, common.BTCChain, 20), NotNil)
}

func (s *KeeperLastHeightSuite) TestGetLastChainHeights(c *C) {
	ctx, k := setupKeeperForTest(c)
	err := k.SetLastChainHeight(ctx, common.BTCChain, 23)
	c.Assert(err, IsNil)
	err = k.SetLastChainHeight(ctx, common.ETHChain, 14)
	c.Assert(err, IsNil)
	last, err := k.GetLastChainHeight(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(23))
	last, err = k.GetLastChainHeight(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(last, Equals, int64(14))
	result, err := k.GetLastChainHeights(ctx)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *KeeperLastHeightSuite) TestSetLastObserveHeight(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetLastObserveHeight(ctx, common.BTCChain, na.NodeAddress, 1024), IsNil)
	c.Assert(k.SetLastObserveHeight(ctx, common.BTCChain, na.NodeAddress, 1025), IsNil)
	result, err := k.GetLastObserveHeight(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(result, DeepEquals, []kvTypes.ChainHeight{
		{Chain: common.BTCChain, Height: 1025},
	})

	c.Assert(k.SetLastObserveHeight(ctx, common.ETHChain, na.NodeAddress, 1114), IsNil)
	result, err = k.GetLastObserveHeight(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(result, DeepEquals, []kvTypes.ChainHeight{
		{Chain: common.BTCChain, Height: 1025},
		{Chain: common.ETHChain, Height: 1114},
	})
}

// TestGetLastObserveHeightIsSorted asserts chains come back in ascending order no
// matter what order they were written in. Callers rely on this instead of sorting;
// the node queries are reachable from WASM contracts, where a varying response
// order diverges gas metering and therefore the app hash.
func (s *KeeperLastHeightSuite) TestGetLastObserveHeightIsSorted(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)

	// deliberately not alphabetical
	writeOrder := []common.Chain{
		common.TRONChain, common.BTCChain, common.GAIAChain, common.AVAXChain,
		common.ETHChain, common.DOGEChain, common.LTCChain, common.BCHChain,
	}
	for i, chain := range writeOrder {
		c.Assert(k.SetLastObserveHeight(ctx, chain, na.NodeAddress, int64(1000+i)), IsNil)
	}

	result, err := k.GetLastObserveHeight(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(result, HasLen, len(writeOrder))

	got := make([]string, len(result))
	for i, chainHeight := range result {
		got[i] = chainHeight.Chain.String()
	}
	expected := make([]string, len(writeOrder))
	for i, chain := range writeOrder {
		expected[i] = chain.String()
	}
	sort.Strings(expected)
	c.Assert(got, DeepEquals, expected)
}
