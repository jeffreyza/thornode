package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type RPCSuite struct {
	backoffs []time.Duration // the retry backoffs of the running test
}

var _ = Suite(&RPCSuite{})

// realRetrySleep is retrySleep as the package sets it, before any test.
var realRetrySleep = retrySleep

// Retries record their backoffs instead of sleeping through them.
func (s *RPCSuite) SetUpTest(c *C) {
	s.backoffs = nil
	retrySleep = func(d time.Duration) { s.backoffs = append(s.backoffs, d) }
}

func (s *RPCSuite) TearDownTest(c *C) {
	retrySleep = realRetrySleep
}

// ---------------------------------------------------------------------------
// Mock JSON-RPC server
// ---------------------------------------------------------------------------

// jsonRPCRequest is the structure of a JSON-RPC request
type jsonRPCRequest struct {
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
	ID     interface{}   `json:"id"`
}

// newMockRPCServer creates a mock HTTP server that responds to JSON-RPC requests.
// The handler function receives the method and params and returns a result or error.
func newMockRPCServer(handler func(method string, params []interface{}) (interface{}, *btcjson.RPCError)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		// Try to parse as a batch request (array of requests)
		var batchReqs []jsonRPCRequest
		if err := json.Unmarshal(body, &batchReqs); err == nil && len(batchReqs) > 0 {
			// batch request
			var responses []json.RawMessage
			for _, req := range batchReqs {
				result, rpcErr := handler(req.Method, req.Params)
				resp := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
				}
				if rpcErr != nil {
					resp["error"] = map[string]interface{}{
						"code":    rpcErr.Code,
						"message": rpcErr.Message,
					}
				} else {
					resp["result"] = result
				}
				raw, _ := json.Marshal(resp)
				responses = append(responses, raw)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(responses)
			return
		}

		// Single request
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		result, rpcErr := handler(req.Method, req.Params)
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
		}
		if rpcErr != nil {
			resp["error"] = map[string]interface{}{
				"code":    rpcErr.Code,
				"message": rpcErr.Message,
			}
		} else {
			resp["result"] = result
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// newErrorServer returns a server that always responds with the given HTTP status code.
func newErrorServer(statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":-1,"message":"server error"}}`, statusCode)
	}))
}

// ---------------------------------------------------------------------------
// Mock SerializableTx
// ---------------------------------------------------------------------------

type mockTx struct {
	data     []byte
	serError error
}

func (m *mockTx) SerializeSize() int { return len(m.data) }
func (m *mockTx) Serialize(w io.Writer) error {
	if m.serError != nil {
		return m.serError
	}
	_, err := w.Write(m.data)
	return err
}

// ---------------------------------------------------------------------------
// NewClient tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestNewClient(c *C) {
	log := zerolog.Nop()

	// BTC chain (default) with explicit http
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 3, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)
	c.Assert(cl.client, NotNil)
	c.Assert(cl.chain, Equals, common.BTCChain)
	c.Assert(cl.maxRetries, Equals, 3)
}

func (s *RPCSuite) TestNewClientNoScheme(c *C) {
	log := zerolog.Nop()

	// Without scheme, should default to http://
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	// Strip http:// from the URL to test auto-prefix
	host := srv.URL[len("http://"):]
	cl, err := NewClient(host, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)
}

func (s *RPCSuite) TestNewClientZEC(c *C) {
	log := zerolog.Nop()

	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 2, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)
	c.Assert(cl, NotNil)
	c.Assert(cl.client, NotNil)
	c.Assert(cl.chain, Equals, common.ZECChain)
}

func (s *RPCSuite) TestNewClientInvalidURL(c *C) {
	log := zerolog.Nop()

	// Invalid URL for default chain should fail
	cl, err := NewClient("://invalid", "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, NotNil)
	c.Assert(cl, IsNil)
}

// ---------------------------------------------------------------------------
// GetBlockCount tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockCount(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblockcount")
		return 800000, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	count, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(800000))
}

func (s *RPCSuite) TestGetBlockCountError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.ErrRPCClientNotConnected, "not connected")
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	_, err = cl.GetBlockCount()
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// GetBlockHash tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockHash(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblockhash")
		return "0000000000000000000abcdef1234567890abcdef1234567890abcdef1234567", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	hash, err := cl.GetBlockHash(800000)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "0000000000000000000abcdef1234567890abcdef1234567890abcdef1234567")
}

// ---------------------------------------------------------------------------
// GetBlockVerboseTxs tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockVerboseTxs(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblock")
		return map[string]interface{}{
			"hash":   "abc123",
			"height": 100,
			"tx":     []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	block, err := cl.GetBlockVerboseTxs("abc123")
	c.Assert(err, IsNil)
	c.Assert(block, NotNil)
	c.Assert(block.Hash, Equals, "abc123")
}

// ---------------------------------------------------------------------------
// GetBlockVerbose tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockVerbose(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblock")
		return map[string]interface{}{
			"hash":   "abc123",
			"height": 100,
			"tx":     []interface{}{"txid1"},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	block, err := cl.GetBlockVerbose("abc123")
	c.Assert(err, IsNil)
	c.Assert(block, NotNil)
	c.Assert(block.Hash, Equals, "abc123")
}

// ---------------------------------------------------------------------------
// GetBlockStats tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetBlockStats(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getblockstats")
		return map[string]interface{}{
			"avgfee": 1000,
			"height": 100,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	stats, err := cl.GetBlockStats("abc123")
	c.Assert(err, IsNil)
	c.Assert(stats, NotNil)
}

// ---------------------------------------------------------------------------
// GetMempoolEntry tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetMempoolEntry(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getmempoolentry")
		return map[string]interface{}{
			"vsize": 200,
			"fees": map[string]interface{}{
				"base": 0.0001,
			},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	entry, err := cl.GetMempoolEntry("txid123")
	c.Assert(err, IsNil)
	c.Assert(entry, NotNil)
}

// ---------------------------------------------------------------------------
// BatchGetMempoolEntry tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetMempoolEntry(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getmempoolentry")
		return map[string]interface{}{
			"vsize": 200,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetMempoolEntry([]string{"txid1", "txid2"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(errs, HasLen, 2)
}

func (s *RPCSuite) TestBatchGetMempoolEntryZEC(c *C) {
	log := zerolog.Nop()

	// ZEC uses sequential calls through zec.Client.BatchCall
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return map[string]interface{}{
			"vsize": 150,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetMempoolEntry([]string{"txid1"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 1)
	c.Assert(errs, HasLen, 1)
}

// ---------------------------------------------------------------------------
// GetRawMempool tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetRawMempool(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawmempool")
		return []string{"txid1", "txid2", "txid3"}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	txids, err := cl.GetRawMempool()
	c.Assert(err, IsNil)
	c.Assert(txids, HasLen, 3)
}

// ---------------------------------------------------------------------------
// GetRawTransactionVerbose tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetRawTransactionVerbose(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawtransaction")
		return map[string]interface{}{
			"txid": "abc123",
			"vout": []interface{}{},
			"vin":  []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	tx, err := cl.GetRawTransactionVerbose("abc123")
	c.Assert(err, IsNil)
	c.Assert(tx, NotNil)
	c.Assert(tx.Txid, Equals, "abc123")
}

// ---------------------------------------------------------------------------
// GetRawTransaction tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetRawTransaction(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawtransaction")
		return "0100000001abcdef", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	rawTx, err := cl.GetRawTransaction("abc123")
	c.Assert(err, IsNil)
	c.Assert(rawTx, Equals, "0100000001abcdef")
}

// ---------------------------------------------------------------------------
// BatchGetRawTransactionVerbose tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetRawTransactionVerbose(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getrawtransaction")
		return map[string]interface{}{
			"txid": "tx1",
			"vout": []interface{}{},
			"vin":  []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetRawTransactionVerbose([]string{"tx1", "tx2"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(errs, HasLen, 2)
}

func (s *RPCSuite) TestBatchGetRawTransactionVerboseZEC(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return map[string]interface{}{
			"txid": "tx1",
			"vout": []interface{}{},
			"vin":  []interface{}{},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetRawTransactionVerbose([]string{"tx1"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 1)
	c.Assert(errs, HasLen, 1)
}

// ---------------------------------------------------------------------------
// SendRawTransaction tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSendRawTransaction(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "sendrawtransaction")
		return "txid_result_123", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	txData := []byte{0x01, 0x02, 0x03}
	tx := &mockTx{data: txData}
	txid, err := cl.SendRawTransaction(tx, 0.1)
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "txid_result_123")
}

func (s *RPCSuite) TestSendRawTransactionNilTx(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return "txid_nil", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	txid, err := cl.SendRawTransaction(nil, 0)
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "txid_nil")
}

func (s *RPCSuite) TestSendRawTransactionSerializeError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	tx := &mockTx{data: []byte{0x01}, serError: fmt.Errorf("serialize failed")}
	_, err = cl.SendRawTransaction(tx, 0)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "serialize failed")
}

func (s *RPCSuite) TestSendRawTransactionHex(c *C) {
	log := zerolog.Nop()
	var receivedHex string
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		if len(params) > 0 {
			if s, ok := params[0].(string); ok {
				receivedHex = s
			}
		}
		return "txid_hex", nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	tx := &mockTx{data: data}
	_, err = cl.SendRawTransaction(tx, false)
	c.Assert(err, IsNil)
	c.Assert(receivedHex, Equals, hex.EncodeToString(data))
}

// ---------------------------------------------------------------------------
// ImportAddress tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestImportAddress(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "importaddress")
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.ImportAddress("bc1qtest")
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// ImportAddressRescan tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestImportAddressRescan(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "importaddress")
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.ImportAddressRescan("bc1qtest")
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// CreateWallet tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestCreateWallet(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "createwallet")
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.CreateWallet("test-wallet")
	c.Assert(err, IsNil)
}

func (s *RPCSuite) TestCreateWalletAlreadyExistsZEC(c *C) {
	log := zerolog.Nop()

	// ZEC path uses zec.Client which goes through HTTP directly.
	// Create a server that returns -4 error via HTTP with proper JSON-RPC error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a non-200 status with a JSON body containing code -4
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":-4,"message":"wallet already exists"}}`))
	}))
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	// The zec client will get a non-200 response, the error goes through extractBTCError
	// but the format won't produce a *btcjson.RPCError via extractBTCError since it's from zec client
	err = cl.CreateWallet("existing-wallet")
	c.Assert(err, NotNil) // ZEC client returns error on non-200
}

func (s *RPCSuite) TestCreateWalletAlreadyExistsExtractBTCError(c *C) {
	// Test the CreateWallet error suppression logic:
	// extractBTCError returns a *btcjson.RPCError, which CreateWallet checks for code -4
	rpcErr := btcjson.NewRPCError(btcjson.ErrRPCWallet, "wallet already exists")
	c.Assert(rpcErr.Code, Equals, btcjson.ErrRPCWallet)

	// Verify extractBTCError can produce btcjson.RPCError from a formatted error string
	err := extractBTCError(fmt.Errorf(`500 Internal Server Error: {"error":{"code":-4,"message":"wallet already exists"}}`))
	rpcErr2, ok := err.(*btcjson.RPCError)
	c.Assert(ok, Equals, true)
	c.Assert(rpcErr2.Code, Equals, btcjson.ErrRPCWallet)
}

func (s *RPCSuite) TestCreateWalletOtherError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.ErrRPCInternal.Code, "internal error")
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	err = cl.CreateWallet("test")
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// GetZecAddressUTXOs tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetZecAddressUTXOsPreservesRawHeightAndIntegerValue(c *C) {
	// The RPC adapter preserves even an out-of-supply value exactly; monetary
	// validation belongs to the selector. This odd value detects float64 loss.
	const exactValue = uint64(9007199254740993)
	expected := []ZecAddressUTXO{
		{Txid: strings.Repeat("AB", 32), Address: "t1vault", Index: 3, Script: "76A914ABC88AC", Satoshis: exactValue, Height: 3500001},
		{Txid: strings.Repeat("cd", 32), Address: "t1vault", Index: 0, Script: "76a914abc88ac", Satoshis: 1, Height: -1},
		{Txid: strings.Repeat("ef", 32), Address: "t1vault", Index: 2, Script: "76a914abc88ac", Satoshis: 123456789, Height: 0},
	}
	var methods []string
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		methods = append(methods, method)
		c.Assert(method, Equals, "getaddressutxos")
		c.Assert(params, DeepEquals, []interface{}{"t1vault"})
		return expected, nil
	})
	defer server.Close()
	client, err := NewClient(server.URL, "user", "pass", 0, time.Second, common.ZECChain, zerolog.Nop())
	c.Assert(err, IsNil)
	result, err := client.GetZecAddressUTXOs("t1vault")
	c.Assert(err, IsNil)
	c.Assert(result, DeepEquals, expected)
	// No tip query or derived confirmation count may rewrite creation heights.
	c.Assert(methods, DeepEquals, []string{"getaddressutxos"})
}

func (s *RPCSuite) TestGetZecAddressUTXOsPropagatesRPCAndDecodeErrors(c *C) {
	for _, response := range []struct {
		name   string
		result interface{}
		rpcErr *btcjson.RPCError
		match  string
	}{
		{"RPC failure", nil, btcjson.NewRPCError(btcjson.ErrRPCInternal.Code, "utxo index unavailable"), ".*utxo index unavailable.*"},
		{"negative value", json.RawMessage(`[{"txid":"bad","satoshis":-1,"height":3500000}]`), nil, ".*cannot unmarshal.*"},
		{"fractional value", json.RawMessage(`[{"txid":"bad","satoshis":1.5,"height":3500000}]`), nil, ".*cannot unmarshal.*"},
	} {
		server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
			c.Assert(method, Equals, "getaddressutxos")
			return response.result, response.rpcErr
		})
		client, err := NewClient(server.URL, "user", "pass", 0, time.Second, common.ZECChain, zerolog.Nop())
		c.Assert(err, IsNil)
		_, err = client.GetZecAddressUTXOs("t1vault")
		c.Assert(err, ErrorMatches, response.match, Commentf("%s", response.name))
		server.Close()
	}
}

func (s *RPCSuite) TestGetZecAddressUTXOsRejectsOtherChainsWithoutRPC(c *C) {
	calls := 0
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		calls++
		return nil, nil
	})
	defer server.Close()
	client, err := NewClient(server.URL, "user", "pass", 0, time.Second, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)
	result, err := client.GetZecAddressUTXOs("bc1vault")
	c.Assert(err, ErrorMatches, "ZEC address UTXOs requested for BTC")
	c.Assert(result, IsNil)
	c.Assert(calls, Equals, 0)
}

// ---------------------------------------------------------------------------
// ListUnspent tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestListUnspent(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "listunspent")
		return []map[string]interface{}{
			{
				"txid":          "txid1",
				"vout":          0,
				"address":       "bc1qtest",
				"scriptPubKey":  "0014abc",
				"amount":        1.5,
				"confirmations": 10,
				"spendable":     true,
			},
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	unspent, err := cl.ListUnspent("bc1qtest")
	c.Assert(err, IsNil)
	c.Assert(unspent, HasLen, 1)
	c.Assert(unspent[0].TxID, Equals, "txid1")
}

func (s *RPCSuite) TestListUnspentZEC(c *C) {
	log := zerolog.Nop()
	callCount := 0
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		callCount++
		switch method {
		case "getblockcount":
			return 1000, nil
		case "getaddressutxos":
			return []map[string]interface{}{
				{
					"txid":        "zectx1",
					"address":     "t1test",
					"outputIndex": 0,
					"script":      "76a914abc88ac",
					"satoshis":    100000000,
					"height":      990,
				},
			}, nil
		}
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	unspent, err := cl.ListUnspent("t1test")
	c.Assert(err, IsNil)
	c.Assert(unspent, HasLen, 1)
	c.Assert(unspent[0].TxID, Equals, "zectx1")
	c.Assert(unspent[0].Amount, Equals, 1.0)
	c.Assert(unspent[0].Confirmations, Equals, int64(10))
	c.Assert(unspent[0].Spendable, Equals, true)
}

func (s *RPCSuite) TestListUnspentZECBlockCountError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		if method == "getblockcount" {
			return nil, btcjson.NewRPCError(btcjson.ErrRPCInternal.Code, "block count error")
		}
		return nil, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	_, err = cl.ListUnspent("t1test")
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// GetNetworkInfo tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestGetNetworkInfo(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		c.Assert(method, Equals, "getnetworkinfo")
		return map[string]interface{}{
			"version":         210100,
			"subversion":      "/Satoshi:21.1.0/",
			"protocolversion": 70016,
			"relayfee":        0.00001,
		}, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	info, err := cl.GetNetworkInfo()
	c.Assert(err, IsNil)
	c.Assert(info, NotNil)
	c.Assert(info.Version, Equals, int32(210100))
}

// ---------------------------------------------------------------------------
// Call routing tests (BTC vs ZEC)
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestCallZEC(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return 12345, nil
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.ZECChain, log)
	c.Assert(err, IsNil)

	var count int64
	err = cl.Call(&count, "getblockcount")
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(12345))
}

// ---------------------------------------------------------------------------
// extractBTCError tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestExtractBTCErrorNil(c *C) {
	c.Assert(extractBTCError(nil), IsNil)
}

func (s *RPCSuite) TestExtractBTCErrorNonRPC(c *C) {
	err := extractBTCError(errors.New("simple error without colon"))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "simple error without colon")
}

func (s *RPCSuite) TestExtractBTCErrorWithRPCJSON(c *C) {
	err := extractBTCError(fmt.Errorf("404 Not Found: %s", `{"error":{"code":-32601,"message":"Method not found"}}`))
	c.Assert(err, NotNil)
	rpcErr, ok := err.(*btcjson.RPCError)
	c.Assert(ok, Equals, true)
	c.Assert(rpcErr.Code, Equals, btcjson.RPCErrorCode(-32601))
	c.Assert(rpcErr.Message, Equals, "Method not found")
}

func (s *RPCSuite) TestExtractBTCErrorInvalidJSON(c *C) {
	err := extractBTCError(fmt.Errorf("500 Internal Server Error: not json"))
	c.Assert(err, NotNil)
	// Should return the original error since JSON parsing failed
	c.Assert(err.Error(), Equals, "500 Internal Server Error: not json")
}

// ---------------------------------------------------------------------------
// retry tests (expanded)
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestRetry(c *C) {
	cl := Client{maxRetries: 3}
	called := 0
	err := cl.retry(func() error {
		called++
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(called, Equals, 1)

	called = 0
	err = cl.retry(func() error {
		called++
		return errors.New("error")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 1)

	called = 0
	c.Assert(s.backoffs, HasLen, 0)

	err = cl.retry(func() error {
		called++
		return errors.New("500 Internal Server Error: work queue depth exceeded")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 4)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second})

	called = 0
	s.backoffs = nil
	err = cl.retry(func() error {
		called++
		if called < 2 {
			return errors.New("500 Internal Server Error: work queue depth exceeded")
		}
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

// The backoff starts at 1s and doubles per retry, up to 10s, and outside the
// tests retry sleeps it.
func (s *RPCSuite) TestRetryBackoff(c *C) {
	cl := Client{maxRetries: 6, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("503 Service Unavailable: server overloaded")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 7)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second,
	})
	c.Assert(reflect.ValueOf(realRetrySleep).Pointer(), Equals, reflect.ValueOf(time.Sleep).Pointer())
}

func (s *RPCSuite) TestRetryConnectionRefused(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("dial tcp 127.0.0.1:8332: connect: connection refused")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryEOF(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("Post http://localhost:8332: EOF")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryLoadingBlockIndex(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("loading block index")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryVerifyingWallet(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("verifying wallet")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryRescanning(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("currently rescanning")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetry503(c *C) {
	cl := Client{maxRetries: 1, log: zerolog.Nop()}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("503 Service Unavailable: server overloaded")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 2)
	c.Assert(s.backoffs, DeepEquals, []time.Duration{time.Second})
}

func (s *RPCSuite) TestRetryZeroRetries(c *C) {
	cl := Client{maxRetries: 0}
	called := 0
	err := cl.retry(func() error {
		called++
		return errors.New("connect: connection refused")
	})
	c.Assert(err, NotNil)
	c.Assert(called, Equals, 1) // no retries, just the initial call
	c.Assert(s.backoffs, HasLen, 0)
}

// ---------------------------------------------------------------------------
// Batch error path tests
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetMempoolEntryServerError(c *C) {
	log := zerolog.Nop()
	srv := newErrorServer(500)
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	_, _, err = cl.BatchGetMempoolEntry([]string{"tx1"})
	c.Assert(err, NotNil)
}

func (s *RPCSuite) TestBatchGetRawTransactionVerboseServerError(c *C) {
	log := zerolog.Nop()
	srv := newErrorServer(500)
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	_, _, err = cl.BatchGetRawTransactionVerbose([]string{"tx1"})
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// Batch with individual errors
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestBatchGetMempoolEntryWithPerItemError(c *C) {
	log := zerolog.Nop()
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var batchReqs []jsonRPCRequest
		if err := json.Unmarshal(body, &batchReqs); err == nil && len(batchReqs) > 0 {
			var responses []json.RawMessage
			for _, req := range batchReqs {
				callNum++
				resp := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
				}
				if callNum == 1 {
					resp["result"] = map[string]interface{}{"vsize": 200}
				} else {
					resp["error"] = map[string]interface{}{
						"code":    -5,
						"message": "Transaction not in mempool",
					}
				}
				raw, _ := json.Marshal(resp)
				responses = append(responses, raw)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(responses)
			return
		}
	}))
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	results, errs, err := cl.BatchGetMempoolEntry([]string{"tx1", "tx2"})
	c.Assert(err, IsNil)
	c.Assert(results, HasLen, 2)
	c.Assert(errs[0], IsNil)
	c.Assert(errs[1], NotNil)
}

// ---------------------------------------------------------------------------
// SendRawTransaction with RPC error
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSendRawTransactionRPCError(c *C) {
	log := zerolog.Nop()
	srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		return nil, btcjson.NewRPCError(btcjson.ErrRPCVerify, "TX rejected")
	})
	defer srv.Close()

	cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
	c.Assert(err, IsNil)

	tx := &mockTx{data: []byte{0x01}}
	_, err = cl.SendRawTransaction(tx, 0)
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// Serialize helper for SendRawTransaction encoding
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSerializableTxInterface(c *C) {
	data := []byte{0x01, 0x02, 0x03, 0x04}
	tx := &mockTx{data: data}
	c.Assert(tx.SerializeSize(), Equals, 4)

	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	c.Assert(err, IsNil)
	c.Assert(buf.Bytes(), DeepEquals, data)
}

// ---------------------------------------------------------------------------
// ZecClientTimeout constant
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestZecClientTimeout(c *C) {
	c.Assert(ZecClientTimeout, Equals, time.Second*10)
}

// ---------------------------------------------------------------------------
// Mock rpcClient – asserts BatchCallContext is used (not BatchCall)
// ---------------------------------------------------------------------------

type mockRPCClient struct {
	batchCallCalled        bool
	batchCallContextCalled bool
	lastCtx                context.Context
}

func (m *mockRPCClient) Call(result any, method string, args ...any) error {
	return nil
}

func (m *mockRPCClient) CallContext(ctx context.Context, result any, method string, args ...any) error {
	return nil
}

func (m *mockRPCClient) BatchCall(batch []ethrpc.BatchElem) error {
	m.batchCallCalled = true
	return nil
}

func (m *mockRPCClient) BatchCallContext(ctx context.Context, batch []ethrpc.BatchElem) error {
	m.batchCallContextCalled = true
	m.lastCtx = ctx
	// Populate results so the callers don't fail on type assertions.
	for i := range batch {
		switch batch[i].Result.(type) {
		case *btcjson.GetMempoolEntryResult:
			batch[i].Result = &btcjson.GetMempoolEntryResult{}
		case *btcjson.TxRawResult:
			batch[i].Result = &btcjson.TxRawResult{}
		}
	}
	return nil
}

// TestBatchGetMempoolEntryUsesBatchCallContext verifies that BatchGetMempoolEntry
// dispatches via BatchCallContext (not BatchCall) and passes a context with a
// deadline when the client has a positive timeout.
func (s *RPCSuite) TestBatchGetMempoolEntryUsesBatchCallContext(c *C) {
	mock := &mockRPCClient{}
	cl := &Client{
		client:  mock,
		log:     zerolog.Nop(),
		chain:   common.BTCChain,
		timeout: 10 * time.Second,
	}

	_, _, err := cl.BatchGetMempoolEntry([]string{"txid1"})
	c.Assert(err, IsNil)

	c.Assert(mock.batchCallCalled, Equals, false)
	c.Assert(mock.batchCallContextCalled, Equals, true)

	deadline, ok := mock.lastCtx.Deadline()
	c.Assert(ok, Equals, true)
	c.Assert(deadline.After(time.Now()), Equals, true)
}

// TestBatchGetRawTransactionVerboseUsesBatchCallContext verifies that
// BatchGetRawTransactionVerbose dispatches via BatchCallContext (not BatchCall)
// and passes a context with a deadline when the client has a positive timeout.
func (s *RPCSuite) TestBatchGetRawTransactionVerboseUsesBatchCallContext(c *C) {
	mock := &mockRPCClient{}
	cl := &Client{
		client:  mock,
		log:     zerolog.Nop(),
		chain:   common.BTCChain,
		timeout: 10 * time.Second,
	}

	_, _, err := cl.BatchGetRawTransactionVerbose([]string{"tx1"})
	c.Assert(err, IsNil)

	c.Assert(mock.batchCallCalled, Equals, false)
	c.Assert(mock.batchCallContextCalled, Equals, true)

	deadline, ok := mock.lastCtx.Deadline()
	c.Assert(ok, Equals, true)
	c.Assert(deadline.After(time.Now()), Equals, true)
}

// TestGetNetworkInfoWarnings verifies getnetworkinfo decodes on both the pre-28
// string "warnings" and the Core 28/29 array "warnings" without dropping RelayFee.
func (s *RPCSuite) TestGetNetworkInfoWarnings(c *C) {
	log := zerolog.Nop()
	cases := []struct {
		name     string
		warnings interface{}
	}{
		{"core<=27 string", "some warning"},
		{"core 28/29 array", []string{"warn a", "warn b"}},
		{"core 28/29 empty array", []string{}},
	}
	for _, tc := range cases {
		srv := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
			c.Assert(method, Equals, "getnetworkinfo")
			return map[string]interface{}{
				"relayfee": 0.00001,
				"warnings": tc.warnings,
			}, nil
		})

		cl, err := NewClient(srv.URL, "user", "pass", 0, time.Second*10, common.BTCChain, log)
		c.Assert(err, IsNil)

		info, err := cl.GetNetworkInfo()
		c.Assert(err, IsNil, Commentf("case %s", tc.name))
		c.Assert(info, NotNil)
		c.Assert(info.RelayFee, Equals, 0.00001, Commentf("case %s: RelayFee must survive decode", tc.name))
		srv.Close()
	}
}

// TestFlexibleWarningsUnmarshal covers the raw decode of the warnings field.
func (s *RPCSuite) TestFlexibleWarningsUnmarshal(c *C) {
	var w flexibleWarnings
	c.Assert(json.Unmarshal([]byte(`"hi"`), &w), IsNil)
	c.Assert([]string(w), DeepEquals, []string{"hi"})

	w = nil
	c.Assert(json.Unmarshal([]byte(`["a","b"]`), &w), IsNil)
	c.Assert([]string(w), DeepEquals, []string{"a", "b"})

	w = nil
	c.Assert(json.Unmarshal([]byte(`""`), &w), IsNil)
	c.Assert(len(w), Equals, 0)

	w = nil
	c.Assert(json.Unmarshal([]byte(`null`), &w), IsNil)
	c.Assert(len(w), Equals, 0)

	w = nil
	c.Assert(json.Unmarshal([]byte(`123`), &w), NotNil)
}
