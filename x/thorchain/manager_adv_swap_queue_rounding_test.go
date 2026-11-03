package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	kv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Record native outbounds too: TxOutStoreDummy only retains external outbounds.
type roundedLimitSwapTxOutStore struct {
	*TxOutStoreDummy
	items []TxOutItem
}

func (s *roundedLimitSwapTxOutStore) TryAddTxOutItem(_ cosmos.Context, _ Manager, item TxOutItem, _ cosmos.Uint) (bool, error) {
	s.items = append(s.items, item)
	return true, nil
}

type roundedLimitSwapFixture struct {
	ctx   cosmos.Context
	mgr   *Mgrs
	book  *SwapQueueAdv
	swap  *MsgSwap
	pool  Pool
	owner cosmos.AccAddress
	out   *roundedLimitSwapTxOutStore
}

func setupRoundedLimitSwap(c *C, asset common.Asset, units, depth, deposit, quantity uint64) roundedLimitSwapFixture {
	ctx, mgr := setupManagerForTest(c)
	out := &roundedLimitSwapTxOutStore{TxOutStoreDummy: NewTxStoreDummy()}
	mgr.txOutStore = out
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.AdvSwapQueueRapidSwapMax.String(), 1)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	owner := GetRandomBech32Addr()
	if asset.IsTradeAsset() {
		unit := types.NewTradeUnit(asset)
		unit.Units = cosmos.NewUint(units)
		unit.Depth = cosmos.NewUint(depth)
		mgr.Keeper().SetTradeUnit(ctx, unit)
		account := types.NewTradeAccount(owner, asset)
		account.Units = cosmos.NewUint(units)
		mgr.Keeper().SetTradeAccount(ctx, account)
	} else {
		_, err := mgr.SecuredAssetManager().Deposit(ctx, asset.GetLayer1Asset(), cosmos.NewUint(units), owner, common.NoAddress, common.BlankTxID)
		c.Assert(err, IsNil)
		secured, err := mgr.Keeper().GetSecuredAsset(ctx, asset)
		c.Assert(err, IsNil)
		secured.Depth = cosmos.NewUint(depth)
		mgr.Keeper().SetSecuredAsset(ctx, secured)
	}

	destination := GetRandomTHORAddress()
	tx := common.NewTx(
		GetRandomTxHash(), common.Address(owner.String()), GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(asset, cosmos.NewUint(deposit))), common.Gas{},
		fmt.Sprintf("=<:%s:%s:1", common.RuneAsset(), destination),
	)
	swap := NewMsgSwap(tx, common.RuneAsset(), destination, cosmos.OneUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil,
		types.SwapType_limit, quantity, 0, types.SwapVersion_v2, owner)
	swap.InitialBlockHeight = ctx.BlockHeight()
	swap.State = &types.SwapState{
		Quantity: quantity, Deposit: cosmos.NewUint(deposit),
		In: cosmos.ZeroUint(), Out: cosmos.ZeroUint(), LastHeight: ctx.BlockHeight() - 1,
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *swap), IsNil)
	return roundedLimitSwapFixture{ctx, mgr, newSwapQueueAdv(mgr.Keeper()), swap, pool, owner, out}
}

func (f roundedLimitSwapFixture) balance() cosmos.Uint {
	asset := f.swap.Tx.Coins[0].Asset
	if asset.IsTradeAsset() {
		return f.mgr.TradeAccountManager().BalanceOf(f.ctx, asset, f.owner)
	}
	return f.mgr.SecuredAssetManager().BalanceOf(f.ctx, asset, f.owner)
}

func (f roundedLimitSwapFixture) assertInvariant(c *C) {
	messages, broken := kv1.StreamingSwapsInvariant(*f.mgr.Keeper().(*kv1.KVStore))(f.ctx)
	c.Assert(broken, Equals, false, Commentf("%v", messages))
}

func (f roundedLimitSwapFixture) events(eventType string) []map[string]string {
	var events []map[string]string
	for _, event := range f.ctx.EventManager().Events() {
		if event.Type != eventType {
			continue
		}
		attributes := make(map[string]string)
		for _, attribute := range event.Attributes {
			attributes[attribute.Key] = attribute.Value
		}
		if attributes["tx_id"] == f.swap.Tx.ID.String() {
			events = append(events, attributes)
		}
	}
	return events
}

func (s AdvSwapQueueSuite) TestEndBlockRoundedLimitSwapStopsAtQuantity(c *C) {
	for _, asset := range []common.Asset{common.BTCAsset.GetTradeAsset(), common.BTCAsset.GetSecuredAsset()} {
		c.Logf("source asset: %s", asset)
		f := setupRoundedLimitSwap(c, asset, 11, 37, 10, 2)
		// Each requested 5-sat chunk redeems one share worth 3 sats. The first
		// short fill must remain queued so the second configured swap can run.
		c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		queued, err := f.mgr.Keeper().GetAdvSwapQueueItem(f.ctx, f.swap.Tx.ID, 0)
		c.Assert(err, IsNil)
		c.Assert(queued.State.In.String(), Equals, "3")
		c.Assert(queued.SuccessCount(), Equals, uint64(1))
		c.Assert(queued.Tx.Coins[0].Amount.String(), Equals, "10")
		c.Assert(queued.State.Out.IsZero(), Equals, false)
		c.Assert(f.out.items, HasLen, 0)
		c.Assert(f.balance().String(), Equals, "34")
		firstLiquidityFee, err := f.mgr.Keeper().GetPoolLiquidityFees(f.ctx, uint64(f.ctx.BlockHeight()), common.BTCAsset)
		c.Assert(err, IsNil)
		f.assertInvariant(c)

		f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1)
		c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		_, err = f.mgr.Keeper().GetAdvSwapQueueItem(f.ctx, f.swap.Tx.ID, 0)
		c.Assert(err, NotNil)
		pool, err := f.mgr.Keeper().GetPool(f.ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(pool.BalanceAsset.Sub(f.pool.BalanceAsset).String(), Equals, "6")
		c.Assert(f.balance().String(), Equals, "31")
		c.Assert(f.out.items, HasLen, 1)
		out := f.out.items[0]
		c.Assert(out.InHash, Equals, f.swap.Tx.ID)
		c.Assert(out.ToAddress, Equals, f.swap.Destination)
		c.Assert(out.Coin.Asset, Equals, common.RuneAsset())
		secondLiquidityFee, err := f.mgr.Keeper().GetPoolLiquidityFees(f.ctx, uint64(f.ctx.BlockHeight()), common.BTCAsset)
		c.Assert(err, IsNil)
		expectedOut := f.pool.BalanceRune.Sub(pool.BalanceRune).Sub(firstLiquidityFee).Sub(secondLiquidityFee)
		c.Assert(out.Coin.Amount.Equal(expectedOut), Equals, true)
		c.Assert(out.Coin.Amount.GT(queued.State.Out), Equals, true)
		streamEvents := f.events(types.StreamingSwapEventType)
		c.Assert(streamEvents, HasLen, 1)
		c.Assert(streamEvents[0]["count"], Equals, "2")
		c.Assert(streamEvents[0]["quantity"], Equals, "2")
		c.Assert(streamEvents[0]["in"], Equals, common.NewCoin(asset, cosmos.NewUint(6)).String())
		c.Assert(streamEvents[0]["out"], Equals, out.Coin.String())
		c.Assert(streamEvents[0]["failed_swaps"], Equals, "")
		f.assertInvariant(c)

		for i := 0; i < 3; i++ {
			f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1)
			c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
			f.assertInvariant(c)
		}
		c.Assert(f.out.items, HasLen, 1)
		c.Assert(f.events(types.StreamingSwapEventType), HasLen, 1)
		c.Assert(f.balance().String(), Equals, "31")
	}
}

func (s AdvSwapQueueSuite) TestEndBlockRoundedLimitSwapSettlesUnredeemableFirstChunk(c *C) {
	for _, asset := range []common.Asset{common.BTCAsset.GetTradeAsset(), common.BTCAsset.GetSecuredAsset()} {
		c.Logf("source asset: %s", asset)
		f := setupRoundedLimitSwap(c, asset, 1, 3, 1, 1)
		// The account has 3 sats, but redeeming 1 sat rounds to zero shares.
		c.Assert(f.balance().String(), Equals, "3")
		c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		_, err := f.mgr.Keeper().GetAdvSwapQueueItem(f.ctx, f.swap.Tx.ID, 0)
		c.Assert(err, NotNil)
		c.Assert(f.balance().String(), Equals, "3")
		pool, err := f.mgr.Keeper().GetPool(f.ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(pool.BalanceAsset.Equal(f.pool.BalanceAsset), Equals, true)
		c.Assert(pool.BalanceRune.Equal(f.pool.BalanceRune), Equals, true)
		c.Assert(f.out.items, HasLen, 0)
		f.assertInvariant(c)

		f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1)
		c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		c.Assert(f.balance().String(), Equals, "3")
		c.Assert(f.out.items, HasLen, 0)
	}
}

func (s AdvSwapQueueSuite) TestEndBlockRoundedLimitSwapSettlesUnredeemableRemainder(c *C) {
	for _, asset := range []common.Asset{common.BTCAsset.GetTradeAsset(), common.BTCAsset.GetSecuredAsset()} {
		c.Logf("source asset: %s", asset)
		f := setupRoundedLimitSwap(c, asset, 3, 7, 3, 2)
		// The first 2 sats redeem one share. The remaining 1 sat rounds to zero
		// of the remaining two shares, despite an account balance of 5 sats.
		c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		queued, err := f.mgr.Keeper().GetAdvSwapQueueItem(f.ctx, f.swap.Tx.ID, 0)
		c.Assert(err, IsNil)
		c.Assert(queued.State.In.String(), Equals, "2")
		c.Assert(queued.SuccessCount(), Equals, uint64(1))
		c.Assert(queued.State.Out.IsZero(), Equals, false)
		c.Assert(f.balance().String(), Equals, "5")
		c.Assert(f.out.items, HasLen, 0)
		poolAfterFill, err := f.mgr.Keeper().GetPool(f.ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		f.assertInvariant(c)

		f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1)
		c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		_, err = f.mgr.Keeper().GetAdvSwapQueueItem(f.ctx, f.swap.Tx.ID, 0)
		c.Assert(err, NotNil)
		c.Assert(f.balance().String(), Equals, "5")
		poolAfterSettlement, err := f.mgr.Keeper().GetPool(f.ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(poolAfterSettlement.BalanceAsset.Equal(poolAfterFill.BalanceAsset), Equals, true)
		c.Assert(poolAfterSettlement.BalanceRune.Equal(poolAfterFill.BalanceRune), Equals, true)
		c.Assert(f.out.items, HasLen, 1)
		c.Assert(f.out.items[0].InHash, Equals, f.swap.Tx.ID)
		c.Assert(f.out.items[0].ToAddress, Equals, f.swap.Destination)
		c.Assert(f.out.items[0].Coin, DeepEquals, common.NewCoin(common.RuneAsset(), queued.State.Out))
		streamEvents := f.events(types.StreamingSwapEventType)
		c.Assert(streamEvents, HasLen, 1)
		c.Assert(streamEvents[0]["count"], Equals, "1")
		c.Assert(streamEvents[0]["quantity"], Equals, "2")
		c.Assert(streamEvents[0]["in"], Equals, common.NewCoin(asset, queued.State.In).String())
		c.Assert(streamEvents[0]["out"], Equals, f.out.items[0].Coin.String())
		c.Assert(streamEvents[0]["failed_swaps"], Equals, "")
		f.assertInvariant(c)

		for i := 0; i < 3; i++ {
			f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1)
			c.Assert(f.book.EndBlock(f.ctx, f.mgr, false), IsNil)
		}
		c.Assert(f.out.items, HasLen, 1)
		c.Assert(f.events(types.StreamingSwapEventType), HasLen, 1)
		c.Assert(f.balance().String(), Equals, "5")
	}
}
