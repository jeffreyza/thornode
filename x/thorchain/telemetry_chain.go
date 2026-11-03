package thorchain

import (
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ---------------------------------------------------------------------------
// Block-invariant telemetry constants.
//
// Every value below is identical on every block, so it is built once at package
// init rather than per block. Two reasons, both measured:
//
//   - A []string{...} or []metrics.Label{...} composite literal passed to
//     telemetry.SetGaugeWithLabels escapes, so it is one heap allocation per
//     gauge write. ~59% of the emit path is the go-metrics sink, and per-call
//     allocation is a direct input to that cost.
//   - The mimir key strings were built with fmt.Sprintf per chain per block
//     (three explicit Sprintf plus four more inside GetMimirWithRef, 133 total
//     across the 19 chains). The keys never change.
//
// SAFETY: never mutate any slice below, and never hand one to
// SetGaugeWithLabels with spare capacity. There are two appenders on the label
// slice we pass - the cosmos wrapper (append(labels, globalLabels...)) and
// go-metrics itself (host/service labels) - and either writes into spare
// capacity in place. Composite literals have cap == len so both are forced to
// copy; anything sliced out of a shared buffer must use the three-index form.
// This also matters because InmemSink RETAINS the label slice it is given
// (intv.Gauges[k] = GaugeValue{... Labels: labels}), so a reused-and-mutated
// slice would corrupt gauges already recorded.
// ---------------------------------------------------------------------------

// Metric name keys, hoisted out of the loops that emit them.
var (
	chainTelemKeyTradeAccountsEnabled = []string{"thornode", "trade_accounts", "enabled"}
	chainTelemKeyHaltSource           = []string{"thornode", "chain", "halt_source"}
	chainTelemKeySigningHalted        = []string{"thornode", "chain", "signing_halted"}
	chainTelemKeySecuredHalted        = []string{"thornode", "chain", "secured_halted"}
	chainTelemKeyTradeHalted          = []string{"thornode", "chain", "trade_halted"}
	chainTelemKeyRouterInfo           = []string{"thornode", "chain", "router_info"}
	chainTelemKeyInboundAvailable     = []string{"thornode", "chain", "inbound_available"}
	chainTelemKeyMarkedToLeave        = []string{"thornode", "node", "marked_to_leave"}
	chainTelemKeyLastObservedHeight   = []string{"thornode", "node", "last_observed_height"}
	chainTelemKeyPreflightStatus      = []string{"thornode", "node", "preflight_status"}
	chainTelemKeyUpgradeHeight        = []string{"thornode", "upgrade_proposal", "height"}
	chainTelemKeyUpgradeApproved      = []string{"thornode", "upgrade_proposal", "approved"}
	chainTelemKeyUpgradeQuorumMissing = []string{"thornode", "upgrade_proposal", "quorum_missing"}
)

var (
	chainTelemLabelsTradeGlobal  = []metrics.Label{telemetry.NewLabel("scope", "global")}
	chainTelemLabelsTradeDeposit = []metrics.Label{telemetry.NewLabel("scope", "deposit")}
)

// chainTelemHaltSourceReasons is the halt_source label order, unchanged: the
// two global keys first, then the two per-chain keys. The bool for each is
// assembled per block in the same index order.
var chainTelemHaltSourceReasons = [4]string{"global", "node_pause", "admin", "solvency"}

// chainTelemAssetHalt is one secured/trade asset halt series: which metric, the
// precomputed mimir key, and the {reason, chain} labels.
type chainTelemAssetHalt struct {
	key      []string
	mimirKey string
	labels   []metrics.Label
}

// chainTelemRail holds everything block-invariant for one chain.
type chainTelemRail struct {
	chain common.Chain

	// Mimir keys, precomputed. Built from chain.String() with the same
	// templates the callers used, so the resulting key bytes are identical to
	// what fmt.Sprintf("Halt%sChain", chain) and
	// GetMimirWithRef(template, chain.String()) produced.
	haltKey        string
	solvencyKey    string
	signingHaltKey string

	chainLabels      []metrics.Label    // {chain}
	haltSourceLabels [4][]metrics.Label // {reason, chain}, indexed by chainTelemHaltSourceReasons
	assetHalts       [4]chainTelemAssetHalt
}

var chainTelemRails = buildChainTelemRails()

func buildChainTelemRails() []chainTelemRail {
	rails := make([]chainTelemRail, 0, len(common.AllChains))
	for _, chain := range common.AllChains {
		chainStr := chain.String()
		chainLabel := telemetry.NewLabel("chain", chainStr)

		rail := chainTelemRail{
			chain:          chain,
			haltKey:        fmt.Sprintf("Halt%sChain", chainStr),
			solvencyKey:    fmt.Sprintf("SolvencyHalt%sChain", chainStr),
			signingHaltKey: fmt.Sprintf(constants.MimirTemplateHaltSigning, chainStr),
			chainLabels:    []metrics.Label{chainLabel},
		}
		for i, reason := range chainTelemHaltSourceReasons {
			rail.haltSourceLabels[i] = []metrics.Label{telemetry.NewLabel("reason", reason), chainLabel}
		}
		for i, ah := range [4]struct {
			key      []string
			reason   string
			template string
		}{
			{chainTelemKeySecuredHalted, "deposit", constants.MimirTemplateSecuredAssetHaltDeposit},
			{chainTelemKeySecuredHalted, "withdraw", constants.MimirTemplateSecuredAssetHaltWithdraw},
			{chainTelemKeyTradeHalted, "deposit", constants.MimirTemplateTradeAssetHaltDeposit},
			{chainTelemKeyTradeHalted, "withdraw", constants.MimirTemplateTradeAssetHaltWithdraw},
		} {
			rail.assetHalts[i] = chainTelemAssetHalt{
				key:      ah.key,
				mimirKey: fmt.Sprintf(ah.template, chainStr),
				labels:   []metrics.Label{telemetry.NewLabel("reason", ah.reason), chainLabel},
			}
		}
		rails = append(rails, rail)
	}
	return rails
}

// chainTelemPreflightStatuses is every status NodeAccountPreflightCheck can
// return, with its label precomputed. NodeActive is deliberately absent - see
// the comment at the emission site.
var chainTelemPreflightStatuses, chainTelemPreflightLabels = func() ([4]NodeStatus, [4]metrics.Label) {
	statuses := [4]NodeStatus{NodeReady, NodeStandby, NodeWhiteListed, NodeDisabled}
	var labels [4]metrics.Label
	for i, s := range statuses {
		labels[i] = telemetry.NewLabel("status", s.String())
	}
	return statuses, labels
}()

// emitChainDetailTelemetry emits the per-chain rails, per-node liveness and upgrade
// quorum detail that the existing chain/node gauges collapse into a boolean or omit
// entirely. It detects one class of failure: the network has quietly stopped moving
// funds - for a chain, for a rail, or for a single node - while every existing
// dashboard stays green.
//
// Concretely: thornode_chain_halted collapses four very different causes (planned
// admin halt vs. an automated solvency halt, which means real fund divergence) into
// one boolean; HaltSigning is set automatically by the slasher's stolen-funds path
// and read only by bifrost, so all outbounds on a chain can stop with no metric
// moving; the secured-asset, trade-asset and WASM halts are separate mimir families
// with no coverage at all; a chain can lose every eligible inbound vault while
// /thorchain/inbound_addresses just returns a shorter list; and the network-consensus
// observation height keeps advancing while one node's bifrost falls behind.
//
// Strictly read-only: every accessor below is a Get/List/Is that touches no state.
func emitChainDetailTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()
	height := ctx.BlockHeight()

	// ---- global rails, read once ----

	// WASM global halt. NOTE the strict `>`: WasmMgr.checkGlobalHalt uses
	// `v > 0 && ctx.BlockHeight() > v`, unlike the secured/trade halts below which use
	// `v <= height`. Mirror it exactly or the gauge flips a block before behaviour does.
	if v, err := k.GetMimir(ctx, constants.MimirKeyWasmHaltGlobal); err == nil {
		telemetry.SetGauge(boolTelem(v > 0 && height > v), "thornode", "wasm", "halted")
	}

	// Secured assets back the stable reserve, so the global halt
	// disables far more than its name suggests. Predicate per SecuredAssetMgr.CheckHalt.
	if v, err := k.GetMimir(ctx, constants.MimirKeySecuredAssetHaltGlobal); err == nil {
		telemetry.SetGauge(boolTelem(v > 0 && v <= height), "thornode", "secured_asset", "halted")
	}

	// Trade accounts are switched off network-wide by config values - a different
	// mechanism from the height-gated per-chain mimirs in the loop below.
	telemetry.SetGaugeWithLabels(
		chainTelemKeyTradeAccountsEnabled,
		boolTelem(k.GetConfigInt64(ctx, constants.TradeAccountsEnabled) > 0),
		chainTelemLabelsTradeGlobal,
	)
	telemetry.SetGaugeWithLabels(
		chainTelemKeyTradeAccountsEnabled,
		boolTelem(k.GetConfigInt64(ctx, constants.TradeAccountsDepositEnabled) > 0),
		chainTelemLabelsTradeDeposit,
	)

	// Global signing halt, read once and reused for every chain: this short-circuits
	// the per-chain read in exactly the order bifrost's CheckSigningHalt uses.
	signingHaltedGlobally := false
	if v, err := k.GetMimir(ctx, "HALTSIGNING"); err == nil && v > 0 && v <= height {
		signingHaltedGlobally = true
	}

	// The first two keys IsChainHalted consults are global, so read them once here
	// rather than once per chain inside the loop. NodePauseChainGlobal is a
	// pause-until-height (>=), HaltChainGlobal halts from a height (>0 && <=).
	chainHaltedGlobally := false
	if v, err := k.GetMimir(ctx, "HaltChainGlobal"); err == nil && v > 0 && v <= height {
		chainHaltedGlobally = true
	}
	chainPausedGlobally := false
	if v, err := k.GetMimir(ctx, "NodePauseChainGlobal"); err == nil && v >= height {
		chainPausedGlobally = true
	}

	// ---- per-chain rails ----
	// Kept per-block on purpose: these are the liveness signals an operator pages
	// on, and the whole loop is tens of writes.
	for i := range chainTelemRails {
		rail := &chainTelemRails[i]
		lastHeight, err := k.GetLastChainHeight(ctx, rail.chain)
		if err != nil {
			ctx.Logger().Error("fail to get last chain height for chain detail telemetry", "chain", rail.chain, "error", err)
			continue
		}
		if lastHeight == 0 {
			// chain not observed on this network, don't emit a series for it
			continue
		}

		// WHICH of the four keys IsChainHalted consults is set. Mirrors
		// keeper/v1/keeper_halt.go exactly. The two global keys are read once above;
		// the two per-chain keys halt from a height (>0 && <=). All four reasons are
		// still emitted per chain so the label set stays uniform and
		// halt_source{chain="X"} explains halted{chain="X"} on its own.
		adminHalted := false
		if v, herr := k.GetMimir(ctx, rail.haltKey); herr == nil {
			adminHalted = v > 0 && v <= height
		}
		solvencyHalted := false
		if v, herr := k.GetMimir(ctx, rail.solvencyKey); herr == nil {
			solvencyHalted = v > 0 && v <= height
		}
		// Same index order as chainTelemHaltSourceReasons.
		haltSourceActive := [4]bool{chainHaltedGlobally, chainPausedGlobally, adminHalted, solvencyHalted}
		for j, labels := range rail.haltSourceLabels {
			telemetry.SetGaugeWithLabels(chainTelemKeyHaltSource, boolTelem(haltSourceActive[j]), labels)
		}

		// Outbound signing halt, invisible to every other metric: IsChainHalted never
		// reads HaltSigning and the only reader in the tree is bifrost. It is set
		// automatically by the slasher's stolen-funds path and by the vault-deficit
		// path, so the network can stop every outbound on a chain while all of the
		// halt gauges above stay 0 and the outbound queue piles up.
		signingHalted := signingHaltedGlobally
		if !signingHalted {
			v, serr := k.GetMimir(ctx, rail.signingHaltKey)
			if serr == nil && v > 0 && v <= height {
				signingHalted = true
			}
		}
		telemetry.SetGaugeWithLabels(chainTelemKeySigningHalted, boolTelem(signingHalted), rail.chainLabels)

		// Secured- and trade-asset halts are separate height-activated mimir families
		// from the chain halts above. Emit the evaluated predicate rather than the raw
		// mimir so the gauge flips exactly when behaviour flips. Withdraw is the one to
		// page on: a deposit halt only stops new inflow, a withdraw halt traps funds
		// already custodied in the asgard vaults.
		for _, ah := range rail.assetHalts {
			v, aerr := k.GetMimir(ctx, ah.mimirKey)
			if aerr != nil {
				continue
			}
			telemetry.SetGaugeWithLabels(ah.key, boolTelem(v > 0 && v <= height), ah.labels)
		}

		// Info gauge (always 1): the router contract every EVM inbound and outbound
		// flows through. Alert on the address changing, not on the value:
		//   count by (chain) (thornode_chain_router_info) > 1
		if cc, cerr := k.GetChainContract(ctx, rail.chain); cerr == nil && !cc.IsEmpty() {
			telemetry.SetGaugeWithLabels(
				chainTelemKeyRouterInfo,
				1,
				// The router value is not block-invariant, so this one slice is
				// still built here. Composite literal, so cap == len.
				[]metrics.Label{rail.chainLabels[0], telemetry.NewLabel("router", cc.Router.String())},
			)
		}
	}

	// Everything below opens a per-node, per-vault or upgrade-proposal iterator and
	// is the only expensive part of this function: the validator scan alone is a
	// second full walk of the node store in a block where emitEndBlockTelemetry
	// already walked it once. None of these signals move faster than a churn, so
	// they refresh once per 10 blocks (~60s) - comfortably inside the shipped
	// prometheus_retention_time of 600s, so no gauge expires between refreshes.
	// 30 blocks (~3 min), not 10: this is the heaviest single block of work in the
	// whole telemetry path - GetLastObserveHeight opens one IAVL prefix iterator PER
	// NODE (~120) and parses a chain string per entry (~1,440). A node's bifrost
	// falling behind is a multi-minute condition, so 3-minute resolution loses nothing
	// an operator would act on, and it cuts the amortised cost to a third.
	if !IsPeriodLastBlock(ctx, 30) {
		return
	}

	// ---- per-node churn queue and observation liveness ----
	// One validator list serves all three per-node metrics below.
	nodes, err := k.ListValidatorsWithBond(ctx)
	if err != nil {
		ctx.Logger().Error("fail to list validators for chain detail telemetry", "error", err)
	}
	vMgr := mgr.ValidatorMgr()
	for _, node := range nodes {
		// cosmos.AccAddress.String() is a mutex-guarded LRU lookup that bech32
		// encodes on a miss; it was previously called once per node per chain
		// (19x on mainnet). Once per node is enough.
		addr := node.NodeAddress.String()
		addrLabel := telemetry.NewLabel("node_address", addr)

		// LeaveScore > 0 IS the churn-out queue: findCountToRemove counts exactly this
		// and nextVaultNodeAccounts orders churn-outs by it. The existing
		// thornode_node_leave_score is a different signal - it only has a series when
		// the node has slash points, so a node marked for age, low bond or low version
		// never appears there at all.
		telemetry.SetGaugeWithLabels(
			chainTelemKeyMarkedToLeave,
			boolTelem(node.LeaveScore > 0),
			[]metrics.Label{addrLabel},
		)

		if node.Status == NodeActive {
			// Per-node, per-chain observation height. The network aggregate
			// (thornode_chain_last_observed_height) keeps advancing while an individual
			// node's bifrost silently falls behind; per-node lag is
			//   max by (chain) (...) - thornode_node_last_observed_height
			// which names the stalled node before it accrues slash points. One prefix
			// iterator per active node is the heaviest read here, hence the gate.
			heights, herr := k.GetLastObserveHeight(ctx, node.NodeAddress)
			if herr != nil {
				ctx.Logger().Error("fail to get last observe height for telemetry", "node", addr, "error", herr)
				continue
			}
			// One backing array per node instead of one slice allocation per
			// chain (19 on mainnet). Each write gets its own two-element window
			// carved out with the three-index form so cap == len - required,
			// because InmemSink RETAINS the slice it is handed and both the
			// cosmos wrapper and go-metrics append their own labels into any
			// spare capacity in place. The array is exactly sized so the appends
			// below never reallocate, and no window is ever written again once
			// handed over. It must NOT be reused for the next node: the previous
			// node's windows are still referenced by the sink.
			labelBuf := make([]metrics.Label, 0, 2*len(heights))
			for _, ch := range heights {
				n := len(labelBuf)
				labelBuf = append(labelBuf, addrLabel, telemetry.NewLabel("chain", ch.Chain.String()))
				telemetry.SetGaugeWithLabels(
					chainTelemKeyLastObservedHeight,
					float32(ch.Height),
					labelBuf[n:n+2:n+2],
				)
			}
			continue
		}

		if vMgr == nil {
			continue
		}
		// Why the standby pool is not producing churn-in candidates: no valid IP, no
		// registered pubkey set, bond below MinimumBondInRune, version below
		// MinJoinLast, jailed, in maintenance, or requested-to-leave. markReadyActors
		// computes exactly this and discards it. The error is only the human-readable
		// reason - the returned status is valid on every path - so it is deliberately
		// dropped rather than exposed as a label. Up to 4 store reads per node.
		preflight, _ := vMgr.NodeAccountPreflightCheck(ctx, node, mgr.GetConstants())
		// Zero-write every reachable status so a node moving between them clears its
		// old series immediately. NodeActive is never returned by this function; a
		// node that churns in stops being written here at all, and its last series
		// clears on the 600s prometheus expiration rather than by a zero-write -
		// writing four permanent zeros per active node is not worth avoiding that.
		// Same one-array-per-node windowing as the observation-height loop above.
		labelBuf := make([]metrics.Label, 0, 2*len(chainTelemPreflightStatuses))
		for i, s := range chainTelemPreflightStatuses {
			n := len(labelBuf)
			labelBuf = append(labelBuf, addrLabel, chainTelemPreflightLabels[i])
			telemetry.SetGaugeWithLabels(
				chainTelemKeyPreflightStatus,
				boolTelem(preflight == s),
				labelBuf[n:n+2:n+2],
			)
		}
	}

	// ---- inbound availability ----
	// Can users still deposit on this chain right now? /thorchain/inbound_addresses
	// silently returns a shorter list when a chain loses every eligible vault - a 200
	// with a missing entry, so nothing alerts - and thornode_chain_halted does not
	// cover it: an unhalted chain with no eligible vault is still dead.
	//
	// Deliberately does NOT call selectActiveVaultForChain: that routes through
	// GetMostSecure -> SortBySecurity, which rescans SigningTransactionPeriod blocks of
	// txout per vault per chain (~36k store reads). Availability is precisely what its
	// error branch tests - whether the candidate set is empty - so test that directly.
	activeVaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("fail to get active vaults for chain detail telemetry", "error", err)
		return
	}
	eligible := make(Vaults, 0, len(activeVaults))
	chains := make(common.Chains, 0, len(activeVaults))
	for _, vault := range activeVaults {
		chains = append(chains, vault.GetChains()...)
		// Both filters fail closed on a read error, exactly as their callers do, so an
		// unreadable vault is never reported as able to serve deposits.
		compromised, cerr := isVaultCompromised(ctx, k, vault.PubKey)
		if cerr != nil || compromised {
			continue
		}
		insolvent, ierr := isVaultInsolvent(ctx, k, vault)
		if ierr != nil || insolvent {
			continue
		}
		eligible = append(eligible, vault)
	}
	// The chain set comes from the active vaults' own chains rather than
	// common.AllChains, so chains this network never serves emit no permanent zero.
	for _, chain := range chains.Distinct() {
		if chain.Equals(common.THORChain) {
			continue
		}
		// preferredActiveVaultsForChain already drops any vault without a usable
		// address for the chain, so a non-empty result is the availability answer.
		telemetry.SetGaugeWithLabels(
			chainTelemKeyInboundAvailable,
			boolTelem(len(preferredActiveVaultsForChain(eligible, chain)) > 0),
			[]metrics.Label{telemetry.NewLabel("chain", chain.String())},
		)
	}

	// ---- upgrade proposal quorum ----
	// A pending hard-fork proposal sitting one or two votes short of 2/3 as its target
	// height approaches is a chain-halt risk, and today the data is reachable only via
	// a manual /thorchain/upgrade_proposals query. Read-only: UpgradeApprovedByMajority
	// only calls ListActiveValidators and the vote iterator. Never call
	// RemoveExpiredUpgradeProposals from here - that one writes.
	iter := k.GetUpgradeProposalIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		nameSplit := strings.Split(string(iter.Key()), "/")
		name := nameSplit[len(nameSplit)-1]

		var upgrade types.Upgrade
		if uerr := k.Cdc().Unmarshal(iter.Value(), &upgrade); uerr != nil {
			continue
		}
		nameLabel := []metrics.Label{telemetry.NewLabel("name", name)}
		telemetry.SetGaugeWithLabels(chainTelemKeyUpgradeHeight, float32(upgrade.Height), nameLabel)

		uq, qerr := keeperv1.UpgradeApprovedByMajority(ctx, k, name)
		if qerr != nil || uq == nil {
			continue
		}
		telemetry.SetGaugeWithLabels(chainTelemKeyUpgradeApproved, boolTelem(uq.Approved), nameLabel)
		// Additional active validators still needed for quorum; 0 once approved.
		telemetry.SetGaugeWithLabels(chainTelemKeyUpgradeQuorumMissing, float32(uq.NeededForQuorum), nameLabel)
	}
}
