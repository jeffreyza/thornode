package observer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func setupTestDB(t *testing.T) (*ObserverStorage, string) {
	tempDir, err := os.MkdirTemp("", "observer-storage-test")
	require.NoError(t, err)

	opts := config.LevelDBOptions{
		FilterBitsPerKey:              10,
		CompactionTableSizeMultiplier: 1,
		WriteBuffer:                   4194304,
		BlockCacheCapacity:            8388608,
		CompactOnInit:                 true,
	}

	storage, err := NewObserverStorage(tempDir, opts)
	require.NoError(t, err)
	require.NotNil(t, storage)

	return storage, tempDir
}

func cleanupTestDB(t *testing.T, storage *ObserverStorage, tempDir string) {
	err := storage.Close()
	require.NoError(t, err)
	err = os.RemoveAll(tempDir)
	require.NoError(t, err)
}

func createTestTxIn(chain common.Chain, blockHeight int64) *types.TxIn {
	return &types.TxIn{
		Chain: chain,
		TxArray: []*types.TxInItem{
			{
				BlockHeight: blockHeight,
				Tx:          "tx-data",
				Memo:        "memo",
			},
		},
	}
}

func TestNewObserverStorage(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer os.RemoveAll(tempDir)

		assert.NotNil(t, storage)
		assert.NotNil(t, storage.db)

		err := storage.Close()
		assert.NoError(t, err)
	})

	t.Run("creation failure", func(t *testing.T) {
		// Create a file where we want to create the DB to force an error
		tempDir, err := os.MkdirTemp("", "observer-storage-test")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		// Create a file with the same name as the path to cause an error
		dbPath := filepath.Join(tempDir, "invalid-db")
		err = os.WriteFile(dbPath, []byte("test"), 0o600)
		require.NoError(t, err)

		opts := config.LevelDBOptions{}
		storage, err := NewObserverStorage(dbPath, opts)

		assert.Error(t, err)
		assert.Nil(t, storage)
	})
}

func TestCreateTxKey(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	testCases := []struct {
		name        string
		txIn        *types.TxIn
		expectedKey string
	}{
		{
			name:        "BTC mempool transaction",
			txIn:        createTestTxIn(common.BTCChain, 100),
			expectedKey: "txs:BTC:100",
		},
		{
			name:        "ETH non-mempool transaction",
			txIn:        createTestTxIn(common.ETHChain, 200),
			expectedKey: "txs:ETH:200",
		},
		{
			name: "transaction with no items",
			txIn: &types.TxIn{
				Chain:   common.BTCChain,
				MemPool: true,
				TxArray: []*types.TxInItem{},
			},
			expectedKey: "txs:BTC:0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := storage.createTxKey(tc.txIn, 0)
			assert.Equal(t, tc.expectedKey, key)
		})
	}
}

func TestPendingErrataPersistsUntilCommitted(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "deadbeef"}
	block := types.ErrataBlock{
		Height:     123,
		DeliveryID: "xmr-reorg-123-deadbeef",
		Txs:        []types.ErrataTx{tx},
	}
	require.NoError(t, storage.AddErrataBlock(block))

	pending, err := storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, block.Height, pending[0].Height)
	assert.Equal(t, block.DeliveryID, pending[0].DeliveryID)
	assert.Equal(t, tx, pending[0].Tx)
	assert.True(t, pending[0].DeliverySeen)
	assert.False(t, pending[0].CleanupComplete)
	assert.False(t, pending[0].Committed)

	// Re-delivery is idempotent, and neither half of the state machine removes
	// the record on its own.
	require.NoError(t, storage.AddErrataBlock(block))
	require.NoError(t, storage.MarkErrataCommitted(tx))
	pending, err = storage.GetPendingErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.True(t, pending[0].Committed)
	assert.False(t, pending[0].CleanupComplete)

	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))
	pending, err = storage.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestErrataAttestationRetryStatePersistsWithoutResurrection(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	tx := types.ErrataTx{Chain: common.XMRChain, TxID: "retry-state"}
	require.NoError(t, storage.AddErrataBlock(types.ErrataBlock{
		Height: 321,
		Txs:    []types.ErrataTx{tx},
	}))
	require.NoError(t, storage.CompleteErrataCleanup([]types.ErrataTx{tx}, nil))

	firstAttempt := time.Unix(1_750_000_000, 123)
	record, recorded, err := storage.RecordErrataAttestationAttempt(tx, firstAttempt)
	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
	assert.Equal(t, firstAttempt.UnixNano(), record.FirstAttemptAtUnixNano)
	assert.Equal(t, firstAttempt.UnixNano(), record.LastAttemptAtUnixNano)

	require.NoError(t, storage.Close())
	reopened, err := NewObserverStorage(tempDir, config.LevelDBOptions{})
	require.NoError(t, err)
	defer cleanupTestDB(t, reopened, tempDir)

	record, found, err := reopened.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint32(1), record.AttestationAttempts)
	assert.Equal(t, firstAttempt.UnixNano(), record.FirstAttemptAtUnixNano)

	exhaustedAt := firstAttempt.Add(errataAttestationRetryHorizon)
	marked, err := reopened.MarkErrataAttestationRetryExhausted(tx, exhaustedAt)
	require.NoError(t, err)
	require.True(t, marked)
	record, found, err = reopened.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.AttestationRetryExhausted)
	assert.Equal(t, exhaustedAt.UnixNano(), record.RetryExhaustedAtUnixNano)

	_, recorded, err = reopened.RecordErrataAttestationAttempt(tx, exhaustedAt.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, recorded)

	// A late live commit still completes the quarantined record, while a stale
	// attempt observed after deletion cannot resurrect it.
	require.NoError(t, reopened.MarkErrataCommitted(tx))
	_, found, err = reopened.GetPendingErrataTx(tx)
	require.NoError(t, err)
	assert.False(t, found)
	_, recorded, err = reopened.RecordErrataAttestationAttempt(tx, exhaustedAt.Add(2*time.Hour))
	require.NoError(t, err)
	assert.False(t, recorded)
}

// Removal must not depend on the errata height matching the deck view's
// height: the errata block reports 99 while the on-deck views sit at 100.
// BTC (like every non-XMR chain) therefore removes TxID-wide.
func TestErrataCleanupPersistsAcrossRestart(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer os.RemoveAll(tempDir)

	txIn := &types.TxIn{
		Chain:                common.BTCChain,
		Filtered:             true,
		ConfirmationRequired: 6,
		TxArray: []*types.TxInItem{
			{BlockHeight: 100, Tx: "orphaned"},
			{BlockHeight: 100, Tx: "retained"},
		},
	}
	require.NoError(t, storage.AddOrUpdateTx(txIn))
	errata := types.ErrataTx{Chain: common.BTCChain, TxID: "orphaned"}
	block := types.ErrataBlock{
		Height:     99,
		DeliveryID: "btc-reorg-99-orphaned",
		Txs:        []types.ErrataTx{errata},
	}
	require.NoError(t, storage.AddErrataBlock(block))

	key := TxInKey(txIn)
	observer := &Observer{
		logger:  log.Logger,
		lock:    &sync.Mutex{},
		onDeck:  map[txInKey]*types.TxIn{key: txIn},
		storage: storage,
	}
	// cleanupErrataBlock is the production entry point: it runs filterErrataTx
	// and then durably marks the outbox record cleanup-complete.
	require.NoError(t, observer.cleanupErrataBlock(block))
	require.Len(t, observer.onDeck[key].TxArray, 1)
	assert.Equal(t, "retained", observer.onDeck[key].TxArray[0].Tx)

	require.NoError(t, storage.Close())
	reopened, err := NewObserverStorage(tempDir, config.LevelDBOptions{})
	require.NoError(t, err)
	defer reopened.Close()

	stored, err := reopened.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].TxArray, 1)
	assert.Equal(t, "retained", stored[0].TxArray[0].Tx)

	record, found, err := reopened.GetPendingErrataTx(errata)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.CleanupComplete)
	assert.False(t, record.Committed)
	require.NoError(t, reopened.MarkErrataCommitted(errata))
	pending, err := reopened.GetPendingErrata()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestGetOnDeckTxs(t *testing.T) {
	t.Run("empty storage", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		txs, err := storage.GetOnDeckTxs()
		assert.NoError(t, err)
		assert.Empty(t, txs)
	})

	t.Run("legacy storage format", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Manually add a legacy format record
		legacyTxs := []*types.TxIn{
			createTestTxIn(common.BTCChain, 100),
			createTestTxIn(common.ETHChain, 200),
		}
		data, err := json.Marshal(legacyTxs)
		require.NoError(t, err)

		err = storage.db.Put([]byte(LegacyOnDeckKey), data, nil)
		require.NoError(t, err)

		txs, err := storage.GetOnDeckTxs()
		assert.NoError(t, err)
		assert.Len(t, txs, 2)
		assert.Equal(t, common.BTCChain, txs[0].Chain)
		assert.Equal(t, common.ETHChain, txs[1].Chain)

		// Verify legacy transactions were migrated
		for _, tx := range legacyTxs {
			key := storage.createTxKey(tx, 0)
			data, err = storage.db.Get([]byte(key), nil)
			assert.NoError(t, err)

			var storedTx types.TxIn
			err = json.Unmarshal(data, &storedTx)
			assert.NoError(t, err)
			assert.Equal(t, tx.Chain, storedTx.Chain)
			assert.Equal(t, tx.TxArray[0].BlockHeight, storedTx.TxArray[0].BlockHeight)
		}

		// Verify migration flag was set
		val, err := storage.db.Get([]byte(LegacyMigrationHandledKey), nil)
		assert.NoError(t, err)
		assert.Equal(t, "true", string(val))
	})

	t.Run("new storage format", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add transactions in new format
		tx1 := createTestTxIn(common.BTCChain, 100)
		tx2 := createTestTxIn(common.ETHChain, 200)

		err := storage.AddOrUpdateTx(tx1)
		require.NoError(t, err)
		err = storage.AddOrUpdateTx(tx2)
		require.NoError(t, err)

		// Make GetOnDeckTxs skip legacy check by setting migration flag
		err = storage.db.Put([]byte(LegacyMigrationHandledKey), []byte("true"), nil)
		require.NoError(t, err)

		txs, err := storage.GetOnDeckTxs()
		assert.NoError(t, err)
		assert.Len(t, txs, 2)

		// Check that we got both transactions (order may vary)
		foundBTC := false
		foundETH := false
		for _, tx := range txs {
			if tx.Chain.Equals(common.BTCChain) {
				foundBTC = true
			}
			if tx.Chain.Equals(common.ETHChain) {
				foundETH = true
			}
		}
		assert.True(t, foundBTC, "BTC transaction not found")
		assert.True(t, foundETH, "ETH transaction not found")
	})

	t.Run("error unmarshalling transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add invalid JSON data
		key := "txs:BTC:100"
		err := storage.db.Put([]byte(key), []byte("invalid json"), nil)
		require.NoError(t, err)

		// Make GetOnDeckTxs skip legacy check
		err = storage.db.Put([]byte(LegacyMigrationHandledKey), []byte("true"), nil)
		require.NoError(t, err)

		txs, err := storage.GetOnDeckTxs()
		assert.Error(t, err)
		assert.Nil(t, txs)
		assert.Contains(t, err.Error(), "fail to unmarshal ondeck tx")
	})
}

func TestAddOrUpdateTx(t *testing.T) {
	t.Run("add new transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Verify it was stored
		key := storage.createTxKey(tx, 0)
		data, err := storage.db.Get([]byte(key), nil)
		assert.NoError(t, err)

		var storedTx types.TxIn
		err = json.Unmarshal(data, &storedTx)
		assert.NoError(t, err)
		assert.Equal(t, common.BTCChain, storedTx.Chain)
		assert.Equal(t, int64(100), storedTx.TxArray[0].BlockHeight)
		assert.Equal(t, "tx-data", storedTx.TxArray[0].Tx)
	})

	t.Run("update existing transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add initial transaction
		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Update the transaction
		tx.TxArray[0].Tx = "updated-tx-data"
		err = storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Verify it was updated
		key := storage.createTxKey(tx, 0)
		data, err := storage.db.Get([]byte(key), nil)
		assert.NoError(t, err)

		var storedTx types.TxIn
		err = json.Unmarshal(data, &storedTx)
		assert.NoError(t, err)
		assert.Equal(t, "updated-tx-data", storedTx.TxArray[0].Tx)
	})
}

func TestObserverWriteOptions(t *testing.T) {
	ordinary := createTestTxIn(common.BTCChain, 100)
	assert.Nil(t, observerWriteOptions(ordinary))

	replay := createTestTxIn(common.XMRChain, 100)
	replay.TxArray[0].OnObservedTxStored = func() {}
	writeOptions := observerWriteOptions(replay)
	require.NotNil(t, writeOptions)
	assert.True(t, writeOptions.Sync)

	rejectedReplay := createTestTxIn(common.XMRChain, 100)
	rejectedReplay.TxArray[0].OnObservedTxRejected = func(error) {}
	writeOptions = observerWriteOptions(rejectedReplay)
	require.NotNil(t, writeOptions)
	assert.True(t, writeOptions.Sync)
}

func TestRemoveTx(t *testing.T) {
	t.Run("remove existing transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Add a transaction
		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.AddOrUpdateTx(tx)
		assert.NoError(t, err)

		// Remove it
		err = storage.RemoveTx(tx, 0)
		assert.NoError(t, err)

		// Verify it's gone
		key := storage.createTxKey(tx, 0)
		_, err = storage.db.Get([]byte(key), nil)
		assert.True(t, errors.Is(err, leveldb.ErrNotFound))
	})

	t.Run("remove non-existent transaction", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer cleanupTestDB(t, storage, tempDir)

		// Try to remove a transaction that doesn't exist
		tx := createTestTxIn(common.BTCChain, 100)
		err := storage.RemoveTx(tx, 0)

		// This should not error, as leveldb.Delete doesn't error for non-existent keys
		assert.NoError(t, err)
	})
}

func TestClose(t *testing.T) {
	t.Run("close database", func(t *testing.T) {
		storage, tempDir := setupTestDB(t)
		defer os.RemoveAll(tempDir)

		err := storage.Close()
		assert.NoError(t, err)

		// Verify it's closed by trying to use it (should error)
		tx := createTestTxIn(common.BTCChain, 100)
		err = storage.AddOrUpdateTx(tx)
		assert.Error(t, err)
	})
}

func TestPendingXMRCertificateDurableHandoffRestartAndFairness(t *testing.T) {
	store, path := setupTestDB(t)
	obs, _ := observerTestXMRPlan(t)
	key := secp256k1.GenPrivKey()
	signed := observerTestSignedHistoricalXMRCertificate(t, obs, key)
	store.beforeCertificateWrite = func() error { return errors.New("injected pre-fsync crash") }
	require.Error(t, store.savePendingXMRCertificate(signed))
	records, _, err := store.pendingXMRCertificates(time.Now().Add(time.Hour), maxPendingXMRCertificates)
	require.NoError(t, err)
	require.Empty(t, records)
	store.beforeCertificateWrite = nil
	require.NoError(t, store.savePendingXMRCertificate(signed))
	want, err := signed.Marshal()
	require.NoError(t, err)
	require.NoError(t, store.Close())
	store, err = NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	defer cleanupTestDB(t, store, path)
	records, _, err = store.pendingXMRCertificates(time.Now().Add(time.Second), maxPendingXMRCertificates)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, want, records[0].Payload, "restart must retain the exact signed bytes before delivery")
	require.NoError(t, store.savePendingXMRCertificate(signed))
	for i := 1; i < maxPendingXMRCertificates; i++ {
		next := obs
		next.Tx.ID = common.TxID(observerTestHash(fmt.Sprintf("pending-%d", i)))
		require.NoError(t, store.savePendingXMRCertificate(observerTestSignedHistoricalXMRCertificate(t, next, key)))
	}
	require.NoError(t, store.savePendingXMRCertificate(signed), "duplicate at capacity must succeed")
	overflow := obs
	overflow.Tx.ID = common.TxID(observerTestHash("overflow"))
	require.ErrorContains(t, store.savePendingXMRCertificate(observerTestSignedHistoricalXMRCertificate(t, overflow, key)), "queue is full")
	first, stats, err := store.pendingXMRCertificates(time.Now(), 16)
	require.NoError(t, err)
	require.Len(t, first, 16)
	require.Equal(t, maxPendingXMRCertificates, stats.count, "occupancy includes every queued record, not only selected retries")
	for _, record := range first {
		require.NoError(t, store.finishPendingXMRCertificate(record, false, time.Now()))
	}
	second, _, err := store.pendingXMRCertificates(time.Now(), 16)
	require.NoError(t, err)
	require.Len(t, second, 16)
	for _, a := range first {
		for _, b := range second {
			require.NotEqual(t, a.key, b.key, "retry backoff must allow later records to progress")
		}
	}
	// A terminal frees capacity; a transient failure did not.
	require.NoError(t, store.finishPendingXMRCertificate(second[0], true, time.Now()))
	require.NoError(t, store.savePendingXMRCertificate(observerTestSignedHistoricalXMRCertificate(t, overflow, key)))
}

func TestPendingXMRCertificateFinalPromotionAndCompletionCAS(t *testing.T) {
	store, path := setupTestDB(t)
	defer cleanupTestDB(t, store, path)
	obs, _ := observerTestXMRPlan(t)
	obs.BlockHeight, obs.FinaliseHeight = 100, 137
	key := secp256k1.GenPrivKey()
	nonfinal := observerTestSignedHistoricalXMRCertificate(t, obs, key)
	require.NoError(t, store.savePendingXMRCertificate(nonfinal))
	records, _, err := store.pendingXMRCertificates(time.Now().Add(time.Second), 16)
	require.NoError(t, err)
	require.Len(t, records, 1)
	obs.BlockHeight = obs.FinaliseHeight
	final := observerTestSignedHistoricalXMRCertificate(t, obs, key)
	require.NoError(t, store.savePendingXMRCertificate(final))
	require.NoError(t, store.finishPendingXMRCertificate(records[0], true, time.Now()))
	require.NoError(t, store.savePendingXMRCertificate(nonfinal))
	records, _, err = store.pendingXMRCertificates(time.Now().Add(time.Second), 16)
	require.NoError(t, err)
	require.Len(t, records, 1)
	want, err := final.Marshal()
	require.NoError(t, err)
	require.Equal(t, want, records[0].Payload, "stale completion or replay must not erase/downgrade the final phase")
}
