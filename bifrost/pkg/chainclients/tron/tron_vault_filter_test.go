package tron

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type receiptFilterBridge struct {
	thorclient.ThorchainBridge
	keys []thorclient.PubKeyContractAddressPair
}

func (b receiptFilterBridge) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return b.keys, nil
}

func receiptFilterVaults(t *testing.T) (*pubkeymanager.PubKeyManager, string, string) {
	t.Helper()
	active := types.GetRandomPubKey()
	inactive := types.GetRandomPubKey()
	manager, err := pubkeymanager.NewPubKeyManager(receiptFilterBridge{keys: []thorclient.PubKeyContractAddressPair{
		{PubKey: active, Algo: common.SigningAlgoSecp256k1},
		{PubKey: inactive, Algo: common.SigningAlgoSecp256k1, Inactive: true},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Stop(); err != nil {
			t.Error(err)
		}
	})
	addresses := make([]string, 2)
	for i, key := range []common.PubKey{active, inactive} {
		address, err := key.GetAddress(common.TRONChain)
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := manager.IsValidPoolAddress(address.String(), common.TRONChain); !ok {
			t.Fatalf("fixture vault %s is not registered", address)
		}
		if ok, _ := manager.IsActivePoolAddress(address.String(), common.TRONChain); ok != (i == 0) {
			t.Fatalf("unexpected active status for fixture vault %s", address)
		}
		addresses[i], err = api.ConvertAddress(address.String())
		if err != nil {
			t.Fatal(err)
		}
	}
	return manager, addresses[0], addresses[1]
}

const receiptFilterContractAddress = "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs"

func receiptFilterTransaction(t *testing.T, asset, id, sender, destination string) api.Transaction {
	t.Helper()
	tx := api.Transaction{TxId: id, Ret: []api.Ret{{ContractRet: "SUCCESS"}}}
	tx.RawData.Data = hex.EncodeToString([]byte("vault-filter-test"))
	tx.RawData.Contract = []api.Contract{{Type: "TransferContract"}}
	contract := &tx.RawData.Contract[0]
	contract.Parameter.Value.OwnerAddress = sender
	contract.Parameter.Value.ToAddress = destination
	contract.Parameter.Value.Amount = 5_000_000
	if asset == "USDT" {
		contract.Type = "TriggerSmartContract"
		contract.Parameter.Value.ToAddress = ""
		contract.Parameter.Value.Amount = 0
		contractAddress, err := api.ConvertAddress(receiptFilterContractAddress)
		if err != nil {
			t.Fatal(err)
		}
		contract.Parameter.Value.ContractAddress = contractAddress
		tokenABI, err := abi.JSON(bytes.NewReader(trc20ContractABI))
		if err != nil {
			t.Fatal(err)
		}
		data, err := tokenABI.Pack("transfer", ethcommon.HexToAddress(destination[2:]), big.NewInt(5_000_000))
		if err != nil {
			t.Fatal(err)
		}
		contract.Parameter.Value.Data = hex.EncodeToString(data)
	}
	return tx
}

func receiptFilterScanner(t *testing.T, url string, manager pubkeymanager.PubKeyValidator) *TronBlockScanner {
	t.Helper()
	// A one second client timeout expired against the local httptest server
	// under race-detector load in CI. Nothing here waits on the timeout.
	scanner := &TronBlockScanner{
		config:    config.BifrostBlockScannerConfiguration{ChainID: common.TRONChain},
		logger:    zerolog.Nop(),
		api:       api.NewTronApi(url, 10*time.Second),
		pubkeyMgr: manager,
		whitelist: map[string]tokenlist.ERC20Token{
			receiptFilterContractAddress: {Name: "USDT", Symbol: "USDT", Address: receiptFilterContractAddress, Decimals: 6},
		},
	}
	var err error
	scanner.abi, err = abi.JSON(bytes.NewReader(trc20ContractABI))
	if err != nil {
		t.Fatal(err)
	}
	return scanner
}

func TestFetchTxsFiltersReceiptsToVaults(t *testing.T) {
	manager, active, inactive := receiptFilterVaults(t)
	external := "41" + strings.Repeat("a", 40)
	targetID := strings.Repeat("b", 64)
	for _, asset := range []string{"TRX", "USDT"} {
		for _, tc := range []struct {
			name, sender, destination string
			wantReceipts              int64
			unrelated                 int
		}{
			{"unrelated", external, external, 0, 0},
			{"inbound", external, active, 1, 0},
			{"outbound", active, external, 1, 0},
			{"inactive inbound", external, inactive, 1, 0},
			{"inactive outbound", inactive, external, 1, 0},
			{"migration", active, inactive, 1, 0},
			{"many unrelated transfers", external, active, 1, 100},
		} {
			t.Run(asset+"/"+tc.name, func(t *testing.T) {
				block := api.Block{}
				block.Header.RawData.Number = 100
				for i := range tc.unrelated {
					block.Transactions = append(block.Transactions, receiptFilterTransaction(t, asset, fmt.Sprintf("%064x", i), external, external))
				}
				block.Transactions = append(block.Transactions, receiptFilterTransaction(t, asset, targetID, tc.sender, tc.destination))
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/wallet/getblockbynum":
						_ = json.NewEncoder(w).Encode(block)
					case "/wallet/gettransactioninfobyid":
						calls.Add(1)
						var request struct{ Value string }
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						if tc.wantReceipts == 0 {
							// This broken unrelated receipt must never stall the scanner.
							_, _ = fmt.Fprint(w, `{"Error":"receipt unavailable"}`)
							return
						}
						_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":100,"fee":700}`, request.Value)
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				scanner := receiptFilterScanner(t, server.URL, manager)
				txIn, err := scanner.FetchTxs(100, 1000)
				if err != nil {
					t.Fatal(err)
				}
				if got := calls.Load(); got != tc.wantReceipts {
					t.Fatalf("receipt requests = %d, want %d", got, tc.wantReceipts)
				}
				if int64(len(txIn.TxArray)) != tc.wantReceipts {
					t.Fatalf("observations = %d, want %d", len(txIn.TxArray), tc.wantReceipts)
				}
				if txIn.Filtered {
					t.Fatal("observer must still attribute vaults and process internal transfers")
				}
				if tc.wantReceipts == 0 {
					return
				}
				item := txIn.TxArray[0]
				wantSender, err := api.ConvertAddress(tc.sender)
				if err != nil {
					t.Fatal(err)
				}
				wantDestination, err := api.ConvertAddress(tc.destination)
				if err != nil {
					t.Fatal(err)
				}
				if item.Tx != targetID || item.Sender != wantSender || item.To != wantDestination || !item.ObservedVaultPubKey.IsEmpty() {
					t.Fatalf("unexpected transfer or premature vault attribution: %+v", item)
				}
				if len(item.Gas) != 1 || item.Gas[0].Amount.Uint64() != 70000 || item.Coins[0].Amount.Uint64() != 500000000 {
					t.Fatalf("incorrect receipt gas or transfer amount: %+v", item)
				}
			})
		}
	}
}

func TestFetchTxsVaultReceiptRetry(t *testing.T) {
	manager, active, inactive := receiptFilterVaults(t)
	external := "41" + strings.Repeat("a", 40)
	firstID, targetID := strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, asset := range []string{"TRX", "USDT"} {
		t.Run(asset, func(t *testing.T) {
			block := api.Block{}
			block.Header.RawData.Number = 100
			block.Transactions = []api.Transaction{
				receiptFilterTransaction(t, asset, strings.Repeat("d", 64), external, external),
				receiptFilterTransaction(t, asset, firstID, external, active),
				receiptFilterTransaction(t, asset, targetID, inactive, external),
			}
			var failReceipt atomic.Bool
			failReceipt.Store(true)
			var calls atomic.Int64
			server := httptestServerForReceiptRetry(t, block, firstID, targetID, &failReceipt, &calls)
			defer server.Close()
			scanner := receiptFilterScanner(t, server.URL, manager)
			txIn, err := scanner.FetchTxs(100, 1000)
			if !errors.Is(err, btypes.ErrUnavailableBlock) || len(txIn.TxArray) != 0 || calls.Load() != 2 {
				t.Fatalf("failed vault receipt must retry the full block: err=%v, observations=%d, requests=%d", err, len(txIn.TxArray), calls.Load())
			}
			failReceipt.Store(false)
			txIn, err = scanner.FetchTxs(100, 1000)
			if err != nil || len(txIn.TxArray) != 2 || calls.Load() != 4 {
				t.Fatalf("retry must recover both vault transfers: err=%v, observations=%d, requests=%d", err, len(txIn.TxArray), calls.Load())
			}
			for i, id := range []string{firstID, targetID} {
				item := txIn.TxArray[i]
				if item.Tx != id || item.BlockHeight != 100 || len(item.Gas) != 1 || item.Gas[0].Amount.Uint64() != 70000 {
					t.Fatalf("incorrect transfer after retry: %+v", item)
				}
			}
		})
	}
}

func httptestServerForReceiptRetry(t *testing.T, block api.Block, firstID, targetID string, failReceipt *atomic.Bool, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getblockbynum":
			_ = json.NewEncoder(w).Encode(block)
		case "/wallet/gettransactioninfobyid":
			calls.Add(1)
			var request struct{ Value string }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if request.Value != firstID && request.Value != targetID {
				t.Error("unrelated receipt was requested")
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if request.Value == targetID && failReceipt.Load() {
				_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":101,"fee":700}`, request.Value)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":100,"fee":700}`, request.Value)
		default:
			http.NotFound(w, r)
		}
	}))
}
