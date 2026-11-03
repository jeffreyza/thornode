package thorchain

import (
	"fmt"

	"cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type HandlerTCYClaim struct{}

var _ = Suite(&HandlerTCYClaim{})

func (s *HandlerTCYClaim) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)

	// happy path
	k.SetMimir(ctx, "TCYClaimingHalt", 0)
	addr := GetRandomRUNEAddress()
	l1Address := GetRandomBTCAddress()
	accSignerAddr, err := GetRandomRUNEAddress().AccAddress()
	c.Assert(err, IsNil)

	msg := NewMsgTCYClaim(addr, l1Address, accSignerAddr)
	handler := NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	addr = GetRandomRUNEAddress()
	l1Address = GetRandomRUNEAddress()
	accSignerAddr, err = GetRandomRUNEAddress().AccAddress()
	c.Assert(err, IsNil)

	msg = NewMsgTCYClaim(addr, l1Address, accSignerAddr)
	handler = NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// invalid msgs
	addr2 := GetRandomBTCAddress()
	l1Address2 := GetRandomBTCAddress()
	accSignerAddr2, err := GetRandomRUNEAddress().AccAddress()
	c.Assert(err, IsNil)

	msg = NewMsgTCYClaim(addr2, l1Address2, accSignerAddr2)
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "invalid rune address: unknown request")

	k.SetMimir(ctx, "TCYClaimingHalt", 1)
	addr = GetRandomRUNEAddress()
	l1Address = GetRandomBTCAddress()
	accSignerAddr, err = GetRandomRUNEAddress().AccAddress()
	c.Assert(err, IsNil)
	msg = NewMsgTCYClaim(addr, l1Address, accSignerAddr)
	handler = NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "tcy claiming is halted")

	k.SetMimir(ctx, "TCYClaimingHalt", 0)
	msg = &MsgTCYClaim{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerTCYClaim) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)

	// setup addresses info
	addr1Asset := common.BTCAsset
	l1Address1 := GetRandomBTCAddress()
	accL1Addr1 := cosmos.AccAddress(l1Address1.String())
	addr1 := GetRandomRUNEAddress()

	addr2Asset := common.ETHAsset
	l1Address2 := GetRandomETHAddress()
	accL1Addr2 := cosmos.AccAddress(l1Address2.String())
	addr2 := GetRandomRUNEAddress()

	addr3Asset := common.LTCAsset
	l1Address3 := GetRandomLTCAddress()
	accL1Addr3 := cosmos.AccAddress(l1Address3.String())
	addr3 := GetRandomRUNEAddress()

	addr4Asset := common.DOGEAsset
	l1Address4 := GetRandomLTCAddress()
	accL1Addr4 := cosmos.AccAddress(l1Address4.String())
	addr4 := GetRandomRUNEAddress()

	// mint TCY
	coin := common.NewCoin(common.TCY, cosmos.NewUint(210))
	err := k.MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)
	err = k.SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(coin))
	c.Assert(err, IsNil)
	claimingNameBalance := k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Assert(claimingNameBalance.Equal(math.NewUint(210)), Equals, true)

	// there should only be TCY contract stakers before sending msg
	stakers, err := k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	scAddresses, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(len(stakers), Equals, len(scAddresses))
	for i, staker := range stakers {
		c.Assert(staker.Address.String(), Equals, scAddresses[i].String())
	}

	// setup pools and claimers
	c.Assert(k.SetPool(ctx, types.Pool{Asset: addr1Asset}), IsNil)
	c.Assert(k.SetPool(ctx, types.Pool{Asset: addr2Asset}), IsNil)
	c.Assert(k.SetPool(ctx, types.Pool{Asset: addr3Asset}), IsNil)
	c.Assert(k.SetPool(ctx, types.Pool{Asset: addr4Asset}), IsNil)

	err = k.SetTCYClaimer(ctx, types.TCYClaimer{
		L1Address: l1Address1,
		Asset:     addr1Asset,
		Amount:    math.NewUint(30),
	})
	c.Assert(err, IsNil)
	_, err = k.GetTCYClaimer(ctx, l1Address1, addr1Asset)
	c.Assert(err, IsNil)

	err = k.SetTCYClaimer(ctx, types.TCYClaimer{
		L1Address: l1Address2,
		Asset:     addr2Asset,
		Amount:    math.NewUint(70),
	})
	c.Assert(err, IsNil)
	_, err = k.GetTCYClaimer(ctx, l1Address2, addr2Asset)
	c.Assert(err, IsNil)

	_, err = k.GetTCYClaimer(ctx, l1Address3, addr3Asset)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYClaimer doesn't exist: %s", l1Address3))

	err = k.SetTCYClaimer(ctx, types.TCYClaimer{
		L1Address: l1Address4,
		Asset:     addr4Asset,
		Amount:    math.NewUint(10),
	})
	c.Assert(err, IsNil)
	_, err = k.GetTCYClaimer(ctx, l1Address4, addr4Asset)
	c.Assert(err, IsNil)

	// Send TCY msg for address 1
	msg := NewMsgTCYClaim(addr1, l1Address1, accL1Addr1)
	handler := NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Only address 1 staker and Claiming module balance should be updated
	addr1Staker, err := k.GetTCYStaker(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(addr1Staker.Amount.Equal(math.NewUint(30)), Equals, true)
	_, err = k.GetTCYClaimer(ctx, l1Address1, addr1Asset)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYClaimer doesn't exist: %s", l1Address1))

	_, err = k.GetTCYStaker(ctx, addr2)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYStaker doesn't exist: %s", addr2))
	_, err = k.GetTCYStaker(ctx, addr3)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYStaker doesn't exist: %s", addr3))

	claimingNameBalance = k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Assert(claimingNameBalance.Equal(math.NewUint(180)), Equals, true)

	// Send TCY msg for address 2
	msg = NewMsgTCYClaim(addr2, l1Address2, accL1Addr2)
	handler = NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Address 1, 2 stakers and Claiming module balance should be updated
	addr1Staker, err = k.GetTCYStaker(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(addr1Staker.Amount.Equal(math.NewUint(30)), Equals, true)
	addr2Staker, err := k.GetTCYStaker(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(addr2Staker.Amount.Equal(math.NewUint(70)), Equals, true)
	_, err = k.GetTCYClaimer(ctx, l1Address2, addr2Asset)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYClaimer doesn't exist: %s", l1Address2))

	_, err = k.GetTCYStaker(ctx, addr3)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYStaker doesn't exist: %s", addr3))

	claimingNameBalance = k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Assert(claimingNameBalance.Equal(math.NewUint(110)), Equals, true)

	// Send TCY msg for address 3, which is not registered on TCYClaimer
	msg = NewMsgTCYClaim(addr3, l1Address3, accL1Addr3)
	handler = NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err.Error(), Equals, fmt.Sprintf("l1 address: (%s) doesn't have any tcy to claim", l1Address3))

	// Address 3 staker should not receive anything and balance should stay the same
	addr1Staker, err = k.GetTCYStaker(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(addr1Staker.Amount.Equal(math.NewUint(30)), Equals, true)
	addr2Staker, err = k.GetTCYStaker(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(addr2Staker.Amount.Equal(math.NewUint(70)), Equals, true)

	_, err = k.GetTCYStaker(ctx, addr3)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYStaker doesn't exist: %s", addr3))
	_, err = k.GetTCYClaimer(ctx, l1Address3, addr3Asset)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYClaimer doesn't exist: %s", l1Address3))

	claimingNameBalance = k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Assert(claimingNameBalance.Equal(math.NewUint(110)), Equals, true)

	// Send TCY msg for address 4
	msg = NewMsgTCYClaim(addr4, l1Address4, accL1Addr4)
	handler = NewTCYClaimHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Address 1, 2, 4 stakers and Claiming module balance should be updated
	addr1Staker, err = k.GetTCYStaker(ctx, addr1)
	c.Assert(err, IsNil)
	c.Assert(addr1Staker.Amount.Equal(math.NewUint(30)), Equals, true)
	addr2Staker, err = k.GetTCYStaker(ctx, addr2)
	c.Assert(err, IsNil)
	c.Assert(addr2Staker.Amount.Equal(math.NewUint(70)), Equals, true)
	addr4Staker, err := k.GetTCYStaker(ctx, addr4)
	c.Assert(err, IsNil)

	_, err = k.GetTCYStaker(ctx, addr3)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYStaker doesn't exist: %s", addr3))
	c.Assert(addr4Staker.Amount.Equal(math.NewUint(10)), Equals, true)

	_, err = k.GetTCYClaimer(ctx, l1Address4, addr4Asset)
	c.Assert(err.Error(), Equals, fmt.Sprintf("TCYClaimer doesn't exist: %s", l1Address4))

	claimingNameBalance = k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Assert(claimingNameBalance.Equal(math.NewUint(100)), Equals, true)
}
