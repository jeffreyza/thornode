package monero

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

const (
	reorgBlockMetaPrefix                = "xmr-reorg-block-meta-v1-"
	scannerCatchupMarkerDBKey           = "xmr-scanner-catchup-marker-v1"
	scannerObservationReceiptPrefix     = "xmr-scanner-observation-v1-"
	scannerRelocationWatchPrefix        = "xmr-scanner-relocation-watch-v1-"
	scannerRevertedTxPrefix             = "xmr-scanner-reverted-tx-v1-"
	maxScannerObservationReceipts       = 4096
	maxScannerObservationReceiptBytes   = 64 << 20
	maxReorgMetasPrunedPerFetch         = 4096
	maxOrdinaryScannerRelocationWatches = 128
)

var (
	syncScannerWrite               = &opt.WriteOptions{Sync: true}
	errScannerRelocationWatchQuota = errors.New("ordinary xmr scanner relocation watch quota exceeded")
)

type reorgBlockMeta struct {
	Height                  int64    `json:"height"`
	BlockHash               string   `json:"block_hash"`
	PreviousHash            string   `json:"previous_hash"`
	CustomerTransactions    []string `json:"customer_transactions,omitempty"`
	VaultOriginTransactions []string `json:"vault_origin_transactions,omitempty"`
	ProvenanceComplete      bool     `json:"provenance_complete,omitempty"`
}

// scannerCatchupMarker is written before registration or churn recovery
// mutates the sidecar. StartHeight is advanced only in the same synced
// LevelDB batch which checkpoints that height's observation receipt and reorg
// journal entry. Receipts from earlier chunks remain until Observer fsyncs the
// complete mixed-height TxIn.
type scannerCatchupMarker struct {
	StartHeight  int64  `json:"start_height"`
	TargetHeight int64  `json:"target_height"`
	AnchorHeight int64  `json:"anchor_height"`
	AnchorHash   string `json:"anchor_hash"`
	// ChurnRollback preserves the recovery's rewind allowance across restarts
	// and lets a later Bifrost request extend it without skipping unfinished work.
	// Older and registration-only markers keep their existing recovery policy.
	ChurnRollback bool `json:"churn_rollback,omitempty"`
}

func (m scannerCatchupMarker) valid() bool {
	return m.StartHeight > 0 && m.TargetHeight >= 0 &&
		m.StartHeight <= m.TargetHeight+1 &&
		m.AnchorHeight == m.StartHeight-1 && strings.TrimSpace(m.AnchorHash) != ""
}

func (m scannerCatchupMarker) completed() bool {
	return m.valid() && m.StartHeight == m.TargetHeight+1
}

func (m scannerCatchupMarker) equal(other scannerCatchupMarker) bool {
	return m.StartHeight == other.StartHeight && m.TargetHeight == other.TargetHeight &&
		m.AnchorHeight == other.AnchorHeight && strings.EqualFold(m.AnchorHash, other.AnchorHash) &&
		m.ChurnRollback == other.ChurnRollback
}

type scannerObservationReceipt struct {
	Height       int64              `json:"height"`
	BlockHash    string             `json:"block_hash"`
	PreviousHash string             `json:"previous_hash"`
	Items        []*stypes.TxInItem `json:"items"`
}

func (r scannerObservationReceipt) valid() bool {
	if r.Height < 0 || strings.TrimSpace(r.BlockHash) == "" ||
		strings.TrimSpace(r.PreviousHash) == "" || len(r.Items) == 0 {
		return false
	}
	for _, item := range r.Items {
		if item == nil || item.IsEmpty() || item.BlockHeight != r.Height {
			return false
		}
	}
	return true
}

type scannerObservationReceiptClaim struct {
	key   []byte
	value []byte
	entry scannerObservationReceipt
}

type scannerRelocationWatch struct {
	TxID           string `json:"tx_id"`
	OriginalHeight int64  `json:"original_height"`
	InPool         bool   `json:"in_pool,omitempty"`
	HoldAbsent     bool   `json:"hold_absent,omitempty"`
	VaultOrigin    bool   `json:"vault_origin,omitempty"`
	LocationHeight int64  `json:"location_height"`
	BlockHash      string `json:"block_hash"`
	PreviousHash   string `json:"previous_hash"`
}

func (w scannerRelocationWatch) valid() bool {
	txID := normalizeTxID(w.TxID)
	decoded, err := hex.DecodeString(txID)
	if err != nil || len(decoded) != 32 || w.OriginalHeight < 0 {
		return false
	}
	if w.InPool || w.HoldAbsent {
		if w.InPool && w.HoldAbsent || w.HoldAbsent && !w.VaultOrigin {
			return false
		}
		return w.LocationHeight == 0 && strings.TrimSpace(w.BlockHash) == "" &&
			strings.TrimSpace(w.PreviousHash) == ""
	}
	return w.LocationHeight > 0 && strings.TrimSpace(w.BlockHash) != "" &&
		strings.TrimSpace(w.PreviousHash) != ""
}

type ReorgStore struct {
	db *leveldb.DB
	// Observer acknowledgements run after Scanner releases scanMu. Serialize
	// their compare-and-delete with every receipt/marker writer so an old
	// callback cannot delete a concurrently written replacement receipt.
	mu sync.Mutex
}

func NewReorgStore(db *leveldb.DB) *ReorgStore {
	return &ReorgStore{db: db}
}

// LatestHeight returns the highest retained journal height, or -1 if empty.
// Journal keys are not zero-padded, so compare their numeric heights.
func (s *ReorgStore) LatestHeight() (int64, error) {
	if s == nil || s.db == nil {
		return -1, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := int64(-1)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(reorgBlockMetaPrefix)), nil)
	defer iter.Release()
	for iter.Next() {
		height, err := strconv.ParseInt(strings.TrimPrefix(string(iter.Key()), reorgBlockMetaPrefix), 10, 64)
		if err != nil || height < 0 {
			return -1, fmt.Errorf("invalid xmr reorg journal key %q", iter.Key())
		}
		latest = max(latest, height)
	}
	if err := iter.Error(); err != nil {
		return -1, fmt.Errorf("find latest xmr reorg journal height: %w", err)
	}
	return latest, nil
}

func (s *ReorgStore) Get(height int64) (*reorgBlockMeta, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	value, err := s.db.Get([]byte(reorgBlockMetaKey(height)), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to load xmr reorg block meta at height %d: %w", height, err)
	}

	var meta reorgBlockMeta
	if err = json.Unmarshal(value, &meta); err != nil {
		return nil, fmt.Errorf("fail to decode xmr reorg block meta at height %d: %w", height, err)
	}
	return &meta, nil
}

func validateReorgBlockMeta(meta *reorgBlockMeta) error {
	if meta == nil {
		return fmt.Errorf("xmr reorg block meta is nil")
	}
	if meta.Height < 0 {
		return fmt.Errorf("xmr reorg block meta height must be >= 0")
	}
	if strings.TrimSpace(meta.BlockHash) == "" {
		return fmt.Errorf("xmr reorg block meta hash is empty at height %d", meta.Height)
	}
	customers := make(map[string]struct{}, len(meta.CustomerTransactions))
	for _, txID := range meta.CustomerTransactions {
		normalized := normalizeTxID(txID)
		if normalized == "" || txID != normalized {
			return fmt.Errorf("xmr block journal contains a non-normalized transaction at height %d", meta.Height)
		}
		customers[normalized] = struct{}{}
	}
	for _, txID := range meta.VaultOriginTransactions {
		normalized := normalizeTxID(txID)
		if normalized == "" || txID != normalized {
			return fmt.Errorf("xmr block journal contains a non-normalized vault transaction at height %d", meta.Height)
		}
		if _, ok := customers[normalized]; !ok {
			return fmt.Errorf("xmr vault-origin transaction is absent from block journal at height %d", meta.Height)
		}
	}
	return nil
}

func (s *ReorgStore) Set(meta *reorgBlockMeta) error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := validateReorgBlockMeta(meta); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("fail to encode xmr reorg block meta at height %d: %w", meta.Height, err)
	}
	if err = s.db.Put([]byte(reorgBlockMetaKey(meta.Height)), value, syncScannerWrite); err != nil {
		return fmt.Errorf("fail to save xmr reorg block meta at height %d: %w", meta.Height, err)
	}
	return nil
}

// SetBatch persists one canonical scan attempt atomically and synchronously.
// Observation receipts are written before this journal update, so a process
// death after the fork is hidden by the new journal still has a durable replay.
func (s *ReorgStore) SetBatch(metas []*reorgBlockMeta) error {
	if s == nil || s.db == nil || len(metas) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := new(leveldb.Batch)
	for _, meta := range metas {
		if err := validateReorgBlockMeta(meta); err != nil {
			return err
		}
		value, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("fail to encode xmr reorg block meta at height %d: %w", meta.Height, err)
		}
		batch.Put([]byte(reorgBlockMetaKey(meta.Height)), value)
	}
	if err := s.db.Write(batch, syncScannerWrite); err != nil {
		return fmt.Errorf("fail to save xmr reorg block meta batch: %w", err)
	}
	return nil
}

func encodeScannerObservationReceipt(
	meta *reorgBlockMeta,
	items []*stypes.TxInItem,
) ([]byte, error) {
	if err := validateReorgBlockMeta(meta); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	entry := scannerObservationReceipt{
		Height:       meta.Height,
		BlockHash:    strings.TrimSpace(meta.BlockHash),
		PreviousHash: strings.TrimSpace(meta.PreviousHash),
		Items:        items,
	}
	if !entry.valid() {
		return nil, fmt.Errorf("invalid xmr scanner observation receipt at height %d", meta.Height)
	}
	value, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode xmr scanner observation receipt at height %d: %w", meta.Height, err)
	}
	if len(value) > maxScannerObservationReceiptBytes {
		return nil, fmt.Errorf("xmr scanner observation receipt at height %d exceeds %d bytes", meta.Height, maxScannerObservationReceiptBytes)
	}
	return value, nil
}

func mergeScannerReceiptItems(existing, incoming []*stypes.TxInItem) []*stypes.TxInItem {
	merged := append([]*stypes.TxInItem(nil), existing...)
	for _, candidate := range incoming {
		duplicate := false
		for _, item := range merged {
			if item.Equals(candidate) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			merged = append(merged, candidate)
		}
	}
	return merged
}

func (s *ReorgStore) putScannerObservationReceipt(
	batch *leveldb.Batch,
	meta *reorgBlockMeta,
	items []*stypes.TxInItem,
) error {
	key := []byte(scannerObservationReceiptKey(meta.Height))
	current, getErr := s.db.Get(key, nil)
	if getErr != nil && !errors.Is(getErr, leveldb.ErrNotFound) {
		return fmt.Errorf("load existing xmr scanner observation receipt at height %d: %w", meta.Height, getErr)
	}
	if getErr == nil {
		var existing scannerObservationReceipt
		if err := json.Unmarshal(current, &existing); err != nil {
			return fmt.Errorf("decode existing xmr scanner observation receipt at height %d: %w", meta.Height, err)
		}
		if !existing.valid() {
			return fmt.Errorf("existing xmr scanner observation receipt at height %d is invalid", meta.Height)
		}
		if strings.EqualFold(existing.BlockHash, meta.BlockHash) &&
			strings.EqualFold(existing.PreviousHash, meta.PreviousHash) {
			// A later vault registration scans only the newly enrolled vaults.
			// Merge its same-canonical-block observations with prior vault results.
			items = mergeScannerReceiptItems(existing.Items, items)
		}
	}

	value, err := encodeScannerObservationReceipt(meta, items)
	if err != nil {
		return err
	}
	if len(value) == 0 {
		// A canonical replacement with no observations must supersede any
		// unacknowledged receipt from the orphaned block at this height.
		batch.Delete(key)
		return nil
	}
	batch.Put(key, value)
	return nil
}

// PutAuthenticatedObservation atomically binds a late source observation to
// its canonical journal, including old heights whose entries were pruned.
// The caller holds scanMu so canonical scanning cannot race the binding.
func (s *ReorgStore) PutAuthenticatedObservation(meta *reorgBlockMeta, item *stypes.TxInItem) error {
	if item == nil {
		return errors.New("authenticated xmr observation requires durable storage")
	}
	return s.putAuthenticatedOutbound(meta, item.Tx, item)
}

// PutAuthenticatedOutbound binds accounting-only evidence to the reorg
// journal without creating or acknowledging any observation receipt.
// The caller holds scanMu and has independently authenticated the spend.
func (s *ReorgStore) PutAuthenticatedOutbound(meta *reorgBlockMeta, txID string) error {
	return s.putAuthenticatedOutbound(meta, txID, nil)
}

func (s *ReorgStore) putAuthenticatedOutbound(meta *reorgBlockMeta, txID string, item *stypes.TxInItem) error {
	if s == nil || s.db == nil || meta == nil || normalizeTxID(txID) == "" {
		return errors.New("authenticated xmr outbound requires durable storage")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reverted, err := s.IsRevertedTx(txID)
	if err != nil {
		return err
	}
	if reverted {
		return errors.New("authenticated xmr observation is permanently reverted")
	}
	existing, err := s.Get(meta.Height)
	if err != nil {
		return err
	}
	if existing != nil {
		if !strings.EqualFold(existing.BlockHash, meta.BlockHash) || !strings.EqualFold(existing.PreviousHash, meta.PreviousHash) {
			return errors.New("authenticated xmr observation conflicts with canonical journal")
		}
		meta = existing
	}
	id := normalizeTxID(txID)
	if !slices.ContainsFunc(meta.CustomerTransactions, func(tx string) bool { return normalizeTxID(tx) == id }) {
		meta.CustomerTransactions = append(meta.CustomerTransactions, id)
	}
	addVaultOriginToMeta(meta, id)
	value, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	if item != nil {
		if err = s.putScannerObservationReceipt(batch, meta, []*stypes.TxInItem{item}); err != nil {
			return err
		}
	}
	batch.Put([]byte(reorgBlockMetaKey(meta.Height)), value)
	// A relocation watch may predate the authenticated provenance. Upgrade it
	// in the same commit so a crash cannot later classify this spend as a deposit.
	watchKey := []byte(scannerRelocationWatchPrefix + id)
	watchValue, watchErr := s.db.Get(watchKey, nil)
	switch {
	case watchErr == nil:
		var watch scannerRelocationWatch
		if err = json.Unmarshal(watchValue, &watch); err != nil || !watch.valid() {
			return errors.New("existing xmr scanner relocation watch is malformed")
		}
		watch.VaultOrigin = true
		watchValue, err = json.Marshal(watch)
		if err != nil {
			return err
		}
		batch.Put(watchKey, watchValue)
	case !errors.Is(watchErr, leveldb.ErrNotFound):
		return watchErr
	}
	return s.db.Write(batch, syncScannerWrite)
}

// PutObservationReceipt fsyncs sidecar observations before the scanner can
// commit a journal which would otherwise make them unreplayable after a crash.
func (s *ReorgStore) PutObservationReceipt(meta *reorgBlockMeta, items []*stypes.TxInItem) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := new(leveldb.Batch)
	if err := s.putScannerObservationReceipt(batch, meta, items); err != nil {
		return err
	}
	if err := s.db.Write(batch, syncScannerWrite); err != nil {
		return fmt.Errorf("save xmr scanner observation receipt at height %d: %w", meta.Height, err)
	}
	return nil
}

func (s *ReorgStore) GetCatchupMarker() (*scannerCatchupMarker, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	return s.getCatchupMarker()
}

func (s *ReorgStore) getCatchupMarker() (*scannerCatchupMarker, error) {
	value, err := s.db.Get([]byte(scannerCatchupMarkerDBKey), nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load xmr scanner catch-up marker: %w", err)
	}
	var marker scannerCatchupMarker
	if err = json.Unmarshal(value, &marker); err != nil {
		return nil, fmt.Errorf("decode xmr scanner catch-up marker: %w", err)
	}
	if !marker.valid() {
		return nil, fmt.Errorf("invalid xmr scanner catch-up marker")
	}
	return &marker, nil
}

// BeginCatchup writes the recovery anchor before the first sidecar advance.
func (s *ReorgStore) BeginCatchup(marker scannerCatchupMarker) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !marker.valid() || marker.completed() {
		return fmt.Errorf("invalid initial xmr scanner catch-up marker")
	}
	value, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode xmr scanner catch-up marker: %w", err)
	}
	if err = s.db.Put([]byte(scannerCatchupMarkerDBKey), value, syncScannerWrite); err != nil {
		return fmt.Errorf("save xmr scanner catch-up marker: %w", err)
	}
	return nil
}

// CheckpointCatchup atomically fsyncs one height's receipt and journal entry
// with the marker for the next height. Thus a lost sidecar response can rewind
// to the old marker, while a committed marker always has accumulated results.
func (s *ReorgStore) CheckpointCatchup(next scannerCatchupMarker, meta *reorgBlockMeta, items []*stypes.TxInItem) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateReorgBlockMeta(meta); err != nil {
		return err
	}
	if !next.valid() || next.AnchorHeight != meta.Height || next.StartHeight != meta.Height+1 ||
		!strings.EqualFold(next.AnchorHash, meta.BlockHash) {
		return fmt.Errorf("invalid xmr scanner catch-up checkpoint at height %d", meta.Height)
	}
	existing, err := s.Get(meta.Height)
	if err != nil {
		return err
	}
	if existing != nil {
		if err = validateReorgBlockMeta(existing); err != nil {
			return err
		}
		if existing.Height != meta.Height {
			return fmt.Errorf("xmr reorg journal height mismatch at height %d", meta.Height)
		}
		if strings.EqualFold(existing.BlockHash, meta.BlockHash) {
			if !strings.EqualFold(existing.PreviousHash, meta.PreviousHash) {
				return fmt.Errorf("xmr reorg journal parent mismatch at height %d", meta.Height)
			}
			// Registration catch-up returns only the newly enrolled vault's delta.
			// Preserve all prior evidence for this block, including on empty retries.
			merged := *meta
			merged.CustomerTransactions = mergeReorgJournalTxIDs(existing.CustomerTransactions, meta.CustomerTransactions)
			merged.VaultOriginTransactions = mergeReorgJournalTxIDs(existing.VaultOriginTransactions, meta.VaultOriginTransactions)
			// A partial delta cannot establish the provenance of older entries.
			merged.ProvenanceComplete = existing.ProvenanceComplete && meta.ProvenanceComplete
			meta = &merged
		}
	}
	markerValue, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode xmr scanner catch-up checkpoint: %w", err)
	}
	metaValue, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode xmr reorg block meta at height %d: %w", meta.Height, err)
	}
	batch := new(leveldb.Batch)
	if err = s.putScannerObservationReceipt(batch, meta, items); err != nil {
		return err
	}
	batch.Put([]byte(reorgBlockMetaKey(meta.Height)), metaValue)
	batch.Put([]byte(scannerCatchupMarkerDBKey), markerValue)
	if err = s.db.Write(batch, syncScannerWrite); err != nil {
		return fmt.Errorf("checkpoint xmr scanner catch-up at height %d: %w", meta.Height, err)
	}
	return nil
}

// mergeReorgJournalTxIDs unions already-validated normalized IDs without
// modifying either input. Sorted unique IDs make retries byte-stable.
func mergeReorgJournalTxIDs(existing, incoming []string) []string {
	merged := make([]string, 0, len(existing)+len(incoming))
	merged = append(merged, existing...)
	merged = append(merged, incoming...)
	slices.Sort(merged)
	return slices.Compact(merged)
}

func (s *ReorgStore) ListObservationReceipts() ([]scannerObservationReceiptClaim, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	iter := s.db.NewIterator(util.BytesPrefix([]byte(scannerObservationReceiptPrefix)), nil)
	defer iter.Release()
	claims := make([]scannerObservationReceiptClaim, 0)
	totalBytes := 0
	for iter.Next() {
		if len(claims) >= maxScannerObservationReceipts {
			return nil, fmt.Errorf("xmr scanner observation receipt count exceeds %d", maxScannerObservationReceipts)
		}
		totalBytes += len(iter.Value())
		if totalBytes > maxScannerObservationReceiptBytes {
			return nil, fmt.Errorf("xmr scanner observation receipts exceed %d bytes", maxScannerObservationReceiptBytes)
		}
		var entry scannerObservationReceipt
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			return nil, fmt.Errorf("decode xmr scanner observation receipt %q: %w", iter.Key(), err)
		}
		if !entry.valid() || string(iter.Key()) != scannerObservationReceiptKey(entry.Height) {
			return nil, fmt.Errorf("invalid xmr scanner observation receipt %q", iter.Key())
		}
		claims = append(claims, scannerObservationReceiptClaim{
			key:   bytes.Clone(iter.Key()),
			value: bytes.Clone(iter.Value()),
			entry: entry,
		})
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate xmr scanner observation receipts: %w", err)
	}
	return claims, nil
}

// AckObservationReceipts CAS-deletes only the exact receipts handed to
// Observer. A reorg replacement written while callbacks are pending survives.
// When completedMarker is non-nil it is cleared in the same synced batch.
func (s *ReorgStore) AckObservationReceipts(claims []scannerObservationReceiptClaim, completedMarker *scannerCatchupMarker) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := new(leveldb.Batch)
	for _, claim := range claims {
		current, err := s.db.Get(claim.key, nil)
		if err != nil {
			if errors.Is(err, leveldb.ErrNotFound) {
				continue
			}
			return fmt.Errorf("load xmr scanner receipt for acknowledgement: %w", err)
		}
		if !bytes.Equal(current, claim.value) {
			return fmt.Errorf("xmr scanner receipt changed before acknowledgement")
		}
		batch.Delete(claim.key)
	}
	if completedMarker != nil {
		if !completedMarker.completed() {
			return fmt.Errorf("cannot acknowledge incomplete xmr scanner catch-up marker")
		}
		current, err := s.getCatchupMarker()
		if err != nil {
			return err
		}
		if current == nil || !current.equal(*completedMarker) {
			return fmt.Errorf("xmr scanner catch-up marker changed before acknowledgement")
		}
		batch.Delete([]byte(scannerCatchupMarkerDBKey))
	}
	if err := s.db.Write(batch, syncScannerWrite); err != nil {
		return fmt.Errorf("acknowledge xmr scanner observations: %w", err)
	}
	return nil
}

func (s *ReorgStore) ClearCompletedCatchupMarker(marker scannerCatchupMarker) error {
	return s.AckObservationReceipts(nil, &marker)
}

func (s *ReorgStore) WatchRelocation(watch scannerRelocationWatch) error {
	if s == nil || s.db == nil {
		return errors.New("xmr reorg store is not initialized")
	}
	watch.TxID = normalizeTxID(watch.TxID)
	watch.BlockHash = strings.ToLower(strings.TrimSpace(watch.BlockHash))
	watch.PreviousHash = strings.ToLower(strings.TrimSpace(watch.PreviousHash))
	if !watch.valid() {
		return errors.New("invalid xmr scanner relocation watch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := []byte(scannerRelocationWatchPrefix + watch.TxID)
	existingValue, existingErr := s.db.Get(key, nil)
	switch {
	case errors.Is(existingErr, leveldb.ErrNotFound):
		iter := s.db.NewIterator(util.BytesPrefix([]byte(scannerRelocationWatchPrefix)), nil)
		ordinaryCount := 0
		for iter.Next() {
			var existing scannerRelocationWatch
			if decodeErr := json.Unmarshal(iter.Value(), &existing); decodeErr != nil || !existing.valid() {
				iter.Release()
				return errors.New("existing xmr scanner relocation watch is malformed")
			}
			if !existing.VaultOrigin {
				ordinaryCount++
			}
		}
		iterErr := iter.Error()
		iter.Release()
		if iterErr != nil {
			return iterErr
		}
		// Authenticated vault-origin holds are a solvency boundary and must never
		// fail merely because earlier permanent holds accumulated. Only ordinary
		// inbound/pool watches consume the bounded anti-DoS quota.
		if !watch.VaultOrigin && ordinaryCount >= maxOrdinaryScannerRelocationWatches {
			return fmt.Errorf("%w (%d)", errScannerRelocationWatchQuota, maxOrdinaryScannerRelocationWatches)
		}
	case existingErr != nil:
		return existingErr
	default:
		var existing scannerRelocationWatch
		if err := json.Unmarshal(existingValue, &existing); err != nil || !existing.valid() {
			return errors.New("existing xmr scanner relocation watch is malformed")
		}
		// Authenticated vault provenance is monotonic. A stale pool/location
		// reconciler may move the watch state, but can never downgrade a safety
		// hold into an ordinary inbound eligible for permanent errata.
		watch.VaultOrigin = watch.VaultOrigin || existing.VaultOrigin
	}
	value, err := json.Marshal(watch)
	if err != nil {
		return err
	}
	return s.db.Put(key, value, syncScannerWrite)
}

// QuarantineRevertedTxs is the local half of THORChain's permanent Reverted
// terminal. It is fsynced before ErrataBlock enters Observer so neither a
// process restart nor an authenticated internal replay can re-enable the exact
// non-expiring Monero transaction while the on-chain voter ignores its TxID.
func (s *ReorgStore) QuarantineRevertedTxs(height int64, txs []stypes.ErrataTx) error {
	if len(txs) == 0 {
		return nil
	}
	if s == nil || s.db == nil {
		return errors.New("xmr reorg store is not initialized")
	}
	if height < 0 {
		return errors.New("xmr reverted transaction height is invalid")
	}
	batch := new(leveldb.Batch)
	for _, tx := range txs {
		txID := normalizeTxID(tx.TxID.String())
		decoded, err := hex.DecodeString(txID)
		if err != nil || len(decoded) != 32 || tx.Chain != common.XMRChain {
			return fmt.Errorf("invalid xmr reverted transaction %q", tx.TxID)
		}
		batch.Put(
			[]byte(scannerRevertedTxPrefix+txID),
			[]byte(strconv.FormatInt(height, 10)),
		)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(batch, syncScannerWrite); err != nil {
		return fmt.Errorf("quarantine reverted xmr transactions: %w", err)
	}
	return nil
}

func (s *ReorgStore) IsRevertedTx(txID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	return isRevertedTxQuarantined(s.db, txID)
}

func isRevertedTxQuarantined(db *leveldb.DB, txID string) (bool, error) {
	if db == nil {
		return false, nil
	}
	txID = normalizeTxID(txID)
	decoded, err := hex.DecodeString(txID)
	if err != nil || len(decoded) != 32 {
		return false, errors.New("invalid xmr reverted transaction hash")
	}
	_, err = db.Get([]byte(scannerRevertedTxPrefix+txID), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load reverted xmr transaction quarantine: %w", err)
	}
	return true, nil
}

func (s *ReorgStore) ListRelocationWatches(limit int) ([]scannerRelocationWatch, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > maxOrdinaryScannerRelocationWatches {
		limit = maxOrdinaryScannerRelocationWatches
	}
	iter := s.db.NewIterator(util.BytesPrefix([]byte(scannerRelocationWatchPrefix)), nil)
	defer iter.Release()
	watches := make([]scannerRelocationWatch, 0, limit)
	for iter.Next() {
		var watch scannerRelocationWatch
		if err := json.Unmarshal(iter.Value(), &watch); err != nil || !watch.valid() {
			return nil, fmt.Errorf("invalid xmr scanner relocation watch %q", iter.Key())
		}
		watches = append(watches, watch)
		if len(watches) >= limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return watches, nil
}

// ListRelocationWatchesAfter returns a circular page after the last processed
// TxID. Scanner keeps the cursor in memory; resetting it after restart is safe,
// while long-lived pool/hold entries can no longer starve later watches.
func (s *ReorgStore) ListRelocationWatchesAfter(cursor string, limit int) ([]scannerRelocationWatch, error) {
	if s == nil || s.db == nil || limit <= 0 {
		return nil, nil
	}
	cursor = normalizeTxID(cursor)
	decode := func(value []byte) (scannerRelocationWatch, error) {
		var watch scannerRelocationWatch
		if err := json.Unmarshal(value, &watch); err != nil || !watch.valid() {
			return scannerRelocationWatch{}, errors.New("invalid xmr scanner relocation watch")
		}
		return watch, nil
	}
	watches := make([]scannerRelocationWatch, 0, limit)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(scannerRelocationWatchPrefix)), nil)
	for iter.Next() {
		watch, err := decode(iter.Value())
		if err != nil {
			iter.Release()
			return nil, err
		}
		if cursor != "" && normalizeTxID(watch.TxID) <= cursor {
			continue
		}
		watches = append(watches, watch)
		if len(watches) == limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		iter.Release()
		return nil, err
	}
	iter.Release()
	if len(watches) == limit || cursor == "" {
		return watches, nil
	}
	iter = s.db.NewIterator(util.BytesPrefix([]byte(scannerRelocationWatchPrefix)), nil)
	defer iter.Release()
	for iter.Next() {
		watch, err := decode(iter.Value())
		if err != nil {
			return nil, err
		}
		if normalizeTxID(watch.TxID) > cursor {
			break
		}
		watches = append(watches, watch)
		if len(watches) == limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return watches, nil
}

func (s *ReorgStore) DeleteRelocationWatch(txID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Delete([]byte(scannerRelocationWatchPrefix+normalizeTxID(txID)), syncScannerWrite)
}

// UpgradeRelocationWatchVaultOrigin applies late authenticated provenance
// directly to an existing watch. The bit is monotonic and synced so a restart
// between MarkSpent/replay authentication and the next poll cannot revert the
// transaction as an ordinary inbound.
func (s *ReorgStore) UpgradeRelocationWatchVaultOrigin(txID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	txID = normalizeTxID(txID)
	key := []byte(scannerRelocationWatchPrefix + txID)
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.db.Get(key, nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var watch scannerRelocationWatch
	if err = json.Unmarshal(value, &watch); err != nil || !watch.valid() {
		return false, errors.New("existing xmr scanner relocation watch is malformed")
	}
	if watch.VaultOrigin {
		return true, nil
	}
	watch.VaultOrigin = true
	value, err = json.Marshal(watch)
	if err != nil {
		return false, err
	}
	if err = s.db.Put(key, value, syncScannerWrite); err != nil {
		return false, fmt.Errorf("upgrade xmr relocation watch vault provenance: %w", err)
	}
	return true, nil
}

// BindRelocationWatch atomically carries the transaction into the processed
// canonical journal and clears its future-location watch.
func (s *ReorgStore) BindRelocationWatch(watch scannerRelocationWatch, meta *reorgBlockMeta) error {
	if s == nil || s.db == nil {
		return errors.New("xmr reorg store is not initialized")
	}
	if err := validateReorgBlockMeta(meta); err != nil {
		return err
	}
	if meta.Height != watch.LocationHeight || !strings.EqualFold(meta.BlockHash, watch.BlockHash) {
		return errors.New("xmr relocation watch does not match canonical journal")
	}
	if !slices.ContainsFunc(meta.CustomerTransactions, func(tx string) bool {
		return normalizeTxID(tx) == normalizeTxID(watch.TxID)
	}) {
		meta.CustomerTransactions = append(meta.CustomerTransactions, normalizeTxID(watch.TxID))
	}
	if watch.VaultOrigin && !slices.ContainsFunc(meta.VaultOriginTransactions, func(tx string) bool {
		return normalizeTxID(tx) == normalizeTxID(watch.TxID)
	}) {
		meta.VaultOriginTransactions = append(meta.VaultOriginTransactions, normalizeTxID(watch.TxID))
	}
	metaValue, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	watchKey := []byte(scannerRelocationWatchPrefix + normalizeTxID(watch.TxID))
	if _, err = s.db.Get(watchKey, nil); err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(reorgBlockMetaKey(meta.Height)), metaValue)
	batch.Delete(watchKey)
	return s.db.Write(batch, syncScannerWrite)
}

func (s *ReorgStore) Delete(height int64) error {
	if s == nil || s.db == nil || height < 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	protected, err := s.reorgMetaProtectedLocked(height)
	if err != nil {
		return err
	}
	if protected {
		return nil
	}
	if err := s.db.Delete([]byte(reorgBlockMetaKey(height)), syncScannerWrite); err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("fail to delete xmr reorg block meta at height %d: %w", height, err)
	}
	return nil
}

// OutstandingWorkHeights returns every height that still carries work which
// depends on canonical metadata: an unacknowledged observation receipt, the
// catch-up rewind anchor, or a relocation watch origin/location. These are
// exactly the heights PruneBefore and Delete refuse to remove metadata for, so
// a metadata hole at one of them cannot have been produced by pruning and
// indicates journal corruption rather than ordinary retention.
//
// Callers snapshot this before mutating metadata or writing receipts: the
// answer must describe pre-existing state, not work the current pass created.
func (s *ReorgStore) OutstandingWorkHeights() (map[int64]struct{}, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	heights, err := s.relocationWatchHeightsLocked()
	if err != nil {
		return nil, err
	}
	marker, err := s.getCatchupMarker()
	if err != nil {
		return nil, err
	}
	if marker != nil {
		heights[marker.AnchorHeight] = struct{}{}
	}
	iter := s.db.NewIterator(util.BytesPrefix([]byte(scannerObservationReceiptPrefix)), nil)
	defer iter.Release()
	for iter.Next() {
		var entry scannerObservationReceipt
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			return nil, fmt.Errorf("decode xmr scanner observation receipt %q: %w", iter.Key(), err)
		}
		heights[entry.Height] = struct{}{}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate xmr scanner observation receipts: %w", err)
	}
	return heights, nil
}

func (s *ReorgStore) reorgMetaProtectedLocked(height int64) (bool, error) {
	if _, err := s.db.Get([]byte(scannerObservationReceiptKey(height)), nil); err == nil {
		return true, nil
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return false, fmt.Errorf("check xmr scanner receipt before pruning height %d: %w", height, err)
	}
	marker, err := s.getCatchupMarker()
	if err != nil {
		return false, err
	}
	if marker != nil && marker.AnchorHeight == height {
		return true, nil
	}
	heights, err := s.relocationWatchHeightsLocked()
	if err != nil {
		return false, err
	}
	_, protected := heights[height]
	return protected, nil
}

func (s *ReorgStore) relocationWatchHeightsLocked() (map[int64]struct{}, error) {
	heights := make(map[int64]struct{})
	iter := s.db.NewIterator(util.BytesPrefix([]byte(scannerRelocationWatchPrefix)), nil)
	defer iter.Release()
	for iter.Next() {
		var watch scannerRelocationWatch
		if err := json.Unmarshal(iter.Value(), &watch); err != nil || !watch.valid() {
			return nil, errors.New("existing xmr scanner relocation watch is malformed")
		}
		heights[watch.OriginalHeight] = struct{}{}
		if watch.LocationHeight > 0 {
			heights[watch.LocationHeight] = struct{}{}
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return heights, nil
}

// PruneBefore streams removal of all obsolete journal entries below cutoff,
// while retaining any meta still needed by an unacknowledged receipt or the
// catch-up rewind anchor. Historical birthday catch-up can create far more
// than one old entry, so deleting only cutoff-1 would leak the remainder.
func (s *ReorgStore) PruneBefore(cutoff int64) (int, error) {
	if s == nil || s.db == nil || cutoff <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	marker, err := s.getCatchupMarker()
	if err != nil {
		return 0, err
	}
	watchHeights, err := s.relocationWatchHeightsLocked()
	if err != nil {
		return 0, err
	}
	iter := s.db.NewIterator(util.BytesPrefix([]byte(reorgBlockMetaPrefix)), nil)
	defer iter.Release()
	batch := new(leveldb.Batch)
	pruned := 0
	for iter.Next() {
		rawHeight := strings.TrimPrefix(string(iter.Key()), reorgBlockMetaPrefix)
		height, parseErr := strconv.ParseInt(rawHeight, 10, 64)
		if parseErr != nil {
			return pruned, fmt.Errorf("invalid xmr reorg journal key %q: %w", iter.Key(), parseErr)
		}
		_, watchProtected := watchHeights[height]
		if height >= cutoff || (marker != nil && marker.AnchorHeight == height) || watchProtected {
			continue
		}
		if _, receiptErr := s.db.Get([]byte(scannerObservationReceiptKey(height)), nil); receiptErr == nil {
			continue
		} else if !errors.Is(receiptErr, leveldb.ErrNotFound) {
			return pruned, receiptErr
		}
		batch.Delete(bytes.Clone(iter.Key()))
		pruned++
		if pruned >= maxReorgMetasPrunedPerFetch {
			break
		}
	}
	if err = iter.Error(); err != nil {
		return pruned, fmt.Errorf("iterate xmr reorg journal for pruning: %w", err)
	}
	if batch.Len() > 0 {
		if err = s.db.Write(batch, nil); err != nil {
			return 0, fmt.Errorf("prune xmr reorg journal: %w", err)
		}
	}
	return pruned, nil
}

func reorgBlockMetaKey(height int64) string {
	return fmt.Sprintf("%s%d", reorgBlockMetaPrefix, height)
}

func scannerObservationReceiptKey(height int64) string {
	return fmt.Sprintf("%s%020d", scannerObservationReceiptPrefix, height)
}
