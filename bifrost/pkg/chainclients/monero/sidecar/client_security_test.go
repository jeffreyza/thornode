package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestSignerAndDaemonCredentialsAreIsolated(t *testing.T) {
	t.Parallel()

	var signerAuthorization string
	signerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signerAuthorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(Info{
			Protocol:     ProtocolName,
			APIMajor:     APIMajorV2,
			GitCommit:    strings.Repeat("a", 40),
			Network:      "mocknet",
			StateSchema:  StateSchemaV4,
			Capabilities: []string{CapabilityBalanceReconciliation},
		})
	}))
	defer signerServer.Close()

	var daemonAuthorization string
	daemonServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonAuthorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "0",
			"result":  map[string]any{"height": 2},
		})
	}))
	defer daemonServer.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		RPCHost:                   daemonServer.URL,
		UserName:                  "daemon-user",
		Password:                  "daemon-password",
		SignerAuthorizationBearer: "signer-token",
	}, signerServer.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.Info(); err != nil {
		t.Fatalf("signer info: %v", err)
	}
	if _, err = client.ChainHeight(); err != nil {
		t.Fatalf("daemon height: %v", err)
	}

	if signerAuthorization != "Bearer signer-token" {
		t.Fatalf("unexpected signer authorization: %q", signerAuthorization)
	}
	if !strings.HasPrefix(daemonAuthorization, "Basic ") {
		t.Fatalf("expected daemon Basic authorization, got %q", daemonAuthorization)
	}
	if strings.Contains(daemonAuthorization, "signer-token") {
		t.Fatal("signer bearer leaked to monerod")
	}
}

func TestNewClientRejectsAmbiguousSignerAuthentication(t *testing.T) {
	t.Parallel()

	_, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerAuthorizationBearer: "bearer",
		SignerUserName:            "user",
		SignerPassword:            "password",
	}, "http://127.0.0.1:8283")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected ambiguous signer auth rejection, got %v", err)
	}
}

func TestClientEndpointsRejectEmbeddedCredentialsAndURLMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		signer   string
		daemon   string
		contains string
	}{
		{name: "signer userinfo", signer: "https://user:pass@signer.example", contains: "must not contain URL credentials"},
		{name: "signer query", signer: "https://signer.example?token=secret", contains: "query or fragment"},
		{name: "signer path", signer: "https://signer.example/prefix", contains: "must not contain a path"},
		{name: "malformed signer", signer: "https://user:pass@%zz", contains: "invalid sidecar endpoint URL"},
		{name: "daemon userinfo", signer: "http://127.0.0.1:8283", daemon: "https://user:pass@daemon.example", contains: "must not contain URL credentials"},
		{name: "daemon query", signer: "http://127.0.0.1:8283", daemon: "https://daemon.example?token=secret", contains: "query or fragment"},
		{name: "malformed daemon", signer: "http://127.0.0.1:8283", daemon: "https://user:pass@%zz", contains: "invalid monero rpc host URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.BifrostChainConfiguration{
				RPCHost:                   tc.daemon,
				SignerAuthorizationBearer: "explicit-signer-token",
			}
			_, err := NewClientWithEndpoint(cfg, tc.signer)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("expected %q rejection, got %v", tc.contains, err)
			}
			if strings.Contains(fmt.Sprint(err), "user:pass") || strings.Contains(fmt.Sprint(err), "token=secret") {
				t.Fatalf("endpoint error leaked credential material: %v", err)
			}
		})
	}
}

func TestRestoreKeysRequiresExplicitTrue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "missing", body: `{}`, wantErr: true},
		{name: "null", body: `{"restored":null}`, wantErr: true},
		{name: "false", body: `{"restored":false}`, wantErr: true},
		{name: "true", body: `{"restored":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			_, err = client.RestoreKeys("vault", []byte("encrypted"))
			if (err != nil) != tc.wantErr {
				t.Fatalf("RestoreKeys error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestRequireCompatibilityFailsClosed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Info{
			Protocol:          ProtocolName,
			APIMajor:          APIMajorV2,
			GitCommit:         strings.Repeat("b", 40),
			Network:           "chainnet",
			MoneroNettype:     "mainnet",
			MoneroGenesisHash: MoneroMainnetGenesisHash,
			StateSchema:       StateSchemaV4,
			Capabilities: []string{
				CapabilityBalanceReconciliation,
				CapabilityScannerBoundedRewind,
				CapabilitySharePlanQuarantine,
				CapabilityMoneroNetworkIdentity,
			},
		})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerExpectedNetwork: "chainnet",
		SignerExpectedCommit:  strings.Repeat("b", 40),
		ChainNetwork:          "mainnet",
	}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.RequireCompatibility(
		CapabilityBalanceReconciliation,
		CapabilityScannerBoundedRewind,
		CapabilitySharePlanQuarantine,
	); err != nil {
		t.Fatalf("expected compatible signer: %v", err)
	}
	if _, err = client.RequireCompatibility(CapabilityAtomicOutgoingV2); err == nil {
		t.Fatal("expected missing capability to fail closed")
	}
}

func TestRequireCompatibilityTreatsRevisionAsMetadataAndRejectsSchemaMismatch(t *testing.T) {
	t.Parallel()

	info := Info{
		Protocol: ProtocolName, APIMajor: APIMajorV2, GitCommit: strings.Repeat("c", 40),
		Network: "chainnet", MoneroNettype: "mainnet", MoneroGenesisHash: MoneroMainnetGenesisHash,
		StateSchema:  StateSchemaV4,
		Capabilities: []string{CapabilityBalanceReconciliation},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(info)
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerExpectedNetwork: "chainnet",
		SignerExpectedCommit:  strings.Repeat("d", 40),
		ChainNetwork:          "mainnet",
	}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = client.RequireCompatibility(CapabilityBalanceReconciliation); err != nil {
		t.Fatalf("source revision mismatch should be deployment metadata, got: %v", err)
	}

	info.StateSchema = 2
	if _, err = client.RequireCompatibility(CapabilityBalanceReconciliation); err == nil {
		t.Fatal("expected unreviewed state schema rejection")
	}
}

func TestRequireCompatibilityAcceptsConfiguredDaemonIdentities(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		chain       string
		nettype     string
		genesisHash string
	}{
		{name: "mocknet", chain: "mocknet", nettype: "fakechain", genesisHash: MoneroMainnetGenesisHash},
		{name: "mainnet", chain: "mainnet", nettype: "mainnet", genesisHash: MoneroMainnetGenesisHash},
		{name: "stagenet", chain: "stagenet", nettype: "stagenet", genesisHash: MoneroStagenetGenesisHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := Info{
				Protocol:          ProtocolName,
				APIMajor:          APIMajorV2,
				GitCommit:         strings.Repeat("e", 40),
				Network:           tc.name,
				MoneroNettype:     tc.nettype,
				MoneroGenesisHash: tc.genesisHash,
				StateSchema:       StateSchemaV4,
				Capabilities:      []string{CapabilityMoneroNetworkIdentity},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(info)
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
				SignerExpectedNetwork: tc.name,
				ChainNetwork:          tc.chain,
			}, server.URL)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			if _, err = client.RequireCompatibility(CapabilityMoneroNetworkIdentity); err != nil {
				t.Fatalf("expected %s daemon identity to pass: %v", tc.name, err)
			}
		})
	}
}

func TestRequireCompatibilityRejectsDaemonIdentityMismatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		nettype     string
		genesisHash string
		want        string
	}{
		{name: "wrong nettype", nettype: "stagenet", genesisHash: MoneroStagenetGenesisHash, want: "Monero nettype"},
		{name: "wrong genesis", nettype: "mainnet", genesisHash: MoneroStagenetGenesisHash, want: "Monero genesis hash"},
		{name: "malformed genesis", nettype: "mainnet", genesisHash: "not-a-hash", want: "64 hex characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := Info{
				Protocol:          ProtocolName,
				APIMajor:          APIMajorV2,
				GitCommit:         strings.Repeat("f", 40),
				Network:           "mainnet",
				MoneroNettype:     tc.nettype,
				MoneroGenesisHash: tc.genesisHash,
				StateSchema:       StateSchemaV4,
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(info)
			}))
			defer server.Close()

			client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
				SignerExpectedNetwork: "mainnet",
				ChainNetwork:          "mainnet",
			}, server.URL)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			if _, err = client.RequireCompatibility(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q identity rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestExpectedMoneroIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		configured string
		nettype    string
		genesis    string
	}{
		{configured: "mocknet", nettype: "fakechain", genesis: MoneroMainnetGenesisHash},
		{configured: "mainnet", nettype: "mainnet", genesis: MoneroMainnetGenesisHash},
		{configured: "stagenet", nettype: "stagenet", genesis: MoneroStagenetGenesisHash},
		{configured: "testnet", nettype: "testnet", genesis: MoneroTestnetGenesisHash},
	} {
		t.Run(tc.configured, func(t *testing.T) {
			nettype, genesis, err := expectedMoneroIdentity(tc.configured)
			if err != nil {
				t.Fatalf("expected identity: %v", err)
			}
			if nettype != tc.nettype || genesis != tc.genesis {
				t.Fatalf("identity = (%q, %q), want (%q, %q)", nettype, genesis, tc.nettype, tc.genesis)
			}
		})
	}
	for _, configured := range []string{"", "unknown", "fakechain", "regtest", "chainnet"} {
		if _, _, err := expectedMoneroIdentity(configured); err == nil {
			t.Fatalf("expected chain network %q to be rejected", configured)
		} else if !strings.Contains(err.Error(), "accepted values: mocknet, mainnet, stagenet, testnet") {
			t.Fatalf("chain network %q rejection does not enumerate the contract: %v", configured, err)
		}
	}
}

func TestCompatibilityTimeoutDoesNotRetainPartialState(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	requestStarted := make(chan int, 1)
	requestCanceled := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(calls.Add(1))
		if call == 1 {
			requestStarted <- call
			<-r.Context().Done()
			requestCanceled <- call
			return
		}
		_ = json.NewEncoder(w).Encode(Info{
			Protocol:          ProtocolName,
			APIMajor:          APIMajorV2,
			GitCommit:         strings.Repeat("a", 40),
			Network:           "mocknet",
			MoneroNettype:     "fakechain",
			MoneroGenesisHash: MoneroMainnetGenesisHash,
			StateSchema:       StateSchemaV4,
			Capabilities:      []string{CapabilityMoneroNetworkIdentity},
		})
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerExpectedNetwork: "mocknet",
		ChainNetwork:          "mocknet",
		BlockScanner: config.BifrostBlockScannerConfiguration{
			HTTPRequestTimeout: time.Second,
		},
	}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	client.client.Timeout = 500 * time.Millisecond
	result := make(chan error, 1)
	go func() {
		_, callErr := client.RequireCompatibility(CapabilityMoneroNetworkIdentity)
		result <- callErr
	}()
	select {
	case call := <-requestStarted:
		if call != 1 {
			t.Fatalf("timed-out request number = %d, want 1", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed compatibility request did not reach the signer")
	}
	if err = <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected compatibility HTTP timeout, got %v", err)
	}
	select {
	case call := <-requestCanceled:
		if call != 1 {
			t.Fatalf("timed-out handler number = %d, want 1", call)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out compatibility request remained active")
	}

	client.client.Timeout = time.Second
	if _, err = client.RequireCompatibility(CapabilityMoneroNetworkIdentity); err != nil {
		t.Fatalf("fresh compatibility check retained partial state after timeout: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("compatibility calls = %d, want timed-out and fresh", got)
	}
}

func TestNonLoopbackHTTPRequiresExplicitDevelopmentEscapeHatch(t *testing.T) {
	t.Parallel()

	cfg := config.BifrostChainConfiguration{
		SignerAuthorizationBearer: "token",
		SignerExpectedNetwork:     "chainnet",
	}
	if _, err := NewClientWithEndpoint(cfg, "http://signer.internal:8283"); err == nil {
		t.Fatal("expected non-loopback cleartext signer rejection")
	}
	cfg.SignerAllowInsecureHTTP = true
	if _, err := NewClientWithEndpoint(cfg, "http://signer.internal:8283"); err != nil {
		t.Fatalf("explicit development escape hatch rejected: %v", err)
	}
	cfg.SignerExpectedNetwork = "mainnet"
	if _, err := NewClientWithEndpoint(cfg, "http://signer.internal:8283"); err == nil {
		t.Fatal("mainnet must reject non-loopback cleartext even with escape hatch")
	}
}

func TestHTTPErrorKeepsOnlySanitizedMachineCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"scanner_hash_mismatch","error":"secret upstream detail"}`))
	}))
	defer server.Close()
	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Info()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %v", err)
	}
	if httpErr.Code != "scanner_hash_mismatch" {
		t.Fatalf("unexpected machine code %q", httpErr.Code)
	}
	if strings.Contains(err.Error(), "secret upstream detail") {
		t.Fatal("remote error body leaked into local error text")
	}
}

func TestMainnetCompatibilityRejectsRedirectToNonLoopbackHTTP(t *testing.T) {
	var redirectedRequests atomic.Int32
	authorization := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		authorization <- r.Header.Get("Authorization")
		if err := json.NewEncoder(w).Encode(Info{
			Protocol:          ProtocolName,
			APIMajor:          APIMajorV2,
			GitCommit:         strings.Repeat("a", 40),
			Network:           "mainnet",
			MoneroNettype:     "mainnet",
			MoneroGenesisHash: MoneroMainnetGenesisHash,
			StateSchema:       StateSchemaV4,
			Capabilities: []string{
				CapabilityAtomicOutgoingV2,
				CapabilityBalanceReconciliation,
				CapabilityBoundRecovery,
				CapabilityPerVaultKeyStatus,
				CapabilityRestartReservation,
				CapabilityDeadOutgoingQuarantine,
				CapabilityScannerContinuity,
				CapabilityDurableInboundReplay,
				CapabilityScannerBoundedRewind,
				CapabilitySharePlanQuarantine,
				CapabilityVerifiedAbsenceBalance,
				CapabilityCanonicalObservation,
				CapabilityCanonicalTxLocation,
				CapabilityMoneroNetworkIdentity,
			},
		}); err != nil {
			t.Errorf("encode compatibility info: %v", err)
		}
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("configured HTTPS signer did not receive its bearer credential")
		}
		http.Redirect(w, r, "http://signer.invalid:8283/v1/monero/info", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerExpectedNetwork:     "mainnet",
		ChainNetwork:              "mainnet",
		SignerAuthorizationBearer: "test-token",
	}, origin.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	transport, ok := origin.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected TLS test transport")
	}
	transport = transport.Clone()
	transport.Proxy = nil
	// Trust only the test certificate and route the non-loopback URL authority
	// to our local target, keeping every connection inside this test.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "signer.invalid:8283" {
			address = target.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	client.client.Transport = transport

	_, err = client.RequireCompatibility(
		CapabilityAtomicOutgoingV2,
		CapabilityBalanceReconciliation,
		CapabilityBoundRecovery,
		CapabilityPerVaultKeyStatus,
		CapabilityRestartReservation,
		CapabilityDeadOutgoingQuarantine,
		CapabilityScannerContinuity,
		CapabilityDurableInboundReplay,
		CapabilityScannerBoundedRewind,
		CapabilitySharePlanQuarantine,
		CapabilityVerifiedAbsenceBalance,
		CapabilityCanonicalObservation,
		CapabilityCanonicalTxLocation,
		CapabilityMoneroNetworkIdentity,
	)
	if err == nil {
		var receivedAuth string
		select {
		case receivedAuth = <-authorization:
		default:
		}
		t.Errorf("mainnet compatibility accepted a non-loopback HTTP redirect: requests=%d authorization=%q", redirectedRequests.Load(), receivedAuth)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Errorf("mainnet redirect policy must reject before sending an HTTP request, got %d requests", got)
	}
}

func TestSignerRequestsRejectRedirects(t *testing.T) {
	t.Parallel()

	for _, operation := range []struct {
		name, method, path, response string
		call                         func(*Client) error
	}{
		{
			name: "info", method: http.MethodGet, path: "/v1/monero/info",
			response: `{"protocol":"xmr-frost"}`,
			call: func(client *Client) error {
				_, err := client.Info()
				return err
			},
		},
		{
			name: "restore", method: http.MethodPost, path: "/v1/monero/keys/restore",
			response: `{"restored":true}`,
			call: func(client *Client) error {
				_, err := client.RestoreKeys("test-vault", []byte("test-recovery"))
				return err
			},
		},
		{
			name: "sign", method: http.MethodPost, path: "/v1/monero/sign/prepare",
			response: `{"session_id":"test-session","preprocess":"test-preprocess"}`,
			call: func(client *Client) error {
				_, err := client.SignPrepare(SignPrepareReq{MsgID: "test-message", VaultPubKey: "test-vault"})
				return err
			},
		},
	} {
		for _, endpointKind := range []string{"https", "loopback-http", "development-http"} {
			for _, status := range []int{
				http.StatusOK,
				http.StatusMovedPermanently,
				http.StatusFound,
				http.StatusSeeOther,
				http.StatusTemporaryRedirect,
				http.StatusPermanentRedirect,
			} {
				for _, locationKind := range []string{"valid", "missing", "empty"} {
					if status == http.StatusOK && locationKind != "valid" {
						continue
					}
					t.Run(fmt.Sprintf("%s/%s/%d/%s", operation.name, endpointKind, status, locationKind), func(t *testing.T) {
						var originRequests, redirectedRequests atomic.Int32
						handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.URL.Path == "/redirected" {
								redirectedRequests.Add(1)
							} else {
								originRequests.Add(1)
								if r.Method != operation.method || r.URL.Path != operation.path {
									t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.Path, operation.method, operation.path)
								}
								if r.Header.Get("Authorization") != "Bearer test-token" {
									t.Error("configured signer did not receive its bearer credential")
								}
								if r.Method == http.MethodPost {
									var payload struct {
										VaultPubKey string `json:"vault_pub_key"`
									}
									if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
										t.Errorf("decode request: %v", err)
									}
									if payload.VaultPubKey != "test-vault" {
										t.Errorf("request vault = %q, want test-vault", payload.VaultPubKey)
									}
								}
								if status != http.StatusOK && locationKind != "missing" {
									location := ""
									if locationKind == "valid" {
										location = "/redirected?token=redirect-secret"
									}
									w.Header().Set("Location", location)
								}
							}
							w.Header().Set("Content-Type", "application/json")
							if r.URL.Path != "/redirected" {
								w.WriteHeader(status)
							}
							// Valid JSON must not turn a 3xx response into a successful call.
							_, _ = w.Write([]byte(operation.response))
						})
						var server *httptest.Server
						if endpointKind == "https" {
							server = httptest.NewTLSServer(handler)
						} else {
							server = httptest.NewServer(handler)
						}
						defer server.Close()

						endpoint := server.URL
						cfg := config.BifrostChainConfiguration{SignerAuthorizationBearer: "test-token"}
						if endpointKind == "development-http" {
							endpoint = "http://signer.invalid:8283"
							cfg.SignerExpectedNetwork = "mocknet"
							cfg.SignerAllowInsecureHTTP = true
						}
						client, err := NewClientWithEndpoint(cfg, endpoint)
						if err != nil {
							t.Fatalf("new client: %v", err)
						}
						transport, ok := server.Client().Transport.(*http.Transport)
						if !ok {
							t.Fatal("unexpected test transport")
						}
						transport = transport.Clone()
						transport.Proxy = nil
						// Map every connection to the local test server, including the
						// configured non-loopback development authority.
						transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
							return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
						}
						defer transport.CloseIdleConnections()
						client.client.Transport = transport
						client.signClient.Transport = transport

						err = operation.call(client)
						if status == http.StatusOK {
							if err != nil {
								t.Fatalf("direct request failed: %v", err)
							}
						} else {
							if err == nil {
								t.Error("redirect response was accepted")
							} else if strings.Contains(err.Error(), "redirect-secret") || strings.Contains(err.Error(), "test-token") {
								t.Errorf("redirect error leaked credential material: %v", err)
							}
						}
						if status != http.StatusOK && locationKind != "valid" && !IsHTTPStatus(err, status) {
							t.Errorf("missing-Location response error = %v, want HTTP status %d", err, status)
						}
						if got := originRequests.Load(); got != 1 {
							t.Errorf("configured signer requests = %d, want 1", got)
						}
						if got := redirectedRequests.Load(); got != 0 {
							t.Errorf("redirected requests = %d, want 0", got)
						}
					})
				}
			}
		}
	}
}
