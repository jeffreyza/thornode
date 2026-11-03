package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

// A registration that rewinds the cursor under a newer reset epoch must survive
// a scanner sync whose response is processed afterwards. Serializing the two
// calls behind a lock was only ever a means to that end; the invariant itself is
// the epoch-then-height ordering in mergeScannerContinuity, which holds no
// matter which response lands first. This test pins the dangerous order: the
// sync response is only released once the registration rewind is cached.
func TestStaleScannerSyncCannotOverwriteRegistrationRewind(t *testing.T) {
	t.Parallel()

	blockHash := strings.Repeat("a", 64)
	parentHash := strings.Repeat("b", 64)

	syncEntered := make(chan struct{})
	releaseSync := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/scanner/sync":
			close(syncEntered)
			<-releaseSync
			_ = json.NewEncoder(w).Encode(Transfers{})
		case "/v1/monero/vaults/register":
			next := uint64(50)
			_ = json.NewEncoder(w).Encode(RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  50,
				ExpectedNextHeight: &next,
				ResetEpoch:         2,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	initialNext := uint64(100)
	client.setScannerCursor(ScannerState{NextHeight: &initialNext})

	// The sync binds epoch 0 / height 100 and would cache epoch 0 / next 101.
	syncDone := make(chan error, 1)
	go func() {
		_, syncErr := client.SyncAt(100, 100, blockHash, parentHash, 72)
		syncDone <- syncErr
	}()
	<-syncEntered

	// The registration rewinds to epoch 2 / next 50 while the sync is in flight.
	if _, err = client.RegisterVault(RegisterVaultReq{
		VaultPubKey: "older-vault", VaultCreationHeight: 50,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if cursor, known := client.CachedScannerState(); !known ||
		cursor.NextHeight == nil || *cursor.NextHeight != 50 || cursor.ResetEpoch != 2 {
		t.Fatalf("registration rewind was not cached: %#v", cursor)
	}

	close(releaseSync)
	if err = <-syncDone; err != nil {
		t.Fatalf("sync: %v", err)
	}

	cursor, known := client.CachedScannerState()
	if !known || cursor.NextHeight == nil || *cursor.NextHeight != 50 || cursor.ResetEpoch != 2 {
		t.Fatalf("registration rewind was overwritten by sync cache update: %#v", cursor)
	}
}
