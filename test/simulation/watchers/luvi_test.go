package watchers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/test/luvicheck"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
)

// pools per height: rune depth, asset depth and LP units of BTC.BTC
type luviTestPool struct{ rune, asset, units string }

func setupLuviWatcherTestServer(t *testing.T, pools map[string]luviTestPool) func() {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pools[r.URL.Query().Get("height")]
		if !ok || r.URL.Path != "/thorchain/pools" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"asset":"BTC.BTC","balance_rune":"%s","balance_asset":"%s","LP_units":"%s","pool_units":"%s","synth_supply":"0"}]`,
			p.rune, p.asset, p.units, p.units)
	}))

	originalURL, originalClient, originalDelay := thornodeURL, httpClient, luviFetchDelay
	thornodeURL, httpClient, luviFetchDelay = server.URL, server.Client(), 0
	return func() {
		server.Close()
		thornodeURL, httpClient, luviFetchDelay = originalURL, originalClient, originalDelay
	}
}

func luviTestBlock(height int64, events ...map[string]string) *thorscan.BlockResponse {
	b := &thorscan.BlockResponse{}
	b.Header.Height = height
	b.EndBlockEvents = events
	return b
}

func TestLuviWatcher(t *testing.T) {
	restore := setupLuviWatcherTestServer(t, map[string]luviTestPool{
		"1": {"100000000000000", "1000000000000", "50000000000000"},
		"2": {"100000100000000", "1000000000000", "50000000000000"}, // reward
		"3": {"100000100000000", "990000000000", "50000000000000"},  // asset gone
		"4": {"100000100000000", "980000000000", "50000000000000"},  // asset gone, with gas event
		"5": {"100000100000000", "980000000000", "51000000000000"},  // units minted, no event
	})
	defer restore()

	monitor := luvicheck.NewMonitor()
	for h := int64(1); h <= 2; h++ {
		if err := checkLuviBlock(monitor, luviTestBlock(h)); err != nil {
			t.Fatalf("height %d: unexpected error %v", h, err)
		}
	}

	err := checkLuviBlock(monitor, luviTestBlock(3))
	if err == nil || !strings.Contains(err.Error(), "BTC.BTC dropped at height 3") {
		t.Fatalf("expected an unexplained drop to fail, got %v", err)
	}

	gas := map[string]string{"type": "gas", "asset": "BTC.BTC"}
	if err = checkLuviBlock(monitor, luviTestBlock(4, gas)); err != nil {
		t.Fatalf("expected a drop with a gas event to pass, got %v", err)
	}

	// a loss event never excuses a ledger mismatch
	err = checkLuviBlock(monitor, luviTestBlock(5, gas))
	if err == nil || !strings.Contains(err.Error(), "ledger mismatch") {
		t.Fatalf("expected a ledger mismatch, got %v", err)
	}

	// a missing height is an error, not a skipped block
	if err = checkLuviBlock(monitor, luviTestBlock(9)); err == nil {
		t.Fatal("expected an error when pools cannot be fetched")
	}
}
