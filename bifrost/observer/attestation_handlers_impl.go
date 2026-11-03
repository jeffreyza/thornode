package observer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Handlers report evidence retention separately from its later delivery.
// A zero outcome is a definitive rejection; err means admission was unable
// to finish. Successful retention, including a duplicate, sets accepted.
type attestationImportOutcome struct {
	accepted bool
	err      error
}

const priceFeedsUpdateDelay = time.Millisecond * 100

// handleObservedTxAttestation processes attestations for observed transactions
func (s *AttestationGossip) handleObservedTxAttestation(ctx context.Context, tx common.AttestTx) {
	s.handleObservedTxAttestationInternal(ctx, tx, false)
}

func (s *AttestationGossip) handleObservedTxAttestationFromStateSync(ctx context.Context, tx common.AttestTx) attestationImportOutcome {
	return s.handleObservedTxAttestationWithStateSyncCancellation(ctx, tx, false, true)
}

func (s *AttestationGossip) handleObservedTxAttestationInternal(ctx context.Context, tx common.AttestTx, queue bool) {
	s.handleObservedTxAttestationWithStateSyncCancellation(ctx, tx, queue, false)
}

func (s *AttestationGossip) handleObservedTxAttestationWithStateSyncCancellation(ctx context.Context, tx common.AttestTx, queue, stateSync bool) (result attestationImportOutcome) {
	obsTx := tx.ObsTx
	// Keep transfer-scoped recovery separate from the shorter delivery budget.
	stateSyncCtx := attestationLockContext(ctx, stateSync)
	barrierHeld := obsTx.Tx.Chain.Equals(common.XMRChain)
	if barrierHeld {
		if err := readLockAttestationContext(stateSyncCtx, &s.xmrErrataBarrier); err != nil {
			return attestationImportOutcome{err: err}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, xmrAttestationTimeout(s.config.PeerTimeout))
		defer cancel()
		defer func() {
			if barrierHeld {
				s.xmrErrataBarrier.RUnlock()
			}
		}()
	}

	k := observedTxKey(obsTx, tx.Inbound, tx.AllowFutureObservation)

	if err := lockAttestationContext(stateSyncCtx, &s.mu); err != nil {
		return attestationImportOutcome{err: err}
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return attestationImportOutcome{err: err}
	}
	if barrierHeld {
		admitted, err := s.admitXMRObservationLocked(obsTx)
		if err != nil || !admitted {
			s.mu.Unlock()
			return attestationImportOutcome{err: err}
		}
	}
	state, ok := s.observedTxs[k]
	if !ok {
		state = s.observedTxsPool.NewAttestationState(&attestableObservedTx{
			ObservedTx: &obsTx,
			inbound:    tx.Inbound,
		})
		s.observedTxs[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	if err := lockAttestationContext(stateSyncCtx, &state.mu); err != nil {
		s.mu.Unlock()
		return attestationImportOutcome{err: err}
	}
	s.mu.Unlock()

	// Bucket identity omits signed fields. An import must match the retained
	// payload before a valid stored signature can count as a duplicate.
	if stateSync && ok && !sameAttestationPayload(state.Item, &attestableObservedTx{ObservedTx: &obsTx, inbound: tx.Inbound}) {
		state.mu.Unlock()
		s.logger.Debug().Msg("reject state-sync observation with conflicting signed payload")
		return
	}

	// Add the attestation
	if err := state.AddAttestation(tx.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		state.mu.Unlock()
		if barrierHeld {
			s.xmrErrataBarrier.RUnlock()
			barrierHeld = false
		}
		if queue {
			s.enqueueObservedTxAttestation(tx)
		}
		return
	}
	result.accepted = true
	// This is the payload every attestation in state was verified against. The
	// incoming envelope can share txKey with a payload that differs in signed
	// fields omitted from that index, so never pair state signatures with obsTx
	// below.
	attestedObsTx := *state.Item.ObservedTx

	// Candidate XMR spent-ref imports are authorized after releasing the state
	// lock. The authorization path may query thornode and the eventual import
	// calls the sidecar, neither of which should run while this mutex is held.
	var maybeApplySpentRefs *common.ObservedTx
	var maybeApplySpentRefsAttestations []*common.Attestation
	var maybeApplySpentRefsPayloadHash [sha256.Size]byte
	var maybeApplySpentRefsPayloadBound bool
	isXMROutboundWithRefs := attestedObsTx.Tx.Chain.Equals(common.XMRChain) && !tx.Inbound &&
		len(attestedObsTx.SpentOutputRefs) > 0 && len(attestedObsTx.SpentOutputRefs) <= common.MaxXMRSpentOutputRefs
	if isXMROutboundWithRefs && !state.spentRefsApplied {
		// Do not set spentRefsApplied here. A peer attestation is only a
		// candidate until it passes the XMR-specific authority checks below; an
		// unauthorized peer must not suppress a later authorized import.
		// Bind the import candidate to the state item whose payload every stored
		// signature was verified against. txKey intentionally omits a few
		// ObservedTx fields, so using the latest envelope here could otherwise
		// pair signatures for the first payload with mutated envelope fields.
		cp := attestedObsTx
		if payload, payloadErr := state.Item.GetSignablePayload(); payloadErr == nil {
			maybeApplySpentRefs = &cp
			maybeApplySpentRefsPayloadHash = sha256.Sum256(payload)
			maybeApplySpentRefsPayloadBound = true
		} else {
			s.logger.Error().Err(payloadErr).Msg("skip xmr spent refs: fail to bind attestation payload")
		}
		// Every entry in this state signed the same exact observation/ref set.
		// Copy while holding the state lock so the authorization step can count
		// a stable source-vault certificate after the lock is released.
		maybeApplySpentRefsAttestations = state.VerifiedAttestationsCopy()
	}

	// Observation quorum is independent of the keysign committee. Even future
	// outbound observations must match thornode's active-validator quorum check.
	attestationCount := state.AttestationCount()
	total := s.activeValidatorCount()

	hasSuperMajority := types.HasSuperMajority(attestationCount, total)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", attestationCount, total)

		s.sendObservedTxAttestationsToThornode(ctx, attestedObsTx, state, k.Inbound, k.AllowFutureObservation, true)
	} else {
		s.logger.Debug().Msgf("observed tx attestation received - %s, id: %s, inbound: %t, final: %t, quorum: %d/%d",
			k.Chain, k.ID, k.Inbound, k.Finalized, attestationCount, total)
	}
	state.mu.Unlock()
	if barrierHeld {
		s.xmrErrataBarrier.RUnlock()
		barrierHeld = false
	}
	if queue {
		s.enqueueObservedTxAttestation(tx)
	}
	// Spent-reference authorization/import uses separate recovery checks and
	// must not extend this observation delivery critical section.
	if maybeApplySpentRefsPayloadBound {
		maybeApplySpentRefsAttestations = s.mergeHistoricalXMRCertificateAttestationsWithContext(
			stateSyncCtx, maybeApplySpentRefsPayloadHash,
			maybeApplySpentRefsAttestations,
		)
	}

	var applySpentRefs *spentOutputRefsImport
	var onApplySpentRefsOutcome func(applied bool)
	if maybeApplySpentRefs != nil && maybeApplySpentRefsPayloadBound && s.observerApplySpentOutputRefs != nil {
		var wireMemo string
		var authorized bool
		if stateSync {
			wireMemo, authorized = s.shouldApplyXMRSpentRefsWithContext(stateSyncCtx, *maybeApplySpentRefs, maybeApplySpentRefsAttestations)
		} else {
			wireMemo, authorized = s.shouldApplyXMRSpentRefsFromAttestations(*maybeApplySpentRefs, maybeApplySpentRefsAttestations)
		}
		if !authorized {
			return
		}
		conflictKey := xmrSpentRefsConflictKeyFor(*maybeApplySpentRefs)
		identity := xmrSpentRefsIdentity(maybeApplySpentRefs.SpentOutputRefs)
		launched := false
		defer func() {
			if !launched {
				s.releaseAuthorizedXMRSpentRefs(conflictKey, identity)
			}
		}()
		if lockAttestationContext(stateSyncCtx, &s.mu) != nil {
			return
		}
		liveState, ok := s.observedTxs[k]
		if ok {
			// Re-acquire locks in the normal global-then-state order. The
			// authorization path above can block on thornode, so the originally
			// captured state may have been pruned and returned to the pool.
			if lockAttestationContext(stateSyncCtx, &liveState.mu) != nil {
				s.mu.Unlock()
				return
			}
		}
		s.mu.Unlock()

		if ok {
			livePayloadMatches := false
			if liveState.Item != nil {
				if livePayload, payloadErr := liveState.Item.GetSignablePayload(); payloadErr == nil {
					livePayloadMatches = sha256.Sum256(livePayload) == maybeApplySpentRefsPayloadHash
				}
			}
			if livePayloadMatches && !liveState.spentRefsApplied {
				// Only the currently indexed state bucket may be marked applied,
				// and only after the sidecar confirms import success. Retryable or
				// terminal failures must leave the opportunity open for a later
				// authorized peer attestation, and must release the conflict-map
				// entry so a differing authorized set is not pinned out.
				applySpentRefs = &spentOutputRefsImport{
					Tx:       *maybeApplySpentRefs,
					WireMemo: wireMemo,
				}
				onApplySpentRefsOutcome = func(applied bool) {
					if applied {
						s.markXMRSpentRefsApplied(k, maybeApplySpentRefsPayloadHash)
						s.markHistoricalXMRSpentRefCertificateApplied(maybeApplySpentRefsPayloadHash)
					}
					s.resolveAuthorizedXMRSpentRefs(conflictKey, identity, applied)
				}
			}
			liveState.mu.Unlock()
		}
		if applySpentRefs == nil {
			// Authorization reserves the exact conflict identity. If the captured
			// state was pruned/replaced before admission completed, release that
			// reservation so a later exact certificate can retry.
			return
		}
		if stateSyncCtx.Err() != nil {
			return
		}
		// The existing async importer owns retries and the outcome after launch.
		launched = true
		s.applySpentRefsAsync(*applySpentRefs, onApplySpentRefsOutcome)
	}
	return
}

// Local delivery must not depend on merging into a bucket whose key omits
// signed fields such as memo and gas. Verify the outgoing envelope itself even
// if AddAttestation accepted a duplicate signature for a different envelope.
// Call after releasing the state lock and XMR reader: AddObservedTx rechecks
// durable admission under the queue mutex, so concurrent cleanup still wins.
func (s *AttestationGossip) enqueueObservedTxAttestation(tx common.AttestTx) {
	if s.batcher == nil || tx.Attestation == nil {
		return
	}
	payload, err := tx.ObsTx.GetSignablePayloadWithInbound(tx.Inbound)
	if err != nil {
		s.logger.Error().Err(err).Msg("skip local observation gossip: fail to marshal payload")
		return
	}
	if err = verifySignature(payload, tx.Attestation.Signature, tx.Attestation.PubKey); err != nil {
		s.logger.Error().Err(err).Msg("skip local observation gossip: invalid signature")
		return
	}
	s.batcher.AddObservedTx(tx)
}

// markXMRSpentRefsApplied flips the latch only on the live state bucket after
// the sidecar confirms import success. This avoids marking a pruned or reused
// AttestationState captured before the async import completed.
func (s *AttestationGossip) markXMRSpentRefsApplied(k txKey, expectedPayloadHash [sha256.Size]byte) {
	s.mu.Lock()
	liveState, ok := s.observedTxs[k]
	if ok {
		liveState.mu.Lock()
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	if liveState.Item == nil {
		liveState.mu.Unlock()
		return
	}
	livePayload, err := liveState.Item.GetSignablePayload()
	if err != nil || sha256.Sum256(livePayload) != expectedPayloadHash {
		liveState.mu.Unlock()
		return
	}
	liveState.spentRefsApplied = true
	liveState.mu.Unlock()
}

// admitXMRObservationLocked is shared by local, peer, and state-transfer
// admission and export filtering. The caller holds mu to read observerStorage.
func (s *AttestationGossip) admitXMRObservationLocked(tx common.ObservedTx) (bool, error) {
	admitted, err := s.observerStorage.admitXMRObservation(tx)
	if err != nil {
		s.logger.Error().Err(err).Msg("reject xmr observation: errata storage read failed")
		return false, err
	}
	return admitted, nil
}

// Cap XMR admission, quorum submission and live batch context budgets.
// A peer batch shares one context budget across its attempts. Shared mutex
// acquisition and mux Close/Reset are not strictly bounded by this duration,
// so it does not guarantee an end-to-end scanner acknowledgement deadline.
func xmrAttestationTimeout(configured time.Duration) time.Duration {
	if configured <= 0 || configured > constants.ThorchainBlockTime {
		return constants.ThorchainBlockTime
	}
	return configured
}

// Observation, network-fee, solvency and errata quorum calls hold a state mutex.
// Give legacy calls deadlines too, since periodic reconciliation holds the
// shared map mutex across those calls. Price-feed batch calls are unchanged.
// Several sequential RPC budgets can still prolong a shared-mutex wait.
func (s *AttestationGossip) thornodeSubmissionContext(ctx context.Context, chain common.Chain) (context.Context, context.CancelFunc) {
	timeout := s.config.PeerTimeout
	if chain.Equals(common.XMRChain) {
		timeout = xmrAttestationTimeout(timeout)
	} else if timeout <= 0 {
		timeout = defaultPeerTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// sendObservedTxAttestationsToThornode sends attestations to thornode via gRPC
func (s *AttestationGossip) sendObservedTxAttestationsToThornode(
	ctx context.Context,
	tx common.ObservedTx,
	state *AttestationState[*attestableObservedTx],
	inbound, allowFutureObservation, isQuorum bool,
) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent observed tx attestations")
		return
	}
	// Send via gRPC to thornode
	rpcCtx, cancel := s.thornodeSubmissionContext(ctx, tx.Tx.Chain)
	defer cancel()
	if _, err := s.grpcClient.SendQuorumTx(rpcCtx, &common.QuorumTx{
		ObsTx:                  tx,
		Attestations:           unsent,
		Inbound:                inbound,
		AllowFutureObservation: allowFutureObservation,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum tx")
		return
	}

	s.logger.Info().Msgf("sent quorum tx to thornode - %s, id: %s, inbound: %t, final: %t, attestations: %s",
		tx.Tx.Chain, tx.Tx.ID, inbound, tx.IsFinal(), state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handleNetworkFeeAttestation processes attestations for network fees
func (s *AttestationGossip) handleNetworkFeeAttestation(ctx context.Context, anf common.AttestNetworkFee) {
	s.handleNetworkFeeAttestationWithStateSyncCancellation(ctx, anf, false)
}

func (s *AttestationGossip) handleNetworkFeeAttestationWithStateSyncCancellation(ctx context.Context, anf common.AttestNetworkFee, stateSync bool) (result attestationImportOutcome) {
	lockCtx := attestationLockContext(ctx, stateSync)
	// State-sync completion counts only admissible evidence. Keep these checks
	// local to import admission; the later S14 change also guards live gossip.
	if stateSync {
		if anf.NetworkFee == nil {
			return
		}
		if err := anf.NetworkFee.Valid(); err != nil {
			return
		}
		if anf.NetworkFee.TransactionRate > uint64(math.MaxInt64) || anf.NetworkFee.TransactionSize > uint64(math.MaxInt64) {
			return
		}
	}
	// Use the network fee as the map key
	k := *anf.NetworkFee

	if err := lockAttestationContext(lockCtx, &s.mu); err != nil {
		return attestationImportOutcome{err: err}
	}
	state, ok := s.networkFees[k]
	if !ok {
		// Create a new attestation state
		state = s.networkFeesPool.NewAttestationState(anf.NetworkFee)
		s.networkFees[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	if err := lockAttestationContext(lockCtx, &state.mu); err != nil {
		s.mu.Unlock()
		return attestationImportOutcome{err: err}
	}
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Add the attestation
	if err := state.AddAttestation(anf.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}
	result.accepted = true

	// Get the active validator count
	activeValCount := s.activeValidatorCount()
	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), activeValCount)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), activeValCount)
		s.sendNetworkFeeAttestationsToThornode(ctx, *state.Item, state, true)
	} else {
		s.logger.Debug().Msgf("network fee attestation received - %s, height: %d, quorum: %d/%d",
			k.Chain, k.Height, state.AttestationCount(), activeValCount)
	}
	return
}

// sendNetworkFeeAttestationsToThornode sends network fee attestations to thornode via gRPC
func (s *AttestationGossip) sendNetworkFeeAttestationsToThornode(ctx context.Context, networkFee common.NetworkFee, state *AttestationState[*common.NetworkFee], isQuorum bool) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent network fee attestations")
		return
	}
	// Send via gRPC to thornode
	rpcCtx, cancel := s.thornodeSubmissionContext(ctx, networkFee.Chain)
	defer cancel()
	if _, err := s.grpcClient.SendQuorumNetworkFee(rpcCtx, &common.QuorumNetworkFee{
		NetworkFee:   &networkFee,
		Attestations: unsent,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum network fee")
		return
	}

	s.logger.Info().Msgf("sent quorum network fee to thornode - %s, height: %d, attestations: %s",
		networkFee.Chain, networkFee.Height, state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handleSolvencyAttestation processes attestations for solvency proofs
func (s *AttestationGossip) handleSolvencyAttestation(ctx context.Context, ats common.AttestSolvency) {
	s.handleSolvencyAttestationWithStateSyncCancellation(ctx, ats, false)
}

func (s *AttestationGossip) handleSolvencyAttestationWithStateSyncCancellation(ctx context.Context, ats common.AttestSolvency, stateSync bool) (result attestationImportOutcome) {
	lockCtx := attestationLockContext(ctx, stateSync)
	// Calculate the hash for the solvency to use as key
	k, err := ats.Solvency.Hash()
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to hash solvency")
		return
	}

	if err := lockAttestationContext(lockCtx, &s.mu); err != nil {
		return attestationImportOutcome{err: err}
	}
	state, ok := s.solvencies[k]
	if !ok {
		// Create a new attestation state
		state = s.solvenciesPool.NewAttestationState(ats.Solvency)
		s.solvencies[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	if err := lockAttestationContext(lockCtx, &state.mu); err != nil {
		s.mu.Unlock()
		return attestationImportOutcome{err: err}
	}
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Solvency's bucket hash excludes its signed Id field.
	if stateSync && ok && !sameAttestationPayload(state.Item, ats.Solvency) {
		s.logger.Debug().Msg("reject state-sync solvency with conflicting signed payload")
		return
	}

	// Add the attestation
	wasEmpty := state.AttestationCount() == 0
	if err := state.AddAttestation(ats.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}
	result.accepted = true
	// Register only the first accepted attestation, including when an earlier
	// rejected signature left an empty state. Duplicates do not add a payload.
	if wasEmpty && shouldTrackSolvencyMetrics(state.Item) {
		s.solvencyMetrics.observePayload(state.Item)
	}

	// Get the active validator count
	activeValCount := s.activeValidatorCount()
	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), activeValCount)
	if shouldTrackSolvencyMetrics(state.Item) {
		s.solvencyMetrics.observeAttestations(
			state.Item,
			state.AttestationCount(),
			requiredSolvencyAttestations(activeValCount),
			hasSuperMajority,
		)
	}

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), activeValCount)
		s.sendSolvencyAttestationsToThornode(ctx, *state.Item, state, true)
	} else {
		s.logger.Debug().Msgf("solvency attestation received - %s, height: %d, quorum: %d/%d",
			ats.Solvency.Chain, ats.Solvency.Height, state.AttestationCount(), activeValCount)
	}
	return
}

// sendSolvencyAttestationsToThornode sends solvency attestations to thornode via gRPC
func (s *AttestationGossip) sendSolvencyAttestationsToThornode(ctx context.Context, solvency common.Solvency, state *AttestationState[*common.Solvency], isQuorum bool) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent solvency attestations")
		return
	}
	// Send via gRPC to thornode
	rpcCtx, cancel := s.thornodeSubmissionContext(ctx, solvency.Chain)
	defer cancel()
	if _, err := s.grpcClient.SendQuorumSolvency(rpcCtx, &common.QuorumSolvency{
		Solvency:     &solvency,
		Attestations: unsent,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum solvency")
		return
	}

	s.logger.Info().Msgf("sent quorum solvency to thornode - %s, height: %d, coins: %s, pubkey: %s, attestations: %s",
		solvency.Chain, solvency.Height, solvency.Coins.String(), solvency.PubKey.String(), state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handleErrataAttestation processes attestations for errata transactions
func (s *AttestationGossip) handleErrataAttestation(ctx context.Context, aet common.AttestErrataTx) {
	s.handleErrataAttestationWithRetry(ctx, aet, false, false)
}

// handlePendingErrataAttestation is the local durable-outbox path. It alone
// may make an identical, previously sent errata vote sendable again; peer
// duplicates and every other attestation type retain their existing behavior.
func (s *AttestationGossip) handlePendingErrataAttestation(ctx context.Context, aet common.AttestErrataTx) {
	s.handleErrataAttestationWithRetry(ctx, aet, true, false)
}

func (s *AttestationGossip) handleErrataAttestationWithRetry(
	ctx context.Context,
	aet common.AttestErrataTx,
	retryPending, stateSync bool,
) (result attestationImportOutcome) {
	lockCtx := attestationLockContext(ctx, stateSync)
	// Use the errata tx as the map key
	k := *aet.ErrataTx

	if err := lockAttestationContext(lockCtx, &s.mu); err != nil {
		return attestationImportOutcome{err: err}
	}
	state, ok := s.errataTxs[k]
	if !ok {
		// Create a new attestation state
		state = s.errataTxsPool.NewAttestationState(aet.ErrataTx)
		s.errataTxs[k] = state
	}

	// note, purposefully locking the single state mutex prior to releasing the global mutex to avoid race in between.
	// This order must be maintained everywhere to avoid deadlock.
	if err := lockAttestationContext(lockCtx, &state.mu); err != nil {
		s.mu.Unlock()
		return attestationImportOutcome{err: err}
	}
	s.mu.Unlock()

	defer state.mu.Unlock()

	// Add the attestation
	if err := state.AddAttestation(aet.Attestation); err != nil {
		s.logger.Error().Err(err).Msg("fail to add attestation")
		return
	}
	result.accepted = true
	if retryPending && rearmPendingErrataAttestations(state, s.config.MinTimeBetweenAttestations) {
		s.logger.Debug().
			Str("chain", k.Chain.String()).
			Str("txid", k.Id.String()).
			Msg("re-arming uncommitted errata attestations for durable retry")
	}

	// Get the active validator count
	activeValCount := s.activeValidatorCount()
	hasSuperMajority := types.HasSuperMajority(state.AttestationCount(), activeValCount)

	// If we have a supermajority, send to thornode
	if hasSuperMajority {
		s.logger.Debug().Msgf("has supermajority: %d/%d", state.AttestationCount(), activeValCount)
		s.sendErrataAttestationsToThornode(ctx, *state.Item, state, true)
	} else {
		s.logger.Debug().Msgf("errata attestation received - %s, id: %s, quorum: %d/%d",
			k.Chain, k.Id, state.AttestationCount(), activeValCount)
	}
	return
}

// rearmPendingErrataAttestations runs with state.mu held. A successful gRPC
// submission is not a terminal receipt: if no committed-quorum event arrives,
// the durable Observer outbox must be able to submit the vote again. Committed
// attestations are never re-armed.
func rearmPendingErrataAttestations(
	state *AttestationState[*common.ErrataTx],
	minTimeBetweenAttestations time.Duration,
) bool {
	if state.lastAttestationsSent.IsZero() ||
		time.Since(state.lastAttestationsSent) < minTimeBetweenAttestations {
		return false
	}

	rearmed := false
	for i := range state.attestations {
		if state.attestations[i].sent && !state.attestations[i].committed {
			state.attestations[i].sent = false
			rearmed = true
		}
	}
	return rearmed
}

// sendErrataAttestationsToThornode sends errata attestations to thornode via gRPC
func (s *AttestationGossip) sendErrataAttestationsToThornode(ctx context.Context, errata common.ErrataTx, state *AttestationState[*common.ErrataTx], isQuorum bool) {
	unsent := state.UnsentAttestations()
	if len(unsent) == 0 {
		s.logger.Debug().Msg("no unsent errata attestations")
		return
	}
	// Send via gRPC to thornode
	rpcCtx, cancel := s.thornodeSubmissionContext(ctx, errata.Chain)
	defer cancel()
	if _, err := s.grpcClient.SendQuorumErrataTx(rpcCtx, &common.QuorumErrataTx{
		ErrataTx:     &errata,
		Attestations: unsent,
	}); err != nil {
		s.logger.Error().Err(err).Msg("fail to send quorum errata")
		return
	}

	s.logger.Info().Msgf("sent quorum errata to thornode - %s - ID: %s - attestations: %s", errata.Chain, errata.Id, state.State())

	// Mark attestations as sent
	state.MarkAttestationsSent(isQuorum)
}

// handlePriceFeedAttestation processes attestations for price feeds
func (s *AttestationGossip) handlePriceFeedAttestation(ctx context.Context, apf common.AttestPriceFeed) {
	s.handlePriceFeedAttestationWithSendContext(ctx, ctx, apf, false)
}

func (s *AttestationGossip) handlePriceFeedAttestationWithSendContext(ctx, sendCtx context.Context, apf common.AttestPriceFeed, stateSync bool) (result attestationImportOutcome) {
	lockCtx := attestationLockContext(ctx, stateSync)
	if apf.PriceFeed == nil || apf.Attestation == nil {
		return
	}
	if err := apf.PriceFeed.Valid(); err != nil {
		s.logger.Debug().Err(err).Msg("reject invalid price feed")
		return
	}
	// Use the pubkey as key
	k := hex.EncodeToString(apf.Attestation.PubKey)

	// Get the marshaled data for signature verification
	signBz, err := apf.PriceFeed.GetSignablePayload()
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get signable payload")
		return
	}

	// Verify the signature
	att := apf.Attestation
	err = verifySignature(signBz, att.Signature, att.PubKey)
	if err != nil {
		s.logger.Error().Err(err).Msgf("signature verification failed for %x", att.PubKey)
		return
	}

	// We don't collect multiple attestations for price feeds, rather want
	// to store the most recent price we received for every node
	if err := lockAttestationContext(lockCtx, &s.mu); err != nil {
		return attestationImportOutcome{err: err}
	}
	defer s.mu.Unlock()

	state, ok := s.priceFeeds[k]
	// A verified duplicate already retained by this node is still evidence.
	if ok && len(state.attestations) > 0 {
		result.accepted = bytes.Equal(state.attestations[0].attestation.Signature, att.Signature)
	}
	if !ok || state.Item.Time < apf.PriceFeed.Time {
		// Set new attestation state
		state = s.priceFeedsPool.NewAttestationState(apf.PriceFeed)
		state.attestations = []attestationSentState{
			{attestation: apf.Attestation, sent: false},
		}
		s.priceFeeds[k] = state
		result.accepted = true

		// we received an updated price feed, start delay to maybe collect
		// more updated values before sending them to thornode
		if s.priceFeedsDelay.IsRunning() {
			return
		}

		s.priceFeedsDelay.Start()
		go func() {
			defer s.priceFeedsDelay.Done()
			select {
			case <-sendCtx.Done():
				return
			case <-time.After(priceFeedsUpdateDelay):
				s.sendPriceFeedAttestationsToThornode(sendCtx)
			}
		}()
	}
	return
}

// sendPriceFeedAttestationsToThornode sends price feed attestations
// to thornode via gRPC
//
// To avoid calling the endpoint hundred times per update, all
// (quorum) price feeds are batched into a single request
func (s *AttestationGossip) sendPriceFeedAttestationsToThornode(
	ctx context.Context,
) {
	qpfb := common.QuorumPriceFeedBatch{
		QuorumPriceFeeds: []*common.QuorumPriceFeed{},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range s.priceFeeds {
		state.mu.Lock()
		defer state.mu.Unlock()

		qpfb.QuorumPriceFeeds = append(qpfb.QuorumPriceFeeds, &common.QuorumPriceFeed{
			PriceFeed:    state.Item,
			Attestations: []*common.Attestation{state.attestations[0].attestation},
		})
	}

	if len(qpfb.QuorumPriceFeeds) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	_, err := s.grpcClient.SendQuorumPriceFeedBatch(ctx, &qpfb)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to send price feed")
		return
	}

	// delete price feeds
	for k := range s.priceFeeds {
		delete(s.priceFeeds, k)
	}
}
