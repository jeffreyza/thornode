package keeperv1

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// SetLastKeygenChains records the chain set a node reported in an asgard keygen
// result, keyed by the keygen block height the report was for.
//
// TssVoter.Sign concatenates every member's chains into one slice, so the voter
// cannot say which member reported which chain. The report itself is the only
// evidence that a node is running with a chain disabled, so keep it per node: one
// entry per declared chain holding the keygen height, plus the keygen height of the
// report itself. GetLastKeygenChains returns the chains recorded at the report height.
//
// Writes are forward-only on the keygen height, mirroring SetLastObserveHeight, so a
// report for an older keygen block arriving late cannot overwrite a newer one. A report
// for a newer keygen block replaces the recorded set.
//
// A second report for the same keygen block is intersected with the first, never
// merged. MsgTssPool.ID does not cover Chains, so a node can open a second voter for
// the same height (a bogus pool pubkey that never reaches consensus) carrying the full
// chain set. A union would let that report mask the incomplete one the real ceremony
// received, and a plain replace would do the same whenever it lands second. An honest
// node sends one asgard report per keygen block, so the intersection only ever narrows
// the set of a node that reports twice, and only to its own detriment.
func (k KVStore) SetLastKeygenChains(ctx cosmos.Context, address cosmos.AccAddress, chains common.Chains, keygenHeight int64) {
	reportKey := k.GetKey(prefixLastKeygenReport, address.String())
	var lastHeight int64
	exist, err := k.getInt64(ctx, reportKey, &lastHeight)
	if err != nil {
		// Log but continue: overwriting corrupted data with a valid height is
		// intentional self-healing behavior, consistent with SetLastObserveHeight.
		ctx.Logger().Error("fail to get last keygen report height", "error", err, "address", address.String())
		exist = false
	}
	if exist && lastHeight > keygenHeight {
		return
	}
	sameReport := exist && lastHeight == keygenHeight

	// Drop every recorded chain the incoming report does not declare; for a repeat
	// report at the same height also drop chains the earlier report did not declare.
	// Keys are collected first and deleted after the iterator closes.
	prefixKey := k.GetKey(prefixLastKeygenChain, address.String(), "/")
	toDelete := make([][]byte, 0)
	iter := k.getIterator(ctx, types.DbPrefix(prefixKey))
	for ; iter.Valid(); iter.Next() {
		chain, err := common.NewChain(strings.TrimPrefix(string(iter.Key()), string(prefixKey)))
		key := append([]byte(nil), iter.Key()...)
		if err != nil || !chains.Has(chain) {
			toDelete = append(toDelete, key)
			continue
		}
		if sameReport {
			value := ProtoInt64{}
			if err := k.cdc.Unmarshal(iter.Value(), &value); err != nil || value.Value != keygenHeight {
				toDelete = append(toDelete, key)
			}
		}
	}
	iter.Close()
	for _, key := range toDelete {
		k.del(ctx, key)
	}

	if !sameReport {
		for _, chain := range chains {
			k.setInt64(ctx, k.GetKey(prefixLastKeygenChain, address.String(), "/", chain.String()), keygenHeight)
		}
	}
	k.setInt64(ctx, reportKey, keygenHeight)
}

// GetLastKeygenChains returns the keygen block height of a node's most recent asgard
// keygen report and the chains that report declared. A node that has never reported
// one returns height 0 and no chains.
//
// Chains recorded against an earlier keygen height are omitted: they were declared
// once and are not part of the latest report.
func (k KVStore) GetLastKeygenChains(ctx cosmos.Context, address cosmos.AccAddress) (int64, common.Chains, error) {
	var reportHeight int64
	exist, err := k.getInt64(ctx, k.GetKey(prefixLastKeygenReport, address.String()), &reportHeight)
	if err != nil {
		return 0, nil, err
	}
	if !exist {
		return 0, nil, nil
	}

	prefixKey := k.GetKey(prefixLastKeygenChain, address.String(), "/")
	iter := k.getIterator(ctx, types.DbPrefix(prefixKey))
	defer iter.Close()
	chains := make(common.Chains, 0)
	for ; iter.Valid(); iter.Next() {
		c := strings.TrimPrefix(string(iter.Key()), string(prefixKey))
		chain, err := common.NewChain(c)
		if err != nil {
			// An unparsable chain cannot be matched against a vault chain either,
			// so skip it rather than failing the whole read.
			ctx.Logger().Error("fail to parse keygen chain", "error", err, "chain", c)
			continue
		}
		value := ProtoInt64{}
		if err := k.cdc.Unmarshal(iter.Value(), &value); err != nil {
			return 0, nil, dbError(ctx, "Unmarshal: last keygen chain height", err)
		}
		if value.Value != reportHeight {
			continue
		}
		chains = append(chains, chain)
	}
	return reportHeight, chains, nil
}

// SetChainSetStall records that a pending asgard vault was created without a chain the
// active asgard vaults serve, during the churn cycle identified by cycleHeight: the
// creation height of the newest active asgard vault. One key per chain holds the latest
// cycle it stalled in, so records from a completed cycle are simply superseded.
func (k KVStore) SetChainSetStall(ctx cosmos.Context, chain common.Chain, cycleHeight int64) {
	k.setInt64(ctx, k.GetKey(prefixChainSetStall, chain.String()), cycleHeight)
}

// GetChainSetStalls returns the chains recorded as stalled in the given churn cycle, in
// ascending chain order.
func (k KVStore) GetChainSetStalls(ctx cosmos.Context, cycleHeight int64) (common.Chains, error) {
	prefixKey := k.GetKey(prefixChainSetStall, "")
	iter := k.getIterator(ctx, types.DbPrefix(prefixKey))
	defer iter.Close()
	chains := make(common.Chains, 0)
	for ; iter.Valid(); iter.Next() {
		value := ProtoInt64{}
		if err := k.cdc.Unmarshal(iter.Value(), &value); err != nil {
			return nil, dbError(ctx, "Unmarshal: chain set stall", err)
		}
		if value.Value != cycleHeight {
			continue
		}
		chain, err := common.NewChain(strings.TrimPrefix(string(iter.Key()), string(prefixKey)))
		if err != nil {
			ctx.Logger().Error("fail to parse chain set stall chain", "error", err, "key", string(iter.Key()))
			continue
		}
		chains = append(chains, chain)
	}
	return chains, nil
}
