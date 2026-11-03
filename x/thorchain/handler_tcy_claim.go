package thorchain

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TCYClaimHandler to process withdraw requests
type TCYClaimHandler struct {
	mgr Manager
}

// NewTCYClaimHandler create a new instance of TCYClaimHandler to process withdraw request
func NewTCYClaimHandler(mgr Manager) TCYClaimHandler {
	return TCYClaimHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of withdraw
func (h TCYClaimHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgTCYClaim)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive MsgTCYClaim", "rune_address", msg.RuneAddress, "l1_address", msg.L1Address)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgTCYClaim failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process msg tcy claim", "error", err)
		return nil, err
	}
	return result, err
}

func (h TCYClaimHandler) validate(ctx cosmos.Context, msg MsgTCYClaim) error {
	if err := msg.ValidateBasic(); err != nil {
		return errTCYClaimFailValidation
	}
	claimingHalt := h.mgr.Keeper().GetConfigInt64(ctx, constants.TCYClaimingHalt)
	if claimingHalt > 0 {
		return fmt.Errorf("tcy claiming is halted")
	}

	if !msg.RuneAddress.IsChain(common.THORChain) {
		return cosmos.ErrUnknownRequest("invalid rune address")
	}

	// reject smart contract addresses to prevent deposits to non-retrievable addresses
	if tcysmartcontract.IsTCYSmartContractAddress(msg.RuneAddress) {
		return fmt.Errorf("cannot claim to TCY smart contract address: %s", msg.RuneAddress)
	}

	return nil
}

func (h TCYClaimHandler) handle(ctx cosmos.Context, msg MsgTCYClaim) (*cosmos.Result, error) {
	claimingTCYBalance := h.mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	if claimingTCYBalance.IsZero() {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "tcy", "claim_failed"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", "no_module_funds")},
		)
		return &cosmos.Result{}, fmt.Errorf("claiming module doesn't have tcy funds")
	}

	claims, err := h.mgr.Keeper().ListTCYClaimersFromL1Address(ctx, msg.L1Address)
	if err != nil {
		return &cosmos.Result{}, err
	}

	for _, claim := range claims {
		if claim.Amount.IsZero() {
			ctx.Logger().Info("claimer doesn't have tcy to claim", "address", claim.L1Address.String(), "asset", claim.Asset.String())
			continue
		}

		// Recheck module balance before each transfer to prevent double-spend
		currentBalance := h.mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
		if currentBalance.LT(claim.Amount) {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "tcy", "claim_failed"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "insufficient_module_balance")},
			)
			return &cosmos.Result{}, fmt.Errorf("insufficient module balance for claim: balance %s, claim_amount %s", currentBalance, claim.Amount)
		}

		ctx.Logger().Info("staking tcy claim", "l1_address", claim.L1Address.String(), "rune_address", msg.RuneAddress.String(), "amount", claim.Amount, "asset", claim.Asset.String())
		coin := common.NewCoin(common.TCY, claim.Amount)
		err = h.mgr.Keeper().SendFromModuleToModule(ctx, TCYClaimingName, TCYStakeName, common.NewCoins(coin))
		if err != nil {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "tcy", "claim_failed"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "send_failed")},
			)
			return &cosmos.Result{}, fmt.Errorf("failed to send from claiming to staking module: %w", err)
		}

		err = h.mgr.Keeper().UpdateTCYStaker(ctx, msg.RuneAddress, claim.Amount)
		if err != nil {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "tcy", "claim_failed"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "update_staker_failed")},
			)
			return &cosmos.Result{}, fmt.Errorf("failed to update tcy staker: %w", err)
		}

		h.mgr.Keeper().DeleteTCYClaimer(ctx, claim.L1Address, claim.Asset)

		evt := types.NewEventTCYClaim(msg.RuneAddress, msg.L1Address, claim.Amount, claim.Asset)
		if err = h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("fail to emit tcy claim event", "error", err)
		}
	}

	return &cosmos.Result{}, nil
}
