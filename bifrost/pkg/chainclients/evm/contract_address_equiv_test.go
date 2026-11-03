package evm

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
)

// referenceIsToValidContractAddress is the original implementation, which
// compared against the EIP-55 checksummed addr.String().
func referenceIsToValidContractAddress(e *EVMScanner, addr *common.Address, includeWhiteList bool) bool {
	if addr == nil {
		return false
	}
	contractAddresses := e.pubkeyMgr.GetContracts(e.cfg.ChainID, false)
	if includeWhiteList {
		contractAddresses = append(contractAddresses, e.whitelistContracts...)
	}
	addrString := addr.String()
	for _, item := range contractAddresses {
		if strings.EqualFold(item.String(), addrString) {
			return true
		}
	}
	return false
}

// contractAddressForms renders a as the many ways a contract address string
// may be stored, including near misses and non-ASCII look-alikes.
func contractAddressForms(a common.Address) []thorcommon.Address {
	h := a.Hex()
	forms := []string{
		h,
		strings.ToLower(h),
		strings.ToUpper(h),
		"0x" + strings.ToUpper(h[2:]),
		h[2:],
		h + " ",
		h[:len(h)-1],
		strings.ReplaceAll(h, "a", "а"), // Cyrillic a
		strings.ReplaceAll(strings.ToLower(h), "0x", "0х"),
	}
	out := make([]thorcommon.Address, len(forms))
	for i, f := range forms {
		out[i] = thorcommon.Address(f)
	}
	return out
}

func randomContractList(rng *rand.Rand, target common.Address) []thorcommon.Address {
	var list []thorcommon.Address
	for n := rng.Intn(5); n > 0; n-- {
		var other common.Address
		rng.Read(other[:])
		forms := contractAddressForms(other)
		list = append(list, forms[rng.Intn(len(forms))])
	}
	if rng.Intn(3) == 0 {
		forms := contractAddressForms(target)
		list = append(list, forms[rng.Intn(len(forms))])
	}
	rng.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
	return list
}

func TestIsToValidContractAddressMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // fixed seed: a reproducible differential test
	for i := 0; i < 20000; i++ {
		var target common.Address
		rng.Read(target[:])
		scanner := &EVMScanner{
			cfg: getConfigForTest(),
			pubkeyMgr: contractsPubKeyValidator{
				MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
				contracts:                randomContractList(rng, target),
			},
			whitelistContracts: randomContractList(rng, target),
		}
		for _, includeWhiteList := range []bool{false, true} {
			want := referenceIsToValidContractAddress(scanner, &target, includeWhiteList)
			if got := scanner.isToValidContractAddress(&target, includeWhiteList); got != want {
				t.Fatalf("addr %s whitelist=%v routers=%v aggregators=%v: got %v, want %v",
					target.Hex(), includeWhiteList, scanner.pubkeyMgr.GetContracts("", false), scanner.whitelistContracts, got, want)
			}
			if got := scanner.isToValidContractAddress(nil, includeWhiteList); got {
				t.Fatal("nil addr must not match")
			}
		}
	}
}
