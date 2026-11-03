package observer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func errataTestObservation(id string, chain common.Chain, height int64) common.ObservedTx {
	tx := common.ObservedTx{
		Tx:          common.Tx{ID: common.TxID(id), Chain: chain},
		BlockHeight: height, FinaliseHeight: height,
	}
	if chain.UsesHeightScopedErrata() {
		tx.XmrMinedHeight = height
	}
	return tx
}

func setupErrataTestGossip(t *testing.T) (*Observer, *AttestationGossip, *MockGRPCClient) {
	t.Helper()
	o, storage := newErrataOrderingObserver(t)
	g, _, _, client, _, _ := setupTestGossip(t)
	g.logger = zerolog.Nop()
	g.batcher.logger = zerolog.Nop()
	g.SetObserverStorage(storage)
	o.attestationGossip = g
	return o, g, client
}

func waitForObserverTest(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not complete")
	}
}

func TestCleanupRejectsLocalPeerAndRestartState(t *testing.T) {
	o, g, _ := setupErrataTestGossip(t)
	old := errataTestObservation("orphan", common.XMRChain, 100)
	require.NoError(t, g.AttestObservedTx(context.Background(), &old, true, false))
	msg := *g.batcher.observedTxBatch[0]
	errata := btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: old.Tx.ID}}}
	require.NoError(t, o.filterErrataTx(errata))
	require.Empty(t, g.observedTxs)
	require.Empty(t, g.batcher.observedTxBatch)

	require.NoError(t, g.AttestObservedTx(context.Background(), &old, true, false))
	g.handleObservedTxAttestation(context.Background(), msg)
	require.Empty(t, g.observedTxs)
	require.Empty(t, g.batcher.observedTxBatch)

	// Fresh gossip over the same durable DB must refuse a peer state snapshot
	// even though no volatile cleanup history survived process restart.
	restarted, _, _, _, _, _ := setupTestGossip(t)
	restarted.SetObserverStorage(o.storage)
	require.NoError(t, restarted.processAttestationStateBatch(zerolog.Nop(), common.QuorumState{
		QuoTxs: []*common.QuorumTx{{ObsTx: old, Inbound: true, Attestations: []*common.Attestation{msg.Attestation}}},
	}))
	require.Empty(t, restarted.observedTxs)

	remine := errataTestObservation("orphan", common.XMRChain, 250)
	require.NoError(t, g.AttestObservedTx(context.Background(), &remine, true, false))
	// Retry is evidence retransmission, including scanner redelivery.
	require.NoError(t, g.AttestErrata(context.Background(), common.ErrataTx{Chain: common.XMRChain, Id: old.Tx.ID}))
	require.NoError(t, o.filterErrataTx(errata))
	require.Len(t, g.observedTxs, 1)
	require.Len(t, g.batcher.observedTxBatch, 1)
	require.Equal(t, int64(250), g.batcher.observedTxBatch[0].ObsTx.BlockHeight)

	require.NoError(t, o.handleErrataTxCommitted(common.ErrataTx{Chain: common.XMRChain, Id: old.Tx.ID}, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	unresolved, err := o.reconcileCommittedXMRErrata()
	require.NoError(t, err)
	require.Zero(t, unresolved)
	require.Empty(t, g.observedTxs)
	require.Empty(t, g.batcher.observedTxBatch)
	require.NoError(t, g.AttestObservedTx(context.Background(), &remine, true, false))
	require.Empty(t, g.observedTxs, "permanent reverted TxID blocks every height")
}

func TestAdmissionFailsClosedOnUnavailableStorage(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			if broken {
				require.NoError(t, o.storage.db.Put([]byte(errataTombstoneKey(common.XMRChain, "storage-failure")), []byte("corrupt"), nil))
			} else {
				g.SetObserverStorage(nil)
			}
			tx := errataTestObservation("storage-failure", common.XMRChain, 100)
			require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			require.Empty(t, g.observedTxs)
			require.Empty(t, g.batcher.observedTxBatch)
			tx.Tx.Chain = common.BTCChain
			require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			require.Len(t, g.observedTxs, 1, "legacy admission does not consult XMR storage")
		})
	}
}

func TestValidatedEnqueueRechecksCleanupAfterPausedValidation(t *testing.T) {
	o, g, _ := setupErrataTestGossip(t)
	tx := errataTestObservation("paused-validation", common.XMRChain, 100)
	validating, resume := make(chan struct{}), make(chan struct{})
	var pauseOnce sync.Once
	originalVerify := verifySignature
	defer func() { verifySignature = originalVerify }()
	verifySignature = func(payload, signature, pubKey []byte) error {
		pauseOnce.Do(func() {
			close(validating)
			<-resume
		})
		return originalVerify(payload, signature, pubKey)
	}
	admitted := make(chan struct{})
	var attestErr error
	go func() {
		attestErr = g.AttestObservedTx(context.Background(), &tx, true, false)
		close(admitted)
	}()
	waitForObserverTest(t, validating)
	g.batcher.mu.Lock()
	locked := true
	defer func() {
		if locked {
			g.batcher.mu.Unlock()
		}
	}()
	require.Empty(t, g.batcher.observedTxBatch, "enqueue must wait for signature validation")
	cleaned := make(chan struct{})
	var cleanupErr error
	go func() {
		cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}})
		close(cleaned)
	}()
	require.Eventually(t, func() bool {
		if g.xmrErrataBarrier.TryRLock() {
			g.xmrErrataBarrier.RUnlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	close(resume)
	// The validated handler releases its local reader before waiting to
	// enqueue. Cleanup writes first, then waits on the same queue mutex.
	require.Eventually(t, func() bool {
		tombstone, err := o.storage.getErrataTombstone(common.XMRChain, tx.Tx.ID.String())
		return err == nil && tombstone != nil
	}, time.Second, time.Millisecond)
	g.batcher.mu.Unlock()
	locked = false
	waitForObserverTest(t, cleaned)
	waitForObserverTest(t, admitted)
	require.NoError(t, cleanupErr)
	require.NoError(t, attestErr)
	require.Empty(t, g.observedTxs)
	require.Empty(t, g.batcher.observedTxBatch, "neither purge/enqueue order may resurrect the orphan")
}

func TestQueueAdmissionFailsClosedWithoutAffectingOtherChains(t *testing.T) {
	for _, mode := range []string{"missing", "uninitialized", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			b := newErrataTestBatcher(t, 0)
			tx := common.AttestTx{ObsTx: errataTestObservation("queue-storage", common.XMRChain, 100)}
			switch mode {
			case "missing":
				b.observerStorage = nil
			case "uninitialized":
				b.observerStorage = new(ObserverStorage)
			case "corrupt":
				require.NoError(t, b.observerStorage.db.Put([]byte(errataTombstoneKey(common.XMRChain, tx.ObsTx.Tx.ID.String())), []byte("corrupt"), nil))
			}
			b.AddObservedTx(tx)
			require.Empty(t, b.observedTxBatch)
			tx.ObsTx.Tx.Chain = common.BTCChain
			b.AddObservedTx(tx)
			require.Len(t, b.observedTxBatch, 1)
		})
	}
}

func TestInvalidLocalAttestationNeverEnqueues(t *testing.T) {
	for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
		t.Run(chain.String(), func(t *testing.T) {
			_, g, _ := setupErrataTestGossip(t)
			tx := errataTestObservation("invalid-signature", chain, 100)
			g.handleObservedTxAttestationInternal(context.Background(), common.AttestTx{
				ObsTx: tx, Inbound: true,
				Attestation: &common.Attestation{PubKey: g.pubKey, Signature: []byte("invalid")},
			}, true)
			require.Empty(t, g.batcher.observedTxBatch)
			require.Zero(t, g.observedTxs[observedTxKey(tx, true, false)].AttestationCount())
			require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			require.Len(t, g.batcher.observedTxBatch, 1, "valid local evidence must still propagate")

			// AddAttestation accepts a known signature without verifying again.
			// Its presence in a bucket must not authorize a mutated envelope.
			mutated := *g.batcher.observedTxBatch[0]
			mutated.ObsTx.Tx.Memo = "different signed payload"
			g.handleObservedTxAttestationInternal(context.Background(), mutated, true)
			require.Len(t, g.batcher.observedTxBatch, 1, "a duplicate signature must not authorize different payload bytes")
		})
	}
}

func TestCleanupRunsBetweenLateRetries(t *testing.T) {
	o, g, client := setupErrataTestGossip(t)
	g.config.MinTimeBetweenAttestations = 0
	g.config.PeerTimeout = time.Second
	for _, id := range []string{"late-a", "late-b"} {
		tx := errataTestObservation(id, common.XMRChain, 100)
		require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client.sendQuorumTxFunc = func(ctx context.Context, tx *common.QuorumTx, _ ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		if tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
			if calls.Add(1) == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return &ebifrost.SendQuorumTxResult{}, nil
	}
	reconciled := make(chan struct{})
	go func() { g.reconcileObservedTxs(context.Background()); close(reconciled) }()
	waitForObserverTest(t, started)

	// This admission must acquire the global map mutex while XMR is stalled.
	btcDone := make(chan struct{})
	go func() {
		tx := errataTestObservation("unrelated", common.BTCChain, 100)
		_ = g.AttestObservedTx(context.Background(), &tx, true, false)
		close(btcDone)
	}()
	waitForObserverTest(t, btcDone)
	cleaned := make(chan struct{})
	var cleanupErr error
	go func() {
		cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{
			{Chain: common.XMRChain, TxID: "late-a"}, {Chain: common.XMRChain, TxID: "late-b"},
		}})
		close(cleaned)
	}()
	// A waiting writer makes TryRLock fail even while the first reader owns
	// its lease. This synchronizes the interleaving without sleeps/map ordering.
	require.Eventually(t, func() bool {
		if g.xmrErrataBarrier.TryRLock() {
			g.xmrErrataBarrier.RUnlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	close(release)
	waitForObserverTest(t, cleaned)
	waitForObserverTest(t, reconciled)
	require.NoError(t, cleanupErr)
	require.Equal(t, int32(1), calls.Load(), "second snapshotted key must be rechecked after cleanup")
	require.Len(t, g.observedTxs, 1)
}

func TestLateRPCFailureAndCancellationRetainEvidence(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelParent), func(t *testing.T) {
			_, g, client := setupErrataTestGossip(t)
			g.config.MinTimeBetweenAttestations = 0
			g.config.PeerTimeout = 80 * time.Millisecond
			tx := errataTestObservation("late-timeout", common.XMRChain, 100)
			require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			k := observedTxKey(tx, true, false)
			started := make(chan struct{})
			client.sendQuorumTxFunc = func(ctx context.Context, _ *common.QuorumTx, _ ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { g.reconcileObservedTx(ctx, k); close(done) }()
			waitForObserverTest(t, started)
			if cancelParent {
				cancel()
			}
			waitForObserverTest(t, done)
			require.True(t, g.xmrErrataBarrier.TryLock())
			g.xmrErrataBarrier.Unlock()
			state := g.observedTxs[k]
			require.Equal(t, 1, state.UnsentCount())
			require.True(t, state.lastAttestationsSent.IsZero())
		})
	}
	require.Equal(t, constants.ThorchainBlockTime, xmrAttestationTimeout(time.Hour))
	require.Equal(t, constants.ThorchainBlockTime, xmrAttestationTimeout(0))
}

func newErrataTestBatcher(t *testing.T, peerCount int) *AttestationBatcher {
	t.Helper()
	peers := []peer.ID{"self"}
	active := make(map[peer.ID]bool)
	for i := 0; i < peerCount; i++ {
		p := peer.ID(fmt.Sprintf("peer-%d", i))
		peers = append(peers, p)
		active[p] = true
	}
	b := NewAttestationBatcher(NewMockHost(peers), zerolog.Nop(), nil, time.Second, 100, 80*time.Millisecond, 1)
	b.setActiveValGetter(func() map[peer.ID]bool { return active })
	storage, tempDir := setupTestDB(t)
	t.Cleanup(func() { cleanupTestDB(t, storage, tempDir) })
	b.observerStorage = storage
	return b
}

func TestBatchFanoutSharesDeadlineForEveryPeer(t *testing.T) {
	b := newErrataTestBatcher(t, 20)
	deadlines := make(chan time.Time, 20)
	var running atomic.Int32
	var workers sync.WaitGroup
	workers.Add(20)
	b.broadcastToPeerFn = func(ctx context.Context, _ peer.ID, _ []byte) {
		defer workers.Done()
		running.Add(1)
		defer running.Add(-1)
		deadline, _ := ctx.Deadline()
		deadlines <- deadline
		<-ctx.Done()
	}
	b.AddObservedTx(common.AttestTx{ObsTx: errataTestObservation("batch-timeout", common.XMRChain, 100)})
	start := time.Now()
	b.sendBatches(context.Background(), true)
	require.Less(t, time.Since(start), time.Second)
	first := <-deadlines
	for i := 1; i < 20; i++ {
		require.Equal(t, first, <-deadlines)
	}
	workers.Wait()
	require.Zero(t, running.Load())
}

func TestBatchCleanupBeforeDequeueAndNonXMRProgress(t *testing.T) {
	b := newErrataTestBatcher(t, 1)
	old := common.AttestTx{ObsTx: errataTestObservation("batch-orphan", common.XMRChain, 100)}
	btc := common.AttestTx{ObsTx: errataTestObservation("btc", common.BTCChain, 100)}
	b.AddObservedTx(old)
	b.AddObservedTx(btc)
	sent := make(chan common.AttestationBatch, 2)
	b.broadcastToPeerFn = func(_ context.Context, _ peer.ID, payload []byte) {
		var batch common.AttestationBatch
		_ = batch.Unmarshal(payload)
		sent <- batch
	}
	b.removeObservedTxsForErrata(common.ErrataTx{Chain: common.XMRChain, Id: old.ObsTx.Tx.ID}, 100)
	b.sendBatches(context.Background(), true)
	batch := <-sent
	require.Len(t, batch.AttestTxs, 1)
	require.Equal(t, common.BTCChain, batch.AttestTxs[0].ObsTx.Tx.Chain)

	b.AddObservedTx(btc)
	b.sendBatches(context.Background(), true)
	<-sent
}

func TestCancelledFanoutReleasesPeerReservations(t *testing.T) {
	b := newErrataTestBatcher(t, 4)
	b.peerTimeout = time.Second
	host, ok := b.host.(*MockHost)
	require.True(t, ok)
	var running atomic.Int32
	started := make(chan struct{}, 4)
	var workers sync.WaitGroup
	workers.Add(4)
	b.broadcastToPeerFn = func(ctx context.Context, p peer.ID, payload []byte) {
		defer workers.Done()
		b.broadcastToPeer(ctx, p, payload)
	}
	host.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
		running.Add(1)
		defer running.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.AddObservedTx(common.AttestTx{ObsTx: errataTestObservation("cancelled", common.XMRChain, 100)})
	done := make(chan struct{})
	go func() { b.sendBatches(ctx, true); close(done) }()
	for i := 0; i < 4; i++ {
		<-started
	}
	cancel()
	waitForObserverTest(t, done)
	workers.Wait()
	require.Zero(t, running.Load())
	for _, sem := range b.peerMgr.semaphores {
		require.Zero(t, sem.refCount)
		require.Equal(t, cap(sem.tokens), len(sem.tokens))
	}

	// Cancellation while waiting for capacity must not consume a token or
	// retain a reservation after the peer attempt ends.
	held, err := b.peerMgr.acquire("busy")
	require.NoError(t, err)
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	_, err = b.peerMgr.acquireContext(cancelled, "busy")
	require.ErrorIs(t, err, context.Canceled)
	b.peerMgr.release(held)
	require.Zero(t, held.refCount)

	// The default 50ms busy timeout can expire before this test observes the
	// waiter or cancels it on a loaded runner; keep the waiter parked so only
	// cancellation can end it.
	b.peerMgr.acquireTimeout = time.Minute
	held, err = b.peerMgr.acquire("waiting")
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	waitDone := make(chan struct{})
	var waitErr error
	go func() {
		_, waitErr = b.peerMgr.acquireContext(waitCtx, "waiting")
		close(waitDone)
	}()
	require.Eventually(t, func() bool {
		held.mu.Lock()
		defer held.mu.Unlock()
		return held.refCount == 2
	}, time.Second, time.Millisecond)
	cancelWait()
	waitForObserverTest(t, waitDone)
	require.ErrorIs(t, waitErr, context.Canceled)
	b.peerMgr.release(held)
	require.Zero(t, held.refCount)
}

type stalledAttestationStateStream struct {
	network.Stream
	started chan struct{}
	reset   chan struct{}
	once    sync.Once
}

func (s *stalledAttestationStateStream) Conn() network.Conn          { return &MockConn{peer: "state-peer"} }
func (s *stalledAttestationStateStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *stalledAttestationStateStream) Read([]byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	<-s.reset
	return 0, io.EOF
}

func (s *stalledAttestationStateStream) Reset() error {
	select {
	case <-s.reset:
	default:
		close(s.reset)
	}
	return nil
}
func (s *stalledAttestationStateStream) SetReadDeadline(time.Time) error  { return nil }
func (s *stalledAttestationStateStream) SetWriteDeadline(time.Time) error { return nil }

func TestStateExportBeginACKDoesNotHoldCleanup(t *testing.T) {
	o, g, _ := setupErrataTestGossip(t)
	g.config.PeerTimeout = 80 * time.Millisecond
	tx := errataTestObservation("state-export", common.XMRChain, 100)
	require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
	stream := &stalledAttestationStateStream{started: make(chan struct{}), reset: make(chan struct{})}
	defer func() { require.NoError(t, stream.Reset()) }()
	done := make(chan struct{})
	go func() { g.sendAttestationState(stream); close(done) }()
	waitForObserverTest(t, stream.started)
	cleaned := make(chan struct{})
	var cleanupErr error
	go func() {
		cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}})
		close(cleaned)
	}()
	waitForObserverTest(t, cleaned)
	require.NoError(t, cleanupErr)
	require.Empty(t, g.observedTxs)
	select {
	case <-done:
		t.Fatal("cleanup should not require resetting the state transfer")
	default:
	}
	require.NoError(t, stream.Reset())
	waitForObserverTest(t, done)
}

func TestCleanupWriteFailurePreservesRetryableObservation(t *testing.T) {
	o, g, _ := setupErrataTestGossip(t)
	tx := errataTestObservation("write-failure", common.XMRChain, 100)
	require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
	o.storage.beforeErrataWrite = func() error { return errors.New("disk failure") }
	block := btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}}
	require.ErrorContains(t, o.filterErrataTx(block), "disk failure")
	require.Len(t, g.observedTxs, 1)
	require.Len(t, g.batcher.observedTxBatch, 1)
	require.True(t, g.xmrErrataBarrier.TryLock())
	g.xmrErrataBarrier.Unlock()
	o.storage.beforeErrataWrite = nil
	require.NoError(t, o.filterErrataTx(block))
	require.Empty(t, g.observedTxs)
	require.Empty(t, g.batcher.observedTxBatch)
}

func TestEmptyStateExportDoesNotTakeXMRBarrier(t *testing.T) {
	_, g, _ := setupErrataTestGossip(t)
	// Empty transfer gets one framed "done" reply.
	stream := NewMockStream(bytes.NewReader([]byte{4, 0, 0, 0, 'd', 'o', 'n', 'e'}), &bytes.Buffer{}, "peer")
	g.xmrErrataBarrier.Lock()
	done := make(chan struct{})
	go func() { g.sendAttestationState(stream); close(done) }()
	waitForObserverTest(t, done)
	g.xmrErrataBarrier.Unlock()
}

func TestCleanupPersistsWhilePeerBatchStalls(t *testing.T) {
	o, g, _ := setupErrataTestGossip(t)
	g.batcher.setActiveValGetter(g.getActiveValidators)
	g.batcher.peerTimeout = time.Hour // the parent cancellation ends this attempt
	tx := errataTestObservation("in-flight", common.XMRChain, 100)
	require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
	started := make(chan struct{})
	var once sync.Once
	var running, activeAtCleanup atomic.Int32
	var workers sync.WaitGroup
	workers.Add(len(g.getActiveValidators()) - 1)
	g.batcher.broadcastToPeerFn = func(ctx context.Context, _ peer.ID, _ []byte) {
		defer workers.Done()
		running.Add(1)
		defer running.Add(-1)
		once.Do(func() { close(started) })
		<-ctx.Done()
	}
	o.storage.beforeErrataWrite = func() error {
		activeAtCleanup.Store(running.Load())
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan struct{})
	go func() { g.batcher.sendBatches(ctx, true); close(sent) }()
	waitForObserverTest(t, started)
	cleaned := make(chan struct{})
	var cleanupErr error
	go func() {
		cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}})
		close(cleaned)
	}()
	waitForObserverTest(t, sent)
	waitForObserverTest(t, cleaned)
	require.NoError(t, cleanupErr)
	require.Positive(t, activeAtCleanup.Load(), "cleanup must persist while peer delivery is stalled")
	require.Empty(t, g.observedTxs)
	require.Empty(t, g.batcher.observedTxBatch)
	cancel()
	workers.Wait()
}

// A pending XMR item outside the selected prefix must not stall a legacy batch.
// The count limit is snapshotted even if a Mimir update lands during preparation.
func TestBatchLimitsLeaveUnselectedXMRQueued(t *testing.T) {
	for _, byteLimited := range []bool{false, true} {
		t.Run(fmt.Sprint(byteLimited), func(t *testing.T) {
			b := newErrataTestBatcher(t, 1)
			btc := common.AttestTx{ObsTx: errataTestObservation("first", common.BTCChain, 100)}
			if byteLimited {
				btc.ObsTx.Tx.Memo = string(bytes.Repeat([]byte{'x'}, maxAttestationBatchBytes))
			} else {
				b.updateMaxBatchSize(1)
				b.batchPool = sync.Pool{New: func() interface{} {
					b.updateMaxBatchSize(2)
					return new(common.AttestationBatch)
				}}
			}
			b.AddObservedTx(btc)
			b.AddObservedTx(common.AttestTx{ObsTx: errataTestObservation("later", common.XMRChain, 100)})
			sent := make(chan struct{})
			b.broadcastToPeerFn = func(_ context.Context, _ peer.ID, payload []byte) {
				var batch common.AttestationBatch
				if err := batch.Unmarshal(payload); err == nil && len(batch.AttestTxs) == 1 &&
					batch.AttestTxs[0].ObsTx.Tx.Chain.Equals(common.BTCChain) {
					close(sent)
				}
			}
			done := make(chan struct{})
			go func() { b.sendBatches(context.Background(), true); close(done) }()
			waitForObserverTest(t, done)
			waitForObserverTest(t, sent)
			require.Len(t, b.observedTxBatch, 1)
			require.Equal(t, common.XMRChain, b.observedTxBatch[0].ObsTx.Tx.Chain)
		})
	}
}

func errataTestDeckItem(t *testing.T, chain common.Chain, id int) *btypes.TxInItem {
	t.Helper()
	pubKey := stypes.GetRandomPubKey()
	if chain.Equals(common.XMRChain) {
		pubKey = stypes.GetRandomEd25519PubKey()
	}
	address, err := pubKey.GetAddress(chain)
	require.NoError(t, err)
	limit := cosmos.NewUint(123)
	return &btypes.TxInItem{
		Tx: fmt.Sprintf("%064x", id), BlockHeight: 100,
		Sender: address.String(), To: address.String(), ObservedVaultPubKey: pubKey,
		Coins:                 common.Coins{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(100))},
		Gas:                   common.Gas{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(1))},
		AggregatorTargetLimit: &limit,
		SpentOutputRefs:       []*common.OutputRef{{TxHash: "source", OutputIndex: 1, KeyImage: "image"}},
	}
}

func TestDeckPreparesOwnedXMRObservationsBeforeSubmission(t *testing.T) {
	for _, limitMode := range []string{"value", "nil", "uninitialized"} {
		t.Run(limitMode, func(t *testing.T) {
			chain := common.XMRChain
			o, _, _ := setupErrataTestGossip(t)
			o.tssKeysignMetricMgr = metrics.NewTssKeysignMetricMgr()
			o.thorchainBridge = &MockThorchainBridge2{nodeStatus: stypes.NodeStatus_Active}
			o.lastNodeStatus = stypes.NodeStatus_Active
			o.chains = map[common.Chain]chainclients.ChainClient{chain: &mockChainClient{}}
			orphan, keep := errataTestDeckItem(t, chain, 1), errataTestDeckItem(t, chain, 2)
			switch limitMode {
			case "nil":
				keep.AggregatorTargetLimit = nil
			case "uninitialized":
				keep.AggregatorTargetLimit = new(cosmos.Uint)
			}
			var stored, rejected int
			keep.OnObservedTxStored = func() { stored++ }
			keep.OnObservedTxRejected = func(error) { rejected++ }
			deck := errataOrderingTxIn(chain, 100, 6,
				errataOrderingItem("invalid", 100, "invalid"), orphan, keep)
			deck.AllowFutureObservation = true
			key := TxInKey(deck)
			o.onDeck[key] = deck
			require.NoError(t, o.storage.AddOrUpdateTx(deck))
			entered, resume := make(chan struct{}), make(chan struct{})
			var work common.ObservedTxs
			var before, after []byte
			o.sendObservedTxsFn = func(txs common.ObservedTxs, allowFuture bool) {
				work = txs
				if allowFuture && len(txs) == 2 {
					before, _ = txs[1].Marshal()
				}
				close(entered)
				<-resume
				after, _ = txs[1].Marshal()
			}
			done := make(chan struct{})
			go func() { o.sendDeck(context.Background()); close(done) }()
			waitForObserverTest(t, entered)
			require.Len(t, work, 2)
			require.NotEmpty(t, before)
			o.lock.Lock()
			require.Len(t, deck.TxArray, 2, "invalid item must be removed before network submission")
			require.Same(t, keep, deck.TxArray[1], "callbacks stay on the original deck item")
			// Exercise both slice ownership and the mutable numeric backing data.
			keep.Coins[0].Amount.BigIntMut().SetUint64(999)
			keep.Gas[0].Amount.BigIntMut().SetUint64(998)
			if limitMode == "value" {
				keep.AggregatorTargetLimit.BigIntMut().SetUint64(997)
			} else {
				limit := cosmos.NewUint(997)
				if keep.AggregatorTargetLimit == nil {
					keep.AggregatorTargetLimit = &limit
				} else {
					*keep.AggregatorTargetLimit = limit
				}
			}
			keep.Coins[0].Asset = common.ETHAsset
			keep.SpentOutputRefs[0].KeyImage = "changed"
			keep.TxKey = "changed"
			keep.Memo = "changed"
			o.lock.Unlock()
			cleaned := make(chan struct{})
			var cleanupErr error
			go func() {
				cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: chain, TxID: common.TxID(orphan.Tx)}}})
				close(cleaned)
			}()
			waitForObserverTest(t, cleaned)
			require.NoError(t, cleanupErr)
			close(resume)
			waitForObserverTest(t, done)
			require.Equal(t, before, after, "deck mutation must not change independently prepared observations")
			require.Len(t, o.onDeck[key].TxArray, 1)
			require.Same(t, keep, o.onDeck[key].TxArray[0])
			require.Zero(t, stored)
			require.Zero(t, rejected)
			keep.OnObservedTxStored()
			keep.OnObservedTxRejected(errors.New("test callback identity"))
			require.Equal(t, 1, stored)
			require.Equal(t, 1, rejected)
			restored, err := o.storage.GetOnDeckTxs()
			require.NoError(t, err)
			require.Len(t, restored, 1)
			require.Len(t, restored[0].TxArray, 1)
			require.Equal(t, keep.Tx, restored[0].TxArray[0].Tx)
		})
	}
}

func TestNonXMRDeckSubmissionRemainsSerializedWithCleanup(t *testing.T) {
	o, _, _ := setupErrataTestGossip(t)
	o.tssKeysignMetricMgr = metrics.NewTssKeysignMetricMgr()
	o.thorchainBridge = &MockThorchainBridge2{nodeStatus: stypes.NodeStatus_Active}
	o.lastNodeStatus = stypes.NodeStatus_Active
	o.chains = map[common.Chain]chainclients.ChainClient{common.BTCChain: &mockChainClient{}}
	orphan, keep := errataTestDeckItem(t, common.BTCChain, 1), errataTestDeckItem(t, common.BTCChain, 2)
	deck := errataOrderingTxIn(common.BTCChain, 100, 6,
		errataOrderingItem("invalid", 100, "invalid"), orphan, keep)
	key := TxInKey(deck)
	o.onDeck[key] = deck
	require.NoError(t, o.storage.AddOrUpdateTx(deck))
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var submitted, submittedAtCleanup atomic.Bool
	o.sendObservedTxsFn = func(txs common.ObservedTxs, _ bool) {
		close(entered)
		<-resume
		submitted.Store(len(txs) == 2 && txs[0].Tx.ID.Equals(common.TxID(orphan.Tx)))
	}
	go func() { o.sendDeck(context.Background()); close(done) }()
	waitForObserverTest(t, entered)
	if o.lock.TryLock() {
		o.lock.Unlock()
		close(resume)
		waitForObserverTest(t, done)
		t.Fatal("non-XMR submission must retain serialization with cleanup")
	}
	o.storage.beforeErrataWrite = func() error {
		submittedAtCleanup.Store(submitted.Load())
		return nil
	}
	cleaned := make(chan struct{})
	var cleanupErr error
	go func() {
		cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.BTCChain, TxID: common.TxID(orphan.Tx)}}})
		close(cleaned)
	}()
	close(resume)
	waitForObserverTest(t, done)
	waitForObserverTest(t, cleaned)
	require.NoError(t, cleanupErr)
	require.True(t, submittedAtCleanup.Load(), "legacy cleanup must follow the prepared observation submission")
	require.Len(t, o.onDeck[key].TxArray, 1, "invalid and orphan items must both be removed")
	require.Same(t, keep, o.onDeck[key].TxArray[0])
}

type spentRefsGuardChain struct {
	chainclients.ChainClient
	guard chainclients.SpentOutputRefsGuard
}

func (c *spentRefsGuardChain) SetSpentOutputRefsGuard(g chainclients.SpentOutputRefsGuard) {
	c.guard = g
}

func TestObserverRequiresAndWiresSpentRefsRecoveryGuard(t *testing.T) {
	g, _, _, _, bridge, _ := setupTestGossip(t)
	chain := &spentRefsGuardChain{}
	o, err := NewObserver(nil, map[common.Chain]chainclients.ChainClient{common.XMRChain: chain},
		bridge, nil, t.TempDir(), metrics.NewTssKeysignMetricMgr(), g, "")
	require.NoError(t, err)
	defer o.storage.Close()
	require.NotNil(t, chain.guard)
	old := errataTestObservation("spent-refs-guard-constructor", common.XMRChain, 110)
	old.XmrMinedHeight = 100
	require.NoError(t, o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: old.Tx.ID}}}))
	release, ok := chain.guard(old)
	require.False(t, ok)
	require.Nil(t, release)
	old.XmrMinedHeight = 101
	release, ok = chain.guard(old)
	require.True(t, ok)
	release()
	_, err = NewObserver(nil, map[common.Chain]chainclients.ChainClient{common.XMRChain: chain},
		bridge, nil, t.TempDir(), metrics.NewTssKeysignMetricMgr(), nil, "")
	require.Error(t, err)
	_, err = NewObserver(nil, map[common.Chain]chainclients.ChainClient{common.XMRChain: &struct{ chainclients.ChainClient }{}},
		bridge, nil, t.TempDir(), metrics.NewTssKeysignMetricMgr(), g, "")
	require.ErrorContains(t, err, "does not support")
}

func TestDelayedSpentRefsImportAndOutcomeCannotCrossCleanup(t *testing.T) {
	for _, afterImport := range []bool{false, true} {
		t.Run(map[bool]string{false: "before import", true: "after import before callback"}[afterImport], func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			tx := errataTestObservation("spent-refs-delayed-import", common.XMRChain, 110)
			tx.XmrMinedHeight = 100
			tx.SpentOutputRefs = []common.OutputRef{{TxHash: "input", SpendTxHash: tx.Tx.ID.String()}}
			paused := make(chan struct{})
			resume := make(chan struct{})
			var resumeOnce sync.Once
			defer resumeOnce.Do(func() { close(resume) })
			done := make(chan bool, 1)
			g.SetObserverApplyCommittedSpentRefs(func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool) {
				if !afterImport {
					close(paused)
					<-resume
				}
				release, ok := g.acquireXMRSpentRefsAttempt(tx)
				if !ok {
					return common.ObservedTx{}, false
				}
				release()
				if afterImport {
					close(paused)
					<-resume
				}
				return tx, true
			})
			g.applyCommittedSpentRefsAsync(tx.ObservedPubKey, tx.Tx.ID, func(imported common.ObservedTx, applied bool) {
				if applied {
					g.recordCommittedXMRSpentRefs(xmrSpentRefsConflictKeyFor(imported), xmrSpentRefsIdentity(imported.SpentOutputRefs))
				}
				done <- applied
			})
			waitForObserverTest(t, paused)
			require.NoError(t, o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}}))
			resumeOnce.Do(func() { close(resume) })
			select {
			case applied := <-done:
				require.False(t, applied)
			case <-time.After(3 * time.Second):
				t.Fatal("callback did not finish")
			}
			g.mu.Lock()
			require.Empty(t, g.authorizedXMRSpentRefs)
			g.mu.Unlock()
		})
	}
}

func TestSpentRefsRecoveryGuardCanonicalRestartAndStorageFailure(t *testing.T) {
	dir := t.TempDir()
	storage, err := NewObserverStorage(dir, config.LevelDBOptions{})
	require.NoError(t, err)
	tx := errataTestObservation("spent-refs-restart", common.XMRChain, 110)
	tx.XmrMinedHeight = 100
	require.NoError(t, storage.applyErrata(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}}, nil))
	require.NoError(t, storage.Close())
	storage, err = NewObserverStorage(dir, config.LevelDBOptions{})
	require.NoError(t, err)
	g, _, _, _, _, _ := setupTestGossip(t)
	g.SetObserverStorage(storage)
	for _, height := range []int64{0, -1, 100, 101, 111} {
		tx.XmrMinedHeight = height
		release, ok := g.acquireXMRSpentRefsAttempt(tx)
		require.Equal(t, height == 101, ok)
		if ok {
			release()
		} else {
			require.Nil(t, release)
		}
	}
	require.NoError(t, storage.Close())
	_, ok := g.acquireXMRSpentRefsAttempt(tx)
	require.False(t, ok, "read failures fail closed")
	// Non-XMR recovery does not wait behind this XMR-specific writer.
	g.xmrErrataBarrier.Lock()
	done := make(chan struct{})
	go func() {
		release, ok := g.acquireXMRSpentRefsAttempt(errataTestObservation("btc", common.BTCChain, 100))
		if ok {
			release()
		}
		close(done)
	}()
	waitForObserverTest(t, done)
	g.xmrErrataBarrier.Unlock()
}

func TestPeerSpentRefsOutcomeRechecksErrataAdmission(t *testing.T) {
	for _, scenario := range []string{"unchanged", "orphaned", "remined", "permanently reverted", "storage failure"} {
		t.Run(scenario, func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			tx := errataTestObservation("peer-spent-refs-delayed-outcome", common.XMRChain, 110)
			tx.XmrMinedHeight = 100
			if scenario == "remined" || scenario == "permanently reverted" {
				tx.XmrMinedHeight = 101
			}
			tx.SpentOutputRefs = []common.OutputRef{{TxHash: "input", SpendTxHash: tx.Tx.ID.String()}}
			require.True(t, g.rememberAuthorizedXMRSpentRefs(tx))
			conflictKey := xmrSpentRefsConflictKeyFor(tx)
			identity := xmrSpentRefsIdentity(tx.SpentOutputRefs)

			imported, resume, importerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			t.Cleanup(func() {
				resumeOnce.Do(func() { close(resume) })
				select {
				case <-importerDone:
				case <-time.After(3 * time.Second):
					t.Error("spent-reference importer did not finish")
				}
			})
			outcomes := make(chan bool, 2)
			g.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, onOutcome func(bool)) {
				defer close(importerDone)
				// The client has completed its import, but Observer has not
				// accepted the successful outcome yet.
				close(imported)
				<-resume
				onOutcome(true)
			})
			g.applySpentRefsAsync(spentOutputRefsImport{Tx: tx}, func(applied bool) {
				g.resolveAuthorizedXMRSpentRefs(conflictKey, identity, applied)
				outcomes <- applied
			})
			waitForObserverTest(t, imported)

			block := btypes.ErrataBlock{
				Height: 100,
				Txs:    []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}},
			}
			switch scenario {
			case "orphaned", "remined":
				require.NoError(t, o.filterErrataTx(block))
			case "permanently reverted":
				require.NoError(t, o.filterErrataTx(block))
				require.NoError(t, o.handleErrataTxCommitted(common.ErrataTx{Chain: common.XMRChain, Id: tx.Tx.ID}, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
			case "storage failure":
				require.NoError(t, o.storage.db.Put([]byte(errataTombstoneKey(common.XMRChain, tx.Tx.ID.String())), []byte("corrupt"), nil))
			}
			resumeOnce.Do(func() { close(resume) })
			wantApplied := scenario == "unchanged" || scenario == "remined"
			select {
			case applied := <-outcomes:
				require.Equal(t, wantApplied, applied)
			case <-time.After(3 * time.Second):
				t.Fatal("spent-reference outcome did not finish")
			}
			waitForObserverTest(t, importerDone)
			require.Eventually(t, func() bool { return len(g.applySpentRefsSem) == 0 }, time.Second, time.Millisecond)
			require.Empty(t, outcomes, "an import must report its final outcome once")
			state, found := observerTestConflictState(g, tx)
			require.True(t, found)
			if wantApplied {
				require.Equal(t, xmrSpentRefsImportConfirmed, state)
			} else {
				require.Equal(t, xmrSpentRefsImportFailed, state, "a rejected outcome must release the pending import reservation")
			}
		})
	}
}

func TestSpentRefsOutcomeAllowsOtherChainsDuringErrataCleanup(t *testing.T) {
	_, g, _ := setupErrataTestGossip(t)
	g.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, onOutcome func(bool)) {
		onOutcome(true)
	})
	g.xmrErrataBarrier.Lock()
	t.Cleanup(func() {
		g.xmrErrataBarrier.Unlock()
		require.Eventually(t, func() bool { return len(g.applySpentRefsSem) == 0 }, time.Second, time.Millisecond)
	})
	outcomes := make(chan bool, 1)
	g.applySpentRefsAsync(spentOutputRefsImport{
		Tx: errataTestObservation("btc-spent-refs-outcome", common.BTCChain, 100),
	}, func(applied bool) { outcomes <- applied })
	select {
	case applied := <-outcomes:
		require.True(t, applied)
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated-chain outcome waited behind XMR cleanup")
	}
	require.Eventually(t, func() bool { return len(g.applySpentRefsSem) == 0 }, time.Second, time.Millisecond)
}
