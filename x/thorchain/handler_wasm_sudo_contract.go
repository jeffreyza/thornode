package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmSudoContractHandler processes incoming MsgSudoContract messages from x/wasm
type WasmSudoContractHandler struct {
	mgr Manager
}

// NewWasmSudoContractHandler create a new instance of WasmSudoContractHandler
func NewWasmSudoContractHandler(mgr Manager) WasmSudoContractHandler {
	return WasmSudoContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmSudoContractHandler
func (h WasmSudoContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgSudoContractResponse, error) {
	msg, ok := m.(*wasmtypes.MsgSudoContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSudoContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgSudoContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmSudoContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgSudoContract) error {
	return nil
}

func (h WasmSudoContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	ctx.Logger().Info("receive MsgSudoContract", "from", msg.Authority)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgSudoContract while THORChain is halted")
	}

	authorityAddr, err := cosmos.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	contractAddr, err := cosmos.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := h.mgr.WasmManager().SudoContract(ctx, contractAddr, authorityAddr, msg.Msg)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgSudoContractResponse{
		Data: data,
	}, nil
}
