package thorchain

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Migrate4to5 migrates from version 4 to 5.
func (m Migrator) ClearObsoleteMimirs(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// Issue #2112, clearing obsolete Mimir keys.

	toClear := func(key string) bool {
		upperKey := strings.ToUpper(key)
		return (strings.Contains(upperKey, "BNB") && !strings.Contains(upperKey, "BSC")) || // Do not clear BSC-BNB keys.
			strings.Contains(upperKey, "TERRA") ||
			strings.Contains(upperKey, "YGG") ||
			strings.EqualFold(key, "MaxConfirmations") || // Only effective with -<Chain> .
			strings.EqualFold(key, "ConfMultiplierBasisPoints") || // Only effective with -<Chain> .
			strings.EqualFold(key, "SystemIncomeBurnRateBp") // Only Bps effective, not Bp.
	}

	iterNode := m.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iterNode.Close()
	for ; iterNode.Valid(); iterNode.Next() {
		key := trimKeyPrefix(iterNode.Key())

		if !toClear(key) {
			continue
		}

		// As with PurgeOperationalNodeMimirs,
		// not emitting individual EventSetNodeMimir events.
		m.mgr.Keeper().DeleteNodeMimirs(ctx, key)
	}

	iter := m.mgr.Keeper().GetMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key := trimKeyPrefix(iter.Key())

		if !toClear(key) {
			continue
		}

		if err := m.mgr.Keeper().DeleteMimir(ctx, key); err != nil {
			ctx.Logger().Error("fail to delete mimir", "key", key, "error", err)
			continue
		}

		// As with Admin key deletion, emit a deletion event.
		mimirEvent := NewEventSetMimir(strings.ToUpper(key), "-1")
		if err := m.mgr.EventMgr().EmitEvent(ctx, mimirEvent); err != nil {
			ctx.Logger().Error("fail to emit set_mimir event", "error", err)
		}
	}

	return nil
}

// Migrate7to8 migrates from version 7 to 8.
func (m Migrator) Migrate7to8(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// Update all KVStore network fees from (Mainnet-only) legacy 1e8 TransactionFeeRate
	// to gas rate units TransactionFeeRate.
	for _, chain := range common.AllChains {
		_, gasRateUnitsPerOne := chain.GetGasUnits()

		if gasRateUnitsPerOne.Equal(cosmos.NewUint(common.One)) {
			// This is already in the right units.
			continue
		}

		if chain.IsTHORChain() {
			// GetGasUnits doesn't have a THORChain entry,
			// so in case of unintended effects skip this.
			continue
		}

		networkFee, err := m.mgr.Keeper().GetNetworkFee(ctx, chain)
		if err != nil {
			ctx.Logger().Error("Error getting NetworkFee for chain", "chain", chain.String(), "error", err)
			return err
		}
		ctx.Logger().Info("NetworkFee details", "chain", chain.String(), "transactionSize", networkFee.TransactionSize, "transactionFeeRate", networkFee.TransactionFeeRate)

		// Skip if TransactionSize is 0 to avoid validation error
		if networkFee.TransactionSize == 0 {
			ctx.Logger().Info("Skipping chain due to zero TransactionSize", "chain", chain.String())
			continue
		}

		networkFee.TransactionFeeRate = cosmos.NewUint(networkFee.TransactionFeeRate).Mul(gasRateUnitsPerOne).QuoUint64(common.One).Uint64()
		if err := m.mgr.Keeper().SaveNetworkFee(ctx, chain, networkFee); err != nil {
			return err
		}
	}

	return nil
}

func (m Migrator) CommonMigrate8to9(ctx sdk.Context) error {
	// reduce the minimum L1 outbound fee to $0.25
	m.mgr.Keeper().SetMimir(ctx, constants.MinimumL1OutboundFeeUSD.String(), 25000000)

	// ------------------------------ Asset Gas Multiplier Adjustments ------------------------------
	//
	// We are going to adjust the target surplus and multipliers for all assets. The steps are:
	// 1. Adjust the target surplus to 10k RUNE
	// 2. Compute the new multiplier for each asset
	// 3. If the multiplier is less than or equal to 100%, do nothing
	// 4. If the multiplier is greater than 100%, increase the withheld amount so that the multiplier is equal to 100%

	// Adjust the target surplus to 10k RUNE
	m.mgr.Keeper().SetMimir(ctx, constants.TargetOutboundFeeSurplusRune.String(), 10000_00000000) // 10k x 10^8

	// Adjust minimum DOFM multiplier to 1%
	// https://gitlab.com/thorchain/thornode/-/issues/2239#note_2690577110
	m.mgr.Keeper().SetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String(), 100)

	// Gather all the parameters for the multiplier calculation
	targetSurplus := cosmos.NewUint(uint64(m.mgr.Keeper().GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)))
	maxMultiplier := cosmos.NewUint(uint64(m.mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)))
	minMultiplier := cosmos.NewUint(uint64(m.mgr.Keeper().GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)))

	// Collect all the assets via pool iterator (copied from querier.go outbound fees endpoint)
	var assets []common.Asset
	iterator := m.mgr.Keeper().GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := m.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return fmt.Errorf("fail to unmarshal pool: %w", err)
		}

		if pool.Asset.IsNative() {
			// To avoid clutter do not by default display the outbound fees
			// of THORChain Assets other than RUNE.
			continue
		}
		if pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() {
			// A Layer 1 Asset's pool must have both depths be non-zero
			// for any outbound fee withholding or gas reimbursement to take place.
			// (This can take place even if the PoolUnits are zero and all liquidity is synths.)
			continue
		}
		if pool.Status != types.PoolStatus_Available {
			// A Layer 1 Asset's pool must be available
			// for any outbound fee withholding or gas reimbursement to take place.
			continue
		}

		assets = append(assets, pool.Asset)
	}

	type feesAndMultiplier struct {
		withheld   cosmos.Uint
		spent      cosmos.Uint
		surplus    cosmos.Uint
		multiplier cosmos.Uint
	}

	getFeesAndMultiplier := func(ctx cosmos.Context, asset common.Asset) (feesAndMultiplier, error) {
		var err error
		var fem feesAndMultiplier
		fem.withheld, err = m.mgr.Keeper().GetOutboundFeeWithheldRune(ctx, asset)
		if err != nil {
			return fem, err
		}
		fem.spent, err = m.mgr.Keeper().GetOutboundFeeSpentRune(ctx, asset)
		if err != nil {
			return fem, err
		}
		fem.surplus = common.SafeSub(fem.withheld, fem.spent)
		fem.multiplier = m.mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, targetSurplus,
			fem.spent, fem.withheld, maxMultiplier, minMultiplier)
		return fem, nil
	}

	processAsset := func(asset common.Asset, targetMultiplier cosmos.Uint) error {
		before, err := getFeesAndMultiplier(ctx, asset)
		if err != nil {
			ctx.Logger().Error("failed to get fees and multiplier before adjustment", "asset", asset, "error", err)
			return err
		}

		equilibriumSurplus := m.mgr.Keeper().GetSurplusForTargetMultiplier(ctx, targetMultiplier)
		if before.multiplier.LTE(targetMultiplier) {
			ctx.Logger().Info(
				"multiplier is less than 100%; no adjustment needed",
				"asset", asset,
				"multiplier", before.multiplier,
				"surplus", before.surplus,
				"target_multiplier", targetMultiplier,
				"equilibrium_surplus", equilibriumSurplus,
				"withheld_adjustment", "none",
			)
			return nil
		}

		// Increase withheld so that the multiplier is equal to 100%
		withheldAdjustment := common.SafeSub(equilibriumSurplus, before.surplus)
		err = m.mgr.Keeper().AddToOutboundFeeWithheldRune(ctx, asset, withheldAdjustment)
		if err != nil {
			ctx.Logger().Error("failed to adjust withheld amount", "asset", asset, "adjustment", withheldAdjustment, "error", err)
			return err
		}

		after, err := getFeesAndMultiplier(ctx, asset)
		if err != nil {
			ctx.Logger().Error("failed to get fees and multiplier after adjustment", "asset", asset, "error", err)
			return err
		}

		ctx.Logger().Info(
			"multiplier was greater than target; adjusted",
			"asset", asset,
			"before_withheld", before.withheld,
			"before_surplus", before.surplus,
			"before_multiplier", before.multiplier,
			"target_multiplier", targetMultiplier,
			"equilibrium_surplus", equilibriumSurplus,
			"adjustment", withheldAdjustment,
			"after_withheld", after.withheld,
			"after_surplus", after.surplus,
			"after_multiplier", after.multiplier,
		)

		return nil
	}

	for _, asset := range assets {
		err := processAsset(asset, cosmos.NewUint(10_000)) // target 100% multiplier
		if err != nil {
			return err
		}
	}

	// Special case TRON.TRX to target 10% multiplier
	// https://gitlab.com/thorchain/thornode/-/issues/2239#note_2690577110
	return processAsset(common.TRXAsset, cosmos.NewUint(1_000))
}

// BurnReserveAndReduceMaxSupply implements ADR-023: burns reserve down to 9.3M RUNE
// and sets MaxRuneSupply to the post-burn total supply (derived from live state).
func (m Migrator) BurnReserveAndReduceMaxSupply(ctx sdk.Context) error {
	const reserveRetain uint64 = 9_300_000_00000000 // 9.3M RUNE to keep in reserve

	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	retainAmount := cosmos.NewUint(reserveRetain)

	if reserveBalance.GT(retainAmount) {
		burnAmount := common.SafeSub(reserveBalance, retainAmount)
		burnCoin := common.NewCoin(common.RuneNative, burnAmount)

		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, ModuleName, common.NewCoins(burnCoin)); err != nil {
			return fmt.Errorf("fail to transfer reserve RUNE for burn: %w", err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, burnCoin); err != nil {
			return fmt.Errorf("fail to burn reserve RUNE: %w", err)
		}

		burnEvt := NewEventMintBurn(BurnSupplyType, burnCoin.Asset.Native(), burnCoin.Amount, "adr023_reserve_burn")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("fail to emit reserve burn event", "error", err)
		}

		ctx.Logger().Info("ADR-023: burned reserve RUNE",
			"burn_amount", burnAmount.String(),
			"remaining_reserve", retainAmount.String())
	} else {
		ctx.Logger().Info("ADR-023: reserve balance already at or below target, skipping burn",
			"reserve_balance", reserveBalance.String(),
			"retain_target", retainAmount.String())
	}

	// Derive MaxRuneSupply from post-burn total supply to avoid state drift.
	postBurnSupply := m.mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset())
	newMaxSupply := int64(postBurnSupply.Uint64())

	m.mgr.Keeper().SetMimir(ctx, constants.MaxRuneSupply.String(), newMaxSupply)
	ctx.Logger().Info("ADR-023: set MaxRuneSupply to post-burn total supply", "new_max_supply", newMaxSupply)

	return nil
}

// Migrate18to19 replaces owner-based affiliate collectors with THORName collectors.
// Keep the balance in the owner's sole stored THORName if its spelling still meets
// registration rules; otherwise pay the owner in RUNE. Module-owned balances go
// to Reserve. The old ledger cannot split earnings.
func (m Migrator) Migrate18to19(ctx cosmos.Context) error {
	ctx, commit := ctx.CacheContext()
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	k := m.mgr.Keeper()
	// Keep the old key layout confined to this migration. The deprecated owner
	// field is ignored; the store key identifies the owner of each legacy balance.
	const legacyCollectorPrefix = "affcol/" // Retire with migration 18 to 19.
	legacyPrefix := k.GetKey(legacyCollectorPrefix, "")
	type entry struct {
		key    []byte
		owner  cosmos.AccAddress
		amount cosmos.Uint
	}
	var entries []entry
	totalRune := cosmos.ZeroUint()
	store := runtime.KVStoreAdapter(m.mgr.storeService.OpenKVStore(ctx))
	collectors := cosmos.KVStorePrefixIterator(store, []byte(legacyCollectorPrefix))
	for ; collectors.Valid(); collectors.Next() {
		key := collectors.Key()
		owner, err := cosmos.AccAddressFromBech32(strings.ToLower(strings.TrimPrefix(string(key), string(legacyPrefix))))
		if err != nil || !bytes.Equal(key, k.GetKey(legacyCollectorPrefix, owner.String())) {
			collectors.Close()
			return fmt.Errorf("invalid owner collector key %q", key)
		}
		var collector AffiliateFeeCollector
		if err := k.Cdc().Unmarshal(collectors.Value(), &collector); err != nil {
			collectors.Close()
			return fmt.Errorf("read collector for %s: %w", owner, err)
		}
		if collector.RuneAmount.IsNil() || collector.Name != "" {
			collectors.Close()
			return fmt.Errorf("invalid owner collector for %s", owner)
		}
		entries = append(entries, entry{append([]byte(nil), key...), owner, collector.RuneAmount})
		totalRune = totalRune.Add(collector.RuneAmount)
	}
	collectors.Close()
	// Reconcile only during the upgrade, never against later changes in funds.
	if len(entries) == 0 {
		return nil
	}

	// Count all stored THORNames, including expired THORNames and those accepted
	// under older registration rules. Pay the owner if there are multiple THORNames
	// or the spelling cannot be renewed or re-registered; preserve the THORName records.
	// Only look up map entries; store iteration determines the write order.
	singleNames := make(map[string]string)
	names := k.GetTHORNameIterator(ctx)
	for ; names.Valid(); names.Next() {
		var tn THORName
		if err := k.Cdc().Unmarshal(names.Value(), &tn); err != nil {
			names.Close()
			return fmt.Errorf("read THORName during collector migration: %w", err)
		}
		if tn.Owner.Empty() {
			continue
		}
		owner := tn.Owner.String()
		if _, exists := singleNames[owner]; exists || len(tn.Name) > 30 || !IsValidTHORName(tn.Name) {
			singleNames[owner] = ""
		} else {
			singleNames[owner] = strings.ToLower(tn.Name)
		}
	}
	names.Close()

	// Treat recorded claims as authoritative. Reserve covers any deficit or
	// receives surplus before payouts. Include existing THORName claims too.
	namedCollectors, err := k.GetAffiliateCollectors(ctx)
	if err != nil {
		return err
	}
	for _, collector := range namedCollectors {
		if collector.RuneAmount.IsNil() {
			return fmt.Errorf("invalid named collector amount for %s", collector.Name)
		}
		totalRune = totalRune.Add(collector.RuneAmount)
	}
	balance := k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName)
	from, to := ReserveName, AffiliateCollectorName
	amount := common.SafeSub(totalRune, balance)
	if balance.GT(totalRune) {
		from, to = to, from
		amount = balance.Sub(totalRune)
	}
	if !amount.IsZero() {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := k.SendFromModuleToModule(ctx, from, to, coins); err != nil {
			return fmt.Errorf("reconcile affiliate collector balance from %s to %s: %w", from, to, err)
		}
		ctx.EventManager().EmitEvent(cosmos.NewEvent("affiliate_collector_migration_reconcile",
			cosmos.NewAttribute("from_module", from),
			cosmos.NewAttribute("to_module", to),
			cosmos.NewAttribute("rune_amount", amount.String()),
		))
	}

	// Iterators are closed before writes. The cache makes reconciliation, THORName
	// credits, payouts and old-key deletions succeed or roll back together.
	for _, item := range entries {
		if !item.amount.IsZero() {
			event := cosmos.NewEvent("affiliate_collector_migration",
				cosmos.NewAttribute("owner", item.owner.String()),
				cosmos.NewAttribute("rune_amount", item.amount.String()),
			)
			if k.IsModuleAccount(ctx, item.owner) {
				// Old ownership transfers could leave fees assigned to a module.
				// Move them to Reserve rather than creating an unusable collector.
				coins := common.NewCoins(common.NewCoin(common.RuneAsset(), item.amount))
				if err := k.SendFromModuleToModule(ctx, AffiliateCollectorName, ReserveName, coins); err != nil {
					return fmt.Errorf("move affiliate collector for %s to Reserve: %w", item.owner, err)
				}
				event = event.AppendAttributes(cosmos.NewAttribute("destination", k.GetModuleAccAddress(ReserveName).String()))
			} else if name := singleNames[item.owner.String()]; name != "" {
				collector, err := k.GetAffiliateCollector(ctx, name)
				if err != nil {
					return err
				}
				collector.RuneAmount = collector.RuneAmount.Add(item.amount)
				if err = k.SetAffiliateCollector(ctx, collector); err != nil {
					return err
				}
				event = event.AppendAttributes(cosmos.NewAttribute("thorname", name))
			} else {
				coins := common.NewCoins(common.NewCoin(common.RuneAsset(), item.amount))
				if err := k.SendFromModuleToAccount(ctx, AffiliateCollectorName, item.owner, coins); err != nil {
					return fmt.Errorf("pay affiliate collector to %s: %w", item.owner, err)
				}
			}
			ctx.EventManager().EmitEvent(event)
		}
		k.DeleteKey(ctx, item.key)
	}
	commit()
	return nil
}

// Migrate19to20 merges swapper clout recorded under ZEC TEX addresses into the
// transparent address record. Outbounds now go to the transparent address, and
// the keeper credits, spends and reclaims clout under that form.
func (m Migrator) Migrate19to20(ctx cosmos.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	k := m.mgr.Keeper()
	type entry struct {
		key   []byte
		clout SwapperClout
	}
	var entries []entry
	iter := k.GetSwapperCloutIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		clout := NewSwapperClout(common.NoAddress)
		if err := k.Cdc().Unmarshal(iter.Value(), &clout); err != nil {
			ctx.Logger().Error("fail to read swapper clout", "key", string(iter.Key()), "error", err)
			continue
		}
		if strings.HasPrefix(clout.Address.String(), "tex") {
			entries = append(entries, entry{append([]byte(nil), iter.Key()...), clout})
		}
	}
	iter.Close()

	// Iterators are closed before writes. Restore each record before merging so a
	// recent spend on one cannot keep the other's stale spend outstanding.
	cloutReset := k.GetConfigInt64(ctx, constants.CloutReset)
	for _, item := range entries {
		transparent, err := common.ConvertToZECTransparentAddress(item.clout.Address)
		if err != nil {
			ctx.Logger().Error("fail to convert swapper clout address", "address", item.clout.Address, "error", err)
			continue
		}
		merged, err := k.GetSwapperClout(ctx, transparent)
		if err != nil {
			ctx.Logger().Error("fail to get swapper clout", "address", transparent, "error", err)
			continue
		}
		merged.Restore(ctx.BlockHeight(), cloutReset)
		item.clout.Restore(ctx.BlockHeight(), cloutReset)
		merged.Score = merged.Score.Add(item.clout.Score)
		merged.Spent = merged.Spent.Add(item.clout.Spent)
		merged.Reclaimed = merged.Reclaimed.Add(item.clout.Reclaimed)
		merged.LastSpentHeight = max(merged.LastSpentHeight, item.clout.LastSpentHeight)
		merged.LastReclaimHeight = max(merged.LastReclaimHeight, item.clout.LastReclaimHeight)
		if err := k.SetSwapperClout(ctx, merged); err != nil {
			ctx.Logger().Error("fail to save swapper clout", "address", transparent, "error", err)
			continue
		}
		k.DeleteKey(ctx, item.key)
		ctx.Logger().Info("merged TEX swapper clout", "tex", item.clout.Address, "address", transparent)
	}
	// Mainnet-only follow-on steps (no-op off mainnet): reverse the erroneous rzxslmgc /
	// h28181638 ZEC / h28172517 TRON slashes + refund bond providers, donate the Secured-Ops
	// LP positions + repay NDT, restore ETH.THOR depth, and retire the low-synth pools. Run
	// AFTER the TEX clout merge so develop's step semantics are unchanged; each sub-step
	// soft-skips on drift and the whole set is gated behind mainnetDonationSpecComplete.
	if err := m.migrate19to20Mainnet(ctx); err != nil {
		return err
	}
	return nil
}

// Migrate20to21 writes down the GAIA.ATOM that vault ...jy4h was credited for
// five inbounds it never received on the Cosmos Hub, and replenishes the
// GAIA.ATOM pool with Reserve RUNE. Mainnet only; the step soft-skips on drift.
func (m Migrator) Migrate20to21(ctx cosmos.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	return m.migrate20to21Mainnet(ctx)
}
