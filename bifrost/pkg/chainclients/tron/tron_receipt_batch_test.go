package tron

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
)

// receiptBatchServer serves the block, the batch info endpoint and the
// per-transaction info endpoint, counting the info calls. batchBody overrides
// the batch response; by default it carries an entry for every transaction in
// the block except those in batchOmit. byIDHeight is the height reported by
// per-transaction lookups.
type receiptBatchServer struct {
	block      api.Block
	batchBody  string
	batchOmit  map[string]bool
	byIDHeight int64

	batchCalls atomic.Int64
	byIDCalls  atomic.Int64
}

func (s *receiptBatchServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getblockbynum":
			_ = json.NewEncoder(w).Encode(s.block)
		case "/wallet/gettransactioninfobyblocknum":
			s.batchCalls.Add(1)
			if s.batchBody != "" {
				_, _ = fmt.Fprint(w, s.batchBody)
				return
			}
			infos := []map[string]any{}
			for _, tx := range s.block.Transactions {
				if s.batchOmit[tx.TxId] {
					continue
				}
				infos = append(infos, map[string]any{"id": tx.TxId, "blockNumber": 100, "fee": 700})
			}
			_ = json.NewEncoder(w).Encode(infos)
		case "/wallet/gettransactioninfobyid":
			s.byIDCalls.Add(1)
			var request struct{ Value string }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":%d,"fee":700}`, request.Value, s.byIDHeight)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// receiptBatchBlock returns a block at height 100 with the given number of
// inbound vault transfers and failed vault-sent transfers, padded with
// unrelated transfers.
func receiptBatchBlock(t *testing.T, asset, active string, inbound, failed int) (api.Block, []string) {
	t.Helper()
	external := "41" + strings.Repeat("a", 40)
	block := api.Block{}
	block.Header.RawData.Number = 100
	var vaultIDs []string
	for i := range 50 {
		block.Transactions = append(block.Transactions, receiptFilterTransaction(t, asset, fmt.Sprintf("%064x", 1000+i), external, external))
	}
	for i := range inbound {
		id := fmt.Sprintf("%064x", 2000+i)
		block.Transactions = append(block.Transactions, receiptFilterTransaction(t, asset, id, external, active))
		vaultIDs = append(vaultIDs, id)
	}
	for i := range failed {
		id := fmt.Sprintf("%064x", 3000+i)
		tx := receiptFilterTransaction(t, asset, id, active, external)
		tx.Ret[0].ContractRet = "REVERT"
		block.Transactions = append(block.Transactions, tx)
		vaultIDs = append(vaultIDs, id)
	}
	return block, vaultIDs
}

func assertReceiptGas(t *testing.T, got, want []string, gas []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("observations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] || gas[i] != 70000 {
			t.Fatalf("observation %d = %s with gas %d, want %s with gas 70000", i, got[i], gas[i], want[i])
		}
	}
}

// Fees of up to two vault transactions are fetched individually; from three
// on, one batch call replaces them. Failed vault-sent transactions count.
func TestFetchTxsReceiptBatchThreshold(t *testing.T) {
	manager, active, _ := receiptFilterVaults(t)
	for _, asset := range []string{"TRX", "USDT"} {
		for _, tc := range []struct {
			name                string
			inbound, failed     int
			wantBatch, wantByID int64
		}{
			{"none", 0, 0, 0, 0},
			{"one", 1, 0, 0, 1},
			{"two", 2, 0, 0, 2},
			{"one plus one failed", 1, 1, 0, 2},
			{"three", 3, 0, 1, 0},
			{"two plus one failed", 2, 1, 1, 0},
			{"many", 40, 0, 1, 0},
		} {
			t.Run(asset+"/"+tc.name, func(t *testing.T) {
				block, vaultIDs := receiptBatchBlock(t, asset, active, tc.inbound, tc.failed)
				srv := &receiptBatchServer{block: block, byIDHeight: 100}
				scanner := receiptFilterScanner(t, srv.start(t).URL, manager)

				txIn, err := scanner.FetchTxs(100, 1000)
				if err != nil {
					t.Fatal(err)
				}
				if got := srv.batchCalls.Load(); got != tc.wantBatch {
					t.Fatalf("batch requests = %d, want %d", got, tc.wantBatch)
				}
				if got := srv.byIDCalls.Load(); got != tc.wantByID {
					t.Fatalf("per-transaction requests = %d, want %d", got, tc.wantByID)
				}
				ids := make([]string, len(txIn.TxArray))
				gas := make([]uint64, len(txIn.TxArray))
				for i, item := range txIn.TxArray {
					ids[i] = item.Tx
					if len(item.Gas) != 1 {
						t.Fatalf("observation %s has gas %v", item.Tx, item.Gas)
					}
					gas[i] = item.Gas[0].Amount.Uint64()
				}
				assertReceiptGas(t, ids, vaultIDs, gas)
			})
		}
	}
}

// A vault transaction missing from the batch response, or a batch endpoint
// answering with an application error, falls back to individual lookups.
func TestFetchTxsReceiptBatchFallsBack(t *testing.T) {
	manager, active, _ := receiptFilterVaults(t)
	block, vaultIDs := receiptBatchBlock(t, "TRX", active, 3, 0)
	for _, tc := range []struct {
		name     string
		srv      *receiptBatchServer
		wantByID int64
	}{
		{"missing entry", &receiptBatchServer{batchOmit: map[string]bool{vaultIDs[1]: true}}, 1},
		{"error object", &receiptBatchServer{batchBody: `{"Error":"block info unavailable"}`}, 3},
		{"empty", &receiptBatchServer{batchBody: `[]`}, 3},
		{"other height", &receiptBatchServer{batchBody: fmt.Sprintf(`[{"id":%q,"blockNumber":99,"fee":1}]`, vaultIDs[0])}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.srv.block, tc.srv.byIDHeight = block, 100
			scanner := receiptFilterScanner(t, tc.srv.start(t).URL, manager)

			txIn, err := scanner.FetchTxs(100, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if got := tc.srv.batchCalls.Load(); got != 1 {
				t.Fatalf("batch requests = %d, want 1", got)
			}
			if got := tc.srv.byIDCalls.Load(); got != tc.wantByID {
				t.Fatalf("per-transaction requests = %d, want %d", got, tc.wantByID)
			}
			ids := make([]string, len(txIn.TxArray))
			gas := make([]uint64, len(txIn.TxArray))
			for i, item := range txIn.TxArray {
				ids[i] = item.Tx
				gas[i] = item.Gas[0].Amount.Uint64()
			}
			assertReceiptGas(t, ids, vaultIDs, gas)
		})
	}
}

// When the fallback lookup also cannot place the transaction in the scanned
// block, the whole block is retried rather than observed without it.
func TestFetchTxsReceiptBatchFallbackMismatchRetries(t *testing.T) {
	manager, active, _ := receiptFilterVaults(t)
	block, vaultIDs := receiptBatchBlock(t, "TRX", active, 3, 0)
	srv := &receiptBatchServer{block: block, batchOmit: map[string]bool{vaultIDs[2]: true}, byIDHeight: 101}
	scanner := receiptFilterScanner(t, srv.start(t).URL, manager)

	txIn, err := scanner.FetchTxs(100, 1000)
	if !errors.Is(err, btypes.ErrUnavailableBlock) || len(txIn.TxArray) != 0 {
		t.Fatalf("mismatched receipt must retry the full block: err=%v, observations=%d", err, len(txIn.TxArray))
	}
	if srv.batchCalls.Load() != 1 || srv.byIDCalls.Load() != 1 {
		t.Fatalf("requests: batch=%d per-transaction=%d, want 1 and 1", srv.batchCalls.Load(), srv.byIDCalls.Load())
	}
}
