package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// DonateHandler is to handle donate message
type DonateHandler struct {
	mgr Manager
}

// NewDonateHandler create a new instance of DonateHandler
func NewDonateHandler(mgr Manager) DonateHandler {
	return DonateHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to execute donate logic
func (h DonateHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgDonate)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive msg donate", "tx_id", msg.Tx.ID)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg donate failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg donate", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

func (h DonateHandler) validate(ctx cosmos.Context, msg MsgDonate) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	if !msg.Asset.IsRune() && msg.Asset.IsNative() {
		ctx.Logger().Error("asset cannot be a non-RUNE native asset", "error", errInvalidMessage)
		return errInvalidMessage
	}
	return nil
}

// handle process MsgDonate, MsgDonate add asset and RUNE to the asset pool
// it simply increase the pool asset/RUNE balance but without taking any of the pool units
func (h DonateHandler) handle(ctx cosmos.Context, msg MsgDonate) error {
	pool, err := h.mgr.Keeper().GetPool(ctx, msg.Asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get pool for (%s)", msg.Asset))
	}
	if pool.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("pool %s not exist", msg.Asset.String()))
	}

	// Verify that donation amounts match the coins in the observed transaction.
	// This provides defense-in-depth validation. The amounts are derived from verified sources:
	// - External chains: Bifrost observers verify actual on-chain transactions before reporting
	// - Native chain: Cosmos SDK bank module ante handler validates transfers
	// The getMsgDonateFromMemo function extracts amounts from tx.Tx.Coins, so this check
	// ensures message integrity and guards against any potential manipulation.
	txRuneCoin := msg.Tx.Coins.GetCoin(common.RuneAsset())
	txAssetCoin := msg.Tx.Coins.GetCoin(msg.Asset)
	if !msg.RuneAmount.Equal(txRuneCoin.Amount) {
		return fmt.Errorf("rune amount mismatch: msg has %s but tx has %s", msg.RuneAmount, txRuneCoin.Amount)
	}
	if !msg.AssetAmount.Equal(txAssetCoin.Amount) {
		return fmt.Errorf("asset amount mismatch: msg has %s but tx has %s", msg.AssetAmount, txAssetCoin.Amount)
	}

	pool.BalanceAsset = pool.BalanceAsset.Add(msg.AssetAmount)
	pool.BalanceRune = pool.BalanceRune.Add(msg.RuneAmount)

	if err = h.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to set pool(%s)", pool))
	}
	// emit event
	donateEvt := NewEventDonate(pool.Asset, msg.Tx)
	if err = h.mgr.EventMgr().EmitEvent(ctx, donateEvt); err != nil {
		return errFailSaveEvent.Wrapf("fail to save donate events: %s", err)
	}
	return nil
}
