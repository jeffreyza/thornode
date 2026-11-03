package keeperv1

import (
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// The observation tally records, per Active node and chain, how often the node
// was missing from the in-time signers of a voter that reached consensus on that
// chain. Only misses are stored per node. Totals are stored once per chain, and a
// node's own total is the chain total minus the chain total at the node's last
// churn-in (its base), so a voter costs one write for the chain plus one per
// missing node rather than one per Active node. A node that churned in after the
// voter reached consensus also gets its base moved up, so the voter is left out of
// its total.
//
// Values are in caller-chosen units and are halved together by
// HalveObservationTallies, so a ratio of missed to (total - base) is unaffected by
// the halving apart from rounding.

// QueueObservationTally schedules a voter to be scored once its
// ObservationDelayFlexibility window has closed.
func (k KVStore) QueueObservationTally(ctx cosmos.Context, entry types.ObservationTallyEntry) {
	record := append([]string{
		entry.Kind,
		entry.Chain.String(),
		strconv.FormatInt(entry.ConsensusHeight, 10),
	}, entry.Ref...)
	k.setStrings(ctx, k.observationTallyDueKey(entry), record)
}

// PopObservationTallies returns and removes every queued entry due at or before
// height, in key order (due height, then kind and voter reference). An entry that
// cannot be read is logged and removed rather than returned as an error, so one bad
// record cannot stall the queue behind it.
func (k KVStore) PopObservationTallies(ctx cosmos.Context, height int64) ([]types.ObservationTallyEntry, error) {
	prefixKey := k.GetKey(prefixObservationTallyDue, "")
	iter := k.getIterator(ctx, types.DbPrefix(prefixKey))
	var entries []types.ObservationTallyEntry
	var keys [][]byte
	for ; iter.Valid(); iter.Next() {
		rest := strings.TrimPrefix(string(iter.Key()), string(prefixKey))
		dueStr, _, _ := strings.Cut(rest, "/")
		due, err := strconv.ParseInt(dueStr, 10, 64)
		if err != nil {
			ctx.Logger().Error("drop observation tally entry with bad due height", "key", string(iter.Key()), "error", err)
			keys = append(keys, append([]byte(nil), iter.Key()...))
			continue
		}
		if due > height {
			break
		}
		keys = append(keys, append([]byte(nil), iter.Key()...))
		value := ProtoStrings{}
		if err = k.cdc.Unmarshal(iter.Value(), &value); err != nil {
			ctx.Logger().Error("drop unreadable observation tally entry", "key", string(iter.Key()), "error", err)
			continue
		}
		if len(value.Value) < 3 {
			ctx.Logger().Error("drop malformed observation tally entry", "key", string(iter.Key()))
			continue
		}
		chain, err := common.NewChain(value.Value[1])
		if err != nil {
			ctx.Logger().Error("drop observation tally entry with bad chain", "key", string(iter.Key()), "error", err)
			continue
		}
		consensusHeight, err := strconv.ParseInt(value.Value[2], 10, 64)
		if err != nil {
			ctx.Logger().Error("drop observation tally entry with bad height", "key", string(iter.Key()), "error", err)
			continue
		}
		entries = append(entries, types.ObservationTallyEntry{
			DueHeight:       due,
			ConsensusHeight: consensusHeight,
			Kind:            value.Value[0],
			Chain:           chain,
			Ref:             value.Value[3:],
		})
	}
	iter.Close()
	for _, key := range keys {
		k.del(ctx, key)
	}
	return entries, nil
}

// AddObservationTally adds units to a chain's total and to the missed count of
// each missed node on that chain. The base of each unscored node moves up with the
// chain total, so the units count towards neither its misses nor its own total.
func (k KVStore) AddObservationTally(ctx cosmos.Context, chain common.Chain, units int64, missed, unscored []cosmos.AccAddress) {
	k.addInt64(ctx, k.GetKey(prefixObservationTotal, chain.String()), units)
	for _, addr := range missed {
		k.addInt64(ctx, k.observationMissedKey(addr, chain), units)
	}
	for _, addr := range unscored {
		k.addInt64(ctx, k.observationBaseKey(addr, chain), units)
	}
}

// GetObservationTally returns a node's missed count and its own total on a chain
// since its last churn-in, both in tally units.
func (k KVStore) GetObservationTally(ctx cosmos.Context, addr cosmos.AccAddress, chain common.Chain) (missed, total int64, err error) {
	var chainTotal, base int64
	if _, err = k.getInt64(ctx, k.GetKey(prefixObservationTotal, chain.String()), &chainTotal); err != nil {
		return 0, 0, err
	}
	if _, err = k.getInt64(ctx, k.observationBaseKey(addr, chain), &base); err != nil {
		return 0, 0, err
	}
	if _, err = k.getInt64(ctx, k.observationMissedKey(addr, chain), &missed); err != nil {
		return 0, 0, err
	}
	// Halving rounds the chain total and the base separately, so the difference can
	// be off by one unit; never let it go negative.
	return missed, max(chainTotal-base, 0), nil
}

// GetObservationTallies returns a node's tally on every chain that has a total, in
// ascending chain order.
func (k KVStore) GetObservationTallies(ctx cosmos.Context, addr cosmos.AccAddress) ([]types.ObservationTally, error) {
	chains, err := k.observationChains(ctx, prefixObservationTotal)
	if err != nil {
		return nil, err
	}
	result := make([]types.ObservationTally, 0, len(chains))
	for _, chain := range chains {
		missed, total, err := k.GetObservationTally(ctx, addr, chain)
		if err != nil {
			return nil, err
		}
		result = append(result, types.ObservationTally{Chain: chain, Missed: missed, Total: total})
	}
	return result, nil
}

// GetObservationGuardSince returns the start of the current run of voters the
// shared-event guard skipped on a chain, moved back by the grace that earlier runs
// used and that has not refilled yet, or 0 if the last voter scored on that chain
// passed the guard.
func (k KVStore) GetObservationGuardSince(ctx cosmos.Context, chain common.Chain) (int64, error) {
	var since int64
	_, err := k.getInt64(ctx, k.GetKey(prefixObservationGuard, chain.String()), &since)
	return since, err
}

// GetObservationGuardChains returns the chains with an open run of guarded voters, in
// ascending order.
func (k KVStore) GetObservationGuardChains(ctx cosmos.Context) (common.Chains, error) {
	return k.observationChains(ctx, prefixObservationGuard)
}

// SetObservationGuardSince records the start of a chain's run of guarded voters; 0
// clears it.
func (k KVStore) SetObservationGuardSince(ctx cosmos.Context, chain common.Chain, height int64) {
	key := k.GetKey(prefixObservationGuard, chain.String())
	if height <= 0 {
		k.del(ctx, key)
		return
	}
	k.setInt64(ctx, key, height)
}

// GetObservationGuardFullAt returns the height at which the shared-event guard's grace
// on a chain is full again after its last run of guarded voters, or 0 if there is no
// such run to refill from.
func (k KVStore) GetObservationGuardFullAt(ctx cosmos.Context, chain common.Chain) (int64, error) {
	var fullAt int64
	_, err := k.getInt64(ctx, k.GetKey(prefixObservationGuardFull, chain.String()), &fullAt)
	return fullAt, err
}

// SetObservationGuardFullAt records the height at which a chain's guard grace is full
// again; 0 clears it.
func (k KVStore) SetObservationGuardFullAt(ctx cosmos.Context, chain common.Chain, height int64) {
	key := k.GetKey(prefixObservationGuardFull, chain.String())
	if height <= 0 {
		k.del(ctx, key)
		return
	}
	k.setInt64(ctx, key, height)
}

// ResetObservationTally starts a node's tally afresh at churn-in: its misses are
// dropped and its base on every chain is set to the current chain total.
func (k KVStore) ResetObservationTally(ctx cosmos.Context, addr cosmos.AccAddress) error {
	k.ClearObservationTally(ctx, addr)
	chains, err := k.observationChains(ctx, prefixObservationTotal)
	if err != nil {
		return err
	}
	for _, chain := range chains {
		var chainTotal int64
		if _, err := k.getInt64(ctx, k.GetKey(prefixObservationTotal, chain.String()), &chainTotal); err != nil {
			return err
		}
		if chainTotal > 0 {
			k.setInt64(ctx, k.observationBaseKey(addr, chain), chainTotal)
		}
	}
	return nil
}

// ClearObservationTally removes a node's misses and bases on every chain.
func (k KVStore) ClearObservationTally(ctx cosmos.Context, addr cosmos.AccAddress) {
	for _, prefix := range []types.DbPrefix{prefixObservationMissed, prefixObservationBase} {
		k.deletePrefix(ctx, types.DbPrefix(k.GetKey(prefix, addr.String(), "/")))
	}
}

// HalveObservationTallies halves every chain total, base and missed count,
// deleting any that reach zero.
func (k KVStore) HalveObservationTallies(ctx cosmos.Context) error {
	for _, prefix := range []types.DbPrefix{prefixObservationTotal, prefixObservationBase, prefixObservationMissed} {
		iter := k.getIterator(ctx, prefix)
		type update struct {
			key   []byte
			value int64
		}
		var updates []update
		for ; iter.Valid(); iter.Next() {
			value := ProtoInt64{}
			if err := k.cdc.Unmarshal(iter.Value(), &value); err != nil {
				iter.Close()
				return dbError(ctx, "Unmarshal: observation tally", err)
			}
			updates = append(updates, update{key: append([]byte(nil), iter.Key()...), value: value.Value / 2})
		}
		iter.Close()
		for _, u := range updates {
			if u.value <= 0 {
				k.del(ctx, u.key)
				continue
			}
			k.setInt64(ctx, u.key, u.value)
		}
	}
	return nil
}

func (k KVStore) observationTallyDueKey(entry types.ObservationTallyEntry) []byte {
	return k.GetKey(prefixObservationTallyDue, fmt.Sprintf("%020d", entry.DueHeight),
		"/", entry.Kind, "/", entry.Chain.String(), "/", strings.Join(entry.Ref, "/"))
}

func (k KVStore) observationMissedKey(addr cosmos.AccAddress, chain common.Chain) []byte {
	return k.GetKey(prefixObservationMissed, addr.String(), "/", chain.String())
}

func (k KVStore) observationBaseKey(addr cosmos.AccAddress, chain common.Chain) []byte {
	return k.GetKey(prefixObservationBase, addr.String(), "/", chain.String())
}

// observationChains returns the chains with a key under prefix, in ascending order.
func (k KVStore) observationChains(ctx cosmos.Context, prefix types.DbPrefix) (common.Chains, error) {
	prefixKey := k.GetKey(prefix, "")
	iter := k.getIterator(ctx, types.DbPrefix(prefixKey))
	defer iter.Close()
	chains := make(common.Chains, 0)
	for ; iter.Valid(); iter.Next() {
		chain, err := common.NewChain(strings.TrimPrefix(string(iter.Key()), string(prefixKey)))
		if err != nil {
			return nil, fmt.Errorf("fail to parse observation tally chain: %w", err)
		}
		chains = append(chains, chain)
	}
	return chains, nil
}

func (k KVStore) addInt64(ctx cosmos.Context, key []byte, delta int64) {
	var value int64
	if _, err := k.getInt64(ctx, key, &value); err != nil {
		// Overwrite a corrupted value rather than stop counting, as SetLastObserveHeight does.
		ctx.Logger().Error("fail to get observation tally", "key", string(key), "error", err)
		value = 0
	}
	k.setInt64(ctx, key, value+delta)
}

func (k KVStore) deletePrefix(ctx cosmos.Context, prefix types.DbPrefix) {
	iter := k.getIterator(ctx, prefix)
	var keys [][]byte
	for ; iter.Valid(); iter.Next() {
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	iter.Close()
	for _, key := range keys {
		k.del(ctx, key)
	}
}
