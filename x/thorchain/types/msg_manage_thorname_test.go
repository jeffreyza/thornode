package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MsgManageTHORNameSuite struct{}

var _ = Suite(&MsgManageTHORNameSuite{})

func (MsgManageTHORNameSuite) TestMsgManageTHORNameSuite(c *C) {
	owner := GetRandomBech32Addr()
	signer := GetRandomBech32Addr()
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))
	msg := NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, signer, 0)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, signer.String())

	// unhappy paths
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, cosmos.AccAddress{}, 0)
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgManageTHORName("myname", common.EmptyChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, signer, 0)
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgManageTHORName("myname", common.ETHChain, common.NoAddress, coin, 0, common.ETHAsset, owner, signer, 0)
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomBTCAddress(), coin, 0, common.ETHAsset, owner, signer, 0)
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)), 0, common.ETHAsset, owner, signer, 0)
	c.Assert(msg.ValidateBasic(), NotNil)

	// test custom multiplier validation
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, signer, 500)
	c.Assert(msg.ValidateBasic(), IsNil)
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, signer, -1)
	c.Assert(msg.ValidateBasic(), IsNil) // -1 is the sentinel for "not provided"
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, signer, -2)
	c.Assert(msg.ValidateBasic(), NotNil)
	msg = NewMsgManageTHORName("myname", common.ETHChain, GetRandomETHAddress(), coin, 0, common.ETHAsset, owner, signer, 10001)
	c.Assert(msg.ValidateBasic(), NotNil)
}
