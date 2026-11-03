package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmInstantiateContract2Handler processes incoming MsgInstantiateContract2 messages from x/wasm
type WasmInstantiateContract2Handler struct {
	mgr Manager
}

// NewWasmInstantiateContract2Handler create a new instance of WasmInstantiateContract2Handler
func NewWasmInstantiateContract2Handler(mgr Manager) WasmInstantiateContract2Handler {
	return WasmInstantiateContract2Handler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmInstantiateContract2Handler
func (h WasmInstantiateContract2Handler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgInstantiateContract2Response, error) {
	msg, ok := m.(*wasmtypes.MsgInstantiateContract2)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgInstantiateContract2 failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgInstantiateContract2", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmInstantiateContract2Handler) validate(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract2) error {
	return nil
}

func (h WasmInstantiateContract2Handler) handle(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract2) (*wasmtypes.MsgInstantiateContract2Response, error) {
	ctx.Logger().Info("receive MsgInstantiateContract2", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgInstantiateContract2 while THORChain is halted")
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

	address, data, err := h.mgr.WasmManager().InstantiateContract2(
		ctx,
		msg.CodeID,
		senderAddr,
		adminAddr,
		msg.Msg,
		msg.Label,
		msg.Funds,
		msg.Salt,
		msg.FixMsg,
	)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgInstantiateContract2Response{
		Address: address.String(),
		Data:    data,
	}, nil
}
