package observer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	check "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestXMRStartupMissingClientKeepsOtherChainsAvailable(t *testing.T) {
	o, storage := newErrataOrderingObserver(t)
	er := common.ErrataTx{Chain: common.XMRChain, Id: "historical-reversal"}
	require.NoError(t, storage.applyCommittedErrata(er, nil))
	require.NoError(t, storage.recordErrataVoterResult(er, true, nil))
	before, err := storage.db.Get([]byte(errataTombstoneKey(er.Chain, er.Id.String())), nil)
	require.NoError(t, err)
	btc := &errataStartOrderChain{}
	o.chains = map[common.Chain]chainclients.ChainClient{common.BTCChain: btc}
	o.permanentErrataQuarantineFn = nil

	// The old startup path fails here before starting even BTC. The new path
	// keeps the same durable quarantine evidence but defers only its consumer.
	require.ErrorContains(t, o.replayPermanentErrataQuarantines(), "XMR chain client is unavailable")
	deferred, err := o.startRestoredChainClients()
	require.NoError(t, err)
	require.True(t, deferred)
	require.True(t, o.IsChainClientStarted(common.BTCChain))
	require.False(t, o.IsChainClientStarted(common.XMRChain))
	o.wg = &sync.WaitGroup{}
	o.startXMRErrataMaintenance(context.Background(), deferred) // No configured XMR client: no worker.
	o.wg.Wait()
	after, err := storage.db.Get([]byte(errataTombstoneKey(er.Chain, er.Id.String())), nil)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestXMRStartupWithoutHistoryStartsImmediately(t *testing.T) {
	o, _ := newErrataOrderingObserver(t)
	xmr, btc := &errataStartOrderChain{}, &errataStartOrderChain{}
	o.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: xmr, common.BTCChain: btc}
	o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		t.Fatal("clean startup should not need quarantine")
		return nil
	}
	deferred, err := o.startRestoredChainClients()
	require.NoError(t, err)
	require.False(t, deferred)
	require.True(t, o.IsChainClientStarted(common.XMRChain))
	require.Equal(t, 1, btc.starts)
	o.stopStartedChainClients()
	require.False(t, o.IsChainClientStarted(common.XMRChain))
}

func TestXMRStartupRestoresCompletedQuarantineAfterRestart(t *testing.T) {
	path := t.TempDir()
	storage, err := NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	er := common.ErrataTx{Chain: common.XMRChain, Id: "completed-before-restart"}
	require.NoError(t, storage.applyCommittedErrata(er, nil))
	require.NoError(t, storage.recordErrataVoterResult(er, true, nil))
	require.NoError(t, storage.Close())
	storage, err = NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	xmr, btc := &errataStartOrderChain{}, &errataStartOrderChain{}
	o := &Observer{
		logger: zerolog.Nop(), storage: storage, lock: &sync.Mutex{},
		onDeck: make(map[txInKey]*types.TxIn), stopChan: make(chan struct{}),
		chains: map[common.Chain]chainclients.ChainClient{common.XMRChain: xmr, common.BTCChain: btc},
	}
	runtimeErrata := common.ErrataTx{Chain: common.XMRChain, Id: "reverted-after-startup"}
	calls := make(map[common.TxID]int)
	o.permanentErrataBroadcastBlockFn = func(common.ErrataTx) error { return nil }
	o.permanentErrataQuarantineFn = func(restored common.ErrataTx, _ int64) error {
		calls[restored.Id]++
		if restored == er {
			require.False(t, o.IsChainClientStarted(common.XMRChain))
		} else {
			require.Equal(t, runtimeErrata, restored)
			require.True(t, o.IsChainClientStarted(common.XMRChain))
		}
		require.True(t, o.IsChainClientStarted(common.BTCChain))
		return nil
	}
	deferred, err := o.startRestoredChainClients()
	require.NoError(t, err)
	require.True(t, deferred)
	o.wg = &sync.WaitGroup{}
	o.startXMRErrataMaintenance(context.Background(), deferred)
	t.Cleanup(func() { o.stopStartedChainClients(); o.wg.Wait() })
	require.Eventually(t, func() bool { return o.IsChainClientStarted(common.XMRChain) }, time.Second, time.Millisecond)
	// The same worker must stay alive for later receipts without replaying
	// completed startup history on every maintenance pass.
	require.NoError(t, o.handleErrataTxCommitted(runtimeErrata, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	require.Eventually(t, func() bool {
		tombstone, readErr := storage.getErrataTombstone(runtimeErrata.Chain, runtimeErrata.Id.String())
		return readErr == nil && tombstone != nil && tombstone.isPermanentlyReverted()
	}, 2*constants.ThorchainBlockTime, time.Millisecond)
	o.stopStartedChainClients()
	o.wg.Wait()
	require.Equal(t, map[common.TxID]int{er.Id: 1, runtimeErrata.Id: 1}, calls)
	require.Equal(t, 1, xmr.starts)
}

func TestXMRStartupSharedStorageCorruptionRemainsFatal(t *testing.T) {
	for _, value := range []string{"{broken", `{"chain":"XMR","tx_id":"different-key","generation":1}`} {
		t.Run(value, func(t *testing.T) {
			o, storage := newErrataOrderingObserver(t)
			btc := &errataStartOrderChain{}
			o.chains = map[common.Chain]chainclients.ChainClient{common.BTCChain: btc}
			require.NoError(t, storage.db.Put([]byte(errataTombstoneKey(common.XMRChain, "corrupt")), []byte(value), nil))
			require.Error(t, o.Start(context.Background()))
			require.Zero(t, btc.starts)
		})
	}
}

func TestXMRStartupRetriesReplayAndChecksNewReceipts(t *testing.T) {
	o, storage := newErrataOrderingObserver(t)
	o.stopChan = make(chan struct{})
	completed := common.ErrataTx{Chain: common.XMRChain, Id: "completed"}
	first := common.ErrataTx{Chain: common.XMRChain, Id: "first-pending"}
	late := common.ErrataTx{Chain: common.XMRChain, Id: "late-pending"}
	require.NoError(t, storage.applyCommittedErrata(completed, nil))
	require.NoError(t, storage.recordErrataVoterResult(completed, true, nil))
	require.NoError(t, storage.applyCommittedErrata(first, nil))
	xmr, btc := &errataStartOrderChain{}, &errataStartOrderChain{}
	o.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: xmr, common.BTCChain: btc}
	deferred, err := o.startRestoredChainClients()
	require.NoError(t, err)
	require.True(t, deferred)
	require.True(t, o.IsChainClientStarted(common.BTCChain))
	require.False(t, o.IsChainClientStarted(common.XMRChain))

	firstFailure := make(chan struct{})
	replayed, applied := 0, 0
	o.permanentErrataQuarantineFn = func(er common.ErrataTx, _ int64) error {
		if o.IsChainClientStarted(common.XMRChain) {
			return errors.New("XMR started before quarantine completed")
		}
		if er == completed {
			replayed++
			if replayed == 1 {
				close(firstFailure)
				return errors.New("temporary quarantine failure")
			}
		} else {
			applied++
			if er == first {
				// Not in the reconciliation snapshot: the final readiness check
				// must keep XMR stopped for another pass.
				return storage.applyCommittedErrata(late, nil)
			}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); o.maintainXMRErrata(ctx, deferred) }()
	t.Cleanup(func() { cancel(); <-done; o.stopStartedChainClients() })
	select {
	case <-firstFailure:
	case <-time.After(time.Second):
		t.Fatal("quarantine replay did not start")
	}
	require.False(t, o.IsChainClientStarted(common.XMRChain))
	require.Eventually(t, func() bool { return o.IsChainClientStarted(common.XMRChain) },
		4*constants.ThorchainBlockTime, time.Millisecond)
	cancel()
	<-done
	require.True(t, o.IsChainClientStarted(common.XMRChain))
	require.Equal(t, 2, replayed, "successful historical replay should not repeat on every pending-receipt pass")
	require.Equal(t, 2, applied, "new receipt must be applied before readiness")
	require.Equal(t, 1, xmr.starts)
	for _, er := range []common.ErrataTx{completed, first, late} {
		tombstone, readErr := storage.getErrataTombstone(er.Chain, er.Id.String())
		require.NoError(t, readErr)
		require.True(t, tombstone.isPermanentlyReverted())
	}
}

func (s *ObserverSuite) TestXMRStartupJoinsQuarantineBeforeStorageClose(c *check.C) {
	pkm, err := pubkeymanager.NewPubKeyManager(s.bridge, s.metrics)
	c.Assert(err, check.IsNil)
	xmr := &errataStartOrderChain{}
	guardedXMR := &spentRefsGuardChain{ChainClient: xmr}
	gossip := &AttestationGossip{logger: zerolog.Nop()}
	o, err := NewObserver(pkm, map[common.Chain]chainclients.ChainClient{common.XMRChain: guardedXMR},
		s.bridge, s.metrics, c.MkDir(), metrics.NewTssKeysignMetricMgr(), gossip, "")
	c.Assert(err, check.IsNil)
	er := common.ErrataTx{Chain: common.XMRChain, Id: "startup-stop"}
	c.Assert(o.storage.applyCommittedErrata(er, nil), check.IsNil)
	c.Assert(o.storage.recordErrataVoterResult(er, true, nil), check.IsNil)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		close(entered)
		<-release
		_, readErr := o.storage.getErrataTombstones()
		return readErr
	}
	o.startXMRErrataMaintenance(context.Background(), true)
	select {
	case <-entered:
	case <-time.After(time.Second):
		c.Fatal("quarantine replay did not start")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- o.Stop() }()
	<-o.stopChan
	select {
	case <-stopDone:
		c.Fatal("Stop closed storage before quarantine finished")
	default:
	}
	_, err = o.storage.getErrataTombstones()
	c.Assert(err, check.IsNil)
	releaseOnce.Do(func() { close(release) })
	c.Assert(<-stopDone, check.IsNil)
	c.Assert(xmr.starts, check.Equals, 0)
	_, err = o.storage.getErrataTombstones()
	c.Assert(err, check.NotNil)
}
