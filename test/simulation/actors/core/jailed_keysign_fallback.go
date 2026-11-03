package core

import (
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/api/types"
	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

const (
	jailedKeysignFallbackReason = "simulation jailed keysign fallback"
)

////////////////////////////////////////////////////////////////////////////////////////
// JailedKeysignFallbackActor
////////////////////////////////////////////////////////////////////////////////////////

// JailedKeysignFallbackActor verifies that jailed vault members are excluded
// when enough clean signers remain, then promoted as fallback signers only when
// the original vault threshold would otherwise be missed.
type JailedKeysignFallbackActor struct {
	SwapActor

	vaultPubKey              common.PubKey
	memberCount              int
	expectPreferredExclusion bool
	targets                  []forcedToLeaveNode
	voters                   []forcedToLeaveNode
	jailVotes                map[string]map[string]bool
	jailHeights              map[string]int64
	refundTxID               string
}

// NewJailedKeysignFallbackActor builds an opt-in simulation that proves jailed
// vault members are fallback-only until the clean party drops below threshold.
func NewJailedKeysignFallbackActor(rng *rand.Rand) *Actor {
	a := &JailedKeysignFallbackActor{
		SwapActor: SwapActor{
			Actor: *NewActor("JailedKeysignFallback BTC.BTC refund", rng),
			from:  common.BTCAsset,
			to:    common.BTCAsset,
		},
		jailVotes:   make(map[string]map[string]bool),
		jailHeights: make(map[string]int64),
	}
	a.Timeout = 35 * time.Minute

	a.Ops = append(a.Ops, a.selectJailedFallbackVault)
	a.Ops = append(a.Ops, a.jailPreferredExcludedMember)
	a.Ops = append(a.Ops, a.verifyJailedPreferredExclusion)
	a.Ops = append(a.Ops, a.jailFallbackPromotionMembers)
	a.Ops = append(a.Ops, a.verifyJailedFallbackPromotion)
	a.Ops = append(a.Ops, a.acquireRefundUser)
	a.Ops = append(a.Ops, a.sendRefundInbound)
	a.Ops = append(a.Ops, a.verifyRefundOutbound)

	return &a.Actor
}

// selectJailedFallbackVault chooses an active BTC vault with enough simulation
// node users to vote synthetic keysign failures through normal chain handlers.
func (a *JailedKeysignFallbackActor) selectJailedFallbackVault(config *OpConfig) OpResult {
	vault, vaultPubKey, err := getActiveVaultForChain(common.BTCChain)
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for active BTC vault")
		return OpResult{Continue: false}
	}

	blockHeight, err := config.NodeUsers[0].Thorchain.GetBlockHeight()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thorchain block height")
		return OpResult{Continue: false}
	}
	policyHeight := blockHeight
	policy, err := config.NodeUsers[0].Thorchain.GetKeysignPolicy(vaultPubKey, policyHeight)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get keysign policy")
		return OpResult{Continue: false}
	}
	setup, skipReason := jailedFallbackVaultSetupFromPolicy(vault, policy)
	if skipReason != "" {
		a.Log().Info().
			Stringer("vault_pubkey", vaultPubKey).
			Int64("policy_height", policyHeight).
			Int("members", setup.memberCount).
			Int("required_signers", setup.requiredSigners).
			Int("active_signers", len(policy.Signers)).
			Int("never_signers", len(policy.NeverSigners)).
			Int("jailed_fallback_signers", len(policy.JailedFallbackSigners)).
			Str("reason", skipReason).
			Msg("skipping jailed keysign fallback actor: active vault cannot exercise clean fallback scenario")
		return OpResult{Finish: true}
	}

	members := setup.memberCount
	requiredSigners := setup.requiredSigners
	jailCount := setup.jailCount
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}
	usersByAddress, err := nodeUsersByThorAddress(config)
	if err != nil {
		return OpResult{Error: err, Finish: true}
	}

	candidates, voters := jailableVaultMembers(vault, nodes, usersByAddress, blockHeight)
	if len(candidates) < jailCount {
		a.Log().Info().
			Int("candidates", len(candidates)).
			Int("required_jails", jailCount).
			Msg("waiting for enough jailable vault members")
		return OpResult{Continue: false}
	}
	if !keysignFailureVotesCanReachConsensus(voters, candidates[:jailCount], members) {
		a.Log().Info().
			Int("voters", len(voters)).
			Int("members", members).
			Msg("waiting for enough vault member users to vote keysign failure")
		return OpResult{Continue: false}
	}

	a.vaultPubKey = vaultPubKey
	a.memberCount = members
	a.expectPreferredExclusion = members-1 >= requiredSigners
	a.expectedOutboundVault = vaultPubKey
	a.targets = candidates[:jailCount]
	a.voters = voters
	a.Log().Info().
		Stringer("vault_pubkey", a.vaultPubKey).
		Int("members", members).
		Int("required_signers", requiredSigners).
		Int("clean_candidates", len(candidates)).
		Int("targets", len(a.targets)).
		Bool("expect_preferred_exclusion", a.expectPreferredExclusion).
		Msg("selected vault for jailed signer fallback simulation")
	return OpResult{Continue: true}
}

type jailedFallbackVaultSetup struct {
	memberCount     int
	requiredSigners int
	jailCount       int
}

func jailedFallbackVaultSetupFromPolicy(vault openapi.Vault, policy thorclient.KeysignPolicy) (jailedFallbackVaultSetup, string) {
	setup := jailedFallbackVaultSetup{
		memberCount:     len(vault.Membership),
		requiredSigners: policy.RequiredSigners,
	}
	setup.jailCount = setup.memberCount - setup.requiredSigners + 1

	if setup.memberCount < 4 || setup.jailCount < 2 {
		return setup, "vault cannot exercise fallback promotion"
	}
	if setup.requiredSigners <= 0 {
		return setup, "keysign policy has no required signer threshold"
	}
	if len(policy.NeverSigners) != 0 {
		return setup, "vault already has never-signers"
	}
	if len(policy.JailedFallbackSigners) != 0 {
		return setup, "vault already has jailed fallback signers"
	}
	if len(policy.Signers) != setup.memberCount {
		return setup, "vault keysign party is already filtered"
	}
	if len(policy.Signers)-1 < setup.requiredSigners {
		return setup, "one jailed member would put clean signers below threshold"
	}
	return setup, ""
}

// jailPreferredExcludedMember creates the first jailed member, which should
// remain excluded while clean signers can still satisfy threshold.
func (a *JailedKeysignFallbackActor) jailPreferredExcludedMember(config *OpConfig) OpResult {
	done, err := broadcastKeysignFailureUntilJailed(config, a.Log(), a.vaultPubKey, a.targets[0], a.voters, a.memberCount, a.jailVotes, a.jailHeights, common.BTCAsset)
	if err != nil {
		return OpResult{Error: err, Finish: true}
	}
	return OpResult{Continue: done}
}

// verifyJailedPreferredExclusion proves the first jailed member is only a
// fallback candidate while clean members can still satisfy the original quorum.
func (a *JailedKeysignFallbackActor) verifyJailedPreferredExclusion(config *OpConfig) OpResult {
	policy, policyHeight, err := keysignPolicyForSimulation(config, a.vaultPubKey)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get keysign policy")
		return OpResult{Continue: false}
	}
	target := a.targets[0].pubKey
	if !pubKeysContain(policy.JailedFallbackSigners, target) {
		a.Log().Info().
			Stringer("target", target).
			Int64("policy_height", policyHeight).
			Msg("waiting for jailed signer to appear as fallback")
		return OpResult{Continue: false}
	}
	if pubKeysContain(policy.Signers, target) {
		if !a.expectPreferredExclusion {
			a.Log().Info().
				Stringer("vault_pubkey", a.vaultPubKey).
				Stringer("jailed_pubkey", target).
				Int64("policy_height", policyHeight).
				Int("signers", len(policy.Signers)).
				Int("required_signers", policy.RequiredSigners).
				Msg("jailed member promoted immediately because clean signers are at threshold")
			return OpResult{Continue: true}
		}
		return OpResult{
			Error:  fmt.Errorf("jailed signer %s was selected while clean signers satisfy threshold", target),
			Finish: true,
		}
	}
	if len(policy.Signers) < policy.RequiredSigners {
		return OpResult{
			Error:  fmt.Errorf("preferred signer policy below threshold: got %d need %d", len(policy.Signers), policy.RequiredSigners),
			Finish: true,
		}
	}

	a.Log().Info().
		Stringer("vault_pubkey", a.vaultPubKey).
		Stringer("jailed_pubkey", target).
		Int64("policy_height", policyHeight).
		Int("signers", len(policy.Signers)).
		Int("required_signers", policy.RequiredSigners).
		Msg("jailed member is excluded while clean signers satisfy threshold")
	return OpResult{Continue: true}
}

// jailFallbackPromotionMembers jails enough additional members that the policy
// must promote deterministic jailed fallback signers to reach threshold.
func (a *JailedKeysignFallbackActor) jailFallbackPromotionMembers(config *OpConfig) OpResult {
	for _, target := range a.targets[1:] {
		done, err := broadcastKeysignFailureUntilJailed(config, a.Log(), a.vaultPubKey, target, a.voters, a.memberCount, a.jailVotes, a.jailHeights, common.BTCAsset)
		if err != nil {
			return OpResult{Error: err, Finish: true}
		}
		if !done {
			return OpResult{Continue: false}
		}
	}
	return OpResult{Continue: true}
}

// verifyJailedFallbackPromotion proves thornode promotes deterministic jailed
// fallback signers once clean members alone are below the original threshold.
func (a *JailedKeysignFallbackActor) verifyJailedFallbackPromotion(config *OpConfig) OpResult {
	policy, policyHeight, err := keysignPolicyForSimulation(config, a.vaultPubKey)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get keysign policy")
		return OpResult{Continue: false}
	}

	promoted := 0
	for _, target := range a.targets {
		if !pubKeysContain(policy.JailedFallbackSigners, target.pubKey) {
			a.Log().Info().
				Stringer("target", target.pubKey).
				Int64("policy_height", policyHeight).
				Msg("waiting for jailed signer to appear as fallback")
			return OpResult{Continue: false}
		}
		if pubKeysContain(policy.Signers, target.pubKey) {
			promoted++
		}
	}
	if len(policy.Signers) < policy.RequiredSigners {
		return OpResult{
			Error:  fmt.Errorf("fallback signer policy below threshold: got %d need %d", len(policy.Signers), policy.RequiredSigners),
			Finish: true,
		}
	}
	if promoted == 0 {
		return OpResult{
			Error:  fmt.Errorf("no jailed fallback signer was promoted into active signer set"),
			Finish: true,
		}
	}

	a.Log().Info().
		Stringer("vault_pubkey", a.vaultPubKey).
		Int64("policy_height", policyHeight).
		Int("jailed_targets", len(a.targets)).
		Int("promoted_fallbacks", promoted).
		Int("signers", len(policy.Signers)).
		Int("required_signers", policy.RequiredSigners).
		Msg("jailed fallback signer promoted to satisfy threshold")
	return OpResult{Continue: true}
}

// acquireRefundUser locks a BTC-funded user for the invalid-memo refund that
// forces a real outbound signature by the selected vault.
func (a *JailedKeysignFallbackActor) acquireRefundUser(config *OpConfig) OpResult {
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

// sendRefundInbound sends an invalid memo to the selected vault so the fallback
// signer policy is exercised by a real refund outbound.
func (a *JailedKeysignFallbackActor) sendRefundInbound(config *OpConfig) OpResult {
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
		Memo:      "TEST-JAILED-FALLBACK-REFUND",
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
		Msg("broadcasted invalid-memo inbound for jailed fallback refund")
	return OpResult{Continue: true}
}

// verifyRefundOutbound waits for the refund outbound and confirms the selected
// vault completed signing after fallback promotion.
func (a *JailedKeysignFallbackActor) verifyRefundOutbound(config *OpConfig) OpResult {
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
		Msg("jailed fallback refund outbound signed")
	a.releaseUserSelection()
	return OpResult{
		Finish: true,
		Error:  verifyErr,
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// jailableVaultMembers splits active vault members into jail candidates and
// voters that can submit synthetic keysign-failure observations.
func jailableVaultMembers(vault openapi.Vault, nodes []openapi.Node, usersByAddress map[string]*User, height int64) ([]forcedToLeaveNode, []forcedToLeaveNode) {
	nodesByPubKey := nodesBySecp256k1(nodes)
	candidates := make([]forcedToLeaveNode, 0, len(vault.Membership))
	voters := make([]forcedToLeaveNode, 0, len(vault.Membership))
	for _, member := range vault.Membership {
		node, ok := nodesByPubKey[member]
		if !ok || node.ForcedToLeave || node.Status != types.NodeStatus_Active.String() {
			continue
		}
		if usersByAddress[node.NodeAddress] == nil {
			continue
		}
		item, err := newForcedToLeaveNode(node)
		if err != nil {
			continue
		}
		voters = append(voters, item)
		if nodeIsJailedAt(node, height) {
			continue
		}
		candidates = append(candidates, item)
	}
	sortForcedToLeaveNodes(candidates)
	sortForcedToLeaveNodes(voters)
	return candidates, voters
}

// keysignFailureVotesCanReachConsensus checks every target can receive a simple
// majority from the remaining vault members.
func keysignFailureVotesCanReachConsensus(voters, targets []forcedToLeaveNode, memberCount int) bool {
	for _, target := range targets {
		count := 0
		for _, voter := range voters {
			if voter.node.NodeAddress != target.node.NodeAddress {
				count++
			}
		}
		if !thorchain.HasSimpleMajority(count, memberCount) {
			return false
		}
	}
	return true
}

// broadcastKeysignFailureUntilJailed submits synthetic keysign-failure reports
// until thornode jails the target member.
func broadcastKeysignFailureUntilJailed(
	config *OpConfig,
	log *zerolog.Logger,
	vaultPubKey common.PubKey,
	target forcedToLeaveNode,
	voters []forcedToLeaveNode,
	memberCount int,
	sent map[string]map[string]bool,
	failureHeights map[string]int64,
	asset common.Asset,
) (bool, error) {
	blockHeight, err := config.NodeUsers[0].Thorchain.GetBlockHeight()
	if err != nil {
		log.Error().Err(err).Msg("failed to get thorchain block height")
		return false, nil
	}
	nodes, err := thornode.GetNodes()
	if err != nil {
		log.Error().Err(err).Msg("failed to get nodes")
		return false, nil
	}
	current, ok := findNodeByAddress(nodes, target.node.NodeAddress)
	if !ok {
		return false, fmt.Errorf("jailed keysign target node %s not found", target.node.NodeAddress)
	}
	if nodeIsJailedAt(current, blockHeight) {
		log.Info().
			Stringer("target", target.pubKey).
			Str("node", target.node.NodeAddress).
			Msg("target node is jailed")
		return true, nil
	}

	targetKey := target.pubKey.String()
	if sent[targetKey] == nil {
		sent[targetKey] = make(map[string]bool)
	}
	failureHeight := failureHeights[targetKey]
	if failureHeight <= 0 {
		failureHeight = blockHeight
		failureHeights[targetKey] = failureHeight
	}
	usersByAddress, err := nodeUsersByThorAddress(config)
	if err != nil {
		return false, err
	}

	sentCount := 0
	for _, voter := range voters {
		if voter.node.NodeAddress == target.node.NodeAddress {
			continue
		}
		if sent[targetKey][voter.node.NodeAddress] {
			sentCount++
			continue
		}
		user := usersByAddress[voter.node.NodeAddress]
		if user == nil {
			continue
		}
		if !user.Acquire() {
			log.Info().Str("node", voter.node.NodeAddress).Msg("waiting for node lock to broadcast keysign failure")
			continue
		}
		blame := thorchain.Blame{
			FailReason: jailedKeysignFallbackReason,
			Round:      tssMessages.KEYSIGN1aUnicast,
			BlameNodes: []thorchain.Node{
				{Pubkey: target.pubKey.String()},
			},
		}
		memo := "SIM-JAILED-FALLBACK:" + target.pubKey.String()
		coins := common.Coins{common.NewCoin(asset, cosmos.NewUint(1))}
		txid, err := user.Thorchain.PostKeysignFailure(blame, failureHeight, memo, coins, vaultPubKey)
		user.Release()
		if err != nil {
			return false, fmt.Errorf("broadcast keysign failure for %s from %s: %w", target.node.NodeAddress, voter.node.NodeAddress, err)
		}
		sent[targetKey][voter.node.NodeAddress] = true
		sentCount++
		log.Info().
			Str("txid", txid.String()).
			Str("signer", voter.node.NodeAddress).
			Str("target", target.node.NodeAddress).
			Msg("broadcasted synthetic keysign failure")
	}

	if !thorchain.HasSimpleMajority(sentCount, memberCount) {
		log.Info().
			Int("sent", sentCount).
			Int("members", memberCount).
			Msg("waiting for enough keysign failure votes")
	}
	return false, nil
}

// keysignPolicyForSimulation queries the same policy height bifrost uses when
// signing in the simulation.
func keysignPolicyForSimulation(config *OpConfig, vaultPubKey common.PubKey) (policy thorclient.KeysignPolicy, policyHeight int64, err error) {
	blockHeight, err := config.NodeUsers[0].Thorchain.GetBlockHeight()
	if err != nil {
		return thorclient.KeysignPolicy{}, 0, err
	}
	policyHeight = blockHeight
	policy, err = config.NodeUsers[0].Thorchain.GetKeysignPolicy(vaultPubKey, policyHeight)
	return policy, policyHeight, err
}

// nodeIsJailedAt reports whether the OpenAPI node jail release height still
// exceeds the policy height under test.
func nodeIsJailedAt(node openapi.Node, height int64) bool {
	releaseHeight, ok := node.Jail.GetReleaseHeightOk()
	return ok && releaseHeight != nil && *releaseHeight > height
}

// NewXMRHistoricalSignerGate excludes one active member from a retired four-member
// vault's signing party using ordinary mocknet blame votes. Its scanner stays on.
// A longer mocknet jail is required so it spans deposit confirmation and signing.
func NewXMRHistoricalSignerGate(vault openapi.Vault) Op {
	var target *forcedToLeaveNode
	var voters []forcedToLeaveNode
	sent := make(map[string]map[string]bool)
	heights := make(map[string]int64)
	return func(config *OpConfig) OpResult {
		if len(config.NodeUsers) < 5 {
			return OpResult{Finish: true, Error: fmt.Errorf("historical XMR recovery requires five running validators")}
		}
		nodes, err := thornode.GetNodes()
		if err != nil {
			return OpResult{Error: err}
		}
		height, err := config.NodeUsers[0].Thorchain.GetBlockHeight()
		if err != nil {
			return OpResult{Error: err}
		}
		if target == nil {
			users, userErr := nodeUsersByThorAddress(config)
			if userErr != nil {
				return OpResult{Error: userErr}
			}
			candidates, activeVoters := jailableVaultMembers(vault, nodes, users, height)
			if len(vault.Membership) != 4 || len(activeVoters) != 3 || len(candidates) == 0 {
				return OpResult{Finish: true, Error: fmt.Errorf("historical XMR gate requires a four-member vault with exactly three active members")}
			}
			if !keysignFailureVotesCanReachConsensus(activeVoters, candidates[:1], len(vault.Membership)) {
				return OpResult{Finish: true, Error: fmt.Errorf("not enough active vault members to jail the test signer")}
			}
			target, voters = &candidates[0], activeVoters
		}
		logger := log.With().Str("scenario", "xmr-historical-refund").Logger()
		done, err := broadcastKeysignFailureUntilJailed(config, &logger, common.PubKey(vault.GetPubKeyFrost()), *target, voters, len(vault.Membership), sent, heights, common.XMRAsset)
		if err != nil || !done {
			return OpResult{Continue: false, Error: err}
		}
		current, ok := findNodeByAddress(nodes, target.node.NodeAddress)
		if !ok || current.Jail.GetReleaseHeight()-height < 120 {
			return OpResult{Finish: true, Error: fmt.Errorf("historical XMR recovery needs at least 120 jail blocks remaining; start the mocknet with JAIL_TIME_KEYSIGN=600")}
		}
		logger.Info().Stringer("excluded_active_member", target.pubKey).Strs("vault_members", vault.Membership).Msg("XMR recovery signing gate ready; scanners remain running")
		return OpResult{Continue: true}
	}
}
