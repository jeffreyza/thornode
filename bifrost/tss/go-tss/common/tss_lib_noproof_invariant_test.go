package common

import (
	"math/big"
	"testing"

	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"
	btss "gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"
)

// TestTssLib_NoProofGuardsMandatory_InV3 documents and pins a core tss-lib v3
// invariant: the Paillier-Blum modproof and Π-fac facproof gates are mandatory
// and cannot be bypassed.
//
// In v3, Parameters.NoProofMod() and Parameters.NoProofFac() always return
// false — every keygen/keysign party runs with both proofs validated. A future
// tss-lib release that flipped the bypass back on (and made either method
// return true) would silently re-enable running the protocol without those
// proofs — a real safety hole. This test catches that regression at unit-test
// time.
//
// If this test ever fails, do NOT just update the assertion — investigate why
// tss-lib re-introduced the bypass and whether it is intentional.
func TestTssLib_NoProofGuardsMandatory_InV3(t *testing.T) {
	// Construct a minimal Parameters; field values don't matter to the
	// NoProofMod / NoProofFac accessors (they're hardcoded false in v3).
	partyIDs := []*btss.PartyID{
		btss.NewPartyID("0", "p0", big.NewInt(1)),
		btss.NewPartyID("1", "p1", big.NewInt(2)),
		btss.NewPartyID("2", "p2", big.NewInt(3)),
	}
	peerCtx := btss.NewPeerContext(partyIDs)
	params := btss.NewParameters(tss.S256(), peerCtx, partyIDs[0], 3, 1)

	if params.NoProofMod() {
		t.Fatal("Parameters.NoProofMod() = true — tss-lib has re-introduced the modproof bypass. The protocol would now run without Paillier-Blum modproof validation. Investigate whether the bypass is intentional and update this test deliberately.")
	}
	if params.NoProofFac() {
		t.Fatal("Parameters.NoProofFac() = true — tss-lib has re-introduced the facproof bypass. The protocol would now run without Π-fac facproof validation. Investigate whether the bypass is intentional and update this test deliberately.")
	}
}
