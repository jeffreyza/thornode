package utxo

import (
	"fmt"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

// Dash signing must stop well before a prolonged ChainLock stall can lead to
// outbound reassignment. Allow one block of lag and one expected block interval
// of height-data age, rather than the shared scanner's longer MaxHealthyLag.
const dashMaxHealthyBlockLag int64 = 1

// chainLockHeightHealth is immutable once published; the scanner and signer read it
// concurrently through Client.chainLockHeightHealth.
type chainLockHeightHealth struct {
	blockHeight     int64
	chainLockHeight int64
	checkedAt       time.Time
}

// isHealthy reports whether height data is fresh and both ChainLock and scanner
// lag are within the Dash signing limit.
func (h *chainLockHeightHealth) isHealthy(now time.Time, scannedHeight int64) bool {
	maxAge := time.Duration(dashMaxHealthyBlockLag*common.DASHChain.ApproximateBlockMilliseconds()) * time.Millisecond
	if h == nil || h.checkedAt.IsZero() || now.Sub(h.checkedAt) >= maxAge {
		return false
	}
	// Check finality even when the scanner is caught up to a frozen ChainLock.
	// Also bound the unscanned backlog so a new ChainLock cannot immediately
	// restore signing using the scanner's old healthy flag before catch-up.
	return h.blockHeight >= h.chainLockHeight &&
		h.blockHeight-h.chainLockHeight <= dashMaxHealthyBlockLag &&
		h.blockHeight-scannedHeight <= dashMaxHealthyBlockLag
}

// isTransactionQuorumLocked checks daemon lock status for balance reporting.
// The cache belongs to one operation, so a previously unlocked tx is refreshed
// on the next call and multiple outputs of one tx need only one RPC lookup.
func (c *Client) isTransactionQuorumLocked(txID string, cache map[string]bool) (bool, error) {
	key := strings.ToLower(txID)
	if final, found := cache[key]; found {
		return final, nil
	}
	tx, err := c.rpc.GetRawTransactionVerbose(txID)
	if err != nil {
		return false, fmt.Errorf("fail to get transaction locks for %s: %w", txID, err)
	}
	if !strings.EqualFold(tx.Txid, txID) {
		return false, fmt.Errorf("finality txid mismatch: got %q, want %q", tx.Txid, txID)
	}
	cache[key] = tx.QuorumLocked()
	return cache[key], nil
}
