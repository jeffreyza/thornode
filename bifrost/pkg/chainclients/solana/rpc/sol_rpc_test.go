package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	. "gopkg.in/check.v1"
)

type SolRPCTestSuite struct{}

var _ = Suite(&SolRPCTestSuite{})

func newTestServer(handler http.HandlerFunc) (*httptest.Server, *SolRPC) {
	server := httptest.NewServer(handler)
	rpc := NewSolRPC(server.URL, 5*time.Second)
	return server, rpc
}

// --- NewSolRPC ---

func (s *SolRPCTestSuite) TestNewSolRPC(c *C) {
	rpc := NewSolRPC("http://localhost:8899", 10*time.Second)
	c.Assert(rpc, NotNil)
	c.Assert(rpc.host, Equals, "http://localhost:8899")
	c.Assert(rpc.timeout, Equals, 10*time.Second)
}

// --- post ---

func (s *SolRPCTestSuite) TestPostSuccess(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.Method, Equals, "POST")
		c.Assert(r.Header.Get("Content-Type"), Equals, "application/json")

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		c.Assert(err, IsNil)
		c.Assert(payload["jsonrpc"], Equals, "2.0")
		c.Assert(payload["method"], Equals, "testMethod")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": "ok"}`))
	})
	defer server.Close()

	var result map[string]interface{}
	err := rpc.post("testMethod", []interface{}{}, &result)
	c.Assert(err, IsNil)
	c.Assert(result["result"], Equals, "ok")
}

func (s *SolRPCTestSuite) TestPostNon200(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	var result map[string]interface{}
	err := rpc.post("testMethod", []interface{}{}, &result)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*non-200 response status: 500.*")
}

func (s *SolRPCTestSuite) TestPostBadURL(c *C) {
	rpc := NewSolRPC("http://127.0.0.1:1", 1*time.Second)
	var result map[string]interface{}
	err := rpc.post("testMethod", []interface{}{}, &result)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to perform request.*")
}

func (s *SolRPCTestSuite) TestPostBadJSON(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})
	defer server.Close()

	var result map[string]interface{}
	err := rpc.post("testMethod", []interface{}{}, &result)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to decode response.*")
}

func (s *SolRPCTestSuite) TestPostInvalidURL(c *C) {
	rpc := NewSolRPC("://bad-url", 1*time.Second)
	var result map[string]interface{}
	err := rpc.post("testMethod", []interface{}{}, &result)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to create request.*")
}

// --- GetLatestBlockhash ---

func (s *SolRPCTestSuite) TestGetLatestBlockhash(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"value":{"blockhash":"9xJcFjz4V5dP2GYfZAWr1kTFr7e5dV7Z"}}}`))
	})
	defer server.Close()

	hash, err := rpc.GetLatestBlockhash()
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "9xJcFjz4V5dP2GYfZAWr1kTFr7e5dV7Z")
}

func (s *SolRPCTestSuite) TestGetLatestBlockhashError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetLatestBlockhash()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getLatestBlockhash.*")
}

// --- GetBalance ---

func (s *SolRPCTestSuite) TestGetBalance(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		params, ok := payload["params"].([]interface{})
		c.Assert(ok, Equals, true)
		c.Assert(params[0], Equals, "someAddress")
		opts, ok := params[1].(map[string]interface{})
		c.Assert(ok, Equals, true)
		c.Assert(opts["commitment"], Equals, "confirmed")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"value":1000000}}`))
	})
	defer server.Close()

	bal, err := rpc.GetBalance("someAddress", "confirmed", 0)
	c.Assert(err, IsNil)
	c.Assert(bal.Int64(), Equals, int64(1000000))
}

func (s *SolRPCTestSuite) TestGetBalanceWithMinSlot(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		params, ok := payload["params"].([]interface{})
		c.Assert(ok, Equals, true)
		opts, ok := params[1].(map[string]interface{})
		c.Assert(ok, Equals, true)
		c.Assert(opts["minContextSlot"], NotNil)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"value":5000}}`))
	})
	defer server.Close()

	bal, err := rpc.GetBalance("addr", "finalized", 100)
	c.Assert(err, IsNil)
	c.Assert(bal.Int64(), Equals, int64(5000))
}

func (s *SolRPCTestSuite) TestGetBalanceError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetBalance("addr", "confirmed", 0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getBalance.*")
}

func (s *SolRPCTestSuite) TestGetBalanceBadValue(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// 1.5e2 is valid JSON number but SetString with base 10 fails for non-integer
		_, _ = w.Write([]byte(`{"result":{"value":1.5e2}}`))
	})
	defer server.Close()

	_, err := rpc.GetBalance("addr", "confirmed", 0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to convert balance to big.Int.*")
}

// --- BroadcastTx ---

func (s *SolRPCTestSuite) TestBroadcastTx(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"txhash123"}`))
	})
	defer server.Close()

	hash, err := rpc.BroadcastTx("base64EncodedTx")
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "txhash123")
}

func (s *SolRPCTestSuite) TestBroadcastTxRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"code":-32002,"message":"Transaction simulation failed"}}`))
	})
	defer server.Close()

	_, err := rpc.BroadcastTx("badTx")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*sendTransaction error.*Transaction simulation failed.*")
}

func (s *SolRPCTestSuite) TestBroadcastTxPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.BroadcastTx("tx")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to sendTransaction.*")
}

// --- GetBlock ---

func (s *SolRPCTestSuite) TestGetBlock(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"blockhash":"abc123","previousBlockhash":"def456","parentSlot":100,"transactions":[]}}`))
	})
	defer server.Close()

	block, err := rpc.GetBlock(101)
	c.Assert(err, IsNil)
	c.Assert(block, NotNil)
	c.Assert(block.Blockhash, Equals, "abc123")
	c.Assert(block.PreviousBlockhash, Equals, "def456")
	c.Assert(block.ParentSlot, Equals, uint64(100))
}

func (s *SolRPCTestSuite) TestGetBlockSkippedSlot(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		msg := fmt.Sprintf("Slot %d was skipped, or missing due to ledger jump to recent snapshot", 42)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"error":{"code":-32009,"message":"%s"}}`, msg)))
	})
	defer server.Close()

	block, err := rpc.GetBlock(42)
	c.Assert(err, IsNil)
	c.Assert(block, IsNil)
}

func (s *SolRPCTestSuite) TestGetBlockRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Block not available"}}`))
	})
	defer server.Close()

	_, err := rpc.GetBlock(999)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*RPC Error: Block not available.*")
}

func (s *SolRPCTestSuite) TestGetBlockPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetBlock(1)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getBlock.*")
}

// --- GetBlockhashAtSlot ---

// The sign path needs ONLY the blockhash of a finalized slot. The request must
// opt out of the transaction list (megabytes on mainnet) and rewards, or the
// lookup burns the ~60s blockhash-liveness budget downloading bytes it discards.
func (s *SolRPCTestSuite) TestGetBlockhashAtSlot(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		c.Assert(json.NewDecoder(r.Body).Decode(&payload), IsNil)
		c.Assert(payload.Method, Equals, "getBlock")
		c.Assert(payload.Params, HasLen, 2)
		c.Assert(payload.Params[0], Equals, float64(101))
		cfg, ok := payload.Params[1].(map[string]interface{})
		c.Assert(ok, Equals, true)
		c.Assert(cfg["transactionDetails"], Equals, "none")
		c.Assert(cfg["rewards"], Equals, false)
		c.Assert(cfg["commitment"], Equals, "finalized")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"blockhash":"abc123","previousBlockhash":"def456","parentSlot":100,"blockHeight":90}}`))
	})
	defer server.Close()

	hash, err := rpc.GetBlockhashAtSlot(101)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "abc123")
}

func (s *SolRPCTestSuite) TestGetBlockhashAtSlotRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32009,"message":"Slot 42 was skipped, or missing due to ledger jump to recent snapshot"}}`))
	})
	defer server.Close()

	// Unlike the scanner's GetBlock, a skipped slot here is NOT silently empty:
	// the caller only ever asks for slots from a finalized getBlocks listing, so
	// any error is unexpected and must surface (retry), never become "".
	_, err := rpc.GetBlockhashAtSlot(42)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*RPC Error -32009: Slot 42 was skipped.*")
}

func (s *SolRPCTestSuite) TestGetBlockhashAtSlotPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetBlockhashAtSlot(1)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getBlock.*")
}

// --- GetBlockHeights ---

func (s *SolRPCTestSuite) TestGetBlockHeights(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[100,101,102,105]}`))
	})
	defer server.Close()

	heights, err := rpc.GetBlockHeights(100, 110)
	c.Assert(err, IsNil)
	c.Assert(heights, DeepEquals, []uint64{100, 101, 102, 105})
}

func (s *SolRPCTestSuite) TestGetBlockHeightsRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"code":-32000,"message":"Slot range too large"}}`))
	})
	defer server.Close()

	_, err := rpc.GetBlockHeights(0, 999999999)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*RPC Error.*Slot range too large.*")
}

func (s *SolRPCTestSuite) TestGetBlockHeightsPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetBlockHeights(0, 10)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getBlocks.*")
}

// --- GetSlot ---

func (s *SolRPCTestSuite) TestGetSlot(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":12345}`))
	})
	defer server.Close()

	slot, err := rpc.GetSlot()
	c.Assert(err, IsNil)
	c.Assert(slot, Equals, uint64(12345))
}

func (s *SolRPCTestSuite) TestGetSlotRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Node is behind"}}`))
	})
	defer server.Close()

	_, err := rpc.GetSlot()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*RPC Error.*Node is behind.*")
}

func (s *SolRPCTestSuite) TestGetSlotNilResult(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	})
	defer server.Close()

	_, err := rpc.GetSlot()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*result is nil.*")
}

func (s *SolRPCTestSuite) TestGetSlotPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetSlot()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getSlot.*")
}

// --- GetSignaturesForAddressUntil ---

func (s *SolRPCTestSuite) TestGetSignaturesForAddressUntil(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":[{"signature":"sig1","slot":100},{"signature":"sig2","slot":101}]}`))
	})
	defer server.Close()

	results, err := rpc.GetSignaturesForAddressUntil("addr", GetSignaturesForAddressUntilParams{
		Commitment: "finalized",
		Limit:      10,
	})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(results[0].Signature, Equals, "sig1")
	c.Assert(results[1].Slot, Equals, uint64(101))
}

func (s *SolRPCTestSuite) TestGetSignaturesForAddressUntilRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"code":-32600,"message":"Invalid request"}}`))
	})
	defer server.Close()

	_, err := rpc.GetSignaturesForAddressUntil("addr", GetSignaturesForAddressUntilParams{})
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*RPC Error.*Invalid request.*")
}

func (s *SolRPCTestSuite) TestGetSignaturesForAddressUntilPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetSignaturesForAddressUntil("addr", GetSignaturesForAddressUntilParams{})
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getSignaturesForAddress.*")
}

// --- GetTransaction ---

func (s *SolRPCTestSuite) TestGetTransaction(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"slot":200,"meta":{"fee":5000},"transaction":{"message":{"accountKeys":["key1"],"instructions":[],"recentBlockhash":"hash1"},"signatures":["sig1"]}}}`))
	})
	defer server.Close()

	tx, err := rpc.GetTransaction("sig1")
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)
	c.Assert(tx.Slot, Equals, uint64(200))
	c.Assert(tx.Meta.Fee, Equals, uint64(5000))
	c.Assert(tx.Transaction.Signatures[0], Equals, "sig1")
}

func (s *SolRPCTestSuite) TestGetTransactionRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`))
	})
	defer server.Close()

	_, err := rpc.GetTransaction("badSig")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*RPC Error.*Invalid params.*")
}

func (s *SolRPCTestSuite) TestGetTransactionPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetTransaction("sig")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getTransaction.*")
}

// --- GetAccountAtIndex ---

func (s *SolRPCTestSuite) TestGetAccountAtIndex(c *C) {
	txn := RPCTxnData{
		Message: RPCMessage{
			AccountKeys: []string{"key0", "key1", "key2"},
		},
	}
	c.Assert(txn.GetAccountAtIndex(0), Equals, "key0")
	c.Assert(txn.GetAccountAtIndex(1), Equals, "key1")
	c.Assert(txn.GetAccountAtIndex(2), Equals, "key2")
}

func (s *SolRPCTestSuite) TestGetAccountAtIndexOutOfBounds(c *C) {
	txn := RPCTxnData{
		Message: RPCMessage{
			AccountKeys: []string{"key0"},
		},
	}
	c.Assert(txn.GetAccountAtIndex(5), Equals, "")
}

func (s *SolRPCTestSuite) TestGetAccountAtIndexEmptyKeys(c *C) {
	txn := RPCTxnData{
		Message: RPCMessage{
			AccountKeys: []string{},
		},
	}
	c.Assert(txn.GetAccountAtIndex(0), Equals, "")
}

// --- GetInstructions ---

func (s *SolRPCTestSuite) TestGetInstructions(c *C) {
	txn := RPCTxnData{
		Message: RPCMessage{
			Instructions: []RPCInstruction{
				{Accounts: []int{0, 1}, Data: "data1", ProgramIdIndex: 2},
				{Accounts: []int{3}, Data: "data2", ProgramIdIndex: 4},
			},
		},
	}
	instrs := txn.GetInstructions()
	c.Assert(instrs, HasLen, 2)
	c.Assert(instrs[0].Data, Equals, "data1")
	c.Assert(instrs[1].ProgramIdIndex, Equals, 4)
}

// --- GetBlockTime ---

func (s *SolRPCTestSuite) TestGetBlockTime(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":1788116000}`))
	})
	defer server.Close()

	ts, err := rpc.GetBlockTime(950)
	c.Assert(err, IsNil)
	c.Assert(ts, NotNil)
	c.Assert(*ts, Equals, int64(1788116000))
}

// TestGetBlockTimeNull is the load-bearing case: a JSON null result MUST decode to
// a nil *int64 (not 0 / 1970). A bare int64 would silently read null as 0, which
// always compares <= any target time and would diverge one node from another.
func (s *SolRPCTestSuite) TestGetBlockTimeNull(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	})
	defer server.Close()

	ts, err := rpc.GetBlockTime(950)
	c.Assert(err, IsNil)
	c.Assert(ts, IsNil)
}

func (s *SolRPCTestSuite) TestGetBlockTimeRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"Block not available for slot"}}`))
	})
	defer server.Close()

	ts, err := rpc.GetBlockTime(950)
	c.Assert(err, NotNil)
	c.Assert(ts, IsNil)
}

func (s *SolRPCTestSuite) TestGetBlockTimePostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	ts, err := rpc.GetBlockTime(950)
	c.Assert(err, NotNil)
	c.Assert(ts, IsNil)
}

// --- IsBlockhashValid ---

func (s *SolRPCTestSuite) TestIsBlockhashValidTrue(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":true}}`))
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 0)
	c.Assert(err, IsNil)
	c.Assert(valid, Equals, true)
}

func (s *SolRPCTestSuite) TestIsBlockhashValidFalse(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":false}}`))
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 0)
	c.Assert(err, IsNil)
	c.Assert(valid, Equals, false)
}

// TestIsBlockhashValidNilResult drives the fail-closed path: a nil result must be an
// ERROR (not a silent false), because SignTx abstains on error rather than guessing.
func (s *SolRPCTestSuite) TestIsBlockhashValidNilResult(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 0)
	c.Assert(err, NotNil)
	c.Assert(valid, Equals, false)
}

func (s *SolRPCTestSuite) TestIsBlockhashValidRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`))
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 0)
	c.Assert(err, NotNil)
	c.Assert(valid, Equals, false)
}

func (s *SolRPCTestSuite) TestIsBlockhashValidPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 0)
	c.Assert(err, NotNil)
	c.Assert(valid, Equals, false)
}

// TestIsBlockhashValidMinContextSlotSent asserts the minContextSlot config is passed
// through when non-zero (so a lagging backend can reject the request).
func (s *SolRPCTestSuite) TestIsBlockhashValidMinContextSlotSent(c *C) {
	var gotMinContext interface{}
	var hadKey bool
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if len(payload.Params) > 1 {
			var cfg map[string]interface{}
			_ = json.Unmarshal(payload.Params[1], &cfg)
			gotMinContext, hadKey = cfg["minContextSlot"]
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":950},"value":true}}`))
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 950)
	c.Assert(err, IsNil)
	c.Assert(valid, Equals, true)
	c.Assert(hadKey, Equals, true)
	c.Assert(gotMinContext, Equals, float64(950)) // JSON numbers decode to float64
}

// TestIsBlockhashValidLaggingBackend covers the defensive guard: a backend that
// answers from a bank BELOW the requested minContextSlot is treated as lagging ->
// error, so SignTx retains rather than trusting a possibly-stale false.
func (s *SolRPCTestSuite) TestIsBlockhashValidLaggingBackend(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		// context.slot 800 is below the requested min of 950
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":800},"value":false}}`))
	})
	defer server.Close()

	valid, err := rpc.IsBlockhashValid("9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", "processed", 950)
	c.Assert(err, NotNil) // lagging backend -> error -> caller retains
	c.Assert(valid, Equals, false)
}

// --- GetFirstAvailableBlock ---

// The consensus-blockhash search treats this floor as PROOF that widening its lookback
// is pointless, so the call must take no params and surface the slot verbatim.
func (s *SolRPCTestSuite) TestGetFirstAvailableBlock(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		c.Assert(json.NewDecoder(r.Body).Decode(&payload), IsNil)
		c.Assert(payload.Method, Equals, "getFirstAvailableBlock")
		c.Assert(payload.Params, HasLen, 0)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":250000}`))
	})
	defer server.Close()

	slot, err := rpc.GetFirstAvailableBlock()
	c.Assert(err, IsNil)
	c.Assert(slot, Equals, uint64(250000))
}

// A provider that does not serve the method must ERROR rather than report slot 0: the
// caller reads 0 as "retains everything" and would stop galloping at the first window.
func (s *SolRPCTestSuite) TestGetFirstAvailableBlockRPCError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
	})
	defer server.Close()

	_, err := rpc.GetFirstAvailableBlock()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*Method not found.*")
}

func (s *SolRPCTestSuite) TestGetFirstAvailableBlockPostError(c *C) {
	server, rpc := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	_, err := rpc.GetFirstAvailableBlock()
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*failed to call getFirstAvailableBlock.*")
}
