package pubkeymanager

import (
	"fmt"
	"strings"
	"testing"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	"gitlab.com/thorchain/thornode/v3/common"
)

// newBenchPubKeyManager returns a PubKeyManager holding n deterministic
// secp256k1 pubkeys and the ETH pool address of the most recently added one.
func newBenchPubKeyManager(b *testing.B, n int) (*PubKeyManager, string) {
	b.Helper()
	pkm, err := NewPubKeyManager(nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	var last string
	for i := 0; i < n; i++ {
		priv := secp256k1.GenPrivKeyFromSecret([]byte(fmt.Sprintf("bifrost-bench-vault-%d", i)))
		tm, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
		if err != nil {
			b.Fatal(err)
		}
		pk, err := common.NewPubKeyFromCrypto(tm)
		if err != nil {
			b.Fatal(err)
		}
		pkm.AddPubKey(pk, false, common.SigningAlgoSecp256k1)
		addr, err := pk.GetAddress(common.ETHChain)
		if err != nil {
			b.Fatal(err)
		}
		last = addr.String()
	}
	return pkm, last
}

func BenchmarkIsValidPoolAddress(b *testing.B) {
	pkm, vault := newBenchPubKeyManager(b, 50)
	cases := []struct {
		name string
		addr string
		want bool
	}{
		// the common case: an address that is not a vault
		{"miss", "0x1111111254EEB25477B68fb85Ed929f73A960582", false},
		{"hit-lower", vault, true},
		// tx.To().String() is EIP-55 mixed case
		{"hit-mixedcase", "0x" + strings.ToUpper(vault[2:]), true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ok, _ := pkm.IsValidPoolAddress(tc.addr, common.ETHChain)
				if ok != tc.want {
					b.Fatalf("got %v want %v", ok, tc.want)
				}
			}
		})
	}
}
