package monero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const nonGuaranteedFeaturedSubaddress = "CjWdTpuDaZ69nTGxzm9YarR82YDYFECi1WaaREZTMy5yDsjaRX5bC3cbC3JpcrBPd7YYpjoWKuBMidgGaKBK5Jye2v3wfMHCy"

func TestMaxInputsFromMimirCapsConsensusSpentRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mimirValue int64
		want       int64
	}{
		{name: "default", mimirValue: 0, want: 15},
		{name: "negative ignored", mimirValue: -1, want: 15},
		{name: "below cap", mimirValue: 20, want: 20},
		{name: "at cap", mimirValue: int64(common.MaxXMRSpentOutputRefs), want: int64(common.MaxXMRSpentOutputRefs)},
		{name: "above cap", mimirValue: int64(common.MaxXMRSpentOutputRefs) + 1, want: int64(common.MaxXMRSpentOutputRefs)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := maxInputsFromMimir(tt.mimirValue); got != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestValidateXMRSignTxRejectsInboundMemo(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "DONATE:XMR.XMR",
	}

	_, err := c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected inbound memo validation error")
	}
	if !strings.Contains(err.Error(), "inbound memo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxAcceptsCheckpoint(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	ckpt, _ := json.Marshal(signCheckpoint{
		SpentOutputRefs: []sidecar.SpentRef{{
			TxHash:      "aabb",
			OutputIndex: 0,
			KeyImage:    "ki_aabb",
			SpendTxHash: "spend_tx",
		}},
	})
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo:       "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		Checkpoint: ckpt,
	}

	_, err := c.validateSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("checkpoint should be accepted: %v", err)
	}
}

func TestValidateXMRSignTxRejectsFeaturedDestination(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        common.Address(common.XMRChecksummedDummyAddress('C')),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err := c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("featured addresses should be rejected as unsupported signing destinations")
	}
}

func TestValidateXMRSignTxRejectsNonGuaranteedFeaturedDestination(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        common.Address(nonGuaranteedFeaturedSubaddress),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err := c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("non-guaranteed featured addresses should be rejected as unsupported signing destinations")
	}
}

func TestValidateXMRSignTxRejectsInsufficientBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)

	// Mock frost signer server that returns low balance
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
			return
		}
		if strings.Contains(r.URL.Path, "/v1/monero/balance/") {
			_ = json.NewEncoder(w).Encode(sidecar.Balance{
				Balance:         2 * moneroAtomicToThorchain,
				UnlockedBalance: 2 * moneroAtomicToThorchain,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: frostSigner,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected insufficient balance error")
	}
	if !strings.Contains(err.Error(), "insufficient xmr balance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxRejectsLockedOnlyBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)

	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
			return
		}
		if strings.Contains(r.URL.Path, "/v1/monero/balance/") {
			_ = json.NewEncoder(w).Encode(sidecar.Balance{
				Balance:         10 * moneroAtomicToThorchain,
				UnlockedBalance: 0,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: frostSigner,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected insufficient balance error when funds are fully locked")
	}
	if !strings.Contains(err.Error(), "insufficient xmr balance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxRejectsInsufficientSpendableBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)

	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:          10 * moneroAtomicToThorchain,
			UnlockedBalance:  10 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(moneroAtomicToThorchain),
		},
	})
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: frostSigner,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected insufficient balance error when spendable balance is below amount")
	}
	if !strings.Contains(err.Error(), "insufficient xmr balance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxRequiresFrostVaultPubKey(t *testing.T) {
	t.Parallel()

	eddsaPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		eddsaPubKey.String(): {
			Balance:          5 * moneroAtomicToThorchain,
			UnlockedBalance:  5 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(5 * moneroAtomicToThorchain),
		},
	})
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:           common.XMRChain,
		sidecar:         frostSigner,
		thorchainBridge: &mockMoneroBridge{vaultByPubKey: map[string]ttypes.Vault{eddsaPubKey.String(): {PubKeyEddsa: eddsaPubKey}}},
		vaultAddressMap: map[string]common.Address{
			eddsaPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKeyEddsa: eddsaPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateSignTx(tx, 100, true)
	if err == nil {
		t.Fatal("expected sign validation to fail closed without VaultPubKeyFrost")
	}
	if !strings.Contains(err.Error(), "vault pubkey is empty") {
		t.Fatalf("expected missing frost vault pubkey error, got %v", err)
	}
}

func TestValidateXMRSignTxAcceptsValidRequest(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	req, err := c.validateSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("expected valid sign request, got error: %v", err)
	}
	if req.amountAtomic == 0 || req.fromAddress == "" || req.toAddress == "" || req.vaultPubKey.IsEmpty() {
		t.Fatalf("expected fully populated validated sign request")
	}
}

// newFrostBalanceServer creates a mock sidecar server that serves balance requests.
func newFrostBalanceServer(t *testing.T, balanceByPubKey map[string]sidecar.Balance) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/monero/keys" {
			vaults := make([]sidecar.VaultKey, 0, len(balanceByPubKey))
			for pubKey := range balanceByPubKey {
				vault, err := common.NewPubKey(pubKey)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				groupKey, err := groupKeyHexFromVaultPubKey(vault)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				vaults = append(vaults, sidecar.VaultKey{VaultPubKey: pubKey, GroupKey: groupKey, HasFrostKeys: true, Signable: true})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"vaults": vaults})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
			return
		}
		// Path format: /v1/monero/balance/{vault_pub_key}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			pk := strings.TrimPrefix(r.URL.Path, "/v1/monero/balance/")
			resp, ok := balanceByPubKey[pk]
			if !ok {
				http.Error(w, fmt.Sprintf("unknown vault pubkey: %s", pk), http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func TestValidateXMRSignTxRequiresOriginalMemoForNewPayout(t *testing.T) {
	client := &Client{chain: common.XMRChain}
	_, err := client.validateSignTx(stypes.TxOutItem{Chain: common.XMRChain, ToAddress: testOutboundXMRDestination()}, 100, true)
	if err == nil || !strings.Contains(err.Error(), "requires its original memo") {
		t.Fatalf("missing memo admitted: %v", err)
	}
}
