package keeper

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type luviJournalKey struct{}

// LuviJournal records, for one LUVI scope (a handler run or an EndBlock step),
// which layer-1 pools were written and their state just before the first
// write: the pool record and the synth supply. A scope's LUVI step is then
// that pre-state against the pool's state when the scope ends.
//
// Journals nest: a scope opened inside another (an inner handler dispatched
// by an outer one) also records into every enclosing journal that has not seen
// the pool yet, so each scope's pre-state is the state at its own first write.
//
// The journal lives on the cosmos.Context, so concurrent queries never share
// one, and capture order follows execution order, which is deterministic.
type LuviJournal struct {
	parent *LuviJournal
	assets []common.Asset
	pre    map[string]types.LuviState
}

// WithLuviJournal attaches a new journal, nested in any journal ctx already
// carries.
func WithLuviJournal(ctx cosmos.Context) (cosmos.Context, *LuviJournal) {
	j := &LuviJournal{
		parent: LuviJournalFromContext(ctx),
		pre:    map[string]types.LuviState{},
	}
	return ctx.WithValue(luviJournalKey{}, j), j
}

// LuviJournalFromContext returns the innermost journal on ctx, or nil.
func LuviJournalFromContext(ctx cosmos.Context) *LuviJournal {
	j, _ := ctx.Value(luviJournalKey{}).(*LuviJournal)
	return j
}

// Assets returns the touched pools in first-touch order.
func (j *LuviJournal) Assets() []common.Asset {
	if j == nil {
		return nil
	}
	return j.assets
}

// Pre returns the state captured before the scope's first write to the pool.
// A pool that did not exist (or was empty) then has an invalid state.
func (j *LuviJournal) Pre(asset common.Asset) (types.LuviState, bool) {
	if j == nil {
		return types.LuviState{}, false
	}
	s, ok := j.pre[asset.String()]
	return s, ok
}

// LuviTrackedAsset maps a pool or synth asset to the layer-1 pool LUVI tracks,
// or reports false for pools LUVI does not cover.
func LuviTrackedAsset(asset common.Asset) (common.Asset, bool) {
	if asset.IsSyntheticAsset() {
		asset = asset.GetLayer1Asset()
	}
	if asset.IsEmpty() || asset.IsRune() || asset.IsDerivedAsset() || asset.IsSyntheticAsset() ||
		asset.IsTradeAsset() || asset.IsSecuredAsset() {
		return common.EmptyAsset, false
	}
	return asset, true
}

// RecordLuviTouch is called by the store before it writes a pool or changes a
// synth supply; capture must read the current, pre-write state. It only runs
// when a journal on ctx has not seen the pool yet, so contexts without a
// journal pay no extra store reads.
func RecordLuviTouch(ctx cosmos.Context, asset common.Asset, capture func(common.Asset) types.LuviState) {
	j := LuviJournalFromContext(ctx)
	if j == nil {
		return
	}
	asset, ok := LuviTrackedAsset(asset)
	if !ok {
		return
	}
	key := asset.String()
	var state *types.LuviState
	for ; j != nil; j = j.parent {
		if _, seen := j.pre[key]; seen {
			// enclosing journals saw it no later than this one
			return
		}
		if state == nil {
			s := capture(asset)
			state = &s
		}
		j.pre[key] = *state
		j.assets = append(j.assets, asset)
	}
}
