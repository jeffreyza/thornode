package tron

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type recoverySolvencyBridge struct{ mockTronSolvencyBridge }

func (*recoverySolvencyBridge) GetMimir(string) (int64, error)                     { return -1, nil }
func (*recoverySolvencyBridge) GetBlockHeight() (int64, error)                     { return 1000, nil }
func (*recoverySolvencyBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) { return 1, 1, nil }

func TestTRONSolvencyAfterReceiptRecovery(t *testing.T) {
	const height int64 = 100
	const address = "41b8c702bc7c186ad437b798dae8c61727d3c69a85"
	vaultAddress, err := api.ConvertAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	txID := strings.Repeat("a", 64)
	var tip, failedReceipts atomic.Int64
	var receiptReady atomic.Bool
	tip.Store(height - 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getnowblock":
			_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":%d}}}`, tip.Load()+ConfirmationBlocks)
		case "/wallet/getblockbynum":
			var request struct {
				Num int64 `json:"num"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil {
				http.Error(w, decodeErr.Error(), 400)
				return
			}
			if request.Num != height {
				_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":%d}}}`, request.Num)
				return
			}
			_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":100,"timestamp":1000}},"transactions":[{"txID":%q,"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"owner_address":%q,"to_address":%q,"amount":5000000}}}]},"ret":[{"contractRet":"SUCCESS"}]}]}`, txID, address, address)
		case "/wallet/gettransactioninfobyid":
			if !receiptReady.Load() {
				failedReceipts.Add(1)
				_, _ = fmt.Fprint(w, `{"Error":"receipt unavailable"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":100,"fee":700}`, txID)
		case "/wallet/getaccount":
			_, _ = fmt.Fprint(w, `{"balance":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	vault := ttypes.Vault{PubKey: ttypes.GetRandomPubKey(), Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(common.One)))}
	bridge := &recoverySolvencyBridge{mockTronSolvencyBridge{asgards: ttypes.Vaults{vault}}}
	scanner := &TronBlockScanner{
		config: config.BifrostBlockScannerConfiguration{ChainID: common.TRONChain, ObservationFlexibilityBlocks: 3},
		logger: zerolog.Nop(), api: api.NewTronApi(server.URL, time.Second),
		pubkeyMgr: receiptRetryVaultValidator{MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(), address: vaultAddress},
	}
	reports := make(chan stypes.Solvency, 4)
	client := &TronClient{
		logger: zerolog.Nop(), config: config.BifrostChainConfiguration{ChainID: common.TRONChain}, chainId: common.TRONChain.String(),
		tronScanner: scanner, bridge: bridge, api: scanner.api, globalSolvencyQueue: reports,
	}
	scanner.reportSolvency = client.ReportSolvency
	shared, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.TRONChain, StartBlockHeight: height - 2, BlockHeightDiscoverBackoff: time.Millisecond,
	}, blockscanner.NewMockScannerStorage(), &metrics.Metrics{}, bridge, scanner)
	if err != nil {
		t.Fatal(err)
	}
	client.blockScanner = shared
	shared.Start(make(chan stypes.TxIn, 4), make(chan common.NetworkFee, 4))
	defer shared.Stop()
	wait := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !ready() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !ready() {
			t.Fatal("scanner did not reach expected state")
		}
	}
	wait(func() bool { return shared.PreviousHeight() == height-1 && shared.IsHealthy() })
	tip.Store(height)
	wait(func() bool { return failedReceipts.Load() >= 3 && !shared.IsHealthy() })
	if shared.PreviousHeight() != height-1 || len(reports) != 0 {
		t.Fatal("failed receipt advanced scanner or emitted solvency")
	}
	receiptReady.Store(true)
	wait(func() bool { return shared.PreviousHeight() == height && shared.IsHealthy() })
	select {
	case report := <-reports:
		if report.Height != height-FinalityBlocks || !report.PubKey.Equals(vault.PubKey) || !report.Chain.Equals(common.TRONChain) {
			t.Fatalf("unexpected insolvency report: %+v", report)
		}
		if !report.Coins.GetCoin(common.TRXAsset).Amount.LT(vault.Coins.GetCoin(common.TRXAsset).Amount) {
			t.Fatal("fixture must be insolvent")
		}
	default:
		t.Fatal("successful receipt retry skipped insolvency report")
	}
	if len(reports) != 0 {
		t.Fatal("duplicate solvency report")
	}
	// FetchTxs can also run in a prefetch worker. It must not report on its own.
	if _, err := scanner.FetchTxs(height, height); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Fatal("FetchTxs still reports outside the ordered loop")
	}
}

func TestTRONSolvencyHookUsesTipAndFlexibility(t *testing.T) {
	client := &TronClient{}
	var reported []int64
	scanner := &TronBlockScanner{
		config: config.BifrostBlockScannerConfiguration{ObservationFlexibilityBlocks: 2},
		reportSolvency: func(height int64) error {
			if client.ShouldReportSolvency(height) {
				reported = append(reported, height)
			}
			return nil
		},
	}
	for _, pair := range [][2]int64{{97, 100}, {99, 101}, {98, 100}} {
		if err := scanner.ReportSolvency(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if len(reported) != 1 || reported[0] != 100 {
		t.Fatalf("unexpected tip-based solvency heights: %v", reported)
	}
}
