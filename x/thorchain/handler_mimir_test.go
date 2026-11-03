package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerMimirSuite struct{}

var _ = Suite(&HandlerMimirSuite{})

func (s *HandlerMimirSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerMimirSuite) TestValidate(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	handler := NewMimirHandler(NewDummyMgrWithKeeper(keeper))

	// invalid msg
	msg := &MsgMimir{}
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerMimirSuite) TestMimirHandle(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	handler := NewMimirHandler(NewDummyMgrWithKeeper(keeper))

	invalidMsg := NewMsgNetworkFee(ctx.BlockHeight(), common.ETHChain, 1, 10000, GetRandomBech32Addr())
	result, err := handler.Run(ctx, invalidMsg)
	c.Check(err, NotNil)
	c.Check(result, IsNil)

	// Non-validator address for MsgMimir (so fails validation).
	addr := GetRandomBech32Addr()
	msg := NewMsgMimir("foo", 55, addr)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// node set mimir
	FundModule(c, ctx, keeper, BondName, 100*common.One)
	ver := "1.92.0"
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Version = ver
	na2 := GetRandomValidatorNode(NodeActive)
	na2.Version = ver
	na3 := GetRandomValidatorNode(NodeActive)
	na3.Version = ver
	c.Assert(keeper.SetNodeAccount(ctx, na1), IsNil)
	c.Assert(keeper.SetNodeAccount(ctx, na2), IsNil)
	c.Assert(keeper.SetNodeAccount(ctx, na3), IsNil)

	result, err = handler.Run(ctx, NewMsgMimir("AffiliateFeeBasisPointsMax", 1, na1.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	mimirs, err := keeper.GetNodeMimirs(ctx, "AffiliateFeeBasisPointsMax")
	c.Assert(err, IsNil)
	c.Assert(mimirs.Mimirs, HasLen, 1)

	// first node set mimir , no consensus
	result, err = handler.Run(ctx, NewMsgMimir("node-mimir", 1, na1.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	mvalue, err := keeper.GetMimir(ctx, "node-mimir")
	c.Assert(err, IsNil)
	c.Assert(mvalue, Equals, int64(-1))

	// second node set mimir, reach consensus
	result, err = handler.Run(ctx, NewMsgMimir("node-mimir", 1, na2.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	mvalue, err = keeper.GetMimir(ctx, "node-mimir")
	c.Assert(err, IsNil)
	c.Assert(mvalue, Equals, int64(1))

	// third node set mimir, reach consensus
	result, err = handler.Run(ctx, NewMsgMimir("node-mimir", 1, na3.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	mvalue, err = keeper.GetMimir(ctx, "node-mimir")
	c.Assert(err, IsNil)
	c.Assert(mvalue, Equals, int64(1))

	// third node vote mimir to a different value, it should not change the admin mimir value
	result, err = handler.Run(ctx, NewMsgMimir("node-mimir", 0, na3.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	mvalue, err = keeper.GetMimir(ctx, "node-mimir")
	c.Assert(err, IsNil)
	c.Assert(mvalue, Equals, int64(1))

	// second node vote mimir to a different value , it should update admin mimir
	result, err = handler.Run(ctx, NewMsgMimir("node-mimir", 0, na2.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	mvalue, err = keeper.GetMimir(ctx, "node-mimir")
	c.Assert(err, IsNil)
	c.Assert(mvalue, Equals, int64(0))

	result, err = handler.Run(ctx, NewMsgMimir("node-mimir-1", 0, na2.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// With OperationalVotesMin 3, Operational Mimir should not be set until at least three nodes agree.
	keeper.SetMimir(ctx, constants.OperationalVotesMin.String(), 3)

	result, err = handler.Run(ctx, NewMsgMimir("HaltSigning", 1, na1.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	result, err = handler.Run(ctx, NewMsgMimir("HaltSigning", 1, na2.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	val, err := keeper.GetMimir(ctx, "HaltSigning")
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(-1))
	// Now the third.
	result, err = handler.Run(ctx, NewMsgMimir("HaltSigning", 1, na3.NodeAddress))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	val, err = keeper.GetMimir(ctx, "HaltSigning")
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(1))
}

func (s *HandlerMimirSuite) TestDisableTSSSignerFilteringOperationalVoting(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	handler := NewMimirHandler(NewDummyMgrWithKeeper(keeper))
	FundModule(c, ctx, keeper, BondName, 100*common.One)

	// Three operational votes must suffice even though eight active nodes
	// would require six votes for an economic Mimir.
	nodes := make(NodeAccounts, 8)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(keeper.SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	key := constants.MimirKeyDisableTSSSignerFiltering
	keeper.SetMimir(ctx, constants.OperationalVotesMin.String(), 3)
	keeper.SetMimir(ctx, key, 0)

	// Both disabling and restoring filtering require the operational minimum.
	for _, value := range []int64{1, 0} {
		for i, node := range nodes[:3] {
			result, err := handler.Run(ctx, NewMsgMimir(key, value, node.NodeAddress))
			c.Assert(err, IsNil)
			c.Assert(result, NotNil)
			actual, err := keeper.GetMimir(ctx, key)
			c.Assert(err, IsNil)
			expected := 1 - value
			if i == 2 {
				expected = value
			}
			c.Assert(actual, Equals, expected, Commentf("value=%d votes=%d", value, i+1))
		}
	}

	// Churn clears operational votes, but preserves the effective Mimir value
	// and unrelated economic votes.
	c.Assert(keeper.SetNodeMimir(ctx, "EconomicMimir", 1, nodes[0].NodeAddress), IsNil)
	keeper.PurgeOperationalNodeMimirs(ctx)
	votes, err := keeper.GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(votes.Mimirs, HasLen, 0)
	actual, err := keeper.GetMimir(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(actual, Equals, int64(0))
	votes, err = keeper.GetNodeMimirs(ctx, "EconomicMimir")
	c.Assert(err, IsNil)
	c.Assert(votes.Mimirs, HasLen, 1)
}

func (HandlerMimirSuite) TestRevShareValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	h := NewMimirHandler(mgr)

	// Signer must be an active validator so validateMimirAuth passes.
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// Create a thorname so the validation passes for existence checks.
	name := types.NewTHORName("foobar", 1000, []types.THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}})
	k.SetTHORName(ctx, name)

	mkMsg := func(key string, value int64) MsgMimir {
		return MsgMimir{Key: key, Value: value, Signer: na.NodeAddress}
	}

	// 1. Non-existent thorname -> reject
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-nobody", 100)), NotNil)

	// 2. Existing thorname, valid value -> accept
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", 100)), IsNil)

	// 2b. Value of 0 (disable) -> accept (inclusive lower bound)
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", 0)), IsNil)

	// 3. Value at cap -> accept
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", constants.RevShareMaxBps)), IsNil)

	// 4. Value over cap -> reject
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", constants.RevShareMaxBps+1)), NotNil)

	// 5. Negative value -> reject
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foobar", -1)), NotNil)

	// 6. REVSHARE key with invalid thorname characters -> targeted error
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foo_bar", 100)), NotNil)
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foo+bar", 100)), NotNil)

	// 7. Hyphen-containing thorname is Mimir-safe and must be accepted (proves
	// the REVSHARE- prefix is stripped whole, not tokenised on '-').
	hyphenName := types.NewTHORName("foo-bar", 1000, []types.THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}})
	k.SetTHORName(ctx, hyphenName)
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-foo-bar", 100)), IsNil)

	// 8. Aliased affiliate: SwapKit's thorname "-_" has Mimir-illegal chars, so
	// nodes vote REVSHARE-SWAPKIT. The alias must resolve to the real thorname.
	swapkit := types.NewTHORName("-_", 1000, []types.THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}})
	k.SetTHORName(ctx, swapkit)
	c.Assert(h.validate(ctx, mkMsg("REVSHARE-SWAPKIT", 100)), IsNil)
	c.Assert(h.validate(ctx, mkMsg("revshare-swapkit", 100)), IsNil) // case-insensitive
}

func (HandlerMimirSuite) TestRevShareAliasRoundTrip(c *C) {
	// The alias token the payout side builds must match what validation resolves,
	// otherwise a voted REVSHARE-SWAPKIT bps would never reach the "-_" thorname.
	c.Assert(revShareThornameToAlias("-_"), Equals, "SWAPKIT")
	c.Assert(revShareThornameToAlias("foobar"), Equals, "foobar") // unaliased passthrough
	c.Assert(revShareAliasToThorname("SWAPKIT"), Equals, "-_")
	c.Assert(revShareAliasToThorname("swapkit"), Equals, "-_") // case-insensitive
	c.Assert(revShareAliasToThorname("FOOBAR"), Equals, "FOOBAR")
}

func (HandlerMimirSuite) TestRevShareIsOperational(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_ = ctx
	c.Assert(mgr.Keeper().IsOperationalMimir("REVSHARE-foobar"), Equals, true)
	c.Assert(mgr.Keeper().IsOperationalMimir("revshare-foobar"), Equals, true) // case-insensitive
}

func (s *HandlerMimirSuite) TestAllowNoVaultOperationalVoting(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	handler := NewMimirHandler(NewDummyMgrWithKeeper(keeper))
	FundModule(c, ctx, keeper, BondName, 100*common.One)

	// Three operational votes must suffice even though eight active nodes
	// would require six votes for an economic Mimir.
	nodes := make(NodeAccounts, 8)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(keeper.SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	key := fmt.Sprintf(constants.MimirTemplateAllowNoVault, common.TRONChain.String())
	keeper.SetMimir(ctx, constants.OperationalVotesMin.String(), 3)
	keeper.SetMimir(ctx, key, 0)

	// Both opening and shutting the novault gate require only the operational minimum.
	for _, value := range []int64{1, 0} {
		for i, node := range nodes[:3] {
			result, err := handler.Run(ctx, NewMsgMimir(key, value, node.NodeAddress))
			c.Assert(err, IsNil)
			c.Assert(result, NotNil)
			actual, err := keeper.GetMimir(ctx, key)
			c.Assert(err, IsNil)
			// The first two votes are below OperationalVotesMin, so the previous
			// value (1-value) still stands; the third vote reaches it and flips.
			expected := 1 - value
			if i == 2 {
				expected = value
			}
			c.Assert(actual, Equals, expected, Commentf("value=%d votes=%d", value, i+1))
		}
	}

	// Churn clears operational votes, but preserves the effective Mimir value
	// and unrelated economic votes.
	c.Assert(keeper.SetNodeMimir(ctx, "EconomicMimir", 1, nodes[0].NodeAddress), IsNil)
	keeper.PurgeOperationalNodeMimirs(ctx)
	votes, err := keeper.GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(votes.Mimirs, HasLen, 0)
	actual, err := keeper.GetMimir(ctx, key)
	c.Assert(err, IsNil)
	c.Assert(actual, Equals, int64(0))
	votes, err = keeper.GetNodeMimirs(ctx, "EconomicMimir")
	c.Assert(err, IsNil)
	c.Assert(votes.Mimirs, HasLen, 1)
}

func (s *HandlerMimirSuite) TestXMRConsolidationOperationalVoting(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	handler := NewMimirHandler(NewDummyMgrWithKeeper(keeper))
	FundModule(c, ctx, keeper, BondName, 100*common.One)

	// Three operational votes must suffice even though eight active nodes
	// would require six votes for an economic Mimir.
	nodes := make(NodeAccounts, 8)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(keeper.SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	keeper.SetMimir(ctx, constants.OperationalVotesMin.String(), 3)
	enabledKey := "XMRConsolidationEnabled"
	lockKey := "XMRConsolidationMaxLockBps"
	keeper.SetMimir(ctx, enabledKey, 1)
	keeper.SetMimir(ctx, lockKey, 1000)

	vote := func(key string, value int64) {
		for i, node := range nodes[:3] {
			before, err := keeper.GetMimir(ctx, key)
			c.Assert(err, IsNil)
			result, err := handler.Run(ctx, NewMsgMimir(key, value, node.NodeAddress))
			c.Assert(err, IsNil)
			c.Assert(result, NotNil)
			actual, err := keeper.GetMimir(ctx, key)
			c.Assert(err, IsNil)
			// Below OperationalVotesMin the previous value stands; the third vote flips it.
			expected := before
			if i == 2 {
				expected = value
			}
			c.Assert(actual, Equals, expected, Commentf("key=%s value=%d votes=%d", key, value, i+1))
		}
	}
	// Disabling, re-enabling and retuning all take only the operational minimum.
	vote(enabledKey, 0)
	vote(enabledKey, 1)
	vote(lockKey, 500)

	// MaxUTXOsToSpend is shared with the other UTXO chains and stays economic:
	// the same three votes change nothing.
	keeper.SetMimir(ctx, "MaxUTXOsToSpend", 15)
	for _, node := range nodes[:3] {
		_, err := handler.Run(ctx, NewMsgMimir("MaxUTXOsToSpend", 5, node.NodeAddress))
		c.Assert(err, IsNil)
	}
	actual, err := keeper.GetMimir(ctx, "MaxUTXOsToSpend")
	c.Assert(err, IsNil)
	c.Assert(actual, Equals, int64(15))

	// Churn clears the operational votes but keeps the effective values and the
	// economic MaxUTXOsToSpend votes.
	keeper.PurgeOperationalNodeMimirs(ctx)
	votes, err := keeper.GetNodeMimirs(ctx, "MaxUTXOsToSpend")
	c.Assert(err, IsNil)
	c.Assert(votes.Mimirs, HasLen, 3)
	for _, kv := range []struct {
		key   string
		value int64
	}{{enabledKey, 1}, {lockKey, 500}} {
		key, value := kv.key, kv.value
		votes, err = keeper.GetNodeMimirs(ctx, key)
		c.Assert(err, IsNil)
		c.Assert(votes.Mimirs, HasLen, 0, Commentf("key=%s", key))
		actual, err = keeper.GetMimir(ctx, key)
		c.Assert(err, IsNil)
		c.Assert(actual, Equals, value, Commentf("key=%s", key))
	}
}
