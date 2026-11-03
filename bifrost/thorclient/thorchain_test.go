package thorclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/prometheus/client_golang/prometheus"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestPackage(t *testing.T) { TestingT(t) }

type ThorchainSuite struct {
	server             *httptest.Server
	cfg                config.BifrostClientConfiguration
	bridge             *thorchainBridge
	authAccountFixture string
	nodeAccountFixture string
}

var _ = Suite(&ThorchainSuite{})

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (s *ThorchainSuite) SetUpTest(c *C) {
	cfg2 := cosmos.GetConfig()
	cfg2.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg, _, kb := SetupThorchainForTest(c)
	s.cfg = cfg
	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.RequestURI, AuthAccountEndpoint):
			httpTestHandler(c, rw, s.authAccountFixture)
		case strings.HasPrefix(req.RequestURI, NodeAccountEndpoint):
			httpTestHandler(c, rw, s.nodeAccountFixture)
		case strings.HasPrefix(req.RequestURI, LastBlockEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/lastblock/eth.json")
		case strings.HasPrefix(req.RequestURI, StatusEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/status/status.json")
		case strings.HasPrefix(req.RequestURI, KeysignEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/keysign/template.json")
		case strings.HasPrefix(req.RequestURI, AsgardVault):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, PubKeysEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/vaults/pubKeys.json")
		case strings.EqualFold(req.RequestURI, BroadcastTxsEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/txs/success.json")
		case strings.HasPrefix(req.RequestURI, ThorchainConstants):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/constants/constants.json")
		case strings.HasPrefix(req.RequestURI, RagnarokEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/ragnarok/ragnarok.json")
		case strings.HasPrefix(req.RequestURI, ChainVersionEndpoint):
			_, err := rw.Write([]byte(`{"current":"` + stypes.GetCurrentVersion().String() + `"}`))
			c.Assert(err, IsNil)
		case strings.HasPrefix(req.RequestURI, MimirEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/mimir/mimir.json")
		case strings.HasPrefix(req.RequestURI, InboundAddressesEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case strings.HasPrefix(req.RequestURI, "/thorchain/thorname/"):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/thorname/thorname.json")
		case strings.HasPrefix(req.RequestURI, PendingOutboundEndpoint):
			// /thorchain/queue/outbound serialises as a BARE JSON array. If a height
			// query was intended (the URI mentions "height", raw or %3F-escaped) but did
			// not parse into a real query param, the '?' was escaped into the path — the
			// exact #3 regression — so 400 to make the test fail.
			if strings.Contains(req.RequestURI, "height") && req.URL.Query().Get("height") == "" {
				rw.WriteHeader(http.StatusBadRequest)
				return
			}
			_, err := rw.Write([]byte(`[{"height":100,"vault_pub_key":"vaultA","in_hash":"HASH1","chain":"SOL"},` +
				`{"height":101,"vault_pub_key":"vaultB","in_hash":"HASH2","chain":"ETH"}]`))
			c.Assert(err, IsNil)
		}
	}))
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	var err error
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	var ok bool
	s.bridge, ok = bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	s.bridge.httpClient.RetryMax = 1 // fail fast
	c.Assert(err, IsNil)
	c.Assert(s.bridge, NotNil)
}

func (s *ThorchainSuite) TearDownTest(c *C) {
	s.server.Close()
}

func (s *ThorchainSuite) TestGetThorChainURL(c *C) {
	uri := s.bridge.getThorChainURL("")
	c.Assert(uri, Equals, "http://"+s.server.Listener.Addr().String())
}

// Model committed stores separately from signing_height: changing the latter
// alone never selects a historical store in the gateway.
type keysignPolicyState struct {
	disable     int64
	compromised int64
	policy      KeysignPolicy
}

type keysignPolicyAPI struct {
	mu       sync.Mutex
	tip      int64
	vault    stypes.Vault
	alias    common.PubKey
	states   map[int64]keysignPolicyState
	requests []string
	// Intercepts errors or advances the tip between requests, under mu.
	intercept func(http.ResponseWriter, *http.Request) bool
}

func newKeysignPolicyAPI(t *testing.T) (*keysignPolicyAPI, *thorchainBridge) {
	t.Helper()
	members := make(common.PubKeys, 17)
	for i := range members {
		members[i] = stypes.GetRandomPubKey()
	}
	threshold, err := conversion.GetThreshold(len(members))
	if err != nil {
		t.Fatal(err)
	}
	f := &keysignPolicyAPI{
		tip:    200,
		vault:  stypes.Vault{PubKey: stypes.GetRandomPubKey(), Membership: members.Strings()},
		alias:  stypes.GetRandomPubKey(),
		states: make(map[int64]keysignPolicyState),
	}
	policy := KeysignPolicy{
		PubKey: f.vault.PubKey, Signers: members[:threshold+1], Height: 100,
		Threshold: threshold, RequiredSigners: threshold + 1, ActiveSignerCount: threshold + 1,
		NeverSigners: members[16:], JailedFallbackSigners: members[7 : threshold+1],
	}
	f.states[100] = keysignPolicyState{disable: -1, policy: policy}
	f.states[200] = f.states[100]
	server := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(server.Close)
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 0
	bridge := &thorchainBridge{
		cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
		httpClient: client, errCounter: newThorchainTestErrCounter(),
	}
	return f, bridge
}

func (f *keysignPolicyAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.RequestURI())
	w.Header().Set("Content-Type", "application/json")
	if f.intercept != nil && f.intercept(w, r) {
		return
	}
	if r.URL.Path == LastBlockEndpoint {
		_ = json.NewEncoder(w).Encode([]openapi.LastBlock{{Thorchain: f.tip}})
		return
	}
	height := f.tip
	if h := r.URL.Query().Get("height"); h != "" {
		var err error
		height, err = strconv.ParseInt(h, 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	state, ok := f.states[height]
	if !ok {
		http.Error(w, "historical store unavailable", http.StatusNotFound)
		return
	}
	path := r.URL.Path
	switch {
	case path == fmt.Sprintf(VaultEndpoint, f.vault.PubKey) || path == fmt.Sprintf(VaultEndpoint, f.alias):
		_ = json.NewEncoder(w).Encode(f.vault)
	case path == MimirEndpoint+"/key/CompromisedVault-"+f.vault.PubKey.String():
		_ = json.NewEncoder(w).Encode(state.compromised)
	case path == MimirEndpoint+"/key/"+constants.MimirKeyDisableTSSSignerFiltering:
		_ = json.NewEncoder(w).Encode(state.disable)
	case path == fmt.Sprintf(KeysignPartyEndpoint, f.vault.PubKey) || path == fmt.Sprintf(KeysignPartyEndpoint, f.alias):
		if state.compromised > 0 {
			http.Error(w, "vault is "+common.CompromisedVaultErrorText, http.StatusBadRequest)
			return
		}
		policy := state.policy
		policy.Height, _ = strconv.ParseInt(r.URL.Query().Get("signing_height"), 10, 64)
		if state.disable > 0 && state.disable <= policy.Height {
			policy.Signers = f.vault.GetMembership()
			policy.ActiveSignerCount = len(policy.Signers)
			policy.NeverSigners = nil
			policy.JailedFallbackSigners = nil
		}
		_ = json.NewEncoder(w).Encode(policy)
	default:
		http.Error(w, "unexpected endpoint", http.StatusNotFound)
	}
}

func TestKeysignPolicySnapshotSurvivesLiveJailChanges(t *testing.T) {
	f, bridge := newKeysignPolicyAPI(t)
	expected := f.states[100].policy
	// A later jail/slash update replaces a preferred fallback in the live party.
	live := f.states[200]
	live.policy.Signers = append(common.PubKeys(nil), expected.Signers[1:]...)
	live.policy.Signers = append(live.policy.Signers, f.vault.GetMembership()[12])
	f.states[200] = live
	first, err := bridge.GetKeysignPolicy(f.vault.PubKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, expected) {
		t.Fatalf("signing height 100 used live jail state instead of its committed snapshot: got %v, want %v", first.Signers, expected.Signers)
	}
	f.mu.Lock()
	for _, request := range f.requests[1:] {
		wantHeight := "height=200"
		if strings.Contains(request, "keysign_party") {
			wantHeight = "height=100&signing_height=100"
		}
		if !strings.HasSuffix(request, wantHeight) {
			t.Errorf("read used the wrong store: %s (want %s)", request, wantHeight)
		}
	}
	live.policy.Signers = f.vault.GetMembership()[5:]
	f.states[201] = live
	f.tip = 201
	f.mu.Unlock()
	second, err := bridge.GetKeysignPolicy(f.vault.PubKey, 100)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("retry changed policy: %v; %v", second, err)
	}
	party, err := bridge.GetKeysignParty(f.vault.PubKey, 100)
	if err != nil || !party.Equals(expected.Signers) {
		t.Fatalf("legacy wrapper changed policy: %v, %v", party, err)
	}
}

func TestKeysignPolicyOverrideTransition(t *testing.T) {
	// S=150 (effective vote), C=200 (clear). Patched nodes retain the historical
	// override for S <= H < C. Pre-S work must be drained at the process boundary.
	for _, h := range []int64{149, 150, 199, 200} {
		t.Run(strconv.FormatInt(h, 10), func(t *testing.T) {
			f, bridge := newKeysignPolicyAPI(t)
			state := f.states[100]
			state.policy.Height = h
			switch {
			case h >= 200:
				state.disable = 0
			case h >= 150:
				state.disable = 1
			}
			f.states[h] = state
			cleared := f.states[200]
			cleared.disable = 0
			f.states[200] = cleared
			policy, err := bridge.GetKeysignPolicy(f.vault.PubKey, h)
			if err != nil {
				t.Fatal(err)
			}
			want := state.policy.Signers
			if h >= 150 && h < 200 {
				want = f.vault.GetMembership()
			}
			if !policy.Signers.Equals(want) {
				t.Fatalf("unexpected party after clearing for H=%d: %v", h, policy.Signers)
			}
			// Before clearing, today's explicit override widens even pre-S work.
			f.mu.Lock()
			current := f.states[200]
			current.disable = 1
			f.states[200] = current
			f.mu.Unlock()
			beforeClear, err := bridge.GetKeysignPolicy(f.vault.PubKey, h)
			if err != nil || !beforeClear.Signers.Equals(f.vault.GetMembership()) {
				t.Fatalf("override failed: %v, %v", beforeClear, err)
			}
			if h < 150 && beforeClear.Signers.Equals(policy.Signers) {
				t.Fatal("fixture did not reproduce pre-S work changing party at clearing")
			}
		})
	}
}

func TestKeysignPolicyOverrideDoesNotRequireHistory(t *testing.T) {
	for _, disable := range []int64{1, 100, 101} {
		t.Run(strconv.FormatInt(disable, 10), func(t *testing.T) {
			f, bridge := newKeysignPolicyAPI(t)
			delete(f.states, 100)
			current := f.states[200]
			current.disable = disable
			f.states[200] = current
			policy, err := bridge.GetKeysignPolicy(f.alias, 100)
			if disable > 100 {
				if err == nil {
					t.Fatal("future override must not bypass missing history")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !policy.PubKey.Equals(f.vault.PubKey) || !policy.Signers.Equals(f.vault.GetMembership()) || policy.Threshold != 11 || policy.RequiredSigners != 12 || policy.Height != 100 {
				t.Fatalf("incorrect full-vault policy: %+v", policy)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, request := range f.requests {
				if strings.Contains(request, "keysign_party") {
					t.Fatal("override unexpectedly queried history")
				}
			}
		})
	}
}

func TestKeysignPolicyControlsAreFreshAndPinned(t *testing.T) {
	f, bridge := newKeysignPolicyAPI(t)
	state := f.states[100]
	state.disable = 1
	f.states[199] = state
	f.tip = 199
	// Populate the shared one-block cache immediately before clearing at C=200.
	if _, err := bridge.GetBlockHeight(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.GetMimir(constants.MimirKeyDisableTSSSignerFiltering); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.GetMimir("CompromisedVault-" + f.vault.PubKey.String()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	cleared := f.states[200]
	cleared.disable = 0
	f.states[200] = cleared
	f.tip = 200
	f.requests = nil
	// The tip advances after its read. Both controls must still be read at T=200.
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/thorchain/vault/") {
			f.tip = 201
		}
		return false
	}
	f.mu.Unlock()
	policy, err := bridge.GetKeysignPolicy(f.vault.PubKey, 200)
	if err != nil || !policy.Signers.Equals(f.states[200].policy.Signers) {
		t.Fatalf("stale controls at clearing: %v, %v", policy, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests[1:] {
		if !strings.Contains(request, "height=200") {
			t.Fatalf("read not pinned to T: %s", request)
		}
	}
	// Height-bearing URLs must not accumulate in the global cache indefinitely.
	httpResponseCachesMu.Lock()
	defer httpResponseCachesMu.Unlock()
	for key := range httpResponseCaches {
		if strings.HasPrefix(key, bridge.getThorChainURL("")) && strings.Contains(key, "height=") {
			t.Fatalf("snapshot URL retained in generic cache: %s", key)
		}
	}
}

func TestKeysignPolicyFailsClosed(t *testing.T) {
	for _, route := range []string{"tip", "vault", "compromised", "override", "history"} {
		for _, failure := range []string{"404", "501", "500", "empty404", "malformed", "null", "transport"} {
			t.Run(route+"/"+failure, func(t *testing.T) {
				f, bridge := newKeysignPolicyAPI(t)
				if route == "compromised" {
					state := f.states[200]
					state.disable = 1
					f.states[200] = state
				}
				matches := func(r *http.Request) bool {
					switch route {
					case "tip":
						return r.URL.Path == LastBlockEndpoint
					case "vault":
						return r.URL.Path == fmt.Sprintf(VaultEndpoint, f.vault.PubKey)
					case "compromised":
						return strings.Contains(r.URL.Path, "CompromisedVault-")
					case "override":
						return strings.Contains(r.URL.Path, constants.MimirKeyDisableTSSSignerFiltering)
					default:
						return strings.HasSuffix(r.URL.Path, "/keysign_party")
					}
				}
				f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
					if !matches(r) {
						return false
					}
					switch failure {
					case "malformed":
						_, _ = w.Write([]byte("{"))
					case "null":
						_, _ = w.Write([]byte("null"))
					case "empty404":
						w.WriteHeader(http.StatusNotFound)
					default:
						status, _ := strconv.Atoi(failure)
						http.Error(w, "historical store unavailable", status)
					}
					return true
				}
				if failure == "transport" {
					bridge.httpClient.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
						if matches(r) {
							return nil, errors.New("connection failed")
						}
						return http.DefaultTransport.RoundTrip(r)
					})
				}
				policy, err := bridge.GetKeysignPolicy(f.vault.PubKey, 100)
				if err == nil || len(policy.Signers) != 0 {
					t.Fatalf("error widened policy: %+v; %v", policy, err)
				}
				if failure == "transport" && !errors.Is(err, errThorchainTransport) {
					t.Fatalf("lost transport error: %v", err)
				}
				if failure == "404" && route != "tip" && !strings.Contains(err.Error(), "historical store unavailable") {
					t.Fatalf("lost error body: %v", err)
				}
			})
		}
	}
}

func TestKeysignPolicyCompromisedVaultWins(t *testing.T) {
	for _, current := range []bool{true, false} {
		t.Run(fmt.Sprintf("current=%v", current), func(t *testing.T) {
			f, bridge := newKeysignPolicyAPI(t)
			h := int64(100)
			if current {
				h = 200
			}
			state := f.states[h]
			state.compromised = 1
			state.disable = 1
			f.states[h] = state
			_, err := bridge.GetKeysignPolicy(f.alias, 100)
			if !IsCompromisedVaultError(err) {
				t.Fatalf("compromised vault did not override full membership: %v", err)
			}
		})
	}
}

func TestKeysignPolicyRejectsUnavailableHeight(t *testing.T) {
	for _, h := range []int64{-1, 0, 201} {
		f, bridge := newKeysignPolicyAPI(t)
		policy, err := bridge.GetKeysignPolicy(f.vault.PubKey, h)
		if err == nil || len(policy.Signers) != 0 {
			t.Fatalf("accepted unavailable H=%d: %+v, %v", h, policy, err)
		}
	}
}

func TestKeysignPolicyRejectsInvalidSnapshot(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*KeysignPolicy)
	}{
		{"height", func(p *KeysignPolicy) { p.Height++ }},
		{"pubkey", func(p *KeysignPolicy) { p.PubKey = stypes.GetRandomPubKey() }},
		{"missing-pubkey", func(p *KeysignPolicy) { p.PubKey = common.EmptyPubKey }},
		{"threshold", func(p *KeysignPolicy) { p.Threshold-- }},
		{"required-signers", func(p *KeysignPolicy) { p.RequiredSigners-- }},
		{"no-quorum", func(p *KeysignPolicy) { p.Signers = p.Signers[:p.Threshold] }},
		{"duplicate", func(p *KeysignPolicy) { p.Signers[0] = p.Signers[1] }},
		{"non-member", func(p *KeysignPolicy) { p.Signers[0] = stypes.GetRandomPubKey() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, bridge := newKeysignPolicyAPI(t)
			policy := f.states[100].policy
			test.change(&policy)
			f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/keysign_party") {
					return false
				}
				_ = json.NewEncoder(w).Encode(policy)
				return true
			}
			result, err := bridge.GetKeysignPolicy(f.vault.PubKey, 100)
			if err == nil || len(result.Signers) != 0 {
				t.Fatalf("accepted invalid snapshot: %+v, %v", result, err)
			}
		})
	}
}

func TestKeysignPolicyCompromisedControlBypassesCache(t *testing.T) {
	f, bridge := newKeysignPolicyAPI(t)
	if value, err := bridge.GetMimir("CompromisedVault-" + f.vault.PubKey.String()); err != nil || value != 0 {
		t.Fatalf("seed cached control: %d, %v", value, err)
	}
	f.mu.Lock()
	state := f.states[200]
	state.compromised = 1
	state.disable = 1
	f.states[201] = state
	f.tip = 201
	f.mu.Unlock()
	_, err := bridge.GetKeysignPolicy(f.alias, 100)
	if !IsCompromisedVaultError(err) {
		t.Fatalf("cached control bypassed compromised hard stop: %v", err)
	}
}

func TestKeysignPolicyRetiringVault(t *testing.T) {
	f, bridge := newKeysignPolicyAPI(t)
	f.vault.Status = stypes.VaultStatus_RetiringVault
	policy, err := bridge.GetKeysignPolicy(f.vault.PubKey, 100)
	if err != nil || !policy.Signers.Equals(f.states[100].policy.Signers) {
		t.Fatalf("retiring vault lost recovery policy: %+v, %v", policy, err)
	}
}

// newThorchainTestErrCounter returns an isolated Prometheus counter so these
// tests do not share global metric state.
func newThorchainTestErrCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "thornode_thorclient_test_errors_total",
		Help: "test counter for total thorclient errors",
	}, []string{"error", "chain"})
}

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	var content []byte
	var err error

	switch fixture {
	case "500":
		rw.WriteHeader(http.StatusInternalServerError)
	default:
		content, err = os.ReadFile(fixture)
		if err != nil {
			c.Fatal(err)
		}
	}

	rw.Header().Set("Content-Type", "application/json")
	if _, err = rw.Write(content); err != nil {
		c.Fatal(err)
	}
}

func (s *ThorchainSuite) TestGet(c *C) {
	buf, status, err := s.bridge.getWithPath("")
	c.Check(status, Equals, http.StatusOK)
	c.Assert(err, IsNil)
	c.Assert(buf, NotNil)
}

func (s *ThorchainSuite) TestGetObservationsStdTx_OutboundShouldHaveConfirmationCounting(c *C) {
	pk := stypes.GetRandomPubKey()
	vaultAddr, err := pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(
		common.Tx{
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "This is my memo!",
			FromAddress: vaultAddr,
			ToAddress:   "0xd58610f89265a2fb637ac40edf59141ff873b266",
		},
		1,
		pk,
		100,
	)

	_, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(out, NotNil)
	c.Assert(err, IsNil)
	c.Assert(out[0].FinaliseHeight > out[0].BlockHeight, Equals, true)
}

func (s *ThorchainSuite) TestSign(c *C) {
	pk := stypes.GetRandomPubKey()
	vaultAddr, err := pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(
		common.Tx{
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "This is my memo!",
			FromAddress: vaultAddr,
			ToAddress:   common.Address("0xd58610f89265a2fb637ac40edf59141ff873b266"),
		},
		1,
		pk,
		1,
	)

	_, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(out, NotNil)
	c.Assert(err, IsNil)
}

func (s *ThorchainSuite) TestGetInboundOutboundXMRRequiresDerivableObservedAddress(c *C) {
	pk := stypes.GetRandomPubKey()
	tx := common.NewObservedTx(
		common.Tx{
			Chain: common.XMRChain,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(123400000)),
			},
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('8')),
			ToAddress:   common.Address(common.XMRChecksummedDummyAddress('4')),
		},
		1,
		pk,
		1,
	)

	_, _, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "xmr observed vault pubkey"), Equals, true)
}

func (s *ThorchainSuite) TestGetInboundOutboundXMRInboundWithEddsaObservedPubKey(c *C) {
	pk, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	vaultAddr, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	tx := common.NewObservedTx(
		common.Tx{
			Chain: common.XMRChain,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('8')),
			ToAddress:   vaultAddr,
		},
		1,
		pk,
		1,
	)

	in, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(err, IsNil)
	c.Assert(in, HasLen, 1)
	c.Assert(out, HasLen, 0)
}

func (s *ThorchainSuite) TestGetInboundOutboundXMROutboundWithEddsaObservedPubKey(c *C) {
	pk, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	vaultAddr, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	tx := common.NewObservedTx(
		common.Tx{
			Chain: common.XMRChain,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "OUT:ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890",
			FromAddress: vaultAddr,
			ToAddress:   common.Address(common.XMRChecksummedDummyAddress('8')),
		},
		1,
		pk,
		100,
	)

	in, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(err, IsNil)
	c.Assert(in, HasLen, 0)
	c.Assert(out, HasLen, 1)
	c.Assert(out[0].FinaliseHeight > out[0].BlockHeight, Equals, true)
}

func (ThorchainSuite) TestNewThorchainBridge(c *C) {
	testFunc := func(cfg config.BifrostClientConfiguration, errChecker, sbChecker Checker) {
		registry := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(registry)
		cdc := codec.NewProtoCodec(registry)
		kb := keyring.NewInMemory(cdc)
		_, _, err := kb.NewMnemonic(cfg.SignerName, keyring.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
		c.Assert(err, IsNil)
		sb, err := NewThorchainBridge(cfg, m, NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
		c.Assert(err, errChecker)
		c.Assert(sb, sbChecker)
	}
	testFunc(config.BifrostClientConfiguration{
		ChainID:         "",
		ChainHost:       "localhost",
		ChainHomeFolder: "~/.thorcli",
		SignerName:      "signer",
		SignerPasswd:    "signerpassword",
	}, NotNil, IsNil)
	testFunc(config.BifrostClientConfiguration{
		ChainID:         "chainid",
		ChainHost:       "",
		ChainHomeFolder: "~/.thorcli",
		SignerName:      "signer",
		SignerPasswd:    "signerpassword",
	}, NotNil, IsNil)
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Success(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, IsNil)
	c.Assert(accNumber, Equals, uint64(3))
	c.Assert(sequence, Equals, uint64(5))
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Fail(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = ""
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, NotNil)
	c.Assert(accNumber, Equals, uint64(0))
	c.Assert(sequence, Equals, uint64(0))
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Fail_500(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = "500"
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, NotNil)
	c.Assert(accNumber, Equals, uint64(0))
	c.Assert(sequence, Equals, uint64(0))
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Fail_Unmarshal(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/malformed.json"
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, NotNil)
	c.Assert(true, Equals, strings.HasPrefix(err.Error(), "failed to unmarshal account resp"))
	c.Assert(accNumber, Equals, uint64(0))
	c.Assert(sequence, Equals, uint64(0))
}

func (s *ThorchainSuite) TestEnsureNodeWhitelisted_Success(c *C) {
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	err := s.bridge.EnsureNodeWhitelisted()
	c.Assert(err, IsNil)
}

func (s *ThorchainSuite) TestEnsureNodeWhitelisted_Fail(c *C) {
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/disabled.json"
	err := s.bridge.EnsureNodeWhitelisted()
	c.Assert(err, NotNil)
}

func (s *ThorchainSuite) TestIsCatchingUp(c *C) {
	ok, err := s.bridge.IsCatchingUp()
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
}

func (s *ThorchainSuite) TestGetAsgards(c *C) {
	vaults, err := s.bridge.GetAsgards()
	c.Assert(err, IsNil)
	c.Assert(vaults, NotNil)
}

func (s *ThorchainSuite) TestGetPubKeys(c *C) {
	pks, err := s.bridge.GetPubKeys()
	c.Assert(err, IsNil)
	c.Assert(pks, HasLen, 8)
	foundFrost := false
	for _, pk := range pks {
		if pk.Algo == common.SigningAlgoEd25519 && pk.PubKey.String() == "tthorpub1zcjduepqfrostvaultpubkeyforxmrtest000000000000000000" {
			foundFrost = true
			break
		}
	}
	c.Assert(foundFrost, Equals, true)
}

func (s *ThorchainSuite) TestPostNetworkFee(c *C) {
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	txid, err := s.bridge.PostNetworkFee(1024, common.ETHChain, 100, 100)
	c.Assert(err, IsNil)
	c.Assert(txid.IsEmpty(), Equals, false)
}

func (s *ThorchainSuite) TestGetConstants(c *C) {
	result, err := s.bridge.GetConstants()
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *ThorchainSuite) TestGetRagnarok(c *C) {
	result, err := s.bridge.RagnarokInProgress()
	c.Assert(err, IsNil)
	c.Assert(result, Equals, false)
}

func (s *ThorchainSuite) TestGetThorchainVersion(c *C) {
	result, err := s.bridge.GetThorchainVersion()
	c.Assert(err, IsNil)
	c.Assert(result.EQ(stypes.GetCurrentVersion()), Equals, true)
}

func (s *ThorchainSuite) TestGetMimir(c *C) {
	result, err := s.bridge.GetMimir("HaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(result, Equals, int64(10))
}

func (s *ThorchainSuite) TestGetContractAddress(c *C) {
	result, err := s.bridge.GetContractAddress()
	c.Assert(err, IsNil)
	c.Assert(result[0].Contracts[common.ETHChain].String(), Equals, "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44")
}

func (s *ThorchainSuite) TestTHORName(c *C) {
	result, err := s.bridge.GetTHORName("test1")
	c.Assert(err, IsNil)
	c.Assert(result.Name, Equals, "test1")
	c.Assert(result.ExpireBlockHeight, Equals, int64(10000))
	c.Assert(result.Aliases, HasLen, 1)
	c.Assert(result.Aliases[0].Chain, Equals, common.THORChain)
	c.Assert(result.Aliases[0].Address, Equals, common.Address("tthor1tdfqy34uptx207scymqsy4k5uzfmry5sf7z3dw"))
}

// TestGetPendingOutbounds guards the /thorchain/queue/outbound decode + the
// height-query URL: the endpoint returns a BARE JSON array (not {tx_out_items}),
// and height>0 must send ?height= as a real query param (not a '?' escaped into
// the path, which a scheme-less ChainHost would do if built by hand).
func (s *ThorchainSuite) TestGetPendingOutbounds(c *C) {
	// live (height 0)
	live, err := s.bridge.GetPendingOutbounds(0)
	c.Assert(err, IsNil)
	c.Assert(live, HasLen, 2)
	c.Assert(live[0].Chain, Equals, "SOL")
	c.Assert(live[0].VaultPubKey, Equals, "vaultA")
	c.Assert(live[0].Height, Equals, int64(100))
	c.Assert(live[0].InHash, Equals, "HASH1")

	// as-of a consensus height — the server 400s if the height query did not arrive
	atHeight, err := s.bridge.GetPendingOutbounds(2_500_000)
	c.Assert(err, IsNil)
	c.Assert(atHeight, HasLen, 2)
	c.Assert(atHeight[1].Chain, Equals, "ETH")
}
