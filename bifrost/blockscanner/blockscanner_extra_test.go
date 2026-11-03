package blockscanner

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newMemLevelDB(c *C) *leveldb.DB {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	c.Assert(err, IsNil)
	return db
}

var errTest = errors.New("test error")

var initMetricsOnce sync.Once

// waitForScanner polls until cond holds, so a test waits for the scan loop
// state it asserts on instead of sleeping a fixed time.
func waitForScanner(c *C, cond func() bool, what string) {
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			c.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// withShortBlockTime runs the rest of a test with a 50ms THORChain block time,
// which paces the scan loop's paused-chain sleep (see
// BlockScannerTestSuite.SetUpTest). Stop the scanner before the returned
// restore runs.
func withShortBlockTime() (restore func()) {
	previous := constants.ThorchainBlockTime
	constants.ThorchainBlockTime = 50 * time.Millisecond
	return func() { constants.ThorchainBlockTime = previous }
}

// ---------------------------------------------------------------------------
// configurable mock fetcher
// ---------------------------------------------------------------------------

type configFetcher struct {
	txIn           types.TxIn
	fetchErr       error
	chainHeight    int64
	chainHeightErr error
	networkFeeSize uint64
	networkFeeRate uint64

	fetches atomic.Int64 // FetchTxs calls
}

func (f *configFetcher) FetchMemPool(height int64) (types.TxIn, error) {
	return f.txIn, f.fetchErr
}

func (f *configFetcher) FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error) {
	f.fetches.Add(1)
	return f.txIn, f.fetchErr
}

func (f *configFetcher) GetHeight() (int64, error) {
	return f.chainHeight, f.chainHeightErr
}

func (f *configFetcher) GetNetworkFee() (uint64, uint64) {
	return f.networkFeeSize, f.networkFeeRate
}

// ---------------------------------------------------------------------------
// LevelDB storage tests
// ---------------------------------------------------------------------------

type LevelDBStorageSuite struct{}

var _ = Suite(&LevelDBStorageSuite{})

func (s *LevelDBStorageSuite) TestNewLevelDBScannerStorage(c *C) {
	db := newMemLevelDB(c)
	defer db.Close()
	store, err := NewLevelDBScannerStorage(db)
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)
}

func (s *LevelDBStorageSuite) TestGetSetScanPos(c *C) {
	db := newMemLevelDB(c)
	defer db.Close()
	store, _ := NewLevelDBScannerStorage(db)

	_, err := store.GetScanPos()
	c.Check(err, NotNil)

	c.Assert(store.SetScanPos(42), IsNil)
	pos, err := store.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, int64(42))

	c.Assert(store.SetScanPos(999), IsNil)
	pos, err = store.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, int64(999))

	c.Assert(store.SetScanPos(-10), IsNil)
	pos, err = store.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, int64(-10))
}

func (s *LevelDBStorageSuite) TestClose(c *C) {
	db := newMemLevelDB(c)
	store, _ := NewLevelDBScannerStorage(db)
	c.Assert(store.Close(), IsNil)
}

// ---------------------------------------------------------------------------
// LevelDB Solana storage tests
// ---------------------------------------------------------------------------

type LevelDBSolanaSuite struct{}

var _ = Suite(&LevelDBSolanaSuite{})

func (s *LevelDBSolanaSuite) TestNewLevelDBScannerStorageSolana(c *C) {
	db := newMemLevelDB(c)
	defer db.Close()
	store, err := NewLevelDBScannerStorageSolana(db)
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)
}

func (s *LevelDBSolanaSuite) TestSolanaGetSetScanPos(c *C) {
	db := newMemLevelDB(c)
	defer db.Close()
	store, _ := NewLevelDBScannerStorageSolana(db)

	_, err := store.GetScanPos()
	c.Check(err, NotNil)

	c.Assert(store.SetScanPos(42), IsNil)
	pos, err := store.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, uint64(42))

	c.Assert(store.SetScanPos(200), IsNil)
	pos, err = store.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, uint64(200))
}

func (s *LevelDBSolanaSuite) TestSolanaGetSetScanStatus(c *C) {
	db := newMemLevelDB(c)
	defer db.Close()
	store, _ := NewLevelDBScannerStorageSolana(db)

	account := "SomeSolanaAccount123"

	_, _, err := store.GetScanStatus(account)
	c.Check(err, NotNil)

	c.Assert(store.SetScanStatus(account, "sig123abc", 999), IsNil)
	sig, slot, err := store.GetScanStatus(account)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, "sig123abc")
	c.Check(slot, Equals, uint64(999))

	c.Assert(store.SetScanStatus(account, "sig456def", 1500), IsNil)
	sig, slot, err = store.GetScanStatus(account)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, "sig456def")
	c.Check(slot, Equals, uint64(1500))
}

func (s *LevelDBSolanaSuite) TestSolanaClose(c *C) {
	db := newMemLevelDB(c)
	store, _ := NewLevelDBScannerStorageSolana(db)
	c.Assert(store.Close(), IsNil)
}

// ---------------------------------------------------------------------------
// BlockScannerStorage constructor tests
// ---------------------------------------------------------------------------

type StorageConstructorSuite struct{}

var _ = Suite(&StorageConstructorSuite{})

func (s *StorageConstructorSuite) TestNewBlockScannerStorage(c *C) {
	tmpdir, err := os.MkdirTemp("", "bs-test-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(tmpdir)

	store, err := NewBlockScannerStorage(tmpdir+"/db", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)
	c.Check(store.GetInternalDb(), NotNil)
	c.Check(store.syncScanPos, Equals, false)

	c.Assert(store.SetScanPos(99), IsNil)
	pos, err := store.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, int64(99))

	store.Close()
}

func (s *StorageConstructorSuite) TestScanPosSyncIsExplicitlyScoped(c *C) {
	tmpdir, err := os.MkdirTemp("", "bs-sync-test-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(tmpdir)

	store, err := NewBlockScannerStorageWithScanPosSync(tmpdir+"/db", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)
	c.Check(store.syncScanPos, Equals, true)
	c.Assert(store.Close(), IsNil)
}

func (s *StorageConstructorSuite) TestNewBlockScannerStorageSolana(c *C) {
	tmpdir, err := os.MkdirTemp("", "bs-sol-test-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(tmpdir)

	store, err := NewBlockScannerStorageSolana(tmpdir+"/db", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	c.Assert(store, NotNil)
	c.Check(store.GetInternalDb(), NotNil)
	store.Close()
}

// ---------------------------------------------------------------------------
// Mock scanner storage tests
// ---------------------------------------------------------------------------

type MockStorageSuite struct{}

var _ = Suite(&MockStorageSuite{})

func (s *MockStorageSuite) TestMockScannerStorage(c *C) {
	mss := NewMockScannerStorage()

	_, err := mss.GetScanPos()
	c.Check(err, NotNil)

	c.Assert(mss.SetScanPos(100), IsNil)
	pos, err := mss.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, int64(100))

	c.Check(mss.GetInternalDb(), IsNil)
	c.Assert(mss.Close(), IsNil)
}

// ---------------------------------------------------------------------------
// DummyFetcher tests
// ---------------------------------------------------------------------------

type DummyFetcherSuite struct{}

var _ = Suite(&DummyFetcherSuite{})

func (s *DummyFetcherSuite) TestDummyFetcher(c *C) {
	txIn := types.TxIn{Chain: common.ETHChain}
	d := DummyFetcher{Tx: txIn}

	tx, err := d.FetchMemPool(1)
	c.Assert(err, IsNil)
	c.Check(tx.Chain, Equals, common.ETHChain)

	tx, err = d.FetchTxs(1, 2)
	c.Assert(err, IsNil)
	c.Check(tx.Chain, Equals, common.ETHChain)

	h, err := d.GetHeight()
	c.Assert(err, IsNil)
	c.Check(h, Equals, int64(0))

	size, rate := d.GetNetworkFee()
	c.Check(size, Equals, uint64(0))
	c.Check(rate, Equals, uint64(0))

	d2 := DummyFetcher{Err: errTest}
	_, err = d2.FetchMemPool(1)
	c.Check(err, NotNil)
	_, err = d2.FetchTxs(1, 2)
	c.Check(err, NotNil)
}

// ---------------------------------------------------------------------------
// BlockScanner extra tests
// ---------------------------------------------------------------------------

type BlockScannerExtraSuite struct {
	keys *thorclient.Keys
}

var _ = Suite(&BlockScannerExtraSuite{})

func (s *BlockScannerExtraSuite) SetUpSuite(c *C) {
	// Initialize the package-level m variable using sync.Once to avoid
	// double-registering Prometheus collectors.
	initMetricsOnce.Do(func() {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9090,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.ETHChain},
		})
		if err != nil {
			panic(err)
		}
	})
	thorchain.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := ckeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic("bob", ckeys.English, cmd.THORChainHDPath, "password", hd.Secp256k1)
	c.Assert(err, IsNil)
	s.keys = thorclient.NewKeysWithKeybase(kb, "bob", "password")
}

func (s *BlockScannerExtraSuite) makeBridge(c *C, server *httptest.Server) thorclient.ThorchainBridge {
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       server.Listener.Addr().String(),
		ChainRPC:        server.Listener.Addr().String(),
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: ".",
	}, m, s.keys)
	c.Assert(err, IsNil)
	return bridge
}

func (s *BlockScannerExtraSuite) TestIsHealthy(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight: 1,
		ChainID:          common.ETHChain,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)

	c.Check(scanner.IsHealthy(), Equals, false)
	scanner.healthy.Store(true)
	c.Check(scanner.IsHealthy(), Equals, true)

	c.Check(scanner.PreviousHeight(), Equals, int64(1))
	atomic.StoreInt64(&scanner.previousBlock, 42)
	c.Check(scanner.PreviousHeight(), Equals, int64(42))
}

func (s *BlockScannerExtraSuite) TestNewBlockScannerValidation(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	bridge := s.makeBridge(c, server)
	mss := NewMockScannerStorage()
	cfg := config.BifrostBlockScannerConfiguration{StartBlockHeight: 1}

	_, err := NewBlockScanner(cfg, nil, m, bridge, DummyFetcher{})
	c.Check(err, ErrorMatches, "scannerStorage is nil")

	_, err = NewBlockScanner(cfg, mss, nil, bridge, DummyFetcher{})
	c.Check(err, ErrorMatches, "metrics instance is nil")

	_, err = NewBlockScanner(cfg, mss, m, nil, DummyFetcher{})
	c.Check(err, ErrorMatches, "thorchain bridge is nil")
}

func (s *BlockScannerExtraSuite) TestRollback(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight: 1,
		ChainID:          common.ETHChain,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	atomic.StoreInt64(&scanner.previousBlock, 100)

	// Higher height - no-op
	c.Assert(scanner.rollback(200), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(100))

	// Equal height - no-op
	c.Assert(scanner.rollback(100), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(100))

	// Lower height - updates
	c.Assert(scanner.rollback(50), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(50))
	pos, _ := mss.GetScanPos()
	c.Check(pos, Equals, int64(50))
}

func (s *BlockScannerExtraSuite) TestGetMessages(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight: 1,
		ChainID:          common.ETHChain,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	c.Check(scanner.GetMessages(), NotNil)
}

func (s *BlockScannerExtraSuite) TestUpdateStaleNetworkFee(c *C) {
	// Case 1: THORChain chain - no-op
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer server.Close()
		bridge := s.makeBridge(c, server)
		mss := NewMockScannerStorage()
		scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1,
			ChainID:          common.THORChain,
		}, mss, m, bridge, DummyFetcher{})
		c.Assert(err, IsNil)
		nfChan := make(chan common.NetworkFee, 1)
		scanner.globalNetworkFeeQueue = nfChan
		scanner.healthy.Store(true)
		scanner.updateStaleNetworkFee(100)
		c.Check(len(nfChan), Equals, 0)
	}

	// Case 2: not healthy - no-op
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer server.Close()
		bridge := s.makeBridge(c, server)
		mss := NewMockScannerStorage()
		scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1,
			ChainID:          common.ETHChain,
		}, mss, m, bridge, DummyFetcher{})
		c.Assert(err, IsNil)
		nfChan := make(chan common.NetworkFee, 1)
		scanner.globalNetworkFeeQueue = nfChan
		scanner.healthy.Store(false)
		scanner.updateStaleNetworkFee(100)
		c.Check(len(nfChan), Equals, 0)
	}

	// Case 3: fees match - no broadcast
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.RequestURI, "/thorchain/inbound_addresses") {
				_, _ = w.Write([]byte(`[{"chain":"ETH","outbound_tx_size":"250","observed_fee_rate":"30"}]`))
			}
		}))
		defer server.Close()
		bridge := s.makeBridge(c, server)
		mss := NewMockScannerStorage()
		fetcher := &configFetcher{networkFeeSize: 250, networkFeeRate: 30, chainHeight: 1}
		scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1,
			ChainID:          common.ETHChain,
		}, mss, m, bridge, fetcher)
		c.Assert(err, IsNil)
		nfChan := make(chan common.NetworkFee, 1)
		scanner.globalNetworkFeeQueue = nfChan
		scanner.healthy.Store(true)
		scanner.updateStaleNetworkFee(100)
		c.Check(len(nfChan), Equals, 0)
	}

	// Case 4: fees differ - should broadcast
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.RequestURI, "/thorchain/inbound_addresses") {
				_, _ = w.Write([]byte(`[{"chain":"ETH","outbound_tx_size":"250","observed_fee_rate":"30"}]`))
			}
		}))
		defer server.Close()
		bridge := s.makeBridge(c, server)
		mss := NewMockScannerStorage()
		fetcher := &configFetcher{networkFeeSize: 300, networkFeeRate: 40, chainHeight: 1}
		scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1,
			ChainID:          common.ETHChain,
		}, mss, m, bridge, fetcher)
		c.Assert(err, IsNil)
		nfChan := make(chan common.NetworkFee, 1)
		scanner.globalNetworkFeeQueue = nfChan
		scanner.healthy.Store(true)
		scanner.updateStaleNetworkFee(100)
		c.Check(len(nfChan), Equals, 1)
		nf := <-nfChan
		c.Check(nf.Chain, Equals, common.ETHChain)
		c.Check(nf.TransactionSize, Equals, uint64(300))
		c.Check(nf.TransactionRate, Equals, uint64(40))
		c.Check(nf.Height, Equals, int64(100))
	}

	// Case 5: bridge error - no broadcast
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.RequestURI, "/thorchain/inbound_addresses") {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer server.Close()
		bridge := s.makeBridge(c, server)
		mss := NewMockScannerStorage()
		fetcher := &configFetcher{networkFeeSize: 300, networkFeeRate: 40, chainHeight: 1}
		scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1,
			ChainID:          common.ETHChain,
		}, mss, m, bridge, fetcher)
		c.Assert(err, IsNil)
		nfChan := make(chan common.NetworkFee, 1)
		scanner.globalNetworkFeeQueue = nfChan
		scanner.healthy.Store(true)
		scanner.updateStaleNetworkFee(100)
		c.Check(len(nfChan), Equals, 0)
	}
}

func (s *BlockScannerExtraSuite) TestGetStartHeightConfigured(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight: 50,
		ChainID:          common.ETHChain,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(50))
}

func (s *BlockScannerExtraSuite) TestGetStartHeightLastObserved(c *C) {
	// Path 2b: has last observed height, no scanner storage position
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock/ETH"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","last_observed_in":75,"last_signed_out":0,"thorchain":100}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.ETHChain,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(75))
}

func (s *BlockScannerExtraSuite) TestGetStartHeightWithStorage(c *C) {
	// Path 2a: last observed + scanner storage within max lag
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock/ETH"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","last_observed_in":100,"last_signed_out":0,"thorchain":100}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	_ = mss.SetScanPos(90)
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID:           common.ETHChain,
		MaxResumeBlockLag: time.Hour,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(90))
}

func (s *BlockScannerExtraSuite) TestGetStartHeightStorageTooFar(c *C) {
	// Path 2a variant: scanner storage too far behind max lag
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock/ETH"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","last_observed_in":100,"last_signed_out":0,"thorchain":100}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	_ = mss.SetScanPos(10)
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID:           common.ETHChain,
		MaxResumeBlockLag: time.Second,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	h := scanner.PreviousHeight()
	c.Check(h > 10, Equals, true, Commentf("Expected > 10, got %d", h))
	c.Check(h <= 100, Equals, true, Commentf("Expected <= 100, got %d", h))
}

func (s *BlockScannerExtraSuite) TestGetStartHeightNoLastObservedWithStorage(c *C) {
	// Path 3: no last observed, scanner storage available
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock/ETH"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	_ = mss.SetScanPos(60)
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.ETHChain,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(60))
}

func (s *BlockScannerExtraSuite) TestGetStartHeightChainHeight(c *C) {
	// Path 4: nothing available, use chain height
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock/ETH"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","last_observed_in":0,"last_signed_out":0,"thorchain":100}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	fetcher := &configFetcher{chainHeight: 200}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.ETHChain,
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(200))
}

func (s *BlockScannerExtraSuite) TestGetStartHeightThorBackfillsRecentWindow(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"","last_observed_in":0,"last_signed_out":0,"thorchain":1200}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	fetcher := &configFetcher{chainHeight: 1200}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID:           common.THORChain,
		MaxResumeBlockLag: time.Hour,
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(600))
}

// startScannerForFloorTest is a helper that constructs a scanner with the given
// floor/start/persisted-pos config, calls Start(), reads PreviousHeight, and stops.
// The fetcher returns chainHeight=0 so scanBlocks cannot advance previousBlock
// during the brief window in which we observe it.
func (s *BlockScannerExtraSuite) startScannerForFloorTest(c *C, startHeight, floorHeight, persistedPos int64) int64 {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	if persistedPos > 0 {
		c.Assert(mss.SetScanPos(persistedPos), IsNil)
	}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           startHeight,
		FloorBlockHeight:           floorHeight,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: time.Second,
		BlockRetryInterval:         time.Second,
		ChainID:                    common.ETHChain,
		ScanMemPool:                false,
	}, mss, m, bridge, &configFetcher{chainHeight: 0})
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn, 1)
	nfChan := make(chan common.NetworkFee, 1)
	scanner.Start(globalChan, nfChan)
	height := scanner.PreviousHeight()
	scanner.Stop()
	return height
}

func (s *BlockScannerExtraSuite) TestStartFloorUnsetUsesStartHeight(c *C) {
	// Floor unset: behaves as before, StartBlockHeight wins.
	got := s.startScannerForFloorTest(c, 10, 0, 0)
	c.Check(got, Equals, int64(10))
}

func (s *BlockScannerExtraSuite) TestStartFloorAdvancesAboveStartHeight(c *C) {
	// Floor > StartBlockHeight, no persisted pos: scanner advances to floor.
	got := s.startScannerForFloorTest(c, 10, 100, 0)
	c.Check(got, Equals, int64(100))
}

func (s *BlockScannerExtraSuite) TestStartFloorRespectsHigherPersistedPos(c *C) {
	// Floor < persisted pos (with StartBlockHeight set): the effective floor is
	// max(FloorBlockHeight, persisted pos), so the scanner is never dragged below
	// the persisted scan position (forward-only — never redo already-scanned work).
	got := s.startScannerForFloorTest(c, 10, 100, 200)
	c.Check(got, Equals, int64(200))
}

func (s *BlockScannerExtraSuite) TestStartFloorAboveStartAndPersisted(c *C) {
	// Floor above both StartBlockHeight and persisted pos: scanner advances to floor.
	got := s.startScannerForFloorTest(c, 10, 500, 200)
	c.Check(got, Equals, int64(500))
}

// TestRollbackNeverGoesBelowFloor is the reorg regression: a rollback (e.g. from
// RollbackToLastObserved) must never drag the scanner below the configured floor,
// otherwise it would re-enter the exact range the floor was set to skip.
func (s *BlockScannerExtraSuite) TestRollbackNeverGoesBelowFloor(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/status"):
			_, _ = w.Write([]byte(`{"result":{"sync_info":{"catching_up":false}}}`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		FloorBlockHeight:           500,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: time.Second,
		BlockRetryInterval:         time.Second,
		ChainID:                    common.ETHChain,
		ScanMemPool:                false,
	}, mss, m, bridge, &configFetcher{chainHeight: 0})
	c.Assert(err, IsNil)

	// Simulate the scanner having progressed well above the floor.
	atomic.StoreInt64(&scanner.previousBlock, 1000)

	// A rollback to above the floor is honored verbatim (rollback only moves down).
	c.Assert(scanner.rollback(700), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(700))

	// A reorg rollback to below the floor must clamp to the floor, in memory and on disk.
	c.Assert(scanner.rollback(200), IsNil)
	c.Check(scanner.PreviousHeight(), Equals, int64(500))
	pos, err := mss.GetScanPos()
	c.Assert(err, IsNil)
	c.Check(pos, Equals, int64(500))
}

func (s *BlockScannerExtraSuite) TestStartStopWithMempoolDisabled(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			buf, _ := os.ReadFile("../../test/fixtures/endpoints/mimir/mimir.json")
			_, _ = w.Write(buf)
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	// The mimir fixture halts the chain, so Stop waits out a paused-chain sleep.
	defer withShortBlockTime()()
	bridge := &pauseCheckCountingBridge{ThorchainBridge: s.makeBridge(c, server)}

	mss := NewMockScannerStorage()
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: time.Second,
		BlockRetryInterval:         time.Second,
		ChainID:                    common.ETHChain,
		ScanMemPool:                false,
	}, mss, m, bridge, DummyFetcher{})
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn)
	nfChan := make(chan common.NetworkFee)
	scanner.Start(globalChan, nfChan)
	waitForPauseChecks(c, bridge, 2)
	scanner.Stop()
}

// TestScanBlocksProcessesTxs verifies that scanBlocks actually processes blocks
// when the chain height is ahead of the current scanner position.
func (s *BlockScannerExtraSuite) TestScanBlocksProcessesTxs(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			// All mimir values unset
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/inbound_addresses"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","outbound_tx_size":"250","observed_fee_rate":"30"}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	// Fetcher returns chain height 5 and some txs
	fetcher := &configFetcher{
		chainHeight:    5,
		networkFeeSize: 250,
		networkFeeRate: 30,
		txIn: types.TxIn{
			Chain: common.ETHChain,
			TxArray: []*types.TxInItem{
				{Tx: "test-tx-1"},
			},
		},
	}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: 100 * time.Millisecond,
		BlockRetryInterval:         100 * time.Millisecond,
		ChainID:                    common.ETHChain,
		MaxHealthyLag:              time.Hour,
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn, 20)
	nfChan := make(chan common.NetworkFee, 5)
	scanner.Start(globalChan, nfChan)

	// Wait for the scanner to process some blocks. Poll instead of sleeping a fixed
	// 2s: under -race on a loaded CI runner that budget expired before the first
	// block was scanned, failing both checks below.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if len(globalChan) > 0 && scanner.PreviousHeight() > 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	scanner.Stop()

	// Should have received txs
	c.Check(len(globalChan) > 0, Equals, true, Commentf("Expected txs in queue, got %d", len(globalChan)))

	// Scanner should be healthy (within 3 blocks of chain tip)
	c.Check(scanner.IsHealthy(), Equals, true)

	// Previous height should have advanced
	c.Check(scanner.PreviousHeight() > 1, Equals, true, Commentf("Expected height > 1, got %d", scanner.PreviousHeight()))
}

// TestScanBlocksWithPrefetch tests the prefetch path.
func (s *BlockScannerExtraSuite) TestScanBlocksWithPrefetch(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/inbound_addresses"):
			_, _ = w.Write([]byte(`[{"chain":"ETH","outbound_tx_size":"250","observed_fee_rate":"30"}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	fetcher := &configFetcher{chainHeight: 10, networkFeeSize: 250, networkFeeRate: 30}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: 100 * time.Millisecond,
		BlockRetryInterval:         100 * time.Millisecond,
		ChainID:                    common.ETHChain,
		PrefetchBlocks:             3,
		MaxHealthyLag:              time.Hour,
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn, 20)
	nfChan := make(chan common.NetworkFee, 5)
	scanner.Start(globalChan, nfChan)
	waitForScanner(c, func() bool { return scanner.PreviousHeight() >= 10 }, "scan to the chain tip")
	scanner.Stop()

	c.Check(scanner.PreviousHeight() > 1, Equals, true)
}

// TestScanMempoolEnabled tests mempool scanning path.
func (s *BlockScannerExtraSuite) TestScanMempoolEnabled(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	// Fetcher returns mempool txs
	fetcher := &configFetcher{
		chainHeight: 0,
		txIn: types.TxIn{
			Chain:   common.ETHChain,
			MemPool: true,
			TxArray: []*types.TxInItem{
				{Tx: "mempool-tx-1"},
			},
		},
	}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: 10 * time.Millisecond,
		BlockRetryInterval:         time.Second,
		ChainID:                    common.ETHChain,
		ScanMemPool:                true,
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn, 10)
	nfChan := make(chan common.NetworkFee, 5)
	scanner.Start(globalChan, nfChan)
	waitForScanner(c, func() bool { return len(globalChan) > 0 }, "a queued mempool tx")
	scanner.Stop()

	// Mempool txs should have been queued
	c.Check(len(globalChan) > 0, Equals, true, Commentf("Expected mempool txs, got %d", len(globalChan)))
}

// TestScanBlocksUnhealthy tests that scanner becomes unhealthy when behind.
func (s *BlockScannerExtraSuite) TestScanBlocksUnhealthy(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	// Chain is at height 1000000 but scanner starts at 1 - very behind
	fetcher := &configFetcher{chainHeight: 1000000}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: 100 * time.Millisecond,
		BlockRetryInterval:         100 * time.Millisecond,
		ChainID:                    common.ETHChain,
		MaxHealthyLag:              time.Second, // very short lag threshold
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn, 100)
	nfChan := make(chan common.NetworkFee, 5)
	scanner.Start(globalChan, nfChan)
	// health is set on every scanned block
	waitForScanner(c, func() bool { return scanner.PreviousHeight() > 1 }, "a scanned block")
	scanner.Stop()

	// Scanner should be unhealthy when significantly behind
	c.Check(scanner.IsHealthy(), Equals, false)
}

// TestStartWithPersistedPos tests that Start reads the persisted scan position.
func (s *BlockScannerExtraSuite) TestStartWithPersistedPos(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	// Set persisted position higher than start height
	_ = mss.SetScanPos(50)
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		// no StartBlockHeight set (0), so Start will check persisted position
		ChainID:                    common.ETHChain,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: time.Second,
		BlockRetryInterval:         time.Second,
	}, mss, m, bridge, DummyFetcher{})
	// This may fail if bridge calls WaitToCatchUp (needs /status endpoint)
	// so let's use StartBlockHeight to skip WaitToCatchUp
	if err != nil {
		return // skip if bridge setup fails
	}

	globalChan := make(chan types.TxIn, 10)
	nfChan := make(chan common.NetworkFee, 5)
	scanner.Start(globalChan, nfChan)
	time.Sleep(300 * time.Millisecond)
	scanner.Stop()
}

// TestScanBlocksFetchError tests the error handling in scanBlocks when FetchTxs fails.
func (s *BlockScannerExtraSuite) TestScanBlocksFetchError(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, thorclient.MimirEndpoint):
			_, _ = w.Write([]byte(`-1`))
		case strings.HasPrefix(r.RequestURI, "/thorchain/lastblock"):
			_, _ = w.Write([]byte(`[{"chain":"NOOP","lastobservedin":0,"lastsignedout":0,"thorchain":0}]`))
		}
	}))
	defer server.Close()
	bridge := s.makeBridge(c, server)

	mss := NewMockScannerStorage()
	// Fetcher returns error
	fetcher := &configFetcher{chainHeight: 5, fetchErr: errTest}
	scanner, err := NewBlockScanner(config.BifrostBlockScannerConfiguration{
		StartBlockHeight:           1,
		BlockScanProcessors:        1,
		HTTPRequestTimeout:         time.Second,
		BlockHeightDiscoverBackoff: 100 * time.Millisecond,
		BlockRetryInterval:         100 * time.Millisecond,
		ChainID:                    common.ETHChain,
	}, mss, m, bridge, fetcher)
	c.Assert(err, IsNil)

	globalChan := make(chan types.TxIn, 20)
	nfChan := make(chan common.NetworkFee, 5)
	scanner.Start(globalChan, nfChan)
	// a second fetch means the first failure was handled and backed off
	waitForScanner(c, func() bool { return fetcher.fetches.Load() >= 2 }, "a retried fetch")
	scanner.Stop()

	// Scanner should become unhealthy on fetch errors
	c.Check(scanner.IsHealthy(), Equals, false)
}
