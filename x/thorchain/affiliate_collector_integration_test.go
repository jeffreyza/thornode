package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type AffiliateCollectorIntegrationSuite struct{}

var _ = Suite(&AffiliateCollectorIntegrationSuite{})

type affiliateCollectorIntegrationGasManager struct {
	GasManager
	fee uint64
}

func (g affiliateCollectorIntegrationGasManager) GetAssetOutboundFee(_ cosmos.Context, _ common.Asset, _ bool) (cosmos.Uint, error) {
	return cosmos.NewUint(g.fee), nil
}

func setupAffiliateCollectorPartners(c *C) (cosmos.Context, *Mgrs, THORName, THORName) {
	ctx, mgr := setupManagerForTest(c)
	mgr.gasMgr = affiliateCollectorIntegrationGasManager{GasManager: mgr.GasMgr(), fee: 100}
	owner := GetRandomBech32Addr()
	partners := make([]THORName, 2)
	for i, name := range []string{"few", "tan"} {
		partners[i] = NewTHORName(name, 1000, []THORNameAlias{
			{Chain: common.THORChain, Address: common.Address(owner.String())},
			{Chain: common.BTCChain, Address: GetRandomBTCAddress()},
		})
		partners[i].Owner = owner
		partners[i].PreferredAsset = common.BTCAsset
		partners[i].PreferredAssetOutboundFeeMultiplier = 1
		mgr.Keeper().SetTHORName(ctx, partners[i])
	}
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	return ctx, mgr, partners[0], partners[1]
}

func assertNamedAffiliateBalance(c *C, ctx cosmos.Context, k keeper.Keeper, name string, amount uint64) {
	collector, err := k.GetAffiliateCollector(ctx, name)
	c.Assert(err, IsNil)
	c.Check(collector.RuneAmount.String(), Equals, cosmos.NewUint(amount).String(), Commentf("collector %s", name))
}

func assertAffiliateIntegrationInvariant(c *C, ctx cosmos.Context, k keeper.Keeper) {
	for _, route := range k.InvariantRoutes() {
		if route.Route == "affiliate_collector" {
			messages, broken := route.Invariant(ctx)
			c.Assert(broken, Equals, false, Commentf("affiliate collector invariant: %v", messages))
			return
		}
	}
	c.Fatal("affiliate collector invariant route missing")
}

func affiliateIntegrationQueuedSwaps(c *C, ctx cosmos.Context, k keeper.Keeper) []MsgSwap {
	var swaps []MsgSwap
	for _, iterator := range []cosmos.Iterator{k.GetSwapQueueIterator(ctx), k.GetAdvSwapQueueItemIterator(ctx)} {
		for ; iterator.Valid(); iterator.Next() {
			var msg MsgSwap
			c.Assert(k.Cdc().Unmarshal(iterator.Value(), &msg), IsNil)
			swaps = append(swaps, msg)
		}
		iterator.Close()
	}
	return swaps
}

// Both THORNames share an owner and preferred asset, but have different beneficiaries.
// Each threshold and queued payout must depend only on that THORName's earnings.
func (AffiliateCollectorIntegrationSuite) TestSameOwnerFeesAndPreferredPayoutsStaySeparate(c *C) {
	for _, advanced := range []int64{0, 1} {
		ctx, mgr, few, tan := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), advanced)
		beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
		tx := GetRandomTx()
		memo := fmt.Sprintf("=:THOR.RUNE:%s::few/tan:600/300", GetRandomTHORAddress())
		fees, err := skimAffiliateFees(ctx, mgr, tx, GetRandomBech32Addr(), common.NewCoin(common.RuneNative, cosmos.NewUint(1000)), memo)
		c.Assert(err, IsNil)
		c.Assert(fees.String(), Equals, "90")
		assertNamedAffiliateBalance(c, ctx, k, "FEW", 60)
		assertNamedAffiliateBalance(c, ctx, k, "tan", 30)
		c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 0)
		assertAffiliateIntegrationInvariant(c, ctx, k)

		// Exactly reaching the threshold does not trigger a payout.
		index := 0
		c.Assert(addRuneToAffiliateCollector(ctx, mgr, common.NewCoin(common.RuneNative, cosmos.NewUint(40)), &few, &index), IsNil)
		assertNamedAffiliateBalance(c, ctx, k, "few", 100)
		c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 0)

		c.Assert(addRuneToAffiliateCollector(ctx, mgr, common.NewCoin(common.RuneNative, cosmos.NewUint(1)), &few, &index), IsNil)
		assertNamedAffiliateBalance(c, ctx, k, "few", 0)
		assertNamedAffiliateBalance(c, ctx, k, "tan", 30)
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "30")
		assertAffiliateIntegrationInvariant(c, ctx, k)

		c.Assert(addRuneToAffiliateCollector(ctx, mgr, common.NewCoin(common.RuneNative, cosmos.NewUint(71)), &tan, &index), IsNil)
		assertNamedAffiliateBalance(c, ctx, k, "tan", 0)
		swaps := affiliateIntegrationQueuedSwaps(c, ctx, k)
		c.Assert(swaps, HasLen, 2, Commentf("advanced queue mode %d", advanced))
		seen := make(map[string]bool)
		for _, swap := range swaps {
			c.Check(swap.Tx.Coins[0].Amount.String(), Equals, "101")
			c.Check(swap.TargetAsset.Equals(common.BTCAsset), Equals, true)
			switch swap.Tx.Memo {
			case PreferredAssetSwapMemoPrefix + "-few":
				c.Check(swap.Destination.Equals(few.GetAlias(common.BTCChain)), Equals, true)
			case PreferredAssetSwapMemoPrefix + "-tan":
				c.Check(swap.Destination.Equals(tan.GetAlias(common.BTCChain)), Equals, true)
			default:
				c.Fatalf("unexpected payout memo %s", swap.Tx.Memo)
			}
			seen[swap.Tx.Memo] = true
		}
		c.Check(seen, HasLen, 2)
		c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}
