package utxo

import "fmt"

// getZecConsolidationHeight gives a locally triggered consolidation a shared
// THORChain signing height. The scanned ZEC block (captured by FetchTxs, not the
// moving RPC tip) selects the previous scheduled reporting interval. Resolve
// its first committed LastObservedIn >= target, bounded by vault creation.
// Later observations cannot change that first crossing. The current THORChain
// height is only a search bound, never the signing reference or expiry anchor.
//
// Waiting one report interval gives quorum time to form. The target is 10..19
// ZEC blocks behind the scanned block, leaving at least 21 of the 40 expiry
// blocks before scanner/signing delays. Missing quorum or historical state
// defers consolidation; it must not select a different reference as a fallback.
func (c *Client) getZecConsolidationHeight(scannedHeight, latestHeight, vaultHeight int64) (int64, error) {
	if scannedHeight < 2*zecHeightReportInterval || vaultHeight <= 0 || latestHeight < vaultHeight {
		return 0, fmt.Errorf("invalid consolidation heights: scanned %d, THORChain %d, vault %d", scannedHeight, latestHeight, vaultHeight)
	}
	target := (scannedHeight/zecHeightReportInterval - 1) * zecHeightReportInterval
	observedAt := func(height int64) (int64, error) {
		// Reuse the historical query's validation and expiry overflow checks.
		expiry, err := c.getZecExpiryHeight(height)
		if err != nil {
			return 0, err
		}
		return int64(expiry) - int64(ZecTxExpiry), nil
	}
	observed, err := observedAt(latestHeight)
	if err != nil {
		return 0, err
	}
	if observed < target {
		return 0, fmt.Errorf("ZEC consolidation target %d is not yet agreed (last observed %d)", target, observed)
	}

	// LastChainHeight is monotonic. Exponential lookback brackets the crossing
	// without querying genesis or old pruned state on every new round.
	upper, step := latestHeight, int64(1)
	for upper > vaultHeight {
		lower := max(vaultHeight, latestHeight-step)
		observed, err = observedAt(lower)
		if err != nil {
			return 0, err
		}
		if observed < target {
			for lower+1 < upper {
				mid := lower + (upper-lower)/2
				observed, err = observedAt(mid)
				if err != nil {
					return 0, err
				}
				if observed >= target {
					upper = mid
				} else {
					lower = mid
				}
			}
			return upper, nil
		}
		upper = lower
		// Cap the step before doubling to avoid overflow near MaxInt64.
		if step > (latestHeight-vaultHeight)/2 {
			step = latestHeight - vaultHeight
		} else {
			step *= 2
		}
	}
	return upper, nil
}
