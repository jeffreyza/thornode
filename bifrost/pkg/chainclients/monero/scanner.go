package monero

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

const (
	// Monero uses 1e12 atomic units and THORChain uses 1e8 decimals.
	moneroAtomicToThorchain = 10000
	// typicalVaultOutboundWeight is the conservative vault/FROST outbound tx
	// weight in Monero weight units used for THORChain network fee reporting.
	// Keep it over-reserved for now so migration txs that spend many UTXOs are
	// less likely to be blocked by fee spikes before the full MaxGas feedback and
	// reschedule path lands.
	typicalVaultOutboundWeight = 10000
	// Retain the existing ceiling for reorg replay and per-call catch-up work.
	maxSidecarScannerRewindBlocks int64 = 256
	// Quiet-chain churn can roll Bifrost back farther than its reorg window.
	// The pinned signer's MAX_SCANNER_REWIND_BLOCKS permits this initial rewind;
	// replay still proceeds in the configured, smaller catch-up batches.
	maxChurnScannerRewindBlocks int64 = 730
)

// Scanner adapts monero daemon/wallet RPC to the generic block scanner fetcher interface.
type Scanner struct {
	logger              zerolog.Logger
	sidecar             *sidecar.Client
	bridge              thorclient.ThorchainBridge
	chain               common.Chain
	localNodePubKey     common.PubKey
	vaultBootstrap      vaultSpendKeyProvider
	reorgStore          *ReorgStore
	internalReplayStore *authenticatedInternalInboundReplayStore
	// scanMu serializes canonical scans with authenticated replay enrollment.
	// The generic scanner can prefetch heights concurrently; a single lock keeps
	// reorg journaling and outbox claims ordered without trusting fetch order.
	scanMu sync.Mutex
	// solvencyReporter is invoked after each healthy scan so that wallet vs
	// thornode vault accounting drift is reported to thornode promptly.
	// Without this hook, Client.ReportSolvency only fires from the
	// auto-unhalt SolvencyCheckRunner which is gated on the chain being
	// already halted — meaning vaults can drift indefinitely undetected.
	solvencyReporter func(int64) error

	currentBlockHeight     *atomic.Int64
	daemonTipHeight        *atomic.Int64
	lastFee                *atomic.Uint64
	globalErrataQueue      chan<- stypes.ErrataBlock
	claimQuarantineHandler func(string) error
	localSignedTxHandler   func(string) (bool, error)
	maxReorgRescan         int64

	// errataDeliveryTimeout bounds both the send to the observer and the wait
	// for its acknowledgement. Zero means the default; tests shorten it.
	errataDeliveryTimeout time.Duration

	asgardMu       sync.RWMutex
	asgardLookup   map[string]common.PubKey
	asgardUpdated  time.Time
	manualAddrByPK map[string]string
	manualPKByAddr map[string]common.PubKey

	observedOutgoingMu     sync.RWMutex
	observedOutgoingByTxID map[string]observedOutgoingTx

	relocationWatchCursor string
}

type observedOutgoingTx struct {
	height int64
	vaults map[string]struct{}
}

func NewScanner(
	logger zerolog.Logger,
	sidecarClient *sidecar.Client,
	bridge thorclient.ThorchainBridge,
	chain common.Chain,
	localNodePubKey common.PubKey,
	maxReorgRescan int64,
) *Scanner {
	return &Scanner{
		logger:                 logger,
		sidecar:                sidecarClient,
		bridge:                 bridge,
		chain:                  chain,
		localNodePubKey:        localNodePubKey,
		currentBlockHeight:     atomic.NewInt64(0),
		daemonTipHeight:        atomic.NewInt64(0),
		lastFee:                atomic.NewUint64(0),
		maxReorgRescan:         maxReorgRescan,
		asgardLookup:           make(map[string]common.PubKey),
		manualAddrByPK:         make(map[string]string),
		manualPKByAddr:         make(map[string]common.PubKey),
		observedOutgoingByTxID: make(map[string]observedOutgoingTx),
	}
}

// SetSolvencyReporter wires the solvency reporter callback used by the
// ordered post-fetch hook. Pass Client.ReportSolvency from the chain client.
func (s *Scanner) SetSolvencyReporter(fn func(int64) error) {
	s.solvencyReporter = fn
}

func (s *Scanner) SetManualVaultAddress(vaultPubKey common.PubKey, address string) {
	if vaultPubKey.IsEmpty() || strings.TrimSpace(address) == "" {
		return
	}
	derived, err := vaultPubKey.GetAddress(s.chain)
	if err != nil || derived.IsEmpty() {
		s.logger.Debug().Err(err).Str("vault_pubkey", vaultPubKey.String()).Msg("skip manual monero vault mapping: cannot derive address from pubkey")
		return
	}
	if !common.AddressEquals(common.XMRChain, derived, common.Address(strings.TrimSpace(address))) {
		s.logger.Warn().
			Str("vault_pubkey", vaultPubKey.String()).
			Str("provided_address", strings.TrimSpace(address)).
			Str("derived_address", derived.String()).
			Msg("rejecting manual monero vault mapping that does not match derived vault address")
		return
	}
	s.asgardMu.Lock()
	s.manualAddrByPK[vaultPubKey.String()] = derived.String()
	s.manualPKByAddr[derived.String()] = vaultPubKey
	s.asgardMu.Unlock()
}

func (s *Scanner) ManualVaultAddress(vaultPubKey common.PubKey) (string, bool) {
	if vaultPubKey.IsEmpty() {
		return "", false
	}
	s.asgardMu.RLock()
	addr, ok := s.manualAddrByPK[vaultPubKey.String()]
	s.asgardMu.RUnlock()
	return addr, ok
}

func (s *Scanner) CurrentHeight() int64 {
	if s.currentBlockHeight == nil {
		return 0
	}
	return s.currentBlockHeight.Load()
}

// SetErrataQueue installs the observer errata queue. Delivery always requires
// the observer to durably persist each errata record and acknowledge it before
// reorg replay is considered delivered; see deliverReorgErrata. There is no
// unacknowledged mode.
func (s *Scanner) SetErrataQueue(globalErrataQueue chan<- stypes.ErrataBlock) {
	s.globalErrataQueue = globalErrataQueue
}

func (s *Scanner) SetReorgStore(store *ReorgStore) {
	s.reorgStore = store
}

func (s *Scanner) SetAuthenticatedInternalInboundReplayStore(store *authenticatedInternalInboundReplayStore) {
	s.internalReplayStore = store
}

func (s *Scanner) SetClaimQuarantineHandler(handler func(string) error) {
	s.claimQuarantineHandler = handler
}

func (s *Scanner) SetLocalSignedTxHandler(handler func(string) (bool, error)) {
	s.localSignedTxHandler = handler
}

func (s *Scanner) SetVaultBootstrap(bootstrap vaultSpendKeyProvider) {
	s.vaultBootstrap = bootstrap
}

// FetchMemPool is a no-op: XMR outbounds are auto-observed at signing and
// inbounds require mined confirmations. Mempool observations can collide
// with mined observation decks.
// Monero's NewClient also hard-codes cfg.BlockScanner.ScanMemPool = false
// to disable mempool polling.
func (s *Scanner) FetchMemPool(_ int64) (stypes.TxIn, error) {
	return stypes.TxIn{}, nil
}

// ValidateScannerRollback rejects churn replay outside the signer's rewind
// window before BlockScanner changes its durable cursor. XMR disables prefetch,
// so this runs between scans; read authoritative state rather than a cache that
// can lag a committed SyncAt whose response was lost. It does not complete or
// discard persisted historical recovery state when rollback cannot be performed.
func (s *Scanner) ValidateScannerRollback(nextHeight int64) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.sidecar == nil {
		return errors.New("monero scanner requires sidecar to validate rollback")
	}
	if nextHeight <= 0 {
		return fmt.Errorf("invalid monero rollback next height %d", nextHeight)
	}
	state, err := s.sidecar.ScannerState()
	if err != nil {
		if isRetryableScannerStateError(err) {
			return fmt.Errorf("%w: fetch sidecar scanner cursor before rollback: %w", btypes.ErrRollbackValidationUnavailable, err)
		}
		return fmt.Errorf("fetch sidecar scanner cursor before rollback: %w", err)
	}
	if state.NextHeight == nil {
		return errors.New("sidecar scanner cursor is unknown; cannot validate rollback")
	}
	// SyncAt can repeat the last scanned block without rewinding, even when
	// catch-up is disabled. Older requests need the configured rewind allowance.
	next := int64(*state.NextHeight)
	if distance := next - nextHeight; distance > 1 && distance > s.churnRewindLimit() {
		return fmt.Errorf("sidecar scanner cursor %d is ahead of requested height %d beyond rewind limit %d; historical replay requires operator recovery", next, nextHeight, s.churnRewindLimit())
	}
	return nil
}

func isRetryableScannerStateError(err error) bool {
	var httpErr *sidecar.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusRequestTimeout || httpErr.Status == http.StatusTooManyRequests ||
			(httpErr.Status >= http.StatusInternalServerError && httpErr.Status < 600)
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (s *Scanner) FetchTxs(fetchHeight, chainHeight int64) (stypes.TxIn, error) {
	txIn, err := s.fetchTxs(fetchHeight, chainHeight)
	if errors.Is(err, sidecar.ErrUnavailableBlock) {
		return txIn, btypes.ErrUnavailableBlock
	}
	if errors.Is(err, btypes.ErrUnavailableBlock) {
		return txIn, err
	}
	// All block reads, including prefetch, reorg and catch-up reads, leave through
	// this boundary. A structural height mismatch must never become a generic
	// stall error that BlockScanner can use to skip the durable cursor forward.
	if _, ok := errors.AsType[*sidecar.ChainBlockHeightMismatchError](err); !ok {
		return txIn, err
	}
	nextHeight := fetchHeight
	if state, known := s.sidecar.CachedScannerState(); known && state.NextHeight != nil {
		nextHeight = int64(*state.NextHeight)
	}
	return txIn, &btypes.ScannerRecoveryRequiredError{
		CurrentHeight: fetchHeight,
		NextHeight:    nextHeight,
		Reason:        err.Error(),
	}
}

func (s *Scanner) fetchTxs(fetchHeight, chainHeight int64) (stypes.TxIn, error) {
	txIn := stypes.TxIn{Chain: common.XMRChain}
	if s.sidecar == nil {
		return txIn, fmt.Errorf("monero scanner requires sidecar for chain queries")
	}

	block, err := s.sidecar.ChainBlock(fetchHeight)
	if err != nil {
		if errors.Is(err, sidecar.ErrUnavailableBlock) {
			return txIn, btypes.ErrUnavailableBlock
		}
		return txIn, fmt.Errorf("fail to fetch monero block %d: %w", fetchHeight, err)
	}

	// Estimate vault outbound fee from monerod's per-weight fee rate x typical
	// vault tx weight. Avoid block-coinbase averaging when possible because it
	// mixes complex user txs and materially overestimates simple vault outbounds.
	if perWeight, perWeightErr := s.sidecar.FeeRate(); perWeightErr == nil && perWeight > 0 {
		feeAtomic := perWeight * typicalVaultOutboundWeight
		if feeAtomic > 0 {
			s.lastFee.Store(feeAtomic)
		}
	} else if feeAtomic := averageNetworkFeeFromBlock(block.Reward, len(block.TxHashes)); feeAtomic > 0 {
		s.lastFee.Store(feeAtomic)
	}

	// ChainBlock/FeeRate are independent reads and need not block replay
	// enrollment. Serialize only the continuity-sensitive registration, rewind,
	// sync, and journal operations below.
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	// Registration may rewind the sidecar to an older vault birthday. Refresh
	// vaults before touching the reorg journal, emitting errata, or advancing
	// local metadata. The requested scan height bounds each vault's registration
	// birthday so a stale creation height cannot rewind the sidecar arbitrarily.
	if s.bridge != nil {
		if refreshErr := s.refreshAsgardLookup(fetchHeight); refreshErr != nil {
			s.logger.Err(refreshErr).Msg("fail to refresh asgard lookup, retrying next block")
			return txIn, btypes.ErrUnavailableBlock
		}
	}

	// Drain a fair page before a new fork can add relocation watches. This gives
	// stale ordinary entries a chance to resolve before the bounded anti-DoS
	// quota is evaluated during reorg emission.
	if err = s.reconcileRelocationWatches(); err != nil {
		return txIn, err
	}
	pendingMetas, reorgErr := s.processReorg(block, chainHeight)
	if reorgErr != nil {
		if errors.Is(reorgErr, btypes.ErrUnavailableBlock) {
			return txIn, btypes.ErrUnavailableBlock
		}
		return txIn, fmt.Errorf("fail to process monero reorg at %d: %w", fetchHeight, reorgErr)
	}
	// Reorg replay wrote its observation receipts synchronously as each sidecar
	// response arrived. Commit the replacement journal now; a crash afterwards
	// is recovered from those receipts instead of requiring the fork again.
	if err = s.reorgStore.SetBatch(pendingMetas); err != nil {
		return txIn, err
	}

	catchupHandled, completedMarker, err := s.advanceSidecarCatchup(fetchHeight, chainHeight)
	if err != nil {
		if errors.Is(err, btypes.ErrUnavailableBlock) {
			return txIn, btypes.ErrUnavailableBlock
		}
		return txIn, err
	}
	currentItems := make([]*stypes.TxInItem, 0)
	if !catchupHandled {
		currentItems, err = s.fetchTransfersByHeight(fetchHeight, chainHeight, block.Hash, block.PreviousHash)
		if err != nil {
			return txIn, err
		}
		currentMeta := s.blockMetaForItems(block, fetchHeight, currentItems)
		// Ordinary one-height SyncAt is exact-replayable by the sidecar. Avoid a
		// per-block receipt/fsync; durable receipts are reserved for multi-height
		// catch-up and reorg replay, where earlier responses cannot be replayed
		// after the journal advances.
		if err = s.reorgStore.Set(currentMeta); err != nil {
			return txIn, err
		}
	}
	if err = s.reconcileRelocationWatches(); err != nil {
		return txIn, err
	}

	lateItems, replayClaims, err := s.replayAuthenticatedInternalInbounds(fetchHeight, chainHeight)
	if err != nil {
		s.releaseAuthenticatedInternalInboundClaims(replayClaims)
		return txIn, err
	}
	replayHandedOff := false
	defer func() {
		if !replayHandedOff {
			s.releaseAuthenticatedInternalInboundClaims(replayClaims)
		}
	}()
	receiptClaims, err := s.canonicalObservationReceipts()
	if err != nil {
		return txIn, err
	}
	receiptItems := make([]*stypes.TxInItem, 0)
	for _, claim := range receiptClaims {
		receiptItems = append(receiptItems, claim.entry.Items...)
	}
	// Each scan response already aggregated its transaction outputs. A same-height
	// reorg or retry can return the same complete observation through both the
	// ordinary scan and a durable receipt. Merge those copies without adding
	// their amounts again, including after the replacement journal is committed.
	canonicalItems := append([]*stypes.TxInItem(nil), currentItems...)
	canonicalItems = append(canonicalItems, receiptItems...)
	canonicalItems = append(canonicalItems, lateItems...)
	canonicalItems = s.mergeTxInItemsByTxID(canonicalItems, false)
	orderTxInItemsHighestFirst(canonicalItems)
	canonicalItems, err = s.prepareCanonicalObservationHandoff(
		canonicalItems, receiptClaims, completedMarker,
	)
	if err != nil {
		return txIn, err
	}
	txIn.TxArray = canonicalItems
	if len(canonicalItems) > 0 {
		// BlockScanner must not advance until Observer owns every observation
		// durably (including the inactive producer certificate outbox).
		txIn.Persisted = make(chan error, 1)
	}

	s.setCurrentHeight(fetchHeight)

	if s.reorgStore != nil && s.maxReorgRescan > 0 {
		pruneBefore := fetchHeight - s.maxReorgRescan
		if pruneBefore > 0 {
			if _, pruneErr := s.reorgStore.PruneBefore(pruneBefore); pruneErr != nil {
				s.logger.Debug().Err(pruneErr).Int64("cutoff", pruneBefore).Msg("fail to prune monero reorg block metas")
			}
		}
	}

	replayHandedOff = true
	return txIn, nil
}

// ReportSolvency runs after FetchTxs has released scanMu and the shared scanner
// has recomputed health. XMR retains its per-height reporting eligibility.
func (s *Scanner) ReportSolvency(height, _ int64) error {
	if s.solvencyReporter == nil {
		return nil
	}
	return s.solvencyReporter(height)
}

// ReconcileCommittedOutbound closes the scan-before-import gap for a payout
// already committed on THORChain. MarkSpent alone records an outgoing as
// pending; a scanner which passed its block will not mark it mined again.
// This path repairs accounting only, preserving the signer's evidence origin.
func (s *Scanner) ReconcileCommittedOutbound(tx common.ObservedTx) error {
	return s.reconcileCommittedOutboundGuarded(tx, nil)
}

func (s *Scanner) reconcileCommittedOutboundGuarded(tx common.ObservedTx, guard func() (func(), bool)) error {
	if s.sidecar == nil || s.reorgStore == nil {
		return errors.New("committed xmr payout reconciliation is unavailable")
	}
	if err := validateXMRSpentRefsForSidecar(tx); err != nil {
		return err
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if guard != nil {
		release, admitted := guard()
		if !admitted || release == nil {
			return errXMRSpentRefsAdmissionRejected
		}
		defer release()
	}
	location, err := s.sidecar.CanonicalTxLocation(tx.Tx.ID.String())
	if err != nil {
		return err
	}
	if s.CurrentHeight() < location.Height {
		// MarkSpent preceded this lock, so the future scan owns mined status.
		return nil
	}
	// Own the reorg bookkeeping BEFORE the sidecar clears pending accounting.
	// A crash at either boundary can retry safely; no observation receipt is
	// created and a pruned canonical journal is recreated atomically.
	meta := &reorgBlockMeta{Height: location.Height, BlockHash: location.BlockHash, PreviousHash: location.PreviousHash, ProvenanceComplete: true}
	if err = s.reorgStore.PutAuthenticatedOutbound(meta, tx.Tx.ID.String()); err != nil {
		return err
	}
	proof, err := s.sidecar.MarkObserved(tx.ObservedPubKey.String(), tx.Tx.ID.String(), location.Height, location.BlockHash)
	if err != nil {
		return err
	}
	amount := tx.Tx.Coins.GetCoin(common.XMRAsset).Amount.Uint64()
	if err = s.validateAuthenticatedMigrationProofForObserved(proof, tx, location.Height, amount); err != nil {
		return err
	}
	current, err := s.sidecar.CanonicalTxLocation(tx.Tx.ID.String())
	if err != nil {
		return err
	}
	if current != location {
		return errors.New("committed xmr payout moved during mined reconciliation")
	}
	s.logger.Debug().Str("txid", tx.Tx.ID.String()).Int64("block_height", location.Height).
		Msg("reconciled committed xmr payout mined accounting")
	return nil
}

// EnqueueAuthenticatedOutboundReplay persists a late, independently verified
// source payout before MarkObserved can change sidecar accounting. The scanner
// owns retries after enrollment, including after restart without a certificate
// retransmission. Committed-accounting imports never enter this path.
func (s *Scanner) EnqueueAuthenticatedOutboundReplay(tx common.ObservedTx) (bool, error) {
	return s.enqueueAuthenticatedOutboundReplayGuarded(tx, nil)
}

var errXMRSpentRefsAdmissionRejected = errors.New("xmr spent-ref replay admission rejected")

// Replay takes scanMu before admission to match the reorg cleanup lock order.
func (s *Scanner) enqueueAuthenticatedOutboundReplayGuarded(tx common.ObservedTx, guard func() (func(), bool)) (bool, error) {
	parsed, err := memo.ParseMemo(common.LatestVersion, tx.Tx.Memo)
	if err != nil || !parsed.IsOutbound() {
		return false, nil
	}
	if s.sidecar == nil || s.reorgStore == nil || s.internalReplayStore == nil {
		return false, errors.New("xmr payout replay is unavailable")
	}
	if err = validateXMRSpentRefsForSidecar(tx); err != nil {
		return false, err
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if guard != nil {
		release, ok := guard()
		if !ok || release == nil {
			return false, errXMRSpentRefsAdmissionRejected
		}
		defer release()
	}
	location, err := s.sidecar.CanonicalTxLocation(tx.Tx.ID.String())
	if err != nil {
		return false, err
	}
	if s.CurrentHeight() < location.Height {
		// MarkSpent completed before this lock: the future canonical scan owns
		// delivery, including if it was waiting for this import to finish.
		return false, nil
	}
	record := authenticatedInternalInboundReplay{
		Kind:              authenticatedReplaySourcePayout,
		TxID:              normalizeTxID(tx.Tx.ID.String()),
		TxKey:             strings.ToLower(strings.TrimSpace(tx.XmrTxKey)),
		BlockHeight:       location.Height,
		BlockHash:         strings.ToLower(strings.TrimSpace(location.BlockHash)),
		PreviousHash:      strings.ToLower(strings.TrimSpace(location.PreviousHash)),
		SourceAddress:     tx.Tx.FromAddress.String(),
		SourceVaultPubKey: tx.ObservedPubKey.String(),
		ToAddress:         tx.Tx.ToAddress.String(),
		AmountThorchain:   tx.Tx.Coins.GetCoin(common.XMRAsset).Amount.Uint64(),
		FeeThorchain:      tx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64(),
		Memo:              strings.TrimSpace(tx.Tx.Memo),
		SpentOutputRefs:   authenticatedInternalSpentRefsFromCommon(tx.SpentOutputRefs),
	}
	if err = s.internalReplayStore.Enqueue(record); err != nil {
		return false, err
	}
	s.logger.Info().Str("txid", record.TxID).Int64("block_height", record.BlockHeight).
		Msg("durably queued authenticated xmr payout source replay")
	return true, nil
}

// persistAuthenticatedPayoutReceipt transfers retry ownership to the existing
// receipt outbox. The caller holds scanMu and retains the pending claim on any
// failure. No destination observation can be constructed from this record kind.
func (s *Scanner) persistAuthenticatedPayoutReceipt(proof sidecar.Transfer, record authenticatedInternalInboundReplay) error {
	if !proof.ObservationEligible || (proof.Origin != sidecar.OutgoingOriginVaultCertified && proof.Origin != sidecar.OutgoingOriginLocalCombined) {
		return errors.New("xmr payout proof is not eligible for source observation")
	}
	current, err := s.sidecar.CanonicalTxLocation(record.TxID)
	if err != nil {
		return err
	}
	if current.Height != record.BlockHeight || !strings.EqualFold(current.BlockHash, record.BlockHash) ||
		!strings.EqualFold(current.PreviousHash, record.PreviousHash) {
		return errors.New("xmr payout moved during replay enrollment")
	}
	sourceVault, err := common.NewPubKey(record.SourceVaultPubKey)
	if err != nil {
		return err
	}
	item, ok := s.transferToTxInItem(proof, record.SourceAddress, record.ToAddress, sourceVault, record.Memo, record.BlockHeight)
	if !ok {
		return errors.New("cannot build authenticated xmr payout observation")
	}
	meta := &reorgBlockMeta{Height: record.BlockHeight, BlockHash: record.BlockHash, PreviousHash: record.PreviousHash, ProvenanceComplete: true}
	return s.reorgStore.PutAuthenticatedObservation(meta, item)
}

// EnqueueAuthenticatedInternalInboundReplay creates a bounded replay request
// only after MarkSpent authenticated a mined migration. Local pre-broadcast
// calls carry a signing height rather than the eventual mined height and are
// deliberately not enrolled; their normal future scan sees the MarkSpent
// metadata directly.
func (s *Scanner) EnqueueAuthenticatedInternalInboundReplay(tx common.ObservedTx) (bool, error) {
	return s.enqueueAuthenticatedInternalInboundReplayGuarded(tx, nil)
}

func (s *Scanner) enqueueAuthenticatedInternalInboundReplayGuarded(tx common.ObservedTx, guard func() (func(), bool)) (bool, error) {
	if s.internalReplayStore == nil || s.reorgStore == nil || s.sidecar == nil || !isXMRTxMigrateMemo(tx.Tx.Memo) {
		return false, nil
	}
	if err := validateXMRSpentRefsForSidecar(tx); err != nil {
		return false, fmt.Errorf("validate authenticated xmr migration replay %s: %w", tx.Tx.ID, err)
	}
	if tx.BlockHeight <= 0 {
		return false, nil
	}
	// The THORChain voter may retain the transaction's pre-reorg height even
	// when Monero mines the same non-expiring transaction on a replacement
	// branch. Locate it through the authenticated sidecar instead of trusting
	// that stale caller-controlled height or scanning an arbitrary range.
	location, err := s.sidecar.CanonicalTxLocation(tx.Tx.ID.String())
	if err != nil {
		return false, fmt.Errorf("locate canonical xmr migration %s: %w", tx.Tx.ID, err)
	}
	if s.CurrentHeight() < location.Height {
		return false, fmt.Errorf(
			"authenticated xmr migration replay %s waits for scanner height %d (current %d)",
			tx.Tx.ID,
			location.Height,
			s.CurrentHeight(),
		)
	}

	s.scanMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.scanMu.Unlock()
		}
	}()
	// Re-check after acquiring the canonical scan lock.
	if s.CurrentHeight() < location.Height {
		return false, fmt.Errorf("authenticated xmr migration replay %s scanner height moved behind %d", tx.Tx.ID, location.Height)
	}
	// Refresh may query THORChain and register vaults, which can rewind the
	// sidecar scanner. Keep scanMu held, but acquire admission afterwards so
	// errata cleanup can finish during this I/O and still reject the replay.
	if refreshErr := s.refreshAsgardLookup(location.Height); refreshErr != nil {
		return false, fmt.Errorf("refresh xmr vault lookup for authenticated replay: %w", refreshErr)
	}
	if guard != nil {
		release, ok := guard()
		if !ok || release == nil {
			return false, errXMRSpentRefsAdmissionRejected
		}
		defer release()
	}
	reverted, err := s.reorgStore.IsRevertedTx(tx.Tx.ID.String())
	if err != nil {
		return false, fmt.Errorf("check reverted xmr migration replay %s: %w", tx.Tx.ID, err)
	}
	if reverted {
		s.logger.Error().Str("txid", tx.Tx.ID.String()).
			Msg("refusing to enqueue permanently reverted xmr migration replay")
		return false, nil
	}
	sourceAddress, err := tx.ObservedPubKey.GetAddress(common.XMRChain)
	if err != nil || sourceAddress.IsEmpty() || !common.AddressEquals(common.XMRChain, sourceAddress, tx.Tx.FromAddress) {
		return false, fmt.Errorf("authenticated xmr migration replay %s has unauthenticated source vault", tx.Tx.ID)
	}
	destinationVault, ok := s.vaultPubKeyForAddress(tx.Tx.ToAddress.String())
	if !ok || destinationVault.IsEmpty() {
		return false, fmt.Errorf("authenticated xmr migration replay %s destination vault is not registered", tx.Tx.ID)
	}
	if destinationVault.Equals(tx.ObservedPubKey) {
		return false, fmt.Errorf("authenticated xmr migration replay %s source and destination vaults are identical", tx.Tx.ID)
	}
	destinationAddress, err := destinationVault.GetAddress(common.XMRChain)
	if err != nil || destinationAddress.IsEmpty() || !common.AddressEquals(common.XMRChain, destinationAddress, tx.Tx.ToAddress) {
		return false, fmt.Errorf("authenticated xmr migration replay %s has invalid destination vault", tx.Tx.ID)
	}
	coin := tx.Tx.Coins.GetCoin(common.XMRAsset)
	if coin.IsEmpty() {
		return false, fmt.Errorf("authenticated xmr migration replay %s has no xmr amount", tx.Tx.ID)
	}
	amountThorchain := coin.Amount.Uint64()

	meta, err := s.reorgStore.Get(location.Height)
	if err != nil {
		return false, err
	}
	switch {
	case meta == nil:
		// Catch-up may discover a migration after the ordinary bounded journal
		// entry was pruned or lost. The live canonical block contains the exact
		// transaction, so recreate a binding instead of silently abandoning the
		// destination credit.
		meta = &reorgBlockMeta{
			Height:                  location.Height,
			BlockHash:               location.BlockHash,
			PreviousHash:            location.PreviousHash,
			CustomerTransactions:    []string{normalizeTxID(tx.Tx.ID.String())},
			VaultOriginTransactions: []string{normalizeTxID(tx.Tx.ID.String())},
			ProvenanceComplete:      true,
		}
		if err = s.reorgStore.Set(meta); err != nil {
			return false, fmt.Errorf("create xmr migration replay journal binding: %w", err)
		}
	case !strings.EqualFold(strings.TrimSpace(location.BlockHash), strings.TrimSpace(meta.BlockHash)) ||
		!strings.EqualFold(strings.TrimSpace(location.PreviousHash), strings.TrimSpace(meta.PreviousHash)):
		return false, fmt.Errorf("xmr migration replay %s conflicts with canonical journal at height %d", tx.Tx.ID, location.Height)
	default:
		changed := false
		if !slices.ContainsFunc(meta.CustomerTransactions, func(candidate string) bool {
			return normalizeTxID(candidate) == normalizeTxID(tx.Tx.ID.String())
		}) {
			meta.CustomerTransactions = append(meta.CustomerTransactions, normalizeTxID(tx.Tx.ID.String()))
			changed = true
		}
		changed = addVaultOriginToMeta(meta, tx.Tx.ID.String()) || changed
		if changed {
			if err = s.reorgStore.Set(meta); err != nil {
				return false, fmt.Errorf("persist xmr migration vault-origin journal binding: %w", err)
			}
		}
	}
	if _, err = s.reorgStore.UpgradeRelocationWatchVaultOrigin(tx.Tx.ID.String()); err != nil {
		return false, fmt.Errorf("upgrade authenticated xmr migration relocation watch: %w", err)
	}
	gasCoin := tx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
	if gasCoin.IsEmpty() || gasCoin.Amount.IsZero() {
		return false, fmt.Errorf("authenticated xmr migration replay %s has no xmr fee", tx.Tx.ID)
	}
	feeThorchain := gasCoin.Amount.Uint64()

	record := authenticatedInternalInboundReplay{
		TxID:                   normalizeTxID(tx.Tx.ID.String()),
		TxKey:                  strings.ToLower(strings.TrimSpace(tx.XmrTxKey)),
		BlockHeight:            location.Height,
		BlockHash:              strings.ToLower(strings.TrimSpace(location.BlockHash)),
		PreviousHash:           strings.ToLower(strings.TrimSpace(location.PreviousHash)),
		SourceAddress:          sourceAddress.String(),
		SourceVaultPubKey:      tx.ObservedPubKey.String(),
		ToAddress:              destinationAddress.String(),
		DestinationVaultPubKey: destinationVault.String(),
		AmountThorchain:        amountThorchain,
		FeeThorchain:           feeThorchain,
		Memo:                   strings.TrimSpace(tx.Tx.Memo),
		SpentOutputRefs:        authenticatedInternalSpentRefsFromCommon(tx.SpentOutputRefs),
	}
	// Persist the replay identity before mutating sidecar reconciliation. A
	// crash at any later point leaves a claim which can repeat MarkObserved.
	if err = s.internalReplayStore.Enqueue(record); err != nil {
		return false, err
	}
	// The canonical identity and durable outbox are now stable. Do not hold the
	// scanner lock across sidecar/daemon I/O from an attestation path.
	s.scanMu.Unlock()
	locked = false
	proof, err := s.sidecar.MarkObserved(tx.ObservedPubKey.String(), tx.Tx.ID.String(), location.Height, location.BlockHash)
	if err != nil {
		return true, fmt.Errorf("mark canonical xmr migration replay %s observed: %w", tx.Tx.ID, err)
	}
	if err = s.validateAuthenticatedMigrationProofForObserved(proof, tx, location.Height, amountThorchain); err != nil {
		return true, fmt.Errorf("validate canonical xmr migration replay %s: %w", tx.Tx.ID, err)
	}
	if err = s.validateAuthenticatedMigrationProofForRecord(proof, record); err != nil {
		return true, fmt.Errorf("bind canonical xmr migration replay %s proof: %w", tx.Tx.ID, err)
	}
	s.logger.Info().Str("txid", record.TxID).Int64("block_height", record.BlockHeight).
		Msg("durably queued authenticated xmr migration destination replay")
	return true, nil
}

func (s *Scanner) replayAuthenticatedInternalInbounds(fetchHeight, _ int64) ([]*stypes.TxInItem, []authenticatedInternalInboundClaim, error) {
	if s.internalReplayStore == nil {
		return nil, nil, nil
	}
	if s.reorgStore == nil {
		return nil, nil, errors.New("xmr internal replay requires reverted transaction quarantine")
	}
	claims, err := s.internalReplayStore.Claim(maxAuthenticatedInternalInboundClaims)
	if err != nil {
		return nil, nil, fmt.Errorf("claim authenticated xmr replays: %w", err)
	}
	completed := false
	defer func() {
		if !completed {
			s.releaseAuthenticatedInternalInboundClaims(claims)
		}
	}()
	items := make([]*stypes.TxInItem, 0, len(claims))
	handedOff := make([]authenticatedInternalInboundClaim, 0, len(claims))
	for _, claim := range claims {
		record := claim.Record
		reverted, revertedErr := s.reorgStore.IsRevertedTx(record.TxID)
		if revertedErr != nil {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, errors.Join(revertedErr, releaseErr)
			}
			return nil, handedOff, revertedErr
		}
		if reverted {
			if invalidateErr := s.internalReplayStore.Invalidate(claim); invalidateErr != nil {
				return nil, handedOff, invalidateErr
			}
			s.logger.Error().Str("txid", record.TxID).
				Msg("discarded authenticated replay for permanently reverted xmr transaction")
			continue
		}
		location, locationErr := s.sidecar.CanonicalTxLocation(record.TxID)
		if locationErr != nil {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, releaseErr
			}
			s.logger.Warn().Err(locationErr).Str("txid", record.TxID).
				Msg("retain authenticated xmr replay until transaction is canonically mined")
			continue
		}
		// A future canonical entry would move the journal beyond the scanned
		// boundary and hide a fork below it. Let ordinary scanning reconcile
		// that block before relocating, marking or emitting either job type.
		if location.Height > fetchHeight {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, releaseErr
			}
			continue
		}
		if location.Height != record.BlockHeight ||
			!strings.EqualFold(location.BlockHash, record.BlockHash) ||
			!strings.EqualFold(location.PreviousHash, record.PreviousHash) {
			relocated, relocateErr := s.internalReplayStore.Relocate(
				claim, location.Height, location.BlockHash, location.PreviousHash,
			)
			if relocateErr != nil {
				if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
					return nil, handedOff, errors.Join(relocateErr, releaseErr)
				}
				return nil, handedOff, relocateErr
			}
			claim = relocated
			record = relocated.Record
			s.logger.Info().Str("txid", record.TxID).Int64("block_height", record.BlockHeight).
				Msg("rebound authenticated xmr replay after canonical relocation")
		}

		if record.Kind == authenticatedReplaySourcePayout {
			// Retain reorg ownership even if the process stops after MarkObserved
			// but before the receipt write. The durable claim owns the retry.
			meta := &reorgBlockMeta{Height: record.BlockHeight, BlockHash: record.BlockHash, PreviousHash: record.PreviousHash, ProvenanceComplete: true}
			if journalErr := s.reorgStore.PutAuthenticatedOutbound(meta, record.TxID); journalErr != nil {
				return nil, handedOff, journalErr
			}
		}

		proof, markErr := s.sidecar.MarkObserved(record.SourceVaultPubKey, record.TxID, record.BlockHeight, record.BlockHash)
		if markErr != nil {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, releaseErr
			}
			s.logger.Warn().Err(markErr).Str("txid", record.TxID).
				Msg("release authenticated xmr replay after canonical sidecar reconciliation failure")
			continue
		}
		if proofErr := s.validateAuthenticatedMigrationProofForRecord(proof, record); proofErr != nil {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, releaseErr
			}
			s.logger.Warn().Err(proofErr).Str("txid", record.TxID).
				Msg("release authenticated xmr replay without exact durable sidecar proof")
			continue
		}
		if record.Kind == authenticatedReplaySourcePayout {
			if receiptErr := s.persistAuthenticatedPayoutReceipt(proof, record); receiptErr != nil {
				if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
					return nil, handedOff, errors.Join(receiptErr, releaseErr)
				}
				s.logger.Warn().Err(receiptErr).Str("txid", record.TxID).
					Msg("retain authenticated xmr payout replay after receipt handoff failure")
				continue
			}
			// Receipt + canonical journal are now synced atomically. Only this
			// handoff (or permanent-revert quarantine above) permits removal.
			// A failed acknowledgement safely repeats the idempotent handoff.
			if ackErr := s.internalReplayStore.Acknowledge(claim); ackErr != nil {
				return nil, handedOff, ackErr
			}
			continue
		}
		sourceItem, destinationItem, transferErr := s.authenticatedMigrationItemsFromProof(proof, record)
		if transferErr != nil {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, errors.Join(transferErr, releaseErr)
			}
			return nil, handedOff, transferErr
		}
		bound, journalErr := s.bindAuthenticatedReplayToJournal(record)
		if journalErr != nil {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, errors.Join(journalErr, releaseErr)
			}
			return nil, handedOff, journalErr
		}
		if !bound {
			if releaseErr := s.internalReplayStore.Release(claim); releaseErr != nil {
				return nil, handedOff, releaseErr
			}
			s.logger.Warn().Str("txid", record.TxID).
				Msg("retain authenticated xmr migration replay after journal relocation race")
			continue
		}
		claimCopy := claim
		onStored := func() {
			if ackErr := s.internalReplayStore.Acknowledge(claimCopy); ackErr != nil {
				s.logger.Error().Err(ackErr).Str("txid", claimCopy.Record.TxID).
					Msg("failed to acknowledge persisted xmr migration replay")
				if releaseErr := s.internalReplayStore.Release(claimCopy); releaseErr != nil {
					s.logger.Error().Err(releaseErr).Str("txid", claimCopy.Record.TxID).
						Msg("failed to release xmr migration replay after acknowledgement failure")
				}
			}
		}
		onRejected := func(rejectErr error) {
			if releaseErr := s.internalReplayStore.Release(claimCopy); releaseErr != nil {
				s.logger.Error().Err(releaseErr).Str("txid", claimCopy.Record.TxID).
					Msg("failed to release rejected xmr migration replay")
				return
			}
			s.logger.Warn().Err(rejectErr).Str("txid", claimCopy.Record.TxID).
				Msg("released rejected xmr migration replay")
		}
		replayItems := []*stypes.TxInItem{destinationItem}
		// A source-vault certificate can arrive after the canonical scan passed
		// this block. Replay its independently verified source observation too;
		// destination-only recovery leaves the migration outbound without quorum.
		// Chain-committed pairing proofs must never create fresh source votes.
		if proof.ObservationEligible && (proof.Origin == sidecar.OutgoingOriginLocalCombined ||
			proof.Origin == sidecar.OutgoingOriginVaultCertified) {
			replayItems = append(replayItems, sourceItem)
		}
		bindAuthenticatedMigrationHandoff(replayItems, onStored, onRejected)
		items = append(items, replayItems...)
		handedOff = append(handedOff, claim)
	}
	completed = true
	return items, handedOff, nil
}

// bindAuthenticatedMigrationHandoff keeps the durable replay until every
// observation is persisted. A partial handoff or crash retries the whole pair;
// the observer already deduplicates by transaction and observed vault.
func bindAuthenticatedMigrationHandoff(items []*stypes.TxInItem, onStored func(), onRejected func(error)) {
	var mu sync.Mutex
	remaining := len(items)
	var terminal sync.Once
	for _, item := range items {
		var itemDone sync.Once
		item.OnObservedTxStored = func() {
			itemDone.Do(func() {
				mu.Lock()
				remaining--
				complete := remaining == 0
				mu.Unlock()
				if complete {
					terminal.Do(onStored)
				}
			})
		}
		item.OnObservedTxRejected = func(err error) {
			itemDone.Do(func() {
				terminal.Do(func() { onRejected(err) })
			})
		}
	}
}

func (s *Scanner) releaseAuthenticatedInternalInboundClaims(claims []authenticatedInternalInboundClaim) {
	for _, claim := range claims {
		if err := s.internalReplayStore.Release(claim); err != nil {
			s.logger.Error().Err(err).Str("txid", claim.Record.TxID).
				Msg("failed to release xmr migration replay before observer handoff")
		}
	}
}

func (s *Scanner) bindAuthenticatedReplayToJournal(record authenticatedInternalInboundReplay) (bool, error) {
	upgradeWatch := func() error {
		_, err := s.reorgStore.UpgradeRelocationWatchVaultOrigin(record.TxID)
		return err
	}
	meta, err := s.reorgStore.Get(record.BlockHeight)
	if err != nil {
		return false, fmt.Errorf("load xmr migration replay journal %s: %w", record.TxID, err)
	}
	if meta == nil {
		meta = &reorgBlockMeta{
			Height:                  record.BlockHeight,
			BlockHash:               record.BlockHash,
			PreviousHash:            record.PreviousHash,
			CustomerTransactions:    []string{record.TxID},
			VaultOriginTransactions: []string{record.TxID},
			ProvenanceComplete:      true,
		}
		if err = s.reorgStore.Set(meta); err != nil {
			return false, fmt.Errorf("create xmr migration replay journal %s: %w", record.TxID, err)
		}
		if err = upgradeWatch(); err != nil {
			return false, fmt.Errorf("upgrade xmr migration replay watch %s: %w", record.TxID, err)
		}
		return true, nil
	}
	if !strings.EqualFold(strings.TrimSpace(meta.BlockHash), strings.TrimSpace(record.BlockHash)) ||
		!strings.EqualFold(strings.TrimSpace(meta.PreviousHash), strings.TrimSpace(record.PreviousHash)) {
		return false, nil
	}
	for _, txID := range meta.CustomerTransactions {
		if normalizeTxID(txID) == normalizeTxID(record.TxID) {
			if addVaultOriginToMeta(meta, record.TxID) {
				if err = s.reorgStore.Set(meta); err != nil {
					return false, fmt.Errorf("persist xmr migration replay vault-origin binding %s: %w", record.TxID, err)
				}
			}
			if err = upgradeWatch(); err != nil {
				return false, fmt.Errorf("upgrade xmr migration replay watch %s: %w", record.TxID, err)
			}
			return true, nil
		}
	}
	meta.CustomerTransactions = append(meta.CustomerTransactions, record.TxID)
	addVaultOriginToMeta(meta, record.TxID)
	if err = s.reorgStore.Set(meta); err != nil {
		return false, fmt.Errorf("bind xmr migration replay %s to reorg journal: %w", record.TxID, err)
	}
	if err = upgradeWatch(); err != nil {
		return false, fmt.Errorf("upgrade xmr migration replay watch %s: %w", record.TxID, err)
	}
	return true, nil
}

func authenticatedInternalSpentRefsFromSidecar(refs []sidecar.SpentRef) []authenticatedInternalSpentRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]authenticatedInternalSpentRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, authenticatedInternalSpentRef{
			TxHash:      strings.ToLower(strings.TrimSpace(ref.TxHash)),
			OutputIndex: ref.OutputIndex,
			KeyImage:    strings.ToLower(strings.TrimSpace(ref.KeyImage)),
		})
	}
	return out
}

func authenticatedInternalSpentRefsFromCommon(refs []common.OutputRef) []authenticatedInternalSpentRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]authenticatedInternalSpentRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, authenticatedInternalSpentRef{
			TxHash:      strings.ToLower(strings.TrimSpace(ref.TxHash)),
			OutputIndex: ref.OutputIndex,
			KeyImage:    strings.ToLower(strings.TrimSpace(ref.KeyImage)),
		})
	}
	return out
}

func (s *Scanner) validateAuthenticatedMigrationProofForObserved(
	proof sidecar.Transfer,
	tx common.ObservedTx,
	canonicalHeight int64,
	amountThorchain uint64,
) error {
	if normalizeTxID(proof.TxID) != normalizeTxID(tx.Tx.ID.String()) {
		return errors.New("sidecar proof transaction hash mismatch")
	}
	if int64(proof.Height) != canonicalHeight || proof.Locked {
		return errors.New("sidecar proof is not mined at the requested height")
	}
	if !common.AddressEquals(common.XMRChain, common.Address(proof.Address), tx.Tx.ToAddress) ||
		proof.Amount/moneroAtomicToThorchain != amountThorchain {
		return errors.New("sidecar proof payment mismatch")
	}
	if strings.TrimSpace(proof.Note) != strings.TrimSpace(tx.Tx.Memo) {
		return errors.New("sidecar proof memo mismatch")
	}
	if !strings.EqualFold(strings.TrimSpace(proof.TxKey), strings.TrimSpace(tx.XmrTxKey)) || !hasUsableXMROutgoingTxKey(proof) {
		return errors.New("sidecar proof tx key mismatch")
	}
	sender, sourceVault := s.resolveOutgoingSender(proof)
	if sender == "" || !sourceVault.Equals(tx.ObservedPubKey) ||
		!common.AddressEquals(common.XMRChain, common.Address(sender), tx.Tx.FromAddress) {
		return errors.New("sidecar proof source vault mismatch")
	}
	if proof.Fee == 0 {
		return errors.New("sidecar proof has zero fee")
	}
	gasCoin := tx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
	if gasCoin.IsEmpty() || gasCoin.Amount.IsZero() ||
		proof.Fee/moneroAtomicToThorchain != gasCoin.Amount.Uint64() {
		return errors.New("sidecar proof fee differs at THORChain precision")
	}
	switch strings.TrimSpace(proof.Origin) {
	case sidecar.OutgoingOriginLocalCombined, sidecar.OutgoingOriginVaultCertified, sidecar.OutgoingOriginChainCommitted:
	default:
		return errors.New("sidecar proof has unauthenticated origin")
	}
	actualRefs := authenticatedInternalSpentRefsFromSidecar(proof.SpentOutputRefs)
	expectedRefs := authenticatedInternalSpentRefsFromCommon(tx.SpentOutputRefs)
	if !authenticatedInternalSpentRefsEqual(actualRefs, expectedRefs) {
		return errors.New("sidecar proof spent refs mismatch")
	}
	for _, ref := range proof.SpentOutputRefs {
		if spendHash := strings.TrimSpace(ref.SpendTxHash); spendHash != "" && normalizeTxID(spendHash) != normalizeTxID(tx.Tx.ID.String()) {
			return errors.New("sidecar proof spent ref names a different spend")
		}
	}
	return nil
}

func (s *Scanner) validateAuthenticatedMigrationProofForRecord(
	proof sidecar.Transfer,
	record authenticatedInternalInboundReplay,
) error {
	if err := validateAuthenticatedInternalInboundReplay(record); err != nil {
		return err
	}
	if proof.Locked {
		return errors.New("sidecar proof is not durably observed")
	}
	if normalizeTxID(proof.TxID) != normalizeTxID(record.TxID) ||
		!strings.EqualFold(strings.TrimSpace(proof.TxKey), strings.TrimSpace(record.TxKey)) ||
		int64(proof.Height) != record.BlockHeight ||
		!common.AddressEquals(common.XMRChain, common.Address(proof.FromAddress), common.Address(record.SourceAddress)) ||
		strings.TrimSpace(proof.VaultPubKey) != strings.TrimSpace(record.SourceVaultPubKey) ||
		!common.AddressEquals(common.XMRChain, common.Address(proof.Address), common.Address(record.ToAddress)) ||
		proof.Amount/moneroAtomicToThorchain != record.AmountThorchain ||
		proof.Fee == 0 || proof.Fee/moneroAtomicToThorchain != record.FeeThorchain ||
		strings.TrimSpace(proof.Note) != strings.TrimSpace(record.Memo) ||
		!authenticatedInternalSpentRefsEqual(
			authenticatedInternalSpentRefsFromSidecar(proof.SpentOutputRefs),
			record.SpentOutputRefs,
		) {
		return errors.New("sidecar proof changed durable replay identity")
	}
	if !hasUsableXMROutgoingTxKey(proof) {
		return errors.New("sidecar proof has invalid tx key")
	}
	switch strings.TrimSpace(proof.Origin) {
	case sidecar.OutgoingOriginLocalCombined, sidecar.OutgoingOriginVaultCertified, sidecar.OutgoingOriginChainCommitted:
	default:
		return errors.New("sidecar proof has unauthenticated origin")
	}
	for _, ref := range proof.SpentOutputRefs {
		if spendHash := strings.TrimSpace(ref.SpendTxHash); spendHash != "" && normalizeTxID(spendHash) != normalizeTxID(record.TxID) {
			return errors.New("sidecar proof spent ref names a different spend")
		}
	}
	return nil
}

func (s *Scanner) authenticatedMigrationItemsFromProof(
	proof sidecar.Transfer,
	record authenticatedInternalInboundReplay,
) (*stypes.TxInItem, *stypes.TxInItem, error) {
	sourceVault, err := common.NewPubKey(record.SourceVaultPubKey)
	if err != nil || sourceVault.IsEmpty() {
		return nil, nil, errors.New("authenticated migration source vault is invalid")
	}
	destinationVault, err := common.NewPubKey(record.DestinationVaultPubKey)
	if err != nil || destinationVault.IsEmpty() {
		return nil, nil, errors.New("authenticated migration destination vault is invalid")
	}
	source, ok := s.transferToTxInItem(
		proof,
		record.SourceAddress,
		record.ToAddress,
		sourceVault,
		record.Memo,
		record.BlockHeight,
	)
	if !ok {
		return nil, nil, errors.New("sidecar proof could not form source authentication item")
	}
	source.SpentOutputRefs = make([]*common.OutputRef, 0, len(record.SpentOutputRefs))
	for _, ref := range record.SpentOutputRefs {
		copyRef := &common.OutputRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: record.TxID,
		}
		source.SpentOutputRefs = append(source.SpentOutputRefs, copyRef)
	}

	// The destination observation intentionally omits outbound-only tx_key and
	// spent-ref metadata, while retaining the exact outgoing fee so all peers
	// construct identical gas for the internal transfer.
	destinationProof := proof
	destinationProof.TxKey = ""
	destinationProof.SpentOutputRefs = nil
	destination, ok := s.transferToTxInItem(
		destinationProof,
		record.SourceAddress,
		record.ToAddress,
		destinationVault,
		record.Memo,
		record.BlockHeight,
	)
	if !ok {
		return nil, nil, errors.New("sidecar proof could not form destination observation")
	}
	return source, destination, nil
}

func (s *Scanner) fetchTransfersByHeight(fetchHeight, chainHeight int64, blockHash, parentHash string) ([]*stypes.TxInItem, error) {
	if s.sidecar == nil || s.bridge == nil {
		return nil, nil
	}
	s.pruneObservedOutgoing(fetchHeight)

	resp, err := s.sidecar.SyncAt(fetchHeight, chainHeight, blockHash, parentHash, s.maxReorgRescan)
	if err != nil {
		if isFrostScannerHashMismatchErr(err) {
			// Refresh the authoritative cursor so a restart with an empty Go cache
			// can enter durable catch-up on the next bounded Fetch retry.
			_, _ = s.sidecar.ScannerState()
			return nil, btypes.ErrUnavailableBlock
		}
		// A cursor discontinuity is not a transient fetch failure. Map it so
		// BlockScanner either rewinds within the automatic window or stops for
		// the operator, instead of re-fetching this height forever.
		return nil, s.mapScannerContinuityError(fetchHeight, err)
	}
	return s.transfersToTxInItems(resp, fetchHeight)
}

func (s *Scanner) mapScannerContinuityError(fetchHeight int64, err error) error {
	var rewind *sidecar.ScannerRewindRequiredError
	if errors.As(err, &rewind) {
		distance := fetchHeight - rewind.NextHeight
		if rewind.NextHeight <= 0 || rewind.NextHeight >= fetchHeight || distance > btypes.MaxCursorRewindBlocks {
			return &btypes.ScannerRecoveryRequiredError{
				CurrentHeight: fetchHeight,
				NextHeight:    rewind.NextHeight,
				Reason: fmt.Sprintf(
					"authenticated sidecar cursor discontinuity is outside the automatic 1..%d block recovery window (reset epoch %d); restore compatible sidecar state or deliberately reset the Bifrost cursor",
					btypes.MaxCursorRewindBlocks,
					rewind.ResetEpoch,
				),
			}
		}
		return &btypes.RewindRequiredError{NextHeight: rewind.NextHeight}
	}
	// The sidecar has durably scanned PAST this height. Unlike a rewind, no
	// amount of retrying repairs it, and rewinding forward to meet the sidecar
	// would skip the intervening blocks without observing them. Map it to the
	// terminal scanner error so BlockScanner stops for the operator instead of
	// re-fetching this height forever.
	var recovery *sidecar.ScannerRecoveryRequiredError
	if errors.As(err, &recovery) {
		return &btypes.ScannerRecoveryRequiredError{
			CurrentHeight: fetchHeight,
			NextHeight:    recovery.NextHeight,
			Reason: fmt.Sprintf(
				"authenticated sidecar cursor is ahead of the requested height (reset epoch %d); "+
					"the sidecar has already scanned past this block, so retrying cannot recover: "+
					"restore compatible sidecar state or deliberately reset the Bifrost cursor",
				recovery.ResetEpoch,
			),
		}
	}
	if isFrostScannerHashMismatchErr(err) {
		return btypes.ErrUnavailableBlock
	}
	return fmt.Errorf("fail to get monero transfers at %d: %w", fetchHeight, err)
}

func (s *Scanner) cursorReplayLimit() int64 {
	limit := s.maxReorgRescan
	if limit > maxSidecarScannerRewindBlocks {
		limit = maxSidecarScannerRewindBlocks
	}
	if limit < 0 {
		return 0
	}
	return limit
}

func (s *Scanner) churnRewindLimit() int64 {
	if s.cursorReplayLimit() == 0 {
		return 0
	}
	return maxChurnScannerRewindBlocks
}

// advanceSidecarCatchup drives at most one bounded chunk of a registration-
// or rollback-triggered historical scan. The durable marker advances only
// after that response, its observations, and its journal entry are fsynced in
// one batch. Retrying after an ambiguous response rewinds at most one block;
// birthday gaps make progress across bounded Fetch retries. The initial churn
// rewind uses the signer's supported window independently of the batch size.
func (s *Scanner) advanceSidecarCatchup(targetHeight, chainHeight int64) (bool, *scannerCatchupMarker, error) {
	return s.advanceSidecarCatchupThrough(targetHeight, targetHeight, chainHeight)
}

// advanceSidecarCatchupThrough preserves targetHeight in the durable marker
// while stopping this invocation at stopHeight. Reorg repair uses this to
// durably reach its common ancestor without discarding an existing later Fetch
// target (and its accumulated receipts).
func (s *Scanner) advanceSidecarCatchupThrough(
	targetHeight, stopHeight, chainHeight int64,
) (bool, *scannerCatchupMarker, error) {
	if s.sidecar == nil {
		return false, nil, errors.New("monero sidecar is nil")
	}
	marker, err := s.reorgStore.GetCatchupMarker()
	if err != nil {
		return false, nil, err
	}

	var state sidecar.ScannerState
	if marker == nil {
		cached, known := s.sidecar.CachedScannerState()
		if !known || cached.NextHeight == nil {
			return false, nil, nil
		}
		next := int64(*cached.NextHeight)
		if next >= targetHeight && next <= targetHeight+1 {
			return false, nil, nil
		}
	}
	state, err = s.sidecar.ScannerState()
	if err != nil {
		return false, nil, fmt.Errorf("fetch sidecar scanner cursor: %w", err)
	}
	if state.NextHeight == nil {
		return false, nil, errors.New("sidecar lost scanner cursor during durable catch-up")
	}
	nextHeight := int64(*state.NextHeight)

	switch {
	case marker == nil:
		switch {
		case nextHeight < targetHeight || nextHeight > targetHeight+1:
			if s.reorgStore == nil {
				return false, nil, errors.New("monero scanner cannot catch up without durable storage")
			}
			if nextHeight <= 0 {
				return false, nil, fmt.Errorf("invalid sidecar catch-up start height %d", nextHeight)
			}
			// Churn-in rolls Bifrost back between scans while the signer retains
			// its durable cursor. Enroll the requested block in the same recovery
			// journal as birthday catch-up before rewinding the signer below.
			// Same-birthday registration cannot do this, and skipping forward
			// would lose observations needed by the newly active validator.
			startHeight := min(nextHeight, targetHeight)
			if limit := s.churnRewindLimit(); nextHeight-startHeight > limit {
				return false, nil, fmt.Errorf("sidecar scanner cursor %d is ahead of requested height %d beyond rewind limit %d", nextHeight, targetHeight, limit)
			}
			anchor, anchorErr := s.sidecar.ChainBlock(startHeight - 1)
			if anchorErr != nil {
				return false, nil, fmt.Errorf("fetch xmr catch-up anchor %d: %w", startHeight-1, anchorErr)
			}
			marker = &scannerCatchupMarker{
				StartHeight:   startHeight,
				TargetHeight:  targetHeight,
				AnchorHeight:  startHeight - 1,
				AnchorHash:    anchor.Hash,
				ChurnRollback: nextHeight > targetHeight,
			}
			if err = s.reorgStore.BeginCatchup(*marker); err != nil {
				return false, nil, err
			}
		default:
			return false, nil, nil
		}
	case targetHeight < marker.TargetHeight:
		// A queued churn rollback can interrupt a catch-up retry. Keep its
		// earlier unfinished history and all receipts, but finish at Bifrost's
		// new target so it can resume ordinary ascending scans afterwards.
		startHeight := min(marker.StartHeight, targetHeight)
		if limit := s.churnRewindLimit(); limit == 0 || nextHeight-startHeight > limit {
			return true, nil, fmt.Errorf("sidecar scanner cursor %d is ahead of requested height %d beyond rewind limit %d", nextHeight, targetHeight, limit)
		}
		if marker.StartHeight > targetHeight {
			anchor, anchorErr := s.sidecar.ChainBlock(targetHeight - 1)
			if anchorErr != nil {
				return true, nil, anchorErr
			}
			marker.StartHeight = targetHeight
			marker.AnchorHeight = targetHeight - 1
			marker.AnchorHash = anchor.Hash
		}
		marker.TargetHeight = targetHeight
		marker.ChurnRollback = true
		if err = s.reorgStore.BeginCatchup(*marker); err != nil {
			return true, nil, err
		}
	case targetHeight > marker.TargetHeight && marker.ChurnRollback && !marker.completed():
		// Startup clamping or stall recovery can move Bifrost forward while a
		// churn rewind/replay is unfinished. Extend its destination, retaining
		// the crash-safe start, anchor and receipts. Registration-only catch-up
		// keeps its existing policy; arbitrary relocation is outside this fix.
		marker.TargetHeight = targetHeight
		if err = s.reorgStore.BeginCatchup(*marker); err != nil {
			return true, nil, err
		}
	case marker.TargetHeight != targetHeight:
		if !marker.completed() {
			return true, nil, fmt.Errorf(
				"xmr scanner catch-up targets height %d while blockscanner requested %d",
				marker.TargetHeight, targetHeight,
			)
		}
		// BlockScanner may persist H after Fetch returns but before Observer's
		// receipt callback. On restart it asks for H+1. Extend the completed
		// session and keep all H receipts instead of permanently mismatching.
		oldTarget := marker.TargetHeight
		canonicalOld, canonicalErr := s.sidecar.ChainBlock(oldTarget)
		if canonicalErr != nil {
			return true, nil, canonicalErr
		}
		if strings.EqualFold(canonicalOld.Hash, marker.AnchorHash) {
			marker.TargetHeight = targetHeight
		} else {
			if oldTarget <= 0 {
				return true, nil, errors.New("cannot reopen genesis xmr catch-up receipt")
			}
			anchor, anchorErr := s.sidecar.ChainBlock(oldTarget - 1)
			if anchorErr != nil {
				return true, nil, anchorErr
			}
			marker = &scannerCatchupMarker{
				StartHeight:   oldTarget,
				TargetHeight:  targetHeight,
				AnchorHeight:  oldTarget - 1,
				AnchorHash:    anchor.Hash,
				ChurnRollback: marker.ChurnRollback,
			}
		}
		if err = s.reorgStore.BeginCatchup(*marker); err != nil {
			return true, nil, err
		}
	}

	// A completed receipt can outlive a same-height reorg before Observer
	// acknowledges it. Re-open the final height so the canonical replacement
	// overwrites that receipt rather than handing off the orphan.
	if marker.completed() {
		canonical, canonicalErr := s.sidecar.ChainBlock(marker.TargetHeight)
		if canonicalErr != nil {
			return true, nil, canonicalErr
		}
		if strings.EqualFold(canonical.Hash, marker.AnchorHash) && nextHeight == marker.StartHeight {
			return true, marker, nil
		}
		if !strings.EqualFold(canonical.Hash, marker.AnchorHash) && marker.TargetHeight <= 0 {
			return true, nil, errors.New("cannot reopen genesis xmr catch-up receipt")
		}
		if !strings.EqualFold(canonical.Hash, marker.AnchorHash) {
			anchor, anchorErr := s.sidecar.ChainBlock(marker.TargetHeight - 1)
			if anchorErr != nil {
				return true, nil, anchorErr
			}
			marker = &scannerCatchupMarker{
				StartHeight:   marker.TargetHeight,
				TargetHeight:  marker.TargetHeight,
				AnchorHeight:  marker.TargetHeight - 1,
				AnchorHash:    anchor.Hash,
				ChurnRollback: marker.ChurnRollback,
			}
			if err = s.reorgStore.BeginCatchup(*marker); err != nil {
				return true, nil, err
			}
		}
	}

	// Registration may discover an even older vault while catch-up is in
	// flight. Move the durable start back and replay/merge every affected height;
	// accumulated receipts at later heights remain until overwritten.
	if nextHeight < marker.StartHeight {
		if nextHeight <= 0 {
			return true, nil, fmt.Errorf("invalid sidecar registration rewind height %d", nextHeight)
		}
		anchor, anchorErr := s.sidecar.ChainBlock(nextHeight - 1)
		if anchorErr != nil {
			return true, nil, anchorErr
		}
		marker.StartHeight = nextHeight
		marker.AnchorHeight = nextHeight - 1
		marker.AnchorHash = anchor.Hash
		if err = s.reorgStore.BeginCatchup(*marker); err != nil {
			return true, nil, err
		}
	}

	// processReorg may have replaced the marker's anchor journal while retaining
	// the same next height. Rebind the crash-rewind marker to the now-canonical
	// hash before issuing another sidecar SyncAt.
	canonicalAnchor, anchorErr := s.sidecar.ChainBlock(marker.AnchorHeight)
	if anchorErr != nil {
		return true, nil, anchorErr
	}
	if !strings.EqualFold(canonicalAnchor.Hash, marker.AnchorHash) {
		marker.AnchorHash = canonicalAnchor.Hash
		if err = s.reorgStore.BeginCatchup(*marker); err != nil {
			return true, nil, err
		}
	}

	if nextHeight > marker.StartHeight {
		distance := nextHeight - marker.StartHeight
		limit := s.cursorReplayLimit()
		if marker.ChurnRollback {
			limit = s.churnRewindLimit()
		}
		if limit == 0 || distance > limit {
			return true, nil, fmt.Errorf("xmr catch-up ambiguous advance %d exceeds rewind limit %d", distance, limit)
		}
		resp, rewindErr := s.sidecar.RewindScanner(sidecar.ScannerRewindReq{
			ExpectedResetEpoch: state.ResetEpoch,
			ExpectedNextHeight: uint64(nextHeight),
			TargetNextHeight:   uint64(marker.StartHeight),
			AnchorHeight:       uint64(marker.AnchorHeight),
			AnchorHash:         marker.AnchorHash,
		})
		if rewindErr != nil {
			return true, nil, rewindErr
		}
		if int64(resp.NextHeight) != marker.StartHeight {
			return true, nil, errors.New("sidecar catch-up rewind returned an invalid next height")
		}
	}

	limit := s.cursorReplayLimit()
	if limit == 0 {
		return true, nil, errors.New("xmr historical scanner catch-up is disabled")
	}
	endHeight := marker.StartHeight + limit - 1
	if endHeight > marker.TargetHeight {
		endHeight = marker.TargetHeight
	}
	if endHeight > stopHeight {
		endHeight = stopHeight
	}
	for height := marker.StartHeight; height <= endHeight; height++ {
		canonical, blockErr := s.sidecar.ChainBlock(height)
		if blockErr != nil {
			if errors.Is(blockErr, sidecar.ErrUnavailableBlock) {
				return true, nil, btypes.ErrUnavailableBlock
			}
			return true, nil, fmt.Errorf("fetch xmr catch-up block %d: %w", height, blockErr)
		}
		items, scanErr := s.fetchTransfersByHeight(height, chainHeight, canonical.Hash, canonical.PreviousHash)
		if scanErr != nil {
			return true, nil, scanErr
		}
		meta := s.blockMetaForItems(canonical, height, items)
		next := scannerCatchupMarker{
			StartHeight:   height + 1,
			TargetHeight:  marker.TargetHeight,
			AnchorHeight:  height,
			AnchorHash:    canonical.Hash,
			ChurnRollback: marker.ChurnRollback,
		}
		if err = s.reorgStore.CheckpointCatchup(next, meta, items); err != nil {
			return true, nil, err
		}
		*marker = next
	}
	if marker.completed() {
		return true, marker, nil
	}
	if marker.StartHeight > stopHeight {
		return true, nil, nil
	}
	return true, nil, btypes.ErrUnavailableBlock
}

// maxObservationReceiptsPerFetch bounds how many unacknowledged observation
// receipts are re-delivered to Observer in a single FetchTxs. Receipts stay
// durable and prune-protected until acknowledged, so a wedged or slow Observer
// costs only extra rounds, never data: the remainder is redelivered on the next
// FetchTxs. Without this cap every FetchTxs re-sends the entire unacked backlog.
//
// ponytail: fixed ceiling of 64 receipts per fetch, chosen to stay far below
// ListObservationReceipts' 4096-receipt / 64MB hard accumulation guard while
// still draining a normal reorg or catch-up burst in one or two passes. If a
// deployment ever sustains a backlog that this cannot drain, the upgrade path
// is real back-pressure -- have FetchTxs decline to advance setCurrentHeight
// while unacked receipts remain above a watermark -- not a bigger constant.
const maxObservationReceiptsPerFetch = 64

func (s *Scanner) canonicalObservationReceipts() ([]scannerObservationReceiptClaim, error) {
	claims, err := s.reorgStore.ListObservationReceipts()
	if err != nil {
		return nil, err
	}
	// Receipt keys are zero-padded, so the listing is height-ascending: truncating
	// keeps the oldest receipts moving first and cannot starve a height.
	if len(claims) > maxObservationReceiptsPerFetch {
		s.logger.Info().
			Int("total", len(claims)).
			Int("delivering", maxObservationReceiptsPerFetch).
			Msg("bounding monero observation receipt redelivery; remainder follows next fetch")
		claims = claims[:maxObservationReceiptsPerFetch]
	}
	for _, claim := range claims {
		meta, metaErr := s.reorgStore.Get(claim.entry.Height)
		if metaErr != nil {
			return nil, metaErr
		}
		if meta == nil || !strings.EqualFold(meta.BlockHash, claim.entry.BlockHash) ||
			!strings.EqualFold(meta.PreviousHash, claim.entry.PreviousHash) {
			return nil, fmt.Errorf("xmr scanner receipt at height %d is not bound to the canonical journal", claim.entry.Height)
		}
	}
	return claims, nil
}

// prepareCanonicalObservationHandoff enforces THOR's permanent Reverted
// terminal before any observation reaches gossip. A later remine of the exact
// non-expiring XMR TxID cannot create a usable on-chain vote, so it is omitted
// even from explicit reorg replay. Durable receipts containing only such items
// are safe to acknowledge directly because the quarantine was fsynced before
// the original ErrataBlock handoff.
func (s *Scanner) prepareCanonicalObservationHandoff(
	items []*stypes.TxInItem,
	claims []scannerObservationReceiptClaim,
	completedMarker *scannerCatchupMarker,
) ([]*stypes.TxInItem, error) {
	filtered := make([]*stypes.TxInItem, 0, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.Tx) == "" {
			continue
		}
		reverted, err := s.reorgStore.IsRevertedTx(item.Tx)
		if err != nil {
			return nil, fmt.Errorf("check permanently reverted xmr observation %s: %w", item.Tx, err)
		}
		if reverted {
			s.logger.Error().Str("txid", item.Tx).Int64("height", item.BlockHeight).
				Msg("dropping remine of permanently reverted xmr transaction")
			continue
		}
		filtered = append(filtered, item)
	}

	if len(claims) > 0 {
		if len(filtered) == 0 {
			if err := s.reorgStore.AckObservationReceipts(claims, completedMarker); err != nil {
				return nil, fmt.Errorf("drain quarantined xmr scanner observations: %w", err)
			}
			return filtered, nil
		}
		s.attachObservationReceiptBarrier(filtered, claims, completedMarker)
	} else if completedMarker != nil {
		// No observation requires an Observer handoff, so the synced journal is
		// itself the terminal durability point for this catch-up.
		if err := s.reorgStore.ClearCompletedCatchupMarker(*completedMarker); err != nil {
			return nil, err
		}
	}
	return filtered, nil
}

// attachObservationReceiptBarrier composes with existing replay callbacks and
// clears durable scanner receipts only after every item in the returned TxIn
// reaches Observer's fsynced stored terminal. Observer's authoritative
// permanent-revert terminal is accepted only after Scanner fsyncs both local
// quarantine layers. An exact orphan-location terminal is also acknowledged,
// but does not quarantine the TxID; every other rejection retains all claims
// for retry.
func (s *Scanner) attachObservationReceiptBarrier(
	items []*stypes.TxInItem,
	claims []scannerObservationReceiptClaim,
	completedMarker *scannerCatchupMarker,
) {
	var barrierMu sync.Mutex
	remaining := len(items)
	rejected := false
	for _, item := range items {
		previousStored := item.OnObservedTxStored
		previousRejected := item.OnObservedTxRejected
		var terminal sync.Once
		item.OnObservedTxStored = func() {
			terminal.Do(func() {
				if previousStored != nil {
					previousStored()
				}
				barrierMu.Lock()
				remaining--
				shouldAck := remaining == 0 && !rejected
				barrierMu.Unlock()
				if shouldAck {
					if err := s.reorgStore.AckObservationReceipts(claims, completedMarker); err != nil {
						s.logger.Error().Err(err).Msg("fail to acknowledge durable xmr scanner observations")
					}
				}
			})
		}
		item.OnObservedTxRejected = func(err error) {
			terminal.Do(func() {
				permanentlyRejected := errors.Is(err, stypes.ErrObservedTxPermanentlyReverted)
				orphaned := errors.Is(err, stypes.ErrObservedTxOrphaned)
				notApplicable := errors.Is(err, stypes.ErrObservedTxNotApplicable)
				if permanentlyRejected {
					if quarantineErr := s.quarantineObserverRejectedTx(item); quarantineErr != nil {
						s.logger.Error().Err(quarantineErr).Str("txid", item.Tx).
							Msg("retain xmr scanner receipt after permanent-revert quarantine failure")
						permanentlyRejected = false
					}
				}
				terminalRejection := orphaned || permanentlyRejected || notApplicable
				if previousRejected != nil {
					previousRejected(err)
				}
				barrierMu.Lock()
				if !terminalRejection {
					rejected = true
				}
				remaining--
				shouldAck := remaining == 0 && !rejected
				barrierMu.Unlock()
				if shouldAck {
					if ackErr := s.reorgStore.AckObservationReceipts(claims, completedMarker); ackErr != nil {
						s.logger.Error().Err(ackErr).Msg("fail to acknowledge terminally rejected xmr scanner observations")
					}
				}
			})
		}
	}
}

func (s *Scanner) quarantineObserverRejectedTx(item *stypes.TxInItem) error {
	if item == nil || normalizeTxID(item.Tx) == "" {
		return errors.New("permanently rejected xmr observation identity is empty")
	}
	if s.claimQuarantineHandler == nil {
		return errors.New("monero signed-claim quarantine is unavailable")
	}
	if err := s.claimQuarantineHandler(item.Tx); err != nil {
		return fmt.Errorf("quarantine Observer-rejected xmr signed claim: %w", err)
	}
	if s.reorgStore == nil {
		return errors.New("monero reverted transaction quarantine is unavailable")
	}
	if err := s.reorgStore.QuarantineRevertedTxs(item.BlockHeight, []stypes.ErrataTx{{
		TxID: common.TxID(item.Tx), Chain: common.XMRChain,
	}}); err != nil {
		return err
	}
	return nil
}

func (s *Scanner) transfersToTxInItems(resp sidecar.Transfers, fetchHeight int64) ([]*stypes.TxInItem, error) {
	if len(resp.Incoming)+len(resp.Outgoing) == 0 {
		return nil, nil
	}

	outgoingVaultsByTxID := s.outgoingVaultsByTxID(resp.Outgoing)
	s.rememberObservedOutgoingVaults(outgoingVaultsByTxID, fetchHeight)
	txItems := make([]*stypes.TxInItem, 0, len(resp.Incoming)+len(resp.Outgoing))
	for _, transfer := range resp.Incoming {
		to := strings.TrimSpace(transfer.Address)
		if to == "" {
			continue
		}
		_, addressErr := common.NewAddress(to)
		if addressErr != nil {
			s.logger.Debug().Err(addressErr).Str("txid", transfer.TxID).Str("to", to).Msg("skip incoming transfer with invalid to address")
			continue
		}

		vaultPubKey, ok := s.resolveIncomingVault(transfer)
		if !ok {
			continue
		}
		if s.isSameVaultOutgoingTx(transfer.TxID, vaultPubKey, outgoingVaultsByTxID) {
			s.logger.Debug().
				Str("txid", transfer.TxID).
				Str("vault_pub_key", vaultPubKey.String()).
				Msg("skip incoming monero change output for known outgoing tx")
			continue
		}

		memoText := strings.TrimSpace(transfer.Note)
		// WARNING: This filter protects vault accounting and reorg safety.
		// OUT, REFUND and RAGNAROK are reserved memos, not deposit instructions.
		// This intentionally rejects external transfers using them. Missing outgoing
		// and local signing metadata after restart/churn can make our own change look
		// like an external inbound. Admitting it can double-count vault funds and
		// journal the spend without vault-origin provenance. A later reorg can then
		// bypass the vault-origin safety hold and emit errata while signed bytes may
		// still be rebroadcast. Relaxing this filter requires coordinated provenance,
		// accounting and reorg handling; neither a timeout nor a missing outgoing
		// record proves that the transfer is external.
		if parsed, err := memo.ParseMemo(common.LatestVersion, memoText); err == nil && parsed.IsOutbound() {
			s.logger.Info().Str("txid", transfer.TxID).Str("memo", memoText).
				Str("vault_pub_key", vaultPubKey.String()).Uint64("amount_piconero", transfer.Amount).
				Msg("skip incoming monero output carrying an outbound memo")
			continue
		}
		sender := unknownSenderAddress(transfer.TxID, to)
		if knownSender, matchingOutgoing, foundKnownSender := s.resolveIncomingKnownSender(transfer, to, vaultPubKey, memoText, resp.Outgoing); foundKnownSender {
			sender = knownSender
			// Incoming sidecar entries do not carry the transaction fee. Pair an
			// internal destination with the exact authenticated outgoing fee so
			// every observer constructs deterministic gas.
			transfer.Fee = matchingOutgoing.Fee
		} else if isInternalXMRMemo(memoText) {
			s.logger.Warn().
				Str("txid", transfer.TxID).
				Str("vault_pub_key", vaultPubKey.String()).
				Str("memo", memoText).
				Msg("skip unattributed internal monero inbound; authenticated migration replay is enrolled only by MarkSpent")
			continue
		}

		item, ok := s.transferToTxInItem(transfer, sender, to, vaultPubKey, memoText, fetchHeight)
		if !ok {
			continue
		}
		txItems = append(txItems, item)
	}
	for _, transfer := range resp.Outgoing {
		// ChainCommitted entries with observation_eligible=false are returned by
		// the sidecar solely as authenticated pairing proofs for an internal
		// destination. They must never become source observations themselves.
		if strings.TrimSpace(transfer.Origin) == sidecar.OutgoingOriginChainCommitted {
			continue
		}
		to := strings.TrimSpace(transfer.Address)
		if to == "" {
			continue
		}
		_, addressErr := common.NewAddress(to)
		if addressErr != nil {
			s.logger.Debug().Err(addressErr).Str("txid", transfer.TxID).Str("to", to).Msg("skip outgoing transfer with invalid to address")
			continue
		}

		sender, vaultPubKey := s.resolveOutgoingSender(transfer)
		if sender == "" || vaultPubKey.IsEmpty() {
			continue
		}

		memoText := strings.TrimSpace(transfer.Note)

		item, ok := s.transferToTxInItem(transfer, sender, to, vaultPubKey, memoText, fetchHeight)
		if !ok {
			continue
		}
		txItems = append(txItems, item)
	}

	return s.dedupeTxInItemsByTxID(txItems), nil
}

func (s *Scanner) outgoingVaultsByTxID(outgoing []sidecar.Transfer) map[string]map[string]struct{} {
	if len(outgoing) == 0 {
		return nil
	}

	result := make(map[string]map[string]struct{}, len(outgoing))
	for _, transfer := range outgoing {
		txID := normalizeTxID(transfer.TxID)
		if txID == "" {
			continue
		}

		_, vaultPubKey := s.resolveOutgoingSender(transfer)
		if vaultPubKey.IsEmpty() {
			continue
		}

		vaults, ok := result[txID]
		if !ok {
			vaults = make(map[string]struct{})
			result[txID] = vaults
		}
		vaults[vaultPubKey.String()] = struct{}{}
	}
	return result
}

func (s *Scanner) isSameVaultOutgoingTx(
	txID string,
	vaultPubKey common.PubKey,
	outgoingVaultsByTxID map[string]map[string]struct{},
) bool {
	if vaultPubKey.IsEmpty() {
		return false
	}

	normalizedTxID := normalizeTxID(txID)
	if normalizedTxID == "" {
		return false
	}
	if len(outgoingVaultsByTxID) > 0 {
		if vaults, ok := outgoingVaultsByTxID[normalizedTxID]; ok {
			if _, ok := vaults[vaultPubKey.String()]; ok {
				return true
			}
		}
	}
	return s.hasObservedOutgoingVault(normalizedTxID, vaultPubKey)
}

func (s *Scanner) rememberObservedOutgoingVaults(outgoingVaultsByTxID map[string]map[string]struct{}, height int64) {
	if len(outgoingVaultsByTxID) == 0 {
		return
	}

	s.observedOutgoingMu.Lock()
	defer s.observedOutgoingMu.Unlock()

	for txID, vaults := range outgoingVaultsByTxID {
		if txID == "" || len(vaults) == 0 {
			continue
		}

		observed := s.observedOutgoingByTxID[txID]
		if observed.vaults == nil {
			observed.vaults = make(map[string]struct{}, len(vaults))
		}
		if height > observed.height {
			observed.height = height
		}
		for vault := range vaults {
			if strings.TrimSpace(vault) == "" {
				continue
			}
			observed.vaults[vault] = struct{}{}
		}
		s.observedOutgoingByTxID[txID] = observed
	}
}

func (s *Scanner) hasObservedOutgoingVault(txID string, vaultPubKey common.PubKey) bool {
	if txID == "" || vaultPubKey.IsEmpty() {
		return false
	}

	s.observedOutgoingMu.RLock()
	defer s.observedOutgoingMu.RUnlock()

	observed, ok := s.observedOutgoingByTxID[txID]
	if !ok {
		return false
	}
	_, ok = observed.vaults[vaultPubKey.String()]
	return ok
}

func (s *Scanner) pruneObservedOutgoing(currentHeight int64) {
	if currentHeight <= 0 {
		return
	}

	retention := s.maxReorgRescan * 4
	if retention < 1024 {
		retention = 1024
	}
	cutoff := currentHeight - retention
	if cutoff <= 0 {
		return
	}

	s.observedOutgoingMu.Lock()
	defer s.observedOutgoingMu.Unlock()

	for txID, observed := range s.observedOutgoingByTxID {
		if observed.height >= cutoff {
			continue
		}
		delete(s.observedOutgoingByTxID, txID)
	}
}

func (s *Scanner) resolveOutgoingSender(transfer sidecar.Transfer) (string, common.PubKey) {
	txID := strings.TrimSpace(transfer.TxID)
	explicitSender := strings.TrimSpace(transfer.FromAddress)
	explicitVault := strings.TrimSpace(transfer.VaultPubKey)

	if explicitSender == "" {
		s.logger.Debug().Str("txid", txID).Msg("skip outgoing transfer: missing explicit from_address")
		return "", common.EmptyPubKey
	}
	sender, err := common.NewAddress(explicitSender)
	if err != nil {
		s.logger.Debug().Err(err).Str("txid", txID).Str("from", explicitSender).Msg("skip outgoing transfer: invalid explicit from_address")
		return "", common.EmptyPubKey
	}
	if explicitVault == "" {
		s.logger.Debug().Str("txid", txID).Str("from", sender.String()).Msg("skip outgoing transfer: missing explicit vault_pub_key")
		return "", common.EmptyPubKey
	}
	vaultPubKey, err := common.NewPubKey(explicitVault)
	if err != nil || vaultPubKey.IsEmpty() {
		s.logger.Debug().Err(err).Str("txid", txID).Str("vault_pub_key", explicitVault).Msg("skip outgoing transfer: invalid explicit vault_pub_key")
		return "", common.EmptyPubKey
	}
	deriveChain := s.chain
	if deriveChain.IsEmpty() {
		deriveChain = common.XMRChain
	}
	derived, err := vaultPubKey.GetAddress(deriveChain)
	if err != nil || derived.IsEmpty() {
		s.logger.Debug().Err(err).Str("txid", txID).Str("vault_pub_key", vaultPubKey.String()).Msg("skip outgoing transfer: cannot derive sender address from vault_pub_key")
		return "", common.EmptyPubKey
	}
	if !common.AddressEquals(common.XMRChain, sender, derived) {
		s.logger.Warn().
			Str("txid", txID).
			Str("from_address", sender.String()).
			Str("vault_pub_key", vaultPubKey.String()).
			Str("derived_address", derived.String()).
			Msg("skip outgoing transfer: from_address does not match derived vault address")
		return "", common.EmptyPubKey
	}

	return sender.String(), vaultPubKey
}

func (s *Scanner) GetHeight() (int64, error) {
	if s.sidecar == nil {
		return 0, fmt.Errorf("monero scanner requires sidecar for chain queries")
	}

	height, err := s.sidecar.ChainHeight()
	if err != nil {
		return 0, err
	}
	s.setDaemonTipHeight(height)
	return height, nil
}

func (s *Scanner) setCurrentHeight(height int64) {
	if s.currentBlockHeight != nil {
		s.currentBlockHeight.Store(height)
	}
}

func (s *Scanner) setDaemonTipHeight(height int64) {
	if s.daemonTipHeight != nil {
		s.daemonTipHeight.Store(height)
	}
}

const defaultXMRFeeTotalThorchain = 20000 // ~0.0002 XMR typical mainnet fee

func totalFeeAtomicToThorchain(feeAtomic uint64) uint64 {
	return feeAtomic / moneroAtomicToThorchain
}

func feeRateFromTotalAtomic(totalAtomic uint64) uint64 {
	if totalAtomic == 0 {
		return 0
	}
	return (totalAtomic + typicalVaultOutboundWeight - 1) / typicalVaultOutboundWeight
}

func (s *Scanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	totalAtomic := s.lastFee.Load()
	if totalAtomic == 0 {
		totalAtomic = defaultXMRFeeTotalThorchain * moneroAtomicToThorchain
	}
	return typicalVaultOutboundWeight, feeRateFromTotalAtomic(totalAtomic)
}

func averageNetworkFeeFromBlock(reward uint64, txCount int) uint64 {
	if txCount <= 0 || reward <= defaultCoinbaseValue {
		return 0
	}

	totalFeesAtomic := reward - defaultCoinbaseValue
	averageFeeAtomic := totalFeesAtomic / uint64(txCount)
	return averageFeeAtomic
}

func (s *Scanner) processReorg(
	block sidecar.ChainBlock,
	chainHeight int64,
) ([]*reorgBlockMeta, error) {
	if s.reorgStore == nil || block.Hash == "" || block.PreviousHash == "" {
		return nil, nil
	}
	// The retained journal is the reconciliation boundary even when churn,
	// registration or restart moves the requested block or signer cursor.
	// Reconcile it before replay can overwrite old metadata or return receipts.
	// This also detects reorgs in already checkpointed chunks of an unfinished
	// registration catch-up before any stale receipt reaches Observer.
	previousHeight, err := s.reorgStore.LatestHeight()
	if err != nil {
		return nil, err
	}
	if previousHeight < 0 {
		return nil, nil
	}
	prevBlockMeta, err := s.reorgStore.Get(previousHeight)
	if err != nil {
		return nil, err
	}
	expectedHash := block.PreviousHash
	switch previousHeight {
	case block.Height - 1:
		// Ordinary forward scanning already fetched the successor's header.
	case block.Height:
		expectedHash = block.Hash
	default:
		canonical, blockErr := s.sidecar.ChainBlock(previousHeight)
		if blockErr != nil {
			return nil, fmt.Errorf("fetch latest journal block %d during reorg check: %w", previousHeight, blockErr)
		}
		expectedHash = canonical.Hash
	}
	if prevBlockMeta == nil || strings.EqualFold(prevBlockMeta.BlockHash, expectedHash) {
		return nil, nil
	}

	s.logger.Warn().
		Int64("height", previousHeight+1).
		Int64("previous_height", previousHeight).
		Str("expected_previous_hash", prevBlockMeta.BlockHash).
		Str("actual_previous_hash", expectedHash).
		Msg("monero reorg detected")

	anchor, replacementBlocks, oldMetas, err := s.discoverReorgReplacement(previousHeight)
	if err != nil {
		return nil, err
	}
	if len(replacementBlocks) == 0 {
		return nil, nil
	}

	state, err := s.sidecar.ScannerState()
	if err != nil {
		return nil, err
	}
	if state.NextHeight == nil {
		return nil, errors.New("sidecar scanner has no durable next_height during reorg")
	}
	targetNext := anchor.Height + 1
	if targetNext <= 0 {
		return nil, fmt.Errorf("invalid monero reorg anchor height %d", anchor.Height)
	}
	expectedNext := int64(*state.NextHeight)
	switch {
	case expectedNext > targetNext:
		distance := expectedNext - targetNext
		limit := s.cursorReplayLimit()
		if limit == 0 || distance > limit {
			return nil, fmt.Errorf(
				"monero reorg rewind distance %d exceeds bounded replay limit %d", distance, limit,
			)
		}
		resp, rewindErr := s.sidecar.RewindScanner(sidecar.ScannerRewindReq{
			ExpectedResetEpoch: state.ResetEpoch,
			ExpectedNextHeight: uint64(expectedNext),
			TargetNextHeight:   uint64(targetNext),
			AnchorHeight:       uint64(anchor.Height),
			AnchorHash:         anchor.Hash,
		})
		if rewindErr != nil {
			return nil, rewindErr
		}
		if resp.NextHeight != uint64(targetNext) || resp.ResetEpoch <= state.ResetEpoch {
			return nil, errors.New("sidecar scanner rewind returned an invalid cursor transition")
		}
		expectedNext = targetNext
	case expectedNext == targetNext:
		// A lost rewind response leaves exactly this state. Bind it to the same
		// canonical anchor when the sidecar has a retained predecessor.
		if state.LastHeight != nil && int64(*state.LastHeight) != anchor.Height {
			return nil, errors.New("sidecar scanner target cursor is not bound to the reorg anchor")
		}
		if state.LastBlockHash != "" && !strings.EqualFold(state.LastBlockHash, anchor.Hash) {
			return nil, errors.New("sidecar scanner target cursor has a conflicting anchor hash")
		}
	default:
		// An older-vault registration can rewind below the fork anchor. Do not
		// fold that historical enrollment into the reorg's deferred journal batch:
		// a crash would leave pre-anchor receipts unbound and unreplayable. Drive
		// it through the per-height synced catch-up checkpoints first. The marker
		// remains completed until the outer Fetch extends it to the requested tip
		// and Observer acknowledges all accumulated observations.
		durableTarget := anchor.Height
		if marker, markerErr := s.reorgStore.GetCatchupMarker(); markerErr != nil {
			return nil, markerErr
		} else if marker != nil && marker.TargetHeight > durableTarget {
			durableTarget = marker.TargetHeight
		}
		handled, _, catchupErr := s.advanceSidecarCatchupThrough(
			durableTarget, anchor.Height, chainHeight,
		)
		if catchupErr != nil {
			return nil, catchupErr
		}
		if !handled {
			return nil, errors.New("sidecar pre-anchor rewind was not handled by durable catch-up")
		}
		state, err = s.sidecar.ScannerState()
		if err != nil {
			return nil, err
		}
		if state.NextHeight == nil || int64(*state.NextHeight) != targetNext {
			return nil, errors.New("durable pre-anchor catch-up did not reach the reorg anchor")
		}
		expectedNext = targetNext
	}

	startHeight := expectedNext
	endHeight := previousHeight
	if endHeight-startHeight+1 > s.cursorReplayLimit() {
		return nil, errors.New("monero reorg replay exceeds configured bound")
	}
	blockByHeight := make(map[int64]sidecar.ChainBlock, len(replacementBlocks))
	for _, replacement := range replacementBlocks {
		blockByHeight[replacement.Height] = replacement
	}
	// Snapshot the heights that already carry outstanding work BEFORE the replay
	// loop writes any observation receipt. The errata loop below uses this to
	// tell an ordinary pruned-metadata hole apart from a corrupt one; taken
	// after the replay it would also see receipts this very pass just wrote and
	// would misreport every normal reorg over a pruned height.
	outstandingBefore, err := s.reorgStore.OutstandingWorkHeights()
	if err != nil {
		return nil, err
	}
	pendingMetas := make([]*reorgBlockMeta, 0, endHeight-startHeight+1)
	for height := startHeight; height <= endHeight; height++ {
		reorgBlock, ok := blockByHeight[height]
		if !ok {
			reorgBlock, err = s.sidecar.ChainBlock(height)
			if err != nil {
				if errors.Is(err, sidecar.ErrUnavailableBlock) {
					return nil, btypes.ErrUnavailableBlock
				}
				return nil, fmt.Errorf("fetch monero block %d during reorg replay: %w", height, err)
			}
		}
		items, transferErr := s.fetchTransfersByHeight(
			height, chainHeight, reorgBlock.Hash, reorgBlock.PreviousHash,
		)
		if transferErr != nil {
			return nil, transferErr
		}
		meta := s.blockMetaForItems(reorgBlock, height, items)
		if err = s.reorgStore.PutObservationReceipt(meta, items); err != nil {
			return nil, err
		}
		pendingMetas = append(pendingMetas, meta)
	}

	// Only emit errata after the entire replacement range has replayed. The old
	// journal remains untouched until FetchTxs also syncs its requested block,
	// so any later failure rediscovers the fork and rewinds for a full retry.
	for _, replacement := range replacementBlocks {
		oldMeta := oldMetas[replacement.Height]
		if oldMeta == nil {
			// No metadata for this height. PruneBefore and Delete both refuse to
			// remove metadata while a receipt, catch-up anchor, or relocation
			// watch still depends on it, so a hole here is ordinary retention
			// only when nothing was outstanding at that height. If something was,
			// the orphaned observations at this height cannot be identified and
			// their errata would be silently dropped: fail closed instead.
			if _, outstanding := outstandingBefore[replacement.Height]; outstanding {
				return nil, fmt.Errorf(
					"monero reorg block metadata is missing at height %d while work is still "+
						"outstanding there; orphan errata cannot be derived, refusing to skip",
					replacement.Height,
				)
			}
			continue
		}
		if strings.EqualFold(oldMeta.BlockHash, replacement.Hash) {
			continue
		}
		if err = s.emitReorgErrata(oldMeta, pendingMetas); err != nil {
			return nil, err
		}
	}

	return pendingMetas, nil
}

func (s *Scanner) discoverReorgReplacement(
	previousHeight int64,
) (sidecar.ChainBlock, []sidecar.ChainBlock, map[int64]*reorgBlockMeta, error) {
	limit := s.cursorReplayLimit()
	if limit == 0 {
		return sidecar.ChainBlock{}, nil, nil, errors.New("monero reorg replay is disabled")
	}
	earliestAnchor := previousHeight - limit
	if earliestAnchor < 0 {
		earliestAnchor = 0
	}
	var anchor sidecar.ChainBlock
	foundAnchor := false
	for height := previousHeight; height >= earliestAnchor; height-- {
		meta, err := s.reorgStore.Get(height)
		if err != nil {
			return sidecar.ChainBlock{}, nil, nil, err
		}
		if meta == nil {
			continue
		}
		canonical, err := s.sidecar.ChainBlock(height)
		if err != nil {
			if recoveryErr := reorgHeightMismatchRecoveryError(
				err, previousHeight+1, height, "reorg check",
			); recoveryErr != nil {
				return sidecar.ChainBlock{}, nil, nil, recoveryErr
			}
			return sidecar.ChainBlock{}, nil, nil, fmt.Errorf(
				"fail to fetch monero block %d during reorg check: %w", height, err,
			)
		}
		if strings.EqualFold(meta.BlockHash, canonical.Hash) {
			anchor = canonical
			foundAnchor = true
			break
		}
		s.logger.Info().
			Int64("height", height).
			Str("recorded_hash", meta.BlockHash).
			Str("canonical_hash", canonical.Hash).
			Msg("reconfirming monero block")
	}
	if !foundAnchor {
		return sidecar.ChainBlock{}, nil, nil, fmt.Errorf(
			"monero reorg has no common ancestor within %d blocks", limit,
		)
	}

	replacements := make([]sidecar.ChainBlock, 0, previousHeight-anchor.Height)
	oldMetas := make(map[int64]*reorgBlockMeta, previousHeight-anchor.Height)
	for height := anchor.Height + 1; height <= previousHeight; height++ {
		canonical, err := s.sidecar.ChainBlock(height)
		if err != nil {
			if recoveryErr := reorgHeightMismatchRecoveryError(
				err, previousHeight+1, anchor.Height+1, "reorg replay",
			); recoveryErr != nil {
				return sidecar.ChainBlock{}, nil, nil, recoveryErr
			}
			return sidecar.ChainBlock{}, nil, nil, fmt.Errorf(
				"fail to fetch monero replacement block %d: %w", height, err,
			)
		}
		meta, err := s.reorgStore.Get(height)
		if err != nil {
			return sidecar.ChainBlock{}, nil, nil, err
		}
		replacements = append(replacements, canonical)
		oldMetas[height] = meta
	}
	return anchor, replacements, oldMetas, nil
}

// reorgHeightMismatchRecoveryError maps the shared typed height mismatch to
// terminal reorg recovery before any rewind or journal mutation.
func reorgHeightMismatchRecoveryError(
	err error,
	currentHeight,
	nextHeight int64,
	phase string,
) error {
	var mismatch *sidecar.ChainBlockHeightMismatchError
	if !errors.As(err, &mismatch) {
		return nil
	}
	return &btypes.ScannerRecoveryRequiredError{
		CurrentHeight: currentHeight,
		NextHeight:    nextHeight,
		Reason: fmt.Sprintf(
			"monero %s requested height %d but the chain query returned height %d",
			phase,
			mismatch.RequestedHeight,
			mismatch.ReturnedHeight,
		),
	}
}

func (s *Scanner) emitReorgErrata(
	blockMeta *reorgBlockMeta,
	replayedReplacementMetas []*reorgBlockMeta,
) error {
	errataTxs := make([]stypes.ErrataTx, 0)
	for _, tx := range blockMeta.CustomerTransactions {
		vaultOrigin := !blockMeta.ProvenanceComplete || reorgMetaHasVaultOrigin(blockMeta, tx)
		if !blockMeta.ProvenanceComplete {
			s.logger.Error().Str("txid", tx).Int64("height", blockMeta.Height).
				Msg("holding orphaned xmr transaction from legacy journal with incomplete origin provenance")
		}
		if !vaultOrigin {
			if s.localSignedTxHandler == nil {
				vaultOrigin = true
				s.logger.Error().Str("txid", tx).
					Msg("holding orphaned xmr transaction: local signed provenance checker is unavailable")
			} else if locallySigned, provenanceErr := s.localSignedTxHandler(tx); provenanceErr != nil {
				vaultOrigin = true
				s.logger.Error().Err(provenanceErr).Str("txid", tx).
					Msg("holding orphaned xmr transaction: local signed provenance check failed")
			} else {
				vaultOrigin = locallySigned
			}
		}
		// Suppress errata for a daemon-verified canonical location. The location
		// endpoint deliberately reports 404 for both pool-only and absent
		// transactions; those cases are distinguished below before making the
		// ObservedTx voter's permanent Reverted transition.
		if location, err := s.sidecar.CanonicalTxLocation(tx); err == nil {
			boundToReplay := false
			for _, replacement := range replayedReplacementMetas {
				if replacement == nil || replacement.Height != location.Height ||
					!strings.EqualFold(replacement.BlockHash, location.BlockHash) {
					continue
				}
				if !slices.ContainsFunc(replacement.CustomerTransactions, func(observedTxID string) bool {
					return normalizeTxID(observedTxID) == normalizeTxID(tx)
				}) {
					replacement.CustomerTransactions = append(replacement.CustomerTransactions, normalizeTxID(tx))
				}
				if vaultOrigin {
					addVaultOriginToMeta(replacement, tx)
				}
				boundToReplay = true
				break
			}
			if boundToReplay {
				continue
			}
			// Thornode permanently marks an ObservedTx voter reverted, so a future canonical
			// location cannot be errata'd and later re-observed under the same TxID.
			// Retain a durable watch until that height is processed and carried into
			// the journal, or until it disappears and errata becomes final.
			if err = s.watchRelocationWithRecovery(scannerRelocationWatch{
				TxID:           tx,
				OriginalHeight: blockMeta.Height,
				VaultOrigin:    vaultOrigin,
				LocationHeight: location.Height,
				BlockHash:      location.BlockHash,
				PreviousHash:   location.PreviousHash,
			}); err != nil {
				return err
			}
			continue
		} else if !sidecar.IsHTTPStatus(err, http.StatusNotFound) {
			return fmt.Errorf("unable to locate canonical monero tx %s during reorg: %w", tx, err)
		}
		// canonical-location intentionally returns 404 for both an absent tx and
		// one still in the pool. Errata is permanent in THOR's ObservedTx voter, while XMR
		// transactions never expire, so retain a durable watch whenever the
		// authenticated daemon still has the exact transaction.
		exists, existsErr := s.sidecar.TxExists(tx)
		if existsErr != nil {
			return fmt.Errorf("check orphaned monero tx %s pool presence: %w", tx, existsErr)
		}
		if exists {
			if err := s.watchRelocationWithRecovery(scannerRelocationWatch{
				TxID: tx, OriginalHeight: blockMeta.Height, InPool: true, VaultOrigin: vaultOrigin,
			}); err != nil {
				return err
			}
			continue
		}
		if vaultOrigin {
			if err := s.watchRelocationWithRecovery(scannerRelocationWatch{
				TxID: tx, OriginalHeight: blockMeta.Height, HoldAbsent: true, VaultOrigin: true,
			}); err != nil {
				return err
			}
			s.logger.Error().Str("txid", tx).Int64("orphaned_height", blockMeta.Height).
				Msg("SAFETY HOLD: absent vault-origin xmr outbound cannot be reverted while signed bytes may still be rebroadcast")
			continue
		}
		errataTxs = append(errataTxs, stypes.ErrataTx{
			TxID:  common.TxID(tx),
			Chain: s.chain,
		})
	}
	return s.deliverReorgErrata(blockMeta.Height, errataTxs)
}

func (s *Scanner) watchRelocationWithRecovery(watch scannerRelocationWatch) error {
	err := s.reorgStore.WatchRelocation(watch)
	if err == nil || !errors.Is(err, errScannerRelocationWatchQuota) {
		return err
	}
	s.logger.Error().Err(err).Str("txid", watch.TxID).
		Msg("xmr relocation watch quota reached; reconciling a fair page before retry")
	if reconcileErr := s.reconcileRelocationWatches(); reconcileErr != nil {
		return errors.Join(err, reconcileErr)
	}
	if err = s.reorgStore.WatchRelocation(watch); err != nil {
		if errors.Is(err, errScannerRelocationWatchQuota) {
			s.logger.Error().Err(err).Str("txid", watch.TxID).
				Msg("CRITICAL: xmr ordinary relocation watch quota remains full after reconciliation")
		}
		return err
	}
	return nil
}

func (s *Scanner) deliverReorgErrata(height int64, errataTxs []stypes.ErrataTx) error {
	if len(errataTxs) == 0 {
		return nil
	}
	if s.globalErrataQueue == nil {
		return errors.New("monero errata queue is unavailable")
	}
	// This handoff proves only that Observer durably recorded the orphaned
	// location and its retryable errata vote. Permanent XMR quarantine is applied
	// separately after Thornode explicitly reports ObservedTxVoter.Reverted.
	terminal := make(chan error, 1)
	var once sync.Once
	block := stypes.ErrataBlock{Height: height, Txs: errataTxs}
	block.OnStored = func() { once.Do(func() { terminal <- nil }) }
	block.OnRejected = func(err error) { once.Do(func() { terminal <- err }) }
	// The queue is unbuffered and this runs with scanMu held, so an observer
	// that has already stopped consuming would otherwise block the scanner
	// forever and wedge BlockScanner.Stop(). Bound both the send and the
	// acknowledgement: the reorg is rediscovered and redriven on the next
	// FetchTxs, so a timeout is safe to retry.
	timeout := s.errataDeliveryTimeout
	if timeout <= 0 {
		timeout = 2 * constants.ThorchainBlockTime
	}
	select {
	case s.globalErrataQueue <- block:
	case <-time.After(timeout):
		return fmt.Errorf("timed out delivering monero errata to observer at height %d", height)
	}
	select {
	case err := <-terminal:
		if err != nil {
			return fmt.Errorf("observer rejected durable monero errata: %w", err)
		}
	case <-time.After(timeout):
		return fmt.Errorf("timed out waiting for observer to durably process monero errata at height %d", height)
	}
	return nil
}

func (s *Scanner) reconcileRelocationWatches() error {
	const maxWatchesPerFetch = 4
	watches, err := s.reorgStore.ListRelocationWatchesAfter(s.relocationWatchCursor, maxWatchesPerFetch)
	if err != nil {
		return err
	}
	if len(watches) > 0 {
		s.relocationWatchCursor = normalizeTxID(watches[len(watches)-1].TxID)
	}
	for _, watch := range watches {
		location, locationErr := s.sidecar.CanonicalTxLocation(watch.TxID)
		if locationErr != nil {
			if !sidecar.IsHTTPStatus(locationErr, http.StatusNotFound) {
				return fmt.Errorf("revalidate xmr relocation watch %s: %w", watch.TxID, locationErr)
			}
			exists, existsErr := s.sidecar.TxExists(watch.TxID)
			if existsErr != nil {
				return fmt.Errorf("revalidate xmr pool watch %s: %w", watch.TxID, existsErr)
			}
			if exists {
				watch.InPool = true
				watch.HoldAbsent = false
				watch.LocationHeight = 0
				watch.BlockHash = ""
				watch.PreviousHash = ""
				if err = s.reorgStore.WatchRelocation(watch); err != nil {
					return err
				}
				continue
			}
			if !watch.VaultOrigin {
				for _, height := range []int64{watch.OriginalHeight, watch.LocationHeight} {
					if height <= 0 || watch.VaultOrigin {
						continue
					}
					meta, metaErr := s.reorgStore.Get(height)
					if metaErr != nil {
						return fmt.Errorf("reclassify orphaned xmr watch %s at %d: %w", watch.TxID, height, metaErr)
					}
					watch.VaultOrigin = meta != nil &&
						(!meta.ProvenanceComplete || reorgMetaHasVaultOrigin(meta, watch.TxID))
				}
				if !watch.VaultOrigin {
					if s.localSignedTxHandler == nil {
						watch.VaultOrigin = true
						s.logger.Error().Str("txid", watch.TxID).
							Msg("holding orphaned xmr watch: local signed provenance checker is unavailable")
					} else if locallySigned, provenanceErr := s.localSignedTxHandler(watch.TxID); provenanceErr != nil {
						watch.VaultOrigin = true
						s.logger.Error().Err(provenanceErr).Str("txid", watch.TxID).
							Msg("holding orphaned xmr watch: local signed provenance check failed")
					} else {
						watch.VaultOrigin = locallySigned
					}
				}
			}
			if watch.VaultOrigin {
				watch.InPool = false
				watch.HoldAbsent = true
				watch.LocationHeight = 0
				watch.BlockHash = ""
				watch.PreviousHash = ""
				if err = s.reorgStore.WatchRelocation(watch); err != nil {
					return err
				}
				s.logger.Error().Str("txid", watch.TxID).Int64("orphaned_height", watch.OriginalHeight).
					Msg("SAFETY HOLD persists: absent vault-origin xmr outbound may still be rebroadcast")
				continue
			}
			if err = s.deliverReorgErrata(watch.OriginalHeight, []stypes.ErrataTx{{
				TxID: common.TxID(watch.TxID), Chain: s.chain,
			}}); err != nil {
				return err
			}
			if err = s.reorgStore.DeleteRelocationWatch(watch.TxID); err != nil {
				return err
			}
			continue
		}
		watch.InPool = false
		watch.HoldAbsent = false
		watch.LocationHeight = location.Height
		watch.BlockHash = location.BlockHash
		watch.PreviousHash = location.PreviousHash
		meta, metaErr := s.reorgStore.Get(location.Height)
		if metaErr != nil {
			return metaErr
		}
		if meta != nil && strings.EqualFold(meta.BlockHash, location.BlockHash) &&
			strings.EqualFold(meta.PreviousHash, location.PreviousHash) {
			if err = s.reorgStore.BindRelocationWatch(watch, meta); err != nil {
				return err
			}
			continue
		}
		if err = s.reorgStore.WatchRelocation(watch); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) blockMetaForItems(
	block sidecar.ChainBlock,
	height int64,
	items []*stypes.TxInItem,
) *reorgBlockMeta {
	txIDs := make([]string, 0, len(items))
	vaultOriginTxIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	vaultSeen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil || item.Tx == "" {
			continue
		}
		txID := normalizeTxID(item.Tx)
		if _, ok := seen[txID]; !ok {
			seen[txID] = struct{}{}
			txIDs = append(txIDs, txID)
		}
		if s.isAuthenticatedVaultOriginItem(item) {
			if _, ok := vaultSeen[txID]; !ok {
				vaultSeen[txID] = struct{}{}
				vaultOriginTxIDs = append(vaultOriginTxIDs, txID)
			}
		}
	}

	return &reorgBlockMeta{
		Height:                  height,
		BlockHash:               block.Hash,
		PreviousHash:            block.PreviousHash,
		CustomerTransactions:    txIDs,
		VaultOriginTransactions: vaultOriginTxIDs,
		ProvenanceComplete:      true,
	}
}

func (s *Scanner) isAuthenticatedVaultOriginItem(item *stypes.TxInItem) bool {
	if item == nil {
		return false
	}
	if isAuthenticatedXMRVaultOrigin(item) {
		return true
	}
	sender := common.ResolveObservedSender(common.XMRChain, common.Address(item.Sender))
	if !sender.Known || !common.XMRChain.IsValidAddress(sender.Address) {
		return false
	}
	_, ok := s.vaultPubKeyForAddress(sender.Address.String())
	return ok
}

func reorgMetaHasVaultOrigin(meta *reorgBlockMeta, txID string) bool {
	if meta == nil {
		return false
	}
	want := normalizeTxID(txID)
	return slices.ContainsFunc(meta.VaultOriginTransactions, func(candidate string) bool {
		return normalizeTxID(candidate) == want
	})
}

func addVaultOriginToMeta(meta *reorgBlockMeta, txID string) bool {
	if meta == nil || reorgMetaHasVaultOrigin(meta, txID) {
		return false
	}
	meta.VaultOriginTransactions = append(meta.VaultOriginTransactions, normalizeTxID(txID))
	return true
}

func (s *Scanner) refreshAsgardLookup(nextScanHeight int64) error {
	s.asgardMu.RLock()
	if time.Since(s.asgardUpdated) < constants.ThorchainBlockTime && len(s.asgardLookup) > 0 {
		s.asgardMu.RUnlock()
		return nil
	}
	s.asgardMu.RUnlock()

	vaults, err := s.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards: %w", err)
	}

	lookup := make(map[string]common.PubKey)
	seenVaults := make(map[string]struct{})
	for _, vault := range vaults {
		if addErr := s.addFrostVaultToLookup(
			lookup,
			seenVaults,
			vault.PubKeyFrost,
			xmrVaultRegistrationBirthday(vault.VaultCreationHeightXmr, nextScanHeight),
		); addErr != nil {
			return fmt.Errorf("fail to refresh active xmr vault %s: %w", vault.PubKey.String(), addErr)
		}
	}

	pubKeys, err := s.bridge.GetPubKeys()
	if err != nil {
		return fmt.Errorf("fail to get vault pubkeys: %w", err)
	}
	for _, pair := range pubKeys {
		if pair.Algo != common.SigningAlgoSecp256k1 || pair.PubKey.IsEmpty() {
			continue
		}
		vault, vaultErr := s.bridge.GetVault(pair.PubKey.String())
		if vaultErr != nil {
			s.logger.Debug().Err(vaultErr).Str("vault_pubkey", pair.PubKey.String()).Msg("skip xmr pubkey during asgard lookup refresh")
			continue
		}
		if addErr := s.addFrostVaultToLookup(
			lookup,
			seenVaults,
			vault.PubKeyFrost,
			xmrVaultRegistrationBirthday(vault.VaultCreationHeightXmr, nextScanHeight),
		); addErr != nil {
			return fmt.Errorf("fail to refresh xmr vault %s from pubkey lookup: %w", vault.PubKey.String(), addErr)
		}
	}

	s.asgardMu.Lock()
	s.asgardLookup = lookup
	s.asgardUpdated = time.Now()
	s.asgardMu.Unlock()

	return nil
}

func (s *Scanner) addFrostVaultToLookup(
	lookup map[string]common.PubKey,
	seenVaults map[string]struct{},
	observedVault common.PubKey,
	registrationBirthday int64,
) error {
	if observedVault.IsEmpty() {
		return nil
	}
	key := observedVault.String()
	if _, ok := seenVaults[key]; ok {
		return nil
	}
	seenVaults[key] = struct{}{}

	derived, addrErr := ensureVaultRegistered(
		s.logger,
		s.sidecar,
		s.chain,
		observedVault,
		s.vaultBootstrap,
		"bifrost_scanner",
		registrationBirthday,
	)
	if addrErr != nil {
		return fmt.Errorf("fail to register vault %s: %w", observedVault.String(), addrErr)
	}
	if derived.IsEmpty() {
		s.logger.Debug().Str("vault_pubkey", observedVault.String()).Msg("skip asgard vault with empty derived address")
		return nil
	}
	lookup[derived.String()] = observedVault
	return nil
}

func (s *Scanner) vaultPubKeyForAddress(addr string) (common.PubKey, bool) {
	s.asgardMu.RLock()
	defer s.asgardMu.RUnlock()
	pk, ok := s.asgardLookup[addr]
	return pk, ok
}

func (s *Scanner) resolveIncomingVault(transfer sidecar.Transfer) (common.PubKey, bool) {
	to := strings.TrimSpace(transfer.Address)
	if to == "" {
		return common.EmptyPubKey, false
	}

	mappedVault, mappedOK := s.vaultPubKeyForAddress(to)
	explicitVault := strings.TrimSpace(transfer.VaultPubKey)
	if explicitVault == "" {
		return mappedVault, mappedOK
	}

	vaultPubKey, err := common.NewPubKey(explicitVault)
	if err != nil || vaultPubKey.IsEmpty() {
		s.logger.Debug().Err(err).Str("txid", transfer.TxID).Str("vault_pub_key", explicitVault).Msg("skip incoming transfer: invalid explicit vault_pub_key")
		return common.EmptyPubKey, false
	}

	deriveChain := s.chain
	if deriveChain.IsEmpty() {
		deriveChain = common.XMRChain
	}
	derived, err := vaultPubKey.GetAddress(deriveChain)
	if err != nil || derived.IsEmpty() {
		s.logger.Debug().Err(err).Str("txid", transfer.TxID).Str("vault_pub_key", vaultPubKey.String()).Msg("skip incoming transfer: cannot derive explicit vault address")
		return common.EmptyPubKey, false
	}
	if !common.AddressEquals(common.XMRChain, derived, common.Address(to)) {
		s.logger.Debug().
			Str("txid", transfer.TxID).
			Str("vault_pub_key", vaultPubKey.String()).
			Str("derived_address", derived.String()).
			Str("to", to).
			Msg("skip incoming transfer: explicit vault_pub_key address does not match transfer address")
		return common.EmptyPubKey, false
	}

	if !mappedOK {
		s.logger.Debug().
			Str("txid", transfer.TxID).
			Str("vault_pub_key", vaultPubKey.String()).
			Str("to", to).
			Msg("skip incoming transfer: explicit vault_pub_key is not a current asgard vault")
		return common.EmptyPubKey, false
	}
	if !mappedVault.Equals(vaultPubKey) {
		s.logger.Debug().
			Str("txid", transfer.TxID).
			Str("explicit_vault_pub_key", vaultPubKey.String()).
			Str("mapped_vault_pub_key", mappedVault.String()).
			Str("to", to).
			Msg("skip incoming transfer: explicit vault_pub_key does not match current asgard address attribution")
		return common.EmptyPubKey, false
	}

	return vaultPubKey, true
}

func (s *Scanner) transferToTxInItem(
	transfer sidecar.Transfer,
	sender string,
	to string,
	vaultPubKey common.PubKey,
	memo string,
	fallbackHeight int64,
) (*stypes.TxInItem, bool) {
	if transfer.TxID == "" || sender == "" || to == "" || vaultPubKey.IsEmpty() {
		return nil, false
	}

	amount := cosmos.NewUint(transfer.Amount / moneroAtomicToThorchain)
	if amount.IsZero() {
		return nil, false
	}

	fee := totalFeeAtomicToThorchain(transfer.Fee)
	if transfer.Fee > 0 && fee == 0 {
		// Preserve a non-zero exact fee at THORChain's coarser precision without
		// falling back to node-local fee estimates.
		fee = 1
	}
	if transfer.Fee == 0 {
		fee = totalFeeAtomicToThorchain(s.lastFee.Load())
	}
	if fee == 0 {
		fee = defaultXMRFeeTotalThorchain
	}

	height := int64(transfer.Height)
	if height <= 0 {
		height = fallbackHeight
	}

	item := stypes.NewTxInItem(
		height,
		transfer.TxID,
		memo,
		sender,
		to,
		common.NewCoins(common.NewCoin(common.XMRAsset, amount)),
		common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(fee))},
		vaultPubKey,
		"",
		"",
		nil,
	)
	txKey := strings.ToLower(strings.TrimSpace(transfer.TxKey))
	if txKey != "" {
		if err := common.ValidateXMRTxKey(common.XMRChain, txKey); err != nil {
			s.logger.Warn().Err(err).Str("txid", transfer.TxID).Msg("skip transfer: invalid xmr tx key")
			return nil, false
		}
	}
	item.SpentOutputRefs = s.echoedSpentOutputRefs(transfer, sender, vaultPubKey)
	item.TxKey = txKey
	return item, true
}

// echoedSpentOutputRefs mirrors the sidecar-verified spent refs of an outgoing
// transfer into the observation, in sidecar order with SpendTxHash set to the
// observed tx, exactly as the signing path attests them. A non-signing
// observer can only see the outbound after a verified mark-spent import, so
// echoing keeps every validator's observation identical and lets signer and
// peer attestations pool into a single quorum variant. Refs that would not
// survive ObservedTx validation are dropped as a set rather than poisoning
// the whole observation.
func (s *Scanner) echoedSpentOutputRefs(transfer sidecar.Transfer, sender string, vaultPubKey common.PubKey) []*common.OutputRef {
	if len(transfer.SpentOutputRefs) == 0 {
		return nil
	}
	// Refs are only valid on outbound observations (spends from the vault);
	// thornode rejects inbound observed txs that carry them. Incoming
	// transfers never have refs, but do not rely on the sidecar for that.
	deriveChain := s.chain
	if deriveChain.IsEmpty() {
		deriveChain = common.XMRChain
	}
	vaultAddress, err := vaultPubKey.GetAddress(deriveChain)
	if err != nil || !common.AddressEquals(common.XMRChain, common.Address(sender), vaultAddress) {
		return nil
	}
	refs := make([]common.OutputRef, len(transfer.SpentOutputRefs))
	for i, ref := range transfer.SpentOutputRefs {
		refs[i] = common.OutputRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: transfer.TxID,
		}
	}
	if err := common.ValidateSpentOutputRefs(common.XMRChain, common.TxID(transfer.TxID), refs); err != nil {
		s.logger.Warn().
			Err(err).
			Str("txid", transfer.TxID).
			Int("refs", len(refs)).
			Msg("skip echoing invalid xmr spent output refs from sidecar")
		return nil
	}
	out := make([]*common.OutputRef, len(refs))
	for i := range refs {
		out[i] = &refs[i]
	}
	return out
}

func (s *Scanner) dedupeTxInItemsByTxID(items []*stypes.TxInItem) []*stypes.TxInItem {
	return s.mergeTxInItemsByTxID(items, true)
}

// aggregateOutputs allows identical raw outputs within a scan response to add
// their amounts. Complete observations from separate scans or receipts instead
// keep one copy per payment while retaining every durable acknowledgement.
func (s *Scanner) mergeTxInItemsByTxID(items []*stypes.TxInItem, aggregateOutputs bool) []*stypes.TxInItem {
	if len(items) < 2 {
		return items
	}

	merged := make(map[string]*stypes.TxInItem, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		observationKey := txInItemObservationKey(item)
		if observationKey == "" {
			continue
		}
		existing, ok := merged[observationKey]
		if !ok {
			merged[observationKey] = item
			order = append(order, observationKey)
			continue
		}
		if existing.BlockHeight != item.BlockHeight {
			// The same transaction can be re-mined at a later canonical height.
			// Never aggregate observations across heights, even if fee/memo metadata
			// changed: doing so can double-credit the same canonical payment. Keep
			// the newest location and preserve both durable handoff callbacks.
			if !txInItemsEqualIgnoringBlockHeight(existing, item) {
				s.logger.Warn().
					Str("txid", item.Tx).
					Int64("old_height", existing.BlockHeight).
					Int64("new_height", item.BlockHeight).
					Msg("xmr relocated observation metadata changed; selecting highest canonical height")
			}
			if item.BlockHeight > existing.BlockHeight {
				composeTxInItemCallbacks(item, existing)
				merged[observationKey] = item
			} else {
				composeTxInItemCallbacks(existing, item)
			}
			continue
		}
		// Complete observations from separate scans or receipts describe one
		// payment even when estimated gas differs. Keep the first copy (the fresh
		// scan precedes receipts) and retain every acknowledgement. Within one
		// response, only exact copies carrying callbacks bypass output aggregation.
		if !aggregateOutputs || ((item.OnObservedTxStored != nil || item.OnObservedTxRejected != nil ||
			existing.OnObservedTxStored != nil || existing.OnObservedTxRejected != nil) && existing.Equals(item)) {
			composeTxInItemCallbacks(existing, item)
			continue
		}

		if existing.BlockHeight != item.BlockHeight || existing.Memo != item.Memo {
			s.logger.Debug().
				Str("txid", item.Tx).
				Str("existing_sender", existing.Sender).
				Str("existing_to", existing.To).
				Str("duplicate_sender", item.Sender).
				Str("duplicate_to", item.To).
				Msg("merging duplicate monero transfer with same txid")
		}

		existing.Coins = existing.Coins.Add(item.Coins...)
		switch {
		case len(existing.Gas) == 0:
			existing.Gas = item.Gas
		case len(item.Gas) > 0 && item.Gas[0].Amount.GT(existing.Gas[0].Amount):
			existing.Gas = item.Gas
		}
		if existing.Memo == "" && item.Memo != "" {
			existing.Memo = item.Memo
		}
	}

	deduped := make([]*stypes.TxInItem, 0, len(order))
	for _, observationKey := range order {
		deduped = append(deduped, merged[observationKey])
	}
	return deduped
}

func txInItemsEqualIgnoringBlockHeight(a, b *stypes.TxInItem) bool {
	if a == nil || b == nil {
		return false
	}
	aCopy := a.Copy()
	bCopy := b.Copy()
	aCopy.BlockHeight = 0
	bCopy.BlockHeight = 0
	return aCopy.Equals(bCopy)
}

func orderTxInItemsHighestFirst(items []*stypes.TxInItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].BlockHeight > items[j].BlockHeight
	})
}

func composeTxInItemCallbacks(destination, source *stypes.TxInItem) {
	if destination == nil || source == nil ||
		(source.OnObservedTxStored == nil && source.OnObservedTxRejected == nil) {
		return
	}
	destinationStored := destination.OnObservedTxStored
	destinationRejected := destination.OnObservedTxRejected
	sourceStored := source.OnObservedTxStored
	sourceRejected := source.OnObservedTxRejected
	var terminal sync.Once
	destination.OnObservedTxStored = func() {
		terminal.Do(func() {
			if destinationStored != nil {
				destinationStored()
			}
			if sourceStored != nil {
				sourceStored()
			}
		})
	}
	destination.OnObservedTxRejected = func(err error) {
		terminal.Do(func() {
			if destinationRejected != nil {
				destinationRejected(err)
			}
			if sourceRejected != nil {
				sourceRejected(err)
			}
		})
	}
}

func txInItemObservationKey(item *stypes.TxInItem) string {
	if item == nil {
		return ""
	}

	txID := normalizeTxID(item.Tx)
	if txID == "" {
		return ""
	}

	return strings.Join([]string{
		txID,
		strings.TrimSpace(item.Sender),
		strings.TrimSpace(item.To),
		item.ObservedVaultPubKey.String(),
	}, "|")
}

func unknownSenderAddress(txID, to string) string {
	return common.XMRUnknownSenderAddress(txID, to)
}

func (s *Scanner) resolveIncomingKnownSender(
	transfer sidecar.Transfer,
	to string,
	destVaultPubKey common.PubKey,
	memoText string,
	outgoing []sidecar.Transfer,
) (string, sidecar.Transfer, bool) {
	if !isInternalXMRMemo(memoText) {
		return "", sidecar.Transfer{}, false
	}

	normalizedTxID := normalizeTxID(transfer.TxID)
	if normalizedTxID == "" {
		return "", sidecar.Transfer{}, false
	}

	var matchedSender string
	var matched sidecar.Transfer
	for _, candidate := range outgoing {
		if normalizeTxID(candidate.TxID) != normalizedTxID {
			continue
		}
		if !s.isAuthenticatedInternalPairingCandidate(transfer, candidate, to, memoText) {
			continue
		}
		if !hasUsableXMROutgoingTxKey(candidate) {
			s.logger.Debug().
				Str("txid", candidate.TxID).
				Str("vault_pub_key", strings.TrimSpace(candidate.VaultPubKey)).
				Msg("skip incoming internal monero attribution: matching outgoing transfer is missing xmr tx key")
			continue
		}
		sender, sourceVaultPubKey := s.resolveOutgoingSender(candidate)
		if sender == "" || sourceVaultPubKey.IsEmpty() {
			continue
		}
		if sourceVaultPubKey.Equals(destVaultPubKey) {
			continue
		}
		if !common.AddressEquals(
			common.XMRChain,
			common.Address(strings.TrimSpace(candidate.Address)),
			common.Address(to),
		) {
			continue
		}
		if matchedSender != "" {
			s.logger.Warn().Str("txid", transfer.TxID).
				Msg("skip ambiguous internal monero attribution with multiple matching outgoings")
			return "", sidecar.Transfer{}, false
		}
		matchedSender = sender
		matched = candidate
	}
	if matchedSender == "" {
		return "", sidecar.Transfer{}, false
	}
	return matchedSender, matched, true
}

// isAuthenticatedInternalPairingCandidate permits a non-observable
// ChainCommitted outgoing only as evidence for the matching internal incoming
// in the same canonical scanner response. All identity-bearing fields must
// match exactly; this proof is never converted into a source TxInItem.
func (s *Scanner) isAuthenticatedInternalPairingCandidate(
	incoming sidecar.Transfer,
	candidate sidecar.Transfer,
	to string,
	memoText string,
) bool {
	switch strings.TrimSpace(candidate.Origin) {
	case sidecar.OutgoingOriginLocalCombined, sidecar.OutgoingOriginVaultCertified:
		if !candidate.ObservationEligible {
			return false
		}
	case sidecar.OutgoingOriginChainCommitted:
		if candidate.ObservationEligible || !hasCompleteChainCommittedPairingProof(candidate) {
			return false
		}
	default:
		return false
	}

	return normalizeTxID(candidate.TxID) == normalizeTxID(incoming.TxID) &&
		common.AddressEquals(
			common.XMRChain,
			common.Address(strings.TrimSpace(candidate.Address)),
			common.Address(strings.TrimSpace(to)),
		) &&
		candidate.Amount == incoming.Amount &&
		candidate.Height == incoming.Height &&
		strings.TrimSpace(candidate.Note) == strings.TrimSpace(memoText)
}

func hasCompleteChainCommittedPairingProof(transfer sidecar.Transfer) bool {
	if strings.TrimSpace(transfer.Origin) != sidecar.OutgoingOriginChainCommitted ||
		transfer.ObservationEligible ||
		!hasUsableXMROutgoingTxKey(transfer) ||
		strings.TrimSpace(transfer.FromAddress) == "" ||
		strings.TrimSpace(transfer.VaultPubKey) == "" ||
		strings.TrimSpace(transfer.Address) == "" ||
		transfer.Amount == 0 || transfer.Fee == 0 || transfer.Height == 0 ||
		strings.TrimSpace(transfer.Note) == "" ||
		len(transfer.SpentOutputRefs) == 0 ||
		len(transfer.SpentOutputRefs) > common.MaxXMRSpentOutputRefs {
		return false
	}
	if err := validateFixedHex(transfer.TxID, 32, "chain-committed tx hash"); err != nil {
		return false
	}

	seenRefs := make(map[string]struct{}, len(transfer.SpentOutputRefs))
	seenKeyImages := make(map[string]struct{}, len(transfer.SpentOutputRefs))
	for _, ref := range transfer.SpentOutputRefs {
		if validateFixedHex(ref.TxHash, 32, "chain-committed spent-ref tx hash") != nil ||
			validateFixedHex(ref.KeyImage, 32, "chain-committed spent-ref key image") != nil {
			return false
		}
		refKey := fmt.Sprintf("%s:%d", normalizeTxID(ref.TxHash), ref.OutputIndex)
		keyImage := normalizeTxID(ref.KeyImage)
		if _, duplicate := seenRefs[refKey]; duplicate {
			return false
		}
		if _, duplicate := seenKeyImages[keyImage]; duplicate {
			return false
		}
		seenRefs[refKey] = struct{}{}
		seenKeyImages[keyImage] = struct{}{}
		if spendTxHash := normalizeTxID(ref.SpendTxHash); spendTxHash != "" && spendTxHash != normalizeTxID(transfer.TxID) {
			return false
		}
	}
	return true
}

func isInternalXMRMemo(memoText string) bool {
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, strings.TrimSpace(memoText))
	return err == nil && parsedMemo.IsInternal()
}

func isXMRTxMigrateMemo(memoText string) bool {
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, strings.TrimSpace(memoText))
	return err == nil && parsedMemo.IsType(memo.TxMigrate)
}

func hasUsableXMROutgoingTxKey(transfer sidecar.Transfer) bool {
	txKey := strings.TrimSpace(transfer.TxKey)
	return txKey != "" && common.ValidateXMRTxKey(common.XMRChain, txKey) == nil
}

func normalizeTxID(txID string) string {
	return strings.ToLower(strings.TrimSpace(txID))
}

func IsUnavailableBlockErr(err error) bool {
	return errors.Is(err, sidecar.ErrUnavailableBlock)
}

// scannerHashMismatchCode is the sidecar's structured error code for a scanner
// sync hash mismatch.
const scannerHashMismatchCode = "scanner_hash_mismatch"

func isFrostScannerHashMismatchErr(err error) bool {
	if err == nil {
		return false
	}
	// Match the structured code. The sidecar deliberately no longer echoes
	// untrusted response bodies into the error text, so the previous prose
	// substring match silently stopped firing and turned a retryable mismatch
	// into a hard scan failure.
	var httpErr *sidecar.HTTPError
	if errors.As(err, &httpErr) &&
		strings.EqualFold(strings.TrimSpace(httpErr.Code), scannerHashMismatchCode) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "scanner sync hash mismatch")
}
