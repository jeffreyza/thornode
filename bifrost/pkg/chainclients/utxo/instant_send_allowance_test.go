package utxo

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type instantSendPolicyBridge struct {
	dashMempoolPolicyBridge
	values map[string]int64
	fail   string
	status ttypes.NodeStatus
	reads  []string
}

func (b *instantSendPolicyBridge) GetMimir(key string) (int64, error) {
	b.reads = append(b.reads, key)
	if key == b.fail {
		return -1, errors.New("Mimir unavailable")
	}
	if value, ok := b.values[key]; ok {
		return value, nil
	}
	return -1, nil
}

func (b *instantSendPolicyBridge) FetchNodeStatus() (ttypes.NodeStatus, error) { return b.status, nil }

func instantSendTestItem(f *dashMempoolFixture, id string) *types.TxInItem {
	return &types.TxInItem{
		Tx: id, Sender: f.user, To: f.asgard, BlockHeight: 100,
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One/10))),
	}
}

func freshInstantSendJournal(c *C, client *Client) func() {
	path := c.MkDir()
	storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
	c.Assert(err, IsNil)
	client.cfg.BlockScanner.DBPath = path
	client.instantSend.recovery.Reset()
	return func() { _ = storage.Close() }
}

func (s *DashSuite) TestInstantSendAllowanceWaitsForExactChainLock(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.instantSend.maxAmount = common.One / 10
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One/10))
	marked, err := client.temporalStorage.HasObservedTx(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	// A higher block that does not include our inbound cannot refill the budget.
	c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{
		Height: 200,
		Tx:     []btcjson.TxRawResult{{Txid: f.lockedOutbound}},
	}), IsNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Available(client.instantSend.maxAmount), Equals, uint64(0))
	// Both deposits reach a block. The early one is settled before deduplication;
	// the deferred one is emitted by the ordinary ChainLock path exactly once.
	block := &btcjson.GetBlockVerboseTxResult{Height: 201}
	for _, id := range []string{f.inbound, f.lowFeeInbound} {
		var raw btcjson.TxRawResult
		var data []byte
		data, err = json.Marshal(f.txs[id])
		c.Assert(err, IsNil)
		c.Assert(json.Unmarshal(data, &raw), IsNil)
		block.Tx = append(block.Tx, raw)
	}
	client.disableVinZeroBatch = true
	client.instantSend.enabled = false // settlement continues while disabled
	batch, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	batch, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
}

func (s *DashSuite) TestInstantSendAllowanceClaimsOnlyOneBoundedDeliveryBatch(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 1
	client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain, map[int64][]*types.TxInItem{
		100: {instantSendTestItem(f, f.inbound), instantSendTestItem(f, f.lowFeeInbound)},
	})
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(client.instantSend.txQueue, HasLen, 1)
	c.Assert(client.instantSend.txQueue[0].TxArray, HasLen, 1)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One/10))
	marked, err := client.temporalStorage.HasObservedTx(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(client.instantSend.txQueue, HasLen, 0)
}

func (s *DashSuite) TestInstantSendMimirDefaultsChangesAndFailures(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	// Use 1.5 DASH deposits to exercise lowering the limit below usage.
	for id, amount := range map[string]float64{
		f.inbound:               1.5,
		f.lowFeeInbound:         1.5,
		strings.Repeat("f", 64): 1.50002,
	} {
		outputs, ok := f.txs[id]["vout"].([]any)
		c.Assert(ok, Equals, true)
		output, ok := outputs[0].(map[string]any)
		c.Assert(ok, Equals, true)
		output["value"] = amount
	}
	bridge := &instantSendPolicyBridge{values: make(map[string]int64), status: ttypes.NodeStatus_Active}
	client.bridge = bridge
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.minAmount, Equals, uint64(common.One/100))
	c.Assert(client.instantSend.maxAmount, Equals, uint64(0))
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	bridge.values[DashInstantSendMinAmountMimirKey] = 0
	bridge.values[DashInstantSendMaxAmountMimirKey] = 2 * common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.maxAmount, Equals, uint64(2*common.One))
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	batch, err := client.instantSend.fetchBatch(123, admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	// A reduced limit retains the first transaction's full reservation.
	bridge.values[DashInstantSendMaxAmountMimirKey] = common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(3*common.One/2))
	c.Assert(state.Available(client.instantSend.maxAmount), Equals, uint64(0))
	bridge.values[DashInstantSendMaxAmountMimirKey] = 3 * common.One
	bridge.fail = DashInstantSendMinAmountMimirKey
	err = client.instantSend.refreshSettings()
	c.Assert(err, NotNil)
	admission, err = client.instantSend.prepareAllowance(err)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(admission.reason, Equals, "settings")
	batch, err = client.instantSend.fetchBatch(123, admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	bridge.fail = ""
	bridge.values[DashInstantSendMinAmountMimirKey] = 2 * common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	batch, err = client.instantSend.fetchBatch(123, admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0) // minimum now exceeds the deposit
	delete(bridge.values, DashInstantSendMinAmountMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.minAmount, Equals, defaultInstantSendMinAmount)
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	batch, err = client.instantSend.fetchBatch(123, admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
	delete(bridge.values, DashInstantSendMaxAmountMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.maxAmount, Equals, uint64(0))
}

func (s *DashSuite) TestInstantSendEnabledMimirDefaultsAndFailures(c *C) {
	for _, defaultEnabled := range []bool{false, true} {
		client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
		defer cleanup()
		client.instantSend.enabled = defaultEnabled
		client.cfg.InstantSend.Enabled = defaultEnabled
		bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
		client.bridge = bridge
		for _, tc := range []struct {
			value int64
			fail  bool
			want  bool
		}{
			{-1, true, false},
			{-1, false, defaultEnabled},
			{1, false, true},
			{-1, false, defaultEnabled},
			{0, false, false},
			{-1, false, defaultEnabled},
			{1, false, true},
			{1, true, false},
			{-1, false, defaultEnabled},
			{1, false, true},
			{-2, false, defaultEnabled},
		} {
			bridge.values[DashInstantSendEnabledMimirKey] = tc.value
			if tc.value == -1 {
				delete(bridge.values, DashInstantSendEnabledMimirKey)
			}
			bridge.fail = ""
			if tc.fail {
				bridge.fail = DashInstantSendEnabledMimirKey
			}
			bridge.reads = nil
			err := client.instantSend.refreshSettings()
			c.Check(err != nil, Equals, tc.fail)
			c.Check(client.instantSend.enabled, Equals, tc.want,
				Commentf("default: %t, value: %d, failed read: %t", defaultEnabled, tc.value, tc.fail))
			c.Check(client.GetConfig().InstantSend.Enabled, Equals, defaultEnabled)
			if tc.fail {
				c.Check(bridge.reads, DeepEquals, []string{DashInstantSendEnabledMimirKey})
			}
		}
		// Losing the bridge must also disable a previously enabled switch.
		bridge.values[DashInstantSendEnabledMimirKey] = 1
		c.Assert(client.instantSend.refreshSettings(), IsNil)
		client.bridge = nil
		c.Check(client.instantSend.refreshSettings(), NotNil)
		c.Check(client.instantSend.enabled, Equals, false)
		c.Check(client.GetConfig().InstantSend.Enabled, Equals, defaultEnabled)
	}
}

func (s *DashSuite) TestInstantSendConfigConcurrentReads(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
	client.bridge = bridge
	initialConfig := client.GetConfig()
	started, stop := make(chan struct{}), make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				_ = client.GetConfig()
			}
		}
	}()
	<-started
	defer func() { close(stop); readers.Wait() }()
	for i := range 100 {
		value := int64(i%4) - 1
		bridge.values[DashInstantSendEnabledMimirKey] = value
		bridge.fail = ""
		if value == 2 {
			bridge.fail = DashInstantSendEnabledMimirKey
		}
		err := client.instantSend.refreshSettings()
		c.Assert(err != nil, Equals, value == 2)
		cfg := client.GetConfig()
		c.Assert(cfg, DeepEquals, initialConfig)
		c.Assert(client.instantSend.enabled, Equals, value == -1 || value == 1)
	}
}

func (s *DashSuite) TestInstantSendEnabledReadFailureKeepsOutboundsAndSettlement(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.instantSend.maxAmount = common.One / 10
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
	bridge := &instantSendPolicyBridge{
		values: map[string]int64{DashInstantSendEnabledMimirKey: 1, DashInstantSendMaxAmountMimirKey: common.One},
		status: ttypes.NodeStatus_Active, fail: DashInstantSendEnabledMimirKey,
	}
	client.bridge = bridge
	client.nextMempoolScan = scanInstantSend
	vaultItem := instantSendTestItem(f, f.lockedOutbound)
	vaultItem.Sender = f.asgard
	client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
		map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.lowFeeInbound), vaultItem}})
	instantSendCalls := f.instantSendCalls

	batch, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Check(batch.MemPool, Equals, true)
	c.Check(batch.TxArray, HasLen, 2)
	c.Check(client.instantSend.txQueue, HasLen, 0)
	c.Check(client.nextMempoolScan, Equals, scanOutbounds)
	c.Check(f.instantSendCalls, Equals, instantSendCalls)
	c.Check(bridge.reads, DeepEquals, []string{DashInstantSendEnabledMimirKey})
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(common.One/10))
	for _, id := range []string{f.lowFeeInbound, f.lockedOutbound} {
		var observed bool
		observed, err = client.temporalStorage.HasObservedTx(id)
		c.Assert(err, IsNil)
		c.Check(observed, Equals, false)
	}

	// Settlement releases the existing reservation while the switch is disabled.
	c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{
		Height: 200, Tx: []btcjson.TxRawResult{{Txid: f.inbound}},
	}), IsNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(0))
	bridge.fail = ""
	_, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	batch, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Check(batch.MemPool, Equals, false)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Check(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
}

func (s *DashSuite) TestInstantSendAmountMimirsUseDuffs(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
	client.bridge = bridge
	for _, tc := range []struct {
		value   int64
		minimum uint64
		maximum uint64
	}{
		{-1, common.One / 100, 0},
		{0, 0, 0},
		{1, 1, 1},
		{common.One / 100, common.One / 100, common.One / 100},
		{common.One, common.One, common.One},
		{1000 * common.One, 1000 * common.One, 1000 * common.One},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64},
	} {
		bridge.values[DashInstantSendMinAmountMimirKey] = tc.value
		bridge.values[DashInstantSendMaxAmountMimirKey] = tc.value
		c.Assert(client.instantSend.refreshSettings(), IsNil)
		c.Assert(client.instantSend.minAmount, Equals, tc.minimum, Commentf("Mimir value %d", tc.value))
		c.Assert(client.instantSend.maxAmount, Equals, tc.maximum, Commentf("Mimir value %d", tc.value))
	}
	bridge.values[DashInstantSendMaxAmountMimirKey] = 1000 * common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	bridge.fail = DashInstantSendMaxAmountMimirKey
	c.Assert(client.instantSend.refreshSettings(), NotNil)
	bridge.fail = ""
	delete(bridge.values, DashInstantSendMaxAmountMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.maxAmount, Equals, uint64(0))
}

func (s *DashSuite) TestInstantSendVaultSenderBypassesAllowance(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.instantSend.maxAmount = 0
	client.instantSend.minAmount = 100 * common.One
	client.cfg.BlockScanner.DBPath = "" // customer observations require durable storage
	item := instantSendTestItem(f, f.lockedOutbound)
	item.Sender = f.asgard
	for _, destination := range []string{f.user, f.asgard} {
		item.To = destination // outbound and vault-to-vault have the same exemption
		accepted, err := client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.inbound), item}, instantSendAdmission{})
		c.Assert(err, IsNil)
		c.Assert(accepted, HasLen, 1)
		c.Assert(accepted[0].Tx, Equals, f.lockedOutbound)
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(0))
		c.Assert(client.temporalStorage.UntrackObservedTx(item.Tx), IsNil)
	}
}

func (s *DashSuite) TestInstantSendFetchMemPoolPausesOnAmountReadFailure(c *C) {
	for _, key := range []string{DashInstantSendMinAmountMimirKey, DashInstantSendMaxAmountMimirKey} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		client.cfg.UTXO.TransactionBatchSize = 1
		client.cfg.UTXO.MaxMempoolBatches = 10
		bridge := &instantSendPolicyBridge{
			values: map[string]int64{DashInstantSendEnabledMimirKey: 1, DashInstantSendMaxAmountMimirKey: common.One},
			status: ttypes.NodeStatus_Active,
		}
		client.bridge = bridge
		client.nextMempoolScan = scanInstantSend
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)

		// The next scan must not reuse the previous scan's successful admission.
		bridge.fail = key
		client.nextMempoolScan = scanInstantSend
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(common.One/10))
		observed, err := client.temporalStorage.HasObservedTx(f.lowFeeInbound)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, false)

		bridge.fail = ""
		client.nextMempoolScan = scanInstantSend
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
	}
}

func (s *DashSuite) TestInstantSendSettlementFailureInvalidatesPreparedAdmission(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)

	// A failed block settlement between preparation and delivery resets recovery.
	closed, closeStorage := newDashClientForRPCServer(c, "http://localhost:1")
	_, err = closed.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{{TxID: f.lockedOutbound, Amount: 1}}, 0, 100, true, 1)
	c.Assert(err, IsNil)
	healthyStorage := client.temporalStorage
	client.temporalStorage = closed.temporalStorage
	closeStorage()
	err = client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Tx: []btcjson.TxRawResult{{Txid: f.lockedOutbound}}})
	c.Assert(err, NotNil)
	c.Assert(client.instantSend.recovery.Complete(), Equals, false)
	client.temporalStorage = healthyStorage

	item := instantSendTestItem(f, f.inbound)
	accepted, err := client.instantSend.claimBatch([]*types.TxInItem{item}, admission)
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	observed, err := client.temporalStorage.HasObservedTx(f.inbound)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)

	// An empty journal becomes ready only after recovery has loaded it again.
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	c.Assert(client.instantSend.recovery.Complete(), Equals, true)
	accepted, err = client.instantSend.claimBatch([]*types.TxInItem{item}, admission)
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 1)
}

func (s *DashSuite) TestInstantSendSnapshotIgnoresMarkersAndNewPolicy(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	defer freshInstantSendJournal(c, client)()
	_, err := client.temporalStorage.TrackObservedTx(f.inbound)
	c.Assert(err, IsNil)
	client.instantSend.minAmount = common.One
	client.instantSend.maxAmount = common.One / 10
	f.entries[f.inbound]["time"] = time.Now().Add(-time.Hour).Unix()
	c.Assert(client.temporalStorage.StartInstantSendWarmup(100, []string{f.inbound, f.lowFeeInbound, f.lockedOutbound}), IsNil)
	client.cfg.UTXO.TransactionBatchSize = 1 // bounded work, persisted between calls
	for range 3 {
		var state sharedutxo.InstantSendState
		state, err = client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		client.instantSend.stateMu.Lock()
		err = client.instantSend.classifySnapshot(state)
		client.instantSend.stateMu.Unlock()
		c.Assert(err, IsNil)
	}
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.SnapshotPending, HasLen, 0)
	c.Assert(state.Reserved, Equals, uint64(common.One/5))
	c.Assert(state.Count, Equals, 2) // vault sender excluded
	c.Assert(state.Reservations[f.inbound].Origin, Equals, sharedutxo.InstantSendSnapshot)
	marked, err := client.temporalStorage.HasObservedTx(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	c.Assert(client.temporalStorage.CompleteInstantSendWarmup(101), IsNil)

	client.instantSend.recovery.Load(nil)
	client.instantSend.maxAmount = common.One
	client.instantSend.minAmount = 0
	client.cfg.UTXO.TransactionBatchSize = 10 // reach the customer candidate in this scan
	calls := countInstantSendRPCs(c, f)
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0) // snapshot deposits remain ChainLock-only
	// Both scan and final claim checks reached this deposit; it was not skipped
	// because of pagination, an observed marker or the old amount policy.
	c.Assert(calls("getislocks/"+f.lowFeeInbound), Equals, 1)
	c.Assert(calls("getrawtransaction/"+f.lowFeeInbound), Equals, 2)
	marked, err = client.temporalStorage.HasObservedTx(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One/5))
	c.Assert(state.Reservations[f.lowFeeInbound].Origin, Equals, sharedutxo.InstantSendSnapshot)
}

func (s *DashSuite) TestInstantSendSnapshotRetriesMissingTransaction(c *C) {
	var fail atomic.Bool
	fail.Store(true)
	id := strings.Repeat("a", 64)
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		c.Assert(method, Equals, "getrawtransaction")
		if fail.Load() {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "offline"}
		}
		return dashUnlockedRawTx(id)
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	defer freshInstantSendJournal(c, client)()
	client.asgardCache.Store(&sharedutxo.AsgardCache{FetchedAt: time.Now()})
	c.Assert(client.temporalStorage.StartInstantSendWarmup(10, []string{id}), IsNil)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	client.instantSend.stateMu.Lock()
	err = client.instantSend.classifySnapshot(state)
	client.instantSend.stateMu.Unlock()
	c.Assert(err, NotNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.SnapshotPending, DeepEquals, []string{id})
	fail.Store(false)
	client.instantSend.stateMu.Lock()
	err = client.instantSend.classifySnapshot(state)
	client.instantSend.stateMu.Unlock()
	c.Assert(err, IsNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.SnapshotPending, HasLen, 0)
}

func (s *DashSuite) TestInstantSendQueuedTransactionCannotReReserveAfterSettlement(c *C) {
	id := strings.Repeat("a", 64)
	var locked atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		c.Assert(method, Equals, "getrawtransaction")
		once.Do(func() { close(entered); <-release })
		tx := dashLockedRawTx(id)
		tx["chainlock"] = locked.Load()
		return tx
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL, 10*time.Second)
	defer cleanup()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	client.asgardCache.Store(&sharedutxo.AsgardCache{FetchedAt: time.Now()})
	item := &types.TxInItem{Tx: id, Sender: "user", Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))}
	claimed := make(chan error, 1)
	var accepted []*types.TxInItem
	go func() {
		var err error
		accepted, err = client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{allowed: true})
		claimed <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		c.Fatal("claim did not reach the finality RPC")
	}
	settled := make(chan error, 1)
	go func() {
		settled <- client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Tx: []btcjson.TxRawResult{{Txid: id}}})
	}()
	select {
	case err := <-settled:
		c.Assert(err, IsNil)
	case <-time.After(5 * time.Second):
		c.Fatal("block settlement waited for the finality RPC")
	}
	unblock()
	c.Assert(<-claimed, IsNil)
	c.Assert(accepted, HasLen, 0)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	marked, err := client.temporalStorage.HasObservedTx(id)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	// Simulate the block scan winning before a queued delivery (even without a
	// marker). The fresh ChainLock read prevents a new reservation.
	c.Assert(client.temporalStorage.UntrackObservedTx(id), IsNil)
	locked.Store(true)
	accepted, err = client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
}

func (s *DashSuite) TestInstantSendWarmupNeedsActiveNodeAndCompletedScannerHeight(c *C) {
	var height atomic.Int64
	height.Store(100)
	var snapshots atomic.Int64
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getbestchainlock":
			return map[string]any{"height": height.Load(), "known_block": true, "blockhash": dashCatchUpHash(height.Load())}
		case "getblockhash":
			return dashCatchUpHash(height.Load())
		case "getblockcount":
			return height.Load()
		case "getrawmempool":
			if len(params) == 0 {
				return []string{} // ordinary outbound fallback during a halt
			}
			snapshots.Add(1)
			return map[string]any{}
		default:
			c.Fatalf("unexpected RPC %s", method)
			return nil
		}
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	defer freshInstantSendJournal(c, client)()
	bridge := &instantSendPolicyBridge{
		status: ttypes.NodeStatus_Active,
		values: map[string]int64{DashInstantSendEnabledMimirKey: 1, DashInstantSendMaxAmountMimirKey: 100 * common.One},
	}
	client.bridge = bridge
	// An inactive or unhealthy node must not initialize an empty allowance.
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(snapshots.Load(), Equals, int64(0))
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer storage.Close()
	fetcher := &dashHealthFetcher{}
	fetcher.height.Store(100)
	scanner, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.DASHChain, StartBlockHeight: 99,
		BlockHeightDiscoverBackoff: time.Millisecond, MaxHealthyLag: 30 * time.Minute,
	}, storage, GetMetricForTest(c, common.DASHChain), dashHealthBridge{}, fetcher)
	c.Assert(err, IsNil)
	client.blockScanner = scanner
	scanner.Start(make(chan types.TxIn), make(chan common.NetworkFee))
	defer scanner.Stop()
	waitForDashCatchUp(c, func() bool { return scanner.PreviousHeight() == 100 && scanner.IsHealthy() })
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	// Even a fully caught-up active node must not start a snapshot while the
	// halt or enabled switch is unavailable. Keep the amount limit positive.
	for _, key := range []string{DashInstantSendEnabledMimirKey, "HaltDASHChain"} {
		bridge.fail = key
		client.nextMempoolScan = scanInstantSend
		var batch types.TxIn
		batch, err = client.FetchMemPool(100)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		c.Assert(snapshots.Load(), Equals, int64(0))
		var state sharedutxo.InstantSendState
		state, err = client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Started, Equals, false)
	}
	bridge.fail = ""
	bridge.status = ttypes.NodeStatus_Standby
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(snapshots.Load(), Equals, int64(0))
	bridge.status = ttypes.NodeStatus_Active
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(snapshots.Load(), Equals, int64(1))
	client.currentBlockHeight.Store(1000) // prefetched height is not completed work
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(snapshots.Load(), Equals, int64(1)) // finite snapshot, not rebuilt each poll
	height.Store(101)
	fetcher.height.Store(101)
	waitForDashCatchUp(c, func() bool { return scanner.PreviousHeight() == 101 })
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
}

func (s *DashSuite) TestInstantSendRestoredAllowanceRequiresInclusionProof(c *C) {
	mined, missing := strings.Repeat("a", 64), strings.Repeat("b", 64)
	hash := strings.Repeat("c", 64)
	var valid atomic.Bool
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if id == missing {
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
			}
			return map[string]any{"txid": id, "chainlock": true, "blockhash": hash}
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
		{TxID: mined, Amount: 60}, {TxID: missing, Amount: 40},
	}, 0, 100, true, 1)
	c.Assert(err, IsNil)
	client.instantSend.maxAmount = 100
	client.instantSend.recovery.Reset()
	client.cfg.UTXO.TransactionBatchSize = 1
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, NotNil) // ChainLock flag alone cannot release the reservation
	c.Assert(admission.allowed, Equals, false)
	next, pending := client.instantSend.recovery.Next()
	c.Assert(pending, Equals, true)
	c.Assert(next, Equals, mined) // failed checks retain their position for retry
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(100))
	valid.Store(true)
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false) // bounded restoration has another ID to check
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	c.Assert(client.instantSend.recovery.Complete(), Equals, true)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(40)) // vanished transaction stays charged
	c.Assert(state.Available(100), Equals, uint64(60))
	marked, err := client.temporalStorage.HasObservedTx(mined)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, true)
	// Recovery continues with admission disabled, limit 0, or unavailable settings.
	for _, tc := range []struct {
		disabled    bool
		limit       uint64
		settingsErr error
	}{
		{limit: 0},
		{disabled: true, limit: 100},
		{limit: 100, settingsErr: errors.New("Mimir unavailable")},
		{limit: 100, settingsErr: errInstantSendPaused},
	} {
		store, closeStore := newDashClientForRPCServer(c, server.URL)
		_, err := store.temporalStorage.ClaimInstantSendTxs([]sharedutxo.InstantSendClaim{{TxID: mined, Amount: 60}}, 0, 100, true, 1)
		c.Assert(err, IsNil)
		store.instantSend.recovery.Reset()
		store.instantSend.enabled = !tc.disabled
		store.instantSend.maxAmount = tc.limit
		admission, err := store.instantSend.prepareAllowance(tc.settingsErr)
		c.Assert(err, IsNil)
		c.Assert(admission.allowed, Equals, false)
		if tc.settingsErr != nil {
			c.Assert(admission.reason, Equals, "settings")
		}
		state, err := store.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(0))
		closeStore()
	}
}
