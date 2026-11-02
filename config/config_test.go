package config

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	. "gopkg.in/check.v1"
	"gopkg.in/yaml.v2"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestPackage(t *testing.T) { TestingT(t) }

type Test struct{}

var _ = Suite(&Test{})

func (Test) SetUpSuite(c *C) {
	// failure-path fetch tests retry MaxRetries times; keep the retries, skip the waiting
	RetryBackoff = time.Millisecond
}

func (Test) TestAllChainsReturned(c *C) {
	b := Bifrost{}
	c.Assert(len(b.GetChains()), Equals, reflect.TypeOf(b.Chains).NumField())
}

func (Test) TestAllDefaultDefined(c *C) {
	// In order to override configuration values, defaults must first be defined
	// in the default YAML file. This test ensures all fields have defaults defined.

	confMap := map[interface{}]interface{}{}
	err := yaml.Unmarshal(defaultConfig, &confMap)
	c.Assert(err, IsNil)

	// recursive check defaults for all fields in config struct
	check(c, []string{}, confMap, reflect.TypeOf(Config{}))
}

func (Test) TestFrostKeygenTimeoutDefault(c *C) {
	// Lock in the default FROST keygen timeout. FROST DKG is a 3-round p2p
	// protocol and historically inherited KeygenTimeout, which was too
	// aggressive in some environments. This default should match the
	// existing production KeygenTimeout (5m).
	confMap := map[interface{}]interface{}{}
	err := yaml.Unmarshal(defaultConfig, &confMap)
	c.Assert(err, IsNil)

	bifrostRaw, ok := confMap["bifrost"]
	c.Assert(ok, Equals, true)
	bifrostMap, ok := bifrostRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	signerRaw, ok := bifrostMap["signer"]
	c.Assert(ok, Equals, true)
	signerMap, ok := signerRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	frostTimeout, ok := signerMap["frost_keygen_timeout"]
	c.Assert(ok, Equals, true, Commentf("default.yaml must define bifrost.signer.frost_keygen_timeout"))
	c.Assert(frostTimeout, Equals, "5m")
}

func (Test) TestXMRSignerEndpointDefault(c *C) {
	confMap := map[interface{}]interface{}{}
	err := yaml.Unmarshal(defaultConfig, &confMap)
	c.Assert(err, IsNil)

	bifrostRaw, ok := confMap["bifrost"]
	c.Assert(ok, Equals, true)
	bifrostMap, ok := bifrostRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	chainsRaw, ok := bifrostMap["chains"]
	c.Assert(ok, Equals, true)
	chainsMap, ok := chainsRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	xmrRaw, ok := chainsMap["xmr"]
	c.Assert(ok, Equals, true)
	xmrMap, ok := xmrRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	signerEndpoint, ok := xmrMap["signer_endpoint"]
	c.Assert(ok, Equals, true)
	c.Assert(signerEndpoint, Equals, "http://127.0.0.1:8283")
}

func check(c *C, path []string, cm map[interface{}]interface{}, t reflect.Type) {
	// ensure all config fields are defined
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("mapstructure")
		tagPath := strings.Join(append(path, tag), ".")

		// skip some fields, since there are environment variables we won't override
		if strings.HasPrefix(tagPath, "bifrost.signer.block_scanner") {
			continue
		}
		if strings.HasPrefix(tagPath, "bifrost.thorchain.back_off") {
			continue
		}
		if t.Field(i).Name == "SignerPasswd" {
			continue
		}

		// assert the field is defined in config
		if _, ok := cm[tag]; !ok {
			c.Fatalf("missing default for %s %s", tagPath, t.Field(i).Type)
		}

		switch t.Field(i).Type.Kind() {

		case reflect.Struct: // recurse if this is a nested struct
			nested, ok := cm[tag].(map[interface{}]interface{})
			c.Assert(ok, Equals, true)
			check(c, append(path, tag), nested, t.Field(i).Type)

		case reflect.Map: // recurse on each value if this is a map
			nested, ok := cm[tag].(map[interface{}]interface{})
			c.Assert(ok, Equals, true)
			for k, v := range nested {
				key, ok := k.(string)
				c.Assert(ok, Equals, true)
				value, ok := v.(map[interface{}]interface{})
				c.Assert(ok, Equals, true)
				check(c, append(path, tag, key), value, t.Field(i).Type.Elem())
			}
		}
	}

	// ensure all defaults are defined in config
	for k := range cm {
		key, ok := k.(string)
		c.Assert(ok, Equals, true)

		found := false
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).Tag.Get("mapstructure") == key {
				found = true
				break
			}
		}
		if !found {
			c.Fatalf("unknown mapping: %s", strings.Join(append(path, key), "."))
		}
	}
}

// -------------------------------------------------------------------------------------
// Init and Global Getters
// -------------------------------------------------------------------------------------

func (Test) TestInit(c *C) {
	Init()

	// After Init, the global config should be populated from defaults
	tn := GetThornode()
	c.Assert(tn.Tendermint.Consensus.TimeoutCommit > 0, Equals, true)

	bf := GetBifrost()
	c.Assert(bf.Metrics.Enabled, Equals, true)
	c.Assert(bf.TSS.Rendezvous, Equals, "asgard")
}

func (Test) TestGetThornode(c *C) {
	Init()
	tn := GetThornode()
	// Should return a copy (value type)
	c.Assert(tn.Cosmos.Pruning, Not(Equals), "")
}

func (Test) TestGetBifrost(c *C) {
	Init()
	bf := GetBifrost()
	chains := bf.GetChains()
	c.Assert(len(chains) > 0, Equals, true)
}

func (Test) TestInitSetsListenAddresses(c *C) {
	// Reset config to clear any previously set listen addresses
	config = Config{}
	Init()

	tn := GetThornode()
	c.Assert(tn.Tendermint.RPC.ListenAddress, Not(Equals), "")
	c.Assert(strings.Contains(tn.Tendermint.RPC.ListenAddress, "tcp://"), Equals, true)
	c.Assert(tn.Tendermint.P2P.ListenAddress, Not(Equals), "")
	c.Assert(strings.Contains(tn.Tendermint.P2P.ListenAddress, "tcp://"), Equals, true)
	c.Assert(tn.Cosmos.EBifrost.Address, Not(Equals), "")
}

// -------------------------------------------------------------------------------------
// InitBifrost
// -------------------------------------------------------------------------------------

func (Test) TestInitBifrost(c *C) {
	Init()

	// Override all paths to use temp directories
	tmpDir := c.MkDir()
	config.Bifrost.Signer.SignerDbPath = filepath.Join(tmpDir, "signer")

	// Override all chain scanner DB paths
	config.Bifrost.Chains.BTC.BlockScanner.DBPath = filepath.Join(tmpDir, "btc")
	config.Bifrost.Chains.ETH.BlockScanner.DBPath = filepath.Join(tmpDir, "eth")
	config.Bifrost.Chains.AVAX.BlockScanner.DBPath = filepath.Join(tmpDir, "avax")
	config.Bifrost.Chains.BCH.BlockScanner.DBPath = filepath.Join(tmpDir, "bch")
	config.Bifrost.Chains.BSC.BlockScanner.DBPath = filepath.Join(tmpDir, "bsc")
	config.Bifrost.Chains.DOGE.BlockScanner.DBPath = filepath.Join(tmpDir, "doge")
	config.Bifrost.Chains.GAIA.BlockScanner.DBPath = filepath.Join(tmpDir, "gaia")
	config.Bifrost.Chains.NOBLE.BlockScanner.DBPath = filepath.Join(tmpDir, "noble")
	config.Bifrost.Chains.LTC.BlockScanner.DBPath = filepath.Join(tmpDir, "ltc")
	config.Bifrost.Chains.BASE.BlockScanner.DBPath = filepath.Join(tmpDir, "base")
	config.Bifrost.Chains.TRON.BlockScanner.DBPath = filepath.Join(tmpDir, "tron")
	config.Bifrost.Chains.XRP.BlockScanner.DBPath = filepath.Join(tmpDir, "xrp")
	config.Bifrost.Chains.XMR.BlockScanner.DBPath = filepath.Join(tmpDir, "xmr")
	config.Bifrost.Chains.SOL.BlockScanner.DBPath = filepath.Join(tmpDir, "sol")
	config.Bifrost.Chains.POL.BlockScanner.DBPath = filepath.Join(tmpDir, "pol")
	config.Bifrost.Chains.ZEC.BlockScanner.DBPath = filepath.Join(tmpDir, "zec")
	config.Bifrost.Chains.SUI.BlockScanner.DBPath = filepath.Join(tmpDir, "sui")
	config.Bifrost.Chains.ADA.BlockScanner.DBPath = filepath.Join(tmpDir, "ada")
	config.Bifrost.Chains.DASH.BlockScanner.DBPath = filepath.Join(tmpDir, "dash")
	config.Bifrost.Chains.PIVX.BlockScanner.DBPath = filepath.Join(tmpDir, "pivx")

	// Clear bootstrap peers so it uses getSeedAddrs (returns empty in mocknet)
	config.Bifrost.TSS.BootstrapPeers = nil

	InitBifrost()

	// Verify directories were created
	_, err := os.Stat(filepath.Join(tmpDir, "signer"))
	c.Assert(err, IsNil)
	_, err = os.Stat(filepath.Join(tmpDir, "btc"))
	c.Assert(err, IsNil)
	_, err = os.Stat(filepath.Join(tmpDir, "eth"))
	c.Assert(err, IsNil)
}

// -------------------------------------------------------------------------------------
// GetProviders
// -------------------------------------------------------------------------------------

func (Test) TestGetProviders(c *C) {
	Init()
	bf := GetBifrost()
	providers := bf.GetProviders()
	c.Assert(len(providers) > 0, Equals, true)

	// Verify all known provider keys exist
	_, ok := providers[common.ProviderBinance]
	c.Assert(ok, Equals, true)
	_, ok = providers[common.ProviderKraken]
	c.Assert(ok, Equals, true)
	_, ok = providers[common.ProviderCoinbase]
	c.Assert(ok, Equals, true)
}

// -------------------------------------------------------------------------------------
// LevelDBOptions
// -------------------------------------------------------------------------------------

func (Test) TestLevelDBOptionsEmpty(c *C) {
	opts := LevelDBOptions{}
	o := opts.Options()
	c.Assert(o, NotNil)
	c.Assert(o.Filter, IsNil)
	c.Assert(o.WriteBuffer, Equals, 0)
	c.Assert(o.BlockCacheCapacity, Equals, 0)
}

func (Test) TestLevelDBOptionsAllSet(c *C) {
	opts := LevelDBOptions{
		FilterBitsPerKey:              10,
		CompactionTableSizeMultiplier: 2.0,
		WriteBuffer:                   1024,
		BlockCacheCapacity:            2048,
	}
	o := opts.Options()
	c.Assert(o.CompactionTableSizeMultiplier, Equals, 2.0)
	c.Assert(o.WriteBuffer, Equals, 1024)
	c.Assert(o.BlockCacheCapacity, Equals, 2048)
	c.Assert(o.Filter, NotNil) // bloom filter set
}

func (Test) TestLevelDBOptionsPartialSet(c *C) {
	// Only filter bits set
	opts := LevelDBOptions{
		FilterBitsPerKey: 5,
	}
	o := opts.Options()
	c.Assert(o.Filter, NotNil)
	c.Assert(o.WriteBuffer, Equals, 0)

	// Only write buffer set
	opts = LevelDBOptions{
		WriteBuffer: 4096,
	}
	o = opts.Options()
	c.Assert(o.Filter, IsNil)
	c.Assert(o.WriteBuffer, Equals, 4096)
}

// -------------------------------------------------------------------------------------
// BifrostTSSConfiguration Getters
// -------------------------------------------------------------------------------------

func (Test) TestBifrostTSSGetP2PPort(c *C) {
	tss := BifrostTSSConfiguration{P2PPort: 5040}
	c.Assert(tss.GetP2PPort(), Equals, 5040)
}

func (Test) TestBifrostTSSGetRendezvous(c *C) {
	tss := BifrostTSSConfiguration{Rendezvous: "asgard"}
	c.Assert(tss.GetRendezvous(), Equals, "asgard")
}

func (Test) TestBifrostTSSGetExternalIP(c *C) {
	tss := BifrostTSSConfiguration{ExternalIP: "1.2.3.4"}
	c.Assert(tss.GetExternalIP(), Equals, "1.2.3.4")
}

// -------------------------------------------------------------------------------------
// GetBootstrapPeers
// -------------------------------------------------------------------------------------

func (Test) TestGetBootstrapPeersEmpty(c *C) {
	tss := BifrostTSSConfiguration{}
	addrs, err := tss.GetBootstrapPeers()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 0)
}

func (Test) TestGetBootstrapPeersWithServer(c *C) {
	// Create a test HTTP server that returns a valid p2p ID
	p2pID := "12D3KooWBnRXhSzH2P5yFn8ZLShzLWKvKaJN9BHP2hGKQeZmFD4P"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "p2pid") {
			_, _ = w.Write([]byte(p2pID))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	// Save and replace httpClient with a custom one that redirects to our server
	oldClient := httpClient
	defer func() { httpClient = oldClient }()

	httpClient = &http.Client{
		Transport: &testTransport{targetURL: ts.URL},
	}

	// Use raw IP so resolveAddrs passes it through
	tss := BifrostTSSConfiguration{
		BootstrapPeers: []string{"127.0.0.1"},
	}
	addrs, err := tss.GetBootstrapPeers()
	c.Assert(err, IsNil)
	c.Assert(len(addrs), Equals, 1)
}

func (Test) TestGetBootstrapPeersHTTPError(c *C) {
	// Save and replace httpClient with one that always fails
	oldClient := httpClient
	defer func() { httpClient = oldClient }()

	httpClient = &http.Client{
		Transport: &errorTransport{},
	}

	tss := BifrostTSSConfiguration{
		BootstrapPeers: []string{"192.0.2.1"},
	}
	addrs, err := tss.GetBootstrapPeers()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 0)
}

func (Test) TestGetBootstrapPeersBadStatus(c *C) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	httpClient = &http.Client{
		Transport: &testTransport{targetURL: ts.URL},
	}

	tss := BifrostTSSConfiguration{
		BootstrapPeers: []string{"1.2.3.4"},
	}
	addrs, err := tss.GetBootstrapPeers()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 0)
}

func (Test) TestGetBootstrapPeersBadMultiaddr(c *C) {
	// Return an invalid p2p ID that will cause multiaddr parsing to fail
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-a-valid-peer-id-!!!"))
	}))
	defer ts.Close()

	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	httpClient = &http.Client{
		Transport: &testTransport{targetURL: ts.URL},
	}

	tss := BifrostTSSConfiguration{
		BootstrapPeers: []string{"1.2.3.4"},
	}
	addrs, err := tss.GetBootstrapPeers()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 0)
}

func (Test) TestGetBootstrapPeersSkipsEmptyIP(c *C) {
	tss := BifrostTSSConfiguration{
		BootstrapPeers: []string{""},
	}
	addrs, err := tss.GetBootstrapPeers()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 0)
}

// testTransport redirects all requests to a target URL.
type testTransport struct {
	targetURL string
	// underlying is the transport to use for the actual request.
	// If nil, uses the real DefaultTransport (saved before replacement).
	underlying http.RoundTripper
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := t.targetURL + req.URL.RequestURI()
	newReq, err := http.NewRequest(req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	transport := t.underlying
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(newReq)
}

// errorTransport always returns an error.
type errorTransport struct{}

func (t *errorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, http.ErrServerClosed
}

// -------------------------------------------------------------------------------------
// Validate
// -------------------------------------------------------------------------------------

func (Test) TestValidateBTC(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.BTCChain,
		RPCHost: "localhost:8332",
	}
	cfg.UTXO.MaxMempoolAncestors = 25
	cfg.Validate() // should not panic
}

func (Test) TestValidateETH(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: "localhost:8545",
	}
	cfg.Validate() // should not panic - default case, no UTXO check
}

func (Test) TestValidateTRON(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.TRONChain,
		APIHost: "localhost:8090",
	}
	cfg.Validate() // should not panic
}

func (Test) TestValidateBCH(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.BCHChain,
		RPCHost: "localhost:8332",
	}
	// BCH has no ancestor limit, any value is fine
	cfg.UTXO.MaxMempoolAncestors = 100
	cfg.Validate()
}

func (Test) TestValidateLTC(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.LTCChain,
		RPCHost: "localhost:9332",
	}
	cfg.UTXO.MaxMempoolAncestors = 10
	cfg.Validate()
}

func (Test) TestValidateDOGE(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.DOGEChain,
		RPCHost: "localhost:22555",
	}
	cfg.UTXO.MaxMempoolAncestors = 0
	cfg.Validate()
}

// -------------------------------------------------------------------------------------
// resolveAddrs
// -------------------------------------------------------------------------------------

func (Test) TestResolveAddrsEmpty(c *C) {
	result := resolveAddrs([]string{})
	c.Assert(result, HasLen, 0)
}

func (Test) TestResolveAddrsIPPassthrough(c *C) {
	result := resolveAddrs([]string{"1.2.3.4"})
	c.Assert(result, DeepEquals, []string{"1.2.3.4"})
}

func (Test) TestResolveAddrsMultipleIPs(c *C) {
	result := resolveAddrs([]string{"1.2.3.4", "5.6.7.8"})
	c.Assert(result, HasLen, 2)
	c.Assert(result[0], Equals, "1.2.3.4")
	c.Assert(result[1], Equals, "5.6.7.8")
}

func (Test) TestResolveAddrsHostname(c *C) {
	result := resolveAddrs([]string{"localhost"})
	c.Assert(len(result), Equals, 1)
}

func (Test) TestResolveAddrsUnresolvable(c *C) {
	result := resolveAddrs([]string{"this-host-does-not-exist-xyz123.invalid"})
	c.Assert(result, HasLen, 0)
}

func (Test) TestResolveAddrsMixed(c *C) {
	result := resolveAddrs([]string{"1.2.3.4", "this-host-does-not-exist-xyz123.invalid", "5.6.7.8"})
	c.Assert(result, HasLen, 2)
	c.Assert(result[0], Equals, "1.2.3.4")
	c.Assert(result[1], Equals, "5.6.7.8")
}

// -------------------------------------------------------------------------------------
// getSeedAddrs (mocknet)
// -------------------------------------------------------------------------------------

func (Test) TestGetSeedAddrsMocknet(c *C) {
	result := getSeedAddrs()
	c.Assert(result, HasLen, 0)
}

// -------------------------------------------------------------------------------------
// assertBifrostHasSeeds (mocknet)
// -------------------------------------------------------------------------------------

func (Test) TestAssertBifrostHasSeedsMocknet(c *C) {
	// In mocknet, assertBifrostHasSeeds checks for a seed file.
	// It should not panic regardless of whether the file exists.
	assertBifrostHasSeeds()
}

func (Test) TestAssertBifrostHasSeedsWithFile(c *C) {
	// Create a temp seed file so the function exercises the file stat path
	tmpDir := c.MkDir()
	seedPath := filepath.Join(tmpDir, "address_book.seed")
	err := os.WriteFile(seedPath, []byte("seed data"), 0o600)
	c.Assert(err, IsNil)

	// Set HOME to temp dir so the function finds our seed file
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Create the .thornode directory structure
	err = os.MkdirAll(filepath.Join(tmpDir, ".thornode"), 0o755)
	c.Assert(err, IsNil)
	err = os.WriteFile(filepath.Join(tmpDir, ".thornode", "address_book.seed"), []byte("seed data"), 0o600)
	c.Assert(err, IsNil)

	assertBifrostHasSeeds()
}

func (Test) TestAssertBifrostHasSeedsEmptyFile(c *C) {
	tmpDir := c.MkDir()
	err := os.MkdirAll(filepath.Join(tmpDir, ".thornode"), 0o755)
	c.Assert(err, IsNil)
	err = os.WriteFile(filepath.Join(tmpDir, ".thornode", "address_book.seed"), []byte{}, 0o600)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	assertBifrostHasSeeds()
}

// -------------------------------------------------------------------------------------
// thornodeSeeds
// -------------------------------------------------------------------------------------

func (Test) TestThornodeSeedsEmpty(c *C) {
	// With no SEEDS env, mocknet getSeedAddrs returns empty
	os.Unsetenv("SEEDS")
	seedAddrs, tmSeeds := thornodeSeeds()
	c.Assert(seedAddrs, HasLen, 0)
	c.Assert(tmSeeds, HasLen, 0)
}

func (Test) TestThornodeSeedsFromEnvEmpty(c *C) {
	os.Setenv("SEEDS", "")
	defer os.Unsetenv("SEEDS")
	seedAddrs, tmSeeds := thornodeSeeds()
	c.Assert(seedAddrs, HasLen, 0)
	c.Assert(tmSeeds, HasLen, 0)
}

func (Test) TestThornodeSeedsFromEnvWithSeeds(c *C) {
	// Set SEEDS to IPs that won't respond - exercises the HTTP retry path
	// but will ultimately return empty tmSeeds since no server is listening
	os.Setenv("SEEDS", "192.0.2.1")
	os.Setenv("CHAIN_ID", "thorchain-mocknet-v1")
	defer os.Unsetenv("SEEDS")
	defer os.Unsetenv("CHAIN_ID")

	// Override httpClient to fail fast
	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	httpClient = &http.Client{
		Transport: &errorTransport{},
	}

	seedAddrs, tmSeeds := thornodeSeeds()
	c.Assert(seedAddrs, HasLen, 1)
	c.Assert(seedAddrs[0], Equals, "192.0.2.1")
	c.Assert(tmSeeds, HasLen, 0)
}

func (Test) TestThornodeSeedsFromEnvWithServerMatch(c *C) {
	// Create a test server that responds with matching network
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"node_info":{"id":"abc123","network":"test-chain"}}}`))
	}))
	defer ts.Close()

	os.Setenv("SEEDS", "127.0.0.1")
	os.Setenv("CHAIN_ID", "test-chain")
	defer os.Unsetenv("SEEDS")
	defer os.Unsetenv("CHAIN_ID")

	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	httpClient = &http.Client{
		Transport: &testTransport{targetURL: ts.URL},
	}

	seedAddrs, tmSeeds := thornodeSeeds()
	c.Assert(seedAddrs, HasLen, 1)
	c.Assert(len(tmSeeds), Equals, 1)
	c.Assert(strings.Contains(tmSeeds[0], "abc123"), Equals, true)
}

func (Test) TestThornodeSeedsMultipleSeeds(c *C) {
	// Test with multiple SEEDS that all match
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"node_info":{"id":"node1","network":"test-net"}}}`))
	}))
	defer ts.Close()

	os.Setenv("SEEDS", "127.0.0.1,127.0.0.2")
	os.Setenv("CHAIN_ID", "test-net")
	defer os.Unsetenv("SEEDS")
	defer os.Unsetenv("CHAIN_ID")

	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	httpClient = &http.Client{
		Transport: &testTransport{targetURL: ts.URL},
	}

	seedAddrs, tmSeeds := thornodeSeeds()
	c.Assert(seedAddrs, HasLen, 2)
	c.Assert(len(tmSeeds) >= 1, Equals, true)
}

// -------------------------------------------------------------------------------------
// InitThornode
// -------------------------------------------------------------------------------------

func (Test) TestInitThornode(c *C) {
	Init()

	// Set up a temp HOME with the required directory structure
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Set THORNODE_ env var to exercise the warning path
	os.Setenv("THORNODE_TEST_VAR", "test")
	defer os.Unsetenv("THORNODE_TEST_VAR")

	// Set EXTERNAL_IP to exercise that path
	os.Setenv("EXTERNAL_IP", "1.2.3.4")
	defer os.Unsetenv("EXTERNAL_IP")

	// Clear SEEDS so thornodeSeeds returns empty (skip genesis fetch)
	os.Unsetenv("SEEDS")

	// Ensure auto_state_sync is disabled
	config.Thornode.AutoStateSync.Enabled = false

	InitThornode(context.Background())

	// Verify template files were created
	_, err = os.Stat(filepath.Join(configDir, "config.toml"))
	c.Assert(err, IsNil)
	_, err = os.Stat(filepath.Join(configDir, "app.toml"))
	c.Assert(err, IsNil)

	// Verify external address was set
	tn := GetThornode()
	c.Assert(strings.Contains(tn.Tendermint.P2P.ExternalAddress, "1.2.3.4"), Equals, true)
}

func (Test) TestInitThornodeWithSeeds(c *C) {
	Init()

	// Set up a temp HOME
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Set up matching seeds server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "status") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":{"node_info":{"id":"seedid","network":"test-chain"}}}`))
		} else if strings.Contains(r.URL.Path, "genesis_chunked") {
			// Return a valid genesis chunk
			genesisData := base64.StdEncoding.EncodeToString([]byte(`{"genesis_time":"2024-01-01T00:00:00Z"}`))
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, genesisData)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	os.Setenv("SEEDS", "127.0.0.1")
	os.Setenv("CHAIN_ID", "test-chain")
	os.Unsetenv("EXTERNAL_IP")
	defer os.Unsetenv("SEEDS")
	defer os.Unsetenv("CHAIN_ID")

	// Override httpClient for thornodeSeeds
	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	httpClient = &http.Client{
		Transport: &testTransport{targetURL: ts.URL},
	}

	// Override http.DefaultTransport for thornodeFetchGenesis (which uses http.Get)
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	config.Thornode.AutoStateSync.Enabled = false

	InitThornode(context.Background())

	// Verify genesis was fetched
	genesisPath := filepath.Join(configDir, "genesis.json")
	data, err := os.ReadFile(genesisPath)
	c.Assert(err, IsNil)
	c.Assert(len(data) > 0, Equals, true)
}

// -------------------------------------------------------------------------------------
// thornodeFetchGenesis
// -------------------------------------------------------------------------------------

func (Test) TestThornodeFetchGenesisExistingFile(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	// Create a non-empty genesis file
	genesisPath := filepath.Join(configDir, "genesis.json")
	err = os.WriteFile(genesisPath, []byte(`{"genesis":"existing"}`), 0o600)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Should return early since genesis already exists
	thornodeFetchGenesis([]string{"1.2.3.4"})

	// Genesis file should remain unchanged
	data, err := os.ReadFile(genesisPath)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, `{"genesis":"existing"}`)
}

func (Test) TestThornodeFetchGenesisViaServer(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Create test server that serves genesis in chunks
	genesisContent := `{"chain_id":"test","genesis_time":"2024-01-01T00:00:00Z"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(genesisContent))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, encoded)
		_, _ = w.Write([]byte(resp))
	}))
	defer ts.Close()

	// Override http.DefaultTransport for http.Get used by thornodeFetchGenesis
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})

	// Verify genesis was written
	genesisPath := filepath.Join(configDir, "genesis.json")
	data, err := os.ReadFile(genesisPath)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, genesisContent)
}

func (Test) TestThornodeFetchGenesisMultiChunk(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Multi-chunk genesis
	chunk0 := base64.StdEncoding.EncodeToString([]byte(`{"part`))
	chunk1 := base64.StdEncoding.EncodeToString([]byte(`":"two"}`))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunkParam := r.URL.Query().Get("chunk")
		if chunkParam == "0" {
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"2"}}`, chunk0)
			_, _ = w.Write([]byte(resp))
		} else if chunkParam == "1" {
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"1","total":"2"}}`, chunk1)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})

	genesisPath := filepath.Join(configDir, "genesis.json")
	data, err := os.ReadFile(genesisPath)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, `{"part":"two"}`)
}

func (Test) TestThornodeFetchGenesisInvalidBase64(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= MaxRetries {
			// Return invalid base64 for first attempts
			_, _ = w.Write([]byte(`{"result":{"data":"!!!invalid-base64!!!","chunk":"0","total":"1"}}`))
		} else {
			// Return valid data after retries
			encoded := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})
}

func (Test) TestThornodeFetchGenesisInvalidJSON(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= MaxRetries {
			_, _ = w.Write([]byte(`not valid json`))
		} else {
			encoded := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})
}

func (Test) TestThornodeFetchGenesisInvalidChunkNumber(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		encoded := base64.StdEncoding.EncodeToString([]byte(`test`))
		if callCount <= MaxRetries {
			// Return non-numeric chunk number
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"abc","total":"1"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		} else {
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})
}

func (Test) TestThornodeFetchGenesisInvalidTotalNumber(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		encoded := base64.StdEncoding.EncodeToString([]byte(`test`))
		if callCount <= MaxRetries {
			// Return non-numeric total
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"xyz"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		} else {
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})
}

func (Test) TestThornodeFetchGenesisHTTPError(c *C) {
	tmpDir := c.MkDir()
	configDir := filepath.Join(tmpDir, ".thornode", "config")
	err := os.MkdirAll(configDir, 0o755)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= MaxRetries {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			encoded := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
			resp := fmt.Sprintf(`{"result":{"data":"%s","chunk":"0","total":"1"}}`, encoded)
			_, _ = w.Write([]byte(resp))
		}
	}))
	defer ts.Close()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = &testTransport{targetURL: ts.URL, underlying: oldTransport}

	thornodeFetchGenesis([]string{"127.0.0.1"})
}

// -------------------------------------------------------------------------------------
// thornodeAutoStateSync
// -------------------------------------------------------------------------------------

func (Test) TestThornodeAutoStateSyncExistingData(c *C) {
	tmpDir := c.MkDir()
	dataDir := filepath.Join(tmpDir, ".thornode", "data")
	err := os.MkdirAll(dataDir, 0o755)
	c.Assert(err, IsNil)
	err = os.WriteFile(filepath.Join(dataDir, "state.db"), []byte("state"), 0o600)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Should return early since state.db exists
	thornodeAutoStateSync(context.Background())
}

func (Test) TestThornodeAutoStateSyncExistingDataReturnsEarly(c *C) {
	tmpDir := c.MkDir()
	dataDir := filepath.Join(tmpDir, ".thornode", "data")
	err := os.MkdirAll(dataDir, 0o755)
	c.Assert(err, IsNil)
	err = os.WriteFile(filepath.Join(dataDir, "state.db"), []byte("state"), 0o600)
	c.Assert(err, IsNil)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	Init()
	config.Thornode.AutoStateSync.Enabled = true

	// Should return early since state.db exists, without modifying statesync config
	thornodeAutoStateSync(context.Background())
	c.Assert(config.Thornode.Tendermint.StateSync.Enable, Equals, false)
}

// -------------------------------------------------------------------------------------
// Struct methods
// -------------------------------------------------------------------------------------

func (Test) TestBifrostChainConfigGetChainID(c *C) {
	cfg := BifrostChainConfiguration{
		ChainID: common.BTCChain,
	}
	_ = cfg.ChainID.String()
}

func (Test) TestBifrostClientConfigFields(c *C) {
	clientCfg := BifrostClientConfiguration{
		ChainID:         common.THORChain,
		ChainHost:       "localhost:1317",
		ChainRPC:        "localhost:26657",
		ChainEBifrost:   "localhost:50051",
		ChainHomeFolder: "/home/thornode",
		SignerName:      "thorchain",
		SignerPasswd:    "password",
	}
	c.Assert(clientCfg.ChainID, Equals, common.THORChain)
	c.Assert(clientCfg.ChainHost, Equals, "localhost:1317")
}

func (Test) TestWhitelistCosmosAssetFields(c *C) {
	asset := WhitelistCosmosAsset{
		Denom:           "uatom",
		Decimals:        6,
		THORChainSymbol: "ATOM",
	}
	c.Assert(asset.Denom, Equals, "uatom")
	c.Assert(asset.Decimals, Equals, 6)
	c.Assert(asset.THORChainSymbol, Equals, "ATOM")
}

func (Test) TestBifrostMetricsConfigFields(c *C) {
	metrics := BifrostMetricsConfiguration{
		Enabled:      true,
		PprofEnabled: false,
		ListenPort:   9000,
	}
	c.Assert(metrics.Enabled, Equals, true)
	c.Assert(metrics.ListenPort, Equals, 9000)
}

func (Test) TestBifrostOracleConfigFields(c *C) {
	oracle := BifrostOracleConfiguration{
		LogLevel:        "info",
		Enabled:         true,
		DetailedMetrics: false,
	}
	c.Assert(oracle.Enabled, Equals, true)
	c.Assert(oracle.LogLevel, Equals, "info")
}

func (Test) TestAttestationStateSyncTimeoutDefault(c *C) {
	confMap := map[interface{}]interface{}{}
	c.Assert(yaml.Unmarshal(defaultConfig, &confMap), IsNil)
	bifrostMap, ok := confMap["bifrost"].(map[interface{}]interface{})
	c.Assert(ok, Equals, true)
	gossipMap, ok := bifrostMap["attestation_gossip"].(map[interface{}]interface{})
	c.Assert(ok, Equals, true)
	c.Assert(gossipMap["state_sync_timeout"], Equals, "30m")
}
