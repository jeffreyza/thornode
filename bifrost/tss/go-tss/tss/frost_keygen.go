package tss

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const (
	frostKeygenMsgIDDomain = "thorchain-frost-keygen-msgid-v1"
	frostDKGContextDomain  = "thorchain-frost-dkg-context-v1"
	frostDKGReorgMargin    = int64(10)

	frostDKGRound1 = "frost_dkg_round1"
	frostDKGRound2 = "frost_dkg_round2"
	frostDKGRound3 = "frost_dkg_round3"
	frostDKGResult = "frost_dkg_result"
)

// frostKeygenInbox keeps phase skew from turning into message loss. Payloads
// and terminal results remain on bounded per-topic channels until their phase;
// transcript votes are routed into the verifier for their tagged round. Each
// topic has its own FrostSessionQueueSize buffer and transport limits.
type frostKeygenInbox struct {
	round1 chan *p2p.Message
	round2 chan *p2p.Message
	round3 chan *p2p.Message
	verify chan *p2p.Message
	result chan *p2p.Message
}

// frostKeygenSubscription binds one wire topic to its phase-specific inbox.
// FrostKeygen materializes this table once and reuses it for registration,
// peer updates, and cancellation so those lifecycle operations cannot drift.
type frostKeygenSubscription struct {
	msgType         messages.THORChainTSSMessageType
	messageChan     chan *p2p.Message
	maxPayloadBytes int
}

func newFrostKeygenInbox() *frostKeygenInbox {
	return &frostKeygenInbox{
		round1: make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		round2: make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		round3: make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		verify: make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		result: make(chan *p2p.Message, p2p.FrostSessionQueueSize),
	}
}

// subscriptions is the single declaration of the keygen topic-to-inbox
// mapping. Callers should retain and reuse the returned table for the complete
// subscription lifecycle rather than reconstructing message-type lists.
func (i *frostKeygenInbox) subscriptions() []frostKeygenSubscription {
	return []frostKeygenSubscription{
		{msgType: messages.TSSFrostDKGRound1Msg, messageChan: i.round1, maxPayloadBytes: p2p.FrostKeygenControlPayload},
		{msgType: messages.TSSFrostDKGRound2Msg, messageChan: i.round2, maxPayloadBytes: p2p.FrostMaxSessionPayload},
		{msgType: messages.TSSFrostDKGRound3Msg, messageChan: i.round3, maxPayloadBytes: p2p.FrostKeygenControlPayload},
		{msgType: messages.TSSFrostVerifyMsg, messageChan: i.verify, maxPayloadBytes: p2p.FrostKeygenControlPayload},
		{msgType: messages.TSSFrostDKGResultMsg, messageChan: i.result, maxPayloadBytes: p2p.FrostKeygenControlPayload},
	}
}

func frostKeygenRoundOrder(round string) int {
	switch strings.TrimSpace(round) {
	case frostDKGRound1:
		return 1
	case frostDKGRound2:
		return 2
	case frostDKGRound3:
		return 3
	default:
		return 0
	}
}

type frostKeygenRoundState struct {
	verifiers map[string]*frostKeygenRoundVerifier
}

func newFrostKeygenRoundState(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
) (*frostKeygenRoundState, error) {
	state := &frostKeygenRoundState{
		verifiers: make(map[string]*frostKeygenRoundVerifier, 3),
	}
	for _, round := range []string{frostDKGRound1, frostDKGRound2, frostDKGRound3} {
		verifier, err := newFrostKeygenRoundVerifier(round, activeSet, participantIndexMap)
		if err != nil {
			return nil, fmt.Errorf("initialize %s transcript verifier: %w", round, err)
		}
		state.verifiers[round] = verifier
	}
	return state, nil
}

func (s *frostKeygenRoundState) verifier(round string) (*frostKeygenRoundVerifier, error) {
	if s == nil {
		return nil, fmt.Errorf("frost keygen round state is not initialized")
	}
	verifier := s.verifiers[round]
	if verifier == nil {
		return nil, fmt.Errorf("unsupported frost keygen round %q", round)
	}
	return verifier, nil
}

type FrostKeygenCoordinator struct {
	logger                    zerolog.Logger
	p2pCommunication          *p2p.Communication
	sidecar                   FrostSidecarKeygenClient
	localPubKey               string
	msgID                     string
	inbox                     *frostKeygenInbox
	roundState                *frostKeygenRoundState
	timeout                   time.Duration
	loggedVerificationDiscard bool
}

// minFrostKeygenTimeout is the minimum per-round wait the FROST DKG
// coordinator will use, regardless of what the caller passes in. FROST DKG is
// a 3-round p2p protocol with inter-round libp2p message exchange and no
// retransmits, so the timeout needs to accommodate a full round trip across
// all active peers. 60s is a conservative floor; production configs should
// set this explicitly to something larger.
const minFrostKeygenTimeout = 60 * time.Second

func NewFrostKeygenCoordinator(
	comm *p2p.Communication,
	sidecar FrostSidecarKeygenClient,
	localPubKey string,
	msgID string,
	inbox *frostKeygenInbox,
	timeout time.Duration,
) *FrostKeygenCoordinator {
	if timeout < minFrostKeygenTimeout {
		timeout = minFrostKeygenTimeout
	}
	if inbox == nil {
		inbox = newFrostKeygenInbox()
	}
	return &FrostKeygenCoordinator{
		logger:           log.With().Str("module", "frost_keygen").Str("msg_id", msgID).Logger(),
		p2pCommunication: comm,
		sidecar:          sidecar,
		localPubKey:      strings.TrimSpace(localPubKey),
		msgID:            strings.TrimSpace(msgID),
		inbox:            inbox,
		timeout:          timeout,
	}
}

// verificationDiscardLog warns once per coordinator and keeps subsequent
// diagnostics at Debug. Only the sequential round receive loops access the flag;
// transport budgets still bound discarded traffic regardless of logging level.
func (c *FrostKeygenCoordinator) verificationDiscardLog() *zerolog.Event {
	if !c.loggedVerificationDiscard {
		c.loggedVerificationDiscard = true
		return c.logger.Warn()
	}
	return c.logger.Debug()
}

// routeVerificationMessage retains valid current/future confirmations, but
// discards malformed or unauthorized votes without vetoing an otherwise valid
// quorum. A later valid vote from that peer may still count. Transport preflight
// already charged the frame; discarding here does not refund its ingress budget.
func (c *FrostKeygenCoordinator) routeVerificationMessage(
	msg *p2p.Message,
	currentRound string,
) *FrostKeygenResponse {
	if msg == nil {
		return nil
	}
	wrapped, ok := parseWrappedMessage(msg.Payload)
	if !ok || wrapped.MessageType != messages.TSSFrostVerifyMsg || strings.TrimSpace(wrapped.MsgID) != c.msgID {
		return nil
	}
	vote, err := decodeFrostKeygenTranscriptVote(wrapped.Payload)
	if err != nil {
		c.verificationDiscardLog().Err(err).Str("peer_id", msg.PeerID.String()).
			Str("round", currentRound).Msg("discarding invalid keygen transcript verification payload")
		return nil
	}

	incomingOrder := frostKeygenRoundOrder(vote.Round)
	currentOrder := frostKeygenRoundOrder(currentRound)
	if incomingOrder == 0 || currentOrder == 0 || incomingOrder < currentOrder {
		return nil
	}
	incomingRound := vote.Round
	verifier, err := c.roundState.verifier(incomingRound)
	if err != nil {
		response := c.failForPubKeys(err.Error(), currentRound, nil)
		return &response
	}

	// The libp2p stream authenticates msg.PeerID, so the wire vote only needs the
	// round and canonical transcript hash; it cannot nominate another attester.
	attesterPubKey, err := conversion.GetPubKeyFromPeerID(msg.PeerID.String())
	if err != nil {
		c.verificationDiscardLog().Err(err).Str("peer_id", msg.PeerID.String()).
			Str("round", incomingRound).Msg("discarding keygen verification with unresolved attester")
		return nil
	}
	if _, failure := verifier.recordTranscriptVote(attesterPubKey, vote.TranscriptHash); failure != nil {
		c.verificationDiscardLog().Str("peer_id", msg.PeerID.String()).Str("round", incomingRound).
			Str("reason", failure.reason).Msg("discarding invalid keygen transcript verification vote")
	}
	return nil
}

// validateRoundPayloadSender centralizes the transport identity and participant
// index checks shared by all three DKG payload rounds. Round-specific semantic
// validation stays in each round driver where reviewers can see it directly.
func (c *FrostKeygenCoordinator) validateRoundPayloadSender(
	msgPeer peer.ID,
	round string,
	senderPubKey string,
	participantIndex uint32,
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
) (string, *FrostKeygenResponse) {
	roundLabel := strings.TrimPrefix(round, "frost_dkg_")
	senderNorm := normalizePubKey(senderPubKey)
	if senderNorm == "" {
		response := c.failForMalformed(msgPeer, fmt.Sprintf("empty sender pubkey in %s payload", roundLabel), round)
		return "", &response
	}
	// Authenticate the claimed key before using it for membership checks or blame.
	if err := c.validateSenderPeerBinding(msgPeer, senderPubKey); err != nil {
		response := c.failForMalformed(msgPeer, err.Error(), round)
		return "", &response
	}
	if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
		response := c.failForPubKeys(
			fmt.Sprintf("%s sender is not in active participant set", roundLabel),
			round,
			[]string{senderPubKey},
		)
		return "", &response
	}
	if expectedIndex, ok := participantIndexMap[senderNorm]; !ok || participantIndex != expectedIndex {
		response := c.failForPubKeys(
			fmt.Sprintf("participant index mismatch in %s payload", roundLabel),
			round,
			[]string{senderPubKey},
		)
		return "", &response
	}
	return senderNorm, nil
}

// recordAndConfirmTranscriptPayload applies the identical transcript-state
// transition for local and remote payloads in every DKG round. Once all
// authenticated payloads are present, it broadcasts exactly one local vote for
// their canonical digest. The second return value marks the transition into the
// confirmation phase so the caller can grant that bounded phase its own timer.
func (c *FrostKeygenCoordinator) recordAndConfirmTranscriptPayload(
	verifier *frostKeygenRoundVerifier,
	senderPubKey string,
	participantIndex uint32,
	payloadHash string,
	peers []peer.ID,
) (bool, bool, *FrostKeygenResponse) {
	isNewPayload, failure := verifier.recordPayload(senderPubKey, participantIndex, payloadHash)
	if failure != nil {
		response := c.failForPubKeys(failure.reason, verifier.round, failure.blamePubKey)
		return false, false, &response
	}
	if !isNewPayload {
		return false, false, nil
	}
	transcriptHash, ready, err := verifier.prepareTranscriptHash(c.msgID)
	if err != nil {
		response := c.failForPubKeys(
			fmt.Sprintf("failed to prepare transcript confirmation: %v", err),
			verifier.round,
			nil,
		)
		return false, false, &response
	}
	if !ready || verifier.localVoteSent {
		return true, false, nil
	}
	if _, failure := verifier.recordTranscriptVote(c.localPubKey, transcriptHash); failure != nil {
		response := c.failForPubKeys(failure.reason, verifier.round, failure.blamePubKey)
		return false, false, &response
	}
	votePayload, err := json.Marshal(frostKeygenTranscriptVote{
		Round:          verifier.round,
		TranscriptHash: transcriptHash,
	})
	if err != nil {
		response := c.failForPubKeys(fmt.Sprintf("failed to marshal transcript confirmation: %v", err), verifier.round, nil)
		return false, false, &response
	}
	verifier.localVoteSent = true
	if err := c.broadcast(messages.TSSFrostVerifyMsg, votePayload, peers); err != nil {
		response := c.failForPubKeys(fmt.Sprintf("failed to broadcast transcript confirmation: %v", err), verifier.round, nil)
		return false, false, &response
	}
	return true, true, nil
}

func resetFrostKeygenRoundTimer(timer *time.Timer, timeout time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(timeout)
}

func FrostKeygenMsgID(req FrostKeygenRequest) (string, error) {
	vault := strings.TrimSpace(req.VaultPubKey)
	if vault == "" {
		return "", fmt.Errorf("vault pubkey is required")
	}
	members, _, _, err := buildParticipantIndex(req.SignerPubKeys)
	if err != nil {
		return "", err
	}
	membersHash, err := tcommon.MsgToHashString([]byte(strings.Join(members, ",")))
	if err != nil {
		return "", fmt.Errorf("fail to hash signer membership: %w", err)
	}

	canonical := strings.Join([]string{
		"domain=" + frostKeygenMsgIDDomain,
		"vault_pub_key=" + vault,
		"block_height=" + strconv.FormatInt(req.BlockHeight, 10),
		"members_hash=" + membersHash,
		"expected_addr=" + strings.TrimSpace(req.ExpectedAddr),
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func FrostDKGContext(msgID string) (string, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return "", fmt.Errorf("msgID is required")
	}
	return tcommon.MsgToHashString([]byte(frostDKGContextDomain + "\n" + msgID))
}

func (t *TssServer) FrostKeygen(req FrostKeygenRequest, sidecar FrostSidecarKeygenClient) (FrostKeygenResponse, error) {
	if sidecar == nil {
		return FrostKeygenResponse{}, fmt.Errorf("frost sidecar keygen client is nil")
	}
	if len(req.SignerPubKeys) == 0 {
		return FrostKeygenResponse{}, fmt.Errorf("signer pub keys are empty")
	}

	msgID, err := FrostKeygenMsgID(req)
	if err != nil {
		return FrostKeygenResponse{}, fmt.Errorf("fail to compute frost keygen msgID: %w", err)
	}
	// Restrict DKG ingress to the declared participant set with the same hard
	// per-session budgets keysign uses. An unrestricted subscription accepts
	// frames from any authenticated peer with no byte or message ceiling.
	allowedPeerIDs, err := conversion.GetPeerIDsFromPubKeys(req.SignerPubKeys)
	if err != nil {
		return FrostKeygenResponse{}, fmt.Errorf("fail to resolve frost keygen session peers: %w", err)
	}

	inbox := newFrostKeygenInbox()
	subscriptions := inbox.subscriptions()
	sigChan := make(chan string, 2)
	verificationMessageLimit := p2p.FrostKeygenVerificationMessagesPerPeer(3)

	for _, subscription := range subscriptions {
		msgType := subscription.msgType
		messageLimit := p2p.FrostMaxMessagesPerPeer
		if msgType == messages.TSSFrostVerifyMsg {
			messageLimit = verificationMessageLimit
		}
		t.p2pCommunication.SetSubscribeWithPeers(
			msgType, msgID, subscription.messageChan, allowedPeerIDs,
			subscription.maxPayloadBytes, messageLimit,
		)
	}
	defer func() {
		for _, subscription := range subscriptions {
			t.p2pCommunication.CancelSubscribe(subscription.msgType, msgID)
		}
		t.p2pCommunication.ReleaseStream(msgID)
		t.partyCoordinator.ReleaseStream(msgID)
		close(sigChan)
	}()

	// FROST has no legacy leadless ceremony. Enter the leader protocol directly
	// so a missing THORChain version cannot prevent an otherwise valid join.
	onlinePeers, _, errJoinParty := t.partyCoordinator.JoinPartyWithLeader(
		msgID,
		req.BlockHeight,
		peerIDsToStrings(allowedPeerIDs),
		frostKeygenJoinThreshold(len(req.SignerPubKeys)),
		sigChan,
	)
	if errJoinParty != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(errJoinParty.Error(), nil),
		}, nil
	}

	activeParticipants, err := conversion.GetPubKeysFromPeerIDs(peerIDsToStrings(onlinePeers))
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("fail to resolve active participants", nil),
		}, nil
	}
	_, _, signerByNormalized, err := buildParticipantIndex(req.SignerPubKeys)
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}, nil
	}
	activeSet, err := buildActiveSet(activeParticipants, signerByNormalized)
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}, nil
	}
	if missing := missingSignersForFullParticipation(activeSet, signerByNormalized); len(missing) > 0 {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame("frost keygen requires all signers online", frostDKGRound1, missing),
		}, nil
	}

	allPeerIDs, peerErr := conversion.GetPeerIDsFromPubKeys(req.SignerPubKeys)
	if peerErr != nil {
		t.logger.Warn().Err(peerErr).Msg("fail to resolve all peer IDs for frost keygen result broadcast, fallback to active peers")
		allPeerIDs = onlinePeers
	}

	coordinator := NewFrostKeygenCoordinator(
		t.p2pCommunication,
		sidecar,
		t.localNodePubKey,
		msgID,
		inbox,
		t.conf.FrostKeygenTimeout,
	)

	isActive := false
	ourPeerID := t.p2pCommunication.GetLocalPeerID()
	for _, p := range onlinePeers {
		if p.String() == ourPeerID {
			isActive = true
			break
		}
	}

	// Wait for peer results at the on-chain supermajority threshold
	// (frostKeygenAdoptionQuorum mirrors HasSuperMajority), not the full set.
	// THORChain finalizes the vault once ceil(2n/3) members report the same
	// success, and after any local failure at most n-1 peers can have
	// succeeded, so a full-set wait can never be met - it times out and this
	// node votes a failure whose (blame-hashed) tss ID differs from the
	// success, which judgeLateSigner never releases: a completed DKG turned into
	// a permanent keygen jail. Both the non-active observer path and the
	// failed-local-execution path below use this same threshold; a non-active
	// waiter left at full-set quorum would re-introduce exactly that spurious
	// failure/jailing for its node class.
	adoptionQuorum := frostKeygenAdoptionQuorum(len(activeSet.originalByNormalized))
	if !isActive {
		t.logger.Info().Str("msg_id", msgID).Msg("not active FROST keygen member, waiting for result")
		waitResp, waitErr := coordinator.WaitForResult(activeParticipants, adoptionQuorum, coordinator.timeout+5*time.Second)
		if waitErr != nil {
			return FrostKeygenResponse{
				Status: tcommon.Fail,
				Blame:  blame.NewBlame(waitErr.Error(), nil),
			}, nil
		}
		if waitResp.Status == tcommon.Success {
			t.persistPeerAddressBook()
		}
		return waitResp, nil
	}

	resp := coordinator.Execute(req, activeSet, onlinePeers)
	if resp.Status == tcommon.Success {
		t.persistPeerAddressBook()
		if err := coordinator.BroadcastResult(resp, allPeerIDs); err != nil {
			t.logger.Warn().Err(err).Msg("fail to broadcast frost keygen result")
		}
		return resp, nil
	}

	// The local execution failed; wait for and adopt the quorum success at the
	// same supermajority threshold (see the comment above).
	waitResp, waitErr := coordinator.WaitForResult(activeParticipants, adoptionQuorum, coordinator.timeout+5*time.Second)
	if waitErr == nil && waitResp.Status == tcommon.Success {
		if adopted, ok := coordinator.AdoptQuorumSuccess(req, waitResp); ok {
			t.persistPeerAddressBook()
			t.logger.Info().
				Str("msg_id", msgID).
				Str("address", adopted.Address).
				Msg("local frost keygen failed but quorum succeeded; adopted result with locally finalized keyshare")
			if err := coordinator.BroadcastResult(adopted, allPeerIDs); err != nil {
				t.logger.Warn().Err(err).Msg("fail to broadcast adopted frost keygen result")
			}
			return adopted, nil
		}
		// This node cannot prove it holds a keyshare for the quorum vault, so
		// it must not report success; fall through to the local failure.
	}
	if err := coordinator.BroadcastResult(resp, allPeerIDs); err != nil {
		t.logger.Warn().Err(err).Msg("fail to broadcast local frost keygen failure result")
	}
	if waitErr == nil && waitResp.Status == tcommon.Fail && len(waitResp.Blame.BlameNodes) > 0 && len(resp.Blame.BlameNodes) == 0 {
		return waitResp, nil
	}
	return resp, nil
}

// frostKeygenAdoptionQuorum mirrors the on-chain supermajority rule
// (x/thorchain/types.HasSuperMajority): ceil(2/3 * total). Once that many
// members report the same keygen success, THORChain will finalize the vault,
// so adopting the result is the only outcome consistent with the network.
//
// For total < 3 this exceeds total-1 (e.g. total=2 -> quorum 2), so adoption
// after a local failure is intentionally unreachable: at most total-1 peers
// can have succeeded, and the vault likewise cannot finalize (it needs 2/2).
// Declining to adopt there matches the network - do not "relax" it into
// over-eager adoption.
func frostKeygenAdoptionQuorum(total int) int {
	if total <= 0 {
		return 0
	}
	quorum := total * 2 / 3
	if (total*2)%3 > 0 {
		quorum++
	}
	return quorum
}

// validateFinalizeKeyshare verifies a sidecar FrostDKGFinalize response proves
// this node holds a usable keyshare for the finalized vault, and returns the
// validated key-bytes hex. The direct keygen path (exchangeRound3) and the
// quorum-adoption path (AdoptQuorumSuccess) gate on this identical proof;
// sharing it keeps the two copies from drifting the day one side gains a check
// the other misses. Callers map the returned error to their own blame/bool
// result shape.
func validateFinalizeKeyshare(finalizeResp FrostSidecarDKGFinalizeResp) (string, error) {
	if !finalizeResp.Success {
		return "", errors.New("frost dkg finalize returned unsuccessful response")
	}
	keyBytesHex := strings.TrimSpace(finalizeResp.KeyBytesHex)
	if keyBytesHex == "" {
		return "", errors.New("frost dkg finalize returned empty key bytes")
	}
	if _, err := hex.DecodeString(keyBytesHex); err != nil {
		return "", errors.New("frost dkg finalize returned invalid key bytes hex")
	}
	return keyBytesHex, nil
}

func frostKeygenJoinThreshold(participantCount int) int {
	if participantCount <= 0 {
		return 0
	}
	// FROST vault keygen requires every signer to participate, unlike the
	// threshold-based ECDSA/EDDSA join-party path.
	return participantCount - 1
}

func (c *FrostKeygenCoordinator) Execute(
	req FrostKeygenRequest,
	activeSet *activeSignerSet,
	activePeerIDs []peer.ID,
) FrostKeygenResponse {
	if c.sidecar == nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost sidecar client is nil", nil),
		}
	}
	if c.msgID == "" {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost keygen msgID is empty", nil),
		}
	}
	_, participantIndexMap, originalByNormalized, err := buildParticipantIndex(req.SignerPubKeys)
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}

	localNorm := normalizePubKey(c.localPubKey)
	localIndex, ok := participantIndexMap[localNorm]
	if !ok {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in participant set", nil),
		}
	}
	if _, ok := activeSet.originalByNormalized[localNorm]; !ok {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in active signer set", nil),
		}
	}
	if missing := missingSignersForFullParticipation(activeSet, originalByNormalized); len(missing) > 0 {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame("frost keygen requires all signers online", frostDKGRound1, missing),
		}
	}
	c.roundState, err = newFrostKeygenRoundState(activeSet, participantIndexMap)
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("failed to initialize frost keygen transcript state: %v", err), nil),
		}
	}
	threshold, err := conversion.GetThreshold(len(activeSet.originalByNormalized))
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("failed to compute threshold: %v", err), nil),
		}
	}
	contextValue, err := FrostDKGContext(c.msgID)
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("failed to compute dkg context: %v", err), nil),
		}
	}

	round1Resp, err := c.sidecar.FrostDKGRound1(FrostSidecarDKGRound1Req{
		VaultPubKey:      req.VaultPubKey,
		Threshold:        uint32(threshold + 1),
		N:                uint32(len(activeSet.originalByNormalized)),
		ParticipantIndex: localIndex,
		Context:          contextValue,
		SessionID:        c.msgID,
	})
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost dkg round1 failed: %v", err), nil),
		}
	}

	localIndexStr := strconv.FormatUint(uint64(localIndex), 10)
	singleParticipant := len(activeSet.originalByNormalized) == 1
	localRound1 := FrostDKGRound1Payload{
		SenderPubKey:     c.localPubKey,
		ParticipantIndex: localIndex,
		Commitments:      round1Resp.Commitments,
	}

	commitments := map[string]string{
		localIndexStr: strings.TrimSpace(localRound1.Commitments),
	}
	if !singleParticipant {
		var failResp *FrostKeygenResponse
		commitments, failResp = c.exchangeRound1(
			activeSet,
			participantIndexMap,
			activePeerIDs,
			localRound1,
		)
		if failResp != nil {
			return *failResp
		}
	}

	round2Resp, err := c.sidecar.FrostDKGRound2(FrostSidecarDKGRound2Req{
		SessionID:      c.msgID,
		AllCommitments: commitments,
	})
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost dkg round2 failed: %v", err), nil),
		}
	}

	encryptedSharesBySigner := map[string]map[string]string{
		localIndexStr: round2Resp.EncryptedShares,
	}
	if !singleParticipant {
		var failResp *FrostKeygenResponse
		encryptedSharesBySigner, failResp = c.exchangeRound2(
			activeSet,
			participantIndexMap,
			activePeerIDs,
			FrostDKGRound2Payload{
				SenderPubKey:     c.localPubKey,
				ParticipantIndex: localIndex,
				EncryptedShares:  round2Resp.EncryptedShares,
			},
		)
		if failResp != nil {
			return *failResp
		}
	}

	allShares := make(map[string]string, len(encryptedSharesBySigner))
	for senderIndex, senderShares := range encryptedSharesBySigner {
		if senderIndex == localIndexStr {
			continue
		}
		share := strings.TrimSpace(senderShares[localIndexStr])
		if share == "" {
			pubKey := senderIndex
			for norm, idx := range participantIndexMap {
				if strconv.FormatUint(uint64(idx), 10) == senderIndex {
					pubKey = originalByNormalized[norm]
					break
				}
			}
			return c.failForPubKeys("missing encrypted share for local participant", frostDKGRound2, []string{pubKey})
		}
		allShares[senderIndex] = share
	}

	round3Resp, err := c.sidecar.FrostDKGRound3(FrostSidecarDKGRound3Req{
		SessionID: c.msgID,
		AllShares: allShares,
	})
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost dkg round3 failed: %v", err), nil),
		}
	}

	var localMoneroHeight int64
	if h, hErr := c.sidecar.GetChainHeight(); hErr != nil {
		c.logger.Warn().Err(hErr).Msg("failed to query local Monero chain height for DKG birthday")
	} else {
		localMoneroHeight = h
	}

	address := strings.TrimSpace(round3Resp.Address)
	groupKey := strings.TrimSpace(round3Resp.GroupKey)
	// A single signer has no peers to derive a quorum median from, but the
	// birthday still has to clear the reorg margin the multi-party path applies
	// in deriveFrostDKGBirthday — otherwise a reorg can orphan the birthday
	// block and the vault scans from a height that no longer exists.
	birthdayHeight := localMoneroHeight - frostDKGReorgMargin
	if address == "" || groupKey == "" {
		return c.failForPubKeys("empty frost keygen result", frostDKGRound3, []string{c.localPubKey})
	}
	if !singleParticipant {
		var failResp *FrostKeygenResponse
		address, groupKey, birthdayHeight, failResp = c.exchangeRound3(
			activeSet,
			participantIndexMap,
			activePeerIDs,
			FrostDKGRound3Payload{
				SenderPubKey:         c.localPubKey,
				ParticipantIndex:     localIndex,
				Address:              round3Resp.Address,
				GroupKey:             round3Resp.GroupKey,
				ObservedMoneroHeight: localMoneroHeight,
			},
		)
		if failResp != nil {
			return *failResp
		}
	}
	if birthdayHeight <= 0 {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame("frost keygen result has no positive observed monero height", frostDKGRound3, nil),
		}
	}

	expectedAddress := strings.TrimSpace(req.ExpectedAddr)
	if expectedAddress != "" && expectedAddress != address {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame("frost keygen result does not match expected address", frostDKGRound3, nil),
		}
	}

	finalizeResp, err := c.sidecar.FrostDKGFinalize(FrostSidecarDKGFinalizeReq{
		SessionID: c.msgID,
		GroupKey:  groupKey,
		Address:   address,
	})
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost dkg finalize failed: %v", err), nil),
		}
	}
	keyBytesHex, err := validateFinalizeKeyshare(finalizeResp)
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame(err.Error(), frostDKGRound3, nil),
		}
	}

	return FrostKeygenResponse{
		Address:              address,
		GroupKey:             groupKey,
		KeyBytesHex:          keyBytesHex,
		ObservedMoneroHeight: birthdayHeight,
		Status:               tcommon.Success,
		Blame:                blame.Blame{},
	}
}

func (c *FrostKeygenCoordinator) exchangeRound1(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostDKGRound1Payload,
) (values map[string]string, failure *FrostKeygenResponse) {
	verifier, verifierErr := c.roundState.verifier(frostDKGRound1)
	if verifierErr != nil {
		resp := c.failForPubKeys(verifierErr.Error(), frostDKGRound1, nil)
		return nil, &resp
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to marshal round1 payload: %v", err), frostDKGRound1, nil)
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostDKGRound1Msg, payload, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast round1 payload: %v", err), frostDKGRound1, nil)
		return nil, &resp
	}
	localHash, err := FrostTranscriptDKGRound1Hash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local round1 payload: %v", err), frostDKGRound1, []string{local.SenderPubKey})
		return nil, &resp
	}
	if _, _, response := c.recordAndConfirmTranscriptPayload(
		verifier,
		local.SenderPubKey,
		local.ParticipantIndex,
		localHash,
		activePeerIDs,
	); response != nil {
		return nil, response
	}

	values = map[string]string{
		strconv.FormatUint(uint64(local.ParticipantIndex), 10): strings.TrimSpace(local.Commitments),
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.inbox.round1:
			if msg == nil {
				continue
			}
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostDKGRound1Msg:
				var incoming FrostDKGRound1Payload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid round1 payload", frostDKGRound1)
					return nil, &resp
				}
				if _, response := c.validateRoundPayloadSender(
					msg.PeerID,
					frostDKGRound1,
					incoming.SenderPubKey,
					incoming.ParticipantIndex,
					activeSet,
					participantIndexMap,
				); response != nil {
					return nil, response
				}
				if strings.TrimSpace(incoming.Commitments) == "" {
					resp := c.failForPubKeys("empty commitments in round1 payload", frostDKGRound1, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				payloadHash, err := FrostTranscriptDKGRound1Hash(c.msgID, incoming)
				if err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to hash round1 payload: %v", err), frostDKGRound1, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				isNewPayload, startedConfirmation, response := c.recordAndConfirmTranscriptPayload(
					verifier,
					incoming.SenderPubKey,
					incoming.ParticipantIndex,
					payloadHash,
					activePeerIDs,
				)
				if response != nil {
					return nil, response
				}
				if startedConfirmation {
					resetFrostKeygenRoundTimer(timer, c.timeout)
				}
				if !isNewPayload {
					continue
				}
				values[strconv.FormatUint(uint64(incoming.ParticipantIndex), 10)] = incoming.Commitments
			}
		case msg := <-c.inbox.verify:
			if response := c.routeVerificationMessage(msg, frostDKGRound1); response != nil {
				return nil, response
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			verificationGaps := verifier.verificationGaps()
			blamePubKeys := mergePubKeyLists(
				missingPayloads,
				verificationGaps.missingPubKeys,
				verificationGaps.equivocatingPubKeys,
			)
			resp := c.failForPubKeys(
				frostKeygenTranscriptTimeoutReason("round1 commitments", missingPayloads, verificationGaps),
				frostDKGRound1,
				blamePubKeys,
			)
			return nil, &resp
		}
	}
	return values, nil
}

func (c *FrostKeygenCoordinator) exchangeRound2(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostDKGRound2Payload,
) (values map[string]map[string]string, failure *FrostKeygenResponse) {
	verifier, verifierErr := c.roundState.verifier(frostDKGRound2)
	if verifierErr != nil {
		resp := c.failForPubKeys(verifierErr.Error(), frostDKGRound2, nil)
		return nil, &resp
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to marshal round2 payload: %v", err), frostDKGRound2, nil)
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostDKGRound2Msg, payload, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast round2 payload: %v", err), frostDKGRound2, nil)
		return nil, &resp
	}
	localHash, err := FrostTranscriptDKGRound2Hash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local round2 payload: %v", err), frostDKGRound2, []string{local.SenderPubKey})
		return nil, &resp
	}
	if _, _, response := c.recordAndConfirmTranscriptPayload(
		verifier,
		local.SenderPubKey,
		local.ParticipantIndex,
		localHash,
		activePeerIDs,
	); response != nil {
		return nil, response
	}

	localSenderIndex := strconv.FormatUint(uint64(local.ParticipantIndex), 10)
	values = map[string]map[string]string{localSenderIndex: local.EncryptedShares}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.inbox.round2:
			if msg == nil {
				continue
			}
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostDKGRound2Msg:
				var incoming FrostDKGRound2Payload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid round2 payload", frostDKGRound2)
					return nil, &resp
				}
				if _, response := c.validateRoundPayloadSender(
					msg.PeerID,
					frostDKGRound2,
					incoming.SenderPubKey,
					incoming.ParticipantIndex,
					activeSet,
					participantIndexMap,
				); response != nil {
					return nil, response
				}
				if len(incoming.EncryptedShares) == 0 && len(activeSet.originalByNormalized) > 1 {
					resp := c.failForPubKeys("empty encrypted shares in round2 payload", frostDKGRound2, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				for norm, idx := range participantIndexMap {
					if _, ok := activeSet.originalByNormalized[norm]; !ok {
						continue
					}
					if idx == incoming.ParticipantIndex {
						continue
					}
					idxKey := strconv.FormatUint(uint64(idx), 10)
					if strings.TrimSpace(incoming.EncryptedShares[idxKey]) == "" {
						resp := c.failForPubKeys("missing encrypted share target in round2 payload", frostDKGRound2, []string{incoming.SenderPubKey})
						return nil, &resp
					}
				}
				payloadHash, err := FrostTranscriptDKGRound2Hash(c.msgID, incoming)
				if err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to hash round2 payload: %v", err), frostDKGRound2, []string{incoming.SenderPubKey})
					return nil, &resp
				}
				isNewPayload, startedConfirmation, response := c.recordAndConfirmTranscriptPayload(
					verifier,
					incoming.SenderPubKey,
					incoming.ParticipantIndex,
					payloadHash,
					activePeerIDs,
				)
				if response != nil {
					return nil, response
				}
				if startedConfirmation {
					resetFrostKeygenRoundTimer(timer, c.timeout)
				}
				if !isNewPayload {
					continue
				}
				senderIndex := strconv.FormatUint(uint64(incoming.ParticipantIndex), 10)
				values[senderIndex] = incoming.EncryptedShares
			}
		case msg := <-c.inbox.verify:
			if response := c.routeVerificationMessage(msg, frostDKGRound2); response != nil {
				return nil, response
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			verificationGaps := verifier.verificationGaps()
			blamePubKeys := mergePubKeyLists(
				missingPayloads,
				verificationGaps.missingPubKeys,
				verificationGaps.equivocatingPubKeys,
			)
			resp := c.failForPubKeys(
				frostKeygenTranscriptTimeoutReason("round2 encrypted shares", missingPayloads, verificationGaps),
				frostDKGRound2,
				blamePubKeys,
			)
			return nil, &resp
		}
	}
	return values, nil
}

func (c *FrostKeygenCoordinator) exchangeRound3(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostDKGRound3Payload,
) (resultAddress, resultGroupKey string, birthday int64, failure *FrostKeygenResponse) {
	verifier, verifierErr := c.roundState.verifier(frostDKGRound3)
	if verifierErr != nil {
		resp := c.failForPubKeys(verifierErr.Error(), frostDKGRound3, nil)
		return "", "", 0, &resp
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to marshal round3 payload: %v", err), frostDKGRound3, nil)
		return "", "", 0, &resp
	}
	if err := c.broadcast(messages.TSSFrostDKGRound3Msg, payload, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast round3 payload: %v", err), frostDKGRound3, nil)
		return "", "", 0, &resp
	}

	groupAddress := strings.TrimSpace(local.Address)
	groupKey := strings.TrimSpace(local.GroupKey)
	heightsBySender := map[string]int64{
		normalizePubKey(local.SenderPubKey): local.ObservedMoneroHeight,
	}
	if groupAddress == "" || groupKey == "" {
		resp := c.failForPubKeys("empty address/group key in local round3 payload", frostDKGRound3, []string{local.SenderPubKey})
		return "", "", 0, &resp
	}
	if strings.TrimSpace(groupAddress) == "" {
		resp := c.failForPubKeys("empty DKG address", frostDKGRound3, []string{local.SenderPubKey})
		return "", "", 0, &resp
	}
	localHash, err := FrostTranscriptDKGRound3Hash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local round3 payload: %v", err), frostDKGRound3, []string{local.SenderPubKey})
		return "", "", 0, &resp
	}
	if _, _, response := c.recordAndConfirmTranscriptPayload(
		verifier,
		local.SenderPubKey,
		local.ParticipantIndex,
		localHash,
		activePeerIDs,
	); response != nil {
		return "", "", 0, response
	}
	// Test-only fault injection for the keygen recovery simulation: shrink only
	// the round-3 wait after sending the payload peers need to finish. The
	// simulation requires this timeout and locally finalized quorum adoption.
	// Non-mocknet builds use the constant-zero stub.
	round3Timeout := c.timeout
	if ms := frostKeygenTestRound3TimeoutMS(); ms > 0 {
		round3Timeout = time.Duration(ms) * time.Millisecond
		c.logger.Warn().
			Dur("round3_timeout", round3Timeout).
			Msg("TEST override active: XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS shrinks the round-3 wait")
	}
	timer := time.NewTimer(round3Timeout)
	defer timer.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.inbox.round3:
			if msg == nil {
				continue
			}
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			switch wrapped.MessageType {
			case messages.TSSFrostDKGRound3Msg:
				var incoming FrostDKGRound3Payload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					resp := c.failForMalformed(msg.PeerID, "invalid round3 payload", frostDKGRound3)
					return "", "", 0, &resp
				}
				senderNorm, response := c.validateRoundPayloadSender(
					msg.PeerID,
					frostDKGRound3,
					incoming.SenderPubKey,
					incoming.ParticipantIndex,
					activeSet,
					participantIndexMap,
				)
				if response != nil {
					return "", "", 0, response
				}

				inAddress := strings.TrimSpace(incoming.Address)
				inGroupKey := strings.TrimSpace(incoming.GroupKey)
				if inAddress == "" || inGroupKey == "" {
					resp := c.failForPubKeys("empty address/group key in round3 payload", frostDKGRound3, []string{incoming.SenderPubKey})
					return "", "", 0, &resp
				}
				if groupAddress != inAddress {
					resp := c.failForPubKeys("DKG address mismatch across participants", frostDKGRound3, []string{incoming.SenderPubKey})
					return "", "", 0, &resp
				}
				if !strings.EqualFold(groupKey, inGroupKey) {
					resp := c.failForPubKeys("DKG group key mismatch across participants", frostDKGRound3, []string{incoming.SenderPubKey})
					return "", "", 0, &resp
				}
				payloadHash, err := FrostTranscriptDKGRound3Hash(c.msgID, incoming)
				if err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to hash round3 payload: %v", err), frostDKGRound3, []string{incoming.SenderPubKey})
					return "", "", 0, &resp
				}
				isNewPayload, startedConfirmation, response := c.recordAndConfirmTranscriptPayload(
					verifier,
					incoming.SenderPubKey,
					incoming.ParticipantIndex,
					payloadHash,
					activePeerIDs,
				)
				if response != nil {
					return "", "", 0, response
				}
				if startedConfirmation {
					resetFrostKeygenRoundTimer(timer, round3Timeout)
				}
				if !isNewPayload {
					continue
				}
				heightsBySender[senderNorm] = incoming.ObservedMoneroHeight
			}
		case msg := <-c.inbox.verify:
			if response := c.routeVerificationMessage(msg, frostDKGRound3); response != nil {
				return "", "", 0, response
			}
		case <-timer.C:
			// Emit fault evidence only when the mocknet test override is enabled.
			// Other builds use the constant-zero stub and emit no test event.
			if frostKeygenTestRound3TimeoutMS() > 0 {
				c.logger.Warn().Str("round", frostDKGRound3).
					Msg("TEST injected XMR FROST round-3 timeout")
			}
			missingPayloads := verifier.missingPayloadPubKeys()
			verificationGaps := verifier.verificationGaps()
			blamePubKeys := mergePubKeyLists(
				missingPayloads,
				verificationGaps.missingPubKeys,
				verificationGaps.equivocatingPubKeys,
			)
			resp := c.failForPubKeys(
				frostKeygenTranscriptTimeoutReason("round3 results", missingPayloads, verificationGaps),
				frostDKGRound3,
				blamePubKeys,
			)
			return "", "", 0, &resp
		}
	}
	birthday, err = frostDKGBirthdayHeight(heightsBySender, len(activeSet.originalByNormalized))
	if err != nil {
		resp := c.failForPubKeys(err.Error(), frostDKGRound3, nil)
		return "", "", 0, &resp
	}
	return groupAddress, groupKey, birthday, nil
}

// frostDKGBirthdayHeight derives one robust scanner birthday from the
// authenticated height vector. Zero is an abstention; a supermajority of
// positive reports is required so unavailable scanners do not block keygen
// and a minority of outliers cannot move the result outside the honest range.
func frostDKGBirthdayHeight(heightsBySender map[string]int64, expected int) (int64, error) {
	if expected <= 0 || len(heightsBySender) != expected {
		return 0, fmt.Errorf("cannot derive DKG birthday without every authenticated round3 payload")
	}
	heights := make([]int64, 0, len(heightsBySender))
	for _, height := range heightsBySender {
		if height < 0 {
			return 0, fmt.Errorf("cannot derive DKG birthday from negative height")
		}
		if height > 0 {
			heights = append(heights, height)
		}
	}
	requiredHeights := frostKeygenAdoptionQuorum(expected)
	if len(heights) < requiredHeights {
		return 0, fmt.Errorf("cannot derive DKG birthday without %d positive authenticated heights", requiredHeights)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	lowerMedian := heights[(len(heights)-1)/2]
	if lowerMedian <= frostDKGReorgMargin {
		return 0, fmt.Errorf("DKG birthday does not leave the required reorg margin")
	}
	return lowerMedian - frostDKGReorgMargin, nil
}

// AdoptQuorumSuccess turns a quorum keygen success into this node's own
// result after a failed local execution, if and only if the node can prove it
// holds a keyshare for exactly that vault: the sidecar finalize validates the
// stored DKG session key material against the adopted group key and address
// and returns the keyshare backup. This heals the timing-skew split where a
// round timer expires moments before the last peer message and a completed
// DKG would otherwise be reported as a keygen failure - whose tss ID differs
// from the success, jailing this node with no path to release. A node whose
// DKG session did not complete fails the finalize and keeps its failure
// result: a member without a keyshare must never report success.
func (c *FrostKeygenCoordinator) AdoptQuorumSuccess(req FrostKeygenRequest, quorumResp FrostKeygenResponse) (FrostKeygenResponse, bool) {
	address := strings.TrimSpace(quorumResp.Address)
	groupKey := strings.TrimSpace(quorumResp.GroupKey)
	if address == "" || groupKey == "" {
		c.logger.Warn().Msg("skip adopting quorum keygen success: empty address or group key")
		return FrostKeygenResponse{}, false
	}
	if req.ExpectedAddr != "" && address != req.ExpectedAddr {
		c.logger.Error().
			Str("expected", req.ExpectedAddr).
			Str("address", address).
			Msg("skip adopting quorum keygen success: address does not match expected")
		return FrostKeygenResponse{}, false
	}
	if quorumResp.ObservedMoneroHeight <= 0 {
		c.logger.Warn().Msg("skip adopting quorum keygen success: missing observed monero height")
		return FrostKeygenResponse{}, false
	}
	finalizeResp, err := c.sidecar.FrostDKGFinalize(FrostSidecarDKGFinalizeReq{
		SessionID: c.msgID,
		GroupKey:  groupKey,
		Address:   address,
	})
	if err != nil {
		c.logger.Warn().Err(err).Msg("cannot adopt quorum keygen success: local keyshare finalize failed")
		return FrostKeygenResponse{}, false
	}
	keyBytesHex, err := validateFinalizeKeyshare(finalizeResp)
	if err != nil {
		c.logger.Warn().Err(err).Msg("cannot adopt quorum keygen success: finalize keyshare proof invalid")
		return FrostKeygenResponse{}, false
	}
	// The observed monero height is adopted from the quorum result, not observed
	// independently by this node: the sidecar finalize proof binds the group key
	// and address but not the height, and the on-chain success vote's tss ID only
	// matches the finishers when the height matches theirs. Adopting their value
	// is therefore required, not optional - so this node attests to a height it
	// did not itself observe. The `> 0` guard above is the only local sanity
	// check; a stricter bound (height within a window of this node's last-seen
	// XMR height) would harden against a fully hostile quorum but cannot be
	// enforced here without breaking tss-ID agreement.
	return FrostKeygenResponse{
		Address:              address,
		GroupKey:             groupKey,
		KeyBytesHex:          keyBytesHex,
		ObservedMoneroHeight: quorumResp.ObservedMoneroHeight,
		Status:               tcommon.Success,
	}, true
}

func (c *FrostKeygenCoordinator) WaitForResult(activeParticipants []string, quorum int, timeout time.Duration) (FrostKeygenResponse, error) {
	_, _, activeByNormalized, err := buildParticipantIndex(activeParticipants)
	if err != nil {
		return FrostKeygenResponse{}, err
	}
	if quorum <= 0 {
		quorum = len(activeByNormalized)
	}
	if quorum <= 0 {
		return FrostKeygenResponse{}, fmt.Errorf("invalid quorum %d", quorum)
	}
	if quorum > len(activeByNormalized) {
		return FrostKeygenResponse{}, fmt.Errorf("invalid quorum %d: only %d active participants", quorum, len(activeByNormalized))
	}
	if timeout <= 0 {
		timeout = c.timeout
	}

	buckets := make(map[string]*dkgResultVoteBucket)
	// Last write wins per sender: an active sender re-voting with a different
	// result moves between buckets instead of inflating the map, so a single
	// hostile member can neither grow memory without bound nor hold votes in
	// two buckets at once.
	voteBySender := make(map[string]string)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg := <-c.inbox.result:
			if msg == nil {
				continue
			}
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok || wrapped.MessageType != messages.TSSFrostDKGResultMsg {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			// Invalid result messages are gossip noise and must never end the
			// wait: the msg ID is derivable by any connected peer, so a single
			// malformed or unauthorized message arriving before the genuine
			// quorum would otherwise veto the adoption path and every
			// non-active waiter. Mirror the keysign result waiter: log, skip,
			// and keep waiting for quorum or timeout.
			var payload FrostDKGResultPayload
			if err := json.Unmarshal(wrapped.Payload, &payload); err != nil {
				c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("skipping malformed frost dkg result payload")
				continue
			}

			senderNorm := normalizePubKey(payload.SenderPubKey)
			if senderNorm == "" {
				c.logger.Warn().Str("peer", msg.PeerID.String()).Msg("skipping frost dkg result with empty sender pubkey")
				continue
			}
			if _, ok := activeByNormalized[senderNorm]; !ok {
				c.logger.Warn().Str("sender", payload.SenderPubKey).Msg("skipping frost dkg result from non-active participant")
				continue
			}
			if err := c.validateSenderPeerBinding(msg.PeerID, payload.SenderPubKey); err != nil {
				c.logger.Warn().Err(err).Str("sender", payload.SenderPubKey).Msg("skipping frost dkg result with peer binding mismatch")
				continue
			}

			normalized, matchKey, err := normalizeDKGResultPayload(payload.Response)
			if err != nil {
				c.logger.Warn().Err(err).Str("sender", payload.SenderPubKey).Msg("skipping malformed frost dkg result")
				continue
			}
			prevKey, voted := voteBySender[senderNorm]
			if voted && prevKey == matchKey {
				continue
			}
			bucket := buckets[matchKey]
			if bucket == nil {
				bucket = &dkgResultVoteBucket{
					response: normalized,
					voters:   make(map[string]struct{}),
				}
				buckets[matchKey] = bucket
			}
			if voted {
				if previous := buckets[prevKey]; previous != nil {
					delete(previous.voters, senderNorm)
					if len(previous.voters) == 0 {
						delete(buckets, prevKey)
					}
				}
			}
			voteBySender[senderNorm] = matchKey
			bucket.voters[senderNorm] = struct{}{}
			if len(bucket.voters) >= quorum {
				return bucket.response, nil
			}
		// The caller never resumes DKG rounds after entering this waiter: an active
		// coordinator has reached a terminal local outcome, while an observer never
		// executes them. Keep consuming their dedicated queues anyway. Bounded p2p
		// delivery blocks on a full queue, so leaving stale traffic unread would park
		// stream-handler goroutines until quorum or the multi-minute session timeout.
		case <-c.inbox.round1:
		case <-c.inbox.round2:
		case <-c.inbox.round3:
		case <-c.inbox.verify:
		case <-timer.C:
			return FrostKeygenResponse{}, fmt.Errorf("timeout waiting for frost dkg result")
		}
	}
}

func (c *FrostKeygenCoordinator) BroadcastResult(resp FrostKeygenResponse, peers []peer.ID) error {
	result := FrostDKGResultPayload{
		SenderPubKey: c.localPubKey,
		Response: FrostDKGResult{
			Address:              resp.Address,
			GroupKey:             resp.GroupKey,
			ObservedMoneroHeight: resp.ObservedMoneroHeight,
			Status:               uint8(resp.Status),
			Blame:                resp.Blame,
		},
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal frost dkg result payload: %w", err)
	}
	return c.broadcast(messages.TSSFrostDKGResultMsg, payload, peers)
}

func (c *FrostKeygenCoordinator) broadcast(msgType messages.THORChainTSSMessageType, payload []byte, peers []peer.ID) error {
	c.p2pCommunication.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: messages.WrappedMessage{
			MessageType: msgType,
			MsgID:       c.msgID,
			Payload:     payload,
		},
		PeersID: peers,
	}
	return nil
}

func (c *FrostKeygenCoordinator) validateSenderPeerBinding(msgPeer peer.ID, senderPubKey string) error {
	expectedPeerID, err := conversion.GetPeerIDFromPubKey(senderPubKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender pubkey")
	}
	if expectedPeerID != msgPeer {
		return fmt.Errorf("sender peer binding mismatch")
	}
	return nil
}

func (c *FrostKeygenCoordinator) failForMalformed(msgPeer peer.ID, reason, round string) FrostKeygenResponse {
	pubKey, err := conversion.GetPubKeyFromPeerID(msgPeer.String())
	if err != nil {
		return FrostKeygenResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame(reason, round, nil),
		}
	}
	return c.failForPubKeys(reason, round, []string{pubKey})
}

func (c *FrostKeygenCoordinator) failForPubKeys(reason, round string, pubKeys []string) FrostKeygenResponse {
	return FrostKeygenResponse{
		Status: tcommon.Fail,
		Blame:  buildBlame(reason, round, pubKeys),
	}
}

type dkgResultVoteBucket struct {
	response FrostKeygenResponse
	voters   map[string]struct{}
}

func normalizeDKGResultPayload(result FrostDKGResult) (FrostKeygenResponse, string, error) {
	status := tcommon.Status(result.Status)
	if status != tcommon.Success && status != tcommon.Fail {
		return FrostKeygenResponse{}, "", fmt.Errorf("invalid frost dkg result status")
	}

	normalized := FrostKeygenResponse{
		Status: status,
	}
	switch status {
	case tcommon.Success:
		address := strings.TrimSpace(result.Address)
		groupKey := strings.TrimSpace(result.GroupKey)
		if address == "" {
			return FrostKeygenResponse{}, "", fmt.Errorf("frost dkg success result has empty address")
		}
		if groupKey == "" {
			return FrostKeygenResponse{}, "", fmt.Errorf("frost dkg success result has empty group key")
		}
		if result.ObservedMoneroHeight <= 0 {
			return FrostKeygenResponse{}, "", fmt.Errorf("frost dkg success result has no positive observed monero height")
		}
		normalized.Address = address
		normalized.GroupKey = strings.ToLower(groupKey)
		normalized.ObservedMoneroHeight = result.ObservedMoneroHeight
	case tcommon.Fail:
		normalizedBlame, err := normalizeBlameForResult(result.Blame)
		if err != nil {
			return FrostKeygenResponse{}, "", err
		}
		normalized.Blame = normalizedBlame
	}

	keyBytes, err := json.Marshal(struct {
		Status   tcommon.Status `json:"status"`
		Address  string         `json:"address"`
		GroupKey string         `json:"group_key"`
		Height   int64          `json:"observed_monero_height"`
		Blame    blame.Blame    `json:"blame"`
	}{
		Status:   normalized.Status,
		Address:  normalized.Address,
		GroupKey: normalized.GroupKey,
		Height:   normalized.ObservedMoneroHeight,
		Blame:    normalized.Blame,
	})
	if err != nil {
		return FrostKeygenResponse{}, "", fmt.Errorf("failed to encode frost dkg result comparison key")
	}
	return normalized, string(keyBytes), nil
}

func missingSignersForFullParticipation(activeSet *activeSignerSet, signerByNormalized map[string]string) []string {
	if activeSet == nil {
		missing := make([]string, 0, len(signerByNormalized))
		for _, pubKey := range signerByNormalized {
			missing = append(missing, pubKey)
		}
		sort.Strings(missing)
		return missing
	}

	missing := make([]string, 0)
	for norm, original := range signerByNormalized {
		if _, ok := activeSet.originalByNormalized[norm]; ok {
			continue
		}
		missing = append(missing, original)
	}
	sort.Strings(missing)
	return missing
}
