package thorchain

import (
	"fmt"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

// benchAssets builds RUNE plus poolCount pool assets, mirroring getAssetPairs.
func benchAssets(poolCount int) []common.Asset {
	assets := []common.Asset{common.RuneAsset()}
	for i := 0; i < poolCount; i++ {
		asset, err := common.NewAsset(fmt.Sprintf("ETH.TKN%d-0X%040X", i, i))
		if err != nil {
			panic(err)
		}
		assets = append(assets, asset)
	}
	return assets
}

func benchPairs(assets []common.Asset) tradePairs {
	pairs := make(tradePairs, 0, len(assets)*(len(assets)-1))
	for _, a1 := range assets {
		for _, a2 := range assets {
			if !a1.Equals(a2) {
				pairs = append(pairs, genTradePair(a1, a2))
			}
		}
	}
	return pairs
}

func BenchmarkFindMatchingTrades(b *testing.B) {
	for _, poolCount := range []int{41, 60} {
		assets := benchAssets(poolCount)
		pairs := benchPairs(assets)
		index := newTradePairIndex(pairs)
		for _, swaps := range []int{100, 300} {
			b.Run(fmt.Sprintf("pools=%d/swaps=%d", poolCount, swaps), func(b *testing.B) {
				for n := 0; n < b.N; n++ {
					todo := make(tradePairs, 0)
					seen := make(tradePairSet)
					for i := 0; i < swaps; i++ {
						source := assets[i%len(assets)]
						target := assets[(i*7+1)%len(assets)]
						if source.Equals(target) {
							target = assets[(i*7+2)%len(assets)]
						}
						todo = todo.findMatchingTradesIndexed(genTradePair(source, target), index, seen)
					}
				}
			})
		}
	}
}

// BenchmarkLimitSwapTTLSharedExpiry captures the adversarial case where many
// orders select the same expiry height. The current list-valued TTL bucket is
// read, scanned, and rewritten for every insertion.
func BenchmarkLimitSwapTTLSharedExpiry(b *testing.B) {
	for _, orderCount := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("orders=%d", orderCount), func(b *testing.B) {
			ctx, mgr := setupBenchmarkManager(b)
			hashes := make([]common.TxID, orderCount)
			for i := range hashes {
				hashes[i] = common.TxID(fmt.Sprintf("%064X", i+1))
			}

			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				cacheCtx, _ := ctx.CacheContext()
				for _, hash := range hashes {
					if err := mgr.Keeper().AddToLimitSwapTTL(cacheCtx, 100_000, hash); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
