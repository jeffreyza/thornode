package tss

import (
	"testing"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/common"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestCanonicalTssRoundSecp256k1(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"thorchain.tsslib.ecdsa.keygen.KGRound1Message":     tssMessages.KEYGEN1,
		"thorchain.tsslib.ecdsa.keygen.KGRound2Message1":    tssMessages.KEYGEN2aUnicast,
		"thorchain.tsslib.ecdsa.signing.SignRound1Message1": tssMessages.KEYSIGN1aUnicast,
		"thorchain.tsslib.ecdsa.signing.SignRound2Message":  tssMessages.KEYSIGN2Unicast,
		tssMessages.KEYSIGN7:                                tssMessages.KEYSIGN7,
		"thorchain.tsslib.ecdsa.signing.SignRound7Message":  tssMessages.KEYSIGN7,
		"thorchain.tsslib.ecdsa.signing.SignRound8Message":  tssMessages.KEYSIGN8,
		"thorchain.tsslib.ecdsa.signing.SignRound9Message":  tssMessages.KEYSIGN9,
		"unrelated-round": "unrelated-round",
	}

	for in, want := range tests {
		if got := thorchaintypes.CanonicalTssRound(in, common.SigningAlgoSecp256k1); got != want {
			t.Errorf("CanonicalTssRound(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalTssRoundEd25519(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"thorchain.tsslib.eddsa.keygen.KGRound1Message":    tssMessages.EDDSAKEYGEN1,
		"thorchain.tsslib.eddsa.keygen.KGRound2Message2":   tssMessages.EDDSAKEYGEN2b,
		"thorchain.tsslib.eddsa.signing.SignRound1Message": tssMessages.EDDSAKEYSIGN1,
		"thorchain.tsslib.eddsa.signing.SignRound3Message": tssMessages.EDDSAKEYSIGN3,
		tssMessages.EDDSAKEYSIGN2:                          tssMessages.EDDSAKEYSIGN2,
		"unrelated-round":                                  "unrelated-round",
	}

	for in, want := range tests {
		if got := thorchaintypes.CanonicalTssRound(in, common.SigningAlgoEd25519); got != want {
			t.Errorf("CanonicalTssRound(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKeysignErrorIsTerminalRound verifies the signer retry path recognises
// final-round names for secp256k1 (v3 KEYSIGN9 only), ed25519
// (EDDSAKEYSIGN3), and FROST's share-exposure boundary (frost_share and
// frost_result). The legacy v1 KEYSIGN7 marker is intentionally not accepted
// on the signer side.
func TestKeysignErrorIsTerminalRound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		round string
		want  bool
	}{
		{
			// tss-lib v3 terminal ECDSA keysign round.
			name:  "canonical secp v3 terminal round",
			round: tssMessages.KEYSIGN9,
			want:  true,
		},
		{
			name:  "raw secp v3 terminal round",
			round: "thorchain.tsslib.ecdsa.signing.SignRound9Message",
			want:  true,
		},
		{
			name:  "canonical eddsa last round",
			round: tssMessages.EDDSAKEYSIGN3,
			want:  true,
		},
		{
			name:  "raw eddsa last round",
			round: "thorchain.tsslib.eddsa.signing.SignRound3Message",
			want:  true,
		},
		{
			name:  "frost share exposure boundary",
			round: tssMessages.FROSTKEYSIGNSHARE,
			want:  true,
		},
		{
			name:  "frost terminal result",
			round: tssMessages.FROSTKEYSIGNRESULT,
			want:  true,
		},
		{
			// v1 terminal round — not accepted on the signer side.
			name:  "non-terminal secp v1 round 7",
			round: tssMessages.KEYSIGN7,
			want:  false,
		},
		{
			name:  "non-terminal raw secp round 7",
			round: "thorchain.tsslib.ecdsa.signing.SignRound7Message",
			want:  false,
		},
		{
			name:  "non-terminal round",
			round: tssMessages.KEYSIGN5,
			want:  false,
		},
		// v3 added rounds 8 and 9 to the secp keysign protocol — 9 is the
		// only terminal one. Pin 8 to false so a future refactor that
		// widens the accept set is caught.
		{
			name:  "non-terminal secp round 8",
			round: tssMessages.KEYSIGN8,
			want:  false,
		},
		// Non-terminal EdDSA keysign rounds.
		{
			name:  "non-terminal eddsa round 1",
			round: tssMessages.EDDSAKEYSIGN1,
			want:  false,
		},
		{
			name:  "non-terminal eddsa round 2",
			round: tssMessages.EDDSAKEYSIGN2,
			want:  false,
		},
		{
			name:  "non-terminal frost preprocess",
			round: tssMessages.FROSTKEYSIGNPREPROCESS,
			want:  false,
		},
		// Keygen rounds are never terminal-keysign even though they share a
		// numeric scheme. Guard against accidental cross-protocol confusion.
		{
			name:  "keygen secp round 1 is not terminal keysign",
			round: tssMessages.KEYGEN1,
			want:  false,
		},
		{
			name:  "keygen secp round 3 is not terminal keysign",
			round: tssMessages.KEYGEN3,
			want:  false,
		},
		{
			name:  "keygen eddsa round 1 is not terminal keysign",
			round: tssMessages.EDDSAKEYGEN1,
			want:  false,
		},
		// Empty + arbitrary strings must not be terminal.
		{
			name:  "empty round",
			round: "",
			want:  false,
		},
		{
			name:  "unknown round string",
			round: "SomeUnknownRoundMessage",
			want:  false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := NewKeysignError(thorchaintypes.Blame{Round: tc.round})
			if got := err.IsTerminalRound(); got != tc.want {
				t.Fatalf("IsTerminalRound(%q) = %v, want %v", tc.round, got, tc.want)
			}
		})
	}
}

// TestKeysignErrorIsJoinPartyFailure verifies only a sync failure before any
// round ran counts as a failure to form the keysign party.
func TestKeysignErrorIsJoinPartyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
		round  string
		want   bool
	}{
		{name: "sync failure before any round", reason: blame.TssSyncFail, want: true},
		{name: "sync failure with a round", reason: blame.TssSyncFail, round: tssMessages.KEYSIGN2Unicast, want: false},
		{name: "timeout in a round", reason: blame.TssTimeout, round: tssMessages.KEYSIGN2Unicast, want: false},
		{name: "timeout without a round", reason: blame.TssTimeout, want: false},
		{name: "terminal round", reason: blame.TssTimeout, round: tssMessages.KEYSIGN9, want: false},
		{name: "internal error", reason: blame.InternalError, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := NewKeysignError(thorchaintypes.Blame{FailReason: tc.reason, Round: tc.round})
			if got := err.IsJoinPartyFailure(); got != tc.want {
				t.Fatalf("IsJoinPartyFailure(%q, %q) = %v, want %v", tc.reason, tc.round, got, tc.want)
			}
		})
	}
}
