package thorchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	tmhttp "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpcserver "github.com/cometbft/cometbft/rpc/jsonrpc/server"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func testBlockAt(height int64, txs []cmttypes.Tx) *cmttypes.Block {
	lastID := cmttypes.BlockID{Hash: make([]byte, 32), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: make([]byte, 32)}}
	lastCommit := &cmttypes.Commit{Height: height - 1, BlockID: lastID, Signatures: []cmttypes.CommitSig{{
		BlockIDFlag:      cmttypes.BlockIDFlagCommit,
		ValidatorAddress: make([]byte, 20),
		Timestamp:        time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
		Signature:        make([]byte, 64),
	}}}
	b := cmttypes.MakeBlock(height, txs, lastCommit, nil)
	b.ChainID = "thorchain-\xffchain" // invalid UTF-8 is replaced by the JSON round trip
	b.Time = time.Date(2026, 10, 7, 12, 0, int(height), 123456789, time.UTC)
	b.AppHash = []byte{0xaa, byte(height)}
	b.ProposerAddress = make([]byte, 20)
	b.ValidatorsHash = make([]byte, 32)
	b.LastBlockID = lastID
	return b
}

func testEvent(typ string, kv ...string) abci.Event {
	e := abci.Event{Type: typ}
	for i := 0; i+1 < len(kv); i += 2 {
		e.Attributes = append(e.Attributes, abci.EventAttribute{Key: kv[i], Value: kv[i+1], Index: i%4 == 0})
	}
	return e
}

// compareQueryBlockClients runs queryBlock for each height through the node's
// JSON-RPC server over HTTP and through the in-process client, and checks the
// responses, or errors, are identical.
func compareQueryBlockClients(c *C, qs *queryServer, ctx cosmos.Context, httpClient *tmhttp.HTTP, local tendermintBlockClient, heights ...string) {
	initTendermintOnce.Do(func() {})
	prevClient, prevErr, prevLocal := tendermintClient, tendermintClientErr, localTendermintClient.Load()
	defer func() {
		tendermintClient, tendermintClientErr = prevClient, prevErr
		localTendermintClient.Store(prevLocal)
	}()
	tendermintClient, tendermintClientErr = httpClient, nil

	for _, height := range heights {
		req := &types.QueryBlockRequest{Height: height}

		localTendermintClient.Store(nil)
		httpRes, httpErr := qs.queryBlock(ctx, req)
		SetLocalTendermintClient(local)
		localRes, localErr := qs.queryBlock(ctx, req)

		comment := Commentf("height %q", height)
		if httpErr != nil {
			c.Assert(localErr, NotNil, comment)
			c.Check(localErr.Error(), Equals, httpErr.Error(), comment)
			c.Check(localRes, IsNil, comment)
			continue
		}
		c.Assert(localErr, IsNil, comment)
		httpBz, err := httpRes.Marshal()
		c.Assert(err, IsNil)
		localBz, err := localRes.Marshal()
		c.Assert(err, IsNil)
		c.Check(localBz, DeepEquals, httpBz, comment)
		httpJSON, err := json.Marshal(httpRes)
		c.Assert(err, IsNil)
		localJSON, err := json.Marshal(localRes)
		c.Assert(err, IsNil)
		c.Check(string(localJSON), Equals, string(httpJSON), comment)
	}
}

// queryBlockServer returns a query server whose codec decodes THORChain txs.
func (s *QuerierSuite) queryBlockServer(c *C) *queryServer {
	registry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(registry)
	types.RegisterInterfaces(registry)
	s.mgr.cdc = codec.NewProtoCodec(registry)
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	return &queryServer{mgr: s.mgr, txConfig: txConfig}
}

func (s *QuerierSuite) TestQueryBlockLocalClientMatchesHTTP(c *C) {
	qs := s.queryBlockServer(c)
	txConfig := qs.txConfig

	var txs []cmttypes.Tx
	for i := 0; i < 3; i++ {
		b := txConfig.NewTxBuilder()
		c.Assert(b.SetMsgs(types.NewMsgSend(GetRandomBech32Addr(), GetRandomBech32Addr(),
			cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(int64(i+1)))))), IsNil)
		b.SetMemo(fmt.Sprintf("memo %d", i))
		bz, err := txConfig.TxEncoder()(b.GetTx())
		c.Assert(err, IsNil)
		txs = append(txs, bz)
	}
	emptyData, err := s.mgr.cdc.MarshalInterface(&types.MsgEmpty{})
	c.Assert(err, IsNil)

	blocks := []*cmttypes.Block{testBlockAt(5, txs), testBlockAt(6, nil), testBlockAt(7, txs[:1])}
	results := map[int64]*abci.ResponseFinalizeBlock{
		5: {
			AppHash: []byte{1},
			Events: []abci.Event{
				testEvent("pool", "pool", "BTC.BTC", "mode", "BeginBlock"),
				testEvent("bad\xffutf8", "k\xfe", "v\xe2\x82", "mode", "EndBlock"),
				testEvent("coin_spent", "amount", "1rune"),
				testEvent("empty"),
			},
			TxResults: []*abci.ExecTxResult{
				{Code: 0, Data: emptyData, GasWanted: 10, GasUsed: 5, Events: []abci.Event{testEvent("message", "action", "send")}},
				{Code: 5, Data: []byte{1, 2, 3}, Log: "failed \xff\xfe memo <&>", Info: "info", Codespace: "sdk", GasWanted: -1, GasUsed: 1 << 40},
				{Code: 0, Events: []abci.Event{testEvent("t\xc0", "a", "b\xed\xa0\x80 "), testEvent("x")}},
			},
		},
		6: {AppHash: []byte{2}},
		// height 7 has a block but no stored results
	}
	env, err := newBlockRPCEnv(blocks, results)
	c.Assert(err, IsNil)
	httpClient, stop, err := serveBlockRPC(blockRoutes(env))
	c.Assert(err, IsNil)
	defer stop()

	ctx := s.ctx.WithBlockHeight(6)
	compareQueryBlockClients(c, qs, ctx, httpClient, envClient{env},
		"5", "6", "7", "", "abc", "4", "8", "0", "-3", "1")

	// the cases above cover what they claim to
	defer connectNodeRPC(env, httpClient)()
	res, err := qs.queryBlock(ctx, &types.QueryBlockRequest{Height: "5"})
	c.Assert(err, IsNil)
	c.Check(res.Header.ChainId, Equals, "thorchain-�chain")
	c.Check(res.Txs, HasLen, 3)
	c.Check(res.Txs[1].Result.Log, Equals, "failed �� memo <&>")
	c.Check(res.BeginBlockEvents, HasLen, 1)
	c.Check(res.EndBlockEvents, HasLen, 1)
	c.Check(res.FinalizeBlockEvents, HasLen, 2)
	_, err = qs.queryBlock(ctx, &types.QueryBlockRequest{Height: "8"})
	c.Check(err, ErrorMatches, `fail to get block from tendermint rpc: error in json rpc client, with http response metadata: \(Status: 200 OK, Protocol HTTP/1.1\)\. RPC error -32603 - Internal error: height 8 must be less than or equal to the current blockchain height 7`)
	_, err = qs.queryBlock(ctx, &types.QueryBlockRequest{Height: "7"})
	c.Check(err, ErrorMatches, `fail to get block results from tendermint rpc: .* RPC error -32603 - Internal error: could not find results for height #7`)
}

func (s *QuerierSuite) TestQueryBlockLocalClientMatchesHTTPMainnet(c *C) {
	qs := s.queryBlockServer(c)

	block, results := loadPerfMainnetBlock(c)
	env, err := newBlockRPCEnv([]*cmttypes.Block{block}, map[int64]*abci.ResponseFinalizeBlock{block.Height: results})
	c.Assert(err, IsNil)
	httpClient, stop, err := serveBlockRPC(blockRoutes(env))
	c.Assert(err, IsNil)
	defer stop()

	ctx := s.ctx.WithBlockHeight(block.Height)
	compareQueryBlockClients(c, qs, ctx, httpClient, envClient{env}, "", fmt.Sprint(block.Height+1))
}

type panicStringer struct{}

func (panicStringer) String() string { return "stringer panic" }

type panicClient struct {
	block   func(*rpctypes.Context, *int64) (*ctypes.ResultBlock, error)
	results func(*rpctypes.Context, *int64) (*ctypes.ResultBlockResults, error)
}

func (p panicClient) Block(_ context.Context, h *int64) (*ctypes.ResultBlock, error) {
	return p.block(&rpctypes.Context{}, h)
}

func (p panicClient) BlockResults(_ context.Context, h *int64) (*ctypes.ResultBlockResults, error) {
	return p.results(&rpctypes.Context{}, h)
}

func (s *QuerierSuite) TestLocalBlockClientPanicMatchesHTTP(c *C) {
	var panicValue any
	block := func(_ *rpctypes.Context, _ *int64) (*ctypes.ResultBlock, error) { panic(panicValue) }
	results := func(_ *rpctypes.Context, _ *int64) (*ctypes.ResultBlockResults, error) { panic(panicValue) }
	httpClient, stop, err := serveBlockRPC(map[string]*rpcserver.RPCFunc{
		"block":         rpcserver.NewRPCFunc(block, "height"),
		"block_results": rpcserver.NewRPCFunc(results, "height"),
	})
	c.Assert(err, IsNil)
	defer stop()
	local := &localBlockClient{c: panicClient{block: block, results: results}}

	ctx := context.Background()
	h := int64(3)
	for _, v := range []any{errors.New("error panic"), "string panic", panicStringer{}} {
		panicValue = v
		_, httpErr := httpClient.Block(ctx, &h)
		_, localErr := local.Block(ctx, &h)
		c.Assert(httpErr, NotNil)
		c.Assert(localErr, NotNil)
		c.Check(localErr.Error(), Equals, httpErr.Error())
		_, httpErr = httpClient.BlockResults(ctx, &h)
		_, localErr = local.BlockResults(ctx, &h)
		c.Assert(httpErr, NotNil)
		c.Assert(localErr, NotNil)
		c.Check(localErr.Error(), Equals, httpErr.Error())
	}
}
