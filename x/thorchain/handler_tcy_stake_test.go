package thorchain

import (
	math "cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type HandlerTCYStake struct{}

var _ = Suite(&HandlerTCYStake{})

func (s *HandlerTCYStake) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)

	// happy path
	k.SetMimir(ctx, "TCYStakingHalt", 0)
	fromAddr := GetRandomRUNEAddress()
	toAddr := GetRandomRUNEAddress()
	accSignerAddr, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)
	coin := common.NewCoin(common.TCY, cosmos.NewUint(100))

	tx := common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	msg := NewMsgTCYStake(tx, accSignerAddr)
	handler := NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// invalid msgs
	// invalid coin
	coin = common.NewCoin(common.ETHAsset, cosmos.NewUint(100))
	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	msg = NewMsgTCYStake(tx, accSignerAddr)
	handler = NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// multiple coins send
	coin = common.NewCoin(common.TCY, cosmos.NewUint(100))
	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin, coin),
		common.Gas{},
		"",
	)

	msg = NewMsgTCYStake(tx, accSignerAddr)
	handler = NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// coin is not tcy
	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(100))
	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	msg = NewMsgTCYStake(tx, accSignerAddr)
	handler = NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// coin amount is zero
	coin = common.NewCoin(common.TCY, cosmos.ZeroUint())
	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	msg = NewMsgTCYStake(tx, cosmos.AccAddress{})
	handler = NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// signer is empty
	coin = common.NewCoin(common.TCY, cosmos.NewUint(100))
	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	msg = NewMsgTCYStake(tx, cosmos.AccAddress{})
	handler = NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// staking halt
	k.SetMimir(ctx, "TCYStakingHalt", 1)
	coin = common.NewCoin(common.TCY, cosmos.NewUint(100))

	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	msg = NewMsgTCYStake(tx, accSignerAddr)
	handler = NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "tcy staking is halted")

	// empty msg
	k.SetMimir(ctx, "TCYStakingHalt", 0)
	msg = &MsgTCYStake{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerTCYUnstake) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)

	// first time stake
	fromAddr := GetRandomRUNEAddress()
	toAddr := GetRandomRUNEAddress()
	accSignerAddr, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)
	amount := cosmos.NewUint(100)
	coin := common.NewCoin(common.TCY, amount)

	tx := common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.NewCoins(coin),
		common.Gas{},
		"",
	)

	// check before state
	c.Assert(k.TCYStakerExists(ctx, fromAddr), Equals, false)

	msg := NewMsgTCYStake(tx, accSignerAddr)
	handler := NewTCYStakeHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// check after state
	c.Assert(k.TCYStakerExists(ctx, fromAddr), Equals, true)

	staker, err := k.GetTCYStaker(ctx, fromAddr)
	c.Assert(err, IsNil)
	c.Assert(staker.Address.Equals(fromAddr), Equals, true)
	c.Assert(staker.Amount.Equal(amount), Equals, true)

	// second time stake
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// the amount should be twice since the msg was send twice
	staker, err = k.GetTCYStaker(ctx, fromAddr)
	c.Assert(err, IsNil)
	c.Assert(staker.Address.Equals(fromAddr), Equals, true)
	c.Assert(staker.Amount.Equal(amount.Mul(math.NewUint(2))), Equals, true)
}

func (s *HandlerTCYStake) TestValidateRejectsSmartContractAddress(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "TCYStakingHalt", 0)

	scAddr, scAcc := tcySmartContractStaker(c, ctx, k, cosmos.ZeroUint())

	tx := common.NewTx(
		common.BlankTxID,
		scAddr,
		GetRandomRUNEAddress(),
		common.NewCoins(common.NewCoin(common.TCY, cosmos.NewUint(100))),
		common.Gas{},
		"",
	)
	msg := NewMsgTCYStake(tx, scAcc)
	handler := NewTCYStakeHandler(NewDummyMgrWithKeeper(k))

	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "cannot stake from TCY smart contract address: "+scAddr.String())

	// the keeper refuses the credit too, rather than reporting a success it dropped
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, NotNil)
	staker, err := k.GetTCYStaker(ctx, scAddr)
	c.Assert(err, IsNil)
	c.Assert(staker.Amount.IsZero(), Equals, true)
}
