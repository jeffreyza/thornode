package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

// churnScheduleLeadBlocks is how far ahead ScheduleChurn places the churn. Every
// vote has to land before that height. A churn scheduled for a height that has
// already passed waits for the next ChurnRetryInterval tick instead, which is
// how the simulation churned before it scheduled churns. The votes land 1-2
// blocks after they are sent, so 8 keeps a 4x margin; at 20 each churn idled
// ~25s before starting.
const churnScheduleLeadBlocks = int64(8)

// ScheduleChurn sets the CHURNINTERVAL Mimir so the next regular churn fires
// churnScheduleLeadBlocks from now, and returns that height.
//
// A churn only starts at lastChurn+ChurnInterval, or on a ChurnRetryInterval
// tick after it (x/thorchain/manager_validator_current.go). By the time the
// simulation unhalts churning or requests a leave, the regular height has
// passed, so the churn used to wait for the next 365-block retry tick: 3-10 min
// per churn in CI. CHURNINTERVAL needs a supermajority of active nodes, so every
// active simulation node votes for the same value.
func ScheduleChurn(logger *zerolog.Logger, config *OpConfig) (int64, error) {
	nodes, err := thornode.GetNodes()
	if err != nil {
		return 0, fmt.Errorf("get nodes: %w", err)
	}
	active := map[string]bool{}
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			active[node.NodeAddress] = true
		}
	}

	var voters []*User
	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return 0, fmt.Errorf("get node thor address: %w", err)
		}
		if active[accAddr.String()] {
			voters = append(voters, node)
		}
	}
	if len(voters) == 0 {
		return 0, fmt.Errorf("no simulation node is active")
	}

	currentHeight, err := voters[0].Thorchain.GetBlockHeight()
	if err != nil {
		return 0, fmt.Errorf("get thorchain block height: %w", err)
	}
	vaults, err := thornode.GetVaults()
	if err != nil {
		return 0, fmt.Errorf("get vaults: %w", err)
	}
	lastChurnHeight, ok := lastActiveVaultHeight(vaults)
	if !ok {
		return 0, fmt.Errorf("no active vault to take the last churn height from")
	}
	interval := scheduledChurnInterval(currentHeight, lastChurnHeight, churnScheduleLeadBlocks)

	for _, node := range voters {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return 0, fmt.Errorf("get node thor address: %w", err)
		}
		msg := thorchain.NewMsgMimir(constants.ChurnInterval.String(), interval, accAddr)
		if _, err = node.Thorchain.Broadcast(msg); err != nil {
			return 0, fmt.Errorf("broadcast %s=%d from %s: %w", constants.ChurnInterval, interval, accAddr, err)
		}
	}

	scheduledHeight := lastChurnHeight + interval
	waitChurnIntervalApplied(logger, voters[0], interval, scheduledHeight)
	logger.Info().
		Int64("churn_interval", interval).
		Int64("current_height", currentHeight).
		Int64("last_churn_height", lastChurnHeight).
		Int64("scheduled_height", scheduledHeight).
		Int("votes", len(voters)).
		Msg("scheduled churn")
	return scheduledHeight, nil
}

// scheduledChurnInterval returns the ChurnInterval that puts the next regular
// churn lead blocks after currentHeight.
func scheduledChurnInterval(currentHeight, lastChurnHeight, lead int64) int64 {
	interval := currentHeight - lastChurnHeight + lead
	if interval < lead {
		return lead
	}
	return interval
}

// lastActiveVaultHeight returns the last churn height the way the churn trigger
// computes it (getLastChurnHeight): the highest status_since of an active vault.
func lastActiveVaultHeight(vaults []openapi.Vault) (int64, bool) {
	var lastChurnHeight int64
	found := false
	for _, vault := range vaults {
		if vault.Status != thorchain.ActiveVault.String() {
			continue
		}
		// The genesis vault has status_since=0, which the API omits from JSON.
		// It is still an active vault and must allow scheduling the first churn.
		statusSince := vault.GetStatusSince()
		if !found || statusSince > lastChurnHeight {
			lastChurnHeight = statusSince
			found = true
		}
	}
	return lastChurnHeight, found
}

// waitChurnIntervalApplied waits until the CHURNINTERVAL votes take effect, and
// warns when they have not by the scheduled height, in which case the churn
// waits for the next ChurnRetryInterval tick. It does not reschedule: the churn
// may already be starting, and a second schedule could trigger another churn.
func waitChurnIntervalApplied(logger *zerolog.Logger, node *User, interval, scheduledHeight int64) {
	key := strings.ToUpper(constants.ChurnInterval.String())
	for {
		mimirs, err := thornode.GetMimirs()
		if err == nil && mimirs[key] == interval {
			return
		}
		height, err := node.Thorchain.GetBlockHeight()
		if err == nil && height >= scheduledHeight {
			logger.Warn().
				Int64("height", height).
				Int64("scheduled_height", scheduledHeight).
				Int64("churn_interval", interval).
				Msg("churn interval votes did not apply before the scheduled height; the churn will wait for ChurnRetryInterval")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}
