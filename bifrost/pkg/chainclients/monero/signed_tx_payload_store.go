package monero

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	signedTxPayloadPrefix          = "xmr-signed-tx-payload-"
	signedTxPayloadTxPrefix        = "xmr-signed-tx-payload-by-hash-"
	signedTxPayloadClaimPrefix     = "xmr-signed-tx-claim-v2-"
	signedTxPayloadObservePrefix   = "xmr-signed-tx-observe-v2-"
	signedTxObserveEpochPrefix     = "xmr-signed-tx-observe-epoch-v1-"
	signedTxObserveEventPrefix     = "xmr-signed-tx-observe-event-v1-"
	signedTxCommittedLogicalPrefix = "xmr-signed-tx-committed-logical-v1-"
	signedTxCommittedTxPrefix      = "xmr-signed-tx-committed-hash-v1-"
	signedTxCommittedAliasPrefix   = "xmr-signed-tx-committed-alias-v1-"
	signedTxCommittedAliasTxPrefix = "xmr-signed-tx-committed-alias-hash-v1-"
	signedTxObservedLogicalPrefix  = "xmr-signed-tx-observed-logical-v1-"
	signedTxObservedTxPrefix       = "xmr-signed-tx-observed-hash-v1-"
	signedTxQuarantinedTxPrefix    = "xmr-signed-tx-quarantined-hash-v1-"
	signedTxQuarantinedLogicPrefix = "xmr-signed-tx-quarantined-logical-v1-"
	signedTxQuarantinedAliasPrefix = "xmr-signed-tx-quarantined-alias-v1-"
	signedTxBroadcastAttemptPrefix = "xmr-signed-tx-broadcast-attempt-v1-"

	signedTxClaimVersion  = uint8(2)
	signedTxClaimReserved = "reserved"
	signedTxClaimSigned   = "signed"
	signedTxClaimObserved = "observed"
)

var (
	signedTxPayloadWriteOptions      = &opt.WriteOptions{Sync: true}
	errXMRSigningClaimConflict       = errors.New("xmr signing claim conflicts with a durable attempt")
	errXMRSigningClaimCommitted      = errors.New("xmr signing claim has a committed unfinalized observation")
	errXMRSigningClaimAliasCommitted = errors.New("xmr signing claim alias has a different committed unfinalized observation")
	errXMRSigningClaimObserved       = errors.New("xmr signing claim is already observed")
	errXMRSigningClaimStale          = errors.New("xmr signing claim observation epoch changed")
	errXMRSigningClaimQuarantined    = errors.New("xmr signing claim is permanently quarantined after errata")
	errXMRBroadcastAlreadyInProgress = errors.New("xmr signed transaction broadcast is already in progress")
	errXMRBroadcastAttemptStale      = errors.New("xmr signed transaction broadcast attempt was superseded")
)

type signedTxPayloadStore struct {
	db                *leveldb.DB
	mu                sync.Mutex
	activeBroadcasts  map[string]string
	newBroadcastToken func() (string, error)
}

type signedTxPayloadLocation struct {
	PayloadKey  string `json:"payload_key"`
	LogicalHash string `json:"logical_hash"`
}

type signedTxPayloadClaimPlan struct {
	CacheVault   string
	CacheHash    string
	LogicalHash  string
	OwnerVault   string
	MsgID        string
	PlanHash     string
	ObserveKeys  []string
	ObserveEpoch map[string]uint64
	TxOutJSON    []byte
}

type signedTxPayloadClaim struct {
	Version        uint8             `json:"version"`
	State          string            `json:"state"`
	PayloadKey     string            `json:"payload_key"`
	CacheVault     string            `json:"cache_vault"`
	CacheHash      string            `json:"cache_hash"`
	LogicalHash    string            `json:"logical_hash"`
	OwnerVault     string            `json:"owner_vault"`
	MsgID          string            `json:"msg_id"`
	PlanHash       string            `json:"plan_hash"`
	ObserveKeys    []string          `json:"observe_keys"`
	ObserveEpoch   map[string]uint64 `json:"observe_epoch"`
	TxOutJSON      []byte            `json:"tx_out_json,omitempty"`
	SignedTxHash   string            `json:"signed_tx_hash,omitempty"`
	ObservedTxHash string            `json:"observed_tx_hash,omitempty"`
}

type signedTxObservationEpoch struct {
	Version uint8  `json:"version"`
	Epoch   uint64 `json:"epoch"`
}

type signedTxObservedLogical struct {
	Version uint8  `json:"version"`
	TxHash  string `json:"tx_hash"`
}

type signedTxBroadcastAttempt struct {
	Version     uint8  `json:"version"`
	Token       string `json:"token"`
	PayloadKey  string `json:"payload_key"`
	LogicalHash string `json:"logical_hash"`
	OwnerVault  string `json:"owner_vault"`
	TxHash      string `json:"tx_hash"`
	MsgID       string `json:"msg_id"`
	PlanHash    string `json:"plan_hash"`
}

func newSignedTxPayloadStore(db *leveldb.DB) *signedTxPayloadStore {
	return &signedTxPayloadStore{
		db:                db,
		activeBroadcasts:  make(map[string]string),
		newBroadcastToken: newSignedTxBroadcastToken,
	}
}

func newSignedTxBroadcastToken() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate xmr broadcast attempt token: %w", err)
	}
	return id.String(), nil
}

func signedTxPayloadKey(cacheVault, cacheHash string) string {
	return signedTxPayloadPrefix + strings.TrimSpace(cacheVault) + "|" + normalizeSignedTxClaimHash(cacheHash)
}

func signedTxPayloadTxKey(txHash string) string {
	return signedTxPayloadTxPrefix + normalizeSignedTxClaimHash(txHash)
}

func signedTxPayloadClaimKey(logicalHash string) string {
	return signedTxPayloadClaimPrefix + normalizeSignedTxClaimHash(logicalHash)
}

func signedTxPayloadObserveKey(observeKey string) string {
	return signedTxPayloadObservePrefix + normalizeSignedTxClaimHash(observeKey)
}

func signedTxObserveEpochKey(observeKey string) string {
	return signedTxObserveEpochPrefix + normalizeSignedTxClaimHash(observeKey)
}

func signedTxObserveEventKey(observeKey, txHash string) string {
	return signedTxObserveEventPrefix + normalizeSignedTxClaimHash(observeKey) + "|" + normalizeSignedTxClaimHash(txHash)
}

func signedTxCommittedLogicalKey(logicalHash string) string {
	return signedTxCommittedLogicalPrefix + normalizeSignedTxClaimHash(logicalHash)
}

func signedTxCommittedTxKey(txHash string) string {
	return signedTxCommittedTxPrefix + normalizeSignedTxClaimHash(txHash)
}

func signedTxCommittedAliasKey(observeKey string) string {
	return signedTxCommittedAliasPrefix + normalizeSignedTxClaimHash(observeKey)
}

func signedTxCommittedAliasTxKey(txHash, observeKey string) string {
	return signedTxCommittedAliasTxPrefix + normalizeSignedTxClaimHash(txHash) + "|" + normalizeSignedTxClaimHash(observeKey)
}

func signedTxCommittedAliasTxKeyPrefix(txHash string) string {
	return signedTxCommittedAliasTxPrefix + normalizeSignedTxClaimHash(txHash) + "|"
}

func signedTxObservedLogicalKey(logicalHash string) string {
	return signedTxObservedLogicalPrefix + normalizeSignedTxClaimHash(logicalHash)
}

func signedTxObservedTxKey(txHash string) string {
	return signedTxObservedTxPrefix + normalizeSignedTxClaimHash(txHash)
}

func signedTxQuarantinedTxKey(txHash string) string {
	return signedTxQuarantinedTxPrefix + normalizeSignedTxClaimHash(txHash)
}

func signedTxQuarantinedLogicalKey(logicalHash string) string {
	return signedTxQuarantinedLogicPrefix + normalizeSignedTxClaimHash(logicalHash)
}

func signedTxQuarantinedAliasKey(observeKey string) string {
	return signedTxQuarantinedAliasPrefix + normalizeSignedTxClaimHash(observeKey)
}

func signedTxBroadcastAttemptKey(logicalHash string) string {
	return signedTxBroadcastAttemptPrefix + normalizeSignedTxClaimHash(logicalHash)
}

func normalizeSignedTxClaimHash(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeObserveKeys(raw []string) ([]string, error) {
	keys := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, key := range raw {
		key = normalizeSignedTxClaimHash(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("xmr signed tx claim has no observation key")
	}
	sort.Strings(keys)
	return keys, nil
}

func normalizeSignedTxClaimPlan(plan signedTxPayloadClaimPlan) (signedTxPayloadClaimPlan, error) {
	plan.CacheVault = strings.TrimSpace(plan.CacheVault)
	plan.CacheHash = normalizeSignedTxClaimHash(plan.CacheHash)
	plan.LogicalHash = normalizeSignedTxClaimHash(plan.LogicalHash)
	plan.OwnerVault = strings.TrimSpace(plan.OwnerVault)
	plan.MsgID = normalizeSignedTxClaimHash(plan.MsgID)
	plan.PlanHash = normalizeSignedTxClaimHash(plan.PlanHash)
	if plan.CacheVault == "" || plan.CacheHash == "" || plan.LogicalHash == "" ||
		plan.OwnerVault == "" || plan.MsgID == "" || plan.PlanHash == "" || len(plan.TxOutJSON) == 0 {
		return signedTxPayloadClaimPlan{}, errors.New("xmr signed tx claim plan is incomplete")
	}

	keys, err := normalizeObserveKeys(plan.ObserveKeys)
	if err != nil {
		return signedTxPayloadClaimPlan{}, err
	}
	epochs := make(map[string]uint64, len(keys))
	for rawKey, epoch := range plan.ObserveEpoch {
		key := normalizeSignedTxClaimHash(rawKey)
		if key == "" {
			continue
		}
		if _, duplicate := epochs[key]; duplicate {
			return signedTxPayloadClaimPlan{}, errors.New("xmr signed tx claim has duplicate observation epochs")
		}
		epochs[key] = epoch
	}
	if len(epochs) != len(keys) {
		return signedTxPayloadClaimPlan{}, errors.New("xmr signed tx claim did not snapshot every observation epoch")
	}
	for _, key := range keys {
		if _, ok := epochs[key]; !ok {
			return signedTxPayloadClaimPlan{}, errors.New("xmr signed tx claim observation epoch is missing")
		}
	}
	plan.ObserveKeys = keys
	plan.ObserveEpoch = epochs
	return plan, nil
}

func cloneEpochs(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for key, epoch := range in {
		out[key] = epoch
	}
	return out
}

func claimFromPlan(plan signedTxPayloadClaimPlan) signedTxPayloadClaim {
	return signedTxPayloadClaim{
		Version:      signedTxClaimVersion,
		State:        signedTxClaimReserved,
		PayloadKey:   signedTxPayloadKey(plan.CacheVault, plan.CacheHash),
		CacheVault:   plan.CacheVault,
		CacheHash:    plan.CacheHash,
		LogicalHash:  plan.LogicalHash,
		OwnerVault:   plan.OwnerVault,
		MsgID:        plan.MsgID,
		PlanHash:     plan.PlanHash,
		ObserveKeys:  append([]string(nil), plan.ObserveKeys...),
		ObserveEpoch: cloneEpochs(plan.ObserveEpoch),
		TxOutJSON:    append([]byte(nil), plan.TxOutJSON...),
	}
}

func decodeSignedTxPayloadClaim(value []byte) (signedTxPayloadClaim, error) {
	var claim signedTxPayloadClaim
	if err := json.Unmarshal(value, &claim); err != nil {
		return signedTxPayloadClaim{}, fmt.Errorf("decode xmr signed tx claim: %w", err)
	}
	if claim.Version != signedTxClaimVersion {
		return signedTxPayloadClaim{}, fmt.Errorf("unsupported xmr signed tx claim version %d", claim.Version)
	}
	if claim.State != signedTxClaimReserved && claim.State != signedTxClaimSigned && claim.State != signedTxClaimObserved {
		return signedTxPayloadClaim{}, fmt.Errorf("unsupported xmr signed tx claim state %q", claim.State)
	}
	if !strings.HasPrefix(claim.PayloadKey, signedTxPayloadPrefix) || claim.CacheVault == "" ||
		claim.CacheHash == "" || claim.LogicalHash == "" || claim.OwnerVault == "" ||
		claim.MsgID == "" || claim.PlanHash == "" || len(claim.ObserveKeys) == 0 ||
		len(claim.ObserveEpoch) != len(claim.ObserveKeys) || len(claim.TxOutJSON) == 0 {
		return signedTxPayloadClaim{}, errors.New("xmr signed tx claim is malformed")
	}
	if claim.PayloadKey != signedTxPayloadKey(claim.CacheVault, claim.CacheHash) {
		return signedTxPayloadClaim{}, errors.New("xmr signed tx claim payload location is inconsistent")
	}
	if !sort.StringsAreSorted(claim.ObserveKeys) {
		return signedTxPayloadClaim{}, errors.New("xmr signed tx claim observation keys are not canonical")
	}
	for _, key := range claim.ObserveKeys {
		if key != normalizeSignedTxClaimHash(key) {
			return signedTxPayloadClaim{}, errors.New("xmr signed tx claim observation key is not canonical")
		}
		if _, ok := claim.ObserveEpoch[key]; !ok {
			return signedTxPayloadClaim{}, errors.New("xmr signed tx claim observation epoch is missing")
		}
	}
	claim.SignedTxHash = normalizeSignedTxClaimHash(claim.SignedTxHash)
	claim.ObservedTxHash = normalizeSignedTxClaimHash(claim.ObservedTxHash)
	if claim.State == signedTxClaimSigned && claim.SignedTxHash == "" {
		return signedTxPayloadClaim{}, errors.New("signed xmr claim has empty transaction hash")
	}
	if claim.State == signedTxClaimObserved && claim.ObservedTxHash == "" {
		return signedTxPayloadClaim{}, errors.New("observed xmr claim has empty transaction hash")
	}
	return claim, nil
}

func signedTxClaimSameOwnership(claim signedTxPayloadClaim, plan signedTxPayloadClaimPlan) bool {
	return claim.PayloadKey == signedTxPayloadKey(plan.CacheVault, plan.CacheHash) &&
		claim.CacheVault == plan.CacheVault &&
		claim.CacheHash == plan.CacheHash &&
		claim.LogicalHash == plan.LogicalHash &&
		strings.EqualFold(claim.OwnerVault, plan.OwnerVault) &&
		slices.Equal(claim.ObserveKeys, plan.ObserveKeys)
}

func signedTxClaimMatchesPlan(claim signedTxPayloadClaim, plan signedTxPayloadClaimPlan) bool {
	return signedTxClaimSameOwnership(claim, plan) &&
		strings.EqualFold(claim.MsgID, plan.MsgID) &&
		strings.EqualFold(claim.PlanHash, plan.PlanHash) &&
		mapsEqualEpochs(claim.ObserveEpoch, plan.ObserveEpoch) &&
		bytes.Equal(claim.TxOutJSON, plan.TxOutJSON)
}

func mapsEqualEpochs(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for key, epoch := range a {
		if b[key] != epoch {
			return false
		}
	}
	return true
}

func decodeObservationEpoch(value []byte) (uint64, error) {
	var state signedTxObservationEpoch
	if err := json.Unmarshal(value, &state); err != nil {
		return 0, fmt.Errorf("decode xmr observation epoch: %w", err)
	}
	if state.Version != 1 {
		return 0, fmt.Errorf("unsupported xmr observation epoch version %d", state.Version)
	}
	return state.Epoch, nil
}

func (s *signedTxPayloadStore) observationEpochLocked(observeKey string) (uint64, error) {
	value, err := s.db.Get([]byte(signedTxObserveEpochKey(observeKey)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get xmr observation epoch: %w", err)
	}
	return decodeObservationEpoch(value)
}

func (s *signedTxPayloadStore) observationEpochsMatchLocked(expected map[string]uint64) (bool, error) {
	for key, expectedEpoch := range expected {
		epoch, err := s.observationEpochLocked(key)
		if err != nil {
			return false, err
		}
		if epoch != expectedEpoch {
			return false, nil
		}
	}
	return true, nil
}

// SnapshotObserveEpochs captures the reusable observation aliases before the
// signing path's last chain/signer checks. Reserve compares the snapshot under
// the same mutex used by completion, closing the absent-event lost wakeup.
func (s *signedTxPayloadStore) SnapshotObserveEpochs(rawKeys []string) (map[string]uint64, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("xmr signed tx payload store is not initialized")
	}
	keys, err := normalizeObserveKeys(rawKeys)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	epochs := make(map[string]uint64, len(keys))
	for _, key := range keys {
		epoch, loadErr := s.observationEpochLocked(key)
		if loadErr != nil {
			return nil, loadErr
		}
		epochs[key] = epoch
	}
	return epochs, nil
}

// Reserve journals ownership before FROST. Once reserved, only the exact same
// plan may retry. A local failure cannot prove that another party did not
// finish and retain the original transaction, so changing the plan would risk
// authorizing two valid spends for one logical outbound.
func (s *signedTxPayloadStore) Reserve(rawPlan signedTxPayloadClaimPlan) error {
	if s == nil || s.db == nil {
		return errors.New("xmr signed tx payload store is not initialized")
	}
	plan, err := normalizeSignedTxClaimPlan(rawPlan)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if quarantined, quarantineErr := s.planQuarantinedLocked(plan); quarantineErr != nil {
		return quarantineErr
	} else if quarantined {
		return errXMRSigningClaimQuarantined
	}

	if _, err = s.db.Get([]byte(signedTxObservedLogicalKey(plan.LogicalHash)), nil); err == nil {
		return errXMRSigningClaimObserved
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("get observed xmr logical tombstone: %w", err)
	}
	if _, committed, committedErr := s.committedLogicalLocked(plan.LogicalHash); committedErr != nil {
		return committedErr
	} else if committed {
		return errXMRSigningClaimCommitted
	}
	for _, observeKey := range plan.ObserveKeys {
		if _, committed, committedErr := s.committedAliasLocked(observeKey); committedErr != nil {
			return committedErr
		} else if committed {
			return errXMRSigningClaimAliasCommitted
		}
	}

	claimKey := signedTxPayloadClaimKey(plan.LogicalHash)
	claimValue, err := s.db.Get([]byte(claimKey), nil)
	if err == nil {
		claim, decodeErr := decodeSignedTxPayloadClaim(claimValue)
		if decodeErr != nil {
			return decodeErr
		}
		if claim.State == signedTxClaimObserved {
			return errXMRSigningClaimObserved
		}
		if claim.State == signedTxClaimSigned {
			return fmt.Errorf("%w: logical outbound already has a signed payload", errXMRSigningClaimConflict)
		}
		if !signedTxClaimSameOwnership(claim, plan) {
			return fmt.Errorf("%w: logical outbound is owned by another vault or operation", errXMRSigningClaimConflict)
		}
		for _, observeKey := range claim.ObserveKeys {
			indexed, indexErr := s.db.Get([]byte(signedTxPayloadObserveKey(observeKey)), nil)
			if indexErr != nil || normalizeSignedTxClaimHash(string(indexed)) != claim.LogicalHash {
				return fmt.Errorf("%w: observation index is missing or inconsistent", errXMRSigningClaimConflict)
			}
		}
		fresh, epochErr := s.observationEpochsMatchLocked(plan.ObserveEpoch)
		if epochErr != nil {
			return epochErr
		}
		if !fresh {
			return errXMRSigningClaimStale
		}
		if !signedTxClaimMatchesPlan(claim, plan) {
			return fmt.Errorf("%w: local failure cannot replace the reserved FROST plan", errXMRSigningClaimConflict)
		}
		return nil
	}
	if !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("get xmr signed tx claim: %w", err)
	}

	fresh, err := s.observationEpochsMatchLocked(plan.ObserveEpoch)
	if err != nil {
		return err
	}
	if !fresh {
		return errXMRSigningClaimStale
	}
	for _, observeKey := range plan.ObserveKeys {
		indexed, indexErr := s.db.Get([]byte(signedTxPayloadObserveKey(observeKey)), nil)
		if indexErr == nil {
			return fmt.Errorf("%w: observation key already owns logical outbound %s", errXMRSigningClaimConflict, string(indexed))
		}
		if !errors.Is(indexErr, leveldb.ErrNotFound) {
			return fmt.Errorf("get xmr signed tx observation index: %w", indexErr)
		}
	}

	claim := claimFromPlan(plan)
	encodedClaim, err := json.Marshal(claim)
	if err != nil {
		return fmt.Errorf("encode reserved xmr signed tx claim: %w", err)
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(claimKey), encodedClaim)
	for _, observeKey := range plan.ObserveKeys {
		batch.Put([]byte(signedTxPayloadObserveKey(observeKey)), []byte(plan.LogicalHash))
	}
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return fmt.Errorf("reserve logical xmr outbound before FROST: %w", err)
	}
	return nil
}

// ReleaseReserved removes an exact reservation only when this node can prove
// it did not release a signature share. Signed and observed claims are never
// released here, and a stale or different plan cannot unlock the outbound.
func (s *signedTxPayloadStore) ReleaseReserved(rawPlan signedTxPayloadClaimPlan) error {
	if s == nil || s.db == nil {
		return errors.New("xmr signed tx payload store is not initialized")
	}
	plan, err := normalizeSignedTxClaimPlan(rawPlan)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if quarantined, quarantineErr := s.planQuarantinedLocked(plan); quarantineErr != nil {
		return quarantineErr
	} else if quarantined {
		return errXMRSigningClaimQuarantined
	}

	if _, err = s.db.Get([]byte(signedTxObservedLogicalKey(plan.LogicalHash)), nil); err == nil {
		return errXMRSigningClaimObserved
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("get observed xmr logical tombstone before release: %w", err)
	}
	if _, committed, committedErr := s.committedLogicalLocked(plan.LogicalHash); committedErr != nil {
		return committedErr
	} else if committed {
		return errXMRSigningClaimCommitted
	}
	for _, observeKey := range plan.ObserveKeys {
		if _, committed, committedErr := s.committedAliasLocked(observeKey); committedErr != nil {
			return committedErr
		} else if committed {
			return errXMRSigningClaimAliasCommitted
		}
	}
	fresh, err := s.observationEpochsMatchLocked(plan.ObserveEpoch)
	if err != nil {
		return err
	}
	if !fresh {
		return errXMRSigningClaimStale
	}

	claimKey := signedTxPayloadClaimKey(plan.LogicalHash)
	claimValue, err := s.db.Get([]byte(claimKey), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get reserved xmr signed tx claim for release: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(claimValue)
	if err != nil {
		return err
	}
	if claim.State != signedTxClaimReserved {
		return fmt.Errorf("%w: only a reserved xmr signing claim can be released", errXMRSigningClaimConflict)
	}
	if !signedTxClaimMatchesPlan(claim, plan) {
		return fmt.Errorf("%w: refusing to release a different xmr signing plan", errXMRSigningClaimConflict)
	}

	batch := new(leveldb.Batch)
	batch.Delete([]byte(claimKey))
	for _, observeKey := range claim.ObserveKeys {
		indexKey := []byte(signedTxPayloadObserveKey(observeKey))
		indexed, indexErr := s.db.Get(indexKey, nil)
		if indexErr == nil && normalizeSignedTxClaimHash(string(indexed)) == claim.LogicalHash {
			batch.Delete(indexKey)
			continue
		}
		if indexErr != nil && !errors.Is(indexErr, leveldb.ErrNotFound) {
			return fmt.Errorf("get xmr signed tx observation index for release: %w", indexErr)
		}
	}
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return fmt.Errorf("release reserved xmr signing claim: %w", err)
	}
	return nil
}

func decodeObservedLogical(value []byte) (signedTxObservedLogical, error) {
	var tombstone signedTxObservedLogical
	if err := json.Unmarshal(value, &tombstone); err != nil {
		return signedTxObservedLogical{}, fmt.Errorf("decode observed xmr logical tombstone: %w", err)
	}
	if tombstone.Version != 1 || normalizeSignedTxClaimHash(tombstone.TxHash) == "" {
		return signedTxObservedLogical{}, errors.New("observed xmr logical tombstone is malformed")
	}
	tombstone.TxHash = normalizeSignedTxClaimHash(tombstone.TxHash)
	return tombstone, nil
}

func decodeBroadcastAttempt(value []byte) (signedTxBroadcastAttempt, error) {
	var attempt signedTxBroadcastAttempt
	if err := json.Unmarshal(value, &attempt); err != nil {
		return signedTxBroadcastAttempt{}, fmt.Errorf("decode xmr broadcast attempt: %w", err)
	}
	attempt.Token = strings.TrimSpace(attempt.Token)
	attempt.LogicalHash = normalizeSignedTxClaimHash(attempt.LogicalHash)
	attempt.OwnerVault = strings.TrimSpace(attempt.OwnerVault)
	attempt.TxHash = normalizeSignedTxClaimHash(attempt.TxHash)
	attempt.MsgID = normalizeSignedTxClaimHash(attempt.MsgID)
	attempt.PlanHash = normalizeSignedTxClaimHash(attempt.PlanHash)
	if attempt.Version != 1 || attempt.Token == "" ||
		!strings.HasPrefix(attempt.PayloadKey, signedTxPayloadPrefix) ||
		attempt.LogicalHash == "" || attempt.OwnerVault == "" || attempt.TxHash == "" ||
		attempt.MsgID == "" || attempt.PlanHash == "" {
		return signedTxBroadcastAttempt{}, errors.New("xmr broadcast attempt is malformed")
	}
	return attempt, nil
}

func (s *signedTxPayloadStore) broadcastAttemptLocked(logicalHash string) (signedTxBroadcastAttempt, bool, error) {
	value, err := s.db.Get([]byte(signedTxBroadcastAttemptKey(logicalHash)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return signedTxBroadcastAttempt{}, false, nil
	}
	if err != nil {
		return signedTxBroadcastAttempt{}, false, fmt.Errorf("get xmr broadcast attempt: %w", err)
	}
	attempt, err := decodeBroadcastAttempt(value)
	if err != nil {
		return signedTxBroadcastAttempt{}, false, err
	}
	if attempt.LogicalHash != normalizeSignedTxClaimHash(logicalHash) {
		return signedTxBroadcastAttempt{}, false, errors.New("xmr broadcast attempt logical index is inconsistent")
	}
	return attempt, true, nil
}

func broadcastAttemptMatchesClaim(attempt signedTxBroadcastAttempt, claim signedTxPayloadClaim) bool {
	return attempt.PayloadKey == claim.PayloadKey &&
		attempt.LogicalHash == claim.LogicalHash &&
		strings.EqualFold(attempt.OwnerVault, claim.OwnerVault) &&
		attempt.TxHash == claim.SignedTxHash &&
		strings.EqualFold(attempt.MsgID, claim.MsgID) &&
		strings.EqualFold(attempt.PlanHash, claim.PlanHash)
}

func normalizeOptionalClaimHashes(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		value = normalizeSignedTxClaimHash(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values, nil
}

func (s *signedTxPayloadStore) quarantineValueLocked(key string) (string, bool, error) {
	value, err := s.db.Get([]byte(key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get xmr signing quarantine: %w", err)
	}
	txHash := normalizeSignedTxClaimHash(string(value))
	if txHash == "" {
		return "", false, errors.New("xmr signing quarantine is malformed")
	}
	return txHash, true, nil
}

func (s *signedTxPayloadStore) txQuarantinedLocked(txHash string) (bool, error) {
	_, found, err := s.quarantineValueLocked(signedTxQuarantinedTxKey(txHash))
	return found, err
}

func (s *signedTxPayloadStore) claimQuarantinedLocked(claim signedTxPayloadClaim) (bool, error) {
	if _, found, err := s.quarantineValueLocked(signedTxQuarantinedLogicalKey(claim.LogicalHash)); err != nil || found {
		return found, err
	}
	if claim.SignedTxHash != "" {
		if found, err := s.txQuarantinedLocked(claim.SignedTxHash); err != nil || found {
			return found, err
		}
	}
	if claim.ObservedTxHash != "" {
		if found, err := s.txQuarantinedLocked(claim.ObservedTxHash); err != nil || found {
			return found, err
		}
	}
	for _, alias := range claim.ObserveKeys {
		if _, found, err := s.quarantineValueLocked(signedTxQuarantinedAliasKey(alias)); err != nil || found {
			return found, err
		}
	}
	return false, nil
}

func (s *signedTxPayloadStore) planQuarantinedLocked(plan signedTxPayloadClaimPlan) (bool, error) {
	if _, found, err := s.quarantineValueLocked(signedTxQuarantinedLogicalKey(plan.LogicalHash)); err != nil || found {
		return found, err
	}
	for _, alias := range plan.ObserveKeys {
		if _, found, err := s.quarantineValueLocked(signedTxQuarantinedAliasKey(alias)); err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// BlockRevertedTxHash durably fences the exact artifact without a history scan.
// It may still wait behind existing store operations sharing the mutex.
func (s *signedTxPayloadStore) BlockRevertedTxHash(rawTxHash string) error {
	if s == nil || s.db == nil {
		return errors.New("xmr signed tx payload store is not initialized")
	}
	txHash := normalizeSignedTxClaimHash(rawTxHash)
	if txHash == "" {
		return errors.New("xmr reverted signing transaction hash is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Put([]byte(signedTxQuarantinedTxKey(txHash)), []byte(txHash), signedTxPayloadWriteOptions); err != nil {
		return fmt.Errorf("block reverted xmr signing transaction: %w", err)
	}
	return nil
}

// QuarantineReverted makes an errata'd transaction a one-way terminal state.
// The exact payload is deliberately retained as forensic evidence, but a
// synced batch blocks its transaction hash, logical owner, and every known
// observation alias before Observer can persist the permanent THOR voter
// transition. Scanning the bounded local indexes also covers a transaction
// committed immediately before the Observer/errata barrier.
func (s *signedTxPayloadStore) QuarantineReverted(rawTxHash string) error {
	if s == nil || s.db == nil {
		return errors.New("xmr signed tx payload store is not initialized")
	}
	txHash := normalizeSignedTxClaimHash(rawTxHash)
	if txHash == "" {
		return errors.New("xmr reverted signing transaction hash is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	logicalSet := make(map[string]struct{})
	aliasSet := make(map[string]struct{})
	if value, err := s.db.Get([]byte(signedTxPayloadTxKey(txHash)), nil); err == nil {
		var location signedTxPayloadLocation
		if decodeErr := json.Unmarshal(value, &location); decodeErr != nil {
			return fmt.Errorf("decode quarantined xmr payload location: %w", decodeErr)
		}
		if logicalHash := normalizeSignedTxClaimHash(location.LogicalHash); logicalHash != "" {
			logicalSet[logicalHash] = struct{}{}
		}
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("get quarantined xmr payload location: %w", err)
	}
	for _, indexKey := range []string{signedTxCommittedTxKey(txHash), signedTxObservedTxKey(txHash)} {
		if value, err := s.db.Get([]byte(indexKey), nil); err == nil {
			identity := normalizeSignedTxClaimHash(string(value))
			if identity != "" {
				// The observed reverse index may point to either a logical identity or
				// an unmatched alias. Blocking both namespaces is harmless and avoids
				// depending on mutable forward indexes during a crash race.
				logicalSet[identity] = struct{}{}
				aliasSet[identity] = struct{}{}
			}
		} else if !errors.Is(err, leveldb.ErrNotFound) {
			return fmt.Errorf("get quarantined xmr observation owner: %w", err)
		}
	}
	eventAliases, err := s.observationAliasesForTxLocked(txHash)
	if err != nil {
		return err
	}
	for _, alias := range eventAliases {
		aliasSet[alias] = struct{}{}
	}

	claimIter := s.db.NewIterator(util.BytesPrefix([]byte(signedTxPayloadClaimPrefix)), nil)
	for claimIter.Next() {
		claim, decodeErr := decodeSignedTxPayloadClaim(claimIter.Value())
		if decodeErr != nil {
			claimIter.Release()
			return decodeErr
		}
		if claim.SignedTxHash != txHash && claim.ObservedTxHash != txHash {
			continue
		}
		logicalSet[claim.LogicalHash] = struct{}{}
		for _, alias := range claim.ObserveKeys {
			aliasSet[alias] = struct{}{}
		}
	}
	if err = claimIter.Error(); err != nil {
		claimIter.Release()
		return fmt.Errorf("iterate xmr claims for quarantine: %w", err)
	}
	claimIter.Release()

	attemptIter := s.db.NewIterator(util.BytesPrefix([]byte(signedTxBroadcastAttemptPrefix)), nil)
	for attemptIter.Next() {
		attempt, decodeErr := decodeBroadcastAttempt(attemptIter.Value())
		if decodeErr != nil {
			attemptIter.Release()
			return decodeErr
		}
		if attempt.TxHash == txHash {
			logicalSet[attempt.LogicalHash] = struct{}{}
		}
	}
	if err = attemptIter.Error(); err != nil {
		attemptIter.Release()
		return fmt.Errorf("iterate xmr broadcast attempts for quarantine: %w", err)
	}
	attemptIter.Release()

	batch := new(leveldb.Batch)
	batch.Put([]byte(signedTxQuarantinedTxKey(txHash)), []byte(txHash))
	for logicalHash := range logicalSet {
		batch.Put([]byte(signedTxQuarantinedLogicalKey(logicalHash)), []byte(txHash))
	}
	for alias := range aliasSet {
		batch.Put([]byte(signedTxQuarantinedAliasKey(alias)), []byte(txHash))
	}
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return fmt.Errorf("quarantine reverted xmr signing claim: %w", err)
	}
	return nil
}

// IsLocallySignedTx is a read-only provenance check used before Scanner makes
// the irreversible errata decision. Payload and attempt indexes are both
// consulted so a restart or an in-flight broadcast cannot make locally signed,
// non-expiring bytes look like an ordinary inbound.
func (s *signedTxPayloadStore) IsLocallySignedTx(rawTxHash string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("xmr signed tx payload store is not initialized")
	}
	txHash := normalizeSignedTxClaimHash(rawTxHash)
	if txHash == "" {
		return false, errors.New("xmr signed transaction provenance hash is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if value, err := s.db.Get([]byte(signedTxPayloadTxKey(txHash)), nil); err == nil {
		var location signedTxPayloadLocation
		if decodeErr := json.Unmarshal(value, &location); decodeErr != nil ||
			normalizeSignedTxClaimHash(location.LogicalHash) == "" ||
			!strings.HasPrefix(location.PayloadKey, signedTxPayloadPrefix) {
			return false, errors.New("xmr signed payload provenance index is malformed")
		}
		return true, nil
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return false, fmt.Errorf("get xmr signed payload provenance index: %w", err)
	}

	claimIter := s.db.NewIterator(util.BytesPrefix([]byte(signedTxPayloadClaimPrefix)), nil)
	defer claimIter.Release()
	for claimIter.Next() {
		claim, err := decodeSignedTxPayloadClaim(claimIter.Value())
		if err != nil {
			return false, err
		}
		if claim.SignedTxHash == txHash || claim.ObservedTxHash == txHash {
			return true, nil
		}
	}
	if err := claimIter.Error(); err != nil {
		return false, fmt.Errorf("iterate xmr claims for transaction provenance: %w", err)
	}

	attemptIter := s.db.NewIterator(util.BytesPrefix([]byte(signedTxBroadcastAttemptPrefix)), nil)
	defer attemptIter.Release()
	for attemptIter.Next() {
		attempt, err := decodeBroadcastAttempt(attemptIter.Value())
		if err != nil {
			return false, err
		}
		if attempt.TxHash == txHash {
			return true, nil
		}
	}
	if err := attemptIter.Error(); err != nil {
		return false, fmt.Errorf("iterate xmr broadcast attempts for transaction provenance: %w", err)
	}
	return false, nil
}

func (s *signedTxPayloadStore) committedLogicalLocked(logicalHash string) (signedTxObservedLogical, bool, error) {
	value, err := s.db.Get([]byte(signedTxCommittedLogicalKey(logicalHash)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return signedTxObservedLogical{}, false, nil
	}
	if err != nil {
		return signedTxObservedLogical{}, false, fmt.Errorf("get committed-unfinalized xmr logical guard: %w", err)
	}
	committed, err := decodeObservedLogical(value)
	if err != nil {
		return signedTxObservedLogical{}, false, fmt.Errorf("decode committed-unfinalized xmr logical guard: %w", err)
	}
	return committed, true, nil
}

func (s *signedTxPayloadStore) putCommittedLogicalLocked(
	batch *leveldb.Batch,
	logicalHash,
	txHash string,
) error {
	committed, found, err := s.committedLogicalLocked(logicalHash)
	if err != nil {
		return err
	}
	if found {
		if committed.TxHash != txHash {
			return fmt.Errorf("%w: logical outbound has two committed-unfinalized transaction hashes", errXMRSigningClaimConflict)
		}
		return nil
	}
	encoded, err := json.Marshal(signedTxObservedLogical{Version: 1, TxHash: txHash})
	if err != nil {
		return fmt.Errorf("encode committed-unfinalized xmr logical guard: %w", err)
	}
	if indexed, indexErr := s.db.Get([]byte(signedTxCommittedTxKey(txHash)), nil); indexErr == nil {
		if normalizeSignedTxClaimHash(string(indexed)) != logicalHash {
			return fmt.Errorf("%w: committed-unfinalized transaction indexes another logical outbound", errXMRSigningClaimConflict)
		}
	} else if !errors.Is(indexErr, leveldb.ErrNotFound) {
		return fmt.Errorf("get committed-unfinalized xmr transaction index: %w", indexErr)
	}
	batch.Put([]byte(signedTxCommittedLogicalKey(logicalHash)), encoded)
	batch.Put([]byte(signedTxCommittedTxKey(txHash)), []byte(logicalHash))
	return nil
}

func (s *signedTxPayloadStore) clearCommittedLogicalLocked(
	batch *leveldb.Batch,
	logicalHash,
	txHash string,
) error {
	committed, found, err := s.committedLogicalLocked(logicalHash)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if committed.TxHash != txHash {
		return fmt.Errorf("%w: final xmr observation differs from committed-unfinalized transaction", errXMRSigningClaimConflict)
	}
	batch.Delete([]byte(signedTxCommittedLogicalKey(logicalHash)))
	batch.Delete([]byte(signedTxCommittedTxKey(txHash)))
	return nil
}

func (s *signedTxPayloadStore) committedAliasLocked(observeKey string) (signedTxObservedLogical, bool, error) {
	value, err := s.db.Get([]byte(signedTxCommittedAliasKey(observeKey)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return signedTxObservedLogical{}, false, nil
	}
	if err != nil {
		return signedTxObservedLogical{}, false, fmt.Errorf("get committed-unfinalized xmr alias guard: %w", err)
	}
	committed, err := decodeObservedLogical(value)
	if err != nil {
		return signedTxObservedLogical{}, false, fmt.Errorf("decode committed-unfinalized xmr alias guard: %w", err)
	}
	return committed, true, nil
}

func (s *signedTxPayloadStore) committedAliasForClaimLocked(claim signedTxPayloadClaim) (string, bool, error) {
	txHash := ""
	for _, observeKey := range claim.ObserveKeys {
		committed, found, err := s.committedAliasLocked(observeKey)
		if err != nil {
			return "", false, err
		}
		if !found {
			continue
		}
		if txHash != "" && txHash != committed.TxHash {
			return "", false, fmt.Errorf("%w: claim aliases select different committed-unfinalized transactions", errXMRSigningClaimConflict)
		}
		txHash = committed.TxHash
	}
	return txHash, txHash != "", nil
}

func (s *signedTxPayloadStore) putCommittedAliasLocked(
	batch *leveldb.Batch,
	observeKey,
	txHash string,
) error {
	committed, found, err := s.committedAliasLocked(observeKey)
	if err != nil {
		return err
	}
	if found {
		if committed.TxHash != txHash {
			return fmt.Errorf("%w: observation alias has two committed-unfinalized transaction hashes", errXMRSigningClaimConflict)
		}
		return nil
	}
	encoded, err := json.Marshal(signedTxObservedLogical{Version: 1, TxHash: txHash})
	if err != nil {
		return fmt.Errorf("encode committed-unfinalized xmr alias guard: %w", err)
	}
	batch.Put([]byte(signedTxCommittedAliasKey(observeKey)), encoded)
	batch.Put([]byte(signedTxCommittedAliasTxKey(txHash, observeKey)), []byte(observeKey))
	return nil
}

func (s *signedTxPayloadStore) clearCommittedAliasLocked(
	batch *leveldb.Batch,
	observeKey,
	txHash string,
) error {
	committed, found, err := s.committedAliasLocked(observeKey)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if committed.TxHash != txHash {
		return fmt.Errorf("%w: final xmr observation differs from committed-unfinalized alias transaction", errXMRSigningClaimConflict)
	}
	batch.Delete([]byte(signedTxCommittedAliasKey(observeKey)))
	batch.Delete([]byte(signedTxCommittedAliasTxKey(txHash, observeKey)))
	return nil
}

func (s *signedTxPayloadStore) clearCommittedAliasesForTxLocked(batch *leveldb.Batch, txHash string) ([]string, error) {
	prefix := signedTxCommittedAliasTxKeyPrefix(txHash)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(prefix)), nil)
	defer iter.Release()
	aliases := make([]string, 0)
	for iter.Next() {
		alias := normalizeSignedTxClaimHash(string(iter.Value()))
		if alias == "" {
			return nil, errors.New("committed-unfinalized xmr alias reverse index is malformed")
		}
		aliases = append(aliases, alias)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate committed-unfinalized xmr aliases: %w", err)
	}
	for _, alias := range aliases {
		committed, found, err := s.committedAliasLocked(alias)
		if err != nil {
			return nil, err
		}
		if !found || committed.TxHash != txHash {
			return nil, fmt.Errorf("%w: committed-unfinalized xmr alias indexes are inconsistent", errXMRSigningClaimConflict)
		}
		batch.Delete([]byte(signedTxCommittedAliasKey(alias)))
		batch.Delete([]byte(signedTxCommittedAliasTxKey(txHash, alias)))
	}
	return aliases, nil
}

func (s *signedTxPayloadStore) observationAliasesForTxLocked(txHash string) ([]string, error) {
	suffix := "|" + normalizeSignedTxClaimHash(txHash)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(signedTxObserveEventPrefix)), nil)
	defer iter.Release()
	aliases := make([]string, 0)
	for iter.Next() {
		key := string(iter.Key())
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		alias := strings.TrimSuffix(strings.TrimPrefix(key, signedTxObserveEventPrefix), suffix)
		alias = normalizeSignedTxClaimHash(alias)
		if alias != "" {
			aliases = append(aliases, alias)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate xmr observation aliases for errata: %w", err)
	}
	return normalizeOptionalClaimHashes(aliases)
}

func (s *signedTxPayloadStore) terminalizeClaimLocked(batch *leveldb.Batch, claim *signedTxPayloadClaim, observedTxHash string) error {
	if claim.State == signedTxClaimObserved {
		if strings.EqualFold(claim.ObservedTxHash, observedTxHash) {
			return nil
		}
		return fmt.Errorf("%w: logical outbound has two observed transaction hashes", errXMRSigningClaimConflict)
	}
	claim.State = signedTxClaimObserved
	claim.ObservedTxHash = observedTxHash
	encoded, err := json.Marshal(claim)
	if err != nil {
		return fmt.Errorf("encode observed xmr claim: %w", err)
	}
	batch.Put([]byte(signedTxPayloadClaimKey(claim.LogicalHash)), encoded)
	for _, alias := range claim.ObserveKeys {
		batch.Delete([]byte(signedTxPayloadObserveKey(alias)))
	}
	// Keep the exact signed artifact behind the final tombstone as forensic
	// evidence. Errata adds a permanent quarantine; it never re-authorizes these
	// bytes or a replacement signing ceremony.
	return nil
}

// Set commits the complete signed artifact only if the exact currently
// reserved plan and its observation snapshot remain current.
func (s *signedTxPayloadStore) Set(rawPlan signedTxPayloadClaimPlan, txHash string, payload []byte) error {
	if s == nil || s.db == nil {
		return errors.New("xmr signed tx payload store is not initialized")
	}
	plan, err := normalizeSignedTxClaimPlan(rawPlan)
	if err != nil {
		return err
	}
	txHash = normalizeSignedTxClaimHash(txHash)
	if txHash == "" || len(payload) == 0 {
		return errors.New("xmr signed tx payload or transaction hash is empty")
	}
	var header struct {
		TxHash string `json:"tx_hash"`
	}
	if err = json.Unmarshal(payload, &header); err != nil {
		return fmt.Errorf("decode xmr signed tx payload before storage: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(header.TxHash), txHash) {
		return errors.New("xmr signed tx payload transaction hash does not match index")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if quarantined, quarantineErr := s.planQuarantinedLocked(plan); quarantineErr != nil {
		return quarantineErr
	} else if quarantined {
		return errXMRSigningClaimQuarantined
	}
	if quarantined, quarantineErr := s.txQuarantinedLocked(txHash); quarantineErr != nil {
		return quarantineErr
	} else if quarantined {
		return errXMRSigningClaimQuarantined
	}
	if _, committed, committedErr := s.committedLogicalLocked(plan.LogicalHash); committedErr != nil {
		return committedErr
	} else if committed {
		return errXMRSigningClaimCommitted
	}
	claimKey := signedTxPayloadClaimKey(plan.LogicalHash)
	claimValue, err := s.db.Get([]byte(claimKey), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("%w: reserved claim disappeared before signed payload commit", errXMRSigningClaimConflict)
	}
	if err != nil {
		return fmt.Errorf("get reserved xmr signed tx claim: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(claimValue)
	if err != nil {
		return err
	}
	if committedTxHash, committed, committedErr := s.committedAliasForClaimLocked(claim); committedErr != nil {
		return committedErr
	} else if committed {
		if !strings.EqualFold(committedTxHash, txHash) {
			return errXMRSigningClaimAliasCommitted
		}
		return errXMRSigningClaimCommitted
	}
	if claim.State == signedTxClaimObserved {
		return errXMRSigningClaimObserved
	}
	if !signedTxClaimMatchesPlan(claim, plan) {
		return fmt.Errorf("%w: signed result does not match current reserved FROST plan", errXMRSigningClaimConflict)
	}
	if _, observedErr := s.db.Get([]byte(signedTxObservedTxKey(txHash)), nil); observedErr == nil {
		batch := new(leveldb.Batch)
		if indexErr := s.putObservedLogicalIndexLocked(batch, txHash, claim.LogicalHash); indexErr != nil {
			return indexErr
		}
		if termErr := s.terminalizeClaimLocked(batch, &claim, txHash); termErr != nil {
			return termErr
		}
		tombstone, _ := json.Marshal(signedTxObservedLogical{Version: 1, TxHash: txHash})
		batch.Put([]byte(signedTxObservedLogicalKey(claim.LogicalHash)), tombstone)
		if writeErr := s.db.Write(batch, signedTxPayloadWriteOptions); writeErr != nil {
			return fmt.Errorf("terminalize late observed xmr result: %w", writeErr)
		}
		return errXMRSigningClaimObserved
	} else if !errors.Is(observedErr, leveldb.ErrNotFound) {
		return fmt.Errorf("check observed xmr transaction hash: %w", observedErr)
	}

	payloadKey := signedTxPayloadKey(plan.CacheVault, plan.CacheHash)
	storedPayload, payloadErr := s.db.Get([]byte(payloadKey), nil)
	if claim.State == signedTxClaimSigned {
		if !strings.EqualFold(claim.SignedTxHash, txHash) || payloadErr != nil || !bytes.Equal(storedPayload, payload) {
			return fmt.Errorf("%w: completed claim differs from late signed result", errXMRSigningClaimConflict)
		}
		return nil
	}
	fresh, err := s.observationEpochsMatchLocked(claim.ObserveEpoch)
	if err != nil {
		return err
	}
	if !fresh {
		return errXMRSigningClaimStale
	}
	if payloadErr != nil && !errors.Is(payloadErr, leveldb.ErrNotFound) {
		return fmt.Errorf("get existing xmr signed tx payload: %w", payloadErr)
	}
	if payloadErr == nil && !bytes.Equal(storedPayload, payload) {
		return fmt.Errorf("%w: refuse to replace xmr signed tx payload", errXMRSigningClaimConflict)
	}

	txKey := signedTxPayloadTxKey(txHash)
	location := signedTxPayloadLocation{PayloadKey: payloadKey, LogicalHash: plan.LogicalHash}
	encodedLocation, err := json.Marshal(location)
	if err != nil {
		return fmt.Errorf("encode xmr signed tx payload location: %w", err)
	}
	existingLocation, locationErr := s.db.Get([]byte(txKey), nil)
	if locationErr == nil && !bytes.Equal(existingLocation, encodedLocation) {
		return fmt.Errorf("%w: transaction hash indexes another signed payload", errXMRSigningClaimConflict)
	}
	if locationErr != nil && !errors.Is(locationErr, leveldb.ErrNotFound) {
		return fmt.Errorf("get existing xmr signed tx payload index: %w", locationErr)
	}

	claim.State = signedTxClaimSigned
	claim.SignedTxHash = txHash
	encodedClaim, err := json.Marshal(claim)
	if err != nil {
		return fmt.Errorf("encode signed xmr claim: %w", err)
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(payloadKey), payload)
	batch.Put([]byte(txKey), encodedLocation)
	batch.Put([]byte(claimKey), encodedClaim)
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return fmt.Errorf("commit xmr signed tx payload: %w", err)
	}
	return nil
}

func (s *signedTxPayloadStore) GetOwned(cacheVault, cacheHash, logicalHash string) ([]byte, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, nil
	}
	cacheVault = strings.TrimSpace(cacheVault)
	cacheHash = normalizeSignedTxClaimHash(cacheHash)
	logicalHash = normalizeSignedTxClaimHash(logicalHash)
	if cacheVault == "" || cacheHash == "" || logicalHash == "" {
		return nil, false, errors.New("xmr signed tx payload recovery identity is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, quarantined, quarantineErr := s.quarantineValueLocked(signedTxQuarantinedLogicalKey(logicalHash)); quarantineErr != nil {
		return nil, false, quarantineErr
	} else if quarantined {
		return nil, false, errXMRSigningClaimQuarantined
	}

	if _, err := s.db.Get([]byte(signedTxObservedLogicalKey(logicalHash)), nil); err == nil {
		return nil, false, errXMRSigningClaimObserved
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return nil, false, fmt.Errorf("get observed xmr logical tombstone: %w", err)
	}
	if _, committed, committedErr := s.committedLogicalLocked(logicalHash); committedErr != nil {
		return nil, false, committedErr
	} else if committed {
		return nil, false, errXMRSigningClaimCommitted
	}
	claimValue, err := s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		if _, payloadErr := s.db.Get([]byte(signedTxPayloadKey(cacheVault, cacheHash)), nil); payloadErr == nil {
			return nil, false, fmt.Errorf("%w: unowned legacy payload requires operator recovery", errXMRSigningClaimConflict)
		} else if !errors.Is(payloadErr, leveldb.ErrNotFound) {
			return nil, false, fmt.Errorf("check unowned xmr signed payload: %w", payloadErr)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get xmr signed tx claim for recovery: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(claimValue)
	if err != nil {
		return nil, false, err
	}
	if quarantined, quarantineErr := s.claimQuarantinedLocked(claim); quarantineErr != nil {
		return nil, false, quarantineErr
	} else if quarantined {
		return nil, false, errXMRSigningClaimQuarantined
	}
	if committedTxHash, committed, committedErr := s.committedAliasForClaimLocked(claim); committedErr != nil {
		return nil, false, committedErr
	} else if committed {
		if claim.SignedTxHash != "" && !strings.EqualFold(claim.SignedTxHash, committedTxHash) {
			return nil, false, errXMRSigningClaimAliasCommitted
		}
		return nil, false, errXMRSigningClaimCommitted
	}
	if claim.PayloadKey != signedTxPayloadKey(cacheVault, cacheHash) {
		return nil, false, fmt.Errorf("%w: logical outbound belongs to another vault or operation", errXMRSigningClaimConflict)
	}
	switch claim.State {
	case signedTxClaimObserved:
		return nil, false, errXMRSigningClaimObserved
	case signedTxClaimReserved:
		return nil, false, nil
	}
	value, err := s.db.Get([]byte(claim.PayloadKey), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, false, fmt.Errorf("%w: signed claim points to a missing payload", errXMRSigningClaimConflict)
	}
	if err != nil {
		return nil, false, fmt.Errorf("get xmr signed tx payload: %w", err)
	}
	return append([]byte(nil), value...), true, nil
}

// ActiveClaim returns the one active operation selected by any supplied alias.
// Consolidation uses it to adopt and recover a pre-restart operation even when
// its newly generated SigningID differs.
func (s *signedTxPayloadStore) ActiveClaim(rawKeys []string, ownerVault string) (signedTxPayloadClaim, bool, error) {
	if s == nil || s.db == nil {
		return signedTxPayloadClaim{}, false, nil
	}
	keys, err := normalizeObserveKeys(rawKeys)
	if err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	ownerVault = strings.TrimSpace(ownerVault)
	if ownerVault == "" {
		return signedTxPayloadClaim{}, false, errors.New("active xmr claim owner is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, key := range keys {
		if _, quarantined, quarantineErr := s.quarantineValueLocked(signedTxQuarantinedAliasKey(key)); quarantineErr != nil {
			return signedTxPayloadClaim{}, false, quarantineErr
		} else if quarantined {
			return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
		}
		if _, committed, committedErr := s.committedAliasLocked(key); committedErr != nil {
			return signedTxPayloadClaim{}, false, committedErr
		} else if committed {
			return signedTxPayloadClaim{}, false, errXMRSigningClaimAliasCommitted
		}
	}
	logicalHash := ""
	for _, key := range keys {
		indexed, indexErr := s.db.Get([]byte(signedTxPayloadObserveKey(key)), nil)
		if errors.Is(indexErr, leveldb.ErrNotFound) {
			continue
		}
		if indexErr != nil {
			return signedTxPayloadClaim{}, false, fmt.Errorf("get active xmr claim alias: %w", indexErr)
		}
		candidate := normalizeSignedTxClaimHash(string(indexed))
		if logicalHash != "" && candidate != logicalHash {
			return signedTxPayloadClaim{}, false, fmt.Errorf("%w: aliases select different active claims", errXMRSigningClaimConflict)
		}
		logicalHash = candidate
	}
	if logicalHash == "" {
		return signedTxPayloadClaim{}, false, nil
	}
	if _, quarantined, quarantineErr := s.quarantineValueLocked(signedTxQuarantinedLogicalKey(logicalHash)); quarantineErr != nil {
		return signedTxPayloadClaim{}, false, quarantineErr
	} else if quarantined {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
	}
	if _, committed, committedErr := s.committedLogicalLocked(logicalHash); committedErr != nil {
		return signedTxPayloadClaim{}, false, committedErr
	} else if committed {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimCommitted
	}
	value, err := s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get active xmr claim: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(value)
	if err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if quarantined, quarantineErr := s.claimQuarantinedLocked(claim); quarantineErr != nil {
		return signedTxPayloadClaim{}, false, quarantineErr
	} else if quarantined {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
	}
	if claim.State == signedTxClaimObserved || !strings.EqualFold(claim.OwnerVault, ownerVault) {
		return signedTxPayloadClaim{}, false, fmt.Errorf("%w: active alias owner is inconsistent", errXMRSigningClaimConflict)
	}
	return claim, true, nil
}

// Claim returns the durable operation state without exposing mutable storage.
func (s *signedTxPayloadStore) Claim(logicalHash string) (signedTxPayloadClaim, bool, error) {
	if s == nil || s.db == nil {
		return signedTxPayloadClaim{}, false, nil
	}
	logicalHash = normalizeSignedTxClaimHash(logicalHash)
	if logicalHash == "" {
		return signedTxPayloadClaim{}, false, errors.New("xmr claim logical hash is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return signedTxPayloadClaim{}, false, nil
	}
	if err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr signing claim: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(value)
	if err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	claim.ObserveKeys = append([]string(nil), claim.ObserveKeys...)
	claim.ObserveEpoch = cloneEpochs(claim.ObserveEpoch)
	claim.TxOutJSON = append([]byte(nil), claim.TxOutJSON...)
	return claim, true, nil
}

func (s *signedTxPayloadStore) recordObservationLocked(batch *leveldb.Batch, observeKey, txHash string) (bool, error) {
	eventKey := signedTxObserveEventKey(observeKey, txHash)
	if _, err := s.db.Get([]byte(eventKey), nil); err == nil {
		return false, nil
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return false, fmt.Errorf("check xmr observation event: %w", err)
	}
	epoch, err := s.observationEpochLocked(observeKey)
	if err != nil {
		return false, err
	}
	if epoch == ^uint64(0) {
		return false, errors.New("xmr observation epoch overflow")
	}
	encoded, err := json.Marshal(signedTxObservationEpoch{Version: 1, Epoch: epoch + 1})
	if err != nil {
		return false, fmt.Errorf("encode xmr observation epoch: %w", err)
	}
	batch.Put([]byte(signedTxObserveEpochKey(observeKey)), encoded)
	batch.Put([]byte(eventKey), []byte(strconv.FormatUint(epoch+1, 10)))
	return true, nil
}

func (s *signedTxPayloadStore) putObservedLogicalLocked(batch *leveldb.Batch, logicalHash, txHash string) error {
	key := signedTxObservedLogicalKey(logicalHash)
	if value, err := s.db.Get([]byte(key), nil); err == nil {
		existing, decodeErr := decodeObservedLogical(value)
		if decodeErr != nil {
			return decodeErr
		}
		if existing.TxHash != txHash {
			return fmt.Errorf("%w: logical outbound has two observed transaction hashes", errXMRSigningClaimConflict)
		}
		return nil
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("get observed xmr logical tombstone: %w", err)
	}
	encoded, err := json.Marshal(signedTxObservedLogical{Version: 1, TxHash: txHash})
	if err != nil {
		return fmt.Errorf("encode observed xmr logical tombstone: %w", err)
	}
	batch.Put([]byte(key), encoded)
	return nil
}

// putObservedAliasIndexLocked records an unmatched/alias-only observation
// without replacing a logical owner already established for the transaction.
// A validated logical completion may canonicalize the reverse index later.
func (s *signedTxPayloadStore) putObservedAliasIndexLocked(batch *leveldb.Batch, txHash, observeKey string) error {
	key := signedTxObservedTxKey(txHash)
	if value, err := s.db.Get([]byte(key), nil); err == nil {
		if normalizeSignedTxClaimHash(string(value)) == "" {
			return errors.New("observed xmr transaction reverse index is malformed")
		}
		return nil
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return fmt.Errorf("get observed xmr transaction reverse index: %w", err)
	}
	batch.Put([]byte(key), []byte(observeKey))
	return nil
}

// putObservedLogicalIndexLocked canonicalizes an alias reverse owner to its
// validated logical claim. It must not replace a different logical tombstone:
// one Monero transaction cannot finalize two logical outbounds, and errata has
// only one reverse edge with which to reopen state.
func (s *signedTxPayloadStore) putObservedLogicalIndexLocked(batch *leveldb.Batch, txHash, logicalHash string) error {
	key := signedTxObservedTxKey(txHash)
	value, err := s.db.Get([]byte(key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		batch.Put([]byte(key), []byte(logicalHash))
		return nil
	}
	if err != nil {
		return fmt.Errorf("get observed xmr transaction reverse index: %w", err)
	}
	existingOwner := normalizeSignedTxClaimHash(string(value))
	if existingOwner == "" {
		return errors.New("observed xmr transaction reverse index is malformed")
	}
	if existingOwner == logicalHash {
		return nil
	}
	existingValue, existingErr := s.db.Get([]byte(signedTxObservedLogicalKey(existingOwner)), nil)
	if existingErr == nil {
		existing, decodeErr := decodeObservedLogical(existingValue)
		if decodeErr != nil {
			return decodeErr
		}
		if existing.TxHash != txHash {
			return fmt.Errorf("%w: observed transaction reverse owner is inconsistent", errXMRSigningClaimConflict)
		}
		return fmt.Errorf("%w: observed transaction already finalizes another logical outbound", errXMRSigningClaimConflict)
	}
	if !errors.Is(existingErr, leveldb.ErrNotFound) {
		return fmt.Errorf("get existing observed xmr logical owner: %w", existingErr)
	}
	// The existing owner is an alias-only observation, so a validated logical
	// completion may safely replace it.
	batch.Put([]byte(key), []byte(logicalHash))
	return nil
}

func (s *signedTxPayloadStore) guardBroadcastAttemptObservationLocked(
	batch *leveldb.Batch,
	logicalHash,
	observedTxHash string,
) error {
	attempt, found, err := s.broadcastAttemptLocked(logicalHash)
	if err != nil || !found {
		return err
	}
	if attempt.TxHash != observedTxHash {
		return fmt.Errorf("%w: a different transaction is already authorized for broadcast", errXMRSigningClaimConflict)
	}
	batch.Delete([]byte(signedTxBroadcastAttemptKey(logicalHash)))
	return nil
}

// CommitUnfinalizedLogical installs a reversible broadcast barrier as soon as
// THORChain commits an observation, while retaining the signed artifact until
// Monero finality. This closes the window in which a different locally signed
// transaction could be submitted for the same logical outbound.
func (s *signedTxPayloadStore) CommitUnfinalizedLogical(
	logicalHash string,
	observeKeys []string,
	observedTxHash string,
) (signedTxPayloadClaim, bool, error) {
	return s.recordUnfinalizedLogical(logicalHash, observeKeys, observedTxHash, false)
}

// ImportVerifiedLogical records visibility-only spent-ref evidence. The exact
// locally signed transaction remains recoverable and broadcastable; a different
// transaction, or an import before local signing, still bars another ceremony.
// Only an authoritative observation may advance the matching local claim.
func (s *signedTxPayloadStore) ImportVerifiedLogical(
	logicalHash string,
	observeKeys []string,
	observedTxHash string,
) (signedTxPayloadClaim, bool, error) {
	return s.recordUnfinalizedLogical(logicalHash, observeKeys, observedTxHash, true)
}

func (s *signedTxPayloadStore) recordUnfinalizedLogical(
	logicalHash string,
	observeKeys []string,
	observedTxHash string,
	visibilityOnly bool,
) (signedTxPayloadClaim, bool, error) {
	if s == nil || s.db == nil {
		return signedTxPayloadClaim{}, false, nil
	}
	logicalHash = normalizeSignedTxClaimHash(logicalHash)
	observedTxHash = normalizeSignedTxClaimHash(observedTxHash)
	keys, err := normalizeObserveKeys(observeKeys)
	if err != nil || logicalHash == "" || observedTxHash == "" {
		return signedTxPayloadClaim{}, false, errors.New("committed-unfinalized xmr logical observation identity is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if quarantined, quarantineErr := s.txQuarantinedLocked(observedTxHash); quarantineErr != nil {
		return signedTxPayloadClaim{}, false, quarantineErr
	} else if quarantined {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
	}

	if value, observedErr := s.db.Get([]byte(signedTxObservedLogicalKey(logicalHash)), nil); observedErr == nil {
		observed, decodeErr := decodeObservedLogical(value)
		if decodeErr != nil {
			return signedTxPayloadClaim{}, false, decodeErr
		}
		if observed.TxHash != observedTxHash {
			return signedTxPayloadClaim{}, false, fmt.Errorf("%w: logical outbound is already finalized by another transaction", errXMRSigningClaimConflict)
		}
		return signedTxPayloadClaim{Version: signedTxClaimVersion, State: signedTxClaimObserved, LogicalHash: logicalHash, ObservedTxHash: observedTxHash}, false, nil
	} else if !errors.Is(observedErr, leveldb.ErrNotFound) {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get finalized xmr logical tombstone: %w", observedErr)
	}

	claim := signedTxPayloadClaim{Version: signedTxClaimVersion, LogicalHash: logicalHash}
	value, claimErr := s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if claimErr == nil {
		claim, err = decodeSignedTxPayloadClaim(value)
		if err != nil {
			return signedTxPayloadClaim{}, false, err
		}
	} else if !errors.Is(claimErr, leveldb.ErrNotFound) {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr claim selected by committed-unfinalized observation: %w", claimErr)
	}
	if visibilityOnly {
		if preserved, preserveErr := s.preserveExactImportedClaimLocked(claim, observedTxHash); preserveErr != nil || preserved {
			return claim, preserved, preserveErr
		}
	}

	batch := new(leveldb.Batch)
	if err = s.guardBroadcastAttemptObservationLocked(batch, logicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	for _, key := range keys {
		if _, recordErr := s.recordObservationLocked(batch, key, observedTxHash); recordErr != nil {
			return signedTxPayloadClaim{}, false, recordErr
		}
	}
	if err = s.putCommittedLogicalLocked(batch, logicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}

	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("commit unfinalized xmr logical observation: %w", err)
	}
	return claim, true, nil
}

// CommitUnfinalizedExact applies the same reversible barrier to management or
// wire-only aliases, but only when the observed hash is the exact locally
// signed transaction. An ambiguous alias must never consume another operation.
func (s *signedTxPayloadStore) CommitUnfinalizedExact(
	observeKey,
	ownerVault,
	observedTxHash string,
) (signedTxPayloadClaim, bool, error) {
	return s.recordUnfinalizedExact(observeKey, ownerVault, observedTxHash, false)
}

// ImportVerifiedExact applies the same visibility-only rule to reusable
// management and wire-memoless aliases without releasing their active owner.
func (s *signedTxPayloadStore) ImportVerifiedExact(
	observeKey,
	ownerVault,
	observedTxHash string,
) (signedTxPayloadClaim, bool, error) {
	return s.recordUnfinalizedExact(observeKey, ownerVault, observedTxHash, true)
}

func (s *signedTxPayloadStore) recordUnfinalizedExact(
	observeKey,
	ownerVault,
	observedTxHash string,
	visibilityOnly bool,
) (signedTxPayloadClaim, bool, error) {
	if s == nil || s.db == nil {
		return signedTxPayloadClaim{}, false, nil
	}
	observeKey = normalizeSignedTxClaimHash(observeKey)
	ownerVault = strings.TrimSpace(ownerVault)
	observedTxHash = normalizeSignedTxClaimHash(observedTxHash)
	if observeKey == "" || ownerVault == "" || observedTxHash == "" {
		return signedTxPayloadClaim{}, false, errors.New("committed-unfinalized exact xmr observation identity is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if quarantined, quarantineErr := s.txQuarantinedLocked(observedTxHash); quarantineErr != nil {
		return signedTxPayloadClaim{}, false, quarantineErr
	} else if quarantined {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
	}
	if visibilityOnly {
		// Exact finalization releases reusable aliases. A delayed import of that
		// same observed transaction must not reacquire the alias and block the
		// next consolidation (or another legitimate wire-memoless outbound).
		if indexed, observedErr := s.db.Get([]byte(signedTxObservedTxKey(observedTxHash)), nil); observedErr == nil {
			if normalizeSignedTxClaimHash(string(indexed)) == "" {
				return signedTxPayloadClaim{}, false, errors.New("observed xmr transaction reverse index is malformed")
			}
			if _, eventErr := s.db.Get([]byte(signedTxObserveEventKey(observeKey, observedTxHash)), nil); eventErr == nil {
				return signedTxPayloadClaim{}, false, nil
			} else if !errors.Is(eventErr, leveldb.ErrNotFound) {
				return signedTxPayloadClaim{}, false, fmt.Errorf("get finalized xmr import alias event: %w", eventErr)
			}
		} else if !errors.Is(observedErr, leveldb.ErrNotFound) {
			return signedTxPayloadClaim{}, false, fmt.Errorf("get finalized xmr import transaction index: %w", observedErr)
		}
	}
	batch := new(leveldb.Batch)
	if _, err := s.recordObservationLocked(batch, observeKey, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err := s.putCommittedAliasLocked(batch, observeKey, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	indexed, err := s.db.Get([]byte(signedTxPayloadObserveKey(observeKey)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
			return signedTxPayloadClaim{}, false, fmt.Errorf("record unmatched committed-unfinalized xmr observation: %w", err)
		}
		return signedTxPayloadClaim{}, false, nil
	}
	if err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr claim alias for committed-unfinalized observation: %w", err)
	}
	value, err := s.db.Get([]byte(signedTxPayloadClaimKey(string(indexed))), nil)
	if err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr claim for committed-unfinalized observation: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(value)
	if err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if !strings.EqualFold(claim.OwnerVault, ownerVault) ||
		!slices.Contains(claim.ObserveKeys, observeKey) ||
		claim.SignedTxHash == "" ||
		!strings.EqualFold(claim.SignedTxHash, observedTxHash) {
		if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
			return signedTxPayloadClaim{}, false, fmt.Errorf("record ambiguous committed-unfinalized xmr observation: %w", err)
		}
		return claim, false, nil
	}
	if visibilityOnly {
		if preserved, preserveErr := s.preserveExactImportedClaimLocked(claim, observedTxHash); preserveErr != nil || preserved {
			return claim, preserved, preserveErr
		}
	}
	if err = s.guardBroadcastAttemptObservationLocked(batch, claim.LogicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.putCommittedLogicalLocked(batch, claim.LogicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("commit unfinalized exact xmr observation: %w", err)
	}
	return claim, true, nil
}

// The already-durable exact signed claim is sufficient duplicate-payment
// protection for a matching import. Check existing authoritative/conflicting
// barriers while holding the same mutex, but never remove or weaken them.
func (s *signedTxPayloadStore) preserveExactImportedClaimLocked(claim signedTxPayloadClaim, txHash string) (bool, error) {
	if claim.SignedTxHash == "" || !strings.EqualFold(claim.SignedTxHash, txHash) {
		return false, nil
	}
	if quarantined, err := s.claimQuarantinedLocked(claim); err != nil {
		return false, err
	} else if quarantined {
		return false, errXMRSigningClaimQuarantined
	}
	if _, err := s.broadcastClaimOutcomeLocked(claim, txHash); err != nil {
		return false, err
	}
	return true, nil
}

// CompleteObservedLogical terminalizes a vault-independent scheduled/migration
// operation. It writes the logical tombstone even when no local claim exists,
// so observation immediately before Reserve cannot be lost.
func (s *signedTxPayloadStore) CompleteObservedLogical(
	logicalHash string,
	observeKeys []string,
	observedTxHash string,
) (signedTxPayloadClaim, bool, error) {
	if s == nil || s.db == nil {
		return signedTxPayloadClaim{}, false, nil
	}
	logicalHash = normalizeSignedTxClaimHash(logicalHash)
	observedTxHash = normalizeSignedTxClaimHash(observedTxHash)
	keys, err := normalizeObserveKeys(observeKeys)
	if err != nil || logicalHash == "" || observedTxHash == "" {
		return signedTxPayloadClaim{}, false, errors.New("committed xmr logical observation identity is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if quarantined, quarantineErr := s.txQuarantinedLocked(observedTxHash); quarantineErr != nil {
		return signedTxPayloadClaim{}, false, quarantineErr
	} else if quarantined {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
	}
	batch := new(leveldb.Batch)
	if err = s.guardBroadcastAttemptObservationLocked(batch, logicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	for _, key := range keys {
		if _, recordErr := s.recordObservationLocked(batch, key, observedTxHash); recordErr != nil {
			return signedTxPayloadClaim{}, false, recordErr
		}
	}
	if err = s.clearCommittedLogicalLocked(batch, logicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if _, err = s.clearCommittedAliasesForTxLocked(batch, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.putObservedLogicalIndexLocked(batch, observedTxHash, logicalHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.putObservedLogicalLocked(batch, logicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}

	claim := signedTxPayloadClaim{Version: signedTxClaimVersion, State: signedTxClaimObserved, LogicalHash: logicalHash, ObservedTxHash: observedTxHash}
	value, getErr := s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if getErr == nil {
		claim, err = decodeSignedTxPayloadClaim(value)
		if err != nil {
			return signedTxPayloadClaim{}, false, err
		}
		if err = s.terminalizeClaimLocked(batch, &claim, observedTxHash); err != nil {
			return signedTxPayloadClaim{}, false, err
		}
	} else if !errors.Is(getErr, leveldb.ErrNotFound) {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr claim selected by logical observation: %w", getErr)
	}
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("complete logical xmr observation: %w", err)
	}
	return claim, true, nil
}

func (s *signedTxPayloadStore) completeObservedAlias(
	observeKey,
	ownerVault,
	observedTxHash string,
	requireExactHash bool,
) (signedTxPayloadClaim, bool, error) {
	if s == nil || s.db == nil {
		return signedTxPayloadClaim{}, false, nil
	}
	observeKey = normalizeSignedTxClaimHash(observeKey)
	ownerVault = strings.TrimSpace(ownerVault)
	observedTxHash = normalizeSignedTxClaimHash(observedTxHash)
	if observeKey == "" || ownerVault == "" || observedTxHash == "" {
		return signedTxPayloadClaim{}, false, errors.New("committed xmr observation identity is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if quarantined, quarantineErr := s.txQuarantinedLocked(observedTxHash); quarantineErr != nil {
		return signedTxPayloadClaim{}, false, quarantineErr
	} else if quarantined {
		return signedTxPayloadClaim{}, false, errXMRSigningClaimQuarantined
	}
	// A released reusable alias can now belong to a newer operation. Replaying
	// an old final event must not install a barrier against that successor. A
	// global final index alone is insufficient: this alias must have seen the
	// event, and any outstanding same-hash barrier still needs completion.
	replayedFinal := false
	if requireExactHash {
		if indexed, observedErr := s.db.Get([]byte(signedTxObservedTxKey(observedTxHash)), nil); observedErr == nil {
			if normalizeSignedTxClaimHash(string(indexed)) == "" {
				return signedTxPayloadClaim{}, false, errors.New("observed xmr transaction reverse index is malformed")
			}
			if _, eventErr := s.db.Get([]byte(signedTxObserveEventKey(observeKey, observedTxHash)), nil); eventErr == nil {
				committed, found, committedErr := s.committedAliasLocked(observeKey)
				if committedErr != nil {
					return signedTxPayloadClaim{}, false, committedErr
				}
				replayedFinal = !found || committed.TxHash != observedTxHash
			} else if !errors.Is(eventErr, leveldb.ErrNotFound) {
				return signedTxPayloadClaim{}, false, fmt.Errorf("get finalized xmr alias event: %w", eventErr)
			}
		} else if !errors.Is(observedErr, leveldb.ErrNotFound) {
			return signedTxPayloadClaim{}, false, fmt.Errorf("get finalized xmr transaction index: %w", observedErr)
		}
	}
	batch := new(leveldb.Batch)
	_, err := s.recordObservationLocked(batch, observeKey, observedTxHash)
	if err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.putObservedAliasIndexLocked(batch, observedTxHash, observeKey); err != nil {
		return signedTxPayloadClaim{}, false, err
	}

	indexed, indexErr := s.db.Get([]byte(signedTxPayloadObserveKey(observeKey)), nil)
	if errors.Is(indexErr, leveldb.ErrNotFound) {
		if replayedFinal {
			return signedTxPayloadClaim{}, false, nil
		}
		if err = s.clearCommittedAliasLocked(batch, observeKey, observedTxHash); err != nil {
			return signedTxPayloadClaim{}, false, err
		}
		if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
			return signedTxPayloadClaim{}, false, fmt.Errorf("record unmatched xmr observation: %w", err)
		}
		return signedTxPayloadClaim{}, false, nil
	}
	if indexErr != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr signed tx observation index: %w", indexErr)
	}
	value, err := s.db.Get([]byte(signedTxPayloadClaimKey(string(indexed))), nil)
	if err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("get xmr claim selected by observation: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(value)
	if err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if !strings.EqualFold(claim.OwnerVault, ownerVault) || !slices.Contains(claim.ObserveKeys, observeKey) {
		return signedTxPayloadClaim{}, false, fmt.Errorf("%w: committed observation does not match claim owner", errXMRSigningClaimConflict)
	}
	if requireExactHash && (claim.SignedTxHash == "" || !strings.EqualFold(claim.SignedTxHash, observedTxHash)) {
		if replayedFinal {
			return claim, false, nil
		}
		// A finalized wire transaction with the same reusable alias does not
		// prove it fulfilled this claim. Keep its alias barrier quarantining the
		// different local payload until semantic/logical resolution or errata.
		if err = s.putCommittedAliasLocked(batch, observeKey, observedTxHash); err != nil {
			return signedTxPayloadClaim{}, false, err
		}
		if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
			return signedTxPayloadClaim{}, false, fmt.Errorf("record ambiguous xmr observation: %w", err)
		}
		return claim, false, nil
	}
	if err = s.guardBroadcastAttemptObservationLocked(batch, claim.LogicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.clearCommittedAliasLocked(batch, observeKey, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.putObservedLogicalLocked(batch, claim.LogicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.clearCommittedLogicalLocked(batch, claim.LogicalHash, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	// Successful exact completion must index the claim's logical hash (rather
	// than its reusable alias) so a later authenticated errata can reopen it.
	if err = s.putObservedLogicalIndexLocked(batch, observedTxHash, claim.LogicalHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.terminalizeClaimLocked(batch, &claim, observedTxHash); err != nil {
		return signedTxPayloadClaim{}, false, err
	}
	if err = s.db.Write(batch, signedTxPayloadWriteOptions); err != nil {
		return signedTxPayloadClaim{}, false, fmt.Errorf("complete observed xmr signed tx claim: %w", err)
	}
	return claim, true, nil
}

// CompleteObserved preserves the legacy exact-owner alias behavior for unique
// management aliases. New scheduled outbounds should use logical completion.
func (s *signedTxPayloadStore) CompleteObserved(observeKey, ownerVault, observedTxHash string) (signedTxPayloadClaim, bool, error) {
	return s.completeObservedAlias(observeKey, ownerVault, observedTxHash, false)
}

// CompleteObservedExact records an ambiguous reusable/wire-only alias but
// terminalizes only when its transaction hash matches the locally signed one.
func (s *signedTxPayloadStore) CompleteObservedExact(observeKey, ownerVault, observedTxHash string) (signedTxPayloadClaim, bool, error) {
	return s.completeObservedAlias(observeKey, ownerVault, observedTxHash, true)
}

func signedTxBroadcastClaimMatches(
	claim signedTxPayloadClaim,
	payloadKey,
	ownerVault,
	txHash,
	msgID,
	planHash string,
) bool {
	return claim.PayloadKey == payloadKey &&
		strings.EqualFold(claim.OwnerVault, ownerVault) &&
		strings.EqualFold(claim.SignedTxHash, txHash) &&
		strings.EqualFold(claim.MsgID, msgID) &&
		strings.EqualFold(claim.PlanHash, planHash)
}

// broadcastClaimOutcomeLocked returns true when durable observation state
// already proves that the exact signed transaction was accepted. A different
// fulfillment always fails closed.
func (s *signedTxPayloadStore) broadcastClaimOutcomeLocked(claim signedTxPayloadClaim, txHash string) (bool, error) {
	if committedTxHash, committed, err := s.committedAliasForClaimLocked(claim); err != nil {
		return false, err
	} else if committed {
		if strings.EqualFold(committedTxHash, txHash) {
			return true, nil
		}
		return false, errXMRSigningClaimAliasCommitted
	}
	if value, err := s.db.Get([]byte(signedTxObservedLogicalKey(claim.LogicalHash)), nil); err == nil {
		tombstone, decodeErr := decodeObservedLogical(value)
		if decodeErr != nil {
			return false, decodeErr
		}
		if tombstone.TxHash == txHash {
			return true, nil
		}
		return false, fmt.Errorf("%w: a different transaction already fulfilled the logical outbound", errXMRSigningClaimObserved)
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return false, fmt.Errorf("get observed xmr logical tombstone: %w", err)
	}
	if committed, found, err := s.committedLogicalLocked(claim.LogicalHash); err != nil {
		return false, err
	} else if found {
		if committed.TxHash == txHash {
			return true, nil
		}
		return false, fmt.Errorf("%w: a different transaction is committed-unfinalized for the logical outbound", errXMRSigningClaimCommitted)
	}
	return false, nil
}

// WithBroadcastAuthorization persists an exact broadcast attempt before
// dispatch and releases the store mutex around the sidecar HTTP call. A retry
// may replace only the token for the same immutable claim and payload; stale
// callbacks cannot finalize a newer attempt. Successful or ambiguous dispatch
// retains the exact authorization until the matching transaction is committed
// or observed. Observation of the exact hash is idempotent success, while any
// differing fulfillment fails closed.
func (s *signedTxPayloadStore) WithBroadcastAuthorization(
	cacheVault,
	cacheHash,
	logicalHash,
	ownerVault,
	txHash,
	msgID,
	planHash string,
	broadcast func() (string, error),
) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("xmr signed tx payload store is not initialized")
	}
	cacheVault = strings.TrimSpace(cacheVault)
	cacheHash = normalizeSignedTxClaimHash(cacheHash)
	logicalHash = normalizeSignedTxClaimHash(logicalHash)
	ownerVault = strings.TrimSpace(ownerVault)
	txHash = normalizeSignedTxClaimHash(txHash)
	msgID = normalizeSignedTxClaimHash(msgID)
	planHash = normalizeSignedTxClaimHash(planHash)
	if cacheVault == "" || cacheHash == "" || logicalHash == "" || ownerVault == "" || txHash == "" || msgID == "" || planHash == "" || broadcast == nil {
		return "", errors.New("xmr broadcast authorization identity is incomplete")
	}
	payloadKey := signedTxPayloadKey(cacheVault, cacheHash)
	s.mu.Lock()
	if quarantined, quarantineErr := s.txQuarantinedLocked(txHash); quarantineErr != nil {
		s.mu.Unlock()
		return "", quarantineErr
	} else if quarantined {
		s.mu.Unlock()
		return "", errXMRSigningClaimQuarantined
	}
	if _, quarantined, quarantineErr := s.quarantineValueLocked(signedTxQuarantinedLogicalKey(logicalHash)); quarantineErr != nil {
		s.mu.Unlock()
		return "", quarantineErr
	} else if quarantined {
		s.mu.Unlock()
		return "", errXMRSigningClaimQuarantined
	}
	value, err := s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("get xmr claim for broadcast authorization: %w", err)
	}
	claim, err := decodeSignedTxPayloadClaim(value)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	if quarantined, quarantineErr := s.claimQuarantinedLocked(claim); quarantineErr != nil {
		s.mu.Unlock()
		return "", quarantineErr
	} else if quarantined {
		s.mu.Unlock()
		return "", errXMRSigningClaimQuarantined
	}
	if claim.LogicalHash != logicalHash || !signedTxBroadcastClaimMatches(claim, payloadKey, ownerVault, txHash, msgID, planHash) {
		s.mu.Unlock()
		return "", fmt.Errorf("%w: payload identity does not match the durable signing claim", errXMRSigningClaimConflict)
	}
	if completed, outcomeErr := s.broadcastClaimOutcomeLocked(claim, txHash); outcomeErr != nil {
		s.mu.Unlock()
		return "", outcomeErr
	} else if completed {
		if deleteErr := s.db.Delete([]byte(signedTxBroadcastAttemptKey(logicalHash)), signedTxPayloadWriteOptions); deleteErr != nil && !errors.Is(deleteErr, leveldb.ErrNotFound) {
			s.mu.Unlock()
			return "", fmt.Errorf("clear completed xmr broadcast attempt: %w", deleteErr)
		}
		s.mu.Unlock()
		return txHash, nil
	}
	if claim.State != signedTxClaimSigned {
		s.mu.Unlock()
		return "", fmt.Errorf("%w: payload is not the authorized signed transaction", errXMRSigningClaimConflict)
	}
	if attempt, found, attemptErr := s.broadcastAttemptLocked(logicalHash); attemptErr != nil {
		s.mu.Unlock()
		return "", attemptErr
	} else if found && !broadcastAttemptMatchesClaim(attempt, claim) {
		s.mu.Unlock()
		return "", fmt.Errorf("%w: persisted broadcast attempt differs from the signed claim", errXMRSigningClaimConflict)
	}
	if s.activeBroadcasts == nil {
		s.activeBroadcasts = make(map[string]string)
	}
	if s.activeBroadcasts[logicalHash] != "" {
		s.mu.Unlock()
		return "", errXMRBroadcastAlreadyInProgress
	}
	tokenSource := s.newBroadcastToken
	if tokenSource == nil {
		tokenSource = newSignedTxBroadcastToken
	}
	token, tokenErr := tokenSource()
	token = strings.TrimSpace(token)
	if tokenErr != nil || token == "" {
		s.mu.Unlock()
		if tokenErr != nil {
			return "", tokenErr
		}
		return "", errors.New("generated xmr broadcast attempt token is empty")
	}
	attempt := signedTxBroadcastAttempt{
		Version:     1,
		Token:       token,
		PayloadKey:  claim.PayloadKey,
		LogicalHash: claim.LogicalHash,
		OwnerVault:  claim.OwnerVault,
		TxHash:      claim.SignedTxHash,
		MsgID:       claim.MsgID,
		PlanHash:    claim.PlanHash,
	}
	encodedAttempt, err := json.Marshal(attempt)
	if err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("encode xmr broadcast attempt: %w", err)
	}
	if err = s.db.Put([]byte(signedTxBroadcastAttemptKey(logicalHash)), encodedAttempt, signedTxPayloadWriteOptions); err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("persist xmr broadcast attempt: %w", err)
	}
	s.activeBroadcasts[logicalHash] = token
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.activeBroadcasts[logicalHash] == token {
			delete(s.activeBroadcasts, logicalHash)
		}
		s.mu.Unlock()
	}()

	broadcastTxHash, broadcastErr := broadcast()
	broadcastTxHash = normalizeSignedTxClaimHash(broadcastTxHash)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Quarantine may race the sidecar HTTP call. The transaction may already
	// have reached a peer, but this callback must never report authorization or
	// make the exact payload recoverable again after the permanent errata.
	if quarantined, quarantineErr := s.txQuarantinedLocked(txHash); quarantineErr != nil {
		return "", quarantineErr
	} else if quarantined {
		return "", errXMRSigningClaimQuarantined
	}
	if _, quarantined, quarantineErr := s.quarantineValueLocked(signedTxQuarantinedLogicalKey(logicalHash)); quarantineErr != nil {
		return "", quarantineErr
	} else if quarantined {
		return "", errXMRSigningClaimQuarantined
	}
	value, err = s.db.Get([]byte(signedTxPayloadClaimKey(logicalHash)), nil)
	if err != nil {
		return "", fmt.Errorf("get xmr claim after broadcast: %w", err)
	}
	claim, err = decodeSignedTxPayloadClaim(value)
	if err != nil {
		return "", err
	}
	if claim.LogicalHash != logicalHash || !signedTxBroadcastClaimMatches(claim, payloadKey, ownerVault, txHash, msgID, planHash) {
		return "", fmt.Errorf("%w: durable signing claim changed during broadcast", errXMRSigningClaimConflict)
	}
	if completed, outcomeErr := s.broadcastClaimOutcomeLocked(claim, txHash); outcomeErr != nil {
		return "", outcomeErr
	} else if completed {
		if deleteErr := s.db.Delete([]byte(signedTxBroadcastAttemptKey(logicalHash)), signedTxPayloadWriteOptions); deleteErr != nil && !errors.Is(deleteErr, leveldb.ErrNotFound) {
			return "", fmt.Errorf("clear observed xmr broadcast attempt: %w", deleteErr)
		}
		return txHash, nil
	}
	persistedAttempt, found, err := s.broadcastAttemptLocked(logicalHash)
	if err != nil {
		return "", err
	}
	if !found || persistedAttempt.Token != token {
		return "", errXMRBroadcastAttemptStale
	}
	if !broadcastAttemptMatchesClaim(persistedAttempt, claim) {
		return "", fmt.Errorf("%w: broadcast attempt identity changed during dispatch", errXMRSigningClaimConflict)
	}
	if broadcastErr != nil {
		return "", broadcastErr
	}
	if broadcastTxHash != txHash {
		return "", fmt.Errorf("%w: broadcast callback returned a different transaction hash", errXMRSigningClaimConflict)
	}
	return txHash, nil
}

// Get is a test/diagnostic accessor for the vault-scoped payload bytes.
func (s *signedTxPayloadStore) Get(cacheVault, cacheHash string) ([]byte, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(cacheVault) == "" || strings.TrimSpace(cacheHash) == "" {
		return nil, false, errors.New("xmr signed tx payload cache identity is incomplete")
	}
	value, err := s.db.Get([]byte(signedTxPayloadKey(cacheVault, cacheHash)), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get xmr signed tx payload: %w", err)
	}
	return append([]byte(nil), value...), true, nil
}
