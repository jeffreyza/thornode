package thorchain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type StableReserveMgr struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

func newStableReserveMgr(keeper keeper.Keeper, eventMgr EventManager) *StableReserveMgr {
	return &StableReserveMgr{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (s *StableReserveMgr) EndBlock(ctx cosmos.Context, mgr Manager) error {
	if !s.IsEnabled(ctx) {
		return nil
	}
	if err := s.scheduleDepegRescue(ctx, mgr); err != nil {
		return err
	}
	return s.scheduleRuneDeployment(ctx, mgr)
}

func (s *StableReserveMgr) IsEnabled(ctx cosmos.Context) bool {
	return s.keeper.GetConfigInt64(ctx, constants.StableReserveEnabled) > 0
}

func (s *StableReserveMgr) IsEligibleStable(ctx cosmos.Context, asset common.Asset) (bool, string) {
	// Lock eligibility to external L1 stables. The check runs on the original
	// asset BEFORE any GetLayer1Asset normalization: GetLayer1Asset strips the
	// synth/secured/trade flags, so normalizing first would let a wrapped USDC
	// masquerade as L1 USDC and be filled by the reserve. Rejecting up front keeps
	// synths, secured assets, and trade assets out of every stable reserve path.
	if !asset.IsExternalL1Asset() {
		return false, "non-l1 stable"
	}
	if !s.isTORAnchor(ctx, asset) {
		return false, "non-tor-anchor asset"
	}
	price, err := s.keeper.GetPrice(ctx, asset.Ticker.String())
	if err != nil {
		return false, "missing oracle price"
	}
	toleranceBps := s.keeper.GetConfigInt64(ctx, constants.StableReserveDepegToleranceBps)
	if !stableReserveOracleHealthy(price.Price, toleranceBps) {
		return false, "unhealthy oracle price"
	}
	poolPrice, ok := s.stableReservePoolPrice(ctx, asset)
	if !ok {
		return false, "missing pool price"
	}
	if !stableReserveOracleHealthy(poolPrice, toleranceBps) {
		return false, "unhealthy pool price"
	}
	return true, ""
}

func (s *StableReserveMgr) Deposit(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, contributor common.Address, txID common.TxID) error {
	if !s.IsEnabled(ctx) {
		return fmt.Errorf("stable reserve disabled")
	}
	if amount.IsZero() {
		return fmt.Errorf("stable reserve deposit amount is zero")
	}
	if ok, reason := s.IsEligibleStable(ctx, asset); !ok {
		return errors.New(reason)
	}

	moduleAddr := s.keeper.GetModuleAccAddress(StableReserveName)
	securedMgr := newSecuredAssetMgr(s.keeper, s.eventMgr)
	if _, err := securedMgr.Deposit(ctx, asset, amount, moduleAddr, contributor, txID); err != nil {
		return err
	}

	credit, err := s.keeper.GetStableReserveCredit(ctx, asset, contributor)
	if err != nil {
		return err
	}
	if err = s.keeper.SetStableReserveCredit(ctx, asset, contributor, credit.Add(amount)); err != nil {
		return err
	}

	return s.eventMgr.EmitEvent(ctx, newStableReserveEvent(
		"stable_reserve_deposit",
		"asset", asset.GetLayer1Asset().String(),
		"contributor", contributor.String(),
		"amount", amount.String(),
		"tx_id", txID.String(),
	))
}

func (s *StableReserveMgr) Withdraw(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, contributor common.Address, txID common.TxID) (common.Coin, bool, error) {
	if !s.IsEnabled(ctx) {
		return common.NoCoin, false, fmt.Errorf("stable reserve disabled")
	}
	if amount.IsZero() {
		return common.NoCoin, false, fmt.Errorf("stable reserve withdrawal amount is zero")
	}

	credit, err := s.keeper.GetStableReserveCredit(ctx, asset, contributor)
	if err != nil {
		return common.NoCoin, false, err
	}

	// Withdrawals are strictly same-asset and clamped to what is actually
	// payable: the contributor may request any amount, but it is reduced first to
	// their available credit and then to the module's same-asset inventory. We
	// fill the largest sub-amount both constraints can satisfy and debit credit by
	// exactly what is sent. Credits are not redeemable cross-asset or in RUNE, so
	// once TrySwap or a depeg rescue moves this asset's inventory elsewhere a
	// contributor's credit can become partially or wholly unfillable while their
	// credit balance persists. Reconciling credits with cross-asset rebalancing is
	// a pending design decision (see settleStableReserveInternalSwap).
	moduleAddr := s.keeper.GetModuleAccAddress(StableReserveName)
	securedMgr := newSecuredAssetMgr(s.keeper, s.eventMgr)
	inventory := securedMgr.BalanceOf(ctx, asset, moduleAddr)

	withdrawAmount := cosmos.MinUint(cosmos.MinUint(amount, credit), inventory)
	if withdrawAmount.IsZero() {
		// A successful-looking no-op: the contributor asked for their money back
		// and got nothing, i.e. the credit-vs-inventory invariant has already
		// broken for a real user. Asset only - the contributor address would be
		// an unbounded label.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "stable_reserve", "withdraw_unfilled"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("asset", asset.GetLayer1Asset().String())},
		)
		_ = s.eventMgr.EmitEvent(ctx, newStableReserveEvent(
			"stable_reserve_withdraw_unfilled",
			"asset", asset.GetLayer1Asset().String(),
			"contributor", contributor.String(),
			"amount", amount.String(),
			"reason", "no withdrawable credit or inventory",
		))
		return common.NoCoin, false, nil
	}

	withdrawCoin, err := securedMgr.Withdraw(ctx, asset.GetSecuredAsset(), withdrawAmount, moduleAddr, contributor, txID)
	if err != nil {
		return common.NoCoin, false, err
	}
	if err = s.keeper.SetStableReserveCredit(ctx, asset, contributor, common.SafeSub(credit, withdrawAmount)); err != nil {
		return common.NoCoin, false, err
	}

	return withdrawCoin, true, s.eventMgr.EmitEvent(ctx, newStableReserveEvent(
		"stable_reserve_withdraw_filled",
		"asset", asset.GetLayer1Asset().String(),
		"contributor", contributor.String(),
		"amount", withdrawCoin.Amount.String(),
		"tx_id", txID.String(),
	))
}

func (s *StableReserveMgr) TrySwap(ctx cosmos.Context, mgr Manager, msg MsgSwap) (bool, cosmos.Uint, error) {
	if !s.IsEnabled(ctx) || !msg.IsMarketSwap() {
		return false, cosmos.ZeroUint(), nil
	}
	// Direct fills only serve single (non-streaming) market swaps. Exclude every
	// advanced-queue (V2) streaming swap for its whole lifetime: State.Quantity is
	// resolved to its final value at queue admission (AddSwapQueueItem), so
	// IsStreaming() is already true on the FIRST sub-swap, when State.Count is
	// still 0. A Count > 0 guard alone missed that first sub-swap — it dispatches
	// before the queue increments Count (post-RunWithEmit) — so the stream's first
	// slice took a direct fill instead of streaming through the pools.
	//
	// A direct-path single market swap reaches here with Quantity still 0 (only
	// the advanced queue resolves it) and must fill, so a Quantity != 1 guard
	// cannot be used. A rare advanced-queue stream whose oversized interval
	// resolves Quantity to 0 slips this check, but NextSize zeroes its amount so
	// the fill is rejected downstream — benign.
	if msg.State != nil && (msg.State.Count > 0 || msg.IsStreaming()) {
		return false, cosmos.ZeroUint(), nil
	}
	// Legacy (V1) streaming swaps track progress in a separate StreamingSwap
	// record, not msg.State, so the guard above never trips for them. On this path
	// TrySwap runs before that record is created and before the sub-swap-size
	// hijack in handler_swap.go, so it would see the full deposit and fill it 1:1
	// every block. Leave legacy streaming swaps to the normal streaming machinery;
	// the direct fill only serves single market swaps.
	if msg.IsLegacyStreaming() {
		return false, cosmos.ZeroUint(), nil
	}
	if len(msg.Tx.Coins) != 1 {
		return false, cosmos.ZeroUint(), nil
	}

	// Lock fills to swaps between two external L1 stable coins. Eligibility runs
	// on the ORIGINAL source/target, not a GetLayer1Asset-normalized form: a
	// market swap's source/target may arrive as a secured, synth, or trade form
	// (handler_swap validation permits any non-native L1-backed form), and
	// normalizing before the check would let those wrapped forms be filled here.
	// IsEligibleStable rejects anything that is not already an external L1 asset,
	// so source and target are guaranteed L1 once both checks pass — the outbound
	// below reads them directly (target.GetChain(), the outbound coin, the
	// outbound fee) without any further normalization.
	source := msg.Tx.Coins[0].Asset
	target := msg.TargetAsset
	if source.Equals(target) {
		return false, cosmos.ZeroUint(), nil
	}
	// Price gate: both legs must sit inside the depeg tolerance band
	// ($1 ± StableReserveDepegToleranceBps, default 100 bps → 0.99–1.01). The
	// IsEligibleStable checks enforce that independently on the source and the
	// target, so any pair that reaches the fill is within tolerance.
	if ok, _ := s.IsEligibleStable(ctx, source); !ok {
		return false, cosmos.ZeroUint(), nil
	}
	if ok, _ := s.IsEligibleStable(ctx, target); !ok {
		return false, cosmos.ZeroUint(), nil
	}

	// Both legs pass IsEligibleStable independently against $1 ± tolerance, so two
	// healthy stables can still sit on opposite edges of the band. Both pricing
	// modes below need these prices — the default mode to guard against that
	// arbitrage drain, oracle mode to scale the fill. Use per-asset pool prices
	// here; ticker oracle prices collapse same-ticker assets across chains.
	srcPrice, ok := s.stableReservePoolPrice(ctx, source)
	if !ok {
		return false, cosmos.ZeroUint(), nil
	}
	tgtPrice, ok := s.stableReservePoolPrice(ctx, target)
	if !ok {
		return false, cosmos.ZeroUint(), nil
	}
	grossIn := msg.Tx.Coins[0].Amount
	grossTarget := grossIn
	if s.keeper.GetConfigInt64(ctx, constants.StableReserveOraclePricing) > 0 {
		// Oracle pricing: scale the output by srcPrice/tgtPrice so the fill is
		// value-neutral. That makes the source-worth-at-least-target guard
		// redundant — every in-band pair fills, the reserve just pays out the
		// value equivalent rather than a flat 1:1 count.
		var ok bool
		grossTarget, ok = stableReserveOracleRatioOutput(grossIn, srcPrice, tgtPrice)
		if !ok || grossTarget.IsZero() {
			return false, cosmos.ZeroUint(), nil
		}
	} else if !stableReserveSourceWorthAtLeastTarget(srcPrice, tgtPrice) {
		return false, cosmos.ZeroUint(), nil
	}
	fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, target, false)
	if err != nil {
		return false, cosmos.ZeroUint(), nil
	}
	if common.SafeSub(grossTarget, fee).LT(msg.TradeTarget) {
		return false, cosmos.ZeroUint(), nil
	}

	securedMgr := newSecuredAssetMgr(s.keeper, s.eventMgr)
	moduleAddr := s.keeper.GetModuleAccAddress(StableReserveName)
	availableTarget := securedMgr.BalanceOf(ctx, target, moduleAddr)
	if availableTarget.LT(grossTarget) {
		return false, cosmos.ZeroUint(), nil
	}

	cacheCtx, commit := ctx.CacheContext()
	if _, err = securedMgr.Deposit(cacheCtx, source, grossIn, moduleAddr, msg.Tx.FromAddress, msg.Tx.ID); err != nil {
		return false, cosmos.ZeroUint(), err
	}
	if _, err = securedMgr.Withdraw(cacheCtx, target.GetSecuredAsset(), grossTarget, moduleAddr, msg.Tx.FromAddress, msg.Tx.ID); err != nil {
		return false, cosmos.ZeroUint(), err
	}

	// Mirror swapper.Swap's outbound gate (see swap_current.go): when the
	// advanced swap queue is active (and not a simulation), the queue accumulates
	// this fill's emit into msg.State.Out and settleSwap creates the completed-swap
	// outbound. Scheduling the outbound here as well would pay the fill twice. Only
	// schedule directly on the legacy path (or in simulation, where settlement is
	// skipped), matching how normal swaps materialize their outbound.
	advSwapQueueSettles := s.keeper.AdvSwapQueueEnabled(ctx) && !isSimulationMode(ctx)
	if !msg.Destination.IsNoop() && !advSwapQueueSettles {
		toi := TxOutItem{
			Chain:     target.GetChain(),
			InHash:    msg.Tx.ID,
			ToAddress: msg.Destination,
			Coin:      common.NewCoin(target, grossTarget),
		}
		var ok bool
		ok, err = mgr.TxOutStore().TryAddTxOutItem(cacheCtx, mgr, toi, msg.TradeTarget)
		if err != nil {
			return false, cosmos.ZeroUint(), err
		}
		if !ok {
			return false, cosmos.ZeroUint(), errFailAddOutboundTx
		}
	}

	swapEvt := NewEventSwap(common.TOR, msg.TradeTarget, cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), msg.Tx, common.NewCoin(target, grossTarget), cosmos.ZeroUint())
	swapEvt.StableReserve = true
	if err = mgr.EventMgr().EmitEvent(cacheCtx, swapEvt); err != nil {
		return false, cosmos.ZeroUint(), err
	}

	commit()
	return true, grossTarget, nil
}

func (s *StableReserveMgr) isTORAnchor(ctx cosmos.Context, asset common.Asset) bool {
	// Mirror keeper.GetAnchors(TOR) semantics without scanning and unmarshalling
	// the whole pool store on every swap
	return isTORAnchorAsset(ctx, s.keeper, asset.GetLayer1Asset())
}

func (s *StableReserveMgr) stableReservePoolPrice(ctx cosmos.Context, asset common.Asset) (string, bool) {
	pool, err := s.keeper.GetPool(ctx, asset.GetLayer1Asset())
	if err != nil || !pool.IsAvailable() || pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() {
		return "", false
	}
	dollarsPerRune := s.stableReserveDollarsPerRune(ctx, asset)
	if dollarsPerRune.IsZero() {
		return "", false
	}

	numerator := new(big.Int).Mul(dollarsPerRune.BigInt(), pool.BalanceRune.BigInt())
	denominator := new(big.Int).Mul(big.NewInt(int64(common.One)), pool.BalanceAsset.BigInt())
	price := new(big.Rat).SetFrac(numerator, denominator)
	return price.FloatString(18), true
}

func (s *StableReserveMgr) stableReserveDollarsPerRune(ctx cosmos.Context, asset common.Asset) cosmos.Uint {
	dollarsPerRune, err := s.keeper.GetMimir(ctx, "DollarsPerRune")
	if err == nil && dollarsPerRune > 0 {
		return cosmos.NewUint(uint64(dollarsPerRune))
	}

	l1 := asset.GetLayer1Asset()
	anchors := s.keeper.GetAnchors(ctx, common.TOR)
	ignoreHalt := true
	for _, anchor := range anchors {
		anchor = anchor.GetLayer1Asset()
		if anchor.Equals(l1) {
			continue
		}
		if !s.keeper.IsChainTradingHalted(ctx, anchor.Chain) {
			ignoreHalt = false
			break
		}
	}

	prices := make([]cosmos.Uint, 0, len(anchors))
	for _, anchor := range anchors {
		anchor = anchor.GetLayer1Asset()
		if anchor.Equals(l1) || (!ignoreHalt && s.keeper.IsChainTradingHalted(ctx, anchor.Chain)) {
			continue
		}
		pool, err := s.keeper.GetPool(ctx, anchor)
		if err != nil {
			ctx.Logger().Error("fail to get stable reserve usd pool", "asset", anchor.String(), "error", err)
			continue
		}
		if !pool.IsAvailable() {
			continue
		}
		value := pool.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti * common.One))
		if !value.IsZero() {
			prices = append(prices, value)
		}
	}
	return common.GetMedianUint(prices).QuoUint64(constants.DollarMulti)
}

func (s *StableReserveMgr) scheduleDepegRescue(ctx cosmos.Context, mgr Manager) error {
	moduleAddr := s.keeper.GetModuleAccAddress(StableReserveName)
	securedMgr := newSecuredAssetMgr(s.keeper, s.eventMgr)
	var healthyTargets []common.Asset

	// Build the block-scoped set of already-queued rescue keys in a single pass
	// over both swap queues, rather than re-scanning them per depegged asset. A
	// prolonged multi-asset depeg floods the queues while re-queuing every block
	// until each rescue settles, so a per-asset scan would cost O(depegged*queue)
	// per block; this is O(queue) total and decision-equivalent because rescue
	// keys are per source asset and each asset is visited once below.
	pending, err := s.pendingRescueKeys(ctx, mgr)
	if err != nil {
		return err
	}

	iter := s.keeper.GetSecuredAssetIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var secured keeper.SecuredAsset
		if err := s.keeper.Cdc().Unmarshal(iter.Value(), &secured); err != nil {
			return err
		}

		asset := secured.Asset.GetLayer1Asset()
		inventory := securedMgr.BalanceOf(ctx, asset, moduleAddr)
		if inventory.IsZero() {
			continue
		}
		if ok, _ := s.IsEligibleStable(ctx, asset); ok {
			continue
		}
		if pending[types.StableReservePendingRescueSwapKey(asset.String())] {
			continue
		}

		target := common.RuneAsset()
		if healthyTargets == nil {
			healthyTargets = s.healthyStableTargets(ctx)
		}
		for _, healthy := range healthyTargets {
			if !healthy.Equals(asset) {
				target = healthy
				break
			}
		}

		// Note this also fires when the ORACLE breaks (IsEligibleStable rejects on
		// missing/unhealthy oracle or pool price), not only on a real depeg, and it
		// re-fires every block until the rescue swap settles.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "stable_reserve", "depeg_detected"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("asset", asset.String())},
		)
		if err := s.eventMgr.EmitEvent(ctx, newStableReserveEvent(
			"stable_reserve_depeg_detected",
			"asset", asset.String(),
			"amount", inventory.String(),
		)); err != nil {
			return err
		}
		if err := s.scheduleRescueSwap(ctx, mgr, target, asset, inventory, moduleAddr); err != nil {
			return err
		}
	}
	return nil
}

func (s *StableReserveMgr) healthyStableTargets(ctx cosmos.Context) []common.Asset {
	anchors := s.keeper.GetAnchors(ctx, common.TOR)
	targets := make([]common.Asset, 0, len(anchors))
	for _, anchor := range anchors {
		asset := anchor.GetLayer1Asset()
		if ok, _ := s.IsEligibleStable(ctx, asset); ok {
			targets = append(targets, asset)
		}
	}
	return targets
}

// pendingRescueKeys scans both swap queues once and returns the set of
// StableReserve pending-swap keys already queued from the module address. It is
// built once per block (see scheduleDepegRescue) and then membership-tested per
// depegged asset, so the two queues are unmarshalled a single time regardless of
// how many assets have depegged.
func (s *StableReserveMgr) pendingRescueKeys(ctx cosmos.Context, mgr Manager) (map[string]bool, error) {
	stableReserveAddress, err := s.keeper.GetModuleAddress(StableReserveName)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	scan := func(iter cosmos.Iterator) error {
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var msg MsgSwap
			if err := s.keeper.Cdc().Unmarshal(iter.Value(), &msg); err != nil {
				return err
			}
			key, ok := types.StableReservePendingSwapKeyFromMemo(msg.Tx.Memo)
			if !ok {
				continue
			}
			if msg.Tx.FromAddress.Equals(stableReserveAddress) {
				keys[key] = true
			}
		}
		return nil
	}
	if err := scan(mgr.Keeper().GetSwapQueueIterator(ctx)); err != nil {
		return nil, err
	}
	if err := scan(mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)); err != nil {
		return nil, err
	}
	return keys, nil
}

func (s *StableReserveMgr) scheduleRuneDeployment(ctx cosmos.Context, mgr Manager) error {
	// Cheap module-balance read first: when there is no RUNE to deploy, skip the
	// full pool load below entirely.
	runeBalance := s.keeper.GetRuneBalanceOfModule(ctx, StableReserveName)
	if runeBalance.IsZero() {
		return nil
	}

	healthyTargets := s.healthyStableTargets(ctx)
	if len(healthyTargets) == 0 {
		return nil
	}

	moduleAddr := s.keeper.GetModuleAccAddress(StableReserveName)
	target := s.selectDeploymentTarget(ctx, healthyTargets, moduleAddr)
	return s.scheduleDeploymentSwap(ctx, mgr, target, runeBalance, moduleAddr)
}

func (s *StableReserveMgr) selectDeploymentTarget(ctx cosmos.Context, targets []common.Asset, moduleAddr cosmos.AccAddress) common.Asset {
	securedMgr := newSecuredAssetMgr(s.keeper, s.eventMgr)
	selected := targets[0]
	selectedInventory := securedMgr.BalanceOf(ctx, selected, moduleAddr)
	for _, target := range targets[1:] {
		inventory := securedMgr.BalanceOf(ctx, target, moduleAddr)
		if inventory.LT(selectedInventory) || (inventory.Equal(selectedInventory) && target.String() < selected.String()) {
			selected = target
			selectedInventory = inventory
		}
	}
	return selected
}

func (s *StableReserveMgr) scheduleDeploymentSwap(ctx cosmos.Context, mgr Manager, target common.Asset, amount cosmos.Uint, moduleAddr cosmos.AccAddress) error {
	stableReserveAddress, err := s.keeper.GetModuleAddress(StableReserveName)
	if err != nil {
		return err
	}
	asgardAddress, err := s.keeper.GetModuleAddress(AsgardName)
	if err != nil {
		return err
	}

	// Deployment swaps use the network's default streaming parameters: a
	// quantity of 0 lets the swap manager size the stream from slip, and an
	// interval of 0 leaves the swap non-streaming. These are intentionally not
	// configurable via constant or mimir.
	const quantity, interval uint64 = 0, 0

	cacheCtx, commit := ctx.CacheContext()
	coin := common.NewCoin(common.RuneAsset(), amount)
	if err = mgr.Keeper().SendFromModuleToModule(cacheCtx, StableReserveName, AsgardName, common.NewCoins(coin)); err != nil {
		return err
	}

	txID := stableReserveSyntheticTxID(ctx, "deploy", target)
	memo := types.StableReserveDeployMemoPrefix + target.String()
	tx := common.NewTx(txID, stableReserveAddress, asgardAddress, common.NewCoins(coin), common.Gas{}, memo)
	msg := NewMsgSwap(tx, target, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, quantity, interval, types.SwapVersion_v1, moduleAddr)
	if err = s.queueStableReserveSwap(cacheCtx, mgr, msg); err != nil {
		return err
	}

	if err = s.eventMgr.EmitEvent(cacheCtx, newStableReserveEvent(
		"stable_reserve_deployment_scheduled",
		"target", target.String(),
		"amount", amount.String(),
		"stream_quantity", fmt.Sprintf("%d", quantity),
		"stream_interval", fmt.Sprintf("%d", interval),
		"tx_id", txID.String(),
	)); err != nil {
		return err
	}

	commit()
	return nil
}

func (s *StableReserveMgr) scheduleRescueSwap(ctx cosmos.Context, mgr Manager, target, source common.Asset, amount cosmos.Uint, moduleAddr cosmos.AccAddress) error {
	from := common.Address(moduleAddr.String())
	txID := stableReserveSyntheticTxID(ctx, "rescue", source)
	memo := fmt.Sprintf("%s%s:%s", types.StableReserveRescueMemoPrefix, source.String(), target.String())
	tx := common.NewTx(
		txID,
		from,
		common.NoopAddress,
		common.NewCoins(common.NewCoin(source.GetSecuredAsset(), amount)),
		common.Gas{},
		memo,
	)

	// Rescue swaps stream one sub-swap per block to minimize slippage while
	// moving a depegged asset. A quantity of 0 lets the swap manager size the
	// stream from slip (matching deployment swaps); the auto-sizer already
	// floors the count at 1, so tiny rescues stay single-shot without a manual
	// guard.
	const quantity, interval uint64 = 0, 1
	msg := NewMsgSwap(tx, target, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, quantity, interval, types.SwapVersion_v1, moduleAddr)
	if err := s.queueStableReserveSwap(ctx, mgr, msg); err != nil {
		return err
	}

	return s.eventMgr.EmitEvent(ctx, newStableReserveEvent(
		"stable_reserve_rescue_scheduled",
		"asset", source.String(),
		"target", target.String(),
		"amount", amount.String(),
		"tx_id", txID.String(),
	))
}

func (s *StableReserveMgr) queueStableReserveSwap(ctx cosmos.Context, mgr Manager, msg *MsgSwap) error {
	if s.keeper.AdvSwapQueueEnabled(ctx) {
		msg.Version = types.SwapVersion_v2
		return mgr.AdvSwapQueueMgr().AddSwapQueueItem(ctx, mgr, msg)
	}
	return s.keeper.SetSwapQueueItem(ctx, *msg, 0)
}

func settleStableReserveInternalSwap(ctx cosmos.Context, mgr Manager, msg MsgSwap, emit cosmos.Uint) (bool, error) {
	purpose := ""
	switch {
	case strings.HasPrefix(msg.Tx.Memo, types.StableReserveDeployMemoPrefix):
		purpose = "deployment"
	case strings.HasPrefix(msg.Tx.Memo, types.StableReserveRescueMemoPrefix):
		purpose = "rescue"
	default:
		return false, nil
	}
	// Verify the swap actually originated from the Stable Reserve module before
	// treating it as an internal settlement. This authorization check must come
	// ahead of the emit.IsZero() shortcut: a settlement decision should never
	// depend on attacker-controllable memo text without first confirming the
	// module is the sender.
	stableReserveAddress, err := mgr.Keeper().GetModuleAddress(StableReserveName)
	if err != nil {
		return false, err
	}
	if !msg.Tx.FromAddress.Equals(stableReserveAddress) {
		return false, nil
	}
	if emit.IsZero() {
		return true, nil
	}

	// NOTE: contributor credits are tracked per-asset (see Withdraw) and are
	// intentionally NOT reconciled here. A deploy/rescue moves the module's
	// inventory across assets (or into RUNE) at the aggregate level without
	// touching individual per-asset credits, so a contributor whose asset is
	// rescued away keeps a credit that can no longer be withdrawn same-asset.
	// The redemption/loss policy for that case is a pending design decision.
	// Internal swaps targeting RUNE (depeg rescues) leave their proceeds in Asgard
	// during the stream: the asgard solvency invariant counts a streaming swap's
	// pending State.Out as RUNE that Asgard must custody until the swap settles, so
	// moving it out per sub-swap would break solvency mid-stream. The accumulated
	// RUNE is moved into the module once, at completion, by settleSwapCached —
	// which runs for both a successful and a failed final sub-swap, so proceeds
	// from earlier sub-swaps are never stranded in Asgard.
	if msg.TargetAsset.IsRune() {
		return true, nil
	}

	target := msg.TargetAsset.GetLayer1Asset()
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	if _, err := newSecuredAssetMgr(mgr.Keeper(), mgr.EventMgr()).Deposit(ctx, target, emit, moduleAddr, common.Address(moduleAddr.String()), msg.Tx.ID); err != nil {
		return false, err
	}

	return true, mgr.EventMgr().EmitEvent(ctx, newStableReserveEvent(
		"stable_reserve_"+purpose+"_inventory_settled",
		"asset", target.String(),
		"amount", emit.String(),
		"tx_id", msg.Tx.ID.String(),
	))
}

func isStableReserveInternalSwap(ctx cosmos.Context, mgr Manager, msg MsgSwap) (bool, error) {
	if !strings.HasPrefix(msg.Tx.Memo, types.StableReserveDeployMemoPrefix) &&
		!strings.HasPrefix(msg.Tx.Memo, types.StableReserveRescueMemoPrefix) {
		return false, nil
	}

	stableReserveAddress, err := mgr.Keeper().GetModuleAddress(StableReserveName)
	if err != nil {
		return false, err
	}
	return msg.Tx.FromAddress.Equals(stableReserveAddress), nil
}

func stableReserveSyntheticTxID(ctx cosmos.Context, purpose string, asset common.Asset) common.TxID {
	sum := sha256.Sum256([]byte(fmt.Sprintf("stable-reserve/%s/%d/%s", purpose, ctx.BlockHeight(), asset.String())))
	txID, err := common.NewTxID(hex.EncodeToString(sum[:]))
	if err != nil {
		return common.BlankTxID
	}
	return txID
}

// stableReserveOracleRatioOutput scales a stable-reserve fill by the oracle
// price ratio: out = floor(grossIn * srcPrice / tgtPrice). Only used when the
// StableReserveOraclePricing mimir is on; the default fill is 1:1. Both prices
// are USD-per-unit strings from stableReservePoolPrice. The result is floored so
// any rounding favors the reserve. All arithmetic uses big.Rat (no float math).
// Returns false if either price is unparsable, the source price is negative, or
// the target price is non-positive — those sign guards also keep the result
// non-negative so cosmos.NewUintFromBigInt can never panic.
func stableReserveOracleRatioOutput(grossIn cosmos.Uint, srcPrice, tgtPrice string) (cosmos.Uint, bool) {
	// Bound input length to mitigate G113 (CVE-2022-23772), mirroring
	// stableReserveOracleHealthy.
	if len(srcPrice) > 64 || len(tgtPrice) > 64 {
		return cosmos.ZeroUint(), false
	}
	src, ok := new(big.Rat).SetString(srcPrice) // nolint:gosec // G113 mitigated by length bound above
	if !ok || src.Sign() < 0 {
		return cosmos.ZeroUint(), false
	}
	tgt, ok := new(big.Rat).SetString(tgtPrice) // nolint:gosec // G113 mitigated by length bound above
	if !ok || tgt.Sign() <= 0 {
		return cosmos.ZeroUint(), false
	}
	out := new(big.Rat).SetInt(grossIn.BigInt())
	out.Mul(out, src)
	out.Quo(out, tgt)
	floored := new(big.Int).Quo(out.Num(), out.Denom())
	return cosmos.NewUintFromBigInt(floored), true
}

// stableReserveSourceWorthAtLeastTarget reports whether the source stable's
// price is >= the target's, so a 1:1 reserve fill never pays out more value
// than it takes in. Inputs are decimal price strings; comparison uses big.Rat
// for determinism (no float math).
func stableReserveSourceWorthAtLeastTarget(srcPrice, tgtPrice string) bool {
	// Bound input length to mitigate G113 (CVE-2022-23772), mirroring
	// stableReserveOracleHealthy.
	if len(srcPrice) > 64 || len(tgtPrice) > 64 {
		return false
	}
	src, ok := new(big.Rat).SetString(srcPrice) // nolint:gosec // G113 mitigated by length bound above
	if !ok {
		return false
	}
	tgt, ok := new(big.Rat).SetString(tgtPrice) // nolint:gosec // G113 mitigated by length bound above
	if !ok {
		return false
	}
	return src.Cmp(tgt) >= 0
}

func stableReserveOracleHealthy(price string, toleranceBps int64) bool {
	// Bound the input length to mitigate G113 (CVE-2022-23772): a maliciously
	// large exponent in Rat.SetString can drive uncontrolled memory consumption.
	if len(price) > 64 {
		return false
	}
	value, ok := new(big.Rat).SetString(price) // nolint:gosec // G113 mitigated by length bound above
	if !ok {
		return false
	}
	if toleranceBps < 0 {
		toleranceBps = 0
	}
	tolerance := new(big.Rat).SetFrac(big.NewInt(toleranceBps), big.NewInt(10_000))
	one := big.NewRat(1, 1)
	lower := new(big.Rat).Sub(one, tolerance)
	upper := new(big.Rat).Add(one, tolerance)
	return value.Cmp(lower) >= 0 && value.Cmp(upper) <= 0
}

type stableReserveEvent struct {
	eventType string
	attrs     []string
}

func newStableReserveEvent(eventType string, attrs ...string) stableReserveEvent {
	return stableReserveEvent{eventType: eventType, attrs: attrs}
}

func (e stableReserveEvent) Events() (cosmos.Events, error) {
	attrs := make([]cosmos.Attribute, 0, len(e.attrs)/2)
	for i := 0; i+1 < len(e.attrs); i += 2 {
		attrs = append(attrs, cosmos.NewAttribute(e.attrs[i], e.attrs[i+1]))
	}
	return cosmos.Events{cosmos.NewEvent(e.eventType, attrs...)}, nil
}

type DummyStableReserveManager struct{}

func NewDummyStableReserveManager() *DummyStableReserveManager { return &DummyStableReserveManager{} }

func (DummyStableReserveManager) EndBlock(ctx cosmos.Context, mgr Manager) error { return nil }
func (DummyStableReserveManager) IsEnabled(ctx cosmos.Context) bool              { return false }
func (DummyStableReserveManager) IsEligibleStable(ctx cosmos.Context, asset common.Asset) (bool, string) {
	return false, "stable reserve disabled"
}

func (DummyStableReserveManager) Deposit(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, contributor common.Address, txID common.TxID) error {
	return fmt.Errorf("stable reserve disabled")
}

func (DummyStableReserveManager) Withdraw(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, contributor common.Address, txID common.TxID) (common.Coin, bool, error) {
	return common.NoCoin, false, fmt.Errorf("stable reserve disabled")
}

func (DummyStableReserveManager) TrySwap(ctx cosmos.Context, mgr Manager, msg MsgSwap) (bool, cosmos.Uint, error) {
	return false, cosmos.ZeroUint(), nil
}

func isStableReserveDepositMemo(memo string) bool {
	// Strip the "|" tail before matching, mirroring newParser (memo_parser.go)
	// so a memo like "STABLE+|app" is recognised the same way it was routed.
	memo = strings.ToUpper(strings.TrimSpace(strings.Split(memo, "|")[0]))
	return memo == "STABLE+" || strings.HasPrefix(memo, "STABLE+:")
}

func isStableReserveWithdrawMemo(memo string) bool {
	// Strip the "|" tail before matching, mirroring newParser (memo_parser.go).
	memo = strings.ToUpper(strings.TrimSpace(strings.Split(memo, "|")[0]))
	return memo == "STABLE-" || strings.HasPrefix(memo, "STABLE-:")
}
