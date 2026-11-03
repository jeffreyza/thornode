package thorchain

import (
	"context"
	"fmt"
	"strings"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TssKeysignHandler is design to process MsgTssKeysignFail
type TssKeysignHandler struct {
	mgr Manager
}

// NewTssKeysignHandler create a new instance of TssKeysignHandler
// when a signer fail to join tss keysign , thorchain need to slash the node account
func NewTssKeysignHandler(mgr Manager) TssKeysignHandler {
	return TssKeysignHandler{
		mgr: mgr,
	}
}

// Run is the main entry to process MsgTssKeysignFail
func (h TssKeysignHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgTssKeysignFail)
	if !ok {
		return nil, errInvalidMessage
	}
	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgTssKeysignFail failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("failed to process MsgTssKeysignFail", "error", err)
	}
	return result, err
}

func (h TssKeysignHandler) validate(ctx cosmos.Context, msg MsgTssKeysignFail) error {
	// ValidateBasic is also executed in message service router's handler and isn't versioned there
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	m, err := NewMsgTssKeysignFail(msg.Height, msg.Blame, msg.Memo, msg.Coins, msg.Signer, msg.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to reconstruct keysign fail msg", "error", err)
		return err
	}
	if !strings.EqualFold(m.ID, msg.ID) {
		return cosmos.ErrUnknownRequest("invalid keysign fail message")
	}

	if _, err = validateKeysignAuth(ctx, h.mgr.Keeper(), msg.GetSigners()); err != nil {
		return err
	}

	active, err := h.mgr.Keeper().CountActiveValidators(ctx)
	if err != nil {
		return wrapError(ctx, err, "fail to get list of active node accounts")
	}

	allowWideBlame := h.mgr.Keeper().GetConfigInt64(ctx, constants.AllowWideBlame)
	if allowWideBlame == 0 && !HasSimpleMajority(active-len(msg.Blame.BlameNodes), active) {
		ctx.Logger().Error("blame cast too wide", "blame", len(msg.Blame.BlameNodes))
		return fmt.Errorf("blame cast too wide: %d/%d", len(msg.Blame.BlameNodes), active)
	}

	return nil
}

func (h TssKeysignHandler) handle(ctx cosmos.Context, msg MsgTssKeysignFail) (*cosmos.Result, error) {
	ctx.Logger().Info("handle MsgTssKeysignFail request", "ID", msg.ID, "signer", msg.Signer, "pubkey", msg.PubKey, "blame", msg.Blame.String())
	voter, err := h.mgr.Keeper().GetTssKeysignFailVoter(ctx, msg.ID)
	if err != nil {
		return nil, err
	}
	if voter.State != thortypes.TssKeysignFailVoterState_PendingVoter {
		// Legacy and completed records are terminal replay barriers. A legacy
		// record cannot distinguish a completed vote from partial or late-report
		// residue, so processing it again could repeat terminal side effects.
		ctx.Logger().Info("MsgTssKeysignFail voter is terminal", "ID", msg.ID, "state", voter.State)
		return &cosmos.Result{}, nil
	}
	observeSlashPoints := h.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	// add labels to telemetry context
	labels := []metrics.Label{
		telemetry.NewLabel("reason", "failed_keysign"),
	}
	if len(msg.Coins) == 1 { // only label when slash is for single asset
		labels = append(
			labels,
			telemetry.NewLabel("chain", string(msg.Coins[0].Asset.Chain)),
			telemetry.NewLabel("symbol", string(msg.Coins[0].Asset.Symbol)),
		)
	}
	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, labels))

	if !voter.Sign(msg.Signer) {
		ctx.Logger().Info("signer already signed MsgTssKeysignFail", "signer", msg.Signer.String(), "txid", msg.ID)
		return &cosmos.Result{}, nil
	}
	err = h.mgr.Keeper().IncNodeAccountSlashPoints(slashCtx, msg.Signer, observeSlashPoints)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to increase observation slash points")
	}

	vault, err := h.mgr.Keeper().GetVault(ctx, msg.PubKey)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get vault")
	}
	if vault.IsEmpty() {
		err = h.mgr.Keeper().SetTssKeysignFailVoter(ctx, voter)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to save tss keysign fail voter")
		}
		return &cosmos.Result{}, nil
	}
	var vaultMemberNodes NodeAccounts
	for _, item := range vault.GetMembership() {
		var addr cosmos.AccAddress
		addr, err = item.GetThorAddress()
		if err != nil {
			return nil, wrapError(ctx, err, "fail to get thor address for "+item.String())
		}
		var na NodeAccount
		na, err = h.mgr.Keeper().GetNodeAccount(ctx, addr)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to get node account")
		}
		vaultMemberNodes = append(vaultMemberNodes, na)
	}

	// Track terminal-round failures from the vault's actual signing committee,
	// not just the current active validator set. Retiring vault members can
	// still participate in keysign during churn after leaving the active set,
	// so using IsNodeKeys here would undercount final-round reports and could
	// miss freezing a vault that is still failing at the terminal round.
	//
	// The terminal safety boundary is the first round where enough signing
	// material may have escaped to another participant: KEYSIGN9 for tss-lib v3
	// secp256k1 (v3 added rounds 8 and 9), EDDSAKEYSIGN3 for EdDSA, and both
	// FROSTKEYSIGNSHARE and FROSTKEYSIGNRESULT for FROST. FROST's local signature
	// share is broadcast before frost_share can fail, so a peer may already be
	// able to aggregate and publish even though the result-propagation phase was
	// never reached. frost_preprocess remains non-terminal.
	// KEYSIGN7 is deliberately NOT accepted.
	// Although round 7 was the terminal round under tss-lib v1, under v3 it is
	// an interior consistency-check commitment round (the U_i/T_i commitment in
	// the GG18 phase-5 check) that runs before any share is revealed at round 9.
	// A round-7 failure is therefore a transient mid-protocol liveness blip, not
	// a terminal signing failure; accepting it would freeze vaults on ordinary
	// peer timeouts. v1 round-7 backward compatibility is intentionally dropped:
	// once v3 is adopted the network signs exclusively under the v3 schema, so
	// no new KEYSIGN7 records are produced (see thornode!4846).
	//
	// CONSENSUS-AFFECTING: this acceptance set determines voter.LastRoundCount
	// math for any MsgTssKeysignFail carrying these round names. All validators
	// must run a build with the same set before the network observes v3 keysign
	// failures, otherwise upgraded and non-upgraded peers diverge.
	//
	// The len(BlameNodes) > 0 guard prevents a colluding minority from
	// freezing a vault by reporting terminal-round failures that blame
	// nobody — Blame.IsEmpty() only checks FailReason, so without this
	// guard an empty-BlameNodes terminal-round report would still bump
	// LastRoundCount toward the freeze threshold.
	round := thortypes.CanonicalTssRound(msg.Blame.Round, "")
	if (round == tssMessages.KEYSIGN9 ||
		round == tssMessages.EDDSAKEYSIGN3 ||
		round == tssMessages.FROSTKEYSIGNSHARE ||
		round == tssMessages.FROSTKEYSIGNRESULT) && len(msg.Blame.BlameNodes) > 0 {
		for _, na := range vaultMemberNodes {
			if msg.Signer.Equals(na.NodeAddress) {
				voter.LastRoundCount++
				// Pre-freeze early warning: two of these freeze the vault below,
				// and LastRoundCount is reset to 0 on consensus, so it cannot be
				// recovered from state afterwards. round is bounded to the four
				// terminal rounds by the guard above.
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "tss", "keysign", "terminal_round_report"},
					1,
					[]metrics.Label{
						telemetry.NewLabel("pubkey", msg.PubKey.String()),
						telemetry.NewLabel("round", round),
					},
				)
				break
			}
		}
	}
	// doesn't have consensus yet
	if !voter.HasConsensus(vaultMemberNodes) {
		err = h.mgr.Keeper().SetTssKeysignFailVoter(ctx, voter)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to save tss keysign fail voter")
		}
		return &cosmos.Result{}, nil
	}
	violaters := make([]string, len(msg.Blame.BlameNodes))
	for i, node := range msg.Blame.BlameNodes {
		violaters[i] = node.Pubkey
	}
	ctx.Logger().Info(
		"tss keysign failure",
		"reason", msg.Blame.FailReason,
		"is_unicast", msg.Blame.IsUnicast,
		"round", msg.Blame.Round,
		"blame", strings.Join(violaters, ", "),
	)

	// Emit the keysign failure with round and blame granularity so off-chain
	// consumers can track failures per vault, round and blamed node without
	// decoding the tx. Blame.Round is already canonical — ValidateBasic
	// rejects non-canonical round names.
	keysignFailEvent := thortypes.NewEventTssKeysignFailure(
		msg.Blame.FailReason,
		msg.Blame.Round,
		msg.Blame.IsUnicast,
		msg.Height,
		violaters,
		msg.ID,
		msg.PubKey,
	)
	if emitErr := h.mgr.EventMgr().EmitEvent(ctx, keysignFailEvent); emitErr != nil {
		ctx.Logger().Error("fail to emit tss keysign failure event", "error", emitErr)
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "tss", "keysign", "failure"},
		float32(1),
		[]metrics.Label{telemetry.NewLabel("pubkey", msg.PubKey.String()), telemetry.NewLabel("round", msg.Blame.Round)},
	)

	// If at least 2 nodes in the simple majority report a terminal-round failure freeze the vault.
	// There is a tradeoff here between the number of nodes required to maliciously freeze
	// the vault and the number of nodes required to maliciously prevent freeze - we err
	// on the side of over-freezing.
	if voter.LastRoundCount > 1 || (voter.LastRoundCount > 0 && voter.MemberSignerCount(vaultMemberNodes) <= 2) {
		// this will cause the vault to be "frozen" which causes the
		// rescheduler to NOT reschedule any outbound txns AND cause the tx out
		// manager to not assign new txns to this vault
		for _, coin := range msg.Coins {
			found := false
			for _, chain := range vault.Frozen {
				if chain == coin.Asset.GetChain().String() {
					found = true
					break
				}
			}
			if !found {
				vault.Frozen = append(vault.Frozen, coin.Asset.GetChain().String())
			}
		}
		err = h.mgr.Keeper().SetVault(ctx, vault)
		if err != nil {
			return nil, fmt.Errorf("fail to save vault: %w", err)
		}
	}

	slashPoints := h.mgr.GetConstants().GetInt64Value(constants.FailKeysignSlashPoints)
	// fail to generate a new tss key let's slash the node account

	for _, node := range msg.Blame.BlameNodes {
		nodePubKey, nodeErr := common.NewPubKey(node.Pubkey)
		if nodeErr != nil {
			return nil, ErrInternal(nodeErr, "fail to parse pubkey")
		}
		na, nodeErr := h.mgr.Keeper().GetNodeAccountByPubKey(ctx, nodePubKey)
		if nodeErr != nil {
			return nil, ErrInternal(nodeErr, fmt.Sprintf("fail to get node account,pub key: %s", nodePubKey.String()))
		}
		err = h.mgr.Keeper().IncNodeAccountSlashPoints(slashCtx, na.NodeAddress, slashPoints)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to inc slash points")
		}

		// Which node is actually being blamed. thornode_tss_keysign_failure says a
		// keysign failed but not who caused it, and thornode_point_slash{reason=
		// "failed_keysign"} cannot answer it either: the same slashCtx charges the
		// honest reporter above and the blamed offender here, so the two are
		// indistinguishable. msg.Blame.Round is canonical by ValidateBasic.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "tss", "keysign", "blame"},
			1,
			[]metrics.Label{
				telemetry.NewLabel("node_address", na.NodeAddress.String()),
				telemetry.NewLabel("round", msg.Blame.Round),
				telemetry.NewLabel("pubkey", msg.PubKey.String()),
			},
		)

		if emitErr := h.mgr.EventMgr().EmitEvent(ctx, NewEventSlashPoint(na.NodeAddress, slashPoints, "fail keysign")); emitErr != nil {
			ctx.Logger().Error("fail to emit slash point event")
		}
		// go to jail
		ctx.Logger().Info("jailing node", "pubkey", na.PubKeySet.Secp256k1)
		jailTime := h.mgr.GetConstants().GetInt64Value(constants.JailTimeKeysign)
		releaseHeight := ctx.BlockHeight() + jailTime
		reason := jailReasonFailedToPerformKeysign
		err = h.mgr.Keeper().SetNodeAccountJail(ctx, na.NodeAddress, releaseHeight, reason)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to set node account jail")
		}
	}

	// The voter is the durable replay barrier. Write it only after every
	// required terminal effect has succeeded. DeliverTx executes the handler in
	// a cached context, so returning any error above rolls back the vote,
	// observation-point charge/refund, vault freeze, blamed-node slash points,
	// jail records and events together. Once the completed state is persisted,
	// a replay may safely no-op.
	for _, signer := range voter.GetSigners() {
		err = h.mgr.Keeper().DecNodeAccountSlashPoints(slashCtx, signer, observeSlashPoints)
		if err != nil {
			return nil, wrapError(ctx, err, "fail to refund observation slash points")
		}
	}
	voter.Signers = nil
	voter.LastRoundCount = 0
	voter.State = thortypes.TssKeysignFailVoterState_CompletedVoter
	err = h.mgr.Keeper().SetTssKeysignFailVoter(ctx, voter)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to save tss keysign fail voter")
	}

	return &cosmos.Result{}, nil
}

func validateKeysignAuth(ctx cosmos.Context, k keeper.Keeper, signers []cosmos.AccAddress) (cosmos.Context, error) {
	if isSignedByActiveNodeAccounts(ctx, k, signers) {
		return ctx.WithPriority(ActiveNodePriority), nil
	}
	shouldAccept := false
	vaults, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return ctx, ErrInternal(err, "fail to get retiring vaults")
	}
	if len(vaults) > 0 {
		for _, signer := range signers {
			nodeAccount, err := k.GetNodeAccount(ctx, signer)
			if err != nil {
				return ctx, ErrInternal(err, "fail to get node account")
			}
			for _, v := range vaults {
				if v.GetMembership().Contains(nodeAccount.PubKeySet.Secp256k1) {
					shouldAccept = true
					break
				}
			}
			if shouldAccept {
				break
			}
		}
	}
	if !shouldAccept {
		return ctx, cosmos.ErrUnauthorized("not authorized")
	}
	return ctx, nil
}

// TssKeysignAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func TssKeysignFailAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgTssKeysignFail) (cosmos.Context, error) {
	return validateKeysignAuth(ctx, k, msg.GetSigners())
}
