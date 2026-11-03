package monero

import (
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
)

func TestVaultAddressStoreRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vault-addresses.db")
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		t.Fatalf("open leveldb: %v", err)
	}
	defer db.Close()

	store := NewVaultAddressStore(db)
	if setErr := store.Set("thorpub1vault", "48qQe7vL5JGG3J8jWzR2a7s9nXh9JepmFp3qfXv5g6G4dR9rVQ4kQ9Q5Y3Vj9zMsF3Qw4Ax6XtW4pS9A"); setErr != nil {
		t.Fatalf("set vault address: %v", setErr)
	}

	got, err := store.Get("thorpub1vault")
	if err != nil {
		t.Fatalf("get vault address: %v", err)
	}
	if got == "" {
		t.Fatalf("expected non-empty vault address")
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("list vault addresses: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected one vault address entry, got %d", len(all))
	}
}
