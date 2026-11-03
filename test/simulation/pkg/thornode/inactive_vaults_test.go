package thornode

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGetInactiveVaultsUsesEligiblePubkeysAndIndividualRecords(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/thorchain/vaults/pubkeys":
			_, _ = w.Write([]byte(`{"asgard":[{"pub_key":"active"}],"inactive":[{"pub_key":"retired"}]}`))
		case "/thorchain/vault/retired":
			_, _ = w.Write([]byte(`{"pub_key":"retired","status":"InactiveVault","addresses":[{"chain":"XMR","address":"retired-address"}]}`))
		case "/thorchain/vaults/asgard":
			// Production deliberately hides ordinary inactive vaults here.
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oldURL, oldClient := thornodeURL, httpClient
	thornodeURL, httpClient = server.URL, server.Client()
	t.Cleanup(func() { thornodeURL, httpClient = oldURL, oldClient })
	vaults, err := GetInactiveVaults()
	if err != nil || len(vaults) != 1 || vaults[0].GetPubKey() != "retired" {
		t.Fatalf("inactive vault must remain discoverable when Asgard list hides it: %v, %v", vaults, err)
	}
	if !reflect.DeepEqual(paths, []string{"/thorchain/vaults/pubkeys", "/thorchain/vault/retired"}) {
		t.Fatalf("wrong discovery endpoints: %v", paths)
	}
}

func TestGetInactiveVaultsRejectsMissingKeyAndQueryFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing key", `{"inactive":[{}]}`, http.StatusOK},
		{"failed vault query", `{"inactive":[{"pub_key":"missing"}]}`, http.StatusOK},
		{"failed list query", `{}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/thorchain/vaults/pubkeys" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			oldURL, oldClient := thornodeURL, httpClient
			thornodeURL, httpClient = server.URL, server.Client()
			t.Cleanup(func() { thornodeURL, httpClient = oldURL, oldClient })
			if _, err := GetInactiveVaults(); err == nil {
				t.Fatal("expected discovery failure")
			}
		})
	}
}
