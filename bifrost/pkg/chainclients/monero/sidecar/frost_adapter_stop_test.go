package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

// xmrStopTestContext builds the exact context NewFrostAdapterWithStop installs.
func xmrStopTestAdapter(t *testing.T, endpoint string, stop <-chan struct{}) *FrostAdapter {
	t.Helper()
	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, endpoint)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	// Production defaults are single-shot; force a real retry/backoff loop so
	// cancellation has something to abort.
	client.signCombineMaxAttempts = 50
	client.signCombineInitialBackoff = time.Hour
	client.signCombineMaxBackoff = time.Hour
	return NewFrostAdapterWithStop(client, stop)
}

func TestXMRStopChannelContextErrAndDoneLifecycle(t *testing.T) {
	t.Parallel()

	stop := make(chan struct{})
	ctx := stopChannelContext{Context: context.Background(), done: stop}

	if err := ctx.Err(); err != nil {
		t.Fatalf("Err() before close must be nil, got %v", err)
	}
	select {
	case <-ctx.Done():
		t.Fatal("Done() must not be closed before the stop channel closes")
	default:
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("stopChannelContext must not report a deadline")
	}

	close(stop)

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not unblock after the stop channel closed")
	}
	err := ctx.Err()
	if err == nil {
		t.Fatal("Err() after close must report cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Err() after close must wrap context.Canceled, got %v", err)
	}
}

func TestXMRNewFrostAdapterWithStopWiresStopChannel(t *testing.T) {
	t.Parallel()

	stop := make(chan struct{})
	adapter := xmrStopTestAdapter(t, "http://127.0.0.1:1", stop)
	if adapter.ctx == nil {
		t.Fatal("adapter context must not be nil")
	}
	if err := adapter.ctx.Err(); err != nil {
		t.Fatalf("adapter context must be live before stop, got %v", err)
	}
	close(stop)
	select {
	case <-adapter.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("adapter context Done() did not unblock on stop close")
	}
	if !errors.Is(adapter.ctx.Err(), context.Canceled) {
		t.Fatalf("adapter context must be canceled after stop close, got %v", adapter.ctx.Err())
	}

	// A nil stop channel must degrade to a background context (never canceled),
	// not to a context whose Done() is a nil channel that reports cancellation.
	nilStop := xmrStopTestAdapter(t, "http://127.0.0.1:1", nil)
	if err := nilStop.ctx.Err(); err != nil {
		t.Fatalf("nil stop channel must yield an uncancelled context, got %v", err)
	}
}

func TestXMRStopChannelAbortsInFlightSignCombineBackoff(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	responded := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"signer_busy"}`))
		select {
		case responded <- struct{}{}:
		default:
		}
	}))
	defer server.Close()

	stop := make(chan struct{})
	adapter := xmrStopTestAdapter(t, server.URL, stop)

	done := make(chan error, 1)
	go func() {
		_, combineErr := adapter.FrostSignCombine(tssp.FrostSidecarCombineReq{
			MsgID:     "msg-stop",
			SessionID: "session-stop",
			AllShares: map[string]string{"thorpub-a": "aa"},
		})
		done <- combineErr
	}()

	// Wait until the client is parked in its (1 hour) retry backoff.
	select {
	case <-responded:
	case <-time.After(5 * time.Second):
		t.Fatal("combine attempt never reached the sidecar mock")
	}

	start := time.Now()
	close(stop)

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected combine to fail once the stop channel closed")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("combine error must wrap context.Canceled, got %v", err)
		}
		// The full retry budget is 50 attempts x 1h; anything near it means the
		// stop channel is not aborting the backoff.
		if elapsed > 5*time.Second {
			t.Fatalf("combine took %s to abort after stop; backoff was not cancelled", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("combine did not abort after the stop channel closed (shutdown would hang)")
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected exactly one attempt before stop cancellation, got %d", got)
	}
}

func TestXMRStopChannelAbortsInFlightSignCombineRequest(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	inFlight := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case inFlight <- struct{}{}:
		default:
		}
		// Hold the request open; only the client's context cancellation should
		// end it.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	stop := make(chan struct{})
	adapter := xmrStopTestAdapter(t, server.URL, stop)

	done := make(chan error, 1)
	go func() {
		_, combineErr := adapter.FrostSignCombine(tssp.FrostSidecarCombineReq{
			MsgID:     "msg-stop-inflight",
			SessionID: "session-stop-inflight",
			AllShares: map[string]string{"thorpub-a": "aa"},
		})
		done <- combineErr
	}()

	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("combine request never reached the sidecar mock")
	}

	start := time.Now()
	close(stop)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected combine to fail once the stop channel closed")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("combine error must wrap context.Canceled, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("in-flight combine took %s to abort after stop", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight combine did not abort after the stop channel closed")
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("stop must abort without further attempts, got %d", got)
	}
}
