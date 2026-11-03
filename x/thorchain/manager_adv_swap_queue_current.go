package thorchain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/cosmos/gogoproto/proto"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// SwapDirection represents the direction of a swap through a pool.
type SwapDirection int

const (
	RuneToAsset SwapDirection = iota
	AssetToRune
)

// poolSwapDir tracks the direction of the last swap through a pool.
type poolSwapDir map[string]SwapDirection

// swapDirEntry represents a pool and the direction a swap goes through it.
type swapDirEntry struct {
	pool      string
	direction SwapDirection
}

// getSwapDirections returns the pool asset and direction for each pool leg of a swap.
// Note: RUNE->RUNE swaps are invalid and rejected by MsgSwap.ValidateBasic,
// so the default case always represents a genuine double swap (asset->asset).
func getSwapDirections(msg MsgSwap) []swapDirEntry {
	source := msg.Tx.Coins[0].Asset.GetLayer1Asset()
	target := msg.TargetAsset.GetLayer1Asset()
	switch {
	case source.IsRune():
		// RUNE -> Asset: single swap, rune-to-asset
		return []swapDirEntry{
			{target.String(), RuneToAsset},
		}
	case target.IsRune():
		// Asset -> RUNE: single swap, asset-to-rune
		return []swapDirEntry{
			{source.String(), AssetToRune},
		}
	default:
		// Double swap: source pool (asset-to-rune) + target pool (rune-to-asset)
		return []swapDirEntry{
			{source.String(), AssetToRune},
			{target.String(), RuneToAsset},
		}
	}
}

// shouldSkipRapidSwapDirection returns true if a market swap should be skipped
// because it would go in the same direction as the last swap through any pool
// it touches. This prevents draining pools without counter-liquidity during
// rapid swap iterations.
//
// For double swaps (e.g. ETH→BTC), the swap is skipped if ANY pool leg matches
// the last recorded direction for that pool. This is a conservative policy that
// errs on the side of caution: even if only one leg conflicts, the swap is
// deferred to allow counter-directional liquidity to flow first.
func shouldSkipRapidSwapDirection(msg MsgSwap, lastPoolDir poolSwapDir, iteration int64) bool {
	if iteration == 0 || !msg.IsMarketSwap() {
		return false
	}
	for _, d := range getSwapDirections(msg) {
		if lastDir, exists := lastPoolDir[d.pool]; exists && lastDir == d.direction {
			return true
		}
	}
	return false
}

// SwapQueueAdv is going to manage the swaps queue
type SwapQueueAdv struct {
	k keeper.Keeper
}

// newSwapQueueAdv create a new vault manager
func newSwapQueueAdv(k keeper.Keeper) *SwapQueueAdv {
	return &SwapQueueAdv{k: k}
}

// FetchQueue - grabs all swap queue items from the kvstore and returns them
func (vm *SwapQueueAdv) FetchQueue(ctx cosmos.Context, mgr Manager, pairs tradePairs, pools Pools, todo tradePairs) (swapItems, error) { // nolint
	return vm.fetchQueue(ctx, mgr, pairs, pools, todo, nil)
}

func (vm *SwapQueueAdv) fetchQueue(ctx cosmos.Context, mgr Manager, pairs tradePairs, pools Pools, todo tradePairs, activeLimitPairs tradePairSet) (swapItems, error) { // nolint
	// if the network is doing a pool cycle, no swaps are executed this
	// block. This is because the change of active pools can cause the
	// mechanism to index/encode the selected pools/trading pairs that need to
	// be checked.
	poolCycle := mgr.Keeper().GetConfigInt64(ctx, constants.PoolCycle)
	if poolCycle > 0 && ctx.BlockHeight()%poolCycle == 0 {
		return nil, nil
	}

	// If todo is empty, set it to every pair
	if len(todo) == 0 {
		todo = pairs
	}

	// get market swap
	marketItems, err := vm.k.GetAdvSwapQueueIndex(ctx, MsgSwap{SwapType: MarketSwap})
	if err != nil {
		return nil, err
	}
	items := make(swapItems, 0, len(marketItems))
	for _, item := range marketItems {
		msg, err := vm.k.GetAdvSwapQueueItem(ctx, item.TxID, item.Index)
		if err != nil {
			ctx.Logger().Error("fail to fetch adv swap item, removing orphan index entry", "tx_id", item.TxID, "index", item.Index, "error", err)
			if rmErr := vm.k.RemoveMarketAdvSwapQueueIndexEntry(ctx, item.TxID, item.Index); rmErr != nil {
				ctx.Logger().Error("fail to remove orphan adv swap index entry", "tx_id", item.TxID, "index", item.Index, "error", rmErr)
			}
			continue
		}

		if !vm.isSwapReady(ctx, msg) {
			continue
		}

		items = append(items, swapItem{
			msg:   msg,
			index: item.Index,
			fee:   cosmos.ZeroUint(),
		})
	}

	for _, pair := range todo {
		if activeLimitPairs != nil {
			if _, ok := activeLimitPairs[pair]; !ok {
				continue
			}
		}
		newItems := vm.discoverLimitSwaps(ctx, mgr, pair, pools)
		items = append(items, newItems...)
	}

	return items, nil
}

// getActiveLimitPairs returns the populated limit pairs when the number of
// price buckets is cheaper to scan than probing every possible pool pair. If
// the scan crosses maxBuckets it returns nil, causing fetchQueue to retain its
// bounded O(P^2) pair-probe path instead of becoming O(queue size).
func (vm *SwapQueueAdv) getActiveLimitPairs(ctx cosmos.Context, maxBuckets int) tradePairSet {
	result := make(tradePairSet)
	iter := vm.k.GetAdvSwapQueueLimitIndexIterator(ctx)
	if iter == nil {
		return result
	}
	defer iter.Close()
	buckets := 0
	for ; iter.Valid(); iter.Next() {
		buckets++
		if buckets > maxBuckets {
			return nil
		}
		// Keys are aqlim//SOURCE>TARGET/RATIO/. Work from the end so a
		// future prefix spelling change does not affect pair extraction.
		parts := strings.Split(string(iter.Key()), "/")
		if len(parts) < 4 {
			continue
		}
		pairParts := strings.SplitN(parts[len(parts)-3], ">", 2)
		if len(pairParts) != 2 {
			continue
		}
		source, err := common.NewAsset(pairParts[0])
		if err != nil {
			continue
		}
		target, err := common.NewAsset(pairParts[1])
		if err != nil {
			continue
		}
		result[genTradePair(source, target)] = struct{}{}
	}
	return result
}

func (vm *SwapQueueAdv) isSwapReady(ctx cosmos.Context, msg MsgSwap) bool {
	// skip processing limit swaps if EnableAdvSwapQueue is set to market only mode
	val := vm.k.GetConfigInt64(ctx, constants.EnableAdvSwapQueue)
	if types.AdvSwapQueueMode(val) == types.AdvSwapQueueModeMarketOnly && msg.IsLimitSwap() {
		return false
	}

	pausedStreaming := vm.k.GetConfigInt64(ctx, constants.StreamingSwapPause)
	if pausedStreaming > 0 && msg.IsStreaming() {
		return false
	}

	// Check if it's the right interval for the next sub-swap
	if msg.IsMarketSwap() && msg.State.Interval > 0 && (ctx.BlockHeight()-msg.State.LastHeight)%int64(msg.State.Interval) != 0 {
		return false
	}

	// if interval is 0, then allow a market swap to execute multiple times in the same block
	// if interval is 1 or greater, then only swap one time per block
	if msg.State.Interval > 0 && msg.State.LastHeight >= ctx.BlockHeight() {
		return false // skip
	}

	if vm.k.IsTradingHalt(ctx, &msg) {
		// if trading/chain is halted, skip
		return false // skip
	}

	// If either source or target in ragnarok, streaming is not allowed. Only skip
	// streaming swaps here: a non-streaming (market) swap must still be processed
	// during ragnarok, otherwise the synth->L1 swap that redeems a saver while its
	// pool is being ragnaroked would be skipped forever - stranding the payout
	// while the saver claim is removed. Non-synth market swaps against a ragnarok
	// pool remain blocked by the pool-availability check in swapOne, so this only
	// lets synth redemptions through. Mirrors handler_swap.go, which likewise only
	// blocks streaming swaps on ragnarok assets.
	//
	// Use the canonical IsRagnarok predicate on the layer1 assets: it also treats
	// a chain's gas asset ragnarok as covering every token pool on that chain,
	// which is exactly how checkPoolRagnarok picks the pools to ragnarok. A
	// direct RAGNAROK-<asset> lookup would miss those token pools.
	if msg.IsStreaming() {
		assets := []common.Asset{
			msg.Tx.Coins[0].Asset.GetLayer1Asset(),
			msg.TargetAsset.GetLayer1Asset(),
		}
		if vm.k.IsRagnarok(ctx, assets) {
			return false
		}
	}

	return true
}

func (vm *SwapQueueAdv) discoverLimitSwaps(ctx cosmos.Context, mgr Manager, pair tradePair, pools Pools) swapItems {
	items := make(swapItems, 0)

	iter := vm.k.GetAdvSwapQueueIndexIterator(ctx, LimitSwap, pair.source, pair.target)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		ratio, err := vm.parseRatioFromKey(string(iter.Key()))
		if err != nil {
			ctx.Logger().Error("fail to parse ratio", "key", string(iter.Key()), "error", err)
			continue
		}

		// Check if fee-less swap meets the ratio requirement
		if !vm.checkFeelessSwap(pools, pair, ratio) {
			break
		}

		record := make([]string, 0)
		value := ProtoStrings{Value: record}
		if err := vm.k.Cdc().Unmarshal(iter.Value(), &value); err != nil {
			ctx.Logger().Error("fail to fetch indexed txn hashes", "error", err)
			continue
		}

		for _, rec := range value.Value {
			// Parse format "txID-index" using last hyphen to handle Cosmos indexed TxIDs
			lastHyphenIndex := strings.LastIndex(rec, "-")
			if lastHyphenIndex == -1 {
				ctx.Logger().Error("invalid swap queue index format - no hyphen found", "record", rec)
				continue
			}
			parts := []string{rec[:lastHyphenIndex], rec[lastHyphenIndex+1:]}
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				ctx.Logger().Error("invalid swap queue index format", "record", rec)
				continue
			}

			hash, err := common.NewTxID(parts[0])
			if err != nil {
				ctx.Logger().Error("fail to parse tx hash", "error", err)
				continue
			}

			index, err := strconv.Atoi(parts[1])
			if err != nil {
				ctx.Logger().Error("fail to parse index", "error", err)
				continue
			}

			msg, err := vm.k.GetAdvSwapQueueItem(ctx, hash, index)
			if err != nil {
				ctx.Logger().Error("fail to fetch msg swap, removing orphan limit index entry", "tx_id", hash, "index", index, "error", err)
				if rmErr := vm.k.RemoveLimitAdvSwapQueueIndexEntry(ctx, iter.Key(), hash, index); rmErr != nil {
					ctx.Logger().Error("fail to remove orphan limit adv swap index entry", "tx_id", hash, "index", index, "error", rmErr)
				}
				continue
			}

			if !vm.isSwapReady(ctx, msg) {
				continue
			}

			// Check if our swap is already completed, ie a limit swap has expired
			if vm.IsDone(ctx, msg) {
				if err := settleSwap(ctx, mgr, msg, "swap has been completed."); err != nil {
					ctx.Logger().Error("fail to handle completed streaming limit swap", "error", err)
				}
				continue
			}

			// do a swap, including swap fees and outbound fees. If this passes attempt the swap.
			if ok := vm.checkWithFeeSwap(ctx, mgr, pools, msg); !ok {
				continue
			}

			items = append(items, swapItem{
				msg:   msg,
				index: index,
				fee:   cosmos.ZeroUint(),
			})
		}
	}
	return items
}

func (vm *SwapQueueAdv) IsDone(ctx cosmos.Context, msg MsgSwap) bool {
	if msg.IsDone() {
		return true
	}

	if msg.IsLimitSwap() {
		ttl := vm.getTTL(ctx, msg)
		if ctx.BlockHeight()-msg.InitialBlockHeight >= ttl {
			return true
		}
	}

	return false
}

// getTTL calculates the custom TTL for a limit swap message
// Uses State.Interval if specified and valid, otherwise falls back to maxAge
func (vm *SwapQueueAdv) getTTL(ctx cosmos.Context, msg MsgSwap) int64 {
	maxAge := vm.k.GetConfigInt64(ctx, constants.StreamingLimitSwapMaxAge)
	return getLimitSwapTTL(msg, maxAge)
}

func (vm *SwapQueueAdv) checkFeelessSwap(pools Pools, pair tradePair, indexRatio uint64) bool {
	var ratio cosmos.Uint
	switch {
	case !pair.HasRune():
		sourcePool, ok := pools.Get(pair.source.GetLayer1Asset())
		if !ok {
			return false
		}
		targetPool, ok := pools.Get(pair.target.GetLayer1Asset())
		if !ok {
			return false
		}
		one := cosmos.NewUint(common.One)
		runeAmt := common.GetSafeShare(one, sourcePool.BalanceAsset, sourcePool.BalanceRune)
		emit := common.GetSafeShare(runeAmt, targetPool.BalanceRune, targetPool.BalanceAsset)
		ratio = vm.getRatio(one, emit)
	case pair.source.IsRune():
		pool, ok := pools.Get(pair.target.GetLayer1Asset())
		if !ok {
			return false
		}
		ratio = vm.getRatio(pool.BalanceRune, pool.BalanceAsset)
	case pair.target.IsRune():
		pool, ok := pools.Get(pair.source.GetLayer1Asset())
		if !ok {
			return false
		}
		ratio = vm.getRatio(pool.BalanceAsset, pool.BalanceRune)
	}
	return cosmos.NewUint(indexRatio).GT(ratio)
}

func (vm *SwapQueueAdv) checkWithFeeSwap(ctx cosmos.Context, mgr Manager, pools Pools, msg MsgSwap) bool {
	swapper, err := GetSwapper(vm.k.GetVersion())
	if err != nil {
		panic(err)
	}

	// Get the next swap size and proportional target for streaming swaps
	swapSize, proportionalTarget := msg.NextSize()

	// account for affiliate fee
	source := common.NewCoin(msg.Tx.Coins[0].Asset, swapSize)
	if !msg.AffiliateBasisPoints.IsZero() {
		maxBasisPoints := cosmos.NewUint(10_000)
		source.Amount = common.GetSafeShare(common.SafeSub(maxBasisPoints, msg.AffiliateBasisPoints), maxBasisPoints, source.Amount)
	}

	target := common.NewCoin(msg.TargetAsset, proportionalTarget)
	var emit cosmos.Uint
	switch {
	case !source.IsRune() && !target.IsRune():
		sourcePool, ok := pools.Get(source.Asset.GetLayer1Asset())
		if !ok {
			return false
		}
		targetPool, ok := pools.Get(target.Asset.GetLayer1Asset())
		if !ok {
			return false
		}
		emit = swapper.CalcAssetEmission(sourcePool.BalanceAsset, source.Amount, sourcePool.BalanceRune)
		emit = swapper.CalcAssetEmission(targetPool.BalanceRune, emit, targetPool.BalanceAsset)
	case source.IsRune():
		pool, ok := pools.Get(target.Asset.GetLayer1Asset())
		if !ok {
			return false
		}
		emit = swapper.CalcAssetEmission(pool.BalanceRune, source.Amount, pool.BalanceAsset)
	case target.IsRune():
		pool, ok := pools.Get(source.Asset.GetLayer1Asset())
		if !ok {
			return false
		}
		emit = swapper.CalcAssetEmission(pool.BalanceAsset, source.Amount, pool.BalanceRune)
	}

	// Check if this would be the last swap by temporarily simulating the state after this swap
	wouldBeLastSwap := false
	if msg.SwapType == MarketSwap {
		// For market swaps, check if count+1 >= quantity. We do >= instead of == just in case
		// of some unexpected dev error and count exceeds quantity somehow
		wouldBeLastSwap = msg.State.Count+1 >= msg.State.Quantity
	} else if msg.SwapType == LimitSwap {
		// Limit swaps settle on full input or their final allowed success, even
		// if earlier account withdrawals left a rounding remainder.
		tempIn := msg.State.In.Add(swapSize)
		wouldBeLastSwap = tempIn.Equal(msg.State.Deposit) ||
			(msg.State.Quantity > 0 && msg.SuccessCount()+1 >= msg.State.Quantity)
	}

	// If this would be the last swap and target is not RUNE, account for outbound fee
	if wouldBeLastSwap && !target.IsRune() {
		// Get the outbound fee from the gas manager
		outboundFee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, target.Asset, false)
		if err == nil && !outboundFee.IsZero() {
			// Deduct the outbound fee from emit amount before comparing
			emit = common.SafeSub(emit, outboundFee)
		}
	}

	return emit.GTE(target.Amount)
}

func (vm *SwapQueueAdv) getRatio(input, output cosmos.Uint) cosmos.Uint {
	if output.IsZero() {
		return cosmos.ZeroUint()
	}
	return input.MulUint64(1e8).Quo(output)
}

// hasSufficientTradeOrSecuredLimitBalance verifies that a Trade/Secured limit
// swap has enough balance for the next sub-swap and can redeem at least one
// account unit. These funds are checked when queued, but are not locked and can
// be spent before execution.
func (vm *SwapQueueAdv) hasSufficientTradeOrSecuredLimitBalance(ctx cosmos.Context, mgr Manager, msg MsgSwap, amount cosmos.Uint) bool {
	if !msg.IsLimitSwap() {
		return true
	}

	source := msg.Tx.Coins[0].Asset
	if !source.IsTradeAsset() && !source.IsSecuredAsset() {
		return true
	}

	fromAcc, err := cosmos.AccAddressFromBech32(msg.Tx.FromAddress.String())
	if err != nil {
		// Let normal swap execution surface the existing parse error.
		return true
	}

	var balance cosmos.Uint
	if source.IsTradeAsset() {
		balance = mgr.TradeAccountManager().BalanceOf(ctx, source, fromAcc)
	} else {
		balance = mgr.SecuredAssetManager().BalanceOf(ctx, source, fromAcc)
	}

	if balance.LT(amount) {
		return false
	}

	var units cosmos.Uint
	if source.IsTradeAsset() {
		account, err := mgr.Keeper().GetTradeAccount(ctx, fromAcc, source)
		if err != nil {
			// Let normal swap execution surface the existing account error.
			return true
		}
		units = account.Units
	} else {
		units = cosmos.NewUintFromBigInt(mgr.Keeper().GetBalanceOf(ctx, fromAcc, source).Amount.BigInt())
	}

	// Match the units-to-redeem calculation in Withdrawal/Withdraw. A nominally
	// sufficient balance can still round to zero units, making this chunk fail
	// repeatedly. Nonzero short fills remain executable within State.Quantity.
	return !common.GetSafeShare(amount, balance, units).IsZero()
}

// getAssetPairs fetches available pools and builds their directional pair
// cross-product.
func (vm *SwapQueueAdv) getAssetPairs(ctx cosmos.Context) (tradePairs, Pools) {
	pools := vm.getAvailablePools(ctx)
	return buildAssetPairs(pools), pools
}

// buildAssetPairs builds the directional pair cross-product of RUNE and the
// given pools, in pool order.
func buildAssetPairs(pools Pools) tradePairs {
	assets := make([]common.Asset, 0, len(pools)+1)
	assets = append(assets, common.RuneAsset())
	for _, pool := range pools {
		assets = append(assets, pool.Asset)
	}

	result := make(tradePairs, 0, len(assets)*(len(assets)-1))
	for _, a1 := range assets {
		for _, a2 := range assets {
			if a1.Equals(a2) {
				continue
			}
			result = append(result, genTradePair(a1, a2))
		}
	}
	return result
}

// samePoolAssets reports whether two getAvailablePools results hold the same
// pool assets in the same order. Balances are ignored.
func samePoolAssets(a, b Pools) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Asset.Equals(b[i].Asset) {
			return false
		}
	}
	return true
}

func (vm *SwapQueueAdv) getAvailablePools(ctx cosmos.Context) Pools {
	pools := make(Pools, 0)
	iterator := vm.k.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		err := vm.k.Cdc().Unmarshal(iterator.Value(), &pool)
		if err != nil {
			ctx.Logger().Error("fail to unmarshal pool", "error", err)
			continue
		}
		if pool.Status != PoolAvailable {
			continue
		}
		if pool.Asset.IsSyntheticAsset() {
			continue
		}
		pools = append(pools, pool)
	}
	return pools
}

func (vm *SwapQueueAdv) getMaxSwapQuantity(ctx cosmos.Context, mgr Manager, sourceAsset, targetAsset common.Asset, msg MsgSwap) (uint64, error) {
	// Pull affiliate context off the memo so sub-swap sizing respects any
	// dynamic-fee floor the affiliate has earned for this pair.
	var swapAffiliates []string
	var swapAffiliateBps []cosmos.Uint
	if parsed, parseErr := ParseMemoWithTHORNames(ctx, vm.k, msg.Tx.Memo); parseErr == nil {
		swapAffiliates = parsed.GetAffiliates()
		swapAffiliateBps = parsed.GetAffiliatesBasisPoints()
	}

	// collect pools involved in this swap
	minSwapSize := cosmos.ZeroUint()
	stableSwap := isStableToStable(ctx, vm.k, sourceAsset, targetAsset)
	var sourceAssetPool types.Pool
	for i, asset := range []common.Asset{sourceAsset, targetAsset} {
		if asset.IsRune() {
			continue
		}

		// get the asset pool
		pool, err := vm.k.GetPool(ctx, asset.GetLayer1Asset())
		if err != nil {
			ctx.Logger().Error("fail to fetch pool", "error", err)
			return 0, err
		}

		// store the source asset pool for later conversion of RUNE to asset
		if i == 0 {
			sourceAssetPool = pool
		}

		// Dynamic fee records are keyed per executed leg, not per end-to-end
		// L1->L1 route. Double swaps therefore need BTC->RUNE / RUNE->ETH style
		// lookups here, matching both execution-time fee application and crediting.
		legSource, legTarget := sourceAsset, targetAsset
		if !sourceAsset.IsRune() && !targetAsset.IsRune() {
			if i == 0 {
				legSource, legTarget = sourceAsset, common.RuneAsset()
			} else {
				legSource, legTarget = common.RuneAsset(), targetAsset
			}
		}

		// get the configured min slip for this asset
		minSlip := getMinSlipBps(ctx, vm.k, common.NoAddress, asset, stableSwap, swapAffiliates, swapAffiliateBps, legSource, legTarget, mgr.DynamicFeeMgr())
		if minSlip.IsZero() {
			continue
		}

		// compute the minimum rune swap size for this leg of the swap
		minRuneSwapSize := common.GetSafeShare(minSlip, cosmos.NewUint(constants.MaxBasisPts), pool.BalanceRune)
		if minSwapSize.IsZero() || minRuneSwapSize.LT(minSwapSize) {
			minSwapSize = minRuneSwapSize
		}
	}

	var maxSwapQuantity cosmos.Uint

	// calculate the max swap quantity
	if !sourceAsset.IsRune() {
		minSwapSize = sourceAssetPool.RuneValueInAsset(minSwapSize)
	}
	if minSwapSize.IsZero() {
		// If no minimum slip is configured, respect the user's requested quantity
		// but still check against max length limits below
		maxSwapQuantity = cosmos.NewUint(msg.State.Quantity)
	} else {
		maxSwapQuantity = msg.State.Deposit.Quo(minSwapSize)
	}

	// make sure maxSwapQuantity doesn't infringe on max length that a
	// streaming swap can exist
	var maxLength int64
	if sourceAsset.IsNative() && targetAsset.IsNative() {
		maxLength = vm.k.GetConfigInt64(ctx, constants.StreamingSwapMaxLengthNative)
	} else {
		maxLength = vm.k.GetConfigInt64(ctx, constants.StreamingSwapMaxLength)
	}
	// For limit swaps, State.Interval represents the custom TTL (expiry in blocks),
	// not the interval between sub-swaps. Limit swaps execute every block (interval=1).
	interval := msg.State.Interval
	if interval == 0 || msg.IsLimitSwap() {
		interval = 1
	}
	maxSwapInMaxLength := uint64(maxLength) / interval
	if maxSwapQuantity.GT(cosmos.NewUint(maxSwapInMaxLength)) {
		return maxSwapInMaxLength, nil
	}

	// sanity check that max swap quantity is not zero
	if maxSwapQuantity.IsZero() {
		return 1, nil
	}

	// if swapping with a derived asset, reduce quantity relative to derived
	// virtual pool depth. The equation for this as follows
	dbps := cosmos.ZeroUint()
	for _, asset := range []common.Asset{sourceAsset, targetAsset} {
		if !asset.IsDerivedAsset() {
			continue
		}
		// get the rune depth of the anchor pool(s)
		runeDepth, _, _ := mgr.NetworkMgr().CalcAnchor(ctx, mgr, asset)
		dpool, _ := vm.k.GetPool(ctx, asset) // get the derived asset pool
		newDbps := common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
		if dbps.IsZero() || newDbps.LT(dbps) {
			dbps = newDbps
		}
	}
	if !dbps.IsZero() {
		// quantity = 1 / (1-dbps)
		// But since we're dealing in basis points (to avoid float math)
		// quantity = 10,000 / (10,000 - dbps)
		maxBasisPoints := cosmos.NewUint(constants.MaxBasisPts)
		diff := common.SafeSub(maxBasisPoints, dbps)
		if !diff.IsZero() {
			newQuantity := maxBasisPoints.Quo(diff)
			if maxSwapQuantity.GT(newQuantity) {
				return newQuantity.Uint64(), nil
			}
		}
	}

	return maxSwapQuantity.Uint64(), nil
}

func (vm *SwapQueueAdv) AddSwapQueueItem(ctx cosmos.Context, mgr Manager, msg *MsgSwap) error {
	// If advanced swap queue is in market-only mode, reject limit swaps
	val := vm.k.GetConfigInt64(ctx, constants.EnableAdvSwapQueue)
	if types.AdvSwapQueueMode(val) == types.AdvSwapQueueModeMarketOnly && msg.IsLimitSwap() {
		return fmt.Errorf("limit swaps are not allowed in market-only mode")
	}

	// Set initial block height when adding the swap
	if msg.InitialBlockHeight == 0 {
		msg.InitialBlockHeight = ctx.BlockHeight()
	}

	// Initialize deposit state if not already set
	if msg.State.Deposit.IsZero() {
		msg.State.Deposit = msg.Tx.Coins[0].Amount
	}

	// Cap limit swap custom TTL (stored in State.Interval) to maxAge so the
	// stored value is always within bounds for downstream consumers.
	if msg.IsLimitSwap() && msg.State.Interval > 0 {
		swapMaxAge := vm.k.GetConfigInt64(ctx, constants.StreamingLimitSwapMaxAge)
		if swapMaxAge < 0 {
			return fmt.Errorf("%s returned maxAge < 0", constants.StreamingLimitSwapMaxAge)
		}
		if maxAge := uint64(swapMaxAge); msg.State.Interval > maxAge {
			msg.State.Interval = maxAge
		}
	}

	maxSwapQuantity, err := vm.getMaxSwapQuantity(ctx, mgr, msg.Tx.Coins[0].Asset, msg.TargetAsset, *msg)
	if err != nil {
		return err
	}

	if msg.State.Quantity == 0 {
		msg.State.Quantity = maxSwapQuantity
	}

	if msg.State.Quantity > maxSwapQuantity {
		msg.State.Quantity = maxSwapQuantity
	}

	swapHandler := NewSwapHandler(mgr)
	if err := swapHandler.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSwap failed validation", "error", err)
		return err
	}

	// Add TTL tracking for limit swaps
	if msg.IsLimitSwap() && msg.InitialBlockHeight > 0 {
		ttl := vm.getTTL(ctx, *msg)
		expiryHeight := msg.InitialBlockHeight + ttl
		if err := vm.k.AddToLimitSwapTTL(ctx, expiryHeight, msg.Tx.ID); err != nil {
			ctx.Logger().Error("fail to add limit swap to TTL", "error", err, "txID", msg.Tx.ID.String(), "expiryHeight", expiryHeight)
			return err
		}
	}

	if err := vm.k.SetAdvSwapQueueItem(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to add swap item", "error", err)
		return err
	}

	return nil
}

// advSwapQueueMaxFailedReasons bounds the per-item FailedSwapReasons telemetry
// slice that EndBlock persists on every re-write of a streaming swap. Without a
// bound each failed sub-swap appends a full error string that is never trimmed,
// growing the stored MsgSwap blob to megabytes for a long-lived failing swap and
// pinning that blob in the IAVL node cache (the dominant driver of validator RSS
// growth to GOMEMLIMIT). Only the error-string history is trimmed; FailedSwaps
// is deliberately left intact so FailCount()/SuccessCount() are unchanged.
const advSwapQueueMaxFailedReasons = 10

// capFailedSwapReasons trims msg.State.FailedSwapReasons to the most recent
// advSwapQueueMaxFailedReasons entries. It copies into a fresh slice (rather than
// re-slicing) so the discarded strings and the original backing array become
// unreachable and can be freed. FailedSwaps is intentionally NOT touched: its
// length is FailCount(), which drives SuccessCount() and limit-swap settlement.
func capFailedSwapReasons(msg *MsgSwap) {
	if msg == nil || msg.State == nil {
		return
	}
	if n := len(msg.State.FailedSwapReasons); n > advSwapQueueMaxFailedReasons {
		msg.State.FailedSwapReasons = append([]string(nil), msg.State.FailedSwapReasons[n-advSwapQueueMaxFailedReasons:]...)
	}
}

// EndBlock trigger the real swap to be processed
func (vm *SwapQueueAdv) EndBlock(ctx cosmos.Context, mgr Manager, telemetryEnabled bool) error {
	// Readiness and handler validation repeatedly query the same Mimir/halt
	// keys for every queue item. Any Mimir write during this EndBlock (e.g. the
	// MaxRuneSupply circuit breaker) disables the cache for the rest of it.
	ctx = keeper.WithMimirCache(ctx)
	ctx = WithMemoCache(ctx)

	swapHandler := NewSwapHandler(mgr)

	minSwapsPerBlock, err := vm.k.GetMimir(ctx, constants.MinSwapsPerBlock.String())
	if minSwapsPerBlock < 0 || err != nil {
		minSwapsPerBlock = mgr.GetConstants().GetInt64Value(constants.MinSwapsPerBlock)
	}
	maxSwapsPerBlock, err := vm.k.GetMimir(ctx, constants.MaxSwapsPerBlock.String())
	if maxSwapsPerBlock < 0 || err != nil {
		maxSwapsPerBlock = mgr.GetConstants().GetInt64Value(constants.MaxSwapsPerBlock)
	}
	synthVirtualDepthMult, err := vm.k.GetMimir(ctx, constants.VirtualMultSynthsBasisPoints.String())
	if synthVirtualDepthMult < 1 || err != nil {
		synthVirtualDepthMult = mgr.GetConstants().GetInt64Value(constants.VirtualMultSynthsBasisPoints)
	}

	// Get rapid swap max from config (mimir or constants)
	rapidSwapMax := mgr.Keeper().GetConfigInt64(ctx, constants.AdvSwapQueueRapidSwapMax)

	// During simulations, keep todo empty to avoid limit swap discovery blocking on iterators
	isSimulation := ctx.Value(constants.CtxSimulationMode) == true

	todo := make(tradePairs, 0)
	todoSeen := make(tradePairSet)
	var pairs tradePairs
	var pairIndex tradePairIndex
	var pools Pools
	var activeLimitPairs tradePairSet
	iterationCount := int64(0)

	// Telemetry tracking variables (counts reflect successful swap executions only)
	swapSuccesses := int64(0)
	marketSwapSuccesses := int64(0)
	limitSwapSuccesses := int64(0)
	completedSwapCount := int64(0)

	// Failed swap attempts, bucketed by a fixed closed set of reasons. Locals
	// only - never chain state - so accumulating on every node while emitting
	// on telemetry nodes only is consensus-neutral.
	failedSwapReasons := map[string]int64{}
	failedSwapCount := int64(0)

	// Track the last swap direction per pool for rapid swap safety.
	lastPoolDir := make(poolSwapDir)

	// Rapid swap iterations - exit early if no swaps succeeded on previous iteration
	prevIterationSuccess := int64(0)
	for iteration := int64(0); iteration < rapidSwapMax; iteration++ {
		// Exit early if previous iteration had no successful swaps (pool state unchanged)
		if iteration > 0 && prevIterationSuccess == 0 {
			break
		}

		iterationCount = iteration + 1

		// Refresh balances each iteration, but only rebuild the O(P^2) pair
		// list when pool membership changes. A swap through a derived asset
		// can reactivate (or suspend) its virtual pool mid-EndBlock, and later
		// iterations must see the same pairs a per-iteration rebuild would.
		if !isSimulation {
			newPools := vm.getAvailablePools(ctx)
			if iteration == 0 || !samePoolAssets(pools, newPools) {
				pairs = buildAssetPairs(newPools)
				pairIndex = newTradePairIndex(pairs)
				activeLimitPairs = vm.getActiveLimitPairs(ctx, len(pairs))
			}
			pools = newPools
		}

		swaps, err := vm.fetchQueue(ctx, mgr, pairs, pools, todo, activeLimitPairs)
		if err != nil {
			ctx.Logger().Error("fail to fetch swap queue from store", "error", err)
			return err
		}

		// Exit early if no swaps found
		if len(swaps) == 0 {
			break
		}

		swaps, err = vm.scoreMsgs(ctx, swaps, synthVirtualDepthMult)
		if err != nil {
			ctx.Logger().Error("fail to fetch swap items", "error", err)
			// continue, don't exit, just do them out of order (instead of not at all)
		}
		swaps = swaps.Sort()

		budget := vm.getTodoNum(int64(len(swaps)), minSwapsPerBlock, maxSwapsPerBlock)
		iterationSuccess := int64(0)
		attempted := int64(0)
		for _, pick := range swaps {
			if attempted >= budget {
				break
			}
			// Direction checks are read-only, so perform them before cloning the
			// message. Later rapid iterations can skip a large fraction of the
			// queue; avoid a protobuf marshal+unmarshal for every skipped item.
			if shouldSkipRapidSwapDirection(pick.msg, lastPoolDir, iteration) {
				continue
			}
			data, marshalErr := proto.Marshal(&pick.msg)
			if marshalErr != nil {
				ctx.Logger().Error("fail to marshal msg swap", "error", marshalErr)
				continue
			}
			if len(data) == 0 {
				ctx.Logger().Error("skip empty swap message", "tx", pick.msg.Tx.ID)
				continue
			}
			msg := &MsgSwap{}
			if marshalErr = proto.Unmarshal(data, msg); marshalErr != nil {
				ctx.Logger().Error("fail to unmarshal msg swap", "error", marshalErr)
				continue
			}

			// Preserve original values before modification
			originalAmount := msg.Tx.Coins[0].Amount
			originalTradeTarget := msg.TradeTarget

			nextAmount, nextTradeTarget := msg.NextSize()
			// Trade/Secured source balances are not escrowed for queued limit
			// swaps. If the next chunk is no longer fully funded or rounds to zero
			// account units, settle before mutating the message so queue index
			// removal uses the original ratio.
			if !vm.hasSufficientTradeOrSecuredLimitBalance(ctx, mgr, *msg, nextAmount) {
				attempted++
				if err := settleSwap(ctx, mgr, *msg, "insufficient trade/secured asset balance"); err != nil {
					ctx.Logger().Error("fail to settle underfunded limit swap", "error", err, "txID", msg.Tx.ID.String(), "index", msg.Index)
				} else {
					completedSwapCount++
				}
				continue
			}

			msg.Tx.Coins[0].Amount, msg.TradeTarget = nextAmount, nextTradeTarget

			// Create a cache context for the swap to ensure state changes are only
			// committed if the swap succeeds (similar to regular swap queue manager)
			cacheCtx, commit := ctx.CacheContext()

			// make the primary swap using the cached context
			var settleMsg string
			emit, effectiveInput := cosmos.ZeroUint(), cosmos.ZeroUint()
			handleErr := runLuviGuarded(ctx, cacheCtx, mgr, msg, func(runCtx cosmos.Context) error {
				var runErr error
				_, emit, effectiveInput, runErr = swapHandler.runWithEmitAndInput(runCtx, msg)
				return runErr
			})
			// Every handler invocation consumes execution budget. State progression
			// may still be skipped for failed market swaps on rapid iterations, but
			// failures must not make MaxSwapsPerBlock ineffective as a CPU bound.
			attempted++
			if handleErr != nil {
				// On iteration 1+ (second rapid swap iteration and beyond), failed market swaps
				// should not advance state. This prevents rapid swaps from burning through
				// all subswaps of a streaming swap in a single block - it behaves like rapid
				// swaps don't exist when swaps can't execute ("escalator becomes stairs").
				if iteration > 0 && msg.IsMarketSwap() {
					continue
				}
				// Telemetry only: bucket the failure into a fixed reason set so a
				// block where every attempt failed is distinguishable from a block
				// with no swaps at all (both read zero on the success counters).
				failedSwapCount++
				switch {
				case errors.Is(handleErr, errSwapFailInvalidAmount):
					failedSwapReasons["invalid_amount"]++
				case errors.Is(handleErr, errSwapFailInvalidBalance):
					failedSwapReasons["invalid_balance"]++
				case errors.Is(handleErr, errSwapFailNotEnoughBalance):
					failedSwapReasons["not_enough_balance"]++
				default:
					failedSwapReasons["other"]++
				}
				// Don't commit - this discards all state changes
				ctx.Logger().Error("fail to execute streaming swap interval", "error", handleErr)
				msg.State.FailedSwaps = append(msg.State.FailedSwaps, msg.State.Count)
				msg.State.FailedSwapReasons = append(msg.State.FailedSwapReasons, handleErr.Error())
				settleMsg = handleErr.Error()
			} else {
				// Update state for successful swap execution (in memory only)
				msg.State.In = msg.State.In.Add(effectiveInput)
				msg.State.Out = msg.State.Out.Add(emit)

				// Update count/height and save swap state in the cache context so
				// it commits atomically with pool changes. This prevents state
				// inconsistency where pool balances are committed but swap tracking
				// state (In, Out, Count) is not persisted.
				msg.State.Count += 1
				msg.State.LastHeight = ctx.BlockHeight()
				msg.Tx.Coins[0].Amount = originalAmount
				msg.TradeTarget = originalTradeTarget
				capFailedSwapReasons(msg)
				if err := vm.k.SetAdvSwapQueueItemState(cacheCtx, *msg); err != nil {
					ctx.Logger().Error("fail to save swap item in cache context, discarding swap", "error", err)
					continue
				}

				// Commit pool changes and swap state atomically
				commit()

				// All success-side bookkeeping below: only reached after the
				// cache-context write and commit have both succeeded.
				settleMsg = "swap has been completed"
				if msg.IsLimitSwap() && msg.SuccessCount() >= msg.State.Quantity && msg.State.In.LT(msg.State.Deposit) {
					settleMsg = "limit swap quantity reached with remaining input"
				}
				iterationSuccess++

				// Track swap direction for rapid swap safety check
				for _, d := range getSwapDirections(*msg) {
					lastPoolDir[d.pool] = d.direction
				}

				// Only populate todo for limit swap discovery during real execution (not simulations)
				if !isSimulation {
					todo = todo.findMatchingTradesIndexed(genTradePair(msg.Tx.Coins[0].Asset, msg.TargetAsset), pairIndex, todoSeen)
				}

				// Track successful swap types for telemetry
				if msg.IsLimitSwap() {
					limitSwapSuccesses++
				} else {
					marketSwapSuccesses++
				}
				swapSuccesses++

				if vm.IsDone(ctx, *msg) {
					if err := settleSwap(ctx, mgr, *msg, settleMsg); err != nil {
						ctx.Logger().Error("fail to handle completed streaming limit swap", "error", err)
					}
					completedSwapCount++
				}
				continue
			}
			msg.State.Count += 1
			msg.State.LastHeight = ctx.BlockHeight()

			// Restore original values before saving
			msg.Tx.Coins[0].Amount = originalAmount
			msg.TradeTarget = originalTradeTarget

			// Save the updated swap state (failure path — no pool commit to be inconsistent with)
			capFailedSwapReasons(msg)
			if err := vm.k.SetAdvSwapQueueItemState(ctx, *msg); err != nil {
				ctx.Logger().Error("fail to save swap item", "error", err)
			}
			if vm.IsDone(ctx, *msg) {
				if err := settleSwap(ctx, mgr, *msg, settleMsg); err != nil {
					ctx.Logger().Error("fail to handle completed streaming limit swap", "error", err)
				}
				completedSwapCount++
			}
		}

		// Track success for early exit on next iteration
		prevIterationSuccess = iterationSuccess
	}

	// Log the number of iterations completed
	ctx.Logger().Info("advanced swap iterations completed", "count", iterationCount)

	// Process expired limit swaps at current block height
	// Skip during simulation - no TTL entries exist in simulation context
	if !isSimulation {
		if err := vm.processExpiredLimitSwaps(ctx, mgr); err != nil {
			ctx.Logger().Error("fail to process expired limit swaps", "error", err)
		}
	}

	// Emit telemetry metrics only if telemetry is enabled
	if telemetryEnabled && !isSimulation {
		vm.emitAdvSwapQueueTelemetry(ctx, mgr, iterationCount, swapSuccesses, marketSwapSuccesses, limitSwapSuccesses, completedSwapCount)

		// Fixed, ordered reason list: never iterate the map itself.
		for _, reason := range []string{"invalid_amount", "invalid_balance", "not_enough_balance", "other"} {
			if n := failedSwapReasons[reason]; n > 0 {
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "adv_swap_queue", "failed_swaps_total"},
					float32(n),
					[]metrics.Label{telemetry.NewLabel("reason", reason)},
				)
			}
		}
		telemetry.SetGauge(float32(failedSwapCount), "thornode", "adv_swap_queue", "failed_swaps_per_block")
	}

	return nil
}

// getTodoNum - determine how many swaps to do.
func (vm *SwapQueueAdv) getTodoNum(queueLen, minSwapsPerBlock, maxSwapsPerBlock int64) int64 {
	// Do half the length of the queue. Unless...
	//  1. Half the queue length is greater than maxSwapsPerBlock
	//  2. Half the queue length is less than minSwapsPerBlock
	todo := queueLen / 2
	if minSwapsPerBlock > todo {
		if queueLen < minSwapsPerBlock {
			todo = queueLen
		} else {
			todo = minSwapsPerBlock
		}
	}
	if maxSwapsPerBlock < todo {
		todo = maxSwapsPerBlock
	}
	return todo
}

// scoreMsgs - this takes a list of MsgSwap, and converts them to a scored
// swapItem list
func (vm *SwapQueueAdv) scoreMsgs(ctx cosmos.Context, items swapItems, synthVirtualDepthMult int64) (swapItems, error) {
	pools := make(map[common.Asset]Pool)

	for i, item := range items {
		// the asset customer send
		sourceAsset := item.msg.Tx.Coins[0].Asset
		// the asset customer want
		targetAsset := item.msg.TargetAsset

		for _, a := range []common.Asset{sourceAsset, targetAsset} {
			if a.IsRune() {
				continue
			}

			if _, ok := pools[a]; !ok {
				var err error
				pools[a], err = vm.k.GetPool(ctx, a.GetLayer1Asset())
				if err != nil {
					ctx.Logger().Error("fail to get pool", "pool", a, "error", err)
					continue
				}
			}
		}

		isDoubleSwap := !sourceAsset.IsRune() && !targetAsset.IsRune()
		stableSwap := isDoubleSwap && isStableToStable(ctx, vm.k, sourceAsset, targetAsset)

		poolAsset := sourceAsset
		if poolAsset.IsRune() {
			poolAsset = targetAsset
		}
		pool := pools[poolAsset]
		if pool.IsEmpty() || !pool.IsAvailable() || pool.BalanceRune.IsZero() || pool.BalanceAsset.IsZero() {
			continue
		}
		virtualDepthMult := int64(10_000)
		if poolAsset.IsSyntheticAsset() {
			virtualDepthMult = synthVirtualDepthMult
		}
		// Get the next sub-swap size for scoring streaming swaps fairly
		// For non-streaming swaps (quantity=1), this returns the full deposit
		subSwapSize, _ := item.msg.NextSize()
		// Fallback to full coin amount if NextSize returns zero (uninitialized state)
		if subSwapSize.IsZero() {
			subSwapSize = item.msg.Tx.Coins[0].Amount
		}
		sourceCoin := common.NewCoin(item.msg.Tx.Coins[0].Asset, subSwapSize)
		emitRune := vm.addLiquidityFee(ctx, pool, sourceCoin, &items[i], virtualDepthMult, poolAsset, stableSwap)

		if !isDoubleSwap {
			continue
		}
		// double swap , thus use the first leg's emitted RUNE and calculate fee again
		runeCoin := common.NewCoin(common.RuneAsset(), emitRune)
		poolAsset = targetAsset
		pool = pools[poolAsset]
		if pool.IsEmpty() || !pool.IsAvailable() || pool.BalanceRune.IsZero() || pool.BalanceAsset.IsZero() {
			// Reset first-leg scoring to prevent inflated priority from partial scoring
			items[i].fee = cosmos.ZeroUint()
			continue
		}
		virtualDepthMult = int64(10_000)
		if targetAsset.IsSyntheticAsset() {
			virtualDepthMult = synthVirtualDepthMult
		}
		vm.addLiquidityFee(ctx, pool, runeCoin, &items[i], virtualDepthMult, poolAsset, stableSwap)
	}

	return items, nil
}

// addLiquidityFee adds the effective liquidity fee (in RUNE) for one swap leg
// to item.fee and returns the emitted amount for the leg. Uses GetSwapCalc so
// the score honors the min-slip floor that swapOne actually charges.
func (vm *SwapQueueAdv) addLiquidityFee(ctx cosmos.Context, pool Pool, sourceCoin common.Coin, item *swapItem, virtualDepthMult int64, nonRuneAsset common.Asset, stableSwap bool) cosmos.Uint {
	// Get our X, x, Y values
	var X, x, Y cosmos.Uint
	x = sourceCoin.Amount
	if sourceCoin.IsRune() {
		X = pool.BalanceRune
		Y = pool.BalanceAsset
	} else {
		Y = pool.BalanceRune
		X = pool.BalanceAsset
	}

	if virtualDepthMult != 10_000 {
		X = common.GetUncappedShare(cosmos.NewUint(uint64(virtualDepthMult)), cosmos.NewUint(10_000), X)
		Y = common.GetUncappedShare(cosmos.NewUint(uint64(virtualDepthMult)), cosmos.NewUint(10_000), Y)
	}

	swapper, err := GetSwapper(vm.k.GetVersion())
	if err != nil {
		panic(err)
	}
	swapSlipBps := swapper.CalcSwapSlip(X, x)
	minSlipBps := getMinSlipBps(ctx, vm.k, common.NoAddress, nonRuneAsset, stableSwap, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	emit, fee, _ := swapper.GetSwapCalc(X, x, Y, swapSlipBps, minSlipBps)
	emit = cosmos.RoundToDecimal(emit, pool.Decimals)
	if sourceCoin.IsRune() {
		fee = pool.AssetValueInRune(fee)
	}
	item.fee = item.fee.Add(fee)
	return emit
}

func (vm *SwapQueueAdv) parseRatioFromKey(key string) (uint64, error) {
	parts := strings.Split(key, "/")
	if len(parts) < 5 {
		return 0, fmt.Errorf("invalid key format")
	}
	return strconv.ParseUint(parts[len(parts)-2], 10, 64)
}

// processExpiredLimitSwaps processes limit swaps that have expired (reached TTL) at the current block height.
// TTL entries are stored at their exact expiry height when a swap is added, and failed settlements
// are re-queued at currentHeight+1, so only the current height needs to be checked each block.
func (vm *SwapQueueAdv) processExpiredLimitSwaps(ctx cosmos.Context, mgr Manager) error {
	currentHeight := ctx.BlockHeight()

	expiredTxHashes, err := vm.k.GetLimitSwapTTL(ctx, currentHeight)
	if err != nil {
		ctx.Logger().Error("fail to get limit swap TTL", "error", err, "blockHeight", currentHeight)
		return nil
	}

	if len(expiredTxHashes) == 0 {
		return nil
	}

	// Process each expired swap, tracking failures for retry
	var failedTxHashes []common.TxID
	for _, txHash := range expiredTxHashes {
		// Try to find the swap in the advanced queue
		// Iterate until HasAdvSwapQueueItem returns false to handle all indices
		found := false
		settleFailed := false
		for index := 0; vm.k.HasAdvSwapQueueItem(ctx, txHash, index); index++ {
			swap, err := vm.k.GetAdvSwapQueueItem(ctx, txHash, index)
			if err != nil {
				ctx.Logger().Error("fail to get expired swap item", "error", err, "txID", txHash.String(), "index", index)
				continue
			}

			// Verify this is actually a limit swap that should expire
			if swap.IsLimitSwap() {
				if err := settleSwap(ctx, mgr, swap, "limit swap expired"); err != nil {
					ctx.Logger().Error("fail to settle expired limit swap", "error", err, "txID", txHash.String(), "index", index)
					settleFailed = true
				} else {
					// Only a settled expiry counts: TTL entries for swaps filled or
					// cancelled before TTL still fire here, and failed settles are
					// re-queued every block, so len(expiredTxHashes) overcounts both.
					telemetry.IncrCounter(1, "thornode", "limit_swap", "expired")
				}
				found = true
			}
		}

		if settleFailed {
			failedTxHashes = append(failedTxHashes, txHash)
			// Failed settles are re-queued at currentHeight+1 forever below, so a
			// wedged settle increments this every single block.
			telemetry.IncrCounter(1, "thornode", "limit_swap", "settle_failed")
		}

		if !found {
			ctx.Logger().Debug("expired swap not found in queue", "txID", txHash.String(), "blockHeight", currentHeight)
		}
	}

	// Remove the TTL entry since we've processed all expired swaps for this height
	vm.k.RemoveLimitSwapTTL(ctx, currentHeight)

	// Re-add failed txHashes at a future height for retry so they are not permanently orphaned
	for _, txHash := range failedTxHashes {
		retryHeight := currentHeight + 1
		if err := vm.k.AddToLimitSwapTTL(ctx, retryHeight, txHash); err != nil {
			ctx.Logger().Error("fail to re-add failed limit swap TTL for retry", "error", err, "txID", txHash.String(), "retryHeight", retryHeight)
		}
	}

	return nil
}

// emitAdvSwapQueueTelemetry emits telemetry metrics for the advanced swap queue
// This method should only be called when telemetry is enabled
func (vm *SwapQueueAdv) emitAdvSwapQueueTelemetry(ctx cosmos.Context, mgr Manager, iterationCount, swapSuccesses, marketSwapSuccesses, limitSwapSuccesses, completedSwapCount int64) {
	// Emit core metrics (counts reflect successful swap executions only)
	telemetry.SetGauge(float32(iterationCount), "thornode", "adv_swap_queue", "iterations_per_block")
	telemetry.SetGauge(float32(swapSuccesses), "thornode", "adv_swap_queue", "total_swaps_per_block")
	telemetry.SetGauge(float32(marketSwapSuccesses), "thornode", "adv_swap_queue", "market_swaps_per_block")
	telemetry.SetGauge(float32(limitSwapSuccesses), "thornode", "adv_swap_queue", "limit_swaps_per_block")
	telemetry.SetGauge(float32(completedSwapCount), "thornode", "adv_swap_queue", "completed_swaps_per_block")

	// Emit total counters (these will accumulate over time)
	telemetry.IncrCounterWithLabels([]string{"thornode", "adv_swap_queue", "market_swaps_total"}, float32(marketSwapSuccesses), nil)
	telemetry.IncrCounterWithLabels([]string{"thornode", "adv_swap_queue", "limit_swaps_total"}, float32(limitSwapSuccesses), nil)
	telemetry.IncrCounterWithLabels([]string{"thornode", "adv_swap_queue", "swaps_completed_total"}, float32(completedSwapCount), nil)

	// Emit queue depth and trading pair metrics
	vm.emitQueueDepthTelemetry(ctx, mgr)
}

// emitQueueDepthTelemetry emits queue depth metrics per trading pair
func (vm *SwapQueueAdv) emitQueueDepthTelemetry(ctx cosmos.Context, mgr Manager) {
	// Get 1 RUNE price in USD for value conversions
	runeUSDPrice := vm.telem(mgr.Keeper().DollarsPerRune(ctx))

	totalLimitSwaps := int64(0)
	totalLimitSwapValue := cosmos.ZeroUint()

	// The old telemetry path opened a limit-index iterator for every
	// directional pool pair, even when nearly all pairs were empty. Scan the
	// queue item store once and aggregate in memory instead. This is telemetry
	// only and changes no consensus state.
	type pairStats struct {
		count int64
		value cosmos.Uint
	}
	pools := vm.getAvailablePools(ctx)
	availableAssets := make(map[common.Asset]struct{}, len(pools)+1)
	poolByAsset := make(map[common.Asset]Pool, len(pools))
	availableAssets[common.RuneAsset()] = struct{}{}
	for _, pool := range pools {
		availableAssets[pool.Asset] = struct{}{}
		poolByAsset[pool.Asset] = pool
	}

	stats := make(map[tradePair]pairStats)
	pairOrder := make(tradePairs, 0)
	marketSwapsQueued := int64(0)
	queuedItems := int64(0)
	oldestAgeBlocks := int64(0)
	iter := vm.k.GetAdvSwapQueueItemIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var msg MsgSwap
		if err := vm.k.Cdc().Unmarshal(iter.Value(), &msg); err != nil {
			continue
		}

		// Oldest queued adv-swap age: a limit swap whose TTL settle keeps failing, or
		// a market swap whose pool went unavailable, sits here forever while newer
		// swaps churn past it, so queue depth alone cannot see it. Only adv-queue
		// items carry InitialBlockHeight (set in AddSwapQueueItem); legacy items
		// carry 0 and are skipped. Counted before the pair filters below so depth
		// covers every stored item, not just the ones that feed the pair gauges.
		queuedItems++
		if msg.InitialBlockHeight > 0 {
			if age := ctx.BlockHeight() - msg.InitialBlockHeight; age > oldestAgeBlocks {
				oldestAgeBlocks = age
			}
		}

		if msg.State == nil || len(msg.Tx.Coins) != 1 {
			continue
		}
		if msg.IsMarketSwap() {
			marketSwapsQueued++
			continue
		}
		if !msg.IsLimitSwap() {
			continue
		}

		source := msg.Tx.Coins[0].Asset.GetLayer1Asset()
		target := msg.TargetAsset.GetLayer1Asset()
		if _, ok := availableAssets[source]; !ok {
			continue
		}
		if _, ok := availableAssets[target]; !ok {
			continue
		}

		pair := genTradePair(source, target)
		stat, exists := stats[pair]
		if !exists {
			stat.value = cosmos.ZeroUint()
			pairOrder = append(pairOrder, pair)
		}
		stat.count++
		remainingValue := common.SafeSub(msg.State.Deposit, msg.State.In)
		runeValue := remainingValue
		if !source.IsRune() {
			if pool, ok := poolByAsset[source]; ok {
				runeValue = pool.AssetValueInRune(remainingValue)
			}
		}
		stat.value = stat.value.Add(runeValue)
		stats[pair] = stat
	}
	iter.Close()

	for _, pair := range pairOrder {
		stat := stats[pair]
		totalLimitSwaps += stat.count
		totalLimitSwapValue = totalLimitSwapValue.Add(stat.value)

		if stat.count > 0 {
			labels := []metrics.Label{
				telemetry.NewLabel("source_asset", pair.source.String()),
				telemetry.NewLabel("target_asset", pair.target.String()),
			}
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "adv_swap_queue", "limit_swaps_by_pair"},
				float32(stat.count),
				labels,
			)
			// Emit both RUNE and USD values for trading pair
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "adv_swap_queue", "limit_swap_value_by_pair", "rune"},
				vm.telem(stat.value),
				labels,
			)
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "adv_swap_queue", "limit_swap_value_by_pair", "usd"},
				vm.telem(stat.value)*runeUSDPrice,
				labels,
			)
		}
	}

	// Emit global metrics in both RUNE and USD
	telemetry.SetGauge(float32(totalLimitSwaps), "thornode", "adv_swap_queue", "total_limit_swaps")
	telemetry.SetGauge(vm.telem(totalLimitSwapValue), "thornode", "adv_swap_queue", "total_limit_swap_value", "rune")
	telemetry.SetGauge(vm.telem(totalLimitSwapValue)*runeUSDPrice, "thornode", "adv_swap_queue", "total_limit_swap_value", "usd")

	telemetry.SetGauge(float32(marketSwapsQueued), "thornode", "adv_swap_queue", "market_swaps_queued")
	telemetry.SetGauge(float32(queuedItems), "thornode", "adv_swap_queue", "items")
	telemetry.SetGauge(float32(oldestAgeBlocks), "thornode", "adv_swap_queue", "oldest_swap_age_blocks")
}

// telem converts cosmos.Uint to float32 for telemetry (similar to helpers.go)
func (vm *SwapQueueAdv) telem(input cosmos.Uint) float32 {
	if !input.BigInt().IsUint64() {
		return 0
	}
	i := input.Uint64()
	return float32(i) / 100000000
}
