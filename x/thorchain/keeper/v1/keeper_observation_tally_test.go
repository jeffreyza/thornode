package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

type KeeperObservationTallySuite struct{}

var _ = Suite(&KeeperObservationTallySuite{})

func (s *KeeperObservationTallySuite) TestQueueAndPop(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash().String()

	k.QueueObservationTally(ctx, types.ObservationTallyEntry{DueHeight: 120, ConsensusHeight: 100, Kind: "IN", Chain: common.BTCChain, Ref: []string{txID}})
	k.QueueObservationTally(ctx, types.ObservationTallyEntry{DueHeight: 1020, ConsensusHeight: 1000, Kind: "NETWORKFEE", Chain: common.ETHChain, Ref: []string{"5", "6", "7"}})
	k.QueueObservationTally(ctx, types.ObservationTallyEntry{DueHeight: 110, ConsensusHeight: 90, Kind: "SOLVENCY", Chain: common.LTCChain, Ref: []string{txID}})

	// nothing is due yet
	entries, err := k.PopObservationTallies(ctx, 109)
	c.Assert(err, IsNil)
	c.Check(entries, HasLen, 0)

	// due entries come back in due-height order, and only once
	entries, err = k.PopObservationTallies(ctx, 120)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 2)
	c.Check(entries[0].Kind, Equals, "SOLVENCY")
	c.Check(entries[0].DueHeight, Equals, int64(110))
	c.Check(entries[0].ConsensusHeight, Equals, int64(90))
	c.Check(entries[0].Chain.Equals(common.LTCChain), Equals, true)
	c.Check(entries[0].Ref, DeepEquals, []string{txID})
	c.Check(entries[1].Kind, Equals, "IN")

	entries, err = k.PopObservationTallies(ctx, 120)
	c.Assert(err, IsNil)
	c.Check(entries, HasLen, 0)

	// a height at the larger order of magnitude still sorts after the smaller ones
	entries, err = k.PopObservationTallies(ctx, 1020)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
	c.Check(entries[0].Ref, DeepEquals, []string{"5", "6", "7"})
}

func (s *KeeperObservationTallySuite) TestPopDropsUnreadableEntries(c *C) {
	ctx, k := setupKeeperForTest(c)
	good := types.ObservationTallyEntry{DueHeight: 120, ConsensusHeight: 100, Kind: "IN", Chain: common.BTCChain, Ref: []string{"later"}}
	k.QueueObservationTally(ctx, good)

	// a due height that does not parse sorts ahead of every numeric one, and a value
	// that does not unmarshal sits at a valid due height before the good entry
	badDue := k.GetKey(prefixObservationTallyDue, "!bad/IN/BTC/x")
	badValue := k.observationTallyDueKey(types.ObservationTallyEntry{DueHeight: 110, Kind: "IN", Chain: common.BTCChain, Ref: []string{"earlier"}})
	k.kvStore(ctx).Set(badDue, []byte("x"))
	k.kvStore(ctx).Set(badValue, []byte{0xff})

	entries, err := k.PopObservationTallies(ctx, 120)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
	c.Check(entries[0].Ref, DeepEquals, []string{"later"})
	c.Check(k.has(ctx, badDue), Equals, false)
	c.Check(k.has(ctx, badValue), Equals, false)
	c.Check(k.has(ctx, k.observationTallyDueKey(good)), Equals, false)
}

func (s *KeeperObservationTallySuite) TestTallyBaseAndReset(c *C) {
	ctx, k := setupKeeperForTest(c)
	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeActive)

	// an empty store reads as zero
	missed, total, err := k.GetObservationTally(ctx, na1.NodeAddress, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(0))
	c.Check(total, Equals, int64(0))

	k.AddObservationTally(ctx, common.BTCChain, 10, []cosmos.AccAddress{na1.NodeAddress}, nil)
	k.AddObservationTally(ctx, common.BTCChain, 10, nil, nil)
	k.AddObservationTally(ctx, common.ETHChain, 10, []cosmos.AccAddress{na1.NodeAddress, na2.NodeAddress}, nil)

	missed, total, err = k.GetObservationTally(ctx, na1.NodeAddress, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(10))
	c.Check(total, Equals, int64(20))

	tallies, err := k.GetObservationTallies(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(tallies, HasLen, 2)
	c.Check(tallies[0], DeepEquals, types.ObservationTally{Chain: common.BTCChain, Missed: 10, Total: 20})
	c.Check(tallies[1], DeepEquals, types.ObservationTally{Chain: common.ETHChain, Missed: 10, Total: 10})

	// churn-in: misses go, and the node's total restarts from the chain total
	c.Assert(k.ResetObservationTally(ctx, na1.NodeAddress), IsNil)
	k.AddObservationTally(ctx, common.BTCChain, 10, nil, nil)
	missed, total, err = k.GetObservationTally(ctx, na1.NodeAddress, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(0))
	c.Check(total, Equals, int64(10))

	// the other node is untouched
	missed, total, err = k.GetObservationTally(ctx, na2.NodeAddress, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(10))
	c.Check(total, Equals, int64(10))

	// churn-out: the node's records are removed
	k.ClearObservationTally(ctx, na2.NodeAddress)
	missed, total, err = k.GetObservationTally(ctx, na2.NodeAddress, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(0))
	c.Check(total, Equals, int64(10))
}

func (s *KeeperObservationTallySuite) TestHalve(c *C) {
	ctx, k := setupKeeperForTest(c)
	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeActive)

	k.AddObservationTally(ctx, common.BTCChain, 400, nil, nil)
	c.Assert(k.ResetObservationTally(ctx, na2.NodeAddress), IsNil) // base 400
	k.AddObservationTally(ctx, common.BTCChain, 600, []cosmos.AccAddress{na1.NodeAddress}, nil)
	k.AddObservationTally(ctx, common.BTCChain, 1, []cosmos.AccAddress{na2.NodeAddress}, nil)

	c.Assert(k.HalveObservationTallies(ctx), IsNil)

	missed, total, err := k.GetObservationTally(ctx, na1.NodeAddress, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(300))
	c.Check(total, Equals, int64(500))

	// na2's single unit of misses rounds to zero and is deleted; its base halves
	// with the chain total, so its own total still excludes pre-churn voters
	missed, total, err = k.GetObservationTally(ctx, na2.NodeAddress, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(missed, Equals, int64(0))
	c.Check(total, Equals, int64(300))
}

func (s *KeeperObservationTallySuite) TestGuardSince(c *C) {
	ctx, k := setupKeeperForTest(c)

	since, err := k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))

	k.SetObservationGuardSince(ctx, common.BTCChain, 1234)
	since, err = k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(1234))

	// per chain, and untouched by halving
	since, err = k.GetObservationGuardSince(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))
	c.Assert(k.HalveObservationTallies(ctx), IsNil)
	since, err = k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(1234))

	k.SetObservationGuardSince(ctx, common.BTCChain, 0)
	since, err = k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))
}

func (s *KeeperObservationTallySuite) TestGuardFullAt(c *C) {
	ctx, k := setupKeeperForTest(c)

	fullAt, err := k.GetObservationGuardFullAt(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(0))

	k.SetObservationGuardFullAt(ctx, common.BTCChain, 5678)
	fullAt, err = k.GetObservationGuardFullAt(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(5678))

	// per chain, apart from the run start, and untouched by halving
	fullAt, err = k.GetObservationGuardFullAt(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(0))
	since, err := k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))
	c.Assert(k.HalveObservationTallies(ctx), IsNil)
	fullAt, err = k.GetObservationGuardFullAt(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(5678))

	k.SetObservationGuardFullAt(ctx, common.BTCChain, 0)
	fullAt, err = k.GetObservationGuardFullAt(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(0))
}
