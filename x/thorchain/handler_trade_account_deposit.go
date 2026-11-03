package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// TradeAccountDepositHandler is handler to process MsgTradeAccountDeposit
type TradeAccountDepositHandler struct {
	mgr Manager
}

// NewTradeAccountDepositHandler create a new instance of TradeAccountDepositHandler
func NewTradeAccountDepositHandler(mgr Manager) TradeAccountDepositHandler {
	return TradeAccountDepositHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for TradeAccountDepositHandler
func (h TradeAccountDepositHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgTradeAccountDeposit)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgTradeAccountDeposit failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgTradeAccountDeposit", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h TradeAccountDepositHandler) validate(ctx cosmos.Context, msg MsgTradeAccountDeposit) error {
	tradeAccountsEnabled := h.mgr.Keeper().GetConfigInt64(ctx, constants.TradeAccountsEnabled)
	tradeAccountsDepositEnabled := h.mgr.Keeper().GetConfigInt64(ctx, constants.TradeAccountsDepositEnabled)
	if tradeAccountsEnabled <= 0 || tradeAccountsDepositEnabled <= 0 {
		return fmt.Errorf("trade accounts are disabled")
	}
	if err := h.checkHalt(ctx, msg.Asset.Chain.String()); err != nil {
		return err
	}
	return msg.ValidateBasic()
}

// handle process MsgTradeAccountDeposit
func (h TradeAccountDepositHandler) handle(ctx cosmos.Context, msg MsgTradeAccountDeposit) error {
	_, err := h.mgr.TradeAccountManager().Deposit(ctx, msg.Asset, msg.Amount, msg.Address, msg.Tx.FromAddress, msg.Tx.ID)
	if err != nil {
		ctx.Logger().Error("fail to handle Deposit", "error", err)
		return err
	}
	return nil
}

func (h TradeAccountDepositHandler) checkHalt(ctx cosmos.Context, val string) error {
	m, err := h.mgr.Keeper().GetMimirWithRef(ctx, constants.MimirTemplateTradeAssetHaltDeposit, val)
	if err != nil {
		return err
	}
	if m > 0 && m <= ctx.BlockHeight() {
		return fmt.Errorf("%s trade asset deposits are disabled", val)
	}
	return nil
}
