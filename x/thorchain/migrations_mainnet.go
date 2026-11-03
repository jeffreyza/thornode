//go:build mainnet
// +build mainnet

package thorchain

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	v2 "gitlab.com/thorchain/thornode/v3/x/thorchain/migrations/v2"
)

// Migrator is a struct for handling in-place store migrations.
type Migrator struct {
	mgr *Mgrs
}

// NewMigrator returns a new Migrator.
func NewMigrator(mgr *Mgrs) Migrator {
	return Migrator{mgr: mgr}
}

// Migrate1to2 migrates from version 1 to 2.
func (m Migrator) Migrate1to2(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	return v2.MigrateStore(ctx, m.mgr.storeService)
}

// Migrate2to3 migrates from version 2 to 3.
func (m Migrator) Migrate2to3(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// refund stagenet funding wallet for user refund
	// original user tx: https://runescan.io/tx/A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52
	// refund to user from stagenet funding wallet: https://bscscan.com/tx/0xba67f3a88f8c998f29e774ffa8328e5625521e37c2db282b29a04ab3d2593f48
	stagenetWallet := "0x3021C479f7F8C9f1D5c7d8523BA5e22C0Bcb5430"
	inTxId := "A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52" // original user tx

	bscUsdt, err := common.NewAsset("BSC.USDT-0X55D398326F99059FF775485246999027B3197955")
	if err != nil {
		return err
	}
	usdtCoin := common.NewCoin(bscUsdt, cosmos.NewUint(4860737515919))
	blockHeight := ctx.BlockHeight()

	// schedule refund
	if err := unsafeAddRefundOutbound(ctx, m.mgr, inTxId, stagenetWallet, usdtCoin, blockHeight); err != nil {
		return err
	}

	return nil
}

// Migrate3to4 migrates from version 4 to 5.
func (m Migrator) Migrate4to5(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ TCY ------------------------------

	totalTCYCoin := common.NewCoin(common.TCY, cosmos.NewUint(210_000_000_00000000))
	err := m.mgr.Keeper().MintToModule(ctx, ModuleName, totalTCYCoin)
	if err != nil {
		return err
	}

	// Claims 206_606_541_28874864
	claimingModuleCoin := common.NewCoin(common.TCY, cosmos.NewUint(206_606_541_28874864))
	err = m.mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(claimingModuleCoin))
	if err != nil {
		return err
	}

	// 210M minus claims: 206_606_541_28874864
	treasuryCoin := common.NewCoin(common.TCY, totalTCYCoin.Amount.Sub(claimingModuleCoin.Amount))
	treasuryAddress, err := common.NewAddress("thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k")
	if err != nil {
		return err
	}

	treasuryAccAddress, err := treasuryAddress.AccAddress()
	if err != nil {
		return err
	}

	err = m.mgr.Keeper().SendFromModuleToAccount(ctx, ModuleName, treasuryAccAddress, common.NewCoins(treasuryCoin))
	if err != nil {
		return err
	}

	err = setTCYClaims(ctx, m.mgr)
	if err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	for _, slashRefund := range mainnetSlashRefunds4to5 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration", "error", err)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient", "error", err, "recipient", recipient, "amount", amount)
		}
	}

	// ------------------------------ Mimir Cleanup ------------------------------

	return m.ClearObsoleteMimirs(ctx)
}

// Migrate5to6 migrates from version 5 to 6.
func (m Migrator) Migrate5to6(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	// Validate Reserve module has sufficient funds before starting refunds
	totalRefundAmount := cosmos.NewUint(14856919212689) // Total amount to be refunded
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, slashRefund := range mainnetSlashRefunds5to6 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", slashRefund.address)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", slashRefund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	return nil
}

// Migrate6to7 migrates from version 6 to 7.
func (m Migrator) Migrate6to7(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// handle manual outbounds
	outbounds, migrationErr := mainnetManualOutbounds6to7(ctx, m.mgr)
	if migrationErr != nil {
		ctx.Logger().Error("failed to create manual outbounds for migration", "error", migrationErr)
	}
	for _, out := range outbounds {
		// schedule outbound for 30 minutes after upgrade height
		outboundHeight := ctx.BlockHeight() + 300
		err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, out, outboundHeight)
		if err != nil {
			ctx.Logger().Error("failed to add manual outbound", "error", err, "outbound", out)
		} else {
			ctx.Logger().Info("successfully added manual outbound", "outbound", out)
		}
	}

	// handle manual observations of dropped inbounds
	inbounds, migrationErr := mainnetManualInbounds6to7()
	if migrationErr != nil {
		ctx.Logger().Error("failed to create manual inbounds for migration", "error", migrationErr)
	}
	for _, in := range inbounds {
		migrationErr = makeFakeTxInObservation(ctx, m.mgr, ObservedTxs{in})
		if migrationErr != nil {
			ctx.Logger().Error("failed to create fake inbound observation", "error", migrationErr, "inbound", in)
		} else {
			ctx.Logger().Info("successfully created fake inbound observation", "inbound", in)
		}
	}

	return nil
}

// makeFakeTxInObservation accepts an array of unobserved inbounds, queries for active
// node accounts, and makes a fake observation for each validator and unobserved TxIn.
// Returns an error if there are no active validators or if every per-validator handler
// invocation failed, so that callers can distinguish total failure from success.
func makeFakeTxInObservation(ctx cosmos.Context, mgr Manager, txs ObservedTxs) error {
	activeNodes, err := mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("Failed to get active nodes", "err", err)
		return err
	}
	if len(activeNodes) == 0 {
		return fmt.Errorf("no active validators to record fake observation")
	}

	handler := NewObservedTxInHandler(mgr)

	successCount := 0
	for _, na := range activeNodes {
		txInMsg := NewMsgObservedTxIn(txs, na.NodeAddress)
		_, err := handler.handle(ctx, *txInMsg)
		if err != nil {
			ctx.Logger().Error("failed ObservedTxIn handler", "error", err)
			continue
		}
		successCount++
	}

	if successCount == 0 {
		return fmt.Errorf("all %d ObservedTxIn handler invocations failed", len(activeNodes))
	}

	return nil
}

// Migrate8to9 migrates from version 8 to 9.
func (m Migrator) Migrate8to9(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	err := m.CommonMigrate8to9(ctx)
	if err != nil {
		return err
	}

	// handle manual outbounds
	outbounds, err := mainnetManualOutbounds8to9(ctx, m.mgr)
	if err != nil {
		ctx.Logger().Error("failed to create manual outbounds for migration", "error", err)
	}
	for _, out := range outbounds {
		outboundHeight := ctx.BlockHeight()
		err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, out, outboundHeight)
		if err != nil {
			ctx.Logger().Error("failed to add manual outbound", "error", err, "outbound", out)
		} else {
			ctx.Logger().Info("successfully added manual outbound", "outbound", out)
		}
	}

	return nil
}

// Migrate9to10 migrates from version 9 to 10.
func (m Migrator) Migrate9to10(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// move excess thor.nami to treasury
	coins := common.Coins{{
		Asset:  common.NAMI,
		Amount: cosmos.NewUint(524245),
	}}
	err := m.mgr.Keeper().SendFromModuleToModule(
		ctx, AsgardName, TreasuryName, coins,
	)
	if err != nil {
		ctx.Logger().Error("failed to move excess nami to treasury", "error", err)
	} else {
		ctx.Logger().Info("successfully moved excess nami to treasury", "coins", coins)
	}

	// re-attempt recoveries
	outbounds, err := mainnetManualOutbounds9to10(ctx, m.mgr)
	if err != nil {
		ctx.Logger().Error("failed to create manual outbounds for migration", "error", err)
	}
	for _, out := range outbounds {
		outboundHeight := ctx.BlockHeight()
		err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, out, outboundHeight)
		if err != nil {
			ctx.Logger().Error("failed to add manual outbound", "error", err, "outbound", out)
		} else {
			ctx.Logger().Info("successfully added manual outbound", "outbound", out)
		}
	}

	return nil
}

// Migrate10to11 migrates from version 10 to 11.
func (m Migrator) Migrate10to11(ctx sdk.Context) error {
	return nil
}

// Migrate11to12 migrates from version 11 to 12.
func (m Migrator) Migrate11to12(ctx sdk.Context) error {
	// loads the manager for this migration
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// avax.usdc asset
	avaxUSDC, migrationErr := common.NewAsset("AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E")
	if migrationErr != nil {
		return fmt.Errorf("failed to parse AVAX.USDC asset: %w", migrationErr)
	}

	// records to be removed
	removeLPs := []LiquidityProvider{
		// https://gateway.liquify.com/chain/thorchain_api/cosmos/tx/v1beta1/txs/5BF8911A9DD947EC1EE7E990085ECB21DD7E395F152B5137F7CD73B76E46870A
		{
			RuneAddress:  "thor1pe0pspu4ep85gxr5h9l6k49g024vemtr80hg4c",
			AssetAddress: "0x03c42ab083bd46202ee430AfC4D3dd8eD8c76c07",
		},
		// https://gateway.liquify.com/chain/thorchain_api/cosmos/tx/v1beta1/txs/248CE4B07E6A63E8CA3E111754849DD560FA9F4BF83BF2FD0B0B40947633B1B9
		{
			RuneAddress:  "",
			AssetAddress: "0x467a0ec2d2f23f0bb165eb7e44a9b16b12f5a7b4",
		},
		// affiliate position for the prior add liquidity transaction
		{
			RuneAddress:  "thor122h9hlrugzdny9ct95z6g7afvpzu34s73uklju",
			AssetAddress: "",
		},
	}

	// get the pool
	pool, migrationErr := m.mgr.Keeper().GetPool(ctx, avaxUSDC)
	if migrationErr != nil {
		return fmt.Errorf("failed to get AVAX.USDC pool: %w", migrationErr)
	}

	for _, lp := range removeLPs {
		// fetch the record
		lp, migrationErr = m.mgr.Keeper().GetLiquidityProvider(ctx, avaxUSDC, lp.GetAddress())
		if migrationErr != nil {
			ctx.Logger().Error("failed to get liquidity provider ",
				"error", migrationErr,
				"rune_address", lp.RuneAddress,
				"asset_address", lp.AssetAddress)
			continue
		}

		// remove the units from the pool
		pool.LPUnits = common.SafeSub(pool.LPUnits, lp.Units)
		ctx.Logger().Info("adjusted pool LP units",
			"asset", avaxUSDC.String(),
			"removed_units", lp.Units.String(),
			"new_pool_lp_units", pool.LPUnits.String())

		// remove the record
		m.mgr.Keeper().RemoveLiquidityProvider(ctx, lp)
		ctx.Logger().Info(
			"removed bad liquidity provider record",
			"asset", avaxUSDC.String(),
			"rune_address", lp.RuneAddress,
			"asset_address", lp.AssetAddress,
		)
	}

	// save the updated pool
	if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return fmt.Errorf("failed to save updated pool: %w", err)
	}

	// remove obsolete/typo mimir keys
	obsoleteMimirs := []string{
		"HALTRADING",
		"ADR012",
		"ADR020",
		"ADR18",
		"DEPRECATEILP",
		"FULLIMPLOSSPROTECTIONBLOCKS",
		"ILPCUTOFF",
		"KILLSWITCHSTART",
		"PROPOSAL6",
		"VOTEDOFM",
		"VOTELENDING",
		"VOTEMAXSYNTHSFORSAVERSYIELD",
		"ENABLEAVAXCHAIN",
		"ENABLEBSC",
		"ENABLESAVINGSVAULTS",
		"ADD-CHAIN-BASE",
		"ADD-CHAIN-XRP",
	}

	for _, key := range obsoleteMimirs {
		if err := m.mgr.Keeper().DeleteMimir(ctx, key); err != nil {
			ctx.Logger().Error("failed to delete obsolete mimir", "key", key, "error", err)
			continue
		}
		ctx.Logger().Info("deleted obsolete mimir", "key", key)
	}

	// refund 43F310A416A4ED8CF8B645B1EBBB5E25FB89F9777A4350F7023DEB62B90EA3AD
	refundRune := common.NewCoin(common.RuneNative, cosmos.NewUint(40000000000))
	userAddr, migrationErr := common.NewAddress("thor1dvvr4kdeurs8fdwgrql6je7l2v9ma73dp50n7m")
	if migrationErr != nil {
		return migrationErr
	}
	userAccAddr, migrationErr := userAddr.AccAddress()
	if migrationErr != nil {
		return migrationErr
	}
	migrationErr = m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, userAccAddr, common.NewCoins(refundRune))
	if migrationErr != nil {
		return migrationErr
	}

	return nil
}

// Migrate12to13 migrates from version 12 to 13.
func (m Migrator) Migrate12to13(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	// Validate Reserve module has sufficient funds before starting refunds
	totalRefundAmount := cosmos.NewUint(mainnetSlashRefunds12to13Total) // Total amount to be refunded
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, slashRefund := range mainnetSlashRefunds12to13 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", slashRefund.address)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", slashRefund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	return nil
}

// Migrate13to14 migrates from version 13 to 14.
func (m Migrator) Migrate13to14(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ADR-023: Burn ~87% of Reserve and reduce MaxRuneSupply to 360M.
	return m.BurnReserveAndReduceMaxSupply(ctx)
}

// Migrate14to15 migrates from version 14 to 15.
//
// Recovers state from the DOGE asgard migration double-broadcast at block
// 26,001,464 (slash tx 1DFE09ABE8D8DD11BA55BD6B499AC1EF91FA0B5CA83D492AB12C08AC89C2B54D)
// where the retiring vault transmitted the same migration outbound twice,
// draining 122,221,658,117,429 sats and triggering a 200% bond slash on the
// 19 vault members. The slash was later absorbed by each member's bond
// providers proportionally via bp.Adjust() during subsequent reward
// distributions.
//
// Four corrections, in order:
//
//  1. Reverse the pool slash event: pool.BalanceAsset += 122,221,658,117,429,
//     pool.BalanceRune -= 26,008,730,208,467. A strict-subtract guards against
//     pool drift between the slash block and the migration block — if the
//     pool's RUNE depth dropped below the credited amount (e.g. via LP
//     activity), the migration aborts cleanly rather than silently clamping.
//  2. Move 26,008,730,208,467 RUNE from Asgard back to Reserve, undoing the
//     bond->asgard transfer that backed the (now-reversed) pool credit.
//  3. Refund the slash directly from Reserve to each provider's wallet, using
//     the pre-computed bond deltas in mainnetSlashRefunds14to15. Per-row sends
//     log-and-continue on failure (matches Migrate4to5 / 5to6 / 12to13). na.Bond
//     is intentionally NOT touched: some slashed nodes will have churned out
//     and unbonded by the time this runs, and others will have a different
//     provider distribution than at slash time. Crediting na.Bond would also
//     trigger a NodeOperatorFee skim on the next bp.Adjust() since providers
//     have already absorbed the slash.
//  4. Reconcile phantom credit on two retiring asgard ETH vaults caused by
//     SafeSub clamping in vault.SubFundsWithClamping during outbound observation when the
//     vault was already drained. See fixVaultClamps_v3_18_0 and
//     mainnetVaultClampFixes14to15 for details. Pre-flight check skips any
//     entry that would clamp again (loud error logged) — better to leave the
//     phantom credit in place than halt the upgrade.
//
// Net coin movement: Asgard -26B, Reserve 0 (in +26B from Asgard, out -52B to
// providers, was +26B from original slash), providers' wallets +52B total.
// ETH vault Coins reduced by 270.427671 ETH across two retiring vaults (no
// module/account movement — purely a vault.Coins ledger correction).
func (m Migrator) Migrate14to15(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	const (
		stolenDogeSats   = uint64(122_221_658_117_429)
		runeCreditedSats = uint64(26_008_730_208_467)
		asgardToReserve  = uint64(26_008_730_208_467)
	)

	// Step 1: reverse pool slash event.
	pool, err := m.mgr.Keeper().GetPool(ctx, common.DOGEAsset)
	if err != nil {
		return fmt.Errorf("fail to get DOGE pool: %w", err)
	}
	if pool.IsEmpty() {
		return fmt.Errorf("DOGE pool is empty, cannot reverse slash")
	}
	runeCredit := cosmos.NewUint(runeCreditedSats)
	if pool.BalanceRune.LT(runeCredit) {
		return fmt.Errorf(
			"DOGE pool BalanceRune (%s) below expected slash credit (%d); "+
				"pool drifted between slash and migration — investigate before retrying",
			pool.BalanceRune.String(), runeCreditedSats,
		)
	}
	pool.BalanceAsset = pool.BalanceAsset.Add(cosmos.NewUint(stolenDogeSats))
	pool.BalanceRune = pool.BalanceRune.Sub(runeCredit)
	if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return fmt.Errorf("fail to save DOGE pool: %w", err)
	}
	poolSlashAmt := []PoolAmt{
		{Asset: common.DOGEAsset, Amount: int64(stolenDogeSats)},
		{Asset: common.RuneAsset(), Amount: -int64(runeCreditedSats)},
	}
	if err := m.mgr.EventMgr().EmitEvent(ctx, NewEventSlash(common.DOGEAsset, poolSlashAmt)); err != nil {
		ctx.Logger().Error("fail to emit pool reversal event", "error", err)
	}

	// Step 2: Asgard -> Reserve.
	asgardCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(asgardToReserve))
	if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(asgardCoin)); err != nil {
		return fmt.Errorf("fail to move asgard->reserve: %w", err)
	}

	// Step 3: Reserve -> per-provider direct refund.
	// Pre-flight Reserve balance check (matches Migrate5to6 / 12to13).
	totalRefundAmount := cosmos.NewUint(mainnetSlashRefunds14to15Total)
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, refund := range mainnetSlashRefunds14to15 {
		recipient, err := cosmos.AccAddressFromBech32(refund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", refund.address)
			continue
		}
		amount := cosmos.NewUint(refund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", refund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	// Step 4: reconcile phantom credit on retiring asgard ETH vaults from
	// SafeSub clamping in vault.SubFundsWithClamping during outbound observation.
	// Failures inside this routine are logged and skipped per-row; we do
	// not return an error from this step so the migration as a whole still
	// completes the prior 3 steps.
	fixVaultClamps_v3_18_0(ctx, m.mgr, mainnetVaultClampFixes14to15)

	return nil
}

// adr028PoolHaircut describes the pool-level depth adjustments applied by the
// ADR-028 conciliation waterfall (steps 1–4).  All amounts are in 1e8 units.
// Fields are populated from on-chain state snapshotted at upgrade height;
// zero values mean the step does not apply to that pool.
type adr028PoolHaircut struct {
	asset common.Asset
	// step1RuneHaircut: ΔR — reduce pool.BalanceRune by this amount and burn it
	// from Asgard. pool.BalanceAsset is reduced proportionally:
	// ΔA = ΔR × BalanceAsset / BalanceRune (symmetric AMM identity).
	// LP units at the reserve address are NOT mutated; polPoolValue() reads
	// pool.BalanceRune live so the depth reduction propagates automatically.
	// After all step-1 pool writes, RUNEPool.ReserveUnits is re-solved to keep
	// providers whole (see Migrate15to16 for the formula).
	step1RuneHaircut uint64
	// step2ReserveSynthBurn: synth amount (in the pool's synth denom) to burn
	// directly from the reserve module bank. No pool balance change — the freed
	// backing shifts from synth-claim to LP-claim on the next CalcUnits recompute.
	step2ReserveSynthBurn uint64
	// step3SaversHaircutBps: savers haircut in basis points applied to the synth
	// vault pool (asset = synthAsset). Reduces every saver LP record pro-rata,
	// reduces the synth pool LPUnits and BalanceAsset, and burns the corresponding
	// synth supply from Asgard.
	step3SaversHaircutBps uint64
	// step4TreasuryDraw: ΔA — asset amount to draw from the treasury LP position.
	// Reduces treasury LP units, pool.LPUnits, pool.BalanceAsset by ΔA, and
	// pool.BalanceRune / Asgard RUNE by ΔR = ΔA × BalanceRune / BalanceAsset.
	// Non-zero only for zero-saver assets where treasury holds LP coverage.
	step4TreasuryDraw uint64
}

// adr028SolveOverflowBps computes the strict-waterfall savers-overflow haircut
// (in bps on the savers-pool BalanceAsset) needed to retire `residualUnits` of
// total-pool units that the reserve-capped layer-1 POL removal could not absorb.
//
// It mirrors the dry-run computeCalibration regime split:
//
//	overhang (2·postA ≤ S): CalcUnits clamps, SU = postL·S is LINEAR in supply, so
//	  burning supply by burn_perc = ΔR/preR retires exactly burn_perc of the units
//	  → overflow_bps = ΔR·10000/preR (this is the BTC=1907 known-good shortcut).
//	non-overhang (2·postA > S): SU = postL·S/(2·postA−S). Solve the post-removal
//	  synth units target (SU_now − residualUnits), invert CalcUnits at (postL, postA)
//	  for the supply S_target = 2·postA·SU_target/(postL+SU_target), and burn
//	  b = S − S_target → overflow_bps = b·10000/saverPool.BalanceAsset.
//
// postL = L − x (LP units after layer-1 removal); postA = A after the ΔA depth cut.
// Returns 0 if there is no savers pool (TRON.TRX, XRP.XRP) — the residual then
// unavoidably falls on DLPs; the dry-run flags it (G9).
func (m Migrator) adr028SolveOverflowBps(
	ctx sdk.Context,
	asset common.Asset,
	postL, postA, synthSupply, targetT cosmos.Uint,
) uint64 {
	synthAsset := asset.GetSyntheticAsset()
	saverPool, err := m.mgr.Keeper().GetPool(ctx, synthAsset)
	if err != nil || saverPool.BalanceAsset.IsZero() {
		// No savers pool / zero depth → no overflow lever (TRX/XRP).
		return 0
	}
	// Pure arithmetic extracted to adr028OverflowBps (migration_math_15to16.go) so
	// its regime switch and clamp are CI-unit-testable; see that file for the
	// CalcUnits-inverse derivation.
	return adr028OverflowBps(postL, postA, synthSupply, targetT, saverPool.BalanceAsset)
}

// splitFreedRune disposes of RUNE freed by a pool-depth reduction. Instead of
// burning the whole amount, it burns HALF and retains the other HALF in the
// Reserve module. Either way the full `freed` amount leaves Asgard (so pool/vault
// RUNE accounting is consistent); only its destination differs — half is destroyed
// (Asgard→thorchain→burn, thorchain being the only Burner-permissioned module) and
// half is credited to the Reserve (Asgard→Reserve). The odd 1e-8 unit, if any, goes
// to the burn. Emits a mint/burn event for the burned half.
func (m Migrator) splitFreedRune(ctx sdk.Context, freed cosmos.Uint, reason string) error {
	if freed.IsZero() {
		return nil
	}
	toBurn, toReserve := adr028SplitFreed(freed) // pure split; odd 1e-8 unit → burn
	if !toBurn.IsZero() {
		bc := common.NewCoin(common.RuneNative, toBurn)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(bc)); err != nil {
			return fmt.Errorf("move burn-half Asgard→thorchain: %w", err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, bc); err != nil {
			return fmt.Errorf("burn freed RUNE: %w", err)
		}
		burnEvt := NewEventMintBurn(BurnSupplyType, bc.Asset.Native(), bc.Amount, reason)
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: freed-rune: emit burn event", "error", err, "reason", reason)
		}
	}
	if !toReserve.IsZero() {
		rc := common.NewCoin(common.RuneNative, toReserve)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(rc)); err != nil {
			return fmt.Errorf("move reserve-half Asgard→Reserve: %w", err)
		}
	}
	ctx.Logger().Info("conciliation: freed RUNE split (burn half, reserve half)",
		"reason", reason, "freed", freed, "burned", toBurn, "to_reserve", toReserve)
	return nil
}

// vault2v28PubKey, adr028TreasuryAddr, stuckStreamingTxStr and the hex-qualified
// adr028Asset* constants are defined in migration_data_15to16.go (build-tag-agnostic
// so the mocknet data-validation test can reach them).

// Migrate15to16 implements ADR-028 conciliation.
// Waterfall order (ADR-028 §3, re-run amounts at upgrade height):
//
//	— zero vault 2v28 keeper balances (log only, no event).
//	— POL haircut: reduce pool.BalanceRune/Asset, burn RUNE from Asgard.
//	— reserve synth burn: burn reserve module's synth bank balances.
//	— savers haircut: reduce saver LP units + synth pool depth pro-rata,
//	  burn synths from Asgard.
//	— treasury LP draw: cover residual for zero-saver pools with treasury LP.
//	— POL provider-whole solve: after all of the above pool writes, solve
//	  RUNEPool.ReserveUnits so the reserve bucket absorbs the full POL loss.
//	— stuck streaming swap: stop the 2v28 ETH.DAI→ETH.USDT streaming swap, pay
//	  the already-produced ETH.USDT (StreamingSwap.Out) as a best-effort outbound,
//	  and log the remainder of the guaranteed minimum the treasury refunds
//	  off-chain (no on-chain fund movement for it).
//
// Snapshot date: 2026-05-26 (re-run before release at upgrade height).
// POL drain = ΔR (RUNE to burn from Asgard; ΔA = ΔR×BalanceAsset/BalanceRune).
// Only the reserve-bucket fraction (55.76%) of LP value is consumed;
// providers are held whole via RUNEPool.ReserveUnits solve after all pool writes.
func (m Migrator) Migrate15to16(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ── Step 0: zero vault 2v28 keeper record ───────────────────────────────
	vault, migrationErr := m.mgr.Keeper().GetVault(ctx, vault2v28PubKey)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: vault 2v28: fail to get vault 2v28: %w", migrationErr)
	}
	originalCoins := vault.Coins
	vault.Coins = common.Coins{}
	if err := m.mgr.Keeper().SetVault(ctx, vault); err != nil {
		return fmt.Errorf("conciliation: vault 2v28: fail to zero vault 2v28: %w", err)
	}
	ctx.Logger().Info("conciliation: vault 2v28: zeroed vault 2v28 keeper record",
		"pubkey", vault2v28PubKey,
		"cleared_coins", originalCoins.String())

	// ── Build per-pool haircut table (assets requiring NewAsset parsing) ─────
	ethUSDT, migrationErr := common.NewAsset(adr028AssetETHUSDT)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse ETH.USDT: %w", migrationErr)
	}
	avaxUSDC, migrationErr := common.NewAsset(adr028AssetAVAXUSDC)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse AVAX.USDC: %w", migrationErr)
	}
	baseUSDC, migrationErr := common.NewAsset(adr028AssetBASEUSDC)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse BASE.USDC: %w", migrationErr)
	}
	bscBUSD, migrationErr := common.NewAsset(adr028AssetBSCBUSD)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse BSC.BUSD: %w", migrationErr)
	}
	bscETH, migrationErr := common.NewAsset(adr028AssetBSCETH)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse BSC.ETH: %w", migrationErr)
	}
	bscBTCB, migrationErr := common.NewAsset(adr028AssetBSCBTCB)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse BSC.BTCB: %w", migrationErr)
	}
	ethUSDC, migrationErr := common.NewAsset(adr028AssetETHUSDC)
	if migrationErr != nil {
		return fmt.Errorf("adr028: failed to parse ETH.USDC: %w", migrationErr)
	}

	// Per-pool waterfall table — 2026-05-26 live-chain snapshot; re-run at upgrade height.
	// Step1 = ΔR (raw 1e8 RUNE to burn from Asgard); ΔA derived at runtime.
	// Step2 = synth units in 1e8 to burn from reserve bank (no pool mutation).
	// Step3 = savers haircut bps applied to the synth vault pool.
	// Step4 = ΔA (raw 1e8 asset) drawn from treasury LP; only assets w/ treasury coverage.
	// Zero values mean the step does not apply to that pool.
	haircuts := []adr028PoolHaircut{
		// DOGE: fully covered by POL haircut (72,302 DOGE) + reserve synth burn (3,392,456 DOGE synths).
		{asset: common.DOGEAsset, step1RuneHaircut: 1_419_918_684_958, step2ReserveSynthBurn: 339_245_565_058_092},
		// BCH: fully covered by POL haircut (174 BCH).
		{asset: common.BCHAsset, step1RuneHaircut: 13_013_460_708_269},
		// BSC.BNB: fully covered by reserve synth burn (45.74 native BNB synths; reserve holds ~143 BNB).
		{asset: common.BNBBEP20Asset, step2ReserveSynthBurn: 4_574_000_000},
		// ETH.USDT: POL haircut (198,410 USDT) + reserve synth burn (27,022 USDT).
		// Savers overflow is RUNTIME-DERIVED by the strict waterfall (step1OverflowBps),
		// no longer a tabled bps — it equals the residual the reserve-capped POL-unit
		// removal could not absorb.
		{asset: ethUSDT, step1RuneHaircut: 33_880_349_267_266, step2ReserveSynthBurn: 2_702_221_779_137},
		// AVAX.USDC: synth units inflated, near-zero LP share → full 2713 bps savers haircut.
		{asset: avaxUSDC, step3SaversHaircutBps: 2713},
		// ETH.ETH: POL haircut (1,086 ETH = ~$3.26M) closes the bifrost-observed
		// absorbed loss from the vault 2v28 compromise (37.5% pool haircut, the
		// largest single per-pool fraction in this migration; POL reserve
		// absorbs the corresponding LP value loss via step-5 inflation).
		// Savers overflow is RUNTIME-DERIVED by the strict waterfall — for ETH's
		// synth-overhang regime it lands at ≈ ΔR/R·10000 bps (clamp linearity),
		// generalizing the BTC known-good result; no tabled bps.
		{asset: common.ETHAsset, step1RuneHaircut: 420_098_359_472_574},
		// GAIA.ATOM: no POL or synth coverage → 3121 bps savers haircut.
		{asset: common.ATOMAsset, step3SaversHaircutBps: 3121},
		// Zero-saver pools with confirmed treasury LP coverage:
		{asset: baseUSDC, step4TreasuryDraw: 4_904_100_000_000},
		{asset: bscBUSD, step4TreasuryDraw: 3_214_300_000_000},
		{asset: bscETH, step4TreasuryDraw: 1_600_000_000},
		{asset: bscBTCB, step4TreasuryDraw: 51_000_000},
		// ─── Pool-haircut entries closing additional vault 2v28 L1 backing
		// drops surfaced by the snapshot dry-run. Each ΔR is sub-1% of pool
		// RUNE; absorbed by POL via step-5 RUNEPool.ReserveUnits re-solve.
		// ETH.USDC: closes 27,179 USDC L1 gap (0.8% pool haircut).
		{asset: ethUSDC, step1RuneHaircut: 4_640_667_267_256},
		// TRON.TRX: closes 2,874 TRX L1 gap via TREASURY draw (not POL). The TRON
		// pool is ~99.7% treasury1-owned and has 0 POL units + 0 savers, so a step-1
		// POL haircut landed the whole loss on ordinary LPs (-88.41 bps DLP v/u
		// drift, flagged by the dry-run). Drawing ΔA from treasury1's LP position
		// (~1,228 of its 138,508 units) makes the treasury bear it and keeps the
		// ~0.3% ordinary LPs whole. ΔA = 287_392_444_998 (2,873.92444998 TRX).
		{asset: common.TRXAsset, step4TreasuryDraw: 287_392_444_998},
		// XRP.XRP: closes 19 XRP L1 gap (<0.01% pool haircut).
		{asset: common.XRPAsset, step1RuneHaircut: 4_831_551_272},
		// ─── Full attack-recapitalization entries for bifrost-OBSERVED native
		// gas-asset drains from vault 2v28. These losses were already absorbed
		// economically at attack time (bifrost decremented vault.Coins as the
		// attacker-initiated L1 outbounds were observed), but pool.BalanceAsset
		// did NOT follow — leaving a pool/vault aggregate gap. Step-1 here
		// closes that gap by reducing pool depth to match actual vault holdings;
		// POL absorbs the LP value loss via step-5 ReserveUnits inflation.
		// ΔR sized to match stolen_amount × poolRune/poolAsset at snapshot height
		// 26,355,121; §3e re-verify these against fresh state at upgrade height.
		// BTC.BTC: closes 40.73 BTC absorbed loss (~$4.07M), 19.0% pool haircut.
		// Savers overflow is RUNTIME-DERIVED by the strict waterfall. In BTC's
		// synth-overhang regime CalcUnits is linear (SynthUnits = LPUnits·S), so
		// the reserve_pool_capacity is ≈0 and the overflow lands at the SAME
		// fraction as the L1 depth cut: ΔR/R·10000 ≈ 1907 bps (the prior tabled
		// value). Burning the synth pool by ΔR/R drives LP value-per-unit drift
		// to ~0: savers bear their proportional share via fewer (whole) synths,
		// DLP & POL kept whole.
		{asset: common.BTCAsset, step1RuneHaircut: 568_945_622_679_283},
		// LTC.LTC: closes 6,866.75 LTC absorbed loss (~$686k), 41.5% pool haircut.
		// Tied for largest per-pool fraction with ETH.ETH; significant L1 pool
		// depth reduction expected.
		{asset: common.LTCAsset, step1RuneHaircut: 69_426_603_965_037},
		// AVAX.AVAX: closes 861 AVAX absorbed loss (~$26k), 1.1% pool haircut.
		{asset: common.AVAXAsset, step1RuneHaircut: 1_444_896_137_532},
	}

	// ── Step 1: POL haircut (per pool) ──────────────────────────────────────
	// Snapshot provider value BEFORE any pool write.
	runePool, migrationErr := m.mgr.Keeper().GetRUNEPool(ctx)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: pol haircut: fail to get RUNEPool: %w", migrationErr)
	}
	vPre, migrationErr := runePoolValue(ctx, m.mgr)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: pol haircut: fail to compute runePoolValue pre: %w", migrationErr)
	}
	providerValuePre := common.GetSafeShare(runePool.PoolUnits, runePool.TotalUnits(), vPre)

	// POL holds LP units in pools via the Reserve module's bech32 address.
	// We need polAddress to compute POL's fractional share of each step-1
	// haircut for accurate pol.RuneWithdrawn accounting (see below).
	polAddress, migrationErr := m.mgr.Keeper().GetModuleAddress(ReserveName)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: pol haircut: fail to get reserve module address: %w", migrationErr)
	}

	totalStep1Rune := cosmos.ZeroUint()        // sum of ΔR burned from Asgard (for logging/conservation)
	totalPOLRuneWithdrawn := cosmos.ZeroUint() // sum of POL's fractional share of ΔR per pool (for pol.RuneWithdrawn)
	totalPOLUnitsRemoved := cosmos.ZeroUint()  // sum of POL pool LP units removed (layer 1, for logging)
	// dlpTargetR/dlpTargetT: per step-1 pool, the PRE-migration BalanceRune and total
	// pool units. The savers overflow is solved LAST (step 3) against the realized
	// post-everything pool state to land total units at the DLP-neutral target
	// Tfinal = R_current · T_orig / R_orig — absorbing the exact residual regardless
	// of how step 1 (depth cut), step 2 (reserve burn + self-neutral paired cut) and
	// the layer-1 POL removal composed. Reserve-first ordering: layer 1 (POL) absorbs
	// up to its reserve cap, the step-2 reserve synth burn absorbs its share, and
	// savers cover only what remains.
	dlpTargetR := map[string]cosmos.Uint{}
	dlpTargetT := map[string]cosmos.Uint{}
	for _, h := range haircuts {
		if h.step1RuneHaircut == 0 {
			continue
		}
		pool, err := m.mgr.Keeper().GetPool(ctx, h.asset)
		if err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to get pool %s: %w", h.asset, err)
		}
		deltaR := cosmos.NewUint(h.step1RuneHaircut)
		if pool.BalanceRune.LT(deltaR) {
			return fmt.Errorf("conciliation: pol haircut: pool %s BalanceRune %s < haircut %d — abort",
				h.asset, pool.BalanceRune, h.step1RuneHaircut)
		}

		// Snapshot POL's fractional share BEFORE applying the haircut so
		// pol.RuneWithdrawn below records only POL's actual loss (not the full
		// ΔR burn). Critical for pools where POL is not the sole/majority LP
		// — for those, crediting the full ΔR would over-count POL's withdrawal
		// and could push pol.CurrentDeposit() negative, breaking the
		// maxPOLDeposit safety gate in manager_network_current.go.
		//
		// Compute CalcUnits in-memory on a local copy so the share denominator
		// (LPUnits + SynthUnits) reflects the current synth supply rather than
		// any stored-stale SynthUnits — critical for pools that are in the
		// degenerate synth-overhang regime (supply > 2×BalanceAsset), where
		// stored SynthUnits is correctly-but-astronomically inflated and POL's
		// share-of-pool-value is effectively zero.
		//
		// preR/preA/T/L are the PRE-haircut depth, total units and LP units used
		// by the strict-waterfall layer-1 math below.
		poolPreHaircut := pool
		synthSupply := m.mgr.Keeper().GetTotalSupply(ctx, h.asset.GetSyntheticAsset())
		poolPreHaircut.CalcUnits(synthSupply)
		preR := poolPreHaircut.BalanceRune
		T := poolPreHaircut.GetPoolUnits() // L + SU (pre-state)
		L := poolPreHaircut.LPUnits
		// Record pre-migration (R, T) so step 3 can solve the savers overflow against
		// the realized post-everything state to land DLP value-per-unit at neutral.
		dlpTargetR[h.asset.String()] = preR
		dlpTargetT[h.asset.String()] = T
		polLP, lpErr := m.mgr.Keeper().GetLiquidityProvider(ctx, h.asset, common.Address(polAddress.String()))

		// ── STRICT WATERFALL — Layer 1: remove POL's pool LP units ───────────
		// To keep DLP value-per-unit neutral after a burn_perc = ΔR/R depth cut,
		// total pool units must shrink by burn_perc. The IDEAL POL-only removal
		// is x* = burn_perc·L (= ΔR·L/preR): removing the same FRACTION of LP
		// units as the depth cut keeps (L−x)/L = 1−burn_perc, and since SU
		// scales with LPUnits in BOTH CalcUnits regimes (non-clamped: SU∝L;
		// clamped overhang: SU = L·S ∝ L), the post total units land at
		// (1−burn_perc)·T — DLP v/u neutral.
		//
		// CEILING: we may remove at most the POL units economically backed by
		// the RESERVE (vs RUNEPool providers): reserve_pool_capacity =
		// polL · ReserveUnits / TotalUnits. Removing more drives vPost below
		// providerValuePre, hitting step-5's wipe branch and forcing loss onto
		// providers — forbidden. x = min(x*, reserveCap, polL).
		xStar := cosmos.ZeroUint()
		if !preR.IsZero() {
			xStar = common.GetUncappedShare(deltaR, preR, L) // ΔR·L/preR
		}
		reserveCap := cosmos.ZeroUint()
		if lpErr == nil && !runePool.TotalUnits().IsZero() {
			reserveCap = common.GetSafeShare(runePool.ReserveUnits, runePool.TotalUnits(), polLP.Units)
		}
		x := xStar
		if x.GT(reserveCap) {
			x = reserveCap
		}
		if lpErr == nil && x.GT(polLP.Units) {
			x = polLP.Units
		}
		if lpErr != nil {
			x = cosmos.ZeroUint() // no POL LP record to remove from
		}
		if !x.IsZero() && lpErr == nil {
			polLP.Units = common.SafeSub(polLP.Units, x)
			m.mgr.Keeper().SetLiquidityProvider(ctx, polLP)
			pool.LPUnits = common.SafeSub(pool.LPUnits, x)
			totalPOLUnitsRemoved = totalPOLUnitsRemoved.Add(x)
		}

		// pol.RuneWithdrawn accounting: credit POL's RUNE-value of the units it
		// ACTUALLY lost — the layer-1 removal (x units) plus the depth-cut value
		// on POL's remaining units. We use the value of the removed units,
		// 2·GetSafeShare(x, T, preR), which is POL's realized loss from layer 1.
		// This keeps pol.CurrentDeposit() accurate without over-counting (the
		// G4b invariant — RuneDeposited ≥ RuneWithdrawn — is dry-run asserted).
		if lpErr == nil && !T.IsZero() && !x.IsZero() {
			polDeltaR := common.GetSafeShare(x, T, preR).MulUint64(2)
			totalPOLRuneWithdrawn = totalPOLRuneWithdrawn.Add(polDeltaR)
		} else if lpErr == nil && !poolPreHaircut.GetPoolUnits().IsZero() && !polLP.Units.IsZero() {
			// Fallback (no layer-1 removal happened, e.g. reserveCap=0): record
			// POL's fractional share of the depth-cut ΔR as before.
			polDeltaR := common.GetSafeShare(polLP.Units, poolPreHaircut.GetPoolUnits(), deltaR)
			totalPOLRuneWithdrawn = totalPOLRuneWithdrawn.Add(polDeltaR)
		}
		// If POL has no LP in this pool (lpErr) or pool is degenerate (synth
		// dominates → POL share ≈ 0), totalPOLRuneWithdrawn gets no contribution
		// for this pool. The full ΔR is still burned from Asgard below — only
		// POL's accounting is fractional.

		// Savers overflow is NOT computed here — it is solved LAST in step 3 against
		// the realized pool state (dlpTargetR/T), so it absorbs the exact residual
		// after this depth cut, the step-2 reserve burn+pairing, and layer 1. Layer 1
		// (reserve-first POL removal) is applied above; savers cover only the rest.
		ctx.Logger().Info("conciliation: pol haircut: strict-waterfall layer 1",
			"asset", h.asset, "x_star", xStar, "reserve_cap", reserveCap, "pol_units_removed", x)

		// ΔA = ΔR × BalanceAsset / BalanceRune (price-ratio preservation).
		deltaA := common.GetSafeShare(deltaR, pool.BalanceRune, pool.BalanceAsset)
		pool.BalanceRune = common.SafeSub(pool.BalanceRune, deltaR)
		pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, deltaA)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to save pool %s: %w", h.asset, err)
		}
		// Freed RUNE: burn HALF, retain HALF in the Reserve (was: burn all).
		if err := m.splitFreedRune(ctx, deltaR, "conciliation_pol_haircut"); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to dispose freed RUNE for %s: %w", h.asset, err)
		}
		mod := NewPoolMod(h.asset, deltaR, false, deltaA, false)
		emitPoolBalanceChangedEvent(ctx, mod, "conciliation_pol_haircut", m.mgr)
		totalStep1Rune = totalStep1Rune.Add(deltaR)
		ctx.Logger().Info("conciliation: pol haircut: POL haircut applied",
			"asset", h.asset, "delta_rune", deltaR, "delta_asset", deltaA)
	}

	// Track POL withdrawal in the POL record so CurrentDeposit() stays accurate.
	// IMPORTANT: only POL's fractional share of each pool's ΔR is recorded as a
	// POL withdrawal — NOT the full ΔR sum. Recording the full ΔR would over-
	// state POL's loss (treating it as if POL owned 100% of every haircut pool)
	// and could push pol.RuneWithdrawn past pol.RuneDeposited → negative
	// CurrentDeposit → broken maxPOLDeposit safety gate.
	if !totalPOLRuneWithdrawn.IsZero() {
		pol, err := m.mgr.Keeper().GetPOL(ctx)
		if err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to get POL: %w", err)
		}
		pol.RuneWithdrawn = pol.RuneWithdrawn.Add(totalPOLRuneWithdrawn)
		if err := m.mgr.Keeper().SetPOL(ctx, pol); err != nil {
			return fmt.Errorf("conciliation: pol haircut: fail to save POL: %w", err)
		}
		ctx.Logger().Info("conciliation: pol haircut: POL.RuneWithdrawn adjusted",
			"total_step1_rune_burned", totalStep1Rune,
			"pol_fractional_withdrawn", totalPOLRuneWithdrawn,
			"non_pol_share", common.SafeSub(totalStep1Rune, totalPOLRuneWithdrawn),
			"total_pol_units_removed", totalPOLUnitsRemoved)
	}

	// ── Step 2: reserve synth burn ───────────────────────────────────────────
	// Pure bank burn — no pool balance mutation.  FreedSynthUnits shifts from
	// synth-claim to LP-claim on the next CalcUnits recompute.
	//
	// The Reserve module account has no Burner permission (see app.maccPerms),
	// so the synth is first sent to thorchain.ModuleName (the only module with
	// Minter+Burner) and burned from there — the standard cosmos-sdk pattern.
	//
	// Cap burnAmt to what Reserve actually holds rather than aborting, mirroring
	// the step-3 idiom: snapshot drift may leave the Reserve synth balance below
	// the planned amount, but a partial burn is still strictly better than
	// halting the chain mid-upgrade. The shortfall is logged for off-chain
	// reconciliation.
	for _, h := range haircuts {
		if h.step2ReserveSynthBurn == 0 {
			continue
		}
		synthAsset := h.asset.GetSyntheticAsset()
		burnAmt := cosmos.NewUint(h.step2ReserveSynthBurn)
		reserveSynthBal := m.mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, synthAsset.Native())
		if reserveSynthBal.LT(burnAmt) {
			ctx.Logger().Error("conciliation: reserve synth burn: Reserve synth balance below burnAmt, capping — shortfall left",
				"asset", synthAsset,
				"reserve_balance", reserveSynthBal,
				"burn_amt", burnAmt,
				"shortfall", common.SafeSub(burnAmt, reserveSynthBal))
			burnAmt = reserveSynthBal
		}
		if burnAmt.IsZero() {
			ctx.Logger().Info("conciliation: reserve synth burn: nothing to burn, skipping",
				"asset", synthAsset)
			continue
		}
		synthCoin := common.NewCoin(synthAsset, burnAmt)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, ModuleName, common.NewCoins(synthCoin)); err != nil {
			return fmt.Errorf("conciliation: reserve synth burn: fail to send %s reserve→thorchain: %w", synthAsset, err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, synthCoin); err != nil {
			return fmt.Errorf("conciliation: reserve synth burn: fail to burn %s from thorchain: %w", synthAsset, err)
		}
		burnEvt := NewEventMintBurn(BurnSupplyType, synthCoin.Asset.Native(), synthCoin.Amount, "conciliation_reserve_synth_burn")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: reserve synth burn: fail to emit burn event", "error", err, "asset", synthAsset)
		}
		ctx.Logger().Info("conciliation: reserve synth burn: burned reserve synths",
			"asset", synthAsset, "amount", burnAmt)

		// ── Self-neutral pairing (Option 2): retire the pool VALUE the burned reserve
		// synths claimed, so DLP/saver value-per-unit is UNCHANGED. A bare burn drops
		// SynthUnits and sprays that freed v/u onto everyone else; pairing it with a
		// depth cut keeps the reserve's absorption position-neutral. Closed-form
		// depth-keep fraction r = R'/R solving L(S−X)/(2A·r−(S−X)) = rT − L exactly
		// (this accounts for A's own re-inflation of SU, which the naive R·ΔSU/T missed):
		//   non-overhang (2A > S): r = (T(S−X) + 2AL) / (2AT)
		//   overhang     (2A ≤ S): r = (S−X) / S      (CalcUnits clamp → SU = L·S)
		// Cut R→rR, A→rA and burn the freed (1−r)·R RUNE; the asset-claim drop also
		// tightens L1 solvency.
		pool, perr := m.mgr.Keeper().GetPool(ctx, h.asset)
		if perr != nil {
			return fmt.Errorf("conciliation: reserve synth burn: fail to get pool %s: %w", h.asset, perr)
		}
		sPre := m.mgr.Keeper().GetTotalSupply(ctx, synthAsset).Add(burnAmt) // supply BEFORE the burn
		if !pool.BalanceRune.IsZero() && !pool.BalanceAsset.IsZero() && !sPre.IsZero() {
			pCopy := pool
			pCopy.CalcUnits(sPre)
			T := pCopy.GetPoolUnits() // L + SU(pre-burn)
			// Pure regime-branching arithmetic extracted to adr028PairedCut
			// (migration_math_15to16.go) so the overhang/non-overhang boundary is
			// CI-unit-testable.
			dRpair, dApair := adr028PairedCut(pool.BalanceRune, pool.BalanceAsset, pool.LPUnits, T, sPre, burnAmt)
			if !dRpair.IsZero() {
				pool.BalanceRune = common.SafeSub(pool.BalanceRune, dRpair)
				pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, dApair)
				if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
					return fmt.Errorf("conciliation: reserve synth burn: fail to save paired cut %s: %w", h.asset, err)
				}
				// Freed RUNE from the paired cut: burn HALF, retain HALF in the Reserve.
				if err := m.splitFreedRune(ctx, dRpair, "conciliation_reserve_paired_cut"); err != nil {
					return fmt.Errorf("conciliation: reserve synth burn: fail to dispose paired RUNE %s: %w", h.asset, err)
				}
				ctx.Logger().Info("conciliation: reserve synth burn: self-neutral paired depth cut",
					"asset", h.asset, "rune_cut", dRpair, "asset_cut", dApair)
			}
		}
	}

	// ── Step 3: savers haircut ───────────────────────────────────────────────
	// Operates on the synth vault pool (asset = synthAsset, one-sided LP with
	// BalanceAsset only).  Reduces every saver LP unit pro-rata, reduces pool
	// LPUnits and BalanceAsset, then burns the freed synth supply from Asgard.
	for _, h := range haircuts {
		synthAsset := h.asset.GetSyntheticAsset()
		// Strict-waterfall OVERFLOW-LAST: for a step-1 pool, solve the savers haircut
		// NOW (against the realized post-step-1/step-2/layer-1 pool state) so total
		// pool units land at the DLP-neutral target Tfinal = R_current·T_orig/R_orig.
		// This OVERRIDES any tabled step3SaversHaircutBps and absorbs the exact
		// residual the reserve-first layers (POL + reserve burn) could not. Pure-savers
		// pools (no step-1, e.g. AVAX.USDC, GAIA.ATOM) keep their tabled bps.
		bps := h.step3SaversHaircutBps
		if rOrig, ok := dlpTargetR[h.asset.String()]; ok && !rOrig.IsZero() {
			// A GetPool failure here must abort, not silently fall through: for every
			// step-1 pool the tabled bps is 0, so swallowing the error would skip the
			// savers haircut entirely and under-allocate that pool's loss — an
			// undetectable error in a one-shot consensus migration. (Matches the
			// hard-error handling on the saver-pool GetPool just below.)
			cur, cerr := m.mgr.Keeper().GetPool(ctx, h.asset)
			if cerr != nil {
				return fmt.Errorf("conciliation: savers haircut: fail to re-read pool %s for overflow solve: %w", h.asset, cerr)
			}
			sCur := m.mgr.Keeper().GetTotalSupply(ctx, synthAsset)
			tFinal := common.GetUncappedShare(cur.BalanceRune, rOrig, dlpTargetT[h.asset.String()])
			bps = m.adr028SolveOverflowBps(ctx, h.asset, cur.LPUnits, cur.BalanceAsset, sCur, tFinal)
		}
		if bps == 0 {
			continue
		}
		saverPool, err := m.mgr.Keeper().GetPool(ctx, synthAsset)
		if err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to get saver pool %s: %w", synthAsset, err)
		}
		// burnAmt = pool.BalanceAsset × bps / 10000
		burnAmt := saverPool.BalanceAsset.MulUint64(bps).QuoUint64(10000)
		if burnAmt.IsZero() {
			ctx.Logger().Error("conciliation: savers haircut: zero burnAmt, skipping", "asset", synthAsset)
			continue
		}
		// Cap burnAmt to what Asgard actually holds rather than aborting; log any shortfall.
		asgardSynthBal := m.mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synthAsset.Native())
		if asgardSynthBal.LT(burnAmt) {
			ctx.Logger().Error("conciliation: savers haircut: Asgard synth balance below burnAmt, capping — shortfall left",
				"asset", synthAsset,
				"asgard_balance", asgardSynthBal,
				"burn_amt", burnAmt,
				"shortfall", common.SafeSub(burnAmt, asgardSynthBal))
			burnAmt = asgardSynthBal
		}
		// Collect all saver LP records before modifying (safe iteration pattern).
		// Drain inside a closure so the iterator is deferred-closed per pool — and
		// on a MustUnmarshal panic — without leaking a defer across the haircuts loop.
		saverLPs := func() []LiquidityProvider {
			iter := m.mgr.Keeper().GetLiquidityProviderIterator(ctx, synthAsset)
			defer iter.Close()
			var lps []LiquidityProvider
			for ; iter.Valid(); iter.Next() {
				var lp LiquidityProvider
				m.mgr.Keeper().Cdc().MustUnmarshal(iter.Value(), &lp)
				lps = append(lps, lp)
			}
			return lps
		}()
		// Reduce each saver's LP units in proportion to the synth actually burned
		// (deduct = lp.Units × burnAmt / BalanceAsset, on the pre-haircut depth),
		// accumulating the exact per-LP deductions to use as the pool-level
		// deduction. Deriving the unit reduction from burnAmt — rather than from
		// bps directly — keeps it coupled to the asset reduction even when burnAmt
		// was capped to the Asgard synth balance above; otherwise LPUnits would
		// shrink by the full bps while BalanceAsset shrank by less, inflating the
		// savers' value-per-unit. In the uncapped path burnAmt = ⌊BalanceAsset ×
		// bps/10000⌋, so this matches the bps haircut. Accumulating per-LP
		// truncations (not a pool-level ⌊⌋) keeps saverPool.LPUnits == Σ lp.Units
		// exact.
		poolAssetPre := saverPool.BalanceAsset
		sumLPDeductions := cosmos.ZeroUint()
		for _, lp := range saverLPs {
			deduct := lp.Units.Mul(burnAmt).Quo(poolAssetPre)
			lp.Units = common.SafeSub(lp.Units, deduct)
			m.mgr.Keeper().SetLiquidityProvider(ctx, lp)
			sumLPDeductions = sumLPDeductions.Add(deduct)
		}
		// Reduce synth pool depth using the accumulated LP deduction so that
		// saverPool.LPUnits == Σ lp.Units is maintained exactly.
		saverPool.LPUnits = common.SafeSub(saverPool.LPUnits, sumLPDeductions)
		saverPool.BalanceAsset = common.SafeSub(saverPool.BalanceAsset, burnAmt)
		if err := m.mgr.Keeper().SetPool(ctx, saverPool); err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to save saver pool %s: %w", synthAsset, err)
		}
		// Burn the freed synth supply from Asgard (bank pairing:
		// Asgard→thorchain→burn — same idiom as step 1's RUNE burn, since
		// AsgardName has no Burner permission in app.maccPerms).
		synthCoin := common.NewCoin(synthAsset, burnAmt)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(synthCoin)); err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to move synths from Asgard for %s: %w", synthAsset, err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, synthCoin); err != nil {
			return fmt.Errorf("conciliation: savers haircut: fail to burn synths for %s: %w", synthAsset, err)
		}
		mod := NewPoolMod(synthAsset, cosmos.ZeroUint(), false, burnAmt, false)
		emitPoolBalanceChangedEvent(ctx, mod, "conciliation_saver_haircut", m.mgr)
		burnEvt := NewEventMintBurn(BurnSupplyType, synthCoin.Asset.Native(), synthCoin.Amount, "conciliation_saver_haircut")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("conciliation: savers haircut: fail to emit burn event", "error", err, "asset", synthAsset)
		}
		ctx.Logger().Info("conciliation: savers haircut: savers haircut applied",
			"asset", synthAsset, "bps", bps, "burn_amt", burnAmt,
			"lp_count", len(saverLPs))
	}

	// ── Step 4: treasury LP draw ─────────────────────────────────────────────
	// Covers residual shortfall for zero-saver assets where treasury holds LP.
	// ΔA = step4TreasuryDraw; ΔR = ΔA × BalanceRune / BalanceAsset.
	// Treasury LP units are reduced by ΔA / BalanceAsset (proportional share).
	// Accounting convention: totalStep4PlannedRune / totalStep4DrawnRune track
	// ONLY pools where a draw could be PRICED and attempted — i.e. pools with live
	// depth. uncovered_rune = planned − drawn is then the RUNE for healthy pools we
	// priced but could not fully draw (treasury-LP record missing → full plannedR
	// uncovered; treasury units < needed → the capped-off slice). Pools skipped for
	// zero depth cannot be priced (a draw on an empty pool is 0 RUNE), so forcing
	// them into the netting would add a meaningless 0; they are reported separately
	// in skipped_pools instead so off-chain reconciliation still sees them.
	totalStep4PlannedRune := cosmos.ZeroUint()
	totalStep4DrawnRune := cosmos.ZeroUint()
	var step4SkippedPools []string
	treasuryAddr, migrationErr := common.NewAddress(adr028TreasuryAddr)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: treasury draw: fail to parse treasury address: %w", migrationErr)
	}
	for _, h := range haircuts {
		if h.step4TreasuryDraw == 0 {
			continue
		}
		pool, err := m.mgr.Keeper().GetPool(ctx, h.asset)
		if err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to get pool %s: %w", h.asset, err)
		}
		if pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() {
			ctx.Logger().Error("conciliation: treasury draw: pool has zero balance, skipping",
				"asset", h.asset, "balance_asset", pool.BalanceAsset, "balance_rune", pool.BalanceRune)
			// Zero depth → no priceable draw; report separately (see convention above).
			step4SkippedPools = append(step4SkippedPools, h.asset.String())
			continue
		}
		deltaA := cosmos.NewUint(h.step4TreasuryDraw)
		// Explicit pre-condition: deltaA must not exceed pool depth. GetSafeShare
		// silently caps deltaR at BalanceRune when deltaA > BalanceAsset, so the
		// "LT(deltaR)" guard below would never fire. A bad snapshot amount must be
		// a hard error, not a silent full-pool drain.
		if deltaA.GT(pool.BalanceAsset) {
			return fmt.Errorf("conciliation: treasury draw: pool %s step4TreasuryDraw %s exceeds BalanceAsset %s — snapshot out of date",
				h.asset, deltaA, pool.BalanceAsset)
		}
		deltaR := common.GetSafeShare(deltaA, pool.BalanceAsset, pool.BalanceRune)
		plannedR := deltaR
		// LP units corresponding to ΔA of the pool (prop to total).
		deltaLP := common.GetSafeShare(deltaA, pool.BalanceAsset, pool.LPUnits)
		// Reduce treasury LP record.
		treasuryLP, err := m.mgr.Keeper().GetLiquidityProvider(ctx, h.asset, treasuryAddr)
		if err != nil {
			ctx.Logger().Error("conciliation: treasury draw: fail to get treasury LP, skipping",
				"asset", h.asset, "error", err)
			totalStep4PlannedRune = totalStep4PlannedRune.Add(plannedR)
			continue
		}
		if treasuryLP.Units.LT(deltaLP) {
			ctx.Logger().Error("conciliation: treasury draw: treasury LP units < deltaLP, capping",
				"asset", h.asset, "treasury_units", treasuryLP.Units, "delta_lp", deltaLP)
			deltaLP = treasuryLP.Units
			// Recompute deltaA, deltaR from the capped deltaLP.
			deltaA = common.GetSafeShare(deltaLP, pool.LPUnits, pool.BalanceAsset)
			deltaR = common.GetSafeShare(deltaA, pool.BalanceAsset, pool.BalanceRune)
		}
		treasuryLP.Units = common.SafeSub(treasuryLP.Units, deltaLP)
		m.mgr.Keeper().SetLiquidityProvider(ctx, treasuryLP)
		pool.LPUnits = common.SafeSub(pool.LPUnits, deltaLP)
		pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, deltaA)
		pool.BalanceRune = common.SafeSub(pool.BalanceRune, deltaR)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to save pool %s: %w", h.asset, err)
		}
		// Freed RUNE: burn HALF, retain HALF in the Reserve (was: burn all).
		if err := m.splitFreedRune(ctx, deltaR, "conciliation_treasury_draw"); err != nil {
			return fmt.Errorf("conciliation: treasury draw: fail to dispose freed RUNE for %s: %w", h.asset, err)
		}
		mod := NewPoolMod(h.asset, deltaR, false, deltaA, false)
		emitPoolBalanceChangedEvent(ctx, mod, "conciliation_treasury_draw", m.mgr)
		totalStep4PlannedRune = totalStep4PlannedRune.Add(plannedR)
		totalStep4DrawnRune = totalStep4DrawnRune.Add(deltaR)
		ctx.Logger().Info("conciliation: treasury draw: treasury LP draw applied",
			"asset", h.asset, "delta_asset", deltaA, "delta_rune", deltaR, "delta_lp", deltaLP)
	}
	ctx.Logger().Info("conciliation: treasury draw: coverage summary",
		"total_planned_rune", totalStep4PlannedRune,
		"total_drawn_rune", totalStep4DrawnRune,
		"uncovered_rune", common.SafeSub(totalStep4PlannedRune, totalStep4DrawnRune),
		"skipped_pools", step4SkippedPools,
		"skipped_pool_count", len(step4SkippedPools))

	// ── Step 4.5: recompute SynthUnits on every L1 pool the waterfall touched ─
	//
	// Steps 2 and 3 burn synth supply via direct bank ops (Reserve→ModuleName→burn
	// for step 2; Asgard→ModuleName→burn for step 3); these do NOT update the L1
	// pool's SynthUnits in the keeper — that recompute is normally deferred to
	// the next swap/withdraw/add-liquidity on that pool via `pool.CalcUnits(...)`.
	//
	// Step 1 reduces BalanceAsset without touching synth supply; the formula
	// SU = (LPUnits × supply) / (2×BalanceAsset − supply) has BalanceAsset in
	// the denominator, so a depth haircut also inflates SynthUnits on next
	// CalcUnits — meaning LPs lose MORE share than just the depth haircut.
	//
	// Without an explicit recompute here, LPs withdrawing in the window between
	// this upgrade block and the first swap/withdraw/add on the pool would read
	// the stale keeper-persisted SynthUnits → wrong (under-)payout. Closing that
	// window is the reason this step exists and must be kept.
	//
	// NOTE: step 5's runePoolValue()/polPoolValue() does NOT depend on this
	// recompute. polPoolValue (helpers.go) calls pool.CalcUnits(GetTotalSupply)
	// on a fresh in-memory pool copy *before* reading GetPoolUnits(), so it never
	// consumes the keeper-persisted SynthUnits field — the POL solve is correct
	// whether or not step 4.5 has run. (An earlier comment here claimed a stale
	// SynthUnits would inflate the POL denominator and make POL over-absorb; that
	// is false for the reason just stated.) The persisted recompute matters only
	// for the LP-withdraw window above.
	//
	// Fix: iterate every haircut-table entry whose step 1/2/3 touched the
	// underlying L1 pool or its synth supply, call pool.CalcUnits(synthSupply)
	// with the post-migration supply, persist via SetPool. Step 4 already keeps
	// LPUnits and BalanceAsset proportional, so its pools also benefit (CalcUnits
	// re-anchors them too without changing the ratio).
	for _, h := range haircuts {
		if h.step1RuneHaircut == 0 && h.step2ReserveSynthBurn == 0 &&
			h.step3SaversHaircutBps == 0 && h.step4TreasuryDraw == 0 {
			continue
		}
		pool, err := m.mgr.Keeper().GetPool(ctx, h.asset)
		if err != nil {
			return fmt.Errorf("conciliation: synthunits recompute: fail to get pool %s: %w", h.asset, err)
		}
		synthSupply := m.mgr.Keeper().GetTotalSupply(ctx, h.asset.GetSyntheticAsset())
		oldSU := pool.SynthUnits
		pool.CalcUnits(synthSupply)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("conciliation: synthunits recompute: fail to save pool %s: %w", h.asset, err)
		}
		ctx.Logger().Info("conciliation: synthunits recompute: pool.SynthUnits recomputed",
			"asset", h.asset, "synth_supply", synthSupply, "old_synth_units", oldSU, "new_synth_units", pool.SynthUnits)
	}

	// ── POL provider-whole solve (after all pool writes) ─────────────────────
	// Now that Steps 1–4 have applied every pool mutation (POL haircut, reserve
	// + savers synth burns, treasury draw), re-snapshot POL value and solve
	// RUNEPool.ReserveUnits so provider value is preserved (the reserve bucket
	// absorbs the entire loss). Steps 2–4 burn synths and draw treasury LP from
	// pools that also hold POL, each of which raises POL value, so vPost must be
	// taken here rather than right after Step 1. providerValuePre was captured
	// before any pool write.
	vPost, migrationErr := runePoolValue(ctx, m.mgr)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: pol solve: fail to compute runePoolValue post: %w", migrationErr)
	}
	switch {
	case runePool.PoolUnits.IsZero():
		// No provider (POL) units to hold whole — leave ReserveUnits untouched.
		// Without this guard providerValuePre would be 0 and the solve below would
		// collapse ReserveUnits to 0, wiping a reserve-only RUNEPool position.
		ctx.Logger().Info("conciliation: pol solve: PoolUnits is zero; leaving ReserveUnits unchanged",
			"reserve_units", runePool.ReserveUnits)
	case providerValuePre.GTE(vPost) || runePool.TotalUnits().IsZero():
		// Edge case: reserve is fully wiped or no units — set ReserveUnits to 0.
		ctx.Logger().Error("conciliation: pol solve: providerValuePre >= vPost or TotalUnits=0; zeroing ReserveUnits",
			"v_pre", vPre, "v_post", vPost, "provider_value_pre", providerValuePre)
		runePool.ReserveUnits = cosmos.ZeroUint()
	default:
		// ReserveUnits_post = PoolUnits × vPost / providerValuePre − PoolUnits
		newTotalUnits := common.GetUncappedShare(runePool.PoolUnits, providerValuePre, vPost)
		runePool.ReserveUnits = common.SafeSub(newTotalUnits, runePool.PoolUnits)
	}
	m.mgr.Keeper().SetRUNEPool(ctx, runePool)
	ctx.Logger().Info("conciliation: pol solve: RUNEPool.ReserveUnits adjusted",
		"v_pre", vPre, "v_post", vPost,
		"provider_value_pre", providerValuePre,
		"new_reserve_units", runePool.ReserveUnits)
	// STRICT-WATERFALL safety: the layer-1 removal is capped by reserve_pool_capacity
	// precisely so that vPost never falls below providerValuePre, i.e. step 5's wipe
	// branch is NEVER taken when any POL units were removed. If ReserveUnits is zero
	// here while layer 1 removed POL units, the reserve ceiling was mis-derived and the
	// loss spilled onto RUNEPool providers — log an ERROR for off-chain reconciliation.
	if runePool.ReserveUnits.IsZero() && !totalPOLUnitsRemoved.IsZero() {
		ctx.Logger().Error("conciliation: pol solve: ReserveUnits ZEROED despite layer-1 POL removal — reserve ceiling mis-derived; loss may have hit RUNEPool providers",
			"total_pol_units_removed", totalPOLUnitsRemoved,
			"v_pre", vPre, "v_post", vPost, "provider_value_pre", providerValuePre)
	}

	// ── Step 5: settle the stuck streaming swap ──────────────────────────────
	// Tx 5071A7 was an ETH.DAI→ETH.USDT streaming swap, 484/1182 sub-swaps done.
	// Two amounts are owed to the user (the swap's destination):
	//   1. StreamingSwap.Out — ETH.USDT already produced by the completed
	//      sub-swaps. The ETH.USDT pool was already debited for it; it never paid
	//      out because the swap never completed. We schedule it as a normal
	//      ETH.USDT outbound from a live Asgard vault — unlike a queued swap, an
	//      outbound persists in the queue until ETH signing resumes. If no active
	//      vault holds enough ETH.USDT to schedule it, we log and skip rather than
	//      abort the migration; the treasury then covers this portion off-chain.
	//   2. The remainder of the user's guaranteed minimum not paid on-chain
	//      (TradeTarget − amount actually scheduled) — its input DAI was stuck in
	//      the drained 2v28 vault. The treasury refunds it to the user off-chain
	//      from its own funds and follows up for reimbursement separately, so the
	//      migration moves no funds for it; we only log the amount owed. (We also
	//      do not queue an on-chain swap: it would run later in EndBlock under
	//      full validation and be aborted while trading is halted.)
	//
	// Trading is halted at upgrade height, so no further sub-swaps execute and
	// StreamingSwap.Out is final — we read it live and pay exactly that.
	// (stuckStreamingTxStr is defined in migration_data_15to16.go.)
	stuckTxID, migrationErr := common.NewTxID(stuckStreamingTxStr)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: streaming swap: failed to parse txid: %w", migrationErr)
	}

	// Idempotent: if the swap already completed or was removed between the
	// snapshot and the upgrade height there is nothing to settle. GetStreamingSwap
	// returns a zero-valued record with a nil error when the key is absent, so
	// check existence explicitly.
	if !m.mgr.Keeper().StreamingSwapExists(ctx, stuckTxID) {
		ctx.Logger().Info("conciliation: streaming swap: stuck swap already absent; nothing to settle",
			"tx", stuckStreamingTxStr)
		return nil
	}

	stuckSwap, migrationErr := m.mgr.Keeper().GetStreamingSwap(ctx, stuckTxID)
	if migrationErr != nil {
		return fmt.Errorf("conciliation: streaming swap: failed to get streaming swap: %w", migrationErr)
	}
	// The destination lives on the queue item (MsgSwap); it is needed only to pay
	// the on-chain outbound. Tolerate its absence (best-effort) so a drifted or
	// consumed queue item does not abort the upgrade — the treasury then refunds
	// the full amount off-chain.
	var destination common.Address
	if stuckQueueMsg, qErr := m.mgr.Keeper().GetSwapQueueItem(ctx, stuckTxID, 0); qErr == nil {
		destination = stuckQueueMsg.Destination
	}

	// 1. Pay the already-produced ETH.USDT (StreamingSwap.Out) as a best-effort
	//    outbound from a live Asgard vault (the outbound fee is deducted by
	//    TxOutStore). ethUSDT was parsed above for the haircut table. Everything
	//    here is best-effort: if the destination is unknown or no active vault
	//    holds enough ETH.USDT to schedule it (e.g. the L1 USDT distribution at
	//    upgrade height does not satisfy it), log and fall through rather than
	//    abort the whole migration — the treasury then refunds this portion
	//    off-chain too.
	usdtOut := stuckSwap.Out
	usdtPaidOnChain := cosmos.ZeroUint()
	switch {
	case usdtOut.IsZero():
		// No output produced yet; the whole guaranteed minimum is refunded off-chain.
	case destination.IsEmpty():
		ctx.Logger().Error("conciliation: streaming swap: unknown destination; skipping ETH.USDT outbound, treasury to refund off-chain",
			"usdt_out", usdtOut)
	default:
		usdtCoin := common.NewCoin(ethUSDT, usdtOut)
		if err := unsafeAddRefundOutbound(ctx, m.mgr, stuckStreamingTxStr, destination.String(), usdtCoin, ctx.BlockHeight()); err != nil {
			ctx.Logger().Error("conciliation: streaming swap: could not schedule ETH.USDT outbound; treasury to refund it off-chain",
				"error", err, "usdt_out", usdtOut, "beneficiary", destination)
		} else {
			usdtPaidOnChain = usdtOut
		}
	}

	// 2. Whatever is not paid on-chain (TradeTarget − usdtPaidOnChain — the
	//    unfulfilled minimum plus any USDT that could not be scheduled above) is
	//    refunded to the user off-chain by the treasury from its own funds; the
	//    treasury follows up for reimbursement separately. The migration moves no
	//    funds for it — record the amount owed.
	//
	// POLICY / FINALITY CAVEAT: usdtPaidOnChain is the amount we *queued* as an
	// outbound, and this netting assumes that queued outbound ultimately settles
	// on-chain. A TxOutItem is durable — it persists in the outbound queue and is
	// retried until ETH signing resumes — so under normal operation "queued" does
	// reach the user. But if that outbound is *permanently* unsignable (vault
	// churn, asset no longer held, manual queue surgery), the user would be short
	// both on-chain (never delivered) and off-chain (already netted out here).
	// The treasury MUST monitor finality of the usdt_paid_onchain outbound and
	// top up the user off-chain if it never lands. We log this explicitly below.
	usdtOffchainRefund := common.SafeSub(stuckSwap.TradeTarget, usdtPaidOnChain)
	if !usdtPaidOnChain.IsZero() {
		ctx.Logger().Warn("conciliation: streaming swap: off-chain refund nets out a QUEUED (not yet finalized) outbound; treasury must verify on-chain delivery and top up off-chain if it fails to settle",
			"usdt_paid_onchain", usdtPaidOnChain,
			"usdt_offchain_refund", usdtOffchainRefund,
			"beneficiary", destination)
	}

	// 3. Stop the stuck swap: remove the streaming-swap record and queue item so
	//    the remaining sub-swaps never execute.
	m.mgr.Keeper().RemoveStreamingSwap(ctx, stuckTxID)
	m.mgr.Keeper().RemoveSwapQueueItem(ctx, stuckTxID, 0)

	ctx.Logger().Info("conciliation: streaming swap: settled stuck swap",
		"tx", stuckStreamingTxStr,
		"usdt_produced", usdtOut,
		"usdt_paid_onchain", usdtPaidOnChain,
		"usdt_offchain_refund", usdtOffchainRefund,
		"beneficiary", destination)

	return nil
}

// Migrate16to17 requeues dangling ETH, GAIA, BTC, and LTC user settlement outbounds
// whose planned voter actions are no longer present in the live outbound queue.
func (m Migrator) Migrate16to17(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	specs, err := mainnetDanglingSettlementSpecs16to17()
	if err != nil {
		return err
	}

	requeueDanglingSettlementOutbounds(ctx, m.mgr, specs, ctx.BlockHeight()+300)

	return nil
}

// requeueDanglingSettlementOutbounds applies best-effort requeues for the fixed
// mainnet migration batch. One bad or already-settled row is logged and skipped
// so drifted live-chain state does not abort the whole store migration.
func requeueDanglingSettlementOutbounds(ctx cosmos.Context, mgr *Mgrs, specs []danglingSettlementSpec, outboundHeight int64) {
	for _, spec := range specs {
		if err := requeueDanglingSettlementOutbound(ctx, mgr, spec, outboundHeight); err != nil {
			ctx.Logger().Error("failed to requeue dangling settlement outbound",
				"tx_id", spec.TxID.String(),
				"chain", spec.Chain.String(),
				"asset", spec.Asset.String(),
				"amount", spec.Amount.String(),
				"error", err)
			continue
		}
		ctx.Logger().Info("requeued dangling settlement outbound",
			"tx_id", spec.TxID.String(),
			"chain", spec.Chain.String(),
			"asset", spec.Asset.String(),
			"amount", spec.Amount.String())
	}
}

func mainnetDanglingSettlementSpecs16to17() ([]danglingSettlementSpec, error) {
	ethUSDT, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	if err != nil {
		return nil, fmt.Errorf("failed to parse ETH.USDT: %w", err)
	}
	ethUSDC, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	if err != nil {
		return nil, fmt.Errorf("failed to parse ETH.USDC: %w", err)
	}
	ethWBTC, err := common.NewAsset("ETH.WBTC-0X2260FAC5E5542A773AA44FBCFEDF7C193BC2C599")
	if err != nil {
		return nil, fmt.Errorf("failed to parse ETH.WBTC: %w", err)
	}

	rows := []struct {
		txID   string
		chain  common.Chain
		asset  common.Asset
		amount uint64
	}{
		{
			txID:   "62566F78A66B8C1D6D756A93E28CA0AD07532FF5949A1393B5828429A1AD0A7B",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 236_382_280_700,
		},
		{
			txID:   "15D63325B96BD0FE409EB1FDE21D8A6145894D364D14F98C17068C704CFBE749",
			chain:  common.ETHChain,
			asset:  ethUSDT,
			amount: 499_997_734_700,
		},
		{
			txID:   "74D5D2F1BE68CB15DF9656BD2A11733FC64FE17CF0DC030A9F5948D93AC5A561",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 499_997_734_500,
		},
		{
			txID:   "4FE29CBBB8A82728E7A1A8E5E4527830B53E6132E74BCB51B02EBBB94CC26597",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 742_745_645_600,
		},
		{
			txID:   "D8A75A8427814347C36AFB1E972785C90D1AF2BCDA8195DAD152AE5C15E08D62",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 97_734_500,
		},
		{
			txID:   "EBE39D82C348CCE1F004D61870C458BD86D1BE6AD202C7C6A1B33989B6774D14",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 97_734_500,
		},
		{
			txID:   "84AF6BC1ABEE78AD1B2EBCD5F1A6FF4773B53E734F57EC840B55BE55039A2163",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 2_981_767_801_900,
		},
		{
			txID:   "D537A15D820D9A800ECCEB5735C1A01ACB89F7EE9CB9D5F0F041DD60403EADDB",
			chain:  common.ETHChain,
			asset:  ethUSDT,
			amount: 799_997_967_100,
		},
		{
			txID:   "DC1F405EA046C804B857321504EF1A8EDBEFED942D26F40EF80311DAA943BF11",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 627_955_000,
		},
		{
			txID:   "93FD5B47E74B7C20881464641FF0A753B65A9BB64E70B75C176B353B31B23779",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 997_734_500,
		},
		{
			txID:   "C53DEFFCF8A62C80F0EB3DD0C97E35C6AAE67BD725C8B070A2EAF619EBDCA367",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 124_436_600,
		},
		{
			txID:   "3C1F5BE0E6AD5296A95B539F137EED2BAC649FFF869F7CCD6245A70CA12569BE",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 4_997_734_500,
		},
		{
			txID:   "28D17EC1B66DEABCC9325C532D471D61B9E2276FF8AD6314908783EF50BEBDD0",
			chain:  common.ETHChain,
			asset:  ethWBTC,
			amount: 972,
		},
		{
			txID:   "33BFFE8CABAD8BF30ECDCE39EC6A2FCFB081F85E707B8BCAD8657997C8535FE4",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 203_911_502_000,
		},
		{
			txID:   "46994110DB09621EC5D793EF57C17A00580E4E25F72252B8F6617955CA9C8671",
			chain:  common.ETHChain,
			asset:  ethUSDC,
			amount: 51_517_759_500,
		},
		{
			txID:   "DC8EEB91473665D60E8CE09A7BB65AB1D66026CD90159A5794FCD1FE7DC832C8",
			chain:  common.GAIAChain,
			asset:  common.ATOMAsset,
			amount: 12_599_998_000,
		},
		{
			txID:   "71633631A00B7085E202351DBF3B7AE52A573BAB15FE6F2D8165878A8A7D8883",
			chain:  common.GAIAChain,
			asset:  common.ATOMAsset,
			amount: 249_997_000,
		},
		{
			txID:   "14A6024EC7688F3243937F43E302FB1D934D7B55AA9CDB28D352E118D96969E3",
			chain:  common.GAIAChain,
			asset:  common.ATOMAsset,
			amount: 9_996_000,
		},
		{
			txID:   "4C0FE03225B88FC460118285D43BE60AB903DFDDE95EE0AB12443CE17C35DB75",
			chain:  common.GAIAChain,
			asset:  common.ATOMAsset,
			amount: 19_998_000,
		},
		{
			txID:   "202B832EA7EB06C5BDFAF9459E1ED7C143A67B557FB6DFF43C8956FB030BFAD5",
			chain:  common.GAIAChain,
			asset:  common.ATOMAsset,
			amount: 8_869_997_000,
		},
		{
			txID:   "A509E8F11121122BD4A8DC87103D2BA3141885A4EDD2413A2CBF3FDF6D2CF495",
			chain:  common.LTCChain,
			asset:  common.LTCAsset,
			amount: 6_814_283,
		},
		{
			txID:   "07612B63ACFFDDCBD3B005324BC20F316E527959F74155823DE3B85E5878D1AD",
			chain:  common.LTCChain,
			asset:  common.LTCAsset,
			amount: 6_482_864_307,
		},
		{
			txID:   "B06FFD1F8DC8B3101760F7EDB52C861EDA1877956A6FACDCBCF7910A522BFB11",
			chain:  common.BTCChain,
			asset:  common.BTCAsset,
			amount: 387_417_726,
		},
		// These BTC rows are safe as-is: miner fees reduce the vault change,
		// so the recipient output still delivers the full amount.
		{
			txID:   "FA0FE33222A00020D6B1A4091828518E41CC792AA47F8C2CAE31B8DA4A7249BC",
			chain:  common.BTCChain,
			asset:  common.BTCAsset,
			amount: 2_370,
		},
		{
			txID:   "C36C8291FC97B421B2498DCFD88AAD0979FDBD4941DB43802F90BFDC5E11A77C",
			chain:  common.BTCChain,
			asset:  common.BTCAsset,
			amount: 4_572,
		},
		{
			txID:   "E3097EA59345B8908A91FCA42E567E025515E8143890481BC75F7E85BE8C6315",
			chain:  common.BTCChain,
			asset:  common.BTCAsset,
			amount: 10_169,
		},
	}

	specs := make([]danglingSettlementSpec, 0, len(rows))
	for _, row := range rows {
		txID, err := common.NewTxID(row.txID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse settlement tx id %s: %w", row.txID, err)
		}
		specs = append(specs, danglingSettlementSpec{
			TxID:   txID,
			Chain:  row.chain,
			Asset:  row.asset,
			Amount: cosmos.NewUint(row.amount),
		})
	}

	return specs, nil
}

// adr028WritedownSpec identifies one pool whose BalanceAsset must be written down
// by `deltaAsset` (1e8) to remove the ADR-028 residual "phantom depth" — pool
// depth in excess of the live-backed Asgard vault holdings.
type adr028WritedownSpec struct {
	asset common.Asset
	// deltaAsset is the phantom slice to remove: pool.BalanceAsset − live backing.
	// It is stored as a DELTA rather than an absolute target because the delta is
	// height-invariant (a swap credits the pool and the vault equally, so the gap
	// is a frozen historical amount) while an absolute target goes stale the
	// moment the pool trades. The write target is derived from live depth at
	// execution: target = pool.BalanceAsset − deltaAsset.
	deltaAsset cosmos.Uint
}

// Migrate17to18 completes the ADR-028 reconciliation begun in Migrate15to16 by
// writing down the residual pool "phantom depth". Migrate15to16 cut the
// record/obligation side but left two halves unfinished: pool depth that still
// exceeds live-backed Asgard holdings (the ~-$3.5M DEFICIT, corrected here), and
// the freed L1 asset left physically in Asgard (the ~+$3.1M orphaned-ΔA SURPLUS,
// left in place — excess L1 backing, net-solvent, swept later if ever).
//
// For each deficit pool, write pool.BalanceAsset down to
//
//	target = max(0, asgard − trade.depth − secured.depth − in-flight-stream)
//
// so pool + trade + secured == asgard. trade.depth and secured.depth are 1:1
// redemption claims honored at par and are NOT touched. BalanceRune is reduced by
// the symmetric ratio ΔR = ΔA × BR/BA to hold pool price constant, and the freed
// RUNE is moved Asgard→Reserve in FULL — none burned (all cut RUNE to Reserve),
// matching the seed pools. After all pool writes,
// RUNEPool.ReserveUnits is re-solved so POL providers are held whole (the reserve
// bucket absorbs the depth loss), same as Migrate15to16.
//
// Snapshot basis: THOR height 26,967,482. The per-pool targets are POINT-IN-TIME
// and MUST be re-derived against fresh chain state at the actual upgrade height
// and hardcoded before release, exactly as Migrate15to16's 2026-05-26 snapshot.
func (m Migrator) Migrate17to18(ctx sdk.Context) error {
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	specs, migrationErr := mainnetADR028WritedownSpecs17to18()
	if migrationErr != nil {
		return migrationErr
	}

	// Snapshot POL provider value BEFORE any pool write; held whole by the
	// ReserveUnits re-solve after all writes (reserve absorbs the depth loss).
	runePool, migrationErr := m.mgr.Keeper().GetRUNEPool(ctx)
	if migrationErr != nil {
		return fmt.Errorf("adr028 writedown: fail to get RUNEPool: %w", migrationErr)
	}
	vPre, migrationErr := runePoolValue(ctx, m.mgr)
	if migrationErr != nil {
		return fmt.Errorf("adr028 writedown: fail to compute runePoolValue pre: %w", migrationErr)
	}
	providerValuePre := common.GetSafeShare(runePool.PoolUnits, runePool.TotalUnits(), vPre)

	// ── Pre-flight balance checks — before any mutation ──────────────────────
	// A mid-loop send failure is NOT recoverable partial state: RunMigrations
	// returns the error, the upgrade handler panics, and the block never commits
	// — deterministically on every node, i.e. a chain halt at the upgrade height
	// needing an emergency binary. Verify up front that Asgard can fund the
	// writedown ΔR moves (ΣΔR, summed exactly as the loop below computes/skips it)
	// plus the seed pools' freed RUNE, and that the Reserve can fund the seed draws
	// plus the Secured-Ops coverage, so a shortfall aborts cleanly instead.
	totalDeltaR := cosmos.ZeroUint()
	for _, spec := range specs {
		pool, err := m.mgr.Keeper().GetPool(ctx, spec.asset)
		if err != nil || pool.IsEmpty() || pool.BalanceAsset.LTE(spec.deltaAsset) {
			continue
		}
		dR := common.GetUncappedShare(spec.deltaAsset, pool.BalanceAsset, pool.BalanceRune)
		if dR.IsZero() || dR.GTE(pool.BalanceRune) {
			continue // mirrors the writedown loop's skip below
		}
		totalDeltaR = totalDeltaR.Add(dR)
	}
	// The seed step (seedADR028Pools) also moves RUNE both ways: a seed pool with
	// more RUNE than its $5k target frees the excess Asgard→Reserve; one with less
	// draws the shortfall Reserve→Asgard. Mirror both, so the Asgard guard covers
	// ΣΔR AND the (often larger) seed-freed drains, and the Reserve guard covers
	// the seed draws AND the Secured-Ops coverage.
	seedSpecs, migrationErr := mainnetADR028SeedSpecs17to18()
	if migrationErr != nil {
		return migrationErr
	}
	seedDraw, seedFreed := cosmos.ZeroUint(), cosmos.ZeroUint()
	for _, spec := range seedSpecs {
		pool, err := m.mgr.Keeper().GetPool(ctx, spec.asset)
		if err != nil || pool.IsEmpty() {
			continue
		}
		switch {
		case spec.seedRune.GT(pool.BalanceRune):
			seedDraw = seedDraw.Add(common.SafeSub(spec.seedRune, pool.BalanceRune))
		case pool.BalanceRune.GT(spec.seedRune):
			seedFreed = seedFreed.Add(common.SafeSub(pool.BalanceRune, spec.seedRune))
		}
	}
	asgardNeed := totalDeltaR.Add(seedFreed)
	if bal := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName); bal.LT(asgardNeed) {
		return fmt.Errorf("adr028 writedown: Asgard RUNE %s < ΣΔR+seedFreed %s; aborting before any mutation", bal, asgardNeed)
	}
	reserveNeed := cosmos.NewUint(adr028SecuredOpsCoverageRune).Add(seedDraw)
	if bal := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(reserveNeed) {
		return fmt.Errorf("adr028 writedown: Reserve RUNE %s < coverage+seedDraw %s; aborting before any mutation", bal, reserveNeed)
	}

	// Snapshot pre-mutation CalcUnits safety for every touched pool. After all
	// mutations, the recompute loop checks whether any pool TRANSITIONED from
	// safe (2×BA > synthSupply) to unsafe (2×BA ≤ synthSupply) — a migration-
	// caused CalcUnits blow-up that must abort. Pre-existing unsafe pools are
	// logged and skipped (the migration didn't create or worsen them).
	preMigrationSafe := make(map[string]bool, len(specs)+len(seedSpecs))
	for _, s := range specs {
		pool, err := m.mgr.Keeper().GetPool(ctx, s.asset)
		if err != nil || pool.IsEmpty() {
			continue
		}
		ss := m.mgr.Keeper().GetTotalSupply(ctx, s.asset.GetSyntheticAsset())
		preMigrationSafe[s.asset.String()] = pool.BalanceAsset.MulUint64(2).GT(ss)
	}
	for _, s := range seedSpecs {
		if _, already := preMigrationSafe[s.asset.String()]; already {
			continue // pool appears in both specs — writedown snapshot is canonical
		}
		pool, err := m.mgr.Keeper().GetPool(ctx, s.asset)
		if err != nil || pool.IsEmpty() {
			continue
		}
		ss := m.mgr.Keeper().GetTotalSupply(ctx, s.asset.GetSyntheticAsset())
		preMigrationSafe[s.asset.String()] = pool.BalanceAsset.MulUint64(2).GT(ss)
	}

	for _, spec := range specs {
		pool, err := m.mgr.Keeper().GetPool(ctx, spec.asset)
		if err != nil {
			return fmt.Errorf("adr028 writedown: fail to get pool %s: %w", spec.asset, err)
		}
		if pool.IsEmpty() {
			ctx.Logger().Error("adr028 writedown: empty pool; skipping", "asset", spec.asset)
			continue
		}
		// Skip if the pool already sits at/below target (drifted since snapshot) —
		// a writedown would only apply if depth still exceeds backing.
		// The write target is derived from LIVE depth, not hardcoded: the phantom
		// slice is height-invariant, so removing it from whatever the pool holds at
		// execution keeps the migration correct however long after the snapshot the
		// upgrade fires. A pool that has since shrunk below its own phantom slice
		// contradicts that invariant — skip it loudly rather than zero the pool.
		if pool.BalanceAsset.LTE(spec.deltaAsset) {
			ctx.Logger().Error("adr028 writedown: BalanceAsset <= phantom delta; skipping",
				"asset", spec.asset, "balance_asset", pool.BalanceAsset, "delta", spec.deltaAsset)
			continue
		}
		deltaA := spec.deltaAsset
		target := common.SafeSub(pool.BalanceAsset, deltaA)
		// ΔR = ΔA × BR/BA — symmetric AMM identity, holds pool price constant.
		deltaR := common.GetUncappedShare(deltaA, pool.BalanceAsset, pool.BalanceRune)
		if deltaR.IsZero() {
			continue // dust pool: ΔR rounds to 0, nothing to move
		}
		if deltaR.GTE(pool.BalanceRune) {
			ctx.Logger().Error("adr028 writedown: ΔR >= BalanceRune (would zero or underflow the RUNE side); skipping",
				"asset", spec.asset, "balance_rune", pool.BalanceRune, "delta_r", deltaR)
			continue
		}

		pool.BalanceAsset = target
		pool.BalanceRune = common.SafeSub(pool.BalanceRune, deltaR)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("adr028 writedown: fail to set pool %s: %w", spec.asset, err)
		}

		// Resolve CalcUnits clamp (2×target ≤ synthSupply) by reducing synth
		// supply. Two paths:
		//   UNDERBACKED (named pools with live savers + vault free backing):
		//     step-3 savers haircut — settle savers at real free backing.
		//   All others: BurnEntireSynthSupply.
		// The pool set is explicit because the generic "vault − pool.BA"
		// gate doesn't account for trade/secured claims that reduce free
		// backing; hardcoding avoids burning savers that should be settled.
		synthAsset := spec.asset.GetSyntheticAsset()
		synthSupply := m.mgr.Keeper().GetTotalSupply(ctx, synthAsset)
		if !synthSupply.IsZero() && target.MulUint64(2).LTE(synthSupply) {
			if adr028SaversHaircutPools[spec.asset.String()] {
				freeBacking, err := m.adr028FreeBacking(ctx, spec.asset, target)
				if err != nil {
					return fmt.Errorf("adr028 writedown: %w", err)
				}
				if !freeBacking.IsZero() {
					if err := m.adr028SaversHaircut(ctx, spec.asset, synthAsset, synthSupply, freeBacking); err != nil {
						return fmt.Errorf("adr028 writedown: fail savers haircut for %s: %w", spec.asset, err)
					}
				} else {
					ctx.Logger().Error("adr028 writedown: haircut pool has zero free backing, degrading to full burn",
						"asset", spec.asset)
					burned, err := m.mgr.Keeper().BurnEntireSynthSupply(ctx, synthAsset)
					if err != nil {
						return fmt.Errorf("adr028 writedown: fail to burn synth supply for %s: %w", spec.asset, err)
					}
					ctx.Logger().Info("adr028 writedown: burned entire synth supply (degraded)",
						"asset", spec.asset, "synth_burned", burned)
				}
			} else {
				wasSafe, hasPre := preMigrationSafe[spec.asset.String()]
				if hasPre && !wasSafe {
					ctx.Logger().Info("adr028 writedown: pre-existing CalcUnits clamp, skipping burn",
						"asset", spec.asset, "target", target, "synth_supply", synthSupply)
				} else {
					burned, err := m.mgr.Keeper().BurnEntireSynthSupply(ctx, synthAsset)
					if err != nil {
						return fmt.Errorf("adr028 writedown: fail to burn synth supply for %s: %w", spec.asset, err)
					}
					if adr028ZeroSaversPools[spec.asset.String()] {
						if err := m.adr028ZeroSavers(ctx, synthAsset); err != nil {
							return fmt.Errorf("adr028 writedown: fail to zero savers for %s: %w", spec.asset, err)
						}
					}
					ctx.Logger().Info("adr028 writedown: burned entire synth supply",
						"asset", spec.asset, "synth_burned", burned)
				}
			}
		}

		emitPoolBalanceChangedEvent(ctx, NewPoolMod(spec.asset, deltaR, false, deltaA, false), "adr028_writedown", m.mgr)

		// Dispose the freed RUNE by moving it Asgard→Reserve in FULL — none burned
		// (all cut RUNE goes to Reserve), matching the seed pools. Deliberately NOT
		// splitFreedRune (that half-burns; kept only for the shipped Migrate15to16).
		freedRune := common.NewCoin(common.RuneAsset(), deltaR)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(freedRune)); err != nil {
			return fmt.Errorf("adr028 writedown: fail to move freed RUNE Asgard→Reserve for %s: %w", spec.asset, err)
		}

		ctx.Logger().Info("adr028 writedown: pool depth written down",
			"asset", spec.asset, "delta_asset", deltaA, "delta_rune", deltaR,
			"new_balance_asset", pool.BalanceAsset, "new_balance_rune", pool.BalanceRune)
	}

	// ── Re-seed the four empty-vault pools to $5k per side ───────────────────
	// These are NOT written down to backing. Both sides are cut to a $5k seed so
	// the pool quotes at market price; the displaced RUNE is moved Asgard→Reserve
	// in full (not burned). Real backing / claim settlement is a separate manual
	// step after the upgrade.
	if err := m.seedADR028Pools(ctx); err != nil {
		return err
	}

	// ── Recompute SynthUnits for ALL pools ──────────────────────────────────
	// CalcUnits derives SU = L·S/(2A−S) from the pool's current BalanceAsset.
	// The writedown and seed paths mutated BalanceAsset without recomputing, so
	// persisted SynthUnits are stale. Recompute before the POL re-solve, which
	// reads GetPoolUnits() (= LPUnits + SynthUnits) via polPoolValue.
	//
	// The CalcUnits-clamp guard aborts only on a SAFE→UNSAFE transition that the
	// migration caused: a pool that was 2×BA > S before mutation but 2×BA ≤ S
	// after. Pre-existing clamp pools (already
	// 2×BA ≤ S before the migration) are logged and skipped; their CalcUnits
	// recompute would just re-persist the clamped value they already had.
	allPools, migrationErr := m.mgr.Keeper().GetPools(ctx)
	if migrationErr != nil {
		return fmt.Errorf("adr028 writedown: fail to get pools for SynthUnits recompute: %w", migrationErr)
	}
	for _, pool := range allPools {
		if pool.Asset.IsNative() || pool.BalanceAsset.IsZero() {
			continue
		}
		synthSupply := m.mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		if pool.BalanceAsset.MulUint64(2).LTE(synthSupply) {
			wasSafe, hasSnapshot := preMigrationSafe[pool.Asset.String()]
			if hasSnapshot && wasSafe {
				// Skip rather than abort: user-held synths surviving the haircut
				// can keep the pool clamped even after the burn. An abort here
				// would deterministically halt the chain at the upgrade height.
				ctx.Logger().Error("adr028 writedown: pool crossed safe→clamp (migration-caused); skipping recompute",
					"asset", pool.Asset, "two_ba", pool.BalanceAsset.MulUint64(2), "synth_supply", synthSupply)
			} else {
				ctx.Logger().Error("adr028 writedown: pre-existing CalcUnits clamp (not migration-caused); skipping recompute",
					"asset", pool.Asset, "balance_asset", pool.BalanceAsset, "synth_supply", synthSupply)
			}
			continue
		}
		pool.CalcUnits(synthSupply)
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("adr028 writedown: fail to save recomputed SynthUnits for %s: %w", pool.Asset, err)
		}
	}

	// ── POL provider-whole solve (after all pool writes) ─────────────────────
	// Re-solve RUNEPool.ReserveUnits so provider value is preserved (the reserve
	// bucket absorbs the depth loss). Identical structure to Migrate15to16; a
	// no-op when POL holds no units in the written-down pools.
	vPost, migrationErr := runePoolValue(ctx, m.mgr)
	if migrationErr != nil {
		return fmt.Errorf("adr028 writedown: fail to compute runePoolValue post: %w", migrationErr)
	}
	switch {
	case runePool.PoolUnits.IsZero():
		ctx.Logger().Info("adr028 writedown: PoolUnits is zero; leaving ReserveUnits unchanged",
			"reserve_units", runePool.ReserveUnits)
	case providerValuePre.GT(vPost) || runePool.TotalUnits().IsZero():
		// Skip the re-solve rather than abort: the writedown should still proceed.
		// The accounting drift is safe-direction (CurrentDeposit overstated → POL
		// under-deposits, not over-deposits). An abort here would deterministically
		// halt the chain at the upgrade height.
		ctx.Logger().Error("adr028 writedown: POL re-solve skipped — providerValuePre > vPost (or TotalUnits=0); leaving ReserveUnits unchanged",
			"v_pre", vPre, "v_post", vPost, "provider_value_pre", providerValuePre,
			"reserve_units", runePool.ReserveUnits)
	default:
		// ReserveUnits_post = PoolUnits × vPost / providerValuePre − PoolUnits
		newTotalUnits := common.GetUncappedShare(runePool.PoolUnits, providerValuePre, vPost)
		runePool.ReserveUnits = common.SafeSub(newTotalUnits, runePool.PoolUnits)
	}
	m.mgr.Keeper().SetRUNEPool(ctx, runePool)
	ctx.Logger().Info("adr028 writedown: RUNEPool.ReserveUnits adjusted",
		"v_pre", vPre, "v_post", vPost,
		"provider_value_pre", providerValuePre,
		"new_reserve_units", runePool.ReserveUnits)

	// ── Secured-Ops coverage settlement ──────────────────────────────────────
	// The three empty-vault pools carry trade+secured claims their drained vaults
	// cannot honour. Send the RUNE-equivalent of that uncovered claim value from
	// the Reserve to the TC Secured-Ops account, which settles the claim holders
	// off-chain in kind.
	if err := m.sendADR028SecuredOpsCoverage(ctx); err != nil {
		return err
	}

	return nil
}

// adr028SecuredOpsAddr is the TC Secured-Ops THORChain account that receives the
// RUNE-equivalent coverage for the empty-vault pools' unbacked trade+secured
// claims (ECDSA pubkey 03af714f…3236fbcd). It is deliberately NOT the similarly
// named Secured-Ops vault (03de3ba3…).
const adr028SecuredOpsAddr = "thor1gkvvzfnkyqv8dfhk0593tfhgr03a3ndgjqdalf"

// adr028SecuredOpsCoverageRune is the RUNE-equivalent of the uncovered
// trade+secured claim value across the three empty-vault seed pools, valued at
// the h=26,967,482 snapshot (RUNE $0.405553, ETH $1,802.40):
//
//	BSC.USDC 23,751.67 + BSC.USDT 23,388.57 + BASE.ETH 9.2217 ETH ≈ $63,761
//	=> 157,221.00 RUNE
//
// Like the seed constants this is $-denominated and MUST be re-derived at the
// upgrade height (RUNE and ETH prices drift) and hardcoded before release.
const adr028SecuredOpsCoverageRune = 15722100323851

// sendADR028SecuredOpsCoverage moves the Secured-Ops coverage RUNE from the
// Reserve to the TC Secured-Ops account. Failure aborts the migration — the
// amount and recipient are fixed, pre-verified constants.
func (m Migrator) sendADR028SecuredOpsCoverage(ctx sdk.Context) error {
	to, err := cosmos.AccAddressFromBech32(adr028SecuredOpsAddr)
	if err != nil {
		return fmt.Errorf("adr028 secured-ops: parse address %s: %w", adr028SecuredOpsAddr, err)
	}
	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(adr028SecuredOpsCoverageRune))
	if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, to, common.NewCoins(coin)); err != nil {
		return fmt.Errorf("adr028 secured-ops: send Reserve→%s: %w", adr028SecuredOpsAddr, err)
	}
	ctx.Logger().Info("adr028 secured-ops: coverage sent from Reserve",
		"to", adr028SecuredOpsAddr, "rune", coin.Amount)
	return nil
}

// mainnetADR028WritedownSpecs17to18 returns the per-pool phantom-depth slice to
// remove for the ADR-028 reconciliation.
//
// SNAPSHOT @ THOR height 26,967,482 (reconciliation inspector's full CSV):
//
//	backing = asgard − trade.depth − secured.depth − stream_inflight
//	delta   = pool.BalanceAsset − backing        (both 1e8)
//
// These are DELTAS, not absolute targets, and that distinction is load-bearing.
// The phantom slice is a frozen historical hole: a swap credits the pool and the
// vault equally, so pool.BalanceAsset and backing move in lockstep and their gap
// does not change with trading. An absolute target, by contrast, goes stale the
// moment the pool trades — measured against live chain state 148,076 blocks
// (~10 days) after this snapshot, shipping absolute targets would have
// over-written-down by ~$302k, concentrated in ETH.USDC/USDT/WBTC/DOGE.
//
// Storing the delta and deriving target = pool.BalanceAsset − delta at execution
// makes the writedown independent of how long after the snapshot the upgrade
// fires. Invariance was verified empirically across all 26 pools over those
// 148,076 blocks: 24 bit-identical, AVAX.USDT −41 native, ETH.USDC −2 native.
// Re-derivation at the upgrade height is therefore a sanity check rather than a
// correctness dependency (unlike the seed constants, which remain $-denominated
// at snapshot prices — see mainnetADR028SeedSpecs17to18).
//
// This set is the non-THOR positive-deficit rows ONLY. Deliberately EXCLUDED:
//   - THOR.TCY / THOR.RUJI: native THORChain tokens whose pool.BalanceAsset is
//     backed by bank/module supply, not asgard vault custody (asgard_record=0 is
//     by design). Writing them down would destroy two solvent pools.
//   - Surplus rows (asgard > obligation): AVAX.AVAX, BCH.BCH, BSC.BTCB, BSC.ETH,
//     BTC.BTC, ETH.ETH, LTC.LTC, SOL.SOL, TRON.TRX, TRON.USDT, XRP.XRP — the
//     orphaned-ΔA/float side, left in place (excess L1 backing, net-solvent).
//   - The four SEEDED pools (BSC.USDC, BSC.USDT, BASE.ETH, ETH.THOR) — they are
//     NOT written down to backing; they are re-seeded and RUNE-funded instead.
//     See mainnetADR028SeedSpecs17to18.
//
// 26 rows total, all positive-target. BSC.BUSD (~$1.17) and ETH.XRUNE are real
// but dead/sunset tokens — retained for completeness; drop both if the
// reconciliation excludes dead tokens.
// adr028SaversHaircutPools identifies writedown pools that carry live saver
// positions with positive vault free backing. These pools get the step-3
// savers haircut (settle savers proportionally at free backing) instead of
// BurnEntireSynthSupply. All other clamped pools are burned entirely.
var adr028SaversHaircutPools = map[string]bool{
	"ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48": true,
}

// adr028ZeroSaversPools identifies UNBACKED writedown pools whose savers
// records must be zeroed alongside BurnEntireSynthSupply. The synth burn
// removes the coins but leaves the saver pool and LP records intact —
// without zeroing, savers would hold phantom units against an empty pool.
var adr028ZeroSaversPools = map[string]bool{
	"ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7":  true,
	"AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E": true,
	"BSC.BNB": true,
}

func mainnetADR028WritedownSpecs17to18() ([]adr028WritedownSpec, error) {
	// {asset identifier, writedown delta BalanceAsset (1e8)}. Generated from the
	// inspector CSV (pool_BA(snapshot) − live backing), adjusted upward for the
	// ETH.USDC/ETH.USDT/GAIA.ATOM/ETH.WBTC outbounds that Migrate16to17 requeues
	// at the same upgrade height: those committed coins still sit in vault.coins
	// when the CSV was snapshotted, inflating the apparent backing. Without this
	// adjustment the pools are under-backed once the outbounds settle.
	rows := []struct {
		asset string
		delta uint64
	}{
		{"AVAX.SOL-0XFE6B19286885A4F7F55ADAD09C3CD1F906D2478F", 38893875330},
		{"AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E", 18438759809862},
		{"AVAX.USDT-0X9702230A8EA53601F5CD2DC00FDBC13D4DF4A8C7", 4256523275720},
		{"BASE.USDC-0X833589FCD6EDB6E08F4C7C32D4F71B54BDA02913", 685491225982},
		{"BASE.VVV-0XACFE6019ED1A7DC6F7B508C02D1B04EC88CC21BF", 2957939847},
		{"BSC.BNB", 4856114802},
		{"BSC.BUSD-0XE9E7CEA3DEDCA5984780BAFC599BD69ADD087D56", 123066821},
		{"BSC.TWT-0X4B0F1812E5DF2A09796481FF14017E6005508003", 3298025883526},
		{"DOGE.DOGE", 188322561538022},
		{"ETH.AAVE-0X7FC66500C84A76AD7E9C93437BFC5AC33E2DDAE9", 2795812734},
		{"ETH.DAI-0X6B175474E89094C44DA98B954EEDEAC495271D0F", 245264736890},
		{"ETH.DPI-0X1494CA1F11D487C2BBE4543E90080AEBA4BA3C2B", 1143217176},
		{"ETH.FOX-0XC770EEFAD204B5180DF6A14EE197D99D808EE52D", 47623739145492},
		{"ETH.GUSD-0X056FD409E1D7A124BD7017459DFEA2F387B6D5CD", 6107452410472},
		{"ETH.LINK-0X514910771AF9CA656AF840DFF83E8264ECF986CA", 104374231717},
		{"ETH.LUSD-0X5F98805A4E8BE255A32880FDEC7F6728C6568BA0", 6413841177179},
		{"ETH.SNX-0XC011A73EE8576FB46F5E1C5751CA3B9FE0AF2A6F", 99792896784},
		{"ETH.TGT-0X108A850856DB3F85D0269A2693D896B394C80325", 77950001213139},
		{"ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 106612841513705}, // 101889575459905 + 4723266053800 (16to17 requeue)
		{"ETH.USDP-0X8E870D67F660D95D5BE530380D0EC0BD388289E1", 3876245256026},
		{"ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", 98310077343961}, // 97010081642161 + 1299995701800 (16to17 requeue)
		{"ETH.VTHOR-0X815C23ECA83261B6EC689B60CC4A58B54BC24D8D", 3438326325955},
		{"ETH.WBTC-0X2260FAC5E5542A773AA44FBCFEDF7C193BC2C599", 520601044}, // 520600072 + 972 (16to17 requeue)
		{"ETH.XRUNE-0X69FA0FEE221AD11012BAB0FDB45D444D3D2CE71C", 7326188839277660},
		{"ETH.YFI-0X0BC529C00C6401AEF6D220BE8C6EA1667F6AD93E", 105337362},
		{"GAIA.ATOM", 1679285461912}, // 1657535475912 + 21749986000 (16to17 requeue)
	}

	specs := make([]adr028WritedownSpec, 0, len(rows))
	for _, r := range rows {
		asset, err := common.NewAsset(r.asset)
		if err != nil {
			return nil, fmt.Errorf("adr028 writedown: parse asset %s: %w", r.asset, err)
		}
		specs = append(specs, adr028WritedownSpec{
			asset:      asset,
			deltaAsset: cosmos.NewUint(r.delta),
		})
	}

	return specs, nil
}

// seedADR028Pools cuts the four re-seeded pools to a $5k-per-side seed. Per pool:
//
//	pool.BalanceAsset = seedAsset   ($5k of asset)
//	pool.BalanceRune  = seedRune    ($5k of RUNE)
//
// This is a symmetric double-sided haircut: both sides are set to $5k so the
// pool quotes at market price (a balanced seed is un-skewed — no arbitrage
// window). The RUNE displaced by collapsing the old depth to the seed is moved
// Asgard→Reserve in FULL — none of it is burned (unlike the writedown path's
// splitFreedRune), so the freed RUNE is retained by the protocol. If a pool's
// RUNE side is somehow already below the seed the shortfall is drawn
// Reserve→Asgard, but for the frozen empty-vault pools that branch is not
// expected to fire. Failures on one pool abort the migration — these are fixed,
// pre-verified rows, so a mismatch means the snapshot is stale.
func (m Migrator) seedADR028Pools(ctx sdk.Context) error {
	specs, err := mainnetADR028SeedSpecs17to18()
	if err != nil {
		return err
	}

	for _, spec := range specs {
		pool, err := m.mgr.Keeper().GetPool(ctx, spec.asset)
		if err != nil {
			return fmt.Errorf("adr028 seed: fail to get pool %s: %w", spec.asset, err)
		}
		if pool.IsEmpty() {
			ctx.Logger().Error("adr028 seed: empty pool; skipping", "asset", spec.asset)
			continue
		}

		oldAsset, oldRune := pool.BalanceAsset, pool.BalanceRune

		// Reconcile the pool's RUNE side with Asgard: move the displaced RUNE to
		// the Reserve in full (no burn), or draw the shortfall from the Reserve.
		switch {
		case oldRune.GT(spec.seedRune):
			freed := common.NewCoin(common.RuneNative, common.SafeSub(oldRune, spec.seedRune))
			if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(freed)); err != nil {
				return fmt.Errorf("adr028 seed: fail to move freed RUNE Asgard→Reserve for %s: %w", spec.asset, err)
			}
		case spec.seedRune.GT(oldRune):
			fund := common.NewCoin(common.RuneNative, common.SafeSub(spec.seedRune, oldRune))
			if err := m.mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(fund)); err != nil {
				return fmt.Errorf("adr028 seed: fail to fund %s Reserve→Asgard: %w", spec.asset, err)
			}
		}

		// Burn the pool's entire synth supply if it would trip the CalcUnits
		// blow-up (2×seedBA ≤ synthSupply). These are frozen ThorFi-era residuals
		// that treasury already settled; zeroing writes off a settled liability.
		synthAsset := spec.asset.GetSyntheticAsset()
		synthSupply := m.mgr.Keeper().GetTotalSupply(ctx, synthAsset)
		if !synthSupply.IsZero() && spec.seedAsset.MulUint64(2).LTE(synthSupply) {
			burned, err := m.mgr.Keeper().BurnEntireSynthSupply(ctx, synthAsset)
			if err != nil {
				return fmt.Errorf("adr028 seed: fail to burn synth supply for %s: %w", spec.asset, err)
			}
			ctx.Logger().Info("adr028 seed: burned residual synth supply",
				"asset", spec.asset, "synth_burned", burned)
		}

		pool.BalanceAsset = spec.seedAsset
		pool.BalanceRune = spec.seedRune
		if err := m.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return fmt.Errorf("adr028 seed: fail to set pool %s: %w", spec.asset, err)
		}

		runeAmt := common.SafeSub(oldRune, spec.seedRune)
		runeAdd := spec.seedRune.GT(oldRune)
		if runeAdd {
			runeAmt = common.SafeSub(spec.seedRune, oldRune)
		}
		assetAmt := common.SafeSub(oldAsset, spec.seedAsset)
		assetAdd := spec.seedAsset.GT(oldAsset)
		if assetAdd {
			assetAmt = common.SafeSub(spec.seedAsset, oldAsset)
		}
		emitPoolBalanceChangedEvent(ctx, NewPoolMod(spec.asset, runeAmt, runeAdd, assetAmt, assetAdd), "adr028_seed", m.mgr)

		ctx.Logger().Info("adr028 seed: pool re-seeded to $5k per side",
			"asset", spec.asset,
			"old_balance_asset", oldAsset, "new_balance_asset", pool.BalanceAsset,
			"old_balance_rune", oldRune, "new_balance_rune", pool.BalanceRune)
	}

	return nil
}

// adr028SeedSpec re-seeds one pool to a fixed $5k-per-side size instead of
// writing it down to live backing.
type adr028SeedSpec struct {
	asset     common.Asset
	seedAsset cosmos.Uint // pool.BalanceAsset is set to exactly this ($5k of asset)
	seedRune  cosmos.Uint // pool.BalanceRune is set to exactly this ($5k of RUNE)
}

// mainnetADR028SeedSpecs17to18 returns the $5k-per-side seed table for the four
// pools that are re-seeded rather than written down to live backing.
//
// SET SELECTION — three of the four are the complete empty-vault set, found by
// scanning all 30 deficit pools at the snapshot height for
//
//	backing = asgard − trade.depth − secured.depth − stream_inflight < 0
//
// i.e. the asgard vault cannot cover even the 1:1 trade+secured redemption
// claims. That returns exactly BSC.USDC, BSC.USDT and BASE.ETH — no other pool
// qualifies (surplus pools cannot by definition; THOR.TCY/RUJI are bank-backed).
// ETH.THOR does NOT meet this criterion — its vault holds 4,168,967 THOR, 748%
// of its claims, and its backing is positive — it is included by explicit
// decision as the exploit-origin token, not by the scan.
//
// SNAPSHOT @ THOR height 26,967,482, priced at that height: RUNE $0.405553,
// ETH $1,802.40 (ETH.ETH pool spot), THOR $0.033671 (ETH.THOR pool spot),
// USDC/USDT $1.00. Unlike the writedown deltas, these seed amounts are absolute
// $-denominated values, so they MUST be re-derived at the actual upgrade height
// (RUNE and asset prices drift) and hardcoded before release. They are hardcoded
// rather than computed from live pool state on purpose — a price lookup inside a
// migration is non-deterministic across nodes and would fork the chain.
//
// NOTE: the seeded pools quote against vaults that are physically drained
// (BSC.USDC holds 7.61 USDC, BSC.USDT 1.51, BASE.ETH 0.04). The seed is balanced
// $5k/$5k so it quotes at market price; real backing and any claim settlement
// are handled by a separate manual step after the upgrade.
func mainnetADR028SeedSpecs17to18() ([]adr028SeedSpec, error) {
	// $5k RUNE at the snapshot price, identical for all four pools.
	const seedRune1e8 = 1232884481190

	// {asset, $5k of asset (1e8)}.
	rows := []struct {
		asset     string
		seedAsset uint64
	}{
		{"BASE.ETH", 277408024},
		{"BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D", 500000000000},
		{"BSC.USDT-0X55D398326F99059FF775485246999027B3197955", 500000000000},
		{"ETH.THOR-0XA5F2211B9B8170F694421F2046281775E8468044", 14849664450329},
	}

	specs := make([]adr028SeedSpec, 0, len(rows))
	for _, r := range rows {
		asset, err := common.NewAsset(r.asset)
		if err != nil {
			return nil, fmt.Errorf("adr028 seed: parse asset %s: %w", r.asset, err)
		}
		specs = append(specs, adr028SeedSpec{
			asset:     asset,
			seedAsset: cosmos.NewUint(r.seedAsset),
			seedRune:  cosmos.NewUint(seedRune1e8),
		})
	}

	return specs, nil
}

// adr028FreeBacking returns the genuinely unencumbered vault float for asset
// after the writedown: vault coins − post-writedown pool.BA − trade depth −
// secured depth. Trade and secured balances are backed by the same Asgard
// vaults and must be excluded so savers are not settled against collateral
// already claimed by other account types.
func (m *Migrator) adr028FreeBacking(ctx cosmos.Context, asset common.Asset, target cosmos.Uint) (cosmos.Uint, error) {
	vaultCoins := cosmos.ZeroUint()
	for _, status := range []VaultStatus{ActiveVault, RetiringVault} {
		vaults, err := m.mgr.Keeper().GetAsgardVaultsByStatus(ctx, status)
		if err != nil {
			return cosmos.ZeroUint(), fmt.Errorf("fail to get %s vaults for %s: %w", status, asset, err)
		}
		for _, v := range vaults {
			vaultCoins = vaultCoins.Add(v.GetCoin(asset).Amount)
		}
	}

	claims := target
	tu, err := m.mgr.Keeper().GetTradeUnit(ctx, asset.GetTradeAsset())
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get trade unit for %s: %w", asset, err)
	}
	claims = claims.Add(tu.Depth)
	sa, err := m.mgr.Keeper().GetSecuredAsset(ctx, asset.GetSecuredAsset())
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get secured asset for %s: %w", asset, err)
	}
	claims = claims.Add(sa.Depth)

	return common.SafeSub(vaultCoins, claims), nil
}

// adr028SaversHaircut settles saver positions at the pool's real free backing
// instead of burning their entire synth supply. Mirrors the Migrate15to16
// step-3 savers haircut pattern: iterates saver LPs, reduces units
// proportionally, reduces the synth pool depth, then burns the excess synth
// from Asgard.
func (m *Migrator) adr028SaversHaircut(
	ctx cosmos.Context,
	asset, synthAsset common.Asset,
	synthSupply, freeBacking cosmos.Uint,
) error {
	saverPool, err := m.mgr.Keeper().GetPool(ctx, synthAsset)
	if err != nil {
		return fmt.Errorf("fail to get saver pool %s: %w", synthAsset, err)
	}

	// Determine how much of the savers' depth to keep (capped at freeBacking).
	saverKeep := freeBacking
	if saverPool.BalanceAsset.LT(freeBacking) {
		saverKeep = saverPool.BalanceAsset
	}
	saverBurnAmt := common.SafeSub(saverPool.BalanceAsset, saverKeep)

	var saverCount int
	if !saverBurnAmt.IsZero() {
		saverLPs := func() []LiquidityProvider {
			iter := m.mgr.Keeper().GetLiquidityProviderIterator(ctx, synthAsset)
			defer iter.Close()
			var lps []LiquidityProvider
			for ; iter.Valid(); iter.Next() {
				var lp LiquidityProvider
				m.mgr.Keeper().Cdc().MustUnmarshal(iter.Value(), &lp)
				lps = append(lps, lp)
			}
			return lps
		}()
		saverCount = len(saverLPs)

		poolAssetPre := saverPool.BalanceAsset
		sumLPDeductions := cosmos.ZeroUint()
		for _, lp := range saverLPs {
			deduct := lp.Units.Mul(saverBurnAmt).Quo(poolAssetPre)
			lp.Units = common.SafeSub(lp.Units, deduct)
			m.mgr.Keeper().SetLiquidityProvider(ctx, lp)
			sumLPDeductions = sumLPDeductions.Add(deduct)
		}
		saverPool.LPUnits = common.SafeSub(saverPool.LPUnits, sumLPDeductions)
		saverPool.BalanceAsset = common.SafeSub(saverPool.BalanceAsset, saverBurnAmt)
		if err := m.mgr.Keeper().SetPool(ctx, saverPool); err != nil {
			return fmt.Errorf("fail to save saver pool %s: %w", synthAsset, err)
		}
	}

	// Burn synths in two passes so Asgard retains exactly saverKeep coins
	// (matching the post-haircut saverPool.BalanceAsset). Reserve-held synths
	// (ThorFi-era residuals) are burned separately. User-held synths survive
	// — they're individual bank balances we can't burn.
	totalBurned := cosmos.ZeroUint()

	// Pass 1: Asgard — burn everything above saverKeep.
	asgardSynthBal := m.mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synthAsset.Native())
	asgardBurn := common.SafeSub(asgardSynthBal, saverKeep)
	if !asgardBurn.IsZero() {
		coin := common.NewCoin(synthAsset, asgardBurn)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(coin)); err != nil {
			return fmt.Errorf("fail to move synths from Asgard for %s: %w", synthAsset, err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, coin); err != nil {
			return fmt.Errorf("fail to burn synths from Asgard for %s: %w", synthAsset, err)
		}
		totalBurned = totalBurned.Add(asgardBurn)
	}

	// Pass 2: Reserve — burn any synths held by the Reserve module.
	reserveSynthBal := m.mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, synthAsset.Native())
	if !reserveSynthBal.IsZero() {
		coin := common.NewCoin(synthAsset, reserveSynthBal)
		if err := m.mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, ModuleName, common.NewCoins(coin)); err != nil {
			return fmt.Errorf("fail to move synths from Reserve for %s: %w", synthAsset, err)
		}
		if err := m.mgr.Keeper().BurnFromModule(ctx, ModuleName, coin); err != nil {
			return fmt.Errorf("fail to burn synths from Reserve for %s: %w", synthAsset, err)
		}
		totalBurned = totalBurned.Add(reserveSynthBal)
	}

	if !totalBurned.IsZero() {
		mod := NewPoolMod(synthAsset, cosmos.ZeroUint(), false, saverBurnAmt, false)
		emitPoolBalanceChangedEvent(ctx, mod, "adr028_savers_haircut", m.mgr)
		burnEvt := NewEventMintBurn(BurnSupplyType, synthAsset.Native(), totalBurned, "adr028_savers_haircut")
		if err := m.mgr.EventMgr().EmitEvent(ctx, burnEvt); err != nil {
			ctx.Logger().Error("adr028 savers haircut: fail to emit burn event", "error", err, "asset", synthAsset)
		}
	}

	userHeld := common.SafeSub(synthSupply, asgardSynthBal.Add(reserveSynthBal))
	ctx.Logger().Info("adr028 writedown: UNDERBACKED — savers settled at free backing",
		"asset", asset, "saver_count", saverCount, "saver_burn_amt", saverBurnAmt,
		"asgard_burn", asgardBurn, "reserve_burn", reserveSynthBal,
		"user_held_surviving", userHeld, "free_backing", freeBacking)
	return nil
}

// adr028ZeroSavers zeros all saver LP records and the saver pool for an
// UNBACKED pool whose synth supply was just burned. Without this, the saver
// pool and LP units would reference a synth that no longer exists.
func (m *Migrator) adr028ZeroSavers(ctx cosmos.Context, synthAsset common.Asset) error {
	saverPool, err := m.mgr.Keeper().GetPool(ctx, synthAsset)
	if err != nil {
		return fmt.Errorf("fail to get saver pool %s: %w", synthAsset, err)
	}
	if saverPool.BalanceAsset.IsZero() && saverPool.LPUnits.IsZero() {
		return nil
	}

	saverLPs := func() []LiquidityProvider {
		iter := m.mgr.Keeper().GetLiquidityProviderIterator(ctx, synthAsset)
		defer iter.Close()
		var lps []LiquidityProvider
		for ; iter.Valid(); iter.Next() {
			var lp LiquidityProvider
			m.mgr.Keeper().Cdc().MustUnmarshal(iter.Value(), &lp)
			lps = append(lps, lp)
		}
		return lps
	}()

	oldBA := saverPool.BalanceAsset
	oldLPUnits := saverPool.LPUnits

	for _, lp := range saverLPs {
		lp.Units = cosmos.ZeroUint()
		m.mgr.Keeper().SetLiquidityProvider(ctx, lp)
	}

	saverPool.LPUnits = cosmos.ZeroUint()
	saverPool.BalanceAsset = cosmos.ZeroUint()
	if err := m.mgr.Keeper().SetPool(ctx, saverPool); err != nil {
		return fmt.Errorf("fail to save zeroed saver pool %s: %w", synthAsset, err)
	}

	ctx.Logger().Info("adr028 writedown: zeroed saver records for UNBACKED pool",
		"asset", synthAsset, "saver_count", len(saverLPs),
		"old_balance_asset", oldBA, "old_lp_units", oldLPUnits)
	return nil
}

// mainnetDonationSpecComplete must be set to true only once the pinned amounts
// in the 19->20 steps have been re-pinned at the release cut. Until then
// Migrate19to20 logs and SKIPS (returns nil): the module version still advances
// to 20, so a release shipped with this false drops the migration permanently
// (the slash refund, donations, ETH.THOR restore, synth retirement and the
// h28181638 ZEC slash refund would then need a new 20->21 migration).
const mainnetDonationSpecComplete = false

// mainnetDonationSpec19to20 is the 3.21.0 donation spec.
func mainnetDonationSpec19to20() (donationMigrationSpec, error) {
	// Three LP positions keyed by the Secured Ops RUNE address. Each is pinned by its
	// EXACT unit count (price-independent): applyDonationMigration aborts if the live units
	// differ at all. Any pre-upgrade add OR withdrawal on one of these records therefore
	// requires re-pinning its Units (and the treasury Amount) at the cut — the units are NOT
	// donated "as they stand". Values read from live mainnet (h28,216,334; unchanged since
	// the adds at h28,021,7xx, no withdrawals). RuneDepositValue/AssetDepositValue are 0 on
	// the two BSC records (the pools were Staged at add, so deposit values were never
	// written), which is why units — not deposit value — are the drift signal. Re-pin at the cut.
	const securedOps = "thor1gkvvzfnkyqv8dfhk0593tfhgr03a3ndgjqdalf"
	pinnedUnits := map[string]uint64{
		"BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D": 38747498669916,
		"BSC.USDT-0X55D398326F99059FF775485246999027B3197955": 9273374739774,
		"BASE.ETH": 12450028028812,
	}
	donations := make([]lpDonation, 0, 3)
	for _, pool := range []string{
		"BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D",
		"BSC.USDT-0X55D398326F99059FF775485246999027B3197955",
		"BASE.ETH",
	} {
		asset, err := common.NewAsset(pool)
		if err != nil {
			return donationMigrationSpec{}, err
		}
		donations = append(donations, lpDonation{Asset: asset, Address: common.Address(securedOps), Units: cosmos.NewUint(pinnedUnits[pool])})
	}
	// The Reserve repays New Dawn Treasury the RUNE value of these donated positions
	// (BSC.USDC/USDT + BASE.ETH): pinned 247,622.29462483 RUNE. The binding is one-sided
	// (see effectivePayment): the Reserve pays min(pinned, live value), so it never over-pays
	// and a market price move of the donated assets no longer aborts the step. Position drift
	// is caught by the per-record Units pin above. Re-pin the Amount at the cut with the
	// current LP+synth unit formula. ETH.THOR is donated AND depth-restored in its own step
	// (see runEthThorRestore19to20); its RUNE capital is repaid there.
	return donationMigrationSpec{
		Donations: donations,
		Payments: []reservePayment{{
			To:      "thor1lhufh0mwasa0lk9udppdegmvnkgqt08f0m9p5g",
			Amount:  cosmos.NewUint(24_762_229_462_483),
			Reason:  "repay New Dawn Treasury for donated LP positions",
			ValueOf: donations,
		}},
		Pairings: nil,
	}, nil
}

// runSlashRefund19to20 reverses the rzxslmgc bond slash (block 27999003) and
// refunds each bond provider from the Reserve, following the 3.18 Migrate14to15
// (DOGE) model. It applies atomically or not at all: if the BTC pool or Reserve
// no longer matches the pinned figures, it skips and logs rather than halting
// the upgrade.
func runSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) {
	cacheCtx, commit := ctx.CacheContext()
	if err := applySlashRefund19to20(cacheCtx, mgr); err != nil {
		ctx.Logger().Error("slash refund skipped: state does not match the pinned figures", "error", err)
		return
	}
	commit()
}

// applySlashRefund19to20 reverses the BTC pool slash, moves the freed RUNE
// Asgard->Reserve, and refunds the 218 bond providers. Pinned from slash block
// 27999003 (pre 27999002): BTC pool -0.53258717 asset / +56,773.63556220 RUNE;
// refund total 113,547.27112439 RUNE.
func applySlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()
	const (
		// +0.53257717 BTC back into the pool: the outbound COIN ONLY, NOT the 1,000-sat gas.
		// The rzxslmgc slash was a re-observed REAL spend (BTC tx 8FC9BAB735B9…1DB8, confirmed
		// on-chain: recipient bc1qd9sny… received 53,257,717 sats, miner fee 1,000 sats), not a
		// phantom — so the 1,000-sat fee was genuinely consumed. The slash debited the pool the
		// full 53,258,717 (send + fee); on a normal matched outbound the pool bears the gas too,
		// so leaving the 1,000 gas debited keeps pool depth == vault backing (ZEC/TRON parity).
		btcPoolAssetCredit = uint64(53_257_717)        // +0.53257717 BTC (send only; 1,000-sat gas left debited)
		btcPoolRuneDebit   = uint64(5_677_363_556_220) // -56,773.63556220 RUNE (reverse the slash credit)
		asgardToReserve    = uint64(5_677_363_556_220) // move the freed RUNE Asgard->Reserve for the refund
	)
	pool, err := k.GetPool(ctx, common.BTCAsset)
	if err != nil {
		return fmt.Errorf("get BTC pool: %w", err)
	}
	if pool.IsEmpty() {
		return fmt.Errorf("BTC pool is empty")
	}
	// Hold the slash reversal to the same bar as every other asset credit in this
	// migration (ETH.THOR surplus, the Pairings branch): credit the BTC back into
	// the pool only if the active vaults demonstrably hold it free of every
	// liability. The slashed BTC was judged lost, and the rzxslmgc replay that
	// caused the slash also produced dropped outbounds now paid by hand — so the
	// 0.53 BTC is not guaranteed back in an active vault. If it is not unaccounted
	// (or solvency cannot be computed, e.g. mid-churn), skip for a re-pin rather
	// than claim BTC the vaults do not hold while the Reserve still pays 218 refunds.
	freeBTC, err := unaccountedVaultAsset19to20(ctx, mgr, common.BTCAsset)
	if err != nil {
		return fmt.Errorf("btc solvency: %w", err)
	}
	if freeBTC.LT(cosmos.NewUint(btcPoolAssetCredit)) {
		return fmt.Errorf("unaccounted BTC %s below slash credit %d; vaults do not hold the reversed BTC", freeBTC, btcPoolAssetCredit)
	}
	runeDebit := cosmos.NewUint(btcPoolRuneDebit)
	if pool.BalanceRune.LT(runeDebit) {
		return fmt.Errorf("BTC pool BalanceRune %s below slash credit %d; pool drifted", pool.BalanceRune, btcPoolRuneDebit)
	}
	pool.BalanceAsset = pool.BalanceAsset.Add(cosmos.NewUint(btcPoolAssetCredit))
	pool.BalanceRune = pool.BalanceRune.Sub(runeDebit)
	if err := k.SetPool(ctx, pool); err != nil {
		return fmt.Errorf("set BTC pool: %w", err)
	}
	poolSlashAmt := []PoolAmt{
		{Asset: common.BTCAsset, Amount: int64(btcPoolAssetCredit)},
		{Asset: common.RuneAsset(), Amount: -int64(btcPoolRuneDebit)},
	}
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(common.BTCAsset, poolSlashAmt)); err != nil {
		ctx.Logger().Error("emit slash reversal event", "error", err)
	}
	asgardCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(asgardToReserve))
	if err := k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(asgardCoin)); err != nil {
		return fmt.Errorf("asgard->reserve: %w", err)
	}
	total := cosmos.NewUint(mainnetSlashRefunds19to20Total)
	// The rows actually paid must sum to the pinned total that the Reserve gate + the
	// Asgard->Reserve top-up are sized against; otherwise a re-pin that edits the 218-row
	// table without the constant could draw more RUNE than was funded. Mirror the ZEC/TRON
	// siblings' runtime sum check (the mainnet-tagged sum test never runs under mocknet CI).
	rowSum := cosmos.ZeroUint()
	for _, r := range mainnetSlashRefunds19to20 {
		rowSum = rowSum.Add(cosmos.NewUint(r.amount))
	}
	if !rowSum.Equal(total) {
		return fmt.Errorf("slash-refund row sum %s != pinned total %s (re-pin)", rowSum, total)
	}
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(total) {
		return fmt.Errorf("reserve %s below slash refund total %s", bal, total)
	}
	// Atomic (per the doc): any bad address or failed send returns an error so
	// the caller's cache context discards the whole step — never a partial
	// refund set.
	for _, r := range mainnetSlashRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		if err != nil {
			return fmt.Errorf("bad slash-refund address %s: %w", r.address, err)
		}
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(r.amount)))
		if err := k.SendFromModuleToAccount(ctx, ReserveName, to, coins); err != nil {
			return fmt.Errorf("slash refund send to %s (%d) failed: %w", r.address, r.amount, err)
		}
	}
	return nil
}

// --- Step 5: erroneous ZEC slash refund (issue 3393) ---------------------------
//
// At block 28181638 the signing members of active vault gqr5qzgg were slashed for a
// "missing tx out" on ZEC outbound 9218AB74. The outbound was in fact paid on-chain;
// it was stored with a tex1-encoded recipient and observed as the equivalent t1
// address, so it failed the txout match and tripped the unauthorized-outbound slash.
// Following the precedent of Migrate5to6/12to13/14to15 and the step-1 BTC slash
// refund above, the remediation reverses the pool half and pays the affected bond
// providers liquid RUNE from the Reserve (not a bond restore): the slash reduced each
// node's aggregate bond, so each provider's loss is its proportional share of its
// node's bond_cost. Pinned from the h28181638 block events; re-pin from live at the cut.
const (
	zecSlashPoolAssetCredit = uint64(3_909_313)      // +0.03909313 ZEC back into the pool: the outbound COIN only, NOT the 25,000-zat gas. The slash debited the pool coin+gas (3,934,313); on a normal matched outbound the pool bears the gas too (debited via GasMgr), so leaving the 25,000 gas debited keeps pool depth == vault backing. (We omit the ~0.0004 RUNE gas reimbursement the normal path pays the pool from the Reserve — immaterial.)
	zecSlashPoolRuneDebit   = uint64(6_996_306_929)  // -69.96306929 RUNE (reverse the slash's pool credit)
	zecSlashAsgardToReserve = uint64(6_996_306_929)  // move the freed pool-reimburse RUNE Asgard->Reserve to fund the refund (= the pool RUNE debit; mirrors step 1 / !4798)
	zecSlashBondRefundTotal = uint64(13_992_613_798) // sum of the per-provider refunds (= the 18 bond_cost slashes 13,992,613,858 less 60 floor-dust across 98 providers)
	// The parked outbound is located by in_hash + recipient across the WHOLE outbound
	// queue, NOT a fixed height/vault: signing is halted, so LackSigning keeps
	// rescheduling it to new heights and vaults. (It was at h28181627/gqr5qzgg when
	// pinned; already rescheduled since.) Re-verify the in_hash at the cut.
	zecSlashInHash    = "9218AB740258CB52897D95C19A0F27D2F3BB37748652B61DF129B2E5D89B5240"
	zecSlashToAddress = "tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k"
)

// zecSlashBondRefunds19to20 — the 98 bond providers of the 18 slashed members, each
// paid its share (provider bond / node total bond at h28181637 * node bond_cost,
// floored) of its node's slash as liquid RUNE. Sum == zecSlashBondRefundTotal.
var zecSlashBondRefunds19to20 = []struct {
	address string
	amount  uint64
}{
	{"thor1n9gcud325e25ywe9sycwwh6pqgalq03e5mj7dg", 9266},
	{"thor1qq3k97p98edy0mpx9vlareaneg2tfnqqs28kam", 772475480},
	{"thor10ugnd7vk29g887ymycw9wu3m242fp84u35803v", 884679774},
	{"thor1zmp5dkczl96tqlj0d5qaptyg4frqt5rqt3dpwz", 8300},
	{"thor1s7w78use8jq3eeyjlhpx4v7kkgc5xm7feqc294", 908523151},
	{"thor1upuw8yvg96uf4nd6jfuq9s25284dzr45kyzarc", 933},
	{"thor1h4rdr7y2mxr9f7mrnl40kx7s8wjx99kk0nfu0r", 108128612},
	{"thor1f8zgw59dt9tzhyu620fpdgs5s7hte7vy3lefqr", 5357448},
	{"thor12pvaukh88f4fjxx4q76dzsamnpuk3pnwus7wgd", 8883465},
	{"thor1q0zjmlacrm44h0ru5t8t0jn2lm7vfy4x6fznem", 34064197},
	{"thor16wjfedsp5tnqqaw9nd465nmyfgwjml68nr7eqv", 6946046},
	{"thor1c3mxxtjksx8l7c3sjuzjelsdw34qsfj5vnzcp9", 70942041},
	{"thor120vexx8hq06d5nc38e5up9m92jc6hflaum6dfu", 88433755},
	{"thor1rl8xfsq9p22tkm539r3394xsyuzjd579xmuy7h", 212378724},
	{"thor12swpcch3nuc8g3mz00v57fd8ys2uph6s0lzvz2", 107688703},
	{"thor1twp6pat08q8ye3j7cyzmz729mlcmjmcxs4quw8", 10376281},
	{"thor1chfgr7hw88thn8s9sj59260mmjwpl65t0jxtd7", 12649181},
	{"thor1smwz0hxflsx2haqs7fqyjpdpg94d4ml2c7rs70", 115780697},
	{"thor1flrs4nkhjhg0yz4hsqmwfn7tm9zuj8qa2z2jcq", 11189457},
	{"thor1d2pe9ep5d3ectgqdjjmhv7ne95hrhy8nv9jhn9", 5189001},
	{"thor13nsa9gc8svlnqpxdns64dwq5zlvhhhwq8j3cus", 9552859},
	{"thor1c20a2h0c66yrefw7dx90dfz8k4hanlylshh4pw", 25075180},
	{"thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 427366674},
	{"thor1thc237kme006k5qey0mys07x855atz0690hrh7", 31061733},
	{"thor14x9q0dvrelvl0ulneq4aea2sl7yp7v7vvgg77w", 28832235},
	{"thor17fvsxqvz4u2wxw3rarayej58c84zch4aym2fkn", 20571076},
	{"thor145j86em232hwuh7h483eqe5shamnwp2wwqsg5j", 420280663},
	{"thor1m24nuaxr5g42qec3mc73ucdk7zwf9qsrmkjhx6", 24771522},
	{"thor17xn4e53auf4xla7g84ln2f4vjv2ks30fz6s7sa", 17692},
	{"thor1qjv2qgc8wj5kz5mpdls8pe7tchnl5q44lky3sw", 65648552},
	{"thor1tc9sydznys0nu0tp5x5glf54cmc7zsj80eam9m", 91322873},
	{"thor13g7v9wu2sehp3ec8cl7mzds4dydzq7qvypmxzt", 886111},
	{"thor1f4xsyqwn3e0k8kca94pd090k3gxpushzua2mtk", 8776},
	{"thor1ewpf8uwamtrndrvhtrn90a2rmhgw0lquw90ewl", 86532291},
	{"thor1c5q3j23rvjjwkududg9ah2krrjt4emtzzvwj0m", 16809238},
	{"thor1uzq9cvjrpvmky2d04ku3xplpz9p276904g3rhc", 106163664},
	{"thor17vngul7c990n0umzj8qjjf5exng6xxpte0pqx8", 13003807},
	{"thor12p854ms0fatcmpz5ht5pmkntlvvry3z97mz6d9", 48627879},
	{"thor1ayysyz3rz5stve7mfvcslvaek76upwet0vkgj9", 12021068},
	{"thor1s9hht2uwpq3ucpf05ed5aqlf9rfu5y99w72dhq", 14916005},
	{"thor1tvzrpkh7hy2avkd6rw9nk07f4qwwyftv4l8gkf", 940433},
	{"thor1224jdtz8fvq6nt65g8ff23nslzpv372q4ghxff", 881122963},
	{"thor1x0fjxheq7tmlz38plstwn55tptwh9gwa7msfsf", 11951515},
	{"thor1ys4eqwefvhj0n5ycs83gjuvnjup3vwscrm5u7l", 21259488},
	{"thor1phv3czcucg4wz3au9hhva2psmvsx9yjpnkkmgx", 8430139},
	{"thor1hftu6pxar7z52257vztmt669nmkctltfwxhtyh", 4496208},
	{"thor1e0hj0p6trsrw8v5dnfjgmsgeu77mnk34qkgfur", 77146929},
	{"thor1f47wux64gfn7m68jusqxke3k2scmqrtatyzl24", 329085973},
	{"thor1ktx5xk5wqs0vzsmdkcrysxmtvssp0zrsgdmv6w", 20362585},
	{"thor1unrtm2agxlkrv6jfzse469petlqty99y9tfdmg", 4810695},
	{"thor1hxrydan0eypp5eutah2dt63swnqwzuvc0elgmd", 334989847},
	{"thor16hw3da67jrctj6cjn9lrz4vwrwtap73um2m2p7", 281279},
	{"thor15cfg979ye9m9v4pcrjyju6t9y7raz8uv38mw4c", 65782},
	{"thor13xlp4yrlwp2a8k462z0287ft3swjxz8zlf3ms5", 412},
	{"thor1lxnrvguwcsw4hqehu33vrr6784tgv28gsdc34l", 16629241},
	{"thor15930gld787mvxz8cq2y0pqa9lh6d4npmyq2rny", 7630974},
	{"thor1ea4se3lufe7jaydls255dfhyw3th90zhqyzntk", 87118219},
	{"thor1x8smkm7lpp3ga8sxwsz5nf6y8wradpugyh66wd", 40020388},
	{"thor1n4vn336ws9jz75428ds9tw8hxhmwpa2zc2lzlh", 35029898},
	{"thor1t0wc9j6jsut6g4chhzxp5ledqd3wx53thxq73l", 248},
	{"thor1rgecczkvgpw233t2caxd6ucv457dsasf6p9yt7", 2183935},
	{"thor1n0cxg33alsh88zqwjq35dx2xujkzezxu24rs6h", 24417747},
	{"thor13nuhnmkx0zddwu4z0u4d0au9rzkmknc7tsdk46", 4},
	{"thor132243hqt4uv5zpkls98dk662nn73067wha2ech", 24671459},
	{"thor1kqn5vjvv4tmk9pfecve8tjhm77jnk6sv2y4dve", 4},
	{"thor1yes8zxhq4p3r5rp9vt6rk9fdxqf2mnm43apumu", 328478451},
	{"thor1gq936peael6u22pxkaawcvuf4y8555j0yusae9", 947089},
	{"thor10nev8r4xv67jgpg05w5fgamhagt5e0cwwylfma", 8400965},
	{"thor19c70xjhlgal42hq4mm5xem7nx5x36eyk4pwatz", 448121},
	{"thor1de8m2hvw9dc9rvptscvd3fexfj3u75aa3r54k2", 1151129},
	{"thor1tkn7706tp2js0j4lsgemttmgwt2jym8kngs7en", 927815},
	{"thor1glytzlz0zllugdsl3yt4cc0mejw83fq9u7mrh0", 485718663},
	{"thor1k8kmz5h7dfjthp56dducr9yxq4jjg9teju3n3a", 1006033913},
	{"thor1eyw2et0yt6kphjvcdf5nlh5pn5szssunx3gpkx", 12999},
	{"thor1fwya3q0lvr4fufqhn5x94p4cuyvsejwqg9wk94", 313482465},
	{"thor1mpdccj4wr88v2jw28cw2cqluqqcrkux6n45n2m", 9566},
	{"thor1jnnep660vqjkkyjxguw84sstvawew25cs6dw5x", 705342501},
	{"thor1456ja57j5wasf23mhgjpskq8mwhxzxejplh0z5", 797017915},
	{"thor1drm88nvzn8qclrddac876ectt548cscgukm62d", 50},
	{"thor19tl4glhxgh57x4s6ua85n7etahws3r4kzgnyp9", 1020407537},
	{"thor1tfm4q8u57qzsznpvh02s8j483aga63cl02k6jt", 8980092},
	{"thor1gldel9ltf7tlmfrtuual8f05z20ds00kwpmr7a", 506},
	{"thor1f52jx7hnwmds290xpak5v6nteaw65txxz0jsr7", 2},
	{"thor1aypu43w55w88xfw7ygq7xplkq6yuepfydpqctx", 23694605},
	{"thor1hxcdgn43pyz58ajdqd0hl3rfl3avwdd5y27whf", 39982019},
	{"thor1mryd47d6ydk2m26m43k8xhp3v65hgegxtyzhf7", 1658975},
	{"thor1uvus0fxnjjut5v25cz3s4rrmlmkkl69qnu35qr", 5502761},
	{"thor186u9lhrnypjd3pv0rlykytj5a2rke0azu5q3lg", 637},
	{"thor1sdxjkhx2arzyspwe3l5twe90sc7gghvpd4m6hk", 5244269},
	{"thor18zg6y8ylus8n3tpzu5xxge3yyquj03vylstrl3", 1086409635},
	{"thor17u5tc7q2scaysfpq87gqevzuxu4f7ca27q0rez", 56146522},
	{"thor1k7tnkjlgfdwylcp3xdh6kcpw3n3h6zp79sul07", 250187870},
	{"thor100lvyqpq7mrfsu44w882wqe8y60r2dkw57kcan", 15531161},
	{"thor12kyfj7uvzs8e8jx2hq9pxc5ruyx07d47kuajan", 80120932},
	{"thor13ammr8d4pjgspskj95m9wxz5ujg0ym0r2fa2m4", 17387},
	{"thor1rkpukrhljr72sxww2t0nwvng84zegp59805e03", 5246},
	{"thor1uwpshu52te8n36mk8hfy3ejjr64jma2s6wsmfq", 5307},
	{"thor142s5raq6jtes359hmy8jte9r34lj8gqs5dx5uk", 1010925918},
}

// Step 6 — erroneous TRON slash refund (board note 942, h28172517, security event
// "missing tx out in=EB30B73E…"). The TRON.USDT outbound was paid EXACTLY ONCE on-chain
// (tx 37509f3d…c702, exact amount/recipient match) but from gqr5qzgg while the txout
// action was assigned to cwcsrz9r — the vault match failed during a TRON signing stall
// and it was slashed as an unauthorized outbound. Same class as the ZEC step-5 refund,
// but the item already COMPLETED (confirmed: tx/status completed, out_txs=1, 0 copies in
// queue/outbound or scheduled), so the refund is gated on the canonical solvency check
// (the slash double-debited the USDT pool, leaving ~tronSlashUsdtAssetCredit of unaccounted
// USDT in the vaults) rather than on cancelling a still-parked copy. Pinned from the
// h28172517 block events; re-verify at the cut. HaltSigningTRON / HaltTRONTrading must stay
// set until the upgrade height.
const (
	tronSlashUsdtAssetCredit = uint64(860_873_585_100)   // +8,608.735851 TRON.USDT back into the pool (reverse the slash's double-debit of the outbound coin)
	tronSlashUsdtRuneDebit   = uint64(1_260_287_094_381) // -12,602.87094381 RUNE (reverse the slash's USDT-pool credit)
	tronSlashTrxRuneDebit    = uint64(678_673_582)       // -6.78673582 RUNE (reverse the slash's TRX-pool credit); the 14.0285 TRX is real gas, so NO TRX coin is credited back (leave the gas debited, same as ZEC)
	tronSlashAsgardToReserve = uint64(1_260_965_767_963) // move the freed pool-reimburse RUNE Asgard->Reserve to fund the refund (= usdtRuneDebit + trxRuneDebit)
	tronSlashBondRefundTotal = uint64(2_521_929_371_268) // sum of the per-node bond_cost refunds from the h28172517 slash events (validated against the table sum at apply time). NOT 2× the pool credit: that is 2,521,931,535,926, 2,164,658 higher — the refund basis is the actual bond_cost, not a derived double.
	tronSlashInHash          = "EB30B73ED730E2C8E2CB55C07780B437AEC2BAAE0AB0BBA8124B68DBBBA6F7AD"
	tronSlashToAddress       = "TJFbQUsxR6HHPo58TH5FnBaWJ6ZmEj37f3"
	// tronSlashCompletedOutHash is the TRON tx hash of the ONE legitimate completed send
	// (tx/status out_txs[0]). The cancel scan carves it out so the already-paid copy is not
	// mistaken for a second settled retry; any OTHER real OutHash on a copy = a second pay.
	tronSlashCompletedOutHash = "37509F3D6ACEE8EF0C6E809185B2DC1D3CEB46610ADEFA694A4E5D6D5167C702"
)

// tronSlashUsdtAsset / tronSlashTrxAsset — the two slashed TRON pools (verified live).
var (
	tronSlashUsdtAsset = "TRON.USDT-TR7NHQJEKQXGTCI8Q8ZY4PL8OTSZGJLJ6T"
	tronSlashTrxAsset  = "TRON.TRX"
)

// tronSlashBondRefunds19to20 — the bond PROVIDERS of the 18 slashed members (gqr5qzgg vault
// at h28172517), each paid its share of its node's slash as liquid RUNE (ZEC-parity, NOT a
// per-node operator payment). Share = floor(node_bond_cost * provider_bond / node_total_bond)
// using the PRE-slash bonds at h28172516; each node's floor remainder goes to that node's
// largest-bond provider so every per-node subtotal equals its bond_cost exactly. Providers
// backing more than one node are summed into one row. Sum == tronSlashBondRefundTotal.
// Re-pin from the h28172517 bond_cost events + h28172516 bond_providers at the cut.
var tronSlashBondRefunds19to20 = []struct {
	address string
	amount  uint64
}{
	{"thor1n9gcud325e25ywe9sycwwh6pqgalq03e5mj7dg", 1671374},
	{"thor1qq3k97p98edy0mpx9vlareaneg2tfnqqs28kam", 139322553537},
	{"thor10ugnd7vk29g887ymycw9wu3m242fp84u35803v", 159559556777},
	{"thor1zmp5dkczl96tqlj0d5qaptyg4frqt5rqt3dpwz", 1497108},
	{"thor1s7w78use8jq3eeyjlhpx4v7kkgc5xm7feqc294", 163859916771},
	{"thor1upuw8yvg96uf4nd6jfuq9s25284dzr45kyzarc", 168414},
	{"thor1h4rdr7y2mxr9f7mrnl40kx7s8wjx99kk0nfu0r", 19501919207},
	{"thor1f8zgw59dt9tzhyu620fpdgs5s7hte7vy3lefqr", 966261540},
	{"thor12pvaukh88f4fjxx4q76dzsamnpuk3pnwus7wgd", 1602208979},
	{"thor1q0zjmlacrm44h0ru5t8t0jn2lm7vfy4x6fznem", 6143769226},
	{"thor16wjfedsp5tnqqaw9nd465nmyfgwjml68nr7eqv", 1252778832},
	{"thor1c3mxxtjksx8l7c3sjuzjelsdw34qsfj5vnzcp9", 12795003320},
	{"thor120vexx8hq06d5nc38e5up9m92jc6hflaum6dfu", 15949783404},
	{"thor1rl8xfsq9p22tkm539r3394xsyuzjd579xmuy7h", 38304317687},
	{"thor12swpcch3nuc8g3mz00v57fd8ys2uph6s0lzvz2", 19422577807},
	{"thor1twp6pat08q8ye3j7cyzmz729mlcmjmcxs4quw8", 1871451022},
	{"thor1chfgr7hw88thn8s9sj59260mmjwpl65t0jxtd7", 2281387913},
	{"thor1smwz0hxflsx2haqs7fqyjpdpg94d4ml2c7rs70", 20882038180},
	{"thor1flrs4nkhjhg0yz4hsqmwfn7tm9zuj8qa2z2jcq", 2018114221},
	{"thor1d2pe9ep5d3ectgqdjjmhv7ne95hrhy8nv9jhn9", 935880790},
	{"thor13nsa9gc8svlnqpxdns64dwq5zlvhhhwq8j3cus", 1722939874},
	{"thor1c20a2h0c66yrefw7dx90dfz8k4hanlylshh4pw", 4522523058},
	{"thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 77079231833},
	{"thor1thc237kme006k5qey0mys07x855atz0690hrh7", 5602249141},
	{"thor14x9q0dvrelvl0ulneq4aea2sl7yp7v7vvgg77w", 5200140016},
	{"thor17fvsxqvz4u2wxw3rarayej58c84zch4aym2fkn", 3710169354},
	{"thor145j86em232hwuh7h483eqe5shamnwp2wwqsg5j", 75801235765},
	{"thor1m24nuaxr5g42qec3mc73ucdk7zwf9qsrmkjhx6", 4467757398},
	{"thor17xn4e53auf4xla7g84ln2f4vjv2ks30fz6s7sa", 3191255},
	{"thor1qjv2qgc8wj5kz5mpdls8pe7tchnl5q44lky3sw", 11840281563},
	{"thor1tc9sydznys0nu0tp5x5glf54cmc7zsj80eam9m", 16470866338},
	{"thor1f4xsyqwn3e0k8kca94pd090k3gxpushzua2mtk", 1582860},
	{"thor1ewpf8uwamtrndrvhtrn90a2rmhgw0lquw90ewl", 15606838266},
	{"thor1c5q3j23rvjjwkududg9ah2krrjt4emtzzvwj0m", 3031689732},
	{"thor1uzq9cvjrpvmky2d04ku3xplpz9p276904g3rhc", 19147524135},
	{"thor17vngul7c990n0umzj8qjjf5exng6xxpte0pqx8", 2345347977},
	{"thor12p854ms0fatcmpz5ht5pmkntlvvry3z97mz6d9", 7172122367},
	{"thor1ayysyz3rz5stve7mfvcslvaek76upwet0vkgj9", 2168102513},
	{"thor1s9hht2uwpq3ucpf05ed5aqlf9rfu5y99w72dhq", 2690229123},
	{"thor1tvzrpkh7hy2avkd6rw9nk07f4qwwyftv4l8gkf", 169615265},
	{"thor1224jdtz8fvq6nt65g8ff23nslzpv372q4ghxff", 158918055610},
	{"thor1x0fjxheq7tmlz38plstwn55tptwh9gwa7msfsf", 2155557958},
	{"thor1ys4eqwefvhj0n5ycs83gjuvnjup3vwscrm5u7l", 3834330436},
	{"thor1phv3czcucg4wz3au9hhva2psmvsx9yjpnkkmgx", 1520447680},
	{"thor1hftu6pxar7z52257vztmt669nmkctltfwxhtyh", 810929697},
	{"thor1e0hj0p6trsrw8v5dnfjgmsgeu77mnk34qkgfur", 13914107956},
	{"thor1f47wux64gfn7m68jusqxke3k2scmqrtatyzl24", 59353467416},
	{"thor1ktx5xk5wqs0vzsmdkcrysxmtvssp0zrsgdmv6w", 3672566339},
	{"thor1unrtm2agxlkrv6jfzse469petlqty99y9tfdmg", 867650085},
	{"thor1hxrydan0eypp5eutah2dt63swnqwzuvc0elgmd", 60418281716},
	{"thor16hw3da67jrctj6cjn9lrz4vwrwtap73um2m2p7", 50731174},
	{"thor15cfg979ye9m9v4pcrjyju6t9y7raz8uv38mw4c", 11864425},
	{"thor13xlp4yrlwp2a8k462z0287ft3swjxz8zlf3ms5", 74440},
	{"thor1lxnrvguwcsw4hqehu33vrr6784tgv28gsdc34l", 2999225860},
	{"thor15930gld787mvxz8cq2y0pqa9lh6d4npmyq2rny", 1376311462},
	{"thor1ea4se3lufe7jaydls255dfhyw3th90zhqyzntk", 15712515359},
	{"thor1x8smkm7lpp3ga8sxwsz5nf6y8wradpugyh66wd", 7218019058},
	{"thor1n4vn336ws9jz75428ds9tw8hxhmwpa2zc2lzlh", 6317941435},
	{"thor1t0wc9j6jsut6g4chhzxp5ledqd3wx53thxq73l", 44860},
	{"thor1rgecczkvgpw233t2caxd6ucv457dsasf6p9yt7", 393891428},
	{"thor1n0cxg33alsh88zqwjq35dx2xujkzezxu24rs6h", 4403949391},
	{"thor13nuhnmkx0zddwu4z0u4d0au9rzkmknc7tsdk46", 754},
	{"thor132243hqt4uv5zpkls98dk662nn73067wha2ech", 4449708606},
	{"thor1kqn5vjvv4tmk9pfecve8tjhm77jnk6sv2y4dve", 871},
	{"thor1yes8zxhq4p3r5rp9vt6rk9fdxqf2mnm43apumu", 59243895645},
	{"thor1gq936peael6u22pxkaawcvuf4y8555j0yusae9", 170815770},
	{"thor10nev8r4xv67jgpg05w5fgamhagt5e0cwwylfma", 1515185945},
	{"thor19c70xjhlgal42hq4mm5xem7nx5x36eyk4pwatz", 80822596},
	{"thor1de8m2hvw9dc9rvptscvd3fexfj3u75aa3r54k2", 207616016},
	{"thor1tkn7706tp2js0j4lsgemttmgwt2jym8kngs7en", 167339425},
	{"thor1glytzlz0zllugdsl3yt4cc0mejw83fq9u7mrh0", 87603511404},
	{"thor1k8kmz5h7dfjthp56dducr9yxq4jjg9teju3n3a", 181446812693},
	{"thor1eyw2et0yt6kphjvcdf5nlh5pn5szssunx3gpkx", 2344597},
	{"thor1fwya3q0lvr4fufqhn5x94p4cuyvsejwqg9wk94", 56539244526},
	{"thor1mpdccj4wr88v2jw28cw2cqluqqcrkux6n45n2m", 1725372},
	{"thor1jnnep660vqjkkyjxguw84sstvawew25cs6dw5x", 127214547345},
	{"thor1456ja57j5wasf23mhgjpskq8mwhxzxejplh0z5", 143748991510},
	{"thor1drm88nvzn8qclrddac876ectt548cscgukm62d", 9104},
	{"thor19tl4glhxgh57x4s6ua85n7etahws3r4kzgnyp9", 184039218662},
	{"thor1tfm4q8u57qzsznpvh02s8j483aga63cl02k6jt", 1619636465},
	{"thor1gldel9ltf7tlmfrtuual8f05z20ds00kwpmr7a", 91440},
	{"thor1f52jx7hnwmds290xpak5v6nteaw65txxz0jsr7", 517},
	{"thor1aypu43w55w88xfw7ygq7xplkq6yuepfydpqctx", 4273524521},
	{"thor1hxcdgn43pyz58ajdqd0hl3rfl3avwdd5y27whf", 7211098935},
	{"thor1mryd47d6ydk2m26m43k8xhp3v65hgegxtyzhf7", 299210484},
	{"thor1uvus0fxnjjut5v25cz3s4rrmlmkkl69qnu35qr", 992470088},
	{"thor186u9lhrnypjd3pv0rlykytj5a2rke0azu5q3lg", 114956},
	{"thor1sdxjkhx2arzyspwe3l5twe90sc7gghvpd4m6hk", 945848882},
	{"thor18zg6y8ylus8n3tpzu5xxge3yyquj03vylstrl3", 195943261244},
	{"thor17u5tc7q2scaysfpq87gqevzuxu4f7ca27q0rez", 10126505026},
	{"thor1k7tnkjlgfdwylcp3xdh6kcpw3n3h6zp79sul07", 45123520377},
	{"thor100lvyqpq7mrfsu44w882wqe8y60r2dkw57kcan", 2801177749},
	{"thor12kyfj7uvzs8e8jx2hq9pxc5ruyx07d47kuajan", 14450494857},
	{"thor13ammr8d4pjgspskj95m9wxz5ujg0ym0r2fa2m4", 3135908},
	{"thor1rkpukrhljr72sxww2t0nwvng84zegp59805e03", 946319},
	{"thor1uwpshu52te8n36mk8hfy3ejjr64jma2s6wsmfq", 957201},
	{"thor142s5raq6jtes359hmy8jte9r34lj8gqs5dx5uk", 182329127731},
}

// signingHalted19to20 reports whether <chain> signing is halted at the current height. The
// slash refunds credit a pool back on the premise that no SECOND outbound of the slashed
// item can still be signed. The canonical solvency gate only catches an ALREADY-OBSERVED
// double-pay (keeper vault balances move on observation, not on signing/broadcast), so a
// signed-but-unobserved retry would slip past it — HaltSigning<chain> is the only true
// guard for that case. It is therefore ENFORCED here, not merely documented: if signing is
// not halted, the refund soft-skips and is redone post-cut.
func signingHalted19to20(ctx cosmos.Context, mgr *Mgrs, chain common.Chain) bool {
	h, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltSigning, chain))
	return err == nil && h > 0 && h <= ctx.BlockHeight()
}

func runZecSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) {
	// HaltSigningZEC MUST be set: it is the only guard against a signed-but-unobserved
	// second pay (the solvency gate below sees vault balances, which move only on
	// observation). Without it, skip and re-pin at the cut.
	if !signingHalted19to20(ctx, mgr, common.ZECChain) {
		ctx.Logger().Error("zec slash refund skipped: HaltSigningZEC not set; an unobserved retry could double-pay", "in_hash", zecSlashInHash)
		return
	}
	// Cancel the already-paid parked outbound FIRST, in its OWN cache context, so the
	// double-pay safety lands even if the financial refund below soft-skips on a drift.
	cancelCtx, cancelCommit := ctx.CacheContext()
	cancelled, settled, scanErr := cancelZecParkedOutbound19to20(cancelCtx, mgr)
	if scanErr != nil {
		// The window could not be fully inspected (a txout read/write failed): the
		// cancel cache is discarded (not committed) and the refund is skipped, since an
		// uninspected batch could hold another live copy or a settled retry. Re-pin at
		// the cut once the store is readable.
		ctx.Logger().Error("zec slash refund skipped: parked-outbound scan incomplete", "in_hash", zecSlashInHash, "error", scanErr)
		return
	}
	if cancelled {
		cancelCommit()
	}
	// The pool ZEC credit below assumes the vault still holds the parked outbound's
	// backing, i.e. no retry ever settled. Only a live, still-unsent copy proves that
	// (an observed retry would have stamped its OutHash on the live copy). If a retry
	// already settled, or the item is gone/outside the window so it cannot be proven,
	// the slash's pool debit may be real: skip the refund and re-pin at the cut.
	if !cancelled || settled {
		ctx.Logger().Error("zec slash refund skipped: parked outbound not found unsent; a retry may have settled",
			"in_hash", zecSlashInHash, "cancelled", cancelled, "settled", settled)
		return
	}
	// The financial refund soft-skips on drift (re-pin at the cut).
	refundCtx, refundCommit := ctx.CacheContext()
	if err := applyZecSlashRefund19to20(refundCtx, mgr); err != nil {
		ctx.Logger().Error("zec slash refund skipped: state does not match the pinned figures", "error", err)
		return
	}
	refundCommit()
}

// applyZecSlashRefund19to20 reverses the erroneous h28181638 ZEC slash and refunds the
// affected bond providers, mirroring the step-1 BTC slash refund: credit the pool ZEC
// back, remove the RUNE the slash added, move that freed RUNE Asgard->Reserve, then pay
// each provider its share of the slash as liquid RUNE from the Reserve. The parked
// outbound is cancelled separately (cancelZecParkedOutbound19to20, own cache context).
// The observed-txout handler already debited the vault once, so the vault is correct and
// is left untouched.
func applyZecSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()
	zec, err := common.NewAsset("ZEC.ZEC")
	if err != nil {
		return fmt.Errorf("parse ZEC.ZEC: %w", err)
	}

	// Validate every refund address + sum up front so a bad row aborts before any write.
	total := cosmos.ZeroUint()
	for _, r := range zecSlashBondRefunds19to20 {
		if _, addrErr := cosmos.AccAddressFromBech32(r.address); addrErr != nil {
			return fmt.Errorf("bad zec-slash refund address %s: %w", r.address, addrErr)
		}
		total = total.Add(cosmos.NewUint(r.amount))
	}
	if !total.Equal(cosmos.NewUint(zecSlashBondRefundTotal)) {
		return fmt.Errorf("zec-slash refund sum %s != pinned %d", total, zecSlashBondRefundTotal)
	}

	// Reverse the pool deltas: credit the ZEC back, remove the RUNE the slash added. This
	// reverses an erroneous DOUBLE debit of the pool ZEC (the swap debited it once for the
	// paid outbound, then the slash debited it again), so the pool is currently under-booked
	// by the slash's extra coin debit while the vault already paid exactly one outbound.
	// That under-booking shows up as ~zecSlashPoolAssetCredit of unaccounted ZEC in the
	// vaults, so the SAME canonical solvency gate as step 1 catches an ALREADY-OBSERVED
	// second pay (which has reduced vault holdings): the headroom is gone, solvency drops,
	// and the credit skips. TWO caveats this gate does NOT cover, handled elsewhere:
	//  - it only catches an OBSERVED retry; a signed-but-unobserved one leaves vault balances
	//    unchanged, so runZecSlashRefund19to20 ENFORCES HaltSigningZEC (signingHalted19to20)
	//    as the real guard for that case. HaltSigningZEC MUST stay set until the upgrade.
	//  - the gate is zero-margin (freeZEC >= X), and calculateNetworkSolvency subtracts the
	//    MaxGas of every OTHER in-window unsigned ZEC outbound — for ZEC the gas asset IS the
	//    pool asset, so a backlog can drag freeZEC below X and SKIP the honest refund (safe
	//    direction, no loss). Re-measure freeZEC against the live ZEC outbound backlog at the
	//    cut and re-pin if the drag is material. (TRON.USDT is immune: its gas is TRX.)
	pool, err := k.GetPool(ctx, zec)
	if err != nil {
		return fmt.Errorf("get ZEC pool: %w", err)
	}
	if pool.IsEmpty() {
		return fmt.Errorf("ZEC pool is empty")
	}
	freeZEC, err := unaccountedVaultAsset19to20(ctx, mgr, zec)
	if err != nil {
		return fmt.Errorf("zec solvency: %w", err)
	}
	if freeZEC.LT(cosmos.NewUint(zecSlashPoolAssetCredit)) {
		return fmt.Errorf("unaccounted ZEC %s below slash credit %d; vaults do not hold the reversed ZEC (a retry may have paid)", freeZEC, zecSlashPoolAssetCredit)
	}
	runeDebit := cosmos.NewUint(zecSlashPoolRuneDebit)
	if pool.BalanceRune.LT(runeDebit) {
		return fmt.Errorf("ZEC pool BalanceRune %s below slash credit %d; pool drifted", pool.BalanceRune, zecSlashPoolRuneDebit)
	}
	pool.BalanceAsset = pool.BalanceAsset.Add(cosmos.NewUint(zecSlashPoolAssetCredit))
	pool.BalanceRune = pool.BalanceRune.Sub(runeDebit)
	if err := k.SetPool(ctx, pool); err != nil {
		return fmt.Errorf("set ZEC pool: %w", err)
	}
	poolSlashAmt := []PoolAmt{
		{Asset: zec, Amount: int64(zecSlashPoolAssetCredit)},
		{Asset: common.RuneAsset(), Amount: -int64(zecSlashPoolRuneDebit)},
	}
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(zec, poolSlashAmt)); err != nil {
		ctx.Logger().Error("emit zec slash reversal event", "error", err)
	}

	// Move the freed pool-reimburse RUNE Asgard->Reserve to fund the refund (step 1 / !4798).
	if err := k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(zecSlashAsgardToReserve)))); err != nil {
		return fmt.Errorf("asgard->reserve: %w", err)
	}

	// Pay each affected bond provider its share as liquid RUNE from the Reserve, which
	// now holds the slash's penalty half plus the freed pool-reimburse half. Atomic: any
	// bad address or failed send returns an error so the cache context discards the step.
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(total) {
		return fmt.Errorf("reserve %s below zec-slash refund total %s", bal, total)
	}
	for _, r := range zecSlashBondRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		if err != nil {
			return fmt.Errorf("bad zec-slash refund address %s: %w", r.address, err)
		}
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(r.amount)))
		if err := k.SendFromModuleToAccount(ctx, ReserveName, to, coins); err != nil {
			return fmt.Errorf("zec-slash refund to %s (%d): %w", r.address, r.amount, err)
		}
	}

	ctx.Logger().Info("refunded erroneous zec slash at h28181638",
		"refund_total", zecSlashBondRefundTotal, "providers", len(zecSlashBondRefunds19to20),
		"pool_rune_removed", zecSlashPoolRuneDebit, "pool_zec_restored", zecSlashPoolAssetCredit)
	return nil
}

// cancelZecParkedOutbound19to20 cancels the already-paid parked ZEC outbound so it can
// never re-sign. It returns cancelled=true if it found + cancelled a live item, and
// settled=true if it saw a copy carrying a real OutHash (a retry that was signed and
// observed, i.e. the vault paid a second time). Signing is halted, so
// LackSigning keeps rescheduling it forward to new heights/vaults; it is located by
// in_hash + recipient (NOT a fixed height/vault). The scan is BOUNDED to the live
// outbound window [h-max(TxOutDelayMax,SigningTransactionPeriod), h+TxOutDelayMax] —
// mirroring calculateNetworkSolvency rather than walking the whole txout keyspace
// (completed blocks are never pruned) — and scans the WHOLE window (no early exit) so a
// settled copy at a LOWER height than the live one is still seen. The backward bound takes
// the larger of TxOutDelayMax and SigningTransactionPeriod because a still-unsent copy
// stays signable for the signing period, which GetConfigInt64 does not constrain to be
// <= TxOutDelayMax. BlankTxID (not
// SetDone): the outbound is already on-chain; marking it done would re-run completion
// accounting and double-count. If it is already gone (sent/cancelled/never present) the
// cancel is a no-op and the caller skips the refund. It returns a non-nil error if any
// batch in the window could not be read or written (a decode failure or a failed cancel
// write): the window was only partially inspected, so the caller MUST discard the cache
// and skip the refund rather than act on an incomplete scan (mirroring
// calculateNetworkSolvency, which also propagates GetTxOut errors). Committed in its OWN
// cache context so the double-pay fix lands even if the financial refund soft-skips on drift.
func cancelZecParkedOutbound19to20(ctx cosmos.Context, mgr *Mgrs) (cancelled, settled bool, err error) {
	k := mgr.Keeper()
	delay := k.GetConfigInt64(ctx, constants.TxOutDelayMax)
	// A still-unsent copy stays signable for SigningTransactionPeriod blocks, which
	// GetConfigInt64 does not constrain to be <= TxOutDelayMax; bound the BACKWARD search
	// by the larger of the two so a signable copy can never fall below the window and
	// re-sign after the refund. Forward reschedules move at most TxOutDelayMax.
	back := delay
	if sp := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod); sp > back {
		back = sp
	}
	lo := ctx.BlockHeight() - back
	// A window around the UPGRADE height assumes LackSigning keeps rescheduling the item
	// forward. With HaltSigningZEC set that never happens: the item sits at its ORIGINAL
	// scheduled outbound height, which on mainnet is ~30k blocks below the window and would
	// be missed, leaving the paid outbound uncancelled and the refund wrongly skipped. Pull
	// the floor down to the inbound voter's OutboundHeight so the stuck copy is always
	// reached. Extend-only: never shrinks the window, so a rescheduled-forward copy is still
	// covered. A decode error on the voter is corruption → propagate (skip the refund).
	voter, vErr := k.GetObservedTxInVoter(ctx, common.TxID(zecSlashInHash))
	if vErr != nil {
		return false, false, fmt.Errorf("zec slash: get inbound voter %s: %w", zecSlashInHash, vErr)
	}
	if voter.OutboundHeight > 0 && voter.OutboundHeight < lo {
		lo = voter.OutboundHeight
	}
	if lo < 1 {
		lo = 1
	}
	hi := ctx.BlockHeight() + delay
	// Scan the WHOLE window before returning (no early exit on the first live copy):
	// a settled retry (a copy carrying a real OutHash) can sit at a LOWER height than
	// the live, still-unsent copy, so returning early would miss it and wrongly let the
	// refund proceed against backing a second real send already consumed. Blank every
	// live copy (none may stay signable) and report settled=true if ANY copy was signed
	// + observed anywhere in the window.
	for h := hi; h >= lo; h-- {
		blk, gErr := k.GetTxOut(ctx, h)
		if gErr != nil {
			// A read error (a TxOut record the codec cannot decode) is NOT an empty
			// height: the window was only partially inspected, so a live or settled
			// copy at this height may be invisible. Do not swallow it as absent —
			// propagate so the caller skips the cancel commit AND the financial refund
			// (mirroring calculateNetworkSolvency, which also propagates GetTxOut errors).
			return false, settled, fmt.Errorf("zec slash: scan txout height %d: %w", h, gErr)
		}
		if blk == nil {
			continue
		}
		dirty := false
		for i := range blk.TxArray {
			it := blk.TxArray[i]
			if it.InHash.String() != zecSlashInHash || it.ToAddress.String() != zecSlashToAddress {
				continue
			}
			if !it.OutHash.IsEmpty() && !it.OutHash.Equals(common.BlankTxID) {
				ctx.Logger().Error("zec slash: parked outbound retry already settled", "height", h, "out_hash", it.OutHash)
				settled = true
				continue
			}
			if it.OutHash.IsEmpty() {
				blk.TxArray[i].OutHash = common.BlankTxID
				dirty = true
			}
		}
		if dirty {
			if sErr := k.SetTxOut(ctx, blk); sErr != nil {
				// Cannot guarantee the cancel persisted: propagate so the caller discards
				// this cache context (cancelled=false) and skips the refund.
				return false, settled, fmt.Errorf("zec slash: set txout for cancel height %d: %w", h, sErr)
			}
			cancelled = true
			ctx.Logger().Info("zec slash: cancelled parked outbound copy", "height", h, "in_hash", zecSlashInHash)
		}
	}
	if !cancelled {
		ctx.Logger().Info("zec slash: no live parked outbound to cancel", "in_hash", zecSlashInHash)
	}
	return cancelled, settled, nil
}

// runTronSlashRefund19to20 applies the h28172517 TRON slash refund atomically or not at
// all. The item already completed on-chain, so (unlike ZEC) the refund is gated on the
// canonical solvency check plus HaltSigningTRON, not on cancelling a parked copy; a
// defensive scan still blanks any still-live reschedule copy so it can never pay a second
// time, and reports a SECOND settled copy (a real OutHash other than the known completed one).
func runTronSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) {
	// HaltSigningTRON MUST be set: it is the only guard against a signed-but-unobserved
	// second pay (the solvency gate sees vault balances, which move only on observation).
	if !signingHalted19to20(ctx, mgr, common.TRONChain) {
		ctx.Logger().Error("tron slash refund skipped: HaltSigningTRON not set; an unobserved retry could double-pay", "in_hash", tronSlashInHash)
		return
	}
	cancelCtx, cancelCommit := ctx.CacheContext()
	blanked, settled, err := cancelTronParkedOutbound19to20(cancelCtx, mgr)
	if err != nil {
		ctx.Logger().Error("tron slash refund skipped: parked-outbound scan incomplete", "in_hash", tronSlashInHash, "error", err)
		return
	}
	if settled {
		// A second copy carries a real OutHash distinct from the completed send: the vault
		// paid twice, so the slash's pool debit is partly real. Skip the refund and re-pin at
		// the cut. But STILL commit the defensive blank of any live empty-OutHash copy — a
		// settled retry does not make the live duplicate safe; it stays signable and could pay
		// a THIRD time. Blanking a duplicate of an already-paid send is always safe.
		if blanked {
			cancelCommit()
		}
		ctx.Logger().Error("tron slash refund skipped: a settled retry copy was found", "in_hash", tronSlashInHash)
		return
	}
	// Branch the refund context from ctx BEFORE committing the blank, so the refund's
	// solvency gate still counts the in-flight (empty-OutHash, still-a-liability) copy via
	// calculateNetworkSolvency: a blank visible to the solvency read would restore phantom
	// headroom and wrongly pass. The blank and the refund touch disjoint keys — the cancel
	// writes only the duplicate copy's txout (SetTxOut), the refund writes pools / module
	// balances / accounts — so the two commits into ctx are independent.
	refundCtx, refundCommit := ctx.CacheContext()
	refundErr := applyTronSlashRefund19to20(refundCtx, mgr)

	// Commit the defensive blank UNCONDITIONALLY (huginn Major): it stops a second signing of
	// the duplicate parked outbound and MUST land even when the refund below is skipped on a
	// state-drift / solvency mismatch. Leaving the re-signable copy live on a refund failure
	// (the prior behaviour) was the double-pay exposure; the standalone blank is strictly
	// safer, and committing the refund afterwards does not clobber it (disjoint keys).
	if blanked {
		cancelCommit()
	}
	if refundErr != nil {
		ctx.Logger().Error("tron slash refund skipped: state does not match the pinned figures", "error", refundErr)
		return
	}
	refundCommit()
}

func applyTronSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()
	usdt, err := common.NewAsset(tronSlashUsdtAsset)
	if err != nil {
		return fmt.Errorf("parse %s: %w", tronSlashUsdtAsset, err)
	}
	trx, err := common.NewAsset(tronSlashTrxAsset)
	if err != nil {
		return fmt.Errorf("parse %s: %w", tronSlashTrxAsset, err)
	}

	// Validate every refund address + sum up front so a bad row aborts before any write.
	total := cosmos.ZeroUint()
	for _, r := range tronSlashBondRefunds19to20 {
		if _, addrErr := cosmos.AccAddressFromBech32(r.address); addrErr != nil {
			return fmt.Errorf("bad tron-slash refund address %s: %w", r.address, addrErr)
		}
		total = total.Add(cosmos.NewUint(r.amount))
	}
	if !total.Equal(cosmos.NewUint(tronSlashBondRefundTotal)) {
		return fmt.Errorf("tron-slash refund sum %s != pinned %d", total, tronSlashBondRefundTotal)
	}

	// USDT pool: credit the double-debited coin back and reverse the slash's RUNE credit.
	// Solvency-gated like step 1 / ZEC: the slash double-debited the USDT pool while the
	// vault paid exactly one outbound, so ~tronSlashUsdtAssetCredit of unaccounted USDT sits
	// in the vaults. The gate passes in the normal single-paid case and fails (skips) if a
	// retry was also paid.
	usdtPool, err := k.GetPool(ctx, usdt)
	if err != nil {
		return fmt.Errorf("get TRON.USDT pool: %w", err)
	}
	if usdtPool.IsEmpty() {
		return fmt.Errorf("TRON.USDT pool is empty")
	}
	freeUSDT, err := unaccountedVaultAsset19to20(ctx, mgr, usdt)
	if err != nil {
		return fmt.Errorf("tron usdt solvency: %w", err)
	}
	if freeUSDT.LT(cosmos.NewUint(tronSlashUsdtAssetCredit)) {
		return fmt.Errorf("unaccounted TRON.USDT %s below slash credit %d; vaults do not hold the reversed USDT (a retry may have paid)", freeUSDT, tronSlashUsdtAssetCredit)
	}
	usdtRuneDebit := cosmos.NewUint(tronSlashUsdtRuneDebit)
	if usdtPool.BalanceRune.LT(usdtRuneDebit) {
		return fmt.Errorf("TRON.USDT pool BalanceRune %s below slash credit %d; pool drifted", usdtPool.BalanceRune, tronSlashUsdtRuneDebit)
	}
	usdtPool.BalanceAsset = usdtPool.BalanceAsset.Add(cosmos.NewUint(tronSlashUsdtAssetCredit))
	usdtPool.BalanceRune = usdtPool.BalanceRune.Sub(usdtRuneDebit)
	if poolErr := k.SetPool(ctx, usdtPool); poolErr != nil {
		return fmt.Errorf("set TRON.USDT pool: %w", poolErr)
	}

	// TRX pool: reverse only the slash's RUNE credit. The 14.0285 TRX is real gas spend (the
	// pool bears gas on a normal outbound too), so NO TRX coin is credited back — leaving the
	// gas debited keeps pool depth == vault backing, same treatment as the ZEC 25,000-zat gas.
	trxPool, err := k.GetPool(ctx, trx)
	if err != nil {
		return fmt.Errorf("get TRON.TRX pool: %w", err)
	}
	if trxPool.IsEmpty() {
		return fmt.Errorf("TRON.TRX pool is empty")
	}
	trxRuneDebit := cosmos.NewUint(tronSlashTrxRuneDebit)
	if trxPool.BalanceRune.LT(trxRuneDebit) {
		return fmt.Errorf("TRON.TRX pool BalanceRune %s below slash credit %d; pool drifted", trxPool.BalanceRune, tronSlashTrxRuneDebit)
	}
	trxPool.BalanceRune = trxPool.BalanceRune.Sub(trxRuneDebit)
	if err := k.SetPool(ctx, trxPool); err != nil {
		return fmt.Errorf("set TRON.TRX pool: %w", err)
	}

	// Emit the pool reversals as slash events for Midgard depth tracking (mirrors step 1 / ZEC).
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(usdt, []PoolAmt{
		{Asset: usdt, Amount: int64(tronSlashUsdtAssetCredit)},
		{Asset: common.RuneAsset(), Amount: -int64(tronSlashUsdtRuneDebit)},
	})); err != nil {
		ctx.Logger().Error("emit tron usdt slash reversal event", "error", err)
	}
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(trx, []PoolAmt{
		{Asset: common.RuneAsset(), Amount: -int64(tronSlashTrxRuneDebit)},
	})); err != nil {
		ctx.Logger().Error("emit tron trx slash reversal event", "error", err)
	}

	// Move the freed pool-reimburse RUNE Asgard->Reserve to fund the refund (step 1 / !4798).
	if err := k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(tronSlashAsgardToReserve)))); err != nil {
		return fmt.Errorf("asgard->reserve: %w", err)
	}

	// Pay each affected bond provider its share as liquid RUNE from the Reserve, which now
	// holds the slash's penalty half plus the freed pool-reimburse half. Atomic.
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(total) {
		return fmt.Errorf("reserve %s below tron-slash refund total %s", bal, total)
	}
	for _, r := range tronSlashBondRefunds19to20 {
		to, err := cosmos.AccAddressFromBech32(r.address)
		if err != nil {
			return fmt.Errorf("bad tron-slash refund address %s: %w", r.address, err)
		}
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(r.amount)))
		if err := k.SendFromModuleToAccount(ctx, ReserveName, to, coins); err != nil {
			return fmt.Errorf("tron-slash refund to %s (%d): %w", r.address, r.amount, err)
		}
	}

	ctx.Logger().Info("refunded erroneous tron slash at h28172517",
		"refund_total", tronSlashBondRefundTotal, "providers", len(tronSlashBondRefunds19to20),
		"usdt_restored", tronSlashUsdtAssetCredit, "usdt_rune_removed", tronSlashUsdtRuneDebit, "trx_rune_removed", tronSlashTrxRuneDebit)
	return nil
}

// cancelTronParkedOutbound19to20 blanks any still-live (empty-OutHash) copy of the paid
// TRON outbound so it can never be signed a second time, and reports settled=true if it
// finds a copy carrying a real OutHash OTHER than the known completed send
// (tronSlashCompletedOutHash) — i.e. a second, retried payment. It is located by in_hash +
// recipient across the live outbound window, with the backward floor pulled down to the
// inbound voter's OutboundHeight (as in the ZEC cancel). The one legitimate completed copy
// carries tronSlashCompletedOutHash and is left untouched and NOT treated as settled.
// Returns a non-nil error if any batch could not be read or written, so the caller discards
// the cache and skips the refund.
func cancelTronParkedOutbound19to20(ctx cosmos.Context, mgr *Mgrs) (blanked, settled bool, err error) {
	k := mgr.Keeper()
	delay := k.GetConfigInt64(ctx, constants.TxOutDelayMax)
	back := delay
	if sp := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod); sp > back {
		back = sp
	}
	lo := ctx.BlockHeight() - back
	voter, vErr := k.GetObservedTxInVoter(ctx, common.TxID(tronSlashInHash))
	if vErr != nil {
		return false, false, fmt.Errorf("tron slash: get inbound voter %s: %w", tronSlashInHash, vErr)
	}
	if voter.OutboundHeight > 0 && voter.OutboundHeight < lo {
		lo = voter.OutboundHeight
	}
	if lo < 1 {
		lo = 1
	}
	hi := ctx.BlockHeight() + delay
	for h := hi; h >= lo; h-- {
		blk, gErr := k.GetTxOut(ctx, h)
		if gErr != nil {
			return false, false, fmt.Errorf("tron slash: scan txout height %d: %w", h, gErr)
		}
		if blk == nil {
			continue
		}
		dirty := false
		for i := range blk.TxArray {
			it := blk.TxArray[i]
			if it.InHash.String() != tronSlashInHash || it.ToAddress.String() != tronSlashToAddress {
				continue
			}
			if it.OutHash.IsEmpty() {
				blk.TxArray[i].OutHash = common.BlankTxID
				dirty = true
				continue
			}
			// A real OutHash that is NOT the one legitimate completed send, and not an
			// already-blanked copy, means a SECOND payment settled — the vault paid twice.
			if !it.OutHash.Equals(common.BlankTxID) && it.OutHash.String() != tronSlashCompletedOutHash {
				settled = true
				ctx.Logger().Error("tron slash: a settled retry copy found", "height", h, "out_hash", it.OutHash)
			}
		}
		if dirty {
			if sErr := k.SetTxOut(ctx, blk); sErr != nil {
				return false, settled, fmt.Errorf("tron slash: set txout for cancel height %d: %w", h, sErr)
			}
			blanked = true
			ctx.Logger().Info("tron slash: cancelled live parked outbound copy", "height", h, "in_hash", tronSlashInHash)
		}
	}
	return blanked, settled, nil
}

// migrate19to20Mainnet runs the seven mainnet-only soft-skip steps folded into Migrate19to20
// (migrations_common.go, after the TEX clout merge): it reverses the rzxslmgc bond slash
// (+ refunds the 218 bond providers), donates the Secured-Ops LP positions and repays NDT
// from the Reserve, restores ETH.THOR depth (donate + Reserve-paired surplus credit + NDT
// capital refund), retires the residual synths of the low-synth-% pools (uncompensated
// burn), refunds the erroneous h28181638 ZEC slash, refunds the erroneous h28172517 TRON
// slash, and restores the phantom-insolvency SOL bond slash at h27591961. Each step
// soft-skips on a drift, so one does not drop the others.
func (m Migrator) migrate19to20Mainnet(ctx sdk.Context) error {
	if !mainnetDonationSpecComplete {
		// Ships guarded: the pinned amounts are completed + the flag flipped at
		// the release cut. Until then, log and SKIP (return nil) rather than
		// returning an error, so a build that reaches an upgrade height before
		// the cut no-ops instead of halting the chain.
		ctx.Logger().Info("migrate19to20Mainnet skipped: mainnet donation spec not complete")
		return nil
	}
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	// Step 1: reverse the bond slash and refund bond providers (soft-skip).
	runSlashRefund19to20(ctx, m.mgr)
	// Step 2: donate the BSC.USDC/USDT + BASE.ETH LP positions + repay NDT (own soft-skip).
	spec, err := mainnetDonationSpec19to20()
	if err != nil {
		return err
	}
	runDonationMigration(ctx, m.mgr, spec)
	// Step 3: ETH.THOR donate + depth restore + NDT capital refund (own soft-skip).
	runEthThorRestore19to20(ctx, m.mgr)
	// Step 4: retire residual synths from the low-synth-% pools (own soft-skip).
	runLowSynthBurn19to20(ctx, m.mgr)
	// Step 5: refund the erroneous h28181638 ZEC "missing tx out" slash and clear
	// the already-paid parked outbound (own soft-skip). See issue 3393.
	runZecSlashRefund19to20(ctx, m.mgr)
	// Step 6: refund the erroneous h28172517 TRON "missing tx out" slash (own soft-skip).
	// The outbound completed once on-chain; the refund is solvency-gated. See issue 3393.
	runTronSlashRefund19to20(ctx, m.mgr)
	// Step 7: restore the phantom-insolvency SOL bond slash at h27591961 per bond provider
	// (own soft-skip, per-node best-effort; pool leg is a one-line A/C switch). See issue 3393.
	runSolSlashRefund19to20(ctx, m.mgr)
	return nil
}

// mainnetSlashRefunds19to20 — 218 bond-provider refunds, sum = 11354727112439 (113,547.27112439 RUNE)
var mainnetSlashRefunds19to20 = []struct {
	address string
	amount  uint64
}{
	{address: "thor1yes8zxhq4p3r5rp9vt6rk9fdxqf2mnm43apumu", amount: 1106619530928},
	{address: "thor18zg6y8ylus8n3tpzu5xxge3yyquj03vylstrl3", amount: 842787757163},
	{address: "thor120mpler6mkgdszaz6pah65crjenmjr74futj97", amount: 717616192571},
	{address: "thor1k8kmz5h7dfjthp56dducr9yxq4jjg9teju3n3a", amount: 678107705940},
	{address: "thor10ttxj3vfjyav0t8398zmpkdz7x902qa4wlwa46", amount: 618617490659},
	{address: "thor1456ja57j5wasf23mhgjpskq8mwhxzxejplh0z5", amount: 611477233862},
	{address: "thor1grzsp2ywwr2f0ank2wdjc6zwu3ell2etjerll5", amount: 605080916680},
	{address: "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", amount: 590189473762},
	{address: "thor1hxrydan0eypp5eutah2dt63swnqwzuvc0elgmd", amount: 451993550613},
	{address: "thor1jnnep660vqjkkyjxguw84sstvawew25cs6dw5x", amount: 448543609119},
	{address: "thor1tqz4wssqavawkc23zykjljz76h8sq84l35607t", amount: 363398776647},
	{address: "thor1tl96h6xy56snwh7w5zp5agd02swkywxpeg7ksu", amount: 358272543776},
	{address: "thor1vwuyfwk85a247ett40j7uwk3k0s8cm5qaxevwl", amount: 257381979579},
	{address: "thor1fd2288t5cgmzp9arc8lgk43acqyjx2lfe893u4", amount: 250329571011},
	{address: "thor158qequwhhnggm4ch4psv55yqpxsugf67n62dy2", amount: 239436927186},
	{address: "thor1eftvlx5glrrcnjqj2lvlyz4l5a0teradh4dvfz", amount: 190549893795},
	{address: "thor1xcqsns97anwd5nx685cuknza7lk3mr5hvm3uwp", amount: 169029641337},
	{address: "thor12sj00sl4mv2ht260kll4tqcj6afqymzvs97u90", amount: 162604179998},
	{address: "thor1ysqwqmcnl3lc886l2u0pqwkrxa9uhywn0jtqcq", amount: 147957379358},
	{address: "thor1fdzkfyf9p4f32kwgn70cfs4555m6lu8n5n5yka", amount: 134408211366},
	{address: "thor1aafq8u4jwavy8fur6cc87khym9nmurrq0l9cvz", amount: 92213781851},
	{address: "thor1glytzlz0zllugdsl3yt4cc0mejw83fq9u7mrh0", amount: 91394021732},
	{address: "thor1mge39hjzfr7uhxkr6ve829jsvytnzafwse5y65", amount: 77248685289},
	{address: "thor1nmnq0r99fwfkp3pg8sdj4wlj2l96hx73m6835y", amount: 71150959771},
	{address: "thor1qvmeavyusxyet7szr2azjzut7tamw4ycfg08ss", amount: 69340694317},
	{address: "thor1v2z9k2ef268n3tjtpfyr3hf95uzp7rkj7d2lnc", amount: 67432714889},
	{address: "thor1628cc4m75t37canvm4nkpdejswvnlgyy3c2u2r", amount: 65967340068},
	{address: "thor1m4hc3fmtmw4sxprh7jxl63hjf2zfz7eagr4t22", amount: 65950492882},
	{address: "thor1sayaegqgmehfyk5qw67r0es9aa65st25kuem5p", amount: 64406903456},
	{address: "thor1u3z82a2j6jge3xq2qdxpgk536h4x3xsc3a30qn", amount: 64224907582},
	{address: "thor1qk52yas6gty2z6js7y564geh3pqq324ht08nzk", amount: 63403364250},
	{address: "thor19tl4glhxgh57x4s6ua85n7etahws3r4kzgnyp9", amount: 62468750903},
	{address: "thor170kk2ww8cra7la9lf4czwhhp3lld5xpp94ujsn", amount: 53149067258},
	{address: "thor10d0q4ectnwjc773cy8jm3jxn9248qaaxj5h7mp", amount: 52669011811},
	{address: "thor1swrnsj9uazq6m928pux302yfyddvm2s9qf8m87", amount: 49768886726},
	{address: "thor1kwvp4z2dqsqwy42ah4a83gnw8ww9nqwawq93zr", amount: 47128101879},
	{address: "thor12rtakf762ez8wad2ljz80hx3475ajqmdm6vxyu", amount: 47061289414},
	{address: "thor1ezk4udl9e3hr80lahytds8dwgf0jqregccm9nf", amount: 43316076711},
	{address: "thor1qt7xkl7vl708dwgqfag7995x6534jsmvsr8y75", amount: 39031633959},
	{address: "thor1rdljlrleylnv37c4adqg9hyjd0q3atuuj793gp", amount: 38072362612},
	{address: "thor10m98fvvg54zrz8dex4k5yc2cdzvp7hx2p5tz4u", amount: 36414582484},
	{address: "thor1p8y207hu3r6rz56u4urecdx6mcg6q6dvnthamy", amount: 35928724056},
	{address: "thor1glhq7vc4kjrml0mx3440k6ax5pxhkn63vtuyed", amount: 34594018418},
	{address: "thor1a9xxp66632zgf57gm84f67mwyr8q4atcl6f3ky", amount: 33087318029},
	{address: "thor1gyfuajr3emvnggf4z6ykvxq8yaueex40vk4nvk", amount: 32288955440},
	{address: "thor1zm34fnl4n0w7qjx82xprhlfyx3hfa78ye0nhjp", amount: 29376560659},
	{address: "thor1rxq5tkxtknevncqghgvkjvg2n7ulenlh008976", amount: 27337553062},
	{address: "thor1ahvyf66fxc605xvkkdkmndvhux3gg84glxhd0d", amount: 27158763936},
	{address: "thor1eza2ea44y6whndge7uswc20twtvge408ul3xew", amount: 26449317579},
	{address: "thor1e0hj0p6trsrw8v5dnfjgmsgeu77mnk34qkgfur", amount: 26247658543},
	{address: "thor1ny39rjaf5v4c39u2krev4zlqxc8hdqgu6wfy58", amount: 25592578051},
	{address: "thor1qjc9j2pf00ryneq9evg2cmr44zszex8arwxfhj", amount: 25333871601},
	{address: "thor1n3dc8j6x4762jkhw39gtqx8exx59p6huld9u43", amount: 23840584616},
	{address: "thor1c09hscv76wtv5w7zh7rhlmv6mxuwzs3h2mfklx", amount: 23594454775},
	{address: "thor1jfma6rrhyx7p9qg4hezu4m70e5ckq5jzeq8wkj", amount: 21680633441},
	{address: "thor1ys4eqwefvhj0n5ycs83gjuvnjup3vwscrm5u7l", amount: 21128494746},
	{address: "thor160h8g80xyjtvj5vdwnyd9qgzv6kdjsyly44dxu", amount: 20355039748},
	{address: "thor1xwqj94q8nwuajwkurv3h6cvwuqe6exy7448yd0", amount: 20262113141},
	{address: "thor1v02x3dww72jwqz8seux9jka8z08e8nq84y3vtg", amount: 20044187516},
	{address: "thor1vknzp0vd4gm3yxgmc2dvctupr2jtxpt4qv8ur0", amount: 18378515716},
	{address: "thor17ykncdkyf50ms6xgtwlyqkqhymh3pmlhyvqtur", amount: 17899407546},
	{address: "thor1rjcuffd5e2qdqdsy0x2ewdrr3pcsx0wmxym9at", amount: 17779841146},
	{address: "thor14k5l4lkzn9qt3vpq0ym9whavrahl2whumq3u2q", amount: 16734826373},
	{address: "thor17dhu5eceyenzv2vvyvaas6h93tuzm5azdgh3e9", amount: 16237534908},
	{address: "thor140dt2w7zm0elwcgfafg5d0y759ktvkkkfrh7al", amount: 16112063598},
	{address: "thor1lle55s4wvmgws9vzrrk944es5ratt0m6dp36xa", amount: 15692149342},
	{address: "thor1vnnj5zfpefsxp677xpjzjt3sjzekrjptqpd6wv", amount: 15492558376},
	{address: "thor1nh4vz0spt9l05pe3vhx4a8pyryta7td35lhy4a", amount: 14901088322},
	{address: "thor1jmfwwgtgtdcrd494hke9q84h3ndkptxg02lplc", amount: 14835194097},
	{address: "thor1sgg3zwudf9egf7xse484lty9l0rqp89lc8p5pn", amount: 14683255547},
	{address: "thor10aa9ksnvra2ah8yy8e509gd8u46m493xtjq90f", amount: 14624330315},
	{address: "thor1stgurhwxy7h2z85nwe6cvwz4gtwzzjfdxk460x", amount: 14274857992},
	{address: "thor1nvxu2d6pj0f40us04vlwv2g7qeey6c33f9fu6s", amount: 13960816765},
	{address: "thor19wrcwsaat3se7fmrmadntyzsxhr7m63e8a96wq", amount: 13749559489},
	{address: "thor16krhuvh6nr2z8mpsrmm7n637d5slwsfqn6w0tq", amount: 13425052599},
	{address: "thor1sarruusl0dgynw674tqv22q464x8vnjhk8klky", amount: 13420156041},
	{address: "thor1j28cscdr0v3e2x6q02q6w7pp958a9quq2uwmj5", amount: 12936428656},
	{address: "thor1ermk3wyrytu5jpt6vd5u7pr9h6r4g2ftyrpvte", amount: 10815655546},
	{address: "thor1yw5l6z2yrvvdnzrqfckw0tx5l9wa58pu8z0a58", amount: 10216164275},
	{address: "thor1k2r3rj5hxk89zmj2pltex0t0uyd5dvhv4klmex", amount: 9044928953},
	{address: "thor1d8s0mpr55m3x2qydx7z07cv4whl0cdret7zjqv", amount: 8689575783},
	{address: "thor1g6y726lv7xmjlpnpu7avjs40epr47dp30xd76u", amount: 8368432695},
	{address: "thor18sjyu3gtledfmzjendjk57nvdc2nnpm44sgkup", amount: 8288049790},
	{address: "thor1qm4n0lqtceq92deysxd7z3avaptwpn3fk2m8m0", amount: 8279381797},
	{address: "thor1xxfmgsglhat0w32rpr3nstrf7vnrxt8w6w0y8u", amount: 8143229385},
	{address: "thor1n2pv2t03pn7kr7ypsvkyp2nce9p57v0pcv39tp", amount: 8099961599},
	{address: "thor1dvfnvrwy7lf049yfcqutcf7pgf7zxg5ydnq7jm", amount: 8055544787},
	{address: "thor1t25s4s9flvw8yed2anlr3wjepj4wtvwnv0f8c7", amount: 7641063398},
	{address: "thor1atfrhw6ldw9rsa4dp0agfdkfmgdc4n53hz7lr9", amount: 7472472880},
	{address: "thor193yv4v40w4s53gphh20r93c74mvurfhmv93kp8", amount: 7229905212},
	{address: "thor12axd9kfjhv2fmzjyudtvcxy9zuv4n5zxrv6f2p", amount: 7207618595},
	{address: "thor195tj2rmnmfg3nu6g9wyf3uu9lrr49duydduadz", amount: 7174484270},
	{address: "thor1fzav6wrppld4vclqjpadmw0wr9n763nnutwwxd", amount: 7012745524},
	{address: "thor1uee88s7uu9mc9q4f5acpgdk6wnjz7ztak45fw9", amount: 6997580232},
	{address: "thor16v5heheenrdcpx9dzqk04d9txsvznv5gaz8zay", amount: 6874633555},
	{address: "thor1d7js5mrfguf7aj7djsm5wekk9620utr3z3uy08", amount: 6848478300},
	{address: "thor1puua7z3gffejwel3e08exe26pdr2mkf67ujp82", amount: 6761511841},
	{address: "thor1hxdtc70azpesae2gg37wwlwk6c8ljdcxh43l87", amount: 6700350391},
	{address: "thor1wk8x2kk6gftkyn7psukktux02acak06y8wfpk5", amount: 6663826889},
	{address: "thor1hj3ts525ehk9e64thckl89lc5pfw6g0p6kh6uc", amount: 6586375996},
	{address: "thor1276tnclzurjfl4pq98rzgv8unh4384xw9keckg", amount: 6194868109},
	{address: "thor16sv556dqy3u6llu9nyr8l0pqj7uqf2mf4u82ur", amount: 6123226243},
	{address: "thor16ltxavn8v2e8uwkc368wf0f4pv68kzv8ltf9rd", amount: 5939873455},
	{address: "thor1nv6l96t53evvkj850f9rf9fpqeewrqtrk8wwnp", amount: 5707413737},
	{address: "thor1nmrau8950uvs80n80p3r5hrs2n0welvz5qnee4", amount: 5684095430},
	{address: "thor1qmag2ln8htpqpkg263pcs0spxdcjjthznz77wh", amount: 5637579998},
	{address: "thor1tkunqcw7mgupa3n2vyz79ymv6f98nf5anq64m4", amount: 5217217284},
	{address: "thor1nk0rc3s2v9szgnaaprguwyg53msp49yr28v62l", amount: 5208646464},
	{address: "thor1pxktgvrh6cm2f2y2dwu7n0hw6x8z32wj2wxkmw", amount: 5173658927},
	{address: "thor1843x94m7qvr3vzg9mhwj5en2zz2fwearxzvz8z", amount: 5094487149},
	{address: "thor1gjk09ghsu07cplc9ztj64z37mvqj3qwxpu9rfq", amount: 4802468812},
	{address: "thor1guupnvedns0te5jppzwc0ncwdp7j883edxv7v5", amount: 4008106847},
	{address: "thor1gutscy5a6t7ehfr06p089ch97fy9zyrylp6f6c", amount: 3960267143},
	{address: "thor17kxkpzkxw6cgewq4hzkr6hd6f72e7wguxugmpp", amount: 3924592408},
	{address: "thor1sdqjuvq77x89glt4n3jjajxclyk8v3z0s6l0zh", amount: 3833876646},
	{address: "thor196u2z6allf0ylgfa2053crvwqkcwrlmnsv3nn5", amount: 3809563324},
	{address: "thor1cru3ux6qjfaz6anfc7s9jef64qye0vmp88f08c", amount: 3708798524},
	{address: "thor1lvylqr4t4362x4z4felcnm9he5tmkawf9uq00f", amount: 3708798524},
	{address: "thor1ujw5u5fys3yrpnf765jxptjwnnkc7aln4l4aev", amount: 3701729061},
	{address: "thor1qqd2wxt20w44h3m472f9v8hccfuuhzh28jzxgx", amount: 3698379966},
	{address: "thor1hwk7m7snhfq8dyg0wzsl3m4cpsz0pfh2klra67", amount: 3678044172},
	{address: "thor1c7pcyruzga8wgaezku6c2lct9vckhkmh5huyxf", amount: 3669075768},
	{address: "thor12mrysuw7ryjhkynn3w7lftp5t3z5g7f59wsz26", amount: 3656082579},
	{address: "thor1fkwmkl96zpl93a547arcc8e00n5zh7eegdq5tv", amount: 3656082579},
	{address: "thor12ekyxdflyrdl8wy2cp3sjj4czcyvytv27e9095", amount: 3635109557},
	{address: "thor1sx6pj92nsr2u8hlknde2w8f38hrjj2jketg2q4", amount: 3588181580},
	{address: "thor1sd8lwwedynx0ysnkmt7ajudelj6rkr5h45469d", amount: 3293258531},
	{address: "thor19qaa3welgj2fmplwha2jnhwm36lktwxedcjkgw", amount: 3137538977},
	{address: "thor1f47n9fth6xy7jhpnyk9rts0p8fygax4dyykzu9", amount: 2948416553},
	{address: "thor160922xp5qahs0mgv8ppzdre6lcxzzexmm43en6", amount: 2924866063},
	{address: "thor16wjfedsp5tnqqaw9nd465nmyfgwjml68nr7eqv", amount: 2804926427},
	{address: "thor15q0ayttll0s7j03atrvead656wdr4wm68ngquc", amount: 2757218636},
	{address: "thor1dgj302ks55k3tmu0cv7uh8cemn0pxp2j2a069a", amount: 2641370534},
	{address: "thor1p86hcrxydywm07tzrqytrsftqcj72rrcvvxjjt", amount: 2535089001},
	{address: "thor17xnq4phyl76pr0e4n08wj6920mmsnf2jnn3tdj", amount: 2487632028},
	{address: "thor1kal4tylc9ameqmk430q8j044edfxg0xf7mkp8t", amount: 2255061438},
	{address: "thor1cns3mynul4mqs6mrp0wjke5pv5ya0yz0kl6mwv", amount: 2249041935},
	{address: "thor1cffnl990rc25zq28memthrgw8wjl6xkryz0rle", amount: 2233411312},
	{address: "thor1upwg9x2mndnhmrptckv4hs7gfy9eysev0gjq0h", amount: 2198768063},
	{address: "thor1q44tshquqqzga8vqaalam60asyt09um5w94f2a", amount: 2197471591},
	{address: "thor133g0mey8dh7wpc4ax7pc6hp4dappgq405t845y", amount: 2191099371},
	{address: "thor12ptyq5336rfjdmasuv63gr4lqtnsdnp65l0sss", amount: 2154250561},
	{address: "thor10hzccmfhhyjj37j0wnrk2mlkz72ky5fs97vt85", amount: 2115796925},
	{address: "thor1zqwpphzyz2wetpelzkt8na3szyr3fttrtmwm4n", amount: 1998999225},
	{address: "thor1qwrnltz2dz6djhdf57n0n9hk75zpnk5dafl8zn", amount: 1970225922},
	{address: "thor1tsqwwpzwzw2j2eet3vek6p8f5lvcwuprh69g6x", amount: 1958102854},
	{address: "thor133rp2lldnyphw6g7340yw58q45tal5mwxc0j8u", amount: 1913212811},
	{address: "thor1nzdk2qn9v2g52dnreyk2sd2hdtw58cnek3qtzz", amount: 1733467307},
	{address: "thor13f2f8rryrjrk4pacyq8pl45m8432npr306trc8", amount: 1707901203},
	{address: "thor122gdxpn255dy0mgh27cehvl3t4f75gr9gthmv7", amount: 1675518718},
	{address: "thor1p2xm606cx6rem24x906wv839v2wh20sjzr5m4l", amount: 1666204805},
	{address: "thor1vpsdc5dku6nmd05uh8nxdxyy7x002yhedyp5pe", amount: 1640849861},
	{address: "thor12qc72x2eueqq7tkdcwlgu8j5ypvsrhnzsesrnl", amount: 1594203499},
	{address: "thor1m46xz9v224v4ng5ka3wrwnc8n5qnk0fw8rgq2a", amount: 1531857610},
	{address: "thor107mf2ddmfux260yxnz8ejnqk2dpjjgslj9r4cu", amount: 1497140637},
	{address: "thor1nyuf4478qs055rnud9jpszy5qxa8z7mz8wjdqn", amount: 1494100582},
	{address: "thor1r9eashc2m2c62fxqhk6vkr4ff95gdyrn0ph6sx", amount: 1490633510},
	{address: "thor1enj8nn562ne7tq7duvsu5g20xh4nkhghn6j7rg", amount: 1482529685},
	{address: "thor1z88v4lfwnxkz2n3ch7fp63jdq3c42vmrghran7", amount: 1470941717},
	{address: "thor1zrgffn6r06q0gdze2fxsvxlgycypunr90q65d8", amount: 1468234320},
	{address: "thor1clk59pvw4hgww53xg2kdncpg7ffd28re7wtw0u", amount: 1452922736},
	{address: "thor1xu25l5cqg6ymtamvfk0sh5nttdhtxd78mt899t", amount: 1421709542},
	{address: "thor1xk5t4w6fqgnkkg6knuggz00cajmmnkmlepepfr", amount: 1381267998},
	{address: "thor1fxf5lduvas2wha7gxmwdnerren6u2r25gzqaqz", amount: 1380859500},
	{address: "thor1hwz2yn6uqg9ws4mvcfpqgsvlxjuyd9e696asj8", amount: 1327826004},
	{address: "thor1x4vsmyyg288al325czuk6fsnkkva5kp4s4q9g8", amount: 1293484626},
	{address: "thor19te78fx7s5zazuugy0nd7xxhnx68kuzps3v2cr", amount: 1189250754},
	{address: "thor1sd2a3jfm05pds2tyuxpgy6kv8gqqq8leh5y90m", amount: 1169946425},
	{address: "thor1wxsekaaftys8d6amklrupnja3sufjrvpkaeey0", amount: 1167199727},
	{address: "thor1c6utwdvav243vvp6pt3tljfqtm89ttw4jgjega", amount: 1084266616},
	{address: "thor1zwvx53qgaehsdwtw95jgnmdlvp8sm2s8haqlfy", amount: 964416741},
	{address: "thor1xcvtqtuug5u3txjyqhmck9hl8pf0dmqavdaamh", amount: 906708480},
	{address: "thor1mcw5rzya7uml0wt3zwmj6xvr3nsev05e22uqmh", amount: 877459819},
	{address: "thor19yhlwuuxyvanksjhsee7rwu9wt9eqs0egpvxu8", amount: 815935675},
	{address: "thor1s955zjphxl6r3lj24358s4ukryzmmjrn23jdxk", amount: 787588470},
	{address: "thor1qxxmvhz4ruw8r38yasy752w2zk9dhr4uygtley", amount: 782582593},
	{address: "thor1rwaamxsva8tz4nz83ghm8hta2uznctnyrv0nzn", amount: 753366554},
	{address: "thor16qvzrjpdccp3hrhl46ty4k2ngexav466tlq88n", amount: 753116583},
	{address: "thor102cq5acm89g3lrj9vea3egq256uc5akmeh0086", amount: 753116482},
	{address: "thor19ceemm5xnuhusg0vuy9rwqr7dpfz935tk7nc5x", amount: 753116482},
	{address: "thor1h4pgv3wnzzh9e3c8xtl2da7ywmcsa6k0vsuqvc", amount: 753116482},
	{address: "thor165u6emtswrd25xju64mgf0r5h8gyzx53adae5s", amount: 752477888},
	{address: "thor1nh9e499j7scns300atewsnlp590x6k7u7g5pgk", amount: 752477888},
	{address: "thor1q7qlqecwf0y4t4hcfqr966y0ggzgfvhwadp00a", amount: 751231190},
	{address: "thor1wzesj9ch3l07nmz0jgnsl9k2mapm7kq7jquyaz", amount: 751231190},
	{address: "thor17vu7spm4r5rcefjrwq5sng3lmnfpmqvgucqqmj", amount: 748143901},
	{address: "thor1u42u4nfpcszgt7d6e072g4mk8h28rccqv3lvdv", amount: 746244808},
	{address: "thor14hpzqlm0tarv9p8mqxu9hg0qt4ecleyzmt2stw", amount: 741759705},
	{address: "thor120vexx8hq06d5nc38e5up9m92jc6hflaum6dfu", amount: 731947732},
	{address: "thor1app08t8svfhkpsrdpg2us959psuqfxuhf59n8c", amount: 731216516},
	{address: "thor1j8k9syzszvmh9hecar0hd9f0k95xahshyh33wa", amount: 712274083},
	{address: "thor1tkn7706tp2js0j4lsgemttmgwt2jym8kngs7en", amount: 709209528},
	{address: "thor1v83nu92g8eu586afxvgvfj74wy9jh6d4wqnk9h", amount: 701433239},
	{address: "thor1ktx5xk5wqs0vzsmdkcrysxmtvssp0zrsgdmv6w", amount: 389917261},
	{address: "thor1dpc3jzxg0jwp5q4rzk0xp334stuas2t47vncc0", amount: 328361012},
	{address: "thor1n9gcud325e25ywe9sycwwh6pqgalq03e5mj7dg", amount: 92971812},
	{address: "thor1tfm4q8u57qzsznpvh02s8j483aga63cl02k6jt", amount: 17019944},
	{address: "thor15ath5vmj35ggjen9n56a9q4pscgj8pt4d8l5t2", amount: 8546644},
	{address: "thor16sa3k77lffrdqm854djjr9lgzv777sadrhpgtt", amount: 7189497},
	{address: "thor1mpdccj4wr88v2jw28cw2cqluqqcrkux6n45n2m", amount: 6451276},
	{address: "thor1uwpshu52te8n36mk8hfy3ejjr64jma2s6wsmfq", amount: 5041410},
	{address: "thor1yxkdmxt2cu5syjx408gahym8k0qcyp50cyzk28", amount: 3632836},
	{address: "thor1wmrse405fdq8yk3xa5alxnfw6yjndk3mvn87dp", amount: 1600732},
	{address: "thor1e2xrqpxuy2qxg2eeukqahlkwxrjjlkewyqlwp2", amount: 460470},
	{address: "thor1wrc6754h83d9eg00vpt5a9l0r7eq273aed9tcy", amount: 304128},
	{address: "thor1ewpf8uwamtrndrvhtrn90a2rmhgw0lquw90ewl", amount: 266897},
	{address: "thor1upuw8yvg96uf4nd6jfuq9s25284dzr45kyzarc", amount: 121632},
	{address: "thor1drm88nvzn8qclrddac876ectt548cscgukm62d", amount: 41279},
	{address: "thor1sns9plez7g9p4k04am0jqz2lgy5ast2x8pv2j5", amount: 34362},
	{address: "thor1fnu8jh8knfhjyaf462jadw0cd2pd876nyp0qjn", amount: 21027},
	{address: "thor1ss9txkzallwm83u5ey6wh4a559v5j8q6df3xz7", amount: 10800},
	{address: "thor1s37zclh4rg0aguyy26sxqh5qn8zlj7yhvedvys", amount: 6155},
	{address: "thor1rz8qq9aqakytxp9e00sdvc5zzv9dehvv2z67z4", amount: 3780},
	{address: "thor1l330td7tk2uckx3gmyryr7gsxs45q2tk3vxm7q", amount: 3123},
	{address: "thor18uquyh2nl9chgxe63erhf68e0v9mwwwwqv9g3l", amount: 2300},
	{address: "thor1692vpzvunjqn5hj82g28r925e4t6lec482zrhh", amount: 433},
	{address: "thor19yufynkuf8n9385xh4pjge6faxc0dtlacj8fnu", amount: 95},
	{address: "thor147l32hkwev7vnv506q5sepazr6zt88zt3eylyt", amount: 7},
}

const mainnetSlashRefunds19to20Total = uint64(11354727112439)
