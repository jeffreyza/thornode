package keeperv1

import . "gopkg.in/check.v1"

type KeeperRefundSuite struct{}

var _ = Suite(&KeeperRefundSuite{})

func (s *KeeperRefundSuite) TestRefundCleanupPrunesAtExpiryHeight(c *C) {
	ctx, k := setupKeeperForTest(c)
	key := "refund-cleanup-key"

	k.SetRefundCleanup(ctx, key)
	c.Check(k.HasRefundCleanup(ctx, key), Equals, true)

	beforeExpiry := ctx.WithBlockHeight(ctx.BlockHeight() + refundCleanupRetentionBlocks - 1)
	k.PruneRefundCleanups(beforeExpiry)
	c.Check(k.HasRefundCleanup(beforeExpiry, key), Equals, true)

	atExpiry := ctx.WithBlockHeight(ctx.BlockHeight() + refundCleanupRetentionBlocks)
	c.Check(k.HasRefundCleanup(atExpiry, key), Equals, true)
	k.PruneRefundCleanups(atExpiry)
	c.Check(k.HasRefundCleanup(atExpiry, key), Equals, false)
}

func (s *KeeperRefundSuite) TestRefundCleanupPruneKeepsRefreshedMarker(c *C) {
	ctx, k := setupKeeperForTest(c)
	key := "refund-cleanup-key"
	oldExpiryHeight := ctx.BlockHeight() + refundCleanupRetentionBlocks

	k.SetRefundCleanup(ctx, key)

	refreshedCtx := ctx.WithBlockHeight(ctx.BlockHeight() + 10)
	k.SetRefundCleanup(refreshedCtx, key)

	oldExpiryCtx := ctx.WithBlockHeight(oldExpiryHeight)
	k.PruneRefundCleanups(oldExpiryCtx)
	c.Check(k.HasRefundCleanup(oldExpiryCtx, key), Equals, true)

	newExpiryCtx := refreshedCtx.WithBlockHeight(refreshedCtx.BlockHeight() + refundCleanupRetentionBlocks)
	k.PruneRefundCleanups(newExpiryCtx)
	c.Check(k.HasRefundCleanup(newExpiryCtx, key), Equals, false)
}
