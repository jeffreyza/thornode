package keeperv1

import (
	"cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

type KeeperTCYClaimerSuite struct{}

var _ = Suite(&KeeperTCYClaimerSuite{})

type ClaimerChain struct {
	address common.Address
	asset   common.Asset
}

func (s *KeeperTCYClaimerSuite) TestTCYClaimer(c *C) {
	ctx, k := setupKeeperForTest(c)

	claimerChains := []ClaimerChain{
		{GetRandomBCHAddress(), common.BCHAsset},
		{GetRandomBTCAddress(), common.BTCAsset},
		{GetRandomETHAddress(), common.ETHAsset},
		{GetRandomBCHAddress(), common.BCHAsset},
		{GetRandomETHAddress(), common.ETHAsset},
	}

	claimers := []TCYClaimer{
		{
			L1Address: claimerChains[0].address,
			Asset:     claimerChains[0].asset,
			Amount:    math.NewUint(1 * common.One),
		},
		{
			L1Address: claimerChains[1].address,
			Asset:     claimerChains[1].asset,
			Amount:    math.NewUint(10 * common.One),
		},
		{
			L1Address: claimerChains[2].address,
			Asset:     claimerChains[2].asset,
			Amount:    math.NewUint(100 * common.One),
		},
		{
			L1Address: claimerChains[3].address,
			Asset:     claimerChains[3].asset,
			Amount:    math.NewUint(1000 * common.One),
		},
		{
			L1Address: claimerChains[4].address,
			Asset:     claimerChains[4].asset,
			Amount:    math.NewUint(10000 * common.One),
		},
	}

	// claimerChains and claimers variables should have same length
	c.Assert(len(claimerChains), Equals, len(claimers))

	for _, claimer := range claimers {
		c.Assert(k.SetTCYClaimer(ctx, claimer), IsNil)
	}

	for i, claimerChain := range claimerChains {
		tcyClaimer, err := k.GetTCYClaimer(ctx, claimerChain.address, claimerChain.asset)
		c.Assert(err, IsNil)
		c.Assert(tcyClaimer.Amount.Equal(claimers[i].Amount), Equals, true)
	}
}

func (s *KeeperTCYClaimerSuite) TestListTCYClaimersFromL1Address(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr := GetRandomBCHAddress()
	addr2 := GetRandomBTCAddress()
	addr3 := GetRandomETHAddress()

	claimerChains := []ClaimerChain{
		{addr, common.BCHAsset},
		{addr, common.BTCAsset},
		{addr2, common.ETHAsset},
		{addr2, common.BCHAsset},
		{addr2, common.LTCAsset},
		{addr3, common.ETHAsset},
	}

	claimers := []TCYClaimer{
		{
			L1Address: claimerChains[0].address,
			Asset:     claimerChains[0].asset,
			Amount:    math.NewUint(1 * common.One),
		},
		{
			L1Address: claimerChains[1].address,
			Asset:     claimerChains[1].asset,
			Amount:    math.NewUint(10 * common.One),
		},
		{
			L1Address: claimerChains[2].address,
			Asset:     claimerChains[2].asset,
			Amount:    math.NewUint(100 * common.One),
		},
		{
			L1Address: claimerChains[3].address,
			Asset:     claimerChains[3].asset,
			Amount:    math.NewUint(1000 * common.One),
		},
		{
			L1Address: claimerChains[4].address,
			Asset:     claimerChains[4].asset,
			Amount:    math.NewUint(10000 * common.One),
		},
		{
			L1Address: claimerChains[5].address,
			Asset:     claimerChains[5].asset,
			Amount:    math.NewUint(100000 * common.One),
		},
	}

	// claimerChains and claimers variables should have same length
	c.Assert(len(claimerChains), Equals, len(claimers))

	for _, claimer := range claimers {
		c.Assert(k.SetTCYClaimer(ctx, claimer), IsNil)
	}

	// check correct claims
	tcyClaims, err := k.ListTCYClaimersFromL1Address(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(len(tcyClaims), Equals, 2)
	c.Assert(tcyClaims[0].Amount.Equal(claimers[0].Amount), Equals, true)
	c.Assert(tcyClaims[0].Asset.Equals(claimers[0].Asset), Equals, true)
	c.Assert(tcyClaims[1].Amount.Equal(claimers[1].Amount), Equals, true)
	c.Assert(tcyClaims[1].Asset.Equals(claimers[1].Asset), Equals, true)

	tcyClaims, err = k.ListTCYClaimersFromL1Address(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(len(tcyClaims), Equals, 3)
	c.Assert(tcyClaims[0].Amount.Equal(claimers[3].Amount), Equals, true)
	c.Assert(tcyClaims[0].Asset.Equals(claimers[3].Asset), Equals, true)
	c.Assert(tcyClaims[1].Amount.Equal(claimers[2].Amount), Equals, true)
	c.Assert(tcyClaims[1].Asset.Equals(claimers[2].Asset), Equals, true)
	c.Assert(tcyClaims[2].Amount.Equal(claimers[4].Amount), Equals, true)
	c.Assert(tcyClaims[2].Asset.Equals(claimers[4].Asset), Equals, true)

	tcyClaims, err = k.ListTCYClaimersFromL1Address(ctx, addr3)
	c.Assert(err, IsNil)
	c.Assert(len(tcyClaims), Equals, 1)
	c.Assert(tcyClaims[0].Amount.Equal(claimers[5].Amount), Equals, true)
	c.Assert(tcyClaims[0].Asset.Equals(claimers[5].Asset), Equals, true)
}
