package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type QuerierLimitSwapsSuite struct{}

var _ = Suite(&QuerierLimitSwapsSuite{})

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryRuneSourceAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Enable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup TOR pool for USD pricing
	torAsset, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	torPool := NewPool()
	torPool.Asset = torAsset
	torPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	torPool.BalanceAsset = cosmos.NewUint(500 * common.One) // 1 RUNE = $2 USD
	torPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, torPool), IsNil)

	// Setup BTC pool for target
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Create a limit swap with RUNE as source asset
	txID := GetRandomTxHash()
	swap := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			FromAddress: GetRandomTHORAddress(),
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000), // 0.5 BTC
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(100 * common.One),
			In:      cosmos.NewUint(30 * common.One), // 30 RUNE swapped
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// Verify counts
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(1))

	// Verify USD value calculation for RUNE source
	// Remaining amount: 100 - 30 = 70 RUNE
	// USD value: 70 RUNE * $2 = $140
	expectedUSD := cosmos.NewUint(140 * common.One)
	actualUSD := cosmos.NewUintFromString(resp.TotalValueUsd)
	c.Assert(actualUSD.String(), Equals, expectedUSD.String())

	// Verify asset pair
	c.Assert(len(resp.AssetPairs), Equals, 1)
	c.Assert(resp.AssetPairs[0].SourceAsset, Equals, common.RuneAsset().String())
	c.Assert(resp.AssetPairs[0].TargetAsset, Equals, common.BTCAsset.String())
	c.Assert(resp.AssetPairs[0].Count, Equals, uint64(1))
	c.Assert(resp.AssetPairs[0].TotalValueUsd, Equals, expectedUSD.String())
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryNonRuneSourceAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Enable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup TOR pool for USD pricing
	torAsset, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	torPool := NewPool()
	torPool.Asset = torAsset
	torPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	torPool.BalanceAsset = cosmos.NewUint(500 * common.One) // 1 RUNE = $2 USD
	torPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, torPool), IsNil)

	// Setup ETH pool for source
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One) // 1 ETH = 10 RUNE
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	// Setup BTC pool for target
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Create a limit swap with ETH as source asset
	txID := GetRandomTxHash()
	swap := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000), // 0.5 BTC
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(10 * common.One),
			In:      cosmos.NewUint(3 * common.One), // 3 ETH swapped
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// Verify counts
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(1))

	// Verify USD value calculation for non-RUNE source
	// Remaining amount: 10 - 3 = 7 ETH
	// RUNE value: 7 ETH * 10 RUNE/ETH = 70 RUNE
	// USD value: 70 RUNE * $2 = $140
	expectedUSD := cosmos.NewUint(140 * common.One)
	actualUSD := cosmos.NewUintFromString(resp.TotalValueUsd)
	c.Assert(actualUSD.String(), Equals, expectedUSD.String())

	// Verify asset pair
	c.Assert(len(resp.AssetPairs), Equals, 1)
	c.Assert(resp.AssetPairs[0].SourceAsset, Equals, common.ETHAsset.String())
	c.Assert(resp.AssetPairs[0].TargetAsset, Equals, common.BTCAsset.String())
	c.Assert(resp.AssetPairs[0].Count, Equals, uint64(1))
	c.Assert(resp.AssetPairs[0].TotalValueUsd, Equals, expectedUSD.String())
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryRemainingAmountCalculation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Enable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup TOR pool for USD pricing
	torAsset, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	torPool := NewPool()
	torPool.Asset = torAsset
	torPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	torPool.BalanceAsset = cosmos.NewUint(1000 * common.One) // 1 RUNE = $1 USD
	torPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, torPool), IsNil)

	// Setup ETH pool
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One) // 1 ETH = 10 RUNE
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Test case 1: Partially executed swap
	txID1 := GetRandomTxHash()
	swap1 := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID1,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(10 * common.One),
			In:      cosmos.NewUint(4 * common.One), // 4 ETH swapped
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap1), IsNil)

	// Test case 2: Fully unexecuted swap
	txID2 := GetRandomTxHash()
	swap2 := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID2,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(25000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(5 * common.One),
			In:      cosmos.ZeroUint(), // Nothing swapped yet
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap2), IsNil)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// Verify counts
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(2))

	// Verify USD value calculation
	// Swap 1: Remaining = 10 - 4 = 6 ETH -> 60 RUNE -> $60 USD
	// Swap 2: Remaining = 5 - 0 = 5 ETH -> 50 RUNE -> $50 USD
	// Total: $110 USD
	expectedUSD := cosmos.NewUint(110 * common.One)
	actualUSD := cosmos.NewUintFromString(resp.TotalValueUsd)
	c.Assert(actualUSD.String(), Equals, expectedUSD.String())
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryMultipleAssetPairs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Enable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup TOR pool for USD pricing
	torAsset, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	torPool := NewPool()
	torPool.Asset = torAsset
	torPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	torPool.BalanceAsset = cosmos.NewUint(1000 * common.One) // 1 RUNE = $1 USD
	torPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, torPool), IsNil)

	// Setup pools
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One) // 1 ETH = 10 RUNE
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Create swap 1: ETH -> BTC
	txID1 := GetRandomTxHash()
	swap1 := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID1,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(10 * common.One),
			In:      cosmos.ZeroUint(),
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap1), IsNil)

	// Create swap 2: ETH -> BTC (same pair)
	txID2 := GetRandomTxHash()
	swap2 := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID2,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(25000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(5 * common.One),
			In:      cosmos.ZeroUint(),
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap2), IsNil)

	// Create swap 3: BTC -> ETH (different pair)
	txID3 := GetRandomTxHash()
	swap3 := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID3,
			FromAddress: GetRandomBTCAddress(),
			Coins:       common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One))},
		},
		TargetAsset:        common.ETHAsset,
		TradeTarget:        cosmos.NewUint(20 * common.One),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(2 * common.One),
			In:      cosmos.ZeroUint(),
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap3), IsNil)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// Verify counts
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(3))

	// Verify asset pairs
	c.Assert(len(resp.AssetPairs), Equals, 2)

	// ETH -> BTC pair should have 2 swaps
	// 10 ETH = 100 RUNE = $100
	// 5 ETH = 50 RUNE = $50
	// Total: $150
	ethBtcPair := resp.AssetPairs[0]
	if ethBtcPair.SourceAsset != common.ETHAsset.String() {
		ethBtcPair = resp.AssetPairs[1]
	}
	c.Assert(ethBtcPair.SourceAsset, Equals, common.ETHAsset.String())
	c.Assert(ethBtcPair.TargetAsset, Equals, common.BTCAsset.String())
	c.Assert(ethBtcPair.Count, Equals, uint64(2))
	expectedETHBTCUSD := cosmos.NewUint(150 * common.One)
	c.Assert(ethBtcPair.TotalValueUsd, Equals, expectedETHBTCUSD.String())

	// BTC -> ETH pair should have 1 swap
	// 2 BTC = 400 RUNE = $400
	btcEthPair := resp.AssetPairs[1]
	if btcEthPair.SourceAsset != common.BTCAsset.String() {
		btcEthPair = resp.AssetPairs[0]
	}
	c.Assert(btcEthPair.SourceAsset, Equals, common.BTCAsset.String())
	c.Assert(btcEthPair.TargetAsset, Equals, common.ETHAsset.String())
	c.Assert(btcEthPair.Count, Equals, uint64(1))
	expectedBTCETHUSD := cosmos.NewUint(400 * common.One)
	c.Assert(btcEthPair.TotalValueUsd, Equals, expectedBTCETHUSD.String())

	// Total USD should be sum of both pairs
	// $150 + $400 = $550
	expectedTotalUSD := cosmos.NewUint(550 * common.One)
	actualTotalUSD := cosmos.NewUintFromString(resp.TotalValueUsd)
	c.Assert(actualTotalUSD.String(), Equals, expectedTotalUSD.String())
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryWithUnavailablePool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Enable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup TOR pool for USD pricing
	torAsset, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	torPool := NewPool()
	torPool.Asset = torAsset
	torPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	torPool.BalanceAsset = cosmos.NewUint(1000 * common.One) // 1 RUNE = $1 USD
	torPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, torPool), IsNil)

	// Setup ETH pool but mark it as staged (unavailable)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.Status = PoolStaged // Unavailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Create a limit swap with ETH (unavailable pool)
	txID := GetRandomTxHash()
	swap := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(10 * common.One),
			In:      cosmos.ZeroUint(),
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// The swap is counted but since pool is unavailable, it skips USD calculation
	// and asset pair tracking (continues before reaching that code)
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(1))
	c.Assert(resp.TotalValueUsd, Equals, "0")
	c.Assert(len(resp.AssetPairs), Equals, 0)
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryWithZeroRemainingAmount(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Enable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup TOR pool for USD pricing
	torAsset, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	torPool := NewPool()
	torPool.Asset = torAsset
	torPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	torPool.BalanceAsset = cosmos.NewUint(1000 * common.One) // 1 RUNE = $1 USD
	torPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, torPool), IsNil)

	// Setup ETH pool
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One) // 1 ETH = 10 RUNE
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	// Create a limit swap that has been fully executed (Deposit == In)
	txID := GetRandomTxHash()
	swap := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 10,
		State: &types.SwapState{
			Deposit: cosmos.NewUint(10 * common.One),
			In:      cosmos.NewUint(10 * common.One), // Fully executed
			Out:     cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// The swap should be counted, but have zero USD value
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(1))
	c.Assert(resp.TotalValueUsd, Equals, "0")
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsSummaryDisabledQueue(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Disable advanced swap queue
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	// Query summary
	ctx = ctx.WithBlockHeight(100)
	resp, err := qs.queryLimitSwapsSummary(ctx, &types.QueryLimitSwapsSummaryRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// Should return empty response
	c.Assert(resp.TotalLimitSwaps, Equals, uint64(0))
	c.Assert(resp.TotalValueUsd, Equals, "0")
	c.Assert(len(resp.AssetPairs), Equals, 0)
	c.Assert(resp.OldestSwapBlocks, Equals, int64(0))
	c.Assert(resp.AverageAgeBlocks, Equals, int64(0))
}

func (s *QuerierLimitSwapsSuite) TestQueryLimitSwapsUsesCustomTTLForExpiry(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	k.SetMimir(ctx, constants.StreamingLimitSwapMaxAge.String(), 1000)

	txID := GetRandomTxHash()
	swap := types.MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			FromAddress: GetRandomETHAddress(),
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset:        common.BTCAsset,
		TradeTarget:        cosmos.NewUint(50000000),
		SwapType:           types.SwapType_limit,
		InitialBlockHeight: 100,
		State: &types.SwapState{
			Deposit:  cosmos.NewUint(10 * common.One),
			In:       cosmos.ZeroUint(),
			Out:      cosmos.ZeroUint(),
			Interval: 50, // custom TTL
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	ctx = ctx.WithBlockHeight(120)
	resp, err := qs.queryLimitSwaps(ctx, &types.QueryLimitSwapsRequest{Limit: 10})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Assert(len(resp.LimitSwaps), Equals, 1)

	// custom TTL is 50 blocks, age is 20 blocks => 30 blocks remaining
	c.Assert(resp.LimitSwaps[0].TimeToExpiryBlocks, Equals, int64(30))
}
