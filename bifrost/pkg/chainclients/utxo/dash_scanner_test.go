package utxo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type dashCatchUpState struct {
	local, locked        int64
	unknown, wrongHash   bool
	failMethod, badBlock string
}

type dashCatchUpBridge struct {
	dashHealthBridge
	asgardReads atomic.Int64
}

func (*dashCatchUpBridge) WaitToCatchUp() error                                { return nil }
func (*dashCatchUpBridge) GetLastObservedInHeight(common.Chain) (int64, error) { return 0, nil }
func (*dashCatchUpBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return ttypes.NodeStatus_Standby, nil
}

func (b *dashCatchUpBridge) GetAsgards() (ttypes.Vaults, error) {
	b.asgardReads.Add(1)
	return nil, nil
}

type dashCatchUpFixture struct {
	state        atomic.Pointer[dashCatchUpState]
	missing981   atomic.Int64
	maxRequested atomic.Int64
	heightReads  atomic.Int64
	bridge       dashCatchUpBridge
}

func dashCatchUpHash(height int64) string { return fmt.Sprintf("%064x", height) }

// Construct the production client to exercise scanner wiring, including
// NewBlockScanner's last-resort startup height lookup.
func newDashCatchUpClient(c *C, start, prefetch int64) (*Client, *dashCatchUpFixture, func()) {
	f := &dashCatchUpFixture{}
	f.state.Store(&dashCatchUpState{local: 980, locked: 1000})
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		state := f.state.Load()
		if method == state.failMethod {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "test RPC failure"}
		}
		switch method {
		case "createwallet":
			return map[string]string{"name": ""}
		case "importaddress":
			return nil
		case "getnetworkinfo":
			return map[string]any{"relayfee": 0.00001}
		case "getbestchainlock":
			f.heightReads.Add(1)
			return map[string]any{"height": state.locked, "blockhash": dashCatchUpHash(state.locked), "known_block": !state.unknown}
		case "getblockcount":
			return state.local
		case "getblockhash":
			var height int64
			c.Assert(json.Unmarshal(params[0], &height), IsNil)
			for previous := f.maxRequested.Load(); height > previous; previous = f.maxRequested.Load() {
				if f.maxRequested.CompareAndSwap(previous, height) {
					break
				}
			}
			if height > state.local {
				if height == 981 {
					f.missing981.Add(1)
				}
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidParameter, Message: "Block height out of range"}
			}
			if state.wrongHash {
				return dashCatchUpHash(height + 1)
			}
			return dashCatchUpHash(height)
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			height, err := strconv.ParseInt(hash, 16, 64)
			c.Assert(err, IsNil)
			block := map[string]any{"hash": hash, "height": height, "previousblockhash": dashCatchUpHash(height - 1), "chainlock": height <= state.locked, "tx": []any{}}
			switch state.badBlock {
			case "unlocked":
				block["chainlock"] = false
			case "height":
				block["height"] = height + 1
			case "hash":
				block["hash"] = dashCatchUpHash(height + 1)
			}
			return block
		case "getblockstats":
			return map[string]any{"avgfeerate": 0}
		default:
			c.Errorf("unexpected RPC %s", method)
			return nil
		}
	})
	ttypes.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(bob, keyring.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID: common.DASHChain, RPCHost: server.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID: common.DASHChain, StartBlockHeight: start, PrefetchBlocks: prefetch,
			BlockHeightDiscoverBackoff: time.Millisecond, HTTPRequestTimeout: time.Second,
			MaxHealthyLag: 30 * time.Minute,
			// Make every test block eligible for the solvency step; health must
			// still suppress it while the daemon is behind its ChainLock.
			ObservationFlexibilityBlocks: 1000,
		},
	}
	cfg.UTXO.BlockCacheCount = 2000
	cfg.UTXO.EstimatedAverageTxSize = 2000
	client, err := NewClient(thorclient.NewKeysWithKeybase(kb, bob, password), cfg, nil, &f.bridge, GetMetricForTest(c, common.DASHChain))
	c.Assert(err, IsNil)
	client.globalNetworkFeeQueue = make(chan common.NetworkFee, 100)
	client.globalSolvencyQueue = make(chan types.Solvency, 100)
	return client, f, func() { client.Stop(); server.Close() }
}

func waitForDashCatchUp(c *C, condition func() bool) {
	deadline := time.Now().Add(5 * time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	c.Assert(condition(), Equals, true)
}

func (s *DashSuite) TestDashScannerCatchesUpThroughAvailableBlocks(c *C) {
	for _, prefetch := range []int64{0, 3} {
		func() {
			client, f, cleanup := newDashCatchUpClient(c, 970, prefetch)
			defer cleanup()
			client.blockScanner.Start(make(chan types.TxIn, 100), make(chan common.NetworkFee, 100))
			waitForDashCatchUp(c, func() bool { return f.missing981.Load() >= 3 })
			c.Assert(client.blockScanner.PreviousHeight(), Equals, int64(980))
			c.Assert(client.IsBlockScannerHealthy(), Equals, false)
			c.Assert(f.bridge.asgardReads.Load(), Equals, int64(0))
			target, err := client.GetHeight()
			c.Assert(err, IsNil)
			c.Assert(target, Equals, int64(1000))
			c.Assert(client.ReportSolvency(target), NotNil)

			f.state.Store(&dashCatchUpState{local: 999, locked: 1000})
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 999 })
			c.Assert(client.IsBlockScannerHealthy(), Equals, false)
			c.Assert(f.bridge.asgardReads.Load(), Equals, int64(0))
			for height := int64(971); height <= 999; height++ {
				var meta *sharedutxo.BlockMeta
				meta, err = client.temporalStorage.GetBlockMeta(height)
				c.Assert(err, IsNil)
				c.Assert(meta, NotNil) // Every intervening block was fetched; none skipped.
				c.Assert(meta.BlockHash, Equals, dashCatchUpHash(height))
			}

			// One ordinary block above the ChainLock must retain the existing
			// one-block signing tolerance and must never be fetched by the scanner.
			f.state.Store(&dashCatchUpState{local: 1001, locked: 1000})
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 1000 && client.IsBlockScannerHealthy() })
			c.Assert(f.maxRequested.Load(), Equals, int64(1000))
			height, err := client.GetHeight()
			c.Assert(err, IsNil)
			c.Assert(height, Equals, int64(1000))
		}()
	}
}

func (s *DashSuite) TestDashScannerHeightValidation(c *C) {
	client, f, cleanup := newDashCatchUpClient(c, 970, 0)
	defer cleanup()
	for _, unknown := range []bool{false, true} {
		f.state.Store(&dashCatchUpState{local: 980, locked: 1000, unknown: unknown})
		height, err := client.GetHeight()
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(1000))
		c.Assert(client.chainLockHeightHealth.Load().isHealthy(time.Now(), 980), Equals, false)
		c.Assert(f.maxRequested.Load(), Equals, int64(0)) // No lookup of the unavailable target.
		c.Assert(client.ReportSolvency(height), NotNil)
		c.Assert(f.bridge.asgardReads.Load(), Equals, int64(0))
	}
	for _, state := range []dashCatchUpState{
		{local: 1000, locked: 1000, unknown: true},
		{local: 1000, locked: 1000, wrongHash: true},
		{local: -1, locked: 1000},
		{local: 1000, locked: -1},
		{local: 1000, locked: 1000, failMethod: "getbestchainlock"},
		{local: 1000, locked: 1000, failMethod: "getblockcount"},
		{local: 1000, locked: 1000, failMethod: "getblockhash"},
	} {
		f.state.Store(&state)
		stale := &chainLockHeightHealth{1000, 1000, time.Now().Add(-time.Hour)}
		client.chainLockHeightHealth.Store(stale)
		_, err := client.GetHeight()
		c.Assert(err, NotNil)
		c.Assert(client.chainLockHeightHealth.Load(), Equals, stale)
	}
	for _, invalid := range []string{"unlocked", "height", "hash"} {
		f.state.Store(&dashCatchUpState{local: 980, locked: 1000, badBlock: invalid})
		_, err := client.FetchTxs(980, 1000)
		c.Assert(errors.Is(err, btypes.ErrUnavailableBlock), Equals, true)
		meta, err := client.temporalStorage.GetBlockMeta(980)
		c.Assert(err, IsNil)
		c.Assert(meta, IsNil)
	}
}

func (s *DashSuite) TestDashScannerStartsWithoutSavedPositionDuringCatchUp(c *C) {
	client, _, cleanup := newDashCatchUpClient(c, 0, 0)
	defer cleanup()
	c.Assert(client.blockScanner.PreviousHeight(), Equals, int64(1000))
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(1000))
	c.Assert(client.ReportSolvency(height), NotNil)
}

type dashHaltedBridge struct{ dashHealthBridge }

func (dashHaltedBridge) GetMimir(key string) (int64, error) {
	if key == "SolvencyHaltDASHChain" {
		return 1, nil
	}
	return 0, nil
}

func (s *DashSuite) TestDashCatchUpBlocksSolvencyRunner(c *C) {
	client, f, cleanup := newDashCatchUpClient(c, 970, 0)
	defer cleanup()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go runners.SolvencyCheckRunner(common.DASHChain, client, dashHaltedBridge{}, stop, &wg, time.Millisecond)
	defer func() { close(stop); wg.Wait() }()
	waitForDashCatchUp(c, func() bool { return f.heightReads.Load() >= 3 })
	c.Assert(f.bridge.asgardReads.Load(), Equals, int64(0))
	c.Assert(client.lastSolvencyCheckHeight.Load(), Equals, int64(0))

	// Solvency can resume after daemon catch-up even though scanning remains
	// stopped by the halt. Scanner health must not be a prerequisite here.
	f.state.Store(&dashCatchUpState{local: 1001, locked: 1000})
	waitForDashCatchUp(c, func() bool { return client.lastSolvencyCheckHeight.Load() == 1000 })
	c.Assert(f.bridge.asgardReads.Load(), Equals, int64(1))
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
}

// ReportSolvency must validate fresh RPC data even if GetHeight recently succeeded.
func (s *DashSuite) TestDashReportSolvencyValidatesLocalChainLock(c *C) {
	client, f, cleanup := newDashCatchUpClient(c, 970, 0)
	defer cleanup()
	f.state.Store(&dashCatchUpState{local: 1001, locked: 1000})
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(1000))

	for _, state := range []dashCatchUpState{
		{local: 980, locked: 1000},
		{local: 999, locked: 1000, unknown: true},
		{local: 1000, locked: 1000, unknown: true},
		{local: 1000, locked: 1000, wrongHash: true},
		{local: -1, locked: 1000},
		{local: 1000, locked: -1},
		{local: 1000, locked: 995}, // The requested report height is no longer finalized.
		{local: 1000, locked: 1000, failMethod: "getbestchainlock"},
		{local: 1000, locked: 1000, failMethod: "getblockcount"},
		{local: 1000, locked: 1000, failMethod: "getblockhash"},
	} {
		f.state.Store(&state)
		c.Assert(client.ReportSolvency(height), NotNil, Commentf("state: %+v", state))
		c.Assert(f.bridge.asgardReads.Load(), Equals, int64(0))
		c.Assert(client.lastSolvencyCheckHeight.Load(), Equals, int64(0))
		c.Assert(client.globalSolvencyQueue, HasLen, 0)
	}

	// A failed attempt must not suppress a retry at the same height.
	f.state.Store(&dashCatchUpState{local: 1000, locked: 1000})
	c.Assert(client.ReportSolvency(height), IsNil)
	c.Assert(f.bridge.asgardReads.Load(), Equals, int64(1))
	c.Assert(client.lastSolvencyCheckHeight.Load(), Equals, height)
}
