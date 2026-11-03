package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setNetworkFee(ctx cosmos.Context, key []byte, record NetworkFee) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getNetworkFee(ctx cosmos.Context, key []byte, record *NetworkFee) (bool, error) {
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

// GetNetworkFee get the network fee of the given chain from kv store , if it doesn't exist , it will create an empty one
func (k KVStore) GetNetworkFee(ctx cosmos.Context, chain common.Chain) (NetworkFee, error) {
	record := NetworkFee{
		Chain:              chain,
		TransactionSize:    0,
		TransactionFeeRate: 0,
	}
	_, err := k.getNetworkFee(ctx, k.GetKey(prefixNetworkFee, chain.String()), &record)
	return record, err
}

// SaveNetworkFee save the network fee to kv store
func (k KVStore) SaveNetworkFee(ctx cosmos.Context, chain common.Chain, networkFee NetworkFee) error {
	if err := networkFee.Valid(); err != nil {
		return err
	}
	k.setNetworkFee(ctx, k.GetKey(prefixNetworkFee, chain.String()), networkFee)
	return nil
}

// GetNetworkFeeIterator
func (k KVStore) GetNetworkFeeIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixNetworkFee)
}
