package monero

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestPackage runs check.v1 suites for this package.
func TestPackage(t *testing.T) { TestingT(t) }

type VaultAddressSuite struct{}

var _ = Suite(&VaultAddressSuite{})

type mockMoneroBridge struct {
	thorclient.ThorchainBridge
	vaultByPubKey       map[string]ttypes.Vault
	observedByTx        map[string]*ttypes.QueryObservedTxVoter
	observedFreshFunc   func(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error)
	spentRefsByVaultKey map[string][]*common.OutputRef
	spentRefsFunc       func(common.PubKey) ([]*common.OutputRef, error)
	spentRefsFreshFunc  func(context.Context, common.PubKey) ([]*common.OutputRef, error)
	pubKeys             []thorclient.PubKeyContractAddressPair
	pubKeysErr          error
	mimirValues         map[string]int64
	keysignPolicy       thorclient.KeysignPolicy
	keysignPolicyErr    error
}

func (m *mockMoneroBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	if v, ok := m.vaultByPubKey[pubkey]; ok {
		return v, nil
	}
	return ttypes.Vault{}, errors.New("vault not found")
}

func (m *mockMoneroBridge) GetAsgards() (ttypes.Vaults, error) {
	vaults := make(ttypes.Vaults, 0, len(m.vaultByPubKey))
	for _, v := range m.vaultByPubKey {
		vaults = append(vaults, v)
	}
	return vaults, nil
}

func (m *mockMoneroBridge) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	if m.pubKeysErr != nil {
		return nil, m.pubKeysErr
	}
	if m.pubKeys != nil {
		return m.pubKeys, nil
	}
	pubKeys := make([]thorclient.PubKeyContractAddressPair, 0, len(m.vaultByPubKey))
	for _, v := range m.vaultByPubKey {
		if v.PubKey.IsEmpty() {
			continue
		}
		pubKeys = append(pubKeys, thorclient.PubKeyContractAddressPair{
			PubKey: v.PubKey,
			Algo:   common.SigningAlgoSecp256k1,
		})
	}
	return pubKeys, nil
}

func (m *mockMoneroBridge) GetObservedTxVoter(txID string) (*ttypes.QueryObservedTxVoter, error) {
	if m.observedByTx != nil {
		if voter, ok := m.observedByTx[txID]; ok {
			return voter, nil
		}
	}
	return nil, errors.New("observed tx voter not found")
}

func (m *mockMoneroBridge) GetObservedTxOutVoter(txID string) (*ttypes.QueryObservedTxVoter, error) {
	return m.GetObservedTxVoter(txID)
}

func (m *mockMoneroBridge) GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*ttypes.QueryObservedTxVoter, bool, error) {
	if m.observedFreshFunc != nil {
		return m.observedFreshFunc(ctx, txID)
	}
	voter, err := m.GetObservedTxVoter(txID)
	return voter, voter != nil, err
}

func (m *mockMoneroBridge) GetXMRSpentOutputRefs(vaultPubKey common.PubKey) ([]*common.OutputRef, error) {
	if m.spentRefsFunc != nil {
		return m.spentRefsFunc(vaultPubKey)
	}
	if m.spentRefsByVaultKey != nil {
		return m.spentRefsByVaultKey[vaultPubKey.String()], nil
	}
	return nil, nil
}

func (m *mockMoneroBridge) GetXMRSpentOutputRefsUncached(ctx context.Context, vaultPubKey common.PubKey) ([]*common.OutputRef, error) {
	if m.spentRefsFreshFunc != nil {
		return m.spentRefsFreshFunc(ctx, vaultPubKey)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.GetXMRSpentOutputRefs(vaultPubKey)
}

func (m *mockMoneroBridge) GetMimir(key string) (int64, error) {
	if value, ok := m.mimirValues[key]; ok {
		return value, nil
	}
	return -1, nil
}

// GetKeysignPolicy returns the injected policy so vault membership tests can
// assert full-vs-active signer handling without a live thorchain bridge.
func (m *mockMoneroBridge) GetKeysignPolicy(common.PubKey, int64) (thorclient.KeysignPolicy, error) {
	return m.keysignPolicy, m.keysignPolicyErr
}

func TestDeriveVaultAddressReturnsEmptyForUnknownVault(t *testing.T) {
	t.Parallel()

	c := &Client{
		chain:           common.XMRChain,
		thorchainBridge: &mockMoneroBridge{vaultByPubKey: map[string]ttypes.Vault{}},
		vaultAddressMap: make(map[string]common.Address),
	}

	got := c.deriveVaultAddress(common.PubKey("thorpub1unknownvault"))
	if got != "" {
		t.Fatalf("expected empty vault address when mapping is unknown, got %s", got)
	}
}

func TestLoadVaultAddressMapRestoresPersistedMapping(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	store := newTestVaultAddressStore(t)
	if err := store.Set(vaultPubKey.String(), vaultAddress.String()); err != nil {
		t.Fatalf("persist mapping: %v", err)
	}

	c := &Client{
		chain:             common.XMRChain,
		vaultAddressStore: store,
		vaultAddressMap:   make(map[string]common.Address),
	}

	if err := c.loadVaultAddressMap(); err != nil {
		t.Fatalf("load vault address map: %v", err)
	}
	mapped, ok := c.cachedVaultAddress(vaultPubKey)
	if !ok {
		t.Fatalf("expected restored mapping for %s", vaultPubKey)
	}
	if !mapped.Equals(vaultAddress) {
		t.Fatalf("expected restored address %s, got %s", vaultAddress, mapped)
	}
}

func TestLoadVaultAddressMapIgnoresMismatchedPersistedMapping(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	_, wrongAddress := testVaultPubKeyAndAddress(t)
	store := newTestVaultAddressStore(t)
	if err := store.Set(vaultPubKey.String(), wrongAddress.String()); err != nil {
		t.Fatalf("persist mismatched mapping: %v", err)
	}

	c := &Client{
		chain:             common.XMRChain,
		vaultAddressStore: store,
		vaultAddressMap:   make(map[string]common.Address),
	}

	if err := c.loadVaultAddressMap(); err != nil {
		t.Fatalf("load vault address map: %v", err)
	}
	if _, ok := c.cachedVaultAddress(vaultPubKey); ok {
		t.Fatalf("expected mismatched persisted mapping for %s to be ignored", vaultPubKey)
	}
}

func TestTxCacheVaultUsesFrostVaultPubKey(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKeyFrost: common.PubKey("thorpub1frost"),
	}

	key := txCacheVault(tx, common.XMRChain)
	if !strings.Contains(key, "thorpub1frost") {
		t.Fatalf("expected cache key to include frost vault pubkey, got %s", key)
	}
}

func TestTxVaultPubKeyUsesFrost(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      common.PubKey("thorpub1secp"),
		VaultPubKeyEddsa: common.PubKey("thorpub1eddsa"),
		VaultPubKeyFrost: common.PubKey("thorpub1frost"),
	}

	got := txVaultPubKey(tx)
	if !got.Equals(tx.VaultPubKeyFrost) {
		t.Fatalf("expected frost vault pubkey, got %s", got.String())
	}
}

func TestTxVaultPubKeyRequiresFrost(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:       common.XMRChain,
		VaultPubKey: common.PubKey("thorpub1secp"),
	}

	got := txVaultPubKey(tx)
	if !got.IsEmpty() {
		t.Fatalf("expected empty vault pubkey when frost key is missing, got %s", got.String())
	}
}

// TestVaultSigningMembersReturnsFullAndActiveMembership verifies Monero keeps
// the full vault membership separate from thornode's active keysign signers.
func (s *VaultAddressSuite) TestVaultSigningMembersReturnsFullAndActiveMembership(c *C) {
	fullMembers := []string{"pk-a", "pk-b", "pk-c", "pk-d"}
	client := &Client{
		chain: common.XMRChain,
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				"vault": {Membership: fullMembers},
			},
			keysignPolicy: thorclient.KeysignPolicy{
				Signers:               common.PubKeys{common.PubKey("pk-a"), common.PubKey("pk-c"), common.PubKey("pk-b")},
				NeverSigners:          common.PubKeys{common.PubKey("pk-d")},
				JailedFallbackSigners: common.PubKeys{common.PubKey("pk-b")},
				RequiredSigners:       3,
			},
		},
	}

	members, activeMembers, err := client.vaultSigningMembers(common.PubKey("vault"), 123)
	c.Assert(err, IsNil)
	c.Assert(members, DeepEquals, fullMembers)

	wantActive := []string{"pk-a", "pk-c", "pk-b"}
	c.Assert(activeMembers, DeepEquals, wantActive)

	wantFull := []string{"pk-a", "pk-b", "pk-c", "pk-d"}
	c.Assert(fullMembers, DeepEquals, wantFull)
}
