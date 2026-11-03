package observer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
)

func startupRetryGossip(t *testing.T, ids ...peer.ID) (*AttestationGossip, *MockHost) {
	t.Helper()
	g, _, _, _, _, _ := setupTestGossip(t)
	g.logger = zerolog.Nop()
	h := NewMockHost(append([]peer.ID{"self"}, ids...))
	g.host = h
	g.activeVals = map[peer.ID]bool{"padding1": true, "padding2": true, "padding3": true}
	for _, id := range ids {
		g.activeVals[id] = true
	}
	g.config.AskPeers = len(ids)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
	}
	t.Cleanup(g.stopStartupStateSync)
	return g, h
}

func startupRetryFee(t *testing.T, g *AttestationGossip, height int64) (*common.NetworkFee, []byte) {
	t.Helper()
	fee, atts := stateSyncSignedFee(t, height, 1)
	stateSyncTrustAttestors(t, g, atts)
	data, err := (&common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}}).Marshal()
	require.NoError(t, err)
	return fee, data
}

func settleStartupRetry(t *testing.T, g *AttestationGossip, now time.Time) {
	t.Helper()
	require.NotNil(t, g.startupState.group)
	waitStateBounds(t, g.startupState.group.done)
	g.tickAttestationStateSync(context.Background(), true, false, now)
}

type gatedStartupStream struct {
	*stateBoundsStream
	gate    <-chan struct{}
	started chan struct{}
	once    sync.Once
}

func (s *gatedStartupStream) Read(p []byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.gate:
		return s.stateBoundsStream.Read(p)
	case <-s.stopped:
		return 0, io.ErrClosedPipe
	}
}

func TestStartupStateSyncParallelOutcomesKeepRunningImports(t *testing.T) {
	g, h := startupRetryGossip(t, "empty", "busy", "evidence", "slow")
	fee, data := startupRetryFee(t, g, 101)
	laterFee, laterData := startupRetryFee(t, g, 102)
	gate := make(chan struct{})
	slow := &gatedStartupStream{stateBoundsStream: stateSyncImportStream(t, "slow", laterData), gate: gate, started: make(chan struct{})}
	good := stateSyncImportStream(t, "evidence", data)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		switch id {
		case "busy":
			return nil, fmt.Errorf("busy")
		case "evidence":
			return good, nil
		case "slow":
			return slow, nil
		default:
			return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
		}
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	waitStateBounds(t, slow.started)
	require.Eventually(t, func() bool { return good.closes.Load() == 1 }, time.Second, time.Millisecond)
	for i := range 5 {
		g.tickAttestationStateSync(context.Background(), true, false, now.Add(time.Duration(i)*time.Second))
	}
	require.Equal(t, 4, g.startupState.requests)
	require.False(t, g.startupState.finished)
	require.Zero(t, slow.resets.Load(), "a completed non-empty peer must not cancel a running import")
	close(gate)
	settleStartupRetry(t, g, now.Add(10*time.Second))
	require.True(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{empty: 1, nonEmpty: 2, failed: 1}, g.startupState.result)
	require.NotNil(t, g.networkFees[*fee])
	require.NotNil(t, g.networkFees[*laterFee])
	g.tickAttestationStateSync(context.Background(), true, false, now.Add(time.Minute))
	require.Equal(t, 4, g.startupState.requests)
}

func TestStartupStateSyncFreshPeersRequestCapAndRemainingFanout(t *testing.T) {
	g, h := startupRetryGossip(t, "a", "b", "c", "d")
	g.config.AskPeers = 2
	calls := make(chan peer.ID, 10)
	original := h.streamFunc
	h.streamFunc = func(ctx context.Context, id peer.ID, p ...protocol.ID) (network.Stream, error) {
		calls <- id
		return original(ctx, id, p...)
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 5)
	for _, expected := range []int{2, 4, 5} {
		dispatch := g.startupState.next
		g.tickAttestationStateSync(context.Background(), true, false, dispatch)
		require.Equal(t, expected, g.startupState.requests)
		settleStartupRetry(t, g, dispatch)
		if expected < 5 {
			g.tickAttestationStateSync(context.Background(), true, false, dispatch.Add(time.Second))
			require.Nil(t, g.startupState.group, "reuse and fresh groups both wait for backoff")
		}
	}
	require.True(t, g.startupState.finished)
	require.Equal(t, "empty_snapshots_only", g.startupState.result.outcome())
	require.Len(t, calls, 5)
	unique := map[peer.ID]bool{}
	for range 4 {
		unique[<-calls] = true
	}
	require.Len(t, unique, 4, "all fresh peers precede any reuse")
	require.True(t, unique[<-calls])
	previous := g.startupState
	g.tickAttestationStateSync(context.Background(), true, false, now.Add(2*time.Hour))
	require.Same(t, previous, g.startupState, "ticks after exhaustion never reset budgets")
	require.Equal(t, 5, g.startupState.requests)
}

func TestStartupStateSyncCountsEveryLaunchedFailure(t *testing.T) {
	g, h := startupRetryGossip(t, "dial", "busy", "reset", "timeout", "empty")
	g.config.AskPeers = 2
	g.config.StateSyncTimeout = 30 * time.Millisecond
	calls := make(chan peer.ID, 10)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		calls <- id
		switch id {
		case "dial", "busy":
			return nil, fmt.Errorf("%s", id)
		case "timeout":
			s := newStateBoundsStream(id)
			s.block = true
			return s, nil
		case "reset":
			return newStateBoundsStream(id), nil
		default:
			return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
		}
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 5)
	for !g.startupState.finished {
		dispatch := g.startupState.next
		g.tickAttestationStateSync(context.Background(), true, false, dispatch)
		settleStartupRetry(t, g, dispatch)
	}
	require.Len(t, calls, 5)
	require.Equal(t, attestationStateGroupResult{empty: 1, failed: 4}, g.startupState.result)
	require.Equal(t, "empty_snapshots_with_failures", g.startupState.result.outcome())
}

func TestStartupStateSyncEmptyOrFailedPeerThenLaterEvidence(t *testing.T) {
	for _, first := range []string{"empty", "malformed reply", "busy"} {
		t.Run(first, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			fee, data := startupRetryFee(t, g, 103)
			var calls atomic.Int32
			h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
				if calls.Add(1) > 1 {
					return stateSyncImportStream(t, id, data), nil
				}
				switch first {
				case "malformed reply":
					return newStateBoundsStream(id, []byte{0xff}), nil
				case "busy":
					return nil, fmt.Errorf("busy")
				default:
					return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
				}
			}
			now := time.Now()
			g.tickAttestationStateSync(context.Background(), true, true, now)
			settleStartupRetry(t, g, now)
			require.False(t, g.startupState.finished)
			g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
			settleStartupRetry(t, g, g.startupState.next)
			require.True(t, g.startupState.finished)
			require.EqualValues(t, 2, calls.Load())
			require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
			require.Equal(t, 1, g.startupState.result.nonEmpty)
		})
	}
}

func TestStartupStateSyncPartialImportPreservedAndDeduplicated(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	fee, atts := stateSyncSignedFee(t, 104, 2)
	stateSyncTrustAttestors(t, g, atts)
	payload := func(attestations []*common.Attestation) []byte {
		data, err := (&common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: attestations}}}).Marshal()
		require.NoError(t, err)
		return data
	}
	firstData, fullData := payload(atts[:1]), payload(atts)
	partial := stateImportAfterRetention(g, fee, newStateBoundsStream("peer",
		append(prefixBatchBegin[:1:1], stateBoundsCount(2)...),
		stateBoundsHeader(0, uint32(len(firstData))), append(prefixBatchData[:1:1], firstData...),
		stateBoundsHeader(3, 0)))
	var calls atomic.Int32
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		if calls.Add(1) == 1 {
			return partial, nil
		}
		return stateSyncImportStream(t, id, fullData), nil
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	settleStartupRetry(t, g, now)
	require.Equal(t, "partial_transfers_only", g.startupState.result.outcome())
	require.False(t, g.startupState.finished)
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
	g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
	settleStartupRetry(t, g, g.startupState.next)
	require.Equal(t, 2, g.networkFees[*fee].AttestationCount())
	require.Equal(t, attestationStateGroupResult{nonEmpty: 1, failed: 1, partial: 1}, g.startupState.result)
	require.Positive(t, partial.resets.Load())
}

func TestStartupStateSyncWindowCapsOriginalAttempt(t *testing.T) {
	g, h := startupRetryGossip(t, "a", "b")
	now := time.Now()
	g.config.StateSyncTimeout = time.Hour
	g.startupState = newAttestationStateCollection(context.Background(), now, 50*time.Millisecond, 50)
	deadlines := make(chan time.Time, 2)
	h.streamFunc = func(ctx context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, fmt.Errorf("missing deadline")
		}
		deadlines <- deadline
		stream := newStateBoundsStream(id)
		stream.block = true
		return stream, nil
	}
	g.tickAttestationStateSync(context.Background(), true, false, now)
	settleStartupRetry(t, g, now.Add(60*time.Millisecond))
	require.True(t, g.startupState.finished)
	require.Equal(t, 2, g.startupState.requests, "elapsed-time budget expires independently of request cap")
	for range 2 {
		require.Equal(t, g.startupState.deadline, <-deadlines)
	}
	require.Less(t, time.Since(now), time.Second)
	// A shorter original attempt still wins over the collection window.
	g2, h2 := startupRetryGossip(t, "peer")
	g2.config.StateSyncTimeout = 20 * time.Millisecond
	var attempt time.Time
	h2.streamFunc = func(ctx context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		attempt, _ = ctx.Deadline()
		stream := newStateBoundsStream(id)
		stream.block = true
		return stream, nil
	}
	g2.tickAttestationStateSync(context.Background(), true, true, time.Now())
	settleStartupRetry(t, g2, time.Now())
	require.True(t, attempt.Before(g2.startupState.deadline))
	require.False(t, g2.startupState.finished)
}

func TestStartupStateSyncCancellationLeaveAndReactivation(t *testing.T) {
	for _, mode := range []string{"shutdown", "parent", "leave active"} {
		t.Run(mode, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			stream := newStateBoundsStream("peer")
			stream.block = true
			var calls atomic.Int32
			h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
				if calls.Add(1) == 1 {
					return stream, nil
				}
				return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Now()
			g.tickAttestationStateSync(ctx, true, true, now)
			waitStateBounds(t, stream.blocked)
			old := g.startupState
			switch mode {
			case "shutdown":
				g.stopStartupStateSync()
			case "parent":
				cancel()
			case "leave active":
				g.tickAttestationStateSync(ctx, false, false, now)
			}
			if old.group != nil {
				waitStateBounds(t, old.group.done)
			}
			require.Positive(t, stream.resets.Load())
			if mode == "leave active" {
				g.tickAttestationStateSync(context.Background(), true, true, now)
				require.NotSame(t, old, g.startupState)
				settleStartupRetry(t, g, now)
				require.Equal(t, 1, g.startupState.requests)
				require.EqualValues(t, 2, calls.Load())
			} else {
				g.tickAttestationStateSync(ctx, true, false, now)
				require.True(t, old.finished)
				require.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestStartupStateSyncBackoffCancellationAndNoEligiblePeers(t *testing.T) {
	g, _ := startupRetryGossip(t, "peer")
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	settleStartupRetry(t, g, now)
	g.tickAttestationStateSync(context.Background(), false, false, now.Add(time.Second))
	require.True(t, g.startupState.finished)
	require.Equal(t, 1, g.startupState.requests)
	empty, _ := startupRetryGossip(t)
	var logs stateSyncLogBuffer
	empty.logger = zerolog.New(&logs)
	empty.startupState = newAttestationStateCollection(context.Background(), now, 30*time.Millisecond, 50)
	empty.tickAttestationStateSync(context.Background(), true, false, now)
	require.Nil(t, empty.startupState.group)
	require.Zero(t, empty.startupState.requests)
	waitStateBounds(t, empty.startupState.ctx.Done())
	// A deck tick can carry a timestamp captured before the deadline fired.
	empty.tickAttestationStateSync(context.Background(), true, false, now)
	require.True(t, empty.startupState.finished)
	require.Equal(t, "no_completed_snapshot", empty.startupState.result.outcome())
	require.Contains(t, logs.String(), `"reason":"window_exhausted"`)
}

func TestStartupStateSyncJitterBoundsAndRecoveryAfterExhaustion(t *testing.T) {
	for round := 1; round <= 100; round++ {
		low, high := attestationStateRetryDelay(round, 0), attestationStateRetryDelay(round, 1)
		require.GreaterOrEqual(t, low, 2500*time.Millisecond)
		require.LessOrEqual(t, high, time.Minute)
		require.Equal(t, 2*low, high)
	}
	require.Equal(t, 2500*time.Millisecond, attestationStateRetryDelay(1, 0))
	require.Equal(t, 30*time.Second, attestationStateRetryDelay(100, 0))
	g, h := startupRetryGossip(t, "peer")
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 1)
	g.tickAttestationStateSync(context.Background(), true, false, now)
	settleStartupRetry(t, g, now)
	require.True(t, g.startupState.finished)
	_, data := startupRetryFee(t, g, 105)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		return stateSyncImportStream(t, id, data), nil
	}
	g.tickAttestationStateSync(context.Background(), true, false, now.Add(time.Hour))
	require.Nil(t, g.startupState.group, "later peer readiness/signing resumption does not re-arm collection")
	// A supported Bifrost restart constructs a fresh gossip service, then the
	// Unknown-to-Active status transition arms a new independent collection.
	restarted, restartedHost := startupRetryGossip(t, "peer")
	restartedHost.streamFunc = h.streamFunc
	restarted.activeVals = g.getActiveValidators()
	restarted.tickAttestationStateSync(context.Background(), true, true, time.Now())
	settleStartupRetry(t, restarted, time.Now())
	require.True(t, restarted.startupState.finished)
	require.Equal(t, 1, restarted.startupState.result.nonEmpty)
}

func TestStartupStateSyncEmptyNumberedFrames(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		return stateSyncImportStream(t, id, nil, nil), nil
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 1)
	g.tickAttestationStateSync(context.Background(), true, false, now)
	settleStartupRetry(t, g, now)
	require.Equal(t, attestationStateGroupResult{empty: 1}, g.startupState.result)
}

func TestStartupStateSyncReactivationWaitsForPreviousGroup(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h.streamFunc = func(ctx context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			<-release // Model cancellation cleanup still owned by the prior dial.
			return nil, ctx.Err()
		}
		return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	waitStateBounds(t, entered)
	old := g.startupState
	g.tickAttestationStateSync(context.Background(), false, false, now)
	g.tickAttestationStateSync(context.Background(), true, true, now)
	require.Same(t, old, g.startupState)
	require.EqualValues(t, 1, calls.Load())
	close(release)
	waitStateBounds(t, old.group.done)
	g.tickAttestationStateSync(context.Background(), true, false, now)
	require.NotSame(t, old, g.startupState)
	settleStartupRetry(t, g, now)
	require.EqualValues(t, 2, calls.Load())
}

func TestStartupStateSyncShutdownDuringBackoff(t *testing.T) {
	g, _ := startupRetryGossip(t, "peer")
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	settleStartupRetry(t, g, now)
	require.Nil(t, g.startupState.group)
	require.True(t, g.startupState.next.After(now))
	g.stopStartupStateSync()
	require.True(t, g.startupState.finished)
	require.ErrorIs(t, g.startupState.ctx.Err(), context.Canceled)
	g.tickAttestationStateSync(context.Background(), true, true, now.Add(time.Hour))
	require.Equal(t, 1, g.startupState.requests)
}

func TestStartupStateSyncExhaustionReportsTransferOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		modes         []string
		result        attestationStateGroupResult
	}{
		{"empty", "empty_snapshots_only", []string{"empty"}, attestationStateGroupResult{empty: 1}},
		{"mixed", "empty_snapshots_with_failures", []string{"empty", "failed"}, attestationStateGroupResult{empty: 1, failed: 1}},
		{"failed", "no_completed_snapshot", []string{"failed"}, attestationStateGroupResult{failed: 1}},
		{"partial", "partial_transfers_only", []string{"partial"}, attestationStateGroupResult{failed: 1, partial: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := []peer.ID{"one"}
			if len(tc.modes) == 2 {
				ids = append(ids, "two")
			}
			g, h := startupRetryGossip(t, ids...)
			fee, data := startupRetryFee(t, g, 120)
			var logs stateSyncLogBuffer
			g.logger = zerolog.New(&logs).Level(zerolog.InfoLevel)
			h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
				index := 0
				if id == "two" {
					index = 1
				}
				switch tc.modes[index] {
				case "failed":
					return nil, fmt.Errorf("busy")
				case "partial":
					return stateImportAfterRetention(g, fee, newStateBoundsStream(id,
						append(prefixBatchBegin[:1:1], stateBoundsCount(2)...),
						stateBoundsHeader(0, uint32(len(data))), append(prefixBatchData[:1:1], data...),
						stateBoundsHeader(3, 0))), nil
				default:
					return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
				}
			}
			now := time.Now()
			g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, len(ids))
			g.tickAttestationStateSync(context.Background(), true, false, now)
			settleStartupRetry(t, g, now)
			require.True(t, g.startupState.finished)
			require.Equal(t, tc.result, g.startupState.result)
			require.Contains(t, logs.String(), fmt.Sprintf("%q:%q", "outcome", tc.outcome))
			require.Contains(t, logs.String(), fmt.Sprintf("%q:%q", "reason", "requests_exhausted"))
		})
	}
}

type joiningStartupStream struct {
	*stateBoundsStream
	cancelled chan struct{}
	release   <-chan struct{}
	once      sync.Once
}

func (s *joiningStartupStream) Read(p []byte) (int, error) {
	n, err := s.stateBoundsStream.Read(p)
	s.once.Do(func() { close(s.cancelled) })
	<-s.release
	return n, err
}

func TestStartupStateSyncObserverStopJoinsBeforeStorageClose(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
	require.NoError(t, err)
	o, err := NewObserver(pkm, nil, &MockThorchainBridge{}, getSharedTestMetrics(), t.TempDir(), metrics.NewTssKeysignMetricMgr(), g, "")
	require.NoError(t, err)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	stream := &joiningStartupStream{stateBoundsStream: newStateBoundsStream("peer"), cancelled: make(chan struct{}), release: release}
	stream.block = true
	h.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) { return stream, nil }
	g.tickAttestationStateSync(context.Background(), true, true, time.Now())
	group := g.startupState.group
	waitStateBounds(t, stream.blocked)
	stopped := make(chan error, 1)
	go func() { stopped <- o.Stop() }()
	waitStateBounds(t, stream.cancelled)
	select {
	case stopErr := <-stopped:
		t.Fatalf("observer stopped before joining its import: %v", stopErr)
	default:
	}
	_, err = o.storage.db.Has([]byte("still-open"), nil)
	require.NoError(t, err, "storage must remain usable until the owned import returns")
	unblock()
	select {
	case stopErr := <-stopped:
		require.NoError(t, stopErr)
	case <-time.After(time.Second):
		t.Fatal("observer failed to join the cancelled import")
	}
	waitStateBounds(t, group.done)
	require.ErrorIs(t, g.startupState.ctx.Err(), context.Canceled)
	require.True(t, g.startupState.finished)
	require.True(t, g.startupStateStopped)
	_, err = o.storage.db.Has([]byte("now-closed"), nil)
	require.Error(t, err)
}

func TestStartupStateSyncPeerSelectionFiltersSelfInactiveAndDuplicates(t *testing.T) {
	g, h := startupRetryGossip(t, "a", "b")
	h.peers = []peer.ID{"self", "inactive", "a", "a", "b"}
	g.activeVals["self"] = true
	require.ElementsMatch(t, []peer.ID{"a", "b"}, g.attestationStatePeers())
	var logs stateSyncLogBuffer
	g.logger = zerolog.New(&logs)
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	settleStartupRetry(t, g, now)
	require.Equal(t, 2, g.startupState.requests)
	require.Equal(t, 2, g.startupState.result.empty)
	require.Equal(t, 1, strings.Count(logs.String(), "startup attestation state group completed"))
}

func TestStartupStateSyncNewPeersKeepPriorityAfterReuse(t *testing.T) {
	g, h := startupRetryGossip(t, "a", "b", "c", "d", "new", "later")
	g.config.AskPeers = 2
	h.peers = []peer.ID{"self", "a", "b", "c", "d"}
	calls := make(chan peer.ID, 20)
	original := h.streamFunc
	h.streamFunc = func(ctx context.Context, id peer.ID, protocols ...protocol.ID) (network.Stream, error) {
		calls <- id
		return original(ctx, id, protocols...)
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 20)
	round := func() []peer.ID {
		t.Helper()
		dispatch := g.startupState.next
		g.tickAttestationStateSync(context.Background(), true, false, dispatch)
		settleStartupRetry(t, g, dispatch)
		var launched []peer.ID
		for len(calls) > 0 {
			launched = append(launched, <-calls)
		}
		return launched
	}
	first := append(round(), round()...)
	require.ElementsMatch(t, []peer.ID{"a", "b", "c", "d"}, first)
	require.Len(t, round(), 2, "the original peer pool has entered reuse")
	h.peers = append(h.peers, "new")
	require.Equal(t, []peer.ID{"new"}, round(), "a new peer gets priority over every reused peer")
	require.Len(t, g.startupState.tried, 5, "lifetime history survives reuse")

	// Losing all eligible connections must not forget earlier attempts.
	h.peers = []peer.ID{"self"}
	g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
	require.Nil(t, g.startupState.group)
	require.Len(t, g.startupState.tried, 5)
	h.peers = []peer.ID{"self", first[0], first[1], "later"}
	require.Equal(t, []peer.ID{"later"}, round(), "returning peers do not compete with a never-attempted peer")
	require.Equal(t, 8, g.startupState.requests)
	require.Len(t, g.startupState.tried, 6)
}

func TestStartupStateSyncActivationWindowIncludesCleanupJoin(t *testing.T) {
	for _, delay := range []time.Duration{time.Minute, startupStateSyncWindow} {
		t.Run(delay.String(), func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			h.streamFunc = func(ctx context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					<-release
					return nil, ctx.Err()
				}
				return newStateBoundsStream(id, append(prefixBatchBegin[:1:1], stateBoundsCount(0)...)), nil
			}
			now := time.Now()
			g.tickAttestationStateSync(context.Background(), true, true, now)
			waitStateBounds(t, entered)
			old := g.startupState
			g.tickAttestationStateSync(context.Background(), false, false, now.Add(time.Minute))
			activation := now.Add(2 * time.Minute)
			g.tickAttestationStateSync(context.Background(), true, true, activation)
			require.Same(t, old, g.startupState)
			require.EqualValues(t, 1, calls.Load())
			unblock()
			waitStateBounds(t, old.group.done)
			joined := activation.Add(delay)
			g.tickAttestationStateSync(context.Background(), true, false, joined)
			require.NotSame(t, old, g.startupState)
			require.Equal(t, activation.Add(startupStateSyncWindow), g.startupState.deadline)
			if delay == startupStateSyncWindow {
				require.True(t, g.startupState.finished, "cleanup consumed the new activation's entire window")
				require.Nil(t, g.startupState.group)
				require.Zero(t, g.startupState.requests)
				require.EqualValues(t, 1, calls.Load())
			} else {
				settleStartupRetry(t, g, joined)
				require.Equal(t, 1, g.startupState.requests)
				require.EqualValues(t, 2, calls.Load())
			}
		})
	}
}
