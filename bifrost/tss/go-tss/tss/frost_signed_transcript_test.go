package tss

import (
	"strings"
	"testing"

	cometsecp "github.com/cometbft/cometbft/crypto/secp256k1"
	cossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	legacybech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

func TestFrostSignedEnvelopeAndEcho(t *testing.T) {
	ensureBech32Prefix()
	privateKey := cometsecp.GenPrivKey()
	pubKey, err := legacybech32.MarshalPubKey(legacybech32.AccPK, &cossecp.PubKey{Key: privateKey.PubKey().Bytes()}) //nolint:staticcheck
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	envelope, err := newFrostSignedEnvelope(
		privateKey,
		strings.Repeat("a", 64),
		"thorpub1vault-a",
		"mainnet",
		frostRoundPreprocess,
		pubKey,
		1,
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatalf("sign envelope: %v", err)
	}
	if err := verifyFrostSignedEnvelope(envelope); err != nil {
		t.Fatalf("verify envelope: %v", err)
	}

	echo, err := newFrostSignedEcho(privateKey, pubKey, envelope)
	if err != nil {
		t.Fatalf("sign echo: %v", err)
	}
	if err := verifyFrostSignedEcho(echo); err != nil {
		t.Fatalf("verify echo: %v", err)
	}

	tampered := envelope
	tampered.PayloadHash = strings.Repeat("c", 64)
	if err := verifyFrostSignedEnvelope(tampered); err == nil {
		t.Fatalf("tampered payload hash must invalidate target signature")
	}
	tampered = envelope
	tampered.VaultPubKey = "thorpub1vault-b"
	if err := verifyFrostSignedEnvelope(tampered); err == nil {
		t.Fatalf("cross-vault replay must invalidate target signature")
	}
	tampered = envelope
	tampered.Network = "stagenet"
	if err := verifyFrostSignedEnvelope(tampered); err == nil {
		t.Fatalf("cross-network replay must invalidate target signature")
	}
}

func TestFrostEquivocationEvidenceRequiresTwoTargetSignatures(t *testing.T) {
	ensureBech32Prefix()
	privateKey := cometsecp.GenPrivKey()
	pubKey, err := legacybech32.MarshalPubKey(legacybech32.AccPK, &cossecp.PubKey{Key: privateKey.PubKey().Bytes()}) //nolint:staticcheck
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	first, err := newFrostSignedEnvelope(privateKey, strings.Repeat("a", 64), "thorpub1vault-a", "mainnet", frostRoundShare, pubKey, 1, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("sign first: %v", err)
	}
	second, err := newFrostSignedEnvelope(privateKey, strings.Repeat("a", 64), "thorpub1vault-a", "mainnet", frostRoundShare, pubKey, 1, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("sign second: %v", err)
	}
	encoded, err := messages.MarshalFrostEquivocationEvidence(messages.FrostEquivocationEvidence{
		ProtocolVersion: messages.FrostProtocolVersion,
		First:           second, // marshal must canonicalize statement order
		Second:          first,
	})
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	decoded, err := messages.UnmarshalFrostEquivocationEvidence(encoded)
	if err != nil {
		t.Fatalf("unmarshal evidence: %v", err)
	}
	if decoded.First.PayloadHash != first.PayloadHash || decoded.Second.PayloadHash != second.PayloadHash {
		t.Fatalf("evidence was not canonically ordered")
	}
	decoded.Second.Signature[0] ^= 0xff
	if err := verifyFrostSignedEnvelope(decoded.Second); err == nil {
		t.Fatalf("tampered second target signature must be rejected")
	}
}

func TestFrostEquivocationEvidenceRejectsCrossVaultPair(t *testing.T) {
	ensureBech32Prefix()
	privateKey := cometsecp.GenPrivKey()
	pubKey, err := legacybech32.MarshalPubKey(legacybech32.AccPK, &cossecp.PubKey{Key: privateKey.PubKey().Bytes()}) //nolint:staticcheck
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	first, err := newFrostSignedEnvelope(privateKey, strings.Repeat("a", 64), "thorpub1vault-a", "mainnet", frostRoundShare, pubKey, 1, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("sign first: %v", err)
	}
	second, err := newFrostSignedEnvelope(privateKey, strings.Repeat("a", 64), "thorpub1vault-b", "mainnet", frostRoundShare, pubKey, 1, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("sign second: %v", err)
	}
	_, err = messages.MarshalFrostEquivocationEvidence(messages.FrostEquivocationEvidence{
		ProtocolVersion: messages.FrostProtocolVersion,
		First:           first,
		Second:          second,
	})
	if err == nil {
		t.Fatal("evidence assembled across vaults must be rejected")
	}
}

func TestFrostEquivocationEvidenceRejectsCrossNetworkPair(t *testing.T) {
	ensureBech32Prefix()
	privateKey := cometsecp.GenPrivKey()
	pubKey, err := legacybech32.MarshalPubKey(legacybech32.AccPK, &cossecp.PubKey{Key: privateKey.PubKey().Bytes()}) //nolint:staticcheck
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	first, err := newFrostSignedEnvelope(privateKey, strings.Repeat("a", 64), "thorpub1vault-a", "mainnet", frostRoundShare, pubKey, 1, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("sign first: %v", err)
	}
	second, err := newFrostSignedEnvelope(privateKey, strings.Repeat("a", 64), "thorpub1vault-a", "stagenet", frostRoundShare, pubKey, 1, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("sign second: %v", err)
	}
	_, err = messages.MarshalFrostEquivocationEvidence(messages.FrostEquivocationEvidence{
		ProtocolVersion: messages.FrostProtocolVersion,
		First:           first,
		Second:          second,
	})
	if err == nil {
		t.Fatal("evidence assembled across deployment networks must be rejected")
	}
}

func TestFrostEquivocationRemainsNonActionableOnChain(t *testing.T) {
	coord := &FrostSignCoordinator{}
	resp := coord.failForEquivocation(messages.FrostEquivocationEvidence{
		First: messages.FrostSignedEnvelope{
			Round:        frostRoundShare,
			SenderPubKey: "thorpub1culprit",
			PayloadHash:  strings.Repeat("a", 64),
		},
		Second: messages.FrostSignedEnvelope{PayloadHash: strings.Repeat("b", 64)},
	})
	if resp.Status != tcommon.Fail {
		t.Fatalf("equivocation must fail the local ceremony, got %q", resp.Status)
	}
	if resp.Blame.Round != frostRoundShare || len(resp.Blame.BlameNodes) != 0 {
		t.Fatalf("unvalidated evidence must not become on-chain blame: %+v", resp.Blame)
	}
}
