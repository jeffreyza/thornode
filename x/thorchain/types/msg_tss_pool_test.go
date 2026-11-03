package types

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type MsgTssPoolSuite struct{}

var _ = Suite(&MsgTssPoolSuite{})

func (s *MsgTssPoolSuite) TestMsgTssPool(c *C) {
	pk := GetRandomPubKey()
	pks := []string{
		GetRandomPubKey().String(), GetRandomPubKey().String(), GetRandomPubKey().String(),
	}
	addr, err := common.PubKey(pks[0]).GetThorAddress()
	c.Assert(err, IsNil)
	keygenTime := int64(1024)
	msg, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, []string{common.RuneAsset().Chain.String()}, addr, keygenTime)
	c.Assert(err, IsNil)
	c.Assert(msg.ValidateBasic(), IsNil)
	EnsureMsgBasicCorrect(msg, c)

	chains := []string{common.RuneAsset().Chain.String()}
	m, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, nil, keygenTime)
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)
	c.Assert(m.ValidateBasic(), NotNil)
	m, err = NewMsgTssPool(nil, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, addr, keygenTime)
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)
	c.Assert(m.ValidateBasic(), NotNil)
	m, err = NewMsgTssPool(pks, "", nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, addr, keygenTime)
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)
	c.Assert(m.ValidateBasic(), NotNil)
	m, err = NewMsgTssPool(pks, "bogusPubkey", nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, addr, keygenTime)
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)
	c.Assert(m.ValidateBasic(), NotNil)

	// fails on empty chain list
	msg, err = NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, []string{}, addr, keygenTime)
	c.Assert(err, IsNil)
	c.Check(msg.ValidateBasic(), NotNil)
	// fails on duplicates in chain list
	msg, err = NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, []string{common.RuneAsset().Chain.String(), common.RuneAsset().Chain.String()}, addr, keygenTime)
	c.Assert(err, IsNil)
	c.Check(msg.ValidateBasic(), NotNil)

	msg1, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, addr, keygenTime)
	c.Assert(err, IsNil)
	msg1.ID = ""
	err1 := msg1.ValidateBasic()
	c.Assert(err1, NotNil)
	c.Check(errors.Is(err1, se.ErrUnknownRequest), Equals, true)

	msg2, err := NewMsgTssPool(append(pks, ""), pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, addr, keygenTime)
	c.Assert(err, IsNil)
	err2 := msg2.ValidateBasic()
	c.Assert(err2, NotNil)
	c.Check(errors.Is(err2, se.ErrUnknownRequest), Equals, true)

	var allPks []string
	for i := 0; i < 110; i++ {
		allPks = append(allPks, GetRandomPubKey().String())
	}
	msg3, err := NewMsgTssPool(allPks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nil, chains, addr, keygenTime)
	c.Assert(err, IsNil)
	err3 := msg3.ValidateBasic()
	c.Assert(err3, NotNil)
	c.Check(errors.Is(err3, se.ErrUnknownRequest), Equals, true)

	xmrMonovaultPk := GetRandomPubKey()
	xmrFrostPk := GetRandomEd25519PubKey()
	xmrMember := []string{GetRandomPubKey().String()}
	xmrSigner, err := common.PubKey(xmrMember[0]).GetThorAddress()
	c.Assert(err, IsNil)
	xmrMsg, err := NewMsgTssPoolV3(
		xmrMember,
		xmrMonovaultPk,
		nil,
		nil,
		KeygenType_XMRMonovaultKeygen,
		1,
		nil,
		common.Chains{common.RuneAsset().Chain, common.XMRChain}.Strings(),
		xmrSigner,
		keygenTime,
		common.EmptyPubKey,
		nil,
		nil,
		xmrFrostPk,
		1024,
	)
	c.Assert(err, IsNil)
	c.Assert(xmrMsg.ValidateBasic(), IsNil)
	xmrMsg.ObservedMoneroHeight = 0
	c.Assert(xmrMsg.ValidateBasic(), NotNil)
}

func (s *MsgTssPoolSuite) TestXMRMonovaultKeygenValidateBasicErrors(c *C) {
	member := []string{GetRandomPubKey().String()}
	signer, err := common.PubKey(member[0]).GetThorAddress()
	c.Assert(err, IsNil)
	chains := common.Chains{common.RuneAsset().Chain, common.XMRChain}.Strings()
	newMsg := func(frostPk common.PubKey, moneroHeight int64, blame []Blame) *MsgTssPool {
		msg, msgErr := NewMsgTssPoolV3(member, GetRandomPubKey(), nil, nil, KeygenType_XMRMonovaultKeygen, 1, blame, chains, signer, 1024, common.EmptyPubKey, nil, nil, frostPk, moneroHeight)
		c.Assert(msgErr, IsNil)
		return msg
	}

	c.Assert(newMsg(GetRandomEd25519PubKey(), 1, nil).ValidateBasic(), IsNil)

	err = newMsg(common.EmptyPubKey, 1, nil).ValidateBasic()
	c.Assert(err, ErrorMatches, ".*xmr frost pool pubkey cannot be empty.*")
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)

	// a secp256k1 key cannot derive a Monero address
	c.Assert(newMsg(GetRandomPubKey(), 1, nil).ValidateBasic(), ErrorMatches, ".*invalid xmr frost pool pubkey.*")

	c.Assert(newMsg(GetRandomEd25519PubKey(), 0, nil).ValidateBasic(), ErrorMatches, ".*xmr observed monero height must be greater than zero.*")
	c.Assert(newMsg(GetRandomEd25519PubKey(), -1, nil).ValidateBasic(), ErrorMatches, ".*xmr observed monero height must be greater than zero.*")

	// failed keygen reports carry blame instead of a FROST key and height
	blame := []Blame{{FailReason: "keygen timeout", BlameNodes: []Node{{Pubkey: member[0]}}}}
	c.Assert(newMsg(common.EmptyPubKey, 0, blame).ValidateBasic(), IsNil)
}

func (s *MsgTssPoolSuite) TestXMRMsgTssPoolIDIncludesFrostKeyAndMoneroHeight(c *C) {
	poolPk := GetRandomPubKey()
	frostPk := GetRandomEd25519PubKey()
	otherFrostPk := GetRandomEd25519PubKey()
	member := []string{GetRandomPubKey().String()}
	addr, err := common.PubKey(member[0]).GetThorAddress()
	c.Assert(err, IsNil)

	base, err := NewMsgTssPoolV3(
		member,
		poolPk,
		nil,
		nil,
		KeygenType_XMRMonovaultKeygen,
		1,
		nil,
		common.Chains{common.RuneAsset().Chain, common.XMRChain}.Strings(),
		addr,
		1024,
		common.EmptyPubKey,
		nil,
		nil,
		frostPk,
		100,
	)
	c.Assert(err, IsNil)

	changedFrost, err := NewMsgTssPoolV3(
		member,
		poolPk,
		nil,
		nil,
		KeygenType_XMRMonovaultKeygen,
		1,
		nil,
		common.Chains{common.RuneAsset().Chain, common.XMRChain}.Strings(),
		addr,
		1024,
		common.EmptyPubKey,
		nil,
		nil,
		otherFrostPk,
		100,
	)
	c.Assert(err, IsNil)
	c.Check(changedFrost.ID == base.ID, Equals, false)

	changedHeight, err := NewMsgTssPoolV3(
		member,
		poolPk,
		nil,
		nil,
		KeygenType_XMRMonovaultKeygen,
		1,
		nil,
		common.Chains{common.RuneAsset().Chain, common.XMRChain}.Strings(),
		addr,
		1024,
		common.EmptyPubKey,
		nil,
		nil,
		frostPk,
		101,
	)
	c.Assert(err, IsNil)
	c.Check(changedHeight.ID == base.ID, Equals, false)
}

func (s *MsgTssPoolSuite) TestMsgTssPoolIDDistinguishesSuccessFromZeroNodeFailure(c *C) {
	poolPk := GetRandomPubKey()
	members := []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
	signer, err := common.PubKey(members[0]).GetThorAddress()
	c.Assert(err, IsNil)

	success, err := NewMsgTssPool(
		members,
		poolPk,
		nil,
		nil,
		KeygenType_AsgardKeygen,
		1,
		nil,
		[]string{common.RuneAsset().Chain.String()},
		signer,
		1024,
	)
	c.Assert(err, IsNil)
	c.Assert(success.IsSuccess(), Equals, true)

	failure, err := NewMsgTssPool(
		members,
		poolPk,
		nil,
		nil,
		KeygenType_AsgardKeygen,
		1,
		[]Blame{{FailReason: "failure without an attributable node"}},
		[]string{common.RuneAsset().Chain.String()},
		signer,
		1024,
	)
	c.Assert(err, IsNil)
	c.Assert(failure.IsSuccess(), Equals, false)
	c.Assert(failure.Blame[0].BlameNodes, HasLen, 0)
	c.Assert(failure.ID, Not(Equals), success.ID)
}

func (s *MsgTssPoolSuite) TestMsgTssPoolFrostKeysharesBackupValidation(c *C) {
	poolPk := GetRandomPubKey()
	frostPk := GetRandomEd25519PubKey()
	member := []string{GetRandomPubKey().String()}
	addr, err := common.PubKey(member[0]).GetThorAddress()
	c.Assert(err, IsNil)

	validBackup := make([]byte, 357)
	for i := range validBackup {
		validBackup[i] = byte(i)
	}

	msg, err := NewMsgTssPoolV3(
		member,
		poolPk,
		nil,
		nil,
		KeygenType_XMRMonovaultKeygen,
		1,
		nil,
		common.Chains{common.RuneAsset().Chain, common.XMRChain}.Strings(),
		addr,
		1024,
		common.EmptyPubKey,
		nil,
		validBackup,
		frostPk,
		1024,
	)
	c.Assert(err, IsNil)
	c.Assert(msg.ValidateBasic(), IsNil)

	msg.KeysharesBackupFrost = []byte("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	c.Assert(msg.ValidateBasic(), NotNil)

	msg.KeysharesBackupFrost = []byte{1, 2, 3}
	c.Assert(msg.ValidateBasic(), NotNil)

	msg.KeysharesBackupFrost = make([]byte, 2048)
	c.Assert(msg.ValidateBasic(), NotNil)
}

func (s *MsgTssPoolSuite) TestMsgTssPoolBlameNodesValidation(c *C) {
	pk := GetRandomPubKey()
	pks := []string{
		GetRandomPubKey().String(), GetRandomPubKey().String(), GetRandomPubKey().String(),
	}
	addr, err := common.PubKey(pks[0]).GetThorAddress()
	c.Assert(err, IsNil)
	keygenTime := int64(1024)

	// Valid: blame nodes are keygen participants with no duplicates
	blame := []Blame{
		{
			FailReason: "test",
			BlameNodes: []Node{
				{Pubkey: pks[0]},
				{Pubkey: pks[1]},
			},
		},
	}
	msg, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, blame, nil, addr, keygenTime)
	c.Assert(err, IsNil)
	// This should fail due to missing chains, but the blame check should pass
	err = msg.ValidateBasic()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Not(Equals), "blame node not in keygen participants: unknown request")
	c.Assert(err.Error(), Not(Equals), "duplicate blame node: unknown request")

	// Invalid: blame node not in keygen participants
	nonParticipantBlame := []Blame{
		{
			FailReason: "test",
			BlameNodes: []Node{
				{Pubkey: pks[0]},
				{Pubkey: GetRandomPubKey().String()}, // not a keygen participant
			},
		},
	}
	msg2, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, nonParticipantBlame, nil, addr, keygenTime)
	c.Assert(err, IsNil)
	err = msg2.ValidateBasic()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "blame node not in keygen participants: unknown request")
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)

	// Invalid: duplicate blame nodes within same Blame
	duplicateBlame := []Blame{
		{
			FailReason: "test",
			BlameNodes: []Node{
				{Pubkey: pks[0]},
				{Pubkey: pks[0]}, // duplicate
			},
		},
	}
	msg3, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, duplicateBlame, nil, addr, keygenTime)
	c.Assert(err, IsNil)
	err = msg3.ValidateBasic()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "duplicate blame node: unknown request")
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)

	// Invalid: duplicate blame nodes across multiple Blame entries
	duplicateAcrossBlame := []Blame{
		{
			FailReason: "test1",
			BlameNodes: []Node{
				{Pubkey: pks[0]},
			},
		},
		{
			FailReason: "test2",
			BlameNodes: []Node{
				{Pubkey: pks[0]}, // duplicate from first Blame
			},
		},
	}
	msg4, err := NewMsgTssPool(pks, pk, nil, nil, KeygenType_AsgardKeygen, 1, duplicateAcrossBlame, nil, addr, keygenTime)
	c.Assert(err, IsNil)
	err = msg4.ValidateBasic()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "duplicate blame node: unknown request")
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)
}
