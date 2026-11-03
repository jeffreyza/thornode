package thorchain

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// EmitEventItem define the method all event need to implement
type EmitEventItem interface {
	Events() (cosmos.Events, error)
}

// EventMgr implement EventManager interface
type EventMgr struct{}

// newEventMgr create a new instance of EventMgr
func newEventMgr() *EventMgr {
	return &EventMgr{}
}

// EmitEvent to block
func (m *EventMgr) EmitEvent(ctx cosmos.Context, evt EmitEventItem) error {
	events, err := evt.Events()
	if err != nil {
		return fmt.Errorf("fail to get events: %w", err)
	}
	ctx.EventManager().EmitEvents(events)
	if !isSimulationMode(ctx) {
		// Event types are a closed set of package constants, so the label is
		// bounded. Two limits are deliberate, and this counts emissions rather
		// than commits - do not read it as a census of on-chain events:
		//   - Not commit-aware. It fires when the event enters ctx, which may be a
		//     CacheContext that is later discarded (handler_switch.go emits
		//     before a fallible step and skips commit()
		//     on failure). A process-local counter cannot roll back with the store.
		//   - Not complete. The sites that call ctx.EventManager().EmitEvent
		//     directly bypass EventMgr entirely and are invisible here: new_node
		//     (handler_bond.go), propose_upgrade/approve_upgrade
		//     (handler_upgrade.go), the vault lifecycle events
		//     (manager_network_current.go), and the node-detail handlers.
		// Making it commit-aware means rebuilding the census on the per-tx
		// abci.TxResults events in app.FinalizeBlock and parsing raw attributes -
		// new plumbing for a metric whose job is trend and anomaly detection.
		for _, e := range events {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "event", "emitted"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("type", e.Type)},
			)
		}
		// mint/burn of native supply - every production emit site routes through
		// EmitEvent, and every Reason is a compile-time literal
		if mb, ok := evt.(*types.EventMintBurn); ok && !mb.Amount.IsNil() {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "mint_burn", "amount"},
				telem(mb.Amount),
				[]metrics.Label{
					telemetry.NewLabel("supply", mb.Supply.String()),
					telemetry.NewLabel("asset", mb.Denom),
					telemetry.NewLabel("reason", mb.Reason),
				},
			)
		}
	}
	return nil
}

// EmitGasEvent emit gas events
func (m *EventMgr) EmitGasEvent(ctx cosmos.Context, gasEvent *EventGas) error {
	if gasEvent == nil {
		return nil
	}
	return m.EmitEvent(ctx, gasEvent)
}

// EmitSwapEvent emit swap event to block
func (m *EventMgr) EmitSwapEvent(ctx cosmos.Context, swap *EventSwap) error {
	// OutTxs is a temporary field that we used, as for now we need to keep backward compatibility so the
	// events change doesn't break midgard and smoke test, for double swap , we first swap the source asset to RUNE ,
	// and then from RUNE to target asset, so the first will be marked as success
	if !swap.OutTxs.IsEmpty() {
		outboundEvt := NewEventOutbound(swap.InTx.ID, swap.OutTxs)
		if err := m.EmitEvent(ctx, outboundEvt); err != nil {
			return fmt.Errorf("fail to emit an outbound event for double swap: %w", err)
		}
	}
	return m.EmitEvent(ctx, swap)
}

// EmitFeeEvent emit a fee event through event manager
func (m *EventMgr) EmitFeeEvent(ctx cosmos.Context, feeEvent *EventFee) error {
	if feeEvent == nil {
		return nil
	}
	if feeEvent.Fee.Coins.IsEmpty() && feeEvent.Fee.PoolDeduct.IsZero() {
		return nil
	}
	events, err := feeEvent.Events()
	if err != nil {
		return fmt.Errorf("fail to emit fee event: %w", err)
	}
	ctx.EventManager().EmitEvents(events)
	// fee events bypass EmitEvent, so count them here or the "fee" type never
	// appears
	if !isSimulationMode(ctx) {
		for _, e := range events {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "event", "emitted"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("type", e.Type)},
			)
		}
	}
	return nil
}
