package utxo

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type instantSendBlockingBridge struct {
	thorclient.ThorchainBridge
	before func(string)
}

func (b *instantSendBlockingBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	b.before("status")
	return b.ThorchainBridge.FetchNodeStatus()
}

func (b *instantSendBlockingBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	b.before("vaults")
	return b.ThorchainBridge.GetAsgardPubKeys()
}

func (b *instantSendBlockingBridge) GetBlockHeight() (int64, error) {
	b.before("halt")
	return b.ThorchainBridge.GetBlockHeight()
}

func (s *DashSuite) TestInstantSendNetworkCallsAllowSettlement(c *C) {
	for _, stage := range []string{"snapshot", "sender", "recovery", "claim", "status", "vaults", "halt"} {
		for _, failSettlement := range []bool{false, true} {
			testInstantSendUnlockedSettlement(c, stage, failSettlement)
		}
	}
}

// TestInstantSendWarmupDiscardsStaleSnapshot ensures a new journal cannot start
// from a height or mempool snapshot captured across concurrent block settlement.
func (s *DashSuite) TestInstantSendWarmupDiscardsStaleSnapshot(c *C) {
	for _, blockedMethod := range []string{"getbestchainlock", "getrawmempool"} {
		func() {
			entered, release := make(chan struct{}), make(chan struct{})
			var enteredOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			hash := dashCatchUpHash(100)
			server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
				if method == blockedMethod {
					enteredOnce.Do(func() { close(entered) })
					<-release
				}
				switch method {
				case "getbestchainlock":
					return map[string]any{"height": 100, "known_block": true, "blockhash": hash}
				case "getblockcount":
					return 100
				case "getblockhash":
					return hash
				case "getrawmempool":
					return map[string]any{}
				default:
					c.Fatalf("unexpected RPC %s", method)
					return nil
				}
			})
			defer server.Close()
			client, cleanup := newDashClientForRPCServer(c, server.URL, 10*time.Second)
			defer cleanup()
			defer freshInstantSendJournal(c, client)()
			defer unblock()
			storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
			c.Assert(err, IsNil)
			defer storage.Close()
			fetcher := &dashHealthFetcher{}
			fetcher.height.Store(100)
			client.blockScanner, err = blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
				ChainID: common.DASHChain, StartBlockHeight: 99,
				BlockHeightDiscoverBackoff: time.Millisecond, MaxHealthyLag: 30 * time.Minute,
			}, storage, GetMetricForTest(c, common.DASHChain), dashHealthBridge{}, fetcher)
			c.Assert(err, IsNil)
			client.blockScanner.Start(make(chan types.TxIn), make(chan common.NetworkFee))
			defer client.blockScanner.Stop()
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 100 && client.blockScanner.IsHealthy() })
			client.chainLockHeightHealth.Store(&chainLockHeightHealth{
				blockHeight: 100, chainLockHeight: 100, checkedAt: time.Now(),
			})
			finished := make(chan error, 1)
			var admission instantSendAdmission
			go func() {
				var scanErr error
				admission, scanErr = client.instantSend.prepareAllowance(nil)
				finished <- scanErr
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				c.Fatal("warmup did not reach the blocked RPC")
			}
			settled := make(chan error, 1)
			go func() { settled <- client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 101}) }()
			select {
			case err = <-settled:
				c.Assert(err, IsNil)
			case <-time.After(5 * time.Second):
				c.Fatal("block settlement waited for warmup")
			}
			unblock()
			c.Assert(<-finished, IsNil)
			c.Assert(admission.allowed, Equals, false)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Started, Equals, false)
		}()
	}
}

// testInstantSendUnlockedSettlement holds a network response until settlement
// finishes. Even a failed settlement must invalidate the pending RPC results.
func testInstantSendUnlockedSettlement(c *C, stage string, failSettlement bool) {
	c.Logf("blocked call: %s, failed settlement: %v", stage, failSettlement)
	client, f, cleanup := newDashMempoolFixture(c, 10*time.Second)
	defer cleanup()
	var err error
	prepare := false
	switch stage {
	case "snapshot", "sender":
		prepare = true
		defer freshInstantSendJournal(c, client)()
		c.Assert(client.temporalStorage.StartInstantSendWarmup(100, []string{f.inbound}), IsNil)
	case "recovery":
		prepare = true
		_, err = client.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{
			{TxID: f.inbound, Amount: common.One / 10},
		}, 0, common.One, true, 1)
		c.Assert(err, IsNil)
		client.instantSend.recovery.Reset()
	case "status":
		prepare = true
	case "vaults":
		client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.asgard)}})
	}

	healthyStorage := client.temporalStorage
	var brokenStorage *sharedutxo.TemporalStorage
	if failSettlement {
		closed, closeStorage := newDashClientForRPCServer(c, "http://localhost:1")
		// Force a write: settling an empty journal is a no-op even on a closed DB.
		_, err = closed.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{
			{TxID: f.inbound, Amount: 1},
		}, 0, 100, true, 1)
		c.Assert(err, IsNil)
		brokenStorage = closed.temporalStorage
		closeStorage()
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	before := func(call string) {
		if call == stage {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
	}
	client.bridge = &instantSendBlockingBridge{ThorchainBridge: client.bridge, before: before}
	f.beforeRawTransaction = func(id string) {
		if id == f.inbound {
			switch stage {
			case "snapshot", "recovery", "claim":
				before(stage)
			}
		}
		if id == strings.Repeat("f", 64) {
			before("sender")
		}
	}

	finished := make(chan error, 1)
	var admission instantSendAdmission
	var accepted []*types.TxInItem
	go func() {
		var scanErr error
		if prepare {
			admission, scanErr = client.instantSend.prepareAllowance(nil)
		} else {
			accepted, scanErr = client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.inbound)}, instantSendAdmission{allowed: true})
		}
		finished <- scanErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		c.Fatal("scan did not reach the blocked call")
	}
	settled := make(chan error, 1)
	go func() {
		if failSettlement {
			client.instantSend.stateMu.Lock()
			client.temporalStorage = brokenStorage
			client.instantSend.stateMu.Unlock()
		}
		settlementErr := client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{
			Height: 101, Tx: []btcjson.TxRawResult{{Txid: f.inbound}},
		})
		if failSettlement {
			client.instantSend.stateMu.Lock()
			client.temporalStorage = healthyStorage
			client.instantSend.stateMu.Unlock()
		}
		settled <- settlementErr
	}()
	select {
	case err = <-settled:
		c.Assert(err != nil, Equals, failSettlement)
	case <-time.After(5 * time.Second):
		c.Fatal("block settlement waited for a network call")
	}
	unblock()
	c.Assert(<-finished, IsNil)
	c.Assert(accepted, HasLen, 0)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(client.instantSend.revision, Equals, uint64(1))
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	wantReserved := uint64(0)
	if stage == "recovery" && failSettlement {
		wantReserved = common.One / 10
	}
	c.Assert(state.Reserved, Equals, wantReserved)
	marked, err := client.temporalStorage.HasObservedTx(f.inbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, stage == "recovery") // only the pre-existing claim
	if failSettlement {
		c.Assert(client.instantSend.recovery.loaded, Equals, false)
		c.Assert(client.instantSend.recovery.pendingTxIDs, HasLen, 0)
	} else if stage == "recovery" {
		next, pending := client.instantSend.recovery.Next()
		c.Assert(pending, Equals, true)
		c.Assert(next, Equals, f.inbound) // the stale RPC must not advance the cursor
	}
	if stage == "snapshot" || stage == "sender" {
		c.Assert(state.Ready, Equals, false)
		c.Assert(len(state.SnapshotPending) == 1, Equals, failSettlement)
	}
}
