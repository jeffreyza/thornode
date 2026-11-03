package core

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// Poll committed fees, not signer health or a locally estimated fee. The caller's
// actor deadline bounds retries, including the initial network-fee observation.
func waitForXMRNetworkFee(log *zerolog.Logger, config *OpConfig) OpResult {
	if config == nil || len(config.NodeUsers) == 0 || config.NodeUsers[0] == nil || config.NodeUsers[0].Thorchain == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("XMR fee readiness requires a THORNode client")}
	}
	size, rate, err := config.NodeUsers[0].Thorchain.GetNetworkFee(common.XMRChain)
	if err != nil || size == 0 || rate == 0 {
		log.Info().Err(err).Uint64("transaction_size", size).Uint64("fee_rate", rate).
			Msg("waiting for committed XMR network fee before submitting swap")
		return OpResult{}
	}
	return OpResult{Continue: true}
}

// Wait for this swap's outbound before accepting spent references. In particular,
// a completed swap with no scheduled outbound cannot later create spent refs.
func waitForXMRSwapOutbound(
	log *zerolog.Logger, txID string,
	getStages func(string) (openapi.TxStagesResponse, error),
	getDetails func(string) (openapi.TxDetailsResponse, error),
) (openapi.TxDetailsResponse, OpResult) {
	stages, err := getStages(txID)
	if err != nil {
		log.Info().Err(err).Str("txid", txID).Msg("waiting for XMR swap stages")
		return openapi.TxDetailsResponse{}, OpResult{}
	}
	// Query details after stages: an older details response must not be combined
	// with a newer completed stage to mistake pending scheduling for failure.
	details, err := getDetails(txID)
	if err != nil {
		log.Info().Err(err).Str("txid", txID).Msg("waiting for XMR swap details")
		return openapi.TxDetailsResponse{}, OpResult{}
	}
	if err := xmrSwapFailure(txID, stages, details); err != nil {
		return details, OpResult{Finish: true, Error: err}
	}
	return details, OpResult{Continue: stages.OutboundSigned != nil && stages.OutboundSigned.Completed}
}

func xmrSwapFailure(txID string, stages openapi.TxStagesResponse, details openapi.TxDetailsResponse) error {
	if details.GetReverted() {
		return fmt.Errorf("XMR swap %s was reverted", txID)
	}
	for _, action := range details.Actions {
		memo := action.GetMemo()
		if memo == "" {
			memo = action.GetOriginalMemo()
		}
		if strings.HasPrefix(strings.ToUpper(memo), "REFUND:") {
			return fmt.Errorf("XMR swap %s was refunded instead of producing the requested outbound: %s", txID, memo)
		}
	}
	if stages.SwapFinalised != nil && stages.SwapFinalised.Completed &&
		stages.SwapStatus != nil && !stages.SwapStatus.Pending &&
		details.GetFinalisedHeight() > 0 && len(details.Actions) == 0 && len(details.OutTxs) == 0 {
		return fmt.Errorf("XMR swap %s finalized without a scheduled outbound (inbound height %d); check swap/refund events for the scheduling failure", txID, details.GetFinalisedHeight())
	}
	return nil
}
