package p2p

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type preflightBoundedStream struct {
	*MockNetworkStream
	maxRead  int
	read     int
	overread bool
}

func (s *preflightBoundedStream) Read(buf []byte) (int, error) {
	remaining := s.maxRead - s.read
	if remaining <= 0 {
		s.overread = true
		return 0, fmt.Errorf("read exceeded preflight bound")
	}
	if len(buf) > remaining {
		buf = buf[:remaining]
	}
	n, err := s.MockNetworkStream.Read(buf)
	s.read += n
	return n, err
}

func newTSSPreflightCommunication(
	topic messages.THORChainTSSMessageType,
	msgID string,
	allowedPeers []peer.ID,
	maxPayload int,
	maxMessages uint32,
) *Communication {
	subscriber := NewMessageIDSubscriber()
	subscriber.SubscribeRestricted(msgID, make(chan *Message, 4), allowedPeers, maxPayload, maxMessages)
	return &Communication{
		logger:           zerolog.Nop(),
		subscribers:      map[messages.THORChainTSSMessageType]*MessageIDSubscriber{topic: subscriber},
		subscriberLocker: &sync.Mutex{},
	}
}

func tssFramePrefix(t *testing.T, topic messages.THORChainTSSMessageType, msgID string) []byte {
	t.Helper()
	prefix := []byte(fmt.Sprintf(`{"message_type":%d,"message_id":%q,"payload":"`, topic, msgID))
	if len(prefix) >= TSSFramePreflightBytes {
		t.Fatalf("test TSS header exceeds preflight bound")
	}
	return append(prefix, bytes.Repeat([]byte("a"), TSSFramePreflightBytes-len(prefix))...)
}

func framedPreflightStream(t *testing.T, declaredLength uint32, prefix []byte) *preflightBoundedStream {
	t.Helper()
	base := NewMockNetworkStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, declaredLength)
	_, _ = base.Write(header)
	_, _ = base.Write(prefix)
	return &preflightBoundedStream{
		MockNetworkStream: base,
		maxRead:           LengthHeader + len(prefix),
	}
}

// TestTSSFramePreflightAcceptsRealWrappedMessage round-trips an actual
// json.Marshal(messages.WrappedMessage) through the preflight reader. The
// header parser only sees message_type and message_id before payload because
// WrappedMessage declares its fields in that order and encoding/json marshals
// in declaration order; reordering those fields, or adding one ahead of
// Payload, fails preflight closed and resets every TSS and FROST stream. This
// test is the guard on that coupling — the hand-built tssFramePrefix frames
// used elsewhere cannot catch it.
func TestTSSFramePreflightAcceptsRealWrappedMessage(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "frost-session"
	remote := peer.ID("allowed-peer")

	tests := []struct {
		name        string
		payloadSize int
	}{
		{name: "small frame", payloadSize: 32},
		{name: "frame exceeding preflight bound", payloadSize: 4 * TSSFramePreflightBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sent := messages.WrappedMessage{
				MessageType: topic,
				MsgID:       msgID,
				Payload:     bytes.Repeat([]byte("p"), tc.payloadSize),
			}
			frame, err := json.Marshal(sent)
			if err != nil {
				t.Fatalf("marshal wrapped message: %v", err)
			}

			base := NewMockNetworkStream()
			header := make([]byte, LengthHeader)
			binary.LittleEndian.PutUint32(header, uint32(len(frame)))
			_, _ = base.Write(header)
			_, _ = base.Write(frame)

			comm := newTSSPreflightCommunication(topic, msgID, []peer.ID{remote}, FrostMaxSessionPayload, 2)
			got, err := readStreamWithBufferPreflight(base, TSSFramePreflightBytes, func(length uint32, prefix []byte) error {
				_, _, preflightErr := comm.preflightTSSFrame(remote, length, prefix)
				return preflightErr
			})
			if err != nil {
				t.Fatalf("real WrappedMessage must survive preflight: %v", err)
			}

			var received messages.WrappedMessage
			if err = json.Unmarshal(got, &received); err != nil {
				t.Fatalf("unmarshal round-tripped frame: %v", err)
			}
			if received.MessageType != sent.MessageType || received.MsgID != sent.MsgID {
				t.Fatalf("frame header did not survive round trip: %+v", received)
			}
			if !bytes.Equal(received.Payload, sent.Payload) {
				t.Fatalf("payload did not survive round trip: got %d bytes, want %d", len(received.Payload), len(sent.Payload))
			}
		})
	}
}

func TestTSSFramePreflightRejectsBeforeFullAllocation(t *testing.T) {
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(true) })

	topic := messages.TSSFrostShareMsg
	msgID := "frost-session"
	allowed := peer.ID("allowed-peer")
	outsider := peer.ID("outsider-peer")
	prefix := tssFramePrefix(t, topic, msgID)

	tests := []struct {
		name           string
		remote         peer.ID
		declaredLength uint32
	}{
		{name: "oversized frost", remote: allowed, declaredLength: FrostMaxSessionPayload + 1},
		{name: "outsider frost", remote: outsider, declaredLength: FrostMaxSessionPayload},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comm := newTSSPreflightCommunication(topic, msgID, []peer.ID{allowed}, FrostMaxSessionPayload, 2)
			stream := framedPreflightStream(t, tc.declaredLength, prefix)
			_, err := readStreamWithBufferPreflight(stream, TSSFramePreflightBytes, func(length uint32, gotPrefix []byte) error {
				_, _, preflightErr := comm.preflightTSSFrame(tc.remote, length, gotPrefix)
				return preflightErr
			})
			if !errors.Is(err, ErrTSSFramePreflightRejected) {
				t.Fatalf("expected bounded preflight rejection, got %v", err)
			}
			if stream.overread {
				t.Fatal("rejected frame attempted to read beyond bounded prefix")
			}
			if stream.read != LengthHeader+TSSFramePreflightBytes {
				t.Fatalf("read %d bytes before rejection, expected %d", stream.read, LengthHeader+TSSFramePreflightBytes)
			}
		})
	}
}

func TestTSSFramePreflightPreservesMatchingLegacyLimitOnly(t *testing.T) {
	topic := messages.TSSKeySignMsg
	msgID := "legacy-session"
	remote := peer.ID("legacy-peer")
	prefix := tssFramePrefix(t, topic, msgID)
	legacyLength := uint32(FrostMaxSessionPayload + 1)

	legacy := newTSSPreflightCommunication(topic, msgID, nil, 0, 0)
	header, subscription, err := legacy.preflightTSSFrame(remote, legacyLength, prefix)
	if err != nil {
		t.Fatalf("matching legacy subscription should retain 20 MiB allowance: %v", err)
	}
	if header.MessageType != topic || header.MsgID != msgID || subscription == nil {
		t.Fatalf("unexpected legacy preflight result: %+v", header)
	}

	missing := &Communication{
		logger:           zerolog.Nop(),
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
	}
	if _, _, err := missing.preflightTSSFrame(remote, legacyLength, prefix); err == nil {
		t.Fatal("20 MiB legacy allowance must require an existing matching subscription")
	}
}

func TestTSSFramePreflightCountsEachFrameOnce(t *testing.T) {
	topic := messages.TSSFrostPreprocessMsg
	msgID := "counted-session"
	remote := peer.ID("allowed-peer")
	comm := newTSSPreflightCommunication(topic, msgID, []peer.ID{remote}, FrostMaxSessionPayload, 2)
	prefix := tssFramePrefix(t, topic, msgID)

	for attempt := 0; attempt < 2; attempt++ {
		_, subscription, err := comm.preflightTSSFrame(remote, 1024, prefix)
		if err != nil {
			t.Fatalf("frame %d should reserve one allowance: %v", attempt+1, err)
		}
		if !subscription.authorized(remote, 1024) {
			t.Fatalf("frame %d should remain authorized after its bounded read", attempt+1)
		}
		if subscription.peerCounts[remote] != uint32(attempt+1) {
			t.Fatalf("post-read authorization double-counted frame %d", attempt+1)
		}
	}
	if _, _, err := comm.preflightTSSFrame(remote, 1024, prefix); err == nil {
		t.Fatal("third frame must be rejected after exactly two preflight reservations")
	}
	subscription := comm.getSubscriber(topic, msgID)
	if subscription == nil || subscription.peerCounts[remote] != 2 {
		t.Fatalf("expected exactly two counted frames, got %+v", subscription)
	}
}

func TestTSSFramePreflightEnforcesCumulativeByteBudgets(t *testing.T) {
	topic := messages.TSSFrostPreprocessMsg
	msgID := "byte-budget-session"
	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")
	outsider := peer.ID("outsider")
	prefix := tssFramePrefix(t, topic, msgID)
	subscriber := NewMessageIDSubscriber()
	subscriber.SubscribeRestrictedWithBudgets(
		msgID,
		make(chan *Message, 4),
		[]peer.ID{peerA, peerB},
		8192,
		10,
		8192,
		12288,
	)
	comm := &Communication{
		logger:           zerolog.Nop(),
		subscribers:      map[messages.THORChainTSSMessageType]*MessageIDSubscriber{topic: subscriber},
		subscriberLocker: &sync.Mutex{},
	}

	if _, _, err := comm.preflightTSSFrame(outsider, 4096, prefix); err == nil {
		t.Fatal("outsider must fail preflight authorization")
	}
	if _, _, err := comm.preflightTSSFrame(peerA, 8193, prefix); err == nil {
		t.Fatal("oversized frame must fail preflight authorization")
	}
	if _, _, err := comm.preflightTSSFrame(peerA, 8192, prefix); err != nil {
		t.Fatalf("peer A should be able to reserve its allowance: %v", err)
	}
	if _, _, err := comm.preflightTSSFrame(peerA, 1, prefix); err == nil {
		t.Fatal("peer A must not consume peer B's allowance")
	}
	if _, _, err := comm.preflightTSSFrame(peerB, 4096, prefix); err != nil {
		t.Fatalf("peer B should retain a fair share of the session: %v", err)
	}
	if _, _, err := comm.preflightTSSFrame(peerB, 1, prefix); err == nil {
		t.Fatal("session cumulative byte budget must fail closed")
	}

	sub := comm.getSubscriber(topic, msgID)
	if sub.sessionBytes != 12288 || sub.peerBytes[peerA] != 8192 || sub.peerBytes[peerB] != 4096 {
		t.Fatalf("unexpected preflight byte accounting: session=%d peers=%v", sub.sessionBytes, sub.peerBytes)
	}
}

func TestTSSFramePreflightRejectsControlByteInMessageID(t *testing.T) {
	prefix := tssFramePrefix(t, messages.TSSFrostShareMsg, "session\nforged-log-line")
	if _, err := decodeTSSFrameHeader(prefix); err == nil {
		t.Fatal("message_id containing a control byte must be rejected")
	}
}
