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

	tcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/common"
)

const (
	frostWireProtocolVersion = messages.FrostProtocolVersion
	// Batch-only and single-echo builds must derive different join IDs so they
	// cannot discover their wire incompatibility after releasing partial shares.
	frostMsgIDDomain             = "thorchain-frost-keysign-msgid-v7"
	frostDecoyContextDomain      = "thorchain-frost-decoy-context-v1"
	frostPlanHashDomain          = "thorchain-frost-eventuality-plan-v2"
	frostEventualityIDDomain     = "thorchain-frost-eventuality-id-v2"
	frostEventualityVersion      = uint8(2)
	frostRoundPreprocess         = messages.FROSTKEYSIGNPREPROCESS
	frostRoundShare              = messages.FROSTKEYSIGNSHARE
	frostRoundResult             = messages.FROSTKEYSIGNRESULT
	frostEventualityHashHexBytes = 64
	frostJoinAttemptDomain       = "thorchain-frost-keysign-join-attempt-v1"
	frostJoinRotationVersion     = "3.20.0"
	frostMaxJoinLeaderAttempts   = 3
	frostMaxResultTxHexChars     = 2 * 1024 * 1024
	frostMaxResultSpentRefs      = 4096
	frostMaxResultBlameNodes     = 128
	frostMaxResultReasonBytes    = 512
	frostMaxResultEvidenceBytes  = 64 * 1024
	frostEquivocationFailReason  = "frost signed envelope equivocation"
)

var errFrostResultTimeout = errors.New("timeout waiting for frost result")

// frostSignInbox keeps phase skew from turning into message loss. Payloads and
// terminal results remain on bounded per-topic channels until their consumer;
// future verification entries move to bounded round state without being
// semantically applied before their tagged phase.
type frostSignInbox struct {
	preprocess chan *p2p.Message
	share      chan *p2p.Message
	verify     chan *p2p.Message
	result     chan *p2p.Message
}

// frostSignSubscription is the total topic-to-inbox setup table for one
// keysign session. Registration, peer narrowing, and cancellation all derive
// from this table so no switch can panic or silently omit a topic.
type frostSignSubscription struct {
	msgType         messages.THORChainTSSMessageType
	messageChan     chan *p2p.Message
	narrowAfterJoin bool
}

func newFrostSignInbox() *frostSignInbox {
	return &frostSignInbox{
		preprocess: make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		share:      make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		verify:     make(chan *p2p.Message, p2p.FrostSessionQueueSize),
		result:     make(chan *p2p.Message, p2p.FrostSessionQueueSize),
	}
}

func (i *frostSignInbox) subscriptions() []frostSignSubscription {
	return []frostSignSubscription{
		{msgType: messages.TSSFrostPreprocessMsg, messageChan: i.preprocess, narrowAfterJoin: true},
		{msgType: messages.TSSFrostShareMsg, messageChan: i.share, narrowAfterJoin: true},
		{msgType: messages.TSSFrostVerifyMsg, messageChan: i.verify, narrowAfterJoin: true},
		{msgType: messages.TSSFrostResultMsg, messageChan: i.result},
	}
}

func (i *frostSignInbox) messageChannel(msgType messages.THORChainTSSMessageType) (chan *p2p.Message, bool) {
	if i == nil {
		return nil, false
	}
	for _, subscription := range i.subscriptions() {
		if subscription.msgType == msgType {
			return subscription.messageChan, true
		}
	}
	return nil, false
}

func frostSignRoundOrder(round string) int {
	switch strings.TrimSpace(round) {
	case frostRoundPreprocess:
		return 1
	case frostRoundShare:
		return 2
	default:
		return 0
	}
}

type frostDeferredVerification struct {
	msgPeer peer.ID
	payload FrostVerifyPayload
}

type frostSignRoundState struct {
	activePeers           map[peer.ID]struct{}
	verifiers             map[string]*frostTranscriptVerifier
	deferredVerifications map[string][]frostDeferredVerification
}

func newFrostSignRoundState(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	vaultPubKey string,
	network string,
) (*frostSignRoundState, error) {
	if activeSet == nil {
		return nil, fmt.Errorf("active signer set is empty")
	}
	state := &frostSignRoundState{
		activePeers:           make(map[peer.ID]struct{}, len(activeSet.originalByNormalized)),
		verifiers:             make(map[string]*frostTranscriptVerifier, 2),
		deferredVerifications: make(map[string][]frostDeferredVerification, 1),
	}
	// Subscription narrowing cannot remove already queued frames. Retain the
	// active peer set to reject those frames before decoding or phase routing.
	for _, pubKey := range activeSet.originalByNormalized {
		id, err := conversion.GetPeerIDFromPubKey(pubKey)
		if err != nil {
			return nil, fmt.Errorf("active signer peer: %w", err)
		}
		state.activePeers[id] = struct{}{}
	}
	for _, round := range []string{frostRoundPreprocess, frostRoundShare} {
		verifier, err := newFrostTranscriptVerifier(round, activeSet, participantIndexMap, vaultPubKey, network)
		if err != nil {
			return nil, fmt.Errorf("initialize %s transcript verifier: %w", round, err)
		}
		state.verifiers[round] = verifier
	}
	return state, nil
}

func (s *frostSignRoundState) verifier(round string) (*frostTranscriptVerifier, error) {
	if s == nil {
		return nil, fmt.Errorf("FROST keysign round state is not initialized")
	}
	verifier := s.verifiers[round]
	if verifier == nil {
		return nil, fmt.Errorf("unsupported FROST keysign round %q", round)
	}
	return verifier, nil
}

func (s *frostSignRoundState) deferVerification(
	round string,
	msgPeer peer.ID,
	payload FrostVerifyPayload,
) error {
	if _, err := s.verifier(round); err != nil {
		return err
	}
	if s.deferredVerifications == nil {
		s.deferredVerifications = make(map[string][]frostDeferredVerification, 1)
	}
	s.deferredVerifications[round] = append(
		s.deferredVerifications[round],
		frostDeferredVerification{msgPeer: msgPeer, payload: payload},
	)
	return nil
}

func (s *frostSignRoundState) takeDeferredVerifications(round string) ([]frostDeferredVerification, error) {
	if _, err := s.verifier(round); err != nil {
		return nil, err
	}
	deferred := s.deferredVerifications[round]
	delete(s.deferredVerifications, round)
	return deferred, nil
}

type FrostSignCoordinator struct {
	logger                    zerolog.Logger
	p2pCommunication          *p2p.Communication
	sidecar                   FrostSidecarClient
	localPubKey               string
	msgID                     string
	vaultPubKey               string
	network                   string
	expectedPlanHash          string
	inbox                     *frostSignInbox
	roundState                *frostSignRoundState
	verificationEntriesByPeer map[peer.ID]uint32
	verificationEntryLimit    uint32
	timeout                   time.Duration
	privateKey                tcrypto.PrivKey
	strictTranscript          bool
}

func NewFrostSignCoordinator(
	comm *p2p.Communication,
	sidecar FrostSidecarClient,
	localPubKey string,
	msgID string,
	expectedPlanHash string,
	inbox *frostSignInbox,
	timeout time.Duration,
	privateKey tcrypto.PrivKey,
) *FrostSignCoordinator {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	if inbox == nil {
		inbox = newFrostSignInbox()
	}
	return &FrostSignCoordinator{
		logger:                    log.With().Str("module", "frost_keysign").Str("msg_id", msgID).Logger(),
		p2pCommunication:          comm,
		sidecar:                   sidecar,
		localPubKey:               strings.TrimSpace(localPubKey),
		msgID:                     strings.TrimSpace(msgID),
		expectedPlanHash:          strings.TrimSpace(expectedPlanHash),
		inbox:                     inbox,
		verificationEntriesByPeer: make(map[peer.ID]uint32),
		timeout:                   timeout,
		privateKey:                privateKey,
		strictTranscript:          privateKey != nil,
	}
}

func (c *FrostSignCoordinator) routeVerificationMessage(
	msg *p2p.Message,
	currentRound string,
) *FrostKeySignResponse {
	if msg == nil || c.roundState == nil {
		return nil
	}
	if _, active := c.roundState.activePeers[msg.PeerID]; !active {
		return nil
	}
	wrapped, ok := parseWrappedMessage(msg.Payload)
	if !ok || wrapped.MessageType != messages.TSSFrostVerifyMsg || strings.TrimSpace(wrapped.MsgID) != c.msgID {
		return nil
	}
	entries, err := decodeFrostVerificationBatch(wrapped.Payload)
	if err != nil {
		c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring invalid FROST verification batch")
		return nil
	}
	for i := range entries {
		if err := c.reserveVerificationEntry(msg.PeerID); err != nil {
			c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring excess FROST verification entries")
			return nil
		}
		if response := c.routeVerificationPayload(msg.PeerID, currentRound, entries[i]); response != nil {
			return response
		}
	}
	return nil
}

func (c *FrostSignCoordinator) routeVerificationPayload(
	msgPeer peer.ID,
	currentRound string,
	incoming FrostVerifyPayload,
) *FrostKeySignResponse {
	incomingOrder := frostSignRoundOrder(incoming.Round)
	currentOrder := frostSignRoundOrder(currentRound)
	if incomingOrder == 0 || currentOrder == 0 || incomingOrder < currentOrder {
		return nil
	}
	incomingRound := strings.TrimSpace(incoming.Round)
	if incomingOrder > currentOrder {
		// The libp2p peer is authenticated and this logical entry is charged on
		// receipt. Binding the claimed sender to that peer, plus transcript
		// signatures, membership, and equivocation, are phase semantics. Deferring
		// them prevents an early share echo from changing preprocess state.
		if err := c.roundState.deferVerification(incomingRound, msgPeer, incoming); err != nil {
			response := c.failForPubKeys(err.Error(), currentRound, nil)
			return &response
		}
		return nil
	}
	verifier, err := c.roundState.verifier(incomingRound)
	if err != nil {
		response := c.failForPubKeys(err.Error(), currentRound, nil)
		return &response
	}
	return c.applyVerification(msgPeer, incomingRound, incoming, verifier)
}

func (c *FrostSignCoordinator) activateDeferredVerifications(round string) *FrostKeySignResponse {
	deferred, err := c.roundState.takeDeferredVerifications(round)
	if err != nil {
		response := c.failForPubKeys(err.Error(), round, nil)
		return &response
	}
	verifier, err := c.roundState.verifier(round)
	if err != nil {
		response := c.failForPubKeys(err.Error(), round, nil)
		return &response
	}
	for _, incoming := range deferred {
		if response := c.applyVerification(incoming.msgPeer, round, incoming.payload, verifier); response != nil {
			return response
		}
	}
	return nil
}

func (c *FrostSignCoordinator) drainQueuedVerifications(round string) *FrostKeySignResponse {
	if c == nil || c.inbox == nil || c.inbox.verify == nil {
		return nil
	}
	for {
		select {
		case msg, ok := <-c.inbox.verify:
			if !ok {
				return nil
			}
			if response := c.routeVerificationMessage(msg, round); response != nil {
				return response
			}
		default:
			return nil
		}
	}
}

func (c *FrostSignCoordinator) reserveVerificationEntry(msgPeer peer.ID) error {
	// Charge decoded entries before phase routing. Otherwise a peer could move
	// unbounded work out of the transport queue merely by tagging it as future.
	limit := c.verificationEntryLimit
	if limit == 0 {
		limit = p2p.FrostMaxMessagesPerPeer
	}
	if c.verificationEntriesByPeer == nil {
		c.verificationEntriesByPeer = make(map[peer.ID]uint32)
	}
	if c.verificationEntriesByPeer[msgPeer] >= limit {
		return fmt.Errorf("transcript verification entry limit exceeded")
	}
	c.verificationEntriesByPeer[msgPeer]++
	return nil
}

// applyVerification validates each keysign batch entry with signed-envelope
// and equivocation checks. Keygen uses separate whole-round transcript votes.
func (c *FrostSignCoordinator) applyVerification(
	msgPeer peer.ID,
	round string,
	incoming FrostVerifyPayload,
	verifier *frostTranscriptVerifier,
) *FrostKeySignResponse {
	attesterNorm := normalizePubKey(incoming.SenderPubKey)
	if attesterNorm == "" {
		return nil
	}
	if _, ok := verifier.activeSet.originalByNormalized[attesterNorm]; !ok {
		return nil
	}
	if err := c.validateSenderPeerBinding(msgPeer, incoming.SenderPubKey); err != nil {
		return nil
	}
	if c.strictTranscript {
		echo := frostEchoFromVerify(c.msgID, c.vaultPubKey, c.network, incoming)
		if err := verifyFrostSignedEcho(echo); err != nil {
			c.logger.Warn().Err(err).Str("peer", msgPeer.String()).Str("round", round).Msg("ignoring invalid signed FROST echo")
			return nil
		}
		evidence, err := verifier.recordSignedEnvelope(echo.Target)
		if err != nil {
			return nil
		}
		if evidence != nil {
			response := c.failForEquivocation(*evidence)
			return &response
		}
	}
	if _, failure := verifier.recordVerification(
		incoming.SenderPubKey,
		incoming.TargetSenderPubKey,
		incoming.TargetParticipantIndex,
		incoming.PayloadHash,
	); failure != nil {
		c.logger.Warn().Err(failure).Str("peer", msgPeer.String()).Str("round", round).Msg("ignoring invalid FROST verification")
	}
	return nil
}

func FrostKeySignMsgID(req FrostKeySignRequest) (string, error) {
	vault := strings.TrimSpace(req.VaultPubKey)
	toAddr := strings.TrimSpace(req.ToAddress)
	if vault == "" {
		return "", fmt.Errorf("vault pubkey is required")
	}
	if toAddr == "" {
		return "", fmt.Errorf("to address is required")
	}
	network := strings.ToLower(strings.TrimSpace(req.Network))
	if network == "" {
		network = common.ChainNetworkName(common.CurrentChainNetwork)
	}

	requiredInputsHash, err := frostRequiredInputsHash(req.RequiredInputs)
	if err != nil {
		return "", fmt.Errorf("fail to hash required inputs: %w", err)
	}

	canonical := strings.Join([]string{
		"domain=" + frostMsgIDDomain,
		"protocol_version=" + strconv.FormatUint(uint64(frostWireProtocolVersion), 10),
		"vault_pub_key=" + vault,
		"network=" + network,
		"to_address=" + toAddr,
		"amount_atomic=" + strconv.FormatUint(req.AmountAtomic, 10),
		"memo=" + req.Memo,
		"fee_rate=" + strconv.FormatUint(req.FeeRate, 10),
		"required_inputs_hash=" + requiredInputsHash,
		"max_inputs=" + strconv.FormatInt(req.MaxInputs, 10),
		"dust_threshold=" + strconv.FormatUint(req.DustThreshold, 10),
		"max_selected_input_value_atomic=" + strconv.FormatUint(req.MaxSelectedInputValueAtomic, 10),
		"max_gas_atomic=" + strconv.FormatUint(req.MaxGasAtomic, 10),
		"tx_in_hash=" + strings.ToLower(strings.TrimSpace(req.TxInHash)),
		"request_id=" + strings.TrimSpace(req.RequestID),
		"block_height=" + strconv.FormatInt(req.BlockHeight, 10),
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func FrostDecoyContext(msgID string) (string, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return "", fmt.Errorf("msgID is required")
	}
	return tcommon.MsgToHashString([]byte(frostDecoyContextDomain + "\n" + msgID))
}

func frostSignerSetHash(signerPubKeys []string) (string, error) {
	members, _, _, err := buildParticipantIndex(signerPubKeys)
	if err != nil {
		return "", err
	}
	return tcommon.MsgToHashString([]byte(strings.Join(members, ",")))
}

// A join timeout proves only that this reporter did not form a party. It does
// not distinguish an unavailable leader from a responsive leader waiting on
// another member, a partition, or local overload. Keep ambiguous availability
// failures non-slashable until there is signed positive evidence.
func frostJoinPartyBlame() blame.Blame {
	return blame.NewBlame(blame.TssSyncFail, nil)
}

func frostJoinAttemptID(msgID, leader string, eligiblePeers []string, attempt int) (string, error) {
	msgID = strings.TrimSpace(msgID)
	leader = strings.TrimSpace(leader)
	if msgID == "" || leader == "" || len(eligiblePeers) == 0 || attempt < 0 {
		return "", errors.New("invalid FROST join attempt context")
	}
	peerSet := make([]string, 0, len(eligiblePeers))
	seenPeers := make(map[string]struct{}, len(eligiblePeers))
	for _, peerID := range eligiblePeers {
		peerID = strings.TrimSpace(peerID)
		if peerID == "" {
			return "", errors.New("invalid FROST join attempt peer")
		}
		if _, ok := seenPeers[peerID]; ok {
			return "", errors.New("duplicate FROST join attempt peer")
		}
		seenPeers[peerID] = struct{}{}
		peerSet = append(peerSet, peerID)
	}
	sort.Strings(peerSet)
	canonical := strings.Join([]string{
		"domain=" + frostJoinAttemptDomain,
		"msg_id=" + msgID,
		"eligible_peers=" + strings.Join(peerSet, ","),
		"leader=" + leader,
		"attempt=" + strconv.Itoa(attempt),
	}, "\n")
	digest, err := tcommon.MsgToHashString([]byte(canonical))
	if err != nil {
		return "", err
	}
	return p2p.FrostJoinAttemptIDPrefix + digest, nil
}

func validateFrostJoinParty(onlinePeers, eligiblePeers []peer.ID, leader string, quorum int) error {
	if quorum < 1 || len(onlinePeers) < quorum {
		return fmt.Errorf("formed FROST party has %d peers; need %d", len(onlinePeers), quorum)
	}
	eligible := make(map[string]struct{}, len(eligiblePeers))
	for _, peerID := range eligiblePeers {
		eligible[peerID.String()] = struct{}{}
	}
	seen := make(map[string]struct{}, len(onlinePeers))
	leaderPresent := false
	for _, peerID := range onlinePeers {
		peerString := peerID.String()
		if _, ok := eligible[peerString]; !ok {
			return fmt.Errorf("formed FROST party contains ineligible peer %s", peerString)
		}
		if _, ok := seen[peerString]; ok {
			return fmt.Errorf("formed FROST party contains duplicate peer %s", peerString)
		}
		seen[peerString] = struct{}{}
		if peerString == leader {
			leaderPresent = true
		}
	}
	if !leaderPresent {
		return errors.New("formed FROST party does not contain its selected leader")
	}
	return nil
}

// joinFrostKeySignParty retries only party formation. No FROST nonce or share
// exists yet, so rotating a keyless or unavailable coordinator cannot create a
// second signing attempt.
func (t *TssServer) joinFrostKeySignParty(
	msgID,
	version string,
	blockHeight int64,
	participants []string,
	threshold int,
	sigChan chan string,
) ([]peer.ID, string, error) {
	oldRotation, err := conversion.VersionLTCheck(strings.TrimSpace(version), frostJoinRotationVersion)
	if err != nil {
		return nil, "", fmt.Errorf("fail to parse FROST join rotation version: %w", err)
	}
	if oldRotation {
		return t.joinParty(msgID, version, blockHeight, participants, threshold, sigChan)
	}

	eligiblePeers, err := conversion.GetPeerIDsFromPubKeys(participants)
	if err != nil {
		return nil, "", fmt.Errorf("fail to resolve FROST join peers: %w", err)
	}
	peerStrings := peerIDsToStrings(eligiblePeers)
	leaders, err := p2p.LeaderNodes(msgID, blockHeight, peerStrings)
	if err != nil {
		return nil, "", fmt.Errorf("fail to order FROST join leaders: %w", err)
	}
	attempts := len(leaders)
	if attempts > frostMaxJoinLeaderAttempts {
		attempts = frostMaxJoinLeaderAttempts
	}

	var lastErr error
	var lastLeader string
	for attempt := 0; attempt < attempts; attempt++ {
		leader := leaders[attempt]
		joinID, idErr := frostJoinAttemptID(msgID, leader, peerStrings, attempt)
		if idErr != nil {
			return nil, leader, idErr
		}
		onlinePeers, selectedLeader, joinErr := t.partyCoordinator.JoinPartyWithSelectedLeader(
			joinID,
			leader,
			peerStrings,
			threshold,
			sigChan,
		)
		t.partyCoordinator.ReleaseStream(joinID)
		if joinErr == nil {
			joinErr = validateFrostJoinParty(onlinePeers, eligiblePeers, selectedLeader, threshold+1)
		}
		if joinErr == nil {
			return onlinePeers, selectedLeader, nil
		}
		lastErr = joinErr
		lastLeader = selectedLeader
		t.logger.Warn().
			Err(joinErr).
			Str("msg_id", msgID).
			Str("join_id", joinID).
			Str("leader", selectedLeader).
			Int("attempt", attempt+1).
			Int("max_attempts", attempts).
			Msg("FROST join attempt failed; rotating coordinator without blame")
	}
	return nil, lastLeader, fmt.Errorf("all %d deterministic FROST join attempts failed: %w", attempts, lastErr)
}

// frostActiveSignerPubKeys returns the filtered FROST signing party when one
// is supplied, otherwise it falls back to full vault membership.
func frostActiveSignerPubKeys(req FrostKeySignRequest) []string {
	if len(req.ActiveSignerPubKeys) > 0 {
		return req.ActiveSignerPubKeys
	}
	return req.SignerPubKeys
}

func frostRequiredInputsHash(inputs []FrostSpentOutputRef) (string, error) {
	normalized := make([]FrostSpentOutputRef, 0, len(inputs))
	for _, input := range inputs {
		txHash := strings.ToLower(strings.TrimSpace(input.TxHash))
		if txHash == "" {
			return "", fmt.Errorf("required input tx_hash is empty")
		}
		normalized = append(normalized, FrostSpentOutputRef{
			TxHash:      txHash,
			OutputIndex: input.OutputIndex,
			KeyImage:    strings.ToLower(strings.TrimSpace(input.KeyImage)),
			SpendTxHash: strings.ToLower(strings.TrimSpace(input.SpendTxHash)),
		})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].TxHash == normalized[j].TxHash {
			if normalized[i].OutputIndex == normalized[j].OutputIndex {
				if normalized[i].KeyImage == normalized[j].KeyImage {
					return normalized[i].SpendTxHash < normalized[j].SpendTxHash
				}
				return normalized[i].KeyImage < normalized[j].KeyImage
			}
			return normalized[i].OutputIndex < normalized[j].OutputIndex
		}
		return normalized[i].TxHash < normalized[j].TxHash
	})
	buf, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return tcommon.MsgToHashString(buf)
}

func FrostPlanHash(req FrostKeySignRequest, msgID string) (string, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return "", fmt.Errorf("msgID is required")
	}
	vault := strings.TrimSpace(req.VaultPubKey)
	toAddress := strings.TrimSpace(req.ToAddress)
	if vault == "" {
		return "", fmt.Errorf("vault pubkey is required")
	}
	if toAddress == "" {
		return "", fmt.Errorf("to address is required")
	}
	signerSetHash, err := frostSignerSetHash(req.SignerPubKeys)
	if err != nil {
		return "", fmt.Errorf("fail to hash signer membership: %w", err)
	}
	activeSignerSetHash, err := frostSignerSetHash(frostActiveSignerPubKeys(req))
	if err != nil {
		return "", fmt.Errorf("fail to hash active signer membership: %w", err)
	}
	requiredInputsHash, err := frostRequiredInputsHash(req.RequiredInputs)
	if err != nil {
		return "", fmt.Errorf("fail to hash required inputs: %w", err)
	}
	canonical := strings.Join([]string{
		"domain=" + frostPlanHashDomain,
		"protocol_version=" + strconv.FormatUint(uint64(frostWireProtocolVersion), 10),
		"msg_id=" + msgID,
		"vault_pub_key=" + vault,
		"to_address=" + toAddress,
		"amount_atomic=" + strconv.FormatUint(req.AmountAtomic, 10),
		"memo=" + req.Memo,
		"fee_rate=" + strconv.FormatUint(req.FeeRate, 10),
		"required_inputs_hash=" + requiredInputsHash,
		"max_inputs=" + strconv.FormatInt(req.MaxInputs, 10),
		"dust_threshold=" + strconv.FormatUint(req.DustThreshold, 10),
		"max_selected_input_value_atomic=" + strconv.FormatUint(req.MaxSelectedInputValueAtomic, 10),
		"max_gas_atomic=" + strconv.FormatUint(req.MaxGasAtomic, 10),
		"signer_set_hash=" + signerSetHash,
		"active_signer_set_hash=" + activeSignerSetHash,
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func FrostTxHexHash(txHex string) (string, error) {
	txHex = strings.TrimSpace(txHex)
	if txHex == "" {
		return "", fmt.Errorf("tx_hex is required")
	}
	raw, err := hex.DecodeString(txHex)
	if err != nil {
		return "", fmt.Errorf("tx_hex is not valid hex")
	}
	return tcommon.MsgToHashString(raw)
}

func FrostEventualityID(planHash, txHash, txHexHash, txKeyHash string, feeAtomic uint64) (string, error) {
	planHash = strings.ToLower(strings.TrimSpace(planHash))
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	txHexHash = strings.ToLower(strings.TrimSpace(txHexHash))
	txKeyHash = strings.ToLower(strings.TrimSpace(txKeyHash))
	if err := validateHashHex("plan_hash", planHash); err != nil {
		return "", err
	}
	if err := validateHashHex("tx_hash", txHash); err != nil {
		return "", err
	}
	if err := validateHashHex("tx_hex_hash", txHexHash); err != nil {
		return "", err
	}
	if err := validateHashHex("tx_key_hash", txKeyHash); err != nil {
		return "", err
	}
	canonical := strings.Join([]string{
		"domain=" + frostEventualityIDDomain,
		"plan_hash=" + planHash,
		"tx_hash=" + txHash,
		"tx_hex_hash=" + txHexHash,
		"tx_key_hash=" + txKeyHash,
		"fee_atomic=" + strconv.FormatUint(feeAtomic, 10),
	}, "\n")
	return tcommon.MsgToHashString([]byte(canonical))
}

func BuildFrostEventuality(planHash, txHash, txHex, txKey string, feeAtomic uint64) (FrostEventuality, error) {
	txHexHash, err := FrostTxHexHash(txHex)
	if err != nil {
		return FrostEventuality{}, err
	}
	txKeyHash, err := FrostTxKeyHash(txKey)
	if err != nil {
		return FrostEventuality{}, err
	}
	eventualityID, err := FrostEventualityID(planHash, txHash, txHexHash, txKeyHash, feeAtomic)
	if err != nil {
		return FrostEventuality{}, err
	}
	return FrostEventuality{
		Version:       frostEventualityVersion,
		PlanHash:      strings.ToLower(strings.TrimSpace(planHash)),
		TxHexHash:     strings.ToLower(strings.TrimSpace(txHexHash)),
		TxKeyHash:     txKeyHash,
		EventualityID: strings.ToLower(strings.TrimSpace(eventualityID)),
	}, nil
}

func ValidateFrostEventuality(
	req FrostKeySignRequest,
	msgID string,
	txHash string,
	txHex string,
	txKey string,
	feeAtomic uint64,
	eventuality FrostEventuality,
) error {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return fmt.Errorf("msg_id is required")
	}
	expectedPlanHash, err := FrostPlanHash(req, msgID)
	if err != nil {
		return fmt.Errorf("fail to derive plan hash: %w", err)
	}
	if eventuality.Version != frostEventualityVersion {
		return fmt.Errorf("unexpected eventuality version %d", eventuality.Version)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.PlanHash), expectedPlanHash) {
		return fmt.Errorf("eventuality plan_hash mismatch")
	}
	expectedTxHexHash, err := FrostTxHexHash(txHex)
	if err != nil {
		return fmt.Errorf("fail to derive tx_hex_hash: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.TxHexHash), expectedTxHexHash) {
		return fmt.Errorf("eventuality tx_hex_hash mismatch")
	}
	expectedTxKeyHash, err := FrostTxKeyHash(txKey)
	if err != nil {
		return fmt.Errorf("fail to derive tx_key_hash: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.TxKeyHash), expectedTxKeyHash) {
		return fmt.Errorf("eventuality tx_key_hash mismatch")
	}
	expectedEventualityID, err := FrostEventualityID(expectedPlanHash, txHash, expectedTxHexHash, expectedTxKeyHash, feeAtomic)
	if err != nil {
		return fmt.Errorf("fail to derive eventuality_id: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(eventuality.EventualityID), expectedEventualityID) {
		return fmt.Errorf("eventuality_id mismatch")
	}
	return nil
}

// FrostTxKeyHash validates the Rust-returned Monero transaction key as a
// canonical lowercase 32-byte hex value and commits to its raw bytes without
// exposing it inside the eventuality identifier.
func FrostTxKeyHash(txKey string) (string, error) {
	if txKey != strings.TrimSpace(txKey) || txKey != strings.ToLower(txKey) || len(txKey) != 64 {
		return "", fmt.Errorf("tx_key must be canonical lowercase 32-byte hex")
	}
	raw, err := hex.DecodeString(txKey)
	if err != nil {
		return "", fmt.Errorf("tx_key must be canonical lowercase 32-byte hex")
	}
	allZero := true
	for _, b := range raw {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "", fmt.Errorf("tx_key must not be zero")
	}
	return tcommon.MsgToHashString(raw)
}

func validateHashHex(label, value string) error {
	if len(value) != frostEventualityHashHexBytes {
		return fmt.Errorf("%s must be %d hex chars", label, frostEventualityHashHexBytes)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s is not valid hex", label)
	}
	return nil
}

// validateFrostPreparedInputs binds the signed Monero transaction back to the
// exact vault outputs selected when this local sidecar prepared the ceremony.
// Ordering is intentionally ignored because Monero may reorder transaction
// inputs while constructing the final prefix.
func validateFrostPreparedInputs(selected, spent []FrostSpentOutputRef, txHash string) error {
	if len(selected) == 0 {
		return fmt.Errorf("prepare returned no selected inputs")
	}
	if len(selected) > frostMaxResultSpentRefs {
		return fmt.Errorf("prepare returned too many selected inputs: %d", len(selected))
	}
	if len(spent) != len(selected) {
		return fmt.Errorf("signed input count %d does not match prepared input count %d", len(spent), len(selected))
	}

	normalizeHash := func(label, value string) (string, error) {
		value = strings.TrimSpace(value)
		if err := validateHashHex(label, value); err != nil {
			return "", err
		}
		return strings.ToLower(value), nil
	}

	finalTxHash, err := normalizeHash("tx_hash", txHash)
	if err != nil {
		return err
	}

	selectedByRef := make(map[string]string, len(selected))
	selectedKeyImages := make(map[string]struct{}, len(selected))
	for i, ref := range selected {
		inputHash, err := normalizeHash(fmt.Sprintf("selected_inputs[%d].tx_hash", i), ref.TxHash)
		if err != nil {
			return err
		}
		inputID := fmt.Sprintf("%s:%d", inputHash, ref.OutputIndex)
		if _, exists := selectedByRef[inputID]; exists {
			return fmt.Errorf("prepare returned duplicate selected input %s", inputID)
		}

		keyImage := ""
		if strings.TrimSpace(ref.KeyImage) != "" {
			keyImage, err = normalizeHash(fmt.Sprintf("selected_inputs[%d].key_image", i), ref.KeyImage)
			if err != nil {
				return err
			}
			if _, exists := selectedKeyImages[keyImage]; exists {
				return fmt.Errorf("prepare returned duplicate key image %s", keyImage)
			}
			selectedKeyImages[keyImage] = struct{}{}
		}
		selectedByRef[inputID] = keyImage
	}

	seenRefs := make(map[string]struct{}, len(spent))
	seenKeyImages := make(map[string]struct{}, len(spent))
	for i, ref := range spent {
		inputHash, err := normalizeHash(fmt.Sprintf("spent_output_refs[%d].tx_hash", i), ref.TxHash)
		if err != nil {
			return err
		}
		inputID := fmt.Sprintf("%s:%d", inputHash, ref.OutputIndex)
		preparedKeyImage, exists := selectedByRef[inputID]
		if !exists {
			return fmt.Errorf("signed transaction spent unprepared input %s", inputID)
		}
		if _, duplicate := seenRefs[inputID]; duplicate {
			return fmt.Errorf("signed transaction repeated input %s", inputID)
		}
		seenRefs[inputID] = struct{}{}

		keyImage, err := normalizeHash(fmt.Sprintf("spent_output_refs[%d].key_image", i), ref.KeyImage)
		if err != nil {
			return err
		}
		if _, duplicate := seenKeyImages[keyImage]; duplicate {
			return fmt.Errorf("signed transaction repeated key image %s", keyImage)
		}
		seenKeyImages[keyImage] = struct{}{}
		if preparedKeyImage != "" && keyImage != preparedKeyImage {
			return fmt.Errorf("signed key image does not match prepared input %s", inputID)
		}

		spendTxHash, err := normalizeHash(fmt.Sprintf("spent_output_refs[%d].spend_tx_hash", i), ref.SpendTxHash)
		if err != nil {
			return err
		}
		if spendTxHash != finalTxHash {
			return fmt.Errorf("spent input %s names a different spend transaction", inputID)
		}
	}

	return nil
}

// normalizeFrostResultSpentRefs validates the portable spent-ref metadata on a
// quorum-gossiped result. Stand-by nodes did not run prepare locally, so they
// cannot compare against a selected-input set, but they can still reject
// malformed, duplicated, or cross-transaction refs before persisting a signed
// payload and checkpoint.
func normalizeFrostResultSpentRefs(refs []FrostSpentOutputRef, txHash string) ([]FrostSpentOutputRef, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("frost success result has no spent refs")
	}
	if len(refs) > frostMaxResultSpentRefs {
		return nil, fmt.Errorf("frost success result spent refs exceeds limit")
	}

	normalizeHash := func(label, value string) (string, error) {
		value = strings.TrimSpace(value)
		if err := validateHashHex(label, value); err != nil {
			return "", err
		}
		return strings.ToLower(value), nil
	}
	finalTxHash, err := normalizeHash("tx_hash", txHash)
	if err != nil {
		return nil, err
	}

	normalized := make([]FrostSpentOutputRef, len(refs))
	seenRefs := make(map[string]struct{}, len(refs))
	seenKeyImages := make(map[string]struct{}, len(refs))
	for i, ref := range refs {
		fundingTxHash, normalizeErr := normalizeHash(fmt.Sprintf("spent_output_refs[%d].tx_hash", i), ref.TxHash)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		inputID := fmt.Sprintf("%s:%d", fundingTxHash, ref.OutputIndex)
		if _, duplicate := seenRefs[inputID]; duplicate {
			return nil, fmt.Errorf("frost success result repeated input %s", inputID)
		}
		seenRefs[inputID] = struct{}{}

		keyImage, normalizeErr := normalizeHash(fmt.Sprintf("spent_output_refs[%d].key_image", i), ref.KeyImage)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if _, duplicate := seenKeyImages[keyImage]; duplicate {
			return nil, fmt.Errorf("frost success result repeated key image %s", keyImage)
		}
		seenKeyImages[keyImage] = struct{}{}

		spendTxHash, normalizeErr := normalizeHash(fmt.Sprintf("spent_output_refs[%d].spend_tx_hash", i), ref.SpendTxHash)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		if spendTxHash != finalTxHash {
			return nil, fmt.Errorf("frost success result input %s names a different spend transaction", inputID)
		}

		normalized[i] = FrostSpentOutputRef{
			TxHash:      fundingTxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    keyImage,
			SpendTxHash: spendTxHash,
		}
	}
	return normalized, nil
}

func (t *TssServer) FrostKeySign(req FrostKeySignRequest, sidecar FrostSidecarClient) (FrostKeySignResponse, error) {
	if sidecar == nil {
		return FrostKeySignResponse{}, fmt.Errorf("frost sidecar client is nil")
	}
	if len(req.SignerPubKeys) == 0 {
		return FrostKeySignResponse{}, fmt.Errorf("signer pub keys are empty")
	}
	expectedNetwork := common.ChainNetworkName(common.CurrentChainNetwork)
	req.Network = strings.ToLower(strings.TrimSpace(req.Network))
	if req.Network == "" {
		req.Network = expectedNetwork
	}
	if req.Network != expectedNetwork {
		return FrostKeySignResponse{}, fmt.Errorf("frost deployment network %q does not match current network %q", req.Network, expectedNetwork)
	}
	activeSignerPubKeys := frostActiveSignerPubKeys(req)
	if len(activeSignerPubKeys) == 0 {
		return FrostKeySignResponse{}, fmt.Errorf("active signer pub keys are empty")
	}
	msgID, err := FrostKeySignMsgID(req)
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to compute frost msgID: %w", err)
	}
	planHash, err := FrostPlanHash(req, msgID)
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to compute frost plan hash: %w", err)
	}
	threshold, err := conversion.GetThreshold(len(req.SignerPubKeys))
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to compute frost threshold: %w", err)
	}
	if len(activeSignerPubKeys) <= threshold {
		return FrostKeySignResponse{}, fmt.Errorf(
			"not enough eligible frost signers: need %d of %d, got %d",
			threshold+1,
			len(req.SignerPubKeys),
			len(activeSignerPubKeys),
		)
	}

	allowedPeerIDs, err := conversion.GetPeerIDsFromPubKeys(activeSignerPubKeys)
	if err != nil {
		return FrostKeySignResponse{}, fmt.Errorf("fail to resolve FROST session peers: %w", err)
	}
	inbox := newFrostSignInbox()
	subscriptions := inbox.subscriptions()
	sigChan := make(chan string, 2)
	verificationMessageLimit := p2p.FrostVerificationMessagesPerPeer(len(activeSignerPubKeys), 2)

	for _, subscription := range subscriptions {
		messageLimit := p2p.FrostMaxMessagesPerPeer
		if subscription.msgType == messages.TSSFrostVerifyMsg {
			messageLimit = verificationMessageLimit
		}
		t.p2pCommunication.SetSubscribeWithPeers(
			subscription.msgType,
			msgID,
			subscription.messageChan,
			allowedPeerIDs,
			p2p.FrostMaxSessionPayload,
			messageLimit,
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

	coordinator := NewFrostSignCoordinator(
		t.p2pCommunication,
		sidecar,
		t.localNodePubKey,
		msgID,
		planHash,
		inbox,
		t.conf.KeySignTimeout,
		t.privateKey,
	)
	// Batching preserves the logical echo count. Charge each decoded entry;
	// the subscription's same count remains a looser ceiling on whole frames.
	coordinator.verificationEntryLimit = verificationMessageLimit

	// Nodes outside the filtered active set must not join the FROST ceremony,
	// but they can still import a quorum-verified result broadcast by signers.
	if !containsNormalizedPubKey(activeSignerPubKeys, t.localNodePubKey) {
		t.logger.Info().Str("msg_id", msgID).Msg("not eligible FROST signer, waiting for result")
		quorum := threshold + 1
		waitResp, waitErr := coordinator.WaitForResult(activeSignerPubKeys, quorum, t.conf.KeySignTimeout+5*time.Second)
		if waitErr != nil {
			if !errors.Is(waitErr, errFrostResultTimeout) {
				return FrostKeySignResponse{}, fmt.Errorf("fail to wait for FROST result as non-eligible signer: %w", waitErr)
			}
			t.logger.Info().Err(waitErr).Str("msg_id", msgID).Msg("timed out waiting for FROST result as non-eligible signer; standing down")
			return FrostKeySignResponse{
				Status: tcommon.NA,
				MsgID:  msgID,
			}, nil
		}
		if waitResp.Status == tcommon.Success {
			if err := reserveFrostSigningClaim(req); err != nil {
				return FrostKeySignResponse{}, err
			}
			t.persistPeerAddressBook()
		}
		return waitResp, nil
	}

	onlinePeers, leader, errJoinParty := t.joinFrostKeySignParty(
		msgID,
		req.Version,
		req.BlockHeight,
		activeSignerPubKeys,
		threshold,
		sigChan,
	)
	if errJoinParty != nil {
		t.logger.Error().Err(errJoinParty).Str("msg_id", msgID).Str("leader", leader).Msg("fail to form FROST keysign party")
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  frostJoinPartyBlame(),
		}, nil
	}

	activeParticipants, err := conversion.GetPubKeysFromPeerIDs(peerIDsToStrings(onlinePeers))
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("fail to resolve active participants", nil),
		}, nil
	}
	// The pre-join subscription is restricted to the eligible signer set. Once
	// the authenticated join response forms a smaller party, narrow all round
	// traffic to that exact set. Result gossip stays on the eligible set so
	// stand-by members can adopt a quorum result.
	for _, subscription := range subscriptions {
		if !subscription.narrowAfterJoin {
			continue
		}
		if !t.p2pCommunication.NarrowSubscribePeers(subscription.msgType, msgID, onlinePeers) {
			reason := fmt.Sprintf("failed to narrow FROST keysign %s subscription", subscription.msgType)
			t.logger.Error().Str("msg_id", msgID).Str("message_type", subscription.msgType.String()).Msg(reason)
			return FrostKeySignResponse{
				Status: tcommon.Fail,
				Blame:  blame.NewBlame(reason, nil),
			}, nil
		}
	}
	allPeerIDs, peerErr := conversion.GetPeerIDsFromPubKeys(req.SignerPubKeys)
	if peerErr != nil {
		t.logger.Warn().Err(peerErr).Msg("fail to resolve all peer IDs for frost result broadcast, fallback to active peers")
		allPeerIDs = onlinePeers
	}

	isActive := false
	ourPeerID := t.p2pCommunication.GetLocalPeerID()
	for _, p := range onlinePeers {
		if p.String() == ourPeerID {
			isActive = true
			break
		}
	}

	if !isActive {
		t.logger.Info().Str("msg_id", msgID).Msg("not active FROST signer, waiting for result")
		quorum := threshold + 1
		waitResp, waitErr := coordinator.WaitForResult(activeParticipants, quorum, t.conf.KeySignTimeout+5*time.Second)
		if waitErr != nil {
			return FrostKeySignResponse{
				Status: tcommon.Fail,
				Blame:  blame.NewBlame(waitErr.Error(), nil),
			}, nil
		}
		if waitResp.Status == tcommon.Success {
			if err := reserveFrostSigningClaim(req); err != nil {
				return FrostKeySignResponse{}, err
			}
			t.persistPeerAddressBook()
		}
		return waitResp, nil
	}

	// Persist local ownership after party selection and before any sidecar call
	// can reserve inputs or release a signature share.
	if err := reserveFrostSigningClaim(req); err != nil {
		return FrostKeySignResponse{}, err
	}
	resp := coordinator.Execute(req, activeParticipants, onlinePeers)
	if resp.Status == tcommon.Success {
		t.persistPeerAddressBook()
		if err := coordinator.BroadcastResult(resp, allPeerIDs); err != nil {
			t.logger.Warn().Err(err).Msg("fail to broadcast frost result")
		}
		return resp, nil
	}

	quorum := threshold + 1
	waitResp, waitErr := coordinator.WaitForResult(activeParticipants, quorum, t.conf.KeySignTimeout+5*time.Second)
	if waitErr == nil && waitResp.Status == tcommon.Success {
		t.persistPeerAddressBook()
		return waitResp, nil
	}

	if err := coordinator.BroadcastResult(resp, allPeerIDs); err != nil {
		t.logger.Warn().Err(err).Msg("fail to broadcast local frost failure result")
	}
	if resp.releaseSigningClaim {
		if err := releaseFrostSigningClaim(req); err != nil {
			return FrostKeySignResponse{}, err
		}
	}
	if waitErr == nil && shouldAdoptFrostFailureResult(resp, waitResp) {
		return waitResp, nil
	}
	return resp, nil
}

// shouldAdoptFrostFailureResult reports whether a quorum-reported failure may
// replace the local failure result. A quorum result only adds value when it
// carries actionable blame the local node could not attribute itself.
//
// It must never erase a stronger local safety boundary: blame proven for an
// earlier phase must not be grafted onto a local share/result phase failure,
// because that would both manufacture a terminal accusation no participant
// actually observed and drop the vault-stick retry semantics. Nodes which
// genuinely observed the earlier peer failure report it at its real round.
func shouldAdoptFrostFailureResult(local, candidate FrostKeySignResponse) bool {
	if candidate.Status != tcommon.Fail || len(candidate.Blame.BlameNodes) == 0 || len(local.Blame.BlameNodes) > 0 {
		return false
	}
	// Once the local signature share may have escaped, never replace that terminal
	// state with a failure attributed to an earlier or non-terminal phase. A
	// terminal-phase quorum failure is safe to adopt because it preserves the
	// same vault-stick/checkpoint semantics while adding actionable peer blame.
	return !isFrostExposureRound(local.Blame.Round) || isFrostExposureRound(candidate.Blame.Round)
}

// isFrostExposureRound reports whether a FROST blame round must retain the
// signing claim after the partial-sign RPC begins. FrostSignPartial runs before
// exchangeShares, so frost_share failures remain past the exposure boundary even
// when retained evidence prevents local-share broadcast. frost_result is later
// in the ceremony; frost_preprocess precedes the partial-sign RPC.
func isFrostExposureRound(round string) bool {
	switch strings.TrimSpace(round) {
	case frostRoundShare, frostRoundResult:
		return true
	default:
		return false
	}
}

func reserveFrostSigningClaim(req FrostKeySignRequest) error {
	if req.ReserveSigningClaim == nil {
		return nil
	}
	if err := req.ReserveSigningClaim(); err != nil {
		return fmt.Errorf("reserve FROST signing claim before local participation: %w", err)
	}
	return nil
}

func releaseFrostSigningClaim(req FrostKeySignRequest) error {
	if req.ReleaseSigningClaim == nil {
		return nil
	}
	if err := req.ReleaseSigningClaim(); err != nil {
		return fmt.Errorf("release unused FROST signing claim: %w", err)
	}
	return nil
}

func (c *FrostSignCoordinator) Execute(
	req FrostKeySignRequest,
	activeParticipants []string,
	activePeerIDs []peer.ID,
) (result FrostKeySignResponse) {
	shareMayHaveEscaped := false
	localShareWasRequired := false
	postExposureRound := ""
	defer func() {
		// FrostSignPartial may release a usable signature share even when its
		// local response is lost. Every failure from that call onward must carry
		// a terminal FROST phase so Bifrost keeps this outbound sticky instead of
		// authorizing a fresh ceremony. Once all shares have been exchanged, use
		// frost_result for combine and result-validation failures.
		if result.Status != tcommon.Success && postExposureRound != "" {
			result.Blame.Round = postExposureRound
		}
		if result.Status != tcommon.Success && localShareWasRequired && !shareMayHaveEscaped {
			result.releaseSigningClaim = true
		}
	}()
	if c.sidecar == nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost sidecar client is nil", nil),
		}
	}
	if c.msgID == "" {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost msgID is empty", nil),
		}
	}
	c.vaultPubKey = strings.TrimSpace(req.VaultPubKey)
	if c.vaultPubKey == "" {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("frost vault pubkey is empty", nil),
		}
	}
	c.network = strings.ToLower(strings.TrimSpace(req.Network))
	if c.network == "" {
		c.network = common.ChainNetworkName(common.CurrentChainNetwork)
	}
	_, participantIndexMap, originalByNormalized, err := buildParticipantIndex(req.SignerPubKeys)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}
	activeSet, err := buildActiveSet(activeParticipants, originalByNormalized)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(err.Error(), nil),
		}
	}
	threshold, err := conversion.GetThreshold(len(req.SignerPubKeys))
	if err != nil {
		return FrostKeySignResponse{Status: tcommon.Fail, Blame: blame.NewBlame(err.Error(), nil)}
	}
	// With spare party members, another threshold may have completed without
	// this node, so a local pre-share failure cannot safely release ownership.
	localShareWasRequired = len(activeSet.originalByNormalized) == threshold+1
	localNorm := normalizePubKey(c.localPubKey)
	localIndex, ok := participantIndexMap[localNorm]
	if !ok {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in participant set", nil),
		}
	}
	if _, ok := activeSet.originalByNormalized[localNorm]; !ok {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame("local signer is not in active signer set", nil),
		}
	}
	c.roundState, err = newFrostSignRoundState(
		activeSet,
		participantIndexMap,
		c.vaultPubKey,
		c.network,
	)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("failed to initialize FROST keysign transcript state: %v", err), nil),
		}
	}
	decoyContext, err := FrostDecoyContext(c.msgID)
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to derive decoy context: %v", err), nil),
		}
	}

	prepareResp, err := c.sidecar.FrostSignPrepare(FrostSidecarPrepareReq{
		MsgID:                       c.msgID,
		VaultPubKey:                 req.VaultPubKey,
		ToAddress:                   req.ToAddress,
		AmountAtomic:                req.AmountAtomic,
		Memo:                        req.Memo,
		FeeRate:                     req.FeeRate,
		DecoyContext:                decoyContext,
		RequiredInputs:              req.RequiredInputs,
		MaxInputs:                   req.MaxInputs,
		DustThreshold:               req.DustThreshold,
		MaxSelectedInputValueAtomic: req.MaxSelectedInputValueAtomic,
		MaxGasAtomic:                req.MaxGasAtomic,
	})
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost prepare failed: %v", err), nil),
		}
	}

	preprocesses, failResp := c.exchangePreprocess(
		activeSet,
		participantIndexMap,
		activePeerIDs,
		FrostPreprocessPayload{
			SenderPubKey:     c.localPubKey,
			ParticipantIndex: localIndex,
			PreprocessHex:    prepareResp.Preprocess,
		},
	)
	if failResp != nil {
		return *failResp
	}

	// Once the partial RPC starts, a usable share may escape even if the local
	// response is lost. Keep the claim fail closed from this point onward.
	shareMayHaveEscaped = true
	postExposureRound = messages.FROSTKEYSIGNSHARE
	partialResp, err := c.sidecar.FrostSignPartial(FrostSidecarPartialReq{
		MsgID:           c.msgID,
		SessionID:       prepareResp.SessionID,
		AllPreprocesses: preprocesses,
	})
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("frost partial failed: %v", err), nil),
		}
	}

	shares, failResp := c.exchangeShares(
		activeSet,
		participantIndexMap,
		activePeerIDs,
		FrostSharePayload{
			SenderPubKey:     c.localPubKey,
			ParticipantIndex: localIndex,
			ShareHex:         partialResp.SignatureShare,
		},
	)
	if failResp != nil {
		return *failResp
	}

	postExposureRound = messages.FROSTKEYSIGNRESULT
	combineResp, err := c.sidecar.FrostSignCombine(FrostSidecarCombineReq{
		MsgID:     c.msgID,
		SessionID: prepareResp.SessionID,
		AllShares: shares,
	})
	if err != nil {
		return c.failForPubKeys(fmt.Sprintf("frost combine failed: %v", err), frostRoundShare, nil)
	}
	// The sidecar enforces MaxGas during transaction construction, but keep the
	// same cap check here so a buggy or mismatched signer cannot return an
	// over-cap transaction that Bifrost would later observe.
	if req.MaxGasAtomic > 0 && combineResp.FeeAtomic > req.MaxGasAtomic {
		resp := c.failForPubKeys(
			fmt.Sprintf("%s actual_fee_atomic=%d max_gas_atomic=%d", FrostFailReasonXMRMaxGasExceeded, combineResp.FeeAtomic, req.MaxGasAtomic),
			frostRoundShare,
			nil,
		)
		resp.FeeAtomic = combineResp.FeeAtomic
		return resp
	}
	if err := validateFrostPreparedInputs(prepareResp.SelectedInputs, combineResp.SpentOutputRefs, combineResp.TxHash); err != nil {
		return c.failForPubKeys(fmt.Sprintf("frost combine input binding failed: %v", err), frostRoundShare, nil)
	}
	txKey := combineResp.TxKey
	if _, err := FrostTxKeyHash(txKey); err != nil {
		return c.failForPubKeys(fmt.Sprintf("invalid frost combine tx_key: %v", err), frostRoundShare, nil)
	}
	eventuality, err := BuildFrostEventuality(c.expectedPlanHash, combineResp.TxHash, combineResp.TxHex, txKey, combineResp.FeeAtomic)
	if err != nil {
		return c.failForPubKeys(fmt.Sprintf("failed to derive eventuality: %v", err), frostRoundShare, nil)
	}

	return FrostKeySignResponse{
		TxHash:          combineResp.TxHash,
		TxHex:           combineResp.TxHex,
		TxKey:           txKey,
		FeeAtomic:       combineResp.FeeAtomic,
		MsgID:           c.msgID,
		Eventuality:     eventuality,
		SpentOutputRefs: combineResp.SpentOutputRefs,
		Status:          tcommon.Success,
		Blame:           blame.Blame{},
	}
}

func (c *FrostSignCoordinator) exchangePreprocess(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostPreprocessPayload,
) (values map[string]string, failure *FrostKeySignResponse) {
	verifier, verifierErr := c.roundState.verifier(frostRoundPreprocess)
	if verifierErr != nil {
		resp := c.failForPubKeys(verifierErr.Error(), frostRoundPreprocess, nil)
		return nil, &resp
	}
	activePeers := c.roundState.activePeers
	localHash, err := FrostTranscriptPreprocessHash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local preprocess payload: %v", err), frostRoundPreprocess, nil)
		return nil, &resp
	}
	localEnvelope := frostEnvelopeFromPreprocess(c.msgID, c.vaultPubKey, c.network, local, localHash)
	if c.strictTranscript {
		localEnvelope, err = newFrostSignedEnvelope(c.privateKey, c.msgID, c.vaultPubKey, c.network, frostRoundPreprocess, local.SenderPubKey, local.ParticipantIndex, localHash)
		if err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundPreprocess, nil)
			return nil, &resp
		}
		local.ProtocolVersion = localEnvelope.ProtocolVersion
		local.Signature = localEnvelope.Signature
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to marshal preprocess payload: %v", err), nil),
		}
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostPreprocessMsg, payload, activePeerIDs); err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to broadcast preprocess payload: %v", err), nil),
		}
		return nil, &resp
	}
	if _, failure := verifier.recordPayload(local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNPREPROCESS, failure.blamePubKey)
		return nil, &resp
	}
	if c.strictTranscript {
		if _, err := verifier.recordSignedEnvelope(localEnvelope); err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundPreprocess, nil)
			return nil, &resp
		}
	}
	verificationBatch := newFrostVerificationBatcher(frostRoundPreprocess, c.localPubKey, len(activeSet.originalByNormalized))
	defer func() {
		if err := c.flushTranscriptVerifications(verificationBatch, activePeerIDs); err != nil && failure == nil {
			values = nil
			response := c.failForPubKeys(fmt.Sprintf("failed to broadcast preprocess verification: %v", err), frostRoundPreprocess, nil)
			failure = &response
		}
	}()
	if err := c.queueSignedTranscriptVerification(verificationBatch, localEnvelope, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast preprocess verification: %v", err), frostRoundPreprocess, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordVerification(c.localPubKey, local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNPREPROCESS, failure.blamePubKey)
		return nil, &resp
	}

	values = map[string]string{
		strconv.FormatUint(uint64(local.ParticipantIndex), 10): local.PreprocessHex,
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	flushTicker := time.NewTicker(frostVerificationBatchInterval)
	defer flushTicker.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.inbox.preprocess:
			if msg == nil {
				continue
			}
			// Narrowing leaves queued frames from removed peers; discard them before parsing.
			if _, active := activePeers[msg.PeerID]; !active {
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
			case messages.TSSFrostPreprocessMsg:
				var incoming FrostPreprocessPayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring malformed preprocess payload")
					continue
				}
				senderNorm := normalizePubKey(incoming.SenderPubKey)
				if senderNorm == "" {
					continue
				}
				if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
					continue
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring spoofed preprocess sender")
					continue
				}
				expectedIndex := participantIndexMap[senderNorm]
				if incoming.ParticipantIndex != expectedIndex {
					continue
				}
				if strings.TrimSpace(incoming.PreprocessHex) == "" {
					continue
				}
				payloadHash, err := FrostTranscriptPreprocessHash(c.msgID, incoming)
				if err != nil {
					continue
				}
				incomingEnvelope := frostEnvelopeFromPreprocess(c.msgID, c.vaultPubKey, c.network, incoming, payloadHash)
				if c.strictTranscript {
					if err := verifyFrostSignedEnvelope(incomingEnvelope); err != nil {
						c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring unsigned preprocess payload")
						continue
					}
					if evidence, err := verifier.recordSignedEnvelope(incomingEnvelope); err != nil {
						continue
					} else if evidence != nil {
						resp := c.failForEquivocation(*evidence)
						return nil, &resp
					}
				}
				isNewPayload, failure := verifier.recordPayload(incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash)
				if failure != nil {
					resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNPREPROCESS, failure.blamePubKey)
					return nil, &resp
				}
				if !isNewPayload {
					continue
				}
				values[strconv.FormatUint(uint64(incoming.ParticipantIndex), 10)] = incoming.PreprocessHex
				if err := c.queueSignedTranscriptVerification(verificationBatch, incomingEnvelope, activePeerIDs); err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast preprocess verification: %v", err), frostRoundPreprocess, nil)
					return nil, &resp
				}
				if _, failure := verifier.recordVerification(c.localPubKey, incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash); failure != nil {
					resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNPREPROCESS, failure.blamePubKey)
					return nil, &resp
				}
			}
		case msg := <-c.inbox.verify:
			if response := c.routeVerificationMessage(msg, frostRoundPreprocess); response != nil {
				return nil, response
			}
		case <-flushTicker.C:
			if err := c.flushTranscriptVerifications(verificationBatch, activePeerIDs); err != nil {
				resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast preprocess verification: %v", err), frostRoundPreprocess, nil)
				return nil, &resp
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			missingVerifiers := verifier.missingVerifierPubKeys()
			resp := c.failForPubKeys(
				transcriptTimeoutReason("preprocess payloads", missingPayloads, missingVerifiers),
				frostRoundPreprocess,
				nil,
			)
			return nil, &resp
		}
	}

	return values, nil
}

func (c *FrostSignCoordinator) exchangeShares(
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
	activePeerIDs []peer.ID,
	local FrostSharePayload,
) (values map[string]string, failure *FrostKeySignResponse) {
	verifier, verifierErr := c.roundState.verifier(frostRoundShare)
	if verifierErr != nil {
		resp := c.failForPubKeys(verifierErr.Error(), frostRoundShare, nil)
		return nil, &resp
	}
	// FrostSignPartial has already run, so the signing claim is sticky. Apply
	// every retained or already-queued share echo at the share boundary before
	// constructing the local envelope; portable equivocation must terminate the
	// ceremony without letting another local share escape.
	if response := c.activateDeferredVerifications(frostRoundShare); response != nil {
		return nil, response
	}
	if response := c.drainQueuedVerifications(frostRoundShare); response != nil {
		return nil, response
	}
	activePeers := c.roundState.activePeers
	localHash, err := FrostTranscriptShareHash(c.msgID, local)
	if err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to hash local share payload: %v", err), frostRoundShare, nil)
		return nil, &resp
	}
	localEnvelope := frostEnvelopeFromShare(c.msgID, c.vaultPubKey, c.network, local, localHash)
	if c.strictTranscript {
		localEnvelope, err = newFrostSignedEnvelope(c.privateKey, c.msgID, c.vaultPubKey, c.network, frostRoundShare, local.SenderPubKey, local.ParticipantIndex, localHash)
		if err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundShare, nil)
			return nil, &resp
		}
		local.ProtocolVersion = localEnvelope.ProtocolVersion
		local.Signature = localEnvelope.Signature
	}
	payload, err := json.Marshal(local)
	if err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to marshal share payload: %v", err), nil),
		}
		return nil, &resp
	}
	if err := c.broadcast(messages.TSSFrostShareMsg, payload, activePeerIDs); err != nil {
		resp := FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  blame.NewBlame(fmt.Sprintf("fail to broadcast share payload: %v", err), nil),
		}
		return nil, &resp
	}
	if _, failure := verifier.recordPayload(local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNSHARE, failure.blamePubKey)
		return nil, &resp
	}
	if c.strictTranscript {
		if _, err := verifier.recordSignedEnvelope(localEnvelope); err != nil {
			resp := c.failForPubKeys(err.Error(), frostRoundShare, nil)
			return nil, &resp
		}
	}
	verificationBatch := newFrostVerificationBatcher(frostRoundShare, c.localPubKey, len(activeSet.originalByNormalized))
	defer func() {
		if err := c.flushTranscriptVerifications(verificationBatch, activePeerIDs); err != nil && failure == nil {
			values = nil
			response := c.failForPubKeys(fmt.Sprintf("failed to broadcast share verification: %v", err), frostRoundShare, nil)
			failure = &response
		}
	}()
	if err := c.queueSignedTranscriptVerification(verificationBatch, localEnvelope, activePeerIDs); err != nil {
		resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast share verification: %v", err), frostRoundShare, nil)
		return nil, &resp
	}
	if _, failure := verifier.recordVerification(c.localPubKey, local.SenderPubKey, local.ParticipantIndex, localHash); failure != nil {
		resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNSHARE, failure.blamePubKey)
		return nil, &resp
	}

	values = map[string]string{
		strconv.FormatUint(uint64(local.ParticipantIndex), 10): local.ShareHex,
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	flushTicker := time.NewTicker(frostVerificationBatchInterval)
	defer flushTicker.Stop()

	for !verifier.isComplete() {
		select {
		case msg := <-c.inbox.share:
			if msg == nil {
				continue
			}
			// Narrowing leaves queued frames from removed peers; discard them before parsing.
			if _, active := activePeers[msg.PeerID]; !active {
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
			case messages.TSSFrostShareMsg:
				var incoming FrostSharePayload
				if err := json.Unmarshal(wrapped.Payload, &incoming); err != nil {
					continue
				}
				senderNorm := normalizePubKey(incoming.SenderPubKey)
				if senderNorm == "" {
					continue
				}
				if _, ok := activeSet.originalByNormalized[senderNorm]; !ok {
					continue
				}
				if err := c.validateSenderPeerBinding(msg.PeerID, incoming.SenderPubKey); err != nil {
					continue
				}
				expectedIndex := participantIndexMap[senderNorm]
				if incoming.ParticipantIndex != expectedIndex {
					continue
				}
				if strings.TrimSpace(incoming.ShareHex) == "" {
					continue
				}
				payloadHash, err := FrostTranscriptShareHash(c.msgID, incoming)
				if err != nil {
					continue
				}
				incomingEnvelope := frostEnvelopeFromShare(c.msgID, c.vaultPubKey, c.network, incoming, payloadHash)
				if c.strictTranscript {
					if err := verifyFrostSignedEnvelope(incomingEnvelope); err != nil {
						c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("ignoring unsigned share payload")
						continue
					}
					if evidence, err := verifier.recordSignedEnvelope(incomingEnvelope); err != nil {
						continue
					} else if evidence != nil {
						resp := c.failForEquivocation(*evidence)
						return nil, &resp
					}
				}
				isNewPayload, failure := verifier.recordPayload(incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash)
				if failure != nil {
					resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNSHARE, failure.blamePubKey)
					return nil, &resp
				}
				if !isNewPayload {
					continue
				}
				values[strconv.FormatUint(uint64(incoming.ParticipantIndex), 10)] = incoming.ShareHex
				if err := c.queueSignedTranscriptVerification(verificationBatch, incomingEnvelope, activePeerIDs); err != nil {
					resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast share verification: %v", err), frostRoundShare, nil)
					return nil, &resp
				}
				if _, failure := verifier.recordVerification(c.localPubKey, incoming.SenderPubKey, incoming.ParticipantIndex, payloadHash); failure != nil {
					resp := c.failForPubKeys(failure.reason, messages.FROSTKEYSIGNSHARE, failure.blamePubKey)
					return nil, &resp
				}
			}
		case msg := <-c.inbox.verify:
			if response := c.routeVerificationMessage(msg, frostRoundShare); response != nil {
				return nil, response
			}
		case <-flushTicker.C:
			if err := c.flushTranscriptVerifications(verificationBatch, activePeerIDs); err != nil {
				resp := c.failForPubKeys(fmt.Sprintf("failed to broadcast share verification: %v", err), frostRoundShare, nil)
				return nil, &resp
			}
		case <-timer.C:
			missingPayloads := verifier.missingPayloadPubKeys()
			missingVerifiers := verifier.missingVerifierPubKeys()
			resp := c.failForPubKeys(
				transcriptTimeoutReason("share payloads", missingPayloads, missingVerifiers),
				frostRoundShare,
				nil,
			)
			return nil, &resp
		}
	}

	return values, nil
}

func (c *FrostSignCoordinator) WaitForResult(activeParticipants []string, quorum int, timeout time.Duration) (FrostKeySignResponse, error) {
	_, _, activeByNormalized, err := buildParticipantIndex(activeParticipants)
	if err != nil {
		return FrostKeySignResponse{}, err
	}
	if quorum <= 0 {
		threshold, thresholdErr := conversion.GetThreshold(len(activeByNormalized))
		if thresholdErr != nil {
			return FrostKeySignResponse{}, thresholdErr
		}
		quorum = threshold + 1
	}
	if quorum <= 0 {
		return FrostKeySignResponse{}, fmt.Errorf("invalid quorum %d", quorum)
	}
	if quorum > len(activeByNormalized) {
		return FrostKeySignResponse{}, fmt.Errorf(
			"invalid quorum %d: only %d active participants",
			quorum,
			len(activeByNormalized),
		)
	}
	if timeout <= 0 {
		timeout = c.timeout
	}

	// Votes count only from active signers bound to their peer, so frames
	// from any other peer are dropped before parsing. After narrowing, the
	// result queue can still hold large frames from removed peers.
	activePeers := make(map[peer.ID]struct{}, len(activeByNormalized))
	for _, pubKey := range activeByNormalized {
		if id, idErr := conversion.GetPeerIDFromPubKey(pubKey); idErr == nil {
			activePeers[id] = struct{}{}
		}
	}
	buckets := make(map[string]*resultVoteBucket, len(activeByNormalized))
	malformedBuckets := make(map[string]*malformedResultVoteBucket, len(activeByNormalized))
	senderVotes := make(map[string]senderResultVote, len(activeByNormalized))
	equivocated := make(map[string]struct{}, len(activeByNormalized))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case msg := <-c.inbox.result:
			if msg == nil {
				continue
			}
			// Narrowing leaves queued frames from removed peers; discard them before parsing.
			if _, active := activePeers[msg.PeerID]; !active {
				continue
			}
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok || wrapped.MessageType != messages.TSSFrostResultMsg {
				continue
			}
			if strings.TrimSpace(wrapped.MsgID) != c.msgID {
				continue
			}
			var payload FrostResultPayload
			if err := json.Unmarshal(wrapped.Payload, &payload); err != nil {
				c.logger.Warn().Err(err).Str("peer", msg.PeerID.String()).Msg("skipping malformed frost result payload")
				continue
			}
			senderNorm := normalizePubKey(payload.SenderPubKey)
			if senderNorm == "" {
				c.logger.Warn().Str("peer", msg.PeerID.String()).Msg("skipping frost result with empty sender pubkey")
				continue
			}
			if _, ok := activeByNormalized[senderNorm]; !ok {
				c.logger.Warn().Str("sender", payload.SenderPubKey).Msg("skipping frost result from non-active signer")
				continue
			}
			if err := c.validateSenderPeerBinding(msg.PeerID, payload.SenderPubKey); err != nil {
				c.logger.Warn().Err(err).Str("sender", payload.SenderPubKey).Msg("skipping frost result with peer binding mismatch")
				continue
			}

			normalized, matchKey, err := normalizeResultPayload(payload.Response, c.msgID, c.expectedPlanHash)
			if err != nil {
				if _, disqualified := equivocated[senderNorm]; disqualified {
					continue
				}
				if _, alreadyVoted := senderVotes[senderNorm]; alreadyVoted {
					// Retain at most one malformed variant per authenticated
					// sender. A later valid vote may replace it below.
					continue
				}
				reason := truncateFrostResultReason(err.Error())
				c.logger.Warn().Err(err).Str("sender", payload.SenderPubKey).Msg("skipping malformed frost result")
				bucket := malformedBuckets[reason]
				if bucket == nil {
					if len(malformedBuckets) >= len(activeByNormalized) {
						continue
					}
					bucket = &malformedResultVoteBucket{
						reason: reason,
						voters: make(map[string]string),
					}
					malformedBuckets[reason] = bucket
				}
				bucket.voters[senderNorm] = activeByNormalized[senderNorm]
				senderVotes[senderNorm] = senderResultVote{key: reason, malformed: true}
				if len(bucket.voters) >= quorum {
					resp := c.failForPubKeys(reason, messages.FROSTKEYSIGNRESULT, bucket.pubKeys())
					return resp, nil
				}
				continue
			}
			if _, disqualified := equivocated[senderNorm]; disqualified {
				continue
			}
			if existing, alreadyVoted := senderVotes[senderNorm]; alreadyVoted {
				if existing.malformed {
					if malformedBucket := malformedBuckets[existing.key]; malformedBucket != nil {
						delete(malformedBucket.voters, senderNorm)
						if len(malformedBucket.voters) == 0 {
							delete(malformedBuckets, existing.key)
						}
					}
					delete(senderVotes, senderNorm)
				} else if existing.key == matchKey {
					continue
				} else {
					// Two different well-formed results from one authenticated
					// sender are equivocation. Remove its first vote and retain
					// only the bounded disqualification marker.
					if previousBucket := buckets[existing.key]; previousBucket != nil {
						delete(previousBucket.voters, senderNorm)
						if len(previousBucket.voters) == 0 {
							delete(buckets, existing.key)
						}
					}
					delete(senderVotes, senderNorm)
					equivocated[senderNorm] = struct{}{}
					c.logger.Warn().Str("sender", payload.SenderPubKey).Msg("disqualifying frost result sender after conflicting valid votes")
					continue
				}
			}

			// Success and failure results both need a matching active-signer
			// quorum. The eventuality proves plan/hash consistency, but it is not
			// a full local Monero transaction verifier, so one active success is
			// not enough to seed a durable retry guard on non-active nodes.
			bucket := buckets[matchKey]
			if bucket == nil {
				if len(buckets) >= len(activeByNormalized) {
					continue
				}
				bucket = &resultVoteBucket{
					response: normalized,
					voters:   make(map[string]struct{}),
				}
				buckets[matchKey] = bucket
			}
			bucket.voters[senderNorm] = struct{}{}
			senderVotes[senderNorm] = senderResultVote{key: matchKey}
			if len(bucket.voters) >= quorum {
				return bucket.response, nil
			}
		// The caller never resumes keysign phases after entering this waiter: an
		// active signer has a terminal local outcome, while an observer never ran
		// them. Keep consuming their dedicated queues because bounded delivery on
		// a full queue would otherwise park stream handlers until result timeout.
		case <-c.inbox.preprocess:
		case <-c.inbox.share:
		case <-c.inbox.verify:
		case <-timer.C:
			return FrostKeySignResponse{}, errFrostResultTimeout
		}
	}
}

func (c *FrostSignCoordinator) BroadcastResult(resp FrostKeySignResponse, peers []peer.ID) error {
	result := FrostResultPayload{
		SenderPubKey: c.localPubKey,
		Response: FrostResult{
			MsgID:           resp.MsgID,
			TxHash:          resp.TxHash,
			TxHex:           resp.TxHex,
			TxKey:           resp.TxKey,
			FeeAtomic:       resp.FeeAtomic,
			Eventuality:     resp.Eventuality,
			SpentOutputRefs: resp.SpentOutputRefs,
			Status:          uint8(resp.Status),
			Blame:           resp.Blame,
		},
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("fail to marshal frost result payload: %w", err)
	}
	return c.broadcast(messages.TSSFrostResultMsg, payload, peers)
}

func (c *FrostSignCoordinator) broadcast(msgType messages.THORChainTSSMessageType, payload []byte, peers []peer.ID) error {
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

func (c *FrostSignCoordinator) queueSignedTranscriptVerification(
	batch *frostVerificationBatcher,
	target messages.FrostSignedEnvelope,
	peers []peer.ID,
) error {
	if c == nil {
		return fmt.Errorf("FROST keysign coordinator is not initialized")
	}
	if batch == nil {
		return fmt.Errorf("FROST keysign verification batch is not initialized")
	}
	payload := FrostVerifyPayload{
		SenderPubKey:           c.localPubKey,
		Round:                  target.Round,
		TargetSenderPubKey:     target.SenderPubKey,
		TargetParticipantIndex: target.ParticipantIndex,
		PayloadHash:            target.PayloadHash,
	}
	if !c.strictTranscript {
		if err := batch.add(payload); err != nil {
			return err
		}
	} else {
		echo, err := newFrostSignedEcho(c.privateKey, c.localPubKey, target)
		if err != nil {
			return err
		}
		payload.ProtocolVersion = echo.ProtocolVersion
		payload.SenderPubKey = echo.AttesterPubKey
		payload.Round = echo.Target.Round
		payload.TargetSenderPubKey = echo.Target.SenderPubKey
		payload.TargetParticipantIndex = echo.Target.ParticipantIndex
		payload.PayloadHash = echo.Target.PayloadHash
		payload.TargetSignature = echo.Target.Signature
		payload.AttesterSignature = echo.Signature
		if err := batch.add(payload); err != nil {
			return err
		}
	}
	if batch.shouldFlush() {
		return c.flushTranscriptVerifications(batch, peers)
	}
	return nil
}

func (c *FrostSignCoordinator) flushTranscriptVerifications(
	batch *frostVerificationBatcher,
	peers []peer.ID,
) error {
	if batch == nil || len(batch.entries) == 0 {
		return nil
	}
	if c == nil || c.p2pCommunication == nil {
		return fmt.Errorf("FROST keysign communication is not initialized")
	}
	buf, err := json.Marshal(FrostVerifyBatchPayload{Verifications: batch.entries})
	if err != nil {
		return fmt.Errorf("failed to marshal transcript verification batch: %w", err)
	}
	if err := c.broadcast(messages.TSSFrostVerifyMsg, buf, peers); err != nil {
		return err
	}
	batch.reset()
	return nil
}

func (c *FrostSignCoordinator) validateSenderPeerBinding(msgPeer peer.ID, senderPubKey string) error {
	expectedPeerID, err := conversion.GetPeerIDFromPubKey(senderPubKey)
	if err != nil {
		return fmt.Errorf("failed to parse sender pubkey")
	}
	if expectedPeerID != msgPeer {
		return fmt.Errorf("sender peer binding mismatch")
	}
	return nil
}

func (c *FrostSignCoordinator) failForMalformed(msgPeer peer.ID, reason, round string) FrostKeySignResponse {
	pubKey, err := conversion.GetPubKeyFromPeerID(msgPeer.String())
	if err != nil {
		return FrostKeySignResponse{
			Status: tcommon.Fail,
			Blame:  buildBlame(reason, round, nil),
		}
	}
	return c.failForPubKeys(reason, round, []string{pubKey})
}

func (c *FrostSignCoordinator) failForPubKeys(reason, round string, pubKeys []string) FrostKeySignResponse {
	return FrostKeySignResponse{
		Status: tcommon.Fail,
		Blame:  buildBlame(reason, round, pubKeys),
	}
}

func (c *FrostSignCoordinator) failForEquivocation(evidence messages.FrostEquivocationEvidence) FrostKeySignResponse {
	// Signed transcript equivocation is sufficient to abort the local ceremony,
	// but MsgTssKeysignFail has no consensus-side evidence verifier yet. Emitting
	// a culprit or FROSTV2 round here would either fail message construction or,
	// if merely allow-listed, make unvalidated evidence slashable. Keep the
	// diagnostic local until the message ID and handler bind and verify it.
	// Preserve only the ordinary ceremony phase so the signer can apply its
	// terminal-share retry guard without making the evidence actionable.
	c.logger.Error().
		Str("sender", evidence.First.SenderPubKey).
		Str("round", evidence.First.Round).
		Str("first_payload_hash", evidence.First.PayloadHash).
		Str("second_payload_hash", evidence.Second.PayloadHash).
		Msg("detected signed FROST equivocation; on-chain evidence reporting is disabled")
	round := strings.TrimSpace(evidence.First.Round)
	if round != frostRoundPreprocess && round != frostRoundShare {
		round = ""
	}
	return c.failForPubKeys(frostEquivocationFailReason, round, nil)
}

func parseWrappedMessage(raw []byte) (messages.WrappedMessage, bool) {
	var wrapped messages.WrappedMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return messages.WrappedMessage{}, false
	}
	return wrapped, true
}

type resultVoteBucket struct {
	response FrostKeySignResponse
	voters   map[string]struct{}
}

type senderResultVote struct {
	key       string
	malformed bool
}

type malformedResultVoteBucket struct {
	reason string
	voters map[string]string
}

func (b *malformedResultVoteBucket) pubKeys() []string {
	pubKeys := make([]string, 0, len(b.voters))
	for _, pubKey := range b.voters {
		pubKeys = append(pubKeys, pubKey)
	}
	sort.Strings(pubKeys)
	return pubKeys
}

func truncateFrostResultReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) <= frostMaxResultReasonBytes {
		return reason
	}
	return reason[:frostMaxResultReasonBytes]
}

func normalizeResultPayload(result FrostResult, expectedMsgID, expectedPlanHash string) (FrostKeySignResponse, string, error) {
	expectedMsgID = strings.TrimSpace(expectedMsgID)
	expectedPlanHash = strings.ToLower(strings.TrimSpace(expectedPlanHash))
	status := tcommon.Status(result.Status)
	if status != tcommon.Success && status != tcommon.Fail {
		return FrostKeySignResponse{}, "", fmt.Errorf("invalid frost result status")
	}

	normalized := FrostKeySignResponse{
		MsgID:     expectedMsgID,
		Status:    status,
		FeeAtomic: result.FeeAtomic,
	}

	switch status {
	case tcommon.Success:
		if len(result.TxHex) > frostMaxResultTxHexChars {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_hex exceeds limit")
		}
		if len(result.SpentOutputRefs) > frostMaxResultSpentRefs {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result spent refs exceeds limit")
		}
		payloadMsgID := strings.TrimSpace(result.MsgID)
		if payloadMsgID == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has empty msg_id")
		}
		if expectedMsgID != "" && payloadMsgID != expectedMsgID {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result msg_id mismatch")
		}
		txHash := strings.ToLower(strings.TrimSpace(result.TxHash))
		txHex := strings.ToLower(strings.TrimSpace(result.TxHex))
		txKey := result.TxKey
		if txHash == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has empty tx_hash")
		}
		if txHex == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has empty tx_hex")
		}
		expectedTxKeyHash, err := FrostTxKeyHash(txKey)
		if err != nil {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result has invalid tx_key")
		}
		if _, err := hex.DecodeString(txHex); err != nil {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_hex is not valid hex")
		}
		if err := validateHashHex("tx_hash", txHash); err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if expectedPlanHash == "" {
			return FrostKeySignResponse{}, "", fmt.Errorf("expected plan hash is empty")
		}
		eventuality := FrostEventuality{
			Version:       result.Eventuality.Version,
			PlanHash:      strings.ToLower(strings.TrimSpace(result.Eventuality.PlanHash)),
			TxHexHash:     strings.ToLower(strings.TrimSpace(result.Eventuality.TxHexHash)),
			TxKeyHash:     strings.ToLower(strings.TrimSpace(result.Eventuality.TxKeyHash)),
			EventualityID: strings.ToLower(strings.TrimSpace(result.Eventuality.EventualityID)),
		}
		if eventuality.Version != frostEventualityVersion {
			return FrostKeySignResponse{}, "", fmt.Errorf("invalid frost eventuality version")
		}
		if eventuality.PlanHash != expectedPlanHash {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result plan_hash mismatch")
		}
		if err := validateHashHex("tx_hex_hash", eventuality.TxHexHash); err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if eventuality.TxKeyHash != expectedTxKeyHash {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_key_hash mismatch")
		}
		if err := validateHashHex("eventuality_id", eventuality.EventualityID); err != nil {
			return FrostKeySignResponse{}, "", err
		}
		expectedTxHexHash, err := FrostTxHexHash(txHex)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if eventuality.TxHexHash != strings.ToLower(expectedTxHexHash) {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result tx_hex_hash mismatch")
		}
		expectedEventualityID, err := FrostEventualityID(expectedPlanHash, txHash, expectedTxHexHash, expectedTxKeyHash, result.FeeAtomic)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		if eventuality.EventualityID != strings.ToLower(expectedEventualityID) {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost success result eventuality_id mismatch")
		}
		normalizedRefs, err := normalizeFrostResultSpentRefs(result.SpentOutputRefs, txHash)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		normalized.MsgID = payloadMsgID
		normalized.TxHash = txHash
		normalized.TxHex = txHex
		normalized.TxKey = txKey
		normalized.Eventuality = eventuality
		normalized.SpentOutputRefs = normalizedRefs
	case tcommon.Fail:
		if len(result.Blame.BlameNodes) > frostMaxResultBlameNodes {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost fail result blame nodes exceeds limit")
		}
		payloadMsgID := strings.TrimSpace(result.MsgID)
		if payloadMsgID != "" && expectedMsgID != "" && payloadMsgID != expectedMsgID {
			return FrostKeySignResponse{}, "", fmt.Errorf("frost fail result msg_id mismatch")
		}
		normalizedBlame, err := normalizeBlameForResult(result.Blame)
		if err != nil {
			return FrostKeySignResponse{}, "", err
		}
		normalized.Blame = normalizedBlame
	}

	keyBytes, err := json.Marshal(struct {
		Status          tcommon.Status        `json:"status"`
		MsgID           string                `json:"msg_id"`
		TxHash          string                `json:"tx_hash"`
		TxHex           string                `json:"tx_hex"`
		TxKey           string                `json:"tx_key"`
		FeeAtomic       uint64                `json:"fee_atomic"`
		Eventuality     FrostEventuality      `json:"eventuality"`
		SpentOutputRefs []FrostSpentOutputRef `json:"spent_output_refs"`
		Blame           blame.Blame           `json:"blame"`
	}{
		Status:          normalized.Status,
		MsgID:           normalized.MsgID,
		TxHash:          normalized.TxHash,
		TxHex:           normalized.TxHex,
		TxKey:           normalized.TxKey,
		FeeAtomic:       normalized.FeeAtomic,
		Eventuality:     normalized.Eventuality,
		SpentOutputRefs: normalized.SpentOutputRefs,
		Blame:           normalized.Blame,
	})
	if err != nil {
		return FrostKeySignResponse{}, "", fmt.Errorf("failed to encode frost result comparison key")
	}
	matchKey, err := tcommon.MsgToHashString(keyBytes)
	if err != nil {
		return FrostKeySignResponse{}, "", fmt.Errorf("failed to hash frost result comparison key")
	}

	return normalized, matchKey, nil
}

func normalizeBlameForResult(in blame.Blame) (blame.Blame, error) {
	reason := strings.TrimSpace(in.FailReason)
	if reason == "" {
		return blame.Blame{}, fmt.Errorf("frost fail result missing blame reason")
	}
	if len(reason) > frostMaxResultReasonBytes {
		return blame.Blame{}, fmt.Errorf("frost fail result reason exceeds limit")
	}
	if len(in.BlameNodes) > frostMaxResultBlameNodes {
		return blame.Blame{}, fmt.Errorf("frost fail result blame nodes exceeds limit")
	}
	if len(in.BlameNodes) == 0 {
		return blame.Blame{}, fmt.Errorf("frost fail result missing blame nodes")
	}

	nodes := make([]blame.Node, 0, len(in.BlameNodes))
	seen := make(map[string]struct{}, len(in.BlameNodes))
	for _, node := range in.BlameNodes {
		if len(node.BlameData)+len(node.BlameSignature) > frostMaxResultEvidenceBytes {
			return blame.Blame{}, fmt.Errorf("frost fail result evidence exceeds limit")
		}
		pubKey := strings.TrimSpace(node.Pubkey)
		if pubKey == "" {
			return blame.Blame{}, fmt.Errorf("frost fail result contains empty blame node pubkey")
		}
		key := strings.Join(
			[]string{
				strings.ToLower(pubKey),
				hex.EncodeToString(node.BlameData),
				hex.EncodeToString(node.BlameSignature),
			},
			"|",
		)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		nodes = append(nodes, blame.Node{
			Pubkey:         pubKey,
			BlameData:      node.BlameData,
			BlameSignature: node.BlameSignature,
		})
	}
	if len(nodes) == 0 {
		return blame.Blame{}, fmt.Errorf("frost fail result missing blame nodes")
	}
	sort.Slice(nodes, func(i, j int) bool {
		iKey := strings.ToLower(nodes[i].Pubkey) + "|" + hex.EncodeToString(nodes[i].BlameData) + "|" + hex.EncodeToString(nodes[i].BlameSignature)
		jKey := strings.ToLower(nodes[j].Pubkey) + "|" + hex.EncodeToString(nodes[j].BlameData) + "|" + hex.EncodeToString(nodes[j].BlameSignature)
		return iKey < jKey
	})

	normalized := blame.NewBlame(reason, nodes)
	normalized.IsUnicast = in.IsUnicast
	normalized.Round = strings.TrimSpace(in.Round)
	return normalized, nil
}

type activeSignerSet struct {
	originalByNormalized map[string]string
}

func (a *activeSignerSet) missingPubKeys(seen map[string]struct{}) []string {
	missing := make([]string, 0)
	for norm, original := range a.originalByNormalized {
		if _, ok := seen[norm]; ok {
			continue
		}
		missing = append(missing, original)
	}
	sort.Strings(missing)
	return missing
}

func buildActiveSet(activeParticipants []string, known map[string]string) (*activeSignerSet, error) {
	set := make(map[string]string, len(activeParticipants))
	for _, participant := range activeParticipants {
		norm := normalizePubKey(participant)
		if norm == "" {
			continue
		}
		original, ok := known[norm]
		if !ok {
			return nil, fmt.Errorf("active participant %s is not in signer set", participant)
		}
		set[norm] = original
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("active signer set is empty")
	}
	return &activeSignerSet{originalByNormalized: set}, nil
}

func buildBlame(reason, round string, pubKeys []string) blame.Blame {
	nodes := make([]blame.Node, 0, len(pubKeys))
	seen := make(map[string]struct{}, len(pubKeys))
	for _, pubKey := range pubKeys {
		pubKey = strings.TrimSpace(pubKey)
		if pubKey == "" {
			continue
		}
		if _, ok := seen[pubKey]; ok {
			continue
		}
		seen[pubKey] = struct{}{}
		nodes = append(nodes, blame.NewNode(pubKey, nil, nil))
	}
	b := blame.NewBlame(reason, nodes)
	b.Round = round
	return b
}

func peerIDsToStrings(pIDs []peer.ID) []string {
	out := make([]string, 0, len(pIDs))
	for _, id := range pIDs {
		out = append(out, id.String())
	}
	return out
}

func buildParticipantIndex(members []string) ([]string, map[string]uint32, map[string]string, error) {
	originalByNormalized := make(map[string]string, len(members))
	normalized := make([]string, 0, len(members))
	for _, member := range members {
		norm := normalizePubKey(member)
		if norm == "" {
			continue
		}
		if _, exists := originalByNormalized[norm]; exists {
			return nil, nil, nil, fmt.Errorf("duplicate signer pubkey %s", member)
		}
		originalByNormalized[norm] = strings.TrimSpace(member)
		normalized = append(normalized, norm)
	}
	if len(normalized) == 0 {
		return nil, nil, nil, fmt.Errorf("signer pub keys are empty")
	}
	sort.Strings(normalized)
	indexMap := make(map[string]uint32, len(normalized))
	for i, norm := range normalized {
		indexMap[norm] = uint32(i + 1)
	}
	return normalized, indexMap, originalByNormalized, nil
}

// normalizePubKey canonicalizes pubkey strings for set membership checks.
func normalizePubKey(pubKey string) string {
	return strings.ToLower(strings.TrimSpace(pubKey))
}

// containsNormalizedPubKey reports whether pubKeys contains pubKey after the
// same trimming and case-folding used in FROST peer validation.
func containsNormalizedPubKey(pubKeys []string, pubKey string) bool {
	target := normalizePubKey(pubKey)
	if target == "" {
		return false
	}
	for _, candidate := range pubKeys {
		if normalizePubKey(candidate) == target {
			return true
		}
	}
	return false
}
