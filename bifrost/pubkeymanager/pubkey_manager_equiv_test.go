package pubkeymanager

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// referenceIsValidPoolAddress is the original uncached IsValidPoolAddress loop.
func referenceIsValidPoolAddress(pkm *PubKeyManager, addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	pkm.rwMutex.RLock()
	defer pkm.rwMutex.RUnlock()
	for _, pk := range pkm.pubkeys {
		if chain.GetSigningAlgo() != pk.Algo {
			continue
		}
		ok, cpi := matchAddress(addr, chain, pk.PubKey)
		if ok {
			return ok, cpi
		}
	}
	return false, common.EmptyChainPoolInfo
}

func alternateCase(s string) string {
	b := []byte(s)
	for i := range b {
		if i%2 == 0 {
			b[i] = strings.ToUpper(string(b[i]))[0]
		}
	}
	return string(b)
}

func poolAddressCandidates(pkm *PubKeyManager, chains []common.Chain) []string {
	addrs := []string{
		"",
		" ",
		"0x",
		"0x1111111254EEB25477B68fb85Ed929f73A960582",
		"not-an-address",
		"K", // Kelvin sign, EqualFold-equal to "k"
		"ſ", // long s, EqualFold-equal to "s"
	}
	for _, pk := range pkm.GetPubKeys() {
		for _, chain := range chains {
			a, err := pk.GetAddress(chain)
			if err != nil {
				continue
			}
			s := a.String()
			addrs = append(addrs,
				s,
				strings.ToLower(s),
				strings.ToUpper(s),
				alternateCase(s),
				s+" ",
				strings.ReplaceAll(s, "k", "K"),
				strings.ReplaceAll(s, "s", "ſ"),
				strings.ReplaceAll(strings.ToUpper(s), "K", "K"),
			)
			if len(s) > 1 {
				addrs = append(addrs, s[:len(s)-1])
			}
		}
	}
	return addrs
}

func assertPoolAddressEquivalent(t *testing.T, pkm *PubKeyManager, stage string) {
	t.Helper()
	chains := []common.Chain{
		common.ETHChain, common.AVAXChain, common.BSCChain, common.BTCChain,
		common.LTCChain, common.DOGEChain, common.THORChain, common.SOLChain,
		common.XRPChain, common.EmptyChain, common.Chain("FOO"),
	}
	addrs := poolAddressCandidates(pkm, chains)
	// run twice so the second pass is served from a warm cache
	for pass := 0; pass < 2; pass++ {
		for _, chain := range chains {
			for _, addr := range addrs {
				wantOK, wantCPI := referenceIsValidPoolAddress(pkm, addr, chain)
				gotOK, gotCPI := pkm.IsValidPoolAddress(addr, chain)
				if gotOK != wantOK || gotCPI != wantCPI {
					t.Fatalf("%s pass %d: IsValidPoolAddress(%q, %s) = (%v, %+v), want (%v, %+v)",
						stage, pass, addr, chain, gotOK, gotCPI, wantOK, wantCPI)
				}
			}
		}
	}
}

func TestIsValidPoolAddressMatchesReference(t *testing.T) {
	pkm, err := NewPubKeyManager(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPoolAddressEquivalent(t, pkm, "empty")

	var secp, eddsa common.PubKeys
	for i := 0; i < 12; i++ {
		pk := types.GetRandomPubKey()
		secp = append(secp, pk)
		pkm.addPubKeyInternal(pk, i%3 == 0, common.SigningAlgoSecp256k1, i%4 == 0)
	}
	for i := 0; i < 4; i++ {
		pk := types.GetRandomEd25519PubKey()
		eddsa = append(eddsa, pk)
		pkm.AddPubKey(pk, false, common.SigningAlgoEd25519)
	}
	// the same key registered under the other algo is skipped by the algo filter
	pkm.AddPubKey(types.GetRandomPubKey(), false, common.SigningAlgoEd25519)
	assertPoolAddressEquivalent(t, pkm, "populated")

	// GetAddress returns NoAddress with a nil error for unsupported chains, so
	// the original loop matches an empty addr there; that must be preserved.
	if ok, cpi := pkm.IsValidPoolAddress("", common.Chain("FOO")); !ok || cpi.PubKey.IsEmpty() {
		t.Fatalf("expected empty addr to match on an unsupported chain, got %v %+v", ok, cpi)
	}

	// mutations must invalidate the cache
	pkm.RemovePubKey(secp[2]) // swap-deletes, reordering pubkeys
	assertPoolAddressEquivalent(t, pkm, "after remove")
	pkm.addPubKeyInternal(secp[0], false, common.SigningAlgoSecp256k1, false) // inactive -> active
	pkm.addPubKeyInternal(secp[1], false, common.SigningAlgoSecp256k1, true)  // active -> inactive
	assertPoolAddressEquivalent(t, pkm, "after inactive toggle")
	pkm.AddNodePubKey(types.GetRandomPubKey(), common.SigningAlgoSecp256k1)
	pkm.AddPubKey(secp[2], false, common.SigningAlgoSecp256k1)
	pkm.RemovePubKey(eddsa[0])
	assertPoolAddressEquivalent(t, pkm, "after re-add")
}

func TestPoolAddressCacheNonASCIIFallsBack(t *testing.T) {
	pkm, err := NewPubKeyManager(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pk := types.GetRandomPubKey()
	pkm.AddPubKey(pk, false, common.SigningAlgoSecp256k1)
	if !pkm.poolAddresses(common.ETHChain, true).allASCII {
		t.Fatal("expected ASCII pool addresses")
	}
	// a set with a non-ASCII address must not be used for EqualFold matching
	pkm.rwMutex.Lock()
	pkm.poolAddressCache[poolAddressCacheKey{Chain: common.ETHChain, IncludeInactive: true}] = poolAddressSet{
		byAddress: map[string]common.ChainPoolInfo{},
		allASCII:  false,
	}
	pkm.rwMutex.Unlock()
	addr, err := pk.GetAddress(common.ETHChain)
	if err != nil {
		t.Fatal(err)
	}
	ok, cpi := pkm.IsValidPoolAddress(strings.ToUpper(addr.String()), common.ETHChain)
	if !ok || !cpi.PubKey.Equals(pk) {
		t.Fatalf("expected fallback to match pubkey, got %v %+v", ok, cpi)
	}
}

func TestIsValidPoolAddressConcurrentWithMutations(t *testing.T) {
	pkm, err := NewPubKeyManager(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stable := types.GetRandomPubKey()
	pkm.AddPubKey(stable, false, common.SigningAlgoSecp256k1)
	addr, err := stable.GetAddress(common.ETHChain)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			pk := types.GetRandomPubKey()
			pkm.addPubKeyInternal(pk, false, common.SigningAlgoSecp256k1, i%2 == 0)
			pkm.RemovePubKey(pk)
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
			if ok, _ := pkm.IsValidPoolAddress(strings.ToUpper(addr.String()), common.ETHChain); !ok {
				t.Fatal("stable vault address not found")
			}
			pkm.IsActivePoolAddress(addr.String(), common.ETHChain)
		}
	}
}
