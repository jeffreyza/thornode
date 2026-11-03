package watchers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvariantsWatcherIgnoresTransientFailures(t *testing.T) {
	server, restore := setupInvariantWatcherTestServer(t, func(invariant string, count int) string {
		if invariant == "asgard" && count == 1 {
			return `{"invariant":"asgard","broken":true,"msg":["oversolvent: 41599rune"]}`
		}
		return fmt.Sprintf(`{"invariant":"%s","broken":false,"msg":[]}`, invariant)
	})
	defer server.Close()
	defer restore()

	watcher := NewInvariants()
	if err := watcher.Fn(nil); err != nil {
		t.Fatalf("expected transient invariant failure to clear, got %v", err)
	}
}

func TestInvariantsWatcherFailsPersistentFailures(t *testing.T) {
	server, restore := setupInvariantWatcherTestServer(t, func(invariant string, count int) string {
		if invariant == "asgard" {
			return `{"invariant":"asgard","broken":true,"msg":["oversolvent: 41599rune"]}`
		}
		return fmt.Sprintf(`{"invariant":"%s","broken":false,"msg":[]}`, invariant)
	})
	defer server.Close()
	defer restore()

	watcher := NewInvariants()
	err := watcher.Fn(nil)
	if err == nil {
		t.Fatal("expected persistent invariant failure to abort watcher")
	}
	if !strings.Contains(err.Error(), "invariant asgard is broken") {
		t.Fatalf("expected asgard invariant failure, got %v", err)
	}
}

func setupInvariantWatcherTestServer(t *testing.T, responder func(invariant string, count int) string) (*httptest.Server, func()) {
	t.Helper()

	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invariant := strings.TrimPrefix(r.URL.Path, "/thorchain/invariant/")
		requests[invariant]++
		height := "42"
		if invariant == "asgard" && requests[invariant] == 1 {
			height = "41"
		}
		w.Header().Set(invariantResponseHeightHeader, height)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responder(invariant, requests[invariant])))
	}))

	originalURL := thornodeURL
	originalClient := httpClient
	originalAttempts := invariantRecheckAttempts
	originalDelay := invariantRecheckDelay

	thornodeURL = server.URL
	httpClient = server.Client()
	invariantRecheckAttempts = 1
	invariantRecheckDelay = 0

	restore := func() {
		thornodeURL = originalURL
		httpClient = originalClient
		invariantRecheckAttempts = originalAttempts
		invariantRecheckDelay = originalDelay
	}

	return server, restore
}
