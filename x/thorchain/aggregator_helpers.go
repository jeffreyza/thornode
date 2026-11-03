package thorchain

import (
	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
)

func hasAggregatorTargetLimit(limit *sdkmath.Uint) bool {
	return limit != nil && !limit.IsZero()
}

func txOutHasAggregator(toi TxOutItem) bool {
	return toi.Aggregator != "" ||
		toi.AggregatorTargetAsset != "" ||
		hasAggregatorTargetLimit(toi.AggregatorTargetLimit)
}

func observedTxHasAggregator(tx common.ObservedTx) bool {
	return tx.Aggregator != "" ||
		tx.AggregatorTarget != "" ||
		hasAggregatorTargetLimit(tx.AggregatorTargetLimit)
}
