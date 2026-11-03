package utxo

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type dashMempoolFixture struct {
	lockedOutbound, unlockedOutbound, inbound, lowFeeInbound string
	asgard, user                                             string
	txs                                                      map[string]map[string]any
	entries                                                  map[string]map[string]any
	failOrdinary, failInstantSend, chainLocked               bool
	ordinaryCalls, instantSendCalls                          int
	beforeRawTransaction                                     func(string)
	beforeRPC                                                func(string, []json.RawMessage)
	overrideRPC                                              func(string, []json.RawMessage) (any, bool)
	utxos                                                    []btcjson.ListUnspentResult
	failMempoolEntry                                         bool
}

func newDashMempoolFixture(c *C, rpcTimeout ...time.Duration) (*Client, *dashMempoolFixture, func()) {
	f := &dashMempoolFixture{
		lockedOutbound: strings.Repeat("a", 64), unlockedOutbound: strings.Repeat("b", 64),
		inbound: strings.Repeat("c", 64), lowFeeInbound: strings.Repeat("d", 64),
		asgard: newDashTestAddress(c, 0x11), user: newDashTestAddress(c, 0x22),
		txs: make(map[string]map[string]any), entries: make(map[string]map[string]any),
	}
	prevVault, prevUser := strings.Repeat("e", 64), strings.Repeat("f", 64)
	output := func(address string, amount float64) map[string]any {
		return map[string]any{"n": 0, "value": amount, "scriptPubKey": map[string]any{
			"hex": newDashP2PKHScriptHex(c, address), "type": "pubkeyhash", "addresses": []string{address},
		}}
	}
	f.txs[prevVault] = map[string]any{"txid": prevVault, "vout": []any{output(f.asgard, 0.10002)}}
	f.txs[prevUser] = map[string]any{"txid": prevUser, "vout": []any{output(f.user, 0.10002)}}
	for _, id := range []string{f.lockedOutbound, f.unlockedOutbound, f.inbound, f.lowFeeInbound} {
		prev, destination, memo := prevVault, f.user, "OUT:"+strings.Repeat("1", 64)
		if id == f.inbound || id == f.lowFeeInbound {
			prev, destination, memo = prevUser, f.asgard, "NOOP"
		}
		locked := id != f.unlockedOutbound
		f.txs[id] = map[string]any{
			"txid": id, "hash": id, "version": 2, "instantlock": locked,
			"vin": []any{map[string]any{"txid": prev, "vout": 0, "sequence": uint32(4294967295)}},
			"vout": []any{output(destination, 0.1), map[string]any{
				"n": 1, "value": 0,
				"scriptPubKey": map[string]any{"hex": newDashMemoScriptHex(c, memo), "type": "nulldata"},
			}},
		}
		f.entries[id] = dashTestMempoolEntry()
		f.entries[id]["instantlock"] = strconv.FormatBool(locked)
	}
	// Outbound observations do not require the InstantSend age policy.
	for _, id := range []string{f.lockedOutbound, f.unlockedOutbound, f.lowFeeInbound} {
		f.entries[id]["fees"] = map[string]any{"base": 0.000002}
	}
	f.entries[f.lockedOutbound]["time"] = time.Now().Add(-time.Hour).Unix()
	blockHash := strings.Repeat("2", 64)
	cycleHash := strings.Repeat("3", 64)
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		if f.beforeRPC != nil {
			f.beforeRPC(method, params)
		}
		if f.overrideRPC != nil {
			if result, overridden := f.overrideRPC(method, params); overridden {
				return result
			}
		}
		switch method {
		case "listunspent":
			return f.utxos
		case "getmempoolentry":
			if f.failMempoolEntry {
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "entry unavailable"}
			}
			return map[string]any{"ancestorcount": 1, "descendantcount": 1}
		case "getrawmempool":
			verbose := false
			if len(params) > 0 {
				c.Assert(json.Unmarshal(params[0], &verbose), IsNil)
			}
			if verbose {
				f.instantSendCalls++
				if f.failInstantSend {
					return "invalid mempool response"
				}
				return f.entries
			}
			f.ordinaryCalls++
			if f.failOrdinary {
				return "invalid mempool response"
			}
			ids := make([]string, 0, len(f.entries))
			for id := range f.entries {
				ids = append(ids, id)
			}
			return ids
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if f.beforeRawTransaction != nil {
				f.beforeRawTransaction(id)
			}
			c.Assert(f.txs[id], NotNil)
			return f.txs[id]
		case "getislocks":
			var ids []string
			c.Assert(json.Unmarshal(params[0], &ids), IsNil)
			locks := make([]any, 0, len(ids))
			for _, id := range ids {
				locks = append(locks, map[string]any{"txid": id, "cycleHash": cycleHash})
			}
			return locks
		case "getblockhash":
			return blockHash
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			if hash == cycleHash {
				return map[string]any{"hash": hash, "height": 100, "confirmations": 1}
			}
			c.Assert(hash, Equals, blockHash)
			return map[string]any{
				"hash": hash, "height": 150, "chainlock": f.chainLocked,
				"tx": []any{f.txs[f.lockedOutbound], f.txs[f.unlockedOutbound]},
			}
		default:
			c.Fatalf("unexpected RPC %s", method)
			return nil
		}
	})
	client, cleanup := newDashClientForRPCServer(c, server.URL, rpcTimeout...)
	client.cfg.UTXO.TransactionBatchSize = 10
	client.cfg.UTXO.MaxMempoolBatches = 1
	client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{common.Address(f.asgard)}, FetchedAt: time.Now(),
	})
	return client, f, func() { server.Close(); cleanup() }
}

func (s *DashSuite) TestDashFetchMemPoolOutboundsAndFinalObservations(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()

	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.Chain, Equals, common.DASHChain)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(batch.TxArray, HasLen, 2)
	seen := make(map[string]bool)
	for _, item := range batch.TxArray {
		seen[item.Tx] = true
		c.Assert(item.Sender, Equals, f.asgard)
		c.Assert(item.To, Equals, f.user)
		c.Assert(item.BlockHeight, Equals, int64(123))
		var observed bool
		observed, err = client.temporalStorage.HasObservedTx(item.Tx)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, false) // final block observation is still needed
	}
	c.Assert(seen, DeepEquals, map[string]bool{f.lockedOutbound: true, f.unlockedOutbound: true})

	// The ordinary scan neither emits deposits nor suppresses InstantSend.
	final, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(final.MemPool, Equals, false)
	c.Assert(final.TxArray, HasLen, 2)
	c.Assert(final.TxArray[0].Tx, Equals, f.inbound)
	c.Assert(final.TxArray[1].Tx, Equals, f.lowFeeInbound)
	c.Assert(final.TxArray[0].BlockHeight, Equals, int64(100))
	observed, err := client.temporalStorage.HasObservedTx(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)

	retry, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(retry.MemPool, Equals, true)
	c.Assert(retry.TxArray, HasLen, 0) // non-final mempool deduplication

	// Both outbounds can still be observed once mined in a ChainLocked block.
	_, err = client.getBlock(150)
	c.Assert(err, NotNil)
	f.chainLocked = true
	block, err := client.getBlock(150)
	c.Assert(err, IsNil)
	client.disableVinZeroBatch = true
	mined, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.MemPool, Equals, false)
	c.Assert(mined.TxArray, HasLen, 2)
	for _, item := range mined.TxArray {
		c.Assert(seen[item.Tx], Equals, true)
		c.Assert(item.BlockHeight, Equals, int64(150))
	}
	mined, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 0)
}

// TestDashMempoolRestartKeepsLockObservation checks the two scanner paths using
// reopened storage, including the commit callback between the observations.
func (s *DashSuite) TestDashMempoolRestartKeepsLockObservation(c *C) {
	for _, migration := range []bool{false, true} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		if migration {
			to := newDashTestAddress(c, 0x33)
			client.asgardCache.Store(&sharedutxo.AsgardCache{
				Addresses: []common.Address{common.Address(f.asgard), common.Address(to)}, FetchedAt: time.Now(),
			})
			f.txs[f.lockedOutbound]["vout"] = []any{
				map[string]any{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{
					"hex": newDashP2PKHScriptHex(c, to), "type": "pubkeyhash", "addresses": []string{to},
				}},
				map[string]any{"n": 1, "value": 0, "scriptPubKey": map[string]any{
					"hex": newDashMemoScriptHex(c, "MIGRATE:90"), "type": "nulldata",
				}},
			}
			f.entries[f.lockedOutbound]["time"] = time.Now().Unix()
		}
		path := c.MkDir()
		storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
		c.Assert(err, IsNil)
		client.cfg.BlockScanner.DBPath = path
		client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
		c.Assert(err, IsNil)
		client.signerCacheManager, err = signercache.NewSignerCacheManager(storage.GetInternalDb())
		c.Assert(err, IsNil)
		batch, err := client.fetchMemPoolTransactions(123, true)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 2)
		for _, item := range batch.TxArray {
			client.OnObservedTxIn(*item, item.BlockHeight)
		}
		c.Assert(storage.Close(), IsNil)
		storage, err = blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
		c.Assert(err, IsNil)
		defer storage.Close()
		client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
		c.Assert(err, IsNil)
		client.signerCacheManager, err = signercache.NewSignerCacheManager(storage.GetInternalDb())
		c.Assert(err, IsNil)
		client.instantSend.recovery.Reset()
		batch, err = client.fetchMemPoolTransactions(124, true)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0) // persisted mempool markers survive restart
		wantMined := 2
		if migration {
			client.instantSend.maxAmount = 0 // vault senders remain exempt
			batch, err = client.instantSend.fetchBatch(124, instantSendAdmission{})
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, false)
			c.Assert(batch.TxArray, HasLen, 1)
			c.Assert(batch.TxArray[0].Tx, Equals, f.lockedOutbound)
			wantMined = 1 // InstantSend already supplied the migration's final observation
		}
		f.chainLocked = true
		block, err := client.getBlock(150)
		c.Assert(err, IsNil)
		client.disableVinZeroBatch = true
		batch, err = client.extractTxs(block)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, wantMined)
		batch, err = client.extractTxs(block)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
	}
}

type dashMempoolPolicyBridge struct {
	thorclient.ThorchainBridge
	enabled int64
}

func (b *dashMempoolPolicyBridge) GetMimir(key string) (int64, error) {
	if key == DashInstantSendEnabledMimirKey {
		return b.enabled, nil
	}
	if key == DashInstantSendMaxAmountMimirKey {
		return 100_000 * common.One, nil
	}
	return -1, nil
}

func (b *dashMempoolPolicyBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return ttypes.NodeStatus_Active, nil
}

func (b *dashMempoolPolicyBridge) GetBlockHeight() (int64, error) { return 100, nil }

func (b *dashMempoolPolicyBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	// These fixtures seed vault addresses directly. An empty refresh retains
	// them when the cache expires, without calling an embedded nil bridge.
	return nil, nil
}

func (s *DashSuite) TestDashFetchMemPoolWithInstantSendDisabled(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 0}
	client.bridge = bridge
	// A disabled policy must discard any previously prepared final batches.
	client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
		map[int64][]*types.TxInItem{100: {{Tx: "stale-policy-batch"}}})
	for i := 0; i < 2; i++ {
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		if i == 0 {
			c.Assert(batch.TxArray, HasLen, 2)
		} else {
			c.Assert(batch.TxArray, HasLen, 0)
		}
	}
	c.Assert(client.instantSend.txQueue, HasLen, 0)
	c.Assert(f.ordinaryCalls, Equals, 2)
	c.Assert(f.instantSendCalls, Equals, 0)
	bridge.enabled = 1
	_, err := client.FetchMemPool(123) // ordinary scan resumes the alternating schedule
	c.Assert(err, IsNil)
	final, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(final.MemPool, Equals, false)
	c.Assert(final.TxArray, HasLen, 2)
	c.Assert(final.TxArray[0].Tx, Equals, f.inbound)
	c.Assert(final.TxArray[1].Tx, Equals, f.lowFeeInbound)
}

func (s *DashSuite) TestDashFetchMemPoolErrorsDoNotStarveOtherPath(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	f.failOrdinary = true
	_, err := client.FetchMemPool(123)
	c.Assert(err, NotNil)
	final, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(final.TxArray, HasLen, 2)
	c.Assert(final.MemPool, Equals, false)

	f.failOrdinary = false
	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	c.Assert(batch.MemPool, Equals, true)
	f.failInstantSend = true
	_, err = client.FetchMemPool(123)
	c.Assert(err, NotNil)
	batch, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(f.ordinaryCalls, Equals, 3)
	c.Assert(f.instantSendCalls, Equals, 2)
}
