package thorchain

import (
	"cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type VolumeMgr struct {
	keeper keeper.Keeper
}

// newVolumeMgr creates a new instance of VolumeMgr
func newVolumeMgr(
	keeper keeper.Keeper,
) *VolumeMgr {
	return &VolumeMgr{
		keeper: keeper,
	}
}

func (vm *VolumeMgr) EndBlock(ctx cosmos.Context) error {
	pools, err := vm.keeper.GetPools(ctx)
	if err != nil {
		return err
	}

	timestamp := ctx.BlockHeader().Time.Unix()
	numBuckets := 60 * 60 * 24 / constants.VolumeBucketSeconds
	currentIndex := (timestamp / constants.VolumeBucketSeconds) % numBuckets

	for _, pool := range pools {
		volume, err := vm.keeper.GetVolume(ctx, pool.Asset)
		if err != nil {
			ctx.Logger().Error("fail to get volume", "asset", pool.Asset, "error", err)
			continue
		}

		initialVolume := volume

		// find all buckets between now and last update, in case the chain was
		// halted for a time larger than the bucket size
		var indexes []int64
		if volume.LastBucket >= 0 && volume.LastBucket != currentIndex {
			index := volume.LastBucket + 1
			for index != currentIndex {
				if index >= numBuckets {
					index = 0
					continue
				}

				indexes = append(indexes, index)
				index++
			}
		}

		indexes = append(indexes, currentIndex)

		for _, index := range indexes {
			var bucket types.VolumeBucket
			bucket, err = vm.keeper.GetVolumeBucket(ctx, pool.Asset, index)
			if err != nil {
				bucket = types.NewVolumeBucket(pool.Asset, index)
			}

			initialBucket := bucket

			if index != volume.LastBucket {
				// old bucket
				volume.TotalRune = common.SafeSub(volume.TotalRune, bucket.AmountRune)
				volume.TotalAsset = common.SafeSub(volume.TotalAsset, bucket.AmountAsset)

				// only set values to current bucket, zero out all intermediates
				if index == currentIndex {
					bucket.AmountRune = volume.ChangeRune
					bucket.AmountAsset = volume.ChangeAsset
				} else {
					bucket.AmountRune = cosmos.ZeroUint()
					bucket.AmountAsset = cosmos.ZeroUint()
				}
			} else {
				bucket.AmountRune = bucket.AmountRune.Add(volume.ChangeRune)
				bucket.AmountAsset = bucket.AmountAsset.Add(volume.ChangeAsset)
			}

			// avoid unnecessary store updates
			if !bucket.Equals(initialBucket) {
				err = vm.keeper.SetVolumeBucket(ctx, bucket)
				if err != nil {
					return err
				}
			}
		}

		volume.TotalRune = volume.TotalRune.Add(volume.ChangeRune)
		volume.TotalAsset = volume.TotalAsset.Add(volume.ChangeAsset)
		volume.ChangeRune = math.ZeroUint()
		volume.ChangeAsset = math.ZeroUint()
		volume.LastBucket = currentIndex

		// avoid unnecessary store updates
		if !volume.Equals(initialVolume) {
			err = vm.keeper.SetVolume(ctx, volume)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
