package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmInstantiateContractHandler processes incoming MsgInstantiateContract messages from x/wasm
type WasmInstantiateContractHandler struct {
	mgr Manager
}

// NewWasmInstantiateContractHandler create a new instance of WasmInstantiateContractHandler
func NewWasmInstantiateContractHandler(mgr Manager) WasmInstantiateContractHandler {
	return WasmInstantiateContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmInstantiateContractHandler
func (h WasmInstantiateContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgInstantiateContractResponse, error) {
	msg, ok := m.(*wasmtypes.MsgInstantiateContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgInstantiateContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgInstantiateContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmInstantiateContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract) error {
	return nil
}

func (h WasmInstantiateContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract) (*wasmtypes.MsgInstantiateContractResponse, error) {
	ctx.Logger().Info("receive MsgInstantiateContract", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgInstantiateContract while THORChain is halted")
	}

	senderAddr, err := cosmos.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	var adminAddr cosmos.AccAddress
	if msg.Admin != "" {
		if adminAddr, err = cosmos.AccAddressFromBech32(msg.Admin); err != nil {
			return nil, errorsmod.Wrap(err, "admin")
		}
	}

	address, data, err := h.mgr.WasmManager().InstantiateContract(
		ctx,
		msg.CodeID,
		senderAddr,
		adminAddr,
		msg.Msg,
		msg.Label,
		msg.Funds,
	)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgInstantiateContractResponse{
		Address: address.String(),
		Data:    data,
	}, nil
}
