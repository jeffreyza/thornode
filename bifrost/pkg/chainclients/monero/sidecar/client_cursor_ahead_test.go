package sidecar

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

// A sidecar cursor AHEAD of the requested height is the one continuity failure
// retrying cannot repair: the signer has durably scanned past the block. It must
// be typed, so the scanner can tell it apart from a transient fetch failure
// instead of re-fetching the same height forever.
func TestPreflightScannerHeightReportsCursorAheadAsRecoveryRequired(t *testing.T) {
	t.Parallel()

	nextHeight := uint64(60)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == scannerStatePath {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ScannerState{
				ResetEpoch:       9,
				NextHeight:       &nextHeight,
				StrictAfterReset: true,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	// Requested 50, sidecar is already at 60, and this is not the one exact
	// last-block replay PreflightScannerHeight permits.
	err = client.PreflightScannerHeight(50, strings.Repeat("a", 64), strings.Repeat("b", 64))

	var recoveryErr *ScannerRecoveryRequiredError
	if !errors.As(err, &recoveryErr) {
		t.Fatalf("expected a typed ScannerRecoveryRequiredError, got %v", err)
	}
	if recoveryErr.RequestedHeight != 50 || recoveryErr.NextHeight != 60 || recoveryErr.ResetEpoch != 9 {
		t.Fatalf("unexpected recovery error: %+v", recoveryErr)
	}

	// A cursor-ahead must never be mistaken for a rewind: rewinding the local
	// cursor forward to the sidecar would skip blocks without observing them.
	var rewindErr *ScannerRewindRequiredError
	if errors.As(err, &rewindErr) {
		t.Fatal("cursor-ahead must not surface as a rewind")
	}
}
