package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog/log"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestMapScannerContinuityErrorRequiresOperatorForUnsafeDeficit(t *testing.T) {
	t.Parallel()

	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	for _, nextHeight := range []int64{
		0,
		1_000 - btypes.MaxCursorRewindBlocks - 1,
		1_000,
		1_001,
	} {
		err := scanner.mapScannerContinuityError(1_000, &sidecar.ScannerRewindRequiredError{
			RequestedHeight: 1_000,
			NextHeight:      nextHeight,
			ResetEpoch:      7,
		})
		var recovery *btypes.ScannerRecoveryRequiredError
		if !errors.As(err, &recovery) {
			t.Fatalf("next height %d must require operator recovery, got %v", nextHeight, err)
		}
		if recovery.CurrentHeight != 1_000 || recovery.NextHeight != nextHeight {
			t.Fatalf("unexpected operator recovery error: %+v", recovery)
		}
	}

	err := scanner.mapScannerContinuityError(1_000, &sidecar.ScannerRewindRequiredError{
		RequestedHeight: 1_000,
		NextHeight:      1_000 - btypes.MaxCursorRewindBlocks,
		ResetEpoch:      7,
	})
	var rewind *btypes.RewindRequiredError
	if !errors.As(err, &rewind) {
		t.Fatalf("bounded deficit must remain automatically recoverable, got %v", err)
	}
}

func TestClientVaultRegistrationSerializesWithScanner(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}
		requestSeen <- struct{}{}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"registered":           true,
			"effective_birthday":   100,
			"reset_epoch":          1,
			"expected_next_height": 100,
		})
	}))
	defer server.Close()

	sidecarClient, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	scanner := NewScanner(log.Logger, sidecarClient, nil, common.XMRChain, common.EmptyPubKey, 72)
	client := &Client{
		logger:  log.Logger,
		sidecar: sidecarClient,
		scanner: scanner,
		chain:   common.XMRChain,
	}
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	scanner.scanMu.Lock()
	done := make(chan error, 1)
	go func() { done <- client.ensureVaultRegistered(vaultPubKey) }()
	select {
	case <-requestSeen:
		t.Fatal("vault registration raced an in-progress scan")
	case <-time.After(100 * time.Millisecond):
	}
	scanner.scanMu.Unlock()

	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("vault registration did not resume after scan completed")
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("serialized vault registration: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serialized vault registration did not return")
	}
}
