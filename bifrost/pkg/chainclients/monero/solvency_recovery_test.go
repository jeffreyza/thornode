package monero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type recoverySolvencyBridge struct{ solvencyBridge }

func (*recoverySolvencyBridge) GetMimir(key string) (int64, error) {
	if key == "HaltScannerXMR" {
		return 0, nil
	}
	return -1, nil
}

func (*recoverySolvencyBridge) GetBlockHeight() (int64, error)                     { return 1000, nil }
func (*recoverySolvencyBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) { return 1, 1, nil }

func TestXMRSolvencyAfterAvailabilityRecovery(t *testing.T) {
	vaultKey, vaultAddress := testVaultPubKeyAndAddress(t)
	reconciled := true
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{vaultKey.String(): {
		Balance: moneroAtomicToThorchain, UnlockedBalance: moneroAtomicToThorchain, SpendableBalance: uint64Ptr(moneroAtomicToThorchain),
		Reconciled: &reconciled, ReconciliationState: "reconciled", PendingOutgoingCount: intPtr(0),
	}})
	defer balanceServer.Close()
	chainServer := newFrostTransfersServer(t, nil, nil, nil)
	defer chainServer.Close()
	tip := atomic.NewInt64(99)
	ready := atomic.NewBool(false)
	failures := atomic.NewInt64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: tip.Load()})
		case r.URL.Path == fmt.Sprintf("/v1/monero/chain/block/%d", tip.Load()) && tip.Load()%10 == 0 && !ready.Load():
			failures.Inc()
			http.Error(w, "block unavailable", http.StatusConflict)
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") || r.URL.Path == "/v1/monero/vaults/register":
			balanceServer.Config.Handler.ServeHTTP(w, r)
		default:
			chainServer.Config.Handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(zerolog.Nop(), signer, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.lastFee.Store(10)
	vault := ttypes.Vault{PubKey: vaultKey, PubKeyFrost: vaultKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)))}
	bridge := &recoverySolvencyBridge{solvencyBridge{asgards: ttypes.Vaults{vault}}}
	reports := make(chan stypes.Solvency, 4)
	client := &Client{
		logger: zerolog.Nop(), cfg: config.BifrostChainConfiguration{SolvencyBlocks: 10}, chain: common.XMRChain,
		scanner: scanner, sidecar: signer, thorchainBridge: bridge, globalSolvencyQueue: reports,
		vaultAddressMap: map[string]common.Address{vaultKey.String(): vaultAddress}, lastSolvencyCheckHeight: atomic.NewInt64(0),
	}
	callbackCalls := atomic.NewInt64(0)
	scanner.SetSolvencyReporter(func(height int64) error {
		callbackCalls.Inc()
		if !scanner.scanMu.TryLock() {
			return fmt.Errorf("solvency callback holds scanMu")
		}
		scanner.scanMu.Unlock()
		return client.ReportSolvency(height)
	})
	shared, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.XMRChain, StartBlockHeight: 98, BlockHeightDiscoverBackoff: time.Millisecond,
	}, blockscanner.NewMockScannerStorage(), &metrics.Metrics{}, bridge, scanner)
	if err != nil {
		t.Fatal(err)
	}
	client.blockScanner = shared
	shared.Start(make(chan stypes.TxIn, 4), make(chan common.NetworkFee, 4))
	defer shared.Stop()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !condition() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !condition() {
			t.Fatal("scanner did not reach expected state")
		}
	}
	wait(func() bool { return shared.PreviousHeight() == 99 && shared.IsHealthy() })
	height := int64(100)
	ready.Store(false)
	failures.Store(0)
	tip.Store(height)
	wait(func() bool { return failures.Load() >= 3 && !shared.IsHealthy() })
	if shared.PreviousHeight() != height-1 || len(reports) != 0 {
		t.Fatal("failed fetch advanced scanner or emitted solvency")
	}
	ready.Store(true)
	wait(func() bool { return shared.PreviousHeight() == height && shared.IsHealthy() })
	select {
	case report := <-reports:
		if report.Height != height || !report.PubKey.Equals(vaultKey) || !report.Chain.Equals(common.XMRChain) {
			t.Fatalf("unexpected insolvency report: %+v", report)
		}
		if !report.Coins.GetCoin(common.XMRAsset).Amount.LT(vault.Coins.GetCoin(common.XMRAsset).Amount) {
			t.Fatal("fixture must be insolvent")
		}
	default:
		t.Fatal("successful availability retry skipped insolvency report")
	}
	if len(reports) != 0 {
		t.Fatal("duplicate solvency report")
	}
	before := callbackCalls.Load()
	if _, err := scanner.FetchTxs(height, height); err != nil {
		t.Fatal(err)
	}
	if callbackCalls.Load() != before {
		t.Fatal("FetchTxs still invokes solvency outside the ordered loop")
	}
}
