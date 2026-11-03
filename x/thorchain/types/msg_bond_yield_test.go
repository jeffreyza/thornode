package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MsgBondYieldSuite struct{}

var _ = Suite(&MsgBondYieldSuite{})

func (s *MsgBondYieldSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *MsgBondYieldSuite) TestValidateBasicPayoutTarget(c *C) {
	nodeAddr := GetRandomBech32Addr()
	providerAddr := GetRandomBech32Addr()
	txin := common.Tx{
		ID:          GetRandomTxHash(),
		FromAddress: common.Address(providerAddr.String()),
	}

	c.Assert(NewMsgBondYield(txin, nodeAddr, 1000, "", providerAddr).ValidateBasic(), IsNil)
	c.Assert(NewMsgBondYield(txin, nodeAddr, 1000, "yieldname", providerAddr).ValidateBasic(), IsNil)
	c.Assert(NewMsgBondYield(txin, nodeAddr, 1000, providerAddr.String(), providerAddr).ValidateBasic(), IsNil)

	err := NewMsgBondYield(txin, nodeAddr, 1000, GetRandomETHAddress().String(), providerAddr).ValidateBasic()
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*payout target address must be a THORChain address.*")

	// bps=0 clears the target in handler storage and does not stage it.
	c.Assert(NewMsgBondYield(txin, nodeAddr, 0, GetRandomETHAddress().String(), providerAddr).ValidateBasic(), IsNil)

	txinWithCoins := txin
	txinWithCoins.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)))
	c.Assert(NewMsgBondYield(txinWithCoins, nodeAddr, 1000, "", providerAddr).ValidateBasic(), NotNil)
}
