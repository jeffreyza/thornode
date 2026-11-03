package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (k KVStore) setTssKeysignFailVoter(ctx cosmos.Context, key []byte, record TssKeysignFailVoter) error {
	store := k.kvStore(ctx)
	buf, err := k.cdc.Marshal(&record)
	if err != nil {
		return fmt.Errorf("failed to marshal TSS keysign fail voter: %w", err)
	}
	store.Set(key, buf)
	return nil
}

// getTssKeysignFailVoter decodes raw persisted bytes. State zero is a legacy
// terminal record, not a new pending voter; consensus paths must always use the
// validated public getter so unsupported states cannot be processed.
func (k KVStore) getTssKeysignFailVoter(ctx cosmos.Context, key []byte, record *TssKeysignFailVoter) (bool, error) {
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

func validateTssKeysignFailVoterState(record TssKeysignFailVoter) error {
	switch record.State {
	case types.TssKeysignFailVoterState_LegacyVoter,
		types.TssKeysignFailVoterState_PendingVoter,
		types.TssKeysignFailVoterState_CompletedVoter:
		return nil
	default:
		return fmt.Errorf("unsupported TSS keysign fail voter state: %d", record.State)
	}
}

// SetTssKeysignFailVoter saves a current voter state. Legacy state is accepted
// only from persisted pre-upgrade bytes; callers importing legacy state must
// first convert it to the explicit completed state.
func (k KVStore) SetTssKeysignFailVoter(ctx cosmos.Context, tss TssKeysignFailVoter) error {
	switch tss.State {
	case types.TssKeysignFailVoterState_PendingVoter,
		types.TssKeysignFailVoterState_CompletedVoter:
	case types.TssKeysignFailVoterState_LegacyVoter:
		return fmt.Errorf("refusing to persist legacy TSS keysign fail voter state")
	default:
		return fmt.Errorf("refusing to persist unsupported TSS keysign fail voter state: %d", tss.State)
	}
	return k.setTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, tss.String()), tss)
}

// GetTssKeysignFailVoter - gets information of a tss keysign failure voter object
func (k KVStore) GetTssKeysignFailVoter(ctx cosmos.Context, id string) (TssKeysignFailVoter, error) {
	record := TssKeysignFailVoter{ID: id}
	found, err := k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &record)
	if err != nil {
		return record, err
	}
	if !found {
		return NewTssKeysignFailVoter(id), nil
	}
	if err := validateTssKeysignFailVoterState(record); err != nil {
		return record, err
	}
	return record, nil
}
