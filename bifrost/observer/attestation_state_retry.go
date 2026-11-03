package observer

import (
	"context"
	cryptorand "crypto/rand"
	"math/rand/v2"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
)

const (
	// The window permits several default 30-minute transfer budgets.
	// Fast failures can instead use five default ten-peer groups.
	// These independent bounds are operational headroom, not completeness.
	startupStateSyncWindow     = 2 * time.Hour
	startupStateSyncRequests   = 50
	startupStateSyncBackoff    = 5 * time.Second
	startupStateSyncMaxBackoff = time.Minute
)

type attestationStateGroup struct {
	done   chan struct{}
	result attestationStateGroupResult // published by closing done
}

type attestationStateCollection struct {
	ctx                            context.Context
	cancel                         context.CancelFunc
	deadline, next                 time.Time
	requestLimit, requests, rounds int
	tried, cycle                   map[peer.ID]bool
	group                          *attestationStateGroup
	result                         attestationStateGroupResult
	finished                       bool
}

func newAttestationStateCollection(parent context.Context, now time.Time, window time.Duration, requests int) *attestationStateCollection {
	ctx, cancel := context.WithDeadline(parent, now.Add(window))
	deadline, _ := ctx.Deadline()
	return &attestationStateCollection{
		ctx: ctx, cancel: cancel, deadline: deadline, next: now,
		requestLimit: requests, tried: make(map[peer.ID]bool), cycle: make(map[peer.ID]bool),
	}
}

// Equal jitter keeps every retry delayed, with a 2.5s initial lower bound
// and a 60s upper bound. The deck tick may delay dispatch further. No sleeper,
// timer or scheduler is added for backoff; context cancellation remains live.
func attestationStateRetryDelay(round int, random float64) time.Duration {
	delay := startupStateSyncBackoff
	for i := 1; i < round && delay < startupStateSyncMaxBackoff; i++ {
		delay = min(2*delay, startupStateSyncMaxBackoff)
	}
	return delay/2 + time.Duration(random*float64(delay/2))
}

// One random byte is sufficient for retry spreading; the deck tick already
// quantizes dispatch. The midpoint fallback preserves both bounds on read failure.
func attestationStateRetryJitter() float64 {
	var random [1]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return 0.5
	}
	return float64(random[0]) / 256
}

// Lifetime history always prioritizes newly eligible peers, even during reuse.
// The separate cycle rotates reused peers without forgetting lifetime attempts.
// Each pass visits fresh eligible peers first. An incomplete fresh group is
// intentionally smaller than AskPeers: reuse waits for the following backoff.
func (c *attestationStateCollection) selectPeers(eligible []peer.ID, fanout int) []peer.ID {
	fresh := make([]peer.ID, 0, len(eligible))
	for _, id := range eligible {
		if !c.tried[id] {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 && len(eligible) > 0 {
		for _, id := range eligible {
			if !c.cycle[id] {
				fresh = append(fresh, id)
			}
		}
		if len(fresh) == 0 {
			clear(c.cycle)
			fresh = append(fresh, eligible...)
		}
	}
	rand.Shuffle(len(fresh), func(i, j int) { fresh[i], fresh[j] = fresh[j], fresh[i] })
	count := min(len(fresh), max(0, fanout), max(0, c.requestLimit-c.requests))
	chosen := fresh[:count]
	for _, id := range chosen {
		c.tried[id] = true
		c.cycle[id] = true
	}
	return chosen
}

func (s *AttestationGossip) attestationStatePeers() []peer.ID {
	active := s.getActiveValidators()
	seen := make(map[peer.ID]bool)
	var eligible []peer.ID
	for _, id := range s.host.Peerstore().Peers() {
		if id != s.host.ID() && active[id] && !seen[id] {
			eligible = append(eligible, id)
			seen[id] = true
		}
	}
	return eligible
}

func (s *AttestationGossip) finishAttestationStateCollection(c *attestationStateCollection, reason string) {
	c.finished = true
	c.cancel()
	s.logAttestationStateCollection(c, reason)
}

// The observer's existing deck owner calls this independently of churn work.
// Only an Active transition arms a collection. Ordinary ticks and signing
// resumption never re-arm an exhausted collection; a Bifrost restart does.
func (s *AttestationGossip) tickAttestationStateSync(parent context.Context, active, activated bool, now time.Time) {
	s.startupStateMu.Lock()
	defer s.startupStateMu.Unlock()
	if s.startupStateStopped {
		return
	}
	c := s.startupState
	if activated {
		s.startupStatePending = now
		if c != nil {
			c.cancel()
		}
	}
	if !active {
		s.startupStatePending = time.Time{}
		if c != nil {
			c.cancel()
		}
	}
	if c != nil {
		if c.group != nil {
			select {
			case <-c.group.done:
				c.result.add(c.group.result)
				c.group = nil
				c.next = now.Add(attestationStateRetryDelay(c.rounds, attestationStateRetryJitter()))
			default:
				return
			}
		}
		if !c.finished {
			reason := c.result.completionReason()
			if reason == "" {
				switch {
				case !now.Before(c.deadline) || c.ctx.Err() == context.DeadlineExceeded:
					reason = "window_exhausted"
				case c.ctx.Err() != nil:
					reason = "cancelled"
				case c.requests >= c.requestLimit:
					reason = "requests_exhausted"
				}
			}
			if reason != "" {
				s.finishAttestationStateCollection(c, reason)
			}
		}
	}
	if !s.startupStatePending.IsZero() && (c == nil || c.finished) {
		c = newAttestationStateCollection(parent, s.startupStatePending, startupStateSyncWindow, startupStateSyncRequests)
		s.startupState = c
		s.startupStatePending = time.Time{}
	}
	if !active || c == nil || c.finished || now.Before(c.next) {
		return
	}
	// Joining an older activation consumes this activation's fixed window.
	if !now.Before(c.deadline) || c.ctx.Err() == context.DeadlineExceeded {
		s.finishAttestationStateCollection(c, "window_exhausted")
		return
	}
	// The parent may already have been cancelled when this activation arrived.
	if c.ctx.Err() != nil {
		s.finishAttestationStateCollection(c, "cancelled")
		return
	}
	fanout := s.config.AskPeers
	if fanout <= 0 {
		fanout = defaultAskPeers
	}
	peers := c.selectPeers(s.attestationStatePeers(), fanout)
	c.rounds++
	if len(peers) == 0 {
		c.next = now.Add(attestationStateRetryDelay(c.rounds, attestationStateRetryJitter()))
		return
	}
	// Reserve every launch before starting it, including dials that fail.
	c.requests += len(peers)
	group := &attestationStateGroup{done: make(chan struct{})}
	c.group = group
	go func() {
		group.result = s.askForAttestationState(c.ctx, peers...)
		close(group.done)
	}()
}

// Shutdown cancels and joins the one owned group before observer storage closes.
func (s *AttestationGossip) stopStartupStateSync() {
	s.startupStateMu.Lock()
	s.startupStateStopped = true
	s.startupStatePending = time.Time{}
	c := s.startupState
	var group *attestationStateGroup
	if c != nil {
		c.cancel()
		group = c.group
	}
	s.startupStateMu.Unlock()
	if group != nil {
		<-group.done
	}
	s.startupStateMu.Lock()
	defer s.startupStateMu.Unlock()
	if c != nil && !c.finished {
		if group != nil {
			c.result.add(group.result)
			c.group = nil
		}
		s.finishAttestationStateCollection(c, "shutdown")
	}
}
