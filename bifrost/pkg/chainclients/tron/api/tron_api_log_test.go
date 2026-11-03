package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// TestFailedCallsAreLogged: get and post used to build a zerolog event
// without sending it, so request failures were never logged.
func TestFailedCallsAreLogged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var logs bytes.Buffer
	api := NewTronApi(server.URL, time.Second)
	api.logger = zerolog.New(&logs)

	if _, err := api.get("getnodeinfo"); err == nil {
		t.Fatal("expected get to fail")
	}
	if _, err := api.post("getnowblock", nil); err == nil {
		t.Fatal("expected post to fail")
	}
	out := logs.String()
	for _, method := range []string{"getnodeinfo", "getnowblock"} {
		if !strings.Contains(out, `"method":"`+method+`"`) || !strings.Contains(out, "response status: 503") {
			t.Fatalf("missing log for %s in %q", method, out)
		}
	}
}
