package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MsgSecuredAssetSuite struct{}

var _ = Suite(&MsgSecuredAssetSuite{})

func (MsgSecuredAssetSuite) TestDeposit(c *C) {
	asset := common.ETHAsset
	amt := cosmos.NewUint(100)
	signer := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	m := NewMsgSecuredAssetDeposit(asset, amt, signer, signer, dummyTx)
	EnsureMsgBasicCorrect(m, c)

	m = NewMsgSecuredAssetDeposit(common.EmptyAsset, amt, signer, signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)

	m = NewMsgSecuredAssetDeposit(common.RuneAsset(), amt, signer, signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)

	m = NewMsgSecuredAssetDeposit(asset, cosmos.ZeroUint(), signer, signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)
}

func (MsgSecuredAssetSuite) TestWithdraw(c *C) {
	asset := common.ETHAsset.GetSecuredAsset()
	amt := cosmos.NewUint(100)
	ethAddr := GetRandomETHAddress()
	signer := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	m := NewMsgSecuredAssetWithdraw(asset, amt, ethAddr, signer, dummyTx)
	EnsureMsgBasicCorrect(m, c)

	m = NewMsgSecuredAssetWithdraw(common.EmptyAsset, amt, ethAddr, signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)

	m = NewMsgSecuredAssetWithdraw(common.RuneAsset(), amt, ethAddr, signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)

	m = NewMsgSecuredAssetWithdraw(asset, cosmos.ZeroUint(), ethAddr, signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)

	m = NewMsgSecuredAssetWithdraw(asset, cosmos.ZeroUint(), GetRandomTHORAddress(), signer, dummyTx)
	c.Check(m.ValidateBasic(), NotNil)
}
