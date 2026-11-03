package thorchain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

// tendermintBlockClient is the part of the CometBFT RPC client that queryBlock
// uses. Both the HTTP client and the in-process local client implement it.
type tendermintBlockClient interface {
	Block(ctx context.Context, height *int64) (*ctypes.ResultBlock, error)
	BlockResults(ctx context.Context, height *int64) (*ctypes.ResultBlockResults, error)
}

// localTendermintClient, when set, serves queryBlock from the in-process
// CometBFT node instead of a JSON-RPC round trip over HTTP to the node's own
// RPC server.
var localTendermintClient atomic.Pointer[localBlockClient]

// SetLocalTendermintClient makes queryBlock read blocks and block results
// directly from the in-process CometBFT node through c (the SDK's
// rpc/client/local client). Without it queryBlock falls back to the HTTP
// client on the configured RPC listen address.
func SetLocalTendermintClient(c tendermintBlockClient) {
	if c == nil {
		localTendermintClient.Store(nil)
		return
	}
	localTendermintClient.Store(&localBlockClient{c: c})
}

// getTendermintClient returns the in-process client when one was set, else the
// HTTP client.
func getTendermintClient() (tendermintBlockClient, error) {
	if c := localTendermintClient.Load(); c != nil {
		return c, nil
	}
	initTendermintOnce.Do(initTendermint)
	if tendermintClientErr != nil {
		return nil, tendermintClientErr
	}
	return tendermintClient, nil
}

// Response metadata the HTTP JSON-RPC client prefixes onto every error. The
// node's own RPC server answers handler errors with 200 OK and panics with 500
// over HTTP/1.1 (rpc/jsonrpc/server: makeJSONRPCHandler, RecoverAndLogHandler).
const (
	rpcErrPrefixOK    = "error in json rpc client, with http response metadata: (Status: 200 OK, Protocol HTTP/1.1)"
	rpcErrPrefixPanic = "error in json rpc client, with http response metadata: (Status: 500 Internal Server Error, Protocol HTTP/1.1)"
)

// localBlockClient wraps the in-process client so that queryBlock sees exactly
// what it saw through the HTTP client: errors carry the same text, a panic in
// the RPC handler becomes the same error instead of reaching the query, and
// strings read as they do after the JSON encode/decode round trip.
//
// The results are decoded fresh from the block and state stores on every call
// (BlockStore.LoadBlock, dbStore.LoadFinalizeBlockResponse), so queryBlock may
// modify them, as it does through the HTTP client.
type localBlockClient struct {
	c tendermintBlockClient
}

func (l *localBlockClient) Block(ctx context.Context, height *int64) (res *ctypes.ResultBlock, err error) {
	defer recoverRPCPanic(&err)
	res, err = l.c.Block(ctx, height)
	if err != nil {
		return nil, wrapRPCError(err)
	}
	if res.Block != nil {
		res.Block.ChainID = jsonRoundTripString(res.Block.ChainID)
	}
	return res, nil
}

func (l *localBlockClient) BlockResults(ctx context.Context, height *int64) (res *ctypes.ResultBlockResults, err error) {
	defer recoverRPCPanic(&err)
	res, err = l.c.BlockResults(ctx, height)
	if err != nil {
		return nil, wrapRPCError(err)
	}
	jsonRoundTripEvents(res.FinalizeBlockEvents)
	for _, txr := range res.TxsResults {
		if txr == nil {
			continue
		}
		txr.Log = jsonRoundTripString(txr.Log)
		txr.Info = jsonRoundTripString(txr.Info)
		txr.Codespace = jsonRoundTripString(txr.Codespace)
		jsonRoundTripEvents(txr.Events)
	}
	return res, nil
}

// wrapRPCError formats an RPC handler error as the HTTP client reports it: the
// server answers it as an internal error (-32603) and the client prefixes the
// HTTP response metadata.
func wrapRPCError(err error) error {
	return fmt.Errorf("%s. %w", rpcErrPrefixOK, &rpctypes.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data:    err.Error(),
	})
}

// recoverRPCPanic turns a panic in the RPC handler into the error the HTTP
// client reports for it, as the RPC server recovers it and answers 500.
func recoverRPCPanic(err *error) {
	e := recover()
	if e == nil {
		return
	}
	var perr error
	switch e := e.(type) {
	case error:
		perr = e
	case string:
		perr = errors.New(e)
	case fmt.Stringer:
		perr = errors.New(e.String())
	default:
		// the server fails to format the response and writes an empty body
		*err = fmt.Errorf("%s. error unmarshalling: unexpected end of JSON input", rpcErrPrefixPanic)
		return
	}
	*err = fmt.Errorf("%s. %w", rpcErrPrefixPanic, &rpctypes.RPCError{
		Code:    -32603,
		Message: "Internal error",
		Data:    perr.Error(),
	})
}

func jsonRoundTripEvents(events []abci.Event) {
	for i := range events {
		events[i].Type = jsonRoundTripString(events[i].Type)
		for j := range events[i].Attributes {
			events[i].Attributes[j].Key = jsonRoundTripString(events[i].Attributes[j].Key)
			events[i].Attributes[j].Value = jsonRoundTripString(events[i].Attributes[j].Value)
		}
	}
}

// jsonRoundTripString returns s as it reads after an encoding/json round trip:
// the encoder writes each byte that is not part of a valid UTF-8 sequence as
// U+FFFD, and leaves valid UTF-8 unchanged.
func jsonRoundTripString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}
