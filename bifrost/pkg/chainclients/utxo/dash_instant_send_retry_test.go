package utxo

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcjson"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

// newInstantSendScanFixture fills the mempool with distinct customer deposits.
func newInstantSendScanFixture(c *C, count int) (*Client, *dashMempoolFixture, []string, func()) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	client.cfg.UTXO.TransactionBatchSize = count
	fixture.entries = make(map[string]map[string]any, count)
	ids := make([]string, count)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		ids[i] = id
		tx := make(map[string]any)
		for key, value := range fixture.txs[fixture.inbound] {
			tx[key] = value
		}
		tx["txid"], tx["hash"] = id, id
		fixture.txs[id] = tx
		fixture.entries[id] = dashTestMempoolEntry()
		fixture.entries[id]["instantlock"] = "true"
	}
	return client, fixture, ids, cleanup
}

// assertInstantSendScanClaims checks that only delivered deposits consumed allowance
// and received durable observation markers, so failed lookups remain retryable.
func assertInstantSendScanClaims(c *C, client *Client, ids, delivered []string) {
	accepted := make(map[string]bool, len(delivered))
	for _, id := range delivered {
		accepted[id] = true
	}
	for _, id := range ids {
		observed, err := client.temporalStorage.HasObservedTx(id)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, accepted[id], Commentf("txid %s", id))
	}
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reservations, HasLen, len(delivered))
	c.Assert(state.Reserved, Equals, uint64(len(delivered))*common.One/10)
}

// TestDashInstantSendRetriesUnavailableLocks preserves good deposits while null,
// None and failed cycle lookups wait for a later scan without claiming allowance.
func (s *DashSuite) TestDashInstantSendRetriesUnavailableLocks(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 4)
	defer cleanup()
	var recovered atomic.Bool
	failedCycle := strings.Repeat("4", 64)
	fixture.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		switch method {
		case "getislocks":
			var requested []string
			c.Assert(json.Unmarshal(params[0], &requested), IsNil)
			locks := make([]any, len(requested))
			for i, id := range requested {
				if !recovered.Load() && id == ids[0] {
					continue // JSON null
				}
				if !recovered.Load() && id == ids[1] {
					locks[i] = "None"
					continue
				}
				cycle := strings.Repeat("3", 64)
				if !recovered.Load() && id == ids[2] {
					cycle = failedCycle
				}
				locks[i] = map[string]any{"txid": id, "cycleHash": cycle}
			}
			return locks, true
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			if hash == failedCycle {
				return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "cycle unavailable"}, true
			}
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[3])
	assertInstantSendScanClaims(c, client, ids, ids[3:])

	recovered.Store(true)
	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 3)
	for i, item := range batch.TxArray {
		c.Assert(item.Tx, Equals, ids[i])
	}
	assertInstantSendScanClaims(c, client, ids, ids)
}

// TestDashInstantSendKeepsSuccessfulLockChunks checks partial progress across the
// RPC's 100-ID boundary and delivery of the failed chunk after RPC recovery.
func (s *DashSuite) TestDashInstantSendKeepsSuccessfulLockChunks(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 101)
	defer cleanup()
	var recovered atomic.Bool
	calls := countInstantSendRPCs(c, fixture)
	fixture.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		if method != "getislocks" {
			return nil, false
		}
		var requested []string
		c.Assert(json.Unmarshal(params[0], &requested), IsNil)
		if requested[0] == ids[100] && !recovered.Load() {
			c.Assert(requested, HasLen, 1)
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "second chunk unavailable"}, true
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 100)
	for i, item := range batch.TxArray {
		c.Assert(item.Tx, Equals, ids[i])
	}
	c.Assert(calls("getislocks"), Equals, 2)
	assertInstantSendScanClaims(c, client, ids, ids[:100])

	recovered.Store(true)
	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[100])
	c.Assert(calls("getislocks"), Equals, 3)
	assertInstantSendScanClaims(c, client, ids, ids)
}

// TestDashInstantSendDoesNotCacheCycleFailures retries a shared cycle for the next
// candidate in the same scan, then caches only its successful height lookup.
func (s *DashSuite) TestDashInstantSendDoesNotCacheCycleFailures(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 3)
	defer cleanup()
	var cycleReads atomic.Int64
	fixture.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
		if method == "getblock" && cycleReads.Add(1) == 1 {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "cycle unavailable"}, true
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[1])
	c.Assert(batch.TxArray[1].Tx, Equals, ids[2])
	c.Assert(cycleReads.Load(), Equals, int64(2))
	assertInstantSendScanClaims(c, client, ids, ids[1:])

	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[0])
	c.Assert(cycleReads.Load(), Equals, int64(3))
	assertInstantSendScanClaims(c, client, ids, ids)
}

// TestDashInstantSendHexOnlyAddresses exercises the deposit and parent parsing
// used by dashd versions that omit scriptPubKey.addresses from RPC responses.
func (s *DashSuite) TestDashInstantSendHexOnlyAddresses(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 1)
	defer cleanup()
	for _, tx := range fixture.txs {
		outputs, ok := tx["vout"].([]any)
		c.Assert(ok, Equals, true)
		for _, value := range outputs {
			var output map[string]any
			output, ok = value.(map[string]any)
			c.Assert(ok, Equals, true)
			var script map[string]any
			script, ok = output["scriptPubKey"].(map[string]any)
			c.Assert(ok, Equals, true)
			delete(script, "addresses")
			delete(script, "address")
		}
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[0])
	c.Assert(batch.TxArray[0].Sender, Equals, fixture.user)
	c.Assert(batch.TxArray[0].To, Equals, fixture.asgard)
	c.Assert(batch.TxArray[0].Memo, Equals, "NOOP")
	assertInstantSendScanClaims(c, client, ids, ids)
}
