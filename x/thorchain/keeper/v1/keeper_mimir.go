package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// GetMimirWithRef is a helper function to more readably insert references (such as Asset MimirString or Chain) into Mimir key templates.
func (k KVStore) GetMimirWithRef(ctx cosmos.Context, template string, ref ...any) (int64, error) {
	// 'template' should be something like "Halt%sChain" (to halt an arbitrary specified chain)
	// or "Ragnarok-%s" (to halt the pool of an arbitrary specified Asset (MimirString used for Assets to join Chain and Symbol with a hyphen).
	key := fmt.Sprintf(template, ref...)
	return k.GetMimir(ctx, key)
}

// SetMimir save a mimir value to key value store
func (k KVStore) SetMimir(ctx cosmos.Context, key string, value int64) {
	keeper.MimirCacheFromContext(ctx).Disable()
	k.setInt64(ctx, k.GetKey(prefixMimir, key), value)
}

// GetNodeMimirs get node mimirs value from key value store
func (k KVStore) GetNodeMimirs(ctx cosmos.Context, key string) (NodeMimirs, error) {
	key = strings.ToUpper(key)
	record := NodeMimirs{}
	store := k.kvStore(ctx)
	if !store.Has(k.GetKey(prefixNodeMimir, key)) {
		return record, nil
	}
	bz := store.Get(k.GetKey(prefixNodeMimir, key))
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return NodeMimirs{}, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

// SetNodeMimir save a mimir value to key value store for a specific node
func (k KVStore) SetNodeMimir(ctx cosmos.Context, key string, value int64, acc cosmos.AccAddress) error {
	key = strings.ToUpper(key)
	kvkey := k.GetKey(prefixNodeMimir, key)
	record, err := k.GetNodeMimirs(ctx, key)
	if err != nil {
		return err
	}
	record.Set(key, value, acc)

	// delete the node mimir if value is negative
	if value < 0 {
		record.Delete(key, acc)
	}

	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil || len(record.Mimirs) == 0 {
		store.Delete(kvkey)
	} else {
		store.Set(kvkey, buf)
	}
	return err
}

// DeleteNodeMimirs deletes all node mimir votes for a given key
func (k KVStore) DeleteNodeMimirs(ctx cosmos.Context, key string) {
	k.del(ctx, k.GetKey(prefixNodeMimir, key))
}

func (k KVStore) PurgeOperationalNodeMimirs(ctx cosmos.Context) {
	iterNode := k.GetNodeMimirIterator(ctx)
	defer iterNode.Close()
	for ; iterNode.Valid(); iterNode.Next() {
		key := strings.TrimPrefix(string(iterNode.Key()), string(prefixNodeMimir)+"/")
		if k.IsOperationalMimir(key) {
			k.DeleteNodeMimirs(ctx, key)
		}
	}
}

// GetMimirIterator iterate gas units
func (k KVStore) GetMimirIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixMimir)
}

// GetNodeMimirIterator iterate gas units
func (k KVStore) GetNodeMimirIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixNodeMimir)
}

func (k KVStore) DeleteMimir(ctx cosmos.Context, key string) error {
	keeper.MimirCacheFromContext(ctx).Disable()
	k.del(ctx, k.GetKey(prefixMimir, key))
	return nil
}

func (k KVStore) GetNodePauseChain(ctx cosmos.Context, acc cosmos.AccAddress) int64 {
	record := int64(-1)
	_, _ = k.getInt64(ctx, k.GetKey(prefixNodePauseChain, acc.String()), &record)
	return record
}

func (k KVStore) SetNodePauseChain(ctx cosmos.Context, acc cosmos.AccAddress) {
	k.setInt64(ctx, k.GetKey(prefixNodePauseChain, acc.String()), ctx.BlockHeight())
}

func (k KVStore) IsOperationalMimir(key string) bool {
	exactMatches := []string{
		"MintSynths",
		"TradeAccountsEnabled",
		"RUNEPoolEnabled",
		"EVMDisableContractWhitelist",
		"MaxOutboundAttempts",
		"AdvSwapQueueRapidSwapMax",
		"EnableADVSwapQueue",
		"StreamingLimitSwapMaxAge",
		constants.OverSolvencyCheckInterval.String(),
		constants.OverSolvencyToTreasuryBps.String(),
		constants.MimirKeyScheduledMigration,
		constants.MimirKeyDisableTSSSignerFiltering,
		constants.MaxRetiredVaultRecoveryAttempts.String(),
		constants.P2PGateDisabled.String(),
		constants.EnableMemolessOutbound.String(),
		constants.POLReserveSystemIncomeBps.String(),
		constants.StableReserveOraclePricing.String(),
		constants.MimirKeyPreflightChainSetDisabled,
	}
	for i := range exactMatches {
		if strings.EqualFold(key, exactMatches[i]) {
			return true
		}
	}

	exactUnmatches := []string{
		"NodePauseChainBlocks",
		"PauseOnSlashThreshold",
	}
	for i := range exactUnmatches {
		if strings.EqualFold(key, exactUnmatches[i]) {
			return false
		}
	}

	// Past this point, compare only upper-case strings due to case sensitivity.
	key = strings.ToUpper(key)
	if strings.HasPrefix(key, "COMPROMISEDVAULT-") {
		return true
	}
	// AllowNoVault-<CHAIN> opens the noop:novault debit for a brief, coordinated
	// insolvency-correction window. That is a time-critical operator action (a
	// gas top-up to a retiring vault must not be credited before the next
	// migration round), so it is operational rather than economic.
	if strings.HasPrefix(key, "ALLOWNOVAULT-") {
		return true
	}
	// XMRConsolidation* tunes bifrost's Monero output consolidation. Each run
	// locks vault liquidity for Monero's 10-block spend lock, so operators must
	// be able to tune or disable it quickly rather than wait for a supermajority.
	if strings.HasPrefix(key, "XMRCONSOLIDATION") {
		return true
	}
	partialMatches := []string{
		"HALT",
		"PAUSE",
		"STOPSOLVENCYCHECK",
		"MIMIRUPGRADECONTRACT",
		"EVMALLOWANCECHECK",
		"POLRESERVEBLACKLIST",
		"POLRESERVEWHITELIST",
		"DYNAMICFEE-WHITELIST",
		"REVSHARE",
		"EVMDIRECTERC20INBOUND",
		"SKIPSIGNERCACHE",
	}
	for i := range partialMatches {
		// Contains rather than HasPrefix to include cases like StreamingSwapPause.
		if strings.Contains(key, partialMatches[i]) {
			return true
		}
	}

	if strings.HasPrefix(key, "L1DYNAMICFEE") {
		return true
	}
	if strings.HasPrefix(key, "OVERSOLVENCYTOTREASURYBPS-") {
		return true
	}

	// all min slip mimirs are operational, but not SlipMinBpsMax
	return strings.HasSuffix(key, "SLIPMINBPS")
}
