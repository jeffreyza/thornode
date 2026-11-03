package messages

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzFrostEvidenceDecode(f *testing.F) {
	valid, err := MarshalFrostEquivocationEvidence(testEvidence())
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{nil, []byte("{}"), []byte("{"), []byte(`{"protocol_version":2,"first":{},"second":{}}`), valid, append([]byte{' '}, valid...), append(append([]byte{}, valid...), 'x')} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > FrostMaxEvidenceBytes+1 {
			t.Skip()
		}
		evidence, err := UnmarshalFrostEquivocationEvidence(raw)
		if err != nil {
			return
		}
		if len(raw) == 0 || len(raw) > FrostMaxEvidenceBytes || evidence.Validate() != nil {
			t.Fatalf("accepted invalid evidence")
		}
		encoded, err := MarshalFrostEquivocationEvidence(evidence)
		if err != nil || !bytes.Equal(raw, encoded) {
			t.Fatalf("accepted noncanonical evidence: marshal error %v", err)
		}
	})
}

func FuzzFrostSignedPayload(f *testing.F) {
	for _, seed := range []string{"", "thorpub1attacker", "thorpub1addwnpepqsender", "bad\nfield", strings.Repeat("x", frostMaxParticipantKeyChars+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sender string) {
		if len(sender) > 512 {
			t.Skip()
		}
		envelope := goldenEnvelope()
		envelope.SenderPubKey = sender
		original, err := envelope.SignBytes()
		if err != nil {
			return
		}
		tampered := envelope
		tampered.PayloadHash = strings.Repeat("c", frostHashHexChars)
		changed, err := tampered.SignBytes()
		if err != nil || bytes.Equal(original, changed) {
			t.Fatalf("payload hash tampering did not change signed bytes: %v", err)
		}
		echo := FrostSignedEcho{ProtocolVersion: FrostProtocolVersion, AttesterPubKey: "thorpub1attester", Target: envelope}
		echoBytes, err := echo.SignBytes()
		if err != nil {
			t.Fatal(err)
		}
		echo.Target.Signature[0] ^= 1
		changedEcho, err := echo.SignBytes()
		if err != nil || bytes.Equal(echoBytes, changedEcho) {
			t.Fatalf("target signature tampering did not change echo bytes: %v", err)
		}
	})
}
