package p2p

import (
	"sort"

	. "gopkg.in/check.v1"
)

type LeaderProviderExtraTestSuite struct{}

var _ = Suite(&LeaderProviderExtraTestSuite{})

func (t *LeaderProviderExtraTestSuite) TestLeaderNodeEmptyPeers(c *C) {
	_, err := LeaderNode("msg", 10, []string{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "invalid input for finding the leader")
}

func (t *LeaderProviderExtraTestSuite) TestLeaderNodeEmptyMsgID(c *C) {
	_, err := LeaderNode("", 10, []string{"peer1"})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "invalid input for finding the leader")
}

func (t *LeaderProviderExtraTestSuite) TestLeaderNodeZeroBlockHeight(c *C) {
	_, err := LeaderNode("msg", 0, []string{"peer1"})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "invalid input for finding the leader")
}

func (t *LeaderProviderExtraTestSuite) TestLeaderNodeSinglePeer(c *C) {
	ret, err := LeaderNode("msg", 10, []string{"peer1"})
	c.Assert(err, IsNil)
	c.Assert(ret, Equals, "peer1")
}

func (t *LeaderProviderExtraTestSuite) TestLeaderNodeDeterministic(c *C) {
	peers := []string{"peer1", "peer2", "peer3"}
	ret1, err := LeaderNode("msg1", 10, peers)
	c.Assert(err, IsNil)
	ret2, err := LeaderNode("msg1", 10, peers)
	c.Assert(err, IsNil)
	c.Assert(ret1, Equals, ret2)
}

func (t *LeaderProviderExtraTestSuite) TestLeaderNodesReturnsDeterministicPermutation(c *C) {
	peers := []string{"peer1", "peer2", "peer3", "peer4"}
	leaders1, err := LeaderNodes("msg1", 10, peers)
	c.Assert(err, IsNil)
	leaders2, err := LeaderNodes("msg1", 10, append([]string(nil), peers...))
	c.Assert(err, IsNil)
	c.Assert(leaders1, DeepEquals, leaders2)

	got := append([]string(nil), leaders1...)
	want := append([]string(nil), peers...)
	sort.Strings(got)
	sort.Strings(want)
	c.Assert(got, DeepEquals, want)

	leader, err := LeaderNode("msg1", 10, peers)
	c.Assert(err, IsNil)
	c.Assert(leader, Equals, leaders1[0])
}
