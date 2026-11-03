package utxo

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
)

// TestInstantSendBlockGapsRestartRecovery distinguishes skipped ranges from
// first blocks, normal progress, retries and scanner rewinds.
func (s *DashSuite) TestInstantSendBlockGapsRestartRecovery(c *C) {
	for _, tc := range []struct {
		previous, next int64
		reset          bool
	}{
		{0, 100, false},
		{100, 101, false},
		{100, 100, false},
		{100, 90, false},
		{100, 102, true},
		{100, 200, true},
	} {
		func() {
			client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
			defer cleanup()
			h := &client.instantSend
			h.lastBlockHeight = tc.previous
			revision := h.revision
			c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: tc.next}), IsNil)
			comment := Commentf("previous=%d next=%d", tc.previous, tc.next)
			c.Check(h.lastBlockHeight, Equals, tc.next, comment)
			c.Check(h.recovery.Complete(), Equals, !tc.reset, comment)
			c.Check(h.revision, Equals, revision+1, comment)
			c.Check(h.currentAdmission(instantSendAdmission{allowed: true}).allowed, Equals, !tc.reset, comment)
			// The next consecutive block must preserve unfinished recovery.
			c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: tc.next + 1}), IsNil)
			c.Check(h.recovery.Complete(), Equals, !tc.reset, comment)
		}()
	}
}

// TestInstantSendGapRecoveryRequiresInclusion verifies bounded recovery after a
// runtime jump, retaining missing and unsettled reservations and retrying errors.
func (s *DashSuite) TestInstantSendGapRecoveryRequiresInclusion(c *C) {
	mined, missing, unsettled := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	hash := strings.Repeat("d", 64)
	var failed, valid atomic.Bool
	failed.Store(true)
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			switch id {
			case missing:
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
			case unsettled:
				return dashLockedRawTx(id)
			default:
				if failed.Load() {
					return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "offline"}
				}
				return map[string]any{"txid": id, "chainlock": true, "blockhash": hash}
			}
		case "getblockhash":
			return hash
		case "getblock":
			ids := []any{}
			if valid.Load() {
				ids = append(ids, map[string]any{"txid": mined})
			}
			return map[string]any{"hash": hash, "height": 101, "chainlock": true, "tx": ids}
		default:
			c.Fatalf("unexpected RPC %s", method)
			return nil
		}
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	_, err := client.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{
		{TxID: mined, Amount: 60}, {TxID: missing, Amount: 20}, {TxID: unsettled, Amount: 20},
	}, 0, 100, true, 3)
	c.Assert(err, IsNil)
	h := &client.instantSend
	h.maxAmount = 100
	client.cfg.UTXO.TransactionBatchSize = 1
	c.Assert(h.recovery.Complete(), Equals, true)
	c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 100}), IsNil)
	c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 110}), IsNil)
	c.Assert(h.currentAdmission(instantSendAdmission{allowed: true}).allowed, Equals, false)

	admission, err := h.prepareAllowance(nil)
	c.Assert(err, ErrorMatches, ".*offline.*")
	c.Check(admission.allowed, Equals, false)
	failed.Store(false)
	admission, err = h.prepareAllowance(nil)
	c.Assert(err, ErrorMatches, "transaction missing from ChainLocked block")
	c.Check(admission.allowed, Equals, false)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(100))
	next, pending := h.recovery.Next()
	c.Check(pending, Equals, true)
	c.Check(next, Equals, mined)

	valid.Store(true)
	admission, err = h.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Check(admission.allowed, Equals, false) // two more reservations need checking
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(40))
	admission, err = h.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Check(admission.allowed, Equals, false) // missing remains charged; continue the pass
	admission, err = h.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Check(admission.allowed, Equals, true)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(40))
	c.Check(state.Available(100), Equals, uint64(60))
	for _, id := range []string{mined, missing, unsettled} {
		var marked bool
		marked, err = client.temporalStorage.HasObservedTx(id)
		c.Assert(err, IsNil)
		c.Check(marked, Equals, true) // recovery must never enable duplicate observations
	}
}

// TestInstantSendGapSettlementFailureKeepsAllowanceBlocked verifies that a failed
// release across a gap retains the charge until a later successful settlement.
func (s *DashSuite) TestInstantSendGapSettlementFailureKeepsAllowanceBlocked(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	broken, closeBroken := newDashClientForRPCServer(c, "http://localhost:1")
	defer closeBroken()
	id := strings.Repeat("a", 64)
	for _, store := range []*sharedutxo.TemporalStorage{client.temporalStorage, broken.temporalStorage} {
		_, err := store.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{{TxID: id, Amount: 40}}, 0, 100, true, 1)
		c.Assert(err, IsNil)
	}
	closeBroken()
	h := &client.instantSend
	c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 100}), IsNil)
	healthy := client.temporalStorage
	client.temporalStorage = broken.temporalStorage
	block := &btcjson.GetBlockVerboseTxResult{Height: 110, Tx: []btcjson.TxRawResult{{Txid: id}}}
	err := h.reconcileBlock(block)
	client.temporalStorage = healthy
	c.Assert(err, ErrorMatches, ".*closed.*")
	c.Check(h.currentAdmission(instantSendAdmission{allowed: true}).allowed, Equals, false)
	state, err := healthy.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(40))
	c.Assert(h.reconcileBlock(block), IsNil)
	admission, err := h.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Check(admission.allowed, Equals, true)
	state, err = healthy.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(0))
}

// TestInstantSendGapInvalidatesRecoveryRPC ensures another scanner jump can reset
// recovery during a slow RPC without letting that old response advance the new pass.
func (s *DashSuite) TestInstantSendGapInvalidatesRecoveryRPC(c *C) {
	id := strings.Repeat("a", 64)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	var reads atomic.Int64
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		c.Assert(method, Equals, "getrawtransaction")
		reads.Add(1)
		enteredOnce.Do(func() { close(entered); <-release })
		return dashLockedRawTx(id)
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL, 10*time.Second)
	defer cleanup()
	defer unblock()
	_, err := client.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{{TxID: id, Amount: 40}}, 0, 100, true, 1)
	c.Assert(err, IsNil)
	h := &client.instantSend
	c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 100}), IsNil)
	c.Assert(h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 105}), IsNil)
	finished := make(chan error, 1)
	var admission instantSendAdmission
	go func() {
		var scanErr error
		admission, scanErr = h.prepareAllowance(nil)
		finished <- scanErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		c.Fatal("recovery did not reach its RPC")
	}
	settled := make(chan error, 1)
	go func() { settled <- h.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Height: 110}) }()
	select {
	case err = <-settled:
		c.Assert(err, IsNil)
	case <-time.After(5 * time.Second):
		c.Fatal("scanner jump waited for the recovery RPC")
	}
	unblock()
	c.Assert(<-finished, IsNil)
	c.Check(admission.allowed, Equals, false)
	c.Check(h.recovery.loaded, Equals, false)
	admission, err = h.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Check(admission.allowed, Equals, true)
	c.Check(reads.Load(), Equals, int64(2))
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(40))
}
