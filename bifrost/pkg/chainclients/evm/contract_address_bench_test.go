package evm

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
)

// contractsPubKeyValidator returns a fixed router list from GetContracts.
type contractsPubKeyValidator struct {
	*pubkeymanager.MockPoolAddressValidator
	contracts []thorcommon.Address
}

func (v contractsPubKeyValidator) GetContracts(thorcommon.Chain, bool) []thorcommon.Address {
	// PubKeyManager.GetContracts builds a fresh slice on every call
	return append([]thorcommon.Address(nil), v.contracts...)
}

// benchContractAddress returns a deterministic address; mixed hex case in the
// thornode string form, as router and aggregator addresses arrive.
func benchContractAddress(i int) common.Address {
	return common.BigToAddress(new(big.Int).SetUint64(0xA0B1C2D3E4F5 + uint64(i)*0x1F2E3D4C5B6A79))
}

// newContractBenchScanner mirrors an AVAX scanner: two routers (current and
// retiring vault) and the chain's 17 mainnet aggregator contracts.
func newContractBenchScanner() (*EVMScanner, []common.Address) {
	var routers, whitelist []thorcommon.Address
	var all []common.Address
	for i := 0; i < 2; i++ {
		a := benchContractAddress(i)
		routers = append(routers, thorcommon.Address(a.Hex()))
		all = append(all, a)
	}
	for i := 2; i < 2+17; i++ {
		a := benchContractAddress(i)
		whitelist = append(whitelist, thorcommon.Address(a.Hex()))
		all = append(all, a)
	}
	cfg := getConfigForTest()
	return &EVMScanner{
		cfg:                cfg,
		pubkeyMgr:          contractsPubKeyValidator{MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(), contracts: routers},
		whitelistContracts: whitelist,
	}, all
}

func BenchmarkIsToValidContractAddress(b *testing.B) {
	scanner, known := newContractBenchScanner()
	unrelated := common.HexToAddress("0x2222222222222222222222222222222222222222")
	cases := []struct {
		name      string
		addr      common.Address
		whitelist bool
		want      bool
	}{
		// SmartContractLogParser checks every log's emitter against the routers
		{"log/miss", unrelated, false, false},
		{"log/hit-router", known[1], false, true},
		// receiptToTxInItem checks tx.To() against routers and aggregators
		{"tx/miss", unrelated, true, false},
		{"tx/hit-last-aggregator", known[len(known)-1], true, true},
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
