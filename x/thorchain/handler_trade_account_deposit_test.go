package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerTradeAccountDeposit struct{}

var _ = Suite(&HandlerTradeAccountDeposit{})

func (HandlerTradeAccountDeposit) TestTradeAccountDeposit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsDepositEnabled.String(), 1)

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)

	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)

	bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "350")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)

	// Pass wrong message type
	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Disable trade accounts
	mgr.Keeper().SetMimir(ctx, "TradeAccountsEnabled", 0)

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*trade accounts are disabled.*")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositDepositDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Keep trade accounts enabled but disable deposit specifically
	mgr.Keeper().SetMimir(ctx, "TradeAccountsDepositEnabled", 0)

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*trade accounts are disabled.*")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositHaltDeposit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsDepositEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateTradeAssetHaltDeposit, asset.Chain), 1)

	msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*trade asset deposits are disabled.*")
}

func (HandlerTradeAccountDeposit) TestTradeAccountDepositValidateBasicFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewTradeAccountDepositHandler(mgr)
	addr := GetRandomBech32Addr()
	dummyTx := common.Tx{ID: "test"}

	// Empty asset should fail ValidateBasic
	msg := NewMsgTradeAccountDeposit(common.EmptyAsset, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Zero amount should fail
	msg = NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.ZeroUint(), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// THORChain asset should fail
	msg = NewMsgTradeAccountDeposit(common.RuneNative, cosmos.NewUint(350), addr, addr, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty signer should fail
	msg = NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(350), addr, cosmos.AccAddress{}, dummyTx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty tx ID should fail
	msg = NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(350), addr, addr, common.Tx{})
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
}
