package thorchain

import (
	"fmt"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type NetworkManagerTestSuite struct{}

var _ = Suite(&NetworkManagerTestSuite{})

func (s *NetworkManagerTestSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *NetworkManagerTestSuite) TestRotateVaultKeepsUnrelatedRegularFamiliesActive(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	oldRegularA := GetRandomVault()
	oldRegularA.Status = ActiveVault
	oldRegularA.Type = AsgardVault
	oldRegularA.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	oldRegularA.Membership = common.PubKeys{GetRandomPubKey(), GetRandomPubKey()}.Strings()

	oldRegularB := GetRandomVault()
	oldRegularB.Status = ActiveVault
	oldRegularB.Type = AsgardVault
	oldRegularB.Chains = common.Chains{common.THORChain, common.ETHChain}.Strings()
	oldRegularB.Membership = common.PubKeys{GetRandomPubKey(), GetRandomPubKey()}.Strings()

	activeXMR := GetRandomVault()
	activeXMR.Status = ActiveVault
	activeXMR.Type = AsgardVault
	activeXMR.Chains = xmrMonovaultChains().Strings()
	activeXMR.Membership = append([]string(nil), oldRegularA.Membership...)

	newRegularA := GetRandomVault()
	newRegularA.Status = InitVault
	newRegularA.Type = AsgardVault
	newRegularA.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	newRegularA.Membership = append([]string(nil), oldRegularA.Membership...)

	for _, vault := range []Vault{oldRegularA, oldRegularB, activeXMR, newRegularA} {
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	}

	c.Assert(networkMgr.RotateVault(ctx, newRegularA), IsNil)

	oldRegularAAfter, err := mgr.Keeper().GetVault(ctx, oldRegularA.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldRegularAAfter.Status, Equals, RetiringVault)

	oldRegularBAfter, err := mgr.Keeper().GetVault(ctx, oldRegularB.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldRegularBAfter.Status, Equals, ActiveVault)

	activeXMRAfter, err := mgr.Keeper().GetVault(ctx, activeXMR.PubKey)
	c.Assert(err, IsNil)
	c.Assert(activeXMRAfter.Status, Equals, ActiveVault)

	newRegularAAfter, err := mgr.Keeper().GetVault(ctx, newRegularA.PubKey)
	c.Assert(err, IsNil)
	c.Assert(newRegularAAfter.Status, Equals, ActiveVault)
}

func (s *NetworkManagerTestSuite) TestRotateVaultXMRMonovaultRetiresOnlyXMRFamilies(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	oldRegular := GetRandomVault()
	oldRegular.Status = ActiveVault
	oldRegular.Type = AsgardVault
	oldRegular.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()

	oldHybridXMR := GetRandomVault()
	oldHybridXMR.Status = ActiveVault
	oldHybridXMR.Type = AsgardVault
	oldHybridXMR.Chains = common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings()

	oldDedicatedXMR := GetRandomVault()
	oldDedicatedXMR.Status = ActiveVault
	oldDedicatedXMR.Type = AsgardVault
	oldDedicatedXMR.Chains = xmrMonovaultChains().Strings()
	oldDedicatedXMR.PubKeyFrost = GetRandomEd25519PubKey()

	newDedicatedXMR := GetRandomVault()
	newDedicatedXMR.Status = InitVault
	newDedicatedXMR.Type = AsgardVault
	newDedicatedXMR.Chains = xmrMonovaultChains().Strings()
	newDedicatedXMR.PubKeyFrost = GetRandomEd25519PubKey()

	for _, vault := range []Vault{oldRegular, oldHybridXMR, oldDedicatedXMR, newDedicatedXMR} {
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	}

	c.Assert(networkMgr.RotateVault(ctx, newDedicatedXMR), IsNil)

	oldRegularAfter, err := mgr.Keeper().GetVault(ctx, oldRegular.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldRegularAfter.Status, Equals, ActiveVault)

	oldHybridXMRAfter, err := mgr.Keeper().GetVault(ctx, oldHybridXMR.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldHybridXMRAfter.Status, Equals, RetiringVault)

	oldDedicatedXMRAfter, err := mgr.Keeper().GetVault(ctx, oldDedicatedXMR.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldDedicatedXMRAfter.Status, Equals, RetiringVault)

	newDedicatedXMRAfter, err := mgr.Keeper().GetVault(ctx, newDedicatedXMR.PubKey)
	c.Assert(err, IsNil)
	c.Assert(newDedicatedXMRAfter.Status, Equals, ActiveVault)
}

func (s *NetworkManagerTestSuite) TestTriggerKeygenIgnoresXMRMonovaultMembershipCollision(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeActive)
	nas := NodeAccounts{na1, na2}
	membership := []string{na1.PubKeySet.Secp256k1.String(), na2.PubKeySet.Secp256k1.String()}

	activeXMR := GetRandomVault()
	activeXMR.Status = ActiveVault
	activeXMR.Type = AsgardVault
	activeXMR.Chains = xmrMonovaultChains().Strings()
	activeXMR.Membership = membership
	activeXMR.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(mgr.Keeper().SetVault(ctx, activeXMR), IsNil)

	c.Assert(networkMgr.TriggerKeygen(ctx, nas), IsNil)

	keygenBlock, err := mgr.Keeper().GetKeygenBlock(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 1)
	c.Assert(keygenBlock.Keygens[0].Type, Equals, AsgardKeygen)
}

func (s *NetworkManagerTestSuite) TestTriggerXMRKeygenIgnoresRegularVaultMembershipCollision(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeActive)
	nas := NodeAccounts{na1, na2}
	membership := []string{na1.PubKeySet.Secp256k1.String(), na2.PubKeySet.Secp256k1.String()}

	activeRegular := GetRandomVault()
	activeRegular.Status = ActiveVault
	activeRegular.Type = AsgardVault
	activeRegular.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	activeRegular.Membership = membership
	c.Assert(mgr.Keeper().SetVault(ctx, activeRegular), IsNil)

	c.Assert(networkMgr.TriggerXMRKeygen(ctx, nas), IsNil)

	keygenBlock, err := mgr.Keeper().GetKeygenBlock(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 1)
	c.Assert(keygenBlock.Keygens[0].Type, Equals, XMRMonovaultKeygen)
}

func (s *NetworkManagerTestSuite) TestUpdateNetwork(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewVaultGenesisSetupTestHelper(mgr.Keeper())
	mgr.K = helper
	networkMgr := newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())

	// fail to get Network should return error
	helper.failGetNetwork = true
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), NotNil)
	helper.failGetNetwork = false

	// TotalReserve is zero , should not doing anything
	vd := NewNetwork()
	err := mgr.Keeper().SetNetwork(ctx, vd)
	c.Assert(err, IsNil)
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	p := NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(common.One * 100)
	p.BalanceAsset = cosmos.NewUint(common.One * 100)
	p.Status = PoolAvailable
	c.Assert(helper.SetPool(ctx, p), IsNil)
	// no active node , thus no bond
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	// Vault for getVaultsLiquidityRune.
	vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{p.Asset.GetChain().String()}, []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(p.Asset, p.BalanceAsset))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// with liquidity fee , and bonds
	c.Assert(helper.Keeper.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(50*common.One)), IsNil)

	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)
	// add bond
	c.Assert(helper.Keeper.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	// fail to get total liquidity fee should result an error
	helper.failGetTotalLiquidityFee = true
	if common.RuneAsset().Equals(common.RuneNative) {
		FundModule(c, ctx, helper, ReserveName, 100*common.One)
	}
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), NotNil)
	helper.failGetTotalLiquidityFee = false

	helper.failToListActiveAccounts = true
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), NotNil)
}

func (s *NetworkManagerTestSuite) TestCalcBlockRewards(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)

	// calcBlockRewards arguments: availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees cosmos.Uint, emissionCurve, blocksPerYear int64

	vaultsLiquidityRune := cosmos.NewUint(1000 * common.One)
	availablePoolsRune := vaultsLiquidityRune.QuoUint64(2) // vaultsLiquidityRune used for availablePoolsRune usually, but *1/2 when testing different values.
	effectiveSecurityBond := cosmos.NewUint(2000 * common.One)
	// Equilibrium state where effectiveSecurityBond is double vaultsLiquidityRune,
	// so expecting equal rewards for vaultsLiquidityRune and the effectiveSecurityBond portion of totalEffectiveBond.

	totalEffectiveBond := effectiveSecurityBond.MulUint64(3).QuoUint64(2) // effectiveSecurityBond used for totalEffectiveBond usually, but *3/2 when testing different values.
	totalReserve := cosmos.NewUint(1000 * common.One)
	totalLiquidityFees := cosmos.ZeroUint() // No liquidity fees unless explicitly specified.
	emissionCurve := constAccessor.GetInt64Value(constants.EmissionCurve)
	blocksPerYear := constAccessor.GetInt64Value(constants.BlocksPerYear)

	// For each example, first totalEffectiveBond = effectiveSecurityBond, as though there were only one node;
	// then totalEffectiveBond = 1.5 * effectiveSecurityBond, as though multiple nodes all with the same bond.

	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	// First, thorough testing with PendulumUseEffectiveSecurity and PendulumUseVaultAssets both true.
	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

	networkMgr.k.SetMimir(ctx, constants.PendulumUseEffectiveSecurity.String(), 1)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseVaultAssets.String(), 1)

	bondR, poolR, lpShare, _, _, _, _, _ := networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(1586), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1585), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(4998), Commentf("%d", lpShare.Uint64())) // Equilibrium
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(1903), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR := bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1268), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1268), Commentf("%d", poolR.Uint64()))     // Equilibrium
	c.Check(lpShare.Uint64(), Equals, uint64(3999), Commentf("%d", lpShare.Uint64())) // ~40% for availablePoolsRune, ~40% for effectiveSecurityBond (equilibrium), ~60% for totalEffectiveBond

	// vaultsLiquidityRune more than availablePoolsRune.
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	// TODO: poolR here is intended to be non-zero; find out what's strange.
	c.Check(bondR.Uint64(), Equals, uint64(2114), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1057), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(3333), Commentf("%d", lpShare.Uint64())) // 500 availablePoolsRune (1000 rune value asset+rune liquidity) is getting half the rewards of 2000 effectiveSecurityBond; same yield)
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(2378), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1585), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(793), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(2501), Commentf("%d", lpShare.Uint64())) // 500 availablePoolsRune (1000 rune value asset+rune liquidity) is getting a third the rewards of 3000 totalEffectiveBond; same yield)

	// Liquidity fees non-zero.
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, cosmos.NewUint(3000), cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3086), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(3085), Commentf("%d", poolR.Uint64()))     // Equilibrium with bondR.  (Liquidity fees sent to Reserve in swap, some received back in EndBlock rewards.)
	c.Check(lpShare.Uint64(), Equals, uint64(4999), Commentf("%d", lpShare.Uint64())) // Equilibrium
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, cosmos.NewUint(3000), cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3703), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(2468), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(2468), Commentf("%d", poolR.Uint64()))     // Equilibrium with effectiveSecurityBondR.  (Liquidity fees sent to Reserve in swap, some received back in EndBlock rewards.)
	c.Check(lpShare.Uint64(), Equals, uint64(3999), Commentf("%d", lpShare.Uint64())) // ~40% for availablePoolsRune, ~40% for effectiveSecurityBond (equilibrium), ~60% for totalEffectiveBond

	// Empty Reserve and no liquidity fees (all rewards zero).
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, cosmos.ZeroUint(), totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(0), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, cosmos.ZeroUint(), totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(0), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(0), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))

	// Now, half-size of effectiveSecurityBond.
	effectiveSecurityBond = cosmos.NewUint(1000 * common.One)

	// Provided liquidity equal to effectiveSecurityBond (no pool rewards).
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1057), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))

	// Zero provided liquidity (incapable of receiving pool rewards).
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.ZeroUint(), cosmos.ZeroUint(), effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64())) // No pools are capable of receiving rewards, so should not transfer any RUNE to the Pool Module (broken invariant).
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.ZeroUint(), cosmos.ZeroUint(), effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1057), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))

	// Provided liquidity more than effectiveSecurityBond.
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.NewUint(2001*common.One), cosmos.NewUint(2001*common.One), effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.NewUint(2001*common.One), cosmos.NewUint(2001*common.One), effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1057), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))

	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	// Next, thorough testing with PendulumUseEffectiveSecurity and PendulumUseVaultAssets both false.
	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

	networkMgr.k.SetMimir(ctx, constants.PendulumUseEffectiveSecurity.String(), 0)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseVaultAssets.String(), 1)
	effectiveSecurityBond = cosmos.NewUint(2000 * common.One) // Resetting from the half-size comparison.

	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(1586), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1585), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(4998), Commentf("%d", lpShare.Uint64())) // Equilibrium
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(1057), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(704), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(2114), Commentf("%d", poolR.Uint64()))     // Not equilibrium; double bondR.
	c.Check(lpShare.Uint64(), Equals, uint64(6667), Commentf("%d", lpShare.Uint64())) // ~67% for availablePoolsRune, ~22% for effectiveSecurityBond (equilibrium), ~33% for totalEffectiveBond

	// vaultsLiquidityRune more than availablePoolsRune.
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	// TODO: poolR here is intended to be non-zero; find out what's strange.
	c.Check(bondR.Uint64(), Equals, uint64(2114), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1057), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(3333), Commentf("%d", lpShare.Uint64())) // 500 availablePoolsRune (1000 rune value asset+rune liquidity) is getting half the rewards of 2000 effectiveSecurityBond; same yield)
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(1585), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1056), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1586), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(5002), Commentf("%d", lpShare.Uint64())) // 500 availablePoolsRune (1000 rune value asset+rune liquidity) is getting half the rewards of 3000 totalEffectiveBond; 3/2 higher yield)

	// Liquidity fees non-zero.
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, cosmos.NewUint(3000), cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3086), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(3085), Commentf("%d", poolR.Uint64()))     // Equilibrium with bondR.  (Liquidity fees sent to Reserve in swap, some received back in EndBlock rewards.)
	c.Check(lpShare.Uint64(), Equals, uint64(4999), Commentf("%d", lpShare.Uint64())) // Equilibrium
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, cosmos.NewUint(3000), cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(2057), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1371), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(4114), Commentf("%d", poolR.Uint64()))     // 3000 liquidity fees to Reserve in swap and 4114 rewards back, triple that of effectiveSecurityBondR.
	c.Check(lpShare.Uint64(), Equals, uint64(6667), Commentf("%d", lpShare.Uint64())) // ~67% for availablePoolsRune, ~22% for effectiveSecurityBond (equilibrium), ~33% for totalEffectiveBond

	// Empty Reserve and no liquidity fees (all rewards zero).
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, cosmos.ZeroUint(), totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(0), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, cosmos.ZeroUint(), totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(0), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(0), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))

	// Now, half-size of effectiveSecurityBond.
	effectiveSecurityBond = cosmos.NewUint(1000 * common.One)

	// Provided liquidity equal to effectiveSecurityBond (still pool rewards since less than totalEffectiveRune).
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, vaultsLiquidityRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(1057), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(352), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(2114), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(6667), Commentf("%d", lpShare.Uint64()))

	// Zero provided liquidity (incapable of receiving pool rewards).
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.ZeroUint(), cosmos.ZeroUint(), effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64())) // No pools are capable of receiving rewards, so should not transfer any RUNE to the Pool Module (broken invariant).
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.ZeroUint(), cosmos.ZeroUint(), effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(1057), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))

	// Provided liquidity more than effectiveSecurityBond.
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.NewUint(2001*common.One), cosmos.NewUint(2001*common.One), effectiveSecurityBond, effectiveSecurityBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(3171), Commentf("%d", bondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(0), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(0), Commentf("%d", lpShare.Uint64()))
	// With totalEffectiveBond = 1.5 * effectiveSecurityBond:
	bondR, poolR, lpShare, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, cosmos.NewUint(2001*common.One), cosmos.NewUint(2001*common.One), effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(bondR.Uint64(), Equals, uint64(2115), Commentf("%d", bondR.Uint64()))
	effectiveSecurityBondR = bondR.Mul(effectiveSecurityBond).Quo(totalEffectiveBond)
	c.Check(effectiveSecurityBondR.Uint64(), Equals, uint64(705), Commentf("%d", effectiveSecurityBondR.Uint64()))
	c.Check(poolR.Uint64(), Equals, uint64(1056), Commentf("%d", poolR.Uint64()))
	c.Check(lpShare.Uint64(), Equals, uint64(3330), Commentf("%d", lpShare.Uint64()))

	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	// Testing of different settings for PendulumAssetsBasisPoints, PendulumUseEffectiveSecurity, PendulumUseVaultAssets.
	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

	networkMgr.k.SetMimir(ctx, constants.PendulumAssetsBasisPoints.String(), 10_000)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseEffectiveSecurity.String(), 1)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseVaultAssets.String(), 1)

	c.Check(totalEffectiveBond.String(), Equals, cosmos.NewUint(3000*common.One).String())
	effectiveSecurityBond = cosmos.NewUint(2000 * common.One) // Resetting from the half-size comparison.
	c.Check(vaultsLiquidityRune.String(), Equals, cosmos.NewUint(1000*common.One).String())
	c.Check(availablePoolsRune.String(), Equals, cosmos.NewUint(500*common.One).String())

	bondR, poolR, _, devFundDeduct, systemIncomeBurnDeduct, tcyStakeDeduct, _, _ := networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(devFundDeduct.String(), Equals, "0")
	c.Check(systemIncomeBurnDeduct.String(), Equals, "0")
	c.Check(tcyStakeDeduct.String(), Equals, "0")
	c.Check(bondR.String(), Equals, "2378")
	c.Check(poolR.String(), Equals, "793")

	e12BondYield := bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield := poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "7926")
	c.Check(e12PoolYield.String(), Equals, "7930")
	c.Check(e12BondYield.QuoUint64(100).String(), Equals, e12PoolYield.QuoUint64(100).String())
	// Approximately equilibrium (affected slightly by rounding steps).

	///////////////////////////////////////////////////////////////////////////////
	// PendulumAssetsBasisPoints 13,333:
	networkMgr.k.SetMimir(ctx, constants.PendulumAssetsBasisPoints.String(), 13_333)

	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "9060")
	c.Check(e12PoolYield.String(), Equals, "4530")
	// The pendulum perceives secured liquidity as being 2/3rds of securing liquidity rather than 1/2, so nodes get more than the yield of pools (double).

	///////////////////////////////////////////////////////////////////////////////
	// PendulumAssetsBasisPoints 6,666:
	networkMgr.k.SetMimir(ctx, constants.PendulumAssetsBasisPoints.String(), 6666)
	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "6343")
	c.Check(e12PoolYield.String(), Equals, "12680")
	// The pendulum perceives secured liquidity as being 1/3rd of securing liquidity rather than 1/2, so nodes get less than the yield of pools (half).

	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	// PendulumUseEffectiveSecurity false, PendulumUseVaultAssets.String() true:
	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	networkMgr.k.SetMimir(ctx, constants.PendulumAssetsBasisPoints.String(), 10_000)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseEffectiveSecurity.String(), 0)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseVaultAssets.String(), 1)

	c.Check(totalEffectiveBond.String(), Equals, cosmos.NewUint(3000*common.One).String())
	c.Check(vaultsLiquidityRune.String(), Equals, cosmos.NewUint(1000*common.One).String())

	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "5283")
	c.Check(e12PoolYield.String(), Equals, "15860")
	// The pendulum perceives the secured vaults liquidity as being 1/3rd of the securing total effective bond, so nodes get 1/3rd the yield of pools.

	// Equilbrium yield when vaultsLiquidityRune is 3/2 greater:
	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune.MulUint64(3).QuoUint64(2), effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "7930")
	c.Check(e12PoolYield.String(), Equals, "7920")
	c.Check(e12BondYield.QuoUint64(100).String(), Equals, e12PoolYield.QuoUint64(100).String())

	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	// PendulumUseEffectiveSecurity true, PendulumUseVaultAssets.String() false:
	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	networkMgr.k.SetMimir(ctx, constants.PendulumAssetsBasisPoints.String(), 10_000)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseEffectiveSecurity.String(), 1)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseVaultAssets.String(), 0)

	c.Check(effectiveSecurityBond.String(), Equals, cosmos.NewUint(2000*common.One).String())
	c.Check(availablePoolsRune.String(), Equals, cosmos.NewUint(500*common.One).String())

	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "3526")
	c.Check(e12PoolYield.String(), Equals, "21130")
	// The pendulum perceives secured liquidity as being 1/4th of securing liquidity rather than 1/2, so nodes get less than the yield of pools (1/6th).

	// Equilbrium yield when availablePoolsRune is doubled:
	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune.MulUint64(2), vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2).MulUint64(2)) // The (doubled) pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "6343")
	c.Check(e12PoolYield.String(), Equals, "6340")
	c.Check(e12BondYield.QuoUint64(100).String(), Equals, e12PoolYield.QuoUint64(100).String())

	// No change when ignoring L1 Assets when vaultsLiquidityRune is equally increased in order to increase availablePoolsRune:
	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune.MulUint64(2), vaultsLiquidityRune.Add(availablePoolsRune), effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2).MulUint64(2)) // The (doubled) pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "6343")
	c.Check(e12PoolYield.String(), Equals, "6340")
	c.Check(e12BondYield.QuoUint64(100).String(), Equals, e12PoolYield.QuoUint64(100).String())

	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	// PendulumUseEffectiveSecurity false, PendulumUseVaultAssets false:
	////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
	networkMgr.k.SetMimir(ctx, constants.PendulumAssetsBasisPoints.String(), 10_000)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseEffectiveSecurity.String(), 0)
	networkMgr.k.SetMimir(ctx, constants.PendulumUseVaultAssets.String(), 0)

	c.Check(totalEffectiveBond.String(), Equals, cosmos.NewUint(3000*common.One).String())
	c.Check(availablePoolsRune.String(), Equals, cosmos.NewUint(500*common.One).String())

	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(2)) // The pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "1763")
	c.Check(e12PoolYield.String(), Equals, "26420")
	// The pendulum perceives secured liquidity as being 1/6th of securing liquidity rather than 1/2, so nodes get less than the yield of pools (1/15th).

	// Equilbrium yield when availablePoolsRune is tripled:
	bondR, poolR, _, _, _, _, _, _ = networkMgr.calcBlockRewards(ctx, availablePoolsRune.MulUint64(3), vaultsLiquidityRune, effectiveSecurityBond, totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve, blocksPerYear, 0, 0, 0, 0, 0)
	e12BondYield = bondR.MulUint64(1e12).Quo(totalEffectiveBond)
	e12PoolYield = poolR.MulUint64(1e12).Quo(availablePoolsRune.MulUint64(3).MulUint64(2)) // The (tripled) pool liquidity experiencing yield is the total value, both Asset and RUNE depths.
	c.Check(e12BondYield.String(), Equals, "5286")
	c.Check(e12PoolYield.String(), Equals, "5283")
	c.Check(e12BondYield.QuoUint64(100).String(), Equals, e12PoolYield.QuoUint64(100).String())
}

func (s *NetworkManagerTestSuite) TestCalcPoolDeficit(c *C) {
	pool1Fees := cosmos.NewUint(1000)
	pool2Fees := cosmos.NewUint(3000)
	totalFees := cosmos.NewUint(4000)

	mgr := NewDummyMgr()
	networkMgr := newNetworkMgr(keeper.KVStoreDummy{}, mgr.TxOutStore(), mgr.EventMgr())

	lpDeficit := cosmos.NewUint(1120)
	amt1 := networkMgr.calcPoolDeficit(lpDeficit, totalFees, pool1Fees)
	amt2 := networkMgr.calcPoolDeficit(lpDeficit, totalFees, pool2Fees)

	c.Check(amt1.Equal(cosmos.NewUint(280)), Equals, true, Commentf("%d", amt1.Uint64()))
	c.Check(amt2.Equal(cosmos.NewUint(840)), Equals, true, Commentf("%d", amt2.Uint64()))
}

func (*NetworkManagerTestSuite) TestProcessGenesisSetup(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewVaultGenesisSetupTestHelper(mgr.Keeper())
	ctx = ctx.WithBlockHeight(1)
	mgr.K = helper
	networkMgr := newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())
	// no active account
	c.Assert(networkMgr.EndBlock(ctx, mgr), NotNil)

	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccount), IsNil)
	mgr.Keeper().SetMimir(ctx, "HaltChurning", 1)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	// make sure the regular vault is created and the dedicated XMR monovault is
	// scheduled through keygen instead of being pre-created.
	vaults, err := mgr.Keeper().GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 1)
	regularVault := vaults[0]

	c.Assert(regularVault.PubKey.Equals(nodeAccount.PubKeySet.Secp256k1), Equals, true)
	c.Assert(regularVault.PubKeyEddsa.Equals(nodeAccount.PubKeySet.Ed25519), Equals, true)
	c.Assert(regularVault.PubKeyFrost.IsEmpty(), Equals, true)
	c.Assert(regularVault.HasChain(common.XMRChain), Equals, false)

	keygenBlock, err := mgr.Keeper().GetKeygenBlock(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 0)

	xmrBootstrapHeight := xmrMonovaultGenesisKeygenHeight(ctx.BlockHeight())
	keygenBlock, err = mgr.Keeper().GetKeygenBlock(ctx, xmrBootstrapHeight)
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 1)
	c.Assert(keygenBlock.Keygens[0].Type, Equals, XMRMonovaultKeygen)

	// fail to get asgard vaults should return an error
	helper.failToGetAsgardVaults = true
	c.Assert(networkMgr.EndBlock(ctx, mgr), NotNil)
	helper.failToGetAsgardVaults = false

	// vault already exist , it should not do anything , and should not error
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)

	ctx, mgr = setupManagerForTest(c)
	helper = NewVaultGenesisSetupTestHelper(mgr.Keeper())
	ctx = ctx.WithBlockHeight(1)
	mgr.K = helper
	networkMgr = newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())

	nodeAccountA := GetRandomValidatorNode(NodeActive)
	nodeAccountB := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccountA), IsNil)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccountB), IsNil)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)

	vaults, err = mgr.Keeper().GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 0)

	keygenBlock, err = mgr.Keeper().GetKeygenBlock(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 1)
	c.Assert(keygenBlock.Keygens[0].Type, Equals, AsgardKeygen)

	xmrBootstrapHeight = xmrMonovaultGenesisKeygenHeight(ctx.BlockHeight())
	keygenBlock, err = mgr.Keeper().GetKeygenBlock(ctx, xmrBootstrapHeight)
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 1)
	c.Assert(keygenBlock.Keygens[0].Type, Equals, XMRMonovaultKeygen)

	ctx, mgr = setupManagerForTest(c)
	helper = NewVaultGenesisSetupTestHelper(mgr.Keeper())
	ctx = ctx.WithBlockHeight(1)
	mgr.K = helper
	networkMgr = newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())
	helper.failToListActiveAccounts = true
	c.Assert(networkMgr.EndBlock(ctx, mgr), NotNil)
	helper.failToListActiveAccounts = false

	helper.failToSetVault = true
	c.Assert(networkMgr.EndBlock(ctx, mgr), NotNil)
	helper.failToSetVault = false

	helper.failGetRetiringAsgardVault = true
	ctx = ctx.WithBlockHeight(1024)
	c.Assert(networkMgr.migrateFunds(ctx, mgr), NotNil)
	helper.failGetRetiringAsgardVault = false

	helper.failGetActiveAsgardVault = true
	c.Assert(networkMgr.migrateFunds(ctx, mgr), NotNil)
	helper.failGetActiveAsgardVault = false
}

func (*NetworkManagerTestSuite) TestProcessGenesisSetupExistingVaultSkipsDelayedXMRKeygen(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(genesisBlockHeight)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	existing := NewVault(
		genesisBlockHeight,
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.THORChain, common.BTCChain}.Strings(),
		nil,
	)
	c.Assert(mgr.Keeper().SetVault(ctx, existing), IsNil)

	c.Assert(networkMgr.processGenesisSetup(ctx), IsNil)

	keygenBlock, err := mgr.Keeper().GetKeygenBlock(ctx, xmrMonovaultGenesisKeygenHeight(ctx.BlockHeight()))
	c.Assert(err, IsNil)
	c.Assert(keygenBlock.Keygens, HasLen, 0)

	vaults, err := mgr.Keeper().GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 1)
	c.Assert(vaults[0].PubKey.Equals(existing.PubKey), Equals, true)
}

// TestFreshGenesisEarlyXMRKeygensRotateSequentially characterizes a fresh
// mocknet chain where churn at height 61 can schedule XMR before the delayed
// genesis keygen at height 129. The records are in distinct keygen blocks, so
// the TSS activation boundary evaluates and rotates each generation on its own.
// This pins the current fresh-chain behavior, not a requirement to retain two
// generations: if future pending-keygen suppression is added, replace the
// early-generation assertions with the new single-generation contract.
func (*NetworkManagerTestSuite) TestFreshGenesisEarlyXMRKeygensRotateSequentially(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(genesisBlockHeight)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	membership := make(common.PubKeys, 0, 4)
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
		membership = append(membership, na.PubKeySet.Secp256k1)
	}

	c.Assert(networkMgr.processGenesisSetup(ctx), IsNil)
	delayedHeight := xmrMonovaultGenesisKeygenHeight(ctx.BlockHeight())
	delayedBlock, err := mgr.Keeper().GetKeygenBlock(ctx, delayedHeight)
	c.Assert(err, IsNil)
	c.Assert(delayedBlock.Keygens, HasLen, 1)
	c.Assert(delayedBlock.Keygens[0].Type, Equals, XMRMonovaultKeygen)

	regular := NewVault(
		genesisBlockHeight,
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.THORChain, common.BTCChain}.Strings(),
		nil,
	)
	regular.Membership = membership.Strings()
	c.Assert(mgr.Keeper().SetVault(ctx, regular), IsNil)

	earlyCtx := ctx.WithBlockHeight(61)
	mgr.Keeper().SetMimir(earlyCtx, constants.ChurnInterval.String(), 60)
	mgr.Keeper().SetMimir(earlyCtx, constants.DesiredValidatorSet.String(), 4)
	mgr.Keeper().SetMimir(earlyCtx, constants.AsgardSize.String(), 10)
	c.Assert(mgr.ValidatorMgr().BeginBlock(earlyCtx, mgr, nil), IsNil)
	earlyBlock, err := mgr.Keeper().GetKeygenBlock(earlyCtx, earlyCtx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(earlyBlock.Keygens, HasLen, 1)
	c.Assert(earlyBlock.Keygens[0].Type, Equals, XMRMonovaultKeygen)
	c.Assert(earlyBlock.Keygens[0].ID, Not(Equals), delayedBlock.Keygens[0].ID)

	handler := NewTssHandler(mgr)
	earlyVault := NewVaultV2(
		earlyCtx.BlockHeight(),
		InitVault,
		AsgardVault,
		GetRandomPubKey(),
		xmrMonovaultChains().Strings(),
		nil,
		common.EmptyPubKey,
	)
	earlyVault.Membership = earlyBlock.Keygens[0].Members
	earlyVault.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(mgr.Keeper().SetVault(earlyCtx, earlyVault), IsNil)
	c.Assert(handler.rotateReadyKeygenVaultFamilies(earlyCtx, earlyBlock), IsNil)

	earlyVault, err = mgr.Keeper().GetVault(earlyCtx, earlyVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(earlyVault.Status, Equals, ActiveVault)

	delayedCtx := ctx.WithBlockHeight(delayedHeight)
	delayedVault := NewVaultV2(
		delayedCtx.BlockHeight(),
		InitVault,
		AsgardVault,
		GetRandomPubKey(),
		xmrMonovaultChains().Strings(),
		nil,
		common.EmptyPubKey,
	)
	delayedVault.Membership = delayedBlock.Keygens[0].Members
	delayedVault.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(mgr.Keeper().SetVault(delayedCtx, delayedVault), IsNil)
	c.Assert(handler.rotateReadyKeygenVaultFamilies(delayedCtx, delayedBlock), IsNil)

	activeVaults, err := mgr.Keeper().GetAsgardVaultsByStatus(delayedCtx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(activeVaults, HasLen, 2)
	activeXMR := filterVaultsByChain(activeVaults, common.XMRChain)
	c.Assert(activeXMR, HasLen, 1)
	c.Assert(activeXMR[0].PubKey.Equals(delayedVault.PubKey), Equals, true)

	initVaults, err := mgr.Keeper().GetAsgardVaultsByStatus(delayedCtx, InitVault)
	c.Assert(err, IsNil)
	c.Assert(filterVaultsByChain(initVaults, common.XMRChain), HasLen, 0)

	earlyVault, err = mgr.Keeper().GetVault(delayedCtx, earlyVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(earlyVault.Status, Equals, RetiringVault)
	delayedVault, err = mgr.Keeper().GetVault(delayedCtx, delayedVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(delayedVault.Status, Equals, ActiveVault)
}

func (*NetworkManagerTestSuite) TestMigrateFundsPropagatesVaultPubKeyFrost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	interval := mgr.GetConstants().GetInt64Value(constants.FundMigrationInterval)
	c.Assert(interval > 0, Equals, true)
	ctx = ctx.WithBlockHeight(interval)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NetworkFee{
		Chain:              common.XMRChain,
		TransactionSize:    5000,
		TransactionFeeRate: 10000,
	}), IsNil)

	retiringVault := GetRandomVault()
	retiringVault.Status = RetiringVault
	retiringVault.StatusSince = 0
	retiringVault.Chains = common.Chains{common.XMRChain}.Strings()
	retiringVault.PubKeyFrost = GetRandomEd25519PubKey()
	retiringVault.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, retiringVault), IsNil)

	activeVault := GetRandomVault()
	activeVault.Status = ActiveVault
	activeVault.Chains = common.Chains{common.XMRChain}.Strings()
	activeVault.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)

	toAddress, err := activeVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(items[0].Chain, Equals, common.XMRChain)
	c.Assert(items[0].Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())
	c.Assert(items[0].ToAddress.Equals(toAddress), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(retiringVault.PubKey), Equals, true)
	c.Assert(items[0].VaultPubKeyEddsa.IsEmpty(), Equals, true)
	c.Assert(items[0].VaultPubKeyFrost.Equals(retiringVault.PubKeyFrost), Equals, true)
}

func (*NetworkManagerTestSuite) TestMigrateFundsDoesNotStallXMRMonovaultFundingOnNonMigratePendingActivity(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NetworkFee{
		Chain:              common.XMRChain,
		TransactionSize:    1,
		TransactionFeeRate: 1,
	}), IsNil)
	targetKey := GetRandomEd25519PubKey()
	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, targetKey, xmrMonovaultChains().Strings(), nil, targetKey)
	target.PubKeyFrost = targetKey
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	retiringKey := GetRandomEd25519PubKey()
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, retiringKey, xmrMonovaultChains().Strings(), nil, retiringKey)
	retiring.PubKeyFrost = retiringKey
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10*common.One))))
	retiring.PendingTxBlockHeights = []int64{ctx.BlockHeight()}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.XMRChain), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(retiring.PubKey), Equals, true)
	c.Assert(items[0].Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())
	targetAddr, err := target.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(items[0].ToAddress.Equals(targetAddr), Equals, true)
}

// TestMigrateFundsInactivatesDeficitOnlyRetiringVault verifies migration cleanup
// clears RetiringVault status once funds are gone, while preserving deficit state.
func (*NetworkManagerTestSuite) TestMigrateFundsInactivatesDeficitOnlyRetiringVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)

	active := GetRandomVault()
	active.Status = ActiveVault
	active.Chains = common.Chains{common.ETHChain}.Strings()
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)

	// The retiring vault has no coins left to migrate, but it still carries a
	// deficit that must remain recorded after status cleanup.
	retiring := GetRandomVault()
	retiring.Status = RetiringVault
	retiring.StatusSince = 0
	retiring.Chains = common.Chains{common.ETHChain}.Strings()
	retiring.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	// Deficit alone should not keep the vault in RetiringVault, but it should
	// continue to protect the vault from deletion.
	updated, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updated.Status, Equals, InactiveVault)
	c.Assert(updated.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(10)), Equals, true)
	c.Assert(mgr.Keeper().DeleteVault(ctx, retiring.PubKey), NotNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (*NetworkManagerTestSuite) TestMigrateFundsSkipsChainWithPendingMigrateTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NetworkFee{
		Chain:              common.XMRChain,
		TransactionSize:    1,
		TransactionFeeRate: 1,
	}), IsNil)

	targetKey := GetRandomEd25519PubKey()
	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, targetKey, xmrMonovaultChains().Strings(), nil, targetKey)
	target.PubKeyFrost = targetKey
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	retiringKey := GetRandomEd25519PubKey()
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, retiringKey, xmrMonovaultChains().Strings(), nil, retiringKey)
	retiring.PubKeyFrost = retiringKey
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	toAddress, err := target.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	txOut := &TxOut{
		Height: ctx.BlockHeight(),
		TxArray: []TxOutItem{
			{
				Chain:       common.XMRChain,
				ToAddress:   toAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
				Memo:        NewMigrateMemo(ctx.BlockHeight() - 1).String(),
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (*NetworkManagerTestSuite) TestMigrateFundsSkipsChainWithUnexpiredStalePendingMigrateTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(400)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XRPChain, NetworkFee{
		Chain:              common.XRPChain,
		TransactionSize:    1,
		TransactionFeeRate: 15,
	}), IsNil)

	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XRPChain}.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	staleHeight := ctx.BlockHeight() - mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod) - 1
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XRPChain}.Strings(), nil, GetRandomEd25519PubKey())
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.XRPAsset, cosmos.NewUint(2*common.One))))
	retiring.PendingTxBlockHeights = []int64{staleHeight}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	toAddress, err := target.GetAddress(common.XRPChain)
	c.Assert(err, IsNil)
	txOut := &TxOut{
		Height: staleHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.XRPChain,
				ToAddress:   toAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
				Memo:        NewMigrateMemo(staleHeight).String(),
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
	updatedRetiring, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedRetiring.PendingTxBlockHeights, DeepEquals, []int64{staleHeight})
}

func (*NetworkManagerTestSuite) TestMigrateFundsReissuesExpiredStalePendingMigrateTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(5000)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XRPChain, NetworkFee{
		Chain:              common.XRPChain,
		TransactionSize:    1,
		TransactionFeeRate: 15,
	}), IsNil)

	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XRPChain}.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	staleHeight := ctx.BlockHeight() - mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)*constants.StaleMigratePendingSigningPeriodMultiplier - 1
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XRPChain}.Strings(), nil, GetRandomEd25519PubKey())
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.XRPAsset, cosmos.NewUint(2*common.One))))
	// The same height can appear more than once when multiple migrate txouts are
	// scheduled for one vault in the same block.
	retiring.PendingTxBlockHeights = []int64{staleHeight, staleHeight}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	toAddress, err := target.GetAddress(common.XRPChain)
	c.Assert(err, IsNil)
	// Both stale txouts match the same retiring vault and memo height, so expiry
	// should replace both old requests and clear both old pending-height entries.
	txOut := &TxOut{
		Height: staleHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.XRPChain,
				ToAddress:   toAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
				Memo:        NewMigrateMemo(staleHeight).String(),
			},
			{
				Chain:       common.XRPChain,
				ToAddress:   toAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
				Memo:        NewMigrateMemo(staleHeight).String(),
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(retiring.PubKey), Equals, true)
	c.Assert(items[0].Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())

	updatedRetiring, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	// Only the freshly reissued migrate should remain pending.
	c.Assert(updatedRetiring.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight()})

	updatedTxOut, err := mgr.Keeper().GetTxOut(ctx, staleHeight)
	c.Assert(err, IsNil)
	c.Assert(updatedTxOut.TxArray, HasLen, 2)
	c.Assert(updatedTxOut.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
	c.Assert(updatedTxOut.TxArray[1].OutHash.Equals(common.BlankTxID), Equals, true)
}

// TestMigrateFundsPendingMigrateHoldsOnlyItsChain covers issue #3350: a stuck
// GAIA token migrate holds the whole GAIA chain, including its gas asset before
// the final round, while BASE keeps migrating from the same RetiringVault.
func (*NetworkManagerTestSuite) TestMigrateFundsPendingMigrateHoldsOnlyItsChain(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	chains := common.Chains{common.GAIAChain, common.BASEChain}
	for _, chain := range chains {
		c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    1,
			TransactionFeeRate: 1,
		}), IsNil)
	}

	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	gaiaToken, err := common.NewAsset("GAIA.LQDY")
	c.Assert(err, IsNil)
	pendingHeight := ctx.BlockHeight() - 1
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	retiring.AddFunds(common.NewCoins(
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(gaiaToken, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.BaseETHAsset, cosmos.NewUint(100*common.One)),
	))
	retiring.PendingTxBlockHeights = []int64{pendingHeight}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	gaiaAddress, err := target.GetAddress(common.GAIAChain)
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
		Height: pendingHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.GAIAChain,
				ToAddress:   gaiaAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(gaiaToken, cosmos.NewUint(20*common.One)),
				Memo:        NewMigrateMemo(pendingHeight).String(),
			},
		},
	}), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	// Only BASE is scheduled: neither the stuck GAIA token nor GAIA's gas asset.
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.BASEChain), Equals, true)
	c.Assert(items[0].Coin.Asset.Equals(common.BaseETHAsset), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(retiring.PubKey), Equals, true)
	c.Assert(items[0].Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())

	updatedRetiring, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedRetiring.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight(), pendingHeight})
}

// TestMigrateFundsUnexpiredStalePendingMigrateHoldsOnlyItsChain checks that a
// migrate older than the signing window, tracked only through the vault's
// pending heights, still holds its chain but no longer holds other chains.
func (*NetworkManagerTestSuite) TestMigrateFundsUnexpiredStalePendingMigrateHoldsOnlyItsChain(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(400)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)
	chains := common.Chains{common.XRPChain, common.BTCChain}
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XRPChain, NetworkFee{
		Chain:              common.XRPChain,
		TransactionSize:    1,
		TransactionFeeRate: 15,
	}), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NetworkFee{
		Chain:              common.BTCChain,
		TransactionSize:    1,
		TransactionFeeRate: 1,
	}), IsNil)

	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	staleHeight := ctx.BlockHeight() - mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod) - 1
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	// Enough XRP that, without the guard, the remainder would clear the reserve
	// and be migrated again.
	retiring.AddFunds(common.NewCoins(
		common.NewCoin(common.XRPAsset, cosmos.NewUint(10*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
	))
	retiring.PendingTxBlockHeights = []int64{staleHeight}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	xrpAddress, err := target.GetAddress(common.XRPChain)
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
		Height: staleHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.XRPChain,
				ToAddress:   xrpAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
				Memo:        NewMigrateMemo(staleHeight).String(),
			},
		},
	}), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.BTCChain), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(retiring.PubKey), Equals, true)

	// The unexpired XRP migrate stays tracked and signable next to the new BTC one.
	updatedRetiring, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedRetiring.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight(), staleHeight})
	staleTxOut, err := mgr.Keeper().GetTxOut(ctx, staleHeight)
	c.Assert(err, IsNil)
	c.Assert(staleTxOut.TxArray[0].OutHash.IsEmpty(), Equals, true)
}

// TestMigrateFundsExpiredPendingMigrateResumesItsChainWhileAnotherChainIsPending
// checks that stale expiry releases only the expired chain: XRP is reissued
// while a recent GAIA migrate from the same RetiringVault still holds GAIA.
func (*NetworkManagerTestSuite) TestMigrateFundsExpiredPendingMigrateResumesItsChainWhileAnotherChainIsPending(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(5000)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)
	chains := common.Chains{common.XRPChain, common.GAIAChain}
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XRPChain, NetworkFee{
		Chain:              common.XRPChain,
		TransactionSize:    1,
		TransactionFeeRate: 15,
	}), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.GAIAChain, NetworkFee{
		Chain:              common.GAIAChain,
		TransactionSize:    1,
		TransactionFeeRate: 1,
	}), IsNil)

	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	staleHeight := ctx.BlockHeight() - mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)*constants.StaleMigratePendingSigningPeriodMultiplier - 1
	gaiaHeight := ctx.BlockHeight() - 1
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	retiring.AddFunds(common.NewCoins(
		common.NewCoin(common.XRPAsset, cosmos.NewUint(10*common.One)),
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One)),
	))
	retiring.PendingTxBlockHeights = []int64{staleHeight, gaiaHeight}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	xrpAddress, err := target.GetAddress(common.XRPChain)
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
		Height: staleHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.XRPChain,
				ToAddress:   xrpAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
				Memo:        NewMigrateMemo(staleHeight).String(),
			},
		},
	}), IsNil)
	gaiaAddress, err := target.GetAddress(common.GAIAChain)
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
		Height: gaiaHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.GAIAChain,
				ToAddress:   gaiaAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(common.ATOMAsset, cosmos.NewUint(20*common.One)),
				Memo:        NewMigrateMemo(gaiaHeight).String(),
			},
		},
	}), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.XRPChain), Equals, true)
	c.Assert(items[0].Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())

	staleTxOut, err := mgr.Keeper().GetTxOut(ctx, staleHeight)
	c.Assert(err, IsNil)
	c.Assert(staleTxOut.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
	updatedRetiring, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedRetiring.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight(), gaiaHeight})
}

// TestMigrateFundsObservedMigrateResumesItsChainWhileAnotherChainIsPending
// walks the hold across two migration ticks: GAIA waits while its migrate is
// pending, then resumes once it is observed, while the BASE migrate scheduled
// on the first tick is still pending and now holds BASE.
func (*NetworkManagerTestSuite) TestMigrateFundsObservedMigrateResumesItsChainWhileAnotherChainIsPending(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 5)
	chains := common.Chains{common.GAIAChain, common.BASEChain}
	for _, chain := range chains {
		c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    1,
			TransactionFeeRate: 1,
		}), IsNil)
	}

	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	gaiaToken, err := common.NewAsset("GAIA.LQDY")
	c.Assert(err, IsNil)
	gaiaHeight := ctx.BlockHeight() - 1
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	retiring.AddFunds(common.NewCoins(
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(gaiaToken, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.BaseETHAsset, cosmos.NewUint(100*common.One)),
	))
	retiring.PendingTxBlockHeights = []int64{gaiaHeight}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	gaiaAddress, err := target.GetAddress(common.GAIAChain)
	c.Assert(err, IsNil)
	gaiaMigrate := common.NewCoin(gaiaToken, cosmos.NewUint(20*common.One))
	c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
		Height: gaiaHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.GAIAChain,
				ToAddress:   gaiaAddress,
				VaultPubKey: retiring.PubKey,
				Coin:        gaiaMigrate,
				Memo:        NewMigrateMemo(gaiaHeight).String(),
			},
		},
	}), IsNil)

	// First tick: GAIA is held, BASE migrates.
	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.BASEChain), Equals, true)

	// Persist the BASE migrate as a still-unsigned txout, as the real store would.
	baseHeight := ctx.BlockHeight()
	c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{Height: baseHeight, TxArray: items}), IsNil)
	mgr.TxOutStore().ClearOutboundItems(ctx)

	// Simulate the persisted effects of observing the GAIA migrate: mark its
	// txout sent, clear its pending height and debit the migrated funds.
	gaiaTxOut, err := mgr.Keeper().GetTxOut(ctx, gaiaHeight)
	c.Assert(err, IsNil)
	gaiaTxOut.TxArray[0].OutHash = GetRandomTxHash()
	c.Assert(mgr.Keeper().SetTxOut(ctx, gaiaTxOut), IsNil)
	retiring, err = mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	retiring.RemovePendingTxBlockHeights(gaiaHeight)
	retiring.SubFundsWithClamping(common.Coins{gaiaMigrate})
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	// Second tick: GAIA resumes (gas and token), BASE is held by its own pending migrate.
	ctx = ctx.WithBlockHeight(baseHeight + 1)
	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 2)
	scheduled := common.Coins{}
	for _, item := range items {
		c.Assert(item.Chain.Equals(common.GAIAChain), Equals, true)
		c.Assert(item.Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())
		scheduled = append(scheduled, item.Coin)
	}
	c.Assert(scheduled.GetCoin(common.ATOMAsset).Amount.IsZero(), Equals, false)
	c.Assert(scheduled.GetCoin(gaiaToken).Amount.IsZero(), Equals, false)

	updatedRetiring, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedRetiring.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight(), ctx.BlockHeight(), baseHeight})
}

func (*NetworkManagerTestSuite) TestMigrateFundsExpiresEveryPendingHeight(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(5000)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)

	chains := common.Chains{common.GAIAChain, common.BASEChain, common.BTCChain}
	target := NewVaultV2(1000, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)
	retiring := NewVaultV2(1000, RetiringVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	staleHeight := ctx.BlockHeight() - mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)*constants.StaleMigratePendingSigningPeriodMultiplier - 1
	for i, chain := range chains {
		c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    1,
			TransactionFeeRate: 1,
		}), IsNil)
		asset := chain.GetGasAsset()
		retiring.AddFunds(common.NewCoins(common.NewCoin(asset, cosmos.NewUint(100*common.One))))
		height := staleHeight - int64(i)
		retiring.PendingTxBlockHeights = append(retiring.PendingTxBlockHeights, height)
		address, err := target.GetAddress(chain)
		c.Assert(err, IsNil)
		c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
			Height: height,
			TxArray: []TxOutItem{{
				Chain:       chain,
				ToAddress:   address,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(asset, cosmos.NewUint(20*common.One)),
				Memo:        NewMigrateMemo(height).String(),
			}},
		}), IsNil)
	}
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	// Several chains can expire together after migration was paused. Removing
	// the newest pending height must not skip the next entry in the same slice.
	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, len(chains))
	for _, chain := range chains {
		count := 0
		for _, item := range items {
			if item.Chain.Equals(chain) {
				count++
				c.Assert(item.VaultPubKey.Equals(retiring.PubKey), Equals, true)
				c.Assert(item.Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())
			}
		}
		c.Check(count, Equals, 1)
	}
	// Every old request must be made unsignable before its replacement is queued.
	for _, height := range retiring.PendingTxBlockHeights {
		oldTxOut, getErr := mgr.Keeper().GetTxOut(ctx, height)
		c.Assert(getErr, IsNil)
		c.Check(oldTxOut.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true, Commentf("height %d", height))
	}
	updated, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updated.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight(), ctx.BlockHeight(), ctx.BlockHeight()})
}

func (*NetworkManagerTestSuite) TestMigrateFundsHoldsEveryPendingChain(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(400)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)

	chains := common.Chains{common.GAIAChain, common.BASEChain, common.BTCChain}
	target := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)
	retiring := NewVaultV2(1, RetiringVault, AsgardVault, GetRandomPubKey(), chains.Strings(), nil, GetRandomEd25519PubKey())
	for _, chain := range chains {
		c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    1,
			TransactionFeeRate: 1,
		}), IsNil)
		retiring.AddFunds(common.NewCoins(common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(100*common.One))))
	}
	// BASE is found in the rolling window; GAIA is found only through its older
	// durable pending height. Both must be held while the third chain progresses.
	heights := []int64{ctx.BlockHeight() - 1, ctx.BlockHeight() - mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod) - 1}
	retiring.PendingTxBlockHeights = heights
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	pendingChains := common.Chains{common.BASEChain, common.GAIAChain}
	for i, chain := range pendingChains {
		address, err := target.GetAddress(chain)
		c.Assert(err, IsNil)
		c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{
			Height: heights[i],
			TxArray: []TxOutItem{{
				Chain:       chain,
				ToAddress:   address,
				VaultPubKey: retiring.PubKey,
				Coin:        common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(20*common.One)),
				Memo:        NewMigrateMemo(heights[i]).String(),
			}},
		}), IsNil)
	}

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.BTCChain), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(retiring.PubKey), Equals, true)
	c.Assert(items[0].Memo, Equals, NewMigrateMemo(ctx.BlockHeight()).String())
	updated, err := mgr.Keeper().GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updated.PendingTxBlockHeights, DeepEquals, []int64{ctx.BlockHeight(), heights[0], heights[1]})
	for _, height := range heights {
		pending, getErr := mgr.Keeper().GetTxOut(ctx, height)
		c.Assert(getErr, IsNil)
		c.Assert(pending.TxArray[0].OutHash.IsEmpty(), Equals, true)
	}
}

func (*NetworkManagerTestSuite) TestMigrateFundsLeavesXMRDustBufferForFinalRound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NetworkFee{
		Chain:              common.XMRChain,
		TransactionSize:    1,
		TransactionFeeRate: 30000,
	}), IsNil)

	targetKey := GetRandomEd25519PubKey()
	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, targetKey, xmrMonovaultChains().Strings(), nil, targetKey)
	target.PubKeyFrost = targetKey
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	retiringKey := GetRandomEd25519PubKey()
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, retiringKey, xmrMonovaultChains().Strings(), nil, retiringKey)
	retiring.PubKeyFrost = retiringKey
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.XMRChain), Equals, true)
	maxGas, err := mgr.GasMgr().GetMaxGas(ctx, common.XMRChain)
	c.Assert(err, IsNil)
	gas := maxGas.Amount
	expected := common.SafeSub(
		common.SafeSub(retiring.Coins.GetCoin(common.XMRAsset).Amount, gas),
		common.XMRChain.DustThreshold(),
	)
	c.Assert(
		items[0].Coin.Amount.Equal(expected),
		Equals,
		true,
		Commentf("amount=%s gas=%s expected=%s", items[0].Coin.Amount.String(), gas.String(), expected.String()),
	)
}

func (*NetworkManagerTestSuite) TestMigrateFundsFinalXMRMigrationCoversFrostFeeEstimate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.txOutStore = NewTxStoreDummy()
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnMigrateRounds.String(), 2)

	const (
		xmrFrostWeight           = uint64(2000)
		xmrScheduledFeeRate      = uint64(4_700_000)
		actualFrostFeeEstimate   = uint64(1_073_010)
		previousMaxGasEstimate   = uint64(1_057_500)
		expectedCurrentMaxGasAmt = uint64(1_410_000)
	)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NetworkFee{
		Chain:              common.XMRChain,
		TransactionSize:    xmrFrostWeight,
		TransactionFeeRate: xmrScheduledFeeRate,
	}), IsNil)

	targetKey := GetRandomEd25519PubKey()
	target := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, targetKey, xmrMonovaultChains().Strings(), nil, targetKey)
	target.PubKeyFrost = targetKey
	c.Assert(mgr.Keeper().SetVault(ctx, target), IsNil)

	spendableBalance := cosmos.NewUint(100_000_000)
	retiringKey := GetRandomEd25519PubKey()
	retiring := NewVaultV2(ctx.BlockHeight()-1, RetiringVault, AsgardVault, retiringKey, xmrMonovaultChains().Strings(), nil, retiringKey)
	retiring.PubKeyFrost = retiringKey
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, spendableBalance)))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	maxGas, err := mgr.GasMgr().GetMaxGas(ctx, common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(maxGas.Amount.Equal(cosmos.NewUint(expectedCurrentMaxGasAmt)), Equals, true)
	c.Assert(previousMaxGasEstimate < actualFrostFeeEstimate, Equals, true)
	c.Assert(maxGas.Amount.GTE(cosmos.NewUint(actualFrostFeeEstimate)), Equals, true)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].Chain.Equals(common.XMRChain), Equals, true)
	c.Assert(items[0].VaultPubKey.Equals(retiring.PubKey), Equals, true)

	expectedAmount := common.SafeSub(
		common.SafeSub(spendableBalance, maxGas.Amount),
		common.XMRChain.DustThreshold(),
	)
	c.Assert(items[0].Coin.Amount.Equal(expectedAmount), Equals, true)

	scheduledSpend := items[0].Coin.Amount.Add(cosmos.NewUint(actualFrostFeeEstimate))
	c.Assert(
		scheduledSpend.LTE(spendableBalance),
		Equals,
		true,
		Commentf("scheduled=%s actual_fee=%d spendable=%s", items[0].Coin.Amount.String(), actualFrostFeeEstimate, spendableBalance.String()),
	)
}

func (*NetworkManagerTestSuite) TestGetAvailablePoolsRune(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewVaultGenesisSetupTestHelper(mgr.Keeper())
	mgr.K = helper
	networkMgr := newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())
	p := NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(common.One * 100)
	p.BalanceAsset = cosmos.NewUint(common.One * 100)
	p.Status = PoolAvailable
	c.Assert(helper.SetPool(ctx, p), IsNil)
	pools, totalLiquidity, err := getAvailablePoolsRune(ctx, networkMgr.k)
	c.Assert(err, IsNil)
	c.Assert(pools, HasLen, 1)
	c.Assert(totalLiquidity.Equal(p.BalanceRune), Equals, true)
}

func (*NetworkManagerTestSuite) TestPayPoolRewards(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewVaultGenesisSetupTestHelper(mgr.Keeper())
	mgr.K = helper
	networkMgr := newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())
	p := NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(common.One * 100)
	p.BalanceAsset = cosmos.NewUint(common.One * 100)
	p.Status = PoolAvailable
	c.Assert(helper.SetPool(ctx, p), IsNil)
	c.Assert(networkMgr.payPoolRewards(ctx, []cosmos.Uint{cosmos.NewUint(100 * common.One)}, Pools{p}), IsNil)
	helper.failToSetPool = true
	c.Assert(networkMgr.payPoolRewards(ctx, []cosmos.Uint{cosmos.NewUint(100 * common.One)}, Pools{p}), NotNil)
}

func (s *NetworkManagerTestSuite) TestSaverYieldFunc(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	net := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	mgr.Keeper().SetMimir(ctx, constants.SynthYieldCycle.String(), 5_000)

	// mint synths
	coin := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(10*common.One))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	spool := NewPool()
	spool.Asset = common.BTCAsset.GetSyntheticAsset()
	spool.BalanceAsset = coin.Amount
	spool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, spool), IsNil)

	// first pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.CalcUnits(coin.Amount)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(net.paySaverYield(ctx, common.BTCAsset, cosmos.NewUint(50*common.One)), IsNil)
	spool, err = mgr.Keeper().GetPool(ctx, spool.Asset)
	c.Assert(err, IsNil)
	c.Assert(spool.BalanceAsset.String(), Equals, "1113100000", Commentf("%d", spool.BalanceAsset.Uint64()))
}

func (s *NetworkManagerTestSuite) TestSaverYieldCall(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(500000 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	coin := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(10*common.One))
	spool := NewPool()
	spool.Asset = common.BTCAsset.GetSyntheticAsset()
	spool.BalanceAsset = coin.Amount
	spool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, spool), IsNil)

	// layer 1 pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.CalcUnits(coin.Amount)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Vault for getVaultsLiquidityRune.
	vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{pool.Asset.GetChain().String()}, []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(pool.Asset, pool.BalanceAsset))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	// test no fees collected
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)
	spool, err = mgr.Keeper().GetPool(ctx, spool.Asset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(spool.BalanceAsset.Uint64(), Equals, uint64(1_007_925_863), Commentf("%d", spool.BalanceAsset.Uint64()))

	// mgr.Keeper().SetMimir(ctx, constants.IncentiveCurve.String(), 50)
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, pool.Asset, cosmos.NewUint(50*common.One)), IsNil)
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)
	spool, err = mgr.Keeper().GetPool(ctx, spool.Asset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(spool.BalanceAsset.Uint64(), Equals, uint64(1_259_882_082), Commentf("%d", spool.BalanceAsset.Uint64()))

	// check we don't give yield when synth utilization is too high
	// add some synths
	coins := cosmos.NewCoins(cosmos.NewCoin("btc/btc", cosmos.NewInt(101*common.One))) // 51% utilization
	c.Assert(mgr.coinKeeper.MintCoins(ctx, ModuleName, coins), IsNil)
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, pool.Asset, cosmos.NewUint(50*common.One)), IsNil)
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)
	spool, err = mgr.Keeper().GetPool(ctx, spool.Asset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(spool.BalanceAsset.Uint64(), Equals, uint64(1_259_882_082), Commentf("%d", spool.BalanceAsset.Uint64()))
}

func (s *NetworkManagerTestSuite) TestRagnarokPool(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	activeVault := GetRandomVault()
	activeVault.StatusSince = ctx.BlockHeight() - 10
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(k.SetVault(ctx, activeVault), IsNil)
	retireVault := GetRandomVault()
	retireVault.Chains = common.Chains{common.ETHChain, common.BTCChain}.Strings()
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.LPUnits = cosmos.NewUint(1600)
	btcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, btcPool), IsNil)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	ethPool.LPUnits = cosmos.NewUint(1600)
	ethPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, ethPool), IsNil)
	addr := GetRandomRUNEAddress()
	lps := LiquidityProviders{
		{
			Asset:             common.BTCAsset,
			RuneAddress:       addr,
			AssetAddress:      GetRandomBTCAddress(),
			LastAddHeight:     5,
			Units:             btcPool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		},
		{
			Asset:             common.BTCAsset,
			RuneAddress:       GetRandomRUNEAddress(),
			AssetAddress:      GetRandomBTCAddress(),
			LastAddHeight:     10,
			Units:             btcPool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		},
	}
	k.SetLiquidityProvider(ctx, lps[0])
	k.SetLiquidityProvider(ctx, lps[1])
	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ctx = ctx.WithBlockHeight(1)
	// block height not correct , doesn't take any actions
	err := networkMgr.checkPoolRagnarok(ctx, mgr)
	c.Assert(err, IsNil)
	for _, a := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		tempPool, getErr := k.GetPool(ctx, a)
		c.Assert(getErr, IsNil)
		c.Assert(tempPool.Status, Equals, PoolAvailable)
	}
	interval := mgr.GetConstants().GetInt64Value(constants.FundMigrationInterval)
	// mimir didn't set , it should not take any actions
	ctx = ctx.WithBlockHeight(interval * 5)
	err = networkMgr.checkPoolRagnarok(ctx, mgr)
	c.Assert(err, IsNil)

	// happy path
	networkMgr.k.SetMimir(ctx, "RagnarokProcessNumOfLPPerIteration", 1)
	networkMgr.k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)
	// first round
	err = networkMgr.checkPoolRagnarok(ctx, mgr)
	c.Assert(err, IsNil)
	items, _ := mgr.txOutStore.GetOutboundItems(ctx)
	c.Assert(items, HasLen, 1, Commentf("%d", len(items)))

	ctx = ctx.WithBlockHeight(interval * 6)
	err = networkMgr.checkPoolRagnarok(ctx, mgr)
	c.Assert(err, IsNil)
	items, _ = mgr.txOutStore.GetOutboundItems(ctx)
	c.Assert(items, HasLen, 2, Commentf("%d", len(items)))

	tempPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(tempPool.Status, Equals, PoolStaged)

	ctx = ctx.WithBlockHeight(interval * 7)
	err = networkMgr.checkPoolRagnarok(ctx, mgr)
	c.Assert(err, IsNil)
	items, _ = mgr.txOutStore.GetOutboundItems(ctx)
	c.Assert(items, HasLen, 2, Commentf("%d", len(items)))

	tempPool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(tempPool.Status, Equals, PoolSuspended)

	tempPool, err = k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(tempPool.Status, Equals, PoolAvailable)

	// when there are none gas token pool , and it is active , gas asset token pool should not be ragnarok
	usdcPool := NewPool()
	usdcAsset, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999999")
	c.Assert(err, IsNil)
	usdcPool.Asset = usdcAsset
	usdcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	usdcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	usdcPool.LPUnits = cosmos.NewUint(1600)
	usdcPool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, usdcPool), IsNil)

	networkMgr.k.SetMimir(ctx, "RAGNAROK-ETH-ETH", 1)
	err = networkMgr.checkPoolRagnarok(ctx, mgr)
	c.Assert(err, IsNil)
	tempPool, err = k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(tempPool.Status, Equals, PoolAvailable)
}

// TestWithdrawLPsRetainsLPOnTransientError verifies that a ragnarok LP
// withdrawal which fails for a transient reason (here a halted chain) does NOT
// delete the LP's claim; the record is retained and retried once the condition
// clears. Regression for issues #2972 / #2971.
func (s *NetworkManagerTestSuite) TestWithdrawLPsRetainsLPOnTransientError(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       GetRandomRUNEAddress(),
		AssetAddress:      GetRandomBTCAddress(),
		LastAddHeight:     5,
		Units:             cosmos.NewUint(1600),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// Halt the chain so the internal withdraw handler fails validation with a
	// transient error, exercising the retain-on-error path.
	k.SetMimir(ctx, "HaltBTCChain", 1)

	done := networkMgr.withdrawLPs(ctx, pool, na, mgr)
	// Not done: the LP could not be withdrawn and must be retried later.
	c.Assert(done, Equals, false)

	// The LP record must NOT be deleted when the withdraw fails transiently.
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.Units.IsZero(), Equals, false, Commentf("LP claim must be retained on transient error"))

	// Once the halt clears, the LP is withdrawn and its record removed.
	k.SetMimir(ctx, "HaltBTCChain", 0)
	done = networkMgr.withdrawLPs(ctx, pool, na, mgr)
	c.Assert(done, Equals, true)
	got, err = k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.Units.IsZero(), Equals, true, Commentf("LP should be withdrawn once the halt clears"))
}

// TestWithdrawLPsRetainsPendingClaim verifies that an LP holding both units and
// staged pending liquidity is not reported as done after the unit withdrawal:
// the pending claim survives that withdraw untouched, so finalizing the pool at
// that point would sweep the pending accounting to the Reserve and orphan the
// provider's claim. Regression for issues #2972 / #2971.
func (s *NetworkManagerTestSuite) TestWithdrawLPsRetainsPendingClaim(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	pendingRune := cosmos.NewUint(30 * common.One)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1600)
	pool.PendingInboundRune = pendingRune
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// A dual-address LP which has units AND a staged single-sided RUNE add that
	// never found its asset pair.
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       GetRandomRUNEAddress(),
		AssetAddress:      GetRandomBTCAddress(),
		LastAddHeight:     5,
		Units:             cosmos.NewUint(1600),
		PendingRune:       pendingRune,
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// First pass: the units are withdrawn, but the pending RUNE is deliberately
	// left on the record - the pool must NOT be reported as ready to finalize.
	done := networkMgr.withdrawLPs(ctx, pool, na, mgr)
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.PendingRune.Equal(pendingRune), Equals, true, Commentf("pending claim must survive the unit withdraw, got %s", got.PendingRune))
	c.Assert(done, Equals, false, Commentf("pool must not finalize while a pending LP claim is unpaid"))

	// Second pass: the now zero-unit record takes the pending branch of the
	// withdraw handler, the claim is paid out and the record removed.
	pool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	done = networkMgr.withdrawLPs(ctx, pool, na, mgr)
	c.Assert(done, Equals, true)

	got, err = k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.Units.IsZero(), Equals, true)
	c.Assert(got.PendingRune.IsZero(), Equals, true, Commentf("pending claim must be paid out, got %s", got.PendingRune))

	// And the pool level pending accounting is cleared with it, so finalization
	// sweeps nothing that still belongs to a provider.
	pool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.PendingInboundRune.IsZero(), Equals, true, Commentf("pool pending inbound rune should be settled, got %s", pool.PendingInboundRune))
}

// TestWithdrawSaversRetainsOnTransientError verifies that a ragnarok saver
// withdrawal which fails for a transient reason (here BurnSynths disabling
// saver withdrawals outside of ragnarok) does NOT delete the saver's claim.
// Regression for issues #3006 / #2970 / #3007.
func (s *NetworkManagerTestSuite) TestWithdrawSaversRetainsOnTransientError(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	synthAsset := common.BTCAsset.GetSyntheticAsset()
	saver := LiquidityProvider{
		Asset:             synthAsset,
		RuneAddress:       common.NoAddress,
		AssetAddress:      GetRandomBTCAddress(),
		LastAddHeight:     5,
		Units:             cosmos.NewUint(100 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, saver)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// Enable BurnSynths without putting the pool into ragnarok; the internal
	// saver withdraw fails validation ("burning synths is disabled").
	k.SetMimir(ctx, constants.BurnSynths.String(), 1)

	done, err := networkMgr.withdrawSavers(ctx, pool, na, mgr)
	c.Assert(err, IsNil)
	c.Assert(done, Equals, false)

	// The saver record must be retained (not deleted without a payout).
	got, err := k.GetLiquidityProvider(ctx, synthAsset, saver.AssetAddress)
	c.Assert(err, IsNil)
	c.Assert(got.Units.IsZero(), Equals, false, Commentf("saver claim must be retained on transient error"))
}

// TestWithdrawLiquidityFatalOnRemainingRuneTransferFailure verifies that if the
// leftover pool RUNE cannot be moved to the Reserve during ragnarok
// finalization, the pool is NOT removed; the error is propagated so the
// interval is rolled back and retried. Regression for issue #3008.
func (s *NetworkManagerTestSuite) TestWithdrawLiquidityFatalOnRemainingRuneTransferFailure(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// A token (non-gas) pool with leftover RUNE but no LPs/savers to withdraw.
	usdcAsset, err := common.NewAsset("ETH.USDC-0X9999999999999999999999999999999999999999")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = usdcAsset
	pool.BalanceRune = cosmos.NewUint(500 * common.One)
	pool.BalanceAsset = cosmos.NewUint(500 * common.One)
	pool.LPUnits = cosmos.ZeroUint()
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// The Asgard module is unfunded, so moving remaining RUNE to the Reserve
	// fails. withdrawLiquidity must surface the error rather than removing the
	// pool while its RUNE is unaccounted for.
	err = networkMgr.withdrawLiquidity(ctx, pool, na, mgr)
	c.Assert(err, NotNil)
	c.Assert(k.PoolExist(ctx, usdcAsset), Equals, true, Commentf("pool must not be removed when remaining RUNE cannot be moved"))
}

// TestRagnarokLastRuneOnlyLPFinalizes covers a ragnarok whose last remaining
// liquidity provider is RUNE-address-only: withdrawing it necessarily drains
// 100% of the RUNE side while the asset side has no address to be paid to.
// Outside of ragnarok that is rejected ("cannot withdraw 100% of only one side
// of the pool"), which used to strand the LP forever - the record was retained
// on every interval and the pool never finalized. During ragnarok the provider
// must instead be paid everything it has an address for, the LP record cleared
// and the pool finalized, leaving the unreachable L1 remainder orphaned in the
// vault for the nodes to dispose of.
func (s *NetworkManagerTestSuite) TestRagnarokLastRuneOnlyLPFinalizes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	k := mgr.Keeper()
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	poolRune := cosmos.NewUint(1000 * common.One)
	poolAsset := cosmos.NewUint(10 * common.One)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = poolRune
	pool.BalanceAsset = poolAsset
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// the RUNE payout is a native txout, which needs an active vault to route on
	vault := GetRandomVault()
	vault.StatusSince = ctx.BlockHeight() - 10
	c.Assert(k.SetVault(ctx, vault), IsNil)

	runeAddr := GetRandomRUNEAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddr,
		AssetAddress:      common.NoAddress,
		LastAddHeight:     5,
		Units:             pool.LPUnits,
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)

	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.withdrawLiquidity(ctx, pool, na, mgr), IsNil)

	// the provider is paid every RUNE it is owed, at the address it does have
	lpAcc, err := runeAddr.AccAddress()
	c.Assert(err, IsNil)
	c.Check(k.GetBalance(ctx, lpAcc).AmountOf(common.RuneNative.Native()).String(), Equals, poolRune.String(),
		Commentf("the RUNE-address-only provider should be paid the whole RUNE side"))

	// the LP record clears
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, runeAddr)
	c.Assert(err, IsNil)
	c.Check(got.Units.IsZero(), Equals, true, Commentf("LP record should be cleared once paid"))

	// and ragnarok finalizes: BTC is a gas asset, so the pool is suspended
	finalPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(finalPool.Status, Equals, PoolSuspended, Commentf("ragnarok should finalize the pool"))
	c.Check(finalPool.BalanceRune.IsZero(), Equals, true)
	c.Check(finalPool.LPUnits.IsZero(), Equals, true)
	// the L1 remainder the provider has no address for is left orphaned
	c.Check(finalPool.BalanceAsset.String(), Equals, poolAsset.String(),
		Commentf("the unreachable L1 remainder should be left for manual disposition"))
}

// TestRagnarokLastAssetOnlyLPFinalizes is the mirror of
// TestRagnarokLastRuneOnlyLPFinalizes: the last provider is asset-address-only,
// so withdrawing it drains 100% of the asset side and leaves RUNE behind. The
// provider is paid its L1 asset and the leftover RUNE - a THORChain asset with
// nowhere to go - is swept to the Reserve as the pool finalizes.
func (s *NetworkManagerTestSuite) TestRagnarokLastAssetOnlyLPFinalizes(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	poolRune := cosmos.NewUint(1000 * common.One)
	poolAsset := cosmos.NewUint(10 * common.One)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = poolRune
	pool.BalanceAsset = poolAsset
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// the leftover RUNE is moved from Asgard to the Reserve on finalization
	FundModule(c, ctx, k, AsgardName, poolRune.Uint64())

	assetAddr := GetRandomBTCAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       common.NoAddress,
		AssetAddress:      assetAddr,
		LastAddHeight:     5,
		Units:             pool.LPUnits,
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(networkMgr.withdrawLiquidity(ctx, pool, na, mgr), IsNil)

	// the provider is paid the asset side (less the gas reserved for the outbound)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1, Commentf("%d", len(items)))
	c.Check(items[0].ToAddress.Equals(assetAddr), Equals, true)
	c.Check(items[0].Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(items[0].Coin.Amount.IsZero(), Equals, false)

	// the LP record clears
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, assetAddr)
	c.Assert(err, IsNil)
	c.Check(got.Units.IsZero(), Equals, true, Commentf("LP record should be cleared once paid"))

	// the leftover THORChain asset goes to the Reserve
	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(common.SafeSub(reserveAfter, reserveBefore).String(), Equals, poolRune.String(),
		Commentf("remaining pool RUNE should be swept to the Reserve"))

	// and ragnarok finalizes
	finalPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(finalPool.Status, Equals, PoolSuspended, Commentf("ragnarok should finalize the pool"))
	c.Check(finalPool.BalanceRune.IsZero(), Equals, true)
	c.Check(finalPool.LPUnits.IsZero(), Equals, true)
}

// TestRagnarokSweepsRemainingNativePoolAssetToReserve covers the finalization of
// a pool whose asset is itself a THORChain asset backed by the Pool Module (TCY
// and whitelisted base-layer pools). Any leftover - as when the last provider
// was RUNE-address-only - has to reach the Reserve, exactly as the leftover pool
// RUNE does; orphaning it behind a removed pool would leave the Pool Module
// oversolvent. An external L1 remainder has no module balance to reconcile and
// is instead left in the vault for the nodes to dispose of.
func (s *NetworkManagerTestSuite) TestRagnarokSweepsRemainingNativePoolAssetToReserve(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	remaining := cosmos.NewUint(100 * common.One)
	pool := NewPool()
	pool.Asset = common.TCY
	pool.BalanceRune = cosmos.ZeroUint()
	pool.BalanceAsset = remaining
	pool.LPUnits = cosmos.ZeroUint()
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// the pool asset is held by the Asgard (Pool) module
	coin := common.NewCoin(common.TCY, remaining)
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	k.SetMimir(ctx, "RAGNAROK-TCY-TCY", 1)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.withdrawLiquidity(ctx, pool, na, mgr), IsNil)

	c.Check(k.GetBalanceOfModule(ctx, ReserveName, common.TCY.Native()).String(), Equals, remaining.String(),
		Commentf("leftover THORChain pool asset should be swept to the Reserve"))
	c.Check(k.GetBalanceOfModule(ctx, AsgardName, common.TCY.Native()).IsZero(), Equals, true,
		Commentf("no pool asset should be left behind in the Pool Module"))
	c.Check(k.PoolExist(ctx, common.TCY), Equals, false, Commentf("ragnarok should finalize the pool"))
}

// TestRagnarokDualSidedLPDustAssetLegKeepsRuneClaim covers a dual-address LP
// whose asset leg is too small to cover the outbound it would need, while its
// RUNE leg is perfectly payable. The withdraw handler schedules the asset
// outbound first and runs inside a CacheContext, so the dust asset leg rolls the
// whole withdraw back - the RUNE leg is never even attempted. Removing the LP on
// that error would delete a payable claim and let the pool finalize with it
// unpaid, so the ragnarok has to fall back to a RUNE-only withdraw instead.
func (s *NetworkManagerTestSuite) TestRagnarokDualSidedLPDustAssetLegKeepsRuneClaim(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	k := mgr.Keeper()
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// 1.5 * 10 * 1000 = 15000 sats of max gas for a BTC outbound
	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 10)), IsNil)

	// The pool holds barely more asset than a single outbound costs in gas, so
	// the asset leg of the withdraw cannot be paid out - but the RUNE side is
	// substantial and has an address to be paid to.
	poolRune := cosmos.NewUint(100 * common.One)
	poolAsset := cosmos.NewUint(20000)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = poolRune
	pool.BalanceAsset = poolAsset
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.StatusSince = ctx.BlockHeight() - 10
	vault.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, poolAsset))
	c.Assert(k.SetVault(ctx, vault), IsNil)
	FundModule(c, ctx, k, AsgardName, poolRune.Uint64())

	runeAddr := GetRandomRUNEAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddr,
		AssetAddress:      GetRandomBTCAddress(),
		LastAddHeight:     5,
		Units:             pool.LPUnits,
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)

	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.withdrawLiquidity(ctx, pool, na, mgr), IsNil)

	// The payable RUNE claim is paid out rather than deleted with the dust.
	lpAcc, err := runeAddr.AccAddress()
	c.Assert(err, IsNil)
	c.Check(k.GetBalance(ctx, lpAcc).AmountOf(common.RuneNative.Native()).String(), Equals, poolRune.String(),
		Commentf("the RUNE leg must be paid out, not rolled back with the dust asset leg"))

	// Only once it is paid does the LP record clear...
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, runeAddr)
	c.Assert(err, IsNil)
	c.Check(got.Units.IsZero(), Equals, true, Commentf("LP record should be cleared once paid"))

	// ...and the pool finalize; the asset dust is left orphaned in the vault.
	finalPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(finalPool.Status, Equals, PoolSuspended, Commentf("ragnarok should finalize the pool"))
	c.Check(finalPool.BalanceRune.IsZero(), Equals, true)
	c.Check(finalPool.LPUnits.IsZero(), Equals, true)
}

// TestRagnarokDualSidedLPDustAssetLegPausedAsymRetainsLP is the counterpart of
// TestRagnarokDualSidedLPDustAssetLegKeepsRuneClaim: while asymmetric withdraws
// are paused for the chain, the RUNE-only fallback would be turned straight back
// into a symmetric withdraw and prove nothing about the RUNE leg. The LP must
// then be retained - never deleted unpaid - and the pool must not finalize.
func (s *NetworkManagerTestSuite) TestRagnarokDualSidedLPDustAssetLegPausedAsymRetainsLP(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	k := mgr.Keeper()
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 10)), IsNil)

	poolRune := cosmos.NewUint(100 * common.One)
	poolAsset := cosmos.NewUint(20000)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = poolRune
	pool.BalanceAsset = poolAsset
	pool.LPUnits = cosmos.NewUint(1600)
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.StatusSince = ctx.BlockHeight() - 10
	vault.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, poolAsset))
	c.Assert(k.SetVault(ctx, vault), IsNil)
	FundModule(c, ctx, k, AsgardName, poolRune.Uint64())

	runeAddr := GetRandomRUNEAddress()
	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       runeAddr,
		AssetAddress:      GetRandomBTCAddress(),
		LastAddHeight:     5,
		Units:             pool.LPUnits,
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	k.SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)
	k.SetMimir(ctx, "PauseAsymWithdrawal-BTC", 1)

	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.withdrawLiquidity(ctx, pool, na, mgr), IsNil)

	// The claim survives, unpaid but intact, to be retried in a later interval.
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, runeAddr)
	c.Assert(err, IsNil)
	c.Check(got.Units.IsZero(), Equals, false, Commentf("LP claim must be retained while it cannot be paid"))

	// And the pool does not finalize while that claim is outstanding.
	finalPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(finalPool.Status, Equals, PoolStaged, Commentf("pool must not finalize with an unpaid claim"))
	c.Check(finalPool.BalanceRune.String(), Equals, poolRune.String())

	// Once the pause lifts, the RUNE-only fallback pays the provider out.
	k.SetMimir(ctx, "PauseAsymWithdrawal-BTC", 0)
	finalPool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(networkMgr.withdrawLiquidity(ctx, finalPool, na, mgr), IsNil)
	lpAcc, err := runeAddr.AccAddress()
	c.Assert(err, IsNil)
	c.Check(k.GetBalance(ctx, lpAcc).AmountOf(common.RuneNative.Native()).String(), Equals, poolRune.String(),
		Commentf("the retained claim should be paid once the pause lifts"))
}

func (s *NetworkManagerTestSuite) TestCleanupAsgardIndex(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault1 := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault1), IsNil)
	vault2 := NewVault(1024, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault2), IsNil)
	vault3 := NewVault(1024, InitVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault3), IsNil)
	vault4 := NewVault(1024, InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault4), IsNil)
	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.cleanupAsgardIndex(ctx), IsNil)
	containsVault := func(vaults Vaults, pubKey common.PubKey) bool {
		for _, item := range vaults {
			if item.PubKey.Equals(pubKey) {
				return true
			}
		}
		return false
	}
	asgards, err := k.GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	c.Assert(containsVault(asgards, vault1.PubKey), Equals, true)
	c.Assert(containsVault(asgards, vault2.PubKey), Equals, true)
	c.Assert(containsVault(asgards, vault3.PubKey), Equals, true)
	c.Assert(containsVault(asgards, vault4.PubKey), Equals, false)
}

func (*NetworkManagerTestSuite) TestPOLLiquidityAdd(c *C) {
	ctx, mgr := setupManagerForTest(c)

	net := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	max := cosmos.NewUint(10000)

	polAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	na := GetRandomValidatorNode(NodeActive)
	signer := na.NodeAddress
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(20 * common.One)
	btcPool.LPUnits = cosmos.NewUint(1600)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// hit max
	util := cosmos.NewUint(1500)
	target := cosmos.NewUint(1000)
	c.Assert(net.addPOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr, nil), IsNil)
	lp, err := mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(7), Commentf("%d", lp.Units.Uint64()))

	// doesn't hit max
	util = cosmos.NewUint(1050)
	c.Assert(net.addPOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr, nil), IsNil)
	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(10), Commentf("%d", lp.Units.Uint64()))

	// no change needed
	util = cosmos.NewUint(1000)
	c.Assert(net.addPOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr, nil), IsNil)
	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(10), Commentf("%d", lp.Units.Uint64()))

	// not enough balance in the reserve module
	max = cosmos.NewUint(1000000)
	util = cosmos.NewUint(50_000)
	btcPool.BalanceRune = cosmos.NewUint(90000000000 * common.One)
	c.Assert(net.addPOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr, nil), IsNil)
	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(10), Commentf("%d", lp.Units.Uint64()))
}

// The rune pool value behind the reserve's new RUNEPool units is read only
// once a POL add succeeds, and still from the state before the deposit.
func (*NetworkManagerTestSuite) TestPOLLiquidityAddValueBeforeDeposit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	net := newNetworkMgr(k, NewTxStoreDummy(), NewDummyEventMgr())
	polAddress, err := k.GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	asgardAddress, err := k.GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(20 * common.One)
	btcPool.LPUnits = cosmos.NewUint(2000 * common.One)
	c.Assert(k.SetPool(ctx, btcPool), IsNil)
	k.SetLiquidityProvider(ctx, LiquidityProvider{
		Asset:       common.BTCAsset,
		RuneAddress: polAddress,
		Units:       cosmos.NewUint(500 * common.One),
	})
	k.SetRUNEPool(ctx, RUNEPool{
		PoolUnits:    cosmos.NewUint(100 * common.One),
		ReserveUnits: cosmos.NewUint(900 * common.One),
	})

	max := cosmos.NewUint(10000)
	util := cosmos.NewUint(1500)
	target := cosmos.NewUint(1000)
	runeAmt := common.GetSafeShare(max, cosmos.NewUint(1000_000), btcPool.BalanceRune)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	// a failed add leaves everything as it was and never computes the value
	k.SetMimir(ctx, fmt.Sprintf(constants.MimirTemplatePauseLPDeposit, common.BTCAsset.MimirString()), 1)
	polValue := &polValueMemo{}
	c.Assert(net.addPOLLiquidity(ctx, btcPool, polAddress, asgardAddress, na.NodeAddress, max, util, target, mgr, polValue), NotNil)
	c.Check(polValue.ok, Equals, false)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).String(), Equals, reserveBefore.String())
	runePool, err := k.GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Check(runePool.ReserveUnits.String(), Equals, cosmos.NewUint(900*common.One).String())

	// a successful add prices the reserve's units at the value before the deposit
	k.SetMimir(ctx, fmt.Sprintf(constants.MimirTemplatePauseLPDeposit, common.BTCAsset.MimirString()), 0)
	valueBefore, err := runePoolValue(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(valueBefore.IsZero(), Equals, false)
	c.Assert(net.addPOLLiquidity(ctx, btcPool, polAddress, asgardAddress, na.NodeAddress, max, util, target, mgr, polValue), IsNil)
	valueAfter, err := runePoolValue(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(valueAfter.GT(valueBefore), Equals, true, Commentf("the deposit should raise the value"))

	expected := cosmos.NewUint(900 * common.One).Add(common.GetSafeShare(runeAmt, valueBefore, cosmos.NewUint(1000*common.One)))
	runePool, err = k.GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Check(runePool.ReserveUnits.String(), Equals, expected.String())
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).String(), Equals, reserveBefore.Sub(runeAmt).String())
	c.Check(polValue.ok, Equals, false, Commentf("a committed add resets the memo"))
}

func (*NetworkManagerTestSuite) TestPOLLiquidityWithdraw(c *C) {
	ctx, mgr := setupManagerForTest(c)

	net := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	max := cosmos.NewUint(10000)

	polAddress, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	asgardAddress, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	na := GetRandomValidatorNode(NodeActive)
	signer := na.NodeAddress
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(2000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(20 * common.One)
	btcPool.LPUnits = cosmos.NewUint(1600)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	lps := LiquidityProviders{
		{
			Asset:             btcPool.Asset,
			RuneAddress:       GetRandomETHAddress(),
			AssetAddress:      GetRandomBTCAddress(),
			LastAddHeight:     5,
			Units:             btcPool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		},
		{
			Asset:             btcPool.Asset,
			RuneAddress:       polAddress,
			AssetAddress:      common.NoAddress,
			LastAddHeight:     10,
			Units:             btcPool.LPUnits.QuoUint64(2),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
		},
	}
	for _, lp := range lps {
		mgr.Keeper().SetLiquidityProvider(ctx, lp)
	}

	// hit max
	util := cosmos.NewUint(500)
	target := cosmos.NewUint(1000)
	c.Assert(net.removePOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr), IsNil)
	lp, err := mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(792), Commentf("%d", lp.Units.Uint64()))
	// To withdraw max 1% (100 basis points) of the pool RUNE depth, asymmetrically withdraw as RUNE 0.5% of all pool units.
	// 0.5% of 1600 is 8; 800 minus 8 is 792.

	// doesn't hit max
	util = cosmos.NewUint(950)
	c.Assert(net.removePOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr), IsNil)
	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(788), Commentf("%d", lp.Units.Uint64()))
	// To withdraw 0.5% of the pool RUNE depth, asymmetrically withdraw as RUNE 0.25% of all pool units.
	// 0.25% of 1592 is 3.98 which rounds to 4; 792 minus 4 is 788.

	// no change needed
	util = cosmos.NewUint(1000)
	c.Assert(net.removePOLLiquidity(ctx, btcPool, polAddress, asgardAddress, signer, max, util, target, mgr), IsNil)
	lp, err = mgr.Keeper().GetLiquidityProvider(ctx, btcPool.Asset, polAddress)
	c.Assert(err, IsNil)
	c.Check(lp.Units.Uint64(), Equals, uint64(788), Commentf("%d", lp.Units.Uint64()))
}

func (*NetworkManagerTestSuite) TestFairMergePOLCycle(c *C) {
	ctx, mgr := setupManagerForTest(c)
	net := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// cycle should do nothing when target is 0
	err := net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err := mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.Uint64(), Equals, uint64(0))
	c.Assert(pol.RuneWithdrawn.Uint64(), Equals, uint64(0))

	// cycle should error when target is greater than 0 with no node accounts
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 1000) // 10% liability
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, ErrorMatches, "dev err: no active node accounts")

	// create dummy eth pool
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	err = mgr.Keeper().SetPool(ctx, pool)
	c.Assert(err, IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	btcPool.LPUnits = cosmos.NewUint(100 * common.One)
	err = mgr.Keeper().SetPool(ctx, btcPool)
	c.Assert(err, IsNil)

	// cycle should error since there are no pol enabled pools
	err = mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive))
	c.Assert(err, IsNil)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, ErrorMatches, "no POL pools")

	// cycle should silently succeed when there is a pool enabled
	mgr.Keeper().SetMimir(ctx, "POL-ETH-ETH", 1)
	mgr.Keeper().SetMimir(ctx, "POL-BTC-BTC", 1)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)

	// pol should still be zero since there are no synths
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.Uint64(), Equals, uint64(0))
	c.Assert(pol.RuneWithdrawn.Uint64(), Equals, uint64(0))

	// add some synths
	coins := cosmos.NewCoins(
		cosmos.NewCoin("eth/eth", cosmos.NewInt(20*common.One)),
		cosmos.NewCoin("btc/btc", cosmos.NewInt(20*common.One)),
	) // 20% utilization, 10% liability
	err = mgr.coinKeeper.MintCoins(ctx, ModuleName, coins)
	c.Assert(err, IsNil)
	err = mgr.Keeper().SetPool(ctx, pool)
	c.Assert(err, IsNil)

	// synth liability should be 10%
	synthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)
	liability := common.GetUncappedShare(pool.SynthUnits, pool.GetPoolUnits(), cosmos.NewUint(10_000))
	c.Assert(liability.String(), Equals, "1000")

	// cycle should succeed, still no rune deposited since max is 0
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)

	// pol should still be zero
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "0")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "0")

	// synth liability should still be 10%
	synthSupply = mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)
	liability = common.GetUncappedShare(pool.SynthUnits, pool.GetPoolUnits(), cosmos.NewUint(10_000))
	c.Assert(liability.String(), Equals, "1000")

	// set pol utilization to 5% should deposit up to the max
	mgr.Keeper().SetMimir(ctx, constants.POLMaxNetworkDeposit.String(), common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 500)
	mgr.Keeper().SetMimir(ctx, constants.POLMaxPoolMovement.String(), 10000)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "200000000")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "0")

	// there needs to be one vault or the withdraw handler fails
	vault := NewVault(0, ActiveVault, types.VaultType_AsgardVault, GetRandomPubKey(), []string{"ETH", "BTC"}, nil)
	err = mgr.Keeper().SetVault(ctx, vault)
	c.Assert(err, IsNil)

	// synth liability should still be 10%
	synthSupply = mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)
	liability = common.GetUncappedShare(pool.SynthUnits, pool.GetPoolUnits(), cosmos.NewUint(10_000))
	c.Assert(liability.String(), Equals, "1000")

	// withdraw entire pol position
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 10000)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "200000000")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199395540")
	// only XYK constant-depths-product withdraw slip, no implicit slip fee

	// synth liability should still be 10%
	synthSupply = mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)
	liability = common.GetUncappedShare(pool.SynthUnits, pool.GetPoolUnits(), cosmos.NewUint(10_000))
	c.Assert(liability.String(), Equals, "1000")

	synthSupply = mgr.Keeper().GetTotalSupply(ctx, btcPool.Asset.GetSyntheticAsset())
	btcPool.CalcUnits(synthSupply)
	liability = common.GetUncappedShare(btcPool.SynthUnits, btcPool.GetPoolUnits(), cosmos.NewUint(10_000))
	c.Assert(liability.String(), Equals, "1000")

	// deposit entire pol position
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 500)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199395540")

	// withdraw entire pol position 1 basis point of rune depth at a time
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 10000)
	mgr.Keeper().SetMimir(ctx, constants.POLMaxPoolMovement.String(), 1)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199415528")
	// another basis point
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199435514")

	// set the buffer to 100% to stop any movement
	mgr.Keeper().SetMimir(ctx, constants.POLBuffer.String(), 10000)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199435514")

	// current liability is at 10%, so buffer at 40% and target of 50% should still not move
	mgr.Keeper().SetMimir(ctx, constants.POLBuffer.String(), 4000)
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 5000)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199435514")

	// any smaller buffer should withdraw one basis point of rune
	mgr.Keeper().SetMimir(ctx, constants.POLBuffer.String(), 3999)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "199455500")

	// withdraw everything
	mgr.Keeper().SetMimir(ctx, constants.POLTargetSynthPerPoolDepth.String(), 10000)
	mgr.Keeper().SetMimir(ctx, constants.POLBuffer.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.POLMaxPoolMovement.String(), 10000)
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "398797134")

	// should be nothing left to withdraw again
	err = net.POLCycle(ctx, mgr)
	c.Assert(err, IsNil)
	pol, err = mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneDeposited.String(), Equals, "400006044")
	c.Assert(pol.RuneWithdrawn.String(), Equals, "398797134")
}

func (s *NetworkManagerTestSuite) TestSpawnDerivedAssets(c *C) {
	ctx, mgr := setupManagerForTest(c)

	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.BSCChain.String())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	mgr.Keeper().SetMimir(ctx, "DerivedDepthBasisPts", 10_000)
	mgr.Keeper().SetMimir(ctx, "TorAnchor-ETH-BUSD-BD1", 1) // enable BUSD pool as a TOR anchor
	maxAnchorSlip := mgr.Keeper().GetConfigInt64(ctx, constants.MaxAnchorSlip)
	ethBusd, err := common.NewAsset("ETH.BUSD-BD1")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = ethBusd
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(187493559385369)
	pool.BalanceAsset = cosmos.NewUint(925681680182301)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	eth, err := common.NewAsset("ETH.ETH")
	c.Assert(err, IsNil)

	pool = NewPool()
	pool.Asset = eth
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(110119961610327)
	pool.BalanceAsset = cosmos.NewUint(2343330836117)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	bscBnb, err := common.NewAsset("BSC.BNB")
	c.Assert(err, IsNil)

	// should not have any affect on THOR.BNB
	bscPool := NewPool()
	bscPool.Asset = bscBnb
	bscPool.Status = PoolAvailable
	bscPool.BalanceRune = cosmos.NewUint(510119961610327)
	bscPool.BalanceAsset = cosmos.NewUint(4343330836117)
	bscPool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, bscPool), IsNil)

	// happy path
	err = nmgr.spawnDerivedAssets(ctx, mgr)
	c.Assert(err, IsNil)
	usd, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(usd.BalanceAsset.Uint64(), Equals, uint64(925681680182301), Commentf("%d", usd.BalanceAsset.Uint64()))
	c.Check(usd.BalanceRune.Uint64(), Equals, uint64(187493559385369), Commentf("%d", usd.BalanceRune.Uint64()))
	dbnb, _ := common.NewAsset("THOR.BNB")
	bnbPool, err := mgr.Keeper().GetPool(ctx, dbnb)
	c.Assert(err, IsNil)
	c.Check(bnbPool.BalanceAsset.Uint64(), Equals, uint64(4343330836117), Commentf("%d", bnbPool.BalanceAsset.Uint64()))
	c.Check(bnbPool.BalanceRune.Uint64(), Equals, uint64(510119961610327), Commentf("%d", bnbPool.BalanceRune.Uint64()))

	// happy path, but some trade volume triggers a lower pool depth
	newctx := ctx.WithBlockHeight(ctx.BlockHeight() - 1)
	err = mgr.Keeper().AddToSwapSlip(newctx, ethBusd, cosmos.NewInt(maxAnchorSlip/4))
	c.Assert(err, IsNil)
	err = nmgr.spawnDerivedAssets(ctx, mgr)
	c.Assert(err, IsNil)
	usd, err = mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Check(usd.Status.String(), Equals, "Available")
	c.Check(usd.BalanceAsset.Uint64(), Equals, uint64(694261260136726), Commentf("%d", usd.BalanceAsset.Uint64()))
	c.Check(usd.BalanceRune.Uint64(), Equals, uint64(140620169539027), Commentf("%d", usd.BalanceRune.Uint64()))

	// unhappy path, too much liquidity fees collected in the anchor pools, goes to 1% depth
	err = mgr.Keeper().AddToSwapSlip(newctx, ethBusd, cosmos.NewInt(10_000))
	c.Assert(err, IsNil)
	err = nmgr.spawnDerivedAssets(ctx, mgr)
	c.Assert(err, IsNil)
	usd, err = mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Assert(usd.Status.String(), Equals, "Available")
	c.Assert(usd.BalanceAsset.Uint64(), Equals, uint64(9256816801824), Commentf("%d", usd.BalanceAsset.Uint64()))
	c.Assert(usd.BalanceRune.Uint64(), Equals, uint64(1874935593854), Commentf("%d", usd.BalanceRune.Uint64()))
	// ensure layer1 bnb pool is NOT suspended
	bnbPool, err = mgr.Keeper().GetPool(ctx, ethBusd)
	c.Assert(err, IsNil)
	c.Assert(bnbPool.Status.String(), Equals, "Available")
	c.Assert(bnbPool.BalanceAsset.Uint64(), Equals, uint64(925681680182301), Commentf("%d", bnbPool.BalanceAsset.Uint64()))
	c.Assert(bnbPool.BalanceRune.Uint64(), Equals, uint64(187493559385369), Commentf("%d", bnbPool.BalanceRune.Uint64()))
}

func (s *NetworkManagerTestSuite) TestSpawnDerivedAssetsBasisPoints(c *C) {
	ctx, mgr := setupManagerForTest(c)

	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	vault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	mgr.Keeper().SetMimir(ctx, "TorAnchor-ETH-BUSD-BD1", 1) // enable ETH.BUSD pool as a TOR anchor
	ethBusd, err := common.NewAsset("ETH.BUSD-BD1")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = ethBusd
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(187493559385369)
	pool.BalanceAsset = cosmos.NewUint(925681680182301)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// test that DerivedDepthBasisPts affects the pool depth
	mgr.Keeper().SetMimir(ctx, "DerivedDepthBasisPts", 20000)
	err = nmgr.spawnDerivedAssets(ctx, mgr)
	c.Assert(err, IsNil)
	usd, err := mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Assert(usd.Status.String(), Equals, "Available")
	c.Check(usd.BalanceAsset.Uint64(), Equals, uint64(1851363360364602), Commentf("%d", usd.BalanceAsset.Uint64()))
	c.Check(usd.BalanceRune.Uint64(), Equals, uint64(374987118770738), Commentf("%d", usd.BalanceRune.Uint64()))

	// test that DerivedDepthBasisPts set to zero will cause the pools to
	// become suspended
	mgr.Keeper().SetMimir(ctx, "DerivedDepthBasisPts", 0)
	err = nmgr.spawnDerivedAssets(ctx, mgr)
	c.Assert(err, IsNil)
	usd, err = mgr.Keeper().GetPool(ctx, common.TOR)
	c.Assert(err, IsNil)
	c.Assert(usd.Status.String(), Equals, "Suspended")
	c.Assert(usd.BalanceAsset.Uint64(), Equals, uint64(1851363360364602), Commentf("%d", usd.BalanceAsset.Uint64()))
	c.Assert(usd.BalanceRune.Uint64(), Equals, uint64(374987118770738), Commentf("%d", usd.BalanceRune.Uint64()))
}

func (s *NetworkManagerTestSuite) TestFetchMeanSlip(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	asset := common.BTCAsset

	var slip int64
	var err error
	slip = nmgr.fetchWeightedMeanSlip(ctx, asset, mgr)
	c.Check(slip, Equals, int64(0))

	// setup slip history
	ctx = ctx.WithBlockHeight(14400 * 14)
	maxAnchorBlocks := mgr.Keeper().GetConfigInt64(ctx, constants.MaxAnchorBlocks)
	dynamicMaxAnchorSlipBlocks := mgr.Keeper().GetConfigInt64(ctx, constants.DynamicMaxAnchorSlipBlocks)
	for i := ctx.BlockHeight(); i > ctx.BlockHeight()-dynamicMaxAnchorSlipBlocks; i -= maxAnchorBlocks {
		if i <= 0 {
			break // dynamicMaxAnchorSlipBlocks > ctx.BlockHeight, end of chain history
		}

		mgr.Keeper().SetSwapSlipSnapShot(ctx, asset, i, i)
	}

	// mean slip will be 0 if the asset has no available pools
	slip = nmgr.fetchWeightedMeanSlip(ctx, asset, mgr)
	c.Check(slip, Equals, int64(0))
	slip, err = mgr.Keeper().GetLongRollup(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(slip, Equals, int64(0))

	// create corresponding pool
	pool := NewPool()
	pool.Asset = asset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// mean slip is available after pool creation, should set long rollup
	slip = nmgr.fetchWeightedMeanSlip(ctx, asset, mgr)
	c.Check(slip, Equals, int64(100950))
	slip, err = mgr.Keeper().GetLongRollup(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(slip, Equals, int64(100950))
}

func (s *NetworkManagerTestSuite) TestDistributeTCYStake(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.K.SetMimir(ctx, "TCYStakeDistributionHalt", 0)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)

	address1 := GetRandomRUNEAddress()
	acc1, err := address1.AccAddress()
	c.Assert(err, IsNil)
	address2 := GetRandomRUNEAddress()
	acc2, err := address2.AccAddress()
	c.Assert(err, IsNil)
	address3 := GetRandomRUNEAddress()
	acc3, err := address3.AccAddress()
	c.Assert(err, IsNil)
	address4 := GetRandomRUNEAddress()
	acc4, err := address4.AccAddress()
	c.Assert(err, IsNil)
	tcyStakeAddress := mgr.Keeper().GetModuleAccAddress(TCYStakeName)

	// Add TCYStaker to accounts -> acc1 ~ 75%, acc2 ~ 25%, acc4 = less than MinTCYForTCYStakeDistribution
	amountAddr1 := cosmos.NewUint(157_499_999_99950000)
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address1,
		Amount:  amountAddr1,
	})
	c.Assert(err, IsNil)

	amountAddr2 := cosmos.NewUint(52_499_999_99950001)
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address2,
		Amount:  amountAddr2,
	})
	c.Assert(err, IsNil)

	// Should be deleted since won't have amount this first run
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address3,
		Amount:  cosmos.ZeroUint(),
	})
	c.Assert(err, IsNil)

	// Set staking amount less than MinTCYForTCYStakeDistribution to address 4
	amt := constAccessor.GetInt64Value(constants.MinTCYForTCYStakeDistribution) - 1
	amountAddr4 := cosmos.NewUint(uint64(amt))
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address4,
		Amount:  amountAddr4,
	})
	c.Assert(err, IsNil)

	// Send staking amounts from address 1, 2 and 4 to tcy staking module
	stakingModuleTCYAmount := amountAddr1.Add(amountAddr2).Add(amountAddr4)
	coin := common.NewCoin(common.TCY, stakingModuleTCYAmount)
	err = mgr.Keeper().MintToModule(ctx, ModuleName, coin)
	c.Assert(err, IsNil)
	err = mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, TCYStakeName, common.NewCoins(coin))
	c.Assert(err, IsNil)

	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address1), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address2), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address3), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address4), Equals, true)

	// Mint less than MinRuneForTCYStakeDistribution on TCYStakeName
	tcyStakeFeeAmount := constAccessor.GetInt64Value(constants.MinRuneForTCYStakeDistribution) - 1
	FundModule(c, ctx, mgr.Keeper(), TCYStakeName, uint64(tcyStakeFeeAmount))

	nmgr.distributeTCYStake(ctx, mgr)

	// Check balances, accounts should not receive funds from TCYStake since funds
	// are less than MinRuneForTCYStakeDistribution
	balanceAcc1 := mgr.Keeper().GetBalanceOf(ctx, acc1, common.RuneNative)
	c.Assert(balanceAcc1.IsZero(), Equals, true)
	balanceAcc2 := mgr.Keeper().GetBalanceOf(ctx, acc2, common.RuneNative)
	c.Assert(balanceAcc2.IsZero(), Equals, true)
	balanceAcc3 := mgr.Keeper().GetBalanceOf(ctx, acc3, common.RuneNative)
	c.Assert(balanceAcc3.IsZero(), Equals, true)
	balanceAcc4 := mgr.Keeper().GetBalanceOf(ctx, acc4, common.RuneNative)
	c.Assert(balanceAcc4.IsZero(), Equals, true)

	balanceTCY := mgr.Keeper().GetBalanceOf(ctx, tcyStakeAddress, common.RuneNative)
	c.Assert(balanceTCY.Amount.Equal(math.NewInt(tcyStakeFeeAmount)), Equals, true)
	c.Assert(balanceTCY.Denom, Equals, common.RuneNative.Native())

	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address1), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address2), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address3), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address4), Equals, true)

	// Mint 210M RUNE to TCYStakeName (fund already has MinRuneForTCYStakeDistribution)
	tcyStakeFeeAmount = 210_000_000_00000000 - tcyStakeFeeAmount
	c.Assert(tcyStakeFeeAmount > 0, Equals, true)
	FundModule(c, ctx, mgr.Keeper(), TCYStakeName, uint64(tcyStakeFeeAmount))
	balanceTCY = mgr.Keeper().GetBalanceOf(ctx, tcyStakeAddress, common.RuneNative)
	c.Assert(balanceTCY.Amount.Equal(math.NewInt(210_000_000_00000000)), Equals, true)

	nmgr.distributeTCYStake(ctx, mgr)

	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address1), Equals, true)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address2), Equals, true)
	// Staking for address 3 should be deleted
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address3), Equals, false)
	c.Assert(mgr.Keeper().TCYStakerExists(ctx, address4), Equals, true)

	// Check balances, accounts should have their corresponding part: 75% to acc1,
	// 25% to acc2, 0% to acc4 and claiming the corresponding part of acc4.
	// TCYStake should not have funds after the distribution
	balanceAcc1 = mgr.Keeper().GetBalanceOf(ctx, acc1, common.RuneNative)
	c.Assert(balanceAcc1.Amount.Equal(math.NewInt(157_499_999_99950000)), Equals, true)
	c.Assert(balanceAcc1.Denom, Equals, common.RuneNative.Native())

	balanceAcc2 = mgr.Keeper().GetBalanceOf(ctx, acc2, common.RuneNative)
	c.Assert(balanceAcc2.Amount.Equal(math.NewInt(52_499_999_99950001)), Equals, true)
	c.Assert(balanceAcc2.Denom, Equals, common.RuneNative.Native())

	balanceAcc4 = mgr.Keeper().GetBalanceOf(ctx, acc4, common.RuneNative)
	c.Assert(balanceAcc4.IsZero(), Equals, true)

	balanceClaiming := mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.RuneNative.Native())
	c.Assert(balanceClaiming.Equal(math.NewUint(99999)), Equals, true)

	balanceTCY = mgr.Keeper().GetBalanceOf(ctx, tcyStakeAddress, common.RuneNative)
	c.Assert(balanceTCY.Amount.IsZero(), Equals, true)

	// Move acc1, acc2 and claiming module RUNE balances to zero
	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(balanceAcc1.Amount.Uint64()))
	err = mgr.Keeper().SendFromAccountToModule(ctx, acc1, ModuleName, common.NewCoins(coin))
	c.Assert(err, IsNil)
	balanceAcc1 = mgr.Keeper().GetBalanceOf(ctx, acc1, common.RuneNative)
	c.Assert(balanceAcc1.Amount.IsZero(), Equals, true)

	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(balanceAcc2.Amount.Uint64()))
	err = mgr.Keeper().SendFromAccountToModule(ctx, acc2, ModuleName, common.NewCoins(coin))
	c.Assert(err, IsNil)
	balanceAcc2 = mgr.Keeper().GetBalanceOf(ctx, acc2, common.RuneNative)
	c.Assert(balanceAcc2.Amount.IsZero(), Equals, true)

	coin = common.NewCoin(common.RuneNative, cosmos.NewUint(balanceClaiming.Uint64()))
	err = mgr.Keeper().SendFromModuleToModule(ctx, TCYClaimingName, ModuleName, common.NewCoins(coin))
	c.Assert(err, IsNil)
	balanceClaiming = mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.RuneNative.Native())
	c.Assert(balanceClaiming.IsZero(), Equals, true)

	// Change distribution to acc1 = 50%, acc2 = 25% and acc4 = 25%
	// remove amountAddr4
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address1,
		Amount:  math.NewUint(105_000_000_00000000),
	})
	c.Assert(err, IsNil)
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address2,
		Amount:  math.NewUint(52_500_000_00000000),
	})
	c.Assert(err, IsNil)
	err = mgr.Keeper().SetTCYStaker(ctx, TCYStaker{
		Address: address3,
		Amount:  math.NewUint(52_500_000_00000000),
	})
	c.Assert(err, IsNil)

	// Mint 420M RUNE to TCYStakeName
	tcyStakeFeeAmount = 420_000_000_00000000
	FundModule(c, ctx, mgr.Keeper(), TCYStakeName, uint64(tcyStakeFeeAmount))

	nmgr.distributeTCYStake(ctx, mgr)

	// Check balances, accounts should have their corresponding part: 50% to acc1,
	// 25% to acc2, 25% to acc3 and acc4 should not receive rune.
	// TCYStake should not have funds after the distribution
	balanceAcc1 = mgr.Keeper().GetBalanceOf(ctx, acc1, common.RuneNative)
	c.Assert(balanceAcc1.Amount.Equal(math.NewInt(210_000_000_00000000)), Equals, true)
	c.Assert(balanceAcc1.Denom, Equals, common.RuneNative.Native())

	balanceAcc2 = mgr.Keeper().GetBalanceOf(ctx, acc2, common.RuneNative)
	c.Assert(balanceAcc2.Amount.Equal(math.NewInt(105_000_000_00000000)), Equals, true)
	c.Assert(balanceAcc2.Denom, Equals, common.RuneNative.Native())

	balanceAcc3 = mgr.Keeper().GetBalanceOf(ctx, acc3, common.RuneNative)
	c.Assert(balanceAcc3.Amount.Equal(math.NewInt(105_000_000_00000000)), Equals, true)
	c.Assert(balanceAcc3.Denom, Equals, common.RuneNative.Native())

	balanceAcc4 = mgr.Keeper().GetBalanceOf(ctx, acc4, common.RuneNative)
	c.Assert(balanceAcc4.IsZero(), Equals, true)

	balanceTCY = mgr.Keeper().GetBalanceOf(ctx, tcyStakeAddress, common.RuneNative)
	c.Assert(balanceTCY.Amount.IsZero(), Equals, true)
}

func (s *NetworkManagerTestSuite) TestGetTCYStakeAmountToDistribute(c *C) {
	_, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	minRune := constAccessor.GetInt64Value(constants.MinRuneForTCYStakeDistribution)

	// If funds are less than MinRuneForTCYStakeDistribution it should NOT distribute that amount
	amount := uint64(minRune - 1)
	tcyStakeAmount := cosmos.NewUint(amount)
	result := nmgr.getTCYStakeAmountToDistribute(tcyStakeAmount, minRune)
	c.Assert(result.IsZero(), Equals, true)

	// If funds are equal to MinRuneForTCYStakeDistribution it should distribute that amount
	amount = uint64(minRune)
	tcyStakeAmount = cosmos.NewUint(amount)
	result = nmgr.getTCYStakeAmountToDistribute(tcyStakeAmount, minRune)
	c.Assert(result.IsZero(), Equals, false)
	c.Assert(result.Equal(tcyStakeAmount), Equals, true)

	// If funds are equal to 2x MinRuneForTCYStakeDistribution it should distribute that amount
	amount = uint64(minRune * 2)
	tcyStakeAmount = cosmos.NewUint(amount)
	result = nmgr.getTCYStakeAmountToDistribute(tcyStakeAmount, minRune)
	c.Assert(result.IsZero(), Equals, false)
	c.Assert(result.Equal(tcyStakeAmount), Equals, true)

	// If funds are equal to 2.5x MinRuneForTCYStakeDistribution it should only distribute 2x
	amount = uint64(float64(minRune) * 2.5)
	amoutMul2 := uint64(minRune * 2)
	tcyStakeAmount = cosmos.NewUint(amount)
	result = nmgr.getTCYStakeAmountToDistribute(tcyStakeAmount, minRune)
	c.Assert(result.IsZero(), Equals, false)
	c.Assert(result.Equal(cosmos.NewUint(amoutMul2)), Equals, true)
}

func (s *NetworkManagerTestSuite) TestCalculateNetworkSolvency(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create a pool with some balance
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.PendingInboundAsset = cosmos.NewUint(10 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create an active vault with funds
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Test 1: Calculate solvency - should show over-solvency
	// Vault has 150, Pool needs 100 + 10 pending = 110, so 40 over-solvent
	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	c.Assert(solvency[0].Asset.Equals(common.BTCAsset), Equals, true)
	expectedOverSolvency := math.NewInt(40 * common.One)
	c.Assert(solvency[0].Amount.Equal(expectedOverSolvency), Equals, true)

	// Test 2: Under-solvency scenario
	// Reduce vault balance so it's under-solvent
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	expectedUnderSolvency := math.NewInt(-60 * common.One)
	c.Assert(solvency[0].Amount.Equal(expectedUnderSolvency), Equals, true)

	// Test 3: Multiple assets
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(200 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(200 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(110*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(250*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 2)

	// Test 4: With swap queue items
	swapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One))},
		},
		TargetAsset: common.RuneAsset(),
	}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, swapMsg, 0), IsNil)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 2)
	// Should subtract the swap queue amount from vault balance

	// Test 5: Empty vaults
	vault.Coins = common.Coins{}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	// All should be under-solvent (negative)
	for _, s := range solvency {
		c.Assert(s.Amount.IsNegative(), Equals, true)
	}

	// Test 6: All THORChain native assets should be excluded (synths, derived, whitelisted)
	// Create a synth pool (BTC/BTC)
	synthBTC := common.BTCAsset.GetSyntheticAsset()
	synthPool := NewPool()
	synthPool.Asset = synthBTC
	synthPool.BalanceAsset = cosmos.NewUint(80 * common.One)
	synthPool.BalanceRune = cosmos.NewUint(80 * common.One)
	synthPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, synthPool), IsNil)

	// Create a derived asset pool (THOR.BTC)
	derivedBTCAsset, err := common.NewAsset("THOR.BTC")
	c.Assert(err, IsNil)
	derivedPool := NewPool()
	derivedPool.Asset = derivedBTCAsset
	derivedPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	derivedPool.BalanceRune = cosmos.NewUint(100 * common.One)
	derivedPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, derivedPool), IsNil)

	// Create a whitelisted native asset pool (RUJI)
	rujiPool := NewPool()
	rujiPool.Asset = common.RUJI
	rujiPool.BalanceAsset = cosmos.NewUint(50 * common.One)
	rujiPool.BalanceRune = cosmos.NewUint(50 * common.One)
	rujiPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, rujiPool), IsNil)

	// Reset vault with only L1 BTC and ETH
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(110*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	// Should only have BTC and ETH, excluding all THORChain native assets
	c.Assert(len(solvency), Equals, 2)
	for _, s := range solvency {
		c.Assert(s.Asset.IsNative(), Equals, false, Commentf("THORChain native asset %s should not be in solvency", s.Asset))
		c.Assert(s.Asset.Equals(common.BTCAsset) || s.Asset.Equals(common.ETHAsset), Equals, true)
	}
}

func (s *NetworkManagerTestSuite) TestCalculateNetworkSolvencySubtractsActiveVaultDeficit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	fundedVault := GetRandomVault()
	fundedVault.Type = AsgardVault
	fundedVault.Status = ActiveVault
	fundedVault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, fundedVault), IsNil)

	deficitVault := GetRandomVault()
	deficitVault.Type = AsgardVault
	deficitVault.Status = ActiveVault
	deficitVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, deficitVault), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(solvency, HasLen, 1)
	c.Assert(solvency[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(solvency[0].Amount.Equal(math.NewInt(30*common.One)), Equals, true)
}

func (s *NetworkManagerTestSuite) TestCalculateNetworkSolvencyIncludesInactiveDeficitAfterIndexCleanup(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	fundedVault := GetRandomVault()
	fundedVault.Type = AsgardVault
	fundedVault.Status = ActiveVault
	fundedVault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, fundedVault), IsNil)

	inactiveVault := GetRandomVault()
	inactiveVault.Type = AsgardVault
	inactiveVault.Status = InactiveVault
	inactiveVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, inactiveVault), IsNil)
	c.Assert(nmgr.cleanupAsgardIndex(ctx), IsNil)

	indexedVaults, err := mgr.Keeper().GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	for _, vault := range indexedVaults {
		c.Assert(vault.PubKey.Equals(inactiveVault.PubKey), Equals, false)
	}

	persistedVault, err := mgr.Keeper().GetVault(ctx, inactiveVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(persistedVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(20*common.One)), Equals, true)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(solvency, HasLen, 1)
	c.Assert(solvency[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(solvency[0].Amount.Equal(math.NewInt(30*common.One)), Equals, true)
}

func (s *NetworkManagerTestSuite) TestProcessOverSolvencyDoesNotSweepInactiveVaultDeficit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	ctx = ctx.WithBlockHeight(432000)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 432000)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), int64(constants.MaxBasisPts))
	c.Assert(mgr.Keeper().AdvSwapQueueEnabled(ctx), Equals, true)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	fundedVault := GetRandomVault()
	fundedVault.Type = AsgardVault
	fundedVault.Status = ActiveVault
	fundedVault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(110*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, fundedVault), IsNil)

	deficitVault := GetRandomVault()
	deficitVault.Type = AsgardVault
	deficitVault.Status = InactiveVault
	deficitVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, deficitVault), IsNil)
	c.Assert(nmgr.cleanupAsgardIndex(ctx), IsNil)

	c.Assert(nmgr.processOverSolvency(ctx, mgr), IsNil)

	iterator := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer iterator.Close()
	c.Assert(iterator.Valid(), Equals, false)
}

func (s *NetworkManagerTestSuite) TestCalculateNetworkSolvencyWithStreamingSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create a pool with some balance
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One) // Pool has 100 BTC from LPs + processed streaming swaps
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create an active vault with funds
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)), // Vault has 150 BTC (includes 50 BTC streaming deposit)
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Test 1: Non-streaming swap - should deduct full amount
	txID := GetRandomTxHash()
	swapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    txID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))},
		},
		TargetAsset: common.RuneAsset(),
	}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, swapMsg, 0), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	// Vault: 150, Pool: 100, SwapQueue: 10 → Solvency: 150 - 100 - 10 = 40
	c.Assert(solvency[0].Amount.Equal(math.NewInt(40*common.One)), Equals, true,
		Commentf("Non-streaming swap should deduct full amount, expected 40, got %s", solvency[0].Amount))

	// Clean up non-streaming swap
	mgr.Keeper().RemoveSwapQueueItem(ctx, txID, 0)

	// Test 2: Legacy streaming swap with partial progress - should only deduct remaining amount
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One))}, // Full deposit: 50 BTC
		},
		TargetAsset:    common.RuneAsset(),
		StreamInterval: 10, // Makes it a legacy streaming swap (V1)
		Version:        types.SwapVersion_v1,
	}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, streamingSwapMsg, 0), IsNil)

	// Create streaming swap state showing 20 BTC has been processed
	streamingSwap := types.NewStreamingSwap(streamingTxID, 5, 10, cosmos.ZeroUint(), cosmos.NewUint(50*common.One))
	streamingSwap.In = cosmos.NewUint(20 * common.One) // 20 BTC processed, 30 BTC remaining
	mgr.Keeper().SetStreamingSwap(ctx, streamingSwap)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	// Vault: 150, Pool: 100 (includes 20 BTC from processed streaming swap), SwapQueue: 30 (remaining)
	// Solvency: 150 - 100 - 30 = 20
	// If we incorrectly deducted full deposit: 150 - 100 - 50 = 0 (wrong!)
	c.Assert(solvency[0].Amount.Equal(math.NewInt(20*common.One)), Equals, true,
		Commentf("Streaming swap should only deduct remaining amount, expected 20, got %s", solvency[0].Amount))

	// Clean up
	mgr.Keeper().RemoveSwapQueueItem(ctx, streamingTxID, 0)
	mgr.Keeper().RemoveStreamingSwap(ctx, streamingTxID)

	// Test 3: Advanced swap queue with streaming swap (V2)
	advStreamingTxID := GetRandomTxHash()
	advStreamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    advStreamingTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(30*common.One))}, // Full deposit: 30 BTC
		},
		TargetAsset: common.RuneAsset(),
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10, // Makes it a streaming swap (State.Quantity > 1)
			Count:    3,
			Interval: 5,
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, advStreamingSwapMsg), IsNil)

	// Create streaming swap state showing 12 BTC has been processed
	advStreamingSwap := types.NewStreamingSwap(advStreamingTxID, 10, 5, cosmos.ZeroUint(), cosmos.NewUint(30*common.One))
	advStreamingSwap.In = cosmos.NewUint(12 * common.One) // 12 BTC processed, 18 BTC remaining
	mgr.Keeper().SetStreamingSwap(ctx, advStreamingSwap)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	// Vault: 150, Pool: 100, AdvSwapQueue: 18 (remaining)
	// Solvency: 150 - 100 - 18 = 32
	c.Assert(solvency[0].Amount.Equal(math.NewInt(32*common.One)), Equals, true,
		Commentf("V2 streaming swap should only deduct remaining amount, expected 32, got %s", solvency[0].Amount))

	// Clean up
	c.Assert(mgr.Keeper().RemoveAdvSwapQueueItem(ctx, advStreamingTxID, 0), IsNil)
	mgr.Keeper().RemoveStreamingSwap(ctx, advStreamingTxID)

	// Test 4: Streaming swap with no progress yet - should deduct full amount
	newStreamingTxID := GetRandomTxHash()
	newStreamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    newStreamingTxID,
			Chain: common.BTCChain,
			Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(25*common.One))},
		},
		TargetAsset:    common.RuneAsset(),
		StreamInterval: 10,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, newStreamingSwapMsg, 0), IsNil)

	// Create streaming swap state with zero progress
	newStreamingSwap := types.NewStreamingSwap(newStreamingTxID, 5, 10, cosmos.ZeroUint(), cosmos.NewUint(25*common.One))
	// In is already zero from NewStreamingSwap
	mgr.Keeper().SetStreamingSwap(ctx, newStreamingSwap)

	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	// Vault: 150, Pool: 100, SwapQueue: 25 (full, since In=0)
	// Solvency: 150 - 100 - 25 = 25
	c.Assert(solvency[0].Amount.Equal(math.NewInt(25*common.One)), Equals, true,
		Commentf("Streaming swap with no progress should deduct full amount, expected 25, got %s", solvency[0].Amount))
}

func (s *NetworkManagerTestSuite) TestSolvencyAdvancedSwapInputProgress(c *C) {
	for _, tc := range []struct {
		name      string
		deposit   uint64
		processed uint64
		quantity  uint64
	}{
		{name: "unprocessed single swap", deposit: 10, quantity: 1},
		{name: "completed single swap", deposit: 10, processed: 10, quantity: 1},
		{name: "partly processed stream", deposit: 10, processed: 4, quantity: 5},
		{name: "single swap without deposit state", quantity: 1},
		{name: "stream without deposit state or legacy record", quantity: 5},
	} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint((100 + tc.processed) * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		vault := GetRandomVault()
		// All 10 BTC of input has arrived; only processed input belongs to the pool.
		vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(110*common.One)))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		msg := types.MsgSwap{
			Tx: common.Tx{
				ID:    GetRandomTxHash(),
				Chain: common.BTCChain,
				Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))),
			},
			TargetAsset: common.RuneAsset(),
			Version:     types.SwapVersion_v2,
			State: &types.SwapState{
				Quantity: tc.quantity,
				Deposit:  cosmos.NewUint(tc.deposit * common.One),
				In:       cosmos.NewUint(tc.processed * common.One),
				Out:      cosmos.ZeroUint(),
			},
		}
		c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
		nmgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
		solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		c.Assert(solvency, HasLen, 1)
		c.Assert(solvency[0].Asset.Equals(common.BTCAsset), Equals, true)
		c.Assert(solvency[0].Amount.IsZero(), Equals, true, Commentf("%s: got %s", tc.name, solvency[0].Amount))
	}
}

func (s *NetworkManagerTestSuite) TestSolvencyAdvancedSwapMissingState(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(432000)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 432000)
	k.SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), int64(constants.MaxBasisPts))
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	vault := GetRandomVault()
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(110*common.One)))
	c.Assert(k.SetVault(ctx, vault), IsNil)
	msg := types.MsgSwap{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.BTCChain,
			Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))),
		},
		TargetAsset: common.RuneAsset(),
		Version:     types.SwapVersion_v2,
	}
	// A decodable record can omit State. Do not guess its liabilities or panic.
	c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
	nmgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, ErrorMatches, "missing state for advanced swap .*/0")
	c.Assert(solvency, IsNil)
	c.Assert(nmgr.processOverSolvency(ctx, mgr), ErrorMatches, "failed to calculate network solvency: missing state for advanced swap .*/0")
	c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 1)
}

func (s *NetworkManagerTestSuite) TestSolvencyStreamingSwapTargetOutput(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create ETH and BTC pools
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(80 * common.One) // Pool already emitted 20 BTC to streaming swap output
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create an active vault
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(150*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Create a V2 streaming swap ETH -> BTC with accumulated output
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.ETHChain,
			Coins: common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))}, // 50 ETH deposit
		},
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10,
			Count:    6,
			Interval: 5,
			Deposit:  cosmos.NewUint(50 * common.One),
			In:       cosmos.NewUint(30 * common.One), // 30 ETH processed
			Out:      cosmos.NewUint(20 * common.One), // 20 BTC emitted from pool
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)

	// No legacy SetStreamingSwap here — this test must exercise the V2 msg.State path only.

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 2)

	// Find ETH and BTC solvency
	var ethSolvency, btcSolvency math.Int
	for _, s := range solvency {
		if s.Asset.Equals(common.ETHAsset) {
			ethSolvency = s.Amount
		}
		if s.Asset.Equals(common.BTCAsset) {
			btcSolvency = s.Amount
		}
	}

	// ETH: Vault 150 - Pool 100 - SwapQueueRemaining 20 = 30
	c.Assert(ethSolvency.Equal(math.NewInt(30*common.One)), Equals, true,
		Commentf("ETH solvency: expected 30, got %s", ethSolvency))

	// BTC: Vault 100 - Pool 80 - StreamingOutput 20 = 0
	// Without the fix, it would be: Vault 100 - Pool 80 = 20 (incorrect over-solvency)
	c.Assert(btcSolvency.Equal(math.NewInt(0)), Equals, true,
		Commentf("BTC solvency: expected 0 (streaming output subtracted), got %s", btcSolvency))
}

// TestSolvencyStreamingSwapTargetOutputFromNativeSource reproduces the case
// where a V2 streaming swap starts from native RUNE and emits external-chain
// target assets. The current implementation skips queue processing too early
// for native sources, so the emitted target liability is not deducted.
func (s *NetworkManagerTestSuite) TestSolvencyStreamingSwapTargetOutputFromNativeSource(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// The BTC pool has already emitted 20 BTC to the in-flight stream, but the
	// vault still holds the coins because settleSwap has not created an outbound yet.
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(80 * common.One) // Pool already emitted 20 BTC
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Intentionally rely on inline msg.State only. This exercises the V2 path the
	// MR 4640 changed, without falling back to the legacy streaming swap record.
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.THORChain,
			Coins: common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(50*common.One))},
		},
		TargetAsset: common.BTCAsset,
		SwapType:    types.SwapType_market,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10,
			Count:    6,
			Interval: 5,
			Deposit:  cosmos.NewUint(50 * common.One),
			In:       cosmos.NewUint(30 * common.One),
			Out:      cosmos.NewUint(20 * common.One),
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)
	c.Assert(solvency[0].Asset.Equals(common.BTCAsset), Equals, true)

	// BTC: Vault 100 - Pool 80 - StreamingOutput 20 = 0
	c.Assert(solvency[0].Amount.Equal(math.ZeroInt()), Equals, true,
		Commentf("BTC solvency: expected 0 when native-source streaming output is deducted, got %s", solvency[0].Amount))

	// Older advanced streams may have all progress in the legacy record. Its
	// output must still be counted before the native source asset is skipped.
	legacy := types.NewStreamingSwap(streamingTxID, 10, 5, cosmos.ZeroUint(), streamingSwapMsg.State.Deposit)
	legacy.In, legacy.Out = streamingSwapMsg.State.In, streamingSwapMsg.State.Out
	mgr.Keeper().SetStreamingSwap(ctx, legacy)
	streamingSwapMsg.State.Deposit = cosmos.ZeroUint()
	streamingSwapMsg.State.In = cosmos.ZeroUint()
	streamingSwapMsg.State.Out = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)
	solvency, err = nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(solvency, HasLen, 1)
	c.Assert(solvency[0].Amount.IsZero(), Equals, true)
}

// TestSolvencyStreamingSwapTargetSynthOutput reproduces the case where a
// streaming swap target is synth. Synth output should not create an external
// Layer1 liability, because the swap path mints synths without reducing the L1
// pool asset depth.
func (s *NetworkManagerTestSuite) TestSolvencyStreamingSwapTargetSynthOutput(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Keep the BTC pool unchanged: synth output should not reduce L1 BTC depth.
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One) // Synth output should not reduce this
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(150*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Again, rely on inline msg.State only so this test covers the new V2
	// streaming solvency accounting path directly.
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.ETHChain,
			Coins: common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))},
		},
		TargetAsset: common.BTCAsset.GetSyntheticAsset(),
		SwapType:    types.SwapType_market,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10,
			Count:    6,
			Interval: 5,
			Deposit:  cosmos.NewUint(50 * common.One),
			In:       cosmos.NewUint(30 * common.One),
			Out:      cosmos.NewUint(20 * common.One),
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 2)

	var ethSolvency, btcSolvency math.Int
	for _, s := range solvency {
		if s.Asset.Equals(common.ETHAsset) {
			ethSolvency = s.Amount
		}
		if s.Asset.Equals(common.BTCAsset) {
			btcSolvency = s.Amount
		}
	}

	// ETH: Vault 150 - Pool 100 - SwapQueueRemaining 20 = 30
	c.Assert(ethSolvency.Equal(math.NewInt(30*common.One)), Equals, true,
		Commentf("ETH solvency: expected 30, got %s", ethSolvency))

	// BTC synth output should not create an L1 BTC liability.
	c.Assert(btcSolvency.Equal(math.ZeroInt()), Equals, true,
		Commentf("BTC solvency: expected 0 when synth output is ignored for L1 solvency, got %s", btcSolvency))
}

// TestSolvencyStreamingSwapNativeSourceTargetOutput verifies that RUNE -> L1 streaming swaps
// still subtract accumulated target output even though the source asset (RUNE) is native.
func (s *NetworkManagerTestSuite) TestSolvencyStreamingSwapNativeSourceTargetOutput(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: BTC pool where BalanceAsset is already reduced by streaming swap output
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(80 * common.One) // Pool emitted 20 BTC
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Vault holds 100 BTC
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// V2 streaming swap: RUNE -> BTC (native source, L1 target)
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.THORChain,
			Coins: common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(50*common.One))},
		},
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10,
			Count:    6,
			Interval: 5,
			Deposit:  cosmos.NewUint(50 * common.One),
			In:       cosmos.NewUint(30 * common.One),
			Out:      cosmos.NewUint(20 * common.One), // 20 BTC emitted
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	var btcSolvency math.Int
	for _, s := range solvency {
		if s.Asset.Equals(common.BTCAsset) {
			btcSolvency = s.Amount
		}
	}

	// BTC: Vault 100 - Pool 80 - StreamingOutput 20 = 0
	// Without the fix, the native-source continue skips target output accounting,
	// yielding: Vault 100 - Pool 80 = 20 (incorrectly over-solvent)
	c.Assert(btcSolvency.Equal(math.NewInt(0)), Equals, true,
		Commentf("BTC solvency: expected 0 (RUNE->BTC streaming output subtracted), got %s", btcSolvency))
}

// TestSolvencyStreamingSwapSynthTargetNotSubtracted verifies that streaming swaps targeting
// synth/trade/secured assets do NOT subtract output from L1 solvency, because those assets
// are minted without reducing pool.BalanceAsset.
func (s *NetworkManagerTestSuite) TestSolvencyStreamingSwapSynthTargetNotSubtracted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: BTC pool with full balance (no L1 assets emitted)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Vault holds 100 BTC
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// V2 streaming swap: RUNE -> BTC/BTC (synth target)
	// Synth minting does NOT reduce pool.BalanceAsset, so output should NOT be subtracted.
	synthTarget := common.BTCAsset.GetSyntheticAsset()
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.THORChain,
			Coins: common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(50*common.One))},
		},
		TargetAsset: synthTarget,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10,
			Count:    6,
			Interval: 5,
			Deposit:  cosmos.NewUint(50 * common.One),
			In:       cosmos.NewUint(30 * common.One),
			Out:      cosmos.NewUint(20 * common.One), // 20 synth BTC minted (not L1)
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	var btcSolvency math.Int
	for _, s := range solvency {
		if s.Asset.Equals(common.BTCAsset) {
			btcSolvency = s.Amount
		}
	}

	// BTC: Vault 100 - Pool 100 = 0
	// The synth streaming output (20) must NOT be subtracted from L1 solvency.
	// Without the fix, it would be: Vault 100 - Pool 100 - SynthOutput 20 = -20 (incorrectly under-solvent)
	c.Assert(btcSolvency.Equal(math.NewInt(0)), Equals, true,
		Commentf("BTC solvency: expected 0 (synth output NOT subtracted), got %s", btcSolvency))
}

// TestSolvencyStreamingSwapTradeTargetSubtracted verifies that the in-flight
// output of a streaming swap targeting a TRADE asset is treated as a liability.
// Trade (and secured) targets, unlike synths, DO reduce the underlying L1 pool
// balance per sub-swap, while the trade/secured depth is not credited until
// settlement. The accumulated output must therefore be subtracted so the lowered
// pool balance does not appear as excess vault funds (issue #3069).
func (s *NetworkManagerTestSuite) TestSolvencyStreamingSwapTradeTargetSubtracted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// BTC pool has already emitted 20 BTC to the in-flight stream (BalanceAsset
	// reduced), but the vault still holds the coins and no trade depth is credited
	// until settlement.
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(80 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// V2 streaming swap: RUNE -> BTC~BTC (trade target)
	streamingTxID := GetRandomTxHash()
	streamingSwapMsg := types.MsgSwap{
		Tx: common.Tx{
			ID:    streamingTxID,
			Chain: common.THORChain,
			Coins: common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(50*common.One))},
		},
		TargetAsset: common.BTCAsset.GetTradeAsset(),
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity: 10,
			Count:    6,
			Interval: 5,
			Deposit:  cosmos.NewUint(50 * common.One),
			In:       cosmos.NewUint(30 * common.One),
			Out:      cosmos.NewUint(20 * common.One), // 20 BTC emitted from pool, owed to trade acct
		},
	}
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, streamingSwapMsg), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	var btcSolvency math.Int
	for _, sol := range solvency {
		if sol.Asset.Equals(common.BTCAsset) {
			btcSolvency = sol.Amount
		}
	}

	// BTC: Vault 100 - Pool 80 - StreamingOutput 20 = 0
	// Without the fix, it would be: Vault 100 - Pool 80 = 20 (phantom over-solvency)
	c.Assert(btcSolvency.Equal(math.NewInt(0)), Equals, true,
		Commentf("BTC solvency: expected 0 (trade streaming output subtracted), got %s", btcSolvency))
}

func (s *NetworkManagerTestSuite) TestProcessPostChurnSolvency(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup: Create a pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create an active vault with over-solvent funds
	activeVault := GetRandomVault()
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// Test 1: Not at interval block - should not process
	ctx = ctx.WithBlockHeight(100)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 432000)
	err := nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	// Test 2: At interval block - should process
	ctx = ctx.WithBlockHeight(432000)

	// Set OverSolvencyToTreasuryBps to 0 - should emit event but not create swaps
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), 0)

	err = nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	// Test 3: With OverSolvencyToTreasuryBps set to 5000 (50%)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), 5000)

	err = nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	// Verify swap was added to queue
	// Note: This depends on which queue is enabled
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer iter.Close()
		hasSwap := false
		for ; iter.Valid(); iter.Next() {
			hasSwap = true
			break
		}
		c.Assert(hasSwap, Equals, true)
	} else {
		iter := mgr.Keeper().GetSwapQueueIterator(ctx)
		defer iter.Close()
		hasSwap := false
		for ; iter.Valid(); iter.Next() {
			hasSwap = true
			break
		}
		c.Assert(hasSwap, Equals, true)
	}

	// Test 4: No over-solvency case
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	err = nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	// Should complete without error even when under-solvent
}

func (s *NetworkManagerTestSuite) TestProcessOverSolvencyUsesPerAssetBps(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	ctx = ctx.WithBlockHeight(432000)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 432000)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), 0)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String()+"-"+common.BTCAsset.MimirString(), 5000)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String()+"-"+common.ETHAsset.MimirString(), 0)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(160*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	err := nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer iter.Close()

	var btcSwaps, ethSwaps int
	for ; iter.Valid(); iter.Next() {
		var msg types.MsgSwap
		c.Assert(mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg), IsNil)
		switch {
		case msg.Tx.Coins[0].Asset.Equals(common.BTCAsset):
			btcSwaps++
			c.Assert(msg.Tx.Coins[0].Amount.Equal(cosmos.NewUint(25*common.One)), Equals, true)
		case msg.Tx.Coins[0].Asset.Equals(common.ETHAsset):
			ethSwaps++
		}
	}

	c.Assert(btcSwaps, Equals, 1)
	c.Assert(ethSwaps, Equals, 0)
}

func (s *NetworkManagerTestSuite) TestCreateSwapToOverSolvency(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup: Create an active vault with BTC asset
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup: Create a pool for the asset
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Test 1: Create swap to over-solvency
	swapAmount := cosmos.NewUint(10 * common.One)
	err := nmgr.createSwapToOverSolvency(ctx, mgr, common.BTCAsset, swapAmount)
	c.Assert(err, IsNil)

	// Verify swap was created in the queue
	var foundSwap *types.MsgSwap
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			foundSwap = &msg
			break
		}
	} else {
		iter := mgr.Keeper().GetSwapQueueIterator(ctx)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			foundSwap = &msg
			break
		}
	}

	c.Assert(foundSwap, NotNil)
	c.Assert(foundSwap.Tx.Coins[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(foundSwap.Tx.Coins[0].Amount.Equal(swapAmount), Equals, true)
	c.Assert(foundSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true)

	// Verify streaming swap parameters
	c.Assert(foundSwap.StreamInterval, Equals, uint64(1))
	c.Assert(foundSwap.StreamQuantity, Equals, uint64(1))

	// Verify sender uses chain-appropriate address from vault
	btcAddr, err := vaultPubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(foundSwap.Tx.FromAddress.String(), Equals, btcAddr.String())
	c.Assert(foundSwap.Tx.ToAddress.String(), Equals, btcAddr.String())

	// Verify destination is over-solvency address
	overSolvencyAddr := mgr.Keeper().GetConstants().GetStringValue(constants.OverSolvencyAddress)
	c.Assert(foundSwap.Destination.String(), Equals, overSolvencyAddr)

	// Test 2: Multiple swaps - add vault for ETH chain
	ethVaultPubKey := GetRandomPubKey()
	ethVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, ethVaultPubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	ethVault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, ethVault), IsNil)

	err = nmgr.createSwapToOverSolvency(ctx, mgr, common.ETHAsset, cosmos.NewUint(5*common.One))
	c.Assert(err, IsNil)
	// Should succeed and add another swap to queue
}

func (s *NetworkManagerTestSuite) TestCreateSwapToOverSolvencyXMRUsesFrostAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	vaultPubKey := GetRandomPubKey()
	vaultPubKeyFrost := GetRandomEd25519PubKey()
	vault := NewVaultV2(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		vaultPubKey,
		common.Chains{common.XMRChain}.Strings(),
		[]ChainContract{},
		common.EmptyPubKey,
	)
	vault.PubKeyFrost = vaultPubKeyFrost
	vault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	swapAmount := cosmos.NewUint(10 * common.One)
	err := nmgr.createSwapToOverSolvency(ctx, mgr, common.XMRAsset, swapAmount)
	c.Assert(err, IsNil)

	var foundSwap *types.MsgSwap
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			foundSwap = &msg
			break
		}
	} else {
		iter := mgr.Keeper().GetSwapQueueIterator(ctx)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			foundSwap = &msg
			break
		}
	}
	c.Assert(foundSwap, NotNil)

	xmrAddr, err := vaultPubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(foundSwap.Tx.FromAddress.Equals(xmrAddr), Equals, true)
	c.Assert(foundSwap.Tx.ToAddress.Equals(xmrAddr), Equals, true)
}

func (s *NetworkManagerTestSuite) TestSolvencyWithScheduledOutbounds(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create a pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create vault with funds
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Add scheduled outbound transaction
	txOut := &TxOut{
		Height: ctx.BlockHeight() + 1,
		TxArray: []TxOutItem{
			{
				Chain:       common.BTCChain,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One)),
				ToAddress:   GetRandomBTCAddress(),
				VaultPubKey: vault.PubKey,
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Calculate solvency - should account for scheduled outbound
	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)

	// Vault has 150, pool needs 100, scheduled outbound 20
	// Expected: 150 - 100 - 20 = 30 over-solvent
	expectedSolvency := math.NewInt(30 * common.One)
	c.Assert(solvency[0].Amount.Equal(expectedSolvency), Equals, true)
}

func (s *NetworkManagerTestSuite) TestSolvencyWithPendingOutboundsAtPastHeight(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create a pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create vault with funds
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Add a pending outbound at a PAST block height (OutHash empty - not yet signed).
	// This simulates an outbound that was scheduled a few blocks ago but Bifrost
	// hasn't signed it yet. The solvency calc should still deduct it.
	pastHeight := ctx.BlockHeight() - 5
	if pastHeight < 1 {
		pastHeight = 1
	}
	txOut := &TxOut{
		Height: pastHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.BTCChain,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One)),
				ToAddress:   GetRandomBTCAddress(),
				VaultPubKey: vault.PubKey,
				// OutHash is empty - not yet signed
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Calculate solvency - should account for pending outbound at past height
	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)

	// Vault has 150, pool needs 100, pending outbound 20
	// Expected: 150 - 100 - 20 = 30 over-solvent
	expectedSolvency := math.NewInt(30 * common.One)
	c.Assert(solvency[0].Amount.Equal(expectedSolvency), Equals, true,
		Commentf("expected %s, got %s", expectedSolvency.String(), solvency[0].Amount.String()))

	// Now simulate the outbound being signed (OutHash set).
	// When OutHash is set, vault.SubFundsWithClamping has already been called in the same block,
	// so the item should NOT be deducted anymore.
	txOut.TxArray[0].OutHash = GetRandomTxHash()
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Also decrement vault coins (as vault.SubFundsWithClamping would do on observation)
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(130*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Calculate solvency again - should still be 30 (130 - 100 = 30)
	// The signed outbound should be skipped, and vault coins already reflect the send
	solvency2, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency2), Equals, 1)
	c.Assert(solvency2[0].Amount.Equal(expectedSolvency), Equals, true,
		Commentf("expected %s, got %s", expectedSolvency.String(), solvency2[0].Amount.String()))
}

// TestSolvencyXMRExtendedBackwardScan verifies that XMR pending outbounds older
// than the base SigningTransactionPeriod but within the extended XMR window
// (xmrSigningPeriodMultiplier x period) are still subtracted, while non-XMR
// outbounds past the base window are not (issue #3071).
func (s *NetworkManagerTestSuite) TestSolvencyXMRExtendedBackwardScan(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	signingPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod) // 300
	// Block height must exceed the extended XMR window so the scan reaches back far enough.
	ctx = ctx.WithBlockHeight(signingPeriod*xmrSigningPeriodMultiplier + 100)

	// XMR pool + vault.
	xmrPool := NewPool()
	xmrPool.Asset = common.XMRAsset
	xmrPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	xmrPool.BalanceRune = cosmos.NewUint(100 * common.One)
	xmrPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, xmrPool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Pending XMR outbound at a height older than the base window (300) but within
	// the extended XMR window (900). Unsigned (OutHash empty) -> still a liability.
	xmrHeight := ctx.BlockHeight() - signingPeriod - 100 // ~400 back, > base 300, < 900
	txOut := &TxOut{
		Height: xmrHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.XMRChain,
				Coin:        common.NewCoin(common.XMRAsset, cosmos.NewUint(20*common.One)),
				ToAddress:   GetRandomTHORAddress(), // address value irrelevant for solvency math
				VaultPubKey: vault.PubKey,
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	var xmrSolvency math.Int
	for _, sol := range solvency {
		if sol.Asset.Equals(common.XMRAsset) {
			xmrSolvency = sol.Amount
		}
	}

	// XMR: Vault 150 - Pool 100 - PendingOutbound 20 = 30
	// Without the fix (base 300 window only), the outbound is missed: 150 - 100 = 50.
	c.Assert(xmrSolvency.Equal(math.NewInt(30*common.One)), Equals, true,
		Commentf("XMR solvency: expected 30 (extended-window pending outbound subtracted), got %s", xmrSolvency))
}

// TestSolvencyForwardScanBeyondMaxOffset verifies that scheduled outbounds placed
// beyond MaxTxOutOffset (up to TxOutDelayMax, as CalcTxOutHeight does under
// congestion) are still subtracted from solvency (issue #3070).
func (s *NetworkManagerTestSuite) TestSolvencyForwardScanBeyondMaxOffset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	maxOffset := mgr.Keeper().GetConfigInt64(ctx, constants.MaxTxOutOffset) // 720
	// Schedule a congested outbound just beyond MaxTxOutOffset (within TxOutDelayMax).
	// CalcTxOutHeight only advances past a block once it is full, so scheduled items are
	// contiguous with no gap; the scan continues until the first empty block past the
	// offset. Place the item at the first block beyond the offset to exercise the
	// extended forward scan without an intervening empty block.
	scheduledHeight := ctx.BlockHeight() + maxOffset + 1
	txOut := &TxOut{
		Height: scheduledHeight,
		TxArray: []TxOutItem{
			{
				Chain:       common.BTCChain,
				Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(20*common.One)),
				ToAddress:   GetRandomBTCAddress(),
				VaultPubKey: vault.PubKey,
			},
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	solvency, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency), Equals, 1)

	// Vault 150 - Pool 100 - ScheduledOutbound 20 = 30
	// Without the fix (scan stops at MaxTxOutOffset), the outbound is missed: 150 - 100 = 50.
	c.Assert(solvency[0].Amount.Equal(math.NewInt(30*common.One)), Equals, true,
		Commentf("expected 30 (outbound beyond MaxTxOutOffset subtracted), got %s", solvency[0].Amount))
}

func (s *NetworkManagerTestSuite) TestSolvencyWithTradeAndSecuredAssets(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create a pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create vault with funds
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Calculate solvency - verifies calculation logic
	solvency1, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency1), Equals, 1)

	// Vault 150 - Pool 100 = 50 over-solvent
	expected := math.NewInt(50 * common.One)
	c.Assert(solvency1[0].Amount.Equal(expected), Equals, true)

	// Test with trade assets: Add trade asset deposits
	// Trade assets are tracked in KVStore (not bank keeper)
	tradeAsset := common.BTCAsset.GetTradeAsset()
	tu := types.NewTradeUnit(tradeAsset)
	tu.Units = cosmos.NewUint(10 * common.One)
	tu.Depth = cosmos.NewUint(10 * common.One)
	mgr.Keeper().SetTradeUnit(ctx, tu)

	// Calculate solvency with trade assets
	solvency2, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency2), Equals, 1)

	// Vault 150 - Pool 100 - TradeDepth 10 = 40 over-solvent
	expected = math.NewInt(40 * common.One)
	c.Assert(solvency2[0].Amount.Equal(expected), Equals, true)

	// Test with secured assets: Add secured asset deposits
	// Secured assets are minted as bank keeper tokens
	securedAsset := common.BTCAsset.GetSecuredAsset()
	addr := GetRandomBech32Addr()
	coin := common.NewCoin(securedAsset, cosmos.NewUint(15*common.One))
	err = mgr.Keeper().MintAndSendToAccount(ctx, addr, coin)
	c.Assert(err, IsNil)

	// Create secured asset pool metadata
	securedPool := types.NewSecuredAsset(securedAsset)
	securedPool.Depth = cosmos.NewUint(15 * common.One)
	mgr.Keeper().SetSecuredAsset(ctx, securedPool)

	// Calculate solvency with both trade and secured assets
	solvency3, err := nmgr.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(solvency3), Equals, 1)

	// Vault 150 - Pool 100 - TradeDepth 10 - SecuredDepth 15 = 25 over-solvent
	expected = math.NewInt(25 * common.One)
	c.Assert(solvency3[0].Amount.Equal(expected), Equals, true)
}

func (s *NetworkManagerTestSuite) TestProcessPostChurnSolvencyEdgeCases(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Set block height to interval so checks will run
	ctx = ctx.WithBlockHeight(432000)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 432000)

	// Test 1: No pools - should not error
	err := nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	// Test 2: No vaults - should not error
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	err = nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)

	// Test 3: OverSolvencyToTreasuryBps exceeds maximum (should cap at 10000)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), 15000)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(200*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	err = nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	// Should cap at 100% and not error

	// Test 4: Negative mimir value (should treat as 0)
	mgr.Keeper().SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), -100)

	err = nmgr.processOverSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	// Should not create any swaps when set to 0
}

func (s *NetworkManagerTestSuite) TestSwapToOverSolvencyIncome(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())

	// Setup: Create an active vault with BTC asset
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup: Create a pool for BTC
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(2000 * common.One) // 2:1 RUNE:BTC ratio
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Test 1: Process L1 asset (BTC) as income
	coin := common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))
	err := nmgr.SwapToOverSolvencyIncome(ctx, mgr, coin)
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		c.Assert(err, IsNil)
		// Verify swap was created in the queue (fee tracking happens during swap execution)
		iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer iter.Close()
		var foundSwap bool
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			if msg.Tx.Coins[0].Asset.Equals(common.BTCAsset) {
				foundSwap = true
				c.Assert(msg.Tx.Coins[0].Amount.Equal(coin.Amount), Equals, true)
				c.Assert(msg.TargetAsset.Equals(common.RuneAsset()), Equals, true)
			}
		}
		c.Assert(foundSwap, Equals, true, Commentf("swap should have been created in queue"))
	} else {
		c.Assert(err, NotNil, Commentf("should return error when AdvSwapQueue is disabled"))
	}

	// Test 2: Process RUNE directly to over-solvency address
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 100*common.One)
	runeCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(50*common.One))
	overSolvencyAddrStr := mgr.Keeper().GetConstants().GetStringValue(constants.OverSolvencyAddress)
	overSolvencyAccAddr, addrErr := cosmos.AccAddressFromBech32(overSolvencyAddrStr)
	c.Assert(addrErr, IsNil)
	runeBalanceBefore := mgr.Keeper().GetBalanceOf(ctx, overSolvencyAccAddr, common.RuneAsset())

	err = nmgr.SwapToOverSolvencyIncome(ctx, mgr, runeCoin)
	c.Assert(err, IsNil)

	// Verify RUNE was transferred to over-solvency address
	runeBalanceAfter := mgr.Keeper().GetBalanceOf(ctx, overSolvencyAccAddr, common.RuneAsset())
	c.Assert(runeBalanceAfter.Amount.Sub(runeBalanceBefore.Amount).Uint64(), Equals, runeCoin.Amount.Uint64())

	// Test 3: Zero amount should return nil without error
	zeroCoin := common.NewCoin(common.BTCAsset, cosmos.ZeroUint())
	err = nmgr.SwapToOverSolvencyIncome(ctx, mgr, zeroCoin)
	c.Assert(err, IsNil)

	// Test 4: Empty coin should return nil without error
	emptyCoin := common.Coin{}
	err = nmgr.SwapToOverSolvencyIncome(ctx, mgr, emptyCoin)
	c.Assert(err, IsNil)

	// Test 5: Asset with no vault should return an error (no active vault found)
	noPoolCoin := common.NewCoin(common.LTCAsset, cosmos.NewUint(10*common.One))
	err = nmgr.SwapToOverSolvencyIncome(ctx, mgr, noPoolCoin)
	c.Assert(err, NotNil) // No active vault for LTC, so swap creation fails
}

func (s *NetworkManagerTestSuite) TestSwapToOverSolvencyIncomeTradeAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup: Create an active vault with BTC asset
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup: Create a pool for BTC (L1)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(3000 * common.One) // 3:1 RUNE:BTC ratio
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Test: Process trade asset - swap created for later execution
	tradeAsset := common.BTCAsset.GetTradeAsset()
	tradeCoin := common.NewCoin(tradeAsset, cosmos.NewUint(5*common.One))
	err := nmgr.SwapToOverSolvencyIncome(ctx, mgr, tradeCoin)
	c.Assert(err, IsNil)

	// Verify swap was created (fee tracking happens during swap execution)
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer iter.Close()
		var foundSwap bool
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			if msg.Tx.Coins[0].Asset.Equals(tradeAsset) {
				foundSwap = true
				c.Assert(msg.Tx.Coins[0].Amount.Equal(tradeCoin.Amount), Equals, true)
				c.Assert(msg.TargetAsset.Equals(common.RuneAsset()), Equals, true)
			}
		}
		c.Assert(foundSwap, Equals, true, Commentf("swap should have been created in queue"))
	}
}

func (s *NetworkManagerTestSuite) TestSwapToOverSolvencyIncomeSecuredAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nmgr := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), NewDummyEventMgr())
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	// Setup: Create an active vault with BTC asset
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup: Create a pool for BTC (L1)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(4000 * common.One) // 4:1 RUNE:BTC ratio
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Test: Process secured asset - swap created for later execution
	securedAsset := common.BTCAsset.GetSecuredAsset()
	securedCoin := common.NewCoin(securedAsset, cosmos.NewUint(2*common.One))
	err := nmgr.SwapToOverSolvencyIncome(ctx, mgr, securedCoin)
	c.Assert(err, IsNil)

	// Verify swap was created (fee tracking happens during swap execution)
	if mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		iter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer iter.Close()
		var foundSwap bool
		for ; iter.Valid(); iter.Next() {
			var msg types.MsgSwap
			unmarshalErr := mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg)
			c.Assert(unmarshalErr, IsNil)
			if msg.Tx.Coins[0].Asset.Equals(securedAsset) {
				foundSwap = true
				c.Assert(msg.Tx.Coins[0].Amount.Equal(securedCoin.Amount), Equals, true)
				c.Assert(msg.TargetAsset.Equals(common.RuneAsset()), Equals, true)
			}
		}
		c.Assert(foundSwap, Equals, true, Commentf("swap should have been created in queue"))
	}
}

func (s *NetworkManagerTestSuite) TestCalcBlockRewardsPOLReserveDeduct(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)

	availablePoolsRune := cosmos.NewUint(1000 * common.One)
	vaultsLiquidityRune := cosmos.NewUint(1000 * common.One)
	effectiveSecurityBond := cosmos.NewUint(2000 * common.One)
	totalEffectiveBond := cosmos.NewUint(2000 * common.One)
	totalReserve := cosmos.NewUint(10000 * common.One)
	totalLiquidityFees := cosmos.NewUint(1000)
	emissionCurve := constAccessor.GetInt64Value(constants.EmissionCurve)
	blocksPerYear := constAccessor.GetInt64Value(constants.BlocksPerYear)

	// With 0 bps, polReserveDeduct should be zero
	_, _, _, _, _, _, _, polReserveDeduct := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve,
		blocksPerYear, 0, 0, 0, 0, 0)
	c.Check(polReserveDeduct.IsZero(), Equals, true)

	// With 500 bps (5%), polReserveDeduct should reduce bond+pool rewards
	bondR1, poolR1, _, _, _, _, _, _ := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve,
		blocksPerYear, 0, 0, 0, 0, 0)
	totalWithout := bondR1.Add(poolR1)

	bondR2, poolR2, _, _, _, _, _, polReserveDeduct := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve,
		blocksPerYear, 0, 0, 0, 0, 500)
	totalWith := bondR2.Add(poolR2)
	c.Check(polReserveDeduct.IsZero(), Equals, false)
	// The deducted amount plus remaining should equal original
	c.Check(totalWith.Add(polReserveDeduct).String(), Equals, totalWithout.String())
}

func (s *NetworkManagerTestSuite) TestPOLSystemIncomeShareFundsStableReserve(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(k.SetNetwork(ctx, NewNetwork()), IsNil)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{pool.Asset.GetChain().String()}, []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(pool.Asset, pool.BalanceAsset))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(k.AddToLiquidityFees(ctx, pool.Asset, cosmos.NewUint(50*common.One)), IsNil)

	k.SetMimir(ctx, constants.DevFundSystemIncomeBps.String(), 0)
	k.SetMimir(ctx, constants.SystemIncomeBurnRateBps.String(), 0)
	k.SetMimir(ctx, constants.TCYStakeSystemIncomeBps.String(), 0)
	k.SetMimir(ctx, constants.MarketingFundSystemIncomeBps.String(), 0)
	k.SetMimir(ctx, constants.POLReserveSystemIncomeBps.String(), 10_000)
	k.SetMimir(ctx, constants.StableReserveEnabled.String(), 1)
	k.SetMimir(ctx, constants.StableReservePOLShareBps.String(), 2_500)

	polBefore := k.GetRuneBalanceOfModule(ctx, POLReserveName)
	stableBefore := k.GetRuneBalanceOfModule(ctx, StableReserveName)

	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	polDelta := common.SafeSub(k.GetRuneBalanceOfModule(ctx, POLReserveName), polBefore)
	stableDelta := common.SafeSub(k.GetRuneBalanceOfModule(ctx, StableReserveName), stableBefore)
	totalPOLIncome := polDelta.Add(stableDelta)
	c.Assert(totalPOLIncome.IsZero(), Equals, false)

	expectedStable := common.GetSafeShare(cosmos.NewUint(2_500), cosmos.NewUint(10_000), totalPOLIncome)
	c.Check(stableDelta.Equal(expectedStable), Equals, true,
		Commentf("stable reserve delta %s should be 25%% of combined POL-system-income transfer %s", stableDelta, totalPOLIncome))
	c.Check(polDelta.Equal(common.SafeSub(totalPOLIncome, expectedStable)), Equals, true,
		Commentf("POL reserve delta %s should receive the remainder after stable reserve share %s", polDelta, expectedStable))
}

// updateNetworkModuleBalances snapshots every module RUNE balance that
// UpdateNetwork can move, so a test can assert value conservation on success
// and a complete rollback on failure.
type updateNetworkModuleBalances struct {
	reserve       cosmos.Uint
	stableReserve cosmos.Uint
	polReserve    cosmos.Uint
	tcyStake      cosmos.Uint
	bond          cosmos.Uint
	asgard        cosmos.Uint
}

func snapshotUpdateNetworkModuleBalances(ctx cosmos.Context, k keeper.Keeper) updateNetworkModuleBalances {
	return updateNetworkModuleBalances{
		reserve:       k.GetRuneBalanceOfModule(ctx, ReserveName),
		stableReserve: k.GetRuneBalanceOfModule(ctx, StableReserveName),
		polReserve:    k.GetRuneBalanceOfModule(ctx, POLReserveName),
		tcyStake:      k.GetRuneBalanceOfModule(ctx, TCYStakeName),
		bond:          k.GetRuneBalanceOfModule(ctx, BondName),
		asgard:        k.GetRuneBalanceOfModule(ctx, AsgardName),
	}
}

// setupUpdateNetworkStableReserveTest builds a network where UpdateNetwork
// makes a Stable Reserve deduction, a POL Reserve deduction and a third
// (TCY stake) deduction in the same call, on a keeper whose SetNetwork - the
// very last write of UpdateNetwork - can be made to fail on demand.
func setupUpdateNetworkStableReserveTest(c *C) (cosmos.Context, *Mgrs, *VaultManagerTestHelpKeeper, *NetworkMgr) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewVaultGenesisSetupTestHelper(mgr.Keeper())
	mgr.K = helper
	networkMgr := newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(helper.SetNetwork(ctx, NewNetwork()), IsNil)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.Status = PoolAvailable
	c.Assert(helper.SetPool(ctx, pool), IsNil)

	vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{pool.Asset.GetChain().String()}, []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(pool.Asset, pool.BalanceAsset))
	c.Assert(helper.SetVault(ctx, vault), IsNil)

	c.Assert(helper.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(helper.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(helper.AddToLiquidityFees(ctx, pool.Asset, cosmos.NewUint(50*common.One)), IsNil)

	// Three deductions in the same call: TCY stake, POL Reserve and - carved out
	// of the POL share - the Stable Reserve. Dev fund / marketing / burn are off
	// so every RUNE that leaves the Reserve lands in a module we snapshot.
	helper.SetMimir(ctx, constants.DevFundSystemIncomeBps.String(), 0)
	helper.SetMimir(ctx, constants.MarketingFundSystemIncomeBps.String(), 0)
	helper.SetMimir(ctx, constants.SystemIncomeBurnRateBps.String(), 0)
	helper.SetMimir(ctx, constants.TCYStakeSystemIncomeBps.String(), 1_000)
	helper.SetMimir(ctx, constants.POLReserveSystemIncomeBps.String(), 2_000)
	helper.SetMimir(ctx, constants.StableReserveEnabled.String(), 1)
	helper.SetMimir(ctx, constants.StableReservePOLShareBps.String(), 2_500)

	return ctx, mgr, helper, networkMgr
}

// TestUpdateNetworkStableReserveDeductionCommitsAtomically pins the Stable
// Reserve transfer to the same CacheContext as the other Reserve deductions.
// Running it on the parent ctx made the Reserve debit disappear when the cache
// committed (the cached, pre-transfer Reserve balance overwrote it), minting
// the Stable Reserve credit out of thin air.
func (s *NetworkManagerTestSuite) TestUpdateNetworkStableReserveDeductionCommitsAtomically(c *C) {
	ctx, mgr, helper, networkMgr := setupUpdateNetworkStableReserveTest(c)

	before := snapshotUpdateNetworkModuleBalances(ctx, helper)
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)
	after := snapshotUpdateNetworkModuleBalances(ctx, helper)

	// All three deductions plus the bond reward must have been paid.
	stableDelta := common.SafeSub(after.stableReserve, before.stableReserve)
	polDelta := common.SafeSub(after.polReserve, before.polReserve)
	tcyDelta := common.SafeSub(after.tcyStake, before.tcyStake)
	bondDelta := common.SafeSub(after.bond, before.bond)
	asgardDelta := common.SafeSub(after.asgard, before.asgard)
	c.Assert(stableDelta.IsZero(), Equals, false, Commentf("stable reserve should be credited"))
	c.Assert(polDelta.IsZero(), Equals, false, Commentf("pol reserve should be credited"))
	c.Assert(tcyDelta.IsZero(), Equals, false, Commentf("tcy stake should be credited"))
	c.Assert(bondDelta.IsZero(), Equals, false, Commentf("bond module should be credited"))

	// Value conservation: every RUNE credited must have been debited from the
	// Reserve. With the Stable Reserve transfer on the parent ctx the commit
	// restored the stale Reserve balance, so the debit came up short by exactly
	// the Stable Reserve credit.
	reserveDebit := common.SafeSub(before.reserve, after.reserve)
	credits := stableDelta.Add(polDelta).Add(tcyDelta).Add(bondDelta).Add(asgardDelta)
	c.Check(reserveDebit.Equal(credits), Equals, true,
		Commentf("reserve debit %s should equal total module credits %s (stable %s, pol %s, tcy %s, bond %s, asgard %s)",
			reserveDebit, credits, stableDelta, polDelta, tcyDelta, bondDelta, asgardDelta))
}

// TestUpdateNetworkStableReserveDeductionRollsBack verifies that when a later
// step of UpdateNetwork fails, the Stable Reserve transfer is discarded along
// with every other Reserve deduction. On the parent ctx it survived the
// rollback, leaving a Stable Reserve credit with a Reserve debit and no
// matching network bookkeeping.
func (s *NetworkManagerTestSuite) TestUpdateNetworkStableReserveDeductionRollsBack(c *C) {
	ctx, mgr, helper, networkMgr := setupUpdateNetworkStableReserveTest(c)

	before := snapshotUpdateNetworkModuleBalances(ctx, helper)
	networkBefore, err := helper.GetNetwork(ctx)
	c.Assert(err, IsNil)

	// Fail the final SetNetwork, after every deduction and reward transfer.
	helper.failSetNetwork = true
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), NotNil)
	helper.failSetNetwork = false

	after := snapshotUpdateNetworkModuleBalances(ctx, helper)
	c.Check(after.stableReserve.Equal(before.stableReserve), Equals, true,
		Commentf("stable reserve %s should be rolled back to %s", after.stableReserve, before.stableReserve))
	c.Check(after.reserve.Equal(before.reserve), Equals, true,
		Commentf("reserve %s should be rolled back to %s", after.reserve, before.reserve))
	c.Check(after.polReserve.Equal(before.polReserve), Equals, true,
		Commentf("pol reserve %s should be rolled back to %s", after.polReserve, before.polReserve))
	c.Check(after.tcyStake.Equal(before.tcyStake), Equals, true,
		Commentf("tcy stake %s should be rolled back to %s", after.tcyStake, before.tcyStake))
	c.Check(after.bond.Equal(before.bond), Equals, true,
		Commentf("bond %s should be rolled back to %s", after.bond, before.bond))
	c.Check(after.asgard.Equal(before.asgard), Equals, true,
		Commentf("asgard %s should be rolled back to %s", after.asgard, before.asgard))

	networkAfter, err := helper.GetNetwork(ctx)
	c.Assert(err, IsNil)
	c.Check(networkAfter.BondRewardRune.Equal(networkBefore.BondRewardRune), Equals, true,
		Commentf("bond reward bookkeeping should not persist when UpdateNetwork fails"))
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	// maxDeployment = 0 means disabled
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), 0)
	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleZeroBalance(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	// Enable deployment but don't fund the module
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), 10_000_000_000)
	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleBestPool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.Status = PoolAvailable
	poolBTC.BalanceRune = cosmos.NewUint(1000 * common.One)
	poolBTC.BalanceAsset = cosmos.NewUint(10 * common.One)
	poolBTC.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, poolBTC), IsNil)

	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset
	poolETH.Status = PoolAvailable
	poolETH.BalanceRune = cosmos.NewUint(500 * common.One)
	poolETH.BalanceAsset = cosmos.NewUint(50 * common.One)
	poolETH.LPUnits = cosmos.NewUint(500 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, poolETH), IsNil)

	// BTC score = 100 / 100000000000 ~= 0
	// ETH score = 200 / 50000000000 ~= 0 but higher ratio
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(200)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	// ETH pool should have received LP since it has higher fees/depth
	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.String(), Equals, cosmos.NewUint(50*common.One).String())

	// BTC pool should have no deposit
	depositBTC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(depositBTC.RuneDeposited.IsZero(), Equals, true)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleCapEnforced(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 500*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.String(), Equals, cosmos.NewUint(100*common.One).String())

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(400*common.One).String())
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleSkipsPausedPools(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	mgr.Keeper().SetMimir(ctx, "PauseLPBTC", ctx.BlockHeight())

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.IsZero(), Equals, true)

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(50*common.One).String())
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleSkipsGlobalTradingHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))
	mgr.Keeper().SetMimir(ctx, "HaltTrading", ctx.BlockHeight())

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.IsZero(), Equals, true)

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(50*common.One).String())
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleSkipsChainTradingHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))
	mgr.Keeper().SetMimir(ctx, "HaltBTCTrading", ctx.BlockHeight())

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.IsZero(), Equals, true)

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(50*common.One).String())
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleNoEligiblePools(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(50*common.One).String())
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleSkipsRagnarokedPools(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	mgr.Keeper().SetMimir(ctx, "RAGNAROK-BTC-BTC", 1)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(50*common.One).String())

	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.IsZero(), Equals, true)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleNegativeMimir(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	// Setting -1 (common disable pattern) should be treated as 0 by SafeUintFromInt64
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), -1)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil) // Should exit early with no error, not wrap to MaxUint64
}

// polReserveNonGasAsset returns a non-gas, non-anchor ERC20 pool asset used
// by the eligibility filter tests.
func polReserveNonGasAsset(c *C) common.Asset {
	asset, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	return asset
}

// polReserveSetupTwoPoolCycle creates a BTC.BTC pool (gas asset) and a
// higher-scoring non-gas ETH.USDC pool, funds the pol_reserve module, and
// sets POLReserveMaxDeployment. Without any other mimirs the filter's
// default set (gas + TOR anchor) restricts deployment to BTC.BTC.
//
// An ETH.ETH pool is also created (with no rolling fees so it never scores)
// because the add-liquidity handler requires the ERC20 asset's gas-asset
// pool to exist before it will accept a deposit into ETH.USDC.
func polReserveSetupTwoPoolCycle(c *C, ctx cosmos.Context, mgr Manager) (common.Asset, common.Asset) {
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.Status = PoolAvailable
	poolBTC.BalanceRune = cosmos.NewUint(1000 * common.One)
	poolBTC.BalanceAsset = cosmos.NewUint(10 * common.One)
	poolBTC.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, poolBTC), IsNil)

	// ETH.ETH exists solely to satisfy the add-liquidity handler's gas-pool
	// requirement for ERC20 deposits. It gets no rolling fees, so it never
	// scores in POLReserveCycle's selection.
	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset
	poolETH.Status = PoolAvailable
	poolETH.BalanceRune = cosmos.NewUint(1000 * common.One)
	poolETH.BalanceAsset = cosmos.NewUint(10 * common.One)
	poolETH.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, poolETH), IsNil)

	usdc := polReserveNonGasAsset(c)
	poolUSDC := NewPool()
	poolUSDC.Asset = usdc
	poolUSDC.Status = PoolAvailable
	poolUSDC.BalanceRune = cosmos.NewUint(500 * common.One)
	poolUSDC.BalanceAsset = cosmos.NewUint(50 * common.One)
	poolUSDC.LPUnits = cosmos.NewUint(500 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, poolUSDC), IsNil)

	// USDC has the higher score (more fees on a smaller pool), so without the
	// eligibility filter it would win the deployment.
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)
	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, usdc, cosmos.NewUint(10000)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	return common.BTCAsset, usdc
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleDefaultEligibleSet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	btc, usdc := polReserveSetupTwoPoolCycle(c, ctx, mgr)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	// BTC is a gas asset => eligible and funded despite having the lower score.
	depositBTC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, btc)
	c.Assert(err, IsNil)
	c.Check(depositBTC.RuneDeposited.String(), Equals, cosmos.NewUint(50*common.One).String())

	// USDC is neither a gas asset nor a TOR anchor => excluded by default.
	depositUSDC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, usdc)
	c.Assert(err, IsNil)
	c.Check(depositUSDC.RuneDeposited.IsZero(), Equals, true)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleTorAnchorEligible(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	btc, usdc := polReserveSetupTwoPoolCycle(c, ctx, mgr)

	// Promote USDC into the default set via the TorAnchor mimir.
	mgr.Keeper().SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	// USDC is now eligible and its higher score beats BTC.
	depositUSDC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, usdc)
	c.Assert(err, IsNil)
	c.Check(depositUSDC.RuneDeposited.String(), Equals, cosmos.NewUint(50*common.One).String())

	depositBTC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, btc)
	c.Assert(err, IsNil)
	c.Check(depositBTC.RuneDeposited.IsZero(), Equals, true)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleWhitelistOverride(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	btc, usdc := polReserveSetupTwoPoolCycle(c, ctx, mgr)

	// Force-include USDC without setting a TorAnchor mimir.
	mgr.Keeper().SetMimir(ctx, "POLReserveWhitelist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	depositUSDC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, usdc)
	c.Assert(err, IsNil)
	c.Check(depositUSDC.RuneDeposited.String(), Equals, cosmos.NewUint(50*common.One).String())

	depositBTC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, btc)
	c.Assert(err, IsNil)
	c.Check(depositBTC.RuneDeposited.IsZero(), Equals, true)
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleBlacklistOverridesDefault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	c.Assert(mgr.Keeper().AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(100)), IsNil)

	FundModule(c, ctx, mgr.Keeper(), POLReserveName, 50*common.One)
	mgr.Keeper().SetMimir(ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))

	// Blacklist the only gas-asset pool.
	mgr.Keeper().SetMimir(ctx, "POLReserveBlacklist-BTC-BTC", 1)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	deposit, err := mgr.Keeper().GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(deposit.RuneDeposited.IsZero(), Equals, true)

	remaining := mgr.Keeper().GetRuneBalanceOfModule(ctx, POLReserveName)
	c.Check(remaining.String(), Equals, cosmos.NewUint(50*common.One).String())
}

func (s *NetworkManagerTestSuite) TestPOLReserveCycleBlacklistOverridesWhitelist(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())

	btc, usdc := polReserveSetupTwoPoolCycle(c, ctx, mgr)

	// Whitelist AND blacklist the same pool => blacklist wins.
	mgr.Keeper().SetMimir(ctx, "POLReserveWhitelist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	mgr.Keeper().SetMimir(ctx, "POLReserveBlacklist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)

	err := networkMgr.POLReserveCycle(ctx, mgr)
	c.Assert(err, IsNil)

	// USDC blocked; BTC (gas asset) is the only remaining candidate and gets funded.
	depositUSDC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, usdc)
	c.Assert(err, IsNil)
	c.Check(depositUSDC.RuneDeposited.IsZero(), Equals, true)

	depositBTC, err := mgr.Keeper().GetPOLReserveDeposit(ctx, btc)
	c.Assert(err, IsNil)
	c.Check(depositBTC.RuneDeposited.String(), Equals, cosmos.NewUint(50*common.One).String())
}

func (s *NetworkManagerTestSuite) TestCalcBlockRewardsPOLReserveHighBPSClamping(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)

	availablePoolsRune := cosmos.NewUint(1000 * common.One)
	vaultsLiquidityRune := cosmos.NewUint(1000 * common.One)
	effectiveSecurityBond := cosmos.NewUint(2000 * common.One)
	totalEffectiveBond := cosmos.NewUint(2000 * common.One)
	totalReserve := cosmos.NewUint(10000 * common.One)
	totalLiquidityFees := cosmos.NewUint(1000)
	emissionCurve := constAccessor.GetInt64Value(constants.EmissionCurve)
	blocksPerYear := constAccessor.GetInt64Value(constants.BlocksPerYear)

	// Set all five deductions to 3000 BPS each (15000 total > 10000)
	// The clamping cascade should ensure POL reserve gets whatever remains
	_, _, _, devFundDeduct, burnDeduct, tcyDeduct, marketingDeduct, polReserveDeduct := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve,
		blocksPerYear, 3000, 3000, 3000, 3000, 3000)

	// Total deducted cannot exceed original system income
	bondR0, poolR0, _, _, _, _, _, _ := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(), emissionCurve,
		blocksPerYear, 0, 0, 0, 0, 0)
	originalSystemIncome := bondR0.Add(poolR0)

	totalDeducted := devFundDeduct.Add(burnDeduct).Add(tcyDeduct).Add(marketingDeduct).Add(polReserveDeduct)
	c.Check(totalDeducted.LTE(originalSystemIncome), Equals, true,
		Commentf("total deducted %s exceeds system income %s", totalDeducted.String(), originalSystemIncome.String()))

	// POL reserve (last in chain) should be squeezed — less than the naive 3000/10000 share
	naivePOLShare := common.GetSafeShare(cosmos.NewUint(3000), cosmos.NewUint(10_000), originalSystemIncome)
	c.Check(polReserveDeduct.LT(naivePOLShare), Equals, true,
		Commentf("polReserveDeduct %s should be less than naive %s", polReserveDeduct.String(), naivePOLShare.String()))
}

// TestCalcBlockRewardsRevShareReducesSystemIncome covers the new totalRevShare
// parameter to calcBlockRewards. With totalRevShare > 0 the systemIncome
// base shrinks, so every downstream deduction (devFund, burn, TCY stake,
// marketing, POL reserve, bond/pool rewards) must shrink with it. The
// existing TestCalcBlockRewards always passes ZeroUint, so this gap was
// uncovered.
func (s *NetworkManagerTestSuite) TestCalcBlockRewardsRevShareReducesSystemIncome(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)

	availablePoolsRune := cosmos.NewUint(1000 * common.One)
	vaultsLiquidityRune := cosmos.NewUint(1000 * common.One)
	effectiveSecurityBond := cosmos.NewUint(2000 * common.One)
	totalEffectiveBond := cosmos.NewUint(2000 * common.One)
	totalReserve := cosmos.NewUint(10000 * common.One)
	// Use a meaningful liquidity-fee base so revshare can move
	// systemIncome by an observable amount even when blockReward is
	// computed against the (mostly absent) reserve in the dummy keeper.
	totalLiquidityFees := cosmos.NewUint(100 * common.One)
	emissionCurve := constAccessor.GetInt64Value(constants.EmissionCurve)
	blocksPerYear := constAccessor.GetInt64Value(constants.BlocksPerYear)

	// Baseline: totalRevShare = 0.
	bondR0, poolR0, _, dev0, burn0, tcy0, mkt0, pol0 := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, cosmos.ZeroUint(),
		emissionCurve, blocksPerYear, 500, 500, 500, 500, 500)

	// With non-zero revshare: ~25% of liquidity-fee base diverted from
	// systemIncome. Every split should shrink versus baseline.
	totalRevShare := totalLiquidityFees.QuoUint64(4)
	bondR1, poolR1, _, dev1, burn1, tcy1, mkt1, pol1 := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, totalRevShare,
		emissionCurve, blocksPerYear, 500, 500, 500, 500, 500)

	c.Check(bondR1.LT(bondR0), Equals, true,
		Commentf("bond reward must shrink: with-revshare=%s baseline=%s", bondR1, bondR0))
	c.Check(poolR1.LT(poolR0), Equals, true,
		Commentf("pool reward must shrink: with-revshare=%s baseline=%s", poolR1, poolR0))
	c.Check(dev1.LT(dev0), Equals, true,
		Commentf("dev fund deduction must shrink: with-revshare=%s baseline=%s", dev1, dev0))
	c.Check(burn1.LT(burn0), Equals, true,
		Commentf("burn deduction must shrink: with-revshare=%s baseline=%s", burn1, burn0))
	c.Check(tcy1.LT(tcy0), Equals, true,
		Commentf("tcy stake deduction must shrink: with-revshare=%s baseline=%s", tcy1, tcy0))
	c.Check(mkt1.LT(mkt0), Equals, true,
		Commentf("marketing deduction must shrink: with-revshare=%s baseline=%s", mkt1, mkt0))
	c.Check(pol1.LT(pol0), Equals, true,
		Commentf("POL reserve deduction must shrink: with-revshare=%s baseline=%s", pol1, pol0))

	// Edge case: revshare equal to liquidity fees plus blockReward — the
	// SafeSub should clamp to zero rather than underflow, and every
	// downstream split should be zero.
	huge := totalLiquidityFees.MulUint64(1000)
	_, _, _, devH, burnH, tcyH, mktH, polH := networkMgr.calcBlockRewards(ctx,
		availablePoolsRune, vaultsLiquidityRune, effectiveSecurityBond,
		totalEffectiveBond, totalReserve, totalLiquidityFees, huge,
		emissionCurve, blocksPerYear, 500, 500, 500, 500, 500)
	c.Check(devH.IsZero(), Equals, true, Commentf("devFundDeduct should clamp to 0 when revshare exceeds systemIncome"))
	c.Check(burnH.IsZero(), Equals, true)
	c.Check(tcyH.IsZero(), Equals, true)
	c.Check(mktH.IsZero(), Equals, true)
	c.Check(polH.IsZero(), Equals, true)
}

// TestRevShareDeductionBeforeSplits exercises the REVSHARE deduction loop. It
// seeds the accumulator, sets REVSHARE mimir for a thorname, invokes
// processRevShare, and asserts funds flow from the Reserve to the
// AffiliateCollector module via the thorname owner's collector entry, and
// that the accumulator is cleared.
func (s *NetworkManagerTestSuite) TestRevShareDeductionBeforeSplits(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// register a thorname with an owner.
	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)

	// set REVSHARE-AFFX to 2500 bps.
	k.SetMimir(ctx, "REVSHARE-AFFX", 2500)

	// seed accumulator with a known fee.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affx", cosmos.NewUint(1000)), IsNil)

	// ensure reserve has enough for the payout.
	FundModule(c, ctx, k, ReserveName, 10_000)

	affcolBefore, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	// Exercise the REVSHARE drain loop directly.
	totalRevShare := networkMgr.processRevShare(ctx, mgr)

	// expected payout = 1000 * 2500 / 10000 = 250.
	expectedPayout := cosmos.NewUint(250)
	c.Check(totalRevShare.Equal(expectedPayout), Equals, true,
		Commentf("totalRevShare = %s, want %s", totalRevShare.String(), expectedPayout.String()))

	affcolAfter, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(affcolAfter.RuneAmount.Sub(affcolBefore.RuneAmount).Equal(expectedPayout), Equals, true,
		Commentf("affiliate collector delta = %s, want %s",
			affcolAfter.RuneAmount.Sub(affcolBefore.RuneAmount).String(), expectedPayout.String()))

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserveBefore.Sub(reserveAfter).Equal(expectedPayout), Equals, true,
		Commentf("reserve delta = %s, want %s",
			reserveBefore.Sub(reserveAfter).String(), expectedPayout.String()))

	// accumulator should be drained.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// The spec invariant is "one event per accumulator entry, always" —
	// assert it on the happy path too so a regression that drops the
	// event only on success doesn't slip past the other tests (which
	// exercise no-mimir / expired / disabled paths).
	var revShareEvt *cosmos.Event
	for i, evt := range ctx.EventManager().Events() {
		if evt.Type == "rev_share" {
			c.Check(revShareEvt, IsNil,
				Commentf("expected exactly one rev_share event on happy path, found more than one"))
			revShareEvt = &ctx.EventManager().Events()[i]
		}
	}
	c.Assert(revShareEvt, NotNil,
		Commentf("rev_share event must be emitted on the successful-payout path"))

	attrs := map[string]string{}
	for _, a := range revShareEvt.Attributes {
		attrs[a.Key] = a.Value
	}
	c.Check(attrs["thorname"], Equals, "AFFX")
	c.Check(attrs["owner"], Equals, ownerAddr.String(),
		Commentf("event owner must equal the registered thorname owner, got %q", attrs["owner"]))
	c.Check(attrs["bps"], Equals, "2500")
	c.Check(attrs["payout"], Equals, expectedPayout.String())
	c.Check(attrs["accrued_fee"], Equals, "1000")
}

// TestRevShareEventEvenWhenNoMimirSet asserts the rev_share event is emitted
// for every thorname with a non-zero accumulator even when REVSHARE mimir is
// unset, so downstream indexers can track gross attribution. No funds should
// move when bps=0.
func (s *NetworkManagerTestSuite) TestRevShareEventEvenWhenNoMimirSet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affy", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)

	// deliberately DO NOT set REVSHARE mimir.

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affy", cosmos.NewUint(1000)), IsNil)

	FundModule(c, ctx, k, ReserveName, 10_000)

	affcolBefore, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.IsZero(), Equals, true,
		Commentf("totalRevShare must be zero when no REVSHARE mimir is set, got %s", totalRevShare.String()))

	// collector must NOT have changed (no bps set -> no payout).
	affcolAfter, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(affcolAfter.RuneAmount.Equal(affcolBefore.RuneAmount), Equals, true,
		Commentf("collector changed when no REVSHARE mimir was set: before=%s after=%s",
			affcolBefore.RuneAmount.String(), affcolAfter.RuneAmount.String()))

	// accumulator should still be drained.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// the rev_share event must be emitted regardless of bps setting, and
	// when no REVSHARE mimir is set the emitted bps attribute must be "0"
	// (not the GetMimirWithRef "unset" sentinel) per the NewEventRevShare
	// contract — otherwise indexers see a confusing negative value.
	emitted := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != "rev_share" {
			continue
		}
		emitted = true
		var bpsAttr string
		for _, a := range evt.Attributes {
			if a.Key == "bps" {
				bpsAttr = a.Value
				break
			}
		}
		c.Check(bpsAttr, Equals, "0",
			Commentf("rev_share bps attribute must be 0 when REVSHARE mimir is unset, got %q", bpsAttr))
		break
	}
	c.Check(emitted, Equals, true, Commentf("rev_share event was not emitted"))
}

// TestRevShareExpiredThornameNoPayoutButEventEmitted asserts that a thorname
// whose ExpireBlockHeight is in the past does NOT receive a payout (no
// collector / reserve change) but a rev_share event IS still emitted (with
// empty owner and zero payout) so indexers can track every accumulator entry.
// This prevents orphaning RUNE while maintaining the spec invariant that
// every accrual produces exactly one event.
func (s *NetworkManagerTestSuite) TestRevShareExpiredThornameNoPayoutButEventEmitted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// Register a thorname that is ALREADY EXPIRED (ExpireBlockHeight < current).
	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affz", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	name.ExpireBlockHeight = ctx.BlockHeight() - 1 // expired
	k.SetTHORName(ctx, name)

	// REVSHARE mimir set, but thorname is expired.
	k.SetMimir(ctx, "REVSHARE-AFFZ", 2500)

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affz", cosmos.NewUint(1000)), IsNil)

	FundModule(c, ctx, k, ReserveName, 10_000)

	collectorBefore, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.IsZero(), Equals, true,
		Commentf("expired thorname must not contribute to totalRevShare, got %s", totalRevShare.String()))

	collectorAfter, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(collectorAfter.RuneAmount.Equal(collectorBefore.RuneAmount), Equals, true,
		Commentf("expired thorname must not receive payout"))

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserveBefore.Equal(reserveAfter), Equals, true,
		Commentf("reserve must not change for expired thorname"))

	// Accumulator must still be cleared (always drain invariant).
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// A rev_share event MUST be emitted even for the expired thorname, with
	// empty owner and zero payout, so indexers never undercount accruals.
	var revShareEvt *cosmos.Event
	for i, evt := range ctx.EventManager().Events() {
		if evt.Type == "rev_share" {
			c.Check(revShareEvt, IsNil,
				Commentf("expected exactly one rev_share event, found more than one"))
			revShareEvt = &ctx.EventManager().Events()[i]
		}
	}
	c.Assert(revShareEvt, NotNil,
		Commentf("rev_share event must be emitted even when thorname is expired"))

	attrs := map[string]string{}
	for _, a := range revShareEvt.Attributes {
		attrs[a.Key] = a.Value
	}
	c.Check(attrs["thorname"], Equals, "AFFZ",
		Commentf("rev_share event should carry the thorname"))
	c.Check(attrs["payout"], Equals, "0",
		Commentf("rev_share event payout should be 0 for expired thorname"))
	c.Check(attrs["bps"], Equals, "2500",
		Commentf("rev_share event should carry the mimir bps even when thorname is expired"))
	c.Check(attrs["owner"], Equals, "",
		Commentf("rev_share event owner should be empty for expired thorname, got %q", attrs["owner"]))
}

// TestRevShareExplicitlyDisabledNoPayoutButEventEmitted covers the
// realistic operator path where REVSHARE-<name> is explicitly set to 0
// (not just absent). The mimir validation already exercises the 0-bps
// accept-path, but processRevShare was previously only tested with
// bps=2500 (active), no mimir set, and an expired thorname — never with
// an explicit zero. Combined with the prior bps=-1 sentinel-leak bug,
// the unset-vs-explicit-zero gap is what allowed that issue to slip
// through. Lock the explicit-disable behavior down here.
func (s *NetworkManagerTestSuite) TestRevShareExplicitlyDisabledNoPayoutButEventEmitted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affd", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)

	// REVSHARE-AFFD explicitly disabled (operator value 0, not unset).
	k.SetMimir(ctx, "REVSHARE-AFFD", 0)

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affd", cosmos.NewUint(1000)), IsNil)

	FundModule(c, ctx, k, ReserveName, 10_000)
	collectorBefore, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.IsZero(), Equals, true,
		Commentf("explicit bps=0 must not contribute to totalRevShare, got %s",
			totalRevShare.String()))

	collectorAfter, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(collectorAfter.RuneAmount.Equal(collectorBefore.RuneAmount), Equals, true,
		Commentf("collector must not change for explicit bps=0"))

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserveBefore.Equal(reserveAfter), Equals, true,
		Commentf("reserve must not change for explicit bps=0"))

	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// Event MUST still fire so indexers can track gross attribution
	// even on disabled thornames. The bps attribute MUST be "0" — not
	// the GetMimirWithRef "-1" sentinel that the constructor previously
	// leaked through unconditionally.
	var revShareEvt *cosmos.Event
	for i, evt := range ctx.EventManager().Events() {
		if evt.Type == "rev_share" {
			c.Check(revShareEvt, IsNil,
				Commentf("expected exactly one rev_share event, found more than one"))
			revShareEvt = &ctx.EventManager().Events()[i]
		}
	}
	c.Assert(revShareEvt, NotNil,
		Commentf("rev_share event must be emitted for explicitly disabled thorname"))

	attrs := map[string]string{}
	for _, a := range revShareEvt.Attributes {
		attrs[a.Key] = a.Value
	}
	c.Check(attrs["thorname"], Equals, "AFFD")
	c.Check(attrs["bps"], Equals, "0",
		Commentf("explicit-disable rev_share bps attribute must be 0, got %q", attrs["bps"]))
	c.Check(attrs["payout"], Equals, "0")
	c.Check(attrs["accrued_fee"], Equals, "1000",
		Commentf("accrued_fee must reflect gross attribution even when payout is 0"))
}

// TestRevShareRoundedToZeroPayoutEventEmitted covers the bps>0 + fee>0 +
// GetSafeShare-rounds-to-zero branch in processRevShare: the
// `if !payout.IsZero()` guard at the credit-and-transfer step is the only
// place this branch is exercised, and a regression that moved event
// emission inside that guard would silently break the "one event per
// accumulator entry, always" invariant. Pin the branch with a tiny fee
// (1) and small bps (100) so the integer payout rounds to zero.
func (s *NetworkManagerTestSuite) TestRevShareRoundedToZeroPayoutEventEmitted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affr", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)

	// bps=100 (1%) with fee=1 → 100*1/10_000 = 0 after integer truncation.
	k.SetMimir(ctx, "REVSHARE-AFFR", 100)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affr", cosmos.NewUint(1)), IsNil)

	FundModule(c, ctx, k, ReserveName, 10_000)
	collectorBefore, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.IsZero(), Equals, true,
		Commentf("rounded-to-zero payout must not contribute to totalRevShare, got %s",
			totalRevShare.String()))

	collectorAfter, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(collectorAfter.RuneAmount.Equal(collectorBefore.RuneAmount), Equals, true,
		Commentf("collector must not change when payout rounds to zero"))

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserveBefore.Equal(reserveAfter), Equals, true,
		Commentf("reserve must not change when payout rounds to zero"))

	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// The event MUST still fire even though payout rounded to zero — this
	// is the one-event-per-entry invariant. A regression that moves the
	// EmitEvent call inside the `if !payout.IsZero()` block would skip it
	// on this branch, and only this test would catch it.
	var revShareEvt *cosmos.Event
	for i, evt := range ctx.EventManager().Events() {
		if evt.Type == "rev_share" {
			c.Check(revShareEvt, IsNil,
				Commentf("expected exactly one rev_share event, found more than one"))
			revShareEvt = &ctx.EventManager().Events()[i]
		}
	}
	c.Assert(revShareEvt, NotNil,
		Commentf("rev_share event must be emitted even when payout rounds to zero"))

	attrs := map[string]string{}
	for _, a := range revShareEvt.Attributes {
		attrs[a.Key] = a.Value
	}
	c.Check(attrs["thorname"], Equals, "AFFR")
	c.Check(attrs["owner"], Equals, ownerAddr.String(),
		Commentf("owner must be populated when the thorname exists (payout rounding does not affect attribution)"))
	c.Check(attrs["bps"], Equals, "100",
		Commentf("bps must reflect the configured mimir even when payout rounds to zero"))
	c.Check(attrs["payout"], Equals, "0")
	c.Check(attrs["accrued_fee"], Equals, "1",
		Commentf("accrued_fee must reflect gross attribution even when payout rounds to zero"))
}

// THORNames sharing an owner retain separate REVSHARE balances.
func (s *NetworkManagerTestSuite) TestRevShareMultiThornameSameOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// One owner, two thornames. Both REVSHARE mimirs set so both pay out.
	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	nameA := NewTHORName("affa", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	nameA.Owner = ownerAddr
	k.SetTHORName(ctx, nameA)
	nameB := NewTHORName("affb", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	nameB.Owner = ownerAddr
	k.SetTHORName(ctx, nameB)

	k.SetMimir(ctx, "REVSHARE-AFFA", 2500) // 25%
	k.SetMimir(ctx, "REVSHARE-AFFB", 2000) // 20%

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affa", cosmos.NewUint(2000)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affb", cosmos.NewUint(5000)), IsNil)

	FundModule(c, ctx, k, ReserveName, 100_000)

	collectorBefore, err := k.GetAffiliateCollector(ctx, nameA.Name)
	c.Assert(err, IsNil)
	collectorBBefore, err := k.GetAffiliateCollector(ctx, nameB.Name)
	c.Assert(err, IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)

	// Expected: AFFA payout = 2000 * 2500 / 10000 = 500
	//           AFFB payout = 5000 * 2000 / 10000 = 1000
	expectedTotal := cosmos.NewUint(1500)
	c.Check(totalRevShare.Equal(expectedTotal), Equals, true,
		Commentf("totalRevShare = %s, want %s (sum of both thornames)",
			totalRevShare.String(), expectedTotal.String()))

	collectorAfter, err := k.GetAffiliateCollector(ctx, nameA.Name)
	c.Assert(err, IsNil)
	collectorDelta := collectorAfter.RuneAmount.Sub(collectorBefore.RuneAmount)
	c.Check(collectorDelta.Equal(cosmos.NewUint(500)), Equals, true)
	collectorBAfter, err := k.GetAffiliateCollector(ctx, nameB.Name)
	c.Assert(err, IsNil)
	c.Check(collectorBAfter.RuneAmount.Sub(collectorBBefore.RuneAmount).Equal(cosmos.NewUint(1000)), Equals, true)

	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserveBefore.Sub(reserveAfter).Equal(expectedTotal), Equals, true,
		Commentf("reserve delta = %s, want %s",
			reserveBefore.Sub(reserveAfter).String(), expectedTotal.String()))

	// Both rev_share events must be emitted, one per thorname.
	seen := map[string]bool{}
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != "rev_share" {
			continue
		}
		for _, a := range evt.Attributes {
			if a.Key == "thorname" {
				seen[a.Value] = true
			}
		}
	}
	c.Check(seen["AFFA"], Equals, true, Commentf("rev_share event for AFFA missing"))
	c.Check(seen["AFFB"], Equals, true, Commentf("rev_share event for AFFB missing"))

	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)
}

// TestRevShareReserveInsufficientRollback exercises the SendFromModuleToModule
// failure path. The Reserve is funded with enough RUNE for the first payout
// only; the second iteration's send fails. processRevShare must roll back
// the second entry's collector mutation (so the collector is consistent),
// still drain the full accumulator (per-block invariant), still emit a
// rev_share event for the failed entry (with payout=0), and not include
// the failed entry's payout in the returned totalRevShare.
func (s *NetworkManagerTestSuite) TestRevShareReserveInsufficientRollback(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// Two thornames with distinct owners so we can observe per-owner
	// rollback independently.
	ownerA := GetRandomBech32Addr()
	ownerB := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	nameA := NewTHORName("affa", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	nameA.Owner = ownerA
	k.SetTHORName(ctx, nameA)
	nameB := NewTHORName("affb", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	nameB.Owner = ownerB
	k.SetTHORName(ctx, nameB)

	k.SetMimir(ctx, "REVSHARE-AFFA", 2500)
	k.SetMimir(ctx, "REVSHARE-AFFB", 2500)

	// Each fee yields a 250 payout (1000 * 2500 / 10000). Seed both,
	// then drain the pre-funded Reserve to leave just 250 — enough for
	// AFFA's payout but not AFFB's.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affa", cosmos.NewUint(1000)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affb", cosmos.NewUint(1000)), IsNil)

	// setupManagerForTest pre-funds the Reserve generously; drain to a
	// known small balance so the second payout exceeds it.
	preFunded := k.GetRuneBalanceOfModule(ctx, ReserveName)
	if preFunded.GT(cosmos.NewUint(250)) {
		drain := preFunded.Sub(cosmos.NewUint(250))
		c.Assert(k.SendFromModuleToModule(ctx, ReserveName, ModuleName,
			common.NewCoins(common.NewCoin(common.RuneNative, drain))), IsNil)
	}
	c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(cosmos.NewUint(250)), Equals, true,
		Commentf("Reserve balance should be exactly 250 after drain"))

	collectorABefore, err := k.GetAffiliateCollector(ctx, nameA.Name)
	c.Assert(err, IsNil)
	collectorBBefore, err := k.GetAffiliateCollector(ctx, nameB.Name)
	c.Assert(err, IsNil)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)

	// Iteration order is lexicographic, so AFFA pays out first (250),
	// then AFFB's send fails because the Reserve is empty.
	c.Check(totalRevShare.Equal(cosmos.NewUint(250)), Equals, true,
		Commentf("totalRevShare = %s, want 250 (only AFFA's send should succeed)",
			totalRevShare.String()))

	// Successful entry: collector grew by the payout amount.
	collectorAAfter, err := k.GetAffiliateCollector(ctx, nameA.Name)
	c.Assert(err, IsNil)
	c.Check(collectorAAfter.RuneAmount.Sub(collectorABefore.RuneAmount).Equal(cosmos.NewUint(250)),
		Equals, true,
		Commentf("AFFA collector should have grown by 250, got delta %s",
			collectorAAfter.RuneAmount.Sub(collectorABefore.RuneAmount).String()))

	// Rolled-back entry: collector unchanged from the pre-call state.
	collectorBAfter, err := k.GetAffiliateCollector(ctx, nameB.Name)
	c.Assert(err, IsNil)
	c.Check(collectorBAfter.RuneAmount.Equal(collectorBBefore.RuneAmount), Equals, true,
		Commentf("AFFB collector must be rolled back to %s after failed send, got %s",
			collectorBBefore.RuneAmount.String(), collectorBAfter.RuneAmount.String()))

	// Reserve was fully drained by AFFA's payout; nothing left for AFFB.
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true)

	// Per-block-only invariant: the accumulator MUST be drained even when
	// some payouts fail.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// Both rev_share events must fire — one with payout=250 (AFFA), one
	// with payout=0 (AFFB rollback).
	events := map[string]map[string]string{}
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != "rev_share" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range evt.Attributes {
			attrs[a.Key] = a.Value
		}
		events[attrs["thorname"]] = attrs
	}
	c.Assert(events["AFFA"], NotNil, Commentf("rev_share event for AFFA missing"))
	c.Check(events["AFFA"]["payout"], Equals, "250")
	c.Assert(events["AFFB"], NotNil, Commentf("rev_share event for AFFB missing on rollback path"))
	c.Check(events["AFFB"]["payout"], Equals, "0",
		Commentf("rolled-back AFFB event must report payout=0, got %q", events["AFFB"]["payout"]))
}

// TestRevShareMissingThornameNoPayoutButEventEmitted covers the case where
// the accumulator carries an entry for a thorname that GetTHORName cannot
// resolve at all (returns a non-nil error, distinct from the registered-
// but-expired branch). Both branches share the canPay=false path in
// processRevShare, but only the expired case is exercised by
// TestRevShareExpiredThornameNoPayoutButEventEmitted; this test pins
// down the err != nil leg.
func (s *NetworkManagerTestSuite) TestRevShareMissingThornameNoPayoutButEventEmitted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// Deliberately do NOT register the thorname: GetTHORName will return
	// `THORName doesn't exist`. Set the REVSHARE mimir anyway so the bps
	// path is exercised.
	k.SetMimir(ctx, "REVSHARE-GHOST", 2500)

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "ghost", cosmos.NewUint(1000)), IsNil)

	FundModule(c, ctx, k, ReserveName, 10_000)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.IsZero(), Equals, true,
		Commentf("missing thorname must not contribute to totalRevShare, got %s",
			totalRevShare.String()))

	// Reserve must be unchanged — no transfer should happen for an
	// unresolvable thorname.
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true,
		Commentf("Reserve must not change for missing thorname"))

	// Accumulator must still be cleared (always-drain invariant).
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0)

	// A rev_share event MUST still fire — indexers should see every
	// accrual, even unresolvable ones, with empty owner and zero payout.
	var revShareEvt *cosmos.Event
	for i, evt := range ctx.EventManager().Events() {
		if evt.Type == "rev_share" {
			c.Check(revShareEvt, IsNil,
				Commentf("expected exactly one rev_share event, found more than one"))
			revShareEvt = &ctx.EventManager().Events()[i]
		}
	}
	c.Assert(revShareEvt, NotNil,
		Commentf("rev_share event must be emitted for missing thorname"))

	attrs := map[string]string{}
	for _, a := range revShareEvt.Attributes {
		attrs[a.Key] = a.Value
	}
	c.Check(attrs["thorname"], Equals, "GHOST")
	c.Check(attrs["payout"], Equals, "0")
	c.Check(attrs["bps"], Equals, "2500")
	c.Check(attrs["owner"], Equals, "",
		Commentf("missing-thorname event owner should be empty, got %q", attrs["owner"]))
}

// TestUpdateNetworkDrainsRevShareBeforeEarlyReturn locks in the documented
// invariant at manager_network_current.go:1726-1730: processRevShare must
// run BEFORE the totalReserve / availablePoolsRune early-return guards in
// UpdateNetwork. A regression that moves the drain below either guard
// would leak accumulator entries across blocks, violating the per-block-only
// spec — this test would fail in that case.
func (s *NetworkManagerTestSuite) TestUpdateNetworkDrainsRevShareBeforeEarlyReturn(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(k.SetNetwork(ctx, NewNetwork()), IsNil)

	// Register a thorname with REVSHARE active so the would-be payout
	// path runs end-to-end (and gracefully no-ops when the Reserve is
	// empty), exercising the drain in full.
	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	k.SetTHORName(ctx, name)
	k.SetMimir(ctx, "REVSHARE-AFFX", 2500)

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affx", cosmos.NewUint(1000)), IsNil)

	// Drain the pre-funded Reserve to zero so totalReserve.IsZero() in
	// UpdateNetwork triggers the early return at line 1735. With the
	// drain running BEFORE that guard, the accumulator must still be
	// cleared by the time UpdateNetwork returns.
	preFunded := k.GetRuneBalanceOfModule(ctx, ReserveName)
	if !preFunded.IsZero() {
		c.Assert(k.SendFromModuleToModule(ctx, ReserveName, ModuleName,
			common.NewCoins(common.NewCoin(common.RuneNative, preFunded))), IsNil)
	}
	c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true,
		Commentf("Reserve should be empty so UpdateNetwork takes the totalReserve.IsZero early-return"))

	// Sanity: accumulator has the entry we just seeded.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 1)

	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	// The accumulator MUST be drained even though UpdateNetwork bailed
	// out at the totalReserve.IsZero guard. If the drain were moved
	// below the guard, this entry would persist into the next block.
	got = k.GetAffiliateLiquidityFees(ctx)
	c.Check(got, HasLen, 0,
		Commentf("processRevShare must run before totalReserve.IsZero early-return; "+
			"accumulator still has %d entries after UpdateNetwork", len(got)))
}

// TestUpdateNetworkBurnDoesNotUnderflow locks in the guard at
// manager_network_current.go:1940-1942: when system income burning is enabled
// and the MaxRuneSupply mimir is smaller than the amount of RUNE burnt this
// block, decrementing it must clamp to zero via common.SafeSub rather than
// panicking on cosmos.Uint underflow. UpdateNetwork runs in EndBlock, so a
// panic here would halt the chain. A regression that reverts to plain .Sub
// would panic in this test instead of returning nil.
func (s *NetworkManagerTestSuite) TestUpdateNetworkBurnDoesNotUnderflow(c *C) {
	ctx, mgr := setupManagerForTest(c)
	helper := NewVaultGenesisSetupTestHelper(mgr.Keeper())
	mgr.K = helper
	networkMgr := newNetworkMgr(helper, mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(mgr.Keeper().SetNetwork(ctx, NewNetwork()), IsNil)

	// Available pool with RUNE so UpdateNetwork passes the availablePoolsRune guard.
	p := NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(common.One * 100)
	p.BalanceAsset = cosmos.NewUint(common.One * 100)
	p.Status = PoolAvailable
	c.Assert(helper.SetPool(ctx, p), IsNil)

	// Vault for getVaultsLiquidityRune.
	vault := NewVault(0, ActiveVault, AsgardVault, GetRandomPubKey(), []string{p.Asset.GetChain().String()}, []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(p.Asset, p.BalanceAsset))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Liquidity fees + active bonds produce non-zero system income.
	c.Assert(helper.Keeper.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(50*common.One)), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(helper.Keeper.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)

	// Enable burning (mocknet defaults this to 0) and set MaxRuneSupply to a
	// value far below the amount that will be burnt this block, so the
	// decrement would underflow without the SafeSub guard.
	mgr.Keeper().SetMimir(ctx, constants.SystemIncomeBurnRateBps.String(), 5_000)
	mgr.Keeper().SetMimir(ctx, constants.MaxRuneSupply.String(), 1)

	// Sanity: burning is enabled, so a non-zero burn will be produced from the
	// block reward + liquidity fees above (the actual deduct is verified
	// indirectly by the MaxRuneSupply clamp assertion below).
	burnRateBps := mgr.Keeper().GetConfigInt64(ctx, constants.SystemIncomeBurnRateBps)
	c.Assert(burnRateBps > 0, Equals, true)

	// Must not panic on underflow; the EndBlock path returns nil.
	c.Assert(networkMgr.UpdateNetwork(ctx, mgr), IsNil)

	// SafeSub clamps the decremented MaxRuneSupply mimir to zero.
	c.Check(mgr.Keeper().GetConfigInt64(ctx, constants.MaxRuneSupply), Equals, int64(0))
}

// TestProcessRevShareTriggersPreferredAssetFlush asserts that when a thorname
// has a preferred asset configured, a successful REVSHARE credit that pushes
// the AffiliateCollector balance over the threshold triggers
// triggerPreferredAssetSwap — clearing the collector and queuing a swap.
//
// Without this trigger, a thorname that ONLY receives REVSHARE (no user-side
// affiliate cuts via the skim path) would accumulate RUNE in the collector
// indefinitely and never flush to its preferred asset.
func (s *NetworkManagerTestSuite) TestProcessRevShareTriggersPreferredAssetFlush(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// Pool needed so triggerPreferredAssetSwap's downstream consumers don't
	// blow up when reasoning about the preferred asset.
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Register a thorname with BTC.BTC as preferred asset and a BTC alias.
	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	btcAddr := GetRandomBTCAddress()
	name := NewTHORName("affp", 1000, []THORNameAlias{
		{Chain: common.THORChain, Address: thorAddr},
		{Chain: common.BTCChain, Address: btcAddr},
	})
	name.Owner = ownerAddr
	name.PreferredAsset = common.BTCAsset
	// Per-thorname multiplier of 1 so the threshold is just outboundFee — a
	// modest REVSHARE credit will cross it without astronomical fees.
	name.PreferredAssetOutboundFeeMultiplier = 1
	k.SetTHORName(ctx, name)

	// REVSHARE-AFFP at 5000 bps so half the accrued fee becomes the payout.
	k.SetMimir(ctx, "REVSHARE-AFFP", 5000)

	// Seed the accumulator with a fee large enough that 50% well exceeds
	// the BTC outbound fee × 1.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affp", cosmos.NewUint(100*common.One)), IsNil)

	FundModule(c, ctx, k, ReserveName, 100*common.One)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	// payout = 100*One * 5000 / 10000 = 50*One.
	expectedPayout := cosmos.NewUint(50 * common.One)
	c.Check(totalRevShare.Equal(expectedPayout), Equals, true,
		Commentf("totalRevShare = %s, want %s", totalRevShare.String(), expectedPayout.String()))

	// triggerPreferredAssetSwap zeroes the AffiliateCollector RuneAmount as
	// part of its successful flush. If our threshold trigger ran, the
	// collector should be empty.
	col, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(col.RuneAmount.IsZero(), Equals, true,
		Commentf("AffiliateCollector.RuneAmount should be zero after preferred-asset flush, got %s",
			col.RuneAmount.String()))

	// And a swap-queue item must exist for the preferred-asset swap. The
	// queue used depends on AdvSwapQueueEnabled — count both so the test
	// is resilient to the test fixture's mode.
	queued := countQueuedSwaps(k, ctx)
	c.Check(queued > 0, Equals, true,
		Commentf("expected at least one queued preferred-asset swap, found %d", queued))
}

// countQueuedSwaps counts swap-queue items across both the v1 and the
// advanced-mode queues so callers don't have to know which is enabled.
func countQueuedSwaps(k keeper.Keeper, ctx cosmos.Context) int {
	count := 0
	iter := k.GetSwapQueueIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		count++
	}
	iter.Close()
	advIter := k.GetAdvSwapQueueItemIterator(ctx)
	for ; advIter.Valid(); advIter.Next() {
		count++
	}
	advIter.Close()
	return count
}

// TestProcessRevShareNoPreferredAssetSkipsFlush asserts the negative case:
// when the thorname has NO preferred asset, processRevShare must NOT attempt
// to trigger a preferred-asset swap (no swap queue item, collector retains
// its credit). This verifies the !tn.PreferredAsset.IsEmpty() guard.
func (s *NetworkManagerTestSuite) TestProcessRevShareNoPreferredAssetSkipsFlush(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affn", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	name.Owner = ownerAddr
	// Deliberately leave PreferredAsset empty.
	k.SetTHORName(ctx, name)

	k.SetMimir(ctx, "REVSHARE-AFFN", 5000)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affn", cosmos.NewUint(100*common.One)), IsNil)
	FundModule(c, ctx, k, ReserveName, 100*common.One)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.Equal(cosmos.NewUint(50*common.One)), Equals, true)

	// The collector retains the credit since there's no preferred asset to
	// flush to.
	col, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(col.RuneAmount.Equal(cosmos.NewUint(50*common.One)), Equals, true,
		Commentf("AffiliateCollector should retain its credit when no preferred asset, got %s",
			col.RuneAmount.String()))

	// No swap should have been queued.
	c.Check(countQueuedSwaps(k, ctx), Equals, 0,
		Commentf("no preferred-asset swap should be queued when thorname has no preferred asset"))
}

func (s *NetworkManagerTestSuite) TestProcessRevShareCollectorErrorsLeaveBalancesUnchanged(c *C) {
	for _, failure := range []string{"read", "write", "transfer"} {
		ctx, mgr, _, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		fundNamedCollector(c, ctx, k, "few", 20)
		FundModule(c, ctx, k, ReserveName, 100)
		k.SetMimir(ctx, "REVSHARE-FEW", 5000)
		c.Assert(k.AddAffiliateLiquidityFee(ctx, "few", cosmos.NewUint(20)), IsNil)
		beforeReserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
		fault := &collectorFailKeeper{Keeper: k, failRead: failure == "read", failWrite: failure == "write", failTransfer: failure == "transfer"}
		mgr.K = fault
		networkMgr := newNetworkMgr(fault, mgr.TxOutStore(), mgr.EventMgr())
		c.Assert(networkMgr.processRevShare(ctx, mgr).IsZero(), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(beforeReserve), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, "few", 20)
		assertAffiliateIntegrationInvariant(c, ctx, k)
		c.Check(k.GetAffiliateLiquidityFees(ctx), HasLen, 0)
		c.Check(countQueuedSwaps(k, ctx), Equals, 0)
	}
}

// TestProcessRevShareBelowThresholdNoFlush asserts that a REVSHARE credit
// that does NOT push the collector over the preferred-asset threshold does
// not trigger a swap. The collector should retain the credit; a future
// REVSHARE credit will eventually cross the threshold and flush.
func (s *NetworkManagerTestSuite) TestProcessRevShareBelowThresholdNoFlush(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	ownerAddr := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	btcAddr := GetRandomBTCAddress()
	name := NewTHORName("affb", 1000, []THORNameAlias{
		{Chain: common.THORChain, Address: thorAddr},
		{Chain: common.BTCChain, Address: btcAddr},
	})
	name.Owner = ownerAddr
	name.PreferredAsset = common.BTCAsset
	// A very high multiplier — our small payout cannot exceed
	// outboundFee × 1_000_000.
	name.PreferredAssetOutboundFeeMultiplier = 1_000_000
	k.SetTHORName(ctx, name)

	k.SetMimir(ctx, "REVSHARE-AFFB", 5000)
	// Tiny fee → tiny payout → below threshold.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "affb", cosmos.NewUint(1000)), IsNil)
	FundModule(c, ctx, k, ReserveName, 100*common.One)

	totalRevShare := networkMgr.processRevShare(ctx, mgr)
	c.Check(totalRevShare.Equal(cosmos.NewUint(500)), Equals, true)

	// Collector retained the credit (no flush).
	col, err := k.GetAffiliateCollector(ctx, name.Name)
	c.Assert(err, IsNil)
	c.Check(col.RuneAmount.Equal(cosmos.NewUint(500)), Equals, true,
		Commentf("collector should retain credit below threshold, got %s",
			col.RuneAmount.String()))

	// No swap queued.
	c.Check(countQueuedSwaps(k, ctx), Equals, 0,
		Commentf("no swap should be queued below threshold"))
}

// txOutStoreDustDummy fails every outbound with ErrNotEnoughToPayFee while
// `dust` is set, so the ragnarok dust path can be exercised without standing up
// a real vault/gas setup.
type txOutStoreDustDummy struct {
	*TxOutStoreDummy
	dust bool
}

func (tos *txOutStoreDustDummy) TryAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, minOut cosmos.Uint) (bool, error) {
	if tos.dust {
		return false, ErrNotEnoughToPayFee
	}
	return tos.TxOutStoreDummy.TryAddTxOutItem(ctx, mgr, toi, minOut)
}

// setupDustLPRagnarok builds a pool plus a dual-address LP holding both units
// and a staged pending RUNE add, with a tx out store that reports every outbound
// as dust.
func setupDustLPRagnarok(c *C) (cosmos.Context, keeper.Keeper, Pool, LiquidityProvider, NodeAccount, *DummyMgr, *txOutStoreDustDummy, cosmos.Uint) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100000)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	pendingRune := cosmos.NewUint(30 * common.One)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	// More pool units than this LP holds, so the RUNE-only retry is not a 100%
	// one-sided drain of the pool (which withdraw() rejects outright) and the
	// dust path is what is actually exercised.
	pool.LPUnits = cosmos.NewUint(3200)
	pool.PendingInboundRune = pendingRune
	pool.Status = PoolStaged
	c.Assert(k.SetPool(ctx, pool), IsNil)

	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		RuneAddress:       GetRandomRUNEAddress(),
		AssetAddress:      GetRandomBTCAddress(),
		LastAddHeight:     5,
		Units:             cosmos.NewUint(1600),
		PendingRune:       pendingRune,
		PendingAsset:      cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
	}
	k.SetLiquidityProvider(ctx, lp)

	mgr := NewDummyMgrWithKeeper(k)
	dustStore := &txOutStoreDustDummy{TxOutStoreDummy: NewTxStoreDummy(), dust: true}
	mgr.txOutStore = dustStore

	return ctx, k, pool, lp, na, mgr, dustStore, pendingRune
}

// TestWithdrawLPsDustUnitsRetainsPendingClaim verifies that when the unit
// withdrawal of an LP which also holds staged pending liquidity is dust, the
// record is not deleted: only the unpayable units are dropped, so the pending
// claim survives and is paid on a later pass. Regression for issue #2972.
func (s *NetworkManagerTestSuite) TestWithdrawLPsDustUnitsRetainsPendingClaim(c *C) {
	ctx, k, pool, lp, na, mgr, dustStore, pendingRune := setupDustLPRagnarok(c)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	// First pass: every outbound is dust, so the unit withdraw fails. The pending
	// claim was never attempted and must not be forfeited with the record.
	done := networkMgr.withdrawLPs(ctx, pool, na, mgr)
	c.Assert(done, Equals, false, Commentf("pool must not finalize while a pending LP claim is unpaid"))
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.PendingRune.Equal(pendingRune), Equals, true, Commentf("pending claim must survive the dust unit withdraw, got %s", got.PendingRune))
	// The dust units are dropped so the next pass takes withdraw()'s pending
	// branch instead of failing identically forever.
	c.Assert(got.Units.IsZero(), Equals, true, Commentf("dust units should be zeroed, got %s", got.Units))

	// Second pass, outbounds payable again: the pending claim is paid out and the
	// record removed.
	dustStore.dust = false
	pool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	done = networkMgr.withdrawLPs(ctx, pool, na, mgr)
	c.Assert(done, Equals, true)
	got, err = k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.PendingRune.IsZero(), Equals, true, Commentf("pending claim must be paid out, got %s", got.PendingRune))

	pool, err = k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.PendingInboundRune.IsZero(), Equals, true, Commentf("pool pending inbound rune should be settled, got %s", pool.PendingInboundRune))
}

// TestWithdrawLPsDustPendingRemovesLP verifies the dust path still terminates:
// once the pending claim itself is proven unpayable the record is removed, so
// retaining it above cannot livelock ragnarok.
func (s *NetworkManagerTestSuite) TestWithdrawLPsDustPendingRemovesLP(c *C) {
	ctx, k, pool, lp, na, mgr, _, _ := setupDustLPRagnarok(c)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(networkMgr.withdrawLPs(ctx, pool, na, mgr), Equals, false)

	// Second pass with everything still dust: the pending leg is now attempted
	// and proven unpayable too, so the record is removed and the pool finalizes.
	pool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(networkMgr.withdrawLPs(ctx, pool, na, mgr), Equals, true)
	got, err := k.GetLiquidityProvider(ctx, common.BTCAsset, lp.RuneAddress)
	c.Assert(err, IsNil)
	c.Assert(got.PendingRune.IsZero(), Equals, true, Commentf("dust pending record should be removed, got %s", got.PendingRune))
}
