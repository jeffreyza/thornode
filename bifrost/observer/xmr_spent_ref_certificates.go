package observer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"golang.org/x/time/rate"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	bifrosttypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	maxHistoricalXMRCertificateBytes            = 256 * 1024
	maxHistoricalXMRCertificateBuckets          = 1024
	maxHistoricalXMRCertificateBucketsPerSigner = 64
	maxHistoricalXMRCertificateRPCCacheEntries  = 1024

	// Allow a short reconnect/reconciliation burst while keeping both the
	// steady-state request rate and stalled in-flight bridge calls bounded.
	historicalXMRCertificatePeerRPCBurst      = 16
	historicalXMRCertificateGlobalRPCBurst    = 64
	historicalXMRCertificateSendConcurrency   = 16
	historicalXMRCertificateRPCConcurrency    = 16
	xmrSpentRefsMaxRetryDelay                 = 10 * time.Minute
	historicalXMRCertificateMaxRPCCacheTTL    = time.Minute
	historicalXMRCertificateMinRPCCacheTTL    = time.Second
	historicalXMRCertificatePeerRPCInterval   = 250 * time.Millisecond
	historicalXMRCertificateGlobalRPCInterval = time.Second / 32
)

const maxPendingObservedXMRSpentRefsPerReconcile = 16

type historicalXMRSpentRefCertificateBucket struct {
	observedTx   common.ObservedTx
	attestations map[string]*common.Attestation
	updatedAt    time.Time
	nextRetryAt  time.Time
	retryDelay   time.Duration
	applied      bool
}

type historicalXMRCertificateVaultCacheEntry struct {
	vault     thortypes.Vault
	expiresAt time.Time
}

type historicalXMRCertificatePlanCacheEntry struct {
	expiresAt time.Time
	wireMemo  string
}

// historicalXMRCertificateRPCControl contains only process-wide RPC state. A
// peer's limiter lives with its peer semaphore, so peerManager pruning also
// bounds limiter memory.
type historicalXMRCertificateRPCControl struct {
	globalLimiter *rate.Limiter
	rpcSem        chan struct{}
	vaultCache    map[string]historicalXMRCertificateVaultCacheEntry
	planCache     map[[sha256.Size]byte]historicalXMRCertificatePlanCacheEntry
}

func (s *AttestationGossip) historicalXMRCertificateRPCControlLocked() *historicalXMRCertificateRPCControl {
	if s.historicalXMRCertificateRPC == nil {
		s.historicalXMRCertificateRPC = &historicalXMRCertificateRPCControl{
			globalLimiter: rate.NewLimiter(rate.Every(historicalXMRCertificateGlobalRPCInterval), historicalXMRCertificateGlobalRPCBurst),
			rpcSem:        make(chan struct{}, historicalXMRCertificateRPCConcurrency),
			vaultCache:    make(map[string]historicalXMRCertificateVaultCacheEntry),
			planCache:     make(map[[sha256.Size]byte]historicalXMRCertificatePlanCacheEntry),
		}
	}
	return s.historicalXMRCertificateRPC
}

// acquireHistoricalXMRCertificateRPC reserves process-wide capacity without
// blocking a stream handler or reconciliation loop behind a stalled bridge.
func (s *AttestationGossip) acquireHistoricalXMRCertificateRPC() (func(), error) {
	s.mu.Lock()
	control := s.historicalXMRCertificateRPCControlLocked()
	sem := control.rpcSem
	s.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	default:
		return nil, fmt.Errorf("historical certificate RPC capacity exhausted")
	}
}

// allowHistoricalXMRCertificate charges the certificate protocol to the same
// peer object that already owns its receive-concurrency limit. peerManager's
// normal pruning therefore also bounds limiter lifetime and memory use.
func (sem *peerSemaphore) allowHistoricalXMRCertificate() bool {
	sem.mu.Lock()
	if sem.historicalXMRCertificateLimiter == nil {
		sem.historicalXMRCertificateLimiter = rate.NewLimiter(
			rate.Every(historicalXMRCertificatePeerRPCInterval),
			historicalXMRCertificatePeerRPCBurst,
		)
	}
	limiter := sem.historicalXMRCertificateLimiter
	sem.mu.Unlock()
	return limiter.Allow()
}

func (s *AttestationGossip) allowHistoricalXMRCertificateGlobally() bool {
	s.mu.Lock()
	limiter := s.historicalXMRCertificateRPCControlLocked().globalLimiter
	s.mu.Unlock()
	return limiter.Allow()
}

// checkHistoricalXMRCertificateCapacity rejects work that cannot consume
// certificate state before it can trigger GetVault or planned-outbound RPCs.
// The same checks are repeated during publication because another goroutine may
// fill capacity while validation is in flight.
func (s *AttestationGossip) checkHistoricalXMRCertificateCapacity(
	payloadHash [sha256.Size]byte,
	signer string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictAppliedHistoricalXMRCertificatesLocked(payloadHash, signer)
	bucket := s.historicalXMRSpentRefCertificates[payloadHash]
	if bucket == nil && len(s.historicalXMRSpentRefCertificates) >= maxHistoricalXMRCertificateBuckets {
		return fmt.Errorf("historical certificate bucket capacity exhausted")
	}
	duplicate := false
	if bucket != nil {
		_, duplicate = bucket.attestations[signer]
	}
	if !duplicate && s.historicalXMRCertificateSignerUse[signer] >= maxHistoricalXMRCertificateBucketsPerSigner {
		return fmt.Errorf("historical certificate signer capacity exhausted")
	}
	return nil
}

// An applied receiver has already handed replay to durable scanner storage.
// Its memory signatures may be evicted; producer retransmission is separately
// owned by the durable certificate outbox until THORChain finalizes the payout.
func (s *AttestationGossip) evictAppliedHistoricalXMRCertificatesLocked(incoming [sha256.Size]byte, signer string) {
	for s.historicalXMRCertificateSignerUse[signer] >= maxHistoricalXMRCertificateBucketsPerSigner ||
		len(s.historicalXMRSpentRefCertificates) >= maxHistoricalXMRCertificateBuckets {
		var oldest [sha256.Size]byte
		var chosen *historicalXMRSpentRefCertificateBucket
		for hash, bucket := range s.historicalXMRSpentRefCertificates {
			if hash == incoming || !bucket.applied {
				continue
			}
			if s.historicalXMRCertificateSignerUse[signer] >= maxHistoricalXMRCertificateBucketsPerSigner && bucket.attestations[signer] == nil {
				continue
			}
			if chosen == nil || bucket.updatedAt.Before(chosen.updatedAt) {
				oldest, chosen = hash, bucket
			}
		}
		if chosen == nil {
			return
		}
		for member := range chosen.attestations {
			s.historicalXMRCertificateSignerUse[member]--
			if s.historicalXMRCertificateSignerUse[member] <= 0 {
				delete(s.historicalXMRCertificateSignerUse, member)
			}
		}
		delete(s.historicalXMRSpentRefCertificates, oldest)
	}
}

func (s *AttestationGossip) historicalXMRCertificateRPCCacheTTL() time.Duration {
	ttl := s.config.ObserveReconcileInterval
	if ttl <= 0 {
		ttl = defaultObserveReconcileInterval
	}
	if ttl < historicalXMRCertificateMinRPCCacheTTL {
		return historicalXMRCertificateMinRPCCacheTTL
	}
	if ttl > historicalXMRCertificateMaxRPCCacheTTL {
		return historicalXMRCertificateMaxRPCCacheTTL
	}
	return ttl
}

func (s *AttestationGossip) xmrSpentRefsInitialRetryDelay() time.Duration {
	delay := s.config.ObserveReconcileInterval
	if delay <= 0 {
		return defaultObserveReconcileInterval
	}
	return delay
}

func (s *AttestationGossip) xmrSpentRefsRetryDelayCap() time.Duration {
	initial := s.xmrSpentRefsInitialRetryDelay()
	if initial > xmrSpentRefsMaxRetryDelay {
		return initial
	}
	return xmrSpentRefsMaxRetryDelay
}

// historicalXMRCertificateVault returns a short-lived, positively validated
// source vault. Errors and missing vaults are never cached, so transient
// THORNode failures remain immediately retryable. Status does not grant authority.
// The caller must hold one certificate RPC slot.
func (s *AttestationGossip) historicalXMRCertificateVault(vaultPubKey common.PubKey) (thortypes.Vault, error) {
	pubKey := vaultPubKey.String()
	now := time.Now()
	s.mu.Lock()
	control := s.historicalXMRCertificateRPCControlLocked()
	entry, ok := control.vaultCache[pubKey]
	if ok && now.Before(entry.expiresAt) {
		s.mu.Unlock()
		return entry.vault, nil
	}
	delete(control.vaultCache, pubKey)
	s.mu.Unlock()

	vault, err := s.bridge.GetVault(pubKey)
	if err != nil {
		return thortypes.Vault{}, err
	}
	if err = validateHistoricalXMRCertificateVault(vaultPubKey, vault); err != nil {
		return thortypes.Vault{}, err
	}
	s.mu.Lock()
	control = s.historicalXMRCertificateRPCControlLocked()
	if _, exists := control.vaultCache[pubKey]; exists || len(control.vaultCache) < maxHistoricalXMRCertificateRPCCacheEntries {
		control.vaultCache[pubKey] = historicalXMRCertificateVaultCacheEntry{
			vault:     vault,
			expiresAt: time.Now().Add(s.historicalXMRCertificateRPCCacheTTL()),
		}
	}
	s.mu.Unlock()
	return vault, nil
}

func validateHistoricalXMRCertificateVault(vaultPubKey common.PubKey, vault thortypes.Vault) error {
	if vault.IsEmpty() {
		return fmt.Errorf("historical certificate source vault is empty")
	}
	if !vault.PubKeyFrost.IsEmpty() {
		if !vault.PubKeyFrost.Equals(vaultPubKey) {
			return fmt.Errorf("historical certificate source vault mismatch")
		}
	} else if !vault.PubKey.Equals(vaultPubKey) {
		return fmt.Errorf("historical certificate source vault mismatch")
	}
	return nil
}

// historicalXMRCertificatePlan caches only a successful exact-payload match,
// including the exact planned wire memo needed by a later sidecar import.
// Scanner lag and RPC errors therefore remain retryable under the rate limit.
// The caller must hold one certificate RPC slot.
func (s *AttestationGossip) historicalXMRCertificatePlan(
	payloadHash [sha256.Size]byte,
	obsTx common.ObservedTx,
) (string, bool) {
	now := time.Now()
	s.mu.Lock()
	control := s.historicalXMRCertificateRPCControlLocked()
	entry, ok := control.planCache[payloadHash]
	if ok && now.Before(entry.expiresAt) {
		s.mu.Unlock()
		return entry.wireMemo, true
	}
	delete(control.planCache, payloadHash)
	s.mu.Unlock()

	wireMemo, ok := s.plannedXMRWireMemo(obsTx)
	if !ok {
		return "", false
	}
	s.mu.Lock()
	control = s.historicalXMRCertificateRPCControlLocked()
	if _, exists := control.planCache[payloadHash]; exists || len(control.planCache) < maxHistoricalXMRCertificateRPCCacheEntries {
		control.planCache[payloadHash] = historicalXMRCertificatePlanCacheEntry{
			expiresAt: time.Now().Add(s.historicalXMRCertificateRPCCacheTTL()),
			wireMemo:  wireMemo,
		}
	}
	s.mu.Unlock()
	return wireMemo, true
}

// validateHistoricalXMRSpentRefCertificate is the admission boundary for the
// certificate-only protocol. It accepts an inactive sender for exactly one
// purpose: certifying a daemon-observed, planned outbound from an XMR
// vault that the sender historically controlled. The exact ObservedTx payload
// is application-signed and, on the network path, bound to the authenticated
// libp2p peer before it can consume certificate state.
func (s *AttestationGossip) validateHistoricalXMRSpentRefCertificate(
	tx common.AttestTx,
	transportPeer peer.ID,
) ([sha256.Size]byte, common.AttestTx, error) {
	var empty [sha256.Size]byte
	if tx.Inbound || !tx.ObsTx.Tx.Chain.Equals(common.XMRChain) {
		return empty, common.AttestTx{}, fmt.Errorf("historical certificate is restricted to XMR outbounds")
	}
	if tx.Attestation == nil || len(tx.Attestation.PubKey) == 0 || len(tx.Attestation.Signature) == 0 {
		return empty, common.AttestTx{}, fmt.Errorf("historical certificate omitted its attestation")
	}
	if err := common.ValidateSpentOutputRefs(tx.ObsTx.Tx.Chain, tx.ObsTx.Tx.ID, tx.ObsTx.SpentOutputRefs); err != nil {
		return empty, common.AttestTx{}, fmt.Errorf("historical certificate has invalid spent refs: %w", err)
	}
	if len(tx.ObsTx.SpentOutputRefs) == 0 {
		return empty, common.AttestTx{}, fmt.Errorf("historical certificate has no spent refs")
	}
	if transportPeer == "" {
		if s.isActiveValidator(s.host.ID()) {
			return empty, common.AttestTx{}, fmt.Errorf("active validators must use ordinary observation gossip")
		}
	} else {
		if s.isActiveValidator(transportPeer) {
			return empty, common.AttestTx{}, fmt.Errorf("active validators must use ordinary observation gossip")
		}
		transportKey, err := transportPeer.ExtractPublicKey()
		if err != nil {
			return empty, common.AttestTx{}, fmt.Errorf("extract historical certificate transport key: %w", err)
		}
		rawTransportKey, err := transportKey.Raw()
		if err != nil || !bytes.Equal(rawTransportKey, tx.Attestation.PubKey) {
			return empty, common.AttestTx{}, fmt.Errorf("historical certificate signer does not match transport peer")
		}
	}

	payload, err := tx.ObsTx.GetSignablePayloadWithInbound(false)
	if err != nil {
		return empty, common.AttestTx{}, fmt.Errorf("encode historical certificate payload: %w", err)
	}
	if err = verifySignature(payload, tx.Attestation.Signature, tx.Attestation.PubKey); err != nil {
		return empty, common.AttestTx{}, fmt.Errorf("verify historical certificate signature: %w", err)
	}
	payloadHash := sha256.Sum256(payload)
	if transportPeer != "" {
		if err = s.checkHistoricalXMRCertificateCapacity(payloadHash, string(tx.Attestation.PubKey)); err != nil {
			return empty, common.AttestTx{}, err
		}
	}

	if s.bridge == nil {
		return empty, common.AttestTx{}, fmt.Errorf("historical certificate vault source is unavailable")
	}
	releaseRPC, err := s.acquireHistoricalXMRCertificateRPC()
	if err != nil {
		return empty, common.AttestTx{}, err
	}
	defer releaseRPC()
	vault, err := s.historicalXMRCertificateVault(tx.ObsTx.ObservedPubKey)
	if err != nil {
		return empty, common.AttestTx{}, fmt.Errorf("load historical certificate source vault: %w", err)
	}
	if !historicalVaultContainsAttester(vault.Membership, tx.Attestation.PubKey) {
		return empty, common.AttestTx{}, fmt.Errorf(
			"%w: historical certificate signer is not a source-vault member",
			bifrosttypes.ErrObservedTxNotApplicable,
		)
	}
	if _, ok := s.historicalXMRCertificatePlan(payloadHash, tx.ObsTx); !ok {
		return empty, common.AttestTx{}, fmt.Errorf("historical certificate does not match a planned outbound")
	}

	// AllowFutureObservation and KeysignMs are not certificate authority. The
	// former is an unsigned observer-routing hint and the latter is deliberately
	// excluded from the signed payload, so canonicalize both before retention.
	tx.AllowFutureObservation = false
	tx.ObsTx.KeysignMs = 0
	return payloadHash, tx, nil
}

func historicalVaultContainsAttester(membership []string, attester []byte) bool {
	for _, member := range membership {
		pk, err := common.DecodePubKey(member)
		if err == nil && bytes.Equal(pk.Bytes(), attester) {
			return true
		}
	}
	return false
}

func (s *AttestationGossip) acceptHistoricalXMRSpentRefCertificate(tx common.AttestTx, transportPeer peer.ID) error {
	payloadHash, tx, err := s.validateHistoricalXMRSpentRefCertificate(tx, transportPeer)
	if err != nil {
		return err
	}
	// Retain an owned copy. The local scanner path passes an ObservedTx whose
	// slice fields may be reused after this method returns; later mutation must
	// not detach an authorized import from the bytes the members signed.
	encodedObservedTx, err := tx.ObsTx.Marshal()
	if err != nil {
		return fmt.Errorf("copy historical certificate observation: %w", err)
	}
	var retainedObservedTx common.ObservedTx
	if err = retainedObservedTx.Unmarshal(encodedObservedTx); err != nil {
		return fmt.Errorf("decode historical certificate observation copy: %w", err)
	}
	tx.ObsTx = retainedObservedTx

	now := time.Now()
	initialRetryDelay := s.xmrSpentRefsInitialRetryDelay()
	signer := string(tx.Attestation.PubKey)
	s.mu.Lock()
	if s.historicalXMRSpentRefCertificates == nil {
		s.historicalXMRSpentRefCertificates = make(map[[sha256.Size]byte]*historicalXMRSpentRefCertificateBucket)
	}
	if s.historicalXMRCertificateSignerUse == nil {
		s.historicalXMRCertificateSignerUse = make(map[string]int)
	}
	s.evictAppliedHistoricalXMRCertificatesLocked(payloadHash, signer)
	bucket := s.historicalXMRSpentRefCertificates[payloadHash]
	duplicate := false
	if bucket != nil {
		_, duplicate = bucket.attestations[signer]
	}
	// Enforce this before publishing a new bucket. Otherwise a signer at its
	// quota can leave an empty bucket behind for every rejected payload.
	if !duplicate && s.historicalXMRCertificateSignerUse[signer] >= maxHistoricalXMRCertificateBucketsPerSigner {
		s.mu.Unlock()
		return fmt.Errorf("historical certificate signer capacity exhausted")
	}
	if bucket == nil {
		if len(s.historicalXMRSpentRefCertificates) >= maxHistoricalXMRCertificateBuckets {
			s.mu.Unlock()
			return fmt.Errorf("historical certificate bucket capacity exhausted")
		}
		bucket = &historicalXMRSpentRefCertificateBucket{
			observedTx:   tx.ObsTx,
			attestations: make(map[string]*common.Attestation),
			updatedAt:    now,
		}
		s.historicalXMRSpentRefCertificates[payloadHash] = bucket
	}
	if !duplicate {
		attestation := *tx.Attestation
		attestation.PubKey = append([]byte(nil), tx.Attestation.PubKey...)
		attestation.Signature = append([]byte(nil), tx.Attestation.Signature...)
		bucket.attestations[signer] = &attestation
		s.historicalXMRCertificateSignerUse[signer]++
	}
	// A newly admitted share (or an explicit duplicate retry signal) gets one
	// immediate attempt below, then starts a fresh exponential retry schedule.
	bucket.updatedAt = now
	bucket.retryDelay = initialRetryDelay
	bucket.nextRetryAt = now.Add(initialRetryDelay)
	s.mu.Unlock()

	// A duplicate is also a retry signal. This matters after the bounded import
	// queue rejects work: the failed conflict reservation is re-admissible, while
	// retained signatures need not allocate another bucket.
	s.tryApplyHistoricalXMRSpentRefCertificate(payloadHash)
	return nil
}

func (s *AttestationGossip) tryApplyHistoricalXMRSpentRefCertificate(payloadHash [sha256.Size]byte) {
	obsTx, attestations, applied, ok := s.historicalXMRSpentRefCertificateSnapshot(payloadHash)
	if !ok || applied || s.observerApplySpentOutputRefs == nil {
		return
	}
	// Ordinary observation signatures may complete a historical quorum. Anchor
	// the lookup to this certificate's exact observation instead of scanning
	// every live observation bucket.
	attestations = append(attestations, s.observedXMRPayloadAttestations(obsTx, payloadHash)...)
	wireMemo, ok := s.shouldApplyHistoricalXMRSpentRefsFromAttestations(obsTx, payloadHash, attestations)
	if !ok {
		return
	}

	conflictKey := xmrSpentRefsConflictKeyFor(obsTx)
	identity := xmrSpentRefsIdentity(obsTx.SpentOutputRefs)
	s.applySpentRefsAsync(spentOutputRefsImport{
		Tx:       obsTx,
		WireMemo: wireMemo,
	}, func(applied bool) {
		if applied {
			s.markHistoricalXMRSpentRefCertificateApplied(payloadHash)
			s.markXMRSpentRefsAppliedByPayload(obsTx, payloadHash)
		}
		s.resolveAuthorizedXMRSpentRefs(conflictKey, identity, applied)
	})
}

// shouldApplyHistoricalXMRSpentRefsFromAttestations applies the ordinary
// source-vault quorum rules while reusing the bounded certificate RPC caches.
func (s *AttestationGossip) shouldApplyHistoricalXMRSpentRefsFromAttestations(
	obsTx common.ObservedTx,
	payloadHash [sha256.Size]byte,
	attestations []*common.Attestation,
) (string, bool) {
	if len(attestations) == 0 || s.bridge == nil {
		return "", false
	}
	releaseRPC, err := s.acquireHistoricalXMRCertificateRPC()
	if err != nil {
		s.logger.Debug().Err(err).Msg("defer historical xmr spent refs: RPC capacity exhausted")
		return "", false
	}
	defer releaseRPC()
	vault, err := s.historicalXMRCertificateVault(obsTx.ObservedPubKey)
	if err != nil {
		s.logger.Debug().Err(err).
			Str("vault", obsTx.ObservedPubKey.String()).
			Msg("skip historical xmr spent refs: cannot load source vault")
		return "", false
	}
	memberAttestations, membershipSize, ok := sourceVaultAttestationQuorumForMembership(
		vault.Membership,
		attestations,
	)
	if !ok {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Str("vault", obsTx.ObservedPubKey.String()).
			Int("source_vault_attestations", memberAttestations).
			Int("source_vault_members", membershipSize).
			Msg("skip historical xmr spent refs: source vault quorum not reached")
		return "", false
	}
	wireMemo, ok := s.historicalXMRCertificatePlan(payloadHash, obsTx)
	if !ok {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Str("memo", obsTx.Tx.Memo).
			Msg("skip historical xmr spent refs: observed outbound does not match planned txout")
		return "", false
	}
	if !s.rememberAuthorizedXMRSpentRefs(obsTx) {
		return "", false
	}
	return wireMemo, true
}

func (s *AttestationGossip) historicalXMRSpentRefCertificateSnapshot(
	payloadHash [sha256.Size]byte,
) (common.ObservedTx, []*common.Attestation, bool, bool) {
	return s.historicalXMRSpentRefCertificateSnapshotWithContext(context.Background(), payloadHash)
}

func (s *AttestationGossip) historicalXMRSpentRefCertificateSnapshotWithContext(ctx context.Context, payloadHash [sha256.Size]byte) (common.ObservedTx, []*common.Attestation, bool, bool) {
	if lockAttestationContext(ctx, &s.mu) != nil {
		return common.ObservedTx{}, nil, false, false
	}
	defer s.mu.Unlock()
	bucket := s.historicalXMRSpentRefCertificates[payloadHash]
	if bucket == nil {
		return common.ObservedTx{}, nil, false, false
	}
	attestations := make([]*common.Attestation, 0, len(bucket.attestations))
	for _, attestation := range bucket.attestations {
		copyAttestation := *attestation
		copyAttestation.PubKey = append([]byte(nil), attestation.PubKey...)
		copyAttestation.Signature = append([]byte(nil), attestation.Signature...)
		attestations = append(attestations, &copyAttestation)
	}
	return bucket.observedTx, attestations, bucket.applied, true
}

func (s *AttestationGossip) historicalXMRSpentRefCertificateApplied(obsTx common.ObservedTx) bool {
	payload, err := obsTx.GetSignablePayloadWithInbound(false)
	if err != nil {
		return false
	}
	_, _, applied, ok := s.historicalXMRSpentRefCertificateSnapshot(sha256.Sum256(payload))
	return ok && applied
}

// visitObservedXMRPayloadStates visits every live observation bucket that can
// correspond to an exact XMR spent-ref payload accepted by the sidecar. Such
// imports are always outbound, and AllowFutureObservation is the only unsigned
// txKey dimension, so its two boolean values exhaust the candidates. The full
// payload hash check is still required because txKey intentionally omits signed
// fields such as the memo.
// visit runs with the state lock held and must not acquire s.mu.
func (s *AttestationGossip) visitObservedXMRPayloadStates(
	obsTx common.ObservedTx,
	payloadHash [sha256.Size]byte,
	visit func(*AttestationState[*attestableObservedTx]),
) {
	for _, allowFutureObservation := range [...]bool{false, true} {
		k := observedTxKey(obsTx, false, allowFutureObservation)
		s.mu.Lock()
		state, ok := s.observedTxs[k]
		if ok {
			// Lock the state before releasing the map lock so pruning cannot
			// remove, pool, and reuse it between lookup and inspection.
			state.mu.Lock()
		}
		s.mu.Unlock()
		if !ok {
			continue
		}

		// The state lock pins this pooled object, allowing the relatively costly
		// payload marshal and hash to run without holding the hot global mutex.
		if state.Item != nil {
			payload, err := state.Item.GetSignablePayload()
			// txKey is deliberately lossy, so only expose a state's signatures or
			// latch after binding it to the certificate's exact signed bytes.
			if err == nil && sha256.Sum256(payload) == payloadHash {
				visit(state)
			}
		}
		state.mu.Unlock()
	}
}

// observedXMRPayloadAttestations returns all verified ordinary attestations,
// including committed shares, for the certificate's exact signed payload across
// both observation routes.
func (s *AttestationGossip) observedXMRPayloadAttestations(
	obsTx common.ObservedTx,
	payloadHash [sha256.Size]byte,
) []*common.Attestation {
	var attestations []*common.Attestation
	s.visitObservedXMRPayloadStates(obsTx, payloadHash, func(state *AttestationState[*attestableObservedTx]) {
		attestations = append(attestations, state.VerifiedAttestationsCopy()...)
	})
	return attestations
}

func (s *AttestationGossip) markHistoricalXMRSpentRefCertificateApplied(payloadHash [sha256.Size]byte) {
	s.mu.Lock()
	if bucket := s.historicalXMRSpentRefCertificates[payloadHash]; bucket != nil {
		bucket.applied = true
		bucket.updatedAt = time.Now()
	}
	s.mu.Unlock()
}

// markXMRSpentRefsAppliedByPayload latches only live states for the exact
// payload accepted by the sidecar. Rechecking the hash prevents a same-key,
// different-payload replacement from being marked after the async import.
func (s *AttestationGossip) markXMRSpentRefsAppliedByPayload(
	obsTx common.ObservedTx,
	payloadHash [sha256.Size]byte,
) {
	s.visitObservedXMRPayloadStates(obsTx, payloadHash, func(state *AttestationState[*attestableObservedTx]) {
		state.spentRefsApplied = true
	})
}

func (s *AttestationGossip) pruneHistoricalXMRSpentRefCertificatesLocked(now time.Time) {
	retention := s.config.NonQuorumTimeout
	if retention <= 0 {
		retention = defaultNonQuorumTimeout
	}
	for payloadHash, bucket := range s.historicalXMRSpentRefCertificates {
		if now.Sub(bucket.updatedAt) <= retention {
			continue
		}
		for signer := range bucket.attestations {
			if s.historicalXMRCertificateSignerUse[signer] <= 1 {
				delete(s.historicalXMRCertificateSignerUse, signer)
			} else {
				s.historicalXMRCertificateSignerUse[signer]--
			}
		}
		delete(s.historicalXMRSpentRefCertificates, payloadHash)
	}
	if s.historicalXMRCertificateRPC == nil {
		return
	}
	for pubKey, entry := range s.historicalXMRCertificateRPC.vaultCache {
		if !now.Before(entry.expiresAt) {
			delete(s.historicalXMRCertificateRPC.vaultCache, pubKey)
		}
	}
	for payloadHash, entry := range s.historicalXMRCertificateRPC.planCache {
		if !now.Before(entry.expiresAt) {
			delete(s.historicalXMRCertificateRPC.planCache, payloadHash)
		}
	}
}

func (s *AttestationGossip) handleStreamXMRSpentRefCertificate(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	logger := s.logger.With().Str("remote_peer", remotePeer.String()).Logger()
	defer closeStream(logger, stream)
	sem, err := s.peerMgr.acquire(remotePeer)
	if err != nil {
		logger.Debug().Err(err).Msg("reject concurrent historical XMR spent-ref certificate")
		return
	}
	defer s.peerMgr.release(sem)
	// Charge the peer before reading or validating the frame. The limiter is
	// pruned with this semaphore and therefore cannot grow independently of the
	// peer manager.
	if !sem.allowHistoricalXMRCertificate() || !s.allowHistoricalXMRCertificateGlobally() {
		logger.Debug().Msg("reject rate-limited historical XMR spent-ref certificate")
		return
	}

	data, err := readBoundedCertificateFrame(stream, maxHistoricalXMRCertificateBytes, s.config.PeerTimeout)
	if err != nil {
		logger.Warn().Err(err).Msg("reject historical XMR spent-ref certificate frame")
		return
	}
	var tx common.AttestTx
	if err = tx.Unmarshal(data); err != nil {
		logger.Warn().Err(err).Msg("reject malformed historical XMR spent-ref certificate")
		return
	}
	if err = s.acceptHistoricalXMRSpentRefCertificate(tx, remotePeer); err != nil {
		logger.Warn().Err(err).Msg("reject unauthorized historical XMR spent-ref certificate")
		return
	}
	if err = p2p.WriteStreamWithBuffer([]byte(p2p.StreamMsgDone), stream); err != nil {
		logger.Debug().Err(err).Msg("fail to acknowledge historical XMR spent-ref certificate")
	}
}

func readBoundedCertificateFrame(stream network.Stream, maxBytes int, timeout time.Duration) ([]byte, error) {
	if stream == nil || maxBytes <= 0 {
		return nil, fmt.Errorf("invalid historical certificate stream limit")
	}
	if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	if err := stream.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	header := make([]byte, p2p.LengthHeader)
	if _, err := io.ReadFull(stream, header); err != nil {
		return nil, fmt.Errorf("read historical certificate frame header: %w", err)
	}
	length := binary.LittleEndian.Uint32(header)
	if length == 0 || uint64(length) > uint64(maxBytes) {
		return nil, fmt.Errorf("historical certificate frame size %d exceeds limit %d", length, maxBytes)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(stream, payload); err != nil {
		return nil, fmt.Errorf("read historical certificate frame payload: %w", err)
	}
	return payload, nil
}

func (s *AttestationGossip) broadcastHistoricalXMRSpentRefCertificate(ctx context.Context, tx common.AttestTx) {
	payload, err := tx.Marshal()
	if err != nil || len(payload) > maxHistoricalXMRCertificateBytes {
		s.logger.Warn().Err(err).Int("payload_bytes", len(payload)).Msg("skip invalid historical XMR certificate broadcast")
		return
	}
	active := s.getActiveValidators()
	s.mu.Lock()
	if s.historicalXMRCertificateSendSem == nil {
		s.historicalXMRCertificateSendSem = make(chan struct{}, historicalXMRCertificateSendConcurrency)
	}
	sendSem := s.historicalXMRCertificateSendSem
	s.mu.Unlock()
	peerTimeout := s.config.PeerTimeout
	if peerTimeout <= 0 {
		peerTimeout = defaultPeerTimeout
	}
	for _, remotePeer := range s.host.Peerstore().Peers() {
		if remotePeer == s.host.ID() || !active[remotePeer] {
			continue
		}
		// Wait for bounded send capacity instead of abandoning the remaining
		// peers. The caller runs outside the shared observer/maintenance loops,
		// and each slot is released within the per-peer timeout below.
		select {
		case sendSem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		go func(remotePeer peer.ID) {
			defer func() { <-sendSem }()
			peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
			defer cancel()
			stream, streamErr := s.host.NewStream(peerCtx, remotePeer, xmrSpentRefCertificateProtocol)
			if streamErr != nil {
				return
			}
			defer closeStream(s.logger, stream)
			if streamErr = p2p.WriteStreamWithBufferWithContext(peerCtx, payload, stream); streamErr != nil {
				return
			}
			_, _ = p2p.ReadStreamWithBufferWithContext(peerCtx, stream)
		}(remotePeer)
	}
}

// triggerHistoricalXMRSpentRefCertificateReconcile keeps a full certificate
// fanout off the maintenance ticker. A pass may span several peer-timeout
// waves, so overlapping ticks are coalesced into the running pass.
func (s *AttestationGossip) triggerHistoricalXMRSpentRefCertificateReconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.historicalXMRCertificateReconcileRunning {
		s.mu.Unlock()
		return
	}
	s.historicalXMRCertificateReconcileRunning = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.historicalXMRCertificateReconcileRunning = false
			s.mu.Unlock()
		}()
		s.reconcileHistoricalXMRSpentRefCertificates(ctx)
	}()
}

func (s *AttestationGossip) reconcileHistoricalXMRSpentRefCertificates(ctx context.Context) {
	if s.certificateObserver != nil {
		s.certificateObserver.retryPendingXMRCertificates(ctx)
	}
	now := time.Now()
	initialRetryDelay := s.xmrSpentRefsInitialRetryDelay()
	retryDelayCap := s.xmrSpentRefsRetryDelayCap()
	s.mu.Lock()
	retained := make([][sha256.Size]byte, 0)
	for payloadHash, bucket := range s.historicalXMRSpentRefCertificates {
		if bucket.applied || (!bucket.nextRetryAt.IsZero() && now.Before(bucket.nextRetryAt)) {
			continue
		}
		// Claim this retry while holding the certificate lock so overlapping
		// reconciliation triggers cannot duplicate the RPC work. A new incoming
		// share resets the schedule and still receives an immediate attempt.
		delay := bucket.retryDelay
		if delay <= 0 {
			delay = initialRetryDelay
		}
		if delay >= retryDelayCap/2 {
			delay = retryDelayCap
		} else {
			delay *= 2
		}
		bucket.retryDelay = delay
		bucket.nextRetryAt = now.Add(delay)
		// Every receiver retries locally. An active receiver may have admitted a
		// complete certificate just as its bounded sidecar-import queue filled;
		// retry must not depend on another network duplicate arriving.
		retained = append(retained, payloadHash)
	}
	s.mu.Unlock()

	for _, payloadHash := range retained {
		// Retry local bounded scheduling even if every previous broadcast was a
		// duplicate and no new signature arrived after capacity returned.
		s.tryApplyHistoricalXMRSpentRefCertificate(payloadHash)
	}
}

// triggerPendingObservedXMRSpentRefsReconcile keeps potentially slow Thornode
// lookups off the maintenance ticker. Only one pass may run at a time, so a
// stalled bridge cannot accumulate reconciliation goroutines on later ticks.
func (s *AttestationGossip) triggerPendingObservedXMRSpentRefsReconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.pendingXMRSpentRefsReconcileRunning {
		s.mu.Unlock()
		return
	}
	s.pendingXMRSpentRefsReconcileRunning = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.pendingXMRSpentRefsReconcileRunning = false
			s.mu.Unlock()
		}()
		s.reconcilePendingObservedXMRSpentRefs(ctx)
	}()
}

// reconcilePendingObservedXMRSpentRefs supplies a periodic retry trigger for
// ordinary active-validator state retained in memory. It filters work that is
// already pending, confirmed, or in backoff before applying the per-pass cap,
// then claims the selected retries before issuing any Thornode RPCs.
func (s *AttestationGossip) reconcilePendingObservedXMRSpentRefs(ctx context.Context) {
	type candidate struct {
		key   txKey
		state *AttestationState[*attestableObservedTx]
		dueAt time.Time
	}

	now := time.Now()
	initialRetryDelay := s.xmrSpentRefsInitialRetryDelay()
	retryDelayCap := s.xmrSpentRefsRetryDelayCap()

	s.mu.Lock()
	due := make([]candidate, 0)
	for key, state := range s.observedTxs {
		state.mu.Lock()
		if state.Item != nil && state.Item.ObservedTx != nil && !state.spentRefsApplied && !key.Inbound &&
			state.Item.ObservedTx.Tx.Chain.Equals(common.XMRChain) && len(state.Item.ObservedTx.SpentOutputRefs) > 0 {
			blocked := s.hasPendingOrConfirmedXMRSpentRefsWithContext(ctx, *state.Item.ObservedTx)
			dueNow := state.spentRefsNextRetryAt.IsZero() || !now.Before(state.spentRefsNextRetryAt)
			if !blocked && dueNow && state.AttestationCount() > 0 {
				dueAt := state.spentRefsNextRetryAt
				if dueAt.IsZero() {
					dueAt = state.firstAttestationObserved
				}
				due = append(due, candidate{key: key, state: state, dueAt: dueAt})
			}
		}
		state.mu.Unlock()
	}

	// Oldest-due ordering prevents a large collection of newly admitted states
	// from starving a retry that has already waited through its backoff.
	sort.Slice(due, func(i, j int) bool { return due[i].dueAt.Before(due[j].dueAt) })
	if len(due) > maxPendingObservedXMRSpentRefsPerReconcile {
		due = due[:maxPendingObservedXMRSpentRefsPerReconcile]
	}

	pending := make([]common.AttestTx, 0, len(due))
	for _, retry := range due {
		state := retry.state
		state.mu.Lock()
		attestations := state.VerifiedAttestationsCopy()
		if len(attestations) > 0 {
			delay := state.spentRefsRetryDelay
			if delay <= 0 {
				delay = initialRetryDelay
			}
			state.spentRefsNextRetryAt = now.Add(delay)
			if delay >= retryDelayCap/2 {
				state.spentRefsRetryDelay = retryDelayCap
			} else {
				state.spentRefsRetryDelay = delay * 2
			}
			pending = append(pending, common.AttestTx{
				ObsTx:                  *state.Item.ObservedTx,
				Inbound:                false,
				AllowFutureObservation: retry.key.AllowFutureObservation,
				Attestation:            attestations[0],
			})
		}
		state.mu.Unlock()
	}
	s.mu.Unlock()

	for _, tx := range pending {
		if ctx.Err() != nil {
			return
		}
		s.handleObservedTxAttestation(ctx, tx)
	}
}

func (s *AttestationGossip) mergeHistoricalXMRCertificateAttestationsWithContext(ctx context.Context, payloadHash [sha256.Size]byte, attestations []*common.Attestation) []*common.Attestation {
	_, historical, _, _ := s.historicalXMRSpentRefCertificateSnapshotWithContext(ctx, payloadHash)
	return append(attestations, historical...)
}

// retryPendingXMRCertificates runs independently of scanner progress. A full
// outbox can backpressure the scanner without preventing completion and drain.
func (o *Observer) retryPendingXMRCertificates(ctx context.Context) {
	o.lastNodeStatusMu.RLock()
	status := o.lastNodeStatus
	o.lastNodeStatusMu.RUnlock()
	if status == thortypes.NodeStatus_Unknown || o.storage == nil {
		return
	}
	now := time.Now()
	records, stats, err := o.storage.pendingXMRCertificates(now, maxPendingObservedXMRSpentRefsPerReconcile)
	if err != nil {
		o.logger.Error().Err(err).Msg("load pending xmr certificates")
		return
	}
	if o.m != nil {
		o.m.GetGauge(metrics.XMRPendingCertificates).Set(float64(stats.count))
		o.m.GetGauge(metrics.XMROldestPendingCertificateAge).Set(stats.oldestAgeSeconds(now))
	}
	s := o.attestationGossip
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		var tx common.AttestTx
		if err = tx.Unmarshal(record.Payload); err != nil {
			o.logger.Error().Err(err).Msg("decode pending xmr certificate")
			continue
		}
		// Reserve this retry before network I/O. Each record keeps its own backoff,
		// so one unavailable payout cannot monopolize the next bounded pass.
		if err = o.storage.finishPendingXMRCertificate(record, false, time.Now()); err != nil {
			o.logger.Error().Err(err).Msg("persist xmr certificate retry schedule")
			continue
		}
		voter, found, queryErr := o.thorchainBridge.GetObservedTxOutVoterUncached(ctx, tx.ObsTx.Tx.ID.String())
		if queryErr == nil && found && pendingXMRCertificateComplete(tx.ObsTx, voter) {
			// Completion applies to both phases of this exact source observation.
			if err = o.storage.completePendingXMRCertificate(tx); err != nil {
				o.logger.Error().Err(err).Msg("complete pending xmr certificate")
			}
			continue
		}
		if !tx.ObsTx.IsFinal() {
			chain, chainErr := o.getChain(common.XMRChain)
			if chainErr != nil {
				continue
			}
			height, heightErr := chain.GetHeight()
			if heightErr == nil && height >= tx.ObsTx.FinaliseHeight {
				tx.ObsTx.BlockHeight = tx.ObsTx.FinaliseHeight
				payload, payloadErr := tx.ObsTx.GetSignablePayloadWithInbound(false)
				if payloadErr != nil {
					continue
				}
				signature, signErr := s.privKey.Sign(payload)
				if signErr != nil {
					continue
				}
				tx.Attestation = &common.Attestation{PubKey: s.pubKey, Signature: signature}
				// Persist the exact final payload before it can leave this process.
				if err = o.storage.savePendingXMRCertificate(tx); err != nil {
					continue
				}
			}
		}
		if s.isActiveValidator(s.host.ID()) {
			// A returning validator contributes an ordinary vote; its historical
			// certificate transport is intentionally rejected by active peers.
			s.handleObservedTxAttestation(ctx, tx)
			s.batcher.AddObservedTx(tx)
		} else {
			// Local application is helpful but never a completion condition. Continue
			// broadcasting even if the in-memory import is applied or at capacity.
			if acceptErr := s.acceptHistoricalXMRSpentRefCertificate(tx, ""); acceptErr != nil {
				o.logger.Debug().Err(acceptErr).Str("tx_id", tx.ObsTx.Tx.ID.String()).Msg("defer local xmr certificate import")
			}
			s.broadcastHistoricalXMRSpentRefCertificate(ctx, tx)
		}
	}
}

func pendingXMRCertificateComplete(obs common.ObservedTx, voter *thortypes.QueryObservedTxVoter) bool {
	if voter == nil || !voter.GetTxID().Equals(obs.Tx.ID) {
		return false
	}
	if voter.Reverted {
		return true
	}
	if voter.GetFinalisedHeight() <= 0 {
		return false
	}
	tx := voter.GetTx()
	return tx.GetBlockHeight() > 0 && tx.GetBlockHeight() == tx.GetFinaliseHeight() &&
		tx.GetTx().EqualsEx(obs.Tx) && tx.GetObservedPubKey().Equals(obs.ObservedPubKey) &&
		obs.HasEquivalentXMRTxKey(common.ObservedTx{Tx: tx.GetTx(), XmrTxKey: tx.GetXmrTxKey()}) &&
		common.SpentOutputRefsEqual(common.OutputRefsFromPointers(tx.GetSpentOutputRefs()), obs.SpentOutputRefs)
}
