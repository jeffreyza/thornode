package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmMigrateContractHandler processes incoming MsgMigrateContract messages from x/wasm
type WasmMigrateContractHandler struct {
	mgr Manager
}

// NewWasmMigrateContractHandler create a new instance of WasmMigrateContractHandler
func NewWasmMigrateContractHandler(mgr Manager) WasmMigrateContractHandler {
	return WasmMigrateContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmMigrateContractHandler
func (h WasmMigrateContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgMigrateContractResponse, error) {
	msg, ok := m.(*wasmtypes.MsgMigrateContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgMigrateContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgMigrateContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmMigrateContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgMigrateContract) error {
	return nil
}

func (h WasmMigrateContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgMigrateContract) (*wasmtypes.MsgMigrateContractResponse, error) {
	ctx.Logger().Info("receive MsgMigrateContract", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgMigrateContract while THORChain is halted")
	}

	senderAddr, err := cosmos.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := h.mgr.WasmManager().MigrateContract(ctx, contractAddr, senderAddr, msg.CodeID, msg.Msg)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgMigrateContractResponse{
		Data: data,
	}, nil
}
