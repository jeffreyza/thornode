package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/oracle"

	lru "github.com/hashicorp/golang-lru"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// signedTxOutCacheSize is the number of signed tx out observations to keep in memory
// to prevent duplicate observations. Based on historical data at the time of writing,
// the peak of Thorchain's L1 swaps was 10k per day.
const signedTxOutCacheSize = 10_000

// deckRefreshTime is the time to wait before reconciling txIn status.
const deckRefreshTime = 1 * time.Second

// defaultOracleUpdateInterval is the interval in which new oracle prices
// are computed and gossiped via p2p
const defaultOracleUpdateInterval = 1 * time.Second

const (
	committedOnlyErrataSafetyHorizon = 30 * 24 * time.Hour
	committedOnlyErrataPruneInterval = time.Hour
	errataAttestationRetryHorizon    = 24 * time.Hour
	errataAttestationRetryMaxDelay   = time.Hour
)

// errataOutcomeBridge deliberately bypasses the normal one-block HTTP cache.
// A cached pre-handler voter response cannot safely decide whether an XMR
// transaction reached Thornode's permanent Reverted state.
type errataOutcomeBridge interface {
	GetBlockHeightUncached() (int64, error)
	GetObservedTxVoterUncached(string) (*stypes.QueryObservedTxVoter, bool, error)
}

// Producers intentionally publish network-fee updates without blocking. A
// bounded queue absorbs short Observer scheduling gaps while preventing
// unbounded memory growth during RPC bursts.
const networkFeeQueueCapacity = 256

var (
	errObservedTxInactive = errors.New("observer is not active")
	errObservedTxEmpty    = errors.New("observed transaction has no items")
	errObservedTxFiltered = errors.New("observed transaction was rejected by observer filters")
	errObservedTxErrata   = types.ErrObservedTxOrphaned
)

type txInKey struct {
	chain                  common.Chain
	height                 int64
	allowFutureObservation bool
}

// durableObservedTxCommitter is implemented by chain clients whose local
// duplicate-payment guard must reach durable storage before Observer removes or
// marks the corresponding deck item. The separate method preserves the shared
// ChainClient interface for clients without this requirement.
type durableObservedTxCommitter interface {
	OnObservedTxInDurable(txIn types.TxInItem, blockHeight int64) error
}

func TxInKey(txIn *types.TxIn) txInKey {
	return txInKey{
		chain:                  txIn.Chain,
		height:                 txIn.TxArray[0].BlockHeight + txIn.ConfirmationRequired,
		allowFutureObservation: txIn.AllowFutureObservation,
	}
}

// Observer observer service
type Observer struct {
	logger                zerolog.Logger
	chains                map[common.Chain]chainclients.ChainClient
	chainLifecycleMu      sync.Mutex
	startedChains         map[common.Chain]chainclients.ChainClient
	stopping              bool
	oracle                *oracle.Oracle
	stopChan              chan struct{}
	pubkeyMgr             *pubkeymanager.PubKeyManager
	onDeck                map[txInKey]*types.TxIn
	lock                  *sync.Mutex
	wg                    *sync.WaitGroup
	globalTxsQueue        chan types.TxIn
	globalErrataQueue     chan types.ErrataBlock
	globalSolvencyQueue   chan types.Solvency
	globalNetworkFeeQueue chan common.NetworkFee
	globalPriceFeedQueue  chan common.PriceFeed
	m                     *metrics.Metrics
	errCounter            *prometheus.CounterVec
	thorchainBridge       thorclient.ThorchainBridge
	storage               *ObserverStorage
	tssKeysignMetricMgr   *metrics.TssKeysignMetricMgr

	// signedTxOutCache is a cache to keep track of observations for outbounds which were
	// manually observed after completion of signing and should be filtered from future
	// mempool and block observations.
	signedTxOutCache   *lru.Cache
	signedTxOutCacheMu sync.Mutex
	attestationGossip  *AttestationGossip

	observerWorkers int

	lastNodeStatus   stypes.NodeStatus
	lastNodeStatusMu sync.RWMutex

	deckDumpFile string

	// beforeObservedTxDeckLock is a deterministic race-test hook. Production
	// leaves it nil.
	beforeObservedTxDeckLock        func()
	errataAttestFn                  func(context.Context, common.ErrataTx) error
	permanentErrataQuarantineFn     func(common.ErrataTx, int64) error
	permanentErrataBroadcastBlockFn func(common.ErrataTx) error
	errataAttestMu                  sync.Mutex
	errataOutcomeMu                 sync.Mutex
	sendObservedTxsFn               func(common.ObservedTxs, bool)
}

// NewObserver create a new instance of Observer for chain
func NewObserver(pubkeyMgr *pubkeymanager.PubKeyManager,
	chains map[common.Chain]chainclients.ChainClient,
	thorchainBridge thorclient.ThorchainBridge,
	m *metrics.Metrics, dataPath string,
	tssKeysignMetricMgr *metrics.TssKeysignMetricMgr,
	attestationGossip *AttestationGossip,
	deckDumpFile string,
) (*Observer, error) {
	logger := log.Logger.With().Str("module", "observer").Logger()

	cfg := config.GetBifrost()

	observerWorkers := cfg.ObserverWorkers
	if observerWorkers == 0 {
		observerWorkers = runtime.NumCPU() / 2
		if observerWorkers == 0 {
			observerWorkers = 1
		}
	}

	var spentRefsGuardSetter chainclients.SpentOutputRefsGuardSetter
	if xmrClient, ok := chains[common.XMRChain]; ok {
		if attestationGossip == nil {
			return nil, errors.New("XMR spent-reference recovery requires errata admission")
		}
		var supported bool
		spentRefsGuardSetter, supported = xmrClient.(chainclients.SpentOutputRefsGuardSetter)
		if !supported {
			return nil, errors.New("XMR chain client does not support spent-reference errata admission")
		}
	}

	storage, err := NewObserverStorage(dataPath, cfg.ObserverLevelDB)
	if err != nil {
		return nil, fmt.Errorf("failed to create observer storage: %w", err)
	}

	if tssKeysignMetricMgr == nil {
		return nil, fmt.Errorf("tss keysign manager is nil")
	}

	signedTxOutCache, err := lru.New(signedTxOutCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create signed tx out cache: %w", err)
	}

	priceOracle, err := oracle.NewOracle(cfg)
	if err != nil {
		return nil, fmt.Errorf("fail to create oracle instance: %w", err)
	}

	observer := &Observer{
		logger:                logger,
		chains:                chains,
		startedChains:         make(map[common.Chain]chainclients.ChainClient),
		oracle:                priceOracle,
		stopChan:              make(chan struct{}),
		m:                     m,
		pubkeyMgr:             pubkeyMgr,
		lock:                  &sync.Mutex{},
		wg:                    &sync.WaitGroup{},
		onDeck:                make(map[txInKey]*types.TxIn),
		globalTxsQueue:        make(chan types.TxIn),
		globalErrataQueue:     make(chan types.ErrataBlock),
		globalSolvencyQueue:   make(chan types.Solvency),
		globalNetworkFeeQueue: make(chan common.NetworkFee, networkFeeQueueCapacity),
		globalPriceFeedQueue:  make(chan common.PriceFeed),
		errCounter:            m.GetCounterVec(metrics.ObserverError),
		thorchainBridge:       thorchainBridge,
		storage:               storage,
		tssKeysignMetricMgr:   tssKeysignMetricMgr,
		signedTxOutCache:      signedTxOutCache,
		attestationGossip:     attestationGossip,
		observerWorkers:       observerWorkers,
		deckDumpFile:          deckDumpFile,
	}
	if attestationGossip != nil {
		// Register committed-event callbacks before Start launches the gossip
		// event client. Otherwise a fast committed event can race startup and
		// leave a durable deck/outbox entry pending indefinitely.
		attestationGossip.certificateObserver = observer
		attestationGossip.SetObserverStorage(observer.storage)
		attestationGossip.SetObserverHandleObservedTxCommitted(observer)
		attestationGossip.SetObserverHandleErrataTxCommitted(observer)
		if spentRefsGuardSetter != nil {
			spentRefsGuardSetter.SetSpentOutputRefsGuard(attestationGossip.acquireXMRSpentRefsAttempt)
		}
	}
	return observer, nil
}

func (o *Observer) getChain(chainID common.Chain) (chainclients.ChainClient, error) {
	chain, ok := o.chains[chainID]
	if !ok {
		o.logger.Debug().Str("chain", chainID.String()).Msg("is not supported yet")
		return nil, errors.New("not supported")
	}
	return chain, nil
}

func (o *Observer) startChainClient(chainID common.Chain, chain chainclients.ChainClient) bool {
	o.chainLifecycleMu.Lock()
	defer o.chainLifecycleMu.Unlock()
	if o.stopping {
		return false
	}
	if o.startedChains == nil {
		o.startedChains = make(map[common.Chain]chainclients.ChainClient)
	}
	if _, started := o.startedChains[chainID]; started {
		return false
	}
	chain.Start(o.globalTxsQueue, o.globalErrataQueue, o.globalSolvencyQueue, o.globalNetworkFeeQueue)
	o.startedChains[chainID] = chain
	return true
}

func (o *Observer) startConfiguredChainClients(deferXMR bool) {
	for chainID, chain := range o.chains {
		if deferXMR && chainID.Equals(common.XMRChain) {
			continue
		}
		o.startChainClient(chainID, chain)
	}
}

// IsChainClientStarted reports completed startup, never readiness during stop.
func (o *Observer) IsChainClientStarted(chainID common.Chain) bool {
	if o == nil {
		return false
	}
	o.chainLifecycleMu.Lock()
	defer o.chainLifecycleMu.Unlock()
	_, started := o.startedChains[chainID]
	return started && !o.stopping
}

// startRestoredChainClients validates shared history before starting any chain.
// Only XMR waits for its historical quarantine writes and voter reconciliation.
func (o *Observer) startRestoredChainClients() (bool, error) {
	tombstones, err := o.storage.getErrataTombstones()
	if err != nil {
		return false, fmt.Errorf("load observer errata startup state: %w", err)
	}
	deferXMR := false
	for _, tombstone := range tombstones {
		if tombstone.Chain.Equals(common.XMRChain) && tombstone.Committed {
			deferXMR = true
			break
		}
	}
	o.startConfiguredChainClients(deferXMR)
	return deferXMR, nil
}

func (o *Observer) startedChainClientSnapshot() []chainclients.ChainClient {
	o.chainLifecycleMu.Lock()
	defer o.chainLifecycleMu.Unlock()
	started := make([]chainclients.ChainClient, 0, len(o.startedChains))
	for _, chain := range o.startedChains {
		started = append(started, chain)
	}
	return started
}

func (o *Observer) stopStartedChainClients() {
	o.chainLifecycleMu.Lock()
	if o.stopping {
		o.chainLifecycleMu.Unlock()
		return
	}
	o.stopping = true
	if o.stopChan != nil {
		close(o.stopChan)
	}
	started := make([]chainclients.ChainClient, 0, len(o.startedChains))
	var xmrClient chainclients.ChainClient
	for chainID, chain := range o.startedChains {
		if chainID.Equals(common.XMRChain) {
			xmrClient = chain
			continue
		}
		started = append(started, chain)
	}
	o.chainLifecycleMu.Unlock()

	for _, chain := range started {
		chain.Stop()
	}
	if xmrClient != nil {
		// Maintenance may still use the XMR client. Let other chains stop first,
		// then join without holding the lifecycle lock before stopping XMR.
		if o.wg != nil {
			o.wg.Wait()
		}
		xmrClient.Stop()
	}
}

// maintainXMRErrata restores startup quarantine once, then owns ongoing cleanup.
// Durable unresolved receipts survive missed ticks, failures and restart.
func (o *Observer) maintainXMRErrata(ctx context.Context, restoreQuarantines bool) {
	ticker := time.NewTicker(constants.ThorchainBlockTime)
	defer ticker.Stop()
	for {
		select {
		case <-o.stopChan:
			return
		case <-ctx.Done():
			return
		default:
		}
		if restoreQuarantines {
			if err := o.replayPermanentErrataQuarantines(); err != nil {
				if !errors.Is(err, context.Canceled) {
					o.logger.Warn().Err(err).Msg("XMR startup quarantine restoration remains pending")
				}
			} else {
				restoreQuarantines = false
			}
		}
		if !restoreQuarantines {
			o.maintainXMRErrataOnce(ctx)
		}
		select {
		case <-o.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Observer) startXMRErrataMaintenance(ctx context.Context, restoreQuarantines bool) {
	o.chainLifecycleMu.Lock()
	defer o.chainLifecycleMu.Unlock()
	if o.stopping || o.chains[common.XMRChain] == nil {
		return
	}
	// Register before shutdown can close stopChan and wait for maintenance.
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.maintainXMRErrata(ctx, restoreQuarantines)
	}()
}

func (o *Observer) maintainXMRErrataOnce(ctx context.Context) {
	unresolved, err := o.reconcileCommittedXMRErrata()
	if err != nil {
		o.logger.Warn().Err(err).Int("unresolved", unresolved).
			Msg("committed XMR errata quarantine remains pending")
		return
	}
	if unresolved != 0 {
		o.logger.Debug().Int("unresolved", unresolved).
			Msg("committed XMR errata reconciliation remains pending")
		return
	}
	chain := o.chains[common.XMRChain]
	if chain == nil || o.IsChainClientStarted(common.XMRChain) {
		return
	}
	// Recheck receipts that arrived during reconciliation before starting XMR.
	// Runtime receipts still use the normal admission and broadcast fences.
	pending, err := o.storage.getUnresolvedCommittedXMRErrata()
	if err == nil && len(pending) == 0 && ctx.Err() == nil {
		o.startChainClient(common.XMRChain, chain)
	}
}

func (o *Observer) Start(ctx context.Context) error {
	if err := o.restoreDeck(); err != nil {
		return fmt.Errorf("restore observer deck: %w", err)
	}
	deferXMR, err := o.startRestoredChainClients()
	if err != nil {
		return err
	}
	o.oracle.Start()
	go o.processOracle()
	go o.processTxIns()
	go o.processErrataTx(ctx)
	go o.processSolvencyQueue(ctx)
	go o.processNetworkFeeQueue(ctx)
	go o.deck(ctx)
	go o.attestationGossip.Start(ctx)
	go o.processPriceFeedQueue(ctx)
	o.startXMRErrataMaintenance(ctx, deferXMR)
	return nil
}

// ObserveSigned is called when a tx is signed by the signer and returns an observation that should be immediately submitted.
// Observations passed to this method with 'allowFutureObservation' false will be cached in memory and skipped if they are later observed in the mempool or block.
func (o *Observer) ObserveSigned(txIn types.TxIn) {
	if !txIn.AllowFutureObservation {
		// add all transaction ids to the signed tx out cache
		o.signedTxOutCacheMu.Lock()
		for _, tx := range txIn.TxArray {
			o.signedTxOutCache.Add(tx.Tx, nil)
		}
		o.signedTxOutCacheMu.Unlock()
	}

	o.globalTxsQueue <- txIn
}

// restoreDeck initializes the memory cache with the ondeck txs from the storage
func (o *Observer) restoreDeck() error {
	onDeckTxs, err := o.storage.GetOnDeckTxs()
	if err != nil {
		return fmt.Errorf("load durable on-deck transactions: %w", err)
	}

	if o.deckDumpFile != "" {
		// dump the ondeck txs to a file for debugging
		o.logger.Info().Msgf("dumping ondeck txs to %s", o.deckDumpFile)
		dumpTxs, err := json.Marshal(onDeckTxs)
		if err != nil {
			o.logger.Error().Err(err).Msg("fail to marshal ondeck txs")
		} else {
			if err := os.WriteFile(o.deckDumpFile, dumpTxs, 0o600); err != nil {
				o.logger.Error().Err(err).Msg("fail to write ondeck txs to file")
			}
			o.logger.Info().Msgf("ondeck txs dumped to %s", o.deckDumpFile)
		}
	}

	o.lock.Lock()
	defer o.lock.Unlock()
	for _, txIn := range onDeckTxs {
		o.onDeck[TxInKey(txIn)] = txIn
	}
	return nil
}

func (o *Observer) deck(ctx context.Context) {
	for {
		select {
		case <-o.stopChan:
			o.sendDeck(ctx)
			return
		case <-time.After(deckRefreshTime):
			o.sendDeck(ctx)
		}
	}
}

// handleObservedTxCommitted will be called when an observed tx has been committed to thorchain,
// notified via AttestationGossip's grpc subscription to thornode..
func (o *Observer) handleObservedTxCommitted(tx common.ObservedTx, allowFutureObservation bool) {
	madeChanges := false

	isFinal := tx.IsFinal()

	o.lock.Lock()
	defer o.lock.Unlock()

	k := txInKey{
		chain:                  tx.Tx.Chain,
		height:                 tx.FinaliseHeight,
		allowFutureObservation: allowFutureObservation,
	}

	deck, ok := o.onDeck[k]
	if !ok {
		return
	}

	for j, txInItem := range deck.TxArray {
		if !txInItem.EqualsObservedTx(tx) {
			continue
		}
		if deck.Chain.UsesHeightScopedErrata() && !deck.MemPool && !deck.AllowFutureObservation {
			// Different mining locations can share a finalization height. A late
			// commit must not complete the replacement location or its callback.
			minedHeight, ok := canonicalTxInMinedHeight(deck, txInItem)
			committedHeight, err := tx.CanonicalMinedHeight()
			if !ok || err != nil || minedHeight != committedHeight {
				continue
			}
		}
		callbackItem := txInItem.Copy()
		if !isFinal || deck.Chain.Equals(common.XMRChain) {
			callbackItem.CommittedUnFinalised = !isFinal
		}
		chain, chainErr := o.getChain(deck.Chain)
		if chainErr != nil {
			o.logger.Error().Err(chainErr).Msg("chain not found")
		}
		durableCommitter, requiresDurableCommit := chain.(durableObservedTxCommitter)
		if requiresDurableCommit {
			if err := durableCommitter.OnObservedTxInDurable(*callbackItem, callbackItem.BlockHeight); err != nil {
				o.logger.Error().Err(err).
					Str("chain", deck.Chain.String()).
					Str("txid", callbackItem.Tx).
					Bool("final", isFinal).
					Msg("chain client failed to durably accept committed observation; retaining observer deck item")
				return
			}
		}
		if isFinal {
			o.logger.Debug().Msgf("tx final %s - %s removing from tx array", tx.Tx.Chain, tx.Tx.ID)
			updated := *deck
			updated.TxArray = slices.Delete(slices.Clone(deck.TxArray), j, j+1)
			if len(updated.TxArray) == 0 {
				o.logger.Debug().Msgf("deck is empty, removing from ondeck")
				if err := o.storage.RemoveTx(deck, k.height); err != nil {
					o.logger.Error().Err(err).Msg("fail to remove tx from storage")
					return
				}
				delete(o.onDeck, k)
			} else {
				if j == 0 {
					// update block confirmation count
					updated.ConfirmationRequired = k.height - updated.TxArray[0].BlockHeight
				}
				if err := o.storage.AddOrUpdateTx(&updated); err != nil {
					o.logger.Error().Err(err).Msg("fail to update tx in storage")
					return
				}
				o.onDeck[k] = &updated
			}
		} else {
			updated := *deck
			updated.TxArray = slices.Clone(deck.TxArray)
			updated.TxArray[j] = callbackItem
			if err := o.storage.AddOrUpdateTx(&updated); err != nil {
				o.logger.Error().Err(err).Msg("fail to update tx in storage")
				return
			}
			o.onDeck[k] = &updated
		}

		if chainErr == nil && !requiresDurableCommit {
			chain.OnObservedTxIn(*callbackItem, callbackItem.BlockHeight)
		}

		madeChanges = true
		break
	}

	if !madeChanges {
		o.logger.Debug().Msgf("no changes made to ondeck, size: %d", len(o.onDeck))
		return
	}

	o.logger.Debug().
		Int("ondeck_size", len(o.onDeck)).
		Str("id", tx.Tx.ID.String()).
		Str("chain", tx.Tx.Chain.String()).
		Int64("height", tx.BlockHeight).
		Str("from", tx.Tx.FromAddress.String()).
		Str("to", tx.Tx.ToAddress.String()).
		Str("memo", tx.Tx.Memo).
		Str("coins", tx.Tx.Coins.String()).
		Str("gas", common.Coins(tx.Tx.Gas).String()).
		Str("observed_vault_pubkey", tx.ObservedPubKey.String()).
		Msg("observed tx committed to thorchain")
}

// handleErrataTxCommitted retires this node's attestation only after a durable
// terminal receipt. REVERTED still needs retryable local quarantine; a no-applicable
// result describes processing time, while the TxID-wide on-chain errata continues
// to protect accounting if an inbound observation reaches quorum later.
func (o *Observer) handleErrataTxCommitted(errata common.ErrataTx, outcome common.ErrataTxOutcome) error {
	if errata.Chain.RequiresTerminalErrataOutcome() {
		switch outcome {
		case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED:
			return nil
		case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED,
			common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION:
		default:
			return fmt.Errorf("unknown committed XMR errata outcome %d", outcome)
		}
	}

	if errata.Chain.Equals(common.XMRChain) && outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED {
		if err := o.blockPermanentErrataBroadcast(errata); err != nil {
			return err
		}
	}

	o.lock.Lock()
	err := o.storage.recordLocalErrataCommitted(errata, outcome)
	o.lock.Unlock()
	if err != nil {
		o.logger.Error().Err(err).
			Str("chain", errata.Chain.String()).Str("txid", errata.Id.String()).
			Msg("fail to persist delivered errata receipt; outbox will remain pending")
		return err
	}
	// Broad quarantine belongs to maintenance; the receipt is its durable retry.
	return nil
}

func (o *Observer) blockPermanentErrataBroadcast(errata common.ErrataTx) error {
	if o.permanentErrataBroadcastBlockFn != nil {
		return o.permanentErrataBroadcastBlockFn(errata)
	}
	quarantiner, ok := o.chains[errata.Chain].(chainclients.PermanentErrataQuarantiner)
	if !ok {
		return fmt.Errorf("%s chain client lacks permanent errata broadcast blocking support", errata.Chain)
	}
	return quarantiner.BlockPermanentErrataBroadcast(errata)
}

func (o *Observer) quarantinePermanentErrata(tombstone errataTombstone) error {
	errata := common.ErrataTx{Chain: tombstone.Chain, Id: tombstone.TxID}
	if o.permanentErrataQuarantineFn != nil {
		return o.permanentErrataQuarantineFn(errata, tombstone.OrphanHeight)
	}
	chain, ok := o.chains[tombstone.Chain]
	if !ok || chain == nil {
		return fmt.Errorf("%s chain client is unavailable for permanent errata quarantine", tombstone.Chain)
	}
	quarantiner, ok := chain.(chainclients.PermanentErrataQuarantiner)
	if !ok {
		return fmt.Errorf("%s chain client lacks permanent errata quarantine support", tombstone.Chain)
	}
	return quarantiner.QuarantinePermanentErrata(errata, tombstone.OrphanHeight)
}

func (o *Observer) replayPermanentErrataQuarantines() error {
	tombstones, err := o.storage.getErrataTombstones()
	if err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if !tombstone.isPermanentlyReverted() {
			continue
		}
		select {
		case <-o.stopChan:
			return context.Canceled
		default:
		}
		if err = o.quarantinePermanentErrata(tombstone); err != nil {
			return fmt.Errorf("quarantine %s transaction %s: %w", tombstone.Chain, tombstone.TxID, err)
		}
	}
	return nil
}

func (o *Observer) sendDeck(ctx context.Context) {
	// fetch and update active validator count on attestation gossip so it can calculate quorum
	activeVals, err := o.thorchainBridge.FetchActiveNodes()
	if err != nil {
		o.logger.Error().Err(err).Msg("failed to get active node count")
		return
	}
	o.attestationGossip.setActiveValidators(activeVals)

	// check if node is active
	nodeStatus, err := o.thorchainBridge.FetchNodeStatus()
	if err != nil {
		o.logger.Error().Err(err).Msg("failed to get node status")
		return
	}
	o.lastNodeStatusMu.RLock()
	lastNodeStatus := o.lastNodeStatus
	o.lastNodeStatusMu.RUnlock()
	if nodeStatus != lastNodeStatus {
		o.lastNodeStatusMu.Lock()
		o.lastNodeStatus = nodeStatus
		o.lastNodeStatusMu.Unlock()

		if nodeStatus == stypes.NodeStatus_Active {
			o.logger.Info().Msg("node is now active, will begin observation and gossip")
			if lastNodeStatus != stypes.NodeStatus_Unknown {
				// if this is not the first startup, we just churned in. rollback block scanners to re-observe recent blocks
				for _, chain := range o.startedChainClientSnapshot() {
					if err := chain.RollbackBlockScanner(); err != nil {
						o.logger.Error().Err(err).Msg("fail to rollback chain")
					}
				}
			}
		}
	}
	o.attestationGossip.tickAttestationStateSync(ctx,
		nodeStatus == stypes.NodeStatus_Active,
		nodeStatus == stypes.NodeStatus_Active && nodeStatus != lastNodeStatus, time.Now())
	if nodeStatus != stypes.NodeStatus_Active {
		o.logger.Debug().Msg("node is not active, will not handle tx in")
		return
	}

	type preparedObservations struct {
		txs         common.ObservedTxs
		allowFuture bool
	}
	var prepared []preparedObservations
	o.lock.Lock()
	for k, deck := range o.onDeck {
		chainClient, err := o.getChain(deck.Chain)
		if err != nil {
			o.logger.Error().Err(err).Msg("fail to retrieve chain client")
			continue
		}
		final := chainClient.ConfirmationCountReady(*deck) && !deck.MemPool
		// Conversion owns the outgoing fields. Apply positional errors while
		// the source deck is locked, leaving its callbacks on the original items.
		txs, invalid, err := o.getThorchainTxIns(deck, final, k.height)
		if err != nil {
			o.logger.Error().Err(err).Msg("fail to convert txin to thorchain txins")
			continue
		}
		o.removeInvalidDeckItemsLocked(k, deck, invalid)
		if len(txs) > 0 {
			if deck.Chain.Equals(common.XMRChain) {
				prepared = append(prepared, preparedObservations{txs, deck.AllowFutureObservation})
			} else {
				// Other chains retain submission serialized with TxID-wide
				// cleanup; they do not have XMR's durable admission fence.
				o.sendObservedTxs(txs, deck.AllowFutureObservation)
			}
		}
	}
	o.lock.Unlock()

	// XMR submission takes the errata reader. Release the deck before network
	// work to preserve cleanup's barrier -> deck lock order.
	for _, work := range prepared {
		o.sendObservedTxs(work.txs, work.allowFuture)
	}
}

// removeInvalidDeckItemsLocked applies conversion failures under the deck lock.
func (o *Observer) removeInvalidDeckItemsLocked(k txInKey, deck *types.TxIn, indices []int) {
	if len(indices) == 0 {
		return
	}
	slices.Sort(indices)
	indices = slices.Compact(indices)
	slices.Reverse(indices)
	firstRemoved := false
	for _, idx := range indices {
		if idx < 0 || idx >= len(deck.TxArray) {
			continue
		}
		firstRemoved = firstRemoved || idx == 0
		deck.TxArray = slices.Delete(deck.TxArray, idx, idx+1)
	}
	if len(deck.TxArray) == 0 {
		delete(o.onDeck, k)
		if err := o.storage.RemoveTx(deck, k.height); err != nil {
			o.logger.Error().Err(err).Msg("fail to remove tx from storage")
		}
		return
	}
	if firstRemoved {
		deck.ConfirmationRequired = k.height - deck.TxArray[0].BlockHeight
	}
	if err := o.storage.AddOrUpdateTx(deck); err != nil {
		o.logger.Error().Err(err).Msg("fail to update tx in storage")
	}
}

func (o *Observer) sendObservedTxs(txs common.ObservedTxs, allowFutureObservation bool) {
	if len(txs) == 0 {
		return
	}
	if o.sendObservedTxsFn != nil {
		o.sendObservedTxsFn(txs, allowFutureObservation)
		return
	}

	inbound, outbound, err := o.thorchainBridge.GetInboundOutbound(txs)
	if err != nil {
		o.logger.Error().Err(err).Msg("fail to get inbound and outbound txs")
		return
	}

	for _, tx := range inbound {
		if err := o.attestationGossip.AttestObservedTx(context.Background(), &tx, true, allowFutureObservation); err != nil {
			o.logger.Err(err).Msg("fail to send inbound tx to thorchain")
		}
	}

	for _, tx := range outbound {
		if err := o.attestationGossip.AttestObservedTx(context.Background(), &tx, false, allowFutureObservation); err != nil {
			o.logger.Err(err).Msg("fail to send outbound tx to thorchain")
		}
	}
}

func (o *Observer) processTxIns() {
	// Create a worker pool with a reasonable number of workers
	// We can use runtime.NumCPU() to get the number of available CPUs
	// but let's limit the workers to avoid overwhelming the system

	// Create a semaphore to limit concurrency
	sem := make(chan struct{}, o.observerWorkers)

	for {
		select {
		case <-o.stopChan:
			// Wait for any running goroutines to complete
			for range o.observerWorkers {
				sem <- struct{}{}
			}
			return
		case txIn := <-o.globalTxsQueue:
			if len(txIn.TxArray) == 0 {
				rejectObservedTx(txIn.TxArray, errObservedTxEmpty)
				ackTxInPersisted(txIn, nil)
				continue
			}

			o.lastNodeStatusMu.RLock()
			lastNodeStatus := o.lastNodeStatus
			o.lastNodeStatusMu.RUnlock()

			if txIn.Chain.Equals(common.XMRChain) && lastNodeStatus == stypes.NodeStatus_Unknown {
				err := errors.New("XMR observer node status is not known yet")
				rejectObservedTx(txIn.TxArray, err)
				ackTxInPersisted(txIn, err)
				continue
			}

			process := func(tx types.TxIn) error {
				return o.processObservedTx(tx)
			}
			operation := "processObservedTx"
			if lastNodeStatus != stypes.NodeStatus_Active {
				if txIn.Chain.Equals(common.XMRChain) {
					// Churned-out members do not produce ordinary observations, but
					// they may still provide an exact signed source-vault certificate
					// for an outbound from a retiring vault they historically owned.
					// This path performs network I/O, so run it in the same bounded pool
					// as active observation processing instead of blocking this queue.
					process = func(tx types.TxIn) error {
						return o.attestHistoricalXMRSpentRefCertificates(tx)
					}
					operation = "attestHistoricalXMRSpentRefCertificates"
				} else {
					rejectObservedTx(txIn.TxArray, errObservedTxInactive)
					ackTxInPersisted(txIn, nil)
					continue
				}
			}

			// Acquire a token from semaphore
			sem <- struct{}{}

			// Process observed tx in a goroutine
			go func(tx types.TxIn, process func(types.TxIn) error, operation string) {
				defer func() {
					// Release the token back to semaphore when done
					<-sem
				}()

				start := time.Now()
				err := process(tx)
				if err != nil {
					o.logger.Error().Err(err).Msgf("failed to %s", operation)
				}
				ackTxInPersisted(tx, err)
				o.logger.Debug().Msgf("%s took %s", operation, time.Since(start))
			}(txIn, process, operation)
		}
	}
}

// processObservedTx will process the observed tx, and either add it to the
// onDeck queue, or merge it with an existing tx in the onDeck queue.
func (o *Observer) processObservedTx(txIn types.TxIn) error {
	if len(txIn.TxArray) == 0 {
		rejectObservedTx(txIn.TxArray, errObservedTxEmpty)
		return nil
	}
	nonEmptyTxArray := make([]*types.TxInItem, 0, len(txIn.TxArray))
	for _, item := range txIn.TxArray {
		if item == nil || item.IsEmpty() {
			rejectObservedTx([]*types.TxInItem{item}, errObservedTxEmpty)
			continue
		}
		nonEmptyTxArray = append(nonEmptyTxArray, item)
	}
	if len(nonEmptyTxArray) == 0 {
		return nil
	}
	txIn.TxArray = nonEmptyTxArray

	// Create a new slice for filtered transactions
	var filteredTxArray []*types.TxInItem

	// Check if we need to filter the incoming transactions
	if !txIn.Filtered {
		filterStart := time.Now()
		// First, get a read lock to check existing transactions
		// Filter without modifying shared state
		for _, item := range txIn.TxArray {
			filtered := o.filterObservations(txIn.Chain, []*types.TxInItem{item}, txIn.MemPool)
			if len(filtered) == 0 {
				rejectObservedTx(
					[]*types.TxInItem{item},
					fmt.Errorf("%w: %s", errObservedTxFiltered, item.Tx),
				)
				continue
			}
			coalesceObservedTxCallbacks(item, filtered)
			filteredTxArray = append(filteredTxArray, filtered...)
		}
		if len(filteredTxArray) == 0 {
			o.logger.Debug().Msgf("txin is empty after filtering, ignore it")
			return nil
		}

		// Set the filtered flag and update TxArray
		txIn.TxArray = filteredTxArray
		txIn.Filtered = true

		// If we're creating a new deck entry, set the confirmation required
		chainClient, err := o.getChain(txIn.Chain)
		if err == nil {
			txIn.ConfirmationRequired = chainClient.GetConfirmationCount(txIn)
		} else {
			o.logger.Error().Err(err).Msg("fail to get chain client for confirmation count")
		}
		o.logger.Debug().Msgf("filterObservations took %s", time.Since(filterStart))
	}

	if o.beforeObservedTxDeckLock != nil {
		o.beforeObservedTxDeckLock()
	}

	// Tombstones must be checked while holding the same lock used by errata
	// application and onDeck mutation. This is the final barrier for a worker
	// which passed ordinary filters before the errata arrived.
	o.lock.Lock()
	accepted, errataRejected, err := o.enforceErrataTombstones(&txIn)
	if err != nil {
		callbacks := takeObservedTxRejectedCallbacks(txIn.TxArray, err)
		o.lock.Unlock()
		invokeObservedTxCallbacks(callbacks)
		return err
	}
	txIn.TxArray = accepted
	if len(txIn.TxArray) == 0 {
		o.lock.Unlock()
		invokeObservedTxCallbacks(errataRejected)
		return nil
	}
	k := TxInKey(&txIn)

	in, ok := o.onDeck[k]
	if ok {
		var persistFailure error
		var rejectedCallbacks, storedCallbacks []func()
		// tx is already in the onDeck, dedupe incoming txs
		dedupeStart := time.Now()
		var newTxs []*types.TxInItem
		var duplicateTxs []*types.TxInItem
		for _, txInItem := range txIn.TxArray {
			foundTx := false
			for _, txInItemDeck := range in.TxArray {
				if txInItemDeck.Equals(txInItem) {
					foundTx = true
					duplicateTxs = append(duplicateTxs, txInItem)
					o.logger.Warn().
						Str("id", txInItem.Tx).
						Str("chain", in.Chain.String()).
						Int64("height", txInItem.BlockHeight).
						Str("from", txInItem.Sender).
						Str("to", txInItem.To).
						Str("memo", txInItem.Memo).
						Str("coins", txInItem.Coins.String()).
						Str("gas", common.Coins(txInItem.Gas).String()).
						Str("observed_vault_pubkey", txInItem.ObservedVaultPubKey.String()).
						Msg("Dropping duplicate observation tx")
					break
				}
			}
			if !foundTx {
				newTxs = append(newTxs, txInItem)
			}
		}
		o.logger.Debug().Msgf("Dedupe took %s", time.Since(dedupeStart))
		if len(newTxs) > 0 {
			originalLen := len(in.TxArray)
			in.TxArray = append(in.TxArray, newTxs...)
			setDeckStart := time.Now()
			persistErr := o.storage.AddOrUpdateTx(in)
			if persistErr != nil {
				o.logger.Error().Err(persistErr).Msg("fail to add tx to storage")
				for i := originalLen; i < len(in.TxArray); i++ {
					in.TxArray[i] = nil
				}
				in.TxArray = in.TxArray[:originalLen]
				rejectedCallbacks = append(
					rejectedCallbacks,
					takeObservedTxRejectedCallbacks(newTxs, fmt.Errorf("persist observed transaction: %w", persistErr))...,
				)
				persistFailure = persistErr
			} else {
				storedCallbacks = append(storedCallbacks, takeObservedTxStoredCallbacks(newTxs)...)
			}
			o.logger.Debug().Msgf("AddOrUpdateTx existing took %s", time.Since(setDeckStart))
		}
		if hasObservedTxCallbacks(duplicateTxs) || txIn.Persisted != nil {
			persistErr := o.storage.addOrUpdateTxSync(in)
			if persistErr != nil {
				o.logger.Error().Err(persistErr).Msg("fail to sync duplicate observed tx in storage")
				rejectedCallbacks = append(
					rejectedCallbacks,
					takeObservedTxRejectedCallbacks(duplicateTxs, fmt.Errorf("sync duplicate observed transaction: %w", persistErr))...,
				)
				if persistFailure == nil {
					persistFailure = persistErr
				}
			} else {
				storedCallbacks = append(storedCallbacks, takeObservedTxStoredCallbacks(duplicateTxs)...)
			}
		}

		o.lock.Unlock()
		invokeObservedTxCallbacks(errataRejected)
		invokeObservedTxCallbacks(rejectedCallbacks)
		invokeObservedTxCallbacks(storedCallbacks)
		return persistFailure
	}

	setDeckStart := time.Now()
	persistErr := o.storage.AddOrUpdateTx(&txIn)
	if persistErr != nil {
		o.logger.Error().Err(persistErr).Msg("fail to add tx to storage")
		callbacks := takeObservedTxRejectedCallbacks(txIn.TxArray, fmt.Errorf("persist observed transaction: %w", persistErr))
		o.lock.Unlock()
		invokeObservedTxCallbacks(errataRejected)
		invokeObservedTxCallbacks(callbacks)
		return persistErr
	}
	o.onDeck[k] = &txIn
	callbacks := takeObservedTxStoredCallbacks(txIn.TxArray)
	o.lock.Unlock()
	invokeObservedTxCallbacks(errataRejected)
	invokeObservedTxCallbacks(callbacks)
	o.logger.Debug().Msgf("AddOrUpdateTx new took %s", time.Since(setDeckStart))
	return nil
}

func ackTxInPersisted(txIn types.TxIn, err error) {
	tryAckPersisted(txIn.Persisted, err)
}

// tryAckPersisted keeps an abandoned acknowledgement from stalling shared
// observer queues. Producers supply a buffered channel with room for one result.
func tryAckPersisted(persisted chan error, err error) bool {
	if persisted == nil {
		return true
	}
	select {
	case persisted <- err:
		return true
	default:
		return false
	}
}

// attestHistoricalXMRSpentRefCertificates converts only pre-attributed XMR
// outbounds with spent refs. AttestObservedTx applies the source-vault,
// historical-membership, exact-signature, and planned-outbound gates and routes
// the result over the certificate-only protocol when this observer is inactive.
func (o *Observer) attestHistoricalXMRSpentRefCertificates(txIn types.TxIn) error {
	if !txIn.Chain.Equals(common.XMRChain) || (txIn.MemPool && !txIn.Filtered) || len(txIn.TxArray) == 0 {
		return nil
	}

	filtered := txIn.TxArray
	if !txIn.Filtered {
		filtered = o.filterObservations(txIn.Chain, txIn.TxArray, txIn.MemPool)
	}
	certificateItems := make([]*types.TxInItem, 0, len(filtered))
	rejectedItems := make([]*types.TxInItem, 0, len(filtered))
	for _, item := range filtered {
		if item == nil || item.ObservedVaultPubKey.IsEmpty() || len(item.SpentOutputRefs) == 0 {
			rejectedItems = append(rejectedItems, item)
			continue
		}
		vaultAddress, err := item.ObservedVaultPubKey.GetAddress(common.XMRChain)
		if err != nil || vaultAddress.IsEmpty() || item.Sender != vaultAddress.String() {
			rejectedItems = append(rejectedItems, item)
			continue
		}
		certificateItems = append(certificateItems, item)
	}
	rejectObservedTx(rejectedItems, types.ErrObservedTxNotApplicable)
	if len(certificateItems) == 0 {
		return nil
	}

	certificateTxIn := txIn
	certificateTxIn.Filtered = true
	certificateTxIn.TxArray = filtered
	chainClient, err := o.getChain(common.XMRChain)
	if err != nil {
		rejectObservedTx(certificateItems, err)
		return err
	}
	// Match the ordinary observer's phase exactly. Instant signer observations
	// are already filtered with zero confirmations; overwriting that setting or
	// dropping their mempool form prevents a churned-out signing member from
	// completing the same source-vault certificate as its active peers.
	if !txIn.Filtered {
		certificateTxIn.ConfirmationRequired = chainClient.GetConfirmationCount(certificateTxIn)
	}
	finaliseHeight := filtered[0].BlockHeight + certificateTxIn.ConfirmationRequired
	finalized := chainClient.ConfirmationCountReady(certificateTxIn) && !certificateTxIn.MemPool
	certificateTxIn.TxArray = certificateItems
	observed, invalidIndices, err := o.getThorchainTxIns(&certificateTxIn, finalized, finaliseHeight)
	if err != nil {
		rejectObservedTx(certificateItems, err)
		return err
	}
	if len(invalidIndices) != 0 || len(observed) != len(certificateItems) {
		err = fmt.Errorf("historical XMR certificate contains %d invalid observation(s)", len(invalidIndices))
		rejectObservedTx(certificateItems, err)
		return err
	}
	var resultErr error
	for i := range observed {
		item := certificateItems[i]
		if err = o.attestationGossip.attestObservedTx(
			context.Background(),
			&observed[i],
			false,
			false,
			true,
		); err != nil {
			// Most pre-attributed XMR outbounds are from vaults this node never
			// controlled. They are intentionally ignored by the narrow admission
			// gate rather than turning an inactive scanner block into an error.
			o.logger.Debug().Err(err).
				Str("tx_id", observed[i].Tx.ID.String()).
				Str("vault", observed[i].ObservedPubKey.String()).
				Msg("inactive observer did not qualify for historical XMR certificate")
			rejectObservedTx([]*types.TxInItem{item}, err)
			if errors.Is(err, types.ErrObservedTxNotApplicable) {
				continue
			}
			resultErr = errors.Join(resultErr, err)
			continue
		}

		invokeObservedTxCallbacks(takeObservedTxStoredCallbacks([]*types.TxInItem{item}))
	}
	return resultErr
}

// enforceErrataTombstones runs with o.lock held. A scanner tombstone fences
// only the exact orphaned height. Only an explicitly confirmed XMR Reverted
// result is a permanent TxID-wide barrier.
func (o *Observer) enforceErrataTombstones(txIn *types.TxIn) (
	[]*types.TxInItem,
	[]func(),
	error,
) {
	accepted := make([]*types.TxInItem, 0, len(txIn.TxArray))
	orphanedItems := make([]*types.TxInItem, 0)
	permanentlyRevertedItems := make([]*types.TxInItem, 0)
	for _, item := range txIn.TxArray {
		tombstone, err := o.storage.getErrataTombstone(txIn.Chain, item.Tx)
		if err != nil {
			return nil, nil, fmt.Errorf("check observer errata tombstone: %w", err)
		}
		if tombstone == nil || !tombstone.blocksTxInItem(txIn, item) {
			accepted = append(accepted, item)
			continue
		}
		if tombstone.isPermanentlyReverted() {
			permanentlyRevertedItems = append(permanentlyRevertedItems, item)
			continue
		}
		orphanedItems = append(orphanedItems, item)
	}
	rejectedCallbacks := takeObservedTxRejectedCallbacks(orphanedItems, errObservedTxErrata)
	rejectedCallbacks = append(
		rejectedCallbacks,
		takeObservedTxRejectedCallbacks(
			permanentlyRevertedItems,
			fmt.Errorf("%w: %s transaction id is permanently reverted", types.ErrObservedTxPermanentlyReverted, txIn.Chain),
		)...,
	)
	return accepted, rejectedCallbacks, nil
}

func hasObservedTxCallbacks(items []*types.TxInItem) bool {
	for _, item := range items {
		if item != nil && (item.OnObservedTxStored != nil || item.OnObservedTxRejected != nil) {
			return true
		}
	}
	return false
}

func takeObservedTxStoredCallbacks(items []*types.TxInItem) []func() {
	callbacks := make([]func(), 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		stored := item.OnObservedTxStored
		item.OnObservedTxStored = nil
		item.OnObservedTxRejected = nil
		if stored != nil {
			callbacks = append(callbacks, stored)
		}
	}
	return callbacks
}

func takeObservedTxRejectedCallbacks(items []*types.TxInItem, err error) []func() {
	callbacks := make([]func(), 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		rejected := item.OnObservedTxRejected
		item.OnObservedTxStored = nil
		item.OnObservedTxRejected = nil
		if rejected != nil {
			callback := rejected
			callbacks = append(callbacks, func() { callback(err) })
		}
	}
	return callbacks
}

func invokeObservedTxCallbacks(callbacks []func()) {
	for _, callback := range callbacks {
		callback()
	}
}

func rejectObservedTx(items []*types.TxInItem, err error) {
	invokeObservedTxCallbacks(takeObservedTxRejectedCallbacks(items, err))
}

// coalesceObservedTxCallbacks makes copies derived from one scanner item share
// one terminal callback. Filtering can legitimately emit inbound and outbound
// views of the same transfer and the original may remain in a scanner prefetch
// cache; the handoff must still be acknowledged or rejected exactly once.
func coalesceObservedTxCallbacks(source *types.TxInItem, filtered []*types.TxInItem) {
	if source == nil ||
		(source.OnObservedTxStored == nil && source.OnObservedTxRejected == nil) {
		return
	}
	stored := source.OnObservedTxStored
	rejected := source.OnObservedTxRejected
	var once sync.Once
	var storedOnce func()
	if stored != nil {
		storedOnce = func() {
			once.Do(stored)
		}
	}
	var rejectedOnce func(error)
	if rejected != nil {
		rejectedOnce = func(err error) {
			once.Do(func() { rejected(err) })
		}
	}
	source.OnObservedTxStored = storedOnce
	source.OnObservedTxRejected = rejectedOnce
	for _, item := range filtered {
		if item == nil {
			continue
		}
		item.OnObservedTxStored = storedOnce
		item.OnObservedTxRejected = rejectedOnce
	}
}

func (o *Observer) filterObservations(chain common.Chain, items []*types.TxInItem, memPool bool) []*types.TxInItem {
	var txs []*types.TxInItem
	for _, txInItem := range items {
		var handled bool
		txs, handled = o.appendPreAttributedXMRObservation(txs, txInItem, chain, memPool)
		if handled {
			continue
		}
		if o.pubkeyMgr == nil {
			continue
		}

		// NOTE: the following could result in the same tx being added
		// twice, which is expected. We want to make sure we generate both
		// a inbound and outbound txn, if we both apply.

		isInternal := false
		// check if the from address is a valid pool
		if ok, cpi := o.pubkeyMgr.IsValidPoolAddress(txInItem.Sender, chain); ok {
			tx := txInItem.Copy()
			tx.ObservedVaultPubKey = cpi.PubKey
			isInternal = true

			// skip the outbound observation if we signed and manually observed
			o.signedTxOutCacheMu.Lock()
			hasSigned := o.signedTxOutCache.Contains(tx.Tx)
			o.signedTxOutCacheMu.Unlock()
			if !hasSigned {
				txs = append(txs, tx)
			}
		}

		// skip creating a duplicate cancel observation
		isCancel := txInItem.Sender == txInItem.To && txInItem.Memo == ""
		if isCancel {
			continue
		}

		// check if the to address is a valid pool address
		// for inbound message , if it is still in mempool , it will be ignored unless it is internal transaction
		// internal tx means both from & to addresses belongs to the network. for example migrate/consolidate
		if ok, cpi := o.pubkeyMgr.IsValidPoolAddress(txInItem.To, chain); ok && (!memPool || isInternal) {
			tx := txInItem.Copy()
			tx.ObservedVaultPubKey = cpi.PubKey
			txs = append(txs, tx)
		}
	}
	return txs
}

func (o *Observer) appendPreAttributedXMRObservation(
	txs []*types.TxInItem,
	txInItem *types.TxInItem,
	chain common.Chain,
	memPool bool,
) ([]*types.TxInItem, bool) {
	if !chain.Equals(common.XMRChain) || txInItem.ObservedVaultPubKey.IsEmpty() {
		return txs, false
	}
	vaultAddress, err := txInItem.ObservedVaultPubKey.GetAddress(chain)
	if err != nil || vaultAddress.IsEmpty() {
		return txs, false
	}

	vaultAddressString := vaultAddress.String()
	senderMatches := txInItem.Sender == vaultAddressString
	toMatches := txInItem.To == vaultAddressString
	if !senderMatches && !toMatches {
		return txs, false
	}

	if senderMatches {
		tx := txInItem.Copy()
		hasSigned := false
		if o.signedTxOutCache != nil {
			o.signedTxOutCacheMu.Lock()
			hasSigned = o.signedTxOutCache.Contains(tx.Tx)
			o.signedTxOutCacheMu.Unlock()
		}
		if !hasSigned {
			txs = append(txs, tx)
		}
	}

	// skip creating a duplicate cancel observation
	if txInItem.Sender == txInItem.To && txInItem.Memo == "" {
		return txs, true
	}

	// Inbound mempool attribution is intentionally unsupported unless the tx is
	// internal and therefore already attributable as an outbound.
	if toMatches && (!memPool || senderMatches) {
		tx := txInItem.Copy()
		txs = append(txs, tx)
	}
	return txs, true
}

func (o *Observer) processErrataTx(ctx context.Context) {
	retryTicker := time.NewTicker(constants.ThorchainBlockTime)
	pruneTicker := time.NewTicker(committedOnlyErrataPruneInterval)
	defer retryTicker.Stop()
	defer pruneTicker.Stop()
	o.pruneCommittedOnlyErrata()

	for {
		select {
		case <-o.stopChan:
			return
		case <-retryTicker.C:
			o.retryPendingErrata(ctx)
		case <-pruneTicker.C:
			o.pruneCommittedOnlyErrata()
		case errataBlock, more := <-o.globalErrataQueue:
			if !more {
				return
			}
			// A producer opts into the scanner-owned durable delivery contract by
			// supplying Persisted. This path resumes a scanner journal only after
			// observer cleanup and outbox state are both durable.
			if errataBlock.Persisted != nil {
				// Acknowledge the atomic durable cleanup before attempting gossip.
				// Outbox retries preserve delivery if the process stops before send.
				processErr := o.filterErrataTx(errataBlock)
				ackErrataPersistence(errataBlock, processErr)
				if processErr != nil {
					rejectErrataBlock(errataBlock, processErr)
					o.logger.Error().Err(processErr).
						Str("delivery_id", errataBlock.DeliveryID).
						Msg("failed to process durable errata block")
					continue
				}
				if errataBlock.OnStored != nil {
					errataBlock.OnStored()
				}
				o.logger.Info().Msgf("Received a errata block %+v from the Thorchain", errataBlock.Height)
				o.retryPendingErrata(ctx)
				continue
			}

			// Producers that opt into neither durability handshake keep the
			// original one-shot behavior: clean local state and attest once,
			// without entering the durable retry outbox.
			if errataBlock.OnStored == nil && errataBlock.OnRejected == nil {
				if processErr := o.processLegacyErrataBlock(ctx, errataBlock); processErr != nil {
					o.logger.Error().Err(processErr).
						Int64("height", errataBlock.Height).
						Msg("failed to process one-shot errata block")
				}
				continue
			}

			// filter
			if err := o.filterErrataTx(errataBlock); err != nil {
				if o.errCounter != nil {
					o.errCounter.WithLabelValues("fail_to_persist_errata_tombstone", "").Inc()
				}
				o.logger.Error().Err(err).Msg("fail to durably apply errata; skip attestation")
				rejectErrataBlock(errataBlock, err)
				continue
			}
			if errataBlock.OnStored != nil {
				errataBlock.OnStored()
			}
			o.logger.Info().Msgf("Received a errata block %+v from the Thorchain", errataBlock.Height)
			o.retryPendingErrata(ctx)
		}
	}
}

// processLegacyErrataBlock preserves the one-shot behavior used by chain
// clients that do not opt into the acknowledged XMR delivery contract. These
// events clean local observer state and attest once without entering the
// durable retry outbox.
func (o *Observer) processLegacyErrataBlock(ctx context.Context, errataBlock types.ErrataBlock) error {
	if err := o.filterLegacyErrataTx(errataBlock); err != nil {
		return err
	}
	if o.attestationGossip == nil {
		return fmt.Errorf("attestation gossip is not configured")
	}

	var broadcastErr error
	for _, errataTx := range errataBlock.Txs {
		if err := o.attestationGossip.AttestErrata(ctx, common.ErrataTx{
			Chain: errataTx.Chain,
			Id:    errataTx.TxID,
		}); err != nil {
			if broadcastErr == nil {
				broadcastErr = fmt.Errorf("failed to broadcast one-shot errata attestation for %s/%s: %w", errataTx.Chain, errataTx.TxID, err)
			}
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail_to_broadcast_errata_tx", "").Inc()
			}
			o.logger.Error().Err(err).
				Str("chain", errataTx.Chain.String()).
				Str("txid", errataTx.TxID.String()).
				Msg("fail to broadcast one-shot errata tx")
		}
	}
	return broadcastErr
}

func ackErrataPersistence(block types.ErrataBlock, err error) {
	tryAckPersisted(block.Persisted, err)
}

// rejectErrataBlock services the callback half of the local errata handoff. It
// is a no-op for producers that only supply the Persisted channel.
func rejectErrataBlock(block types.ErrataBlock, err error) {
	if block.OnRejected != nil {
		block.OnRejected(err)
	}
}

func (o *Observer) pruneCommittedOnlyErrata() {
	pruned, err := o.storage.PruneCommittedOnlyErrata(time.Now().Add(-committedOnlyErrataSafetyHorizon))
	if err != nil {
		o.logger.Error().Err(err).Msg("failed to prune committed-only errata tombstones")
		return
	}
	if pruned > 0 {
		o.logger.Info().Int("records", pruned).Msg("pruned expired committed-only errata tombstones")
	}
}

func (o *Observer) cleanupErrataBlock(errataBlock types.ErrataBlock) error {
	cleanupBlock := types.ErrataBlock{Height: errataBlock.Height, DeliveryID: errataBlock.DeliveryID}
	var legacyCleanup []types.ErrataTx
	for _, errataTx := range errataBlock.Txs {
		record, found, err := o.storage.GetPendingErrataTx(errataTx)
		if err != nil {
			return err
		}
		if found && record.DeliverySeen && !record.CleanupComplete {
			cleanupBlock.Txs = append(cleanupBlock.Txs, errataTx)
			if !errataTx.Chain.Equals(common.XMRChain) {
				legacyCleanup = append(legacyCleanup, errataTx)
			}
		}
	}
	if len(cleanupBlock.Txs) > 0 {
		// XMR tombstones, deck mutations and CleanupComplete share one fsync.
		// Older non-XMR outbox records still need their explicit completion:
		// applyErrata deliberately creates/updates outbox state only for XMR.
		if err := o.filterErrataTx(cleanupBlock); err != nil {
			return err
		}
		if len(legacyCleanup) > 0 {
			return o.storage.CompleteErrataCleanup(legacyCleanup, nil)
		}
	}
	return nil
}

func (o *Observer) broadcastErrataBlock(ctx context.Context, errataBlock types.ErrataBlock, now time.Time) error {
	o.logger.Info().Int64("height", errataBlock.Height).
		Str("delivery_id", errataBlock.DeliveryID).
		Msg("processing durable errata block")
	var broadcastErr error
	for _, errataTx := range errataBlock.Txs {
		record, found, err := o.storage.GetPendingErrataTx(errataTx)
		if err != nil {
			return err
		}
		if !found || record.Committed {
			continue
		}
		if record.AttestationRetryExhausted {
			continue
		}
		if !record.CleanupComplete {
			return fmt.Errorf("local cleanup is not durable for errata %s/%s", errataTx.Chain, errataTx.TxID)
		}
		if record.FirstAttemptAtUnixNano != 0 {
			firstAttempt := time.Unix(0, record.FirstAttemptAtUnixNano)
			if !now.Before(firstAttempt.Add(errataAttestationRetryHorizon)) {
				// A known inbound may never reach observation quorum and thus
				// never yield a terminal errata outcome. Bound automatic gossip
				// here; retain the exhausted record for operator reconciliation.
				exhausted, exhaustErr := o.storage.MarkErrataAttestationRetryExhausted(errataTx, now)
				if exhaustErr != nil {
					return exhaustErr
				}
				if exhausted {
					o.logger.Error().
						Str("chain", errataTx.Chain.String()).
						Str("txid", errataTx.TxID.String()).
						Uint32("attempts", record.AttestationAttempts).
						Msg("errata attestation retry horizon exhausted; operator must verify THORChain voter state")
					if o.errCounter != nil {
						o.errCounter.WithLabelValues("errata_retry_exhausted", errataTx.Chain.String()).Inc()
					}
				}
				continue
			}
		}
		if record.LastAttemptAtUnixNano != 0 {
			lastAttempt := time.Unix(0, record.LastAttemptAtUnixNano)
			if now.Before(lastAttempt.Add(errataAttestationRetryDelay(record.AttestationAttempts))) {
				continue
			}
		}

		attempt, recorded, attemptErr := o.storage.RecordErrataAttestationAttempt(errataTx, now)
		if attemptErr != nil {
			return attemptErr
		}
		if !recorded {
			continue
		}
		if err := o.attestErrata(ctx, common.ErrataTx{
			Chain: errataTx.Chain,
			Id:    errataTx.TxID,
		}); err != nil {
			if broadcastErr == nil {
				broadcastErr = fmt.Errorf("failed to accept local errata attestation for %s/%s: %w", errataTx.Chain, errataTx.TxID, err)
			}
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail_to_broadcast_errata_tx", "").Inc()
			}
			o.logger.Error().Err(err).
				Str("chain", errataTx.Chain.String()).
				Str("txid", errataTx.TxID.String()).
				Uint32("attempt", attempt.AttestationAttempts).
				Msg("fail to broadcast durable errata tx")
		}
	}
	return broadcastErr
}

func errataAttestationRetryDelay(attempts uint32) time.Duration {
	if attempts == 0 {
		return 0
	}
	delay := constants.ThorchainBlockTime
	for attempt := uint32(1); attempt < attempts && delay < errataAttestationRetryMaxDelay; attempt++ {
		if delay >= errataAttestationRetryMaxDelay/2 {
			return errataAttestationRetryMaxDelay
		}
		delay *= 2
	}
	if delay > errataAttestationRetryMaxDelay {
		return errataAttestationRetryMaxDelay
	}
	return delay
}

func (o *Observer) retryPendingErrata(ctx context.Context) {
	o.retryPendingErrataAt(ctx, time.Now())
}

func (o *Observer) retryPendingErrataAt(ctx context.Context, now time.Time) {
	o.errataAttestMu.Lock()
	defer o.errataAttestMu.Unlock()
	pending, err := o.storage.GetPendingErrata()
	if err != nil {
		o.logger.Error().Err(err).Msg("fail to load pending errata attestations")
		return
	}
	for _, record := range pending {
		block := types.ErrataBlock{
			Height:     record.Height,
			DeliveryID: record.DeliveryID,
			Txs:        []types.ErrataTx{record.Tx},
		}
		if err = o.cleanupErrataBlock(block); err == nil {
			err = o.broadcastErrataBlock(ctx, block, now)
		}
		if err != nil {
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail_to_broadcast_errata_tx", "").Inc()
			}
			o.logger.Error().Err(err).
				Str("chain", record.Tx.Chain.String()).Str("txid", record.Tx.TxID.String()).
				Str("delivery_id", record.DeliveryID).
				Msg("fail to broadcast durable pending errata; will retry")
		}
	}
}

// reconcileCommittedXMRErrata promotes only an explicit on-chain Reverted
// result into XMR's permanent local quarantine. Delivery and outcome are
// separate durable states so a crash or an early post-handler notification
// cannot turn a recoverable canonical re-mining into a permanent rejection.
func (o *Observer) reconcileCommittedXMRErrata() (int, error) {
	o.errataOutcomeMu.Lock()
	defer o.errataOutcomeMu.Unlock()

	pending, err := o.storage.getUnresolvedCommittedXMRErrata()
	if err != nil {
		return 0, fmt.Errorf("load unresolved committed XMR errata: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}
	select {
	case <-o.stopChan:
		return len(pending), nil
	default:
	}
	// Only pre-outcome durable receipts need REST confirmation. New terminal
	// outcomes already carry THORNode's result and must not wait for a query.
	var bridge errataOutcomeBridge
	var currentHeight int64
	var heightErr error
	for _, tombstone := range pending {
		if tombstone.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED {
			continue
		}
		bridge, _ = o.thorchainBridge.(errataOutcomeBridge)
		if bridge == nil {
			heightErr = errors.New("thorchain bridge lacks uncached XMR errata confirmation support")
		} else {
			currentHeight, heightErr = bridge.GetBlockHeightUncached()
			if heightErr == nil && (currentHeight < 0 || currentHeight == math.MaxInt64) {
				heightErr = fmt.Errorf("thornode returned invalid height %d for XMR errata confirmation", currentHeight)
			}
		}
		break
	}

	unresolved := 0
	reconcileErrors := make([]error, 0)
	for index, tombstone := range pending {
		// Finish an in-flight operation, but leave unstarted records durable for
		// restart instead of extending shutdown by the entire history backlog.
		select {
		case <-o.stopChan:
			return unresolved + len(pending) - index, errors.Join(reconcileErrors...)
		default:
		}
		errata := common.ErrataTx{Chain: tombstone.Chain, Id: tombstone.TxID}
		if tombstone.Outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED {
			if bridge == nil {
				unresolved++
				reconcileErrors = append(reconcileErrors, heightErr)
				continue
			}
			if tombstone.VerifyAfterHeight == 0 && heightErr == nil {
				verifyAfterHeight := currentHeight + 1
				o.lock.Lock()
				err = o.storage.recordErrataCommitted(errata, verifyAfterHeight)
				o.lock.Unlock()
				if err != nil {
					unresolved++
					reconcileErrors = append(reconcileErrors, fmt.Errorf(
						"persist XMR errata verification barrier for %s: %w", tombstone.TxID, err,
					))
					continue
				}
				tombstone.VerifyAfterHeight = verifyAfterHeight
			}

			voter, found, voterErr := bridge.GetObservedTxVoterUncached(tombstone.TxID.String())
			if voterErr != nil {
				unresolved++
				reconcileErrors = append(reconcileErrors, fmt.Errorf(
					"query uncached XMR errata voter %s: %w", tombstone.TxID, voterErr,
				))
				o.logger.Warn().Err(voterErr).
					Str("txid", tombstone.TxID.String()).
					Msg("XMR errata voter outcome is not queryable yet; will retry")
				continue
			}
			if found && (voter == nil || voter.TxID.IsEmpty() || !voter.TxID.Equals(tombstone.TxID)) {
				unresolved++
				mismatchErr := errors.New("uncached XMR errata voter response does not match the requested transaction")
				reconcileErrors = append(reconcileErrors, fmt.Errorf("%s: %w", tombstone.TxID, mismatchErr))
				actualTxID := ""
				if voter != nil {
					actualTxID = voter.TxID.String()
				}
				o.logger.Error().
					Str("expected_txid", tombstone.TxID.String()).
					Str("actual_txid", actualTxID).
					Msg("ignore mismatched XMR errata voter response")
				continue
			}

			// Reverted is monotonic and may be acted on immediately. A false or absent
			// ObservedTxVoter is not a durable terminal: the committed event may have
			// represented a partial errata batch and later votes can still reach
			// consensus. Keep the outcome re-checkable after the height barrier.
			if !found || !voter.Reverted {
				if heightErr != nil || currentHeight < tombstone.VerifyAfterHeight {
					unresolved++
					if heightErr != nil {
						reconcileErrors = append(reconcileErrors, fmt.Errorf(
							"query uncached Thornode height for %s: %w", tombstone.TxID, heightErr,
						))
					}
					continue
				}
				unresolved++
				continue
			}
		}

		// New REVERTED receipts already block admission and the exact artifact.
		// Legacy receipts still establish both stores here. Completion follows
		// successful broad quarantine so partial writes remain retryable.
		if err = o.quarantinePermanentErrata(tombstone); err != nil {
			unresolved++
			reconcileErrors = append(reconcileErrors, fmt.Errorf(
				"quarantine permanently reverted XMR transaction %s: %w", tombstone.TxID, err,
			))
			o.logger.Error().Err(err).Str("txid", tombstone.TxID.String()).
				Msg("fail to quarantine explicitly reverted XMR transaction; will retry")
			continue
		}

		unlock := o.lockXMRErrataCleanup(types.ErrataBlock{Txs: []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}}})
		o.lock.Lock()
		errataIDs := map[string]struct{}{
			errataObservationKey(tombstone.Chain, tombstone.TxID.String()): {},
		}
		mutations, updated, removedItems := o.collectErrataDeckMutations(errataIDs, 0, true)
		err = o.storage.recordErrataVoterResult(errata, true, mutations)
		if err == nil {
			o.applyErrataDeckUpdates(updated)
		}
		o.lock.Unlock()
		if err == nil && o.attestationGossip != nil {
			o.attestationGossip.removeObservedTxsForErrata(errata, 0)
		}
		unlock()
		if err != nil {
			unresolved++
			reconcileErrors = append(reconcileErrors, fmt.Errorf(
				"persist permanent XMR errata outcome for %s: %w", tombstone.TxID, err,
			))
			o.logger.Error().Err(err).Str("txid", tombstone.TxID.String()).
				Msg("fail to persist permanent XMR errata outcome; will retry")
			continue
		}
		callbacks := takeObservedTxRejectedCallbacks(
			removedItems,
			fmt.Errorf("%w: %s transaction id is permanently reverted", types.ErrObservedTxPermanentlyReverted, tombstone.Chain),
		)
		invokeObservedTxCallbacks(callbacks)
	}
	return unresolved, errors.Join(reconcileErrors...)
}

func (o *Observer) attestErrata(ctx context.Context, errata common.ErrataTx) error {
	if o.errataAttestFn != nil {
		return o.errataAttestFn(ctx, errata)
	}
	if o.attestationGossip == nil {
		return errors.New("errata attestation gossip is unavailable")
	}
	return o.attestationGossip.AttestErrata(ctx, errata)
}

// filterErrataTx with confirmation counting logic in place, all inbound tx to asgard will be parked and waiting for confirmation count to reach
// the target threshold before it get forward to THORChain,  it is possible that when a re-org happened on BTC / ETH
// the transaction that has been re-org out ,still in bifrost memory waiting for confirmation, as such, it should be
// removed from ondeck tx queue, and not forward it to THORChain
func (o *Observer) filterErrataTx(block types.ErrataBlock) error {
	return o.filterErrataTxWithOutbox(block, true)
}

func (o *Observer) filterLegacyErrataTx(block types.ErrataBlock) error {
	return o.filterErrataTxWithOutbox(block, false)
}

func (o *Observer) filterErrataTxWithOutbox(block types.ErrataBlock, durableOutbox bool) error {
	// Cleanup wins either before an observation is admitted, or after its
	// protected send functions return. Transport-queued requests may arrive later.
	// Shared locks and mux teardown can delay cleanup and scanner acknowledgement;
	// retained data and the durable outbox support recovery through retries.
	// Never acquire this writer while holding o.lock.
	unlock := o.lockXMRErrataCleanup(block)
	o.lock.Lock()
	if len(block.Txs) == 0 {
		o.lock.Unlock()
		unlock()
		return nil
	}

	errataIDs := make(map[string]struct{}, len(block.Txs))
	for _, tx := range block.Txs {
		key := errataObservationKey(tx.Chain, tx.TxID.String())
		errataIDs[key] = struct{}{}
	}

	mutations, updated, removedItems := o.collectErrataDeckMutations(errataIDs, block.Height, false)

	var persistErr error
	if durableOutbox {
		persistErr = o.storage.applyErrata(block, mutations)
	} else {
		persistErr = o.storage.applyLegacyErrata(block, mutations)
	}
	if persistErr != nil {
		o.lock.Unlock()
		unlock()
		return persistErr
	}
	o.applyErrataDeckUpdates(updated)
	callbacks := takeObservedTxRejectedCallbacks(removedItems, errObservedTxErrata)
	o.lock.Unlock()
	if o.attestationGossip != nil {
		for _, tx := range block.Txs {
			if tx.Chain.Equals(common.XMRChain) {
				o.attestationGossip.removeObservedTxsForErrata(common.ErrataTx{Chain: tx.Chain, Id: tx.TxID}, block.Height)
			}
		}
	}
	unlock()
	// Callbacks may re-enter observation delivery and must run outside both locks.
	invokeObservedTxCallbacks(callbacks)
	for _, tx := range block.Txs {
		o.logger.Info().Str("chain", tx.Chain.String()).
			Str("txid", tx.TxID.String()).
			Msg("durably removed on-deck transaction due to errata")
	}
	return nil
}

func (o *Observer) lockXMRErrataCleanup(block types.ErrataBlock) func() {
	if o.attestationGossip != nil {
		for _, tx := range block.Txs {
			if tx.Chain.Equals(common.XMRChain) {
				o.attestationGossip.xmrErrataBarrier.Lock()
				return o.attestationGossip.xmrErrataBarrier.Unlock
			}
		}
	}
	return func() {}
}

// collectErrataDeckMutations runs with o.lock held and describes the exact
// persisted/in-memory deck changes without mutating either. This lets storage
// fsync the TxID barrier and deck changes as one atomic LevelDB batch.
func (o *Observer) collectErrataDeckMutations(
	errataIDs map[string]struct{},
	orphanHeight int64,
	allHeights bool,
) (
	[]errataDeckMutation,
	map[txInKey]*types.TxIn,
	[]*types.TxInItem,
) {
	mutations := make([]errataDeckMutation, 0)
	updated := make(map[txInKey]*types.TxIn)
	removedItems := make([]*types.TxInItem, 0)
	for key, txIn := range o.onDeck {
		if txIn == nil || len(txIn.TxArray) == 0 {
			continue
		}
		kept := make([]*types.TxInItem, 0, len(txIn.TxArray))
		for _, item := range txIn.TxArray {
			if item == nil {
				continue
			}
			_, matchingErrata := errataIDs[errataObservationKey(txIn.Chain, item.Tx)]
			// Height scoping is XMR-only. XMR pairs a height-scoped primary path
			// with the TxID-wide tombstone backstop in reconcileCommittedXMRErrata,
			// so a view re-mined at another height must survive the orphan-height
			// removal. Every other chain has no such backstop: scoping their
			// removal to the errata height would strand deck views recorded at a
			// different height than the errata reports (a mempool observation is
			// stamped with the scan cursor, not the mined height), leaving the
			// observer re-attesting a view of a reverted TxID. Those chains keep
			// the TxID-wide removal.
			heightScoped := !allHeights && txIn.Chain.UsesHeightScopedErrata()
			minedHeight, hasMinedHeight := canonicalTxInMinedHeight(txIn, item)
			if !matchingErrata || (heightScoped && hasMinedHeight && minedHeight != orphanHeight) {
				kept = append(kept, item)
				continue
			}
			o.logger.Info().Str("txid", item.Tx).Msg("drop on-deck observation view due to errata")
			removedItems = append(removedItems, item)
		}
		if len(kept) == len(txIn.TxArray) {
			continue
		}
		if len(kept) == 0 {
			mutations = append(mutations, errataDeckMutation{key: key})
			updated[key] = nil
			continue
		}
		copyTxIn := *txIn
		copyTxIn.TxArray = kept
		// Preserve the exact original finalization/deck key even when the first
		// observation view was removed.
		copyTxIn.ConfirmationRequired = key.height - kept[0].BlockHeight
		mutations = append(mutations, errataDeckMutation{key: key, txIn: &copyTxIn})
		updated[key] = &copyTxIn
	}
	return mutations, updated, removedItems
}

// applyErrataDeckUpdates runs with o.lock held after the atomic storage batch
// succeeds, bringing the in-memory deck to the same state.
func (o *Observer) applyErrataDeckUpdates(updated map[txInKey]*types.TxIn) {
	for key, txIn := range updated {
		if txIn == nil {
			delete(o.onDeck, key)
		} else {
			o.onDeck[key] = txIn
		}
	}
}

// errataObservationKey is an in-process match key only. It normalizes both the
// chain and the transaction id so a case or whitespace difference between a
// scanner observation and an errata record cannot bypass the barrier.
func errataObservationKey(chain common.Chain, txID string) string {
	return strings.ToLower(strings.TrimSpace(chain.String())) + "\x00" + strings.ToLower(strings.TrimSpace(txID))
}

// getThorchainTxIns convert to the type thorchain expected
// maybe in later THORNode can just refactor this to use the type in thorchain
func (o *Observer) getThorchainTxIns(txIn *types.TxIn, finalized bool, finaliseHeight int64) (common.ObservedTxs, []int, error) {
	obsTxs := make(common.ObservedTxs, 0, len(txIn.TxArray))
	invalidIndices := make([]int, 0)
	o.logger.Debug().Msgf("len %d", len(txIn.TxArray))
	for i, item := range txIn.TxArray {
		if item.CommittedUnFinalised && !finalized {
			// we have already committed this tx in the un-finalized state,
			// and the tx is not yet final, so we should not send it again.
			continue
		}
		isInvalid := false
		if item.Coins.IsEmpty() {
			o.logger.Info().Msgf("item(%+v) , coins are empty , so ignore", item)
			isInvalid = true
		}
		if len([]byte(item.Memo)) > constants.MaxMemoSize {
			o.logger.Info().Msgf("tx (%s) memo (%s) too long", item.Tx, item.Memo)
			isInvalid = true
		}

		if len(item.To) == 0 {
			o.logger.Info().Msgf("tx (%s) to address is empty,ignore it", item.Tx)
			isInvalid = true
		}
		if isInvalid {
			invalidIndices = append(invalidIndices, i)
			continue
		}
		o.logger.Debug().Str("tx-hash", item.Tx).Msg("txInItem")
		blockHeight := strconv.FormatInt(item.BlockHeight, 10)
		txID, err := common.NewTxID(item.Tx)
		if err != nil {
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail_to_parse_tx_hash", blockHeight).Inc()
			}
			o.logger.Err(err).Msgf("fail to parse tx hash, %s is invalid", item.Tx)
			invalidIndices = append(invalidIndices, i)
			continue
		}
		sender, err := common.NewAddress(item.Sender)
		if err != nil {
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail_to_parse_sender", item.Sender).Inc()
			}
			// log the error , and ignore the transaction, since the address is not valid
			o.logger.Err(err).Msgf("fail to parse sender, %s is invalid sender address", item.Sender)
			invalidIndices = append(invalidIndices, i)
			continue
		}

		to, err := common.NewAddress(item.To)
		if err != nil {
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail_to_parse_to", item.To).Inc()
			}
			o.logger.Err(err).Msgf("fail to parse to, %s is invalid to address", item.To)
			invalidIndices = append(invalidIndices, i)
			continue
		}

		o.logger.Debug().Msgf("pool pubkey %s", item.ObservedVaultPubKey)
		chainAddr, err := item.ObservedVaultPubKey.GetAddress(txIn.Chain)
		o.logger.Debug().Msgf("%s address %s", txIn.Chain.String(), chainAddr)
		if err != nil {
			if o.errCounter != nil {
				o.errCounter.WithLabelValues("fail to parse observed pool address", item.ObservedVaultPubKey.String()).Inc()
			}
			o.logger.Err(err).Msgf("fail to parse observed pool address: %s", item.ObservedVaultPubKey.String())
			invalidIndices = append(invalidIndices, i)
			continue
		}
		height := item.BlockHeight
		if finalized {
			height = finaliseHeight
		}
		// Strip out any empty Coin from Coins and Gas, as even one empty Coin will make a MsgObservedTxIn for instance fail validation.
		tx := common.NewTx(txID, sender, to, item.Coins.NoneEmpty(), item.Gas.NoneEmpty(), item.Memo)
		// NoneEmpty allocates slices but retains Uint backing values. Prepared
		// observations outlive the deck lock, so copy those values as well.
		for i := range tx.Coins {
			tx.Coins[i].Amount = cosmos.NewUintFromBigInt(tx.Coins[i].Amount.BigInt())
		}
		for i := range tx.Gas {
			tx.Gas[i].Amount = cosmos.NewUintFromBigInt(tx.Gas[i].Amount.BigInt())
		}
		obsTx := common.NewObservedTx(tx, height, item.ObservedVaultPubKey, finaliseHeight)
		obsTx.KeysignMs = o.tssKeysignMetricMgr.GetTssKeysignMetric(item.Tx)
		obsTx.Aggregator = item.Aggregator
		obsTx.AggregatorTarget = item.AggregatorTarget
		if item.AggregatorTargetLimit != nil {
			limit := *item.AggregatorTargetLimit
			if !limit.IsNil() {
				limit = cosmos.NewUintFromBigInt(limit.BigInt())
			}
			obsTx.AggregatorTargetLimit = &limit
		}
		obsTx.SpentOutputRefs = common.OutputRefsFromPointers(item.SpentOutputRefs)
		obsTx.XmrTxKey = item.TxKey
		if item.SolNonce != nil {
			sn := *item.SolNonce
			obsTx.SolNonce = &sn
		}
		if txIn.Chain.UsesHeightScopedErrata() {
			if minedHeight, ok := canonicalTxInMinedHeight(txIn, item); ok {
				obsTx.XmrMinedHeight = minedHeight
			}
		}
		obsTxs = append(obsTxs, obsTx)
	}
	return obsTxs, invalidIndices, nil
}

func (o *Observer) processSolvencyQueue(ctx context.Context) {
	for {
		select {
		case <-o.stopChan:
			return
		case solvencyItem, more := <-o.globalSolvencyQueue:
			if !more {
				return
			}
			if solvencyItem.Chain.IsEmpty() || solvencyItem.PubKey.IsEmpty() {
				continue
			}
			o.logger.Debug().Msgf("solvency:%+v", solvencyItem)
			if err := o.attestationGossip.AttestSolvency(ctx, common.Solvency{
				Chain:  solvencyItem.Chain,
				Height: solvencyItem.Height,
				PubKey: solvencyItem.PubKey,
				Coins:  solvencyItem.Coins,
			}); err != nil {
				o.errCounter.WithLabelValues("fail_to_broadcast_solvency", "").Inc()
				o.logger.Error().Err(err).Msg("fail to broadcast solvency tx")
			}
		}
	}
}

func (o *Observer) processNetworkFeeQueue(ctx context.Context) {
	for {
		select {
		case <-o.stopChan:
			return
		case networkFee := <-o.globalNetworkFeeQueue:
			if err := networkFee.Valid(); err != nil {
				o.logger.Error().Err(err).Msgf("invalid network fee - %s", networkFee.String())
				continue
			}
			if err := o.attestationGossip.AttestNetworkFee(ctx, networkFee); err != nil {
				o.logger.Err(err).Msg("fail to send network fee to thorchain")
			}
		}
	}
}

func (o *Observer) processPriceFeedQueue(ctx context.Context) {
	for {
		select {
		case <-o.stopChan:
			return
		case priceFeed := <-o.globalPriceFeedQueue:
			if err := priceFeed.Valid(); err != nil {
				o.logger.Warn().Err(err).Msg("invalid price feed")
				continue
			}
			if err := o.attestationGossip.AttestPriceFeed(ctx, priceFeed); err != nil {
				if err.Error() != "skipping attest price feed: not active" {
					o.logger.Err(err).Msg("fail to send price feed")
				}
			}
		}
	}
}

// Stop the observer
func (o *Observer) Stop() error {
	if o.attestationGossip != nil {
		o.attestationGossip.stopStartupStateSync()
	}
	o.logger.Debug().Msg("request to stop observer")
	defer o.logger.Debug().Msg("observer stopped")

	o.stopStartedChainClients()
	// Startup restoration may still be running without a started XMR client.
	// Join it before closing the Observer database it uses.
	o.wg.Wait()
	o.oracle.Stop()
	if err := o.pubkeyMgr.Stop(); err != nil {
		o.logger.Error().Err(err).Msg("fail to stop pool address manager")
	}
	if err := o.storage.Close(); err != nil {
		o.logger.Err(err).Msg("fail to close observer storage")
	}

	return o.m.Stop()
}

func (o *Observer) processOracle() {
	updateInterval, haltOracle := o.getOracleMimirs()

	go func() {
		ticker := time.NewTicker(updateInterval)
		defer ticker.Stop()

		for {
			select {
			case <-o.stopChan:
				return
			case <-ticker.C:
				if !haltOracle {
					prices, version := o.oracle.GetPrices()
					if len(prices) == 0 {
						continue
					}

					pf := common.PriceFeed{
						Version: version,
						Time:    time.Now().UnixMilli(),
						Rates:   prices,
					}

					o.globalPriceFeedQueue <- pf
				}

				var newInterval time.Duration
				newInterval, haltOracle = o.getOracleMimirs()
				if newInterval != updateInterval {
					updateInterval = newInterval
					ticker.Stop()
					ticker = time.NewTicker(updateInterval)
				}
			}
		}
	}()
}

func (o *Observer) getOracleMimirs() (time.Duration, bool) {
	interval := defaultOracleUpdateInterval
	halt := false

	mimirs := []constants.ConstantName{
		constants.HaltOracle,
		constants.OracleUpdateInterval,
	}

	for _, mimir := range mimirs {
		value, err := o.thorchainBridge.GetMimir(mimir.String())
		if err != nil {
			o.logger.
				Warn().
				Err(err).
				Msgf("fail to get mimir: %s", mimir.String())
			continue
		}

		if value <= 0 {
			continue
		}

		switch mimir {
		case constants.HaltOracle:
			halt = true
		case constants.OracleUpdateInterval:
			interval = time.Duration(value) * time.Millisecond
		default:
		}
	}

	return interval, halt
}
