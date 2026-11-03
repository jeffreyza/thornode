package p2p

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// The coordinator starts draining while narrowing is still waking removed
// handlers. Go's select has no priority, so a removed handler still delivers
// if a receive completes its send before narrowing's close reaches it, or if
// it is between its check and its select. This test asserts what holds under
// every interleaving: no removed handler stays blocked, every stale frame is
// from the removed peer, and the active frame arrives.
func TestNarrowingWhileDrainingNeverStrandsActiveFrame(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	const queueSize = 2
	const removedFrames = queueSize + 6
	worstStale := 0
	for trial := 0; trial < 100; trial++ {
		topic := messages.TSSFrostShareMsg
		msgID := "narrow-while-draining"
		removed := peer.ID("removed-peer")
		active := peer.ID("active-peer")
		comm := &Communication{
			subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
			subscriberLocker: &sync.Mutex{},
			streamMgr:        NewStreamMgr(),
			stopChan:         make(chan struct{}),
		}
		queue := make(chan *Message, queueSize)
		comm.SetSubscribeWithPeers(topic, msgID, queue, []peer.ID{removed, active}, FrostMaxSessionPayload, 64)
		subscription := comm.getSubscriber(topic, msgID)
		frame := frostTestFrame(t, topic, msgID)

		var handled sync.WaitGroup
		for i := 0; i < removedFrames; i++ {
			stream := newLifecycleHandlerStream(removed, frame)
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
				t.Fatalf("trial %d: removed peer did not fill the queue", trial)
			}
			time.Sleep(100 * time.Microsecond)
		}

		narrowed := make(chan struct{})
		go func() {
			if !comm.NarrowSubscribePeers(topic, msgID, []peer.ID{active}) {
				t.Error("narrowing to the active peer was rejected")
			}
			close(narrowed)
			comm.handleStreamTss(newLifecycleHandlerStream(active, frame))
		}()
		stale := 0
	drain:
		for {
			select {
			case message := <-queue:
				if message.PeerID == active {
					break drain
				}
				if message.PeerID != removed {
					t.Fatalf("trial %d: unexpected sender %s", trial, message.PeerID)
				}
				stale++
			case <-time.After(2 * time.Second):
				t.Fatalf("trial %d: active frame never arrived after %d stale frames", trial, stale)
			}
		}
		<-narrowed
		allHandled := make(chan struct{})
		go func() {
			handled.Wait()
			close(allHandled)
		}()
		waitForTestSignal(t, allHandled, "removed-peer handlers to finish")
		if stale > removedFrames {
			t.Fatalf("trial %d: %d stale frames from %d sent", trial, stale, removedFrames)
		}
		if stale > worstStale {
			worstStale = stale
		}
		comm.CancelSubscribe(topic, msgID)
	}
	t.Logf("worst stale frames ahead of the active frame: %d (queue %d, removed frames %d)", worstStale, queueSize, removedFrames)
}
