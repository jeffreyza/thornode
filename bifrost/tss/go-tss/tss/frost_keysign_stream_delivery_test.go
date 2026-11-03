package tss

import (
	"encoding/json"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// A queued frame's stream closes without consumer progress. Delivery precedes
// closure in production; the helper waits for EOF before exercising discard.
func TestFrostKeysignQueuedVerificationStreamClosesWithoutConsumerProgress(t *testing.T) {
	receive := newRestrictedFrostTestReceiver(t)
	coord := newTestFrostSignCoordinator(testPubKeys[0], "different-session", newFrostSignInbox(), time.Second)
	activeSet, participantIndexMap, _ := testFrostSignParticipants(t, testPubKeys[:2])
	initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
	// A valid future echo would be retained if the session gate were removed;
	// malformed fixture bytes would mask that regression behind decoder rejection.
	payload, err := json.Marshal(FrostVerifyBatchPayload{Verifications: []FrostVerifyPayload{{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		PayloadHash:            testHashOf('a'),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	msg := receive(t, messages.TSSFrostVerifyMsg, payload)
	expectedPeer, err := conversion.GetPeerIDFromPubKey(testPubKeys[1])
	if err != nil {
		t.Fatal(err)
	}
	if msg.PeerID != expectedPeer {
		t.Fatalf("delivered peer=%s, want active peer=%s", msg.PeerID, expectedPeer)
	}
	wrapped, ok := parseWrappedMessage(msg.Payload)
	if !ok || wrapped.MsgID == coord.msgID {
		t.Fatal("discard fixture must carry a different session ID")
	}
	if response := coord.routeVerificationMessage(msg, frostRoundPreprocess); response != nil {
		t.Fatalf("mismatched session became terminal: %+v", response)
	}
	if len(coord.verificationEntriesByPeer) != 0 || len(coord.roundState.deferredVerifications) != 0 {
		t.Fatal("mismatched session changed verification state")
	}
}
