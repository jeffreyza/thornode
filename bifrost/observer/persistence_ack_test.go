package observer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestProcessTxInsAcknowledgesDurableObservation(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	observer := &Observer{
		logger:          log.Logger,
		stopChan:        stop,
		lock:            &sync.Mutex{},
		onDeck:          make(map[txInKey]*types.TxIn),
		globalTxsQueue:  make(chan types.TxIn, 1),
		storage:         storage,
		observerWorkers: 1,
		lastNodeStatus:  stypes.NodeStatus_Active,
	}
	go func() {
		observer.processTxIns()
		close(done)
	}()

	persisted := make(chan error, 1)
	observer.globalTxsQueue <- types.TxIn{
		Chain:    common.XMRChain,
		Filtered: true,
		TxArray: []*types.TxInItem{
			{BlockHeight: 123, Tx: "xmr-observation"},
		},
		Persisted: persisted,
	}
	require.NoError(t, waitPersistenceAck(t, persisted))

	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].TxArray, 1)
	assert.Equal(t, "xmr-observation", stored[0].TxArray[0].Tx)

	close(stop)
	waitObserverLoop(t, done)
}

func TestProcessErrataAcknowledgesOnlyAfterDurableCleanup(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-orphan"}
	orphaned := &types.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{
			{BlockHeight: 456, Tx: tx.TxID.String()},
		},
	}
	require.NoError(t, storage.AddOrUpdateTx(orphaned))
	key := TxInKey(orphaned)
	gossip, _, _, _, _, _ := setupTestGossip(t)
	require.NoError(t, gossip.AttestObservedTx(context.Background(), &common.ObservedTx{
		Tx: common.Tx{
			ID:    tx.TxID,
			Chain: tx.Chain,
		},
		BlockHeight:    456,
		FinaliseHeight: 456,
	}, true, false))
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            map[txInKey]*types.TxIn{key: orphaned},
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	persisted := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height:     456,
		DeliveryID: "xmr-reorg-456-xmr-orphan",
		Txs:        []types.ErrataTx{tx},
		Persisted:  persisted,
	}
	require.NoError(t, waitPersistenceAck(t, persisted))

	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.DeliverySeen)
	assert.True(t, record.CleanupComplete)
	assert.False(t, record.Committed)
	assert.Empty(t, observer.onDeck)

	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	assert.Empty(t, stored)
	gossip.mu.Lock()
	assert.Empty(t, gossip.observedTxs)
	gossip.mu.Unlock()

	close(stop)
	waitObserverLoop(t, done)
}

func TestProcessErrataWithoutAckPreservesLegacyOneShotPath(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	tx := types.ErrataTx{Chain: common.BTCChain, TxID: "btc-orphan"}
	orphaned := &types.TxIn{
		Chain:                common.BTCChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{
			{BlockHeight: 500, Tx: tx.TxID.String()},
		},
	}
	require.NoError(t, storage.AddOrUpdateTx(orphaned))
	key := TxInKey(orphaned)
	gossip, _, _, _, _, _ := setupTestGossip(t)
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            map[txInKey]*types.TxIn{key: orphaned},
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	// A nil acknowledgement channel is the production contract used by the
	// non-XMR chain clients. It must remain a one-shot event.
	observer.globalErrataQueue <- types.ErrataBlock{
		Height: 500,
		Txs:    []types.ErrataTx{tx},
	}
	require.Eventually(t, func() bool {
		gossip.mu.Lock()
		defer gossip.mu.Unlock()
		_, found := gossip.errataTxs[common.ErrataTx{Chain: tx.Chain, Id: tx.TxID}]
		return found
	}, 2*time.Second, 10*time.Millisecond)

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.Empty(t, observer.onDeck)
	stored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	assert.Empty(t, stored)

	close(stop)
	waitObserverLoop(t, done)
}

func TestProcessErrataAcknowledgesCleanupDespiteAttestationFailure(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	stop := make(chan struct{})
	done := make(chan struct{})
	gossip, _, _, _, _, _ := setupTestGossip(t)
	gossip.setActiveValidators(nil)
	observer := &Observer{
		logger:            log.Logger,
		stopChan:          stop,
		lock:              &sync.Mutex{},
		onDeck:            make(map[txInKey]*types.TxIn),
		globalErrataQueue: make(chan types.ErrataBlock, 1),
		storage:           storage,
		attestationGossip: gossip,
	}
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "xmr-attestation-failure"}
	persisted := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height:     457,
		DeliveryID: "xmr-reorg-457-xmr-attestation-failure",
		Txs:        []types.ErrataTx{tx},
		Persisted:  persisted,
	}
	require.NoError(t, waitPersistenceAck(t, persisted))
	require.Eventually(t, func() bool {
		record, found, err := storage.GetPendingErrataTx(tx)
		return err == nil && found && record.AttestationAttempts == 1
	}, time.Second, time.Millisecond)

	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.CleanupComplete)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
	assert.False(t, record.Committed)

	// Scanner redelivery acknowledges the same durable cleanup. It must not
	// bypass the retry ledger's backoff or make network availability part of
	// the scanner's persistence handoff.
	retryAck := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		Height:     457,
		DeliveryID: "xmr-reorg-457-xmr-attestation-failure",
		Txs:        []types.ErrataTx{tx},
		Persisted:  retryAck,
	}
	require.NoError(t, waitPersistenceAck(t, retryAck))
	record, found, err = storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint32(1), record.AttestationAttempts)

	close(stop)
	waitObserverLoop(t, done)
}

func TestQuorumErrataCommitCompletesDurableOutbox(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "committed-orphan"}
	require.NoError(t, storage.AddErrataBlock(types.ErrataBlock{
		Height: 789,
		Txs:    []types.ErrataTx{tx},
	}))
	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))

	observer := &Observer{
		logger:  log.Logger,
		storage: storage,
		lock:    &sync.Mutex{},
	}
	gossip := &AttestationGossip{
		logger:      log.Logger,
		pubKey:      []byte("local-validator"),
		errataTxs:   make(map[common.ErrataTx]*AttestationState[*common.ErrataTx]),
		observedTxs: make(map[txKey]*AttestationState[*attestableObservedTx]),
	}
	gossip.SetObserverHandleErrataTxCommitted(observer)
	quorumErrata := common.QuorumErrataTx{
		Outcome:  common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION,
		ErrataTx: &common.ErrataTx{Chain: tx.Chain, Id: tx.TxID},
		Attestations: []*common.Attestation{{
			PubKey: gossip.pubKey,
		}},
	}
	payload, err := quorumErrata.Marshal()
	require.NoError(t, err)
	gossip.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestErrataAttestationRetryIsBackedOffAndTerminal(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "unconfirmed-errata"}
	block := types.ErrataBlock{Height: 900, Txs: []types.ErrataTx{tx}}
	require.NoError(t, storage.AddErrataBlock(block))
	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))

	now := time.Unix(1_750_000_000, 0)
	_, recorded, err := storage.RecordErrataAttestationAttempt(tx, now.Add(-errataAttestationRetryHorizon))
	require.NoError(t, err)
	require.True(t, recorded)

	observer := &Observer{logger: log.Logger, storage: storage}
	require.NoError(t, observer.broadcastErrataBlock(context.Background(), block, now))
	record, found, err := storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.AttestationRetryExhausted)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)

	// Terminal quarantine suppresses later sends even when no gossip client is
	// configured; without it this call would try to attest and fail.
	require.NoError(t, observer.broadcastErrataBlock(context.Background(), block, now.Add(time.Hour)))
	record, found, err = storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
}

func TestErrataAttestationRetryDelayIsExponentiallyBounded(t *testing.T) {
	assert.Zero(t, errataAttestationRetryDelay(0))
	assert.Equal(t, constants.ThorchainBlockTime, errataAttestationRetryDelay(1))
	assert.Equal(t, 2*constants.ThorchainBlockTime, errataAttestationRetryDelay(2))
	assert.Equal(t, errataAttestationRetryMaxDelay, errataAttestationRetryDelay(32))
}

func waitPersistenceAck(t *testing.T, persisted <-chan error) error {
	t.Helper()
	select {
	case err := <-persisted:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observer persistence acknowledgement")
		return nil
	}
}

func waitObserverLoop(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observer loop to stop")
	}
}
