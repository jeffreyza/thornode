package thorchain

import (
	"strconv"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type LuviRewardSuite struct{}

var _ = Suite(&LuviRewardSuite{})

const (
	luviRewardPoolRune  = 1_000_000 * common.One
	luviRewardPoolAsset = 500_000 * common.One
)

// luviRewardEnv has an ETH pool without synths and a BTC pool with synths at
// half the asset depth (S/(2A) = 25%), both earning liquidity fees.
func luviRewardEnv(c *C, height int64) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(height)
	k := mgr.Keeper()
	for _, asset := range []common.Asset{common.ETHAsset, common.BTCAsset} {
		p := NewPool()
		p.Asset = asset
		p.BalanceRune = cosmos.NewUint(luviRewardPoolRune)
		p.BalanceAsset = cosmos.NewUint(luviRewardPoolAsset)
		p.LPUnits = cosmos.NewUint(luviRewardPoolRune)
		p.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, p), IsNil)
		c.Assert(k.AddToLiquidityFees(ctx, asset, cosmos.NewUint(100*common.One)), IsNil)

		vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{asset.GetChain().String()}, []ChainContract{})
		vault.Coins = common.NewCoins(common.NewCoin(asset, p.BalanceAsset))
		c.Assert(k.SetVault(ctx, vault), IsNil)
	}
	synth := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(luviRewardPoolAsset/2))
	c.Assert(k.MintToModule(ctx, ModuleName, synth), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synth)), IsNil)

	FundModule(c, ctx, k, ReserveName, 100_000_000*common.One)
	FundModule(c, ctx, k, AsgardName, 2*luviRewardPoolRune)
	c.Assert(k.SetNetwork(ctx, NewNetwork()), IsNil)
	// bond well above the pooled RUNE, so the pendulum pays pools
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(10_000_000 * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	}
	return ctx, mgr
}

func luviRewardEvents(ctx cosmos.Context) []map[string]string {
	var out []map[string]string
	for _, e := range types.FlattenLuviEvents(ctx.EventManager().Events()) {
		if e["type"] == types.LuviRewardEventType {
			out = append(out, e)
		}
	}
	return out
}

func luviRewardEventFor(evts []map[string]string, pool string) map[string]string {
	for _, e := range evts {
		if e["pool"] == pool && e["kind"] == "pool" {
			return e
		}
	}
	return nil
}

func atoi(c *C, s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	c.Assert(err, IsNil)
	return v
}

func (s *LuviRewardSuite) TestUpdateNetworkSample(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks)
	nm := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(nm.UpdateNetwork(ctx, mgr), IsNil)

	evts := luviRewardEvents(ctx)
	c.Assert(evts, HasLen, 2, Commentf("%v", evts))

	eth := luviRewardEventFor(evts, common.ETHAsset.String())
	c.Assert(eth, NotNil)
	c.Check(eth["ok"], Equals, "true")
	c.Check(eth["reason"], Equals, "")
	c.Check(eth["reward_rune"], Equals, eth["reported_rune"])
	c.Check(eth["reward_rune"], Not(Equals), "0")
	c.Check(eth["expected_lp_share_bps"], Equals, "10000")
	c.Check(eth["lp_share_bps"], Equals, "10000")

	btc := luviRewardEventFor(evts, common.BTCAsset.String())
	c.Assert(btc, NotNil)
	c.Check(btc["ok"], Equals, "true")
	c.Check(btc["expected_lp_share_bps"], Equals, "7500")
	c.Check(btc["lp_share_bps"], Equals, "7500")
	// the hedged dG is the same for both: it does not depend on the synths
	c.Check(btc["delta_g"], Equals, eth["delta_g"])
	c.Check(btc["delta_g"], Equals, btc["expected_delta_g"])
}

func (s *LuviRewardSuite) TestUpdateNetworkQuietOffSample(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks+1)
	nm := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(nm.UpdateNetwork(ctx, mgr), IsNil)
	c.Check(luviRewardEvents(ctx), HasLen, 0)
	// the rewards were paid
	p, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(p.BalanceRune.GT(cosmos.NewUint(luviRewardPoolRune)), Equals, true)
}

func (s *LuviRewardSuite) TestUpdateNetworkMimirOff(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks)
	mgr.Keeper().SetMimir(ctx, constants.LUVIGuard.String(), 0)
	nm := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(nm.UpdateNetwork(ctx, mgr), IsNil)
	c.Check(luviRewardEvents(ctx), HasLen, 0)

	pools := Pools{NewPool()}
	c.Check(beginLuviRewardCheck(ctx, mgr.Keeper(), []cosmos.Uint{cosmos.OneUint()}, pools), IsNil)
	var nilCheck *luviRewardCheck
	nilCheck.end(ctx, mgr.Keeper(), mgr.EventMgr(), nil) // no-op
}

// luviRewardPools returns the current ETH and BTC pools.
func luviRewardPools(c *C, ctx cosmos.Context, mgr *Mgrs) Pools {
	eth, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	btc, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	return Pools{eth, btc}
}

func luviRewardEvt(amts []cosmos.Uint, pools Pools) *types.EventRewards {
	var pa []PoolAmt
	for i, a := range amts {
		pa = append(pa, PoolAmt{Asset: pools[i].Asset, Amount: int64(a.Uint64())})
	}
	z := cosmos.ZeroUint()
	return NewEventRewards(z, pa, z, z, z, z, z)
}

func (s *LuviRewardSuite) TestCorrectPayoutPasses(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks+1)
	k := mgr.Keeper()
	nm := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	pools := luviRewardPools(c, ctx, mgr)
	amts := []cosmos.Uint{cosmos.NewUint(30 * common.One), cosmos.NewUint(70 * common.One)}

	chk := beginLuviRewardCheck(ctx, k, amts, pools)
	c.Assert(chk, NotNil)
	c.Assert(nm.payPoolRewards(ctx, amts, pools), IsNil)
	chk.end(ctx, k, mgr.EventMgr(), luviRewardEvt(amts, pools))
	c.Check(luviRewardEvents(ctx), HasLen, 0)
}

func (s *LuviRewardSuite) TestRewardCreditedToWrongPool(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks+1)
	k := mgr.Keeper()
	pools := luviRewardPools(c, ctx, mgr)
	amts := []cosmos.Uint{cosmos.NewUint(30 * common.One), cosmos.NewUint(70 * common.One)}

	chk := beginLuviRewardCheck(ctx, k, amts, pools)
	// a faulty payout: the RUNE reaches Asgard, but both rewards land in BTC
	total := amts[0].Add(amts[1])
	c.Assert(k.SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(common.NewCoin(common.RuneNative, total))), IsNil)
	btc := pools[1]
	btc.BalanceRune = btc.BalanceRune.Add(total)
	c.Assert(k.SetPool(ctx, btc), IsNil)
	chk.end(ctx, k, mgr.EventMgr(), luviRewardEvt(amts, pools))

	evts := luviRewardEvents(ctx)
	c.Assert(evts, HasLen, 2, Commentf("%v", evts))
	for _, e := range evts {
		c.Check(e["ok"], Equals, "false")
		c.Check(e["reason"], Equals, "delta_g_mismatch")
	}
	eth := luviRewardEventFor(evts, common.ETHAsset.String())
	c.Assert(eth, NotNil)
	c.Check(eth["delta_g"], Equals, cosmos.ZeroDec().String())
	c.Check(eth["lp_share_bps"], Equals, "0")
	btcEvt := luviRewardEventFor(evts, common.BTCAsset.String())
	c.Assert(btcEvt, NotNil)
	// BTC got 100/70 of its reward: the LP share is 7500 * 100/70
	share := atoi(c, btcEvt["lp_share_bps"])
	c.Check(share >= 10_713 && share <= 10_715, Equals, true, Commentf("share %d", share))
}

func (s *LuviRewardSuite) TestRewardClobberedByLaterWrite(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks+1)
	k := mgr.Keeper()
	nm := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	pools := luviRewardPools(c, ctx, mgr)
	stale := pools[0]
	amts := []cosmos.Uint{cosmos.NewUint(30 * common.One), cosmos.NewUint(70 * common.One)}

	chk := beginLuviRewardCheck(ctx, k, amts, Pools{pools[0], pools[1]})
	c.Assert(nm.payPoolRewards(ctx, amts, Pools{pools[0], pools[1]}), IsNil)
	// a later write of a stale copy of the ETH pool undoes its reward
	c.Assert(k.SetPool(ctx, stale), IsNil)
	chk.end(ctx, k, mgr.EventMgr(), luviRewardEvt(amts, pools))

	evts := luviRewardEvents(ctx)
	c.Assert(evts, HasLen, 1, Commentf("%v", evts))
	c.Check(evts[0]["pool"], Equals, common.ETHAsset.String())
	c.Check(evts[0]["reason"], Equals, "delta_g_mismatch")
}

func (s *LuviRewardSuite) TestRewardsEventMismatch(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks+1)
	k := mgr.Keeper()
	nm := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	pools := luviRewardPools(c, ctx, mgr)
	amts := []cosmos.Uint{cosmos.NewUint(30 * common.One), cosmos.NewUint(70 * common.One)}

	chk := beginLuviRewardCheck(ctx, k, amts, pools)
	c.Assert(nm.payPoolRewards(ctx, amts, pools), IsNil)
	// the event under-reports the ETH reward
	reported := []cosmos.Uint{cosmos.NewUint(20 * common.One), amts[1]}
	chk.end(ctx, k, mgr.EventMgr(), luviRewardEvt(reported, pools))

	evts := luviRewardEvents(ctx)
	c.Assert(evts, HasLen, 2, Commentf("%v", evts))
	eth := luviRewardEventFor(evts, common.ETHAsset.String())
	c.Assert(eth, NotNil)
	c.Check(eth["reason"], Equals, "event_pool_mismatch")
	c.Check(eth["reported_rune"], Equals, cosmos.NewUint(20*common.One).String())
	var total map[string]string
	for _, e := range evts {
		if e["kind"] == "total" {
			total = e
		}
	}
	c.Assert(total, NotNil)
	c.Check(total["reason"], Equals, "event_total_mismatch")
	c.Check(total["ok"], Equals, "false")
	c.Check(total["reward_rune"], Equals, cosmos.NewUint(100*common.One).String())
	c.Check(total["reported_rune"], Equals, cosmos.NewUint(90*common.One).String())
}

func (s *LuviRewardSuite) TestRewardNotFunded(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks+1)
	k := mgr.Keeper()
	pools := luviRewardPools(c, ctx, mgr)
	amts := []cosmos.Uint{cosmos.NewUint(30 * common.One), cosmos.NewUint(70 * common.One)}

	chk := beginLuviRewardCheck(ctx, k, amts, pools)
	// depths credited, but the RUNE never left the Reserve
	for i := range pools {
		p := pools[i]
		p.BalanceRune = p.BalanceRune.Add(amts[i])
		c.Assert(k.SetPool(ctx, p), IsNil)
	}
	chk.end(ctx, k, mgr.EventMgr(), luviRewardEvt(amts, pools))

	evts := luviRewardEvents(ctx)
	c.Assert(evts, HasLen, 1, Commentf("%v", evts))
	c.Check(evts[0]["kind"], Equals, "total")
	c.Check(evts[0]["reason"], Equals, "asgard_balance_mismatch")
}

func (s *LuviRewardSuite) TestUntrackedAndZeroRewards(c *C) {
	ctx, mgr := luviRewardEnv(c, luviRewardSampleBlocks)
	k := mgr.Keeper()
	nm := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	pools := luviRewardPools(c, ctx, mgr)
	// a pool without LP units cannot be measured; a zero reward is skipped;
	// a reward without a pool is ignored
	empty := pools[0]
	empty.LPUnits = cosmos.ZeroUint()
	c.Assert(k.SetPool(ctx, empty), IsNil)
	amts := []cosmos.Uint{cosmos.NewUint(30 * common.One), cosmos.ZeroUint(), cosmos.OneUint()}
	paid := Pools{empty, pools[1]}

	chk := beginLuviRewardCheck(ctx, k, amts, paid)
	c.Assert(chk, NotNil)
	c.Assert(chk.entries, HasLen, 2)
	c.Assert(nm.payPoolRewards(ctx, amts[:2], paid), IsNil)
	chk.end(ctx, k, mgr.EventMgr(), luviRewardEvt(amts[:2], paid))
	// sample height, but neither entry is a measured, rewarded pool
	c.Check(luviRewardEvents(ctx), HasLen, 0)

	// without the rewards event every reward is unreported
	chk = beginLuviRewardCheck(ctx, k, amts[:1], paid)
	c.Assert(nm.payPoolRewards(ctx, amts[:1], paid), IsNil)
	chk.end(ctx, k, mgr.EventMgr(), nil)
	evts := luviRewardEvents(ctx)
	c.Assert(evts, HasLen, 2, Commentf("%v", evts))
	c.Check(evts[0]["reason"], Equals, "event_pool_mismatch")
	c.Check(evts[1]["reason"], Equals, "event_total_mismatch")

	c.Check(beginLuviRewardCheck(ctx, k, nil, nil), IsNil)
}
