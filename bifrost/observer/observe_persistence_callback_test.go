package observer

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newPersistenceCallbackObserver(t *testing.T) (*Observer, string, func()) {
	t.Helper()
	storage, tempDir := setupTestDB(t)
	observer := &Observer{
		logger:          log.Logger,
		onDeck:          make(map[txInKey]*types.TxIn),
		lock:            &sync.Mutex{},
		storage:         storage,
		chains:          make(map[common.Chain]chainclients.ChainClient),
		stopChan:        make(chan struct{}),
		globalTxsQueue:  make(chan types.TxIn),
		observerWorkers: 1,
	}
	return observer, tempDir, func() {
		require.NoError(t, storage.Close())
		require.NoError(t, os.RemoveAll(tempDir))
	}
}

func persistenceCallbackTxIn(stored func(), rejected func(error)) types.TxIn {
	return types.TxIn{
		Chain:    common.XMRChain,
		Filtered: true,
		TxArray: []*types.TxInItem{{
			BlockHeight:          100,
			Tx:                   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			OnObservedTxStored:   stored,
			OnObservedTxRejected: rejected,
		}},
	}
}

func TestProcessObservedTxPersistenceCallbacks(t *testing.T) {
	t.Run("durable write acknowledges stored", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()

		stored := 0
		rejected := 0
		callbackOutsideLock := false
		txIn := persistenceCallbackTxIn(func() {
			stored++
			callbackOutsideLock = observer.lock.TryLock()
			if callbackOutsideLock {
				observer.lock.Unlock()
			}
		}, func(error) { rejected++ })
		require.NoError(t, observer.processObservedTx(txIn))

		assert.Equal(t, 1, stored)
		assert.Zero(t, rejected)
		assert.True(t, callbackOutsideLock)
		key := TxInKey(&txIn)
		require.Contains(t, observer.onDeck, key)
		assert.Nil(t, observer.onDeck[key].TxArray[0].OnObservedTxStored)
		assert.Nil(t, observer.onDeck[key].TxArray[0].OnObservedTxRejected)
	})

	t.Run("durably stored duplicate acknowledges stored", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()

		require.NoError(t, observer.processObservedTx(persistenceCallbackTxIn(nil, nil)))
		stored := 0
		rejected := 0
		callbackOutsideLock := false
		require.NoError(t, observer.processObservedTx(persistenceCallbackTxIn(func() {
			stored++
			callbackOutsideLock = observer.lock.TryLock()
			if callbackOutsideLock {
				observer.lock.Unlock()
			}
		}, func(error) { rejected++ })))

		assert.Equal(t, 1, stored)
		assert.Zero(t, rejected)
		assert.True(t, callbackOutsideLock)
		assert.Len(t, observer.onDeck, 1)
	})

	t.Run("durable duplicate is fsynced before stored acknowledgement", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()
		require.NoError(t, observer.processObservedTx(persistenceCallbackTxIn(nil, nil)))
		observer.storage.beforeSyncTxWrite = func() error {
			return errors.New("injected duplicate sync failure")
		}

		stored := 0
		var rejection error
		callbackOutsideLock := false
		// The injected sync failure must surface as a returned error, not just
		// as a rejection callback.
		require.Error(t, observer.processObservedTx(persistenceCallbackTxIn(
			func() { stored++ },
			func(err error) {
				rejection = err
				callbackOutsideLock = observer.lock.TryLock()
				if callbackOutsideLock {
					observer.lock.Unlock()
				}
			},
		)))

		assert.Zero(t, stored)
		require.Error(t, rejection)
		assert.Contains(t, rejection.Error(), "sync duplicate observed transaction")
		assert.True(t, callbackOutsideLock)
		assert.Len(t, observer.onDeck, 1)
	})

	t.Run("storage failure rejects without false ondeck entry", func(t *testing.T) {
		observer, tempDir, _ := newPersistenceCallbackObserver(t)
		require.NoError(t, observer.storage.Close())
		defer func() { require.NoError(t, os.RemoveAll(tempDir)) }()

		stored := 0
		var rejection error
		callbackOutsideLock := false
		// Storage is closed, so the write failure must propagate as an error.
		require.Error(t, observer.processObservedTx(persistenceCallbackTxIn(func() { stored++ }, func(err error) {
			rejection = err
			callbackOutsideLock = observer.lock.TryLock()
			if callbackOutsideLock {
				observer.lock.Unlock()
			}
		})))

		assert.Zero(t, stored)
		require.Error(t, rejection)
		assert.True(t, callbackOutsideLock)
		assert.Empty(t, observer.onDeck)
	})

	t.Run("filter rejection invokes rejected", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()

		var rejection error
		txIn := persistenceCallbackTxIn(nil, func(err error) { rejection = err })
		txIn.Filtered = false
		require.NoError(t, observer.processObservedTx(txIn))

		assert.ErrorIs(t, rejection, errObservedTxFiltered)
		assert.Empty(t, observer.onDeck)
	})

	t.Run("empty item invokes rejected", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()

		var rejection error
		require.NoError(t, observer.processObservedTx(types.TxIn{
			Chain:    common.XMRChain,
			Filtered: true,
			TxArray: []*types.TxInItem{{
				OnObservedTxRejected: func(err error) { rejection = err },
			}},
		}))

		assert.ErrorIs(t, rejection, errObservedTxEmpty)
		assert.Empty(t, observer.onDeck)
	})

	t.Run("non-empty filtered subset rejects omitted callback", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()

		vaultPubKey := randomXMRObservedPubKey(t)
		vaultAddress, err := vaultPubKey.GetAddress(common.XMRChain)
		require.NoError(t, err)
		stored := 0
		var rejection error
		require.NoError(t, observer.processObservedTx(types.TxIn{
			Chain: common.XMRChain,
			TxArray: []*types.TxInItem{
				{
					BlockHeight:          100,
					Tx:                   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Sender:               common.XMRSeededDummyAddress('8', []byte("accepted-sender")),
					To:                   vaultAddress.String(),
					ObservedVaultPubKey:  vaultPubKey,
					OnObservedTxStored:   func() { stored++ },
					OnObservedTxRejected: func(error) { t.Fatal("accepted item was rejected") },
				},
				{
					BlockHeight:          100,
					Tx:                   "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
					Sender:               "unknown-sender",
					To:                   "unknown-destination",
					OnObservedTxStored:   func() { t.Fatal("filtered item was stored") },
					OnObservedTxRejected: func(err error) { rejection = err },
				},
			},
		}))

		assert.Equal(t, 1, stored)
		assert.ErrorIs(t, rejection, errObservedTxFiltered)
		require.Len(t, observer.onDeck, 1)
		for _, deck := range observer.onDeck {
			require.Len(t, deck.TxArray, 1)
			assert.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", deck.TxArray[0].Tx)
		}
	})

	t.Run("multiple filtered views acknowledge once", func(t *testing.T) {
		observer, _, cleanup := newPersistenceCallbackObserver(t)
		defer cleanup()

		vaultPubKey := randomXMRObservedPubKey(t)
		vaultAddress, err := vaultPubKey.GetAddress(common.XMRChain)
		require.NoError(t, err)
		stored := 0
		rejected := 0
		source := &types.TxInItem{
			BlockHeight:          100,
			Tx:                   "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			Memo:                 "MIGRATE:100",
			Sender:               vaultAddress.String(),
			To:                   vaultAddress.String(),
			ObservedVaultPubKey:  vaultPubKey,
			OnObservedTxStored:   func() { stored++ },
			OnObservedTxRejected: func(error) { rejected++ },
		}
		require.NoError(t, observer.processObservedTx(types.TxIn{
			Chain:   common.XMRChain,
			TxArray: []*types.TxInItem{source},
		}))

		// The original item may remain in a scanner prefetch cache. Its
		// callback shares the same terminal once guard as all filtered copies.
		source.OnObservedTxStored()
		assert.Equal(t, 1, stored)
		assert.Zero(t, rejected)
	})
}

func TestProcessTxInsInactiveRejects(t *testing.T) {
	observer, _, cleanup := newPersistenceCallbackObserver(t)
	defer cleanup()
	observer.lastNodeStatus = stypes.NodeStatus_Standby

	rejected := make(chan error, 1)
	go observer.processTxIns()
	observer.globalTxsQueue <- persistenceCallbackTxIn(nil, func(err error) { rejected <- err })

	select {
	case err := <-rejected:
		assert.ErrorIs(t, err, types.ErrObservedTxNotApplicable)
	case <-time.After(time.Second):
		t.Fatal("observer did not reject inactive observation")
	}
	close(observer.stopChan)
}

func TestXMRUnknownNodeStatusRetriesPersistenceHandoff(t *testing.T) {
	observer, _, cleanup := newPersistenceCallbackObserver(t)
	defer cleanup()
	done := make(chan struct{})
	go func() { observer.processTxIns(); close(done) }()
	for _, callbacks := range []bool{false, true} {
		rejected := make(chan error, 1)
		tx := persistenceCallbackTxIn(nil, nil)
		if callbacks {
			tx.TxArray[0].OnObservedTxRejected = func(err error) { rejected <- err }
		}
		tx.Persisted = make(chan error, 1)
		observer.globalTxsQueue <- tx
		select {
		case err := <-tx.Persisted:
			require.ErrorContains(t, err, "status is not known")
			require.NotErrorIs(t, err, types.ErrObservedTxNotApplicable)
		case <-time.After(time.Second):
			t.Fatal("missing retry acknowledgement")
		}
		if callbacks {
			require.Error(t, <-rejected)
		}
	}
	require.Empty(t, observer.onDeck)
	observer.lastNodeStatusMu.Lock()
	observer.lastNodeStatus = stypes.NodeStatus_Active
	observer.lastNodeStatusMu.Unlock()
	tx := persistenceCallbackTxIn(nil, nil)
	tx.Persisted = make(chan error, 1)
	observer.globalTxsQueue <- tx
	require.NoError(t, <-tx.Persisted)
	require.Len(t, observer.onDeck, 1)
	close(observer.stopChan)
	<-done
}
