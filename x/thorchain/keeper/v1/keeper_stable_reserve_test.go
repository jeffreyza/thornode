package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type KeeperStableReserveSuite struct{}

var _ = Suite(&KeeperStableReserveSuite{})

// TestStableReserveCredit_UnsetReturnsZero ensures an address with no stored
// credit reads back as zero rather than erroring.
func (s *KeeperStableReserveSuite) TestStableReserveCredit_UnsetReturnsZero(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomETHAddress()
	credit, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr)
	c.Assert(err, IsNil)
	c.Check(credit.IsZero(), Equals, true)
}

// TestStableReserveCredit_RoundTrip ensures a stored credit reads back exactly.
func (s *KeeperStableReserveSuite) TestStableReserveCredit_RoundTrip(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomETHAddress()
	amount := cosmos.NewUint(1234 * common.One)
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr, amount), IsNil)

	credit, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(amount), Equals, true)
}

// TestStableReserveCredit_Overwrite ensures a second Set replaces, not adds to,
// the prior value.
func (s *KeeperStableReserveSuite) TestStableReserveCredit_Overwrite(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomETHAddress()
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr, cosmos.NewUint(100)), IsNil)
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr, cosmos.NewUint(42)), IsNil)

	credit, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(42)), Equals, true)
}

// TestStableReserveCredit_ZeroDeletes ensures setting a credit to zero clears
// the entry so a later read still returns zero (and leaves no orphan key).
func (s *KeeperStableReserveSuite) TestStableReserveCredit_ZeroDeletes(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomETHAddress()
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr, cosmos.NewUint(500)), IsNil)
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr, cosmos.ZeroUint()), IsNil)

	credit, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr)
	c.Assert(err, IsNil)
	c.Check(credit.IsZero(), Equals, true)
}

// TestStableReserveCredit_PerAddressIsolation ensures one address's credit does
// not leak into another address under the same asset.
func (s *KeeperStableReserveSuite) TestStableReserveCredit_PerAddressIsolation(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomETHAddress()
	addr2 := GetRandomETHAddress()
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr1, cosmos.NewUint(777)), IsNil)

	credit2, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr2)
	c.Assert(err, IsNil)
	c.Check(credit2.IsZero(), Equals, true)
}

// TestStableReserveCredit_PerAssetIsolation ensures distinct layer-1 assets keep
// independent credit balances for the same address.
func (s *KeeperStableReserveSuite) TestStableReserveCredit_PerAssetIsolation(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomETHAddress()
	c.Assert(k.SetStableReserveCredit(ctx, common.ETHAsset, addr, cosmos.NewUint(900)), IsNil)

	btcCredit, err := k.GetStableReserveCredit(ctx, common.BTCAsset, addr)
	c.Assert(err, IsNil)
	c.Check(btcCredit.IsZero(), Equals, true)

	ethCredit, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr)
	c.Assert(err, IsNil)
	c.Check(ethCredit.Equal(cosmos.NewUint(900)), Equals, true)
}

// TestStableReserveCredit_CollapsesToLayer1 documents and guards the key design:
// the credit key is derived from GetLayer1Asset(), so synthetic and secured
// variants of the same underlying asset share one credit entry. A write through
// any variant must be visible through the others and through the layer-1 asset.
func (s *KeeperStableReserveSuite) TestStableReserveCredit_CollapsesToLayer1(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomETHAddress()
	amount := cosmos.NewUint(314 * common.One)

	// Write through the synthetic variant.
	synth := common.ETHAsset.GetSyntheticAsset()
	c.Assert(k.SetStableReserveCredit(ctx, synth, addr, amount), IsNil)

	// Visible through the layer-1 asset...
	l1Credit, err := k.GetStableReserveCredit(ctx, common.ETHAsset, addr)
	c.Assert(err, IsNil)
	c.Check(l1Credit.Equal(amount), Equals, true)

	// ...and through the secured variant.
	secured := common.ETHAsset.GetSecuredAsset()
	securedCredit, err := k.GetStableReserveCredit(ctx, secured, addr)
	c.Assert(err, IsNil)
	c.Check(securedCredit.Equal(amount), Equals, true)
}
