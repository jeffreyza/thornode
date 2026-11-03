package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog/log"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestFetchTxsCallsScannerSyncWithBlockMetadata(t *testing.T) {
	t.Parallel()

	var captured sidecar.SyncReq
	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
			writeTestScannerState(t, w, 100)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 150})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			rawHeight := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/")
			height, err := strconv.ParseInt(rawHeight, 10, 64)
			if err != nil {
				http.Error(w, "invalid height", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
				Height:       height,
				Reward:       defaultCoinbaseValue + 200000,
				Hash:         testBlockHash(height),
				PreviousHash: testBlockHash(height - 1),
				TxHashes:     []string{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	bridge := &scannerMemoBridge{
		asgards: ttypes.Vaults{},
	}
	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)

	// Bound the call so a future merge cannot combine this function's narrower
	// continuity lock with an outer scanMu lock and hang the whole test package.
	fetchDone := make(chan error, 1)
	go func() {
		_, fetchErr := scanner.FetchTxs(100, 150)
		fetchDone <- fetchErr
	}()
	select {
	case err = <-fetchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("FetchTxs deadlocked while acquiring the scanner continuity lock")
	}
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if scanner.CurrentHeight() != 100 {
		t.Fatalf("expected current height 100 after successful scan, got %d", scanner.CurrentHeight())
	}
	if captured.Height != 100 {
		t.Fatalf("expected height 100, got %d", captured.Height)
	}
	if captured.TipHeight != 150 {
		t.Fatalf("expected tip height 150, got %d", captured.TipHeight)
	}
	if captured.BlockHash != testBlockHash(100) {
		t.Fatalf("expected block hash %s, got %s", testBlockHash(100), captured.BlockHash)
	}
	if captured.ParentHash != testBlockHash(99) {
		t.Fatalf("expected parent hash %s, got %s", testBlockHash(99), captured.ParentHash)
	}
	if captured.ReorgWindow != 72 {
		t.Fatalf("expected reorg window 72, got %d", captured.ReorgWindow)
	}
}

func TestFetchTxsMapsScannerSyncHashMismatchToUnavailableBlock(t *testing.T) {
	t.Parallel()

	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
			writeTestScannerState(t, w, 200)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 220})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			rawHeight := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/")
			height, err := strconv.ParseInt(rawHeight, 10, 64)
			if err != nil {
				http.Error(w, "invalid height", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
				Height:       height,
				Reward:       defaultCoinbaseValue + 200000,
				Hash:         testBlockHash(height),
				PreviousHash: testBlockHash(height - 1),
				TxHashes:     []string{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "scanner sync hash mismatch at height 200",
				"code":  "scanner_hash_mismatch",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{asgards: ttypes.Vaults{}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	_, err = scanner.FetchTxs(200, 220)
	if !errors.Is(err, btypes.ErrUnavailableBlock) {
		t.Fatalf("expected unavailable block error, got %v", err)
	}
	if scanner.CurrentHeight() != 0 {
		t.Fatalf("expected current height not to advance after failed scan, got %d", scanner.CurrentHeight())
	}
}

func TestGetHeightDoesNotAdvanceProcessedHeight(t *testing.T) {
	t.Parallel()

	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height" {
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 321})
			return
		}
		http.NotFound(w, r)
	}))
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72)

	height, err := scanner.GetHeight()
	if err != nil {
		t.Fatalf("get height: %v", err)
	}
	if height != 321 {
		t.Fatalf("expected daemon height 321, got %d", height)
	}
	if scanner.CurrentHeight() != 0 {
		t.Fatalf("expected processed height to remain 0, got %d", scanner.CurrentHeight())
	}
}

func TestFetchTxsFailsClosedWithoutSidecar(t *testing.T) {
	t.Parallel()

	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	_, err := scanner.FetchTxs(100, 100)
	if err == nil {
		t.Fatal("expected scanner to fail without sidecar")
	}
	if !strings.Contains(err.Error(), "requires sidecar") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchTxsPropagatesSidecarUnavailable(t *testing.T) {
	t.Parallel()

	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/") {
			http.Error(w, "daemon unavailable", http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72)

	_, err = scanner.FetchTxs(300, 300)
	if err == nil {
		t.Fatal("expected scanner error")
	}
	if errors.Is(err, btypes.ErrUnavailableBlock) {
		t.Fatalf("unexpected unavailable-block mapping for 503: %v", err)
	}
	if !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("expected propagated 503 detail, got %v", err)
	}
}
