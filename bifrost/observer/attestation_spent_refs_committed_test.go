package observer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

// handleQuorumTxCommitted forwards only an XMR outbound's vault and tx ID to
// the consensus-backed importer, regardless of whether this node attested.
func TestQuorumTxCommittedAppliesXMRSpentRefs(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	t.Run("applies spent refs for XMR outbound on committed event", func(t *testing.T) {
		type committedHint struct {
			vault common.PubKey
			txID  common.TxID
		}
		var tx common.ObservedTx
		// The callback runs on the import goroutine, so hand the hint back over
		// a channel instead of racing a shared variable and sleep.
		callbackRequests := make(chan committedHint, 1)
		ag.SetObserverApplyCommittedSpentRefs(func(_ context.Context, vault common.PubKey, txID common.TxID) (common.ObservedTx, bool) {
			callbackRequests <- committedHint{vault: vault, txID: txID}
			return tx, true
		})

		refs := []common.OutputRef{
			{TxHash: "spent1", OutputIndex: 0, KeyImage: "ki_spent1", SpendTxHash: "xmr-committed-1"},
			{TxHash: "spent2", OutputIndex: 1, KeyImage: "ki_spent2", SpendTxHash: "xmr-committed-1"},
		}
		tx = common.ObservedTx{
			Tx: common.Tx{
				ID:    "xmr-committed-1",
				Chain: common.XMRChain,
			},
			ObservedPubKey:  "pk-frost",
			SpentOutputRefs: refs,
		}

		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: false,
			Attestations: []*common.Attestation{
				{PubKey: []byte("other-node"), Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		// Pre-populate the observedTxs map so handleQuorumTxCommitted doesn't
		// return early at the "!ok" check. We need a matching txKey.
		k := txKey{
			Chain:     common.XMRChain,
			ID:        "xmr-committed-1",
			Finalized: tx.IsFinal(),
		}
		ag.mu.Lock()
		ag.observedTxs[k] = ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &tx, inbound: false})
		ag.mu.Unlock()

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		var callbackRequest committedHint
		select {
		case callbackRequest = <-callbackRequests:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "spent refs callback should fire for XMR outbound committed event")
		}
		assert.True(t, callbackRequest.vault.Equals(tx.ObservedPubKey))
		assert.True(t, callbackRequest.txID.Equals(tx.Tx.ID))
	})

	t.Run("does not apply spent refs for non-XMR committed event", func(t *testing.T) {
		fired := make(chan struct{}, 1)
		ag.SetObserverApplyCommittedSpentRefs(func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool) {
			fired <- struct{}{}
			return common.ObservedTx{}, true
		})

		tx := common.ObservedTx{
			Tx: common.Tx{
				ID:    "btc-committed-1",
				Chain: common.BTCChain,
			},
			SpentOutputRefs: []common.OutputRef{{TxHash: "x", OutputIndex: 0}},
		}

		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: false,
			Attestations: []*common.Attestation{
				{PubKey: ag.pubKey, Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		k := txKey{
			Chain:     common.BTCChain,
			ID:        "btc-committed-1",
			Finalized: tx.IsFinal(),
		}
		ag.mu.Lock()
		ag.observedTxs[k] = ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &tx, inbound: false})
		ag.mu.Unlock()

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		select {
		case <-fired:
			assert.Fail(t, "spent refs callback should NOT fire for non-XMR chain")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("does not apply spent refs for inbound XMR committed event", func(t *testing.T) {
		fired := make(chan struct{}, 1)
		ag.SetObserverApplyCommittedSpentRefs(func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool) {
			fired <- struct{}{}
			return common.ObservedTx{}, true
		})

		tx := common.ObservedTx{
			Tx: common.Tx{
				ID:    "xmr-inbound-1",
				Chain: common.XMRChain,
			},
			SpentOutputRefs: []common.OutputRef{{TxHash: "x", OutputIndex: 0, KeyImage: "ki_x", SpendTxHash: "xmr-inbound-1"}},
		}

		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: true,
			Attestations: []*common.Attestation{
				{PubKey: ag.pubKey, Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		k := txKey{
			Chain:     common.XMRChain,
			ID:        "xmr-inbound-1",
			Inbound:   true,
			Finalized: tx.IsFinal(),
		}
		ag.mu.Lock()
		ag.observedTxs[k] = ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &tx, inbound: true})
		ag.mu.Unlock()

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		select {
		case <-fired:
			assert.Fail(t, "spent refs callback should NOT fire for inbound XMR")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("event refs are not trusted as an admission condition", func(t *testing.T) {
		called := make(chan struct{}, 1)
		ag.SetObserverApplyCommittedSpentRefs(func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool) {
			called <- struct{}{}
			return common.ObservedTx{}, false
		})

		tx := common.ObservedTx{
			Tx: common.Tx{
				ID:    "xmr-norefs-1",
				Chain: common.XMRChain,
			},
			ObservedPubKey: "pk-frost",
		}

		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: false,
			Attestations: []*common.Attestation{
				{PubKey: ag.pubKey, Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		k := txKey{
			Chain:     common.XMRChain,
			ID:        "xmr-norefs-1",
			Finalized: tx.IsFinal(),
		}
		ag.mu.Lock()
		ag.observedTxs[k] = ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &tx, inbound: false})
		ag.mu.Unlock()

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("committed hint should not trust event-supplied spent refs")
		}
	})
}

func TestQuorumTxCommittedBookkeepingUsesConsensusImport(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	txID := common.TxID("xmr-committed-consensus")
	vault := common.PubKey("pk-frost")
	eventTx := common.ObservedTx{
		Tx: common.Tx{
			ID:        txID,
			Chain:     common.XMRChain,
			ToAddress: "malicious-pending-destination",
			Memo:      "OUT:malicious-pending-outbound",
		},
		ObservedPubKey: vault,
		SpentOutputRefs: []common.OutputRef{{
			TxHash: "event-input", OutputIndex: 0, KeyImage: "event-key-image", SpendTxHash: txID.String(),
		}},
	}
	consensusTx := eventTx
	consensusTx.Tx.ToAddress = "consensus-destination"
	consensusTx.Tx.Memo = "MIGRATE:100"
	consensusTx.SpentOutputRefs = []common.OutputRef{{
		TxHash: "consensus-input", OutputIndex: 1, KeyImage: "consensus-key-image", SpendTxHash: txID.String(),
	}}

	eventState := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &eventTx, inbound: false})
	consensusState := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &consensusTx, inbound: false})
	ag.mu.Lock()
	ag.observedTxs[observedTxKey(eventTx, false, false)] = eventState
	ag.observedTxs[observedTxKey(consensusTx, false, false)] = consensusState
	ag.mu.Unlock()

	type committedHint struct {
		vault common.PubKey
		txID  common.TxID
	}
	hints := make(chan committedHint, 1)
	ag.SetObserverApplyCommittedSpentRefs(func(_ context.Context, gotVault common.PubKey, gotTxID common.TxID) (common.ObservedTx, bool) {
		hints <- committedHint{vault: gotVault, txID: gotTxID}
		return consensusTx, true
	})
	qtx := common.QuorumTx{ObsTx: eventTx, Inbound: false}
	payload, err := qtx.Marshal()
	require.NoError(t, err)
	ag.handleQuorumTxCommitted(&ebifrost.EventNotification{Payload: payload})
	select {
	case hint := <-hints:
		assert.True(t, hint.vault.Equals(vault))
		assert.True(t, hint.txID.Equals(txID))
	case <-time.After(time.Second):
		t.Fatal("committed import callback did not receive the event hint")
	}

	require.Eventually(t, func() bool {
		consensusState.mu.Lock()
		defer consensusState.mu.Unlock()
		return consensusState.spentRefsApplied
	}, time.Second, 10*time.Millisecond)
	eventState.mu.Lock()
	assert.False(t, eventState.spentRefsApplied, "event-supplied metadata must not be latched")
	eventState.mu.Unlock()

	var identity string
	ag.authorizedXMRSpentRefsMu.Lock()
	entry := ag.authorizedXMRSpentRefs[xmrSpentRefsConflictKeyFor(consensusTx)]
	if entry != nil {
		identity = entry.identity
	}
	ag.authorizedXMRSpentRefsMu.Unlock()
	require.NotNil(t, entry)
	assert.Equal(t, xmrSpentRefsIdentity(consensusTx.SpentOutputRefs), identity)
}

func TestQuorumTxCommittedWithoutConsensusDoesNotLatchSpentRefs(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	tx := common.ObservedTx{
		Tx:             common.Tx{ID: "xmr-not-yet-consensus", Chain: common.XMRChain},
		ObservedPubKey: "pk-frost",
		SpentOutputRefs: []common.OutputRef{{
			TxHash: "input", OutputIndex: 0, KeyImage: "key-image", SpendTxHash: "xmr-not-yet-consensus",
		}},
	}
	state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &tx, inbound: false})
	ag.mu.Lock()
	ag.observedTxs[observedTxKey(tx, false, false)] = state
	ag.mu.Unlock()

	called := make(chan struct{}, 1)
	ag.SetObserverApplyCommittedSpentRefs(func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool) {
		called <- struct{}{}
		return common.ObservedTx{}, false
	})
	qtx := common.QuorumTx{ObsTx: tx, Inbound: false}
	payload, err := qtx.Marshal()
	require.NoError(t, err)
	ag.handleQuorumTxCommitted(&ebifrost.EventNotification{Payload: payload})
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("committed import callback did not run")
	}
	require.Eventually(t, func() bool { return len(ag.applyCommittedSpentRefsSem) == 0 }, time.Second, 10*time.Millisecond)

	state.mu.Lock()
	assert.False(t, state.spentRefsApplied)
	state.mu.Unlock()
	ag.authorizedXMRSpentRefsMu.Lock()
	_, remembered := ag.authorizedXMRSpentRefs[xmrSpentRefsConflictKeyFor(tx)]
	ag.authorizedXMRSpentRefsMu.Unlock()
	assert.False(t, remembered)
}
