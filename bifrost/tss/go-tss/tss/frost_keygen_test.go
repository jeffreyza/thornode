package tss

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Exercise the server entry point, not only the coordinator, so a reintroduced
// legacy version dispatch cannot silently prevent FROST from forming a party.
func TestFrostKeygenJoinsWithoutThorchainVersion(t *testing.T) {
	ensureBech32Prefix()
	encodedKey, err := base64.StdEncoding.DecodeString(testPriKeyArr[0])
	if err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(string(encodedKey))
	if err != nil {
		t.Fatal(err)
	}
	comm, err := p2p.NewCommunication(&p2p.Config{Port: 0, RendezvousString: "frost-version-test"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = comm.Start(key); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := comm.Stop(); err != nil {
			t.Error(err)
		}
	}()
	party := p2p.NewPartyCoordinator(comm.GetHost(), time.Second)
	defer party.Stop()
	server := &TssServer{p2pCommunication: comm, partyCoordinator: party, localNodePubKey: testPubKeys[0]}
	sidecar := &mockFrostSidecarKeygen{
		round1Resp:   FrostSidecarDKGRound1Resp{Commitments: "commitment"},
		round2Resp:   FrostSidecarDKGRound2Resp{EncryptedShares: map[string]string{}},
		round3Resp:   FrostSidecarDKGRound3Resp{Address: "48xmrsinglesigner", GroupKey: strings.Repeat("cd", 32)},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
		chainHeight:  123,
	}
	resp, err := server.FrostKeygen(FrostKeygenRequest{
		VaultPubKey: "thorpub-single", SignerPubKeys: []string{testPubKeys[0]},
		BlockHeight: 100,
	}, sidecar)
	if err != nil || resp.Status != tcommon.Success || len(sidecar.round1Reqs) != 1 {
		t.Fatalf("FROST keygen without version failed: err=%v response=%+v", err, resp)
	}
}

func TestFrostKeygenExecuteFailsOnSidecarDKGError(t *testing.T) {
	members := []string{testPubKeys[0]}
	_, _, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatal(err)
	}
	active, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := &mockFrostSidecarKeygen{round1Err: errors.New("sidecar unavailable")}
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "sidecar-error", newFrostKeygenInbox(), time.Second)
	coord.sidecar = sidecar
	resp := coord.Execute(FrostKeygenRequest{VaultPubKey: "vault", SignerPubKeys: members}, active, nil)
	if resp.Status != tcommon.Fail || len(sidecar.round1Reqs) != 1 || len(sidecar.round2Reqs) != 0 || len(sidecar.finalizeReqs) != 0 {
		t.Fatalf("sidecar DKG failure was not terminal for local execution: %+v", resp)
	}
}

type mockFrostSidecarKeygen struct {
	round1Reqs   []FrostSidecarDKGRound1Req
	round2Reqs   []FrostSidecarDKGRound2Req
	round3Reqs   []FrostSidecarDKGRound3Req
	finalizeReqs []FrostSidecarDKGFinalizeReq

	round1Resp   FrostSidecarDKGRound1Resp
	round2Resp   FrostSidecarDKGRound2Resp
	round3Resp   FrostSidecarDKGRound3Resp
	finalizeResp FrostSidecarDKGFinalizeResp

	round1Err   error
	round2Err   error
	round3Err   error
	finalizeErr error
	chainHeight int64
	chainErr    error
}

func (m *mockFrostSidecarKeygen) FrostDKGRound1(req FrostSidecarDKGRound1Req) (FrostSidecarDKGRound1Resp, error) {
	m.round1Reqs = append(m.round1Reqs, req)
	if m.round1Err != nil {
		return FrostSidecarDKGRound1Resp{}, m.round1Err
	}
	return m.round1Resp, nil
}

func (m *mockFrostSidecarKeygen) FrostDKGRound2(req FrostSidecarDKGRound2Req) (FrostSidecarDKGRound2Resp, error) {
	m.round2Reqs = append(m.round2Reqs, req)
	if m.round2Err != nil {
		return FrostSidecarDKGRound2Resp{}, m.round2Err
	}
	return m.round2Resp, nil
}

func (m *mockFrostSidecarKeygen) FrostDKGRound3(req FrostSidecarDKGRound3Req) (FrostSidecarDKGRound3Resp, error) {
	m.round3Reqs = append(m.round3Reqs, req)
	if m.round3Err != nil {
		return FrostSidecarDKGRound3Resp{}, m.round3Err
	}
	return m.round3Resp, nil
}

func (m *mockFrostSidecarKeygen) FrostDKGFinalize(req FrostSidecarDKGFinalizeReq) (FrostSidecarDKGFinalizeResp, error) {
	m.finalizeReqs = append(m.finalizeReqs, req)
	if m.finalizeErr != nil {
		return FrostSidecarDKGFinalizeResp{}, m.finalizeErr
	}
	return m.finalizeResp, nil
}

func (m *mockFrostSidecarKeygen) GetChainHeight() (int64, error) {
	if m.chainErr != nil {
		return 0, m.chainErr
	}
	return m.chainHeight, nil
}

func newTestFrostKeygenCoordinator(localPubKey, msgID string, inbox *frostKeygenInbox, timeout time.Duration) *FrostKeygenCoordinator {
	return &FrostKeygenCoordinator{
		p2pCommunication: &p2p.Communication{BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 64)},
		localPubKey:      localPubKey,
		msgID:            msgID,
		inbox:            inbox,
		timeout:          timeout,
	}
}

func initTestFrostKeygenRoundState(
	t *testing.T,
	coord *FrostKeygenCoordinator,
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
) {
	t.Helper()
	state, err := newFrostKeygenRoundState(activeSet, participantIndexMap)
	if err != nil {
		t.Fatalf("initialize frost keygen round state: %v", err)
	}
	coord.roundState = state
}

func enqueueWrappedPayload(
	t *testing.T,
	inbox *frostKeygenInbox,
	msgType messages.THORChainTSSMessageType,
	msgID string,
	peerPubKey string,
	payload []byte,
) {
	t.Helper()
	peerID, err := conversion.GetPeerIDFromPubKey(peerPubKey)
	if err != nil {
		t.Fatalf("peer id from pubkey: %v", err)
	}
	wrapped, err := json.Marshal(messages.WrappedMessage{
		MessageType: msgType,
		MsgID:       msgID,
		Payload:     payload,
	})
	if err != nil {
		t.Fatalf("marshal wrapped message: %v", err)
	}
	for _, subscription := range inbox.subscriptions() {
		if subscription.msgType == msgType {
			subscription.messageChan <- &p2p.Message{PeerID: peerID, Payload: wrapped}
			return
		}
	}
	t.Fatalf("unsupported FROST keygen test message type %q", msgType)
}

// TestFrostKeygenInboxSubscriptionsCoverEveryTopic protects the lifecycle
// invariant: each keygen topic owns one non-nil phase inbox, with no omissions
// or channel reuse hidden by the shared subscription table.
func TestFrostKeygenInboxSubscriptionsCoverEveryTopic(t *testing.T) {
	inbox := newFrostKeygenInbox()
	want := map[messages.THORChainTSSMessageType]chan *p2p.Message{
		messages.TSSFrostDKGRound1Msg: inbox.round1,
		messages.TSSFrostDKGRound2Msg: inbox.round2,
		messages.TSSFrostDKGRound3Msg: inbox.round3,
		messages.TSSFrostVerifyMsg:    inbox.verify,
		messages.TSSFrostDKGResultMsg: inbox.result,
	}
	seenChannels := make(map[chan *p2p.Message]struct{}, len(want))
	for _, subscription := range inbox.subscriptions() {
		wantChannel, ok := want[subscription.msgType]
		if !ok {
			t.Fatalf("unexpected or duplicate FROST keygen topic %q", subscription.msgType)
		}
		if subscription.messageChan == nil {
			t.Fatalf("FROST keygen topic %q has a nil inbox", subscription.msgType)
		}
		if subscription.messageChan != wantChannel {
			t.Fatalf("FROST keygen topic %q is bound to the wrong inbox", subscription.msgType)
		}
		wantPayloadLimit := p2p.FrostKeygenControlPayload
		if subscription.msgType == messages.TSSFrostDKGRound2Msg {
			wantPayloadLimit = p2p.FrostMaxSessionPayload
		}
		if subscription.maxPayloadBytes != wantPayloadLimit {
			t.Fatalf("FROST keygen topic %q payload limit = %d, want %d", subscription.msgType, subscription.maxPayloadBytes, wantPayloadLimit)
		}
		if _, duplicate := seenChannels[subscription.messageChan]; duplicate {
			t.Fatalf("FROST keygen topic %q reuses another topic's inbox", subscription.msgType)
		}
		seenChannels[subscription.messageChan] = struct{}{}
		delete(want, subscription.msgType)
	}
	if len(want) != 0 {
		t.Fatalf("FROST keygen subscription table is missing %d topics", len(want))
	}
}

func enqueueRoundMessage(
	t *testing.T,
	inbox *frostKeygenInbox,
	msgType messages.THORChainTSSMessageType,
	msgID string,
	peerPubKey string,
	payload interface{},
) {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	enqueueWrappedPayload(t, inbox, msgType, msgID, peerPubKey, payloadBytes)
}

func enqueueVerifyMessage(
	t *testing.T,
	inbox *frostKeygenInbox,
	msgID string,
	peerPubKey string,
	round string,
	transcriptHash string,
) {
	t.Helper()
	payloadBytes, err := json.Marshal(frostKeygenTranscriptVote{
		Round:          round,
		TranscriptHash: transcriptHash,
	})
	if err != nil {
		t.Fatalf("marshal verification payload: %v", err)
	}
	enqueueWrappedPayload(t, inbox, messages.TSSFrostVerifyMsg, msgID, peerPubKey, payloadBytes)
}

func testFrostKeygenTranscriptHash(
	t *testing.T,
	msgID string,
	round string,
	entries ...frostKeygenTranscriptEntry,
) string {
	t.Helper()
	transcriptHash, err := frostKeygenTranscriptHash(msgID, round, entries)
	if err != nil {
		t.Fatalf("hash frost keygen transcript: %v", err)
	}
	return transcriptHash
}

func testFrostKeygenParticipants(
	t *testing.T,
	members []string,
) (*activeSignerSet, map[string]uint32, []peer.ID) {
	t.Helper()
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}
	return activeSet, participantIndexMap, peerIDs
}

func TestNewFrostKeygenCoordinatorClampsTimeoutToFloor(t *testing.T) {
	// Any non-zero timeout shorter than the floor must be clamped up.
	// This locks in the guarantee that a misconfigured (or zero-value)
	// FrostKeygenTimeout cannot starve the 3-round p2p DKG exchange.
	cases := []struct {
		name    string
		in      time.Duration
		wantMin time.Duration
	}{
		{name: "zero", in: 0, wantMin: minFrostKeygenTimeout},
		{name: "very short", in: 1 * time.Second, wantMin: minFrostKeygenTimeout},
		{name: "equal to floor", in: minFrostKeygenTimeout, wantMin: minFrostKeygenTimeout},
		{name: "above floor", in: 5 * time.Minute, wantMin: 5 * time.Minute},
	}
	msgChan := newFrostKeygenInbox()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coord := NewFrostKeygenCoordinator(nil, nil, "local", "msg-id", msgChan, tc.in)
			if coord.timeout < tc.wantMin {
				t.Fatalf("timeout not clamped: got %v, want >= %v", coord.timeout, tc.wantMin)
			}
			if tc.in >= minFrostKeygenTimeout && coord.timeout != tc.in {
				t.Fatalf("timeout above floor was modified: in=%v coord.timeout=%v", tc.in, coord.timeout)
			}
		})
	}
}

func TestFrostKeygenParticipantIndexDeterministic(t *testing.T) {
	membersA := []string{testPubKeys[2], testPubKeys[0], testPubKeys[1]}
	membersB := []string{testPubKeys[1], testPubKeys[2], testPubKeys[0]}

	_, idxA, _, err := buildParticipantIndex(membersA)
	if err != nil {
		t.Fatalf("build participant index A: %v", err)
	}
	_, idxB, _, err := buildParticipantIndex(membersB)
	if err != nil {
		t.Fatalf("build participant index B: %v", err)
	}

	for _, pubKey := range []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]} {
		norm := normalizePubKey(pubKey)
		if idxA[norm] != idxB[norm] {
			t.Fatalf("participant index mismatch for %s: %d != %d", pubKey, idxA[norm], idxB[norm])
		}
	}
}

func TestFrostKeygenRejectsIncompleteParticipantSet(t *testing.T) {
	_, _, signerByNormalized, err := buildParticipantIndex([]string{testPubKeys[0], testPubKeys[1], testPubKeys[2]})
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet([]string{testPubKeys[0], testPubKeys[1]}, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}

	missing := missingSignersForFullParticipation(activeSet, signerByNormalized)
	if len(missing) != 1 || missing[0] != testPubKeys[2] {
		t.Fatalf("expected missing signer %s, got %+v", testPubKeys[2], missing)
	}
}

func TestFrostKeygenExecuteRejectsPartialActiveSet(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	_, _, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet([]string{testPubKeys[0], testPubKeys[1]}, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}

	sidecar := &mockFrostSidecarKeygen{}
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), time.Second)
	coord.sidecar = sidecar
	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "vault",
		SignerPubKeys: members,
	}, activeSet, nil)

	if resp.Status != tcommon.Fail {
		t.Fatalf("expected failure for partial active set, got %+v", resp)
	}
	if !strings.Contains(resp.Blame.FailReason, "requires all signers online") {
		t.Fatalf("unexpected failure reason: %q", resp.Blame.FailReason)
	}
	if len(sidecar.round1Reqs) != 0 {
		t.Fatalf("sidecar should not be called for partial active set")
	}
}

func TestFrostKeygenJoinThresholdRequiresFullParticipation(t *testing.T) {
	if got := frostKeygenJoinThreshold(4); got != 3 {
		t.Fatalf("expected full participation threshold for 4 signers to be 3, got %d", got)
	}
	if got := frostKeygenJoinThreshold(1); got != 0 {
		t.Fatalf("expected single-signer threshold to be 0, got %d", got)
	}
	if got := frostKeygenJoinThreshold(0); got != 0 {
		t.Fatalf("expected empty signer set threshold to be 0, got %d", got)
	}
}

func TestFrostKeygenRound1TimeoutReturnsBlame(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), 30*time.Millisecond)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	})
	if failResp == nil {
		t.Fatalf("expected timeout failure response")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if failResp.Blame.Round != frostDKGRound1 {
		t.Fatalf("expected blame round %s, got %s", frostDKGRound1, failResp.Blame.Round)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected missing signer blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
}

func TestFrostKeygenRound1RejectsMalformedPayload(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	msgChan := newFrostKeygenInbox()
	enqueueWrappedPayload(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], []byte("{bad-json"))
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)

	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	})
	if failResp == nil {
		t.Fatalf("expected malformed payload failure response")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected malformed sender blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
}

func TestFrostKeygenRound1RejectsSenderBindingMismatch(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[2], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment",
	})
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)

	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	})
	if failResp == nil {
		t.Fatalf("expected sender-binding failure response")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[2] {
		t.Fatalf("expected sender binding blame for %s, got %+v", testPubKeys[2], failResp.Blame.BlameNodes)
	}
}

func TestFrostKeygenRoundsBlameTransportPeerForInvalidSender(t *testing.T) {
	ensureBech32Prefix()

	// The transport sender is a member distinct from both the receiver and the
	// impersonated member. A payload claim must never select the blamed node.
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	localShares := map[string]string{
		strconv.FormatUint(uint64(participantIndexMap[normalizePubKey(testPubKeys[1])]), 10): "local-to-member1",
		strconv.FormatUint(uint64(participantIndexMap[normalizePubKey(testPubKeys[2])]), 10): "local-to-member2",
	}
	cases := []struct {
		name   string
		claim  string
		reason string
	}{
		{name: "member claim", claim: testPubKeys[1], reason: "sender peer binding mismatch"},
		{name: "non-member claim", claim: testPubKeys[3], reason: "sender peer binding mismatch"},
		{name: "invalid claim", claim: "invalid-pubkey", reason: "failed to parse sender pubkey"},
		{name: "empty claim", claim: "", reason: "empty sender pubkey"},
	}
	for _, round := range []string{frostDKGRound1, frostDKGRound2, frostDKGRound3} {
		for _, tc := range cases {
			t.Run(round+"/"+tc.name, func(t *testing.T) {
				inbox := newFrostKeygenInbox()
				coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
				initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
				claimedIndex := participantIndexMap[normalizePubKey(tc.claim)]
				var failure *FrostKeygenResponse
				switch round {
				case frostDKGRound1:
					remote := FrostDKGRound1Payload{
						SenderPubKey: tc.claim, ParticipantIndex: claimedIndex, Commitments: "remote-commitment",
					}
					enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[2], remote)
					_, failure = coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, FrostDKGRound1Payload{
						SenderPubKey: testPubKeys[0], ParticipantIndex: localIndex, Commitments: "local-commitment",
					})
				case frostDKGRound2:
					remote := FrostDKGRound2Payload{
						SenderPubKey: tc.claim, ParticipantIndex: claimedIndex,
						EncryptedShares: map[string]string{strconv.FormatUint(uint64(localIndex), 10): "remote-to-local"},
					}
					enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound2Msg, "msg-id", testPubKeys[2], remote)
					_, failure = coord.exchangeRound2(activeSet, participantIndexMap, peerIDs, FrostDKGRound2Payload{
						SenderPubKey: testPubKeys[0], ParticipantIndex: localIndex, EncryptedShares: localShares,
					})
				case frostDKGRound3:
					remote := FrostDKGRound3Payload{
						SenderPubKey: tc.claim, ParticipantIndex: claimedIndex,
						Address: "48addr", GroupKey: strings.Repeat("ab", 32),
					}
					enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[2], remote)
					_, _, _, failure = coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
						SenderPubKey: testPubKeys[0], ParticipantIndex: localIndex,
						Address: "48addr", GroupKey: strings.Repeat("ab", 32),
					})
				}
				if failure == nil || failure.Status != tcommon.Fail {
					t.Fatalf("expected invalid sender to fail keygen, got %+v", failure)
				}
				if failure.Blame.Round != round || !strings.Contains(failure.Blame.FailReason, tc.reason) {
					t.Fatalf("unexpected failure: %+v", failure.Blame)
				}
				if len(failure.Blame.BlameNodes) != 1 || failure.Blame.BlameNodes[0].Pubkey != testPubKeys[2] {
					t.Fatalf("expected transport peer %s to be blamed, got %+v", testPubKeys[2], failure.Blame.BlameNodes)
				}
			})
		}
	}
}

func TestFrostKeygenSenderBindingFailureWithoutResolvablePeerHasNoBlame(t *testing.T) {
	ensureBech32Prefix()

	activeSet, participantIndexMap, _ := testFrostKeygenParticipants(t, []string{testPubKeys[0], testPubKeys[1]})
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), time.Second)
	_, failure := coord.validateRoundPayloadSender(
		peer.ID("invalid-peer"), frostDKGRound1, testPubKeys[1],
		participantIndexMap[normalizePubKey(testPubKeys[1])], activeSet, participantIndexMap,
	)
	if failure == nil || failure.Status != tcommon.Fail {
		t.Fatalf("expected sender-binding failure, got %+v", failure)
	}
	if failure.Blame.FailReason != "sender peer binding mismatch" || len(failure.Blame.BlameNodes) != 0 {
		t.Fatalf("unresolvable peer must not blame the claimed key: %+v", failure.Blame)
	}
}

func TestFrostKeygenRound3RejectsResultMismatch(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}
	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[1], FrostDKGRound3Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Address:          "48addr",
		GroupKey:         strings.Repeat("cd", 32),
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, _, _, failResp := coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Address:          "48addr",
		GroupKey:         strings.Repeat("ab", 32),
	})
	if failResp == nil {
		t.Fatalf("expected mismatch failure response")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected mismatch blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
}

func TestFrostKeygenRound3RejectsUncertifiedPayloadsOnTimeout(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	groupAddress := "48addr"
	groupKey := strings.Repeat("ab", 32)
	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[1], FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[1],
		ParticipantIndex:     participantIndexMap[normalizePubKey(testPubKeys[1])],
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 123,
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, 30*time.Millisecond)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, _, _, failResp := coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[0],
		ParticipantIndex:     participantIndexMap[normalizePubKey(testPubKeys[0])],
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
	})
	if failResp == nil {
		t.Fatal("expected uncertified round3 payloads to fail on timeout")
	}
	if failResp.Status != tcommon.Fail || failResp.Blame.Round != frostDKGRound3 {
		t.Fatalf("expected round3 failure, got %+v", failResp)
	}
}

func TestFrostDKGBirthdayUsesLowerMedianAndMargin(t *testing.T) {
	height, err := frostDKGBirthdayHeight(map[string]int64{
		"a": 1_000,
		"b": 1_001,
		"c": 1_002,
		"d": 50_000,
	}, 4)
	if err != nil {
		t.Fatalf("derive birthday: %v", err)
	}
	if height != 991 {
		t.Fatalf("expected lower median 1001 minus margin 10, got %d", height)
	}

	height, err = frostDKGBirthdayHeight(map[string]int64{
		"a": 0,
		"b": 1_000,
		"c": 1_001,
		"d": 1_002,
	}, 4)
	if err != nil {
		t.Fatalf("derive birthday with one abstention: %v", err)
	}
	if height != 991 {
		t.Fatalf("expected positive-height median 1001 minus margin 10, got %d", height)
	}

	if _, err := frostDKGBirthdayHeight(map[string]int64{
		"a": 0,
		"b": 0,
		"c": 1_000,
		"d": 1_001,
	}, 4); err == nil {
		t.Fatal("expected fewer than quorum positive heights to be rejected")
	}
	if _, err := frostDKGBirthdayHeight(map[string]int64{"a": -1}, 1); err == nil {
		t.Fatal("expected a negative height to be rejected")
	}
}

func TestFrostKeygenRound1AcceptsTranscriptVerification(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localPayload := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	}
	remotePayload := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment",
	}
	localHash, err := FrostTranscriptDKGRound1Hash("msg-id", localPayload)
	if err != nil {
		t.Fatalf("local hash: %v", err)
	}
	remoteHash, err := FrostTranscriptDKGRound1Hash("msg-id", remotePayload)
	if err != nil {
		t.Fatalf("remote hash: %v", err)
	}

	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], remotePayload)
	transcriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound1,
		frostKeygenTranscriptEntry{participantIndex: localPayload.ParticipantIndex, payloadHash: localHash},
		frostKeygenTranscriptEntry{participantIndex: remotePayload.ParticipantIndex, payloadHash: remoteHash},
	)
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound1, transcriptHash)

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	values, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, localPayload)
	if failResp != nil {
		t.Fatalf("expected success, got fail response: %+v", failResp)
	}
	if len(values) != 2 {
		t.Fatalf("expected two commitment payloads, got %d", len(values))
	}
}

func TestFrostKeygenRound1GrantsConfirmationPhaseFullTimeout(t *testing.T) {
	// Virtual time keeps the deadline assertions independent of scheduler delays
	// on loaded race-test runners, without changing the coordinator's timer.
	synctest.Test(t, func(t *testing.T) {
		ensureBech32Prefix()

		members := []string{testPubKeys[0], testPubKeys[1]}
		activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
		localPayload := FrostDKGRound1Payload{
			SenderPubKey:     testPubKeys[0],
			ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
			Commitments:      "local-commitment",
		}
		remotePayload := FrostDKGRound1Payload{
			SenderPubKey:     testPubKeys[1],
			ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
			Commitments:      "remote-commitment",
		}
		localHash, err := FrostTranscriptDKGRound1Hash("msg-id", localPayload)
		if err != nil {
			t.Fatalf("local hash: %v", err)
		}
		remoteHash, err := FrostTranscriptDKGRound1Hash("msg-id", remotePayload)
		if err != nil {
			t.Fatalf("remote hash: %v", err)
		}
		transcriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound1,
			frostKeygenTranscriptEntry{participantIndex: localPayload.ParticipantIndex, payloadHash: localHash},
			frostKeygenTranscriptEntry{participantIndex: remotePayload.ParticipantIndex, payloadHash: remoteHash},
		)

		inbox := newFrostKeygenInbox()
		// Deliver the payload before the original deadline, then deliver its vote
		// after that deadline but within the new confirmation phase.
		const phaseTimeout = time.Second
		coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, phaseTimeout)
		initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
		done := make(chan *FrostKeygenResponse, 1)
		go func() {
			_, failure := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, localPayload)
			done <- failure
		}()

		if initial := <-coord.p2pCommunication.BroadcastMsgChan; initial.WrappedMessage.MessageType != messages.TSSFrostDKGRound1Msg {
			t.Fatalf("initial broadcast type = %q, want round1", initial.WrappedMessage.MessageType)
		}
		synctest.Wait() // The payload-collection timer is now running.
		time.Sleep(700 * time.Millisecond)
		enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], remotePayload)
		select {
		case confirmation := <-coord.p2pCommunication.BroadcastMsgChan:
			if confirmation.WrappedMessage.MessageType != messages.TSSFrostVerifyMsg {
				t.Fatalf("confirmation broadcast type = %q, want verification", confirmation.WrappedMessage.MessageType)
			}
		case <-time.After(phaseTimeout):
			t.Fatal("local transcript confirmation was not broadcast")
		}
		synctest.Wait() // Let the coordinator reset its timer after broadcasting.

		// This vote arrives after the original payload-collection deadline but well
		// within the reset confirmation deadline. Without the phase reset the round
		// would expire roughly 300 ms after the local confirmation was emitted.
		time.Sleep(500 * time.Millisecond)
		enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound1, transcriptHash)
		select {
		case failure := <-done:
			if failure != nil {
				t.Fatalf("confirmation phase did not receive its own timeout: %+v", failure)
			}
		case <-time.After(phaseTimeout):
			t.Fatal("round did not finish after quorum confirmation")
		}
	})
}

func TestFrostKeygenRound1RejectsSenderEquivocation(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment-a",
	})
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment-b",
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	})
	if failResp == nil {
		t.Fatalf("expected sender equivocation failure")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
}

func TestFrostKeygenRound1RejectsVerifierEquivocation(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localPayload := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	}
	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment",
	})
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound1, strings.Repeat("a", 64))
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound1, strings.Repeat("f", 64))

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, 30*time.Millisecond)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	// Equivocation is only reported when the round times out. Route both votes
	// before the round timer exists: left queued, a loaded runner can fire the
	// timer before select drains them and the timeout carries no equivocation.
	for range 2 {
		if response := coord.routeVerificationMessage(<-msgChan.verify, frostDKGRound1); response != nil {
			t.Fatalf("equivocating vote failed before the round timeout: %+v", response)
		}
	}
	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, localPayload)
	if failResp == nil {
		t.Fatalf("expected verifier equivocation failure")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected verifier equivocation blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
	if !strings.Contains(failResp.Blame.FailReason, "verification vote equivocation") {
		t.Fatalf("expected equivocation diagnosis, got %q", failResp.Blame.FailReason)
	}
}

func TestFrostKeygenVerificationGapsSeparateParticipantBehavior(t *testing.T) {
	ensureBech32Prefix()

	members := testPubKeys[:4]
	activeSet, participantIndexMap, _ := testFrostKeygenParticipants(t, members)
	verifier, err := newFrostKeygenRoundVerifier(frostDKGRound1, activeSet, participantIndexMap)
	if err != nil {
		t.Fatalf("new keygen round verifier: %v", err)
	}
	if _, failure := verifier.recordTranscriptVote(testPubKeys[3], strings.Repeat("c", 64)); failure != nil {
		t.Fatalf("record first equivocating vote: %v", failure)
	}
	if _, failure := verifier.recordTranscriptVote(testPubKeys[3], strings.Repeat("d", 64)); failure != nil {
		t.Fatalf("record second equivocating vote: %v", failure)
	}
	earlyGaps := verifier.verificationGaps()
	if len(earlyGaps.missingPubKeys) != 0 || len(earlyGaps.conflictingPubKeys) != 0 ||
		len(earlyGaps.equivocatingPubKeys) != 1 || earlyGaps.equivocatingPubKeys[0] != testPubKeys[3] {
		t.Fatalf("pre-transcript verification gaps = %+v, want only equivocator %s", earlyGaps, testPubKeys[3])
	}

	verifier.transcriptHash = strings.Repeat("a", 64)
	if _, failure := verifier.recordTranscriptVote(testPubKeys[0], verifier.transcriptHash); failure != nil {
		t.Fatalf("record matching vote: %v", failure)
	}
	if _, failure := verifier.recordTranscriptVote(testPubKeys[2], strings.Repeat("b", 64)); failure != nil {
		t.Fatalf("record conflicting vote: %v", failure)
	}

	gaps := verifier.verificationGaps()
	if len(gaps.missingPubKeys) != 1 || gaps.missingPubKeys[0] != testPubKeys[1] {
		t.Fatalf("missing verifiers = %v, want [%s]", gaps.missingPubKeys, testPubKeys[1])
	}
	if len(gaps.conflictingPubKeys) != 1 || gaps.conflictingPubKeys[0] != testPubKeys[2] {
		t.Fatalf("conflicting verifiers = %v, want [%s]", gaps.conflictingPubKeys, testPubKeys[2])
	}
	if len(gaps.equivocatingPubKeys) != 1 || gaps.equivocatingPubKeys[0] != testPubKeys[3] {
		t.Fatalf("equivocating verifiers = %v, want [%s]", gaps.equivocatingPubKeys, testPubKeys[3])
	}
}

func TestFrostKeygenRound1ConflictingVerificationIsNotBlamedAsMissing(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
	localPayload := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	}
	remotePayload := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment",
	}
	localHash, err := FrostTranscriptDKGRound1Hash("msg-id", localPayload)
	if err != nil {
		t.Fatalf("local payload hash: %v", err)
	}
	remoteHash, err := FrostTranscriptDKGRound1Hash("msg-id", remotePayload)
	if err != nil {
		t.Fatalf("remote payload hash: %v", err)
	}
	transcriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound1,
		frostKeygenTranscriptEntry{participantIndex: localPayload.ParticipantIndex, payloadHash: localHash},
		frostKeygenTranscriptEntry{participantIndex: remotePayload.ParticipantIndex, payloadHash: remoteHash},
	)
	conflictingHash := strings.Repeat("f", 64)
	if conflictingHash == transcriptHash {
		conflictingHash = strings.Repeat("e", 64)
	}

	inbox := newFrostKeygenInbox()
	enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], remotePayload)
	enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound1, conflictingHash)
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, 30*time.Millisecond)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, localPayload)
	if failResp == nil {
		t.Fatal("expected transcript disagreement timeout")
	}
	if len(failResp.Blame.BlameNodes) != 0 {
		t.Fatalf("conflicting verifier must not be blamed as missing: %+v", failResp.Blame.BlameNodes)
	}
	if !strings.Contains(failResp.Blame.FailReason, "local transcript disagrees") {
		t.Fatalf("expected transcript disagreement diagnosis, got %q", failResp.Blame.FailReason)
	}
}

func TestFrostKeygenRound1TimeoutMissingVerifications(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment",
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, 30*time.Millisecond)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, failResp := coord.exchangeRound1(activeSet, participantIndexMap, peerIDs, FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Commitments:      "local-commitment",
	})
	if failResp == nil {
		t.Fatalf("expected timeout failure")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if len(failResp.Blame.BlameNodes) != 1 || failResp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected missing verifier blame for %s, got %+v", testPubKeys[1], failResp.Blame.BlameNodes)
	}
}

func TestFrostTranscriptVerifierRejectsUnanimousHashMismatch(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	verifier, err := newFrostTranscriptVerifier(frostDKGRound1, activeSet, participantIndexMap)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}

	targetPayload := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
		Commitments:      "remote-commitment",
	}
	targetHash, err := FrostTranscriptDKGRound1Hash("msg-id", targetPayload)
	if err != nil {
		t.Fatalf("target hash: %v", err)
	}
	if _, failure := verifier.recordPayload(targetPayload.SenderPubKey, targetPayload.ParticipantIndex, targetHash); failure != nil {
		t.Fatalf("record payload failed: %v", failure)
	}

	badHash := strings.Repeat("f", 64)
	if _, failure := verifier.recordVerification(testPubKeys[0], targetPayload.SenderPubKey, targetPayload.ParticipantIndex, badHash); failure != nil {
		t.Fatalf("first mismatched verification should not fail before quorum: %v", failure)
	}
	if _, failure := verifier.recordVerification(testPubKeys[1], targetPayload.SenderPubKey, targetPayload.ParticipantIndex, badHash); failure != nil {
		t.Fatalf("unsigned hash mismatch must not assign blame: %v", failure)
	}
	if verifier.isComplete() {
		t.Fatalf("mismatched verification quorum must not complete the transcript")
	}
}

func TestFrostTranscriptVerifierDuplicateRoundPayloadIsIdempotentAndConflictRejected(t *testing.T) {
	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	verifier, err := newFrostTranscriptVerifier(frostDKGRound1, activeSet, participantIndexMap)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	target := testPubKeys[1]
	index := participantIndexMap[normalizePubKey(target)]
	firstHash := strings.Repeat("a", 64)
	conflictHash := strings.Repeat("b", 64)

	added, failure := verifier.recordPayload(target, index, firstHash)
	if !added || failure != nil {
		t.Fatalf("first payload added=%t failure=%v", added, failure)
	}
	added, failure = verifier.recordPayload(target, index, firstHash)
	if added || failure != nil {
		t.Fatalf("exact retry added=%t failure=%v", added, failure)
	}
	if len(verifier.payloadHashByTarget) != 1 {
		t.Fatalf("exact retry changed payload cardinality: %d", len(verifier.payloadHashByTarget))
	}
	added, failure = verifier.recordPayload(target, index, conflictHash)
	if added || failure == nil {
		t.Fatalf("conflicting retry added=%t failure=%v", added, failure)
	}
	if got := verifier.payloadHashByTarget[normalizePubKey(target)]; got != firstHash {
		t.Fatalf("conflict replaced first payload hash: %s", got)
	}
}

func TestFrostTranscriptDKGRound2HashDeterministicAcrossMapOrder(t *testing.T) {
	payloadA := FrostDKGRound2Payload{
		SenderPubKey:     "sender",
		ParticipantIndex: 1,
		EncryptedShares: map[string]string{
			"2": "value-b",
			"1": "value-a",
			"3": "value-c",
		},
	}
	payloadB := FrostDKGRound2Payload{
		SenderPubKey:     "sender",
		ParticipantIndex: 1,
		EncryptedShares: map[string]string{
			"3": "value-c",
			"1": "value-a",
			"2": "value-b",
		},
	}
	hashA, err := FrostTranscriptDKGRound2Hash("msg-id", payloadA)
	if err != nil {
		t.Fatalf("hashA: %v", err)
	}
	hashB, err := FrostTranscriptDKGRound2Hash("msg-id", payloadB)
	if err != nil {
		t.Fatalf("hashB: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("expected deterministic map-order hash, got %s vs %s", hashA, hashB)
	}
}

func TestFrostTranscriptDKGRound2HashAllowsEmptyShares(t *testing.T) {
	payload := FrostDKGRound2Payload{
		SenderPubKey:     "sender",
		ParticipantIndex: 1,
		EncryptedShares:  map[string]string{},
	}

	hashA, err := FrostTranscriptDKGRound2Hash("msg-id", payload)
	if err != nil {
		t.Fatalf("hashA: %v", err)
	}
	hashB, err := FrostTranscriptDKGRound2Hash("msg-id", payload)
	if err != nil {
		t.Fatalf("hashB: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("expected deterministic empty-share hash, got %s vs %s", hashA, hashB)
	}
}

func TestFrostKeygenExecuteSuccess(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	activePeerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	remoteIndexKey := strconv.FormatUint(uint64(remoteIndex), 10)

	msgChan := newFrostKeygenInbox()
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)
	localRound1 := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		Commitments:      "local-commitment",
	}
	remoteRound1 := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		Commitments:      "remote-commitment",
	}
	localRound1Hash, err := FrostTranscriptDKGRound1Hash("msg-id", localRound1)
	if err != nil {
		t.Fatalf("local round1 hash: %v", err)
	}
	remoteRound1Hash, err := FrostTranscriptDKGRound1Hash("msg-id", remoteRound1)
	if err != nil {
		t.Fatalf("remote round1 hash: %v", err)
	}

	localRound2 := FrostDKGRound2Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		EncryptedShares: map[string]string{
			remoteIndexKey: "local-to-remote",
		},
	}
	remoteRound2 := FrostDKGRound2Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		EncryptedShares: map[string]string{
			localIndexKey: "remote-to-local",
		},
	}
	localRound2Hash, err := FrostTranscriptDKGRound2Hash("msg-id", localRound2)
	if err != nil {
		t.Fatalf("local round2 hash: %v", err)
	}
	remoteRound2Hash, err := FrostTranscriptDKGRound2Hash("msg-id", remoteRound2)
	if err != nil {
		t.Fatalf("remote round2 hash: %v", err)
	}

	localRound3 := FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[0],
		ParticipantIndex:     localIndex,
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
	}
	remoteRound3 := FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[1],
		ParticipantIndex:     remoteIndex,
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 455,
	}
	localRound3Hash, err := FrostTranscriptDKGRound3Hash("msg-id", localRound3)
	if err != nil {
		t.Fatalf("local round3 hash: %v", err)
	}
	remoteRound3Hash, err := FrostTranscriptDKGRound3Hash("msg-id", remoteRound3)
	if err != nil {
		t.Fatalf("remote round3 hash: %v", err)
	}

	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		Commitments:      "remote-commitment",
	})
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound1,
		testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound1,
			frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound1Hash},
			frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound1Hash},
		),
	)
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound2Msg, "msg-id", testPubKeys[1], remoteRound2)
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound2,
		testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound2,
			frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound2Hash},
			frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound2Hash},
		),
	)
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[1], remoteRound3)
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound3,
		testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound3,
			frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound3Hash},
			frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound3Hash},
		),
	)

	sidecar := &mockFrostSidecarKeygen{
		round1Resp: FrostSidecarDKGRound1Resp{Commitments: "local-commitment"},
		round2Resp: FrostSidecarDKGRound2Resp{EncryptedShares: localRound2.EncryptedShares},
		round3Resp: FrostSidecarDKGRound3Resp{
			Address:  groupAddress,
			GroupKey: groupKey,
		},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
		chainHeight:  456,
	}

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	coord.sidecar = sidecar
	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-vault",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, activePeerIDs)
	if resp.Status != tcommon.Success {
		t.Fatalf("expected success status, got %d with blame %+v", resp.Status, resp.Blame)
	}
	if resp.Address != groupAddress || resp.GroupKey != groupKey {
		t.Fatalf("unexpected keygen response: %+v", resp)
	}
	if resp.ObservedMoneroHeight != 445 {
		t.Fatalf("expected lower height 455 minus margin 10, got %d", resp.ObservedMoneroHeight)
	}

	if len(sidecar.round1Reqs) != 1 {
		t.Fatalf("expected one round1 call, got %d", len(sidecar.round1Reqs))
	}
	if sidecar.round1Reqs[0].ParticipantIndex != localIndex {
		t.Fatalf("expected local participant index %d, got %d", localIndex, sidecar.round1Reqs[0].ParticipantIndex)
	}
	if sidecar.round1Reqs[0].N != 2 || sidecar.round1Reqs[0].Threshold != 2 {
		t.Fatalf("unexpected round1 N/threshold: %+v", sidecar.round1Reqs[0])
	}
	if sidecar.round1Reqs[0].VaultPubKey != "thorpub-vault" {
		t.Fatalf("unexpected round1 vault pubkey: %+v", sidecar.round1Reqs[0])
	}
	if len(sidecar.finalizeReqs) != 1 {
		t.Fatalf("expected one finalize call, got %d", len(sidecar.finalizeReqs))
	}
	if len(sidecar.round3Reqs) != 1 {
		t.Fatalf("expected one round3 call, got %d", len(sidecar.round3Reqs))
	}
	if len(sidecar.round3Reqs[0].AllShares) != 1 || sidecar.round3Reqs[0].AllShares[remoteIndexKey] != "remote-to-local" {
		t.Fatalf("unexpected round3 shares: %+v", sidecar.round3Reqs[0].AllShares)
	}
	if sidecar.finalizeReqs[0].SessionID != "msg-id" {
		t.Fatalf("unexpected finalize session id: %+v", sidecar.finalizeReqs[0])
	}
	if sidecar.finalizeReqs[0].GroupKey != groupKey || sidecar.finalizeReqs[0].Address != groupAddress {
		t.Fatalf("unexpected finalize payload: %+v", sidecar.finalizeReqs[0])
	}
}

// TestFrostKeygenRetainsFutureRoundTraffic forces the exact phase skew that a
// shared queue could not tolerate: round-3 payload and echo traffic arrives
// before the final round-2 echoes. Round 2 must finish without consuming the
// payload, and Round 3 must start with the early echoes still available.
func TestFrostKeygenRetainsFutureRoundTraffic(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	remoteIndexKey := strconv.FormatUint(uint64(remoteIndex), 10)

	localRound2 := FrostDKGRound2Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		EncryptedShares: map[string]string{
			remoteIndexKey: "local-to-remote",
		},
	}
	remoteRound2 := FrostDKGRound2Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		EncryptedShares: map[string]string{
			localIndexKey: "remote-to-local",
		},
	}
	localRound2Hash, err := FrostTranscriptDKGRound2Hash("msg-id", localRound2)
	if err != nil {
		t.Fatalf("local round2 hash: %v", err)
	}
	remoteRound2Hash, err := FrostTranscriptDKGRound2Hash("msg-id", remoteRound2)
	if err != nil {
		t.Fatalf("remote round2 hash: %v", err)
	}

	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)
	localRound3 := FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[0],
		ParticipantIndex:     localIndex,
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
	}
	remoteRound3 := FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[1],
		ParticipantIndex:     remoteIndex,
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 455,
	}
	localRound3Hash, err := FrostTranscriptDKGRound3Hash("msg-id", localRound3)
	if err != nil {
		t.Fatalf("local round3 hash: %v", err)
	}
	remoteRound3Hash, err := FrostTranscriptDKGRound3Hash("msg-id", remoteRound3)
	if err != nil {
		t.Fatalf("remote round3 hash: %v", err)
	}

	inbox := newFrostKeygenInbox()
	// The remote node is a full phase ahead. These are intentionally queued
	// before the round-2 messages on the shared verification topic.
	enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound3Msg, "msg-id", testPubKeys[1], remoteRound3)
	round3TranscriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound3,
		frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound3Hash},
		frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound3Hash},
	)
	enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound3, round3TranscriptHash)
	enqueueRoundMessage(t, inbox, messages.TSSFrostDKGRound2Msg, "msg-id", testPubKeys[1], remoteRound2)
	round2TranscriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound2,
		frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound2Hash},
		frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound2Hash},
	)
	enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound2, round2TranscriptHash)

	// Every message is already queued, so the timeout only bounds CPU time; a
	// 100ms bound timed out under -race on a loaded runner.
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	shares, failure := coord.exchangeRound2(activeSet, participantIndexMap, peerIDs, localRound2)
	if failure != nil {
		t.Fatalf("round2 failed under phase skew: %+v", failure)
	}
	if shares[remoteIndexKey][localIndexKey] != "remote-to-local" {
		t.Fatalf("round2 lost the remote encrypted share: %+v", shares)
	}
	round3Verifier, err := coord.roundState.verifier(frostDKGRound3)
	if err != nil {
		t.Fatalf("round3 verifier: %v", err)
	}
	if got := len(round3Verifier.payloadHashByTarget); got != 0 {
		t.Fatalf("future round payloads must remain unvalidated, got %d", got)
	}
	if round3Verifier.isComplete() {
		t.Fatal("future echoes completed round3 before its payloads were validated")
	}
	remoteNorm := normalizePubKey(testPubKeys[1])
	if got := round3Verifier.votesByAttester[remoteNorm]; got != round3TranscriptHash {
		t.Fatalf("expected one retained early round vote, got %q", got)
	}
	if got := len(inbox.round3); got != 1 {
		t.Fatalf("expected the early round3 payload to remain queued, got %d", got)
	}

	address, derivedKey, birthday, failure := coord.exchangeRound3(
		activeSet,
		participantIndexMap,
		peerIDs,
		localRound3,
	)
	if failure != nil {
		t.Fatalf("round3 failed after draining staged traffic: %+v", failure)
	}
	if address != groupAddress || derivedKey != groupKey || birthday != 445 {
		t.Fatalf("unexpected round3 result: address=%q key=%q birthday=%d", address, derivedKey, birthday)
	}
}

// TestFrostKeygenEarlyResultQuorumSurvivesRound3 proves terminal results stay
// on their dedicated channel while the local node is still waiting in Round 3
// and remain available to the adoption waiter after the local round times out.
func TestFrostKeygenEarlyResultQuorumSurvivesRound3(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	activeSet, participantIndexMap, peerIDs := testFrostKeygenParticipants(t, members)
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)
	success := FrostDKGResult{
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
		Status:               uint8(tcommon.Success),
	}

	inbox := newFrostKeygenInbox()
	enqueueRoundMessage(t, inbox, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[1],
		Response:     success,
	})
	enqueueRoundMessage(t, inbox, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[2], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[2],
		Response:     success,
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, 30*time.Millisecond)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	_, _, _, failure := coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
		SenderPubKey:         testPubKeys[0],
		ParticipantIndex:     participantIndexMap[normalizePubKey(testPubKeys[0])],
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
	})
	if failure == nil || failure.Blame.Round != frostDKGRound3 {
		t.Fatalf("expected local round3 timeout, got %+v", failure)
	}

	// Both results were queued before round3 started, so this deadline only bounds
	// a hang; a short one loses the race on a loaded -race runner.
	response, err := coord.WaitForResult(members, 2, 10*time.Second)
	if err != nil {
		t.Fatalf("early result quorum was lost: %v", err)
	}
	if response.Status != tcommon.Success || response.Address != groupAddress || response.GroupKey != groupKey {
		t.Fatalf("unexpected retained result: %+v", response)
	}
}

func TestFrostKeygenFutureVerificationIsIdempotentAndCannotCompleteWithoutPayload(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, _ := testFrostKeygenParticipants(t, members)
	inbox := newFrostKeygenInbox()
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	localPayloadHash := strings.Repeat("a", 64)
	remotePayloadHash := strings.Repeat("b", 64)
	transcriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound3,
		frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localPayloadHash},
		frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remotePayloadHash},
	)
	enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound3, transcriptHash)
	enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound3, strings.ToUpper(transcriptHash))
	for range 2 {
		if response := coord.routeVerificationMessage(<-inbox.verify, frostDKGRound2); response != nil {
			t.Fatalf("future verification failed early: %+v", response)
		}
	}

	verifier, err := coord.roundState.verifier(frostDKGRound3)
	if err != nil {
		t.Fatalf("round3 verifier: %v", err)
	}
	remoteNorm := normalizePubKey(testPubKeys[1])
	if got := verifier.votesByAttester[remoteNorm]; got != transcriptHash {
		t.Fatalf("authenticated peer was not recorded as the attester: got %q want %q", got, transcriptHash)
	}
	if verifier.isComplete() {
		t.Fatal("future echo completed round3 without a payload")
	}
	if _, failure := verifier.recordPayload(testPubKeys[0], localIndex, localPayloadHash); failure != nil {
		t.Fatalf("record local payload: %v", failure)
	}
	if verifier.isComplete() {
		t.Fatal("round3 completed while the remote payload was still absent")
	}
	if _, failure := verifier.recordPayload(testPubKeys[1], remoteIndex, remotePayloadHash); failure != nil {
		t.Fatalf("record remote payload: %v", failure)
	}
	preparedHash, ready, err := verifier.prepareTranscriptHash("msg-id")
	if err != nil || !ready || preparedHash != transcriptHash {
		t.Fatalf("prepare transcript hash: hash=%q ready=%t err=%v", preparedHash, ready, err)
	}
	if _, failure := verifier.recordTranscriptVote(testPubKeys[0], preparedHash); failure != nil {
		t.Fatalf("record local transcript vote: %v", failure)
	}
	if !verifier.isComplete() {
		t.Fatal("early remote vote was not counted after the full transcript arrived")
	}
}

func TestFrostKeygenFutureVerificationConflictDisqualifiesAttester(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	activeSet, participantIndexMap, _ := testFrostKeygenParticipants(t, members)
	inbox := newFrostKeygenInbox()
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	conflictingHash := strings.Repeat("b", 64)
	for _, hash := range []string{strings.Repeat("a", 64), conflictingHash, strings.Repeat("a", 64)} {
		enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[1], frostDKGRound3, hash)
		if response := coord.routeVerificationMessage(<-inbox.verify, frostDKGRound2); response != nil {
			t.Fatalf("future verification failed early: %+v", response)
		}
	}

	verifier, err := coord.roundState.verifier(frostDKGRound3)
	if err != nil {
		t.Fatalf("round3 verifier: %v", err)
	}
	remoteNorm := normalizePubKey(testPubKeys[1])
	if _, disqualified := verifier.equivocatingAttesters[remoteNorm]; !disqualified {
		t.Fatal("conflicting future echoes did not disqualify their attester")
	}
	if _, retained := verifier.votesByAttester[remoteNorm]; retained {
		t.Fatal("disqualified attester still contributes a verification vote")
	}
	entries := make([]frostKeygenTranscriptEntry, 0, len(members))
	for _, member := range members {
		index := participantIndexMap[normalizePubKey(member)]
		payloadHash := strings.Repeat(strconv.FormatUint(uint64(index), 10), 64)
		payloadHash = payloadHash[:64]
		if _, failure := verifier.recordPayload(member, index, payloadHash); failure != nil {
			t.Fatalf("record payload for %s: %v", member, failure)
		}
		entries = append(entries, frostKeygenTranscriptEntry{participantIndex: index, payloadHash: payloadHash})
	}
	transcriptHash := testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound3, entries...)
	preparedHash, ready, err := verifier.prepareTranscriptHash("msg-id")
	if err != nil || !ready || preparedHash != transcriptHash {
		t.Fatalf("prepare transcript hash: hash=%q ready=%t err=%v", preparedHash, ready, err)
	}
	if _, failure := verifier.recordTranscriptVote(testPubKeys[0], transcriptHash); failure != nil {
		t.Fatalf("record local vote: %v", failure)
	}
	if verifier.isComplete() {
		t.Fatal("disqualified attester incorrectly completed the transcript quorum")
	}
	enqueueVerifyMessage(t, inbox, "msg-id", testPubKeys[2], frostDKGRound3, transcriptHash)
	if response := coord.routeVerificationMessage(<-inbox.verify, frostDKGRound2); response != nil {
		t.Fatalf("replacement future verification failed: %+v", response)
	}
	if !verifier.isComplete() {
		t.Fatal("non-equivocating quorum did not verify the transcript")
	}
}

// TestFrostKeygenExecuteBlamesAlmostCompleteFinisherOnLastRoundTimeout pins
// the bifrost half of the chainnet mass-jailing mechanism and its fix. The
// remote peer participates fully through rounds 1 and 2 — payloads and
// transcript verifications — and goes silent only in round 3, after the
// local sidecar has already derived the group key. The DKG was one peer
// message away from completing, and Execute still fails with a round-3
// timeout blame (unchanged: the coordinator cannot know the peers finished).
//
// The healing now happens one level up: when the peers' result broadcasts
// reach the on-chain supermajority threshold, AdoptQuorumSuccess proves the
// local keyshare against the adopted group key via the sidecar finalize and
// converts the failure into the same success the finishers report — same tss
// ID, so the node votes with the quorum instead of filing a permanently
// unreleasable failure (see
// TestXMRMonovaultKeygenFailureSubmitterStaysJailedAfterSuccessConsensus in
// x/thorchain for the on-chain half, which still needs the
// MajorityConsensusBlockHeight fix for reports landing after consensus).
// The tail of this test covers exactly that adoption.
func TestFrostKeygenExecuteBlamesAlmostCompleteFinisherOnLastRoundTimeout(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	activePeerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	remoteIndex := participantIndexMap[normalizePubKey(testPubKeys[1])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	remoteIndexKey := strconv.FormatUint(uint64(remoteIndex), 10)

	msgChan := newFrostKeygenInbox()
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)

	localRound1 := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		Commitments:      "local-commitment",
	}
	remoteRound1 := FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		Commitments:      "remote-commitment",
	}
	localRound1Hash, err := FrostTranscriptDKGRound1Hash("msg-id", localRound1)
	if err != nil {
		t.Fatalf("local round1 hash: %v", err)
	}
	remoteRound1Hash, err := FrostTranscriptDKGRound1Hash("msg-id", remoteRound1)
	if err != nil {
		t.Fatalf("remote round1 hash: %v", err)
	}

	localRound2 := FrostDKGRound2Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		EncryptedShares: map[string]string{
			remoteIndexKey: "local-to-remote",
		},
	}
	remoteRound2 := FrostDKGRound2Payload{
		SenderPubKey:     testPubKeys[1],
		ParticipantIndex: remoteIndex,
		EncryptedShares: map[string]string{
			localIndexKey: "remote-to-local",
		},
	}
	localRound2Hash, err := FrostTranscriptDKGRound2Hash("msg-id", localRound2)
	if err != nil {
		t.Fatalf("local round2 hash: %v", err)
	}
	remoteRound2Hash, err := FrostTranscriptDKGRound2Hash("msg-id", remoteRound2)
	if err != nil {
		t.Fatalf("remote round2 hash: %v", err)
	}

	// Rounds 1 and 2: the remote peer is fully present and verifiable.
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id", testPubKeys[1], remoteRound1)
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound1,
		testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound1,
			frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound1Hash},
			frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound1Hash},
		),
	)
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound2Msg, "msg-id", testPubKeys[1], remoteRound2)
	enqueueVerifyMessage(t, msgChan, "msg-id", testPubKeys[1], frostDKGRound2,
		testFrostKeygenTranscriptHash(t, "msg-id", frostDKGRound2,
			frostKeygenTranscriptEntry{participantIndex: localIndex, payloadHash: localRound2Hash},
			frostKeygenTranscriptEntry{participantIndex: remoteIndex, payloadHash: remoteRound2Hash},
		),
	)
	// Round 3: nothing from the remote peer — its final message is a moment
	// too late for the local per-round timer.

	sidecar := &mockFrostSidecarKeygen{
		round1Resp: FrostSidecarDKGRound1Resp{Commitments: "local-commitment"},
		round2Resp: FrostSidecarDKGRound2Resp{EncryptedShares: localRound2.EncryptedShares},
		round3Resp: FrostSidecarDKGRound3Resp{
			Address:  groupAddress,
			GroupKey: groupKey,
		},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
		chainHeight:  456,
	}

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, 150*time.Millisecond)
	coord.sidecar = sidecar
	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-vault",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, activePeerIDs)

	// Current behavior: timeout failure blaming the almost-complete peer.
	if resp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d (resp %+v)", resp.Status, resp)
	}
	if resp.Blame.Round != frostDKGRound3 {
		t.Fatalf("expected blame round %s, got %s", frostDKGRound3, resp.Blame.Round)
	}
	if len(resp.Blame.BlameNodes) != 1 || resp.Blame.BlameNodes[0].Pubkey != testPubKeys[1] {
		t.Fatalf("expected round3 timeout blame for %s, got %+v", testPubKeys[1], resp.Blame.BlameNodes)
	}

	// The local sidecar had already derived the group key: the DKG state was
	// one peer message away from done when the node gave up.
	if len(sidecar.round3Reqs) != 1 {
		t.Fatalf("expected local round3 sidecar call to have happened, got %d", len(sidecar.round3Reqs))
	}
	if len(sidecar.finalizeReqs) != 0 {
		t.Fatalf("expected no finalize call after timeout, got %d", len(sidecar.finalizeReqs))
	}

	// The fix: once the finishers' result broadcasts reach quorum, the node
	// adopts their success — the sidecar finalize proves the local session
	// derived exactly the adopted group key and hands back the keyshare, so
	// the node reports the same-ID success instead of the failure above.
	quorumResp := FrostKeygenResponse{
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
		Status:               tcommon.Success,
	}
	adopted, ok := coord.AdoptQuorumSuccess(FrostKeygenRequest{
		VaultPubKey:   "thorpub-vault",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, quorumResp)
	if !ok {
		t.Fatalf("expected quorum success adoption after completed local DKG")
	}
	if adopted.Status != tcommon.Success {
		t.Fatalf("expected adopted success status, got %d", adopted.Status)
	}
	if adopted.Address != groupAddress || adopted.GroupKey != groupKey {
		t.Fatalf("adopted result must carry the quorum vault: %+v", adopted)
	}
	if adopted.ObservedMoneroHeight != 456 {
		t.Fatalf("adopted result must carry the quorum monero height, got %d", adopted.ObservedMoneroHeight)
	}
	if adopted.KeyBytesHex != "abcd" {
		t.Fatalf("adopted result must carry the locally finalized keyshare, got %q", adopted.KeyBytesHex)
	}
	if len(sidecar.finalizeReqs) != 1 {
		t.Fatalf("expected exactly one finalize call for the adoption proof, got %d", len(sidecar.finalizeReqs))
	}
	if sidecar.finalizeReqs[0].GroupKey != groupKey || sidecar.finalizeReqs[0].Address != groupAddress {
		t.Fatalf("adoption must finalize against the adopted vault: %+v", sidecar.finalizeReqs[0])
	}
}

// TestFrostKeygenAdoptQuorumSuccessRequiresKeyshareProof pins the safety of
// the adoption path: a node may only convert a quorum success into its own
// result when the sidecar finalize proves its DKG session derived exactly
// the adopted vault. Every failure of that proof keeps the local failure.
func TestFrostKeygenAdoptQuorumSuccessRequiresKeyshareProof(t *testing.T) {
	ensureBech32Prefix()

	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)
	req := FrostKeygenRequest{VaultPubKey: "thorpub-vault", ExpectedAddr: groupAddress}
	quorumResp := FrostKeygenResponse{
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
		Status:               tcommon.Success,
	}

	// No completed local DKG session: the sidecar rejects the finalize and the
	// adoption must be refused.
	sidecar := &mockFrostSidecarKeygen{finalizeErr: errors.New("DKG session keys not found")}
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), time.Second)
	coord.sidecar = sidecar
	if _, ok := coord.AdoptQuorumSuccess(req, quorumResp); ok {
		t.Fatalf("expected adoption refusal without a finalizable local session")
	}

	// Address not matching the expected vault address: refuse before ever
	// touching the sidecar.
	sidecar = &mockFrostSidecarKeygen{finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"}}
	coord.sidecar = sidecar
	mismatched := quorumResp
	mismatched.Address = "48otheraddress"
	if _, ok := coord.AdoptQuorumSuccess(req, mismatched); ok {
		t.Fatalf("expected adoption refusal on expected-address mismatch")
	}
	if len(sidecar.finalizeReqs) != 0 {
		t.Fatalf("expected no finalize call on address mismatch, got %d", len(sidecar.finalizeReqs))
	}

	// Finalize succeeding without key bytes cannot prove keyshare possession.
	sidecar = &mockFrostSidecarKeygen{finalizeResp: FrostSidecarDKGFinalizeResp{Success: true}}
	coord.sidecar = sidecar
	if _, ok := coord.AdoptQuorumSuccess(req, quorumResp); ok {
		t.Fatalf("expected adoption refusal on empty keyshare bytes")
	}

	// A quorum response without a positive monero height cannot build a valid
	// XMR keygen success message.
	sidecar = &mockFrostSidecarKeygen{finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"}}
	coord.sidecar = sidecar
	noHeight := quorumResp
	noHeight.ObservedMoneroHeight = 0
	if _, ok := coord.AdoptQuorumSuccess(req, noHeight); ok {
		t.Fatalf("expected adoption refusal without observed monero height")
	}
}

// TestFrostKeygenAdoptionQuorumMatchesOnChainSupermajority keeps the bifrost
// adoption threshold aligned with x/thorchain/types.HasSuperMajority: adopt
// exactly when THORChain will finalize the vault. It asserts against the
// on-chain rule itself rather than hardcoded literals, so a change to
// SuperMajorityFactor is caught here instead of silently diverging from the
// bifrost copy.
func TestFrostKeygenAdoptionQuorumMatchesOnChainSupermajority(t *testing.T) {
	if got := frostKeygenAdoptionQuorum(0); got != 0 {
		t.Fatalf("adoption quorum for 0 participants: expected 0, got %d", got)
	}
	for total := 1; total <= 128; total++ {
		q := frostKeygenAdoptionQuorum(total)
		// q must be the exact on-chain supermajority threshold for `total`
		// members: q reaches it, q-1 does not.
		if !thorchaintypes.HasSuperMajority(q, total) {
			t.Fatalf("adoption quorum %d for %d participants is below the on-chain supermajority", q, total)
		}
		if thorchaintypes.HasSuperMajority(q-1, total) {
			t.Fatalf("adoption quorum %d for %d participants exceeds the on-chain supermajority (q-1 already reaches it)", q, total)
		}
	}
}

// TestFrostKeygenWaitForResultSurvivesInvalidResultMessages pins the result
// waiter's noise tolerance: the keygen msg ID is derivable by any connected
// peer, so a single malformed or unauthorized DKG result message arriving
// before the genuine quorum must be skipped, not end the wait — otherwise one
// peer could veto the quorum-success adoption and every non-active waiter.
// (This mirrors the keysign result waiter's behavior.)
func TestFrostKeygenWaitForResultSurvivesInvalidResultMessages(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)
	success := FrostDKGResult{
		Address:              groupAddress,
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
		Status:               uint8(tcommon.Success),
	}

	msgChan := newFrostKeygenInbox()

	// 1. Unparseable result payload.
	enqueueWrappedPayload(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], []byte("{not-json"))
	// 2. Empty sender pubkey.
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
		SenderPubKey: "",
		Response:     success,
	})
	// 3. Sender outside the active participant set.
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[3], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[3],
		Response:     success,
	})
	// 4. Sender/peer binding mismatch: claims to be member 1 but arrives from
	// member 2's peer.
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[2], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[1],
		Response:     success,
	})
	// 5. Structurally invalid result (success without an address).
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[1],
		Response: FrostDKGResult{
			GroupKey:             groupKey,
			ObservedMoneroHeight: 456,
			Status:               uint8(tcommon.Success),
		},
	})
	// The genuine quorum arrives after all the noise.
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[1],
		Response:     success,
	})
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[2], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[2],
		Response:     success,
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	resp, err := coord.WaitForResult(members, 2, 2*time.Second)
	if err != nil {
		t.Fatalf("expected quorum success despite invalid result noise, got error: %v", err)
	}
	if resp.Status != tcommon.Success {
		t.Fatalf("expected success status, got %d (blame %+v)", resp.Status, resp.Blame)
	}
	if resp.Address != groupAddress || resp.GroupKey != groupKey {
		t.Fatalf("unexpected adopted result: %+v", resp)
	}
}

// TestFrostKeygenWaitForResultDrainsNonResultTraffic pins the failed-active
// path: once local execution stops, peers can still advance and fill the four
// phase queues while this node waits to adopt their result. The waiter must
// consume those now-obsolete frames so bounded delivery does not leave stream
// handlers parked until the keygen timeout.
func TestFrostKeygenWaitForResultDrainsNonResultTraffic(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	inbox := newFrostKeygenInbox()
	nonResultQueues := []chan *p2p.Message{
		inbox.round1,
		inbox.round2,
		inbox.round3,
		inbox.verify,
	}

	// Fill every queue before the waiter starts, then attempt one additional
	// delivery to each. All four sends block before the fix because result was
	// the only topic WaitForResult consumed.
	delivered := make(chan struct{}, len(nonResultQueues))
	for _, queue := range nonResultQueues {
		for range cap(queue) {
			queue <- &p2p.Message{}
		}
		go func(queue chan *p2p.Message) {
			queue <- &p2p.Message{}
			delivered <- struct{}{}
		}(queue)
	}

	type waitResult struct {
		response FrostKeygenResponse
		err      error
	}
	waitDone := make(chan waitResult, 1)
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", inbox, time.Second)
	go func() {
		response, err := coord.WaitForResult(members, 2, 2*time.Second)
		waitDone <- waitResult{response: response, err: err}
	}()

	for range nonResultQueues {
		select {
		case <-delivered:
		case <-time.After(time.Second):
			t.Fatal("non-result queue delivery remained blocked during result wait")
		}
	}

	success := FrostDKGResult{
		Address:              "48xmrvaultaddress",
		GroupKey:             strings.Repeat("ab", 32),
		ObservedMoneroHeight: 456,
		Status:               uint8(tcommon.Success),
	}
	for _, sender := range members[1:] {
		enqueueRoundMessage(t, inbox, messages.TSSFrostDKGResultMsg, "msg-id", sender, FrostDKGResultPayload{
			SenderPubKey: sender,
			Response:     success,
		})
	}

	select {
	case result := <-waitDone:
		if result.err != nil {
			t.Fatalf("wait for result: %v", result.err)
		}
		if result.response.Status != tcommon.Success || result.response.Address != success.Address {
			t.Fatalf("unexpected quorum result: %+v", result.response)
		}
	case <-time.After(time.Second):
		t.Fatal("result quorum was not returned after draining non-result traffic")
	}
}

// TestFrostKeygenWaitForResultLastVoteWinsPerSender pins the per-sender vote
// accounting: a sender re-voting with a different result moves between vote
// buckets, so one member cannot hold votes in two buckets at once (or count
// toward a quorum with a result it has since replaced).
func TestFrostKeygenWaitForResultLastVoteWinsPerSender(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	groupKey := strings.Repeat("ab", 32)
	resultX := FrostDKGResult{
		Address:              "48addressX",
		GroupKey:             groupKey,
		ObservedMoneroHeight: 456,
		Status:               uint8(tcommon.Success),
	}
	resultY := FrostDKGResult{
		Address:              "48addressY",
		GroupKey:             strings.Repeat("cd", 32),
		ObservedMoneroHeight: 456,
		Status:               uint8(tcommon.Success),
	}

	msgChan := newFrostKeygenInbox()
	// Member 1 votes X, then replaces it with Y; member 2 votes X. No result
	// may reach quorum 2: X holds only member 2 after the replacement.
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[1],
		Response:     resultX,
	})
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[1],
		Response:     resultY,
	})
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[2], FrostDKGResultPayload{
		SenderPubKey: testPubKeys[2],
		Response:     resultX,
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, time.Second)
	if _, err := coord.WaitForResult(members, 2, 500*time.Millisecond); err == nil {
		t.Fatalf("expected timeout: the replaced vote must not count toward quorum")
	}
}

func TestFrostKeygenWaitForResultDuplicateVotesDoNotInflateQuorum(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2]}
	result := FrostDKGResult{
		Address:              "48duplicatevoteaddress",
		GroupKey:             strings.Repeat("ab", 32),
		ObservedMoneroHeight: 456,
		Status:               uint8(tcommon.Success),
	}
	msgChan := newFrostKeygenInbox()
	for i := 0; i < 5; i++ {
		enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGResultMsg, "msg-id", testPubKeys[1], FrostDKGResultPayload{
			SenderPubKey: testPubKeys[1],
			Response:     result,
		})
	}
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", msgChan, 50*time.Millisecond)
	if _, err := coord.WaitForResult(members, 2, 100*time.Millisecond); err == nil {
		t.Fatal("duplicate votes from one sender reached quorum")
	}
}

func TestFrostKeygenExecuteSuccessSingleSigner(t *testing.T) {
	ensureBech32Prefix()

	members := []string{testPubKeys[0]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	activePeerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	groupAddress := "48xmrsinglesigner"
	groupKey := strings.Repeat("cd", 32)

	sidecar := &mockFrostSidecarKeygen{
		round1Resp: FrostSidecarDKGRound1Resp{Commitments: "single-commitment"},
		round2Resp: FrostSidecarDKGRound2Resp{EncryptedShares: map[string]string{}},
		round3Resp: FrostSidecarDKGRound3Resp{
			Address:  groupAddress,
			GroupKey: groupKey,
		},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
		chainHeight:  123,
	}

	msgChan := newFrostKeygenInbox()
	enqueueRoundMessage(t, msgChan, messages.TSSFrostDKGRound1Msg, "msg-id-single", testPubKeys[0], FrostDKGRound1Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: localIndex,
		Commitments:      "stale-self-commitment",
	})

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id-single", msgChan, time.Second)
	coord.sidecar = sidecar
	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-single",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, activePeerIDs)
	if resp.Status != tcommon.Success {
		t.Fatalf("expected success status, got %d with blame %+v", resp.Status, resp.Blame)
	}
	if resp.Address != groupAddress || resp.GroupKey != groupKey {
		t.Fatalf("unexpected single-signer keygen response: %+v", resp)
	}
	if resp.ObservedMoneroHeight != sidecar.chainHeight-frostDKGReorgMargin {
		t.Fatalf("expected single-signer birthday %d, got %d", sidecar.chainHeight-frostDKGReorgMargin, resp.ObservedMoneroHeight)
	}
	if len(sidecar.round1Reqs) != 1 {
		t.Fatalf("expected one round1 call, got %d", len(sidecar.round1Reqs))
	}
	if sidecar.round1Reqs[0].ParticipantIndex != localIndex {
		t.Fatalf("expected local participant index %d, got %d", localIndex, sidecar.round1Reqs[0].ParticipantIndex)
	}
	if sidecar.round1Reqs[0].N != 1 || sidecar.round1Reqs[0].Threshold != 1 {
		t.Fatalf("unexpected single-signer round1 N/threshold: %+v", sidecar.round1Reqs[0])
	}
	if len(sidecar.round2Reqs) != 1 {
		t.Fatalf("expected one round2 call, got %d", len(sidecar.round2Reqs))
	}
	if len(sidecar.round2Reqs[0].AllCommitments) != 1 || sidecar.round2Reqs[0].AllCommitments[localIndexKey] != "single-commitment" {
		t.Fatalf("expected only local commitment in single-signer round2, got %+v", sidecar.round2Reqs[0].AllCommitments)
	}
	if len(coord.p2pCommunication.BroadcastMsgChan) != 0 {
		t.Fatalf("expected single-signer keygen to skip p2p transcript exchange, broadcast %d messages", len(coord.p2pCommunication.BroadcastMsgChan))
	}
	if len(msgChan.round1) != 1 {
		t.Fatalf("expected queued p2p noise to remain unread for single-signer keygen, got %d queued messages", len(msgChan.round1))
	}
	if len(sidecar.round3Reqs) != 1 {
		t.Fatalf("expected one round3 call, got %d", len(sidecar.round3Reqs))
	}
	if len(sidecar.round3Reqs[0].AllShares) != 0 {
		t.Fatalf("expected no remote shares in single-signer round3, got %+v", sidecar.round3Reqs[0].AllShares)
	}
	if len(sidecar.finalizeReqs) != 1 {
		t.Fatalf("expected one finalize call, got %d", len(sidecar.finalizeReqs))
	}
}

func TestFrostKeygenExecuteFailsWhenFinalizeUnsuccessful(t *testing.T) {
	members := []string{testPubKeys[0]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)

	sidecar := &mockFrostSidecarKeygen{
		round1Resp: FrostSidecarDKGRound1Resp{Commitments: "local-commitment"},
		round2Resp: FrostSidecarDKGRound2Resp{
			EncryptedShares: map[string]string{
				localIndexKey: "local-share",
			},
		},
		round3Resp: FrostSidecarDKGRound3Resp{
			Address:  groupAddress,
			GroupKey: groupKey,
		},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: false},
		chainHeight:  123,
	}

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id-finalize-fail", newFrostKeygenInbox(), time.Second)
	coord.sidecar = sidecar

	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-vault",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, nil)

	if resp.Status != tcommon.Fail {
		t.Fatalf("expected fail status when finalize is unsuccessful, got %d", resp.Status)
	}
	if len(sidecar.finalizeReqs) != 1 {
		t.Fatalf("expected one finalize call, got %d", len(sidecar.finalizeReqs))
	}
}

func TestFrostKeygenExecuteFailsWithoutPositiveMoneroHeight(t *testing.T) {
	members := []string{testPubKeys[0]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)

	sidecar := &mockFrostSidecarKeygen{
		round1Resp: FrostSidecarDKGRound1Resp{Commitments: "local-commitment"},
		round2Resp: FrostSidecarDKGRound2Resp{
			EncryptedShares: map[string]string{
				localIndexKey: "local-share",
			},
		},
		round3Resp: FrostSidecarDKGRound3Resp{
			Address:  groupAddress,
			GroupKey: groupKey,
		},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
		chainErr:     errors.New("height unavailable"),
	}

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id-height-fail", newFrostKeygenInbox(), time.Second)
	coord.sidecar = sidecar

	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-vault",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, nil)

	if resp.Status != tcommon.Fail {
		t.Fatalf("expected fail status without positive height, got %d", resp.Status)
	}
	if len(sidecar.finalizeReqs) != 0 {
		t.Fatalf("expected finalize not to run without positive height, got %d calls", len(sidecar.finalizeReqs))
	}
}

func TestFrostKeygenExecuteFailsWithInvalidFinalizeKeyBytes(t *testing.T) {
	members := []string{testPubKeys[0]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}

	localIndex := participantIndexMap[normalizePubKey(testPubKeys[0])]
	localIndexKey := strconv.FormatUint(uint64(localIndex), 10)
	groupAddress := "48xmrvaultaddress"
	groupKey := strings.Repeat("ab", 32)

	sidecar := &mockFrostSidecarKeygen{
		round1Resp: FrostSidecarDKGRound1Resp{Commitments: "local-commitment"},
		round2Resp: FrostSidecarDKGRound2Resp{
			EncryptedShares: map[string]string{
				localIndexKey: "local-share",
			},
		},
		round3Resp: FrostSidecarDKGRound3Resp{
			Address:  groupAddress,
			GroupKey: groupKey,
		},
		finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "not-hex"},
		chainHeight:  123,
	}

	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id-keybytes-fail", newFrostKeygenInbox(), time.Second)
	coord.sidecar = sidecar

	resp := coord.Execute(FrostKeygenRequest{
		VaultPubKey:   "thorpub-vault",
		SignerPubKeys: members,
		ExpectedAddr:  groupAddress,
	}, activeSet, nil)

	if resp.Status != tcommon.Fail {
		t.Fatalf("expected fail status for invalid key bytes, got %d", resp.Status)
	}
	if len(sidecar.finalizeReqs) != 1 {
		t.Fatalf("expected one finalize call, got %d", len(sidecar.finalizeReqs))
	}
}
