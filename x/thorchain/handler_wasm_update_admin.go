package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmUpdateAdminHandler processes incoming MsgUpdateAdmin messages from x/wasm
type WasmUpdateAdminHandler struct {
	mgr Manager
}

// NewWasmUpdateAdminHandler create a new instance of WasmUpdateAdminHandler
func NewWasmUpdateAdminHandler(mgr Manager) WasmUpdateAdminHandler {
	return WasmUpdateAdminHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmUpdateAdminHandler
func (h WasmUpdateAdminHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgUpdateAdminResponse, error) {
	msg, ok := m.(*wasmtypes.MsgUpdateAdmin)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgUpdateAdmin failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgUpdateAdmin", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmUpdateAdminHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgUpdateAdmin) error {
	return nil
}

func (h WasmUpdateAdminHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgUpdateAdmin) (*wasmtypes.MsgUpdateAdminResponse, error) {
	ctx.Logger().Info("receive MsgUpdateAdmin", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgUpdateAdmin while THORChain is halted")
	}

	senderAddr, err := cosmos.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	newAdminAddr, err := cosmos.AccAddressFromBech32(msg.NewAdmin)
	if err != nil {
		return nil, errorsmod.Wrap(err, "newAdmin")
	}

	contractAddr, err := cosmos.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	_, err = h.mgr.WasmManager().UpdateAdmin(ctx, contractAddr, senderAddr, newAdminAddr)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUpdateAdminResponse{}, nil
}
