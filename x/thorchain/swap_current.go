package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type SwapperImpl struct{}

func newSwapper() Swapper {
	return &SwapperImpl{}
}

// validateMessage is trying to validate the legitimacy of the incoming message and decide whether THORNode can handle it
func (s *SwapperImpl) validateMessage(tx common.Tx, target common.Asset, destination common.Address) error {
	if err := tx.Valid(); err != nil {
		return err
	}
	if target.IsEmpty() {
		return errors.New("target is empty")
	}
	if destination.IsEmpty() {
		return errors.New("destination is empty")
	}
	if tx.Coins[0].Asset.IsTradeAsset() && !target.IsTradeAsset() && !target.IsRune() {
		return errors.New("swaps from trade asset to L1 incur slip, use trade-")
	}
	if target.IsTradeAsset() && !tx.Coins[0].Asset.IsTradeAsset() && !tx.Coins[0].IsRune() {
		return errors.New("swaps from L1 to trade asset incur slip, use trade+")
	}

	return nil
}

func (s *SwapperImpl) Swap(ctx cosmos.Context,
	keeper keeper.Keeper,
	tx common.Tx,
	target common.Asset,
	destination common.Address,
	swapTarget cosmos.Uint,
	dexAgg string,
	dexAggTargetAsset string,
	dexAggLimit *cosmos.Uint,
	swp StreamingSwap,
	synthVirtualDepthMult int64, mgr Manager,
	affiliates []string, affiliateBps []cosmos.Uint,
) (cosmos.Uint, cosmos.Uint, []*EventSwap, error) {
	var swapEvents []*EventSwap

	if err := s.validateMessage(tx, target, destination); err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), swapEvents, err
	}
	source := tx.Coins[0].Asset
	effectiveInput := tx.Coins[0].Amount

	if source.IsSyntheticAsset() {
		burnHeight := mgr.Keeper().GetConfigInt64(ctx, constants.BurnSynths)
		// The BurnSynths mimir disables synth-source swaps, but it must NOT block
		// the synth->L1 swap that pays out a saver whose pool is being ragnaroked.
		// WithdrawLiquidityHandler.validate relaxes the matching gate for ragnarok
		// and queues this swap; without the same exception here the queued saver
		// payout would be rejected, deleting the saver claim with no outbound.
		if burnHeight > 0 && ctx.BlockHeight() > burnHeight &&
			!mgr.Keeper().IsRagnarok(ctx, []common.Asset{source.GetLayer1Asset()}) {
			return cosmos.ZeroUint(), effectiveInput, swapEvents, fmt.Errorf("burning synthetics has been disabled")
		}
	}
	if target.IsSyntheticAsset() {
		mintHeight := mgr.Keeper().GetConfigInt64(ctx, constants.MintSynths)
		if mintHeight > 0 && ctx.BlockHeight() > mintHeight {
			return cosmos.ZeroUint(), effectiveInput, swapEvents, fmt.Errorf("minting synthetics has been disabled")
		}
	}

	if !destination.IsNoop() && !destination.IsChain(target.GetChain()) {
		return cosmos.ZeroUint(), effectiveInput, swapEvents, fmt.Errorf("destination address is not a valid %s address", target.GetChain())
	}
	if source.Equals(target) {
		return cosmos.ZeroUint(), effectiveInput, swapEvents, fmt.Errorf("cannot swap from %s --> %s, assets match", source, target)
	}

	isDoubleSwap := !source.IsRune() && !target.IsRune()
	stableSwap := isDoubleSwap && isStableToStable(ctx, keeper, source, target)
	if isDoubleSwap {
		var swapErr error
		var swapEvt *EventSwap
		var amt cosmos.Uint
		// Here we use a swapTarget of 0 because the target is for the next swap asset in a double swap
		amt, effectiveInput, swapEvt, swapErr = s.swapOne(ctx, mgr, tx, common.RuneAsset(), destination, cosmos.ZeroUint(), synthVirtualDepthMult, stableSwap, affiliates, affiliateBps)
		if swapErr != nil {
			return cosmos.ZeroUint(), effectiveInput, swapEvents, swapErr
		}
		tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), amt)}
		tx.Gas = nil
		swapEvents = append(swapEvents, swapEvt)
	}
	assetAmount, swapInput, swapEvt, swapErr := s.swapOne(ctx, mgr, tx, target, destination, swapTarget, synthVirtualDepthMult, stableSwap, affiliates, affiliateBps)
	if swapErr != nil {
		return cosmos.ZeroUint(), effectiveInput, swapEvents, swapErr
	}
	if !isDoubleSwap {
		effectiveInput = swapInput
	}
	swapEvents = append(swapEvents, swapEvt)
	if !swapTarget.IsZero() {
		checkAmount := assetAmount
		// deduct L1 outbound fee to ensure user receives their limit amount after fees
		if !target.IsNative() {
			outboundFee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, target, false)
			if err == nil && !outboundFee.IsZero() {
				feeToDeduct := outboundFee
				// for streaming swaps, each sub-swap bears proportional share of the fee
				if swp.Quantity > 0 {
					feeToDeduct = outboundFee.QuoUint64(swp.Quantity)
				}
				checkAmount = common.SafeSub(assetAmount, feeToDeduct)
			}
		}
		if checkAmount.LT(swapTarget) {
			// **NOTE** this error string is utilized by the adv swap queue manager to
			// catch the error. DO NOT change this error string without updating
			// the adv swap queue manager as well
			return cosmos.ZeroUint(), effectiveInput, swapEvents, fmt.Errorf("emit asset %s less than price limit %s", checkAmount, swapTarget)
		}
	}
	// emit asset is zero
	if assetAmount.IsZero() {
		return cosmos.ZeroUint(), effectiveInput, swapEvents, errors.New("zero emit asset")
	}

	// Thanks to CacheContext, the swap event can be emitted before handling outbounds,
	// since if there's a later error the event emission will not take place.
	for _, evt := range swapEvents {
		if swp.Quantity > evt.StreamingSwapQuantity {
			evt.StreamingSwapQuantity = swp.Quantity
			evt.StreamingSwapCount = swp.Count + 1 // first swap count is "zero"
		} else {
			evt.StreamingSwapQuantity = 1
			evt.StreamingSwapCount = 1
		}
		if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("fail to emit swap event", "error", err)
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "swap", "count"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("pool", evt.Pool.String())},
		)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "swap", "slip"},
			telem(evt.SwapSlip),
			[]metrics.Label{telemetry.NewLabel("pool", evt.Pool.String())},
		)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "swap", "liquidity_fee"},
			telem(evt.LiquidityFeeInRune),
			[]metrics.Label{telemetry.NewLabel("pool", evt.Pool.String())},
		)

		volume, err := keeper.GetVolume(ctx, evt.Pool)
		if err != nil {
			volume = types.NewVolume(evt.Pool)
		}

		if evt.EmitAsset.Asset.GetLayer1Asset().Equals(evt.Pool) {
			volume.ChangeAsset = volume.ChangeAsset.Add(evt.EmitAsset.Amount)
			volume.ChangeRune = volume.ChangeRune.Add(evt.InTx.Coins[0].Amount)
		} else {
			volume.ChangeAsset = volume.ChangeAsset.Add(evt.InTx.Coins[0].Amount)
			volume.ChangeRune = volume.ChangeRune.Add(evt.EmitAsset.Amount)
		}

		err = keeper.SetVolume(ctx, volume)
		if err != nil {
			ctx.Logger().Error("fail to save volume", "error", err)
		}
	}

	if !destination.IsNoop() {
		toi := TxOutItem{
			Chain:                 target.GetChain(),
			InHash:                tx.ID,
			ToAddress:             destination,
			Coin:                  common.NewCoin(target, assetAmount),
			Aggregator:            dexAgg,
			AggregatorTargetAsset: dexAggTargetAsset,
			AggregatorTargetLimit: dexAggLimit,
		}

		// streaming swap outbounds are handled in the swap queue manager
		// all swaps are managed by the swap queue manager for the advanced swap queue
		// Skip outbound creation when:
		// 1. It's a streaming swap (swp.Valid() == nil)
		// 2. OR advanced swap queue is enabled AND we're not in simulation mode
		advSwapQueueEnabled := keeper.AdvSwapQueueEnabled(ctx)
		isSimMode := isSimulationMode(ctx)
		streamingSwap := swp.Valid() == nil

		// Defer collector conversions and preferred-asset payouts to settlement,
		// which handles module transfers and collector accounting together.
		collectorAddress := common.Address(keeper.GetModuleAccAddress(AffiliateCollectorName).String())
		collectorOutput := (target.IsRune() && destination.Equals(collectorAddress)) ||
			(tx.FromAddress.Equals(collectorAddress) && strings.HasPrefix(tx.Memo, PreferredAssetSwapMemoPrefix+"-"))
		if collectorOutput || streamingSwap || (advSwapQueueEnabled && !isSimMode) {
			// Skip - outbound will be created by swap queue manager or settleSwap
		} else {
			// Create outbound for regular swaps when advanced queue is disabled
			// OR when in simulation mode (to generate fee events for quotes)
			ok, err := mgr.TxOutStore().TryAddTxOutItem(ctx, mgr, toi, swapTarget)
			if err != nil {
				return assetAmount, effectiveInput, swapEvents, ErrInternal(err, "fail to add outbound tx")
			}
			if !ok {
				return assetAmount, effectiveInput, swapEvents, errFailAddOutboundTx
			}
		}
	}

	return assetAmount, effectiveInput, swapEvents, nil
}

func (s *SwapperImpl) swapOne(ctx cosmos.Context,
	mgr Manager, tx common.Tx,
	target common.Asset,
	destination common.Address,
	swapTarget cosmos.Uint,
	synthVirtualDepthMult int64,
	stableOverride bool,
	affiliates []string, affiliateBps []cosmos.Uint,
) (amt cosmos.Uint, input cosmos.Uint, evt *EventSwap, swapErr error) {
	tradeAccountsEnabled := mgr.Keeper().GetConfigInt64(ctx, constants.TradeAccountsEnabled)
	tradeAccountsDepositEnabled := mgr.Keeper().GetConfigInt64(ctx, constants.TradeAccountsDepositEnabled)

	source := tx.Coins[0].Asset
	amount := tx.Coins[0].Amount
	input = amount

	ctx.Logger().Info("swapping", "from", tx.FromAddress, "coins", tx.Coins[0], "target", target, "to", destination)

	// Set asset to our pool asset
	var minSlipAsset common.Asset
	if source.IsRune() {
		minSlipAsset = target
	} else {
		minSlipAsset = source
	}
	poolAsset := minSlipAsset.GetLayer1Asset()

	if source.IsTradeAsset() {
		if tradeAccountsEnabled <= 0 {
			return cosmos.ZeroUint(), input, evt, fmt.Errorf("trade accounts are disabled")
		}
		fromAcc, err := cosmos.AccAddressFromBech32(tx.FromAddress.String())
		if err != nil {
			return cosmos.ZeroUint(), input, evt, ErrInternal(err, "fail to parse from address")
		}
		amount, err = mgr.TradeAccountManager().Withdrawal(ctx, source, amount, fromAcc, common.NoAddress, tx.ID)
		if err != nil {
			return cosmos.ZeroUint(), input, evt, ErrInternal(err, "fail to withdraw from trade")
		}
		input = amount
	}

	if source.IsSecuredAsset() {
		fromAcc, err := cosmos.AccAddressFromBech32(tx.FromAddress.String())
		if err != nil {
			return cosmos.ZeroUint(), input, evt, ErrInternal(err, "fail to parse from address")
		}
		withdrawAmount, err := mgr.SecuredAssetManager().Withdraw(ctx, source, amount, fromAcc, common.NoAddress, tx.ID)
		if err != nil {
			return cosmos.ZeroUint(), input, evt, ErrInternal(err, "fail to withdraw from secured asset")
		}
		amount = withdrawAmount.Amount
		input = amount
	}

	if target.IsTradeAsset() && tradeAccountsDepositEnabled <= 0 {
		return cosmos.ZeroUint(), input, evt, fmt.Errorf("trade accounts deposits are disabled")
	}

	swapEvt := NewEventSwap(
		poolAsset,
		swapTarget,
		cosmos.ZeroUint(),
		cosmos.ZeroUint(),
		cosmos.ZeroUint(),
		tx,
		common.NoCoin,
		cosmos.ZeroUint(),
	)

	// Update swap event input with source and amount details,
	// notably for if the Trade/Secured amount withdrawn is less than the transaction-specified amount.
	// For streaming swaps, InTx already only represents the sub-swap amount, not the original inbound.
	swapEvt.InTx.Coins = common.NewCoins(common.NewCoin(source, amount))

	if poolAsset.IsDerivedAsset() {
		// regenerate derived virtual pool
		mgr.NetworkMgr().SpawnDerivedAsset(ctx, poolAsset, mgr)
	}

	// Check if pool exists
	keeper := mgr.Keeper()
	if !keeper.PoolExist(ctx, poolAsset) {
		err := fmt.Errorf("pool %s doesn't exist", poolAsset)
		return cosmos.ZeroUint(), input, evt, err
	}

	pool, err := keeper.GetPool(ctx, poolAsset)
	if err != nil {
		return cosmos.ZeroUint(), input, evt, ErrInternal(err, fmt.Sprintf("fail to get pool(%s)", poolAsset))
	}
	// sanity check: ensure we're never swapping with the vault
	// (technically is actually the yield bearing synth vault)
	if pool.Asset.IsSyntheticAsset() {
		return cosmos.ZeroUint(), input, evt, ErrInternal(err, fmt.Sprintf("dev error: swapping with a vault(%s) is not allowed", pool.Asset))
	}
	synthSupply := keeper.GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)

	// pool must be available unless source is synthetic
	// synths may be redeemed regardless of pool status
	if !source.IsSyntheticAsset() && !pool.IsAvailable() {
		return cosmos.ZeroUint(), input, evt, fmt.Errorf("pool(%s) is not available", pool.Asset)
	}

	// Get our X, x, Y values
	var X, Y cosmos.Uint
	if source.IsRune() {
		X = pool.BalanceRune
		Y = pool.BalanceAsset
	} else {
		Y = pool.BalanceRune
		X = pool.BalanceAsset
	}
	x := amount

	// give virtual pool depth if we're swapping with a synthetic asset
	if source.IsSyntheticAsset() || target.IsSyntheticAsset() {
		X = common.GetUncappedShare(cosmos.NewUint(uint64(synthVirtualDepthMult)), cosmos.NewUint(10_000), X)
		Y = common.GetUncappedShare(cosmos.NewUint(uint64(synthVirtualDepthMult)), cosmos.NewUint(10_000), Y)
	}

	// check our X,x,Y values are valid
	if x.IsZero() {
		return cosmos.ZeroUint(), input, evt, errSwapFailInvalidAmount
	}
	if X.IsZero() || Y.IsZero() {
		return cosmos.ZeroUint(), input, evt, errSwapFailInvalidBalance
	}

	swapSlipBps := s.CalcSwapSlip(X, x)
	swapEvt.PoolSlip = swapSlipBps

	// the non-RUNE asset is used to determine which min slip to use
	minSlipBps := getMinSlipBps(ctx, mgr.Keeper(), tx.FromAddress, minSlipAsset, stableOverride, affiliates, affiliateBps, source, target, mgr.DynamicFeeMgr())

	var (
		emitAssets   cosmos.Uint
		liquidityFee cosmos.Uint
	)
	emitAssets, liquidityFee, swapEvt.SwapSlip = s.GetSwapCalc(X, x, Y, swapSlipBps, minSlipBps)
	emitAssets = cosmos.RoundToDecimal(emitAssets, pool.Decimals)
	swapEvt.EmitAsset = common.NewCoin(target, emitAssets)
	swapEvt.LiquidityFee = liquidityFee

	// do THORNode have enough balance to swap?
	if emitAssets.GTE(Y) {
		return cosmos.ZeroUint(), input, evt, errSwapFailNotEnoughBalance
	}

	// Log the effective input amount after Trade/Secured withdrawals, which may
	// differ from the original transaction coin amount.
	ctx.Logger().Info("pre swap", "pool", pool.Asset, "rune", pool.BalanceRune, "asset", pool.BalanceAsset, "lp units", pool.LPUnits, "synth units", pool.SynthUnits, "amount", amount)

	// Burning of input synth or derived pool input (Asset or RUNE).
	if source.IsSyntheticAsset() || pool.Asset.IsDerivedAsset() {
		burnCoin := tx.Coins[0]
		if err := mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(burnCoin)); err != nil {
			ctx.Logger().Error("fail to move coins during swap", "error", err)
			return cosmos.ZeroUint(), input, evt, err
		} else if err := mgr.Keeper().BurnFromModule(ctx, ModuleName, burnCoin); err != nil {
			ctx.Logger().Error("fail to burn coins during swap", "error", err)
		} else {
			burnEvt := NewEventMintBurn(BurnSupplyType, burnCoin.Asset.Native(), burnCoin.Amount, "swap")
			if err := mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
				ctx.Logger().Error("fail to emit burn event", "error", err)
			}
		}
	}

	// Minting of output synth or derived pool output (Asset or RUNE).
	if (target.IsSyntheticAsset() || pool.Asset.IsDerivedAsset()) &&
		!emitAssets.IsZero() {
		// If the source isn't RUNE, the target should be RUNE.
		mintCoin := common.NewCoin(target, emitAssets)
		if err := mgr.Keeper().MintToModule(ctx, ModuleName, mintCoin); err != nil {
			ctx.Logger().Error("fail to mint coins during swap", "error", err)
			return cosmos.ZeroUint(), input, evt, err
		} else {
			mintEvt := NewEventMintBurn(MintSupplyType, mintCoin.Asset.Native(), mintCoin.Amount, "swap")
			if err := mgr.EventMgr().EmitEvent(ctx, mintEvt); err != nil {
				ctx.Logger().Error("fail to emit mint event", "error", err)
			}

			if err := mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(mintCoin)); err != nil {
				ctx.Logger().Error("fail to move coins during swap", "error", err)
				return cosmos.ZeroUint(), input, evt, err
			}
		}
	}

	// Use pool fields here rather than X and Y as synthVirtualDepthMult could affect X and Y.
	// Only alter BalanceAsset when the non-RUNE asset isn't a synth.
	if source.IsRune() {
		pool.BalanceRune = pool.BalanceRune.Add(x)
		if !target.IsSyntheticAsset() {
			pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, emitAssets)
		}
	} else {
		// The target should be RUNE.
		pool.BalanceRune = common.SafeSub(pool.BalanceRune, emitAssets)
		if !source.IsSyntheticAsset() {
			pool.BalanceAsset = pool.BalanceAsset.Add(x)
		}
	}
	if source.IsSyntheticAsset() || target.IsSyntheticAsset() {
		synthSupply = keeper.GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		pool.CalcUnits(synthSupply)
	}

	// Now that pool depths have been adjusted to post-swap, determine LiquidityFeeInRune.
	if target.IsRune() {
		// Because the output asset is RUNE, liquidity Fee is already in RUNE.
		swapEvt.LiquidityFeeInRune = swapEvt.LiquidityFee
	} else {
		// Momentarily deduct the liquidity fee for RuneDisbursementForAssetAdd.
		pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, swapEvt.LiquidityFee)
		swapEvt.LiquidityFeeInRune = pool.RuneDisbursementForAssetAdd(swapEvt.LiquidityFee)
		// Restore the BalanceAsset which is constant-depths-product swapped for the LiquidityFeeInRune.
		pool.BalanceAsset = pool.BalanceAsset.Add(swapEvt.LiquidityFee)
	}

	if !pool.Asset.IsDerivedAsset() {
		// Deduct LiquidityFeeInRune from the pool's RUNE depth and send it to the Reserve Module to be system income.
		// (So that the liquidity fee isn't used for later swaps in the same block.)
		if !swapEvt.LiquidityFeeInRune.IsZero() {
			pool.BalanceRune = common.SafeSub(pool.BalanceRune, swapEvt.LiquidityFeeInRune)
			liqFeeCoin := common.NewCoin(common.RuneAsset(), swapEvt.LiquidityFeeInRune)

			targetModule := ReserveName
			if pool.Asset.IsTCY() {
				targetModule = TCYClaimingName
			}

			if err := mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, targetModule, common.NewCoins(liqFeeCoin)); err != nil {
				ctx.Logger().Error("fail to move liquidity fee RUNE during swap", "module", targetModule, "error", err)
				return cosmos.ZeroUint(), input, evt, err
			}

			// Only add to liquidity fees if not TCY
			if !pool.Asset.IsTCY() {
				if err := keeper.AddToLiquidityFees(ctx, pool.Asset, swapEvt.LiquidityFeeInRune); err != nil {
					return cosmos.ZeroUint(), input, evt, fmt.Errorf("fail to add to liquidity fees: %w", err)
				}
				// Skip REVSHARE attribution for protocol-internal affiliate-fee
				// conversion swaps. affiliateSwapToRune schedules these to
				// convert a user-side affiliate cut into RUNE, routing to one
				// of two destinations:
				//   - the AffiliateCollector module address (preferred-asset case), or
				//   - the resolved thorname's THORChain alias, or owner if no
				//     alias is set (non-PA case).
				// Their fees come from a fee-bearing user-issued swap and
				// should not double-credit the affiliate. The non-PA case is
				// further gated by `target == RUNE` so a user-issued swap to
				// RUNE with affiliate=X and destination=X.alias|X.owner (a rare
				// and effectively self-payment scenario) is the only false-
				// negative; normal user-to-RUNE swaps stay attributable.
				if attributeThorname, ok := resolveFirstAffiliateTHORName(ctx, keeper, tx.Memo); ok {
					skip := false
					if affColAddr, err := keeper.GetModuleAddress(AffiliateCollectorName); err == nil && destination.Equals(affColAddr) {
						skip = true
					}
					if !skip && target.Equals(common.RuneAsset()) {
						if tn, tnErr := keeper.GetTHORName(ctx, attributeThorname); tnErr == nil && !tn.Owner.Empty() {
							if destination.String() == tn.Owner.String() || destination.Equals(tn.GetAlias(common.THORChain)) {
								skip = true
							}
						}
					}
					if !skip {
						if err := keeper.AddAffiliateLiquidityFee(ctx, attributeThorname, swapEvt.LiquidityFeeInRune); err != nil {
							ctx.Logger().Error("fail to attribute affiliate liquidity fee", "thorname", attributeThorname, "error", err)
							// non-fatal — attribution failure must not fail the swap
						}
					}
				}
			}
		}

		// use calculated floor
		if err := keeper.AddToSwapSlip(ctx, pool.Asset, cosmos.NewInt(int64(swapEvt.PoolSlip.Uint64()))); err != nil {
			return cosmos.ZeroUint(), input, evt, fmt.Errorf("fail to add to swap slip: %w", err)
		}
	}

	ctx.Logger().Info("post swap", "pool", pool.Asset, "rune", pool.BalanceRune, "asset", pool.BalanceAsset, "lp units", pool.LPUnits, "synth units", pool.SynthUnits, "emit asset", emitAssets)

	// Even for a Derived Asset pool, set the pool so the txout manager's GetFee for toi.Coin.Asset uses updated balances.
	if err := keeper.SetPool(ctx, pool); err != nil {
		return cosmos.ZeroUint(), input, evt, fmt.Errorf("fail to set pool")
	}

	// if target is trade account, check whether swaps to trade assets are enabled
	if target.IsTradeAsset() {
		if tradeAccountsEnabled <= 0 {
			return cosmos.ZeroUint(), input, evt, fmt.Errorf("trade accounts are disabled")
		}
	}

	// if target is secured asset, check whether swaps to secured assets are enabled
	if target.IsSecuredAsset() {
		if err := mgr.SecuredAssetManager().CheckHalt(ctx); err != nil {
			return cosmos.ZeroUint(), input, evt, err
		}
	}

	// apply swapper clout
	availableClout := swapEvt.LiquidityFeeInRune
	for i, addr := range []common.Address{tx.FromAddress, destination} {
		if addr.IsEmpty() {
			ctx.Logger().Error("dev error: address is empty for clout calculation")
			continue
		}
		clout, err := keeper.GetSwapperClout(ctx, addr)
		if err != nil {
			ctx.Logger().Error("fail to get swapper clout destination address", "error", err)
			continue
		}
		if i == 0 {
			clout.Score = clout.Score.Add(availableClout.QuoUint64(2))
			availableClout = common.SafeSub(availableClout, availableClout.QuoUint64(2))
		} else {
			clout.Score = clout.Score.Add(availableClout)
		}
		if err := keeper.SetSwapperClout(ctx, clout); err != nil {
			ctx.Logger().Error("fail to save swapper clout", "error", err)
		}
	}

	return emitAssets, input, swapEvt, nil
}

// calculate the number of assets sent to the address (includes liquidity fee)
// nolint
func (s *SwapperImpl) CalcAssetEmission(X, x, Y cosmos.Uint) cosmos.Uint {
	// ( x * X * Y ) / ( x + X )^2
	numerator := x.Mul(X).Mul(Y)
	denominator := x.Add(X).Mul(x.Add(X))
	if denominator.IsZero() {
		return cosmos.ZeroUint()
	}
	return numerator.Quo(denominator)
}

// calculate the asset amount to be sent to address using a predefined fee (fee calculated using artificial floor)
// nolint
func (s *SwapperImpl) CalcMaxAssetEmission(X, x, Y, fee cosmos.Uint) cosmos.Uint {
	// (( x * Y ) / ( x + X )) - fee
	numerator := x.Mul(Y)
	denominator := x.Add(X)
	if denominator.IsZero() {
		return cosmos.ZeroUint()
	}
	return common.SafeSub(numerator.Quo(denominator), fee)
}

// CalculateLiquidityFee the fee of the swap
// nolint
func (s *SwapperImpl) CalcLiquidityFee(X, x, Y cosmos.Uint) cosmos.Uint {
	// ( x^2 *  Y ) / ( x + X )^2
	numerator := x.Mul(x).Mul(Y)
	denominator := x.Add(X).Mul(x.Add(X))
	if denominator.IsZero() {
		return cosmos.ZeroUint()
	}
	return numerator.Quo(denominator)
}

// CalcMinLiquidityFee calculates the fee of the swap using min artificial slip floor
// nolint
func (s *SwapperImpl) CalcMinLiquidityFee(X, x, Y, minSlipBps cosmos.Uint) cosmos.Uint {
	// minSlip * ( x  *  Y ) / ( x + X )
	numerator := common.GetSafeShare(minSlipBps, cosmos.NewUint(constants.MaxBasisPts), x.Mul(Y))
	denominator := x.Add(X)
	if denominator.IsZero() {
		return cosmos.ZeroUint()
	}
	return numerator.Quo(denominator)
}

// CalcSwapSlip - calculate the swap slip, expressed in basis points (10000)
// nolint
func (s *SwapperImpl) CalcSwapSlip(Xi, xi cosmos.Uint) cosmos.Uint {
	// Cast to DECs
	xD := cosmos.NewDecFromBigInt(xi.BigInt())
	XD := cosmos.NewDecFromBigInt(Xi.BigInt())
	dec10k := cosmos.NewDec(10000)
	// x / (x + X)
	denD := xD.Add(XD)
	if denD.IsZero() {
		return cosmos.ZeroUint()
	}
	swapSlipD := xD.Quo(denD)                                     // Division with DECs
	swapSlip := swapSlipD.Mul(dec10k)                             // Adds 5 0's
	swapSlipUint := cosmos.NewUint(uint64(swapSlip.RoundInt64())) // Casts back to Uint as Basis Points
	return swapSlipUint
}

// GetSwapCalc returns emission, liquidity fee and slip for a swap
// nolint
func (s *SwapperImpl) GetSwapCalc(X, x, Y, slipBps, minSlipBps cosmos.Uint) (emitAssets, liquidityFee, slip cosmos.Uint) {
	if minSlipBps.GT(slipBps) {
		// adjust calc emission based on artificial floor
		liquidityFee = s.CalcMinLiquidityFee(X, x, Y, minSlipBps)
		emitAssets = s.CalcMaxAssetEmission(X, x, Y, liquidityFee)
		slip = minSlipBps
	} else {
		liquidityFee = s.CalcLiquidityFee(X, x, Y)
		emitAssets = s.CalcAssetEmission(X, x, Y)
		slip = slipBps
	}
	return
}

// resolveFirstAffiliateTHORName parses memo as a SwapMemo and returns the
// first affiliate name if it resolves to an existing, non-expired THORName.
// Returns ("", false) when the memo isn't a swap memo, has no affiliates,
// or the first affiliate is a raw address or an expired/missing thorname.
// Used by the swap fee-recording path to attribute liquidity fees to the
// first memo affiliate for the affiliate revenue share accumulator.
//
// The name is upper-cased so in-memory comparisons and log lines line up
// with the canonical form used by the rev-share accumulator and
// `REVSHARE-%s` mimir keys (both go through `KVStore.GetKey`, which
// upper-cases). THORName lookups are case-insensitive (lower-cased
// base64), so owner resolution is unaffected.
func resolveFirstAffiliateTHORName(ctx cosmos.Context, k keeper.Keeper, memoStr string) (string, bool) {
	parsed, err := ParseMemoWithTHORNames(ctx, k, memoStr)
	if err != nil {
		return "", false
	}
	sm, ok := parsed.(SwapMemo)
	if !ok {
		return "", false
	}
	affiliates := sm.GetAffiliates()
	if len(affiliates) == 0 {
		return "", false
	}
	first := affiliates[0]
	if !k.THORNameExists(ctx, first) {
		return "", false
	}
	// THORNameExists checks active-at-this-height; that's sufficient.
	return strings.ToUpper(first), true
}
