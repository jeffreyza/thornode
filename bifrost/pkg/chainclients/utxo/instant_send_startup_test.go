package utxo

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// newInstantSendWarmupClient starts an active, caught-up client with no journal.
func newInstantSendWarmupClient(c *C, handler func(string, []json.RawMessage) any, rpcTimeout ...time.Duration) (*Client, *dashHealthFetcher, func()) {
	fetcher := &dashHealthFetcher{}
	fetcher.height.Store(100)
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		height := fetcher.height.Load()
		switch method {
		case "getbestchainlock":
			return map[string]any{"height": height, "known_block": true, "blockhash": dashCatchUpHash(height)}
		case "getblockhash":
			return dashCatchUpHash(height)
		case "getblockcount":
			return height
		default:
			return handler(method, params)
		}
	})
	client, cleanup := newDashClientForRPCServer(c, server.URL, rpcTimeout...)
	closeJournal := freshInstantSendJournal(c, client)
	stopScanner := startInstantSendWarmupScanner(c, client, fetcher)
	_, err := client.GetHeight()
	c.Assert(err, IsNil)
	return client, fetcher, func() {
		stopScanner()
		closeJournal()
		cleanup()
		server.Close()
	}
}

// TestInstantSendSnapshotRecoveryChecks keeps errors distinct from verified absence.
func (s *DashSuite) TestInstantSendSnapshotRecoveryChecks(c *C) {
	missingID, goodID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, scenario := range []string{
		"absent", "locked", "unlocked", "unknown", "mempool error", "null mempool",
		"lookup error", "height error", "scanner behind", "old ChainLock", "ChainLock lag", "stale health",
		"block during lookup", "no scanner",
	} {
		func() {
			c.Log(scenario)
			var retry atomic.Bool
			var mempoolReads, goodReads atomic.Int64
			fetcher := &dashHealthFetcher{}
			fetcher.height.Store(101)
			if scenario == "block during lookup" {
				fetcher.height.Store(100)
			}
			var client *Client
			server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
				height := int64(101)
				if !retry.Load() {
					switch scenario {
					case "scanner behind":
						height = 102
					case "old ChainLock":
						height = 100
					}
				}
				switch method {
				case "getbestchainlock":
					if scenario == "height error" && !retry.Load() {
						return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "height unavailable"}
					}
					return map[string]any{"height": height, "known_block": true, "blockhash": dashCatchUpHash(height)}
				case "getblockcount":
					if scenario == "ChainLock lag" && !retry.Load() {
						return height + 2
					}
					return height
				case "getblockhash":
					return dashCatchUpHash(height)
				case "getrawtransaction":
					var id string
					c.Assert(json.Unmarshal(params[0], &id), IsNil)
					if id == goodID {
						goodReads.Add(1)
						return dashLockedRawTx(id)
					}
					c.Assert(id, Equals, missingID)
					if !retry.Load() {
						switch scenario {
						case "lookup error":
							return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "lookup unavailable"}
						case "block during lookup":
							fetcher.height.Store(101)
							waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 101 })
						}
					}
					return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
				case "getrawmempool":
					mempoolReads.Add(1)
					if !retry.Load() {
						switch scenario {
						case "stale health":
							client.chainLockHeightHealth.Store(&chainLockHeightHealth{
								blockHeight: 101, chainLockHeight: 101, checkedAt: time.Now().Add(-time.Hour),
							})
						case "locked", "unlocked", "unknown":
							status := map[string]string{"locked": "true", "unlocked": "false", "unknown": "unknown"}[scenario]
							return map[string]any{missingID: map[string]any{"instantlock": status}}
						case "mempool error":
							return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "mempool unavailable"}
						case "null mempool":
							return nil
						}
					}
					return map[string]any{}
				default:
					c.Fatalf("unexpected RPC %s", method)
					return nil
				}
			})
			defer server.Close()
			var cleanup func()
			client, cleanup = newDashClientForRPCServer(c, server.URL)
			defer cleanup()
			defer freshInstantSendJournal(c, client)()
			c.Assert(client.temporalStorage.StartInstantSendWarmup(100, []string{missingID, goodID}), IsNil)
			defer startInstantSendWarmupScanner(c, client, fetcher)()
			scanner := client.blockScanner
			if scenario == "no scanner" {
				client.blockScanner = nil
			}
			admission, err := client.instantSend.prepareAllowance(nil)
			if scenario == "absent" {
				c.Assert(err, IsNil)
				c.Assert(admission.allowed, Equals, true)
			} else {
				c.Assert(err, NotNil)
				c.Assert(admission.allowed, Equals, false)
			}
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Ready, Equals, scenario == "absent")
			if scenario != "absent" {
				c.Assert(state.SnapshotPending, DeepEquals, []string{missingID})
			}
			c.Assert(goodReads.Load(), Equals, int64(1)) // healthy entries still progress
			switch scenario {
			case "lookup error", "height error", "scanner behind", "old ChainLock", "ChainLock lag", "block during lookup", "no scanner":
				c.Assert(mempoolReads.Load(), Equals, int64(0))
			default:
				c.Assert(mempoolReads.Load(), Equals, int64(1))
			}

			retry.Store(true)
			client.blockScanner = scanner
			admission, err = client.instantSend.prepareAllowance(nil)
			c.Assert(err, IsNil)
			c.Assert(admission.allowed, Equals, true)
			state, err = client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.SnapshotPending, HasLen, 0)
			c.Assert(state.Reserved, Equals, uint64(0))
			c.Assert(goodReads.Load(), Equals, int64(1))
			marked, err := client.temporalStorage.HasObservedTx(missingID)
			c.Assert(err, IsNil)
			c.Assert(marked, Equals, false)
		}()
	}
}

// TestInstantSendSnapshotRecoveryRetriesTransactionsAndParents resumes normal accounting
// when data returns; a missing parent must never discard its customer transaction.
func (s *DashSuite) TestInstantSendSnapshotRecoveryRetriesTransactionsAndParents(c *C) {
	for _, missing := range []string{"transaction", "parent"} {
		func() {
			c.Log(missing)
			id, parent := strings.Repeat("a", 64), strings.Repeat("b", 64)
			missingID := id
			if missing == "parent" {
				missingID = parent
			}
			vault, user := newDashTestAddress(c, 0x11), newDashTestAddress(c, 0x22)
			tx := dashLockedRawTx(id)
			tx["vin"] = []any{map[string]any{"txid": parent, "vout": 0}}
			tx["vout"] = []any{map[string]any{
				"n": 0, "value": 30, "scriptPubKey": map[string]any{"hex": newDashP2PKHScriptHex(c, vault)},
			}}
			parentTx := map[string]any{"txid": parent, "vout": []any{map[string]any{
				"n": 0, "value": 31, "scriptPubKey": map[string]any{"hex": newDashP2PKHScriptHex(c, user)},
			}}}
			var available atomic.Bool
			var mempoolReads atomic.Int64
			client, fetcher, cleanup := newInstantSendWarmupClient(c, func(method string, params []json.RawMessage) any {
				switch method {
				case "getrawtransaction":
					var requested string
					c.Assert(json.Unmarshal(params[0], &requested), IsNil)
					if !available.Load() && requested == missingID {
						return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
					}
					if requested == parent {
						return parentTx
					}
					c.Assert(requested, Equals, id)
					return tx
				case "getrawmempool":
					mempoolReads.Add(1)
					return map[string]any{id: map[string]any{"instantlock": "true"}}
				default:
					c.Fatalf("unexpected RPC %s", method)
					return nil
				}
			})
			defer cleanup()
			client.instantSend.maxAmount = 100 * common.One
			client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(vault)}, FetchedAt: time.Now()})
			c.Assert(client.temporalStorage.StartInstantSendWarmup(100, []string{id}), IsNil)
			fetcher.height.Store(101)
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 101 })
			admission, err := client.instantSend.prepareAllowance(nil)
			c.Assert(err, NotNil)
			c.Assert(admission.allowed, Equals, false)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.SnapshotPending, DeepEquals, []string{id})
			if missing == "parent" {
				c.Assert(mempoolReads.Load(), Equals, int64(0))
			} else {
				c.Assert(mempoolReads.Load(), Equals, int64(1))
			}
			available.Store(true)
			admission, err = client.instantSend.prepareAllowance(nil)
			c.Assert(err, IsNil)
			c.Assert(admission.allowed, Equals, true)
			state, err = client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.SnapshotPending, HasLen, 0)
			c.Assert(state.Reserved, Equals, uint64(30*common.One))
			c.Assert(state.Reservations[id].Origin, Equals, sharedutxo.InstantSendSnapshot)
		}()
	}
}

// TestInstantSendSnapshotRecoveryDiscardsStaleChecks lets settlement finish while
// the absence check is blocked, and rejects its result even when settlement fails.
func (s *DashSuite) TestInstantSendSnapshotRecoveryDiscardsStaleChecks(c *C) {
	for _, failSettlement := range []bool{false, true} {
		func() {
			id := strings.Repeat("a", 64)
			entered, release := make(chan struct{}), make(chan struct{})
			var enteredOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			client, fetcher, cleanup := newInstantSendWarmupClient(c, func(method string, _ []json.RawMessage) any {
				switch method {
				case "getrawtransaction":
					return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
				case "getrawmempool":
					enteredOnce.Do(func() { close(entered) })
					<-release
					return map[string]any{}
				default:
					c.Fatalf("unexpected RPC %s", method)
					return nil
				}
			}, 10*time.Second)
			defer cleanup()
			defer unblock()
			c.Assert(client.temporalStorage.StartInstantSendWarmup(100, []string{id}), IsNil)
			fetcher.height.Store(101)
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 101 })
			var broken *sharedutxo.TemporalStorage
			if failSettlement {
				closed, closeStorage := newDashClientForRPCServer(c, "http://localhost:1")
				_, err := closed.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{
					{TxID: id, Amount: 1},
				}, 0, 100, true, 1)
				c.Assert(err, IsNil)
				broken = closed.temporalStorage
				closeStorage()
			}
			finished := make(chan error, 1)
			var admission instantSendAdmission
			go func() {
				var err error
				admission, err = client.instantSend.prepareAllowance(nil)
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				c.Fatal("recovery did not reach the mempool check")
			}
			settled := make(chan error, 1)
			go func() {
				client.instantSend.stateMu.Lock()
				healthy := client.temporalStorage
				if failSettlement {
					client.temporalStorage = broken
				}
				client.instantSend.stateMu.Unlock()
				block := &btcjson.GetBlockVerboseTxResult{Height: 102}
				if failSettlement {
					block.Tx = []btcjson.TxRawResult{{Txid: id}} // force a failed write
				}
				err := client.instantSend.reconcileBlock(block)
				client.instantSend.stateMu.Lock()
				client.temporalStorage = healthy
				client.instantSend.stateMu.Unlock()
				settled <- err
			}()
			select {
			case err := <-settled:
				c.Assert(err != nil, Equals, failSettlement)
			case <-time.After(5 * time.Second):
				c.Fatal("settlement waited for the recovery RPC")
			}
			unblock()
			c.Assert(<-finished, IsNil)
			c.Assert(admission.allowed, Equals, false)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Ready, Equals, false)
			c.Assert(state.SnapshotPending, DeepEquals, []string{id})
			fetcher.height.Store(102)
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 102 })
			admission, err = client.instantSend.prepareAllowance(nil)
			c.Assert(err, IsNil)
			c.Assert(admission.allowed, Equals, true)
		}()
	}
}

// TestInstantSendSnapshotRecoveryPersists verifies failed writes, restart and retained exposure.
func (s *DashSuite) TestInstantSendSnapshotRecoveryPersists(c *C) {
	missingID, knownID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var mempoolReads atomic.Int64
	client, fetcher, cleanup := newInstantSendWarmupClient(c, func(method string, _ []json.RawMessage) any {
		switch method {
		case "getrawtransaction":
			return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
		case "getrawmempool":
			mempoolReads.Add(1)
			return map[string]any{}
		default:
			c.Fatalf("unexpected RPC %s", method)
			return nil
		}
	})
	defer cleanup()
	path := c.MkDir()
	storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer func() { _ = storage.Close() }()
	client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
	c.Assert(err, IsNil)
	client.instantSend.maxAmount = 100 * common.One
	c.Assert(client.temporalStorage.StartInstantSendWarmup(100, []string{missingID, knownID}), IsNil)
	c.Assert(client.temporalStorage.RecordInstantSendSnapshot([]string{knownID}, []sharedutxo.InstantSendReservation{
		{TxID: knownID, Amount: 30 * common.One, CreatedAt: 1},
	}), IsNil)
	_, err = client.temporalStorage.TrackObservedTx(knownID)
	c.Assert(err, IsNil)
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, NotNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(mempoolReads.Load(), Equals, int64(0))

	// Reopen the database before the next block: restart must preserve the wait.
	c.Assert(storage.Close(), IsNil)
	storage, err = blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
	c.Assert(err, IsNil)
	client.instantSend.recovery.Reset()
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, NotNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(mempoolReads.Load(), Equals, int64(0))

	fetcher.height.Store(101)
	waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 101 })
	// Cached state remains readable, but the removal write must fail atomically.
	c.Assert(storage.Close(), IsNil)
	var logs bytes.Buffer
	client.log = zerolog.New(&logs)
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, NotNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(strings.Contains(logs.String(), "discarded missing"), Equals, false)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.SnapshotPending, DeepEquals, []string{missingID})
	c.Assert(state.Reserved, Equals, uint64(30*common.One))

	storage, err = blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
	c.Assert(err, IsNil)
	client.instantSend.recovery.Reset()
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.SnapshotHeight, Equals, int64(100))
	c.Assert(state.Ready, Equals, true)
	c.Assert(state.SnapshotPending, HasLen, 0)
	c.Assert(state.Reserved, Equals, uint64(30*common.One))
	c.Assert(state.Available(client.instantSend.maxAmount), Equals, uint64(70*common.One))
	c.Assert(strings.Contains(logs.String(), missingID), Equals, true)
	marked, err := client.temporalStorage.HasObservedTx(knownID)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, true)
	marked, err = client.temporalStorage.HasObservedTx(missingID)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	c.Assert(mempoolReads.Load(), Equals, int64(2))
}

// startInstantSendWarmupScanner runs a real scanner to the fixture's completed height.
func startInstantSendWarmupScanner(c *C, client *Client, fetcher *dashHealthFetcher) func() {
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	height := fetcher.height.Load()
	scanner, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.DASHChain, StartBlockHeight: height - 1,
		BlockHeightDiscoverBackoff: time.Millisecond, MaxHealthyLag: 30 * time.Minute,
	}, storage, GetMetricForTest(c, common.DASHChain), dashHealthBridge{}, fetcher)
	c.Assert(err, IsNil)
	client.blockScanner = scanner
	scanner.Start(make(chan types.TxIn), make(chan common.NetworkFee))
	waitForDashCatchUp(c, func() bool { return scanner.PreviousHeight() == height && scanner.IsHealthy() })
	return func() {
		scanner.Stop()
		_ = storage.Close()
	}
}

func (s *DashSuite) TestInstantSendWarmupRetriesUnavailableLockStatus(c *C) {
	oldID, newID, uncertainID := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, status := range []string{"unknown", "missing", "unexpected", "rpc_error"} {
		var healthy atomic.Bool
		var snapshots, lookups atomic.Int64
		client, fetcher, cleanup := newInstantSendWarmupClient(c, func(method string, params []json.RawMessage) any {
			switch method {
			case "getrawmempool":
				snapshots.Add(1)
				if healthy.Load() {
					return map[string]any{
						newID: map[string]any{"instantlock": "true"},
						oldID: map[string]any{"instantlock": "false"},
					}
				}
				if status == "rpc_error" {
					return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "offline"}
				}
				entry := map[string]any{}
				if status != "missing" {
					entry["instantlock"] = status
				}
				return map[string]any{oldID: map[string]any{"instantlock": "true"}, uncertainID: entry}
			case "getrawtransaction":
				lookups.Add(1)
				var id string
				c.Assert(json.Unmarshal(params[0], &id), IsNil)
				c.Assert(id, Equals, newID) // retry must classify the fresh list
				return dashLockedRawTx(id)
			default:
				c.Fatalf("unexpected RPC %s", method)
				return nil
			}
		})
		defer cleanup()
		admission, err := client.instantSend.prepareAllowance(nil)
		c.Assert(err, NotNil, Commentf("status: %s", status))
		if status != "rpc_error" {
			c.Assert(err.Error(), Equals, "InstantSend mempool lock status unavailable")
		}
		c.Assert(admission.allowed, Equals, false)
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Started, Equals, false)
		c.Assert(state.Ready, Equals, false)
		c.Assert(state.SnapshotPending, HasLen, 0)
		c.Assert(state.Reserved, Equals, uint64(0))
		c.Assert(lookups.Load(), Equals, int64(0))

		healthy.Store(true)
		admission, err = client.instantSend.prepareAllowance(nil)
		c.Assert(err, IsNil)
		c.Assert(admission.allowed, Equals, false) // still waiting for scanner progress
		c.Assert(snapshots.Load(), Equals, int64(2))
		c.Assert(lookups.Load(), Equals, int64(1))
		fetcher.height.Store(101)
		waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 101 })
		admission, err = client.instantSend.prepareAllowance(nil)
		c.Assert(err, IsNil)
		c.Assert(admission.allowed, Equals, true)
		c.Assert(snapshots.Load(), Equals, int64(2))
	}
}

func (s *DashSuite) TestInstantSendWarmupSelectsOnlyLockedTransactions(c *C) {
	lockedID, unlockedID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, scenario := range []string{"empty", "unlocked", "mixed"} {
		var snapshots, lookups atomic.Int64
		client, fetcher, cleanup := newInstantSendWarmupClient(c, func(method string, params []json.RawMessage) any {
			switch method {
			case "getrawmempool":
				snapshots.Add(1)
				entries := map[string]any{}
				if scenario != "empty" {
					entries[unlockedID] = map[string]any{"instantlock": "false"}
				}
				if scenario == "mixed" {
					entries[lockedID] = map[string]any{"instantlock": "true"}
				}
				return entries
			case "getrawtransaction":
				lookups.Add(1)
				var id string
				c.Assert(json.Unmarshal(params[0], &id), IsNil)
				c.Assert(id, Equals, lockedID)
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
			default:
				c.Fatalf("unexpected RPC %s", method)
				return nil
			}
		})
		defer cleanup()
		admission, err := client.instantSend.prepareAllowance(nil)
		if scenario == "mixed" {
			c.Assert(err, NotNil)
		} else {
			c.Assert(err, IsNil)
			c.Assert(lookups.Load(), Equals, int64(0))
		}
		c.Assert(admission.allowed, Equals, false)
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Started, Equals, true)
		c.Assert(state.Ready, Equals, false)
		if scenario == "mixed" {
			c.Assert(state.SnapshotPending, DeepEquals, []string{lockedID})
		} else {
			c.Assert(state.SnapshotPending, HasLen, 0)
		}

		fetcher.height.Store(101)
		waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 101 })
		admission, err = client.instantSend.prepareAllowance(nil)
		if scenario == "mixed" {
			c.Assert(err, NotNil)
			c.Assert(admission.allowed, Equals, false)
			c.Assert(lookups.Load(), Equals, int64(2))
		} else {
			c.Assert(err, IsNil)
			c.Assert(admission.allowed, Equals, true)
		}
		state, err = client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		if scenario == "mixed" {
			// A missing transaction still listed in the fresh mempool stays pending.
			c.Assert(state.SnapshotPending, DeepEquals, []string{lockedID})
			c.Assert(snapshots.Load(), Equals, int64(2))
		} else {
			c.Assert(snapshots.Load(), Equals, int64(1))
		}
	}
}
