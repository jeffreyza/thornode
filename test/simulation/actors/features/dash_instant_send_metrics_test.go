package features

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScrapeInstantSendMetricsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	_, err := scrapeInstantSendMetrics(server.URL)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("stalled scrape must time out: %v", err)
	}
}

func TestScrapeInstantSendMetrics(t *testing.T) {
	valid := "# HELP dash_instantsend_available_duffs Available allowance\n" +
		"dash_instantsend_available_duffs 220000000\n" +
		"dash_instantsend_warming_up 0\n"
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  string
	}{
		{"valid", valid, http.StatusOK, ""},
		{"empty", "", http.StatusOK, "missing"},
		{"wrong allowance name", strings.ReplaceAll(valid, "dash_instantsend_available_duffs", "dash_instantsend_available_duffs_extra"), http.StatusOK, "missing"},
		{"unrelated counter ignored", valid + "dash_instantsend_observed_total NaN\n", http.StatusOK, ""},
		{"missing allowance", strings.ReplaceAll(valid, "dash_instantsend_available_duffs 220000000\n", ""), http.StatusOK, "missing"},
		{"missing warmup", strings.ReplaceAll(valid, "dash_instantsend_warming_up 0\n", ""), http.StatusOK, "missing"},
		{"malformed", strings.ReplaceAll(valid, "duffs 220000000", "duffs"), http.StatusOK, "malformed"},
		{"invalid number", strings.ReplaceAll(valid, "duffs 220000000", "duffs nope"), http.StatusOK, "parse"},
		{"negative", strings.ReplaceAll(valid, "duffs 220000000", "duffs -1"), http.StatusOK, "invalid"},
		{"NaN", strings.ReplaceAll(valid, "duffs 220000000", "duffs NaN"), http.StatusOK, "invalid"},
		{"infinity", strings.ReplaceAll(valid, "duffs 220000000", "duffs +Inf"), http.StatusOK, "invalid"},
		{"invalid warmup", strings.ReplaceAll(valid, "up 0", "up 0.5"), http.StatusOK, "invalid"},
		{"HTTP failure", valid, http.StatusServiceUnavailable, "HTTP 503"},
		{"truncated scrape", valid + strings.Repeat("x", 70_000), http.StatusOK, "token too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			state, err := scrapeInstantSendMetrics(server.URL)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil || state.available != 220000000 || state.warmingUp {
				t.Fatalf("incorrect metric snapshot: %+v, %v", state, err)
			}
		})
	}
}
