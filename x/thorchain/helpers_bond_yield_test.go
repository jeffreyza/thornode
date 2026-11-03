package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HelperBondYieldSuite struct{}

var _ = Suite(&HelperBondYieldSuite{})

func (s *HelperBondYieldSuite) TestBondYieldEnabled(c *C) {
	ctx, mgr := setupManagerForTest(c)

	c.Check(isBondYieldEnabled(ctx, mgr), Equals, false)

	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)
	c.Check(isBondYieldEnabled(ctx, mgr), Equals, true)

	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 0)
	c.Check(isBondYieldEnabled(ctx, mgr), Equals, false)
}

func (s *HelperBondYieldSuite) TestBondRewardSplitsYield(c *C) {
	acc := GetRandomBech32Addr()
	bp := NewBondProviders(acc)
	bp.Providers = []BondProvider{NewBondProvider(acc)}
	bp.Bond(cosmos.NewUint(1000), acc)
	c.Assert(bp.SetAutoClaim(acc, 2500, ""), Equals, true)

	rebonded, allocations, err := applyBondReward(&bp, acc, cosmos.NewUint(500), true)
	c.Assert(err, IsNil)
	c.Check(rebonded.Uint64(), Equals, uint64(375))
	c.Assert(allocations, HasLen, 1)
	c.Check(allocations[0].ProviderAddress.Equals(acc), Equals, true)
	c.Check(allocations[0].RewardShare.Uint64(), Equals, uint64(500))
	c.Check(allocations[0].PayoutAmount.Uint64(), Equals, uint64(125))
	c.Check(allocations[0].RebondedAmount.Uint64(), Equals, uint64(375))
	c.Check(allocations[0].Bps, Equals, uint64(2500))

	provider := bp.Get(acc)
	c.Check(provider.Bond.Uint64(), Equals, uint64(1375))
}

func (s *HelperBondYieldSuite) TestBondRewardSplitsMixedProviderBps(c *C) {
	operator := GetRandomBech32Addr()
	provider1 := GetRandomBech32Addr()
	provider2 := GetRandomBech32Addr()

	bp := NewBondProviders(operator)
	bp.Providers = []BondProvider{
		NewBondProvider(operator),
		NewBondProvider(provider1),
		NewBondProvider(provider2),
	}
	bp.Bond(cosmos.NewUint(600), operator)
	bp.Bond(cosmos.NewUint(300), provider1)
	bp.Bond(cosmos.NewUint(100), provider2)
	c.Assert(bp.SetAutoClaim(provider1, 4000, ""), Equals, true)
	c.Assert(bp.SetAutoClaim(provider2, constants.MaxBasisPts, ""), Equals, true)

	rebonded, allocations, err := applyBondReward(&bp, operator, cosmos.NewUint(1000), true)
	c.Assert(err, IsNil)
	c.Check(rebonded.Uint64(), Equals, uint64(780))
	c.Assert(allocations, HasLen, 3)

	c.Check(allocations[0].ProviderAddress.Equals(provider1), Equals, true)
	c.Check(allocations[0].RewardShare.Uint64(), Equals, uint64(300))
	c.Check(allocations[0].PayoutAmount.Uint64(), Equals, uint64(120))
	c.Check(allocations[0].RebondedAmount.Uint64(), Equals, uint64(180))
	c.Check(allocations[0].Bps, Equals, uint64(4000))

	c.Check(allocations[1].ProviderAddress.Equals(provider2), Equals, true)
	c.Check(allocations[1].RewardShare.Uint64(), Equals, uint64(100))
	c.Check(allocations[1].PayoutAmount.Uint64(), Equals, uint64(100))
	c.Check(allocations[1].RebondedAmount.IsZero(), Equals, true)
	c.Check(allocations[1].Bps, Equals, constants.MaxBasisPts)

	c.Check(allocations[2].ProviderAddress.Equals(operator), Equals, true)
	c.Check(allocations[2].RewardShare.Uint64(), Equals, uint64(600))
	c.Check(allocations[2].PayoutAmount.IsZero(), Equals, true)
	c.Check(allocations[2].RebondedAmount.Uint64(), Equals, uint64(600))
	c.Check(allocations[2].Bps, Equals, uint64(0))

	c.Check(bp.Get(operator).Bond.Uint64(), Equals, uint64(1200))
	c.Check(bp.Get(provider1).Bond.Uint64(), Equals, uint64(480))
	c.Check(bp.Get(provider2).Bond.Uint64(), Equals, uint64(100))
}

func (s *HelperBondYieldSuite) TestBondRewardRebondsWhenYieldDisabled(c *C) {
	acc := GetRandomBech32Addr()
	bp := NewBondProviders(acc)
	bp.Providers = []BondProvider{NewBondProvider(acc)}
	bp.Bond(cosmos.NewUint(1000), acc)

	rebonded, allocations, err := applyBondReward(&bp, acc, cosmos.NewUint(500), false)
	c.Assert(err, IsNil)
	c.Check(rebonded.Uint64(), Equals, uint64(500))
	c.Check(allocations, HasLen, 0)

	provider := bp.Get(acc)
	c.Check(provider.Bond.Uint64(), Equals, uint64(1500))
}

func (s *HelperBondYieldSuite) TestBondRewardRemainderUsesOperatorSlot(c *C) {
	operator := GetRandomBech32Addr()
	provider1 := GetRandomBech32Addr()
	provider2 := GetRandomBech32Addr()

	bp := NewBondProviders(operator)
	bp.Providers = []BondProvider{
		NewBondProvider(operator),
		NewBondProvider(provider1),
		NewBondProvider(provider2),
	}
	bp.Bond(cosmos.NewUint(1), operator)
	bp.Bond(cosmos.NewUint(1), provider1)
	bp.Bond(cosmos.NewUint(1), provider2)

	rebonded, allocations, err := applyBondReward(&bp, operator, cosmos.NewUint(10), false)
	c.Assert(err, IsNil)
	c.Check(rebonded.Uint64(), Equals, uint64(10))
	c.Check(allocations, HasLen, 0)
	c.Check(bp.Get(provider1).Bond.Uint64(), Equals, uint64(4))
	c.Check(bp.Get(provider2).Bond.Uint64(), Equals, uint64(4))
	c.Check(bp.Get(operator).Bond.Uint64(), Equals, uint64(5))
}

func (s *HelperBondYieldSuite) TestBondRewardCapsRoundedSharesAtReward(c *C) {
	operator := GetRandomBech32Addr()
	providers := []cosmos.AccAddress{
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
	}

	bp := NewBondProviders(operator)
	bp.Providers = []BondProvider{NewBondProvider(operator)}
	for _, provider := range providers {
		bp.Providers = append(bp.Providers, NewBondProvider(provider))
		bp.Bond(cosmos.NewUint(1_000_000_000_000), provider)
	}

	rebonded, allocations, err := applyBondReward(&bp, operator, cosmos.NewUint(3), false)
	c.Assert(err, IsNil)
	c.Check(rebonded.Uint64(), Equals, uint64(3))
	c.Check(allocations, HasLen, 0)
	c.Check(bp.Get(operator).Bond.IsZero(), Equals, true)
	for i, provider := range providers {
		expected := uint64(1_000_000_000_000)
		if i < 3 {
			expected++
		}
		c.Check(bp.Get(provider).Bond.Uint64(), Equals, expected)
	}
}

func (s *HelperBondYieldSuite) TestBondRewardAssertsOperatorFirst(c *C) {
	operator := GetRandomBech32Addr()
	provider := GetRandomBech32Addr()

	bp := NewBondProviders(operator)
	bp.Providers = []BondProvider{
		NewBondProvider(provider),
		NewBondProvider(operator),
	}
	bp.Bond(cosmos.NewUint(100), provider)
	bp.Bond(cosmos.NewUint(100), operator)

	rebonded, allocations, err := applyBondReward(&bp, operator, cosmos.NewUint(10), false)
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "bond provider operator invariant violation:.*")
	c.Check(rebonded.IsZero(), Equals, true)
	c.Check(allocations, HasLen, 0)
	c.Check(bp.Get(provider).Bond.Uint64(), Equals, uint64(100))
	c.Check(bp.Get(operator).Bond.Uint64(), Equals, uint64(100))
}

func (s *HelperBondYieldSuite) TestNormalizeBondProviderOperator(c *C) {
	operator := GetRandomBech32Addr()
	provider := GetRandomBech32Addr()

	bp := NewBondProviders(GetRandomBech32Addr())
	bp.Providers = []BondProvider{
		NewBondProvider(provider),
		NewBondProvider(operator),
	}
	bp.Providers[0].Bond = cosmos.NewUint(100)
	bp.Providers[1].Bond = cosmos.NewUint(200)
	bp.Providers[1].Bps = 4000
	bp.Providers[1].PayoutTarget = "yieldname"

	c.Assert(normalizeBondProviderOperator(&bp, operator), IsNil)
	c.Check(bp.Providers[0].BondAddress.Equals(operator), Equals, true)
	c.Check(bp.Providers[0].Bond.Uint64(), Equals, uint64(200))
	c.Check(bp.Providers[0].Bps, Equals, uint64(4000))
	c.Check(bp.Providers[0].PayoutTarget, Equals, "yieldname")
	c.Check(bp.Providers[1].BondAddress.Equals(provider), Equals, true)
	c.Check(bp.Providers[1].Bond.Uint64(), Equals, uint64(100))

	missing := GetRandomBech32Addr()
	err := normalizeBondProviderOperator(&bp, missing)
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "node operator.*not listed in bond providers")
}

func (s *HelperBondYieldSuite) TestBondRewardMatchesLegacyAdjustFeePath(c *C) {
	operator := GetRandomBech32Addr()
	provider1 := GetRandomBech32Addr()
	provider2 := GetRandomBech32Addr()
	nodeBond := cosmos.NewUint(123456789)
	reward := cosmos.NewUint(9876543)

	initial := NewBondProviders(operator)
	initial.NodeOperatorFee = cosmos.NewUint(777)
	initial.Providers = []BondProvider{
		NewBondProvider(operator),
		NewBondProvider(provider1),
		NewBondProvider(provider2),
	}
	initial.Bond(cosmos.NewUint(70000000), operator)
	initial.Bond(cosmos.NewUint(33333333), provider1)
	initial.Bond(cosmos.NewUint(20123456), provider2)
	c.Assert(initial.SetAutoClaim(provider1, 2500, ""), Equals, true)
	c.Assert(initial.SetAutoClaim(provider2, 7500, ""), Equals, true)

	legacyBP := cloneBondProviders(initial)
	legacyBP.Adjust(nodeBond)
	lastOperatorBond := legacyBP.Get(operator).Bond
	legacyNodeBond := nodeBond.Add(reward)
	legacyBP.Adjust(legacyNodeBond)
	legacyOperator := legacyBP.Get(operator)
	legacyNodeOperatorFees := common.GetSafeShare(initial.NodeOperatorFee, cosmos.NewUint(constants.MaxBasisPts), reward)
	operatorBondIncrease := common.SafeSub(legacyOperator.Bond, lastOperatorBond)
	if legacyNodeOperatorFees.GT(operatorBondIncrease) {
		legacyNodeOperatorFees = operatorBondIncrease
	}
	legacyNodeBond = common.SafeSub(legacyNodeBond, legacyNodeOperatorFees)
	legacyBP.Unbond(legacyNodeOperatorFees, operator)

	newBP := cloneBondProviders(initial)
	newBP.Adjust(nodeBond)
	newNodeOperatorFees := common.GetSafeShare(newBP.NodeOperatorFee, cosmos.NewUint(constants.MaxBasisPts), reward)
	if newNodeOperatorFees.GT(reward) {
		newNodeOperatorFees = reward
	}
	providerReward := common.SafeSub(reward, newNodeOperatorFees)
	rebonded, allocations, err := applyBondReward(&newBP, operator, providerReward, false)
	c.Assert(err, IsNil)
	newNodeBond := nodeBond.Add(rebonded)

	c.Check(newNodeOperatorFees.String(), Equals, legacyNodeOperatorFees.String())
	c.Check(newNodeBond.String(), Equals, legacyNodeBond.String())
	c.Check(allocations, HasLen, 0)
	c.Check(newBP.Get(operator).Bond.String(), Equals, legacyBP.Get(operator).Bond.String())
	c.Check(newBP.Get(provider1).Bond.String(), Equals, legacyBP.Get(provider1).Bond.String())
	c.Check(newBP.Get(provider2).Bond.String(), Equals, legacyBP.Get(provider2).Bond.String())
}

func (s *HelperBondYieldSuite) TestBondYieldPayoutUsesCurrentTHORNamePreferredAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	provider := NewBondProvider(providerAcc)
	provider.PayoutTarget = "yieldname"

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	tn := THORName{
		Name:              "yieldname",
		Owner:             providerAcc,
		ExpireBlockHeight: 10000000,
		PreferredAsset:    common.ETHAsset,
		Aliases: []THORNameAlias{
			{Chain: common.ETHChain, Address: GetRandomETHAddress()},
			{Chain: common.BTCChain, Address: GetRandomBTCAddress()},
		},
	}
	mgr.Keeper().SetTHORName(ctx, tn)

	payoutAcc, resolved, err := resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Assert(err, IsNil)
	c.Check(payoutAcc.Empty(), Equals, true)
	c.Check(resolved.PreferredAsset.Equals(common.ETHAsset), Equals, true)

	tn.PreferredAsset = common.BTCAsset
	mgr.Keeper().SetTHORName(ctx, tn)

	payoutAcc, resolved, err = resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Assert(err, IsNil)
	c.Check(payoutAcc.Empty(), Equals, true)
	c.Check(resolved.PreferredAsset.Equals(common.BTCAsset), Equals, true)

	stored, err := mgr.Keeper().GetTHORName(ctx, "yieldname")
	c.Assert(err, IsNil)
	c.Check(stored.PreferredAsset.Equals(common.BTCAsset), Equals, true)
}

func (s *HelperBondYieldSuite) TestBondYieldAutoClaimTHORNameMustBeOwnedByProvider(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAcc), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	tn := THORName{
		Name:              "yieldname",
		Owner:             GetRandomBech32Addr(),
		ExpireBlockHeight: 10000000,
		PreferredAsset:    common.ETHAsset,
		Aliases: []THORNameAlias{
			{Chain: common.ETHChain, Address: GetRandomETHAddress()},
		},
	}
	mgr.Keeper().SetTHORName(ctx, tn)

	tx := common.Tx{
		ID:          GetRandomTxHash(),
		FromAddress: common.Address(providerAcc.String()),
	}
	msg := NewMsgBondYield(tx, nodeAcc.NodeAddress, constants.MaxBasisPts, "yieldname", providerAcc)
	handler := NewBondYieldHandler(mgr)

	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "(?s).*payout THORName owner must match bond provider.*")
}

func (s *HelperBondYieldSuite) TestBondYieldConfigRequiresPositiveNodeStatus(c *C) {
	for _, tc := range []struct {
		status  ttypes.NodeStatus
		wantErr bool
	}{
		{NodeActive, false},
		{NodeStandby, false},
		{NodeReady, false},
		{NodeDisabled, true},
		{NodeUnknown, true},
	} {
		ctx, mgr := setupManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

		nodeAcc := GetRandomValidatorNode(tc.status)
		nodeAcc.Bond = cosmos.NewUint(100 * common.One)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAcc), IsNil)

		providerAcc, err := nodeAcc.BondAddress.AccAddress()
		c.Assert(err, IsNil)
		tx := common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: common.Address(providerAcc.String()),
		}
		msg := NewMsgBondYield(tx, nodeAcc.NodeAddress, 1000, "", providerAcc)
		handler := NewBondYieldHandler(mgr)

		err = handler.validate(ctx, *msg)
		if tc.wantErr {
			c.Assert(err, NotNil)
			c.Check(err, ErrorMatches, "(?s).*node account status is not eligible for bond yield.*")
			continue
		}
		c.Check(err, IsNil)
	}
}

func (s *HelperBondYieldSuite) TestBondYieldClearSkipsPayoutTargetValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAcc), IsNil)

	bp := NewBondProviders(nodeAcc.NodeAddress)
	bp.Providers = []BondProvider{NewBondProvider(providerAcc)}
	bp.Bond(nodeAcc.Bond, providerAcc)
	c.Assert(bp.SetAutoClaim(providerAcc, 2500, "oldyield"), Equals, true)
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	tx := common.Tx{
		ID:          GetRandomTxHash(),
		FromAddress: common.Address(providerAcc.String()),
	}
	msg := NewMsgBondYield(tx, nodeAcc.NodeAddress, 0, "missingyield", providerAcc)
	handler := NewBondYieldHandler(mgr)

	_, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)

	stored, err := mgr.Keeper().GetBondProviders(ctx, nodeAcc.NodeAddress)
	c.Assert(err, IsNil)
	provider := stored.Get(providerAcc)
	c.Check(provider.Bps, Equals, uint64(0))
	c.Check(provider.PayoutTarget, Equals, "")
}

func (s *HelperBondYieldSuite) TestBondYieldConfigRejectsNonProviderBeforePayoutTargetValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	operatorAcc := GetRandomBech32Addr()
	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(operatorAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAcc), IsNil)

	bp := NewBondProviders(nodeAcc.NodeAddress)
	bp.Providers = []BondProvider{NewBondProvider(operatorAcc)}
	bp.Bond(nodeAcc.Bond, operatorAcc)
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	tx := common.Tx{
		ID:          GetRandomTxHash(),
		FromAddress: common.Address(providerAcc.String()),
	}
	msg := NewMsgBondYield(tx, nodeAcc.NodeAddress, 2500, "missingyield", providerAcc)
	handler := NewBondYieldHandler(mgr)

	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, ".*bond provider address is not valid for node account.*")
}

func (s *HelperBondYieldSuite) TestBondYieldConfigDoesNotAdjustProviderBonds(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	operatorAcc := GetRandomBech32Addr()
	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(operatorAcc.String())
	nodeAcc.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAcc), IsNil)

	operatorBond := cosmos.NewUint(100 * common.One)
	providerBond := cosmos.NewUint(200 * common.One)
	bp := NewBondProviders(nodeAcc.NodeAddress)
	bp.Providers = []BondProvider{
		NewBondProvider(operatorAcc),
		NewBondProvider(providerAcc),
	}
	bp.Bond(operatorBond, operatorAcc)
	bp.Bond(providerBond, providerAcc)
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	tx := common.Tx{
		ID:          GetRandomTxHash(),
		FromAddress: common.Address(providerAcc.String()),
	}
	msg := NewMsgBondYield(tx, nodeAcc.NodeAddress, 2500, "", providerAcc)
	handler := NewBondYieldHandler(mgr)

	_, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)

	stored, err := mgr.Keeper().GetBondProviders(ctx, nodeAcc.NodeAddress)
	c.Assert(err, IsNil)
	operator := stored.Get(operatorAcc)
	provider := stored.Get(providerAcc)
	c.Check(operator.Bond.Equal(operatorBond), Equals, true)
	c.Check(provider.Bond.Equal(providerBond), Equals, true)
	c.Check(provider.Bps, Equals, uint64(2500))
	c.Check(provider.PayoutTarget, Equals, "")
}

func (s *HelperBondYieldSuite) TestBondYieldPayoutRejectsTHORNameOwnerChange(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)

	provider := NewBondProvider(providerAcc)
	c.Assert(provider.BondAddress.Equals(providerAcc), Equals, true)
	provider.PayoutTarget = "yieldname"

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	tn := THORName{
		Name:              "yieldname",
		Owner:             GetRandomBech32Addr(),
		ExpireBlockHeight: 10000000,
		PreferredAsset:    common.ETHAsset,
		Aliases: []THORNameAlias{
			{Chain: common.ETHChain, Address: GetRandomETHAddress()},
		},
	}
	mgr.Keeper().SetTHORName(ctx, tn)

	FundModule(c, ctx, mgr.Keeper(), BondName, 20*common.One)

	err := payBondYield(ctx, mgr, common.Tx{}, &nodeAcc, provider, cosmos.NewUint(10*common.One))
	c.Assert(err, NotNil)
	c.Check(err, ErrorMatches, "(?s).*payout THORName owner must match bond provider.*")
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, tn.Owner, common.RuneNative).Amount.IsZero(), Equals, true)
}

func (s *HelperBondYieldSuite) TestBondYieldDirectPayoutTarget(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	provider := NewBondProvider(providerAcc)

	payoutAcc := GetRandomBech32Addr()
	provider.PayoutTarget = payoutAcc.String()
	acc, tn, err := resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Assert(err, IsNil)
	c.Check(acc.Equals(payoutAcc), Equals, true)
	c.Check(tn.Name, Equals, "")

	provider.PayoutTarget = GetRandomETHAddress().String()
	_, _, err = resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Check(err, ErrorMatches, "payout target address must be a THORChain address")

	provider.PayoutTarget = "yieldname"
	mgr.Keeper().SetTHORName(ctx, THORName{
		Name:              "yieldname",
		Owner:             providerAcc,
		ExpireBlockHeight: 10000000,
		PreferredAsset:    common.ETHAsset,
	})
	acc, tn, err = resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Assert(err, IsNil)
	c.Check(acc.Empty(), Equals, true)
	c.Check(tn.Name, Equals, "yieldname")
}

func (s *HelperBondYieldSuite) TestBondYieldRejectsModulePayoutTargets(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	provider := NewBondProvider(providerAcc)

	provider.PayoutTarget = mgr.Keeper().GetModuleAccAddress(AsgardName).String()
	_, _, err := resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Check(err, ErrorMatches, "payout target address cannot be a network module")

	provider.PayoutTarget = "yieldname"
	mgr.Keeper().SetTHORName(ctx, THORName{
		Name:              "yieldname",
		Owner:             providerAcc,
		ExpireBlockHeight: 10000000,
		Aliases: []THORNameAlias{
			{Chain: common.THORChain, Address: common.Address(mgr.Keeper().GetModuleAccAddress(ReserveName).String())},
		},
	})

	_, _, err = resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Check(err, ErrorMatches, "payout target address cannot be a network module")
}

func (s *HelperBondYieldSuite) TestBondYieldDefaultPayoutReceivesRune(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)
	provider := NewBondProvider(providerAcc)

	FundModule(c, ctx, mgr.Keeper(), BondName, 110*common.One)

	err := payBondYield(ctx, mgr, common.Tx{}, &nodeAcc, provider, cosmos.NewUint(10*common.One))
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.Equal(cosmos.NewInt(10*common.One)), Equals, true)
}

func (s *HelperBondYieldSuite) TestBondYieldDirectPayoutTargetReceivesRune(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	payoutAcc := GetRandomBech32Addr()
	payoutTarget := payoutAcc.String()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)
	provider := NewBondProvider(providerAcc)
	provider.PayoutTarget = payoutTarget

	FundModule(c, ctx, mgr.Keeper(), BondName, 110*common.One)

	err := payBondYield(ctx, mgr, common.Tx{}, &nodeAcc, provider, cosmos.NewUint(10*common.One))
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, payoutAcc, common.RuneNative).Amount.Equal(cosmos.NewInt(10*common.One)), Equals, true)
}

func (s *HelperBondYieldSuite) TestBondYieldTHORNameWithoutPreferredAssetUsesTHORAlias(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	payoutAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)

	provider := NewBondProvider(providerAcc)
	provider.PayoutTarget = "yieldname"

	tn := THORName{
		Name:              "yieldname",
		Owner:             providerAcc,
		ExpireBlockHeight: 10000000,
		Aliases: []THORNameAlias{
			{Chain: common.THORChain, Address: common.Address(payoutAcc.String())},
		},
	}
	mgr.Keeper().SetTHORName(ctx, tn)

	payoutAccResolved, resolved, err := resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Assert(err, IsNil)
	c.Check(payoutAccResolved.Equals(payoutAcc), Equals, true)
	c.Check(resolved.Name, Equals, "")

	FundModule(c, ctx, mgr.Keeper(), BondName, 110*common.One)

	err = payBondYield(ctx, mgr, common.Tx{}, &nodeAcc, provider, cosmos.NewUint(10*common.One))
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, payoutAcc, common.RuneNative).Amount.Equal(cosmos.NewInt(10*common.One)), Equals, true)
}

func (s *HelperBondYieldSuite) TestBondYieldTHORNameWithoutPreferredAssetFallsBackToOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)

	provider := NewBondProvider(providerAcc)
	provider.PayoutTarget = "yieldname"

	tn := THORName{
		Name:              "yieldname",
		Owner:             providerAcc,
		ExpireBlockHeight: 10000000,
		Aliases: []THORNameAlias{
			{Chain: common.ETHChain, Address: GetRandomETHAddress()},
		},
	}
	mgr.Keeper().SetTHORName(ctx, tn)

	payoutAcc, resolved, err := resolveBondYieldPayoutTarget(ctx, mgr, provider)
	c.Assert(err, IsNil)
	c.Check(payoutAcc.Equals(providerAcc), Equals, true)
	c.Check(resolved.Name, Equals, "")

	FundModule(c, ctx, mgr.Keeper(), BondName, 110*common.One)

	err = payBondYield(ctx, mgr, common.Tx{}, &nodeAcc, provider, cosmos.NewUint(10*common.One))
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.Equal(cosmos.NewInt(10*common.One)), Equals, true)
}

func (s *HelperBondYieldSuite) TestBondYieldPreferredAssetPayoutUsesAffiliateCollector(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	providerAcc := GetRandomBech32Addr()
	nodeAcc := GetRandomValidatorNode(NodeActive)
	nodeAcc.BondAddress = common.Address(providerAcc.String())
	nodeAcc.Bond = cosmos.NewUint(100 * common.One)

	provider := NewBondProvider(providerAcc)
	provider.PayoutTarget = "yieldname"

	tn := NewTHORName("yieldname", 1000, []THORNameAlias{
		{Chain: common.THORChain, Address: GetRandomTHORAddress()},
		{Chain: common.BTCChain, Address: GetRandomBTCAddress()},
	})
	tn.Owner = providerAcc
	tn.PreferredAsset = common.BTCAsset
	tn.PreferredAssetOutboundFeeMultiplier = 1_000_000
	k.SetTHORName(ctx, tn)

	amount := cosmos.NewUint(500)
	FundModule(c, ctx, k, BondName, common.One)

	err := payBondYield(ctx, mgr, common.Tx{}, &nodeAcc, provider, amount)
	c.Assert(err, IsNil)

	collector, err := k.GetAffiliateCollector(ctx, tn.Name)
	c.Assert(err, IsNil)
	c.Check(collector.RuneAmount.Equal(amount), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(amount), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, BondName).Equal(cosmos.NewUint(common.One-amount.Uint64())), Equals, true)
	c.Check(k.GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)
	c.Check(countQueuedSwaps(k, ctx), Equals, 0)
}

func cloneBondProviders(bp BondProviders) BondProviders {
	clone := bp
	clone.Providers = append([]BondProvider(nil), bp.Providers...)
	return clone
}
