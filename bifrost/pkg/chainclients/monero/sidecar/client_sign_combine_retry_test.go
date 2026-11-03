package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestSignCombineRetryReusesExactRequest(t *testing.T) {
	t.Parallel()

	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read combine request: %v", err)
			return
		}
		bodies = append(bodies, append([]byte(nil), body...))
		if len(bodies) < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"signer_busy"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(SignCombineResp{
			TxHash: "tx-hash",
			TxHex:  "deadbeef",
			TxKey:  "11",
		})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	client.signCombineMaxAttempts = 4
	client.signCombineInitialBackoff = time.Millisecond
	client.signCombineMaxBackoff = 2 * time.Millisecond

	req := SignCombineReq{
		MsgID:     "msg-123",
		SessionID: "session-456",
		AllShares: map[string]string{"thorpub-a": "aa", "thorpub-b": "bb"},
	}
	resp, err := client.SignCombineContext(context.Background(), req)
	if err != nil {
		t.Fatalf("sign combine: %v", err)
	}
	if resp.TxHash != "tx-hash" {
		t.Fatalf("unexpected combine response: %+v", resp)
	}
	if len(bodies) != 3 {
		t.Fatalf("expected three attempts, got %d", len(bodies))
	}
	for attempt := 1; attempt < len(bodies); attempt++ {
		if !bytes.Equal(bodies[0], bodies[attempt]) {
			t.Fatalf("attempt %d changed serialized combine request:\nfirst: %s\nretry: %s", attempt+1, bodies[0], bodies[attempt])
		}
	}
	var got SignCombineReq
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatalf("decode captured combine request: %v", err)
	}
	if got.MsgID != req.MsgID || got.SessionID != req.SessionID || !reflect.DeepEqual(got.AllShares, req.AllShares) {
		t.Fatalf("combine identity changed: got %+v want %+v", got, req)
	}
}

func TestSignCombineRetryIsBounded(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal"}`))
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	client.signCombineMaxAttempts = 3
	client.signCombineInitialBackoff = time.Millisecond
	client.signCombineMaxBackoff = time.Millisecond

	_, err = client.SignCombineContext(context.Background(), SignCombineReq{
		MsgID:     "msg-bounded",
		SessionID: "session-bounded",
		AllShares: map[string]string{"thorpub-a": "aa"},
	})
	if err == nil {
		t.Fatal("expected combine failure")
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected exactly three attempts, got %d", attempts.Load())
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusInternalServerError {
		t.Fatalf("expected wrapped final HTTP 500 error, got %v", err)
	}
}

func TestSignCombineDoesNotRetryDeterministicClientError(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_share"}`))
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	client.signCombineMaxAttempts = 5
	client.signCombineInitialBackoff = time.Millisecond

	_, err = client.SignCombineContext(context.Background(), SignCombineReq{
		MsgID:     "msg-invalid",
		SessionID: "session-invalid",
		AllShares: map[string]string{"thorpub-a": "invalid"},
	})
	if err == nil {
		t.Fatal("expected deterministic combine failure")
	}
	if attempts.Load() != 1 {
		t.Fatalf("deterministic HTTP 400 must not be retried, got %d attempts", attempts.Load())
	}
}

func TestSignCombineRetryBackoffIsCancellable(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	responded := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"signer_busy"}`))
		responded <- struct{}{}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	client.signCombineMaxAttempts = 10
	client.signCombineInitialBackoff = time.Hour
	client.signCombineMaxBackoff = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, combineErr := client.SignCombineContext(ctx, SignCombineReq{
			MsgID:     "msg-cancel",
			SessionID: "session-cancel",
			AllShares: map[string]string{"thorpub-a": "aa"},
		})
		done <- combineErr
	}()

	select {
	case <-responded:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("combine attempt did not reach server")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("combine retry did not stop after cancellation")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected cancellation before a retry, got %d attempts", attempts.Load())
	}
}

func TestSignCombineRetriesTransientRateLimit(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(SignCombineResp{TxHash: "tx-hash", TxKey: "11"})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	client.signCombineMaxAttempts = 2
	client.signCombineInitialBackoff = time.Millisecond
	client.signCombineMaxBackoff = time.Millisecond

	_, err = client.SignCombineContext(context.Background(), SignCombineReq{
		MsgID:     "msg-rate-limit",
		SessionID: "session-rate-limit",
		AllShares: map[string]string{"thorpub-a": "aa"},
	})
	if err != nil {
		t.Fatalf("expected transient rate-limit retry to succeed: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected one retry after HTTP 429, got %d attempts", attempts.Load())
	}
}

// Combine may only be retried against a signer that proves it durably replays
// combine. An unproven signer must be broadcast to exactly once, or a retry can
// put the same Monero transaction on the chain twice.
func TestSignCombineRetriesOnlyWhenSignerProvesIdempotency(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		capabilities []string
		wantAttempts int32
	}{
		{name: "unproven signer is single-shot", capabilities: nil, wantAttempts: 1},
		{
			name:         "idempotent signer retries",
			capabilities: []string{CapabilityIdempotentCombine},
			wantAttempts: idempotentSignCombineMaxAttempts,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"internal"}`))
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatalf("new sidecar client: %v", err)
			}
			client.signCombineInitialBackoff = time.Millisecond
			client.signCombineMaxBackoff = time.Millisecond
			client.SetSignCombineIdempotent(Info{Capabilities: tc.capabilities}.HasCapability(CapabilityIdempotentCombine))

			if _, err = client.SignCombineContext(context.Background(), SignCombineReq{
				MsgID:     "msg-idempotency",
				SessionID: "session-idempotency",
			}); err == nil {
				t.Fatal("expected the 5xx combine to fail")
			}
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("combine broadcast %d times, want %d", got, tc.wantAttempts)
			}
		})
	}
}
