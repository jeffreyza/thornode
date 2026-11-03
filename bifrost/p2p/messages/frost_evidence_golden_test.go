package messages

import (
	"encoding/hex"
	"strings"
	"testing"
)

// goldenEnvelope is a fully-specified, already-canonical envelope. Its SignBytes
// output is frozen below. Do not "fix" the golden string to match a code change
// without understanding that the bytes ARE the network contract: every node that
// signed an envelope under the old layout produced a signature over the old
// bytes, so any reordering, separator change, or domain edit silently
// invalidates every previously-signed envelope and echo on the network. If this
// test fails, that is the decision you are making — make it deliberately and
// bump the version domain.
func goldenEnvelope() FrostSignedEnvelope {
	return FrostSignedEnvelope{
		ProtocolVersion:  FrostProtocolVersion,
		SessionID:        strings.Repeat("a", frostHashHexChars),
		VaultPubKey:      "thorpub1addwnpepqvault",
		Network:          "mocknet",
		Round:            FrostBlameRoundPreprocessEquivocation,
		SenderPubKey:     "thorpub1addwnpepqsender",
		ParticipantIndex: 3,
		PayloadHash:      strings.Repeat("b", frostHashHexChars),
		Signature:        []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

const goldenEnvelopeSignBytes = "domain=thorchain-frost-signed-envelope-v2\n" +
	"protocol_version=2\n" +
	"session_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
	"vault_pub_key=thorpub1addwnpepqvault\n" +
	"network=mocknet\n" +
	"round=FROSTV2PreprocessEquivocation\n" +
	"sender_pub_key=thorpub1addwnpepqsender\n" +
	"participant_index=3\n" +
	"payload_hash=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestFrostSignedEnvelopeSignBytesGoldenVector(t *testing.T) {
	got, err := goldenEnvelope().SignBytes()
	if err != nil {
		t.Fatalf("golden envelope must produce sign bytes: %v", err)
	}
	if string(got) != goldenEnvelopeSignBytes {
		t.Fatalf("FROST envelope sign-bytes layout changed.\n got: %q\nwant: %q\n"+
			"this silently invalidates every previously-signed envelope on the network; "+
			"if intentional, bump the domain version", string(got), goldenEnvelopeSignBytes)
	}
}

// The echo binds the target's signature into its own signed bytes (the
// anti-transplant property). Freeze the echo layout too, since it references
// the envelope bytes as hex and any change to either breaks signature
// compatibility.
func TestFrostSignedEchoSignBytesGoldenVector(t *testing.T) {
	env := goldenEnvelope()
	echo := FrostSignedEcho{
		ProtocolVersion: FrostProtocolVersion,
		AttesterPubKey:  "thorpub1addwnpepqattester",
		Target:          env,
		Signature:       []byte{0x01, 0x02},
	}

	want := "domain=thorchain-frost-signed-echo-v2\n" +
		"protocol_version=2\n" +
		"attester_pub_key=thorpub1addwnpepqattester\n" +
		"target_statement=" + hex.EncodeToString([]byte(goldenEnvelopeSignBytes)) + "\n" +
		"target_signature=" + hex.EncodeToString(env.Signature)

	got, err := echo.SignBytes()
	if err != nil {
		t.Fatalf("golden echo must produce sign bytes: %v", err)
	}
	if string(got) != want {
		t.Fatalf("FROST echo sign-bytes layout changed.\n got: %q\nwant: %q", string(got), want)
	}
	// The target signature must actually appear in the signed bytes, or an echo
	// could be lifted onto a different proof.
	if !strings.Contains(string(got), hex.EncodeToString(env.Signature)) {
		t.Fatal("echo sign bytes must bind the target signature")
	}
}
