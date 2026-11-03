package core

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestXMRFrostChurnReadinessPassesWhenClusterIsReady(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)

	result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
	if !result.ready {
		t.Fatalf("expected readiness gate to pass, got reason=%s err=%v", result.reason, result.err)
	}
	if result.readyValidators != 2 {
		t.Fatalf("expected two ready validators, got %d", result.readyValidators)
	}
	if result.moneroHeight != 42 {
		t.Fatalf("expected monero height 42, got %d", result.moneroHeight)
	}
}

func TestXMRFrostChurnReadinessAllowsPreChurnStandbyValidators(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)
	deps.nodes = func() ([]openapi.Node, error) {
		return []openapi.Node{
			{NodeAddress: "tthor1dog", Status: atypes.NodeStatus_Active.String(), PeerId: "dog-peer", IpAddress: "172.19.0.2"},
			{NodeAddress: "tthor1cat", Status: atypes.NodeStatus_Standby.String(), PeerId: "cat-peer", IpAddress: "172.19.0.3"},
		}, nil
	}

	result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
	if !result.ready {
		t.Fatalf("expected standby validator with peer id to pass pre-churn readiness, got reason=%s pending=%v", result.reason, result.pendingValidators)
	}
}

func TestXMRFrostChurnReadinessRequiresValidatorPeerIDs(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)
	deps.nodes = func() ([]openapi.Node, error) {
		return []openapi.Node{
			{NodeAddress: "tthor1dog", Status: atypes.NodeStatus_Active.String(), PeerId: "dog-peer", IpAddress: "172.19.0.2"},
			{NodeAddress: "tthor1cat", Status: atypes.NodeStatus_Ready.String(), IpAddress: "172.19.0.3"},
		}, nil
	}

	result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
	if result.ready {
		t.Fatal("expected readiness gate to wait for missing peer id")
	}
	if result.reason != "validator nodes not ready" {
		t.Fatalf("unexpected reason: %s", result.reason)
	}
	if len(result.pendingValidators) != 1 || !strings.Contains(result.pendingValidators[0], "no_peer_id") {
		t.Fatalf("expected missing peer id in pending validators, got %v", result.pendingValidators)
	}
}

func TestXMRFrostChurnReadinessRequiresRegisteredIP(t *testing.T) {
	for _, status := range []atypes.NodeStatus{atypes.NodeStatus_Active, atypes.NodeStatus_Ready, atypes.NodeStatus_Standby} {
		for _, tc := range []struct {
			name  string
			ip    string
			valid bool
		}{
			{name: "missing"},
			{name: "whitespace", ip: " "},
			{name: "hostname", ip: "thornode-cat"},
			{name: "malformed", ip: "172.19.0.999"},
			{name: "port", ip: "172.19.0.3:26656"},
			{name: "padded", ip: " 172.19.0.3 "},
			{name: "ipv4", ip: "172.19.0.3", valid: true},
			{name: "ipv6", ip: "fd00::3", valid: true},
		} {
			t.Run(status.String()+"/"+tc.name, func(t *testing.T) {
				deps := readyXMRFrostChurnDeps(t)
				deps.nodes = func() ([]openapi.Node, error) {
					return []openapi.Node{
						{NodeAddress: "tthor1dog", Status: atypes.NodeStatus_Active.String(), PeerId: "dog-peer", IpAddress: "172.19.0.2"},
						{NodeAddress: "tthor1cat", Status: status.String(), PeerId: "cat-peer", IpAddress: tc.ip},
					}, nil
				}
				result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
				if result.ready != tc.valid {
					t.Fatalf("ready=%v for registered IP %q, want %v", result.ready, tc.ip, tc.valid)
				}
				if !tc.valid {
					pending := "tthor1cat:" + status.String() + ":invalid_ip_address"
					if result.readyValidators != 1 || result.reason != "validator nodes not ready" ||
						len(result.pendingValidators) != 1 || result.pendingValidators[0] != pending {
						t.Fatalf("expected invalid registered IP diagnostic, got %+v", result)
					}
				}
			})
		}
	}
}

func TestXMRFrostChurnReadinessRequiresHTTPReadiness(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)
	deps.httpGet = func(endpoint string) (*http.Response, error) {
		if endpoint == "http://frost-cat/health" {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("warming up")),
			}, nil
		}
		return okHTTPResponse(), nil
	}

	result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
	if result.ready {
		t.Fatal("expected readiness gate to wait for unhealthy frost endpoint")
	}
	if result.reason != "xmr frost endpoint not ready" {
		t.Fatalf("unexpected reason: %s", result.reason)
	}
	if result.endpointName != "cat-xmr-frost" {
		t.Fatalf("expected cat frost endpoint, got %s", result.endpointName)
	}
}

func TestXMRFrostChurnReadinessRequiresPositiveMoneroHeight(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)
	deps.moneroHeight = func(string) (int64, error) {
		return 0, nil
	}

	result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
	if result.ready {
		t.Fatal("expected readiness gate to wait for monero height")
	}
	if result.reason != "monero daemon height is zero" {
		t.Fatalf("unexpected reason: %s", result.reason)
	}
}

func TestXMRFrostChurnReadinessSurfacesMoneroHeightErrors(t *testing.T) {
	deps := readyXMRFrostChurnDeps(t)
	deps.moneroHeight = func(string) (int64, error) {
		return 0, errors.New("daemon unavailable")
	}

	result := checkXMRFrostChurnReadiness(&OpConfig{}, deps)
	if result.ready {
		t.Fatal("expected readiness gate to wait for monero height")
	}
	if result.reason != "monero daemon height unavailable" {
		t.Fatalf("unexpected reason: %s", result.reason)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "daemon unavailable") {
		t.Fatalf("expected monero error, got %v", result.err)
	}
}

func TestReadinessEndpointsFromEnvFallbackAndOverrides(t *testing.T) {
	defaults := []readinessEndpoint{
		{name: "default-a", url: "http://default-a/ready"},
		{name: "default-b", url: "http://default-b/ready"},
	}
	envName := "TEST_XMR_FROST_READINESS_ENDPOINTS"

	t.Setenv(envName, "")
	got := readinessEndpointsFromEnv(envName, defaults)
	if len(got) != len(defaults) || got[0] != defaults[0] || got[1] != defaults[1] {
		t.Fatalf("expected defaults for empty env, got %+v", got)
	}

	t.Setenv(envName, " , ,, ")
	got = readinessEndpointsFromEnv(envName, defaults)
	if len(got) != len(defaults) || got[0] != defaults[0] || got[1] != defaults[1] {
		t.Fatalf("expected defaults for separator-only env, got %+v", got)
	}

	t.Setenv(envName, "http://node-a/ready, ,http://node-b/ready")
	got = readinessEndpointsFromEnv(envName, defaults)
	if len(got) != 2 {
		t.Fatalf("expected two override endpoints, got %+v", got)
	}
	if got[0].url != "http://node-a/ready" || got[1].url != "http://node-b/ready" {
		t.Fatalf("unexpected override urls: %+v", got)
	}
	if got[0].name == "" || got[1].name == "" {
		t.Fatalf("expected generated endpoint names, got %+v", got)
	}
}

func readyXMRFrostChurnDeps(t *testing.T) xmrFrostChurnReadinessDeps {
	t.Helper()

	return xmrFrostChurnReadinessDeps{
		nodeAddresses: func(*OpConfig) ([]common.Address, error) {
			return []common.Address{"tthor1dog", "tthor1cat"}, nil
		},
		nodes: func() ([]openapi.Node, error) {
			return []openapi.Node{
				{NodeAddress: "tthor1dog", Status: atypes.NodeStatus_Active.String(), PeerId: "dog-peer", IpAddress: "172.19.0.2"},
				{NodeAddress: "tthor1cat", Status: atypes.NodeStatus_Ready.String(), PeerId: "cat-peer", IpAddress: "172.19.0.3"},
			}, nil
		},
		httpGet: func(string) (*http.Response, error) {
			return okHTTPResponse(), nil
		},
		moneroHeight: func(string) (int64, error) {
			return 42, nil
		},
		bifrostEndpoints: []readinessEndpoint{
			{name: "dog-bifrost", url: "http://bifrost-dog/p2pid"},
			{name: "cat-bifrost", url: "http://bifrost-cat/p2pid"},
		},
		frostEndpoints: []readinessEndpoint{
			{name: "dog-xmr-frost", url: "http://frost-dog/health"},
			{name: "cat-xmr-frost", url: "http://frost-cat/health"},
		},
		moneroRPC: "http://monero/json_rpc",
	}
}

func okHTTPResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
	}
}
