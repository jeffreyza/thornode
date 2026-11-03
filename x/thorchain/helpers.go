package thorchain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdkmath "cosmossdk.io/math"
	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	"github.com/hashicorp/go-multierror"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// isSimulationMode checks if the context indicates we're in simulation mode
func isSimulationMode(ctx cosmos.Context) bool {
	simulationMode, ok := ctx.Value(constants.CtxSimulationMode).(bool)
	return ok && simulationMode
}

func txOutPubKeyMatchesObserved(chain common.Chain, observed common.PubKey, item TxOutItem) bool {
	if observed.Equals(item.VaultPubKey) || observed.Equals(item.VaultPubKeyFrost) {
		return true
	}
	if chain == common.XMRChain {
		return false
	}
	return observed.Equals(item.VaultPubKeyEddsa)
}

func txOutVaultPubKeyEddsa(chain common.Chain, pubKey common.PubKey) common.PubKey {
	if chain == common.XMRChain {
		return common.EmptyPubKey
	}
	return pubKey
}

func txOutSourcePubKey(item TxOutItem) (common.PubKey, error) {
	switch item.Chain.GetSigningAlgo() {
	case common.SigningAlgoSecp256k1:
		if item.VaultPubKey.IsEmpty() {
			return common.EmptyPubKey, fmt.Errorf("vault pubkey is empty")
		}
		return item.VaultPubKey, nil
	case common.SigningAlgoEd25519:
		if item.Chain == common.XMRChain {
			if item.VaultPubKeyFrost.IsEmpty() {
				return common.EmptyPubKey, fmt.Errorf("vault pubkey frost is empty")
			}
			return item.VaultPubKeyFrost, nil
		}
		if item.VaultPubKeyEddsa.IsEmpty() {
			return common.EmptyPubKey, fmt.Errorf("vault pubkey eddsa is empty")
		}
		return item.VaultPubKeyEddsa, nil
	default:
		return common.EmptyPubKey, fmt.Errorf("unknown signing algorithm: %s", item.Chain.GetSigningAlgo())
	}
}

// observedVaultHasEffectiveRefundFunds checks whether a refund can safely keep
// using the observed inbound vault. Refunds may preselect the vault that saw the
// inbound, but that vault may already have outstanding outbounds assigned that
// have not yet been observed and deducted from vault accounting.
func observedVaultHasEffectiveRefundFunds(ctx cosmos.Context, tx ObservedTx, mgr Manager, vault Vault, coin common.Coin) bool {
	if vault.IsEmpty() {
		return false
	}

	// Work on a copy: pending deduction is only for this availability check.
	availableVault := vault

	// Pending outbounds are already committed to this vault, even though the
	// vault ledger will not be debited until those outbounds are observed.
	availableVault.DeductVaultPendingOutbounds(mgr.Keeper().GetPendingOutbounds(ctx, coin.Asset))

	// Native THORChain outbounds do not need L1 gas reservation.
	if coin.Asset.GetChain().IsTHORChain() {
		return availableVault.GetCoin(coin.Asset).Amount.GTE(coin.Amount)
	}

	maxGasCoin, err := mgr.GasMgr().GetMaxGas(ctx, coin.Asset.GetChain())
	if err != nil {
		ctx.Logger().Error("fail to get max gas for observed refund vault", "tx_id", tx.Tx.ID, "chain", coin.Asset.GetChain(), "error", err)
		return false
	}

	requiredCoinAmount := coin.Amount
	if coin.Asset.Equals(maxGasCoin.Asset) {
		if vault.Status == InactiveVault {
			// Inactive-vault refunds keep the original vault for recovery and
			// later deduct MaxGas from the coin amount, so the total wallet spend
			// is the refund coin amount rather than coin amount plus MaxGas.
			return availableVault.GetCoin(coin.Asset).Amount.GTE(requiredCoinAmount)
		}
		// Gas-asset refunds must leave enough of the same asset to pay gas.
		requiredCoinAmount = requiredCoinAmount.Add(maxGasCoin.Amount)
	} else if availableVault.GetCoin(maxGasCoin.Asset).Amount.LT(maxGasCoin.Amount) {
		// Token refunds still require enough chain gas asset in the vault.
		return false
	}

	return availableVault.GetCoin(coin.Asset).Amount.GTE(requiredCoinAmount)
}

func refundTx(ctx cosmos.Context, tx ObservedTx, mgr Manager, refundCode uint32, refundReason, sourceModuleName string) error {
	// If THORNode recognize one of the coins, and therefore able to refund
	// withholding fees, refund all coins.

	refundCoins := make(common.Coins, 0)
	for _, coin := range tx.Tx.Coins {
		// Do not emit Trade/Secured Asset refund event or attempt refund txout,
		// as Trade/Secured Asset withdrawals take place in the internal handlers
		// (state changes and event emission only if the internal handler succeeds)
		// and not in the deposit handler.
		if coin.Asset.IsTradeAsset() || coin.Asset.IsSecuredAsset() {
			continue
		}

		if coin.IsRune() && coin.Asset.GetChain().Equals(common.ETHChain) {
			continue
		}
		pool, err := mgr.Keeper().GetPool(ctx, coin.Asset.GetLayer1Asset())
		if err != nil {
			return fmt.Errorf("fail to get pool: %w", err)
		}

		// Only attempt an outbound if a fee can be taken from the coin.
		if coin.IsRune() || !pool.BalanceRune.IsZero() {
			toAddr := tx.Tx.FromAddress
			usesSourceSender := true
			memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), tx.Tx.Memo)
			hasMemoRefundAddress := false
			if err == nil {
				hasMemoRefundAddress = memo.IsType(TxSwap) ||
					(memo.IsType(TxAdd) && coin.Asset.GetChain().RequiresExplicitRefundAddress())
			}
			if err == nil && hasMemoRefundAddress && !memo.GetRefundAddress().IsEmpty() && !coin.Asset.GetChain().IsTHORChain() {
				// If the memo specifies a refund address, send the refund to that address. If
				// refund memo can't be parsed or is invalid for the refund chain, it will
				// default back to the sender address. Add-liquidity memo addresses are used
				// only on chains that require an explicit refund address.
				refundAddress := memo.GetRefundAddress()
				validRefundAddress := refundAddress.IsChain(coin.Asset.GetChain())
				if coin.Asset.GetChain().RequiresExplicitRefundAddress() {
					validRefundAddress = validRefundAddress && coin.Asset.GetChain().IsValidAddress(refundAddress)
				}
				if coin.Asset.GetChain().OneOf(common.ZECChain, common.DASHChain) {
					validRefundAddress = validRefundAddress && refundAddress.IsCurrentNetwork(coin.Asset.GetChain())
				}
				if validRefundAddress {
					toAddr = refundAddress
					usesSourceSender = false
				}
			}

			sender := tx.Tx.ObservedSender()
			var vault Vault
			if !tx.ObservedPubKey.IsEmpty() {
				vault, err = mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
				if err != nil {
					return fmt.Errorf("fail to get vault: %w", err)
				}
				// Only keep the observed vault when it can fund the refund after
				// pending outbounds and gas reservation. Otherwise let txout
				// discovery choose an available vault.
				if !observedVaultHasEffectiveRefundFunds(ctx, tx, mgr, vault, coin) {
					vault = Vault{}
				}
			}

			toi := TxOutItem{
				Chain:                 coin.Asset.GetChain(),
				ToAddress:             toAddr,
				VaultPubKey:           vault.PubKey,
				VaultPubKeyEddsa:      txOutVaultPubKeyEddsa(coin.Asset.GetChain(), vault.PubKeyEddsa),
				VaultPubKeyFrost:      vault.PubKeyFrost,
				Coin:                  coin,
				Memo:                  NewRefundMemo(tx.Tx.ID).String(),
				MaxGas:                []common.Coin{},
				GasRate:               0,
				InHash:                tx.Tx.ID,
				OutHash:               "",
				ModuleName:            sourceModuleName,
				Aggregator:            "",
				AggregatorTargetAsset: "",
				AggregatorTargetLimit: &cosmos.Uint{},
			}

			if refundActionAlreadyScheduled(ctx, mgr, toi) {
				ctx.Logger().Info("skip duplicate refund action", "in_hash", toi.InHash.String(), "coin", toi.Coin.String(), "memo", toi.GetMemo())
				continue
			}

			if usesSourceSender && sender.RefundAddressRequired {
				ctx.Logger().Info("skip refund to unavailable sender", "in_hash", tx.Tx.ID.String(), "coin", coin.String())
				refundReason = fmt.Sprintf("%s; fail to refund (%s): sender unavailable and no explicit refund address", refundReason, toi.Coin.String())
				unrefundableCoinCleanup(ctx, mgr, toi, "failed_refund_unknown_sender")
				continue
			}

			success, err := mgr.TxOutStore().TryAddTxOutItem(ctx, mgr, toi, cosmos.ZeroUint())
			if err != nil {
				ctx.Logger().Error("fail to prepare outbound tx", "error", err)
				// concatenate the refund failure to refundReason
				refundReason = fmt.Sprintf("%s; fail to refund (%s): %s", refundReason, toi.Coin.String(), err)

				unrefundableCoinCleanup(ctx, mgr, toi, "failed_refund")
			}
			if success {
				refundCoins = append(refundCoins, toi.Coin)
				// Count refunds actually scheduled, not merely attempted. Skipped in
				// simulation mode: the querier's quote simulation reaches refundTx via
				// SwapQ().EndBlock for any failing simulated swap, and would otherwise
				// inflate this counter on every quote request.
				if !isSimulationMode(ctx) {
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "refund"},
						1,
						[]metrics.Label{
							telemetry.NewLabel("chain", toi.Chain.String()),
							telemetry.NewLabel("code", refundCodeLabel(refundCode)),
						},
					)
				}
			}
		}
		// Zombie coins are just dropped.
	}

	// For refund events, emit the event after the txout attempt in order to include the 'fail to refund' reason if unsuccessful.
	eventRefund := NewEventRefund(refundCode, refundReason, tx.Tx, common.NewFee(common.Coins{}, cosmos.ZeroUint()))
	if len(refundCoins) > 0 {
		// create a new TX based on the coins thorchain refund , some of the coins thorchain doesn't refund
		// coin thorchain doesn't have pool with , likely airdrop
		newTx := common.NewTx(tx.Tx.ID, tx.Tx.FromAddress, tx.Tx.ToAddress, tx.Tx.Coins, tx.Tx.Gas, tx.Tx.Memo)
		eventRefund = NewEventRefund(refundCode, refundReason, newTx, common.Fee{}) // fee param not used in downstream event
	}
	if err := mgr.EventMgr().EmitEvent(ctx, eventRefund); err != nil {
		return fmt.Errorf("fail to emit refund event: %w", err)
	}

	return nil
}

func refundActionAlreadyScheduled(ctx cosmos.Context, mgr Manager, toi TxOutItem) bool {
	memoIdentity := func(memo string) string {
		base, _, _ := strings.Cut(memo, "|")
		return base
	}
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, toi.InHash)
	if err != nil {
		return false
	}
	for _, action := range voter.Actions {
		// Deliberately match on the stable refund action identity rather than
		// amount or destination, which may vary after fee or vault selection.
		if action.Coin.Asset.Equals(toi.Coin.Asset) &&
			memoIdentity(action.GetMemo()) == memoIdentity(toi.GetMemo()) &&
			action.Chain.Equals(toi.Chain) {
			return true
		}
	}
	return false
}

func refundCleanupKey(toi TxOutItem, burnReason string) string {
	key := strings.Join([]string{
		toi.InHash.String(),
		toi.Chain.String(),
		toi.Coin.String(),
		toi.GetMemo(),
		toi.GetModuleName(),
		burnReason,
	}, "|")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// unrefundableCoinCleanup - update the accounting for a failed outbound of toi.Coin
// native rune: send to the reserve
// native coin besides rune: burn
// non-native coin: donate to its pool
func unrefundableCoinCleanup(ctx cosmos.Context, mgr Manager, toi TxOutItem, burnReason string) {
	coin := toi.Coin
	cleanupKey := refundCleanupKey(toi, burnReason)

	if mgr.Keeper().HasRefundCleanup(ctx, cleanupKey) {
		ctx.Logger().Info("skip duplicate unrefundable coin cleanup", "in_hash", toi.InHash.String(), "coin", coin.String(), "reason", burnReason)
		return
	}

	// Past this point the network has given up on returning a user's coins. Every
	// increment is a customer-visible loss event, and nothing else exports it.
	// disposition records where the money actually ended up, so one deferred emit
	// covers all the exit paths below instead of a counter at each. Skipped in
	// simulation mode, which reaches here through the querier's quote simulation.
	disposition := "left_in_vault"
	if !isSimulationMode(ctx) {
		defer func() {
			labels := []metrics.Label{
				telemetry.NewLabel("reason", burnReason),
				telemetry.NewLabel("disposition", disposition),
				telemetry.NewLabel("asset", coin.Asset.String()),
			}
			telemetry.IncrCounterWithLabels([]string{"thornode", "unrefundable_cleanup"}, 1, labels)
			telemetry.IncrCounterWithLabels([]string{"thornode", "unrefundable_cleanup", "amount"}, telem(coin.Amount), labels)
		}()
	}

	if coin.Asset.IsTradeAsset() {
		mgr.Keeper().SetRefundCleanup(ctx, cleanupKey)
		disposition = "trade_asset_noop"
		return
	}

	sourceModuleName := toi.GetModuleName() // Ensure that non-"".

	// For context in emitted events, retrieve the original transaction that prompted the cleanup.
	// If there is no retrievable transaction, leave those fields empty.
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, toi.InHash)
	if err != nil {
		ctx.Logger().Error("fail to get observed tx in", "error", err, "hash", toi.InHash.String())
		return
	}
	tx := voter.Tx.Tx
	// For emitted events' amounts (such as EventDonate), replace the Coins with the coin being cleaned up.
	tx.Coins = common.NewCoins(toi.Coin)

	// Select course of action according to coin type:
	// External coin, native coin which isn't RUNE, or native RUNE (not from the Reserve).
	switch {
	case !coin.Asset.IsNative():
		// Add unrefundable external coins to their pool so the vault balance
		// is correctly reflected in the pool accounting.  Failed-refund
		// external coins have earlier been established to have existing pools
		// with non-zero BalanceRune.
		pool, poolErr := mgr.Keeper().GetPool(ctx, coin.Asset.GetLayer1Asset())
		if poolErr != nil || pool.BalanceRune.IsZero() {
			ctx.Logger().Error("fail to refund non-native tx, leaving coins in vault",
				"toi.InHash", toi.InHash, "toi.Coin", toi.Coin, "pool_err", poolErr)
			return
		}
		pool.BalanceAsset = pool.BalanceAsset.Add(coin.Amount)
		if poolErr = mgr.Keeper().SetPool(ctx, pool); poolErr != nil {
			ctx.Logger().Error("fail to add unrefundable coin to pool",
				"error", poolErr, "toi.InHash", toi.InHash, "toi.Coin", toi.Coin)
			return
		}
		mgr.Keeper().SetRefundCleanup(ctx, cleanupKey)
		disposition = "pool_donate"
		donateEvt := NewEventDonate(coin.Asset.GetLayer1Asset(), tx)
		if emitErr := mgr.EventMgr().EmitEvent(ctx, donateEvt); emitErr != nil {
			ctx.Logger().Error("fail to emit donate event for unrefundable coin", "error", emitErr)
		}
	case sourceModuleName != ReserveName:
		// If unable to refund THOR.RUNE, send it to the Reserve.
		err := mgr.Keeper().SendFromModuleToModule(ctx, sourceModuleName, ReserveName, common.NewCoins(coin))
		if err != nil {
			ctx.Logger().Error("fail to send native coin to Reserve during cleanup", "error", err)
			return
		}
		mgr.Keeper().SetRefundCleanup(ctx, cleanupKey)
		disposition = "reserve"

		reserveContributor := NewReserveContributor(tx.FromAddress, coin.Amount)
		reserveEvent := NewEventReserve(reserveContributor, tx)
		if err := mgr.EventMgr().EmitEvent(ctx, reserveEvent); err != nil {
			ctx.Logger().Error("fail to emit reserve event", "error", err)
		}
	default:
		// If not satisfying the other conditions this coin should be a native coin in the Reserve,
		// so leave it there.
		mgr.Keeper().SetRefundCleanup(ctx, cleanupKey)
		disposition = "reserve_noop"
	}
}

func getMaxSwapQuantity(ctx cosmos.Context, mgr Manager, sourceAsset, targetAsset common.Asset, swp StreamingSwap, affiliates []string, affiliateBps []cosmos.Uint) (uint64, error) {
	// Interval 0 is rapid streaming. For quantity limits, treat it as 1 block
	// so rapid requests can still be bounded by swap size and max length rules.
	interval := swp.Interval
	if interval == 0 {
		interval = 1
	}

	// collect pools involved in this swap
	minSwapSize := cosmos.ZeroUint()
	stableSwap := isStableToStable(ctx, mgr.Keeper(), sourceAsset, targetAsset)
	var sourceAssetPool types.Pool
	for i, asset := range []common.Asset{sourceAsset, targetAsset} {
		if asset.IsRune() {
			continue
		}

		// get the asset pool
		pool, err := mgr.Keeper().GetPool(ctx, asset.GetLayer1Asset())
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

		// Honor any dynamic floor for this thorname/pair so sub-swaps sized
		// below the dynamic minimum aren't carved out here only to be charged
		// the floor at swap time.
		minSlip := getMinSlipBps(ctx, mgr.Keeper(), common.NoAddress, asset, stableSwap, affiliates, affiliateBps, legSource, legTarget, mgr.DynamicFeeMgr())
		if minSlip.IsZero() {
			continue
		}

		// compute the minimum rune swap size for this leg of the swap
		minRuneSwapSize := common.GetSafeShare(minSlip, cosmos.NewUint(constants.MaxBasisPts), pool.BalanceRune)
		if minSwapSize.IsZero() || minRuneSwapSize.LT(minSwapSize) {
			minSwapSize = minRuneSwapSize
		}
	}

	// calculate the max swap quantity
	if !sourceAsset.IsRune() {
		minSwapSize = sourceAssetPool.RuneValueInAsset(minSwapSize)
	}
	if minSwapSize.IsZero() {
		return 1, nil
	}
	maxSwapQuantity := swp.Deposit.Quo(minSwapSize)

	// make sure maxSwapQuantity doesn't infringe on max length that a
	// streaming swap can exist
	var maxLength int64
	if sourceAsset.IsNative() && targetAsset.IsNative() {
		maxLength = mgr.Keeper().GetConfigInt64(ctx, constants.StreamingSwapMaxLengthNative)
	} else {
		maxLength = mgr.Keeper().GetConfigInt64(ctx, constants.StreamingSwapMaxLength)
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
		dpool, _ := mgr.Keeper().GetPool(ctx, asset) // get the derived asset pool
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

// isStableToStable returns true if both source and target L1 assets are TOR anchors (stablecoins).
//
// It runs inside txs (e.g. a deposit's swap queue entry), where store reads are
// gas and gas_used is consensus-visible, so it keeps the GetAnchors scan: point
// lookups of the two candidates give the same answer with different gas.
func isStableToStable(ctx cosmos.Context, k keeper.Keeper, source, target common.Asset) bool {
	anchors := k.GetAnchors(ctx, common.TOR)
	if len(anchors) == 0 {
		return false
	}
	sourceL1 := source.GetLayer1Asset()
	targetL1 := target.GetLayer1Asset()
	var sourceIsAnchor, targetIsAnchor bool
	for _, anchor := range anchors {
		if anchor.Equals(sourceL1) {
			sourceIsAnchor = true
		}
		if anchor.Equals(targetL1) {
			targetIsAnchor = true
		}
	}
	return sourceIsAnchor && targetIsAnchor
}

// isTORAnchorAsset reports whether GetAnchors(TOR) would include the layer-1 asset.
func isTORAnchorAsset(ctx cosmos.Context, k keeper.Keeper, l1 common.Asset) bool {
	if !k.PoolExist(ctx, l1) {
		return false
	}
	val, err := k.GetMimir(ctx, keeperv1.TorAnchorMimirKey(l1))
	return err == nil && val > 0
}

// getMinSlipBps returns artificial slip floor, expressed in basis points (10000).
func getMinSlipBps(
	ctx cosmos.Context,
	k keeper.Keeper,
	from common.Address,
	asset common.Asset,
	stableOverride bool,
	affiliates []string,
	affiliateBps []cosmos.Uint,
	sourceAsset, targetAsset common.Asset,
	dynamicFeeMgr DynamicFeeManager,
) cosmos.Uint {
	wasmArbContract := k.GetConstants().GetStringValue(constants.WasmArbContract)
	if !from.IsEmpty() && !asset.IsEmpty() && wasmArbContract != "" && strings.EqualFold(from.String(), wasmArbContract) {
		return cosmos.SafeUintFromInt64(k.GetConfigInt64(ctx, constants.WasmArbSlipMinBps))
	}
	if !from.IsEmpty() && !asset.IsEmpty() && wasmArbContract != "" {
		wasmArbAddr, err := common.NewAddress(wasmArbContract)
		if err == nil && from.Equals(wasmArbAddr) {
			return cosmos.SafeUintFromInt64(k.GetConfigInt64(ctx, constants.WasmArbSlipMinBps))
		}
	}

	var ref constants.ConstantName
	switch {
	case asset.IsSyntheticAsset():
		ref = constants.SynthSlipMinBps
	case asset.IsTradeAsset():
		ref = constants.TradeAccountsSlipMinBps
	case asset.IsDerivedAsset():
		ref = constants.DerivedSlipMinBps
	case asset.IsSecuredAsset():
		ref = constants.SecuredAssetSlipMinBps
	default:
		ref = constants.L1SlipMinBps
		if dynamicFeeMgr != nil && len(affiliates) > 0 {
			dynBps := dynamicFeeMgr.GetDynamicBps(ctx, affiliates, affiliateBps, sourceAsset, targetAsset)
			if dynBps > 0 {
				return cosmos.SafeUintFromInt64(int64(dynBps))
			}
		}
	}
	if stableOverride {
		stableBps := k.GetConfigInt64(ctx, constants.StableSlipMinBps)
		if stableBps > 0 {
			return cosmos.SafeUintFromInt64(stableBps)
		}
	}
	minFeeMimir := k.GetConfigInt64(ctx, ref)
	return cosmos.SafeUintFromInt64(minFeeMimir)
}

func refundBond(ctx cosmos.Context, tx common.Tx, acc cosmos.AccAddress, amt cosmos.Uint, nodeAcc *NodeAccount, mgr Manager) error {
	if nodeAcc.Status == NodeActive {
		ctx.Logger().Info("node still active, cannot refund bond", "node address", nodeAcc.NodeAddress, "node pub key", nodeAcc.PubKeySet.Secp256k1)
		return nil
	}

	// ensures nodes don't return bond while being churned into the network
	// (removing their bond last second)
	if nodeAcc.Status == NodeReady {
		ctx.Logger().Info("node ready, cannot refund bond", "node address", nodeAcc.NodeAddress, "node pub key", nodeAcc.PubKeySet.Secp256k1)
		return nil
	}

	if amt.IsZero() || amt.GT(nodeAcc.Bond) {
		amt = nodeAcc.Bond
	}

	bp, err := mgr.Keeper().GetBondProviders(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", nodeAcc.NodeAddress))
	}

	err = passiveBackfill(ctx, mgr, *nodeAcc, &bp)
	if err != nil {
		return err
	}

	bp.Adjust(nodeAcc.Bond) // redistribute node bond amongst bond providers
	provider := bp.Get(acc)

	if !provider.IsEmpty() && !provider.Bond.IsZero() {
		if amt.GT(provider.Bond) {
			amt = provider.Bond
		}

		bp.Unbond(amt, provider.BondAddress)

		// refund bond;
		// this is always RUNE, and the MsgDeposit handler should have already deducted a network fee,
		// so this can be done with SendFromModuleToAccount even if under 0.02 RUNE
		// (which would cause TryAddTxOutItem to fail from no output after fee deduction)
		unbondCoin := common.NewCoin(common.RuneAsset(), amt)
		err = mgr.Keeper().SendFromModuleToAccount(ctx, BondName, provider.BondAddress, common.NewCoins(unbondCoin))
		if err != nil {
			return ErrInternal(err, "fail to send unbonded RUNE to bond address")
		}

		bondEvent := NewEventBond(amt, BondReturned, tx, nodeAcc, provider.BondAddress)
		if err := mgr.EventMgr().EmitEvent(ctx, bondEvent); err != nil {
			ctx.Logger().Error("fail to emit bond event", "error", err)
		}

		nodeAcc.Bond = common.SafeSub(nodeAcc.Bond, amt)
	}

	if nodeAcc.RequestedToLeave {
		// when node already request to leave , it can't come back , here means the node already unbond
		// so set the node to disabled status
		nodeAcc.UpdateStatus(NodeDisabled, ctx.BlockHeight())
	}
	if err := mgr.Keeper().SetNodeAccount(ctx, *nodeAcc); err != nil {
		ctx.Logger().Error("fail to save node account", "node_account", nodeAcc, "error", err)
		return err
	}
	if err := mgr.Keeper().SetBondProviders(ctx, bp); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save bond providers(%s)", bp.NodeAddress.String()))
	}

	return nil
}

// isSignedByActiveNodeAccounts check if all signers are active validator nodes
func isSignedByActiveNodeAccounts(ctx cosmos.Context, k keeper.Keeper, signers []cosmos.AccAddress) bool {
	if len(signers) == 0 {
		return false
	}
	for _, signer := range signers {
		if signer.Equals(k.GetModuleAccAddress(AsgardName)) {
			continue
		}
		if err := signedByActiveNodeAccount(ctx, k, signer); err != nil {
			ctx.Logger().Error("unauthorized account", "error", err)
			return false
		}
	}
	return true
}

func activeNodeAccountsSignerPriority(ctx cosmos.Context, k keeper.Keeper, signers []cosmos.AccAddress) (cosmos.Context, error) {
	if isSignedByActiveNodeAccounts(ctx, k, signers) {
		return ctx.WithPriority(ActiveNodePriority), nil
	}
	return ctx, cosmos.ErrUnauthorized(fmt.Sprintf("%+v are not authorized", signers))
}

// signedByActiveNodeAccounts returns an error unless all signers are active validator nodes
func signedByActiveNodeAccount(ctx cosmos.Context, k keeper.Keeper, signer cosmos.AccAddress) error {
	nodeAccount, err := k.GetNodeAccount(ctx, signer)
	if err != nil {
		return fmt.Errorf("error fetching node account: %s: %w", signer.String(), err)
	}
	if nodeAccount.IsEmpty() {
		return fmt.Errorf("node account is unexpectedly empty: %s", signer.String())
	}
	if nodeAccount.Status != NodeActive {
		return fmt.Errorf(
			"node account %s not active: %s",
			signer.String(),
			nodeAccount.Status,
		)
	}
	if nodeAccount.Type != NodeTypeValidator {
		return fmt.Errorf(
			"node account %s must be a validator: %s",
			signer.String(),
			nodeAccount.Type,
		)
	}

	return nil
}

func wrapError(ctx cosmos.Context, err error, wrap string) error {
	err = fmt.Errorf("%s: %w", wrap, err)
	ctx.Logger().Error(wrap, "error", err)
	return multierror.Append(errInternal, err)
}

// addGasFees to gas manager and deduct from vault
func addGasFees(ctx cosmos.Context, mgr Manager, tx ObservedTx) error {
	// If there's no gas, then nothing to do.
	if tx.Tx.Gas.IsEmpty() {
		return nil
	}

	if isTronZeroGasTx(tx) {
		return nil
	}

	// If the transaction wasn't from a known vault, then no relevance for known vaults or pools.
	if !mgr.Keeper().VaultExists(ctx, tx.ObservedPubKey) {
		return nil
	}

	// Since a known vault has spent gas, definitely deduct that gas from the vault's balance
	vault, err := mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		return err
	}
	// This covers both the cancel/approval tx path and the main non-slash
	// outbound gas path that go through addGasFees.
	// If the gas debit underflows, debitVault records deficit instead of
	// allowing the accounting gap to disappear silently.
	debitVault(ctx, mgr, &vault, tx, tx.Tx.Gas.ToCoins(), vaultDeficitReasonOutboundGas)
	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		return err
	}

	// If the vault is an InactiveVault doing an automatic refund,
	// any balance is not represented in the pools,
	// so the Reserve should not reimburse the gas pool.
	if vault.Status == InactiveVault {
		return nil
	}

	// when ragnarok is in progress, if the tx is for gas coin then don't reimburse the pool with reserve
	// liquidity providers they need to pay their own gas
	// if the outbound coin is not gas asset, then reserve will reimburse it , otherwise the gas asset pool will be in a loss
	if mgr.Keeper().RagnarokInProgress(ctx) {
		gasAsset := tx.Tx.Chain.GetGasAsset()
		if !tx.Tx.Coins.GetCoin(gasAsset).IsEmpty() {
			return nil
		}
	}

	// Add the gas to the gas manager to be reimbursed by the Reserve.
	outAsset := common.EmptyAsset
	if len(tx.Tx.Coins) != 0 {
		// Use the first Coin's Asset to indicate the associated outbound Asset for this Gas.
		outAsset = tx.Tx.Coins[0].Asset
	}
	mgr.GasMgr().AddGasAsset(outAsset, tx.Tx.Gas, true)
	return nil
}

func emitPoolBalanceChangedEvent(ctx cosmos.Context, poolMod PoolMod, reason string, mgr Manager) {
	evt := NewEventPoolBalanceChanged(poolMod, reason)
	if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit pool balance changed event", "error", err)
	}
}

func getSynthSupplyRemaining(ctx cosmos.Context, mgr Manager, asset common.Asset) (cosmos.Uint, error) {
	maxSynths, err := mgr.Keeper().GetMimir(ctx, constants.MaxSynthPerPoolDepth.String())
	if maxSynths < 0 || err != nil {
		maxSynths = mgr.GetConstants().GetInt64Value(constants.MaxSynthPerPoolDepth)
	}

	synthSupply := mgr.Keeper().GetTotalSupply(ctx, asset.GetSyntheticAsset())
	pool, err := mgr.Keeper().GetPool(ctx, asset.GetLayer1Asset())
	if err != nil {
		return cosmos.ZeroUint(), ErrInternal(err, "fail to get pool")
	}

	if pool.BalanceAsset.IsZero() {
		return cosmos.ZeroUint(), fmt.Errorf("pool(%s) has zero asset balance", pool.Asset.String())
	}

	maxSynthSupply := cosmos.NewUint(uint64(maxSynths)).Mul(pool.BalanceAsset.MulUint64(2)).QuoUint64(MaxWithdrawBasisPoints)
	if maxSynthSupply.LT(synthSupply) {
		return cosmos.ZeroUint(), fmt.Errorf("synth supply over target (%d/%d)", synthSupply.Uint64(), maxSynthSupply.Uint64())
	}

	return maxSynthSupply.Sub(synthSupply), nil
}

// isSynthMintPaused fails validation if synth supply is already too high, relative to pool depth
func isSynthMintPaused(ctx cosmos.Context, mgr Manager, targetAsset common.Asset, outputAmt cosmos.Uint) error {
	// check if the pool is in ragnarok
	k := "RAGNAROK-" + targetAsset.MimirString()
	v, err := mgr.Keeper().GetMimir(ctx, k)
	if err != nil {
		return err
	}
	if v > 0 {
		return fmt.Errorf("pool is in ragnarok")
	}

	mintHeight := mgr.Keeper().GetConfigInt64(ctx, constants.MintSynths)
	if mintHeight > 0 && ctx.BlockHeight() > mintHeight {
		return fmt.Errorf("minting synthetics has been disabled")
	}

	remaining, err := getSynthSupplyRemaining(ctx, mgr, targetAsset)
	if err != nil {
		return err
	}

	if remaining.LT(outputAmt) {
		return fmt.Errorf("insufficient synth capacity: want=%d have=%d", outputAmt.Uint64(), remaining.Uint64())
	}

	return nil
}

func telem(input cosmos.Uint) float32 {
	if !input.BigInt().IsUint64() {
		return 0
	}
	i := input.Uint64()
	return float32(i) / 100000000
}

func telemInt(input cosmos.Int) float32 {
	if !input.BigInt().IsInt64() {
		return 0
	}
	i := input.Int64()
	return float32(i) / 100000000
}

// telemChainNames is the set of chain label values a metric may carry, built once
// from common.AllChains.
var telemChainNames = func() map[string]struct{} {
	m := make(map[string]struct{}, len(common.AllChains))
	for _, c := range common.AllChains {
		m[c.String()] = struct{}{}
	}
	return m
}()

// telemChain renders a chain as a metric label value, collapsing anything outside
// common.AllChains to a fixed "other".
//
// Chain.Valid (common/chain.go) only checks length and uppercase-ness, and nothing
// upstream narrows it further: MsgObservedTxQuorum.ValidateBasic delegates to
// Tx.Valid, which only requires a non-empty Chain, and Address.IsChain accepts an
// unknown chain through its default branch. So a chain that arrives on an inbound
// message - or one derived from a user-supplied memo asset - is attacker-chosen,
// and emitting it verbatim mints unbounded Prometheus series on every
// telemetry-enabled validator, including from messages that were then rejected.
//
// Label normalisation only. It is deliberately not a validation helper: no
// consensus path calls it, and collapsing to "other" must never become a way to
// accept a chain a handler would otherwise reject.
func telemChain(c common.Chain) string {
	name := c.String()
	if _, ok := telemChainNames[name]; ok {
		return name
	}
	return "other"
}

// telemRaw renders an unscaled magnitude (unit counts, scores) as a gauge value.
// cosmos.Uint.Uint64() panics when out of range, and a panic here is swallowed by
// the recover in emitEndBlockTelemetry - which would silently stop every metric
// emitted after it, every block. Guard instead.
func telemRaw(input cosmos.Uint) float32 {
	if !input.BigInt().IsUint64() {
		return 0
	}
	return float32(input.Uint64())
}

func emitEndBlockTelemetry(ctx cosmos.Context, mgr Manager) error {
	// capture panics
	defer func() {
		if err := recover(); err != nil {
			ctx.Logger().Error("panic while emitting end block telemetry", "error", err)
			// the recover above makes a telemetry panic otherwise invisible to monitoring
			telemetry.IncrCounter(1, "thornode", "telemetry", "panic")
		}
	}()

	// Per-entity gauge loops (per-pool, per-node, per-vault) produce thousands of
	// gauge writes a block, and the go-metrics sink layer - not the keeper reads -
	// is where that time goes. Prometheus scrapes every 15-30s (2.5-5 blocks) and
	// go-metrics gauges retain their last value (prometheus_retention_time=600s in
	// config/default.yaml), so a series refreshed every 5 blocks is indistinguishable
	// to a scraper from one refreshed every block. Each group gets a different
	// residue so the cost is spread across the period instead of landing on one
	// block. Small scalar gauges an operator alerts on for liveness stay per-block.
	//
	// Residue choice is deliberate: every pre-existing IsPeriodLastBlock(10/50/100/600)
	// gate in the telemetry_*.go emitters also fires at height%5==0, so the heaviest
	// group (pools, ~16 gauges x ~100 pools) is put on residue 2 and the lightest of the
	// three (vaults) shares residue 0 with them. Residues 3 and 4 are mimir and secured
	// assets, gated inside their own emitters below. Residue 1 also carries the
	// node-store scans in emitNetworkStateTelemetry, which are gated there so all
	// node-account walks land on the same block as the per-node loop here.
	emitVaults := ctx.BlockHeight()%5 == 0
	emitNodes := ctx.BlockHeight()%5 == 1
	emitPools := ctx.BlockHeight()%5 == 2

	// emit network data
	network, err := mgr.Keeper().GetNetwork(ctx)
	if err != nil {
		return err
	}

	telemetry.SetGauge(telem(network.BondRewardRune), "thornode", "network", "bond_reward_rune")
	telemetry.SetGauge(telemRaw(network.TotalBondUnits), "thornode", "network", "total_bond_units")
	telemetry.SetGauge(float32(network.LPIncomeSplit), "thornode", "network", "lp_income_split")
	telemetry.SetGauge(float32(network.NodeIncomeSplit), "thornode", "network", "node_income_split")
	telemetry.SetGauge(telem(cosmos.NewUint(network.OutboundGasSpentRune)), "thornode", "network", "outbound_gas_spent_rune")
	telemetry.SetGauge(telem(cosmos.NewUint(network.OutboundGasWithheldRune)), "thornode", "network", "outbound_gas_withheld_rune")

	// emit protocol owned liquidity data
	pol, err := mgr.Keeper().GetPOL(ctx)
	if err != nil {
		return err
	}
	telemetry.SetGauge(telem(pol.RuneDeposited), "thornode", "pol", "rune_deposited")
	telemetry.SetGauge(telem(pol.RuneWithdrawn), "thornode", "pol", "rune_withdrawn")
	telemetry.SetGauge(telemInt(pol.CurrentDeposit()), "thornode", "pol", "current_deposit")
	polValue, err := polPoolValue(ctx, mgr)
	if err == nil {
		telemetry.SetGauge(telem(polValue), "thornode", "pol", "value")
		telemetry.SetGauge(telemInt(pol.PnL(polValue)), "thornode", "pol", "pnl")
	}

	// emit module balances
	for _, name := range []string{ReserveName, AsgardName, BondName} {
		modAddr := mgr.Keeper().GetModuleAccAddress(name)
		bal := mgr.Keeper().GetBalance(ctx, modAddr)
		modLabel := telemetry.NewLabel("module", name)
		for _, coin := range bal {
			denom := telemetry.NewLabel("denom", coin.Denom)
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "module", "balance"},
				telemInt(coin.Amount),
				[]metrics.Label{modLabel, denom},
			)
		}
	}

	// emit node metrics
	nodes, err := mgr.Keeper().ListValidatorsWithBond(ctx)
	if err != nil {
		return err
	}
	totalActiveBond := cosmos.ZeroUint()
	for _, node := range nodes {
		// totalActiveBond feeds the per-block network.total_active_bond and
		// security_ratio scalars, so it is accumulated every block regardless of
		// whether the per-node series are refreshed this block.
		if node.Status == NodeActive {
			totalActiveBond = totalActiveBond.Add(node.Bond)
		}
		if !emitNodes {
			continue
		}

		// String() once per node: it is the dominant per-iteration allocation and
		// four of the label sets below need it.
		addr := node.NodeAddress.String()
		nodeLabel := []metrics.Label{telemetry.NewLabel("node_address", addr)}
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "node", "bond"},
			telem(node.Bond),
			[]metrics.Label{telemetry.NewLabel("node_address", addr), telemetry.NewLabel("status", node.Status.String())},
		)

		// info-style gauge (always 1) so `count by (version)` / `by (status)` works in PromQL
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "node", "info"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("node_address", addr),
				telemetry.NewLabel("status", node.Status.String()),
				telemetry.NewLabel("version", node.Version),
			},
		)
		telemetry.SetGaugeWithLabels([]string{"thornode", "node", "missing_blocks"}, float32(node.MissingBlocks), nodeLabel)
		telemetry.SetGaugeWithLabels([]string{"thornode", "node", "requested_to_leave"}, boolTelem(node.RequestedToLeave), nodeLabel)
		telemetry.SetGaugeWithLabels([]string{"thornode", "node", "forced_to_leave"}, boolTelem(node.ForcedToLeave), nodeLabel)
		telemetry.SetGaugeWithLabels([]string{"thornode", "node", "maintenance"}, boolTelem(node.Maintenance), nodeLabel)

		// jail: blocks remaining until release (0 when not jailed)
		jailBlocks := float32(0)
		if jail, jerr := mgr.Keeper().GetNodeAccountJail(ctx, node.NodeAddress); jerr == nil && jail.ReleaseHeight > ctx.BlockHeight() {
			jailBlocks = float32(jail.ReleaseHeight - ctx.BlockHeight())
		}
		telemetry.SetGaugeWithLabels([]string{"thornode", "node", "jail_blocks_remaining"}, jailBlocks, nodeLabel)

		pts, perr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
		if perr != nil {
			continue
		}
		// same label content as nodeLabel; reuse it rather than rebuilding the slice.
		telemetry.SetGaugeWithLabels([]string{"thornode", "node", "slash_points"}, float32(pts), nodeLabel)

		age := cosmos.NewUint(uint64((ctx.BlockHeight() - node.StatusSince) * common.One))
		if pts > 0 {
			leaveScore := age.QuoUint64(uint64(pts))
			telemetry.SetGaugeWithLabels([]string{"thornode", "node", "leave_score"}, telemRaw(leaveScore), nodeLabel)
		}
	}

	// get 1 RUNE price in USD
	runeUSDPrice := telem(mgr.Keeper().DollarsPerRune(ctx))
	telemetry.SetGauge(runeUSDPrice, "thornode", "price", "usd", "thor", "rune")

	// emit pool metrics
	pools, err := mgr.Keeper().GetPools(ctx)
	if err != nil {
		return err
	}
	totalPooledRune := cosmos.ZeroUint()
	for _, pool := range pools {
		if pool.LPUnits.IsZero() {
			continue
		}
		// totalPooledRune feeds the per-block network.total_pooled_rune and
		// security_ratio scalars - accumulate it every block, gate only the
		// per-pool series (and the seven keeper reads that exist solely to feed them).
		totalPooledRune = totalPooledRune.Add(pool.BalanceRune)
		if !emitPools {
			continue
		}
		synthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		labels := []metrics.Label{telemetry.NewLabel("pool", pool.Asset.String()), telemetry.NewLabel("status", pool.Status.String())}
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "balance", "synth"}, telem(synthSupply), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "balance", "rune"}, telem(pool.BalanceRune), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "balance", "asset"}, telem(pool.BalanceAsset), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "pending", "rune"}, telem(pool.PendingInboundRune), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "pending", "asset"}, telem(pool.PendingInboundAsset), labels)

		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "units", "pool"}, telem(pool.CalcUnits(synthSupply)), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "units", "lp"}, telem(pool.LPUnits), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "units", "synth"}, telem(pool.SynthUnits), labels)

		// pricing
		price := float32(0)
		if !pool.BalanceAsset.IsZero() {
			price = runeUSDPrice * telem(pool.BalanceRune) / telem(pool.BalanceAsset)
		}
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "price", "usd"}, price, labels)

		// trade accounts
		var tu TradeUnit
		tu, err = mgr.Keeper().GetTradeUnit(ctx, pool.Asset.GetTradeAsset())
		if err != nil {
			ctx.Logger().Error("fail to get trade unit", "error", err)
			continue
		}
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "tradeasset", "units"}, telem(tu.Units), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "tradeasset", "depth"}, telem(tu.Depth), labels)

		// fee & slip pressure per pool
		if fee, ferr := mgr.Keeper().GetRollingPoolLiquidityFee(ctx, pool.Asset); ferr == nil {
			telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "liquidity_fee", "rolling"}, telem(cosmos.NewUint(fee)), labels)
		}
		if fee, ferr := mgr.Keeper().GetEffectivePoolLiquidityFee(ctx, pool.Asset); ferr == nil {
			telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "liquidity_fee", "effective"}, telem(cosmos.NewUint(fee)), labels)
		}
		if slip, serr := mgr.Keeper().GetLongRollup(ctx, pool.Asset); serr == nil {
			telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "swap_slip", "long_rollup"}, float32(slip), labels)
		}

		// NOTE: the outbound_fee withheld/spent/surplus gauges used to be emitted
		// here via Keeper.GetOutboundFeeWithheldRune. That accessor is NOT read-only:
		// on a zero read it lazily writes the equilibrium surplus back to the store
		// (keeper/v1/keeper_outbound_fees.go). emitEndBlockTelemetry runs only on
		// nodes with telemetry.enabled, so that write happened on some validators and
		// not others - a state divergence. They now live in emitQueueDetailTelemetry,
		// which resolves the same value read-only through the store iterator.

		// halt / pause state, per pool
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "deposit_paused"}, boolTelem(mgr.Keeper().IsPoolDepositPaused(ctx, pool.Asset)), labels)
		ragStart, rerr := mgr.Keeper().GetPoolRagnarokStart(ctx, pool.Asset)
		telemetry.SetGaugeWithLabels([]string{"thornode", "pool", "ragnarok"}, boolTelem(rerr == nil && ragStart > 0), labels)
	}
	telemetry.SetGauge(telem(totalPooledRune), "thornode", "network", "total_pooled_rune")
	telemetry.SetGauge(telem(totalActiveBond), "thornode", "network", "total_active_bond")
	if !totalPooledRune.IsZero() {
		// bond/liquidity security ratio: below 1 means pools out-value the bond securing them
		telemetry.SetGauge(telem(totalActiveBond)/telem(totalPooledRune), "thornode", "network", "security_ratio")
	}

	// emit vault metrics
	asgards, _ := mgr.Keeper().GetAsgardVaults(ctx)
	vaultCounts := map[VaultStatus]int{}
	vaultAssetTotals := map[string]cosmos.Uint{}
	for _, vault := range asgards {
		// vault.count by status is four cheap scalars and the stuck-churn alarm -
		// keep it per-block. The per-vault and per-coin series below are gated.
		vaultCounts[vault.Status]++
		if !emitVaults {
			continue
		}
		if vault.Status != ActiveVault && vault.Status != RetiringVault {
			continue
		}
		for _, coin := range vault.Coins {
			key := coin.Asset.GetLayer1Asset().String()
			if cur, ok := vaultAssetTotals[key]; ok {
				vaultAssetTotals[key] = cur.Add(coin.Amount)
			} else {
				vaultAssetTotals[key] = coin.Amount
			}
		}

		// calculate the total value of this vault
		totalValue := cosmos.ZeroUint()
		for _, coin := range vault.Coins {
			if coin.IsRune() {
				totalValue = totalValue.Add(coin.Amount)
			} else {
				var pool Pool
				pool, err = mgr.Keeper().GetPool(ctx, coin.Asset.GetLayer1Asset())
				if err != nil {
					continue
				}
				totalValue = totalValue.Add(pool.AssetValueInRune(coin.Amount))
			}
		}
		// vault_type/pubkey are invariant across this vault's coins - build the two
		// Labels (and the vault.balance key) once instead of per coin.
		typeLabel := telemetry.NewLabel("vault_type", vault.Type.String())
		pkLabel := telemetry.NewLabel("pubkey", vault.PubKey.String())
		labels := []metrics.Label{typeLabel, pkLabel}
		telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "total_value"}, telem(totalValue), labels)

		balanceKey := []string{"thornode", "vault", "balance"}
		for _, coin := range vault.Coins {
			vaultCoinLabel := []metrics.Label{typeLabel, pkLabel, telemetry.NewLabel("asset", coin.Asset.String())}
			telemetry.SetGaugeWithLabels(balanceKey, telem(coin.Amount), vaultCoinLabel)
		}
	}

	// vault counts by status - a retiring count that never returns to zero means a stuck churn
	for _, status := range []VaultStatus{ActiveVault, RetiringVault, InactiveVault, InitVault} {
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "count"},
			float32(vaultCounts[status]),
			[]metrics.Label{telemetry.NewLabel("status", status.String())},
		)
	}

	// Vault backing: what the asgards actually custody for an L1 asset vs everything
	// that asset backs. Vaults hold more than the pool depth - trade account and
	// secured asset depth are custodied in the same vaults - so comparing against
	// pool.BalanceAsset alone would leave this permanently negative and useless.
	// A delta that trends negative is funds going missing. It is subtracted on
	// cosmos.Uint before the float32 conversion (solvencyDelta) so a deficit smaller
	// than one float32 ULP of the balance is not quantised to zero.
	//
	// Gated with the rest of the vault group: vaultAssetTotals is only populated on
	// those blocks, and this loop is three gauges plus two keeper reads per pool.
	if emitVaults {
		for _, pool := range pools {
			if pool.LPUnits.IsZero() {
				continue
			}
			// External layer-1 pools only. GetPools also returns savers pools
			// (BTC/BTC) and native pools (THOR.TCY), both of which can carry
			// LPUnits while their assets live in module accounts rather than in a
			// vault - AsgardInvariant accounts for those module balances
			// separately. vaultAssetTotals is keyed by the layer-1 assets the
			// asgards actually custody, so those pools would report held == 0
			// against a positive backing and pin vault_backing_delta negative
			// forever on healthy accounting.
			if !pool.Asset.IsExternalL1Asset() {
				continue
			}
			assetStr := pool.Asset.String()
			held := cosmos.ZeroUint()
			if amt, ok := vaultAssetTotals[assetStr]; ok {
				held = amt
			}
			backing := pool.BalanceAsset
			if tu, terr := mgr.Keeper().GetTradeUnit(ctx, pool.Asset.GetTradeAsset()); terr == nil {
				backing = backing.Add(tu.Depth)
			}
			if sa, serr := mgr.Keeper().GetSecuredAsset(ctx, pool.Asset.GetSecuredAsset()); serr == nil {
				backing = backing.Add(sa.Depth)
			}
			labels := []metrics.Label{telemetry.NewLabel("pool", assetStr)}
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "backing", "held"}, telem(held), labels)
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "backing", "owed"}, telem(backing), labels)
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "backing", "delta"}, solvencyDelta(held, backing), labels)
		}
	}

	// emit queue metrics
	signingTransactionPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	startHeight := ctx.BlockHeight() - signingTransactionPeriod
	txOutDelayMax, err := mgr.Keeper().GetMimir(ctx, constants.TxOutDelayMax.String())
	if txOutDelayMax <= 0 || err != nil {
		txOutDelayMax = mgr.GetConstants().GetInt64Value(constants.TxOutDelayMax)
	}
	maxTxOutOffset, err := mgr.Keeper().GetMimir(ctx, constants.MaxTxOutOffset.String())
	if maxTxOutOffset <= 0 || err != nil {
		maxTxOutOffset = mgr.GetConstants().GetInt64Value(constants.MaxTxOutOffset)
	}
	var queueSwap, queueInternal, queueOutbound int64
	queueScheduledOutboundValue := cosmos.ZeroUint()
	iterator := mgr.Keeper().GetSwapQueueIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &msg); err != nil {
			continue
		}
		queueSwap++
	}
	for height := startHeight; height <= ctx.BlockHeight(); height++ {
		txs, err := mgr.Keeper().GetTxOut(ctx, height)
		if err != nil {
			continue
		}
		for _, tx := range txs.TxArray {
			if tx.OutHash.IsEmpty() {
				memo, _ := ParseMemo(mgr.GetVersion(), tx.Memo)
				if memo.IsInternal() {
					queueInternal++
				} else if memo.IsOutbound() {
					queueOutbound++
				}
			}
		}
	}
	cloutSpent := cosmos.ZeroUint()
	for height := ctx.BlockHeight() + 1; height <= ctx.BlockHeight()+txOutDelayMax; height++ {
		value, clout, err := mgr.Keeper().GetTxOutValue(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			continue
		}
		if height > ctx.BlockHeight()+maxTxOutOffset && value.IsZero() {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		queueScheduledOutboundValue = queueScheduledOutboundValue.Add(value)
		cloutSpent = cloutSpent.Add(clout)
	}
	telemetry.SetGauge(float32(queueInternal), "thornode", "queue", "internal")
	telemetry.SetGauge(float32(queueOutbound), "thornode", "queue", "outbound")
	telemetry.SetGauge(float32(queueSwap), "thornode", "queue", "swap")
	telemetry.SetGauge(telem(cloutSpent), "thornode", "queue", "scheduled", "clout", "rune")
	telemetry.SetGauge(telem(cloutSpent)*runeUSDPrice, "thornode", "queue", "scheduled", "clout", "usd")
	telemetry.SetGauge(telem(queueScheduledOutboundValue), "thornode", "queue", "scheduled", "value", "rune")
	telemetry.SetGauge(telem(queueScheduledOutboundValue)*runeUSDPrice, "thornode", "queue", "scheduled", "value", "usd")

	// NOTE: `pools` and `asgards` above are already fetched this block.
	// emitQueueDetailTelemetry re-runs GetPools and emitVaultDetailTelemetry re-runs
	// GetAsgardVaults. Once those take the slice as a parameter -
	// emitQueueDetailTelemetry(ctx, mgr, pools) / emitVaultDetailTelemetry(ctx, mgr, asgards) -
	// pass them here and drop the duplicate reads.
	emitChainTelemetry(ctx, mgr)
	emitNetworkStateTelemetry(ctx, mgr)
	emitStreamingSwapTelemetry(ctx, mgr)
	emitSecuredAssetTelemetry(ctx, mgr)
	emitMimirTelemetry(ctx, mgr)
	emitVaultDetailTelemetry(ctx, mgr)
	emitSolvencyTelemetry(ctx, mgr)
	emitChainDetailTelemetry(ctx, mgr)
	emitQueueDetailTelemetry(ctx, mgr)
	emitTCYTelemetry(ctx, mgr)

	return nil
}

// boolTelem renders a boolean as a 0/1 gauge.
func boolTelem(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// emitChainTelemetry emits per-chain observation, halt and fee state. This is the
// primary "is a chain wedged" signal: last_observed_height going flat while other
// chains advance means bifrost observation has stalled for that chain.
func emitChainTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()
	for _, chain := range common.AllChains {
		labels := []metrics.Label{telemetry.NewLabel("chain", chain.String())}

		height, err := k.GetLastChainHeight(ctx, chain)
		if err != nil {
			ctx.Logger().Error("fail to get last chain height for telemetry", "chain", chain, "error", err)
			continue
		}
		if height == 0 {
			// chain not observed on this network, don't emit a series for it
			continue
		}
		telemetry.SetGaugeWithLabels([]string{"thornode", "chain", "last_observed_height"}, float32(height), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "chain", "halted"}, boolTelem(k.IsChainHalted(ctx, chain)), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "chain", "trading_halted"}, boolTelem(k.IsChainTradingHalted(ctx, chain)), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "chain", "lp_paused"}, boolTelem(k.IsLPPaused(ctx, chain)), labels)

		nf, err := k.GetNetworkFee(ctx, chain)
		if err != nil {
			continue
		}
		telemetry.SetGaugeWithLabels([]string{"thornode", "chain", "network_fee", "tx_size"}, float32(nf.TransactionSize), labels)
		telemetry.SetGaugeWithLabels([]string{"thornode", "chain", "network_fee", "rate"}, float32(nf.TransactionFeeRate), labels)
	}
}

// emitNetworkStateTelemetry emits churn, version, global halt and RUNEPool state.
func emitNetworkStateTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()

	// global halts
	telemetry.SetGauge(boolTelem(k.IsGlobalTradingHalted(ctx)), "thornode", "network", "trading_halted")
	telemetry.SetGauge(boolTelem(k.RagnarokInProgress(ctx)), "thornode", "network", "ragnarok_in_progress")

	// signing liveness
	if h, err := k.GetLastSignedHeight(ctx); err == nil {
		telemetry.SetGauge(float32(h), "thornode", "network", "last_signed_height")
	}

	// churn: blocks since the last successful churn is the stuck-churn alarm
	lastChurn := getLastChurnHeight(ctx, k)
	telemetry.SetGauge(float32(lastChurn), "thornode", "churn", "last_height")
	telemetry.SetGauge(float32(ctx.BlockHeight()-lastChurn), "thornode", "churn", "blocks_since")

	// The two blocks below are the expensive half of this emitter: six full walks
	// of the node-account store, each unmarshalling every NodeAccount.
	// GetMinJoinVersion and GetLowestActiveVersion scan it once apiece, and
	// ListValidatorsByStatus once per status. They share residue 1 with the
	// per-node gauge loop in emitEndBlockTelemetry so all node-store work lands on
	// one block of the five rather than every block. Neither series is a liveness
	// alarm - node counts move on join/leave and versions on the timescale of a
	// churn - and go-metrics gauges hold their last value between updates, so a
	// scraper cannot tell the difference. The scalar halt, signing and churn gauges
	// above stay per-block.
	if ctx.BlockHeight()%5 == 1 {
		// version consensus: a min_join above lowest_active means nodes are being churned out on version
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "network", "version_info"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("consensus", mgr.GetVersion().String()),
				telemetry.NewLabel("min_join", k.GetMinJoinVersion(ctx).String()),
				telemetry.NewLabel("lowest_active", k.GetLowestActiveVersion(ctx).String()),
			},
		)

		// node counts by status - avoids a PromQL aggregation over per-node series
		for _, status := range []NodeStatus{NodeActive, NodeStandby, NodeReady, NodeDisabled} {
			nas, err := k.ListValidatorsByStatus(ctx, status)
			if err != nil {
				continue
			}
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "node", "count"},
				float32(len(nas)),
				[]metrics.Label{telemetry.NewLabel("status", status.String())},
			)
		}
	}

	// RUNEPool
	if rp, err := k.GetRUNEPool(ctx); err == nil {
		telemetry.SetGauge(telem(rp.PoolUnits), "thornode", "runepool", "pool_units")
		telemetry.SetGauge(telem(rp.ReserveUnits), "thornode", "runepool", "reserve_units")
		telemetry.SetGauge(telem(rp.RuneDeposited), "thornode", "runepool", "rune_deposited")
		telemetry.SetGauge(telem(rp.RuneWithdrawn), "thornode", "runepool", "rune_withdrawn")
	}
	if v, err := runePoolValue(ctx, mgr); err == nil {
		telemetry.SetGauge(telem(v), "thornode", "runepool", "value")
	}
}

// emitStreamingSwapTelemetry emits in-flight streaming swap depth. A rising
// remaining-count with a flat completion rate means streaming swaps are stalling.
func emitStreamingSwapTelemetry(ctx cosmos.Context, mgr Manager) {
	iter := mgr.Keeper().GetStreamingSwapIterator(ctx)
	defer iter.Close()

	var count, remaining, failed int64
	deposited := cosmos.ZeroUint()
	swappedIn := cosmos.ZeroUint()
	for ; iter.Valid(); iter.Next() {
		var swp StreamingSwap
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &swp); err != nil {
			continue
		}
		count++
		if swp.Quantity > swp.Count {
			remaining += int64(swp.Quantity - swp.Count)
		}
		failed += int64(len(swp.FailedSwaps))
		deposited = deposited.Add(swp.Deposit)
		swappedIn = swappedIn.Add(swp.In)
	}
	telemetry.SetGauge(float32(count), "thornode", "streaming_swap", "count")
	telemetry.SetGauge(float32(remaining), "thornode", "streaming_swap", "sub_swaps_remaining")
	telemetry.SetGauge(float32(failed), "thornode", "streaming_swap", "failed_sub_swaps")
	telemetry.SetGauge(telem(deposited), "thornode", "streaming_swap", "deposited")
	telemetry.SetGauge(telem(swappedIn), "thornode", "streaming_swap", "swapped_in")
}

// refundCodeLabel maps a refund code to a bounded, readable label. Named cases are
// the ones worth alerting on separately; anything else falls back to its numeric code,
// which keeps the label bounded without needing this list kept in sync with errors.go.
func refundCodeLabel(code uint32) string {
	switch code {
	case CodeTxFail:
		return "tx_fail"
	case CodeInvalidVault:
		return "invalid_vault"
	case CodeInvalidMemo:
		return "invalid_memo"
	case CodeSwapFail:
		return "swap_fail"
	case CodeInternalError:
		return "internal_error"
	case CodeInvalidPoolStatus:
		return "invalid_pool_status"
	default:
		return fmt.Sprintf("%d", code)
	}
}

// emitMimirTelemetry emits every explicitly-set mimir value as a gauge. Mimir is the
// operator control plane - a key left at, or set to, the wrong value is a classic mainnet
// incident - and nothing else exposes it to Prometheus, so today it can only be diffed by
// polling the REST API. A change shows up as a step in the series (PromQL
// changes()/delta()), which is what an alert or an agent watches.
//
// Cardinality is bounded by operator action: the mimir store only holds keys that have
// actually been set, so unset keys falling back to their constant default emit nothing.
//
// Gated to every 5th block (residue 3, staggered against the pool/node/vault groups):
// with ~200 set keys this is one of the larger per-entity loops, and Prometheus scrapes
// no faster than every 15s. Cost of the gate: the step edge is located to within 5
// blocks (~30s) rather than 1, and a mimir set and then unset again inside a single
// 5-block window is invisible - the series never shows the intermediate value.
//
// Note: gauges are float32, so values above 2^24 - which includes the block-height-valued
// halt keys - quantise. Use these to see WHICH key changed and roughly when, not as an
// exact height.
func emitMimirTelemetry(ctx cosmos.Context, mgr Manager) {
	if ctx.BlockHeight()%5 != 3 {
		return
	}

	iter := mgr.Keeper().GetMimirIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		value := types.ProtoInt64{}
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &value); err != nil {
			continue
		}
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "mimir", "value"},
			float32(value.GetValue()),
			[]metrics.Label{telemetry.NewLabel("key", trimKeyPrefix(iter.Key()))},
		)
	}
}

// emitSecuredAssetTelemetry emits secured asset depth per asset.
//
// Gated to every 5th block (residue 4, staggered against the pool/node/vault/mimir
// groups): one gauge per secured asset, a depth series read by a scraper that samples
// no faster than every 15s. Cost of the gate: a depth change is timestamped to within
// 5 blocks (~30s), and depth is a level, not a counter, so no movement is lost - only
// intra-window intermediate values are.
func emitSecuredAssetTelemetry(ctx cosmos.Context, mgr Manager) {
	if ctx.BlockHeight()%5 != 4 {
		return
	}

	iter := mgr.Keeper().GetSecuredAssetIterator(ctx)
	defer iter.Close()

	depthKey := []string{"thornode", "secured_asset", "depth"}
	for ; iter.Valid(); iter.Next() {
		var sa SecuredAsset
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &sa); err != nil {
			continue
		}
		telemetry.SetGaugeWithLabels(
			depthKey,
			telem(sa.Depth),
			[]metrics.Label{telemetry.NewLabel("asset", sa.Asset.String())},
		)
	}
}

func getAvailablePoolsRune(ctx cosmos.Context, keeper keeper.Keeper) (Pools, cosmos.Uint, error) {
	// Get Available layer 1 pools and sum their RUNE balances.
	availablePoolsRune := cosmos.ZeroUint()
	var availablePools Pools
	iterator := keeper.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := keeper.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, cosmos.ZeroUint(), fmt.Errorf("fail to unmarshal pool: %w", err)
		}
		if !pool.IsAvailable() {
			continue
		}
		if pool.Asset.IsNative() {
			continue
		}
		if pool.BalanceRune.IsZero() {
			continue
		}
		availablePoolsRune = availablePoolsRune.Add(pool.BalanceRune)
		availablePools = append(availablePools, pool)
	}
	return availablePools, availablePoolsRune, nil
}

func getVaultsLiquidityRune(ctx cosmos.Context, keeper keeper.Keeper) (cosmos.Uint, error) {
	// Sum the RUNE values of non-Inactive vault Coins.
	vaultsLiquidityRune := cosmos.ZeroUint()
	poolCache := map[common.Asset]Pool{}
	vaults, err := keeper.GetAsgardVaults(ctx)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get vaults: %w", err)
	}
	for i := range vaults {
		// cleanupAsgardIndex removes InactiveVaults from the index on churn,
		// but RetiringVaults which become InactiveVaults and later receive inbounds
		// are not cleared from the index until the next churn,
		// so check nevertheless.
		// Similarly, an InactiveVault inbound (to be automatically refunded)
		// re-adds that InactiveVault to the Asgard Index with SetVault
		// until cleared again in the next churn.
		if vaults[i].Status == InactiveVault {
			continue
		}

		for _, coin := range vaults[i].Coins {
			if coin.IsRune() {
				vaultsLiquidityRune = vaultsLiquidityRune.Add(coin.Amount)
				continue
			}

			pool, ok := poolCache[coin.Asset]
			if !ok {
				pool, err = keeper.GetPool(ctx, coin.Asset)
				if err != nil {
					return cosmos.ZeroUint(), fmt.Errorf("fail to get pool for asset %s, err:%w", coin.Asset, err)
				}
				poolCache[coin.Asset] = pool
			}

			vaultsLiquidityRune = vaultsLiquidityRune.Add(pool.AssetValueInRune(coin.Amount))
		}
	}
	return vaultsLiquidityRune, nil
}

// get the total bond of the bottom 2/3rds active nodes
func getEffectiveSecurityBond(nas NodeAccounts) cosmos.Uint {
	amt := cosmos.ZeroUint()
	sort.SliceStable(nas, func(i, j int) bool {
		return nas[i].Bond.LT(nas[j].Bond)
	})
	t := len(nas) * 2 / 3
	if len(nas)%3 == 0 {
		t -= 1
	}
	for i, na := range nas {
		if i <= t {
			amt = amt.Add(na.Bond)
		}
	}
	return amt
}

// Calculates total "effective bond" - the total bond when taking into account the
// Bond-weighted hard-cap
func getTotalEffectiveBond(nas NodeAccounts) (cosmos.Uint, cosmos.Uint) {
	bondHardCap := getHardBondCap(nas)

	totalEffectiveBond := cosmos.ZeroUint()
	for _, item := range nas {
		b := item.Bond
		if item.Bond.GT(bondHardCap) {
			b = bondHardCap
		}

		totalEffectiveBond = totalEffectiveBond.Add(b)
	}

	return totalEffectiveBond, bondHardCap
}

// find the bond size the highest of the bottom 2/3rds node bonds
func getHardBondCap(nas NodeAccounts) cosmos.Uint {
	if len(nas) == 0 {
		return cosmos.ZeroUint()
	}
	sort.SliceStable(nas, func(i, j int) bool {
		return nas[i].Bond.LT(nas[j].Bond)
	})
	i := len(nas) * 2 / 3
	if len(nas)%3 == 0 {
		i -= 1
	}
	return nas[i].Bond
}

// From a list of (active) nodes, get a list of those not in a list (of signers).
func getNonSigners(nas []NodeAccount, signers []cosmos.AccAddress) []cosmos.AccAddress {
	var nonSigners []cosmos.AccAddress
	var signed bool

	for _, na := range nas {
		signed = false
		for _, signer := range signers {
			if na.NodeAddress.Equals(signer) {
				signed = true
				break
			}
		}

		if !signed {
			nonSigners = append(nonSigners, na.NodeAddress)
		}
	}
	return nonSigners
}

// In the case where the max gas of the chain of a queued outbound tx has changed
// Update the ObservedTxVoter so the network can still match the outbound with
// the observed inbound
func updateTxOutGas(ctx cosmos.Context, keeper keeper.Keeper, txOut types.TxOutItem, gas common.Gas) error {
	// When txOut.InHash is 0000000000000000000000000000000000000000000000000000000000000000 , which means the outbound is trigger by the network internally
	// For example , migration, etc. there is no related inbound observation , thus doesn't need to try to find it and update anything
	if txOut.InHash == common.BlankTxID {
		return nil
	}
	voter, err := keeper.GetObservedTxInVoter(ctx, txOut.InHash)
	if err != nil {
		return err
	}

	txOutIndex := -1
	for i, tx := range voter.Actions {
		if tx.Equals(txOut) {
			txOutIndex = i
			voter.Actions[txOutIndex].MaxGas = gas
			keeper.SetObservedTxInVoter(ctx, voter)
			break
		}
	}

	if txOutIndex == -1 {
		return fmt.Errorf("fail to find tx out in ObservedTxVoter %s", txOut.InHash)
	}

	return nil
}

// In the case where the gas rate of the chain of a queued outbound tx has changed
// Update the ObservedTxVoter so the network can still match the outbound with
// the observed inbound
func updateTxOutGasRate(ctx cosmos.Context, keeper keeper.Keeper, txOut types.TxOutItem, gasRate int64) error {
	// When txOut.InHash is 0000000000000000000000000000000000000000000000000000000000000000 , which means the outbound is trigger by the network internally
	// For example , migration, etc. there is no related inbound observation , thus doesn't need to try to find it and update anything
	if txOut.InHash == common.BlankTxID {
		return nil
	}
	voter, err := keeper.GetObservedTxInVoter(ctx, txOut.InHash)
	if err != nil {
		return err
	}

	txOutIndex := -1
	for i, tx := range voter.Actions {
		if tx.Equals(txOut) {
			txOutIndex = i
			voter.Actions[txOutIndex].GasRate = gasRate
			keeper.SetObservedTxInVoter(ctx, voter)
			break
		}
	}

	if txOutIndex == -1 {
		return fmt.Errorf("fail to find tx out in ObservedTxVoter %s", txOut.InHash)
	}

	return nil
}

// backfill bond provider information (passive migration code)
func passiveBackfill(ctx cosmos.Context, mgr Manager, nodeAccount NodeAccount, bp *BondProviders) error {
	if len(bp.Providers) == 0 {
		// no providers yet, add node operator bond address to the bond provider list
		nodeOpBondAddr, err := nodeAccount.BondAddress.AccAddress()
		if err != nil {
			return ErrInternal(err, fmt.Sprintf("fail to parse bond address(%s)", nodeAccount.BondAddress))
		}
		p := NewBondProvider(nodeOpBondAddr)
		p.Bond = nodeAccount.Bond
		bp.Providers = append(bp.Providers, p)
		defaultNodeOperationFee := mgr.Keeper().GetConfigInt64(ctx, constants.NodeOperatorFee)
		bp.NodeOperatorFee = cosmos.NewUint(uint64(defaultNodeOperationFee))
	}

	return nil
}

// atTVLCap - returns bool on if we've hit the TVL hard cap. Coins passed in
// are included in the calculation
func atTVLCap(ctx cosmos.Context, coins common.Coins, mgr Manager) bool {
	// Check if StrictBondLiquidityRatio is enabled
	strictBondLiquidityRatio := mgr.GetConstants().GetBoolValue(constants.StrictBondLiquidityRatio)
	if !strictBondLiquidityRatio {
		return false
	}

	vaults, err := mgr.Keeper().GetAsgardVaults(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get vaults for atTVLCap", "error", err)
		return true
	}

	// coins must be copied to a new variable to avoid modifying the original
	coins = coins.Copy()
	for _, vault := range vaults {
		if vault.IsAsgard() && (vault.IsActive() || vault.IsRetiring()) {
			coins = coins.Add(vault.Coins...)
		}
	}

	runeCoin := coins.GetCoin(common.RuneAsset())
	totalRuneValue := runeCoin.Amount
	for _, coin := range coins {
		if coin.IsEmpty() {
			continue
		}
		asset := coin.Asset
		// while asgard vaults don't contain native assets, the `coins`
		// parameter might
		if asset.IsSyntheticAsset() {
			asset = asset.GetLayer1Asset()
		}
		var pool Pool
		pool, err = mgr.Keeper().GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("fail to get pool for atTVLCap", "asset", coin.Asset, "error", err)
			continue
		}
		if !pool.IsAvailable() && !pool.IsStaged() {
			continue
		}
		if pool.BalanceRune.IsZero() || pool.BalanceAsset.IsZero() {
			continue
		}
		if pool.Asset.IsNative() {
			continue
		}
		totalRuneValue = totalRuneValue.Add(pool.AssetValueInRune(coin.Amount))
	}

	// get effectiveSecurity
	var nodeAccounts NodeAccounts
	nodeAccounts, err = mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get validators to calculate TVL cap", "error", err)
		return true
	}

	tvlCapBasisPoints := mgr.Keeper().GetConfigInt64(ctx, constants.TVLCapBasisPoints)
	security := cosmos.ZeroUint()
	if tvlCapBasisPoints > 0 {
		for _, na := range nodeAccounts {
			security = security.Add(na.Bond)
		}
		security = common.GetUncappedShare(cosmos.NewUint(uint64(tvlCapBasisPoints)), cosmos.NewUint(constants.MaxBasisPts), security)
	} else {
		security = getEffectiveSecurityBond(nodeAccounts)
	}

	if totalRuneValue.GT(security) {
		ctx.Logger().Debug("reached TVL cap", "total rune value", totalRuneValue.String(), "security", security.String())
		return true
	}
	return false
}

func IsModuleAccAddress(keeper keeper.Keeper, accAddr cosmos.AccAddress) bool {
	return accAddr.Equals(keeper.GetModuleAccAddress(AsgardName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(BondName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(ReserveName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(LendingName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(AffiliateCollectorName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(ModuleName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(TCYClaimingName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(TCYStakeName)) ||
		accAddr.Equals(keeper.GetModuleAccAddress(TreasuryName))
}

func NewSwapMemo(ctx cosmos.Context, mgr Manager, targetAsset common.Asset, destination common.Address, limit cosmos.Uint, affiliate string, affiliateBps cosmos.Uint) string {
	return fmt.Sprintf("=:%s:%s:%s:%s:%s", targetAsset, destination, limit.String(), affiliate, affiliateBps.String())
}

// willSwapOutputExceedLimitAndFees returns true if the swap output will exceed the
// limit (if provided) + the outbound fee on the destination chain
func willSwapOutputExceedLimitAndFees(ctx cosmos.Context, mgr Manager, msg MsgSwap) bool {
	swapper, err := GetSwapper(mgr.GetVersion())
	if err != nil {
		panic(err)
	}

	source := msg.Tx.Coins[0]
	target := common.NewCoin(msg.TargetAsset, msg.TradeTarget)

	var emit cosmos.Uint
	switch {
	case !source.IsRune() && !target.IsRune():
		var sourcePool Pool
		sourcePool, err = mgr.Keeper().GetPool(ctx, source.Asset.GetLayer1Asset())
		if err != nil {
			return false
		}
		var targetPool Pool
		targetPool, err = mgr.Keeper().GetPool(ctx, target.Asset.GetLayer1Asset())
		if err != nil {
			return false
		}
		emit = swapper.CalcAssetEmission(sourcePool.BalanceAsset, source.Amount, sourcePool.BalanceRune)
		emit = swapper.CalcAssetEmission(targetPool.BalanceRune, emit, targetPool.BalanceAsset)
	case source.IsRune():
		var pool Pool
		pool, err = mgr.Keeper().GetPool(ctx, target.Asset.GetLayer1Asset())
		if err != nil {
			return false
		}
		emit = swapper.CalcAssetEmission(pool.BalanceRune, source.Amount, pool.BalanceAsset)
	case target.IsRune():
		var pool Pool
		pool, err = mgr.Keeper().GetPool(ctx, source.Asset.GetLayer1Asset())
		if err != nil {
			return false
		}
		emit = swapper.CalcAssetEmission(pool.BalanceAsset, source.Amount, pool.BalanceRune)
	}

	// Check that the swap will emit more than the limit amount + outbound fee
	transactionFeeAsset, err := mgr.GasMgr().GetAssetOutboundFee(ctx, msg.TargetAsset, false)
	return err == nil && emit.GT(target.Amount.Add(transactionFeeAsset))
}

// getLastChurnHeight returns the block height of the last churn.
func getLastChurnHeight(ctx cosmos.Context, k keeper.Keeper) int64 {
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("failed to get asgard vaults", "error", err)
		return 0
	}
	// calculate last churn block height
	var lastChurnHeight int64 // the last block height we had a successful churn
	for _, vault := range vaults {
		if vault.StatusSince > lastChurnHeight {
			lastChurnHeight = vault.StatusSince
		}
	}
	return lastChurnHeight
}

// settleSwap handles the settlement of any type of swap (market, limit, streaming) from the advanced swap queue, including:
// - Scheduling the outbound transaction for swapped amounts
// - Refunding any remaining deposit
// - Removing the swap from advanced queue and indexes
// - Emitting appropriate events
func settleSwap(ctx cosmos.Context, mgr Manager, msg MsgSwap, settleReason string) error {
	// Skip collector swaps that have already left the queue so repeated settlement
	// cannot deliver their output or restore their unused RUNE twice.
	if (affiliateCollectorConversionName(mgr.Keeper(), msg) != "" || isPreferredAssetCollectorSwap(mgr.Keeper(), msg)) &&
		!mgr.Keeper().HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)) {
		return nil
	}
	// Settle swap mutates multiple pieces of state (scheduled outbounds, refunds,
	// queue removal, events). Run it in a cache context so partial failures do not
	// leave committed side effects that can be duplicated on retry.
	cacheCtx, commit := ctx.CacheContext()
	if err := settleSwapCached(cacheCtx, mgr, msg, settleReason); err != nil {
		return err
	}
	commit()
	return nil
}

func settleSwapCached(ctx cosmos.Context, mgr Manager, msg MsgSwap, settleReason string) error {
	// Deliver accumulated collector output and credit any converted RUNE in the
	// same cached context as queue removal, so failed settlement can be retried.
	collectorConversion := affiliateCollectorConversionName(mgr.Keeper(), msg) != ""
	collectorPayout := isPreferredAssetCollectorSwap(mgr.Keeper(), msg)
	if !msg.State.Out.IsZero() && (collectorConversion || collectorPayout) {
		if err := deliverAffiliateCollectorOutput(ctx, mgr, msg, msg.State.Out); err != nil {
			if !collectorPayout || !errors.Is(err, ErrNotEnoughToPayFee) {
				return err
			}
			// Output below the fee or dust limit cannot be paid. Return it to
			// the pool through the normal cleanup path, then restore only unused
			// input below. Keep the queue item if cleanup fails so it can retry.
			toi := TxOutItem{
				Chain:     msg.TargetAsset.GetChain(),
				InHash:    msg.Tx.ID,
				ToAddress: msg.Destination,
				Coin:      common.NewCoin(msg.TargetAsset, msg.State.Out),
			}
			unrefundableCoinCleanup(ctx, mgr, toi, "failed_outbound")
			if !mgr.Keeper().HasRefundCleanup(ctx, refundCleanupKey(toi, "failed_outbound")) {
				return fmt.Errorf("failed to clean up preferred asset payout %s", msg.Tx.ID)
			}
			refundReason := fmt.Sprintf("%s; fail to refund (%s): swap output", err, toi.Coin.String())
			refundTx := common.NewTx(msg.Tx.ID, msg.Tx.FromAddress, msg.Tx.ToAddress, common.NewCoins(toi.Coin), msg.Tx.Gas, msg.Tx.Memo)
			if emitErr := mgr.EventMgr().EmitEvent(ctx, NewEventRefund(CodeFailAddOutboundTx, refundReason, refundTx, common.Fee{})); emitErr != nil {
				return emitErr
			}
		}
	}
	stableReserveInternal, err := isStableReserveInternalSwap(ctx, mgr, msg)
	if err != nil {
		return err
	}

	// Schedule outbound for what was already swapped whenever settlement is the
	// place materializing the final outbound. The advanced swap queue skips
	// outbound creation during execution and relies on settlement to create the
	// completed swap outbound, even after multiple sub-swaps have already run.
	if !msg.State.Out.IsZero() && !stableReserveInternal && !collectorConversion && !collectorPayout {
		// Parse memo to check if this is a savers add
		memo, err := ParseMemoWithTHORNamesCached(ctx, mgr.Keeper(), msg.Tx.Memo)
		if err != nil {
			ctx.Logger().Error("fail to parse memo", "error", err)
			// Continue with outbound even if memo parsing fails
			memo = nil
		}

		// Only schedule outbound if not a savers add and not in simulation mode
		if (memo == nil || !memo.IsType(TxAdd)) && !isSimulationMode(ctx) {
			// Handle aggregator if present
			dexAgg := ""
			if len(msg.Aggregator) > 0 {
				dexAgg, err = FetchDexAggregator(
					msg.TargetAsset.GetChain(),
					msg.Aggregator,
				)
				if err != nil {
					ctx.Logger().Error("fail to fetch dex aggregator", "error", err)
					// Continue without aggregator
				}
			}

			toi := TxOutItem{
				Chain:                 msg.TargetAsset.GetChain(),
				InHash:                msg.Tx.ID,
				ToAddress:             msg.Destination,
				Coin:                  common.NewCoin(msg.TargetAsset, msg.State.Out),
				Memo:                  "",
				MaxGas:                []common.Coin{},
				GasRate:               0,
				OutHash:               "",
				ModuleName:            "",
				Aggregator:            dexAgg,
				AggregatorTargetAsset: msg.AggregatorTargetAddress,
				AggregatorTargetLimit: msg.AggregatorTargetLimit,
				CloutSpent:            &cosmos.Uint{},
			}

			if _, err := mgr.TxOutStore().TryAddTxOutItem(ctx, mgr, toi, cosmos.ZeroUint()); err != nil {
				ctx.Logger().Error("fail to schedule swap outbound", "error", err)
				unrefundableCoinCleanup(ctx, mgr, toi, "failed_outbound")

				// Emit a "fail to refund" refund event
				refundReasonFull := fmt.Sprintf("%s; fail to refund (%s): swap output", err, toi.Coin.String())
				refundTx := common.NewTx(msg.Tx.ID, msg.Tx.FromAddress, msg.Tx.ToAddress, common.NewCoins(toi.Coin), msg.Tx.Gas, msg.Tx.Memo)
				eventRefund := NewEventRefund(CodeFailAddOutboundTx, refundReasonFull, refundTx, common.Fee{})
				if err := mgr.EventMgr().EmitEvent(ctx, eventRefund); err != nil {
					ctx.Logger().Error("fail to emit refund event", "error", err)
				}
			}
		}
	}

	// Internal stable-reserve rescues targeting RUNE accumulate their proceeds in
	// Asgard during the stream (the outbound above is skipped for internal swaps,
	// and settleStableReserveInternalSwap deliberately leaves the RUNE in Asgard so
	// the solvency invariant stays satisfied mid-stream). Move the accumulated RUNE
	// into the module here, at completion — this runs for both a successful and a
	// failed final sub-swap, so proceeds from earlier sub-swaps are never stranded.
	// Deploys always target a stable (never RUNE), so a RUNE target is always a rescue.
	if stableReserveInternal && msg.TargetAsset.IsRune() && !msg.State.Out.IsZero() {
		coin := common.NewCoin(common.RuneAsset(), msg.State.Out)
		if err := mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, StableReserveName, common.NewCoins(coin)); err != nil {
			return err
		}
		if err := mgr.EventMgr().EmitEvent(ctx, newStableReserveEvent(
			"stable_reserve_rescue_rune_settled",
			"amount", msg.State.Out.String(),
			"tx_id", msg.Tx.ID.String(),
		)); err != nil {
			return err
		}
	}

	// Return unused payout RUNE to both the module and this THORName's collector.
	// A normal refund would charge an outbound fee and leave the ledger uncredited.
	if msg.State.Deposit.GT(msg.State.In) && collectorPayout {
		if err := restorePreferredAssetCollector(ctx, mgr, msg.Tx.Memo, msg.State.Deposit.Sub(msg.State.In)); err != nil {
			return err
		}
	}
	// Other swaps refund their remaining deposit through the normal refund path.
	if msg.State.Deposit.GT(msg.State.In) && !collectorPayout {
		remainder := common.SafeSub(msg.State.Deposit, msg.State.In)
		refundCoin := common.NewCoin(msg.Tx.Coins[0].Asset, remainder)
		refundCoinTx := msg.Tx
		refundCoinTx.Coins = common.NewCoins(refundCoin)

		// Try to get the observed tx for vault selection
		voter, voterErr := mgr.Keeper().GetObservedTxInVoter(ctx, msg.Tx.ID)
		if voterErr == nil && !voter.Tx.IsEmpty() {
			if refundErr := refundTx(ctx, ObservedTx{Tx: refundCoinTx, ObservedPubKey: voter.Tx.ObservedPubKey}, mgr, CodeSwapFail, settleReason, ""); refundErr != nil {
				return fmt.Errorf("fail to refund swap remainder: %w", refundErr)
			}
		} else {
			if refundErr := refundTx(ctx, ObservedTx{Tx: refundCoinTx}, mgr, CodeSwapFail, settleReason, ""); refundErr != nil {
				return fmt.Errorf("fail to refund swap remainder: %w", refundErr)
			}
		}
	}

	// Remove the item and its index entry together, reusing the message already
	// loaded by EndBlock instead of fetching and decoding it again.
	if err := mgr.Keeper().RemoveAdvSwapQueueItemMsg(ctx, msg); err != nil {
		// Log but don't fail
		ctx.Logger().Debug("fail to remove swap from adv queue", "error", err)
	}

	// Emit streaming swap event if this was a streaming swap
	if msg.IsStreaming() {
		// Create a StreamingSwap struct from the MsgSwap state for the event
		swp := StreamingSwap{
			TxID:              msg.Tx.ID,
			Interval:          msg.State.Interval,
			Quantity:          msg.State.Quantity,
			Count:             msg.State.Count,
			LastHeight:        msg.State.LastHeight,
			TradeTarget:       msg.TradeTarget,
			Deposit:           msg.State.Deposit,
			In:                msg.State.In,
			Out:               msg.State.Out,
			FailedSwaps:       msg.State.FailedSwaps,
			FailedSwapReasons: msg.State.FailedSwapReasons,
		}

		evt := NewEventStreamingSwap(msg.Tx.Coins[0].Asset, msg.TargetAsset, swp)
		if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("fail to emit streaming swap event", "error", err)
		}
	}

	// Emit limit swap close event if this was a limit swap being closed
	if msg.IsLimitSwap() {
		evt := NewEventLimitSwapClose(msg.Tx.ID, settleReason, ctx.BlockHeight())
		if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("fail to emit limit swap close event", "error", err)
		}
	}

	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// RUNEPool and POL
////////////////////////////////////////////////////////////////////////////////////////

// reserveExitRUNEPool will release as much reserve ownership of the runepool as
// possible to providers. The amount is limited by the reserve units and pending rune -
// whichever is less. The ownership units are adjusted and a corresponding amount of
// rune is transferred from the runepool module to the reserve.
func reserveExitRUNEPool(ctx cosmos.Context, mgr Manager) error {
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return fmt.Errorf("unable to get runepool: %w", err)
	}

	pendingRune := mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)

	// if the reserve owns no pol or there are no pending rune, nothing to do
	if runePool.ReserveUnits.IsZero() || pendingRune.IsZero() {
		return nil
	}

	// the reserve will exit as much as possible, limited by reserve share or pending rune
	reserveExitRune := pendingRune
	runePoolValue, err := runePoolValue(ctx, mgr)
	if err != nil {
		return fmt.Errorf("fail to get pol pool value: %w", err)
	}
	reserveRunepoolValue := common.GetSafeShare(runePool.ReserveUnits, runePool.TotalUnits(), runePoolValue)
	if reserveRunepoolValue.LT(reserveExitRune) {
		reserveExitRune = reserveRunepoolValue
	}
	reserveExitUnits := common.GetSafeShare(reserveExitRune, reserveRunepoolValue, runePool.ReserveUnits)

	// transfer the corresponding rune from runepool module to reserve
	reserveExitCoins := common.NewCoins(common.NewCoin(common.RuneNative, reserveExitRune))
	err = mgr.Keeper().SendFromModuleToModule(ctx, RUNEPoolName, ReserveName, reserveExitCoins)
	if err != nil {
		return fmt.Errorf("unable to SendFromModuleToModule: %s", err)
	}

	ctx.Logger().Info("reserve exited runepool", "units", reserveExitUnits, "rune", reserveExitRune)

	// update runepool units
	runePool.ReserveUnits = common.SafeSub(runePool.ReserveUnits, reserveExitUnits)
	mgr.Keeper().SetRUNEPool(ctx, runePool)

	return nil
}

// reserveEnterRUNEPool will acquire the provided rune value of the runepool from the
// providers to the reserve. The ownership units are adjusted and a corresponding amount
// of rune is transferred from the reserve to the runepool module. This allows the
// reserve to reclaim units to allow providers to withdraw.
func reserveEnterRUNEPool(ctx cosmos.Context, mgr Manager, rune cosmos.Uint) error {
	runePool, err := mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return fmt.Errorf("fail to get runepool: %w", err)
	}

	// determine the rune value of the units
	runePoolValue, err := runePoolValue(ctx, mgr)
	if err != nil {
		return fmt.Errorf("fail to get runepool value: %w", err)
	}
	units := common.GetSafeShare(rune, runePoolValue, runePool.TotalUnits())
	coins := common.NewCoins(common.NewCoin(common.RuneNative, rune))

	// transfer the rune from the reserve to the runepool
	err = mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, RUNEPoolName, coins)
	if err != nil {
		return fmt.Errorf("fail to transfer rune from reserve to runepool: %w", err)
	}

	ctx.Logger().Info("reserve entered runepool", "units", units, "rune", rune)

	// update runepool units
	runePool.ReserveUnits = runePool.ReserveUnits.Add(units)
	mgr.Keeper().SetRUNEPool(ctx, runePool)

	return nil
}

// runePoolValue is the POL value in RUNE plus undeployed RUNE in the runepool module.
func runePoolValue(ctx cosmos.Context, mgr Manager) (cosmos.Uint, error) {
	polValue, err := polPoolValue(ctx, mgr)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	// get the total rune in the runepool module
	pending := mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)

	return polValue.Add(pending), nil
}

// polValueMemo caches polPoolValue across a run of POL operations. Callers
// reset it after anything that can change pools, synth supply or the POL
// liquidity provider. A nil memo computes every time.
type polValueMemo struct {
	value cosmos.Uint
	ok    bool
}

func (m *polValueMemo) reset() {
	if m != nil {
		m.ok = false
	}
}

// runePoolValue is runePoolValue with the POL part taken from the memo.
func (m *polValueMemo) runePoolValue(ctx cosmos.Context, mgr Manager) (cosmos.Uint, error) {
	if m == nil {
		return runePoolValue(ctx, mgr)
	}
	if !m.ok {
		value, err := polPoolValue(ctx, mgr)
		if err != nil {
			return cosmos.ZeroUint(), err
		}
		m.value, m.ok = value, true
	}
	pending := mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)
	return m.value.Add(pending), nil
}

// polPoolValue - calculates how much the POL is worth in rune
func polPoolValue(ctx cosmos.Context, mgr Manager) (cosmos.Uint, error) {
	total := cosmos.ZeroUint()

	polAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	if err != nil {
		return total, err
	}

	pools, err := mgr.Keeper().GetPools(ctx)
	if err != nil {
		return total, err
	}
	for _, pool := range pools {
		if pool.Asset.IsNative() {
			continue
		}
		if pool.BalanceRune.IsZero() {
			continue
		}
		synthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		pool.CalcUnits(synthSupply)
		lp, err := mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, polAddress)
		if err != nil {
			return total, err
		}
		share := common.GetSafeShare(lp.Units, pool.GetPoolUnits(), pool.BalanceRune)
		total = total.Add(share.MulUint64(2))
	}

	return total, nil
}

// This removes the first prefix ending with "//" (if there is one) from a KVStore key,
// such as when obtained through an Iterator, whatever the prefix may be.
// The "/" at the end of every prefix, together with the "/" added by KVStore GetKey
// (to ensure that no prefix ever contains another prefix)
// should ensure that each prefix ends with "//".
func trimKeyPrefix(key []byte) string {
	keyString := string(key)
	if _, after, found := strings.Cut(keyString, "//"); found {
		return after
	}
	return keyString
}

func IsPeriodLastBlock(ctx cosmos.Context, blocksPerPeriod int64) bool {
	return ctx.BlockHeight()%blocksPerPeriod == 0
}

func isTronZeroGasTx(tx ObservedTx) bool {
	if !tx.Tx.Chain.Equals(common.TRONChain) {
		return false
	}

	gasAmount := tx.Tx.Gas.ToCoins().GetCoin(common.TRXAsset).Amount
	return gasAmount.Equal(cosmos.NewUint(1))
}

// leadingZeros pads a string with leading zeros to reach the specified length.
// If str is already longer than length, it returns the first 'length' characters.
func leadingZeros(length int, str string) string {
	switch {
	case len(str) < length:
		var b strings.Builder
		for i := 1; i <= length-len(str); i++ {
			b.WriteString("0")
		}
		b.WriteString(str)
		return b.String()
	case len(str) > length:
		return str[:length]
	}
	return str
}

// isOutboundFakeGasTx returns true if the observed outbound is a "fake gas" transaction
// for a failed outbound. When an outbound fails (e.g., out of gas), bifrost
// observes the failed transaction and reports it as an outbound observation with
// amount=1 wei and memo="OUT:failed_txhash" so the gas can be accounted for. This
// should not trigger slashing as it's a legitimate observation of a failed transaction.
// Checks are:
// - must only have one coin in outbound
// - coin asset must be the gas asset
// - coin amount must be 1
// - memo must start with "OUT:" (bifrost sets memo to OUT:txhash for failed tx observations)
func isOutboundFakeGasTx(tx ObservedTx) bool {
	isLenCoins1 := len(tx.Tx.Coins) == 1
	if !isLenCoins1 {
		return false
	}
	asset := tx.Tx.Coins[0].Asset
	gasAsset := asset.Chain.GetGasAsset()
	isAssetGasAsset := asset.Equals(gasAsset)
	if !isAssetGasAsset {
		return false
	}

	if !tx.Tx.Coins[0].Amount.Equal(sdkmath.NewUint(1)) {
		return false
	}

	// fake gas txs have a self-referential out memo
	return tx.Tx.Memo == "OUT:"+tx.Tx.ID.String()
}

// activeSolVaultNonce returns a vault's committed durable nonce unless the
// break-glass mimir SOLNonceClear-<vault pubkey> voids it (a record committed at or
// before the mimir's height is ignored; a later commit re-anchors the vault). All
// consensus readers of the committed nonce go through this.
func activeSolVaultNonce(ctx cosmos.Context, k keeper.Keeper, vaultPubKey common.PubKey) (SolVaultNonce, bool, error) {
	record, found, err := k.GetSolVaultNonce(ctx, vaultPubKey)
	if err != nil || !found {
		return record, found, err
	}
	clearHeight, err := k.GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateSOLNonceClear, vaultPubKey.String()))
	if err != nil {
		return SolVaultNonce{}, false, err
	}
	if clearHeight > 0 && record.UpdatedHeight <= clearHeight {
		return SolVaultNonce{}, false, nil
	}
	return record, true, nil
}

// solNonceCreateMaxRent caps the rent (1e8 units) booked as gas for a durable-nonce
// account creation: 0.01 SOL, ~7x the current rent-exempt minimum.
const solNonceCreateMaxRent = 1_000_000

// isSolNonceCreateTx reports whether an observed vault outflow is a Solana vault's
// durable-nonce account CREATION: no memo and a single SOL coin (the rent, capped)
// sent to the sender's own seed-derived nonce account. Classified on these consensus
// fields alone — not on the attested SolNonce, which EqualsConsensus ignores, so one
// vote (whichever completed quorum) must not decide the branch. The shape alone can
// also match a scheduled memoless payment to that address, so the caller also
// requires agreed creation evidence (solNonceCommittedBy).
func isSolNonceCreateTx(tx ObservedTx) bool {
	if !tx.Tx.Chain.Equals(common.SOLChain) || strings.TrimSpace(tx.Tx.Memo) != "" {
		return false
	}
	if len(tx.Tx.Coins) != 1 || !tx.Tx.Coins[0].Asset.Equals(common.SOLAsset) {
		return false
	}
	// Rent for an 80-byte nonce account is ~0.0015 SOL; anything far above it is not a
	// nonce creation and must not be written off as gas.
	if tx.Tx.Coins[0].Amount.GT(cosmos.NewUint(solNonceCreateMaxRent)) {
		return false
	}
	expected, err := common.SolNonceAccountAddress(tx.Tx.FromAddress.String())
	return err == nil && tx.Tx.ToAddress.String() == expected
}

// isCancelOrApprovalTx returns true if the observed outbound is a cancel transaction
// sent by bifrost to unstuck a pending transaction or an approval transaction for
// router V6.
//
// Cancel transactions occurs on EVM chains where bifrost needs to replace a stuck
// transaction by sending a new transaction with the same nonce but higher gas price. To
// "cancel" the original transaction, bifrost sends a zero-value transaction to the
// vault's own address. Note: Cancel transactions have amount=0 on the EVM chain, but
// bifrost converts them to DustThreshold when observing to make them observable.
//
// Approval transactions are sent by bifrost to approve token allowances for the router
// contract on behalf of the vault with the ERC20 balance.
func isCancelOrApprovalTx(tx ObservedTx) bool {
	// Must have exactly one coin
	if len(tx.Tx.Coins) != 1 {
		return false
	}

	asset := tx.Tx.Coins[0].Asset

	// Must be an EVM chain
	if !asset.Chain.IsEVM() {
		return false
	}

	// Must be the gas asset
	gasAsset := asset.Chain.GetGasAsset()
	if !asset.Equals(gasAsset) {
		return false
	}

	// Must have amount = DustThreshold (cancel transactions have 0 value on chain,
	// but bifrost scanner converts them to DustThreshold to make them observable)
	dustThreshold := asset.Chain.DustThreshold()
	if !tx.Tx.Coins[0].Amount.Equal(dustThreshold) {
		return false
	}

	// Must have no memo
	if tx.Tx.Memo != "" {
		return false
	}

	return true
}

// applyMemolessOutboundLogic applies the memoless outbound logic to a TxOutItem
// It modifies the memo based on enableMemolessOutbound setting and memo type
// The function is safe to call multiple times on the same TxOutItem
func applyMemolessOutboundLogic(version semver.Version, toi *TxOutItem, enableMemolessOutbound int64) {
	// Store original memo if not already set (prevents overwriting on multiple calls)
	if toi.OriginalMemo == "" {
		toi.OriginalMemo = toi.Memo
	}

	// If feature is disabled, restore memo from original_memo
	if enableMemolessOutbound <= 0 {
		if toi.OriginalMemo != "" {
			toi.Memo = toi.OriginalMemo
		}
		return
	}

	// Feature is enabled - apply memoless logic
	memo, err := ParseMemo(version, toi.OriginalMemo)
	if err != nil {
		// If memo parsing fails, preserve the original memo
		toi.Memo = toi.OriginalMemo
		return
	}

	switch {
	case toi.Aggregator != "", strings.Contains(toi.OriginalMemo, "|"):
		// Utilizes dex agg or data passthrough, preserve memo
		toi.Memo = toi.OriginalMemo
	case memo.GetType().IsOutboundMemoless():
		// Outbound memo type supports memoless, clear the memo
		toi.Memo = ""
	default:
		// For all other memo types, preserve the original memo
		toi.Memo = toi.OriginalMemo
	}
}
