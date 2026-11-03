package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	rand "math/rand/v2"
	"os"
	"strings"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

// XMRKeygenRecoveryActor requires the environment prepared by
// make test-simulation-xmr-keygen-recovery: bootstrap first, then delayed P2P
// and a short mocknet-only round-3 deadline on pig. Its log is mounted read-only
// so an ordinary successful ceremony cannot satisfy this regression.
type XMRKeygenRecoveryActor struct {
	Actor
	evidenceLog  string
	vaultsBefore map[string]bool
	readyVault   string
	readySince   time.Time
}

const (
	xmrKeygenJailReason        = "failed to vote keygen in time"
	xmrKeygenRecoveryStability = 30 * time.Second
)

func NewXMRKeygenRecoveryActor(rng *rand.Rand) *Actor {
	a := &XMRKeygenRecoveryActor{
		Actor:       *NewActor("XMRKeygenRecovery", rng),
		evidenceLog: os.Getenv("XMR_KEYGEN_RECOVERY_LOG"),
	}
	a.Timeout = 20 * time.Minute

	a.Ops = append(a.Ops, a.capturePreChurnVaults)
	a.Ops = append(a.Ops, a.startChurn)
	a.Ops = append(a.Ops, a.waitForKeygenRecovery)

	return &a.Actor
}

func (a *XMRKeygenRecoveryActor) capturePreChurnVaults(config *OpConfig) OpResult {
	if _, err := a.readRecoveryEvidence(); err != nil {
		return OpResult{Finish: true, Error: err}
	}
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vaults")
		return OpResult{Continue: false}
	}

	a.vaultsBefore = make(map[string]bool, len(vaults))
	for _, vault := range vaults {
		a.vaultsBefore[vault.GetPubKey()] = true
	}
	a.Log().Info().Int("vaults", len(a.vaultsBefore)).Msg("captured pre-churn vault set")
	return OpResult{Continue: true}
}

func (a *XMRKeygenRecoveryActor) startChurn(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	if err := keygenVoteJail(nodes); err != nil {
		return OpResult{Finish: true, Error: err}
	}

	activeNodes := 0
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes++
		}
	}
	if activeNodes > 1 {
		// The regression needs the 1->4 churn to schedule the monovault keygen; an
		// already-churned cluster cannot run it again deterministically.
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("cluster already churned (%d active nodes); reset the cluster to run the keygen recovery regression", activeNodes),
		}
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

	// Mirror the xmr-post-churn churn setup: expand 1->4 and keep the churn
	// retry beyond the FROST keygen timeout so a missed keygen gets retried.
	for _, mimir := range []struct {
		key   string
		value int64
	}{
		{"NUMBEROFNEWNODESPERCHURN", xmrPostChurnNewNodesPerChurn},
		{"CHURNRETRYINTERVAL", xmrPostChurnRetryInterval},
		{"HALTCHURNING", 0},
	} {
		msg := thorchain.NewMsgMimir(mimir.key, mimir.value, accAddr)
		txid, broadcastErr := node.Thorchain.Broadcast(msg)
		if broadcastErr != nil {
			a.Log().Error().Err(broadcastErr).Str("key", mimir.key).Msg("failed to broadcast mimir")
			return OpResult{Continue: false}
		}
		a.Log().Info().
			Str("key", mimir.key).
			Int64("value", mimir.value).
			Str("txid", txid.String()).
			Msg("broadcasted mimir")
	}

	a.Log().Info().Msg("churn unhalted; requiring round-3 timeout, locally finalized quorum adoption, four successful results, and stable vault activation without keygen-vote jail")
	return OpResult{Continue: true}
}

// Use the wire metric shape: tss_time is emitted as a JSON number by THORNode.
// json.Number also accepts quoted numbers from API encoders.
type xmrRecoveryMetric struct {
	PubKey       string `json:"pub_key"`
	NodeTssTimes []struct {
		Address string      `json:"address"`
		TssTime json.Number `json:"tss_time"`
	} `json:"node_tss_times"`
}

type xmrRecoveryEvidence struct {
	Ceremony string
	Address  string
}

func (a *XMRKeygenRecoveryActor) readRecoveryEvidence() ([]xmrRecoveryEvidence, error) {
	if a.evidenceLog == "" {
		return nil, fmt.Errorf("XMR_KEYGEN_RECOVERY_LOG is required; run make test-simulation-xmr-keygen-recovery")
	}
	data, err := os.ReadFile(a.evidenceLog)
	if err != nil {
		return nil, fmt.Errorf("read pig's keygen recovery log: %w", err)
	}
	return parseXMRRecoveryEvidence(data), nil
}

// Read existing structured Bifrost logs, in order. Ignore unrelated lines and
// the incomplete last line while docker logs is appending. No production hook
// telemetry is needed; the timeout marker is gated by the mocknet override.
func parseXMRRecoveryEvidence(data []byte) []xmrRecoveryEvidence {
	overrides := make(map[string]bool)
	timeouts := make(map[string]bool)
	var recovered []xmrRecoveryEvidence
	for _, line := range bytes.Split(data, []byte("\n")) {
		var event struct {
			Message  string  `json:"message"`
			Ceremony string  `json:"msg_id"`
			Round    string  `json:"round"`
			Timeout  float64 `json:"round3_timeout"`
			Address  string  `json:"address"`
		}
		if json.Unmarshal(line, &event) != nil || strings.TrimSpace(event.Ceremony) == "" {
			continue
		}
		switch event.Message {
		case "TEST override active: XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS shrinks the round-3 wait":
			overrides[event.Ceremony] = event.Timeout > 0
		case "TEST injected XMR FROST round-3 timeout":
			if event.Round == "frost_dkg_round3" && overrides[event.Ceremony] {
				timeouts[event.Ceremony] = true
			}
		case "local frost keygen failed but quorum succeeded; adopted result with locally finalized keyshare":
			if timeouts[event.Ceremony] && strings.TrimSpace(event.Address) != "" {
				recovered = append(recovered, xmrRecoveryEvidence{event.Ceremony, event.Address})
			}
		}
	}
	return recovered
}

func keygenVoteJail(nodes []openapi.Node) error {
	for _, node := range nodes {
		if node.Jail.GetReason() == xmrKeygenJailReason {
			return fmt.Errorf("keygen-vote jail detected for %s; inspect XMR and Asgard ceremony logs", node.NodeAddress)
		}
	}
	return nil
}

func (a *XMRKeygenRecoveryActor) pendingRecovery() OpResult {
	a.readyVault = ""
	a.readySince = time.Time{}
	return OpResult{}
}

func (a *XMRKeygenRecoveryActor) waitForKeygenRecovery(config *OpConfig) OpResult {
	// Always check jail first, even before a vault exists or another query fails.
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return a.pendingRecovery()
	}
	if err = keygenVoteJail(nodes); err != nil {
		return OpResult{Finish: true, Error: err}
	}
	evidence, err := a.readRecoveryEvidence()
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vaults")
		return a.pendingRecovery()
	}
	var metrics []xmrRecoveryMetric
	for _, vault := range vaults {
		if a.vaultsBefore[vault.GetPubKey()] || vault.Status != thorchain.ActiveVault.String() {
			continue
		}
		for _, address := range vault.Addresses {
			if address.Chain != common.XMRChain.String() || strings.TrimSpace(address.Address) == "" {
				continue
			}
			var result []xmrRecoveryMetric
			url := fmt.Sprintf("%s/thorchain/metric/keygen/%s", thornode.BaseURL(), vault.GetPubKey())
			if err = thornode.Get(url, &result); err != nil {
				a.Log().Error().Err(err).Msg("failed to get keygen votes")
				return a.pendingRecovery()
			}
			metrics = append(metrics, result...)
		}
	}
	return a.checkKeygenRecovery(vaults, nodes, metrics, evidence, time.Now())
}

func recoveryVotesComplete(metric xmrRecoveryMetric, nodes []openapi.Node) bool {
	expected := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() && node.NodeAddress != "" {
			expected[node.NodeAddress] = true
		}
	}
	if len(expected) != 4 || len(metric.NodeTssTimes) != 4 {
		return false
	}
	for _, vote := range metric.NodeTssTimes {
		elapsed, err := vote.TssTime.Int64()
		if !expected[vote.Address] || err != nil || elapsed <= 0 {
			return false
		}
		delete(expected, vote.Address)
	}
	return len(expected) == 0
}

func (a *XMRKeygenRecoveryActor) checkKeygenRecovery(vaults []openapi.Vault, nodes []openapi.Node, metrics []xmrRecoveryMetric, evidence []xmrRecoveryEvidence, now time.Time) OpResult {
	if err := keygenVoteJail(nodes); err != nil {
		return OpResult{Finish: true, Error: err}
	}
	candidate, ceremony, recoveredAddress := "", "", ""
	for _, vault := range vaults {
		if a.vaultsBefore[vault.GetPubKey()] || vault.GetPubKey() == "" ||
			vault.Status != thorchain.ActiveVault.String() || len(vault.Membership) != 4 {
			continue
		}
		for _, recovered := range evidence {
			if !xmrVaultMatchesActiveNodes([]openapi.Vault{vault}, common.Address(recovered.Address), nodes) {
				continue
			}
			for _, metric := range metrics {
				if metric.PubKey == vault.GetPubKey() && recoveryVotesComplete(metric, nodes) {
					candidate, ceremony, recoveredAddress = vault.GetPubKey(), recovered.Ceremony, recovered.Address
				}
			}
		}
	}
	if candidate == "" {
		a.Log().Info().Int("recovered_ceremonies", len(evidence)).
			Msg("waiting for injected timeout, locally finalized adoption, four votes and matching active XMR vault")
		return a.pendingRecovery()
	}
	if a.readyVault != candidate {
		a.readyVault = candidate
		a.readySince = now
	}
	if now.Sub(a.readySince) < xmrKeygenRecoveryStability {
		return OpResult{}
	}
	a.Log().Info().Str("ceremony_id", ceremony).Str("new_xmr_vault", candidate).
		Str("xmr_address", recoveredAddress).Int("successful_results", 4).
		Msg("XMRKeygenRecovery PASSED: injected round-3 timeout, locally finalized quorum adoption, four successful results and stable active vault without keygen-vote jail")
	return OpResult{Continue: true}
}
