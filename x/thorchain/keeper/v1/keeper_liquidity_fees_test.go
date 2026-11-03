package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type KeeperLiquidityFeesSuite struct{}

var _ = Suite(&KeeperLiquidityFeesSuite{})

func (s *KeeperLiquidityFeesSuite) TestLiquidityFees(c *C) {
	ctx, k := setupKeeperForTest(c)

	ctx = ctx.WithBlockHeight(10)
	height := uint64(ctx.BlockHeight())
	err := k.AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100))
	c.Assert(err, IsNil)
	err = k.AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100))
	c.Assert(err, IsNil)
	err = k.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(300))
	c.Assert(err, IsNil)

	i, err := k.GetTotalLiquidityFees(ctx, height)
	c.Assert(err, IsNil)
	c.Check(i.Uint64(), Equals, uint64(500))

	i, err = k.GetPoolLiquidityFees(ctx, height, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(i.Uint64(), Equals, uint64(200), Commentf("%d", i.Uint64()))

	rolling, err := k.GetRollingPoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rolling, Equals, uint64(200), Commentf("%d", rolling))

	i, err = k.GetPoolLiquidityFees(ctx, height, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(i.Uint64(), Equals, uint64(300), Commentf("%d", i.Uint64()))

	rolling, err = k.GetRollingPoolLiquidityFee(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(rolling, Equals, uint64(300), Commentf("%d", rolling))
}
