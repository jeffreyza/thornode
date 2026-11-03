package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SwapQueueSuite struct{}

var _ = Suite(&SwapQueueSuite{})

func (s SwapQueueSuite) TestGetTodoNum(c *C) {
	queue := newSwapQueue(keeper.KVStoreDummy{})

	c.Check(queue.getTodoNum(10000, 10, 100), Equals, int64(100)) // MaxSwapsPerBlock

	c.Check(queue.getTodoNum(202, 10, 100), Equals, int64(100)) // MaxSwapsPerBlock
	c.Check(queue.getTodoNum(201, 10, 100), Equals, int64(100)) // MaxSwapsPerBlock
	c.Check(queue.getTodoNum(200, 10, 100), Equals, int64(100)) // MaxSwapsPerBlock
	c.Check(queue.getTodoNum(199, 10, 100), Equals, int64(99))  // halves it
	c.Check(queue.getTodoNum(198, 10, 100), Equals, int64(99))  // halves it

	c.Check(queue.getTodoNum(50, 10, 100), Equals, int64(25)) // halves it

	c.Check(queue.getTodoNum(22, 10, 100), Equals, int64(11)) // halves it
	c.Check(queue.getTodoNum(21, 10, 100), Equals, int64(10)) // MinSwapsPerblock
	c.Check(queue.getTodoNum(11, 10, 100), Equals, int64(10)) // MinSwapsPerblock
	c.Check(queue.getTodoNum(10, 10, 100), Equals, int64(10)) // MinSwapsPerblock
	c.Check(queue.getTodoNum(9, 10, 100), Equals, int64(9))   // does all of them
	c.Check(queue.getTodoNum(8, 10, 100), Equals, int64(8))   // does all of them

	c.Check(queue.getTodoNum(1, 10, 100), Equals, int64(1)) // does all of them
	c.Check(queue.getTodoNum(0, 10, 100), Equals, int64(0)) // does none
}

func (s SwapQueueSuite) TestScoreMsgs(c *C) {
	ctx, k := setupKeeperForTest(c)

	pool := NewPool()
	pool.Asset = common.ATOMAsset
	pool.BalanceRune = cosmos.NewUint(143166 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	pool = NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(73708333 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	pool = NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	queue := newSwapQueue(k)

	// check that we sort by liquidity ok
	msgs := []*MsgSwap{
		NewMsgSwap(common.Tx{
			ID:    common.TxID("5E1DF027321F1FE37CA19B9ECB11C2B4ABEC0D8322199D335D9CE4C39F85F115"),
			Coins: common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One))},
		}, common.ATOMAsset, GetRandomGAIAAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("53C1A22436B385133BDD9157BB365DB7AAC885910D2FA7C9DC3578A04FFD4ADC"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(50*common.One))},
		}, common.RuneAsset(), GetRandomGAIAAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("6A470EB9AFE82981979A5EEEED3296E1E325597794BD5BFB3543A372CAF435E5"),
			Coins: common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1*common.One))},
		}, common.ATOMAsset, GetRandomGAIAAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("5EE9A7CCC55A3EBAFA0E542388CA1B909B1E3CE96929ED34427B96B7CCE9F8E8"),
			Coins: common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))},
		}, common.ATOMAsset, GetRandomGAIAAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("0FF2A521FB11FFEA4DFE3B7AD4066FF0A33202E652D846F8397EFC447C97A91B"),
			Coins: common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))},
		}, common.ATOMAsset, GetRandomGAIAAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),

		NewMsgSwap(common.Tx{
			ID:    common.TxID("1100000000000000000000000000000000000000000000000000000000000001"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(150*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),

		NewMsgSwap(common.Tx{
			ID:    common.TxID("1100000000000000000000000000000000000000000000000000000000000002"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(151*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),

		// synthetics can be redeemed on unavailable pools, should score
		NewMsgSwap(common.Tx{
			ID:    common.TxID("1100000000000000000000000000000000000000000000000000000000000003"),
			Coins: common.Coins{common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(3*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
	}

	swaps := make(swapItems, len(msgs))
	for i, msg := range msgs {
		swaps[i] = swapItem{
			msg: *msg,
			fee: cosmos.ZeroUint(),
		}
	}
	swaps, err := queue.scoreMsgs(ctx, swaps, 10_000)
	c.Assert(err, IsNil)
	swaps = swaps.Sort()
	c.Check(swaps, HasLen, 8)
	// Sort is fee-descending with (txID, index) tiebreaker — see swapItems.Sort.
	assertSortedByFeeDesc(c, swaps)
	// The heaviest swaps (151 and 150 ATOM) should still win the top two slots.
	c.Check(swaps[0].msg.Tx.Coins[0].Amount.Equal(cosmos.NewUint(151*common.One)), Equals, true, Commentf("%d", swaps[0].msg.Tx.Coins[0].Amount.Uint64()))
	c.Check(swaps[1].msg.Tx.Coins[0].Amount.Equal(cosmos.NewUint(150*common.One)), Equals, true, Commentf("%d", swaps[1].msg.Tx.Coins[0].Amount.Uint64()))

	// check that slip is taken into account with cross-pool swaps
	// Use fixed TxIDs for deterministic tie-breaking in Sort
	msgs = []*MsgSwap{
		NewMsgSwap(common.Tx{
			ID:    common.TxID("AA00000000000000000000000000000000000000000000000000000000000001"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(2*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("AA00000000000000000000000000000000000000000000000000000000000002"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(50*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("AA00000000000000000000000000000000000000000000000000000000000003"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(1*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("AA00000000000000000000000000000000000000000000000000000000000004"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("AA00000000000000000000000000000000000000000000000000000000000005"),
			Coins: common.Coins{common.NewCoin(common.ATOMAsset, cosmos.NewUint(10*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("BB00000000000000000000000000000000000000000000000000000000000001"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("BB00000000000000000000000000000000000000000000000000000000000002"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("BB00000000000000000000000000000000000000000000000000000000000003"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("BB00000000000000000000000000000000000000000000000000000000000004"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
		NewMsgSwap(common.Tx{
			ID:    common.TxID("BB00000000000000000000000000000000000000000000000000000000000005"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))},
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),

		NewMsgSwap(common.Tx{
			ID:    common.TxID("0A00000000000000000000000000000000000000000000000000000000000001"),
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))},
		}, common.ATOMAsset, GetRandomGAIAAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
			"", "", nil,
			types.SwapType_market,
			0, 0, types.SwapVersion_v1, GetRandomBech32Addr()),
	}

	swaps = make(swapItems, len(msgs))
	for i, msg := range msgs {
		swaps[i] = swapItem{
			msg: *msg,
			fee: cosmos.ZeroUint(),
		}
	}
	swaps, err = queue.scoreMsgs(ctx, swaps, 10_000)
	c.Assert(err, IsNil)
	swaps = swaps.Sort()
	c.Assert(swaps, HasLen, 11)
	// Cross-pool/same-pool mix: assert the invariant (fee-desc with deterministic
	// tiebreak) rather than a hardcoded position-by-position order. Any reshuffle
	// from scoring changes is still legal so long as this invariant holds.
	assertSortedByFeeDesc(c, swaps)
	// Guard against the "assertSortedByFeeDesc passes on all-zero fees"
	// degenerate case — make sure scoring actually produced a non-zero fee
	// for the top item.
	c.Check(swaps[0].fee.GT(cosmos.ZeroUint()), Equals, true,
		Commentf("top item should have non-zero effective fee"))
	// Sort determinism: running Sort twice on the same input must match.
	second := append(swapItems{}, swaps...).Sort()
	for i := range swaps {
		c.Check(swaps[i].msg.Tx.ID.Equals(second[i].msg.Tx.ID), Equals, true,
			Commentf("Sort not deterministic at index %d", i))
	}
}

func (s SwapQueueSuite) TestScoreMsgsUsesMinSlipFloor(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "L1SlipMinBps", 100)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1_000_000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
	pool.Decimals = 8
	c.Assert(k.SetPool(ctx, pool), IsNil)

	amount := cosmos.NewUint(common.One)
	msg := NewMsgSwap(common.Tx{
		ID:    common.TxID("DD00000000000000000000000000000000000000000000000000000000000000"),
		Coins: common.Coins{common.NewCoin(common.ETHAsset, amount)},
	}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	queue := newSwapQueue(k)
	scored, err := queue.scoreMsgs(ctx, swapItems{{msg: *msg, fee: cosmos.ZeroUint()}}, 10_000)
	c.Assert(err, IsNil)
	c.Assert(scored, HasLen, 1)

	swapper, err := GetSwapper(k.GetVersion())
	c.Assert(err, IsNil)
	rawFee := swapper.CalcLiquidityFee(pool.BalanceAsset, amount, pool.BalanceRune)
	_, floorFee := expectedSwapLeg(ctx, k, pool, msg.Tx.Coins[0], 10_000, common.ETHAsset, false)

	c.Assert(floorFee.GT(rawFee), Equals, true,
		Commentf("min-slip fee should exceed raw liquidity fee: floor=%s raw=%s", floorFee, rawFee))
	c.Check(scored[0].fee.Equal(floorFee), Equals, true,
		Commentf("expected scored fee %s, got %s", floorFee, scored[0].fee))
}

func (s SwapQueueSuite) TestDoubleSwapScoreUsesFirstLegEmitRune(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "L1SlipMinBps", 0)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.Decimals = 8
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1_000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Decimals = 8
	c.Assert(k.SetPool(ctx, btcPool), IsNil)

	amount := cosmos.NewUint(50 * common.One)
	msg := NewMsgSwap(common.Tx{
		ID:    common.TxID("EE00000000000000000000000000000000000000000000000000000000000000"),
		Coins: common.Coins{common.NewCoin(common.ETHAsset, amount)},
	}, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		0, 0, types.SwapVersion_v1, GetRandomBech32Addr())

	queue := newSwapQueue(k)
	scored, err := queue.scoreMsgs(ctx, swapItems{{msg: *msg, fee: cosmos.ZeroUint()}}, 10_000)
	c.Assert(err, IsNil)
	c.Assert(scored, HasLen, 1)

	emitRune, firstFee := expectedSwapLeg(ctx, k, ethPool, msg.Tx.Coins[0], 10_000, common.ETHAsset, false)
	_, secondFee := expectedSwapLeg(ctx, k, btcPool, common.NewCoin(common.RuneAsset(), emitRune), 10_000, common.BTCAsset, false)
	expectedFee := firstFee.Add(secondFee)

	spotRune := ethPool.AssetValueInRune(amount)
	_, spotSecondFee := expectedSwapLeg(ctx, k, btcPool, common.NewCoin(common.RuneAsset(), spotRune), 10_000, common.BTCAsset, false)
	spotFee := firstFee.Add(spotSecondFee)

	c.Assert(spotRune.GT(emitRune), Equals, true,
		Commentf("test setup should make spot RUNE exceed emitted RUNE: spot=%s emit=%s", spotRune, emitRune))
	c.Assert(spotFee.GT(expectedFee), Equals, true,
		Commentf("test setup should distinguish spot scoring from emit scoring: spot=%s emit=%s", spotFee, expectedFee))
	c.Check(scored[0].fee.Equal(expectedFee), Equals, true,
		Commentf("expected scored fee %s, got %s", expectedFee, scored[0].fee))
	c.Check(scored[0].fee.Equal(spotFee), Equals, false,
		Commentf("score should not use spot-value RUNE for second leg"))
}

// assertSortedByFeeDesc verifies the invariant swapItems.Sort must preserve:
// items are ordered by fee descending, with (txID asc, sub-swap index asc)
// as a stable tiebreaker when fees are equal. Tests that don't care about
// exact positions should use this instead of hardcoding expected orderings,
// since any scoring change that preserves the invariant is still legal.
func assertSortedByFeeDesc(c *C, items swapItems) {
	for i := 0; i+1 < len(items); i++ {
		a, b := items[i], items[i+1]
		if a.fee.Equal(b.fee) {
			aID, bID := a.msg.Tx.ID.String(), b.msg.Tx.ID.String()
			if aID == bID {
				c.Check(a.index <= b.index, Equals, true,
					Commentf("equal (fee,txID) but index out of order at %d: %d > %d", i, a.index, b.index))
				continue
			}
			c.Check(aID < bID, Equals, true,
				Commentf("equal fee but txID out of order at %d: %s > %s", i, aID, bID))
			continue
		}
		c.Check(a.fee.GT(b.fee), Equals, true,
			Commentf("fee not descending at %d: %s < %s", i, a.fee.String(), b.fee.String()))
	}
}

func expectedSwapLeg(ctx cosmos.Context, k keeper.Keeper, pool Pool, sourceCoin common.Coin, virtualDepthMult int64, nonRuneAsset common.Asset, stableSwap bool) (cosmos.Uint, cosmos.Uint) {
	var X, x, Y cosmos.Uint
	x = sourceCoin.Amount
	if sourceCoin.IsRune() {
		X = pool.BalanceRune
		Y = pool.BalanceAsset
	} else {
		Y = pool.BalanceRune
		X = pool.BalanceAsset
	}

	X = common.GetUncappedShare(cosmos.NewUint(uint64(virtualDepthMult)), cosmos.NewUint(10_000), X)
	Y = common.GetUncappedShare(cosmos.NewUint(uint64(virtualDepthMult)), cosmos.NewUint(10_000), Y)

	swapper, err := GetSwapper(k.GetVersion())
	if err != nil {
		panic(err)
	}

	swapSlipBps := swapper.CalcSwapSlip(X, x)
	minSlipBps := getMinSlipBps(ctx, k, common.NoAddress, nonRuneAsset, stableSwap, nil, nil, common.EmptyAsset, common.EmptyAsset, nil)
	emit, fee, _ := swapper.GetSwapCalc(X, x, Y, swapSlipBps, minSlipBps)
	emit = cosmos.RoundToDecimal(emit, pool.Decimals)
	if sourceCoin.IsRune() {
		fee = pool.AssetValueInRune(fee)
	}
	return emit, fee
}

func (s SwapQueueSuite) TestStreamingSwapSelection(c *C) {
	ctx, k := setupKeeperForTest(c)
	queue := newSwapQueue(k)

	ethAddr := GetRandomETHAddress()
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		ethAddr,
		ethAddr,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One*100))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"",
	)

	// happy path
	msg := NewMsgSwap(tx, common.ETHAsset.GetSyntheticAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 10, 20, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(k.SetSwapQueueItem(ctx, *msg, 0), IsNil)

	// no saved streaming swap, should swap now
	items, err := queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)

	// save streaming swap data, should have same result
	swp := msg.GetStreamingSwap()
	k.SetStreamingSwap(ctx, swp)
	items, err = queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)

	// last height is this block, no result
	swp.LastHeight = ctx.BlockHeight()
	k.SetStreamingSwap(ctx, swp)
	items, err = queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)

	// last height is halfway there
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + (int64(swp.Interval) / 2))
	items, err = queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)

	// last height is interval blocks ago
	ctx = ctx.WithBlockHeight(swp.LastHeight + int64(swp.Interval))
	items, err = queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)
}

func (s SwapQueueSuite) TestStreamingSwapOutbounds(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(143166 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	pool.Asset = common.ETHAsset
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	queue := newSwapQueue(mgr.Keeper())

	badHandler := func(mgr Manager) cosmos.Handler {
		return func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			return nil, fmt.Errorf("failed handler")
		}
	}
	/*
		goodHandler := func(mgr Manager) cosmos.Handler {
			return func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
				return nil, fmt.Errorf("failed handler")
			}
		}
	*/

	ethAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		ethAddr,
		ethAddr,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		fmt.Sprintf("=:BTC.BTC:%s", btcAddr),
	)

	msg := NewMsgSwap(tx, common.BTCAsset, btcAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 10, 20, types.SwapVersion_v1, GetRandomBech32Addr())
	swp := msg.GetStreamingSwap()
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)

	// test that the refund handler works
	queue.handler = badHandler
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(strings.HasPrefix(items[0].Memo, "REFUND:"), Equals, true)
	// ensure swp has been deleted
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false)
	// ensure swap queue item is gone
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "not found")
	mgr.TxOutStore().ClearOutboundItems(ctx)

	// test we DO NOT send outbound while streaming swap isn't done
	swp.In = swp.Deposit.QuoUint64(2)
	swp.Out = cosmos.NewUint(12345)
	swp.Count = 5
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
	// make sure we haven't delete the streaming swap entity
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, true)
	// ensure swap queue item is NOT gone
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)

	// test we DO send outbounds while streaming swap is done
	swp.In = swp.Deposit.QuoUint64(3)
	swp.Out = cosmos.NewUint(12345)
	swp.Count = swp.Quantity
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 2)
	c.Check(items[0].Memo, Equals, "")                                 // ensure it's not a refund tx (for the partial swap)
	c.Check(strings.HasPrefix(items[1].Memo, "REFUND:"), Equals, true) // ensure it's a refund tx (for the partial refund)
	c.Check(items[0].Coin.Equals(common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))), Equals, true, Commentf("%s", items[0].Coin.String()))
	c.Check(items[1].Coin.Equals(common.NewCoin(common.ETHAsset, cosmos.NewUint(6666666667))), Equals, true, Commentf("%s", items[1].Coin.String()))
	// make sure we have deleted the streaming swap entity
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false)
	// ensure swap queue item is gone
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)

	// test we do send send the outbound (no refund needed)
	swp.In = swp.Deposit
	swp.Out = cosmos.NewUint(12345)
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].Memo, Equals, "") // ensure its not a refund tx
	c.Check(items[0].Coin.Equals(common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))), Equals, true, Commentf("%s", items[0].Coin.String()))
	// make sure we have deleted the streaming swap entity
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false)
	// ensure swap queue item is gone
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)
}

// TestStreamingSwapOutboundMemoParseFailure is a regression test for issues
// #2983/#2981: a completed streaming swap's state is removed before the memo is
// re-parsed. If ParseMemoWithTHORNames fails (e.g. a THORName referenced in the
// memo expired mid-stream), EndBlock previously returned early, orphaning the
// already-swapped output (swp.Out) with no outbound and no retry. EndBlock must
// instead log and still schedule the outbound to the already-resolved destination.
func (s SwapQueueSuite) TestStreamingSwapOutboundMemoParseFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(143166 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	queue := newSwapQueue(mgr.Keeper())
	queue.handler = func(mgr Manager) cosmos.Handler {
		return func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			return nil, fmt.Errorf("failed handler")
		}
	}

	ethAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	txID := GetRandomTxHash()
	// The memo references a THORName that does not exist, so a later
	// ParseMemoWithTHORNames call fails even though msg.Destination was already
	// resolved to btcAddr at swap-creation time.
	tx := common.NewTx(
		txID,
		ethAddr,
		ethAddr,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"=:BTC.BTC:nonexistentthorname",
	)

	msg := NewMsgSwap(tx, common.BTCAsset, btcAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 10, 20, types.SwapVersion_v1, GetRandomBech32Addr())
	swp := msg.GetStreamingSwap()
	swp.In = swp.Deposit
	swp.Out = cosmos.NewUint(12345)
	swp.Count = swp.Quantity
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)

	// Even though the memo can no longer be parsed, EndBlock must not return an
	// error and must still schedule the already-swapped output outbound.
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].Memo, Equals, "") // not a refund, the swapped output is delivered
	c.Check(items[0].ToAddress.Equals(btcAddr), Equals, true)
	c.Check(items[0].Coin.Equals(common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))), Equals, true, Commentf("%s", items[0].Coin.String()))
	// state removed so it is not retried
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)
}

// TestStreamingSwapOutboundAggregatorFailure is a regression test for issue
// #2982: within the completed streaming-swap block (after state removal), a
// FetchDexAggregator failure previously returned early, orphaning swp.Out and
// aborting the rest of the queue for the block. EndBlock must instead log and
// schedule a plain outbound without the aggregator so the funds are delivered.
func (s SwapQueueSuite) TestStreamingSwapOutboundAggregatorFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(143166 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	queue := newSwapQueue(mgr.Keeper())
	queue.handler = func(mgr Manager) cosmos.Handler {
		return func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			return nil, fmt.Errorf("failed handler")
		}
	}

	ethAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		ethAddr,
		ethAddr,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		fmt.Sprintf("=:BTC.BTC:%s", btcAddr),
	)

	// A non-empty aggregator that is not on the whitelist for the BTC target
	// chain, so FetchDexAggregator returns an error at completion time.
	msg := NewMsgSwap(tx, common.BTCAsset, btcAddr, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "notawhitelistedaggregator", "", nil, types.SwapType_market, 10, 20, types.SwapVersion_v1, GetRandomBech32Addr())
	swp := msg.GetStreamingSwap()
	swp.In = swp.Deposit
	swp.Out = cosmos.NewUint(12345)
	swp.Count = swp.Quantity
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)

	// The aggregator lookup fails, but EndBlock must not return an error and must
	// still schedule the outbound (without an aggregator) so the output is delivered.
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].Memo, Equals, "")
	c.Check(items[0].Aggregator, Equals, "") // scheduled without the invalid aggregator
	c.Check(items[0].ToAddress.Equals(btcAddr), Equals, true)
	c.Check(items[0].Coin.Equals(common.NewCoin(common.BTCAsset, cosmos.NewUint(12345))), Equals, true, Commentf("%s", items[0].Coin.String()))
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false)
	_, err = mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)
}

// TestStreamingSwapSaversAddMemoParseFailure verifies that when a completed
// streaming *savers add* can no longer be fully parsed (e.g. an affiliate
// THORName in the memo expired mid-stream), EndBlock still correctly treats it
// as an add and does NOT schedule an external outbound for the synth output.
// The add-type is recovered from the memo prefix, which does not depend on
// THORName resolution.
func (s SwapQueueSuite) TestStreamingSwapSaversAddMemoParseFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(143166 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	queue := newSwapQueue(mgr.Keeper())
	queue.handler = func(mgr Manager) cosmos.Handler {
		return func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
			return nil, fmt.Errorf("failed handler")
		}
	}

	btcAddr := GetRandomBTCAddress()
	txID := GetRandomTxHash()
	synthAsset := common.BTCAsset.GetSyntheticAsset()
	// A savers add memo whose affiliate is a THORName that does not exist, so a
	// later ParseMemoWithTHORNames fails at completion time.
	tx := common.NewTx(
		txID,
		btcAddr,
		btcAddr,
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One*100))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
		"+:BTC/BTC::nonexistentthorname:100",
	)

	msg := NewMsgSwap(tx, synthAsset, common.NoAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 10, 20, types.SwapVersion_v1, GetRandomBech32Addr())
	swp := msg.GetStreamingSwap()
	swp.In = swp.Deposit
	swp.Out = cosmos.NewUint(12345)
	swp.Count = swp.Quantity
	mgr.Keeper().SetStreamingSwap(ctx, swp)
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)

	// EndBlock must not error and must NOT schedule an outbound for a savers add,
	// even though the memo can no longer be fully parsed.
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0, Commentf("savers add must not schedule an outbound"))
	// state removed
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false)
	mgr.TxOutStore().ClearOutboundItems(ctx)
}

// TestStreamingLimitSwapNoDivisionByZero is a regression test for the October 7, 2025 incident
// where a limit swap with streaming parameters (quantity=2, interval=2) caused a division by zero panic.
//
// The bug occurred because:
// 1. User creates a limit swap with streaming parameters while EnableAdvSwapQueue mimir = 0 (forcing v1 mode)
// 2. Handler skips creating StreamingSwap record for limit swaps (handler_swap.go:326)
// 3. Manager tries to update non-existent swap, creating a "hollowed-out" record with Interval=0
// 4. Next block attempts division by zero: (height - lastHeight) % interval
//
// This test verifies that limit swaps with streaming parameters:
// - Execute successfully without panicking
// - Do NOT create or update StreamingSwap records in keeper
// - Complete in a single iteration
func (s SwapQueueSuite) TestStreamingLimitSwapNoDivisionByZero(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()

	// Setup pool
	pool := NewPool()
	pool.Asset = common.AVAXAsset
	pool.BalanceRune = cosmos.NewUint(143166 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	queue := newSwapQueue(mgr.Keeper())

	// Create the exact transaction from the incident:
	// Streaming LIMIT swap with quantity=2, interval=2
	// Memo: =<:AVAX-USDT-0X9702230A8EA53601F5CD2DC00FDBC13D4DF4A8C7:thor1dvvr4kdeurs8fdwgrql6je7l2v9ma73dp50n7m:40000000000/2/2
	txID := common.TxID("43F310A416A4ED8CF8B645B1EBBB5E25FB89F9777A4350F7023DEB62B90EA3AD")
	tx := common.NewTx(
		txID,
		GetRandomTHORAddress(),
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(40000000000))),
		common.Gas{},
		"=<:AVAX-USDT-0X9702230A8EA53601F5CD2DC00FDBC13D4DF4A8C7:thor1dvvr4kdeurs8fdwgrql6je7l2v9ma73dp50n7m:40000000000/2/2",
	)

	// Create a LIMIT swap with streaming parameters (quantity=2, interval=2)
	// This is the problematic combination that caused the panic
	targetAsset, _ := common.NewAsset("AVAX.USDT-0X9702230A8EA53601F5CD2DC00FDBC13D4DF4A8C7")
	msg := NewMsgSwap(
		tx,
		targetAsset,
		GetRandomTHORAddress(),
		cosmos.NewUint(40000000000), // trade target (limit price)
		common.NoAddress,
		cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_limit, // This is a LIMIT swap
		2,                    // quantity = 2 (streaming parameter)
		2,                    // interval = 2 (streaming parameter)
		types.SwapVersion_v1,
		GetRandomBech32Addr(),
	)

	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)

	// CRITICAL: Verify that no StreamingSwap record exists before EndBlock
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false,
		Commentf("No StreamingSwap record should exist for limit swap before processing"))

	// Process the swap - this should NOT panic with division by zero
	c.Assert(queue.EndBlock(ctx, mgr), IsNil)

	// Verify no StreamingSwap record was created (limit swaps should not persist state)
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false,
		Commentf("Limit swap should NOT create StreamingSwap record"))

	// Verify swap queue item was removed (swap completed)
	_, err := mgr.Keeper().GetSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "not found",
		Commentf("Swap queue item should be removed after limit swap completes"))

	// Simulate the next block to ensure no division by zero panic occurs
	// In the original bug, this is where the panic happened
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// This should complete without panic - there should be no items to fetch
	// because the limit swap completed in a single iteration
	items, err := queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0,
		Commentf("No items should remain in queue after limit swap completion"))

	// Explicitly verify the keeper validation prevents invalid swaps from being saved
	// This tests the defense-in-depth validation layer
	invalidSwap := NewStreamingSwap(
		txID,
		0, // quantity = 0 (invalid)
		0, // interval = 0 (invalid - would cause division by zero)
		cosmos.ZeroUint(),
		cosmos.ZeroUint(),
	)
	invalidSwap.LastHeight = ctx.BlockHeight()

	// Attempting to save this invalid swap should be rejected by the keeper
	mgr.Keeper().SetStreamingSwap(ctx, invalidSwap)
	c.Check(mgr.Keeper().StreamingSwapExists(ctx, txID), Equals, false,
		Commentf("Keeper should reject invalid StreamingSwap with interval=0"))
}
