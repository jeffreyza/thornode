package ethereum

import (
	"math/rand"
	"strings"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
)

// referenceETHIsToValidContractAddress is the original implementation, which
// compared against the EIP-55 checksummed addr.String() for every item.
func referenceETHIsToValidContractAddress(e *ETHScanner, addr *ecommon.Address, includeWhiteList bool) bool {
	contractAddresses := e.pubkeyMgr.GetContracts(common.ETHChain, false)
	if includeWhiteList || useWhitelistSmartContract {
		contractAddresses = append(contractAddresses, whitelistSmartContractAddress...)
	}
	for _, item := range contractAddresses {
		if strings.EqualFold(item.String(), addr.String()) {
			return true
		}
	}
	return false
}

func ethAddressForms(a ecommon.Address) []common.Address {
	h := a.Hex()
	forms := []string{
		h, strings.ToLower(h), strings.ToUpper(h), "0x" + strings.ToUpper(h[2:]),
		h[2:], h + " ", h[:len(h)-1], strings.ReplaceAll(h, "a", "а"),
	}
	out := make([]common.Address, len(forms))
	for i, f := range forms {
		out[i] = common.Address(f)
	}
	return out
}

func randomETHContractList(rng *rand.Rand, target ecommon.Address) []common.Address {
	var list []common.Address
	for n := rng.Intn(5); n > 0; n-- {
		var other ecommon.Address
		rng.Read(other[:])
		forms := ethAddressForms(other)
		list = append(list, forms[rng.Intn(len(forms))])
	}
	if rng.Intn(3) == 0 {
		forms := ethAddressForms(target)
		list = append(list, forms[rng.Intn(len(forms))])
	}
	rng.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
	return list
}

// panics reports whether f panics.
func panics(f func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	f()
	return false
}

func TestETHIsToValidContractAddressMatchesReference(t *testing.T) {
	savedWhitelist, savedUse := whitelistSmartContractAddress, useWhitelistSmartContract
	defer func() { whitelistSmartContractAddress, useWhitelistSmartContract = savedWhitelist, savedUse }()

	rng := rand.New(rand.NewSource(11)) //nolint:gosec // fixed seed: a reproducible differential test
	for i := 0; i < 20000; i++ {
		var target ecommon.Address
		rng.Read(target[:])
		scanner := &ETHScanner{pubkeyMgr: contractsPubKeyValidator{
			MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
			contracts:                randomETHContractList(rng, target),
		}}
		whitelistSmartContractAddress = randomETHContractList(rng, target)
		useWhitelistSmartContract = rng.Intn(4) == 0
		for _, includeWhiteList := range []bool{false, true} {
			want := referenceETHIsToValidContractAddress(scanner, &target, includeWhiteList)
			if got := scanner.isToValidContractAddress(&target, includeWhiteList); got != want {
				t.Fatalf("addr %s whitelist=%v use=%v: got %v, want %v", target.Hex(), includeWhiteList, useWhitelistSmartContract, got, want)
			}
			// a nil addr panics exactly when there is something to compare
			wantPanic := panics(func() { referenceETHIsToValidContractAddress(scanner, nil, includeWhiteList) })
			gotPanic := panics(func() { scanner.isToValidContractAddress(nil, includeWhiteList) })
			if gotPanic != wantPanic {
				t.Fatalf("nil addr whitelist=%v: panic %v, want %v", includeWhiteList, gotPanic, wantPanic)
			}
		}
	}
}
