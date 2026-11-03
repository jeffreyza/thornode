package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/zec"
	"gitlab.com/thorchain/thornode/v3/common"
)

// op-geth rpc uses 10s timeouts
const ZecClientTimeout = time.Second * 10

////////////////////////////////////////////////////////////////////////////////////////
// Interfaces
////////////////////////////////////////////////////////////////////////////////////////

type SerializableTx interface {
	SerializeSize() int
	Serialize(io.Writer) error
}

type rpcClient interface {
	Call(result any, method string, args ...any) error
	BatchCall(batch []rpc.BatchElem) error
	CallContext(ctx context.Context, result any, method string, args ...any) error
	BatchCallContext(ctx context.Context, batch []rpc.BatchElem) error
}

////////////////////////////////////////////////////////////////////////////////////////
// Client
////////////////////////////////////////////////////////////////////////////////////////

// Client represents a client connection to a UTXO daemon. Internally this uses the
// Ethereum JSON-RPC 2.0 implementation, which allows batching and connection reuse.
type Client struct {
	client     rpcClient
	log        zerolog.Logger
	chain      common.Chain
	maxRetries int
	timeout    time.Duration
}

// NewClient returns a client connection to a UTXO daemon.
func NewClient(host, user, password string, maxRetries int, timeout time.Duration, chain common.Chain, log zerolog.Logger) (
	*Client, error,
) {
	authFn := func(h http.Header) error {
		auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		h.Set("Authorization", fmt.Sprintf("Basic %s", auth))
		return nil
	}

	// default to http if no scheme is specified
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}

	client := Client{
		log:        log,
		chain:      chain,
		maxRetries: maxRetries,
		timeout:    timeout,
	}

	var err error

	switch chain {
	case common.ZECChain:
		header := http.Header{}
		err = authFn(header)
		if err != nil {
			return nil, err
		}
		client.client = zec.NewClient(host, ZecClientTimeout, header)
	default:
		client.client, err = rpc.DialOptions(context.Background(), host, rpc.WithHTTPAuth(authFn))
	}

	if err != nil {
		return nil, err
	}

	return &client, nil
}

// GetBlockCount returns the number of blocks in the longest block chain.
func (c *Client) GetBlockCount() (int64, error) {
	var count int64
	err := c.Call(&count, "getblockcount")
	return count, extractBTCError(err)
}

// SendRawTransaction serializes and sends the transaction. The maxFeeParam differs in
// type between chains - ensure the correct variant is used.
func (c *Client) SendRawTransaction(tx SerializableTx, maxFeeParam any) (string, error) {
	txHex := ""
	if tx != nil {
		buf := bytes.NewBuffer(make([]byte, 0, tx.SerializeSize()))
		if err := tx.Serialize(buf); err != nil {
			return "", err
		}
		txHex = hex.EncodeToString(buf.Bytes())
	}

	args := []interface{}{txHex, maxFeeParam}

	var txid string
	err := c.Call(&txid, "sendrawtransaction", args...)
	return txid, extractBTCError(err)
}

// GetBlockHash returns the hash of the block in best-block-chain at the given height.
func (c *Client) GetBlockHash(height int64) (string, error) {
	var hash string
	err := c.Call(&hash, "getblockhash", height)
	return hash, extractBTCError(err)
}

// GetBlockVerboseTxs returns information about the block with verbosity 2.
func (c *Client) GetBlockVerboseTxs(hash string) (*BlockResult, error) {
	var block BlockResult
	err := c.Call(&block, "getblock", hash, 2)
	return &block, extractBTCError(err)
}

// GetBlockVerbose returns information about the block with verbosity 1.
func (c *Client) GetBlockVerbose(hash string) (*btcjson.GetBlockVerboseResult, error) {
	var block btcjson.GetBlockVerboseResult
	err := c.Call(&block, "getblock", hash, 1)
	return &block, extractBTCError(err)
}

// GetBlockStats returns statistics about the block at the given hash.
func (c *Client) GetBlockStats(hash string) (*btcjson.GetBlockStatsResult, error) {
	var stats btcjson.GetBlockStatsResult
	err := c.Call(&stats, "getblockstats", hash)
	return &stats, extractBTCError(err)
}

// GetMempoolEntry returns mempool data for the given transaction.
func (c *Client) GetMempoolEntry(txid string) (*btcjson.GetMempoolEntryResult, error) {
	var entry btcjson.GetMempoolEntryResult
	err := c.Call(&entry, "getmempoolentry", txid)
	return &entry, extractBTCError(err)
}

// BatchGetMempoolEntry returns mempool data for the given transactions.
func (c *Client) BatchGetMempoolEntry(txids []string) ([]*btcjson.GetMempoolEntryResult, []error, error) {
	// create batch request
	batch := []rpc.BatchElem{}
	for _, txid := range txids {
		batch = append(batch, rpc.BatchElem{
			Method: "getmempoolentry",
			Args:   []interface{}{txid},
			Result: &btcjson.GetMempoolEntryResult{},
		})
	}

	// call batch request
	err := c.BatchCall(batch)
	if err != nil {
		return nil, nil, err
	}

	// collect results
	errs := make([]error, len(txids))
	results := make([]*btcjson.GetMempoolEntryResult, len(txids))
	var ok bool
	for i, b := range batch {
		results[i], ok = b.Result.(*btcjson.GetMempoolEntryResult)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected type returned from batch call: %T", b.Result)
		}
		errs[i] = extractBTCError(b.Error)
	}

	return results, errs, nil
}

// GetRawMempool returns all transaction ids in the mempool.
func (c *Client) GetRawMempool() ([]string, error) {
	var txids []string
	err := c.Call(&txids, "getrawmempool")
	return txids, extractBTCError(err)
}

// GetRawTransactionVerbose returns a raw transaction for the transaction id.
func (c *Client) GetRawTransactionVerbose(txID string) (*TxResult, error) {
	var tx TxResult
	err := c.Call(&tx, "getrawtransaction", txID, true)
	return &tx, extractBTCError(err)
}

// GetRawTransaction returns a raw transaction string for the transaction id.
func (c *Client) GetRawTransaction(txid string) (string, error) {
	var tx string
	err := c.Call(&tx, "getrawtransaction", txid, false)
	return tx, extractBTCError(err)
}

// BatchGetRawTransactionVerbose returns a raw transaction for given transaction ids.
func (c *Client) BatchGetRawTransactionVerbose(txIDs []string) ([]*TxResult, []error, error) {
	// create batch request
	batch := make([]rpc.BatchElem, 0, len(txIDs))
	for _, txID := range txIDs {
		args := []interface{}{txID, true}
		batch = append(batch, rpc.BatchElem{
			Method: "getrawtransaction",
			Args:   args,
			Result: &TxResult{},
		})
	}

	// call batch request
	err := c.BatchCall(batch)
	if err != nil {
		return nil, nil, err
	}

	// collect results
	errs := make([]error, len(txIDs))
	results := make([]*TxResult, len(txIDs))
	var ok bool
	for i, b := range batch {
		results[i], ok = b.Result.(*TxResult)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected type returned from batch call: %T", b.Result)
		}
		errs[i] = extractBTCError(b.Error)
	}

	return results, errs, nil
}

// ImportAddress imports the address with no rescan.
func (c *Client) ImportAddress(address string) error {
	err := c.Call(nil, "importaddress", address, "", false)
	return extractBTCError(err)
}

// ImportAddressRescan imports the address with rescan.
func (c *Client) ImportAddressRescan(address string) error {
	err := c.Call(nil, "importaddress", address, "", true)
	return extractBTCError(err)
}

// IsAddressImported reports whether the node already tracks the address.
func (c *Client) IsAddressImported(address string) (bool, error) {
	info, err := c.getAddressTrackingInfo("getaddressinfo", address)
	if err != nil && isMethodNotFoundError(err) {
		info, err = c.getAddressTrackingInfo("validateaddress", address)
	}
	if err != nil {
		return false, extractBTCError(err)
	}

	return info.IsMine || info.IsWatchOnly, nil
}

func (c *Client) getAddressTrackingInfo(method, address string) (struct {
	IsMine      bool `json:"ismine"`
	IsWatchOnly bool `json:"iswatchonly"`
}, error,
) {
	var info struct {
		IsMine      bool `json:"ismine"`
		IsWatchOnly bool `json:"iswatchonly"`
	}

	err := c.Call(&info, method, address)
	if err != nil {
		return info, err
	}

	return info, nil
}

func isMethodNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	var rpcErr *btcjson.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == btcjson.RPCErrorCode(-32601) {
		return true
	}

	return strings.Contains(strings.ToLower(err.Error()), "method not found")
}

// CreateWallet creates a new wallet.
func (c *Client) CreateWallet(name string) error {
	err := c.Call(nil, "createwallet", name, false, false, "", false, false)
	err = extractBTCError(err)

	// ignore code -4 (wallet already exists)
	if rpcErr, ok := err.(*btcjson.RPCError); ok && rpcErr.Code == btcjson.ErrRPCWallet {
		return nil
	}

	return err
}

// ZecAddressUTXO preserves the block height and integer value returned by Zebra.
// Signing must not recover this height from confirmations at a moving RPC tip.
type ZecAddressUTXO struct {
	Txid     string `json:"txid"`
	Address  string `json:"address"`
	Index    uint32 `json:"outputIndex"`
	Script   string `json:"script"`
	Satoshis uint64 `json:"satoshis"`
	Height   int64  `json:"height"`
}

func (c *Client) GetZecAddressUTXOs(address string) ([]ZecAddressUTXO, error) {
	if c.chain != common.ZECChain {
		return nil, fmt.Errorf("ZEC address UTXOs requested for %s", c.chain)
	}
	var utxos []ZecAddressUTXO
	err := c.Call(&utxos, "getaddressutxos", []any{address}...)
	return utxos, extractBTCError(err)
}

// ListUnspent returns all unspent outputs with between min and max confirmations.
func (c *Client) ListUnspent(address string) ([]btcjson.ListUnspentResult, error) {
	var unspent []btcjson.ListUnspentResult
	var err error

	switch c.chain {
	case common.ZECChain:
		var height int64
		height, err = c.GetBlockCount()
		if err != nil {
			return nil, err
		}

		var utxos []ZecAddressUTXO
		utxos, err = c.GetZecAddressUTXOs(address)

		for _, utxo := range utxos {
			unspent = append(unspent, btcjson.ListUnspentResult{
				TxID:          utxo.Txid,
				Vout:          utxo.Index,
				Address:       utxo.Address,
				ScriptPubKey:  utxo.Script,
				Amount:        float64(utxo.Satoshis) / 1e8,
				Confirmations: height - utxo.Height,
				Spendable:     true,
			})
		}
	default:
		const minConfirm = 0
		const maxConfirm = 9999999
		err = c.Call(&unspent, "listunspent", minConfirm, maxConfirm, []string{address})
	}

	return unspent, extractBTCError(err)
}

// flexibleWarnings decodes bitcoind's getnetworkinfo "warnings" field, which is
// a plain string on Bitcoin Core <=27 and an array of strings on Core 28/29.
type flexibleWarnings []string

func (w *flexibleWarnings) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	switch b[0] {
	case '[':
		return json.Unmarshal(b, (*[]string)(w))
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s != "" {
			*w = []string{s}
		}
		return nil
	default:
		return fmt.Errorf("unexpected getnetworkinfo warnings shape: %s", b)
	}
}

func (c *Client) GetNetworkInfo() (*btcjson.GetNetworkInfoResult, error) {
	// Decode into a shadow of GetNetworkInfoResult whose warnings field accepts
	// both the pre-28 string and the Core 28/29 array shapes. The shallower
	// (outer) warnings field wins over the embedded string one during decode, so
	// the array shape no longer aborts the whole unmarshal (dropping RelayFee).
	var info struct {
		btcjson.GetNetworkInfoResult
		Warnings flexibleWarnings `json:"warnings"`
	}
	if err := c.Call(&info, "getnetworkinfo"); err != nil {
		return nil, extractBTCError(err)
	}
	result := info.GetNetworkInfoResult
	result.Warnings = strings.Join(info.Warnings, "; ")
	return &result, nil
}

func (c *Client) Call(result any, method string, args ...interface{}) error {
	fn := func() error {
		if c.timeout <= 0 {
			return c.client.Call(result, method, args...)
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		return c.client.CallContext(ctx, result, method, args...)
	}

	err := c.retry(fn)
	return extractBTCError(err)
}

func (c *Client) BatchCall(batch []rpc.BatchElem) error {
	err := c.retry(func() error {
		return c.batchCall(batch)
	})
	return extractBTCError(err)
}

func (c *Client) batchCall(batch []rpc.BatchElem) error {
	if c.timeout <= 0 {
		return c.client.BatchCall(batch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.client.BatchCallContext(ctx, batch)
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// Ethereum RPC returns an error with the response appended to the HTTP status like:
// 404 Not Found: {"error":{"code":-32601,"message":"Method not found"},"id":1}
//
// This makes best effort to extract and return the error as a btcjson.RPCError.
func extractBTCError(err error) error {
	if err == nil {
		return nil
	}

	// Bitcoin Core >=28 answers JSON-RPC 2.0 requests with HTTP 200 and a
	// spec-compliant error object, which the go-ethereum rpc client surfaces as
	// an rpc.Error (structured code + message) rather than the pre-28 HTTP-500
	// "<status>: <json body>" string the split below expects. Convert it
	// directly so the RPC error code (e.g. -4 wallet already exists) survives.
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		return btcjson.NewRPCError(btcjson.RPCErrorCode(rpcErr.ErrorCode()), rpcErr.Error())
	}

	// split the error into the HTTP status and the JSON response
	parts := strings.SplitN(err.Error(), ": ", 2)
	if len(parts) != 2 {
		return err
	}

	// parse the JSON response
	var response struct {
		Error struct {
			Code    btcjson.RPCErrorCode `json:"code"`
			Message string               `json:"message"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal([]byte(parts[1]), &response); jsonErr != nil {
		return err
	}

	// return the error message
	return btcjson.NewRPCError(response.Error.Code, response.Error.Message)
}

// retrySleep waits out a retry backoff. Tests replace it to record the
// backoffs instead of sleeping through them.
var retrySleep = time.Sleep

// retry will wrap the function call with a retry loop on specific errors - namely 500
// response when the daemon is overloaded the the work queue is exceeded. We use a
// simple static backoff of 1 second for now.
func (c *Client) retry(fn func() error) error {
	var err error
	backoff := time.Second
	for i := 0; i <= c.maxRetries; i++ {
		err = fn()
		if err == nil {
			break
		}

		errStr := strings.ToLower(err.Error())

		// check in order of most to least likely based on log sampling
		retry := strings.Contains(errStr, "connect: connection refused") ||
			strings.Contains(errStr, "work queue depth exceeded") ||
			(strings.HasPrefix(errStr, "post") && strings.HasSuffix(errStr, "eof")) ||
			strings.Contains(errStr, "loading block index") || // daemon startup
			strings.Contains(errStr, "verifying wallet") || // daemon startup
			strings.Contains(errStr, "verifying wallet") || // daemon startup
			strings.Contains(errStr, "currently rescanning") || // rescanning wallet
			strings.HasPrefix(errStr, "503 service unavailable")

		// break if not a retryable error
		if !retry {
			break
		}

		// break if this was the last retry
		if i == c.maxRetries {
			break
		}

		// backoff and retry
		c.log.Err(err).
			Str("backoff", backoff.String()).
			Msgf("retrying %d/%d after backoff", i+1, c.maxRetries)
		retrySleep(backoff)

		// exponential backoff, max 10 seconds
		backoff *= 2
		if backoff > time.Second*10 {
			backoff = time.Second * 10
		}
	}
	return err
}
