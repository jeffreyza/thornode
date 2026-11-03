package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

type KeeperKeygenChainsSuite struct{}

var _ = Suite(&KeeperKeygenChainsSuite{})

func (s *KeeperKeygenChainsSuite) TestLastKeygenChains_EmptyKeeper(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeStandby)

	height, chains, err := k.GetLastKeygenChains(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(0))
	c.Check(chains, HasLen, 0)
}

func (s *KeeperKeygenChainsSuite) TestLastKeygenChains_LatestReportWins(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeStandby)

	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain, common.ETHChain}, 100)
	height, chains, err := k.GetLastKeygenChains(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(100))
	c.Check(chains.Strings(), DeepEquals, []string{common.BTCChain.String(), common.ETHChain.String()})

	// A chain dropped from a later report is no longer part of the declared set, even
	// though its own record survives at the earlier height. This is the whole point of
	// the store: it distinguishes "declared this round" from "declared once, long ago".
	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain}, 200)
	height, chains, err = k.GetLastKeygenChains(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(200))
	c.Check(chains.Strings(), DeepEquals, []string{common.BTCChain.String()})
}

func (s *KeeperKeygenChainsSuite) TestLastKeygenChains_ForwardOnly(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeStandby)

	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain}, 200)
	// A report for an older keygen block arriving late must not overwrite the newer one.
	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain, common.ETHChain}, 150)

	height, chains, err := k.GetLastKeygenChains(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(200))
	c.Check(chains.Strings(), DeepEquals, []string{common.BTCChain.String()})
}

func (s *KeeperKeygenChainsSuite) TestLastKeygenChains_PerNode(c *C) {
	ctx, k := setupKeeperForTest(c)
	na1 := GetRandomValidatorNode(NodeStandby)
	na2 := GetRandomValidatorNode(NodeStandby)

	k.SetLastKeygenChains(ctx, na1.NodeAddress, common.Chains{common.BTCChain, common.ETHChain}, 100)
	k.SetLastKeygenChains(ctx, na2.NodeAddress, common.Chains{common.BTCChain}, 100)

	_, chains, err := k.GetLastKeygenChains(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(chains, HasLen, 2)

	_, chains, err = k.GetLastKeygenChains(ctx, na2.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(chains.Strings(), DeepEquals, []string{common.BTCChain.String()})
}

func (s *KeeperKeygenChainsSuite) TestLastKeygenChains_SameHeightIntersects(c *C) {
	ctx, k := setupKeeperForTest(c)
	full := common.Chains{common.BTCChain, common.ETHChain, common.SOLChain}
	partial := common.Chains{common.BTCChain, common.ETHChain}

	// MsgTssPool.ID does not cover Chains, so a second report for the same keygen
	// block (a bogus voter) can carry the full set. Whichever lands second, the
	// incomplete report must survive: a union or a replace would let the full one
	// mask it.
	for _, order := range []struct {
		name          string
		first, second common.Chains
	}{
		{"partial then full", partial, full},
		{"full then partial", full, partial},
	} {
		na := GetRandomValidatorNode(NodeActive)
		k.SetLastKeygenChains(ctx, na.NodeAddress, order.first, 100)
		k.SetLastKeygenChains(ctx, na.NodeAddress, order.second, 100)

		height, chains, err := k.GetLastKeygenChains(ctx, na.NodeAddress)
		c.Assert(err, IsNil, Commentf(order.name))
		c.Check(height, Equals, int64(100), Commentf(order.name))
		c.Check(chains.Strings(), DeepEquals, partial.Strings(), Commentf(order.name))
	}
}

func (s *KeeperKeygenChainsSuite) TestLastKeygenChains_NewerHeightReplaces(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)

	// A narrowed set at one height does not carry into the next report: a newer
	// keygen block is judged on its own report alone.
	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain, common.ETHChain}, 100)
	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain}, 100)
	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain, common.ETHChain, common.SOLChain}, 200)

	height, chains, err := k.GetLastKeygenChains(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(200))
	c.Check(chains.Strings(), DeepEquals, []string{common.BTCChain.String(), common.ETHChain.String(), common.SOLChain.String()})

	// A chain dropped by the newer report is pruned from the store, not just
	// filtered at read time.
	k.SetLastKeygenChains(ctx, na.NodeAddress, common.Chains{common.BTCChain}, 300)
	prefixKey := k.GetKey(prefixLastKeygenChain, na.NodeAddress.String(), "/")
	iter := k.getIterator(ctx, types.DbPrefix(prefixKey))
	defer iter.Close()
	stored := 0
	for ; iter.Valid(); iter.Next() {
		stored++
	}
	c.Check(stored, Equals, 1)
}

func (s *KeeperKeygenChainsSuite) TestChainSetStalls_PerCycle(c *C) {
	ctx, k := setupKeeperForTest(c)

	chains, err := k.GetChainSetStalls(ctx, 500)
	c.Assert(err, IsNil)
	c.Check(chains, HasLen, 0)

	k.SetChainSetStall(ctx, common.ETHChain, 400)
	k.SetChainSetStall(ctx, common.BTCChain, 500)
	chains, err = k.GetChainSetStalls(ctx, 500)
	c.Assert(err, IsNil)
	c.Check(chains, DeepEquals, common.Chains{common.BTCChain}, Commentf("a record from an earlier cycle does not count"))

	// A stall in the current cycle supersedes the old record; results are in chain order.
	k.SetChainSetStall(ctx, common.ETHChain, 500)
	chains, err = k.GetChainSetStalls(ctx, 500)
	c.Assert(err, IsNil)
	c.Check(chains, DeepEquals, common.Chains{common.BTCChain, common.ETHChain})
}
