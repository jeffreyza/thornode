package sidecar

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestRewindScannerForReorgSuccess(t *testing.T) {
	t.Parallel()

	anchorHash := strings.Repeat("a", 64)
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			writeScannerRewindTestState(w, 3, 100, 99, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			postCalls.Add(1)
			var req ScannerRewindReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Network != "mocknet" || req.ExpectedResetEpoch != 3 ||
				req.ExpectedNextHeight != 100 || req.TargetNextHeight != 90 ||
				req.AnchorHeight != 89 || req.AnchorHash != anchorHash {
				t.Errorf("unexpected rewind request: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(ScannerRewindResp{
				Rewound:            true,
				PreviousNextHeight: 100,
				NextHeight:         90,
				ResetEpoch:         4,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	resp, err := client.RewindScannerForReorg(90, 89, strings.ToUpper(anchorHash))
	if err != nil {
		t.Fatalf("rewind scanner: %v", err)
	}
	if !resp.Rewound || resp.PreviousNextHeight != 100 || resp.NextHeight != 90 || resp.ResetEpoch != 4 {
		t.Fatalf("unexpected rewind response: %+v", resp)
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("expected one rewind POST, got %d", got)
	}
	continuity, ok := client.cachedScannerContinuity()
	if !ok || continuity.resetEpoch != 4 || continuity.nextHeight != 90 ||
		!continuity.hasLastHeight || continuity.lastHeight != 89 || continuity.lastBlockHash != anchorHash {
		t.Fatalf("rewind continuity was not cached: %+v present=%t", continuity, ok)
	}
}

func TestRewindScannerForReorgConflict(t *testing.T) {
	t.Parallel()

	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			writeScannerRewindTestState(w, 3, 100, 99, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			postCalls.Add(1)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"scanner_rewind_conflict"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	_, err := client.RewindScannerForReorg(90, 89, strings.Repeat("a", 64))
	if !IsHTTPStatus(err, http.StatusConflict) {
		t.Fatalf("expected rewind conflict, got %v", err)
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("conflict must not be retried, got %d POSTs", got)
	}
	if client.pendingScannerRewind != nil {
		t.Fatal("definitive conflict must clear pending rewind intent")
	}
}

func TestRewindScannerForReorgRejectsBeyondSafetyBound(t *testing.T) {
	t.Parallel()

	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			writeScannerRewindTestState(w, 3, 1000, 999, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			postCalls.Add(1)
			_ = json.NewEncoder(w).Encode(ScannerRewindResp{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	if _, err := client.RewindScannerForReorg(200, 199, strings.Repeat("a", 64)); err == nil {
		t.Fatal("expected over-bound rewind to fail")
	}
	if got := postCalls.Load(); got != 0 {
		t.Fatalf("over-bound rewind reached mutation endpoint %d times", got)
	}
}

func TestRewindScannerForReorgRejectsMalformedResponse(t *testing.T) {
	t.Parallel()

	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			writeScannerRewindTestState(w, 3, 100, 99, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			postCalls.Add(1)
			_, _ = w.Write([]byte(`{"rewound":true,"previous_next_height":100,"next_height":90}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	if _, err := client.RewindScannerForReorg(90, 89, strings.Repeat("a", 64)); err == nil {
		t.Fatal("expected malformed rewind response to fail closed")
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("malformed response must not be retried as a transport loss, got %d POSTs", got)
	}
	if client.pendingScannerRewind == nil {
		t.Fatal("ambiguous malformed success response should retain exact retry intent")
	}
}

func TestRewindScannerForReorgRetriesLostCommittedResponseIdempotently(t *testing.T) {
	t.Parallel()

	anchorHash := strings.Repeat("a", 64)
	parentHash := strings.Repeat("b", 64)
	var committed atomic.Bool
	var postCalls atomic.Int32
	var getCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			getCalls.Add(1)
			if committed.Load() {
				writeScannerRewindTestState(w, 4, 90, 89, anchorHash, parentHash)
				return
			}
			writeScannerRewindTestState(w, 3, 100, 99, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			call := postCalls.Add(1)
			var req ScannerRewindReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.ExpectedResetEpoch != 3 || req.ExpectedNextHeight != 100 ||
				req.TargetNextHeight != 90 || req.AnchorHeight != 89 || req.AnchorHash != anchorHash {
				t.Errorf("retry changed compare-and-set request: %+v", req)
			}
			if call == 1 {
				committed.Store(true)
				closeRewindTestConnection(t, w)
				return
			}
			_ = json.NewEncoder(w).Encode(ScannerRewindResp{
				Rewound:            false,
				PreviousNextHeight: 100,
				NextHeight:         90,
				ResetEpoch:         4,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	resp, err := client.RewindScannerForReorg(90, 89, anchorHash)
	if err != nil {
		t.Fatalf("idempotent rewind recovery: %v", err)
	}
	if resp.Rewound {
		t.Fatalf("lost-response retry should report idempotent completion: %+v", resp)
	}
	if got := postCalls.Load(); got != 2 {
		t.Fatalf("expected one lost response and one exact retry, got %d POSTs", got)
	}
	if got := getCalls.Load(); got != 2 {
		t.Fatalf("expected initial and proof state reads, got %d", got)
	}
	if client.pendingScannerRewind != nil {
		t.Fatal("verified idempotent completion must clear pending intent")
	}
}

func TestRewindScannerForReorgRecognizesDurableCompletionAfterClientRestart(t *testing.T) {
	t.Parallel()

	anchorHash := strings.Repeat("a", 64)
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			writeScannerRewindTestState(w, 4, 90, 89, anchorHash, strings.Repeat("b", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			postCalls.Add(1)
			_ = json.NewEncoder(w).Encode(ScannerRewindResp{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// This is intentionally a fresh Client with no in-memory pending receipt.
	client := newScannerRewindTestClient(t, server.URL)
	resp, err := client.RewindScannerForReorg(90, 89, anchorHash)
	if err != nil {
		t.Fatalf("recognize durable rewind after restart: %v", err)
	}
	if resp.Rewound || resp.NextHeight != 90 || resp.ResetEpoch != 4 {
		t.Fatalf("unexpected durable-completion response: %+v", resp)
	}
	if got := postCalls.Load(); got != 0 {
		t.Fatalf("already-proven durable rewind must not POST, got %d calls", got)
	}
}

func TestRewindScannerForReorgSerializesSyncAndSeedsContinuity(t *testing.T) {
	t.Parallel()

	anchorHash := strings.Repeat("a", 64)
	rewindStarted := make(chan struct{})
	releaseRewind := make(chan struct{})
	syncRequest := make(chan SyncReq, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			writeScannerRewindTestState(w, 3, 100, 99, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			close(rewindStarted)
			<-releaseRewind
			_ = json.NewEncoder(w).Encode(ScannerRewindResp{
				Rewound:            true,
				PreviousNextHeight: 100,
				NextHeight:         90,
				ResetEpoch:         4,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			var req SyncReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			syncRequest <- req
			_ = json.NewEncoder(w).Encode(Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	rewindResult := make(chan error, 1)
	go func() {
		_, err := client.RewindScannerForReorg(90, 89, anchorHash)
		rewindResult <- err
	}()
	<-rewindStarted

	syncResult := make(chan error, 1)
	go func() {
		_, err := client.SyncAt(90, 100, strings.Repeat("e", 64), anchorHash, 10)
		syncResult <- err
	}()
	select {
	case req := <-syncRequest:
		t.Fatalf("SyncAt raced an in-flight rewind: %+v", req)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRewind)
	if err := <-rewindResult; err != nil {
		t.Fatalf("rewind scanner: %v", err)
	}
	if err := <-syncResult; err != nil {
		t.Fatalf("sync after rewind: %v", err)
	}
	req := <-syncRequest
	if req.ResetEpoch == nil || *req.ResetEpoch != 4 ||
		req.ExpectedNextHeight == nil || *req.ExpectedNextHeight != 90 {
		t.Fatalf("sync did not bind post-rewind continuity: %+v", req)
	}
}

// A 5xx is neither ambiguous (no replay) nor definitive (the latch is retained),
// so the pending rewind stays set. A deeper reorg then supersedes it. Because the
// authenticated cursor provably never moved, the new rewind must still go through
// rather than latching reorg recovery shut for the life of the process.
func TestRewindScannerForReorgSupersedesPendingRewindWhenCursorNeverMoved(t *testing.T) {
	t.Parallel()

	firstAnchor := strings.Repeat("a", 64)
	secondAnchor := strings.Repeat("b", 64)
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == scannerStatePath:
			// The cursor never moves: epoch 3 / next 100 throughout.
			writeScannerRewindTestState(w, 3, 100, 99, strings.Repeat("c", 64), strings.Repeat("d", 64))
		case r.Method == http.MethodPost && r.URL.Path == scannerRewindPath:
			if postCalls.Add(1) == 1 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			var req ScannerRewindReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.TargetNextHeight != 85 || req.AnchorHeight != 84 || req.AnchorHash != secondAnchor {
				t.Errorf("second rewind did not carry the superseding anchor: %+v", req)
			}
			if req.ExpectedResetEpoch != 3 || req.ExpectedNextHeight != 100 {
				t.Errorf("second rewind did not bind the unmoved cursor: %+v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ScannerRewindResp{
				Rewound:            true,
				PreviousNextHeight: 100,
				NextHeight:         85,
				ResetEpoch:         4,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	if _, err := client.RewindScannerForReorg(90, 89, firstAnchor); err == nil {
		t.Fatal("expected the 5xx rewind to fail")
	}
	if client.pendingScannerRewind == nil {
		t.Fatal("a 5xx rewind must latch a pending result")
	}

	resp, err := client.RewindScannerForReorg(85, 84, secondAnchor)
	if err != nil {
		t.Fatalf("superseding rewind was refused after an unresolved pending rewind: %v", err)
	}
	if !resp.Rewound || resp.NextHeight != 85 || resp.ResetEpoch != 4 {
		t.Fatalf("unexpected superseding rewind response: %+v", resp)
	}
	if client.pendingScannerRewind != nil {
		t.Fatal("a committed rewind must clear the pending latch")
	}
}

func TestRewindScannerForReorgRejectsNonHexAnchor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an invalid anchor hash must never reach the sidecar: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := newScannerRewindTestClient(t, server.URL)
	for _, anchor := range []string{"", "not-hex", strings.Repeat("a", 63), strings.Repeat("z", 64)} {
		if _, err := client.RewindScannerForReorg(90, 89, anchor); err == nil {
			t.Fatalf("expected anchor %q to be rejected client-side", anchor)
		}
	}
}

// RewindScanner seeds the epoch-ordered continuity cache directly, so a
// response that did not advance the reset epoch would be silently discarded by
// mergeScannerContinuity while the call still reported success. It must be
// rejected loudly instead, and it must not seed the cache.
func TestRewindScannerRejectsResponseWithoutEpochAdvance(t *testing.T) {
	t.Parallel()

	anchorHash := strings.Repeat("a", 64)
	newRewindServer := func(t *testing.T, resp ScannerRewindResp) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method != http.MethodPost || r.URL.Path != scannerRewindPath {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		t.Cleanup(server.Close)
		return server
	}

	resp := ScannerRewindResp{
		Rewound:            true,
		PreviousNextHeight: 100,
		NextHeight:         90,
		ResetEpoch:         4,
	}
	server := newRewindServer(t, resp)

	req := ScannerRewindReq{
		ExpectedResetEpoch: 3,
		ExpectedNextHeight: 100,
		TargetNextHeight:   90,
		AnchorHeight:       89,
		AnchorHash:         anchorHash,
	}

	client := newScannerRewindTestClient(t, server.URL)
	got, err := client.RewindScanner(req)
	if err != nil {
		t.Fatalf("rewind scanner: %v", err)
	}
	if got != resp {
		t.Fatalf("unexpected rewind response: %+v", got)
	}
	continuity, ok := client.cachedScannerContinuity()
	if !ok || continuity.resetEpoch != 4 || continuity.nextHeight != 90 ||
		!continuity.strictAfterReset || !continuity.hasLastHeight ||
		continuity.lastHeight != 89 || continuity.lastBlockHash != anchorHash {
		t.Fatalf("rewind continuity was not cached: %+v present=%t", continuity, ok)
	}

	for _, tc := range []struct {
		name string
		resp ScannerRewindResp
	}{
		{
			name: "stale epoch",
			resp: ScannerRewindResp{Rewound: true, PreviousNextHeight: 100, NextHeight: 90, ResetEpoch: 3},
		},
		{
			name: "previous cursor mismatch",
			resp: ScannerRewindResp{Rewound: true, PreviousNextHeight: 99, NextHeight: 90, ResetEpoch: 4},
		},
		{
			name: "wrong target",
			resp: ScannerRewindResp{Rewound: true, PreviousNextHeight: 100, NextHeight: 95, ResetEpoch: 4},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staleServer := newRewindServer(t, tc.resp)
			stale := newScannerRewindTestClient(t, staleServer.URL)
			if _, staleErr := stale.RewindScanner(req); staleErr == nil {
				t.Fatalf("expected invalid rewind continuity error")
			}
			if staleContinuity, cached := stale.cachedScannerContinuity(); cached {
				t.Fatalf("invalid rewind response seeded the cursor: %+v", staleContinuity)
			}
		})
	}
}

func TestRewindScannerUsesExpectedSignerNetwork(t *testing.T) {
	t.Parallel()

	anchorHash := strings.Repeat("a", 64)
	var received ScannerRewindReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != scannerRewindPath {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(ScannerRewindResp{
			Rewound:            true,
			PreviousNextHeight: received.ExpectedNextHeight,
			NextHeight:         received.TargetNextHeight,
			ResetEpoch:         received.ExpectedResetEpoch + 1,
		})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		// A real Monero daemon still reports mainnet in chainnet deployments.
		// The signer protocol identity is deliberately independent.
		ChainNetwork:          "mainnet",
		SignerExpectedNetwork: "chainnet",
	}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	req := ScannerRewindReq{
		ExpectedResetEpoch: 3,
		ExpectedNextHeight: 100,
		TargetNextHeight:   90,
		AnchorHeight:       89,
		AnchorHash:         anchorHash,
	}
	_, err = client.RewindScanner(req)
	if err != nil {
		t.Fatalf("rewind scanner: %v", err)
	}
	if received.Network != "chainnet" {
		t.Fatalf("rewind network = %q, want chainnet", received.Network)
	}

	req.Network = "mainnet"
	_, err = client.RewindScanner(req)
	if err == nil || !strings.Contains(err.Error(), "does not match expected sidecar network") {
		t.Fatalf("expected network mismatch, got %v", err)
	}
}

func newScannerRewindTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerExpectedNetwork: "mocknet",
	}, endpoint)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func writeScannerRewindTestState(
	w http.ResponseWriter,
	epoch,
	nextHeight,
	lastHeight uint64,
	lastBlockHash,
	lastParentHash string,
) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ScannerState{
		ResetEpoch:       epoch,
		NextHeight:       &nextHeight,
		LastHeight:       &lastHeight,
		LastBlockHash:    lastBlockHash,
		LastParentHash:   lastParentHash,
		StrictAfterReset: epoch > 0,
	})
}

func closeRewindTestConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("test server does not support connection hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack committed response: %v", err)
	}
	closeHijackedRewindConnection(t, conn, rw)
}

func closeHijackedRewindConnection(t *testing.T, conn net.Conn, rw *bufio.ReadWriter) {
	t.Helper()
	if rw != nil {
		_ = rw.Flush()
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close hijacked response: %v", err)
	}
}
