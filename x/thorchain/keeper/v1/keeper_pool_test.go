package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperPoolSuite struct{}

var _ = Suite(&KeeperPoolSuite{})

func (s *KeeperPoolSuite) TestPool(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Check(k.SetPool(ctx, Pool{}), NotNil) // empty asset should error

	pool := NewPool()
	pool.Asset = common.ETHAsset

	err := k.SetPool(ctx, pool)
	c.Assert(err, IsNil)
	pool, err = k.GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	c.Check(pool.Asset.Equals(common.ETHAsset), Equals, true)
	c.Check(k.PoolExist(ctx, common.ETHAsset), Equals, true)
	c.Check(k.PoolExist(ctx, common.BTCAsset), Equals, false)

	pools, err := k.GetPools(ctx)
	c.Assert(err, IsNil)
	c.Assert(pools, HasLen, 1)

	p, err := k.GetPool(ctx, common.BTCAsset)
	c.Check(err, IsNil)
	c.Check(p.Valid(), NotNil)
}

func (s *KeeperPoolSuite) TestPoolLUVI(c *C) {
	luvi := cosmos.NewUint(12345)

	ctx, k := setupKeeperForTest(c)
	k.SetPoolLUVI(ctx, common.BTCAsset, luvi)
	luvi2, err := k.GetPoolLUVI(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(luvi.Uint64(), Equals, luvi2.Uint64())
}

func (s *KeeperPoolSuite) TestRemovePoolDropsLuviAccumulator(c *C) {
	ctx, k := setupKeeperForTest(c)
	pool := NewPool()
	pool.Asset = common.ETHAsset
	c.Assert(k.SetPool(ctx, pool), IsNil)
	k.SetPoolLuviAccumulator(ctx, types.PoolLuvi{Asset: common.ETHAsset, Height: 1})
	_, found, err := k.GetPoolLuviAccumulator(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)

	k.RemovePool(ctx, common.ETHAsset)
	c.Check(k.PoolExist(ctx, common.ETHAsset), Equals, false)
	_, found, err = k.GetPoolLuviAccumulator(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)

	// removing a pool without an accumulator is fine
	k.RemovePool(ctx, common.BTCAsset)
}

func (s *KeeperPoolSuite) TestLuviCaptureUnreadablePool(c *C) {
	ctx, k := setupKeeperForTest(c)
	// a pool record that does not unmarshal
	k.kvStore(ctx).Set(k.GetKey(prefixPool, common.ETHAsset.String()), []byte{0xff})
	_, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, NotNil)

	// the journal captures the unreadable pool as an empty, zero-valued state
	jctx, j := keeper.WithLuviJournal(ctx)
	k.RemovePool(jctx, common.ETHAsset)
	pre, ok := j.Pre(common.ETHAsset)
	c.Assert(ok, Equals, true)
	c.Check(pre.IsValid(), Equals, false)
	c.Check(pre.SynthSupply.IsZero(), Equals, true)
}
