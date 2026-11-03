package tss

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/common"
)

type MsgIDSuite struct{}

var _ = Suite(&MsgIDSuite{})

// TestKeysignRequestMsgIDIncludesPoolPubKeyButNotSigners locks the canonical
// keysign msgID data to the vault key while keeping the active signer set out.
func (s *MsgIDSuite) TestKeysignRequestMsgIDIncludesPoolPubKeyButNotSigners(c *C) {
	server := &TssServer{}
	messages := []string{"msg-b", "msg-a"}
	req := keysign.NewRequest("pool-a", common.SigningAlgoSecp256k1, messages, 120, []string{"signer-a", "signer-b"}, "1.0.0")

	baseID, err := server.requestToMsgId(req)
	c.Assert(err, IsNil)

	reorderedMessages := req
	reorderedMessages.Messages = []string{"msg-a", "msg-b"}
	reorderedMessageID, err := server.requestToMsgId(reorderedMessages)
	c.Assert(err, IsNil)
	c.Assert(reorderedMessageID, Equals, baseID)

	changedSigners := req
	changedSigners.SignerPubKeys = []string{"signer-c", "signer-d"}
	changedSignerID, err := server.requestToMsgId(changedSigners)
	c.Assert(err, IsNil)
	c.Assert(changedSignerID, Equals, baseID)

	changedPool := req
	changedPool.PoolPubKey = "pool-b"
	changedPoolID, err := server.requestToMsgId(changedPool)
	c.Assert(err, IsNil)
	c.Assert(changedPoolID == baseID, Equals, false)
}
