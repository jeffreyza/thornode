package monero

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

const (
	authenticatedInternalInboundReplayPrefix = "xmr-authenticated-internal-replay-v1-"
	maxAuthenticatedInternalInboundReplays   = 128
	authenticatedReplayCursorKey             = "xmr-authenticated-replay-cursor-v1"
	// The empty kind retains the existing migration record format. Payouts
	// share its capacity and leases but can only produce a source observation.
	authenticatedReplaySourcePayout = "source-payout"
	// Reconciliation performs a remote canonical-proof request per claim. Keep
	// the scan path bounded to one claim per block. Payouts additionally recheck
	// the location after MarkObserved; remaining work retries on later blocks.
	maxAuthenticatedInternalInboundClaims  = 1
	authenticatedInternalInboundClaimLease = 2 * time.Minute
)

var syncInternalInboundReplayWrite = &opt.WriteOptions{Sync: true}

// authenticatedInternalInboundReplay is created only after MarkSpent has
// authenticated an exact vault-origin transaction. Unlike the former raw
// pending-inbound design, scanner-controlled input can never create a record.
// The kind explicitly separates migration pairing from source-only payouts.
type authenticatedInternalInboundReplay struct {
	Kind                   string                          `json:"kind,omitempty"`
	TxID                   string                          `json:"tx_id"`
	TxKey                  string                          `json:"tx_key"`
	BlockHeight            int64                           `json:"block_height"`
	BlockHash              string                          `json:"block_hash"`
	PreviousHash           string                          `json:"previous_hash"`
	SourceAddress          string                          `json:"source_address"`
	SourceVaultPubKey      string                          `json:"source_vault_pub_key"`
	ToAddress              string                          `json:"to_address"`
	DestinationVaultPubKey string                          `json:"destination_vault_pub_key"`
	AmountThorchain        uint64                          `json:"amount_thorchain"`
	FeeThorchain           uint64                          `json:"fee_thorchain"`
	Memo                   string                          `json:"memo"`
	SpentOutputRefs        []authenticatedInternalSpentRef `json:"spent_output_refs"`
	ClaimToken             string                          `json:"claim_token,omitempty"`
	ClaimedAtUnix          int64                           `json:"claimed_at_unix,omitempty"`
}

type authenticatedInternalSpentRef struct {
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
	KeyImage    string `json:"key_image"`
}

func (r authenticatedInternalInboundReplay) key() string {
	return authenticatedInternalInboundReplayPrefix + normalizeTxID(r.TxID)
}

func (r authenticatedInternalInboundReplay) sameExact(other authenticatedInternalInboundReplay) bool {
	return r.Kind == other.Kind && normalizeTxID(r.TxID) == normalizeTxID(other.TxID) &&
		strings.EqualFold(strings.TrimSpace(r.TxKey), strings.TrimSpace(other.TxKey)) &&
		r.BlockHeight == other.BlockHeight &&
		strings.EqualFold(strings.TrimSpace(r.BlockHash), strings.TrimSpace(other.BlockHash)) &&
		strings.EqualFold(strings.TrimSpace(r.PreviousHash), strings.TrimSpace(other.PreviousHash)) &&
		common.AddressEquals(common.XMRChain, common.Address(r.SourceAddress), common.Address(other.SourceAddress)) &&
		strings.TrimSpace(r.SourceVaultPubKey) == strings.TrimSpace(other.SourceVaultPubKey) &&
		common.AddressEquals(common.XMRChain, common.Address(r.ToAddress), common.Address(other.ToAddress)) &&
		strings.TrimSpace(r.DestinationVaultPubKey) == strings.TrimSpace(other.DestinationVaultPubKey) &&
		r.AmountThorchain == other.AmountThorchain &&
		r.FeeThorchain == other.FeeThorchain &&
		strings.TrimSpace(r.Memo) == strings.TrimSpace(other.Memo) &&
		authenticatedInternalSpentRefsEqual(r.SpentOutputRefs, other.SpentOutputRefs)
}

func authenticatedInternalSpentRefsEqual(lhs, rhs []authenticatedInternalSpentRef) bool {
	if len(lhs) != len(rhs) {
		return false
	}
	canonical := func(refs []authenticatedInternalSpentRef) []authenticatedInternalSpentRef {
		out := append([]authenticatedInternalSpentRef(nil), refs...)
		for i := range out {
			out[i].TxHash = strings.ToLower(strings.TrimSpace(out[i].TxHash))
			out[i].KeyImage = strings.ToLower(strings.TrimSpace(out[i].KeyImage))
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].TxHash != out[j].TxHash {
				return out[i].TxHash < out[j].TxHash
			}
			return out[i].OutputIndex < out[j].OutputIndex
		})
		return out
	}
	lhs = canonical(lhs)
	rhs = canonical(rhs)
	for i := range lhs {
		if lhs[i] != rhs[i] {
			return false
		}
	}
	return true
}

type authenticatedInternalInboundClaim struct {
	Record authenticatedInternalInboundReplay
	Token  string
}

// authenticatedReplayCursor keeps retry order independent of claim leases and
// block timing. It survives acknowledgment, relocation and startup claim reset.
type authenticatedReplayCursor struct {
	BlockHeight int64  `json:"block_height"`
	TxID        string `json:"tx_id"`
}

type authenticatedInternalInboundReplayStore struct {
	db        *leveldb.DB
	mu        sync.Mutex
	nextClaim uint64
	now       func() time.Time
}

func newAuthenticatedInternalInboundReplayStore(db *leveldb.DB) *authenticatedInternalInboundReplayStore {
	return &authenticatedInternalInboundReplayStore{db: db, now: time.Now}
}

// ResetClaims makes records claimed by a process which crashed deliverable
// again. It must run before the scanner starts.
func (s *authenticatedInternalInboundReplayStore) ResetClaims() error {
	if s == nil || s.db == nil {
		return errors.New("authenticated internal inbound replay store is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.listLocked()
	if err != nil {
		return err
	}
	batch := new(leveldb.Batch)
	for _, record := range records {
		reverted, revertedErr := isRevertedTxQuarantined(s.db, record.TxID)
		if revertedErr != nil {
			return revertedErr
		}
		if reverted {
			batch.Delete([]byte(record.key()))
			continue
		}
		if record.ClaimToken == "" {
			continue
		}
		record.ClaimToken = ""
		record.ClaimedAtUnix = 0
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return fmt.Errorf("encode reset xmr internal replay %s: %w", record.TxID, marshalErr)
		}
		batch.Put([]byte(record.key()), encoded)
	}
	if batch.Len() == 0 {
		return nil
	}
	if err = s.db.Write(batch, syncInternalInboundReplayWrite); err != nil {
		return fmt.Errorf("reset xmr internal replay claims: %w", err)
	}
	return nil
}

// Enqueue durably records one already-authenticated, canonical transaction.
// An idempotent retry preserves an outstanding claim; conflicting identities
// for the same transaction are rejected.
func (s *authenticatedInternalInboundReplayStore) Enqueue(record authenticatedInternalInboundReplay) error {
	if s == nil || s.db == nil {
		return errors.New("authenticated internal inbound replay store is not initialized")
	}
	record.ClaimToken = ""
	record.ClaimedAtUnix = 0
	if err := validateAuthenticatedInternalInboundReplay(record); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reverted, err := isRevertedTxQuarantined(s.db, record.TxID)
	if err != nil {
		return err
	}
	if reverted {
		return fmt.Errorf("authenticated internal inbound replay %s is permanently reverted", record.TxID)
	}
	key := []byte(record.key())
	existing, err := s.getLocked(key)
	if err == nil {
		if !existing.sameExact(record) {
			return fmt.Errorf("authenticated internal inbound replay %s changed exact identity", record.TxID)
		}
		return nil
	}
	if !errors.Is(err, leveldb.ErrNotFound) {
		return err
	}
	records, err := s.listLocked()
	if err != nil {
		return err
	}
	if len(records) >= maxAuthenticatedInternalInboundReplays {
		return fmt.Errorf("authenticated internal inbound replay outbox is full (%d)", maxAuthenticatedInternalInboundReplays)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode authenticated internal inbound replay %s: %w", record.TxID, err)
	}
	if err = s.db.Put(key, encoded, syncInternalInboundReplayWrite); err != nil {
		return fmt.Errorf("persist authenticated internal inbound replay %s: %w", record.TxID, err)
	}
	return nil
}

// Claim marks at most limit records in-flight. A claim remains exclusive until
// the observer explicitly acknowledges or rejects it. Start after the previous
// attempt and wrap once, so a released or expired oldest claim cannot starve
// younger payouts or migration credits.
func (s *authenticatedInternalInboundReplayStore) Claim(limit int) ([]authenticatedInternalInboundClaim, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > maxAuthenticatedInternalInboundClaims {
		limit = maxAuthenticatedInternalInboundClaims
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	start, err := s.claimStartLocked(records)
	if err != nil {
		return nil, err
	}
	claims := make([]authenticatedInternalInboundClaim, 0, limit)
	batch := new(leveldb.Batch)
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	staleBefore := now.Add(-authenticatedInternalInboundClaimLease).Unix()
	for offset := range records {
		record := records[(start+offset)%len(records)]
		reverted, revertedErr := isRevertedTxQuarantined(s.db, record.TxID)
		if revertedErr != nil {
			return nil, revertedErr
		}
		if reverted {
			batch.Delete([]byte(record.key()))
			continue
		}
		if len(claims) >= limit {
			continue
		}
		if record.ClaimToken != "" && record.ClaimedAtUnix > staleBefore {
			continue
		}
		s.nextClaim++
		record.ClaimToken = strconv.FormatUint(s.nextClaim, 10)
		record.ClaimedAtUnix = now.Unix()
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode claimed xmr internal replay %s: %w", record.TxID, marshalErr)
		}
		batch.Put([]byte(record.key()), encoded)
		claims = append(claims, authenticatedInternalInboundClaim{Record: record, Token: record.ClaimToken})
	}
	if len(claims) > 0 {
		last := claims[len(claims)-1].Record
		cursor, cursorErr := json.Marshal(authenticatedReplayCursor{
			BlockHeight: last.BlockHeight, TxID: normalizeTxID(last.TxID),
		})
		if cursorErr != nil {
			return nil, fmt.Errorf("encode xmr authenticated replay cursor: %w", cursorErr)
		}
		// Advance retry position atomically with the claim, including when a
		// process stops before performing any remote work.
		batch.Put([]byte(authenticatedReplayCursorKey), cursor)
	}
	if batch.Len() > 0 {
		if err = s.db.Write(batch, syncInternalInboundReplayWrite); err != nil {
			return nil, fmt.Errorf("claim authenticated internal inbound replays: %w", err)
		}
	}
	return claims, nil
}

// claimStartLocked finds the first record after the persisted (height, txid)
// position. The referenced record need not still exist or have the same height.
func (s *authenticatedInternalInboundReplayStore) claimStartLocked(records []authenticatedInternalInboundReplay) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	value, err := s.db.Get([]byte(authenticatedReplayCursorKey), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read xmr authenticated replay cursor: %w", err)
	}
	var cursor authenticatedReplayCursor
	if err = json.Unmarshal(value, &cursor); err == nil {
		if cursor.BlockHeight <= 0 {
			err = errors.New("xmr authenticated replay cursor has invalid height")
		} else {
			err = validateAuthenticatedInternalFixedHex(cursor.TxID, 32, "xmr authenticated replay cursor tx hash")
		}
	}
	if err != nil {
		// This metadata only selects retry order. Validated jobs and live
		// leases stay authoritative; the next claim batch repairs the cursor.
		log.Warn().Err(err).Msg("restart xmr authenticated replay rotation after malformed cursor")
		return 0, nil
	}
	start := sort.Search(len(records), func(i int) bool {
		return records[i].BlockHeight > cursor.BlockHeight ||
			(records[i].BlockHeight == cursor.BlockHeight && normalizeTxID(records[i].TxID) > normalizeTxID(cursor.TxID))
	})
	return start % len(records), nil
}

func (s *authenticatedInternalInboundReplayStore) Release(claim authenticatedInternalInboundClaim) error {
	return s.finishClaim(claim, false)
}

func (s *authenticatedInternalInboundReplayStore) Acknowledge(claim authenticatedInternalInboundClaim) error {
	return s.finishClaim(claim, true)
}

// Relocate atomically rebinds an in-flight replay to the same transaction's
// newly verified canonical block while preserving its lease token. This keeps
// destination credit deliverable when Monero re-mines the transaction between
// enrollment and Observer handoff.
func (s *authenticatedInternalInboundReplayStore) Relocate(
	claim authenticatedInternalInboundClaim,
	height int64,
	blockHash, previousHash string,
) (authenticatedInternalInboundClaim, error) {
	if s == nil || s.db == nil {
		return authenticatedInternalInboundClaim{}, errors.New("authenticated internal inbound replay store is not initialized")
	}
	if claim.Token == "" {
		return authenticatedInternalInboundClaim{}, errors.New("xmr internal replay claim token is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := []byte(claim.Record.key())
	record, err := s.getLocked(key)
	if err != nil {
		return authenticatedInternalInboundClaim{}, err
	}
	reverted, err := isRevertedTxQuarantined(s.db, record.TxID)
	if err != nil {
		return authenticatedInternalInboundClaim{}, err
	}
	if reverted {
		return authenticatedInternalInboundClaim{}, errors.New("xmr internal replay is permanently reverted")
	}
	if record.ClaimToken != claim.Token {
		return authenticatedInternalInboundClaim{}, errors.New("xmr internal replay claim changed during relocation")
	}
	record.BlockHeight = height
	record.BlockHash = strings.ToLower(strings.TrimSpace(blockHash))
	record.PreviousHash = strings.ToLower(strings.TrimSpace(previousHash))
	if err = validateAuthenticatedInternalInboundReplay(record); err != nil {
		return authenticatedInternalInboundClaim{}, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return authenticatedInternalInboundClaim{}, fmt.Errorf("encode relocated xmr internal replay %s: %w", record.TxID, err)
	}
	if err = s.db.Put(key, encoded, syncInternalInboundReplayWrite); err != nil {
		return authenticatedInternalInboundClaim{}, fmt.Errorf("persist relocated xmr internal replay %s: %w", record.TxID, err)
	}
	return authenticatedInternalInboundClaim{Record: record, Token: claim.Token}, nil
}

// Invalidate removes a claimed record whose original canonical identity no
// longer exists. Token matching prevents an old callback deleting a new claim.
func (s *authenticatedInternalInboundReplayStore) Invalidate(claim authenticatedInternalInboundClaim) error {
	return s.finishClaim(claim, true)
}

func (s *authenticatedInternalInboundReplayStore) finishClaim(claim authenticatedInternalInboundClaim, remove bool) error {
	if s == nil || s.db == nil {
		return nil
	}
	if claim.Token == "" {
		return errors.New("xmr internal replay claim token is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := []byte(claim.Record.key())
	record, err := s.getLocked(key)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.ClaimToken != claim.Token {
		return nil
	}
	if remove {
		if err = s.db.Delete(key, syncInternalInboundReplayWrite); err != nil && !errors.Is(err, leveldb.ErrNotFound) {
			return fmt.Errorf("delete authenticated internal inbound replay %s: %w", record.TxID, err)
		}
		return nil
	}
	record.ClaimToken = ""
	record.ClaimedAtUnix = 0
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode released xmr internal replay %s: %w", record.TxID, err)
	}
	if err = s.db.Put(key, encoded, syncInternalInboundReplayWrite); err != nil {
		return fmt.Errorf("release authenticated internal inbound replay %s: %w", record.TxID, err)
	}
	return nil
}

func (s *authenticatedInternalInboundReplayStore) Count() (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.listLocked()
	return len(records), err
}

func (s *authenticatedInternalInboundReplayStore) getLocked(key []byte) (authenticatedInternalInboundReplay, error) {
	value, err := s.db.Get(key, nil)
	if err != nil {
		return authenticatedInternalInboundReplay{}, err
	}
	var record authenticatedInternalInboundReplay
	if err = json.Unmarshal(value, &record); err != nil {
		return authenticatedInternalInboundReplay{}, fmt.Errorf("decode authenticated internal inbound replay %q: %w", string(key), err)
	}
	return record, nil
}

func (s *authenticatedInternalInboundReplayStore) listLocked() ([]authenticatedInternalInboundReplay, error) {
	iter := s.db.NewIterator(util.BytesPrefix([]byte(authenticatedInternalInboundReplayPrefix)), nil)
	defer iter.Release()
	records := make([]authenticatedInternalInboundReplay, 0)
	for iter.Next() {
		var record authenticatedInternalInboundReplay
		if err := json.Unmarshal(iter.Value(), &record); err != nil {
			return nil, fmt.Errorf("decode authenticated internal inbound replay %q: %w", string(iter.Key()), err)
		}
		if err := validateAuthenticatedInternalInboundReplay(record); err != nil {
			return nil, fmt.Errorf("validate authenticated internal inbound replay %q: %w", string(iter.Key()), err)
		}
		if string(iter.Key()) != record.key() {
			return nil, fmt.Errorf("authenticated internal inbound replay key mismatch for %s", record.TxID)
		}
		records = append(records, record)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("list authenticated internal inbound replays: %w", err)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].BlockHeight != records[j].BlockHeight {
			return records[i].BlockHeight < records[j].BlockHeight
		}
		return records[i].key() < records[j].key()
	})
	return records, nil
}

func validateAuthenticatedInternalInboundReplay(record authenticatedInternalInboundReplay) error {
	if err := validateAuthenticatedInternalFixedHex(record.TxID, 32, "xmr internal replay tx hash"); err != nil {
		return err
	}
	if err := validateAuthenticatedInternalFixedHex(record.TxKey, 32, "xmr internal replay tx key"); err != nil {
		return err
	}
	if err := validateAuthenticatedInternalFixedHex(record.BlockHash, 32, "xmr internal replay block hash"); err != nil {
		return err
	}
	if err := validateAuthenticatedInternalFixedHex(record.PreviousHash, 32, "xmr internal replay previous block hash"); err != nil {
		return err
	}
	if record.BlockHeight <= 0 {
		return fmt.Errorf("xmr internal replay %s has invalid block height", record.TxID)
	}
	if record.AmountThorchain == 0 {
		return fmt.Errorf("xmr internal replay %s has zero amount", record.TxID)
	}
	// common.Tx cannot represent a non-zero atomic fee below one THOR unit: its
	// Gas coin must itself be non-zero. Preserve that protocol boundary here.
	if record.FeeThorchain == 0 {
		return fmt.Errorf("xmr internal replay %s has unrepresentable zero fee", record.TxID)
	}
	if len(record.SpentOutputRefs) == 0 || len(record.SpentOutputRefs) > common.MaxXMRSpentOutputRefs {
		return fmt.Errorf("xmr internal replay %s has invalid spent-ref count", record.TxID)
	}
	seenRefs := make(map[string]struct{}, len(record.SpentOutputRefs))
	seenKeyImages := make(map[string]struct{}, len(record.SpentOutputRefs))
	for _, ref := range record.SpentOutputRefs {
		if err := validateAuthenticatedInternalFixedHex(ref.TxHash, 32, "xmr internal replay spent-ref tx hash"); err != nil {
			return err
		}
		if err := validateAuthenticatedInternalFixedHex(ref.KeyImage, 32, "xmr internal replay spent-ref key image"); err != nil {
			return err
		}
		refKey := strings.ToLower(strings.TrimSpace(ref.TxHash)) + ":" + strconv.FormatUint(uint64(ref.OutputIndex), 10)
		if _, exists := seenRefs[refKey]; exists {
			return fmt.Errorf("xmr internal replay %s has duplicate spent ref", record.TxID)
		}
		seenRefs[refKey] = struct{}{}
		keyImage := strings.ToLower(strings.TrimSpace(ref.KeyImage))
		if _, exists := seenKeyImages[keyImage]; exists {
			return fmt.Errorf("xmr internal replay %s has duplicate spent-ref key image", record.TxID)
		}
		seenKeyImages[keyImage] = struct{}{}
	}
	if record.ClaimedAtUnix < 0 || (record.ClaimToken == "") != (record.ClaimedAtUnix == 0) {
		return fmt.Errorf("xmr internal replay %s has inconsistent claim lease", record.TxID)
	}
	if len(record.Memo) > constants.MaxMemoSize {
		return fmt.Errorf("xmr authenticated replay %s memo is too long", record.TxID)
	}
	switch record.Kind {
	case "":
		if !isXMRTxMigrateMemo(record.Memo) {
			return fmt.Errorf("xmr internal replay %s has invalid migration memo", record.TxID)
		}
	case authenticatedReplaySourcePayout:
		parsed, err := memo.ParseMemo(common.LatestVersion, record.Memo)
		if err != nil || !parsed.IsOutbound() || record.DestinationVaultPubKey != "" ||
			!common.XMRChain.IsValidAddress(common.Address(record.ToAddress)) {
			return fmt.Errorf("xmr source payout replay %s has invalid payment identity", record.TxID)
		}
	default:
		return fmt.Errorf("xmr authenticated replay %s has unknown kind %q", record.TxID, record.Kind)
	}
	sourceVault, err := common.NewPubKey(strings.TrimSpace(record.SourceVaultPubKey))
	if err != nil || sourceVault.IsEmpty() {
		return fmt.Errorf("xmr internal replay %s has invalid source vault: %w", record.TxID, err)
	}
	source, err := sourceVault.GetAddress(common.XMRChain)
	if err != nil || source.IsEmpty() || !common.AddressEquals(common.XMRChain, source, common.Address(record.SourceAddress)) {
		return fmt.Errorf("xmr internal replay %s source address does not match source vault", record.TxID)
	}
	if record.Kind == authenticatedReplaySourcePayout {
		return nil
	}
	destinationVault, err := common.NewPubKey(strings.TrimSpace(record.DestinationVaultPubKey))
	if err != nil || destinationVault.IsEmpty() {
		return fmt.Errorf("xmr internal replay %s has invalid destination vault: %w", record.TxID, err)
	}
	destination, err := destinationVault.GetAddress(common.XMRChain)
	if err != nil || destination.IsEmpty() || !common.AddressEquals(common.XMRChain, destination, common.Address(record.ToAddress)) {
		return fmt.Errorf("xmr internal replay %s destination address does not match destination vault", record.TxID)
	}
	if sourceVault.Equals(destinationVault) {
		return fmt.Errorf("xmr internal replay %s source and destination vaults are identical", record.TxID)
	}
	return nil
}

func validateAuthenticatedInternalFixedHex(value string, byteLen int, field string) error {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != byteLen*2 {
		return fmt.Errorf("%s must be %d-byte hex", field, byteLen)
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) != byteLen {
		return fmt.Errorf("%s must be %d-byte hex", field, byteLen)
	}
	return nil
}
