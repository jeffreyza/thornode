package thorchain

import (
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type HandlerSendSuiteV87 struct{}

var _ = Suite(&HandlerSendSuiteV87{})

func (s *HandlerSendSuiteV87) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	moduleNameAccAddr := k.GetModuleAccAddress(ModuleName)

	msg := &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      cosmos.NewCoins(cosmos.NewCoin("dummy", cosmos.NewInt(12))),
	}
	handler := NewSendHandler(NewDummyMgrWithKeeper(k))
	err := handler.validate(ctx, msg)
	c.Assert(err, IsNil)

	msg.ToAddress = moduleNameAccAddr
	// "dummy" is not the denom of the asset it parses to (THOR.DUMMY, whose
	// denom is "thor.dummy"), so it cannot be converted to a deposit.
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)

	msg.Amount = cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12)))
	err = handler.validate(ctx, msg)
	c.Assert(err, IsNil, Commentf("sending to module address: %s indicates a memo will be attached for MsgDeposit", ModuleName))

	for _, moduleName := range []string{AsgardName, BondName, ReserveName} {
		msg.ToAddress = k.GetModuleAccAddress(moduleName)
		validateErr := handler.validate(ctx, msg)
		c.Assert(validateErr, NotNil, Commentf("cannot send to module: %s", moduleName))
	}

	// invalid msg
	msg = &MsgSend{}
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)

	// Note that cosmos.NewCoins and cosmos.ParsedCoins both sanitise (drop) zero-amount coins.
	zeroCoin := cosmos.NewCoin("dummy", cosmos.ZeroInt())
	c.Assert(zeroCoin.String(), Equals, "0dummy")
	newCoins := cosmos.NewCoins(zeroCoin)
	c.Assert(newCoins.String(), Equals, "")
	parsedCoins, err := cosmos.ParseCoins("0dummy")
	c.Assert(err, IsNil)
	c.Assert(parsedCoins.String(), Equals, "")
	// (This is a valid ParseCoins format, as shown with a non-zero amount.)
	parsedCoins, err = cosmos.ParseCoins("1dummy")
	c.Assert(err, IsNil)
	c.Assert(parsedCoins.String(), Equals, "1dummy")

	zeroCoins := cosmos.Coins{zeroCoin}
	c.Assert(zeroCoins.String(), Equals, "0dummy")

	// Test zero amount rejection when not for MsgDeposit conversion.
	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      zeroCoins,
	}
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)
	// Test zero amount validity when for MsgDeposit conversion (such as for MsgUnbond).
	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   moduleNameAccAddr,
		Amount:      zeroCoins,
	}
	err = handler.validate(ctx, msg)
	c.Assert(err, IsNil)
}

func (s *HandlerSendSuiteV87) TestValidateMultiple(c *C) {
	// Verify that multiple coins can be sent, except to module address
	ctx, k := setupKeeperForTest(c)

	// cosmos bank sends are gated in the handler and default to off, and this
	// case is about the coins rather than the gate
	k.SetMimir(ctx, "BankSendEnabled", 1)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	handler := NewSendHandler(NewDummyMgrWithKeeper(k))
	moduleAddress := k.GetModuleAccAddress(ModuleName)

	msg := &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err := handler.validate(ctx, msg)
	c.Assert(err, IsNil)

	bank := &banktypes.MsgSend{
		FromAddress: addr1.String(),
		ToAddress:   addr2.String(),
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err = handler.validate(ctx, bank)
	c.Assert(err, IsNil)

	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   moduleAddress,
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)

	bank = &banktypes.MsgSend{
		FromAddress: addr1.String(),
		ToAddress:   moduleAddress.String(),
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err = handler.validate(ctx, bank)
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()

	FundAccount(c, ctx, k, addr1, 200*common.One)

	coin, err := common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One)).Native()
	c.Assert(err, IsNil)
	msg := &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      cosmos.NewCoins(coin),
	}

	handler := NewSendHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, msg)
	c.Assert(err, IsNil)

	// invalid msg should result in a error
	result, err := handler.Run(ctx, NewMsgNetworkFee(ctx.BlockHeight(), common.ETHChain, 1, 10000, GetRandomBech32Addr()))
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	// insufficient funds
	coin, err = common.NewCoin(common.RuneNative, cosmos.NewUint(3000*common.One)).Native()
	c.Assert(err, IsNil)
	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      cosmos.NewCoins(coin),
	}
	_, err = handler.handle(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestDepositConversionRequiresCanonicalDenom(c *C) {
	ctx, _ := setupKeeperForTest(c)

	fromAddr := GetRandomBech32Addr()

	// "x/ruji" is the denom of the asset it parses to, so it converts
	_, err := getDeposit(ctx, fromAddr, cosmos.Coins{cosmos.NewCoin("x/ruji", cosmos.NewInt(12))})
	c.Assert(err, IsNil)

	// "x/rune" parses to THOR.RUNE, whose denom is "rune" - converting it would
	// debit native RUNE for a send of a different denom
	_, err = getDeposit(ctx, fromAddr, cosmos.Coins{cosmos.NewCoin("x/rune", cosmos.NewInt(12))})
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestDepositConversionRequiresDirectSigner(c *C) {
	ctx, _ := setupKeeperForTest(c)

	fromAddr := GetRandomBech32Addr()
	coins := cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12)))
	memoCtx := ctx.WithValue(ContextKeyTxMemo, "SWAP:BTC.BTC")

	// a directly signed send is converted, inheriting the memo of the tx it signed
	signedCtx := memoCtx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(fromAddr): {},
	})
	msgDeposit, err := getDeposit(signedCtx, fromAddr, coins)
	c.Assert(err, IsNil)
	c.Assert(msgDeposit.Memo, Equals, "SWAP:BTC.BTC")
	c.Assert(msgDeposit.Signer.Equals(fromAddr), Equals, true)

	// a send dispatched on behalf of fromAddr must not execute a memo fromAddr
	// never signed
	dispatchedCtx := memoCtx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(GetRandomBech32Addr()): {},
	})
	_, err = getDeposit(dispatchedCtx, fromAddr, coins)
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestSendRequiresDirectSignerOrContract(c *C) {
	ctx, k := setupKeeperForTest(c)

	fromAddr := GetRandomBech32Addr()
	toAddr := GetRandomBech32Addr()
	handler := NewSendHandler(NewDummyMgrWithKeeper(k))

	msg := &MsgSend{
		FromAddress: fromAddr,
		ToAddress:   toAddr,
		Amount:      cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12))),
	}

	// signed by its sender
	signedCtx := ctx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(fromAddr): {},
	})
	c.Assert(handler.validate(signedCtx, msg), IsNil)

	// dispatched by neither the sender nor a contract
	dispatchedCtx := ctx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(GetRandomBech32Addr()): {},
	})
	c.Assert(handler.validate(dispatchedCtx, msg), NotNil)
}

func (s *HandlerSendSuiteV87) TestBankSendEnabledGateInHandler(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	FundAccount(c, ctx, k, addr1, 200*common.One)

	handler := NewBankSendHandler(NewSendHandler(NewDummyMgrWithKeeper(k)))
	msg := &banktypes.MsgSend{
		FromAddress: addr1.String(),
		ToAddress:   addr2.String(),
		Amount:      cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12))),
	}

	// the gate is enforced by the msg server itself, not only by the ante
	// handler, so that no dispatch path can bypass it
	k.SetMimir(ctx, "BankSendEnabled", 0)
	_, err := handler.Send(ctx, msg)
	c.Assert(err, ErrorMatches, ".*bank sends are disabled.*")

	k.SetMimir(ctx, "BankSendEnabled", 1)
	_, err = handler.Send(ctx, msg)
	c.Assert(err, IsNil)
}
