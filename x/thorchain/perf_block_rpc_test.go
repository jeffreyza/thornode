package thorchain

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"os"

	dbm "github.com/cometbft/cometbft-db"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	tmhttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/rpc/core"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

// A real mainnet block (height 28143210: 33 txs - MsgObservedTxQuorum, MsgDeposit,
// MsgTssKeysignFail, MsgPriceFeedQuorumBatch - and 450 finalize-block events) as
// the node's /block and /block_results RPC returned it.
const perfMainnetBlockFile = "../../test/performance/testdata/mainnet-block-28143210.json.gz"

// loadPerfMainnetBlock returns the mainnet block and its FinalizeBlock response.
func loadPerfMainnetBlock(b interface{ Fatal(args ...any) }) (*cmttypes.Block, *abci.ResponseFinalizeBlock) {
	f, err := os.Open(perfMainnetBlockFile)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		b.Fatal(err)
	}
	var raw struct {
		Block        json.RawMessage `json:"block"`
		BlockResults json.RawMessage `json:"block_results"`
	}
	if err = json.NewDecoder(zr).Decode(&raw); err != nil {
		b.Fatal(err)
	}
	var rb ctypes.ResultBlock
	if err = cmtjson.Unmarshal(raw.Block, &rb); err != nil {
		b.Fatal(err)
	}
	var rr ctypes.ResultBlockResults
	if err = cmtjson.Unmarshal(raw.BlockResults, &rr); err != nil {
		b.Fatal(err)
	}
	return rb.Block, &abci.ResponseFinalizeBlock{
		Events:                rr.FinalizeBlockEvents,
		TxResults:             rr.TxsResults,
		ValidatorUpdates:      rr.ValidatorUpdates,
		ConsensusParamUpdates: rr.ConsensusParamUpdates,
		AppHash:               rr.AppHash,
	}
}

// newBlockRPCEnv stores the blocks and results in in-memory CometBFT block and
// state stores and returns the RPC environment over them.
func newBlockRPCEnv(blocks []*cmttypes.Block, results map[int64]*abci.ResponseFinalizeBlock) (*core.Environment, error) {
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{})
	for _, b := range blocks {
		parts, err := b.MakePartSet(cmttypes.BlockPartSizeBytes)
		if err != nil {
			return nil, err
		}
		seen := &cmttypes.Commit{
			Height:  b.Height,
			BlockID: cmttypes.BlockID{Hash: b.Hash(), PartSetHeader: parts.Header()},
			Signatures: []cmttypes.CommitSig{{
				BlockIDFlag:      cmttypes.BlockIDFlagCommit,
				ValidatorAddress: make([]byte, 20),
				Timestamp:        b.Time,
				Signature:        []byte("sig"),
			}},
		}
		blockStore.SaveBlock(b, parts, seen)
		if r, ok := results[b.Height]; ok {
			if err := stateStore.SaveFinalizeBlockResponse(b.Height, r); err != nil {
				return nil, err
			}
		}
	}
	return &core.Environment{
		BlockStore: blockStore,
		StateStore: stateStore,
		Logger:     cmtlog.NewNopLogger(),
	}, nil
}

func blockRoutes(env *core.Environment) map[string]*rpcserver.RPCFunc {
	routes := env.GetRoutes()
	return map[string]*rpcserver.RPCFunc{
		"block":         routes["block"],
		"block_results": routes["block_results"],
	}
}

// serveBlockRPC serves the routes over HTTP the way the node's RPC server
// does, and returns an HTTP client for it.
func serveBlockRPC(routes map[string]*rpcserver.RPCFunc) (*tmhttp.HTTP, func(), error) {
	listener, err := rpcserver.Listen("tcp://127.0.0.1:0", 0)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	rpcserver.RegisterRPCFuncs(mux, routes, cmtlog.NewNopLogger())
	go func() {
		_ = rpcserver.Serve(listener, mux, cmtlog.NewNopLogger(), rpcserver.DefaultConfig())
	}()
	client, err := tmhttp.New("tcp://"+listener.Addr().String(), "/websocket")
	if err != nil {
		_ = listener.Close()
		return nil, nil, err
	}
	return client, func() { _ = listener.Close() }, nil
}

// connectNodeRPC wires queryBlock to the CometBFT node the way a running
// THORNode does: the HTTP client to the node's own RPC server is the fallback,
// and app.RegisterTendermintService hands queryBlock the SDK's in-process
// client (rpc/client/local, which calls the RPC environment directly, as
// envClient does). It returns a function restoring the previous wiring.
func connectNodeRPC(env *core.Environment, httpClient *tmhttp.HTTP) func() {
	initTendermintOnce.Do(func() {})
	prevClient, prevErr, prevLocal := tendermintClient, tendermintClientErr, localTendermintClient.Load()
	tendermintClient, tendermintClientErr = httpClient, nil
	SetLocalTendermintClient(envClient{env})
	return func() {
		tendermintClient, tendermintClientErr = prevClient, prevErr
		localTendermintClient.Store(prevLocal)
	}
}

// envClient calls the RPC environment directly, as rpc/client/local does.
type envClient struct{ env *core.Environment }

func (e envClient) Block(_ context.Context, height *int64) (*ctypes.ResultBlock, error) {
	return e.env.Block(&rpctypes.Context{}, height)
}

func (e envClient) BlockResults(_ context.Context, height *int64) (*ctypes.ResultBlockResults, error) {
	return e.env.BlockResults(&rpctypes.Context{}, height)
}
