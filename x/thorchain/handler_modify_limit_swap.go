package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// ModifyLimitSwapHandler is the handler to process MsgModifyLimitSwap.
type ModifyLimitSwapHandler struct {
	mgr Manager
}

// NewModifyLimitSwapHandler creates a new instance of ModifyLimitSwapHandler.
func NewModifyLimitSwapHandler(mgr Manager) ModifyLimitSwapHandler {
	return ModifyLimitSwapHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for ModifyLimitSwapHandler.
func (h ModifyLimitSwapHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgModifyLimitSwap)
	if !ok {
		return nil, errInvalidMessage
	}

	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgModifyLimitSwap failed validation", "error", err)
		return nil, err
	}

	err = h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgModifyLimitSwap", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, err
}

// validate performs stateless validation on MsgModifyLimitSwap.
//
// Security note: MsgModifyLimitSwap is NOT directly submittable via gRPC (it is not registered
// in the protobuf service descriptor). It can only reach this handler through two paths:
//
//  1. Native THORChain (MsgDeposit): The From address is derived from msg.GetSigners()[0]
//     in DepositHandler.handle(), so the user cannot spoof it. Additionally,
//     MsgModifyLimitSwap.ValidateBasic() enforces From == Signer for THORChain-native source assets.
//
//  2. External chains (Bifrost observation): The message is constructed by getMsgModifyLimitSwap()
//     from an ObservedTx, where the From address comes from the actual on-chain transaction
//     observed and agreed upon by multiple Bifrost validators. To spoof another user's From
//     address, an attacker would need to send a transaction from that address on the external
//     chain, which requires the victim's private key.
//
// Therefore, handle()'s check that msg.From matches the stored swap's FromAddress is sufficient —
// the From field is always authenticated by the message flow.
func (h ModifyLimitSwapHandler) validate(ctx cosmos.Context, msg MsgModifyLimitSwap) error {
	return msg.ValidateBasic()
}

func (h ModifyLimitSwapHandler) handle(ctx cosmos.Context, msg MsgModifyLimitSwap) error {
	// Design Decision: Swaps are identified by source/target assets rather than tx_id
	// This allows users to modify their swaps without tracking the original transaction ID.
	// Security is maintained by verifying the FromAddress matches the original swap creator.
	// If multiple swaps exist with the same source/target for a user, only the first is modified.

	// get the txn hashes that match this fake swap msg
	items, err := h.mgr.Keeper().GetAdvSwapQueueIndex(ctx, MsgSwap{
		Tx: common.Tx{
			Coins: common.NewCoins(msg.Source),
		},
		TargetAsset: msg.Target.Asset,
		TradeTarget: msg.Target.Amount,
		SwapType:    LimitSwap,
	})
	if err != nil {
		return err
	}

	// Find the first matching swap from the user.
	// Limit iterations to prevent DoS via bloated ratio-grouped index buckets.
	// An attacker could create many low-value limit swaps at the same price ratio,
	// inflating the shared index and forcing expensive iteration on modify/cancel.
	maxIterations := h.mgr.Keeper().GetConfigInt64(ctx, constants.ModifyLimitSwapMaxIterations)
	var matchingSwap *MsgSwap
	for i, item := range items {
		if int64(i) >= maxIterations {
			return fmt.Errorf("swap index too large, exceeded max iterations (%d)", maxIterations)
		}

		msgSwap, err := h.mgr.Keeper().GetAdvSwapQueueItem(ctx, item.TxID, item.Index)
		if err != nil {
			ctx.Logger().Error("fail to get swap book item", "hash", item.TxID, "index", item.Index)
			continue
		}

		// Ensure the From address matches the original swap creator.
		// The From field is authenticated by the message flow: for native deposits it's derived
		// from the signer's account, and for external chains it's from the observed on-chain tx.
		// See the validate() comment for full security analysis.
		if !msgSwap.Tx.FromAddress.Equals(msg.From) {
			continue
		}

		// Found a matching swap - break early to avoid loading unnecessary items
		matchingSwap = &msgSwap
		break
	}

	if matchingSwap == nil {
		return fmt.Errorf("could not find matching limit swap")
	}

	// Only modify the first matching swap
	msgSwap := *matchingSwap
	if msg.ModifiedTargetAmount.IsZero() {
		// the target is being modified to zero, which is interpreted as a cancel
		if err := h.cancelLimitSwap(ctx, msgSwap); err != nil {
			return err
		}
	} else {
		// modify the limit swap
		if err := h.modifyLimitSwap(ctx, msgSwap, msg.ModifiedTargetAmount); err != nil {
			return err
		}
	}

	// Donate any incoming funds from the modification transaction to the pool
	// If donation fails, the entire transaction fails to ensure atomicity and prevent fund loss.
	// The handler is wrapped in CacheContext by NewInternalHandler(), so all state changes are rolled back on error.
	if !msg.DepositAmount.IsZero() && !msg.DepositAsset.IsEmpty() {
		if err := h.donateToPool(ctx, msg.DepositAsset, msg.DepositAmount, msg.From); err != nil {
			return fmt.Errorf("fail to donate modification tx funds to pool: %w", err)
		}
	}

	modEvent := NewEventModifyLimitSwap(msg.From, msg.Source, msg.Target, msg.ModifiedTargetAmount)
	if err := h.mgr.EventMgr().EmitEvent(ctx, modEvent); err != nil {
		ctx.Logger().Error("fail to emit modEvent event", "error", err)
	}

	return nil
}

// cancelLimitSwap handles the cancellation of a limit swap
func (h ModifyLimitSwapHandler) cancelLimitSwap(ctx cosmos.Context, msgSwap MsgSwap) error {
	// Use settleSwap to handle the cancellation
	// This will handle any partial swaps and refund the remainder
	return settleSwap(ctx, h.mgr, msgSwap, "limit swap cancelled")
}

// modifyLimitSwap handles the modification of a limit swap's target amount
func (h ModifyLimitSwapHandler) modifyLimitSwap(ctx cosmos.Context, msgSwap MsgSwap, newTargetAmount cosmos.Uint) error {
	// remove current index
	if err := h.mgr.Keeper().RemoveAdvSwapQueueIndex(ctx, msgSwap); err != nil {
		return err
	}

	// update trade target
	msgSwap.TradeTarget = newTargetAmount

	// save the modified swap back to the queue (SetAdvSwapQueueItem also updates the index)
	if err := h.mgr.Keeper().SetAdvSwapQueueItem(ctx, msgSwap); err != nil {
		return err
	}

	return nil
}

// donateToPool adds the given amount to the specified pool's balance
func (h ModifyLimitSwapHandler) donateToPool(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, from common.Address) error {
	// RUNE cannot be donated to a pool directly - it stays in Reserve.
	// The funds were already sent to Reserve by handler_deposit.go, so we just skip the donation.
	if asset.IsRune() {
		return nil
	}

	// Get the pool for the asset
	pool, err := h.mgr.Keeper().GetPool(ctx, asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get pool: %w", err)
	}
	if pool.IsEmpty() {
		return fmt.Errorf("pool does not exist for asset %s", asset)
	}

	// For native THORChain assets (RUNE, synths, etc.), transfer funds from Reserve to Asgard.
	// The funds were deposited to Reserve by handler_deposit.go when processing the native deposit.
	// Pool balances must be backed by actual funds in the Asgard module account.
	// External chain assets (BTC, ETH, etc.) are observed via Bifrost and don't go through bank module.
	if asset.IsNative() {
		coin := common.NewCoin(asset, amount)
		if err := h.mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(coin)); err != nil {
			return fmt.Errorf("fail to transfer funds from reserve to asgard: %w", err)
		}
	}

	// Add the amount to the pool's asset balance (RUNE is handled by early return above)
	pool.BalanceAsset = pool.BalanceAsset.Add(amount)

	// Save the updated pool
	if err := h.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return fmt.Errorf("fail to save pool: %w", err)
	}

	// Create a minimal transaction for the donation event
	tx := common.Tx{
		ID:          common.TxID(""),
		Chain:       asset.GetChain(),
		FromAddress: from,
		ToAddress:   common.NoAddress,
		Coins:       common.NewCoins(common.NewCoin(asset, amount)),
		Gas:         nil,
		Memo:        "THOR-MODIFY-LIMIT",
	}

	// Emit a donation event
	donateEvt := NewEventDonate(pool.Asset, tx)
	if err := h.mgr.EventMgr().EmitEvent(ctx, donateEvt); err != nil {
		ctx.Logger().Error("fail to emit donate event", "error", err)
	}

	return nil
}
