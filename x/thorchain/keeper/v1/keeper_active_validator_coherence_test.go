package keeperv1

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"

	. "gopkg.in/check.v1"
)

type KeeperActiveValidatorCoherenceSuite struct{}

var _ = Suite(&KeeperActiveValidatorCoherenceSuite{})

// TestTwoKeepersListActiveValidatorsCoherent pins the invariant that two KVStore
// instances over the same store stay coherent: a node-account (bond) write
// through keeper A must be visible to keeper B's next ListActiveValidators in
// the same block, and the bond invariant must still hold.
//
// Background: the app builds separate KVStore instances for the fee/ante path
// and for the managers (app.go wires two runtime.NewKVStoreService values over
// the same store key). A per-keeper-instance memoization of ListActiveValidators
// broke this — keeper B kept serving the pre-fee bond after keeper A charged a
// fee, and a Ragnarok-start reward payout that read keeper B's stale list saved
// the old bond back, leaving the bond module short. This test fails against that
// cached implementation and passes once the per-instance cache is removed.
func (s *KeeperActiveValidatorCoherenceSuite) TestTwoKeepersListActiveValidatorsCoherent(c *C) {
	ctx, kA := setupKeeperForTest(c)
	s.checkTwoKeepersCoherent(c, ctx, kA)
}

// The BeginBlock/EndBlock active-validator cache is scoped to the context, not
// to a keeper instance, so a write through either keeper disables it for both.
func (s *KeeperActiveValidatorCoherenceSuite) TestTwoKeepersCoherentWithBlockCache(c *C) {
	ctx, kA := setupKeeperForTest(c)
	s.checkTwoKeepersCoherent(c, keeper.WithActiveValidatorsCache(ctx), kA)
}

func (s *KeeperActiveValidatorCoherenceSuite) checkTwoKeepersCoherent(c *C, ctx cosmos.Context, kA KVStore) {
	// The removed cache only served in ExecModeFinalize; exercise that mode so
	// the test covers the path that was cached.
	ctx = ctx.WithExecMode(sdk.ExecModeFinalize)

	// A second keeper instance over the SAME store, as the managers get a
	// distinct keeper from the app/fee handler.
	kB := NewKVStore(kA.cdc, kA.storeKey, kA.coinKeeper, kA.accountKeeper, kA.upgradeKeeper, kA.version)

	// One active node bonded 1000 RUNE, bond module funded to match, no pending
	// bond rewards — so the bond invariant is satisfied to start.
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(kA.SetNodeAccount(ctx, na), IsNil)
	fund := common.NewCoin(common.RuneAsset(), na.Bond)
	c.Assert(kA.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(kA.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(fund)), IsNil)
	network, err := kA.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = cosmos.ZeroUint()
	c.Assert(kA.SetNetwork(ctx, network), IsNil)

	// Populate keeper B's active-validator view (this is what the old cache
	// memoized at bond=1000).
	before, err := kB.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(before), Equals, 1)
	c.Assert(before[0].Bond.Equal(na.Bond), Equals, true)

	// Charge a bond fee through keeper A: debit the node's bond and move the same
	// amount bond-module -> reserve, as a real node-paid fee does, keeping the
	// invariant satisfiable at the new, lower bond.
	fee := cosmos.NewUint(2 * common.One / 100) // 0.02 RUNE
	na.Bond = common.SafeSub(na.Bond, fee)
	c.Assert(kA.SetNodeAccount(ctx, na), IsNil)
	feeCoin := common.NewCoin(common.RuneAsset(), fee)
	c.Assert(kA.SendFromModuleToModule(ctx, BondName, ReserveName, common.NewCoins(feeCoin)), IsNil)

	// Keeper B must now observe the reduced bond, not the stale pre-fee 1000.
	after, err := kB.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(after), Equals, 1)
	c.Check(after[0].Bond.Equal(na.Bond), Equals, true,
		Commentf("keeper B served a stale bond across instances: got %s want %s", after[0].Bond, na.Bond))

	// And the bond invariant holds across the two keepers.
	_, broken := BondInvariant(kB)(ctx)
	c.Check(broken, Equals, false)
}
