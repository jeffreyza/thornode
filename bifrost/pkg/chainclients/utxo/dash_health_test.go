package utxo

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

func TestDashHeightHealthBoundaries(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		health  *chainLockHeightHealth
		scanned int64
		want    bool
	}{
		{"no successful read", nil, 100, false},
		{"missing timestamp", &chainLockHeightHealth{blockHeight: 100, chainLockHeight: 100}, 100, false},
		{"caught up", &chainLockHeightHealth{100, 100, now}, 100, true},
		{"one block finality gap", &chainLockHeightHealth{101, 100, now}, 100, true},
		{"two block finality gap", &chainLockHeightHealth{102, 100, now}, 100, false},
		{"just before expiry", &chainLockHeightHealth{100, 100, now.Add(-150*time.Second + time.Nanosecond)}, 100, true},
		{"at expiry", &chainLockHeightHealth{100, 100, now.Add(-150 * time.Second)}, 100, false},
		{"after expiry", &chainLockHeightHealth{100, 100, now.Add(-time.Hour)}, 100, false},
		{"locks recovered but scanner behind", &chainLockHeightHealth{110, 110, now}, 100, false},
		{"one block scanner gap", &chainLockHeightHealth{110, 110, now}, 109, true},
		{"two block scanner gap", &chainLockHeightHealth{110, 110, now}, 108, false},
		{"combined gap exceeds limit", &chainLockHeightHealth{110, 109, now}, 108, false},
		{"daemon behind ChainLock", &chainLockHeightHealth{980, 1000, now}, 980, false},
		{"daemon one block behind ChainLock", &chainLockHeightHealth{999, 1000, now}, 999, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.health.isHealthy(now, tc.scanned); got != tc.want {
				t.Fatalf("health = %v, want %v", got, tc.want)
			}
		})
	}
}

type dashHealthBridge struct{ thorclient.ThorchainBridge }

func (dashHealthBridge) GetBlockHeight() (int64, error) { return 1, nil }
func (dashHealthBridge) GetMimir(string) (int64, error) { return 0, nil }
func (dashHealthBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 1, 1, nil
}

// Drive the real scanner to a controlled height, without unrelated transaction,
// fee or solvency RPCs. Its healthy flag stays true while this height is frozen.
type dashHealthFetcher struct{ height atomic.Int64 }

func (f *dashHealthFetcher) GetHeight() (int64, error)     { return f.height.Load(), nil }
func (*dashHealthFetcher) GetNetworkFee() (uint64, uint64) { return 1, 1 }
func (*dashHealthFetcher) FetchTxs(int64, int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (*dashHealthFetcher) FetchMemPool(int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *DashSuite) TestDashScannerHeightHealth(c *C) {
	const hash = "0000000000000000000000000000000000000000000000000000000000000123"
	type rpcState struct {
		block, locked int64
		failMethod    string
		unknownBlock  bool
		wrongHash     bool
	}
	var state atomic.Pointer[rpcState]
	state.Store(&rpcState{block: 100, locked: 100})
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		current := state.Load()
		if method == current.failMethod {
			return []string{"invalid RPC result"}
		}
		switch method {
		case "getbestchainlock":
			return map[string]any{"blockhash": hash, "height": current.locked, "known_block": !current.unknownBlock}
		case "getblockhash":
			if current.wrongHash {
				return strings.Repeat("b", 64)
			}
			return hash
		case "getblockcount":
			return current.block
		default:
			c.Errorf("unexpected RPC %s", method)
			return nil
		}
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer storage.Close()
	fetcher := &dashHealthFetcher{}
	fetcher.height.Store(100)
	scanner, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID:                    common.DASHChain,
		StartBlockHeight:           99,
		BlockHeightDiscoverBackoff: time.Millisecond,
		MaxHealthyLag:              30 * time.Minute,
	}, storage, GetMetricForTest(c, common.DASHChain), dashHealthBridge{}, fetcher)
	c.Assert(err, IsNil)
	client.blockScanner = scanner

	// Even a fresh finality snapshot must not override an unhealthy scanner.
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	client.chainLockHeightHealth.Store(nil)
	scanner.Start(make(chan types.TxIn), make(chan common.NetworkFee))
	defer scanner.Stop()
	waitForScan := func(height int64) {
		deadline := time.Now().Add(5 * time.Second)
		for scanner.PreviousHeight() < height && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		c.Assert(scanner.PreviousHeight(), Equals, height)
		c.Assert(scanner.IsHealthy(), Equals, true)
	}
	waitForScan(100)
	c.Assert(client.IsBlockScannerHealthy(), Equals, false) // no snapshot yet
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(100))
	c.Assert(client.IsBlockScannerHealthy(), Equals, true)

	// Successful polling of a frozen ChainLock must not hide a growing gap.
	for _, block := range []int64{101, 102, 120} {
		state.Store(&rpcState{block: block, locked: 100})
		height, err = client.GetHeight()
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(100)) // scan limit remains the ChainLock
		c.Assert(scanner.IsHealthy(), Equals, true)
		c.Assert(client.IsBlockScannerHealthy(), Equals, block <= 101)
	}

	// A recovering ChainLock alone must not re-enable signing before scanning.
	state.Store(&rpcState{block: 120, locked: 120})
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	fetcher.height.Store(120)
	waitForScan(120)
	c.Assert(client.IsBlockScannerHealthy(), Equals, true)

	// Every read/validation failure must preserve the old timestamp, allowing
	// health to expire even when the shared scanner's healthy flag stays true.
	for _, failure := range []rpcState{
		{block: 120, locked: 120, failMethod: "getbestchainlock"},
		{block: 120, locked: 120, failMethod: "getblockhash"},
		{block: 120, locked: 120, failMethod: "getblockcount"},
		{block: 120, locked: 120, unknownBlock: true},
		{block: 120, locked: 120, wrongHash: true},
		{block: 120, locked: -1},
	} {
		stale := &chainLockHeightHealth{120, 120, time.Now().Add(-time.Hour)}
		client.chainLockHeightHealth.Store(stale)
		state.Store(&failure)
		_, err = client.GetHeight()
		c.Assert(err, NotNil)
		c.Assert(client.chainLockHeightHealth.Load(), Equals, stale)
		c.Assert(scanner.IsHealthy(), Equals, true)
		c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	}
	state.Store(&rpcState{block: 120, locked: 120})
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(client.IsBlockScannerHealthy(), Equals, true)

	// A daemon behind its ChainLock still supplies the scanner target, but
	// publishing that catch-up state must disable signing immediately.
	state.Store(&rpcState{block: 119, locked: 120})
	height, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(120))
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)

	// With no completed reads (including a hung RPC), the snapshot expires.
	client.chainLockHeightHealth.Store(&chainLockHeightHealth{120, 120, time.Now().Add(-time.Hour)})
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)

	// Other UTXO chains still use the shared scanner flag and ordinary height.
	client.chainLockHeightHealth.Store(nil)
	for _, chain := range []common.Chain{common.BTCChain, common.BCHChain, common.LTCChain, common.DOGEChain, common.ZECChain} {
		client.cfg.ChainID = chain
		state.Store(&rpcState{block: 125, locked: 120, failMethod: "getbestchainlock"})
		height, err = client.GetHeight()
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(125))
		c.Assert(client.chainLockHeightHealth.Load(), IsNil)
		c.Assert(client.IsBlockScannerHealthy(), Equals, true)
	}
}
