package keeperv1

import (
	"google.golang.org/protobuf/encoding/protowire"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type KeeperTHORNameSuite struct{}

var _ = Suite(&KeeperTHORNameSuite{})

func (s *KeeperTHORNameSuite) TestTHORName(c *C) {
	ctx, k := setupKeeperForTest(c)
	var err error
	ref := "helloworld"

	ok := k.THORNameExists(ctx, ref)
	c.Assert(ok, Equals, false)

	thorAddr := GetRandomTHORAddress()
	ethAddr := GetRandomETHAddress()
	name := NewTHORName(ref, 50, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}, {Chain: common.ETHChain, Address: ethAddr}})
	k.SetTHORName(ctx, name)

	ok = k.THORNameExists(ctx, ref)
	c.Assert(ok, Equals, true)
	ok = k.THORNameExists(ctx, "bogus")
	c.Assert(ok, Equals, false)

	name, err = k.GetTHORName(ctx, ref)
	c.Assert(err, IsNil)
	c.Assert(name.GetAlias(common.THORChain).Equals(thorAddr), Equals, true)
	c.Assert(name.GetAlias(common.ETHChain).Equals(ethAddr), Equals, true)
}

func (s *KeeperTHORNameSuite) TestAffiliateCollectorsByName(c *C) {
	ctx, k := setupKeeperForTest(c)

	collector, err := k.GetAffiliateCollector(ctx, "FEW")
	c.Assert(err, IsNil)
	c.Assert(collector.Name, Equals, "few")
	c.Assert(collector.RuneAmount.IsZero(), Equals, true)

	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "FEW", RuneAmount: cosmos.NewUint(100)}), IsNil)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "tan", RuneAmount: cosmos.NewUint(200)}), IsNil)

	collector, err = k.GetAffiliateCollector(ctx, "fEw")
	c.Assert(err, IsNil)
	c.Assert(collector.Name, Equals, "few")
	c.Assert(collector.RuneAmount.Equal(cosmos.NewUint(100)), Equals, true)
	collector.RuneAmount = collector.RuneAmount.AddUint64(25)
	c.Assert(k.SetAffiliateCollector(ctx, collector), IsNil)

	collector, err = k.GetAffiliateCollector(ctx, "TAN")
	c.Assert(err, IsNil)
	c.Assert(collector.Name, Equals, "tan")
	c.Assert(collector.RuneAmount.Equal(cosmos.NewUint(200)), Equals, true)

	collectors, err := k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Assert(collectors, HasLen, 2)
	c.Assert(collectors[0].Name, Equals, "few")
	c.Assert(collectors[0].RuneAmount.Equal(cosmos.NewUint(125)), Equals, true)
	c.Assert(collectors[1].Name, Equals, "tan")

	// Exported records retain the THORName used to rebuild their storage key.
	for _, original := range collectors {
		encoded, marshalErr := k.Cdc().Marshal(&original)
		c.Assert(marshalErr, IsNil)
		var restored AffiliateFeeCollector
		c.Assert(k.Cdc().Unmarshal(encoded, &restored), IsNil)
		c.Assert(restored.Name, Equals, original.Name)
		c.Assert(restored.RuneAmount.Equal(original.RuneAmount), Equals, true)
		c.Assert(k.SetAffiliateCollector(ctx, restored), IsNil)
	}
	collectors, err = k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Assert(collectors, HasLen, 2)
}

func (s *KeeperTHORNameSuite) TestAffiliateCollectorRejectsEmptyTHORName(c *C) {
	ctx, k := setupKeeperForTest(c)
	for _, amount := range []uint64{0, 100} {
		c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{RuneAmount: cosmos.NewUint(amount)}),
			ErrorMatches, "affiliate collector THORName cannot be empty")
	}
	_, err := k.GetAffiliateCollector(ctx, "")
	c.Assert(err, ErrorMatches, "affiliate collector THORName cannot be empty")
	collectors, err := k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Assert(collectors, HasLen, 0)

	// Reject empty identifiers without applying today's registration spelling rules.
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "THORSWAP.FI", RuneAmount: cosmos.NewUint(100)}), IsNil)
	collector, err := k.GetAffiliateCollector(ctx, "thorswap.fi")
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.String(), Equals, "100")
}

func (s *KeeperTHORNameSuite) TestAffiliateCollectorDeprecatedOwnerCompatibility(c *C) {
	ctx, k := setupKeeperForTest(c)
	owner := GetRandomBech32Addr()
	// Preserve the deprecated field on the wire without using it as the key.
	encoded := protowire.AppendTag(nil, 1, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, owner)
	encoded = protowire.AppendTag(encoded, 2, protowire.BytesType)
	encoded = protowire.AppendString(encoded, "123")
	encoded = protowire.AppendTag(encoded, 3, protowire.BytesType)
	encoded = protowire.AppendString(encoded, "few")
	var collector AffiliateFeeCollector
	c.Assert(k.Cdc().Unmarshal(encoded, &collector), IsNil)
	roundTrip, err := k.Cdc().Marshal(&collector)
	c.Assert(err, IsNil)
	c.Assert(roundTrip, DeepEquals, encoded)
	c.Assert(k.SetAffiliateCollector(ctx, collector), IsNil)
	stored, err := k.GetAffiliateCollector(ctx, "few")
	c.Assert(err, IsNil)
	c.Assert(stored.RuneAmount.String(), Equals, "123")
	stored, err = k.GetAffiliateCollector(ctx, owner.String())
	c.Assert(err, IsNil)
	c.Assert(stored.RuneAmount.IsZero(), Equals, true)
}

func (s *KeeperTHORNameSuite) TestAffiliateCollectorNamespaceIsolation(c *C) {
	ctx, k := setupKeeperForTest(c)
	owner := GetRandomBech32Addr()
	// Seed the actual retired wire format directly. Normal collector access
	// must neither expose this owner balance nor modify it before migration.
	oldValue := protowire.AppendTag(nil, 1, protowire.BytesType)
	oldValue = protowire.AppendBytes(oldValue, owner)
	oldValue = protowire.AppendTag(oldValue, 2, protowire.BytesType)
	oldValue = protowire.AppendString(oldValue, "987654")
	oldKey := k.GetKey("affcol/", owner.String())
	store := k.kvStore(ctx)
	store.Set(oldKey, oldValue)

	for _, name := range []string{"few", owner.String()} {
		collector, err := k.GetAffiliateCollector(ctx, name)
		c.Assert(err, IsNil)
		c.Check(collector.Name, Equals, name)
		c.Check(collector.RuneAmount.IsZero(), Equals, true)
	}
	iterator := k.GetAffiliateCollectorIterator(ctx)
	c.Check(iterator.Valid(), Equals, false)
	iterator.Close()
	collectors, err := k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Check(collectors, HasLen, 0)

	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "FEW", RuneAmount: cosmos.NewUint(100)}), IsNil)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "tan", RuneAmount: cosmos.NewUint(200)}), IsNil)
	collector, err := k.GetAffiliateCollector(ctx, "fEw")
	c.Assert(err, IsNil)
	c.Assert(collector.Name, Equals, "few")
	c.Assert(collector.RuneAmount.Equal(cosmos.NewUint(100)), Equals, true)
	collector.RuneAmount = cosmos.NewUint(125)
	c.Assert(k.SetAffiliateCollector(ctx, collector), IsNil)

	iterator = k.GetAffiliateCollectorIterator(ctx)
	var iterated []AffiliateFeeCollector
	for ; iterator.Valid(); iterator.Next() {
		var entry AffiliateFeeCollector
		c.Assert(k.Cdc().Unmarshal(iterator.Value(), &entry), IsNil)
		c.Check(iterator.Key(), DeepEquals, k.GetKey("affcol_name/", entry.Name))
		iterated = append(iterated, entry)
	}
	iterator.Close()
	collectors, err = k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Assert(collectors, HasLen, 2)
	c.Check(iterated, DeepEquals, collectors)
	c.Check(collectors[0].Name, Equals, "few")
	c.Check(collectors[0].RuneAmount.Equal(cosmos.NewUint(125)), Equals, true)
	c.Check(collectors[1].Name, Equals, "tan")
	c.Check(collectors[1].RuneAmount.Equal(cosmos.NewUint(200)), Equals, true)
	oldAfter := store.Get(oldKey)
	c.Check(oldAfter, DeepEquals, oldValue)
}
