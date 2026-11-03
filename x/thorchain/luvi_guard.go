package thorchain

import (
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// The LUVI guard checks each handler run and each EndBlock step (a "scope")
// for drops in the hedged LP equity G of the pools it wrote (see
// types/type_luvi.go). Swaps are k-neutral and LP flows move G only by
// rounding, so a drop beyond the computed allowance (gas and slash
// reimbursement, LP-flow rounding) means value left LPs in a way the protocol
// does not intend. For user-initiated value flows (luviEnforcedMsgs) the
// handler is rejected and its changes reverted; everything else is reported
// with an EventLuviLoss. Drops the allowance covers (gas, slashes, rounding)
// are not reported; the per-pool accumulator still records them. The
// LUVIGuard Mimir (default 1) turns both off.

var errLuviGuard = errors.New("luvi guard: pool LP value dropped")

// luviEnforcedMsgs are the messages a user can send or trigger directly, whose
// handlers the guard rejects on an excess drop. Protocol-driven messages
// (outbounds, refunds, migrations, observations) are reported only: rejecting
// them would stall the chain's own bookkeeping. So are these message types
// when the protocol sends them itself (POL, ragnarok, saver ejects, pending
// liquidity commits) through newLuviProtocolHandler.
var luviEnforcedMsgs = map[string]bool{
	sdk.MsgTypeURL(&MsgSwap{}):                   true,
	sdk.MsgTypeURL(&MsgAddLiquidity{}):           true,
	sdk.MsgTypeURL(&MsgWithdrawLiquidity{}):      true,
	sdk.MsgTypeURL(&MsgDonate{}):                 true,
	sdk.MsgTypeURL(&MsgModifyLimitSwap{}):        true,
	sdk.MsgTypeURL(&MsgSwitch{}):                 true,
	sdk.MsgTypeURL(&MsgTradeAccountDeposit{}):    true,
	sdk.MsgTypeURL(&MsgTradeAccountWithdrawal{}): true,
	sdk.MsgTypeURL(&MsgSecuredAssetDeposit{}):    true,
	sdk.MsgTypeURL(&MsgSecuredAssetWithdraw{}):   true,
	sdk.MsgTypeURL(&MsgRunePoolDeposit{}):        true,
	sdk.MsgTypeURL(&MsgRunePoolWithdraw{}):       true,
	sdk.MsgTypeURL(&MsgTCYStake{}):               true,
	sdk.MsgTypeURL(&MsgTCYUnstake{}):             true,
	sdk.MsgTypeURL(&MsgTCYClaim{}):               true,
}

// luviProtocolKey marks a context whose handler runs a message the protocol
// sent itself (newLuviProtocolHandler), which the guard reports but never
// rejects.
type luviProtocolKey struct{}

// newLuviProtocolHandler is NewInternalHandler for the messages managers send
// on the protocol's behalf. A ragnarok withdraw may drain one side of a pool
// on purpose, so rejecting a loss there would stall the wind-down.
func newLuviProtocolHandler(mgr Manager) cosmos.Handler {
	handler := NewInternalHandler(mgr)
	return func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
		return handler(ctx.WithValue(luviProtocolKey{}, true), msg)
	}
}

func isLuviProtocolContext(ctx cosmos.Context) bool {
	protocol, _ := ctx.Value(luviProtocolKey{}).(bool)
	return protocol
}

// luviScope is one checked unit of work.
type luviScope struct {
	name       string
	txID       common.TxID
	enforce    bool
	journal    *keeper.LuviJournal
	eventsFrom int
}

// luviGuardEnabled reports whether the LUVIGuard Mimir (or constant) is on.
func luviGuardEnabled(ctx cosmos.Context, k keeper.Keeper) bool {
	return k.GetConfigInt64(ctx, constants.LUVIGuard) > 0
}

// beginLuviScope starts a scope and returns the context the work must run
// in. It returns ctx unchanged and a nil scope (whose methods are no-ops) when
// the guard is off. Events already on ctx are not part of the scope.
func beginLuviScope(ctx cosmos.Context, k keeper.Keeper, name string, txID common.TxID, enforce bool) (cosmos.Context, *luviScope) {
	if !luviGuardEnabled(ctx, k) {
		return ctx, nil
	}
	ctx, j := keeper.WithLuviJournal(ctx)
	return ctx, &luviScope{
		name:       name,
		txID:       txID,
		enforce:    enforce,
		journal:    j,
		eventsFrom: len(ctx.EventManager().Events()),
	}
}

// beginLuviMsgScope starts the scope of one handler run.
func beginLuviMsgScope(ctx cosmos.Context, k keeper.Keeper, msg cosmos.Msg) (cosmos.Context, *luviScope) {
	name := sdk.MsgTypeURL(msg)
	return beginLuviScope(ctx, k, name, luviMsgTxID(msg), luviEnforcedMsgs[name] && !isLuviProtocolContext(ctx))
}

func luviMsgTxID(msg cosmos.Msg) common.TxID {
	switch m := msg.(type) {
	case interface{ GetTx() common.Tx }:
		return m.GetTx().ID
	case interface{ GetTx() common.ObservedTx }:
		return m.GetTx().Tx.ID
	}
	return ""
}

// luviPoolState reads a layer-1 pool's current LUVI state.
func luviPoolState(ctx cosmos.Context, k keeper.Keeper, asset common.Asset) types.LuviState {
	pool, err := k.GetPool(ctx, asset)
	if err != nil {
		// an unreadable pool counts as empty
		return types.EmptyLuviState()
	}
	return types.NewLuviState(pool, k.GetTotalSupply(ctx, asset.GetSyntheticAsset()))
}

// check computes the scope's LUVI step for every pool it wrote, as of ctx (the
// context the work ran in). It returns the loss events to emit and whether any
// of them is an excess the guard enforces.
func (s *luviScope) check(ctx cosmos.Context, k keeper.Keeper) ([]*types.EventLuviLoss, bool) {
	if s == nil {
		return nil, false
	}
	all := ctx.EventManager().Events()
	var scoped cosmos.Events
	if s.eventsFrom <= len(all) {
		scoped = all[s.eventsFrom:]
	}
	ev := types.NewLuviStepEvents(types.FlattenLuviEvents(scoped))

	var losses []*types.EventLuviLoss
	excess := false
	virtualSynthDepth := int64(-1) // read lazily: only steps that move synths need it
	for _, asset := range s.journal.Assets() {
		pre, _ := s.journal.Pre(asset)
		if !pre.IsValid() {
			// a new or empty pool has no LP value to lose
			continue
		}
		post := luviPoolState(ctx, k, asset)
		pool := asset.String()
		// A virtual synth depth above 1x prices synth swaps against a deeper
		// pool than the real one: a configured subsidy from LPs to synth
		// traders (mocknet only), which the allowance does not model. Skip
		// the pool rather than report a loss the configuration intends.
		if !pre.SynthSupply.Equal(post.SynthSupply) {
			if virtualSynthDepth < 0 {
				virtualSynthDepth = k.GetConfigInt64(ctx, constants.VirtualMultSynthsBasisPoints)
			}
			if virtualSynthDepth > int64(constants.MaxBasisPts) {
				continue
			}
		}
		loss := func(kind string, dG, norm, allowed cosmos.Dec, over bool) {
			// an inner scope (a handler run inside this EndBlock step) already
			// reported this pool: repeat it only if this step is the first to
			// find an excess
			if ev.Reported[pool] && (!over || ev.ReportedExcess[pool]) {
				excess = excess || over
				return
			}
			losses = append(losses, &types.EventLuviLoss{
				Pool:             asset,
				Scope:            s.name,
				TxID:             s.txID,
				DeltaG:           dG,
				NormalizedDeltaG: norm,
				Allowed:          allowed,
				Kind:             kind,
				Excess:           over,
				LpUnitsBefore:    pre.LPUnits,
				LpUnitsAfter:     post.LPUnits,
			})
			excess = excess || over
		}

		if !ev.Errata[pool] {
			if err := types.CheckLPUnitLedger(pre.LPUnits, post.LPUnits, ev.Added[pool], ev.Withdrawn[pool]); err != nil {
				zero := cosmos.ZeroDec()
				loss("ledger", zero, zero, zero, true)
			}
		}

		tolerance := types.LuviDefaultTolerance
		if !post.IsValid() {
			// with every LP gone there is nothing left to lose; with LP units
			// still outstanding but a depth gone, G has lost all of sqrt(A*R)
			if post.LPUnits.IsZero() {
				continue
			}
			allowed := tolerance.Add(types.LuviAllowance(pre, pre, ev.AssetPaidOf(pool), ev.RoundingOps(pool)))
			minusOne := cosmos.NewDec(-1)
			if minusOne.LT(allowed.Neg()) {
				loss("drop", pre.SqrtAR().Neg(), minusOne, allowed, true)
			}
			continue
		}

		tracker, err := types.NewLuviTracker(pre)
		if err != nil {
			continue
		}
		step, err := tracker.Observe(post)
		if err != nil {
			continue
		}
		if step.NormalizedDeltaG.GTE(tolerance.Neg()) {
			continue
		}
		allowed := tolerance.Add(types.LuviAllowance(pre, post, ev.AssetPaidOf(pool), ev.RoundingOps(pool)))
		over := step.NormalizedDeltaG.LT(allowed.Neg())
		if over {
			loss("drop", step.DeltaG, step.NormalizedDeltaG, allowed, true)
		}
	}
	return losses, excess && s.enforce
}

// emitLuviLosses emits the loss events on ctx, marking them rejected when the
// scope's changes were reverted.
func emitLuviLosses(ctx cosmos.Context, mgr Manager, losses []*types.EventLuviLoss, rejected bool) {
	for _, l := range losses {
		l.Rejected = rejected && l.Excess
		if err := mgr.EventMgr().EmitEvent(ctx, l); err != nil {
			ctx.Logger().Error("fail to emit luvi loss event", "error", err)
		}
		if l.Excess {
			ctx.Logger().Error("luvi: pool LP value dropped beyond the allowance",
				"pool", l.Pool, "scope", l.Scope, "tx", l.TxID, "kind", l.Kind,
				"normalized_delta_g", l.NormalizedDeltaG, "allowed", l.Allowed, "rejected", l.Rejected)
		}
	}
}

// end finishes a report-only scope that ran directly in ctx (an EndBlock step).
func (s *luviScope) end(ctx cosmos.Context, mgr Manager) {
	if s == nil {
		return
	}
	losses, _ := s.check(ctx, mgr.Keeper())
	emitLuviLosses(ctx, mgr, losses, false)
}

// runLuviGuarded runs a handler in cacheCtx (a cache of parent) under a
// scope. On an enforced excess drop it returns an error, so the caller must
// not commit, and emits the loss events on parent; otherwise it emits them on
// cacheCtx, to be committed with the handler's other events.
func runLuviGuarded(parent, cacheCtx cosmos.Context, mgr Manager, msg cosmos.Msg, run func(cosmos.Context) error) error {
	runCtx, scope := beginLuviMsgScope(cacheCtx, mgr.Keeper(), msg)
	if err := run(runCtx); err != nil {
		return err
	}
	losses, reject := scope.check(runCtx, mgr.Keeper())
	if reject {
		emitLuviLosses(parent, mgr, losses, true)
		return fmt.Errorf("%w (%s)", errLuviGuard, scope.name)
	}
	emitLuviLosses(cacheCtx, mgr, losses, false)
	return nil
}
