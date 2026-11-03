package observer

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
)

// we have one semaphore per peer (per active validator node), so we don't need to prune that often
// but we do need to prune it, because we don't want to keep semaphores of nodes that aren't
// online anymore around forever.
const semaphorePruneInterval = 5 * time.Minute

type AttestationBatcher struct {
	observedTxBatch []*common.AttestTx
	networkFeeBatch []*common.AttestNetworkFee
	solvencyBatch   []*common.AttestSolvency
	errataTxBatch   []*common.AttestErrataTx
	priceFeedBatch  []*common.AttestPriceFeed

	batchPool sync.Pool

	mu             sync.Mutex
	batchInterval  time.Duration
	maxBatchSize   int64
	maxBatchSizeMu sync.Mutex
	peerTimeout    time.Duration // Timeout for peer send
	ackTimeout     time.Duration // Short deadline for the post-write ACK read

	getActiveValidators func() map[peer.ID]bool

	lastBatchSent time.Time
	batchTicker   *time.Ticker
	forceSendChan chan struct{} // Channel to signal immediate send

	host    host.Host
	logger  zerolog.Logger
	metrics *batchMetrics

	peerMgr           *peerManager
	observerStorage   *ObserverStorage // protected by mu; wired before startup
	broadcastToPeerFn func(context.Context, peer.ID, []byte)
}

// Metrics for the batcher
type batchMetrics struct {
	BatchSends      prometheus.Counter
	BatchClears     prometheus.Counter
	MessagesBatched *prometheus.CounterVec // By type
	BatchSize       prometheus.Histogram
	BatchSendTime   prometheus.Histogram
}

// NewAttestationBatcher creates a new instance of AttestationBatcher
func NewAttestationBatcher(
	host host.Host,
	logger zerolog.Logger,
	m *metrics.Metrics,
	batchInterval time.Duration,
	maxBatchSize int64,
	peerTimeout time.Duration,
	peerConcurrentSends int,
) *AttestationBatcher {
	// Create batch metrics
	batchMetrics := &batchMetrics{
		BatchSends:      m.GetCounter(metrics.BatchSends),
		BatchClears:     m.GetCounter(metrics.BatchClears),
		MessagesBatched: m.GetCounterVec(metrics.MessagesBatched),
		BatchSize:       m.GetHistograms(metrics.BatchSize),
		BatchSendTime:   m.GetHistograms(metrics.BatchSendTime),
	}

	logger = logger.With().Str("module", "attestation_batcher").Logger()

	b := &AttestationBatcher{
		// Initialize with empty slices with initial capacity
		observedTxBatch: make([]*common.AttestTx, 0, maxBatchSize),
		networkFeeBatch: make([]*common.AttestNetworkFee, 0, maxBatchSize),
		solvencyBatch:   make([]*common.AttestSolvency, 0, maxBatchSize),
		errataTxBatch:   make([]*common.AttestErrataTx, 0, maxBatchSize),

		batchInterval: batchInterval,
		maxBatchSize:  maxBatchSize,
		peerTimeout:   peerTimeout,
		ackTimeout:    attestationAckTimeout,

		peerMgr: newPeerManager(logger, peerConcurrentSends),

		lastBatchSent: time.Time{}, // Zero time

		host:    host,
		logger:  logger,
		metrics: batchMetrics,

		forceSendChan: make(chan struct{}, 1), // Buffer of 1 to avoid blocking
	}

	// sync.Pool for reusing AttestationBatch objects to reduce memory allocations.
	b.batchPool = sync.Pool{New: b.newAttestationBatch}

	return b
}

func (b *AttestationBatcher) getMaxBatchSize() int64 {
	b.maxBatchSizeMu.Lock()
	defer b.maxBatchSizeMu.Unlock()
	return b.maxBatchSize
}

func (b *AttestationBatcher) newAttestationBatch() interface{} {
	maxBatchSize := b.getMaxBatchSize()
	return &common.AttestationBatch{
		AttestTxs:         make([]*common.AttestTx, 0, maxBatchSize), // Preallocate with maxBatchSize capacity
		AttestNetworkFees: make([]*common.AttestNetworkFee, 0, maxBatchSize),
		AttestSolvencies:  make([]*common.AttestSolvency, 0, maxBatchSize),
		AttestErrataTxs:   make([]*common.AttestErrataTx, 0, maxBatchSize),
	}
}

func (b *AttestationBatcher) setActiveValGetter(getter func() map[peer.ID]bool) {
	b.getActiveValidators = getter
}

// updateMaxBatchSize updates the maximum batch size at runtime.
func (b *AttestationBatcher) updateMaxBatchSize(newSize int64) {
	if newSize <= 0 {
		b.logger.Error().Int64("new_size", newSize).Msg("invalid batch size, must be greater than 0")
		return
	}
	if newSize > maxAllowedBatchSize {
		b.logger.Warn().
			Int64("new_size", newSize).
			Int64("clamped_to", maxAllowedBatchSize).
			Msg("batch size above allowed maximum, clamping")
		newSize = maxAllowedBatchSize
	}

	b.maxBatchSizeMu.Lock()
	defer b.maxBatchSizeMu.Unlock()

	oldSize := b.maxBatchSize
	// Set the new max batch size
	b.maxBatchSize = newSize

	b.logger.Info().Int64("old_size", oldSize).Int64("new_size", newSize).Msg("max batch size updated")
}

func (b *AttestationBatcher) Start(ctx context.Context) {
	b.batchTicker = time.NewTicker(b.batchInterval)

	defer func() {
		b.batchTicker.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			// Context is done, exit the loop
			// don't need to worry about sending any batches,
			// as next startup will send from the deck again.
			return
		case <-b.batchTicker.C:
			b.sendBatches(ctx, false)
		case <-b.forceSendChan:
			b.sendBatches(ctx, true)
		}
	}
}

func (b *AttestationBatcher) sendBatches(ctx context.Context, force bool) {
	if ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	max := b.getMaxBatchSize()

	// Only send if we have messages to send or enough time has passed
	hasMessages := len(b.observedTxBatch) > 0 ||
		len(b.networkFeeBatch) > 0 ||
		len(b.solvencyBatch) > 0 ||
		len(b.errataTxBatch) > 0 ||
		len(b.priceFeedBatch) > 0
	timeThresholdMet := time.Since(b.lastBatchSent) >= b.batchInterval

	if !hasMessages || (!timeThresholdMet && !force) {
		b.mu.Unlock()
		return
	}

	start := time.Now()
	// Get a batched message from the pool
	batch, ok := b.batchPool.Get().(*common.AttestationBatch)
	if !ok {
		batch = &common.AttestationBatch{
			AttestTxs:         make([]*common.AttestTx, 0, max),
			AttestNetworkFees: make([]*common.AttestNetworkFee, 0, max),
			AttestSolvencies:  make([]*common.AttestSolvency, 0, max),
			AttestErrataTxs:   make([]*common.AttestErrataTx, 0, max),
			AttestPriceFeeds:  make([]*common.AttestPriceFeed, 0, max),
		}
	}

	// Take items up to both the item-count limit and a shared byte budget so
	// the marshaled frame stays under the wire-level p2p.MaxPayload read cap.
	// Anything left behind is sent by a follow-up flush.
	clear := true
	bytesUsed := 0
	takeWithinLimits := func(pendingLen int64, itemSize func(int64) int) int64 {
		n := pendingLen
		if n > max {
			n = max
		}
		var i int64
		for ; i < n; i++ {
			sz := itemSize(i) + protoItemOverhead
			// Always take the first item of an empty frame so a single
			// oversized item cannot stall the queue forever.
			if bytesUsed > 0 && bytesUsed+sz > maxAttestationBatchBytes {
				break
			}
			bytesUsed += sz
		}
		if i < pendingLen {
			clear = false
		}
		return i
	}

	batchSizeTx := takeWithinLimits(int64(len(b.observedTxBatch)),
		func(i int64) int { return b.observedTxBatch[i].Size() })
	batchSizeNF := takeWithinLimits(int64(len(b.networkFeeBatch)),
		func(i int64) int { return b.networkFeeBatch[i].Size() })
	batchSizeSolvency := takeWithinLimits(int64(len(b.solvencyBatch)),
		func(i int64) int { return b.solvencyBatch[i].Size() })
	batchSizeErrata := takeWithinLimits(int64(len(b.errataTxBatch)),
		func(i int64) int { return b.errataTxBatch[i].Size() })
	batchSizePriceFeed := takeWithinLimits(int64(len(b.priceFeedBatch)),
		func(i int64) int { return b.priceFeedBatch[i].Size() })

	// Populate the batch
	batch.AttestTxs = append(batch.AttestTxs[:0], b.observedTxBatch[:batchSizeTx]...)
	batch.AttestNetworkFees = append(batch.AttestNetworkFees[:0], b.networkFeeBatch[:batchSizeNF]...)
	batch.AttestSolvencies = append(batch.AttestSolvencies[:0], b.solvencyBatch[:batchSizeSolvency]...)
	batch.AttestErrataTxs = append(batch.AttestErrataTxs[:0], b.errataTxBatch[:batchSizeErrata]...)
	batch.AttestPriceFeeds = append(batch.AttestPriceFeeds[:0], b.priceFeedBatch[:batchSizePriceFeed]...)

	txCount := len(batch.AttestTxs)
	nfCount := len(batch.AttestNetworkFees)
	solvencyCount := len(batch.AttestSolvencies)
	errataCount := len(batch.AttestErrataTxs)
	priceFeedCount := len(batch.AttestPriceFeeds)

	b.observedTxBatch = b.observedTxBatch[batchSizeTx:]
	b.networkFeeBatch = b.networkFeeBatch[batchSizeNF:]
	b.solvencyBatch = b.solvencyBatch[batchSizeSolvency:]
	b.errataTxBatch = b.errataTxBatch[batchSizeErrata:]
	b.priceFeedBatch = b.priceFeedBatch[batchSizePriceFeed:]

	b.mu.Unlock()

	// Serialization finishes before returning the batch to the pool. Peer
	// sends own the serialized bytes and do not delay the next batch or cleanup.
	b.broadcastToAllPeers(ctx, *batch)

	// Return the batch to the pool after clearing it
	batch.AttestTxs = batch.AttestTxs[:0]
	batch.AttestNetworkFees = batch.AttestNetworkFees[:0]
	batch.AttestSolvencies = batch.AttestSolvencies[:0]
	batch.AttestErrataTxs = batch.AttestErrataTxs[:0]
	batch.AttestPriceFeeds = batch.AttestPriceFeeds[:0]
	b.batchPool.Put(batch)

	batchDuration := time.Since(start)
	b.metrics.MessagesBatched.WithLabelValues("observed_tx").Add(float64(txCount))
	b.metrics.MessagesBatched.WithLabelValues("network_fee").Add(float64(nfCount))
	b.metrics.MessagesBatched.WithLabelValues("solvency").Add(float64(solvencyCount))
	b.metrics.MessagesBatched.WithLabelValues("errata_tx").Add(float64(errataCount))
	b.metrics.MessagesBatched.WithLabelValues("price_feed").Add(float64(priceFeedCount))
	b.metrics.BatchSendTime.Observe(batchDuration.Seconds())
	b.metrics.BatchSends.Inc()

	if clear {
		// Log batch clear
		b.logger.Debug().Msg("attestation batches cleared")

		// Update metrics
		b.metrics.BatchClears.Inc()

		return
	}
	b.mu.Lock()
	b.lastBatchSent = time.Now()
	b.mu.Unlock()
	// Items were left behind (count cap or byte budget); schedule a follow-up
	// flush rather than waiting for the next interval.
	b.triggerBatchSend()
}

func containsXMRObservedTx(txs []*common.AttestTx) bool {
	for _, tx := range txs {
		if tx != nil && tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
			return true
		}
	}
	return false
}

// removeObservedTxsForErrata runs under the cleanup writer for XMR. Clear the
// unused tail as well so removed payloads are not retained by the queue array.
func (b *AttestationBatcher) removeObservedTxsForErrata(errata common.ErrataTx, orphanHeight int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.observedTxBatch[:0]
	for _, tx := range b.observedTxBatch {
		if tx == nil || !tx.ObsTx.Tx.Chain.Equals(errata.Chain) || !tx.ObsTx.Tx.ID.Equals(errata.Id) {
			kept = append(kept, tx)
			continue
		}
		if errata.Chain.UsesHeightScopedErrata() && orphanHeight > 0 {
			minedHeight, err := tx.ObsTx.CanonicalMinedHeight()
			if err == nil && minedHeight != orphanHeight {
				kept = append(kept, tx)
			}
		}
	}
	clear(b.observedTxBatch[len(kept):])
	b.observedTxBatch = kept
}

// AddObservedTx adds an observed transaction attestation to the batch
func (b *AttestationBatcher) AddObservedTx(tx common.AttestTx) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	// The caller has validated the attestation. Cleanup can run between that
	// validation and enqueue, so check durable admission under the same mutex
	// as queue purge. Never acquire the gossip map or barrier from this lock.
	if tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
		admitted, err := b.observerStorage.admitXMRObservation(tx.ObsTx)
		if err != nil {
			b.logger.Error().Err(err).Msg("reject xmr enqueue: errata storage read failed")
		}
		if !admitted {
			return
		}
	}
	b.observedTxBatch = append(b.observedTxBatch, &tx)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.observedTxBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.observedTxBatch)).
			Msg("observed tx batch reached max size, triggering immediate send")

		// The buffered notification is nonblocking and needs no extra worker.
		b.triggerBatchSend()
	}
}

// AddNetworkFee adds a network fee attestation to the batch
func (b *AttestationBatcher) AddNetworkFee(fee common.AttestNetworkFee) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.networkFeeBatch = append(b.networkFeeBatch, &fee)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.networkFeeBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.networkFeeBatch)).
			Msg("network fee batch reached max size, triggering immediate send")

		b.triggerBatchSend()
	}
}

// AddSolvency adds a solvency attestation to the batch
func (b *AttestationBatcher) AddSolvency(solvency common.AttestSolvency) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.solvencyBatch = append(b.solvencyBatch, &solvency)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.solvencyBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.solvencyBatch)).
			Msg("solvency batch reached max size, triggering immediate send")

		b.triggerBatchSend()
	}
}

// AddErrataTx adds an errata transaction attestation to the batch
func (b *AttestationBatcher) AddErrataTx(errata common.AttestErrataTx) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, queued := range b.errataTxBatch {
		if queued == nil || queued.ErrataTx == nil || errata.ErrataTx == nil ||
			queued.Attestation == nil || errata.Attestation == nil {
			continue
		}
		if queued.ErrataTx.Equals(errata.ErrataTx) &&
			bytes.Equal(queued.Attestation.PubKey, errata.Attestation.PubKey) {
			// Durable outbox retries may call AttestErrata every second while
			// waiting for the committed-quorum event. Keep at most one copy of
			// this node's identical vote in the current batch.
			return
		}
	}

	b.errataTxBatch = append(b.errataTxBatch, &errata)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.errataTxBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.errataTxBatch)).
			Msg("errata tx batch reached max size, triggering immediate send")

		b.triggerBatchSend()
	}
}

// AddPriceFeed adds an price feed attestation to the batch
func (b *AttestationBatcher) AddPriceFeed(priceFeed common.AttestPriceFeed) {
	maxBatchSize := b.getMaxBatchSize()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.priceFeedBatch = append(b.priceFeedBatch, &priceFeed)

	// If we've reached the maximum batch size, trigger an immediate send
	if int64(len(b.priceFeedBatch)) >= maxBatchSize {
		b.logger.Debug().
			Int("batch_size", len(b.priceFeedBatch)).
			Msg("price feed batch reached max size, triggering immediate send")

		b.triggerBatchSend()
	}
}

// triggerBatchSend triggers an immediate batch send outside the regular interval
func (b *AttestationBatcher) triggerBatchSend() {
	select {
	case b.forceSendChan <- struct{}{}:
		// Successfully triggered a send
	default:
		// Channel is full, a send is already pending
	}
}

// A batch containing an XMR observed-transaction attestation retains one absolute
// deadline for every peer attempt, including capacity waits. Other batches use
// separate per-peer timeouts. Each worker owns its cancellation; no completion
// worker or cleanup lock is needed for transport delivery.
func (b *AttestationBatcher) broadcastToAllPeers(ctx context.Context, batch common.AttestationBatch) {
	var deadline time.Time
	if containsXMRObservedTx(batch.AttestTxs) {
		deadline = time.Now().Add(xmrAttestationTimeout(b.peerTimeout))
	}
	// Marshal the batch
	payload, err := batch.Marshal()
	if err != nil {
		b.logger.Error().Err(err).Msg("failed to marshal attestation batch")
		return
	}

	peers := b.host.Peerstore().Peers()

	if b.getActiveValidators == nil {
		b.logger.Warn().Msg("active validator getter not set – skipping broadcast")
		return
	}
	// Skip self, send to all other active vals that we are peered with
	activeVals := b.getActiveValidators()
	var peersToSend []peer.ID
	for _, peerID := range peers {
		if peerID == b.host.ID() {
			continue
		}
		if _, ok := activeVals[peerID]; !ok {
			continue
		}
		peersToSend = append(peersToSend, peerID)
	}

	if len(peersToSend) == 0 {
		b.logger.Debug().Msg("no peers to broadcast to")
		return
	}

	b.logger.Debug().
		Int("peer_count", len(peersToSend)).
		Int("payload_bytes", len(payload)).
		Msg("broadcasting attestation batch to peers")

	for _, p := range peersToSend {
		go func(remote peer.ID) {
			peerCtx := ctx
			if !deadline.IsZero() {
				var cancel context.CancelFunc
				peerCtx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
			}
			if b.broadcastToPeerFn != nil {
				b.broadcastToPeerFn(peerCtx, remote, payload)
			} else {
				b.broadcastToPeer(peerCtx, remote, payload)
			}
		}(p)
	}

	b.logger.Debug().Msg("broadcast initiated to all peers")
}

func (b *AttestationBatcher) broadcastToPeer(ctx context.Context, peer peer.ID, payload []byte) {
	// Skip a peer whose send circuit breaker is open (repeatedly unresponsive):
	// don't even acquire a token, so one black-hole peer can't keep the per-peer
	// semaphore saturated for everyone else.
	if b.peerMgr.shouldSkipPeer(peer) {
		b.logger.Debug().Msgf("skipping peer in send-breaker cooldown: %s", peer)
		return
	}
	// shouldSkipPeer may have admitted a half-open trial (set trialInFlight).
	// Guarantee that flag is cleared on every exit path — including the ones
	// below that intentionally do not record a result (acquire timeout, parent
	// context expiry) — so a trial can never get stuck and blacklist the peer.
	defer b.peerMgr.finishTrial(peer)

	// Limit the number of concurrent sends to this peer
	sem, err := b.peerMgr.acquireContext(ctx, peer)
	if err != nil {
		// Backpressure, not a peer-health signal: the tokens are tied up locally
		// (a legitimate send burst can exhaust them in <50ms). Do NOT count it
		// toward the breaker, or a healthy peer would false-trip. Debug, not error.
		b.logger.Debug().Err(err).Msgf("skipping send, peer semaphore busy: %s", peer)
		return
	}

	defer b.peerMgr.release(sem)

	b.logger.Debug().Msgf("starting attestation send to peer: %s", peer)

	// Create a context with timeout for this specific peer
	timeout := b.peerTimeout
	if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	peerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stream, err := b.host.NewStream(peerCtx, peer, batchedAttestationProtocol)
	if err != nil {
		// Only a genuine per-peer failure (parent context still alive) counts;
		// a shutdown/global-deadline cancel is not the peer's fault.
		if ctx.Err() == nil {
			b.peerMgr.recordSendResult(peer, false)
		}
		b.logger.Error().Err(err).Msgf("fail to create stream to peer: %s", peer)
		return
	}

	ok := b.sendPayloadToStream(peerCtx, stream, payload)
	// A failure only indicts the peer when our side wasn't cancelled.
	if ok || ctx.Err() == nil {
		b.peerMgr.recordSendResult(peer, ok)
	}

	b.logger.Debug().Msgf("completed attestation send to peer: %s", peer)
}

// sendPayloadToStream writes payload and waits for the peer's ACK. It returns
// true only on a clean, acknowledged send. The ACK read uses a short, dedicated
// deadline (attestationAckTimeout) rather than the full peer timeout, so a peer
// that accepts the stream but never replies releases the send token quickly.
func (b *AttestationBatcher) sendPayloadToStream(ctx context.Context, stream network.Stream, payload []byte) bool {
	peer := stream.Conn().RemotePeer()
	logger := b.logger.With().Str("remote_peer", peer.String()).Logger()

	defer closeStream(logger, stream)

	if err := p2p.WriteStreamWithBufferWithContext(ctx, payload, stream); err != nil {
		b.logger.Error().Err(err).Msgf("fail to write payload to peer: %s", peer)
		return false
	}

	// Wait for acknowledgment under a short deadline: the write has already
	// landed in the peer's buffer, so a missing ACK means the peer is not
	// processing. Bound it well under the peer timeout to free the token fast.
	ackTimeout := b.ackTimeout
	if ackTimeout <= 0 {
		ackTimeout = attestationAckTimeout
	}
	if b.peerTimeout > 0 && ackTimeout > b.peerTimeout {
		ackTimeout = b.peerTimeout
	}
	ackCtx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()

	reply, err := p2p.ReadStreamWithBufferWithContext(ackCtx, stream)
	if err != nil {
		b.logger.Error().Err(err).Msgf("fail to read reply from peer: %s", peer)
		return false
	}

	if string(reply) != p2p.StreamMsgDone {
		b.logger.Error().Msgf("unexpected reply from peer: %s", peer)
		return false
	}

	b.logger.Debug().Msgf("attestation sent to peer: %s", peer)
	return true
}
