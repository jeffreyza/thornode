package watchers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSolvencyWatcherIgnoresZeroValueHalts(t *testing.T) {
	server, restore := setupSolvencyWatcherTestServer(t, func(count int) string {
		return `{"SOLVENCYHALTBCHCHAIN":0}`
	})
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	if err := watcher.Fn(nil); err != nil {
		t.Fatalf("expected zero-valued halt to be ignored, got %v", err)
	}
}

func TestSolvencyWatcherIgnoresTransientHalts(t *testing.T) {
	server, restore := setupSolvencyWatcherTestServer(t, func(count int) string {
		if count == 1 {
			return `{"SOLVENCYHALTBCHCHAIN":2762}`
		}
		return `{"SOLVENCYHALTBCHCHAIN":0}`
	})
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	if err := watcher.Fn(nil); err != nil {
		t.Fatalf("expected transient solvency halt to clear, got %v", err)
	}
}

func TestSolvencyWatcherFailsPersistentHalts(t *testing.T) {
	server, restore := setupSolvencyWatcherTestServer(t, func(count int) string {
		return `{"SOLVENCYHALTBCHCHAIN":2762}`
	})
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	err := watcher.Fn(nil)
	if err == nil {
		t.Fatal("expected persistent solvency halt to abort watcher")
	}
	if !strings.Contains(err.Error(), "SOLVENCYHALTBCHCHAIN=2762") {
		t.Fatalf("expected BCH solvency halt error, got %v", err)
	}
}

func TestSolvencyWatcherIgnoresPendingRagnarokHaltsThatAutoClear(t *testing.T) {
	server, restore := setupSolvencyWatcherHTTPServer(
		t,
		func(path string, count int) string {
			switch path {
			case "/thorchain/mimir":
				if count <= 3 {
					return `{"SOLVENCYHALTBTCCHAIN":2777}`
				}
				return `{"SOLVENCYHALTBTCCHAIN":0}`
			case "/thorchain/queue/outbound":
				return `[{"chain":"BTC","memo":"RAGNAROK:2730"}]`
			default:
				return `{}`
			}
		},
	)
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	if err := watcher.Fn(nil); err != nil {
		t.Fatalf("expected pending ragnarok solvency halt to clear, got %v", err)
	}
}

func TestSolvencyWatcherIgnoresPersistentPendingRagnarokHalts(t *testing.T) {
	server, restore := setupSolvencyWatcherHTTPServer(
		t,
		func(path string, count int) string {
			switch path {
			case "/thorchain/mimir":
				return `{"SOLVENCYHALTBTCCHAIN":2777}`
			case "/thorchain/queue/outbound":
				return `[{"chain":"BTC","memo":"RAGNAROK:2730"}]`
			default:
				return `{}`
			}
		},
	)
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	if err := watcher.Fn(nil); err != nil {
		t.Fatalf("expected persistent pending-ragnarok solvency halt to be ignored, got %v", err)
	}
}

func TestSolvencyWatcherIgnoresPersistentActiveRagnarokHalts(t *testing.T) {
	server, restore := setupSolvencyWatcherHTTPServer(
		t,
		func(path string, count int) string {
			switch path {
			case "/thorchain/mimir":
				return `{"SOLVENCYHALTXMRCHAIN":1735,"RAGNAROK-XMR-XMR":1}`
			case "/thorchain/queue/outbound":
				return `[]`
			default:
				return `{}`
			}
		},
	)
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	if err := watcher.Fn(nil); err != nil {
		t.Fatalf("expected persistent active-ragnarok solvency halt to be ignored, got %v", err)
	}
}

func TestSolvencyWatcherFailsPersistentHaltsWithoutMatchingRagnarok(t *testing.T) {
	server, restore := setupSolvencyWatcherHTTPServer(
		t,
		func(path string, count int) string {
			switch path {
			case "/thorchain/mimir":
				return `{"SOLVENCYHALTXMRCHAIN":1735,"RAGNAROK-BTC-BTC":1}`
			case "/thorchain/queue/outbound":
				return `[]`
			default:
				return `{}`
			}
		},
	)
	defer server.Close()
	defer restore()

	watcher := NewSolvencyHalt()
	err := watcher.Fn(nil)
	if err == nil {
		t.Fatal("expected persistent XMR solvency halt without matching ragnarok to fail")
	}
	if !strings.Contains(err.Error(), "SOLVENCYHALTXMRCHAIN=1735") {
		t.Fatalf("expected XMR solvency halt error, got %v", err)
	}
}

func setupSolvencyWatcherTestServer(t *testing.T, responder func(count int) string) (*httptest.Server, func()) {
	return setupSolvencyWatcherHTTPServer(t, func(path string, count int) string {
		if path == "/thorchain/mimir" {
			return responder(count)
		}
		return `[]`
	})
}

func setupSolvencyWatcherHTTPServer(t *testing.T, responder func(path string, count int) string) (*httptest.Server, func()) {
	t.Helper()

	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responder(r.URL.Path, requests[r.URL.Path])))
	}))

	originalURL := thornodeURL
	originalClient := httpClient
	originalAttempts := solvencyHaltRecheckAttempts
	originalDelay := solvencyHaltRecheckDelay

	thornodeURL = server.URL
	httpClient = server.Client()
	solvencyHaltRecheckAttempts = 1
	solvencyHaltRecheckDelay = 0

	restore := func() {
		thornodeURL = originalURL
		httpClient = originalClient
		solvencyHaltRecheckAttempts = originalAttempts
		solvencyHaltRecheckDelay = originalDelay
	}

	return server, restore
}
