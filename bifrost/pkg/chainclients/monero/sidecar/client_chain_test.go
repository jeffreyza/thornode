package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestClientChainHeight(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/chain/height" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(ChainHeightResp{Height: 123})
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	height, err := signer.ChainHeight()
	if err != nil {
		t.Fatalf("GetChainHeight failed: %v", err)
	}
	if height != 123 {
		t.Fatalf("expected height 123, got %d", height)
	}
}

func TestClientChainHeightPrefersDaemonRPC(t *testing.T) {
	t.Parallel()

	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalls.Add(1)
		http.Error(w, "unexpected sidecar call", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json_rpc" {
			http.NotFound(w, r)
			return
		}

		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "get_info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"height": 124,
				},
			})
		default:
			http.Error(w, "unexpected method: "+req.Method, http.StatusBadRequest)
		}
	}))
	defer daemon.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{
			RPCHost: daemon.URL,
		},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	height, err := signer.ChainHeight()
	if err != nil {
		t.Fatalf("GetChainHeight failed: %v", err)
	}
	if height != 123 {
		t.Fatalf("expected daemon-backed height 123, got %d", height)
	}
	if sidecarCalls.Load() != 0 {
		t.Fatalf("expected daemon path to avoid sidecar, got %d sidecar calls", sidecarCalls.Load())
	}
}

func TestClientChainBlockMapsConflictToUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"requested height above tip"}`))
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.ChainBlock(200)
	if !errors.Is(err, ErrUnavailableBlock) {
		t.Fatalf("expected ErrUnavailableBlock, got %v", err)
	}
}

func TestClientChainBlockRejectsMismatchedSidecarHeight(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ChainBlockResp{Height: 43})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}

	_, err = client.ChainBlock(42)
	var mismatch *ChainBlockHeightMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected typed height mismatch, got %v", err)
	}
	if mismatch.RequestedHeight != 42 || mismatch.ReturnedHeight != 43 {
		t.Fatalf("unexpected height mismatch: %+v", mismatch)
	}
}

func TestClientChainBlockPrefersDaemonRPC(t *testing.T) {
	t.Parallel()

	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalls.Add(1)
		http.Error(w, "unexpected sidecar call", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json_rpc" {
			http.NotFound(w, r)
			return
		}

		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "get_info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"height": 56,
				},
			})
		case "get_block":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"block_header": map[string]any{
						"height":    42,
						"reward":    9001,
						"hash":      "hash-42",
						"prev_hash": "hash-41",
					},
					"json": `{"tx_hashes":["tx-1","tx-2"]}`,
				},
			})
		default:
			http.Error(w, "unexpected method: "+req.Method, http.StatusBadRequest)
		}
	}))
	defer daemon.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{
			RPCHost: daemon.URL,
		},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	block, err := signer.ChainBlock(42)
	if err != nil {
		t.Fatalf("GetChainBlock failed: %v", err)
	}
	if block.Height != 42 || block.Reward != 9001 || block.Hash != "hash-42" || block.PreviousHash != "hash-41" {
		t.Fatalf("unexpected block: %#v", block)
	}
	if len(block.TxHashes) != 2 || block.TxHashes[0] != "tx-1" || block.TxHashes[1] != "tx-2" {
		t.Fatalf("unexpected tx hashes: %#v", block.TxHashes)
	}
	if sidecarCalls.Load() != 0 {
		t.Fatalf("expected daemon path to avoid sidecar, got %d sidecar calls", sidecarCalls.Load())
	}
}

func TestClientChainBlockRejectsMismatchedDaemonHeightWithoutFallback(t *testing.T) {
	t.Parallel()

	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sidecarCalls.Add(1)
		http.Error(w, "unexpected sidecar fallback", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "get_info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"height": 100},
			})
		case "get_block":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"block_header": map[string]any{"height": 43},
				},
			})
		default:
			http.Error(w, "unexpected method: "+req.Method, http.StatusBadRequest)
		}
	}))
	defer daemon.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		RPCHost: daemon.URL,
	}, server.URL)
	if err != nil {
		t.Fatalf("new daemon-backed client: %v", err)
	}

	_, err = client.ChainBlock(42)
	var mismatch *ChainBlockHeightMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected typed height mismatch, got %v", err)
	}
	if mismatch.RequestedHeight != 42 || mismatch.ReturnedHeight != 43 {
		t.Fatalf("unexpected height mismatch: %+v", mismatch)
	}
	if sidecarCalls.Load() != 0 {
		t.Fatalf("daemon height mismatch fell back to sidecar: %d calls", sidecarCalls.Load())
	}
}

func TestClientChainHeightFallsBackToSidecar(t *testing.T) {
	t.Parallel()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "daemon unavailable", http.StatusServiceUnavailable)
	}))
	defer daemon.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/chain/height" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(ChainHeightResp{Height: 77})
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{
			RPCHost: daemon.URL,
		},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	height, err := signer.ChainHeight()
	if err != nil {
		t.Fatalf("GetChainHeight failed: %v", err)
	}
	if height != 77 {
		t.Fatalf("expected sidecar fallback height 77, got %d", height)
	}
}

func TestClientChainBlockMapsDaemonTipToUnavailable(t *testing.T) {
	t.Parallel()

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json_rpc" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "get_info" {
			http.Error(w, "unexpected method: "+req.Method, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"height": 10,
			},
		})
	}))
	defer daemon.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected sidecar call", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{
			RPCHost: daemon.URL,
		},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.ChainBlock(10)
	if !errors.Is(err, ErrUnavailableBlock) {
		t.Fatalf("expected ErrUnavailableBlock, got %v", err)
	}
}

func TestClientTxExists(t *testing.T) {
	t.Parallel()

	expectedPath := "/v1/monero/chain/tx/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/exists"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != expectedPath {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(TxExistsResp{Exists: true})
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	exists, err := signer.TxExists("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("TxExists failed: %v", err)
	}
	if !exists {
		t.Fatal("expected tx to exist")
	}
}

func TestClientTxExistsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.TxExists("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err == nil {
		t.Fatal("expected TxExists error")
	}
	if !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientResetScanner(t *testing.T) {
	t.Parallel()

	var receivedVault string
	var receivedBirthday int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/scanner/reset" || r.Method != http.MethodPost {
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		var req struct {
			VaultPubKey    string `json:"vault_pub_key"`
			BirthdayHeight int64  `json:"birthday_height"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		receivedVault = req.VaultPubKey
		receivedBirthday = req.BirthdayHeight
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reset":                true,
			"birthday_height":      req.BirthdayHeight,
			"reset_epoch":          7,
			"expected_next_height": req.BirthdayHeight,
		})
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	err = signer.ResetScanner("thorpub1testvault", 50000)
	if err != nil {
		t.Fatalf("ResetScanner: %v", err)
	}
	if receivedVault != "thorpub1testvault" {
		t.Fatalf("expected vault thorpub1testvault, got %s", receivedVault)
	}
	if receivedBirthday != 50000 {
		t.Fatalf("expected birthday 50000, got %d", receivedBirthday)
	}
}

func TestSyncAtRecoversDurableResetEpochAfterClientRestart(t *testing.T) {
	t.Parallel()

	postCalls := 0
	nextHeight := uint64(50)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
			_ = json.NewEncoder(w).Encode(ScannerState{
				ResetEpoch: 7, NextHeight: &nextHeight, StrictAfterReset: true,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			postCalls++
			var req SyncReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if postCalls == 1 {
				if req.ResetEpoch != nil || req.ExpectedNextHeight != nil {
					t.Errorf("restart request unexpectedly had continuity fields: %+v", req)
				}
				http.Error(w, "reset epoch required", http.StatusConflict)
				return
			}
			if req.ResetEpoch == nil || *req.ResetEpoch != 7 ||
				req.ExpectedNextHeight == nil || *req.ExpectedNextHeight != 50 {
				t.Errorf("retry did not bind recovered continuity: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.SyncAt(50, 60, strings.Repeat("a", 64), strings.Repeat("b", 64), 10); err != nil {
		t.Fatalf("SyncAt continuity recovery: %v", err)
	}
	if postCalls != 2 {
		t.Fatalf("expected one failed request and one bound retry, got %d", postCalls)
	}
}

func TestSyncAtReturnsTypedRewindForOlderAuthenticatedCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		epoch  uint64
		strict bool
	}{
		{name: "strict reset cursor", epoch: 7, strict: true},
		{name: "fresh durable baseline", epoch: 0, strict: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var postCalls atomic.Int32
			nextHeight := uint64(40)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
					_ = json.NewEncoder(w).Encode(ScannerState{
						ResetEpoch:       tt.epoch,
						NextHeight:       &nextHeight,
						StrictAfterReset: tt.strict,
					})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
					postCalls.Add(1)
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":"scanner_cursor_conflict"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			_, err = client.SyncAt(50, 60, strings.Repeat("a", 64), strings.Repeat("b", 64), 10)
			var rewindErr *ScannerRewindRequiredError
			if !errors.As(err, &rewindErr) {
				t.Fatalf("expected typed rewind error, got %v", err)
			}
			if rewindErr.RequestedHeight != 50 || rewindErr.NextHeight != 40 || rewindErr.ResetEpoch != tt.epoch {
				t.Fatalf("unexpected rewind error: %+v", rewindErr)
			}
			if got := postCalls.Load(); got != 1 {
				t.Fatalf("older cursor must not be retried at the wrong height; got %d POSTs", got)
			}
		})
	}
}

func TestSyncAtRetriesAuthenticatedExactReplayOnce(t *testing.T) {
	t.Parallel()

	blockHash := strings.Repeat("a", 64)
	parentHash := strings.Repeat("b", 64)
	nextHeight := uint64(51)
	lastHeight := uint64(50)
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			_ = json.NewEncoder(w).Encode(ScannerState{
				ResetEpoch:     0,
				NextHeight:     &nextHeight,
				LastHeight:     &lastHeight,
				LastBlockHash:  blockHash,
				LastParentHash: parentHash,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			call := postCalls.Add(1)
			var req SyncReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if call == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"scanner_cursor_conflict"}`))
				return
			}
			if req.ResetEpoch == nil || *req.ResetEpoch != 0 ||
				req.ExpectedNextHeight == nil || *req.ExpectedNextHeight != 50 {
				t.Errorf("exact replay retry omitted authenticated continuity: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.SyncAt(50, 60, blockHash, parentHash, 10); err != nil {
		t.Fatalf("exact replay recovery: %v", err)
	}
	if got := postCalls.Load(); got != 2 {
		t.Fatalf("expected one conflict and one exact replay, got %d POSTs", got)
	}
}

func TestSyncAtCursorAheadNonReplayIsNotTypedOrRetried(t *testing.T) {
	t.Parallel()

	nextHeight := uint64(60)
	lastHeight := uint64(59)
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			_ = json.NewEncoder(w).Encode(ScannerState{
				ResetEpoch:     0,
				NextHeight:     &nextHeight,
				LastHeight:     &lastHeight,
				LastBlockHash:  strings.Repeat("c", 64),
				LastParentHash: strings.Repeat("d", 64),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			postCalls.Add(1)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"scanner_cursor_conflict"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.SyncAt(50, 60, strings.Repeat("a", 64), strings.Repeat("b", 64), 10)
	if err == nil {
		t.Fatal("expected cursor-ahead conflict")
	}
	var rewindErr *ScannerRewindRequiredError
	if errors.As(err, &rewindErr) {
		t.Fatalf("cursor ahead must not be reported as a rewind: %+v", rewindErr)
	}
	if !IsHTTPStatus(err, http.StatusConflict) {
		t.Fatalf("expected original cursor-ahead conflict, got %v", err)
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("cursor-ahead request must not be retried, got %d POSTs", got)
	}
}

func TestConcurrentStaleRegistrationCannotDowngradeContinuity(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var closeFirst sync.Once
	var registerCalls atomic.Int32
	var syncCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			call := registerCalls.Add(1)
			if call == 1 {
				closeFirst.Do(func() { close(firstStarted) })
				<-releaseFirst
				next := uint64(100)
				_ = json.NewEncoder(w).Encode(RegisterVaultResp{
					Registered:         true,
					EffectiveBirthday:  100,
					ResetEpoch:         1,
					ExpectedNextHeight: &next,
				})
				return
			}
			next := uint64(40)
			_ = json.NewEncoder(w).Encode(RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  40,
				ResetEpoch:         2,
				ExpectedNextHeight: &next,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			syncCalls.Add(1)
			_ = json.NewEncoder(w).Encode(Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	firstResult := make(chan error, 1)
	go func() {
		_, callErr := client.RegisterVault(RegisterVaultReq{
			VaultPubKey: "first", VaultCreationHeight: 100,
		})
		firstResult <- callErr
	}()
	<-firstStarted
	if _, err = client.RegisterVault(RegisterVaultReq{
		VaultPubKey: "second", VaultCreationHeight: 40,
	}); err != nil {
		t.Fatalf("newer registration: %v", err)
	}
	close(releaseFirst)
	if err = <-firstResult; err != nil {
		t.Fatalf("stale registration response: %v", err)
	}

	continuity, ok := client.cachedScannerContinuity()
	if !ok || continuity.resetEpoch != 2 || continuity.nextHeight != 40 {
		t.Fatalf("stale response downgraded continuity: cached=%+v present=%t", continuity, ok)
	}
	_, err = client.SyncAt(50, 60, strings.Repeat("a", 64), strings.Repeat("b", 64), 10)
	var rewindErr *ScannerRewindRequiredError
	if !errors.As(err, &rewindErr) || rewindErr.ResetEpoch != 2 || rewindErr.NextHeight != 40 {
		t.Fatalf("expected cached epoch-2 rewind, got %v", err)
	}
	if got := syncCalls.Load(); got != 0 {
		t.Fatalf("cached registration rewind should stop the wrong POST, got %d", got)
	}
}

func TestPreflightScannerHeightRequiresExactAheadReplay(t *testing.T) {
	t.Parallel()

	blockHash := strings.Repeat("a", 64)
	parentHash := strings.Repeat("b", 64)
	nextHeight := uint64(51)
	lastHeight := uint64(50)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != scannerStatePath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(ScannerState{
			ResetEpoch:     0,
			NextHeight:     &nextHeight,
			LastHeight:     &lastHeight,
			LastBlockHash:  blockHash,
			LastParentHash: parentHash,
		})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err = client.PreflightScannerHeight(50, blockHash, parentHash); err != nil {
		t.Fatalf("exact replay preflight: %v", err)
	}
	if err = client.PreflightScannerHeight(50, strings.Repeat("c", 64), parentHash); err == nil {
		t.Fatal("expected non-exact cursor-ahead preflight to fail")
	} else {
		var rewindErr *ScannerRewindRequiredError
		if errors.As(err, &rewindErr) {
			t.Fatalf("cursor ahead must not be typed as a rewind: %+v", rewindErr)
		}
	}
}

func TestSyncAtRejectsMalformedScannerContinuityWithoutRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stateBody string
	}{
		{
			name:      "nil next height",
			stateBody: `{"reset_epoch":0,"next_height":null,"strict_after_reset":false}`,
		},
		{
			name:      "next height outside int64 range",
			stateBody: `{"reset_epoch":0,"next_height":9223372036854775808,"strict_after_reset":false}`,
		},
		{
			name: "non-contiguous last height",
			stateBody: `{"reset_epoch":0,"next_height":51,"last_height":40,` +
				`"last_block_hash":"` + strings.Repeat("a", 64) + `",` +
				`"last_parent_hash":"` + strings.Repeat("b", 64) + `",` +
				`"strict_after_reset":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var postCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
					_, _ = w.Write([]byte(tt.stateBody))
				case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
					postCalls.Add(1)
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":"scanner_cursor_conflict"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			_, err = client.SyncAt(50, 60, strings.Repeat("a", 64), strings.Repeat("b", 64), 10)
			if err == nil {
				t.Fatal("expected malformed continuity to fail closed")
			}
			var rewindErr *ScannerRewindRequiredError
			if errors.As(err, &rewindErr) {
				t.Fatalf("malformed continuity must not become a rewind instruction: %+v", rewindErr)
			}
			if got := postCalls.Load(); got != 1 {
				t.Fatalf("malformed continuity must not be retried, got %d POSTs", got)
			}
		})
	}
}

func TestRegisterVaultRejectsNilContinuityCursor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"registered":true,"effective_birthday":1,"reset_epoch":0}`))
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.RegisterVault(RegisterVaultReq{VaultPubKey: "vault"}); err == nil {
		t.Fatal("expected nil registration cursor to fail closed")
	}
	if _, ok := client.cachedScannerContinuity(); ok {
		t.Fatal("invalid registration response must not populate continuity cache")
	}
}

func TestRegisterVaultRestoresRetainedBirthdayAfterRestart(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RegisterVaultReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.VaultCreationHeight != 559 {
			http.Error(w, "unexpected birthday", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{
			"registered":false,
			"effective_birthday":504,
			"reset_epoch":2,
			"expected_next_height":504
		}`))
	}))
	defer server.Close()

	for attempt := 0; attempt < 2; attempt++ {
		// A fresh client models Bifrost restarting with no in-memory continuity.
		client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
		if err != nil {
			t.Fatalf("new client after restart %d: %v", attempt, err)
		}
		resp, err := client.RegisterVault(RegisterVaultReq{
			VaultPubKey:         "vault",
			VaultCreationHeight: 559,
		})
		if err != nil {
			t.Fatalf("restore registration after restart %d: %v", attempt, err)
		}
		if resp.EffectiveBirthday != 504 {
			t.Fatalf("effective birthday = %d, want 504", resp.EffectiveBirthday)
		}
		state, ok := client.CachedScannerState()
		if !ok || state.NextHeight == nil || *state.NextHeight != 504 ||
			state.ResetEpoch != 2 || !state.StrictAfterReset {
			t.Fatalf("restart did not restore retained continuity: %+v present=%t", state, ok)
		}
	}
}

func TestRegisterVaultRejectsUnsafeEffectiveBirthday(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		birthday int64
		body     string
	}{
		{
			name:     "missing",
			birthday: 559,
			body:     `{"registered":false,"reset_epoch":0,"expected_next_height":600}`,
		},
		{
			name:     "zero",
			birthday: 559,
			body:     `{"registered":false,"effective_birthday":0,"reset_epoch":0,"expected_next_height":600}`,
		},
		{
			name:     "later than candidate",
			birthday: 559,
			body:     `{"registered":false,"effective_birthday":560,"reset_epoch":0,"expected_next_height":600}`,
		},
		{
			name:     "outside int64 scanner range",
			birthday: 559,
			body:     `{"registered":false,"effective_birthday":9223372036854775808,"reset_epoch":0,"expected_next_height":600}`,
		},
		{
			name:     "reset without cursor",
			birthday: 0,
			body:     `{"registered":false,"effective_birthday":0,"reset_epoch":1}`,
		},
		{
			name:     "null birthday",
			birthday: 559,
			body:     `{"registered":false,"effective_birthday":null,"reset_epoch":0,"expected_next_height":600}`,
		},
		{
			name:     "null reset epoch",
			birthday: 559,
			body:     `{"registered":false,"effective_birthday":504,"reset_epoch":null,"expected_next_height":600}`,
		},
		{
			name:     "cursor outside int64 scanner range",
			birthday: 559,
			body:     `{"registered":false,"effective_birthday":504,"reset_epoch":0,"expected_next_height":9223372036854775808}`,
		},
		{
			name:     "negative requested birthday",
			birthday: -1,
			body:     `{"registered":false,"effective_birthday":504,"reset_epoch":0,"expected_next_height":600}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			if _, err = client.RegisterVault(RegisterVaultReq{
				VaultPubKey:         "vault",
				VaultCreationHeight: test.birthday,
			}); err == nil {
				t.Fatal("expected unsafe effective birthday to fail closed")
			}
			if _, ok := client.cachedScannerContinuity(); ok {
				t.Fatal("unsafe registration response populated continuity cache")
			}
			wantCalls := int32(1)
			if test.birthday < 0 {
				wantCalls = 0
			}
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("registration calls = %d, want %d", got, wantCalls)
			}
		})
	}
}

func TestRegisterVaultAcceptsBoundaryBirthdays(t *testing.T) {
	for _, birthday := range []int64{1, math.MaxInt64} {
		t.Run(fmt.Sprintf("birthday_%d", birthday), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req RegisterVaultReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VaultCreationHeight != birthday {
					http.Error(w, "unexpected registration birthday", http.StatusBadRequest)
					return
				}
				next := uint64(birthday)
				_ = json.NewEncoder(w).Encode(RegisterVaultResp{
					EffectiveBirthday: next, ExpectedNextHeight: &next,
				})
			}))
			defer server.Close()
			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.RegisterVault(RegisterVaultReq{VaultPubKey: "vault", VaultCreationHeight: birthday})
			if err != nil || resp.EffectiveBirthday != uint64(birthday) {
				t.Fatalf("valid boundary birthday rejected: resp=%+v err=%v", resp, err)
			}
			state, known := client.CachedScannerState()
			if !known || state.NextHeight == nil || *state.NextHeight != uint64(birthday) {
				t.Fatalf("boundary registration did not seed continuity: %+v known=%t", state, known)
			}
		})
	}
}

func TestScannerStateRejectsInvalidRefreshWithoutLosingContinuity(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"missing epoch", `{"next_height":50,"strict_after_reset":false}`},
		{"missing strict mode", `{"reset_epoch":0,"next_height":50}`},
		{"missing cursor", `{"reset_epoch":1,"strict_after_reset":true}`},
		{"overflow cursor", `{"reset_epoch":1,"next_height":9223372036854775808,"strict_after_reset":true}`},
		{"inconsistent reset", `{"reset_epoch":1,"next_height":50,"strict_after_reset":false}`},
		{"unbound hash", `{"reset_epoch":1,"next_height":50,"last_block_hash":"` + strings.Repeat("a", 64) + `","strict_after_reset":true}`},
		{"noncontiguous height", `{"reset_epoch":1,"next_height":50,"last_height":48,"strict_after_reset":true}`},
		{"invalid block hash", `{"reset_epoch":1,"next_height":50,"last_height":49,"last_block_hash":"` + strings.Repeat("z", 64) + `","last_parent_hash":"` + strings.Repeat("b", 64) + `","strict_after_reset":true}`},
		{"missing parent hash", `{"reset_epoch":1,"next_height":50,"last_height":49,"last_block_hash":"` + strings.Repeat("a", 64) + `","strict_after_reset":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stateReads, syncCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case scannerStatePath:
					if stateReads.Add(1) == 2 {
						_, _ = w.Write([]byte(test.body))
						return
					}
					next, last := uint64(50), uint64(49)
					_ = json.NewEncoder(w).Encode(ScannerState{
						NextHeight: &next, LastHeight: &last,
						LastBlockHash: strings.Repeat("a", 64), LastParentHash: strings.Repeat("b", 64),
					})
				case "/v1/monero/scanner/sync":
					syncCalls.Add(1)
					var req SyncReq
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ResetEpoch == nil ||
						*req.ResetEpoch != 0 || req.ExpectedNextHeight == nil || *req.ExpectedNextHeight != 50 {
						http.Error(w, "invalid continuity binding", http.StatusConflict)
						return
					}
					_ = json.NewEncoder(w).Encode(Transfers{})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.ScannerState(); err != nil {
				t.Fatal(err)
			}
			before, known := client.cachedScannerContinuity()
			if !known {
				t.Fatal("valid state did not seed continuity")
			}
			if _, err = client.ScannerState(); err == nil {
				t.Fatal("invalid state refresh was accepted")
			}
			if after, ok := client.cachedScannerContinuity(); !ok || after != before {
				t.Fatalf("invalid refresh replaced validated continuity: before=%+v after=%+v", before, after)
			}
			if _, err = client.ScannerState(); err != nil {
				t.Fatalf("repaired state could not be retried: %v", err)
			}
			if _, err = client.SyncAt(50, 60, strings.Repeat("c", 64), strings.Repeat("a", 64), 10); err != nil {
				t.Fatalf("repaired continuity could not resume scanning: %v", err)
			}
			if stateReads.Load() != 3 || syncCalls.Load() != 1 {
				t.Fatalf("unexpected retry work: state=%d sync=%d", stateReads.Load(), syncCalls.Load())
			}
		})
	}
}

func TestRegisterVaultAllowsRecoveryOnlyRegistrationWithoutCursor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"registered":true,
			"effective_birthday":0,
			"reset_epoch":0
		}`))
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.RegisterVault(RegisterVaultReq{VaultPubKey: "recovery-vault"}); err != nil {
		t.Fatalf("recovery-only registration: %v", err)
	}
	if _, ok := client.cachedScannerContinuity(); ok {
		t.Fatal("recovery-only registration invented scanner continuity")
	}
}

func TestNewClientAllowsAuthenticatedMocknetHTTP(t *testing.T) {
	t.Parallel()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{
			ChainNetwork:              "mocknet",
			SignerAuthorizationBearer: "test-token",
			SignerExpectedNetwork:     "mocknet",
			SignerAllowInsecureHTTP:   true,
		},
		"http://xmr-frost-signer:8283",
	)
	if err != nil {
		t.Fatalf("expected mocknet service endpoint to be accepted, got %v", err)
	}
	if signer.endpoint != "http://xmr-frost-signer:8283" {
		t.Fatalf("expected signer endpoint to be preserved, got %q", signer.endpoint)
	}
}

func TestNewClientRejectsNonLoopbackWithoutAuth(t *testing.T) {
	t.Setenv("SIGNER_SEED_PHRASE", "test mocknet signer seed phrase")

	_, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{
			ChainNetwork:            "mocknet",
			SignerExpectedNetwork:   "mocknet",
			SignerAllowInsecureHTTP: true,
		},
		"http://xmr-frost-signer:8283",
	)
	if err == nil {
		t.Fatal("expected non-loopback endpoint without explicit auth to be rejected")
	}
	if !strings.Contains(err.Error(), "requires signer_authorization_bearer or signer_username/signer_password") {
		t.Fatalf("unexpected error: %v", err)
	}
}
