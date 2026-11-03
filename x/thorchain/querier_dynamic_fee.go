package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (qs queryServer) queryDynamicL1Fees(ctx cosmos.Context, req *types.QueryDynamicL1FeesRequest) (*types.QueryDynamicL1FeesResponse, error) {
	resp := &types.QueryDynamicL1FeesResponse{}

	iter := qs.mgr.Keeper().GetDynamicFeeRecordIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var rec types.DynamicFeeRecord
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}

		state := dynamicFeeWhitelistState(ctx, qs.mgr.Keeper(), rec.Thorname)
		entry := types.DynamicL1FeeEntry{
			Thorname:        rec.Thorname,
			Pair:            fmt.Sprintf("%s|%s", rec.PairAssetA, rec.PairAssetB),
			DynamicBps:      rec.DynamicBps,
			WhitelistState:  state,
			LastActiveEpoch: rec.LastActiveEpoch,
		}
		if latest, ok := rec.LatestHistory(); ok {
			entry.LatestFeesTor = latest.FeesTor.String()
		}
		resp.Entries = append(resp.Entries, entry)
	}

	return resp, nil
}

func (qs queryServer) queryDynamicL1FeesByThorname(ctx cosmos.Context, req *types.QueryDynamicL1FeesByThornameRequest) (*types.QueryDynamicL1FeesByThornameResponse, error) {
	thorname := req.Thorname
	state := dynamicFeeWhitelistState(ctx, qs.mgr.Keeper(), thorname)

	resp := &types.QueryDynamicL1FeesByThornameResponse{
		Thorname:       thorname,
		WhitelistState: state,
	}

	iter := qs.mgr.Keeper().GetDynamicFeeRecordIteratorForThorname(ctx, thorname)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var rec types.DynamicFeeRecord
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}

		detail := types.DynamicL1FeePairDetail{
			Pair:            fmt.Sprintf("%s|%s", rec.PairAssetA, rec.PairAssetB),
			DynamicBps:      rec.DynamicBps,
			LastActiveEpoch: rec.LastActiveEpoch,
		}
		for _, h := range rec.History {
			detail.History = append(detail.History, types.DynamicL1FeeHistoryEntry{
				Epoch:      h.Epoch,
				VolumeTor:  h.VolumeTor.String(),
				FeesTor:    h.FeesTor.String(),
				BpsAtClose: h.BpsAtClose,
			})
		}
		resp.Pairs = append(resp.Pairs, detail)
	}

	return resp, nil
}

// queryDynamicL1FeesCurrent returns the live (in-progress, not yet sealed)
// per-(thorname, pair) accumulators for the current epoch. The current epoch
// is computed at query time from block height and the configured epoch length.
func (qs queryServer) queryDynamicL1FeesCurrent(ctx cosmos.Context, req *types.QueryDynamicL1FeesCurrentRequest) (*types.QueryDynamicL1FeesCurrentResponse, error) {
	resp := &types.QueryDynamicL1FeesCurrentResponse{}

	epochBlocks := qs.mgr.Keeper().GetConfigInt64(ctx, constants.L1DynamicFeeEpochBlocks)
	if epochBlocks > 0 {
		resp.Epoch = uint64(ctx.BlockHeight() / epochBlocks)
	}

	iter := qs.mgr.Keeper().GetDynamicFeeAccumulatorIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		resp.Entries = append(resp.Entries, types.DynamicL1FeeCurrentEntry{
			Thorname:  acc.Thorname,
			Pair:      fmt.Sprintf("%s|%s", acc.PairAssetA, acc.PairAssetB),
			VolumeTor: acc.VolumeTor.String(),
			FeesTor:   acc.FeesTor.String(),
			Epoch:     resp.Epoch,
		})
	}

	return resp, nil
}
