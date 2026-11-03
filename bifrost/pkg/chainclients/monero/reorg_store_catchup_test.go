package monero

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

func TestCheckpointCatchupEmptyDeltaPreservesJournal(t *testing.T) {
	store := newScannerReceiptStore(t)
	txID := testXMRHash("retiring-vault-deposit")
	original := terminalReorgMeta(90, "h90", "h89", txID, true)
	if err := store.Set(original); err != nil {
		t.Fatal(err)
	}
	next := scannerCatchupMarker{StartHeight: 91, TargetHeight: 100, AnchorHeight: 90, AnchorHash: "h90"}
	delta := &reorgBlockMeta{Height: 90, BlockHash: "h90", PreviousHash: "h89", ProvenanceComplete: true}
	if err := store.CheckpointCatchup(next, delta, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(90)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("empty new-vault delta erased prior journal evidence: got=%#v want=%#v err=%v", got, original, err)
	}
}

func TestCheckpointCatchupAccumulatesProvenanceAndReceipts(t *testing.T) {
	for _, oldComplete := range []bool{false, true} {
		for _, deltaComplete := range []bool{false, true} {
			t.Run(fmt.Sprintf("complete_%t_%t", oldComplete, deltaComplete), func(t *testing.T) {
				store := newScannerReceiptStore(t)
				first := scannerReceiptItem("old-vault", 90)
				second := scannerReceiptItem("new-vault", 90)
				old := terminalReorgMeta(90, "h90", "h89", first.Tx, true)
				old.ProvenanceComplete = oldComplete
				if err := store.Set(old); err != nil {
					t.Fatal(err)
				}
				if err := store.PutObservationReceipt(old, []*stypes.TxInItem{first}); err != nil {
					t.Fatal(err)
				}
				delta := terminalReorgMeta(90, "H90", "H89", second.Tx, true)
				delta.CustomerTransactions = []string{second.Tx, first.Tx, second.Tx}
				delta.ProvenanceComplete = deltaComplete
				next := scannerCatchupMarker{StartHeight: 91, TargetHeight: 90, AnchorHeight: 90, AnchorHash: "h90"}
				wantIDs := []string{first.Tx, second.Tx}
				slices.Sort(wantIDs)
				var initialValue []byte
				for attempt := 0; attempt < 3; attempt++ {
					if err := store.CheckpointCatchup(next, delta, []*stypes.TxInItem{second}); err != nil {
						t.Fatal(err)
					}
					got, err := store.Get(90)
					if err != nil || got == nil ||
						!reflect.DeepEqual(got.CustomerTransactions, wantIDs) ||
						!reflect.DeepEqual(got.VaultOriginTransactions, wantIDs) ||
						got.ProvenanceComplete != (oldComplete && deltaComplete) {
						t.Fatalf("lost accumulated journal/provenance: %#v err=%v", got, err)
					}
					value, err := store.db.Get([]byte(reorgBlockMetaKey(90)), nil)
					if err != nil {
						t.Fatal(err)
					}
					if attempt == 0 {
						initialValue = value
					} else if !bytes.Equal(initialValue, value) {
						t.Fatal("identical retry changed serialized journal")
					}
				}
				if len(delta.CustomerTransactions) != 3 || delta.CustomerTransactions[0] != second.Tx {
					t.Fatal("checkpoint mutated caller's delta")
				}
				claims, err := store.ListObservationReceipts()
				if err != nil || len(claims) != 1 || len(claims[0].entry.Items) != 2 ||
					claims[0].entry.BlockHash != delta.BlockHash || claims[0].entry.PreviousHash != delta.PreviousHash {
					t.Fatalf("receipt lost accumulated observations or canonical binding: %#v err=%v", claims, err)
				}
				marker, err := store.GetCatchupMarker()
				if err != nil || marker == nil || !marker.equal(next) {
					t.Fatalf("checkpoint marker not bound to journal: %#v err=%v", marker, err)
				}
			})
		}
	}
}

func TestCheckpointCatchupReplacesDifferentBlock(t *testing.T) {
	for _, withItems := range []bool{false, true} {
		t.Run(fmt.Sprintf("with_items_%t", withItems), func(t *testing.T) {
			store := newScannerReceiptStore(t)
			first := scannerReceiptItem("orphaned-vault", 90)
			old := terminalReorgMeta(90, "old90", "old89", first.Tx, true)
			old.ProvenanceComplete = false
			if err := store.Set(old); err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservationReceipt(old, []*stypes.TxInItem{first}); err != nil {
				t.Fatal(err)
			}
			replacement := &reorgBlockMeta{Height: 90, BlockHash: "new90", PreviousHash: "new89", ProvenanceComplete: true}
			var items []*stypes.TxInItem
			if withItems {
				items = []*stypes.TxInItem{scannerReceiptItem("replacement-vault", 90)}
				replacement.CustomerTransactions = []string{items[0].Tx}
			}
			next := scannerCatchupMarker{StartHeight: 91, TargetHeight: 90, AnchorHeight: 90, AnchorHash: "new90"}
			if err := store.CheckpointCatchup(next, replacement, items); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(90)
			if err != nil || !reflect.DeepEqual(got, replacement) {
				t.Fatalf("replacement inherited orphan evidence: %#v err=%v", got, err)
			}
			claims, err := store.ListObservationReceipts()
			if err != nil || len(claims) != len(items) {
				t.Fatalf("replacement retained orphan receipt: %#v err=%v", claims, err)
			}
			if withItems && (claims[0].entry.BlockHash != replacement.BlockHash ||
				len(claims[0].entry.Items) != 1 || claims[0].entry.Items[0].Tx != items[0].Tx) {
				t.Fatalf("replacement receipt inherited orphan observations: %#v", claims)
			}
		})
	}
}

// captureCatchupState compares the entire small test database, including receipts
// and the marker, so a rejected checkpoint cannot partially advance durable state.
func captureCatchupState(t *testing.T, store *ReorgStore) map[string]string {
	t.Helper()
	iter := store.db.NewIterator(nil, nil)
	defer iter.Release()
	state := make(map[string]string)
	for iter.Next() {
		state[string(iter.Key())] = string(iter.Value())
	}
	if err := iter.Error(); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestCheckpointCatchupRejectsInconsistentStateAtomically(t *testing.T) {
	for _, invalid := range []string{"nil", "parent", "stored-height", "stored-json", "stored-subset", "incoming-subset", "unnormalized", "receipt", "marker"} {
		t.Run(invalid, func(t *testing.T) {
			store := newScannerReceiptStore(t)
			item := scannerReceiptItem("old-vault", 90)
			old := terminalReorgMeta(90, "h90", "h89", item.Tx, true)
			if err := store.Set(old); err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservationReceipt(old, []*stypes.TxInItem{item}); err != nil {
				t.Fatal(err)
			}
			if err := store.BeginCatchup(scannerCatchupMarker{StartHeight: 90, TargetHeight: 100, AnchorHeight: 89, AnchorHash: "h89"}); err != nil {
				t.Fatal(err)
			}
			delta := terminalReorgMeta(90, "h90", "h89", testXMRHash("new-vault"), false)
			next := scannerCatchupMarker{StartHeight: 91, TargetHeight: 100, AnchorHeight: 90, AnchorHash: "h90"}
			var items []*stypes.TxInItem
			switch invalid {
			case "nil":
				delta = nil
			case "parent":
				delta.PreviousHash = "conflicting89"
			case "stored-height":
				old.Height = 91
			case "stored-json":
				if err := store.db.Put([]byte(reorgBlockMetaKey(90)), []byte("{"), nil); err != nil {
					t.Fatal(err)
				}
			case "stored-subset":
				old.VaultOriginTransactions = []string{testXMRHash("absent")}
			case "incoming-subset":
				delta.VaultOriginTransactions = []string{item.Tx}
			case "unnormalized":
				delta.CustomerTransactions = []string{strings.ToUpper(delta.CustomerTransactions[0])}
			case "receipt":
				items = []*stypes.TxInItem{scannerReceiptItem("wrong-height", 91)}
			case "marker":
				next.AnchorHash = "conflicting90"
			}
			if invalid == "stored-height" || invalid == "stored-subset" {
				value, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.db.Put([]byte(reorgBlockMetaKey(90)), value, nil); err != nil {
					t.Fatal(err)
				}
			}
			before := captureCatchupState(t, store)
			if err := store.CheckpointCatchup(next, delta, items); err == nil {
				t.Fatal("inconsistent checkpoint accepted")
			}
			if after := captureCatchupState(t, store); !reflect.DeepEqual(after, before) {
				t.Fatal("rejected checkpoint partially changed durable state")
			}
		})
	}
}

type catchupJournalStorage struct {
	storage.Storage
	writes    atomic.Int64
	syncs     atomic.Int64
	failWrite atomic.Bool
}

func (s *catchupJournalStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	writer, err := s.Storage.Create(fd)
	if err != nil || fd.Type != storage.TypeJournal {
		return writer, err
	}
	return &catchupJournalWriter{Writer: writer, owner: s}, nil
}

type catchupJournalWriter struct {
	storage.Writer
	owner *catchupJournalStorage
}

func (w *catchupJournalWriter) Write(p []byte) (int, error) {
	w.owner.writes.Add(1)
	if w.owner.failWrite.Load() {
		return 0, errors.New("injected checkpoint journal write failure")
	}
	return w.Writer.Write(p)
}

func (w *catchupJournalWriter) Sync() error {
	w.owner.syncs.Add(1)
	return w.Writer.Sync()
}

func TestCheckpointCatchupRestartPruningAndWriteFailure(t *testing.T) {
	withProductionFsync(t) // counts the checkpoint's syncs
	for _, failWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("write_failure_%t", failWrite), func(t *testing.T) {
			disk := &catchupJournalStorage{Storage: storage.NewMemStorage()}
			db, err := leveldb.Open(disk, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := NewReorgStore(db)
			first := scannerReceiptItem("old-vault", 90)
			old := terminalReorgMeta(90, "h90", "h89", first.Tx, true)
			next := scannerCatchupMarker{StartHeight: 91, TargetHeight: 90, AnchorHeight: 90, AnchorHash: "h90"}
			if err = store.CheckpointCatchup(next, old, []*stypes.TxInItem{first}); err != nil {
				t.Fatal(err)
			}
			if err = store.BeginCatchup(scannerCatchupMarker{StartHeight: 90, TargetHeight: 90, AnchorHeight: 89, AnchorHash: "h89"}); err != nil {
				t.Fatal(err)
			}
			before := captureCatchupState(t, store)
			second := scannerReceiptItem("new-vault", 90)
			delta := terminalReorgMeta(90, "h90", "h89", second.Tx, false)
			disk.writes.Store(0)
			disk.syncs.Store(0)
			disk.failWrite.Store(failWrite)
			checkpointErr := store.CheckpointCatchup(next, delta, []*stypes.TxInItem{second})
			if (checkpointErr != nil) != failWrite {
				t.Fatalf("checkpoint result: %v", checkpointErr)
			}
			if !failWrite && (disk.writes.Load() != 1 || disk.syncs.Load() != 1) {
				t.Fatalf("small checkpoint used %d writes and %d syncs", disk.writes.Load(), disk.syncs.Load())
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			disk.failWrite.Store(false)
			db, err = leveldb.Open(disk, nil)
			if err != nil {
				t.Fatal(err)
			}
			store = NewReorgStore(db)
			if failWrite {
				if after := captureCatchupState(t, store); !reflect.DeepEqual(after, before) {
					t.Fatal("failed checkpoint recovered partial state")
				}
			}
			// A retry after restart converges for both a failed write and a
			// successful commit whose response was lost.
			if err = store.CheckpointCatchup(next, delta, []*stypes.TxInItem{second}); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(90)
			if err != nil || got == nil || len(got.CustomerTransactions) != 2 ||
				!slices.Contains(got.CustomerTransactions, first.Tx) || !slices.Contains(got.CustomerTransactions, second.Tx) ||
				!reflect.DeepEqual(got.VaultOriginTransactions, []string{first.Tx}) {
				t.Fatalf("restart/retry lost journal: %#v err=%v", got, err)
			}
			claims, err := store.ListObservationReceipts()
			if err != nil || len(claims) != 1 || len(claims[0].entry.Items) != 2 {
				t.Fatalf("restart/retry lost receipt: %#v err=%v", claims, err)
			}
			if count, pruneErr := store.PruneBefore(100); pruneErr != nil || count != 0 {
				t.Fatalf("pruned pending checkpoint: count=%d err=%v", count, pruneErr)
			}
			if err = store.AckObservationReceipts(claims, &next); err != nil {
				t.Fatal(err)
			}
			if count, pruneErr := store.PruneBefore(100); pruneErr != nil || count != 1 {
				t.Fatalf("acknowledged checkpoint did not prune: count=%d err=%v", count, pruneErr)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("merged journal bytes=%d, customer IDs=%d, vault-origin IDs=%d; successful small checkpoint: one journal write and one sync",
				len(encoded), len(got.CustomerTransactions), len(got.VaultOriginTransactions))
		})
	}
}

func BenchmarkMergeReorgJournalTxIDs(b *testing.B) {
	for _, count := range []int{8, 256, 4096} {
		b.Run(fmt.Sprintf("retained_%d", count), func(b *testing.B) {
			existing := make([]string, count)
			for i := range existing {
				existing[i] = fmt.Sprintf("%064x", count-i)
			}
			incoming := []string{existing[0], fmt.Sprintf("%064x", count+1)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got := mergeReorgJournalTxIDs(existing, incoming)
				if len(got) != count+1 {
					b.Fatal("journal union lost or duplicated transactions")
				}
			}
		})
	}
}
