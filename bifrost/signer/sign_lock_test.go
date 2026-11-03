package signer

import (
	"math/big"
	"sync"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	thtypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type mockChainNoVaultLock struct{}

func (m *mockChainNoVaultLock) Start(chan thtypes.TxIn, chan thtypes.ErrataBlock, chan thtypes.Solvency, chan common.NetworkFee) {
}
func (m *mockChainNoVaultLock) Stop()                       {}
func (m *mockChainNoVaultLock) IsBlockScannerHealthy() bool { return true }
func (m *mockChainNoVaultLock) SignTx(thtypes.TxOutItem, int64) ([]byte, []byte, *thtypes.TxInItem, error) {
	return nil, nil, nil, nil
}

func (m *mockChainNoVaultLock) BroadcastTx(thtypes.TxOutItem, []byte) (string, error) { return "", nil }

func (m *mockChainNoVaultLock) GetHeight() (int64, error)       { return 0, nil }
func (m *mockChainNoVaultLock) GetAddress(common.PubKey) string { return "" }
func (m *mockChainNoVaultLock) GetAccount(common.PubKey, *big.Int) (common.Account, error) {
	return common.Account{}, nil
}

func (m *mockChainNoVaultLock) GetAccountByAddress(string, *big.Int) (common.Account, error) {
	return common.Account{}, nil
}
func (m *mockChainNoVaultLock) GetChain() common.Chain { return common.XMRChain }
func (m *mockChainNoVaultLock) GetConfig() config.BifrostChainConfiguration {
	return config.BifrostChainConfiguration{}
}
func (m *mockChainNoVaultLock) OnObservedTxIn(thtypes.TxInItem, int64)   {}
func (m *mockChainNoVaultLock) GetConfirmationCount(thtypes.TxIn) int64  { return 0 }
func (m *mockChainNoVaultLock) ConfirmationCountReady(thtypes.TxIn) bool { return true }
func (m *mockChainNoVaultLock) GetBlockScannerHeight() (int64, error)    { return 0, nil }
func (m *mockChainNoVaultLock) GetLatestTxForVault(string) (string, string, error) {
	return "", "", nil
}
func (m *mockChainNoVaultLock) RollbackBlockScanner() error { return nil }

type mockChainWithVaultLock struct {
	mockChainNoVaultLock
	lock *sync.Mutex
}

func (m *mockChainWithVaultLock) GetVaultLock(string) *sync.Mutex {
	return m.lock
}

type mockChainWithVaultLockKey struct {
	mockChainNoVaultLock
	key string
}

func (m *mockChainWithVaultLockKey) GetVaultLockKey(_ thtypes.TxOutItem) string {
	return m.key
}

func TestChainVaultLockUsesOptionalLocker(t *testing.T) {
	t.Parallel()

	var noLock types.ChainClient = &mockChainNoVaultLock{}
	if got := chainVaultLock(noLock, "vault"); got != nil {
		t.Fatalf("expected nil lock for non-lockable chain, got %v", got)
	}

	expected := &sync.Mutex{}
	var withLock types.ChainClient = &mockChainWithVaultLock{lock: expected}
	got := chainVaultLock(withLock, "vault")
	if got != expected {
		t.Fatalf("expected vault lock pointer %p, got %p", expected, got)
	}
}

func TestChainVaultLockKeyUsesResolverWhenAvailable(t *testing.T) {
	t.Parallel()

	tx := thtypes.TxOutItem{
		VaultPubKey: "thorpub1secp",
	}

	var noResolver types.ChainClient = &mockChainNoVaultLock{}
	if got := chainVaultLockKey(noResolver, tx); got != tx.VaultPubKey.String() {
		t.Fatalf("expected default vault key %q, got %q", tx.VaultPubKey.String(), got)
	}

	var withResolver types.ChainClient = &mockChainWithVaultLockKey{key: "thorpub1eddsa"}
	if got := chainVaultLockKey(withResolver, tx); got != "thorpub1eddsa" {
		t.Fatalf("expected resolver key %q, got %q", "thorpub1eddsa", got)
	}
}
