package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type KeeperTxOutSuite struct{}

var _ = Suite(&KeeperTxOutSuite{})

func (KeeperTxOutSuite) TestKeeperTxOut(c *C) {
	ctx, k := setupKeeperForTest(c)
	txOut := NewTxOut(1)
	spent := cosmos.NewUint(100)
	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "hello",
		CloutSpent:  &spent,
	}
	txOut.TxArray = append(txOut.TxArray, txOutItem)
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(92419747020392)
	pool.BalanceAsset = cosmos.NewUint(1402011488988)
	err := k.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	txOut1, err := k.GetTxOut(ctx, 1)
	c.Assert(err, IsNil)
	c.Assert(txOut1, NotNil)
	c.Assert(txOut1.Height, Equals, int64(1))

	txOut2, err := k.GetTxOut(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(txOut2, NotNil)

	c.Check(k.AppendTxOut(ctx, 100, txOutItem), IsNil)

	iter := k.GetTxOutIterator(ctx)
	c.Check(iter, NotNil)
	defer iter.Close()
	c.Check(k.ClearTxOut(ctx, 100), IsNil)

	txOut3 := NewTxOut(1024)
	c.Check(k.SetTxOut(ctx, txOut3), IsNil)

	value, clout, err := k.GetTxOutValue(ctx, 1)
	c.Assert(err, IsNil)
	c.Check(value.Uint64(), Equals, uint64(659193934902), Commentf("%d", value.Uint64()))
	c.Check(clout.String(), Equals, "100")
}

func (KeeperTxOutSuite) TestKeeperTxOutCache(c *C) {
	ctx, k := setupKeeperForTest(c)
	spent := cosmos.NewUint(100)
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "hello",
		CloutSpent:  &spent,
	}
	stored := NewTxOut(5)
	stored.TxArray = append(stored.TxArray, item)
	c.Assert(k.SetTxOut(ctx, stored), IsNil)

	// read returns the TxOut of height and the gas the read consumed
	read := func(ctx cosmos.Context, height int64) (*TxOut, uint64) {
		before := ctx.GasMeter().GasConsumed()
		txOut, err := k.GetTxOut(ctx, height)
		c.Assert(err, IsNil)
		return txOut, ctx.GasMeter().GasConsumed() - before
	}

	plain := map[int64]*TxOut{}
	for _, h := range []int64{5, 6} {
		plain[h], _ = read(ctx, h)
	}
	c.Assert(plain[5].TxArray, HasLen, 1)
	c.Assert(plain[6].TxArray, HasLen, 0)

	cached := keeper.WithTxOutCache(ctx)
	cache := keeper.TxOutCacheFromContext(cached)
	c.Assert(cache, NotNil)
	c.Check(keeper.TxOutCacheFromContext(keeper.WithTxOutCache(cached)) == cache, Equals, true) // reused, not replaced

	// present and absent heights read the store once, then come from the cache
	for _, h := range []int64{5, 6} {
		first, gas := read(cached, h)
		c.Check(gas > 0, Equals, true)
		c.Check(first, DeepEquals, plain[h])
		second, gas := read(cached, h)
		c.Check(gas, Equals, uint64(0))
		c.Check(second, DeepEquals, plain[h])
	}

	// a caller mutating the returned TxOut does not change the cached one
	mutated, _ := read(cached, 5)
	mutated.TxArray[0].Memo = "changed"
	mutated.TxArray[0].Coin.Amount = cosmos.NewUint(1)
	mutated.TxArray = append(mutated.TxArray, item)
	again, _ := read(cached, 5)
	c.Check(again, DeepEquals, plain[5])

	// a fork shares the snapshot; its writes disable only the fork
	forked := keeper.ForkTxOutCache(cached)
	_, gas := read(forked, 6)
	c.Check(gas, Equals, uint64(0))
	_, gas = read(forked, 7) // filled through the fork
	c.Check(gas > 0, Equals, true)
	_, gas = read(cached, 7)
	c.Check(gas, Equals, uint64(0))
	branch, _ := forked.CacheContext()
	c.Assert(k.AppendTxOut(branch, 6, item), IsNil)
	got, gas := read(forked, 6)
	c.Check(gas > 0, Equals, true)
	c.Check(got.TxArray, HasLen, 0) // branch never written back
	got, gas = read(branch, 6)
	c.Check(gas > 0, Equals, true)
	c.Check(got.TxArray, HasLen, 1)
	_, gas = read(cached, 6)
	c.Check(gas, Equals, uint64(0))

	// any txout write through the cached context disables it for good,
	// including writes through a branch of it
	for i, write := range []func(ctx cosmos.Context){
		func(ctx cosmos.Context) { c.Assert(k.SetTxOut(ctx, stored), IsNil) },
		func(ctx cosmos.Context) { c.Assert(k.AppendTxOut(ctx, 6, item), IsNil) },
		func(ctx cosmos.Context) { c.Assert(k.ClearTxOut(ctx, 5), IsNil) },
	} {
		comment := Commentf("write %d", i)
		cached = keeper.WithTxOutCache(ctx)
		_, _ = read(cached, 5)
		_, gas = read(cached, 5)
		c.Check(gas, Equals, uint64(0), comment)
		branch, _ := cached.CacheContext()
		write(branch)
		_, gas = read(cached, 5)
		c.Check(gas > 0, Equals, true, comment)
		_, gas = read(cached, 5) // no refill
		c.Check(gas > 0, Equals, true, comment)
	}
}
