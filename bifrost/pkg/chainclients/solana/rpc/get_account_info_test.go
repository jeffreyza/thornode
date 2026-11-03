package rpc

import (
	"encoding/json"
	"errors"
	"net/http"

	. "gopkg.in/check.v1"
)

type GetNonceAccountTestSuite struct{}

var _ = Suite(&GetNonceAccountTestSuite{})

// respond writes a JSON-RPC result envelope with the given `value` for
// getAccountInfo. value is the raw JSON for result.value.
func nonceAcctHandler(value string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":100},"value":` + value + `}}`))
	}
}

// TestGetNonceAccountValid: a real initialized nonce account (data is an OBJECT).
func (s *GetNonceAccountTestSuite) TestValid(c *C) {
	value := `{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"initialized","info":{"authority":"9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde","blockhash":"9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"}}},"space":80}`
	server, rpc := newTestServer(nonceAcctHandler(value))
	defer server.Close()
	nonce, authority, err := rpc.GetNonceAccount("Nonce111", 0)
	c.Assert(err, IsNil)
	c.Assert(nonce, Equals, "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo")
	c.Assert(authority, Equals, "9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
}

// TestGetNonceAccountDustedArrayData: the CRITICAL durable-nonce regression. jsonParsed
// returns data as an ARRAY ["<base64>","base64"] for a plain/dusted account the
// runtime can't parse as a nonce. This must be a RECOVERABLE
// ErrNonceAccountNotProvisioned (fall through to recent-blockhash), NOT a
// fail-closed transport error — otherwise anyone can halt a vault's SOL outbounds
// by sending 1 lamport to its deterministic seed address.
func (s *GetNonceAccountTestSuite) TestDustedArrayData(c *C) {
	value := `{"owner":"11111111111111111111111111111111","data":["","base64"],"lamports":1}`
	server, rpc := newTestServer(nonceAcctHandler(value))
	defer server.Close()
	_, _, err := rpc.GetNonceAccount("Dusted111", 0)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, ErrNonceAccountNotProvisioned), Equals, true)
}

// TestGetNonceAccountMissing: value:null (never-created address) is recoverable.
func (s *GetNonceAccountTestSuite) TestMissing(c *C) {
	server, rpc := newTestServer(nonceAcctHandler("null"))
	defer server.Close()
	_, _, err := rpc.GetNonceAccount("Missing111", 0)
	c.Assert(errors.Is(err, ErrNonceAccountNotProvisioned), Equals, true)
}

// TestGetNonceAccountUninitialized: a nonce-program account not yet initialized is recoverable.
func (s *GetNonceAccountTestSuite) TestUninitialized(c *C) {
	value := `{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"uninitialized","info":{}}},"space":80}`
	server, rpc := newTestServer(nonceAcctHandler(value))
	defer server.Close()
	_, _, err := rpc.GetNonceAccount("Uninit111", 0)
	c.Assert(errors.Is(err, ErrNonceAccountNotProvisioned), Equals, true)
}

// TestGetNonceAccountRPCError: a genuine RPC error FAILS CLOSED (not the sentinel),
// so a provisioned vault never silently reverts to the expiring path on a blip.
func (s *GetNonceAccountTestSuite) TestRPCError(c *C) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
	}
	server, rpc := newTestServer(handler)
	defer server.Close()
	_, _, err := rpc.GetNonceAccount("Any111", 0)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, ErrNonceAccountNotProvisioned), Equals, false)
}

// sanity: the valid fixture is well-formed JSON (guards the test data itself).
func (s *GetNonceAccountTestSuite) TestFixtureWellFormed(c *C) {
	var v interface{}
	c.Assert(json.Unmarshal([]byte(`["","base64"]`), &v), IsNil)
}
