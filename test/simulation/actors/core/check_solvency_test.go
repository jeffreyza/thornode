package core

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestVaultIsSolvent(t *testing.T) {
	tests := []struct {
		name      string
		asset     common.Asset
		vault     cosmos.Uint
		l1        cosmos.Uint
		solvent   bool
		shortfall cosmos.Uint
		tolerance cosmos.Uint
	}{
		{
			name:      "exact match for non XMR asset",
			asset:     common.BTCAsset,
			vault:     cosmos.NewUint(100),
			l1:        cosmos.NewUint(100),
			solvent:   true,
			shortfall: cosmos.ZeroUint(),
			tolerance: cosmos.ZeroUint(),
		},
		{
			name:      "non XMR asset allows over solvency",
			asset:     common.BTCAsset,
			vault:     cosmos.NewUint(100),
			l1:        cosmos.NewUint(101),
			solvent:   true,
			shortfall: cosmos.ZeroUint(),
			tolerance: cosmos.ZeroUint(),
		},
		{
			name:      "non XMR asset still fails on shortfall",
			asset:     common.BTCAsset,
			vault:     cosmos.NewUint(100),
			l1:        cosmos.NewUint(99),
			solvent:   false,
			shortfall: cosmos.NewUint(1),
			tolerance: cosmos.ZeroUint(),
		},
		{
			name:      "xmr allows small dust shortfall",
			asset:     common.XMRAsset,
			vault:     cosmos.NewUint(100),
			l1:        cosmos.NewUint(97),
			solvent:   true,
			shortfall: cosmos.NewUint(3),
			tolerance: xmrSolvencyTolerance,
		},
		{
			name:      "xmr still fails beyond tolerance",
			asset:     common.XMRAsset,
			vault:     cosmos.NewUint(100),
			l1:        cosmos.NewUint(70),
			solvent:   false,
			shortfall: cosmos.NewUint(30),
			tolerance: xmrSolvencyTolerance,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			solvent, shortfall, tolerance := vaultIsSolvent(tc.asset, tc.vault, tc.l1)
			if solvent != tc.solvent {
				t.Fatalf("expected solvent=%t, got %t", tc.solvent, solvent)
			}
			if !shortfall.Equal(tc.shortfall) {
				t.Fatalf("expected shortfall %s, got %s", tc.shortfall, shortfall)
			}
			if !tolerance.Equal(tc.tolerance) {
				t.Fatalf("expected tolerance %s, got %s", tc.tolerance, tolerance)
			}
		})
	}
}

func TestSolvencyExcess(t *testing.T) {
	t.Parallel()

	excess := solvencyExcess(cosmos.NewUint(100), cosmos.NewUint(125))
	if !excess.Equal(cosmos.NewUint(25)) {
		t.Fatalf("expected excess of 25, got %s", excess)
	}

	none := solvencyExcess(cosmos.NewUint(125), cosmos.NewUint(100))
	if !none.IsZero() {
		t.Fatalf("expected zero excess, got %s", none)
	}
}

func TestSolvencyMismatchRetryable(t *testing.T) {
	tests := []struct {
		name       string
		mismatches []solvencyMismatch
		retryable  bool
	}{
		{
			name:       "no mismatches is not retryable",
			mismatches: nil,
			retryable:  false,
		},
		{
			name: "xmr only mismatch is retryable",
			mismatches: []solvencyMismatch{
				{asset: common.XMRAsset},
			},
			retryable: true,
		},
		{
			name: "sol only mismatch is retryable",
			mismatches: []solvencyMismatch{
				{asset: common.SOLAsset},
			},
			retryable: true,
		},
		{
			name: "xmr and sol mismatch is retryable",
			mismatches: []solvencyMismatch{
				{asset: common.XMRAsset},
				{asset: common.SOLAsset},
			},
			retryable: true,
		},
		{
			name: "non xmr mismatch is not retryable",
			mismatches: []solvencyMismatch{
				{asset: common.BTCAsset},
			},
			retryable: false,
		},
		{
			name: "mixed mismatch is not retryable",
			mismatches: []solvencyMismatch{
				{asset: common.XMRAsset},
				{asset: common.BTCAsset},
			},
			retryable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := solvencyMismatchRetryable(tc.mismatches); got != tc.retryable {
				t.Fatalf("expected retryable=%t, got %t", tc.retryable, got)
			}
		})
	}
}

func TestEd25519SolvencyPubKeyUsesVaultKeyForXMR(t *testing.T) {
	eddsaKey := common.PubKey("tthorpub1zcjduepqamk0ywsr8x95evuv4xm85p225sd7w763y69l9lyclznp6eyqqv9qgvhalh")
	frostKey := common.PubKey("tthorpub1zcjduepq068dr0x7ue973drmq4eqmzhcq3650n7nx5fhgn9gl207luxp6vskkjr3d")

	got := ed25519SolvencyPubKey(common.XMRAsset, eddsaKey, frostKey)
	if !got.Equals(frostKey) {
		t.Fatalf("expected XMR solvency to query vault frost pubkey %s, got %s", frostKey, got)
	}

	got = ed25519SolvencyPubKey(common.SOLAsset, eddsaKey, frostKey)
	if !got.Equals(eddsaKey) {
		t.Fatalf("expected SOL solvency to query vault eddsa pubkey %s, got %s", eddsaKey, got)
	}
}

func TestSolvencySkipsXMRVaultWithoutPubKey(t *testing.T) {
	eddsaKey := common.PubKey("tthorpub1zcjduepqamk0ywsr8x95evuv4xm85p225sd7w763y69l9lyclznp6eyqqv9qgvhalh")
	frostKey := common.PubKey("tthorpub1zcjduepq068dr0x7ue973drmq4eqmzhcq3650n7nx5fhgn9gl207luxp6vskkjr3d")

	if !skipSolvencyVaultCoin(common.XMRAsset, "", "") {
		t.Fatal("expected XMR without a frost pubkey to be skipped")
	}
	if skipSolvencyVaultCoin(common.XMRAsset, "", frostKey) {
		t.Fatal("expected XMR with a frost pubkey to be checked")
	}
	if skipSolvencyVaultCoin(common.SOLAsset, eddsaKey, "") {
		t.Fatal("expected SOL with an eddsa pubkey to be checked")
	}
}
