package keeperv1

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type KeeperCloutSuite struct{}

var _ = Suite(&KeeperCloutSuite{})

func (s *KeeperCloutSuite) TestSwapperClout(c *C) {
	ctx, k := setupKeeperForTest(c)
	var err error

	addr := GetRandomTHORAddress()
	clout := NewSwapperClout(addr)
	clout.Score = cosmos.NewUint(1000)

	c.Assert(k.SetSwapperClout(ctx, clout), IsNil)

	clout, err = k.GetSwapperClout(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(clout.Address.String(), Equals, addr.String())
	c.Assert(clout.Score.String(), Equals, "1000")
}

func (s *KeeperCloutSuite) TestSwapperCloutZECTex(c *C) {
	ctx, k := setupKeeperForTest(c)

	var tex, transparent common.Address = "tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k", "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU"
	if common.CurrentChainNetwork == common.MockNet {
		tex, transparent = "textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays", "tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7"
	}

	// Clout credited to the TEX address is stored under the transparent address.
	clout := NewSwapperClout(tex)
	clout.Score = cosmos.NewUint(1000)
	c.Assert(k.SetSwapperClout(ctx, clout), IsNil)

	for _, addr := range []common.Address{tex, transparent} {
		clout, err := k.GetSwapperClout(ctx, addr)
		c.Assert(err, IsNil)
		c.Check(clout.Address, Equals, transparent)
		c.Check(clout.Score.String(), Equals, "1000")
	}
	iter := k.GetSwapperCloutIterator(ctx)
	defer iter.Close()
	c.Assert(iter.Valid(), Equals, true)
	c.Check(string(iter.Key()), Equals, string(k.GetKey(prefixSwapperClout, transparent.String())))
	iter.Next()
	c.Check(iter.Valid(), Equals, false)
}

func (s *KeeperCloutSuite) TestSwapperCloutPreservesUnconvertibleAddresses(c *C) {
	ctx, k := setupKeeperForTest(c)
	wrongNetwork := common.Address("textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays")
	if common.CurrentChainNetwork == common.MockNet {
		wrongNetwork = "tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k"
	}

	addresses := []common.Address{
		"tex1invalid", "textest1invalid", wrongNetwork,
		common.Address("tex" + strings.Repeat("1", 40)),
	}
	for _, addr := range addresses {
		comment := Commentf("address: %s", addr)
		clout := NewSwapperClout(addr)
		clout.Score = cosmos.NewUint(1000)
		clout.Spent = cosmos.NewUint(300)
		clout.Reclaimed = cosmos.NewUint(100)
		c.Assert(k.SetSwapperClout(ctx, clout), IsNil, comment)

		got, err := k.GetSwapperClout(ctx, addr)
		c.Assert(err, IsNil, comment)
		c.Check(got, DeepEquals, clout, comment)
	}

	iter := k.GetSwapperCloutIterator(ctx)
	defer iter.Close()
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Check(count, Equals, len(addresses))
}
