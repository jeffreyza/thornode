package p2p

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// Peers that leave the party can fill the ceremony queue before narrowing.
// Narrowing must release their budget and wake their blocked handlers. Nothing
// drains the queue during narrowing here, so the close reaches every waiting
// handler before a receive could complete its send, and the retained signer
// waits behind only the queued frames. Go's select has no priority, so that
// bound does not hold under concurrent draining;
// TestNarrowingWhileDrainingNeverStrandsActiveFrame covers that case.
func TestNarrowingWakesRemovedPeersBlockedOnFullQueue(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	const queueSize = 2
	const removedFrames = queueSize + 3
	topic := messages.TSSFrostShareMsg
	msgID := "full-queue-narrowing"
	removed := peer.ID("removed-peer")
	active := peer.ID("active-peer")
	comm := &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
		streamMgr:        NewStreamMgr(),
		stopChan:         make(chan struct{}),
	}
	queue := make(chan *Message, queueSize)
	comm.SetSubscribeWithPeers(topic, msgID, queue, []peer.ID{removed, active}, FrostMaxSessionPayload, 16)
	t.Cleanup(func() { comm.CancelSubscribe(topic, msgID) })
	subscription := comm.getSubscriber(topic, msgID)
	frame := frostTestFrame(t, topic, msgID)

	streams := make([]*lifecycleHandlerStream, 0, removedFrames)
	var handled sync.WaitGroup
	for i := 0; i < removedFrames; i++ {
		stream := newLifecycleHandlerStream(removed, frame)
		streams = append(streams, stream)
		handled.Add(1)
		go func() {
			defer handled.Done()
			comm.handleStreamTss(stream)
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		subscription.lock.Lock()
		charged := subscription.peerCounts[removed]
		subscription.lock.Unlock()
		if charged == removedFrames && len(queue) == queueSize {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removed peer did not fill the queue: charged=%d queued=%d", charged, len(queue))
		}
		time.Sleep(time.Millisecond)
	}
	// Let the surplus handlers pass their post-read checks and block.
	time.Sleep(20 * time.Millisecond)

	if !comm.NarrowSubscribePeers(topic, msgID, []peer.ID{active}) {
		t.Fatal("narrowing to the active peer was rejected")
	}
	allHandled := make(chan struct{})
	go func() {
		handled.Wait()
		close(allHandled)
	}()
	waitForTestSignal(t, allHandled, "blocked removed-peer handlers to give up after narrowing")

	resets, closes := 0, 0
	for _, stream := range streams {
		resets += int(stream.resetCount.Load())
		closes += int(stream.closeCount.Load())
	}
	if resets != removedFrames-queueSize || closes != queueSize {
		t.Fatalf("removed-peer streams reset=%d closed=%d, want %d/%d", resets, closes, removedFrames-queueSize, queueSize)
	}
	subscription.lock.Lock()
	spent := subscription.sessionBytes
	subscription.lock.Unlock()
	if spent != 0 {
		t.Fatalf("narrowing kept %d bytes of removed-peer charges", spent)
	}

	activeStream := newLifecycleHandlerStream(active, frame)
	go comm.handleStreamTss(activeStream)
	for i := 0; i <= queueSize; i++ {
		select {
		case message := <-queue:
			if message.PeerID == active {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("queue stalled after %d removed-peer frames", i)
		}
	}
	t.Fatalf("active frame waited behind more than %d removed-peer frames", queueSize)
}
