package thorchain

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// RunePoolWithdrawHandler a handler to process withdrawals from RunePool
type RunePoolWithdrawHandler struct {
	mgr Manager
}

// NewRunePoolWithdrawHandler create new RunePoolWithdrawHandler
func NewRunePoolWithdrawHandler(mgr Manager) RunePoolWithdrawHandler {
	return RunePoolWithdrawHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h RunePoolWithdrawHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgRunePoolWithdraw)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info(
		"receive MsgRunePoolWithdraw",
		"tx_id", msg.Tx.ID,
		"signer", msg.Signer,
		"basis_points", msg.BasisPoints,
		"affiliate_address", msg.AffiliateAddress,
		"affiliate_basis_points", msg.AffiliateBasisPoints,
	)

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg rune pool withdraw failed validation", "error", err)
		return nil, err
	}

	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process msg rune pool withdraw", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h RunePoolWithdrawHandler) validate(ctx cosmos.Context, msg MsgRunePoolWithdraw) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	runePoolEnabled := h.mgr.Keeper().GetConfigInt64(ctx, constants.RUNEPoolEnabled)
	if runePoolEnabled <= 0 {
		// thornode_rune_pool_withdraw_count only counts successes, so a total
		// withdraw outage is otherwise indistinguishable from no demand.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "rune_pool", "withdraw_rejected"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", "disabled")},
		)
		return fmt.Errorf("RUNEPool disabled")
	}
	runePoolWithdrawPaused := h.mgr.Keeper().GetConfigInt64(ctx, constants.RUNEPoolHaltWithdraw)
	if runePoolWithdrawPaused > 0 && ctx.BlockHeight() >= runePoolWithdrawPaused {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "rune_pool", "withdraw_rejected"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", "paused")},
		)
		return fmt.Errorf("RUNEPool withdraw paused")
	}
	maxAffBasisPts := h.mgr.Keeper().GetConfigInt64(ctx, constants.MaxAffiliateFeeBasisPoints)
	if !msg.AffiliateBasisPoints.IsZero() && msg.AffiliateBasisPoints.GT(cosmos.NewUint(uint64(maxAffBasisPts))) {
		return fmt.Errorf("invalid affiliate basis points, max: %d, request: %d", maxAffBasisPts, msg.AffiliateBasisPoints.Uint64())
	}
	return nil
}

func (h RunePoolWithdrawHandler) handle(ctx cosmos.Context, msg MsgRunePoolWithdraw) error {
	accAddr, err := cosmos.AccAddressFromBech32(msg.Signer.String())
	if err != nil {
		return fmt.Errorf("unable to AccAddressFromBech32: %s", err)
	}
	runeProvider, err := h.mgr.Keeper().GetRUNEProvider(ctx, accAddr)
	if err != nil {
		return fmt.Errorf("unable to GetRUNEProvider: %s", err)
	}

	// ensure the deposit has reached maturity
	depositMaturity := h.mgr.Keeper().GetConfigInt64(ctx, constants.RUNEPoolDepositMaturityBlocks)
	currentBlockHeight := ctx.BlockHeight()
	blocksSinceLastDeposit := currentBlockHeight - runeProvider.LastDepositHeight
	if blocksSinceLastDeposit < depositMaturity {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "rune_pool", "withdraw_rejected"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", "immature")},
		)
		return fmt.Errorf("deposit reaches maturity in %d blocks", depositMaturity-blocksSinceLastDeposit)
	}

	// rune pool tracks the reserve and pooler unit shares of pol
	runePool, err := h.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return fmt.Errorf("fail to get rune pool: %s", err)
	}

	// compute withdraw units
	maxBps := cosmos.NewUint(constants.MaxBasisPts)
	withdrawUnits := common.GetSafeShare(msg.BasisPoints, maxBps, runeProvider.Units)

	totalRunePoolValue, err := runePoolValue(ctx, h.mgr)
	if err != nil {
		return fmt.Errorf("fail to get rune pool value: %w", err)
	}

	// determine the profit of the withdraw amount to share with affiliate
	affiliateAmount := cosmos.ZeroUint()
	if !msg.AffiliateBasisPoints.IsZero() {
		totalUnits := runePool.TotalUnits()
		currentValue := common.GetSafeShare(runeProvider.Units, totalUnits, totalRunePoolValue)
		withdrawValue := common.GetSafeShare(msg.BasisPoints, maxBps, currentValue)

		// Compute yield proportionally using lifetime PnL to avoid overcounting.
		// The previous approach (SafeSub(DepositAmount, WithdrawAmount) as remaining
		// principal) is incorrect because WithdrawAmount includes yield, causing
		// depositRemaining to understate remaining principal after profitable
		// partial withdrawals. Instead, distribute the lifetime PnL proportionally
		// across total value generated (current value + all past withdrawals).
		totalValue := currentValue.Add(runeProvider.WithdrawAmount)
		lifetimePnL := common.SafeSub(totalValue, runeProvider.DepositAmount)
		withdrawYield := common.GetSafeShare(lifetimePnL, totalValue, withdrawValue)
		affiliateAmount = common.GetSafeShare(msg.AffiliateBasisPoints, maxBps, withdrawYield)
	}

	// compute withdraw amount
	withdrawAmount := common.GetSafeShare(withdrawUnits, runePool.TotalUnits(), totalRunePoolValue)

	// if insufficient pending units, reserve should enter to create space for withdraw
	pendingRune := h.mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)
	if withdrawAmount.GT(pendingRune) {
		reserveEnterRune := common.SafeSub(withdrawAmount, pendingRune)

		// There may be cases where providers are in a state of profit, and for the reserve
		// to buy their share of POL it must exceed POLMaxNetworkDeposit to cover the profit
		// of the provider. We allow exceeding this limit up to RUNEPoolMaxReserveBackstop
		// beyond the POLMaxNetworkDeposit as a circuit breaker. If the circuit breaker is
		// reached, withdraws will fail pending governance to increase the limit or extend
		// logic to trigger POL withdraw and sacrifice pool depth to satisfy withdrawals.
		maxReserveBackstop := h.mgr.Keeper().GetConfigInt64(ctx, constants.RUNEPoolMaxReserveBackstop)
		polMaxNetworkDeposit := h.mgr.Keeper().GetConfigInt64(ctx, constants.POLMaxNetworkDeposit)
		maxReserveUsage := cosmos.NewInt(maxReserveBackstop).Add(cosmos.NewInt(polMaxNetworkDeposit))
		var pol ProtocolOwnedLiquidity
		pol, err = h.mgr.Keeper().GetPOL(ctx)
		if err != nil {
			return fmt.Errorf("fail to get POL: %w", err)
		}
		currentReserveDeposit := pol.CurrentDeposit().
			Sub(runePool.CurrentDeposit()).
			Add(cosmos.NewIntFromBigInt(pendingRune.BigInt()))
		newReserveDeposit := currentReserveDeposit.Add(cosmos.NewIntFromBigInt(reserveEnterRune.BigInt()))
		if newReserveDeposit.GT(maxReserveUsage) {
			// The documented circuit breaker: once tripped every withdraw for
			// every provider fails, with no event and no metric until now.
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "rune_pool", "withdraw_rejected"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "reserve_backstop")},
			)
			return fmt.Errorf("reserve enter %d rune exceeds backstop", reserveEnterRune.Uint64())
		}

		err = reserveEnterRUNEPool(ctx, h.mgr, reserveEnterRune)
		if err != nil {
			return fmt.Errorf("fail to reserve enter rune pool: %w", err)
		}

		// fetch rune pool after reserve enter for updated units
		runePool, err = h.mgr.Keeper().GetRUNEPool(ctx)
		if err != nil {
			return fmt.Errorf("fail to get rune pool: %w", err)
		}
	}

	// update provider and rune pool records
	runeProvider.Units = common.SafeSub(runeProvider.Units, withdrawUnits)
	runeProvider.WithdrawAmount = runeProvider.WithdrawAmount.Add(withdrawAmount)
	runeProvider.LastWithdrawHeight = ctx.BlockHeight()
	h.mgr.Keeper().SetRUNEProvider(ctx, runeProvider)
	runePool.PoolUnits = common.SafeSub(runePool.PoolUnits, withdrawUnits)
	runePool.RuneWithdrawn = runePool.RuneWithdrawn.Add(withdrawAmount)
	h.mgr.Keeper().SetRUNEPool(ctx, runePool)

	// send the affiliate fee
	userAmount := common.SafeSub(withdrawAmount, affiliateAmount)
	if !affiliateAmount.IsZero() {
		affiliateCoins := common.NewCoins(common.NewCoin(common.RuneNative, affiliateAmount))
		var affiliateAddress cosmos.AccAddress
		affiliateAddress, err = msg.AffiliateAddress.AccAddress()
		if err != nil {
			return fmt.Errorf("fail to get affiliate address: %w", err)
		}
		err = h.mgr.Keeper().SendFromModuleToAccount(ctx, RUNEPoolName, affiliateAddress, affiliateCoins)
		if err != nil {
			return fmt.Errorf("fail to send affiliate fee: %w", err)
		}
	}

	// send the user the withdraw
	userCoins := common.NewCoins(common.NewCoin(common.RuneNative, userAmount))
	err = h.mgr.Keeper().SendFromModuleToAccount(ctx, RUNEPoolName, msg.Signer, userCoins)
	if err != nil {
		return fmt.Errorf("fail to send user withdraw: %w", err)
	}

	ctx.Logger().Info(
		"runepool withdraw",
		"address", msg.Signer,
		"units", withdrawUnits,
		"amount", userAmount,
		"affiliate_amount", affiliateAmount,
	)

	withdrawEvent := NewEventRUNEPoolWithdraw(
		runeProvider.RuneAddress,
		int64(msg.BasisPoints.Uint64()),
		withdrawAmount,
		withdrawUnits,
		msg.Tx.ID,
		msg.AffiliateAddress,
		int64(msg.AffiliateBasisPoints.Uint64()),
		affiliateAmount,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, withdrawEvent); err != nil {
		ctx.Logger().Error("fail to emit rune pool withdraw event", "error", err)
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "rune_pool", "withdraw_count"},
		float32(1),
		[]metrics.Label{},
	)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "rune_pool", "withdraw_amount"},
		telem(withdrawEvent.RuneAmount),
		[]metrics.Label{},
	)

	return nil
}
