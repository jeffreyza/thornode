package utxo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

// coinbaseRPCServer serves getblockhash and the 112-tx verbose block fixture
// and counts the full-block getblock calls. hash overrides the getblockhash
// result (a reorged height) and failGetblock makes getblock fail.
type coinbaseRPCServer struct {
	*httptest.Server
	getblockCalls atomic.Int64
	hash          atomic.Pointer[string]
	failGetblock  atomic.Bool
}

func newCoinbaseRPCServer(tb testing.TB) *coinbaseRPCServer {
	tb.Helper()
	load := func(path string) json.RawMessage {
		raw, err := os.ReadFile(path)
		if err != nil {
			tb.Fatal(err)
		}
		var fixture struct {
			Result json.RawMessage `json:"result"`
		}
		if err = json.Unmarshal(raw, &fixture); err != nil {
			tb.Fatal(err)
		}
		return fixture.Result
	}
	results := map[string]json.RawMessage{
		"getblockhash": load("../../../../test/fixtures/btc/blockhash.json"),
		"getblock":     load("../../../../test/fixtures/btc/block_verbose.json"),
	}
	s := &coinbaseRPCServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &r)
		result := results[r.Method]
		switch r.Method {
		case "getblock":
			s.getblockCalls.Add(1)
			if s.failGetblock.Load() {
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -1, "message": "getblock failed"}})
				_, _ = rw.Write(resp)
				return
			}
		case "getblockhash":
			if h := s.hash.Load(); h != nil {
				result, _ = json.Marshal(*h)
			}
		}
		resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
		_, _ = rw.Write(resp)
	}))
	tb.Cleanup(s.Close)
	return s
}

func newCoinbaseTestClient(tb testing.TB, server *coinbaseRPCServer) *Client {
	tb.Helper()
	rpcClient, err := rpc.NewClient(server.Listener.Addr().String(), "user", "pass", 1, 10*time.Second, common.BTCChain, zerolog.Nop())
	if err != nil {
		tb.Fatal(err)
	}
	return &Client{log: zerolog.Nop(), rpc: rpcClient}
}

// BenchmarkScanThenCoinbase is FetchTxs' block fetch followed by the coinbase
// lookup GetConfirmationCount makes for the same block.
func BenchmarkScanThenCoinbase(b *testing.B) {
	server := newCoinbaseRPCServer(b)
	c := newCoinbaseTestClient(b, server)
	const height = 1696761
	b.ReportAllocs()
	start := server.getblockCalls.Load()
	for b.Loop() {
		if _, err := c.getBlock(height); err != nil {
			b.Fatal(err)
		}
		if v, err := c.getCoinbaseValue(height); err != nil || v <= 0 {
			b.Fatalf("got %d, %v", v, err)
		}
	}
	b.ReportMetric(float64(server.getblockCalls.Load()-start)/float64(b.N), "getblock-calls/op")
}
