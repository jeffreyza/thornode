package monero

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmath "cosmossdk.io/math"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptokeysed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"gitlab.com/thorchain/thornode/v3/common"
	thored25519 "gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const testMnemonic = "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

func TestAddressFromSpendKeyHexMatchesXMRAddress(t *testing.T) {
	spendKeyHex, expected := testSpendKeyHexAndAddress(t)

	got, err := addressFromSpendKeyHex(spendKeyHex)
	if err != nil {
		t.Fatalf("failed to derive address from spend key: %v", err)
	}

	if !got.Equals(expected) {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestAddressFromSpendKeyHexUsesWalletCompatibleMocknetAddress(t *testing.T) {
	spendKeyHex, _ := testSpendKeyHexAndAddress(t)

	got, err := addressFromSpendKeyHex(spendKeyHex)
	if err != nil {
		t.Fatalf("failed to derive address from spend key: %v", err)
	}

	if len(got.String()) != 95 {
		t.Fatalf("expected 95-char mocknet XMR address, got %d (%s)", len(got.String()), got)
	}
	if got.String()[0] != '4' {
		t.Fatalf("expected wallet-compatible mocknet XMR address prefix 4, got %s", got)
	}
	if common.IsXMRFeaturedGuaranteedAddress(got.String()) {
		t.Fatalf("expected mocknet XMR address to avoid featured+guaranteed format, got %s", got)
	}
}

func TestGetAccountUsesUnlockedUserBalanceAndTotalVaultBalance(t *testing.T) {
	spendKeyHex, address := testSpendKeyHexAndAddress(t)
	userUnlocked := sdkmath.NewUint(42 * common.One)
	vaultTotal := sdkmath.NewUint(77 * common.One)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/sim/balance":
			if got := r.Header.Get("Authorization"); got != "Bearer helper-token" {
				t.Fatalf("unexpected helper auth header: %s", got)
			}

			var req helperBalanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode helper balance request: %v", err)
			}
			if req.SpendKeyHex != spendKeyHex {
				t.Fatalf("expected spend key %s, got %s", spendKeyHex, req.SpendKeyHex)
			}
			if req.BirthdayHeight == nil || *req.BirthdayHeight != 123 {
				t.Fatalf("expected birthday height 123, got %v", req.BirthdayHeight)
			}

			_ = json.NewEncoder(w).Encode(helperBalanceResponse{
				Address:               address.String(),
				BalanceAtomic:         toAtomic(userUnlocked),
				UnlockedBalanceAtomic: toAtomic(userUnlocked),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/balance/thorpub1vaulttest":
			if got := r.Header.Get("Authorization"); got != "Bearer frost-token" {
				t.Fatalf("unexpected frost auth header: %s", got)
			}

			_ = json.NewEncoder(w).Encode(frostBalanceResponse{
				Balance:         toAtomic(vaultTotal),
				UnlockedBalance: toAtomic(userUnlocked),
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := &Client{
		chain:          common.XMRChain,
		address:        address,
		spendKeyHex:    spendKeyHex,
		helperEndpoint: server.URL,
		helperBearer:   "helper-token",
		frostEndpoint:  server.URL,
		frostBearer:    "frost-token",
		httpClient:     server.Client(),
	}
	client.SetBirthdayHeight(123)

	userAccount, err := client.GetAccount(nil)
	if err != nil {
		t.Fatalf("failed to get user account: %v", err)
	}
	if !userAccount.Coins.GetCoin(common.XMRAsset).Amount.Equal(userUnlocked) {
		t.Fatalf("expected unlocked user balance %s, got %s", userUnlocked, userAccount.Coins.GetCoin(common.XMRAsset).Amount)
	}

	vaultPubKey := common.PubKey("thorpub1vaulttest")
	vaultAccount, err := client.GetAccount(&vaultPubKey)
	if err != nil {
		t.Fatalf("failed to get vault account: %v", err)
	}
	if !vaultAccount.Coins.GetCoin(common.XMRAsset).Amount.Equal(vaultTotal) {
		t.Fatalf("expected vault balance %s, got %s", vaultTotal, vaultAccount.Coins.GetCoin(common.XMRAsset).Amount)
	}
}

func TestGetVaultBalanceReturnsTotalAndSpendableBalances(t *testing.T) {
	totalBalance := sdkmath.NewUint(77 * common.One)
	spendableBalance := sdkmath.NewUint(42 * common.One)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/monero/balance/thorpub1vaulttest" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer frost-token" {
			t.Fatalf("unexpected frost auth header: %s", got)
		}

		_ = json.NewEncoder(w).Encode(frostBalanceResponse{
			Balance:          toAtomic(totalBalance),
			UnlockedBalance:  0,
			SpendableBalance: ptrUint64(toAtomic(spendableBalance)),
		})
	}))
	defer server.Close()

	client := &Client{
		chain:         common.XMRChain,
		frostEndpoint: server.URL,
		frostBearer:   "frost-token",
		httpClient:    server.Client(),
	}

	total, spendable, err := client.GetVaultBalance(common.PubKey("thorpub1vaulttest"))
	if err != nil {
		t.Fatalf("failed to get vault balance: %v", err)
	}
	if !total.Equal(totalBalance) {
		t.Fatalf("expected total vault balance %s, got %s", totalBalance, total)
	}
	if !spendable.Equal(spendableBalance) {
		t.Fatalf("expected spendable vault balance %s, got %s", spendableBalance, spendable)
	}
}

func ptrUint64(v uint64) *uint64 {
	return &v
}

func TestGetHeightUsesHelperChainHeight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/monero/chain/height" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer helper-token" {
			t.Fatalf("unexpected helper auth header: %s", got)
		}

		_ = json.NewEncoder(w).Encode(chainHeightResponse{Height: 123})
	}))
	defer server.Close()

	client := &Client{
		helperEndpoint: server.URL,
		helperBearer:   "helper-token",
		httpClient:     server.Client(),
	}

	height, err := client.GetHeight()
	if err != nil {
		t.Fatalf("failed to get XMR chain height: %v", err)
	}
	if height != 123 {
		t.Fatalf("expected height 123, got %d", height)
	}
}

func TestSignAndBroadcastTxUsesHelperTransferContract(t *testing.T) {
	spendKeyHex, address := testSpendKeyHexAndAddress(t)
	swapAmount := sdkmath.NewUint(5 * common.One)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/sim/transfer" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer helper-token" {
			t.Fatalf("unexpected helper auth header: %s", got)
		}

		var req helperTransferRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode helper transfer request: %v", err)
		}
		if req.SpendKeyHex != spendKeyHex {
			t.Fatalf("expected spend key %s, got %s", spendKeyHex, req.SpendKeyHex)
		}
		if req.BirthdayHeight == nil || *req.BirthdayHeight != 456 {
			t.Fatalf("expected birthday height 456, got %v", req.BirthdayHeight)
		}
		if req.AmountAtomic != toAtomic(swapAmount) {
			t.Fatalf("expected atomic amount %d, got %d", toAtomic(swapAmount), req.AmountAtomic)
		}
		if req.Memo != "=:BTC.BTC:bc1qexample" {
			t.Fatalf("unexpected memo: %s", req.Memo)
		}

		_ = json.NewEncoder(w).Encode(helperTransferResponse{TxHash: "abc123"})
	}))
	defer server.Close()

	client := &Client{
		chain:          common.XMRChain,
		address:        address,
		spendKeyHex:    spendKeyHex,
		helperEndpoint: server.URL,
		helperBearer:   "helper-token",
		httpClient:     server.Client(),
	}
	client.SetBirthdayHeight(456)

	signed, err := client.SignTx(SimTx{
		Chain:     common.XMRChain,
		ToAddress: common.Address(common.XMRChecksummedDummyAddress('4')),
		Coin:      common.NewCoin(common.XMRAsset, swapAmount),
		Memo:      "=:BTC.BTC:bc1qexample",
	})
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}

	txHash, err := client.BroadcastTx(signed)
	if err != nil {
		t.Fatalf("failed to broadcast tx: %v", err)
	}
	if txHash != "abc123" {
		t.Fatalf("expected tx hash abc123, got %s", txHash)
	}
}

func TestFaucetRetriesTransientWalletStartupError(t *testing.T) {
	spendKeyHex, address := testSpendKeyHexAndAddress(t)
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/sim/faucet" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":"wallet-rpc refresh failed: wallet-rpc refresh failed with -1: Unexpected hard fork version v16 at height 1. Make sure the node you are connected to is running the latest version"}`)
			return
		}

		_ = json.NewEncoder(w).Encode(helperTransferResponse{TxHash: "faucet123"})
	}))
	defer server.Close()

	client := &Client{
		chain:          common.XMRChain,
		address:        address,
		spendKeyHex:    spendKeyHex,
		helperEndpoint: server.URL,
		helperBearer:   "helper-token",
		httpClient:     server.Client(),
	}

	txHash, err := client.Faucet(address, sdkmath.NewUint(common.One))
	if err != nil {
		t.Fatalf("expected faucet to recover from transient wallet startup error, got %v", err)
	}
	if txHash != "faucet123" {
		t.Fatalf("expected tx hash faucet123, got %s", txHash)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 faucet attempts, got %d", attempts)
	}
}

func TestFaucetRetriesTransientUnlockedMoneyError(t *testing.T) {
	spendKeyHex, address := testSpendKeyHexAndAddress(t)
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/sim/faucet" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":"wallet-rpc transfer failed with -37: not enough unlocked money"}`)
			return
		}

		_ = json.NewEncoder(w).Encode(helperTransferResponse{TxHash: "faucet456"})
	}))
	defer server.Close()

	client := &Client{
		chain:          common.XMRChain,
		address:        address,
		spendKeyHex:    spendKeyHex,
		helperEndpoint: server.URL,
		helperBearer:   "helper-token",
		httpClient:     server.Client(),
	}

	txHash, err := client.Faucet(address, sdkmath.NewUint(common.One))
	if err != nil {
		t.Fatalf("expected faucet to recover from transient unlocked-balance error, got %v", err)
	}
	if txHash != "faucet456" {
		t.Fatalf("expected tx hash faucet456, got %s", txHash)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 faucet attempts, got %d", attempts)
	}
}

func testSpendKeyHexAndAddress(t *testing.T) (string, common.Address) {
	t.Helper()

	derivedPriv, err := thored25519.Ed25519.Derive()(testMnemonic, "", thored25519.HDPath)
	if err != nil {
		t.Fatalf("failed to derive Ed25519 private key: %v", err)
	}
	privKey := thored25519.Ed25519.Generate()(derivedPriv).(*cryptokeysed25519.PrivKey)

	pk, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		t.Fatalf("failed to derive Ed25519 public key: %v", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		t.Fatalf("failed to derive THORChain pubkey: %v", err)
	}
	address, err := pubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("failed to derive XMR address: %v", err)
	}

	return hex.EncodeToString(privKey.Bytes()[:32]), address
}
