package observer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	check "gopkg.in/check.v1"
)

func TestXMRErrataQuarantineDoesNotBlockOtherChainConsumer(t *testing.T) {
	o, storage := newErrataOrderingObserver(t)
	o.stopChan = make(chan struct{})
	o.globalErrataQueue = make(chan types.ErrataBlock)
	o.errataAttestFn = func(context.Context, common.ErrataTx) error { return nil }
	reverted := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("slow-quarantine")}
	require.NoError(t, storage.recordLocalErrataCommitted(reverted, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))

	entered, release := make(chan struct{}), make(chan struct{})
	quarantineDone, consumerDone := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		close(entered)
		<-release
		return nil
	}
	// The cleanup below owns cancel and runs even after a fatal assertion.
	ctx, cancel := context.WithCancel(context.Background())
	// Join both production paths before the fixture closes Observer storage,
	// including when the consumer regression fails on the synchronous base.
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		close(o.stopChan)
		cancel()
		for _, done := range []chan struct{}{quarantineDone, consumerDone} {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("errata processing did not stop after quarantine was released")
			}
		}
	})
	go func() {
		defer close(quarantineDone)
		_, _ = o.reconcileCommittedXMRErrata()
	}()
	go func() {
		defer close(consumerDone)
		o.processErrataTx(ctx)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("XMR quarantine did not start")
	}

	deliver := func(txID string) {
		t.Helper()
		stored := make(chan error, 1)
		block := types.ErrataBlock{
			Height: 100,
			Txs:    []types.ErrataTx{{Chain: common.BTCChain, TxID: common.TxID(txID)}},
			OnStored: func() {
				tombstone, err := storage.getErrataTombstone(common.BTCChain, txID)
				if err == nil && tombstone == nil {
					err = errors.New("consumer acknowledged errata before durable acceptance")
				}
				stored <- err
			},
			OnRejected: func(err error) { stored <- err },
		}
		select {
		case o.globalErrataQueue <- block:
		case <-time.After(2 * time.Second):
			t.Fatal("shared errata consumer stopped accepting BTC blocks behind XMR quarantine")
		}
		select {
		case err := <-stored:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("shared errata consumer did not durably acknowledge BTC errata")
		}
	}
	// The first block causes a retry pass. A second unbuffered delivery proves
	// the shared consumer returned to its queue while XMR quarantine is slow.
	deliver("btc-first")
	deliver("btc-second")
	select {
	case <-quarantineDone:
		t.Fatal("BTC delivery was only tested after XMR quarantine completed")
	default:
	}
}

func TestXMRReadinessWaitsForEveryReceiptIncludingNewPassArrival(t *testing.T) {
	o, reopen := newTerminalErrataObserver(t)
	first := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("first")}
	second := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("second")}
	third := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("third")}
	require.NoError(t, o.handleErrataTxCommitted(first, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	reopen()
	xmr, btc := &errataStartOrderChain{}, &errataStartOrderChain{}
	o.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: xmr, common.BTCChain: btc}
	deferred, startupErr := o.startRestoredChainClients()
	require.NoError(t, startupErr)
	require.True(t, deferred)
	require.True(t, o.IsChainClientStarted(common.BTCChain))
	require.False(t, o.IsChainClientStarted(common.XMRChain))
	calls := 0
	o.permanentErrataQuarantineFn = func(er common.ErrataTx, _ int64) error {
		calls++
		require.False(t, o.IsChainClientStarted(common.XMRChain))
		if er == first {
			// New receipts were not present in the maintenance pass snapshot.
			for _, incoming := range []common.ErrataTx{second, third} {
				if err := o.handleErrataTxCommitted(incoming, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED); err != nil {
					return err
				}
			}
		}
		return nil
	}
	o.maintainXMRErrataOnce(context.Background())
	require.Equal(t, 1, calls)
	require.False(t, o.IsChainClientStarted(common.XMRChain), "successful stale snapshot cannot open readiness")
	o.permanentErrataQuarantineFn = func(er common.ErrataTx, _ int64) error {
		calls++
		if er == second {
			return errors.New("signed store unavailable")
		}
		return nil
	}
	o.maintainXMRErrataOnce(context.Background())
	require.False(t, o.IsChainClientStarted(common.XMRChain), "a later successful record cannot mask an earlier failure")
	require.Equal(t, 3, calls)
	reopen()
	o.permanentErrataQuarantineFn = func(er common.ErrataTx, _ int64) error {
		calls++
		require.Equal(t, second, er, "restart must only retry the incomplete record")
		return nil
	}
	o.maintainXMRErrataOnce(context.Background())
	require.True(t, o.IsChainClientStarted(common.XMRChain))
	require.Equal(t, 1, xmr.starts)
	require.Equal(t, 1, btc.starts)
	for range 3 {
		o.maintainXMRErrataOnce(context.Background())
	}
	require.Equal(t, 4, calls, "completed records perform no repeated quarantine")
	tombstones, err := o.storage.getErrataTombstones()
	require.NoError(t, err)
	require.Len(t, tombstones, 3)
	for _, tombstone := range tombstones {
		require.True(t, tombstone.isPermanentlyReverted())
	}
	o.stopStartedChainClients()
	require.False(t, o.IsChainClientStarted(common.XMRChain))
	o.maintainXMRErrataOnce(context.Background())
	require.Equal(t, 1, xmr.starts, "stopping cannot restart a ready client")
}

func TestXMRErrataStorageWritersOwnReadModifyWriteLock(t *testing.T) {
	for _, writer := range []string{"legacy-receipt", "receipt", "completion"} {
		t.Run(writer, func(t *testing.T) {
			o, _ := newTerminalErrataObserver(t)
			er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("writer")}
			require.NoError(t, o.storage.recordLocalErrataCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
			o.storage.beforeErrataWrite = func() error {
				if o.storage.errataMu.TryLock() {
					o.storage.errataMu.Unlock()
					return errors.New("writer released storage mutex before durable batch")
				}
				return nil
			}
			var err error
			switch writer {
			case "legacy-receipt":
				err = o.storage.recordErrataCommitted(er, 0)
			case "receipt":
				err = o.storage.recordLocalErrataCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			case "completion":
				err = o.storage.recordErrataVoterResult(er, true, nil)
			}
			require.NoError(t, err)
		})
	}
}

func TestXMRStopLeavesRemainingQuarantineForRestart(t *testing.T) {
	o, reopen := newTerminalErrataObserver(t)
	o.stopChan = make(chan struct{})
	first := common.ErrataTx{Chain: common.XMRChain, Id: "a-first"}
	second := common.ErrataTx{Chain: common.XMRChain, Id: "b-second"}
	for _, er := range []common.ErrataTx{first, second} {
		require.NoError(t, o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		calls++
		if calls == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	go func() { defer close(done); o.maintainXMRErrataOnce(context.Background()) }()
	<-entered
	o.stopStartedChainClients()
	close(release)
	<-done
	require.Equal(t, 1, calls, "shutdown must not begin the next quarantine")
	reopen()
	pending, err := o.storage.getUnresolvedCommittedXMRErrata()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, second.Id, pending[0].TxID)
	// Model a new process: the first record completed, the untouched second
	// receipt remains REVERTED and blocks admission until its retry finishes.
	o.stopChan = make(chan struct{})
	o.stopping = false
	o.permanentErrataQuarantineFn = func(er common.ErrataTx, _ int64) error {
		require.Equal(t, second, er)
		calls++
		return nil
	}
	o.maintainXMRErrataOnce(context.Background())
	require.Equal(t, 2, calls)
	pending, err = o.storage.getUnresolvedCommittedXMRErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestXMRPeriodicRetryStartsClientAfterFailure(t *testing.T) {
	o, _ := newTerminalErrataObserver(t)
	o.stopChan, o.wg = make(chan struct{}), &sync.WaitGroup{}
	xmr := &errataStartOrderChain{}
	o.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: xmr}
	er := common.ErrataTx{Chain: common.XMRChain, Id: "periodic-retry"}
	require.NoError(t, o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
	deferred, startupErr := o.startRestoredChainClients()
	require.NoError(t, startupErr)
	require.True(t, deferred)
	firstPass := make(chan struct{})
	calls := 0
	o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		calls++
		if calls == 1 {
			close(firstPass)
			return errors.New("temporary quarantine failure")
		}
		return nil
	}
	started := time.Now()
	o.startXMRErrataMaintenance(context.Background(), deferred)
	t.Cleanup(func() { o.stopStartedChainClients(); o.wg.Wait() })
	<-firstPass
	require.False(t, o.IsChainClientStarted(common.XMRChain))
	require.Eventually(t, func() bool { return o.IsChainClientStarted(common.XMRChain) }, 2*constants.ThorchainBlockTime, 5*time.Millisecond)
	require.GreaterOrEqual(t, time.Since(started), constants.ThorchainBlockTime, "retry must not restore one-second history scans")
	o.stopStartedChainClients()
	o.wg.Wait()
	require.Equal(t, 2, calls)
	require.Equal(t, 1, xmr.starts)
	tombstone, err := o.storage.getErrataTombstone(er.Chain, er.Id.String())
	require.NoError(t, err)
	require.True(t, tombstone.isPermanentlyReverted())
}

type xmrStopTestChain struct {
	mockChainClient
	stopped chan struct{}
	onStop  func()
}

func (c *xmrStopTestChain) Stop() {
	if c.onStop != nil {
		c.onStop()
	}
	close(c.stopped)
}

func (s *ObserverSuite) TestXMRMaintenanceDeliveryAndStopOwnership(c *check.C) {
	pkm, err := pubkeymanager.NewPubKeyManager(s.bridge, s.metrics)
	c.Assert(err, check.IsNil)
	chain := &xmrStopTestChain{stopped: make(chan struct{})}
	btc := &xmrStopTestChain{stopped: make(chan struct{})}
	guardedXMR := &spentRefsGuardChain{ChainClient: chain}
	gossip := &AttestationGossip{logger: zerolog.Nop()}
	o, err := NewObserver(pkm, map[common.Chain]chainclients.ChainClient{
		common.XMRChain: guardedXMR, common.BTCChain: btc,
	},
		s.bridge, s.metrics, c.MkDir(), metrics.NewTssKeysignMetricMgr(), gossip, "")
	c.Assert(err, check.IsNil)
	deferred, startupErr := o.startRestoredChainClients()
	c.Assert(startupErr, check.IsNil)
	c.Assert(deferred, check.Equals, false)
	c.Assert(o.IsChainClientStarted(common.XMRChain), check.Equals, true)
	er := common.ErrataTx{Chain: common.XMRChain, Id: "maintenance-stop"}
	completedAtStop := make(chan bool, 1)
	chain.onStop = func() {
		tombstone, readErr := o.storage.getErrataTombstone(er.Chain, er.Id.String())
		completedAtStop <- readErr == nil && tombstone != nil && tombstone.isPermanentlyReverted()
	}
	o.permanentErrataBroadcastBlockFn = func(common.ErrataTx) error { return nil }
	c.Assert(o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED), check.IsNil)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
		close(entered)
		<-release
		return nil
	}
	o.errataAttestFn = func(context.Context, common.ErrataTx) error { return nil }
	o.startXMRErrataMaintenance(context.Background(), deferred)
	<-entered
	// The real scanner consumer and durable retry remain independent of broad
	// quarantine and do not retain errataAttestMu behind maintenance.
	consumerDone := make(chan struct{})
	go func() { defer close(consumerDone); o.processErrataTx(context.Background()) }()
	for _, id := range []common.TxID{"second-delivery", "third-delivery"} {
		block := types.ErrataBlock{Height: 200, Txs: []types.ErrataTx{{Chain: common.XMRChain, TxID: id}}, Persisted: make(chan error, 1)}
		select {
		case o.globalErrataQueue <- block:
		case <-time.After(time.Second):
			c.Fatal("quarantine blocked global errata consumer")
		}
		select {
		case persistErr := <-block.Persisted:
			c.Assert(persistErr, check.IsNil)
		case <-time.After(time.Second):
			c.Fatal("quarantine blocked durable scanner acknowledgement")
		}
		record, found, readErr := o.storage.GetPendingErrataTx(block.Txs[0])
		c.Assert(readErr, check.IsNil)
		c.Assert(found && record.DeliverySeen && record.CleanupComplete, check.Equals, true)
	}
	stopDone := make(chan struct{})
	var stopErr error
	go func() { defer close(stopDone); stopErr = o.Stop() }()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		<-stopDone
		<-consumerDone
	}()
	<-o.stopChan
	c.Assert(o.IsChainClientStarted(common.XMRChain), check.Equals, false)
	select {
	case <-btc.stopped:
	case <-time.After(time.Second):
		c.Fatal("XMR maintenance delayed another chain's stop")
	}
	select {
	case <-chain.stopped:
		c.Error("XMR client stopped before owned maintenance completed")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-stopDone:
		c.Fatal("Stop returned before owned maintenance completed")
	default:
	}
	// Both the XMR client and Observer storage must outlive maintenance,
	// regardless of which resources the client releases during its Stop.
	pending, err := o.storage.getUnresolvedCommittedXMRErrata()
	c.Assert(err, check.IsNil)
	c.Assert(pending, check.HasLen, 1)
	releaseOnce.Do(func() { close(release) })
	<-stopDone
	c.Assert(stopErr, check.IsNil)
	c.Assert(<-completedAtStop, check.Equals, true,
		check.Commentf("XMR client Stop must see the completed durable quarantine"))
	select {
	case <-chain.stopped:
	default:
		c.Fatal("Observer Stop returned without stopping the XMR client")
	}
	_, err = o.storage.getUnresolvedCommittedXMRErrata()
	c.Assert(err, check.NotNil, check.Commentf("Observer storage closes only after maintenance joins"))
}
