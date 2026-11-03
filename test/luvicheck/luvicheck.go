// Package luvicheck watches pools block by block for losses in LP value, using
// the synth-aware LUVI index G (x/thorchain/types/type_luvi.go), and checks
// that LP units only change through add_liquidity and withdraw events. The
// regression and simulation harnesses share it.
package luvicheck

import (
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// DefaultTolerance is the largest per-block drop in G, normalized by the
// previous sqrt(A*R), that is treated as noise. Integer rounding in swaps
// and synth units moves G by well under 1e-15.
//
// On top of it, each pool gets computed allowances per block (see allowance)
// for gas and slash reimbursements, LP-flow rounding, and the error from
// valuing the block's LP flows at the previous block's unit value.
var DefaultTolerance = types.LuviDefaultTolerance

// PoolSnapshot is the subset of a /thorchain/pools entry the check needs.
type PoolSnapshot struct {
	Asset        string `json:"asset"`
	BalanceRune  string `json:"balance_rune"`
	BalanceAsset string `json:"balance_asset"`
	LPUnits      string `json:"LP_units"`
	PoolUnits    string `json:"pool_units"`
	SynthSupply  string `json:"synth_supply"`
}

// ToState converts the snapshot, taking pool units exactly as the chain
// reports them (synth-unit clamp included).
func (p PoolSnapshot) ToState() (types.LuviState, error) {
	var s types.LuviState
	fields := []struct {
		name string
		in   string
		out  *cosmos.Uint
	}{
		{"balance_rune", p.BalanceRune, &s.BalanceRune},
		{"balance_asset", p.BalanceAsset, &s.BalanceAsset},
		{"LP_units", p.LPUnits, &s.LPUnits},
		{"pool_units", p.PoolUnits, &s.PoolUnits},
		{"synth_supply", p.SynthSupply, &s.SynthSupply},
	}
	for _, f := range fields {
		in := f.in
		if in == "" {
			in = "0"
		}
		v, err := cosmos.ParseUint(in)
		if err != nil {
			return s, fmt.Errorf("pool %s: bad %s %q: %w", p.Asset, f.name, f.in, err)
		}
		*f.out = v
	}
	return s, nil
}

// ViolationKind classifies a Violation.
type ViolationKind string

const (
	// Drop means G fell by more than the tolerance in one block.
	Drop ViolationKind = "drop"
	// Ledger means LP units changed by a different amount than the block's
	// add_liquidity and withdraw events report.
	Ledger ViolationKind = "ledger"
)

// Violation is one failed check for one pool in one block.
type Violation struct {
	Pool             string
	Height           int64
	Kind             ViolationKind
	NormalizedDeltaG cosmos.Dec
	// Allowed is the drop that was allowed (tolerance plus allowances).
	Allowed cosmos.Dec
	Msg     string
}

func (v Violation) Error() string {
	if v.Kind == Drop {
		return fmt.Sprintf("luvi: %s dropped at height %d: dG/sqrt(AR) = %s (allowed -%s; %s)", v.Pool, v.Height, v.NormalizedDeltaG, v.Allowed, v.Msg)
	}
	return fmt.Sprintf("luvi: %s LP unit ledger mismatch at height %d: %s", v.Pool, v.Height, v.Msg)
}

type poolTracker struct {
	tracker *types.LuviTracker
	prev    types.LuviState
}

// Monitor tracks every pool across consecutive blocks. It must see every
// block, in order: G is only exact when each step covers one block.
type Monitor struct {
	Tolerance cosmos.Dec
	pools     map[string]*poolTracker
}

// NewMonitor returns a Monitor using DefaultTolerance.
func NewMonitor() *Monitor {
	return &Monitor{Tolerance: DefaultTolerance, pools: map[string]*poolTracker{}}
}

// ObserveBlock records the pools as of the end of a block, along with every
// event the block emitted, and returns the checks that failed. A pool is
// checked from the second block it is seen with non-zero depths and units. A
// tracked pool that disappears or empties gets a final check (see retire) and
// is then forgotten.
func (m *Monitor) ObserveBlock(height int64, pools []PoolSnapshot, events []map[string]string) ([]Violation, error) {
	ev := types.NewLuviStepEvents(events)
	added, withdrawn, errata, paid := ev.Added, ev.Withdrawn, ev.Errata, ev.AssetPaid

	var violations []Violation
	seen := map[string]bool{}
	for _, p := range pools {
		seen[p.Asset] = true
		state, err := p.ToState()
		if err != nil {
			return nil, err
		}
		pt := m.pools[p.Asset]
		if !state.IsValid() {
			if pt != nil {
				allowed := m.Tolerance.Add(retireAllowance(pt.prev, assetPaidOf(paid, p.Asset), len(added[p.Asset])+len(withdrawn[p.Asset])))
				violations = append(violations, retire(p.Asset, height, pt.prev, state, allowed, added, withdrawn, errata[p.Asset])...)
				delete(m.pools, p.Asset)
			}
			continue
		}
		if pt == nil {
			var tracker *types.LuviTracker
			tracker, err = types.NewLuviTracker(state)
			if err != nil {
				return nil, err
			}
			m.pools[p.Asset] = &poolTracker{tracker: tracker, prev: state}
			continue
		}

		if !errata[p.Asset] {
			if err = types.CheckLPUnitLedger(pt.prev.LPUnits, state.LPUnits, added[p.Asset], withdrawn[p.Asset]); err != nil {
				violations = append(violations, Violation{
					Pool: p.Asset, Height: height, Kind: Ledger, Msg: err.Error(),
				})
			}
		}

		step, err := pt.tracker.Observe(state)
		if err != nil {
			return nil, err
		}
		prevState := pt.prev
		allowed := m.Tolerance.Add(allowance(prevState, state, assetPaidOf(paid, p.Asset), len(added[p.Asset])+len(withdrawn[p.Asset])))
		pt.prev = state
		if step.NormalizedDeltaG.LT(allowed.Neg()) {
			violations = append(violations, Violation{
				Pool: p.Asset, Height: height, Kind: Drop, NormalizedDeltaG: step.NormalizedDeltaG, Allowed: allowed,
				Msg: fmt.Sprintf("rune %s->%s asset %s->%s LP units %s->%s synths %s->%s",
					prevState.BalanceRune, state.BalanceRune, prevState.BalanceAsset, state.BalanceAsset,
					prevState.LPUnits, state.LPUnits, prevState.SynthSupply, state.SynthSupply),
			})
		}
	}
	for asset, pt := range m.pools {
		if !seen[asset] {
			// the pools endpoint drops pools with zero LP units
			gone := types.LuviState{
				BalanceRune: cosmos.ZeroUint(), BalanceAsset: cosmos.ZeroUint(), LPUnits: cosmos.ZeroUint(),
				PoolUnits: cosmos.ZeroUint(), SynthSupply: cosmos.ZeroUint(),
			}
			allowed := m.Tolerance.Add(retireAllowance(pt.prev, assetPaidOf(paid, asset), len(added[asset])+len(withdrawn[asset])))
			violations = append(violations, retire(asset, height, pt.prev, gone, allowed, added, withdrawn, errata[asset])...)
			delete(m.pools, asset)
		}
	}

	sort.SliceStable(violations, func(i, j int) bool { return violations[i].Pool < violations[j].Pool })
	return violations, nil
}

// retire checks the last block of a tracked pool that has emptied or
// disappeared (state is all zero when it disappeared). LP units must still match
// the block's unit events, unless an errata event names the pool (a
// staged-pool burn and ragnarok both remove LPs through withdraw events, so a
// pool status event excuses nothing). And if either depth is gone while LP
// units remain, G has lost the whole sqrt(A*R):
// a Drop of -1 unless the block's allowance covers it, as it does when gas or
// a slash pays out the whole asset depth. The harnesses treat any other such
// drop like a live one (excused by a loss event in simulation, by luvi_drop
// in regression).
func retire(asset string, height int64, prev, state types.LuviState, allowed cosmos.Dec, added, withdrawn map[string][]cosmos.Uint, ledgerExempt bool) []Violation {
	var violations []Violation
	units := state.LPUnits
	if !ledgerExempt {
		if err := types.CheckLPUnitLedger(prev.LPUnits, units, added[asset], withdrawn[asset]); err != nil {
			violations = append(violations, Violation{
				Pool: asset, Height: height, Kind: Ledger, Msg: err.Error(),
			})
		}
	}
	depleted := !units.IsZero() && (state.BalanceRune.IsZero() || state.BalanceAsset.IsZero())
	if depleted && cosmos.NewDec(-1).LT(allowed.Neg()) {
		violations = append(violations, Violation{
			Pool: asset, Height: height, Kind: Drop, NormalizedDeltaG: cosmos.NewDec(-1), Allowed: allowed,
			Msg: fmt.Sprintf("depleted with LP units outstanding: rune %s->%s asset %s->%s LP units %s->%s",
				prev.BalanceRune, state.BalanceRune, prev.BalanceAsset, state.BalanceAsset, prev.LPUnits, units),
		})
	}
	return violations
}

// allowance is types.LuviAllowance; see there for what it covers.
func allowance(prev, next types.LuviState, assetPaid cosmos.Uint, flows int) cosmos.Dec {
	return types.LuviAllowance(prev, next, assetPaid, flows)
}

// retireAllowance is allowance without the LP flow timing term, which values
// flows against a unit value that an emptied pool no longer has.
func retireAllowance(prev types.LuviState, assetPaid cosmos.Uint, flows int) cosmos.Dec {
	return allowance(prev, prev, assetPaid, flows)
}

func assetPaidOf(paid map[string]cosmos.Uint, asset string) cosmos.Uint {
	if g, ok := paid[asset]; ok {
		return g
	}
	return cosmos.ZeroUint()
}

// LossEventPools returns the pools named by events that can legitimately
// lower G: errata, vault slashes, gas reimbursement, pool status changes and
// direct pool balance changes (staged-pool cost and similar).
func LossEventPools(events []map[string]string) map[string]bool {
	pools := map[string]bool{}
	for _, e := range events {
		switch e["type"] {
		case types.ErrataEventType, types.GasEventType, types.PoolBalanceChangeEventType:
			pools[e["asset"]] = true
		case types.SlashEventType, types.PoolEventType:
			pools[e["pool"]] = true
		}
	}
	return pools
}
