package thorchain

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Store migrations run once, at an upgrade height, and can rewrite pool
// depths, LP units and synth supply directly. Every registered x/thorchain
// migration runs inside a report-only LUVI scope named migration/<from>to<to>
// (see luviGuardMigration), so a migration that lowers any pool's hedged LP
// equity G emits an EventLuviLoss and logs a per-pool summary. The scope never
// fails, reverts or halts the upgrade, and the LUVIGuard Mimir turns it off.
//
// The CI gate (luvi_migration_test.go) runs each registered migration against
// a fixture state and fails on any undeclared drop: a migration that moves LP
// value on purpose must say so in luviDeclaredWriteDowns.

// luviMigration is one registered x/thorchain store migration.
type luviMigration struct {
	from uint64
	run  func(sdk.Context) error
}

// luviMigrations lists the migrations RegisterServices registers, in order.
// Add a new migration here, not directly in RegisterServices, so it runs under
// the LUVI scope and the CI gate covers it.
func (m Migrator) luviMigrations() []luviMigration {
	return []luviMigration{
		{15, m.Migrate15to16},
		{16, m.Migrate16to17},
		{17, m.Migrate17to18},
		{18, m.Migrate18to19},
		{19, m.Migrate19to20},
		{20, m.Migrate20to21},
	}
}

// luviMigrationScopeName is the LUVI scope (and allowlist key) of the
// migration from version from to from+1.
func luviMigrationScopeName(from uint64) string {
	return fmt.Sprintf("migration/%dto%d", from, from+1)
}

// luviDeclaredWriteDown is a pool LP-value drop a migration makes on purpose.
type luviDeclaredWriteDown struct {
	// MaxDrop is the largest normalized drop in G (dG / sqrt(A*R), as a
	// positive number) the migration may make in the pool.
	MaxDrop cosmos.Dec
	// Reason is a one-line justification for the reviewer.
	Reason string
}

// luviDeclaredWriteDowns lists, per migration scope and pool, the LP-value
// drops a migration is allowed to make. The CI gate fails on any other drop
// beyond the LUVI tolerance. An entry here is a statement that LPs of that pool
// lose value in the upgrade: keep it as small as the migration needs, and say
// why.
//
// The bounds are normalized drops at the CI fixtures' depths (mainnet pools
// on 2026-10-25, luvi_migration_test.go and luvi_migration_mainnet_test.go);
// a fixed amount written off a pool is a larger share of a shallower pool.
var luviDeclaredWriteDowns = map[string]map[string]luviDeclaredWriteDown{
	// ADR-028 phantom-depth write-down (executed on mainnet at h27,580,000):
	// removes a pinned asset amount, and RUNE at constant price, that the
	// vaults no longer backed. 19.6% (DOGE) and 21.3% (GAIA) of today's depth.
	"migration/17to18": {
		"DOGE.DOGE": {MaxDrop: cosmos.NewDecWithPrec(25, 2), Reason: "ADR-028 write-down of unbacked DOGE depth to vault backing"},
		"GAIA.ATOM": {MaxDrop: cosmos.NewDecWithPrec(25, 2), Reason: "ADR-028 write-down of unbacked GAIA.ATOM depth to vault backing"},
	},
	// Slash reversals (issue 3393): each takes back the RUNE an erroneous slash
	// credited the pool and returns the asset it debited, at today's price.
	"migration/19to20": {
		// 6,140.85 RUNE out, 30.64 SOL back, worth ~5,400 RUNE: -0.53%
		"SOL.SOL": {MaxDrop: cosmos.NewDecWithPrec(75, 4), Reason: "h27591961 phantom SOL slash reversal returns less SOL than the RUNE it reclaims"},
		// 6.79 RUNE out, the 14.03 TRX gas stays debited: -0.16 bp
		"TRON.TRX": {MaxDrop: cosmos.NewDecWithPrec(3, 5), Reason: "h28172517 TRON slash reversal reclaims the gas reimbursement; the gas was real"},
	},
}

// luviDeclaredDrop returns the drop declared for a migration scope and pool.
func luviDeclaredDrop(scope string, pool common.Asset) (luviDeclaredWriteDown, bool) {
	d, ok := luviDeclaredWriteDowns[scope][pool.String()]
	return d, ok
}

// luviGuardMigration wraps a migration so it runs inside a report-only LUVI
// scope. It returns the migration's own result unchanged: the scope only
// emits events and logs.
func luviGuardMigration(mgr *Mgrs, from uint64, run func(sdk.Context) error) func(sdk.Context) error {
	name := luviMigrationScopeName(from)
	return func(ctx sdk.Context) error {
		k := mgr.Keeper()
		scopeCtx, scope := beginLuviMigrationScope(ctx, k, name)
		if err := run(scopeCtx); err != nil {
			// the upgrade fails and its writes are discarded: nothing to report
			return err
		}
		scope.endMigration(scopeCtx, k)
		return nil
	}
}

// beginLuviMigrationScope starts a report-only scope and records every
// existing pool in its journal up front. The journal otherwise only sees
// pools written through the keeper; a migration may also write pool records
// or synth balances directly (raw store keys, bank sends), so the scope
// compares every pool's state before and after instead.
func beginLuviMigrationScope(ctx cosmos.Context, k keeper.Keeper, name string) (cosmos.Context, *luviScope) {
	ctx, scope := beginLuviScope(ctx, k, name, "", false)
	if scope == nil {
		return ctx, nil
	}
	var assets []common.Asset
	iter := k.GetPoolIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var pool Pool
		if err := k.Cdc().Unmarshal(iter.Value(), &pool); err != nil {
			ctx.Logger().Error("luvi: fail to read pool before migration", "scope", name, "key", string(iter.Key()), "error", err)
			continue
		}
		assets = append(assets, pool.Asset)
	}
	iter.Close()
	capture := func(asset common.Asset) types.LuviState { return luviPoolState(ctx, k, asset) }
	for _, asset := range assets {
		keeper.RecordLuviTouch(ctx, asset, capture)
	}
	return ctx, scope
}

// luviMigrationChange is one pool's state across a migration.
type luviMigrationChange struct {
	Pool common.Asset
	Pre  types.LuviState
	Post types.LuviState
	// NormalizedDeltaG is dG / sqrt(A*R) of the pre-state; -1 when the pool
	// had LP value and lost a depth or all its LP units (record removed or
	// emptied), and zero for a pool that had no LP value to lose.
	NormalizedDeltaG cosmos.Dec
}

// changes returns the scope's pools whose state differs from their state
// before the scope, in first-touch order, with each one's normalized step in G.
func (s *luviScope) changes(ctx cosmos.Context, k keeper.Keeper) []luviMigrationChange {
	if s == nil {
		return nil
	}
	var out []luviMigrationChange
	for _, asset := range s.journal.Assets() {
		pre, _ := s.journal.Pre(asset)
		post := luviPoolState(ctx, k, asset)
		if luviStatesEqual(pre, post) {
			continue
		}
		ch := luviMigrationChange{Pool: asset, Pre: pre, Post: post, NormalizedDeltaG: cosmos.ZeroDec()}
		switch {
		case !pre.IsValid():
			// a new or empty pool had no LP value to lose
		case !post.IsValid():
			ch.NormalizedDeltaG = cosmos.NewDec(-1)
		default:
			tracker, err := types.NewLuviTracker(pre)
			if err != nil {
				break
			}
			if step, err := tracker.Observe(post); err == nil {
				ch.NormalizedDeltaG = step.NormalizedDeltaG
			}
		}
		out = append(out, ch)
	}
	return out
}

// endMigration reports the migration's LUVI step: the scope's loss events
// (as for any other scope), a loss for a pool whose LPs lost everything (which
// the generic check skips once the LP units are gone), and one log line per
// changed pool. It writes no state beyond the events.
func (s *luviScope) endMigration(ctx cosmos.Context, k keeper.Keeper) {
	if s == nil {
		return
	}
	losses, _ := s.check(ctx, k)
	for _, ch := range s.changes(ctx, k) {
		declared, isDeclared := luviDeclaredDrop(s.name, ch.Pool)
		drop := ch.NormalizedDeltaG.LT(types.LuviDefaultTolerance.Neg())
		if ch.Pre.IsValid() && !ch.Post.IsValid() && ch.Post.LPUnits.IsZero() {
			zero := cosmos.ZeroDec()
			losses = append(losses, &types.EventLuviLoss{
				Pool:             ch.Pool,
				Scope:            s.name,
				DeltaG:           ch.Pre.SqrtAR().Neg(),
				NormalizedDeltaG: ch.NormalizedDeltaG,
				Allowed:          zero,
				Kind:             "removed",
				Excess:           true,
				LpUnitsBefore:    ch.Pre.LPUnits,
				LpUnitsAfter:     ch.Post.LPUnits,
			})
		}
		maxDrop := cosmos.ZeroDec()
		if isDeclared {
			maxDrop = declared.MaxDrop
		}
		log := ctx.Logger().Info
		if drop && (!isDeclared || ch.NormalizedDeltaG.Neg().GT(maxDrop)) {
			log = ctx.Logger().Error
		}
		log("luvi: migration changed pool",
			"scope", s.name, "pool", ch.Pool,
			"rune_before", ch.Pre.BalanceRune, "rune_after", ch.Post.BalanceRune,
			"asset_before", ch.Pre.BalanceAsset, "asset_after", ch.Post.BalanceAsset,
			"lp_units_before", ch.Pre.LPUnits, "lp_units_after", ch.Post.LPUnits,
			"synth_before", ch.Pre.SynthSupply, "synth_after", ch.Post.SynthSupply,
			"normalized_delta_g", ch.NormalizedDeltaG, "declared", isDeclared, "declared_max_drop", maxDrop)
	}
	for _, l := range losses {
		events, err := l.Events()
		if err != nil {
			ctx.Logger().Error("fail to build luvi loss event", "scope", s.name, "error", err)
			continue
		}
		// emitted directly: in the upgrade's PreBlock the managers (and the
		// event manager) may not be loaded yet
		ctx.EventManager().EmitEvents(events)
	}
}
