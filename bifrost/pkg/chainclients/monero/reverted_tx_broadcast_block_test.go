package monero

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestRevertedTxBroadcastBlockSurvivesPartialQuarantineAndRestart(t *testing.T) {
	path := t.TempDir()
	database, err := leveldb.OpenFile(path, nil)
	require.NoError(t, err)
	store := newSignedTxPayloadStore(database)
	t.Cleanup(func() { require.NoError(t, store.db.Close()) })
	reopen := func() {
		var reopenErr error
		database, reopenErr = leveldb.OpenFile(path, nil)
		require.NoError(t, reopenErr)
		store = newSignedTxPayloadStore(database)
	}
	plan := standaloneClaimPlan("quarantined-owner")
	const txHash = "abababababababababababababababababababababababababababababababab"
	er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID(txHash)}
	require.NoError(t, store.Reserve(plan))
	require.NoError(t, store.Set(plan, txHash, standalonePayload(txHash)))
	// A failed exact write must return an error, not claim a successful fence.
	require.NoError(t, store.db.Close())
	require.Error(t, store.BlockRevertedTxHash(txHash))
	reopen()
	hasBlock, err := store.db.Has([]byte(signedTxQuarantinedTxKey(txHash)), nil)
	require.NoError(t, err)
	require.False(t, hasBlock)
	// Poison unrelated historical data: full identity quarantine must scan it,
	// but the exact block must neither scan nor depend on it.
	badHistory := []byte(signedTxPayloadClaimKey("unrelated-broken-history"))
	require.NoError(t, store.db.Put(badHistory, []byte("bad-json"), nil))
	client := &Client{signedTxPayloadStore: store}
	require.NoError(t, client.BlockPermanentErrataBroadcast(er))
	require.Error(t, client.QuarantinePermanentErrata(er, 100))
	require.NoError(t, store.db.Close())
	reopen()
	assertBlocked := func() {
		payload, found, checkErr := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
		require.ErrorIs(t, checkErr, errXMRSigningClaimQuarantined)
		require.False(t, found)
		require.Empty(t, payload)
		called := false
		_, checkErr = store.WithBroadcastAuthorization(plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
			txHash, plan.MsgID, plan.PlanHash, func() (string, error) { called = true; return txHash, nil })
		require.ErrorIs(t, checkErr, errXMRSigningClaimQuarantined)
		require.False(t, called)
		hasPayload, checkErr := store.db.Has([]byte(signedTxPayloadTxKey(txHash)), nil)
		require.NoError(t, checkErr)
		require.True(t, hasPayload, "quarantine retains the signed artifact index")
	}
	assertBlocked()
	require.NoError(t, store.db.Delete(badHistory, nil))
	client.signedTxPayloadStore = store
	require.ErrorContains(t, client.QuarantinePermanentErrata(er, 100), "scanner")
	assertBlocked()
	failedScannerDB, err := leveldb.OpenFile(t.TempDir(), nil)
	require.NoError(t, err)
	client.scanner = &Scanner{reorgStore: NewReorgStore(failedScannerDB)}
	require.NoError(t, failedScannerDB.Close())
	require.ErrorContains(t, client.QuarantinePermanentErrata(er, 100), "quarantine reverted xmr transactions")
	assertBlocked()
	client.scanner = &Scanner{reorgStore: NewReorgStore(store.db)}
	require.NoError(t, client.QuarantinePermanentErrata(er, 100))
	require.NoError(t, store.db.Close())
	reopen()
	assertBlocked()
	reverted, err := NewReorgStore(store.db).IsRevertedTx(txHash)
	require.NoError(t, err)
	require.True(t, reverted)
	require.NoError(t, store.BlockRevertedTxHash(txHash), "exact blocking remains idempotent after completion")
}

func TestRevertedTxBroadcastBlockRejectsUnavailableStoreAndInvalidRequests(t *testing.T) {
	client := &Client{}
	require.Error(t, client.BlockPermanentErrataBroadcast(common.ErrataTx{Chain: common.XMRChain, Id: "tx"}))
	client.signedTxPayloadStore = newStandaloneSignedStore(t)
	require.Error(t, client.BlockPermanentErrataBroadcast(common.ErrataTx{Chain: common.BTCChain, Id: "tx"}))
	require.Error(t, client.BlockPermanentErrataBroadcast(common.ErrataTx{Chain: common.XMRChain}))
	require.Error(t, client.signedTxPayloadStore.BlockRevertedTxHash("  "))
}

func TestRevertedTxBroadcastBlockRejectsLateBroadcastCompletion(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "inflight-broadcast", "exact-tx")
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.WithBroadcastAuthorization(plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
			"exact-tx", plan.MsgID, plan.PlanHash, func() (string, error) {
				close(entered)
				<-release
				return "exact-tx", nil
			})
		done <- err
	}()
	<-entered
	err := store.BlockRevertedTxHash("exact-tx")
	close(release)
	require.NoError(t, err)
	require.ErrorIs(t, <-done, errXMRSigningClaimQuarantined, "late broadcast completion cannot reopen the exact fence")
	// The already-authorized network call is not cancelled by this fence;
	// subsequent authorization and local completion must reject it.
	called := false
	_, err = store.WithBroadcastAuthorization(plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", plan.MsgID, plan.PlanHash, func() (string, error) { called = true; return "exact-tx", nil })
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	require.False(t, called)
}
