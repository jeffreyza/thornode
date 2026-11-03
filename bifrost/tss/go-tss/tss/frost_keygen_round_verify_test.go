package tss

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func TestFrostKeygenTranscriptHashIsCanonicalAndSessionBound(t *testing.T) {
	entries := []frostKeygenTranscriptEntry{
		{participantIndex: 2, payloadHash: strings.Repeat("b", 64)},
		{participantIndex: 1, payloadHash: strings.Repeat("a", 64)},
	}
	hash, err := frostKeygenTranscriptHash("msg-id", frostDKGRound1, entries)
	if err != nil {
		t.Fatalf("hash transcript: %v", err)
	}
	reordered, err := frostKeygenTranscriptHash("msg-id", frostDKGRound1, []frostKeygenTranscriptEntry{entries[1], entries[0]})
	if err != nil {
		t.Fatalf("hash reordered transcript: %v", err)
	}
	if reordered != hash {
		t.Fatalf("canonical ordering changed digest: %q != %q", reordered, hash)
	}

	mutations := []struct {
		msgID   string
		round   string
		entries []frostKeygenTranscriptEntry
	}{
		{msgID: "other-msg", round: frostDKGRound1, entries: entries},
		{msgID: "msg-id", round: frostDKGRound2, entries: entries},
		{msgID: "msg-id", round: frostDKGRound1, entries: []frostKeygenTranscriptEntry{
			{participantIndex: 1, payloadHash: strings.Repeat("a", 64)},
			{participantIndex: 2, payloadHash: strings.Repeat("c", 64)},
		}},
	}
	for _, mutation := range mutations {
		got, err := frostKeygenTranscriptHash(mutation.msgID, mutation.round, mutation.entries)
		if err != nil {
			t.Fatalf("hash mutated transcript: %v", err)
		}
		if got == hash {
			t.Fatalf("transcript mutation did not change digest: %+v", mutation)
		}
	}

	invalid := [][]frostKeygenTranscriptEntry{
		{{participantIndex: 0, payloadHash: strings.Repeat("a", 64)}},
		{
			{participantIndex: 1, payloadHash: strings.Repeat("a", 64)},
			{participantIndex: 1, payloadHash: strings.Repeat("b", 64)},
		},
		{{participantIndex: 1, payloadHash: "invalid"}},
	}
	for _, badEntries := range invalid {
		if _, err := frostKeygenTranscriptHash("msg-id", frostDKGRound1, badEntries); err == nil {
			t.Fatalf("invalid transcript entries were accepted: %+v", badEntries)
		}
	}
}

func TestDecodeFrostKeygenTranscriptVoteRejectsLegacyAndMalformedPayloads(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	validPayload, err := json.Marshal(frostKeygenTranscriptVote{
		Round:          frostDKGRound1,
		TranscriptHash: strings.ToUpper(validHash),
	})
	if err != nil {
		t.Fatalf("marshal valid vote: %v", err)
	}
	vote, err := decodeFrostKeygenTranscriptVote(validPayload)
	if err != nil {
		t.Fatalf("decode valid vote: %v", err)
	}
	if vote.Round != frostDKGRound1 || vote.TranscriptHash != validHash {
		t.Fatalf("unexpected normalized vote: %+v", vote)
	}

	invalidPayloads := []any{
		FrostVerifyPayload{Round: frostDKGRound1, PayloadHash: validHash},
		map[string]any{"round": frostDKGRound1, "verifications": []any{}},
		frostKeygenTranscriptVote{TranscriptHash: validHash},
		frostKeygenTranscriptVote{Round: "future", TranscriptHash: validHash},
		frostKeygenTranscriptVote{Round: frostDKGRound1, TranscriptHash: "invalid"},
	}
	for _, invalid := range invalidPayloads {
		payload, err := json.Marshal(invalid)
		if err != nil {
			t.Fatalf("marshal invalid vote: %v", err)
		}
		if _, err := decodeFrostKeygenTranscriptVote(payload); err == nil {
			t.Fatalf("invalid keygen transcript vote was accepted: %s", payload)
		}
	}
}

func TestFrostKeygenRoundDigestCollapses95PartyVerificationMatrix(t *testing.T) {
	const partySize = 95
	activeSet := &activeSignerSet{originalByNormalized: make(map[string]string, partySize)}
	participantIndexMap := make(map[string]uint32, partySize)
	members := make([]string, 0, partySize)
	for i := range partySize {
		member := fmt.Sprintf("participant-%03d", i+1)
		members = append(members, member)
		activeSet.originalByNormalized[normalizePubKey(member)] = member
		participantIndexMap[normalizePubKey(member)] = uint32(i + 1)
	}
	verifier, err := newFrostKeygenRoundVerifier(frostDKGRound1, activeSet, participantIndexMap)
	if err != nil {
		t.Fatalf("new round verifier: %v", err)
	}
	coord := newTestFrostKeygenCoordinator(members[0], "msg-id", newFrostKeygenInbox(), 0)
	peers := make([]peer.ID, partySize-1)
	for i := range peers {
		peers[i] = peer.ID(fmt.Sprintf("peer-%03d", i+1))
	}

	for i, member := range members {
		isNew, startedConfirmation, response := coord.recordAndConfirmTranscriptPayload(
			verifier,
			member,
			uint32(i+1),
			fmt.Sprintf("%064x", i+1),
			peers,
		)
		if response != nil || !isNew {
			t.Fatalf("record payload %d: new=%t response=%+v", i, isNew, response)
		}
		if startedConfirmation != (i == partySize-1) {
			t.Fatalf("payload %d confirmation transition=%t", i, startedConfirmation)
		}
	}

	if got := len(coord.p2pCommunication.BroadcastMsgChan); got != 1 {
		t.Fatalf("verification envelopes = %d, want 1", got)
	}
	queued := <-coord.p2pCommunication.BroadcastMsgChan
	if queued.WrappedMessage.MessageType != messages.TSSFrostVerifyMsg {
		t.Fatalf("broadcast type = %q, want verification", queued.WrappedMessage.MessageType)
	}
	if len(queued.WrappedMessage.Payload) >= p2p.FrostKeygenControlPayload {
		t.Fatalf("transcript vote size = %d, exceeds control cap", len(queued.WrappedMessage.Payload))
	}
	if strings.Contains(string(queued.WrappedMessage.Payload), "verifications") || strings.Contains(string(queued.WrappedMessage.Payload), "target_sender") {
		t.Fatalf("transcript vote retained per-target matrix: %s", queued.WrappedMessage.Payload)
	}
	vote, err := decodeFrostKeygenTranscriptVote(queued.WrappedMessage.Payload)
	if err != nil || vote.TranscriptHash != verifier.transcriptHash {
		t.Fatalf("decode broadcast vote: vote=%+v err=%v", vote, err)
	}

	oldSemanticDeliveries := 3 * partySize * partySize * (partySize - 1)
	newSemanticDeliveries := 3 * partySize * (partySize - 1)
	if oldSemanticDeliveries/newSemanticDeliveries != partySize {
		t.Fatalf("verification reduction factor = %d, want %d", oldSemanticDeliveries/newSemanticDeliveries, partySize)
	}
	if oldSemanticDeliveries != 2_545_050 || newSemanticDeliveries != 26_790 {
		t.Fatalf("unexpected 95-party cost: old=%d new=%d", oldSemanticDeliveries, newSemanticDeliveries)
	}
}
