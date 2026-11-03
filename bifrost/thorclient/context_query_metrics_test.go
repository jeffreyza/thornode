package thorclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type queryMetricResponseBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *queryMetricResponseBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

func TestContextQueryErrorMetrics(t *testing.T) {
	queries := []struct {
		name string
		call func(*thorchainBridge, context.Context, string) ([]byte, int, error)
	}{
		{"cached", (*thorchainBridge).getWithContext},
		{"uncached", (*thorchainBridge).getUncachedContext},
		{"legacy", func(b *thorchainBridge, _ context.Context, url string) ([]byte, int, error) {
			return b.get(url)
		}},
	}
	for _, query := range queries {
		for _, phase := range []string{"headers", "body"} {
			for _, outcome := range []string{"caller cancellation", "caller deadline", "HTTP timeout"} {
				if query.name == "legacy" && outcome != "HTTP timeout" {
					continue // The legacy query has no caller context.
				}
				t.Run(query.name+"/"+phase+"/"+outcome, func(t *testing.T) {
					started := make(chan struct{})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if phase == "body" {
							_, _ = w.Write([]byte("{"))
							if err := http.NewResponseController(w).Flush(); err != nil {
								t.Error(err)
								return
							}
						}
						if phase == "headers" {
							close(started)
						}
						<-r.Context().Done()
					}))
					t.Cleanup(server.Close)
					client := retryablehttp.NewClient()
					client.Logger = nil
					client.RetryMax = 0
					if phase == "body" {
						transport := client.HTTPClient.Transport
						client.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
							resp, err := transport.RoundTrip(req)
							if err == nil {
								resp.Body = &queryMetricResponseBody{ReadCloser: resp.Body, started: started}
							}
							return resp, err
						})
					}
					bridge := &thorchainBridge{httpClient: client, errCounter: newThorchainTestErrCounter()}
					ctx, cancel := context.WithCancel(context.Background())
					t.Cleanup(cancel)
					if outcome == "caller deadline" {
						var cancelDeadline context.CancelFunc
						ctx, cancelDeadline = context.WithTimeout(ctx, 100*time.Millisecond)
						t.Cleanup(cancelDeadline)
					} else if outcome == "HTTP timeout" {
						client.HTTPClient.Timeout = 100 * time.Millisecond
					}
					done := make(chan error, 1)
					go func() { _, _, err := query.call(bridge, ctx, server.URL); done <- err }()
					select {
					case <-started:
					case <-time.After(time.Second):
						t.Fatal("request did not reach the stalled phase")
					}
					wantErr := context.DeadlineExceeded
					if outcome == "caller cancellation" {
						wantErr = context.Canceled
						cancel()
					}
					select {
					case err := <-done:
						require.ErrorIs(t, err, wantErr)
					case <-time.After(time.Second):
						t.Fatal("query did not stop")
					}
					var wantGet, wantRead float64
					if outcome == "HTTP timeout" {
						require.NoError(t, ctx.Err(), "HTTP timeout must leave the caller context active")
						if phase == "headers" {
							wantGet = 1
						} else {
							wantRead = 1
						}
					}
					require.Equal(t, wantGet, testutil.ToFloat64(bridge.errCounter.WithLabelValues("fail_get_from_thorchain", "")))
					require.Equal(t, wantRead, testutil.ToFloat64(bridge.errCounter.WithLabelValues("fail_read_thorchain_resp", "")))
				})
			}
		}
	}
}

func TestContextQueryInternalTimeoutIsCounted(t *testing.T) {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	bridge := &thorchainBridge{httpClient: client, errCounter: newThorchainTestErrCounter()}
	// The uncached query's own deadline must still be reported as a failure.
	_, _, err := bridge.getUncachedContext(context.Background(), "http://thornode.invalid")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, float64(1), testutil.ToFloat64(bridge.errCounter.WithLabelValues("fail_get_from_thorchain", "")))
}

func TestContextQueryFailureAfterCancellationIsCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bridge := &thorchainBridge{errCounter: newThorchainTestErrCounter()}
	_, _, err := bridge.readThorchainResponse(ctx, nil, io.ErrUnexpectedEOF)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF))}
	_, _, err = bridge.readThorchainResponse(ctx, resp, nil)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, float64(1), testutil.ToFloat64(bridge.errCounter.WithLabelValues("fail_get_from_thorchain", "")))
	require.Equal(t, float64(1), testutil.ToFloat64(bridge.errCounter.WithLabelValues("fail_read_thorchain_resp", "")))
}
