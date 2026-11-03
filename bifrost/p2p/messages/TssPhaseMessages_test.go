package messages

import (
	"testing"
)

func TestValidTssKeysignRounds_RejectsUnvalidatedFrostEvidence(t *testing.T) {
	for _, round := range []string{
		FrostBlameRoundPreprocessEquivocation,
		FrostBlameRoundShareEquivocation,
	} {
		if ValidTssKeysignRounds[round] {
			t.Fatalf("unvalidated evidence round %q must not be accepted on-chain", round)
		}
	}
}
