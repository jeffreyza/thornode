package thorclient

import (
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/cmd"
)

// BenchmarkGetSignerInfo reads the signer record from a file-backed keyring,
// as bifrost does (FetchNodeStatus calls it about once a second via sendDeck).
func BenchmarkGetSignerInfo(b *testing.B) {
	const name, passphrase = "bench-signer", "bench-password"
	kb, err := getKeybase(b.TempDir(), strings.NewReader(strings.Repeat(passphrase+"\n", 16)))
	if err != nil {
		b.Fatal(err)
	}
	if _, _, err = kb.NewMnemonic(name, ckeys.English, cmd.THORChainHDPath, passphrase, hd.Secp256k1); err != nil {
		b.Fatal(err)
	}
	keys := NewKeysWithKeybase(kb, name, passphrase)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := keys.GetSignerInfo().GetAddress(); err != nil {
			b.Fatal(err)
		}
	}
}
