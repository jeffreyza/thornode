package xrp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/rs/zerolog"
)

// defaultHTTPTimeout is the fallback per-request timeout when the block scanner
// configuration carries no value. It intentionally matches the 5s context
// deadline the xrpl-go rpc client applies internally to every request, which is
// the effective ceiling for any configured value.
const defaultHTTPTimeout = 5 * time.Second

// defaultRetryBackoff is deliberately short: every retry attempt runs inside
// the SINGLE 5s request context xrpl-go creates before invoking Do, so backoff
// sleeps and prior-attempt latency all draw down one shared budget. Effective
// retries are therefore "up to N immediate connection-failure recoveries
// within the 5s window", not N full timeout budgets.
const defaultRetryBackoff = 100 * time.Millisecond

// retryHTTPClient implements xrpl-go's rpc.HTTPClient, wrapping http.Client to
// fix two behaviors of the library's default (&http.Client{Timeout: 1s}):
//
//   - The 1s timeout is far below the request budget of every other chain
//     client (most honor the configured http_request_timeout, default 30s;
//     the tightest hardcoded outliers, gaia and zec, still allow 10s) and
//     turns ordinary p99 latency into request failures. Every failure
//     bubbles into BlockScanner.healthy=false, so XRP flaps unhealthy at a
//     rate no other chain exhibits, and under a degraded network path the
//     fast-fail regime completes NOTHING while 30s-timeout chains keep
//     scanning. Note the library additionally imposes a hardcoded 5s context
//     deadline per request, so the effective timeout is min(configured, 5s)
//     until that is lifted upstream.
//
//   - The library performs no transport-level retries. Its JSON-RPC requests
//     are POSTs, which Go's http.Transport will not transparently retry on a
//     stale keepalive connection, so idle-connection evictions by middleboxes
//     or provider LBs surface as hard errors (-> health flaps). Mirroring the
//     UTXO client's approach (bifrost/pkg/chainclients/utxo/rpc), Do retries a
//     whitelist of transient transport errors with a short, context-aware
//     backoff, logging each retry. HTTP-level 503 handling is deliberately
//     NOT duplicated here: the library already retries 503 responses itself.
//
// Retries clone the request and rewind the body via GetBody, which is always
// populated for the library's bytes.Reader-backed requests. Timeouts are
// deliberately NOT retried: a timed-out attempt has already consumed the full
// per-request budget, and the block scanner's own loop re-fetches the same
// height after its discovery backoff.
type retryHTTPClient struct {
	inner      *http.Client
	maxRetries int
	backoff    time.Duration
	logger     zerolog.Logger
}

var _ rpc.HTTPClient = (*retryHTTPClient)(nil)

func newRetryHTTPClient(timeout time.Duration, maxRetries int, logger zerolog.Logger) *retryHTTPClient {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	return &retryHTTPClient{
		inner:      &http.Client{Timeout: timeout},
		maxRetries: maxRetries,
		backoff:    defaultRetryBackoff,
		logger:     logger,
	}
}

// isTransientNetError reports whether err is a transport-level failure that is
// safe and useful to retry for an idempotent JSON-RPC request. Typed checks
// first; the string fallback stays narrow (mirroring the UTXO rpc client's
// empirically-derived whitelist) because intermediate layers can rewrap errors
// without preserving the chain. "eof" is matched as a suffix only, so semantic
// errors that merely contain those letters mid-word can never trigger a retry.
func isTransientNetError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.HasSuffix(errStr, "eof")
}

func (r *retryHTTPClient) Do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		attemptReq := req
		if attempt > 0 {
			// A replay needs a rewindable body.
			if req.Body != nil && req.GetBody == nil {
				break
			}
			r.logger.Warn().Err(lastErr).
				Int("attempt", attempt).
				Int("max_retries", r.maxRetries).
				Msg("retrying transient rpc transport error")
			// Context-aware backoff: the request context (xrpl-go caps it at
			// 5s) is shared by all attempts — if it expires during backoff,
			// return the underlying cause instead of sleeping the budget away.
			select {
			case <-req.Context().Done():
				return nil, lastErr
			case <-time.After(r.backoff):
			}
			attemptReq = req.Clone(req.Context())
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					break
				}
				attemptReq.Body = body
			}
		}

		resp, err := r.inner.Do(attemptReq)
		if err == nil {
			return resp, nil
		}
		if !isTransientNetError(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// isLedgerNotFound reports whether err is rippled's lgrNotFound error — the
// ledger at the requested index is not (yet) available on this server. While
// chasing the tip this is an expected condition, equivalent to the
// not-yet-validated case already handled via Validated=false: the scanner
// should treat the block as unavailable and retry, not mark the chain
// unhealthy. xrpl-go surfaces rippled errors verbatim as their error code
// string (rpc.ClientError wrapping result.error), so match the typed error
// first and fall back to the message for rewrapped chains.
func isLedgerNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ce *rpc.ClientError
	if errors.As(err, &ce) {
		return strings.Contains(ce.ErrorString, "lgrNotFound")
	}
	return strings.Contains(err.Error(), "lgrNotFound")
}
