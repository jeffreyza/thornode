package core

import (
	"errors"
	"fmt"
	"math/big"
	rand "math/rand/v2"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

type XMRPostChurnActor struct {
	SwapActor

	inboundBefore   common.Address
	inboundAfter    common.Address
	requireExisting bool
	captureActor    *Actor
}

const (
	xmrPostChurnNewNodesPerChurn = int64(3)
	xmrPostChurnLeadBlocks       = int64(10)
	// Keep this above the 300s FROST keygen timeout, but low enough that a
	// missed local keygen gets a retry before the actor deadline. This matches
	// the mocknet ChurnRetryInterval override; 360 would also clear the timeout,
	// but using one value avoids a misleading reviewer-only delta.
	xmrPostChurnRetryInterval = int64(365)
)

func NewXMRPostChurnActor(rng *rand.Rand) *Actor {
	a := newXMRPostChurnActor(rng)
	a.Ops = append([]Op{a.captureActiveInbound, a.startChurn}, a.Ops...)
	return &a.Actor
}

// NewXMRChurnOutChecks snapshots the current vault before a caller's churn-out
// and verifies that rotation afterward, without initiating another churn.
func NewXMRChurnOutChecks(rng *rand.Rand) (before, after *Actor) {
	a := newXMRPostChurnActor(rng)
	a.requireExisting = true
	before = NewActor("XMRPreChurnOut", rng)
	before.Timeout = a.Timeout
	a.captureActor = before
	before.Ops = []Op{a.captureActiveInbound}
	return before, &a.Actor
}

func newXMRPostChurnActor(rng *rand.Rand) *XMRPostChurnActor {
	a := &XMRPostChurnActor{
		SwapActor: SwapActor{
			Actor:           *NewActor("XMRPostChurn BTC.BTC => XMR.XMR", rng),
			from:            common.BTCAsset,
			to:              common.XMRAsset,
			requireOutbound: true,
		},
	}
	a.Timeout = 35 * time.Minute

	a.Ops = append(a.Ops, a.waitForVaultRotation)
	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.getQuote)
	a.Ops = append(a.Ops, a.sendSwap)
	a.Ops = append(a.Ops, a.verifyOutbound)

	return a
}

func (a *XMRPostChurnActor) captureActiveInbound(_ *OpConfig) OpResult {
	return a.captureInbound(func(chain common.Chain) (common.Address, *common.Address, error) {
		address, router, err := thornode.GetInboundAddress(chain)
		if err != nil || !a.requireExisting {
			return address, router, err
		}
		nodes, err := thornode.GetNodes()
		if err != nil {
			return common.NoAddress, nil, err
		}
		vaults, err := thornode.GetVaults()
		if err != nil {
			return common.NoAddress, nil, err
		}
		if !xmrVaultMatchesActiveNodes(vaults, address, nodes) {
			return common.NoAddress, nil, fmt.Errorf("waiting for XMR vault membership to match active validators before churn-out")
		}
		return address, router, nil
	})
}

func (a *XMRPostChurnActor) captureInbound(get func(common.Chain) (common.Address, *common.Address, error)) OpResult {
	logger := a.Log()
	if a.captureActor != nil {
		logger = a.captureActor.Log()
	}
	inboundAddress, _, err := get(common.XMRChain)
	if err != nil && !errors.Is(err, thornode.ErrInboundAddressNotFound) {
		logger.Error().Err(err).Msg("waiting for pre-churn XMR vault readiness")
		return OpResult{Continue: false}
	}
	if err != nil || inboundAddress.IsEmpty() {
		// A successful query may find no vault before initial keygen. Churn-out
		// verification must snapshot an existing address before requesting leave.
		logger.Info().Err(err).Msg("no pre-churn XMR inbound address")
		return OpResult{Continue: !a.requireExisting}
	}

	a.inboundBefore = inboundAddress
	logger.Info().Stringer("inbound_before", inboundAddress).Msg("captured pre-churn XMR inbound address")
	return OpResult{Continue: true}
}

func (a *XMRPostChurnActor) startChurn(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := map[string]bool{}
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}
	if len(activeNodes) > 1 {
		a.Log().Info().Msg("cluster already churned; waiting for XMR vault rotation")
		return OpResult{Continue: true}
	}

	if result := waitForXMRFrostChurnReadiness(a.Log(), config); !result.Continue {
		return result
	}

	node := config.NodeUsers[0]
	if !node.Acquire() {
		a.Log().Warn().Msg("failed to acquire node lock")
		return OpResult{Continue: false}
	}
	defer node.Release()

	accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get node thor address")
		return OpResult{Continue: false}
	}

	currentHeight, err := node.Thorchain.GetBlockHeight()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thorchain block height")
		return OpResult{Continue: false}
	}
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get active vaults")
		return OpResult{Continue: false}
	}
	lastChurnHeight, ok := lastActiveVaultHeight(vaults)
	if !ok {
		a.Log().Error().Msg("failed to find active vault status height")
		return OpResult{Continue: false}
	}
	churnInterval := xmrPostChurnScheduledInterval(currentHeight, lastChurnHeight)
	txid, err := a.broadcastMimir(node, accAddr, "CHURNINTERVAL", churnInterval)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast churn interval mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().
		Int64("value", churnInterval).
		Int64("current_height", currentHeight).
		Int64("last_churn_height", lastChurnHeight).
		Str("txid", txid.String()).
		Msg("broadcasted churn interval mimir")

	txid, err = a.broadcastMimir(node, accAddr, constants.ChurnRetryInterval.String(), xmrPostChurnRetryInterval)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast churn retry interval mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().
		Int64("value", xmrPostChurnRetryInterval).
		Str("txid", txid.String()).
		Msg("broadcasted churn retry interval mimir")

	txid, err = a.broadcastMimir(node, accAddr, constants.NumberOfNewNodesPerChurn.String(), xmrPostChurnNewNodesPerChurn)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast node expansion mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().
		Int64("value", xmrPostChurnNewNodesPerChurn).
		Str("txid", txid.String()).
		Msg("broadcasted node expansion mimir")

	txid, err = a.broadcastMimir(node, accAddr, "HaltChurning", 0)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast churn mimir")
		return OpResult{Continue: false}
	}

	a.Log().Info().Str("txid", txid.String()).Msg("broadcasted churn mimir")
	return OpResult{Continue: true}
}

func (a *XMRPostChurnActor) waitForVaultRotation(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := 0
	statusCounts := map[string]int{}
	for _, node := range nodes {
		statusCounts[node.Status]++
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes++
		}
	}
	minimum := 2
	if a.requireExisting {
		minimum = int(constants.NewConstantValue().GetInt64Value(constants.MinimumNodesForBFT))
	}
	if activeNodes < minimum {
		a.Log().Info().
			Int("active_nodes", activeNodes).
			Interface("node_statuses", statusCounts).
			Msg("waiting for churn to activate additional nodes")
		return OpResult{Continue: false}
	}

	inboundAddress, _, err := thornode.GetInboundAddress(common.XMRChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get XMR inbound address")
		return OpResult{Continue: false}
	}
	if inboundAddress.Equals(a.inboundBefore) {
		a.Log().Info().Stringer("inbound_address", inboundAddress).Msg("waiting for XMR inbound address rotation")
		return OpResult{Continue: false}
	}

	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get asgard vaults")
		return OpResult{Continue: false}
	}
	if a.requireExisting && !xmrVaultMatchesActiveNodes(vaults, inboundAddress, nodes) {
		a.Log().Info().Msg("waiting for replacement XMR vault membership to match active validators")
		return OpResult{}
	}
	if !xmrVaultRotationReady(vaults, a.inboundBefore, inboundAddress, activeNodes) {
		a.Log().Info().Stringer("inbound_address", inboundAddress).Msg("waiting for active XMR vault funding")
		return OpResult{Continue: false}
	}

	snapshot, err := getActiveXMRVaultBalance(config, nil)
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for active XMR vault spendable balance")
		return OpResult{Continue: false}
	}
	if !xmrPostChurnReady(vaults, a.inboundBefore, inboundAddress, activeNodes, snapshot) {
		a.Log().Info().
			Stringer("vault_pubkey", snapshot.pubKey).
			Stringer("total", snapshot.total).
			Stringer("spendable", snapshot.spendable).
			Msg("waiting for active XMR vault spendable balance")
		return OpResult{Continue: false}
	}

	a.inboundAfter = inboundAddress
	a.expectedOutboundVault = snapshot.pubKey
	a.Log().Info().
		Stringer("inbound_before", a.inboundBefore).
		Stringer("inbound_after", a.inboundAfter).
		Msg("XMR active vault rotated")
	return OpResult{Continue: true}
}

// Match identities, not just counts: a four-member vault containing the departed
// node must not satisfy a four-active-node replacement check. This is a mocknet
// scenario assertion; validator eligibility and keygen membership stay on chain.
func xmrVaultMatchesActiveNodes(vaults []openapi.Vault, inbound common.Address, nodes []openapi.Node) bool {
	active := make(map[string]bool)
	for _, node := range nodes {
		if node.Status != atypes.NodeStatus_Active.String() {
			continue
		}
		pubkey := node.PubKeySet.GetSecp256k1()
		if pubkey == "" || active[pubkey] {
			return false
		}
		active[pubkey] = true
	}
	if int64(len(active)) < constants.NewConstantValue().GetInt64Value(constants.MinimumNodesForBFT) {
		return false
	}
	for _, vault := range vaults {
		if vault.Status != thorchain.ActiveVault.String() || vault.GetPubKeyFrost() == "" || !vaultHasAddress(vault, common.XMRChain, inbound) {
			continue
		}
		members := make(map[string]bool)
		for _, pubkey := range vault.Membership {
			if !active[pubkey] || members[pubkey] {
				return false
			}
			members[pubkey] = true
		}
		return len(members) == len(active)
	}
	return false
}

func xmrPostChurnReady(vaults []openapi.Vault, inboundBefore, inboundAfter common.Address, activeNodes int, snapshot xmrVaultBalanceSnapshot) bool {
	return xmrVaultRotationReady(vaults, inboundBefore, inboundAfter, activeNodes) && !snapshot.spendable.IsZero()
}

func xmrVaultRotationReady(vaults []openapi.Vault, inboundBefore, inboundAfter common.Address, activeNodes int) bool {
	if activeNodes <= 1 {
		return false
	}
	if inboundAfter.IsEmpty() || inboundAfter.Equals(inboundBefore) {
		return false
	}
	for _, vault := range vaults {
		if vault.Status != thorchain.ActiveVault.String() {
			continue
		}
		if !vaultHasAddress(vault, common.XMRChain, inboundAfter) {
			continue
		}
		if !vaultHasPositiveCoin(vault, common.XMRAsset) {
			continue
		}
		return true
	}
	return false
}

func xmrPostChurnScheduledInterval(currentHeight, lastChurnHeight int64) int64 {
	return scheduledChurnInterval(currentHeight, lastChurnHeight, xmrPostChurnLeadBlocks)
}

func vaultHasAddress(vault openapi.Vault, chain common.Chain, address common.Address) bool {
	for _, vaultAddress := range vault.Addresses {
		if vaultAddress.Chain == chain.String() && common.Address(vaultAddress.Address).Equals(address) {
			return true
		}
	}
	return false
}

func vaultHasPositiveCoin(vault openapi.Vault, asset common.Asset) bool {
	for _, coin := range vault.Coins {
		if coin.Asset != asset.String() {
			continue
		}
		amount, ok := new(big.Int).SetString(coin.Amount, 10)
		return ok && amount.Sign() > 0
	}
	return false
}

func (a *XMRPostChurnActor) broadcastMimir(node *User, accAddr cosmos.AccAddress, key string, value int64) (common.TxID, error) {
	mimir := thorchain.NewMsgMimir(key, value, accAddr)
	txid, err := node.Thorchain.Broadcast(mimir)
	if err != nil {
		return common.TxID(""), fmt.Errorf("broadcast %s=%d: %w", key, value, err)
	}
	return txid, nil
}
