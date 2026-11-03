package core

import (
	"fmt"
	rand "math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

////////////////////////////////////////////////////////////////////////////////////////
// ForcedToLeaveKeysignActor
////////////////////////////////////////////////////////////////////////////////////////

// ForcedToLeaveKeysignActor verifies that a forced-to-leave member is filtered
// from the active vault keysign party and that the remaining party can still
// sign an outbound from that vault.
type ForcedToLeaveKeysignActor struct {
	SwapActor

	vaultPubKey common.PubKey
	flagged     forcedToLeaveNode
	banVotes    map[string]bool
	refundTxID  string
}

// NewForcedToLeaveKeysignActor builds the opt-in simulation that proves a banned
// vault member is excluded from the keysign party before exercising a real
// refund outbound from that same vault.
func NewForcedToLeaveKeysignActor(rng *rand.Rand) *Actor {
	a := &ForcedToLeaveKeysignActor{
		SwapActor: SwapActor{
			Actor: *NewActor("ForcedToLeaveKeysign BTC.BTC refund", rng),
			from:  common.BTCAsset,
			to:    common.BTCAsset,
		},
		banVotes: make(map[string]bool),
	}
	a.Timeout = 25 * time.Minute

	a.Ops = append(a.Ops, a.selectActiveVaultMember)
	a.Ops = append(a.Ops, a.forceLeaveSelectedMember)
	a.Ops = append(a.Ops, a.verifyFilteredKeysignParty)
	a.Ops = append(a.Ops, a.acquireRefundUser)
	a.Ops = append(a.Ops, a.sendRefundInbound)
	a.Ops = append(a.Ops, a.verifyRefundOutbound)

	return &a.Actor
}

// selectActiveVaultMember waits for an active BTC vault where removing one
// member still leaves enough signers for the configured TSS threshold.
func (a *ForcedToLeaveKeysignActor) selectActiveVaultMember(config *OpConfig) OpResult {
	vault, vaultPubKey, err := getActiveVaultForChain(common.BTCChain)
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for active BTC vault")
		return OpResult{Continue: false}
	}
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}
	activeValidators := activeValidatorCount(nodes)
	if activeValidators < 3 {
		// With fewer than three active validators, the remaining validators
		// cannot both force-leave one active signer through MsgBan consensus and
		// keep enough vault members for the refund keysign. Skip small topologies
		// explicitly instead of waiting for an impossible removable signer.
		a.Log().Info().
			Int("active_validators", activeValidators).
			Msg("skipping forced-to-leave keysign actor: not enough active validators")
		return OpResult{Finish: true}
	}
	if !vaultRemovalKeepsThreshold(vault) {
		a.Log().Info().
			Int("members", len(vault.Membership)).
			Msg("waiting for multi-node vault that can lose one signer")
		return OpResult{Continue: false}
	}
	usersByAddress, err := nodeUsersByThorAddress(config)
	if err != nil {
		return OpResult{Error: err, Finish: true}
	}

	nodesByPubKey := nodesBySecp256k1(nodes)
	candidates := make([]forcedToLeaveNode, 0, len(vault.Membership))
	for _, member := range vault.Membership {
		node, ok := nodesByPubKey[member]
		if !ok || node.ForcedToLeave || node.Status != atypes.NodeStatus_Active.String() {
			continue
		}
		candidate, err := newForcedToLeaveNode(node)
		if err != nil {
			return OpResult{Error: err, Finish: true}
		}
		if !banCanReachConsensus(nodes, usersByAddress, candidate.node.NodeAddress) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sortForcedToLeaveNodes(candidates)
	if len(candidates) == 0 {
		a.Log().Info().Stringer("vault_pubkey", vaultPubKey).Msg("waiting for removable active vault member")
		return OpResult{Continue: false}
	}

	a.vaultPubKey = vaultPubKey
	a.expectedOutboundVault = vaultPubKey
	a.flagged = candidates[0]
	a.Log().Info().
		Stringer("vault_pubkey", a.vaultPubKey).
		Stringer("flagged_pubkey", a.flagged.pubKey).
		Str("flagged_node", a.flagged.node.NodeAddress).
		Msg("selected active vault member to force leave")
	return OpResult{Continue: true}
}

// forceLeaveSelectedMember drives the normal MsgBan flow until thornode reports
// the selected vault member with forced_to_leave=true.
func (a *ForcedToLeaveKeysignActor) forceLeaveSelectedMember(config *OpConfig) OpResult {
	done, err := broadcastBanUntilForced(config, a.Log(), a.flagged, a.banVotes)
	if err != nil {
		return OpResult{Error: err, Finish: true}
	}
	return OpResult{Continue: done}
}

// verifyFilteredKeysignParty queries the same filtered party Bifrost uses for
// signing and fails immediately if the forced-to-leave member is still present.
func (a *ForcedToLeaveKeysignActor) verifyFilteredKeysignParty(config *OpConfig) OpResult {
	blockHeight, err := config.NodeUsers[0].Thorchain.GetBlockHeight()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thorchain block height")
		return OpResult{Continue: false}
	}
	policyHeight := blockHeight
	policy, err := config.NodeUsers[0].Thorchain.GetKeysignPolicy(a.vaultPubKey, policyHeight)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get filtered keysign party")
		return OpResult{Continue: false}
	}
	party := policy.Signers
	if pubKeysContain(party, a.flagged.pubKey) {
		return OpResult{
			Error:  fmt.Errorf("filtered keysign party still contains forced-to-leave pubkey %s", a.flagged.pubKey),
			Finish: true,
		}
	}
	if len(party) < policy.RequiredSigners {
		return OpResult{
			Error:  fmt.Errorf("filtered keysign party is below threshold: got %d need %d", len(party), policy.RequiredSigners),
			Finish: true,
		}
	}

	a.Log().Info().
		Stringer("vault_pubkey", a.vaultPubKey).
		Stringer("flagged_pubkey", a.flagged.pubKey).
		Int64("policy_height", policyHeight).
		Int("keysign_party_members", len(party)).
		Msg("forced-to-leave member is absent from filtered keysign party")
	return OpResult{Continue: true}
}

// acquireRefundUser locks a BTC-funded user for the invalid-memo refund that
// forces a real outbound signature without depending on trading being enabled.
func (a *ForcedToLeaveKeysignActor) acquireRefundUser(config *OpConfig) OpResult {
	pool, err := thornode.GetPool(common.BTCAsset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get BTC pool")
		return OpResult{Continue: false}
	}

	targetAmount := cosmos.NewUintFromString(pool.BalanceAsset).QuoUint64(1000)
	if targetAmount.IsZero() {
		targetAmount = common.BTCChain.DustThreshold().MulUint64(10)
	}
	return a.acquireUserForAmount(config, targetAmount, true)
}

// sendRefundInbound sends an invalid memo directly to the selected active vault
// so THORNode must create a refund outbound from that same vault.
func (a *ForcedToLeaveKeysignActor) sendRefundInbound(config *OpConfig) OpResult {
	vault, err := thornode.GetVault(a.vaultPubKey.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get active vault")
		return OpResult{Continue: false}
	}
	inboundAddr, ok := vaultChainAddress(vault, common.BTCChain)
	if !ok {
		return OpResult{
			Error:  fmt.Errorf("active vault %s has no BTC address", a.vaultPubKey),
			Finish: true,
		}
	}

	tx := SimTx{
		Chain:     common.BTCChain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(common.BTCAsset, a.swapAmount),
		Memo:      "TEST-FORCED-TO-LEAVE-REFUND",
	}
	txid, err := SignAndBroadcastTx(a.user.ChainClients[common.BTCChain], tx)
	if err != nil {
		if isPostSelectionFundingError(err) {
			return a.finishAfterPostSelectionFundingError(err)
		}
		a.Log().Error().Err(err).Msg("failed to broadcast refund inbound")
		return OpResult{Continue: false}
	}

	a.refundTxID = txid
	a.Log().Info().
		Str("txid", txid).
		Stringer("vault_pubkey", a.vaultPubKey).
		Msg("broadcasted invalid-memo inbound for forced-to-leave refund")
	return OpResult{Continue: true}
}

// verifyRefundOutbound waits for the refund outbound and proves it was produced
// by the vault whose filtered keysign party excluded the forced-to-leave member.
func (a *ForcedToLeaveKeysignActor) verifyRefundOutbound(config *OpConfig) OpResult {
	stages, err := thornode.GetTxStages(a.refundTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get refund tx stages")
		return OpResult{Continue: false}
	}
	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{Continue: false}
	}

	details, err := thornode.GetTxDetails(a.refundTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get refund tx details")
		return OpResult{Continue: false}
	}
	if len(details.OutTxs) != 1 {
		return OpResult{
			Error:  fmt.Errorf("expected exactly one refund out transaction"),
			Finish: true,
		}
	}
	if len(details.Actions) != 1 {
		return OpResult{
			Error:  fmt.Errorf("expected exactly one refund action"),
			Finish: true,
		}
	}

	var verifyErr error
	action := details.Actions[0]
	if action.VaultPubKey == nil {
		verifyErr = fmt.Errorf("expected refund vault %s, got empty vault pubkey", a.vaultPubKey)
	} else if *action.VaultPubKey != a.vaultPubKey.String() {
		verifyErr = fmt.Errorf("expected refund vault %s, got %s", a.vaultPubKey, *action.VaultPubKey)
	}

	actionMemo := ""
	if action.Memo != nil {
		actionMemo = *action.Memo
	} else if action.OriginalMemo != nil {
		actionMemo = *action.OriginalMemo
	}
	if !strings.HasPrefix(actionMemo, "REFUND:") {
		verifyErr = fmt.Errorf("expected refund action memo, got %q", actionMemo)
	}

	a.Log().Info().
		Str("txid", a.refundTxID).
		Stringer("vault_pubkey", a.vaultPubKey).
		Stringer("flagged_pubkey", a.flagged.pubKey).
		Msg("forced-to-leave refund outbound signed")
	a.releaseUserSelection()
	return OpResult{
		Finish: true,
		Error:  verifyErr,
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// ForcedToLeaveKeygenActor
////////////////////////////////////////////////////////////////////////////////////////

// ForcedToLeaveKeygenActor verifies that forced-to-leave nodes are excluded from
// newly generated vaults and that the new vault can still sign an outbound.
type ForcedToLeaveKeygenActor struct {
	SwapActor

	initialVaults map[string]bool
	newVault      common.PubKey
	flagged       forcedToLeaveNode
	banVotes      map[string]bool
	churnStarted  bool
}

// NewForcedToLeaveKeygenActor builds the opt-in simulation that marks a
// would-be keygen participant forced-to-leave before churn and then signs from
// the newly generated vault.
func NewForcedToLeaveKeygenActor(rng *rand.Rand) *Actor {
	a := &ForcedToLeaveKeygenActor{
		SwapActor: SwapActor{
			Actor: *NewActor("ForcedToLeaveKeygen BTC.BTC => ETH.ETH", rng),
			from:  common.BTCAsset,
			to:    common.ETHAsset,
		},
		banVotes: make(map[string]bool),
	}
	a.Timeout = 35 * time.Minute

	a.Ops = append(a.Ops, a.selectKeygenCandidate)
	a.Ops = append(a.Ops, a.forceLeaveKeygenCandidate)
	a.Ops = append(a.Ops, a.triggerChurn)
	a.Ops = append(a.Ops, a.waitForNewVault)
	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.getQuote)
	a.Ops = append(a.Ops, a.sendSwap)
	a.Ops = append(a.Ops, a.verifyOutbound)

	return &a.Actor
}

// selectKeygenCandidate snapshots the current active vaults and finds an
// active or standby node that can be banned without dropping below BFT minimums.
func (a *ForcedToLeaveKeygenActor) selectKeygenCandidate(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vaults")
		return OpResult{Continue: false}
	}
	a.initialVaults = activeVaultPubKeySet(vaults)

	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}
	usersByAddress, err := nodeUsersByThorAddress(config)
	if err != nil {
		return OpResult{Error: err, Finish: true}
	}
	minMembers, err := minimumNodesForBFT()
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to query MinimumNodesForBFT, using default")
		minMembers = 4
	}

	eligible := keygenEligibleNodes(nodes)
	if len(eligible)-1 < minMembers {
		// The default cluster has exactly MinimumNodesForBFT validators and no
		// spare standby. In that topology, excluding one would-be participant
		// would make keygen invalid instead of testing the forced_to_leave filter.
		a.Log().Info().
			Int("eligible_nodes", len(eligible)).
			Int("minimum_members", minMembers).
			Msg("skipping forced-to-leave keygen actor: no spare keygen participant")
		return OpResult{Finish: true}
	}

	candidates := keygenForcedToLeaveCandidates(nodes, usersByAddress, minMembers)
	if len(candidates) == 0 {
		a.Log().Info().
			Int("eligible_nodes", len(eligible)).
			Int("minimum_members", minMembers).
			Msg("waiting for force-leave keygen candidate")
		return OpResult{Continue: false}
	}

	a.flagged = candidates[0]
	a.Log().Info().
		Stringer("flagged_pubkey", a.flagged.pubKey).
		Str("flagged_node", a.flagged.node.NodeAddress).
		Str("status", a.flagged.node.Status).
		Msg("selected would-be keygen participant to force leave")
	return OpResult{Continue: true}
}

// forceLeaveKeygenCandidate drives the normal MsgBan flow for the node selected
// before churn starts.
func (a *ForcedToLeaveKeygenActor) forceLeaveKeygenCandidate(config *OpConfig) OpResult {
	done, err := broadcastBanUntilForced(config, a.Log(), a.flagged, a.banVotes)
	if err != nil {
		return OpResult{Error: err, Finish: true}
	}
	return OpResult{Continue: done}
}

// triggerChurn enables churn with the same Mimir path used by other simulation
// churn actors so keygen is exercised through production handlers.
func (a *ForcedToLeaveKeygenActor) triggerChurn(config *OpConfig) OpResult {
	if a.churnStarted {
		return OpResult{Continue: true}
	}

	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}
	node, accAddr, ok := firstActiveNodeUser(config, nodes, a.flagged.node.NodeAddress)
	if !ok {
		a.Log().Info().Msg("waiting for active node signer to trigger churn")
		return OpResult{Continue: false}
	}
	if !node.Acquire() {
		a.Log().Info().Str("node", accAddr.String()).Msg("waiting for node lock to trigger churn")
		return OpResult{Continue: false}
	}
	defer node.Release()

	for _, item := range []struct {
		key   string
		value int64
	}{
		{key: "NUMBEROFNEWNODESPERCHURN", value: int64(len(nodes))},
		{key: "CHURNRETRYINTERVAL", value: xmrPostChurnRetryInterval},
		{key: "HALTCHURNING", value: 0},
	} {
		mimir := thorchain.NewMsgMimir(item.key, item.value, accAddr)
		txid, err := node.Thorchain.Broadcast(mimir)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("broadcast %s=%d: %w", item.key, item.value, err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid.String()).
			Str("key", item.key).
			Int64("value", item.value).
			Msg("broadcasted churn mimir")
	}

	a.churnStarted = true
	return OpResult{Continue: true}
}

// waitForNewVault waits until a newly active ETH-capable vault appears, verifies
// the forced-to-leave node is absent, and pins the final swap to that vault.
func (a *ForcedToLeaveKeygenActor) waitForNewVault(config *OpConfig) OpResult {
	network, err := thornode.GetNetwork()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get network")
		return OpResult{Continue: false}
	}

	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vaults")
		return OpResult{Continue: false}
	}
	for _, vault := range vaults {
		if vault.Status != thorchain.ActiveVault.String() {
			continue
		}
		vaultPubKey, ok := vaultPubKey(vault)
		if !ok || a.initialVaults[vaultPubKey.String()] {
			continue
		}
		if !vaultHasChain(vault, common.ETHChain) {
			continue
		}
		if stringSliceContains(vault.Membership, a.flagged.pubKey.String()) {
			return OpResult{
				Error:  fmt.Errorf("new vault %s includes forced-to-leave pubkey %s", vaultPubKey, a.flagged.pubKey),
				Finish: true,
			}
		}
		if network.VaultsMigrating {
			a.Log().Info().Stringer("vault_pubkey", vaultPubKey).Msg("waiting for vault migration to complete")
			return OpResult{Continue: false}
		}

		a.newVault = vaultPubKey
		a.expectedOutboundVault = vaultPubKey
		a.Log().Info().
			Stringer("vault_pubkey", a.newVault).
			Stringer("flagged_pubkey", a.flagged.pubKey).
			Int("members", len(vault.Membership)).
			Msg("new active vault excludes forced-to-leave pubkey")
		return OpResult{Continue: true}
	}

	a.Log().Info().Stringer("flagged_pubkey", a.flagged.pubKey).Msg("waiting for new active vault")
	return OpResult{Continue: false}
}

////////////////////////////////////////////////////////////////////////////////////////
// Shared helpers
////////////////////////////////////////////////////////////////////////////////////////

type forcedToLeaveNode struct {
	node    openapi.Node
	pubKey  common.PubKey
	address cosmos.AccAddress
}

// newForcedToLeaveNode validates and normalizes the node identity fields used by
// the ban message and vault membership checks.
func newForcedToLeaveNode(node openapi.Node) (forcedToLeaveNode, error) {
	pubKey := nodeSecp256k1(node)
	if pubKey.IsEmpty() {
		return forcedToLeaveNode{}, fmt.Errorf("node %s has empty secp256k1 pubkey", node.NodeAddress)
	}
	addr, err := cosmos.AccAddressFromBech32(node.NodeAddress)
	if err != nil {
		return forcedToLeaveNode{}, fmt.Errorf("invalid node address %s: %w", node.NodeAddress, err)
	}
	return forcedToLeaveNode{
		node:    node,
		pubKey:  pubKey,
		address: addr,
	}, nil
}

// getActiveVaultForChain resolves the current inbound vault for a chain and
// returns only after it is an active vault.
func getActiveVaultForChain(chain common.Chain) (openapi.Vault, common.PubKey, error) {
	pubKey, err := thornode.GetInboundAddressPubKey(chain)
	if err != nil {
		return openapi.Vault{}, common.EmptyPubKey, err
	}
	vault, err := thornode.GetVault(pubKey.String())
	if err != nil {
		return openapi.Vault{}, common.EmptyPubKey, err
	}
	if vault.Status != thorchain.ActiveVault.String() {
		return openapi.Vault{}, common.EmptyPubKey, fmt.Errorf("vault %s status is %s", pubKey, vault.Status)
	}
	return vault, pubKey, nil
}

// vaultPubKey extracts the secp256k1 vault pubkey from an OpenAPI vault record.
func vaultPubKey(vault openapi.Vault) (common.PubKey, bool) {
	if vault.PubKey == nil || *vault.PubKey == "" {
		return common.EmptyPubKey, false
	}
	return common.PubKey(*vault.PubKey), true
}

// vaultHasChain checks both chain metadata and address metadata because vault
// responses can expose chain support in either field depending on endpoint state.
func vaultHasChain(vault openapi.Vault, chain common.Chain) bool {
	for _, item := range vault.Chains {
		if item == chain.String() {
			return true
		}
	}
	for _, address := range vault.Addresses {
		if address.Chain == chain.String() {
			return true
		}
	}
	return false
}

// vaultChainAddress returns the vault address for the requested chain.
func vaultChainAddress(vault openapi.Vault, chain common.Chain) (common.Address, bool) {
	for _, address := range vault.Addresses {
		if address.Chain == chain.String() && address.Address != "" {
			return common.Address(address.Address), true
		}
	}
	return common.NoAddress, false
}

// vaultRemovalKeepsThreshold checks that excluding one member still leaves more
// signers than the TSS threshold required for the original vault size.
func vaultRemovalKeepsThreshold(vault openapi.Vault) bool {
	members := len(vault.Membership)
	return members > 1 && members-1 > tssThreshold(members)
}

// tssThreshold mirrors the threshold formula used by the TSS key manager.
func tssThreshold(participants int) int {
	if participants <= 0 {
		return 0
	}
	return (participants*2+2)/3 - 1
}

// requiredSuperMajority returns the number of active validators required for a
// MsgBan voter to reach chain consensus.
func requiredSuperMajority(total int) int {
	if total <= 0 {
		return 0
	}
	required := total * 2 / 3
	if (total*2)%3 > 0 {
		required++
	}
	return required
}

// activeValidatorCount is used by forced-to-leave actors to distinguish a
// topology that can exercise forced signer exclusion from the single-node
// simulation, where the actor should skip explicitly.
func activeValidatorCount(nodes []openapi.Node) int {
	active := 0
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			active++
		}
	}
	return active
}

// nodeSecp256k1 extracts the node secp256k1 pubkey used in vault membership.
func nodeSecp256k1(node openapi.Node) common.PubKey {
	if node.PubKeySet.Secp256k1 == nil {
		return common.EmptyPubKey
	}
	return common.PubKey(*node.PubKeySet.Secp256k1)
}

// nodesBySecp256k1 indexes node account records by the vault membership pubkey.
func nodesBySecp256k1(nodes []openapi.Node) map[string]openapi.Node {
	out := make(map[string]openapi.Node, len(nodes))
	for _, node := range nodes {
		pubKey := nodeSecp256k1(node)
		if pubKey.IsEmpty() {
			continue
		}
		out[pubKey.String()] = node
	}
	return out
}

// nodeUsersByThorAddress maps simulation validator clients to their thornode
// addresses so MsgBan votes are sent by the matching local key.
func nodeUsersByThorAddress(config *OpConfig) (map[string]*User, error) {
	out := make(map[string]*User, len(config.NodeUsers))
	for _, user := range config.NodeUsers {
		addr, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return nil, fmt.Errorf("failed to get node thor address: %w", err)
		}
		out[addr.String()] = user
	}
	return out, nil
}

// banCanReachConsensus verifies enough active validator clients are available to
// pass MsgBan consensus without counting the target node as a voter.
func banCanReachConsensus(nodes []openapi.Node, usersByAddress map[string]*User, targetAddress string) bool {
	activeTotal := 0
	availableSigners := 0
	for _, node := range nodes {
		if node.Status != atypes.NodeStatus_Active.String() {
			continue
		}
		activeTotal++
		if node.NodeAddress == targetAddress {
			continue
		}
		if usersByAddress[node.NodeAddress] != nil {
			availableSigners++
		}
	}
	return activeTotal > 0 && availableSigners >= requiredSuperMajority(activeTotal)
}

// broadcastBanUntilForced sends MsgBan votes from active simulation validators
// and returns true once the chain reports the target as forced_to_leave.
func broadcastBanUntilForced(config *OpConfig, log *zerolog.Logger, target forcedToLeaveNode, sent map[string]bool) (bool, error) {
	nodes, err := thornode.GetNodes()
	if err != nil {
		log.Error().Err(err).Msg("failed to get nodes")
		return false, nil
	}
	current, ok := findNodeByAddress(nodes, target.node.NodeAddress)
	if !ok {
		return false, fmt.Errorf("forced-to-leave target node %s not found", target.node.NodeAddress)
	}
	if current.ForcedToLeave {
		log.Info().
			Stringer("flagged_pubkey", target.pubKey).
			Str("flagged_node", target.node.NodeAddress).
			Msg("node is forced to leave")
		return true, nil
	}

	usersByAddress, err := nodeUsersByThorAddress(config)
	if err != nil {
		return false, err
	}
	if !banCanReachConsensus(nodes, usersByAddress, target.node.NodeAddress) {
		return false, fmt.Errorf("not enough active simulation node users to ban %s", target.node.NodeAddress)
	}

	activeTotal := 0
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeTotal++
		}
	}
	required := requiredSuperMajority(activeTotal)

	sentCount := 0
	for _, node := range nodes {
		if node.Status != atypes.NodeStatus_Active.String() || node.NodeAddress == target.node.NodeAddress {
			continue
		}
		if sent[node.NodeAddress] {
			sentCount++
			continue
		}
		user := usersByAddress[node.NodeAddress]
		if user == nil {
			continue
		}
		if !user.Acquire() {
			log.Info().Str("node", node.NodeAddress).Msg("waiting for node lock to broadcast ban")
			continue
		}
		signer, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			user.Release()
			return false, fmt.Errorf("failed to get signer address: %w", err)
		}
		msg := thorchain.NewMsgBan(target.address, signer)
		txid, err := user.Thorchain.Broadcast(msg)
		user.Release()
		if err != nil {
			return false, fmt.Errorf("broadcast ban for %s from %s: %w", target.node.NodeAddress, node.NodeAddress, err)
		}
		sent[node.NodeAddress] = true
		sentCount++
		log.Info().
			Str("txid", txid.String()).
			Str("signer", node.NodeAddress).
			Str("target", target.node.NodeAddress).
			Msg("broadcasted ban vote")
	}
	if sentCount < required {
		log.Info().
			Int("sent", sentCount).
			Int("required", required).
			Msg("waiting for enough ban votes")
		return false, nil
	}

	return false, nil
}

// findNodeByAddress returns the current node account for a thornode address.
func findNodeByAddress(nodes []openapi.Node, address string) (openapi.Node, bool) {
	for _, node := range nodes {
		if node.NodeAddress == address {
			return node, true
		}
	}
	return openapi.Node{}, false
}

// sortForcedToLeaveNodes gives deterministic candidate selection across
// simulation runs with the same chain state.
func sortForcedToLeaveNodes(nodes []forcedToLeaveNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].node.Status != nodes[j].node.Status {
			return nodes[i].node.Status > nodes[j].node.Status
		}
		return nodes[i].node.NodeAddress < nodes[j].node.NodeAddress
	})
}

// keygenForcedToLeaveCandidates returns active or standby nodes that can be
// excluded from keygen while leaving enough non-forced participants for churn.
func keygenForcedToLeaveCandidates(nodes []openapi.Node, usersByAddress map[string]*User, minMembers int) []forcedToLeaveNode {
	eligible := keygenEligibleNodes(nodes)
	candidates := make([]forcedToLeaveNode, 0, len(eligible))
	for _, node := range eligible {
		if len(eligible)-1 < minMembers {
			continue
		}
		if !banCanReachConsensus(nodes, usersByAddress, node.NodeAddress) {
			continue
		}
		candidate, err := newForcedToLeaveNode(node)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		// Prefer standby nodes because they are the clearest would-be new keygen
		// participants when the churn actor expands the active set.
		leftStandby := candidates[i].node.Status == atypes.NodeStatus_Standby.String()
		rightStandby := candidates[j].node.Status == atypes.NodeStatus_Standby.String()
		if leftStandby != rightStandby {
			return leftStandby
		}
		return candidates[i].node.NodeAddress < candidates[j].node.NodeAddress
	})
	return candidates
}

// keygenEligibleNodes returns the node accounts that would be considered for a
// new keygen before applying the forced_to_leave exclusion.
func keygenEligibleNodes(nodes []openapi.Node) []openapi.Node {
	eligible := make([]openapi.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.ForcedToLeave {
			continue
		}
		if node.Status != atypes.NodeStatus_Active.String() && node.Status != atypes.NodeStatus_Standby.String() {
			continue
		}
		if nodeSecp256k1(node).IsEmpty() {
			continue
		}
		eligible = append(eligible, node)
	}
	return eligible
}

// activeVaultPubKeySet records the active vault pubkeys before churn so the
// actor can distinguish newly generated vaults later.
func activeVaultPubKeySet(vaults []openapi.Vault) map[string]bool {
	out := make(map[string]bool)
	for _, vault := range vaults {
		if vault.Status != thorchain.ActiveVault.String() {
			continue
		}
		pubKey, ok := vaultPubKey(vault)
		if !ok {
			continue
		}
		out[pubKey.String()] = true
	}
	return out
}

// firstActiveNodeUser finds an unlocked active validator client to submit Mimir
// updates, skipping the node that was just forced to leave.
func firstActiveNodeUser(config *OpConfig, nodes []openapi.Node, excludeAddress string) (*User, cosmos.AccAddress, bool) {
	usersByAddress, err := nodeUsersByThorAddress(config)
	if err != nil {
		return nil, nil, false
	}
	for _, node := range nodes {
		if node.Status != atypes.NodeStatus_Active.String() || node.ForcedToLeave || node.NodeAddress == excludeAddress {
			continue
		}
		user := usersByAddress[node.NodeAddress]
		if user == nil {
			continue
		}
		addr, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			continue
		}
		return user, addr, true
	}
	return nil, nil, false
}

// minimumNodesForBFT reads the live chain constant so the actor follows the
// current simulation configuration instead of hard-coding the BFT floor.
func minimumNodesForBFT() (int, error) {
	var constants struct {
		Int64Values map[string]int64 `json:"int_64_values"`
	}
	if err := thornode.Get(fmt.Sprintf("%s/thorchain/constants", thornode.BaseURL()), &constants); err != nil {
		return 0, err
	}
	value := constants.Int64Values["MinimumNodesForBFT"]
	if value <= 0 {
		return 0, fmt.Errorf("MinimumNodesForBFT is unset")
	}
	return int(value), nil
}

// stringSliceContains reports whether a string appears in a slice.
func stringSliceContains(items []string, item string) bool {
	for _, existing := range items {
		if existing == item {
			return true
		}
	}
	return false
}

// pubKeysContain reports whether a pubkey appears in a pubkey slice.
func pubKeysContain(items common.PubKeys, item common.PubKey) bool {
	for _, existing := range items {
		if existing.Equals(item) {
			return true
		}
	}
	return false
}
