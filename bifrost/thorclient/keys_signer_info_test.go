package thorclient

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/cmd"
)

// countingKeyring counts Key reads and can be made to fail them.
type countingKeyring struct {
	ckeys.Keyring
	keyCalls atomic.Int64
	fail     atomic.Bool
}

var errKeyringUnavailable = errors.New("keyring unavailable")

func (k *countingKeyring) Key(uid string) (*ckeys.Record, error) {
	k.keyCalls.Add(1)
	if k.fail.Load() {
		return nil, errKeyringUnavailable
	}
	return k.Keyring.Key(uid)
}

func newCountingKeyring(t *testing.T, name, passphrase string) *countingKeyring {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := ckeys.NewInMemory(codec.NewProtoCodec(registry))
	if _, _, err := kb.NewMnemonic(name, ckeys.English, cmd.THORChainHDPath, passphrase, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	return &countingKeyring{Keyring: kb}
}

func TestGetSignerInfoReadsKeyringOnce(t *testing.T) {
	const name, passphrase = "signer", "password"
	kb := newCountingKeyring(t, name, passphrase)
	keys := NewKeysWithKeybase(kb, name, passphrase)

	direct, err := kb.Keyring.Key(name)
	if err != nil {
		t.Fatal(err)
	}
	first := keys.GetSignerInfo()
	second := keys.GetSignerInfo()
	if first != second || kb.keyCalls.Load() != 1 {
		t.Fatalf("keyring read %d times", kb.keyCalls.Load())
	}
	gotAddr, err := first.GetAddress()
	if err != nil {
		t.Fatal(err)
	}
	wantAddr, err := direct.GetAddress()
	if err != nil {
		t.Fatal(err)
	}
	if !gotAddr.Equals(wantAddr) {
		t.Fatalf("address %s, want %s", gotAddr, wantAddr)
	}

	// Behavior change: once read, a failing keyring no longer panics.
	kb.fail.Store(true)
	if keys.GetSignerInfo() != first {
		t.Fatal("expected the cached record")
	}

	// A first read that fails still panics, as before.
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic when the first read fails")
		}
	}()
	NewKeysWithKeybase(kb, name, passphrase).GetSignerInfo()
}

func TestGetSignerInfoConcurrent(t *testing.T) {
	const name, passphrase = "signer", "password"
	keys := NewKeysWithKeybase(newCountingKeyring(t, name, passphrase), name, passphrase)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := keys.GetSignerInfo().GetAddress(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
