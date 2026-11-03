package utxo

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"golang.org/x/sync/errgroup"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// instantSendHandler owns InstantSend scanning, allowance and recovery state.
// It shares the client's dependencies and must not be copied after first use.
type instantSendHandler struct {
	client *Client

	// Owned by the single mempool scanner; rebuilt after restart.
	mempoolCursor string
	// Transactions with no vault output, cached only for this vault-address set.
	ignored map[string]struct{}
	vaults  []string
	// Transaction batches awaiting final checks and delivery.
	txQueue []types.TxIn
	// Amount settings are owned by the mempool scanner.
	// Both amounts and their Mimir values use duffs (common.One per DASH).
	minAmount uint64
	maxAmount uint64

	// Serializes allowance writes, recovery progress, block height, revision and switch updates.
	// Release during RPCs so ChainLock settlement can continue. After relocking,
	// check the revision and keep the lock through the resulting state updates.
	stateMu sync.Mutex
	// Effective switch after applying Mimir; cfg retains the startup value.
	enabled bool
	// Incremented on settlement attempts to reject stale RPC results.
	revision uint64
	recovery instantSendRecovery
	// Last processed block; zero before scanning. Protected by stateMu.
	lastBlockHeight int64
}

const (
	DashInstantSendEnabledMimirKey   = "DashInstantSendEnabled"
	DashInstantSendMinAmountMimirKey = "DashInstantSendMinAmount"
	DashInstantSendMaxAmountMimirKey = "DashInstantSendMaxAmount"
)

const defaultInstantSendMinAmount uint64 = common.One / 100 // 0.01 DASH, in duffs

var errInstantSendPaused = errors.New("InstantSend observations paused")

// isPaused follows blockscanner.IsChainPaused on successful reads.
// Unlike the shared scanner, it defers final observations if any read fails.
func (h *instantSendHandler) isPaused() bool {
	if h.client.bridge == nil {
		h.client.log.Error().Msg("InstantSend halt bridge unavailable")
		return true
	}
	thorHeight, err := h.client.bridge.GetBlockHeight()
	if err != nil {
		h.client.log.Err(err).Msg("fail to get THORChain height for InstantSend halt check")
		return true
	}
	var height int64
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal"} {
		height, err = h.client.bridge.GetMimir(key)
		if err != nil {
			h.client.log.Err(err).Str("key", key).Msg("fail to read InstantSend halt setting")
			return true
		}
		if height > 0 && thorHeight >= height {
			return true
		}
	}
	height, err = h.client.bridge.GetMimir("NodePauseChainGlobal")
	if err != nil {
		h.client.log.Err(err).Msg("fail to read InstantSend node pause setting")
		return true
	}
	return height > 0 && thorHeight <= height
}

// refreshSettings reads the InstantSend switch and amount policy.
// An unset switch restores the startup default; a failed read disables InstantSend.
func (h *instantSendHandler) refreshSettings() error {
	var enabled int64
	var err error
	if h.client.bridge == nil {
		err = errors.New("InstantSend settings bridge unavailable")
	} else {
		enabled, err = h.client.bridge.GetMimir(DashInstantSendEnabledMimirKey)
	}

	effectiveEnabled := false
	if err == nil {
		effectiveEnabled = h.client.cfg.InstantSend.Enabled
		if enabled >= 0 {
			effectiveEnabled = enabled > 0
		}
	}
	h.stateMu.Lock()
	h.enabled = effectiveEnabled
	h.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("fail to read InstantSend enabled switch: %w", err)
	}
	minAmount, err := h.client.bridge.GetMimir(DashInstantSendMinAmountMimirKey)
	if err != nil {
		return fmt.Errorf("fail to read InstantSend minimum amount: %w", err)
	}
	maxAmount, err := h.client.bridge.GetMimir(DashInstantSendMaxAmountMimirKey)
	if err != nil {
		return fmt.Errorf("fail to read InstantSend maximum amount: %w", err)
	}
	h.minAmount = defaultInstantSendMinAmount
	if minAmount >= 0 {
		h.minAmount = uint64(minAmount)
	}
	h.maxAmount = 0
	if maxAmount > 0 {
		h.maxAmount = uint64(maxAmount)
	}
	return nil
}

// allowAge applies our InstantSend policy for mempool observations.
//
// When this returns false, we intentionally do NOT emit the tx via the
// InstantSend mempool path and instead wait for the normal block-based flow
// (which checks the fetched block's ChainLock).
func (h *instantSendHandler) allowAge(age time.Duration) (bool, string) {
	if h.client.cfg.InstantSend.MaxMempoolAge > 0 && age > h.client.cfg.InstantSend.MaxMempoolAge {
		return false, fmt.Sprintf("mempool age too high: %v > %v", age, h.client.cfg.InstantSend.MaxMempoolAge)
	}
	return true, ""
}

// observationHeight uses the lock's network-agreed cycle, independent
// of scanner lag and reorg retention settings. Core validates quorum eligibility.
func (h *instantSendHandler) observationHeight(lock *rpc.ISLockResult, _ int64) (int64, error) {
	if lock == nil || strings.TrimSpace(lock.CycleHash) == "" {
		return 0, errors.New("missing InstantSend cycle hash")
	}
	block, err := h.client.rpc.GetBlockVerbose(lock.CycleHash)
	if err != nil {
		return 0, fmt.Errorf("fail to fetch InstantSend cycle block: %w", err)
	}
	if block.Height <= 0 || block.Confirmations <= 0 || !strings.EqualFold(block.Hash, lock.CycleHash) {
		return 0, errors.New("InstantSend cycle block is not in the active chain")
	}
	return block.Height, nil
}

// refreshVaults invalidates unrelated transaction IDs when vaults change
// and prunes IDs that left the mempool. Only the mempool scanner owns this cache.
func (h *instantSendHandler) refreshVaults(mempool map[string]rpc.GetRawMempoolVerboseResult) {
	addresses, err := h.client.getAsgardAddress()
	if err != nil {
		h.client.log.Debug().Err(err).Msg("skip InstantSend output filter while vault addresses are unavailable")
		clear(h.ignored)
		h.vaults = nil
		return
	}
	vaults := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if !address.IsEmpty() {
			vaults = append(vaults, strings.ToLower(address.String()))
		}
	}
	slices.Sort(vaults)
	vaults = slices.Compact(vaults)
	if !slices.Equal(vaults, h.vaults) || h.client.cfg.MempoolTxIDCacheSize <= 0 {
		clear(h.ignored)
	}
	h.vaults = vaults
	for id := range h.ignored {
		if _, present := mempool[id]; !present {
			delete(h.ignored, id)
		}
	}
}

// mayPayVault excludes only transactions with clearly unrelated outputs.
// Missing vault data or uncertain script decoding leaves the normal scan in charge.
func (h *instantSendHandler) mayPayVault(tx *rpc.TxResult) bool {
	if len(h.vaults) == 0 || len(tx.Vout) == 0 {
		return true
	}
	for _, out := range tx.Vout {
		addresses := h.client.getAddressesFromScriptPubKey(out.ScriptPubKey)
		if len(addresses) == 0 {
			// OP_RETURN cannot pay a vault; other undecoded scripts remain retryable.
			script, err := hex.DecodeString(out.ScriptPubKey.Hex)
			if err != nil || len(script) == 0 || script[0] != txscript.OP_RETURN {
				return true
			}
		}
		for _, address := range addresses {
			if _, found := slices.BinarySearch(h.vaults, strings.ToLower(address)); found {
				return true
			}
		}
	}
	return false
}

// candidates rotates through the mempool so a bounded scan cannot starve
// later txids behind candidates whose locks or policy checks are still pending.
func (h *instantSendHandler) candidates(mempool map[string]rpc.GetRawMempoolVerboseResult) ([]string, error) {
	ids := make([]string, 0, len(mempool))
	for id, entry := range mempool {
		if entry.InstantLock != "false" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	limit := len(ids)
	if h.client.cfg.UTXO.TransactionBatchSize > 0 && h.client.cfg.UTXO.MaxMempoolBatches > 0 {
		limit = min(limit, h.client.cfg.UTXO.TransactionBatchSize*h.client.cfg.UTXO.MaxMempoolBatches)
	}
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > h.mempoolCursor })
	candidates := make([]string, 0, limit)
	for offset := 0; offset < len(ids) && len(candidates) < limit; offset++ {
		id := ids[(start+offset)%len(ids)]
		h.mempoolCursor = id
		if _, ignored := h.ignored[id]; ignored {
			continue
		}
		observed, err := h.client.temporalStorage.HasObservedTx(strings.ToLower(id))
		if err != nil {
			return nil, fmt.Errorf("fail to check observation marker for %s: %w", id, err)
		}
		if !observed {
			candidates = append(candidates, id)
		}
	}
	return candidates, nil
}

// fetchBatch returns one queued observation batch, scanning for more
// when the queue is empty. Only the returned batch is marked as observed; the
// remaining batches can still be retried or discarded after a settings change.
func (h *instantSendHandler) fetchBatch(height int64, admission instantSendAdmission) (types.TxIn, error) {
	if len(h.txQueue) == 0 {
		groups, err := h.scan(height)
		if err != nil {
			return types.TxIn{}, err
		}
		h.txQueue = groups
	}
	if len(h.txQueue) == 0 {
		return types.TxIn{Chain: h.client.GetChain()}, nil
	}
	batch := h.txQueue[0]
	// A cycle group can span several mempool batches. Bound the fresh finality
	// RPC and allowance write batch, keeping the remainder queued.
	count := min(len(batch.TxArray), h.workLimit())
	batch.TxArray = batch.TxArray[:count]
	var err error
	batch.TxArray, err = h.claimBatch(batch.TxArray, admission)
	if err != nil {
		return types.TxIn{}, err
	}
	if count == len(h.txQueue[0].TxArray) {
		h.txQueue[0] = types.TxIn{}
		h.txQueue = h.txQueue[1:]
	} else {
		h.txQueue[0].TxArray = h.txQueue[0].TxArray[count:]
	}
	if h.client.m != nil {
		if counter := h.client.m.GetCounter(metrics.DashInstantSendObserved); counter != nil {
			counter.Add(float64(len(batch.TxArray)))
		}
	}
	return batch, nil
}

// scan collects a bounded set of locked mempool candidates, grouped by
// lock cycle height. Allowance checks and observation claims happen before delivery.
func (h *instantSendHandler) scan(height int64) ([]types.TxIn, error) {
	// Only Dash currently supports the lock RPCs used by this scan.
	if !h.client.GetChain().Equals(common.DASHChain) {
		return nil, nil
	}
	mempool, err := h.client.rpc.GetRawMempoolVerbose()
	if err != nil {
		return nil, fmt.Errorf("fail to get %s mempool: %w", h.client.GetChain(), err)
	}
	h.refreshVaults(mempool)
	ids, err := h.candidates(mempool)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	workers := h.client.cfg.ParallelMempoolScan
	if workers <= 0 {
		workers = 5
	}
	// Indexed results avoid shared slice mutation and retain candidate order.
	raw := make([]*rpc.TxResult, len(ids))
	var group errgroup.Group
	group.SetLimit(workers)
	for i, id := range ids {
		group.Go(func() error {
			tx, rpcErr := h.client.rpc.GetRawTransactionVerbose(id)
			if rpcErr != nil {
				h.client.log.Debug().Err(rpcErr).Str("txid", id).Msg("retry InstantSend mempool transaction later")
			} else if strings.EqualFold(tx.Txid, id) && tx.InstantSendLocked() {
				raw[i] = tx
			}
			return nil
		})
	}
	err = group.Wait()
	if err != nil {
		return nil, err
	}
	lockedIDs := make([]string, 0, len(ids))
	lockedTxs := make([]*rpc.TxResult, 0, len(ids))
	for i, tx := range raw {
		if tx == nil {
			continue
		}
		if !h.mayPayVault(tx) {
			if len(h.ignored) < h.client.cfg.MempoolTxIDCacheSize {
				if h.ignored == nil {
					h.ignored = make(map[string]struct{})
				}
				h.ignored[ids[i]] = struct{}{}
			}
			continue
		}
		lockedIDs = append(lockedIDs, ids[i])
		lockedTxs = append(lockedTxs, tx)
	}
	locks, err := h.client.rpc.GetISLocks(lockedIDs)
	if err != nil {
		// Successful chunks can still progress. Failed chunks have nil entries.
		h.client.log.Debug().Err(err).Msg("retry missing InstantSend lock batches later")
	}
	cycleHeights := make(map[string]int64)
	items := make([]*types.TxInItem, len(locks))
	for i, lock := range locks {
		if lock == nil {
			continue
		}
		anchor, found := cycleHeights[lock.CycleHash]
		if !found {
			anchor, err = h.observationHeight(lock, height)
			if err != nil {
				h.client.log.Debug().Err(err).Str("txid", lock.TxID).Msg("retry InstantSend cycle lookup later")
				continue
			}
			cycleHeights[lock.CycleHash] = anchor
		}
		group.Go(func() error {
			items[i] = h.buildTxInItem(lockedTxs[i], anchor, mempool[lockedIDs[i]])
			return nil
		})
	}
	err = group.Wait()
	if err != nil {
		return nil, err
	}
	byHeight := make(map[int64][]*types.TxInItem)
	for _, item := range items {
		if item != nil {
			byHeight[item.BlockHeight] = append(byHeight[item.BlockHeight], item)
		}
	}
	return instantSendObservationGroups(h.client.GetChain(), byHeight), nil
}

// buildTxInItem extracts a vault inbound at the lock cycle height.
// It returns nil if extraction fails or the recipient, dust, or age checks reject it.
func (h *instantSendHandler) buildTxInItem(tx *rpc.TxResult, anchor int64, entry rpc.GetRawMempoolVerboseResult) *types.TxInItem {
	item, err := h.client.getTxIn(&tx.TxRawResult, anchor, true, nil)
	if err != nil {
		h.client.log.Debug().Err(err).Str("txid", tx.Txid).Msg("retry InstantSend inbound extraction later")
		return nil
	}
	if item.IsEmpty() || item.Coins.IsEmpty() {
		return nil
	}
	if !h.client.isAsgardAddress(item.To) {
		return nil
	}
	if item.Coins[0].Amount.LT(h.client.cfg.ChainID.DustThreshold()) {
		return nil
	}
	var age time.Duration
	if entry.Time > 0 {
		age = time.Since(time.Unix(entry.Time, 0))
	}
	allow, reason := h.allowAge(age)
	if !allow {
		h.client.log.Debug().Str("txid", item.Tx).Str("reason", reason).Msg("InstantSend transaction waiting for block observation")
		return nil
	}
	return &item
}

// instantSendObservationGroups builds final batches ordered by lock cycle height.
// Observer uses a single finalization height for each batch. Mixing cycle heights
// would make the signed observation depend on which worker happened to finish first.
func instantSendObservationGroups(chain common.Chain, byHeight map[int64][]*types.TxInItem) []types.TxIn {
	heights := make([]int64, 0, len(byHeight))
	for height := range byHeight {
		heights = append(heights, height)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	groups := make([]types.TxIn, 0, len(heights))
	for _, height := range heights {
		groups = append(groups, types.TxIn{
			Chain: chain,
			// Compatibility with Observer's finalized-inbound path. These items
			// have Core-validated finality despite originating in the mempool.
			MemPool:              false,
			ConfirmationRequired: 0,
			TxArray:              byHeight[height],
		})
	}
	return groups
}
