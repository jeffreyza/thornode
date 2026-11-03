package utxo

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// errInstantSendStateChanged defers RPC results invalidated by block settlement.
var errInstantSendStateChanged = errors.New("InstantSend state changed during RPC checks")

// instantSendAdmission belongs to one scan, so a previous success cannot hide a
// failed settings read or preparation error on the next scan.
type instantSendAdmission struct {
	allowed bool
	reason  string // deferral metric label when customer admission is blocked
}

// instantSendRecovery tracks the reservation checks for this process. It does
// not own the durable reservations or require them all to settle before completion.
// All access is protected by stateMu.
type instantSendRecovery struct {
	loaded       bool
	pendingTxIDs []string
}

// Load captures reservation IDs in a stable order once per recovery pass.
func (r *instantSendRecovery) Load(reservations map[string]sharedutxo.InstantSendReservation) {
	if r.loaded {
		return
	}
	r.pendingTxIDs = make([]string, 0, len(reservations))
	for id := range reservations {
		r.pendingTxIDs = append(r.pendingTxIDs, id)
	}
	slices.Sort(r.pendingTxIDs)
	r.loaded = true
}

// Next returns the next reservation ID without advancing, or false when none remain.
func (r *instantSendRecovery) Next() (string, bool) {
	if len(r.pendingTxIDs) == 0 {
		return "", false
	}
	return r.pendingTxIDs[0], true
}

// Advance is called only after Next returned a transaction and its check succeeded.
func (r *instantSendRecovery) Advance() {
	r.pendingTxIDs[0] = ""
	r.pendingTxIDs = r.pendingTxIDs[1:]
}

// Complete reports whether reservations were loaded and every recovery check finished.
func (r *instantSendRecovery) Complete() bool {
	return r.loaded && len(r.pendingTxIDs) == 0
}

// Reset clears local recovery progress so the durable reservations are checked again.
func (r *instantSendRecovery) Reset() {
	*r = instantSendRecovery{}
}

// lockAndCheckRevision locks state and checks whether the revision is unchanged.
// The mutex remains locked on both true and false returns.
func (h *instantSendHandler) lockAndCheckRevision(revision uint64) bool {
	h.stateMu.Lock()
	return revision == h.revision
}

// currentAdmission rechecks recovery immediately before reserving
// allowance or reporting availability. Call with stateMu held.
func (h *instantSendHandler) currentAdmission(admission instantSendAdmission) instantSendAdmission {
	if !admission.allowed {
		if admission.reason == "" {
			admission.reason = "not_ready"
		}
		return admission
	}
	// Customer admission needs InstantSend enabled and a positive allowance.
	if !h.enabled || h.maxAmount == 0 {
		return instantSendAdmission{reason: "not_ready"}
	}
	// Reservations must survive a restart.
	if h.client.cfg.BlockScanner.DBPath == "" {
		return instantSendAdmission{reason: "not_ready"}
	}
	// Finish checking existing reservations before admitting new deposits.
	if !h.recovery.Complete() {
		return instantSendAdmission{reason: "not_ready"}
	}
	return admission
}

// prepareAllowance resumes durable state before admitting customers.
// Only a missing journal needs a snapshot. Existing reservations survive restarts,
// disabled InstantSend, Mimir changes, mempool eviction and lost lock flags.
func (h *instantSendHandler) prepareAllowance(err error) (instantSendAdmission, error) {
	h.stateMu.Lock()
	// Every RPC section reacquires the lock before returning, so this stays balanced.
	defer h.stateMu.Unlock()
	revision := h.revision
	blocked := instantSendAdmission{reason: "not_ready"}
	if err != nil {
		blocked.reason = "settings"
	}
	if h.client.cfg.BlockScanner.DBPath == "" {
		return blocked, nil
	}
	// Disabling admission must not prevent settlement of an existing journal.
	canAdmit := false
	var statusErr error
	if h.enabled && err == nil && h.maxAmount > 0 && h.client.bridge != nil {
		var status ttypes.NodeStatus
		// Allow settlement while waiting for THORNode's status response.
		h.stateMu.Unlock()
		status, statusErr = h.client.bridge.FetchNodeStatus()
		// Check for intervening settlement before using the response.
		if !h.lockAndCheckRevision(revision) {
			return blocked, nil
		}
		canAdmit = statusErr == nil && status == ttypes.NodeStatus_Active
	}
	state, err := h.client.temporalStorage.GetInstantSendState()
	if err != nil {
		return blocked, err
	}
	if !state.Started {
		if !canAdmit {
			return blocked, statusErr
		}
		if h.client.blockScanner == nil || !h.client.IsBlockScannerHealthy() {
			return blocked, nil
		}
		var height int64
		// Allow settlement while reading the Dash height.
		h.stateMu.Unlock()
		height, err = h.client.GetHeight()
		if !h.lockAndCheckRevision(revision) {
			return blocked, nil
		}
		if err != nil {
			return blocked, err
		}
		// Scanner health allows a small lag; a new snapshot requires full catch-up.
		if !h.client.IsBlockScannerHealthy() || h.client.blockScanner.PreviousHeight() < height {
			return blocked, nil
		}
		var mempool map[string]rpc.GetRawMempoolVerboseResult
		// Allow settlement while fetching the mempool snapshot.
		h.stateMu.Unlock()
		mempool, err = h.client.rpc.GetRawMempoolVerbose()
		if !h.lockAndCheckRevision(revision) {
			return blocked, nil
		}
		if err != nil {
			return blocked, err
		}
		ids := make([]string, 0, len(mempool))
		for id, entry := range mempool {
			switch entry.InstantLock {
			case "true":
				ids = append(ids, id)
			case "false":
				continue
			default:
				// Retry a fresh mempool rather than persist an uncertain snapshot.
				return blocked, errors.New("InstantSend mempool lock status unavailable")
			}
		}
		if err = h.client.temporalStorage.StartInstantSendWarmup(height, ids); err != nil {
			return blocked, err
		}
		state, err = h.client.temporalStorage.GetInstantSendState()
		if err != nil {
			return blocked, err
		}
	}
	if !state.Ready {
		if h.client.bridge == nil {
			return blocked, nil
		}
		if err = h.classifySnapshot(state); err != nil {
			if errors.Is(err, errInstantSendStateChanged) {
				return blocked, nil
			}
			return blocked, err
		}
		// Classification can itself settle a verified block while holding the lock.
		revision = h.revision
		if h.client.blockScanner == nil {
			return blocked, nil
		}
		// PreviousHeight advances after scanner delivery; currentBlockHeight is
		// updated while fetching and cannot prove that catch-up has completed.
		if err = h.client.temporalStorage.CompleteInstantSendWarmup(h.client.blockScanner.PreviousHeight()); err != nil {
			return blocked, err
		}
		state, err = h.client.temporalStorage.GetInstantSendState()
		if err != nil || !state.Ready {
			return blocked, err
		}
	}
	// Startup or a runtime scanner jump may skip blocks containing reservations.
	// Check every existing reservation once before using the restored allowance.
	h.recovery.Load(state.Reservations)
	for range h.workLimit() {
		id, pending := h.recovery.Next()
		if !pending {
			break
		}
		if _, stillReserved := state.Reservations[id]; stillReserved {
			var tx *rpc.TxResult
			var settled []string
			// Inclusion checks use RPCs; block settlement must be able to run meanwhile.
			h.stateMu.Unlock()
			tx, err = h.client.rpc.GetRawTransactionVerbose(id)
			var rpcErr *btcjson.RPCError
			// An unknown or evicted transaction remains charged. It is not proof
			// that a previously reported deposit is safe to forget.
			if err == nil {
				settled, err = h.getSettlement(id, tx)
			} else if errors.As(err, &rpcErr) && rpcErr.Code == btcjson.ErrRPCInvalidAddressOrKey {
				err = nil
			}
			// Settlement may have reset recovery; check before advancing its cursor.
			if !h.lockAndCheckRevision(revision) {
				return blocked, nil
			}
			if next, ok := h.recovery.Next(); !ok || next != id {
				return blocked, nil
			}
			if err != nil {
				return blocked, err
			}
			if len(settled) > 0 {
				if err = h.releaseReservations(settled); err != nil {
					return blocked, err
				}
				revision = h.revision
				// One block can settle many pending IDs. Avoid fetching it again.
				state, err = h.client.temporalStorage.GetInstantSendState()
				if err != nil {
					return blocked, err
				}
			}
		}
		h.recovery.Advance()
	}
	if !canAdmit || !h.recovery.Complete() {
		return blocked, statusErr
	}
	return instantSendAdmission{allowed: true}, nil
}

// workLimit bounds each allowance work batch using the configured
// transaction batch size, with a default of 100.
func (h *instantSendHandler) workLimit() int {
	if h.client.cfg.UTXO.TransactionBatchSize > 0 {
		return h.client.cfg.UTXO.TransactionBatchSize
	}
	return 100
}

// classifySnapshot ignores observed markers and the new minimum/age
// gates: these deposits might already have been credited before this startup.
// Call with stateMu held; it is held again on every return.
// Network work temporarily releases it so block settlement can continue;
// a changed revision prevents stale results from restoring settled reservations.
func (h *instantSendHandler) classifySnapshot(state sharedutxo.InstantSendState) error {
	if len(state.SnapshotPending) == 0 {
		return nil
	}
	revision := h.revision
	ids := state.SnapshotPending[:min(len(state.SnapshotPending), h.workLimit())]
	// Only lookups started after a newer block was scanned can establish absence.
	var scannedHeight int64
	if h.client.blockScanner != nil {
		scannedHeight = h.client.blockScanner.PreviousHeight()
	}
	// A failed vault-list refresh must not classify a deposit as irrelevant.
	var txs []*rpc.TxResult
	var txErrors []error
	// Vault refresh and transaction lookups can both wait on the network.
	h.stateMu.Unlock()
	_, err := h.client.getAsgardAddress()
	if err == nil {
		txs, txErrors, err = h.client.rpc.BatchGetRawTransactionVerbose(ids)
	}
	if !h.lockAndCheckRevision(revision) {
		return errInstantSendStateChanged
	}
	if err != nil {
		return err
	}
	var classified []string
	var reservations []sharedutxo.InstantSendReservation
	var failures []error
	missing := make(map[string]error)
	pending := make(map[string]bool, len(state.SnapshotPending))
	for _, id := range state.SnapshotPending {
		pending[id] = true
	}
	for i, id := range ids {
		if !pending[id] {
			continue
		}
		if txErrors[i] != nil {
			var rpcErr *btcjson.RPCError
			_, reserved := state.Reservations[id]
			notFound := errors.As(txErrors[i], &rpcErr) && rpcErr.Code == btcjson.ErrRPCInvalidAddressOrKey
			if notFound && !reserved && scannedHeight > state.SnapshotHeight {
				missing[id] = txErrors[i]
			} else {
				failures = append(failures, txErrors[i])
			}
			continue
		}
		tx := txs[i]
		var settled []string
		var amount uint64
		// Inclusion, sender and vault lookups run unlocked to allow settlement.
		h.stateMu.Unlock()
		settled, err = h.getSettlement(id, tx)
		if err == nil && len(settled) == 0 {
			amount, err = h.snapshotAmount(tx)
		}
		// Reject results if settlement changed the snapshot while we were waiting.
		if !h.lockAndCheckRevision(revision) {
			return errInstantSendStateChanged
		}
		if err != nil {
			failures = append(failures, err)
			break // retry the remaining snapshot on a later scan
		}
		if len(settled) > 0 {
			if err = h.releaseReservations(settled); err != nil {
				return err
			}
			revision = h.revision
			// Inclusion also removes other snapshot IDs found in this block.
			var current sharedutxo.InstantSendState
			current, err = h.client.temporalStorage.GetInstantSendState()
			if err != nil {
				return err
			}
			clear(pending)
			for _, id := range current.SnapshotPending {
				pending[id] = true
			}
			continue
		}
		classified = append(classified, id)
		if amount > 0 {
			reservations = append(reservations, sharedutxo.InstantSendReservation{TxID: id, Amount: amount, CreatedAt: time.Now().Unix()})
		}
	}
	var missingIDs, removed []string
	for _, id := range ids {
		if pending[id] && missing[id] != nil {
			missingIDs = append(missingIDs, id)
		}
	}
	if len(missingIDs) > 0 {
		removed, err = h.recoverSnapshot(missingIDs, state.SnapshotHeight, scannedHeight)
		if errors.Is(err, errInstantSendStateChanged) {
			return err
		}
		if err != nil {
			failures = append(failures, err)
		}
		classified = append(classified, removed...)
		for _, id := range removed {
			delete(missing, id)
		}
		for _, id := range missingIDs {
			if missing[id] != nil {
				failures = append(failures, missing[id])
			}
		}
	}
	if err = h.client.temporalStorage.RecordInstantSendSnapshot(classified, reservations); err != nil {
		return err
	}
	if len(removed) > 0 {
		h.client.log.Warn().Strs("txids", removed).Int64("snapshot_height", state.SnapshotHeight).
			Msg("discarded missing InstantSend startup transactions after ChainLock catch-up")
	}
	return errors.Join(failures...)
}

// recoverSnapshot returns unresolved startup IDs eligible for removal
// after ChainLock catch-up and a fresh mempool check. It does not release reservations.
// Call with stateMu held; RPCs run unlocked and every return holds it again.
func (h *instantSendHandler) recoverSnapshot(ids []string, snapshotHeight, scannedHeight int64) ([]string, error) {
	revision := h.revision
	// Allow settlement while checking the current ChainLock and mempool.
	h.stateMu.Unlock()
	height, err := h.client.GetHeight()
	var mempool map[string]rpc.GetRawMempoolVerboseResult
	caughtUp := height > snapshotHeight && scannedHeight >= height
	if err == nil && caughtUp && h.client.IsBlockScannerHealthy() {
		mempool, err = h.client.rpc.GetRawMempoolVerbose()
		if err == nil && mempool == nil {
			err = errors.New("missing InstantSend recovery mempool response")
		}
	}
	if !h.lockAndCheckRevision(revision) {
		return nil, errInstantSendStateChanged
	}
	if err != nil {
		return nil, err
	}
	if mempool == nil || h.client.blockScanner.PreviousHeight() < height || !h.client.IsBlockScannerHealthy() {
		return nil, nil
	}
	var removed []string
	for _, id := range ids {
		if _, present := mempool[id]; !present {
			removed = append(removed, id)
		}
	}
	return removed, nil
}

// snapshotAmount returns the customer deposit amount to reserve during
// startup, in duffs. Vault senders are exempt; current minimum and age limits do not apply.
func (h *instantSendHandler) snapshotAmount(tx *rpc.TxResult) (uint64, error) {
	if !tx.InstantSendLocked() || len(tx.Vin) == 0 {
		return 0, nil
	}
	// Avoid fetching parents for unrelated mempool traffic.
	relevant := false
	for _, out := range tx.Vout {
		addresses := h.client.getAddressesFromScriptPubKey(out.ScriptPubKey)
		if len(addresses) == 1 && h.client.isAsgardAddress(addresses[0]) {
			relevant = true
			break
		}
	}
	if !relevant {
		return 0, nil
	}
	sender, err := h.client.getSender(&tx.TxRawResult, nil)
	if errors.Is(err, errInvalidSenderUTXO) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if h.client.isAsgardAddress(sender) {
		return 0, nil
	}
	out, err := h.client.getOutput(sender, &tx.TxRawResult, false)
	if errors.Is(err, btypes.ErrFailOutputMatchCriteria) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !h.client.isValidUTXO(out.ScriptPubKey.Hex) {
		return 0, nil
	}
	amount, err := btcutil.NewAmount(out.Value)
	if err != nil {
		return 0, err
	}
	if amount <= 0 || uint64(amount) < h.client.cfg.ChainID.DustThreshold().Uint64() {
		return 0, nil
	}
	return uint64(amount), nil
}

// getSettlement verifies inclusion in a canonical ChainLocked block
// and returns its transaction IDs. It performs RPCs without changing the journal;
// call without stateMu held.
func (h *instantSendHandler) getSettlement(id string, tx *rpc.TxResult) ([]string, error) {
	if !strings.EqualFold(id, tx.Txid) {
		return nil, errors.New("InstantSend transaction ID mismatch")
	}
	if !tx.ChainLock {
		return nil, nil
	}
	if tx.BlockHash == "" {
		return nil, errors.New("ChainLocked transaction has no block hash")
	}
	block, err := h.client.rpc.GetBlockVerboseTxs(tx.BlockHash)
	if err != nil {
		return nil, err
	}
	if !block.ChainLock || block.Height < 0 || !strings.EqualFold(block.Hash, tx.BlockHash) {
		return nil, errors.New("invalid ChainLocked transaction block")
	}
	hash, err := h.client.rpc.GetBlockHash(block.Height)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hash, block.Hash) {
		return nil, errors.New("ChainLocked transaction is not in the active chain")
	}
	ids := make([]string, 0, len(block.Tx))
	found := false
	for _, item := range block.Tx {
		ids = append(ids, item.Txid)
		if strings.EqualFold(item.Txid, id) {
			found = true
		}
	}
	if !found {
		return nil, errors.New("transaction missing from ChainLocked block")
	}
	return ids, nil
}

// releaseReservations invalidates outstanding RPC results even when
// settlement fails. A failure resets recovery before further customer admission.
// Call with stateMu held, after verifying ChainLock inclusion.
func (h *instantSendHandler) releaseReservations(ids []string) error {
	h.revision++
	err := h.client.temporalStorage.ReleaseInstantSendReservations(ids)
	if err != nil {
		// Retry inclusion checks before any further customer admission.
		h.recovery.Reset()
	}
	return err
}

// reconcileBlock releases reservations for transactions in an already
// verified ChainLocked block without waiting for mempool network calls.
// A skipped range restarts recovery to check reservations mined in those blocks.
func (h *instantSendHandler) reconcileBlock(block *btcjson.GetBlockVerboseTxResult) error {
	ids := make([]string, 0, len(block.Tx))
	for _, tx := range block.Tx {
		ids = append(ids, tx.Txid)
	}
	// Keep the revision increment, reservation release and recovery reset together.
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.lastBlockHeight > 0 && block.Height > h.lastBlockHeight+1 {
		h.recovery.Reset()
	}
	h.lastBlockHeight = block.Height
	return h.releaseReservations(ids)
}

// claimBatch checks finality without the allowance lock, then atomically
// claims allowance and markers only if settlement has not invalidated those checks.
// Vault senders bypass the allowance.
func (h *instantSendHandler) claimBatch(items []*types.TxInItem, admission instantSendAdmission) ([]*types.TxInItem, error) {
	h.stateMu.Lock()
	// Every RPC section reacquires the lock before returning, so this stays balanced.
	defer h.stateMu.Unlock()
	revision := h.revision
	claims := make([]sharedutxo.InstantSendClaim, 0, len(items))
	byID := make(map[string]*types.TxInItem, len(items))
	ids := make([]string, 0, len(items))
	var exempt []*types.TxInItem
	for _, item := range items {
		id := strings.ToLower(item.Tx)
		if _, duplicate := byID[id]; duplicate {
			continue
		}
		ids = append(ids, id)
		byID[id] = item
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// Queued batches may outlive a block scan. Recheck finality without holding
	// up settlement, then reject the results if settlement happened meanwhile.
	h.stateMu.Unlock()
	txs, txErrors, err := h.client.rpc.BatchGetRawTransactionVerbose(ids)
	// Recheck the revision under the lock before using these finality results.
	if !h.lockAndCheckRevision(revision) {
		h.recordDeferral("finality")
		return nil, nil
	}
	if err != nil {
		h.recordDeferral("finality")
		return nil, err
	}
	// Vault-cache refreshes and the halt check below can make network calls.
	h.stateMu.Unlock()
	for i, id := range ids {
		tx := txs[i]
		if txErrors[i] != nil || !strings.EqualFold(tx.Txid, id) || tx.ChainLock || !tx.InstantSendLocked() {
			h.recordDeferral("finality")
			continue
		}
		item := byID[id]
		vaultSender := h.client.isAsgardAddress(item.Sender)
		if vaultSender {
			exempt = append(exempt, item)
		}
		claims = append(claims, sharedutxo.InstantSendClaim{TxID: id, Amount: item.Coins.GetCoin(h.client.cfg.ChainID.GetGasAsset()).Amount.Uint64(), Exempt: vaultSender})
	}
	// Check after the finality and sender lookups, immediately before any writes.
	// Vault senders bypass the amount allowance, but must still respect halts.
	paused := h.isPaused()
	// Hold through the revision check and claim so settlement cannot slip between them.
	h.stateMu.Lock()
	if paused {
		return nil, errInstantSendPaused
	}
	if revision != h.revision {
		h.recordDeferral("finality")
		return nil, nil
	}
	admission = h.currentAdmission(admission)
	accepted, err := h.client.temporalStorage.ClaimInstantSendTxs(claims, h.minAmount, h.maxAmount, admission.allowed, time.Now().Unix())
	if err != nil {
		h.recordDeferral("storage")
		h.client.log.Err(err).Msg("fail to reserve InstantSend allowance")
		if len(exempt) == 0 {
			return nil, err
		}
		// A damaged allowance journal must not stop vault transactions. They use
		// the existing observation-marker store without touching the allowance.
		// Release again for the fresh halt lookup needed by this fallback.
		h.stateMu.Unlock()
		paused = h.isPaused()
		// Protect the final checks and fallback marker write together.
		h.stateMu.Lock()
		if paused {
			return nil, errInstantSendPaused
		}
		if revision != h.revision {
			h.recordDeferral("finality")
			return nil, nil
		}
		return h.client.trackObservedTxs(exempt)
	}
	result := make([]*types.TxInItem, 0, len(accepted))
	for _, id := range accepted {
		result = append(result, byID[id])
		delete(byID, id)
	}
	state, _ := h.client.temporalStorage.GetInstantSendState()
	for _, claim := range claims {
		if _, skipped := byID[claim.TxID]; !skipped || claim.Exempt {
			continue
		}
		var observed bool
		if observed, err = h.client.temporalStorage.HasObservedTx(claim.TxID); err == nil && observed {
			continue // another scanner already delivered this transaction
		}
		reason := "allowance"
		switch {
		case state.Reservations[claim.TxID].Origin == sharedutxo.InstantSendSnapshot:
			reason = "startup_snapshot"
		case !admission.allowed:
			reason = admission.reason
		case claim.Amount < h.minAmount:
			reason = "minimum"
		}
		h.recordDeferral(reason)
	}
	return result, nil
}

// recordDeferral records an observation deferral in the metric for its reason.
func (h *instantSendHandler) recordDeferral(reason string) {
	if h.client.m != nil {
		h.client.m.GetCounterVec(metrics.InstantSendDeferred).WithLabelValues(reason).Inc()
	}
}

// publishAllowance reports journal usage, available allowance, and warmup status.
// It is called by the mempool scanner, which owns the current Mimir settings.
func (h *instantSendHandler) publishAllowance(admission instantSendAdmission) {
	if h.client.m == nil {
		return
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	state, err := h.client.temporalStorage.GetInstantSendState()
	if err != nil {
		h.client.m.GetGauge(metrics.InstantSendAvailable).Set(0)
		return
	}
	warming := float64(1)
	if state.Ready {
		warming = 0
	}
	available := state.Available(h.maxAmount)
	if !h.currentAdmission(admission).allowed {
		available = 0
	}
	for metric, value := range map[metrics.MetricName]float64{
		metrics.InstantSendReserved:  float64(state.Reserved),
		metrics.InstantSendAvailable: float64(available),
		metrics.InstantSendPending:   float64(state.Count),
		metrics.InstantSendWarmingUp: warming,
	} {
		h.client.m.GetGauge(metric).Set(value)
	}
}
