package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// SwapHandler is the handler to process swap request
type SwapHandler struct {
	mgr Manager
}

// NewSwapHandler create a new instance of swap handler
func NewSwapHandler(mgr Manager) SwapHandler {
	return SwapHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of swap message
func (h SwapHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	result, _, err := h.RunWithEmit(ctx, m)
	return result, err
}

// RunWithEmit executes the swap and returns the emit amount along with the result.
func (h SwapHandler) RunWithEmit(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, cosmos.Uint, error) {
	result, emit, _, err := h.runWithEmitAndInput(ctx, m)
	return result, emit, err
}

func (h SwapHandler) runWithEmitAndInput(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, cosmos.Uint, cosmos.Uint, error) {
	msg, ok := m.(*MsgSwap)
	if !ok {
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSwap failed validation", "error", err)
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}
	result, emit, input, err := h.handleWithSwapResult(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgSwap", "error", err)
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}
	return result, emit, input, err
}

func (h SwapHandler) validate(ctx cosmos.Context, msg MsgSwap) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	if msg.Destination.Equals(common.Address(h.mgr.Keeper().GetModuleAccAddress(AffiliateCollectorName).String())) &&
		affiliateCollectorConversionName(h.mgr.Keeper(), msg) == "" {
		return fmt.Errorf("external swap cannot target the affiliate collector module")
	}

	stableReserveInternal, err := isStableReserveInternalSwap(ctx, h.mgr, msg)
	if err != nil {
		return err
	}
	if stableReserveInternal && !msg.TargetAsset.IsRune() {
		if ok, reason := h.mgr.StableReserveManager().IsEligibleStable(ctx, msg.TargetAsset); !ok {
			purpose := "deployment"
			if strings.HasPrefix(msg.Tx.Memo, types.StableReserveRescueMemoPrefix) {
				purpose = "rescue"
			}
			return fmt.Errorf("stable reserve %s target no longer eligible: %s", purpose, reason)
		}
	}

	// For external-origin (here valid) memos, do not allow a network module as the final destination.
	// If unable to parse the memo, here assume it to be internal.
	memo, _ := ParseMemoWithTHORNamesCached(ctx, h.mgr.Keeper(), msg.Tx.Memo)
	mem, isSwapMemo := memo.(SwapMemo)
	target := msg.TargetAsset

	// Reject a swap whose final destination is the target EVM asset's own token
	// contract. On EVM chains the output address would be the ERC-20 contract
	// itself, not a recipient wallet; paying out there burns/locks the funds with
	// no recovery, so refund at validation instead. Only EVM token assets carry a
	// "-<contract>" suffix in their symbol (native gas assets and RUNE do not).
	// Checked outside the isSwapMemo block, on msg.Destination (the address the
	// outbound actually pays), so it also covers MsgSwaps built from non-swap memos
	// (e.g. preferred-asset swaps, where Destination is a user-controlled THORName alias).
	if target.Chain.IsEVM() && !msg.Destination.IsEmpty() {
		parts := strings.Split(target.Symbol.String(), "-")
		if len(parts) >= 2 && len(parts[1]) > 0 &&
			strings.EqualFold(msg.Destination.String(), parts[1]) {
			return fmt.Errorf("swap destination cannot be the target asset's token contract")
		}
	}

	if isSwapMemo {
		// Prevent swaps to module addresses to protect system invariants,
		// except for swaps to RUNE destined for the reserve (for over-solvency clearing).
		destAccAddr, err := mem.Destination.AccAddress()
		if err == nil && IsModuleAccAddress(h.mgr.Keeper(), destAccAddr) {
			// Allow swaps to RUNE (any asset -> RUNE) destined for reserve module (for over-solvency clearing)
			isSwapToRuneForReserve := target.IsRune() &&
				destAccAddr.Equals(h.mgr.Keeper().GetModuleAccAddress(ReserveName))
			if !isSwapToRuneForReserve {
				return fmt.Errorf("a network module cannot be the final destination of a swap memo")
			}
		}

		if target.IsSyntheticAsset() && h.mgr.Keeper().GetConfigInt64(ctx, constants.ManualSwapsToSynthDisabled) > 0 {
			// Reject manual swap attempts for minting synths (encouraging Trade Assets for manual swaps),
			// allowing synth minting only in other contexts like with add liquidity memos (Savers) or internal memos.
			return fmt.Errorf("manual swaps to synths not supported, use trade assets instead")
		}
	}

	if h.mgr.Keeper().IsTradingHalt(ctx, &msg) {
		return errors.New("trading is halted, can't process swap")
	}

	if target.IsDerivedAsset() || msg.Tx.Coins[0].Asset.IsDerivedAsset() {
		if h.mgr.Keeper().GetConfigInt64(ctx, constants.EnableDerivedAssets) == 0 {
			// since derived assets are disabled, only the protocol can use
			// them (specifically lending)
			if !msg.Tx.FromAddress.Equals(common.NoopAddress) && !msg.Tx.ToAddress.Equals(common.NoopAddress) && !msg.Destination.Equals(common.NoopAddress) {
				return fmt.Errorf("swapping to/from a derived asset is not allowed, except for lending (%s or %s)", msg.Tx.FromAddress, msg.Destination)
			}
		}
	}

	if len(msg.Aggregator) > 0 {
		swapOutDisabled := h.mgr.Keeper().GetConfigInt64(ctx, constants.SwapOutDexAggregationDisabled)
		if swapOutDisabled > 0 {
			return errors.New("swap out dex integration disabled")
		}
		if !msg.TargetAsset.Equals(msg.TargetAsset.Chain.GetGasAsset()) {
			return fmt.Errorf("target asset (%s) is not gas asset , can't use dex feature", msg.TargetAsset)
		}
		// validate that a referenced dex aggregator is legit
		addr, err := FetchDexAggregator(target.Chain, msg.Aggregator)
		if err != nil {
			return err
		}
		if addr == "" {
			return fmt.Errorf("aggregator address is empty")
		}
		if len(msg.AggregatorTargetAddress) == 0 {
			return fmt.Errorf("aggregator target address is empty")
		}
	}

	if target.IsSyntheticAsset() && target.GetLayer1Asset().IsNative() {
		return errors.New("minting a synthetic of a native coin is not allowed")
	}

	if target.IsTradeAsset() && target.GetLayer1Asset().IsNative() {
		return errors.New("swapping to a trade asset of a native coin is not allowed")
	}

	if target.IsSecuredAsset() && target.GetLayer1Asset().IsNative() {
		return errors.New("swapping to a secured asset of a native coin is not allowed")
	}

	var sourceCoin common.Coin
	if len(msg.Tx.Coins) > 0 {
		sourceCoin = msg.Tx.Coins[0]
	}

	if msg.IsLegacyStreaming() {
		pausedStreaming := h.mgr.Keeper().GetConfigInt64(ctx, constants.StreamingSwapPause)
		if pausedStreaming > 0 {
			return fmt.Errorf("streaming swaps are paused")
		}

		// if either source or target in ragnarok, streaming is not allowed
		for _, asset := range []common.Asset{sourceCoin.Asset, target} {
			key := "RAGNAROK-" + asset.MimirString()
			ragnarok, err := h.mgr.Keeper().GetMimir(ctx, key)
			if err == nil && ragnarok > 0 {
				return fmt.Errorf("streaming swaps disabled on ragnarok asset %s", asset)
			}
		}

		swp := msg.GetStreamingSwap()
		if h.mgr.Keeper().StreamingSwapExists(ctx, msg.Tx.ID) {
			var err error
			swp, err = h.mgr.Keeper().GetStreamingSwap(ctx, msg.Tx.ID)
			if err != nil {
				ctx.Logger().Error("fail to fetch streaming swap", "error", err)
				return err
			}
		}

		if (swp.Quantity > 0 && swp.IsDone()) || swp.In.GTE(swp.Deposit) {
			// check both swap count and swap in vs deposit to cover all basis
			return fmt.Errorf("streaming swap is completed, cannot continue to swap again")
		}

		if swp.Count > 0 {
			// end validation early, as synth TVL caps are not applied to streaming
			// swaps. This is to ensure that streaming swaps don't get interrupted
			// and cause a partial fulfillment, which would cause issues for
			// internal streaming swaps for savers and loans.
			return nil
		} else {
			// first swap we check the entire swap amount (not just the
			// sub-swap amount) to ensure the value of the entire has TVL/synth
			// room
			sourceCoin.Amount = swp.Deposit
		}
	}

	if target.IsSyntheticAsset() {
		// the following is only applicable for mainnet
		totalLiquidityRUNE, err := h.getTotalLiquidityRUNE(ctx)
		if err != nil {
			return ErrInternal(err, "fail to get total liquidity RUNE")
		}

		// total liquidity RUNE after current add liquidity
		if len(msg.Tx.Coins) > 0 {
			// calculate rune value on incoming swap, and add to total liquidity.
			runeVal := sourceCoin.Amount
			if !sourceCoin.IsRune() {
				var pool Pool
				pool, err = h.mgr.Keeper().GetPool(ctx, sourceCoin.Asset.GetLayer1Asset())
				if err != nil {
					return ErrInternal(err, "fail to get pool")
				}
				runeVal = pool.AssetValueInRune(sourceCoin.Amount)
			}
			totalLiquidityRUNE = totalLiquidityRUNE.Add(runeVal)
		}
		maximumLiquidityRune, err := h.mgr.Keeper().GetMimir(ctx, constants.MaximumLiquidityRune.String())
		if maximumLiquidityRune < 0 || err != nil {
			maximumLiquidityRune = h.mgr.GetConstants().GetInt64Value(constants.MaximumLiquidityRune)
		}
		if maximumLiquidityRune > 0 {
			if totalLiquidityRUNE.GT(cosmos.NewUint(uint64(maximumLiquidityRune))) {
				return errAddLiquidityRUNEOverLimit
			}
		}

		// fail validation if synth supply is already too high, relative to pool depth
		// do a simulated swap to see how much of the target synth the network
		// will need to mint and check if that amount exceeds limits
		targetAmount, runeAmount := cosmos.ZeroUint(), cosmos.ZeroUint()
		var swapper Swapper
		swapper, err = GetSwapper(h.mgr.GetVersion())
		if err == nil {
			if sourceCoin.IsRune() {
				runeAmount = sourceCoin.Amount
			} else {
				// asset --> rune swap
				sourceAssetPool := sourceCoin.Asset
				if sourceAssetPool.IsSyntheticAsset() {
					sourceAssetPool = sourceAssetPool.GetLayer1Asset()
				}
				var sourcePool Pool
				sourcePool, err = h.mgr.Keeper().GetPool(ctx, sourceAssetPool)
				if err != nil {
					ctx.Logger().Error("fail to fetch pool for swap simulation", "error", err)
				} else {
					runeAmount = swapper.CalcAssetEmission(sourcePool.BalanceAsset, sourceCoin.Amount, sourcePool.BalanceRune)
				}
			}
			// rune --> synth swap
			var targetPool Pool
			targetPool, err = h.mgr.Keeper().GetPool(ctx, target.GetLayer1Asset())
			if err != nil {
				ctx.Logger().Error("fail to fetch pool for swap simulation", "error", err)
			} else {
				targetAmount = swapper.CalcAssetEmission(targetPool.BalanceRune, runeAmount, targetPool.BalanceAsset)
			}
		}
		err = isSynthMintPaused(ctx, h.mgr, target, targetAmount)
		if err != nil {
			return err
		}

		ensureLiquidityNoLargerThanBond := h.mgr.GetConstants().GetBoolValue(constants.StrictBondLiquidityRatio)
		if ensureLiquidityNoLargerThanBond {
			// If source and target are synthetic assets there is no net
			// liquidity gain (RUNE is just moved from pool A to pool B), so
			// skip this check
			if !sourceCoin.Asset.IsSyntheticAsset() && atTVLCap(ctx, common.NewCoins(sourceCoin), h.mgr) {
				return errAddLiquidityRUNEMoreThanBond
			}
		}
	}

	return nil
}

func (h SwapHandler) handleWithEmit(ctx cosmos.Context, msg MsgSwap) (*cosmos.Result, cosmos.Uint, error) {
	result, emit, _, err := h.handleWithSwapResult(ctx, msg)
	return result, emit, err
}

func (h SwapHandler) handleWithSwapResult(ctx cosmos.Context, msg MsgSwap) (*cosmos.Result, cosmos.Uint, cosmos.Uint, error) {
	logFields := []interface{}{
		"request tx hash", msg.Tx.ID,
		"index", msg.Index,
		"source", msg.Tx.Coins[0].String(),
		"target asset", msg.TargetAsset,
		"signer", msg.Signer.String(),
		"swap type", msg.SwapType.String(),
		"stream quantity", msg.StreamQuantity,
		"stream interval", msg.StreamInterval,
		"trade target", msg.TradeTarget.String(),
	}

	if msg.State != nil {
		logFields = append(
			logFields,
			"interval", msg.State.Interval,
			"quantity", msg.State.Quantity,
			"count", msg.State.Count,
			"deposit", msg.State.Deposit.String(),
			"in", msg.State.In.String(),
			"out", msg.State.Out.String(),
			"ttl", msg.State.Ttl,
			"last_height", msg.State.LastHeight,
		)
	}

	ctx.Logger().Info("receive MsgSwap", logFields...)
	// Validate the destination network before executing the swap.
	if !msg.Destination.IsCurrentNetwork(msg.Destination.GetChain()) {
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("address(%s) is not same network", msg.Destination)
	}

	synthVirtualDepthMult, err := h.mgr.Keeper().GetMimir(ctx, constants.VirtualMultSynthsBasisPoints.String())
	if synthVirtualDepthMult < 1 || err != nil {
		synthVirtualDepthMult = h.mgr.GetConstants().GetInt64Value(constants.VirtualMultSynthsBasisPoints)
	}

	dexAgg := ""
	dexAggTargetAsset := ""
	if len(msg.Aggregator) > 0 {
		dexAgg, err = FetchDexAggregator(msg.TargetAsset.Chain, msg.Aggregator)
		if err != nil {
			return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
		}
	}
	dexAggTargetAsset = msg.AggregatorTargetAddress

	swapper, err := GetSwapper(h.mgr.Keeper().GetVersion())
	if err != nil {
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}

	swp := msg.GetStreamingSwap()

	// for advanced swap queue, copy the current count from msg.State
	if msg.State != nil {
		// For advanced queue swaps, execution state is tracked in msg.State.
		// Keep StreamingSwap fields in sync so downstream logic (for example,
		// limit checks with outbound-fee allocation) uses the actual sub-swap state.
		swp.Quantity = msg.State.Quantity
		swp.Interval = msg.State.Interval
		swp.Deposit = msg.State.Deposit
		swp.In = msg.State.In
		swp.Out = msg.State.Out
		swp.Count = msg.State.Count
	}

	// Parsed up here so sub-swap sizing in getMaxSwapQuantity can honor the
	// affiliate's dynamic floor; also used below for swap accounting.
	mem, parseMemoErr := ParseMemoWithTHORNamesCached(ctx, h.mgr.Keeper(), msg.Tx.Memo)
	var swapAffiliates []string
	var swapAffiliateBps []cosmos.Uint
	if parseMemoErr == nil {
		swapAffiliates = mem.GetAffiliates()
		swapAffiliateBps = mem.GetAffiliatesBasisPoints()
	}

	if filled, emit, swapErr := h.mgr.StableReserveManager().TrySwap(ctx, h.mgr, msg); swapErr != nil {
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), swapErr
	} else if filled {
		return &cosmos.Result{}, emit, msg.Tx.Coins[0].Amount, nil
	}

	// Only create streaming swap records for actual streaming swaps (not limit swaps)
	if msg.IsLegacyStreaming() && !msg.IsLimitSwap() {
		if h.mgr.Keeper().StreamingSwapExists(ctx, msg.Tx.ID) {
			swp, err = h.mgr.Keeper().GetStreamingSwap(ctx, msg.Tx.ID)
			if err != nil {
				ctx.Logger().Error("fail to fetch streaming swap", "error", err)
				return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
			}
		}

		// for first swap only, override interval and quantity (if needed)
		if swp.Count == 0 {
			// ensure interval is never larger than max length, override if so
			maxLength := h.mgr.Keeper().GetConfigInt64(ctx, constants.StreamingSwapMaxLength)
			if uint64(maxLength) < swp.Interval {
				swp.Interval = uint64(maxLength)
			}

			sourceAsset := msg.Tx.Coins[0].Asset
			targetAsset := msg.TargetAsset
			var maxSwapQuantity uint64
			maxSwapQuantity, err = getMaxSwapQuantity(ctx, h.mgr, sourceAsset, targetAsset, swp, swapAffiliates, swapAffiliateBps)
			if err != nil {
				return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
			}
			if swp.Quantity == 0 || swp.Quantity > maxSwapQuantity {
				swp.Quantity = maxSwapQuantity
			}
		}
		h.mgr.Keeper().SetStreamingSwap(ctx, swp)
		// hijack the inbound amount
		// NOTE: its okay if the amount is zero. The swap will fail as it
		// should, which will cause the swap queue manager later to send out
		// the In/Out amounts accordingly
		msg.Tx.Coins[0].Amount, msg.TradeTarget = swp.NextSize()
	}

	emit, effectiveInput, swapEvents, swapErr := swapper.Swap(
		ctx,
		h.mgr.Keeper(),
		msg.Tx,
		msg.TargetAsset,
		msg.Destination,
		msg.TradeTarget,
		dexAgg,
		dexAggTargetAsset,
		msg.AggregatorTargetLimit,
		swp,
		synthVirtualDepthMult,
		h.mgr,
		swapAffiliates, swapAffiliateBps,
	)
	if swapErr != nil {
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), swapErr
	}

	// Track the final emit value to return
	finalEmit := emit

	affColAddress, err := h.mgr.Keeper().GetModuleAddress(AffiliateCollectorName)
	if err != nil {
		ctx.Logger().Error("failed to retrieve AffiliateCollector module address", "error", err)
	}

	// V1 delivers collector output here in the handler's cached context.
	// V2 accumulates output and delivers it once during settlement.
	collectorConversion := affiliateCollectorConversionName(h.mgr.Keeper(), msg) != ""
	collectorPayout := isPreferredAssetCollectorSwap(h.mgr.Keeper(), msg)
	if (collectorConversion || collectorPayout) && msg.IsV1() {
		if err = deliverAffiliateCollectorOutput(ctx, h.mgr, msg, emit); err != nil {
			return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
		}
	}

	// Credit dynamic fee tracking for eligible affiliates. Per-leg, with
	// RUNE→TOR conversion so the closed-loop gradient signal is not corrupted
	// by RUNE price moves.
	creditDynamicFeeForLegs(ctx, h.mgr, swapEvents, swapAffiliates, swapAffiliateBps)

	stableReserveInternalSettled := false
	if settled, settleErr := settleStableReserveInternalSwap(ctx, h.mgr, msg, emit); settleErr != nil {
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), settleErr
	} else {
		stableReserveInternalSettled = settled
	}

	// Check if swap to a synth would cause synth supply to exceed
	// MaxSynthPerPoolDepth cap
	// Ignore caps when the swap is streaming (its checked at the start of the
	// stream, not during)
	if msg.TargetAsset.IsSyntheticAsset() && !msg.IsLegacyStreaming() {
		err = isSynthMintPaused(ctx, h.mgr, msg.TargetAsset, emit)
		if err != nil {
			return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
		}
	}

	// Only update streaming swap records for actual streaming swaps (not limit swaps)
	if msg.IsLegacyStreaming() && !msg.IsLimitSwap() {
		// only increment In/Out if we have a successful swap
		swp.In = swp.In.Add(msg.Tx.Coins[0].Amount)
		swp.Out = swp.Out.Add(emit)
		h.mgr.Keeper().SetStreamingSwap(ctx, swp)
		if !swp.IsLastSwap() {
			// exit early so we don't execute follow-on handlers mid streaming swap. if this
			// is the last swap execute the follow-on handlers as swap count is incremented in
			// the swap queue manager
			return &cosmos.Result{}, finalEmit, effectiveInput, nil
		}
		emit = swp.Out
		finalEmit = swp.Out
	}

	// Internal reserve swaps and collector conversions use dedicated settlement
	// paths, so skip the downstream handlers selected by the memo.
	if stableReserveInternalSettled || collectorConversion {
		return &cosmos.Result{}, finalEmit, effectiveInput, nil
	}

	// this is a preferred asset swap, so return early since there is no need to call any
	// downstream handlers
	memo := msg.Tx.Memo
	fromAdd := msg.Tx.FromAddress
	if strings.HasPrefix(memo, PreferredAssetSwapMemoPrefix) && fromAdd.Equals(affColAddress) {
		return &cosmos.Result{}, finalEmit, effectiveInput, nil
	}

	if parseMemoErr != nil {
		ctx.Logger().Error("swap handler failed to parse memo", "memo", msg.Tx.Memo, "error", parseMemoErr)
		return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), parseMemoErr
	}
	if mem.GetType() == TxAdd {
		m, ok := mem.(AddLiquidityMemo)
		if !ok {
			return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("fail to cast add liquidity memo")
		}
		m.Asset = fuzzyAssetMatch(ctx, h.mgr.Keeper(), m.Asset)
		msg.Tx.Coins = common.NewCoins(common.NewCoin(m.Asset, emit))
		obTx := ObservedTx{Tx: msg.Tx}
		msg, err := getMsgAddLiquidityFromMemo(ctx, m, obTx, msg.Signer)
		if err != nil {
			return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
		}
		handler := NewAddLiquidityHandler(h.mgr)
		_, err = handler.Run(ctx, msg)
		if err != nil {
			ctx.Logger().Error("swap handler failed to add liquidity", "error", err)
			return nil, cosmos.ZeroUint(), cosmos.ZeroUint(), err
		}
	}
	return &cosmos.Result{}, finalEmit, effectiveInput, nil
}

// getTotalLiquidityRUNE we have in all pools
func (h SwapHandler) getTotalLiquidityRUNE(ctx cosmos.Context) (cosmos.Uint, error) {
	pools, err := h.mgr.Keeper().GetPools(ctx)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get pools from data store: %w", err)
	}
	total := cosmos.ZeroUint()
	for _, p := range pools {
		// ignore suspended pools
		if p.Status == PoolSuspended {
			continue
		}
		if p.Asset.IsSyntheticAsset() {
			continue
		}
		if p.Asset.IsDerivedAsset() {
			continue
		}
		total = total.Add(p.BalanceRune)
	}
	return total, nil
}
