package tssrounds

import (
	"testing"
)

// TestValidTssKeysignRounds_HasExpectedCanonicalSet pins the canonical
// keysign round set. Adding a new round (e.g. v4's hypothetical KEYSIGN10)
// must be a deliberate edit that updates both the constants and this test.
func TestValidTssKeysignRounds_HasExpectedCanonicalSet(t *testing.T) {
	expected := map[string]bool{
		KEYSIGN1aUnicast:       true,
		KEYSIGN1b:              true,
		KEYSIGN2Unicast:        true,
		KEYSIGN3:               true,
		KEYSIGN4:               true,
		KEYSIGN5:               true,
		KEYSIGN6:               true,
		KEYSIGN7:               true,
		KEYSIGN8:               true,
		KEYSIGN9:               true,
		EDDSAKEYSIGN1:          true,
		EDDSAKEYSIGN2:          true,
		EDDSAKEYSIGN3:          true,
		FROSTKEYSIGNPREPROCESS: true,
		FROSTKEYSIGNSHARE:      true,
		FROSTKEYSIGNRESULT:     true,
	}

	if len(ValidTssKeysignRounds) != len(expected) {
		t.Fatalf("ValidTssKeysignRounds has %d entries, expected %d (added or removed a round without updating this test?)", len(ValidTssKeysignRounds), len(expected))
	}
	for name, want := range expected {
		if got := ValidTssKeysignRounds[name]; got != want {
			t.Errorf("ValidTssKeysignRounds[%q] = %v, want %v", name, got, want)
		}
	}
	for name := range ValidTssKeysignRounds {
		if _, ok := expected[name]; !ok {
			t.Errorf("ValidTssKeysignRounds contains unexpected entry %q", name)
		}
	}
}

// TestKeysignRoundConstants_AreDistinctAndNonEmpty guards against accidental
// duplicate values across the keysign round constants (a typo like setting
// KEYSIGN8 = "SignRound7Message" would silently corrupt blame round mapping).
func TestKeysignRoundConstants_AreDistinctAndNonEmpty(t *testing.T) {
	consts := map[string]string{
		"KEYSIGN1aUnicast":       KEYSIGN1aUnicast,
		"KEYSIGN1b":              KEYSIGN1b,
		"KEYSIGN2Unicast":        KEYSIGN2Unicast,
		"KEYSIGN3":               KEYSIGN3,
		"KEYSIGN4":               KEYSIGN4,
		"KEYSIGN5":               KEYSIGN5,
		"KEYSIGN6":               KEYSIGN6,
		"KEYSIGN7":               KEYSIGN7,
		"KEYSIGN8":               KEYSIGN8,
		"KEYSIGN9":               KEYSIGN9,
		"EDDSAKEYSIGN1":          EDDSAKEYSIGN1,
		"EDDSAKEYSIGN2":          EDDSAKEYSIGN2,
		"EDDSAKEYSIGN3":          EDDSAKEYSIGN3,
		"FROSTKEYSIGNPREPROCESS": FROSTKEYSIGNPREPROCESS,
		"FROSTKEYSIGNSHARE":      FROSTKEYSIGNSHARE,
		"FROSTKEYSIGNRESULT":     FROSTKEYSIGNRESULT,
	}

	seen := map[string]string{}
	for name, value := range consts {
		if value == "" {
			t.Errorf("constant %s is empty", name)
		}
		if prev, ok := seen[value]; ok {
			t.Errorf("constants %s and %s both equal %q", name, prev, value)
		}
		seen[value] = name
	}
}

// TestTSSKEYSIGNROUNDS_FitsAllKeysignIndices pins TSSKEYSIGNROUNDS to the
// maximum blame.RoundInfo.Index + 1 across the ECDSA SignRound types defined
// by tss-lib. tss_helper.go's GetMsgRound emits indices 0..9 for KEYSIGN
// rounds 1..9 (round 1 splits into Message1+Message2), so the value must be
// at least 10. A future tss-lib release that adds round 10 would require
// bumping this — and tests would fail loudly if anyone forgot.
func TestTSSKEYSIGNROUNDS_FitsAllKeysignIndices(t *testing.T) {
	// Max index emitted by GetMsgRound for ECDSA keysign is 9 (KEYSIGN9).
	const maxIndexEmitted = 9
	if TSSKEYSIGNROUNDS <= maxIndexEmitted {
		t.Fatalf("TSSKEYSIGNROUNDS=%d must be greater than max ECDSA keysign index=%d (otherwise round arrays sized by this constant would index-out-of-range)", TSSKEYSIGNROUNDS, maxIndexEmitted)
	}
}
