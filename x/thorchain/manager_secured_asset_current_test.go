package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type SecuredAssetMgrSuite struct{}

var _ = Suite(&SecuredAssetMgrSuite{})

func (s *SecuredAssetMgrSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s *SecuredAssetMgrSuite) TestDepositAndWithdraw(c *C) {
	ctx, k := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr := newSecuredAssetMgr(k, eventMgr)

	asset := common.BTCAsset
	securedAsset := asset.GetSecuredAsset()

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	// addr3 := GetRandomBech32Addr()

	_, err = mgr.Deposit(ctx, securedAsset, cosmos.NewUint(100*common.One), addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, NotNil)

	mintAmt, err := mgr.Deposit(ctx, asset, cosmos.NewUint(100*common.One), addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "10000000000btc-btc")

	bal := mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, cosmos.NewUint(100*common.One).String())

	mintAmt, err = mgr.Deposit(ctx, asset, cosmos.NewUint(50*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "5000000000btc-btc")

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, cosmos.NewUint(50*common.One).String())
	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, cosmos.NewUint(100*common.One).String())

	// withdrawal
	_, err = mgr.Withdraw(ctx, asset, cosmos.NewUint(30*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, NotNil)

	withdrawAmt, err := mgr.Withdraw(ctx, securedAsset, cosmos.NewUint(30*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "3000000000 BTC.BTC")
	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, cosmos.NewUint(20*common.One).String())
	withdrawAmt, err = mgr.Withdraw(ctx, securedAsset, cosmos.NewUint(30*common.One), addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "2000000000 BTC.BTC")
	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, cosmos.NewUint(0).String())
}

func (s *SecuredAssetMgrSuite) TestRateDecrease(c *C) {
	ctx, k := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr := newSecuredAssetMgr(k, eventMgr)

	asset := common.BCHAsset
	securedAsset := asset.GetSecuredAsset()

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()

	depositAmount1 := cosmos.NewUint(100 * common.One)
	depositAmount2 := cosmos.NewUint(200 * common.One)
	dilutionAmount := cosmos.NewUint(100 * common.One)
	dilutedAmount1 := cosmos.NewUint(75 * common.One)
	dilutedAmount2 := cosmos.NewUint(150 * common.One)

	mintAmt, err := mgr.Deposit(ctx, asset, depositAmount1, addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "10000000000bch-bch")

	mintAmt, err = mgr.Deposit(ctx, asset, depositAmount2, addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "20000000000bch-bch")

	bal := mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, depositAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, depositAmount2.String())

	// Simulate fee application logic.
	// Pool share tokens are minted without any corresponding token deposits to increase the pool depth
	// Account SDK token balance should remain fixed, `mgr.BalanceOf` should return the reduced amount

	coin := common.NewCoin(securedAsset, dilutionAmount)
	err = mgr.keeper.MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)

	// Total issued supply = 100 + 200 + 100 = 400
	// Deposited BTC = 300
	// Share ratio = 0.75
	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, dilutedAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, dilutedAmount2.String())

	// Fee removed, module burns its own share tokens
	err = mgr.keeper.BurnFromModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)

	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, depositAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, depositAmount2.String())
}

func (s *SecuredAssetMgrSuite) TestRateDecreaseDeposit(c *C) {
	ctx, k := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr := newSecuredAssetMgr(k, eventMgr)

	asset := common.BTCAsset
	securedAsset := asset.GetSecuredAsset()
	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	addr3 := GetRandomBech32Addr()

	depositAmount1 := cosmos.NewUint(1000)
	depositAmount2 := cosmos.NewUint(2000)
	dilutionAmount := cosmos.NewUint(1000)

	mintAmt, err := mgr.Deposit(ctx, asset, depositAmount1, addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "1000btc-btc")

	mintAmt, err = mgr.Deposit(ctx, asset, depositAmount2, addr2, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(mintAmt.String(), Equals, "2000btc-btc")

	bal := mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, depositAmount1.String())

	bal = mgr.BalanceOf(ctx, asset, addr2)
	c.Check(bal.String(), Equals, depositAmount2.String())

	// Simulate fee application logic.
	// Pool share tokens are minted without any corresponding token deposits to increase the pool depth
	// Account SDK token balance should remain fixed, `mgr.BalanceOf` should return the reduced amount

	coin := common.NewCoin(securedAsset, dilutionAmount)
	err = mgr.keeper.MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)

	// Ensure that new deposits have enough shares issued that new deposits are worth 1:1
	mintAmt, err = mgr.Deposit(ctx, asset, depositAmount1, addr3, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	// Issuance after a dilution will be greater than the deposit amount
	c.Check(mintAmt.String(), Equals, "1333btc-btc")
	bal = mgr.BalanceOf(ctx, asset, addr3)
	c.Check(bal.String(), Equals, depositAmount1.String())

	// Finally, check that Withdrawal logic allocates the right amount of the pool for given withdraw amount
	withdrawAmt, err := mgr.Withdraw(ctx, securedAsset, depositAmount1, addr1, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "750 BTC.BTC")

	bal = mgr.BalanceOf(ctx, asset, addr1)
	c.Check(bal.String(), Equals, "0")

	withdrawAmt, err = mgr.Withdraw(ctx, securedAsset, depositAmount1, addr3, common.NoAddress, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(withdrawAmt.String(), Equals, "1000 BTC.BTC")
	bal = mgr.BalanceOf(ctx, asset, addr3)
	c.Check(bal.String(), Equals, "0")
}
