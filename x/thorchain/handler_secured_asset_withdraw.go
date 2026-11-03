package thorchain

import (
	"fmt"

	"github.com/hashicorp/go-multierror"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// SecuredAssetWithdrawHandler is handler to process MsgSecuredAssetWithdraw
type SecuredAssetWithdrawHandler struct {
	mgr Manager
}

// NewSecuredAssetWithdrawHandler create a new instance of SecuredAssetWithdrawHandler
func NewSecuredAssetWithdrawHandler(mgr Manager) SecuredAssetWithdrawHandler {
	return SecuredAssetWithdrawHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for SecuredAssetWithdrawHandler
func (h SecuredAssetWithdrawHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgSecuredAssetWithdraw)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSecuredAssetWithdraw failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgSecuredAssetWithdraw", "error", err)
	}
	return &cosmos.Result{}, err
}

func (h SecuredAssetWithdrawHandler) validate(ctx cosmos.Context, msg MsgSecuredAssetWithdraw) error {
	m, err := h.mgr.Keeper().GetMimirWithRef(ctx, constants.MimirTemplateSecuredAssetHaltWithdraw, msg.Asset.Chain.String())
	if err != nil {
		return err
	}
	if m > 0 && m <= ctx.BlockHeight() {
		return fmt.Errorf("%s secured asset withdrawals are disabled", msg.Asset.Chain)
	}

	// Validate that the address network matches the current chain network
	if !msg.AssetAddress.IsCurrentNetwork(msg.AssetAddress.GetChain()) {
		return fmt.Errorf("address(%s) is not same network", msg.AssetAddress)
	}

	return msg.ValidateBasic()
}

// handle process MsgSecuredAssetWithdraw
func (h SecuredAssetWithdrawHandler) handle(ctx cosmos.Context, msg MsgSecuredAssetWithdraw) error {
	if isStableReserveWithdrawMemo(msg.Tx.Memo) {
		if len(msg.Tx.Coins) != 1 {
			return cosmos.ErrInvalidCoins("only send 1 coins per message")
		}

		requestCoin := msg.Tx.Coins[0]
		if requestCoin.Amount.GT(cosmos.SafeUintFromInt64(h.mgr.Keeper().GetConfigInt64(ctx, constants.StableReserveWithdrawalRequestMax))) {
			// Reject oversized request coins directly; StableReserveManager.Withdraw
			// re-checks this same condition, so calling it just to surface the error
			// is redundant.
			return fmt.Errorf("stable reserve withdrawal request coin too large")
		}

		withdrawAmount, filled, err := h.mgr.StableReserveManager().Withdraw(ctx, msg.Asset.GetLayer1Asset(), msg.Amount, msg.Tx.FromAddress, msg.Tx.ID)
		if err != nil {
			return err
		}
		if !filled {
			return fmt.Errorf("stable reserve withdrawal unfilled")
		}

		// Charge the request-coin fee only after the withdrawal is confirmed
		// filled. Scheduling it before the Withdraw call committed the fee swap
		// even on the unfilled (filled==false, nil-error) path — charging the
		// contributor for a no-op they must then retry and pay for again.
		if err = scheduleStableReserveWithdrawalFeeSwap(ctx, h.mgr, msg, requestCoin); err != nil {
			return err
		}

		return addSecuredAssetWithdrawOutbound(ctx, h.mgr, msg, withdrawAmount)
	}

	withdrawAmount, err := h.mgr.SecuredAssetManager().Withdraw(ctx, msg.Asset, msg.Amount, msg.Signer, msg.AssetAddress, msg.Tx.ID)
	if err != nil {
		return err
	}

	return addSecuredAssetWithdrawOutbound(ctx, h.mgr, msg, withdrawAmount)
}

func addSecuredAssetWithdrawOutbound(ctx cosmos.Context, mgr Manager, msg MsgSecuredAssetWithdraw, withdrawAmount common.Coin) error {
	toi := TxOutItem{
		Chain:     withdrawAmount.Asset.GetChain(),
		InHash:    msg.Tx.ID,
		ToAddress: msg.AssetAddress,
		Coin:      withdrawAmount,
	}

	ok, err := mgr.TxOutStore().TryAddTxOutItem(ctx, mgr, toi, cosmos.ZeroUint())
	if err != nil {
		return multierror.Append(errFailAddOutboundTx, err)
	}
	if !ok {
		return errFailAddOutboundTx
	}

	return nil
}

func scheduleStableReserveWithdrawalFeeSwap(ctx cosmos.Context, mgr Manager, msg MsgSecuredAssetWithdraw, requestCoin common.Coin) error {
	if requestCoin.Amount.IsZero() {
		return nil
	}

	reserveAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	if err != nil {
		return err
	}

	feeTx := msg.Tx
	feeTx.ID = stableReserveSyntheticTxID(ctx, "withdraw-fee-"+msg.Tx.ID.String(), requestCoin.Asset)
	feeTx.Coins = common.NewCoins(requestCoin)
	feeTx.Memo = types.StableReserveWithdrawFeeMemoPrefix + msg.Tx.ID.String()
	feeSwap := NewMsgSwap(feeTx, common.RuneAsset(), reserveAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, msg.Signer)
	return mgr.Keeper().SetSwapQueueItem(ctx, *feeSwap, 0)
}
