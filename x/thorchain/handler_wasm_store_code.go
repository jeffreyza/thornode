package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmStoreCodeHandler processes incoming MsgStoreCode messages from x/wasm
type WasmStoreCodeHandler struct {
	mgr Manager
}

// NewWasmStoreCodeHandler create a new instance of WasmStoreCodeHandler
func NewWasmStoreCodeHandler(mgr Manager) WasmStoreCodeHandler {
	return WasmStoreCodeHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmStoreCodeHandler
func (h WasmStoreCodeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgStoreCodeResponse, error) {
	msg, ok := m.(*wasmtypes.MsgStoreCode)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgStoreCode failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgStoreCode", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmStoreCodeHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgStoreCode) error {
	return nil
}

func (h WasmStoreCodeHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgStoreCode) (*wasmtypes.MsgStoreCodeResponse, error) {
	ctx.Logger().Info("receive MsgStoreCode", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgStoreCode while THORChain is halted")
	}

	senderAddr, err := cosmos.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	codeId, checksum, err := h.mgr.WasmManager().StoreCode(ctx, senderAddr, msg.WASMByteCode)
	if err != nil {
		return nil, err
	}
	return &wasmtypes.MsgStoreCodeResponse{
		CodeID:   codeId,
		Checksum: checksum,
	}, err
}
