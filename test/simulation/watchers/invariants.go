package watchers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

var (
	invariantRecheckAttempts = 2
	invariantRecheckDelay    = 2 * time.Second
)

type invariantResponse struct {
	Broken    bool     `json:"broken"`
	Invariant string   `json:"invariant"`
	Msg       []string `json:"msg"`
}

// The REST gateway accepts x-cosmos-block-height and returns the observed height
// as grpc-gateway forwarded metadata (Grpc-Metadata-X-Cosmos-Block-Height), since
// THORNode's custom gateway router keeps the default outgoing header prefix. The
// unprefixed form is accepted as a fallback. An empty height means the gateway did
// not report it.
const (
	invariantHeightHeader         = "x-cosmos-block-height"
	invariantResponseHeightHeader = "Grpc-Metadata-X-Cosmos-Block-Height"
)

func fetchInvariant(ctx context.Context, cl *http.Client, endpoint, height string) (invariantResponse, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return invariantResponse{}, "", err
	}
	if height != "" {
		req.Header.Set(invariantHeightHeader, height)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return invariantResponse{}, "", err
	}
	defer resp.Body.Close()
	observed := resp.Header.Get(invariantResponseHeightHeader)
	if observed == "" {
		observed = resp.Header.Get(invariantHeightHeader)
	}
	if resp.StatusCode != http.StatusOK {
		return invariantResponse{}, observed, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if observed != "" {
		n, parseErr := strconv.ParseInt(observed, 10, 64)
		if parseErr != nil || n <= 0 {
			return invariantResponse{}, observed, fmt.Errorf("invalid observed height %q", observed)
		}
	}
	if height != "" && observed != "" && observed != height {
		return invariantResponse{}, observed, fmt.Errorf("requested height %s, observed height %s", height, observed)
	}
	var wire struct {
		Broken    *bool    `json:"broken"`
		Invariant string   `json:"invariant"`
		Msg       []string `json:"msg"`
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&wire); err != nil {
		return invariantResponse{}, observed, fmt.Errorf("decode response: %w", err)
	}
	if wire.Broken == nil {
		return invariantResponse{}, observed, fmt.Errorf("decode response: missing broken field")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invariantResponse{}, observed, fmt.Errorf("decode response: trailing data: %v", err)
	}
	return invariantResponse{Broken: *wire.Broken, Invariant: wire.Invariant, Msg: wire.Msg}, observed, nil
}

func waitInvariantRetry(ctx context.Context) error {
	timer := time.NewTimer(invariantRecheckDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func NewInvariants() *Watcher {
	invariants := []string{}
	k := keeperv1.KVStore{}
	for _, ir := range k.InvariantRoutes() {
		invariants = append(invariants, ir.Route)
	}
	var mu sync.Mutex
	var lastHeight string

	return &Watcher{
		Name:     "Invariants",
		Interval: 10 * time.Second,
		Fn: func(config *OpConfig) error {
			mu.Lock()
			defer mu.Unlock()
			height, err := verifyInvariantsAtHeight(context.Background(), invariants)
			if err != nil {
				return err
			}
			if lastHeight != "" {
				if height == "" {
					return fmt.Errorf("invariant verification lost observed height after height %s", lastHeight)
				}
				previous, _ := strconv.ParseInt(lastHeight, 10, 64)
				current, _ := strconv.ParseInt(height, 10, 64)
				if current < previous {
					return fmt.Errorf("stale invariant verification at height %s after height %s", height, lastHeight)
				}
			}
			lastHeight = height
			return nil
		},
	}
}

func verifyInvariants(ctx context.Context, invariants []string) error {
	_, err := verifyInvariantsAtHeight(ctx, invariants)
	return err
}

func verifyInvariantsAtHeight(ctx context.Context, invariants []string) (string, error) {
	if len(invariants) == 0 {
		return "", fmt.Errorf("no invariant routes registered")
	}
	// Pin the remaining routes to the first successful response's height. A
	// gateway without height metadata still gets checked, but cannot be pinned.
	height := ""
	for _, invariant := range invariants {
		endpoint := fmt.Sprintf("%s/thorchain/invariant/%s", thornodeURL, invariant)
		var lastErr error
		var brokenHeight string
		for attempt := 0; attempt <= invariantRecheckAttempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("invariant %s at %s height %q: %w", invariant, endpoint, height, err)
			}
			result, observed, err := fetchInvariant(ctx, httpClient, endpoint, height)
			if err == nil && result.Invariant != invariant {
				err = fmt.Errorf("response invariant %q, expected %q", result.Invariant, invariant)
			}
			if err == nil && height != "" && observed == "" {
				err = fmt.Errorf("missing observed height for pinned request")
			}
			if err == nil {
				if !result.Broken {
					if brokenHeight != "" {
						current, _ := strconv.ParseInt(observed, 10, 64)
						previous, _ := strconv.ParseInt(brokenHeight, 10, 64)
						if current <= previous {
							return "", fmt.Errorf("invariant %s endpoint %s cleared at height %s without a newer snapshot than broken height %s", invariant, endpoint, observed, brokenHeight)
						}
					}
					if height == "" {
						height = observed
					}
					break
				}
				lastErr = fmt.Errorf("invariant %s is broken at height %q: %s", invariant, observed, strings.Join(result.Msg, ", "))
				// A pinned violation is repeatable. Without an observed height,
				// there is no evidence that a later response is a new snapshot.
				if height != "" || observed == "" {
					return "", lastErr
				}
				// Retries must advance past the highest broken height seen, so an
				// older snapshot cannot lower the bar recovery has to clear.
				if brokenHeight != "" {
					current, _ := strconv.ParseInt(observed, 10, 64)
					previous, _ := strconv.ParseInt(brokenHeight, 10, 64)
					if current <= previous {
						return "", fmt.Errorf("%w (retry did not advance past broken height %s)", lastErr, brokenHeight)
					}
				}
				brokenHeight = observed
			} else {
				lastErr = fmt.Errorf("invariant %s endpoint %s requested height %q observed height %q: %w", invariant, endpoint, height, observed, err)
			}
			if attempt == invariantRecheckAttempts {
				return "", fmt.Errorf("verification failed after %d attempts: %w", attempt+1, lastErr)
			}
			if err := waitInvariantRetry(ctx); err != nil {
				return "", fmt.Errorf("invariant %s endpoint %s height %q: %w", invariant, endpoint, height, err)
			}
		}
	}
	return height, nil
}
