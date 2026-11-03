package thorchain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// observationDelayFlexibility lets a slow chain allow later observations without
// changing other chains. The value is measured in THORChain blocks.
func observationDelayFlexibility(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) int64 {
	value, err := k.GetMimir(ctx, constants.ObservationDelayFlexibility.String()+chain.String())
	if err == nil && value >= 0 {
		return value
	}
	return k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
}

// processTxInAttestation applies a single attestation for an observed tx and saves
// the voter. MsgObservedTxIn (single attester) uses it; MsgObservedTxQuorum applies
// its attestations with applyTxInAttestation and saves the voter once.
func processTxInAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool) {
	voter, ok, added := applyTxInAttestation(ctx, mgr, voter, nas, tx, signer, shouldSlashForDuplicate)
	if added {
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	}
	return voter, ok
}

// applyTxInAttestation records a single attestation on the voter without saving
// it, so a quorum message can save the voter once after all its attestations.
// It returns the voter, whether this attestation brought it to consensus, and
// whether it was recorded: false for a duplicate, which leaves the voter unchanged.
func applyTxInAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool, bool) {
	k := mgr.Keeper()
	slasher := mgr.Slasher()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := observationDelayFlexibility(ctx, k, tx.Tx.Chain)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txin"),
		telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
	}))
	slashCtx = ctx.WithContext(context.WithValue(slashCtx.Context(), constants.CtxObservedTx, tx.Tx.ID.String()))

	ok := false
	if err := k.SetLastObserveHeight(ctx, tx.Tx.Chain, signer, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to save last observe height", "error", err, "signer", signer, "chain", tx.Tx.Chain)
	}

	// As an observation requires processing by all nodes no matter what,
	// any observation should increment ObserveSlashPoints,
	// to be decremented only if contributing to or within ObservationDelayFlexibility of consensus.
	slasher.IncSlashPoints(slashCtx, observeSlashPoints, signer)

	// A committee member's pre-broadcast "instant" observation and its later
	// scanner observation of the same tx differ only in gas (EVM) or block height
	// (all chains, from signing-tip vs inclusion) and split into separate voter
	// entries. Detect that the signer already recorded a matching variant BEFORE
	// Add records this one, so the redundant observe point can be refunded exactly
	// once below without depending on the post-consensus flex branch.
	// Refund a redundant re-observation only for the signer's FIRST prior variant
	// (exactly one earlier matching observation) — the honest "observed it, lost the
	// gas/height race, re-observed once" case. Further variants (count >= 2) are
	// self-inflicted spam and keep their observe slash point, so a node cannot file
	// unlimited gas/height variants at zero cost to erase the per-observation charge.
	redundantReObservation := voter.ShouldRefundRedundantInboundObservation(tx, signer)

	obsChain := telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))
	obsDir := telemetry.NewLabel("direction", "in")

	if !voter.Add(tx, signer) {
		if !shouldSlashForDuplicate {
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observe", "attestation"}, 1,
			[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", "duplicate")},
		)
		// A duplicate message, so do nothing further.
		return voter, ok, false
	}

	// Single mutually-exclusive attestation result per call; late wins over redundant.
	attResult := "new"

	// The signer already observed this tx via a different gas/height variant, so
	// this observation is redundant honest work rather than a missed observation:
	// refund its observe point. The observation stays recorded above so it still
	// counts toward consensus and gas correction.
	if redundantReObservation {
		attResult = "redundant"
		slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
	}
	if voter.HasFinalised(nas) {
		if voter.FinalisedHeight == 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "true")},
			)
			ok = true
			voter.Height = ctx.BlockHeight() // Always record the consensus height of the finalised Tx
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			voter.FinalisedHeight = ctx.BlockHeight()
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetInboundObserveSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			// Persist the credited signer set, including legacy XMR observers,
			// so later observations cannot collect the same consensus credit again.
			voter.RecordObserveSigners(signers)
			queueObservationTally(ctx, k, observationTallyKindTxIn, tx.Tx.Chain, tx.Tx.ID.String())
		} else if ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.EqualsInboundObservationCredit(tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
			attResult = "late"
		}
	}
	if !ok && voter.HasConsensus(nas) && !tx.IsFinal() && voter.FinalisedHeight == 0 {
		if voter.Height == 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "false")},
			)
			ok = true
			voter.Height = ctx.BlockHeight()
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			// this is the tx that has consensus
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetInboundObserveSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			// Persist the credited signer set, including legacy XMR observers,
			// so later observations cannot collect the same consensus credit again.
			voter.RecordObserveSigners(signers)
		} else if ctx.BlockHeight() <= (voter.Height+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.EqualsInboundObservationCredit(tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
			attResult = "late"
		}
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "observe", "attestation"}, 1,
		[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", attResult)},
	)

	return voter, ok, true
}

// ensureVaultAndGetTxInVoter will make sure the vault exists, then get the ObservedTxInVoter from the store.
// if it doesn't exist, it will create a new one.
func ensureVaultAndGetTxInVoter(ctx cosmos.Context, vaultPubKey common.PubKey, txID common.TxID, k keeper.Keeper) (ObservedTxVoter, error) {
	// check we are sending to a valid vault
	if !k.VaultExists(ctx, vaultPubKey) {
		ctx.Logger().Info("Not valid Observed Pubkey", "observed pub key", vaultPubKey)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txin_vault_not_found")},
		)
		return ObservedTxVoter{}, fmt.Errorf("vault not found for observed tx in pubkey: %s", vaultPubKey)
	}

	voter, err := k.GetObservedTxInVoter(ctx, txID)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txin_voter_read_fail")},
		)
		return ObservedTxVoter{}, fmt.Errorf("fail to get tx in voter: %w", err)
	}

	return voter, nil
}

const (
	compromisedVaultMimirKeyPrefix  = "CompromisedVault-"
	compromisedVaultNoOpSecurityMsg = "inbound to compromised vault no-oped"
)

// isVaultCompromised reports whether CompromisedVault-<pubkey> is set.
// The compromised-vault Mimir is keyed by vault pubkey rather than chain or
// address so it covers every inbound address exposed by the same vault.
// On KV read failure it returns true so callers can fail closed.
func isVaultCompromised(ctx cosmos.Context, k keeper.Keeper, vaultPubKey common.PubKey) (bool, error) {
	flagged, err := k.GetMimir(ctx, compromisedVaultMimirKeyPrefix+vaultPubKey.String())
	if err != nil {
		return true, err
	}
	return flagged > 0, nil
}

// emitCompromisedVaultNoOpSecurityEvent emits the operator-facing alert for an
// inbound that reached quorum but was intentionally skipped because its vault is
// flagged by CompromisedVault-<pubkey>.
func emitCompromisedVaultNoOpSecurityEvent(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, vaultPubKey common.PubKey) {
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "inbound", "compromised_vault_noop"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
			telemetry.NewLabel("pubkey", vaultPubKey.String()),
		},
	)

	// EventSecurity includes the external tx attributes, but not the observed
	// vault pubkey, so keep the vault key in the message for incident review.
	msg := fmt.Sprintf("%s: vault=%s", compromisedVaultNoOpSecurityMsg, vaultPubKey.String())
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx.Tx, msg)); err != nil {
		ctx.Logger().Error("fail to emit compromised vault no-op security event", "pub_key", vaultPubKey.String(), "tx", tx.Tx.ID.String(), "error", err)
	}
}

// handleObservedTxInQuorum - will process the observed tx in quorum.
// used by both MsgObservedTxIn and MsgObservedTxInQuorum after processing
// attestation(s).
func handleObservedTxInQuorum(
	ctx cosmos.Context,
	mgr Manager,
	signer cosmos.AccAddress,
	activeNodeAccounts NodeAccounts,
	handler cosmos.Handler,
	tx common.ObservedTx,
	voter ObservedTxVoter,
	observers []cosmos.AccAddress,
	isQuorum bool,
) error {
	if !isQuorum {
		if voter.Height == ctx.BlockHeight() || voter.FinalisedHeight == ctx.BlockHeight() {
			// we've already process the transaction, but we should still
			// update the observing addresses
			mgr.ObMgr().AppendObserver(tx.Tx.Chain, observers)
		}
		return nil
	}

	// all logic after this is upon consensus
	k := mgr.Keeper()

	// An XMR errata can reach consensus while the original inbound has only
	// partial observations. In that ordering the errata voter is committed,
	// but the empty inbound voter cannot yet be marked Reverted and later
	// errata votes short-circuit on BlockHeight. Re-apply that monotonic outcome
	// at the inbound's first quorum, before any vault, pool, memo, or swap side
	// effect can run.
	if err := applyCommittedXMRErrataToInboundConsensus(ctx, k, &voter); err != nil {
		return err
	}

	if voter.Reverted {
		ctx.Logger().Info("tx had been reverted", "Tx", tx.String())
		return nil
	}

	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return nil
	}

	voter.Tx.Tx.Memo = tx.Tx.Memo

	hasFinalised := voter.HasFinalised(activeNodeAccounts)

	compromised, compromisedErr := isVaultCompromised(ctx, k, vault.PubKey)
	if compromisedErr != nil {
		ctx.Logger().Error("failed to read CompromisedVault mimir, treating inbound as no-op", "pub_key", vault.PubKey.String(), "error", compromisedErr)
	}
	if compromised {
		// The tx has already reached observation quorum. Record observation
		// metadata, but deliberately skip every value-changing action below:
		// vault accounting, memo resolution, refunds, swap queueing, and
		// internal handler dispatch. Funds sent to a compromised vault should
		// not affect THORChain state.
		if err = k.SetLastChainHeight(ctx, tx.Tx.Chain, tx.BlockHeight); err != nil {
			ctx.Logger().Error("fail to set last chain height", "error", err)
		}

		mgr.ObMgr().AppendObserver(tx.Tx.Chain, voter.Tx.GetSigners())
		if hasFinalised {
			// Mark finalised no-op observations done so they do not remain as
			// pending inbound work waiting for an outbound that will never be
			// scheduled.
			emitCompromisedVaultNoOpSecurityEvent(ctx, mgr, tx, vault.PubKey)
			voter.SetDone()
			ctx.Logger().Info("observed tx in to compromised vault no-oped", "pub_key", vault.PubKey.String(), "tx", tx.Tx.ID.String())
		} else {
			ctx.Logger().Info("observed tx in to compromised vault pending confirmation counting", "pub_key", vault.PubKey.String(), "tx", tx.Tx.ID.String())
		}
		k.SetObservedTxInVoter(ctx, voter)
		return nil
	}

	// add memo for memoless transactions (after consensus is reached)
	if len(tx.Tx.Coins) > 0 {
		// Use the asset from the first coin for reference memo lookup
		refAsset := tx.Tx.Coins[0].Asset

		// Generate reference memo for memoless transactions
		if strings.TrimSpace(voter.Tx.Tx.Memo) == "" {
			var referenceID string
			referenceID, err = generateReferenceMemoID(ctx, mgr, refAsset, tx)
			if err != nil {
				ctx.Logger().Error("failed to generate reference memo", "error", err, "txid", tx.Tx.ID.String())
				// Continue without reference memo - will likely be refunded later due to empty memo
			} else {
				// Use ReferenceReadMemo's CreateMemo function
				// Note: Reference memo expiry and duplicate usage validation is handled later in fetchMemoFromReference
				refMemo := NewReferenceReadMemo(referenceID)
				voter.Tx.Tx.Memo = refMemo.CreateMemo()
			}
		}

	}

	// memo errors are ignored here and will be caught later in processing,
	// after vault update, voter setup, etc and the coin will be refunded
	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo)

	// Update vault balances from inbounds with Migrate memos immediately,
	// to minimise any gap between outbound and inbound observations.
	// TODO: In future somehow update both balances in a single action,
	// so the ActiveVault balance increase is guaranteed to never be early nor late?
	if hasFinalised || memo.IsType(TxMigrate) {
		if vault.IsAsgard() && !voter.UpdatedVault {
			if tx.Tx.FromAddress.Equals(tx.Tx.ToAddress) {
				// Don't add to or subtract from vault balances when the sender and recipient are the same
				// (particularly avoid Consolidate SafeSub zeroing of vault balances).
				voter.UpdatedVault = true
			} else if shouldCreditObservedInboundVault(ctx, mgr, tx, memo.IsType(TxMigrate), memo.GetBlockHeight()) {
				vault.AddFunds(tx.Tx.Coins)
				vault.InboundTxCount++
				voter.UpdatedVault = true
			}
		}
	}
	if err = k.SetLastChainHeight(ctx, tx.Tx.Chain, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to set last chain height", "error", err)
	}

	// save the changes in Tx Voter to key value store
	k.SetObservedTxInVoter(ctx, voter)
	if err = k.SetVault(ctx, vault); err != nil {
		ctx.Logger().Error("fail to set vault", "error", err)
		return nil
	}

	if !vault.IsAsgard() {
		ctx.Logger().Info("Vault is not an Asgard vault, transaction ignored.")
		return nil
	}

	if memo.IsOutbound() || memo.IsInternal() {
		// do not process outbound handlers here, or internal handlers
		return nil
	}

	// add addresses to observing addresses. This is used to detect
	// active/inactive observing node accounts

	mgr.ObMgr().AppendObserver(tx.Tx.Chain, voter.Tx.GetSigners())

	if !hasFinalised {
		ctx.Logger().Info("transaction pending confirmation counting", "hash", voter.TxID)
		return nil
	}

	// Resolve reference memo before any finalized refund path. In particular,
	// an inactive-vault XMR refund needs the registered ADD asset address rather
	// than the unavailable observed sender encoded by the raw r:<ref> memo.
	// Keep the parsed memo aligned as well so post-handler completion logic uses
	// the actual transaction type rather than TxReferenceReadMemo.
	// Amount-encoded memoless inbounds have their synthesized r:<ref> memo on
	// the voter, while the observed transaction memo remains empty. Preserve
	// that synthesized memo so successful processing consumes the reference.
	referenceMemo := voter.Tx.Tx.Memo
	referenceAsset := common.EmptyAsset
	if len(tx.Tx.Coins) > 0 {
		referenceAsset = tx.Tx.Coins[0].Asset
	}
	referenceResolved := false
	if resolvedMemo, ok := resolveReferenceMemo(ctx, mgr, k, &tx, &voter); ok {
		memo = resolvedMemo
		referenceResolved = true
	}

	ctx.Logger().Debug(
		"tx in finalized and has consensus",
		"id", tx.Tx.ID.String(),
		"chain", tx.Tx.Chain.String(),
		"height", tx.BlockHeight,
		"from", tx.Tx.FromAddress.String(),
		"to", tx.Tx.ToAddress.String(),
		"memo", tx.Tx.Memo,
		"coins", tx.Tx.Coins.String(),
		"gas", common.Coins(tx.Tx.Gas).String(),
		"observed_vault_pubkey", tx.ObservedPubKey.String(),
	)

	if vault.Status == InactiveVault {
		ctx.Logger().Error("observed tx on inactive vault", "tx", tx.String())
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "inactive_vault"),
			},
		)
		if newErr := refundTx(ctx, tx, mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""); newErr != nil {
			ctx.Logger().Error("fail to refund", "error", newErr)
		}
		return nil
	}

	// construct msg from memo
	m, txErr := processOneTxIn(ctx, k, voter.Tx, signer)
	if txErr != nil {
		ctx.Logger().Error("fail to process inbound tx", "error", txErr, "tx hash", tx.Tx.ID.String())
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "invalid_memo"),
			},
		)
		if newErr := refundTx(ctx, tx, mgr, CodeInvalidMemo, txErr.Error(), ""); nil != newErr {
			ctx.Logger().Error("fail to refund", "error", newErr)
		}
		return nil
	}

	// check if we've halted trading
	swapMsg, isSwap := m.(*MsgSwap)
	_, isAddLiquidity := m.(*MsgAddLiquidity)

	if isSwap || isAddLiquidity {
		if k.IsTradingHalt(ctx, m) || k.RagnarokInProgress(ctx) {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "inbound", "refund"}, 1,
				[]metrics.Label{
					telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
					telemetry.NewLabel("reason", "trading_halted"),
				},
			)
			if newErr := refundTx(ctx, tx, mgr, se.ErrUnauthorized.ABCICode(), "trading halted", ""); nil != newErr {
				ctx.Logger().Error("fail to refund for halted trading", "error", newErr)
			}
			return nil
		}
	}

	// if its a swap, send it to our queue for processing later
	if isSwap {
		if err = addSwap(ctx, mgr, *swapMsg); err != nil {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "inbound", "refund"}, 1,
				[]metrics.Label{
					telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
					telemetry.NewLabel("reason", "swap_queue_failed"),
				},
			)
			if refundErr := refundTx(ctx, tx, mgr, CodeSwapFail, err.Error(), ""); refundErr != nil {
				ctx.Logger().Error("fail to refund swap", "error", refundErr)
				return fmt.Errorf("swap failed and refund also failed: %w, refund error: %v", err, refundErr)
			}
			return nil
		}
		if referenceResolved {
			trackReferenceMemoUsage(ctx, mgr, referenceAsset, referenceMemo, tx.Tx.ID)
		}
		return nil
	}

	mCtx := ctx

	// Check and block switch assets
	// Check is independent of the mimir to enable the handler in order to support
	// bifrost & switch whitelisting prior to switching commencing
	_, isSwitch := m.(*MsgSwitch)
	if !isSwitch && len(tx.Tx.Coins) > 0 && mgr.SwitchManager().IsSwitch(ctx, tx.Tx.Coins[0].Asset) {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "switch_asset"),
			},
		)
		if err = refundTx(ctx, tx, mgr, CodeTxFail, "asset is a switch asset", ""); err != nil {
			ctx.Logger().Error("fail to refund", "error", err)
		}

		return nil
	}

	_, err = handler(mCtx, m)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "handler_failed"),
			},
		)
		if err = refundTx(ctx, tx, mgr, CodeTxFail, err.Error(), ""); err != nil {
			return fmt.Errorf("fail to refund: %w", err)
		}
		return nil
	}

	// if an outbound is not expected, mark the voter as done
	if !memo.GetType().HasOutbound() {
		// retrieve the voter from store in case the handler caused a change
		voter, err = k.GetObservedTxInVoter(ctx, tx.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to get voter")
		}
		voter.SetDone()
		k.SetObservedTxInVoter(ctx, voter)
	}
	if referenceResolved {
		trackReferenceMemoUsage(ctx, mgr, referenceAsset, referenceMemo, tx.Tx.ID)
	}

	ctx.Logger().Info("tx in processed", "chain", tx.Tx.Chain, "id", tx.Tx.ID, "finalized", tx.IsFinal())

	return nil
}

// applyCommittedXMRErrataToInboundConsensus closes the ordering gap between a
// committed errata voter and the original XMR inbound reaching quorum. It must
// not run the normal errata accounting reversal: at this point the inbound has
// not executed any value-changing path, so marking it Reverted is sufficient
// and avoids subtracting state which was never credited.
func applyCommittedXMRErrataToInboundConsensus(ctx cosmos.Context, k keeper.Keeper, voter *ObservedTxVoter) error {
	if voter == nil || voter.Reverted || voter.Tx.IsEmpty() || !voter.Tx.Tx.Chain.Equals(common.XMRChain) {
		return nil
	}
	errataVoter, err := k.GetErrataTxVoter(ctx, voter.TxID, voter.Tx.Tx.Chain)
	if err != nil {
		return fmt.Errorf("fail to get errata voter for inbound %s: %w", voter.TxID, err)
	}
	if errataVoter.BlockHeight <= 0 {
		return nil
	}
	voter.SetReverted()
	k.SetObservedTxInVoter(ctx, *voter)
	return nil
}

func shouldCreditObservedInboundVault(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, isMigrate bool, migrateHeight int64) bool {
	if !tx.Tx.Chain.Equals(common.XMRChain) || !isMigrate {
		return true
	}
	if common.IsXMRUnknownSenderAddress(tx.Tx.FromAddress) {
		ctx.Logger().Info("skip xmr migrate inbound vault credit: source vault is unknown", "tx", tx.Tx.ID.String(), "to", tx.Tx.ToAddress.String())
		return false
	}

	txOut, err := mgr.Keeper().GetTxOut(ctx, migrateHeight)
	if err != nil {
		ctx.Logger().Error("skip xmr migrate inbound vault credit: fail to get txout", "height", migrateHeight, "tx", tx.Tx.ID.String(), "error", err)
		return false
	}
	for _, item := range txOut.TxArray {
		if !xmrMigrateInboundMatchesTxOut(tx.Tx, item) {
			continue
		}
		return true
	}

	ctx.Logger().Info(
		"skip xmr migrate inbound vault credit: no matching scheduled txout",
		"tx", tx.Tx.ID.String(),
		"height", migrateHeight,
		"from", tx.Tx.FromAddress.String(),
		"to", tx.Tx.ToAddress.String(),
		"coins", tx.Tx.Coins.String(),
	)
	return false
}

func xmrMigrateInboundMatchesTxOut(tx common.Tx, item TxOutItem) bool {
	if !item.Chain.Equals(common.XMRChain) {
		return false
	}
	if !item.InHash.Equals(common.BlankTxID) {
		return false
	}
	if !common.AddressEquals(item.Chain, item.ToAddress, tx.ToAddress) {
		return false
	}

	sourcePubKey, err := txOutSourcePubKey(item)
	if err != nil {
		return false
	}
	fromAddress, err := sourcePubKey.GetAddress(item.Chain)
	if err != nil {
		return false
	}
	if !common.AddressEquals(item.Chain, fromAddress, tx.FromAddress) {
		return false
	}

	matchCoin := tx.Coins.Contains(item.Coin)
	if matchCoin {
		return true
	}

	asset := item.Chain.GetGasAsset()
	if !item.Coin.Asset.Equals(asset) {
		return false
	}
	// Inbound XMR gas is observer-reported and may not equal the actual signer fee.
	received := tx.Coins.GetCoin(asset).Amount
	minReceived := item.Coin.Amount
	maxReceived := item.Coin.Amount.Add(item.MaxGas.ToCoins().GetCoin(asset).Amount)
	return !received.LT(minReceived) && !received.GT(maxReceived)
}

// processTxOutAttestation applies a single attestation for an observed tx and saves
// the voter. MsgObservedTxOut (single attester) uses it; MsgObservedTxQuorum applies
// its attestations with applyTxOutAttestation and saves the voter once.
func processTxOutAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool) {
	voter, ok, added := applyTxOutAttestation(ctx, mgr, voter, nas, tx, signer, shouldSlashForDuplicate)
	if added {
		mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	}
	return voter, ok
}

// applyTxOutAttestation records a single attestation on the voter without saving
// it, so a quorum message can save the voter once after all its attestations.
// It returns the voter, whether this attestation brought it to consensus, and
// whether it was recorded: false for a duplicate, which leaves the voter unchanged.
func applyTxOutAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool, bool) {
	k := mgr.Keeper()
	slasher := mgr.Slasher()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := observationDelayFlexibility(ctx, k, tx.Tx.Chain)
	ok := false

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txout"),
		telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
	}))
	slashCtx = ctx.WithContext(context.WithValue(slashCtx.Context(), constants.CtxObservedTx, tx.Tx.ID.String()))

	if err := k.SetLastObserveHeight(ctx, tx.Tx.Chain, signer, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to save last observe height", "error", err, "signer", signer, "chain", tx.Tx.Chain)
	}

	// As an observation requires processing by all nodes no matter what,
	// any observation should increment ObserveSlashPoints,
	// to be decremented only if contributing to or within ObservationDelayFlexibility of consensus.
	slasher.IncSlashPoints(slashCtx, observeSlashPoints, signer)

	// A committee member's pre-broadcast "instant" observation and its later
	// scanner observation of the same tx differ only in gas (EVM) or block height
	// (all chains, from signing-tip vs inclusion) and split into separate voter
	// entries. Detect that the signer already recorded a matching variant BEFORE
	// Add records this one, so the redundant observe point can be refunded exactly
	// once below without depending on the post-consensus flex branch.
	// Refund a redundant re-observation only for the signer's FIRST prior variant
	// (exactly one earlier matching observation) — the honest "observed it, lost the
	// gas/height race, re-observed once" case. Further variants (count >= 2) are
	// self-inflicted spam and keep their observe slash point, so a node cannot file
	// unlimited gas/height variants at zero cost to erase the per-observation charge.
	redundantReObservation := voter.ShouldRefundRedundantOutboundObservation(tx, signer)

	obsChain := telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))
	obsDir := telemetry.NewLabel("direction", "out")

	if !voter.Add(tx, signer) {
		if !shouldSlashForDuplicate {
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observe", "attestation"}, 1,
			[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", "duplicate")},
		)
		// A duplicate message, so do nothing further.
		return voter, ok, false
	}

	// Single mutually-exclusive attestation result per call; late wins over redundant.
	attResult := "new"

	// The signer already observed this tx via a different gas/height variant, so
	// this observation is redundant honest work rather than a missed observation:
	// refund its observe point. The observation stays recorded above so it still
	// counts toward consensus and gas correction.
	if redundantReObservation {
		attResult = "redundant"
		slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
	}

	// Outbound memos can have | data passthrough,
	// so linked TxID extracted with memo parsing and GetTxID
	// rather than strings.Split .
	if memo, err := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo); err != nil {
		ctx.Logger().Error("fail to parse outbound memo", "error", err, "memo", tx.Tx.Memo)
	} else if inhash := memo.GetTxID(); !inhash.IsEmpty() {
		k.SetObservedLink(ctx, inhash, tx.Tx.ID)
	}

	if voter.HasFinalised(nas) {
		if voter.FinalisedHeight == 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "true")},
			)
			if voter.Height == 0 {
				ok = true
				// Record the consensus height at which outbound consensus actions are taken.
				voter.Height = ctx.BlockHeight()
				if voter.UnfinalizedHeight == 0 {
					voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
				}
			}
			voter.FinalisedHeight = ctx.BlockHeight()
			voter.Tx = *voter.GetTx(nas)

			ctx.Logger().Debug(
				"tx out finalized and has consensus",
				"id", tx.Tx.ID.String(),
				"chain", tx.Tx.Chain.String(),
				"height", tx.BlockHeight,
				"from", tx.Tx.FromAddress.String(),
				"to", tx.Tx.ToAddress.String(),
				"memo", tx.Tx.Memo,
				"coins", tx.Tx.Coins.String(),
				"gas", common.Coins(tx.Tx.Gas).String(),
				"observed_vault_pubkey", tx.ObservedPubKey.String(),
			)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetOutboundObserveSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			// Record the credited gas-agnostic observers on the consensus Tx so the
			// ObservationDelayFlexibility branch below (keyed off Tx.HasSigned) does
			// not refund the same node a second time when its own gas-used
			// observation arrives after consensus has already formed.
			voter.RecordObserveSigners(signers)
			queueObservationTally(ctx, k, observationTallyKindTxOut, tx.Tx.Chain, tx.Tx.ID.String())
		} else if ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.EqualsOutboundObservationCredit(tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so we still take away their slash points
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
			attResult = "late"
		}

		// Gas correction for re-org re-observations.
		// When a voter is finalized but a re-observation has the same tx except gas
		// (likely due to a reorg changing the effective gas price), check if enough
		// nodes agree on the corrected gas to update vault accounting.
		if voter.FinalisedHeight > 0 &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			!voter.Tx.Tx.Gas.Equals(tx.Tx.Gas) &&
			voter.Tx.EqualsConsensusIgnoreGas(tx) {
			gasCorrectionSigners := countMatchingSigners(voter, tx, nas)
			if HasSuperMajority(gasCorrectionSigners, len(nas)) {
				correctOutboundGas(ctx, mgr, voter.Tx, tx)
				voter.Tx.Tx.Gas = tx.Tx.Gas
			}
		}
	}
	if !ok && voter.HasConsensus(nas) && !tx.IsFinal() && voter.FinalisedHeight == 0 {
		if voter.Height == 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "false")},
			)
			ok = true
			// Record the consensus height at which outbound consensus actions are taken,
			// even if not yet Finalised.
			voter.Height = ctx.BlockHeight()
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			// this is the tx that has consensus
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetOutboundObserveSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			// Record the credited gas-agnostic observers on the consensus Tx so the
			// ObservationDelayFlexibility branch below (keyed off Tx.HasSigned) does
			// not refund the same node a second time when its own gas-used
			// observation arrives after consensus has already formed.
			voter.RecordObserveSigners(signers)
		} else if ctx.BlockHeight() <= (voter.Height+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.EqualsOutboundObservationCredit(tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
			attResult = "late"
		}

		// Gas correction for re-org re-observations (non-finalized consensus path).
		if voter.Height > 0 &&
			!voter.Tx.Tx.Gas.Equals(tx.Tx.Gas) &&
			voter.Tx.EqualsConsensusIgnoreGas(tx) {
			gasCorrectionSigners := countMatchingSigners(voter, tx, nas)
			if HasSuperMajority(gasCorrectionSigners, len(nas)) {
				correctOutboundGas(ctx, mgr, voter.Tx, tx)
				voter.Tx.Tx.Gas = tx.Tx.Gas
			}
		}
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "observe", "attestation"}, 1,
		[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", attResult)},
	)

	return voter, ok, true
}

// countMatchingSigners counts the number of unique active node signers that have observed
// the exact same tx (including gas) as the given observation.
func countMatchingSigners(voter ObservedTxVoter, tx ObservedTx, nas NodeAccounts) int {
	signers := make(map[string]struct{})
	for _, otherTx := range voter.Txs {
		if otherTx.IsFinal() != tx.IsFinal() {
			continue
		}
		if !otherTx.ObservedPubKey.Equals(tx.ObservedPubKey) {
			continue
		}
		if !otherTx.EqualsConsensus(tx) {
			continue
		}
		for _, s := range otherTx.GetSigners() {
			if !nas.IsNodeKeys(s) {
				continue
			}
			signers[s.String()] = struct{}{}
		}
	}
	return len(signers)
}

// correctOutboundGas adjusts a vault's gas accounting when a reorg results in
// a different gas amount for the same outbound transaction. It computes the delta
// between the original consensus gas and the corrected gas, then adjusts the vault
// balance and gas manager accordingly.
func correctOutboundGas(ctx cosmos.Context, mgr Manager, consensusTx common.ObservedTx, correctedTx ObservedTx) {
	oldGas := consensusTx.Tx.Gas
	newGas := correctedTx.Tx.Gas

	vault, err := mgr.Keeper().GetVault(ctx, correctedTx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault for gas correction", "error", err)
		return
	}

	// Calculate gas delta per asset and adjust vault balance.
	var additionalGas common.Gas
	for _, newGasCoin := range newGas {
		oldAmount := oldGas.ToCoins().GetCoin(newGasCoin.Asset).Amount
		if newGasCoin.Amount.GT(oldAmount) {
			// More gas was actually spent - deduct additional from vault.
			delta := common.NewCoin(newGasCoin.Asset, newGasCoin.Amount.Sub(oldAmount))
			debitVault(ctx, mgr, &vault, consensusTx, common.NewCoins(delta), vaultDeficitReasonOutboundGasCorrection)
			additionalGas = additionalGas.Add(delta)
		} else if oldAmount.GT(newGasCoin.Amount) {
			// Less gas was actually spent - credit back to vault.
			delta := common.NewCoin(newGasCoin.Asset, oldAmount.Sub(newGasCoin.Amount))
			vault.AddFunds(common.NewCoins(delta))
		}
	}

	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		ctx.Logger().Error("fail to save vault after gas correction", "error", err)
		return
	}

	// If more gas was spent, add the delta to the gas manager for reserve reimbursement,
	// skipping reimbursement for inactive vaults and during ragnarok (matching addGasFees behavior).
	if !additionalGas.IsEmpty() {
		if vault.Status == InactiveVault {
			return
		}
		if mgr.Keeper().RagnarokInProgress(ctx) {
			gasAsset := correctedTx.Tx.Chain.GetGasAsset()
			if !correctedTx.Tx.Coins.GetCoin(gasAsset).IsEmpty() {
				return
			}
		}
		outAsset := common.EmptyAsset
		if len(correctedTx.Tx.Coins) != 0 {
			outAsset = correctedTx.Tx.Coins[0].Asset
		}
		mgr.GasMgr().AddGasAsset(outAsset, additionalGas, false)
	}

	ctx.Logger().Info(
		"corrected outbound gas after reorg",
		"txid", correctedTx.Tx.ID,
		"old_gas", common.Coins(oldGas).String(),
		"new_gas", common.Coins(newGas).String(),
	)
}

// ensureVaultAndGetTxOutVoter will make sure the vault exists, then get the ObservedTxOutVoter from the store.
// if it doesn't exist, it will create a new one.
func ensureVaultAndGetTxOutVoter(ctx cosmos.Context, k keeper.Keeper, vaultPubKey common.PubKey, txID common.TxID, observers []cosmos.AccAddress, keysignMs int64) (ObservedTxVoter, error) {
	// check we are sending from a valid vault
	if !k.VaultExists(ctx, vaultPubKey) {
		ctx.Logger().Info("Not valid Observed Pubkey", "observed pub key", vaultPubKey)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txout_vault_not_found")},
		)
		return ObservedTxVoter{}, fmt.Errorf("vault not found for observed tx out pubkey: %s", vaultPubKey)
	}

	if keysignMs > 0 {
		keysignMetric, err := k.GetTssKeysignMetric(ctx, txID)
		if err != nil {
			ctx.Logger().Error("fail to get keysign metric", "error", err)
		} else {
			for _, o := range observers {
				keysignMetric.AddNodeTssTime(o, keysignMs)
			}
			k.SetTssKeysignMetric(ctx, keysignMetric)
		}
	}

	voter, err := k.GetObservedTxOutVoter(ctx, txID)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txout_voter_read_fail")},
		)
		return ObservedTxVoter{}, fmt.Errorf("fail to get tx out voter: %w", err)
	}

	return voter, nil
}

// handleObservedTxOutQuorum - will process the observed tx out quorum.
// used by both MsgObservedTxOut and MsgObservedTxOutQuorum after processing
// attestation(s).
func handleObservedTxOutQuorum(
	ctx cosmos.Context,
	mgr Manager,
	signer cosmos.AccAddress,
	activeNodeAccounts NodeAccounts,
	handler cosmos.Handler,
	tx common.ObservedTx,
	voter ObservedTxVoter,
	observers []cosmos.AccAddress,
	isQuorum bool,
) error {
	// check whether the tx has consensus
	if !isQuorum {
		if voter.Height == ctx.BlockHeight() || voter.FinalisedHeight == ctx.BlockHeight() {
			// we've already process the transaction, but we should still
			// update the observing addresses
			mgr.ObMgr().AppendObserver(tx.Tx.Chain, observers)
		}
		if len(tx.SpentOutputRefs) > 0 && (voter.Height > 0 || voter.FinalisedHeight > 0) {
			saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, tx, activeNodeAccounts)
		}
		// A late final vote can complete the durable-nonce supermajority after the
		// outbound itself reached consensus. If that commit is a nonce-account creation
		// whose processing was deferred at quorum (below), book it now, exactly once:
		// the commit fires once per value.
		// Gated on the chain, not on this vote: the tally reads every recorded vote,
		// so a vote that omits SolNonce can still be the one that completes it.
		if tx.Tx.Chain.Equals(common.SOLChain) && saveSolVaultNonceWithQuorum(ctx, mgr, voter, activeNodeAccounts) &&
			voter.Height > 0 && isSolNonceCreateTx(voter.Tx) && solNonceCommittedBy(ctx, mgr, voter.Tx) {
			bookSolNonceCreateAsGas(ctx, mgr, voter.Tx)
		}
		return nil
	}

	k := mgr.Keeper()

	// Commit the vault's durable nonce BEFORE any early return below: every branch
	// (cancel/approval, inbound-memo slash, normal outbound) is a vault spend that
	// can advance the nonce. Gated on the chain, not on this vote (see above).
	if tx.Tx.Chain.Equals(common.SOLChain) {
		saveSolVaultNonceWithQuorum(ctx, mgr, voter, activeNodeAccounts)
	}

	// A Solana vault's durable-nonce account creation: the rent it moved into the nonce
	// account (plus the fee) is booked as vault gas, so the vault's recorded balance
	// keeps matching its main account (migrations never overshoot, solvency needs no
	// adjustment). It is an operational spend: no outbound to match, no slash. Checked
	// before the memo-based branches below, which would treat it as unmatched.
	if isSolNonceCreateTx(tx) {
		if solNonceCommittedBy(ctx, mgr, tx) {
			bookSolNonceCreateAsGas(ctx, mgr, tx)
			return nil
		}
		// Outbound consensus ignores SolNonce, so it can form before the nonce
		// supermajority does (one vote omitting or changing the nonce is enough). If
		// any final consensus vote attests this as the vault's nonce-account creation,
		// defer rather than slash it as an unmatched outflow: the late vote that
		// commits the nonce books it (above). Only a real creation carries a SolNonce
		// for this shape; bifrost never sends a durable payment to the nonce address.
		//
		// Residual: a single byzantine vote can attach a SolNonce to a genuine memoless
		// transfer of this shape and so defer it instead of slashing it. Every such
		// transfer pays the vault's OWN seed-derived nonce account, so the funds stay
		// under vault control and the attacker gains nothing; if no nonce ever commits,
		// the rent + fee (<= solNonceCreateMaxRent) stays unbooked, a bounded
		// accounting drift, not a loss.
		if voterAttestsSolNonceCreate(voter, tx) {
			ctx.Logger().Info("deferring durable-nonce account creation until its nonce commits", "txid", tx.Tx.ID)
			return nil
		}
	}

	if isCancelOrApprovalTx(tx) {
		ctx.Logger().Info("skipping slash for cancel tx with empty memo", "txid", tx.Tx.ID)
		// Credit gas to gas manager and deduct from vault (legit operational spend, no penalty)
		// This also adds to fee_spent_rune for dynamic outbound fee calculation
		if err := addGasFees(ctx, mgr, tx); err != nil {
			ctx.Logger().Error("fail to add gas fees for cancel tx", "error", err)
		}
		return nil
	}

	// if memo isn't valid or its an inbound memo, slash the vault
	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo)
	if memo.IsInbound() {
		vault, err := k.GetVault(ctx, tx.ObservedPubKey)
		if err != nil {
			ctx.Logger().Error("fail to get vault", "error", err)
			return nil
		}
		toSlash := make(common.Coins, len(tx.Tx.Coins))
		copy(toSlash, tx.Tx.Coins)
		toSlash = toSlash.Add(tx.Tx.Gas.ToCoins()...)

		slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
			telemetry.NewLabel("reason", "sent_extra_funds"),
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
		}))

		if err := mgr.Slasher().SlashVault(slashCtx, tx.ObservedPubKey, toSlash, mgr); err != nil {
			ctx.Logger().Error("fail to slash account for sending extra fund", "error", err)
		}
		// The inbound-memo slash debit has the same silent-drop risk as a normal
		// outbound debit: bifrost reports the vault sent toSlash, but THORChain's
		// tracked balance may be below that. Record any underflow as deficit and
		// surface it instead of losing the accounting gap.
		debitVault(ctx, mgr, &vault, tx, toSlash, vaultDeficitReasonVaultSlash)
		if err := k.SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		} else if tx.Tx.Chain.RequiresTerminalErrataOutcome() {
			// Record the principal debit only once it is stored, so errata
			// restores exactly what this observation removed from the vault.
			voter.UpdatedVault = true
			k.SetObservedTxOutVoter(ctx, voter)
		}

		return nil
	}

	// Process the handler if we can get the consensus tx.
	// Even if the handler fails, vault accounting must still happen because
	// the outbound transaction is irrevocable (funds already left the vault).
	vaultSlash := false
	slashObservedOutbound := func(reason string, err error) {
		ctx.Logger().Error("slash observed outbound", "reason", reason, "error", err, "tx", tx.Tx.String())
		toSlash := make(common.Coins, len(tx.Tx.Coins))
		copy(toSlash, tx.Tx.Coins)
		toSlash = toSlash.Add(tx.Tx.Gas.ToCoins()...)

		slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
			telemetry.NewLabel("reason", reason),
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
		}))

		if slashErr := mgr.Slasher().SlashVault(slashCtx, tx.ObservedPubKey, toSlash, mgr); slashErr != nil {
			ctx.Logger().Error("fail to slash account for observed outbound", "error", slashErr)
		} else {
			vaultSlash = true
		}
	}
	txOut := voter.GetTx(activeNodeAccounts) // get consensus tx, in case our for loop is incorrect
	if txOut == nil || txOut.IsEmpty() {
		ctx.Logger().Error("fail to get consensus tx from voter", "txid", tx.Tx.ID)
		slashObservedOutbound("missing_consensus_tx", nil)
	} else {
		txOut.Tx.Memo = tx.Tx.Memo

		// For memoless outbounds, look up the original memo from the scheduled TxOutItem
		if txOut.Tx.Memo == "" {
			if originalMemo := findOriginalMemoForOutbound(ctx, mgr, tx); originalMemo != "" {
				txOut.Tx.Memo = originalMemo
				ctx.Logger().Info("resolved memoless outbound", "original_memo", originalMemo, "txid", tx.Tx.ID)
				// The per-vote link above parsed the empty raw memo, so a memoless
				// outbound has no observed link. The SOL durable-nonce landed-copy hold
				// finds copies only through that link: record it from the recovered memo.
				if tx.Tx.Chain.Equals(common.SOLChain) {
					if recovered, mErr := ParseMemoWithTHORNames(ctx, k, originalMemo); mErr == nil {
						if inhash := recovered.GetTxID(); !inhash.IsEmpty() {
							k.SetObservedLink(ctx, inhash, tx.Tx.ID)
						}
					}
				}
			}
		}

		if tx.Tx.Chain.IsEmpty() {
			ctx.Logger().Error("fail to process txOut: chain is empty", "tx", tx.Tx.String())
			slashObservedOutbound("empty_chain", nil)
		} else if m, mErr := processOneTxIn(ctx, k, *txOut, signer); mErr != nil {
			ctx.Logger().Error("fail to process txOut",
				"error", mErr,
				"tx", tx.Tx.String())
			slashObservedOutbound("invalid_outbound", mErr)
		} else {
			// add addresses to observing addresses. This is used to detect
			// active/inactive observing node accounts
			mgr.ObMgr().AppendObserver(tx.Tx.Chain, txOut.GetSigners())

			// emit tss keysign metrics
			if tx.KeysignMs > 0 {
				keysignMetric, kmErr := k.GetTssKeysignMetric(ctx, tx.Tx.ID)
				if kmErr != nil {
					ctx.Logger().Error("fail to get tss keysign metric", "error", kmErr, "hash", tx.Tx.ID)
				} else {
					medianKeysignMs := keysignMetric.GetMedianTime()
					evt := NewEventTssKeysignMetric(keysignMetric.TxID, medianKeysignMs)
					if emitErr := mgr.EventMgr().EmitEvent(ctx, evt); emitErr != nil {
						ctx.Logger().Error("fail to emit tss metric event", "error", emitErr)
					}
					// median keysign time climbs well before a degrading signing
					// party starts producing outright MsgTssKeysignFail
					telemetry.SetGaugeWithLabels(
						[]string{"thornode", "tss", "keysign", "time_ms"},
						float32(medianKeysignMs),
						[]metrics.Label{telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))},
					)
				}
			}

			res, hErr := handler(ctx, m)
			if hErr != nil {
				ctx.Logger().Error("handler failed:", "error", hErr)
				slashObservedOutbound("failed_outbound", hErr)
			} else if res != nil && res.Events != nil {
				for _, ev := range res.Events {
					if ev.Type == "vault-slash" {
						vaultSlash = true
						break
					}
				}
			}
		}
	}

	// Vault accounting - always runs because outbound is irrevocable.
	// Gas and coin deductions must happen regardless of handler success.

	// only deduct gas fee via the manager if there was not a vault slash that covered it
	if !vaultSlash {
		if err := addGasFees(ctx, mgr, tx); err != nil {
			ctx.Logger().Error("fail to add gas fee", "error", err)
		}
	}

	// If sending from one of our vaults, decrement coins
	principalDebited := false
	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
	} else {
		// if the vault was slashed we skipped the gas manager above and deduct gas directly
		if vaultSlash {
			// The slashed-vault gas debit can underflow the same way as the main
			// coin debit when the vault has insufficient gas reserve.
			debitVault(ctx, mgr, &vault, tx, tx.Tx.Gas.ToCoins(), vaultDeficitReasonVaultSlashGas)
		}

		// Don't add to or subtract from vault balances when the sender and recipient are the same
		// (particularly avoid Consolidate SafeSub zeroing of vault balances).
		if !tx.Tx.FromAddress.Equals(tx.Tx.ToAddress) {
			// skip deducting funds for outbound fake gas
			if !isOutboundFakeGasTx(tx) {
				// An outbound observation should normally be covered by the vault's
				// tracked balance. If it is not, the L1 already sent the funds, so
				// THORChain must record the deficit and halt/signal rather than keep
				// phantom credit on the vault.
				debitVault(ctx, mgr, &vault, tx, tx.Tx.Coins, vaultDeficitReasonOutbound)
				principalDebited = true
			}
			vault.OutboundTxCount++
		}

		if vault.IsAsgard() && memo.IsType(TxMigrate) {
			// only remove the block height that had been specified in the memo
			vault.RemovePendingTxBlockHeights(memo.GetBlockHeight())
		}

		// RetiringVault status should clear once all real coins are drained,
		// even if deficit accounting still remains on the vault.
		if !vault.HasFunds() && vault.Status == RetiringVault {
			// We have successfully removed all funds from a retiring vault;
			// mark it inactive so it no longer blocks churn or unbonding.
			if vault.HasDeficit() {
				ctx.Logger().Info(
					"Retiring vault has no funds but still has deficit; marking inactive",
					"vault_pubkey", vault.PubKey,
					"deficit", vault.Deficit.String(),
				)
			}
			vault.UpdateStatus(InactiveVault, ctx.BlockHeight())
		}
		// if the vault is frozen, then unfreeze it. Since we saw that a
		// transaction was signed
		for _, coin := range tx.Tx.Coins {
			for i := range vault.Frozen {
				if strings.EqualFold(coin.Asset.GetChain().String(), vault.Frozen[i]) {
					vault.Frozen = append(vault.Frozen[:i], vault.Frozen[i+1:]...)
					break
				}
			}
		}
		if err := k.SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		} else if principalDebited && tx.Tx.Chain.RequiresTerminalErrataOutcome() {
			// Record the principal debit only once it is stored, so errata
			// restores exactly what this observation removed from the vault.
			voter.UpdatedVault = true
		}
	}
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, tx, activeNodeAccounts)

	// Mark voter as done AFTER all vault operations to prevent
	// inconsistency if vault ops fail.
	voter.SetDone()
	k.SetObservedTxOutVoter(ctx, voter)

	ctx.Logger().Info("tx out processed", "chain", tx.Tx.Chain, "id", tx.Tx.ID, "finalized", tx.IsFinal())

	return nil
}

// saveSolVaultNonceWithQuorum commits a Solana vault's durable nonce from the
// observations of the vault transaction that set it: the nonce account's creation,
// or a durable outbound that advanced it. It writes only when
//   - the transaction itself reached FINAL consensus (voter.Tx), so a nonce advance
//     never outruns the consensus record LackSigning uses to detect a landed copy,
//   - a supermajority of active validators attested the IDENTICAL SolNonce on FINAL
//     observations of that consensus tx (no "absent matches anything" rule),
//   - the nonce account is the vault's own seed-derived account, and
//   - the attested slot is newer than the stored one (idempotent and monotonic).
//
// Signers build every durable outbound from this committed value, so the value a
// vault's durable txs use changes only here, when the tx that consumed the previous
// value is committed. It is called on every observation of the transaction, so a
// late final vote that completes the supermajority still commits. It never affects
// outbound processing: SolNonce is deliberately not part of EqualsConsensus.
// It reports whether this call committed a new value.
func saveSolVaultNonceWithQuorum(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, nas NodeAccounts) bool {
	consensus := voter.Tx
	if voter.FinalisedHeight <= 0 || consensus.IsEmpty() || !consensus.IsFinal() {
		return false
	}
	hasCandidate := false
	for _, v := range voter.Txs {
		if v.SolNonce != nil {
			hasCandidate = true
			break
		}
	}
	if !hasCandidate {
		return false
	}
	// Late votes after the value is committed skip the tally (commit is idempotent).
	var committedSlot int64
	if vault, err := mgr.Keeper().GetVault(ctx, consensus.ObservedPubKey); err == nil {
		if existing, found, err := mgr.Keeper().GetSolVaultNonce(ctx, vault.PubKey); err == nil && found {
			committedSlot = existing.Slot
		}
	}
	for _, candidate := range voter.Txs {
		if candidate.SolNonce == nil || !candidate.Tx.Chain.Equals(common.SOLChain) || !candidate.IsFinal() ||
			candidate.SolNonce.Slot <= committedSlot {
			continue
		}
		// Outbound processing rewrites a memoless outbound's consensus memo to the
		// recovered one (voter.Tx aliases the stored consensus), so raw votes would no
		// longer match it and a late vote could never complete the tally. Compare on
		// the candidate's raw memo instead: counted votes still agree with each other
		// on the memo and with the consensus on every other field.
		ref := consensus
		ref.Tx.Memo = candidate.Tx.Memo
		voters := make(map[string]struct{})
		for _, tx := range voter.Txs {
			if tx.SolNonce == nil || !tx.IsFinal() || !tx.EqualsConsensus(ref) {
				continue
			}
			if !tx.Tx.ID.Equals(candidate.Tx.ID) ||
				!tx.Tx.FromAddress.Equals(candidate.Tx.FromAddress) ||
				!tx.ObservedPubKey.Equals(candidate.ObservedPubKey) {
				continue
			}
			if !common.SolNonceUpdatesEqual(tx.SolNonce, candidate.SolNonce) {
				continue
			}
			for _, signer := range tx.GetSigners() {
				if !nas.IsNodeKeys(signer) {
					continue
				}
				voters[signer.String()] = struct{}{}
			}
		}
		if !HasSuperMajority(len(voters), len(nas)) {
			continue
		}
		return commitSolVaultNonce(ctx, mgr, candidate)
	}
	return false
}

// commitSolVaultNonce writes an agreed durable-nonce update for the observing vault,
// after checking it names the vault's own seed-derived nonce account and is newer
// than what is stored.
func commitSolVaultNonce(ctx cosmos.Context, mgr Manager, obs common.ObservedTx) bool {
	k := mgr.Keeper()
	vault, err := k.GetVault(ctx, obs.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault for sol durable nonce", "pubkey", obs.ObservedPubKey, "error", err)
		return false
	}
	vaultAddr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil || !vaultAddr.Equals(obs.Tx.FromAddress) {
		ctx.Logger().Error("sol durable nonce observation is not from the vault's SOL address",
			"vault", vault.PubKey, "from", obs.Tx.FromAddress, "error", err)
		return false
	}
	expected, err := common.SolNonceAccountAddress(obs.Tx.FromAddress.String())
	if err != nil || expected != obs.SolNonce.NonceAccount {
		ctx.Logger().Error("sol durable nonce account is not the vault's seed-derived account",
			"vault", vault.PubKey, "from", obs.Tx.FromAddress, "attested", obs.SolNonce.NonceAccount, "expected", expected, "error", err)
		return false
	}
	existing, found, err := k.GetSolVaultNonce(ctx, vault.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to get sol vault nonce", "vault", vault.PubKey, "error", err)
		return false
	}
	if found && existing.Slot >= obs.SolNonce.Slot {
		return false // already committed (idempotent), or an older update
	}
	// The previous ACTIVE commit (none on a first commit or after a break-glass
	// clear) bounds which pending outbounds were signed durably with a now-consumed
	// nonce and can be re-issued straight away (reissueSupersededSolDurable).
	prev, prevActive, err := activeSolVaultNonce(ctx, k, vault.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to get active sol vault nonce", "vault", vault.PubKey, "error", err)
		prevActive = false
	}
	k.SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey:   vault.PubKey,
		NonceAccount:  obs.SolNonce.NonceAccount,
		Nonce:         obs.SolNonce.Nonce,
		Slot:          obs.SolNonce.Slot,
		UpdatedHeight: ctx.BlockHeight(),
		SetByTx:       obs.Tx.ID,
	})
	ctx.Logger().Info("committed sol durable nonce", "vault", vault.PubKey, "nonce", obs.SolNonce.Nonce,
		"slot", obs.SolNonce.Slot, "set_by_tx", obs.Tx.ID)
	if prevActive {
		reissueSupersededSolDurable(ctx, mgr, vault, prev.UpdatedHeight, &obs)
	}
	return true
}

// bookSolNonceCreateAsGas books a nonce-account creation's rent plus fee as vault gas.
func bookSolNonceCreateAsGas(ctx cosmos.Context, mgr Manager, tx common.ObservedTx) {
	gasTx := tx
	gasTx.Tx.Gas = common.Gas(tx.Tx.Gas.ToCoins().Add(tx.Tx.Coins...)) // Add returns a new slice
	if err := addGasFees(ctx, mgr, gasTx); err != nil {
		ctx.Logger().Error("fail to book durable-nonce account rent as gas", "error", err)
	}
}

// voterAttestsSolNonceCreate reports whether any final vote matching tx's consensus
// fields attests a durable nonce for tx's destination (the vault's nonce account).
func voterAttestsSolNonceCreate(voter ObservedTxVoter, tx common.ObservedTx) bool {
	for _, v := range voter.Txs {
		if v.SolNonce != nil && v.IsFinal() && v.EqualsConsensus(tx) && v.SolNonce.NonceAccount == tx.Tx.ToAddress.String() {
			return true
		}
	}
	return false
}

// solNonceCommittedBy reports whether tx is the transaction that set the observing
// vault's committed durable nonce, i.e. a supermajority attested the nonce it
// created (saveSolVaultNonceWithQuorum runs just before this check). This is the
// agreed evidence that a memoless transfer to the nonce account is the account's
// creation and not a scheduled (memoless) payment to that address.
func solNonceCommittedBy(ctx cosmos.Context, mgr Manager, tx common.ObservedTx) bool {
	vault, err := mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		return false
	}
	record, found, err := mgr.Keeper().GetSolVaultNonce(ctx, vault.PubKey)
	return err == nil && found && record.SetByTx.Equals(tx.Tx.ID)
}

// saveXMRSpentOutputRefsWithQuorum persists XMR spent-output refs for the
// voter's consensus outbound once a supermajority of active nodes has
// reported matching refs. Safe to call repeatedly: it skips the write when
// the refs are already stored.
func saveXMRSpentOutputRefsWithQuorum(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, tx common.ObservedTx, activeNodeAccounts NodeAccounts) {
	if !tx.Tx.Chain.Equals(common.XMRChain) {
		return
	}
	if len(tx.SpentOutputRefs) == 0 {
		return
	}
	if voter.Height <= 0 && voter.FinalisedHeight <= 0 {
		return
	}

	// Spent refs are side metadata for the outbound that already reached
	// consensus. Do not let a separate ref-bearing observation select its own
	// outbound; it must attach to the voter's consensus tx.
	consensusTx := voter.GetTx(activeNodeAccounts)
	if consensusTx == nil || consensusTx.IsEmpty() {
		return
	}

	refTx := findSpentOutputRefs(*consensusTx, voter, activeNodeAccounts)
	if len(refTx.SpentOutputRefs) == 0 {
		return
	}

	existing, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, refTx.ObservedPubKey)
	existingSet := make(map[string]struct{}, len(existing))
	if err != nil {
		ctx.Logger().Error("fail to get xmr spent output refs", "error", err)
	} else {
		for _, ref := range existing {
			existingSet[common.SpentOutputRefIdentity(ref)] = struct{}{}
		}
	}

	allExisting := len(existingSet) > 0
	for _, ref := range refTx.SpentOutputRefs {
		if _, ok := existingSet[common.SpentOutputRefIdentity(ref)]; !ok {
			allExisting = false
			break
		}
	}
	if allExisting {
		return
	}

	if err := mgr.Keeper().SetXMRSpentOutputRefs(ctx, refTx.ObservedPubKey, refTx.Tx.ID, refTx.SpentOutputRefs, refTx.BlockHeight, ctx.BlockHeight()); err != nil {
		ctx.Logger().Error("fail to save xmr spent output refs", "error", err)
	}
}

// findSpentOutputRefs returns the observed tx whose spent-output refs reached
// supermajority for this outbound voter's consensus tx.
func findSpentOutputRefs(consensusTx common.ObservedTx, voter ObservedTxVoter, nas NodeAccounts) common.ObservedTx {
	for _, candidate := range voter.Txs {
		// Candidate ref sets are only eligible when their outbound payload is the
		// same payload that already reached consensus.
		if !matchesConsensusOutboundIgnoringSpentRefs(candidate, consensusTx) || len(candidate.SpentOutputRefs) == 0 {
			continue
		}

		voters := make(map[string]struct{})
		for _, tx := range voter.Txs {
			if tx.IsFinal() != candidate.IsFinal() {
				continue
			}
			if !tx.ObservedPubKey.Equals(candidate.ObservedPubKey) {
				continue
			}
			if !tx.Tx.EqualsEx(candidate.Tx) {
				continue
			}
			if !tx.HasEquivalentXMRTxKey(candidate) {
				continue
			}
			// Count only observations that both agree with the candidate ref set
			// and still bind back to the consensus outbound.
			if !matchesConsensusOutboundIgnoringSpentRefs(tx, consensusTx) {
				continue
			}
			if !common.SpentOutputRefsEqual(tx.SpentOutputRefs, candidate.SpentOutputRefs) {
				continue
			}
			for _, signer := range tx.GetSigners() {
				if !nas.IsNodeKeys(signer) {
					continue
				}
				voters[signer.String()] = struct{}{}
			}
		}

		if HasSuperMajority(len(voters), len(nas)) {
			return candidate
		}
	}

	return common.ObservedTx{}
}

// matchesConsensusOutboundIgnoringSpentRefs binds spent-ref metadata to the
// already-consensed outbound payload. XMR observers can report the same
// outbound at different external heights, so height fields are not part of
// this metadata identity check.
func matchesConsensusOutboundIgnoringSpentRefs(tx, consensusTx common.ObservedTx) bool {
	if !tx.Tx.Chain.Equals(common.XMRChain) || !consensusTx.Tx.Chain.Equals(common.XMRChain) {
		return false
	}
	if tx.IsFinal() != consensusTx.IsFinal() {
		return false
	}
	if !tx.Tx.EqualsEx(consensusTx.Tx) {
		return false
	}
	if !tx.ObservedPubKey.Equals(consensusTx.ObservedPubKey) {
		return false
	}
	if !strings.EqualFold(tx.Aggregator, consensusTx.Aggregator) {
		return false
	}
	if !strings.EqualFold(tx.AggregatorTarget, consensusTx.AggregatorTarget) {
		return false
	}

	emptyAmt := cosmos.ZeroUint()
	if tx.AggregatorTargetLimit == nil {
		tx.AggregatorTargetLimit = &emptyAmt
	}
	if consensusTx.AggregatorTargetLimit == nil {
		consensusTx.AggregatorTargetLimit = &emptyAmt
	}
	if !tx.AggregatorTargetLimit.Equal(*consensusTx.AggregatorTargetLimit) {
		return false
	}

	return tx.HasEquivalentXMRTxKey(consensusTx)
}

type vaultDeficitReason string

const (
	vaultDeficitReasonOutbound              vaultDeficitReason = "outbound"
	vaultDeficitReasonOutboundGas           vaultDeficitReason = "outbound_gas"
	vaultDeficitReasonOutboundGasCorrection vaultDeficitReason = "outbound_gas_correction"
	vaultDeficitReasonVaultSlash            vaultDeficitReason = "vault_slash"
	vaultDeficitReasonVaultSlashGas         vaultDeficitReason = "vault_slash_gas"
	vaultDeficitReasonInboundErrataReversal vaultDeficitReason = "inbound_errata_reversal"
	vaultDeficitReasonNoVault               vaultDeficitReason = "novault"
)

// debitVault subtracts funds and handles any newly-created deficit.
// It mutates the vault in memory; callers are still responsible for SetVault.
// The requested coins are explicit so gas and main coin debits share the same path.
func debitVault(ctx cosmos.Context, mgr Manager, vault *Vault, tx common.ObservedTx, requested common.Coins, reason vaultDeficitReason) {
	if missed := vault.SubFundsWithDeficit(requested); !missed.IsEmpty() {
		handleVaultDeficitCreated(ctx, mgr, mgr.Keeper(), *vault, tx, requested, missed, reason)
	}
}

// handleVaultDeficitCreated emits security signals for newly-created vault deficit.
func handleVaultDeficitCreated(ctx cosmos.Context, mgr Manager, k keeper.Keeper, vault Vault, tx common.ObservedTx, requested common.Coins, missed common.Coins, reason vaultDeficitReason) {
	requestedByAsset := common.Coins{}.Add(requested...).NoneEmpty()
	missedByAsset := common.Coins{}.Add(missed...).NoneEmpty()
	for _, coin := range missedByAsset {
		requestedAmount := requestedByAsset.GetCoin(coin.Asset).Amount
		accountedFromCoins := common.SafeSub(requestedAmount, coin.Amount)
		deficitTotal := vault.GetDeficit(coin.Asset).Amount
		msg := fmt.Sprintf(
			"vault deficit created: vault=%s tx=%s reason=%s asset=%s requested=%s accounted_from_coins=%s deficit_delta=%s deficit_total=%s",
			vault.PubKey, tx.Tx.ID, reason, coin.Asset, requestedAmount, accountedFromCoins, coin.Amount, deficitTotal,
		)
		ctx.Logger().Error(msg)

		// The ledger just discovered missing funds. Today this produces a log line
		// and an on-chain security event and nothing scrapeable; the reason label
		// discriminates benign gas rounding from funds-missing.
		deficitLabels := []metrics.Label{
			telemetry.NewLabel("reason", string(reason)),
			telemetry.NewLabel("asset", coin.Asset.String()),
		}
		telemetry.IncrCounterWithLabels([]string{"thornode", "vault", "deficit_created"}, 1, deficitLabels)
		telemetry.IncrCounterWithLabels([]string{"thornode", "vault", "deficit_created_amount"}, telem(coin.Amount), deficitLabels)

		if err := mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx.Tx, msg)); err != nil {
			ctx.Logger().Error("fail to emit vault deficit security event", "error", err)
		}

		if shouldHaltForVaultDeficit(ctx, mgr, k, reason, common.NewCoin(coin.Asset, deficitTotal)) {
			// Halt signing and trading, not the chain-scanner solvency halt. Bifrost
			// should keep observing while THORChain stops new outbounds and trades
			// against the affected chain until operators investigate and unhalt.
			chain := coin.Asset.GetChain()
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "vault", "deficit_halt"},
				1,
				[]metrics.Label{
					telemetry.NewLabel("chain", chain.String()),
					telemetry.NewLabel("reason", string(reason)),
				},
			)
			setHaltMimirIfNotPreserved(ctx, k, mgr, fmt.Sprintf(constants.MimirTemplateHaltSigning, chain))
			setHaltMimirIfNotPreserved(ctx, k, mgr, fmt.Sprintf(constants.MimirTemplateHaltTrading, chain))
		}
	}
}

// shouldHaltForVaultDeficit applies reason-specific halt policy for new deficits.
func shouldHaltForVaultDeficit(ctx cosmos.Context, mgr Manager, k keeper.Keeper, reason vaultDeficitReason, deficit common.Coin) bool {
	switch reason {
	case vaultDeficitReasonOutbound, vaultDeficitReasonVaultSlash, vaultDeficitReasonVaultSlashGas, vaultDeficitReasonNoVault:
		return true
	case vaultDeficitReasonOutboundGas, vaultDeficitReasonOutboundGasCorrection:
		return gasDeficitCrossesHaltThreshold(ctx, mgr, k, deficit)
	case vaultDeficitReasonInboundErrataReversal:
		return false
	default:
		return true
	}
}

// gasDeficitCrossesHaltThreshold checks the total per-asset deficit against current max gas.
// It is only called for gas-related deficit reasons.
func gasDeficitCrossesHaltThreshold(ctx cosmos.Context, mgr Manager, k keeper.Keeper, deficit common.Coin) bool {
	multiplier := k.GetConfigInt64(ctx, constants.VaultDeficitGasHaltMaxGasMultiplier)
	if multiplier <= 0 {
		return false
	}
	chain := deficit.Asset.GetChain()
	maxGasCoin, err := mgr.GasMgr().GetMaxGas(ctx, chain)
	if err != nil {
		ctx.Logger().Error("fail to get max gas for vault deficit halt threshold", "chain", chain, "error", err)
		return true
	}
	if maxGasCoin.Amount.IsZero() {
		ctx.Logger().Error("max gas is zero for vault deficit halt threshold", "chain", chain)
		return false
	}
	threshold := maxGasCoin.Amount.MulUint64(uint64(multiplier))
	return deficit.Amount.GTE(threshold)
}

// setHaltMimirIfNotPreserved writes ctx.BlockHeight() to a halt mimir unless
// the operator has set the indefinite-manual sentinel (1), in which case the
// halt is preserved. A halt scheduled for a future block is overwritten:
// the vault deficit signals a present-time divergence that warrants halting
// now rather than waiting for the scheduled height.
//
// Differs from processSolvencyAttestation's guard, which preserves future
// halts because it can also unhalt — overwriting there would let a future
// halt be unhalted by an earlier solvent attestation. This function only
// sets, never unhalts, so future halts are safe to overwrite.
func setHaltMimirIfNotPreserved(ctx cosmos.Context, k keeper.Keeper, mgr Manager, key string) {
	haltVal, err := k.GetMimir(ctx, key)
	if err != nil {
		ctx.Logger().Error("fail to get mimir", "key", key, "error", err)
	}
	// Preserve manual indefinite halt; same-block dedupe avoids duplicate events.
	if haltVal == 1 || haltVal == ctx.BlockHeight() {
		return
	}
	k.SetMimir(ctx, key, ctx.BlockHeight())
	ev := NewEventSetMimir(strings.ToUpper(key), strconv.FormatInt(ctx.BlockHeight(), 10))
	if err := mgr.EventMgr().EmitEvent(ctx, ev); err != nil {
		ctx.Logger().Error("fail to emit set_mimir event for vault deficit", "key", key, "error", err)
	}
}

// matchedOutbound holds a matching TxOutItem with its height and hash for sorting
type matchedOutbound struct {
	height int64
	item   TxOutItem
	hash   string
}

// findOriginalMemoForOutbound searches the TxOut queue for a matching scheduled outbound
// and returns its OriginalMemo. This is needed for memoless outbounds where the on-chain
// memo is empty but we need the original memo to properly link the outbound to its inbound.
//
// When multiple TxOutItems match (e.g., two outbounds with identical on-chain fields but
// different memos), this function uses deterministic ordering matching Bifrost's signer
// (see bifrost/signer/storage.go:200-204) to ensure THORNode picks the same one Bifrost signed.
func findOriginalMemoForOutbound(ctx cosmos.Context, mgr Manager, tx common.ObservedTx) string {
	// Check coins early to avoid unnecessary iteration
	if len(tx.Tx.Coins) == 0 {
		return ""
	}

	k := mgr.Keeper()
	signingTransPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	searchPeriod := signingTransPeriod
	switch {
	case tx.Tx.Chain.Equals(common.XMRChain):
		searchPeriod = signingTransPeriod * xmrSigningPeriodMultiplier
	case isObservedEVMAggregatorOutbound(tx):
		searchPeriod = signingTransPeriod * evmAggregatorSigningPeriodMultiplier
	case isObservedEVMTokenOutbound(tx):
		searchPeriod = signingTransPeriod * evmTokenSigningPeriodMultiplier
	}

	// Search recent TxOut blocks for matching TxOutItem
	// Use the same range logic as handler_common_outbound.go
	earliestHeight := ctx.BlockHeight() - searchPeriod
	if earliestHeight < 1 {
		earliestHeight = 1
	}

	// A TxOutItem might be rescheduled (by LackSigning) rounded up to nearest multiple of RescheduleCoalesceBlocks,
	// so check backwards from that future nearest multiple.
	latestHeight := ctx.BlockHeight()
	rescheduleCoalesceBlocks := k.GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks)
	if rescheduleCoalesceBlocks > 1 {
		overBlocks := latestHeight % rescheduleCoalesceBlocks
		if overBlocks != 0 {
			latestHeight += rescheduleCoalesceBlocks - overBlocks
		}
	}

	// Collect all matching TxOutItems
	var matches []matchedOutbound

	for height := earliestHeight; height <= latestHeight; height++ {
		txOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			continue
		}
		for _, item := range txOut.TxArray {
			// CommonOutboundTxHandler records the current observation's OutHash
			// before memo recovery runs, so that exact item remains eligible here.
			// Exclude every other completed item; duplicate delivery of the current
			// transaction is stopped earlier by the ObservedTxVoter done guard.
			if !item.OutHash.IsEmpty() && !item.OutHash.Equals(tx.Tx.ID) {
				continue
			}
			if !item.Chain.Equals(tx.Tx.Chain) {
				continue
			}
			if !common.EquivalentAddresses(item.Chain, item.ToAddress, tx.Tx.ToAddress) {
				continue
			}
			if !txOutPubKeyMatchesObserved(tx.Tx.Chain, tx.ObservedPubKey, item) {
				continue
			}
			if !item.Coin.Asset.Equals(tx.Tx.Coins[0].Asset) {
				continue
			}
			// Check coin amount matches (with some flexibility for gas)
			// Similar to handler_common_outbound.go logic
			matchCoin := tx.Tx.Coins.EqualsEx(common.Coins{item.Coin})
			if !matchCoin && item.Coin.Asset.Equals(item.Chain.GetGasAsset()) {
				asset := item.Chain.GetGasAsset()
				intendToSpend := item.Coin.Amount.Add(item.MaxGas.ToCoins().GetCoin(asset).Amount)
				actualSpend := tx.Tx.Coins.GetCoin(asset).Amount.Add(tx.Tx.Gas.ToCoins().GetCoin(asset).Amount)
				if intendToSpend.Equal(actualSpend) {
					matchCoin = true
				}
			}
			if !matchCoin {
				continue
			}
			// Found a match - add to list for deterministic sorting
			matches = append(matches, matchedOutbound{
				height: height,
				item:   item,
				hash:   item.Hash(),
			})
		}
	}

	if len(matches) == 0 {
		return ""
	}

	// Sort by height (ascending), then by hash (ascending)
	// This matches Bifrost's deterministic ordering in signer/storage.go:200-204
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].hash < matches[j].hash
	})
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].height < matches[j].height
	})

	// Return memo from first match (what Bifrost would sign first)
	if matches[0].item.OriginalMemo != "" {
		return matches[0].item.OriginalMemo
	}
	return matches[0].item.Memo
}

// resolveReferenceMemo resolves and parses a memoless reference memo before
// processing. A successful resolution updates both the stored voter and the
// in-flight observed tx so processing, refund, and completion paths share the
// same memo. A failed resolution empties only the voter memo to prevent
// execution; the in-flight copy keeps the raw r:<ref> for refund events. When
// memoless transactions are halted, an otherwise-valid reference is resolved
// only into the in-flight copy so it can provide a safe refund address without
// becoming executable.
func resolveReferenceMemo(ctx cosmos.Context, mgr Manager, k keeper.Keeper, tx *common.ObservedTx, voter *ObservedTxVoter) (mem.Memo, bool) {
	if len(tx.Tx.Coins) == 0 {
		return mem.EmptyMemo, false
	}

	preMemo := voter.Tx.Tx.Memo
	referenceMemo, err := ParseMemoWithTHORNames(ctx, k, preMemo)
	if err != nil || !referenceMemo.IsType(TxReferenceReadMemo) {
		return mem.EmptyMemo, false
	}

	asset := tx.Tx.Coins[0].Asset
	lookup := lookupMemoFromReference(ctx, mgr, asset, voter.Tx.Tx, voter.UnfinalizedHeight)
	resolvedMemo := lookup.memo
	// The voter memo is the source used by processOneTxIn. Store the lookup
	// result even when it is empty so a missing, expired, or exhausted
	// reference cannot execute using the raw r:<ref> memo.
	voter.Tx.Tx.Memo = resolvedMemo
	ctx.Logger().Info("reference memo conversion", "pre", preMemo, "post", resolvedMemo, "asset", asset)

	if resolvedMemo == "" {
		// Do not also blank tx.Tx.Memo. This in-flight copy is used by refund
		// events, where preserving the observed r:<ref> is more informative than
		// replacing it with an empty memo and does not make the reference executable.
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}

	parsedMemo, err := ParseMemoWithTHORNames(ctx, k, resolvedMemo)
	if err != nil {
		voter.Tx.Tx.Memo = ""
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}
	if parsedMemo.IsOutbound() || parsedMemo.IsInternal() {
		ctx.Logger().Error("resolved reference memo expands to a forbidden type; blanking",
			"type", parsedMemo.GetType().String(), "tx", tx.Tx.ID.String())
		voter.Tx.Tx.Memo = ""
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}

	tx.Tx.Memo = resolvedMemo
	if lookup.halted {
		// HaltMemoless must stop execution, but sender-opaque chains still need
		// the validated registered memo on the in-flight copy so refundTx can
		// recover its explicit refund address. Keep the stored voter empty and
		// do not report a successful resolution, which also prevents usage from
		// being tracked by the caller.
		voter.Tx.Tx.Memo = ""
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}

	k.SetObservedTxInVoter(ctx, *voter)
	// The caller parsed its local memo before reference resolution. Return the
	// resolved parsed value so post-handler HasOutbound/SetDone decisions use
	// the registered transaction type rather than TxReferenceReadMemo.
	return parsedMemo, true
}

type referenceMemoLookupResult struct {
	memo   string
	halted bool
}

// fetchMemoFromReference fetches a memo without consuming the reference. Usage
// is recorded only after the resolved transaction has been accepted for execution.
func fetchMemoFromReference(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.Tx, txObservationHeight int64) string {
	lookup := lookupMemoFromReference(ctx, mgr, asset, tx, txObservationHeight)
	if lookup.halted {
		return ""
	}
	return lookup.memo
}

// lookupMemoFromReference validates a reference independently from the
// operational halt. Callers that process transactions must still reject a
// halted lookup, while refund handling can use an otherwise-valid memo solely
// to recover its explicit refund address.
func lookupMemoFromReference(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.Tx, txObservationHeight int64) referenceMemoLookupResult {
	mem, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), tx.Memo)
	if err != nil {
		ctx.Logger().Debug("failed to parse memo in fetchMemoFromReference", "error", err, "memo", tx.Memo, "txid", tx.ID)
	}
	if mem.GetType() == TxReferenceReadMemo {
		m, ok := mem.(ReferenceReadMemo)
		if !ok {
			return referenceMemoLookupResult{memo: tx.Memo}
		}

		result := referenceMemoLookupResult{}
		haltMemoless, err := mgr.Keeper().GetMimir(ctx, constants.HaltMemoless.String())
		if err == nil && haltMemoless > 0 {
			result.halted = true
		}

		// Check if the reference memo exists before proceeding
		if !mgr.Keeper().ReferenceMemoExists(ctx, asset, m.Reference) {
			ctx.Logger().Debug("reference memo not found", "reference", m.Reference, "asset", asset)
			return result
		}

		refMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, m.Reference)
		if err != nil {
			ctx.Logger().Error("unable to fetch pool for ref memo lookup", "error", err, "asset", asset)
			return result
		}

		// Ensure transaction was observed after the reference memo was created
		if txObservationHeight <= refMemo.Height {
			ctx.Logger().Info("transaction observed before reference memo creation", "tx_height", txObservationHeight, "memo_height", refMemo.Height, "reference", m.Reference)
			// Do not track this attempt against the current record. Reference numbers
			// are recycled, so the record may belong to a later registrant whose
			// usage allowance must not be consumed by an older observation.
			return result
		}

		ttl := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
		if !refMemo.IsExpired(ctx.BlockHeight(), ttl) {
			// Check usage limit
			maxUse := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnMaxUse)
			if maxUse > 0 && refMemo.GetUsageCount() >= maxUse {
				ctx.Logger().Info("reference memo usage limit exceeded in fetchMemoFromReference", "reference", m.Reference, "usage_count", refMemo.GetUsageCount(), "max_use", maxUse)
				return result
			}

			result.memo = refMemo.Memo
			return result
		}

		return result
	}
	return referenceMemoLookupResult{memo: tx.Memo}
}

// trackReferenceMemoUsage tracks that a transaction has used a reference memo
func trackReferenceMemoUsage(ctx cosmos.Context, mgr Manager, asset common.Asset, memo string, txID common.TxID) {
	mem, _ := ParseMemo(mgr.GetVersion(), memo) // ignore err
	if mem.GetType() == TxReferenceReadMemo {
		m, ok := mem.(ReferenceReadMemo)
		if !ok {
			return
		}

		// Only track usage for reference memos that actually exist in state
		if !mgr.Keeper().ReferenceMemoExists(ctx, asset, m.Reference) {
			ctx.Logger().Debug("reference memo not found for usage tracking, skipping", "reference", m.Reference, "asset", asset)
			return
		}

		refMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, m.Reference)
		if err != nil {
			ctx.Logger().Error("fail to get reference memo for usage tracking", "error", err)
			return
		}

		// Check if already used by this transaction
		if refMemo.HasBeenUsedBy(txID) {
			// Already tracked, nothing to do
			return
		}

		// Add this transaction to the usage list (with duplicate protection)
		if refMemo.AddUsage(txID) {
			mgr.Keeper().SetReferenceMemo(ctx, refMemo)
			maxUse := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnMaxUse)
			ctx.Logger().Info("reference memo usage tracked", "reference", m.Reference, "usage_count", refMemo.GetUsageCount(), "max_use", maxUse, "txid", txID, "asset", asset, "memo", refMemo.Memo)
		}
	}
}

func generateReferenceMemoID(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.ObservedTx) (string, error) {
	if len(tx.Tx.Coins) == 0 {
		return "", fmt.Errorf("no coins in transaction for reference generation")
	}

	coinAmount := tx.Tx.Coins[0].Amount
	if coinAmount.GT(cosmos.NewUint(math.MaxUint64)) {
		return "", fmt.Errorf("coin amount exceeds uint64 max")
	}
	amount := coinAmount.Uint64()
	return ExtractReferenceFromAmount(ctx, mgr, asset, amount)
}

// ExtractReferenceFromAmount extracts the reference ID from a transaction amount.
// This is the core logic shared by both the handler (generateReferenceMemoID)
// and the querier (queryReferenceMemoPreflight).
func ExtractReferenceFromAmount(ctx cosmos.Context, mgr Manager, asset common.Asset, amount uint64) (string, error) {
	if asset.IsEmpty() {
		return "", fmt.Errorf("asset is empty for reference generation")
	}

	if amount == 0 {
		return "", fmt.Errorf("zero amount in transaction for reference generation")
	}

	var decimals int64
	if asset.IsGasAsset() {
		decimals = asset.Chain.GetGasAssetDecimal()
	} else {
		pool, err := mgr.Keeper().GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("unable to fetch pool for ref memo", "error", err)
			return "", err
		}
		decimals = pool.Decimals
		if decimals == 0 {
			// For non-gas assets, a pool decimal value of zero is used as a sentinel
			// for THORChain default precision (1e8), not literal 0-decimal precision.
			decimals = int64(common.THORChainDecimals)
		}
	}

	baseEnd := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnRefCount)
	txnRefLength := len(fmt.Sprintf("%d", baseEnd))

	// Prevent overflow: uint64 max is ~1.8e19, so max safe length is 19 digits
	const maxRefLength = 19
	if txnRefLength > maxRefLength {
		return "", fmt.Errorf("reference length %d exceeds maximum %d to prevent overflow", txnRefLength, maxRefLength)
	}

	// Calculate modulus based on reference length (10^txnRefLength)
	var modulus uint64 = 1
	if baseEnd > 0 {
		for i := 0; i < txnRefLength; i++ {
			modulus *= 10
		}
	}

	// Normalize amount to THORChain decimals.
	// Note: Only decimals < THORChainDecimals (8) need handling here. For chains with
	// decimals >= THORChainDecimals (e.g. SOL at 9), Bifrost already normalizes all
	// inbound amounts to 1e8 precision before they reach THORNode, so no scaling is
	// needed in those cases.
	if decimals < int64(common.THORChainDecimals) {
		divisor := int64(1)
		for i := decimals; i < int64(common.THORChainDecimals); i++ {
			divisor *= 10
		}
		amount /= uint64(divisor)
	}

	// Extract reference from the amount.
	refNum := amount % modulus

	// Zero references are not allowed as they would create ambiguous reference IDs.
	// This occurs when the amount (after decimal normalization) is exactly divisible
	// by the modulus (e.g., 50000000 sat with modulus 100 → ref 0).
	// Users should adjust their amount slightly to avoid this edge case.
	if refNum == 0 {
		return "", fmt.Errorf("zero reference from amount is invalid: amount %d is divisible by modulus %d", amount, modulus)
	}

	return leadingZeros(txnRefLength, fmt.Sprintf("%d", refNum)), nil
}
