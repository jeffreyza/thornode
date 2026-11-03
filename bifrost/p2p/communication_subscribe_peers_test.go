package p2p

import (
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func newTestCommunication() *Communication {
	return &Communication{
		subscribers:      make(map[messages.THORChainTSSMessageType]*MessageIDSubscriber),
		subscriberLocker: &sync.Mutex{},
	}
}

// A restricted subscription is the authorization boundary for a FROST
// ceremony: a peer that clears the global connection gater but is not a
// ceremony member must not reach the session channel, and the legacy
// unbounded frame allowance must not apply.
func TestSetSubscribeWithPeersRestrictsIngress(t *testing.T) {
	member := peer.ID("member-peer")
	outsider := peer.ID("outsider-peer")

	c := newTestCommunication()
	c.SetSubscribeWithPeers(
		messages.TSSFrostDKGRound1Msg, "msg-id", make(chan *Message, 1),
		[]peer.ID{member}, 8, 2,
	)

	sub := c.getSubscriber(messages.TSSFrostDKGRound1Msg, "msg-id")
	if sub == nil {
		t.Fatal("expected a subscription to be registered")
	}
	if sub.accepts(outsider, 1) {
		t.Fatal("outsider peer must not be accepted by a restricted subscription")
	}
	if sub.accepts(member, 9) {
		t.Fatal("frame above maxPayloadBytes must be rejected")
	}
	if !sub.accepts(member, 8) {
		t.Fatal("member peer within budget must be accepted")
	}
	if sub.maxBytesPerPeer != FrostMaxBytesPerPeer || sub.maxSessionBytes != FrostMaxSessionBytes {
		t.Fatalf("session byte budgets not applied: perPeer=%d session=%d",
			sub.maxBytesPerPeer, sub.maxSessionBytes)
	}
}

// The caller owns the slice it passes in; later party filtering must not be
// able to widen an authorization decision that is already registered.
func TestSetSubscribeWithPeersCopiesAllowList(t *testing.T) {
	member := peer.ID("member-peer")
	outsider := peer.ID("outsider-peer")

	allowed := []peer.ID{member, "placeholder"}
	c := newTestCommunication()
	c.SetSubscribeWithPeers(
		messages.TSSFrostDKGRound1Msg, "msg-id", make(chan *Message, 1),
		allowed, 8, 2,
	)

	allowed[1] = outsider // mutate the caller's slice after registration

	sub := c.getSubscriber(messages.TSSFrostDKGRound1Msg, "msg-id")
	if sub.accepts(outsider, 1) {
		t.Fatal("mutating the caller slice must not widen the allow-list")
	}
}

// NarrowSubscribePeers narrows an existing session. It must never bring a
// subscription into existence, or an unknown msgID would silently become an
// authorized session.
func TestNarrowSubscribePeersRejectsUnknownSession(t *testing.T) {
	c := newTestCommunication()

	if c.NarrowSubscribePeers(messages.TSSFrostDKGRound1Msg, "never-registered", nil) {
		t.Fatal("expected false for an unknown message type")
	}

	c.SetSubscribeWithPeers(
		messages.TSSFrostDKGRound1Msg, "msg-id", make(chan *Message, 1),
		[]peer.ID{"member-peer"}, 8, 2,
	)
	if c.NarrowSubscribePeers(messages.TSSFrostDKGRound1Msg, "other-msg-id", nil) {
		t.Fatal("expected false for an unknown msgID on a known message type")
	}
	if c.getSubscriber(messages.TSSFrostDKGRound1Msg, "other-msg-id") != nil {
		t.Fatal("NarrowSubscribePeers must not create a subscription")
	}
}

// Guards the keygen hardening: a bare SetSubscribe accepts any authenticated
// peer with no byte ceiling, which is why FROST ceremonies must not use it.
func TestSetSubscribeIsUnrestricted(t *testing.T) {
	c := newTestCommunication()
	c.SetSubscribe(messages.TSSFrostDKGRound1Msg, "msg-id", make(chan *Message, 1))

	sub := c.getSubscriber(messages.TSSFrostDKGRound1Msg, "msg-id")
	if !sub.accepts(peer.ID("any-unlisted-peer"), 20*1024*1024) {
		t.Fatal("SetSubscribe is expected to be unrestricted; if this now fails, " +
			"the unrestricted path was hardened and FROST callers should be revisited")
	}
}
