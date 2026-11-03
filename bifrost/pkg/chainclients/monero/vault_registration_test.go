package monero

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestGroupKeyHexFromVaultPubKey(t *testing.T) {
	t.Parallel()

	raw := sha256.Sum256([]byte("vault-registration-group-key"))
	vaultPubKey, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(raw[:]))
	if err != nil {
		t.Fatalf("new pub key: %v", err)
	}

	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("groupKeyHexFromVaultPubKey failed: %v", err)
	}

	expected := hex.EncodeToString(raw[:])
	if groupKeyHex != expected {
		t.Fatalf("expected group key %s, got %s", expected, groupKeyHex)
	}
}

func TestVaultPubKeyFromGroupKeyHex(t *testing.T) {
	t.Parallel()

	raw := sha256.Sum256([]byte("vault-registration-pubkey-from-group-key"))
	expectedPubKey, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(raw[:]))
	if err != nil {
		t.Fatalf("new pub key: %v", err)
	}

	pubKey, err := vaultPubKeyFromGroupKeyHex(hex.EncodeToString(raw[:]))
	if err != nil {
		t.Fatalf("vaultPubKeyFromGroupKeyHex failed: %v", err)
	}
	if !pubKey.Equals(expectedPubKey) {
		t.Fatalf("expected pubkey %s, got %s", expectedPubKey, pubKey)
	}
}

func TestSetVaultAddressMappingInitializesZeroValueCaches(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "zero-value-vault-address-cache")
	address, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}
	client := &Client{logger: log.Logger, chain: common.XMRChain}

	client.setVaultAddressMapping(vaultPubKey, address)

	if got, ok := client.vaultAddressMap[vaultPubKey.String()]; !ok || !got.Equals(address) {
		t.Fatalf("forward vault mapping was not initialized: %v", got)
	}
	if got := client.vaultAddrToKey[address.String()]; got != vaultPubKey.String() {
		t.Fatalf("reverse vault mapping was not initialized: %q", got)
	}
}

func TestSetVaultAddressMappingRepairsMissingReverseCache(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "missing-reverse-vault-address-cache")
	address, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}
	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): address},
	}

	client.setVaultAddressMapping(vaultPubKey, address)

	if got := client.vaultAddrToKey[address.String()]; got != vaultPubKey.String() {
		t.Fatalf("reverse vault mapping was not repaired: %q", got)
	}
}

func TestClientEnsureXMRVaultBalanceRegistersVault(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "ensure-xmr-vault-balance")
	address, deriveErr := vaultPubKey.GetAddress(common.XMRChain)
	if deriveErr != nil {
		t.Fatalf("derive address: %v", deriveErr)
	}
	expectedGroupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}

	var mu sync.Mutex
	registered := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req sidecar.RegisterVaultReq
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			if req.VaultPubKey != vaultPubKey.String() {
				http.Error(w, "unexpected vault pubkey", http.StatusBadRequest)
				return
			}
			if req.GroupKey != expectedGroupKey {
				http.Error(w, "unexpected group key", http.StatusBadRequest)
				return
			}
			if req.Address != address.String() {
				http.Error(w, "unexpected address", http.StatusBadRequest)
				return
			}
			if req.Source != "bifrost" {
				http.Error(w, "unexpected source", http.StatusBadRequest)
				return
			}
			mu.Lock()
			registered = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
		case "/v1/monero/balance/" + vaultPubKey.String():
			mu.Lock()
			isRegistered := registered
			mu.Unlock()
			if !isRegistered {
				http.Error(w, "vault not registered", http.StatusPreconditionFailed)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.Balance{
				Balance:         5000,
				UnlockedBalance: 5000,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		sidecar:         signer,
		vaultAddressMap: make(map[string]common.Address),
	}

	if err := client.ensureVaultBalance(vaultPubKey, 1000, 0); err != nil {
		t.Fatalf("ensureVaultBalance failed: %v", err)
	}

	mapped, ok := client.cachedVaultAddress(vaultPubKey)
	if !ok {
		t.Fatal("expected vault address mapping to be cached")
	}
	if !mapped.Equals(address) {
		t.Fatalf("expected mapped address %s, got %s", address, mapped)
	}
}

func TestEnsureFrostVaultRegisteredIncludesBootstrapSpendKeyForMatchingVault(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "matching-bootstrap-spend-key")
	address, deriveErr := vaultPubKey.GetAddress(common.XMRChain)
	if deriveErr != nil {
		t.Fatalf("derive address: %v", deriveErr)
	}
	expectedGroupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	expectedSpendKeyHex := strings.Repeat("ab", 32)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}

		var req sidecar.RegisterVaultReq
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			http.Error(w, decodeErr.Error(), http.StatusBadRequest)
			return
		}
		if req.VaultPubKey != vaultPubKey.String() {
			http.Error(w, "unexpected vault pubkey", http.StatusBadRequest)
			return
		}
		if req.GroupKey != expectedGroupKey {
			http.Error(w, "unexpected group key", http.StatusBadRequest)
			return
		}
		if req.Address != address.String() {
			http.Error(w, "unexpected address", http.StatusBadRequest)
			return
		}
		if req.SpendKeyHex != expectedSpendKeyHex {
			http.Error(w, "unexpected spend key", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bootstrap := &vaultBootstrap{
		vaultPubKey: vaultPubKey,
		spendKeyHex: expectedSpendKeyHex,
	}
	registeredAddress, err := ensureVaultRegistered(log.Logger, signer, common.XMRChain, vaultPubKey, bootstrap, "test", 0)
	if err != nil {
		t.Fatalf("ensureVaultRegistered failed: %v", err)
	}
	if !registeredAddress.Equals(address) {
		t.Fatalf("expected address %s, got %s", address, registeredAddress)
	}
}

func TestEnsureFrostVaultRegisteredOmitsBootstrapSpendKeyForOtherVault(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "non-matching-bootstrap-spend-key")
	address, deriveErr := vaultPubKey.GetAddress(common.XMRChain)
	if deriveErr != nil {
		t.Fatalf("derive address: %v", deriveErr)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}

		var req sidecar.RegisterVaultReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.SpendKeyHex != "" {
			http.Error(w, "expected empty spend key", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bootstrap := &vaultBootstrap{
		vaultPubKey: testVaultPubKey(t, "different-bootstrap-vault"),
		spendKeyHex: strings.Repeat("cd", 32),
	}
	registeredAddress, err := ensureVaultRegistered(log.Logger, signer, common.XMRChain, vaultPubKey, bootstrap, "test", 0)
	if err != nil {
		t.Fatalf("ensureVaultRegistered failed: %v", err)
	}
	if !registeredAddress.Equals(address) {
		t.Fatalf("expected address %s, got %s", address, registeredAddress)
	}
}

func TestGetAccountByAddressRegistersVaultBeforeBalanceQuery(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "account-query-registers-vault")
	address, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}

	var mu sync.Mutex
	registered := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/vaults/register":
			mu.Lock()
			registered = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			mu.Lock()
			isRegistered := registered
			mu.Unlock()
			if !isRegistered {
				http.Error(w, "vault not registered", http.StatusPreconditionFailed)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.Balance{
				Balance:         7 * moneroAtomicToThorchain,
				UnlockedBalance: 7 * moneroAtomicToThorchain,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		sidecar:         signer,
		vaultAddressMap: make(map[string]common.Address),
	}
	client.setVaultAddressMapping(vaultPubKey, address)

	account, err := client.GetAccountByAddress(address.String(), nil)
	if err != nil {
		t.Fatalf("GetAccountByAddress failed: %v", err)
	}

	coin := account.Coins.GetCoin(common.XMRAsset)
	if coin.Amount.Uint64() != 7 {
		t.Fatalf("expected 7 XMR units, got %s", coin.Amount.String())
	}
}

func TestGetAccountByAddressUsesTotalBalanceWhenSpendableAlsoProvided(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "account-query-uses-spendable-balance")
	address, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			_ = json.NewEncoder(w).Encode(sidecar.Balance{
				Balance:          9 * moneroAtomicToThorchain,
				UnlockedBalance:  9 * moneroAtomicToThorchain,
				SpendableBalance: uint64Ptr(3 * moneroAtomicToThorchain),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		sidecar:         signer,
		vaultAddressMap: make(map[string]common.Address),
	}
	client.setVaultAddressMapping(vaultPubKey, address)

	account, err := client.GetAccountByAddress(address.String(), nil)
	if err != nil {
		t.Fatalf("GetAccountByAddress failed: %v", err)
	}

	coin := account.Coins.GetCoin(common.XMRAsset)
	if coin.Amount.Uint64() != 9 {
		t.Fatalf("expected total 9 XMR units, got %s", coin.Amount.String())
	}
}

// TestResolveVaultAddressOnlyMapsFrostKey verifies that resolveVaultAddress
// only stores the frost key in the address map, not the eddsa key. If the
// eddsa key leaks into the map, GetAccountByAddress will call
// ensureVaultRegistered with the eddsa key, causing the sidecar to
// return 500 (conflicting registration).
func TestResolveVaultAddressOnlyMapsFrostKey(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-resolve-addr")
	eddsaKey := testVaultPubKey(t, "eddsa-key-resolve-addr")
	ecdsaKey := testVaultPubKey(t, "ecdsa-key-resolve-addr")

	c := &Client{
		chain:           common.XMRChain,
		logger:          testLogger(),
		vaultAddressMap: make(map[string]common.Address),
		vaultAddrToKey:  make(map[string]string),
	}

	vault := ttypes.Vault{
		PubKey:      ecdsaKey,
		PubKeyEddsa: eddsaKey,
		PubKeyFrost: frostKey,
	}

	addr := c.resolveVaultAddress(vault)
	require.False(t, addr.IsEmpty(), "resolveVaultAddress should return a valid address")

	// The frost key should be in the address map.
	_, frostMapped := c.vaultAddressMap[frostKey.String()]
	assert.True(t, frostMapped, "frost key should be mapped to the vault address")

	// The eddsa key must NOT be in the address map.
	_, eddsaMapped := c.vaultAddressMap[eddsaKey.String()]
	assert.False(t, eddsaMapped,
		"BUG: eddsa key was stored in address map — this causes double registration on the sidecar")

	// The reverse lookup must point to the frost key, not eddsa.
	pubKey, found := c.findVaultPubKeyByAddress(addr.String())
	require.True(t, found)
	assert.Equal(t, frostKey, pubKey,
		"findVaultPubKeyByAddress should return the frost key, not eddsa")
	_, found = c.findVaultPubKeyByAddress(strings.ToUpper(addr.String()))
	assert.False(t, found, "case-variant xmr address must not resolve through the vault reverse map")
}

// TestTxVaultPubKeyNeverReturnsEddsa verifies that txVaultPubKey returns the
// frost key when VaultPubKeyFrost is set, preventing eddsa-keyed sidecar calls.
func TestTxVaultPubKeyNeverReturnsEddsa(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-for-txvault")
	eddsaKey := testVaultPubKey(t, "eddsa-for-txvault")
	ecdsaKey := testVaultPubKey(t, "ecdsa-for-txvault")

	// When all keys are set, frost must be returned.
	tx := stypes.TxOutItem{
		VaultPubKey:      ecdsaKey,
		VaultPubKeyEddsa: eddsaKey,
		VaultPubKeyFrost: frostKey,
	}
	result := txVaultPubKey(tx)
	assert.Equal(t, frostKey, result, "must return frost key, not eddsa")
	assert.NotEqual(t, eddsaKey, result, "must NEVER return eddsa when frost is set")
}
