package evm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

const safeBlockNumber = 0x1234567

// safeBlockServer serves eth_getBlockByNumber("safe", fullTxs) like a node
// would: tx hashes when fullTxs is false, full tx objects when it is true.
// fullTxsJSON overrides the full tx list; result overrides the whole result.
type safeBlockServer struct {
	header      map[string]any
	hashes      []string
	fullTxsJSON []json.RawMessage
	result      *string
	rpcError    bool
	rendered    map[bool]string // cached block JSON by fullTxs
}

func newSafeBlockServer(tb testing.TB, txCount int) *safeBlockServer {
	tb.Helper()
	key, err := crypto.HexToECDSA("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
	if err != nil {
		tb.Fatal(err)
	}
	chainID := big.NewInt(8453)
	signer := etypes.NewPragueSigner(chainID)
	from := crypto.PubkeyToAddress(key.PublicKey)
	blockHash := ecommon.HexToHash("0xabcdef")

	s := &safeBlockServer{}
	for i := 0; i < txCount; i++ {
		to := ecommon.BigToAddress(big.NewInt(int64(0x1000 + i)))
		tx, err := etypes.SignNewTx(key, signer, &etypes.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: big.NewInt(1e9),
			GasFeeCap: big.NewInt(2e9),
			Gas:       90000,
			To:        &to,
			Value:     big.NewInt(1e15),
			Data:      make([]byte, 68),
		})
		if err != nil {
			tb.Fatal(err)
		}
		raw, err := tx.MarshalJSON()
		if err != nil {
			tb.Fatal(err)
		}
		var obj map[string]any
		if err = json.Unmarshal(raw, &obj); err != nil {
			tb.Fatal(err)
		}
		obj["from"] = from.Hex()
		obj["blockHash"] = blockHash.Hex()
		obj["blockNumber"] = fmt.Sprintf("0x%x", safeBlockNumber)
		obj["transactionIndex"] = fmt.Sprintf("0x%x", i)
		full, err := json.Marshal(obj)
		if err != nil {
			tb.Fatal(err)
		}
		s.fullTxsJSON = append(s.fullTxsJSON, full)
		s.hashes = append(s.hashes, tx.Hash().Hex())
	}

	txRoot := etypes.EmptyTxsHash.Hex()
	if txCount > 0 {
		txRoot = ecommon.HexToHash("0x01").Hex() // ethclient only checks emptiness
	}
	s.header = map[string]any{
		"hash":             blockHash.Hex(),
		"parentHash":       ecommon.HexToHash("0x02").Hex(),
		"sha3Uncles":       etypes.EmptyUncleHash.Hex(),
		"miner":            ecommon.Address{}.Hex(),
		"stateRoot":        ecommon.HexToHash("0x03").Hex(),
		"transactionsRoot": txRoot,
		"receiptsRoot":     ecommon.HexToHash("0x04").Hex(),
		"logsBloom":        "0x" + fmt.Sprintf("%0512x", 0),
		"difficulty":       "0x0",
		"number":           fmt.Sprintf("0x%x", safeBlockNumber),
		"gasLimit":         "0x1c9c380",
		"gasUsed":          "0x0",
		"timestamp":        "0x6500000",
		"extraData":        "0x",
		"mixHash":          ecommon.Hash{}.Hex(),
		"nonce":            "0x0000000000000000",
		"baseFeePerGas":    "0x1",
		"uncles":           []string{},
	}
	return s
}

func (s *safeBlockServer) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var rpcReq struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params []any           `json:"params"`
	}
	_ = json.Unmarshal(body, &rpcReq)
	if s.rpcError {
		_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"boom"}}`, rpcReq.ID)
		return
	}
	if rpcReq.Method != "eth_getBlockByNumber" || len(rpcReq.Params) != 2 || rpcReq.Params[0] != "safe" {
		_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unexpected %s %v"}}`, rpcReq.ID, rpcReq.Method, rpcReq.Params)
		return
	}
	var result string
	if s.result != nil {
		result = *s.result
	} else {
		full, _ := rpcReq.Params[1].(bool)
		result = s.render(full)
	}
	_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":%s,"result":%s}`, rpcReq.ID, result)
}

// render builds the block JSON once per variant so benchmarks measure the
// client, not the fake server. Tests run requests sequentially.
func (s *safeBlockServer) render(full bool) string {
	if r, ok := s.rendered[full]; ok {
		return r
	}
	block := make(map[string]any, len(s.header)+1)
	for k, v := range s.header {
		block[k] = v
	}
	if full {
		block["transactions"] = s.fullTxsJSON
	} else {
		block["transactions"] = s.hashes
	}
	bz, _ := json.Marshal(block)
	if s.rendered == nil {
		s.rendered = map[bool]string{}
	}
	s.rendered[full] = string(bz)
	return s.rendered[full]
}

func newSafeBlockEthRPC(tb testing.TB, s *safeBlockServer) *EthRPC {
	tb.Helper()
	server := httptest.NewServer(s)
	tb.Cleanup(server.Close)
	client, err := ethclient.Dial(server.URL)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(client.Close)
	e, err := NewEthRPC(client, 10*time.Second, "BASE")
	if err != nil {
		tb.Fatal(err)
	}
	return e
}

func TestGetBlockHeightSafe(t *testing.T) {
	for _, txCount := range []int{0, 3} {
		e := newSafeBlockEthRPC(t, newSafeBlockServer(t, txCount))
		height, err := e.GetBlockHeightSafe()
		if err != nil || height != safeBlockNumber {
			t.Fatalf("txs=%d: got %d, %v; want %d", txCount, height, err, safeBlockNumber)
		}
	}

	null := "null"
	s := newSafeBlockServer(t, 0)
	s.result = &null
	if height, err := newSafeBlockEthRPC(t, s).GetBlockHeightSafe(); height != -1 || !errors.Is(err, ethereum.NotFound) {
		t.Fatalf("null block: got %d, %v; want -1, NotFound", height, err)
	}

	s = newSafeBlockServer(t, 0)
	s.rpcError = true
	if height, err := newSafeBlockEthRPC(t, s).GetBlockHeightSafe(); height != -1 || err == nil {
		t.Fatalf("rpc error: got %d, %v; want -1, error", height, err)
	}

	badHeader := `{"number":"not-hex"}`
	s = newSafeBlockServer(t, 0)
	s.result = &badHeader
	if height, err := newSafeBlockEthRPC(t, s).GetBlockHeightSafe(); height != -1 || err == nil {
		t.Fatalf("bad header: got %d, %v; want -1, error", height, err)
	}
}

// BenchmarkGetBlockHeightSafe measures BASE's per-iteration safe height
// lookup against a busy block.
func BenchmarkGetBlockHeightSafe(b *testing.B) {
	e := newSafeBlockEthRPC(b, newSafeBlockServer(b, 200))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		height, err := e.GetBlockHeightSafe()
		if err != nil || height != safeBlockNumber {
			b.Fatalf("got %d, %v", height, err)
		}
	}
}

// TestGetBlockHeightSafeIgnoresBody: the height lookup reads only the header,
// so a safe block whose transactions do not decode no longer stalls height
// discovery; FetchTxs still fetches and rejects that block when scanned.
func TestGetBlockHeightSafeIgnoresBody(t *testing.T) {
	s := newSafeBlockServer(t, 2)
	s.fullTxsJSON[1] = json.RawMessage(`{"type":"0x7f","hash":"` + ecommon.HexToHash("0x01").Hex() + `"}`) // unsupported tx type
	e := newSafeBlockEthRPC(t, s)
	height, err := e.GetBlockHeightSafe()
	if err != nil || height != safeBlockNumber {
		t.Fatalf("got %d, %v; want %d", height, err, safeBlockNumber)
	}
	_, err = e.client.BlockByNumber(t.Context(), big.NewInt(rpc.SafeBlockNumber.Int64()))
	if !errors.Is(err, etypes.ErrTxTypeNotSupported) {
		t.Fatalf("expected the full block to fail to decode, got %v", err)
	}
}
