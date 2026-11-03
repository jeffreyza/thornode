package observer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

const (
	// validators can get credit for observing a tx for up to this amount of time after it is committed, after which it count against a slash penalty.
	defaultLateObserveTimeout = 2 * time.Minute

	// Prune observed txs after this amount of time, even if they are not yet committed.
	// Gives some time for longer chain halts.
	// If chain halts for longer than this, validators will need to restart their bifrosts to re-share their attestations.
	defaultNonQuorumTimeout = 10 * time.Hour

	// minTimeBetweenAttestations is the minimum time between sending batches of attestations for a quorum tx to thornode.
	defaultMinTimeBetweenAttestations = 30 * time.Second

	// how often to prune old observed txs and check if late attestations should be sent.
	// should be less than lateObserveTimeout and minTimeBetweenAttestations by at least a factor of 2.
	defaultObserveReconcileInterval = 15 * time.Second

	// defaultAskPeers is the number of random peers to ask for their attestation state on startup.
	defaultAskPeers = 10

	// defaultAskPeersDelay is the delay before asking peers for their attestation state on startup.
	defaultAskPeersDelay = 5 * time.Second

	defaultPeerTimeout = 20 * time.Second

	// attestationAckTimeout bounds only the wait for a peer's ACK after the batch
	// is already written. Kept well under defaultPeerTimeout so a peer that
	// accepts the stream but never replies frees its send token quickly instead
	// of tying it up for the full peer timeout.
	attestationAckTimeout = 5 * time.Second

	defaultPeerConcurrentSends    = 4
	defaultPeerConcurrentReceives = 5
	defaultMaxBatchSize           = 100
	defaultBatchInterval          = 2 * time.Second

	streamAckBegin  = "ack_begin"
	streamAckHeader = "ack_header"
	streamAckData   = "ack_data"
)

var (
	attestationStateProtocol       protocol.ID = "/p2p/attestation-state/v2"
	batchedAttestationProtocol     protocol.ID = "/p2p/batched-attestations"
	xmrSpentRefCertificateProtocol protocol.ID = "/p2p/xmr-spent-ref-certificate/v2"

	// AttestationState protocol prefixes
	prefixSendState = []byte{0x01} // request

	prefixBatchBegin  = []byte{0x02} // start of a batch
	prefixBatchHeader = []byte{0x03} // header of a batch
	prefixBatchData   = []byte{0x04} // data of a batch
	prefixBatchEnd    = []byte{0x05} // end of a batch

	// maxAttestationBatchBytes bounds the serialized size of any single
	// attestation frame written to a p2p stream (state-sync batches and live
	// gossip broadcasts). Batches are packed by bytes rather than item count,
	// so honest frames stay under the wire-level p2p.MaxPayload read cap
	// regardless of validator-set size or mimir batch settings. Var rather
	// than const so tests can lower it to force splitting.
	maxAttestationBatchBytes = p2p.MaxPayload / 2

	// maxAllowedBatchSize caps mimir-driven MaxBatchSize growth. Frame size
	// is bound by maxAttestationBatchBytes, so larger item counts only add
	// per-flush latency and pool memory.
	maxAllowedBatchSize int64 = 1000
)

// quorumStateBatcher greedily packs items into QuorumState batches whose
// serialized size stays under maxAttestationBatchBytes.
type quorumStateBatcher struct {
	batches []common.QuorumState
	current common.QuorumState
	size    int
}

// protoItemOverhead over-estimates the per-item proto framing cost inside a
// QuorumState: one byte for the repeated-field tag plus up to four bytes for
// the length varint.
const protoItemOverhead = 5

func (q *quorumStateBatcher) add(itemSize int, appendItem func(*common.QuorumState)) {
	sz := itemSize + protoItemOverhead
	// Flush before adding if this item would push the current batch over the
	// budget. A single item larger than the budget still gets its own batch:
	// dropping it silently would be worse, and honest items never approach
	// the budget.
	if q.size > 0 && q.size+sz > maxAttestationBatchBytes {
		q.flush()
	}
	appendItem(&q.current)
	q.size += sz
}

func (q *quorumStateBatcher) flush() {
	if q.size > 0 {
		q.batches = append(q.batches, q.current)
		q.current = common.QuorumState{}
		q.size = 0
	}
}

// txKey contains the properties that are required to uniquely identify an observed tx
type txKey struct {
	Chain                  common.Chain
	ID                     common.TxID
	UniqueHash             string
	AllowFutureObservation bool
	Finalized              bool
	Inbound                bool
}

func observedTxKey(obsTx common.ObservedTx, inbound, allowFutureObservation bool) txKey {
	return txKey{
		Chain:                  obsTx.Tx.Chain,
		ID:                     obsTx.Tx.ID,
		UniqueHash:             obsTx.AttestationUniqueHash(),
		AllowFutureObservation: allowFutureObservation,
		Finalized:              obsTx.IsFinal(),
		Inbound:                inbound,
	}
}

// attestableObservedTx binds an ObservedTx to its inbound/outbound classification
// so GetSignablePayload produces the same bytes the observer signed (which include
// the inbound bit). Without this wrapper, AttestationState could not verify
// observer signatures because *common.ObservedTx alone doesn't carry the bit.
type attestableObservedTx struct {
	*common.ObservedTx
	inbound bool
}

func (a *attestableObservedTx) GetSignablePayload() ([]byte, error) {
	return a.ObservedTx.GetSignablePayloadWithInbound(a.inbound)
}

func (a *attestableObservedTx) IsValid() bool {
	return a != nil && a.ObservedTx != nil
}

type KeysInterface interface {
	GetPrivateKey() (*secp256k1.PrivKey, error)
}

type EventClientInterface interface {
	Start()
	Stop()
	RegisterHandler(eventType string, handler func(*ebifrost.EventNotification))
}

// AttestationGossip handles observed tx attestations to/from other nodes
type AttestationGossip struct {
	logger zerolog.Logger
	host   host.Host

	grpcClient  ebifrost.LocalhostBifrostClient
	eventClient EventClientInterface
	bridge      thorclient.ThorchainBridge

	privKey cryptotypes.PrivKey // our private key, cached for performance
	pubKey  []byte              // our public key, cached for performance

	config config.BifrostAttestationGossipConfig

	// Generic maps for different attestation types
	observedTxs map[txKey]*AttestationState[*attestableObservedTx]
	networkFees map[common.NetworkFee]*AttestationState[*common.NetworkFee]
	solvencies  map[common.TxID]*AttestationState[*common.Solvency]
	errataTxs   map[common.ErrataTx]*AttestationState[*common.ErrataTx]
	priceFeeds  map[string]*AttestationState[*common.PriceFeed]

	solvencyMetrics *solvencyAttestationMetrics
	// authorizedXMRSpentRefs remembers the source-vault-certified, pre-chain
	// ref set admitted for an XMR outbound so later certified disagreements do
	// not race a conflicting sidecar import. Entries track the import outcome:
	// a set whose import failed terminally may be replaced by a differing
	// certified set, and entries expire so a failed certificate cannot pin a
	// txid for the life of the process.
	// This mutex protects only reservation bookkeeping. Never hold it while
	// acquiring another lock, logging or making RPCs: cancellation cleanup must
	// not wait for the global observer mutex or external work.
	authorizedXMRSpentRefsMu sync.Mutex
	authorizedXMRSpentRefs   map[xmrSpentRefsConflictKey]*xmrSpentRefsConflictEntry
	// historicalXMRSpentRefCertificates is deliberately separate from
	// observedTxs. Retiring-vault members that have churned out may contribute a
	// source-vault certificate share, but that share must never count toward the
	// active-validator observation quorum sent to Thornode.
	historicalXMRSpentRefCertificates        map[[32]byte]*historicalXMRSpentRefCertificateBucket
	historicalXMRCertificateSignerUse        map[string]int
	historicalXMRCertificateSendSem          chan struct{}
	historicalXMRCertificateRPC              *historicalXMRCertificateRPCControl
	historicalXMRCertificateReconcileRunning bool
	pendingXMRSpentRefsReconcileRunning      bool
	mu                                       sync.Mutex

	// Lock order: XMR errata barrier -> observer deck / gossip map -> state
	// or batch mutex. Readers protect local admission and THORNode submission;
	// writers protect durable cleanup and its volatile purge. Never nest readers.
	// Peer sends and state export hold no reader. Queue admission checks storage
	// under the batch mutex. Shared locks can wait behind unrelated RPCs, and
	// requests already handed to THORNode may complete remotely after cleanup.
	xmrErrataBarrier sync.RWMutex
	observerStorage  *ObserverStorage

	observedTxsPool *AttestationStatePool[*attestableObservedTx]
	networkFeesPool *AttestationStatePool[*common.NetworkFee]
	solvenciesPool  *AttestationStatePool[*common.Solvency]
	errataTxsPool   *AttestationStatePool[*common.ErrataTx]
	priceFeedsPool  *AttestationStatePool[*common.PriceFeed]

	priceFeedsDelay *Delay

	activeVals map[peer.ID]bool // active val peer IDs
	avMu       sync.Mutex

	certificateObserver               *Observer
	observerHandleObservedTxCommitted func(tx common.ObservedTx, allowFutureObservation bool)
	observerHandleErrataTxCommitted   func(errata common.ErrataTx, outcome common.ErrataTxOutcome) error
	observerApplySpentOutputRefs      func(request spentOutputRefsImport, onOutcome func(applied bool))
	observerApplyCommittedSpentRefs   func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool)
	applySpentRefsSem                 chan struct{}
	applyCommittedSpentRefsSem        chan struct{}
	runCtxMu                          sync.RWMutex
	runCtx                            context.Context

	batcher *AttestationBatcher

	// peerManager is used to limit the number of concurrent receives from a peer
	peerMgr *peerManager

	// Export admission is separate from ordinary gossip concurrency. Own one
	// copied snapshot per peer, with a fixed total before any snapshot work.
	stateExportMu sync.Mutex
	stateExports  map[peer.ID]struct{}

	startupStateMu      sync.Mutex
	startupState        *attestationStateCollection
	startupStatePending time.Time
	startupStateStopped bool

	stateInitPeers   map[peer.ID]bool // peers that we have asked for their attestation state
	stateInitContext context.Context
	stateInitWorkers *sync.WaitGroup
	stateInitMu      sync.Mutex
}

// NewAttestationGossip create a new instance of AttestationGossip
func NewAttestationGossip(
	host host.Host,
	keys *thorclient.Keys,
	thornodeBifrostGRPCAddress string,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	config config.BifrostAttestationGossipConfig,
) (*AttestationGossip, error) {
	cc, err := grpc.NewClient(thornodeBifrostGRPCAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	normalizeConfig(&config)

	grpcClient := ebifrost.NewLocalhostBifrostClient(cc)
	eventClient := NewEventClient(grpcClient)

	pk, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	batcher := NewAttestationBatcher(
		host,
		log.With().Str("module", "attestation_batcher").Logger(),
		m,
		config.BatchInterval,
		config.MaxBatchSize,
		config.PeerTimeout,
		config.PeerConcurrentSends,
	)

	logger := log.With().Str("module", "attestation_gossip").Logger()

	s := &AttestationGossip{
		logger:      logger,
		host:        host,
		privKey:     pk,
		pubKey:      pk.PubKey().Bytes(),
		grpcClient:  grpcClient,
		config:      config,
		bridge:      bridge,
		eventClient: eventClient,

		// Initialize generic maps
		observedTxs:                       make(map[txKey]*AttestationState[*attestableObservedTx]),
		networkFees:                       make(map[common.NetworkFee]*AttestationState[*common.NetworkFee]),
		solvencies:                        make(map[common.TxID]*AttestationState[*common.Solvency]),
		errataTxs:                         make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
		priceFeeds:                        make(map[string]*AttestationState[*common.PriceFeed]),
		solvencyMetrics:                   newSolvencyAttestationMetrics(m),
		authorizedXMRSpentRefs:            make(map[xmrSpentRefsConflictKey]*xmrSpentRefsConflictEntry),
		historicalXMRSpentRefCertificates: make(map[[32]byte]*historicalXMRSpentRefCertificateBucket),
		historicalXMRCertificateSignerUse: make(map[string]int),
		historicalXMRCertificateSendSem:   make(chan struct{}, historicalXMRCertificateSendConcurrency),

		peerMgr: newPeerManager(logger, config.PeerConcurrentReceives),

		observedTxsPool: NewAttestationStatePool[*attestableObservedTx](),
		networkFeesPool: NewAttestationStatePool[*common.NetworkFee](),
		solvenciesPool:  NewAttestationStatePool[*common.Solvency](),
		errataTxsPool:   NewAttestationStatePool[*common.ErrataTx](),
		priceFeedsPool:  NewAttestationStatePool[*common.PriceFeed](),

		priceFeedsDelay: NewDelay(),
		runCtx:          context.Background(),

		batcher: batcher,

		// Bound concurrent sidecar mark-spent calls. Committed catch-up has a
		// separate reserve so pre-quorum gossip cannot consume all capacity.
		applySpentRefsSem:          make(chan struct{}, 16),
		applyCommittedSpentRefsSem: make(chan struct{}, 4),
	}
	batcher.setActiveValGetter(s.getActiveValidators)
	// Register event handlers
	eventClient.RegisterHandler(ebifrost.EventQuorumTxCommitted, s.handleQuorumTxCommitted)
	eventClient.RegisterHandler(ebifrost.EventQuorumNetworkFeeCommitted, s.handleQuorumNetworkFeeCommitted)
	eventClient.RegisterHandler(ebifrost.EventQuorumSolvencyCommitted, s.handleQuorumSolvencyCommitted)
	eventClient.RegisterHandler(ebifrost.EventQuorumErrataTxCommitted, s.handleQuorumErrataTxCommitted)

	// Register stream handlers
	host.SetStreamHandler(attestationStateProtocol, s.handleStreamAttestationState)
	host.SetStreamHandler(batchedAttestationProtocol, s.handleStreamBatchedAttestations)
	host.SetStreamHandler(xmrSpentRefCertificateProtocol, s.handleStreamXMRSpentRefCertificate)

	return s, nil
}

// normalizeConfig ensures that the config has values for all fields.
func normalizeConfig(config *config.BifrostAttestationGossipConfig) {
	if config.ObserveReconcileInterval == 0 {
		config.ObserveReconcileInterval = defaultObserveReconcileInterval
	}
	if config.LateObserveTimeout == 0 {
		config.LateObserveTimeout = defaultLateObserveTimeout
	}
	if config.NonQuorumTimeout == 0 {
		config.NonQuorumTimeout = defaultNonQuorumTimeout
	}
	if config.MinTimeBetweenAttestations == 0 {
		config.MinTimeBetweenAttestations = defaultMinTimeBetweenAttestations
	}
	if config.AskPeers == 0 {
		config.AskPeers = defaultAskPeers
	}
	if config.AskPeersDelay == 0 {
		config.AskPeersDelay = defaultAskPeersDelay
	}
	if config.PeerTimeout == 0 {
		config.PeerTimeout = defaultPeerTimeout
	}
	if config.MaxBatchSize == 0 {
		config.MaxBatchSize = defaultMaxBatchSize
	}
	if config.PeerConcurrentSends == 0 {
		config.PeerConcurrentSends = defaultPeerConcurrentSends
	}
	if config.PeerConcurrentReceives == 0 {
		config.PeerConcurrentReceives = defaultPeerConcurrentReceives
	}
	if config.PeerConcurrentReceives < config.PeerConcurrentSends {
		// ensure that the number of concurrent receives is at least as large as the number of concurrent sends
		config.PeerConcurrentReceives = config.PeerConcurrentSends
	}
	if config.BatchInterval == 0 {
		config.BatchInterval = defaultBatchInterval
	}
}

// Set the active validators list
func (s *AttestationGossip) setActiveValidators(activeVals common.PubKeys) {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	activePeers := make(map[peer.ID]bool, len(activeVals))
	for _, pub := range activeVals {
		pk, err := pub.Decode()
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to convert bech32 pubkey to raw secp256k1 pubkey")
			continue
		}
		peerID, err := conversion.GetPeerIDFromSecp256PubKey(pk.Bytes())
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to convert secp256k1 pubkey to peer ID")
			continue
		}
		activePeers[peerID] = true
	}

	s.activeVals = activePeers
}

// Get the number of active validators
func (s *AttestationGossip) activeValidatorCount() int {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	return len(s.activeVals)
}

// Check if a public key belongs to an active validator
func (s *AttestationGossip) isActiveValidator(p peer.ID) bool {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	_, ok := s.activeVals[p]
	return ok
}

func (s *AttestationGossip) getActiveValidators() map[peer.ID]bool {
	s.avMu.Lock()
	defer s.avMu.Unlock()
	return s.activeVals
}

func (s *AttestationGossip) SetObserverHandleObservedTxCommitted(o *Observer) {
	s.observerHandleObservedTxCommitted = o.handleObservedTxCommitted
}

func (s *AttestationGossip) SetObserverHandleErrataTxCommitted(o *Observer) {
	s.observerHandleErrataTxCommitted = o.handleErrataTxCommitted
}

// SetObserverStorage wires durable admission before observer startup. XMR
// admission fails closed until storage is ready, including in peer handlers.
func (s *AttestationGossip) SetObserverStorage(storage *ObserverStorage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observerStorage = storage
	if s.batcher != nil {
		s.batcher.mu.Lock()
		s.batcher.observerStorage = storage
		s.batcher.mu.Unlock()
	}
}

type spentOutputRefsImport = chainclienttypes.SpentOutputRefsImport

// SetObserverApplySpentOutputRefs wires the XMR sidecar import. The import
// reports its final outcome through onOutcome exactly once, which may happen
// after the call returns when the client retries transient failures.
func (s *AttestationGossip) SetObserverApplySpentOutputRefs(fn func(request spentOutputRefsImport, onOutcome func(applied bool))) {
	s.observerApplySpentOutputRefs = fn
}

// acquireXMRSpentRefsAttempt orders one mutation against durable errata
// admission. Retry waits and metadata queries run outside this barrier.
func (s *AttestationGossip) acquireXMRSpentRefsAttempt(tx common.ObservedTx) (func(), bool) {
	if !tx.Tx.Chain.Equals(common.XMRChain) {
		return func() {}, true
	}
	s.xmrErrataBarrier.RLock()
	s.mu.Lock()
	admitted, err := s.admitXMRObservationLocked(tx)
	s.mu.Unlock()
	if err != nil || !admitted {
		s.xmrErrataBarrier.RUnlock()
		return nil, false
	}
	return s.xmrErrataBarrier.RUnlock, true
}

// spentRefsOutcome rechecks admission because cleanup may finish between the
// client's last durable write and Observer's successful outcome bookkeeping.
// Successful XMR outcomes call onOutcome while holding the errata barrier's
// read lock. Callbacks must not reacquire the barrier as a reader or writer,
// enter errata cleanup, or invert the barrier-before-bookkeeping lock order.
func (s *AttestationGossip) spentRefsOutcome(tx common.ObservedTx, onOutcome func(bool)) func(bool) {
	return func(applied bool) {
		if onOutcome == nil {
			return
		}
		if applied {
			release, admitted := s.acquireXMRSpentRefsAttempt(tx)
			if !admitted {
				onOutcome(false)
				return
			}
			defer release()
		}
		onOutcome(applied)
	}
}

// SetObserverApplyCommittedSpentRefs wires the consensus-backed import used by
// live quorum notifications. The notification supplies only a vault and tx ID;
// the client must return the canonical transaction it reconstructed and applied.
func (s *AttestationGossip) SetObserverApplyCommittedSpentRefs(
	fn func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool),
) {
	s.observerApplyCommittedSpentRefs = fn
}

// applySpentRefsAsync keeps pre-quorum sidecar imports off the attestation hot
// path while preserving the final outcome callback through the concurrency
// limiter. Capacity is acquired before spawning so an attacker cannot turn a
// full limiter into an unbounded population of blocked goroutines. Saturated
// work is rejected immediately; reconciliation can retry it later.
func (s *AttestationGossip) applySpentRefsAsync(request spentOutputRefsImport, onOutcome func(applied bool)) {
	request.ReplaySource = !request.ChainCommitted
	s.applySpentRefsAsyncWithLimit(request, onOutcome, s.applySpentRefsSem, "pre-quorum")
}

func (s *AttestationGossip) applySpentRefsAsyncWithLimit(
	request spentOutputRefsImport,
	onOutcome func(applied bool),
	sem chan struct{},
	origin string,
) {
	if s.observerApplySpentOutputRefs == nil {
		if onOutcome != nil {
			onOutcome(false)
		}
		return
	}
	s.runSpentRefsAsyncWithLimit(request.Tx.Tx.ID, sem, origin, onOutcome, func() {
		s.observerApplySpentOutputRefs(request, s.spentRefsOutcome(request.Tx, onOutcome))
	})
}

// applyCommittedSpentRefsAsync retains the separate committed-work capacity
// while keeping event metadata out of the sidecar import request by type.
func (s *AttestationGossip) applyCommittedSpentRefsAsync(
	vaultPubKey common.PubKey,
	txID common.TxID,
	onOutcome func(common.ObservedTx, bool),
) {
	if s.observerApplyCommittedSpentRefs == nil {
		if onOutcome != nil {
			onOutcome(common.ObservedTx{}, false)
		}
		return
	}
	onRejected := func(bool) {
		if onOutcome != nil {
			onOutcome(common.ObservedTx{}, false)
		}
	}
	s.runSpentRefsAsyncWithLimit(txID, s.applyCommittedSpentRefsSem, "committed", onRejected, func() {
		imported, applied := s.observerApplyCommittedSpentRefs(s.lifecycleContext(), vaultPubKey, txID)
		if onOutcome != nil {
			s.spentRefsOutcome(imported, func(ok bool) { onOutcome(imported, ok) })(applied)
		}
	})
}

func (s *AttestationGossip) runSpentRefsAsyncWithLimit(
	txID common.TxID,
	sem chan struct{},
	origin string,
	onRejected func(bool),
	work func(),
) {
	if sem == nil {
		if onRejected != nil {
			onRejected(false)
		}
		return
	}
	select {
	case sem <- struct{}{}:
		// Capacity is owned before the goroutine exists.
	default:
		s.logger.Warn().Str("tx_id", txID.String()).Str("origin", origin).
			Msg("xmr spent-ref import capacity exhausted; reconciliation may retry")
		if onRejected != nil {
			onRejected(false)
		}
		return
	}
	go func() {
		defer func() { <-sem }()
		work()
	}()
}

func (s *AttestationGossip) lifecycleContext() context.Context {
	s.runCtxMu.RLock()
	defer s.runCtxMu.RUnlock()
	return s.runCtx
}

// Handle a committed quorum transaction event
func (s *AttestationGossip) handleQuorumTxCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum tx committed event")

	var qtx common.QuorumTx
	if err := qtx.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum tx")
		return
	}

	if s.observerHandleObservedTxCommitted != nil {
		// if our attestation is in the quorum tx, we can remove it from our observer deck.
		for _, att := range qtx.Attestations {
			if bytes.Equal(att.PubKey, s.pubKey) {
				// we have attested to this tx, and it has been committed to the chain.
				s.logger.Debug().Msg("our attestation is in the quorum tx, passing to observer to remove from ondeck")
				s.observerHandleObservedTxCommitted(qtx.ObsTx, qtx.AllowFutureObservation)
				break
			}
		}
	}

	if s.observerApplyCommittedSpentRefs != nil &&
		!qtx.Inbound &&
		qtx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
		// DeliverTx can emit before the HTTP query exposes the committed voter.
		// The client owns a short bounded retry; periodic catch-up remains the
		// durable fallback. Nothing except the hint's vault and tx ID crosses
		// this boundary until the client returns consensus-backed metadata.
		hintVault := qtx.ObsTx.ObservedPubKey
		hintTxID := qtx.ObsTx.Tx.ID
		if hintVault.IsEmpty() || hintTxID.IsEmpty() {
			s.logger.Warn().Msg("ignore committed xmr spent-ref hint without vault or transaction ID")
		} else {
			s.applyCommittedSpentRefsAsync(hintVault, hintTxID, func(imported common.ObservedTx, applied bool) {
				if !applied {
					return
				}
				if !imported.Tx.ID.Equals(hintTxID) ||
					!imported.Tx.Chain.Equals(common.XMRChain) ||
					!imported.ObservedPubKey.Equals(hintVault) {
					s.logger.Error().Str("tx_id", hintTxID.String()).
						Msg("committed xmr spent-ref importer returned mismatched consensus transaction")
					return
				}

				payload, err := imported.GetSignablePayloadWithInbound(false)
				if err != nil {
					s.logger.Error().Err(err).Str("tx_id", imported.Tx.ID.String()).
						Msg("cannot bind committed xmr spent refs to consensus observation payload")
					return
				}
				payloadHash := sha256.Sum256(payload)
				// Consensus-backed refs outrank pre-quorum conflict memory only after
				// the sidecar accepts the exact canonical transaction returned above.
				s.recordCommittedXMRSpentRefs(
					xmrSpentRefsConflictKeyFor(imported),
					xmrSpentRefsIdentity(imported.SpentOutputRefs),
				)
				s.markXMRSpentRefsAppliedByPayload(imported, payloadHash)
				s.markHistoricalXMRSpentRefCertificateApplied(payloadHash)
			})
		}
	}

	k := observedTxKey(qtx.ObsTx, qtx.Inbound, qtx.AllowFutureObservation)

	s.mu.Lock()
	as, ok := s.observedTxs[k]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qtx.Attestations)
}

// Handle a committed quorum network fee event
func (s *AttestationGossip) handleQuorumNetworkFeeCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum network fee committed event")

	var qnf common.QuorumNetworkFee
	if err := qnf.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum network fee")
		return
	}

	s.mu.Lock()
	as, ok := s.networkFees[*qnf.NetworkFee]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qnf.Attestations)
}

// Handle a committed quorum solvency event
func (s *AttestationGossip) handleQuorumSolvencyCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum solvency committed event")

	var qs common.QuorumSolvency
	if err := qs.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum solvency")
		return
	}

	s.mu.Lock()
	as, ok := s.solvencies[qs.Solvency.Id]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qs.Attestations)
}

// Handle a committed quorum errata tx event
func (s *AttestationGossip) handleQuorumErrataTxCommitted(en *ebifrost.EventNotification) {
	s.logger.Debug().Msg("handling quorum errata tx committed event")

	var qe common.QuorumErrataTx
	if err := qe.Unmarshal(en.Payload); err != nil {
		s.logger.Error().Err(err).Msg("fail to unmarshal quorum errata")
		return
	}
	if qe.ErrataTx == nil || qe.ErrataTx.Chain.IsEmpty() || qe.ErrataTx.Id.IsEmpty() {
		s.logger.Error().Msg("committed quorum errata is missing chain or transaction id")
		return
	}

	if qe.ErrataTx.Chain.RequiresTerminalErrataOutcome() {
		switch qe.Outcome {
		case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED,
			common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION:
		default:
			// An accepted batch can still lack current-active-set quorum.
			// Keep its outbox and every attestation retryable; unknown outcomes
			// also fail closed instead of silently discarding evidence.
			return
		}
	}

	// A committed batch can contain only another node's vote. Clear this
	// observer's durable outbox only when Thornode confirms that this node's
	// exact attestation was included in the delivered message.
	localAttestationCommitted := false
	for _, attestation := range qe.Attestations {
		if attestation != nil && bytes.Equal(attestation.PubKey, s.pubKey) {
			localAttestationCommitted = true
			break
		}
	}
	if localAttestationCommitted && s.observerHandleErrataTxCommitted != nil {
		if err := s.observerHandleErrataTxCommitted(*qe.ErrataTx, qe.Outcome); err != nil {
			s.logger.Error().Err(err).
				Str("chain", qe.ErrataTx.Chain.String()).
				Str("txid", qe.ErrataTx.Id.String()).
				Msg("fail to durably apply committed errata; retain retryable attestations")
			return
		}
	}

	s.mu.Lock()
	as, ok := s.errataTxs[*qe.ErrataTx]
	if ok {
		// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
		// This order must be maintained everywhere to avoid deadlock.
		as.mu.Lock()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	defer as.mu.Unlock()
	as.MarkAttestationsCommitted(qe.Attestations)
}

// Snapshot keys, not pooled state pointers: cleanup can run between attempts.
// No global map mutex is held across an observation RPC, and a queued XMR
// cleanup writer gets a turn before the next XMR key is read.
func (s *AttestationGossip) reconcileObservedTxs(ctx context.Context) {
	s.mu.Lock()
	keys := make([]txKey, 0, len(s.observedTxs))
	for k := range s.observedTxs {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		s.reconcileObservedTx(ctx, k)
	}
}

func (s *AttestationGossip) reconcileObservedTx(ctx context.Context, k txKey) {
	if k.Chain.Equals(common.XMRChain) {
		s.xmrErrataBarrier.RLock()
		defer s.xmrErrataBarrier.RUnlock()
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, xmrAttestationTimeout(s.config.PeerTimeout))
		defer cancel()
	}
	s.mu.Lock()
	state, ok := s.observedTxs[k]
	if !ok {
		s.mu.Unlock()
		return
	}
	state.mu.Lock()
	if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
		delete(s.observedTxs, k)
		// Every production pool get holds mu, so the state cannot be reused
		// until its old mutex has been unlocked.
		s.observedTxsPool.PutAttestationState(state)
		state.mu.Unlock()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	defer state.mu.Unlock()
	if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
		s.sendObservedTxAttestationsToThornode(ctx, *state.Item.ObservedTx, state, k.Inbound, k.AllowFutureObservation, false)
	}
}

// Start the attestation gossip service
func (s *AttestationGossip) Start(ctx context.Context) {
	s.runCtxMu.Lock()
	s.runCtx = ctx
	s.runCtxMu.Unlock()

	ticker := time.NewTicker(s.config.ObserveReconcileInterval)

	startupDelay := s.config.AskPeersDelay
	delayTimer := time.NewTimer(startupDelay)
	semPruneTicker := time.NewTicker(semaphorePruneInterval)

	defer func() {
		ticker.Stop()
		delayTimer.Stop()
		semPruneTicker.Stop()
	}()

	s.reconcileMimirConfigs()

	go s.batcher.Start(ctx)

	for {
		select {
		case <-ticker.C:
			// prune old attestations and check for late ones to send
			now := time.Now()
			s.reconcileObservedTxs(ctx)
			s.mu.Lock()

			// Prune network fees
			for k, state := range s.networkFees {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					delete(s.networkFees, k)
					s.networkFeesPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late network fee attestations")
					s.sendNetworkFeeAttestationsToThornode(ctx, *state.Item, state, false)
				}
				state.mu.Unlock()
			}

			// Prune solvencies
			for k, state := range s.solvencies {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					if state.AttestationCount() > 0 && shouldTrackSolvencyMetrics(state.Item) {
						s.solvencyMetrics.prunePayload(state.Item, requiredSolvencyAttestations(s.activeValidatorCount()))
					}
					delete(s.solvencies, k)
					s.solvenciesPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late solvency attestations")
					s.sendSolvencyAttestationsToThornode(ctx, *state.Item, state, false)
				}
				state.mu.Unlock()
			}

			// Prune errata transactions
			for k, state := range s.errataTxs {
				state.mu.Lock()
				if state.ExpiredAfterQuorum(s.config.LateObserveTimeout, s.config.NonQuorumTimeout) {
					delete(s.errataTxs, k)
					s.errataTxsPool.PutAttestationState(state)
				} else if state.ShouldSendLate(s.config.MinTimeBetweenAttestations) {
					s.logger.Debug().Msg("sending late errata attestations")
					s.sendErrataAttestationsToThornode(ctx, *state.Item, state, false)
				}
				state.mu.Unlock()
			}

			// Prune price feeds
			s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])

			// Prune XMR spent-ref conflict memory
			s.pruneAuthorizedXMRSpentRefs(now)
			s.pruneHistoricalXMRSpentRefCertificatesLocked(now)

			s.mu.Unlock()
			s.triggerPendingObservedXMRSpentRefsReconcile(ctx)
			s.triggerHistoricalXMRSpentRefCertificateReconcile(ctx)
			s.reconcileMimirConfigs()

		case <-delayTimer.C:
			s.eventClient.Start()

		case <-semPruneTicker.C:
			// Periodically prune semaphores that have been idle for a while
			s.peerMgr.prune()
			s.batcher.peerMgr.prune()
		case <-ctx.Done():
			s.eventClient.Stop()
			return
		}
	}
}

func (s *AttestationGossip) reconcileMimirConfigs() {
	maxBatchSize, err := s.bridge.GetMimir(constants.MimirMaxBatchSize)
	if err != nil {
		s.logger.Error().Str("mimir", constants.MimirMaxBatchSize).Err(err).Msg("fail to get mimir value")
	} else if maxBatchSize > 0 {
		if maxBatchSize != s.batcher.getMaxBatchSize() {
			s.batcher.updateMaxBatchSize(maxBatchSize)
		}
	}

	peerConcurrentSends, err := s.bridge.GetMimir(constants.MimirPeerConcurrentSends)
	if err != nil {
		s.logger.Error().Str("mimir", constants.MimirPeerConcurrentSends).Err(err).Msg("fail to get mimir value")
	} else if peerConcurrentSends > 0 {
		if peerConcurrentSends != int64(s.batcher.peerMgr.getLimit()) {
			s.batcher.peerMgr.updateLimit(int(peerConcurrentSends)) //nolint:staticcheck
		}
	}

	peerConcurrentReceives, err := s.bridge.GetMimir(constants.MimirPeerConcurrentReceives)
	if err != nil {
		s.logger.Error().Str("mimir", constants.MimirPeerConcurrentReceives).Err(err).Msg("fail to get mimir value")
	} else if peerConcurrentReceives > 0 {
		if peerConcurrentReceives != int64(s.peerMgr.getLimit()) {
			s.peerMgr.updateLimit(int(peerConcurrentReceives)) //nolint:staticcheck
		}
	}
}
