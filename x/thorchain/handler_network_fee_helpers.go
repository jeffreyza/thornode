package thorchain

import (
	"context"
	"strconv"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func processNetworkFeeAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter *keeper.ObservedNetworkFeeVoter,
	attester cosmos.AccAddress,
	active NodeAccounts,
	nf *common.NetworkFee,
	shouldSlashForDuplicate bool,
) error {
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := observationDelayFlexibility(ctx, mgr.Keeper(), voter.Chain)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_network_fee"),
		telemetry.NewLabel("chain", telemChain(nf.Chain)),
	}))

	if !voter.Sign(attester) {
		// Slash for the network having to handle the extra message/s.
		if shouldSlashForDuplicate {
			mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, attester)
		}
		ctx.Logger().Info("signer already signed network fee", "signer", attester.String(), "block height", nf.Height, "chain", nf.Chain.String())
		return nil
	}

	// doesn't have consensus yet
	if !voter.HasConsensus(active) {
		// Before consensus, slash until consensus.
		mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, attester)
		return nil
	}

	if voter.BlockHeight > 0 {
		// After consensus, only decrement slash points if within the ObservationDelayFlexibility period.
		if (voter.BlockHeight + observeFlex) >= ctx.BlockHeight() {
			mgr.Slasher().DecSlashPoints(slashCtx, lackOfObservationPenalty, attester)
		}
		// MsgNetworkFeeQuorum tx already processed
		return nil
	}

	voter.BlockHeight = ctx.BlockHeight()

	// Record the height at which this chain's network fee last reached consensus.
	// A frozen fee value is otherwise indistinguishable from a stable one.
	telemetry.SetGaugeWithLabels(
		[]string{"thornode", "network_fee", "last_consensus_height"},
		float32(ctx.BlockHeight()),
		[]metrics.Label{telemetry.NewLabel("chain", telemChain(nf.Chain))},
	)

	// This signer brings the voter to consensus; increment the signer's slash points like the before-consensus signers,
	// then decrement all the signers' slash points and increment the non-signers' slash points.
	mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, attester)
	signers := voter.GetSigners()
	nonSigners := getNonSigners(active, signers)
	mgr.Slasher().DecSlashPoints(slashCtx, observeSlashPoints, signers...)
	mgr.Slasher().IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
	queueObservationTally(ctx, mgr.Keeper(), observationTallyKindNetworkFee, voter.Chain,
		strconv.FormatInt(voter.ReportBlockHeight, 10), strconv.FormatInt(voter.FeeRate, 10), strconv.FormatInt(voter.TransactionSize, 10))

	ctx.Logger().Info("update network fee", "chain", nf.Chain.String(), "transaction-size", nf.TransactionSize, "fee-rate", nf.TransactionRate)
	if err := mgr.Keeper().SaveNetworkFee(ctx, nf.Chain, NetworkFee{
		Chain:              nf.Chain,
		TransactionSize:    nf.TransactionSize,
		TransactionFeeRate: nf.TransactionRate,
	}); err != nil {
		return ErrInternal(err, "fail to save network fee")
	}

	if nf.Height > 0 {
		if err := mgr.Keeper().SetLastChainHeight(ctx, nf.Chain, nf.Height); err != nil {
			ctx.Logger().Error("fail to set last chain height from network fee", "chain", nf.Chain, "height", nf.Height, "error", err)
		}
	}

	return nil
}
