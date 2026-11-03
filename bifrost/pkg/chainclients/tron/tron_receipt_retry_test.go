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
	"time"

	"github.com/rs/zerolog"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type receiptRetryVaultValidator struct {
	*pubkeymanager.MockPoolAddressValidator
	address string
}

func (v receiptRetryVaultValidator) IsValidPoolAddress(address string, chain common.Chain) (bool, common.ChainPoolInfo) {
	if address != v.address || !chain.Equals(common.TRONChain) {
		return false, common.EmptyChainPoolInfo
	}
	return true, common.ChainPoolInfo{
		Chain:       chain,
		PubKey:      common.PubKey(pubkeymanager.MockPubkey),
		PoolAddress: common.Address(address),
	}
}

func TestFetchTxsRetriesReceiptFailure(t *testing.T) {
	const address = "41b8c702bc7c186ad437b798dae8c61727d3c69a85"
	vaultAddress, err := api.ConvertAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	firstID, targetID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	transaction := func(id, result string) string {
		return fmt.Sprintf(`{"txID":%q,"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"owner_address":%q,"to_address":%q,"amount":5000000}}}]},"ret":[{"contractRet":%q}]}`, id, address, address, result)
	}

	for _, result := range []string{"SUCCESS", "REVERT"} {
		for _, failure := range []struct {
			name   string
			status int
			body   string
		}{
			{"application", http.StatusOK, `{"Error":"lack of computing resources"}`},
			{"http", http.StatusServiceUnavailable, `unavailable`},
			{"malformed", http.StatusOK, `{`},
			{"empty", http.StatusOK, `{}`},
			{"missing height", http.StatusOK, fmt.Sprintf(`{"id":%q}`, targetID)},
			{"missing identity", http.StatusOK, `{"blockNumber":100}`},
			{"wrong identity", http.StatusOK, fmt.Sprintf(`{"id":%q,"blockNumber":100,"fee":700}`, firstID)},
			{"wrong height", http.StatusOK, fmt.Sprintf(`{"id":%q,"blockNumber":101,"fee":700}`, targetID)},
		} {
			t.Run(result+"/"+failure.name, func(t *testing.T) {
				var failReceipt atomic.Bool
				failReceipt.Store(true)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/wallet/getblockbynum":
						// The first transaction succeeds before the second receipt fails.
						// Retrying must recover both, without emitting a partial block.
						_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":100,"timestamp":1000}},"transactions":[%s,%s]}`,
							transaction(firstID, "SUCCESS"), transaction(targetID, result))
					case "/wallet/gettransactioninfobyid":
						var request struct {
							Value string `json:"value"`
						}
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						if request.Value == targetID && failReceipt.Load() {
							w.WriteHeader(failure.status)
							_, _ = w.Write([]byte(failure.body))
							return
						}
						// Zero fees are valid when bandwidth covers the transfer.
						fee := 100
						if request.Value == targetID {
							fee = 0
						}
						_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":100,"fee":%d}`, strings.ToUpper(request.Value), fee)
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				scanner := &TronBlockScanner{
					config: config.BifrostBlockScannerConfiguration{ChainID: common.TRONChain},
					logger: zerolog.Nop(),
					api:    api.NewTronApi(server.URL, time.Second),
					pubkeyMgr: receiptRetryVaultValidator{
						MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
						address:                  vaultAddress,
					},
				}

				txIn, err := scanner.FetchTxs(100, 1000)
				if !errors.Is(err, btypes.ErrUnavailableBlock) {
					t.Fatalf("receipt failure must keep the scanner at this height, got %v", err)
				}
				if len(txIn.TxArray) != 0 {
					t.Fatalf("receipt failure emitted %d partial observations", len(txIn.TxArray))
				}

				failReceipt.Store(false)
				txIn, err = scanner.FetchTxs(100, 1000)
				if err != nil {
					t.Fatal(err)
				}
				if len(txIn.TxArray) != 2 {
					t.Fatalf("retry recovered %d observations, want 2", len(txIn.TxArray))
				}
				for i, id := range []string{firstID, targetID} {
					item := txIn.TxArray[i]
					if item.Tx != id || item.BlockHeight != 100 {
						t.Fatalf("unexpected observation after retry: %+v", item)
					}
					wantGas := uint64(10000)
					if id == targetID {
						wantGas = 1
					}
					if len(item.Gas) != 1 || item.Gas[0].Amount.Uint64() != wantGas {
						t.Fatalf("receipt gas not recovered: %v", item.Gas)
					}
				}
				target := txIn.TxArray[1]
				if result == "REVERT" {
					if target.Memo != "OUT:"+strings.ToUpper(targetID) || target.Coins[0].Amount.Uint64() != 1 {
						t.Fatalf("failed outbound accounting not recovered: %+v", target)
					}
				} else if target.Coins[0].Amount.Uint64() != 500000000 {
					t.Fatalf("transfer amount not recovered: %s", target.Coins[0].Amount)
				}
			})
		}
	}
}
