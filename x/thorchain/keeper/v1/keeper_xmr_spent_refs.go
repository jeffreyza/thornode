package keeperv1

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const xmrSpentOutputRefRetentionBlocks int64 = 432000

func xmrSpentOutputRefVaultPrefix(vaultPubKey common.PubKey) []byte {
	return []byte(fmt.Sprintf("%s%s/", prefixXMRSpentOutputRef, strings.ToUpper(vaultPubKey.String())))
}

func xmrSpentOutputRefKey(vaultPubKey common.PubKey, observedHeight, finalisedHeight int64, ref common.OutputRef) []byte {
	if finalisedHeight < 0 {
		finalisedHeight = 0
	}
	if observedHeight < 0 {
		observedHeight = 0
	}
	sum := sha256.Sum256([]byte(common.SpentOutputRefIdentity(ref)))
	return []byte(fmt.Sprintf(
		"%s%020d/%020d/%s/%x",
		xmrSpentOutputRefVaultPrefix(vaultPubKey),
		finalisedHeight,
		observedHeight,
		strings.ToUpper(ref.SpendTxHash),
		sum,
	))
}

func xmrSpentOutputRefHeightFromKey(prefix, key []byte) (int64, bool) {
	if !bytes.HasPrefix(key, prefix) {
		return 0, false
	}
	rest := string(key[len(prefix):])
	idx := strings.IndexByte(rest, '/')
	if idx <= 0 {
		return 0, false
	}
	height, err := strconv.ParseInt(rest[:idx], 10, 64)
	if err != nil {
		return 0, false
	}
	return height, true
}

func (k KVStore) SetXMRSpentOutputRefs(ctx cosmos.Context, vaultPubKey common.PubKey, spendTxHash common.TxID, refs []common.OutputRef, observedHeight, finalisedHeight int64) error {
	if vaultPubKey.IsEmpty() || len(refs) == 0 {
		return nil
	}
	if err := common.ValidateSpentOutputRefs(common.XMRChain, spendTxHash, refs); err != nil {
		return err
	}
	if finalisedHeight <= 0 {
		finalisedHeight = ctx.BlockHeight()
	}
	if observedHeight <= 0 {
		observedHeight = finalisedHeight
	}

	store := k.kvStore(ctx)
	for _, ref := range refs {
		key := xmrSpentOutputRefKey(vaultPubKey, observedHeight, finalisedHeight, ref)
		store.Set(key, k.cdc.MustMarshal(&ref))
	}
	k.pruneXMRSpentOutputRefs(ctx, vaultPubKey, finalisedHeight)
	return nil
}

func (k KVStore) GetXMRSpentOutputRefs(ctx cosmos.Context, vaultPubKey common.PubKey) ([]common.OutputRef, error) {
	if vaultPubKey.IsEmpty() {
		return nil, nil
	}

	prefix := xmrSpentOutputRefVaultPrefix(vaultPubKey)
	iterator := k.getIterator(ctx, kvPrefix(prefix))
	defer iterator.Close()

	seen := make(map[string]struct{})
	var refs []common.OutputRef
	for ; iterator.Valid(); iterator.Next() {
		var ref common.OutputRef
		if err := k.cdc.Unmarshal(iterator.Value(), &ref); err != nil {
			return nil, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", &ref, iterator.Key()), err)
		}
		key := common.SpentOutputRefIdentity(ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}

	sort.Slice(refs, func(i, j int) bool {
		return common.SpentOutputRefIdentity(refs[i]) < common.SpentOutputRefIdentity(refs[j])
	})
	return refs, nil
}

func (k KVStore) pruneXMRSpentOutputRefs(ctx cosmos.Context, vaultPubKey common.PubKey, finalisedHeight int64) {
	if finalisedHeight <= xmrSpentOutputRefRetentionBlocks {
		return
	}
	cutoff := finalisedHeight - xmrSpentOutputRefRetentionBlocks
	prefix := xmrSpentOutputRefVaultPrefix(vaultPubKey)
	iterator := k.getIterator(ctx, kvPrefix(prefix))
	defer iterator.Close()

	var deleteKeys [][]byte
	for ; iterator.Valid(); iterator.Next() {
		height, ok := xmrSpentOutputRefHeightFromKey(prefix, iterator.Key())
		if ok && height < cutoff {
			deleteKeys = append(deleteKeys, append([]byte(nil), iterator.Key()...))
		}
	}

	store := k.kvStore(ctx)
	for _, key := range deleteKeys {
		store.Delete(key)
	}
}
