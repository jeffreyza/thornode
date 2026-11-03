package features

import (
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	bondYieldBps                       = uint64(4000)
	bondYieldEnabled                   = int64(1)
	bondYieldMinL1OutboundFeeUSDKey    = "MinimumL1OutboundFeeUSD"
	bondYieldMinL1OutboundFeeUSD       = int64(0)
	bondYieldMinOutboundFeeMultKey     = "MinOutboundFeeMultiplierBasisPoints"
	bondYieldMinOutboundFeeMultBps     = int64(1)
	bondYieldTHORNameFundRune          = uint64(50 * common.One)
	bondYieldPreferredAssetHighMultBps = int64(constants.MaxBasisPts)
)

type BondYieldState struct {
	node                     *User
	owner                    *User
	nodeAddress              cosmos.AccAddress
	providerAddress          common.Address
	payoutOwnerAddress       common.Address
	payoutOwnerNode          *User
	payoutAsset              common.Asset
	payoutName               string
	collectorBeforeAutoClaim cosmos.Uint
	collectorAfterAutoClaim  cosmos.Uint
	skipReason               string
	// leaveStatusSince is the target's status_since when its leave was requested.
	leaveStatusSince int64
}

// NewBondYieldState creates the shared state carried across the Bond Yield scenario actors.
func NewBondYieldState() *BondYieldState {
	return &BondYieldState{
		payoutAsset: common.ETHAsset,
	}
}

// SkipReason reports why the Bond Yield scenario cannot run in this environment.
func (s *BondYieldState) SkipReason() string {
	return s.skipReason
}

type BondYieldSetupActor struct {
	Actor
	state *BondYieldState
}

// NewBondYieldSetupActor enables Bond Yield and prepares a provider-owned payout THORName.
func NewBondYieldSetupActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldSetupActor{
		Actor: *NewActor("BondYield-Setup", rng),
		state: state,
	}
	a.Timeout = 5 * time.Minute
	a.Ops = append(a.Ops, a.requireMocknetCluster)
	a.Ops = append(a.Ops, a.acquireActiveNodeAndOwner)
	a.Ops = append(a.Ops, a.enableBondYield)
	a.Ops = append(a.Ops, a.setLowOutboundFeeMimirs)
	a.Ops = append(a.Ops, a.waitBondYieldMimirs)
	a.Ops = append(a.Ops, a.registerPayoutTHORName)
	a.Ops = append(a.Ops, a.waitPayoutTHORNameRegistered)
	a.Ops = append(a.Ops, a.transferPayoutTHORNameToProvider)
	a.Ops = append(a.Ops, a.waitPayoutTHORNameOwnedByProvider)
	a.Ops = append(a.Ops, a.configureProviderPayoutTHORName)
	a.Ops = append(a.Ops, a.waitPayoutTHORName)
	return &a.Actor
}

type BondYieldSelectTargetActor struct {
	Actor
	state *BondYieldState
}

// NewBondYieldSelectTargetActor finds the active provider whose next reward can be auto-claimed.
func NewBondYieldSelectTargetActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldSelectTargetActor{
		Actor: *NewActor("BondYield-SelectTarget", rng),
		state: state,
	}
	a.Timeout = 2 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.selectActiveTargetWithYield)
	return &a.Actor
}

type BondYieldTargetAutoClaimActor struct {
	Actor
	state *BondYieldState
}

// NewBondYieldTargetAutoClaimActor configures the target provider to auto-claim to the payout THORName.
func NewBondYieldTargetAutoClaimActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldTargetAutoClaimActor{
		Actor: *NewActor("BondYield-TargetAutoClaim", rng),
		state: state,
	}
	a.Timeout = 2 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.transferPayoutTHORNameToTarget)
	a.Ops = append(a.Ops, a.waitPayoutTHORNameOwnedByTarget)
	a.Ops = append(a.Ops, a.configureTargetPayoutTHORName)
	a.Ops = append(a.Ops, a.waitTargetPayoutTHORName)
	a.Ops = append(a.Ops, a.configureTargetAutoClaim)
	a.Ops = append(a.Ops, a.waitTargetAutoClaimConfigured)
	return &a.Actor
}

type BondYieldChurnOutActor struct {
	Actor
	state *BondYieldState
}

// NewBondYieldChurnOutActor churns the target node out and checks automatic payout behavior.
func NewBondYieldChurnOutActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldChurnOutActor{
		Actor: *NewActor("BondYield-ChurnOut", rng),
		state: state,
	}
	a.Timeout = 35 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.requestTargetLeave)
	a.Ops = append(a.Ops, a.scheduleTargetChurnOut)
	a.Ops = append(a.Ops, a.waitTargetChurnedOut)
	return &a.Actor
}

// NewBondYieldCleanupActor restores the mocknet defaults for global Mimirs
// changed by the Bond Yield scenario. Each restore actor queries the current
// validator set and votes from every active simulation node, so cleanup still
// succeeds when the node used during setup has churned out.
func NewBondYieldCleanupActor(rng *rand.Rand) *Actor {
	a := NewActor("BondYield-Cleanup", rng)
	defaults := constants.NewConstantValue()
	for _, setting := range []struct {
		key   string
		value int64
	}{
		{constants.BondYieldEnabled.String(), defaults.GetInt64Value(constants.BondYieldEnabled)},
		{bondYieldMinL1OutboundFeeUSDKey, defaults.GetInt64Value(constants.MinimumL1OutboundFeeUSD)},
		{bondYieldMinOutboundFeeMultKey, defaults.GetInt64Value(constants.MinOutboundFeeMultiplierBasisPoints)},
		// set by core.ScheduleChurn for the cluster churn and the churn-out
		{constants.ChurnInterval.String(), defaults.GetInt64Value(constants.ChurnInterval)},
	} {
		a.Append(NewMultiNodeMimirActor(setting.key, setting.value, rng))
	}
	return a
}

type bondYieldNode struct {
	Status            string `json:"status"`
	StatusSince       int64  `json:"status_since"`
	TotalBond         string `json:"total_bond"`
	RequestedToLeave  bool   `json:"requested_to_leave"`
	CurrentAward      string `json:"current_award"`
	SlashPoints       int64  `json:"slash_points"`
	ActiveBlockHeight int64  `json:"active_block_height"`
	BondProviders     struct {
		Providers []bondYieldProvider `json:"providers"`
	} `json:"bond_providers"`
}

type bondYieldProvider struct {
	BondAddress  string `json:"bond_address"`
	Bond         string `json:"bond"`
	Bps          uint64 `json:"bps"`
	PayoutTarget string `json:"payout_target"`
}

type bondYieldTHORName struct {
	Owner                               string               `json:"owner"`
	PreferredAsset                      string               `json:"preferred_asset"`
	PreferredAssetOutboundFeeMultiplier int64                `json:"preferred_asset_outbound_fee_multiplier"`
	AffiliateCollectorRune              string               `json:"affiliate_collector_rune"`
	Aliases                             []bondYieldAliasInfo `json:"aliases"`
}

type bondYieldAliasInfo struct {
	Chain   string `json:"chain"`
	Address string `json:"address"`
}

// requireMocknetCluster skips Bond Yield checks unless the simulation is running a multi-node mocknet.
func (a *BondYieldSetupActor) requireMocknetCluster(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}
	if len(nodes) < 2 {
		a.state.skipReason = fmt.Sprintf("mocknet-cluster not detected: node_count=%d", len(nodes))
		a.Log().Info().Str("reason", a.state.skipReason).Msg("skipping bond-yield test")
		return OpResult{Finish: true}
	}
	return OpResult{Continue: true}
}

// skipIfRequired stops target selection when setup determined the harness cannot run Bond Yield checks.
func (a *BondYieldSelectTargetActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

// skipIfRequired stops auto-claim setup when setup determined the harness cannot run Bond Yield checks.
func (a *BondYieldTargetAutoClaimActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

// skipIfRequired stops churn-out checks when setup determined the harness cannot run Bond Yield checks.
func (a *BondYieldChurnOutActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

// acquireActiveNodeAndOwner chooses the single active node and an unlocked user to register the payout THORName.
func (a *BondYieldSetupActor) acquireActiveNodeAndOwner(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}
	if len(activeNodes) != 1 {
		a.Log().Info().Int("active_nodes", len(activeNodes)).Msg("waiting for single active node before bond-yield churn test")
		return OpResult{Continue: false}
	}

	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get node THOR address")
			return OpResult{Continue: false}
		}
		if !activeNodes[accAddr.String()] {
			continue
		}
		a.state.node = node
		a.state.nodeAddress = accAddr
		a.state.providerAddress = common.Address(accAddr.String())
		break
	}
	if a.state.node == nil {
		a.Log().Info().Msg("waiting for active node user")
		return OpResult{Continue: false}
	}

	for _, user := range config.Users {
		if user.IsLocked() {
			continue
		}
		balances, err := thornode.GetBalances(user.Address(common.THORChain))
		if err != nil {
			a.Log().Error().Err(err).Str("user", user.Name()).Msg("failed to get user balance")
			continue
		}
		if balances.GetCoin(common.RuneAsset()).Amount.LT(cosmos.NewUint(bondYieldTHORNameFundRune)) {
			continue
		}

		a.state.owner = user
		a.state.payoutName = fmt.Sprintf("simyield%s", user.Name())
		a.Log().Info().
			Stringer("node", a.state.nodeAddress).
			Str("registrar", user.Name()).
			Str("thorname", a.state.payoutName).
			Msg("selected active node bond provider and THORName registrar")
		return OpResult{Continue: true}
	}

	a.Log().Info().Msg("waiting for THORName owner user")
	return OpResult{Continue: false}
}

// enableBondYield turns on Bond Yield automatic payouts in the simulation.
func (a *BondYieldSetupActor) enableBondYield(config *OpConfig) OpResult {
	return a.broadcastMimir(constants.BondYieldEnabled.String(), bondYieldEnabled)
}

// setLowOutboundFeeMimirs lowers outbound fee Mimirs so small simulated payouts can complete.
func (a *BondYieldSetupActor) setLowOutboundFeeMimirs(config *OpConfig) OpResult {
	for key, value := range map[string]int64{
		bondYieldMinL1OutboundFeeUSDKey: bondYieldMinL1OutboundFeeUSD,
		bondYieldMinOutboundFeeMultKey:  bondYieldMinOutboundFeeMultBps,
	} {
		result := a.broadcastMimir(key, value)
		if !result.Continue && !result.Finish {
			return result
		}
	}
	return OpResult{Continue: true}
}

// waitBondYieldMimirs waits until all Bond Yield and fee Mimirs are visible through thornode.
func (a *BondYieldSetupActor) waitBondYieldMimirs(config *OpConfig) OpResult {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get mimirs")
		return OpResult{Continue: false}
	}

	expected := map[string]int64{
		constants.BondYieldEnabled.String(): bondYieldEnabled,
		bondYieldMinL1OutboundFeeUSDKey:     bondYieldMinL1OutboundFeeUSD,
		bondYieldMinOutboundFeeMultKey:      bondYieldMinOutboundFeeMultBps,
	}
	for key, want := range expected {
		got, ok := getMimirCaseInsensitive(mimirs, key)
		if !ok || got != want {
			a.Log().Info().Str("key", key).Int64("want", want).Int64("got", got).Bool("found", ok).Msg("waiting for bond-yield mimir")
			return OpResult{Continue: false}
		}
	}

	return OpResult{Continue: true}
}

// registerPayoutTHORName creates the payout THORName with the registrar user before ownership transfer.
func (a *BondYieldSetupActor) registerPayoutTHORName(config *OpConfig) OpResult {
	txid, memo, err := broadcastPayoutTHORNameRegistration(a.state, cosmos.NewUint(bondYieldTHORNameFundRune), bondYieldPreferredAssetHighMultBps)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to register payout THORName")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted payout THORName registration")
	return OpResult{Continue: true}
}

// waitPayoutTHORNameRegistered waits for the initial THORName registration and preferred-asset setup.
func (a *BondYieldSetupActor) waitPayoutTHORNameRegistered(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Info().Err(err).Str("thorname", a.state.payoutName).Msg("waiting for payout THORName")
		return OpResult{Continue: false}
	}
	if tn.Owner != a.state.owner.Address(common.THORChain).String() {
		a.Log().Info().Str("owner", tn.Owner).Msg("waiting for payout THORName registration owner")
		return OpResult{Continue: false}
	}
	if !hasAlias(tn, a.state.payoutAsset.GetChain(), a.state.owner.Address(a.state.payoutAsset.GetChain())) {
		a.Log().Info().Str("thorname", a.state.payoutName).Msg("waiting for payout THORName asset alias")
		return OpResult{Continue: false}
	}
	if tn.PreferredAsset != a.state.payoutAsset.String() {
		a.Log().Info().Str("preferred_asset", tn.PreferredAsset).Msg("waiting for payout THORName preferred asset")
		return OpResult{Continue: false}
	}
	if tn.PreferredAssetOutboundFeeMultiplier != bondYieldPreferredAssetHighMultBps {
		a.Log().Info().Int64("multiplier", tn.PreferredAssetOutboundFeeMultiplier).Msg("waiting for high THORName payout multiplier")
		return OpResult{Continue: false}
	}

	a.Log().Info().Msg("payout THORName registration ready")
	return OpResult{Continue: true}
}

// transferPayoutTHORNameToProvider transfers the payout THORName to the selected bond provider.
func (a *BondYieldSetupActor) transferPayoutTHORNameToProvider(config *OpConfig) OpResult {
	txid, memo, err := broadcastPayoutTHORNameOwnerTransfer(a.state)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to transfer payout THORName to bond provider")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted payout THORName owner transfer")
	return OpResult{Continue: true}
}

// waitPayoutTHORNameOwnedByProvider confirms the provider owns the payout THORName before it is used.
func (a *BondYieldSetupActor) waitPayoutTHORNameOwnedByProvider(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Info().Err(err).Str("thorname", a.state.payoutName).Msg("waiting for payout THORName")
		return OpResult{Continue: false}
	}
	if tn.Owner != a.state.providerAddress.String() {
		a.Log().Info().Str("owner", tn.Owner).Str("provider", a.state.providerAddress.String()).Msg("waiting for payout THORName provider ownership")
		return OpResult{Continue: false}
	}
	a.state.payoutOwnerNode = a.state.node
	a.state.payoutOwnerAddress = a.state.providerAddress
	a.Log().Info().Msg("payout THORName owned by bond provider")
	return OpResult{Continue: true}
}

// configureProviderPayoutTHORName re-applies alias and preferred-asset settings as the provider owner.
func (a *BondYieldSetupActor) configureProviderPayoutTHORName(config *OpConfig) OpResult {
	txid, memo, err := broadcastPayoutTHORNameProviderConfig(a.state, bondYieldPreferredAssetHighMultBps)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to configure provider-owned payout THORName")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted provider-owned payout THORName configuration")
	return OpResult{Continue: true}
}

// waitPayoutTHORName verifies the provider-owned THORName is fully configured for auto-claim payouts.
func (a *BondYieldSetupActor) waitPayoutTHORName(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Info().Err(err).Str("thorname", a.state.payoutName).Msg("waiting for payout THORName")
		return OpResult{Continue: false}
	}
	if tn.Owner != a.state.providerAddress.String() {
		a.Log().Info().Str("owner", tn.Owner).Str("provider", a.state.providerAddress.String()).Msg("waiting for payout THORName provider ownership")
		return OpResult{Continue: false}
	}
	if !hasAlias(tn, a.state.payoutAsset.GetChain(), a.state.owner.Address(a.state.payoutAsset.GetChain())) {
		a.Log().Info().Str("thorname", a.state.payoutName).Msg("waiting for payout THORName asset alias")
		return OpResult{Continue: false}
	}
	if tn.PreferredAsset != a.state.payoutAsset.String() {
		a.Log().Info().Str("preferred_asset", tn.PreferredAsset).Msg("waiting for payout THORName preferred asset")
		return OpResult{Continue: false}
	}
	if tn.PreferredAssetOutboundFeeMultiplier != bondYieldPreferredAssetHighMultBps {
		a.Log().Info().Int64("multiplier", tn.PreferredAssetOutboundFeeMultiplier).Msg("waiting for high THORName payout multiplier")
		return OpResult{Continue: false}
	}

	a.state.payoutOwnerNode = a.state.node
	a.state.payoutOwnerAddress = a.state.providerAddress
	a.Log().Info().Msg("payout THORName ready")
	return OpResult{Continue: true}
}

// selectActiveTargetWithYield finds an active provider with a current bond award to split.
func (a *BondYieldSelectTargetActor) selectActiveTargetWithYield(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}

	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get node THOR address")
			return OpResult{Continue: false}
		}
		if !activeNodes[accAddr.String()] {
			continue
		}

		providerAddress := common.Address(accAddr.String())
		queriedNode, _, ok, err := getBondYieldNodeProvider(accAddr, providerAddress.String())
		if err != nil {
			a.Log().Error().Err(err).Stringer("node", accAddr).Msg("failed to query active target provider")
			continue
		}
		currentAward := bondYieldUint(queriedNode.CurrentAward)
		if !ok || currentAward.LTE(cosmos.NewUint(1)) {
			continue
		}

		a.state.node = node
		a.state.nodeAddress = accAddr
		a.state.providerAddress = providerAddress
		a.Log().Info().
			Stringer("node", a.state.nodeAddress).
			Str("current_award", currentAward.String()).
			Msg("selected active node for bond-yield payout checks")
		return OpResult{Continue: true}
	}

	a.Log().Info().Int("active_nodes", len(activeNodes)).Msg("waiting for active node with current bond award")
	return OpResult{Continue: false}
}

// configureTargetAutoClaim stores the target provider's auto-claim settings against the payout THORName.
func (a *BondYieldTargetAutoClaimActor) configureTargetAutoClaim(config *OpConfig) OpResult {
	txid, memo, err := broadcastBondYieldAutoClaim(a.state.node, a.state.nodeAddress, bondYieldBps, a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to configure target bond-yield auto-claim")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted target bond-yield auto-claim")
	return OpResult{Continue: true}
}

// transferPayoutTHORNameToTarget moves THORName ownership to the selected target provider.
func (a *BondYieldTargetAutoClaimActor) transferPayoutTHORNameToTarget(config *OpConfig) OpResult {
	if a.state.payoutOwnerAddress.Equals(a.state.providerAddress) {
		return OpResult{Continue: true}
	}

	txid, memo, err := broadcastPayoutTHORNameOwnerTransferFrom(a.state.payoutOwnerNode, a.state, a.state.payoutOwnerAddress, a.state.providerAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to transfer payout THORName to target bond provider")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted payout THORName transfer to target provider")
	return OpResult{Continue: true}
}

// waitPayoutTHORNameOwnedByTarget confirms the target provider can legally use the THORName payout target.
func (a *BondYieldTargetAutoClaimActor) waitPayoutTHORNameOwnedByTarget(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Info().Err(err).Str("thorname", a.state.payoutName).Msg("waiting for payout THORName")
		return OpResult{Continue: false}
	}
	if tn.Owner != a.state.providerAddress.String() {
		a.Log().Info().Str("owner", tn.Owner).Str("provider", a.state.providerAddress.String()).Msg("waiting for payout THORName target ownership")
		return OpResult{Continue: false}
	}
	a.state.payoutOwnerNode = a.state.node
	a.state.payoutOwnerAddress = a.state.providerAddress
	a.Log().Info().Msg("payout THORName owned by target bond provider")
	return OpResult{Continue: true}
}

// configureTargetPayoutTHORName restores alias and preferred asset metadata after target ownership transfer.
func (a *BondYieldTargetAutoClaimActor) configureTargetPayoutTHORName(config *OpConfig) OpResult {
	txid, memo, err := broadcastPayoutTHORNameProviderConfig(a.state, bondYieldPreferredAssetHighMultBps)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to configure target-owned payout THORName")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted target-owned payout THORName configuration")
	return OpResult{Continue: true}
}

// waitTargetPayoutTHORName waits until the target-owned payout THORName is ready for auto claims.
func (a *BondYieldTargetAutoClaimActor) waitTargetPayoutTHORName(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Info().Err(err).Str("thorname", a.state.payoutName).Msg("waiting for payout THORName")
		return OpResult{Continue: false}
	}
	if tn.Owner != a.state.providerAddress.String() {
		a.Log().Info().Str("owner", tn.Owner).Str("provider", a.state.providerAddress.String()).Msg("waiting for payout THORName target ownership")
		return OpResult{Continue: false}
	}
	if !hasAlias(tn, a.state.payoutAsset.GetChain(), a.state.owner.Address(a.state.payoutAsset.GetChain())) {
		a.Log().Info().Str("thorname", a.state.payoutName).Msg("waiting for target payout THORName asset alias")
		return OpResult{Continue: false}
	}
	if tn.PreferredAsset != a.state.payoutAsset.String() {
		a.Log().Info().Str("preferred_asset", tn.PreferredAsset).Msg("waiting for target payout THORName preferred asset")
		return OpResult{Continue: false}
	}
	if tn.PreferredAssetOutboundFeeMultiplier != bondYieldPreferredAssetHighMultBps {
		a.Log().Info().Int64("multiplier", tn.PreferredAssetOutboundFeeMultiplier).Msg("waiting for high target payout THORName multiplier")
		return OpResult{Continue: false}
	}
	a.state.collectorBeforeAutoClaim = bondYieldUint(tn.AffiliateCollectorRune)
	a.Log().Info().
		Str("collector_rune", a.state.collectorBeforeAutoClaim.String()).
		Msg("target payout THORName ready")
	return OpResult{Continue: true}
}

// waitTargetAutoClaimConfigured checks that the target provider's auto-claim settings persisted.
func (a *BondYieldTargetAutoClaimActor) waitTargetAutoClaimConfigured(config *OpConfig) OpResult {
	return waitBondYieldAutoClaim(a.Actor, a.state.nodeAddress, a.state.providerAddress.String(), bondYieldBps, a.state.payoutName)
}

// requestTargetLeave asks the target node to leave so auto-claim can be tested during churn-out.
func (a *BondYieldChurnOutActor) requestTargetLeave(config *OpConfig) OpResult {
	node, err := getBondYieldNode(a.state.nodeAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target node before leave")
		return OpResult{Continue: false}
	}
	a.state.leaveStatusSince = node.StatusSince

	txid, err := broadcastLeave(a.state.node, a.state.nodeAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast target leave")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Stringer("node", a.state.nodeAddress).Msg("broadcasted target leave")
	return OpResult{Continue: true}
}

// scheduleTargetChurnOut makes the churn that removes the target fire soon. The
// previous churn's migration is still running at lastChurn+ChurnInterval, so
// without this the leave waits for a ChurnRetryInterval tick.
func (a *BondYieldChurnOutActor) scheduleTargetChurnOut(config *OpConfig) OpResult {
	if _, err := core.ScheduleChurn(a.Log(), config); err != nil {
		a.Log().Error().Err(err).Msg("failed to schedule target churn-out")
		return OpResult{Continue: false}
	}
	return OpResult{Continue: true}
}

// bondYieldTargetChurnedOut reports whether the target left the active set after its
// leave request. Removal clears RequestedToLeave (ValidatorMgr.EndBlock), so a churn
// that fires as soon as the migration ends can mark the node Ready and re-admit it
// before the next poll sees Standby. Any status change since the leave counts.
func bondYieldTargetChurnedOut(node bondYieldNode, leaveStatusSince int64) bool {
	return node.Status != atypes.NodeStatus_Active.String() || node.StatusSince != leaveStatusSince
}

// waitTargetChurnedOut verifies churn-out auto-claim moved yield into the payout THORName collector.
func (a *BondYieldChurnOutActor) waitTargetChurnedOut(config *OpConfig) OpResult {
	node, _, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target node")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	if !bondYieldTargetChurnedOut(node, a.state.leaveStatusSince) {
		a.Log().Info().Str("status", node.Status).Bool("requested_to_leave", node.RequestedToLeave).Msg("waiting for target node to churn out")
		return OpResult{Continue: false}
	}

	network, err := thornode.GetNetwork()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get network")
		return OpResult{Continue: false}
	}
	if network.VaultsMigrating {
		a.Log().Info().Str("status", node.Status).Msg("waiting for churn-out migration to finish")
		return OpResult{Continue: false}
	}

	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query payout THORName")
		return OpResult{Continue: false}
	}
	if tn.Owner != a.state.providerAddress.String() {
		return OpResult{
			Finish: true,
			Error: fmt.Errorf(
				"payout THORName owner changed before collector assertion: got %s, want %s",
				tn.Owner,
				a.state.providerAddress,
			),
		}
	}
	currentRune := bondYieldUint(tn.AffiliateCollectorRune)
	if currentRune.GT(a.state.collectorBeforeAutoClaim) {
		a.state.collectorAfterAutoClaim = currentRune
		a.Log().Info().
			Str("collector_rune", currentRune.String()).
			Msg("verified automatic bond-yield payout to affiliate collector during target churn-out")
		return OpResult{Continue: true}
	}

	a.Log().Info().
		Str("collector_rune", currentRune.String()).
		Str("collector_before_auto_claim", a.state.collectorBeforeAutoClaim.String()).
		Msg("waiting for target churn-out auto-claim")
	return OpResult{Continue: false}
}

// broadcastMimir sends a Mimir update from the active node selected for setup.
func (a *BondYieldSetupActor) broadcastMimir(key string, value int64) OpResult {
	if !a.state.node.Acquire() {
		a.Log().Info().Msg("waiting for node account lock")
		return OpResult{Continue: false}
	}
	defer a.state.node.Release()

	mimir := thorchain.NewMsgMimir(key, value, a.state.nodeAddress)
	txid, err := a.state.node.Thorchain.Broadcast(mimir)
	if err != nil {
		a.Log().Error().Err(err).Str("key", key).Int64("value", value).Msg("failed to broadcast mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid.String()).Str("key", key).Int64("value", value).Msg("broadcasted mimir")
	return OpResult{Continue: true}
}

// skipBondYieldIfRequired finishes actors after setup records an environment skip reason.
func skipBondYieldIfRequired(actor Actor, state *BondYieldState) OpResult {
	if state.skipReason == "" {
		return OpResult{Continue: true}
	}
	actor.Log().Info().Str("reason", state.skipReason).Msg("skipping bond-yield actor")
	return OpResult{Finish: true}
}

// waitBondYieldAutoClaim polls the node query until provider auto-claim config matches expectations.
func waitBondYieldAutoClaim(actor Actor, nodeAddress cosmos.AccAddress, providerAddress string, bps uint64, payoutName string) OpResult {
	provider, ok, err := getBondYieldProvider(nodeAddress, providerAddress)
	if err != nil {
		actor.Log().Error().Err(err).Msg("failed to query bond provider")
		return OpResult{Continue: false}
	}
	if !ok {
		actor.Log().Info().Str("provider", providerAddress).Msg("waiting for bond provider")
		return OpResult{Continue: false}
	}
	if provider.Bps == bps && provider.PayoutTarget == payoutName {
		actor.Log().Info().Uint64("bps", bps).Str("payout_target", payoutName).Msg("bond-yield auto-claim configuration ready")
		return OpResult{Continue: true}
	}

	actor.Log().Info().
		Uint64("bps", provider.Bps).
		Str("payout_target", provider.PayoutTarget).
		Uint64("want_bps", bps).
		Str("want_payout_target", payoutName).
		Msg("waiting for bond-yield auto-claim config")
	return OpResult{Continue: false}
}

// broadcastPayoutTHORNameRegistration registers the payout THORName with the registrar user.
func broadcastPayoutTHORNameRegistration(state *BondYieldState, coinAmount cosmos.Uint, multiplier int64) (string, string, error) {
	return broadcastPayoutTHORNameUpdate(state.owner, state, state.owner.Address(common.THORChain), coinAmount, multiplier)
}

// broadcastPayoutTHORNameOwnerTransfer transfers the payout THORName from registrar to provider.
func broadcastPayoutTHORNameOwnerTransfer(state *BondYieldState) (string, string, error) {
	return broadcastPayoutTHORNameOwnerTransferFrom(state.owner, state, state.owner.Address(common.THORChain), state.providerAddress)
}

// broadcastPayoutTHORNameOwnerTransferFrom emits a THORName memo that changes ownership.
func broadcastPayoutTHORNameOwnerTransferFrom(signer *User, state *BondYieldState, fromAddress, toAddress common.Address) (string, string, error) {
	memo := fmt.Sprintf(
		"~:%s:%s:%s:%s::0:-1",
		state.payoutName,
		common.THORChain,
		fromAddress,
		toAddress,
	)
	txid, err := broadcastDeposit(signer, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())), memo)
	return txid, memo, err
}

// broadcastPayoutTHORNameProviderConfig updates alias and preferred asset as the current THORName owner.
func broadcastPayoutTHORNameProviderConfig(state *BondYieldState, multiplier int64) (string, string, error) {
	return broadcastPayoutTHORNameUpdate(state.payoutOwnerNode, state, state.payoutOwnerAddress, cosmos.ZeroUint(), multiplier)
}

// broadcastPayoutTHORNameUpdate emits the THORName memo used for registration and provider-owned updates.
func broadcastPayoutTHORNameUpdate(signer *User, state *BondYieldState, ownerAddress common.Address, coinAmount cosmos.Uint, multiplier int64) (string, string, error) {
	aliasAddress := state.owner.Address(state.payoutAsset.GetChain())
	memo := fmt.Sprintf(
		"~:%s:%s:%s:%s:%s:0:%d",
		state.payoutName,
		state.payoutAsset.GetChain(),
		aliasAddress,
		ownerAddress,
		state.payoutAsset,
		multiplier,
	)
	coin := common.NewCoin(common.RuneAsset(), coinAmount)
	txid, err := broadcastDeposit(signer, common.NewCoins(coin), memo)
	return txid, memo, err
}

// broadcastBondYieldAutoClaim stores or clears a provider's Bond Yield auto-claim configuration.
func broadcastBondYieldAutoClaim(user *User, nodeAddress cosmos.AccAddress, bps uint64, payoutName string) (string, string, error) {
	memo := fmt.Sprintf("bondyield:%s:%d", nodeAddress.String(), bps)
	if payoutName != "" {
		memo = fmt.Sprintf("%s:%s", memo, payoutName)
	}
	txid, err := broadcastDeposit(user, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())), memo)
	return txid, memo, err
}

// broadcastLeave requests that a node leave the active validator set.
func broadcastLeave(user *User, nodeAddress cosmos.AccAddress) (string, error) {
	memo := fmt.Sprintf("leave:%s", nodeAddress.String())
	return broadcastDeposit(user, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())), memo)
}

// broadcastDeposit wraps a memo-only THORChain deposit and serializes access to the signer.
func broadcastDeposit(user *User, coins common.Coins, memo string) (string, error) {
	if !user.Acquire() {
		return "", fmt.Errorf("failed to acquire user lock")
	}
	defer user.Release()

	accAddr, err := user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		return "", err
	}
	deposit := ttypes.NewMsgDeposit(coins, memo, accAddr)
	txid, err := user.Thorchain.Broadcast(deposit)
	if err != nil {
		return "", err
	}
	return txid.String(), nil
}

// getBondYieldProvider fetches a single provider from the node query response.
func getBondYieldProvider(nodeAddress cosmos.AccAddress, providerAddress string) (bondYieldProvider, bool, error) {
	_, provider, ok, err := getBondYieldNodeProvider(nodeAddress, providerAddress)
	return provider, ok, err
}

// getBondYieldNodeProvider fetches a node and returns the provider matching the requested address.
func getBondYieldNodeProvider(nodeAddress cosmos.AccAddress, providerAddress string) (bondYieldNode, bondYieldProvider, bool, error) {
	node, err := getBondYieldNode(nodeAddress)
	if err != nil {
		return bondYieldNode{}, bondYieldProvider{}, false, err
	}
	for _, provider := range node.BondProviders.Providers {
		if provider.BondAddress == providerAddress {
			return node, provider, true, nil
		}
	}
	return node, bondYieldProvider{}, false, nil
}

// getBondYieldNode queries the thornode node endpoint into the Bond Yield test shape.
func getBondYieldNode(nodeAddress cosmos.AccAddress) (bondYieldNode, error) {
	var node bondYieldNode
	url := fmt.Sprintf("%s/thorchain/node/%s", thornode.BaseURL(), nodeAddress.String())
	if err := thornode.Get(url, &node); err != nil {
		return bondYieldNode{}, err
	}
	return node, nil
}

// getBondYieldTHORName queries the THORName endpoint into the Bond Yield test shape.
func getBondYieldTHORName(name string) (bondYieldTHORName, error) {
	var tn bondYieldTHORName
	url := fmt.Sprintf("%s/thorchain/thorname/%s", thornode.BaseURL(), name)
	if err := thornode.Get(url, &tn); err != nil {
		return bondYieldTHORName{}, err
	}
	return tn, nil
}

// bondYieldUint converts optional query string amounts into cosmos.Uint values.
func bondYieldUint(value string) cosmos.Uint {
	if value == "" {
		return cosmos.ZeroUint()
	}
	return cosmos.NewUintFromString(value)
}

// getMimirCaseInsensitive finds a Mimir regardless of response key casing.
func getMimirCaseInsensitive(mimirs map[string]int64, key string) (int64, bool) {
	for k, v := range mimirs {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return 0, false
}

// hasAlias checks that the THORName has the chain alias expected by preferred-asset payouts.
func hasAlias(tn bondYieldTHORName, chain common.Chain, address common.Address) bool {
	for _, alias := range tn.Aliases {
		if alias.Chain == chain.String() && alias.Address == address.String() {
			return true
		}
	}
	return false
}
