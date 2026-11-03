package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// --- DynamicFeeRecord ---

func (k KVStore) setDynamicFeeRecord(ctx cosmos.Context, key []byte, record DynamicFeeRecord) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getDynamicFeeRecord(ctx cosmos.Context, key []byte, record *DynamicFeeRecord) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}

	return true, nil
}

func (k KVStore) GetDynamicFeeRecordIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixDynamicFee)
}

// GetDynamicFeeRecordIteratorForThorname returns a prefix iterator scoped to
// records owned by a single thorname. Relies on the key structure
// <prefixDynamicFee>/<THORNAME_UPPER>|<PAIRA>|<PAIRB> established by
// KVStore.GetKey and DynamicFeeRecord.Key.
func (k KVStore) GetDynamicFeeRecordIteratorForThorname(ctx cosmos.Context, thorname string) cosmos.Iterator {
	store := k.kvStore(ctx)
	prefix := fmt.Sprintf("%s/%s|", prefixDynamicFee, strings.ToUpper(thorname))
	return cosmos.KVStorePrefixIterator(store, []byte(prefix))
}

func (k KVStore) SetDynamicFeeRecord(ctx cosmos.Context, record DynamicFeeRecord) error {
	k.setDynamicFeeRecord(ctx, k.GetKey(prefixDynamicFee, record.Key()), record)
	return nil
}

func (k KVStore) GetDynamicFeeRecord(ctx cosmos.Context, thorname, pairAssetA, pairAssetB string) (DynamicFeeRecord, error) {
	record := DynamicFeeRecord{
		Thorname:   thorname,
		PairAssetA: pairAssetA,
		PairAssetB: pairAssetB,
	}
	_, err := k.getDynamicFeeRecord(ctx, k.GetKey(prefixDynamicFee, record.Key()), &record)
	return record, err
}

func (k KVStore) DeleteDynamicFeeRecord(ctx cosmos.Context, thorname, pairAssetA, pairAssetB string) error {
	record := DynamicFeeRecord{
		Thorname:   thorname,
		PairAssetA: pairAssetA,
		PairAssetB: pairAssetB,
	}
	k.del(ctx, k.GetKey(prefixDynamicFee, record.Key()))
	return nil
}

// --- DynamicFeeAccumulator ---

func (k KVStore) setDynamicFeeAccumulator(ctx cosmos.Context, key []byte, acc DynamicFeeAccumulator) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&acc)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getDynamicFeeAccumulator(ctx cosmos.Context, key []byte, acc *DynamicFeeAccumulator) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, acc); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", acc, key), err)
	}

	return true, nil
}

func (k KVStore) GetDynamicFeeAccumulatorIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixDynamicFeeAcc)
}

func (k KVStore) SetDynamicFeeAccumulator(ctx cosmos.Context, acc DynamicFeeAccumulator) error {
	k.setDynamicFeeAccumulator(ctx, k.GetKey(prefixDynamicFeeAcc, acc.Key()), acc)
	return nil
}

func (k KVStore) GetDynamicFeeAccumulator(ctx cosmos.Context, thorname, pairAssetA, pairAssetB string) (DynamicFeeAccumulator, error) {
	acc := NewDynamicFeeAccumulator(thorname, pairAssetA, pairAssetB)
	_, err := k.getDynamicFeeAccumulator(ctx, k.GetKey(prefixDynamicFeeAcc, acc.Key()), &acc)
	return acc, err
}

func (k KVStore) DeleteDynamicFeeAccumulator(ctx cosmos.Context, thorname, pairAssetA, pairAssetB string) error {
	acc := NewDynamicFeeAccumulator(thorname, pairAssetA, pairAssetB)
	k.del(ctx, k.GetKey(prefixDynamicFeeAcc, acc.Key()))
	return nil
}

// --- DeleteAllDynamicFeeStateForThorname ---

func (k KVStore) DeleteAllDynamicFeeStateForThorname(ctx cosmos.Context, thorname string) error {
	// Collect all (pairA, pairB) tuples for this thorname using a prefix
	// iterator scoped by thorname. CreditSwap establishes the invariant that
	// every accumulator has a matching record, so record iteration alone
	// tells us every key to delete.
	type pairKey struct{ a, b string }
	var pairs []pairKey
	iter := k.GetDynamicFeeRecordIteratorForThorname(ctx, thorname)
	for ; iter.Valid(); iter.Next() {
		var record DynamicFeeRecord
		if err := k.cdc.Unmarshal(iter.Value(), &record); err != nil {
			iter.Close()
			return dbError(ctx, "Unmarshal DynamicFeeRecord", err)
		}
		pairs = append(pairs, pairKey{record.PairAssetA, record.PairAssetB})
	}
	iter.Close()

	// Delete record and accumulator for each tuple. Accumulator delete is
	// idempotent — safe to call when no accumulator exists (post-epoch-seal).
	for _, p := range pairs {
		if err := k.DeleteDynamicFeeRecord(ctx, thorname, p.a, p.b); err != nil {
			return err
		}
		if err := k.DeleteDynamicFeeAccumulator(ctx, thorname, p.a, p.b); err != nil {
			return err
		}
	}

	return nil
}

// GetLastObservedDynamicFeeEpochBlocks returns the value of
// L1DynamicFeeEpochBlocks last observed by the DynamicFeeMgr at an EndBlock
// call. Returns (0, nil) before the first observation has been recorded.
//
// Stored separately from the mimir so the manager can detect governance
// changes to epochBlocks between EndBlock firings — without a
// previous-value reference, LastActiveEpoch (computed under the old scale)
// becomes incomparable with the new epoch numbering and can cause
// premature pruning of otherwise-live records.
func (k KVStore) GetLastObservedDynamicFeeEpochBlocks(ctx cosmos.Context) (int64, error) {
	record := int64(0)
	_, err := k.getInt64(ctx, k.GetKey(prefixDynamicFeeEpochBlocks, ""), &record)
	return record, err
}

// SetLastObservedDynamicFeeEpochBlocks records the value of
// L1DynamicFeeEpochBlocks as of the current EndBlock run.
func (k KVStore) SetLastObservedDynamicFeeEpochBlocks(ctx cosmos.Context, value int64) {
	k.setInt64(ctx, k.GetKey(prefixDynamicFeeEpochBlocks, ""), value)
}

// GetRemovedDynamicFeeWhitelistThornames returns thornames whose
// DYNAMICFEE-WHITELIST mimir entry exists but holds a state other than 1
// (active) or 2 (accrual-only) — i.e. thornames whose dynamic-fee state
// should be reclaimed. Uses a prefix iterator over the mimir store scoped
// to DYNAMICFEE-WHITELIST-* keys, avoiding a full scan of the record store.
//
// Invariant: only mimir entries that *exist* with state ∉ {1, 2} are
// reported. Entries removed via DeleteMimir (i.e. absent keys) are NOT
// surfaced here and so will not trigger active cleanup — operators must
// SetMimir(..., 0) to enrol a thorname for reclamation. Records left
// behind by an outright delete will eventually self-prune via the
// LastActiveEpoch ≥ MaxDynamicFeeHistory check in the manager.
//
// Thornames are returned in uppercased form (matching how they are stored
// under prefixMimir); DeleteAllDynamicFeeStateForThorname uppercases
// internally, so the casing is transparent to callers.
func (k KVStore) GetRemovedDynamicFeeWhitelistThornames(ctx cosmos.Context) []string {
	store := k.kvStore(ctx)
	prefix := fmt.Sprintf("%s/DYNAMICFEE-WHITELIST-", prefixMimir)
	iter := cosmos.KVStorePrefixIterator(store, []byte(prefix))
	defer iter.Close()

	var removed []string
	for ; iter.Valid(); iter.Next() {
		var value ProtoInt64
		if err := k.cdc.Unmarshal(iter.Value(), &value); err != nil {
			continue
		}
		state := value.GetValue()
		if state == 1 || state == 2 {
			continue
		}
		thorname := strings.TrimPrefix(string(iter.Key()), prefix)
		if thorname == "" {
			continue
		}
		removed = append(removed, thorname)
	}
	return removed
}
