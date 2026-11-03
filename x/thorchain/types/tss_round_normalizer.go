package types

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/tssrounds"
	"gitlab.com/thorchain/thornode/v3/common"
)

// CanonicalTssRound normalizes raw TSS round names into the canonical constants
// used in Thornode consensus and telemetry. When algo is empty, it is inferred
// from the raw round name when possible.
func CanonicalTssRound(round string, algo common.SigningAlgo) string {
	if round == "" {
		return ""
	}

	switch {
	case strings.Contains(round, ".eddsa."):
		if algo == "" {
			algo = common.SigningAlgoEd25519
		}
	case strings.Contains(round, ".ecdsa."):
		if algo == "" {
			algo = common.SigningAlgoSecp256k1
		}
	}

	switch algo {
	case common.SigningAlgoSecp256k1:
		return canonicalSecp256k1TssRound(round)
	case common.SigningAlgoEd25519:
		return canonicalEd25519TssRound(round)
	default:
		return round
	}
}

// NormalizeTssKeysignRound canonicalizes a keysign round and verifies that the
// result is one of the supported keysign-round identifiers.
func NormalizeTssKeysignRound(round string) (string, error) {
	canonical := CanonicalTssRound(round, "")
	if canonical == "" {
		return "", nil
	}
	if !tssrounds.ValidTssKeysignRounds[canonical] {
		return "", fmt.Errorf("invalid blame round: %s", round)
	}
	return canonical, nil
}

// canonicalSecp256k1TssRound maps secp256k1 TSS library phase names into the
// stable round identifiers used in messages and state.
func canonicalSecp256k1TssRound(round string) string {
	switch {
	case round == tssrounds.KEYGEN1 || strings.HasSuffix(round, "KGRound1Message"):
		return tssrounds.KEYGEN1
	case round == tssrounds.KEYGEN2aUnicast || strings.HasSuffix(round, "KGRound2Message1"):
		return tssrounds.KEYGEN2aUnicast
	case round == tssrounds.KEYGEN2b || strings.HasSuffix(round, "KGRound2Message2"):
		return tssrounds.KEYGEN2b
	case round == tssrounds.KEYGEN3 || strings.HasSuffix(round, "KGRound3Message"):
		return tssrounds.KEYGEN3
	case round == tssrounds.KEYSIGN1aUnicast || strings.HasSuffix(round, "SignRound1Message1"):
		return tssrounds.KEYSIGN1aUnicast
	case round == tssrounds.KEYSIGN1b || strings.HasSuffix(round, "SignRound1Message2"):
		return tssrounds.KEYSIGN1b
	case round == tssrounds.KEYSIGN2Unicast || strings.HasSuffix(round, "SignRound2Message"):
		return tssrounds.KEYSIGN2Unicast
	case round == tssrounds.KEYSIGN3 || strings.HasSuffix(round, "SignRound3Message"):
		return tssrounds.KEYSIGN3
	case round == tssrounds.KEYSIGN4 || strings.HasSuffix(round, "SignRound4Message"):
		return tssrounds.KEYSIGN4
	case round == tssrounds.KEYSIGN5 || strings.HasSuffix(round, "SignRound5Message"):
		return tssrounds.KEYSIGN5
	case round == tssrounds.KEYSIGN6 || strings.HasSuffix(round, "SignRound6Message"):
		return tssrounds.KEYSIGN6
	case round == tssrounds.KEYSIGN7 || strings.HasSuffix(round, "SignRound7Message"):
		return tssrounds.KEYSIGN7
	case round == tssrounds.KEYSIGN8 || strings.HasSuffix(round, "SignRound8Message"):
		return tssrounds.KEYSIGN8
	case round == tssrounds.KEYSIGN9 || strings.HasSuffix(round, "SignRound9Message"):
		return tssrounds.KEYSIGN9
	default:
		return round
	}
}

// canonicalEd25519TssRound maps ed25519 TSS library phase names into the
// stable round identifiers used in messages and state.
func canonicalEd25519TssRound(round string) string {
	switch {
	case round == tssrounds.EDDSAKEYGEN1 || strings.HasSuffix(round, "KGRound1Message"):
		return tssrounds.EDDSAKEYGEN1
	case round == tssrounds.EDDSAKEYGEN2a || strings.HasSuffix(round, "KGRound2Message1"):
		return tssrounds.EDDSAKEYGEN2a
	case round == tssrounds.EDDSAKEYGEN2b || strings.HasSuffix(round, "KGRound2Message2"):
		return tssrounds.EDDSAKEYGEN2b
	case round == tssrounds.EDDSAKEYSIGN1 || strings.HasSuffix(round, "SignRound1Message"):
		return tssrounds.EDDSAKEYSIGN1
	case round == tssrounds.EDDSAKEYSIGN2 || strings.HasSuffix(round, "SignRound2Message"):
		return tssrounds.EDDSAKEYSIGN2
	case round == tssrounds.EDDSAKEYSIGN3 || strings.HasSuffix(round, "SignRound3Message"):
		return tssrounds.EDDSAKEYSIGN3
	default:
		return round
	}
}
