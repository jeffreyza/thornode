package keeperv1

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (k KVStore) setPool(ctx cosmos.Context, key []byte, record Pool) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getPool(ctx cosmos.Context, key []byte, record *Pool) (bool, error) {
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

// GetPoolIterator iterate pools
func (k KVStore) GetPoolIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixPool)
}

// GetPools return all pool in key value store regardless state
func (k KVStore) GetPools(ctx cosmos.Context) (Pools, error) {
	var pools Pools
	iterator := k.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		// Full scans (GetAnchors per stable-swap check, EndBlock managers) see
		// mostly unchanged pools, so their decodes are memoized. Point reads in
		// getPool often follow a SetPool of the same pool and would only miss.
		if !k.memoDecodePool(iterator.Key(), iterator.Value(), &pool) {
			if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
				return nil, dbError(ctx, "Unmarsahl: pool", err)
			}
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

// GetPool get the entire Pool metadata struct based on given asset.
// Note: returns a default-initialized Pool (with empty Asset) when the pool
// does not exist, with err==nil. Callers should check pool.IsEmpty() or use
// PoolExist() to distinguish between a nonexistent pool and an existing one.
func (k KVStore) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	record := NewPool()
	_, err := k.getPool(ctx, k.GetKey(prefixPool, asset.String()), &record)

	return record, err
}

// SetPool save the entire Pool metadata struct to key value store
func (k KVStore) SetPool(ctx cosmos.Context, pool Pool) error {
	if pool.Asset.IsEmpty() {
		return errors.New("cannot save a pool with an empty asset")
	}
	keeper.RecordLuviTouch(ctx, pool.Asset, k.luviCapture(ctx))
	k.setPool(ctx, k.GetKey(prefixPool, pool.Asset.String()), pool)
	return nil
}

// PoolExist check whether the given pool exist in the data store
func (k KVStore) PoolExist(ctx cosmos.Context, asset common.Asset) bool {
	return k.has(ctx, k.GetKey(prefixPool, asset.String()))
}

func (k KVStore) RemovePool(ctx cosmos.Context, asset common.Asset) {
	keeper.RecordLuviTouch(ctx, asset, k.luviCapture(ctx))
	k.del(ctx, k.GetKey(prefixPool, asset.String()))
	// a re-created pool starts a fresh accumulator rather than observing a
	// step from the removed pool's last state
	k.RemovePoolLuviAccumulator(ctx, asset)
}

func (k KVStore) SetPoolLUVI(ctx cosmos.Context, asset common.Asset, luvi cosmos.Uint) {
	key := k.GetKey(prefixPoolLUVI, asset.String())
	k.setUint(ctx, key, luvi)
}

func (k KVStore) GetPoolLUVI(ctx cosmos.Context, asset common.Asset) (cosmos.Uint, error) {
	key := k.GetKey(prefixPoolLUVI, asset.String())
	record := cosmos.ZeroUint()
	_, err := k.getUint(ctx, key, &record)
	return record, err
}

// GetPoolLuviAccumulator returns the pool's LUVI accumulator, and whether one
// is stored.
func (k KVStore) GetPoolLuviAccumulator(ctx cosmos.Context, asset common.Asset) (types.PoolLuvi, bool, error) {
	var record types.PoolLuvi
	store := k.kvStore(ctx)
	key := k.GetKey(prefixPoolLuviAccumulator, asset.String())
	bz := store.Get(key)
	if bz == nil {
		return record, false, nil
	}
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return record, true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", &record, key), err)
	}
	return record, true, nil
}

// SetPoolLuviAccumulator saves the pool's LUVI accumulator.
func (k KVStore) SetPoolLuviAccumulator(ctx cosmos.Context, record types.PoolLuvi) {
	k.kvStore(ctx).Set(k.GetKey(prefixPoolLuviAccumulator, record.Asset.String()), k.cdc.MustMarshal(&record))
}

// RemovePoolLuviAccumulator deletes the pool's LUVI accumulator.
func (k KVStore) RemovePoolLuviAccumulator(ctx cosmos.Context, asset common.Asset) {
	k.del(ctx, k.GetKey(prefixPoolLuviAccumulator, asset.String()))
}

// luviCapture reads a layer-1 pool's current LUVI state, for the LUVI journal
// (see keeper.LuviJournal).
func (k KVStore) luviCapture(ctx cosmos.Context) func(common.Asset) types.LuviState {
	return func(asset common.Asset) types.LuviState {
		pool, err := k.GetPool(ctx, asset)
		if err != nil {
			// an unreadable pool counts as empty
			return types.EmptyLuviState()
		}
		return types.NewLuviState(pool, k.GetTotalSupply(ctx, asset.GetSyntheticAsset()))
	}
}
