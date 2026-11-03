package observer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	types "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newErrataOrderingObserver(t *testing.T) (*Observer, *ObserverStorage) {
	t.Helper()
	storage, tempDir := setupTestDB(t)
	t.Cleanup(func() { cleanupTestDB(t, storage, tempDir) })
	thornodeHeight := int64(100)
	return &Observer{
		logger:                          log.Logger,
		storage:                         storage,
		onDeck:                          make(map[txInKey]*types.TxIn),
		lock:                            &sync.Mutex{},
		permanentErrataQuarantineFn:     func(common.ErrataTx, int64) error { return nil },
		permanentErrataBroadcastBlockFn: func(common.ErrataTx) error { return nil },
		thorchainBridge: &MockThorchainBridge{
			getBlockHeightFunc: func() (int64, error) {
				thornodeHeight++
				return thornodeHeight, nil
			},
			getObservedTxVoterFunc: func(txID string) (*stypes.QueryObservedTxVoter, error) {
				return &stypes.QueryObservedTxVoter{TxID: common.TxID(txID), Reverted: true}, nil
			},
		},
	}, storage
}

func errataOrderingTxIn(
	chain common.Chain,
	height, confirmations int64,
	items ...*types.TxInItem,
) *types.TxIn {
	return &types.TxIn{
		Chain:                chain,
		TxArray:              items,
		Filtered:             true,
		ConfirmationRequired: confirmations,
	}
}

func errataOrderingItem(txID string, height int64, view string) *types.TxInItem {
	return &types.TxInItem{
		BlockHeight: height,
		Tx:          txID,
		Memo:        view,
		Sender:      "sender-" + view,
		To:          "to-" + view,
	}
}

// XMR is the chain this test's survival assertion applies to. Removal is
// scoped to the orphan height only for XMR, which pairs it with the TxID-wide
// tombstone backstop in reconcileCommittedXMRErrata; that is what lets a view
// re-mined at height 250 outlive an errata reported at height 100. On BTC a
// genuinely re-mined transaction never produces an errata in the first place
// (utxo/client_internal.go "still exists"), and BTC removal is TxID-wide.
func TestFilterErrataTxAtomicallyRemovesEveryViewAndExactDeckKey(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	first := errataOrderingTxIn(
		common.XMRChain,
		100,
		6,
		errataOrderingItem("tx-a", 100, "inbound"),
		errataOrderingItem("tx-a", 100, "outbound"),
		errataOrderingItem("tx-b", 100, "second-id"),
		errataOrderingItem("tx-keep", 100, "keep"),
	)
	second := errataOrderingTxIn(
		common.XMRChain,
		250,
		9,
		errataOrderingItem("tx-a", 250, "higher-height-deck-entry"),
	)
	firstKey, secondKey := TxInKey(first), TxInKey(second)
	observer.onDeck[firstKey] = first
	observer.onDeck[secondKey] = second
	require.NoError(t, storage.AddOrUpdateTx(first))
	require.NoError(t, storage.AddOrUpdateTx(second))

	err := observer.filterErrataTx(types.ErrataBlock{
		Height: 100,
		Txs: []types.ErrataTx{
			{Chain: common.XMRChain, TxID: common.TxID("tx-a")},
			{Chain: common.XMRChain, TxID: common.TxID("tx-b")},
		},
	})
	require.NoError(t, err)
	require.Len(t, observer.onDeck, 2)
	require.Len(t, observer.onDeck[firstKey].TxArray, 1)
	require.Equal(t, "tx-keep", observer.onDeck[firstKey].TxArray[0].Tx)
	require.Equal(t, int64(6), observer.onDeck[firstKey].ConfirmationRequired)
	require.Equal(t, "tx-a", observer.onDeck[secondKey].TxArray[0].Tx)

	// GetOnDeckTxs is the restart path. Confirmations make the real key 106,
	// not the orphan height 100; this catches deletion under the wrong key.
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 2)
	restoredIDs := make([]string, 0, 2)
	for _, txIn := range restored {
		require.Len(t, txIn.TxArray, 1)
		restoredIDs = append(restoredIDs, txIn.TxArray[0].Tx)
	}
	require.ElementsMatch(t, []string{"tx-keep", "tx-a"}, restoredIDs)
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 2)
}

func TestErrataTombstoneWinsAgainstPausedOldObservationWorker(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	ready := make(chan struct{})
	resume := make(chan struct{})
	observer.beforeObservedTxDeckLock = func() {
		close(ready)
		<-resume
	}

	var rejected error
	stored := false
	item := errataOrderingItem("paused-old", 100, "old-branch")
	item.OnObservedTxStored = func() { stored = true }
	item.OnObservedTxRejected = func(err error) { rejected = err }
	done := make(chan struct{})
	var processErr error
	go func() {
		processErr = observer.processObservedTx(*errataOrderingTxIn(common.BTCChain, 100, 0, item))
		close(done)
	}()
	<-ready
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 100,
		Txs:    []types.ErrataTx{{Chain: common.BTCChain, TxID: common.TxID("paused-old")}},
	}))
	close(resume)
	<-done

	require.NoError(t, processErr)
	require.False(t, stored)
	require.ErrorIs(t, rejected, errObservedTxErrata)
	require.NotErrorIs(t, rejected, types.ErrObservedTxPermanentlyReverted)
	require.Empty(t, observer.onDeck)
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Empty(t, restored)

	// The same signed transaction may later be mined at a new canonical
	// location. The old-worker fence must not become a TxID-wide quarantine.
	observer.beforeObservedTxDeckLock = nil
	canonicalStored := false
	canonical := errataOrderingItem("paused-old", 250, "canonical-remine")
	canonical.OnObservedTxStored = func() { canonicalStored = true }
	require.NoError(t, observer.processObservedTx(*errataOrderingTxIn(common.BTCChain, 250, 0, canonical)))
	require.True(t, canonicalStored)
	require.Len(t, observer.onDeck, 1)
}

func TestPendingErrataRejectsOnlyOrphanHeight(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	block := types.ErrataBlock{
		Height: 100,
		Txs:    []types.ErrataTx{{Chain: common.XMRChain, TxID: common.TxID("pending")}},
	}
	require.NoError(t, observer.filterErrataTx(block))

	for _, height := range []int64{100, 250} {
		height := height
		t.Run(fmt.Sprintf("height-%d", height), func(t *testing.T) {
			stored := false
			var rejected error
			item := errataOrderingItem("pending", height, "replay")
			item.OnObservedTxStored = func() { stored = true }
			item.OnObservedTxRejected = func(err error) { rejected = err }
			require.NoError(t, observer.processObservedTx(*errataOrderingTxIn(common.XMRChain, height, 0, item)))
			if height == block.Height {
				require.False(t, stored)
				require.ErrorIs(t, rejected, types.ErrObservedTxOrphaned)
				require.NotErrorIs(t, rejected, types.ErrObservedTxPermanentlyReverted)
				return
			}
			require.True(t, stored)
			require.NoError(t, rejected)
		})
	}
	require.Len(t, observer.onDeck, 1)
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1)
	require.Equal(t, int64(250), restored[0].TxArray[0].BlockHeight)
}

func TestPendingErrataRetriesAcrossObserverRestartUntilCommittedEvent(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("pending-restart")}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 77,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))

	// A new Observer object over the same durable storage models process
	// restart after queue receipt/tombstone fsync but before attestation.
	restarted := &Observer{
		logger:                          log.Logger,
		storage:                         storage,
		onDeck:                          make(map[txInKey]*types.TxIn),
		lock:                            &sync.Mutex{},
		thorchainBridge:                 observer.thorchainBridge,
		permanentErrataQuarantineFn:     func(common.ErrataTx, int64) error { return nil },
		permanentErrataBroadcastBlockFn: func(common.ErrataTx) error { return nil },
	}
	attempts := 0
	restarted.errataAttestFn = func(context.Context, common.ErrataTx) error {
		attempts++
		if attempts == 1 {
			return errors.New("transient gossip failure")
		}
		return nil
	}
	firstAttempt := time.Now()
	restarted.retryPendingErrataAt(context.Background(), firstAttempt)
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	restarted.retryPendingErrataAt(context.Background(), firstAttempt.Add(errataAttestationRetryDelay(1)))
	pending, err = storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1, "local enqueue must not clear the durable outbox")
	require.Equal(t, 2, attempts)
	record, found, err := storage.GetPendingErrataTx(types.ErrataTx{Chain: errata.Chain, TxID: errata.Id})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(2), record.AttestationAttempts)

	// Only the committed-quorum event is a strong enough terminal receipt.
	require.NoError(t, restarted.handleErrataTxCommitted(errata, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION))
	pending, err = storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.True(t, tombstone.Committed)
}

func TestCallbackErrataUsesDurableRetryBackoffAndExhaustion(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := types.ErrataTx{Chain: common.XMRChain, TxID: common.TxID("callback-retry-ledger")}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 79,
		Txs:    []types.ErrataTx{errata},
	}))

	record, found, err := storage.GetPendingErrataTx(errata)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, record.DeliverySeen)
	require.True(t, record.CleanupComplete)

	attempts := 0
	observer.errataAttestFn = func(context.Context, common.ErrataTx) error {
		attempts++
		return nil
	}
	firstAttempt := time.Now()
	observer.retryPendingErrataAt(context.Background(), firstAttempt)
	observer.retryPendingErrataAt(context.Background(), firstAttempt.Add(errataAttestationRetryDelay(1)-time.Nanosecond))
	require.Equal(t, 1, attempts, "durable backoff must suppress an early callback retry")

	observer.retryPendingErrataAt(context.Background(), firstAttempt.Add(errataAttestationRetryHorizon))
	require.Equal(t, 1, attempts, "the 24-hour horizon must quarantine instead of gossiping forever")
	record, found, err = storage.GetPendingErrataTx(errata)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, record.AttestationRetryExhausted)
	require.Equal(t, uint32(1), record.AttestationAttempts)
	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestCrashAfterLocalErrataEnqueueReplaysOutboxAfterRestart(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("crash-before-batch-send")}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 78,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))

	firstEnqueues := 0
	observer.errataAttestFn = func(context.Context, common.ErrataTx) error {
		firstEnqueues++
		return nil // accepted only by the volatile in-memory batcher
	}
	firstAttempt := time.Now()
	observer.retryPendingErrataAt(context.Background(), firstAttempt)
	require.Equal(t, 1, firstEnqueues)
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1, "local enqueue is not a durable terminal receipt")

	// Model a crash before the batcher writes to any peer. A fresh Observer over
	// the same LevelDB must enqueue the vote again.
	restarted := &Observer{
		logger:                          log.Logger,
		storage:                         storage,
		onDeck:                          make(map[txInKey]*types.TxIn),
		lock:                            &sync.Mutex{},
		thorchainBridge:                 observer.thorchainBridge,
		permanentErrataQuarantineFn:     func(common.ErrataTx, int64) error { return nil },
		permanentErrataBroadcastBlockFn: func(common.ErrataTx) error { return nil },
	}
	restartEnqueues := 0
	restarted.errataAttestFn = func(context.Context, common.ErrataTx) error {
		restartEnqueues++
		return nil
	}
	restarted.retryPendingErrataAt(context.Background(), firstAttempt.Add(errataAttestationRetryDelay(1)))
	require.Equal(t, 1, restartEnqueues)
	pending, err = storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)

	require.NoError(t, restarted.handleErrataTxCommitted(errata, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION))
	pending, err = storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestDuplicateErrataHandoffPreservesCommittedGeneration(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("duplicate-handoff")}
	block := types.ErrataBlock{
		Height: 88,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}
	require.NoError(t, observer.filterErrataTx(block))
	require.NoError(t, observer.handleErrataTxCommitted(errata, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION))
	before, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.True(t, before.Committed)

	// Scanner crash after Observer fsync but before consuming OnStored replays
	// the same errata. It must not manufacture a new pending generation.
	require.NoError(t, observer.filterErrataTx(block))
	after, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.Equal(t, before.Generation, after.Generation)
	require.True(t, after.Committed)
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
	_, found, err := storage.GetPendingErrataTx(types.ErrataTx{Chain: errata.Chain, TxID: errata.Id})
	require.NoError(t, err)
	require.False(t, found, "a duplicate callback must not reopen an already committed outbox entry")
}

func TestQuorumErrataCommittedEventClearsOutboxWithoutVolatileState(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("committed-event")}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 91,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))

	gossip := &AttestationGossip{
		logger:    log.Logger,
		pubKey:    []byte("local-validator"),
		errataTxs: make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
	}
	gossip.SetObserverHandleErrataTxCommitted(observer)
	payload, err := (&common.QuorumErrataTx{
		Outcome:  common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION,
		ErrataTx: &errata,
		Attestations: []*common.Attestation{{
			PubKey: gossip.pubKey,
		}},
	}).Marshal()
	require.NoError(t, err)
	gossip.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})

	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestQuorumErrataCommittedEventRetainsOutboxWithoutLocalAttestation(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("other-validator-event")}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 92,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))

	gossip := &AttestationGossip{
		logger:    log.Logger,
		pubKey:    []byte("local-validator"),
		errataTxs: make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
	}
	gossip.SetObserverHandleErrataTxCommitted(observer)
	payload, err := (&common.QuorumErrataTx{
		Outcome:  common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION,
		ErrataTx: &errata,
		Attestations: []*common.Attestation{{
			PubKey: []byte("other-validator"),
		}},
	}).Marshal()
	require.NoError(t, err)
	gossip.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})

	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.False(t, pending[0].Committed)
}

func TestLegacyCommittedErrataEventBeforeScannerHandoffDoesNotReopenOutbox(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("event-before-scanner")}
	bridge, bridgeOK := observer.thorchainBridge.(*MockThorchainBridge)
	require.True(t, bridgeOK)
	thornodeHeight := int64(100)
	bridge.getBlockHeightFreshFunc = func() (int64, error) { return thornodeHeight, nil }
	bridge.getObservedTxVoterFreshFunc = func(txID string) (*stypes.QueryObservedTxVoter, bool, error) {
		return &stypes.QueryObservedTxVoter{TxID: common.TxID(txID), Reverted: false}, true, nil
	}
	quarantineCalls := 0
	observer.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		quarantineCalls++
		return nil
	}

	// A matching observation can already be durably on deck when a peer's
	// committed event wins the race. The event has no orphan location and must
	// not delete or reject that observation by TxID alone.
	var deckRejected error
	deckItem := errataOrderingItem(errata.Id.String(), 90, "pre-event-deck")
	deckItem.OnObservedTxRejected = func(err error) { deckRejected = err }
	deckTx := errataOrderingTxIn(errata.Chain, 90, 0, deckItem)
	deckKey := TxInKey(deckTx)
	observer.onDeck[deckKey] = deckTx
	require.NoError(t, storage.AddOrUpdateTx(deckTx))

	require.NoError(t, seedLegacyErrataReceipt(t, observer, errata))
	require.NoError(t, deckRejected)
	require.Contains(t, observer.onDeck, deckKey)
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1)
	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	require.True(t, tombstone.Committed)
	require.Zero(t, tombstone.OrphanHeight)
	require.Equal(t, int64(101), tombstone.VerifyAfterHeight)
	require.Nil(t, tombstone.VoterReverted, "false is not authoritative at the event height")
	require.Zero(t, quarantineCalls)

	thornodeHeight = 101
	unresolved, err := observer.reconcileCommittedXMRErrata()
	require.NoError(t, err)
	require.Equal(t, 1, unresolved)
	tombstone, err = storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.Nil(t, tombstone.VoterReverted)
	require.Zero(t, quarantineCalls)

	// Once the local scanner supplies the orphan height, only that exact deck
	// location is removed and acknowledged as orphaned.
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 90,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))
	require.ErrorIs(t, deckRejected, types.ErrObservedTxOrphaned)
	require.NotErrorIs(t, deckRejected, types.ErrObservedTxPermanentlyReverted)
	require.Empty(t, observer.onDeck)

	tombstone, err = storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	require.True(t, tombstone.Committed)
	require.Equal(t, int64(90), tombstone.OrphanHeight)
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)

	restarted := &Observer{
		logger:  log.Logger,
		storage: storage,
		onDeck:  make(map[txInKey]*types.TxIn),
		lock:    &sync.Mutex{},
	}
	require.NoError(t, restarted.restoreDeck())
	canonicalStored := false
	canonical := errataOrderingItem(errata.Id.String(), 93, "canonical-remine")
	canonical.OnObservedTxStored = func() { canonicalStored = true }
	require.NoError(t, restarted.processObservedTx(*errataOrderingTxIn(errata.Chain, 93, 0, canonical)))
	require.True(t, canonicalStored)
	require.Len(t, restarted.onDeck, 1)
}

func TestXMRCommittedErrataQuarantinesAfterPendingBarrierBeforeTerminalReceipt(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	}
	rejected := false
	item := errataOrderingItem(errata.Id.String(), 120, "event-first-local-deck")
	item.OnObservedTxRejected = func(err error) {
		require.ErrorIs(t, err, types.ErrObservedTxPermanentlyReverted)
		rejected = true
	}
	txIn := errataOrderingTxIn(common.XMRChain, 120, 4, item)
	key := TxInKey(txIn)
	observer.onDeck[key] = txIn
	require.NoError(t, storage.AddOrUpdateTx(txIn))

	hookCalls := 0
	observer.permanentErrataQuarantineFn = func(got common.ErrataTx, height int64) error {
		hookCalls++
		require.Equal(t, errata, got)
		require.Zero(t, height, "event-first tombstone has no scanner height yet")
		tombstone, err := storage.getErrataTombstone(got.Chain, got.Id.String())
		require.NoError(t, err)
		require.NotNil(t, tombstone)
		require.True(t, tombstone.Committed, "delivery receipt must precede voter reconciliation")
		require.Nil(t, tombstone.VoterReverted, "permanent state must follow chain quarantine")
		hasDeckRow, err := storage.db.Has([]byte(createObserverTxKey(key.chain, key.height, key.allowFutureObservation)), nil)
		require.NoError(t, err)
		require.True(t, hasDeckRow, "event without an orphan location must not delete a deck row")
		require.Contains(t, observer.onDeck, key)
		require.False(t, rejected, "direct quarantine precedes the receipt callback")
		require.True(t, observer.lock.TryLock(), "quarantine hook ran while Observer lock was held")
		observer.lock.Unlock()
		return nil
	}

	require.NoError(t, observer.handleErrataTxCommitted(errata, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	require.Zero(t, hookCalls, "broad quarantine must run outside committed delivery")
	unresolved, reconcileErr := observer.reconcileCommittedXMRErrata()
	require.NoError(t, reconcileErr)
	require.Zero(t, unresolved)
	require.Equal(t, 1, hookCalls)
	require.True(t, rejected)
	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.True(t, tombstone.Committed)
	require.NotNil(t, tombstone.VoterReverted)
	require.True(t, *tombstone.VoterReverted)
	require.NotContains(t, observer.onDeck, key)

	var higherRejected error
	higher := errataOrderingItem(errata.Id.String(), 250, "canonical-after-revert")
	higher.OnObservedTxRejected = func(err error) { higherRejected = err }
	require.NoError(t, observer.processObservedTx(*errataOrderingTxIn(common.XMRChain, 250, 0, higher)))
	require.ErrorIs(t, higherRejected, types.ErrObservedTxPermanentlyReverted)
	require.Empty(t, observer.onDeck)

	restarted := &Observer{
		logger:  log.Logger,
		storage: storage,
		onDeck:  make(map[txInKey]*types.TxIn),
		lock:    &sync.Mutex{},
	}
	replayed := 0
	restarted.permanentErrataQuarantineFn = func(got common.ErrataTx, height int64) error {
		require.Equal(t, errata, got)
		require.Zero(t, height)
		replayed++
		return nil
	}
	unresolved, reconcileErr = restarted.reconcileCommittedXMRErrata()
	require.NoError(t, reconcileErr)
	require.Zero(t, unresolved)
	require.Zero(t, replayed, "runtime maintenance must not replay completed quarantine")
	var restartedRejected error
	remined := errataOrderingItem(errata.Id.String(), 300, "restart-after-revert")
	remined.OnObservedTxRejected = func(err error) { restartedRejected = err }
	require.NoError(t, restarted.processObservedTx(*errataOrderingTxIn(common.XMRChain, 300, 0, remined)))
	require.ErrorIs(t, restartedRejected, types.ErrObservedTxPermanentlyReverted)
	require.Empty(t, restarted.onDeck)
}

func TestXMRCommittedErrataQuarantineFailureRetriesOutcomeAfterRestart(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
	injected := errors.New("injected XMR quarantine failure")
	observer.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error { return injected }
	require.NoError(t, observer.handleErrataTxCommitted(errata, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	unresolved, reconcileErr := observer.reconcileCommittedXMRErrata()
	require.ErrorIs(t, reconcileErr, injected)
	require.Equal(t, 1, unresolved)

	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	require.True(t, tombstone.Committed, "DeliverTx receipt must stop duplicate errata votes")
	require.Nil(t, tombstone.VoterReverted, "failed local quarantine must leave outcome retryable")
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)

	// A fresh Observer must not replay unresolved state as permanent. It retries
	// the explicit voter outcome and performs the idempotent local quarantine.
	restarted := &Observer{
		logger:  log.Logger,
		storage: storage,
		onDeck:  make(map[txInKey]*types.TxIn),
		lock:    &sync.Mutex{},
		thorchainBridge: &MockThorchainBridge{
			getBlockHeightFunc: func() (int64, error) { return 1_000, nil },
			getObservedTxVoterFunc: func(txID string) (*stypes.QueryObservedTxVoter, error) {
				return &stypes.QueryObservedTxVoter{TxID: common.TxID(txID), Reverted: true}, nil
			},
		},
	}
	replayed := 0
	restarted.permanentErrataQuarantineFn = func(got common.ErrataTx, height int64) error {
		require.Equal(t, errata, got)
		require.Zero(t, height)
		replayed++
		return nil
	}
	require.Zero(t, replayed)
	unresolved, reconcileErr = restarted.reconcileCommittedXMRErrata()
	require.NoError(t, reconcileErr)
	require.Zero(t, unresolved)
	require.Equal(t, 1, replayed)
	tombstone, err = storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.True(t, tombstone.Committed)
	require.NotNil(t, tombstone.VoterReverted)
	require.True(t, *tombstone.VoterReverted)
}

func TestLegacyXMRCommittedErrataVoterQueryFailureRetainsOutcomeRetry(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("abababababababababababababababababababababababababababababababab"),
	}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 75,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))

	queries := 0
	bridge, bridgeOK := observer.thorchainBridge.(*MockThorchainBridge)
	require.True(t, bridgeOK)
	bridge.getObservedTxVoterFreshFunc = func(txID string) (*stypes.QueryObservedTxVoter, bool, error) {
		queries++
		if queries == 1 {
			return nil, false, errors.New("voter block is not query-visible yet")
		}
		return &stypes.QueryObservedTxVoter{TxID: common.TxID(txID), Reverted: true}, true, nil
	}
	quarantines := 0
	observer.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		quarantines++
		return nil
	}

	require.NoError(t, seedLegacyErrataReceipt(t, observer, errata))
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending, "committed delivery must stop duplicate errata votes")

	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.True(t, tombstone.Committed)
	require.Nil(t, tombstone.VoterReverted)
	require.Zero(t, quarantines)

	unresolved, reconcileErr := observer.reconcileCommittedXMRErrata()
	require.NoError(t, reconcileErr)
	require.Zero(t, unresolved)
	tombstone, err = storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone.VoterReverted)
	require.True(t, *tombstone.VoterReverted)
	require.Equal(t, 1, quarantines)
}

func TestLegacyXMRCommittedErrataReceiptSurvivesConfirmationRPCOutage(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"),
	}
	bridge, bridgeOK := observer.thorchainBridge.(*MockThorchainBridge)
	require.True(t, bridgeOK)
	bridge.getBlockHeightFreshFunc = func() (int64, error) {
		return 0, errors.New("thornode height unavailable")
	}
	bridge.getObservedTxVoterFreshFunc = func(string) (*stypes.QueryObservedTxVoter, bool, error) {
		return nil, false, errors.New("thornode voter unavailable")
	}

	require.NoError(t, seedLegacyErrataReceipt(t, observer, errata))
	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone)
	require.True(t, tombstone.Committed, "the one-shot event must be durable before REST confirmation")
	require.Zero(t, tombstone.VerifyAfterHeight)
	require.Nil(t, tombstone.VoterReverted)
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending, "an RPC outage must not reopen an already delivered vote")
}

func TestLegacyXMRMissingVoterRemainsRecheckableAfterFreshHeightBarrier(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("edededededededededededededededededededededededededededededededed"),
	}
	bridge, bridgeOK := observer.thorchainBridge.(*MockThorchainBridge)
	require.True(t, bridgeOK)
	height := int64(700)
	bridge.getBlockHeightFreshFunc = func() (int64, error) { return height, nil }
	bridge.getObservedTxVoterFreshFunc = func(string) (*stypes.QueryObservedTxVoter, bool, error) {
		return nil, false, nil
	}

	require.NoError(t, seedLegacyErrataReceipt(t, observer, errata))
	tombstone, err := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.Equal(t, int64(701), tombstone.VerifyAfterHeight)
	require.Nil(t, tombstone.VoterReverted)

	height = 701
	unresolved, err := observer.reconcileCommittedXMRErrata()
	require.NoError(t, err)
	require.Equal(t, 1, unresolved)
	tombstone, err = storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.Nil(t, tombstone.VoterReverted)

	bridge.getObservedTxVoterFreshFunc = func(txID string) (*stypes.QueryObservedTxVoter, bool, error) {
		return &stypes.QueryObservedTxVoter{TxID: common.TxID(txID), Reverted: true}, true, nil
	}
	unresolved, err = observer.reconcileCommittedXMRErrata()
	require.NoError(t, err)
	require.Zero(t, unresolved)
	tombstone, err = storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, err)
	require.NotNil(t, tombstone.VoterReverted)
	require.True(t, *tombstone.VoterReverted)
}

func TestUnresolvedCommittedErrataDefersOnlyXMRChainStart(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("fefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefe"),
	}
	require.NoError(t, storage.applyCommittedErrata(errata, nil))
	bridge, bridgeOK := observer.thorchainBridge.(*MockThorchainBridge)
	require.True(t, bridgeOK)
	bridge.getBlockHeightFreshFunc = func() (int64, error) { return 900, nil }
	bridge.getObservedTxVoterFreshFunc = func(string) (*stypes.QueryObservedTxVoter, bool, error) {
		return &stypes.QueryObservedTxVoter{TxID: errata.Id, Reverted: false}, true, nil
	}
	xmrChain := &errataStartOrderChain{}
	btcChain := &errataStartOrderChain{}
	observer.chains = map[common.Chain]chainclients.ChainClient{
		common.XMRChain: xmrChain,
		common.BTCChain: btcChain,
	}
	observer.stopChan = make(chan struct{})

	unresolved, err := observer.reconcileCommittedXMRErrata()
	require.NoError(t, err)
	require.Equal(t, 1, unresolved)
	observer.startConfiguredChainClients(true)
	require.Equal(t, 1, btcChain.starts, "unrelated chain clients must remain operational")
	require.Zero(t, xmrChain.starts, "XMR must not scan or rebroadcast before the outcome is classified")
	started := observer.startedChainClientSnapshot()
	require.Len(t, started, 1)
	require.Same(t, btcChain, started[0])
	for _, chain := range started {
		require.NoError(t, chain.RollbackBlockScanner())
	}
	require.Equal(t, 1, btcChain.rollbacks)
	require.Zero(t, xmrChain.rollbacks, "a deferred scanner has no rollback receiver")
	tombstone, getErr := storage.getErrataTombstone(errata.Chain, errata.Id.String())
	require.NoError(t, getErr)
	require.Equal(t, int64(901), tombstone.VerifyAfterHeight)
	require.Nil(t, tombstone.VoterReverted, "a false voter is not a permanent terminal")

	bridge.getObservedTxVoterFreshFunc = func(string) (*stypes.QueryObservedTxVoter, bool, error) {
		return &stypes.QueryObservedTxVoter{TxID: errata.Id, Reverted: true}, true, nil
	}
	observer.maintainXMRErrataOnce(context.Background())
	require.Equal(t, 1, xmrChain.starts)
	observer.stopStartedChainClients()
	require.Equal(t, 1, btcChain.stops)
	require.Equal(t, 1, xmrChain.stops)
}

func TestDeferredXMRChainCannotStartAfterObserverStop(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("abababababababababababababababababababababababababababababababab"),
	}
	require.NoError(t, storage.applyCommittedErrata(errata, nil))
	bridge, bridgeOK := observer.thorchainBridge.(*MockThorchainBridge)
	require.True(t, bridgeOK)
	bridge.getBlockHeightFreshFunc = func() (int64, error) { return 900, nil }
	bridge.getObservedTxVoterFreshFunc = func(string) (*stypes.QueryObservedTxVoter, bool, error) {
		return &stypes.QueryObservedTxVoter{TxID: errata.Id, Reverted: false}, true, nil
	}
	xmrChain := &errataStartOrderChain{}
	btcChain := &errataStartOrderChain{}
	observer.chains = map[common.Chain]chainclients.ChainClient{
		common.XMRChain: xmrChain,
		common.BTCChain: btcChain,
	}
	observer.stopChan = make(chan struct{})
	observer.startConfiguredChainClients(true)
	require.Equal(t, 1, btcChain.starts)

	observer.stopStartedChainClients()
	require.Equal(t, 1, btcChain.stops)
	require.Zero(t, xmrChain.stops)

	bridge.getObservedTxVoterFreshFunc = func(string) (*stypes.QueryObservedTxVoter, bool, error) {
		return &stypes.QueryObservedTxVoter{TxID: errata.Id, Reverted: true}, true, nil
	}
	observer.maintainXMRErrataOnce(context.Background())
	require.Zero(t, xmrChain.starts, "shutdown must win over late errata reconciliation")
}

func TestErrataTombstoneRetainsEveryOrphanHeight(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	txID := common.TxID("multi-location")
	for _, height := range []int64{100, 80} {
		require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
			Height: height,
			Txs:    []types.ErrataTx{{Chain: common.BTCChain, TxID: txID}},
		}))
	}
	tombstone, err := storage.getErrataTombstone(common.BTCChain, txID.String())
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{100, 80}, tombstone.OrphanHeights)
	require.Equal(t, uint64(2), tombstone.Generation)

	for _, height := range []int64{100, 80} {
		var rejected error
		item := errataOrderingItem(txID.String(), height, fmt.Sprintf("stale-%d", height))
		item.OnObservedTxRejected = func(err error) { rejected = err }
		require.NoError(t, observer.processObservedTx(*errataOrderingTxIn(common.BTCChain, height, 0, item)))
		require.ErrorIs(t, rejected, types.ErrObservedTxOrphaned)
	}
	canonicalStored := false
	canonical := errataOrderingItem(txID.String(), 120, "canonical")
	canonical.OnObservedTxStored = func() { canonicalStored = true }
	require.NoError(t, observer.processObservedTx(*errataOrderingTxIn(common.BTCChain, 120, 0, canonical)))
	require.True(t, canonicalStored)
}

type errataStartOrderChain struct {
	mockChainClient
	starts    int
	stops     int
	rollbacks int
}

func (c *errataStartOrderChain) Start(
	chan types.TxIn,
	chan types.ErrataBlock,
	chan types.Solvency,
	chan common.NetworkFee,
) {
	c.starts++
}

func (c *errataStartOrderChain) Stop() {
	c.stops++
}

func (c *errataStartOrderChain) RollbackBlockScanner() error {
	c.rollbacks++
	return nil
}

func TestObserverStartupReplaysOnlyExplicitlyRevertedXMRBeforeChainStart(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	pending := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
	}
	delivered := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
	}
	reverted := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
	}
	require.NoError(t, storage.applyErrata(types.ErrataBlock{
		Height: 44,
		Txs:    []types.ErrataTx{{Chain: pending.Chain, TxID: pending.Id}},
	}, nil))
	require.NoError(t, storage.applyCommittedErrata(delivered, nil))
	require.NoError(t, storage.recordErrataVoterResult(delivered, false, nil))
	require.NoError(t, storage.applyCommittedErrata(reverted, nil))
	require.NoError(t, storage.recordErrataVoterResult(reverted, true, nil))

	seen := make(map[common.TxID]int64)
	observer.permanentErrataQuarantineFn = func(errata common.ErrataTx, height int64) error {
		seen[errata.Id] = height
		return nil
	}
	require.NoError(t, observer.replayPermanentErrataQuarantines())
	require.Equal(t, map[common.TxID]int64{reverted.Id: 0}, seen)

	chain := &errataStartOrderChain{}
	observer.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: chain}
	stopBeforeStart := errors.New("stop after proving replay order")
	deferred, err := observer.startRestoredChainClients()
	require.NoError(t, err)
	require.True(t, deferred)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		require.Zero(t, chain.starts)
		cancel()
		return stopBeforeStart
	}
	observer.maintainXMRErrata(ctx, true)
	require.Zero(t, chain.starts)
}

func TestXMRPermanentErrataMissingChainHookFailsClosed(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	errata := common.ErrataTx{
		Chain: common.XMRChain,
		Id:    common.TxID("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
	}
	require.NoError(t, storage.applyCommittedErrata(errata, nil))
	observer.permanentErrataQuarantineFn = nil
	observer.chains = map[common.Chain]chainclients.ChainClient{
		common.XMRChain: &mockChainClient{},
	}
	unresolved, err := observer.reconcileCommittedXMRErrata()
	require.ErrorContains(t, err, "lacks permanent errata quarantine support")
	require.Equal(t, 1, unresolved)
}

func TestPendingErrataResubmitsAfterThrottleUntilCommittedEvent(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)
	observer.attestationGossip = ag
	ag.SetObserverHandleErrataTxCommitted(observer)
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("durable-resubmit")}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 101,
		Txs:    []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))

	originalVerifySignature := verifySignature
	verifySignature = func(_, _, _ []byte) error { return nil }
	t.Cleanup(func() { verifySignature = originalVerifySignature })

	submissions := 0
	grpcClient.sendQuorumErrataFunc = func(
		context.Context,
		*common.QuorumErrataTx,
		...grpc.CallOption,
	) (*ebifrost.SendQuorumErrataTxResult, error) {
		submissions++
		return &ebifrost.SendQuorumErrataTxResult{}, nil
	}

	// The durable outbox contributes this node's vote; one peer vote completes
	// this implementation's two-of-three supermajority and produces the first
	// successful gRPC send.
	firstAttempt := time.Now()
	observer.retryPendingErrataAt(context.Background(), firstAttempt)
	ag.handleErrataAttestation(context.Background(), common.AttestErrataTx{
		ErrataTx: &errata,
		Attestation: &common.Attestation{
			PubKey:    []byte(fmt.Sprintf("peer-%d", 0)),
			Signature: []byte(fmt.Sprintf("signature-%d", 0)),
		},
	})
	require.Equal(t, 1, submissions)

	// A tick before the throttle expires remains deduped end to end.
	observer.retryPendingErrataAt(context.Background(), firstAttempt)
	require.Equal(t, 1, submissions)

	// No committed event arrived. Once the existing throttle expires, the
	// pending tombstone re-arms only uncommitted errata votes and resubmits.
	ag.mu.Lock()
	state := ag.errataTxs[errata]
	require.NotNil(t, state)
	state.mu.Lock()
	ag.mu.Unlock()
	state.lastAttestationsSent = time.Now().Add(-ag.config.MinTimeBetweenAttestations - time.Second)
	state.mu.Unlock()
	secondAttempt := firstAttempt.Add(errataAttestationRetryDelay(1))
	observer.retryPendingErrataAt(context.Background(), secondAttempt)
	require.Equal(t, 2, submissions)

	state.mu.Lock()
	committedAttestations := state.AttestationsCopy()
	state.mu.Unlock()
	payload, err := (&common.QuorumErrataTx{
		Outcome:      common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION,
		ErrataTx:     &errata,
		Attestations: committedAttestations,
	}).Marshal()
	require.NoError(t, err)

	// A committed notification is not terminal until Observer fsync succeeds.
	// If that write fails, Gossip must leave votes uncommitted so the durable
	// tombstone can re-arm and submit them again.
	failCommittedWrite := true
	storage.beforeErrataWrite = func() error {
		if failCommittedWrite {
			failCommittedWrite = false
			return errors.New("injected committed tombstone fsync failure")
		}
		return nil
	}
	ag.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})
	pending, err := storage.getPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	state.mu.Lock()
	for _, attestation := range state.attestations {
		require.False(t, attestation.committed)
	}
	state.lastAttestationsSent = time.Now().Add(-ag.config.MinTimeBetweenAttestations - time.Second)
	state.mu.Unlock()
	thirdAttempt := secondAttempt.Add(errataAttestationRetryDelay(2))
	observer.retryPendingErrataAt(context.Background(), thirdAttempt)
	require.Equal(t, 3, submissions)

	// The replayed committed event can now fsync the barrier and only then mark
	// the in-memory votes committed.
	ag.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})
	pending, err = storage.getPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
	state.mu.Lock()
	for _, attestation := range state.attestations {
		require.True(t, attestation.committed)
	}
	state.mu.Unlock()

	// Even after another nominal retry interval, the committed tombstone is no
	// longer selected by the durable outbox.
	state.mu.Lock()
	state.lastAttestationsSent = time.Now().Add(-ag.config.MinTimeBetweenAttestations - time.Second)
	state.mu.Unlock()
	observer.retryPendingErrataAt(context.Background(), thirdAttempt.Add(errataAttestationRetryDelay(3)))
	require.Equal(t, 3, submissions)
}

func TestCommittedErrataWithoutObserverHandlerStillMarksGossipState(t *testing.T) {
	errata := common.ErrataTx{Chain: common.BTCChain, Id: common.TxID("no-observer-handler")}
	attestation := &common.Attestation{PubKey: []byte("validator"), Signature: []byte("signature")}
	state := &AttestationState[*common.ErrataTx]{
		Item: &errata,
		attestations: []attestationSentState{{
			attestation: attestation,
			sent:        true,
		}},
	}
	gossip := &AttestationGossip{
		logger:    log.Logger,
		errataTxs: map[common.ErrataTx]*AttestationState[*common.ErrataTx]{errata: state},
	}
	payload, err := (&common.QuorumErrataTx{
		Outcome:      common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION,
		ErrataTx:     &errata,
		Attestations: []*common.Attestation{attestation},
	}).Marshal()
	require.NoError(t, err)

	gossip.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})
	state.mu.Lock()
	defer state.mu.Unlock()
	require.True(t, state.attestations[0].committed)
}

func TestErrataBatcherDeduplicatesDurableOutboxRetries(t *testing.T) {
	batcher := &AttestationBatcher{
		errataTxBatch: make([]*common.AttestErrataTx, 0, 2),
		maxBatchSize:  100,
		forceSendChan: make(chan struct{}, 1),
		logger:        log.Logger,
	}
	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("dedupe")}
	message := common.AttestErrataTx{
		ErrataTx: &errata,
		Attestation: &common.Attestation{
			PubKey:    []byte("same-pubkey"),
			Signature: []byte("same-signature"),
		},
	}
	batcher.AddErrataTx(message)
	batcher.AddErrataTx(message)
	require.Len(t, batcher.errataTxBatch, 1)
}

func TestRestoreDropsOnlyExactOrphanDeckRowWhenTombstoneAlreadyDurable(t *testing.T) {
	_, storage := newErrataOrderingObserver(t)
	orphan := errataOrderingTxIn(
		common.BTCChain,
		100,
		6,
		errataOrderingItem("stale-after-crash", 100, "orphan"),
	)
	canonical := errataOrderingTxIn(
		common.BTCChain,
		160,
		6,
		errataOrderingItem("stale-after-crash", 160, "canonical-remine"),
	)
	require.NoError(t, storage.AddOrUpdateTx(orphan))
	require.NoError(t, storage.AddOrUpdateTx(canonical))
	// Simulate storage left by an older/non-atomic handoff: tombstone durable,
	// the exact orphan deck row still present, and a canonical re-mining stored
	// at a later height.
	require.NoError(t, storage.applyErrata(types.ErrataBlock{
		Height: 100,
		Txs: []types.ErrataTx{{
			Chain: common.BTCChain,
			TxID:  common.TxID("stale-after-crash"),
		}},
	}, nil))
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1)
	require.Equal(t, int64(160), restored[0].TxArray[0].BlockHeight)
}

func TestObserverStartFailsClosedOnCorruptErrataTombstone(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	stale := errataOrderingTxIn(
		common.XMRChain,
		100,
		6,
		errataOrderingItem("corrupt-tombstone", 100, "stale"),
	)
	require.NoError(t, storage.AddOrUpdateTx(stale))
	require.NoError(t, storage.db.Put(
		[]byte(errataTombstoneKey(common.XMRChain, "corrupt-tombstone")),
		[]byte("{not-json"),
		nil,
	))

	err := observer.Start(context.Background())
	require.ErrorContains(t, err, "decode observer errata tombstone")
	require.Empty(t, observer.onDeck, "startup must not continue with a partial restore")
}

func TestInactiveTransitionRetainsAndActiveTransitionRegossipsXMRDeck(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	observer.tssKeysignMetricMgr = metrics.NewTssKeysignMetricMgr()
	bridge := &MockThorchainBridge2{
		nodeStatus:  stypes.NodeStatus_Standby,
		activeNodes: common.PubKeys{},
	}
	observer.thorchainBridge = bridge
	observer.attestationGossip = &AttestationGossip{
		logger: log.Logger,
		host:   NewMockHost(nil),
	}
	observer.chains = map[common.Chain]chainclients.ChainClient{
		common.XMRChain: &mockChainClient{},
	}
	observer.lastNodeStatus = stypes.NodeStatus_Active
	historical := errataOrderingTxIn(
		common.XMRChain,
		100,
		6,
		errataTestDeckItem(t, common.XMRChain, 3),
	)
	key := TxInKey(historical)
	observer.onDeck[key] = historical
	require.NoError(t, storage.AddOrUpdateTx(historical))

	observer.sendDeck(context.Background())
	require.Contains(t, observer.onDeck, key)
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1, "inactive churn must retain the durable scanner handoff")

	regossiped := 0
	observer.sendObservedTxsFn = func(txs common.ObservedTxs, _ bool) {
		require.Equal(t, historical.TxArray[0].Tx, txs[0].Tx.ID.String())
		regossiped++
	}
	bridge.nodeStatus = stypes.NodeStatus_Active
	observer.sendDeck(context.Background())
	require.Equal(t, 1, regossiped)
	require.Contains(t, observer.onDeck, key)
}

func TestErrataStoredCallbackRunsAfterDurableAcceptance(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	observer.stopChan = make(chan struct{})
	observer.globalErrataQueue = make(chan types.ErrataBlock)
	observer.errataAttestFn = func(context.Context, common.ErrataTx) error {
		return errors.New("leave pending for retry")
	}
	stored := make(chan error, 1)
	block := types.ErrataBlock{
		Height: 90,
		Txs:    []types.ErrataTx{{Chain: common.XMRChain, TxID: common.TxID("durable-ack")}},
		OnStored: func() {
			tombstone, err := storage.getErrataTombstone(common.XMRChain, "durable-ack")
			if err != nil {
				stored <- err
				return
			}
			if tombstone == nil {
				stored <- errors.New("stored callback ran before tombstone was readable")
				return
			}
			stored <- nil
		},
		OnRejected: func(err error) { stored <- err },
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go observer.processErrataTx(ctx)
	observer.globalErrataQueue <- block
	require.NoError(t, <-stored)
	close(observer.stopChan)
}

// A non-XMR errata must remove the on-deck view even when the errata height
// differs from the height the view was recorded at. UTXO mempool observations
// are stamped with the scan cursor rather than the mined height, so a tx seen
// in the mempool at 99 and later mined into 100 produces an errata at 100
// against a deck view at 99. Scoping removal to the errata height would strand
// that view, and mempool deck entries are never final, so the observer would
// keep re-attesting a view of a reverted TxID while attesting its errata.
func TestFilterErrataTxRemovesNonXMRViewAtDifferentHeight(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	txIn := errataOrderingTxIn(
		common.BTCChain,
		99,
		0,
		errataOrderingItem("mempool-drift", 99, "mempool-view"),
		errataOrderingItem("tx-keep", 99, "keep"),
	)
	key := TxInKey(txIn)
	observer.onDeck[key] = txIn
	require.NoError(t, storage.AddOrUpdateTx(txIn))

	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 100,
		Txs:    []types.ErrataTx{{Chain: common.BTCChain, TxID: common.TxID("mempool-drift")}},
	}))

	require.Len(t, observer.onDeck[key].TxArray, 1)
	require.Equal(t, "tx-keep", observer.onDeck[key].TxArray[0].Tx)

	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1)
	require.Len(t, restored[0].TxArray, 1)
	require.Equal(t, "tx-keep", restored[0].TxArray[0].Tx)
}

// The same drift on XMR is height-scoped: the view at another height survives
// filterErrataTx and is instead removed TxID-wide by the tombstone backstop in
// reconcileCommittedXMRErrata.
func TestFilterErrataTxKeepsXMRViewAtDifferentHeight(t *testing.T) {
	observer, storage := newErrataOrderingObserver(t)
	txIn := errataOrderingTxIn(
		common.XMRChain,
		99,
		0,
		errataOrderingItem("xmr-drift", 99, "view"),
	)
	key := TxInKey(txIn)
	observer.onDeck[key] = txIn
	require.NoError(t, storage.AddOrUpdateTx(txIn))

	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 100,
		Txs:    []types.ErrataTx{{Chain: common.XMRChain, TxID: common.TxID("xmr-drift")}},
	}))

	require.Len(t, observer.onDeck[key].TxArray, 1)
	require.Equal(t, "xmr-drift", observer.onDeck[key].TxArray[0].Tx)
}

// seedLegacyErrataReceipt models durable pre-outcome state, not a new event.
// New UNSPECIFIED events must never create these delivered receipts.
func seedLegacyErrataReceipt(t *testing.T, o *Observer, er common.ErrataTx) error {
	t.Helper()
	o.storage.errataMu.Lock()
	err := o.storage.applyErrataBatch(types.ErrataBlock{
		Txs: []types.ErrataTx{{Chain: er.Chain, TxID: er.Id}},
	}, errataTombstoneUpdate{forceCommitted: true, completeOutbox: true}, nil)
	o.storage.errataMu.Unlock()
	if err != nil {
		return err
	}
	_, _ = o.reconcileCommittedXMRErrata()
	return nil
}
