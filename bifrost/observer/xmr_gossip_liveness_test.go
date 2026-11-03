package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestBatchLoopProgressesWhileXMRPeerStalls(t *testing.T) {
	b := newErrataTestBatcher(t, 1)
	b.batchInterval = time.Hour // force notifications drive the real loop
	b.peerTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	xmrStarted, btcSent := make(chan struct{}), make(chan struct{})
	xmrDone := make(chan struct{})
	var running atomic.Int32
	var payloadChanged atomic.Bool
	b.broadcastToPeerFn = func(ctx context.Context, _ peer.ID, payload []byte) {
		var batch common.AttestationBatch
		if err := batch.Unmarshal(payload); err != nil {
			payloadChanged.Store(true)
			return
		}
		if containsXMRObservedTx(batch.AttestTxs) {
			defer close(xmrDone)
			running.Add(1)
			defer running.Add(-1)
			snapshot := bytes.Clone(payload)
			close(xmrStarted)
			<-ctx.Done()
			payloadChanged.Store(!bytes.Equal(snapshot, payload))
			return
		}
		close(btcSent)
	}
	go func() { b.Start(ctx); close(loopDone) }()
	defer func() {
		cancel()
		waitForObserverTest(t, loopDone)
		waitForObserverTest(t, xmrDone)
	}()
	b.AddObservedTx(common.AttestTx{ObsTx: errataTestObservation("stalled-xmr", common.XMRChain, 100)})
	b.triggerBatchSend()
	waitForObserverTest(t, xmrStarted)
	b.AddObservedTx(common.AttestTx{ObsTx: errataTestObservation("later-btc", common.BTCChain, 100)})
	b.triggerBatchSend()
	select {
	case <-btcSent:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("non-XMR batch waited for the earlier stalled XMR peer")
	}
	require.Equal(t, int32(1), running.Load())
	cancel()
	waitForObserverTest(t, xmrDone)
	require.Zero(t, running.Load())
	require.False(t, payloadChanged.Load(), "later batch reuse must not change an in-flight payload")
}

// Pause one real framing/ACK exchange. Reset interrupts the simulated peer,
// allowing the old whole-transfer watchdog to fail these regressions cleanly.
type pausedAttestationStateStream struct {
	*AutoResponderStream
	pausePrefix     []byte
	lastPrefix      byte
	paused          bool
	ready           chan struct{}
	resume          chan struct{}
	reset           chan struct{}
	resetOnce       sync.Once
	failWritePrefix []byte
	failReadPrefix  []byte
	dataWritten     atomic.Bool
}

func newPausedAttestationStateStream(prefix []byte) *pausedAttestationStateStream {
	return &pausedAttestationStateStream{
		AutoResponderStream: NewAutoResponderStream("state-peer"),
		pausePrefix:         prefix, ready: make(chan struct{}), resume: make(chan struct{}), reset: make(chan struct{}),
	}
}

func (s *pausedAttestationStateStream) Write(p []byte) (int, error) {
	if len(p) > p2p.LengthHeader {
		s.lastPrefix = p[p2p.LengthHeader]
		if len(s.failWritePrefix) > 0 && s.lastPrefix == s.failWritePrefix[0] {
			return 0, errors.New("injected stream write failure")
		}
	}
	n, err := s.AutoResponderStream.Write(p)
	if err == nil && n == len(p) && s.lastPrefix == prefixBatchData[0] {
		s.dataWritten.Store(true)
	}
	return n, err
}

func (s *pausedAttestationStateStream) Read(p []byte) (int, error) {
	if len(s.failReadPrefix) > 0 && s.lastPrefix == s.failReadPrefix[0] {
		return 0, io.ErrUnexpectedEOF
	}
	if !s.paused && s.lastPrefix == s.pausePrefix[0] {
		s.paused = true
		close(s.ready)
		select {
		case <-s.resume:
		case <-s.reset:
			return 0, io.EOF
		}
	}
	return s.AutoResponderStream.Read(p)
}

func TestStateExportRejectsQuarantinedOrUnreadableSnapshot(t *testing.T) {
	for _, mode := range []string{"permanent", "unavailable", "corrupt", "all_removed"} {
		t.Run(mode, func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			for _, height := range []int64{100, 101} {
				tx := errataTestObservation("restricted", common.XMRChain, height)
				require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			}
			if mode != "all_removed" {
				tx := errataTestObservation("btc", common.BTCChain, 100)
				require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			}
			stream := newPausedAttestationStateStream(prefixBatchBegin)
			defer func() { require.NoError(t, stream.Reset()) }()
			done := make(chan struct{})
			go func() { g.sendAttestationState(stream); close(done) }()
			waitForObserverTest(t, stream.ready)
			switch mode {
			case "permanent", "all_removed":
				require.NoError(t, o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{
					{Chain: common.XMRChain, TxID: "restricted"},
				}}))
				require.NoError(t, o.handleErrataTxCommitted(common.ErrataTx{Chain: common.XMRChain, Id: "restricted"}, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
			case "unavailable":
				g.SetObserverStorage(nil)
			case "corrupt":
				require.NoError(t, o.storage.db.Put([]byte(errataTombstoneKey(common.XMRChain, "restricted")), []byte("corrupt"), nil))
			}
			close(stream.resume)
			waitForObserverTest(t, done)
			batches := readExportedAttestationBatches(t, stream)
			var count int
			for _, batch := range batches {
				for _, tx := range batch.QuoTxs {
					require.Equal(t, common.BTCChain, tx.ObsTx.Tx.Chain)
					count++
				}
			}
			if mode == "all_removed" {
				require.Zero(t, count)
			} else {
				require.Equal(t, 1, count, "unrelated state must remain transferable")
			}
		})
	}
}

func TestStateFrameFailuresAllowRetry(t *testing.T) {
	for _, failure := range []struct {
		name        string
		write, read []byte
	}{
		{"header_write", prefixBatchHeader, nil},
		{"header_ack", nil, prefixBatchHeader},
		{"data_write", prefixBatchData, nil},
		{"data_ack", nil, prefixBatchData},
	} {
		t.Run(failure.name, func(t *testing.T) {
			_, g, _ := setupErrataTestGossip(t)
			tx := errataTestObservation("retry-state", common.XMRChain, 100)
			require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			msg := g.batcher.observedTxBatch[0]
			batch := common.QuorumState{QuoTxs: []*common.QuorumTx{{ObsTx: tx, Inbound: true, Attestations: []*common.Attestation{msg.Attestation}}}}
			stream := newPausedAttestationStateStream(prefixBatchEnd) // no pause in a single-frame call
			stream.failWritePrefix, stream.failReadPrefix = failure.write, failure.read
			require.Error(t, g.sendAttestationStateBatch(stream, 0, batch, time.Now().Add(defaultStateSyncTimeout)))
			require.True(t, g.xmrErrataBarrier.TryLock(), "failed frame must release cleanup")
			g.xmrErrataBarrier.Unlock()
			require.Len(t, g.observedTxs, 1, "failed export must retain retryable evidence")
			require.NoError(t, g.sendAttestationStateBatch(NewAutoResponderStream("retry-peer"), 0, batch, time.Now().Add(defaultStateSyncTimeout)))
		})
	}
}

func (s *pausedAttestationStateStream) Reset() error {
	s.resetOnce.Do(func() { close(s.reset) })
	return nil
}

func readExportedAttestationBatches(t *testing.T, stream *pausedAttestationStateStream) []common.QuorumState {
	t.Helper()
	messages := ParseMessages(stream.GetWrittenData())
	require.NotEmpty(t, messages)
	require.Equal(t, prefixBatchEnd, messages[len(messages)-1], "transfer must finish, not silently truncate")
	count := int(binary.LittleEndian.Uint32(messages[0][1:]))
	var batches []common.QuorumState
	for i, message := range messages {
		if !bytes.HasPrefix(message, prefixBatchData) {
			continue
		}
		require.Greater(t, i, 0)
		header := messages[i-1]
		require.True(t, bytes.HasPrefix(header, prefixBatchHeader))
		require.Equal(t, int(binary.LittleEndian.Uint32(header[5:9])), len(message)-1)
		var batch common.QuorumState
		require.NoError(t, batch.Unmarshal(message[1:]))
		batches = append(batches, batch)
	}
	require.Len(t, batches, count, "filtered frames must retain the announced protocol shape")
	return batches
}

func TestStateExportCanExceedOneBlock(t *testing.T) {
	_, g, _ := setupErrataTestGossip(t)
	g.config.PeerTimeout = 20 * time.Second
	for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
		tx := errataTestObservation(chain.String(), chain, 100)
		require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
	}
	stream := newPausedAttestationStateStream(prefixBatchBegin)
	defer func() { require.NoError(t, stream.Reset()) }()
	done := make(chan struct{})
	go func() { g.sendAttestationState(stream); close(done) }()
	waitForObserverTest(t, stream.ready)
	select {
	case <-done:
		t.Fatal("healthy transfer was reset by the XMR operation budget")
	case <-time.After(constants.ThorchainBlockTime + 100*time.Millisecond):
	}
	close(stream.resume)
	waitForObserverTest(t, done)
	batches := readExportedAttestationBatches(t, stream)
	var count int
	for _, batch := range batches {
		count += len(batch.QuoTxs)
	}
	require.Equal(t, 2, count)
}

func TestStateExportRevalidatesBetweenFrames(t *testing.T) {
	for _, beforeFirst := range []bool{true, false} {
		name, prefix := "between_frames", prefixBatchData
		if beforeFirst {
			name, prefix = "before_first_frame", prefixBatchBegin
		}
		t.Run(name, func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			g.config.PeerTimeout = 80 * time.Millisecond
			previousBudget := maxAttestationBatchBytes
			maxAttestationBatchBytes = 1 // one item per frame; map order is irrelevant
			defer func() { maxAttestationBatchBytes = previousBudget }()
			for _, tx := range []common.ObservedTx{
				errataTestObservation("orphan", common.XMRChain, 100),
				errataTestObservation("orphan", common.XMRChain, 101),
				errataTestObservation("second-orphan", common.XMRChain, 100),
				errataTestObservation("btc", common.BTCChain, 100),
			} {
				require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
			}
			stream := newPausedAttestationStateStream(prefix)
			defer func() { require.NoError(t, stream.Reset()) }()
			done := make(chan struct{})
			go func() { g.sendAttestationState(stream); close(done) }()
			waitForObserverTest(t, stream.ready)
			cleaned := make(chan struct{})
			var cleanupErr error
			go func() {
				cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{
					{Chain: common.XMRChain, TxID: "orphan"},
					{Chain: common.XMRChain, TxID: "second-orphan"},
				}})
				close(cleaned)
			}()
			waitForObserverTest(t, cleaned)
			require.NoError(t, cleanupErr)
			select {
			case <-done:
				t.Fatal("cleanup should complete while the transfer is still awaiting its ACK")
			default:
			}
			close(stream.resume)
			waitForObserverTest(t, done)
			batches := readExportedAttestationBatches(t, stream)
			var btc, remined bool
			for i, batch := range batches {
				for _, tx := range batch.QuoTxs {
					switch {
					case tx.ObsTx.Tx.Chain.Equals(common.BTCChain):
						btc = true
					case tx.ObsTx.BlockHeight == 101:
						remined = true
					case beforeFirst || i > 0:
						t.Fatalf("frame %d newly sent an orphan after cleanup", i)
					}
				}
			}
			require.True(t, btc)
			require.True(t, remined, "height-scoped cleanup must preserve the re-mined view")
		})
	}
}

func TestCleanupPersistsWhileStateHeaderACKStalls(t *testing.T) {
	o, g, _ := setupErrataTestGossip(t)
	tx := errataTestObservation("in-flight-state", common.XMRChain, 100)
	require.NoError(t, g.AttestObservedTx(context.Background(), &tx, true, false))
	stream := newPausedAttestationStateStream(prefixBatchHeader)
	defer func() { require.NoError(t, stream.Reset()) }()
	done := make(chan struct{})
	go func() { g.sendAttestationState(stream); close(done) }()
	waitForObserverTest(t, stream.ready)
	var writtenAtCleanup atomic.Bool
	o.storage.beforeErrataWrite = func() error {
		writtenAtCleanup.Store(stream.dataWritten.Load())
		return nil
	}
	cleaned := make(chan struct{})
	var cleanupErr error
	go func() {
		cleanupErr = o.filterErrataTx(btypes.ErrataBlock{Height: 100, Txs: []btypes.ErrataTx{{Chain: common.XMRChain, TxID: tx.Tx.ID}}})
		close(cleaned)
	}()
	waitForObserverTest(t, cleaned)
	require.NoError(t, cleanupErr)
	select {
	case <-done:
		t.Fatal("cleanup must complete without ending the stalled state transfer")
	default:
	}
	close(stream.resume)
	waitForObserverTest(t, done)
	require.False(t, writtenAtCleanup.Load())
	require.Len(t, readExportedAttestationBatches(t, stream)[0].QuoTxs, 1, "a prepared frame can still arrive after cleanup")
	require.Empty(t, g.observedTxs)
}
