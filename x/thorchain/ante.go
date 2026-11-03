package thorchain

import (
	math "math"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256r1"
	"github.com/cosmos/cosmos-sdk/crypto/types/multisig"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	ContextKeyTxMemo    = "tx_memo"
	ContextKeyTxSigners = "tx_signers"
	ActiveNodePriority  = int64(math.MaxInt64)
)

type AnteDecorator struct {
	keeper keeper.Keeper
}

func NewAnteDecorator(keeper keeper.Keeper) AnteDecorator {
	return AnteDecorator{
		keeper: keeper,
	}
}

func (ad AnteDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	if err = ad.rejectMultipleDepositMsgs(tx.GetMsgs()); err != nil {
		return ctx, err
	}

	if err = ad.rejectDuplicateTHORNameMsgs(tx.GetMsgs()); err != nil {
		return ctx, err
	}

	// TODO remove on hard fork, when all signers will be allowed (v47+)
	if err = ad.rejectInvalidSigners(tx); err != nil {
		return ctx, err
	}

	if mTx, ok := tx.(sdk.TxWithMemo); ok {
		ctx = ctx.WithValue(ContextKeyTxMemo, mTx.GetMemo())
	}

	// Record who signs the transaction body, so that handlers can tell a
	// directly signed message - one this ante handler has seen and gated - from
	// a message dispatched internally, which never passes through here.
	signers, err := txSigners(tx)
	if err != nil {
		return ctx, err
	}
	ctx = ctx.WithValue(ContextKeyTxSigners, signers)

	// run the message-specific ante for each msg, all must succeed
	version, _ := ad.keeper.GetVersionWithCtx(ctx)
	for _, msg := range tx.GetMsgs() {
		newCtx, err = ad.anteHandleMessage(ctx, version, msg)
		if err != nil {
			return ctx, err
		}
	}

	return next(newCtx, tx, simulate)
}

// txSigners returns the set of addresses required to sign the transaction body,
// keyed by their raw bytes. The bech32 form is deliberately not used: signer
// bytes come straight off the wire here, before any signature has been checked,
// and AccAddress.String() would both feed them to the SDK's global address
// cache and panic on anything it cannot encode.
func txSigners(tx sdk.Tx) (map[string]struct{}, error) {
	sigTx, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return nil, cosmos.ErrUnknownRequest("invalid transaction type")
	}
	signers, err := sigTx.GetSigners()
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(signers))
	for _, signer := range signers {
		set[string(signer)] = struct{}{}
	}
	return set, nil
}

// TxSignedBy reports whether addr signs the transaction body currently being
// executed. Messages dispatched from inside another message - by a CosmWasm
// contract, or by any message router that executes a message on behalf of its
// signer - are not signed by their sender and return false.
//
// The signer set is recorded by the ante handler, so a context that carries none
// never ran one. There is nothing to check against, and every address is taken
// to be a signer. Note this includes BeginBlock and EndBlock, which therefore
// return true rather than false. The paths relying on that fallback are:
//
//   - InitGenesis, and the unit and regression tests that build a context
//     directly instead of executing a transaction;
//   - the block lifecycle, notably the contract executions driven by the
//     x/scheduler EndBlocker. Nothing there dispatches a MsgSend from an address
//     that is not itself the executing contract, and contracts are checked by
//     ownership rather than by signature in MsgSendValidate. The deposit memo is
//     recorded by this same ante handler, so a send that reaches getDeposit from
//     a block hook carries no memo to execute either.
//
// TODO: marking trusted internal contexts explicitly would be a safer default
// than reading a missing signer set as authorization, and would let the block
// lifecycle be denied rather than exempted.
func TxSignedBy(ctx cosmos.Context, addr cosmos.AccAddress) bool {
	signers, ok := ctx.Context().Value(ContextKeyTxSigners).(map[string]struct{})
	if !ok {
		return true
	}
	_, found := signers[string(addr)]
	return found
}

// rejectInvalidSigners reject txs if they are signed with secp256r1 keys
func (ad AnteDecorator) rejectInvalidSigners(tx sdk.Tx) error {
	sigTx, okTx := tx.(authsigning.SigVerifiableTx)
	if !okTx {
		return cosmos.ErrUnknownRequest("invalid transaction type")
	}
	sigs, err := sigTx.GetSignaturesV2()
	if err != nil {
		return err
	}
	for _, sig := range sigs {
		pubkey := sig.PubKey
		switch pubkey := pubkey.(type) {
		case *secp256r1.PubKey:
			return cosmos.ErrUnknownRequest("secp256r1 keys not allowed")
		case multisig.PubKey:
			for _, pk := range pubkey.GetPubKeys() {
				if _, okPk := pk.(*secp256r1.PubKey); okPk {
					return cosmos.ErrUnknownRequest("secp256r1 keys not allowed")
				}
			}
		}
	}
	return nil
}

// rejectMultipleDepositMsgs only one deposit msg allowed per tx
func (ad AnteDecorator) rejectMultipleDepositMsgs(msgs []cosmos.Msg) error {
	hasDeposit := false
	for _, msg := range msgs {
		if ad.isDeposit(msg) {
			if hasDeposit {
				return cosmos.ErrUnknownRequest("only one deposit msg per tx")
			}
			hasDeposit = true
		}
	}
	return nil
}

// rejectDuplicateTHORNameMsgs prevents multiple MsgManageTHORName messages for the same name in a single tx
// This prevents exploitation where a second message could inherit state changes (like extended expiration)
// from the first message without paying for them
func (ad AnteDecorator) rejectDuplicateTHORNameMsgs(msgs []cosmos.Msg) error {
	seenNames := make(map[string]struct{})
	for _, msg := range msgs {
		if tnMsg, ok := msg.(*types.MsgManageTHORName); ok {
			if _, exists := seenNames[tnMsg.Name]; exists {
				return cosmos.ErrUnknownRequest("duplicate THORName operation in same tx: " + tnMsg.Name)
			}
			seenNames[tnMsg.Name] = struct{}{}
		}
	}
	return nil
}

// isDeposit returns true if the msg is a deposit
func (ad AnteDecorator) isDeposit(msg cosmos.Msg) bool {
	switch m := msg.(type) {
	case *types.MsgDeposit:
		return true
	case *types.MsgSend:
		return m.ToAddress.Equals(ad.keeper.GetModuleAccAddress(ModuleName))
	case *banktypes.MsgSend:
		return m.ToAddress == ad.keeper.GetModuleAccAddress(ModuleName).String()
	default:
		return false
	}
}

// anteHandleMessage calls the msg-specific ante handling for a given msg
func (ad AnteDecorator) anteHandleMessage(ctx sdk.Context, version semver.Version, msg cosmos.Msg) (sdk.Context, error) {
	// ideally each handler would impl an ante func and we could instantiate
	// handlers and call ante, but handlers require mgr which is unavailable
	switch m := msg.(type) {

	// consensus handlers
	case *types.MsgBan:
		return BanAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgErrataTx:
		return ErrataTxAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgNetworkFee:
		return NetworkFeeAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgObservedTxIn:
		return ObservedTxInAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgObservedTxOut:
		return ObservedTxOutAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgSolvency:
		return SolvencyAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgSolNonceAttest:
		return SolNonceAttestAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgTssPool:
		return TssAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgTssKeysignFail:
		return TssKeysignFailAnteHandler(ctx, version, ad.keeper, *m)

	// cli handlers (non-consensus)
	case *types.MsgSetIPAddress:
		return IPAddressAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgMimir:
		return MimirAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgNodePauseChain:
		return NodePauseChainAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgSetNodeKeys:
		return SetNodeKeysAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgSetVersion:
		return VersionAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgProposeUpgrade, *types.MsgApproveUpgrade, *types.MsgRejectUpgrade:
		legacyMsg, ok := msg.(sdk.LegacyMsg)
		if !ok {
			return ctx, cosmos.ErrUnknownRequest("invalid message type")
		}
		return ActiveValidatorAnteHandler(ctx, version, ad.keeper, legacyMsg.GetSigners()[0])

	// native handlers (non-consensus)
	case *types.MsgDeposit:
		return DepositAnteHandler(ctx, version, ad.keeper, *m)
	case *types.MsgSend, *banktypes.MsgSend:
		_, ok := m.(*banktypes.MsgSend)
		if ok {
			enabled := ad.keeper.GetConfigInt64(ctx, constants.BankSendEnabled)
			if enabled <= 0 {
				return ctx, cosmos.ErrUnknownRequest("bank sends are disabled")
			}
		}
		return SendAnteHandler(ctx, version, ad.keeper, m)
	case *wasmtypes.MsgStoreCode,
		*wasmtypes.MsgInstantiateContract,
		*wasmtypes.MsgInstantiateContract2,
		*wasmtypes.MsgExecuteContract,
		*wasmtypes.MsgMigrateContract,
		*wasmtypes.MsgSudoContract,
		*wasmtypes.MsgUpdateAdmin,
		*wasmtypes.MsgClearAdmin:
		return ctx, nil
	default:
		return ctx, cosmos.ErrUnknownRequest("invalid message type")
	}
}

// InfiniteGasDecorator uses an infinite gas meter to prevent out-of-gas panics
// and allow non-versioned changes to be made without breaking consensus,
// as long as the resulting state is consistent.
type InfiniteGasDecorator struct {
	keeper keeper.Keeper
}

func NewGasDecorator(keeper keeper.Keeper) InfiniteGasDecorator {
	return InfiniteGasDecorator{
		keeper: keeper,
	}
}

func (d InfiniteGasDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	return next(ctx, tx, simulate)
}
