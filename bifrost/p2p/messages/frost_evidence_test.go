package messages

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	// Hex letters, not just digits: an all-digit hash is unchanged by ToUpper,
	// which would make the case-mutation tests below silently vacuous.
	evidenceHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	evidenceHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	evidenceSess  = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func testEnvelope(payloadHash string) FrostSignedEnvelope {
	return FrostSignedEnvelope{
		ProtocolVersion:  FrostProtocolVersion,
		SessionID:        evidenceSess,
		VaultPubKey:      "tthorpub1vault",
		Network:          "mocknet",
		Round:            FrostBlameRoundShareEquivocation,
		SenderPubKey:     "tthorpub1sender",
		ParticipantIndex: 3,
		PayloadHash:      payloadHash,
		Signature:        []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

func testEvidence() FrostEquivocationEvidence {
	return FrostEquivocationEvidence{
		ProtocolVersion: FrostProtocolVersion,
		First:           testEnvelope(evidenceHashA),
		Second:          testEnvelope(evidenceHashB),
	}
}

func TestFrostEvidenceRoundTrip(t *testing.T) {
	buf, err := MarshalFrostEquivocationEvidence(testEvidence())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalFrostEquivocationEvidence(buf)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.First.PayloadHash != evidenceHashA || got.Second.PayloadHash != evidenceHashB {
		t.Fatalf("round trip did not preserve envelopes: %+v", got)
	}
}

// Marshal must impose a total order on the pair, so the same equivocation
// reported by two different nodes produces the same evidence bytes.
func TestFrostEvidenceMarshalIsOrderIndependent(t *testing.T) {
	ordered, err := MarshalFrostEquivocationEvidence(testEvidence())
	if err != nil {
		t.Fatalf("marshal ordered: %v", err)
	}
	swapped := testEvidence()
	swapped.First, swapped.Second = swapped.Second, swapped.First
	reordered, err := MarshalFrostEquivocationEvidence(swapped)
	if err != nil {
		t.Fatalf("marshal swapped: %v", err)
	}
	if !bytes.Equal(ordered, reordered) {
		t.Fatalf("swapping the pair changed the evidence bytes:\n%s\n%s", ordered, reordered)
	}
}

// Signing and validation both trim and lower-case, so an envelope carrying a
// padded or upper-cased payload hash signs and validates identically to the
// canonical one. If the wire form were not pinned, one equivocation would have
// several valid encodings -- distinct evidence hashes for a single offence.
func TestFrostEvidenceRejectsNonCanonicalEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mutir func(*FrostSignedEnvelope)
	}{
		{"padded payload hash", func(e *FrostSignedEnvelope) { e.PayloadHash = " " + e.PayloadHash }},
		{"upper payload hash", func(e *FrostSignedEnvelope) { e.PayloadHash = strings.ToUpper(e.PayloadHash) }},
		{"padded session id", func(e *FrostSignedEnvelope) { e.SessionID += " " }},
		{"upper session id", func(e *FrostSignedEnvelope) { e.SessionID = strings.ToUpper(e.SessionID) }},
		{"padded network", func(e *FrostSignedEnvelope) { e.Network = " " + e.Network }},
		{"padded round", func(e *FrostSignedEnvelope) { e.Round += " " }},
		{"padded sender", func(e *FrostSignedEnvelope) { e.SenderPubKey = " " + e.SenderPubKey }},
		{"padded vault", func(e *FrostSignedEnvelope) { e.VaultPubKey += " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := testEvidence()
			tc.mutir(&evidence.First)
			// Hand-encode: Marshal would canonicalize the mutation away.
			buf, err := json.Marshal(evidence)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if _, err := UnmarshalFrostEquivocationEvidence(buf); err == nil {
				t.Fatal("non-canonical evidence was accepted")
			}
		})
	}
}

// The malleability this guards: a padded payload hash sorts on the space
// (0x20, below every hex digit) while the signature and Validate see only the
// trimmed value. Marshal must normalize before ordering, or a second valid
// encoding of the same equivocation exists.
func TestFrostEvidenceMarshalNormalizesBeforeOrdering(t *testing.T) {
	evidence := testEvidence()
	evidence.First.PayloadHash = " " + evidenceHashB
	evidence.Second.PayloadHash = evidenceHashA

	buf, err := MarshalFrostEquivocationEvidence(evidence)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalFrostEquivocationEvidence(buf)
	if err != nil {
		t.Fatalf("marshal produced evidence it will not decode: %v", err)
	}
	if got.First.PayloadHash != evidenceHashA || got.Second.PayloadHash != evidenceHashB {
		t.Fatalf("padding defeated the canonical ordering: %+v", got)
	}

	canonical, err := MarshalFrostEquivocationEvidence(testEvidence())
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	if !bytes.Equal(buf, canonical) {
		t.Fatalf("a padded hash produced a second encoding of one equivocation:\n%s\n%s", buf, canonical)
	}
}

func TestFrostEvidenceRejectsNonCanonicalOrdering(t *testing.T) {
	evidence := testEvidence()
	evidence.First, evidence.Second = evidence.Second, evidence.First // B before A
	buf, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := UnmarshalFrostEquivocationEvidence(buf); err == nil {
		t.Fatal("descending payload-hash ordering was accepted")
	}
}

func TestFrostEvidenceValidateRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mutir func(*FrostEquivocationEvidence)
	}{
		{"bad version", func(e *FrostEquivocationEvidence) { e.ProtocolVersion = 1 }},
		{"identical payload hashes", func(e *FrostEquivocationEvidence) { e.Second.PayloadHash = e.First.PayloadHash }},
		{"payload hashes differ only by case", func(e *FrostEquivocationEvidence) {
			e.Second.PayloadHash = strings.ToUpper(e.First.PayloadHash)
		}},
		{"cross-session pair", func(e *FrostEquivocationEvidence) { e.Second.SessionID = strings.Repeat("b", 64) }},
		{"cross-vault pair", func(e *FrostEquivocationEvidence) { e.Second.VaultPubKey = "tthorpub1other" }},
		{"cross-network pair", func(e *FrostEquivocationEvidence) { e.Second.Network = "stagenet" }},
		{"cross-round pair", func(e *FrostEquivocationEvidence) { e.Second.Round = FrostBlameRoundPreprocessEquivocation }},
		{"cross-sender pair", func(e *FrostEquivocationEvidence) { e.Second.SenderPubKey = "tthorpub1other" }},
		{"cross-index pair", func(e *FrostEquivocationEvidence) { e.Second.ParticipantIndex = 9 }},
		{"empty signature", func(e *FrostEquivocationEvidence) { e.First.Signature = nil }},
		{"oversized signature", func(e *FrostEquivocationEvidence) {
			e.First.Signature = bytes.Repeat([]byte{1}, frostMaxSignatureSize+1)
		}},
		{"unknown network", func(e *FrostEquivocationEvidence) {
			e.First.Network, e.Second.Network = "testnet", "testnet"
		}},
		{"zero participant index", func(e *FrostEquivocationEvidence) {
			e.First.ParticipantIndex, e.Second.ParticipantIndex = 0, 0
		}},
		{"short session id", func(e *FrostEquivocationEvidence) {
			e.First.SessionID, e.Second.SessionID = "abcd", "abcd"
		}},
		{"non-hex session id", func(e *FrostEquivocationEvidence) {
			e.First.SessionID, e.Second.SessionID = strings.Repeat("z", 64), strings.Repeat("z", 64)
		}},
		{"non-hex payload hash", func(e *FrostEquivocationEvidence) {
			e.First.PayloadHash = strings.Repeat("z", 64)
		}},
		{"short payload hash", func(e *FrostEquivocationEvidence) { e.First.PayloadHash = "abcd" }},
		{"oversized vault key", func(e *FrostEquivocationEvidence) {
			e.First.VaultPubKey = strings.Repeat("a", frostMaxVaultKeyChars+1)
			e.Second.VaultPubKey = e.First.VaultPubKey
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := testEvidence()
			tc.mutir(&evidence)
			if err := evidence.Validate(); err == nil {
				t.Fatal("Validate accepted invalid evidence")
			}
			if _, err := MarshalFrostEquivocationEvidence(evidence); err == nil {
				t.Fatal("Marshal accepted invalid evidence")
			}
		})
	}
}

func TestFrostEvidenceUnmarshalRejectsMalformedBuffers(t *testing.T) {
	valid, err := MarshalFrostEquivocationEvidence(testEvidence())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, tc := range []struct {
		name string
		buf  []byte
	}{
		{"empty", nil},
		{"oversized", bytes.Repeat([]byte{'a'}, FrostMaxEvidenceBytes+1)},
		{"garbage", []byte("{")},
		{"trailing data", append(append([]byte{}, valid...), []byte(`{"extra":1}`)...)},
		{"unknown field", []byte(`{"protocol_version":2,"first":{},"second":{},"attacker":1}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UnmarshalFrostEquivocationEvidence(tc.buf); err == nil {
				t.Fatal("malformed evidence was accepted")
			}
		})
	}
}

// The domain tag is what stops an envelope statement being replayed as an echo
// statement, and every identifying field must be bound into the signed bytes.
func TestFrostSignBytesDomainSeparationAndBinding(t *testing.T) {
	envelope := testEnvelope(evidenceHashA)
	base, err := envelope.SignBytes()
	if err != nil {
		t.Fatalf("sign bytes: %v", err)
	}
	if !bytes.HasPrefix(base, []byte("domain=thorchain-frost-signed-envelope-v2")) {
		t.Fatalf("envelope statement is not domain separated: %s", base)
	}

	echo := FrostSignedEcho{
		ProtocolVersion: FrostProtocolVersion,
		AttesterPubKey:  "tthorpub1attester",
		Target:          envelope,
		Signature:       []byte{1},
	}
	echoBytes, err := echo.SignBytes()
	if err != nil {
		t.Fatalf("echo sign bytes: %v", err)
	}
	if !bytes.HasPrefix(echoBytes, []byte("domain=thorchain-frost-signed-echo-v2")) {
		t.Fatalf("echo statement is not domain separated: %s", echoBytes)
	}
	if bytes.Equal(base, echoBytes) {
		t.Fatal("envelope and echo produce the same signing bytes")
	}

	// Every identifying field must change the statement.
	for _, tc := range []struct {
		name  string
		mutir func(*FrostSignedEnvelope)
	}{
		{"session", func(e *FrostSignedEnvelope) { e.SessionID = strings.Repeat("c", 64) }},
		{"vault", func(e *FrostSignedEnvelope) { e.VaultPubKey = "tthorpub1other" }},
		{"network", func(e *FrostSignedEnvelope) { e.Network = "stagenet" }},
		{"round", func(e *FrostSignedEnvelope) { e.Round = FrostBlameRoundPreprocessEquivocation }},
		{"sender", func(e *FrostSignedEnvelope) { e.SenderPubKey = "tthorpub1other" }},
		{"index", func(e *FrostSignedEnvelope) { e.ParticipantIndex = 7 }},
		{"payload", func(e *FrostSignedEnvelope) { e.PayloadHash = evidenceHashB }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := testEnvelope(evidenceHashA)
			tc.mutir(&mutated)
			got, err := mutated.SignBytes()
			if err != nil {
				t.Fatalf("sign bytes: %v", err)
			}
			if bytes.Equal(base, got) {
				t.Fatalf("%s is not bound into the signed statement", tc.name)
			}
		})
	}
}

// Whitespace and case must not change the signed statement, since that is the
// normalization the canonical wire form is pinned against.
func TestFrostSignBytesNormalizesFields(t *testing.T) {
	base, err := testEnvelope(evidenceHashA).SignBytes()
	if err != nil {
		t.Fatalf("sign bytes: %v", err)
	}
	padded := testEnvelope(" " + strings.ToUpper(evidenceHashA) + " ")
	padded.SessionID = " " + strings.ToUpper(evidenceSess)
	padded.Network = " MOCKNET "
	got, err := padded.SignBytes()
	if err != nil {
		t.Fatalf("padded sign bytes: %v", err)
	}
	if !bytes.Equal(base, got) {
		t.Fatalf("normalization is not applied to the signed statement:\n%s\n%s", base, got)
	}
}

func TestFrostSignBytesRejectsUnsafeFreeFormFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mutir func(*FrostSignedEnvelope)
	}{
		{"newline in vault", func(e *FrostSignedEnvelope) { e.VaultPubKey += "\nround=frost_share" }},
		{"newline in round", func(e *FrostSignedEnvelope) { e.Round += "\nsender_pub_key=tthorpub1other" }},
		{"newline in sender", func(e *FrostSignedEnvelope) { e.SenderPubKey += "\nparticipant_index=9" }},
		{"control in round", func(e *FrostSignedEnvelope) { e.Round += "\tother" }},
		{"non ascii sender", func(e *FrostSignedEnvelope) { e.SenderPubKey += "é" }},
		{"oversized round", func(e *FrostSignedEnvelope) { e.Round = strings.Repeat("r", frostMaxRoundChars+1) }},
		{"oversized sender", func(e *FrostSignedEnvelope) {
			e.SenderPubKey = strings.Repeat("p", frostMaxParticipantKeyChars+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := testEnvelope(evidenceHashA)
			tc.mutir(&envelope)
			if _, err := envelope.SignBytes(); err == nil {
				t.Fatal("unsafe envelope field was accepted")
			}
		})
	}

	for _, attester := range []string{
		"tthorpub1attester\ntarget_statement=00",
		"tthorpub1attester\tother",
		strings.Repeat("p", frostMaxParticipantKeyChars+1),
	} {
		echo := FrostSignedEcho{
			ProtocolVersion: FrostProtocolVersion,
			AttesterPubKey:  attester,
			Target:          testEnvelope(evidenceHashA),
		}
		if _, err := echo.SignBytes(); err == nil {
			t.Fatalf("unsafe echo attester %q was accepted", attester)
		}
	}
}

// An echo binds the target's signature, so it cannot be transplanted onto a
// different envelope carrying the same statement.
func TestFrostEchoBindsTargetSignature(t *testing.T) {
	echo := FrostSignedEcho{
		ProtocolVersion: FrostProtocolVersion,
		AttesterPubKey:  "tthorpub1attester",
		Target:          testEnvelope(evidenceHashA),
		Signature:       []byte{1},
	}
	base, err := echo.SignBytes()
	if err != nil {
		t.Fatalf("sign bytes: %v", err)
	}

	transplanted := echo
	transplanted.Target.Signature = []byte{0xff, 0xff}
	got, err := transplanted.SignBytes()
	if err != nil {
		t.Fatalf("sign bytes: %v", err)
	}
	if bytes.Equal(base, got) {
		t.Fatal("the target signature is not bound into the echo statement")
	}

	attester := echo
	attester.AttesterPubKey = "tthorpub1other"
	got, err = attester.SignBytes()
	if err != nil {
		t.Fatalf("sign bytes: %v", err)
	}
	if bytes.Equal(base, got) {
		t.Fatal("the attester is not bound into the echo statement")
	}
}

func TestFrostEvidenceBlameRoundHelpers(t *testing.T) {
	for _, round := range []string{FrostBlameRoundPreprocessEquivocation, FrostBlameRoundShareEquivocation} {
		if !IsFrostEvidenceBlameRound(round) {
			t.Fatalf("%s is not recognized as a blame round", round)
		}
		if _, ok := FrostEvidenceTranscriptRound(round); !ok {
			t.Fatalf("%s has no transcript round", round)
		}
	}
	if IsFrostEvidenceBlameRound("frost_share") {
		t.Fatal("a transcript round name must not be accepted as a blame round")
	}
	if _, ok := FrostEvidenceTranscriptRound("nonsense"); ok {
		t.Fatal("an unknown round produced a transcript round")
	}
}
