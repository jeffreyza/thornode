package evm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	ethclient "github.com/ethereum/go-ethereum/ethclient"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
)

// validEthTx is a well-formed eth_getTransactionByHash result (pending, no block).
const validEthTx = `{
    "jsonrpc": "2.0", "id": 1,
    "result": {
        "nonce": "0x2", "gasPrice": "0x1", "gas": "0x13990",
        "to": "0xe65e9d372f8cacc7b6dfcd4af6507851ed31bb44",
        "value": "0x22b1c8c1227a00000", "input": "0x",
        "v": "0xa96",
        "r": "0x4fed375d064158c79dd0ee1e35cbfbe6e19ed7c0005763ca1edc10121124d1fd",
        "s": "0x56d194669c9188176ed87e96b4bd2e2b3869cdb959c1153a557e3d8d8d48c12c",
        "hash": "0x81604fe8c8df8b5e32daafa00acd06ec97281ed3056ab368cf57e2dcacd7e2d1"
    }
}`

// TestEVMVerifyBroadcast covers the signer-cache verifyFn: a tx present on chain
// (mined or pending) → respect cache; definitively absent → clear + re-sign; any
// non-NotFound RPC error → ambiguous (fail-closed).
func TestEVMVerifyBroadcast(t *testing.T) {
	sig := "0x81604fe8c8df8b5e32daafa00acd06ec97281ed3056ab368cf57e2dcacd7e2d1"
	var mode string
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &r)
		if r.Method == "eth_chainId" {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xf"}`))
			return
		}
		switch mode {
		case "present":
			_, _ = rw.Write([]byte(validEthTx))
		case "absent":
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		default: // rpc error
			rw.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	ethClient, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &EVMClient{ethClient: ethClient}

	for _, tc := range []struct {
		mode string
		want signercache.LookupResult
	}{
		{"present", signercache.LookupFound},
		{"absent", signercache.LookupNotFound},
		{"error", signercache.LookupAmbiguous},
	} {
		mode = tc.mode
		if got := c.verifyBroadcast(sig); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.mode, got, tc.want)
		}
	}
}
