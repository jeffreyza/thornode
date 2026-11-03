package utxo

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

// countInstantSendRPCs counts requests safely across parallel RPC workers.
func countInstantSendRPCs(c *C, f *dashMempoolFixture) func(string) int {
	var mu sync.Mutex
	calls := make(map[string]int)
	f.beforeRPC = func(method string, params []json.RawMessage) {
		var ids []string
		switch method {
		case "getrawtransaction", "getblock":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			ids = []string{id}
		case "getislocks":
			c.Assert(json.Unmarshal(params[0], &ids), IsNil)
		}
		mu.Lock()
		defer mu.Unlock()
		calls[method]++
		for _, id := range ids {
			calls[method+"/"+id]++
		}
	}
	return func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[key]
	}
}

// TestInstantSendFiltersUnrelatedTransactions verifies both the early RPC filter
// and its cache, without hiding ordinary outbound or later block observations.
func (s *DashSuite) TestInstantSendFiltersUnrelatedTransactions(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.MempoolTxIDCacheSize = 10
	f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
	// Exercise address decoding from script hex rather than supplied addresses.
	f.txs[f.lockedOutbound]["vout"] = []any{map[string]any{
		"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": newDashP2PKHScriptHex(c, f.user)},
	}, map[string]any{"n": 1, "value": 0, "scriptPubKey": map[string]any{"hex": newDashMemoScriptHex(c, "NOOP")}}}
	calls := countInstantSendRPCs(c, f)
	for range 3 {
		groups, err := client.instantSend.scan(123)
		c.Assert(err, IsNil)
		c.Assert(groups, HasLen, 0)
	}
	c.Assert(calls("getrawtransaction"), Equals, 1)
	c.Assert(calls("getislocks"), Equals, 0)
	c.Assert(calls("getblock"), Equals, 0)
	c.Assert(client.instantSend.ignored, HasLen, 1)
	observed, err := client.temporalStorage.HasObservedTx(f.lockedOutbound)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	batch, err := client.fetchMemPoolTransactions(123, true)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.lockedOutbound)
}

// TestInstantSendFilterKeepsVaultPayments covers deposits and exempt vault transfers.
func (s *DashSuite) TestInstantSendFilterKeepsVaultPayments(c *C) {
	for _, vaultSender := range []bool{false, true} {
		client, f, cleanup := newDashMempoolFixture(c)
		func() {
			defer cleanup()
			client.cfg.MempoolTxIDCacheSize = 10
			client.cfg.ParallelMempoolScan = 3
			f.entries = map[string]map[string]any{f.inbound: dashTestMempoolEntry()}
			memo := "NOOP"
			if vaultSender {
				f.txs[f.inbound]["vin"] = f.txs[f.lockedOutbound]["vin"]
				client.instantSend.maxAmount = 0 // vault senders still bypass allowance
				memo = "CONSOLIDATE"
			}
			// Omit addresses to exercise script decoding, but keep the RPC script
			// types so the consolidation memo is recognised as OP_RETURN data.
			f.txs[f.inbound]["vout"] = []any{map[string]any{
				"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": newDashP2PKHScriptHex(c, f.asgard), "type": "pubkeyhash"},
			}, map[string]any{"n": 1, "value": 0, "scriptPubKey": map[string]any{"hex": newDashMemoScriptHex(c, memo), "type": "nulldata"}}}
			batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: !vaultSender})
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 1)
			c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
			c.Assert(batch.TxArray[0].Memo, Equals, memo)
			c.Assert(client.instantSend.ignored, HasLen, 0)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			want := uint64(common.One / 10)
			if vaultSender {
				want = 0
			}
			c.Assert(state.Reserved, Equals, want)
		}()
	}
}

// TestInstantSendFilterInvalidation proves a newly relevant tx is retried on churn.
func (s *DashSuite) TestInstantSendFilterInvalidation(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.MempoolTxIDCacheSize = 10
	f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
	other := newDashTestAddress(c, 0x33)
	setVaults := func(addresses ...string) {
		vaults := make([]common.Address, 0, len(addresses))
		for _, address := range addresses {
			vaults = append(vaults, common.Address(address))
		}
		client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: vaults, FetchedAt: time.Now()})
	}
	setVaults(f.asgard, other)
	calls := countInstantSendRPCs(c, f)
	_, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	setVaults(other, f.asgard, other) // same set, different order and duplicate
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 1)
	delete(f.entries, f.lockedOutbound)
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	f.entries[f.lockedOutbound] = dashTestMempoolEntry()
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
	setVaults(f.asgard, f.user)
	groups, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 1)
	c.Assert(groups[0].TxArray, HasLen, 1)
	c.Assert(groups[0].TxArray[0].Tx, Equals, f.lockedOutbound)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 3)
	c.Assert(client.instantSend.ignored, HasLen, 0)
}

// TestInstantSendFilterCacheBound keeps the output filter active even with no cache.
func (s *DashSuite) TestInstantSendFilterCacheBound(c *C) {
	for _, capacity := range []int{-1, 0, 1} {
		client, f, cleanup := newDashMempoolFixture(c)
		func() {
			defer cleanup()
			client.cfg.MempoolTxIDCacheSize = capacity
			client.cfg.ParallelMempoolScan = 3
			delete(f.entries, f.unlockedOutbound)
			for _, id := range []string{f.inbound, f.lowFeeInbound} {
				f.txs[id]["vout"] = f.txs[f.lockedOutbound]["vout"]
			}
			calls := countInstantSendRPCs(c, f)
			for range 3 {
				_, err := client.instantSend.scan(123)
				c.Assert(err, IsNil)
				c.Assert(len(client.instantSend.ignored) <= max(capacity, 0), Equals, true)
			}
			want := 9
			if capacity > 0 {
				want = 7 // three initial lookups, then two per scan
			}
			c.Assert(calls("getrawtransaction"), Equals, want)
			c.Assert(calls("getislocks"), Equals, 0)
		}()
	}
}

// TestInstantSendFilterRetriesUnknownTransactions never caches failed or uncertain RPC data.
func (s *DashSuite) TestInstantSendFilterRetriesUnknownTransactions(c *C) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"RPC decoding error", "txid", 123},
		{"mismatched transaction ID", "txid", strings.Repeat("0", 64)},
		{"not yet locked", "instantlock", false},
		{"uncertain script", "vout", []any{map[string]any{
			"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": "invalid", "type": "nulldata"},
		}}},
	} {
		c.Log(tc.name)
		client, f, cleanup := newDashMempoolFixture(c)
		func() {
			defer cleanup()
			client.cfg.MempoolTxIDCacheSize = 10
			f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
			original := f.txs[f.lockedOutbound][tc.field]
			f.txs[f.lockedOutbound][tc.field] = tc.value
			calls := countInstantSendRPCs(c, f)
			_, err := client.instantSend.scan(123)
			c.Assert(err, IsNil)
			c.Assert(client.instantSend.ignored, HasLen, 0)
			f.txs[f.lockedOutbound][tc.field] = original
			_, err = client.instantSend.scan(123)
			c.Assert(err, IsNil)
			c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
			c.Assert(client.instantSend.ignored, HasLen, 1)
		}()
	}
}

type instantSendVaultErrorBridge struct{ thorclient.ThorchainBridge }

// GetAsgardPubKeys simulates a failed refresh while stale addresses remain available.
func (instantSendVaultErrorBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, errors.New("vault refresh unavailable")
}

// TestInstantSendFilterBypassesUnavailableVaults preserves retries after a failed refresh.
func (s *DashSuite) TestInstantSendFilterBypassesUnavailableVaults(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.MempoolTxIDCacheSize = 10
	f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
	calls := countInstantSendRPCs(c, f)
	_, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 1)
	bridge := client.bridge
	client.bridge = instantSendVaultErrorBridge{bridge}
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.asgard)}})
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
	client.bridge = bridge
	client.asgardCache.Store(nil) // a successful refresh with no usable vaults
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 3)
}

// TestInstantSendCandidatesSkipIgnoredWithoutStarving verifies rotation with a binding cap.
func (s *DashSuite) TestInstantSendCandidatesSkipIgnoredWithoutStarving(c *C) {
	client, _, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 2
	ids := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64)}
	mempool := make(map[string]rpc.GetRawMempoolVerboseResult)
	for _, id := range ids {
		mempool[id] = rpc.GetRawMempoolVerboseResult{InstantLock: "true"}
	}
	client.instantSend.ignored = map[string]struct{}{ids[0]: {}, ids[2]: {}}
	for _, want := range [][]string{{ids[1], ids[3]}, {ids[4], ids[1]}, {ids[3], ids[4]}} {
		got, err := client.instantSend.candidates(mempool)
		c.Assert(err, IsNil)
		c.Assert(got, DeepEquals, want)
	}
}
