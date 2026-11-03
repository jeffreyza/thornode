package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Keep failed refund cleanup markers for roughly 30 days at 6-second blocks.
const refundCleanupRetentionBlocks int64 = 432000

func refundCleanupExpiryPrefix(expiryHeight int64) []byte {
	if expiryHeight < 0 {
		expiryHeight = 0
	}
	return []byte(fmt.Sprintf("%s%020d/", prefixRefundCleanupExpiry, expiryHeight))
}

func refundCleanupExpiryKey(expiryHeight int64, key string) []byte {
	return []byte(fmt.Sprintf("%s%s", refundCleanupExpiryPrefix(expiryHeight), strings.ToUpper(key)))
}

func (k KVStore) SetRefundCleanup(ctx cosmos.Context, key string) {
	expiryHeight := ctx.BlockHeight() + refundCleanupRetentionBlocks
	k.setInt64(ctx, k.GetKey(prefixRefundCleanup, key), expiryHeight)
	k.setInt64(ctx, refundCleanupExpiryKey(expiryHeight, key), 1)
}

func (k KVStore) HasRefundCleanup(ctx cosmos.Context, key string) bool {
	var expiryHeight int64
	exists, err := k.getInt64(ctx, k.GetKey(prefixRefundCleanup, key), &expiryHeight)
	if err != nil {
		ctx.Logger().Error("fail to get refund cleanup marker", "key", key, "error", err)
		return k.has(ctx, k.GetKey(prefixRefundCleanup, key))
	}
	return exists && expiryHeight >= ctx.BlockHeight()
}

func (k KVStore) PruneRefundCleanups(ctx cosmos.Context) {
	expiryHeight := ctx.BlockHeight()
	prefix := refundCleanupExpiryPrefix(expiryHeight)
	iterator := k.getIterator(ctx, kvPrefix(prefix))
	defer iterator.Close()

	var expiryKeys [][]byte
	var cleanupKeys []string
	for ; iterator.Valid(); iterator.Next() {
		expiryKeys = append(expiryKeys, append([]byte(nil), iterator.Key()...))
		cleanupKeys = append(cleanupKeys, string(iterator.Key()[len(prefix):]))
	}

	for i, cleanupKey := range cleanupKeys {
		directKey := k.GetKey(prefixRefundCleanup, cleanupKey)
		var storedExpiryHeight int64
		exists, err := k.getInt64(ctx, directKey, &storedExpiryHeight)
		if err != nil {
			ctx.Logger().Error("fail to get refund cleanup marker during prune", "key", cleanupKey, "error", err)
		}
		if exists && storedExpiryHeight == expiryHeight {
			k.del(ctx, directKey)
		}
		k.del(ctx, expiryKeys[i])
	}
}
