package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestClientPermanentErrataQuarantinesSignedArtifactThenScannerTxID(t *testing.T) {
	const txID = "abababababababababababababababababababababababababababababababab"
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan("logical-permanent-errata")
	if err := store.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(plan, txID, standalonePayload(txID)); err != nil {
		t.Fatal(err)
	}

	errata := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID(txID)}
	client := &Client{signedTxPayloadStore: store}
	if err := client.QuarantinePermanentErrata(errata, 77); err == nil {
		t.Fatal("missing scanner quarantine did not fail closed")
	}
	if _, found, lookupErr := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(lookupErr, errXMRSigningClaimQuarantined) || found {
		t.Fatalf("first phase left signed transaction broadcastable: found=%t err=%v", found, lookupErr)
	}

	reorgStore := NewReorgStore(store.db)
	client.scanner = &Scanner{reorgStore: reorgStore}
	if err := client.QuarantinePermanentErrata(errata, 77); err != nil {
		t.Fatalf("retry permanent errata quarantine: %v", err)
	}
	reverted, err := reorgStore.IsRevertedTx(txID)
	if err != nil || !reverted {
		t.Fatalf("scanner transaction was not permanently quarantined: reverted=%t err=%v", reverted, err)
	}

	// Both phases are durable and idempotent, which is required for Observer's
	// unconditional startup replay of old and partially applied tombstones.
	restartedSignedStore := newSignedTxPayloadStore(store.db)
	restartedReorgStore := NewReorgStore(store.db)
	restarted := &Client{
		signedTxPayloadStore: restartedSignedStore,
		scanner:              &Scanner{reorgStore: restartedReorgStore},
	}
	if err = restarted.QuarantinePermanentErrata(errata, 77); err != nil {
		t.Fatalf("restart replay permanent errata quarantine: %v", err)
	}
	if _, found, lookupErr := restartedSignedStore.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(lookupErr, errXMRSigningClaimQuarantined) || found {
		t.Fatalf("restart reopened signed transaction: found=%t err=%v", found, lookupErr)
	}
	reverted, err = restartedReorgStore.IsRevertedTx(txID)
	if err != nil || !reverted {
		t.Fatalf("restart reopened scanner transaction: reverted=%t err=%v", reverted, err)
	}
}

type errataClientFixture struct {
	client         *Client
	tx             stypes.TxOutItem
	signed         []byte
	payload        signedTxPayload
	signingCalls   atomic.Int32
	rpcCalls       atomic.Int32
	broadcastCalls atomic.Int32
	dbPath         string
	db             *leveldb.DB
}

// Count calls from the production SignTx/BroadcastTx paths. Only the external
// FROST response and HTTP sidecar are controlled; quarantine and stores are real.
func newErrataClientFixture(t *testing.T, beforeBroadcastReply func()) *errataClientFixture {
	t.Helper()
	f := &errataClientFixture{dbPath: t.TempDir()}
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			f.signingCalls.Add(1)
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	client, tx, signingServer, _ := newFrostSignTxCleanupFixture(t, tssServer, 0)
	signingHandler := signingServer.Config.Handler
	signingServer.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.rpcCalls.Add(1)
		if r.URL.Path != "/v1/monero/broadcast" {
			signingHandler.ServeHTTP(w, r)
			return
		}
		f.broadcastCalls.Add(1)
		var req sidecar.BroadcastReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if beforeBroadcastReply != nil {
			beforeBroadcastReply()
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(sidecar.BroadcastResp{TxHash: req.TxHash, TxKey: req.TxKey}))
	}))
	t.Cleanup(server.Close)
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	f.db, err = leveldb.OpenFile(f.dbPath, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.db.Close()) })
	client.sidecar = signer
	client.signedTxPayloadStore = newSignedTxPayloadStore(f.db)
	f.client, f.tx = client, tx
	f.signed, _, _, err = client.SignTx(tx, 12345)
	require.NoError(t, err)
	require.NotEmpty(t, f.signed)
	require.NoError(t, json.Unmarshal(f.signed, &f.payload))
	require.Equal(t, int32(1), f.signingCalls.Load(), "positive control must reach FROST once")
	require.Positive(t, f.rpcCalls.Load(), "positive control must reach the HTTP sidecar")
	return f
}

func (f *errataClientFixture) reopen(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.Close())
	db, err := leveldb.OpenFile(f.dbPath, nil)
	require.NoError(t, err)
	f.db = db
	previous := f.client
	f.client = &Client{
		logger:               previous.logger,
		chain:                previous.chain,
		localNodePubKey:      previous.localNodePubKey,
		sidecar:              previous.sidecar,
		tssServer:            previous.tssServer,
		thorchainBridge:      previous.thorchainBridge,
		vaultAddressMap:      previous.vaultAddressMap,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newSignedTxPayloadStore(db),
		scanner:              &Scanner{reorgStore: NewReorgStore(db)},
	}
}

func (f *errataClientFixture) requireBlocked(t *testing.T) {
	t.Helper()
	signingBefore, rpcBefore := f.signingCalls.Load(), f.rpcCalls.Load()
	broadcastBefore := f.broadcastCalls.Load()
	signed, checkpoint, observation, err := f.client.SignTx(f.tx, 12346)
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	require.Empty(t, signed)
	require.Empty(t, checkpoint)
	require.Nil(t, observation)
	hash, err := f.client.BroadcastTx(f.tx, f.signed)
	require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	require.Empty(t, hash)
	require.Equal(t, signingBefore, f.signingCalls.Load(), "fenced retry must not enter FROST")
	require.Equal(t, rpcBefore, f.rpcCalls.Load(), "fenced retry must not reach the sidecar")
	require.Equal(t, broadcastBefore, f.broadcastCalls.Load(), "fenced bytes must not reach broadcast RPC")
}

func TestPermanentErrataFenceBlocksClientRPCsAcrossQuarantineRetry(t *testing.T) {
	f := newErrataClientFixture(t, nil)
	// The same bytes must be broadcastable before installing the fence.
	hash, err := f.client.BroadcastTx(f.tx, f.signed)
	require.NoError(t, err)
	require.Equal(t, f.payload.TxHash, hash)
	require.Equal(t, int32(1), f.broadcastCalls.Load())
	er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID(hash)}
	require.NoError(t, f.client.BlockPermanentErrataBroadcast(er))
	f.requireBlocked(t)

	// A broken unrelated claim fails the broad scan after the exact hash fence
	// is already installed. Its failure must not authorize signing or broadcast.
	badHistory := []byte(signedTxPayloadClaimKey("unrelated-invalid-history"))
	require.NoError(t, f.db.Put(badHistory, []byte("bad-json"), nil))
	require.ErrorContains(t, f.client.QuarantinePermanentErrata(er, 100), "signed transaction")
	f.requireBlocked(t)
	f.reopen(t)
	f.requireBlocked(t)

	require.NoError(t, f.db.Delete(badHistory, nil))
	scannerDB, err := leveldb.OpenFile(t.TempDir(), nil)
	require.NoError(t, err)
	require.NoError(t, scannerDB.Close())
	f.client.scanner = &Scanner{reorgStore: NewReorgStore(scannerDB)}
	require.ErrorContains(t, f.client.QuarantinePermanentErrata(er, 100), "scanner transaction")
	f.requireBlocked(t)

	f.client.scanner = &Scanner{reorgStore: NewReorgStore(f.db)}
	require.NoError(t, f.client.QuarantinePermanentErrata(er, 100))
	f.reopen(t)
	reverted, err := f.client.scanner.reorgStore.IsRevertedTx(hash)
	require.NoError(t, err)
	require.True(t, reverted)
	require.NoError(t, f.client.QuarantinePermanentErrata(er, 100), "completed quarantine is idempotent")
	f.requireBlocked(t)
	require.Equal(t, int32(1), f.signingCalls.Load())
	require.Equal(t, int32(1), f.broadcastCalls.Load())
}

func TestPermanentErrataFenceRejectsInFlightClientBroadcastCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	f := newErrataClientFixture(t, func() {
		close(entered)
		<-release
	})
	// Release the HTTP handler even when an assertion fails before the join.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	done := make(chan error, 1)
	go func() {
		_, err := f.client.BroadcastTx(f.tx, f.signed)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("production broadcast did not reach the sidecar")
	}
	er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID(f.payload.TxHash)}
	require.NoError(t, f.client.BlockPermanentErrataBroadcast(er))
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		require.ErrorIs(t, err, errXMRSigningClaimQuarantined)
	case <-time.After(time.Second):
		t.Fatal("in-flight broadcast did not finish")
	}
	// The already-authorized RPC cannot be recalled, but its successful reply
	// must not mark the signer cache or permit any subsequent RPC.
	require.Equal(t, int32(1), f.broadcastCalls.Load())
	require.False(t, f.client.signerCacheManager.HasSigned(f.payload.ClaimCacheHash))
	f.requireBlocked(t)
	f.reopen(t)
	f.requireBlocked(t)
}
