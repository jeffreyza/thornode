//go:build mocknet
// +build mocknet

package keeperv1

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
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

	// mocknet only fallback to environment variable if unset
	envKey := constants.CamelToSnakeUpper(key)
	envKey = strings.ReplaceAll(envKey, "-", "_") // also handle mimir with "-" in key
	if record == -1 && os.Getenv(envKey) != "" {
		envValue, err := strconv.ParseInt(os.Getenv(envKey), 10, 64)
		if err != nil {
			return record, fmt.Errorf("invalid mimir value for %s: %w", envKey, err)
		}
		cache.Set(key, envValue)
		return envValue, nil
	}

	if err == nil {
		cache.Set(key, record)
	}
	return record, err
}
