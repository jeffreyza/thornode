package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type terminalCountingStream struct {
	*MockNetworkStream
	resetStarted chan struct{}
	resetGate    <-chan struct{}
	resetOnce    sync.Once
	resetCount   atomic.Int32
	closeCount   atomic.Int32
}

func newTerminalCountingStream() *terminalCountingStream {
	return &terminalCountingStream{MockNetworkStream: NewMockNetworkStream()}
}

func (s *terminalCountingStream) Reset() error {
	s.resetCount.Add(1)
	if s.resetStarted != nil {
		s.resetOnce.Do(func() { close(s.resetStarted) })
	}
	if s.resetGate != nil {
		<-s.resetGate
	}
	return nil
}

func (s *terminalCountingStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type lifecycleHandlerStream struct {
	*terminalCountingStream
	conn network.Conn
}

func (s *lifecycleHandlerStream) Conn() network.Conn { return s.conn }

func newLifecycleHandlerStream(remote peer.ID, frame []byte) *lifecycleHandlerStream {
	stream := newTerminalCountingStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, uint32(len(frame)))
	_, _ = stream.Buffer.Write(header)
	_, _ = stream.Buffer.Write(frame)
	return &lifecycleHandlerStream{
		terminalCountingStream: stream,
		conn:                   fakeTSSConn{remote: remote},
	}
}

func frostTestFrame(t *testing.T, topic messages.THORChainTSSMessageType, msgID string) []byte {
	t.Helper()
	frame, err := json.Marshal(messages.WrappedMessage{
		MessageType: topic,
		MsgID:       msgID,
		Payload:     []byte("frost-lifecycle-payload"),
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return frame
}

func frostStreamCount(net network.Network) int {
	count := 0
	for _, conn := range net.Conns() {
		for _, stream := range conn.GetStreams() {
			if stream.Protocol() == TSSProtocolID {
				count++
			}
		}
	}
	return count
}

func TestSuccessfulFrostStreamsFullyCloseOnBothEndpoints(t *testing.T) {
	senderHost, err := libp2p.New(context.Background(), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create sender host: %v", err)
	}
	t.Cleanup(func() { _ = senderHost.Close() })
	receiverHost, err := libp2p.New(context.Background(), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create receiver host: %v", err)
	}
	t.Cleanup(func() { _ = receiverHost.Close() })

	const frames = 4
	topic := messages.TSSFrostShareMsg
	msgID := "fully-close-successful-frost-streams"
	channel := make(chan *Message, frames)
	receiver := &Communication{
		host:             receiverHost,
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
	}
	receiver.SetSubscribeWithPeers(topic, msgID, channel, []peer.ID{senderHost.ID()}, FrostMaxSessionPayload, frames)
	receiverHost.SetStreamHandler(TSSProtocolID, receiver.handleStreamTss)
	t.Cleanup(func() { receiver.CancelSubscribe(topic, msgID) })

	sender := &Communication{host: senderHost, streamMgr: NewStreamMgr(), stopChan: make(chan struct{})}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelConnect()
	if err = senderHost.Connect(connectCtx, peer.AddrInfo{ID: receiverHost.ID(), Addrs: receiverHost.Addrs()}); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}
	frame := frostTestFrame(t, topic, msgID)

	for i := 0; i < frames; i++ {
		sendResult := make(chan error, 1)
		go func() { sendResult <- sender.writeToStream(receiverHost.ID(), frame, msgID, true) }()

		var message *Message
		select {
		case message = <-channel:
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d was not delivered", i)
		}
		select {
		case err := <-sendResult:
			if err != nil {
				t.Fatalf("frame %d sender close failed: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d sender waited for receiver acceptance", i)
		}
		if message.PeerID != senderHost.ID() {
			t.Fatalf("frame %d delivered from %s", i, message.PeerID)
		}
		deadline := time.Now().Add(2 * time.Second)
		for frostStreamCount(senderHost.Network()) != 0 || frostStreamCount(receiverHost.Network()) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("frame %d leaked streams: sender=%d receiver=%d", i, frostStreamCount(senderHost.Network()), frostStreamCount(receiverHost.Network()))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

type keysignFrameReadBarrier struct {
	network.Stream
	frameBytes int
	readBytes  int
	readDone   chan struct{}
	readOnce   sync.Once
	resets     atomic.Int32
}

func (s *keysignFrameReadBarrier) Read(payload []byte) (int, error) {
	n, readErr := s.Stream.Read(payload)
	s.readBytes += n
	if s.readBytes >= s.frameBytes {
		s.readOnce.Do(func() { close(s.readDone) })
	}
	return n, readErr
}

func (s *keysignFrameReadBarrier) Reset() error {
	s.resets.Add(1)
	return s.Stream.Reset()
}

// The frame exceeds the preflight prefix, so the read-complete barrier is after
// reservation. With no queue consumer, cancellation must release the handler;
// a new subscription then accepts a frame over the same real libp2p connection.
func TestFrostKeysignCancellationAllowsNextRealSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	senderHost, senderErr := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if senderErr != nil {
		t.Fatal(senderErr)
	}
	t.Cleanup(func() { _ = senderHost.Close() })
	receiverHost, receiverErr := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if receiverErr != nil {
		t.Fatal(receiverErr)
	}
	t.Cleanup(func() { _ = receiverHost.Close() })
	receiver := &Communication{
		logger: zerolog.Nop(), host: receiverHost, stopChan: make(chan struct{}), streamMgr: NewStreamMgr(),
		subscribers: make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber), subscriberLocker: &sync.Mutex{},
	}
	sender := &Communication{logger: zerolog.Nop(), host: senderHost, stopChan: make(chan struct{}), streamMgr: NewStreamMgr()}
	const cancelledID = "cancelled-keysign-session"
	const nextID = "subsequent-keysign-session"
	topic := messages.TSSFrostShareMsg
	receiver.SetSubscribeWithPeers(topic, cancelledID, make(chan *Message), []peer.ID{senderHost.ID()}, FrostMaxSessionPayload, 1)
	oldSubscription := receiver.getSubscriber(topic, cancelledID)
	t.Cleanup(func() {
		receiver.CancelSubscribe(topic, cancelledID)
		receiver.CancelSubscribe(topic, nextID)
	})
	oldFrame, marshalErr := json.Marshal(messages.WrappedMessage{
		MessageType: topic, MsgID: cancelledID, Payload: bytes.Repeat([]byte("a"), 2*TSSFramePreflightBytes),
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if len(oldFrame) <= 2*TSSFramePreflightBytes {
		t.Fatal("frame fixture must exceed reader lookahead before its preflight callback")
	}
	started := make(chan *keysignFrameReadBarrier, 1)
	handled := make(chan struct{})
	receiverHost.SetStreamHandler(TSSProtocolID, func(stream network.Stream) {
		observed := &keysignFrameReadBarrier{Stream: stream, frameBytes: LengthHeader + len(oldFrame), readDone: make(chan struct{})}
		started <- observed
		receiver.handleStreamTss(observed)
		close(handled)
	})
	if connectErr := senderHost.Connect(ctx, peer.AddrInfo{ID: receiverHost.ID(), Addrs: receiverHost.Addrs()}); connectErr != nil {
		t.Fatal(connectErr)
	}
	oldStream, openErr := senderHost.NewStream(ctx, receiverHost.ID(), TSSProtocolID)
	if openErr != nil {
		t.Fatal(openErr)
	}
	connectionID := oldStream.Conn().ID()
	t.Cleanup(func() { _ = oldStream.Close() })
	if writeErr := WriteStreamWithBuffer(oldFrame, oldStream); writeErr != nil {
		t.Fatal(writeErr)
	}
	var observed *keysignFrameReadBarrier
	select {
	case observed = <-started:
	case <-ctx.Done():
		t.Fatal("real receiver handler did not start")
	}
	waitForTestSignal(t, observed.readDone, "charged keysign frame to finish reading")
	oldSubscription.lock.Lock()
	charged := oldSubscription.peerCounts[senderHost.ID()]
	oldSubscription.lock.Unlock()
	if charged != 1 {
		t.Fatalf("frame reached read barrier without preflight reservation: %d", charged)
	}
	receiver.CancelSubscribe(topic, cancelledID)
	waitForTestSignal(t, handled, "cancelled keysign handler to terminate")
	if deadlineErr := oldStream.SetReadDeadline(time.Now().Add(2 * time.Second)); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	var ack [1]byte
	n, oldReadErr := oldStream.Read(ack[:])
	if n != 0 || oldReadErr == nil {
		t.Fatalf("cancelled stream did not terminate: n=%d err=%v", n, oldReadErr)
	}
	var timeoutErr net.Error
	if errors.As(oldReadErr, &timeoutErr) && timeoutErr.Timeout() {
		t.Fatal("cancelled stream remained open until its read deadline")
	}
	if observed.resets.Load() != 1 || parkedStreamCount(receiver.streamMgr) != 0 {
		t.Fatal("cancellation did not give the frame one terminal reset owner")
	}
	receiverHost.SetStreamHandler(TSSProtocolID, receiver.handleStreamTss)
	nextMessages := make(chan *Message, 1)
	receiver.SetSubscribeWithPeers(topic, nextID, nextMessages, []peer.ID{senderHost.ID()}, FrostMaxSessionPayload, 1)
	nextFrame := frostTestFrame(t, topic, nextID)
	if writeErr := sender.writeToStream(receiverHost.ID(), nextFrame, nextID, true); writeErr != nil {
		t.Fatalf("subsequent real session could not write: %v", writeErr)
	}
	connections := senderHost.Network().ConnsToPeer(receiverHost.ID())
	if len(connections) != 1 || connections[0].ID() != connectionID {
		t.Fatal("subsequent session needed a replacement connection after stream cancellation")
	}
	select {
	case message := <-nextMessages:
		if message.PeerID != senderHost.ID() || !bytes.Equal(message.Payload, nextFrame) {
			t.Fatal("subsequent real session received the wrong peer or frame")
		}
	case <-ctx.Done():
		t.Fatal("subsequent real session did not progress after cancellation")
	}
}
