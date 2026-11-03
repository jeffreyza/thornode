package thorchain

import (
	"fmt"
	"math/big"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type PoolMgrSuite struct{}

var _ = Suite(&PoolMgrSuite{})

type failNativePoolBurnKeeper struct {
	keeper.Keeper
}

func (k failNativePoolBurnKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	// Fail only the native asset evacuation path added to pool burning.
	if from == AsgardName && to == TreasuryName {
		return fmt.Errorf("fail native burn transfer")
	}
	return k.Keeper.SendFromModuleToModule(ctx, from, to, coins)
}

type failStageCostPoolSaveKeeper struct {
	keeper.Keeper
	asset common.Asset
}

func (k failStageCostPoolSaveKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	if pool.Asset.Equals(k.asset) {
		return fmt.Errorf("fail staged pool save")
	}
	return k.Keeper.SetPool(ctx, pool)
}

func (s *PoolMgrSuite) TestEnableNextPool(c *C) {
	var err error
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	// Fund Asgard module with enough RUNE for staged pool fees
	FundModule(c, ctx, k, AsgardName, 1000*common.One)
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	pool = NewPool()
	pool.Asset = common.BTCAsset // gas pool should be enabled by default
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(50 * common.One)
	pool.BalanceAsset = cosmos.NewUint(50 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	usdcAsset, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999999")
	c.Assert(err, IsNil)
	pool = NewPool()
	pool.Asset = usdcAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(40 * common.One)
	pool.BalanceAsset = cosmos.NewUint(40 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	xmrAsset, err := common.NewAsset("XMR.XMR")
	c.Assert(err, IsNil)
	pool = NewPool()
	pool.Asset = xmrAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(40 * common.One)
	pool.BalanceAsset = cosmos.NewUint(0 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// usdAsset
	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)
	pool = NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(140 * common.One)
	pool.BalanceAsset = cosmos.NewUint(0 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	poolMgr := newPoolMgr()

	// should enable BTC
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)
	pool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pool.Status, Equals, PoolAvailable)

	// should enable ETH
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)
	pool, err = k.GetPool(ctx, usdcAsset)
	c.Assert(err, IsNil)
	c.Check(pool.Status, Equals, PoolAvailable)

	// should NOT enable XMR, since it has no assets
	c.Assert(poolMgr.cyclePools(ctx, 100, 20*common.One, 10*common.One, mgr), IsNil)
	pool, err = k.GetPool(ctx, xmrAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.IsEmpty(), Equals, false)
	c.Check(pool.Status, Equals, PoolStaged)
	c.Check(pool.BalanceRune.Uint64(), Equals, uint64(40*common.One))
}

func (s *PoolMgrSuite) TestCyclePoolsSkipsDerivedPools(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	derivedAsset := common.BTCAsset.GetDerivedAsset()
	pool := NewPool()
	pool.Asset = derivedAsset
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	pool, err := k.GetPool(ctx, derivedAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.IsEmpty(), Equals, false)
	c.Check(pool.Status, Equals, PoolAvailable)
	c.Check(pool.BalanceRune.IsZero(), Equals, true)
	c.Check(pool.BalanceAsset.IsZero(), Equals, true)
}

func (s *PoolMgrSuite) TestAbandonPool(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	// Fund Asgard module with enough RUNE for staged pool fees
	FundModule(c, ctx, k, AsgardName, 1000*common.One)
	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	synthPool := NewPool()
	synthPool.Asset = usdAsset.GetSyntheticAsset()
	synthPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, synthPool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	runeAddr := GetRandomRUNEAddress()
	ethAddr := GetRandomETHAddress()
	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  runeAddr,
		AssetAddress: ethAddr,
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	poolMgr := newPoolMgr()

	// add event manager to context to intecept withdraw event
	em := cosmos.NewEventManager()
	ctx = ctx.WithEventManager(em)
	eventManager, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventManager

	// cycle pools
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 100*common.One, mgr), IsNil)

	// check withdraw event (keys must exist with empty values for midgard)
	expected := map[string]string{
		"pool":                     "ETH.TUSDB",
		"liquidity_provider_units": "0",
		"basis_points":             "10000",
		"asymmetry":                "0.000000000000000000",
		"emit_asset":               "0",
		"emit_rune":                "0",
		"id":                       "0000000000000000000000000000000000000000000000000000000000000000",
		"chain":                    "THOR",
		"from":                     runeAddr.String(),
		"to":                       "",
		"coin":                     "0 THOR.RUNE",
		"memo":                     "",
	}
	for _, e := range em.Events() {
		if e.Type == "withdraw" {
			actual := make(map[string]string)
			for _, attr := range e.Attributes {
				actual[attr.Key] = attr.Value
			}
			c.Assert(actual, DeepEquals, expected)
		}
	}

	// check pool was deleted
	pool, err = k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.IsZero(), Equals, true)
	c.Assert(pool.BalanceAsset.IsZero(), Equals, true)

	// check related synth pool record was also deleted
	synthPool, err = k.GetPool(ctx, usdAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Assert(synthPool.IsEmpty(), Equals, true)

	// check vault remove pool asset
	vault, err = k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.HasAsset(usdAsset), Equals, false)
	c.Assert(vault.CoinLength(), Equals, 1)

	// check that liquidity provider got removed
	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 0)
}

func (s *PoolMgrSuite) TestDemotePoolWithLowLiquidityFees(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	// Fund Asgard module with enough RUNE for staged pool fees
	FundModule(c, ctx, k, AsgardName, 1000*common.One)
	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset
	poolETH.Status = PoolAvailable
	poolETH.BalanceRune = cosmos.NewUint(100000 * common.One)
	poolETH.BalanceAsset = cosmos.NewUint(100000 * common.One)
	c.Assert(k.SetPool(ctx, poolETH), IsNil)

	ethUsdc, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999999")
	c.Assert(err, IsNil)
	poolUsdc := NewPool()
	poolUsdc.Asset = ethUsdc
	poolUsdc.Status = PoolAvailable
	poolUsdc.BalanceRune = cosmos.NewUint(100000 * common.One)
	poolUsdc.BalanceAsset = cosmos.NewUint(100000 * common.One)
	c.Assert(k.SetPool(ctx, poolUsdc), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	runeAddr := GetRandomRUNEAddress()
	ethAddr := GetRandomETHAddress()
	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  runeAddr,
		AssetAddress: ethAddr,
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)
	k.SetMimir(ctx, constants.MinimumPoolLiquidityFee.String(), 100000000)

	poolMgr := newPoolMgr()

	// cycle pools
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 100*common.One, mgr), IsNil)

	// check pool was deleted
	pool, err = k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.IsZero(), Equals, true)
	c.Assert(pool.BalanceAsset.IsZero(), Equals, true)

	// check vault remove pool asset
	vault, err = k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.HasAsset(usdAsset), Equals, false)
	c.Assert(vault.CoinLength(), Equals, 1)

	// check that liquidity provider got removed
	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 0)
	afterETHPool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(afterETHPool.Status == PoolAvailable, Equals, true)
	afterETHEth, err := k.GetPool(ctx, ethUsdc)
	c.Assert(err, IsNil)
	c.Assert(afterETHEth.Status == PoolStaged, Equals, true)
}

// TestBurnStagedPoolSkipsPendingLiquidity verifies that staged-pool burning
// does not delete LP state or sweep pending funds while a single-sided add is
// still pending on the pool.
func (s *PoolMgrSuite) TestBurnStagedPoolSkipsPendingLiquidity(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	pendingRune := cosmos.NewUint(55 * common.One)
	// Fund Asgard so the pre-fix bug would actually sweep pending rune to
	// Reserve instead of only failing due to insufficient funds.
	FundModule(c, ctx, k, AsgardName, pendingRune.Uint64())

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.PendingInboundRune = pendingRune
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  pendingRune,
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	reserveRuneBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceRune.IsZero(), Equals, true)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)
	c.Assert(afterPool.PendingInboundRune.Equal(pendingRune), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	reserveRuneAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reserveRuneAfter.Equal(reserveRuneBefore), Equals, true)
}

// TestAbandonNativePoolSkipsBurnOnTreasuryTransferFailure verifies that a
// failed native Asgard -> Treasury transfer aborts pool burning before pool
// deletion or LP cleanup.
func (s *PoolMgrSuite) TestAbandonNativePoolSkipsBurnOnTreasuryTransferFailure(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(failNativePoolBurnKeeper{Keeper: k})

	// Use a THOR-native pool asset so pool burning must evacuate the Asgard
	// module balance instead of only zeroing vault coin entries.
	pool := NewPool()
	pool.Asset = common.TCY
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:        common.TCY,
		RuneAddress:  GetRandomTHORAddress(),
		AssetAddress: GetRandomTHORAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	// Seed the native asset where the burn path expects to find it: in Asgard's
	// module balance for the native denom.
	coin := common.NewCoin(common.TCY, cosmos.NewUint(100*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	// Snapshot the module balances before the burn attempt. Because pool burning
	// now runs in a CacheContext, a failed native Treasury transfer should leave
	// all of these balances unchanged.
	asgardNativeBefore := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	treasuryNativeBefore := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	// The failed Asgard -> Treasury transfer should abort the burn before pool
	// deletion or LP cleanup, leaving the pool intact for retry.
	afterPool, err := k.GetPool(ctx, common.TCY)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	// LP cleanup must also be rolled back when the cached burn aborts.
	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, common.TCY)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	// The failed native asset move must not persist after the cached burn
	// attempt aborts.
	asgardBalance := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	c.Assert(asgardBalance.Equal(asgardNativeBefore), Equals, true)
	treasuryBalance := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())
	c.Assert(treasuryBalance.Equal(treasuryNativeBefore), Equals, true)
}

// TestAbandonNativePoolSkipsBurnWhenAsgardBalanceMissing verifies that pool
// burning fails closed if native module funds are missing from Asgard.
func (s *PoolMgrSuite) TestAbandonNativePoolSkipsBurnWhenAsgardBalanceMissing(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	pool := NewPool()
	pool.Asset = common.TCY
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:        common.TCY,
		RuneAddress:  GetRandomTHORAddress(),
		AssetAddress: GetRandomTHORAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, common.TCY)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, common.TCY)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	asgardBalance := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	c.Assert(asgardBalance.IsZero(), Equals, true)
	treasuryBalance := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())
	c.Assert(treasuryBalance.IsZero(), Equals, true)
}

// TestAbandonNativePoolSkipsBurnWithAdvancedQueuedNativeDepositLiability
// verifies that a staged native pool is not removed while an advanced queued
// swap still holds source-asset deposit liability in that native asset.
func (s *PoolMgrSuite) TestAbandonNativePoolSkipsBurnWithAdvancedQueuedNativeDepositLiability(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	pool := NewPool()
	pool.Asset = common.TCY
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:        common.TCY,
		RuneAddress:  GetRandomTHORAddress(),
		AssetAddress: GetRandomTHORAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	coin := common.NewCoin(common.TCY, cosmos.NewUint(115*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	txID := GetRandomTxHash()
	swapMsg := MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.THORChain,
			FromAddress: GetRandomTHORAddress(),
			Coins:       common.Coins{common.NewCoin(common.TCY, cosmos.NewUint(25*common.One))},
		},
		TargetAsset: common.RuneAsset(),
		SwapType:    MarketSwap,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 1,
			Deposit:  cosmos.NewUint(25 * common.One),
			In:       cosmos.NewUint(10 * common.One),
			Out:      cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swapMsg), IsNil)

	asgardNativeBefore := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	treasuryNativeBefore := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, common.TCY)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, common.TCY)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	asgardBalance := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	c.Assert(asgardBalance.Equal(asgardNativeBefore), Equals, true)
	treasuryBalance := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())
	c.Assert(treasuryBalance.Equal(treasuryNativeBefore), Equals, true)
}

// TestAbandonNativePoolSkipsBurnWithLegacyQueuedNativeOutput verifies that a
// staged native pool is not removed while the legacy swap queue still has
// accumulated native output awaiting settlement.
func (s *PoolMgrSuite) TestAbandonNativePoolSkipsBurnWithLegacyQueuedNativeOutput(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	pool := NewPool()
	pool.Asset = common.TCY
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:        common.TCY,
		RuneAddress:  GetRandomTHORAddress(),
		AssetAddress: GetRandomTHORAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	coin := common.NewCoin(common.TCY, cosmos.NewUint(125*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	txID := GetRandomTxHash()
	swapMsg := MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.THORChain,
			FromAddress: GetRandomTHORAddress(),
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(25*common.One))},
		},
		TargetAsset:    common.TCY,
		StreamInterval: 10,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swapMsg, 0), IsNil)

	streamingSwap := NewStreamingSwap(txID, 5, 10, cosmos.ZeroUint(), cosmos.NewUint(25*common.One))
	streamingSwap.In = cosmos.NewUint(25 * common.One)
	streamingSwap.Out = cosmos.NewUint(25 * common.One)
	k.SetStreamingSwap(ctx, streamingSwap)

	asgardNativeBefore := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	treasuryNativeBefore := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, common.TCY)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, common.TCY)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	asgardBalance := k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native())
	c.Assert(asgardBalance.Equal(asgardNativeBefore), Equals, true)
	treasuryBalance := k.GetBalanceOfModule(ctx, TreasuryName, common.TCY.Native())
	c.Assert(treasuryBalance.Equal(treasuryNativeBefore), Equals, true)
}

// TestAbandonPoolSkipsBurnWithPendingOutboundLiability verifies that a staged
// vault-backed pool is not removed while an unsent txout still depends on its
// asset balance.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithPendingOutboundLiability(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	txOutHeight := ctx.BlockHeight() + 1
	if txOutHeight < 1 {
		txOutHeight = 1
	}
	pendingOutbound := TxOutItem{
		Chain:       usdAsset.GetChain(),
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(usdAsset, cosmos.NewUint(25*common.One)),
		InHash:      GetRandomTxHash(),
		Memo:        "OUT:pending",
	}
	c.Assert(k.AppendTxOut(ctx, txOutHeight, pendingOutbound), IsNil)
	c.Assert(len(k.GetPendingOutbounds(ctx, usdAsset)), Equals, 1)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)
	c.Assert(len(k.GetPendingOutbounds(ctx, usdAsset)), Equals, 1)
}

// TestAbandonPoolSkipsBurnWithRelatedAssetSynthSupply verifies that a staged L1
// pool is not removed while related synth supply still exists.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithRelatedAssetSynthSupply(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	synthAsset := usdAsset.GetSyntheticAsset()
	synthCoin := common.NewCoin(synthAsset, cosmos.NewUint(25*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoin)), IsNil)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)
	c.Assert(k.GetTotalSupply(ctx, synthAsset).Equal(synthCoin.Amount), Equals, true)
}

// TestAbandonPoolSkipsBurnWithRelatedAssetSaverLP verifies that a staged L1 pool
// is not removed while saver LP state still exists on the related synth pool.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithRelatedAssetSaverLP(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	synthAsset := usdAsset.GetSyntheticAsset()
	synthPool := NewPool()
	synthPool.Asset = synthAsset
	synthPool.Status = PoolAvailable
	synthPool.BalanceAsset = cosmos.NewUint(25 * common.One)
	synthPool.LPUnits = cosmos.NewUint(25 * common.One)
	c.Assert(k.SetPool(ctx, synthPool), IsNil)

	saverLP := LiquidityProvider{
		Asset:        synthAsset,
		RuneAddress:  common.NoAddress,
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.NewUint(25 * common.One),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, saverLP)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	saverCount := 0
	saverIterator := k.GetLiquidityProviderIterator(ctx, synthAsset)
	defer saverIterator.Close()
	for ; saverIterator.Valid(); saverIterator.Next() {
		saverCount++
	}
	c.Assert(saverCount, Equals, 1)
}

// TestAbandonPoolSkipsBurnWithRelatedAssetTradeUnits verifies that a staged L1
// pool is not removed while trade-account depth still depends on the same
// underlying asset backing.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithRelatedAssetTradeUnits(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	tradeAsset := usdAsset.GetTradeAsset()
	tradeUnit := types.NewTradeUnit(tradeAsset)
	tradeUnit.Units = cosmos.NewUint(25 * common.One)
	tradeUnit.Depth = cosmos.NewUint(25 * common.One)
	k.SetTradeUnit(ctx, tradeUnit)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	afterTradeUnit, err := k.GetTradeUnit(ctx, tradeAsset)
	c.Assert(err, IsNil)
	c.Assert(afterTradeUnit.Depth.Equal(tradeUnit.Depth), Equals, true)
	c.Assert(afterTradeUnit.Units.Equal(tradeUnit.Units), Equals, true)
}

// TestAbandonPoolSkipsBurnWithQueuedTradeAssetOutput verifies that a staged L1
// pool is not removed while a queued swap still has already-accounted trade
// asset output awaiting settlement.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithQueuedTradeAssetOutput(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	tradeAsset := usdAsset.GetTradeAsset()
	swapMsg := MsgSwap{
		Tx: common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.THORChain,
			FromAddress: GetRandomTHORAddress(),
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(25*common.One))},
		},
		TargetAsset: tradeAsset,
		SwapType:    MarketSwap,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 1,
			Deposit:  cosmos.NewUint(25 * common.One),
			In:       cosmos.NewUint(25 * common.One),
			Out:      cosmos.NewUint(10 * common.One),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swapMsg), IsNil)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)
}

// TestAbandonPoolSkipsBurnWithPendingTradeAssetOutbound verifies that a staged
// L1 pool is not removed while a scheduled but unsent trade-asset outbound
// still depends on the underlying pool backing.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithPendingTradeAssetOutbound(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	txOutHeight := ctx.BlockHeight() + 1
	if txOutHeight < 1 {
		txOutHeight = 1
	}
	pendingOutbound := TxOutItem{
		Chain:       usdAsset.GetChain(),
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(usdAsset.GetTradeAsset(), cosmos.NewUint(25*common.One)),
		InHash:      GetRandomTxHash(),
		Memo:        "OUT:pending-trade",
	}
	c.Assert(k.AppendTxOut(ctx, txOutHeight, pendingOutbound), IsNil)
	c.Assert(len(k.GetPendingOutbounds(ctx, usdAsset.GetTradeAsset())), Equals, 1)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)
	c.Assert(len(k.GetPendingOutbounds(ctx, usdAsset.GetTradeAsset())), Equals, 1)
}

// TestAbandonPoolSkipsBurnWithRelatedAssetSecuredDepth verifies that a staged L1
// pool is not removed while secured-asset depth or supply still depends on the
// same underlying asset backing.
func (s *PoolMgrSuite) TestAbandonPoolSkipsBurnWithRelatedAssetSecuredDepth(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	securedAsset := usdAsset.GetSecuredAsset()
	securedCoin := common.NewCoin(securedAsset, cosmos.NewUint(25*common.One))
	c.Assert(k.MintAndSendToAccount(ctx, GetRandomBech32Addr(), securedCoin), IsNil)

	securedState := types.NewSecuredAsset(securedAsset)
	securedState.Depth = cosmos.NewUint(25 * common.One)
	k.SetSecuredAsset(ctx, securedState)

	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 1, 0, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	afterSecuredState, err := k.GetSecuredAsset(ctx, securedAsset)
	c.Assert(err, IsNil)
	c.Assert(afterSecuredState.Depth.Equal(securedState.Depth), Equals, true)
	c.Assert(k.GetTotalSupply(ctx, securedAsset).Equal(securedCoin.Amount), Equals, true)
}

// TestStageCostSaveFailureDoesNotCommitReserveOrBurnPool verifies that staged
// pool fees are only committed when the pool update also commits.
func (s *PoolMgrSuite) TestStageCostSaveFailureDoesNotCommitReserveOrBurnPool(c *C) {
	ctx, k := setupKeeperForTest(c)

	usdAsset, err := common.NewAsset("ETH.TUSDB")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = usdAsset
	pool.Status = PoolStaged
	pool.BalanceRune = cosmos.NewUint(10 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(usdAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	lp := LiquidityProvider{
		Asset:        usdAsset,
		RuneAddress:  GetRandomRUNEAddress(),
		AssetAddress: GetRandomETHAddress(),
		Units:        cosmos.ZeroUint(),
		PendingRune:  cosmos.ZeroUint(),
		PendingAsset: cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	FundModule(c, ctx, k, AsgardName, 10*common.One)
	reserveRuneBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	mgr := NewDummyMgrWithKeeper(failStageCostPoolSaveKeeper{Keeper: k, asset: usdAsset})
	poolMgr := newPoolMgr()
	c.Assert(poolMgr.cyclePools(ctx, 100, 20*common.One, 10*common.One, mgr), IsNil)

	afterPool, err := k.GetPool(ctx, usdAsset)
	c.Assert(err, IsNil)
	c.Assert(afterPool.IsEmpty(), Equals, false)
	c.Assert(afterPool.Status, Equals, PoolStaged)
	c.Assert(afterPool.BalanceRune.Equal(pool.BalanceRune), Equals, true)
	c.Assert(afterPool.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)

	afterVault, err := k.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.HasAsset(usdAsset), Equals, true)

	count := 0
	iterator := k.GetLiquidityProviderIterator(ctx, usdAsset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		count++
	}
	c.Assert(count, Equals, 1)

	reserveRuneAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reserveRuneAfter.Equal(reserveRuneBefore), Equals, true)
}

func (s *PoolMgrSuite) TestPoolMeetTradingVolumeCriteria(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	asset := common.BTCAsset

	pool := Pool{
		Asset:        asset,
		BalanceAsset: cosmos.NewUint(1000 * common.One),
		BalanceRune:  cosmos.NewUint(1000 * common.One),
	}

	minFee := cosmos.ZeroUint()
	meets := pm.poolMeetTradingVolumeCriteria(ctx, mgr, pool, minFee)
	c.Assert(meets, Equals, true,
		Commentf("all pools should meet criteria if min fees is zero"))

	minFee = cosmos.NewUint(10 * common.One)
	meets = pm.poolMeetTradingVolumeCriteria(ctx, mgr, pool, minFee)
	c.Assert(meets, Equals, false,
		Commentf("pool with no fees collected should not meet criteria"))

	err := k.AddToLiquidityFees(ctx, pool.Asset, minFee.Add(cosmos.NewUint(1)))
	c.Assert(err, IsNil)
	cur, err := k.GetRollingPoolLiquidityFee(ctx, pool.Asset)
	c.Assert(err, IsNil)
	c.Assert(cur, Equals, minFee.Add(cosmos.NewUint(1)).Uint64())

	meets = pm.poolMeetTradingVolumeCriteria(ctx, mgr, pool, minFee)
	c.Assert(meets, Equals, true,
		Commentf("pool should meet min fee criteria"))
}

func (s *PoolMgrSuite) TestRemoveAssetFromVault(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	asset := common.BTCAsset

	v0 := GetRandomVault()
	v0.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(k.SetVault(ctx, v0), IsNil)
	c.Assert(v0.HasAsset(asset), Equals, false,
		Commentf("vault0 should not have asset balance"))

	v1 := GetRandomVault()
	v1.Coins = common.Coins{
		common.NewCoin(asset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
	}
	c.Assert(k.SetVault(ctx, v1), IsNil)
	c.Assert(v1.HasAsset(asset), Equals, true,
		Commentf("vault1 should have asset balance"))

	c.Assert(pm.removeAssetFromVault(ctx, asset, mgr), IsNil)

	v0, err := k.GetVault(ctx, v0.PubKey)
	c.Assert(err, IsNil)
	c.Assert(v0.HasAsset(asset), Equals, false,
		Commentf("vault0 should still not have asset balance"))

	v1, err = k.GetVault(ctx, v1.PubKey)
	c.Assert(err, IsNil)
	c.Assert(v1.HasAsset(asset), Equals, false,
		Commentf("vault1 should no longer have asset"))
}

func (s *PoolMgrSuite) TestRemoveNativeAssetFromVault(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	asset := common.TCY
	largeAmount := cosmos.NewUintFromBigInt(new(big.Int).Lsh(big.NewInt(1), 64))
	coin := common.NewCoin(asset, largeAmount)

	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	c.Assert(pm.removeAssetFromVault(ctx, asset, mgr), IsNil)

	asgardBalance := k.GetBalanceOfModule(ctx, AsgardName, asset.Native())
	c.Assert(asgardBalance.IsZero(), Equals, true)

	treasuryBalance := k.GetBalanceOfModule(ctx, TreasuryName, asset.Native())
	c.Assert(treasuryBalance.Equal(largeAmount), Equals, true)
}

func (s *PoolMgrSuite) TestRemoveNativeAssetFromVaultMissingBalance(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	asset := common.TCY

	err := pm.removeAssetFromVault(ctx, asset, mgr)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "native asset THOR.TCY missing from asgard during pool burn")

	asgardBalance := k.GetBalanceOfModule(ctx, AsgardName, asset.Native())
	c.Assert(asgardBalance.IsZero(), Equals, true)

	treasuryBalance := k.GetBalanceOfModule(ctx, TreasuryName, asset.Native())
	c.Assert(treasuryBalance.IsZero(), Equals, true)
}

func (s *PoolMgrSuite) TestRemoveLiquidityProviders(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	pm := newPoolMgr()

	countLiquidityProviders := func(ctx cosmos.Context, k keeper.Keeper, asset common.Asset) int {
		count := 0
		iterator := k.GetLiquidityProviderIterator(ctx, asset)
		defer iterator.Close()
		for ; iterator.Valid(); iterator.Next() {
			count++
		}
		return count
	}

	asset := common.BTCAsset

	c.Assert(countLiquidityProviders(ctx, k, asset), Equals, 0,
		Commentf("should not have lps before adding"))

	k.SetLiquidityProvider(ctx, LiquidityProvider{
		Asset:       asset,
		RuneAddress: GetRandomRUNEAddress(),
	})
	k.SetLiquidityProvider(ctx, LiquidityProvider{
		Asset:       asset,
		RuneAddress: GetRandomRUNEAddress(),
	})
	c.Assert(countLiquidityProviders(ctx, k, asset), Equals, 2,
		Commentf("should have 2 lps after adding"))

	c.Assert(pm.removeLiquidityProviders(ctx, asset, mgr), IsNil)

	c.Assert(countLiquidityProviders(ctx, k, asset), Equals, 0,
		Commentf("should have 0 lps after removing"))
}

func (s *PoolMgrSuite) TestCommitPendingLiquidity(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	pendingLiquidityAgeLimit := mgr.Keeper().GetConfigInt64(ctx, constants.PendingLiquidityAgeLimit)
	ctx = ctx.WithBlockHeight(pendingLiquidityAgeLimit + 10)

	pm := newPoolMgr()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	asset := common.BTCAsset

	pool := NewPool()
	pool.Asset = asset
	pool.BalanceRune = cosmos.NewUint(100_000)
	pool.BalanceAsset = cosmos.NewUint(100_000)
	// PendingInboundRune/Asset must equal the sum of LP pending amounts that will be committed
	// lp1 has PendingRune=501 and will be committed (old LastAddHeight)
	// lp2 has PendingAsset=600 and will be committed (old LastAddHeight)
	// lp3 has PendingAsset=811 but will NOT be committed (recent LastAddHeight)
	pool.PendingInboundRune = cosmos.NewUint(501)
	pool.PendingInboundAsset = cosmos.NewUint(600 + 811) // 600 from lp2 (to be committed) + 811 from lp3 (not to be committed)
	pool.LPUnits = cosmos.NewUint(1000)
	c.Check(mgr.Keeper().SetPool(ctx, pool), IsNil)

	lp1 := LiquidityProvider{
		Asset:         pool.Asset,
		RuneAddress:   GetRandomRUNEAddress(),
		AssetAddress:  GetRandomBTCAddress(),
		PendingRune:   cosmos.NewUint(501),
		Units:         cosmos.NewUint(300),
		LastAddHeight: 1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp1)

	lp2 := LiquidityProvider{
		Asset:         pool.Asset,
		RuneAddress:   GetRandomRUNEAddress(),
		AssetAddress:  GetRandomBTCAddress(),
		PendingAsset:  cosmos.NewUint(600),
		Units:         cosmos.NewUint(500),
		LastAddHeight: 1,
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp2)

	lp3 := LiquidityProvider{
		Asset:         pool.Asset,
		RuneAddress:   GetRandomRUNEAddress(),
		AssetAddress:  GetRandomBTCAddress(),
		PendingAsset:  cosmos.NewUint(811),
		Units:         cosmos.NewUint(200),
		LastAddHeight: ctx.BlockHeight(),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp3)

	err = pm.commitPendingLiquidity(ctx, pool, mgr)
	c.Assert(err, IsNil)

	pool, err = mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceRune.Uint64(), Equals, uint64(100_501), Commentf("%d", pool.BalanceRune.Uint64()))
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(100_600), Commentf("%d", pool.BalanceAsset.Uint64()))

	lp, err := mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, lp1.GetAddress())
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(302), Commentf("%d", lp.Units.Uint64()))
	c.Check(lp.PendingRune.Uint64(), Equals, uint64(0), Commentf("%d", lp.PendingRune.Uint64()))
	c.Check(lp.PendingAsset.Uint64(), Equals, uint64(0), Commentf("%d", lp.PendingAsset.Uint64()))

	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, lp2.GetAddress())
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(502), Commentf("%d", lp.Units.Uint64()))
	c.Check(lp.PendingRune.Uint64(), Equals, uint64(0), Commentf("%d", lp.PendingRune.Uint64()))
	c.Check(lp.PendingAsset.Uint64(), Equals, uint64(0), Commentf("%d", lp.PendingAsset.Uint64()))

	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, lp3.GetAddress())
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(200), Commentf("%d", lp.Units.Uint64()))
	c.Check(lp.PendingRune.Uint64(), Equals, uint64(0), Commentf("%d", lp.PendingRune.Uint64()))
	c.Check(lp.PendingAsset.Uint64(), Equals, uint64(811), Commentf("%d", lp.PendingAsset.Uint64()))
}

func (s *PoolMgrSuite) TestCleanupPendingLiquidityDueAndTVLCap(c *C) {
	type tc struct {
		name      string
		due       bool
		tvlCapped bool
		committed bool
	}
	for _, t := range []tc{
		{"due, under the TVL cap", true, false, true},
		{"due, at the TVL cap", true, true, false},
		{"not due, under the TVL cap", false, false, false},
		{"not due, at the TVL cap", false, true, false},
	} {
		ctx, mgr := setupManagerForTest(c)
		ageLimit := mgr.Keeper().GetConfigInt64(ctx, constants.PendingLiquidityAgeLimit)
		c.Assert(ageLimit > 1, Equals, true)
		// one pool with pending liquidity: it is due when height % ageLimit == 0
		height := 3 * ageLimit
		if !t.due {
			height++
		}
		ctx = ctx.WithBlockHeight(height)
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(1)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(100_000)
		pool.BalanceAsset = cosmos.NewUint(100_000)
		pool.PendingInboundRune = cosmos.NewUint(501)
		pool.LPUnits = cosmos.NewUint(1000)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		lp := LiquidityProvider{
			Asset:         pool.Asset,
			RuneAddress:   GetRandomRUNEAddress(),
			AssetAddress:  GetRandomBTCAddress(),
			PendingRune:   cosmos.NewUint(501),
			Units:         cosmos.NewUint(300),
			LastAddHeight: 1,
		}
		mgr.Keeper().SetLiquidityProvider(ctx, lp)

		if t.tvlCapped {
			// a 1-unit bond secures a vault holding 10k BTC units
			mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
				constants.StrictBondLiquidityRatio: true,
			}, nil)
			vault := GetRandomVault()
			vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10_000))}
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		}
		c.Assert(atTVLCap(ctx, nil, mgr), Equals, t.tvlCapped, Commentf(t.name))

		newPoolMgr().cleanupPendingLiquidity(ctx, mgr)

		got, err := mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, lp.GetAddress())
		c.Assert(err, IsNil)
		if t.committed {
			c.Check(got.PendingRune.IsZero(), Equals, true, Commentf(t.name))
			c.Check(got.Units.Uint64() > 300, Equals, true, Commentf(t.name))
		} else {
			c.Check(got.PendingRune.Uint64(), Equals, uint64(501), Commentf(t.name))
			c.Check(got.Units.Uint64(), Equals, uint64(300), Commentf(t.name))
		}
	}
}
