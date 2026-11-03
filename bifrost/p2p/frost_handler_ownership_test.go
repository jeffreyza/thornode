package p2p

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type gatedCloseHandlerStream struct {
	*lifecycleHandlerStream
	closeStarted chan struct{}
	closeGate    <-chan struct{}
	closeOnce    sync.Once
}

func (s *gatedCloseHandlerStream) Close() error {
	s.closeOnce.Do(func() { close(s.closeStarted) })
	<-s.closeGate
	return s.lifecycleHandlerStream.Close()
}

func waitForPeerCharge(t *testing.T, subscription *MessageSubscription, id peer.ID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		subscription.lock.Lock()
		charged := subscription.peerCounts[id]
		subscription.lock.Unlock()
		if charged > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer %s was never charged at preflight", id)
		}
		time.Sleep(time.Millisecond)
	}
}

func returnsWithin(t *testing.T, d time.Duration, description string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s blocked on transport cleanup", description)
	}
}

// The handler delivers a FROST frame before closing its stream, so a slow
// yamux Close stalls only the handler goroutine, never the coordinator.
func TestFrostHandlerDeliversBeforeSlowClose(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	topic := messages.TSSFrostShareMsg
	msgID := "deliver-before-slow-close"
	remote := peer.ID("slow-close-peer")
	comm := newHandlerCommunication(topic, msgID, []peer.ID{remote})
	t.Cleanup(func() { comm.CancelSubscribe(topic, msgID) })
	subscription := comm.getSubscriber(topic, msgID)

	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	stream := &gatedCloseHandlerStream{
		lifecycleHandlerStream: newLifecycleHandlerStream(remote, frostTestFrame(t, topic, msgID)),
		closeStarted:           make(chan struct{}),
		closeGate:              gate,
	}
	handled := make(chan struct{})
	go func() {
		comm.handleStreamTss(stream)
		close(handled)
	}()

	select {
	case message := <-subscription.channel:
		if message.PeerID != remote {
			t.Fatalf("delivered from %s", message.PeerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("frame was not delivered while its stream Close was blocked")
	}
	waitForTestSignal(t, stream.closeStarted, "handler Close")
	select {
	case <-handled:
		t.Fatal("handler returned before its blocked Close finished")
	default:
	}
	release.Do(func() { close(gate) })
	waitForTestSignal(t, handled, "handler return")
	if stream.closeCount.Load() != 1 || stream.resetCount.Load() != 0 {
		t.Fatalf("terminal actions close=%d reset=%d", stream.closeCount.Load(), stream.resetCount.Load())
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("delivered FROST stream parked %d streams", got)
	}
}

// Narrowing and cancellation never touch streams. A handler blocked in
// delivery owns its stream and resets it itself after cancellation, while
// the registry stays available and the removed peer's charge is released.
func TestFrostNarrowingAndCancelDoNoStreamIO(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	topic := messages.TSSFrostShareMsg
	otherTopic := messages.TSSFrostPreprocessMsg
	msgID := "narrow-cancel-no-stream-io"
	removed := peer.ID("removed-peer")
	retained := peer.ID("retained-peer")
	comm := &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
	}
	// Unbuffered: nobody consumes, so the handler blocks in delivery.
	comm.SetSubscribeWithPeers(topic, msgID, make(chan *Message), []peer.ID{removed, retained}, FrostMaxSessionPayload, 8)
	comm.SetSubscribeWithPeers(otherTopic, msgID, make(chan *Message, 1), []peer.ID{retained}, FrostMaxSessionPayload, 8)
	t.Cleanup(func() { comm.CancelSubscribe(otherTopic, msgID) })
	subscription := comm.getSubscriber(topic, msgID)

	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	stream := newLifecycleHandlerStream(removed, frostTestFrame(t, topic, msgID))
	stream.resetStarted = make(chan struct{})
	stream.resetGate = gate
	handled := make(chan struct{})
	go func() {
		comm.handleStreamTss(stream)
		close(handled)
	}()
	waitForPeerCharge(t, subscription, removed)
	// Let the handler finish its tiny read and park in delivery.
	time.Sleep(20 * time.Millisecond)

	returnsWithin(t, time.Second, "narrowing", func() {
		if !comm.NarrowSubscribePeers(topic, msgID, []peer.ID{retained}) {
			t.Error("narrowing to a subset was rejected")
		}
	})
	if comm.NarrowSubscribePeers(topic, msgID, []peer.ID{removed, retained}) {
		t.Fatal("narrowing re-admitted a removed peer")
	}
	subscription.lock.Lock()
	spent := subscription.sessionBytes
	subscription.lock.Unlock()
	if spent != 0 {
		t.Fatalf("narrowing kept the removed peer's session charge: %d bytes", spent)
	}
	if subscription.accepts(removed, 1) {
		t.Fatal("a removed peer regained access after narrowing")
	}

	returnsWithin(t, time.Second, "cancellation", func() { comm.CancelSubscribe(topic, msgID) })
	waitForTestSignal(t, stream.resetStarted, "handler Reset after cancellation")
	returnsWithin(t, time.Second, "unrelated lookup during a blocked Reset", func() {
		if comm.getSubscriber(otherTopic, msgID) == nil {
			t.Error("unrelated subscription disappeared")
		}
	})
	returnsWithin(t, time.Second, "replacement during a blocked Reset", func() {
		comm.SetSubscribeWithPeers(topic, msgID, make(chan *Message, 1), []peer.ID{retained}, FrostMaxSessionPayload, 8)
	})
	t.Cleanup(func() { comm.CancelSubscribe(topic, msgID) })

	release.Do(func() { close(gate) })
	waitForTestSignal(t, handled, "handler return")
	if stream.resetCount.Load() != 1 || stream.closeCount.Load() != 0 {
		t.Fatalf("terminal actions reset=%d close=%d", stream.resetCount.Load(), stream.closeCount.Load())
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("undelivered FROST stream parked %d streams", got)
	}
}
