package tss

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// A node that did not join the signing party waits for the result on the result
// queue that holds removed peers' stale frames. Frames from peers outside the
// active set must be dropped before parsing: the post-parse path would log
// "non-active signer" for this frame, so the log must stay silent about it.
func TestFrostWaitForResultDropsNonActivePeerBeforeParsing(t *testing.T) {
	ensureBech32Prefix()

	var logs bytes.Buffer
	msgChan := newFrostSignInbox()
	coord := &FrostSignCoordinator{
		localPubKey:      testPubKeys[0],
		msgID:            "msg-id",
		expectedPlanHash: testExpectedPlanHash,
		inbox:            msgChan,
		timeout:          time.Second,
		logger:           zerolog.New(&logs),
	}

	result := mustSuccessResult(t, "msg-id", testExpectedPlanHash, testHashOf('c'), "deadbeef", 10)
	// A correctly bound peer outside the active set, queued first.
	enqueueResultMessage(t, msgChan, "msg-id", testPubKeys[3], testPubKeys[3], FrostResultPayload{
		SenderPubKey: testPubKeys[3],
		Response:     result,
	})
	enqueueResultMessage(t, msgChan, "msg-id", testPubKeys[1], testPubKeys[1], FrostResultPayload{
		SenderPubKey: testPubKeys[1],
		Response:     result,
	})
	enqueueResultMessage(t, msgChan, "msg-id", testPubKeys[2], testPubKeys[2], FrostResultPayload{
		SenderPubKey: testPubKeys[2],
		Response:     result,
	})

	resp, err := coord.WaitForResult([]string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}, 2, time.Second)
	if err != nil {
		t.Fatalf("wait for result: %v", err)
	}
	if resp.Status != tcommon.Success || resp.TxHash != testHashOf('c') {
		t.Fatalf("unexpected result: %+v", resp)
	}
	if strings.Contains(logs.String(), "non-active signer") {
		t.Fatalf("a non-active peer's result frame was parsed before being dropped: %s", logs.String())
	}
}

// The preprocess round drops a removed peer's frame before parsing it: a
// malformed payload from that peer would otherwise be parsed and logged.
func TestFrostExchangePreprocessDropsRemovedPeerBeforeParsing(t *testing.T) {
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

	msgChan := newFrostSignInbox()
	// Wrapped correctly, but the inner payload is not a preprocess object.
	enqueueKeySignMessage(t, msgChan, messages.TSSFrostPreprocessMsg, "msg-id", testPubKeys[2], "not-a-preprocess-payload")
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

	var logs bytes.Buffer
	coord := newTestFrostSignCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
	coord.logger = zerolog.New(&logs)
	if _, failResp := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, localPayload); failResp != nil {
		t.Fatalf("round failed: %+v", failResp)
	}
	if strings.Contains(logs.String(), "malformed preprocess") {
		t.Fatalf("a removed peer's frame was parsed before being dropped: %s", logs.String())
	}
}
