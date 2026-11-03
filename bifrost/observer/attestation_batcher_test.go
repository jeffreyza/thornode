package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
)

// TestAttestationBatcher tests the attestation batcher functionality
func TestAttestationBatcher(t *testing.T) {
	// Setup logger for tests
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	t.Run("creates batcher with custom values", func(t *testing.T) {
		host := NewMockHost([]peer.ID{})

		batchInterval := 3 * time.Second
		maxBatchSize := int64(200)
		peerTimeout := 30 * time.Second
		concurrentSends := 5

		batcher := NewAttestationBatcher(host, logger, nil, batchInterval, maxBatchSize, peerTimeout, concurrentSends)

		assert.Equal(t, batchInterval, batcher.batchInterval)
		assert.Equal(t, maxBatchSize, batcher.maxBatchSize)
		assert.Equal(t, peerTimeout, batcher.peerTimeout)
		assert.Equal(t, concurrentSends, batcher.peerMgr.limit)
	})

	t.Run("clamps mimir-driven batch size", func(t *testing.T) {
		host := NewMockHost([]peer.ID{})
		batcher := NewAttestationBatcher(host, logger, nil, time.Second, 100, time.Second, 1)

		batcher.updateMaxBatchSize(maxAllowedBatchSize * 10)
		assert.Equal(t, maxAllowedBatchSize, batcher.getMaxBatchSize())

		batcher.updateMaxBatchSize(500)
		assert.Equal(t, int64(500), batcher.getMaxBatchSize())

		// invalid values are ignored
		batcher.updateMaxBatchSize(0)
		assert.Equal(t, int64(500), batcher.getMaxBatchSize())
	})

	t.Run("batches and broadcasts attestations", func(t *testing.T) {
		// Create peer IDs
		peer1 := peer.ID("peer1")
		peer2 := peer.ID("peer2")
		peer3 := peer.ID("peer3")
		peers := []peer.ID{peer1, peer2, peer3}

		// Create a mock host that tracks stream creation
		mockHost := NewBatcherMockHost(peers)

		// Create batcher with short batch interval for testing
		batcher := NewAttestationBatcher(mockHost, logger, nil, 50*time.Millisecond, 10, 1*time.Second, 4)

		// Set active validator getter
		batcher.setActiveValGetter(func() map[peer.ID]bool {
			return map[peer.ID]bool{
				peer1: true,
				peer2: true,
				peer3: true,
			}
		})

		// Start the batcher
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go batcher.Start(ctx)

		// Add some transactions to batch
		for i := 0; i < 5; i++ {
			batcher.AddObservedTx(common.AttestTx{
				ObsTx: common.ObservedTx{
					Tx: common.Tx{
						ID:    common.TxID(fmt.Sprintf("tx%d", i)),
						Chain: common.BTCChain,
					},
				},
				Inbound: true,
				Attestation: &common.Attestation{
					PubKey:    []byte("pubkey"),
					Signature: []byte(fmt.Sprintf("sig%d", i)),
				},
			})
		}

		// Wait for batch interval plus a little buffer
		time.Sleep(150 * time.Millisecond)

		// Verify streams were created to peers
		mockHost.mu.Lock()
		streamCount := len(mockHost.createdStreams)
		mockHost.mu.Unlock()

		assert.GreaterOrEqual(t, streamCount, 2, "Should create streams to both peers")

		// Verify the batch protocol was used
		mockHost.mu.Lock()
		batchProtocolUsed := false
		for _, stream := range mockHost.createdStreams {
			if stream.protocol == batchedAttestationProtocol {
				batchProtocolUsed = true
				break
			}
		}
		mockHost.mu.Unlock()

		assert.True(t, batchProtocolUsed, "Should use batch protocol")

		// Check metrics
		// txBatched, err := m.GetCounterVec(metrics.MessagesBatched).GetMetricWithLabelValues("observed_tx")
		// require.NoError(t, err)
		// assert.Equal(t, float64(5), testutil.ToFloat64(txBatched))

		// Reset mock host for next test
		mockHost.clearStreams()

		// Add different types of attestations
		batcher.AddNetworkFee(common.AttestNetworkFee{
			NetworkFee: &common.NetworkFee{
				Chain:  common.BTCChain,
				Height: 100,
			},
			Attestation: &common.Attestation{
				PubKey:    []byte("pubkey"),
				Signature: []byte("nf-sig"),
			},
		})

		batcher.AddSolvency(common.AttestSolvency{
			Solvency: &common.Solvency{
				Chain: common.BTCChain,
				Id:    common.TxID("solvency-id"),
			},
			Attestation: &common.Attestation{
				PubKey:    []byte("pubkey"),
				Signature: []byte("solv-sig"),
			},
		})

		batcher.AddErrataTx(common.AttestErrataTx{
			ErrataTx: &common.ErrataTx{
				Chain: common.BTCChain,
				Id:    common.TxID("errata-id"),
			},
			Attestation: &common.Attestation{
				PubKey:    []byte("pubkey"),
				Signature: []byte("errata-sig"),
			},
		})

		// Wait for batch interval
		time.Sleep(150 * time.Millisecond)

		// Check metrics for other attestation types
		// nfBatched, err := m.GetCounterVec(metrics.MessagesBatched).GetMetricWithLabelValues("network_fee")
		// require.NoError(t, err)
		// assert.Equal(t, float64(1), testutil.ToFloat64(nfBatched))

		// solvencyBatched, err := m.GetCounterVec(metrics.MessagesBatched).GetMetricWithLabelValues("solvency")
		// require.NoError(t, err)
		// assert.Equal(t, float64(1), testutil.ToFloat64(solvencyBatched))

		// errataBatched, err := m.GetCounterVec(metrics.MessagesBatched).GetMetricWithLabelValues("errata_tx")
		// require.NoError(t, err)
		// assert.Equal(t, float64(1), testutil.ToFloat64(errataBatched))
	})

	t.Run("forces send when batch size is exceeded", func(t *testing.T) {
		// Create peer IDs
		peer1 := peer.ID("peer1")
		peer2 := peer.ID("peer2")
		peers := []peer.ID{peer1, peer2}

		// Create a mock host that tracks stream creation
		mockHost := NewBatcherMockHost(peers)

		// Create batcher with long batch interval so it won't trigger naturally
		batcher := NewAttestationBatcher(mockHost, logger, nil, 10*time.Second, 5, 1*time.Second, 4)

		// Set active validator getter
		batcher.setActiveValGetter(func() map[peer.ID]bool {
			return map[peer.ID]bool{
				peer1: true,
				peer2: true,
			}
		})

		// Start the batcher
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go batcher.Start(ctx)

		// Add transactions just up to the batch size limit to trigger force send
		for i := 0; i < 5; i++ {
			batcher.AddObservedTx(common.AttestTx{
				ObsTx: common.ObservedTx{
					Tx: common.Tx{
						ID:    common.TxID(fmt.Sprintf("tx%d", i)),
						Chain: common.BTCChain,
					},
				},
				Attestation: &common.Attestation{
					PubKey:    []byte("pubkey"),
					Signature: []byte(fmt.Sprintf("sig%d", i)),
				},
			})
		}

		// Wait a bit for force send to happen
		time.Sleep(500 * time.Millisecond)

		// Verify streams were created to the peer
		mockHost.mu.Lock()
		streamCount := len(mockHost.createdStreams)
		mockHost.mu.Unlock()

		assert.GreaterOrEqual(t, streamCount, 1, "Should force send and create stream to peer")

		// Clear mock host for next test
		mockHost.clearStreams()

		// Add a network fee batch that exceeds limit
		for i := 0; i < 6; i++ {
			batcher.AddNetworkFee(common.AttestNetworkFee{
				NetworkFee: &common.NetworkFee{
					Chain:  common.BTCChain,
					Height: int64(100 + i),
				},
				Attestation: &common.Attestation{
					PubKey:    []byte("pubkey"),
					Signature: []byte(fmt.Sprintf("nf-sig%d", i)),
				},
			})
		}

		// Wait a bit for force send
		time.Sleep(500 * time.Millisecond)

		// Verify another stream was created
		mockHost.mu.Lock()
		newStreamCount := len(mockHost.createdStreams)
		mockHost.mu.Unlock()

		assert.GreaterOrEqual(t, newStreamCount, 1, "Should force send network fees batch")
	})

	t.Run("handles batch clearing properly", func(t *testing.T) {
		// This test covers queue clearing; ticker and peer delivery are tested
		// above. Avoid background work and wall-clock scheduling assumptions.
		mockHost := NewBatcherMockHost(nil)

		// Create batcher
		batcher := NewAttestationBatcher(mockHost, logger, nil, 100*time.Millisecond, 10, 1*time.Second, 4)
		batcher.setActiveValGetter(func() map[peer.ID]bool { return map[peer.ID]bool{} })

		// Add a mix of attestation types
		for i := 0; i < 3; i++ {
			batcher.AddObservedTx(common.AttestTx{
				ObsTx: common.ObservedTx{
					Tx: common.Tx{
						ID:    common.TxID(fmt.Sprintf("tx%d", i)),
						Chain: common.BTCChain,
					},
				},
				Attestation: &common.Attestation{
					PubKey:    []byte("pubkey"),
					Signature: []byte(fmt.Sprintf("sig%d", i)),
				},
			})

			batcher.AddNetworkFee(common.AttestNetworkFee{
				NetworkFee: &common.NetworkFee{
					Chain:  common.BTCChain,
					Height: int64(100 + i),
				},
				Attestation: &common.Attestation{
					PubKey:    []byte("pubkey"),
					Signature: []byte(fmt.Sprintf("nf-sig%d", i)),
				},
			})
		}

		assert.Len(t, batcher.observedTxBatch, 3)
		assert.Len(t, batcher.networkFeeBatch, 3)
		batcher.sendBatches(context.Background(), false)
		assert.Empty(t, batcher.observedTxBatch, "Observed tx batch should be cleared after sending")
		assert.Empty(t, batcher.networkFeeBatch, "Network fee batch should be cleared after sending")

		// Check batch clears metric
		// batchClears := m.GetCounter(metrics.BatchClears)
		// assert.GreaterOrEqual(t, testutil.ToFloat64(batchClears), float64(1), "Batch clears metric should be incremented")
	})
}

// BatcherMockHost is a more detailed mock host implementation for testing the batcher
type BatcherMockHost struct {
	*MockHost
	mu             sync.Mutex
	createdStreams []*mockStreamInfo
	// noAck models a peer that accepts the stream but never reads/replies: the
	// stream opens, but no "done" acknowledgment is ever written back.
	noAck bool
}

type mockStreamInfo struct {
	peer     peer.ID
	protocol protocol.ID
	stream   *MockStream
}

// NewBatcherMockHost creates a new detailed mock host for testing
func NewBatcherMockHost(peers []peer.ID) *BatcherMockHost {
	return &BatcherMockHost{
		MockHost:       NewMockHost(peers),
		createdStreams: make([]*mockStreamInfo, 0),
	}
}

func (h *BatcherMockHost) NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (network.Stream, error) {
	if p == h.ID() {
		return nil, errors.New("cannot create stream to self")
	}

	clientToServer := &bytes.Buffer{}
	serverToClient := &bytes.Buffer{}

	// Create a stream that writes to clientToServer and reads from serverToClient
	stream := &MockStream{
		reader: serverToClient,
		writer: clientToServer,
		peer:   p,
		mu:     new(sync.Mutex),
	}

	// Set up a goroutine to automatically respond with "done", unless this host
	// models an unresponsive peer that never acknowledges.
	if !h.noAck {
		go func() {
			time.Sleep(50 * time.Millisecond)
			// Write "done" using proper protocol format
			lengthBytes := make([]byte, p2p.LengthHeader)
			binary.LittleEndian.PutUint32(lengthBytes, 4) // "done" is 4 bytes
			stream.mu.Lock()
			defer stream.mu.Unlock()
			serverToClient.Write(lengthBytes)
			serverToClient.WriteString("done")
		}()
	}

	var pid protocol.ID
	if len(pids) > 0 {
		pid = pids[0]
	}

	// Store info about created stream
	h.mu.Lock()
	defer h.mu.Unlock()

	h.createdStreams = append(h.createdStreams, &mockStreamInfo{
		peer:     p,
		protocol: pid,
		stream:   stream,
	})

	return stream, nil
}

func (h *BatcherMockHost) clearStreams() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.createdStreams = make([]*mockStreamInfo, 0)
}

// TestBroadcastToUnresponsivePeerTripsBreaker reproduces a peer that accepts the
// stream but never reads/ACKs. Each send must free its token within the short
// ACK timeout (not the full peer timeout); after the threshold the breaker opens
// and further sends are skipped without even opening a stream.
func TestBroadcastToUnresponsivePeerTripsBreaker(t *testing.T) {
	logger := zerolog.Nop()
	peerID := peer.ID("unresponsive-peer")
	// peers[0] is the mock host's own ID; target a non-self peer.
	mockHost := NewBatcherMockHost([]peer.ID{peer.ID("self"), peerID})
	mockHost.noAck = true // accepts the stream, never acknowledges

	// Generous peer timeout (5s) but a short dedicated ACK timeout (200ms): this
	// isolates the ACK-timeout behaviour from the peer timeout. Pre-change code
	// (ACK read bound only by peerTimeout) would hold the token ~5s per send and
	// fail the per-send assertion below.
	// A single send slot makes every subsequent attempt depend on the previous
	// timeout returning its token.
	batcher := NewAttestationBatcher(mockHost, logger, nil, time.Hour, 10, 5*time.Second, 1)
	batcher.ackTimeout = 200 * time.Millisecond

	payload := []byte("batch")

	// The elapsed time includes CI scheduling delays as well as the 200ms ACK
	// timeout. Allow headroom while still rejecting the full 5s peer timeout.
	for i := 0; i < peerBreakerThreshold; i++ {
		start := time.Now()
		batcher.broadcastToPeer(context.Background(), peerID, payload)
		require.Less(t, time.Since(start), 2*time.Second,
			"send to a non-ACKing peer must free the token via the short ACK timeout, not ride the 5s peer timeout")

		batcher.peerMgr.mu.Lock()
		sem := batcher.peerMgr.semaphores[peerID]
		batcher.peerMgr.mu.Unlock()
		require.NotNil(t, sem)
		sem.mu.Lock()
		available, references := len(sem.tokens), sem.refCount
		sem.mu.Unlock()
		require.Equal(t, 1, available, "timed-out send must return its token")
		require.Zero(t, references, "timed-out send must release its semaphore reference")
	}

	mockHost.mu.Lock()
	streamsAfterThreshold := len(mockHost.createdStreams)
	mockHost.mu.Unlock()
	require.Equal(t, peerBreakerThreshold, streamsAfterThreshold, "each pre-breaker send opens one stream")
	require.True(t, batcher.peerMgr.shouldSkipPeer(peerID), "breaker should be open after the threshold")

	// A further send while the breaker is open must be skipped: no new stream.
	batcher.broadcastToPeer(context.Background(), peerID, payload)
	mockHost.mu.Lock()
	streamsAfterSkip := len(mockHost.createdStreams)
	mockHost.mu.Unlock()
	assert.Equal(t, streamsAfterThreshold, streamsAfterSkip, "open breaker must skip the send (no new stream)")
}
