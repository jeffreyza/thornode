package observer

import (
	"errors"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// Exercise scanner redelivery across two process boundaries, including a
// failed duplicate fsync and a reorg. The re-mined XMR view at another height
// remains eligible until THORChain explicitly reports permanent reversion.
func TestRecoveryObservationErrataCrashMatrix(t *testing.T) {
	path := t.TempDir()
	open := func() *ObserverStorage {
		t.Helper()
		storage, err := NewObserverStorage(path, config.LevelDBOptions{})
		require.NoError(t, err)
		return storage
	}
	storage := open()
	observer := &Observer{logger: zerolog.Nop(), storage: storage, lock: &sync.Mutex{}, onDeck: make(map[txInKey]*types.TxIn)}
	txID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observation := func(height int64) types.TxIn {
		return types.TxIn{
			Chain: common.XMRChain, Filtered: true, ConfirmationRequired: 6,
			TxArray: []*types.TxInItem{{Tx: txID, BlockHeight: height}},
		}
	}
	first := observation(100)
	stored := 0
	first.TxArray[0].OnObservedTxStored = func() { stored++ }
	require.NoError(t, observer.processObservedTx(first))
	require.Equal(t, 1, stored)
	require.NoError(t, storage.Close()) // crash after ack, before chain confirmation

	storage = open()
	observer.storage = storage
	restored, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1)
	observer.onDeck[TxInKey(restored[0])] = restored[0]
	storage.beforeSyncTxWrite = func() error { return errors.New("injected duplicate fsync failure") }
	rejected := 0
	duplicate := observation(100)
	duplicate.TxArray[0].OnObservedTxRejected = func(error) { rejected++ }
	require.ErrorContains(t, observer.processObservedTx(duplicate), "injected duplicate fsync failure")
	require.Equal(t, 1, rejected)
	storage.beforeSyncTxWrite = nil
	require.NoError(t, observer.processObservedTx(observation(100)))
	restored, err = storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, restored, 1)
	require.Len(t, restored[0].TxArray, 1)

	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID(txID)}
	require.NoError(t, observer.filterErrataTx(types.ErrataBlock{
		Height: 100, DeliveryID: "reorg-100", Txs: []types.ErrataTx{{Chain: errata.Chain, TxID: errata.Id}},
	}))
	require.Empty(t, observer.onDeck)
	require.NoError(t, storage.Close()) // crash after atomic tombstone and cleanup

	storage = open()
	defer func() { require.NoError(t, storage.Close()) }()
	observer.storage = storage
	restored, err = storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Empty(t, restored)
	require.NoError(t, observer.processObservedTx(observation(100)))
	require.Empty(t, observer.onDeck, "orphaned view must not reappear")
	require.NoError(t, observer.processObservedTx(observation(101)))
	require.Len(t, observer.onDeck, 1, "re-mined view must remain eligible")
	require.NoError(t, storage.applyCommittedErrata(errata, nil))
	require.NoError(t, storage.recordErrataVoterResult(errata, true, nil))
	restored, err = storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Empty(t, restored, "permanent reversion blocks every height on restart")
}
