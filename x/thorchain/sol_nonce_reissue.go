package thorchain

import (
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// reissueSupersededSolDurable re-issues, at the current height and on the same vault,
// every pending SOL outbound of vault whose keysign height h satisfies
// prevHeight <= h < ctx.BlockHeight(), right after the vault's committed durable nonce
// advanced from a value committed at prevHeight.
//
// Why: bifrost signs a durable outbound with the nonce committed at its keysign height
// and serializes one durable tx per vault. A sibling waiting behind the tx that just
// consumed that nonce can never sign at its own height (the value there is consumed),
// so without this it waits a whole SigningTransactionPeriod for LackSigning.
//
// Why it cannot pay twice: every copy of such an item was signed with the nonce
// committed at some height in [prevHeight, now), i.e. the value just consumed by the
// committing tx (or an older, already consumed one), so no copy can land any more.
// Callers pass prevHeight only when an ACTIVE (not break-glass-voided) record existed,
// so items were signed durably, except outbounds to the vault's own address or nonce
// account, which bifrost always signs with a recent blockhash; those are skipped.
// consumed, when set, is the observed tx that consumed the nonce: items it fulfils are
// left for normal outbound matching. Items with a landed copy (solDurableCopyLanded)
// and items past MaxOutboundAttempts are also left to LackSigning.
func reissueSupersededSolDurable(ctx cosmos.Context, mgr Manager, vault Vault, prevHeight int64, consumed *common.ObservedTx) {
	reissueSolDurableAt(ctx, mgr, vault, prevHeight, consumed, ctx.BlockHeight(), true, solSiblingCache{})
}

// reissueSolDurableAt is reissueSupersededSolDurable re-adding the items at height at
// (the current height, or LackSigning's reschedule height so they share one queue
// snapshot with the item it re-adds). respectBudget skips items past
// MaxOutboundAttempts: right after a nonce advance LackSigning's recovery handles
// them, but LackSigning's same-vault (keepVault) path has no such recovery, so its
// co-location call must move them too or two exhausted siblings deadlock.
// siblings is the landed-copy scan cache, shared with the caller's pass when it has one.
func reissueSolDurableAt(ctx cosmos.Context, mgr Manager, vault Vault, prevHeight int64, consumed *common.ObservedTx, at int64, respectBudget bool, siblings solSiblingCache) {
	k := mgr.Keeper()
	now := ctx.BlockHeight()
	signingTransPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	maxOutboundAttempts := k.GetConfigInt64(ctx, constants.MaxOutboundAttempts)
	// Bifrost signs these destinations on the recent-blockhash path even for a
	// durable vault (durableDestinationExcluded), so a consumed nonce does not kill
	// their copies: leave them to LackSigning, after their blockhash has expired.
	vaultAddr, addrErr := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	nonceAddr := ""
	if addrErr == nil {
		nonceAddr, _ = common.SolNonceAccountAddress(vaultAddr.String())
	}
	slasher := &SlasherImpl{keeper: k}
	// Telemetry: the nonce advanced (re-issue) or LackSigning co-located siblings
	// on a vault whose nonce has not moved.
	reason := "durable_nonce_advanced"
	if !respectBudget {
		reason = "durable_nonce_colocate"
	}
	start := prevHeight
	// Older heights have already been through LackSigning.
	if signingTransPeriod > 0 && start < now-signingTransPeriod {
		start = now - signingTransPeriod
	}
	if start < 1 {
		start = 1
	}
	for h := start; h < now; h++ {
		txOut, err := k.GetTxOut(ctx, h)
		if err != nil {
			ctx.Logger().Error("fail to get txout for durable nonce reissue", "height", h, "error", err)
			continue
		}
		changed := false
		for i, item := range txOut.TxArray {
			if !item.Chain.Equals(common.SOLChain) || !item.OutHash.IsEmpty() || !item.VaultPubKey.Equals(vault.PubKey) {
				continue
			}
			if consumed != nil && txOutPubKeyMatchesObserved(item.Chain, consumed.ObservedPubKey, item) &&
				outboundObservationFulfils(ctx, mgr, *consumed, item) {
				continue
			}
			if addrErr != nil || item.ToAddress.Equals(vaultAddr) || item.ToAddress.String() == nonceAddr {
				continue
			}
			if memo, _ := ParseMemoWithTHORNames(ctx, k, item.GetMemo()); memo.IsInternal() {
				continue // migrations are rescheduled by their own mechanism, as in LackSigning
			}
			inVoter, vErr := k.GetObservedTxInVoter(ctx, item.InHash)
			if vErr != nil {
				continue // leave it to LackSigning
			}
			// Past the attempt budget, stop refreshing the height so LackSigning's
			// recovery (attempt limit, treasury) still reaches the item.
			if respectBudget && maxOutboundAttempts > 0 && signingTransPeriod > 0 &&
				(now-inVoter.FinalisedHeight)/signingTransPeriod >= maxOutboundAttempts {
				continue
			}
			// A re-attestation can sit on top of a successful, finalised vault payment,
			// and an unmatched copy may have landed: never re-issue an item whose copy
			// landed (the same guard LackSigning applies).
			if slasher.solDurableCopyLanded(ctx, mgr, item, inVoter.FinalisedHeight, siblings) {
				continue
			}
			if err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, item, at); err != nil {
				ctx.Logger().Error("fail to reissue superseded durable outbound", "hash", item.InHash, "error", err)
				continue
			}
			// As in LackSigning: the replaced item is marked sent so it is not signed again.
			txOut.TxArray[i].OutHash = common.BlankTxID
			changed = true
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "outbound", "rescheduled"},
				float32(1),
				[]metrics.Label{
					telemetry.NewLabel("chain", item.Chain.String()),
					telemetry.NewLabel("asset", item.Coin.Asset.String()),
					telemetry.NewLabel("reason", reason),
				},
			)
		}
		if changed {
			if err = k.SetTxOut(ctx, txOut); err != nil {
				ctx.Logger().Error("fail to save txout after durable nonce reissue", "height", h, "error", err)
			}
		}
	}
}
