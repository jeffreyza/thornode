package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/tssrounds"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type TypeTssKeysignFailTestSuite struct{}

var _ = Suite(&TypeTssKeysignFailTestSuite{})

func (s *TypeTssKeysignFailTestSuite) TestVoter(c *C) {
	nodes := []Node{
		{Pubkey: GetRandomPubKey().String()},
		{Pubkey: GetRandomPubKey().String()},
		{Pubkey: GetRandomPubKey().String()},
	}
	b := Blame{BlameNodes: nodes, FailReason: "fail to keysign", Round: tssrounds.KEYSIGN1aUnicast}
	m, err := NewMsgTssKeysignFail(1, b, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, GetRandomBech32Addr(), GetRandomPubKey())
	c.Assert(err, IsNil)
	tss := NewTssKeysignFailVoter(m.ID)
	c.Check(tss.String(), Equals, tss.ID)
	c.Check(tss.State, Equals, TssKeysignFailVoterState_PendingVoter)
	c.Check(int32(TssKeysignFailVoterState_LegacyVoter), Equals, int32(0))
	c.Check(int32(TssKeysignFailVoterState_PendingVoter), Equals, int32(1))
	c.Check(int32(TssKeysignFailVoterState_CompletedVoter), Equals, int32(2))

	addr := GetRandomBech32Addr()
	c.Check(tss.HasSigned(addr), Equals, false)
	tss.Sign(addr)
	c.Check(tss.Signers, HasLen, 1)
	c.Check(tss.HasSigned(addr), Equals, true)
	tss.Sign(addr) // ensure signing twice doesn't duplicate
	c.Check(tss.Signers, HasLen, 1)

	c.Check(tss.HasConsensus(nil), Equals, false)
	nas := NodeAccounts{
		NodeAccount{NodeAddress: addr, Status: NodeStatus_Active},
	}
	c.Check(tss.HasConsensus(nas), Equals, true)
}
