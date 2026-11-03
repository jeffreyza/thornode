package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerRunePoolDepositSuite struct{}

var _ = Suite(&HandlerRunePoolDepositSuite{})

func (s *HandlerRunePoolDepositSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_InvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// passing wrong message type should fail
	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_ValidationDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	addr := GetRandomBech32Addr()
	tx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One)),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolDeposit(addr, tx)

	// RUNEPool is disabled by default (value 0)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "RUNEPool disabled")
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_ValidationHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Set halt deposit at block 10 (current block is 18)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolHaltDeposit.String(), 10)

	addr := GetRandomBech32Addr()
	tx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One)),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolDeposit(addr, tx)

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "RUNEPool deposit paused")
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_ValidateBasicFail(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Empty signer should fail ValidateBasic
	msg := NewMsgRunePoolDeposit(cosmos.AccAddress{}, common.Tx{})
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_HappyPath(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	addr := GetRandomBech32Addr()
	depositAmt := cosmos.NewUint(100 * common.One)

	// Fund the Asgard module so it can transfer
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 200*common.One)

	tx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolDeposit(addr, tx)

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify RUNEProvider was created with correct values
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(runeProvider.Units.Equal(depositAmt), Equals, true, Commentf("expected %s, got %s", depositAmt, runeProvider.Units))
	c.Assert(runeProvider.DepositAmount.Equal(depositAmt), Equals, true)
	c.Assert(runeProvider.LastDepositHeight, Equals, ctx.BlockHeight())

	// Verify RUNEPool was updated
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(runePool.PoolUnits.Equal(depositAmt), Equals, true)
	c.Assert(runePool.RuneDeposited.Equal(depositAmt), Equals, true)
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_MultipleDeposits(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	addr := GetRandomBech32Addr()
	firstDeposit := cosmos.NewUint(100 * common.One)
	secondDeposit := cosmos.NewUint(50 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// First deposit
	tx1 := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, firstDeposit),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg1 := NewMsgRunePoolDeposit(addr, tx1)
	_, err := h.Run(ctx, msg1)
	c.Assert(err, IsNil)

	// Second deposit from same address
	tx2 := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, secondDeposit),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg2 := NewMsgRunePoolDeposit(addr, tx2)
	_, err = h.Run(ctx, msg2)
	c.Assert(err, IsNil)

	// Verify RUNEProvider accumulated correctly
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	expectedDeposit := firstDeposit.Add(secondDeposit)
	c.Assert(runeProvider.DepositAmount.Equal(expectedDeposit), Equals, true,
		Commentf("expected deposit %s, got %s", expectedDeposit, runeProvider.DepositAmount))

	// Verify RUNEPool totals
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(runePool.RuneDeposited.Equal(expectedDeposit), Equals, true)
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_TwoProviders(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	deposit1 := cosmos.NewUint(100 * common.One)
	deposit2 := cosmos.NewUint(200 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// First provider deposits
	tx1 := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, deposit1),
		},
		FromAddress: common.Address(addr1.String()),
		ToAddress:   common.Address(addr1.String()),
	}
	_, err := h.Run(ctx, NewMsgRunePoolDeposit(addr1, tx1))
	c.Assert(err, IsNil)

	// Second provider deposits
	tx2 := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, deposit2),
		},
		FromAddress: common.Address(addr2.String()),
		ToAddress:   common.Address(addr2.String()),
	}
	_, err = h.Run(ctx, NewMsgRunePoolDeposit(addr2, tx2))
	c.Assert(err, IsNil)

	// Verify each provider
	rp1, err := mgr.Keeper().GetRUNEProvider(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(rp1.DepositAmount.Equal(deposit1), Equals, true)

	rp2, err := mgr.Keeper().GetRUNEProvider(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(rp2.DepositAmount.Equal(deposit2), Equals, true)

	// Verify total pool
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	totalDeposited := deposit1.Add(deposit2)
	c.Assert(runePool.RuneDeposited.Equal(totalDeposited), Equals, true)
}

func (s *HandlerRunePoolDepositSuite) TestRunePoolDepositHandler_HaltNotReached(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Set halt deposit at block 100 (current block is 18, so not yet halted)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolHaltDeposit.String(), 100)

	addr := GetRandomBech32Addr()
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 200*common.One)

	tx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One)),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolDeposit(addr, tx)

	// Should succeed since halt block hasn't been reached
	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}
