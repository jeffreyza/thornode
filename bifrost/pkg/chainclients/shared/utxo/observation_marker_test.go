package utxo

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

func openObservationMarkerStorage(t *testing.T, path string, opts *opt.Options) (*TemporalStorage, *leveldb.DB) {
	t.Helper()
	db, err := leveldb.OpenFile(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewTemporalStorage(db, 0)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func TestObservedMarkerSurvivesRestartWithoutObserverHandoff(t *testing.T) {
	path := t.TempDir()
	store, db := openObservationMarkerStorage(t, path, nil)
	added, err := store.TrackObservedTxs([]string{"tx", "second-tx", "tx"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"tx", "second-tx"}) {
		t.Fatalf("claimed transactions: got %v, want [tx second-tx]", added)
	}
	// There is deliberately no Observer handoff or acknowledgement before closing.
	err = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, _ = openObservationMarkerStorage(t, path, nil)
	for _, txid := range []string{"tx", "second-tx"} {
		var observed bool
		observed, err = store.HasObservedTx(txid)
		if err != nil {
			t.Fatal(err)
		}
		if !observed {
			t.Fatalf("missing marker after restart: %s", txid)
		}
	}
	wasAdded, err := store.TrackObservedTx("tx")
	if err != nil {
		t.Fatal(err)
	}
	if wasAdded {
		t.Fatal("duplicate transaction accepted after restart")
	}
	added, err = store.TrackObservedTxs([]string{"second-tx", "different-tx", "tx", "different-tx"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"different-tx"}) {
		t.Fatalf("mixed batch after restart: got %v, want [different-tx]", added)
	}
}

func TestObservedMarkerConcurrentClaims(t *testing.T) {
	store, _ := openObservationMarkerStorage(t, t.TempDir(), nil)
	results := make(chan []string, 64)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			var accepted []string
			var err error
			if i%2 == 0 {
				var added bool
				added, err = store.TrackObservedTx("same-tx")
				if added {
					accepted = []string{"same-tx"}
				}
			} else {
				accepted, err = store.TrackObservedTxs([]string{"same-tx", "batch-tx", "batch-tx"})
			}
			if err != nil {
				t.Error(err)
			}
			results <- accepted
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	counts := make(map[string]int)
	for accepted := range results {
		for _, txid := range accepted {
			counts[txid]++
		}
	}
	if len(counts) != 2 || counts["same-tx"] != 1 || counts["batch-tx"] != 1 {
		t.Fatalf("concurrent claims: got %v, want each transaction exactly once", counts)
	}
}

func TestObservedMarkerWriteFailureDoesNotAcceptTransaction(t *testing.T) {
	path := t.TempDir()
	store, db := openObservationMarkerStorage(t, path, nil)
	_, err := store.TrackObservedTx("known-tx")
	if err != nil {
		t.Fatal(err)
	}
	err = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, _ = openObservationMarkerStorage(t, path, &opt.Options{ReadOnly: true})
	// Reads succeed, but writing the new marker must fail.
	observed, err := store.HasObservedTx("tx")
	if err != nil {
		t.Fatal(err)
	}
	if observed {
		t.Fatal("unexpected existing marker")
	}
	added, err := store.TrackObservedTx("tx")
	if err == nil {
		t.Fatal("write to read-only storage returned no error")
	}
	if added {
		t.Fatal("failed write accepted transaction")
	}
	accepted, err := store.TrackObservedTxs([]string{"known-tx", "tx", "other-tx"})
	if err == nil {
		t.Fatal("batch write to read-only storage returned no error")
	}
	if len(accepted) != 0 {
		t.Fatalf("failed batch accepted transactions: %v", accepted)
	}
	for _, txid := range []string{"tx", "other-tx"} {
		observed, err := store.HasObservedTx(txid)
		if err != nil {
			t.Fatal(err)
		}
		if observed {
			t.Fatalf("failed batch left marker %s", txid)
		}
	}
}

type observationJournalStorage struct {
	storage.Storage
	syncs atomic.Int64
}

func (s *observationJournalStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	writer, err := s.Storage.Create(fd)
	if err != nil || fd.Type != storage.TypeJournal {
		return writer, err
	}
	return &observationJournalWriter{Writer: writer, syncs: &s.syncs}, nil
}

type observationJournalWriter struct {
	storage.Writer
	syncs *atomic.Int64
}

func (w *observationJournalWriter) Sync() error {
	w.syncs.Add(1)
	return w.Writer.Sync()
}

func TestObservedMarkersUseOneSyncPerBatch(t *testing.T) {
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
	before := files.syncs.Load()
	added, err := store.TrackObservedTxs([]string{"third", "first", "third", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"third", "first", "second"}) {
		t.Fatalf("batch order and duplicates: got %v, want [third first second]", added)
	}
	if got := files.syncs.Load() - before; got != 1 {
		t.Fatalf("batch performed %d journal syncs, want 1", got)
	}
	for _, ids := range [][]string{nil, {"first", "second", "first"}} {
		added, err = store.TrackObservedTxs(ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(added) != 0 {
			t.Fatalf("empty or duplicate batch accepted transactions: %v", added)
		}
	}
	if got := files.syncs.Load() - before; got != 1 {
		t.Fatalf("empty or duplicate batch caused extra syncs: got %d", got)
	}
	wasAdded, err := store.TrackObservedTx("fourth")
	if err != nil {
		t.Fatal(err)
	}
	if !wasAdded {
		t.Fatal("single claim did not add transaction")
	}
	if got := files.syncs.Load() - before; got != 2 {
		t.Fatalf("single wrapper did not sync its marker: total syncs=%d, want 2", got)
	}
}
