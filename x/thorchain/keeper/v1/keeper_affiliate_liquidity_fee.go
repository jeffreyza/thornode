package keeperv1

import (
	"fmt"
	"math"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// AddAffiliateLiquidityFee accumulates the given rune-denominated liquidity
// fee against the supplied thorname for the current block. Called from the
// swap path when the swap's first memo affiliate resolves to an active
// THORName. The accumulator is cleared at end-of-block by
// DeleteAffiliateLiquidityFees after rev-share payouts are processed.
//
// The fee and the running sum must both fit in a uint64; either bound being
// exceeded returns an error so the accumulator can never wrap or truncate
// silently. This mirrors the safeUint64 pattern used by the outbound-fee
// rune accumulators in the same package.
func (k KVStore) AddAffiliateLiquidityFee(ctx cosmos.Context, thorname string, fee cosmos.Uint) error {
	if fee.IsZero() {
		return nil
	}
	feeU64, err := safeUint64(fee)
	if err != nil {
		return fmt.Errorf("affiliate liquidity fee for %s: %w", thorname, err)
	}
	key := k.GetKey(prefixAffiliateLiquidityFee, thorname)
	var current uint64
	if _, err := k.getUint64(ctx, key, &current); err != nil {
		return err
	}
	if current > math.MaxUint64-feeU64 {
		return fmt.Errorf("affiliate liquidity fee accumulator overflow for %s: current=%d fee=%d", thorname, current, feeU64)
	}
	k.setUint64(ctx, key, current+feeU64)
	return nil
}

// GetAffiliateLiquidityFees returns all per-block accumulator entries,
// sorted lexicographically by Thorname (upper-cased). The lexicographic
// ordering inherits from the IAVL store iteration, which is the same
// order this method preserves explicitly so callers in consensus paths
// can rely on it without re-sorting. Returns an empty (non-nil) slice
// when the accumulator is empty.
//
// A per-entry decode failure is logged and the entry is skipped rather
// than aborting the whole iteration: the end-of-block drain in
// processRevShare unconditionally deletes the accumulator, so an aborted
// iteration would silently discard every sibling entry's attribution
// for the block. Skip-and-continue limits the blast radius to the single
// corrupted entry, and is the reason this function does not return an
// error.
func (k KVStore) GetAffiliateLiquidityFees(ctx cosmos.Context) []keeper.AffiliateLiquidityFee {
	out := []keeper.AffiliateLiquidityFee{}
	iter := k.getIterator(ctx, prefixAffiliateLiquidityFee)
	defer iter.Close()
	keyPrefix := string(prefixAffiliateLiquidityFee) + "/"
	for ; iter.Valid(); iter.Next() {
		rawKey := strings.TrimPrefix(string(iter.Key()), keyPrefix)
		var value ProtoUint64
		if err := k.cdc.Unmarshal(iter.Value(), &value); err != nil {
			ctx.Logger().Error("fail to decode affiliate liquidity fee entry; skipping",
				"thorname", rawKey, "error", err)
			continue
		}
		out = append(out, keeper.AffiliateLiquidityFee{
			Thorname: rawKey,
			Fee:      cosmos.NewUint(value.GetValue()),
		})
	}
	return out
}

// DeleteAffiliateLiquidityFees removes every accumulator entry. Called at
// end-of-block after REVSHARE payouts are made.
func (k KVStore) DeleteAffiliateLiquidityFees(ctx cosmos.Context) {
	iter := k.getIterator(ctx, prefixAffiliateLiquidityFee)
	defer iter.Close()
	store := k.kvStore(ctx)
	// Collect keys first; mutating the store while iterating is unsafe.
	var keys [][]byte
	for ; iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	for _, key := range keys {
		store.Delete(key)
	}
}
