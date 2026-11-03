package thorchain

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// nodeAccountForcedOrFlagged identifies node states that must never be selected
// for keygen/keysign membership. RequestedToLeave remains eligible until churn.
func nodeAccountForcedOrFlagged(nodeAccount NodeAccount) bool {
	return nodeAccount.ForcedToLeave
}

// tssSignerFilteringDisabled returns true when the disable Mimir is active at
// the requested policy height.
func tssSignerFilteringDisabled(ctx cosmos.Context, k keeper.Keeper) (bool, error) {
	disableAt, err := k.GetMimir(ctx, constants.MimirKeyDisableTSSSignerFiltering)
	if err != nil {
		return false, err
	}
	return disableAt > 0 && disableAt <= ctx.BlockHeight(), nil
}

// keygenEligibleNodeAccounts removes nodes that must not receive new key shares
// before selecting participants for a keygen block, unless signer filtering is
// disabled by Mimir. Jail is intentionally ignored here; it only affects
// keysign party selection for already-created vaults.
func keygenEligibleNodeAccounts(ctx cosmos.Context, k keeper.Keeper, nas NodeAccounts) (NodeAccounts, error) {
	filteringDisabled, err := tssSignerFilteringDisabled(ctx, k)
	if err != nil {
		return nil, fmt.Errorf("fail to get TSS signer filtering mimir: %w", err)
	}
	if filteringDisabled {
		return nas, nil
	}

	eligible := make(NodeAccounts, 0, len(nas))
	for _, nodeAccount := range nas {
		if nodeAccountForcedOrFlagged(nodeAccount) {
			continue
		}
		eligible = append(eligible, nodeAccount)
	}
	return eligible, nil
}

// validateKeygenEligibleThreshold prevents forced-leave filtering from
// permanently shrinking a new vault below the original keygen safety threshold.
func validateKeygenEligibleThreshold(original, eligible NodeAccounts) error {
	if len(eligible) == 0 {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "keygen", "eligibility_abort"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "no_eligible")},
		)
		return fmt.Errorf("no eligible keygen members")
	}
	threshold, err := conversion.GetThreshold(len(original))
	if err != nil {
		return fmt.Errorf("fail to calculate keygen threshold: %w", err)
	}
	required := threshold + 1
	if len(eligible) < required {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "keygen", "eligibility_abort"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "below_threshold")},
		)
		return fmt.Errorf("not enough eligible keygen members after filtering: need at least %d of %d original members, got %d", required, len(original), len(eligible))
	}
	return nil
}
