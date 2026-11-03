package keeperv1

import (
	"encoding/binary"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setSolvencyVoter(ctx cosmos.Context, key []byte, record SolvencyVoter) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getSolvencyVoter(ctx cosmos.Context, key []byte, record *SolvencyVoter) (bool, error) {
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

// SetSolvencyVoter - save a solvency voter object
func (k KVStore) SetSolvencyVoter(ctx cosmos.Context, solvencyVoter SolvencyVoter) {
	key := fmt.Sprintf("%s-%s", solvencyVoter.Chain, solvencyVoter.Id)
	k.setSolvencyVoter(ctx, k.GetKey(prefixSolvencyVoter, key), solvencyVoter)
}

// GetSolvencyVoter - gets information of solvency voter
func (k KVStore) GetSolvencyVoter(ctx cosmos.Context, txID common.TxID, chain common.Chain) (SolvencyVoter, error) {
	key := fmt.Sprintf("%s-%s", chain, txID)
	var solvencyVoter SolvencyVoter
	_, err := k.getSolvencyVoter(ctx, k.GetKey(prefixSolvencyVoter, key), &solvencyVoter)
	return solvencyVoter, err
}

// GetSolvencyVoterQuota reads a validator's last admission window and creation
// count for the given chain.
// Missing records have an unused allowance; malformed records fail closed.
func (k KVStore) GetSolvencyVoterQuota(ctx cosmos.Context, chain common.Chain, signer cosmos.AccAddress) (window, used uint64, err error) {
	key := fmt.Sprintf("%s-%s", chain, signer)
	store := k.kvStore(ctx)
	bz := store.Get(k.GetKey(prefixSolvencyVoterQuota, key))
	if bz == nil {
		return 0, 0, nil
	}
	if len(bz) != 16 {
		return 0, 0, fmt.Errorf("invalid solvency voter quota for %s on %s: expected 16 bytes, got %d", signer, chain, len(bz))
	}
	return binary.BigEndian.Uint64(bz[:8]), binary.BigEndian.Uint64(bz[8:]), nil
}

// SetSolvencyVoterQuota overwrites one record per chain and validator, rather than
// accumulating a key per window. Its two uint64 fields use big-endian encoding.
func (k KVStore) SetSolvencyVoterQuota(ctx cosmos.Context, chain common.Chain, signer cosmos.AccAddress, window, used uint64) {
	key := fmt.Sprintf("%s-%s", chain, signer)
	bz := make([]byte, 16)
	binary.BigEndian.PutUint64(bz[:8], window)
	binary.BigEndian.PutUint64(bz[8:], used)
	store := k.kvStore(ctx)
	store.Set(k.GetKey(prefixSolvencyVoterQuota, key), bz)
}
