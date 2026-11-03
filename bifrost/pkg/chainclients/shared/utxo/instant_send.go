package utxo

import (
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	instantSendStateKey          = "instant-send-state"
	instantSendReservationPrefix = "instant-send-reservation-"
	InstantSendObservation       = "observation"
	InstantSendSnapshot          = "snapshot"
)

// InstantSendReservation remains charged until its transaction is ChainLocked.
// Snapshot entries do not imply that this node has observed the transaction.
type InstantSendReservation struct {
	TxID      string `json:"tx_id"`
	Amount    uint64 `json:"amount"`
	Origin    string `json:"origin"`
	CreatedAt int64  `json:"created_at"`
}

type InstantSendState struct {
	Version         int                               `json:"version"`
	Started         bool                              `json:"started"`
	Ready           bool                              `json:"ready"`
	SnapshotHeight  int64                             `json:"snapshot_height"`
	SnapshotPending []string                          `json:"snapshot_pending,omitempty"`
	Reserved        uint64                            `json:"reserved"`
	Count           int                               `json:"count"`
	Reservations    map[string]InstantSendReservation `json:"-"`
}

// Available returns the unused allowance, or zero while warming up or at the limit.
func (s InstantSendState) Available(limit uint64) uint64 {
	if !s.Ready || s.Reserved >= limit {
		return 0
	}
	return limit - s.Reserved
}

// clone copies the state without sharing its pending IDs or reservations.
func (s InstantSendState) clone() InstantSendState {
	s.SnapshotPending = slices.Clone(s.SnapshotPending)
	s.Reservations = maps.Clone(s.Reservations)
	return s
}

// validate rejects inconsistent journal metadata, snapshot IDs, and reservation totals.
func (s InstantSendState) validate() error {
	if s.Version != 1 || s.SnapshotHeight < 0 {
		return errors.New("invalid InstantSend journal metadata")
	}
	// A ready journal must have started and finished processing its snapshot.
	if s.Ready && (!s.Started || len(s.SnapshotPending) > 0) {
		return errors.New("invalid InstantSend journal metadata")
	}
	// An unstarted journal cannot contain snapshot work or reservations.
	if !s.Started && (len(s.SnapshotPending) > 0 || len(s.Reservations) > 0) {
		return errors.New("invalid InstantSend journal metadata")
	}
	seen := make(map[string]bool, len(s.SnapshotPending))
	for _, id := range s.SnapshotPending {
		if id == "" || id != strings.ToLower(id) || seen[id] {
			return errors.New("invalid InstantSend snapshot IDs")
		}
		seen[id] = true
	}
	var total uint64
	for id, r := range s.Reservations {
		if id == "" || id != strings.ToLower(id) || id != r.TxID {
			return errors.New("invalid InstantSend reservation")
		}
		if r.Amount == 0 || r.CreatedAt <= 0 {
			return errors.New("invalid InstantSend reservation")
		}
		if r.Origin != InstantSendObservation && r.Origin != InstantSendSnapshot {
			return errors.New("invalid InstantSend reservation")
		}
		if math.MaxUint64-total < r.Amount {
			return errors.New("invalid InstantSend reservation")
		}
		total += r.Amount
	}
	if total != s.Reserved || len(s.Reservations) != s.Count {
		return errors.New("inconsistent InstantSend reservation totals")
	}
	return nil
}

// loadInstantSendState must be called with observedTxMu held.
func (t *TemporalStorage) loadInstantSendState() (InstantSendState, error) {
	if t.instantSendState != nil {
		return t.instantSendState.clone(), nil
	}
	s := InstantSendState{Reservations: make(map[string]InstantSendReservation)}
	b, err := t.db.Get([]byte(instantSendStateKey), nil)
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return s, err
	}
	missing := errors.Is(err, leveldb.ErrNotFound)
	if missing {
		s.Version = 1
	} else {
		if err = json.Unmarshal(b, &s); err != nil {
			return s, err
		}
	}
	it := t.db.NewIterator(util.BytesPrefix([]byte(instantSendReservationPrefix)), nil)
	defer it.Release()
	for it.Next() {
		var r InstantSendReservation
		if err = json.Unmarshal(it.Value(), &r); err != nil {
			return s, err
		}
		id := strings.TrimPrefix(string(it.Key()), instantSendReservationPrefix)
		s.Reservations[id] = r
	}
	if err = it.Error(); err != nil {
		return s, err
	}
	if missing && len(s.Reservations) != 0 {
		return s, errors.New("missing InstantSend metadata with existing reservations")
	}
	if err = s.validate(); err != nil {
		return s, err
	}
	cached := s.clone()
	t.instantSendState = &cached
	return s, nil
}

// GetInstantSendState returns a validated copy of the journal under the observation lock.
func (t *TemporalStorage) GetInstantSendState() (InstantSendState, error) {
	t.observedTxMu.Lock()
	defer t.observedTxMu.Unlock()
	return t.loadInstantSendState()
}

// updateInstantSend writes changed reservations, metadata and any observation
// markers together. An unsuccessful write never publishes the tentative state.
func (t *TemporalStorage) updateInstantSend(update func(*InstantSendState, *leveldb.Batch) error) error {
	t.observedTxMu.Lock()
	defer t.observedTxMu.Unlock()
	before, err := t.loadInstantSendState()
	if err != nil {
		return err
	}
	after := before.clone()
	batch := new(leveldb.Batch)
	if err = update(&after, batch); err != nil {
		return err
	}
	after.Count = len(after.Reservations)
	if err = after.validate(); err != nil {
		return err
	}
	for id, r := range after.Reservations {
		if old, ok := before.Reservations[id]; ok && old == r {
			continue
		}
		var b []byte
		b, err = json.Marshal(r)
		if err != nil {
			return err
		}
		batch.Put([]byte(instantSendReservationPrefix+id), b)
	}
	for id := range before.Reservations {
		if _, ok := after.Reservations[id]; !ok {
			batch.Delete([]byte(instantSendReservationPrefix + id))
		}
	}
	oldMeta, err := json.Marshal(before)
	if err != nil {
		return err
	}
	newMeta, err := json.Marshal(after)
	if err != nil {
		return err
	}
	if string(oldMeta) != string(newMeta) {
		batch.Put([]byte(instantSendStateKey), newMeta)
	}
	if batch.Len() == 0 {
		return nil
	}
	if err = t.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return err
	}
	t.instantSendState = &after
	return nil
}

// StartInstantSendWarmup records the initial mempool snapshot and scanner height.
// An existing journal is preserved so a restart cannot reset its allowance usage.
func (t *TemporalStorage) StartInstantSendWarmup(height int64, ids []string) error {
	return t.updateInstantSend(func(s *InstantSendState, _ *leveldb.Batch) error {
		if s.Started {
			return nil
		}
		s.Started = true
		s.SnapshotHeight = height
		seen := make(map[string]bool)
		for _, id := range ids {
			id = strings.ToLower(id)
			if id == "" {
				return errors.New("empty snapshot transaction ID")
			}
			if !seen[id] {
				s.SnapshotPending = append(s.SnapshotPending, id)
				seen[id] = true
			}
		}
		slices.Sort(s.SnapshotPending)
		return nil
	})
}

// RecordInstantSendSnapshot saves reservations and removes classified snapshot IDs
// in one write. IDs already settled by a block cannot create new reservations.
func (t *TemporalStorage) RecordInstantSendSnapshot(classified []string, reservations []InstantSendReservation) error {
	return t.updateInstantSend(func(s *InstantSendState, _ *leveldb.Batch) error {
		if !s.Started || s.Ready {
			return errors.New("InstantSend snapshot is not in progress")
		}
		pending := make(map[string]bool, len(s.SnapshotPending))
		for _, id := range s.SnapshotPending {
			pending[id] = true
		}
		done := make(map[string]bool, len(classified))
		for _, id := range classified {
			done[strings.ToLower(id)] = true
		}
		for _, r := range reservations {
			r.TxID = strings.ToLower(r.TxID)
			r.Origin = InstantSendSnapshot
			if !done[r.TxID] {
				return errors.New("snapshot reservation has not been classified")
			}
			// Another transaction's inclusion proof may already have settled
			// this ID while the caller was assembling the snapshot batch.
			if !pending[r.TxID] {
				continue
			}
			if _, exists := s.Reservations[r.TxID]; exists {
				continue
			}
			if math.MaxUint64-s.Reserved < r.Amount {
				return errors.New("InstantSend amount overflow")
			}
			s.Reservations[r.TxID] = r
			s.Reserved += r.Amount
		}
		removeSnapshotIDs(s, classified)
		return nil
	})
}

// removeSnapshotIDs removes the supplied transaction IDs from the pending startup snapshot.
func removeSnapshotIDs(s *InstantSendState, ids []string) {
	removed := make(map[string]bool, len(ids))
	for _, id := range ids {
		removed[strings.ToLower(id)] = true
	}
	s.SnapshotPending = slices.DeleteFunc(s.SnapshotPending, func(id string) bool { return removed[id] })
}

// CompleteInstantSendWarmup marks the journal ready once the snapshot is processed
// and the block scanner has advanced past its starting height.
func (t *TemporalStorage) CompleteInstantSendWarmup(scannedHeight int64) error {
	return t.updateInstantSend(func(s *InstantSendState, _ *leveldb.Batch) error {
		if s.Started && len(s.SnapshotPending) == 0 && scannedHeight > s.SnapshotHeight {
			s.Ready = true
		}
		return nil
	})
}

// ReleaseInstantSendReservations releases allowance and pending snapshot entries
// without removing observation markers. Callers must first verify that the
// transactions are included in a canonical ChainLocked block.
func (t *TemporalStorage) ReleaseInstantSendReservations(ids []string) error {
	return t.updateInstantSend(func(s *InstantSendState, _ *leveldb.Batch) error {
		for _, id := range ids {
			id = strings.ToLower(id)
			if r, ok := s.Reservations[id]; ok {
				s.Reserved -= r.Amount
				delete(s.Reservations, id)
			}
		}
		// A block also settles snapshot IDs whose RPC classification was pending.
		removeSnapshotIDs(s, ids)
		return nil
	})
}

type InstantSendClaim struct {
	TxID   string
	Amount uint64
	Exempt bool
}

// ClaimInstantSendTxs atomically selects a batch against the shared allowance.
// The caller serializes its fresh finality checks with block reconciliation.
func (t *TemporalStorage) ClaimInstantSendTxs(claims []InstantSendClaim, minimum, limit uint64, customersAllowed bool, now int64) ([]string, error) {
	var accepted []string
	err := t.updateInstantSend(func(s *InstantSendState, batch *leveldb.Batch) error {
		seen := make(map[string]bool)
		for _, claim := range claims {
			id := strings.ToLower(claim.TxID)
			if id == "" {
				return errors.New("empty InstantSend transaction ID")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			observed, err := t.db.Has([]byte(t.getObservedTxKey(id)), nil)
			if err != nil {
				return err
			}
			if observed {
				continue
			}
			if !claim.Exempt {
				if _, reserved := s.Reservations[id]; reserved {
					continue
				}
				if !customersAllowed {
					continue
				}
				if claim.Amount == 0 || claim.Amount < minimum {
					continue
				}
				if claim.Amount > s.Available(limit) {
					continue
				}
				s.Reservations[id] = InstantSendReservation{TxID: id, Amount: claim.Amount, Origin: InstantSendObservation, CreatedAt: now}
				s.Reserved += claim.Amount
			}
			batch.Put([]byte(t.getObservedTxKey(id)), []byte(id))
			accepted = append(accepted, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return accepted, nil
}
