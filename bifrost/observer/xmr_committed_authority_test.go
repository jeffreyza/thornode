package observer

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

func TestXMRCommittedAuthorityVerifiedCopy(t *testing.T) {
	observed, _ := observerTestXMRPlan(t)
	state := NewAttestationStatePool[*attestableObservedTx]().NewAttestationState(
		&attestableObservedTx{ObservedTx: &observed},
	)
	var votes []*common.Attestation
	for range 3 {
		vote := observerTestSignedHistoricalXMRCertificate(t, observed, secp256k1.GenPrivKey()).Attestation
		require.NoError(t, state.AddAttestation(vote))
		votes = append(votes, vote)
	}
	state.MarkAttestationsCommitted(votes[:2])
	require.Len(t, state.AttestationsCopy(), 1, "delivery must still exclude committed shares")

	// Keep this regression executable on the parent, where the proof accessor
	// is absent, so its failure is an assertion rather than a compile error.
	proof, ok := any(state).(interface{ VerifiedAttestationsCopy() []*common.Attestation })
	require.True(t, ok, "verified proof must be available independently of delivery bookkeeping")
	verified := proof.VerifiedAttestationsCopy()
	require.Equal(t, votes, verified)
	for i, vote := range verified {
		vote.PubKey[0] ^= 0xff
		vote.Signature[0] ^= 0xff
		assert.NotEqual(t, votes[i].PubKey, vote.PubKey)
		assert.NotEqual(t, votes[i].Signature, vote.Signature)
	}
	require.Equal(t, votes, proof.VerifiedAttestationsCopy(), "proof copies must not alias stored signatures")
	require.Equal(t, votes[2:], state.AttestationsCopy())
}

func TestXMRCommittedAuthorityImmediateImport(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	observed, planned := observerTestXMRPlan(t)
	members, shares := committedAuthorityTestShares(t, observed)
	ag.setActiveValidators(members)
	bridge.getVaultFunc = observerTestVaultFunc(observed.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	applied := make(chan spentOutputRefsImport, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		onOutcome(true)
		applied <- request
	})

	for _, share := range shares[:2] {
		ag.handleObservedTxAttestation(context.Background(), share)
		commitAuthorityTestShares(t, ag, observed, share.Attestation)
	}
	select {
	case <-applied:
		t.Fatal("two of four source-vault members must not authorize import")
	default:
	}
	ag.handleObservedTxAttestation(context.Background(), shares[2])
	select {
	case request := <-applied:
		require.Equal(t, observed, request.Tx)
		require.Equal(t, observed.Tx.Memo, request.WireMemo)
	case <-time.After(time.Second):
		t.Fatal("three source-vault shares must authorize import even when the first two are committed")
	}
	require.True(t, observedXMRPayloadApplied(t, ag, observedTxKey(observed, false, false)))
}

func TestXMRCommittedAuthorityReconciliation(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	observed, planned := observerTestXMRPlan(t)
	members, shares := committedAuthorityTestShares(t, observed)
	ag.setActiveValidators(members)
	bridge.getVaultFunc = observerTestVaultFunc(observed.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	attempts := make(chan spentOutputRefsImport, 3)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		// Simulate transient sidecar failures. The callback releases the failed
		// reservation before the next explicit reconcile tick.
		onOutcome(false)
		attempts <- request
	})
	for _, share := range shares[:3] {
		ag.handleObservedTxAttestation(context.Background(), share)
	}
	select {
	case <-attempts:
	case <-time.After(time.Second):
		t.Fatal("initial source-vault quorum did not attempt import")
	}
	require.Eventually(t, func() bool { return len(ag.applySpentRefsSem) == 0 }, time.Second, time.Millisecond)
	commitAuthorityTestShares(t, ag, observed, shares[0].Attestation, shares[1].Attestation, shares[2].Attestation)
	key := observedTxKey(observed, false, false)
	state := ag.observedTxs[key]
	state.mu.Lock()
	remaining := state.AttestationsCopy()
	state.mu.Unlock()
	require.Empty(t, remaining, "all delivery shares should be committed")

	started := time.Now()
	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	state.mu.Lock()
	nextRetryAt, retryDelay := state.spentRefsNextRetryAt, state.spentRefsRetryDelay
	state.mu.Unlock()
	assert.True(t, nextRetryAt.After(started), "all-committed work must advance its retry schedule")
	assert.Positive(t, retryDelay)
	select {
	case request := <-attempts:
		require.Equal(t, observed, request.Tx)
	case <-time.After(time.Second):
		t.Fatal("all-committed shares must be replayed after a failed import")
	}
	require.Eventually(t, func() bool { return len(ag.applySpentRefsSem) == 0 }, time.Second, time.Millisecond)
	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	select {
	case <-attempts:
		t.Fatal("backoff must prevent the same failed state from immediately taking another slot")
	default:
	}

	// A later due tick succeeds without any new peer share arriving.
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		onOutcome(true)
		attempts <- request
	})
	state.mu.Lock()
	state.spentRefsNextRetryAt = time.Now().Add(-time.Second)
	state.mu.Unlock()
	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	select {
	case request := <-attempts:
		require.Equal(t, observed, request.Tx)
	case <-time.After(time.Second):
		t.Fatal("a later reconcile tick must import using committed proof")
	}
	require.True(t, observedXMRPayloadApplied(t, ag, key))
}

func TestXMRCommittedAuthorityHistoricalCombination(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	observed, planned := observerTestXMRPlan(t)
	members, shares := committedAuthorityTestShares(t, observed)
	ag.setActiveValidators(nil)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(observed.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	applied := make(chan spentOutputRefsImport, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		onOutcome(true)
		applied <- request
	})
	for _, share := range shares[:2] {
		ag.handleObservedTxAttestation(context.Background(), share)
	}
	commitAuthorityTestShares(t, ag, observed, shares[0].Attestation, shares[1].Attestation)
	payload, err := observed.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	payloadHash := sha256.Sum256(payload)
	_, historical, _, _ := ag.historicalXMRSpentRefCertificateSnapshotWithContext(context.Background(), payloadHash)
	require.Empty(t, historical, "ordinary shares are not archived automatically")

	// A former live signer now supplies the same share through the historical
	// route. Its duplicate must not turn two unique members into a quorum.
	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(shares[0], ""))
	combined := ag.mergeHistoricalXMRCertificateAttestationsWithContext(context.Background(), payloadHash, ag.observedXMRPayloadAttestations(observed, payloadHash))
	count, total, quorum := ag.sourceVaultAttestationQuorumWithLoader(observed.ObservedPubKey, combined, bridge.GetVault)
	assert.Equal(t, 2, count)
	require.Equal(t, 4, total)
	require.False(t, quorum)
	select {
	case <-applied:
		t.Fatal("overlapping live and historical shares must count once")
	default:
	}

	// One additional historical member completes the proof using both committed
	// live shares; the historical bucket alone remains below quorum.
	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(shares[2], ""))
	select {
	case request := <-applied:
		require.Equal(t, observed, request.Tx)
	case <-time.After(time.Second):
		t.Fatal("committed live shares and the historical certificate must combine to authorize import")
	}
	_, historical, _, _ = ag.historicalXMRSpentRefCertificateSnapshotWithContext(context.Background(), payloadHash)
	require.Len(t, historical, 2)
	require.True(t, observedXMRPayloadApplied(t, ag, observedTxKey(observed, false, false)))
}

func TestXMRCommittedAuthorityRetention(t *testing.T) {
	const lateTimeout, nonQuorumTimeout = 5 * time.Minute, 10 * time.Hour
	for _, committed := range []bool{false, true} {
		name := "quorum sent"
		if committed {
			name = "all shares committed"
		}
		t.Run(name, func(t *testing.T) {
			observed, _ := observerTestXMRPlan(t)
			state := NewAttestationStatePool[*attestableObservedTx]().NewAttestationState(&attestableObservedTx{ObservedTx: &observed})
			vote := observerTestSignedHistoricalXMRCertificate(t, observed, secp256k1.GenPrivKey()).Attestation
			require.NoError(t, state.AddAttestation(vote))
			state.firstAttestationObserved = time.Now().Add(-6 * time.Minute)
			if committed {
				state.MarkAttestationsCommitted([]*common.Attestation{vote})
				state.lastCommittedAttestation = state.firstAttestationObserved
			} else {
				state.quorumAttestationsSent = state.firstAttestationObserved
			}
			assert.False(t, state.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout), "unapplied XMR proof must survive delivery expiry")
			state.spentRefsApplied = true
			assert.True(t, state.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout), "successful import restores ordinary expiry")
			state.spentRefsApplied = false

			// A refreshed delivery/commit timestamp must not extend proof lifetime
			// beyond NonQuorumTimeout from the first observation.
			state.firstAttestationObserved = time.Now().Add(-nonQuorumTimeout - time.Second)
			state.lastAttestationsSent = time.Now()
			state.quorumAttestationsSent = time.Now()
			state.lastCommittedAttestation = time.Now()
			assert.True(t, state.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout), "pending proof retention must have an absolute bound")
		})
	}

	for _, name := range []string{"non-XMR", "inbound XMR", "XMR without refs"} {
		t.Run(name, func(t *testing.T) {
			observed, _ := observerTestXMRPlan(t)
			item := &attestableObservedTx{ObservedTx: &observed}
			switch name {
			case "non-XMR":
				observed.Tx.Chain = common.BTCChain
			case "inbound XMR":
				item.inbound = true
			case "XMR without refs":
				observed.SpentOutputRefs = nil
			}
			state := NewAttestationStatePool[*attestableObservedTx]().NewAttestationState(item)
			state.quorumAttestationsSent = time.Now().Add(-6 * time.Minute)
			assert.True(t, state.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout))
			state.firstAttestationObserved = time.Now().Add(-nonQuorumTimeout - time.Second)
			state.quorumAttestationsSent = time.Now()
			assert.False(t, state.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout), "other observations must retain their existing expiry rules")
		})
	}
	fee := NewAttestationStatePool[*common.NetworkFee]().NewAttestationState(&common.NetworkFee{Chain: common.XMRChain})
	fee.quorumAttestationsSent = time.Now().Add(-6 * time.Minute)
	assert.True(t, fee.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout))
	fee.firstAttestationObserved = time.Now().Add(-nonQuorumTimeout - time.Second)
	fee.quorumAttestationsSent = time.Now()
	assert.False(t, fee.ExpiredAfterQuorum(lateTimeout, nonQuorumTimeout), "other item types must retain their existing expiry rules")
}

func committedAuthorityTestShares(t *testing.T, observed common.ObservedTx) (common.PubKeys, []common.AttestTx) {
	t.Helper()
	var members common.PubKeys
	var shares []common.AttestTx
	for range 4 {
		signer := secp256k1.GenPrivKey()
		members = append(members, observerTestPubKey(t, signer))
		shares = append(shares, observerTestSignedHistoricalXMRCertificate(t, observed, signer))
	}
	return members, shares
}

func commitAuthorityTestShares(t *testing.T, ag *AttestationGossip, observed common.ObservedTx, shares ...*common.Attestation) {
	t.Helper()
	event := common.QuorumTx{ObsTx: observed, Attestations: shares}
	payload, err := event.Marshal()
	require.NoError(t, err)
	ag.handleQuorumTxCommitted(&ebifrost.EventNotification{Payload: payload})
}
