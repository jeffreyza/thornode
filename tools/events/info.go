package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/blang/semver"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v2"

	sdk "github.com/cosmos/cosmos-sdk/types"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/notify"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Scan Info
////////////////////////////////////////////////////////////////////////////////////////

func ScanInfo(block *thorscan.BlockResponse) {
	Version(block)
	Churn(block)
	SetNodeMimir(block)
	SetMimir(block)
	KeygenFailure(block)
	TORAnchorDrift(block)
	UpgradeProposalAndApproval(block)
	TssKeysignFailure(block)
}

////////////////////////////////////////////////////////////////////////////////////////
// Version
////////////////////////////////////////////////////////////////////////////////////////

func Version(block *thorscan.BlockResponse) {
	for _, event := range block.BeginBlockEvents {
		if event["type"] == types.VersionEventType {
			msg := fmt.Sprintf("Network Version Upgraded: `%s`", event["version"])
			notify.Notify(config.Get().Notifications.Info, msg, block.Header.Height, nil, notify.Success, nil)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Set Mimir
////////////////////////////////////////////////////////////////////////////////////////

func SetMimir(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] != types.SetMimirEventType {
				continue
			}

			// if the transaction does not contain mimir message it auto triggered
			source := "auto"

			// determine if this is a node mimir
			for _, msg := range tx.Tx.GetMsgs() {
				if _, ok := msg.(*thorchain.MsgMimir); ok {
					source = "node"
					break
				}
			}

			var msg string
			level := notify.Success
			switch event["key"] {
			case "NODEPAUSECHAINGLOBAL":
				msg, level = formatNodePauseMessage(block.Header.Height, tx, event)
			default:
				msg = formatMimirMessage(block.Header.Height, source, event["key"], event["value"])
			}

			notify.Notify(config.Get().Notifications.Info, msg, block.Header.Height, nil, level, nil)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Set Node Mimir
////////////////////////////////////////////////////////////////////////////////////////

func SetNodeMimir(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] == types.SetNodeMimirEventType {
				title, fields := formatNodeMimirMessage(block.Header.Height, event["address"], event["key"], event["value"])
				notify.Notify(config.Get().Notifications.Info, title, block.Header.Height, nil, notify.Info, fields)
			}
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Keygen Failure
////////////////////////////////////////////////////////////////////////////////////////

func KeygenFailure(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		// skip failed decode transactions
		if tx.Tx == nil {
			continue
		}

		for _, event := range tx.Result.Events {
			if event["type"] != types.TSSKeygenFailure {
				continue
			}

			// get nodes at keygen height
			heightStr := event["height"]
			height, err := strconv.ParseInt(heightStr, 10, 64)
			if err != nil {
				log.Panic().Err(err).Str("height", heightStr).Msg("failed to parse keygen height")
			}
			nodes := []openapi.Node{}
			err = util.ThornodeCachedRetryGet("thorchain/nodes", height, &nodes)
			if err != nil {
				log.Panic().Err(err).Msg("failed to get nodes")
			}

			// gather pubkey and operator mappings
			pubToAddr := make(map[string]string)
			addrToOperator := make(map[string]string)
			for _, node := range nodes {
				if node.PubKeySet.Secp256k1 == nil {
					continue
				}
				pubToAddr[*node.PubKeySet.Secp256k1] = node.NodeAddress
				addrToOperator[node.NodeAddress] = node.NodeOperatorAddress
			}

			// gather blame nodes
			blames := []string{}
			blameNodes := make(map[string]bool)
			for _, blame := range strings.Split(event["blame"], ",") {
				blame = strings.TrimSpace(blame)
				if blame == "" {
					continue
				}
				blames = append(blames, blame)
				blameNodes[blame] = true
			}

			// find tsspool message
			var msgTssPool *thorchain.MsgTssPool
			found := false
			for _, msg := range tx.Tx.GetMsgs() {
				if _, ok := msg.(*thorchain.MsgTssPool); ok {
					msgTssPool, _ = msg.(*thorchain.MsgTssPool)
					found = true
				}
			}
			if !found {
				log.Panic().Msg("failed to find tsspool message for keygen failure event")
			}

			// gather all members
			members := make(map[string]bool)
			for _, pk := range msgTssPool.PubKeys {
				members[pubToAddr[pk]] = true
			}

			// gather unblamed members
			others := make(map[string]bool)
			for member := range members {
				if blameNodes[member] {
					continue
				}
				others[member] = true
			}

			// build the blame and others strings
			blameStrs := []string{}
			for _, blame := range blames {
				blameStr := fmt.Sprintf(
					"`%s:%s`",
					addrToOperator[blame][len(addrToOperator[blame])-4:], blame[len(blame)-4:],
				)
				blameStrs = append(blameStrs, blameStr)
			}
			othersStrs := []string{}
			for other := range others {
				othersStrs = append(othersStrs, other[len(other)-4:])
			}

			// build the fields
			fields := util.NewOrderedMap()
			fields.Set("Keygen Height", event["height"])
			fields.Set("Reason", event["reason"])
			fields.Set("Blame", strings.Join(blameStrs, ", "))
			fields.Set("Others", fmt.Sprintf("`%s`", strings.Join(othersStrs, ", ")))

			// notify
			title := "Keygen Failure"
			notify.Notify(config.Get().Notifications.Info, title, block.Header.Height, nil, notify.Error, fields)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Churn
////////////////////////////////////////////////////////////////////////////////////////

type ChurnState int64

const (
	ChurnStateComplete ChurnState = iota
	ChurnStateKeygen
	ChurnStateMigrating
)

type ChurnInfo struct {
	State           ChurnState                 `json:"state"`
	KeyshareBackups map[string]map[string]bool `json:"keyshare_backups"`
}

func Churn(block *thorscan.BlockResponse) {
	// get the current state
	info := ChurnInfo{}
	err := util.Load("churn", &info)
	if err != nil {
		log.Debug().Err(err).Msg("failed to load churn state")
	}

	// track keyshare backups
	updated := false
	for _, tx := range block.Txs {
		// skip failed decode transactions
		if tx.Tx == nil {
			continue
		}

		for _, msg := range tx.Tx.GetMsgs() {
			msgTssPool, ok := msg.(*thorchain.MsgTssPool)
			if !ok {
				continue
			}

			// track keyshare backups
			if len(msgTssPool.KeysharesBackup) > 1 {
				pk := string(msgTssPool.PoolPubKey)
				if info.KeyshareBackups == nil {
					info.KeyshareBackups = make(map[string]map[string]bool)
				}
				if info.KeyshareBackups[pk] == nil {
					info.KeyshareBackups[pk] = make(map[string]bool)
				}
				updated = true
				info.KeyshareBackups[pk][msgTssPool.Signer.String()] = true
			}

			// track eddsa keyshare backups
			if len(msgTssPool.KeysharesBackupEddsa) > 1 {
				pk := string(msgTssPool.PoolPubKeyEddsa)
				if info.KeyshareBackups == nil {
					info.KeyshareBackups = make(map[string]map[string]bool)
				}
				if info.KeyshareBackups[pk] == nil {
					info.KeyshareBackups[pk] = make(map[string]bool)
				}
				updated = true
				info.KeyshareBackups[pk][msgTssPool.Signer.String()] = true
			}
		}
	}
	if updated {
		err = util.Store("churn", info)
		if err != nil {
			log.Panic().Err(err).Msg("failed to save churn state")
		}
	}

	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			switch event["type"] {
			case types.TSSKeygenMetricEventType: // check for keygen started
				if info.State == ChurnStateComplete {
					info.State = ChurnStateKeygen
					err = util.Store("churn", info)
					if err != nil {
						log.Panic().Err(err).Msg("failed to save churn state")
					}
					title := "Keygen Started"
					notify.Notify(config.Get().Notifications.Info, title, block.Header.Height, nil, notify.Info, nil)
				}
			case thorchain.EventTypeActiveVault: // check for active vault (keygens complete)
				// extract the keygen height from the TssPool message
				var msgTssPool *thorchain.MsgTssPool
				found := false
				for _, msg := range tx.Tx.GetMsgs() {
					if m, ok := msg.(*thorchain.MsgTssPool); ok {
						msgTssPool = m
						found = true
					}
				}
				if !found {
					log.Panic().Msg("failed to find tsspool message")
				}

				if info.State == ChurnStateKeygen {
					info.State = ChurnStateMigrating
					err = util.Store("churn", info)
					if err != nil {
						log.Panic().Err(err).Msg("failed to save churn state")
					}
					notifyChurnStarted(block.Header.Height, msgTssPool.Height, info.KeyshareBackups)
				}
			default:
				continue
			}
		}
	}

	// if migrating, check for completion on every block
	if info.State == ChurnStateMigrating && !vaultsMigrating(block.Header.Height) {
		// reset churn info for next churn
		info.State = ChurnStateComplete
		info.KeyshareBackups = make(map[string]map[string]bool)

		err = util.Store("churn", info)
		if err != nil {
			log.Panic().Err(err).Msg("failed to save churn state")
		}
		title := "Churn Complete"
		notify.Notify(config.Get().Notifications.Info, title, block.Header.Height, nil, notify.Success, nil)
	}
}

func vaultsMigrating(height int64) bool {
	network := openapi.NetworkResponse{}
	err := util.ThornodeCachedRetryGet("thorchain/network", height, &network)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get network")
	}
	return network.VaultsMigrating
}

func notifyChurnStarted(height, keygenHeight int64, keyshareBackups map[string]map[string]bool) {
	// get nodes at current and previous height
	oldNodes := []openapi.Node{}
	newNodes := []openapi.Node{}
	err := util.ThornodeCachedRetryGet("thorchain/nodes", keygenHeight, &oldNodes)
	if err != nil {
		log.Panic().Err(err).Int64("height", height-1).Msg("failed to get old nodes")
	}
	err = util.ThornodeCachedRetryGet("thorchain/nodes", height, &newNodes)
	if err != nil {
		log.Panic().Err(err).Int64("height", height).Msg("failed to get new nodes")
	}

	// determine the nodes that were removed
	oldActive := make(map[string]openapi.Node)
	newActive := make(map[string]openapi.Node)
	for _, oldNode := range oldNodes {
		if oldNode.Status != types.NodeStatus_Active.String() {
			continue
		}
		oldActive[oldNode.NodeAddress] = oldNode
	}
	for _, newNode := range newNodes {
		if newNode.Status != types.NodeStatus_Active.String() {
			continue
		}
		newActive[newNode.NodeAddress] = newNode
	}

	// determine the nodes that were added
	added := []openapi.Node{}
	for _, newNode := range newActive {
		if _, ok := oldActive[newNode.NodeAddress]; !ok {
			added = append(added, newNode)
		}
	}

	// determine the nodes that were removed
	left := []openapi.Node{}
	removed := []openapi.Node{}
	for _, oldNode := range oldActive {
		if _, ok := newActive[oldNode.NodeAddress]; ok {
			continue
		}
		if oldNode.LeaveHeight > 0 {
			if oldNode.LeaveHeight < height {
				left = append(left, oldNode)
			} else {
				removed = append(removed, oldNode)
			}
		}
	}

	// standby nodes
	oldActiveNodes := make([]openapi.Node, 0, len(oldActive))
	for _, node := range oldActive {
		oldActiveNodes = append(oldActiveNodes, node)
	}
	params := churnOutParams{
		MinSlashPointsForBadValidator:  util.GetConfigInt64(keygenHeight, constants.MinSlashPointsForBadValidator),
		BadValidatorRedline:            util.GetConfigInt64(keygenHeight, constants.BadValidatorRedline),
		DesiredValidatorSet:            util.GetConfigInt64(keygenHeight, constants.DesiredValidatorSet),
		MaxNodeToChurnOutForLowVersion: util.GetConfigInt64(keygenHeight, constants.MaxNodeToChurnOutForLowVersion),
		MissingBlockChurnOut:           util.GetConfigInt64(keygenHeight, constants.MissingBlockChurnOut),
		MaxMissingBlockChurnOut:        util.GetConfigInt64(keygenHeight, constants.MaxMissingBlockChurnOut),
	}

	// low version nodes are only marked once the minimum join version is settled
	version := openapi.VersionResponse{}
	err = util.ThornodeCachedRetryGet("thorchain/version", keygenHeight, &version)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get version")
	}
	minJoinSince := int64(0)
	if version.NextSinceHeight != nil {
		minJoinSince = *version.NextSinceHeight
	}
	if keygenHeight >= minJoinSince+util.GetConfigInt64(keygenHeight, constants.ChurnOutForLowVersionBlocks) {
		params.MinJoinVersion = version.Next
	}
	standbyNodes, removed := churnOutLabels(oldActiveNodes, removed, params)

	title := "Churn Started"

	// compute the keyshare backups counts for new vault members
	keyshareBackupLines := []string{}
	vaults := []openapi.Vault{}
	err = util.ThornodeCachedRetryGet("thorchain/vaults/asgard", height, &vaults)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get vaults")
	}
	for _, vault := range vaults {
		if vault.Status != types.VaultStatus_ActiveVault.String() {
			continue
		}
		if vault.PubKey == nil || vault.PubKeyEddsa == nil {
			continue
		}
		pk := *vault.PubKey
		eddsaPk := *vault.PubKeyEddsa
		keyshareBackupLines = append(
			keyshareBackupLines,
			fmt.Sprintf(
				"`%s` (ECDSA): %d/%d (%.2f%%)",
				pk[len(pk)-4:], len(keyshareBackups[pk]), len(vault.Membership),
				100*float64(len(keyshareBackups[pk]))/float64(len(vault.Membership)),
			),
		)
		keyshareBackupLines = append(
			keyshareBackupLines,
			fmt.Sprintf(
				"`%s` (EDDSA): %d/%d (%.2f%%)",
				eddsaPk[len(eddsaPk)-4:], len(keyshareBackups[eddsaPk]), len(vault.Membership),
				100*float64(len(keyshareBackups[eddsaPk]))/float64(len(vault.Membership)),
			),
		)
	}

	fields := util.NewOrderedMap()
	fields.Set("Keyshare Backups", strings.Join(keyshareBackupLines, "\n"))

	// active nodes
	if len(added) > 0 {
		activeNodes := []string{}
		for _, node := range added {
			activeNodes = append(activeNodes, fmt.Sprintf("`%s`", node.NodeAddress[len(node.NodeAddress)-4:]))
		}
		fields.Set("Active", strings.Join(activeNodes, ", "))
	}

	// standby nodes
	for _, node := range left {
		standbyNodes = append(standbyNodes, fmt.Sprintf("`%s` (leave)", node.NodeAddress[len(node.NodeAddress)-4:]))
	}
	for _, node := range removed {
		standbyNodes = append(standbyNodes, fmt.Sprintf("`%s`", node.NodeAddress[len(node.NodeAddress)-4:]))
	}
	fields.Set("Standby", strings.Join(standbyNodes, ", "))

	notify.Notify(config.Get().Notifications.Info, title, height, nil, notify.Success, fields)
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// churnOutParams are the network config values used to mark nodes for churn out.
type churnOutParams struct {
	MinSlashPointsForBadValidator  int64
	BadValidatorRedline            int64
	DesiredValidatorSet            int64
	MaxNodeToChurnOutForLowVersion int64
	MissingBlockChurnOut           int64
	MaxMissingBlockChurnOut        int64

	// MinJoinVersion is empty when low version nodes are not marked.
	MinJoinVersion string
}

// churnOutScore mirrors the validator manager score for the slash points, using a
// fixed number of blocks since the last churn as it is the same for all nodes.
func churnOutScore(slashPoints int64) cosmos.Uint {
	return cosmos.NewUint(43200 * common.One).QuoUint64(uint64(slashPoints))
}

// churnOutLabels returns the standby labels for the removed nodes with the reason
// they were churned out, and the removed nodes for which no reason was determined.
// The reasons mirror the order in which the validator manager marks active nodes
// (bad behavior, low bond, low version, age, missing blocks), where each mark skips the nodes
// that are already marked. They must be evaluated against the whole active set
// rather than only the removed nodes, since not every mark applies on every churn
// (the low bond mark requires a full active set) and a marked node may stay active.
func churnOutLabels(active, removed []openapi.Node, params churnOutParams) ([]string, []openapi.Node) {
	isRemoved := make(map[string]bool, len(removed))
	for _, node := range removed {
		isRemoved[node.NodeAddress] = true
	}

	// sort for determinism and prefer removed nodes on ties, since the removed node
	// is the one that was marked
	active = append([]openapi.Node{}, active...)
	sort.SliceStable(active, func(i, j int) bool {
		if isRemoved[active[i].NodeAddress] != isRemoved[active[j].NodeAddress] {
			return isRemoved[active[i].NodeAddress]
		}
		return active[i].NodeAddress < active[j].NodeAddress
	})

	// nodes that requested or were forced to leave keep their leave score
	reasons := make(map[string]string)
	for _, node := range active {
		switch {
		case node.RequestedToLeave:
			reasons[node.NodeAddress] = "leave"
		case node.ForcedToLeave:
			reasons[node.NodeAddress] = ""
		}
	}

	// bad behavior: all nodes over the redline, otherwise the worst offender
	if params.BadValidatorRedline > 0 {
		var worst *openapi.Node
		offenders := []openapi.Node{}
		totalScore := cosmos.ZeroUint()
		for i, node := range active {
			if node.SlashPoints <= params.MinSlashPointsForBadValidator {
				continue
			}
			offenders = append(offenders, node)
			totalScore = totalScore.Add(churnOutScore(node.SlashPoints))
			if worst == nil || node.SlashPoints > worst.SlashPoints {
				worst = &active[i]
			}
		}
		// lower score is worse
		redline := totalScore.QuoUint64(uint64(len(active))).QuoUint64(uint64(params.BadValidatorRedline))
		redlined := false
		for _, node := range offenders {
			if churnOutScore(node.SlashPoints).GT(redline) {
				continue
			}
			redlined = true
			if _, ok := reasons[node.NodeAddress]; !ok {
				reasons[node.NodeAddress] = "worst"
			}
		}
		if worst != nil && !redlined {
			if _, ok := reasons[worst.NodeAddress]; !ok {
				reasons[worst.NodeAddress] = "worst"
			}
		}
	}

	// mark sets the reason on the first unmarked node for which no other is less
	mark := func(reason string, less func(a, b openapi.Node) bool) {
		var found *openapi.Node
		for i, node := range active {
			if _, ok := reasons[node.NodeAddress]; ok {
				continue
			}
			if found == nil || less(node, *found) {
				found = &active[i]
			}
		}
		if found != nil {
			reasons[found.NodeAddress] = reason
		}
	}

	// low bond: only once the active set is at capacity
	if params.DesiredValidatorSet <= 0 || int64(len(active)) >= params.DesiredValidatorSet {
		mark("lowest bond", func(a, b openapi.Node) bool {
			return cosmos.NewUintFromString(a.TotalBond).LT(cosmos.NewUintFromString(b.TotalBond))
		})
	}

	// low version: up to the max in store (address) order
	if params.MinJoinVersion != "" && params.MaxNodeToChurnOutForLowVersion > 0 {
		minJoinVersion, err := semver.Make(params.MinJoinVersion)
		if err == nil {
			byAddress := append([]openapi.Node{}, active...)
			sort.SliceStable(byAddress, func(i, j int) bool {
				return byAddress[i].NodeAddress < byAddress[j].NodeAddress
			})
			count := int64(0)
			for _, node := range byAddress {
				if count >= params.MaxNodeToChurnOutForLowVersion {
					break
				}
				if _, ok := reasons[node.NodeAddress]; ok {
					continue
				}
				version, _ := semver.Make(node.Version)
				if version.LT(minJoinVersion) {
					reasons[node.NodeAddress] = "version"
					count++
				}
			}
		}
	}

	// age
	mark("oldest", func(a, b openapi.Node) bool {
		return a.StatusSince < b.StatusSince
	})

	// missing blocks
	if params.MissingBlockChurnOut > 0 && params.MaxMissingBlockChurnOut > 0 {
		for _, node := range active {
			if _, ok := reasons[node.NodeAddress]; ok {
				continue
			}
			if node.MissingBlocks > params.MissingBlockChurnOut {
				reasons[node.NodeAddress] = "missing"
			}
		}
	}

	labels := []string{}
	unknown := []openapi.Node{}
	for _, node := range active {
		if !isRemoved[node.NodeAddress] {
			continue
		}
		if reasons[node.NodeAddress] == "" {
			unknown = append(unknown, node)
			continue
		}
		labels = append(labels, fmt.Sprintf("`%s` (%s)", node.NodeAddress[len(node.NodeAddress)-4:], reasons[node.NodeAddress]))
	}
	return labels, unknown
}

func formatNodePauseMessage(height int64, tx thorscan.BlockTx, event map[string]string) (string, notify.Level) {
	legacyMsg, ok := tx.Tx.GetMsgs()[0].(sdk.LegacyMsg)
	if !ok {
		log.Panic().Msg("failed to cast to legacy message")
	}
	signer := legacyMsg.GetSigners()[0].String()
	pauseHeight, err := strconv.ParseInt(event["value"], 10, 64)
	if err != nil {
		log.Panic().Str("value", event["value"]).Err(err).Msg("failed to parse pause height")
	}

	action := fmt.Sprintf("**Node `%s` Unpaused**", signer[len(signer)-4:])
	level := notify.Success
	if height <= pauseHeight {
		level = notify.Danger
		action = fmt.Sprintf("**Node `%s` Paused**: %d blocks", signer[len(signer)-4:], pauseHeight-height)
	}

	return action, level
}

func formatMimirMessage(height int64, source, key, value string) string {
	// get value at previous height
	mimirs := make(map[string]int64)
	err := util.ThornodeCachedRetryGet("thorchain/mimir", height-1, &mimirs)
	if err != nil {
		log.Panic().Int64("height", height-1).Err(err).Msg("failed to get mimirs")
	}

	if previous, ok := mimirs[key]; ok {
		return fmt.Sprintf(
			"Mimir **`%s`** Changed: %d -> %s (%s)",
			key, previous, value, source,
		)
	}
	return fmt.Sprintf("Mimir **`%s`** Set: %s (%s)", key, value, source)
}

func formatNodeMimirMessage(height int64, node, key, value string) (string, *util.OrderedMap) {
	// convert value to int64
	valueInt, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		log.Panic().Err(err).Str("value", value).Msg("failed to parse value")
	}

	// get all active nodes at current height
	nodes := []openapi.Node{}
	err = util.ThornodeCachedRetryGet("thorchain/nodes", height, &nodes)
	if err != nil {
		log.Panic().Int64("height", height).Err(err).Msg("failed to get active nodes")
	}
	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == types.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}

	// get value at previous height
	mimirs := openapi.MimirNodesResponse{}
	err = util.ThornodeCachedRetryGet("thorchain/mimir/nodes_all", height-1, &mimirs)
	if err != nil {
		log.Panic().Int64("height", height-1).Err(err).Msg("failed to get node mimirs")
	}

	var previous *int64
	votes := make(map[int64]int64)
	for _, mimir := range mimirs.Mimirs {
		// skip if key or signer is nil
		if mimir.Key == nil || mimir.Signer == nil {
			continue
		}

		// skip votes that are not this key
		if *mimir.Key != key {
			continue
		}

		// TODO: fix in thornode - missing value in response is "0"
		value := int64(0)
		if mimir.Value != nil {
			value = *mimir.Value
		}

		// skip votes from non-active nodes
		if _, ok := activeNodes[*mimir.Signer]; !ok {
			continue
		}

		// see if there was a previous value
		if *mimir.Signer == node {
			previous = &value
			continue
		}

		// count the votes
		if _, ok := votes[value]; !ok {
			votes[value] = 1
		} else {
			votes[value]++
		}
	}

	// add the new vote
	votes[valueInt]++

	fields := util.NewOrderedMap()
	title := fmt.Sprintf("Node Mimir: `%s`", key)
	fields.Set("Node", fmt.Sprintf("`%s`", node[len(node)-4:]))
	valueStr := fmt.Sprintf("%d", valueInt)
	if previous != nil {
		valueStr += fmt.Sprintf(" (change from `%d`)", *previous)
	}
	fields.Set("Value", valueStr)

	// add the votes and validator count
	votesLines := []string{}
	for vote, count := range votes {
		percentage := float64(count) / float64(len(activeNodes)) * 100
		line := fmt.Sprintf("**`%d`**: %d/%d (%.1f%%)", vote, count, len(activeNodes), percentage)
		votesLines = append(votesLines, line)
	}
	fields.Set("Votes", strings.Join(votesLines, "\n"))

	return title, fields
}

////////////////////////////////////////////////////////////////////////////////////////
// TOR Anchor Drift
////////////////////////////////////////////////////////////////////////////////////////

// isChainTradingHalted mirrors keeper.IsChainTradingHalted using the mimir map
// returned by /thorchain/mimir (keys are upper-cased by the endpoint).
func isChainTradingHalted(mimirs map[string]int64, chain common.Chain, height int64) bool {
	if v := mimirs[fmt.Sprintf("HALT%sTRADING", chain)]; v > 0 && v <= height {
		return true
	}
	return isChainHalted(mimirs, chain, height)
}

// isChainHalted mirrors keeper.IsChainHalted.
func isChainHalted(mimirs map[string]int64, chain common.Chain, height int64) bool {
	if v := mimirs["HALTCHAINGLOBAL"]; v > 0 && v <= height {
		return true
	}
	if v := mimirs["NODEPAUSECHAINGLOBAL"]; v >= height {
		return true
	}
	if v := mimirs[fmt.Sprintf("HALT%sCHAIN", chain)]; v > 0 && v <= height {
		return true
	}
	if v := mimirs[fmt.Sprintf("SOLVENCYHALT%sCHAIN", chain)]; v > 0 && v <= height {
		return true
	}
	return false
}

func TORAnchorDrift(block *thorscan.BlockResponse) {
	if block.Header.Height%config.Get().TORAnchorCheckBlocks != 0 {
		return
	}

	// get mimirs
	mimirs := map[string]int64{}
	err := util.ThornodeCachedRetryGet("thorchain/mimir", block.Header.Height, &mimirs)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get mimirs")
	}

	// get pools
	pools := []openapi.Pool{}
	err = util.ThornodeCachedRetryGet("thorchain/pools", block.Header.Height, &pools)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get pools")
	}

	// find all TOR pools
	torPools := []openapi.Pool{}
	minPrice := cosmos.NewUint(common.One)
	maxPrice := cosmos.NewUint(common.One)
	for _, pool := range pools {
		var asset common.Asset
		asset, err = common.NewAsset(pool.Asset)
		if err != nil {
			log.Panic().Err(err).Msg("failed to parse pool asset")
		}
		if mimirs[fmt.Sprintf("TORANCHOR-%s", asset.MimirString())] == 0 {
			continue
		}
		// skip anchor pools on chains with trading halted — their stale
		// AssetTorPrice would otherwise trigger false-positive drift alerts.
		if isChainTradingHalted(mimirs, asset.Chain, block.Header.Height) {
			continue
		}
		price := cosmos.NewUintFromString(pool.AssetTorPrice)
		if price.LT(minPrice) {
			minPrice = price
		}
		if price.GT(maxPrice) {
			maxPrice = price
		}
		torPools = append(torPools, pool)
	}

	// skip if not over the drift threshold
	driftBPS := maxPrice.Sub(minPrice).MulUint64(constants.MaxBasisPts).Quo(maxPrice)
	if driftBPS.LT(cosmos.NewUint(config.Get().Thresholds.TORAnchorDriftBasisPoints)) {
		return
	}

	// sort tor pools by price
	sort.Slice(torPools, func(i, j int) bool {
		iPrice := cosmos.NewUintFromString(torPools[i].AssetTorPrice)
		jPrice := cosmos.NewUintFromString(torPools[j].AssetTorPrice)
		return iPrice.LT(jPrice)
	})

	// build notification
	fields := util.NewOrderedMap()
	maxFieldLenth := 0
	for _, pool := range torPools {
		shortAsset := strings.Split(pool.Asset, "-")[0]
		if len(shortAsset) > maxFieldLenth {
			maxFieldLenth = len(shortAsset)
		}
	}
	for _, pool := range torPools {
		shortAsset := strings.Split(pool.Asset, "-")[0]
		if len(shortAsset) < maxFieldLenth {
			shortAsset = strings.Repeat(" ", maxFieldLenth-len(shortAsset)) + shortAsset
		}
		price := float64(cosmos.NewUintFromString(pool.AssetTorPrice).Uint64()) / common.One
		fields.Set(shortAsset, util.FormatUSD(price))
	}

	title := fmt.Sprintf("TOR Anchor Drift (%.02f%%)", float64(driftBPS.Uint64())/100)
	notify.Notify(config.Get().Notifications.Info, title, block.Header.Height, nil, notify.Warning, fields)
}

////////////////////////////////////////////////////////////////////////////////////////
// Upgrade Proposal and Approval
////////////////////////////////////////////////////////////////////////////////////////

func UpgradeProposalAndApproval(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {

			fields := util.NewOrderedMap()
			lines := []string{}
			level := notify.Info
			var title string
			switch event["type"] {
			case "propose_upgrade":
				title = fmt.Sprintf("Upgrade Proposed: `%s`", event["name"])
				level = notify.Broadcast
				fields.Set("Height", fmt.Sprintf("[`%s`](%s/block/%s)", event["height"], config.Get().Links.Explorer, event["height"]))

				// decode info into yaml for notification
				rawInfo := event["info"]
				rawInfo = strings.ReplaceAll(rawInfo, "\\", "") // remove escapes
				info := map[string]any{}
				err := json.Unmarshal([]byte(rawInfo), &info)
				if err != nil {
					log.Error().Err(err).Msg("failed to decode event info")
					break
				}
				yamlInfo, err := yaml.Marshal(info)
				if err != nil {
					log.Error().Err(err).Msg("failed to marshal event info")
					break
				}
				lines = []string{fmt.Sprintf("```yaml\n%s\n```", yamlInfo)}

			case "approve_upgrade", "reject_upgrade":
				action := "Approved"
				if event["type"] == "reject_upgrade" {
					action = "Rejected"
					level = notify.Warning
				}
				title = fmt.Sprintf("Upgrade %s: `%s`", action, event["name"])

				// fetch proposal stats
				proposal := openapi.UpgradeProposal{}
				path := fmt.Sprintf("thorchain/upgrade_proposal/%s", event["name"])
				err := util.ThornodeCachedRetryGet(path, block.Header.Height, &proposal)
				if err != nil {
					log.Panic().Err(err).Msg("failed to get upgrade proposal")
				}
				if proposal.ApprovedPercent == nil || proposal.ValidatorsToQuorum == nil {
					break
				}
				if *proposal.ValidatorsToQuorum == 0 {
					level = notify.Success
				}

				// add progress fields
				percent, err := strconv.ParseFloat(*proposal.ApprovedPercent, 64)
				if err != nil {
					log.Panic().Err(err).Msg("failed to parse approved percent")
				}
				fields.Set("Approval Percent", fmt.Sprintf("%.1f%%", percent))
				fields.Set("Approvals Required", fmt.Sprintf("%d", *proposal.ValidatorsToQuorum))

			default:
				continue
			}

			fields.Set("Node", fmt.Sprintf("`%s`", event["thor_address"][len(event["thor_address"])-4:]))
			notify.Notify(config.Get().Notifications.Info, title, block.Header.Height, lines, level, fields)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// TSS Keysign Failure
////////////////////////////////////////////////////////////////////////////////////////

// terminalKeysignRounds mirrors the acceptance set in the TssKeysignHandler
// freeze path: a consensus failure at one of these rounds means enough signing
// material may have escaped that the network may freeze the vault. Used only
// to annotate the round histogram in rate alerts — the per-report terminal
// Security alert is owned by LastRoundFailures in security.go.
var terminalKeysignRounds = map[string]bool{
	tssMessages.KEYSIGN9:           true,
	tssMessages.EDDSAKEYSIGN3:      true,
	tssMessages.FROSTKEYSIGNSHARE:  true,
	tssMessages.FROSTKEYSIGNRESULT: true,
}

// maxBlameSummaryNodes caps the number of blamed nodes listed in rate alerts.
const maxBlameSummaryNodes = 5

// TssKeysignFailureRecord is one confirmed keysign failure within the window.
type TssKeysignFailureRecord struct {
	Height int64    `json:"height"`
	Round  string   `json:"round"`
	Blame  []string `json:"blame,omitempty"`
	// ID is the MsgTssKeysignFail consensus id from the event, used to keep
	// failure accounting idempotent when a block is re-scanned after a crash.
	ID string `json:"id,omitempty"`
}

type TssKeysignFailureHistory struct {
	VaultFailures      map[string][]TssKeysignFailureRecord `json:"vault_failures"`
	VaultPastCounts    map[string][]int                     `json:"vault_past_counts"`
	NetworkPastCounts  []int                                `json:"network_past_counts"`
	LastSnapshotHeight int64                                `json:"last_snapshot_height"`
}

func TssKeysignFailure(block *thorscan.BlockResponse) {
	// load history
	history := TssKeysignFailureHistory{}
	err := util.Load("tss_keysign_failure", &history)
	if err != nil {
		log.Debug().Err(err).Msg("failed to load tss keysign failure history")
	}
	if history.VaultFailures == nil {
		history.VaultFailures = make(map[string][]TssKeysignFailureRecord)
	}
	if history.VaultPastCounts == nil {
		history.VaultPastCounts = make(map[string][]int)
	}
	if history.LastSnapshotHeight == 0 {
		history.LastSnapshotHeight = block.Header.Height
	}

	// constants
	window := config.Get().Thresholds.TssKeysignFailure.WindowBlocks
	sigma := config.Get().Thresholds.TssKeysignFailure.Sigma
	minSnapshots := config.Get().Thresholds.TssKeysignFailure.MinSnapshots
	maxSnapshots := config.Get().Thresholds.TssKeysignFailure.MaxSnapshots
	minFailures := config.Get().Thresholds.TssKeysignFailure.MinFailures

	// capture previous counts for edge trigger
	prevVaultCounts := make(map[string]int)
	prevNetworkCount := 0
	for k, v := range history.VaultFailures {
		prevVaultCounts[k] = len(v)
		prevNetworkCount += len(v)
	}

	// track current block failures — the event is self-contained (vault
	// pubkey, canonical round and blamed nodes are attributes), no need to
	// decode the tx messages
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] != types.TSSKeysignFailure {
				continue
			}

			vaultPubKey := event["pubkey"]
			if vaultPubKey == "" {
				log.Error().Msg("tss_keysign_failure event missing vault pubkey")
				continue
			}

			record := TssKeysignFailureRecord{
				Height: block.Header.Height,
				Round:  event["round"],
				Blame:  parseKeysignBlameNodes(event["blame"]),
				ID:     event["id"],
			}

			// skip failures already recorded in the window — history is
			// persisted before the scan height cursor, so a crash can replay
			// a block, and the consensus id is stable per failure instance
			duplicate := false
			if record.ID != "" {
				for _, f := range history.VaultFailures[vaultPubKey] {
					if f.ID == record.ID {
						duplicate = true
						break
					}
				}
			}
			if duplicate {
				continue
			}

			history.VaultFailures[vaultPubKey] = append(history.VaultFailures[vaultPubKey], record)
		}
	}

	// prune history (moving window)
	prune := func(failures []TssKeysignFailureRecord) []TssKeysignFailureRecord {
		valid := []TssKeysignFailureRecord{}
		for _, f := range failures {
			if block.Header.Height-f.Height <= window {
				valid = append(valid, f)
			}
		}
		return valid
	}

	// snapshot counts for statistics every window
	if block.Header.Height-history.LastSnapshotHeight >= window {
		networkCount := 0
		for _, failures := range history.VaultFailures {
			networkCount += len(failures)
		}
		history.NetworkPastCounts = append(history.NetworkPastCounts, networkCount)
		if len(history.NetworkPastCounts) > maxSnapshots {
			history.NetworkPastCounts = history.NetworkPastCounts[1:]
		}

		for pubKey, failures := range history.VaultFailures {
			history.VaultPastCounts[pubKey] = append(history.VaultPastCounts[pubKey], len(failures))
			if len(history.VaultPastCounts[pubKey]) > maxSnapshots {
				history.VaultPastCounts[pubKey] = history.VaultPastCounts[pubKey][1:]
			}
		}
		history.LastSnapshotHeight = block.Header.Height
	}

	// helper to calculate threshold
	getStatsThreshold := func(history []int) (float64, bool) {
		if len(history) < minSnapshots { // need minimum history for meaningful stats
			return 0, false
		}
		sum := 0
		for _, v := range history {
			sum += v
		}
		mean := float64(sum) / float64(len(history))

		variance := 0.0
		for _, v := range history {
			variance += (float64(v) - mean) * (float64(v) - mean)
		}
		stdDev := math.Sqrt(variance / float64(len(history)))

		return mean + (sigma * stdDev), true
	}

	// helper to apply the absolute floor, so crossing the alert threshold
	// requires at least minFailures in the window
	alertThreshold := func(statsThreshold float64) float64 {
		return math.Max(statsThreshold, float64(minFailures-1))
	}

	// prune vault failures and gather the current network window
	networkFailures := []TssKeysignFailureRecord{}
	for pubKey, failures := range history.VaultFailures {
		history.VaultFailures[pubKey] = prune(failures)
		networkFailures = append(networkFailures, history.VaultFailures[pubKey]...)
	}
	currNetworkCount := len(networkFailures)

	// check network alert
	netStatsThreshold, netStatsOk := getStatsThreshold(history.NetworkPastCounts)
	if netStatsOk {
		netThreshold := alertThreshold(netStatsThreshold)
		// analyze-ignore(float-comparison)
		if float64(currNetworkCount) > netThreshold && float64(prevNetworkCount) <= netThreshold {
			notifyTSSKeysignFailure(block.Header.Height, "Network High TSS Keysign Failure Rate", "", currNetworkCount, int(netStatsThreshold), networkFailures)
		}
	}

	// check vault alerts
	for pubKey, failures := range history.VaultFailures {
		prevVaultCount := prevVaultCounts[pubKey]
		currVaultCount := len(failures)

		// calculate threshold: use vault stats if available, else fallback to network stats
		threshold, ok := getStatsThreshold(history.VaultPastCounts[pubKey])
		if !ok {
			threshold, ok = netStatsThreshold, netStatsOk
		}

		// only alert if we have a valid threshold (from vault or network)
		if ok {
			vaultThreshold := alertThreshold(threshold)
			// analyze-ignore(float-comparison)
			if float64(currVaultCount) > vaultThreshold && float64(prevVaultCount) <= vaultThreshold {
				notifyTSSKeysignFailure(block.Header.Height, "Vault High TSS Keysign Failure Rate", pubKey, currVaultCount, int(threshold), failures)
			}
		}
	}

	// save history
	err = util.Store("tss_keysign_failure", history)
	if err != nil {
		log.Error().Err(err).Msg("failed to save tss keysign failure history")
	}
}

// parseKeysignBlameNodes splits the event blame attribute (pubkeys joined by
// ", ") into a list of blamed node pubkeys.
func parseKeysignBlameNodes(blame string) []string {
	nodes := []string{}
	for _, node := range strings.Split(blame, ",") {
		node = strings.TrimSpace(node)
		if node == "" {
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// shortKeysignPubKey renders the conventional last-4-character node shorthand.
func shortKeysignPubKey(pk string) string {
	if len(pk) <= 4 {
		return pk
	}
	return pk[len(pk)-4:]
}

// summarizeKeysignRounds renders a per-round histogram of the given failures.
func summarizeKeysignRounds(failures []TssKeysignFailureRecord) string {
	counts := map[string]int{}
	for _, f := range failures {
		round := f.Round
		if round == "" {
			round = "unknown"
		}
		counts[round]++
	}
	if len(counts) == 0 {
		return ""
	}
	type roundCount struct {
		round string
		count int
	}
	sorted := []roundCount{}
	for round, count := range counts {
		sorted = append(sorted, roundCount{round, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].round < sorted[j].round
	})
	lines := []string{}
	for _, rc := range sorted {
		line := fmt.Sprintf("`%s`: %d", rc.round, rc.count)
		if terminalKeysignRounds[rc.round] {
			line += " (terminal)"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// summarizeKeysignBlame renders the most-blamed nodes across the given failures.
func summarizeKeysignBlame(failures []TssKeysignFailureRecord) string {
	counts := map[string]int{}
	for _, f := range failures {
		for _, node := range f.Blame {
			counts[node]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	type blameCount struct {
		node  string
		count int
	}
	sorted := []blameCount{}
	for node, count := range counts {
		sorted = append(sorted, blameCount{node, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].node < sorted[j].node
	})
	lines := []string{}
	for i, bc := range sorted {
		if i == maxBlameSummaryNodes {
			lines = append(lines, fmt.Sprintf("…and %d more", len(sorted)-maxBlameSummaryNodes))
			break
		}
		lines = append(lines, fmt.Sprintf("`%s`: %d", shortKeysignPubKey(bc.node), bc.count))
	}
	return strings.Join(lines, "\n")
}

func notifyTSSKeysignFailure(height int64, title, pubKey string, count, threshold int, failures []TssKeysignFailureRecord) {
	fields := util.NewOrderedMap()
	if pubKey != "" {
		fields.Set("Vault", fmt.Sprintf("`%s`", shortKeysignPubKey(pubKey)))
	}
	fields.Set("Failures (window)", fmt.Sprintf("%d", count))
	fields.Set("Expected Rate (Avg + Nσ)", fmt.Sprintf("%d", threshold))
	if rounds := summarizeKeysignRounds(failures); rounds != "" {
		fields.Set("Rounds", rounds)
	}
	if blame := summarizeKeysignBlame(failures); blame != "" {
		fields.Set("Blame", blame)
	}

	// rate alerts are informational and do not tag @here; terminal-round
	// failures are escalated separately by LastRoundFailures in security.go
	notify.Notify(config.Get().Notifications.Info, title, height, nil, notify.Warning, fields)
}
