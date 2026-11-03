package tss

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// Invalid confirmations must not veto a three-of-four quorum. Use the real
// round exchange so future failures, payload requirements and timeout blame
// are exercised together, rather than only checking the vote map.
func TestFrostKeygenDiscardsMalformedVerification(t *testing.T) {
	ensureBech32Prefix()
	for _, tc := range []struct {
		name                 string
		future               bool
		validBeforeMalformed bool
		payload              string
		validVoters          []int
		incompletePayload    bool
		wantSuccess          bool
		wantMissing          []int
		wantBlame            []int
	}{
		{name: "future hash with sufficient quorum", future: true, validVoters: []int{1, 2}, wantSuccess: true},
		{name: "current hash with sufficient quorum", validVoters: []int{1, 2}, wantSuccess: true},
		{name: "stale malformed vote", payload: `{"round":"frost_dkg_round1","transcript_hash":"invalid"}`, validVoters: []int{1, 2}, wantSuccess: true},
		{name: "invalid JSON", payload: `{`, validVoters: []int{1, 2}, wantSuccess: true},
		{name: "unknown round", payload: `{"round":"unknown","transcript_hash":"invalid"}`, validVoters: []int{1, 2}, wantSuccess: true},
		{name: "future malformed then valid sender counts", future: true, validVoters: []int{1, 3}, wantSuccess: true},
		{name: "current malformed then valid sender counts", validVoters: []int{1, 3}, wantSuccess: true},
		{name: "future valid then malformed sender counts", future: true, validBeforeMalformed: true, validVoters: []int{3, 1}, wantSuccess: true},
		{name: "current valid then malformed sender counts", validBeforeMalformed: true, validVoters: []int{3, 1}, wantSuccess: true},
		{name: "insufficient quorum blames missing voters", validVoters: []int{1}, wantMissing: []int{2, 3}, wantBlame: []int{2, 3}},
		{name: "later valid sender is not missing at timeout", validVoters: []int{3}, wantMissing: []int{1, 2}, wantBlame: []int{1, 2}},
		{name: "incomplete transcript cannot blame missing votes", validVoters: []int{1}, incompletePayload: true, wantBlame: []int{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := testPubKeys[:4]
			activeSet, indexes, peers := testFrostKeygenParticipants(t, members)
			inbox := newFrostKeygenInbox()
			coord := newTestFrostKeygenCoordinator(members[0], "invalid-vote", inbox, time.Second)
			initTestFrostKeygenRoundState(t, coord, activeSet, indexes)
			payloads := make([]FrostDKGRound3Payload, len(members))
			entries := make([]frostKeygenTranscriptEntry, len(members))
			for i, member := range members {
				payloads[i] = FrostDKGRound3Payload{
					SenderPubKey: member, ParticipantIndex: indexes[normalizePubKey(member)],
					Address: "48addr", GroupKey: strings.Repeat("ab", 32), ObservedMoneroHeight: 456,
				}
				hash, err := FrostTranscriptDKGRound3Hash(coord.msgID, payloads[i])
				if err != nil {
					t.Fatal(err)
				}
				entries[i] = frostKeygenTranscriptEntry{participantIndex: payloads[i].ParticipantIndex, payloadHash: hash}
				if i != 0 && !(tc.incompletePayload && i == 2) {
					enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, coord.msgID, member, payloads[i])
				}
			}
			hash := testFrostKeygenTranscriptHash(t, coord.msgID, frostDKGRound3, entries...)
			invalidPayload := tc.payload
			if invalidPayload == "" {
				invalidPayload = `{"round":"frost_dkg_round3","transcript_hash":"invalid"}`
			}
			if tc.validBeforeMalformed {
				// Keep this sender's valid vote before its malformed message, but
				// the quorum-completing vote after it so the discard is exercised.
				enqueueVerifyMessage(t, inbox, coord.msgID, members[3], frostDKGRound3, hash)
			}
			enqueueWrappedPayload(t, inbox, messages.TSSFrostVerifyMsg, coord.msgID, members[3], []byte(invalidPayload))
			if tc.future {
				// Route the queued future votes before entering round 3; the old
				// code poisoned round 3 at this exact transition.
				for len(inbox.verify) != 0 {
					if failure := coord.routeVerificationMessage(<-inbox.verify, frostDKGRound2); failure != nil {
						t.Fatalf("future vote aborted the current round: %+v", failure)
					}
				}
			}
			for _, voter := range tc.validVoters {
				if tc.validBeforeMalformed && voter == 3 {
					continue
				}
				enqueueVerifyMessage(t, inbox, coord.msgID, members[voter], frostDKGRound3, hash)
			}
			address, groupKey, birthday, failure := coord.exchangeRound3(activeSet, indexes, peers, payloads[0])
			if tc.wantSuccess {
				if failure != nil || address != payloads[0].Address || groupKey != payloads[0].GroupKey || birthday != 446 {
					t.Fatalf("three-of-four confirmation failed: address=%q key=%q birthday=%d failure=%+v", address, groupKey, birthday, failure)
				}
			} else {
				if failure == nil || !strings.Contains(failure.Blame.FailReason, "timeout") || failure.Blame.Round != frostDKGRound3 {
					t.Fatalf("expected round 3 timeout, got %+v", failure)
				}
				wantBlame := make([]string, 0, len(tc.wantBlame))
				for _, i := range tc.wantBlame {
					wantBlame = append(wantBlame, members[i])
				}
				gotBlame := make([]string, 0, len(failure.Blame.BlameNodes))
				for _, node := range failure.Blame.BlameNodes {
					gotBlame = append(gotBlame, node.Pubkey)
				}
				slices.Sort(wantBlame)
				slices.Sort(gotBlame)
				if !slices.Equal(gotBlame, wantBlame) {
					t.Fatalf("timeout blame = %v, want %v", gotBlame, wantBlame)
				}
			}
			verifier, err := coord.roundState.verifier(frostDKGRound3)
			if err != nil {
				t.Fatal(err)
			}
			if (verifier.transcriptHash == "") != tc.incompletePayload {
				t.Fatalf("unexpected local transcript completeness: %q", verifier.transcriptHash)
			}
			if len(verifier.equivocatingAttesters) != 0 {
				t.Fatal("discarding malformed data must not disqualify its sender")
			}
			if got, want := verifier.votesByAttester[normalizePubKey(members[3])], slices.Contains(tc.validVoters, 3); (got == hash) != want || (!want && got != "") {
				t.Fatalf("malformed sender's retained vote = %q, valid vote sent = %t", got, want)
			}
			if !tc.wantSuccess {
				wantMissing := make([]string, 0, len(tc.wantMissing))
				for _, i := range tc.wantMissing {
					wantMissing = append(wantMissing, members[i])
				}
				slices.Sort(wantMissing)
				if got := verifier.verificationGaps().missingPubKeys; !slices.Equal(got, wantMissing) {
					t.Fatalf("missing verification votes = %v, want %v", got, wantMissing)
				}
			}
			if len(inbox.verify) != 0 {
				t.Fatal("test did not consume all verification traffic")
			}
		})
	}
}

// One warning identifies the ceremony without flooding warning logs up to the
// transport allowance. Every later discard still includes diagnostics at Debug.
func TestFrostKeygenVerificationDiscardLoggingOncePerCoordinator(t *testing.T) {
	ensureBech32Prefix()
	activeSet, indexes, _ := testFrostKeygenParticipants(t, testPubKeys[:2])
	for ceremony := 0; ceremony < 2; ceremony++ {
		var logs bytes.Buffer
		inbox := newFrostKeygenInbox()
		coord := newTestFrostKeygenCoordinator(testPubKeys[0], "discard-logging", inbox, time.Second)
		coord.logger = zerolog.New(&logs).Level(zerolog.DebugLevel)
		initTestFrostKeygenRoundState(t, coord, activeSet, indexes)
		enqueueWrappedPayload(t, inbox, messages.TSSFrostVerifyMsg, coord.msgID, testPubKeys[1], []byte(`{`))
		enqueueVerifyMessage(t, inbox, coord.msgID, testPubKeys[2], frostDKGRound3, strings.Repeat("a", 64))
		enqueueVerifyMessage(t, inbox, coord.msgID, testPubKeys[1], frostDKGRound3, strings.Repeat("a", 64))
		decoder := json.NewDecoder(&logs)
		for i, wantLevel := range []string{"warn", "debug", "debug"} {
			msg := <-inbox.verify
			if i == 2 {
				msg.PeerID = peer.ID("invalid")
			}
			currentRound := frostDKGRound2
			if i != 0 {
				currentRound = frostDKGRound3
			}
			if failure := coord.routeVerificationMessage(msg, currentRound); failure != nil {
				t.Fatalf("discard aborted ceremony: %+v", failure)
			}
			var event map[string]string
			if err := decoder.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event["level"] != wantLevel || event["peer_id"] != msg.PeerID.String() || event["round"] != currentRound || event["message"] == "" || (event["error"] == "" && event["reason"] == "") {
				t.Fatalf("ceremony %d discard %d: expected %s with diagnostics, got %v", ceremony, i, wantLevel, event)
			}
		}
	}
}

// Peer binding is still mandatory, but an unresolvable or non-member attester
// must not abort the ceremony or populate any round's confirmation state.
func TestFrostKeygenDiscardsUnauthorizedVerification(t *testing.T) {
	ensureBech32Prefix()
	for _, currentRound := range []string{frostDKGRound2, frostDKGRound3} {
		for _, invalidPeer := range []bool{false, true} {
			activeSet, indexes, _ := testFrostKeygenParticipants(t, testPubKeys[:2])
			inbox := newFrostKeygenInbox()
			coord := newTestFrostKeygenCoordinator(testPubKeys[0], "unauthorized-vote", inbox, time.Second)
			initTestFrostKeygenRoundState(t, coord, activeSet, indexes)
			enqueueVerifyMessage(t, inbox, coord.msgID, testPubKeys[2], frostDKGRound3, strings.Repeat("a", 64))
			msg := <-inbox.verify
			if invalidPeer {
				msg.PeerID = peer.ID("invalid")
			}
			if failure := coord.routeVerificationMessage(msg, currentRound); failure != nil {
				t.Fatalf("unauthorized verification aborted %s (invalid peer=%t): %+v", currentRound, invalidPeer, failure)
			}
			for round, verifier := range coord.roundState.verifiers {
				if len(verifier.votesByAttester) != 0 || len(verifier.equivocatingAttesters) != 0 {
					t.Fatalf("unauthorized verification changed %s", round)
				}
			}
		}
	}
}
