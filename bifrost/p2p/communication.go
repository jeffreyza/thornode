package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	tcrypto "github.com/cometbft/cometbft/crypto"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	peerstorecore "github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	discovery "github.com/libp2p/go-libp2p-discovery"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-peerstore/addr"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	maddr "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

var (
	joinPartyProtocol           protocol.ID = "/p2p/join-party"
	joinPartyProtocolWithLeader protocol.ID = "/p2p/join-party-leader"
)

// bootstrapRetrySleep is the wait between bootstrap connection rounds. Tests of
// unreachable bootstrap peers shorten it; the number of rounds still derives
// from bootstrapRetryInterval.
var bootstrapRetrySleep = bootstrapRetryInterval

// TSSProtocolID protocol id used for tss
var (
	TSSProtocolID        protocol.ID = "/p2p/tss"
	ObservedTxProtocolID protocol.ID = "/p2p/observed-tx"
)

const (
	// TimeoutConnecting maximum time for wait for peers to connect
	TimeoutConnecting      = time.Second * 20
	bootstrapRetryInterval = time.Second * 5

	StreamUnknown = "UNKNOWN"
	StreamMsgDone = "done"

	// FrostMaxSessionPayload bounds a single FROST outer frame before its full
	// allocation. Monero transactions and DKG maps are well below this ceiling;
	// keeping a distinct limit prevents the 20 MiB legacy TSS frame allowance
	// from becoming the FROST session allowance.
	FrostMaxSessionPayload = 1 * 1024 * 1024
	// FrostSessionQueueSize bounds each coordinator topic queue. FROST keygen
	// applies the smaller control-frame limit below to four of its five topics;
	// only encrypted round-2 shares retain the 1 MiB ceiling. Its maximum channel
	// retention is therefore roughly 64 * (1 MiB + 4 * 64 KiB) = 80 MiB. This is
	// not an aggregate process-memory bound: blocked handlers retain frames too,
	// subject to the per-topic session budgets. Narrowing releases removed peers'
	// charges and wakes their blocked handlers; cancellation resets frames still
	// being read. Released charges can outlive only queued frames or deliveries
	// racing with narrowing, as documented on MessageSubscription.deliver.
	FrostSessionQueueSize = 64
	// FrostKeygenControlPayload bounds round 1, round 3, transcript-vote, and
	// terminal-result frames. Maximum-party payload fixtures must remain below
	// this ceiling; round 2 uses FrostMaxSessionPayload because it carries one
	// encrypted share per remote participant.
	FrostKeygenControlPayload = 64 * 1024
	// FrostMaxMessagesPerPeer is the baseline retained/processed traffic bound
	// per peer for one message type and session. Shared FROST verification topics
	// use FrostVerificationMessagesPerPeer because one peer legitimately echoes
	// every participant's transcript in every ceremony round.
	FrostMaxMessagesPerPeer = uint32(128)
	// frostVerificationMessageHeadroom permits a small, bounded amount of
	// delivery overlap without weakening the cumulative byte limits below.
	frostVerificationMessageHeadroom = uint32(16)
	// FrostMaxBytesPerPeer is a per-peer MEMORY backstop, not a fairness
	// guarantee. It is a fixed constant, so it does not scale down with party
	// size: at the maximum ~128-member vault it is ~16x a peer's fair share of
	// FrostMaxSessionBytes, which means a handful of authenticated members
	// (FrostMaxSessionBytes/FrostMaxBytesPerPeer = 8) can spend the whole
	// session budget and starve the rest. This is tolerated because such an
	// attacker must already be an authorized party member, who can fail the
	// ceremony by other means (bad share, refusing to sign) and is attributable
	// via equivocation evidence. Honest full-size load stays far under the
	// session cap (see TestFrostFullPartyCompletesWithinSessionBudget); the
	// starvation characteristic is pinned by
	// TestFrostPerPeerCapIsAMemoryBackstopNotFairness.
	// FrostMaxSessionBytes bounds cumulative bytes accepted for one type+msgID
	// subscription before any full-frame allocation.
	FrostMaxBytesPerPeer = uint64(8 * 1024 * 1024)
	FrostMaxSessionBytes = uint64(64 * 1024 * 1024)
)

// FrostKeygenVerificationMessagesPerPeer permits one complete-transcript vote
// per DKG round plus bounded duplicate/delivery headroom. Unlike keysign's
// per-target echoes, this limit does not scale with party size.
func FrostKeygenVerificationMessagesPerPeer(rounds int) uint32 {
	if rounds <= 0 {
		return frostVerificationMessageHeadroom
	}
	if uint64(rounds) > uint64(^uint32(0)-frostVerificationMessageHeadroom) {
		return ^uint32(0)
	}
	return uint32(rounds) + frostVerificationMessageHeadroom
}

// FrostVerificationMessagesPerPeer sizes the shared verification-topic count
// for a complete ceremony. The baseline preserves the existing allowance for
// small parties; larger parties receive exactly one echo per participant and
// round plus bounded delivery headroom. Byte budgets remain the hard memory
// bound even if the participant set grows unexpectedly.
func FrostVerificationMessagesPerPeer(partySize, rounds int) uint32 {
	if partySize <= 0 || rounds <= 0 {
		return FrostMaxMessagesPerPeer
	}
	max := ^uint32(0)
	party := uint64(partySize)
	roundCount := uint64(rounds)
	if party > uint64(max-frostVerificationMessageHeadroom)/roundCount {
		return max
	}
	limit := uint32(party*roundCount) + frostVerificationMessageHeadroom
	if limit < FrostMaxMessagesPerPeer {
		return FrostMaxMessagesPerPeer
	}
	return limit
}

// Message that get transfer across the wire
type Message struct {
	PeerID  peer.ID
	Payload []byte
}

const maxTSSFrameMsgIDBytes = 256

type tssFrameHeader struct {
	MessageType messages.THORChainTSSMessageType
	MsgID       string
}

// decodeTSSFrameHeader parses only the canonical fields emitted before the
// base64 payload in WrappedMessage. Non-canonical/oversized headers fail
// closed instead of forcing allocation of the declared outer frame.
func decodeTSSFrameHeader(prefix []byte) (tssFrameHeader, error) {
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	token, err := decoder.Token()
	if err != nil {
		return tssFrameHeader{}, fmt.Errorf("invalid TSS frame header: %w", err)
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return tssFrameHeader{}, fmt.Errorf("TSS frame is not a JSON object")
	}

	var header tssFrameHeader
	seenType := false
	seenMsgID := false
	for {
		token, err = decoder.Token()
		if err != nil {
			return tssFrameHeader{}, fmt.Errorf("TSS frame header exceeds bounded prefix")
		}
		if closing, ok := token.(json.Delim); ok && closing == '}' {
			return tssFrameHeader{}, fmt.Errorf("TSS frame header is incomplete")
		}
		field, ok := token.(string)
		if !ok {
			return tssFrameHeader{}, fmt.Errorf("invalid TSS frame header field")
		}
		switch field {
		case "message_type":
			if seenType {
				return tssFrameHeader{}, fmt.Errorf("duplicate TSS message_type header")
			}
			if err := decoder.Decode(&header.MessageType); err != nil {
				return tssFrameHeader{}, fmt.Errorf("invalid TSS message_type header")
			}
			seenType = true
		case "message_id":
			if seenMsgID {
				return tssFrameHeader{}, fmt.Errorf("duplicate TSS message_id header")
			}
			if err := decoder.Decode(&header.MsgID); err != nil {
				return tssFrameHeader{}, fmt.Errorf("invalid TSS message_id header")
			}
			if !validTSSFrameMsgID(header.MsgID) {
				return tssFrameHeader{}, fmt.Errorf("invalid TSS message_id header")
			}
			seenMsgID = true
		case "payload":
			return tssFrameHeader{}, fmt.Errorf("TSS payload appeared before complete session header")
		default:
			return tssFrameHeader{}, fmt.Errorf("unexpected TSS frame header field")
		}
		if seenType && seenMsgID {
			return header, nil
		}
	}
}

func validTSSFrameMsgID(msgID string) bool {
	if msgID == "" || len(msgID) > maxTSSFrameMsgIDBytes {
		return false
	}
	for i := 0; i < len(msgID); i++ {
		if msgID[i] < 0x21 || msgID[i] > 0x7e {
			return false
		}
	}
	return true
}

// preflightTSSFrame authenticates and reserves exactly one message allowance
// before the outer frame is allocated. Legacy subscriptions retain their
// existing 20 MiB frame limit only when type+msgID actually match.
func (c *Communication) preflightTSSFrame(remotePeer peer.ID, length uint32, prefix []byte) (tssFrameHeader, *MessageSubscription, error) {
	header, err := decodeTSSFrameHeader(prefix)
	if err != nil {
		return tssFrameHeader{}, nil, err
	}
	subscription := c.getSubscriber(header.MessageType, header.MsgID)
	if subscription == nil {
		return tssFrameHeader{}, nil, fmt.Errorf("no matching TSS session subscription")
	}
	if !subscription.accepts(remotePeer, int(length)) {
		return tssFrameHeader{}, nil, fmt.Errorf("unauthorized, over-limit, or exhausted TSS session frame")
	}
	return header, subscription, nil
}

// Communication use p2p to broadcast messages among all the TSS nodes
type Communication struct {
	config           P2PConfig
	stateManager     *storage.FileStateMgr
	logger           zerolog.Logger
	listenAddr       maddr.Multiaddr
	host             host.Host
	wg               *sync.WaitGroup
	stopChan         chan struct{} // channel to indicate whether we should stop
	subscribers      map[messages.THORChainTSSMessageType]*MessageIDSubscriber
	subscriberLocker *sync.Mutex
	streamCount      int64
	BroadcastMsgChan chan *messages.BroadcastMsgChan
	externalAddr     maddr.Multiaddr
	streamMgr        *StreamMgr
	nodeGater        *NodeGater // gater to restrict connections to allowed nodes only
	peerFinder       peerFinder

	// connectToOnePeerFn is a deterministic stream-opening seam for lifecycle
	// tests. Production leaves it nil and opens a fresh libp2p stream normally.
	connectToOnePeerFn func(peer.ID) (network.Stream, error)
}

type peerFinder interface {
	FindPeer(context.Context, peer.ID) (peer.AddrInfo, error)
}

type P2PConfig interface {
	GetBootstrapPeers() ([]maddr.Multiaddr, error)
	GetP2PPort() int
	GetExternalIP() string
	GetRendezvous() string
}

func StartP2P(
	cfg P2PConfig,
	priKey tcrypto.PrivKey,
	baseFolder string,
) (*Communication, *storage.FileStateMgr, error) {
	return StartP2PWithBridge(cfg, priKey, baseFolder, nil)
}

// StartP2PWithBridge starts the P2P communication layer with optional node gating.
// If bridge is provided, only active or ready nodes will be allowed for inbound connections.
func StartP2PWithBridge(
	cfg P2PConfig,
	priKey tcrypto.PrivKey,
	baseFolder string,
	bridge thorclient.ThorchainBridge,
) (*Communication, *storage.FileStateMgr, error) {
	var stateOpts []storage.FileStateMgrOption
	if seedPhrase, err := common.GetSignerSeedPhrase(); err == nil {
		stateOpts = append(stateOpts, storage.WithAtRestEncryption(seedPhrase))
	} else {
		log.Warn().Err(err).Msg("signer seed phrase unavailable, keyshare at-rest encryption disabled")
	}
	stateManager, err := storage.NewFileStateMgr(baseFolder, stateOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to create file state manager")
	}

	comm, err := NewCommunication(cfg, stateManager, bridge)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to create communication layer: %w", err)
	}

	priKeyRawBytes, err := conversion.GetPriKeyRawBytes(priKey)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get private key")
	}
	if err := comm.Start(priKeyRawBytes); nil != err {
		return nil, nil, fmt.Errorf("fail to start p2p network: %w", err)
	}

	return comm, stateManager, nil
}

// NewCommunication create a new instance of Communication
func NewCommunication(cfg P2PConfig, stateManager *storage.FileStateMgr, bridge thorclient.ThorchainBridge) (*Communication, error) {
	port, externalIP := cfg.GetP2PPort(), cfg.GetExternalIP()
	addr, err := maddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))
	if err != nil {
		return nil, fmt.Errorf("fail to create listen addr: %w", err)
	}
	var externalAddr maddr.Multiaddr = nil
	if len(externalIP) != 0 {
		externalAddr, err = maddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", externalIP, port))
		if err != nil {
			return nil, fmt.Errorf("fail to create listen with given external IP: %w", err)
		}
	}

	comm := &Communication{
		config:           cfg,
		stateManager:     stateManager,
		logger:           log.With().Str("module", "communication").Logger(),
		listenAddr:       addr,
		wg:               &sync.WaitGroup{},
		stopChan:         make(chan struct{}),
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamCount:      0,
		BroadcastMsgChan: make(chan *messages.BroadcastMsgChan, 1024),
		externalAddr:     externalAddr,
		streamMgr:        NewStreamMgr(),
	}

	// If bridge is provided, create the node gater
	if bridge != nil {
		// Create gater with 60 second refresh interval
		gater := NewNodeGater(bridge, 60*time.Second)
		comm.nodeGater = gater
		comm.logger.Info().Msg("node gater enabled - only allowed nodes can connect")
	}

	return comm, nil
}

// GetHost return the host
func (c *Communication) GetHost() host.Host {
	return c.host
}

// GetLocalPeerID from p2p host
func (c *Communication) GetLocalPeerID() string {
	return c.host.ID().String()
}

const (
	streamWriteAttempts = 3

	// streamWriteRetryDelay is the base backoff between write attempts; it is
	// multiplied by the attempt number, giving 2s then 4s. Deliberately longer
	// than a network round trip: libp2p keeps its own dial backoff per peer, so
	// a sub-second retry lands inside that window and fails instantly. Spacing
	// at 2s/4s gives the backoff a chance to clear.
	streamWriteRetryDelay = 2 * time.Second

	// streamWriteRetryBudget is a nominal retry deadline checked between
	// attempts. It is not a hard wall-clock cap: an attempt already in progress
	// may run beyond it before the loop decides whether another attempt may start.
	streamWriteRetryBudget = 60 * time.Second
)

type tssStreamPolicy struct {
	attempts  int
	fullClose bool
}

// Broadcast message to Peers. The message policy decides whether a failed
// write is retried and whether both endpoints fully close the stream.
func (c *Communication) Broadcast(peers []peer.ID, msg []byte, msgID string, msgType messages.THORChainTSSMessageType) {
	if len(peers) == 0 {
		return
	}
	c.wg.Add(1)
	go c.broadcastToPeers(peers, msg, msgID, msgType, streamPolicyFor(msgType))
}

// streamPolicyFor keeps retry and close behavior together so a newly added
// FROST message cannot accidentally get only half of its stream lifecycle.
func streamPolicyFor(msgType messages.THORChainTSSMessageType) tssStreamPolicy {
	switch msgType {
	case messages.TSSFrostDKGRound1Msg,
		messages.TSSFrostDKGRound2Msg,
		messages.TSSFrostDKGRound3Msg,
		messages.TSSFrostDKGResultMsg:
		return tssStreamPolicy{attempts: streamWriteAttempts, fullClose: true}
	case messages.TSSFrostPreprocessMsg,
		messages.TSSFrostShareMsg,
		messages.TSSFrostVerifyMsg,
		messages.TSSFrostResultMsg:
		return tssStreamPolicy{attempts: 1, fullClose: true}
	case messages.TSSKeyGenMsg, messages.TSSKeyGenVerMsg:
		return tssStreamPolicy{attempts: streamWriteAttempts}
	default:
		return tssStreamPolicy{attempts: 1}
	}
}

func (c *Communication) broadcastToPeers(
	peers []peer.ID,
	msg []byte,
	msgID string,
	msgType messages.THORChainTSSMessageType,
	policy tssStreamPolicy,
) {
	defer c.wg.Done()
	defer func() {
		c.logger.Debug().Msgf("finished sending message to peer(%v)", peers)
	}()
	var (
		wgSend    sync.WaitGroup
		failed    int64
		recovered int64
	)
	start := time.Now()
	wgSend.Add(len(peers))
	for _, p := range peers {
		go func(p peer.ID) {
			defer wgSend.Done()
			attempts, err := c.writeToStreamWithRetry(p, msg, msgID, policy.attempts, policy.fullClose)
			if err != nil {
				atomic.AddInt64(&failed, 1)
				c.logger.Error().Err(err).
					Str("peer", p.String()).
					Str("msgID", msgID).
					Int("attempts", attempts).
					Msg("fail to write to stream")
				return
			}
			if attempts > 1 {
				atomic.AddInt64(&recovered, 1)
			}
		}(p)
	}
	wgSend.Wait()

	if f, r := atomic.LoadInt64(&failed), atomic.LoadInt64(&recovered); f > 0 || r > 0 {
		c.logger.Info().
			Str("msgID", msgID).
			Str("msgType", msgType.String()).
			Int("peers", len(peers)).
			Int("bytes", len(msg)).
			Int64("recovered", r).
			Int64("failed", f).
			Dur("elapsed", time.Since(start)).
			Msg("tss fanout retry summary")
	}
}

// writeToStreamWithRetry pushes msg to a single peer on a fresh stream per
// attempt. The nominal budget is checked only between attempts.
func (c *Communication) writeToStreamWithRetry(
	pID peer.ID,
	msg []byte,
	msgID string,
	maxAttempts int,
	fullClose bool,
) (int, error) {
	return c.writeToStreamWithRetryDelay(pID, msg, msgID, maxAttempts, fullClose, streamWriteRetryDelay)
}

func (c *Communication) writeToStreamWithRetryDelay(
	pID peer.ID,
	msg []byte,
	msgID string,
	maxAttempts int,
	fullClose bool,
	retryDelay time.Duration,
) (int, error) {
	var err error
	deadline := time.Now().Add(streamWriteRetryBudget)
	attempt := 0
	for attempt < maxAttempts {
		attempt++
		err = c.writeToStream(pID, msg, msgID, fullClose)
		if err == nil {
			if attempt > 1 {
				c.logger.Info().
					Str("peer", pID.String()).
					Str("msgID", msgID).
					Int("attempt", attempt).
					Msg("stream write recovered on retry")
			}
			return attempt, nil
		}
		if attempt >= maxAttempts || time.Now().After(deadline) {
			break
		}
		c.logger.Debug().Err(err).
			Str("peer", pID.String()).
			Str("msgID", msgID).
			Msg("stream write failed, retrying on a new stream")
		select {
		case <-c.stopChan:
			return attempt, err
		case <-time.After(retryDelay * time.Duration(attempt)):
		}
	}
	return attempt, err
}

func (c *Communication) writeToStream(pID peer.ID, msg []byte, msgID string, fullClose bool) error {
	// don't send to ourselves
	if c.host != nil && pID == c.host.ID() {
		return nil
	}
	stream, err := c.openStreamToPeer(pID)
	if err != nil {
		return fmt.Errorf("fail to open stream to peer(%s): %w", pID, err)
	}
	if nil == stream {
		return nil
	}

	c.logger.Debug().Msgf(">>>writing messages to peer(%s)", pID)

	if err := writeStreamWithBuffer(msg, stream, !fullClose); err != nil {
		if fullClose {
			_ = stream.Reset()
		} else {
			c.streamMgr.AddStream(msgID, stream)
		}
		return err
	}
	return c.finishWrittenTSSStream(stream, msgID, fullClose)
}

func (c *Communication) openStreamToPeer(pID peer.ID) (network.Stream, error) {
	if c.connectToOnePeerFn != nil {
		return c.connectToOnePeerFn(pID)
	}
	return c.connectToOnePeer(pID)
}

// finishWrittenTSSStream gives successful FROST writes immediate terminal
// ownership. Legacy TSS streams retain their ceremony-scoped release behavior.
func (c *Communication) finishWrittenTSSStream(stream network.Stream, msgID string, fullClose bool) error {
	if fullClose {
		if err := closeStreamAndDrain(stream); err != nil {
			return fmt.Errorf("fail to close FROST stream: %w", err)
		}
		return nil
	}
	c.streamMgr.AddStream(msgID, stream)
	return nil
}

func (c *Communication) handleStreamTss(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	peerID := remotePeer.String()
	c.logger.Debug().Msgf("reading from tss stream of peer: %s", peerID)

	select {
	case <-c.stopChan:
		_ = stream.Reset()
		return
	default:
		var preflightHeader tssFrameHeader
		var subscription *MessageSubscription
		var unwatch func() bool
		dataBuf, err := readStreamWithBufferPreflight(stream, TSSFramePreflightBytes, func(length uint32, prefix []byte) error {
			var preflightErr error
			preflightHeader, subscription, preflightErr = c.preflightTSSFrame(remotePeer, length, prefix)
			if preflightErr == nil && streamPolicyFor(preflightHeader.MessageType).fullClose {
				// A charged FROST frame must not keep its buffer and handler
				// until the read deadline once its ceremony ends. Cancellation
				// triggers this reset in its own goroutine, so stop never waits
				// on transport I/O, and no goroutine exists until then.
				unwatch = context.AfterFunc(subscription.ctx, func() { _ = stream.Reset() })
			}
			return preflightErr
		})
		if unwatch != nil && !unwatch() {
			// Cancellation already reset the stream; the frame cannot be
			// delivered to a stopped subscription.
			return
		}
		// A FROST stream carries exactly one frame. This handler owns it: it
		// resets the stream on any failure and fully closes it after delivery.
		// Registry, narrowing, and coordinator paths never wait on its
		// transport I/O; cancellation only triggers the reset this handler
		// registered, which runs in its own goroutine. Legacy TSS streams keep
		// their ceremony-scoped release through streamMgr.
		frost := subscription != nil && streamPolicyFor(preflightHeader.MessageType).fullClose
		if err != nil {
			c.logger.Error().Err(err).Msgf("fail to read from stream,peerID: %s", peerID)
			if frost || errors.Is(err, ErrTSSFramePreflightRejected) {
				if resetErr := stream.Reset(); resetErr != nil {
					c.logger.Debug().Err(resetErr).Msg("fail to reset rejected TSS stream")
				}
				return
			}
			c.streamMgr.AddStream(StreamUnknown, stream)
			return
		}
		var wrappedMsg messages.WrappedMessage
		if err := json.Unmarshal(dataBuf, &wrappedMsg); nil != err {
			c.logger.Error().Err(err).Msg("fail to unmarshal wrapped message bytes")
			if frost {
				_ = stream.Reset()
			} else {
				c.streamMgr.AddStream(StreamUnknown, stream)
			}
			return
		}
		if wrappedMsg.MessageType != preflightHeader.MessageType || wrappedMsg.MsgID != preflightHeader.MsgID {
			c.logger.Warn().Msg("dropping TSS frame whose full message changed its preflight session header")
			if err := stream.Reset(); err != nil {
				c.logger.Debug().Err(err).Msg("fail to reset header-mismatched TSS stream")
			}
			return
		}
		if current := c.getSubscriber(wrappedMsg.MessageType, wrappedMsg.MsgID); current != subscription ||
			!subscription.authorized(remotePeer, len(dataBuf)) {
			c.logger.Warn().Msg("dropping TSS frame whose subscription changed after preflight")
			if err := stream.Reset(); err != nil {
				c.logger.Debug().Err(err).Msg("fail to reset stale-subscription TSS stream")
			}
			return
		}
		c.logger.Debug().
			Str("message_type", wrappedMsg.MessageType.String()).
			Str("msg_id", wrappedMsg.MsgID).
			Int("payload_bytes", len(wrappedMsg.Payload)).
			Msg("received TSS message")
		message := &Message{
			PeerID:  remotePeer,
			Payload: dataBuf,
		}
		// Park legacy streams before delivery so a consumer cannot race
		// ReleaseStream ahead of this stream being tracked.
		if !frost {
			c.streamMgr.AddStream(wrappedMsg.MsgID, stream)
		}
		// Bounded FROST queues apply backpressure rather than dropping a valid
		// ceremony frame during a burst.
		if !subscription.deliver(message, c.stopChan) {
			c.logger.Warn().
				Str("peer", remotePeer.String()).
				Str("msg_id", wrappedMsg.MsgID).
				Str("message_type", wrappedMsg.MessageType.String()).
				Msg("stopped before TSS session message delivery completed")
			if err := stream.Reset(); err != nil {
				c.logger.Debug().Err(err).Msg("fail to reset undelivered TSS stream")
			}
			return
		}
		// Deliver first: a slow Close then delays only this handler goroutine,
		// never the coordinator that is already processing the message.
		if frost {
			if err := closeStreamAndDrain(stream); err != nil {
				c.logger.Debug().Err(err).Msg("fail to close delivered FROST stream")
			}
		}
	}
}

func (c *Communication) getPeers() addr.AddrList {
	var bootstrapPeers addr.AddrList

	cfgBoostrapPeers, err := c.config.GetBootstrapPeers()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get bootstrap peers from config")
	} else {
		bootstrapPeers = cfgBoostrapPeers
	}

	if c.stateManager != nil {
		savedPeers, err := c.stateManager.RetrieveP2PAddresses()
		if err != nil {
			c.logger.Error().Err(err).Msg("fail to get saved peers from state manager")
		} else {
			bootstrapPeers = append(bootstrapPeers, savedPeers...)
		}
	}

	// Apply the same allowlist used by the inbound connection gater so that
	// outbound bootstrap dials are restricted to bonded validator peers.
	if c.nodeGater != nil {
		bootstrapPeers = c.nodeGater.FilterAllowedPeers(bootstrapPeers)
	}

	return bootstrapPeers
}

func (c *Communication) bootstrapConnectivityRequired() bool {
	cfgBootstrapPeers, err := c.config.GetBootstrapPeers()
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to get bootstrap peers from config")
		return true
	}
	if len(cfgBootstrapPeers) == 0 {
		return false
	}
	return !allowUnreachableConfiguredBootstrapPeers
}

func (c *Communication) bootStrapConnectivityCheck() error {
	bootstrapPeers := c.getPeers()

	if len(bootstrapPeers) == 0 {
		c.logger.Error().Msg("we do not have the bootstrap node set, quit the connectivity check")
		return nil
	}

	var onlineNodes uint32
	var wg sync.WaitGroup
	for _, el := range bootstrapPeers {
		peer, err := peer.AddrInfoFromP2pAddr(el)
		if err != nil {
			c.logger.Error().Err(err).Msg("error in decode the bootstrap node, skip it")
			continue
		}
		wg.Add(1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			defer cancel()
			defer wg.Done()
			outChan := ping.Ping(ctx, c.host, peer.ID)
			select {
			case ret, ok := <-outChan:
				if !ok {
					return
				}
				if ret.Error == nil {
					c.logger.Debug().Msgf("connect to peer %v with RTT %v\n", peer.ID, ret.RTT)
					atomic.AddUint32(&onlineNodes, 1)
				}
			case <-ctx.Done():
				c.logger.Error().Msgf("fail to ping the node %s within 2 seconds", peer.ID)
			}
		}()
	}
	wg.Wait()

	if onlineNodes > 0 {
		c.logger.Info().Msgf("we have successfully ping pong %d nodes", onlineNodes)
		return nil
	}
	c.logger.Error().Msg("fail to ping any bootstrap node")
	return errors.New("the node cannot ping any bootstrap node")
}

func (c *Communication) startChannel(privKeyBytes []byte) error {
	ctx := context.Background()
	p2pPriKey, err := crypto.UnmarshalSecp256k1PrivateKey(privKeyBytes)
	if err != nil {
		c.logger.Error().Msgf("error is %f", err)
		return err
	}

	addressFactory := func(addrs []maddr.Multiaddr) []maddr.Multiaddr {
		if c.externalAddr != nil {
			return []maddr.Multiaddr{c.externalAddr}
		}
		return addrs
	}

	// Build libp2p options
	opts := []libp2p.Option{
		libp2p.ListenAddrs([]maddr.Multiaddr{c.listenAddr}...),
		libp2p.Identity(p2pPriKey),
		libp2p.AddrsFactory(addressFactory),
	}

	// Add connection gater if configured
	if c.nodeGater != nil {
		c.logger.Info().Msg("configuring libp2p with node gater")
		opts = append(opts, libp2p.ConnectionGater(c.nodeGater))
	}

	h, err := libp2p.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("fail to create p2p host: %w", err)
	}
	c.host = h
	c.logger.Info().Msgf("Host created, we are: %s, at: %s", h.ID(), h.Addrs())
	h.SetStreamHandler(TSSProtocolID, c.handleStreamTss)
	// Start a DHT, for use in peer discovery. We can't just make a new DHT
	// client because we want each peer to maintain its own local copy of the
	// DHT, so that the bootstrapping node of the DHT can go down without
	// inhibiting future peer discovery.
	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return fmt.Errorf("fail to create DHT: %w", err)
	}
	c.peerFinder = kademliaDHT
	c.logger.Debug().Msg("Bootstrapping the DHT")
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("fail to bootstrap DHT: %w", err)
	}

	requireBootstrapConnectivity := c.bootstrapConnectivityRequired()
	var connectionErr error
	for i := 0; i < c.bootstrapRetryAttempts(); i++ {
		connectionErr = c.connectToBootstrapPeers()
		if connectionErr == nil {
			break
		}
		c.logger.Warn().Err(connectionErr).Msgf("cannot connect to any bootstrap node, retry in %s", bootstrapRetrySleep)
		time.Sleep(bootstrapRetrySleep)
	}
	if connectionErr != nil {
		if requireBootstrapConnectivity {
			return fmt.Errorf("fail to connect to bootstrap peer: %w", connectionErr)
		}
		// Mocknet/simulation simultaneous restarts can temporarily make every
		// configured bootstrap peer unreachable. Continue there so the HTTP
		// /p2pid endpoint comes up and peers can later recover through DHT.
		c.logger.Warn().Err(connectionErr).Msg("bootstrap peers unreachable at startup, continuing - will discover via DHT")
	}

	// We use a rendezvous point "meet me here" to announce our location.
	// This is like telling your friends to meet you at the Eiffel Tower.
	routingDiscovery := discovery.NewRoutingDiscovery(kademliaDHT)
	discovery.Advertise(ctx, routingDiscovery, c.config.GetRendezvous())
	if err = c.bootStrapConnectivityCheck(); err != nil {
		if requireBootstrapConnectivity {
			return err
		}
		c.logger.Warn().Err(err).Msg("bootstrap connectivity check failed, continuing")
	}

	c.logger.Info().Msg("Successfully announced!")
	return nil
}

func (c *Communication) bootstrapRetryAttempts() int {
	attempts := 5
	if c == nil || c.nodeGater == nil || c.nodeGater.refreshInterval <= 0 {
		return attempts
	}

	// Give bootstrap peers at least one full node-gater refresh window plus a
	// little extra time so simultaneous bifrost restarts do not fail before the
	// bootstrap node has refreshed its allowlist to include the other nodes.
	gaterAttempts := int(c.nodeGater.refreshInterval/bootstrapRetryInterval) + 2
	if gaterAttempts > attempts {
		return gaterAttempts
	}
	return attempts
}

func (c *Communication) connectToOnePeer(pID peer.ID) (network.Stream, error) {
	c.logger.Debug().Msgf("peer:%s,current:%s", pID, c.host.ID())
	// dont connect to itself
	if pID == c.host.ID() {
		return nil, nil
	}
	c.logger.Debug().Msgf("connect to peer : %s", pID.String())
	ctx, cancel := context.WithTimeout(context.Background(), TimeoutConnecting)
	defer cancel()
	if len(c.host.Peerstore().Addrs(pID)) == 0 {
		if err := c.discoverPeer(ctx, pID); err != nil {
			c.logger.Debug().Err(err).Msg("fail to discover peer before opening stream")
		}
	}
	stream, err := c.host.NewStream(ctx, pID, TSSProtocolID)
	if err != nil {
		return nil, fmt.Errorf("fail to create new stream to peer: %s, %w", pID, err)
	}
	return stream, nil
}

func (c *Communication) discoverPeer(ctx context.Context, pID peer.ID) error {
	if c == nil || c.host == nil {
		return errors.New("communication host is not initialized")
	}
	if len(c.host.Peerstore().Addrs(pID)) > 0 {
		return nil
	}
	if c.peerFinder == nil {
		return errors.New("peer discovery is not initialized")
	}

	info, err := c.peerFinder.FindPeer(ctx, pID)
	if err != nil {
		return err
	}
	if info.ID != pID {
		return fmt.Errorf("discovered unexpected peer %s while looking for %s", info.ID, pID)
	}
	if len(info.Addrs) == 0 {
		return fmt.Errorf("discovered peer %s without addresses", pID)
	}

	c.host.Peerstore().AddAddrs(info.ID, info.Addrs, peerstorecore.TempAddrTTL)
	return nil
}

func (c *Communication) connectToBootstrapPeers() error {
	bootstrapPeers := c.getPeers()
	// Let's connect to the bootstrap nodes first. They will tell us about the
	// other nodes in the network.
	if len(bootstrapPeers) == 0 {
		c.logger.Info().Msg("no bootstrap node set, we skip the connection")
		return nil
	}
	var wg sync.WaitGroup
	connRet := make(chan bool, len(bootstrapPeers))
	dialAttempts := 0
	for _, peerAddr := range bootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			return fmt.Errorf("fail to add peer: %w", err)
		}
		// Skip connecting to ourselves
		if pi.ID == c.host.ID() {
			c.logger.Debug().Msgf("skipping connection to self: %s", pi.ID)
			continue
		}
		dialAttempts++
		wg.Add(1)
		go func(connRet chan bool) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), TimeoutConnecting)
			defer cancel()
			if err := c.host.Connect(ctx, *pi); err != nil {
				c.logger.Error().Err(err).Msgf("fail to connect to %s", pi.String())
				connRet <- false
				return
			}
			connRet <- true
			c.logger.Info().Msgf("Connection established with bootstrap node: %s", *pi)
		}(connRet)
	}
	wg.Wait()
	// Read exactly dialAttempts results, not len(bootstrapPeers). Self entries
	// are skipped above without spawning a goroutine, so reading more than
	// dialAttempts blocks forever waiting for sends that never come.
	if dialAttempts == 0 {
		return nil
	}
	for i := 0; i < dialAttempts; i++ {
		if <-connRet {
			return nil
		}
	}
	return errors.New("fail to connect to any peer")
}

// Start will start the communication
func (c *Communication) Start(priKeyBytes []byte) error {
	// Start the node gater if configured
	if c.nodeGater != nil {
		c.nodeGater.Start()
	}

	err := c.startChannel(priKeyBytes)
	if err == nil {
		c.wg.Add(1)
		go c.ProcessBroadcast()
	} else {
		if c.host != nil {
			if closeErr := c.host.Close(); closeErr != nil {
				c.logger.Err(closeErr).Msg("fail to close partially started host")
			}
			c.host = nil
		}
		if c.nodeGater != nil {
			c.nodeGater.Stop()
		}
	}
	return err
}

// Stop communication
func (c *Communication) Stop() error {
	// Stop the node gater if configured
	if c.nodeGater != nil {
		c.nodeGater.Stop()
	}

	// we need to stop the handler and the p2p services firstly, then terminate the our communication threads
	// host is nil when Start failed and already tore it down
	if c.host != nil {
		if err := c.host.Close(); err != nil {
			c.logger.Err(err).Msg("fail to close host network")
		}
	}

	close(c.stopChan)
	c.wg.Wait()
	return nil
}

func (c *Communication) SetSubscribe(topic messages.THORChainTSSMessageType, msgID string, channel chan *Message) {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()

	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		messageIDSubscribers = NewMessageIDSubscriber()
		c.subscribers[topic] = messageIDSubscribers
	}
	messageIDSubscribers.Subscribe(msgID, channel)
}

// SetSubscribeWithPeers creates a session subscription that accepts messages
// only from the supplied authenticated libp2p peers and applies hard per-
// session resource limits before delivery to the protocol coordinator.
func (c *Communication) SetSubscribeWithPeers(
	topic messages.THORChainTSSMessageType,
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
) {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()

	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		messageIDSubscribers = NewMessageIDSubscriber()
		c.subscribers[topic] = messageIDSubscribers
	}
	// Copy the caller-owned slice so later party filtering cannot mutate the
	// authorization decision of an already registered subscription.
	peers := append([]peer.ID(nil), allowedPeers...)
	messageIDSubscribers.SubscribeRestrictedWithBudgets(
		msgID,
		channel,
		peers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		FrostMaxBytesPerPeer,
		FrostMaxSessionBytes,
	)
}

// NarrowSubscribePeers restricts an existing authenticated allow-list after
// party formation. It rejects re-expansion and never creates a subscription.
func (c *Communication) NarrowSubscribePeers(topic messages.THORChainTSSMessageType, msgID string, allowedPeers []peer.ID) bool {
	c.subscriberLocker.Lock()
	messageIDSubscribers := c.subscribers[topic]
	c.subscriberLocker.Unlock()
	if messageIDSubscribers == nil {
		return false
	}
	return messageIDSubscribers.NarrowAllowedPeers(msgID, append([]peer.ID(nil), allowedPeers...))
}

func (c *Communication) getSubscriber(topic messages.THORChainTSSMessageType, msgID string) *MessageSubscription {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()
	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		c.logger.Debug().Msgf("fail to find subscribers for %s", topic)
		return nil
	}
	return messageIDSubscribers.GetSubscriber(msgID)
}

func (c *Communication) CancelSubscribe(topic messages.THORChainTSSMessageType, msgID string) {
	c.subscriberLocker.Lock()
	defer c.subscriberLocker.Unlock()

	messageIDSubscribers, ok := c.subscribers[topic]
	if !ok {
		c.logger.Debug().Msgf("cannot find the given channels %s", topic.String())
		return
	}
	if nil == messageIDSubscribers {
		return
	}
	messageIDSubscribers.UnSubscribe(msgID)
	if messageIDSubscribers.IsEmpty() {
		delete(c.subscribers, topic)
	}
}

func (c *Communication) ProcessBroadcast() {
	c.logger.Debug().Msg("start to process broadcast message channel")
	defer c.logger.Debug().Msg("stop process broadcast message channel")
	defer c.wg.Done()
	for {
		select {
		case msg := <-c.BroadcastMsgChan:
			wrappedMsgBytes, err := json.Marshal(msg.WrappedMessage)
			if err != nil {
				c.logger.Error().Err(err).Msg("fail to marshal a wrapped message to json bytes")
				continue
			}
			c.logger.Debug().Msgf("broadcast message %s to %+v", msg.WrappedMessage, msg.PeersID)
			c.Broadcast(msg.PeersID, wrappedMsgBytes, msg.WrappedMessage.MsgID, msg.WrappedMessage.MessageType)

		case <-c.stopChan:
			return
		}
	}
}

func (c *Communication) ReleaseStream(msgID string) {
	c.streamMgr.ReleaseStream(msgID)
}
