package observer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

const semAcquireTimeout = 50 * time.Millisecond

// Per-peer circuit breaker for the send path. A peer that accepts the stream
// but never ACKs holds a send token for the whole send deadline; under steady
// gossip that re-saturates the per-peer token budget and every further send to
// it fails acquire ("peer is busy"). After peerBreakerThreshold consecutive
// failed sends we open the breaker for peerBreakerCooldown and skip the peer
// entirely, so one unresponsive peer can neither tie up our sender nor spam the
// log. A single successful send closes it.
const (
	peerBreakerThreshold = 3
	peerBreakerCooldown  = 60 * time.Second
)

type peerSemaphore struct {
	tokens                          chan struct{}
	historicalXMRCertificateLimiter *rate.Limiter
	refCount                        int
	lastZero                        time.Time
	mu                              sync.Mutex
}

// peerBreaker is the per-peer send-failure circuit breaker state. Kept separate
// from peerSemaphore because a peer in cooldown has no in-flight sends
// (refCount 0) and would otherwise be pruned, losing the backoff.
type peerBreaker struct {
	consecutiveFailures int
	openUntil           time.Time // non-zero once open; cooldown expiry
	trialInFlight       bool      // a half-open trial send is out (admit exactly one)
	lastUpdate          time.Time // last state change, for idle pruning
}

type peerManager struct {
	logger zerolog.Logger

	semaphores map[peer.ID]*peerSemaphore
	mu         sync.Mutex
	limit      int
	// acquireTimeout bounds how long acquireContext waits for a token.
	acquireTimeout time.Duration

	breakers map[peer.ID]*peerBreaker
	bmu      sync.Mutex
	// nowFn is time.Now, overridable in tests to drive the cooldown clock.
	nowFn func() time.Time
}

func newPeerManager(logger zerolog.Logger, limit int) *peerManager {
	return &peerManager{
		logger:         logger.With().Str("component", "peer_manager").Logger(),
		semaphores:     make(map[peer.ID]*peerSemaphore),
		limit:          limit,
		acquireTimeout: semAcquireTimeout,
		breakers:       make(map[peer.ID]*peerBreaker),
		nowFn:          time.Now,
	}
}

func (m *peerManager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

// shouldSkipPeer reports whether the breaker for peer is open, so the caller
// should skip sending without acquiring a token. While open and inside the
// cooldown it always skips; once the cooldown elapses it admits exactly one
// half-open trial send (further callers keep skipping until that trial reports
// via recordSendResult).
func (m *peerManager) shouldSkipPeer(peer peer.ID) bool {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	b, ok := m.breakers[peer]
	if !ok || b.openUntil.IsZero() {
		return false // no entry or closed
	}
	if m.now().Before(b.openUntil) {
		return true // still cooling down
	}
	// Cooldown elapsed: admit a single half-open trial, skip the rest.
	if b.trialInFlight {
		return true
	}
	b.trialInFlight = true
	b.lastUpdate = m.now()
	return false
}

// finishTrial clears a half-open trial flag without recording a pass/fail. The
// caller MUST defer this for every send it starts, so a trial that exits on a
// path which does not reach recordSendResult (parent-context expiry, acquire
// timeout) cannot leave trialInFlight stuck true and permanently skip the peer.
// It is a no-op when there is no entry or no trial is in flight.
func (m *peerManager) finishTrial(peer peer.ID) {
	m.bmu.Lock()
	defer m.bmu.Unlock()
	b, ok := m.breakers[peer]
	if !ok || !b.trialInFlight {
		return
	}
	b.trialInFlight = false
	b.lastUpdate = m.now()
}

// recordSendResult updates the breaker after a send attempt that actually
// reached the peer (stream opened). A success closes the breaker; the
// peerBreakerThreshold'th consecutive failure opens it, and any later failure
// (including a failed half-open trial) re-arms the cooldown. Acquire-timeouts
// and shutdown-context errors are NOT routed here — they are local/global, not
// peer-health signals, and would false-trip a healthy peer under a send burst.
func (m *peerManager) recordSendResult(peer peer.ID, success bool) {
	m.bmu.Lock()
	defer m.bmu.Unlock()

	if success {
		delete(m.breakers, peer) // fully closes + resets the count
		return
	}

	b, ok := m.breakers[peer]
	if !ok {
		b = &peerBreaker{}
		m.breakers[peer] = b
	}
	b.consecutiveFailures++
	b.trialInFlight = false
	b.lastUpdate = m.now()

	if b.consecutiveFailures >= peerBreakerThreshold {
		wasClosed := b.openUntil.IsZero()
		b.openUntil = m.now().Add(peerBreakerCooldown)
		if wasClosed {
			// Log only on the closed->open transition, not on every later failure.
			m.logger.Warn().
				Str("peer", peer.String()).
				Int("failures", b.consecutiveFailures).
				Dur("cooldown", peerBreakerCooldown).
				Msg("opening send circuit breaker for unresponsive peer")
		}
	}
}

func (m *peerManager) getLimit() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limit
}

func (m *peerManager) getSemaphoreForAcquire(peer peer.ID) *peerSemaphore {
	m.mu.Lock()
	sem, exists := m.semaphores[peer]
	if !exists {
		// Create a new semaphore and fill it with tokens
		sem = &peerSemaphore{
			tokens:   make(chan struct{}, m.limit),
			refCount: 0,
			mu:       sync.Mutex{},
		}

		// Fill the channel with tokens
		for range m.limit {
			sem.tokens <- struct{}{}
		}

		m.semaphores[peer] = sem
	}
	m.mu.Unlock()

	sem.mu.Lock()
	sem.refCount++
	sem.mu.Unlock()
	return sem
}

func (m *peerManager) updateLimit(newLimit int) {
	if newLimit <= 0 {
		m.logger.Warn().Msgf("Attempted to update limit to invalid value: %d, ignoring", newLimit)
		return
	}

	m.mu.Lock()
	oldLimit := m.limit
	m.limit = newLimit

	// Update existing semaphores
	for peerID, sem := range m.semaphores {
		sem.mu.Lock()

		// Count how many tokens are currently in use
		inUseCount := cap(sem.tokens) - len(sem.tokens)

		// Create a new token channel with the new limit
		newTokens := make(chan struct{}, newLimit)

		// Fill available slots in the new channel
		availableSlots := newLimit - min(inUseCount, newLimit)
		for i := 0; i < availableSlots; i++ {
			newTokens <- struct{}{}
		}

		// Replace the tokens channel
		sem.tokens = newTokens
		sem.mu.Unlock()

		m.logger.Debug().
			Int("oldLimit", oldLimit).
			Int("newLimit", newLimit).
			Str("peer", peerID.String()).
			Int("tokensInUse", inUseCount).
			Int("availableSlots", availableSlots).
			Msg("Updated semaphore limit")
	}

	m.mu.Unlock()

	m.logger.Info().
		Int("oldLimit", oldLimit).
		Int("newLimit", newLimit).
		Int("updatedSemaphores", len(m.semaphores)).
		Msg("Updated peer manager limit")
}

func (m *peerManager) acquire(peer peer.ID) (*peerSemaphore, error) {
	return m.acquireContext(context.Background(), peer)
}

func (m *peerManager) acquireContext(ctx context.Context, peer peer.ID) (*peerSemaphore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sem := m.getSemaphoreForAcquire(peer)

	// Create a timeout channel
	timer := time.NewTimer(m.acquireTimeout)
	defer timer.Stop()

	sem.mu.Lock()
	tokens := sem.tokens
	sem.mu.Unlock()

	// Try to acquire token with timeout
	select {
	case <-tokens:
		// Successfully acquired token
		return sem, nil

	case <-ctx.Done():
		m.decRefCount(sem)
		return nil, ctx.Err()

	case <-timer.C:
		// Timed out
		m.decRefCount(sem)
		return nil, fmt.Errorf("peer %s is busy", peer.String())
	}
}

func (m *peerManager) release(sem *peerSemaphore) {
	sem.mu.Lock()
	// If channel capacity has changed, we need to be careful about releasing
	if len(sem.tokens) < cap(sem.tokens) {
		sem.tokens <- struct{}{}
	}
	sem.mu.Unlock()

	// Clean up semaphore if this was the last reference
	m.decRefCount(sem)
}

func (m *peerManager) decRefCount(sem *peerSemaphore) {
	sem.mu.Lock()
	defer sem.mu.Unlock()
	sem.refCount--
	if sem.refCount <= 0 {
		// do not delete here, delete in main loop periodically if ref counts are zero
		sem.lastZero = time.Now()
	}
}

func (m *peerManager) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for peerID, sem := range m.semaphores {
		sem.mu.Lock()
		if sem.refCount == 0 && time.Since(sem.lastZero) >= semaphorePruneInterval {
			delete(m.semaphores, peerID)
			m.logger.Debug().Msgf("pruned semaphore for peer: %s", peerID)
		}
		sem.mu.Unlock()
	}

	// Drop only idle breaker entries: keep an active cooldown and a peer with a
	// half-open trial in flight, so neither the backoff nor accumulating-but-recent
	// failure counts are lost.
	m.bmu.Lock()
	now := m.now()
	for peerID, b := range m.breakers {
		coolingDown := !b.openUntil.IsZero() && now.Before(b.openUntil)
		if coolingDown || b.trialInFlight {
			continue
		}
		if now.Sub(b.lastUpdate) >= semaphorePruneInterval {
			delete(m.breakers, peerID)
		}
	}
	m.bmu.Unlock()
}
