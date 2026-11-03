package thorchain

import (
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type VolumeManagerSuite struct{}

var _ = Suite(&VolumeManagerSuite{})

func (s *VolumeManagerSuite) TestConstant(c *C) {
	c.Assert(constants.VolumeBucketSeconds, Equals, int64(900))
}

func (s *VolumeManagerSuite) TestBuckets(c *C) {
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	err := mgr.K.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	volume, err := mgr.K.GetVolume(ctx, common.BTCAsset)
	c.Assert(err, NotNil)

	err = mgr.K.SetVolume(ctx, types.NewVolume(common.BTCAsset))
	c.Assert(err, IsNil)

	timestamp := time.Unix(0, 0) // start bucket = 0
	ctx = ctx.WithBlockTime(timestamp)

	for i := 0; i < 300; i++ {
		timestamp = timestamp.Add(time.Second * time.Duration(constants.VolumeBucketSeconds))
		ctx = ctx.WithBlockTime(timestamp)

		volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
		c.Assert(err, IsNil)

		volume.ChangeAsset = cosmos.NewUint(1)
		err = mgr.K.SetVolume(ctx, volume)
		c.Assert(err, IsNil)

		err = mgr.volumeManager.EndBlock(ctx)
		c.Assert(err, IsNil)
	}

	volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(volume, NotNil)
	c.Assert(volume.Valid(), IsNil)
	// +1 every 15mins = 96 / day
	c.Assert(volume.TotalAsset.String(), Equals, "96")
	// 300 % 96 = 12
	c.Assert(volume.LastBucket, Equals, int64(12))

	// halt chain for 1h, no new volume during that time
	timestamp = timestamp.Add(time.Hour)
	ctx = ctx.WithBlockTime(timestamp)

	err = mgr.volumeManager.EndBlock(ctx)
	c.Assert(err, IsNil)

	volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(volume.Valid(), IsNil)
	// -4 volume for one hour downtime
	c.Assert(volume.TotalAsset.String(), Equals, "92")
	// +4 buckets
	c.Assert(volume.LastBucket, Equals, int64(16))

	// no volume for 24 hours (need two blocks to change volume,LastUpdate)
	for i := 0; i < 2; i++ {
		timestamp = timestamp.Add(time.Hour * 12)
		ctx = ctx.WithBlockTime(timestamp)

		err = mgr.volumeManager.EndBlock(ctx)
		c.Assert(err, IsNil)
	}
	volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(volume.Valid(), IsNil)

	// no volume for 24 hours
	c.Assert(volume.TotalAsset.String(), Equals, "0")
	c.Assert(volume.LastBucket, Equals, int64(16))

	iterator := mgr.K.GetVolumeBucketIterator(ctx, volume.Asset)
	c.Assert(iterator, NotNil)
	defer iterator.Close()

	n := 0
	for ; iterator.Valid(); iterator.Next() {
		n++
	}
	c.Assert(n, Equals, 96)
}

func (s *VolumeManagerSuite) TestBucketWrites(c *C) {
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	err := mgr.K.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	volume := types.NewVolume(common.BTCAsset)
	err = mgr.K.SetVolume(ctx, volume)
	c.Assert(err, IsNil)

	// running updates twice, to simulate full 24h cycle
	for i := 0; i < 2; i++ {
		timestamp := time.Unix(0, 0) // start bucket = 0
		ctx = ctx.WithBlockTime(timestamp)

		for j := 0; j < 5; j++ {
			volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
			c.Assert(err, IsNil)

			switch j {
			case 0, 2, 4:
				volume.ChangeRune = cosmos.NewUint(123)
				err = mgr.K.SetVolume(ctx, volume)
				c.Assert(err, IsNil)
			}

			err = mgr.volumeManager.EndBlock(ctx)
			c.Assert(err, IsNil)

			timestamp = timestamp.Add(time.Second * time.Duration(constants.VolumeBucketSeconds))
			ctx = ctx.WithBlockTime(timestamp)
		}

		volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(volume.TotalRune.String(), Equals, "369")
	}

	volume, err = mgr.K.GetVolume(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(volume.TotalRune.String(), Equals, "369")

	iterator := mgr.K.GetVolumeBucketIterator(ctx, volume.Asset)
	c.Assert(iterator, NotNil)
	defer iterator.Close()

	// check amount of buckets
	n := 0
	for ; iterator.Valid(); iterator.Next() {
		n++
	}
	// only updated three buckets
	c.Assert(n, Equals, 3)

	// only buckets 0, 2 and 4 should be present
	for i := int64(0); i < 5; i++ {
		bucket, err := mgr.K.GetVolumeBucket(ctx, common.BTCAsset, i)
		switch i {
		case 0, 2, 4:
			c.Assert(err, IsNil)
			c.Assert(bucket.AmountRune.String(), Equals, "123")
		default:
			c.Assert(err, NotNil)
		}
	}
}
