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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

func newSignPartialRetryClient(t *testing.T, url string) *Client {
	t.Helper()
	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, url)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	client.signPartialInitialBackoff = time.Millisecond
	client.signPartialMaxBackoff = 2 * time.Millisecond
	return client
}

// rateLimitedPartialServer answers sign/partial with 429 for the first
// rateLimited requests and a signature share afterwards, recording every body.
func rateLimitedPartialServer(t *testing.T, rateLimited int) (*httptest.Server, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read partial request: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		count := len(bodies)
		mu.Unlock()
		if count <= rateLimited {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(SignPartialResp{SignatureShare: "share-hex"})
	}))
	t.Cleanup(server.Close)
	return server, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), bodies...)
	}
}

func TestSignPartialRetriesRateLimitWithExactRequest(t *testing.T) {
	t.Parallel()

	server, bodies := rateLimitedPartialServer(t, 2)
	client := newSignPartialRetryClient(t, server.URL)

	req := SignPartialReq{
		MsgID:           "msg-123",
		SessionID:       "session-456",
		AllPreprocesses: map[string]string{"thorpub-a": "aa", "thorpub-b": "bb"},
	}
	resp, err := client.SignPartialContext(context.Background(), req)
	if err != nil {
		t.Fatalf("sign partial: %v", err)
	}
	if resp.SignatureShare != "share-hex" {
		t.Fatalf("unexpected partial response: %+v", resp)
	}
	got := bodies()
	if len(got) != 3 {
		t.Fatalf("expected three attempts, got %d", len(got))
	}
	for attempt := 1; attempt < len(got); attempt++ {
		if !bytes.Equal(got[0], got[attempt]) {
			t.Fatalf("attempt %d changed serialized partial request:\nfirst: %s\nretry: %s", attempt+1, got[0], got[attempt])
		}
	}
	var decoded SignPartialReq
	if err := json.Unmarshal(got[0], &decoded); err != nil {
		t.Fatalf("decode captured partial request: %v", err)
	}
	if decoded.MsgID != req.MsgID || decoded.SessionID != req.SessionID || !reflect.DeepEqual(decoded.AllPreprocesses, req.AllPreprocesses) {
		t.Fatalf("partial identity changed: got %+v want %+v", decoded, req)
	}
}

func TestFrostAdapterSignPartialRetriesRateLimit(t *testing.T) {
	t.Parallel()

	server, bodies := rateLimitedPartialServer(t, 1)
	client := newSignPartialRetryClient(t, server.URL)
	stop := make(chan struct{})
	adapter := NewFrostAdapterWithStop(client, stop)

	resp, err := adapter.FrostSignPartial(tssp.FrostSidecarPartialReq{
		MsgID:           "msg-adapter",
		SessionID:       "session-adapter",
		AllPreprocesses: map[string]string{"thorpub-a": "aa"},
	})
	if err != nil {
		t.Fatalf("frost sign partial: %v", err)
	}
	if resp.SignatureShare != "share-hex" {
		t.Fatalf("unexpected partial response: %+v", resp)
	}
	if got := len(bodies()); got != 2 {
		t.Fatalf("expected two attempts, got %d", got)
	}
}

func TestSignPartialRateLimitRetryIsBounded(t *testing.T) {
	t.Parallel()

	server, bodies := rateLimitedPartialServer(t, 1000)
	client := newSignPartialRetryClient(t, server.URL)
	client.signPartialMaxAttempts = 3

	_, err := client.SignPartialContext(context.Background(), SignPartialReq{
		MsgID:           "msg-bounded",
		SessionID:       "session-bounded",
		AllPreprocesses: map[string]string{"thorpub-a": "aa"},
	})
	if err == nil {
		t.Fatal("expected partial failure")
	}
	if got := len(bodies()); got != 3 {
		t.Fatalf("expected exactly three attempts, got %d", got)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusTooManyRequests {
		t.Fatalf("expected wrapped final HTTP 429 error, got %v", err)
	}
}

func TestSignPartialDoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusConflict,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := newSignPartialRetryClient(t, server.URL)

			_, err := client.SignPartialContext(context.Background(), SignPartialReq{
				MsgID:     "msg-no-retry",
				SessionID: "session-no-retry",
			})
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status != status {
				t.Fatalf("expected HTTP %d error, got %v", status, err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("expected a single attempt for status %d, got %d", status, attempts.Load())
			}
		})
	}
}

func TestSignPartialRateLimitRetryStopsOnCancel(t *testing.T) {
	t.Parallel()

	server, bodies := rateLimitedPartialServer(t, 1000)
	client := newSignPartialRetryClient(t, server.URL)
	client.signPartialInitialBackoff = time.Hour
	client.signPartialMaxBackoff = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.SignPartialContext(ctx, SignPartialReq{MsgID: "msg-cancel", SessionID: "session-cancel"})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(bodies()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sign partial retry did not stop on cancellation")
	}
	if got := len(bodies()); got != 1 {
		t.Fatalf("expected one attempt before cancellation, got %d", got)
	}
}
