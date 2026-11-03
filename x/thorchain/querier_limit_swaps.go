package thorchain

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (qs queryServer) queryLimitSwaps(ctx cosmos.Context, req *types.QueryLimitSwapsRequest) (*types.QueryLimitSwapsResponse, error) {
	// Set defaults for pagination
	offset := req.Offset
	limit := req.Limit
	if limit == 0 {
		limit = config.GetThornode().API.Pagination.DefaultPageSize
	}
	if limit > config.GetThornode().API.Pagination.MaxPageSize {
		limit = config.GetThornode().API.Pagination.MaxPageSize
	}

	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "ratio"
	}
	sortOrder := req.SortOrder
	if sortOrder == "" {
		sortOrder = "asc"
	}

	var allSwaps []*types.LimitSwapWithDetails
	currentBlockHeight := ctx.BlockHeight()
	maxAge := qs.mgr.Keeper().GetConfigInt64(ctx, constants.StreamingLimitSwapMaxAge)

	// Only check advanced swap queue if enabled (limit swaps are only in advanced queue)
	if !qs.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		return &types.QueryLimitSwapsResponse{
			LimitSwaps: allSwaps,
			Pagination: &types.PaginationMeta{
				Offset:  offset,
				Limit:   limit,
				Total:   0,
				HasNext: false,
				HasPrev: offset > 0,
			},
		}, nil
	}

	// Get advanced swap queue iterator
	iterator := qs.mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var msg MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &msg); err != nil {
			continue
		}

		// Filter only limit swaps
		if !msg.IsLimitSwap() {
			continue
		}

		// Apply filters
		if req.SourceAsset != "" && !strings.EqualFold(msg.Tx.Coins[0].Asset.String(), req.SourceAsset) {
			continue
		}
		if req.TargetAsset != "" && !strings.EqualFold(msg.TargetAsset.String(), req.TargetAsset) {
			continue
		}
		if req.Sender != "" && !strings.EqualFold(msg.Tx.FromAddress.String(), req.Sender) {
			continue
		}

		// Calculate details
		blocksSinceCreated := currentBlockHeight - msg.InitialBlockHeight
		timeToExpiryBlocks := getLimitSwapTTL(msg, maxAge) - blocksSinceCreated
		if timeToExpiryBlocks < 0 {
			timeToExpiryBlocks = 0
		}

		// Calculate ratio - this is the key under which the limit swap is stored
		ratioStr := ""
		if !msg.TradeTarget.IsZero() {
			tradeTarget := msg.TradeTarget
			inputAmount := msg.Tx.Coins[0].Amount
			if !inputAmount.IsZero() {
				ratio := common.GetUncappedShare(tradeTarget, inputAmount, cosmos.NewUint(common.One))
				ratioStr = ratio.String()
			}
		}

		limitSwap := &types.LimitSwapWithDetails{
			Swap:               &msg,
			Ratio:              ratioStr,
			BlocksSinceCreated: blocksSinceCreated,
			TimeToExpiryBlocks: timeToExpiryBlocks,
			CreatedTimestamp:   0, // We don't have timestamp info readily available
		}

		allSwaps = append(allSwaps, limitSwap)
	}

	// Sort results
	switch sortBy {
	case "ratio":
		if sortOrder == "desc" {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				ratioI := cosmos.ZeroUint()
				ratioJ := cosmos.ZeroUint()
				if allSwaps[i].Ratio != "" {
					ratioI = cosmos.NewUintFromString(allSwaps[i].Ratio)
				}
				if allSwaps[j].Ratio != "" {
					ratioJ = cosmos.NewUintFromString(allSwaps[j].Ratio)
				}
				return ratioI.GT(ratioJ)
			})
		} else {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				ratioI := cosmos.ZeroUint()
				ratioJ := cosmos.ZeroUint()
				if allSwaps[i].Ratio != "" {
					ratioI = cosmos.NewUintFromString(allSwaps[i].Ratio)
				}
				if allSwaps[j].Ratio != "" {
					ratioJ = cosmos.NewUintFromString(allSwaps[j].Ratio)
				}
				return ratioI.LT(ratioJ)
			})
		}
	case "age":
		if sortOrder == "desc" {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				return allSwaps[i].BlocksSinceCreated > allSwaps[j].BlocksSinceCreated
			})
		} else {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				return allSwaps[i].BlocksSinceCreated < allSwaps[j].BlocksSinceCreated
			})
		}
	case "created_height":
		if sortOrder == "desc" {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				return allSwaps[i].Swap.InitialBlockHeight > allSwaps[j].Swap.InitialBlockHeight
			})
		} else {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				return allSwaps[i].Swap.InitialBlockHeight < allSwaps[j].Swap.InitialBlockHeight
			})
		}
	case "amount":
		if sortOrder == "desc" {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				return allSwaps[i].Swap.Tx.Coins[0].Amount.GT(allSwaps[j].Swap.Tx.Coins[0].Amount)
			})
		} else {
			sort.SliceStable(allSwaps, func(i, j int) bool {
				return allSwaps[i].Swap.Tx.Coins[0].Amount.LT(allSwaps[j].Swap.Tx.Coins[0].Amount)
			})
		}
	}

	// Apply pagination
	total := uint64(len(allSwaps))
	var paginatedSwaps []*types.LimitSwapWithDetails

	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedSwaps = allSwaps[offset:end]
	}

	return &types.QueryLimitSwapsResponse{
		LimitSwaps: paginatedSwaps,
		Pagination: &types.PaginationMeta{
			Offset:  offset,
			Limit:   limit,
			Total:   total,
			HasNext: offset+limit < total,
			HasPrev: offset > 0,
		},
	}, nil
}

func (qs queryServer) queryLimitSwapsSummary(ctx cosmos.Context, req *types.QueryLimitSwapsSummaryRequest) (*types.QueryLimitSwapsSummaryResponse, error) {
	var totalLimitSwaps uint64
	var totalValueUSD cosmos.Uint = cosmos.ZeroUint()
	assetPairMap := make(map[string]*types.AssetPairSummary)
	var oldestSwapBlocks int64
	var totalAgeBlocks int64
	var swapCount int64

	currentBlockHeight := ctx.BlockHeight()

	// Get USD pricing using TOR pool
	dollarsPerRune := dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper())

	// Only check advanced swap queue if enabled (limit swaps are only in advanced queue)
	if !qs.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		return &types.QueryLimitSwapsSummaryResponse{
			TotalLimitSwaps:  0,
			TotalValueUsd:    "0",
			AssetPairs:       []*types.AssetPairSummary{},
			OldestSwapBlocks: 0,
			AverageAgeBlocks: 0,
		}, nil
	}

	iterator := qs.mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var msg MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &msg); err != nil {
			continue
		}

		// Filter only limit swaps
		if !msg.IsLimitSwap() {
			continue
		}

		// Apply filters for summary endpoint
		if req.SourceAsset != "" && !strings.EqualFold(msg.Tx.Coins[0].Asset.String(), req.SourceAsset) {
			continue
		}
		if req.TargetAsset != "" && !strings.EqualFold(msg.TargetAsset.String(), req.TargetAsset) {
			continue
		}

		totalLimitSwaps++

		// Calculate age
		blocksSinceCreated := currentBlockHeight - msg.InitialBlockHeight
		totalAgeBlocks += blocksSinceCreated
		swapCount++

		if oldestSwapBlocks == 0 || blocksSinceCreated > oldestSwapBlocks {
			oldestSwapBlocks = blocksSinceCreated
		}

		// Calculate USD value using TOR pricing
		// Calculate remaining amount (not yet swapped)
		remainingAmount := common.SafeSub(msg.State.Deposit, msg.State.In)

		// Convert to RUNE value
		runeValue := remainingAmount
		if !msg.Tx.Coins[0].Asset.IsRune() {
			// Only get pool if source asset is not RUNE
			sourcePool, err := qs.mgr.Keeper().GetPool(ctx, msg.Tx.Coins[0].Asset.GetLayer1Asset())
			if err != nil || !sourcePool.IsAvailable() {
				// Skip if pool not available
				continue
			}
			runeValue = sourcePool.AssetValueInRune(remainingAmount)
		}

		// Convert RUNE to USD
		if !runeValue.IsZero() && !dollarsPerRune.IsZero() {
			usdValue := common.GetUncappedShare(runeValue, dollarsPerRune, cosmos.NewUint(common.One))
			totalValueUSD = totalValueUSD.Add(usdValue)
		}

		// Track asset pairs
		pairKey := fmt.Sprintf("%s-%s", msg.Tx.Coins[0].Asset.String(), msg.TargetAsset.String())
		if pair, exists := assetPairMap[pairKey]; exists {
			pair.Count++
			// Add to pair USD value
			// Calculate remaining amount (not yet swapped)
			pairRemainingAmount := common.SafeSub(msg.State.Deposit, msg.State.In)

			// Convert to RUNE value
			pairRuneValue := pairRemainingAmount
			if !msg.Tx.Coins[0].Asset.IsRune() {
				// Only get pool if source asset is not RUNE
				if pairSourcePool, err := qs.mgr.Keeper().GetPool(ctx, msg.Tx.Coins[0].Asset.GetLayer1Asset()); err == nil && pairSourcePool.IsAvailable() {
					pairRuneValue = pairSourcePool.AssetValueInRune(pairRemainingAmount)
				} else {
					pairRuneValue = cosmos.ZeroUint()
				}
			}

			// Convert RUNE to USD
			if !pairRuneValue.IsZero() && !dollarsPerRune.IsZero() {
				usdValue := common.GetUncappedShare(pairRuneValue, dollarsPerRune, cosmos.NewUint(common.One))
				currentValue := cosmos.ZeroUint()
				if pair.TotalValueUsd != "" {
					currentValue = cosmos.NewUintFromString(pair.TotalValueUsd)
				}
				newValue := currentValue.Add(usdValue)
				pair.TotalValueUsd = newValue.String()
			}
		} else {
			// Calculate initial USD value for this pair
			pairUsdValue := "0"

			// Calculate remaining amount (not yet swapped)
			initRemainingAmount := common.SafeSub(msg.State.Deposit, msg.State.In)

			// Convert to RUNE value
			initRuneValue := initRemainingAmount
			if !msg.Tx.Coins[0].Asset.IsRune() {
				// Only get pool if source asset is not RUNE
				if initSourcePool, err := qs.mgr.Keeper().GetPool(ctx, msg.Tx.Coins[0].Asset.GetLayer1Asset()); err == nil && initSourcePool.IsAvailable() {
					initRuneValue = initSourcePool.AssetValueInRune(initRemainingAmount)
				} else {
					initRuneValue = cosmos.ZeroUint()
				}
			}

			// Convert RUNE to USD
			if !initRuneValue.IsZero() && !dollarsPerRune.IsZero() {
				usdValue := common.GetUncappedShare(initRuneValue, dollarsPerRune, cosmos.NewUint(common.One))
				pairUsdValue = usdValue.String()
			}

			assetPairMap[pairKey] = &types.AssetPairSummary{
				SourceAsset:   msg.Tx.Coins[0].Asset.String(),
				TargetAsset:   msg.TargetAsset.String(),
				Count:         1,
				TotalValueUsd: pairUsdValue,
			}
		}
	}

	// Convert map to slice with deterministic ordering
	var assetPairs []*types.AssetPairSummary
	// Sort map keys first
	var sortedKeys []string
	// analyze-ignore(map-iteration)
	for key := range assetPairMap {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)

	// Iterate in sorted order
	for _, key := range sortedKeys {
		assetPairs = append(assetPairs, assetPairMap[key])
	}

	// Calculate average age
	var averageAgeBlocks int64
	if swapCount > 0 {
		averageAgeBlocks = totalAgeBlocks / swapCount
	}

	return &types.QueryLimitSwapsSummaryResponse{
		TotalLimitSwaps:  totalLimitSwaps,
		TotalValueUsd:    totalValueUSD.String(),
		AssetPairs:       assetPairs,
		OldestSwapBlocks: oldestSwapBlocks,
		AverageAgeBlocks: averageAgeBlocks,
	}, nil
}
