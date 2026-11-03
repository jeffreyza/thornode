package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// isBondYieldEnabled reports whether bond yield automatic payouts are enabled.
func isBondYieldEnabled(ctx cosmos.Context, mgr Manager) bool {
	return mgr.Keeper().GetConfigInt64(ctx, constants.BondYieldEnabled) > 0
}

type bondYieldRewardAllocation struct {
	ProviderAddress cosmos.AccAddress
	RewardShare     cosmos.Uint
	PayoutAmount    cosmos.Uint
	RebondedAmount  cosmos.Uint
	Bps             uint64
}

// applyBondReward splits a node's provider reward across bond providers,
// rebonding each provider's retained share and returning payout allocations
// for any reward-time Bond Yield distributions.
func applyBondReward(bp *BondProviders, operator cosmos.AccAddress, reward cosmos.Uint, bondYieldEnabled bool) (cosmos.Uint, []bondYieldRewardAllocation, error) {
	rebonded := cosmos.ZeroUint()
	if reward.IsZero() {
		return rebonded, nil, nil
	}
	if err := assertBondProviderOperatorInvariant(*bp, operator); err != nil {
		return rebonded, nil, err
	}

	// Capture provider weights before mutating any bond balances below.
	totalBond := cosmos.ZeroUint()
	for _, provider := range bp.Providers {
		totalBond = totalBond.Add(provider.Bond)
	}
	if totalBond.IsZero() {
		// With no provider weight to split by, keep legacy behavior by
		// rebonding the full reward to the node operator provider.
		bp.Providers[0].Bond = bp.Providers[0].Bond.Add(reward)
		return reward, nil, nil
	}

	var allocations []bondYieldRewardAllocation
	// applyProviderShare mutates the provider bond immediately for the rebonded
	// amount, while recording payout metadata for the caller to settle later.
	applyProviderShare := func(i int, share cosmos.Uint) {
		if share.IsZero() {
			return
		}

		provider := &bp.Providers[i]
		payout := cosmos.ZeroUint()
		bps := uint64(0)
		// When Bond Yield is enabled, the provider's configured basis points
		// are paid out now and only the remainder is rebonded.
		if bondYieldEnabled {
			bps = provider.Bps
			if bps > constants.MaxBasisPts {
				bps = constants.MaxBasisPts
			}
			payout = common.GetSafeShare(cosmos.NewUint(bps), cosmos.NewUint(constants.MaxBasisPts), share)
		}

		providerRebonded := common.SafeSub(share, payout)
		if !providerRebonded.IsZero() {
			provider.Bond = provider.Bond.Add(providerRebonded)
		}
		rebonded = rebonded.Add(providerRebonded)
		if bondYieldEnabled {
			// Keep the original share and split so failed payouts can be
			// rebonded by the caller without recomputing provider weights.
			allocations = append(allocations, bondYieldRewardAllocation{
				ProviderAddress: provider.BondAddress,
				RewardShare:     share,
				PayoutAmount:    payout,
				RebondedAmount:  providerRebonded,
				Bps:             bps,
			})
		}
	}

	allocated := cosmos.ZeroUint()
	// Non-operator providers are allocated first; the operator receives the
	// remainder. Cap each rounded share by the unallocated reward so multiple
	// round-ups cannot collectively allocate more than the reward.
	for i := 1; i < len(bp.Providers); i++ {
		share := common.GetSafeShare(bp.Providers[i].Bond, totalBond, reward)
		remaining := common.SafeSub(reward, allocated)
		if share.GT(remaining) {
			share = remaining
		}
		allocated = allocated.Add(share)
		applyProviderShare(i, share)
	}
	applyProviderShare(0, common.SafeSub(reward, allocated))
	return rebonded, allocations, nil
}

func assertBondProviderOperatorInvariant(bp BondProviders, operator cosmos.AccAddress) error {
	if len(bp.Providers) == 0 {
		return fmt.Errorf("bond providers cannot be empty")
	}
	if !bp.Providers[0].BondAddress.Equals(operator) {
		return fmt.Errorf("bond provider operator invariant violation: providers[0](%s) must be node operator(%s)", bp.Providers[0].BondAddress, operator)
	}
	return nil
}

// normalizeBondProviderOperator repairs legacy provider ordering by moving the
// node operator's complete provider record to index zero. BondProviders.Adjust
// assigns operator semantics to that index, so preserving the full record
// (including bond-yield settings) is important when reordering it.
func normalizeBondProviderOperator(bp *BondProviders, operator cosmos.AccAddress) error {
	if len(bp.Providers) == 0 {
		return fmt.Errorf("bond providers cannot be empty")
	}
	for i := range bp.Providers {
		if !bp.Providers[i].BondAddress.Equals(operator) {
			continue
		}
		if i != 0 {
			bp.Providers[0], bp.Providers[i] = bp.Providers[i], bp.Providers[0]
		}
		return nil
	}
	return fmt.Errorf("node operator(%s) not listed in bond providers", operator)
}

// resolveBondYieldPayoutTarget returns a THORChain account address when
// the payout target is a raw address or a THORName with no preferred asset,
// and returns a THORName when the payout target is a THORName with a preferred asset
//
//	(which requires payout via the affiliate collector).
func resolveBondYieldPayoutTarget(ctx cosmos.Context, mgr Manager, provider BondProvider) (cosmos.AccAddress, THORName, error) {
	payoutTarget := provider.PayoutTarget
	if payoutTarget == "" {
		// No configured payout target means the provider receives native RUNE directly.
		if err := validateBondYieldPayoutAccount(mgr, provider.BondAddress); err != nil {
			return nil, THORName{}, err
		}
		return provider.BondAddress, THORName{}, nil
	}
	addr, err := common.NewAddress(payoutTarget)
	if err == nil {
		// Address must be a valid THORChain address.
		if !addr.GetChain().IsTHORChain() {
			return nil, THORName{}, fmt.Errorf("payout target address must be a THORChain address")
		}
		var acc cosmos.AccAddress
		acc, err = addr.AccAddress()
		if err != nil {
			return nil, THORName{}, fmt.Errorf("failed to parse payout THORChain address: %w", err)
		}
		if err = validateBondYieldPayoutAccount(mgr, acc); err != nil {
			return nil, THORName{}, err
		}
		return acc, THORName{}, nil
	}
	var tn THORName
	tn, err = mgr.Keeper().GetTHORName(ctx, payoutTarget)
	if err != nil {
		return nil, THORName{}, fmt.Errorf("failed to get THORName: %w", err)
	}
	// THORName payout targets must be owned by the bond provider at registration time.
	if !provider.BondAddress.Equals(tn.Owner) {
		return nil, THORName{}, fmt.Errorf("payout THORName owner must match bond provider")
	}
	if tn.PreferredAsset.IsEmpty() {
		// If preferred asset is empty, payout goes to thor alias or to the owner = bond provider.
		alias := tn.GetAlias(common.THORChain)
		if alias.IsEmpty() {
			if err = validateBondYieldPayoutAccount(mgr, tn.Owner); err != nil {
				return nil, THORName{}, err
			}
			return tn.Owner, THORName{}, nil
		}
		var acc cosmos.AccAddress
		acc, err = alias.AccAddress()
		if err != nil {
			return nil, THORName{}, fmt.Errorf("failed to parse payout THORName THORChain alias: %w", err)
		}
		if err = validateBondYieldPayoutAccount(mgr, acc); err != nil {
			return nil, THORName{}, err
		}
		return acc, THORName{}, nil
	}

	return nil, tn, nil
}

func validateBondYieldPayoutAccount(mgr Manager, acc cosmos.AccAddress) error {
	if IsModuleAccAddress(mgr.Keeper(), acc) {
		return fmt.Errorf("payout target address cannot be a network module")
	}
	return nil
}

// payBondYield sends a provider's reward-time yield payout in RUNE directly to
// the provider, configured THORChain address, or payout THORName route.
func payBondYield(ctx cosmos.Context, mgr Manager, tx common.Tx, nodeAcc *NodeAccount, provider BondProvider, amount cosmos.Uint) error {
	if provider.IsEmpty() {
		return fmt.Errorf("bond provider not found")
	}
	if amount.IsZero() {
		return nil
	}

	coin := common.NewCoin(common.RuneNative, amount)
	payoutAcc, tn, err := resolveBondYieldPayoutTarget(ctx, mgr, provider)
	if err != nil {
		return fmt.Errorf("failed to resolve bond yield payout target: %w", err)
	}
	if !payoutAcc.Empty() {
		// Direct THORChain payout targets receive native RUNE.
		if err = mgr.Keeper().SendFromModuleToAccount(ctx, BondName, payoutAcc, common.NewCoins(coin)); err != nil {
			return fmt.Errorf("fail to send bond yield payout: %w", err)
		}
	} else {
		swapIndex := 0
		if err = addRuneToAffiliateCollectorFromModule(ctx, mgr, BondName, coin, &tn, &swapIndex); err != nil {
			return fmt.Errorf("fail to move bond yield to affiliate collector: %w", err)
		}
	}

	bondReturnedEvent := NewEventBond(amount, BondReturned, tx, nodeAcc, provider.BondAddress)
	if err := mgr.EventMgr().EmitEvent(ctx, bondReturnedEvent); err != nil {
		ctx.Logger().Error("fail to emit bond event", "error", err)
	}

	return nil
}
