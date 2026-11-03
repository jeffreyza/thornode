package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgWithdrawSuite struct{}

var _ = Suite(&MsgWithdrawSuite{})

func (s *MsgWithdrawSuite) TestMsgWithdrawLiquidity(c *C) {
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"",
	)
	runeAddr := GetRandomRUNEAddress()
	acc1 := GetRandomBech32Addr()
	m := NewMsgWithdrawLiquidity(tx, runeAddr, cosmos.NewUint(10000), common.ETHAsset, common.EmptyAsset, acc1)
	EnsureMsgBasicCorrect(m, c)

	inputs := []struct {
		tx                  common.Tx
		publicAddress       common.Address
		withdrawBasisPoints cosmos.Uint
		asset               common.Asset
		signer              cosmos.AccAddress
	}{
		{
			tx:                  GetRandomTx(),
			publicAddress:       common.NoAddress,
			withdrawBasisPoints: cosmos.NewUint(10000),
			asset:               common.ETHAsset,
			signer:              acc1,
		},
		{
			tx:                  common.Tx{},
			publicAddress:       runeAddr,
			withdrawBasisPoints: cosmos.NewUint(12000),
			asset:               common.ETHAsset,
			signer:              acc1,
		},
		{
			tx:                  GetRandomTx(),
			publicAddress:       runeAddr,
			withdrawBasisPoints: cosmos.ZeroUint(),
			asset:               common.ETHAsset,
			signer:              acc1,
		},
		{
			tx:                  GetRandomTx(),
			publicAddress:       runeAddr,
			withdrawBasisPoints: cosmos.NewUint(10000),
			asset:               common.Asset{},
			signer:              acc1,
		},
		{
			tx:                  GetRandomTx(),
			publicAddress:       runeAddr,
			withdrawBasisPoints: cosmos.NewUint(10000),
			asset:               common.ETHAsset,
			signer:              cosmos.AccAddress{},
		},
		{
			tx:                  GetRandomTx(),
			publicAddress:       runeAddr,
			withdrawBasisPoints: cosmos.NewUint(12000),
			asset:               common.ETHAsset,
			signer:              acc1,
		},
	}
	for _, item := range inputs {
		m = NewMsgWithdrawLiquidity(item.tx, item.publicAddress, item.withdrawBasisPoints, item.asset, common.EmptyAsset, item.signer)
		c.Check(m.ValidateBasic(), NotNil)
	}
}
