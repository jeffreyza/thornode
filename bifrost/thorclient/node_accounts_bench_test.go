package thorclient

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/config"
)

func setBenchBech32Prefixes() {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg.SetBech32PrefixForValidator("tthorv", "tthorvpub")
	cfg.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
}

// nodeAccountsBody repeats the nodes.json fixture entries to n nodes.
func nodeAccountsBody(tb testing.TB, n int) []byte {
	tb.Helper()
	raw, err := os.ReadFile("../../test/fixtures/endpoints/nodeaccount/nodes.json")
	if err != nil {
		tb.Fatal(err)
	}
	var fixture []map[string]any
	if err = json.Unmarshal(raw, &fixture); err != nil {
		tb.Fatal(err)
	}
	all := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, fixture[i%len(fixture)])
	}
	body, err := json.Marshal(all)
	if err != nil {
		tb.Fatal(err)
	}
	return body
}

// cacheNodeAccountsBody stores body as the bridge's node accounts response in
// the shared HTTP cache, as get does after a successful fetch.
func cacheNodeAccountsBody(tb testing.TB, bridge *thorchainBridge, body []byte) {
	tb.Helper()
	url := bridge.getThorChainURL(NodeAccountsEndpoint)
	httpResponseCachesMu.Lock()
	httpResponseCaches[url] = &httpResponseCache{
		httpResponse:        body,
		httpResponseChecked: time.Now().Add(100 * 365 * 24 * time.Hour), // never expires during the run
		httpResponseLock:    make(chan struct{}, 1),
	}
	httpResponseCachesMu.Unlock()
	tb.Cleanup(func() {
		httpResponseCachesMu.Lock()
		delete(httpResponseCaches, url)
		httpResponseCachesMu.Unlock()
	})
}

func newNodeAccountsTestBridge(host string) *thorchainBridge {
	return &thorchainBridge{cfg: config.BifrostClientConfiguration{ChainHost: host}}
}

func BenchmarkFetchActiveNodes(b *testing.B) {
	setBenchBech32Prefixes()
	bridge := newNodeAccountsTestBridge("bench-node-accounts.invalid:1317")
	// sendDeck hits a cached body on 5 of every 6 calls: deckRefreshTime is 1s
	// and the HTTP cache keeps a response for one THORChain block
	cacheNodeAccountsBody(b, bridge, nodeAccountsBody(b, 120))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		active, err := bridge.FetchActiveNodes()
		if err != nil || len(active) != 60 {
			b.Fatalf("got %d active, err %v", len(active), err)
		}
	}
}
