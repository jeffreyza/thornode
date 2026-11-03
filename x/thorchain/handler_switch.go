package thorchain

import (
	"fmt"

	"github.com/hashicorp/go-multierror"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// SwitchHandler is handler to process MsgSwitch
type SwitchHandler struct {
	mgr Manager
}

// NewSwitchHandler create a new instance of SwitchHandler
func NewSwitchHandler(mgr Manager) SwitchHandler {
	return SwitchHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for SwitchHandler
func (h SwitchHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgSwitch)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSwitch failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgSwitch", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h SwitchHandler) validate(ctx cosmos.Context, msg MsgSwitch) error {
	if err := h.checkEnabled(ctx, msg.Asset); err != nil {
		return err
	}

	return msg.ValidateBasic()
}

func (h SwitchHandler) handle(ctx cosmos.Context, msg MsgSwitch) error {
	// The native mint and the L1 burn outbound must be atomic: minting the native
	// replacement token before the burn outbound is durably scheduled would allow
	// the supply to be inflated without a corresponding L1 burn if scheduling later
	// fails or the outbound is dropped. Perform both inside a cache context and only
	// commit once the outbound has been successfully scheduled; any earlier return
	// discards the mint along with every other cached side effect.
	cacheCtx, commit := ctx.CacheContext()

	// Mint the full amount as native tokens. Switch assets are tokens (not gas assets),
	// so the outbound burn gas is subsidized by the gas asset pool—no outbound fee
	// deduction from the switch amount is needed.
	addr, err := h.mgr.SwitchManager().Switch(cacheCtx, msg.Asset, msg.Amount, msg.Address, msg.Tx.FromAddress, msg.Tx.ID)
	if err != nil {
		ctx.Logger().Error("fail to handle switch", "error", err)
		return err
	}

	toi := TxOutItem{
		Chain:     msg.Asset.GetChain(),
		InHash:    msg.Tx.ID,
		ToAddress: addr,
		Coin:      common.NewCoin(msg.Asset, msg.Amount),
	}

	ok, err := h.mgr.TxOutStore().TryAddTxOutItem(cacheCtx, h.mgr, toi, cosmos.ZeroUint())
	if err != nil {
		return multierror.Append(errFailAddOutboundTx, err)
	}
	if !ok {
		return errFailAddOutboundTx
	}

	// Both the mint and the burn outbound succeeded; commit the cached state and
	// events to the parent context.
	commit()

	return nil
}

func (h SwitchHandler) checkEnabled(ctx cosmos.Context, asset common.Asset) error {
	m, err := h.mgr.Keeper().GetMimirWithRef(ctx, constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	if err != nil {
		return err
	}
	if m <= 0 || m >= ctx.BlockHeight() {
		return fmt.Errorf("%s switching is not enabled", asset.String())
	}
	return nil
}
