package keeperv1

import (
	"cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	. "gopkg.in/check.v1"
)

type KeeperTCYStakerSuite struct{}

var _ = Suite(&KeeperTCYStakerSuite{})

func (s *KeeperTCYStakerSuite) TestTCYStaker(c *C) {
	ctx, k := setupKeeperForTest(c)
	initStakers := []TCYStaker{
		{
			Address: GetRandomRUNEAddress(),
			Amount:  math.NewUint(1 * common.One),
		},
		{
			Address: GetRandomRUNEAddress(),
			Amount:  math.NewUint(10 * common.One),
		},
		{
			Address: GetRandomRUNEAddress(),
			Amount:  math.NewUint(100 * common.One),
		},
		{
			Address: GetRandomRUNEAddress(),
			Amount:  math.NewUint(1000 * common.One),
		},
		{
			Address: GetRandomRUNEAddress(),
			Amount:  math.NewUint(10000 * common.One),
		},
	}

	// Set stakers
	for _, staker := range initStakers {
		c.Assert(k.SetTCYStaker(ctx, staker), IsNil)
	}

	stakers, err := k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)

	addresses, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	expectedLen := len(initStakers) + len(addresses)
	c.Assert(len(stakers), Equals, expectedLen)

	var staker TCYStaker
	for _, initStaker := range initStakers {
		c.Assert(k.TCYStakerExists(ctx, initStaker.Address), Equals, true)
		staker, err = k.GetTCYStaker(ctx, initStaker.Address)
		c.Assert(err, IsNil)
		c.Assert(staker.Amount.Equal(initStaker.Amount), Equals, true)
	}

	// Delete stakers
	for _, staker := range initStakers {
		k.DeleteTCYStaker(ctx, staker.Address)
	}

	stakers, err = k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)

	c.Assert(len(stakers), Equals, len(addresses))

	for _, staker := range initStakers {
		c.Assert(k.TCYStakerExists(ctx, staker.Address), Equals, false)
	}
}

// UpdateTCYStaker used to read the smart contract's balance, add the new amount and
// hand the result to SetTCYStaker, which drops it on the floor for those addresses -
// reporting success while the credit was lost.
func (s *KeeperTCYStakerSuite) TestUpdateTCYStakerRejectsSmartContractAddress(c *C) {
	ctx, k := setupKeeperForTest(c)

	addresses, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(len(addresses) > 0, Equals, true)
	scAddr := addresses[0]

	err = k.UpdateTCYStaker(ctx, scAddr, math.NewUint(100*common.One))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "cannot stake for TCY smart contract address: "+scAddr.String())

	// the synthetic staker still only reflects the contract's own (zero) balance
	staker, err := k.GetTCYStaker(ctx, scAddr)
	c.Assert(err, IsNil)
	c.Assert(staker.Amount.IsZero(), Equals, true)

	// regular addresses are unaffected
	addr := GetRandomRUNEAddress()
	c.Assert(k.UpdateTCYStaker(ctx, addr, math.NewUint(5*common.One)), IsNil)
	staker, err = k.GetTCYStaker(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(staker.Amount.Equal(math.NewUint(5*common.One)), Equals, true)
}
