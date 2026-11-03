package thorchain

import (
	"fmt"
	"math"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// WasmExecuteContractHandler processes incoming MsgExecuteContract messages from x/wasm
type WasmExecuteContractHandler struct {
	mgr Manager
}

// NewWasmExecuteContractHandler create a new instance of WasmExecuteContractHandler
func NewWasmExecuteContractHandler(mgr Manager) WasmExecuteContractHandler {
	return WasmExecuteContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmExecuteContractHandler
func (h WasmExecuteContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*wasmtypes.MsgExecuteContractResponse, error) {
	msg, ok := m.(*wasmtypes.MsgExecuteContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgExecuteContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgExecuteContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmExecuteContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) error {
	return nil
}

func (h WasmExecuteContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*wasmtypes.MsgExecuteContractResponse, error) {
	ctx.Logger().Info("receive MsgExecuteContract", "from", msg.Sender)
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgExecuteContract while THORChain is halted")
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}
	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := h.mgr.WasmManager().ExecuteContract(ctx, contractAddr, senderAddr, msg.Msg, msg.Funds)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgExecuteContractResponse{
		Data: data,
	}, nil
}

type WasmExecuteAnteDecorator struct {
	keeper keeper.Keeper
	ak     authante.AccountKeeper
	bk     authtypes.BankKeeper
}

func NewWasmExecuteAnteDecorator(
	keeper keeper.Keeper,
	ak authante.AccountKeeper,
	bk authtypes.BankKeeper,
) WasmExecuteAnteDecorator {
	return WasmExecuteAnteDecorator{
		keeper: keeper,
		ak:     ak,
		bk:     bk,
	}
}

func (ad WasmExecuteAnteDecorator) AnteHandle(
	ctx sdk.Context,
	tx sdk.Tx,
	simulate bool,
	next sdk.AnteHandler,
) (newCtx sdk.Context, err error) {
	for _, msg := range tx.GetMsgs() {
		// We can't allocate gas to a specific Msg in the Tx, so we deduct fees
		// for gas for the entirety of any tx that contains a wasm Msg that has no
		// fee charged in ante.
		//
		// Only add message types here whose own ante handling is a gas fee and
		// nothing else. A message that executes other messages (authz MsgExec
		// was such a case) would take this fee path and then pass through
		// AnteDecorator, which inspects top level messages only - its inner
		// messages would reach their handlers having skipped every
		// message-specific ante check.
		switch msg.(type) {
		case *wasmtypes.MsgStoreCode,
			*wasmtypes.MsgInstantiateContract,
			*wasmtypes.MsgInstantiateContract2,
			*wasmtypes.MsgExecuteContract,
			*wasmtypes.MsgMigrateContract,
			*wasmtypes.MsgSudoContract,
			*wasmtypes.MsgClearAdmin,
			*wasmtypes.MsgUpdateAdmin:
			tfc := newTxFeeChcker(ad.keeper)
			handler := ante.NewDeductFeeDecorator(ad.ak, ad.bk, nil, tfc)
			return handler.AnteHandle(ctx, tx, simulate, next)
		}
	}

	return next(ctx, tx, simulate)
}

func newTxFeeChcker(k keeper.Keeper) ante.TxFeeChecker {
	return func(ctx sdk.Context, tx sdk.Tx) (sdk.Coins, int64, error) {
		return checkTxFeeWithValidatorMinGasPrices(ctx, k, tx)
	}
}

func checkTxFeeWithValidatorMinGasPrices(ctx sdk.Context, k keeper.Keeper, tx sdk.Tx) (sdk.Coins, int64, error) {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return nil, 0, errorsmod.Wrap(sdkerrors.ErrTxDecode, "Tx must be a FeeTx")
	}

	feeCoins := feeTx.GetFee()
	gas := feeTx.GetGas()

	// Ensure that the provided fees meet a minimum threshold for the validator,
	// if this is a CheckTx. This is only for local mempool purposes, and thus
	// is only ran on check tx.
	if ctx.IsCheckTx() {
		minGasPrice, err := k.GetMimir(ctx, constants.MimirKeyWasmMinGasPrice)
		if err != nil {
			return nil, 0, err
		}
		clamped := common.Max(minGasPrice, 0)
		dec := sdkmath.LegacyNewDecFromIntWithPrec(sdkmath.NewInt(clamped), 18)

		minGasPrices := sdk.NewDecCoins(sdk.DecCoin{
			Denom:  "rune",
			Amount: dec,
		})

		if !minGasPrices.IsZero() {
			requiredFees := make(sdk.Coins, len(minGasPrices))

			// Validate gas before conversion to int64 to prevent integer overflow.
			// If gas > MaxInt64, conversion would silently overflow to negative value,
			// causing incorrect fee calculation that could bypass payment requirements.
			if gas > math.MaxInt64 {
				return nil, 0, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "gas value %d exceeds maximum allowed value", gas)
			}

			// Determine the required fees by multiplying each required minimum gas
			// price by the gas limit, where fee = ceil(minGasPrice * gasLimit).
			glDec := sdkmath.LegacyNewDec(int64(gas))
			for i, gp := range minGasPrices {
				fee := gp.Amount.Mul(glDec)
				requiredFees[i] = sdk.NewCoin(gp.Denom, fee.Ceil().RoundInt())
			}

			if !feeCoins.IsAnyGTE(requiredFees) {
				return nil, 0, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "insufficient fees; got: %s required: %s", feeCoins, requiredFees)
			}
		}
	}

	return feeCoins, 0, nil
}
