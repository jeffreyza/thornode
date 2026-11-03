package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TCYStakeHandler to process withdraw requests
type TCYStakeHandler struct {
	mgr Manager
}

// NewTCYStakeHandler create a new instance of TCYStakeHandler to process withdraw request
func NewTCYStakeHandler(mgr Manager) TCYStakeHandler {
	return TCYStakeHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of withdraw
func (h TCYStakeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgTCYStake)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgTCYStake failed validation", "error", err)
		return nil, err
	}
	ctx.Logger().Info("receive MsgTCYStake", "address", msg.Tx.FromAddress, "amount", msg.Tx.Coins[0].Amount)

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process msg tcy stake", "error", err)
		return nil, err
	}
	return result, err
}

func (h TCYStakeHandler) validate(ctx cosmos.Context, msg MsgTCYStake) error {
	if err := msg.ValidateBasic(); err != nil {
		return errTCYStakeFailValidation
	}
	stakingHalt := h.mgr.Keeper().GetConfigInt64(ctx, constants.TCYStakingHalt)
	if stakingHalt > 0 {
		return fmt.Errorf("tcy staking is halted")
	}

	// The keeper stores no staker record for TCY smart contract addresses, so staking
	// from one would move the TCY into the TCYStake module with nothing to credit it to.
	// UpdateTCYStaker refuses it too; rejecting here fails the deposit up front, as the
	// sibling claim handler does.
	if tcysmartcontract.IsTCYSmartContractAddress(msg.Tx.FromAddress) {
		return fmt.Errorf("cannot stake from TCY smart contract address: %s", msg.Tx.FromAddress)
	}

	return nil
}

func (h TCYStakeHandler) handle(ctx cosmos.Context, msg MsgTCYStake) (*cosmos.Result, error) {
	ctx.Logger().Info("staking tcy claim", "address", msg.Tx.FromAddress.String(), "amount", msg.Tx.Coins[0].Amount.String())

	err := h.mgr.Keeper().UpdateTCYStaker(ctx, msg.Tx.FromAddress, msg.Tx.Coins[0].Amount)
	if err != nil {
		return nil, fmt.Errorf("failed to update tcy staker: %w", err)
	}

	evt := types.NewEventTCYStake(msg.Tx.FromAddress, msg.Tx.Coins[0].Amount)
	return &cosmos.Result{}, h.mgr.EventMgr().EmitEvent(ctx, evt)
}
