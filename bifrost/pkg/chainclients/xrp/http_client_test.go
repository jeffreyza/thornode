package xrp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/rs/zerolog"
)

func newTestRetryClient(timeout time.Duration, maxRetries int) *retryHTTPClient {
	c := newRetryHTTPClient(timeout, maxRetries, zerolog.Nop())
	c.backoff = 10 * time.Millisecond // keep tests fast
	return c
}

func postJSON(t *testing.T, c *retryHTTPClient, url string, body string) (*http.Response, error) {
	t.Helper()
	// bytes.Reader bodies populate GetBody, matching how xrpl-go builds requests.
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("building request: %s", err)
	}
	return c.Do(req)
}

// A request slower than the old library default (1s) but within the configured
// timeout must COMPLETE — the core of the fix: latency tails become successful
// responses instead of aborted requests.
func TestRetryHTTPClientCompletesSlowResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps 1.5s to exercise a latency tail; skipped in -short mode")
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	defer srv.Close()

	c := newTestRetryClient(4*time.Second, 3)
	resp, err := postJSON(t, c, srv.URL, `{"method":"ledger"}`)
	if err != nil {
		t.Fatalf("expected slow response to complete, got error: %s", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 request, server saw %d", got)
	}
}

// Timeouts are NOT retried: the attempt consumed its full budget, and the block
// scanner's own loop handles re-fetching. Exactly one request must be issued.
func TestRetryHTTPClientDoesNotRetryTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("srv.Close waits out the handler's 2s sleep; skipped in -short mode")
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestRetryClient(300*time.Millisecond, 3)
	_, err := postJSON(t, c, srv.URL, `{}`) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("timeout must not be retried; server saw %d requests", got)
	}
}

// Transient connection-level failures ARE retried, with the body replayed
// intact on each attempt (POSTs are not transparently retried by net/http).
func TestRetryHTTPClientRetriesConnectionReset(t *testing.T) {
	const body = `{"method":"ledger","params":[{"ledger_index":123}]}`
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		got, _ := io.ReadAll(r.Body)
		if string(got) != body {
			t.Errorf("attempt %d: body not replayed intact: %q", n, string(got))
		}
		if n <= 2 {
			// Abort the connection without a response -> client sees EOF.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %s", err)
			}
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	defer srv.Close()

	c := newTestRetryClient(2*time.Second, 3)
	resp, err := postJSON(t, c, srv.URL, body)
	if err != nil {
		t.Fatalf("expected retries to recover, got: %s", err)
	}
	defer resp.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (2 aborted + 1 success), server saw %d", got)
	}
}

// Exhausted retries surface the last transport error rather than hanging or
// returning a nil response.
func TestRetryHTTPClientExhaustsRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %s", err)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	c := newTestRetryClient(2*time.Second, 2)
	_, err := postJSON(t, c, srv.URL, `{}`) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), server saw %d", got)
	}
}

// An expired/cancelled request context aborts the backoff instead of sleeping
// through it, surfacing the underlying transport error promptly.
func TestRetryHTTPClientRespectsContextDuringBackoff(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %s", err)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	c := newTestRetryClient(2*time.Second, 5)
	c.backoff = 10 * time.Second // would sleep ~50s without context awareness

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("building request: %s", err)
	}

	start := time.Now()
	_, err = c.Do(req) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("backoff ignored context cancellation; took %s", elapsed)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 attempt before context expiry, server saw %d", got)
	}
}

func TestIsTransientNetError(t *testing.T) {
	cases := []struct {
		err       error
		transient bool
	}{
		{nil, false},
		// typed, including wrapped chains
		{io.EOF, true},
		{fmt.Errorf("post failed: %w", io.EOF), true},
		{io.ErrUnexpectedEOF, true},
		{fmt.Errorf("dial: %w", syscall.ECONNREFUSED), true},
		{fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		// stringly (rewrapped without chain preservation)
		{errors.New("Post \"http://x\": EOF"), true},
		{errors.New("dial tcp 127.0.0.1:5005: connect: connection refused"), true},
		{errors.New("read tcp 10.0.0.1:1->10.0.0.2:2: read: connection reset by peer"), true},
		{errors.New("write tcp 10.0.0.1:1->10.0.0.2:2: write: broken pipe"), true},
		{errors.New("unexpected EOF"), true},
		// timeouts and semantic errors must NOT be retried
		{errors.New("Post \"http://x\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), false},
		{errors.New("actNotFound"), false},
		{errors.New("invalid params"), false},
		// contains the letters "eof" mid-word: suffix-only matching must not retry
		{errors.New("geofence check failed for request"), false},
	}
	for _, tc := range cases {
		if got := isTransientNetError(tc.err); got != tc.transient {
			t.Errorf("isTransientNetError(%v) = %v, want %v", tc.err, got, tc.transient)
		}
	}
}

func TestIsLedgerNotFound(t *testing.T) {
	cases := []struct {
		err      error
		notFound bool
	}{
		{nil, false},
		// the exact shape xrpl-go produces: ClientError wrapping result.error
		{&rpc.ClientError{ErrorString: "lgrNotFound"}, true},
		// rewrapped chains still match via errors.As
		{fmt.Errorf("fetch ledger 123: %w", &rpc.ClientError{ErrorString: "lgrNotFound"}), true},
		// message-only fallback
		{errors.New("lgrNotFound"), true},
		{&rpc.ClientError{ErrorString: "actNotFound"}, false},
		{errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		if got := isLedgerNotFound(tc.err); got != tc.notFound {
			t.Errorf("isLedgerNotFound(%v) = %v, want %v", tc.err, got, tc.notFound)
		}
	}
}

// The constructor must never produce an unbounded client: a zero/negative
// configured timeout falls back to the library-ceiling default rather than
// http.Client's "no timeout".
func TestNewRetryHTTPClientDefaults(t *testing.T) {
	c := newRetryHTTPClient(0, -5, zerolog.Nop())
	if c.inner.Timeout != defaultHTTPTimeout {
		t.Fatalf("zero timeout must default to %s, got %s", defaultHTTPTimeout, c.inner.Timeout)
	}
	if c.maxRetries != 0 {
		t.Fatalf("negative maxRetries must clamp to 0, got %d", c.maxRetries)
	}
	if c.backoff != defaultRetryBackoff {
		t.Fatalf("backoff must default to %s, got %s", defaultRetryBackoff, c.backoff)
	}
}
