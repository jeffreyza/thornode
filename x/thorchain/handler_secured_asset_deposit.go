package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// SecuredAssetDepositHandler is handler to process MsgSecuredAssetDeposit
type SecuredAssetDepositHandler struct {
	mgr Manager
}

// NewSecuredAssetDepositHandler create a new instance of SecuredAssetDepositHandler
func NewSecuredAssetDepositHandler(mgr Manager) SecuredAssetDepositHandler {
	return SecuredAssetDepositHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for SecuredAssetDepositHandler
func (h SecuredAssetDepositHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgSecuredAssetDeposit)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSecuredAssetDeposit failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgSecuredAssetDeposit", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h SecuredAssetDepositHandler) validate(ctx cosmos.Context, msg MsgSecuredAssetDeposit) error {
	if err := validateSecuredAssetDeposit(ctx, h.mgr, msg.Asset, msg.Amount); err != nil {
		return err
	}

	return msg.ValidateBasic()
}

// validateSecuredAssetDeposit gates taking new layer-1 custody as a secured
// asset. Any path that mints secured shares for a fresh layer-1 inbound must
// run it before SecuredAssetManager().Deposit, otherwise it is an unguarded
// route around these controls (#2962, #2702). Paths that instead pay out value
// the network already custodies - withdrawing liquidity to a secured asset,
// the secured-asset fallback for an undeliverable outbound - are deliberately
// not gated: failing those strands funds rather than stopping an inflow.
func validateSecuredAssetDeposit(ctx cosmos.Context, mgr Manager, asset common.Asset, amount cosmos.Uint) error {
	if err := checkSecuredAssetDepositHalt(ctx, mgr, asset.Chain.String()); err != nil {
		return err
	}

	pool, err := mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		return fmt.Errorf("error loading %s pool", asset)
	}
	if !pool.IsAvailable() || pool.IsEmpty() {
		return fmt.Errorf("pool %s unavailable", asset)
	}

	// STABLE+ (stable-reserve) deposits are intentionally NOT exempt from the
	// bond/liquidity TVL cap: they mint into the same secured-asset pool depth
	// that atTVLCap bounds against bonded RUNE, so exempting them would let total
	// custodied secured value exceed what node bond secures.
	if atTVLCap(ctx, common.NewCoins(
		common.NewCoin(asset, amount),
	), mgr) {
		return fmt.Errorf("%s secured deposits more than bond", asset)
	}

	return nil
}

// handle process MsgSecuredAssetDeposit
func (h SecuredAssetDepositHandler) handle(ctx cosmos.Context, msg MsgSecuredAssetDeposit) error {
	if isStableReserveDepositMemo(msg.Tx.Memo) {
		return h.mgr.StableReserveManager().Deposit(ctx, msg.Asset, msg.Amount, msg.Tx.FromAddress, msg.Tx.ID)
	}

	_, err := h.mgr.SecuredAssetManager().Deposit(ctx, msg.Asset, msg.Amount, msg.Address, msg.Tx.FromAddress, msg.Tx.ID)
	if err != nil {
		ctx.Logger().Error("fail to handle Deposit", "error", err)
		return err
	}
	return nil
}

func checkSecuredAssetDepositHalt(ctx cosmos.Context, mgr Manager, val string) error {
	m, err := mgr.Keeper().GetMimirWithRef(ctx, constants.MimirTemplateSecuredAssetHaltDeposit, val)
	if err != nil {
		return err
	}
	if m > 0 && m <= ctx.BlockHeight() {
		return fmt.Errorf("%s secured asset deposits are disabled", val)
	}
	return nil
}
