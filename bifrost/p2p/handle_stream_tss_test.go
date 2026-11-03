package p2p

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// handleStreamTss reads Conn().RemotePeer() and nothing else off the
// connection, so overriding that one method is enough. The embedded nil
// interface makes any other call panic loudly rather than silently pass.
type fakeTSSConn struct {
	network.Conn
	remote peer.ID
}

func (c fakeTSSConn) RemotePeer() peer.ID { return c.remote }

// handlerStream records whether the handler reset it, which is how every
// fail-closed branch in handleStreamTss is supposed to end.
type handlerStream struct {
	*MockNetworkStream
	conn        network.Conn
	resetCalled atomic.Bool
}

func (s *handlerStream) Reset() error {
	s.resetCalled.Store(true)
	return nil
}
func (s *handlerStream) Conn() network.Conn { return s.conn }

func newHandlerStream(remote peer.ID, frame []byte) *handlerStream {
	return newHandlerStreamWithDeclaredLength(remote, uint32(len(frame)), frame)
}

func newHandlerStreamWithDeclaredLength(remote peer.ID, declaredLength uint32, frame []byte) *handlerStream {
	base := NewMockNetworkStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, declaredLength)
	_, _ = base.Write(header)
	_, _ = base.Write(frame)
	return &handlerStream{
		MockNetworkStream: base,
		conn:              fakeTSSConn{remote: remote},
	}
}

func newHandlerCommunication(
	topic messages.THORChainTSSMessageType,
	msgID string,
	allowed []peer.ID,
) *Communication {
	return newHandlerCommunicationWithLimit(topic, msgID, allowed, 8)
}

func newHandlerCommunicationWithLimit(
	topic messages.THORChainTSSMessageType,
	msgID string,
	allowed []peer.ID,
	maxMessages uint32,
) *Communication {
	c := &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
	}
	c.SetSubscribeWithPeers(topic, msgID, make(chan *Message, 4), allowed, FrostMaxSessionPayload, maxMessages)
	return c
}

func parkedStreamCount(sm *StreamMgr) int {
	sm.streamLocker.RLock()
	defer sm.streamLocker.RUnlock()
	total := 0
	for _, streams := range sm.unusedStreams {
		total += len(streams)
	}
	return total
}

// The frame's bounded PREFIX declares the session that preflight authorizes
// against, but the full body is what gets delivered. If the two are allowed to
// disagree, a peer authorized for session A can have its frame delivered into
// session B — it passes preflight under A's allow-list and budgets, then lands
// in B's channel. This is the single most security-load-bearing check in the
// handler, and the preflight tests cannot reach it because they never read a
// full body.
func TestHandleStreamTssRejectsFrameWhosePrefixAndBodyDisagree(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	authorized := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	remote := peer.ID("member-peer")

	// Duplicate JSON keys are what let prefix and body disagree at all:
	// encoding/json takes the LAST occurrence of a repeated key, while the
	// preflight header parser only ever sees the FIRST — it reads a bounded
	// 4096-byte prefix. So a frame can pass preflight as the authorized session
	// (charged against its allow-list and budgets) and then unmarshal as a
	// different one. Pad the payload so the trailing override lands far outside
	// the preflight window.
	padding := strings.Repeat("A", 2*TSSFramePreflightBytes)

	cases := []struct {
		name     string
		override string
	}{
		{
			name:     "body switches msgID",
			override: `"message_id":"other-session"`,
		},
		{
			name:     "body switches message type",
			override: fmt.Sprintf(`"message_type":%d`, messages.TSSFrostResultMsg),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comm := newHandlerCommunication(authorized, msgID, []peer.ID{remote})
			sub := comm.getSubscriber(authorized, msgID)

			frame := []byte(fmt.Sprintf(
				`{"message_type":%d,"message_id":%q,"payload":%q,%s}`,
				authorized, msgID, padding, tc.override,
			))

			// Sanity: the frame really is the attack, i.e. preflight and body
			// genuinely disagree. Otherwise this test proves nothing.
			var decoded messages.WrappedMessage
			if err := json.Unmarshal(frame, &decoded); err != nil {
				t.Fatalf("attack frame must be valid JSON: %v", err)
			}
			if decoded.MessageType == authorized && decoded.MsgID == msgID {
				t.Fatal("frame body did not diverge from its prefix; the test is not exercising the check")
			}

			stream := newHandlerStream(remote, frame)
			comm.handleStreamTss(stream)

			// The divergent frame must never be delivered into the authorized
			// session's channel...
			select {
			case msg := <-sub.channel:
				t.Fatalf("transplanted frame was delivered into the authorized session: %q", msg.Payload)
			default:
			}
			// ...nor into whatever session its body named.
			if other := comm.getSubscriber(decoded.MessageType, decoded.MsgID); other != nil {
				select {
				case msg := <-other.channel:
					t.Fatalf("transplanted frame was delivered into the body's session: %q", msg.Payload)
				default:
				}
			}
			if !stream.resetCalled.Load() {
				t.Fatal("a frame whose body contradicts its preflight header must be reset")
			}
		})
	}
}

// The coordinator may discard an invalid inner vote after successful delivery.
// Draining those messages must not replenish any of the transport allowances.
func TestHandleStreamTssDiscardedKeygenVotesConsumeBudget(t *testing.T) {
	topic := messages.TSSFrostVerifyMsg
	msgID := "discarded-keygen-votes"
	remote := peer.ID("member-peer")
	limit := FrostKeygenVerificationMessagesPerPeer(3)
	comm := newHandlerCommunicationWithLimit(topic, msgID, []peer.ID{remote}, limit)
	t.Cleanup(func() { comm.CancelSubscribe(topic, msgID) })
	subscription := comm.getSubscriber(topic, msgID)
	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic, MsgID: msgID,
		Payload: []byte(`{"round":"frost_dkg_round3","transcript_hash":"invalid"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for range limit {
		stream := newHandlerStream(remote, frame)
		comm.handleStreamTss(stream)
		select {
		case <-subscription.channel:
			// Delivery is complete; the protocol rejects the invalid inner vote.
		default:
			t.Fatal("in-budget vote was not delivered")
		}
	}
	subscription.lock.Lock()
	count, peerBytes, sessionBytes := subscription.peerCounts[remote], subscription.peerBytes[remote], subscription.sessionBytes
	subscription.lock.Unlock()
	wantBytes := uint64(limit) * uint64(len(frame))
	if count != limit || peerBytes != wantBytes || sessionBytes != wantBytes {
		t.Fatalf("discarded votes charged %d/%d/%d, want %d/%d/%d", count, peerBytes, sessionBytes, limit, wantBytes, wantBytes)
	}
	overLimit := newHandlerStream(remote, frame)
	comm.handleStreamTss(overLimit)
	if !overLimit.resetCalled.Load() {
		t.Fatal("over-budget vote was not rejected")
	}
	select {
	case <-subscription.channel:
		t.Fatal("discarding votes allowed an over-budget delivery")
	default:
	}
}

// A peer-attributable failure after preflight has already caused allocation,
// reading, or parsing work. That work must remain charged to the peer; otherwise
// the peer can refund its allowance by failing each frame and repeat forever.
func TestHandleStreamTssPeerProtocolErrorsConsumeReservation(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	topic := messages.TSSFrostShareMsg
	msgID := "peer-protocol-failure-budget"
	remote := peer.ID("member-peer")
	validFrame := frostTestFrame(t, topic, msgID)

	truncatedPrefix := tssFramePrefix(t, topic, msgID)
	truncatedLength := uint32(len(truncatedPrefix) + 1024)
	malformedFrame := []byte(fmt.Sprintf(
		`{"message_type":%d,"message_id":%q,"payload":`,
		topic, msgID,
	))
	mismatchFrame := []byte(fmt.Sprintf(
		`{"message_type":%d,"message_id":%q,"payload":%q,"message_id":"other-session"}`,
		topic, msgID, strings.Repeat("A", 2*TSSFramePreflightBytes),
	))

	tests := []struct {
		name         string
		newStream    func() *handlerStream
		chargedBytes uint64
	}{
		{
			name: "truncated body",
			newStream: func() *handlerStream {
				return newHandlerStreamWithDeclaredLength(remote, truncatedLength, truncatedPrefix)
			},
			chargedBytes: uint64(truncatedLength),
		},
		{
			name: "malformed JSON",
			newStream: func() *handlerStream {
				return newHandlerStream(remote, malformedFrame)
			},
			chargedBytes: uint64(len(malformedFrame)),
		},
		{
			name: "preflight and body header mismatch",
			newStream: func() *handlerStream {
				return newHandlerStream(remote, mismatchFrame)
			},
			chargedBytes: uint64(len(mismatchFrame)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comm := newHandlerCommunicationWithLimit(topic, msgID, []peer.ID{remote}, 1)
			t.Cleanup(func() { comm.CancelSubscribe(topic, msgID) })
			subscription := comm.getSubscriber(topic, msgID)

			stream := tc.newStream()
			comm.handleStreamTss(stream)

			if !stream.resetCalled.Load() {
				t.Fatal("peer protocol failure must reset its stream")
			}
			subscription.lock.Lock()
			peerCount := subscription.peerCounts[remote]
			peerBytes := subscription.peerBytes[remote]
			sessionBytes := subscription.sessionBytes
			subscription.lock.Unlock()
			if peerCount != 1 || peerBytes != tc.chargedBytes || sessionBytes != tc.chargedBytes {
				t.Fatalf(
					"protocol failure charge count=%d peerBytes=%d sessionBytes=%d, want 1/%d/%d",
					peerCount, peerBytes, sessionBytes, tc.chargedBytes, tc.chargedBytes,
				)
			}

			retry := newHandlerStream(remote, validFrame)
			comm.handleStreamTss(retry)
			if !retry.resetCalled.Load() {
				t.Fatal("peer exceeded its cumulative message allowance but retry was not reset")
			}
			select {
			case message := <-subscription.channel:
				t.Fatalf("over-limit peer reached the session channel: %q", message.Payload)
			default:
			}
			if got := parkedStreamCount(comm.streamMgr); got != 0 {
				t.Fatalf("protocol failure left %d streams parked", got)
			}
		})
	}
}

// A frame rejected in preflight must be RESET, not parked in streamMgr. Parking
// it would leak the stream: streamMgr only releases streams keyed by a msgID a
// ceremony later completes, and a rejected frame has no such ceremony.
func TestHandleStreamTssResetsPreflightRejectedStreamWithoutParkingIt(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	member := peer.ID("member-peer")
	outsider := peer.ID("outsider-peer")

	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("payload"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	comm := newHandlerCommunication(topic, msgID, []peer.ID{member})

	// The outsider clears the global connection gater but is not a member of
	// this ceremony, so preflight must reject it.
	stream := newHandlerStream(outsider, frame)
	comm.handleStreamTss(stream)

	if !stream.resetCalled.Load() {
		t.Fatal("a preflight-rejected stream must be reset")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("a preflight-rejected stream must not be parked in streamMgr, got %d parked", got)
	}

	sub := comm.getSubscriber(topic, msgID)
	select {
	case msg := <-sub.channel:
		t.Fatalf("unauthorized peer's frame reached the session channel: %q", msg.Payload)
	default:
	}
}

// The global MaxPayload gate runs BEFORE preflight(), so a peer that clears only
// the connection gater can trip it without belonging to any ceremony. It must be
// classified as a preflight rejection like every other pre-session reject, or the
// stream parks in StreamUnknown with no msgID a ceremony will ever release.
func TestHandleStreamTssResetsOversizedLengthHeaderWithoutParkingIt(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	member := peer.ID("member-peer")
	outsider := peer.ID("outsider-peer")

	comm := newHandlerCommunication(topic, msgID, []peer.ID{member})

	// Header only, no body: the size gate must reject on the declared length
	// alone. If it did not, the truncated prefix read below it would fail with
	// an ordinary error and the stream would be parked instead of reset.
	base := NewMockNetworkStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, MaxPayload+1)
	if _, err := base.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	stream := &handlerStream{MockNetworkStream: base, conn: fakeTSSConn{remote: outsider}}

	comm.handleStreamTss(stream)

	if !stream.resetCalled.Load() {
		t.Fatal("a stream declaring an oversized payload length must be reset")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("an oversized-length stream must not be parked in streamMgr, got %d parked", got)
	}
}

// The authorized path must still work, or the tests above would pass for the
// wrong reason (a handler that drops everything).
func TestHandleStreamTssDeliversAuthorizedFrame(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "authorized-session"
	member := peer.ID("member-peer")

	sent := messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("a-real-ceremony-frame"),
	}
	frame, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	comm := newHandlerCommunication(topic, msgID, []peer.ID{member})
	stream := newHandlerStream(member, frame)
	comm.handleStreamTss(stream)

	sub := comm.getSubscriber(topic, msgID)
	var delivered *Message
	select {
	case msg := <-sub.channel:
		delivered = msg
		if msg.PeerID != member {
			t.Fatalf("delivered frame attributed to the wrong peer: %s", msg.PeerID)
		}
		var got messages.WrappedMessage
		if err = json.Unmarshal(msg.Payload, &got); err != nil {
			t.Fatalf("unmarshal delivered frame: %v", err)
		}
		if got.MsgID != msgID || got.MessageType != topic {
			t.Fatalf("delivered frame changed session: %+v", got)
		}
	default:
		t.Fatal("an authorized member's frame must be delivered to the session channel")
	}

	if stream.resetCalled.Load() {
		t.Fatal("an authorized frame's stream must not be reset")
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("a delivered FROST stream must be closed by its handler, not parked: %d", got)
	}
	if delivered == nil {
		t.Fatal("no frame delivered")
	}
}
