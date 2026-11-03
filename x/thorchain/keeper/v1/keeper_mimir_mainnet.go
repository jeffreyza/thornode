//go:build !mocknet
// +build !mocknet

package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// GetMimir get a mimir value from key value store
func (k KVStore) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	cache := keeper.MimirCacheFromContext(ctx)
	if value, ok := cache.Get(key); ok {
		return value, nil
	}
	record := int64(-1)
	_, err := k.getInt64(ctx, k.GetKey(prefixMimir, key), &record)
	if err == nil {
		cache.Set(key, record)
	}
	return record, err
}
