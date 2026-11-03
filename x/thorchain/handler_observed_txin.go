package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// ObservedTxInHandler to handle MsgObservedTxIn
type ObservedTxInHandler struct {
	mgr Manager
}

// NewObservedTxInHandler create a new instance of ObservedTxInHandler
func NewObservedTxInHandler(mgr Manager) ObservedTxInHandler {
	return ObservedTxInHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of ObservedTxInHandler
func (h ObservedTxInHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgObservedTxIn)
	if !ok {
		return nil, errInvalidMessage
	}
	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgObservedTxIn failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgObservedTxIn message", "error", err)
	}
	return result, err
}

func (h ObservedTxInHandler) validate(ctx cosmos.Context, msg MsgObservedTxIn) error {
	// ValidateBasic is also executed in message service router's handler and isn't versioned there
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if !isSignedByActiveNodeAccounts(ctx, h.mgr.Keeper(), msg.GetSigners()) {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%+v are not authorized", msg.GetSigners()))
	}

	return nil
}

func (h ObservedTxInHandler) handle(ctx cosmos.Context, msg MsgObservedTxIn) (*cosmos.Result, error) {
	activeNodeAccounts, err := h.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}
	handler := NewInternalHandler(h.mgr)
	for _, tx := range msg.Txs {
		voter, err := ensureVaultAndGetTxInVoter(ctx, tx.ObservedPubKey, tx.Tx.ID, h.mgr.Keeper())
		if err != nil {
			ctx.Logger().Error("fail to ensure vault and get tx in voter", "error", err)
			// No counter here: ensureVaultAndGetTxInVoter already emits
			// thornode.observation.dropped with an accurate reason label on both
			// of its error branches. Counting again here would double-count and
			// would mislabel the voter-read failure as an unknown vault.
			continue
		}

		voter, isQuorum := processTxInAttestation(ctx, h.mgr, voter, activeNodeAccounts, tx, msg.Signer, true)
		if err := handleObservedTxInQuorum(ctx, h.mgr, msg.Signer, activeNodeAccounts, handler, tx, voter, voter.Tx.GetSigners(), isQuorum); err != nil {
			ctx.Logger().Error("fail to handle observed tx in quorum", "error", err, "tx", tx.Tx.ID)
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observed_tx", "handling_failed"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))},
			)
		}
	}
	return &cosmos.Result{}, nil
}

func addSwap(ctx cosmos.Context, mgr Manager, msg MsgSwap) error {
	// Route swap based on message version instead of configuration
	if msg.IsV2() {
		// TODO: swap to synth if layer1 asset (follow on PR)
		// TODO: create handler to modify/cancel a limit swap (follow on PR)
		if err := mgr.AdvSwapQueueMgr().AddSwapQueueItem(ctx, mgr, &msg); err != nil {
			ctx.Logger().Error("fail to add swap to queue", "error", err)
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "swap_queue", "add_failed"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "adv_queue")},
			)
			return err
		}
		if msg.IsLimitSwap() {
			source := msg.Tx.Coins[0]
			target := common.NewCoin(msg.TargetAsset, msg.TradeTarget)
			evt := NewEventLimitSwap(source, target, msg.Tx.ID)
			if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
				ctx.Logger().Error("fail to emit swap event", "error", err)
			}
		}
	} else {
		return addSwapDirect(ctx, mgr.Keeper(), msg)
	}
	return nil
}

// addSwapDirect adds the swap directly to the swap queue (no order book) - segmented
// out into its own function to allow easier maintenance of original behavior vs order
// book behavior.
func addSwapDirect(ctx cosmos.Context, k keeper.Keeper, msg MsgSwap) error {
	if msg.Tx.Coins.IsEmpty() {
		return nil
	}
	// Queue the main swap
	if err := k.SetSwapQueueItem(ctx, msg, 0); err != nil {
		ctx.Logger().Error("fail to add swap to queue", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "swap_queue", "add_failed"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", "direct")},
		)
		return err
	}
	return nil
}

// ObservedTxInAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func ObservedTxInAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgObservedTxIn) (cosmos.Context, error) {
	return activeNodeAccountsSignerPriority(ctx, k, msg.GetSigners())
}
