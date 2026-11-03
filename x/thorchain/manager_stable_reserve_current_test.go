package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type StableReserveMgrSuite struct{}

var _ = Suite(&StableReserveMgrSuite{})

func (s *StableReserveMgrSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func setupStableReserveAsset(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, price string) {
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1_000_000 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.Keeper().SetMimir(ctx, "TorAnchor-"+strings.ReplaceAll(asset.String(), ".", "-"), 1)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: asset.Ticker.String(), Price: price}), IsNil)
}

func setStableReservePoolPriceBps(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, bps uint64) {
	pool, err := mgr.Keeper().GetPool(ctx, asset)
	c.Assert(err, IsNil)
	pool.BalanceRune = pool.BalanceAsset.MulUint64(bps).QuoUint64(constants.MaxBasisPts)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
}

func setupStableReserveSuite(c *C) (cosmos.Context, *Mgrs, common.Asset, common.Asset) {
	ctx, mgr := setupManagerForTest(c)
	primeStableReserveModule(c, ctx, mgr)
	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccount), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, nodeAccount.Bond.Uint64())
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	setupStableReserveAsset(c, ctx, mgr, usdc, "1.00")
	setupStableReserveAsset(c, ctx, mgr, usdt, "1.00")
	mgr.Keeper().SetMimir(ctx, constants.StableReserveEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.StableReserveDepegToleranceBps.String(), 100)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	return ctx, mgr, usdc, usdt
}

func primeStableReserveModule(c *C, ctx cosmos.Context, mgr *Mgrs) {
	coin := common.NewCoin(common.RuneNative, cosmos.OneUint())
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, StableReserveName, common.NewCoins(coin)), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, StableReserveName, ModuleName, common.NewCoins(coin)), IsNil)
	c.Assert(mgr.Keeper().BurnFromModule(ctx, ModuleName, coin), IsNil)
}

func fundStableReserveSurplus(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, amount cosmos.Uint) {
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	_, err := mgr.SecuredAssetManager().Deposit(ctx, asset, amount, moduleAddr, common.Address(moduleAddr.String()), common.BlankTxID)
	c.Assert(err, IsNil)
}

func (s *StableReserveMgrSuite) TestStableReserveMemoRecognitionStripsTail(c *C) {
	// Must match newParser's "|" stripping so a "STABLE+|app" memo routed as a
	// stable-reserve deposit is still recognised in the handler (otherwise the
	// deposit falls through to secured-asset minting to the validator).
	c.Assert(isStableReserveDepositMemo("STABLE+"), Equals, true)
	c.Assert(isStableReserveDepositMemo("STABLE+|app"), Equals, true)
	c.Assert(isStableReserveDepositMemo("stable+|app:x"), Equals, true)
	c.Assert(isStableReserveDepositMemo("SECURE+|app"), Equals, false)
	c.Assert(isStableReserveWithdrawMemo("STABLE-|app"), Equals, true)
	c.Assert(isStableReserveWithdrawMemo("STABLE-"), Equals, true)
	c.Assert(isStableReserveWithdrawMemo("SECURE-|app"), Equals, false)
}

func (s *StableReserveMgrSuite) TestStableEligibilityRequiresTorAnchorAndHealthyOracle(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()

	eligible, reason := stableReserveMgr.IsEligibleStable(ctx, usdc)
	c.Check(eligible, Equals, true)
	c.Check(reason, Equals, "")

	// Eligibility checks the original asset BEFORE GetLayer1Asset normalization,
	// so wrapped forms of an eligible external L1 stable are rejected: synths,
	// secured assets, and trade assets cannot be filled by the stable reserve.
	for _, wrapped := range []common.Asset{
		usdc.GetSyntheticAsset(),
		usdc.GetSecuredAsset(),
		usdc.GetTradeAsset(),
	} {
		eligible, reason = stableReserveMgr.IsEligibleStable(ctx, wrapped)
		c.Check(eligible, Equals, false)
		c.Check(reason, Equals, "non-l1 stable")
	}

	// A THORChain-native asset is not an external L1 asset and is rejected.
	eligible, reason = stableReserveMgr.IsEligibleStable(ctx, common.RuneAsset())
	c.Check(eligible, Equals, false)
	c.Check(reason, Equals, "non-l1 stable")

	btc := common.BTCAsset
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: "BTC", Price: "1.00"}), IsNil)
	eligible, reason = stableReserveMgr.IsEligibleStable(ctx, btc)
	c.Check(eligible, Equals, false)
	c.Check(reason, Equals, "non-tor-anchor asset")

	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdc.Ticker.String(), Price: "0.98"}), IsNil)
	eligible, reason = stableReserveMgr.IsEligibleStable(ctx, usdc)
	c.Check(eligible, Equals, false)
	c.Check(reason, Equals, "unhealthy oracle price")

	mgr.Keeper().DelPrice(ctx, usdc.Ticker.String())
	eligible, reason = stableReserveMgr.IsEligibleStable(ctx, usdc)
	c.Check(eligible, Equals, false)
	c.Check(reason, Equals, "missing oracle price")
}

func (s *StableReserveMgrSuite) TestStableEligibilityRejectsChainSpecificPoolDepeg(c *C) {
	ctx, mgr, ethUSDC, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", int64(common.One))

	avaxUSDC, err := common.NewAsset("AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E")
	c.Assert(err, IsNil)
	setupStableReserveAsset(c, ctx, mgr, avaxUSDC, "1.00")
	setStableReservePoolPriceBps(c, ctx, mgr, avaxUSDC, 9_800)

	eligible, reason := stableReserveMgr.IsEligibleStable(ctx, avaxUSDC)
	c.Check(eligible, Equals, false)
	c.Check(reason, Equals, "unhealthy pool price")

	eligible, reason = stableReserveMgr.IsEligibleStable(ctx, ethUSDC)
	c.Check(eligible, Equals, true)
	c.Check(reason, Equals, "")
}

func (s *StableReserveMgrSuite) TestStableEligibilityRejectsSelfNormalizedPoolDepeg(c *C) {
	ctx, mgr := setupManagerForTest(c)
	stableReserveMgr := mgr.StableReserveManager()
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	setupStableReserveAsset(c, ctx, mgr, usdc, "1.00")
	setStableReservePoolPriceBps(c, ctx, mgr, usdc, 9_800)
	mgr.Keeper().SetMimir(ctx, constants.StableReserveDepegToleranceBps.String(), 100)

	eligible, reason := stableReserveMgr.IsEligibleStable(ctx, usdc)
	c.Check(eligible, Equals, false)
	c.Check(reason, Equals, "missing pool price")
}

func (s *StableReserveMgrSuite) TestContributorDepositAndWithdrawalCredits(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	amount := cosmos.NewUint(100 * common.One)

	err := stableReserveMgr.Deposit(ctx, usdc, amount, contributor, common.BlankTxID)
	c.Assert(err, IsNil)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(amount), Equals, true)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	inventory := mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr)
	c.Check(inventory.Equal(amount), Equals, true)

	withdrawCoin, filled, err := stableReserveMgr.Withdraw(ctx, usdc, cosmos.NewUint(40*common.One), contributor, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, true)
	c.Check(withdrawCoin.Equals(common.NewCoin(usdc, cosmos.NewUint(40*common.One))), Equals, true)

	credit, err = mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(60*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveDisabledBlocksModulePaths(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	mgr.Keeper().SetMimir(ctx, constants.StableReserveEnabled.String(), 0)

	err := stableReserveMgr.Deposit(ctx, usdc, cosmos.NewUint(100*common.One), GetRandomETHAddress(), common.BlankTxID)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "stable reserve disabled")

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	filled, emit, err := stableReserveMgr.TrySwap(ctx, mgr, *msg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(emit.IsZero(), Equals, true)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)
	_, err = firstQueuedSwapWithMemoPrefix(ctx, mgr, "stable-reserve-")
	c.Assert(err, NotNil)
}

func (s *StableReserveMgrSuite) TestStableReserveRejectsUnsupportedSwapAttempts(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	c.Assert(stableReserveMgr.Deposit(ctx, usdt, cosmos.NewUint(150*common.One), GetRandomETHAddress(), common.BlankTxID), IsNil)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	limitMsg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, LimitSwap, 0, 0, types.SwapVersion_v2, GetRandomBech32Addr())
	filled, _, err := stableReserveMgr.TrySwap(ctx, mgr, *limitMsg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)

	secondAttempt := *NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	secondAttempt.State = &types.SwapState{Count: 1}
	filled, _, err = stableReserveMgr.TrySwap(ctx, mgr, secondAttempt)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)
}

// TestStableReserveSkipsFirstSubSwapOfStream verifies the fix for the direct-fill
// leak on a V2 stream's first sub-swap. An eligible stable→stable MARKET swap
// that would otherwise fill is rejected because it is streaming (Quantity > 1),
// even though State.Count is still 0 (the queue increments Count only after
// dispatch). Quantity is resolved at queue admission, so IsStreaming() is already
// true here; the earlier Count > 0 guard alone let this first slice take a direct
// fill instead of streaming through the pools.
func (s *StableReserveMgrSuite) TestStableReserveSkipsFirstSubSwapOfStream(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	// Fund the module so a direct fill WOULD succeed if the guard failed to exclude it.
	c.Assert(stableReserveMgr.Deposit(ctx, usdt, cosmos.NewUint(150*common.One), GetRandomETHAddress(), common.BlankTxID), IsNil)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	// First sub-swap of a 2-part V2 stream: eligible market swap, Count == 0 but Quantity == 2.
	streamMsg := *NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 2, 1, types.SwapVersion_v2, GetRandomBech32Addr())
	streamMsg.State = &types.SwapState{Count: 0, Quantity: 2, Interval: 1, Deposit: cosmos.NewUint(100 * common.One)}
	c.Assert(streamMsg.IsStreaming(), Equals, true)

	filled, _, err := stableReserveMgr.TrySwap(ctx, mgr, streamMsg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false, Commentf("first sub-swap of a V2 stream must stream, not take a direct SR fill"))
	// Inventory untouched: no source deposited, no target withdrawn.
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveWithdrawalClampsToCredit(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	amount := cosmos.NewUint(100 * common.One)
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, amount, contributor, common.BlankTxID), IsNil)

	// Requesting more than the credit balance is no longer an error: the
	// withdrawal is clamped down to the available credit (100) and filled.
	withdrawCoin, filled, err := stableReserveMgr.Withdraw(ctx, usdc, cosmos.NewUint(120*common.One), contributor, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, true)
	c.Check(withdrawCoin.Equals(common.NewCoin(usdc, amount)), Equals, true)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.IsZero(), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveWithdrawalPartialFillAgainstInventory(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	amount := cosmos.NewUint(100 * common.One)
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, amount, contributor, common.BlankTxID), IsNil)

	// Drain 70 of the module's inventory directly, leaving only 30 behind while
	// the contributor's credit stays at 100.
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	_, err := mgr.SecuredAssetManager().Withdraw(ctx, usdc.GetSecuredAsset(), cosmos.NewUint(70*common.One), moduleAddr, contributor, common.BlankTxID)
	c.Assert(err, IsNil)

	// Request the full 100: only the 30 of remaining inventory is sent, and the
	// credit is debited by exactly that amount (100 - 30 = 70).
	withdrawCoin, filled, err := stableReserveMgr.Withdraw(ctx, usdc, cosmos.NewUint(100*common.One), contributor, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, true)
	c.Check(withdrawCoin.Equals(common.NewCoin(usdc, cosmos.NewUint(30*common.One))), Equals, true)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(70*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveWithdrawalUnfilledPreservesCredit(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	amount := cosmos.NewUint(100 * common.One)
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, amount, contributor, common.BlankTxID), IsNil)

	// Drain the module's entire inventory: with nothing to pay out, the
	// withdrawal is unfilled and the contributor's credit is preserved in full.
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	_, err := mgr.SecuredAssetManager().Withdraw(ctx, usdc.GetSecuredAsset(), amount, moduleAddr, contributor, common.BlankTxID)
	c.Assert(err, IsNil)
	withdrawCoin, filled, err := stableReserveMgr.Withdraw(ctx, usdc, cosmos.NewUint(10*common.One), contributor, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(withdrawCoin.Equals(common.NoCoin), Equals, true)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(amount), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveQueriesExposeInventoryAndContributorCredit(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	amount := cosmos.NewUint(100 * common.One)
	mgr.Keeper().SetMimir(ctx, constants.StableReservePOLShareBps.String(), 2500)
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, amount, contributor, common.BlankTxID), IsNil)

	resp, err := queryServer{mgr: mgr}.queryStableReserve(ctx, &types.QueryStableReserveRequest{})
	c.Assert(err, IsNil)
	c.Check(resp.Enabled, Equals, true)
	c.Check(resp.DepegToleranceBps, Equals, "100")
	c.Check(resp.PolShareBps, Equals, "2500")

	inventory := make(map[string]*types.QueryStableReserveInventory)
	for _, item := range resp.Inventory {
		inventory[item.Asset] = item
	}
	c.Assert(inventory[usdc.String()], NotNil)
	c.Check(inventory[usdc.String()].Amount, Equals, amount.String())
	c.Check(inventory[usdc.String()].Healthy, Equals, true)
	c.Check(inventory[usdc.String()].OraclePrice, Equals, "1.00")
	c.Assert(inventory[usdt.String()], NotNil)
	c.Check(inventory[usdt.String()].Amount, Equals, "0")
	c.Check(inventory[usdt.String()].Healthy, Equals, true)

	contributorResp, err := queryServer{mgr: mgr}.queryStableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{
		Asset:   usdc.String(),
		Address: contributor.String(),
	})
	c.Assert(err, IsNil)
	c.Check(contributorResp.Asset, Equals, usdc.String())
	c.Check(contributorResp.Address, Equals, contributor.String())
	c.Check(contributorResp.Credit, Equals, amount.String())
}

func (s *StableReserveMgrSuite) TestStableReserveContributorQueryRejectsInvalidParams(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	qs := queryServer{mgr: mgr}

	_, err := qs.queryStableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{
		Address: GetRandomETHAddress().String(),
	})
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "asset not provided")

	_, err = qs.queryStableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{
		Asset:   "E1.USDC",
		Address: GetRandomETHAddress().String(),
	})
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "could not parse asset:.*")

	_, err = qs.queryStableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{
		Asset:   common.RuneAsset().String(),
		Address: GetRandomETHAddress().String(),
	})
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "invalid stable reserve asset: THOR.RUNE")

	_, err = qs.queryStableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{
		Asset: usdc.String(),
	})
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "address not provided")

	_, err = qs.queryStableReserveContributor(ctx, &types.QueryStableReserveContributorRequest{
		Asset:   usdc.String(),
		Address: "not-an-address",
	})
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "could not parse address:.*")
}

func (s *StableReserveMgrSuite) TestStableReserveMemosRouteThroughSecuredAssetHandlers(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	contributor := GetRandomETHAddress()
	signer := GetRandomBech32Addr()

	depositTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: contributor,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(50*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		Memo:        "STABLE+",
	}
	depositMsg, err := processOneTxIn(ctx, mgr.Keeper(), NewObservedTx(depositTx, ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()), signer)
	c.Assert(err, IsNil)
	stableDeposit, ok := depositMsg.(*MsgSecuredAssetDeposit)
	c.Assert(ok, Equals, true)
	c.Check(stableDeposit.Asset.Equals(usdc), Equals, true)
	c.Check(stableDeposit.Amount.Equal(cosmos.NewUint(50*common.One)), Equals, true)

	_, err = NewSecuredAssetDepositHandler(mgr).Run(ctx, stableDeposit)
	c.Assert(err, IsNil)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(50*common.One)), Equals, true)

	trailingDepositTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: contributor,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(25*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		Memo:        "STABLE+:1",
	}
	trailingDepositMsg, err := processOneTxIn(ctx, mgr.Keeper(), NewObservedTx(trailingDepositTx, ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()), signer)
	c.Assert(err, IsNil)
	stableTrailingDeposit, ok := trailingDepositMsg.(*MsgSecuredAssetDeposit)
	c.Assert(ok, Equals, true)

	_, err = NewSecuredAssetDepositHandler(mgr).Run(ctx, stableTrailingDeposit)
	c.Assert(err, IsNil)

	credit, err = mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(75*common.One)), Equals, true)
	signerAddress := common.Address(signer.String())
	signerCredit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, signerAddress)
	c.Assert(err, IsNil)
	c.Check(signerCredit.IsZero(), Equals, true)

	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: contributor,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(usdc, cosmos.OneUint())),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		Memo:        "STABLE-:4000000000",
	}
	withdrawMsg, err := processOneTxIn(ctx, mgr.Keeper(), NewObservedTx(withdrawTx, ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()), signer)
	c.Assert(err, IsNil)
	stableWithdraw, ok := withdrawMsg.(*MsgSecuredAssetWithdraw)
	c.Assert(ok, Equals, true)
	c.Check(stableWithdraw.Asset.Equals(usdc.GetSecuredAsset()), Equals, true)
	c.Check(stableWithdraw.Amount.Equal(cosmos.NewUint(40*common.One)), Equals, true)
	c.Check(stableWithdraw.AssetAddress.Equals(contributor), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveWithdrawalHandlerCreatesOutbound(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	contributor := GetRandomETHAddress()
	signer := GetRandomBech32Addr()
	stableReserveMgr := mgr.StableReserveManager()
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, cosmos.NewUint(100*common.One), contributor, common.BlankTxID), IsNil)

	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(usdc, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.ETHChain, NetworkFee{
		Chain: common.ETHChain, TransactionSize: 80000, TransactionFeeRate: 30,
	}), IsNil)

	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: contributor,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(usdc, cosmos.OneUint())),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		Memo:        "STABLE-:4000000000",
	}
	withdrawMsg, err := processOneTxIn(ctx, mgr.Keeper(), NewObservedTx(withdrawTx, ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()), signer)
	c.Assert(err, IsNil)

	_, err = NewSecuredAssetWithdrawHandler(mgr).Run(ctx, withdrawMsg)
	c.Assert(err, IsNil)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(60*common.One)), Equals, true)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(cosmos.NewUint(60*common.One)), Equals, true)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, withdrawTx.ID)
	c.Assert(err, IsNil)
	items, err := mgr.Keeper().GetTxOut(ctx, voter.OutboundHeight)
	c.Assert(err, IsNil)
	c.Assert(items.TxArray, HasLen, 1)
	c.Check(items.TxArray[0].Coin.Equals(common.NewCoin(usdc, cosmos.NewUint(40*common.One))), Equals, true)
	c.Check(items.TxArray[0].ToAddress.Equals(contributor), Equals, true)

	feeSwap, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, "stable-reserve-withdraw-fee:")
	c.Assert(err, IsNil)
	c.Check(feeSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true)
	c.Check(feeSwap.Tx.Coins[0].Equals(common.NewCoin(usdc, cosmos.OneUint())), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveWithdrawalHandlerUnfilledChargesNoFee(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	contributor := GetRandomETHAddress()
	signer := GetRandomBech32Addr()
	stableReserveMgr := mgr.StableReserveManager()
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, cosmos.NewUint(100*common.One), contributor, common.BlankTxID), IsNil)

	// Drain the module's USDC inventory so the withdrawal cannot be filled, while
	// leaving the contributor's credit intact.
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	_, err := mgr.SecuredAssetManager().Withdraw(ctx, usdc.GetSecuredAsset(), cosmos.NewUint(100*common.One), moduleAddr, contributor, common.BlankTxID)
	c.Assert(err, IsNil)
	c.Assert(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)

	withdrawTx := common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: contributor,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(usdc, cosmos.OneUint())),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		Memo:        "STABLE-:4000000000",
	}
	withdrawMsg, err := processOneTxIn(ctx, mgr.Keeper(), NewObservedTx(withdrawTx, ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()), signer)
	c.Assert(err, IsNil)

	_, err = NewSecuredAssetWithdrawHandler(mgr).Run(ctx, withdrawMsg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "stable reserve withdrawal unfilled")

	// The withdrawal did not fill: the contributor's credit must be preserved and
	// no request-coin fee swap may be queued (the fee is charged only on a fill).
	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(100*common.One)), Equals, true)

	_, err = firstQueuedSwapWithMemoPrefix(ctx, mgr, types.StableReserveWithdrawFeeMemoPrefix)
	c.Assert(err, NotNil)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapFillMutatesReserveAndEmitsStableReserveEvent(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	fundStableReserveSurplus(c, ctx, mgr, usdt, cosmos.NewUint(150*common.One))

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	_, emit, err := NewSwapHandler(mgr).RunWithEmit(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(emit.Equal(cosmos.NewUint(100*common.One)), Equals, true)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(50*common.One)), Equals, true)

	found := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != types.SwapEventType {
			continue
		}
		for _, attr := range evt.Attributes {
			if attr.Key == "stable_reserve" && attr.Value == "true" {
				found = true
			}
			if attr.Key == "liquidity_fee" {
				c.Check(attr.Value, Equals, "0")
			}
		}
	}
	c.Check(found, Equals, true)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapCanSpendCreditedInventory(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	c.Assert(stableReserveMgr.Deposit(ctx, usdt, cosmos.NewUint(150*common.One), contributor, common.BlankTxID), IsNil)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	filled, emit, err := stableReserveMgr.TrySwap(ctx, mgr, *msg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, true)
	c.Check(emit.Equal(cosmos.NewUint(100*common.One)), Equals, true)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(50*common.One)), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(cosmos.NewUint(100*common.One)), Equals, true)
	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdt, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(150*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapSkipsLegacyStreaming(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	c.Assert(stableReserveMgr.Deposit(ctx, usdt, cosmos.NewUint(150*common.One), contributor, common.BlankTxID), IsNil)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	// Legacy (V1) streaming market swap: interval > 0 with a v1 version. TrySwap
	// runs before the persisted StreamingSwap record exists, so filling the full
	// deposit here would be re-paid every block. It must decline and defer to the
	// streaming machinery.
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 5, 10, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(msg.IsLegacyStreaming(), Equals, true)

	filled, emit, err := stableReserveMgr.TrySwap(ctx, mgr, *msg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(emit.IsZero(), Equals, true)

	// Inventory and credit are untouched — no deposit was pulled or paid out.
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdt, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(cosmos.NewUint(150*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapRefusesValueLosingFill(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	fundStableReserveSurplus(c, ctx, mgr, usdt, cosmos.NewUint(150*common.One))

	// Both stables stay inside the depeg tolerance band ($1 ± 1%) and so pass
	// eligibility, but they sit on opposite edges: filling usdc -> usdt 1:1 would
	// pay out ~2% of free reserve value. The value guard must refuse the fill.
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", int64(common.One))
	setStableReservePoolPriceBps(c, ctx, mgr, usdc, 9_900)
	setStableReservePoolPriceBps(c, ctx, mgr, usdt, 10_100)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	filled, emit, err := stableReserveMgr.TrySwap(ctx, mgr, *msg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(emit.IsZero(), Equals, true)

	// Reserve inventory untouched by the refused fill.
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapOraclePricingFillsBandEdges(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	fundStableReserveSurplus(c, ctx, mgr, usdt, cosmos.NewUint(150*common.One))

	// Same opposite-band-edge pair the 1:1 path refuses above. With
	// StableReserveOraclePricing on, the fill goes through but the output is
	// scaled by srcPrice/tgtPrice instead of paying out 1:1.
	mgr.Keeper().SetMimir(ctx, constants.StableReserveOraclePricing.String(), 1)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", int64(common.One))
	setStableReservePoolPriceBps(c, ctx, mgr, usdc, 9_900)
	setStableReservePoolPriceBps(c, ctx, mgr, usdt, 10_100)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	filled, emit, err := stableReserveMgr.TrySwap(ctx, mgr, *msg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, true)
	// floor(100e8 * 0.99 / 1.01) = 9_801_980_198, less than a 1:1 fill.
	c.Check(emit.Equal(cosmos.NewUint(9_801_980_198)), Equals, true)

	// The reserve is credited the full source amount, not the scaled output.
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(cosmos.NewUint(100*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapRefusesSameTickerPoolDepeg(c *C) {
	ctx, mgr, ethUSDC, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", int64(common.One))

	avaxUSDC, err := common.NewAsset("AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E")
	c.Assert(err, IsNil)
	setupStableReserveAsset(c, ctx, mgr, avaxUSDC, "1.00")
	setStableReservePoolPriceBps(c, ctx, mgr, avaxUSDC, 9_800)
	fundStableReserveSurplus(c, ctx, mgr, ethUSDC, cosmos.NewUint(150*common.One))

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(avaxUSDC, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.AVAXAsset, cosmos.OneUint())},
		"=:ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
	)
	msg := NewMsgSwap(tx, ethUSDC, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	filled, emit, err := stableReserveMgr.TrySwap(ctx, mgr, *msg)
	c.Assert(err, IsNil)
	c.Check(filled, Equals, false)
	c.Check(emit.IsZero(), Equals, true)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, ethUSDC, moduleAddr).Equal(cosmos.NewUint(150*common.One)), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, avaxUSDC, moduleAddr).IsZero(), Equals, true)
}

func (s *StableReserveMgrSuite) TestFreeStableSwapFallbackDoesNotMutateReserve(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	fundStableReserveSurplus(c, ctx, mgr, usdt, cosmos.NewUint(10*common.One))

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())},
		"=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
	)
	msg := NewMsgSwap(tx, usdt, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	_, _, err := NewSwapHandler(mgr).RunWithEmit(ctx, msg)
	c.Assert(err, IsNil)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdt, moduleAddr).Equal(cosmos.NewUint(10*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestRuneDeploymentSchedulesLowestInventoryHealthyStable(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, cosmos.NewUint(20*common.One), GetRandomETHAddress(), common.BlankTxID), IsNil)
	FundModule(c, ctx, mgr.Keeper(), StableReserveName, 100*common.One)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)

	deploy, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, "stable-reserve-deploy:")
	c.Assert(err, IsNil)
	c.Check(deploy.TargetAsset.Equals(usdt), Equals, true)
	c.Check(deploy.Tx.Coins[0].Equals(common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))), Equals, true)
	c.Check(deploy.StreamQuantity, Equals, uint64(0))
	c.Check(deploy.StreamInterval, Equals, uint64(0))
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, StableReserveName).IsZero(), Equals, true)

	resp, err := queryServer{mgr: mgr}.queryStableReserve(ctx, &types.QueryStableReserveRequest{})
	c.Assert(err, IsNil)
	c.Check(resp.DeploymentStreamQuantity, Equals, "0")
	c.Check(resp.DeploymentStreamInterval, Equals, "0")
	c.Assert(resp.ActiveSwaps, HasLen, 1)
	c.Check(resp.ActiveSwaps[0].Purpose, Equals, "deployment")
	c.Check(resp.ActiveSwaps[0].TargetAsset, Equals, usdt.String())
}

func (s *StableReserveMgrSuite) TestRuneDeploymentSettlesFromActiveQueue(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	FundModule(c, ctx, mgr.Keeper(), StableReserveName, 40*common.One)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, false)
}

func (s *StableReserveMgrSuite) TestRuneDeploymentSpendsAvailableRuneOnce(c *C) {
	ctx, mgr, _, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	FundModule(c, ctx, mgr.Keeper(), StableReserveName, 100*common.One)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, StableReserveName).IsZero(), Equals, true)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)
	swaps := queuedSwapsWithMemoPrefix(ctx, mgr, types.StableReserveDeployMemoPrefix)
	c.Check(swaps, HasLen, 1)
}

func (s *StableReserveMgrSuite) TestStableReserveInternalSwapSettlementAddsInventory(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	stableReserveAddress, err := mgr.Keeper().GetModuleAddress(StableReserveName)
	c.Assert(err, IsNil)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 100*common.One)

	tx := common.NewTx(
		GetRandomTxHash(),
		stableReserveAddress,
		asgardAddress,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))),
		common.Gas{},
		"stable-reserve-deploy:"+usdc.String(),
	)
	msg := NewMsgSwap(tx, usdc, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, moduleAddr)

	_, emit, err := NewSwapHandler(mgr).RunWithEmit(ctx, msg)
	c.Assert(err, IsNil)
	c.Check(emit.IsZero(), Equals, false)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(emit), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveInternalRescueSettlesRuneAtCompletion(c *C) {
	// A rescue targeting RUNE holds its proceeds in Asgard during the stream (the
	// asgard solvency invariant counts pending State.Out as Asgard-custodied RUNE).
	// The accumulated RUNE is moved into the module at completion by settleSwap,
	// regardless of whether the final sub-swap succeeded — so earlier sub-swaps'
	// proceeds are never stranded. This asserts that completion transfer directly.
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveAddress, err := mgr.Keeper().GetModuleAddress(StableReserveName)
	c.Assert(err, IsNil)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	// Asgard already holds the accumulated rescue proceeds (as prior sub-swaps do).
	accumulated := cosmos.NewUint(9 * common.One)
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 9*common.One)

	tx := common.NewTx(
		GetRandomTxHash(),
		stableReserveAddress,
		asgardAddress,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(30*common.One))),
		common.Gas{},
		types.StableReserveRescueMemoPrefix+usdc.String(),
	)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	msg := NewMsgSwap(tx, common.RuneAsset(), common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 3, 1, types.SwapVersion_v1, moduleAddr)
	msg.State.Out = accumulated

	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, StableReserveName).IsZero(), Equals, true)
	c.Assert(settleSwap(ctx, mgr, *msg, "test"), IsNil)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, StableReserveName).Equal(accumulated), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveInternalSwapSettlementUsesPreSwapEligibility(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	stableReserveAddress, err := mgr.Keeper().GetModuleAddress(StableReserveName)
	c.Assert(err, IsNil)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	msg := NewMsgSwap(
		common.NewTx(GetRandomTxHash(), stableReserveAddress, asgardAddress, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))), common.Gas{}, "stable-reserve-deploy:"+usdc.String()),
		usdc, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, 0, types.SwapVersion_v1, moduleAddr,
	)

	c.Assert(NewSwapHandler(mgr).validate(ctx, *msg), IsNil)
	setStableReservePoolPriceBps(c, ctx, mgr, usdc, 8_000)
	c.Assert(NewSwapHandler(mgr).validate(ctx, *msg), ErrorMatches, "stable reserve deployment target no longer eligible: unhealthy pool price")
	settled, err := settleStableReserveInternalSwap(ctx, mgr, *msg, cosmos.NewUint(9*common.One))
	c.Assert(err, IsNil)
	c.Check(settled, Equals, true)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(cosmos.NewUint(9*common.One)), Equals, true)
}

func (s *StableReserveMgrSuite) TestStableReserveInternalSwapSettlementRejectsNonModuleSender(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	msg := NewMsgSwap(
		common.NewTx(
			GetRandomTxHash(),
			GetRandomETHAddress(),
			asgardAddress,
			common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))),
			common.Gas{},
			"stable-reserve-deploy:"+usdc.String(),
		),
		usdc,
		common.NoopAddress,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		0,
		0,
		types.SwapVersion_v1,
		moduleAddr,
	)

	settled, err := settleStableReserveInternalSwap(ctx, mgr, *msg, cosmos.NewUint(10*common.One))
	c.Assert(err, IsNil)
	c.Check(settled, Equals, false)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
}

func (s *StableReserveMgrSuite) TestDepegRescueSchedulesStreamingSwapToHealthyStable(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	creditAmount := cosmos.NewUint(25 * common.One)
	rescueAmount := cosmos.NewUint(100 * common.One)
	totalInventory := creditAmount.Add(rescueAmount)
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, creditAmount, contributor, common.BlankTxID), IsNil)
	fundStableReserveSurplus(c, ctx, mgr, usdc, rescueAmount)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdc.Ticker.String(), Price: "0.98"}), IsNil)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)

	rescue, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, "stable-reserve-rescue:")
	c.Assert(err, IsNil)
	c.Check(rescue.TargetAsset.Equals(usdt), Equals, true)
	c.Check(rescue.Tx.Coins[0].Equals(common.NewCoin(usdc.GetSecuredAsset(), totalInventory)), Equals, true)
	// Quantity 0 lets the swap manager size the stream from slip; interval 1
	// keeps it streaming one sub-swap per block.
	c.Check(rescue.StreamQuantity, Equals, uint64(0))
	c.Check(rescue.StreamInterval, Equals, uint64(1))
	c.Check(strings.Contains(rescue.Tx.Memo, "stable-reserve-rescue:"+usdc.String()), Equals, true)

	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(creditAmount), Equals, true)
}

func (s *StableReserveMgrSuite) TestDepegRescueSchedulesCreditedInventory(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	contributor := GetRandomETHAddress()
	amount := cosmos.NewUint(100 * common.One)
	c.Assert(stableReserveMgr.Deposit(ctx, usdc, amount, contributor, common.BlankTxID), IsNil)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdc.Ticker.String(), Price: "0.98"}), IsNil)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)

	rescue, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, "stable-reserve-rescue:")
	c.Assert(err, IsNil)
	c.Check(rescue.TargetAsset.Equals(usdt), Equals, true)
	c.Check(rescue.Tx.Coins[0].Equals(common.NewCoin(usdc.GetSecuredAsset(), amount)), Equals, true)
	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).Equal(amount), Equals, true)
	credit, err := mgr.Keeper().GetStableReserveCredit(ctx, usdc, contributor)
	c.Assert(err, IsNil)
	c.Check(credit.Equal(amount), Equals, true)
}

func (s *StableReserveMgrSuite) TestDepegRescueQueueScanLifecycle(c *C) {
	ctx, mgr, usdc, _ := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	amount := cosmos.NewUint(100 * common.One)
	fundStableReserveSurplus(c, ctx, mgr, usdc, amount)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdc.Ticker.String(), Price: "0.98"}), IsNil)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)

	rescue, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, types.StableReserveRescueMemoPrefix)
	c.Assert(err, IsNil)
	rescueKey := types.StableReservePendingRescueSwapKey(usdc.String())
	pending, err := stableReserveMgr.(*StableReserveMgr).pendingRescueKeys(ctx, mgr)
	c.Assert(err, IsNil)
	c.Check(pending[rescueKey], Equals, true)

	c.Assert(removeQueuedSwap(ctx, mgr, rescue), IsNil)
	pending, err = stableReserveMgr.(*StableReserveMgr).pendingRescueKeys(ctx, mgr)
	c.Assert(err, IsNil)
	c.Check(pending[rescueKey], Equals, false)
}

func (s *StableReserveMgrSuite) TestDepegRescueSchedulesRuneFallbackWhenAllStablesUnhealthy(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	amount := cosmos.NewUint(100 * common.One)
	fundStableReserveSurplus(c, ctx, mgr, usdc, amount)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdc.Ticker.String(), Price: "0.98"}), IsNil)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdt.Ticker.String(), Price: "0.98"}), IsNil)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)

	rescue, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, "stable-reserve-rescue:")
	c.Assert(err, IsNil)
	c.Check(rescue.TargetAsset.Equals(common.RuneAsset()), Equals, true)
	c.Check(rescue.Tx.Coins[0].Equals(common.NewCoin(usdc.GetSecuredAsset(), amount)), Equals, true)
	c.Check(strings.Contains(rescue.Tx.Memo, "stable-reserve-rescue:"+usdc.String()), Equals, true)
}

func (s *StableReserveMgrSuite) TestDepegRescueRuneFallbackSettlesFromActiveQueue(c *C) {
	ctx, mgr, usdc, usdt := setupStableReserveSuite(c)
	stableReserveMgr := mgr.StableReserveManager()
	amount := cosmos.NewUint(100 * common.One)
	fundStableReserveSurplus(c, ctx, mgr, usdc, amount)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdc.Ticker.String(), Price: "0.98"}), IsNil)
	c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: usdt.Ticker.String(), Price: "0.98"}), IsNil)

	c.Assert(stableReserveMgr.EndBlock(ctx, mgr), IsNil)
	rescue, err := firstQueuedSwapWithMemoPrefix(ctx, mgr, types.StableReserveRescueMemoPrefix)
	c.Assert(err, IsNil)
	c.Check(rescue.TargetAsset.Equals(common.RuneAsset()), Equals, true)

	for i := 0; i < 10; i++ {
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
		c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	}

	moduleAddr := mgr.Keeper().GetModuleAccAddress(StableReserveName)
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, usdc, moduleAddr).IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, StableReserveName).IsZero(), Equals, false)
}

func firstQueuedSwapWithMemoPrefix(ctx cosmos.Context, mgr *Mgrs, prefix string) (MsgSwap, error) {
	swaps := queuedSwapsWithMemoPrefix(ctx, mgr, prefix)
	if len(swaps) == 0 {
		return MsgSwap{}, fmt.Errorf("%s swap not queued", prefix)
	}
	return swaps[0], nil
}

func queuedSwapsWithMemoPrefix(ctx cosmos.Context, mgr *Mgrs, prefix string) []MsgSwap {
	var swaps []MsgSwap
	iter := mgr.Keeper().GetSwapQueueIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg); err != nil {
			continue
		}
		if strings.HasPrefix(msg.Tx.Memo, prefix) {
			swaps = append(swaps, msg)
		}
	}
	advIter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer advIter.Close()
	for ; advIter.Valid(); advIter.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(advIter.Value(), &msg); err != nil {
			continue
		}
		if strings.HasPrefix(msg.Tx.Memo, prefix) {
			swaps = append(swaps, msg)
		}
	}
	return swaps
}

func removeQueuedSwap(ctx cosmos.Context, mgr *Mgrs, msg MsgSwap) error {
	if mgr.Keeper().HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)) {
		return mgr.Keeper().RemoveAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index))
	}
	mgr.Keeper().RemoveSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index))
	return nil
}

func (s *StableReserveMgrSuite) TestStableReserveOracleRatioOutput(c *C) {
	in := cosmos.NewUint(100 * common.One)

	// Equal prices settle 1:1.
	out, ok := stableReserveOracleRatioOutput(in, "1.00", "1.00")
	c.Check(ok, Equals, true)
	c.Check(out.Equal(in), Equals, true)

	// Opposite band edges scale down and floor toward the reserve:
	// floor(100e8 * 0.99 / 1.01) = floor(990_000_000_000 / 101) = 9_801_980_198.
	out, ok = stableReserveOracleRatioOutput(in, "0.99", "1.01")
	c.Check(ok, Equals, true)
	c.Check(out.Equal(cosmos.NewUint(9_801_980_198)), Equals, true)
	// The output is strictly less than a flat 1:1 fill, so the reserve never
	// overpays at the band edge.
	c.Check(out.LT(in), Equals, true)

	// Source worth more than target scales the output up.
	out, ok = stableReserveOracleRatioOutput(in, "1.01", "0.99")
	c.Check(ok, Equals, true)
	c.Check(out.Equal(cosmos.NewUint(10_202_020_202)), Equals, true)

	// Unparsable or non-positive target prices are rejected, not panicked on.
	for _, tc := range []struct{ src, tgt string }{
		{"1.00", "0"},     // zero target -> no division by zero
		{"1.00", "-1.00"}, // negative target
		{"-1.00", "1.00"}, // negative source -> floored result must stay non-negative
		{"abc", "1.00"},   // unparsable source
		{"1.00", "abc"},   // unparsable target
	} {
		_, ok = stableReserveOracleRatioOutput(in, tc.src, tc.tgt)
		c.Check(ok, Equals, false, Commentf("src=%s tgt=%s", tc.src, tc.tgt))
	}
}
