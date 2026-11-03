package types

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type MsgObservedTxQuorumSuite struct{}

var _ = Suite(&MsgObservedTxQuorumSuite{})

func (s *MsgObservedTxQuorumSuite) TestMsgObservedTxQuorumRequiresXMROutboundTxKey(c *C) {
	pk := getRandomXMRPubKeyForMsgTest(c)
	fromAddress, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	toAddress, err := getRandomXMRPubKeyForMsgTest(c).GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		FromAddress: fromAddress,
		ToAddress:   toAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		Memo:        "OUT:xmr-quorum-metadata",
	}, 55, pk, 55)

	msg := NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*xmr outbound observations must include xmr tx key.*")

	tx.XmrTxKey = strings.Repeat("a", 64)
	msg = NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), IsNil)

	tx.SpentOutputRefs = []common.OutputRef{validXMRSpentRefForMsgTest(tx)}
	msg = NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), IsNil)
}

func (s *MsgObservedTxQuorumSuite) TestRejectsXMRVaultAddressCaseVariants(c *C) {
	pk := getRandomXMRPubKeyForMsgTest(c)
	vaultAddress, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	otherAddress, err := getRandomXMRPubKeyForMsgTest(c).GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	outbound := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		FromAddress: common.Address(strings.ToUpper(vaultAddress.String())),
		ToAddress:   otherAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.OneUint())),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
	}, 55, pk, 55)
	outbound.XmrTxKey = strings.Repeat("a", 64)
	msg := NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: outbound}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)

	inbound := outbound
	inbound.Tx.FromAddress = otherAddress
	inbound.Tx.ToAddress = common.Address(strings.ToUpper(vaultAddress.String()))
	inbound.XmrTxKey = ""
	msg = NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: inbound, Inbound: true}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)
}

func (s *MsgObservedTxQuorumSuite) TestMsgObservedTxQuorumRejectsInboundXMRMetadata(c *C) {
	pk := getRandomXMRPubKeyForMsgTest(c)
	toAddress, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		FromAddress: toAddress,
		ToAddress:   toAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		Memo:        "=:THOR.RUNE:" + GetRandomTHORAddress().String(),
	}, 55, pk, 55)
	tx.XmrMinedHeight = 55
	tx.XmrTxKey = strings.Repeat("a", 64)

	msg := NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: tx, Inbound: true}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*xmr tx key is only valid for outbound observed transactions.*")

	tx.XmrTxKey = ""
	tx.SpentOutputRefs = []common.OutputRef{validXMRSpentRefForMsgTest(tx)}
	msg = NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: tx, Inbound: true}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*spent output refs are only valid for outbound observed transactions.*")
}
