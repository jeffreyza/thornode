package thorchain

import (
	"errors"
	"fmt"
	"net"
	"sort"

	abci "github.com/cometbft/cometbft/abci/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ValidatorMgr is to manage a list of validators , and rotate them
type ValidatorMgr struct {
	k                  keeper.Keeper
	networkMgr         NetworkManager
	txOutStore         TxOutStore
	eventMgr           EventManager
	existingValidators []string
}

// newValidatorMgr create a new instance of ValidatorMgr
func newValidatorMgr(k keeper.Keeper, networkMgr NetworkManager, txOutStore TxOutStore, eventMgr EventManager) *ValidatorMgr {
	return &ValidatorMgr{
		k:          k,
		networkMgr: networkMgr,
		txOutStore: txOutStore,
		eventMgr:   eventMgr,
	}
}

// BeginBlock when block begin
func (vm *ValidatorMgr) BeginBlock(ctx cosmos.Context, mgr Manager, existingValidators []string) error {
	vm.existingValidators = existingValidators
	height := ctx.BlockHeight()
	if height == genesisBlockHeight {
		if err := vm.setupValidatorNodes(ctx, height); err != nil {
			return fmt.Errorf("fail to setup validator nodes: %w", err)
		}
	}
	if vm.k.RagnarokInProgress(ctx) {
		// ragnarok is in progress, no point to check node rotation
		return nil
	}

	lastChurnHeight := getLastChurnHeight(ctx, vm.k)
	churnInterval := vm.k.GetConfigInt64(ctx, constants.ChurnInterval)
	churnRetryInterval := vm.k.GetConfigInt64(ctx, constants.ChurnRetryInterval)
	// Only compute retry tick if the interval is valid and past the scheduled churn height
	onChurnTick := false
	if churnRetryInterval > 0 && ctx.BlockHeight() > lastChurnHeight+churnInterval {
		onChurnTick = (ctx.BlockHeight()-lastChurnHeight-churnInterval)%churnRetryInterval == 0
	}
	// Allow regular churns to proceed regardless of churnRetryInterval
	isRegularChurn := lastChurnHeight+churnInterval == ctx.BlockHeight()
	if !onChurnTick && !isRegularChurn {
		return nil
	}

	halt, err := vm.k.GetMimir(ctx, "HaltChurning")
	if halt > 0 && halt <= ctx.BlockHeight() && err == nil {
		ctx.Logger().Info("churn event skipped due to mimir has halted churning")
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "churn", "skipped"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", "halt_churning")},
		)
		return nil
	}

	vaults, err := vm.k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("Failed to get Asgard vaults", "error", err)
		return err
	}

	// calculate if we need to retry a churn because we are overdue for a
	// successful one
	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return err
	}
	asgardSize := vm.k.GetConfigInt64(ctx, constants.AsgardSize)
	if asgardSize <= 0 {
		return nil
	}
	expectedActiveVaults := splitAsgardGroupCount(len(nas), asgardSize)
	// Do not treat the dedicated XMR monovault as a missing active vault until
	// it actually exists in the active set. Counting a future monovault here
	// causes regular churn retries to overlap a still-settling Asgard keygen,
	// which can keep the delayed successful round from ever becoming the lone
	// init-vault set that RotateVault expects.
	if hasDedicatedXMRMonovault(vaults) {
		expectedActiveVaults++
	}
	incompleteChurnCheck := len(vaults) != expectedActiveVaults
	oldVaultCheck := ctx.BlockHeight()-lastChurnHeight > churnInterval
	retryChurn := (oldVaultCheck || incompleteChurnCheck) && onChurnTick

	// skip churn if any active chain is halted
	shouldChurn := lastChurnHeight+churnInterval == ctx.BlockHeight() || retryChurn
	if !shouldChurn {
		return nil
	}

	// collect all chains for active vaults
	activeChains := make(common.Chains, 0)
	for _, v := range vaults {
		activeChains = append(activeChains, v.GetChains()...)
	}
	activeChains = activeChains.Distinct()

	for _, chain := range activeChains {
		if mgr.Keeper().IsChainHalted(ctx, chain) {
			ctx.Logger().Info("Skipping node account rotation for halted chain", "chain", chain)
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "skipped"},
				float32(1),
				[]metrics.Label{
					telemetry.NewLabel("reason", "chain_halted"),
					telemetry.NewLabel("chain", chain.String()),
				},
			)
			return nil
		}
	}

	// Do not churn while a RetiringVault still has real funds to migrate.
	// Deficit-only retiring vaults are accounting records, not migration work,
	// so they must not block validator rotation.
	retiringVaults, err := vm.k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return err
	}
	for _, vault := range retiringVaults {
		// Sub-dust remainders (e.g. the XMR final-round migration buffer) cannot
		// be migrated and are only burned once nth exceeds ChurnMigrateRounds,
		// which can be long after the last real migration; they must not block
		// rotation until then.
		if vault.HasFundsAboveDust() {
			ctx.Logger().Info("Skipping rotation due to retiring vaults still have funds.")
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "skipped"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "retiring_vault_has_funds")},
			)
			return nil
		}
	}

	if retryChurn {
		ctx.Logger().Info("Checking for node account rotation... (retry)")
		if oldVaultCheck {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "retry"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "old_vault")},
			)
		}
		if incompleteChurnCheck {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "retry"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "incomplete_vaults")},
			)
		}
	} else {
		ctx.Logger().Info("Checking for node account rotation...")
	}

	// churn is called exactly once here; the returned error is propagated
	// unchanged on every path. The counters are leaf, process-local side effects.
	if churnErr := vm.churn(ctx); churnErr != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "churn", "attempt"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("status", "failed")},
		)
		return churnErr
	}
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "churn", "attempt"},
		float32(1),
		[]metrics.Label{telemetry.NewLabel("status", "ok")},
	)
	return nil
}

func (vm *ValidatorMgr) hasPendingKeygen(ctx cosmos.Context, keygenType thortypes.KeygenType) (bool, error) {
	lookback := keygenResultValidityBlocks(ctx, vm.k, keygenType)
	// A regular Asgard keygen shares the longer XMR validity window when both
	// types are co-scheduled in one block. Scan that full possible window, then
	// use the actual block contents below to select the precise expiry.
	if keygenType == AsgardKeygen {
		lookback = max(lookback, keygenResultValidityBlocks(ctx, vm.k, XMRMonovaultKeygen))
	}
	startHeight := ctx.BlockHeight() - lookback
	if startHeight < genesisBlockHeight {
		startHeight = genesisBlockHeight
	}

	activeVaults, err := vm.k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return false, fmt.Errorf("fail to get active vaults: %w", err)
	}
	initVaults, err := vm.k.GetAsgardVaultsByStatus(ctx, InitVault)
	if err != nil {
		return false, fmt.Errorf("fail to get init vaults: %w", err)
	}
	readyVaults := append(append(Vaults(nil), activeVaults...), initVaults...)

	// Scanning newest first, a resolved keygen of this type supersedes every
	// older one. An older keygen's vault may have retired since, so membership
	// can no longer resolve it, yet no result for it can matter any more.
	// Keygens in the same block (split Asgard groups) are each still checked.
	for height := ctx.BlockHeight(); height >= startHeight; height-- {
		keygenBlock, err := vm.k.GetKeygenBlock(ctx, height)
		if err != nil {
			return false, fmt.Errorf("fail to get keygen block: %w", err)
		}
		superseded := false
		for _, keygen := range keygenBlock.Keygens {
			if keygen.Type != keygenType {
				continue
			}
			if keygenResolvedByVaults(keygen, readyVaults) {
				superseded = true
				continue
			}
			// keygenResultValidityBlocks returns a window that ends one block before
			// the next retry tick, so a keygen stops being pending exactly as its
			// retry fires (not one tick later). handler_tss accepts results over the
			// same window, so a result can no longer finalize a vault in the block
			// the retry is scheduled, which stops the retry double-triggering a live
			// keygen.
			if ctx.BlockHeight()-height > keygenResultValidityBlocksForBlock(ctx, vm.k, keygen.Type, keygenBlock) {
				continue
			}
			return true, nil
		}
		if superseded {
			return false, nil
		}
	}
	return false, nil
}

func keygenResolvedByVaults(keygen Keygen, vaults Vaults) bool {
	members := keygen.GetMembers()
	for _, vault := range vaults {
		if !vault.MembershipEquals(members) {
			continue
		}
		switch keygen.Type {
		case XMRMonovaultKeygen:
			if vault.HasChain(common.XMRChain) {
				return true
			}
		case AsgardKeygen:
			if !vault.HasChain(common.XMRChain) {
				return true
			}
		}
	}
	return false
}

func (vm *ValidatorMgr) churn(ctx cosmos.Context) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := vm.churnInner(cacheCtx); err != nil {
		return err
	}
	commit()
	return nil
}

func (vm *ValidatorMgr) churnInner(ctx cosmos.Context) error {
	desiredValidatorSet := vm.k.GetConfigInt64(ctx, constants.DesiredValidatorSet)
	asgardSize := vm.k.GetConfigInt64(ctx, constants.AsgardSize)
	redline := vm.k.GetConfigInt64(ctx, constants.BadValidatorRedline)
	minSlashPointsForBadValidator := vm.k.GetConfigInt64(ctx, constants.MinSlashPointsForBadValidator)

	// update list of ready actors
	if err := vm.markReadyActors(ctx); err != nil {
		return err
	}

	// clear leave scores
	if err := vm.clearLeaveScores(ctx); err != nil {
		return err
	}

	// Mark bad, old, low bond, and old version validators
	// mark someone to get churned out for bad behavior
	_, err := vm.markBadActor(ctx, minSlashPointsForBadValidator, redline)
	if err != nil {
		return err
	}

	// mark someone to get churned out for low bond only once the active set is at capacity
	if err = vm.markLowBondActor(ctx, desiredValidatorSet); err != nil {
		return err
	}

	// mark someone to get churned out for low version
	if err = vm.markLowVersionValidators(ctx); err != nil {
		return err
	}

	// mark someone to get churned out for age
	if err = vm.markOldActor(ctx); err != nil {
		return err
	}

	// mark someone(s) for not signing blocks
	if err = vm.markMissingActors(ctx); err != nil {
		return err
	}

	// mark someone(s) for reporting a keygen chain set that would stall the rotation
	if err = vm.markChainSetActors(ctx); err != nil {
		return err
	}

	// mark someone(s) for missing observations on a chain
	if err = vm.markMissingObservationActors(ctx); err != nil {
		return err
	}

	next, ok, err := vm.nextVaultNodeAccounts(ctx, int(desiredValidatorSet))
	if err != nil {
		return err
	}
	if ok {
		splitGroups := vm.splitNext(ctx, next, asgardSize)
		pendingAsgard, pendingErr := vm.hasPendingKeygen(ctx, AsgardKeygen)
		if pendingErr != nil {
			return pendingErr
		}
		if pendingAsgard {
			ctx.Logger().Info("Skipping regular Asgard keygen due to pending keygen")
		} else {
			for i, nodeAccSet := range splitGroups {
				// Churn scheduling is intentionally all-or-nothing under the churn
				// CacheContext. If any split cannot form a threshold-safe keygen after
				// member filtering, abort the whole churn so earlier split keygens and
				// mark/reset changes roll back together. Scheduling only surviving
				// splits would create a partial replacement vault set for this churn.
				if triggerErr := vm.networkMgr.TriggerKeygen(ctx, nodeAccSet); triggerErr != nil {
					return fmt.Errorf("fail to trigger keygen for split %d/%d: %w", i+1, len(splitGroups), triggerErr)
				}
			}
		}
	}

	// XMR monovault maintenance is independent from ordinary vault rotation.
	// At the BFT floor the validator membership can be stable (ok == false)
	// while the XMR vault is missing or its last keygen has expired. Recover the
	// monovault without scheduling an unnecessary regular Asgard keygen.
	needsXMR, err := vm.xmrVaultMembershipNeedsKeygen(ctx, next)
	if err != nil {
		return err
	}
	if needsXMR {
		pendingXMR, err := vm.hasPendingKeygen(ctx, XMRMonovaultKeygen)
		if err != nil {
			return err
		}
		if pendingXMR {
			ctx.Logger().Info("Skipping XMR FROST keygen due to pending keygen")
		} else if err := vm.networkMgr.TriggerXMRKeygen(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

// splits given list of node accounts into separate list of nas, for separate
// asgard vaults
func (vm *ValidatorMgr) splitNext(ctx cosmos.Context, nas NodeAccounts, asgardSize int64) []NodeAccounts {
	if asgardSize <= 0 { // sanity check
		return nil
	}
	// calculate the number of asgard vaults we'll need to support the given
	// list of node accounts
	groupNum := int64(len(nas)) / asgardSize
	if int64(len(nas))%asgardSize > 0 {
		groupNum++
	}
	if groupNum <= 0 { // sanity check
		return nil
	}

	// we want to ensure that a single node operator (designated by bond
	// address) doesn't get too many tss shares for a single Asgard vault. So we
	// first break out our node accounts into two groups. First, duplicate bond
	// addresses (multi-node operators), and second non-duplicate (single node
	// operators). Then we sort the duplicate group by bond address, then by
	// bond size (large to small). Then we sort the non-duplicate group by bond size (large
	// to small). Then iterate over the first group into asgard vaults first,
	// then the second group. In the end multi-node operators are spread out
	// against as many asgard vaults as possible. This also makes it more
	// difficult for a malicious actor to acquire enough spots in a single
	// asgard to steal as enough are taken by "good actors" that they can't
	// acquire enough tss shares.

	// Check for duplicates
	bondAddrMap := make(map[string]int)
	for _, na := range nas {
		bondAddrMap[na.BondAddress.String()]++
	}
	var duplicateNas, nonDuplicateNas NodeAccounts
	for _, na := range nas {
		if bondAddrMap[na.BondAddress.String()] > 1 {
			duplicateNas = append(duplicateNas, na)
		} else {
			nonDuplicateNas = append(nonDuplicateNas, na)
		}
	}

	sort.SliceStable(duplicateNas, func(i, j int) bool {
		// Check if the bond address counts are the same
		if bondAddrMap[duplicateNas[i].BondAddress.String()] == bondAddrMap[duplicateNas[j].BondAddress.String()] {
			// Check if bond addresses are the same
			if duplicateNas[i].BondAddress.String() == duplicateNas[j].BondAddress.String() {
				// Sort by bond size
				return duplicateNas[i].Bond.GT(duplicateNas[j].Bond)
			}
			// Sort by bond address
			return duplicateNas[i].BondAddress.String() < duplicateNas[j].BondAddress.String()
		}
		// Sort by bond address count
		return bondAddrMap[duplicateNas[i].BondAddress.String()] > bondAddrMap[duplicateNas[j].BondAddress.String()]
	})

	// sort by bond size for non-duplicates
	sort.SliceStable(nonDuplicateNas, func(i, j int) bool {
		return nonDuplicateNas[i].Bond.LT(nonDuplicateNas[j].Bond)
	})

	groups := make([]NodeAccounts, groupNum)
	for i, na := range append(duplicateNas, nonDuplicateNas...) {
		groups[i%len(groups)] = append(groups[i%len(groups)], na)
	}

	// sanity checks
	for i, group := range groups {
		// ensure no group is more than the max
		if int64(len(group)) > asgardSize {
			ctx.Logger().Info("Skipping rotation due to an Asgard group is larger than the max size.")
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "split_aborted"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "group_too_large")},
			)
			return nil
		}
		// ensure no group is less than the min
		if int64(len(group)) < 2 {
			ctx.Logger().Info("Skipping rotation due to an Asgard group is smaller than the min size.")
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "split_aborted"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("reason", "group_too_small")},
			)
			return nil
		}
		// ensure a single group is significantly larger than another
		if i > 0 {
			diff := len(groups[i]) - len(groups[i-1])
			if diff < 0 {
				diff = -diff
			}
			if diff > 1 {
				ctx.Logger().Info("Skipping rotation due to an Asgard groups having dissimilar membership size.")
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "churn", "split_aborted"},
					float32(1),
					[]metrics.Label{telemetry.NewLabel("reason", "dissimilar_group_size")},
				)
				return nil
			}
		}
	}

	return groups
}

// EndBlock when block commit
func (vm *ValidatorMgr) EndBlock(ctx cosmos.Context, mgr Manager) []abci.ValidatorUpdate {
	height := ctx.BlockHeight()
	activeNodes, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get all active nodes", "error", err)
		return nil
	}

	// when ragnarok is in progress, just process ragnarok
	if vm.k.RagnarokInProgress(ctx) {
		// process ragnarok
		if err = vm.processRagnarok(ctx, mgr); err != nil {
			ctx.Logger().Error("fail to process ragnarok protocol", "error", err)
		}
		return nil
	}

	var newNodes, removedNodes NodeAccounts
	newNodes, removedNodes, err = vm.getChangedNodes(ctx, activeNodes)
	if err != nil {
		ctx.Logger().Error("fail to get node changes", "error", err)
		return nil
	}

	artificialRagnarokBlockHeight := vm.k.GetConfigInt64(ctx, constants.ArtificialRagnarokBlockHeight)
	if artificialRagnarokBlockHeight > 0 {
		ctx.Logger().Info("Artificial Ragnarok is planned", "height", artificialRagnarokBlockHeight)
	}
	minimumNodesForBFT := vm.k.GetConstants().GetInt64Value(constants.MinimumNodesForBFT)
	nodesAfterChange := len(activeNodes) + len(newNodes) - len(removedNodes)
	if (len(activeNodes) >= int(minimumNodesForBFT) && nodesAfterChange < int(minimumNodesForBFT)) ||
		(artificialRagnarokBlockHeight > 0 && ctx.BlockHeight() >= artificialRagnarokBlockHeight) {
		// THORNode don't have enough validators for BFT

		// Check we're not migrating funds
		var retiring Vaults
		retiring, err = vm.k.GetAsgardVaultsByStatus(ctx, RetiringVault)
		if err != nil {
			ctx.Logger().Error("fail to get retiring vaults", "error", err)
			return nil
		}

		if len(retiring) == 0 { // wait until all funds are migrated before starting ragnarok
			if err = vm.processRagnarok(ctx, mgr); err != nil {
				ctx.Logger().Error("fail to process ragnarok protocol", "error", err)
			}
			return nil
		}
	}

	// If there's been a churn (the nodes have changed), continue; if there hasn't, end the function.
	if len(newNodes) == 0 && len(removedNodes) == 0 {
		return nil
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "churn", "nodes_changed"},
		float32(len(newNodes)),
		[]metrics.Label{telemetry.NewLabel("status", "churned_in")},
	)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "churn", "nodes_changed"},
		float32(len(removedNodes)),
		[]metrics.Label{telemetry.NewLabel("status", "churned_out")},
	)

	// remove low bond node accounts
	if err = vm.k.RemoveLowBondValidatorAccounts(ctx); err != nil {
		ctx.Logger().Error("fail to remove low bond node accounts", "error", err)
	}

	// payout all active node accounts their rewards
	// This including nodes churning out, and takes place before changing the activity status below.
	//
	// distributeBondReward wraps each per-node payout in a CacheContext, so a
	// partial failure cannot leave any single node in a half-paid state: either
	// that node's bond reward is fully applied or none of it is. The unpaid
	// share remains in network.BondRewardRune and is redistributed in the next
	// churn. We deliberately log-and-continue rather than halt EndBlock here:
	// returning an error would prevent the churn (and validator-set update)
	// from happening this block, which is far more disruptive to chain
	// liveness than a one-off skipped payout for the affected node(s).
	if err = vm.distributeBondReward(ctx, mgr); err != nil {
		ctx.Logger().Error("fail to pay node bond rewards; undistributed reward remains in BondRewardRune for next churn", "error", err)
	}

	validators := make([]abci.ValidatorUpdate, 0, len(newNodes)+len(removedNodes))
	for _, na := range newNodes {
		ctx.EventManager().EmitEvent(
			cosmos.NewEvent("UpdateNodeAccountStatus",
				cosmos.NewAttribute("Address", na.NodeAddress.String()),
				cosmos.NewAttribute("Former:", na.Status.String()),
				cosmos.NewAttribute("Current:", NodeActive.String())),
		)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "node", "status_change"},
			float32(1),
			[]metrics.Label{
				telemetry.NewLabel("from", na.Status.String()),
				telemetry.NewLabel("to", NodeActive.String()),
			},
		)
		na.UpdateStatus(NodeActive, height)
		na.LeaveScore = 0
		na.RequestedToLeave = false
		na.MissingBlocks = 0 // zero missing blocks that weren't signed (if any)

		vm.k.ResetNodeAccountSlashPoints(ctx, na.NodeAddress)
		// restart the observation tally, as MissingBlocks restarts above
		if err = vm.k.ResetObservationTally(ctx, na.NodeAddress); err != nil {
			ctx.Logger().Error("fail to reset observation tally", "node address", na.NodeAddress, "error", err)
		}
		if err = vm.k.SetNodeAccount(ctx, na); err != nil {
			ctx.Logger().Error("fail to save node account", "error", err)
		}
		var pk cryptotypes.PubKey
		pk, err = cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
		if err != nil {
			ctx.Logger().Error("fail to parse consensus public key", "key", na.ValidatorConsPubKey, "error", err)
			continue
		}
		validators = append(validators, abci.Ed25519ValidatorUpdate(pk.Bytes(), 100))
	}
	for _, na := range removedNodes {
		// retrieve the node from key value store again , as the node might get paid bond, thus the node properties has been changed
		var nodeRemove NodeAccount
		nodeRemove, err = vm.k.GetNodeAccount(ctx, na.NodeAddress)
		if err != nil {
			ctx.Logger().Error("fail to get node account from key value store", "node address", na.NodeAddress)
			continue
		}

		status := NodeStandby
		if nodeRemove.ForcedToLeave {
			status = NodeDisabled
		}
		// if removed node requested to leave , unset it , so they can join back again
		if nodeRemove.RequestedToLeave {
			nodeRemove.RequestedToLeave = false
		}
		ctx.EventManager().EmitEvent(
			cosmos.NewEvent("UpdateNodeAccountStatus",
				cosmos.NewAttribute("Address", nodeRemove.NodeAddress.String()),
				cosmos.NewAttribute("Former:", nodeRemove.Status.String()),
				cosmos.NewAttribute("Current:", status.String())),
		)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "node", "status_change"},
			float32(1),
			[]metrics.Label{
				telemetry.NewLabel("from", nodeRemove.Status.String()),
				telemetry.NewLabel("to", status.String()),
			},
		)
		nodeRemove.UpdateStatus(status, height)
		vm.k.ClearObservationTally(ctx, nodeRemove.NodeAddress)
		if err = vm.k.SetNodeAccount(ctx, nodeRemove); err != nil {
			ctx.Logger().Error("fail to save node account", "error", err)
		}

		var pk cryptotypes.PubKey
		pk, err = cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, nodeRemove.ValidatorConsPubKey)
		if err != nil {
			ctx.Logger().Error("fail to parse consensus public key", "key", nodeRemove.ValidatorConsPubKey, "error", err)
			continue
		}
		caddr := sdk.ValAddress(pk.Address()).String()
		found := false
		for _, exist := range vm.existingValidators {
			if exist == caddr {
				validators = append(validators, abci.Ed25519ValidatorUpdate(pk.Bytes(), 0))
				found = true
				break
			}
		}
		if !found {
			ctx.Logger().Info("validator is not present, so can't be removed", "validator address", caddr)
		}

	}
	// reset all nodes in ready status back to standby status
	ready, err := vm.k.ListValidatorsByStatus(ctx, NodeReady)
	if err != nil {
		ctx.Logger().Error("fail to get list of ready node accounts", "error", err)
	}
	for _, na := range ready {
		na.UpdateStatus(NodeStandby, ctx.BlockHeight())
		if err := vm.k.SetNodeAccount(ctx, na); err != nil {
			ctx.Logger().Error("fail to set node account", "error", err)
		}
	}

	// Now that the node statuses have been updated, update the stored MinJoinVersion.
	vm.k.SetMinJoinLast(ctx)

	// On each churn, purge OperationalMimir node votes
	// (without changing the set Mimir values themselves).
	// This is to stop any OperationalMimir key's threshold for change
	// from creeping up inconveniently high over time.
	// If a small number of nodes repeatedly uses this purge to go against the majority preference,
	// the EconomicMimir OperationalVotesMin could be set to a higher threshold.
	vm.k.PurgeOperationalNodeMimirs(ctx)

	return validators
}

// getChangedNodes to identify which node had been removed ,and which one had been added
// newNodes , removed nodes,err
func (vm *ValidatorMgr) getChangedNodes(ctx cosmos.Context, activeNodes NodeAccounts) (NodeAccounts, NodeAccounts, error) {
	var newActive NodeAccounts    // store the list of new active users
	var removedNodes NodeAccounts // nodes that had been removed

	activeVaults, err := vm.k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("fail to get active asgards", "error", err)
		return newActive, removedNodes, fmt.Errorf("fail to get active asgards: %w", err)
	}
	if len(activeVaults) == 0 {
		return newActive, removedNodes, errors.New("no active vault")
	}
	var membership common.PubKeys
	seenMembers := make(map[string]struct{})
	for _, vault := range activeVaults {
		for _, member := range vault.GetMembership() {
			memberKey := member.String()
			if _, exists := seenMembers[memberKey]; exists {
				continue
			}
			seenMembers[memberKey] = struct{}{}
			membership = append(membership, member)
		}
	}

	// find active node accounts that are no longer active
	for _, na := range activeNodes {
		found := false
		for _, vault := range activeVaults {
			if vault.Contains(na.PubKeySet.Secp256k1) {
				found = true
				break
			}
		}
		if na.ForcedToLeave {
			found = false
		}
		if !found && len(membership) > 0 {
			removedNodes = append(removedNodes, na)
		}
	}

	// find ready nodes that change to active
	for _, pk := range membership {
		na, err := vm.k.GetNodeAccountByPubKey(ctx, pk)
		if err != nil {
			ctx.Logger().Error("fail to get node account", "error", err)
			continue
		}
		// Disabled account can't go back , it should not be include in the newActive
		if na.Status != NodeActive && na.Status != NodeDisabled {
			newActive = append(newActive, na)
		}
	}

	return newActive, removedNodes, nil
}

// payNodeAccountBondAward pay
func (vm *ValidatorMgr) payNodeAccountBondAward(ctx cosmos.Context, lastChurnHeight int64, na NodeAccount, totalBondReward, totalEffectiveBond, bondHardCap cosmos.Uint, mgr Manager) error {
	if na.ActiveBlockHeight == 0 || na.Bond.IsZero() {
		return nil
	}

	network, err := vm.k.GetNetwork(ctx)
	if err != nil {
		return fmt.Errorf("fail to get network: %w", err)
	}

	var slashPts int64
	slashPts, err = vm.k.GetNodeAccountSlashPoints(ctx, na.NodeAddress)
	if err != nil {
		return fmt.Errorf("fail to get node slash points: %w", err)
	}

	// Find number of blocks since the last churn (the last bond reward payout)
	totalActiveBlocks := ctx.BlockHeight() - lastChurnHeight

	// find number of blocks they were well behaved (ie active - slash points)
	earnedBlocks := totalActiveBlocks - slashPts
	if earnedBlocks < 0 {
		earnedBlocks = 0
	}

	naEffectiveBond := na.Bond
	if naEffectiveBond.GT(bondHardCap) {
		naEffectiveBond = bondHardCap
	}

	// reward = totalBondReward * (naEffectiveBond / totalEffectiveBond) * (unslashed blocks since last churn / blocks since last churn)
	reward := common.GetUncappedShare(naEffectiveBond, totalEffectiveBond, totalBondReward)
	reward = common.GetUncappedShare(cosmos.NewUint(uint64(earnedBlocks)), cosmos.NewUint(uint64(totalActiveBlocks)), reward)

	// Independently rounded node shares can exceed the original reward pool.
	// Cap against the remaining ledger before fees, payouts, or rebonding so
	// SafeSub below cannot hide an unfunded reward allocation.
	if reward.GT(network.BondRewardRune) {
		reward = network.BondRewardRune
	}

	// Load and normalize bond providers before applying this reward, so any
	// previous rewards or legacy provider state are already accounted for.
	bp, err := vm.k.GetBondProviders(ctx, na.NodeAddress)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get bond providers(%s)", na.NodeAddress))
	}
	nodeOperatorAccAddr, err := na.BondAddress.AccAddress()
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to parse bond address(%s)", na.BondAddress))
	}
	err = passiveBackfill(ctx, mgr, na, &bp)
	if err != nil {
		return err
	}
	if err = normalizeBondProviderOperator(&bp, nodeOperatorAccAddr); err != nil {
		return ErrInternal(err, "invalid bond provider operator invariant")
	}
	// Ensure that previous rewards are already accounted for
	bp.Adjust(na.Bond)

	// Remove the NodeOperatorFee portion from the reward before distributing
	// the remaining reward to bond providers.
	nodeOperatorFees := common.GetSafeShare(bp.NodeOperatorFee, cosmos.NewUint(constants.MaxBasisPts), reward)
	if nodeOperatorFees.GT(reward) {
		nodeOperatorFees = reward
	}
	providerReward := common.SafeSub(reward, nodeOperatorFees)

	// Distribute provider reward to bond providers. With Bond Yield disabled,
	// the full provider reward is rebonded; with it enabled, the configured
	// payout portions are paid out and only the remainder is rebonded.
	bondYieldEnabled := isBondYieldEnabled(ctx, mgr)
	rebondedReward, rewardAllocations, err := applyBondReward(&bp, nodeOperatorAccAddr, providerReward, bondYieldEnabled)
	if err != nil {
		return ErrInternal(err, "fail to apply bond reward")
	}
	// Add only the rebonded reward to the slashable node bond.
	na.Bond = na.Bond.Add(rebondedReward)

	// Minus the number of RUNE THORNode has awarded them.
	network.BondRewardRune = common.SafeSub(network.BondRewardRune, reward)
	// Minus the number of units na has (do not include slash points).
	network.TotalBondUnits = common.SafeSub(
		network.TotalBondUnits,
		cosmos.NewUint(uint64(totalActiveBlocks)),
	)

	// Transfer node operator fees BEFORE persisting accounting updates.
	// If the bank send fails, the in-memory bond/network changes above are
	// discarded with the function error and no accounting is corrupted.
	if !nodeOperatorFees.IsZero() {
		coin := common.NewCoin(common.RuneNative, nodeOperatorFees)
		sdkErr := vm.k.SendFromModuleToAccount(ctx, BondName, nodeOperatorAccAddr, common.NewCoins(coin))
		if sdkErr != nil {
			return fmt.Errorf("fail to send node operator fees: %w", sdkErr)
		}
	}

	if bondYieldEnabled {
		for i := range rewardAllocations {
			allocation := &rewardAllocations[i]
			if allocation.PayoutAmount.IsZero() {
				continue
			}
			provider := bp.Get(allocation.ProviderAddress)
			// A failed provider payout must not strand funds. Rebond the failed
			// payout amount so funds remain slashable and recoverable.
			if err = payBondYield(ctx, mgr, common.Tx{}, &na, provider, allocation.PayoutAmount); err != nil {
				ctx.Logger().Error("fail to pay bond yield",
					"node_address", na.NodeAddress.String(),
					"provider_address", allocation.ProviderAddress.String(),
					"amount", allocation.PayoutAmount.String(),
					"error", err,
				)
				rebonded := allocation.PayoutAmount
				bp.Bond(rebonded, allocation.ProviderAddress)
				na.Bond = na.Bond.Add(rebonded)
				allocation.RebondedAmount = allocation.RebondedAmount.Add(rebonded)
				allocation.PayoutAmount = common.SafeSub(allocation.PayoutAmount, rebonded)
				continue
			}
		}
	}

	// Now commit the accounting updates. SetNetwork/SetNodeAccount/SetBondProviders
	// in the THORChain keeper always return nil, so reaching this point means
	// the payout is fully consistent.
	if err := vm.k.SetNetwork(ctx, network); err != nil {
		return fmt.Errorf("fail to save network data: %w", err)
	}
	// Minus slash points used in this calculation.
	vm.k.SetNodeAccountSlashPoints(ctx, na.NodeAddress, slashPts-totalActiveBlocks)
	if err := vm.k.SetNodeAccount(ctx, na); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}
	if err := mgr.Keeper().SetBondProviders(ctx, bp); err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to save bond providers(%s)", bp.NodeAddress.String()))
	}

	if bondYieldEnabled {
		for _, allocation := range rewardAllocations {
			if allocation.Bps == 0 && allocation.PayoutAmount.IsZero() {
				continue
			}
			provider := bp.Get(allocation.ProviderAddress)
			bondYieldEvent := NewEventBondYield(
				na.NodeAddress,
				allocation.ProviderAddress,
				allocation.RewardShare,
				allocation.PayoutAmount,
				allocation.RebondedAmount,
				allocation.Bps,
				provider.PayoutTarget,
			)
			if err := mgr.EventMgr().EmitEvent(ctx, bondYieldEvent); err != nil {
				ctx.Logger().Error("fail to emit bond yield event", "error", err)
			}
		}
	}

	// Emit BondReward event for the full pre-payout reward.
	bondRewardEvent := NewEventBond(reward, BondReward, common.Tx{}, &na, nil)
	if err := mgr.EventMgr().EmitEvent(ctx, bondRewardEvent); err != nil {
		ctx.Logger().Error("fail to emit bond event", "error", err)
	}
	if !nodeOperatorFees.IsZero() {
		// TODO: Remove this NewAddress call entirely in the next version of this manager.
		_, err := common.NewAddress(na.NodeAddress.String())
		if err != nil {
			return fmt.Errorf("fail to parse node address: %w", err)
		}
		// Emit BondReturned event for the node operator fee payout.
		bondReturnedEvent := NewEventBond(nodeOperatorFees, BondReturned, common.Tx{}, &na, nodeOperatorAccAddr)
		if err := mgr.EventMgr().EmitEvent(ctx, bondReturnedEvent); err != nil {
			ctx.Logger().Error("fail to emit bond event", "error", err)
		}
	}

	return nil
}

// determines when/if to run each part of the ragnarok process
func (vm *ValidatorMgr) processRagnarok(ctx cosmos.Context, mgr Manager) error {
	// execute Ragnarok protocol, no going back
	// THORNode have to request the fund back now, because once it get to the rotate block height ,
	// THORNode won't have validators anymore
	ragnarokHeight, err := vm.k.GetRagnarokBlockHeight(ctx)
	if err != nil {
		return fmt.Errorf("fail to get ragnarok height: %w", err)
	}

	if ragnarokHeight == 0 {
		ragnarokHeight = ctx.BlockHeight()
		// Use CacheContext for atomicity: distributeBondReward commits each
		// node payout independently, so a partial failure would otherwise leave
		// some nodes paid while ragnarokHeight remains 0, causing duplicate
		// payouts on retry.
		cacheCtx, commit := ctx.CacheContext()
		if err = vm.distributeBondReward(cacheCtx, mgr); err != nil {
			return fmt.Errorf("when ragnarok triggered, fail to give all active node bond reward %w", err)
		}
		vm.k.SetRagnarokBlockHeight(cacheCtx, ragnarokHeight)
		commit()
		return nil
	}

	nth, err := vm.k.GetRagnarokNth(ctx)
	if err != nil {
		return fmt.Errorf("fail to get ragnarok nth: %w", err)
	}

	position, err := vm.k.GetRagnarokWithdrawPosition(ctx)
	if err != nil {
		return fmt.Errorf("fail to get ragnarok position: %w", err)
	}
	if !position.IsEmpty() {
		if err = vm.ragnarokPools(ctx, nth, mgr); err != nil {
			return fmt.Errorf("fail to ragnarok pools: %w", err)
		}
		return nil
	}

	// check if we have any pending ragnarok transactions
	pending, err := vm.k.GetRagnarokPending(ctx)
	if err != nil {
		return fmt.Errorf("fail to get ragnarok pending: %w", err)
	}
	if pending > 0 {
		var txOutQueue int64
		txOutQueue, err = vm.getPendingTxOut(ctx)
		if err != nil {
			return fmt.Errorf("fail to get pending tx out item: %w", err)
		}
		if txOutQueue > 0 {
			ctx.Logger().Info("awaiting previous ragnarok transaction to clear before continuing", "nth", nth, "count", pending)
			return nil
		}
	}

	nth++ // increment by 1
	ctx.Logger().Info("starting next ragnarok iteration", "iteration", nth)
	if err = vm.ragnarokProtocolStage2(ctx, nth, mgr); err != nil {
		ctx.Logger().Error("fail to execute ragnarok protocol step 2", "error", err)
		return err
	}
	vm.k.SetRagnarokNth(ctx, nth)

	return nil
}

func (vm *ValidatorMgr) getPendingTxOut(ctx cosmos.Context) (int64, error) {
	signingTransactionPeriod := vm.k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	startHeight := ctx.BlockHeight() - signingTransactionPeriod
	count := int64(0)
	for height := startHeight; height <= ctx.BlockHeight(); height++ {
		txs, err := vm.k.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			return 0, fmt.Errorf("fail to get tx out array from key value store: %w", err)
		}
		for _, tx := range txs.TxArray {
			if tx.OutHash.IsEmpty() {
				count++
			}
		}
	}
	return count, nil
}

func (vm *ValidatorMgr) ragnarokProtocolStage2(ctx cosmos.Context, nth int64, mgr Manager) error {
	// Ragnarok Protocol
	// If THORNode can no longer be BFT, do a graceful shutdown of the entire network.
	// 1) THORNode will refund the validator's bond
	// 2) return all fund to liquidity providers

	// Use CacheContext for atomicity: if ragnarokPools fails after ragnarokBond
	// succeeds, all state changes are discarded to prevent partial mutations.
	cacheCtx, commit := ctx.CacheContext()

	// refund bonders
	if err := vm.ragnarokBond(cacheCtx, nth, mgr); err != nil {
		return fmt.Errorf("fail to ragnarok bond: %w", err)
	}

	// refund liquidity providers. This is last to ensure there is likely gas for the
	// returning bond and reserve
	if err := vm.ragnarokPools(cacheCtx, nth, mgr); err != nil {
		return fmt.Errorf("fail to ragnarok pools: %w", err)
	}

	commit()
	return nil
}

func (vm *ValidatorMgr) distributeBondReward(ctx cosmos.Context, mgr Manager) error {
	var resultErr error
	active, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return fmt.Errorf("fail to get all active node account: %w", err)
	}

	// Note that unlike estimated CurrentAward distribution in querier.go ,
	// this estimate treats lastChurnHeight as the active_block_height of the youngest active node,
	// rather than the block_height of the first (oldest) Asgard vault.
	// As an example, note from the below URLs that these 5293733 and 5293728 respectively in block 5336942.
	// https://gateway.liquify.com/chain/thorchain_api/thorchain/nodes?height=5336942
	// (Nodes .cxmy and .uy3a .)
	// https://gateway.liquify.com/chain/thorchain_api/thorchain/vaults/asgard?height=5336942
	lastChurnHeight := int64(0)
	for _, node := range active {
		if node.ActiveBlockHeight > lastChurnHeight {
			lastChurnHeight = node.ActiveBlockHeight
		}
	}

	totalEffectiveBond, bondHardCap := getTotalEffectiveBond(active)

	network, err := vm.k.GetNetwork(ctx)
	if err != nil {
		return fmt.Errorf("fail to get network: %w", err)
	}

	for _, item := range active {
		cacheCtx, commit := ctx.CacheContext()
		if err := vm.payNodeAccountBondAward(cacheCtx, lastChurnHeight, item, network.BondRewardRune, totalEffectiveBond, bondHardCap, mgr); err != nil {
			resultErr = errors.Join(resultErr, err)
			ctx.Logger().Error("fail to pay node account bond award", "node address", item.NodeAddress.String(), "error", err)
			// No per-node label: a node bech32 address is unbounded-cardinality
			// label material. The failing node address is already in the log line.
			telemetry.IncrCounter(float32(1), "thornode", "churn", "bond_reward_failed")
		} else {
			commit()
		}
	}
	return resultErr
}

func (vm *ValidatorMgr) ragnarokBond(ctx cosmos.Context, nth int64, mgr Manager) error {
	// bond should be returned on the back 10, not the first 10
	nth -= 10
	if nth < 1 {
		return nil
	}

	nas, err := vm.k.ListValidatorsWithBond(ctx)
	if err != nil {
		ctx.Logger().Error("can't get nodes", "error", err)
		return err
	}
	// nth * 10 == the amount of the bond we want to send
	for i, na := range nas {
		if na.Bond.IsZero() {
			continue
		}

		if nth > 10 { // cap at 10
			nth = 10
		}
		amt := na.Bond.MulUint64(uint64(nth)).QuoUint64(10)

		bp, err := vm.k.GetBondProviders(ctx, na.NodeAddress)
		if err != nil {
			return fmt.Errorf("fail to get bond providers(%s): %w", na.NodeAddress, err)
		}
		if err := passiveBackfill(ctx, mgr, na, &bp); err != nil {
			return err
		}
		bp.Adjust(na.Bond)

		// refund bond proportionally to each bond provider
		totalRefunded := cosmos.ZeroUint()
		for _, provider := range bp.Providers {
			if provider.Bond.IsZero() || provider.BondAddress.Empty() {
				continue
			}
			providerAmt := common.GetSafeShare(provider.Bond, na.Bond, amt)
			if providerAmt.IsZero() {
				continue
			}

			txOutItem := TxOutItem{
				Chain:      common.RuneAsset().Chain,
				ToAddress:  common.Address(provider.BondAddress.String()),
				InHash:     common.BlankTxID,
				Coin:       common.NewCoin(common.RuneAsset(), providerAmt),
				Memo:       NewRagnarokMemo(ctx.BlockHeight()).String(),
				ModuleName: BondName,
			}
			ok, err := vm.txOutStore.TryAddTxOutItem(ctx, mgr, txOutItem, cosmos.ZeroUint())
			if err != nil {
				if !errors.Is(err, ErrNotEnoughToPayFee) {
					return err
				}
				ctx.Logger().Error("not enough to pay fee during ragnarok bond refund, proceeding best effort", "provider", provider.BondAddress, "node", na.NodeAddress)
			}
			if !ok && err == nil {
				continue
			}

			// add a pending ragnarok transaction
			pending, err := vm.k.GetRagnarokPending(ctx)
			if err != nil {
				return fmt.Errorf("fail to get ragnarok pending: %w", err)
			}
			vm.k.SetRagnarokPending(ctx, pending+1)

			bp.Unbond(providerAmt, provider.BondAddress)
			totalRefunded = totalRefunded.Add(providerAmt)

			bondEvent := NewEventBond(providerAmt, BondCost, common.Tx{}, &nas[i], provider.BondAddress)
			if err := mgr.EventMgr().EmitEvent(ctx, bondEvent); err != nil {
				return fmt.Errorf("fail to emit bond event: %w", err)
			}
		}

		if totalRefunded.IsZero() {
			continue
		}

		na.Bond = common.SafeSub(na.Bond, totalRefunded)
		if err := vm.k.SetNodeAccount(ctx, na); err != nil {
			return err
		}
		if err := vm.k.SetBondProviders(ctx, bp); err != nil {
			return fmt.Errorf("fail to save bond providers(%s): %w", na.NodeAddress, err)
		}
	}

	return nil
}

func (vm *ValidatorMgr) ragnarokPools(ctx cosmos.Context, nth int64, mgr Manager) error {
	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return fmt.Errorf("fail to get active nodes: %w", err)
	}
	if len(nas) == 0 {
		return fmt.Errorf("can't find any active nodes")
	}
	na := nas[0]

	position, err := vm.k.GetRagnarokWithdrawPosition(ctx)
	if err != nil {
		return fmt.Errorf("fail to get ragnarok position: %w", err)
	}
	basisPoints := MaxWithdrawBasisPoints
	// go through all the pools
	pools, err := vm.k.GetPools(ctx)
	if err != nil {
		return fmt.Errorf("fail to get pools: %w", err)
	}
	// set all pools to staged status
	for _, pool := range pools {
		if pool.Status != PoolStaged {
			poolEvent := NewEventPool(pool.Asset, PoolStaged)
			if err = vm.eventMgr.EmitEvent(ctx, poolEvent); err != nil {
				ctx.Logger().Error("fail to emit pool event", "error", err)
			}

			pool.Status = PoolStaged
			if err = vm.k.SetPool(ctx, pool); err != nil {
				return fmt.Errorf("fail to set pool %s to Stage status: %w", pool.Asset, err)
			}
		}
	}

	// the following line is pointless, granted. But in this case, removing it
	// would cause a consensus failure
	_ = vm.k.GetLowestActiveVersion(ctx)

	nextPool := false
	maxWithdrawsPerBlock := 20
	count := 0

	for i := len(pools) - 1; i >= 0; i-- { // iterate backwards
		pool := pools[i]

		if nextPool { // we've iterated to the next pool after our position pool
			position.Pool = pool.Asset
		}

		if !position.Pool.IsEmpty() && !pool.Asset.Equals(position.Pool) {
			continue
		}

		nextPool = true
		position.Pool = pool.Asset

		// withdraw gas asset pool on the back 10 nths
		if nth <= 10 && pool.Asset.IsGasAsset() {
			continue
		}

		j := int64(-1)
		iterator := vm.k.GetLiquidityProviderIterator(ctx, pool.Asset)
		defer iterator.Close()
		for ; iterator.Valid(); iterator.Next() {
			j++
			if j == position.Number {
				position.Number++
				var lp LiquidityProvider
				if err = vm.k.Cdc().Unmarshal(iterator.Value(), &lp); err != nil {
					ctx.Logger().Error("fail to unmarshal liquidity provider", "error", err)
					continue
				}

				if lp.Units.IsZero() {
					continue
				}
				var withdrawAddr common.Address
				withdrawAsset := common.EmptyAsset
				if !lp.RuneAddress.IsEmpty() {
					withdrawAddr = lp.RuneAddress
					// if liquidity provider only add RUNE , then asset address will be empty
					if lp.AssetAddress.IsEmpty() {
						withdrawAsset = common.RuneAsset()
					}
				} else {
					// if liquidity provider only add Asset, then RUNE Address will be empty
					withdrawAddr = lp.AssetAddress
					withdrawAsset = lp.Asset
				}
				withdrawMsg := NewMsgWithdrawLiquidity(
					common.GetRagnarokTx(pool.Asset.Chain, withdrawAddr, withdrawAddr),
					withdrawAddr,
					cosmos.NewUint(uint64(basisPoints)),
					pool.Asset,
					withdrawAsset,
					na.NodeAddress,
				)

				handler := newLuviProtocolHandler(mgr)
				_, err = handler(ctx, withdrawMsg)
				if err != nil {
					ctx.Logger().Error("fail to withdraw", "liquidity provider", lp.RuneAddress, "error", err)
				} else if !withdrawAsset.Equals(common.RuneAsset()) {
					// when withdraw asset is only RUNE , then it should process more , because RUNE asset doesn't leave THORChain
					count++
					pending, err := vm.k.GetRagnarokPending(ctx)
					if err != nil {
						vm.k.SetRagnarokWithdrawPosition(ctx, position)
						return fmt.Errorf("fail to get ragnarok pending: %w", err)
					}
					vm.k.SetRagnarokPending(ctx, pending+1)
					if count >= maxWithdrawsPerBlock {
						break
					}
				}
			}
		}
		if count >= maxWithdrawsPerBlock {
			break
		}
		position.Number = 0
	}

	if count < maxWithdrawsPerBlock { // we've completed all pools/liquidity providers, reset the position
		position = RagnarokWithdrawPosition{}
	}
	vm.k.SetRagnarokWithdrawPosition(ctx, position)

	return nil
}

// setupValidatorNodes it is one off it only get called when genesis
func (vm *ValidatorMgr) setupValidatorNodes(ctx cosmos.Context, height int64) error {
	if height != genesisBlockHeight {
		ctx.Logger().Info("only need to setup validator node when start up", "height", height)
		return nil
	}

	iter := vm.k.GetNodeAccountIterator(ctx)
	defer iter.Close()
	readyNodes := NodeAccounts{}
	activeCandidateNodes := NodeAccounts{}
	for ; iter.Valid(); iter.Next() {
		var na NodeAccount
		if err := vm.k.Cdc().Unmarshal(iter.Value(), &na); err != nil {
			return fmt.Errorf("fail to unmarshal node account, %w", err)
		}
		// when THORNode first start , THORNode only care about these two status
		switch na.Status {
		case NodeReady:
			readyNodes = append(readyNodes, na)
		case NodeActive:
			activeCandidateNodes = append(activeCandidateNodes, na)
		}
	}
	totalActiveValidators := len(activeCandidateNodes)
	totalNominatedValidators := len(readyNodes)
	if totalActiveValidators == 0 && totalNominatedValidators == 0 {
		return errors.New("no validators available")
	}

	sort.Sort(activeCandidateNodes)
	sort.Sort(readyNodes)
	activeCandidateNodes = append(activeCandidateNodes, readyNodes...)
	desiredValidatorSet := vm.k.GetConfigInt64(ctx, constants.DesiredValidatorSet)
	for idx, item := range activeCandidateNodes {
		if int64(idx) < desiredValidatorSet {
			item.UpdateStatus(NodeActive, ctx.BlockHeight())
		} else {
			item.UpdateStatus(NodeStandby, ctx.BlockHeight())
		}
		if err := vm.k.SetNodeAccount(ctx, item); err != nil {
			return fmt.Errorf("fail to save node account: %w", err)
		}
	}
	return nil
}

func (vm *ValidatorMgr) getScore(ctx cosmos.Context, slashPts, lastChurnHeight int64) cosmos.Uint {
	// get to the 8th decimal point, but keep numbers integers for safer math
	score := cosmos.NewUint(uint64((ctx.BlockHeight() - lastChurnHeight) * common.One))
	if slashPts == 0 {
		return score
	}
	return score.QuoUint64(uint64(slashPts))
}

// Iterate over active node accounts, finding bad actors with high slash points
func (vm *ValidatorMgr) findBadActors(ctx cosmos.Context, minSlashPointsForBadValidator, badValidatorRedline int64) (NodeAccounts, error) {
	badActors := make(NodeAccounts, 0)

	// Guard against division by zero: badValidatorRedline is used as a divisor
	// below. If Mimir sets BadValidatorRedline to 0, skip bad actor detection
	// rather than panicking in a consensus-critical code path.
	if badValidatorRedline <= 0 {
		return badActors, nil
	}

	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return badActors, err
	}

	if len(nas) == 0 {
		return nil, nil
	}

	// NOTE: Our score gives a numerical representation of the behavior our a
	// node account. The lower the score, the worse behavior. The score is
	// determined by relative to how many slash points they have over how long
	// they have been an active node account.
	type badTracker struct {
		Score       cosmos.Uint
		NodeAccount NodeAccount
	}
	tracker := make([]badTracker, 0, len(nas))
	totalScore := cosmos.ZeroUint()

	// Find bad actor relative to age / slashpoints
	lastChurnHeight := getLastChurnHeight(ctx, vm.k)
	for _, na := range nas {
		slashPts, err := vm.k.GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		if err != nil {
			return badActors, fmt.Errorf("fail to get node slash points: %w", err)
		}

		if slashPts <= minSlashPointsForBadValidator {
			continue
		}

		score := vm.getScore(ctx, slashPts, lastChurnHeight)
		totalScore = totalScore.Add(score)

		tracker = append(tracker, badTracker{
			Score:       score,
			NodeAccount: na,
		})
	}

	if len(tracker) == 0 {
		// no offenders, exit nicely
		return nil, nil
	}

	sort.SliceStable(tracker, func(i, j int) bool {
		return tracker[i].Score.LT(tracker[j].Score)
	})

	// score lower is worse
	avgScore := totalScore.QuoUint64(uint64(len(nas)))

	// NOTE: our redline is a hard line in the sand to determine if a node
	// account is sufficiently bad that it should just be removed now. This
	// ensures that if we have multiple "really bad" node accounts, they all
	// can get removed in the same churn. It is important to note we shouldn't
	// be able to churn out more than 1/3rd of our node accounts in a single
	// churn, as that could threaten the security of the funds. This logic to
	// protect against this is not inside this function.
	redline := avgScore.QuoUint64(uint64(badValidatorRedline))

	// find any node accounts that have crossed the red line
	for _, track := range tracker {
		if redline.GTE(track.Score) {
			badActors = append(badActors, track.NodeAccount)
		}
	}

	// if no one crossed the redline, lets just grab the worse offender
	if len(badActors) == 0 {
		badActors = NodeAccounts{tracker[0].NodeAccount}
	}

	return badActors, nil
}

// markChainSetActors marks Active nodes for churn-out when their most recent asgard
// keygen report omitted a chain that a pending vault of this churn cycle was then
// created without.
//
// NodeAccountPreflightCheck can only hold back a churn-IN candidate. An Active member
// never passes through it, and a vault's chain set is fixed at creation, so Active
// members reporting without a chain can drop it from the vault they help generate (a
// chain is kept only if two thirds of the members list it) and stall every rotation
// attempt for as long as they stay members. Membership of the next vault IS the next
// active set - nextVaultNodeAccounts returns one list for both - so leaving a node out
// of the ceremony and churning it out are the same operation. There is no lighter lever.
//
// A node is judged only on a chain that actually stalled: one the active vaults serve
// that a pending InitVault was created without (recordChainSetStalls). Omitting a chain
// that still reached its supermajority stalls nothing, so a node lagging on a new chain
// is not marked on a retry caused by something else.
//
// Every faulty node is marked, not a subset. Marks are cleared on every churn attempt,
// and a node left out of a keygen cannot file a newer report, so a node marked on one
// attempt is faulty again on the next; choosing a subset would keep choosing the same
// nodes and never reach the rest. ChainSetChurnOutMax is instead a sanity limit: when
// more nodes are faulty than that, none are marked, because a stall that many nodes
// cause is a network-level event for governance, not a misconfigured node.
//
// The same evidence also outlasts a fix. A node that enables the chain after being left
// out cannot show it until it is in a keygen again, so it stays out until the churn
// completes, when the new vaults are newer than its report. That costs it the rest of
// the cycle on standby and no slash; letting it back in to re-report would let a node
// that is still misconfigured stall every other attempt.
//
// Every mark runs before nextVaultNodeAccounts in the same churnInner, which sorts these
// nodes ahead of the other leave candidates (see chainSetLeaveFirst). Removal is still
// subject to findCountToRemove, so no more than findMaxAbleToLeave can go at once.
//
// Disabled unless mimir ChainSetChurnOutMax is above 0. This ejects a validator over a
// configuration fault, so it is armed deliberately - ideally once the preflight half has
// shown that the recorded reports match reality.
func (vm *ValidatorMgr) markChainSetActors(ctx cosmos.Context) error {
	maxFaulty, err := vm.k.GetMimir(ctx, constants.MimirKeyChainSetChurnOutMax)
	if err != nil || maxFaulty <= 0 {
		return nil // unset or 0: skip this mark
	}

	if err = vm.recordChainSetStalls(ctx); err != nil {
		return err
	}

	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return err
	}
	faulty, err := vm.chainSetFaultyNodes(ctx, nas)
	if err != nil {
		return err
	}
	for _, n := range faulty {
		// Only mark an actor not already marked for churn-out.
		if n.LeaveScore > 0 {
			continue
		}
		ctx.Logger().Info("active node reported an incomplete keygen chain set",
			"node address", n.NodeAddress.String(),
		)
		if err := vm.markActor(ctx, n, "for reporting an incomplete keygen chain set"); err != nil {
			return err
		}
	}

	return nil
}

// recordChainSetStalls records, for the current churn cycle, every chain the active
// asgard vaults serve that a pending asgard InitVault was created without. Such a vault
// cannot rotate in. The failed round's InitVaults are still present here: they are only
// retired when the next keygen is scheduled, after the marks.
func (vm *ValidatorMgr) recordChainSetStalls(ctx cosmos.Context) error {
	required, vaultHeight, err := vm.activeVaultChains(ctx)
	if err != nil {
		return err
	}
	if len(required) == 0 {
		return nil
	}
	initVaults, err := vm.k.GetAsgardVaultsByStatus(ctx, InitVault)
	if err != nil {
		return err
	}
	for _, vault := range initVaults {
		// The XMR monovault has its own fixed chain set; see activeVaultChains.
		if vault.HasChain(common.XMRChain) {
			continue
		}
		for _, chain := range required {
			if !vault.HasChain(chain) {
				vm.k.SetChainSetStall(ctx, chain, vaultHeight)
			}
		}
	}
	return nil
}

// Iterate over active node accounts, finding the one that hasn't been signing blocks
func (vm *ValidatorMgr) markMissingActors(ctx cosmos.Context) error {
	maxMissingBlocks := vm.k.GetConfigInt64(ctx, constants.MissingBlockChurnOut)
	maxChurnOut := vm.k.GetConfigInt64(ctx, constants.MaxMissingBlockChurnOut)
	if maxMissingBlocks == 0 || maxChurnOut == 0 {
		return nil // skip this mark
	}

	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return err
	}

	// sort node accounts by number of missing blocks, highest first
	sort.SliceStable(nas, func(i, j int) bool {
		return nas[i].MissingBlocks > nas[j].MissingBlocks
	})

	counter := int64(0)
	for _, n := range nas {
		// Only mark an old actor not already marked for churn-out.
		if n.LeaveScore > 0 {
			continue
		}

		if maxMissingBlocks < int64(n.MissingBlocks) {
			if err := vm.markActor(ctx, n, "for not signing blocks"); err != nil {
				return err
			}
			counter += 1
			if counter >= maxChurnOut {
				break
			}
		}
	}

	return nil
}

// Iterate over active node accounts, finding those that miss most of the voters on
// a chain, worst first
func (vm *ValidatorMgr) markMissingObservationActors(ctx cosmos.Context) error {
	maxChurnOut := vm.k.GetConfigInt64(ctx, constants.MaxMissingObservationChurnOut)
	if maxChurnOut <= 0 {
		return nil // skip this mark
	}
	thresholdBps := vm.k.GetConfigInt64(ctx, constants.MissingObservationChurnOutBps)
	minSamples := vm.k.GetConfigInt64(ctx, constants.MissingObservationMinSamples) * observationTallyUnit

	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return err
	}

	type candidate struct {
		na       NodeAccount
		chain    common.Chain
		ratioBps int64
	}
	var candidates []candidate
	for _, na := range nas {
		// Only mark an actor not already marked for churn-out.
		if na.LeaveScore > 0 {
			continue
		}
		tallies, err := vm.k.GetObservationTallies(ctx, na.NodeAddress)
		if err != nil {
			// An unreadable tally is not evidence against the node, and must not
			// fail the churn.
			ctx.Logger().Error("fail to get observation tallies", "node address", na.NodeAddress, "error", err)
			continue
		}
		worst := candidate{na: na, ratioBps: -1}
		for _, t := range tallies {
			if !observationTallyFailing(t.Missed, t.Total, thresholdBps, minSamples) {
				continue
			}
			ratioBps := min(t.Missed, t.Total) * 10_000 / t.Total
			if ratioBps > worst.ratioBps {
				worst.chain, worst.ratioBps = t.Chain, ratioBps
			}
		}
		if worst.ratioBps >= 0 {
			candidates = append(candidates, worst)
		}
	}

	// worst ratio first; the address breaks ties so every node marks the same nodes
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ratioBps != candidates[j].ratioBps {
			return candidates[i].ratioBps > candidates[j].ratioBps
		}
		return candidates[i].na.NodeAddress.String() < candidates[j].na.NodeAddress.String()
	})

	for i, c := range candidates {
		if int64(i) >= maxChurnOut {
			break
		}
		ctx.Logger().Info("node missing observations", "node address", c.na.NodeAddress, "chain", c.chain, "missed bps", c.ratioBps)
		if err := vm.markActor(ctx, c.na, "for missing observations"); err != nil {
			return err
		}
	}

	return nil
}

// Iterate over active node accounts, finding the one that has been active longest
func (vm *ValidatorMgr) findOldActor(ctx cosmos.Context) (NodeAccount, error) {
	na := NodeAccount{}
	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return na, err
	}

	na.StatusSince = ctx.BlockHeight() // set the start status age to "now"
	for _, n := range nas {
		// Only mark an old actor not already marked for churn-out.
		if n.LeaveScore > 0 {
			continue
		}
		if n.StatusSince < na.StatusSince {
			na = n
		}
	}

	return na, nil
}

// Iterate over active node accounts, finding the one that has the lowest bond
func (vm *ValidatorMgr) findLowBondActor(ctx cosmos.Context) (NodeAccount, error) {
	na := NodeAccount{}
	nas, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return na, err
	}

	first := true
	for _, n := range nas {
		// Only mark a low bond actor not already marked for churn-out.
		if n.LeaveScore > 0 {
			continue
		}
		if first || n.Bond.LT(na.Bond) {
			na = n
			first = false
		}
	}

	return na, nil
}

// Mark an actor to be churned out
func (vm *ValidatorMgr) markActor(ctx cosmos.Context, na NodeAccount, reason string) error {
	if !na.IsEmpty() && na.LeaveScore == 0 {
		ctx.Logger().Info("marked Validator to be churned out", "node address", na.NodeAddress, "reason", reason)
		// reason is always one of seven compile-time string literals at the seven
		// markActor call sites in this file, so label cardinality is fixed at 7.
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "churn", "node_marked"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("reason", reason)},
		)
		slashPts, err := vm.k.GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		if err != nil {
			return fmt.Errorf("fail to get node account(%s) slash points: %w", na.NodeAddress, err)
		}
		na.LeaveScore = vm.getScore(ctx, slashPts, getLastChurnHeight(ctx, vm.k)).Uint64()
		return vm.k.SetNodeAccount(ctx, na)
	}
	return nil
}

// Mark an old actor to be churned out
func (vm *ValidatorMgr) markOldActor(ctx cosmos.Context) error {
	na, err := vm.findOldActor(ctx)
	if err != nil {
		return err
	}
	if err := vm.markActor(ctx, na, "for age"); err != nil {
		return err
	}
	return nil
}

// Mark a low bond actor to be churned out once the active set is at capacity.
func (vm *ValidatorMgr) markLowBondActor(ctx cosmos.Context, desiredValidatorSet int64) error {
	if desiredValidatorSet > 0 {
		nas, err := vm.k.ListActiveValidators(ctx)
		if err != nil {
			return err
		}
		if int64(len(nas)) < desiredValidatorSet {
			return nil
		}
	}

	na, err := vm.findLowBondActor(ctx)
	if err != nil {
		return err
	}
	if err := vm.markActor(ctx, na, "for low bond"); err != nil {
		return err
	}
	return nil
}

// Mark a bad actor to be churned out
func (vm *ValidatorMgr) markBadActor(ctx cosmos.Context, minSlashPointsForBadValidator, redline int64) (int64, error) {
	nas, err := vm.findBadActors(ctx, minSlashPointsForBadValidator, redline)
	if err != nil {
		return 0, err
	}
	for _, na := range nas {
		if err := vm.markActor(ctx, na, "for bad behavior"); err != nil {
			return 0, err
		}
	}
	return int64(len(nas)), nil
}

// Mark up to `MaxNodeToChurnOutForLowVersion` nodes as low version
// This will slate them to churn out. `MaxNodeToChurnOutForLowVersion`
// is a Mimir setting that defaults in constants to 1
func (vm *ValidatorMgr) markLowVersionValidators(ctx cosmos.Context) error {
	// Only mark low version validators later than ChurnOutForLowVersionBlocks since the MinJoinVersion last changed.
	_, minJoinlastHeight := vm.k.GetMinJoinLast(ctx)
	churnOutForLowVersionBlocks := vm.k.GetConfigInt64(ctx, constants.ChurnOutForLowVersionBlocks)
	if ctx.BlockHeight() < minJoinlastHeight+churnOutForLowVersionBlocks {
		return nil
	}

	// Get max number of nodes to mark as low version
	maxNodes := vm.k.GetConfigInt64(ctx, constants.MaxNodeToChurnOutForLowVersion)

	nodeAccs, err := vm.findLowVersionValidators(ctx, maxNodes)
	if err != nil {
		return err
	}
	if len(nodeAccs) > 0 {
		for _, na := range nodeAccs {
			if err := vm.markActor(ctx, na, "for version lower than minimum join version"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Finds up to `maxNodesToFind` active validators with version lower than the most "popular" version
func (vm *ValidatorMgr) findLowVersionValidators(ctx cosmos.Context, maxNodesToFind int64) (NodeAccounts, error) {
	minimumVersion, _ := vm.k.GetMinJoinLast(ctx)
	activeNodes, err := vm.k.ListValidatorsByStatus(ctx, NodeActive)
	if err != nil {
		return NodeAccounts{}, err
	}
	nodeAccs := NodeAccounts{}
	for _, na := range activeNodes {
		// Only mark low version actors not already marked for churn-out.
		if na.LeaveScore > 0 {
			continue
		}
		if na.GetVersion().LT(minimumVersion) {
			nodeAccs = append(nodeAccs, na)
		}
		if len(nodeAccs) == int(maxNodesToFind) {
			return nodeAccs, nil
		}
	}
	return nodeAccs, nil
}

// clearLeaveScores - clears all leaves scores of active validators except for
// ones that requested to leave
func (vm *ValidatorMgr) clearLeaveScores(ctx cosmos.Context) error {
	active, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return err
	}

	for _, na := range active {
		if na.RequestedToLeave || na.ForcedToLeave {
			continue
		}
		na.LeaveScore = 0

		if err := vm.k.SetNodeAccount(ctx, na); err != nil {
			return err
		}
	}

	return nil
}

// find any actor that are ready to become "ready" status
func (vm *ValidatorMgr) markReadyActors(ctx cosmos.Context) error {
	standby, err := vm.k.ListValidatorsByStatus(ctx, NodeStandby)
	if err != nil {
		return err
	}
	ready, err := vm.k.ListValidatorsByStatus(ctx, NodeReady)
	if err != nil {
		return err
	}

	// check all ready and standby nodes are in "ready" state (upgrade/downgrade as needed)
	for _, na := range append(standby, ready...) {
		status, _ := vm.NodeAccountPreflightCheck(ctx, na, vm.k.GetConstants())
		na.UpdateStatus(status, ctx.BlockHeight())

		if err := vm.k.SetNodeAccount(ctx, na); err != nil {
			return err
		}
	}

	return nil
}

// NodeAccountPreflightCheck preflight check to find out what the node account's next status will be
func (vm *ValidatorMgr) NodeAccountPreflightCheck(ctx cosmos.Context, na NodeAccount, _ constants.ConstantValues) (NodeStatus, error) {
	// ensure banned nodes can't get churned in again
	if na.ForcedToLeave {
		return NodeDisabled, fmt.Errorf("node account has been banned")
	}

	// Check if they've requested to leave
	if na.RequestedToLeave {
		return NodeStandby, fmt.Errorf("node account has requested to leave")
	}

	if na.Maintenance {
		return NodeStandby, fmt.Errorf("node account is in maintenance mode")
	}

	// Check that the node account has an IP address
	if net.ParseIP(na.IPAddress) == nil {
		return NodeStandby, fmt.Errorf("node account has invalid registered IP address")
	}

	// Check that the node account has an pubkey set
	if na.PubKeySet.IsEmpty() {
		return NodeWhiteListed, fmt.Errorf("node account has not registered their pubkey set")
	}

	// ensure we have enough rune
	minBond := vm.k.GetConfigInt64(ctx, constants.MinimumBondInRune)
	if na.Bond.LT(cosmos.NewUint(uint64(minBond))) {
		return NodeStandby, fmt.Errorf("node account does not have minimum bond requirement: %d/%d", na.Bond.Uint64(), minBond)
	}

	minVersion, _ := vm.k.GetMinJoinLast(ctx)
	// Check version number is still supported
	if na.GetVersion().LT(minVersion) {
		return NodeStandby, fmt.Errorf("node account does not meet min version requirement: %s vs %s", na.Version, minVersion)
	}

	jail, err := vm.k.GetNodeAccountJail(ctx, na.NodeAddress)
	if err != nil {
		ctx.Logger().Error("fail to get node account jail", "error", err)
		return NodeStandby, fmt.Errorf("cannot fetch jail status: %w", err)
	}
	if jail.IsJailed(ctx) {
		return NodeStandby, fmt.Errorf("node account is jailed until block %d: %s", jail.ReleaseHeight, jail.Reason)
	}

	if vm.k.RagnarokInProgress(ctx) {
		return NodeStandby, fmt.Errorf("ragnarok is currently in progress: no churning")
	}

	if err := vm.keygenChainSetCheck(ctx, na); err != nil {
		return NodeStandby, err
	}

	return NodeReady, nil
}

// keygenChainSetCheck keeps a node out of the Ready set when its most recent asgard
// keygen report declared a chain set that omits a chain the active vaults serve.
//
// A new vault keeps a chain only when a supermajority of the keygen members report it
// (TssVoter.ConsensusChains), and tryRotateInitVaults refuses to rotate vaults whose
// chain sets differ. One member running with a chain disabled can therefore drop that
// chain from the vault it helped generate and stall the rotation, which retries every
// ChurnRetryInterval with the same members - so the same report stalls every retry.
// The report that proves the misconfiguration is already on chain in MsgTssPool.Chains
// and is recorded per node by SetLastKeygenChains, so the next tick can leave that node
// out of the Ready set rather than re-running a ceremony that cannot rotate.
//
// Only a report at or after the newest active vault's creation height is judged, which
// bounds the check in both directions:
//   - a node that has never reported an asgard keygen has nothing to judge, so new nodes
//     and nodes that have never been selected are unaffected;
//   - an older report may predate a chain being added to the vaults, so a chain missing
//     from it is not evidence of anything;
//   - the next completed churn creates newer vaults and so expires every recorded
//     report. A node held back here sits out only the churn it would have stalled and is
//     judged again on its next report, so it cannot be locked out of the set.
//
// A halted chain is still required: halting stops observation and signing, but the vault
// chain set is built from the reports regardless, so omitting a halted chain stalls the
// rotation exactly the same way. A chain under RAGNAROK is not (see chainRetiring).
func (vm *ValidatorMgr) keygenChainSetCheck(ctx cosmos.Context, na NodeAccount) error {
	if disabled, err := vm.k.GetMimir(ctx, constants.MimirKeyPreflightChainSetDisabled); err == nil && disabled > 0 {
		return nil
	}

	required, vaultHeight, err := vm.activeVaultChains(ctx)
	if err != nil {
		// An unreadable input is not evidence of a missing chain. Leave the node's
		// readiness to the other checks rather than holding back the whole Ready set on
		// a store error.
		ctx.Logger().Error("fail to get active asgard vaults", "error", err)
		return nil
	}

	missing, reportHeight := vm.missingKeygenChain(ctx, na, required, vaultHeight)
	if missing.IsEmpty() {
		return nil
	}

	return fmt.Errorf("node account keygen report at height %d omitted chain %s served by the active vaults", reportHeight, missing)
}

// activeVaultChains returns the chains the active asgard vaults serve, and the creation
// height of the newest of them. XMR vaults are excluded from both, and chains being
// retired from the chain set (see chainRetiring).
func (vm *ValidatorMgr) activeVaultChains(ctx cosmos.Context) (common.Chains, int64, error) {
	vaults, err := vm.k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return nil, 0, err
	}

	var vaultHeight int64
	chains := make(common.Chains, 0)
	for _, vault := range vaults {
		// The XMR monovault keygen reports its own fixed chain set
		// (Signer.keygenChainsForType), so an asgard report never carries XMR and its
		// absence says nothing about the reporter. The monovault also rotates on its own
		// schedule, so its creation height must not set the report-age bound either: a
		// monovault newer than the asgard vaults would push the bound past every asgard
		// report and silently disable the check. Skip it the way
		// regularVaultMembershipNeedsKeygen does.
		if vault.HasChain(common.XMRChain) {
			continue
		}
		if vault.BlockHeight > vaultHeight {
			vaultHeight = vault.BlockHeight
		}
		for _, chain := range vault.GetChains() {
			if chains.Has(chain) || vm.chainRetiring(ctx, chain) {
				continue
			}
			chains = append(chains, chain)
		}
	}

	return chains, vaultHeight, nil
}

// missingKeygenChain returns the first of the required chains that a node's most recent
// asgard keygen report omitted, with that report's keygen height.
//
// It returns EmptyChain whenever there is nothing to judge: an unreadable record, no
// report on record at all, a report older than the active vaults, or a complete report.
// See keygenChainSetCheck for why the report-age bound is what stops any of this from
// locking a node out.
func (vm *ValidatorMgr) missingKeygenChain(ctx cosmos.Context, na NodeAccount, required common.Chains, vaultHeight int64) (common.Chain, int64) {
	reportHeight, declared, err := vm.k.GetLastKeygenChains(ctx, na.NodeAddress)
	if err != nil {
		ctx.Logger().Error("fail to get last keygen chains", "error", err, "node address", na.NodeAddress.String())
		return common.EmptyChain, 0
	}
	if reportHeight == 0 || reportHeight < vaultHeight {
		return common.EmptyChain, reportHeight
	}

	for _, chain := range required {
		if !declared.Has(chain) {
			return chain, reportHeight
		}
	}

	return common.EmptyChain, reportHeight
}

// chainRetiring reports whether a chain is being retired, signalled on chain by the
// RAGNAROK-<gas asset> mimir. Retiring a chain has operators set OptToRetire in their
// bifrost config, which drops the chain from their keygen reports
// (Signer.keygenChainsForType) while the active vaults still carry it. Requiring it
// would hold back, or churn out, exactly the nodes that adopted the retirement first.
func (vm *ValidatorMgr) chainRetiring(ctx cosmos.Context, chain common.Chain) bool {
	return vm.k.IsRagnarok(ctx, []common.Asset{chain.GetGasAsset()})
}

// Returns a list of nodes to include in the next pool
func (vm *ValidatorMgr) nextVaultNodeAccounts(ctx cosmos.Context, targetCount int) (NodeAccounts, bool, error) {
	rotation := false // track if are making any changes to the current active node accounts

	ready, err := vm.k.ListValidatorsByStatus(ctx, NodeReady)
	if err != nil {
		return nil, false, err
	}

	// sort by bond size, descending
	sort.SliceStable(ready, func(i, j int) bool {
		return ready[i].Bond.GT(ready[j].Bond)
	})

	active, err := vm.k.ListActiveValidators(ctx)
	if err != nil {
		return nil, false, err
	}

	// find out all the nodes that had been marked to leave , and update their score again , because even after a node has been marked
	// to be churn out , they can continue to accumulate slash points, in the scenario that an active node go offline , and consistently fail
	// keygen / keysign for a while , we would like to churn it out first
	lastChurnHeight := getLastChurnHeight(ctx, vm.k)
	for idx, item := range active {

		if item.LeaveScore == 0 {
			continue
		}
		var slashPts int64
		slashPts, err = vm.k.GetNodeAccountSlashPoints(ctx, item.NodeAddress)
		if err != nil {
			ctx.Logger().Error("fail to get node account slash points", "error", err, "node address", item.NodeAddress.String())
			continue
		}
		newScore := vm.getScore(ctx, slashPts, lastChurnHeight)
		if !newScore.IsZero() {
			active[idx].LeaveScore = newScore.Uint64()
		}
	}

	chainSetFirst := vm.chainSetLeaveFirst(ctx, active)

	// sort by LeaveScore ascending
	// giving preferential treatment to people who are forced to leave,
	//  then those who would stall the keygen on their chain set, then requested to leave
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].ForcedToLeave != active[j].ForcedToLeave {
			return active[i].ForcedToLeave
		}
		fi, fj := chainSetFirst[active[i].NodeAddress.String()], chainSetFirst[active[j].NodeAddress.String()]
		if fi != fj {
			return fi
		}
		if active[i].RequestedToLeave != active[j].RequestedToLeave {
			return active[i].RequestedToLeave
		}
		// sort by LeaveHeight ascending , but exclude LeaveHeight == 0 , because that's the default value
		if active[i].LeaveScore == 0 && active[j].LeaveScore > 0 {
			return false
		}
		if active[i].LeaveScore > 0 && active[j].LeaveScore == 0 {
			return true
		}
		return active[i].LeaveScore < active[j].LeaveScore
	})

	toRemove := findCountToRemove(active)
	if toRemove > 0 {
		rotation = true
		active = active[toRemove:]
	}
	newNode, err := vm.k.GetMimir(ctx, constants.NumberOfNewNodesPerChurn.String())
	if err != nil || newNode <= 0 {
		newNode = 1
	}

	// add ready nodes to become active
	limit := toRemove + int(newNode) // Max limit of ready nodes to churn in
	minimumNodesForBFT := vm.k.GetConstants().GetInt64Value(constants.MinimumNodesForBFT)
	if len(active)+limit < int(minimumNodesForBFT) {
		limit = int(minimumNodesForBFT) - len(active)
	}

	for i := 1; targetCount > len(active); i++ {
		if len(ready) >= i {
			rotation = true
			active = append(active, ready[i-1])
		}
		if i >= limit || i > len(ready) { // limit adding ready accounts
			break
		}
	}

	if !rotation {
		needsKeygen, keygenErr := vm.regularVaultMembershipNeedsKeygen(ctx, active)
		if keygenErr != nil {
			return nil, false, keygenErr
		}
		rotation = needsKeygen
	}

	return active, rotation, nil
}

// chainSetFaultyNodes returns, in node-address order, the given Active nodes whose most
// recent asgard keygen report in this churn cycle omitted a chain that stalled a
// rotation in this cycle (recordChainSetStalls). It returns none when the mark is off,
// and none when more nodes are faulty than ChainSetChurnOutMax (see markChainSetActors).
func (vm *ValidatorMgr) chainSetFaultyNodes(ctx cosmos.Context, active NodeAccounts) (NodeAccounts, error) {
	maxFaulty, err := vm.k.GetMimir(ctx, constants.MimirKeyChainSetChurnOutMax)
	if err != nil || maxFaulty <= 0 {
		return nil, nil
	}
	required, vaultHeight, err := vm.activeVaultChains(ctx)
	if err != nil {
		return nil, err
	}
	stalls, err := vm.k.GetChainSetStalls(ctx, vaultHeight)
	if err != nil {
		return nil, err
	}
	// A stalled chain that is no longer required (now under RAGNAROK) no longer counts.
	stalled := make(common.Chains, 0, len(stalls))
	for _, chain := range stalls {
		if required.Has(chain) {
			stalled = append(stalled, chain)
		}
	}
	if len(stalled) == 0 {
		return nil, nil
	}

	sorted := make(NodeAccounts, len(active))
	copy(sorted, active)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].NodeAddress.String() < sorted[j].NodeAddress.String()
	})
	var faulty NodeAccounts
	for _, na := range sorted {
		if missing, _ := vm.missingKeygenChain(ctx, na, stalled, vaultHeight); !missing.IsEmpty() {
			faulty = append(faulty, na)
		}
	}
	if int64(len(faulty)) > maxFaulty {
		ctx.Logger().Info("more nodes reported an incomplete keygen chain set than ChainSetChurnOutMax, marking none",
			"faulty", len(faulty),
			"limit", maxFaulty,
		)
		return nil, nil
	}
	return faulty, nil
}

// chainSetLeaveFirst returns, keyed by node address, the chain-set-faulty nodes that
// nextVaultNodeAccounts sorts right after ForcedToLeave and ahead of RequestedToLeave and
// the LeaveScore order. LeaveScore alone would not remove them: it is recomputed from
// slash points, and a node whose only fault is its chain config is typically clean, so
// it would leave last among the marked nodes and keep stalling the keygen. They also go
// ahead of a voluntary leaver, who would otherwise take the slot and then never leave,
// because no vault completes while they stay.
//
// When they would take every removal slot that ForcedToLeave leaves free while another
// node is also waiting to leave, the last slot is left to the ordinary order, so an
// offline or bad-actor node, which would fail the keygen too, can still go. With a
// single free slot they keep it. With none (four or fewer Active nodes, where
// findCountToRemove only lets a forced or requested leaver go) the ordinary order is
// left untouched, so a faulty node cannot block a voluntary leaver there. Faulty nodes
// past the limit stay marked and compete on LeaveScore. Callers only look entries up,
// never range over the map.
func (vm *ValidatorMgr) chainSetLeaveFirst(ctx cosmos.Context, active NodeAccounts) map[string]bool {
	first := make(map[string]bool)
	faulty, err := vm.chainSetFaultyNodes(ctx, active)
	if err != nil {
		// An unreadable record is not evidence: leave the ordinary churn order alone.
		ctx.Logger().Error("fail to get chain-set-faulty nodes", "error", err)
		return first
	}
	if len(faulty) == 0 {
		return first
	}
	isFaulty := make(map[string]bool, len(faulty))
	for _, na := range faulty {
		isFaulty[na.NodeAddress.String()] = true
	}
	forced, othersWaiting := 0, false
	for _, na := range active {
		switch {
		case na.ForcedToLeave:
			forced++
		case isFaulty[na.NodeAddress.String()]:
		case na.RequestedToLeave || na.LeaveScore > 0:
			othersWaiting = true
		}
	}

	free := findMaxAbleToLeave(len(active)) - forced
	if free <= 0 {
		return first
	}
	limit := len(faulty)
	if free > 1 && limit >= free && othersWaiting {
		limit = free - 1
	}
	for _, na := range faulty[:limit] {
		first[na.NodeAddress.String()] = true
	}
	return first
}

func (vm *ValidatorMgr) regularVaultMembershipNeedsKeygen(ctx cosmos.Context, active NodeAccounts) (bool, error) {
	asgardSize := vm.k.GetConfigInt64(ctx, constants.AsgardSize)
	if asgardSize <= 0 {
		return false, nil
	}
	expectedGroups := vm.splitNext(ctx, active, asgardSize)
	if len(expectedGroups) == 0 {
		return false, nil
	}

	activeVaults, err := vm.k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return false, fmt.Errorf("fail to get active asgards: %w", err)
	}
	if len(activeVaults) == 0 {
		return false, nil
	}
	regularVaults := make(Vaults, 0, len(activeVaults))
	for _, vault := range activeVaults {
		if vault.HasChain(common.XMRChain) {
			continue
		}
		regularVaults = append(regularVaults, vault)
	}
	if len(regularVaults) != len(expectedGroups) {
		return true, nil
	}

	for _, group := range expectedGroups {
		members := make(common.PubKeys, 0, len(group))
		for _, na := range group {
			members = append(members, na.PubKeySet.Secp256k1)
		}
		found := false
		for _, vault := range regularVaults {
			if vault.MembershipEquals(members) {
				found = true
				break
			}
		}
		if !found {
			return true, nil
		}
	}
	return false, nil
}

func (vm *ValidatorMgr) xmrVaultMembershipNeedsKeygen(ctx cosmos.Context, active NodeAccounts) (bool, error) {
	if len(active) == 0 {
		return false, nil
	}
	allVaults, err := vm.k.GetAsgardVaults(ctx)
	if err != nil {
		return false, fmt.Errorf("fail to get Asgard vaults: %w", err)
	}
	if !shouldMaintainXMRMonovault(allVaults, len(active)) {
		return false, nil
	}

	members := make(common.PubKeys, 0, len(active))
	for _, na := range active {
		members = append(members, na.PubKeySet.Secp256k1)
	}
	for _, vault := range allVaults {
		if (vault.Status == ActiveVault || vault.Status == InitVault) &&
			vault.HasChain(common.XMRChain) && vault.MembershipEquals(members) {
			return false, nil
		}
	}
	return true, nil
}
