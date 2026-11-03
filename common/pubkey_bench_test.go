package common

import (
	"testing"

	"github.com/cometbft/cometbft/crypto/secp256k1"
)

// BenchmarkPubKeyGetAddress measures the cached path of GetAddress, which the
// pubkey manager hits for every vault on every pool-address lookup.
func BenchmarkPubKeyGetAddress(b *testing.B) {
	pk, err := NewPubKeyFromCrypto(secp256k1.GenPrivKeySecp256k1([]byte("bifrost-bench-getaddress")).PubKey())
	if err != nil {
		b.Fatal(err)
	}
	for _, chain := range []Chain{ETHChain, BTCChain} {
		if _, err = pk.GetAddress(chain); err != nil { // warm the cache
			b.Fatal(err)
		}
		b.Run(chain.String(), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := pk.GetAddress(chain); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
