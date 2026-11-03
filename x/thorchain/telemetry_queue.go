package thorchain

import (
	"sort"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// stuckOutboundHeights carries the heights below the scan window that still hold
// an unsigned outbound, so the pending gauges do not report a stuck outbound as
// drained once it ages past the window.
//
// LackSigning inspects a scheduled item exactly once, at height+its signing period.
// Every `continue` on that path - no vault, no observed-tx voter, retired-vault max
// attempts, treasury recovery failed - leaves the item at its original height with
// an empty OutHash and nothing ever revisits it. The sliding window alone would
// therefore drop it after 3x SigningTransactionPeriod and reset the count and age
// gauges to zero while the funds are still stranded. (keeper GetPendingOutbounds,
// the precedent this scan cites, has the same blind spot.)
//
// Node-local and read-only: written only from the telemetry path, which runs from
// module.go EndBlock behind the telemetry.enabled flag and is never reached from a
// simulation or CheckTx context. It is not consensus state and never reaches the
// keeper.
//
// In-memory, so a restart - which every node performs at every upgrade - starts
// with an empty set. stuckOutboundBackfill below rebuilds it once per process
// rather than leaving a long-stranded outbound invisible with nothing to
// rediscover it.
//
// The chains recorded per height are the ones that still held an unsigned item
// there at the end of the last scan. They exist so the cap below can be applied
// without blinding a whole chain - see the promotion step in the scan. They pick
// which heights are worth re-checking, never whether a chain is covered: a height
// this far in the past can only lose items, so a stale list over-promotes and
// never under-promotes.
var stuckOutboundHeights = map[int64][]common.Chain{}

// Ceiling on how many out-of-window heights are re-checked per scan, on top of the
// ~900-height window. The oldest are the ones scanned, so pending_oldest_blocks
// stays correct past the cap and the per-chain counts undercount. The excess is
// carried forward unscanned rather than dropped, so it comes back into range as
// the older heights clear.
//
// The cap is global while the gauges it feeds are per chain, so on its own it
// would blind a chain entirely: 256 stuck BTC heights ahead of the only stuck ETH
// height leave ETH reporting zero count, age and value while its outbound is still
// stranded, and the alert clears. The scan therefore promotes the oldest height of
// every chain the capped pass did not actually find, which bounds the walk at
// maxStuckOutboundHeights + len(common.AllChains) and keeps pending >= 1 and
// pending_oldest_blocks exact for every affected chain. Only the per-chain counts
// and values undercount past the cap, and scan_truncated_heights reports by how
// many heights. Promotions themselves are budgeted - see
// maxStuckOutboundPromotions - and scan_promotions_deferred reports when that
// budget, rather than genuine coverage, is what stopped the walk.
const maxStuckOutboundHeights = 256

// How many out-of-window heights a scan may read for promotions, on top of the cap
// and the window.
//
// A promotion that finds nothing teaches the scan nothing about its chain, so it
// does not advance that chain's observed oldest and the next candidate qualifies
// too. Candidates go stale in bulk - a backlog clearing after an incident leaves a
// pile of remembered heights whose items are now signed - so without a budget one
// EndBlock would read the whole accumulated set, precisely when the node can least
// afford it. Each spent read permanently evicts a stale height, so the backlog
// still drains, just across scans instead of inside one block.
const maxStuckOutboundPromotions = 64

// The one-off startup backfill: how far below the scan window it reaches, and how
// many heights it examines per scan block.
//
// Without it a restart forgets every out-of-window height, and an outbound already
// stranded past the ~900-block window is invisible again with nothing to
// rediscover it - the exact multi-day-stuck-funds case the carry-over exists for.
// The walk is read-only, strictly bounded per scan block so it never competes with
// block production, and thornode_outbound_scan_backfill_remaining reports
// incomplete coverage until it lands.
//
// 432,000 blocks is roughly 30 days of history at mainnet block time. At 2,048
// heights per scan block and one scan block in ten, it completes in ~2,110 blocks
// - a few hours after a restart. Anything stranded longer than the depth is
// outside what this mechanism claims to cover; the gauge reaching 0 is what says
// the claim is in force.
const (
	stuckOutboundBackfillDepth   = 432_000
	stuckOutboundBackfillPerScan = 2048
)

// Backfill progress, node-local like the set it fills. -1 before the first scan of
// this process; thereafter the next height to examine, walking down. Below
// stuckOutboundBackfillFloor means complete.
var (
	stuckOutboundBackfillCursor int64 = -1
	stuckOutboundBackfillFloor  int64
)

// appendChain adds c to chains unless it is already there. Chain sets per height
// are a handful of entries at most, so a linear scan beats a map.
func appendChain(chains []common.Chain, c common.Chain) []common.Chain {
	for _, have := range chains {
		if have.Equals(c) {
			return chains
		}
	}
	return append(chains, c)
}

// emitQueueDetailTelemetry emits the per-chain detail behind the unlabelled queue
// scalars, plus the three subsystems whose failure modes are invisible today:
//
//   - stuck outbounds: which chain's unsigned queue is backing up, how old its
//     oldest unsigned item is, and how much RUNE value is stranded there. The
//     shipped thornode_queue_{internal,outbound} gauges are unlabelled scalars, so
//     a wedged BTC signer hides behind healthy ETH traffic, and a flat count says
//     nothing about items rotting past SigningTransactionPeriod.
//   - the dynamic outbound-fee control loop pinning at its Max (surplus collapsed,
//     users over-charged) or Min (runaway surplus), and pools dropping out of
//     routing (24h rolling volume going flat while blocks keep producing).
//   - stable reserve insolvency: per-asset inventory drifting below the per-asset
//     contributor credit that inventory is supposed to make whole.
//   - wedged streaming swaps: streams in flight but no sub-swap executing.
//
// Strictly read-only. It runs from emitEndBlockTelemetry, which is gated on the
// node-local telemetry.enabled flag, so any keeper write, event or gas consumption
// reached from here would fork the chain between telemetry and non-telemetry nodes.
func emitQueueDetailTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()

	// The per-entity gauge loops here (per-pool, per-secured-asset) refresh every
	// 5 blocks rather than every block. Prometheus scrapes every 15-30s, i.e. every
	// 2.5-5 blocks, go-metrics gauges retain their last value and the configured
	// prometheus_retention_time is 600s, so a 5-block refresh is indistinguishable
	// to a scraper from a per-block one.
	//
	// Residue 4, deliberately NOT residue 2 where emitPools in emitEndBlockTelemetry
	// (helpers.go) lives. Co-phasing the two per-pool loops was measured at 2,350 gauge
	// writes / 11.4ms on residue 2 against ~350 / 6.9ms on residue 4 - the whole point
	// of staggering. Sharing a residue bought nothing: go-metrics gauges retain their
	// last value and both families are populated at every scrape, so an instant-vector
	// join across them is coherent whatever block each was written on. Residue 0 is the
	// wrong home for any heavy group - every pre-existing IsPeriodLastBlock(10/50/100/600)
	// gate fires there.
	// IsPeriodLastBlock only does height%n==0, so the residue is written out directly.
	detailBlock := ctx.BlockHeight()%5 == 4
	// The overdue-outbound scan keeps its own, longer gate (see below). It fires on
	// residue 0, so it never coincides with detailBlock and the fetch below is
	// load-bearing rather than redundant: it costs one extra GetPools per 10 blocks
	// to keep both consumers on a single read.
	scanBlock := IsPeriodLastBlock(ctx, 10)

	// Fetched once, on gate blocks only, and reused by both the outbound-value
	// lookup and the pool loop below - do not re-fetch.
	var pools Pools
	if detailBlock || scanBlock {
		var err error
		pools, err = k.GetPools(ctx)
		if err != nil {
			ctx.Logger().Error("fail to get pools for queue telemetry", "error", err)
			pools = nil
		}
	}
	// ---- pending outbound queue, per chain --------------------------------
	//
	// One pass over the txout store serves the count, the age and the value
	// gauges. Scan the widest window an unsigned outbound can legitimately
	// occupy: XMR and EVM token/aggregator outbounds live 3x the signing period
	// (types.MaxTxOutSigningTransactionPeriod) and are exactly the ones that
	// stick longest. Precedent: keeper GetPendingOutbounds.
	//
	// No per-item SigningTransactionPeriod filter is applied on purpose. An item
	// still unsigned past its own period is the LackSigning reschedule failure
	// these gauges exist to surface - a successful reschedule stamps the old
	// item's OutHash with BlankTxID, which drops it from this scan on its own.
	// Items that age past the window without ever being resolved are carried in
	// stuckOutboundHeights and re-checked individually - see there for why the
	// window alone is not enough.
	// Gated: this walks up to 3x SigningTransactionPeriod (~900) heights, by far the
	// most expensive thing in the per-block telemetry path. A stuck outbound is
	// measured in blocks-since-due over a 900-block window, so 10-block resolution
	// loses nothing, and go-metrics gauges hold their last value between updates.
	if scanBlock {
		// One aggregate per chain, one map lookup per tx item. Pointer values so the
		// counters are updated in place rather than through four keyed writes.
		type chainPending struct {
			internal, outbound int64
			oldest             int64
			value              cosmos.Uint
		}
		pending := map[common.Chain]*chainPending{}

		// Asset.String() is a fmt.Sprintf; keying the pool cache by the layer-1
		// Asset struct (precedent: keeper GetTOIsValue) means String() runs once per
		// pool at setup and never inside the item loop. A nil entry is memoised as
		// "no pool for this asset" so a miss is not re-resolved per item.
		poolCache := make(map[string]*Pool, len(pools))
		for i := range pools {
			poolCache[pools[i].Asset.String()] = &pools[i]
		}
		poolByAsset := make(map[common.Asset]*Pool, len(pools))

		signingPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
		dueStartHeight := ctx.BlockHeight() - types.MaxTxOutSigningTransactionPeriod(signingPeriod)
		if dueStartHeight < 1 {
			dueStartHeight = 1
		}
		// ---- one-off startup backfill ---------------------------------------
		//
		// Runs FIRST, so anything it finds joins this scan's candidate set and is
		// counted in the same scan that reports coverage complete. Recording it for
		// the next scan instead left a one-scan window where
		// scan_backfill_remaining read 0 - a full all-clear - while a just-found
		// stranded outbound was not yet in any pending gauge.
		//
		// Read-only: GetTxOut and nothing else.
		if stuckOutboundBackfillCursor < 0 {
			stuckOutboundBackfillCursor = dueStartHeight - 1
			stuckOutboundBackfillFloor = dueStartHeight - stuckOutboundBackfillDepth
			if stuckOutboundBackfillFloor < 1 {
				stuckOutboundBackfillFloor = 1
			}
		}
		// Candidate heights for this scan: everything carried from the last scan,
		// plus whatever this backfill batch turns up. A local copy - the global set
		// is replaced wholesale at the end from what this scan actually observed.
		candidates := make(map[int64][]common.Chain, len(stuckOutboundHeights)+8)
		// Copied wholesale; no ordering is derived from this iteration.
		// analyze-ignore(map-iteration)
		for h, chains := range stuckOutboundHeights {
			candidates[h] = chains
		}
		for n := 0; n < stuckOutboundBackfillPerScan && stuckOutboundBackfillCursor >= stuckOutboundBackfillFloor; n++ {
			h := stuckOutboundBackfillCursor
			stuckOutboundBackfillCursor--
			txs, terr := k.GetTxOut(ctx, h)
			if terr != nil || txs == nil {
				continue
			}
			var chains []common.Chain
			for i := range txs.TxArray {
				tx := &txs.TxArray[i]
				if !tx.OutHash.IsEmpty() {
					continue
				}
				chains = appendChain(chains, tx.Chain)
			}
			if len(chains) > 0 {
				candidates[h] = chains
			}
		}

		carried := make([]int64, 0, len(candidates))
		// Sorted immediately below before anything reads it, and this path is
		// node-local telemetry that never touches the keeper regardless.
		// analyze-ignore(map-iteration)
		for h := range candidates {
			if h < dueStartHeight {
				carried = append(carried, h)
			}
		}
		// Ascending, so the cap keeps the oldest - the ones an operator is alerting on.
		sort.Slice(carried, func(i, j int) bool { return carried[i] < carried[j] })

		scan, rest := carried, []int64(nil)
		if len(carried) > maxStuckOutboundHeights {
			scan, rest = carried[:maxStuckOutboundHeights], carried[maxStuckOutboundHeights:]
		}

		// Rebuilt from scratch each scan: a height drops out the moment its last
		// unsigned item is signed, rescheduled (OutHash stamped BlankTxID) or the
		// TxOut record goes away.
		stillUnsigned := make(map[int64][]common.Chain, len(candidates))

		// walk aggregates one height into pending and stillUnsigned. Hoisted out of
		// the loop because the promotion pass below has to walk heights AFTER seeing
		// what the main pass found, which no single ordered sweep can express.
		//
		// Walk order is deliberately irrelevant: oldest is a running minimum rather
		// than "whatever the first ascending hit was". The previous ordering
		// invariant forced every scheduling decision to be made before the data was
		// observed, which is what made both the cap and the backfill wrong.
		walk := func(height int64) {
			txs, terr := k.GetTxOut(ctx, height)
			if terr != nil || txs == nil {
				return
			}
			for i := range txs.TxArray {
				tx := &txs.TxArray[i]
				if !tx.OutHash.IsEmpty() {
					continue
				}
				// The chain set per height is what lets the cap below promote a
				// chain's oldest height instead of blinding it.
				stillUnsigned[height] = appendChain(stillUnsigned[height], tx.Chain)
				p := pending[tx.Chain]
				if p == nil {
					p = &chainPending{oldest: height, value: cosmos.ZeroUint()}
					pending[tx.Chain] = p
				} else if height < p.oldest {
					p.oldest = height
				}
				// Only the memo's tx-type prefix decides the class, and every sub-parser
				// returns a memo carrying its own type even when the body fails to parse
				// (the error is discarded at this call site anyway), so classifying off
				// the prefix is equivalent to a full ParseMemo here at a fraction of the
				// cost. An unrecognised prefix counts toward neither class, exactly as an
				// EmptyMemo did.
				internal, outbound := queueMemoClass(tx.Memo)
				if internal {
					p.internal++
				} else if outbound {
					p.outbound++
				}
				if tx.Coin.Amount.IsNil() {
					continue
				}
				if tx.Coin.IsRune() {
					p.value = p.value.Add(tx.Coin.Amount)
					continue
				}
				l1 := tx.Coin.Asset.GetLayer1Asset()
				pool, memoised := poolByAsset[l1]
				if !memoised {
					pool = poolCache[l1.String()]
					poolByAsset[l1] = pool
				}
				if pool != nil {
					p.value = p.value.Add(pool.AssetValueInRune(tx.Coin.Amount))
				}
			}
		}

		for _, height := range scan {
			walk(height)
		}
		for height := dueStartHeight; height <= ctx.BlockHeight(); height++ {
			walk(height)
		}

		// Per-chain rescue, decided on what the pass above actually FOUND rather
		// than on the chain lists the last scan recorded. The cap is global while
		// the gauges are per chain, so without this a wall of stuck BTC heights
		// blinds the only stuck ETH height entirely.
		//
		// Using the stale lists to decide coverage was wrong in a way retaining the
		// heights did not fix: if the ETH item among the first 256 has since been
		// signed, ETH is still listed there, still counted as covered, and a newer
		// unsigned ETH height past the cap is skipped - ETH reports zero for that
		// scan. candidates[h] is used only to pick which heights are worth trying,
		// which is safe in the one direction that matters: a height's item set can
		// only shrink once it is this far in the past, so a stale list over-promotes
		// and never under-promotes.
		//
		// The test is "is h older than anything observed for this chain", NOT "does
		// this chain appear at all". pending already includes the window by this
		// point, so mere presence would let ordinary recent traffic stand in for a
		// stranded ancestor: one fresh ETH outbound inside the window would mark ETH
		// covered, skip its capped-out stuck height, and leave pending_oldest_blocks
		// reporting the fresh item's age - the alert clears while the old outbound is
		// still unsigned. Comparing against oldest is also what keeps the bound at
		// maxStuckOutboundHeights + len(common.AllChains): rest is ascending, so the
		// first height promoted for a chain becomes that chain's oldest and every
		// later one fails the test.
		var overflow []int64
		promotions, deferred := 0, 0
		for i, h := range rest {
			if promotions >= maxStuckOutboundPromotions {
				// Budget spent. Carry the rest unexamined rather than reading an
				// unbounded tail inside EndBlock; the spent reads have already evicted
				// that many stale heights, so the next scan gets further.
				deferred = len(rest) - i
				overflow = append(overflow, rest[i:]...)
				break
			}
			rescue := false
			for _, c := range candidates[h] {
				if p := pending[c]; p == nil || h < p.oldest {
					rescue = true
					break
				}
			}
			if rescue {
				// Walked immediately, so its chains count as covered for the heights
				// considered after it and one stuck chain cannot drag in the whole tail.
				promotions++
				walk(h)
			} else {
				overflow = append(overflow, h)
			}
		}

		// Not re-scanned this round, so the last known state is the best available -
		// keep them queued, with their chains, rather than forgetting them. Dropping
		// them would put those heights right back in the blind spot this whole
		// mechanism exists to close, and dropping their chains would break the
		// per-chain promotion above on the next scan.
		for _, h := range overflow {
			stillUnsigned[h] = candidates[h]
		}
		stuckOutboundHeights = stillUnsigned

		// Heights the backfill has not reached yet. Non-zero means the out-of-window
		// picture is still incomplete and the pending gauges may understate a
		// long-stranded outbound; alert rules that care should hold off on "all
		// clear" while it is above zero.
		backfillRemaining := stuckOutboundBackfillCursor - stuckOutboundBackfillFloor + 1
		if backfillRemaining < 0 {
			backfillRemaining = 0
		}
		telemetry.SetGauge(float32(backfillRemaining), "thornode", "outbound", "scan_backfill_remaining")

		// How many out-of-window heights the cap left unscanned this round. Zero in
		// normal operation. While it is non-zero the per-chain pending counts and
		// values undercount - pending_oldest_blocks and "is this chain stuck at all"
		// stay exact, because every affected chain's oldest height is promoted into
		// the scan above.
		telemetry.SetGauge(float32(len(overflow)), "thornode", "outbound", "scan_truncated_heights")

		// Promotion candidates the budget left unread. Distinct from the gauge above
		// on purpose: ordinary cap truncation still leaves pending_oldest_blocks
		// exact, because a truncated height is only truncated when an OLDER height on
		// the same chain was scanned. This one does not - an unread candidate may be
		// older than anything this scan saw for its chain, so while it is non-zero
		// pending_oldest_blocks can understate and a stuck-outbound alert keyed on age
		// should not be trusted to clear.
		telemetry.SetGauge(float32(deferred), "thornode", "outbound", "scan_promotions_deferred")

		// Iterate the fixed chain list, never the maps: emission order stays
		// deterministic and a drained chain reports 0 instead of leaving a stale
		// non-zero series behind.
		for _, chain := range common.AllChains {
			chainName := chain.String()
			// The in-memory sink RETAINS the label slice it is handed (go-metrics
			// inmem.go, GaugeValue.Labels), so one slice may serve several calls only
			// while its contents never change - as chainLabel's do not. Never mutate a
			// label slice between calls to save an allocation: that would rewrite the
			// labels of every sample already holding it.
			chainLabel := []metrics.Label{telemetry.NewLabel("chain", chainName)}
			p := pending[chain]
			var internal, outbound, age float32
			value := cosmos.ZeroUint()
			// The nil-check is mandatory: a zero-value cosmos.Uint has a nil big.Int
			// and telem would dereference it.
			if p != nil {
				internal = float32(p.internal)
				outbound = float32(p.outbound)
				// Age of the oldest still-unsigned item on this chain. Persistently above
				// SigningTransactionPeriod means signing is failing, not merely busy.
				age = float32(ctx.BlockHeight() - p.oldest)
				if !p.value.IsNil() {
					value = p.value
				}
			}
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "outbound", "pending"},
				internal,
				[]metrics.Label{telemetry.NewLabel("chain", chainName), telemetry.NewLabel("memo_type", "internal")},
			)
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "outbound", "pending"},
				outbound,
				[]metrics.Label{telemetry.NewLabel("chain", chainName), telemetry.NewLabel("memo_type", "outbound")},
			)
			telemetry.SetGaugeWithLabels([]string{"thornode", "outbound", "pending_oldest_blocks"}, age, chainLabel)
			telemetry.SetGaugeWithLabels([]string{"thornode", "outbound", "pending_value_rune"}, telem(value), chainLabel)
		}
	}

	// ---- pool 24h volume and outbound fee multiplier ----------------------
	//
	// Per-pool gauge loop: five gauges x every pool is the bulk of this emitter's
	// per-block cost, so it refreshes on the 5-block detail gate. The withheld
	// iterator, the equilibrium read and the three config reads below feed nothing
	// else, so they move inside the gate with it.
	if detailBlock {
		// Config read once per gate block rather than once per pool.
		targetOutboundFeeSurplus := cosmos.SafeUintFromInt64(k.GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune))
		maxOutboundFeeMultiplier := cosmos.SafeUintFromInt64(k.GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints))
		minOutboundFeeMultiplier := cosmos.SafeUintFromInt64(k.GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints))

		// DO NOT replace this iterator with keeper.GetOutboundFeeWithheldRune. That
		// accessor looks read-only but lazily WRITES the equilibrium surplus back to
		// the store on a zero read (keeper/v1/keeper_outbound_fees.go, the
		// `if withheld.IsZero()` branch), which from this telemetry-gated path would
		// fork the chain. The iterator is read-only, and the same equilibrium value
		// is reproduced below via the read-only GetSurplusForTargetMultiplier so the
		// multiplier still matches what GasMgr would compute.
		equilibriumWithheld := k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(10_000))
		withheldByAsset := map[string]cosmos.Uint{}
		whIter := k.GetOutboundFeeWithheldRuneIterator(ctx)
		defer whIter.Close()
		for ; whIter.Valid(); whIter.Next() {
			var withheldRecord types.ProtoUint64
			if uerr := k.Cdc().Unmarshal(whIter.Value(), &withheldRecord); uerr != nil {
				continue
			}
			// KVStore.GetKey uppercases the asset half of the key, so the lookup below
			// must uppercase too.
			withheldByAsset[trimKeyPrefix(whIter.Key())] = cosmos.NewUint(withheldRecord.GetValue())
		}

		for i := range pools {
			pool := &pools[i]
			if pool.LPUnits.IsZero() {
				continue
			}
			// Asset.String() is a fmt.Sprintf; it is loop-invariant per pool and used
			// twice, so build it once.
			assetName := pool.Asset.String()
			labels := []metrics.Label{
				telemetry.NewLabel("pool", assetName),
				telemetry.NewLabel("status", pool.Status.String()),
			}

			// 24h rolling volume maintained by VolumeMgr.EndBlock (96 x 15m buckets),
			// read one EndBlock behind - harmless for a 24h window. One pool flat while
			// the rest trade means it dropped out of routing; every pool flat while
			// blocks keep producing means an aggregator or quote-API outage.
			if vol, verr := k.GetVolume(ctx, pool.Asset); verr == nil && !vol.TotalRune.IsNil() {
				telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "volume_24h", "rune"}, telem(vol.TotalRune), labels)
			}

			spent, serr := k.GetOutboundFeeSpentRune(ctx, pool.Asset)
			if serr != nil {
				ctx.Logger().Error("fail to get outbound fee spent rune for telemetry", "asset", pool.Asset, "error", serr)
				continue
			}
			withheld, found := withheldByAsset[strings.ToUpper(assetName)]
			if !found || withheld.IsZero() {
				// Mirror the lazy-init value GasMgr would observe for an unwritten or
				// zeroed entry, without performing that write.
				withheld = equilibriumWithheld
			}
			// Outbound fee subsidy accounting. A persistently negative surplus means the
			// network is paying out more gas than it collects on this asset. Emitted here
			// rather than in emitEndBlockTelemetry because `withheld` above is resolved
			// read-only; the obvious keeper accessor writes on a zero read.
			telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "outbound_fee", "withheld_rune"}, telem(withheld), labels)
			telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "outbound_fee", "spent_rune"}, telem(spent), labels)
			telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "outbound_fee", "surplus_rune"}, solvencyDelta(withheld, spent), labels)

			// The knob users actually feel. Pinned at Max means the gas surplus has
			// collapsed and users are being over-charged to claw it back; pinned at Min
			// means the surplus is running away. Not derivable in PromQL because the
			// min/max/target config values are exposed nowhere else. Basis points, so
			// telemRaw (telem would divide by 1e8). Arg order is (target, spent,
			// withheld) - see the GasMgr.GetFee call site.
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "pool", "outbound_fee", "multiplier_bps"},
				telemRaw(mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, targetOutboundFeeSurplus, spent, withheld, maxOutboundFeeMultiplier, minOutboundFeeMultiplier)),
				labels,
			)
		}
	}

	// ---- stable reserve inventory -----------------------------------------
	//
	// Per-secured-asset gauge loop, so it rides the same 5-block detail gate as the
	// pool loop above. Its comparison partner, credit_outstanding below, is already
	// on a 100-block gate, so a 5-block inventory refresh loses nothing. The two no
	// longer land on the same block (credit is height%100==0, i.e. residue 0), but
	// the skew is at most 3 blocks (~18s) against a 10-minute refresh, far inside
	// the staleness the credit gate already accepts.
	//
	// Module address resolved once, not once per asset.
	var stableReserveAddr cosmos.AccAddress
	if detailBlock {
		stableReserveAddr = k.GetModuleAccAddress(StableReserveName)
	}
	if stableReserveAddr != nil {
		saIter := k.GetSecuredAssetIterator(ctx)
		defer saIter.Close()
		for ; saIter.Valid(); saIter.Next() {
			var sa SecuredAsset
			if uerr := k.Cdc().Unmarshal(saIter.Value(), &sa); uerr != nil {
				continue
			}
			// Underlying inventory, not the raw share balance: BalanceOf converts
			// shares to underlying via GetSafeShare(balance, shareSupply, depth).
			// This is the number every stable-reserve path gates on - withdrawals
			// clamp to it, TrySwap refuses to fill below it, depeg rescue skips
			// zero-inventory assets - so cross-asset drift is only visible here.
			// Label with the layer-1 form ("BTC.BTC"), not the secured form
			// ("BTC-BTC"): credit_outstanding below is keyed off the L1 asset, and
			// the whole point of the pair is comparing them per asset in PromQL,
			// which needs the label values to be identical. Symbols and chains are
			// always uppercase, so the two spellings match exactly.
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "stable_reserve", "inventory"},
				telem(mgr.SecuredAssetManager().BalanceOf(ctx, sa.Asset, stableReserveAddr)),
				[]metrics.Label{telemetry.NewLabel("asset", sa.Asset.GetLayer1Asset().String())},
			)
		}
	}

	// Contributor credits live in an unbounded per-(asset,address) store, so scan
	// it every 100 blocks (~10 min) rather than every block. Free on mainnet today
	// - the store is empty while StableReserveEnabled is 0.
	//
	// credit_outstanding{asset} exceeding inventory{asset} is the invariant break
	// the stable reserve documents against itself: a TrySwap fill or a depeg rescue
	// moves module inventory across assets while per-asset contributor credit is
	// deliberately not reconciled, so a credit can become permanently unfillable
	// while the module balance persists.
	if IsPeriodLastBlock(ctx, 100) {
		credits := map[string]cosmos.Uint{}
		var order []string
		crIter := k.GetStableReserveCreditIterator(ctx)
		defer crIter.Close()
		for ; crIter.Valid(); crIter.Next() {
			// Key is "<ASSET>/<ADDRESS>"; the contributor address is discarded and
			// never becomes a label.
			assetKey, _, found := strings.Cut(trimKeyPrefix(crIter.Key()), "/")
			if !found {
				continue
			}
			// Value is "<address>/<amount>", with a legacy bare-amount form.
			amountStr := string(crIter.Value())
			if _, after, ok := strings.Cut(amountStr, "/"); ok {
				amountStr = after
			}
			// ParseUint, never NewUintFromString: the latter panics on malformed
			// input, and the recover in emitEndBlockTelemetry would swallow that,
			// silently stopping every metric emitted after this point, every block.
			amount, perr := cosmos.ParseUint(amountStr)
			if perr != nil {
				continue
			}
			if cur, seen := credits[assetKey]; seen {
				credits[assetKey] = cur.Add(amount)
			} else {
				credits[assetKey] = amount
				order = append(order, assetKey)
			}
		}
		// Emit in the iterator's own first-seen order; never range the map.
		for _, assetKey := range order {
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "stable_reserve", "credit_outstanding"},
				telem(credits[assetKey]),
				[]metrics.Label{telemetry.NewLabel("asset", assetKey)},
			)
		}
	}

	// ---- streaming swap staleness -----------------------------------------
	//
	// The shipped count / sub_swaps_remaining gauges cannot tell ten streams
	// running normally from ten streams frozen since a chain halted. swapped_out
	// is user output already swapped but not yet delivered - the funds in limbo if
	// a stream is wedged.
	var maxIdleBlocks, stalled int64
	swappedOut := cosmos.ZeroUint()
	// One fold for both stores: the two record types carry the same three fields
	// under different names, and a stalled V2 stream is exactly as invisible as a
	// stalled V1 one.
	foldStream := func(out cosmos.Uint, lastHeight int64, interval uint64) {
		if !out.IsNil() {
			swappedOut = swappedOut.Add(out)
		}
		if lastHeight <= 0 {
			return
		}
		idle := ctx.BlockHeight() - lastHeight
		if idle > maxIdleBlocks {
			maxIdleBlocks = idle
		}
		// Clamp before the uint64->int64 conversion so a bogus Interval cannot wrap
		// negative and mark every stream stalled.
		iv := int64(1)
		if interval > 0 && interval <= 1_000_000 {
			iv = int64(interval)
		}
		if idle > 10*iv {
			stalled++
		}
	}

	ssIter := k.GetStreamingSwapIterator(ctx)
	defer ssIter.Close()
	for ; ssIter.Valid(); ssIter.Next() {
		var swp StreamingSwap
		if uerr := k.Cdc().Unmarshal(ssIter.Value(), &swp); uerr != nil {
			continue
		}
		foldStream(swp.Out, swp.LastHeight, swp.Interval)
	}

	// The streaming-swap store only ever holds legacy records: SwapHandler writes
	// one only for msg.IsLegacyStreaming(), i.e. StreamInterval > 0 AND version v1.
	// V2 keeps its progress in MsgSwap.State on the adv swap queue instead, so on a
	// network that has moved to V2 all three gauges above sat flat at zero no matter
	// how many streams were wedged with undelivered output.
	advIter := k.GetAdvSwapQueueItemIterator(ctx)
	defer advIter.Close()
	for ; advIter.Valid(); advIter.Next() {
		var msg MsgSwap
		if uerr := k.Cdc().Unmarshal(advIter.Value(), &msg); uerr != nil {
			continue
		}
		// State is the V2 progress record. Check it before IsStreaming, which
		// dereferences it. Limit swaps reuse State.Interval as a TTL rather than a
		// sub-swap cadence (see the cap in manager_adv_swap_queue_current), so
		// their idle time is not a staleness signal and they are left out.
		if msg.State == nil || !msg.IsMarketSwap() || !msg.IsStreaming() {
			continue
		}
		foldStream(msg.State.Out, msg.State.LastHeight, msg.State.Interval)
	}
	telemetry.SetGauge(float32(maxIdleBlocks), "thornode", "streaming_swap", "blocks_since_last_sub_swap_max")
	telemetry.SetGauge(float32(stalled), "thornode", "streaming_swap", "stalled")
	telemetry.SetGauge(telem(swappedOut), "thornode", "streaming_swap", "swapped_out")
}

// queueMemoClass reports whether a txout memo is an internal (MIGRATE,
// CONSOLIDATE) or an outbound (OUT, REFUND, RAGNAROK) memo, for the per-chain
// pending-outbound counters only.
//
// It deliberately does not call ParseMemo. Both classes are a pure function of the
// memo's tx-type prefix, which ParseMemo itself derives exactly this way
// (memo_parser.go newParser: cut at '|', then at ':', then StringToTxType); the
// per-type sub-parsers that follow only fill in bodies this function never looks
// at, and every one of them still returns a memo carrying its own tx type when the
// body fails to parse - which the scan's call site discarded anyway. So the
// classification is unchanged while ~2,700 memo parses per gate block collapse to
// two index scans and a map lookup each. An unrecognised prefix counts toward
// neither class, exactly as ParseMemo's EmptyMemo did. The one behavioural
// difference is in ParseMemo's favour-of-nothing direction: a memo whose body
// panicked inside ParseMemo used to be dropped from both counts, and is now
// counted by its prefix.
func queueMemoClass(memo string) (internal, outbound bool) {
	if i := strings.IndexByte(memo, '|'); i >= 0 {
		memo = memo[:i]
	}
	if i := strings.IndexByte(memo, ':'); i >= 0 {
		memo = memo[:i]
	}
	txType, err := StringToTxType(memo)
	if err != nil {
		return false, false
	}
	return txType.IsInternal(), txType.IsOutbound()
}
