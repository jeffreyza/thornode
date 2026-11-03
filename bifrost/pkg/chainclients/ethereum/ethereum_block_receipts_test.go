package ethereum

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/rs/zerolog"
)

func makeReceiptsTestTx(nonce uint64) *etypes.Transaction {
	to := ecommon.HexToAddress("0x9f4aab49a9cd8fc54dcb3701846f608a6f2c44da")
	return etypes.NewTx(&etypes.LegacyTx{
		Nonce:    nonce,
		To:       &to,
		Value:    big.NewInt(1),
		Gas:      21000,
		GasPrice: big.NewInt(1),
	})
}

func makeReceiptsTestBlock(number int64, txs []*etypes.Transaction) *etypes.Block {
	header := &etypes.Header{
		Number:     big.NewInt(number),
		Difficulty: big.NewInt(1),
	}
	return etypes.NewBlock(header, &etypes.Body{Transactions: txs}, nil, trie.NewStackTrie(nil), etypes.DefaultBlockConfig)
}

func makeReceiptFor(tx *etypes.Transaction, block *etypes.Block) *etypes.Receipt {
	return &etypes.Receipt{
		Status:            etypes.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs:              []*etypes.Log{},
		TxHash:            tx.Hash(),
		GasUsed:           21000,
		EffectiveGasPrice: big.NewInt(1),
		BlockHash:         block.Hash(),
		BlockNumber:       block.Number(),
		TransactionIndex:  0,
	}
}

// newBlockReceiptsServer serves eth_getBlockReceipts with the given receipts
// (or a method-not-found error when supported is false), counting calls and
// capturing the block identifier param.
func newBlockReceiptsServer(
	t *testing.T,
	supported bool,
	receipts []*etypes.Receipt,
	calls *int32,
	gotParam *string,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params []string        `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method != "eth_getBlockReceipts" {
			t.Errorf("unexpected method: %s", req.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		atomic.AddInt32(calls, 1)
		if len(req.Params) > 0 && gotParam != nil {
			*gotParam = req.Params[0]
		}
		w.Header().Set("Content-Type", "application/json")
		if !supported {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"the method eth_getBlockReceipts does not exist/is not available"}}`, req.ID)
			return
		}
		result, err := json.Marshal(receipts)
		if err != nil {
			t.Errorf("marshal receipts: %s", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
}

// A supported node yields every receipt keyed by transaction hash from a
// single call, with the block hash passed as the block identifier.
func TestFetchBlockReceipts(t *testing.T) {
	txA, txB := makeReceiptsTestTx(0), makeReceiptsTestTx(1)
	block := makeReceiptsTestBlock(42, []*etypes.Transaction{txA, txB})

	var calls int32
	var gotParam string
	srv := newBlockReceiptsServer(t, true, []*etypes.Receipt{makeReceiptFor(txA, block), makeReceiptFor(txB, block)}, &calls, &gotParam)
	defer srv.Close()

	client, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer client.Close()

	receipts := fetchBlockReceipts(context.Background(), client, zerolog.Nop(), block)
	if len(receipts) != 2 {
		t.Fatalf("expected 2 receipts, got %d", len(receipts))
	}
	for _, tx := range []*etypes.Transaction{txA, txB} {
		receipt, ok := receipts[tx.Hash()]
		if !ok || receipt == nil {
			t.Fatalf("missing receipt for tx %s", tx.Hash().Hex())
		}
		if receipt.TxHash != tx.Hash() {
			t.Fatalf("receipt keyed to wrong tx: %s", receipt.TxHash.Hex())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 eth_getBlockReceipts call, got %d", got)
	}
	if gotParam != block.Hash().Hex() {
		t.Fatalf("expected block hash param %s, got %q", block.Hash().Hex(), gotParam)
	}
}

// A node that does not serve eth_getBlockReceipts returns nil, signaling the
// caller to fall back to per-transaction lookups.
func TestFetchBlockReceiptsUnsupportedNode(t *testing.T) {
	tx := makeReceiptsTestTx(0)
	block := makeReceiptsTestBlock(42, []*etypes.Transaction{tx})

	var calls int32
	srv := newBlockReceiptsServer(t, false, nil, &calls, nil)
	defer srv.Close()

	client, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer client.Close()

	receipts := fetchBlockReceipts(context.Background(), client, zerolog.Nop(), block)
	if receipts != nil {
		t.Fatalf("expected nil map on unsupported node, got %d receipts", len(receipts))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 attempted call, got %d", got)
	}
}

// A block with no transactions must not issue the call at all.
func TestFetchBlockReceiptsEmptyBlock(t *testing.T) {
	block := makeReceiptsTestBlock(42, nil)

	var calls int32
	srv := newBlockReceiptsServer(t, true, nil, &calls, nil)
	defer srv.Close()

	client, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer client.Close()

	if receipts := fetchBlockReceipts(context.Background(), client, zerolog.Nop(), block); receipts != nil {
		t.Fatalf("expected nil map for an empty block, got %d receipts", len(receipts))
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected no calls for an empty block, got %d", got)
	}
}

// A receipt carrying another block's hash (served across a reorg) is dropped,
// leaving its transaction to the per-transaction fallback.
func TestFetchBlockReceiptsDropsBlockHashMismatch(t *testing.T) {
	txA, txB := makeReceiptsTestTx(0), makeReceiptsTestTx(1)
	block := makeReceiptsTestBlock(42, []*etypes.Transaction{txA, txB})

	stale := makeReceiptFor(txB, block)
	stale.BlockHash = ecommon.HexToHash("0xdead")

	var calls int32
	srv := newBlockReceiptsServer(t, true, []*etypes.Receipt{makeReceiptFor(txA, block), stale}, &calls, nil)
	defer srv.Close()

	client, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer client.Close()

	receipts := fetchBlockReceipts(context.Background(), client, zerolog.Nop(), block)
	if len(receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(receipts))
	}
	if _, ok := receipts[txA.Hash()]; !ok {
		t.Fatalf("missing receipt for tx %s", txA.Hash().Hex())
	}
	if _, ok := receipts[txB.Hash()]; ok {
		t.Fatalf("receipt from another block was kept for tx %s", txB.Hash().Hex())
	}
}

// A receipt without an effective gas price is dropped, leaving its transaction
// to the per-transaction fallback instead of panicking in gas accounting.
func TestFetchBlockReceiptsDropsNilEffectiveGasPrice(t *testing.T) {
	txA, txB := makeReceiptsTestTx(0), makeReceiptsTestTx(1)
	block := makeReceiptsTestBlock(42, []*etypes.Transaction{txA, txB})

	noPrice := makeReceiptFor(txB, block)
	noPrice.EffectiveGasPrice = nil

	var calls int32
	srv := newBlockReceiptsServer(t, true, []*etypes.Receipt{makeReceiptFor(txA, block), noPrice}, &calls, nil)
	defer srv.Close()

	client, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer client.Close()

	receipts := fetchBlockReceipts(context.Background(), client, zerolog.Nop(), block)
	if len(receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(receipts))
	}
	if _, ok := receipts[txA.Hash()]; !ok {
		t.Fatalf("missing receipt for tx %s", txA.Hash().Hex())
	}
	if _, ok := receipts[txB.Hash()]; ok {
		t.Fatalf("receipt without effective gas price was kept for tx %s", txB.Hash().Hex())
	}
}
