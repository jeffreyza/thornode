package utxo

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

func readyInstantSendStore(t *testing.T, store *TemporalStorage) {
	t.Helper()
	if err := store.StartInstantSendWarmup(10, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteInstantSendWarmup(11); err != nil {
		t.Fatal(err)
	}
}

func instantSendState(t *testing.T, store *TemporalStorage) InstantSendState {
	t.Helper()
	s, err := store.GetInstantSendState()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInstantSendAllowanceSelectionAndSettlement(t *testing.T) {
	store, _ := openObservationMarkerStorage(t, t.TempDir(), nil)
	readyInstantSendStore(t, store)
	claims := []InstantSendClaim{
		{TxID: "oversize", Amount: 101},
		{TxID: "small", Amount: 9},
		{TxID: "FIRST", Amount: 60},
		{TxID: "first", Amount: 60},
		{TxID: "second", Amount: 40},
		{TxID: "waiting", Amount: 10},
		{TxID: "vault", Amount: 500, Exempt: true},
	}
	accepted, err := store.ClaimInstantSendTxs(claims, 10, 100, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accepted, []string{"first", "second", "vault"}) {
		t.Fatal(accepted)
	}
	s := instantSendState(t, store)
	if s.Reserved != 100 || s.Count != 2 || s.Available(100) != 0 {
		t.Fatal(s)
	}
	for _, id := range []string{"oversize", "small", "waiting"} {
		var marked bool
		marked, err = store.HasObservedTx(id)
		if err != nil || marked {
			t.Fatalf("deferred %s marked: %v %v", id, marked, err)
		}
	}
	// Lowering the limit does not erase exposure or underflow availability.
	if s.Available(50) != 0 || s.Available(150) != 50 {
		t.Fatal("incorrect resized allowance")
	}
	if err = store.ReleaseInstantSendReservations([]string{"vault", "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if instantSendState(t, store).Reserved != 100 {
		t.Fatal("unrelated block refilled allowance")
	}
	if err = store.ReleaseInstantSendReservations([]string{"FIRST", "first"}); err != nil {
		t.Fatal(err)
	}
	if err = store.ReleaseInstantSendReservations([]string{"first"}); err != nil {
		t.Fatal(err)
	}
	s = instantSendState(t, store)
	if s.Reserved != 40 || s.Available(100) != 60 {
		t.Fatal(s)
	}
	accepted, err = store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "waiting", Amount: 10}}, 10, 100, true, 2)
	if err != nil || !slices.Equal(accepted, []string{"waiting"}) {
		t.Fatalf("retry: %v %v", accepted, err)
	}
	// Settlement removes the reservation, never the observed marker.
	accepted, err = store.ClaimInstantSendTxs(claims, 0, 1000, true, 3)
	if err != nil || slices.Contains(accepted, "first") {
		t.Fatalf("duplicate: %v %v", accepted, err)
	}
}

func TestInstantSendConcurrentAllowanceClaims(t *testing.T) {
	store, _ := openObservationMarkerStorage(t, t.TempDir(), nil)
	readyInstantSendStore(t, store)
	var wg sync.WaitGroup
	results := make(chan []string, 100)
	for i := range 100 {
		wg.Go(func() {
			ids, err := store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: fmt.Sprint(i), Amount: 7}, {TxID: "shared", Amount: 3}}, 0, 100, true, 1)
			if err != nil {
				t.Error(err)
			}
			results <- ids
		})
	}
	wg.Wait()
	close(results)
	seen := make(map[string]bool)
	for ids := range results {
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("duplicate %s", id)
			}
			seen[id] = true
		}
	}
	s := instantSendState(t, store)
	if s.Reserved != 94 || s.Count != 14 || len(seen) != 14 {
		t.Fatal(s, seen)
	}
}

func TestInstantSendWarmupSurvivesRestartAndKeepsSnapshotChainLockOnly(t *testing.T) {
	path := t.TempDir()
	store, db := openObservationMarkerStorage(t, path, nil)
	// Legacy observation markers must not hide existing exposure from a snapshot.
	if _, err := store.TrackObservedTx("old"); err != nil {
		t.Fatal(err)
	}
	if err := store.StartInstantSendWarmup(10, []string{"old", "pending", "OLD"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInstantSendSnapshot([]string{"old"}, []InstantSendReservation{{TxID: "old", Amount: 80, CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, _ = openObservationMarkerStorage(t, path, nil)
	s := instantSendState(t, store)
	if !slices.Equal(s.SnapshotPending, []string{"pending"}) || s.Reserved != 80 || s.Ready {
		t.Fatal(s)
	}
	if err := store.CompleteInstantSendWarmup(11); err != nil {
		t.Fatal(err)
	}
	if instantSendState(t, store).Ready {
		t.Fatal("finished before snapshot completed")
	}
	if err := store.RecordInstantSendSnapshot([]string{"pending"}, []InstantSendReservation{{TxID: "pending", Amount: 40, CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteInstantSendWarmup(10); err != nil {
		t.Fatal(err)
	}
	if instantSendState(t, store).Ready {
		t.Fatal("finished before scanner passed baseline")
	}
	if err := store.CompleteInstantSendWarmup(11); err != nil {
		t.Fatal(err)
	}
	s = instantSendState(t, store)
	if !s.Ready || s.Reserved != 120 || s.Available(100) != 0 || s.Available(200) != 80 {
		t.Fatal(s)
	}
	accepted, err := store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "pending", Amount: 40}, {TxID: "new", Amount: 10}}, 0, 200, true, 2)
	if err != nil || !slices.Equal(accepted, []string{"new"}) {
		t.Fatal(accepted, err)
	}
	marked, err := store.HasObservedTx("pending")
	if err != nil || marked {
		t.Fatal("snapshot wrote observed marker", marked, err)
	}
	// The ordinary block path can still report a snapshot deposit exactly once.
	if err = store.ReleaseInstantSendReservations([]string{"pending"}); err != nil {
		t.Fatal(err)
	}
	added, err := store.TrackObservedTx("pending")
	if err != nil || !added {
		t.Fatal(added, err)
	}
}

func TestInstantSendAtomicWriteFailureAndRestart(t *testing.T) {
	path := t.TempDir()
	store, db := openObservationMarkerStorage(t, path, nil)
	readyInstantSendStore(t, store)
	accepted, err := store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "known", Amount: 20}}, 0, 100, true, 1)
	if err != nil || len(accepted) != 1 {
		t.Fatal(accepted, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, db = openObservationMarkerStorage(t, path, &opt.Options{ReadOnly: true})
	accepted, err = store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "new", Amount: 30}, {TxID: "vault", Exempt: true}}, 0, 100, true, 2)
	if err == nil || len(accepted) != 0 {
		t.Fatal("failed write accepted batch", accepted, err)
	}
	if instantSendState(t, store).Reserved != 20 {
		t.Fatal("tentative state leaked")
	}
	for _, id := range []string{"new", "vault"} {
		var marked bool
		marked, err = store.HasObservedTx(id)
		if err != nil || marked {
			t.Fatal(id, marked, err)
		}
	}
	if err = store.ReleaseInstantSendReservations([]string{"known"}); err == nil {
		t.Fatal("failed settlement succeeded")
	}
	if instantSendState(t, store).Reserved != 20 {
		t.Fatal("failed settlement released exposure")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, _ = openObservationMarkerStorage(t, path, nil)
	s := instantSendState(t, store)
	marked, err := store.HasObservedTx("known")
	if err != nil || !marked || s.Reserved != 20 || s.Reservations["known"].Origin != InstantSendObservation {
		t.Fatal(s, marked, err)
	}
}

func TestInstantSendVaultsBypassClosedAllowance(t *testing.T) {
	store, _ := openObservationMarkerStorage(t, t.TempDir(), nil)
	// Brand-new storage, customer policy unavailable, and no allowance.
	accepted, err := store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "in", Amount: 10}, {TxID: "vault", Amount: 1, Exempt: true}}, 100, 0, false, 1)
	if err != nil || !slices.Equal(accepted, []string{"vault"}) {
		t.Fatal(accepted, err)
	}
	if instantSendState(t, store).Reserved != 0 {
		t.Fatal("vault spent allowance")
	}
}

func TestInstantSendCorruptJournalDoesNotReset(t *testing.T) {
	for _, data := range []string{`broken`, `null`, `{}`, `{"version":2}`, `{"version":1,"reserved":10,"count":1}`} {
		store, db := openObservationMarkerStorage(t, t.TempDir(), nil)
		if err := db.Put([]byte(instantSendStateKey), []byte(data), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetInstantSendState(); err == nil {
			t.Fatal("accepted corrupt metadata", data)
		}
		if err := store.StartInstantSendWarmup(10, nil); err == nil {
			t.Fatal("overwrote corrupt metadata")
		}
		accepted, err := store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "new", Amount: 10}}, 0, 100, true, 1)
		if err == nil || len(accepted) != 0 {
			t.Fatal("admitted with corrupt metadata", accepted, err)
		}
	}
}

func TestInstantSendOneSyncForReservationAndMarkerBatch(t *testing.T) {
	files := &observationJournalStorage{Storage: storage.NewMemStorage()}
	db, err := leveldb.Open(files, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewTemporalStorage(db, 0)
	if err != nil {
		t.Fatal(err)
	}
	readyInstantSendStore(t, store)
	before := files.syncs.Load()
	accepted, err := store.ClaimInstantSendTxs([]InstantSendClaim{{TxID: "one", Amount: 10}, {TxID: "two", Amount: 20}, {TxID: "vault", Exempt: true}}, 0, 100, true, 1)
	if err != nil || len(accepted) != 3 {
		t.Fatal(accepted, err)
	}
	if got := files.syncs.Load() - before; got != 1 {
		t.Fatalf("got %d syncs, want 1", got)
	}
	if s := instantSendState(t, store); s.Reserved != 30 || s.Count != 2 {
		t.Fatal(s)
	}
}

func TestInstantSendSnapshotDoesNotRestoreSettledReservation(t *testing.T) {
	store, _ := openObservationMarkerStorage(t, t.TempDir(), nil)
	if err := store.StartInstantSendWarmup(10, []string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	// While classifying the second snapshot entry, its block proves inclusion
	// of both entries. A prepared reservation for the first must not survive.
	if err := store.ReleaseInstantSendReservations([]string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInstantSendSnapshot([]string{"first"}, []InstantSendReservation{{TxID: "first", Amount: 10, CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	s := instantSendState(t, store)
	if s.Reserved != 0 || len(s.SnapshotPending) != 0 {
		t.Fatal(s)
	}
}
