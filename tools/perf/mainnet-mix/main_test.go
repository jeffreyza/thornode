package main

import (
	"strings"
	"testing"
)

func TestGetErrorsDoNotLeakAPIKey(t *testing.T) {
	const key = "s3cr3t-key"
	t.Setenv("LIQUIFY_API_KEY", key)
	saved, savedWait := *endpoint, retryWait
	defer func() { *endpoint, retryWait = saved, savedWait }()
	retryWait = func(int) {}

	// connection refused (transport error) and a malformed URL
	for _, ep := range []string{"http://127.0.0.1:1", "http://bad host\x7f"} {
		*endpoint = ep
		var out any
		err := get("/thorchain/block", &out)
		if err == nil {
			t.Fatalf("%q: expected an error", ep)
		}
		if strings.Contains(err.Error(), key) {
			t.Fatalf("%q: error leaks the API key: %v", ep, err)
		}
		if !strings.Contains(err.Error(), "/thorchain/block") {
			t.Fatalf("%q: error lost the request path: %v", ep, err)
		}
	}
}
