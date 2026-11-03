package evm

import (
	"math/rand"
	"strings"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"
)

func TestLowerHexAddress(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // fixed seed: a reproducible differential test
	addrs := []ecommon.Address{{}, ecommon.MaxAddress, ecommon.HexToAddress("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed")}
	for i := 0; i < 10000; i++ {
		var a ecommon.Address
		rng.Read(a[:])
		addrs = append(addrs, a)
	}
	for _, a := range addrs {
		if got, want := LowerHexAddress(a), strings.ToLower(a.Hex()); got != want {
			t.Fatalf("LowerHexAddress(%x) = %s, want %s", a, got, want)
		}
		if !strings.EqualFold(LowerHexAddress(a), a.String()) {
			t.Fatalf("LowerHexAddress(%x) not EqualFold to String()", a)
		}
	}
}

func FuzzLowerHexAddress(f *testing.F) {
	f.Add(make([]byte, ecommon.AddressLength))
	f.Fuzz(func(t *testing.T, b []byte) {
		a := ecommon.BytesToAddress(b)
		if got, want := LowerHexAddress(a), strings.ToLower(a.Hex()); got != want {
			t.Fatalf("LowerHexAddress(%x) = %s, want %s", a, got, want)
		}
	})
}
