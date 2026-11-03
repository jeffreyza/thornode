package thorchain

import (
	"context"
	"fmt"
	"strings"

	math "cosmossdk.io/math"
	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/bank/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

var _ types.MsgServer = (*BankSendHandler)(nil)

// BankSendHandler is a wrapper handler used to override msg routing for cosmos bank sends.
type BankSendHandler struct {
	h BaseHandler[sdk.Msg]
}

// NewBankSendHandler create a new instance of BankSendHandler
func NewBankSendHandler(h BaseHandler[sdk.Msg]) BankSendHandler {
	return BankSendHandler{h: h}
}

// Send is the entrypoint for bank MsgSend, passing through to the thorchain handler.
func (h BankSendHandler) Send(goCtx context.Context, msg *bank.MsgSend) (*bank.MsgSendResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if _, err := h.h.Run(ctx, msg); err != nil {
		return nil, err
	}

	return &bank.MsgSendResponse{}, nil
}

// MultiSend is rejected by the ante handler, but necessary to satisfy MsgServer
// interface for bank send bank.MsgMultiSend. Nothing here implements it, so it
// is refused rather than reported as a transfer that never happened.
func (h BankSendHandler) MultiSend(ctx context.Context, msg *bank.MsgMultiSend) (*bank.MsgMultiSendResponse, error) {
	return nil, cosmos.ErrUnknownRequest("bank multi sends are not supported")
}

// UpdateParams defines a governance operation for updating the x/bank module parameters.
// The authority is defined in the keeper.
//
// Since: cosmos-sdk 0.47
func (h BankSendHandler) UpdateParams(context.Context, *bank.MsgUpdateParams) (*bank.MsgUpdateParamsResponse, error) {
	return &bank.MsgUpdateParamsResponse{}, nil
}

// SetSendEnabled is a governance operation for setting the SendEnabled flag
// on any number of Denoms. Only the entries to add or update should be
// included. Entries that already exist in the store, but that aren't
// included in this message, will be left unchanged.
//
// Since: cosmos-sdk 0.47
func (h BankSendHandler) SetSendEnabled(context.Context, *bank.MsgSetSendEnabled) (*bank.MsgSetSendEnabledResponse, error) {
	return &bank.MsgSetSendEnabledResponse{}, nil
}

// NewSendHandler create a new instance of SendHandler
func NewSendHandler(mgr Manager) BaseHandler[sdk.Msg] {
	return BaseHandler[sdk.Msg]{
		mgr:    mgr,
		logger: MsgSendLogger,
		validators: NewValidators[sdk.Msg]().
			Register("3.0.0", MsgSendValidate),
		handlers: NewHandlers[sdk.Msg]().
			Register("3.0.0", MsgSendHandle),
	}
}

func MsgSendLogger(ctx cosmos.Context, m sdk.Msg) {
	msg, err := getThorSend(m)
	if err != nil {
		return
	}

	ctx.Logger().Info("receive MsgSend", "from", msg.FromAddress, "to", msg.ToAddress, "coins", msg.Amount)
}

// getThorSend returns a thor MsgSend from either a thor MsgSend or a x/bank MsgSend
func getThorSend(msg sdk.Msg) (*MsgSend, error) {
	switch msg := msg.(type) {
	case *MsgSend:
		return msg, nil
	case *bank.MsgSend:
		fromAddress, err := cosmos.AccAddressFromBech32(msg.FromAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to parse from address: %s", msg.FromAddress)
		}

		toAddress, err := cosmos.AccAddressFromBech32(msg.ToAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to parse to address: %s", msg.ToAddress)
		}

		return &MsgSend{
			FromAddress: fromAddress,
			ToAddress:   toAddress,
			Amount:      msg.Amount,
		}, nil
	default:
		return nil, fmt.Errorf("not a supported send message type: %T", msg)
	}
}

func getDeposit(ctx cosmos.Context, fromAddress sdk.AccAddress, sdkCoins sdk.Coins) (*MsgDeposit, error) {
	// The memo belongs to the transaction body, which is signed by the
	// transaction signers - not necessarily by fromAddress. Converting a send
	// that fromAddress did not sign into a deposit would execute a memo chosen
	// by somebody else with fromAddress' authority: an arbitrary THORChain
	// command (swap, withdraw, unbond, THORName transfer) in place of the plain
	// value transfer the sender authorised. Only convert directly signed sends;
	// anything dispatched on behalf of fromAddress must use MsgDeposit, which
	// carries its own memo.
	if !TxSignedBy(ctx, fromAddress) {
		return nil, fmt.Errorf("cannot convert send to deposit: %s did not sign the transaction", fromAddress)
	}

	var memo string
	ctxTxMemo := ctx.Context().Value(ContextKeyTxMemo)
	if ctxTxMemo != nil {
		if m, ok := ctxTxMemo.(string); ok {
			memo = m
		}
	}
	coins := make(common.Coins, len(sdkCoins))
	for i, coin := range sdkCoins {
		asset, err := common.NewAsset(
			// Strip x/denom denom string prefix to force asset.NewAsset
			// to interpret custom denom strings as native assets
			strings.TrimPrefix(coin.Denom, "x/"),
		)
		if err != nil {
			return nil, err
		}

		// The asset is converted back to a bank denom further down the deposit
		// path, and the conversion is not injective: "x/rune" and "RUNE" both
		// parse to THOR.RUNE, whose native denom is "rune". A coin that does not
		// survive the round trip would debit a denom other than the one that was
		// sent, so only accept those that do. Zero amount coins are exempt: they
		// only carry the memo of a deposit (MsgUnbond and friends) and move no
		// value, whatever they name.
		if coin.Amount.IsPositive() && asset.Native() != coin.Denom {
			return nil, fmt.Errorf("invalid denom: %s does not match asset %s", coin.Denom, asset)
		}

		coins[i] = common.Coin{
			Asset:  asset,
			Amount: math.NewUintFromBigInt(coin.Amount.BigInt()),
		}
	}
	return NewMsgDeposit(coins, memo, fromAddress), nil
}

// SendAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func SendAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, m sdk.Msg) (cosmos.Context, error) {
	msg, err := getThorSend(m)
	if err != nil {
		return ctx, err
	}
	return ctx, k.DeductNativeTxFeeFromAccount(ctx, msg.GetSigners()[0])
}

func MsgSendValidate(ctx cosmos.Context, mgr Manager, m sdk.Msg) error {
	msg, err := getThorSend(m)
	if err != nil {
		return err
	}

	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// A send must either be signed by its sender - in which case the ante
	// handler has run, deducting the native tx fee and applying the bank send
	// gate and the one-deposit-per-tx rule - or be dispatched by a CosmWasm
	// contract moving its own funds, which pays for execution through the
	// wrapping transaction. Any other dispatch path spends an account's balance
	// without the sender ever having been charged or checked.
	fromContract := mgr.WasmManager().HasContract(ctx, msg.FromAddress)
	if !fromContract && !TxSignedBy(ctx, msg.FromAddress) {
		return fmt.Errorf("send from %s was not signed by its sender", msg.FromAddress)
	}

	// The ante handler gates cosmos bank sends on BankSendEnabled, but it only
	// sees the top level messages of a transaction. Applying the switch here as
	// well, on the path every bank send takes, means no dispatch path can move
	// funds with bank sends turned off. Contracts are exempt: they move funds on
	// behalf of their users while executing an already permissioned contract.
	if _, isBankSend := m.(*bank.MsgSend); isBankSend && !fromContract {
		if mgr.Keeper().GetConfigInt64(ctx, constants.BankSendEnabled) <= 0 {
			return cosmos.ErrUnknownRequest("bank sends are disabled")
		}
	}

	k := mgr.Keeper()
	isThorModAddr := msg.ToAddress.Equals(k.GetModuleAccAddress(ModuleName))

	// Allow MsgSend with memo-based MsgDeposit is allowed to the thor module address
	if isThorModAddr {
		msgDeposit, err := getDeposit(ctx, msg.GetSigners()[0], msg.Amount)
		if err != nil {
			return err
		}
		return NewDepositHandler(mgr).validate(ctx, *msgDeposit)
	}
	// disallow sends to other modules, they should only be interacted with via deposit messages.
	if IsModuleAccAddress(k, msg.ToAddress) {
		return fmt.Errorf("cannot use MsgSend for Module transactions, use MsgDeposit instead")
	}
	// Having been confirmed to be for MsgSend and not MsgDeposit, do the Cosmos-SDK Coins IsValid check.
	// (This implicitly includes IsAllPositive.)
	if !msg.Amount.IsValid() {
		return cosmos.ErrInvalidCoins("coins must be valid")
	}
	return nil
}

func MsgSendHandle(ctx cosmos.Context, mgr Manager, m sdk.Msg) (*cosmos.Result, error) {
	msg, err := getThorSend(m)
	if err != nil {
		return nil, err
	}

	k := mgr.Keeper()

	if k.IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgSend while THORChain is halted")
	}

	// MsgSend to the thorchain module address is treated as a MsgDeposit for client compatibility reasons.
	// In this case, the memo will be used like in any other MsgDeposit.
	if msg.ToAddress.Equals(k.GetModuleAccAddress(ModuleName)) {
		msgDeposit, err := getDeposit(ctx, msg.FromAddress, msg.Amount)
		if err != nil {
			return nil, err
		}

		return NewDepositHandler(mgr).handle(ctx, *msgDeposit, 0)
	} else if err := k.SendCoins(ctx, msg.FromAddress, msg.ToAddress, msg.Amount); err != nil {
		return nil, err
	}

	return &cosmos.Result{}, nil
}
