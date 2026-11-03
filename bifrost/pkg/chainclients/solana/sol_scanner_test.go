package solana

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

const TestGasPriceResolution = 1

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

func getConfigForTest(rpcHost string) config.BifrostBlockScannerConfiguration {
	return config.BifrostBlockScannerConfiguration{
		ChainID:                    common.SOLChain,
		StartBlockHeight:           1, // avoids querying thorchain for block height
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		HTTPRequestReadTimeout:     time.Second * 30,
		HTTPRequestWriteTimeout:    time.Second * 30,
		MaxHTTPRequestRetry:        3,
		BlockHeightDiscoverBackoff: time.Second,
		BlockRetryInterval:         time.Second,
		GasCacheBlocks:             100,
		Concurrency:                1,
		GasPriceResolution:         TestGasPriceResolution, // 50 navax
		TransactionBatchSize:       500,
	}
}

type BlockScannerTestSuite struct {
	m      *metrics.Metrics
	bridge thorclient.ThorchainBridge
	keys   *thorclient.Keys
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob-ecdsa",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	thorKeys := thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)
	s.keys = thorKeys
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, thorKeys)
	c.Assert(err, IsNil)
}

func (s *BlockScannerTestSuite) TestGetBlock(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch req.RequestURI {
		case thorclient.PubKeysEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
		default:
			body, err := io.ReadAll(req.Body)
			c.Assert(err, IsNil)
			type RPCRequest struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      interface{}     `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}
			var rpcRequest RPCRequest
			err = json.Unmarshal(body, &rpcRequest)
			c.Assert(err, IsNil)
			switch rpcRequest.Method {
			case "getBlock":
				httpTestHandler(c, rw, "./test/getBlock.json")
			case "getSlot":
				httpTestHandler(c, rw, "./test/getSlot.json")
			case "getLatestBlockHash":
				httpTestHandler(c, rw, "./test/getLatestBlockHash.json")
			default:
				rw.WriteHeader(404)
			}
		}
	}))

	storage, err := blockscanner.NewBlockScannerStorageSolana("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	signerCacheManager, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)
	pubKeyManager, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)

	cfg := getConfigForTest("http://" + server.Listener.Addr().String())

	rpcClient := rpc.NewSolRPC("http://"+server.Listener.Addr().String(), cfg.HTTPRequestTimeout)
	_, err = NewSOLScanner(make(chan struct{}), cfg, storage, s.bridge, s.m, rpcClient, pubKeyManager, nil, signerCacheManager)
	c.Assert(err, IsNil)

	// TODO
}

// TestScanVaultOmitsMinContextSlotWithUntilCursor guards the fix for the idle-vault
// -32020 wedge: when scanVault has an until-cursor, it must NOT also pin
// minContextSlot (which tracks the near-tip scan position). If it did, an idle
// vault whose cursor sig is below minContextSlot makes the RPC return -32020 and
// marks the whole scanner unhealthy. The captured on-wire params assert the exact
// request shape, not just the Go struct.
func (s *BlockScannerTestSuite) TestScanVaultOmitsMinContextSlotWithUntilCursor(c *C) {
	// capturedRaw holds the raw getSignaturesForAddress params object (params = [address,
	// paramsObj]) from the most recent request. Capture in the handler, but ASSERT in the
	// test goroutine — a failing c.Assert inside an httptest handler is a gocheck footgun.
	var capturedRaw json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.RequestURI == thorclient.PubKeysEndpoint {
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/vaults/pubKeys.json")
			return
		}
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &rpcReq) == nil &&
			rpcReq.Method == "getSignaturesForAddress" && len(rpcReq.Params) >= 2 {
			capturedRaw = rpcReq.Params[1]
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`)) // empty -> healthy break
	}))
	defer server.Close()

	storage, err := blockscanner.NewBlockScannerStorageSolana("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	signerCacheManager, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)
	pubKeyManager, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	cfg := getConfigForTest("http://" + server.Listener.Addr().String())
	rpcClient := rpc.NewSolRPC("http://"+server.Listener.Addr().String(), cfg.HTTPRequestTimeout)
	scanner, err := NewSOLScanner(make(chan struct{}), cfg, storage, s.bridge, s.m, rpcClient, pubKeyManager, nil, signerCacheManager)
	c.Assert(err, IsNil)

	const minCtx = uint64(428_000_000)
	// run drives one scanVault and returns the captured on-wire params, asserted here in
	// the test goroutine (not the handler).
	run := func(cursor string) map[string]any {
		capturedRaw = nil
		var wg sync.WaitGroup
		wg.Add(1)
		unhealthy := false
		ok := make([]bool, 1)
		scanner.scanVault("Vault1111111111111111111111111111111111111", 0, cursor, minCtx,
			func() { unhealthy = true }, make([]int, 1), ok, make([]string, 1), &wg)
		wg.Wait()
		c.Assert(unhealthy, Equals, false)
		c.Assert(ok[0], Equals, true)
		c.Assert(capturedRaw, NotNil)
		params := map[string]any{}
		c.Assert(json.Unmarshal(capturedRaw, &params), IsNil)
		return params
	}

	// With an until-cursor: minContextSlot OMITTED (the fix), the cursor wired through as
	// until, and no before on the first page.
	const cursorSig = "5QUofaQtCursorSig1111111111111111111111111111111111111111111111111"
	withCursor := run(cursorSig)
	_, hasMCS := withCursor["minContextSlot"]
	c.Assert(hasMCS, Equals, false, Commentf("minContextSlot must be omitted with cursor: %v", withCursor))
	c.Assert(withCursor["until"], Equals, cursorSig) // wired through, not merely present
	_, hasBefore := withCursor["before"]
	c.Assert(hasBefore, Equals, false) // first page, no before cursor

	// No cursor (first scan / StartBlockHeight floor): minContextSlot KEPT, no until.
	noCursor := run("")
	mcs, hasMCS2 := noCursor["minContextSlot"]
	c.Assert(hasMCS2, Equals, true, Commentf("minContextSlot must be kept without cursor: %v", noCursor))
	mcsF, ok := mcs.(float64)
	c.Assert(ok, Equals, true)
	c.Assert(uint64(mcsF), Equals, minCtx)
	_, hasUntil := noCursor["until"]
	c.Assert(hasUntil, Equals, false)
}
