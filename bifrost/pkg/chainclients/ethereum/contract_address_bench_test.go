package ethereum

import (
	"math/big"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
)

// contractsPubKeyValidator returns a fixed router list from GetContracts.
type contractsPubKeyValidator struct {
	*pubkeymanager.MockPoolAddressValidator
	contracts []common.Address
}

func (v contractsPubKeyValidator) GetContracts(common.Chain, bool) []common.Address {
	// PubKeyManager.GetContracts builds a fresh slice on every call
	return append([]common.Address(nil), v.contracts...)
}

func benchRouterAddresses() []ecommon.Address {
	return []ecommon.Address{
		ecommon.BigToAddress(new(big.Int).SetUint64(0xA0B1C2D3E4F5)),
		ecommon.BigToAddress(new(big.Int).SetUint64(0xF5E4D3C2B1A0)),
	}
}

func BenchmarkETHIsToValidContractAddress(b *testing.B) {
	routers := benchRouterAddresses()
	scanner := &ETHScanner{pubkeyMgr: contractsPubKeyValidator{
		MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
		contracts:                []common.Address{common.Address(routers[0].Hex()), common.Address(routers[1].Hex())},
	}}
	lastWhitelisted := ecommon.HexToAddress(whitelistSmartContractAddress[len(whitelistSmartContractAddress)-1].String())
	unrelated := ecommon.HexToAddress("0x2222222222222222222222222222222222222222")
	cases := []struct {
		name      string
		addr      ecommon.Address
		whitelist bool
		want      bool
	}{
		{"log/miss", unrelated, false, false},
		{"log/hit-router", routers[1], false, true},
		{"tx/miss", unrelated, true, false},
		{"tx/hit-last-whitelisted", lastWhitelisted, true, true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			addr := tc.addr
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if got := scanner.isToValidContractAddress(&addr, tc.whitelist); got != tc.want {
					b.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}
