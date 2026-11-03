package thorchain

import (
	"fmt"
	"slices"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type DynamicFeeMgr struct {
	keeper keeper.Keeper
}

func newDynamicFeeMgr(keeper keeper.Keeper) *DynamicFeeMgr {
	return &DynamicFeeMgr{keeper: keeper}
}

func isL1Asset(asset common.Asset) bool {
	return !asset.IsSyntheticAsset() && !asset.IsTradeAsset() && !asset.IsDerivedAsset() && !asset.IsSecuredAsset()
}

func dynamicFeePairKey(source, target common.Asset) (string, string, bool) {
	if !isL1Asset(source) || !isL1Asset(target) {
		return "", "", false
	}
	var a, b string
	switch {
	case source.IsRune():
		a, b = types.NormalizePairAssets(target.String(), common.RuneAsset().String())
	case target.IsRune():
		a, b = types.NormalizePairAssets(source.String(), common.RuneAsset().String())
	default:
		a, b = types.NormalizePairAssets(source.String(), target.String())
	}
	return a, b, true
}

func dynamicFeeWhitelistState(ctx cosmos.Context, k keeper.Keeper, thorname string) int64 {
	key := fmt.Sprintf("DYNAMICFEE-WHITELIST-%s", strings.ToUpper(thorname))
	val, err := k.GetMimir(ctx, key)
	if err != nil {
		return 0
	}
	return val
}

// dominantEligibleAffiliate returns the largest-bps affiliate that is both an
// existing THORName and whose dynamic-fee whitelist state appears in
// allowedStates. Filtering ineligible entries out of the selection (rather
// than picking-then-rejecting) prevents an integrator from listing an
// unparsable / address-form / unwhitelisted secondary affiliate at higher
// bps to steer the dynamic-fee selector to "no match" and collapse the floor
// to static — see ADR §4 free-rider discussion. First-wins on ties matches
// the prior largest-bps-only behavior.
//
// Returns "", false when the inputs are malformed, empty, or no affiliate
// satisfies the eligibility predicate.
func (m *DynamicFeeMgr) dominantEligibleAffiliate(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, allowedStates ...int64) (string, bool) {
	if len(affiliates) == 0 || len(affiliates) != len(affiliateBps) {
		return "", false
	}
	bestIdx := -1
	bestBps := cosmos.ZeroUint()
	for i, bps := range affiliateBps {
		aff := affiliates[i]
		if aff == "" {
			continue
		}
		if !m.keeper.THORNameExists(ctx, aff) {
			continue
		}
		if !slices.Contains(allowedStates, dynamicFeeWhitelistState(ctx, m.keeper, aff)) {
			continue
		}
		if bps.GT(bestBps) {
			bestBps = bps
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return "", false
	}
	return affiliates[bestIdx], true
}

// CreditSwap accumulates volume and fees for a (thorname, pair) into the
// current-epoch accumulator. volumeTor and feesTor are denominated in TOR
// (1e8-precision USD value) so that the gradient signal does not drift with
// the RUNE/USD price.
func (m *DynamicFeeMgr) CreditSwap(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, sourceAsset, targetAsset common.Asset, volumeTor, feesTor cosmos.Uint) {
	enabled := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled)
	if enabled != 1 {
		return
	}

	pairA, pairB, ok := dynamicFeePairKey(sourceAsset, targetAsset)
	if !ok {
		return
	}

	// Allow whitelist state 1 (active) or 2 (monitor) so monitor-mode
	// thornames still accumulate the revenue signal without affecting
	// live fee charging.
	aff, ok := m.dominantEligibleAffiliate(ctx, affiliates, affiliateBps, 1, 2)
	if !ok {
		return
	}

	// Lazily create DynamicFeeRecord first, then write the accumulator —
	// DeleteAllDynamicFeeStateForThorname iterates records only and relies
	// on the invariant that every accumulator has a matching record. Writing
	// the record first ensures a record-write failure leaves no orphaned
	// accumulator behind.
	rec, err := m.keeper.GetDynamicFeeRecord(ctx, aff, pairA, pairB)
	if err != nil {
		ctx.Logger().Error("fail to get dynamic fee record", "error", err)
		return
	}
	if rec.DynamicBps == 0 && len(rec.History) == 0 {
		rec.DynamicBps = nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeFloorBPS))
		rec.Thorname = aff
		rec.PairAssetA = pairA
		rec.PairAssetB = pairB
		if setErr := m.keeper.SetDynamicFeeRecord(ctx, rec); setErr != nil {
			ctx.Logger().Error("fail to create dynamic fee record", "error", setErr)
			return
		}
	}

	affAcc, err := m.keeper.GetDynamicFeeAccumulator(ctx, aff, pairA, pairB)
	if err != nil {
		ctx.Logger().Error("fail to get thorname accumulator", "error", err)
		return
	}
	affAcc.VolumeTor = affAcc.VolumeTor.Add(volumeTor)
	affAcc.FeesTor = affAcc.FeesTor.Add(feesTor)
	if setErr := m.keeper.SetDynamicFeeAccumulator(ctx, affAcc); setErr != nil {
		ctx.Logger().Error("fail to set thorname accumulator", "error", setErr)
	}
}

// creditDynamicFeeForLegs credits the dynamic-fee accumulators for every
// L1↔RUNE leg in a swap event list. RUNE-denominated volume and fee values
// are converted to TOR (1e8-precision USD value) via DollarsPerRune so the
// gradient signal is independent of RUNE price moves.
//
// When DollarsPerRune is unavailable (no TOR anchors, or all halted), the
// entire credit is skipped: recording zeros would drag the window-mean fee
// signal toward zero and trigger spurious controller reversals on the next
// epoch boundary.
func creditDynamicFeeForLegs(ctx cosmos.Context, mgr Manager, swapEvents []*EventSwap, affiliates []string, affiliateBps []cosmos.Uint) {
	if len(affiliates) == 0 {
		return
	}
	dollarsPerRune := mgr.Keeper().DollarsPerRune(ctx)
	if dollarsPerRune.IsZero() {
		return
	}
	one := cosmos.NewUint(common.One)
	for _, evt := range swapEvents {
		if evt == nil || len(evt.InTx.Coins) == 0 {
			continue
		}
		legSource := evt.InTx.Coins[0].Asset
		legTarget := evt.EmitAsset.Asset
		var legVolumeRune cosmos.Uint
		switch {
		case legSource.IsRune():
			legVolumeRune = evt.InTx.Coins[0].Amount
		case legTarget.IsRune():
			legVolumeRune = evt.EmitAsset.Amount.Add(evt.LiquidityFeeInRune)
		default:
			// No RUNE side on this leg — skip. Synth/trade/secured
			// legs don't touch an L1 pool.
			continue
		}
		legVolumeTor := legVolumeRune.Mul(dollarsPerRune).Quo(one)
		legFeesTor := evt.LiquidityFeeInRune.Mul(dollarsPerRune).Quo(one)
		mgr.DynamicFeeMgr().CreditSwap(ctx, affiliates, affiliateBps,
			legSource, legTarget, legVolumeTor, legFeesTor)
	}
}

func (m *DynamicFeeMgr) GetDynamicBps(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, sourceAsset, targetAsset common.Asset) uint64 {
	enabled := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled)
	if enabled != 1 {
		return 0
	}

	pairA, pairB, ok := dynamicFeePairKey(sourceAsset, targetAsset)
	if !ok {
		return 0
	}

	// Only state 1 (active) thornames govern live fees; monitor-mode
	// (state 2) entries are excluded so they do not drive the floor.
	thorname, ok := m.dominantEligibleAffiliate(ctx, affiliates, affiliateBps, 1)
	if !ok {
		return 0
	}

	rec, err := m.keeper.GetDynamicFeeRecord(ctx, thorname, pairA, pairB)
	if err != nil {
		return 0
	}
	if rec.DynamicBps == 0 && len(rec.History) == 0 {
		return nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeFloorBPS))
	}

	return rec.DynamicBps
}

func (m *DynamicFeeMgr) EndBlock(ctx cosmos.Context) error {
	enabled := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEnabled)
	if enabled != 1 {
		// Drop any accumulator rows still carrying pre-disable volume/fees. If
		// we left them, a later re-enable would add new volume on top of old
		// and seal the mix into a wrong epoch.
		m.clearAccumulators(ctx)
		return nil
	}

	epochBlocks := m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeEpochBlocks)
	if epochBlocks <= 0 {
		return nil
	}

	if ctx.BlockHeight()%epochBlocks != 0 {
		return nil
	}

	// Reclaim state for thornames removed from the whitelist. Runs at epoch
	// boundaries only; live fee charging and accumulation are already gated
	// on the whitelist mimir, so delaying cleanup to epoch has no user-visible
	// effect and avoids an O(records) iterator scan every block.
	m.cleanupRemovedThornames(ctx)

	// Rebase stored LastActiveEpoch values when governance has changed
	// L1DynamicFeeEpochBlocks since the last seal. Without this, a shrink
	// inflates (currentEpoch - LastActiveEpoch) and would prematurely prune
	// otherwise-live records; a grow was already handled (underflow guard
	// below). Must run before sealing so fresh seals and pruning both see
	// a consistent epoch scale.
	m.rebaseOnEpochBlocksChange(ctx, epochBlocks)

	currentEpoch := uint64(ctx.BlockHeight() / epochBlocks)
	// Negative mimir values are treated as zero to prevent int64→uint64 wrap
	// (a -1 ceiling would otherwise clamp every record to MaxUint64 bps).
	floorBps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeFloorBPS))
	ceilingBps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeCeilingBPS))
	stepBps := nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeStepBPS))
	deadbandBps := cosmos.NewUint(nonNegUint64(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeDeadbandBPS)))
	windowEpochs := clampWindowEpochs(m.keeper.GetConfigInt64(ctx, constants.L1DynamicFeeWindowEpochs))
	tenK := cosmos.NewUint(10000)

	// Seal all accumulators. Only thorname-keyed accumulators exist under the
	// revenue-gradient algorithm; the pair-wide accumulator used by the older
	// share-based design is gone.
	accIter := m.keeper.GetDynamicFeeAccumulatorIterator(ctx)
	var thornameAccs []types.DynamicFeeAccumulator
	for ; accIter.Valid(); accIter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := m.keeper.Cdc().Unmarshal(accIter.Value(), &acc); err != nil {
			continue
		}
		thornameAccs = append(thornameAccs, acc)
	}
	accIter.Close()

	for _, acc := range thornameAccs {
		if err := m.sealEpochForRecord(ctx, acc, currentEpoch, floorBps, ceilingBps, stepBps, windowEpochs, deadbandBps, tenK); err != nil {
			ctx.Logger().Error("fail to seal dynamic fee epoch", "error", err, "thorname", acc.Thorname)
		}
		if err := m.keeper.DeleteDynamicFeeAccumulator(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB); err != nil {
			ctx.Logger().Error("fail to delete thorname accumulator", "error", err)
		}
	}

	// Prune inactive records. Collect-then-delete to avoid mutating the store
	// under an open IAVL iterator, which can skip entries or panic depending
	// on the backend.
	recIter := m.keeper.GetDynamicFeeRecordIterator(ctx)
	var staleRecs []types.DynamicFeeRecord
	for ; recIter.Valid(); recIter.Next() {
		var rec types.DynamicFeeRecord
		if err := m.keeper.Cdc().Unmarshal(recIter.Value(), &rec); err != nil {
			continue
		}
		// Guard against uint64 underflow if epochBlocks was increased via Mimir
		// (which shrinks currentEpoch) while older records still carry the
		// larger LastActiveEpoch computed under the old epoch size.
		if rec.LastActiveEpoch > currentEpoch {
			continue
		}
		if currentEpoch-rec.LastActiveEpoch >= types.MaxDynamicFeeHistory {
			staleRecs = append(staleRecs, rec)
		}
	}
	recIter.Close()

	for _, rec := range staleRecs {
		if err := m.keeper.DeleteDynamicFeeRecord(ctx, rec.Thorname, rec.PairAssetA, rec.PairAssetB); err != nil {
			ctx.Logger().Error("fail to prune dynamic fee record", "error", err)
		}
	}

	return nil
}

// sealEpochForRecord appends this epoch's entry, decides the next
// dynamic_bps via the closed-loop revenue gradient, and emits a
// dynamic_l1_fee_update event on a bps change.
func (m *DynamicFeeMgr) sealEpochForRecord(ctx cosmos.Context, acc types.DynamicFeeAccumulator, currentEpoch, floorBps, ceilingBps, stepBps uint64, windowEpochs int, deadbandBps, tenK cosmos.Uint) error {
	rec, err := m.keeper.GetDynamicFeeRecord(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB)
	if err != nil {
		return err
	}

	oldBps := rec.DynamicBps

	// Append the new epoch entry first so the gradient calculation can treat
	// the most-recent history entry as "this epoch". bps_at_close records the
	// bps that actually governed this epoch's fees_tor; rec.DynamicBps is then
	// advanced to the next epoch's setting below.
	rec.AppendHistory(types.DynamicFeeEpochRecord{
		Epoch:      currentEpoch,
		VolumeTor:  acc.VolumeTor,
		FeesTor:    acc.FeesTor,
		BpsAtClose: oldBps,
	})

	reason, newBps, feesBefore, feesAfter, deltaPctBps := decideNextBps(rec, oldBps, floorBps, ceilingBps, stepBps, windowEpochs, deadbandBps, tenK)
	rec.DynamicBps = newBps
	rec.LastActiveEpoch = currentEpoch

	if err := m.keeper.SetDynamicFeeRecord(ctx, rec); err != nil {
		return err
	}

	if newBps != oldBps {
		ctx.EventManager().EmitEvent(
			cosmos.NewEvent(
				types.DynamicL1FeeUpdateEventType,
				cosmos.NewAttribute("thorname", acc.Thorname),
				cosmos.NewAttribute("pair", fmt.Sprintf("%s|%s", acc.PairAssetA, acc.PairAssetB)),
				cosmos.NewAttribute("epoch", fmt.Sprintf("%d", currentEpoch)),
				cosmos.NewAttribute("old_bps", fmt.Sprintf("%d", oldBps)),
				cosmos.NewAttribute("new_bps", fmt.Sprintf("%d", newBps)),
				cosmos.NewAttribute("fees_before", feesBefore.String()),
				cosmos.NewAttribute("fees_after", feesAfter.String()),
				cosmos.NewAttribute("delta_pct_bps", deltaPctBps.String()),
				cosmos.NewAttribute("reason", reason),
			),
		)
	}

	return nil
}

// decideNextBps implements the closed-loop revenue gradient rule for one
// (thorname, pair) record. The newest history entry must already hold this
// epoch's FeesTor. It returns the reason string, next bps, and the window
// averages used in the decision (for event emission).
//
// Rule:
//   - No prior bps change in history → cold_start_probe (move up one step).
//   - Previous direction == +1 and fees went up → continue_up (raise).
//   - Previous direction == +1 and fees went down → reverse_down (lower).
//   - Previous direction == -1 and fees went up → continue_down (lower).
//   - Previous direction == -1 and fees went down → reverse_up (raise).
//   - |Δ%| below deadband → hold.
func decideNextBps(rec types.DynamicFeeRecord, oldBps, floorBps, ceilingBps, stepBps uint64, windowEpochs int, deadbandBps, tenK cosmos.Uint) (reason string, newBps uint64, feesBefore, feesAfter, deltaPctBps cosmos.Uint) {
	changeIdx, hasChange := lastBpsChangeBefore(rec.History, len(rec.History)-1)
	if !hasChange {
		return "cold_start_probe", clampBps(oldBps+stepBps, floorBps, ceilingBps), cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()
	}

	directionUp := rec.History[changeIdx].BpsAtClose > rec.History[changeIdx-1].BpsAtClose
	feesBefore = windowMeanFees(rec.History, changeIdx-windowEpochs, changeIdx)
	// Use only the latest post-change epochs instead of averaging every
	// epoch since the last bps move.
	feesAfter = windowMeanFees(rec.History, max(changeIdx, len(rec.History)-windowEpochs), len(rec.History))

	if feesBefore.IsZero() {
		return "hold", oldBps, feesBefore, feesAfter, cosmos.ZeroUint()
	}

	var absDelta cosmos.Uint
	feesIncreased := feesAfter.GT(feesBefore)
	if feesIncreased {
		absDelta = feesAfter.Sub(feesBefore)
	} else {
		absDelta = feesBefore.Sub(feesAfter)
	}
	deltaPctBps = common.GetSafeShare(absDelta, feesBefore, tenK)

	if deltaPctBps.LT(deadbandBps) {
		return "hold", oldBps, feesBefore, feesAfter, deltaPctBps
	}

	switch {
	case directionUp && feesIncreased:
		return "continue_up", clampBps(oldBps+stepBps, floorBps, ceilingBps), feesBefore, feesAfter, deltaPctBps
	case directionUp && !feesIncreased:
		return "reverse_down", clampBpsDown(oldBps, stepBps, floorBps), feesBefore, feesAfter, deltaPctBps
	case !directionUp && feesIncreased:
		return "continue_down", clampBpsDown(oldBps, stepBps, floorBps), feesBefore, feesAfter, deltaPctBps
	default:
		return "reverse_up", clampBps(oldBps+stepBps, floorBps, ceilingBps), feesBefore, feesAfter, deltaPctBps
	}
}

// lastBpsChangeBefore walks history backward before upToExclusive and returns
// the index of the most-recent entry whose bps_at_close differs from its
// predecessor. The entry at upToExclusive is intentionally excluded so the
// latest bps transition is not evaluated until another epoch has been sealed.
// Returns (0, false) when history lacks two entries to compare.
func lastBpsChangeBefore(history []types.DynamicFeeEpochRecord, upToExclusive int) (int, bool) {
	if upToExclusive < 1 {
		return 0, false
	}
	for i := upToExclusive - 1; i >= 1; i-- {
		if history[i].BpsAtClose != history[i-1].BpsAtClose {
			return i, true
		}
	}
	return 0, false
}

// windowMeanFees returns the mean of FeesTor across history[lo:hi] with
// bounds clamped into the slice. If the resulting window is empty, returns
// zero.
func windowMeanFees(history []types.DynamicFeeEpochRecord, lo, hi int) cosmos.Uint {
	if lo < 0 {
		lo = 0
	}
	if hi > len(history) {
		hi = len(history)
	}
	if hi-lo <= 0 {
		return cosmos.ZeroUint()
	}
	sum := cosmos.ZeroUint()
	for i := lo; i < hi; i++ {
		sum = sum.Add(history[i].FeesTor)
	}
	return sum.Quo(cosmos.NewUint(uint64(hi - lo)))
}

func clampBps(candidate, floorBps, ceilingBps uint64) uint64 {
	if candidate > ceilingBps {
		return ceilingBps
	}
	if candidate < floorBps {
		return floorBps
	}
	return candidate
}

func clampBpsDown(oldBps, stepBps, floorBps uint64) uint64 {
	if oldBps > stepBps && oldBps-stepBps >= floorBps {
		return oldBps - stepBps
	}
	return floorBps
}

// nonNegUint64 converts a governance-controlled int64 to uint64, treating
// negatives as 0. Without this, a negative mimir value wraps to a very large
// uint64 and would make clampBps pin every record to MaxUint64.
func nonNegUint64(raw int64) uint64 {
	if raw < 0 {
		return 0
	}
	return uint64(raw)
}

// clampWindowEpochs bounds the configured gradient-window size to [1,
// MaxDynamicFeeHistory]. The lower bound avoids a degenerate empty "before"
// window; the upper bound avoids overrunning the bounded history buffer.
func clampWindowEpochs(raw int64) int {
	if raw < 1 {
		return 1
	}
	if raw > int64(types.MaxDynamicFeeHistory) {
		return types.MaxDynamicFeeHistory
	}
	return int(raw)
}

// rebaseOnEpochBlocksChange converts stored per-record LastActiveEpoch (and
// history epoch labels) from the previously-observed epoch scale to the
// current scale, preserving approximate block-height recency. Runs at most
// once per governance change; updates the stored observation afterward.
//
// Formula: newEpoch = oldEpoch * oldEB / newEB. This keeps the block-height
// range each epoch index corresponds to stable across the change, so the
// MaxDynamicFeeHistory pruning window continues to mean "records inactive
// for ~MaxDynamicFeeHistory * newEB blocks".
//
// History epoch labels are rebased alongside LastActiveEpoch to keep them
// coherent in querier output. Under a large shrink (newEB ≪ oldEB) distinct
// old epochs can collapse to the same new epoch number — this is cosmetic
// only since decideNextBps walks history by index, not epoch number.
func (m *DynamicFeeMgr) rebaseOnEpochBlocksChange(ctx cosmos.Context, currentEpochBlocks int64) {
	if currentEpochBlocks <= 0 {
		return
	}
	observed, err := m.keeper.GetLastObservedDynamicFeeEpochBlocks(ctx)
	if err != nil {
		ctx.Logger().Error("fail to read last-observed dynamic fee epoch blocks", "error", err)
		return
	}
	if observed == currentEpochBlocks {
		return
	}
	// First observation (fresh store, or feature newly enabled): just record
	// and return — nothing to rebase from.
	if observed <= 0 {
		m.keeper.SetLastObservedDynamicFeeEpochBlocks(ctx, currentEpochBlocks)
		return
	}

	iter := m.keeper.GetDynamicFeeRecordIterator(ctx)
	var records []types.DynamicFeeRecord
	for ; iter.Valid(); iter.Next() {
		var rec types.DynamicFeeRecord
		if err := m.keeper.Cdc().Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	iter.Close()

	oldEB := uint64(observed)
	newEB := uint64(currentEpochBlocks)
	for _, rec := range records {
		rec.LastActiveEpoch = rec.LastActiveEpoch * oldEB / newEB
		for i := range rec.History {
			rec.History[i].Epoch = rec.History[i].Epoch * oldEB / newEB
		}
		if err := m.keeper.SetDynamicFeeRecord(ctx, rec); err != nil {
			ctx.Logger().Error("fail to rebase dynamic fee record", "error", err, "thorname", rec.Thorname)
		}
	}

	m.keeper.SetLastObservedDynamicFeeEpochBlocks(ctx, currentEpochBlocks)
}

// clearAccumulators deletes every DynamicFeeAccumulator row. Collect-then-delete
// to avoid mutating the store under an open IAVL iterator.
func (m *DynamicFeeMgr) clearAccumulators(ctx cosmos.Context) {
	iter := m.keeper.GetDynamicFeeAccumulatorIterator(ctx)
	var accs []types.DynamicFeeAccumulator
	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := m.keeper.Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		accs = append(accs, acc)
	}
	iter.Close()
	for _, acc := range accs {
		if err := m.keeper.DeleteDynamicFeeAccumulator(ctx, acc.Thorname, acc.PairAssetA, acc.PairAssetB); err != nil {
			ctx.Logger().Error("fail to clear dynamic fee accumulator", "error", err, "thorname", acc.Thorname)
		}
	}
}

func (m *DynamicFeeMgr) cleanupRemovedThornames(ctx cosmos.Context) {
	for _, thorname := range m.keeper.GetRemovedDynamicFeeWhitelistThornames(ctx) {
		if err := m.keeper.DeleteAllDynamicFeeStateForThorname(ctx, thorname); err != nil {
			ctx.Logger().Error("fail to cleanup dynamic fee state for removed thorname", "thorname", thorname, "error", err)
		}
	}
}
