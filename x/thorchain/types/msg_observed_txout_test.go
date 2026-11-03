package types

import (
	"errors"
	"fmt"
	"strings"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"
)

type MsgObservedTxOutSuite struct{}

var _ = Suite(&MsgObservedTxOutSuite{})

func getRandomXMRPubKeyForMsgTest(c *C) common.PubKey {
	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	_, err = pubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	return pubKey
}

func validXMRSpentRefForMsgTest(tx common.ObservedTx) common.OutputRef {
	return common.OutputRef{
		TxHash:      GetRandomTxHash().String(),
		OutputIndex: 0,
		KeyImage:    strings.Repeat("b", 64),
		SpendTxHash: tx.Tx.ID.String(),
	}
}

func (s *MsgObservedTxOutSuite) TestMsgObservedTxOut(c *C) {
	var err error
	pk := GetRandomPubKey()
	tx := common.NewObservedTx(GetRandomTx(), 55, pk, 55)
	tx.Tx.FromAddress, err = pk.GetAddress(tx.Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	acc := GetRandomBech32Addr()

	m := NewMsgObservedTxOut(common.ObservedTxs{tx}, acc)
	EnsureMsgBasicCorrect(m, c)

	m1 := NewMsgObservedTxOut(nil, acc)
	c.Assert(m1.ValidateBasic(), NotNil)
	m2 := NewMsgObservedTxOut(common.ObservedTxs{tx}, cosmos.AccAddress{})
	c.Assert(m2.ValidateBasic(), NotNil)

	// will not accept observations with pre-determined signers. This is
	// important to ensure an observer can fake signers from other node accounts
	// *IMPORTANT* DON'T REMOVE THIS CHECK
	tx.Signers = append(tx.Signers, GetRandomBech32Addr().String())
	m3 := NewMsgObservedTxOut(common.ObservedTxs{tx}, acc)
	c.Assert(m3.ValidateBasic(), NotNil)

	tx4 := common.NewObservedTx(GetRandomTx(), 1, pk, 1)
	m4 := NewMsgObservedTxOut(common.ObservedTxs{tx4}, acc)
	err4 := m4.ValidateBasic()
	c.Assert(err4, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	tx5 := common.NewObservedTx(GetRandomTx(), 1, pk, 1)
	tx5.Tx.FromAddress, err = pk.GetAddress(tx.Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	tx5.OutHashes = []string{
		GetRandomTxHash().String(),
	}
	m5 := NewMsgObservedTxOut(common.ObservedTxs{tx5}, acc)
	err5 := m5.ValidateBasic()
	c.Assert(err5, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	tx6 := common.NewObservedTx(GetRandomTx(), 1, pk, 1)
	tx6.Tx.FromAddress = common.Address("")
	m6 := NewMsgObservedTxOut(common.ObservedTxs{tx6}, acc)
	err6 := m6.ValidateBasic()
	c.Assert(err6, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	tx7 := common.NewObservedTx(GetRandomTx(), 1, "whatever", 1)
	m7 := NewMsgObservedTxOut(common.ObservedTxs{tx7}, acc)
	err7 := m7.ValidateBasic()
	c.Assert(err7, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)
}

func (s *MsgObservedTxOutSuite) TestMsgObservedTxOutRejectsOversizedXMRSpentRefs(c *C) {
	var err error
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
		Memo:        "OUT:spent-refs",
	}, 55, pk, 55)
	for i := 0; i <= common.MaxXMRSpentOutputRefs; i++ {
		tx.SpentOutputRefs = append(tx.SpentOutputRefs, common.OutputRef{
			TxHash:      fmt.Sprintf("tx-%d", i),
			OutputIndex: uint32(i),
			KeyImage:    fmt.Sprintf("ki-%d", i),
			SpendTxHash: tx.Tx.ID.String(),
		})
	}

	m := NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(m.ValidateBasic(), NotNil)
}

func (s *MsgObservedTxOutSuite) TestMsgObservedTxOutRequiresXMRTxKey(c *C) {
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
		Memo:        "OUT:xmr-tx-key",
	}, 55, pk, 55)

	msg := NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*xmr outbound observations must include xmr tx key.*")

	tx.XmrTxKey = "   "
	msg = NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*xmr outbound observations must include xmr tx key.*")

	tx.XmrTxKey = strings.Repeat("a", 63)
	msg = NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*xmr tx key must be 64 hex characters.*")

	tx.XmrTxKey = strings.Repeat("z", 64)
	msg = NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*xmr tx key must be hex.*")

	tx.XmrTxKey = strings.Repeat("a", 64)
	msg = NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), IsNil)
}

func (s *MsgObservedTxOutSuite) TestRejectsXMRVaultAddressCaseVariant(c *C) {
	pk := getRandomXMRPubKeyForMsgTest(c)
	fromAddress, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	toAddress, err := getRandomXMRPubKeyForMsgTest(c).GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		FromAddress: common.Address(strings.ToUpper(fromAddress.String())),
		ToAddress:   toAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.OneUint())),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
	}, 55, pk, 55)
	tx.XmrTxKey = strings.Repeat("a", 64)

	msg := NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)
}

func (s *MsgObservedTxOutSuite) TestMsgObservedTxOutAllowsOptionalXMRSpentRefs(c *C) {
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
		Memo:        "OUT:xmr-spent-refs",
	}, 55, pk, 55)
	tx.XmrTxKey = strings.Repeat("a", 64)

	msg := NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), IsNil)

	tx.SpentOutputRefs = []common.OutputRef{validXMRSpentRefForMsgTest(tx)}
	msg = NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), IsNil)
}
