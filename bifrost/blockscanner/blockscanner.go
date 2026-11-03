package blockscanner

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// BlockScannerFetcher define the methods a block scanner need to implement
type BlockScannerFetcher interface {
	// FetchMemPool scan the mempool
	FetchMemPool(height int64) (types.TxIn, error)
	// FetchTxs scan block with the given height
	FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error)
	// GetHeight return current block height
	GetHeight() (int64, error)
	// GetNetworkFee returns current network fee details
	GetNetworkFee() (transactionSize, transactionFeeRate uint64)
}

// BlockScannerSolvencyReporter is an optional post-fetch hook. The ordered scan
// loop calls it only after a successful fetch has restored scanner health, never
// from prefetch workers. Reporting failures do not discard the fetched block.
type BlockScannerSolvencyReporter interface {
	ReportSolvency(fetchHeight, chainHeight int64) error
}

type Block struct {
	Height int64
	Txs    []string
}

// BlockScanner is used to discover block height
type BlockScanner struct {
	cfg                   config.BifrostBlockScannerConfiguration
	logger                zerolog.Logger
	wg                    *sync.WaitGroup
	scanChan              chan int64
	stopChan              chan struct{}
	rollbackChan          chan int64
	scannerStorage        ScannerStorage
	metrics               *metrics.Metrics
	previousBlock         int64
	globalTxsQueue        chan types.TxIn
	globalNetworkFeeQueue chan common.NetworkFee
	errorCounter          *prometheus.CounterVec
	thorchainBridge       thorclient.ThorchainBridge
	chainScanner          BlockScannerFetcher
	healthy               *atomic.Bool

	// stall tracking, only touched from the scanBlocks goroutine
	stallHeight int64
	stallSince  time.Time
}

// NewBlockScanner create a new instance of BlockScanner
func NewBlockScanner(cfg config.BifrostBlockScannerConfiguration, scannerStorage ScannerStorage, m *metrics.Metrics, thorchainBridge thorclient.ThorchainBridge, chainScanner BlockScannerFetcher) (*BlockScanner, error) {
	var err error
	if scannerStorage == nil {
		return nil, errors.New("scannerStorage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics instance is nil")
	}
	if thorchainBridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}

	logger := log.Logger.With().Str("module", "blockscanner").Str("chain", cfg.ChainID.String()).Logger()
	scanner := &BlockScanner{
		cfg:             cfg,
		logger:          logger,
		wg:              &sync.WaitGroup{},
		stopChan:        make(chan struct{}),
		scanChan:        make(chan int64),
		rollbackChan:    make(chan int64),
		scannerStorage:  scannerStorage,
		metrics:         m,
		errorCounter:    m.GetCounterVec(metrics.CommonBlockScannerError),
		thorchainBridge: thorchainBridge,
		chainScanner:    chainScanner,
		healthy:         &atomic.Bool{},
	}

	scanner.previousBlock, err = scanner.GetStartHeight()
	logger.Info().Int64("block height", scanner.previousBlock).Msg("block scanner last fetch height")
	return scanner, err
}

// IsHealthy return if the block scanner is healthy or not
func (b *BlockScanner) IsHealthy() bool {
	return b.healthy.Load()
}

func (b *BlockScanner) PreviousHeight() int64 {
	return atomic.LoadInt64(&b.previousBlock)
}

// RollbackToLastObserved rollback the block scanner to last observed height minus flex period
func (b *BlockScanner) RollbackToLastObserved() error {
	lastObservedHeight, err := b.thorchainBridge.GetLastObservedInHeight(b.cfg.ChainID)
	if err != nil {
		return fmt.Errorf("fail to get last observed height: %w", err)
	}
	if lastObservedHeight <= 0 {
		// no last observed height, no need to rollback
		return nil
	}

	maxConfirmations, err := b.thorchainBridge.GetMimirWithRef(constants.MimirTemplateMaxConfirmations, b.cfg.ChainID.String())
	if err != nil || maxConfirmations < 0 {
		maxConfirmations = 0
	}

	c, err := b.thorchainBridge.GetConstants()
	if err != nil {
		return fmt.Errorf("fail to get constants: %w", err)
	}

	obsDelayFlexConst := constants.ObservationDelayFlexibility.String()
	observerFlexWindowBlocksThor := c[obsDelayFlexConst]
	observerFlexWindowBlocksThorMimir, err := b.thorchainBridge.GetMimir(obsDelayFlexConst)
	if err == nil && observerFlexWindowBlocksThorMimir > 0 {
		observerFlexWindowBlocksThor = observerFlexWindowBlocksThorMimir
	}

	thorBlockTimeMs := c[constants.ThorchainBlockTime.String()] / int64(time.Millisecond)
	observerFlexWindowBlocksChain := observerFlexWindowBlocksThor * thorBlockTimeMs / b.cfg.ChainID.ApproximateBlockMilliseconds()
	if observerFlexWindowBlocksChain < 1 {
		observerFlexWindowBlocksChain = 1
	}

	rollbackHeight := lastObservedHeight - max(observerFlexWindowBlocksChain, maxConfirmations)

	if rollbackHeight < 0 {
		rollbackHeight = 0
	}

	select {
	case b.rollbackChan <- rollbackHeight:
		return nil
	case <-b.stopChan:
		return errors.New("block scanner stopped before rollback could be queued")
	}
}

func (b *BlockScanner) rollback(height int64) error {
	// FloorBlockHeight is a hard lower bound: never roll back below it, even on a
	// reorg. Without this clamp a rollback (e.g. RollbackToLastObserved) could drag
	// the scanner back into a range the floor was configured to skip.
	if b.cfg.FloorBlockHeight > 0 && height < b.cfg.FloorBlockHeight {
		height = b.cfg.FloorBlockHeight
	}
	if b.PreviousHeight() <= height {
		// height is already below rollback amount, proceed as normal
		return nil
	}
	// Stateful chain clients may be unable to replay an arbitrarily old block.
	// Validate on the scan loop before persisting a cursor they cannot follow.
	// Rejection leaves the existing cursor and pending recovery state intact.
	if validator, ok := b.chainScanner.(interface{ ValidateScannerRollback(int64) error }); ok {
		if err := validator.ValidateScannerRollback(height + 1); err != nil {
			return fmt.Errorf("rollback validation failed; scan position retained and requested history not replayed: %w", err)
		}
	}
	if err := b.scannerStorage.SetScanPos(height); err != nil {
		return fmt.Errorf("fail to set scan pos: %w", err)
	}
	// set the previous block to height
	atomic.StoreInt64(&b.previousBlock, height)
	return nil
}

// rewindToNextHeight durably positions the scanner immediately before
// nextHeight. Unlike operator-driven rollback, a chain-client cursor rewind is
// never clamped: every bound must be satisfied exactly or the request fails.
func (b *BlockScanner) rewindToNextHeight(currentHeight, nextHeight int64) error {
	if nextHeight <= 0 {
		return fmt.Errorf("invalid cursor rewind next height %d: must be positive", nextHeight)
	}
	if nextHeight >= currentHeight {
		return fmt.Errorf("invalid cursor rewind next height %d: must be below current height %d", nextHeight, currentHeight)
	}
	if currentHeight-nextHeight > btypes.MaxCursorRewindBlocks {
		return fmt.Errorf(
			"cursor rewind from %d to %d exceeds hard limit of %d blocks",
			currentHeight,
			nextHeight,
			btypes.MaxCursorRewindBlocks,
		)
	}

	previousHeight := nextHeight - 1
	if previousHeight < b.cfg.FloorBlockHeight {
		return fmt.Errorf(
			"cursor rewind next height %d would move previous height %d below floor %d",
			nextHeight,
			previousHeight,
			b.cfg.FloorBlockHeight,
		)
	}

	// LevelDBScannerStorage performs a synchronous write. Publish the in-memory
	// cursor only after that durable write succeeds so a restart and the running
	// process always agree on the next block to fetch.
	if err := b.scannerStorage.SetScanPos(previousHeight); err != nil {
		return fmt.Errorf("fail to persist cursor rewind to %d: %w", previousHeight, err)
	}
	atomic.StoreInt64(&b.previousBlock, previousHeight)
	return nil
}

// maxResumeLagBlocks converts MaxResumeBlockLag into a block count for this
// chain, or 0 when catch-up is disabled or the chain has no known block time.
func (b *BlockScanner) maxResumeLagBlocks() int64 {
	if b.cfg.MaxResumeBlockLag <= 0 {
		return 0
	}
	ms := b.cfg.ChainID.ApproximateBlockMilliseconds()
	if ms <= 0 {
		return 0
	}
	return b.cfg.MaxResumeBlockLag.Milliseconds() / ms
}

// skipAheadIfStalled moves a wedged cursor forward to the catch-up window and
// reports whether it moved. GetStartHeight applies the same MaxResumeBlockLag
// bound when the scanner starts, but a scanner that wedges while running — a
// pruned or permanently unavailable block, a fetch that fails deterministically
// — would otherwise retry the same height until an operator restarts it, and
// stay unhealthy (so refuse to sign) the whole time.
//
// The skip target is derived from the chain's last observed height on
// THORChain, which is a consensus value, so every block passed over was already
// observed to supermajority by the rest of the network. That is what makes this
// safe to do unilaterally, unlike the operator-chosen FloorBlockHeight.
func (b *BlockScanner) skipAheadIfStalled(currentBlock int64) bool {
	maxLag := b.maxResumeLagBlocks()
	if maxLag <= 0 || b.thorchainBridge == nil || b.cfg.ChainID.Equals(common.THORChain) {
		return false
	}

	// A single failure is not a stall. Only skip once this exact height has been
	// failing for longer than MaxResumeBlockLag, which is the same window that
	// decides the skip target: a scanner stuck on one block for that long is by
	// definition past the lag the operator agreed to tolerate. Reusing it needs
	// no new config, and no transient RPC error or daemon restart trips it.
	if b.stallHeight != currentBlock {
		b.stallHeight, b.stallSince = currentBlock, time.Now()
		return false
	}
	if time.Since(b.stallSince) < b.cfg.MaxResumeBlockLag {
		return false
	}

	observed, err := b.thorchainBridge.GetLastObservedInHeight(b.cfg.ChainID)
	if err != nil || observed <= 0 {
		return false
	}

	// Forward-only: a cursor inside the catch-up window is not stalled, it is
	// just behind, and must keep scanning every block it has not seen.
	target := observed - maxLag
	if target <= b.PreviousHeight() {
		return false
	}

	// Publish the in-memory cursor only after the durable write succeeds, so a
	// restart and the running process agree on the next block to fetch.
	if err := b.scannerStorage.SetScanPos(target); err != nil {
		b.logger.Error().Err(err).Int64("target", target).Msg("fail to persist stall skip ahead")
		return false
	}
	previous := atomic.SwapInt64(&b.previousBlock, target)
	b.logger.Warn().
		Int64("from", previous).
		Int64("to", target).
		Int64("last_observed_in", observed).
		Msg("block scanner stalled beyond max resume lag, skipping ahead to consensus observed height")
	b.recordError("stalled_scanner_skip_ahead")
	return true
}

func rewindRequiredNextHeight(err error) (int64, bool) {
	// RewindRequiredError intentionally has a value receiver, so tolerate both
	// value and pointer construction by chain scanners, including wrapped errors.
	var value btypes.RewindRequiredError
	if errors.As(err, &value) {
		return value.NextHeight, true
	}
	var pointer *btypes.RewindRequiredError
	if errors.As(err, &pointer) && pointer != nil {
		return pointer.NextHeight, true
	}
	return 0, false
}

func scannerRecoveryRequired(err error) (*btypes.ScannerRecoveryRequiredError, bool) {
	// ScannerRecoveryRequiredError also has a value receiver. Accept both forms
	// so a producer cannot accidentally turn an operator fail-stop into an
	// endlessly retried transient error merely by returning a value.
	var value btypes.ScannerRecoveryRequiredError
	if errors.As(err, &value) {
		return &value, true
	}
	var pointer *btypes.ScannerRecoveryRequiredError
	if errors.As(err, &pointer) && pointer != nil {
		return pointer, true
	}
	return nil, false
}

// GetMessages return the channel
func (b *BlockScanner) GetMessages() <-chan int64 {
	return b.scanChan
}

// Start block scanner
func (b *BlockScanner) Start(globalTxsQueue chan types.TxIn, globalNetworkFeeQueue chan common.NetworkFee) {
	b.globalTxsQueue = globalTxsQueue
	b.globalNetworkFeeQueue = globalNetworkFeeQueue

	currentPos, err := b.scannerStorage.GetScanPos()
	if err != nil {
		b.logger.Error().Err(err).Msgf("fail to get current block scan pos, %s will start from %d", b.cfg.ChainID, b.previousBlock)
	}

	// Only use persisted scan position if no explicit start height was configured.
	// When StartBlockHeight is set, rewind the persisted scan position to that
	// height so operators can force a rescan from a specific height, durably
	// across restarts.
	if b.cfg.StartBlockHeight > 0 {
		if err == nil && currentPos > 0 {
			if serr := b.scannerStorage.SetScanPos(b.cfg.StartBlockHeight); serr != nil {
				b.logger.Error().Err(serr).Int64("height", b.cfg.StartBlockHeight).Msg("fail to rewind scan pos for start height override")
			} else {
				b.logger.Info().Int64("height", b.cfg.StartBlockHeight).Msg("rewound scan pos for rescan")
			}
		}
	} else if err == nil && currentPos > b.previousBlock {
		b.previousBlock = currentPos
	}

	// FloorBlockHeight prevents the scanner from ever starting below this height.
	// The effective floor is max(FloorBlockHeight, persisted scan pos) so the
	// scanner is never dragged backwards below where it has already scanned to.
	if b.cfg.FloorBlockHeight > 0 {
		floor := b.cfg.FloorBlockHeight
		if err == nil && currentPos > floor {
			floor = currentPos
		}
		if floor > b.previousBlock {
			b.logger.Info().
				Int64("floor", floor).
				Int64("previous", b.previousBlock).
				Msgf("advancing %s scanner to configured floor block height", b.cfg.ChainID)
			b.previousBlock = floor
		}
	}
	b.wg.Add(2)
	go b.scanBlocks()
	go b.scanMempool()
}

func (b *BlockScanner) scanMempool() {
	b.logger.Info().Msg("start to scan mempool")
	defer b.logger.Info().Msg("stop scan mempool")
	defer b.wg.Done()

	if !b.cfg.ScanMemPool {
		b.logger.Info().Msg("mempool scan is disabled")
		return
	}

	for {
		select {
		case <-b.stopChan:
			return
		default:
			// mempool scan will continue even the chain get halted , thus the network can still aware of outbound transaction
			// during chain halt
			preBlockHeight := atomic.LoadInt64(&b.previousBlock)
			currentBlock := preBlockHeight + 1
			txInMemPool, err := b.chainScanner.FetchMemPool(currentBlock)
			if err != nil {
				b.logger.Error().Err(err).Msg("fail to fetch MemPool")
			}
			if len(txInMemPool.TxArray) > 0 {
				select {
				case <-b.stopChan:
					return
				case b.globalTxsQueue <- txInMemPool:
				}
			} else {
				// backoff between mempool scans (some chain clients always return nothing)
				time.Sleep(constants.ThorchainBlockTime)
			}
		}
	}
}

const mimirHaltScannerXMR = "HaltScannerXMR"

// IsXMRScannerPaused checks the Bifrost-only XMR scanner switch. A missing or
// unreadable setting fails closed so deploying the monovault cannot implicitly
// enable economic activity. Zero explicitly enables scanning; any positive
// value pauses it.
func IsXMRScannerPaused(logger zerolog.Logger, bridge thorclient.ThorchainBridge) bool {
	halted, err := bridge.GetMimir(mimirHaltScannerXMR)
	if err != nil {
		logger.Error().Err(err).Str("mimir", mimirHaltScannerXMR).Msg("fail to get XMR scanner halt")
		return true
	}
	if halted < 0 {
		logger.Debug().Str("mimir", mimirHaltScannerXMR).Msg("XMR scanner halt is unset; keeping scanner paused")
		return true
	}
	return halted > 0
}

// Checks current mimir settings to determine if the current chain is paused
// either globally or specifically
func IsChainPaused(cfg config.BifrostBlockScannerConfiguration, logger zerolog.Logger, bridge thorclient.ThorchainBridge) bool {
	if cfg.ChainID.Equals(common.XMRChain) && IsXMRScannerPaused(logger, bridge) {
		return true
	}

	thorHeight, err := bridge.GetBlockHeight()
	if err != nil {
		logger.Error().Err(err).Msg("fail to get THORChain block height")
	}

	// Check if chain has been halted via mimir
	haltHeight, err := bridge.GetMimir(fmt.Sprintf("Halt%sChain", cfg.ChainID))
	if err != nil {
		logger.Error().Err(err).Msgf("fail to get mimir setting %s", fmt.Sprintf("Halt%sChain", cfg.ChainID))
	}
	if haltHeight > 0 && thorHeight >= haltHeight {
		return true
	}

	// Check if chain has been halted by auto solvency checks
	solvencyHaltHeight, err := bridge.GetMimir(fmt.Sprintf("SolvencyHalt%sChain", cfg.ChainID))
	if err != nil {
		logger.Error().Err(err).Msgf("fail to get mimir %s", fmt.Sprintf("SolvencyHalt%sChain", cfg.ChainID))
	}
	if solvencyHaltHeight > 0 && thorHeight >= solvencyHaltHeight {
		return true
	}

	// Check if all chains halted globally
	globalHaltHeight, err := bridge.GetMimir("HaltChainGlobal")
	if err != nil {
		logger.Error().Err(err).Msg("fail to get mimir setting HaltChainGlobal")
	}
	if globalHaltHeight > 0 && thorHeight >= globalHaltHeight {
		return true
	}

	// Check if a node temporarily paused all chains
	nodePauseHeight, err := bridge.GetMimir("NodePauseChainGlobal")
	if err != nil {
		logger.Error().Err(err).Msg("fail to get mimir setting NodePauseChainGlobal")
	}

	return (nodePauseHeight > 0 && thorHeight <= nodePauseHeight)
}

// scanBlocks
func (b *BlockScanner) scanBlocks() {
	b.logger.Debug().Msg("start to scan blocks")
	defer b.logger.Debug().Msg("stop scan blocks")
	defer b.wg.Done()

	lastMimirCheck := time.Now().Add(-constants.ThorchainBlockTime)
	isChainPaused := false

	type fetchTxsResult struct {
		txIn types.TxIn
		err  error
	}
	type prefetchedFetch struct {
		result    chan fetchTxsResult
		abandoned chan struct{}
	}
	prefetch := map[int64]*prefetchedFetch{}
	clearPrefetch := func() {
		abandoned := prefetch
		prefetch = make(map[int64]*prefetchedFetch)
		for _, fetch := range abandoned {
			// Signal the existing bounded fetch goroutine rather than creating
			// an unbounded set of drain goroutines on repeated rewinds.
			close(fetch.abandoned)
		}
	}
	defer clearPrefetch()

	// start up to grab those blocks
	for {
		select {
		case <-b.stopChan:
			return
		case amount := <-b.rollbackChan:
			// Retain the earliest requested height through temporary validation failures.
			// Forward scans and skip-ahead must wait so they cannot age the replay
			// beyond the supported window. Until rollback saves the cursor this
			// request exists only in memory and is lost on process restart.
			for {
				err := b.rollback(amount)
				if err == nil {
					// Prefetched results belong to the abandoned cursor lineage.
					clearPrefetch()
					break
				}
				retry := errors.Is(err, btypes.ErrRollbackValidationUnavailable)
				if retry {
					b.healthy.Store(false)
					b.logger.Warn().Err(err).Int64("rollback_height", amount).
						Msg("rollback validation unavailable; retaining request for retry")
				} else {
					b.logger.Error().Err(err).Msg("fail to rollback block scanner")
				}
				b.recordError("rollback_failed")
				stopped := func() bool {
					timer := time.NewTimer(b.cfg.BlockHeightDiscoverBackoff)
					defer timer.Stop()
					var queued <-chan int64
					if retry {
						// Observer sends rollbacks from the loop that submits every
						// chain's observations. Accept later requests while waiting,
						// keeping the lower target that covers both.
						queued = b.rollbackChan
					}
					for {
						select {
						case <-b.stopChan:
							return true
						case next := <-queued:
							amount = min(amount, next)
						case <-timer.C:
							return false
						}
					}
				}()
				if stopped {
					return
				}
				if !retry {
					break
				}
			}
		default:
			preBlockHeight := atomic.LoadInt64(&b.previousBlock)
			currentBlock := preBlockHeight + 1
			// check if mimir has disabled this chain
			if time.Since(lastMimirCheck) >= constants.ThorchainBlockTime {
				isChainPaused = IsChainPaused(b.cfg, b.logger, b.thorchainBridge)
				lastMimirCheck = time.Now()
			}

			// Chain is paused, mark as unhealthy
			if isChainPaused {
				b.healthy.Store(false)
				time.Sleep(constants.ThorchainBlockTime)
				continue
			}

			chainHeight, err := b.chainScanner.GetHeight()
			if err != nil {
				b.logger.Error().Err(err).Msg("fail to get chain block height")
				time.Sleep(b.cfg.BlockHeightDiscoverBackoff)
				continue
			}
			if chainHeight < currentBlock {
				time.Sleep(b.cfg.BlockHeightDiscoverBackoff)
				continue
			}

			// get the prefetched result or call FetchTxs for the block
			var txIn types.TxIn
			if fetch, ok := prefetch[currentBlock]; ok {
				// already prefetched, just wait for the result
				var result fetchTxsResult
				select {
				case result = <-fetch.result:
				case <-b.stopChan:
					return
				}
				delete(prefetch, currentBlock)
				txIn, err = result.txIn, result.err
			} else {
				txIn, err = b.chainScanner.FetchTxs(currentBlock, chainHeight)
			}

			if err != nil {
				// A fetcher may reserve durable replay state before it discovers a
				// later error. Release that in-process reservation on every failed
				// fetch; its synced journal remains available for the retry.
				if txIn.AbortPersistence != nil {
					txIn.AbortPersistence()
				}

				if recoveryRequired, recovery := scannerRecoveryRequired(err); recovery {
					clearPrefetch()
					b.logger.Error().Err(err).
						Int64("current_height", recoveryRequired.CurrentHeight).
						Int64("next_height", recoveryRequired.NextHeight).
						Msg("chain scanner stopped pending operator recovery")
					b.healthy.Store(false)
					b.recordError("operator_recovery_required")
					return
				}

				if nextHeight, rewindRequired := rewindRequiredNextHeight(err); rewindRequired {
					// Even a rejected rewind proves the prefetched results were
					// produced against a cursor lineage the chain client abandoned.
					clearPrefetch()
					if rewindErr := b.rewindToNextHeight(currentBlock, nextHeight); rewindErr != nil {
						b.logger.Error().Err(rewindErr).
							Int64("current_height", currentBlock).
							Int64("next_height", nextHeight).
							Msg("reject chain scanner cursor rewind")
						b.healthy.Store(false)
						b.recordError("cursor_rewind_rejected")
						time.Sleep(b.cfg.BlockHeightDiscoverBackoff)
						continue
					}
					b.logger.Warn().
						Int64("current_height", currentBlock).
						Int64("next_height", nextHeight).
						Msg("rewound block scanner to chain client cursor")
					continue
				}

				// This height is at or below the reported tip, but has not been fully
				// scanned. Retrying it must not preserve stale signing health.
				b.healthy.Store(false)
				// Expected availability retries remain quiet and never skip a height.
				if !errors.Is(err, btypes.ErrUnavailableBlock) {
					b.logger.Error().Err(err).Int64("block height", currentBlock).Msg("fail to get RPCBlock")

					// An error that never clears would otherwise retry this same height
					// forever, so give the cursor the same catch-up bound the scanner
					// gets at startup before backing off. ErrUnavailableBlock is
					// excluded: chain clients return it to ask for a retry of this
					// exact height while they keep per-height state, and their
					// terminal cases already use ScannerRecoveryRequiredError.
					if b.skipAheadIfStalled(currentBlock) {
						clearPrefetch()
						continue
					}
				}
				time.Sleep(b.cfg.BlockHeightDiscoverBackoff)
				continue
			}

			// a height that fetched is not stalled, so a later rewind back to it
			// starts its stall clock fresh rather than inheriting an old one
			b.stallHeight = 0

			// start prefetching next blocks if configured
			if b.cfg.PrefetchBlocks > 1 {
				for i := int64(1); i < b.cfg.PrefetchBlocks; i++ {
					prefetchHeight := currentBlock + i
					if _, ok := prefetch[prefetchHeight]; !ok && prefetchHeight <= chainHeight {
						fetch := &prefetchedFetch{
							result:    make(chan fetchTxsResult),
							abandoned: make(chan struct{}),
						}
						prefetch[prefetchHeight] = fetch
						go func(height int64, fetch *prefetchedFetch) {
							fetchTxIn, fetchErr := b.chainScanner.FetchTxs(height, chainHeight)
							select {
							case fetch.result <- fetchTxsResult{txIn: fetchTxIn, err: fetchErr}:
							case <-fetch.abandoned:
								if fetchTxIn.AbortPersistence != nil {
									fetchTxIn.AbortPersistence()
								}
							case <-b.stopChan:
								if fetchTxIn.AbortPersistence != nil {
									fetchTxIn.AbortPersistence()
								}
							}
						}(prefetchHeight, fetch)
					}
				}
			}

			ms := b.cfg.ChainID.ApproximateBlockMilliseconds()

			// consider 3 blocks or the configured lag time behind tip as healthy
			lagDuration := time.Duration((chainHeight-currentBlock)*ms) * time.Millisecond
			if chainHeight-currentBlock <= 3 || lagDuration < b.cfg.MaxHealthyLag {
				b.healthy.Store(true)
			} else {
				b.healthy.Store(false)
			}

			if b.healthy.Load() {
				if reporter, ok := b.chainScanner.(BlockScannerSolvencyReporter); ok {
					if solvencyErr := reporter.ReportSolvency(currentBlock, chainHeight); solvencyErr != nil {
						b.logger.Error().Err(solvencyErr).Int64("height", currentBlock).Msg("fail to report solvency after scan")
					}
				}
			}

			// determine how often we compare THORNode network fee to Bifrost network fee.
			// General goal is about once per hour, but report immediately if
			// THORChain has no fee for this chain yet (rate=0).
			mod := ((60 * 60 * 1000) + ms - 1) / ms
			if currentBlock%mod == 0 {
				b.updateStaleNetworkFee(currentBlock)
			} else if _, thorRate, _ := b.thorchainBridge.GetNetworkFee(b.cfg.ChainID); thorRate == 0 && !b.cfg.ChainID.Equals(common.THORChain) && b.healthy.Load() {
				b.updateStaleNetworkFee(currentBlock)
			}

			// determine how often we print a info log line for scanner
			// progress. General goal is about once per minute
			mod = (60_000 + ms - 1) / ms
			// enable this one , so we could see how far it is behind
			if currentBlock%mod == 0 || !b.healthy.Load() {
				b.logger.Info().
					Int64("block height", currentBlock).
					Int("txs", len(txIn.TxArray)).
					Int64("gap", chainHeight-currentBlock).
					Bool("healthy", b.healthy.Load()).
					Msg("scan block")
			}

			b.logger.Debug().Msgf("the gap is %d , healthy: %+v", chainHeight-currentBlock, b.healthy.Load())

			b.metrics.GetCounter(metrics.TotalBlockScanned).Inc()
			// A scanner may need Observer to acknowledge durable metadata even when
			// the delivery contains no transaction items. Enqueue those handshakes so
			// Observer's empty-TxIn acknowledgement path remains reachable.
			if len(txIn.TxArray) > 0 || txIn.Persisted != nil {
				select {
				case <-b.stopChan:
					return
				case b.globalTxsQueue <- txIn:
				}
			}
			if err = waitTxInPersistence(txIn, b.stopChan); err != nil {
				if txIn.AbortPersistence != nil {
					txIn.AbortPersistence()
				}
				if !b.backoffAfterPersistenceFailure(err, "observation_persistence_failed", "observer failed to persist scanned transactions") {
					return
				}
				continue
			}
			if txIn.FinalizePersistence != nil {
				if err = txIn.FinalizePersistence(); err != nil {
					if !b.backoffAfterPersistenceFailure(err, "persistence_finalize_failed", "chain scanner failed to finalize persisted transactions") {
						return
					}
					continue
				}
			}
			if err = b.scannerStorage.SetScanPos(currentBlock); err != nil {
				if !b.backoffAfterPersistenceFailure(err, "scan_position_persistence_failed", "fail to save block scan pos") {
					return
				}
				continue
			}
			atomic.StoreInt64(&b.previousBlock, currentBlock)
		}
	}
}

// backoffAfterPersistenceFailure makes a durable-progress failure visible and
// prevents the scanner from hammering the same block and re-enqueuing its
// observations in a tight loop. The wait is cancellable so Stop remains prompt.
func (b *BlockScanner) backoffAfterPersistenceFailure(err error, errorName, message string) bool {
	b.logger.Error().Err(err).Msg(message)
	b.healthy.Store(false)
	b.recordError(errorName)

	backoff := b.cfg.BlockHeightDiscoverBackoff
	if backoff <= 0 {
		backoff = constants.ThorchainBlockTime
	}
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-b.stopChan:
		return false
	case <-timer.C:
		return true
	}
}

func (b *BlockScanner) recordError(errorName string) {
	if b.errorCounter == nil {
		return
	}
	b.errorCounter.WithLabelValues(errorName, b.cfg.ChainID.String()).Inc()
}

func waitTxInPersistence(txIn types.TxIn, stop <-chan struct{}) error {
	if txIn.Persisted == nil {
		return nil
	}
	timer := time.NewTimer(2 * constants.ThorchainBlockTime)
	defer timer.Stop()
	select {
	case err := <-txIn.Persisted:
		return err
	case <-stop:
		return fmt.Errorf("block scanner stopped before observation persistence")
	case <-timer.C:
		return fmt.Errorf("timed out waiting for observation persistence")
	}
}

// updateStaleNetworkFee broadcasts a network fee observation if the local scanner fee
// does not match the fee published to THORNode. This can be called periodically to
// ensure fee changes find consensus despite raciness on the observation height.
func (b *BlockScanner) updateStaleNetworkFee(currentBlock int64) {
	// Only broadcast MsgNetworkFee if the chain isn't THORChain
	// and the scanner is healthy.
	if b.cfg.ChainID.Equals(common.THORChain) || !b.healthy.Load() {
		return
	}

	transactionSize, transactionFeeRate := b.chainScanner.GetNetworkFee()
	thorTransactionSize, thorTransactionFeeRate, err := b.thorchainBridge.GetNetworkFee(b.cfg.ChainID)
	if err != nil {
		b.logger.Error().Err(err).Msg("fail to get thornode network fee")
		return
	}
	// Do not broadcast a regularly-timed network fee if the THORNode network fee is already consistent with the scanner's.
	if thorTransactionSize == transactionSize && thorTransactionFeeRate == transactionFeeRate {
		b.logger.Info().
			Int64("height", currentBlock).
			Uint64("size", transactionSize).
			Uint64("rate", transactionFeeRate).
			Msg("thornode network fee is consistent with scanner, no need to broadcast")
		return
	}

	b.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           b.cfg.ChainID,
		Height:          currentBlock,
		TransactionSize: transactionSize,
		TransactionRate: transactionFeeRate,
	}

	b.logger.Info().
		Int64("height", currentBlock).
		Uint64("size", transactionSize).
		Uint64("rate", transactionFeeRate).
		Msg("sent timed network fee to THORChain")
}

// GetStartHeight determines the height to start scanning:
//  1. Use the config start height if set.
//  2. If last consensus inbound height (lastblock) is available:
//     a) Use local scanner storage height if available, up to the max lag from lastblock.
//     b) Otherwise, use lastblock.
//  3. Otherwise, use local scanner storage height if available.
//  4. Otherwise, use the last height from the chain itself.
func (b *BlockScanner) GetStartHeight() (int64, error) {
	// get scanner storage height
	currentPos, _ := b.scannerStorage.GetScanPos() // ignore error

	clog := b.logger.With().Stringer("chain", b.cfg.ChainID).Logger()

	// 1. Use the config start height if set.
	if b.cfg.StartBlockHeight > 0 {
		clog.Info().
			Int64("start_height", b.cfg.StartBlockHeight).
			Msg("using configured start block height")
		return b.cfg.StartBlockHeight, nil
	}

	// wait for thorchain to be caught up first
	if err := b.thorchainBridge.WaitToCatchUp(); err != nil {
		clog.Info().Err(err).Msg("waiting for thorchain to catch up")
		return 0, err
	}

	if b.thorchainBridge != nil && b.cfg.ChainID != common.THORChain {
		// A failure here is not fatal: THORChain legitimately has no lastblock row
		// for a chain it has never observed. But it does mean the max-lag bound
		// below is skipped and the stale local cursor is returned verbatim, so say
		// so rather than dropping the error on the floor. skipAheadIfStalled
		// re-applies the bound once scanning is running.
		height, err := b.thorchainBridge.GetLastObservedInHeight(b.cfg.ChainID)
		if err != nil {
			clog.Warn().Err(err).
				Int64("scan_pos", currentPos).
				Msg("no consensus observed height, starting without the max resume lag bound")
		}
		if height > 0 {

			// 2.a) Use local scanner storage height if available, up to the max lag from lastblock.
			if currentPos > 0 {
				// calculate the max lag
				maxLagBlocks := b.maxResumeLagBlocks()

				// return the position up to the max block lag behind the consensus height
				if height <= currentPos+maxLagBlocks {
					clog.Info().
						Int64("start_height", currentPos).
						Int64("last_observed_height", height).
						Msg("using local scanner storage height")

					return currentPos, nil
				} else {
					clog.Info().
						Int64("start_height", height-maxLagBlocks).
						Int64("last_observed_height", height).
						Msg("using last observed height with max lag")
					return height - maxLagBlocks, nil
				}
			}

			// 2.b) Otherwise, use lastblock.
			clog.Info().
				Int64("start_height", height).
				Msg("using last observed height")
			return height, nil
		}
	}

	//  3. Otherwise, use local scanner storage height if available.
	if currentPos > 0 {
		clog.Info().
			Int64("start_height", currentPos).
			Msg("using local scanner storage height")
		return currentPos, nil
	}

	//  4. Otherwise, use the last height from the chain itself.
	height, err := b.chainScanner.GetHeight()
	if err != nil {
		clog.Error().Err(err).Msg("failed to get chain height")
		return 0, err
	}
	if b.cfg.ChainID == common.THORChain && b.cfg.MaxResumeBlockLag > 0 {
		maxLagBlocks := b.maxResumeLagBlocks()
		if maxLagBlocks > 0 {
			startHeight := height - maxLagBlocks
			if startHeight < 1 {
				startHeight = 1
			}
			clog.Info().
				Int64("start_height", startHeight).
				Int64("chain_height", height).
				Msg("using chain height with max lag backfill")
			return startHeight, nil
		}
	}
	clog.Info().
		Int64("start_height", height).
		Msg("using chain height as start height")
	return height, nil
}

func (b *BlockScanner) Stop() {
	b.logger.Debug().Msg("receive stop request")
	defer b.logger.Debug().Msg("common block scanner stopped")
	close(b.stopChan)
	b.wg.Wait()
}
