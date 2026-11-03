package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func intPtr(v int) *int { return &v }

func TestShouldReportSolvency(t *testing.T) {
	c := &Client{
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(3)},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if c.ShouldReportSolvency(9) {
		t.Fatal("should not report on non-solvency block")
	}
	if !c.ShouldReportSolvency(10) {
		t.Fatal("should report on solvency block")
	}

	c.lastSolvencyCheckHeight.Store(10)
	if c.ShouldReportSolvency(10) {
		t.Fatal("should not report at same height twice")
	}

	c.scanner.lastFee.Store(0)
	if c.ShouldReportSolvency(20) {
		t.Fatal("should not report when fee is unknown")
	}
}

func TestShouldReportSolvencyRequiresInitializedState(t *testing.T) {
	c := &Client{
		cfg: config.BifrostChainConfiguration{SolvencyBlocks: 10},
	}
	if c.ShouldReportSolvency(10) {
		t.Fatal("should not report when scanner and solvency state are not initialized")
	}
}

func TestReportSolvencySkipsUnreconciledSidecarBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"balance":                100 * moneroAtomicToThorchain,
				"unlocked_balance":       100 * moneroAtomicToThorchain,
				"spendable_balance":      100 * moneroAtomicToThorchain,
				"reconciled":             false,
				"pending_outgoing_count": 1,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		balanceServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	queue := make(chan stypes.Solvency, 1)
	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:                   common.XMRChain,
		thorchainBridge:         &solvencyBridge{asgards: ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000)))}}},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(10)},
		sidecar:                 frostSigner,
		globalSolvencyQueue:     queue,
		vaultAddressMap:         map[string]common.Address{vaultPubKey.String(): vaultAddress},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("expected unreconciled balance to defer solvency report, got %d messages", len(queue))
	}
	if got := c.lastSolvencyCheckHeight.Load(); got != 0 {
		t.Fatalf("unreconciled balance should not consume solvency height, got %d", got)
	}
}

func TestReportSolvencyReportsReconciledVaultWhenSiblingUnreconciled(t *testing.T) {
	t.Parallel()

	unreconciledPubKey, unreconciledAddress := testVaultPubKeyAndAddress(t)
	reconciledPubKey, reconciledAddress := testVaultPubKeyAndAddress(t)
	reconciled := true
	unreconciled := false
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		unreconciledPubKey.String(): {
			Balance:              100 * moneroAtomicToThorchain,
			UnlockedBalance:      100 * moneroAtomicToThorchain,
			SpendableBalance:     uint64Ptr(100 * moneroAtomicToThorchain),
			Reconciled:           &unreconciled,
			ReconciliationState:  "pending_outgoing",
			PendingOutgoingCount: intPtr(1),
		},
		reconciledPubKey.String(): {
			Balance:              10 * moneroAtomicToThorchain,
			UnlockedBalance:      10 * moneroAtomicToThorchain,
			SpendableBalance:     uint64Ptr(10 * moneroAtomicToThorchain),
			Reconciled:           &reconciled,
			ReconciliationState:  "reconciled",
			PendingOutgoingCount: intPtr(0),
		},
	})
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	queue := make(chan stypes.Solvency, 2)
	c := &Client{
		logger: log.Logger,
		cfg:    config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:  common.XMRChain,
		thorchainBridge: &solvencyBridge{asgards: ttypes.Vaults{
			{PubKey: unreconciledPubKey, PubKeyEddsa: unreconciledPubKey, PubKeyFrost: unreconciledPubKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100)))},
			{PubKey: reconciledPubKey, PubKeyEddsa: reconciledPubKey, PubKeyFrost: reconciledPubKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500)))},
		}},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(10)},
		sidecar:                 frostSigner,
		globalSolvencyQueue:     queue,
		vaultAddressMap:         map[string]common.Address{unreconciledPubKey.String(): unreconciledAddress, reconciledPubKey.String(): reconciledAddress},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("expected reconciled insolvent vault to report, got %d messages", len(queue))
	}
	msg := <-queue
	if !msg.PubKey.Equals(reconciledPubKey) {
		t.Fatalf("expected solvency message for reconciled vault %s, got %s", reconciledPubKey, msg.PubKey)
	}
	if got := c.lastSolvencyCheckHeight.Load(); got != 10 {
		t.Fatalf("expected mixed report to consume solvency height 10, got %d", got)
	}
}

func TestReportSolvencyResumesAfterUnreconciledSidecarBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	reconciled := true
	unreconciled := false
	balanceCalls := 0
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			balanceCalls++
			resp := sidecar.Balance{
				Balance:              100 * moneroAtomicToThorchain,
				UnlockedBalance:      100 * moneroAtomicToThorchain,
				SpendableBalance:     uint64Ptr(100 * moneroAtomicToThorchain),
				Reconciled:           &reconciled,
				ReconciliationState:  "reconciled",
				PendingOutgoingCount: intPtr(0),
			}
			if balanceCalls == 1 {
				resp.Reconciled = &unreconciled
				resp.ReconciliationState = "pending_outgoing"
				resp.PendingOutgoingCount = intPtr(1)
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	queue := make(chan stypes.Solvency, 1)
	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:                   common.XMRChain,
		thorchainBridge:         &solvencyBridge{solvencyHalted: true, asgards: ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000)))}}},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(10)},
		sidecar:                 frostSigner,
		globalSolvencyQueue:     queue,
		vaultAddressMap:         map[string]common.Address{vaultPubKey.String(): vaultAddress},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("first report solvency: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("expected unreconciled balance to defer first solvency report, got %d messages", len(queue))
	}
	if got := c.lastSolvencyCheckHeight.Load(); got != 0 {
		t.Fatalf("unreconciled balance should not consume solvency height, got %d", got)
	}

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("second report solvency: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("expected reconciled balance to resume solvency report, got %d messages", len(queue))
	}
	if got := c.lastSolvencyCheckHeight.Load(); got != 10 {
		t.Fatalf("expected resumed report to consume solvency height 10, got %d", got)
	}
}

func TestReportSolvencyAutoUnhaltUsesCheckedVaultCount(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:              100 * moneroAtomicToThorchain,
			UnlockedBalance:      100 * moneroAtomicToThorchain,
			SpendableBalance:     uint64Ptr(100 * moneroAtomicToThorchain),
			ReconciliationState:  "reconciled",
			PendingOutgoingCount: intPtr(0),
		},
	})
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		balanceServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	queue := make(chan stypes.Solvency, 2)
	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:                   common.XMRChain,
		thorchainBridge:         &solvencyBridge{solvencyHalted: true, asgards: ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100)))}, {PubKey: common.EmptyPubKey, PubKeyEddsa: common.EmptyPubKey, PubKeyFrost: common.EmptyPubKey}}},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(10)},
		sidecar:                 frostSigner,
		globalSolvencyQueue:     queue,
		vaultAddressMap:         map[string]common.Address{vaultPubKey.String(): vaultAddress},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("expected one solvent auto-unhalt message, got %d", len(queue))
	}
	msg := <-queue
	if !msg.PubKey.Equals(vaultPubKey) {
		t.Fatalf("expected solvency message for checked vault %s, got %s", vaultPubKey, msg.PubKey)
	}
}

func TestReportSolvencyDeferralEscalationCounter(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	var reconciledFlag atomic.Bool
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			pending := 1
			state := "pending"
			if reconciledFlag.Load() {
				pending = 0
				state = "reconciled"
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"balance":                100 * moneroAtomicToThorchain,
				"unlocked_balance":       100 * moneroAtomicToThorchain,
				"spendable_balance":      100 * moneroAtomicToThorchain,
				"reconciled":             reconciledFlag.Load(),
				"reconciliation_state":   state,
				"pending_outgoing_count": pending,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	queue := make(chan stypes.Solvency, 2)
	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:                   common.XMRChain,
		thorchainBridge:         &solvencyBridge{asgards: ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100*moneroAtomicToThorchain/1_0000)))}}},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(10)},
		sidecar:                 frostSigner,
		globalSolvencyQueue:     queue,
		vaultAddressMap:         map[string]common.Address{vaultPubKey.String(): vaultAddress},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	deferralRounds := func() int {
		c.solvencyDeferralMu.Lock()
		defer c.solvencyDeferralMu.Unlock()
		return c.solvencyDeferralRounds[vaultAddress.String()]
	}

	// consecutive deferred rounds accumulate (height cursor is preserved, so
	// each solvency block retries)
	for i, height := range []int64{10, 20, 30} {
		if err := c.ReportSolvency(height); err != nil {
			t.Fatalf("report solvency at %d: %v", height, err)
		}
		if got := deferralRounds(); got != i+1 {
			t.Fatalf("expected %d consecutive deferral rounds at height %d, got %d", i+1, height, got)
		}
	}

	// a reconciled report clears the streak
	reconciledFlag.Store(true)
	if err := c.ReportSolvency(40); err != nil {
		t.Fatalf("report solvency after reconcile: %v", err)
	}
	if got := deferralRounds(); got != 0 {
		t.Fatalf("expected deferral rounds cleared after reconciled report, got %d", got)
	}
}
