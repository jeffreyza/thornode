package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	xmrFrostReadinessHTTPTimeout = 2 * time.Second
	defaultXMRDaemonRPC          = "http://127.0.0.1:18081/json_rpc"
)

type readinessEndpoint struct {
	name string
	url  string
}

type xmrFrostChurnReadinessDeps struct {
	nodeAddresses    func(*OpConfig) ([]common.Address, error)
	nodes            func() ([]openapi.Node, error)
	httpGet          func(string) (*http.Response, error)
	moneroHeight     func(string) (int64, error)
	bifrostEndpoints []readinessEndpoint
	frostEndpoints   []readinessEndpoint
	moneroRPC        string
}

type xmrFrostChurnReadinessResult struct {
	ready              bool
	reason             string
	endpointName       string
	endpointURL        string
	err                error
	pendingValidators  []string
	readyValidators    int
	expectedValidators int
	moneroHeight       int64
}

var xmrFrostReadinessHTTPClient = &http.Client{Timeout: xmrFrostReadinessHTTPTimeout}

func waitForXMRFrostChurnReadiness(logger *zerolog.Logger, config *OpConfig) OpResult {
	result := checkXMRFrostChurnReadiness(config, defaultXMRFrostChurnReadinessDeps())
	if result.ready {
		logger.Info().
			Int("validators", result.readyValidators).
			Int64("monero_height", result.moneroHeight).
			Msg("xmr frost churn readiness gate passed")
		return OpResult{Continue: true}
	}

	event := logger.Info().
		Str("reason", result.reason).
		Int("ready_validators", result.readyValidators).
		Int("expected_validators", result.expectedValidators)
	if result.endpointName != "" {
		event = event.Str("endpoint", result.endpointName).Str("url", result.endpointURL)
	}
	if len(result.pendingValidators) > 0 {
		event = event.Strs("pending_validators", result.pendingValidators)
	}
	if result.err != nil {
		event = event.Err(result.err)
	}
	event.Msg("waiting for xmr frost churn readiness")

	return OpResult{Continue: false}
}

func defaultXMRFrostChurnReadinessDeps() xmrFrostChurnReadinessDeps {
	return xmrFrostChurnReadinessDeps{
		nodeAddresses: defaultNodeAddresses,
		nodes:         thornode.GetNodes,
		httpGet:       xmrFrostReadinessHTTPClient.Get,
		moneroHeight:  getMoneroDaemonBlockCount,
		bifrostEndpoints: readinessEndpointsFromEnv("SIMULATION_BIFROST_READINESS_ENDPOINTS", []readinessEndpoint{
			{name: "dog-bifrost", url: "http://127.0.0.1:6040/p2pid"},
			{name: "cat-bifrost", url: "http://127.0.0.1:6041/p2pid"},
			{name: "fox-bifrost", url: "http://127.0.0.1:6042/p2pid"},
			{name: "pig-bifrost", url: "http://127.0.0.1:6043/p2pid"},
			{name: "frog-bifrost", url: "http://127.0.0.1:6044/p2pid"},
		}),
		frostEndpoints: readinessEndpointsFromEnv("SIMULATION_XMR_FROST_READINESS_ENDPOINTS", []readinessEndpoint{
			{name: "dog-xmr-frost", url: "http://127.0.0.1:8283/health"},
			{name: "cat-xmr-frost", url: "http://127.0.0.1:8285/health"},
			{name: "fox-xmr-frost", url: "http://127.0.0.1:8286/health"},
			{name: "pig-xmr-frost", url: "http://127.0.0.1:8287/health"},
			{name: "frog-xmr-frost", url: "http://127.0.0.1:8288/health"},
		}),
		moneroRPC: envOrDefault("SIMULATION_XMR_DAEMON_RPC", defaultXMRDaemonRPC),
	}
}

func checkXMRFrostChurnReadiness(config *OpConfig, deps xmrFrostChurnReadinessDeps) xmrFrostChurnReadinessResult {
	expectedAddresses, err := deps.nodeAddresses(config)
	if err != nil {
		return xmrFrostChurnReadinessResult{reason: "validator address lookup failed", err: err}
	}

	expected := len(expectedAddresses)
	nodes, err := deps.nodes()
	if err != nil {
		return xmrFrostChurnReadinessResult{
			reason:             "thornode node lookup failed",
			err:                err,
			expectedValidators: expected,
		}
	}

	readyValidators, pendingValidators := readyXMRChurnValidators(expectedAddresses, nodes)
	if readyValidators < expected {
		return xmrFrostChurnReadinessResult{
			reason:             "validator nodes not ready",
			pendingValidators:  pendingValidators,
			readyValidators:    readyValidators,
			expectedValidators: expected,
		}
	}

	for _, endpoint := range deps.bifrostEndpoints {
		if err := httpEndpointReady(deps.httpGet, endpoint.url); err != nil {
			return xmrFrostChurnReadinessResult{
				reason:             "bifrost endpoint not ready",
				endpointName:       endpoint.name,
				endpointURL:        endpoint.url,
				err:                err,
				readyValidators:    readyValidators,
				expectedValidators: expected,
			}
		}
	}

	for _, endpoint := range deps.frostEndpoints {
		if err := httpEndpointReady(deps.httpGet, endpoint.url); err != nil {
			return xmrFrostChurnReadinessResult{
				reason:             "xmr frost endpoint not ready",
				endpointName:       endpoint.name,
				endpointURL:        endpoint.url,
				err:                err,
				readyValidators:    readyValidators,
				expectedValidators: expected,
			}
		}
	}

	height, err := deps.moneroHeight(deps.moneroRPC)
	if err != nil {
		return xmrFrostChurnReadinessResult{
			reason:             "monero daemon height unavailable",
			endpointName:       "monero-daemon",
			endpointURL:        deps.moneroRPC,
			err:                err,
			readyValidators:    readyValidators,
			expectedValidators: expected,
		}
	}
	if height <= 0 {
		return xmrFrostChurnReadinessResult{
			reason:             "monero daemon height is zero",
			endpointName:       "monero-daemon",
			endpointURL:        deps.moneroRPC,
			readyValidators:    readyValidators,
			expectedValidators: expected,
			moneroHeight:       height,
		}
	}

	return xmrFrostChurnReadinessResult{
		ready:              true,
		readyValidators:    readyValidators,
		expectedValidators: expected,
		moneroHeight:       height,
	}
}

func defaultNodeAddresses(config *OpConfig) ([]common.Address, error) {
	if config == nil || len(config.NodeUsers) == 0 {
		return nil, fmt.Errorf("no validator users configured")
	}

	addresses := make([]common.Address, 0, len(config.NodeUsers))
	for i, user := range config.NodeUsers {
		if user == nil {
			return nil, fmt.Errorf("validator user %d is nil", i)
		}
		addr, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return nil, fmt.Errorf("validator user %d thor address: %w", i, err)
		}
		addresses = append(addresses, common.Address(addr.String()))
	}
	return addresses, nil
}

func readyXMRChurnValidators(expected []common.Address, nodes []openapi.Node) (int, []string) {
	nodesByAddress := make(map[string]openapi.Node, len(nodes))
	for _, node := range nodes {
		nodesByAddress[node.NodeAddress] = node
	}

	ready := 0
	pending := make([]string, 0)
	for _, address := range expected {
		node, ok := nodesByAddress[address.String()]
		if !ok {
			pending = append(pending, fmt.Sprintf("%s:missing", address))
			continue
		}
		if node.Status != atypes.NodeStatus_Active.String() &&
			node.Status != atypes.NodeStatus_Ready.String() &&
			node.Status != atypes.NodeStatus_Standby.String() {
			pending = append(pending, fmt.Sprintf("%s:%s", address, node.Status))
			continue
		}
		if strings.TrimSpace(node.PeerId) == "" {
			pending = append(pending, fmt.Sprintf("%s:%s:no_peer_id", address, node.Status))
			continue
		}
		// Match NodeAccountPreflightCheck's registered-IP requirement. Standby
		// remains valid here because HaltChurning also pauses markReadyActors.
		if net.ParseIP(node.IpAddress) == nil {
			pending = append(pending, fmt.Sprintf("%s:%s:invalid_ip_address", address, node.Status))
			continue
		}
		ready++
	}
	return ready, pending
}

func httpEndpointReady(get func(string) (*http.Response, error), endpoint string) error {
	res, err := get(endpoint)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return fmt.Errorf("unexpected status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func getMoneroDaemonBlockCount(endpoint string) (int64, error) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"0","method":"get_block_count"}`)
	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := xmrFrostReadinessHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return 0, fmt.Errorf("unexpected status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var response struct {
		Result struct {
			Count int64 `json:"count"`
		} `json:"result"`
		Error any `json:"error,omitempty"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return 0, err
	}
	if response.Error != nil {
		return 0, fmt.Errorf("json-rpc error: %v", response.Error)
	}
	return response.Result.Count, nil
}

func readinessEndpointsFromEnv(envName string, defaults []readinessEndpoint) []readinessEndpoint {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return defaults
	}

	parts := strings.Split(raw, ",")
	endpoints := make([]readinessEndpoint, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		endpoints = append(endpoints, readinessEndpoint{
			name: fmt.Sprintf("%s-%d", strings.ToLower(envName), i+1),
			url:  part,
		})
	}
	if len(endpoints) == 0 {
		return defaults
	}
	return endpoints
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
