package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmClearAdminHandler processes incoming MsgClearAdmin messages from x/wasm
type WasmClearAdminHandler struct {
	mgr Manager
}

// NewWasmClearAdminHandler create a new instance of WasmClearAdminHandler
func NewWasmClearAdminHandler(mgr Manager) WasmClearAdminHandler {
	return WasmClearAdminHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmClearAdminHandler
func (h WasmClearAdminHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgClearAdminResponse, error) {
	msg, ok := m.(*wasmtypes.MsgClearAdmin)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgClearAdmin failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgClearAdmin", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmClearAdminHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgClearAdmin) error {
	return nil
}

func (h WasmClearAdminHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgClearAdmin) (*wasmtypes.MsgClearAdminResponse, error) {
	ctx.Logger().Info("receive MsgClearAdmin", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgClearAdmin while THORChain is halted")
	}

	senderAddr, err := cosmos.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	contractAddr, err := cosmos.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	_, err = h.mgr.WasmManager().ClearAdmin(ctx, contractAddr, senderAddr)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgClearAdminResponse{}, nil
}
