package thorchain

import (
	"cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type HandlerTCYUnstake struct{}

var _ = Suite(&HandlerTCYUnstake{})

func (s *HandlerTCYUnstake) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)

	// happy path
	k.SetMimir(ctx, "TCYUnstakingHalt", 0)
	fromAddr := GetRandomRUNEAddress()
	toAddr := GetRandomRUNEAddress()
	accSignerAddr, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)
	bps := cosmos.NewUint(100_00)

	tx := common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.Coins{},
		common.Gas{},
		"",
	)

	msg := NewMsgTCYUnstake(tx, bps, accSignerAddr)
	handler := NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// invalid msgs
	// empty signer
	msg = NewMsgTCYUnstake(tx, bps, cosmos.AccAddress{})
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// empty bps
	msg = NewMsgTCYUnstake(tx, cosmos.ZeroUint(), cosmos.AccAddress{})
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// bps more than 100%
	msg = NewMsgTCYUnstake(tx, cosmos.NewUint(200_00), cosmos.AccAddress{})
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// tx from address not rune address
	tx = common.NewTx(
		common.BlankTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{},
		common.Gas{},
		"",
	)
	msg = NewMsgTCYUnstake(tx, bps, accSignerAddr)
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// happy path
	k.SetMimir(ctx, "TCYUnstakingHalt", 1)
	bps = cosmos.NewUint(100_00)

	tx = common.NewTx(
		common.BlankTxID,
		fromAddr,
		toAddr,
		common.Coins{},
		common.Gas{},
		"",
	)

	msg = NewMsgTCYUnstake(tx, bps, accSignerAddr)
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "tcy unstaking is halt")

	// empty msg
	k.SetMimir(ctx, "TCYUnstakingHalt", 0)
	msg = &MsgTCYUnstake{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerTCYStake) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomRUNEAddress()
	accAddr1, err := addr1.AccAddress()
	c.Assert(err, IsNil)
	addr1Amount := cosmos.NewUint(100)
	addr2 := GetRandomRUNEAddress()
	accAddr2, err := addr2.AccAddress()
	c.Assert(err, IsNil)
	addr2Amount := cosmos.NewUint(200)

	tx := common.NewTx(
		common.BlankTxID,
		common.NoAddress,
		GetRandomRUNEAddress(),
		common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(1))),
		common.Gas{},
		"",
	)

	// set stakers and staking module
	err = k.SetTCYStaker(ctx, TCYStaker{
		Address: addr1,
		Amount:  addr1Amount,
	})
	c.Assert(err, IsNil)
	err = k.SetTCYStaker(ctx, TCYStaker{
		Address: addr2,
		Amount:  addr2Amount,
	})
	c.Assert(err, IsNil)

	stakeAmount := addr1Amount.Add(addr2Amount)
	coin := common.NewCoin(common.TCY, stakeAmount)
	err = k.MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)
	err = k.SendFromModuleToModule(ctx, ModuleName, TCYStakeName, common.NewCoins(coin))
	c.Assert(err, IsNil)
	tcyStakeAmount := k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native())
	c.Assert(tcyStakeAmount.Equal(stakeAmount), Equals, true)

	// check state before
	c.Assert(k.TCYStakerExists(ctx, addr1), Equals, true)
	c.Assert(k.TCYStakerExists(ctx, addr2), Equals, true)

	addr1Coin := k.GetBalanceOf(ctx, accAddr1, common.TCY)
	c.Assert(addr1Coin.IsZero(), Equals, true)
	addr2Coin := k.GetBalanceOf(ctx, accAddr2, common.TCY)
	c.Assert(addr2Coin.IsZero(), Equals, true)

	// unstake 100% from addr1
	tx.FromAddress = addr1
	bps := cosmos.NewUint(100_00)
	msg := NewMsgTCYUnstake(tx, bps, accAddr1)
	handler := NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Assert(k.TCYStakerExists(ctx, addr1), Equals, false)
	c.Assert(k.TCYStakerExists(ctx, addr2), Equals, true)

	addr1Coin = k.GetBalanceOf(ctx, accAddr1, common.TCY)
	c.Assert(addr1Coin.Amount.Equal(math.NewInt(100)), Equals, true)
	addr2Coin = k.GetBalanceOf(ctx, accAddr2, common.TCY)
	c.Assert(addr2Coin.IsZero(), Equals, true)
	tcyStakeAmount = k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native())
	c.Assert(tcyStakeAmount.Equal(addr2Amount), Equals, true)

	// unstake 25% from addr2
	tx.FromAddress = addr2
	bps = cosmos.NewUint(25_00)
	msg = NewMsgTCYUnstake(tx, bps, accAddr2)
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Assert(k.TCYStakerExists(ctx, addr1), Equals, false)
	c.Assert(k.TCYStakerExists(ctx, addr2), Equals, true)

	staker, err := k.GetTCYStaker(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(staker.Address.Equals(addr2), Equals, true)
	c.Assert(staker.Amount.Equal(math.NewUint(150)), Equals, true)

	addr1Coin = k.GetBalanceOf(ctx, accAddr1, common.TCY)
	c.Assert(addr1Coin.Amount.Equal(math.NewInt(100)), Equals, true)
	addr2Coin = k.GetBalanceOf(ctx, accAddr2, common.TCY)
	c.Assert(addr2Coin.Amount.Equal(math.NewInt(50)), Equals, true)
	tcyStakeAmount = k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native())
	c.Assert(tcyStakeAmount.Equal(math.NewUint(150)), Equals, true)

	// unstake 100% from addr2
	tx.FromAddress = addr2
	bps = cosmos.NewUint(100_00)
	msg = NewMsgTCYUnstake(tx, bps, accAddr2)
	handler = NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Assert(k.TCYStakerExists(ctx, addr1), Equals, false)
	c.Assert(k.TCYStakerExists(ctx, addr2), Equals, false)

	addr1Coin = k.GetBalanceOf(ctx, accAddr1, common.TCY)
	c.Assert(addr1Coin.Amount.Equal(math.NewInt(100)), Equals, true)
	addr2Coin = k.GetBalanceOf(ctx, accAddr2, common.TCY)
	c.Assert(addr2Coin.Amount.Equal(math.NewInt(200)), Equals, true)
	tcyStakeAmount = k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native())
	c.Assert(tcyStakeAmount.IsZero(), Equals, true)
}

// tcySmartContractStaker returns the first configured TCY smart contract address,
// funded with amount of TCY in its own account - which is where those addresses hold
// their TCY, and what GetTCYStaker reports as their stake.
func tcySmartContractStaker(c *C, ctx cosmos.Context, k keeper.Keeper, amount cosmos.Uint) (common.Address, cosmos.AccAddress) {
	addrs, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(len(addrs) > 0, Equals, true)
	addr := addrs[0]

	acc, err := addr.AccAddress()
	c.Assert(err, IsNil)
	if !amount.IsZero() {
		coin := common.NewCoin(common.TCY, amount)
		c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
		c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, acc, common.NewCoins(coin)), IsNil)
	}
	return addr, acc
}

func (s *HandlerTCYUnstake) TestValidateRejectsSmartContractAddress(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "TCYUnstakingHalt", 0)

	scAddr, scAcc := tcySmartContractStaker(c, ctx, k, cosmos.NewUint(500))

	tx := common.NewTx(
		common.BlankTxID,
		scAddr,
		GetRandomRUNEAddress(),
		common.Coins{},
		common.Gas{},
		"",
	)
	msg := NewMsgTCYUnstake(tx, cosmos.NewUint(100_00), scAcc)
	handler := NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))

	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "cannot unstake from TCY smart contract address: "+scAddr.String())
}

// A smart contract unstake would have paid a share of the contract's own balance out of
// the TCYStake module - which only holds the regular stakers' deposits - and, since the
// keeper stores no record for those addresses, nothing would have been decremented: the
// payout grew the balance GetTCYStaker reports, so the drain could be repeated.
func (s *HandlerTCYUnstake) TestSmartContractUnstakeCannotDrainStakeModule(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "TCYUnstakingHalt", 0)

	scAddr, scAcc := tcySmartContractStaker(c, ctx, k, cosmos.NewUint(500))

	// a regular staker's TCY, held by the module on their behalf
	staker := GetRandomRUNEAddress()
	stakerAmount := cosmos.NewUint(1000)
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(staker, stakerAmount)), IsNil)
	stakeCoin := common.NewCoin(common.TCY, stakerAmount)
	c.Assert(k.MintToModule(ctx, ModuleName, stakeCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, TCYStakeName, common.NewCoins(stakeCoin)), IsNil)

	tx := common.NewTx(
		common.BlankTxID,
		scAddr,
		GetRandomRUNEAddress(),
		common.Coins{},
		common.Gas{},
		"",
	)
	msg := NewMsgTCYUnstake(tx, cosmos.NewUint(100_00), scAcc)
	handler := NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))

	for i := 0; i < 3; i++ {
		_, err := handler.Run(ctx, msg)
		c.Assert(err, NotNil)
		c.Assert(k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native()).Equal(stakerAmount), Equals, true)
		c.Assert(k.GetBalanceOf(ctx, scAcc, common.TCY).Amount.Equal(math.NewInt(500)), Equals, true)
	}

	// the regular staker can still withdraw everything the module holds
	tx.FromAddress = staker
	stakerAcc, err := staker.AccAddress()
	c.Assert(err, IsNil)
	_, err = handler.Run(ctx, NewMsgTCYUnstake(tx, cosmos.NewUint(100_00), stakerAcc))
	c.Assert(err, IsNil)
	c.Assert(k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native()).IsZero(), Equals, true)
	c.Assert(k.GetBalanceOf(ctx, stakerAcc, common.TCY).Amount.Equal(math.NewInt(1000)), Equals, true)
}

// handle() is reached only after validate() has already rejected these addresses, so
// this pins the independent guard on the fund moving half of the path: called directly,
// handle() must refuse before SendFromModuleToAccount pays out other stakers' deposits.
func (s *HandlerTCYUnstake) TestHandleRejectsSmartContractAddress(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "TCYUnstakingHalt", 0)

	scAddr, scAcc := tcySmartContractStaker(c, ctx, k, cosmos.NewUint(500))

	// a regular staker's TCY, held by the module on their behalf
	stakerAmount := cosmos.NewUint(1000)
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(GetRandomRUNEAddress(), stakerAmount)), IsNil)
	stakeCoin := common.NewCoin(common.TCY, stakerAmount)
	c.Assert(k.MintToModule(ctx, ModuleName, stakeCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, TCYStakeName, common.NewCoins(stakeCoin)), IsNil)

	tx := common.NewTx(
		common.BlankTxID,
		scAddr,
		GetRandomRUNEAddress(),
		common.Coins{},
		common.Gas{},
		"",
	)
	msg := NewMsgTCYUnstake(tx, cosmos.NewUint(100_00), scAcc)
	handler := NewTCYUnstakeHandler(NewDummyMgrWithKeeper(k))

	_, err := handler.handle(ctx, *msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "cannot unstake from TCY smart contract address: "+scAddr.String())

	// nothing left the module, and the contract's own balance is untouched
	c.Assert(k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native()).Equal(stakerAmount), Equals, true)
	c.Assert(k.GetBalanceOf(ctx, scAcc, common.TCY).Amount.Equal(math.NewInt(500)), Equals, true)
	c.Assert(tcysmartcontract.IsTCYSmartContractAddress(scAddr), Equals, true)
}
