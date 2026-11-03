package utxo

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/prometheus/client_golang/prometheus/testutil"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type dashHaltBridge struct {
	thorclient.ThorchainBridge
	getHeight func() (int64, error)
	getMimir  func(string) (int64, error)
	reads     []string
}

func (b *dashHaltBridge) GetBlockHeight() (int64, error) {
	b.reads = append(b.reads, "height")
	if b.getHeight != nil {
		return b.getHeight()
	}
	return b.ThorchainBridge.GetBlockHeight()
}

func (b *dashHaltBridge) GetMimir(key string) (int64, error) {
	b.reads = append(b.reads, key)
	if b.getMimir != nil {
		return b.getMimir(key)
	}
	return b.ThorchainBridge.GetMimir(key)
}

func (s *DashSuite) TestInstantSendHaltBoundariesAndReadErrors(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	base := client.bridge
	keys := []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"}
	for i, key := range keys {
		for _, value := range []int64{-1, 0, 99, 100, 101} {
			bridge := &dashHaltBridge{ThorchainBridge: base, getMimir: func(k string) (int64, error) {
				if k == key {
					return value, nil
				}
				return -1, nil
			}}
			client.bridge = bridge
			want := value > 0 && value <= 100
			if key == "NodePauseChainGlobal" {
				want = value > 0 && value >= 100
			}
			c.Assert(client.instantSend.isPaused(), Equals, want, Commentf("%s=%d", key, value))
			if want {
				c.Assert(bridge.reads, HasLen, i+2) // height and keys through the active halt
			} else {
				c.Assert(bridge.reads, HasLen, 5)
			}
			c.Assert(blockscanner.IsChainPaused(client.cfg.BlockScanner, client.log, bridge), Equals, want)
		}
	}
	allReads := append([]string{"height"}, keys...)
	for i, failed := range allReads {
		bridge := &dashHaltBridge{
			ThorchainBridge: base,
			getHeight: func() (int64, error) {
				if failed == "height" {
					return 0, errors.New("height unavailable")
				}
				return 100, nil
			},
			getMimir: func(key string) (int64, error) {
				if key == failed {
					return -1, errors.New("Mimir unavailable")
				}
				return -1, nil
			},
		}
		client.bridge = bridge
		c.Assert(client.instantSend.isPaused(), Equals, true)
		c.Assert(bridge.reads, DeepEquals, allReads[:i+1]) // no calls after the failure
	}
	client.bridge = nil
	c.Assert(client.instantSend.isPaused(), Equals, true)
}

func (s *DashSuite) TestInstantSendEarlyHaltRetainsReservationsAndResumes(c *C) {
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
		c.Logf("halt control: %s", key)
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		client.m = GetMetricForTest(c, common.DASHChain)
		counter := client.m.GetCounter(metrics.DashInstantSendObserved)
		before := testutil.ToFloat64(counter)
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		c.Assert(batch.TxArray, HasLen, 2)
		// Already delivered deposits stay charged while new queued candidates wait.
		_, err = client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.inbound)}, instantSendAdmission{allowed: true})
		c.Assert(err, IsNil)
		client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
			map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.lowFeeInbound)}})
		base := client.bridge
		phase := "halted"
		client.bridge = &dashHaltBridge{ThorchainBridge: base, getMimir: func(k string) (int64, error) {
			if k == key {
				switch phase {
				case "halted":
					return 100, nil
				case "failed refresh":
					return -1, errors.New("halt refresh failed")
				}
			}
			return base.GetMimir(k)
		}}
		var state sharedutxo.InstantSendState
		var marked bool
		for _, nextPhase := range []string{"halted", "failed refresh"} {
			phase = nextPhase
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 0) // ordinary outbounds were already delivered
			c.Assert(f.instantSendCalls, Equals, 0)
			c.Assert(client.instantSend.txQueue, HasLen, 0)
			c.Assert(client.nextMempoolScan, Equals, scanOutbounds)
			state, err = client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Reserved, Equals, uint64(common.One/10))
			marked, err = client.temporalStorage.HasObservedTx(f.lowFeeInbound)
			c.Assert(err, IsNil)
			c.Assert(marked, Equals, false)
			c.Assert(testutil.ToFloat64(client.m.GetGauge(metrics.InstantSendAvailable)), Equals, float64(0))
			c.Assert(testutil.ToFloat64(counter), Equals, before)
		}
		c.Assert(f.ordinaryCalls, Equals, 3)

		phase = "recovered"
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, false)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
		// Neither the earlier reservation nor the resumed observation is emitted twice.
		_, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
	}
}

func (s *DashSuite) TestInstantSendEarlyLookupFailureAndRecovery(c *C) {
	for _, lookup := range []string{"height", "HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal", "missing bridge"} {
		c.Logf("failed lookup: %s", lookup)
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		base := client.bridge
		client.bridge = &dashHaltBridge{
			ThorchainBridge: base,
			getHeight: func() (int64, error) {
				if lookup == "height" {
					return 0, errors.New("height unavailable")
				}
				return base.GetBlockHeight()
			},
			getMimir: func(key string) (int64, error) {
				if lookup == key {
					return -1, errors.New("Mimir unavailable")
				}
				return base.GetMimir(key)
			},
		}
		entries := f.entries
		if lookup == "missing bridge" {
			client.bridge = nil
			// No vault lookup is needed, even if the seeded address cache expires.
			f.entries = nil
		}
		client.nextMempoolScan = scanInstantSend
		client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
			map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.inbound)}})
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		if lookup == "missing bridge" {
			c.Assert(batch.TxArray, HasLen, 0)
		} else {
			c.Assert(batch.TxArray, HasLen, 2)
			for _, item := range batch.TxArray {
				c.Assert(item.Sender, Equals, f.asgard)
			}
		}
		c.Assert(f.ordinaryCalls, Equals, 1)
		c.Assert(f.instantSendCalls, Equals, 0)
		c.Assert(client.instantSend.txQueue, HasLen, 0)
		c.Assert(client.nextMempoolScan, Equals, scanOutbounds)
		var marked bool
		for _, id := range []string{f.inbound, f.lowFeeInbound} {
			marked, err = client.temporalStorage.HasObservedTx(id)
			c.Assert(err, IsNil)
			c.Assert(marked, Equals, false)
		}
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(0))

		client.bridge = base
		f.entries = entries
		_, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, false)
		c.Assert(batch.TxArray, HasLen, 2)
	}
}

func (s *DashSuite) TestInstantSendLateHaltFallsBackWithoutClaims(c *C) {
	for _, queued := range []bool{false, true} {
		for _, failure := range []string{"halt", "height", "HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
			c.Logf("queued: %v, late failure: %s", queued, failure)
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			client.m = GetMetricForTest(c, common.DASHChain)
			counter := client.m.GetCounter(metrics.DashInstantSendObserved)
			before := testutil.ToFloat64(counter)
			client.nextMempoolScan = scanInstantSend
			if queued {
				client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
					map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.inbound)}})
			}
			var paused atomic.Bool
			f.beforeRawTransaction = func(id string) {
				if id == f.inbound {
					paused.Store(true) // during discovery or the queued batch's finality RPC
				}
			}
			base := client.bridge
			client.bridge = &dashHaltBridge{
				ThorchainBridge: base,
				getHeight: func() (int64, error) {
					if paused.Load() && failure == "height" {
						return 0, errors.New("height unavailable")
					}
					return base.GetBlockHeight()
				},
				getMimir: func(key string) (int64, error) {
					if paused.Load() {
						if key == failure {
							return -1, errors.New("Mimir unavailable")
						}
						if key == "HaltDASHChain" && failure == "halt" {
							return 100, nil
						}
					}
					return base.GetMimir(key)
				},
			}
			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 2)
			c.Assert(client.instantSend.txQueue, HasLen, 0)
			c.Assert(client.nextMempoolScan, Equals, scanOutbounds)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Reserved, Equals, uint64(0))
			for _, id := range []string{f.inbound, f.lowFeeInbound, f.lockedOutbound} {
				var marked bool
				marked, err = client.temporalStorage.HasObservedTx(id)
				c.Assert(err, IsNil)
				c.Assert(marked, Equals, false)
			}
			c.Assert(testutil.ToFloat64(client.m.GetGauge(metrics.InstantSendAvailable)), Equals, float64(0))
			c.Assert(testutil.ToFloat64(counter), Equals, before)
			// A deferred deposit remains eligible for the normal block observation.
			c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Tx: []btcjson.TxRawResult{{Txid: f.inbound}}}), IsNil)
			accepted, err := client.trackObservedTxs([]*types.TxInItem{instantSendTestItem(f, f.inbound)})
			c.Assert(err, IsNil)
			c.Assert(accepted, HasLen, 1)
		}
	}
}

func (s *DashSuite) TestInstantSendVaultHaltIncludesStorageFallback(c *C) {
	for _, corrupt := range []bool{false, true} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		if corrupt {
			storage, err := blockscanner.NewBlockScannerStorage(c.MkDir(), config.LevelDBOptions{})
			c.Assert(err, IsNil)
			defer storage.Close()
			client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
			c.Assert(err, IsNil)
			c.Assert(storage.GetInternalDb().Put([]byte("instant-send-state"), []byte("invalid journal"), nil), IsNil)
		}
		checks := 0
		base := client.bridge
		client.bridge = &dashHaltBridge{ThorchainBridge: base, getHeight: func() (int64, error) {
			// Both the initial guard and the storage-fallback guard must let
			// block settlement acquire the allowance lock while the bridge waits.
			c.Assert(client.instantSend.stateMu.TryLock(), Equals, true)
			client.instantSend.stateMu.Unlock()
			checks++
			if !corrupt || checks > 1 {
				return 0, errors.New("height unavailable")
			}
			return 100, nil
		}}
		item := instantSendTestItem(f, f.lockedOutbound)
		item.Sender = f.asgard
		item.To = f.asgard // vault-to-vault still obeys emergency halts
		accepted, err := client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{})
		c.Assert(errors.Is(err, errInstantSendPaused), Equals, true)
		c.Assert(accepted, HasLen, 0)
		marked, err := client.temporalStorage.HasObservedTx(item.Tx)
		c.Assert(err, IsNil)
		c.Assert(marked, Equals, false)
		if corrupt {
			c.Assert(checks, Equals, 2) // second guard protects the marker-only fallback
		}
		client.bridge = base
		accepted, err = client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{})
		c.Assert(err, IsNil)
		c.Assert(accepted, HasLen, 1)
	}
}

// TestInstantSendHaltDoesNotStartWarmup exercises the InstantSend turn with an
// otherwise healthy scanner, then proves removing the halt starts the snapshot.
func (s *DashSuite) TestInstantSendHaltDoesNotStartWarmup(c *C) {
	for _, scenario := range []string{"halt", "height unavailable"} {
		func() {
			c.Log(scenario)
			var snapshots atomic.Int64
			client, _, cleanup := newInstantSendWarmupClient(c, func(method string, params []json.RawMessage) any {
				c.Assert(method, Equals, "getrawmempool")
				if len(params) > 0 {
					snapshots.Add(1)
					return map[string]any{}
				}
				return []string{}
			})
			defer cleanup()
			base := client.bridge
			bridge := &dashHaltBridge{ThorchainBridge: base}
			if scenario == "halt" {
				bridge.getMimir = func(key string) (int64, error) {
					if key == "HaltDASHChain" {
						return 1, nil
					}
					return base.GetMimir(key)
				}
			} else {
				bridge.getHeight = func() (int64, error) {
					return 0, errors.New("height unavailable")
				}
			}
			client.bridge = bridge
			client.nextMempoolScan = scanInstantSend
			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(snapshots.Load(), Equals, int64(0))
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Started, Equals, false)

			client.bridge = base
			client.nextMempoolScan = scanInstantSend
			_, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			state, err = client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Started, Equals, true)
			c.Assert(snapshots.Load() > 0, Equals, true)
		}()
	}
}

func (s *DashSuite) TestInstantSendHaltStillSettlesVerifiedReservations(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	_, err := client.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{
		{TxID: f.lockedOutbound, Amount: 60}, {TxID: f.inbound, Amount: 40},
	}, 0, 100, true, 1)
	c.Assert(err, IsNil)
	client.instantSend.recovery.Reset()
	f.chainLocked = true
	f.txs[f.lockedOutbound]["chainlock"] = true
	f.txs[f.lockedOutbound]["blockhash"] = strings.Repeat("2", 64)
	base := client.bridge
	client.bridge = &dashHaltBridge{ThorchainBridge: base, getMimir: func(key string) (int64, error) {
		if key == "HaltDASHChain" {
			return 100, nil
		}
		return base.GetMimir(key)
	}}
	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(f.instantSendCalls, Equals, 0)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(40))
	c.Assert(state.Reservations, HasLen, 1)
	c.Assert(state.Reservations[f.inbound].Amount, Equals, uint64(40))
	c.Assert(client.instantSend.recovery.Complete(), Equals, true)
}
