package observer

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestSecurityMutationAttestationAuthenticityAndReplay(t *testing.T) {
	key := secp256k1.GenPrivKey()
	other := secp256k1.GenPrivKey()
	payload := []byte("transaction-A")
	sig, err := key.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	state := []attestationSentState{}
	valid := mutationMessage{payload: payload, attestation: &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: sig}}
	if err = ProcessAttestation(&state, valid); err != nil {
		t.Fatal(err)
	}
	if len(state) != 1 {
		t.Fatalf("valid count = %d", len(state))
	}
	if err = ProcessAttestation(&state, valid); err != nil || len(state) != 1 {
		t.Fatalf("duplicate changed count to %d: %v", len(state), err)
	}
	otherSig, err := other.Sign([]byte("transaction-C"))
	if err != nil {
		t.Fatal(err)
	}
	invalid := mutationMessage{payload: []byte("transaction-B"), attestation: &common.Attestation{PubKey: other.PubKey().Bytes(), Signature: otherSig}}
	if err := ProcessAttestation(&state, invalid); err == nil || len(state) != 1 {
		t.Fatalf("forged attestation accepted: count=%d err=%v", len(state), err)
	}
}

type mutationMessage struct {
	payload     []byte
	attestation *common.Attestation
}

func (m mutationMessage) GetAttestation() *common.Attestation { return m.attestation }
func (m mutationMessage) GetSignablePayload() ([]byte, error) { return m.payload, nil }
