package blockscanner

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type scannerHaltTestBridge struct {
	thorclient.ThorchainBridge

	mu               sync.Mutex
	mimirCalls       []string
	getMimir         func(string) (int64, error)
	blockHeight      int64
	blockHeightCalls atomic.Int32
}

func (b *scannerHaltTestBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	b.mimirCalls = append(b.mimirCalls, key)
	b.mu.Unlock()
	return b.getMimir(key)
}

func (b *scannerHaltTestBridge) GetBlockHeight() (int64, error) {
	b.blockHeightCalls.Add(1)
	return b.blockHeight, nil
}

func (b *scannerHaltTestBridge) calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.mimirCalls...)
}

func TestXMRScannerHalt(t *testing.T) {
	tests := []struct {
		name       string
		haltValue  int64
		mimirErr   error
		wantPaused bool
	}{
		{name: "unset", haltValue: -1, wantPaused: true},
		{name: "any negative value fails closed", haltValue: -2, wantPaused: true},
		{name: "zero explicitly opens", wantPaused: false},
		{name: "positive value pauses", haltValue: 1, wantPaused: true},
		{name: "all positive values pause", haltValue: 101, wantPaused: true},
		{name: "mimir query error", mimirErr: errors.New("mimir unavailable"), wantPaused: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge := &scannerHaltTestBridge{
				getMimir: func(key string) (int64, error) {
					if key != mimirHaltScannerXMR {
						t.Fatalf("unexpected Mimir lookup %q", key)
					}
					return test.haltValue, test.mimirErr
				},
			}

			if got := IsXMRScannerPaused(zerolog.Nop(), bridge); got != test.wantPaused {
				t.Fatalf("IsXMRScannerPaused() = %t, want %t", got, test.wantPaused)
			}
			if got := bridge.calls(); len(got) != 1 || got[0] != mimirHaltScannerXMR {
				t.Fatalf("Mimir lookups = %v, want [%s]", got, mimirHaltScannerXMR)
			}
			if got := bridge.blockHeightCalls.Load(); got != 0 {
				t.Fatalf("GetBlockHeight calls = %d, want 0", got)
			}
		})
	}
}

func TestIsChainPausedUsesXMRScannerHalt(t *testing.T) {
	type pauseCase struct {
		name            string
		haltValue       int64
		mimirErr        error
		mimirValues     map[string]int64
		wantPaused      bool
		wantHeightCalls int32
		wantCalls       []string
	}
	tests := []pauseCase{
		{
			name:       "active halt stops before shared halt lookups",
			haltValue:  1,
			wantPaused: true,
			wantCalls:  []string{mimirHaltScannerXMR},
		},
		{
			name:       "policy read error stops before shared halt lookups",
			mimirErr:   errors.New("scanner policy unavailable"),
			wantPaused: true,
			wantCalls:  []string{mimirHaltScannerXMR},
		},
		{
			name:            "clear halt continues shared scanner work",
			wantHeightCalls: 1,
			wantCalls: []string{
				mimirHaltScannerXMR,
				"HaltXMRChain",
				"SolvencyHaltXMRChain",
				"HaltChainGlobal",
				"NodePauseChainGlobal",
			},
		},
		{
			name:            "clear scanner halt preserves existing XMR chain halt",
			mimirValues:     map[string]int64{"HaltXMRChain": 1},
			wantPaused:      true,
			wantHeightCalls: 1,
			wantCalls: []string{
				mimirHaltScannerXMR,
				"HaltXMRChain",
			},
		},
	}
	sharedHalts := []string{"HaltXMRChain", "SolvencyHaltXMRChain", "HaltChainGlobal", "NodePauseChainGlobal"}
	for i, key := range sharedHalts {
		for _, value := range []int64{99, 100, 101} {
			paused := value <= 100
			if key == "NodePauseChainGlobal" {
				paused = value >= 100
			}
			calls := append([]string{mimirHaltScannerXMR}, sharedHalts...)
			if paused {
				calls = calls[:i+2]
			}
			tests = append(tests, pauseCase{
				name:            fmt.Sprintf("clear scanner gate/%s/%d", key, value),
				mimirValues:     map[string]int64{key: value},
				wantPaused:      paused,
				wantHeightCalls: 1,
				wantCalls:       calls,
			})
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge := &scannerHaltTestBridge{
				getMimir: func(key string) (int64, error) {
					if key == mimirHaltScannerXMR {
						return test.haltValue, test.mimirErr
					}
					if value, ok := test.mimirValues[key]; ok {
						return value, nil
					}
					return 0, nil
				},
				blockHeight: 100,
			}

			cfg := config.BifrostBlockScannerConfiguration{ChainID: common.XMRChain}
			if got := IsChainPaused(cfg, zerolog.Nop(), bridge); got != test.wantPaused {
				t.Fatalf("IsChainPaused() = %t, want %t", got, test.wantPaused)
			}
			if got := bridge.blockHeightCalls.Load(); got != test.wantHeightCalls {
				t.Fatalf("GetBlockHeight calls = %d, want %d", got, test.wantHeightCalls)
			}
			if got := bridge.calls(); !slices.Equal(got, test.wantCalls) {
				t.Fatalf("Mimir lookups = %v, want %v", got, test.wantCalls)
			}
		})
	}
}

func TestIsChainPausedSkipsScannerHaltForNonXMR(t *testing.T) {
	bridge := &scannerHaltTestBridge{
		getMimir: func(key string) (int64, error) {
			if key == "HaltScannerETH" || key == mimirHaltScannerXMR {
				t.Fatalf("non-XMR scanner issued irrelevant scanner-halt lookup %q", key)
			}
			return 0, nil
		},
		blockHeight: 100,
	}

	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.ETHChain}
	if IsChainPaused(cfg, zerolog.Nop(), bridge) {
		t.Fatal("ETH scanner was paused")
	}
	if got := bridge.blockHeightCalls.Load(); got != 1 {
		t.Fatalf("GetBlockHeight calls = %d, want 1", got)
	}
	wantMimirCalls := []string{
		"HaltETHChain",
		"SolvencyHaltETHChain",
		"HaltChainGlobal",
		"NodePauseChainGlobal",
	}
	if got := bridge.calls(); !slices.Equal(got, wantMimirCalls) {
		t.Fatalf("Mimir lookups = %v, want %v", got, wantMimirCalls)
	}
}

type activationTestFetcher struct {
	chain        common.Chain
	tip          atomic.Int64
	fetched      []int64
	mempoolCalls atomic.Int32
}

func (f *activationTestFetcher) GetHeight() (int64, error) { return f.tip.Load(), nil }

func (f *activationTestFetcher) FetchTxs(height, _ int64) (types.TxIn, error) {
	f.fetched = append(f.fetched, height)
	return types.TxIn{Chain: f.chain, TxArray: []*types.TxInItem{{BlockHeight: height, Tx: fmt.Sprint(height)}}}, nil
}

func (f *activationTestFetcher) FetchMemPool(int64) (types.TxIn, error) {
	f.mempoolCalls.Add(1)
	return types.TxIn{}, nil
}

func (*activationTestFetcher) GetNetworkFee() (uint64, uint64) { return 1, 1 }

type activationTestBridge struct{ scannerHaltTestBridge }

func (*activationTestBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 1, 1, nil
}

func activationScannerDefaults(t *testing.T) config.Config {
	t.Helper()
	// Read the repository defaults without inheriting the caller's environment
	// or mutating the global configuration shared by the other test suites.
	v := viper.New()
	v.SetConfigFile("../../config/default.yaml")
	if err := v.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	var defaults config.Config
	if err := v.Unmarshal(&defaults); err != nil {
		t.Fatal(err)
	}
	cfg := defaults.Bifrost.Chains.XMR.BlockScanner
	if cfg.ScanMemPool || cfg.BlockHeightDiscoverBackoff != 30*time.Second {
		t.Fatalf("unexpected XMR scanner defaults: %+v", cfg)
	}
	return defaults
}

func newActivationTestScanner(t *testing.T, cfg config.BifrostBlockScannerConfiguration, bridge *activationTestBridge) (*BlockScanner, *activationTestFetcher, *MockScannerStorage, chan types.TxIn) {
	t.Helper()
	// A known stored cursor isolates activation from birthday discovery.
	cfg.StartBlockHeight = 1
	fetcher := &activationTestFetcher{chain: cfg.ChainID}
	fetcher.tip.Store(2)
	storage := NewMockScannerStorage()
	if err := storage.SetScanPos(1); err != nil {
		t.Fatal(err)
	}
	scanner, err := NewBlockScanner(cfg, storage, &metrics.Metrics{}, bridge, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	scanner.logger = zerolog.Nop()
	queue := make(chan types.TxIn, 4)
	scanner.Start(queue, make(chan common.NetworkFee, 4))
	return scanner, fetcher, storage, queue
}

func TestXMRScannerActivationRetriesWithoutMovingCursorAndKeepsETHScanning(t *testing.T) {
	defaults := activationScannerDefaults(t)
	cfg := defaults.Bifrost.Chains.XMR.BlockScanner
	synctest.Test(t, func(t *testing.T) {
		var policyMu sync.Mutex
		halt, policyErr := int64(-1), error(nil)
		setPolicy := func(value int64, err error) {
			policyMu.Lock()
			defer policyMu.Unlock()
			halt, policyErr = value, err
		}
		bridge := &activationTestBridge{scannerHaltTestBridge: scannerHaltTestBridge{
			blockHeight: 100,
			getMimir: func(key string) (int64, error) {
				if key == mimirHaltScannerXMR {
					policyMu.Lock()
					defer policyMu.Unlock()
					return halt, policyErr
				}
				return 0, nil
			},
		}}
		xmr, xmrFetcher, storage, queue := newActivationTestScanner(t, cfg, bridge)
		defer xmr.Stop()
		ethCfg := defaults.Bifrost.Chains.ETH.BlockScanner
		eth, ethFetcher, _, _ := newActivationTestScanner(t, ethCfg, bridge)
		defer eth.Stop()
		synctest.Wait()

		assertPaused := func() {
			t.Helper()
			pos, err := storage.GetScanPos()
			if err != nil || pos != 1 || xmr.PreviousHeight() != 1 || xmr.IsHealthy() || len(xmrFetcher.fetched) != 0 || len(queue) != 0 {
				t.Fatalf("closed gate advanced scanner: pos=%d cursor=%d healthy=%t fetched=%v deliveries=%d err=%v", pos, xmr.PreviousHeight(), xmr.IsHealthy(), xmrFetcher.fetched, len(queue), err)
			}
		}
		assertPaused()
		if eth.PreviousHeight() != 2 || !eth.IsHealthy() {
			t.Fatal("unset XMR policy prevented ETH progress")
		}
		// Even a zero result must stay closed when the policy read fails.
		setPolicy(0, errors.New("policy temporarily unavailable"))
		ethFetcher.tip.Store(3)
		for range 2 {
			time.Sleep(max(constants.ThorchainBlockTime, ethCfg.BlockHeightDiscoverBackoff))
			synctest.Wait()
			assertPaused()
			if eth.PreviousHeight() != 3 || !eth.IsHealthy() {
				t.Fatal("unreadable XMR policy prevented ETH progress")
			}
		}
		setPolicy(0, nil)
		time.Sleep(constants.ThorchainBlockTime)
		synctest.Wait()
		if xmr.PreviousHeight() != 2 || !xmr.IsHealthy() || len(queue) != 1 {
			t.Fatal("explicit activation did not resume the existing scanner")
		}
		// A read outage must also close a previously open scanner. Its cursor
		// remains unchanged while another chain advances on the same bridge.
		setPolicy(0, errors.New("running scanner policy unavailable"))
		xmrFetcher.tip.Store(3)
		ethFetcher.tip.Store(4)
		time.Sleep(cfg.BlockHeightDiscoverBackoff)
		synctest.Wait()
		pos, err := storage.GetScanPos()
		if err != nil || pos != 2 || xmr.PreviousHeight() != 2 || xmr.IsHealthy() || len(queue) != 1 {
			t.Fatal("running XMR scanner crossed a policy read outage")
		}
		if eth.PreviousHeight() != 4 || !eth.IsHealthy() {
			t.Fatal("XMR policy read outage prevented ETH progress")
		}
		setPolicy(1, nil)
		time.Sleep(constants.ThorchainBlockTime)
		synctest.Wait()
		if xmr.PreviousHeight() != 2 || xmr.IsHealthy() || len(queue) != 1 {
			t.Fatal("renewed scanner halt resumed work")
		}
		setPolicy(0, nil)
		time.Sleep(constants.ThorchainBlockTime)
		synctest.Wait()
		if xmr.PreviousHeight() != 3 || !xmr.IsHealthy() || !slices.Equal(xmrFetcher.fetched, []int64{2, 3}) || len(queue) != 2 {
			t.Fatalf("reactivation lost or duplicated work: cursor=%d fetched=%v deliveries=%d", xmr.PreviousHeight(), xmrFetcher.fetched, len(queue))
		}
		if xmrFetcher.mempoolCalls.Load() != 0 {
			t.Fatal("scanner activation bypassed the disabled mempool default")
		}
	})
}

func TestXMRScannerStopJoinsPausedWorker(t *testing.T) {
	cfg := activationScannerDefaults(t).Bifrost.Chains.XMR.BlockScanner
	synctest.Test(t, func(t *testing.T) {
		bridge := &activationTestBridge{scannerHaltTestBridge: scannerHaltTestBridge{
			getMimir: func(string) (int64, error) { return 0, errors.New("policy unavailable") },
		}}
		scanner, fetcher, storage, queue := newActivationTestScanner(t, cfg, bridge)
		var stopOnce sync.Once
		defer stopOnce.Do(scanner.Stop)
		synctest.Wait()
		stopped := make(chan struct{})
		go func() {
			stopOnce.Do(scanner.Stop)
			close(stopped)
		}()
		// The current pause uses one block-time sleep; Stop owns and joins that
		// worker, but does not promise to interrupt the sleep immediately.
		time.Sleep(constants.ThorchainBlockTime)
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("Stop did not join the paused scanner after its polling interval")
		}
		calls := len(bridge.calls())
		time.Sleep(2 * cfg.BlockHeightDiscoverBackoff)
		synctest.Wait()
		pos, err := storage.GetScanPos()
		if err != nil || pos != 1 || len(fetcher.fetched) != 0 || len(queue) != 0 || len(bridge.calls()) != calls {
			t.Fatal("scanner retained work after Stop returned")
		}
	})
}
