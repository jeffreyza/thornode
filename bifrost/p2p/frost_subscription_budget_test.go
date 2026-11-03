package p2p

import (
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"
)

// Peers removed by narrowing can never send to the session again, so their
// share of the shared session budget returns to the retained party, once.
func TestNarrowingReleasesRemovedPeersSessionBudgetOnce(t *testing.T) {
	removed := peer.ID("removed-peer")
	retained := peer.ID("retained-peer")
	// The per-peer cap exceeds the session cap, so only the shared session
	// budget can reject the retained peer below.
	s := newMessageSubscription(make(chan *Message, 1), []peer.ID{removed, retained}, 8, 4, 16, 8)

	if !s.accepts(removed, 8) {
		t.Fatal("removed peer could not spend its allowance before narrowing")
	}
	if s.accepts(retained, 1) {
		t.Fatal("the session budget was not exhausted by the removed peer")
	}
	if !s.narrowAllowedPeers([]peer.ID{retained}) {
		t.Fatal("narrowing to a subset was rejected")
	}
	if !s.accepts(retained, 8) {
		t.Fatal("retained peer is still starved by a removed peer's charges")
	}
	if !s.narrowAllowedPeers([]peer.ID{retained}) {
		t.Fatal("narrowing to the current set must be idempotent")
	}
	if s.accepts(retained, 1) {
		t.Fatal("repeated narrowing refunded the session budget twice")
	}
	if s.accepts(removed, 1) {
		t.Fatal("a removed peer regained access after its charges were released")
	}
}

// A stopped subscription must neither charge a late frame nor report a
// successful narrowing, even when the caller looked it up before the stop.
func TestStoppedSubscriptionRejectsChargesAndNarrowing(t *testing.T) {
	remote := peer.ID("remote-peer")
	s := newMessageSubscription(make(chan *Message, 1), []peer.ID{remote}, 8, 4, 16, 16)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stop()
		}()
	}
	wg.Wait()

	select {
	case <-s.done:
	default:
		t.Fatal("stop did not close done")
	}
	if s.accepts(remote, 1) {
		t.Fatal("a stopped subscription charged a late frame")
	}
	s.lock.Lock()
	charged, spent := s.peerCounts[remote], s.sessionBytes
	s.lock.Unlock()
	if charged != 0 || spent != 0 {
		t.Fatalf("stopped subscription recorded charges: count=%d bytes=%d", charged, spent)
	}
	if s.authorized(remote, 1) {
		t.Fatal("a stopped subscription re-authorized a frame after its read")
	}
	if s.narrowAllowedPeers([]peer.ID{remote}) {
		t.Fatal("narrowing reported success on a stopped subscription")
	}
}
