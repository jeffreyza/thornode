package p2p

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p-core/peer"
)

// MessageSubscription is the security boundary between an authenticated
// libp2p stream and a protocol session.  A node may be allowed by the global
// connection gater without being a member of this particular ceremony, so a
// subscription can additionally restrict delivery to an exact peer set.
//
// The per-peer counter is intentionally kept on the subscription rather than
// globally: it is bounded by the allow-list and is discarded with the session.
type MessageSubscription struct {
	channel            chan *Message
	allowedPeers       map[peer.ID]struct{}
	maxPayloadBytes    int
	maxMessagesPerPeer uint32
	maxBytesPerPeer    uint64
	maxSessionBytes    uint64
	ctx                context.Context
	cancel             context.CancelFunc
	done               <-chan struct{}

	lock    sync.Mutex
	stopped bool
	// narrowed is closed and replaced on every narrowing so deliveries
	// blocked on a full queue recheck whether their peer is still allowed.
	narrowed     chan struct{}
	peerCounts   map[peer.ID]uint32
	peerBytes    map[peer.ID]uint64
	sessionBytes uint64
}

func newMessageSubscription(
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
	maxBytesPerPeer uint64,
	maxSessionBytes uint64,
) *MessageSubscription {
	var allowed map[peer.ID]struct{}
	if allowedPeers != nil {
		allowed = make(map[peer.ID]struct{}, len(allowedPeers))
		for _, id := range allowedPeers {
			allowed[id] = struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MessageSubscription{
		channel:            channel,
		allowedPeers:       allowed,
		maxPayloadBytes:    maxPayloadBytes,
		maxMessagesPerPeer: maxMessagesPerPeer,
		maxBytesPerPeer:    maxBytesPerPeer,
		maxSessionBytes:    maxSessionBytes,
		ctx:                ctx,
		cancel:             cancel,
		done:               ctx.Done(),
		narrowed:           make(chan struct{}),
		peerCounts:         make(map[peer.ID]uint32, len(allowed)),
		peerBytes:          make(map[peer.ID]uint64, len(allowed)),
	}
}

// accepts authorizes the frame and atomically reserves exactly one per-peer
// message allowance plus its cumulative byte allowances. The stream handler
// must call it only during preflight; delivery does not reserve a second time.
func (s *MessageSubscription) accepts(id peer.ID, payloadBytes int) bool {
	if s == nil || s.channel == nil || payloadBytes < 0 {
		return false
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.stopped || !s.authorizedLocked(id, payloadBytes) {
		return false
	}
	if s.maxMessagesPerPeer == 0 {
		return s.reserveBytesLocked(id, uint64(payloadBytes))
	}
	if s.peerCounts[id] >= s.maxMessagesPerPeer {
		return false
	}
	if !s.reserveBytesLocked(id, uint64(payloadBytes)) {
		return false
	}
	s.peerCounts[id]++
	return true
}

// reserveBytesLocked atomically applies both fairness and total-session
// limits. Failed reservations consume neither allowance. The subtraction form
// avoids integer overflow if these limits are changed in the future.
func (s *MessageSubscription) reserveBytesLocked(id peer.ID, payloadBytes uint64) bool {
	peerBytes := s.peerBytes[id]
	if exceedsByteBudget(peerBytes, payloadBytes, s.maxBytesPerPeer) ||
		exceedsByteBudget(s.sessionBytes, payloadBytes, s.maxSessionBytes) {
		return false
	}
	if s.maxBytesPerPeer > 0 {
		s.peerBytes[id] = peerBytes + payloadBytes
	}
	if s.maxSessionBytes > 0 {
		s.sessionBytes += payloadBytes
	}
	return true
}

func exceedsByteBudget(current, additional, maximum uint64) bool {
	return maximum > 0 && (current > maximum || additional > maximum-current)
}

// authorized rechecks mutable peer/frame constraints after the frame read
// without consuming a second message or byte allowance.
func (s *MessageSubscription) authorized(id peer.ID, payloadBytes int) bool {
	if s == nil || s.channel == nil || payloadBytes < 0 {
		return false
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return !s.stopped && s.authorizedLocked(id, payloadBytes)
}

func (s *MessageSubscription) authorizedLocked(id peer.ID, payloadBytes int) bool {
	if s.maxPayloadBytes > 0 && payloadBytes > s.maxPayloadBytes {
		return false
	}
	if s.allowedPeers != nil {
		if _, ok := s.allowedPeers[id]; !ok {
			return false
		}
	}
	return true
}

// narrowAllowedPeers restricts the allow-list after party formation. It never
// re-authorizes a peer that is not already allowed, so a ceremony can only
// shrink its party. Removed peers can never send to this session again, so
// their share of the shared session budget is released: peers that refused
// to join cannot starve the retained party. It performs no transport I/O.
func (s *MessageSubscription) narrowAllowedPeers(allowedPeers []peer.ID) bool {
	if s == nil {
		return false
	}
	allowed := make(map[peer.ID]struct{}, len(allowedPeers))
	for _, id := range allowedPeers {
		allowed[id] = struct{}{}
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.stopped {
		return false
	}
	// A nil current set is the unrestricted wildcard and may be narrowed once
	// to any explicit set.
	if s.allowedPeers != nil {
		for id := range allowed {
			if _, ok := s.allowedPeers[id]; !ok {
				return false
			}
		}
	}
	s.allowedPeers = allowed
	// Deleting the entries keeps a repeated narrowing from refunding twice.
	for id, spent := range s.peerBytes {
		if _, retained := allowed[id]; retained {
			continue
		}
		if spent > s.sessionBytes {
			spent = s.sessionBytes
		}
		s.sessionBytes -= spent
		delete(s.peerBytes, id)
		delete(s.peerCounts, id)
	}
	close(s.narrowed)
	s.narrowed = make(chan struct{})
	return true
}

func (s *MessageSubscription) Channel() chan *Message {
	if s == nil {
		return nil
	}
	return s.channel
}

// deliver applies backpressure when a bounded protocol queue is full. Dropping
// an authenticated FROST frame here can make an otherwise healthy ceremony
// fail merely because a valid burst exceeded the local buffer. Session byte
// and message budgets bound the number of blocked handlers.
//
// Narrowing wakes handlers whose peer was removed, and they give up their
// place. That bounds stale traffic but does not eliminate it. Go's select has
// no priority between the send and the narrowing signal: a waiting handler
// whose send a receive completes before the close reaches it still delivers,
// and so can a handler caught between releasing s.lock and entering select if
// the queue has room at that instant. Frames already queued stay queued.
// Consumers must therefore drop frames from removed peers with a sender check
// before parsing, as the keysign rounds and result waiter do.
//
// Cancellation is not strict against a send in progress either: a frame that
// races it can still enter its own ceremony's queue, where the coordinator's
// msgID and sender checks apply. Queues are per ceremony, so it never reaches
// another ceremony. Cancellation guarantees that no new frame is charged and
// that no delivery keeps waiting.
func (s *MessageSubscription) deliver(message *Message, stop <-chan struct{}) bool {
	if s == nil || s.channel == nil || message == nil {
		return false
	}
	for {
		s.lock.Lock()
		allowed := !s.stopped && s.authorizedLocked(message.PeerID, len(message.Payload))
		narrowed := s.narrowed
		s.lock.Unlock()
		if !allowed {
			return false
		}
		select {
		case s.channel <- message:
			return true
		case <-narrowed:
		case <-s.done:
			return false
		case <-stop:
			return false
		}
	}
}

// stop invalidates the subscription. Setting stopped and cancelling its
// context under s.lock makes every later charge or narrowing observe the stop.
// Cancelling the context triggers the resets that handlers registered for
// FROST frames still being read; stop does not wait for them.
func (s *MessageSubscription) stop() {
	if s == nil {
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.cancel()
}

// MessageIDSubscriber
type MessageIDSubscriber struct {
	lock        *sync.Mutex
	subscribers map[string]*MessageSubscription
}

// NewMessageIDSubscriber create a new instance of MessageIDSubscriber
func NewMessageIDSubscriber() *MessageIDSubscriber {
	return &MessageIDSubscriber{
		lock:        &sync.Mutex{},
		subscribers: make(map[string]*MessageSubscription),
	}
}

// Subscribe a message id
func (ms *MessageIDSubscriber) Subscribe(msgID string, channel chan *Message) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if previous := ms.subscribers[msgID]; previous != nil {
		previous.stop()
	}
	ms.subscribers[msgID] = newMessageSubscription(channel, nil, 0, 0, 0, 0)
}

// SubscribeRestricted registers a session-scoped authenticated peer allow-list
// and resource bounds. A nil allow-list permits every authenticated peer; an
// empty non-nil list accepts no peers. Delivery uses bounded backpressure so a
// valid burst is not silently discarded.
func (ms *MessageIDSubscriber) SubscribeRestricted(
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
) {
	ms.SubscribeRestrictedWithBudgets(
		msgID,
		channel,
		allowedPeers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		FrostMaxBytesPerPeer,
		FrostMaxSessionBytes,
	)
}

// SubscribeRestrictedWithBudgets registers the full set of resource bounds.
// It is separate from SubscribeRestricted to preserve compatibility for
// callers while ensuring the production FROST path always supplies explicit
// cumulative byte limits.
func (ms *MessageIDSubscriber) SubscribeRestrictedWithBudgets(
	msgID string,
	channel chan *Message,
	allowedPeers []peer.ID,
	maxPayloadBytes int,
	maxMessagesPerPeer uint32,
	maxBytesPerPeer uint64,
	maxSessionBytes uint64,
) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if previous := ms.subscribers[msgID]; previous != nil {
		previous.stop()
	}
	ms.subscribers[msgID] = newMessageSubscription(
		channel,
		allowedPeers,
		maxPayloadBytes,
		maxMessagesPerPeer,
		maxBytesPerPeer,
		maxSessionBytes,
	)
}

// UnSubscribe a messageid
func (ms *MessageIDSubscriber) UnSubscribe(msgID string) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if subscription := ms.subscribers[msgID]; subscription != nil {
		subscription.stop()
	}
	delete(ms.subscribers, msgID)
}

// NarrowAllowedPeers restricts an existing session allow-list after party
// formation. It never creates a subscription and rejects re-expansion.
func (ms *MessageIDSubscriber) NarrowAllowedPeers(msgID string, allowedPeers []peer.ID) bool {
	ms.lock.Lock()
	subscription, ok := ms.subscribers[msgID]
	ms.lock.Unlock()
	if !ok || subscription == nil {
		return false
	}
	return subscription.narrowAllowedPeers(allowedPeers)
}

// GetSubscribers return a subscriber of given message id
func (ms *MessageIDSubscriber) GetSubscriber(msgID string) *MessageSubscription {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	c, ok := ms.subscribers[msgID]
	if !ok {
		return nil
	}
	return c
}

// IsEmpty check whether there is subscribers
func (ms *MessageIDSubscriber) IsEmpty() bool {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	return len(ms.subscribers) == 0
}
