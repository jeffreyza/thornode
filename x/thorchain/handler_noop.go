package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// NoOpHandler is to handle no-op messages (e.g. vault balance adjustments)
type NoOpHandler struct {
	mgr Manager
}

// NewNoOpHandler create a new instance of NoOpHandler
func NewNoOpHandler(mgr Manager) NoOpHandler {
	return NoOpHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to execute no-op logic
func (h NoOpHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgNoOp)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("receive msg no op", "tx_id", msg.ObservedTx.Tx.ID)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg no op failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg noop", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, nil
}

func (h NoOpHandler) validate(ctx cosmos.Context, msg MsgNoOp) error {
	return msg.ValidateBasic()
}

// handle process MsgNoOp
// For "novault" action, it subtracts coins from the vault balance (e.g. to correct for already-observed funds)
func (h NoOpHandler) handle(ctx cosmos.Context, msg MsgNoOp) error {
	action := msg.GetAction()
	if len(action) == 0 {
		return nil
	}
	if !strings.EqualFold(action, "novault") {
		return nil
	}

	// The "novault" action subtracts the just-credited coins from the vault's tracked
	// balance, deliberately diverging tracked from physical accounting. The action
	// string originates from the inbound L1 memo, which any sender controls, so on its
	// own it lets anyone debit an asgard vault's accounting by depositing real funds.
	// Gate the debit behind a chain-scoped mimir that node operators open only for the
	// brief, coordinated window in which a legitimate insolvency correction is made.
	// When the gate is closed, leave the observation credit in place (equivalent to a
	// plain noop / donation) so an unauthorized novault cannot desync the vault.
	if !h.noVaultAuthorized(ctx, msg.ObservedTx.Tx.Chain) {
		ctx.Logger().Info("noop:novault not authorized, leaving vault credit in place",
			"chain", msg.ObservedTx.Tx.Chain, "tx_id", msg.ObservedTx.Tx.ID)
		return nil
	}

	// Get the vault and verify it exists
	vault, err := h.mgr.Keeper().GetVault(ctx, msg.ObservedTx.ObservedPubKey)
	if err != nil {
		return fmt.Errorf("fail to get vault: %w", err)
	}

	// Verify vault is not empty (exists)
	if vault.IsEmpty() {
		return fmt.Errorf("vault does not exist: %s", msg.ObservedTx.ObservedPubKey)
	}

	// Verify vault status is active or retiring - only these vaults should have funds modified
	if !vault.IsActive() && !vault.IsRetiring() {
		return fmt.Errorf("vault is not active or retiring: %s (status: %s)", msg.ObservedTx.ObservedPubKey, vault.Status)
	}

	// Aggregate duplicate assets before the sufficiency check so the later debit
	// cannot create a deficit after each individual coin appeared covered.
	coins := common.Coins{}.Add(msg.ObservedTx.Tx.Coins...).NoneEmpty()

	// Verify the vault has sufficient funds for each asset being subtracted.
	for _, coin := range coins {
		vaultCoin := vault.GetCoin(coin.Asset)
		if vaultCoin.Amount.LT(coin.Amount) {
			return fmt.Errorf("vault has insufficient funds for %s: has %s, attempting to subtract %s",
				coin.Asset, vaultCoin.Amount, coin.Amount)
		}
	}

	// Subtract the coins from vault, as it has been added to. The sufficiency
	// check above makes deficit creation unreachable here; the halting reason is
	// retained as a defensive signal if that precondition is ever broken.
	debitVault(ctx, h.mgr, &vault, msg.ObservedTx, coins, vaultDeficitReasonNoVault)
	if err := h.mgr.Keeper().SetVault(ctx, vault); err != nil {
		return fmt.Errorf("fail to save vault: %w", err)
	}

	// Emit an event on every authorized debit so external monitoring can alarm if a
	// novault adjustment ever occurs (these are rare, dangerous, dev-centric ops).
	ctx.EventManager().EmitEvent(
		cosmos.NewEvent("noop_novault_debit",
			cosmos.NewAttribute("chain", msg.ObservedTx.Tx.Chain.String()),
			cosmos.NewAttribute("vault", msg.ObservedTx.ObservedPubKey.String()),
			cosmos.NewAttribute("tx_id", msg.ObservedTx.Tx.ID.String()),
			cosmos.NewAttribute("coins", msg.ObservedTx.Tx.Coins.String()),
		),
	)
	return nil
}

// noVaultAuthorized reports whether node operators have opened the chain-scoped
// mimir gate that authorizes a noop:novault vault debit. The gate is honored when
// the mimir is set to 1 (manual, indefinite override) or to a block height that has
// not yet passed (a windowed authorization that auto-expires).
func (h NoOpHandler) noVaultAuthorized(ctx cosmos.Context, chain common.Chain) bool {
	m, err := h.mgr.Keeper().GetMimirWithRef(ctx, constants.MimirTemplateAllowNoVault, chain.String())
	if err != nil {
		ctx.Logger().Error("fail to get AllowNoVault mimir", "error", err, "chain", chain)
		return false
	}
	return m == 1 || (m > 0 && m >= ctx.BlockHeight())
}
