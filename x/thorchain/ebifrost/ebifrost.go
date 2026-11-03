package ebifrost

import (
	"bytes"
	"context"
	"errors"
	fmt "fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"

	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/hashicorp/go-metrics"
	common "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/grpc"
)

const (
	// This is a dummy address used for injected transactions.
	// It is not used otherwise, as quorum msg attestations are verified within the handlers.
	ebifrostSigner                     = "thor1zxhfu0qmmq6gmgq4sgz0xgq69h0nhqx5yrseu5"
	cachedBlocks                       = 10
	EventQuorumTxCommitted             = "quorum_tx_committed"
	EventQuorumNetworkFeeCommitted     = "quorum_network_fee_committed"
	EventQuorumSolvencyCommitted       = "quorum_solvency_committed"
	EventQuorumErrataTxCommitted       = "quorum_errata_tx_committed"
	EventQuorumPriceFeedBatchCommitted = "quorum_price_feed_batch:committed"
)

var ErrAlreadyStarted = errors.New("ebifrost already started")

var notifyEvents = []string{
	EventQuorumTxCommitted,
	EventQuorumNetworkFeeCommitted,
	EventQuorumSolvencyCommitted,
	EventQuorumErrataTxCommitted,
	EventQuorumPriceFeedBatchCommitted,
}

var _, ebifrostSignerAcc, _ = bech32.DecodeAndConvert(ebifrostSigner)

// copyQuorum transfers ownership of a protobuf message to the cache. A wire
// round trip is used because gogoproto.Clone cannot merge the custom Cosmos
// integer fields carried by some quorum messages (notably solvency).
func copyQuorum[T proto.Message](item T) (T, error) {
	var zero T
	bz, err := proto.Marshal(item)
	if err != nil {
		return zero, err
	}
	copy, ok := reflect.New(reflect.TypeOf(item).Elem()).Interface().(T)
	if !ok {
		return zero, fmt.Errorf("quorum copy has unexpected type %T", item)
	}
	if err := proto.Unmarshal(bz, copy); err != nil {
		return zero, err
	}
	return copy, nil
}

// EnshrinedBifrost is a thornode service that is used to communicate with bifrost
// for the purpose of observing transactions and processing quorum attestations.
type EnshrinedBifrost struct {
	cdc    codec.Codec
	s      *grpc.Server
	logger log.Logger

	cfg EBifrostConfig

	quorumTxCache   *InjectCache[*common.QuorumTx]
	networkFeeCache *InjectCache[*common.QuorumNetworkFee]
	solvencyCache   *InjectCache[*common.QuorumSolvency]
	errataCache     *InjectCache[*common.QuorumErrataTx]
	priceFeedCache  *InjectCache[*common.QuorumPriceFeedBatch]

	subscribers   map[string][]chan *EventNotification
	subscribersMu sync.Mutex

	started bool
	mu      sync.Mutex

	stopChan chan struct{}
}

func NewEnshrinedBifrost(cdc codec.Codec, logger log.Logger, config EBifrostConfig) *EnshrinedBifrost {
	if !config.Enable {
		return nil
	}

	s := grpc.NewServer()
	ebs := &EnshrinedBifrost{
		cdc:             cdc,
		logger:          logger.With("module", "ebifrost"),
		s:               s,
		quorumTxCache:   NewInjectCache[*common.QuorumTx](),
		networkFeeCache: NewInjectCache[*common.QuorumNetworkFee](),
		solvencyCache:   NewInjectCache[*common.QuorumSolvency](),
		errataCache:     NewInjectCache[*common.QuorumErrataTx](),
		priceFeedCache:  NewInjectCache[*common.QuorumPriceFeedBatch](),
		subscribers:     make(map[string][]chan *EventNotification),
		cfg:             config,
		stopChan:        make(chan struct{}),
	}

	RegisterLocalhostBifrostServer(s, ebs)
	return ebs
}

func (b *EnshrinedBifrost) Start() error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return ErrAlreadyStarted
	}
	b.started = true

	lis, err := net.Listen("tcp", b.cfg.Address)
	if err != nil {
		return err
	}
	go func() {
		if err := b.s.Serve(lis); err != nil {
			panic(fmt.Errorf("failed to start enshrined bifrost grpc server: %w", err))
		}
	}()

	// Start the prune timer if TTL is enabled
	if b.cfg.CacheItemTTL > 0 {
		b.startPruneTimer()
	}

	return nil
}

func (b *EnshrinedBifrost) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		return
	}
	b.started = false

	close(b.stopChan)

	b.s.Stop()
}

// startPruneTimer starts a timer to periodically prune expired items from the caches
func (b *EnshrinedBifrost) startPruneTimer() {
	interval := b.cfg.CacheItemTTL / 10 // Check every 1/10th of the TTL
	if interval < time.Second {
		interval = time.Second // Minimum interval of 1 second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				b.logger.Debug("Pruning expired cache items", "ttl", b.cfg.CacheItemTTL.String())
				prunedTxs := b.quorumTxCache.PruneExpiredItems(b.cfg.CacheItemTTL)
				prunedNfs := b.networkFeeCache.PruneExpiredItems(b.cfg.CacheItemTTL)
				prunedSlvs := b.solvencyCache.PruneExpiredItems(b.cfg.CacheItemTTL)
				prunedEtxs := b.errataCache.PruneExpiredItems(b.cfg.CacheItemTTL)

				// An attested item that expires here never reached consensus, so
				// any sustained non-zero rate means observed-but-unprocessed
				// activity. priceFeedCache is deliberately excluded: it is pruned
				// with ttl 0 on every commit, which is normal operation.
				for _, pruned := range []struct {
					kind  string
					count int
				}{
					{"tx", len(prunedTxs)},
					{"network_fee", len(prunedNfs)},
					{"solvency", len(prunedSlvs)},
					{"errata", len(prunedEtxs)},
				} {
					if pruned.count == 0 {
						continue
					}
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "ebifrost", "cache_pruned"},
						float32(pruned.count),
						[]metrics.Label{telemetry.NewLabel("kind", pruned.kind)},
					)
				}

				// Pending-attestation backlog: rising depth with a flat injection
				// rate means items are accumulating faster than the proposer can
				// inject them, and pruning is about to start.
				for _, depth := range []struct {
					kind  string
					items int
				}{
					{"tx", len(b.quorumTxCache.Get())},
					{"network_fee", len(b.networkFeeCache.Get())},
					{"solvency", len(b.solvencyCache.Get())},
					{"errata", len(b.errataCache.Get())},
				} {
					telemetry.SetGaugeWithLabels(
						[]string{"thornode", "ebifrost", "cache_items"},
						float32(depth.items),
						[]metrics.Label{telemetry.NewLabel("kind", depth.kind)},
					)
				}

				for _, tx := range prunedTxs {
					b.logger.Warn(
						"EBifrost pruned quorum tx",
						"attestations", len(tx.Attestations),
						"chain", tx.ObsTx.Tx.Chain,
						"hash", tx.ObsTx.Tx.ID,
						"block_height", tx.ObsTx.BlockHeight,
						"finalise_height", tx.ObsTx.FinaliseHeight,
						"from", tx.ObsTx.Tx.FromAddress,
						"to", tx.ObsTx.Tx.ToAddress,
						"memo", tx.ObsTx.Tx.Memo,
						"coins", tx.ObsTx.Tx.Coins.String(),
						"gas", tx.ObsTx.Tx.Gas.ToCoins().String(),
						"inbound", tx.Inbound,
					)
				}
				for _, nf := range prunedNfs {
					b.logger.Warn(
						"EBifrost pruned quorum network fee",
						"attestations", len(nf.Attestations),
						"chain", nf.NetworkFee.Chain,
						"height", nf.NetworkFee.Height,
						"tx_size", nf.NetworkFee.TransactionSize,
						"tx_rate", nf.NetworkFee.TransactionRate,
					)
				}
				for _, slv := range prunedSlvs {
					b.logger.Warn(
						"Ebifrost pruned quorum solvency",
						"attestations", len(slv.Attestations),
						"chain", slv.Solvency.Chain,
						"height", slv.Solvency.Height,
						"pubkey", slv.Solvency.PubKey,
						"coins", slv.Solvency.Coins.String(),
					)
				}
				for _, etx := range prunedEtxs {
					b.logger.Warn(
						"Ebifrost pruned quorum errata tx",
						"attestations", len(etx.Attestations),
						"chain", etx.ErrataTx.Chain,
						"id", etx.ErrataTx.Id,
					)
				}
			case <-b.stopChan:
				return
			}
		}
	}()
}

func (b *EnshrinedBifrost) SendQuorumTx(ctx context.Context, tx *common.QuorumTx) (*SendQuorumTxResult, error) {
	// The gRPC caller retains its message. The cache owns an independent copy.
	var err error
	tx, err = copyQuorum(tx)
	if err != nil {
		return nil, err
	}
	if payload, err := tx.ObsTx.GetSignablePayloadWithInbound(tx.Inbound); err == nil {
		warmAttestationSigs(sigWarmJob{payload, append([]*common.Attestation(nil), tx.Attestations...)})
	}
	if err := b.quorumTxCache.AddItem(
		tx,
		(*common.QuorumTx).GetAttestations,
		(*common.QuorumTx).SetAttestations,
		(*common.QuorumTx).Equals,
	); err != nil {
		return nil, err
	}

	return &SendQuorumTxResult{}, nil
}

func (b *EnshrinedBifrost) SendQuorumNetworkFee(ctx context.Context, nf *common.QuorumNetworkFee) (*SendQuorumNetworkFeeResult, error) {
	var err error
	nf, err = copyQuorum(nf)
	if err != nil {
		return nil, err
	}
	if nf.NetworkFee != nil {
		if payload, err := nf.NetworkFee.GetSignablePayload(); err == nil {
			warmAttestationSigs(sigWarmJob{payload, append([]*common.Attestation(nil), nf.Attestations...)})
		}
	}
	if err := b.networkFeeCache.AddItem(
		nf,
		(*common.QuorumNetworkFee).GetAttestations,
		(*common.QuorumNetworkFee).SetAttestations,
		(*common.QuorumNetworkFee).Equals,
	); err != nil {
		return nil, err
	}

	return &SendQuorumNetworkFeeResult{}, nil
}

func (b *EnshrinedBifrost) SendQuorumSolvency(ctx context.Context, s *common.QuorumSolvency) (*SendQuorumSolvencyResult, error) {
	var err error
	s, err = copyQuorum(s)
	if err != nil {
		return nil, err
	}
	if s.Solvency != nil {
		// GetSignablePayload fills in an empty Id; warm from a copy so the
		// cached item stays exactly as received.
		solvency := *s.Solvency
		if payload, err := solvency.GetSignablePayload(); err == nil {
			warmAttestationSigs(sigWarmJob{payload, append([]*common.Attestation(nil), s.Attestations...)})
		}
	}
	if err := b.solvencyCache.AddItem(
		s,
		(*common.QuorumSolvency).GetAttestations,
		(*common.QuorumSolvency).SetAttestations,
		(*common.QuorumSolvency).Equals,
	); err != nil {
		return nil, err
	}

	return &SendQuorumSolvencyResult{}, nil
}

func (b *EnshrinedBifrost) SendQuorumErrataTx(ctx context.Context, e *common.QuorumErrataTx) (*SendQuorumErrataTxResult, error) {
	var err error
	e, err = copyQuorum(e)
	if err != nil {
		return nil, err
	}
	if e.ErrataTx != nil {
		if payload, err := e.ErrataTx.GetSignablePayload(); err == nil {
			warmAttestationSigs(sigWarmJob{payload, append([]*common.Attestation(nil), e.Attestations...)})
		}
	}
	b.quorumTxCache.Lock()
	for i, item := range b.quorumTxCache.items {
		tx := item.Item
		if tx.ObsTx.Tx.Chain == e.ErrataTx.Chain && tx.ObsTx.Tx.ID == e.ErrataTx.Id {
			// remove the tx from the cache because we observed an error for it
			b.quorumTxCache.items = append(b.quorumTxCache.items[:i], b.quorumTxCache.items[i+1:]...)
			break
		}
	}
	b.quorumTxCache.Unlock()

	if err := b.errataCache.AddItem(
		e,
		(*common.QuorumErrataTx).GetAttestations,
		(*common.QuorumErrataTx).SetAttestations,
		(*common.QuorumErrataTx).Equals,
	); err != nil {
		return nil, err
	}

	return &SendQuorumErrataTxResult{}, nil
}

func (b *EnshrinedBifrost) SendQuorumPriceFeedBatch(ctx context.Context, qpfb *common.QuorumPriceFeedBatch) (*SendQuorumPriceFeedBatchResult, error) {
	var err error
	qpfb, err = copyQuorum(qpfb)
	if err != nil {
		return nil, err
	}
	jobs := make([]sigWarmJob, 0, len(qpfb.QuorumPriceFeeds))
	for _, qpf := range qpfb.QuorumPriceFeeds {
		if qpf == nil || qpf.PriceFeed == nil {
			continue
		}
		if payload, err := qpf.PriceFeed.GetSignablePayload(); err == nil {
			jobs = append(jobs, sigWarmJob{payload, append([]*common.Attestation(nil), qpf.Attestations...)})
		}
	}
	warmAttestationSigs(jobs...)
	// no need to check for existing attestations or merging, update the
	// cache immediately
	b.priceFeedCache.Lock()
	if len(b.priceFeedCache.items) == 0 {
		b.priceFeedCache.items = []TimestampedItem[*common.QuorumPriceFeedBatch]{{
			Item:      qpfb,
			Timestamp: time.Now(),
		}}
	} else {
		qpfs := b.priceFeedCache.items[0].Item.QuorumPriceFeeds
		for _, newQpf := range qpfb.QuorumPriceFeeds {
			found := false

			newPk := newQpf.Attestations[0].PubKey

			for i, oldQpf := range qpfs {
				oldPk := oldQpf.Attestations[0].PubKey

				if !bytes.Equal(oldPk, newPk) {
					continue
				}

				qpfs[i] = newQpf
				found = true
				break
			}
			if !found {
				qpfs = append(qpfs, newQpf)
			}
		}
		b.priceFeedCache.items[0].Item.QuorumPriceFeeds = qpfs
		b.priceFeedCache.items[0].Timestamp = time.Now()
	}
	b.priceFeedCache.Unlock()

	return &SendQuorumPriceFeedBatchResult{}, nil
}

func (b *EnshrinedBifrost) SubscribeToEvents(req *SubscribeRequest, stream LocalhostBifrost_SubscribeToEventsServer) error {
	for _, eventType := range req.EventTypes {
		found := false
		for _, notifyEvent := range notifyEvents {
			if eventType == notifyEvent {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown event type: %s", eventType)
		}
	}

	eventCh := make(chan *EventNotification)

	// Register this client
	b.subscribersMu.Lock()
	for _, eventType := range req.EventTypes {
		b.subscribers[eventType] = append(b.subscribers[eventType], eventCh)
	}
	b.subscribersMu.Unlock()

	b.logger.Info("Client subscribed to events", "event_types", req.EventTypes)

	// Keep the connection open and forward events to client
	for {
		select {
		case event := <-eventCh:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-stream.Context().Done():
			// Client disconnected, clean up
			b.removeSubscriber(eventCh)
			return nil
		}
	}
}

// removeSubscriber removes the given channel from all event type subscriptions
func (b *EnshrinedBifrost) removeSubscriber(ch chan *EventNotification) {
	b.subscribersMu.Lock()
	defer b.subscribersMu.Unlock()

	// Iterate through all event types
	// analyze-ignore(map-iteration)
	for eventType, subscribers := range b.subscribers {
		// Create a new slice without the channel we're removing
		newSubscribers := make([]chan *EventNotification, 0, len(subscribers))

		for _, subCh := range subscribers {
			if subCh != ch {
				newSubscribers = append(newSubscribers, subCh)
			}
		}

		// Update the subscribers list for this event type
		if len(newSubscribers) == 0 {
			// No subscribers left for this event type, remove the key
			delete(b.subscribers, eventType)
		} else {
			b.subscribers[eventType] = newSubscribers
		}
	}

	// Close the channel to signal subscribers they won't receive more events
	close(ch)
}

func (b *EnshrinedBifrost) broadcastEvent(eventType string, payload []byte) {
	event := &EventNotification{
		EventType: eventType,
		Payload:   payload,
		Timestamp: time.Now().Unix(),
	}

	b.subscribersMu.Lock()
	subscribers := b.subscribers[eventType]
	b.subscribersMu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
			b.logger.Debug("Event sent to subscriber", "event", eventType)
			// Event sent successfully
		default:
			b.logger.Error("Failed to send event to subscriber", "event", eventType)
			// Channel is full or closed, could implement cleanup here
		}
	}
}

func (b *EnshrinedBifrost) broadcastQuorumTxEvent(tx *common.QuorumTx) {
	payload, err := tx.Marshal()
	if err != nil {
		b.logger.Error("Failed to marshal quorum tx event", "error", err)
		return
	}
	go b.broadcastEvent(EventQuorumTxCommitted, payload)
}

// MarkQuorumTxAttestationsConfirmed is intended to be called by the bifrost post handler after a tx has been processed.
// It will look for any matching quorum txs, remove the confirmed attestations from them, and remove the quorum txs if all attestations have been confirmed.
func (b *EnshrinedBifrost) MarkQuorumTxAttestationsConfirmed(ctx context.Context, qtx *common.QuorumTx) {
	if b == nil {
		return
	}

	// The recent-block cache retains this message after confirmation. Keep it
	// independent of the caller and of any pending cache item.
	var err error
	qtx, err = copyQuorum(qtx)
	if err != nil {
		b.logger.Error("Failed to copy quorum tx confirmation", "error", err)
		return
	}
	// Encode before cache confirmation can remove attestations from an aliased item.
	b.broadcastQuorumTxEvent(qtx)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	// Use atomic method to prevent race condition between MarkAttestationsConfirmed and AddToBlock.
	// This ensures AddItem cannot check recentBlockItems between these two operations.
	found := b.quorumTxCache.MarkAttestationsConfirmedAndAddToBlock(
		qtx,
		height,
		cachedBlocks,
		b.logger,
		(*common.QuorumTx).Equals,
		(*common.QuorumTx).GetAttestations,
		(*common.QuorumTx).RemoveAttestations,
		func(qtx *common.QuorumTx, logger log.Logger) {
			obsTx := qtx.ObsTx
			logger.Debug("Marking quorum tx attestations confirmed",
				"chain", obsTx.Tx.Chain,
				"hash", obsTx.Tx.ID,
				"attestations", len(qtx.Attestations))
		},
	)

	if !found {
		cmpObsTx := qtx.ObsTx
		b.logger.Debug(
			"Failed to find quorum tx to mark attestations confirmed",
			"chain", cmpObsTx.Tx.Chain,
			"hash", cmpObsTx.Tx.ID,
		)
	}
}

func (b *EnshrinedBifrost) broadcastQuorumNetworkFeeEvent(nf *common.QuorumNetworkFee) {
	payload, err := nf.Marshal()
	if err != nil {
		b.logger.Error("Failed to marshal quorum network fee event", "error", err)
		return
	}
	go b.broadcastEvent(EventQuorumNetworkFeeCommitted, payload)
}

// MarkQuorumNetworkFeeAttestationsConfirmed is intended to be called by the bifrost post handler after a tx has been processed.
// It will look for any matching quorum txs, remove the confirmed attestations from them, and remove the quorum txs if all attestations have been confirmed.
func (b *EnshrinedBifrost) MarkQuorumNetworkFeeAttestationsConfirmed(ctx context.Context, qnf *common.QuorumNetworkFee) {
	if b == nil {
		return
	}

	var err error
	qnf, err = copyQuorum(qnf)
	if err != nil {
		b.logger.Error("Failed to copy quorum network fee confirmation", "error", err)
		return
	}
	b.broadcastQuorumNetworkFeeEvent(qnf)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	// Use atomic method to prevent race condition between MarkAttestationsConfirmed and AddToBlock.
	found := b.networkFeeCache.MarkAttestationsConfirmedAndAddToBlock(
		qnf,
		height,
		cachedBlocks,
		b.logger,
		(*common.QuorumNetworkFee).Equals,
		(*common.QuorumNetworkFee).GetAttestations,
		(*common.QuorumNetworkFee).RemoveAttestations,
		func(qnf *common.QuorumNetworkFee, logger log.Logger) {
			nf := qnf.NetworkFee
			logger.Debug("Marking quorum network fee attestations confirmed",
				"chain", nf.Chain,
				"height", nf.Height,
				"tx_size", nf.TransactionSize,
				"tx_rate", nf.TransactionRate,
				"attestations", len(qnf.Attestations))
		},
	)

	if !found {
		cmpNf := qnf.NetworkFee
		b.logger.Debug(
			"Failed to find quorum network fee to mark attestations confirmed",
			"chain", cmpNf.Chain,
			"height", cmpNf.Height,
			"tx_size", cmpNf.TransactionSize,
			"tx_rate", cmpNf.TransactionRate,
		)
	}
}

func (b *EnshrinedBifrost) broadcastQuorumSolvencyEvent(s *common.QuorumSolvency) {
	payload, err := s.Marshal()
	if err != nil {
		b.logger.Error("Failed to marshal quorum solvency event", "error", err)
		return
	}
	go b.broadcastEvent(EventQuorumSolvencyCommitted, payload)
}

// MarkQuorumSolvencyAttestationsConfirmed is intended to be called by the bifrost post handler after a tx has been processed.
// It will look for any matching quorum txs, remove the confirmed attestations from them, and remove the quorum txs if all attestations have been confirmed.
func (b *EnshrinedBifrost) MarkQuorumSolvencyAttestationsConfirmed(ctx context.Context, qs *common.QuorumSolvency) {
	if b == nil {
		return
	}

	var err error
	qs, err = copyQuorum(qs)
	if err != nil {
		b.logger.Error("Failed to copy quorum solvency confirmation", "error", err)
		return
	}
	b.broadcastQuorumSolvencyEvent(qs)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	// Use atomic method to prevent race condition between MarkAttestationsConfirmed and AddToBlock.
	found := b.solvencyCache.MarkAttestationsConfirmedAndAddToBlock(
		qs,
		height,
		cachedBlocks,
		b.logger,
		(*common.QuorumSolvency).Equals,
		(*common.QuorumSolvency).GetAttestations,
		(*common.QuorumSolvency).RemoveAttestations,
		func(qs *common.QuorumSolvency, logger log.Logger) {
			s := qs.Solvency
			logger.Debug("Marking quorum solvency attestations confirmed",
				"chain", s.Chain,
				"height", s.Height,
				"coins", s.Coins,
				"pub_key", s.PubKey,
				"attestations", len(qs.Attestations))
		},
	)

	if !found {
		cmpS := qs.Solvency
		b.logger.Debug(
			"Failed to find quorum solvency to mark attestations confirmed",
			"chain", cmpS.Chain,
			"height", cmpS.Height,
			"coins", cmpS.Coins,
			"pub_key", cmpS.PubKey,
		)
	}
}

func (b *EnshrinedBifrost) broadcastQuorumErrataTxEvent(qe *common.QuorumErrataTx) {
	payload, err := qe.Marshal()
	if err != nil {
		b.logger.Error("Failed to marshal quorum errata event", "error", err)
		return
	}
	go b.broadcastEvent(EventQuorumErrataTxCommitted, payload)
}

// MarkQuorumErrataTxAttestationsConfirmed is intended to be called by the bifrost post handler after a tx has been processed.
// It will look for any matching quorum txs, remove the confirmed attestations from them, and remove the quorum txs if all attestations have been confirmed.
func (b *EnshrinedBifrost) MarkQuorumErrataTxAttestationsConfirmed(ctx context.Context, qe *common.QuorumErrataTx) {
	if b == nil {
		return
	}

	var err error
	qe, err = copyQuorum(qe)
	if err != nil {
		b.logger.Error("Failed to copy quorum errata confirmation", "error", err)
		return
	}
	// Freeze the committed receipt before cache cleanup or caller reuse.
	// Subscriber delivery remains asynchronous and owns only the encoded bytes.
	b.broadcastQuorumErrataTxEvent(qe)

	if qe.ErrataTx.Chain.RequiresTerminalErrataOutcome() &&
		qe.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED &&
		qe.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION {
		// A delivered pending batch is no longer in flight, but its signatures
		// must be admissible on the next durable Bifrost retry. Recording it in
		// recentBlockItems would delay retries until cache expiry, which
		// expireXMRErrataReceipts checks on each local proposal.
		b.errataCache.mu.Lock()
		b.errataCache.markAttestationsConfirmedLocked(
			qe, b.logger,
			(*common.QuorumErrataTx).Equals,
			(*common.QuorumErrataTx).GetAttestations,
			(*common.QuorumErrataTx).RemoveAttestations,
			func(*common.QuorumErrataTx, log.Logger) {},
		)
		b.errataCache.mu.Unlock()
		return
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	// Use atomic method to prevent race condition between MarkAttestationsConfirmed and AddToBlock.
	found := b.errataCache.MarkAttestationsConfirmedAndAddToBlock(
		qe,
		height,
		cachedBlocks,
		b.logger,
		(*common.QuorumErrataTx).Equals,
		(*common.QuorumErrataTx).GetAttestations,
		(*common.QuorumErrataTx).RemoveAttestations,
		func(qe *common.QuorumErrataTx, logger log.Logger) {
			er := qe.ErrataTx
			logger.Debug("Marking quorum errata attestations confirmed",
				"chain", er.Chain,
				"id", er.Id,
				"attestations", len(qe.Attestations))
		},
	)

	if !found {
		cmpEr := qe.ErrataTx
		b.logger.Debug(
			"Failed to find quorum errata to mark attestations confirmed",
			"chain", cmpEr.Chain,
			"id", cmpEr.Id,
		)
	}
}

func (b *EnshrinedBifrost) broadcastQuorumPriceFeedBatchEvent(qpfb *common.QuorumPriceFeedBatch) {
	payload, err := qpfb.Marshal()
	if err != nil {
		b.logger.Error("Failed to marshal quorum price feed event", "error", err)
		return
	}
	go b.broadcastEvent(EventQuorumPriceFeedBatchCommitted, payload)
}

// MarkQuorumPriceFeedBatchAttestationsConfirmed is intended to be called by the bifrost post handler after a tx has been processed.
// It will broadcast the commit event.
func (b *EnshrinedBifrost) MarkQuorumPriceFeedBatchAttestationsConfirmed(ctx context.Context, qpf *common.QuorumPriceFeedBatch) {
	if b == nil {
		return
	}

	// Remove all cached price feeds, so we don't keep feeds from churned nodes
	b.priceFeedCache.PruneExpiredItems(0)
	b.broadcastQuorumPriceFeedBatchEvent(qpf)
}

func (b *EnshrinedBifrost) MarshalTx(msg sdk.Msg) ([]byte, error) {
	itx := NewInjectTx(b.cdc, []sdk.Msg{msg})
	return itx.Tx.Marshal()
}

// expireXMRErrataReceipts lets a durable Bifrost retry recover a lost terminal
// event after the existing cache window. Quiet errata history must age with
// proposal height, not depend on a different errata being confirmed first.
// Keep other chains' delivery semantics unchanged.
func (b *EnshrinedBifrost) expireXMRErrataReceipts(currentHeight int64) {
	b.errataCache.mu.Lock()
	defer b.errataCache.mu.Unlock()
	// This is proposer-local retry history, not consensus state.
	// analyze-ignore(map-iteration)
	for height, items := range b.errataCache.recentBlockItems {
		if height >= currentHeight-cachedBlocks {
			continue
		}
		retained := items[:0]
		for _, item := range items {
			if item != nil && item.ErrataTx != nil && item.ErrataTx.Chain.RequiresTerminalErrataOutcome() {
				continue
			}
			retained = append(retained, item)
		}
		clear(items[len(retained):])
		if len(retained) == 0 {
			delete(b.errataCache.recentBlockItems, height)
		} else {
			b.errataCache.recentBlockItems[height] = retained
		}
	}
}

// ProposalInjectTxs is intended to be called by the current proposing validator during PrepareProposal
// and will return a list of in-quorum transactions to be included in the next block along with the total byte length of the transactions.
func (b *EnshrinedBifrost) ProposalInjectTxs(ctx sdk.Context, maxTxBytes int64) ([][]byte, int64) {
	if b == nil {
		return nil, 0
	}

	b.expireXMRErrataReceipts(ctx.BlockHeight())

	var injectTxs [][]byte
	var txBzLen int64

	// Process observed txs
	txBzs := b.quorumTxCache.ProcessForProposal(
		func(tx *common.QuorumTx) (sdk.Msg, error) {
			return types.NewMsgObservedTxQuorum(tx, ebifrostSignerAcc), nil
		},
		b.MarshalTx,
		func(tx *common.QuorumTx, logger log.Logger) {
			obsTx := tx.ObsTx
			logger.Info("Injecting quorum tx",
				"chain", obsTx.Tx.Chain,
				"hash", obsTx.Tx.ID,
				"finalized", obsTx.IsFinal(),
				"inbound", tx.Inbound,
				"attestations", len(tx.Attestations),
				"memo", obsTx.Tx.Memo,
				"coins", obsTx.Tx.Coins)
		},
		b.logger,
	)
	for _, bz := range txBzs {
		addLen := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{bz})
		if txBzLen+addLen > maxTxBytes {
			// Silently skipped because the proposed block is full. The item stays
			// cached and is retried, but under sustained load it can be dropped
			// every round until PruneExpiredItems discards it. Only increments on
			// the proposing node, so the network rate is the sum across nodes.
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "ebifrost", "inject_dropped_oversize"},
				1,
				[]metrics.Label{telemetry.NewLabel("kind", "tx")},
			)
			continue
		}
		txBzLen += addLen
		injectTxs = append(injectTxs, bz)
	}

	// Process network fees
	nfBzs := b.networkFeeCache.ProcessForProposal(
		func(qnf *common.QuorumNetworkFee) (sdk.Msg, error) {
			return types.NewMsgNetworkFeeQuorum(qnf, ebifrostSignerAcc), nil
		},
		b.MarshalTx,
		func(qnf *common.QuorumNetworkFee, logger log.Logger) {
			nf := qnf.NetworkFee
			logger.Info("Injecting quorum network fee",
				"chain", nf.Chain,
				"height", nf.Height,
				"attestations", len(qnf.Attestations))
		},
		b.logger,
	)
	for _, bz := range nfBzs {
		addLen := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{bz})
		if txBzLen+addLen > maxTxBytes {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "ebifrost", "inject_dropped_oversize"},
				1,
				[]metrics.Label{telemetry.NewLabel("kind", "network_fee")},
			)
			continue
		}
		txBzLen += addLen
		injectTxs = append(injectTxs, bz)
	}

	// Process solvency
	sBzs := b.solvencyCache.ProcessForProposal(
		func(qs *common.QuorumSolvency) (sdk.Msg, error) {
			return types.NewMsgSolvencyQuorum(qs, ebifrostSignerAcc)
		},
		b.MarshalTx,
		func(qs *common.QuorumSolvency, logger log.Logger) {
			s := qs.Solvency
			logger.Info("Injecting quorum solvency",
				"chain", s.Chain,
				"pubkey", s.PubKey,
				"height", s.Height,
				"coins", s.Coins,
				"attestations", len(qs.Attestations))
		},
		b.logger,
	)
	for _, bz := range sBzs {
		addLen := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{bz})
		if txBzLen+addLen > maxTxBytes {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "ebifrost", "inject_dropped_oversize"},
				1,
				[]metrics.Label{telemetry.NewLabel("kind", "solvency")},
			)
			continue
		}
		txBzLen += addLen
		injectTxs = append(injectTxs, bz)
	}

	// Process errata
	eBzs := b.errataCache.ProcessForProposal(
		func(qe *common.QuorumErrataTx) (sdk.Msg, error) {
			return types.NewMsgErrataTxQuorum(qe, ebifrostSignerAcc), nil
		},
		b.MarshalTx,
		func(qe *common.QuorumErrataTx, logger log.Logger) {
			e := qe.ErrataTx
			logger.Info("Injecting quorum errata",
				"chain", e.Chain,
				"id", e.Id,
				"attestations", len(qe.Attestations))
		},
		b.logger,
	)
	for _, bz := range eBzs {
		addLen := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{bz})
		if txBzLen+addLen > maxTxBytes {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "ebifrost", "inject_dropped_oversize"},
				1,
				[]metrics.Label{telemetry.NewLabel("kind", "errata")},
			)
			continue
		}
		txBzLen += addLen
		injectTxs = append(injectTxs, bz)
	}

	// Process price feeds
	pfBzs := b.priceFeedCache.ProcessForProposal(
		func(qpfb *common.QuorumPriceFeedBatch) (sdk.Msg, error) {
			return types.NewMsgPriceFeedQuorumBatch(qpfb.QuorumPriceFeeds, ebifrostSignerAcc), nil
		},
		b.MarshalTx,
		func(qpfb *common.QuorumPriceFeedBatch, logger log.Logger) {
			logger.Info("Injecting quorum price feed batch",
				"price_feeds", len(qpfb.QuorumPriceFeeds))
		},
		b.logger,
	)
	for _, bz := range pfBzs {
		addLen := cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{bz})
		if txBzLen+addLen > maxTxBytes {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "ebifrost", "inject_dropped_oversize"},
				1,
				[]metrics.Label{telemetry.NewLabel("kind", "price_feed")},
			)
			continue
		}
		txBzLen += addLen
		injectTxs = append(injectTxs, bz)
	}

	return injectTxs, txBzLen
}

// Test use only
func (b *EnshrinedBifrost) GetInjectedMsgs(ctx sdk.Context, txs [][]byte) ([]sdk.Msg, error) {
	var msgs []sdk.Msg

	for _, txbz := range txs {
		tx, err := TxDecoder(b.cdc, nil)(txbz)
		if err != nil {
			return nil, err
		}

		msgs = append(msgs, tx.GetMsgs()...)
	}

	return msgs, nil
}
