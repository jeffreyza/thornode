package observer

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"gitlab.com/thorchain/thornode/v3/bifrost/db"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ObserverStorage save the ondeck tx in item to key value store, in case bifrost restart
type ObserverStorage struct {
	db *leveldb.DB

	// errataMu serializes read-modify-write access to the durable errata outbox.
	errataMu               sync.Mutex
	certificateMu          sync.Mutex
	beforeCertificateWrite func() error

	// beforeSyncTxWrite is a deterministic failure hook used only by durable
	// duplicate-handoff tests. Production leaves it nil.
	beforeSyncTxWrite func() error

	// beforeErrataWrite is a deterministic failure hook for committed-event
	// durability tests. Production leaves it nil.
	beforeErrataWrite func() error
}

const (
	LegacyOnDeckKey           = "ondeck-tx"
	LegacyMigrationHandledKey = "ondeck-migration-handled"
	OnDeckTxKeyPrefix         = "txs:"
	PendingErrataKeyPrefix    = "errata:v1:"
	ErrataTombstoneKeyPrefix  = "errata-tombstone-v1:"
)

// admitXMRObservation reads durable admission without acquiring observer,
// gossip, state or batch locks. Callers serialize their own local mutation
// against cleanup, which persists the tombstone before purging volatile state.
func (s *ObserverStorage) admitXMRObservation(tx common.ObservedTx) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("errata storage is not ready")
	}
	tombstone, err := s.getErrataTombstone(tx.Tx.Chain, tx.Tx.ID.String())
	if err != nil {
		return false, err
	}
	// Gossip admission and validated batch enqueue must use the original mined
	// location, not the observation height rewritten during finalization.
	return tombstone == nil || !tombstone.blocksObservedTx(tx), nil
}

type persistedErrata struct {
	Height                    int64          `json:"height"`
	DeliveryID                string         `json:"delivery_id,omitempty"`
	Tx                        types.ErrataTx `json:"tx"`
	DeliverySeen              bool           `json:"delivery_seen,omitempty"`
	CleanupComplete           bool           `json:"cleanup_complete,omitempty"`
	Committed                 bool           `json:"committed,omitempty"`
	CommittedAtUnixNano       int64          `json:"committed_at_unix_nano,omitempty"`
	AttestationAttempts       uint32         `json:"attestation_attempts,omitempty"`
	FirstAttemptAtUnixNano    int64          `json:"first_attempt_at_unix_nano,omitempty"`
	LastAttemptAtUnixNano     int64          `json:"last_attempt_at_unix_nano,omitempty"`
	AttestationRetryExhausted bool           `json:"attestation_retry_exhausted,omitempty"`
	RetryExhaustedAtUnixNano  int64          `json:"retry_exhausted_at_unix_nano,omitempty"`
}

// errataCleanupMutation is one exact on-deck storage transition performed in
// the same batch that records local errata cleanup. FinalizeHeight is the
// original txInKey height, not the orphaned block height.
type errataCleanupMutation struct {
	TxIn           *types.TxIn
	FinalizeHeight int64
	Delete         bool
}

type errataTombstone struct {
	Chain             common.Chain           `json:"chain"`
	TxID              common.TxID            `json:"tx_id"`
	OrphanHeight      int64                  `json:"orphan_height"`
	OrphanHeights     []int64                `json:"orphan_heights,omitempty"`
	Generation        uint64                 `json:"generation"`
	Committed         bool                   `json:"committed"`
	Outcome           common.ErrataTxOutcome `json:"outcome,omitempty"`
	VoterReverted     *bool                  `json:"voter_reverted,omitempty"`
	VerifyAfterHeight int64                  `json:"verify_after_height,omitempty"`
}

// isPermanentlyReverted records completed local quarantine. Older receipts have
// no voter_reverted field; nil still requires reconciliation.
func (t errataTombstone) isPermanentlyReverted() bool {
	return t.Chain.Equals(common.XMRChain) && t.VoterReverted != nil && *t.VoterReverted
}

// hasCommittedRevertedOutcome blocks admission while local quarantine retries.
func (t errataTombstone) hasCommittedRevertedOutcome() bool {
	return t.Chain.Equals(common.XMRChain) && t.Committed &&
		t.Outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED
}

// blocksMinedHeight keeps ordinary errata barriers scoped to the exact orphaned
// location, except a known XMR reversal, even before local completion.
func (t errataTombstone) blocksMinedHeight(height int64) bool {
	if t.isPermanentlyReverted() || t.hasCommittedRevertedOutcome() {
		return true
	}
	if t.OrphanHeight > 0 && height == t.OrphanHeight {
		return true
	}
	return slices.Contains(t.OrphanHeights, height)
}

// blocksObservedTx fails closed when an observation cannot establish its
// mining location. Permanent reversal is TxID-wide and needs no location.
func (t errataTombstone) blocksObservedTx(tx common.ObservedTx) bool {
	if t.isPermanentlyReverted() {
		return true
	}
	height, err := tx.CanonicalMinedHeight()
	return err != nil || t.blocksMinedHeight(height)
}

// canonicalTxInMinedHeight reads the scanner-owned location before finalization
// rewrites the observation height. Mempool/future XMR items are not mined yet.
func canonicalTxInMinedHeight(txIn *types.TxIn, item *types.TxInItem) (int64, bool) {
	if txIn == nil || item == nil || item.BlockHeight <= 0 {
		return 0, false
	}
	if txIn.Chain.UsesHeightScopedErrata() && (txIn.MemPool || txIn.AllowFutureObservation) {
		return 0, false
	}
	return item.BlockHeight, true
}

func (t errataTombstone) blocksTxInItem(txIn *types.TxIn, item *types.TxInItem) bool {
	if !txIn.Chain.UsesHeightScopedErrata() {
		return t.blocksMinedHeight(item.BlockHeight)
	}
	height, ok := canonicalTxInMinedHeight(txIn, item)
	return !ok || t.blocksMinedHeight(height)
}

func appendUniqueErrataHeight(heights []int64, height int64) []int64 {
	if height <= 0 || slices.Contains(heights, height) {
		return heights
	}
	return append(heights, height)
}

type errataDeckMutation struct {
	key  txInKey
	txIn *types.TxIn // nil deletes the exact original deck key
}

// NewObserverStorage create a new instance of LevelDBScannerStorage
func NewObserverStorage(path string, opts config.LevelDBOptions) (*ObserverStorage, error) {
	ldb, err := db.NewLevelDB(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create observer storage: %w", err)
	}

	return &ObserverStorage{db: ldb}, nil
}

// createTxKey creates a unique key for a TxIn based on chain, finalization height, and future-observation mode.
func (s *ObserverStorage) createTxKey(txIn *types.TxIn, finalizeHeight int64) string {
	if finalizeHeight == 0 && len(txIn.TxArray) > 0 {
		finalizeHeight = txIn.TxArray[0].BlockHeight + txIn.ConfirmationRequired
	}

	return createObserverTxKey(txIn.Chain, finalizeHeight, txIn.AllowFutureObservation)
}

func createObserverTxKey(chain common.Chain, finalizeHeight int64, allowFutureObservation bool) string {
	key := fmt.Sprintf("%s%s:%d", OnDeckTxKeyPrefix, chain.String(), finalizeHeight)
	if allowFutureObservation {
		key += ":future"
	}
	return key
}

func errataTombstoneKey(chain common.Chain, txID string) string {
	return fmt.Sprintf("%s%s:%s", ErrataTombstoneKeyPrefix, chain.String(), strings.ToLower(strings.TrimSpace(txID)))
}

func (s *ObserverStorage) getErrataTombstone(chain common.Chain, txID string) (*errataTombstone, error) {
	data, err := s.db.Get([]byte(errataTombstoneKey(chain, txID)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read observer errata tombstone: %w", err)
	}
	var tombstone errataTombstone
	if err = json.Unmarshal(data, &tombstone); err != nil {
		return nil, fmt.Errorf("decode observer errata tombstone: %w", err)
	}
	if err = validateErrataTombstone(tombstone); err != nil {
		return nil, err
	}
	if !tombstone.Chain.Equals(chain) ||
		!strings.EqualFold(strings.TrimSpace(tombstone.TxID.String()), strings.TrimSpace(txID)) {
		return nil, errors.New("observer errata tombstone key does not match its payload")
	}
	return &tombstone, nil
}

func validateErrataTombstone(tombstone errataTombstone) error {
	if tombstone.Chain.IsEmpty() || strings.TrimSpace(tombstone.TxID.String()) == "" ||
		tombstone.OrphanHeight < 0 || tombstone.Generation == 0 || tombstone.VerifyAfterHeight < 0 {
		return errors.New("observer errata tombstone is invalid")
	}
	for _, height := range tombstone.OrphanHeights {
		if height <= 0 {
			return errors.New("observer errata tombstone has an invalid orphan height")
		}
	}
	if !tombstone.Committed && (tombstone.VoterReverted != nil || tombstone.VerifyAfterHeight != 0) {
		return errors.New("uncommitted observer errata tombstone has a voter result")
	}
	switch tombstone.Outcome {
	case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED:
	case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED:
		if !tombstone.Committed || !tombstone.Chain.RequiresTerminalErrataOutcome() ||
			tombstone.VerifyAfterHeight != 0 || (tombstone.VoterReverted != nil && !*tombstone.VoterReverted) {
			return errors.New("observer errata reverted outcome is invalid")
		}
	case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION:
		if !tombstone.Committed || !tombstone.Chain.RequiresTerminalErrataOutcome() ||
			tombstone.VoterReverted == nil || *tombstone.VoterReverted {
			return errors.New("observer errata no-applicable-observation outcome is invalid")
		}
	default:
		return errors.New("observer errata tombstone has an unknown outcome")
	}
	if tombstone.VerifyAfterHeight != 0 && !tombstone.Chain.Equals(common.XMRChain) {
		return errors.New("non-XMR observer errata has a verification height")
	}
	if tombstone.VoterReverted != nil {
		if !tombstone.Chain.Equals(common.XMRChain) || tombstone.VerifyAfterHeight != 0 {
			return errors.New("observer errata voter result is invalid")
		}
	}
	return nil
}

type errataTombstoneUpdate struct {
	forceCommitted    bool
	completeOutbox    bool
	verifyAfterHeight int64
	voterReverted     *bool
	outcome           common.ErrataTxOutcome
	recordDelivery    bool
}

// applyErrata atomically fsyncs every tombstone and the corresponding exact
// on-deck mutations. A crash can therefore expose either the old deck without
// a tombstone or the new deck with tombstones, never a resurrectable orphan.
func (s *ObserverStorage) applyErrata(
	block types.ErrataBlock,
	mutations []errataDeckMutation,
) error {
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	return s.applyErrataBatch(block, errataTombstoneUpdate{recordDelivery: true}, mutations)
}

// applyLegacyErrata writes only the TxID tombstone and deck cleanup. Producers
// without an acknowledgement callback retain their original one-shot gossip
// behavior and must not silently enter XMR's durable retry outbox.
func (s *ObserverStorage) applyLegacyErrata(
	block types.ErrataBlock,
	mutations []errataDeckMutation,
) error {
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	return s.applyErrataBatch(block, errataTombstoneUpdate{}, mutations)
}

// applyCommittedErrata is retained for focused storage tests. Committed means
// only that the errata quorum was delivered; it is not evidence that
// ObservedTxVoter.Reverted was set.
func (s *ObserverStorage) applyCommittedErrata(
	errata common.ErrataTx,
	mutations []errataDeckMutation,
) error {
	if errata.Chain.IsEmpty() || errata.Id.IsEmpty() {
		return errors.New("committed observer errata is invalid")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	return s.applyErrataBatch(types.ErrataBlock{
		Txs: []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}, errataTombstoneUpdate{forceCommitted: true}, mutations)
}

// applyErrataBatch requires errataMu for the entire read-modify-write batch,
// including legacy receipt updates that can overlap terminal outcome writes.
func (s *ObserverStorage) applyErrataBatch(
	block types.ErrataBlock,
	update errataTombstoneUpdate,
	mutations []errataDeckMutation,
) error {
	if update.completeOutbox && !update.forceCommitted {
		return errors.New("observer errata outbox completion requires a committed event")
	}
	if update.verifyAfterHeight < 0 {
		return errors.New("observer errata verification height is invalid")
	}
	if update.voterReverted != nil && update.verifyAfterHeight != 0 {
		return errors.New("observer errata voter result update is invalid")
	}
	batch := new(leveldb.Batch)
	seen := make(map[string]struct{}, len(block.Txs))
	for _, tx := range block.Txs {
		key := errataTombstoneKey(tx.Chain, tx.TxID.String())
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		tombstone := errataTombstone{
			Chain:        tx.Chain,
			TxID:         tx.TxID,
			OrphanHeight: block.Height,
			Generation:   1,
			Committed:    false,
		}
		if block.Height > 0 {
			tombstone.OrphanHeights = []int64{block.Height}
		}
		hadLocalDelivery := false
		if existing, err := s.getErrataTombstone(tx.Chain, tx.TxID.String()); err != nil {
			return err
		} else if existing != nil {
			hadLocalDelivery = existing.OrphanHeight > 0 || len(existing.OrphanHeights) > 0
			tombstone = *existing
			if block.Height <= 0 || tombstone.blocksMinedHeight(block.Height) {
				// The scanner can crash after this batch is fsynced but before it
				// consumes OnStored. Its restart replay is the same handoff, not a
				// new generation: preserve a committed receipt so an already-final
				// THOR reversal cannot become permanently pending again.
			} else {
				if existing.Generation == math.MaxUint64 {
					return fmt.Errorf("observer errata tombstone generation exhausted for %s", tx.TxID)
				}
				tombstone.Generation++
				tombstone.OrphanHeight = block.Height
				tombstone.OrphanHeights = appendUniqueErrataHeight(tombstone.OrphanHeights, existing.OrphanHeight)
				tombstone.OrphanHeights = appendUniqueErrataHeight(tombstone.OrphanHeights, block.Height)
				// Delivery and an explicit voter result are TxID-scoped. A later
				// scanner height refines the exact local barrier without reopening
				// an already-delivered errata outbox entry.
			}
		}
		if update.forceCommitted {
			tombstone.Committed = true
			if tombstone.Outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED && tombstone.VoterReverted == nil && tombstone.VerifyAfterHeight == 0 {
				tombstone.VerifyAfterHeight = update.verifyAfterHeight
			}
		}
		if update.outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED {
			if tombstone.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED && tombstone.Outcome != update.outcome {
				return fmt.Errorf("observer errata outcome conflicts for %s", tx.TxID)
			}
			if tombstone.VoterReverted != nil && *tombstone.VoterReverted != (update.outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED) {
				return fmt.Errorf("observer errata voter result conflicts for %s", tx.TxID)
			}
			tombstone.Committed = true
			tombstone.Outcome = update.outcome
			tombstone.VerifyAfterHeight = 0
			if update.outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION {
				result := false
				tombstone.VoterReverted = &result
			}
		}
		if update.voterReverted != nil {
			if tombstone.VoterReverted != nil && *tombstone.VoterReverted != *update.voterReverted {
				return fmt.Errorf("observer errata voter result conflicts for %s", tx.TxID)
			}
			result := *update.voterReverted
			outcome := common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION
			if result {
				outcome = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED
			}
			if tombstone.Outcome != common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED && tombstone.Outcome != outcome {
				return fmt.Errorf("observer errata outcome conflicts for %s", tx.TxID)
			}
			tombstone.Outcome = outcome
			tombstone.Committed = true
			tombstone.VoterReverted = &result
			tombstone.VerifyAfterHeight = 0
		}
		if err := validateErrataTombstone(tombstone); err != nil {
			return err
		}
		encoded, err := json.Marshal(tombstone)
		if err != nil {
			return fmt.Errorf("encode observer errata tombstone: %w", err)
		}
		batch.Put([]byte(key), encoded)
		// A committed quorum event is useful for the local durable outbox only
		// when it contains this node's attestation. The gossip layer enforces
		// that ownership check before calling recordLocalErrataCommitted.
		// Persist the tombstone and outbox completion in this same sync batch so
		// a crash cannot leave an acknowledged local vote retrying indefinitely.
		if update.completeOutbox && errataOutboxChain(tx.Chain) {
			outboxKey := []byte(pendingErrataKey(tx))
			record, found, getErr := s.getPersistedErrataLocked(tx)
			if getErr != nil {
				return getErr
			}
			// Scanner delivery writes its tombstone and outbox record in one
			// batch. If delivery is already visible but its record is gone, this
			// is a duplicate committed event after completion; do not resurrect
			// a commit-only retry record. A commit before scanner handoff has no
			// delivered height and still needs the placeholder.
			if !found && !hadLocalDelivery {
				record = persistedErrata{Tx: tx}
				found = true
			}
			if found {
				record.Tx = tx
				record.Committed = true
				if record.CommittedAtUnixNano == 0 {
					record.CommittedAtUnixNano = time.Now().UnixNano()
				}
				if errataFinished(record) {
					batch.Delete(outboxKey)
				} else {
					outboxData, encodeErr := json.Marshal(record)
					if encodeErr != nil {
						return fmt.Errorf("encode committed errata outbox: %w", encodeErr)
					}
					batch.Put(outboxKey, outboxData)
				}
			}
		}

		if update.recordDelivery && errataOutboxChain(tx.Chain) {
			outboxKey := []byte(pendingErrataKey(tx))
			record, found, getErr := s.getPersistedErrataLocked(tx)
			if getErr != nil {
				return getErr
			}
			if !found {
				record = persistedErrata{Tx: tx}
			}
			record.Height = block.Height
			record.DeliveryID = block.DeliveryID
			record.Tx = tx
			record.DeliverySeen = true
			record.Committed = record.Committed || tombstone.Committed
			// The same synchronous batch contains the TxID tombstone and every
			// on-deck mutation, so callback delivery is already cleanup-complete.
			record.CleanupComplete = true
			if errataFinished(record) {
				batch.Delete(outboxKey)
			} else {
				outboxData, encodeErr := json.Marshal(record)
				if encodeErr != nil {
					return fmt.Errorf("encode callback errata outbox: %w", encodeErr)
				}
				batch.Put(outboxKey, outboxData)
			}
		}
	}
	for _, mutation := range mutations {
		key := []byte(createObserverTxKey(
			mutation.key.chain,
			mutation.key.height,
			mutation.key.allowFutureObservation,
		))
		if mutation.txIn == nil {
			batch.Delete(key)
			continue
		}
		data, err := json.Marshal(mutation.txIn)
		if err != nil {
			return fmt.Errorf("encode errata-filtered on-deck transaction: %w", err)
		}
		batch.Put(key, data)
	}
	if s.beforeErrataWrite != nil {
		if err := s.beforeErrataWrite(); err != nil {
			return fmt.Errorf("before observer errata write: %w", err)
		}
	}
	if err := s.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return fmt.Errorf("persist observer errata tombstones and deck mutations: %w", err)
	}
	return nil
}

func (s *ObserverStorage) MigrateLegacy() ([]*types.TxIn, error) {
	_, err := s.db.Get([]byte(LegacyMigrationHandledKey), nil)
	if !errors.Is(err, leveldb.ErrNotFound) {
		return nil, nil
	}
	buf, err := s.db.Get([]byte(LegacyOnDeckKey), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to get ondeck tx from key value store: %w", err)
	}
	var result []*types.TxIn
	if err = json.Unmarshal(buf, &result); err != nil {
		return nil, fmt.Errorf("fail to unmarshal ondeck tx: %w", err)
	}

	// Migrate the ondeck tx to the new format
	for _, txIn := range result {
		key := s.createTxKey(txIn, 0)
		data, err := json.Marshal(txIn)
		if err != nil {
			return nil, fmt.Errorf("fail to marshal ondeck tx to json: %w", err)
		}
		if err := s.db.Put([]byte(key), data, nil); err != nil {
			return nil, fmt.Errorf("fail to put ondeck tx to key value store: %w", err)
		}
	}

	if err := s.db.Delete([]byte(LegacyOnDeckKey), nil); err != nil {
		return nil, fmt.Errorf("fail to delete legacy ondeck tx from key value store: %w", err)
	}

	if err := s.db.Put([]byte(LegacyMigrationHandledKey), []byte("true"), nil); err != nil {
		return nil, fmt.Errorf("fail to put migration handled flag to key value store: %w", err)
	}

	return result, nil
}

// GetOnDeckTxs retrieve the ondeck tx from key value store
func (s *ObserverStorage) GetOnDeckTxs() ([]*types.TxIn, error) {
	// Check if the legacy migration has been handled
	legacyTxs, err := s.MigrateLegacy()
	if err != nil {
		return nil, fmt.Errorf("fail to migrate legacy ondeck tx: %w", err)
	}

	// If legacy txs exist, return them
	if len(legacyTxs) > 0 {
		return s.filterTombstonedOnDeck(legacyTxs)
	}

	// Use a DB iterator to get all keys with the prefix
	iter := s.db.NewIterator(util.BytesPrefix([]byte(OnDeckTxKeyPrefix)), nil)
	defer iter.Release()

	var result []*types.TxIn
	for iter.Next() {
		var txIn types.TxIn
		if err := json.Unmarshal(iter.Value(), &txIn); err != nil {
			return nil, fmt.Errorf("fail to unmarshal ondeck tx: %w", err)
		}
		result = append(result, &txIn)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("error iterating through ondeck txs: %w", err)
	}

	return s.filterTombstonedOnDeck(result)
}

// filterTombstonedOnDeck is a defensive restart barrier. The normal errata
// path batches tombstone and deck mutation atomically, but this also prevents
// stale rows from older versions or interrupted migrations entering memory.
func (s *ObserverStorage) filterTombstonedOnDeck(txIns []*types.TxIn) ([]*types.TxIn, error) {
	result := make([]*types.TxIn, 0, len(txIns))
	for _, txIn := range txIns {
		if txIn == nil || len(txIn.TxArray) == 0 {
			continue
		}
		finalizeHeight := txIn.TxArray[0].BlockHeight + txIn.ConfirmationRequired
		kept := make([]*types.TxInItem, 0, len(txIn.TxArray))
		for _, item := range txIn.TxArray {
			if item == nil {
				continue
			}
			tombstone, err := s.getErrataTombstone(txIn.Chain, item.Tx)
			if err != nil {
				return nil, err
			}
			if tombstone != nil && tombstone.blocksTxInItem(txIn, item) {
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			continue
		}
		copyTxIn := *txIn
		copyTxIn.TxArray = kept
		copyTxIn.ConfirmationRequired = finalizeHeight - kept[0].BlockHeight
		result = append(result, &copyTxIn)
	}
	return result, nil
}

// AddOrUpdateTx adds or updates a single TxIn in storage
func (s *ObserverStorage) AddOrUpdateTx(txIn *types.TxIn) error {
	return s.addOrUpdateTx(txIn, observerWriteOptions(txIn))
}

// addOrUpdateTxSync rewrites an already on-deck transaction with an fsync.
// This is required when a callback-bearing replay deduplicates against an
// entry that may originally have been written with ordinary async durability.
func (s *ObserverStorage) addOrUpdateTxSync(txIn *types.TxIn) error {
	if s.beforeSyncTxWrite != nil {
		if err := s.beforeSyncTxWrite(); err != nil {
			return fmt.Errorf("before synchronous on-deck write: %w", err)
		}
	}
	return s.addOrUpdateTx(txIn, &opt.WriteOptions{Sync: true})
}

func (s *ObserverStorage) getErrataTombstones() ([]errataTombstone, error) {
	iter := s.db.NewIterator(util.BytesPrefix([]byte(ErrataTombstoneKeyPrefix)), nil)
	defer iter.Release()
	result := make([]errataTombstone, 0)
	for iter.Next() {
		var tombstone errataTombstone
		if err := json.Unmarshal(iter.Value(), &tombstone); err != nil {
			return nil, fmt.Errorf("decode observer errata tombstone: %w", err)
		}
		if err := validateErrataTombstone(tombstone); err != nil {
			return nil, err
		}
		if string(iter.Key()) != errataTombstoneKey(tombstone.Chain, tombstone.TxID.String()) {
			return nil, errors.New("observer errata tombstone key does not match its payload")
		}
		result = append(result, tombstone)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate observer errata tombstones: %w", err)
	}
	return result, nil
}

func (s *ObserverStorage) getPendingErrata() ([]errataTombstone, error) {
	tombstones, err := s.getErrataTombstones()
	if err != nil {
		return nil, err
	}
	result := make([]errataTombstone, 0, len(tombstones))
	for _, tombstone := range tombstones {
		if !tombstone.Committed {
			result = append(result, tombstone)
		}
	}
	return result, nil
}

func (s *ObserverStorage) recordErrataCommitted(errata common.ErrataTx, verifyAfterHeight int64) error {
	if errata.Chain.IsEmpty() || errata.Id.IsEmpty() {
		return errors.New("committed observer errata is invalid")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	return s.applyErrataBatch(types.ErrataBlock{
		Txs: []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}, errataTombstoneUpdate{
		forceCommitted:    true,
		verifyAfterHeight: verifyAfterHeight,
	}, nil)
}

// recordLocalErrataCommitted atomically records the committed tombstone and
// completes this node's durable retry. Callers must first prove that the
// committed quorum event includes this node's attestation.
func (s *ObserverStorage) recordLocalErrataCommitted(errata common.ErrataTx, outcome common.ErrataTxOutcome) error {
	if errata.Chain.RequiresTerminalErrataOutcome() {
		switch outcome {
		case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED:
			// Delivery without a terminal result does not retire durable retries.
			return nil
		case common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED,
			common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION:
		default:
			return fmt.Errorf("unknown committed errata outcome %d", outcome)
		}
	} else {
		outcome = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED
	}
	if errata.Chain.IsEmpty() || errata.Id.IsEmpty() {
		return errors.New("committed observer errata is invalid")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	return s.applyErrataBatch(types.ErrataBlock{
		Txs: []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}, errataTombstoneUpdate{
		forceCommitted: true,
		completeOutbox: true,
		outcome:        outcome,
	}, nil)
}

func (s *ObserverStorage) getUnresolvedCommittedXMRErrata() ([]errataTombstone, error) {
	tombstones, err := s.getErrataTombstones()
	if err != nil {
		return nil, err
	}
	result := make([]errataTombstone, 0, len(tombstones))
	for _, tombstone := range tombstones {
		if tombstone.Chain.Equals(common.XMRChain) && tombstone.Committed && tombstone.VoterReverted == nil {
			result = append(result, tombstone)
		}
	}
	return result, nil
}

func (s *ObserverStorage) recordErrataVoterResult(
	errata common.ErrataTx,
	reverted bool,
	mutations []errataDeckMutation,
) error {
	if !errata.Chain.Equals(common.XMRChain) || errata.Id.IsEmpty() {
		return errors.New("observer errata voter result is invalid")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	tombstone, err := s.getErrataTombstone(errata.Chain, errata.Id.String())
	if err != nil {
		return err
	}
	if tombstone == nil || !tombstone.Committed {
		return errors.New("observer errata voter result has no committed delivery")
	}
	return s.applyErrataBatch(types.ErrataBlock{
		Txs: []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}, errataTombstoneUpdate{voterReverted: &reverted}, mutations)
}

func (s *ObserverStorage) addOrUpdateTx(txIn *types.TxIn, writeOptions *opt.WriteOptions) error {
	key := s.createTxKey(txIn, 0)
	data, err := json.Marshal(txIn)
	if err != nil {
		return fmt.Errorf("fail to marshal ondeck tx to json: %w", err)
	}
	return s.db.Put([]byte(key), data, writeOptions)
}

// AddErrataBlock persists errata before the scanner's reorg journal advances.
func (s *ObserverStorage) AddErrataBlock(block types.ErrataBlock) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("observer storage is not configured")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	batch := new(leveldb.Batch)
	for _, tx := range block.Txs {
		key := []byte(pendingErrataKey(tx))
		record, found, err := s.getPersistedErrataLocked(tx)
		if err != nil {
			return err
		}
		if !found {
			record = persistedErrata{Tx: tx}
		}
		record.Height = block.Height
		record.DeliveryID = block.DeliveryID
		record.Tx = tx
		record.DeliverySeen = true
		if errataFinished(record) {
			batch.Delete(key)
			continue
		}
		data, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("fail to marshal pending errata: %w", err)
		}
		batch.Put(key, data)
	}
	if batch.Len() == 0 {
		return nil
	}
	if err := s.db.Write(batch, errataWriteOptions(block.Txs)); err != nil {
		return fmt.Errorf("fail to persist pending errata: %w", err)
	}
	return nil
}

func (s *ObserverStorage) GetPendingErrata() ([]persistedErrata, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	iter := s.db.NewIterator(util.BytesPrefix([]byte(PendingErrataKeyPrefix)), nil)
	defer iter.Release()

	result := make([]persistedErrata, 0)
	for iter.Next() {
		record, err := decodePersistedErrata(iter.Value())
		if err != nil {
			return nil, fmt.Errorf("fail to decode pending errata: %w", err)
		}
		if record.AttestationRetryExhausted {
			continue
		}
		result = append(result, record)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("fail to iterate pending errata: %w", err)
	}
	return result, nil
}

func (s *ObserverStorage) GetPendingErrataTx(tx types.ErrataTx) (persistedErrata, bool, error) {
	if s == nil || s.db == nil {
		return persistedErrata{}, false, nil
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	return s.getPersistedErrataLocked(tx)
}

// errataOutboxChain reports whether a chain uses the durable, acknowledged
// errata outbox. Only XMR does: the outbox exists to make XMR reorg replay
// restartable, and only the XMR scanner sets ErrataBlock.Persisted. Other
// chains publish errata without a local durability handshake, so there is no
// local delivery for a THORChain commit to race against.
func errataOutboxChain(chain common.Chain) bool {
	return chain.Equals(common.XMRChain)
}

// MarkErrataCommitted records the THORChain commit independently from local
// on-deck cleanup. A commit that arrives before the local scanner delivery is
// retained as a tombstone so a later AddErrataBlock cannot resurrect work that
// THORChain already accepted.
//
// The commit handler fires for every quorum errata on every chain, including
// ones this node never delivered. Creating a tombstone for those would write a
// LevelDB record per BTC/ETH/DOGE errata on every node, which the retry ticker
// then re-reads until the 30-day prune collects it. The tombstone only defends
// against a commit racing ahead of a local delivery, which is outbox-only, so
// a commit with no local record on a non-outbox chain is dropped.
func (s *ObserverStorage) MarkErrataCommitted(tx types.ErrataTx) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("observer storage is not configured")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	record, found, err := s.getPersistedErrataLocked(tx)
	if err != nil {
		return err
	}
	if !found {
		if !errataOutboxChain(tx.Chain) {
			return nil
		}
		record = persistedErrata{Tx: tx}
	}
	record.Tx = tx
	record.Committed = true
	if record.CommittedAtUnixNano == 0 {
		record.CommittedAtUnixNano = time.Now().UnixNano()
	}
	return s.writePersistedErrataLocked(record)
}

// RecordErrataAttestationAttempt durably advances the retry schedule before an
// attestation is emitted. It never creates or resurrects a record: a concurrent
// commit notification that removed the outbox item wins.
func (s *ObserverStorage) RecordErrataAttestationAttempt(tx types.ErrataTx, at time.Time) (persistedErrata, bool, error) {
	if s == nil || s.db == nil {
		return persistedErrata{}, false, fmt.Errorf("observer storage is not configured")
	}
	if at.IsZero() {
		return persistedErrata{}, false, fmt.Errorf("errata attestation attempt time is zero")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	record, found, err := s.getPersistedErrataLocked(tx)
	if err != nil || !found {
		return record, false, err
	}
	if record.Committed || record.AttestationRetryExhausted || !record.CleanupComplete {
		return record, false, nil
	}
	atNano := at.UnixNano()
	if record.FirstAttemptAtUnixNano == 0 {
		record.FirstAttemptAtUnixNano = atNano
	}
	record.LastAttemptAtUnixNano = atNano
	record.AttestationAttempts++
	if err = s.writePersistedErrataLocked(record); err != nil {
		return persistedErrata{}, false, err
	}
	return record, true, nil
}

// MarkErrataAttestationRetryExhausted places an unconfirmed errata record in a
// durable terminal quarantine. A later commit event still completes and
// removes it, but the observer will no longer gossip it indefinitely.
func (s *ObserverStorage) MarkErrataAttestationRetryExhausted(tx types.ErrataTx, at time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("observer storage is not configured")
	}
	if at.IsZero() {
		return false, fmt.Errorf("errata retry exhaustion time is zero")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	record, found, err := s.getPersistedErrataLocked(tx)
	if err != nil || !found {
		return false, err
	}
	if record.Committed || record.AttestationRetryExhausted || !record.CleanupComplete {
		return false, nil
	}
	record.AttestationRetryExhausted = true
	record.RetryExhaustedAtUnixNano = at.UnixNano()
	if err = s.writePersistedErrataLocked(record); err != nil {
		return false, err
	}
	return true, nil
}

// PruneCommittedOnlyErrata bounds commit-before-delivery tombstones after a
// conservative safety horizon. Records whose scanner delivery or cleanup has
// begun remain in the durable outbox until normal completion.
func (s *ObserverStorage) PruneCommittedOnlyErrata(cutoff time.Time) (int, error) {
	if s == nil || s.db == nil || cutoff.IsZero() {
		return 0, nil
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	iter := s.db.NewIterator(util.BytesPrefix([]byte(PendingErrataKeyPrefix)), nil)
	defer iter.Release()
	batch := new(leveldb.Batch)
	pruned := 0
	syncWrite := false
	cutoffNano := cutoff.UnixNano()
	for iter.Next() {
		record, err := decodePersistedErrata(iter.Value())
		if err != nil {
			return 0, fmt.Errorf("fail to decode pending errata while pruning: %w", err)
		}
		if !record.Committed || record.DeliverySeen || record.CleanupComplete ||
			record.CommittedAtUnixNano == 0 || record.CommittedAtUnixNano > cutoffNano {
			continue
		}
		batch.Delete(append([]byte(nil), iter.Key()...))
		pruned++
		syncWrite = syncWrite || record.Tx.Chain.Equals(common.XMRChain)
	}
	if err := iter.Error(); err != nil {
		return 0, fmt.Errorf("fail to iterate pending errata while pruning: %w", err)
	}
	if pruned == 0 {
		return 0, nil
	}
	if err := s.db.Write(batch, syncWriteOptions(syncWrite)); err != nil {
		return 0, fmt.Errorf("fail to prune committed-only errata tombstones: %w", err)
	}
	return pruned, nil
}

// CompleteErrataCleanup atomically persists every exact on-deck mutation and
// the cleanup-complete state for the corresponding errata records. In-memory
// state is changed only after this batch succeeds.
func (s *ObserverStorage) CompleteErrataCleanup(txs []types.ErrataTx, mutations []errataCleanupMutation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("observer storage is not configured")
	}
	s.errataMu.Lock()
	defer s.errataMu.Unlock()

	batch := new(leveldb.Batch)
	for _, mutation := range mutations {
		if mutation.TxIn == nil {
			return fmt.Errorf("errata cleanup contains nil on-deck mutation")
		}
		key := []byte(s.createTxKey(mutation.TxIn, mutation.FinalizeHeight))
		if mutation.Delete {
			batch.Delete(key)
			continue
		}
		data, err := json.Marshal(mutation.TxIn)
		if err != nil {
			return fmt.Errorf("fail to marshal errata-cleaned on-deck tx: %w", err)
		}
		batch.Put(key, data)
	}

	seen := make(map[string]struct{}, len(txs))
	for _, tx := range txs {
		keyString := pendingErrataKey(tx)
		if _, ok := seen[keyString]; ok {
			continue
		}
		seen[keyString] = struct{}{}
		record, found, err := s.getPersistedErrataLocked(tx)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		record.CleanupComplete = true
		key := []byte(keyString)
		if errataFinished(record) {
			batch.Delete(key)
			continue
		}
		data, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("fail to marshal cleaned errata: %w", err)
		}
		batch.Put(key, data)
	}

	if batch.Len() == 0 {
		return nil
	}
	if err := s.db.Write(batch, errataCleanupWriteOptions(txs, mutations)); err != nil {
		return fmt.Errorf("fail to durably apply errata cleanup: %w", err)
	}
	return nil
}

func (s *ObserverStorage) getPersistedErrataLocked(tx types.ErrataTx) (persistedErrata, bool, error) {
	value, err := s.db.Get([]byte(pendingErrataKey(tx)), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return persistedErrata{}, false, nil
		}
		return persistedErrata{}, false, fmt.Errorf("fail to load pending errata: %w", err)
	}
	record, err := decodePersistedErrata(value)
	if err != nil {
		return persistedErrata{}, false, fmt.Errorf("fail to decode pending errata: %w", err)
	}
	return record, true, nil
}

func (s *ObserverStorage) writePersistedErrataLocked(record persistedErrata) error {
	key := []byte(pendingErrataKey(record.Tx))
	if errataFinished(record) {
		if err := s.db.Delete(key, chainWriteOptions(record.Tx.Chain)); err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return fmt.Errorf("fail to remove completed errata: %w", err)
		}
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("fail to marshal pending errata: %w", err)
	}
	if err = s.db.Put(key, data, chainWriteOptions(record.Tx.Chain)); err != nil {
		return fmt.Errorf("fail to persist pending errata state: %w", err)
	}
	return nil
}

func decodePersistedErrata(data []byte) (persistedErrata, error) {
	var record persistedErrata
	if err := json.Unmarshal(data, &record); err != nil {
		return persistedErrata{}, err
	}
	if !record.DeliverySeen && (record.Height != 0 || record.DeliveryID != "") {
		record.DeliverySeen = true
	}
	return record, nil
}

func errataFinished(record persistedErrata) bool {
	return record.DeliverySeen && record.CleanupComplete && record.Committed
}

func pendingErrataKey(tx types.ErrataTx) string {
	return fmt.Sprintf("%s%s:%s", PendingErrataKeyPrefix, tx.Chain, tx.TxID)
}

func syncWriteOptions(sync bool) *opt.WriteOptions {
	if sync {
		return &opt.WriteOptions{Sync: true}
	}
	return nil
}

func errataWriteOptions(txs []types.ErrataTx) *opt.WriteOptions {
	for _, tx := range txs {
		if tx.Chain.Equals(common.XMRChain) {
			return syncWriteOptions(true)
		}
	}
	return nil
}

func errataCleanupWriteOptions(txs []types.ErrataTx, mutations []errataCleanupMutation) *opt.WriteOptions {
	if options := errataWriteOptions(txs); options != nil {
		return options
	}
	for _, mutation := range mutations {
		if mutation.TxIn != nil && mutation.TxIn.Chain.Equals(common.XMRChain) {
			return syncWriteOptions(true)
		}
	}
	return nil
}

// chainWriteOptions requests an fsync for chains whose local observer state
// must survive an unclean shutdown. Only XMR needs it today.
func chainWriteOptions(chain common.Chain) *opt.WriteOptions {
	return syncWriteOptions(chain.Equals(common.XMRChain))
}

// observerWriteOptions requests an fsync for observations on a chain with
// durable local state (XMR) and for any observation participating in an
// explicit durable scanner-to-observer handoff. Ordinary chain traffic retains
// the existing asynchronous LevelDB write behavior.
func observerWriteOptions(txIn *types.TxIn) *opt.WriteOptions {
	if txIn == nil {
		return nil
	}
	if options := chainWriteOptions(txIn.Chain); options != nil {
		return options
	}
	for _, item := range txIn.TxArray {
		if item != nil && (item.OnObservedTxStored != nil || item.OnObservedTxRejected != nil) {
			return &opt.WriteOptions{Sync: true}
		}
	}
	return nil
}

// RemoveTx removes a single TxIn from storage
func (s *ObserverStorage) RemoveTx(txIn *types.TxIn, finalizeHeight int64) error {
	key := s.createTxKey(txIn, finalizeHeight)
	return s.db.Delete([]byte(key), observerWriteOptions(txIn))
}

// RemoveAllTxs removes all TxIn from storage
func (s *ObserverStorage) RemoveAllTxs() error {
	iter := s.db.NewIterator(util.BytesPrefix([]byte(OnDeckTxKeyPrefix)), nil)
	defer iter.Release()

	for iter.Next() {
		if err := s.db.Delete(iter.Key(), nil); err != nil {
			return fmt.Errorf("fail to delete ondeck tx from key value store: %w", err)
		}
	}

	if err := iter.Error(); err != nil {
		return fmt.Errorf("error iterating through ondeck txs: %w", err)
	}

	return nil
}

func (s *ObserverStorage) Close() error {
	return s.db.Close()
}

const (
	pendingXMRCertificatePrefix = "xmr-certificate-v1:"
	maxPendingXMRCertificates   = 256
)

type pendingXMRCertificate struct {
	// Zero means an older local record has no known enqueue time. Preserve it.
	FirstEnqueuedAt time.Time     `json:"first_enqueued_at"`
	Payload         []byte        `json:"payload"`
	NextAttempt     time.Time     `json:"next_attempt"`
	RetryDelay      time.Duration `json:"retry_delay"`
	key             string
	raw             []byte
}

func pendingXMRCertificateKey(tx common.AttestTx) (string, error) {
	// Both phases occupy one slot. Preserve the actual signed payload separately.
	obs := tx.ObsTx
	obs.BlockHeight = obs.FinaliseHeight
	payload, err := obs.GetSignablePayloadWithInbound(false)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%x", pendingXMRCertificatePrefix, sha256.Sum256(payload)), nil
}

func (s *ObserverStorage) savePendingXMRCertificate(tx common.AttestTx) error {
	if s == nil || s.db == nil {
		return errors.New("xmr certificate storage is unavailable")
	}
	payload, err := tx.Marshal()
	if err != nil {
		return err
	}
	if len(payload) > maxHistoricalXMRCertificateBytes {
		return errors.New("xmr certificate exceeds durable payload limit")
	}
	key, err := pendingXMRCertificateKey(tx)
	if err != nil {
		return err
	}
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	previous, err := s.db.Get([]byte(key), nil)
	record := pendingXMRCertificate{Payload: payload, NextAttempt: time.Now()}
	switch {
	case err == nil:
		if err = json.Unmarshal(previous, &record); err != nil {
			return err
		}
		var old common.AttestTx
		if err = old.Unmarshal(record.Payload); err != nil {
			return err
		}
		// Redelivery neither consumes a slot nor defeats retry backoff. Only a
		// transition to final replaces the signed bytes; never downgrade a final.
		if old.ObsTx.IsFinal() || !tx.ObsTx.IsFinal() {
			return nil
		}
		record.Payload = payload
		record.NextAttempt = time.Now()
	case errors.Is(err, leveldb.ErrNotFound):
		iter := s.db.NewIterator(util.BytesPrefix([]byte(pendingXMRCertificatePrefix)), nil)
		count := 0
		for iter.Next() {
			count++
			if count >= maxPendingXMRCertificates {
				break
			}
		}
		iterErr := iter.Error()
		iter.Release()
		if iterErr != nil {
			return iterErr
		}
		if count >= maxPendingXMRCertificates {
			return errors.New("pending xmr certificate queue is full; retry scanner handoff")
		}
		record.FirstEnqueuedAt = record.NextAttempt
	default:
		return err
	}
	value, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if s.beforeCertificateWrite != nil {
		if err = s.beforeCertificateWrite(); err != nil {
			return err
		}
	}
	return s.db.Put([]byte(key), value, &opt.WriteOptions{Sync: true})
}

// Statistics come from the same bounded scan as retry selection, including
// records still in backoff. Missing enqueue times do not prevent recovery.
type pendingXMRCertificateStats struct {
	count      int
	oldest     time.Time
	unknownAge bool
}

func (s pendingXMRCertificateStats) oldestAgeSeconds(now time.Time) float64 {
	if s.unknownAge {
		return -1
	}
	if s.oldest.IsZero() {
		return 0
	}
	return math.Max(0, now.Sub(s.oldest).Seconds())
}

func (s *ObserverStorage) pendingXMRCertificates(now time.Time, limit int) ([]pendingXMRCertificate, pendingXMRCertificateStats, error) {
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	iter := s.db.NewIterator(util.BytesPrefix([]byte(pendingXMRCertificatePrefix)), nil)
	defer iter.Release()
	records := make([]pendingXMRCertificate, 0)
	var stats pendingXMRCertificateStats
	for iter.Next() {
		stats.count++
		if stats.count > maxPendingXMRCertificates {
			return nil, stats, errors.New("pending xmr certificate queue exceeds limit")
		}
		var record pendingXMRCertificate
		if len(iter.Value()) > 2*maxHistoricalXMRCertificateBytes {
			return nil, stats, errors.New("oversized saved xmr certificate")
		}
		if err := json.Unmarshal(iter.Value(), &record); err != nil {
			return nil, stats, err
		}
		if record.FirstEnqueuedAt.IsZero() {
			stats.unknownAge = true
		} else if stats.oldest.IsZero() || record.FirstEnqueuedAt.Before(stats.oldest) {
			stats.oldest = record.FirstEnqueuedAt
		}
		if record.NextAttempt.After(now) {
			continue
		}
		record.key = string(iter.Key())
		record.raw = append([]byte(nil), iter.Value()...)
		records = append(records, record)
	}
	if err := iter.Error(); err != nil {
		return nil, stats, err
	}
	slices.SortFunc(records, func(a, b pendingXMRCertificate) int { return a.NextAttempt.Compare(b.NextAttempt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, stats, nil
}

// finishPendingXMRCertificate uses a compare-and-swap so retry completion
// cannot erase a newer final payload saved concurrently by scanner redelivery.
func (s *ObserverStorage) finishPendingXMRCertificate(record pendingXMRCertificate, complete bool, now time.Time) error {
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	current, err := s.db.Get([]byte(record.key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, record.raw) {
		return nil
	}
	if complete {
		return s.db.Delete([]byte(record.key), &opt.WriteOptions{Sync: true})
	}
	if record.RetryDelay <= 0 {
		record.RetryDelay = defaultObserveReconcileInterval
	} else {
		record.RetryDelay *= 2
	}
	if record.RetryDelay > xmrSpentRefsMaxRetryDelay {
		record.RetryDelay = xmrSpentRefsMaxRetryDelay
	}
	record.NextAttempt = now.Add(record.RetryDelay)
	value, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.db.Put([]byte(record.key), value, &opt.WriteOptions{Sync: true})
}

func (s *ObserverStorage) completePendingXMRCertificate(tx common.AttestTx) error {
	key, err := pendingXMRCertificateKey(tx)
	if err != nil {
		return err
	}
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()
	return s.db.Delete([]byte(key), &opt.WriteOptions{Sync: true})
}
