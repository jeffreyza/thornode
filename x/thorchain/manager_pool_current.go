package thorchain

import (
	"fmt"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type PoolMgr struct{}

func newPoolMgr() *PoolMgr {
	return &PoolMgr{}
}

// EndBlock cycle pools if required and if ragnarok is not in progress
func (pm *PoolMgr) EndBlock(ctx cosmos.Context, mgr Manager) error {
	poolCycle := mgr.Keeper().GetConfigInt64(ctx, constants.PoolCycle)
	// Enable a pool every poolCycle
	if poolCycle > 0 && ctx.BlockHeight()%poolCycle == 0 && !mgr.Keeper().RagnarokInProgress(ctx) {
		var maxAvailablePools, minRunePoolDepth, stagedPoolCost int64
		var err error
		maxAvailablePools, err = mgr.Keeper().GetMimir(ctx, constants.MaxAvailablePools.String())
		if maxAvailablePools < 0 || err != nil {
			maxAvailablePools = mgr.GetConstants().GetInt64Value(constants.MaxAvailablePools)
		}
		minRunePoolDepth, err = mgr.Keeper().GetMimir(ctx, constants.MinRunePoolDepth.String())
		if minRunePoolDepth < 0 || err != nil {
			minRunePoolDepth = mgr.GetConstants().GetInt64Value(constants.MinRunePoolDepth)
		}
		stagedPoolCost, err = mgr.Keeper().GetMimir(ctx, constants.StagedPoolCost.String())
		if stagedPoolCost < 0 || err != nil {
			stagedPoolCost = mgr.GetConstants().GetInt64Value(constants.StagedPoolCost)
		}
		if err = pm.cyclePools(ctx, maxAvailablePools, minRunePoolDepth, stagedPoolCost, mgr); err != nil {
			ctx.Logger().Error("Unable to enable a pool", "error", err)
		}
	}
	pm.cleanupPendingLiquidity(ctx, mgr)
	if err := pm.checkSaversUtilization(ctx, mgr); err != nil {
		ctx.Logger().Error("Unable to force withdraw saver position", "error", err)
	}

	return nil
}

// cyclePools update the set of Available and Staged pools
// Available non-gas pools not meeting the fee quota since last cycle, or not
// meeting availability requirements, are demoted to Staged.
// Staged pools are charged a fee and those with with zero rune depth and
// non-zero asset depth are removed along with their liquidity providers, and
// remaining assets are abandoned.
// The valid Staged pool with the highest rune depth is promoted to Available.
// If there are more than the maximum available pools, the Available pool with
// with the lowest rune depth is demoted to Staged
func (pm *PoolMgr) cyclePools(ctx cosmos.Context, maxAvailablePools, minRunePoolDepth, stagedPoolCost int64, mgr Manager) error {
	var availblePoolCount int64
	onDeck := NewPool()        // currently staged pool that could get promoted
	choppingBlock := NewPool() // currently available pool that is on the chopping block to being demoted
	var synthPoolsToRemove []common.Asset
	minRuneDepth := cosmos.NewUint(uint64(minRunePoolDepth))
	minPoolLiquidityFee := mgr.Keeper().GetConfigInt64(ctx, constants.MinimumPoolLiquidityFee)
	queuedSwapLiabilityAssets, err := pm.getQueuedSwapLiabilityAssets(ctx, mgr)
	if err != nil {
		return err
	}
	// quick func to check the validity of a pool
	validPool := func(pool Pool) bool {
		if pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() || pool.BalanceRune.LT(minRuneDepth) {
			return false
		}
		return true
	}

	// quick func to save a pool status and emit event
	setPool := func(pool Pool) error {
		poolEvt := NewEventPool(pool.Asset, pool.Status)
		if err := mgr.EventMgr().EmitEvent(ctx, poolEvt); err != nil {
			return fmt.Errorf("fail to emit pool event: %w", err)
		}

		switch pool.Status {
		case PoolAvailable:
			ctx.Logger().Info("New available pool", "pool", pool.Asset)
		case PoolStaged:
			ctx.Logger().Info("Pool demoted to staged status", "pool", pool.Asset)
		}
		// a demotion instantly blocks swaps and single-sided adds on this pool;
		// setPool is only ever called on a genuine transition
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "pool", "status_change"},
			float32(1),
			[]metrics.Label{
				telemetry.NewLabel("pool", pool.Asset.String()),
				telemetry.NewLabel("status", pool.Status.String()),
			},
		)
		pool.StatusSince = ctx.BlockHeight()
		return mgr.Keeper().SetPool(ctx, pool)
	}

	iterator := mgr.Keeper().GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return err
		}
		// skip all cycling on saver and derived pools
		if pool.Asset.IsSyntheticAsset() || pool.Asset.IsDerivedAsset() {
			continue
		}
		if pool.Asset.IsGasAsset() {
			if fee, err := mgr.Keeper().GetRollingPoolLiquidityFee(ctx, pool.Asset); err != nil {
				ctx.Logger().Error("fail to get rolling pool liquidity fee", "asset", pool.Asset, "error", err)
			} else {
				mgr.Keeper().SavePrevCyclePoolLiquidityFee(ctx, pool.Asset, fee)
			}
			mgr.Keeper().ResetRollingPoolLiquidityFee(ctx, pool.Asset)
			continue
		}
		switch pool.Status {
		case PoolAvailable:
			// any available pools that have no asset, no rune, or less than
			// min rune, moves back to staged status
			if validPool(pool) &&
				pm.poolMeetTradingVolumeCriteria(ctx, mgr, pool, cosmos.NewUint(uint64(minPoolLiquidityFee))) {
				availblePoolCount += 1
				if pool.BalanceRune.LT(choppingBlock.BalanceRune) || choppingBlock.IsEmpty() {
					choppingBlock = pool
				}
			} else {
				pool.Status = PoolStaged
				if err := setPool(pool); err != nil {
					return err
				}
			}
			if fee, feeErr := mgr.Keeper().GetRollingPoolLiquidityFee(ctx, pool.Asset); feeErr != nil {
				ctx.Logger().Error("fail to get rolling pool liquidity fee", "asset", pool.Asset, "error", feeErr)
			} else {
				mgr.Keeper().SavePrevCyclePoolLiquidityFee(ctx, pool.Asset, fee)
			}
			mgr.Keeper().ResetRollingPoolLiquidityFee(ctx, pool.Asset)
		case PoolStaged:
			// deduct staged pool rune fee
			fee := cosmos.NewUint(uint64(stagedPoolCost))
			if fee.GT(pool.BalanceRune) {
				fee = pool.BalanceRune
			}
			if !fee.IsZero() {
				updatedPool := pool
				updatedPool.BalanceRune = common.SafeSub(updatedPool.BalanceRune, fee)

				cacheCtx, commit := ctx.CacheContext()
				if err := mgr.Keeper().AddPoolFeeToReserve(cacheCtx, fee); err != nil {
					ctx.Logger().Error("fail to add rune to reserve", "from pool", pool.Asset, "err", err)
				} else if err := mgr.Keeper().SetPool(cacheCtx, updatedPool); err != nil {
					ctx.Logger().Error("fail to save pool", "pool", pool.Asset, "err", err)
				} else {
					commit()
					pool = updatedPool

					emitPoolBalanceChangedEvent(ctx,
						NewPoolMod(pool.Asset, fee, false, cosmos.ZeroUint(), false),
						"pool stage cost",
						mgr)
				}
			}
			// check if the rune balance is zero, and asset balance IS NOT
			// zero. This is because we don't want to abandon a pool that is in
			// the process of being created (race condition). We can safely
			// assume, if a pool has asset, but no rune, it should be
			// abandoned.
			if pool.BalanceRune.IsZero() && !pool.BalanceAsset.IsZero() {
				// Pending inbound liquidity still belongs to LPs and may be
				// committed later by cleanupPendingLiquidity. Do not burn a staged
				// pool until all pending liquidity has been resolved.
				if !pool.PendingInboundRune.IsZero() || !pool.PendingInboundAsset.IsZero() {
					ctx.Logger().Info(
						"skipping staged pool burn while pending liquidity remains",
						"pool", pool.Asset,
						"pending_rune", pool.PendingInboundRune,
						"pending_asset", pool.PendingInboundAsset,
					)
					continue
				}
				// Queued swaps can still need this pool asset either for a refund of
				// unprocessed input or for already-accounted output settlement.
				if _, ok := queuedSwapLiabilityAssets[pool.Asset.String()]; ok {
					ctx.Logger().Info(
						"skipping staged pool burn while queued swap liabilities remain",
						"pool", pool.Asset,
					)
					continue
				}
				// Scheduled but unsent txouts still rely on this asset balance in
				// vault accounting, so do not zero the asset out underneath them.
				if pm.hasPendingOutboundLiability(ctx, pool.Asset, mgr) {
					ctx.Logger().Info(
						"skipping staged pool burn while pending outbound liabilities remain",
						"pool", pool.Asset,
					)
					continue
				}
				// Related synth, saver, trade, or secured asset state can still have
				// claims on the same underlying L1 backing, so block the burn until
				// those liabilities are gone as well.
				relatedAssetLiabilityReason, err := pm.getRelatedAssetLiabilityReason(ctx, pool.Asset, mgr, queuedSwapLiabilityAssets)
				if err != nil {
					return err
				}
				if relatedAssetLiabilityReason != "" {
					ctx.Logger().Info(
						"skipping staged pool burn while related asset liabilities remain",
						"pool", pool.Asset,
						"reason", relatedAssetLiabilityReason,
					)
					continue
				}

				// the staged pool no longer has any rune, abandon the pool
				// and liquidity provider, and burn the asset (via zero'ing
				// the vaults for the asset, and churning away from the
				// tokens)
				ctx.Logger().Info("burning pool", "pool", pool.Asset)

				if err := pm.burnStagedPool(ctx, pool, mgr, queuedSwapLiabilityAssets); err != nil {
					ctx.Logger().Error("fail to burn staged pool, skipping pool burn", "from pool", pool.Asset, "err", err)
					// a burn that keeps failing leaves the pool wedged half-dead
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "pool", "burn_failed"},
						float32(1),
						[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
					)
					continue
				}
				// removes every LP position, the pool itself, and zeroes the asset
				// in every vault - the most destructive routine in the pool manager
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "pool", "burned"},
					float32(1),
					[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
				)
				synthPoolsToRemove = append(synthPoolsToRemove, pool.Asset.GetSyntheticAsset())
			} else if validPool(pool) && onDeck.BalanceRune.LT(pool.BalanceRune) {
				onDeck = pool
			}
		}
	}

	for _, synthAsset := range synthPoolsToRemove {
		mgr.Keeper().RemovePool(ctx, synthAsset)
	}

	if availblePoolCount >= maxAvailablePools {
		// if we've hit our max available pools, and the onDeck pool is less
		// than the chopping block pool, then we do make no changes, by
		// resetting the variables
		if onDeck.BalanceRune.LTE(choppingBlock.BalanceRune) {
			onDeck = NewPool()        // reset
			choppingBlock = NewPool() // reset
		}
	} else {
		// since we haven't hit the max number of available pools, there is no
		// available pool on the chopping block
		choppingBlock = NewPool() // reset
	}

	if !onDeck.IsEmpty() {
		onDeck.Status = PoolAvailable
		if err := setPool(onDeck); err != nil {
			return err
		}
	}

	if !choppingBlock.IsEmpty() {
		choppingBlock.Status = PoolStaged
		if err := setPool(choppingBlock); err != nil {
			return err
		}
	}

	return nil
}

// poolMeetTradingVolumeCriteria check if pool generated the minimum amount of fees since last cycle
func (pm *PoolMgr) poolMeetTradingVolumeCriteria(ctx cosmos.Context, mgr Manager, pool Pool, minPoolLiquidityFee cosmos.Uint) bool {
	if minPoolLiquidityFee.IsZero() {
		return true
	}
	blockPoolLiquidityFee, err := mgr.Keeper().GetRollingPoolLiquidityFee(ctx, pool.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get rolling pool liquidity from key value store", "error", err)
		// when we failed to get rolling liquidity fee from key value store for some reason, return true here
		// thus the pool will not be demoted
		return true
	}
	return cosmos.NewUint(blockPoolLiquidityFee).GTE(minPoolLiquidityFee)
}

// getQueuedSwapLiabilityAssets returns assets with balances still owed to
// queued swaps, either as remaining deposits to refund or as already-emitted
// swap output awaiting settlement.
func (pm *PoolMgr) getQueuedSwapLiabilityAssets(ctx cosmos.Context, mgr Manager) (map[string]struct{}, error) {
	liabilities := make(map[string]struct{})
	markLiability := func(asset common.Asset) {
		if !asset.IsEmpty() {
			liabilities[asset.String()] = struct{}{}
		}
	}

	swapIter := mgr.Keeper().GetSwapQueueIterator(ctx)
	defer swapIter.Close()
	for ; swapIter.Valid(); swapIter.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapIter.Value(), &msg); err != nil {
			return nil, fmt.Errorf("fail to unmarshal queued swap: %w", err)
		}
		if msg.Tx.Coins.IsEmpty() {
			continue
		}

		swp := msg.GetStreamingSwap()
		if msg.IsLegacyStreaming() && mgr.Keeper().StreamingSwapExists(ctx, msg.Tx.ID) {
			var err error
			swp, err = mgr.Keeper().GetStreamingSwap(ctx, msg.Tx.ID)
			if err != nil {
				return nil, fmt.Errorf("fail to get streaming swap %s: %w", msg.Tx.ID, err)
			}
		}

		if swp.Deposit.GT(swp.In) {
			markLiability(msg.Tx.Coins[0].Asset)
		}
		if !swp.Out.IsZero() {
			markLiability(msg.TargetAsset)
		}
	}

	advSwapIter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer advSwapIter.Close()
	for ; advSwapIter.Valid(); advSwapIter.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(advSwapIter.Value(), &msg); err != nil {
			return nil, fmt.Errorf("fail to unmarshal advanced queued swap: %w", err)
		}
		if msg.Tx.Coins.IsEmpty() || msg.State == nil {
			continue
		}

		if msg.State.Deposit.GT(msg.State.In) {
			markLiability(msg.Tx.Coins[0].Asset)
		}
		if !msg.State.Out.IsZero() {
			markLiability(msg.TargetAsset)
		}
	}

	return liabilities, nil
}

// hasPendingOutboundLiability returns true when the asset already has unsent
// txouts assigned to vaults. Burning the pool would otherwise zero balances
// still owed by those outbounds.
func (pm *PoolMgr) hasPendingOutboundLiability(ctx cosmos.Context, asset common.Asset, mgr Manager) bool {
	return len(mgr.Keeper().GetPendingOutbounds(ctx, asset)) > 0
}

// hasLiquidityProviders returns true when any LP or saver position state still
// exists for the given asset.
func (pm *PoolMgr) hasLiquidityProviders(ctx cosmos.Context, asset common.Asset, mgr Manager) bool {
	iterator := mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	return iterator.Valid()
}

// getRelatedAssetLiabilityReason returns the first reason an L1 pool still
// has outstanding liabilities in related asset systems that depend on the
// same underlying asset backing. Burning the L1 pool must stay blocked while
// any of these related claims remain.
func (pm *PoolMgr) getRelatedAssetLiabilityReason(ctx cosmos.Context, asset common.Asset, mgr Manager, queuedSwapLiabilityAssets map[string]struct{}) (string, error) {
	// THOR-native assets do not have L1-backed synth/trade/secured related assets.
	if asset.IsNative() {
		return "", nil
	}

	// Synths and savers are direct related pools whose supply and LP state are
	// backed by the same L1 pool asset.
	synthAsset := asset.GetSyntheticAsset()
	if _, ok := queuedSwapLiabilityAssets[synthAsset.String()]; ok {
		return "queued synth swap liabilities remain", nil
	}
	if pm.hasPendingOutboundLiability(ctx, synthAsset, mgr) {
		return "pending synth outbound liabilities remain", nil
	}
	if !mgr.Keeper().GetTotalSupply(ctx, synthAsset).IsZero() {
		return "synth supply remains", nil
	}
	if pm.hasLiquidityProviders(ctx, synthAsset, mgr) {
		return "saver lp state remains", nil
	}

	synthPool, err := mgr.Keeper().GetPool(ctx, synthAsset)
	if err != nil {
		return "", fmt.Errorf("fail to get related synth pool %s: %w", synthAsset, err)
	}
	if !synthPool.IsEmpty() &&
		(!synthPool.BalanceRune.IsZero() ||
			!synthPool.BalanceAsset.IsZero() ||
			!synthPool.LPUnits.IsZero() ||
			!synthPool.PendingInboundRune.IsZero() ||
			!synthPool.PendingInboundAsset.IsZero()) {
		return "synthetic pool state remains", nil
	}

	// Trade accounts are backed by the underlying L1 asset depth recorded in the
	// trade unit, so pool burning must wait until that depth and its units are gone.
	tradeAsset := asset.GetTradeAsset()
	if _, ok := queuedSwapLiabilityAssets[tradeAsset.String()]; ok {
		return "queued trade asset liabilities remain", nil
	}
	if pm.hasPendingOutboundLiability(ctx, tradeAsset, mgr) {
		return "pending trade outbound liabilities remain", nil
	}
	tradeUnit, err := mgr.Keeper().GetTradeUnit(ctx, tradeAsset)
	if err != nil {
		return "", fmt.Errorf("fail to get related trade unit %s: %w", tradeAsset, err)
	}
	if !tradeUnit.Depth.IsZero() || !tradeUnit.Units.IsZero() {
		return "trade asset depth remains", nil
	}

	// Secured assets also represent claims on the same underlying L1 asset, so
	// both the recorded depth and the minted share supply must be zero first.
	securedAsset := asset.GetSecuredAsset()
	if _, ok := queuedSwapLiabilityAssets[securedAsset.String()]; ok {
		return "queued secured asset liabilities remain", nil
	}
	if pm.hasPendingOutboundLiability(ctx, securedAsset, mgr) {
		return "pending secured outbound liabilities remain", nil
	}
	securedState, err := mgr.Keeper().GetSecuredAsset(ctx, securedAsset)
	if err != nil {
		return "", fmt.Errorf("fail to get related secured asset %s: %w", securedAsset, err)
	}
	if !securedState.Depth.IsZero() || !mgr.Keeper().GetTotalSupply(ctx, securedAsset).IsZero() {
		return "secured asset depth remains", nil
	}

	return "", nil
}

// burnStagedPool atomically removes a staged pool and its backing funds.
func (pm *PoolMgr) burnStagedPool(ctx cosmos.Context, pool Pool, mgr Manager, queuedSwapLiabilityAssets map[string]struct{}) error {
	if !pool.PendingInboundRune.IsZero() || !pool.PendingInboundAsset.IsZero() {
		return fmt.Errorf("cannot burn staged pool %s with pending inbound liquidity", pool.Asset)
	}
	if _, ok := queuedSwapLiabilityAssets[pool.Asset.String()]; ok {
		return fmt.Errorf("cannot burn staged pool %s with queued swap liabilities", pool.Asset)
	}
	if pm.hasPendingOutboundLiability(ctx, pool.Asset, mgr) {
		return fmt.Errorf("cannot burn staged pool %s with pending outbound liabilities", pool.Asset)
	}
	relatedAssetLiabilityReason, err := pm.getRelatedAssetLiabilityReason(ctx, pool.Asset, mgr, queuedSwapLiabilityAssets)
	if err != nil {
		return err
	}
	if relatedAssetLiabilityReason != "" {
		return fmt.Errorf("cannot burn staged pool %s while related asset liabilities remain: %s", pool.Asset, relatedAssetLiabilityReason)
	}

	cacheCtx, commit := ctx.CacheContext()

	if pool.Asset.IsNative() {
		// Native pool assets live in module balances rather than vaults, so
		// move them first and rely on the cached context to discard the move if
		// any later step in pool burning fails.
		if err := pm.removeAssetFromVault(cacheCtx, pool.Asset, mgr); err != nil {
			return fmt.Errorf("fail to remove native asset from asgard during pool burning: %w", err)
		}
	}

	if err := pm.removeLiquidityProviders(cacheCtx, pool.Asset, mgr); err != nil {
		return fmt.Errorf("fail to remove liquidity providers during pool burning: %w", err)
	}

	mgr.Keeper().RemovePool(cacheCtx, pool.Asset)

	poolEvent := NewEventPool(pool.Asset, PoolSuspended)
	if err := mgr.EventMgr().EmitEvent(cacheCtx, poolEvent); err != nil {
		return fmt.Errorf("fail to emit pool event: %w", err)
	}

	if !pool.Asset.IsNative() {
		if err := pm.removeAssetFromVault(cacheCtx, pool.Asset, mgr); err != nil {
			return fmt.Errorf("fail to remove asset from vault during pool burning: %w", err)
		}
	}

	commit()
	return nil
}

// removeAssetFromVault moves native assets out of Asgard or zeroes asset
// balances in all vaults that still hold the pool asset.
func (pm *PoolMgr) removeAssetFromVault(ctx cosmos.Context, asset common.Asset, mgr Manager) error {
	if asset.IsNative() {
		asgardAccAddress := mgr.Keeper().GetModuleAccAddress(AsgardName)
		if asgardAccAddress.Empty() {
			return fmt.Errorf("fail to get asgard acc address")
		}

		nativeBalance := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, asset.Native())
		if nativeBalance.IsZero() {
			return fmt.Errorf("native asset %s missing from asgard during pool burn", asset)
		}

		// Native pool assets live in module balances rather than vault,
		// so preserve whatever balance remains by moving it to Treasury.
		if err := mgr.Keeper().SendFromModuleToModule(
			ctx,
			AsgardName,
			TreasuryName,
			common.Coins{{
				Asset:  asset,
				Amount: nativeBalance,
			}},
		); err != nil {
			return fmt.Errorf("fail to send native pool asset to treasury: %w", err)
		}

		return nil
	}

	// zero vaults with the pool asset
	vaultIter := mgr.Keeper().GetVaultIterator(ctx)
	defer vaultIter.Close()
	for ; vaultIter.Valid(); vaultIter.Next() {
		var vault Vault
		if err := mgr.Keeper().Cdc().Unmarshal(vaultIter.Value(), &vault); err != nil {
			return fmt.Errorf("fail to unmarshal vault: %w", err)
		}
		if vault.HasAsset(asset) {
			for i, coin := range vault.Coins {
				if asset.Equals(coin.Asset) {
					vault.Coins[i].Amount = cosmos.ZeroUint()
					if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
						return fmt.Errorf("fail to save vault: %w", err)
					}
					break
				}
			}
		}
	}
	return nil
}

// removeLiquidityProviders remove all lps for the given asset pool
func (pm *PoolMgr) removeLiquidityProviders(ctx cosmos.Context, asset common.Asset, mgr Manager) error {
	iterator := mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		if err := mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &lp); err != nil {
			return fmt.Errorf("fail to unmarshal liquidity provider: %w", err)
		}

		// fields must be populated with empty values or midgard will not process
		withdrawEvt := NewEventWithdraw(
			asset,
			lp.Units,
			int64(10000),
			cosmos.ZeroDec(),
			common.Tx{
				ID:          common.BlankTxID,
				FromAddress: lp.GetAddress(),
				ToAddress:   common.NoAddress,
				Coins:       common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())),
				Chain:       common.THORChain,
			},
			cosmos.ZeroUint(),
			cosmos.ZeroUint(),
		)
		if err := mgr.EventMgr().EmitEvent(ctx, withdrawEvt); err != nil {
			return fmt.Errorf("fail to emit pool withdraw event: %w", err)
		}

		mgr.Keeper().RemoveLiquidityProvider(ctx, lp)
	}
	return nil
}

func (pm *PoolMgr) cleanupPendingLiquidity(ctx cosmos.Context, mgr Manager) {
	pendingLiquidityAgeLimit := mgr.Keeper().GetConfigInt64(ctx, constants.PendingLiquidityAgeLimit)
	if pendingLiquidityAgeLimit <= 0 {
		return
	}

	var pools Pools
	iterator := mgr.Keeper().GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		err := mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool)
		if err != nil {
			ctx.Logger().Error("fail to unmarshal pool for cleanup pending liquidity", "error", err)
			continue
		}
		if pool.Asset.IsDerivedAsset() || pool.Asset.IsSyntheticAsset() {
			continue
		}
		if !pool.IsAvailable() && !pool.IsStaged() {
			continue
		}
		// no need to commit pending liquidity when there is none, quick exit
		if pool.PendingInboundRune.IsZero() && pool.PendingInboundAsset.IsZero() {
			continue
		}
		if mgr.Keeper().IsChainHalted(ctx, pool.Asset.GetChain()) || mgr.Keeper().IsLPPaused(ctx, pool.Asset.GetChain()) {
			continue
		}
		pools = append(pools, pool)
	}

	if len(pools) == 0 {
		return
	}

	// process each pool within ageLimit evenly (in terms of blocks between
	// each pool). For example, if ageLimit is 100 blocks, and we have 5 pools,
	// we want to clean a pool every ~20 blocks, but each pool is only cleaned
	// once every 100 blocks (just a different 100 blocks)
	separator := pendingLiquidityAgeLimit / int64(len(pools))
	if separator == 0 {
		// If PendingLiquidityAgeLimit is smaller than the number of pools,
		// still spread them out over the available blocks (and wrap around).
		separator = 1
	}
	cleanupTarget := ctx.BlockHeight() % pendingLiquidityAgeLimit
	var due Pools
	for i, pool := range pools {
		if cleanupTarget == (separator*int64(i))%pendingLiquidityAgeLimit {
			due = append(due, pool)
		}
	}
	if len(due) == 0 {
		return
	}

	// The TVL cap check reads every asgard vault, a pool per vault coin and
	// every active validator. Most blocks clean no pool, so it runs only once
	// one is due, still before anything is committed.
	if atTVLCap(ctx, nil, mgr) {
		ctx.Logger().Info("cleaning pending liquidity skipped due to TVL cap")
		return
	}
	for _, pool := range due {
		if err := pm.commitPendingLiquidity(ctx, pool, mgr); err != nil {
			ctx.Logger().Error("fail to clean pending liquidity", "pool", pool.Asset, "error", err)
		}
	}
}

func (pm *PoolMgr) checkSaversUtilization(ctx cosmos.Context, mgr Manager) error {
	if mgr.Keeper().IsGlobalTradingHalted(ctx) {
		return nil
	}
	saversFreq := mgr.Keeper().GetConfigInt64(ctx, constants.SaversEjectInterval)
	if saversFreq <= 0 || ctx.BlockHeight()%saversFreq != 0 {
		return nil
	}

	nodeAccounts, err := mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return err
	}

	if len(nodeAccounts) == 0 {
		return fmt.Errorf("dev err: no active node accounts")
	}

	var (
		synthPool           Pool
		maxUtilizationRatio = sdkmath.ZeroUint()
		handler             = newLuviProtocolHandler(mgr)
		signer              = nodeAccounts[0].NodeAddress
		synthCap            = mgr.Keeper().GetConfigInt64(ctx, constants.MaxSynthPerPoolDepth)
	)
	iterator := mgr.Keeper().GetPoolIterator(ctx)
	defer iterator.Close()

	// Find the pool with the highest utilization ratio above the cap
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		err = mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool)
		if err != nil {
			ctx.Logger().Error("fail to unmarshal pool for synth utilization check", "error", err)
			continue
		}

		// skip non synth asset pool
		if !pool.Asset.IsSyntheticAsset() {
			continue
		}

		if !pool.IsAvailable() && !pool.IsStaged() {
			continue
		}

		l1Chain := pool.Asset.GetLayer1Asset().GetChain()
		if mgr.Keeper().IsChainTradingHalted(ctx, l1Chain) || mgr.Keeper().IsLPPaused(ctx, l1Chain) {
			continue
		}

		if mgr.Keeper().IsRagnarok(ctx, []common.Asset{pool.Asset.GetLayer1Asset()}) {
			continue
		}

		synthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		var l1Pool Pool
		l1Pool, err = mgr.Keeper().GetPool(ctx, pool.Asset.GetLayer1Asset())
		if err != nil {
			continue
		}

		maxSynth := common.GetUncappedShare(cosmos.NewUint(uint64(synthCap)), cosmos.NewUint(constants.MaxBasisPts), l1Pool.BalanceAsset.MulUint64(2))
		if synthSupply.GT(maxSynth) {
			utilizationRatio := common.GetUncappedShare(synthSupply, maxSynth, sdkmath.NewUint(constants.MaxBasisPts))
			if synthPool.IsEmpty() || utilizationRatio.GT(maxUtilizationRatio) {
				synthPool = pool
				maxUtilizationRatio = utilizationRatio
			}
		}
	}
	if synthPool.IsEmpty() {
		// No pool found with synth utilization above the cap
		return nil
	}

	lpIterator := mgr.Keeper().GetLiquidityProviderIterator(ctx, synthPool.Asset)
	defer lpIterator.Close()
	var latestLp LiquidityProvider
	for ; lpIterator.Valid(); lpIterator.Next() {
		var lp LiquidityProvider
		if err = mgr.Keeper().Cdc().Unmarshal(lpIterator.Value(), &lp); err != nil {
			ctx.Logger().Error("fail to unmarshal liquidity provider", "error", err)
			continue
		}
		// get the last added lp
		if lp.LastAddHeight > latestLp.LastAddHeight {
			latestLp = lp
		} else if lp.LastAddHeight == latestLp.LastAddHeight {
			// withdraw smaller position
			if lp.Units.LT(latestLp.Units) {
				latestLp = lp
			}
		}
	}

	// sanity check
	if latestLp.LastAddHeight == 0 || latestLp.AssetAddress.IsEmpty() {
		ctx.Logger().Error("saver utilization exceeded without any LP position", "asset", synthPool.Asset)
		return nil
	}

	coins := common.NewCoins(common.NewCoin(synthPool.Asset.GetLayer1Asset(), cosmos.ZeroUint())) // used in tx hash
	tx := common.NewTx(
		common.BlankTxID, latestLp.AssetAddress, latestLp.AssetAddress, coins, nil, "THOR-SAVER-EJECT",
	)
	tx.Chain = synthPool.Asset.GetChain()
	tx.ID, err = common.NewTxID(tx.Hash(ctx.BlockHeight()))
	if err != nil {
		ctx.Logger().Error("fail to create tx id", "error", err, "tx", tx)
		return nil
	}

	withdrawMsg := NewMsgWithdrawLiquidity(
		tx,
		latestLp.AssetAddress,
		cosmos.NewUint(constants.MaxBasisPts),
		synthPool.Asset.GetSyntheticAsset(),
		common.EmptyAsset,
		signer,
	)
	ctx.Logger().Info("closing saver LP position", "tx_id", tx.ID, "pool", synthPool.Asset, "units", latestLp.Units, "asset_address", latestLp.AssetAddress)
	_, err = handler(ctx, withdrawMsg)
	ejectLabels := []metrics.Label{telemetry.NewLabel("pool", synthPool.Asset.String())}
	if err != nil {
		ctx.Logger().Error("fail to eject saver position", "asset", synthPool.Asset, "address", latestLp.AssetAddress, "error", err)
		// the network decided it must eject but could not: the cap stays breached
		telemetry.IncrCounterWithLabels([]string{"thornode", "saver", "eject_failed"}, float32(1), ejectLabels)
	} else {
		// involuntary position closure
		telemetry.IncrCounterWithLabels([]string{"thornode", "saver", "ejected"}, float32(1), ejectLabels)
		telemetry.IncrCounterWithLabels([]string{"thornode", "saver", "ejected_units"}, telem(latestLp.Units), ejectLabels)
	}

	return nil
}

// commitPendingLiquidity - for aged pending liquidity, commit it to the pool
func (pm *PoolMgr) commitPendingLiquidity(ctx cosmos.Context, pool Pool, mgr Manager) error {
	ctx.Logger().Info("cleaning pending liquidity in pool", "pool", pool.Asset)
	// track stats
	var count int
	cleanedRune := cosmos.ZeroUint()
	cleanedAsset := cosmos.ZeroUint()
	var failCount int
	failedRune := cosmos.ZeroUint()
	failedAsset := cosmos.ZeroUint()

	// no need to commit pending liquidity when there is none, quick exit
	if pool.PendingInboundRune.IsZero() && pool.PendingInboundAsset.IsZero() {
		return nil
	}

	// the add liquidity handler rejects adds into a pool in synth overhang:
	// leave the liquidity pending (and withdrawable) until the pool leaves it
	if isLUVIOverhangGuarded(ctx, mgr.Keeper(), pool, mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())) {
		ctx.Logger().Info("skip committing pending liquidity, pool is in synth overhang", "pool", pool.Asset)
		return nil
	}

	// get a signer of the txn
	nodeAccounts, err := mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return err
	}
	if len(nodeAccounts) == 0 {
		return fmt.Errorf("dev err: no active node accounts")
	}
	signer := nodeAccounts[0].NodeAddress

	handler := newLuviProtocolHandler(mgr)
	pendingLiquidityAgeLimit := mgr.Keeper().GetConfigInt64(ctx, constants.PendingLiquidityAgeLimit)

	iterator := mgr.Keeper().GetLiquidityProviderIterator(ctx, pool.Asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		if err := mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &lp); err != nil {
			ctx.Logger().Error("fail to unmarshal liquidity provider", "error", err)
			continue
		}

		// check if this LP has any pending liquidity, quick exit if it doesn't
		if lp.PendingRune.IsZero() && lp.PendingAsset.IsZero() {
			continue
		}

		if lp.AssetAddress.IsEmpty() || lp.RuneAddress.IsEmpty() {
			continue
		}

		// check if last add height is within our pendingLiquidityAgeLimit
		if ctx.BlockHeight()-lp.LastAddHeight <= pendingLiquidityAgeLimit {
			continue
		}

		runeAdd := cosmos.ZeroUint()
		assetAdd := cosmos.ZeroUint()
		tx := common.Tx{
			ID:        common.BlankTxID,
			ToAddress: common.NoAddress,
			Memo:      "THOR-PENDING-LIQUIDITY-ADD",
		}

		if !lp.PendingRune.IsZero() {
			tx.FromAddress = lp.AssetAddress
			tx.Chain = pool.Asset.GetChain()
			tx.Coins = common.NewCoins(common.NewCoin(pool.Asset, assetAdd))
		}
		if !lp.PendingAsset.IsZero() {
			tx.FromAddress = lp.RuneAddress
			tx.Chain = common.THORChain
			tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), runeAdd))
		}

		msg := NewMsgAddLiquidity(tx, lp.Asset, runeAdd, assetAdd, lp.RuneAddress, lp.AssetAddress, common.NoAddress, cosmos.ZeroUint(), signer)
		_, err := handler(ctx, msg)
		if err != nil {
			ctx.Logger().Error("failed to commit pending liquidity", "asset", lp.Asset, "thor address", lp.RuneAddress, "asset address", lp.AssetAddress, "pending rune", lp.PendingRune, "pending asset", lp.PendingAsset, "error", err)
			// since we failed to clear pending liquidity, lets add it to the
			// running total for the pool
			failCount += 1
			failedRune = failedRune.Add(lp.PendingRune)
			failedAsset = failedAsset.Add(lp.PendingAsset)
			continue
		}

		count += 1
		cleanedRune = cleanedRune.Add(lp.PendingRune)
		cleanedAsset = cleanedAsset.Add(lp.PendingAsset)
	}
	ctx.Logger().Info("cleaned pending liquidity", "pool", pool.Asset, "count", count, "cleared rune", cleanedRune.String(), "cleared asset", cleanedAsset.String())

	// add telemetry
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "pool", "pending", "clear", "count"},
		float32(count),
		[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
	)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "pool", "pending", "clear", "rune"},
		telem(cleanedRune),
		[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
	)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "pool", "pending", "clear", "asset"},
		telem(cleanedAsset),
		[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
	)
	// the counters above record successes only, so an all-failing pool is
	// otherwise indistinguishable from a pool with nothing pending
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "pool", "pending", "clear", "failed"},
		float32(failCount),
		[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
	)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "pool", "pending", "clear", "failed_rune"},
		telem(failedRune),
		[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
	)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "pool", "pending", "clear", "failed_asset"},
		telem(failedAsset),
		[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
	)

	return nil
}
