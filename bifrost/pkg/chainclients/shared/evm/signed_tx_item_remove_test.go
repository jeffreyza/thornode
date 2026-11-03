package evm

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"

	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
)

// TestRemoveSignedTxItemMatchesReference runs random add/remove/list sequences
// against the original unconditional-Delete removal and compares listings.
func TestRemoveSignedTxItemMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(13)) //nolint:gosec // fixed seed: a reproducible differential test
	got := newBenchBlockMetaAccessor(t)
	want := newBenchBlockMetaAccessor(t)
	for i := 0; i < 5000; i++ {
		hash := benchTxHash(uint64(rng.Intn(64)))
		switch rng.Intn(3) {
		case 0:
			item := evmtypes.SignedTxItem{Hash: hash, Height: int64(rng.Intn(100)), VaultPubKey: fmt.Sprint(rng.Intn(4))}
			if err := got.AddSignedTxItem(item); err != nil {
				t.Fatal(err)
			}
			if err := want.AddSignedTxItem(item); err != nil {
				t.Fatal(err)
			}
		case 1:
			gotErr := got.RemoveSignedTxItem(hash)
			wantErr := want.db.Delete([]byte(want.getSignedTxItemKey(hash)), nil)
			if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Fatalf("op %d: remove err %v, want %v", i, gotErr, wantErr)
			}
		default:
			gotItems, gotErr := got.GetSignedTxItems()
			wantItems, wantErr := want.GetSignedTxItems()
			if gotErr != nil || wantErr != nil || fmt.Sprint(gotItems) != fmt.Sprint(wantItems) {
				t.Fatalf("op %d: items %v (%v), want %v (%v)", i, gotItems, gotErr, wantItems, wantErr)
			}
		}
	}
}

// TestRemoveSignedTxItemReadOnlyDB pins the one behavior difference: removing
// a hash that was never stored no longer attempts the write, so a database
// that rejects writes no longer turns it into an error. Removing a stored
// hash still fails as before.
func TestRemoveSignedTxItemReadOnlyDB(t *testing.T) {
	stor := storage.NewMemStorage()
	db, err := leveldb.Open(stor, nil)
	if err != nil {
		t.Fatal(err)
	}
	accessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = accessor.AddSignedTxItem(evmtypes.SignedTxItem{Hash: benchTxHash(1)}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	roDB, err := leveldb.Open(stor, &opt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer roDB.Close()
	ro, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, roDB)
	if err != nil {
		t.Fatal(err)
	}
	if err = ro.RemoveSignedTxItem(benchTxHash(2)); err != nil {
		t.Fatalf("removing a missing hash: %v", err)
	}
	if err = ro.RemoveSignedTxItem(benchTxHash(1)); !errors.Is(err, leveldb.ErrReadOnly) {
		t.Fatalf("removing a stored hash: got %v, want %v", err, leveldb.ErrReadOnly)
	}
}
