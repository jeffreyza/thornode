package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerRunePoolWithdrawSuite struct{}

var _ = Suite(&HandlerRunePoolWithdrawSuite{})

func (s *HandlerRunePoolWithdrawSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_InvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolWithdrawHandler(mgr)

	// passing wrong message type should fail
	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_ValidationDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolWithdrawHandler(mgr)

	addr := GetRandomBech32Addr()
	tx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, tx, cosmos.NewUint(5000), common.NoAddress, cosmos.ZeroUint())

	// RUNEPool is disabled by default
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "RUNEPool disabled")
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_ValidationHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Set halt withdraw at block 10 (current block is 18)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolHaltWithdraw.String(), 10)

	addr := GetRandomBech32Addr()
	tx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, tx, cosmos.NewUint(5000), common.NoAddress, cosmos.ZeroUint())

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "RUNEPool withdraw paused")
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_ValidateBasicFail(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Zero basis points should fail
	addr := GetRandomBech32Addr()
	tx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, tx, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint())
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_AffiliateBpsTooHigh(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Set max affiliate fee to 1000
	mgr.Keeper().SetMimir(ctx, constants.MaxAffiliateFeeBasisPoints.String(), 1000)

	addr := GetRandomBech32Addr()
	affiliateAddr := GetRandomTHORAddress()
	tx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	// Set affiliate bps to 2000 which exceeds max of 1000
	msg := NewMsgRunePoolWithdraw(addr, tx, cosmos.NewUint(5000), affiliateAddr, cosmos.NewUint(2000))

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "invalid affiliate basis points, max: 1000, request: 2000")
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_DepositNotMature(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewRunePoolWithdrawHandler(mgr)
	depositHandler := NewRunePoolDepositHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)

	// Set maturity to 100 blocks
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 100)

	addr := GetRandomBech32Addr()
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit first
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One)),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err := depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Try to withdraw at the same block height - should fail since maturity not reached
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), common.NoAddress, cosmos.ZeroUint())
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "deposit reaches maturity in 100 blocks")
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_HappyPath(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool, set maturity to 0
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)

	addr := GetRandomBech32Addr()
	depositAmt := cosmos.NewUint(100 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit first
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err := depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Withdraw 100% (10000 basis points)
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), common.NoAddress, cosmos.ZeroUint())
	result, err := withdrawHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify provider units are zero after full withdrawal
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(runeProvider.Units.IsZero(), Equals, true)
	c.Assert(runeProvider.WithdrawAmount.Equal(depositAmt), Equals, true)
	c.Assert(runeProvider.LastWithdrawHeight, Equals, ctx.BlockHeight())

	// Verify RUNEPool updated
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(runePool.PoolUnits.IsZero(), Equals, true)
	c.Assert(runePool.RuneWithdrawn.Equal(depositAmt), Equals, true)
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_PartialWithdraw(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool, set maturity to 0
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)

	addr := GetRandomBech32Addr()
	depositAmt := cosmos.NewUint(100 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err := depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Withdraw 50% (5000 basis points)
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(5000), common.NoAddress, cosmos.ZeroUint())
	_, err = withdrawHandler.Run(ctx, msg)
	c.Assert(err, IsNil)

	// Verify about half remains
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(runeProvider.Units.IsZero(), Equals, false)

	// Verify pool still has units
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(runePool.PoolUnits.IsZero(), Equals, false)
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_WithAffiliate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool, set maturity to 0
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.MaxAffiliateFeeBasisPoints.String(), 1000)

	addr := GetRandomBech32Addr()
	affiliateAddr := common.Address(GetRandomBech32Addr().String())
	depositAmt := cosmos.NewUint(100 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err := depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Withdraw with affiliate fee - 100% withdraw with 500 bps affiliate
	// Since there's no profit (just deposited), affiliate amount should be zero
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), affiliateAddr, cosmos.NewUint(500))
	result, err := withdrawHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify full withdrawal occurred
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(runeProvider.Units.IsZero(), Equals, true)
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_ReserveEnterPath(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool, set maturity to 0
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)
	// Set high backstop and POL limits to allow reserve enter
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolMaxReserveBackstop.String(), int64(500_000_000*common.One))
	mgr.Keeper().SetMimir(ctx, constants.POLMaxNetworkDeposit.String(), int64(500_000_000*common.One))

	// Set up a pool with Reserve as LP to create POL value.
	// This makes runePoolValue = polPoolValue + pendingRune.
	// When pendingRune is drained, runePoolValue still has polPoolValue,
	// so withdrawAmount > pendingRune, triggering reserve enter.
	polAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Reserve as LP gives POL value
	lp := LiquidityProvider{
		Asset:       common.DOGEAsset,
		RuneAddress: polAddress,
		Units:       cosmos.NewUint(500 * common.One),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	addr := GetRandomBech32Addr()
	depositAmt := cosmos.NewUint(100 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit RUNE
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err = depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Drain the RUNEPool module balance
	pendingRune := mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)
	if !pendingRune.IsZero() {
		drainCoins := common.NewCoins(common.NewCoin(common.RuneNative, pendingRune))
		err = mgr.Keeper().SendFromModuleToModule(ctx, RUNEPoolName, AsgardName, drainCoins)
		c.Assert(err, IsNil)
	}

	// Withdraw should trigger reserve enter path since POL value > 0 but pending = 0
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), common.NoAddress, cosmos.ZeroUint())
	result, err := withdrawHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify withdrawal succeeded
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(runeProvider.Units.IsZero(), Equals, true)
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_ReserveEnterExceedsBackstop(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool, set maturity to 0
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)
	// Set very low backstop and POL limits to trigger the circuit breaker
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolMaxReserveBackstop.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.POLMaxNetworkDeposit.String(), 1)

	// Set up a pool with Reserve as LP to create POL value
	polAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:       common.DOGEAsset,
		RuneAddress: polAddress,
		Units:       cosmos.NewUint(500 * common.One),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	addr := GetRandomBech32Addr()
	depositAmt := cosmos.NewUint(100 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit RUNE
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err = depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Drain the RUNEPool module
	pendingRune := mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)
	if !pendingRune.IsZero() {
		drainCoins := common.NewCoins(common.NewCoin(common.RuneNative, pendingRune))
		err = mgr.Keeper().SendFromModuleToModule(ctx, RUNEPoolName, AsgardName, drainCoins)
		c.Assert(err, IsNil)
	}

	// Withdraw should fail because reserve enter exceeds backstop
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), common.NoAddress, cosmos.ZeroUint())
	_, err = withdrawHandler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "reserve enter .* rune exceeds backstop")
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_WithAffiliateProfit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool, set maturity to 0
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.MaxAffiliateFeeBasisPoints.String(), 1000)

	addr := GetRandomBech32Addr()
	affiliateAddr := common.Address(GetRandomBech32Addr().String())
	depositAmt := cosmos.NewUint(100 * common.One)

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit RUNE
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, depositAmt),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err := depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Simulate profit by adding extra RUNE to the RUNEPool module
	// This increases the total rune pool value, creating yield for the provider
	FundModule(c, ctx, mgr.Keeper(), RUNEPoolName, 50*common.One)

	// Withdraw with affiliate - provider has profit, so affiliate gets a share
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), affiliateAddr, cosmos.NewUint(1000))
	result, err := withdrawHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify provider units are zero after full withdrawal
	runeProvider, err := mgr.Keeper().GetRUNEProvider(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(runeProvider.Units.IsZero(), Equals, true)
	// Withdraw amount should be more than deposit (due to profit)
	c.Assert(runeProvider.WithdrawAmount.GT(depositAmt), Equals, true,
		Commentf("withdraw %s should be greater than deposit %s", runeProvider.WithdrawAmount, depositAmt))
}

func (s *HandlerRunePoolWithdrawSuite) TestRunePoolWithdrawHandler_HaltNotReached(c *C) {
	ctx, mgr := setupManagerForTest(c)
	depositHandler := NewRunePoolDepositHandler(mgr)
	withdrawHandler := NewRunePoolWithdrawHandler(mgr)

	// Enable RUNEPool
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolDepositMaturityBlocks.String(), 0)

	// Set halt withdraw at block 100 (current block is 18, so not yet halted)
	mgr.Keeper().SetMimir(ctx, constants.RUNEPoolHaltWithdraw.String(), 100)

	addr := GetRandomBech32Addr()
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 500*common.One)

	// Deposit
	depositTx := common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.THORChain,
		Coins: common.Coins{
			common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One)),
		},
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	_, err := depositHandler.Run(ctx, NewMsgRunePoolDeposit(addr, depositTx))
	c.Assert(err, IsNil)

	// Withdraw should succeed since halt block hasn't been reached
	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.THORChain,
		FromAddress: common.Address(addr.String()),
		ToAddress:   common.Address(addr.String()),
	}
	msg := NewMsgRunePoolWithdraw(addr, withdrawTx, cosmos.NewUint(10000), common.NoAddress, cosmos.ZeroUint())
	result, err := withdrawHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}
