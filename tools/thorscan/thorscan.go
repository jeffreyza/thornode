package thorscan

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ctypes "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/app/params"
	prefix "gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// -------------------------------------------------------------------------------------
// Config
// -------------------------------------------------------------------------------------

const (
	// ---------- environment keys ----------

	EnvAPIEndpoint = "API_ENDPOINT"
	EnvParallelism = "PARALLELISM"
)

// -------------------------------------------------------------------------------------
// HTTP
// -------------------------------------------------------------------------------------

// Transport sets the X-Client-ID header on all requests.
type Transport struct {
	Transport http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("X-Client-ID", "thorscan-go")
	return t.Transport.RoundTrip(req)
}

var httpClient *http.Client

// -------------------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------------------

func getBlock(height int64) (*BlockResponse, error) {
	url := APIEndpoint + "/thorchain/block"
	if height > 0 {
		url += "?height=" + strconv.FormatInt(height, 10)
	}

	// build request
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// accept gzip
	req.Header.Set("Accept-Encoding", "gzip")

	// send request
	var res *http.Response
	res, err = httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// close body
	defer res.Body.Close()

	// wrap response body in a gzip reader
	if strings.Contains(res.Header.Get("Content-Encoding"), "gzip") {
		res.Body, err = gzip.NewReader(res.Body)
		if err != nil {
			return nil, err
		}
	}

	// check status code
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("block not found")
	case http.StatusInternalServerError:
		// attempt to read the body
		var body []byte
		body, err = io.ReadAll(res.Body)
		if err == nil && bytes.Contains(body, []byte("cannot query with height in the future")) {
			return nil, fmt.Errorf("block not found")
		}
		fallthrough
	default:
		return nil, fmt.Errorf("bad status code: %d", res.StatusCode)
	}

	// decode response
	var blockResp BlockResponse
	err = json.NewDecoder(res.Body).Decode(&blockResp)
	if err != nil {
		log.Error().Err(err).Int64("height", height).Msg("failed to decode block response")
	}

	return &blockResp, nil
}

// -------------------------------------------------------------------------------------
// Init
// -------------------------------------------------------------------------------------

var (
	Parallelism = 4
	APIEndpoint = "https://gateway.liquify.com/chain/thorchain_api"

	encodingConfig params.EncodingConfig
)

// SetAPIEndpoint sets the API endpoint to use.
func SetAPIEndpoint(endpoint string) {
	APIEndpoint = endpoint
}

func init() {
	var err error

	// set log level
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// set config from env
	if e := os.Getenv(EnvAPIEndpoint); e != "" {
		log.Info().Str("endpoint", e).Msg("setting api endpoint")
		APIEndpoint = e
	}
	if e := os.Getenv(EnvParallelism); e != "" {
		log.Info().Str("prefetch", e).Msg("setting prefetch blocks")
		Parallelism, err = strconv.Atoi(e)
		if err != nil {
			log.Fatal().Err(err).Msg("bad prefetch value")
		}
	}

	// use our own transport to set the client id
	transport := &Transport{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          Parallelism * 2,
			MaxIdleConnsPerHost:   Parallelism * 2,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	// create new client with better connection reuse
	httpClient = &http.Client{Transport: transport}

	// create encoding config
	ccfg := cosmos.GetConfig()
	ccfg.SetBech32PrefixForAccount(prefix.Bech32PrefixAccAddr, prefix.Bech32PrefixAccPub)
	ccfg.SetBech32PrefixForValidator(prefix.Bech32PrefixValAddr, prefix.Bech32PrefixValPub)
	ccfg.SetBech32PrefixForConsensusNode(prefix.Bech32PrefixConsAddr, prefix.Bech32PrefixConsPub)
	ccfg.Seal()
	encodingConfig = app.MakeEncodingConfig()
}

// -------------------------------------------------------------------------------------
// Types
// -------------------------------------------------------------------------------------

// BlockTx wraps the openapi type with a custom Tx field for unmarshaling.
type BlockTx struct {
	openapi.BlockTx
	Tx ctypes.Tx `json:"tx"`
}

func (b *BlockTx) UnmarshalJSON(data []byte) error {
	// unmarshal into temporary type with raw json tx
	type unmarshalQueryBlockTx struct {
		openapi.BlockTx
		Tx json.RawMessage `json:"tx,omitempty"`
	}
	var ubt unmarshalQueryBlockTx
	if err := json.Unmarshal(data, &ubt); err != nil {
		return err
	}
	b.BlockTx = ubt.BlockTx

	// unmarshal tx into cosmos type
	if ubt.Tx != nil {
		tx, err := encodingConfig.TxConfig.TxJSONDecoder()(ubt.Tx)
		if err != nil {
			return err
		}
		b.Tx = tx
	}

	return nil
}

// BlockResponse wraps the openapi type with a custom Txs field for unmarshaling.
type BlockResponse struct {
	openapi.BlockResponse
	Txs []BlockTx `json:"txs"`
}

// -------------------------------------------------------------------------------------
// Exported
// -------------------------------------------------------------------------------------

func Scan(startHeight, stopHeight int) <-chan *BlockResponse {
	// get current height if start was not provided
	if startHeight <= 0 || stopHeight < 0 {
		block, err := getBlock(-1)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get current height")
		}

		// set start height
		if startHeight <= 0 {
			startHeight = int(block.Header.Height) + startHeight
		}
		if stopHeight < 0 { // zero height means tail indefinitely
			stopHeight = int(block.Header.Height) + stopHeight
		}
	}

	// create queue for block heights to fetch
	queue := make(chan int64)
	go func() {
		for height := int64(startHeight); stopHeight == 0 || int(height) <= stopHeight; height++ {
			queue <- height
		}
	}()

	// setup ring buffer for block prefetching with routine per slot
	ring := make([]chan *BlockResponse, Parallelism)
	shutdown := make(chan struct{}, Parallelism-1)
	for i := 0; i < Parallelism; i++ {
		ring[i] = make(chan *BlockResponse)
		go func(i int) {
			for height := range queue {
				for {
					b, err := getBlock(height)
					if err != nil {
						if !strings.Contains(err.Error(), "block not found") {
							log.Error().Err(err).Int64("height", height).Msg("failed to fetch block")
						}
						time.Sleep(constants.ThorchainBlockTime)
						continue
					}
					ring[int(height)%Parallelism] <- b

					// allow all but one routine to exit once we near tip
					blockTime, err := time.Parse(time.RFC3339, b.Header.Time)
					if err != nil {
						log.Fatal().Err(err).Msg("failed to parse block time")
					}
					near := time.Now().Add(-constants.ThorchainBlockTime * time.Duration(Parallelism))
					if err == nil && blockTime.After(near) {
						select {
						case shutdown <- struct{}{}:
							log.Debug().Int64("height", height).Msg("shutting down extra worker")
							return
						default:
						}
					}

					break
				}
			}
		}(i)
	}

	// start sequential reader to send to blocks channel
	out := make(chan *BlockResponse, Parallelism)
	go func() {
		for height := int64(startHeight); stopHeight == 0 || int(height) <= stopHeight; height++ {
			out <- <-ring[int(height)%Parallelism]
		}
		close(out)
	}()

	return out
}
