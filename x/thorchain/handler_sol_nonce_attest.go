package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// solNonceReattestMinAgeBlocks is how long a vault's committed durable nonce must
// stand before a re-attestation may replace it (~1h at 6s blocks). Normal commits
// come from the observation of the tx that advanced the nonce within about a minute;
// the delay keeps re-attestation from racing, and pre-empting, that path.
const solNonceReattestMinAgeBlocks = int64(600)

// SolNonceAttestHandler processes MsgSolNonceAttest: validators re-attest a Solana
// vault's durable-nonce account after its on-chain nonce moved past the committed
// value without a committed observation (every tx that advanced it FAILED on-chain,
// so none was observed and the vault's durable outbounds are wedged).
type SolNonceAttestHandler struct {
	mgr Manager
}

// NewSolNonceAttestHandler creates a new SolNonceAttestHandler.
func NewSolNonceAttestHandler(mgr Manager) SolNonceAttestHandler {
	return SolNonceAttestHandler{mgr: mgr}
}

// Run is the main entry point to process MsgSolNonceAttest.
func (h SolNonceAttestHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgSolNonceAttest)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg sol nonce attest failed validation", "error", err)
		return nil, err
	}
	return h.handle(ctx, *msg)
}

func (h SolNonceAttestHandler) validate(ctx cosmos.Context, msg MsgSolNonceAttest) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	if !isSignedByActiveNodeAccounts(ctx, h.mgr.Keeper(), msg.GetSigners()) {
		return cosmos.ErrUnauthorized(fmt.Sprintf("%+v are not authorized", msg.GetSigners()))
	}
	return nil
}

// handle records the vote and re-anchors the vault's committed nonce when a
// supermajority of active validators attested the identical value, and
//   - the account is the vault's own seed-derived nonce account,
//   - the value differs from the committed one, and
//   - the committed value is at least solNonceReattestMinAgeBlocks old.
//
// Votes count together only when identical, slot included, so one validator voting
// a deviant slot splits off instead of poisoning the value. The slot must be newer
// than the committed one. The new record's set_by_tx is the attested last_tx.
func (h SolNonceAttestHandler) handle(ctx cosmos.Context, msg MsgSolNonceAttest) (*cosmos.Result, error) {
	k := h.mgr.Keeper()
	vault, err := k.GetVault(ctx, msg.VaultPubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault %s: %w", msg.VaultPubKey, err)
	}
	vaultAddr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil {
		return nil, fmt.Errorf("vault %s has no SOL address: %w", vault.PubKey, err)
	}
	expected, err := common.SolNonceAccountAddress(vaultAddr.String())
	if err != nil || expected != msg.NonceAccount {
		return nil, cosmos.ErrUnknownRequest("nonce account is not the vault's seed-derived account")
	}

	// Re-attestation repairs a vault that already has a committed nonce (possibly
	// voided by the break-glass); it never bootstraps one, which is the creation
	// observation's job.
	if _, rawFound, rawErr := k.GetSolVaultNonce(ctx, vault.PubKey); rawErr != nil {
		return nil, fmt.Errorf("fail to get sol vault nonce: %w", rawErr)
	} else if !rawFound {
		return nil, cosmos.ErrUnknownRequest("vault has no committed durable nonce to re-attest")
	}

	voter, err := k.GetSolNonceAttestVoter(ctx, vault.PubKey, msg.NonceAccount, msg.Nonce, msg.LastTx, msg.Slot)
	if err != nil {
		return nil, fmt.Errorf("fail to get sol nonce attest voter: %w", err)
	}
	voter.Add(msg.Signer.String())
	defer func() { k.SetSolNonceAttestVoter(ctx, voter) }()
	if voter.CommittedHeight > 0 {
		return &cosmos.Result{}, nil
	}

	active, err := k.ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}
	counted := 0
	for _, signer := range voter.Signers {
		addr, aErr := cosmos.AccAddressFromBech32(signer)
		if aErr != nil || !active.IsNodeKeys(addr) {
			continue
		}
		counted++
	}
	if !HasSuperMajority(counted, len(active)) {
		return &cosmos.Result{}, nil
	}

	existing, found, err := activeSolVaultNonce(ctx, k, vault.PubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get sol vault nonce: %w", err)
	}
	if found {
		if existing.Nonce == msg.Nonce {
			voter.CommittedHeight = ctx.BlockHeight() // already the committed value
			return &cosmos.Result{}, nil
		}
		if ctx.BlockHeight()-existing.UpdatedHeight < solNonceReattestMinAgeBlocks {
			return &cosmos.Result{}, nil // too soon: a later vote re-checks
		}
		if msg.Slot <= existing.Slot {
			return &cosmos.Result{}, nil
		}
	}
	k.SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey:   vault.PubKey,
		NonceAccount:  msg.NonceAccount,
		Nonce:         msg.Nonce,
		Slot:          msg.Slot,
		UpdatedHeight: ctx.BlockHeight(),
		SetByTx:       msg.LastTx,
	})
	voter.CommittedHeight = ctx.BlockHeight()
	ctx.Logger().Info("re-attested sol durable nonce", "vault", vault.PubKey, "nonce", msg.Nonce,
		"slot", msg.Slot, "last_tx", msg.LastTx)
	if found {
		// The re-attested txs all failed, so nothing they carried needs matching.
		reissueSupersededSolDurable(ctx, h.mgr, vault, existing.UpdatedHeight, nil)
	}
	return &cosmos.Result{}, nil
}

// SolNonceAttestAnteHandler gates mempool entry to active validators.
func SolNonceAttestAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgSolNonceAttest) (cosmos.Context, error) {
	return activeNodeAccountsSignerPriority(ctx, k, msg.GetSigners())
}
