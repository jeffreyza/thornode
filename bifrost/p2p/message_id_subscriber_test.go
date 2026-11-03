package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	. "gopkg.in/check.v1"
)

type MessageIDSubscriberTestSuit struct{}

var _ = Suite(&MessageIDSubscriberTestSuit{})

func (MessageIDSubscriberTestSuit) TestMessageIDSubscriber(c *C) {
	ms := NewMessageIDSubscriber()
	ms.Subscribe("hello", make(chan *Message))
	channel := ms.GetSubscriber("helloworld")
	c.Assert(channel, IsNil)
	channel1 := ms.GetSubscriber("hello")
	c.Assert(channel1, NotNil)
	c.Assert(ms.IsEmpty(), Equals, false)
	ms.UnSubscribe("hello")
	channel2 := ms.GetSubscriber("hello")
	c.Assert(channel2, IsNil)
	c.Assert(ms.IsEmpty(), Equals, true)
}

func (MessageIDSubscriberTestSuit) TestRestrictedSubscription(c *C) {
	allowed := peer.ID("allowed-peer")
	outsider := peer.ID("outsider-peer")
	ms := NewMessageIDSubscriber()
	ms.SubscribeRestricted("frost", make(chan *Message, 1), []peer.ID{allowed, outsider}, 4, 2)
	sub := ms.GetSubscriber("frost")
	c.Assert(sub, NotNil)
	c.Assert(sub.maxBytesPerPeer, Equals, FrostMaxBytesPerPeer)
	c.Assert(sub.maxSessionBytes, Equals, FrostMaxSessionBytes)
	c.Assert(sub.accepts(allowed, 5), Equals, false)
	c.Assert(sub.accepts(allowed, 4), Equals, true)
	c.Assert(sub.accepts(allowed, 4), Equals, true)
	c.Assert(sub.accepts(allowed, 4), Equals, false)

	c.Assert(ms.NarrowAllowedPeers("frost", []peer.ID{outsider}), Equals, true)
	c.Assert(sub.accepts(allowed, 1), Equals, false)
	c.Assert(sub.accepts(outsider, 1), Equals, true)
}

func TestRestrictedSubscriptionEnforcesByteBudgetsFairly(t *testing.T) {
	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")
	outsider := peer.ID("outsider")
	ms := NewMessageIDSubscriber()
	ms.SubscribeRestrictedWithBudgets(
		"frost",
		make(chan *Message, 1),
		[]peer.ID{peerA, peerB},
		8,
		10,
		8,
		12,
	)
	sub := ms.GetSubscriber("frost")
	if sub == nil {
		t.Fatal("expected restricted subscription")
	}

	// Authorization failures must not consume any cumulative allowance.
	if sub.accepts(outsider, 4) {
		t.Fatal("outsider must not reserve session bytes")
	}
	if sub.accepts(peerA, 9) {
		t.Fatal("oversized frame must not reserve session bytes")
	}
	if sub.sessionBytes != 0 || len(sub.peerBytes) != 0 {
		t.Fatalf("rejected frames consumed bytes: session=%d peers=%v", sub.sessionBytes, sub.peerBytes)
	}

	if !sub.accepts(peerA, 8) {
		t.Fatal("peer A should be able to spend its byte allowance")
	}
	if sub.accepts(peerA, 1) {
		t.Fatal("peer A must not spend peer B's fair-share allowance")
	}
	if !sub.accepts(peerB, 4) {
		t.Fatal("peer B should retain room after peer A is exhausted")
	}
	if sub.accepts(peerB, 1) {
		t.Fatal("cumulative session budget must reject further frames")
	}
	if sub.sessionBytes != 12 || sub.peerBytes[peerA] != 8 || sub.peerBytes[peerB] != 4 {
		t.Fatalf("unexpected byte accounting: session=%d peers=%v", sub.sessionBytes, sub.peerBytes)
	}
	if sub.peerCounts[peerA] != 1 || sub.peerCounts[peerB] != 1 {
		t.Fatalf("rejections must not consume message counts: %v", sub.peerCounts)
	}
}

func TestRestrictedSubscriptionNarrowingRejectsExpansionAndKeepsAllowance(t *testing.T) {
	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")
	ms := NewMessageIDSubscriber()
	ms.SubscribeRestrictedWithBudgets(
		"frost",
		make(chan *Message, 1),
		[]peer.ID{peerA, peerB},
		8,
		10,
		8,
		32,
	)
	sub := ms.GetSubscriber("frost")
	if !sub.accepts(peerA, 6) {
		t.Fatal("initial peer A reservation failed")
	}
	if !sub.accepts(peerB, 6) {
		t.Fatal("initial peer B reservation failed")
	}
	if !ms.NarrowAllowedPeers("frost", []peer.ID{peerB}) {
		t.Fatal("failed to narrow allowed peers")
	}
	if ms.NarrowAllowedPeers("frost", []peer.ID{peerA, peerB}) {
		t.Fatal("narrowing must reject peer re-expansion")
	}
	if sub.accepts(peerA, 1) {
		t.Fatal("rejected expansion re-authorized peer A")
	}
	if !ms.NarrowAllowedPeers("frost", []peer.ID{peerB}) {
		t.Fatal("narrowing to the current set should be idempotent")
	}
	if !sub.accepts(peerB, 2) || sub.accepts(peerB, 1) {
		t.Fatal("narrowing reset retained peer B's byte allowance")
	}
}

func TestRestrictedSubscriptionWildcardCanNarrowToEmpty(t *testing.T) {
	remote := peer.ID("remote-peer")
	ms := NewMessageIDSubscriber()
	ms.SubscribeRestricted("frost", make(chan *Message, 1), nil, 8, 2)
	sub := ms.GetSubscriber("frost")
	if !sub.accepts(remote, 1) {
		t.Fatal("nil allow-list should initially accept an authenticated peer")
	}
	if !ms.NarrowAllowedPeers("frost", []peer.ID{}) {
		t.Fatal("failed to narrow wildcard allow-list to empty")
	}
	if sub.accepts(remote, 1) {
		t.Fatal("empty allow-list must reject every peer")
	}
}

func TestSubscriptionDeliveryBackpressure(t *testing.T) {
	legacyChannel := make(chan *Message)
	legacy := NewMessageIDSubscriber()
	legacy.Subscribe("legacy", legacyChannel)
	legacySub := legacy.GetSubscriber("legacy")
	delivered := make(chan bool, 1)
	go func() {
		delivered <- legacySub.deliver(&Message{PeerID: peer.ID("legacy-peer")}, nil)
	}()

	select {
	case <-delivered:
		t.Fatal("legacy unrestricted delivery returned before a receiver was ready")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-legacyChannel:
	case <-time.After(time.Second):
		t.Fatal("legacy delivery did not reach the receiver")
	}
	select {
	case ok := <-delivered:
		if !ok {
			t.Fatal("legacy delivery unexpectedly failed")
		}
	case <-time.After(time.Second):
		t.Fatal("legacy delivery remained blocked after receive")
	}

	restrictedChannel := make(chan *Message, 1)
	restrictedChannel <- &Message{}
	restricted := NewMessageIDSubscriber()
	restricted.SubscribeRestricted("frost", restrictedChannel, []peer.ID{peer.ID("frost-peer")}, 1024, 1)
	restrictedDelivered := make(chan bool, 1)
	stop := make(chan struct{})
	go func() {
		restrictedDelivered <- restricted.GetSubscriber("frost").deliver(&Message{PeerID: peer.ID("frost-peer")}, stop)
	}()
	select {
	case <-restrictedDelivered:
		t.Fatal("restricted delivery returned while its queue was full")
	case <-time.After(25 * time.Millisecond):
	}
	<-restrictedChannel
	select {
	case ok := <-restrictedDelivered:
		if !ok {
			t.Fatal("restricted delivery unexpectedly failed after capacity became available")
		}
	case <-time.After(time.Second):
		t.Fatal("restricted delivery did not resume after capacity became available")
	}

	// Drain the message delivered above before filling the queue again to test
	// cancellation. Otherwise this test itself blocks on the prefill send.
	<-restrictedChannel
	restrictedChannel <- &Message{}
	cancelledDelivery := make(chan bool, 1)
	sub := restricted.GetSubscriber("frost")
	go func() {
		cancelledDelivery <- sub.deliver(&Message{PeerID: peer.ID("frost-peer")}, stop)
	}()
	select {
	case <-cancelledDelivery:
		t.Fatal("restricted delivery returned before cancellation")
	case <-time.After(25 * time.Millisecond):
	}
	restricted.UnSubscribe("frost")
	select {
	case ok := <-cancelledDelivery:
		if ok {
			t.Fatal("cancelled subscription reported successful delivery")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription cancellation did not unblock delivery")
	}
}
