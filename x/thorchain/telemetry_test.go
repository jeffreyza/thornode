package thorchain

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"cosmossdk.io/store/rootmulti"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// telemetryEmitters is every emitter reachable from EndBlock. emitEndBlockTelemetry
// calls all of them but recovers panics, so the tests below also call each one
// directly: a panic must fail the test, not silently skip the emitters after it.
var telemetryEmitters = []func(cosmos.Context, Manager){
	emitChainTelemetry,
	emitNetworkStateTelemetry,
	emitStreamingSwapTelemetry,
	emitSecuredAssetTelemetry,
	emitMimirTelemetry,
	emitVaultDetailTelemetry,
	emitSolvencyTelemetry,
	emitChainDetailTelemetry,
	emitQueueDetailTelemetry,
	emitTCYTelemetry,
}

func emitAllTelemetry(t *testing.T, ctx cosmos.Context, mgr Manager) {
	t.Helper()
	if err := emitEndBlockTelemetry(ctx, mgr); err != nil {
		t.Fatalf("height %d: %v", ctx.BlockHeight(), err)
	}
	for _, emit := range telemetryEmitters {
		emit(ctx, mgr)
	}
}

// telemetrySweepHeights opens every gate at least once, in ascending order
// (SetLastChainHeight rejects a backward step, so the seed goes at the first):
// 1_200_000 is residue 0 and divisible by 10, 30, 50, 100 and 600; the next four
// are residues 1-4; the rest isolate the periodic gates one at a time.
var telemetrySweepHeights = []int64{
	1_200_000, 1_200_001, 1_200_002, 1_200_003, 1_200_004,
	1_200_005, 1_200_010, 1_200_030, 1_200_050, 1_200_100,
}

// resetStuckOutboundState clears the process-wide carry-over set so tests do not
// inherit each other's heights.
// resetStuckOutboundState clears the process-wide carry-over set so tests do not
// inherit each other's heights, and marks the startup backfill complete so a test
// asserting on the carry-over cannot pass because the backfill rediscovered the
// item instead. Tests that want the backfill re-arm the cursor themselves.
func resetStuckOutboundState(t *testing.T) {
	t.Helper()
	clear := func() {
		stuckOutboundHeights = map[int64][]common.Chain{}
		stuckOutboundBackfillCursor, stuckOutboundBackfillFloor = 0, 1
	}
	clear()
	t.Cleanup(clear)
}

func setupSeededTelemetry(t *testing.T) (cosmos.Context, *Mgrs) {
	t.Helper()
	ctx, mgr := setupBenchmarkManager(t)
	ctx = ctx.WithBlockHeight(telemetrySweepHeights[0])
	// The sweep leaves the fixture's ~900 seeded txout heights in this process-wide
	// set; clear it so tests do not inherit each other's carry-over.
	resetStuckOutboundState(t)
	// Armed, not complete: the startup backfill is part of the scan path and the
	// write detector in TestTelemetryWritesNoState has to walk it too.
	stuckOutboundBackfillCursor = -1
	seedTelemetryState(t, ctx, mgr)
	return ctx, mgr
}

// snapshotStores copies every key/value in every mounted store. It is the write
// detector for TestTelemetryWritesNoState: the fixture mounts all stores on one
// shared memdb, so a commit hash is not available, and a snapshot of the working
// trees sees uncommitted writes anyway. All stores, not just x/thorchain: auth
// creates module accounts lazily on read, which a thorchain-only scan would miss.
func snapshotStores(t *testing.T, ctx cosmos.Context) map[string][]byte {
	t.Helper()
	rs, ok := ctx.MultiStore().(*rootmulti.Store)
	if !ok {
		t.Fatalf("multistore %T is not a rootmulti.Store", ctx.MultiStore())
	}
	keys := rs.StoreKeysByName()
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	snap := map[string][]byte{}
	for _, name := range names {
		it := rs.GetKVStore(keys[name]).Iterator(nil, nil)
		for ; it.Valid(); it.Next() {
			snap[name+"/"+string(it.Key())] = bytes.Clone(it.Value())
		}
		it.Close()
	}
	return snap
}

// firstStoreDiff returns the first key whose presence or value differs, or "".
func firstStoreDiff(before, after map[string][]byte) string {
	for k, v := range before {
		if w, ok := after[k]; !ok || !bytes.Equal(v, w) {
			return k
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			return k
		}
	}
	return ""
}

// The telemetry path runs only on nodes with telemetry.enabled. A single keeper
// accessor that writes on read (GetOutboundFeeWithheldRune did) diverges state
// between validators and halts the chain. Every gate is opened against a fixture
// where every store the gated loops walk is populated, and no mounted store may
// change by a single byte.
func TestTelemetryWritesNoState(t *testing.T) {
	ctx, mgr := setupSeededTelemetry(t)
	before := snapshotStores(t, ctx)
	if len(before) == 0 {
		t.Fatal("empty snapshot; the guard below proves nothing")
	}

	for _, height := range telemetrySweepHeights {
		emitAllTelemetry(t, ctx.WithBlockHeight(height), mgr)
		if diff := firstStoreDiff(before, snapshotStores(t, ctx)); diff != "" {
			t.Fatalf("telemetry at height %d wrote state: first differing key %q", height, diff)
		}
	}

	// Negative control: a real write is visible to this guard.
	mgr.Keeper().SetMimir(ctx, "TelemetryWriteCanary", 1)
	if diff := firstStoreDiff(before, snapshotStores(t, ctx)); diff == "" {
		t.Fatal("a real write left the snapshot unchanged; the guard above proves nothing")
	}
}

// hasGauge reports whether the sink saw the scalar `name` or any labelled series
// `name{...}`.
func (s *recordingSink) hasGauge(name string) bool {
	if _, ok := s.gauges[name]; ok {
		return true
	}
	return s.gaugesWithPrefix(name+"{") > 0
}

// The stagger is the load-bearing decision of the cost work: each heavy group on
// its own height%5 residue, none sharing one with the pre-existing %10/%30/%50/
// %100/%600 gates except the light vault group on residue 0. A group that moves
// onto an occupied residue doubles the worst block; a flipped comparison stops a
// whole family. Each signature metric must appear exactly on its residue.
func TestTelemetryStagger(t *testing.T) {
	ctx, mgr := setupSeededTelemetry(t)

	signatures := []struct {
		metric string
		on     func(h int64) bool
	}{
		{"thornode.vault.total_value", func(h int64) bool { return h%5 == 0 }},
		{"thornode.node.bond", func(h int64) bool { return h%5 == 1 }},
		{"thornode.pool.balance.rune", func(h int64) bool { return h%5 == 2 }},
		{"thornode.mimir.value", func(h int64) bool { return h%5 == 3 }},
		{"thornode.vault.migrate_round", func(h int64) bool { return h%5 == 3 }},
		{"thornode.solvency.bond_delta", func(h int64) bool { return h%5 == 3 }},
		{"thornode.secured_asset.depth", func(h int64) bool { return h%5 == 4 }},
		{"thornode.pool.outbound_fee.withheld_rune", func(h int64) bool { return h%5 == 4 }},
		{"thornode.outbound.pending", func(h int64) bool { return h%10 == 0 }},
		{"thornode.churn.expected_active_vaults", func(h int64) bool { return h%10 == 0 }},
		{"thornode.node.last_observed_height", func(h int64) bool { return h%30 == 0 }},
		{"thornode.node.preflight_status", func(h int64) bool { return h%30 == 0 }},
		{"thornode.chain.router_info", func(h int64) bool { return true }},
		{"thornode.upgrade_proposal.height", func(h int64) bool { return h%30 == 0 }},
		{"thornode.tcy.claim_outstanding", func(h int64) bool { return h%50 == 0 }},
		{"thornode.solvency.affiliate_collector_delta", func(h int64) bool { return h%50 == 0 }},
		{"thornode.stable_reserve.credit_outstanding", func(h int64) bool { return h%100 == 0 }},
		{"thornode.invariant.broken", func(h int64) bool { return h%600 == 0 }},
	}

	for _, height := range telemetrySweepHeights {
		sink := installRecordingSink(t)
		emitAllTelemetry(t, ctx.WithBlockHeight(height), mgr)
		if n := sink.gaugesWithPrefix("thornode."); n == 0 {
			t.Errorf("height %d: no thornode gauges emitted", height)
		}
		for _, sig := range signatures {
			if got, want := sink.hasGauge(sig.metric), sig.on(height); got != want {
				t.Errorf("height %d: %s emitted=%v, want %v", height, sig.metric, got, want)
			}
		}
	}
}

// Emitted values and labels for the derived series: the withheld-record key
// round-trip, the credit key/value split, the migrate_round formula and per-node
// observation heights. Each is asserted against the value the keeper hands the
// consensus path, so a helper that quietly starts returning zero fails here.
func TestTelemetryEmittedValues(t *testing.T) {
	ctx, mgr := setupSeededTelemetry(t)
	k := mgr.Keeper()
	base := ctx.BlockHeight()

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	must(t, k.SetPool(ctx, pool))
	// Large enough that a float32 built the wrong way (differencing two telem()
	// results, or converting a raw Uint64) visibly loses digits. The read-back
	// withheld also carries the lazy-init equilibrium, so the true surplus has a
	// fractional RUNE part; at 5e7 RUNE one float32 ULP is 4 RUNE, so differencing
	// two telem() results rounds it to a multiple of 4 (6900 vs 6899.55).
	must(t, k.AddToOutboundFeeWithheldRune(ctx, common.BTCAsset, cosmos.NewUint(5_000_000_300_000_000)))
	must(t, k.AddToOutboundFeeSpentRune(ctx, common.BTCAsset, cosmos.NewUint(5_000_000_000_000_000)))
	// The value GasMgr reads; non-zero now, so this read does not write.
	withheld, err := k.GetOutboundFeeWithheldRune(ctx, common.BTCAsset)
	must(t, err)
	spent, err := k.GetOutboundFeeSpentRune(ctx, common.BTCAsset)
	must(t, err)

	// Two contributors under one asset: the "<ASSET>/<ADDRESS>" key loses the
	// address, the "<address>/<amount>" value keeps only the amount, and the two sum.
	must(t, k.SetStableReserveCredit(ctx, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(7*common.One)))
	must(t, k.SetStableReserveCredit(ctx, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(3*common.One)))

	interval := k.GetConfigInt64(ctx, constants.FundMigrationInterval)
	retiring := GetRandomVault()
	retiring.Status = RetiringVault
	retiring.StatusSince = base + 3 - 2*interval // two full intervals ago at height base+3
	must(t, k.SetVault(ctx, retiring))
	active := GetRandomVault()
	active.Status = ActiveVault
	must(t, k.SetVault(ctx, active))

	observing := GetRandomValidatorNode(NodeActive)
	must(t, k.SetNodeAccount(ctx, observing))
	must(t, k.SetLastObserveHeight(ctx, common.BTCChain, observing.NodeAddress, 777_777))
	silent := GetRandomValidatorNode(NodeActive)
	must(t, k.SetNodeAccount(ctx, silent))

	poolLabels := "{pool=BTC.BTC,status=Available}"
	vaultKey := func(v Vault) string {
		return fmt.Sprintf("{vault_type=%s,pubkey=%s}", v.Type.String(), v.PubKey.String())
	}
	cases := []struct {
		height int64
		metric string
		want   float32
	}{
		{base + 4, "thornode.pool.outbound_fee.withheld_rune" + poolLabels, telem(withheld)},
		{base + 4, "thornode.pool.outbound_fee.spent_rune" + poolLabels, telem(spent)},
		{base + 4, "thornode.pool.outbound_fee.surplus_rune" + poolLabels, telem(withheld.Sub(spent))},
		{base + 100, "thornode.stable_reserve.credit_outstanding{asset=BTC.BTC}", 10},
		{base + 3, "thornode.vault.migrate_round" + vaultKey(retiring), 3},
		{base + 3, "thornode.vault.migrate_round" + vaultKey(active), 0},
		{base + 30, fmt.Sprintf("thornode.node.last_observed_height{node_address=%s,chain=BTC}", observing.NodeAddress), 777_777},
		{base + 50, "thornode.tcy.claim_outstanding_count", benchTCYClaimers},
		// sum of 1..benchTCYClaimers base units, from the seed
		{base + 50, "thornode.tcy.claim_outstanding", telem(cosmos.NewUint(benchTCYClaimers * (benchTCYClaimers + 1) / 2))},
	}
	for _, tc := range cases {
		sink := installRecordingSink(t)
		emitAllTelemetry(t, ctx.WithBlockHeight(tc.height), mgr)
		got, ok := sink.gauges[tc.metric]
		if !ok {
			t.Errorf("height %d: %s not emitted", tc.height, tc.metric)
			continue
		}
		if math.Abs(float64(got-tc.want)) > 1e-6*math.Max(1, math.Abs(float64(tc.want))) {
			t.Errorf("height %d: %s = %v, want %v", tc.height, tc.metric, got, tc.want)
		}
	}

	// A node with no observation record must have no series at all - not a zero
	// that reads as "observing height 0".
	sink := installRecordingSink(t)
	emitAllTelemetry(t, ctx.WithBlockHeight(base+30), mgr)
	if n := sink.gaugesWithPrefix("thornode.node.last_observed_height{node_address=" + silent.NodeAddress.String()); n != 0 {
		t.Errorf("silent node has %d last_observed_height series, want none", n)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Counters are process-local and cannot roll back. The querier's quote simulation
// runs the real handlers under CtxSimulationMode, so every counter on a path a
// quote can reach is guarded by isSimulationMode - without it API traffic drowns
// the on-chain signal. The swap queue case is the path a quote actually takes:
// SwapQ().EndBlock with a swap that fails and is refunded, which must record no
// thornode counter of any name under simulation. refundTx and EventMgr.EmitEvent
// pin the two widest guards individually.
func TestTelemetrySimulationModeSuppressesCounters(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	// native outbounds need an active asgard to sign from
	asgard := GetRandomVault()
	asgard.Status = ActiveVault
	must(t, mgr.Keeper().SetVault(ctx, asgard))
	simCtx := ctx.WithValue(constants.CtxSimulationMode, true)
	if isSimulationMode(ctx) || !isSimulationMode(simCtx) {
		t.Fatal("isSimulationMode does not follow the context value")
	}

	refund := func(c cosmos.Context) {
		tx := common.NewTx(
			GetRandomTxHash(), GetRandomTHORAddress(), GetRandomTHORAddress(),
			common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(10*common.One))},
			common.Gas{common.NewCoin(common.RuneNative, cosmos.NewUint(2_000_000))},
			"SWAP:BTC.BTC",
		)
		must(t, refundTx(c, ObservedTx{Tx: tx, BlockHeight: c.BlockHeight()}, mgr, CodeSwapFail, "test", ""))
	}
	emit := func(c cosmos.Context) {
		must(t, mgr.EventMgr().EmitEvent(c, NewEventSetMimir("TelemetryTest", "1")))
	}
	// no BTC pool exists, so the swap fails in the handler and is refunded
	swapQueue := func(c cosmos.Context) {
		btcAddr := GetRandomBTCAddress()
		tx := common.NewTx(
			GetRandomTxHash(), GetRandomTHORAddress(), GetRandomTHORAddress(),
			common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(10*common.One))},
			common.Gas{common.NewCoin(common.RuneNative, cosmos.NewUint(2_000_000))},
			"=:BTC.BTC:"+btcAddr.String(),
		)
		msg := NewMsgSwap(tx, common.BTCAsset, btcAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
		must(t, mgr.Keeper().SetSwapQueueItem(c, *msg, 0))
		must(t, mgr.SwapQ().EndBlock(c, mgr))
	}

	for _, tc := range []struct {
		name   string
		run    func(cosmos.Context)
		metric string
	}{
		{"swap_queue", swapQueue, "thornode.swap.failed{"},
		{"refund", refund, "thornode.refund{"},
		{"event", emit, "thornode.event.emitted{"},
	} {
		sink := installRecordingSink(t)
		tc.run(simCtx)
		if n := countersWithPrefix(sink, "thornode."); n != 0 {
			t.Errorf("%s: %d thornode counters under simulation mode, want 0: %v", tc.name, n, counterNames(sink))
		}
		tc.run(ctx)
		if n := countersWithPrefix(sink, tc.metric); n == 0 {
			t.Errorf("%s: no %s counter outside simulation mode; the fixture does not reach the counter", tc.name, tc.metric)
		}
	}
}

func counterNames(s *recordingSink) []string {
	names := make([]string, 0, len(s.counters))
	for k := range s.counters {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func countersWithPrefix(s *recordingSink, prefix string) int {
	n := 0
	for k := range s.counters {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// swapQueueFailureReason is the only thing standing between free-text swap errors
// and the reason label. The string matches are coupled to messages in
// swap_current.go and helpers.go by nothing but this test: the errors below are
// built with the exact formats those files use.
func TestSwapQueueFailureReason(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{errSwapFailInvalidAmount, "invalid_amount"},
		{fmt.Errorf("wrapped: %w", errSwapFailInvalidBalance), "invalid_balance"},
		{errSwapFailNotEnoughBalance, "not_enough_balance"},
		// swap_current.go
		{fmt.Errorf("emit asset %s less than price limit %s", cosmos.NewUint(1), cosmos.NewUint(2)), "price_limit"},
		{errors.New("zero emit asset"), "zero_emit_asset"},
		{fmt.Errorf("pool %s doesn't exist", common.BTCAsset), "pool_not_exist"},
		{errors.New("burning synthetics has been disabled"), "disabled"},
		{errors.New("minting synthetics has been disabled"), "disabled"},
		// helpers.go
		{fmt.Errorf("insufficient synth capacity: want=%d have=%d", 5, 1), "synth_cap"},
		{errors.New("something new"), "other"},
	} {
		if got := swapQueueFailureReason(tc.err); got != tc.want {
			t.Errorf("swapQueueFailureReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// The blame round in MsgTssPool is node-supplied text. The label must collapse to
// the seven keygen rounds or "other", whatever the raw tss-lib phase name looks like.
func TestCanonicalKeygenRoundLabel(t *testing.T) {
	for _, tc := range []struct{ round, want string }{
		{tssMessages.KEYGEN1, tssMessages.KEYGEN1},
		{"thorchain.tsslib.ecdsa.keygen.KGRound1Message", tssMessages.KEYGEN1},
		{"thorchain.tsslib.ecdsa.keygen.KGRound2Message1", tssMessages.KEYGEN2aUnicast},
		{"thorchain.tsslib.ecdsa.keygen.KGRound2Message2", tssMessages.KEYGEN2b},
		{"thorchain.tsslib.ecdsa.keygen.KGRound3Message", tssMessages.KEYGEN3},
		{"thorchain.tsslib.eddsa.keygen.KGRound1Message", tssMessages.EDDSAKEYGEN1},
		{"thorchain.tsslib.eddsa.keygen.KGRound2Message1", tssMessages.EDDSAKEYGEN2a},
		{"thorchain.tsslib.eddsa.keygen.KGRound2Message2", tssMessages.EDDSAKEYGEN2b},
		// keysign rounds, empty and garbage all collapse
		{tssMessages.KEYSIGN1aUnicast, "other"},
		{"thorchain.tsslib.ecdsa.signing.SignRound3Message", "other"},
		{"", "other"},
		{strings.Repeat("x", 512), "other"},
	} {
		if got := canonicalKeygenRoundLabel(tc.round); got != tc.want {
			t.Errorf("canonicalKeygenRoundLabel(%q) = %q, want %q", tc.round, got, tc.want)
		}
	}
}

// queueMemoClass classifies off the memo prefix instead of a full ParseMemo. This
// pins the equivalence for every tx type plus the malformed shapes the queue holds.
func TestQueueMemoClass(t *testing.T) {
	version := GetCurrentVersion()
	hash := GetRandomTxHash().String()
	memos := []string{
		"OUT:" + hash, "REFUND:" + hash, "MIGRATE:100", "RAGNAROK:100", "CONSOLIDATE",
		"SWAP:BTC.BTC:" + GetRandomBTCAddress().String(), "=:BTC.BTC", "ADD:BTC.BTC",
		"BOND:" + GetRandomBech32Addr().String(), "NOOP", "noop:NOVAULT", "OUT|" + hash,
		"out:" + hash, "", ":", "garbage:" + hash, "OUT", "OUT:",
	}
	for _, m := range memos {
		internal, outbound := queueMemoClass(m)
		parsed, err := ParseMemo(version, m)
		// every sub-parser returns a memo carrying its type even when the body fails;
		// an unrecognised prefix yields TxUnknown, which is in neither class
		wantInternal, wantOutbound := parsed.GetType().IsInternal(), parsed.GetType().IsOutbound()
		if internal != wantInternal || outbound != wantOutbound {
			t.Errorf("queueMemoClass(%q) = (%v,%v), ParseMemo says (%v,%v) err=%v", m, internal, outbound, wantInternal, wantOutbound, err)
		}
	}
}

func TestSolvencyDelta(t *testing.T) {
	one := cosmos.NewUint(common.One)
	for _, tc := range []struct {
		held, owed cosmos.Uint
		want       float32
	}{
		{one.MulUint64(5), one.MulUint64(3), 2},
		{one.MulUint64(3), one.MulUint64(5), -2},
		{one, one, 0},
		{cosmos.Uint{}, one, 0},
		{one, cosmos.Uint{}, 0},
		// a divergence far below float32 resolution at module scale survives the Uint subtraction
		{cosmos.NewUint(1_000_000_000 * common.One).AddUint64(1), cosmos.NewUint(1_000_000_000 * common.One), telem(cosmos.NewUint(1))},
	} {
		if got := solvencyDelta(tc.held, tc.owed); got != tc.want {
			t.Errorf("solvencyDelta(%v,%v) = %v, want %v", tc.held, tc.owed, got, tc.want)
		}
	}
}

// The chain rails precompute every mimir key the halt gauges read. A key that
// drifts from what the halt handlers write silently zeroes that series.
func TestChainTelemRails(t *testing.T) {
	if len(chainTelemRails) != len(common.AllChains) {
		t.Fatalf("%d rails for %d chains", len(chainTelemRails), len(common.AllChains))
	}
	for _, rail := range chainTelemRails {
		chain := rail.chain.String()
		for _, kv := range []struct{ got, want string }{
			{rail.haltKey, fmt.Sprintf("Halt%sChain", chain)},
			{rail.solvencyKey, fmt.Sprintf("SolvencyHalt%sChain", chain)},
			{rail.signingHaltKey, fmt.Sprintf(constants.MimirTemplateHaltSigning, chain)},
			{rail.assetHalts[0].mimirKey, fmt.Sprintf(constants.MimirTemplateSecuredAssetHaltDeposit, chain)},
			{rail.assetHalts[1].mimirKey, fmt.Sprintf(constants.MimirTemplateSecuredAssetHaltWithdraw, chain)},
			{rail.assetHalts[2].mimirKey, fmt.Sprintf(constants.MimirTemplateTradeAssetHaltDeposit, chain)},
			{rail.assetHalts[3].mimirKey, fmt.Sprintf(constants.MimirTemplateTradeAssetHaltWithdraw, chain)},
		} {
			if kv.got != kv.want {
				t.Errorf("%s: key %q, want %q", chain, kv.got, kv.want)
			}
		}
	}
}

// An outbound whose reschedule failed keeps an empty OutHash at its original
// height forever - LackSigning looks at that height exactly once. The sliding
// scan window alone would drop it after 3x SigningTransactionPeriod and report
// the chain as drained while the funds are still stranded, which is the alert
// clearing itself. stuckOutboundHeights is what keeps it visible.
func TestStuckOutboundOutlivesScanWindow(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const stuckHeight = 1_200_000
	ctx = ctx.WithBlockHeight(stuckHeight)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))

	txOut := NewTxOut(stuckHeight)
	txOut.TxArray = append(txOut.TxArray, TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		Memo:        "OUT:" + GetRandomTxHash().String(),
		InHash:      GetRandomTxHash(),
	})
	must(t, k.SetTxOut(ctx, txOut))

	const count = "thornode.outbound.pending{chain=BTC,memo_type=outbound}"
	const age = "thornode.outbound.pending_oldest_blocks{chain=BTC}"
	// The scan gate is IsPeriodLastBlock(ctx, 10).
	scan := func(height int64) map[string]float32 {
		if height%10 != 0 {
			t.Fatalf("height %d does not open the scan gate", height)
		}
		sink := installRecordingSink(t)
		emitQueueDetailTelemetry(ctx.WithBlockHeight(height), mgr)
		return sink.gauges
	}
	check := func(what string, got map[string]float32, wantCount, wantAge float32) {
		t.Helper()
		if got[count] != wantCount {
			t.Errorf("%s: %s = %v, want %v", what, count, got[count], wantCount)
		}
		if got[age] != wantAge {
			t.Errorf("%s: %s = %v, want %v", what, age, got[age], wantAge)
		}
	}

	// Inside the window: found by the plain walk, and recorded as still unsigned.
	check("in window", scan(stuckHeight+10), 1, 10)

	// Past the window: only the carried-over height can find it now.
	past := stuckHeight + window + 10
	if past%10 != 0 {
		past += 10 - past%10
	}
	check("past window", scan(past), 1, float32(past-stuckHeight))

	// Signed at last: the height drops out of the carry-over and the series
	// returns to zero rather than staying latched.
	txOut.TxArray[0].OutHash = GetRandomTxHash()
	must(t, k.SetTxOut(ctx, txOut))
	check("signed", scan(past+10), 0, 0)
	if _, ok := stuckOutboundHeights[stuckHeight]; ok {
		t.Errorf("height %d still carried after the outbound was signed", stuckHeight)
	}
}

// Past maxStuckOutboundHeights the excess is carried forward unscanned, not
// dropped: dropping it would put those heights straight back into the blind spot
// the carry-over exists to close. The tail also aliases the scan slice's backing
// array, so a reslice here silently loses it.
func TestStuckOutboundOverflowIsCarriedNotDropped(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const first = 1_200_000
	last := int64(first + maxStuckOutboundHeights) // one more than the cap scans
	ctx = ctx.WithBlockHeight(first)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))
	for h := int64(first); h <= last; h++ {
		txOut := NewTxOut(h)
		txOut.TxArray = append(txOut.TxArray, TxOutItem{
			Chain:       common.BTCChain,
			ToAddress:   GetRandomBTCAddress(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
			Memo:        "OUT:" + GetRandomTxHash().String(),
			InHash:      GetRandomTxHash(),
		})
		must(t, k.SetTxOut(ctx, txOut))
	}

	// All of them inside the window: every height gets recorded. The scan gate is
	// IsPeriodLastBlock(ctx, 10).
	inWindow := last + 10
	inWindow -= inWindow % 10
	emitQueueDetailTelemetry(ctx.WithBlockHeight(inWindow), mgr)
	if n := len(stuckOutboundHeights); n != maxStuckOutboundHeights+1 {
		t.Fatalf("recorded %d heights, want %d", n, maxStuckOutboundHeights+1)
	}

	// All of them past the window: only the oldest maxStuckOutboundHeights are
	// re-scanned, and the newest must survive as overflow.
	past := inWindow + window + 10
	past -= past % 10
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past), mgr)
	if _, ok := stuckOutboundHeights[last]; !ok {
		t.Errorf("overflow height %d dropped", last)
	}
	if n := len(stuckOutboundHeights); n != maxStuckOutboundHeights+1 {
		t.Errorf("carried %d heights, want %d", n, maxStuckOutboundHeights+1)
	}
}

// Chain.Valid only checks length and uppercase-ness, so a chain reaching a handler
// on an inbound message - or derived from a user-supplied memo asset - is
// attacker-chosen. Every label value must come from common.AllChains or be "other".
func TestTelemChainBoundsLabelValues(t *testing.T) {
	for _, c := range common.AllChains {
		if got := telemChain(c); got != c.String() {
			t.Errorf("telemChain(%s) = %q, want %q", c, got, c.String())
		}
	}
	// Lowercase spellings normalise rather than falling through to "other".
	if got := telemChain(common.Chain("btc")); got != common.BTCChain.String() {
		t.Errorf(`telemChain("btc") = %q, want %q`, got, common.BTCChain.String())
	}
	for _, c := range []common.Chain{"", "AAA", "ZZZZZZZZZZ", "btcx", "OTHER"} {
		if got := telemChain(c); got != "other" {
			t.Errorf("telemChain(%q) = %q, want \"other\"", c, got)
		}
	}
}

// A gap resolving by way of the adjusted balance reaching zero used to skip the
// zero write - the c.IsEmpty() branch continued before the emission - so
// solvency_gap_rune stayed pinned at its last positive value until retention
// expired, while insolvencyCheck returned false and the caller cleared the
// insolvency flag. The alert outlived the incident.
func TestSolvencyGapZeroesWhenPendingOutboundDrainsVault(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	ctx = ctx.WithBlockHeight(1_200_000)

	vault := GetRandomVault()
	vault.Chains = common.Chains{common.ETHChain}.Strings()
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)))
	must(t, k.SetVault(ctx, vault))

	// A gap is only priceable through a pool with depth; without one
	// insolvencyCheck bails out as insolvent before the interesting branch.
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1_000_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	pool.Status = PoolAvailable
	must(t, k.SetPool(ctx, pool))

	gauge := "thornode.solvency.gap_rune{asset=ETH.ETH,pubkey=" + vault.PubKey.String() + "}"

	// Wallet reports nothing: the whole vault balance is a gap.
	sink := installRecordingSink(t)
	insolvencyCheck(ctx, mgr, vault, common.Coins{}, common.ETHChain)
	if got, ok := sink.gauges[gauge]; !ok || got <= 0 {
		t.Fatalf("positive gap: %s = %v (present %v), want > 0", gauge, got, ok)
	}

	// A pending outbound for the full balance drains the adjusted vault to zero,
	// which is how such a gap actually resolves - outbound recovery owns the
	// liability and insolvencyCheck stops flagging.
	txOut := NewTxOut(ctx.BlockHeight() - 1)
	txOut.TxArray = append(txOut.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:" + GetRandomTxHash().String(),
		InHash:      GetRandomTxHash(),
	})
	must(t, k.SetTxOut(ctx, txOut))

	sink = installRecordingSink(t)
	if insolvencyCheck(ctx, mgr, vault, common.Coins{}, common.ETHChain) {
		t.Fatal("vault still reported insolvent once the pending outbound covers the balance")
	}
	got, ok := sink.gauges[gauge]
	if !ok {
		t.Fatalf("%s not written; the stale positive value stays pinned", gauge)
	}
	if got != 0 {
		t.Errorf("%s = %v, want 0", gauge, got)
	}
}

// GetPools returns savers pools (BTC/BTC) and native pools (THOR.TCY) alongside
// the external layer-1 ones. Their assets sit in module accounts, not in an
// asgard, so vaultAssetTotals never has an entry for them: before the
// IsExternalL1Asset guard each one emitted held == 0 against a positive backing
// and pinned vault_backing_delta negative on healthy accounting.
func TestVaultBackingSkipsSaversAndNativePools(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	// emitVaults is ctx.BlockHeight()%5 == 0.
	ctx = ctx.WithBlockHeight(1_200_000)

	funded := func(asset common.Asset) Pool {
		p := NewPool()
		p.Asset = asset
		p.BalanceRune = cosmos.NewUint(1_000 * common.One)
		p.BalanceAsset = cosmos.NewUint(1_000 * common.One)
		p.LPUnits = cosmos.NewUint(1_000 * common.One)
		p.Status = PoolAvailable
		return p
	}
	savers := funded(common.BTCAsset.GetSyntheticAsset())
	must(t, k.SetPool(ctx, savers))
	tcy := funded(common.TCY)
	must(t, k.SetPool(ctx, tcy))
	must(t, k.SetPool(ctx, funded(common.BTCAsset)))

	sink := installRecordingSink(t)
	must(t, emitEndBlockTelemetry(ctx, mgr))

	for _, asset := range []common.Asset{savers.Asset, tcy.Asset} {
		key := "thornode.vault.backing.delta{pool=" + asset.String() + "}"
		if got, ok := sink.gauges[key]; ok {
			t.Errorf("%s emitted (%v); the pool's asset is not custodied in a vault", key, got)
		}
	}
	if _, ok := sink.gauges["thornode.vault.backing.delta{pool=BTC.BTC}"]; !ok {
		t.Error("thornode.vault.backing.delta{pool=BTC.BTC} missing; the guard dropped a real layer-1 pool")
	}
}

// The streaming-swap store only holds legacy records - SwapHandler writes one
// only for msg.IsLegacyStreaming() (StreamInterval > 0 AND version v1). V2 keeps
// its progress in MsgSwap.State on the adv swap queue, so with an empty legacy
// store all three streaming gauges used to read zero however many V2 streams
// were wedged with undelivered output.
func TestStreamingGaugesIncludeV2Streams(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	ctx = ctx.WithBlockHeight(1_200_000)

	// V2-only fixture: nothing in the legacy streaming store.
	iter := k.GetStreamingSwapIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var swp StreamingSwap
		must(t, k.Cdc().Unmarshal(iter.Value(), &swp))
		k.RemoveStreamingSwap(ctx, swp.TxID)
	}
	iter.Close()

	const idle, interval = 500, 10
	msg := MsgSwap{
		Tx: common.NewTx(GetRandomTxHash(), GetRandomTHORAddress(), GetRandomTHORAddress(),
			common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))),
			common.Gas{}, "=:BTC.BTC"),
		TargetAsset: common.BTCAsset,
		Destination: GetRandomBTCAddress(),
		Signer:      GetRandomBech32Addr(),
		Version:     types.SwapVersion_v2,
		SwapType:    types.SwapType_market,
		State: &types.SwapState{
			Interval:   interval,
			Quantity:   10,
			Count:      3,
			LastHeight: ctx.BlockHeight() - idle,
			Deposit:    cosmos.NewUint(100 * common.One),
			In:         cosmos.NewUint(30 * common.One),
			Out:        cosmos.NewUint(7 * common.One),
		},
	}
	must(t, k.SetAdvSwapQueueItem(ctx, msg))

	sink := installRecordingSink(t)
	emitQueueDetailTelemetry(ctx, mgr)

	for key, want := range map[string]float32{
		"thornode.streaming_swap.blocks_since_last_sub_swap_max": idle,
		"thornode.streaming_swap.stalled":                        1,
		"thornode.streaming_swap.swapped_out":                    7,
	} {
		if got := sink.gauges[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

// The scan cap is global; the gauges it feeds are per chain. Without the
// per-chain promotion, a wall of stuck BTC heights fills the cap and the only
// stuck ETH height is carried in the overflow only - ETH then reports zero count,
// zero age and zero value while its outbound is still stranded, and the alert
// clears on a chain nobody has fixed.
func TestStuckOutboundCapKeepsEveryChainVisible(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const first = 1_200_000
	ctx = ctx.WithBlockHeight(first)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))

	put := func(height int64, chain common.Chain, asset common.Asset, to common.Address) {
		txOut := NewTxOut(height)
		txOut.TxArray = append(txOut.TxArray, TxOutItem{
			Chain:       chain,
			ToAddress:   to,
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(asset, cosmos.NewUint(common.One)),
			Memo:        "OUT:" + GetRandomTxHash().String(),
			InHash:      GetRandomTxHash(),
		})
		must(t, k.SetTxOut(ctx, txOut))
	}

	// Exactly enough BTC heights to fill the cap, then the only ETH height, then
	// two more BTC heights that must stay in the overflow.
	for i := 0; i < maxStuckOutboundHeights; i++ {
		put(first+int64(i), common.BTCChain, common.BTCAsset, GetRandomBTCAddress())
	}
	ethHeight := int64(first + maxStuckOutboundHeights)
	put(ethHeight, common.ETHChain, common.ETHAsset, GetRandomETHAddress())
	lastBTC := ethHeight + 2
	put(ethHeight+1, common.BTCChain, common.BTCAsset, GetRandomBTCAddress())
	put(lastBTC, common.BTCChain, common.BTCAsset, GetRandomBTCAddress())

	// First scan with everything inside the window, so every height is recorded
	// along with the chain that holds it. The scan gate is IsPeriodLastBlock(ctx, 10).
	inWindow := lastBTC + 10
	inWindow -= inWindow % 10
	emitQueueDetailTelemetry(ctx.WithBlockHeight(inWindow), mgr)

	// Now all of them are out of window and the cap bites.
	past := inWindow + window + 10
	past -= past % 10
	sink := installRecordingSink(t)
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past), mgr)

	for key, want := range map[string]float32{
		"thornode.outbound.pending{chain=ETH,memo_type=outbound}": 1,
		"thornode.outbound.pending_oldest_blocks{chain=ETH}":      float32(past - ethHeight),
		"thornode.outbound.pending{chain=BTC,memo_type=outbound}": maxStuckOutboundHeights,
		"thornode.outbound.pending_oldest_blocks{chain=BTC}":      float32(past - first),
		// The two BTC heights past the cap: BTC is already represented, so they
		// are not promoted and the truncation is reported rather than hidden.
		"thornode.outbound.scan_truncated_heights": 2,
	} {
		if got := sink.gauges[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}

	// Truncated heights are still carried, chains and all, so they come back into
	// range as the older ones clear.
	if _, ok := stuckOutboundHeights[lastBTC]; !ok {
		t.Errorf("truncated height %d dropped", lastBTC)
	}
}

// The carry-over set is in-memory, so a restart - which every node performs at
// every upgrade - starts empty. Without the backfill an outbound already stranded
// past the ~900-block window is invisible again and nothing ever rediscovers it:
// the sliding window cannot reach it and the carry-over has nothing to carry.
func TestStuckOutboundBackfillRediscoversAfterRestart(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const stuckHeight = 1_200_000
	ctx = ctx.WithBlockHeight(stuckHeight)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))

	txOut := NewTxOut(stuckHeight)
	txOut.TxArray = append(txOut.TxArray, TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		Memo:        "OUT:" + GetRandomTxHash().String(),
		InHash:      GetRandomTxHash(),
	})
	must(t, k.SetTxOut(ctx, txOut))

	// The restart: an empty set, well past the window, with the backfill armed.
	stuckOutboundBackfillCursor = -1
	past := stuckHeight + window + 500
	past -= past % 10

	const count = "thornode.outbound.pending{chain=BTC,memo_type=outbound}"
	const age = "thornode.outbound.pending_oldest_blocks{chain=BTC}"
	const remaining = "thornode.outbound.scan_backfill_remaining"

	// First scan after the restart. The window cannot reach the item and the
	// carry-over is empty, so only the backfill can find it - and it is counted in
	// the same scan that found it, not merely recorded for the next one.
	sink := installRecordingSink(t)
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past), mgr)
	if got, want := sink.gauges[count], float32(1); got != want {
		t.Fatalf("first scan: %s = %v, want %v", count, got, want)
	}
	if got, want := sink.gauges[age], float32(past-stuckHeight); got != want {
		t.Errorf("first scan: %s = %v, want %v", age, got, want)
	}
	if _, ok := stuckOutboundHeights[stuckHeight]; !ok {
		t.Fatalf("backfill did not record height %d", stuckHeight)
	}
	// Still more history to walk, so coverage is reported incomplete.
	if got := sink.gauges[remaining]; got <= 0 {
		t.Fatalf("first scan: %s = %v, want > 0 while coverage is incomplete", remaining, got)
	}

	// And it stays visible on the following scan, now via the carry-over.
	sink = installRecordingSink(t)
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past+10), mgr)
	if got, want := sink.gauges[count], float32(1); got != want {
		t.Errorf("second scan: %s = %v, want %v", count, got, want)
	}
	if got, want := sink.gauges[age], float32(past+10-stuckHeight); got != want {
		t.Errorf("second scan: %s = %v, want %v", age, got, want)
	}

	// The walk is bounded per scan, so it takes several scans to land; drive it to
	// completion and the coverage gauge must reach zero and stay there.
	for i := 0; i < stuckOutboundBackfillDepth/stuckOutboundBackfillPerScan+2; i++ {
		sink = installRecordingSink(t)
		emitQueueDetailTelemetry(ctx.WithBlockHeight(past+20+int64(i)*10), mgr)
		if sink.gauges[remaining] == 0 {
			break
		}
	}
	if got := sink.gauges[remaining]; got != 0 {
		t.Errorf("%s = %v after driving the backfill to completion, want 0", remaining, got)
	}
}

// The scan where the backfill finishes is the one that first reports coverage
// complete. Anything that final batch turns up has to be counted in that same
// scan: recording it for the next one leaves a full scan during which
// scan_backfill_remaining reads 0 - an unambiguous all-clear - while a
// just-discovered stranded outbound is in no pending gauge at all.
func TestStuckOutboundBackfillCountsItsFinalBatch(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	// Put the stuck height on the backfill floor, so it is reachable only by the
	// very last batch of the walk.
	const scanHeight = 1_800_000
	ctx = ctx.WithBlockHeight(scanHeight)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))
	stuckHeight := scanHeight - window - stuckOutboundBackfillDepth
	if stuckHeight < 1 {
		t.Fatalf("fixture height %d is below genesis", stuckHeight)
	}

	txOut := NewTxOut(stuckHeight)
	txOut.TxArray = append(txOut.TxArray, TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		Memo:        "OUT:" + GetRandomTxHash().String(),
		InHash:      GetRandomTxHash(),
	})
	must(t, k.SetTxOut(ctx, txOut))

	stuckOutboundBackfillCursor = -1
	const count = "thornode.outbound.pending{chain=BTC,memo_type=outbound}"
	const age = "thornode.outbound.pending_oldest_blocks{chain=BTC}"
	const remaining = "thornode.outbound.scan_backfill_remaining"

	// Drive scans until coverage first reads complete, then assert on THAT scan.
	var sink *recordingSink
	var at int64
	for i := 0; i < stuckOutboundBackfillDepth/stuckOutboundBackfillPerScan+4; i++ {
		at = scanHeight + int64(i)*10
		sink = installRecordingSink(t)
		emitQueueDetailTelemetry(ctx.WithBlockHeight(at), mgr)
		if sink.gauges[remaining] == 0 {
			break
		}
	}
	if got := sink.gauges[remaining]; got != 0 {
		t.Fatalf("%s = %v; the backfill never completed", remaining, got)
	}
	if got, want := sink.gauges[count], float32(1); got != want {
		t.Errorf("on the scan that first reports complete coverage: %s = %v, want %v", count, got, want)
	}
	if got, want := sink.gauges[age], float32(at-stuckHeight); got != want {
		t.Errorf("on the scan that first reports complete coverage: %s = %v, want %v", age, got, want)
	}
}

// The promotion past the cap must be decided on what this scan actually found,
// not on the chain lists the last scan recorded. Otherwise a chain whose item
// inside the capped window has since been signed still counts as covered, and its
// one remaining unsigned height past the cap is skipped - the chain reports zero
// pending while an outbound is still stranded on it.
func TestStuckOutboundPromotionUsesFreshCoverage(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const first = 1_200_000
	ctx = ctx.WithBlockHeight(first)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))

	item := func(chain common.Chain, asset common.Asset, to common.Address) TxOutItem {
		return TxOutItem{
			Chain:       chain,
			ToAddress:   to,
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(asset, cosmos.NewUint(common.One)),
			Memo:        "OUT:" + GetRandomTxHash().String(),
			InHash:      GetRandomTxHash(),
		}
	}

	// The capped window: every one of these heights holds BOTH a BTC and an ETH
	// outbound, so after the first scan each is recorded as covering both chains.
	for i := 0; i < maxStuckOutboundHeights; i++ {
		txOut := NewTxOut(first + int64(i))
		txOut.TxArray = append(txOut.TxArray,
			item(common.BTCChain, common.BTCAsset, GetRandomBTCAddress()),
			item(common.ETHChain, common.ETHAsset, GetRandomETHAddress()),
		)
		must(t, k.SetTxOut(ctx, txOut))
	}
	// The only ETH outbound that will still be unsigned, past the cap.
	ethHeight := int64(first + maxStuckOutboundHeights)
	ethOut := NewTxOut(ethHeight)
	ethOut.TxArray = append(ethOut.TxArray, item(common.ETHChain, common.ETHAsset, GetRandomETHAddress()))
	must(t, k.SetTxOut(ctx, ethOut))

	// First scan, everything in window: each capped height records {BTC, ETH}.
	inWindow := ethHeight + 10
	inWindow -= inWindow % 10
	emitQueueDetailTelemetry(ctx.WithBlockHeight(inWindow), mgr)

	// Now sign every ETH item inside the capped window. The recorded chain lists
	// still say ETH; the store no longer does.
	for i := 0; i < maxStuckOutboundHeights; i++ {
		h := int64(first + i)
		txOut, err := k.GetTxOut(ctx, h)
		must(t, err)
		for j := range txOut.TxArray {
			if txOut.TxArray[j].Chain.Equals(common.ETHChain) {
				txOut.TxArray[j].OutHash = GetRandomTxHash()
			}
		}
		must(t, k.SetTxOut(ctx, txOut))
	}

	past := inWindow + window + 10
	past -= past % 10
	sink := installRecordingSink(t)
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past), mgr)

	for key, want := range map[string]float32{
		"thornode.outbound.pending{chain=ETH,memo_type=outbound}": 1,
		"thornode.outbound.pending_oldest_blocks{chain=ETH}":      float32(past - ethHeight),
		"thornode.outbound.pending{chain=BTC,memo_type=outbound}": maxStuckOutboundHeights,
		"thornode.outbound.pending_oldest_blocks{chain=BTC}":      float32(past - first),
	} {
		if got := sink.gauges[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

// pending already holds the current scan window by the time promotion runs, so
// deciding coverage on mere presence lets ordinary recent traffic stand in for a
// stranded ancestor: one fresh ETH outbound inside the window marks ETH covered,
// its capped-out stuck height is skipped, and pending_oldest_blocks reports the
// fresh item's age. The alert clears while the old outbound is still unsigned.
func TestStuckOutboundPromotionIgnoresRecentTrafficOnSameChain(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const first = 1_200_000
	ctx = ctx.WithBlockHeight(first)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))

	put := func(height int64, chain common.Chain, asset common.Asset, to common.Address) {
		txOut := NewTxOut(height)
		txOut.TxArray = append(txOut.TxArray, TxOutItem{
			Chain:       chain,
			ToAddress:   to,
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(asset, cosmos.NewUint(common.One)),
			Memo:        "OUT:" + GetRandomTxHash().String(),
			InHash:      GetRandomTxHash(),
		})
		must(t, k.SetTxOut(ctx, txOut))
	}

	// The cap, filled with BTC, then the one stranded ETH height behind it.
	for i := 0; i < maxStuckOutboundHeights; i++ {
		put(first+int64(i), common.BTCChain, common.BTCAsset, GetRandomBTCAddress())
	}
	ethHeight := int64(first + maxStuckOutboundHeights)
	put(ethHeight, common.ETHChain, common.ETHAsset, GetRandomETHAddress())

	// Record them all while they are still in window.
	inWindow := ethHeight + 10
	inWindow -= inWindow % 10
	emitQueueDetailTelemetry(ctx.WithBlockHeight(inWindow), mgr)

	// Now move past the window and add ordinary, healthy ETH traffic inside it.
	past := inWindow + window + 100
	past -= past % 10
	recentETH := past - 5
	put(recentETH, common.ETHChain, common.ETHAsset, GetRandomETHAddress())

	sink := installRecordingSink(t)
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past), mgr)

	// Both ETH items counted, and the age must be the STRANDED one's, not the
	// fresh one's.
	for key, want := range map[string]float32{
		"thornode.outbound.pending{chain=ETH,memo_type=outbound}": 2,
		"thornode.outbound.pending_oldest_blocks{chain=ETH}":      float32(past - ethHeight),
	} {
		if got := sink.gauges[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if got := sink.gauges["thornode.outbound.pending_oldest_blocks{chain=ETH}"]; got == float32(past-recentETH) {
		t.Errorf("pending_oldest_blocks{ETH} reports the recent item's age (%v); the stranded outbound is hidden", got)
	}
}

// A promotion that finds nothing does not advance its chain's observed oldest, so
// the next stale candidate qualifies too. Candidates go stale in bulk - a backlog
// clearing after an incident leaves a pile of remembered heights whose items are
// now signed - so without a budget one EndBlock reads the entire accumulated set.
func TestStuckOutboundPromotionBudgetBoundsStaleCandidates(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	resetStuckOutboundState(t)

	const first = 1_200_000
	const staleETH = maxStuckOutboundPromotions + 36
	ctx = ctx.WithBlockHeight(first)
	window := types.MaxTxOutSigningTransactionPeriod(k.GetConfigInt64(ctx, constants.SigningTransactionPeriod))

	put := func(height int64, chain common.Chain, asset common.Asset, to common.Address) {
		txOut := NewTxOut(height)
		txOut.TxArray = append(txOut.TxArray, TxOutItem{
			Chain:       chain,
			ToAddress:   to,
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(asset, cosmos.NewUint(common.One)),
			Memo:        "OUT:" + GetRandomTxHash().String(),
			InHash:      GetRandomTxHash(),
		})
		must(t, k.SetTxOut(ctx, txOut))
	}

	// BTC fills the cap and stays stuck, so every ETH height lands in rest.
	for i := 0; i < maxStuckOutboundHeights; i++ {
		put(first+int64(i), common.BTCChain, common.BTCAsset, GetRandomBTCAddress())
	}
	ethFirst := int64(first + maxStuckOutboundHeights)
	for i := 0; i < staleETH; i++ {
		put(ethFirst+int64(i), common.ETHChain, common.ETHAsset, GetRandomETHAddress())
	}

	// Record them all while in window.
	inWindow := ethFirst + staleETH + 10
	inWindow -= inWindow % 10
	emitQueueDetailTelemetry(ctx.WithBlockHeight(inWindow), mgr)

	// The backlog clears: every ETH item is signed. The remembered chain lists
	// still say ETH, so all of them remain promotion candidates.
	for i := 0; i < staleETH; i++ {
		h := ethFirst + int64(i)
		txOut, err := k.GetTxOut(ctx, h)
		must(t, err)
		for j := range txOut.TxArray {
			txOut.TxArray[j].OutHash = GetRandomTxHash()
		}
		must(t, k.SetTxOut(ctx, txOut))
	}

	// Healthy recent ETH traffic inside the window, so pending[ETH] is non-nil and
	// far newer than every stale candidate - each one satisfies h < p.oldest.
	past := inWindow + window + 100
	past -= past % 10
	put(past-5, common.ETHChain, common.ETHAsset, GetRandomETHAddress())

	sink := installRecordingSink(t)
	emitQueueDetailTelemetry(ctx.WithBlockHeight(past), mgr)

	// Exactly the budget's worth of stale candidates was read; a read one walks
	// clean and is evicted, an unread one is carried.
	remainingStale := 0
	for i := 0; i < staleETH; i++ {
		if _, ok := stuckOutboundHeights[ethFirst+int64(i)]; ok {
			remainingStale++
		}
	}
	if want := staleETH - maxStuckOutboundPromotions; remainingStale != want {
		t.Errorf("%d stale candidates left, want %d (budget %d of %d)",
			remainingStale, want, maxStuckOutboundPromotions, staleETH)
	}
	const deferredGauge = "thornode.outbound.scan_promotions_deferred"
	if got, want := sink.gauges[deferredGauge], float32(staleETH-maxStuckOutboundPromotions); got != want {
		t.Errorf("%s = %v, want %v", deferredGauge, got, want)
	}

	// The backlog still drains: the next scans keep spending the budget until the
	// carried set is clean and coverage is reported complete again.
	var at int64
	for i := 1; i <= staleETH/maxStuckOutboundPromotions+2; i++ {
		at = past + int64(i)*10
		sink = installRecordingSink(t)
		emitQueueDetailTelemetry(ctx.WithBlockHeight(at), mgr)
		if sink.gauges[deferredGauge] == 0 {
			break
		}
	}
	if got := sink.gauges[deferredGauge]; got != 0 {
		t.Errorf("%s = %v after draining, want 0", deferredGauge, got)
	}
	for i := 0; i < staleETH; i++ {
		if _, ok := stuckOutboundHeights[ethFirst+int64(i)]; ok {
			t.Errorf("stale candidate %d never evicted", ethFirst+int64(i))
			break
		}
	}
}
