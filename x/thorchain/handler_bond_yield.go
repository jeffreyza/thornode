package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// BondYieldHandler processes bond provider auto-claim settings.
type BondYieldHandler struct {
	mgr Manager
}

// NewBondYieldHandler creates the handler for bond yield auto-claim messages.
func NewBondYieldHandler(mgr Manager) BondYieldHandler {
	return BondYieldHandler{mgr: mgr}
}

// Run validates and applies a MsgBondYield transaction.
func (h BondYieldHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgBondYield)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgBondYield",
		"node address", msg.NodeAddress,
		"request hash", msg.TxIn.ID,
		"bps", msg.Bps,
	)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg bond yield fail validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg bond yield", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

// validate checks basic state, auto-claim settings, and payout destination constraints.
func (h BondYieldHandler) validate(ctx cosmos.Context, msg MsgBondYield) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if !isBondYieldEnabled(ctx, h.mgr) {
		return cosmos.ErrUnknownRequest("bond yield is not enabled")
	}

	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get node account(%s)", msg.NodeAddress))
	}

	switch nodeAccount.Status {
	case NodeActive, NodeStandby, NodeReady:
		// allow configuring yield for positive validator states
	default:
		return cosmos.ErrUnknownRequest(fmt.Sprintf("node account status is not eligible for bond yield: %s", nodeAccount.Status))
	}

	// The signer is the bond provider whose yield settings are being changed.
	providerAcc, err := msg.TxIn.FromAddress.AccAddress()
	if err != nil {
		return cosmos.ErrInvalidAddress("bond yield from address must be a THORChain address")
	}

	bp, err := h.mgr.Keeper().GetBondProviders(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", msg.NodeAddress))
	}
	if err = passiveBackfill(ctx, h.mgr, nodeAccount, &bp); err != nil {
		return err
	}
	if !bp.Has(providerAcc) {
		return cosmos.ErrUnknownRequest("bond provider address is not valid for node account")
	}

	if msg.Bps == 0 {
		// handle/SetAutoClaim clears payout_target for zero-bps updates, so a
		// stale or malformed target in a clear message is intentionally ignored.
		return nil
	}
	if msg.PayoutTarget == "" {
		return nil
	}

	provider := NewBondProvider(providerAcc)
	provider.PayoutTarget = msg.PayoutTarget
	if _, _, err := resolveBondYieldPayoutTarget(ctx, h.mgr, provider); err != nil {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("invalid bond yield payout target: %s", err))
	}

	return nil
}

// handle stores auto-claim settings. Payouts happen later during reward handling.
func (h BondYieldHandler) handle(ctx cosmos.Context, msg MsgBondYield) error {
	nodeAccount, err := h.mgr.Keeper().GetNodeAccount(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get node account(%s)", msg.NodeAddress))
	}

	providerAcc, err := msg.TxIn.FromAddress.AccAddress()
	if err != nil {
		return ErrInternal(err, "fail to parse bond provider address")
	}

	bp, err := h.mgr.Keeper().GetBondProviders(ctx, msg.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", msg.NodeAddress))
	}
	// Backfill before checking provider membership so legacy node state has a
	// well-formed provider list before Bond Yield settings are written.
	if err = passiveBackfill(ctx, h.mgr, nodeAccount, &bp); err != nil {
		return err
	}
	if !bp.Has(providerAcc) {
		return cosmos.ErrUnknownRequest("bond provider address is not valid for node account")
	}

	if !bp.SetAutoClaim(providerAcc, msg.Bps, msg.PayoutTarget) {
		return cosmos.ErrUnknownRequest("bond provider address is not valid for node account")
	}
	return h.mgr.Keeper().SetBondProviders(ctx, bp)
}
