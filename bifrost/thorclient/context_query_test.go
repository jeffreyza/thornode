package thorclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestContextQueryCacheCollapsesRequestsAndCancelsWaiter(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var requestStartedOnce sync.Once
	var releaseOnce sync.Once
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		requestStartedOnce.Do(func() { close(requestStarted) })
		<-releaseRequest
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseRequest) }) })

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	httpClient.RetryMax = 0
	bridge := &thorchainBridge{
		cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
		httpClient: httpClient,
		errCounter: newThorchainTestErrCounter(),
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := bridge.GetVault("shared-vault")
		firstDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("first context-aware query did not start")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := bridge.GetVaultWithContext(waitCtx, "shared-vault")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, int32(1), requests.Load(), "a cache waiter must not start another request")

	releaseOnce.Do(func() { close(releaseRequest) })
	select {
	case err := <-firstDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("first context-aware query did not finish")
	}

	// A normal large-vault quorum may evaluate the same vault once per
	// accumulated attestation. The raw response should still be fetched once.
	for range 67 {
		_, err := bridge.GetVaultWithContext(context.Background(), "shared-vault")
		require.NoError(t, err)
		_, err = bridge.GetVault("shared-vault")
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), requests.Load())
}

func TestContextQueryCanceledSuccessfulResponsePreservesCache(t *testing.T) {
	bridge := &thorchainBridge{}
	url := t.Name()
	oldChecked := time.Now().Add(-2 * constants.ThorchainBlockTime)
	cache := &httpResponseCache{httpResponse: []byte("old"), httpResponseChecked: oldChecked, httpResponseLock: make(chan struct{}, 1)}
	httpResponseCachesMu.Lock()
	httpResponseCaches[url] = cache
	httpResponseCachesMu.Unlock()
	t.Cleanup(func() { httpResponseCachesMu.Lock(); delete(httpResponseCaches, url); httpResponseCachesMu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := bridge.getCached(ctx, url, func() ([]byte, int, error) { cancel(); return []byte("late"), http.StatusOK, nil })
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "old", string(cache.httpResponse))
	require.Equal(t, oldChecked, cache.httpResponseChecked)
	_, _, err = bridge.getCached(ctx, url, func() ([]byte, int, error) { t.Fatal("pre-canceled call ran query"); return nil, 0, nil })
	require.ErrorIs(t, err, context.Canceled)
	buf, _, err := bridge.getCached(context.Background(), url, func() ([]byte, int, error) { return []byte("fresh"), http.StatusOK, nil })
	require.NoError(t, err)
	require.Equal(t, "fresh", string(buf))
}

func TestContextQueryCancelsRetryBackoff(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	backoff := make(chan struct{})
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 5
	client.Backoff = func(time.Duration, time.Duration, int, *http.Response) time.Duration {
		close(backoff)
		return time.Minute
	}
	bridge := &thorchainBridge{httpClient: client, errCounter: newThorchainTestErrCounter()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := bridge.getWithContext(ctx, server.URL); done <- err }()
	select {
	case <-backoff:
	case <-time.After(time.Second):
		t.Fatal("request never entered retry backoff")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("retry delay ignored cancellation")
	}
	require.Equal(t, int32(1), requests.Load())
}

func TestContextQueryCancelsResponseBody(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test response writer does not support flushing")
			return
		}
		_, _ = w.Write([]byte("{"))
		flusher.Flush()
		close(started)
		<-r.Context().Done()
		close(exited)
	}))
	defer server.Close()
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 0
	bridge := &thorchainBridge{httpClient: client, errCounter: newThorchainTestErrCounter()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := bridge.getWithContext(ctx, server.URL); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("response body did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("response body ignored cancellation")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("canceled request still retained server handler")
	}
}

func TestCanceledContextQueryDoesNotPopulateCache(t *testing.T) {
	firstStarted := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	httpClient.RetryMax = 0
	bridge := &thorchainBridge{
		cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
		httpClient: httpClient,
		errCounter: newThorchainTestErrCounter(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		_, err := bridge.GetVaultWithContext(ctx, "cancelled-vault")
		firstDone <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("cancelled context-aware query did not start")
	}
	select {
	case err := <-firstDone:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("context-aware query ignored cancellation")
	}

	_, err := bridge.GetVaultWithContext(context.Background(), "cancelled-vault")
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load(), "canceled response must not satisfy the next query")
}

func TestContextQueriesCancelRequestAndRetries(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *thorchainBridge) error
	}{
		{
			name: "vault",
			call: func(ctx context.Context, bridge *thorchainBridge) error {
				_, err := bridge.GetVaultWithContext(ctx, "vault")
				return err
			},
		},
		{
			name: "keysign",
			call: func(ctx context.Context, bridge *thorchainBridge) error {
				_, err := bridge.GetKeysignWithContext(ctx, 123, "")
				return err
			},
		},
		{
			name: "live pending outbounds",
			call: func(ctx context.Context, bridge *thorchainBridge) error {
				_, err := bridge.GetPendingOutboundsWithContext(ctx, 0)
				return err
			},
		},
		{
			name: "historical pending outbounds",
			call: func(ctx context.Context, bridge *thorchainBridge) error {
				_, err := bridge.GetPendingOutboundsWithContext(ctx, 123)
				return err
			},
		},
		{
			name: "observed tx voter",
			call: func(ctx context.Context, bridge *thorchainBridge) error {
				_, err := bridge.GetObservedTxVoterWithContext(ctx, "txid")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			requestExited := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
				close(requestStarted)
				<-requestExited
			}))
			t.Cleanup(server.Close)
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(requestExited) }) })

			httpClient := retryablehttp.NewClient()
			httpClient.Logger = nil
			httpClient.RetryMax = 5
			bridge := &thorchainBridge{
				cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
				httpClient: httpClient,
				errCounter: newThorchainTestErrCounter(),
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- tt.call(ctx, bridge)
			}()

			select {
			case <-requestStarted:
			case <-time.After(time.Second):
				t.Fatal("context-aware Thornode request did not start")
			}
			select {
			case err := <-done:
				require.Error(t, err)
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case <-time.After(time.Second):
				t.Fatal("context-aware Thornode request ignored cancellation")
			}
			releaseOnce.Do(func() { close(requestExited) })
			require.Equal(t, int32(1), requests.Load(), "cancellation must stop retry attempts")
		})
	}
}
