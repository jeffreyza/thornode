package tss

import (
	"strconv"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// Narrowing does not purge frames a removed peer queued earlier; p2p still
// delivers them. The coordinator's active-set check must drop them, including
// frames from a correctly bound eligible signer that did not join the party.
func TestFrostExchangePreprocessIgnoresRemovedEligiblePeer(t *testing.T) {
	ensureBech32Prefix()

	eligible := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	active := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(eligible)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(active, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(active)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	payloadFor := func(pubKey, hex string) FrostPreprocessPayload {
		return FrostPreprocessPayload{
			SenderPubKey:     pubKey,
			ParticipantIndex: participantIndexMap[normalizePubKey(pubKey)],
			PreprocessHex:    hex,
		}
	}
	hashOf := func(payload FrostPreprocessPayload) string {
		hash, hashErr := FrostTranscriptPreprocessHash("msg-id", payload)
		if hashErr != nil {
			t.Fatalf("hash: %v", hashErr)
		}
		return hash
	}
	localPayload := payloadFor(testPubKeys[0], "aa")
	remotePayload := payloadFor(testPubKeys[1], "bb")
	removedPayload := payloadFor(testPubKeys[2], "cc")

	msgChan := newFrostSignInbox()
	// Frames the removed peer queued before narrowing arrive first.
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[2], removedPayload)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[2], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[2],
		Round:                  messages.FROSTKEYSIGNPREPROCESS,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: localPayload.ParticipantIndex,
		PayloadHash:            "not-the-local-hash",
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[1], remotePayload)
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  messages.FROSTKEYSIGNPREPROCESS,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: remotePayload.ParticipantIndex,
		PayloadHash:            hashOf(remotePayload),
	})
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostVerifyMsg, "msg-id", testPubKeys[1], FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  messages.FROSTKEYSIGNPREPROCESS,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: localPayload.ParticipantIndex,
		PayloadHash:            hashOf(localPayload),
	})

	coord := newTestFrostSignCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
	values, failResp := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, localPayload)
	if failResp != nil {
		t.Fatalf("a removed peer's queued frames failed the round: %+v", failResp)
	}
	if len(values) != 2 {
		t.Fatalf("expected the two active payloads, got %d", len(values))
	}
	if _, ok := values[strconv.FormatUint(uint64(removedPayload.ParticipantIndex), 10)]; ok {
		t.Fatal("the removed peer's preprocess payload was accepted")
	}
}
