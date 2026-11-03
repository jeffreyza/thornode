package thorclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	retryablehttp "github.com/hashicorp/go-retryablehttp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Endpoint urls
const (
	AuthAccountEndpoint         = "/cosmos/auth/v1beta1/accounts"
	BroadcastTxsEndpoint        = "/"
	ConstantsEndpoint           = "/thorchain/constants"
	KeygenEndpoint              = "/thorchain/keygen"
	KeysignEndpoint             = "/thorchain/keysign"
	LastBlockEndpoint           = "/thorchain/lastblock"
	NodeAccountEndpoint         = "/thorchain/node"
	NodeAccountsEndpoint        = "/thorchain/nodes"
	StatusEndpoint              = "/status"
	VaultEndpoint               = "/thorchain/vault/%s"
	KeysignPartyEndpoint        = "/thorchain/vault/%s/keysign_party"
	XMRSpentOutputRefsEndpoint  = "/thorchain/xmr/spent_refs/%s"
	AsgardVault                 = "/thorchain/vaults/asgard"
	PubKeysEndpoint             = "/thorchain/vaults/pubkeys"
	PendingOutboundEndpoint     = "/thorchain/queue/outbound"
	ThorchainConstants          = "/thorchain/constants"
	RagnarokEndpoint            = "/thorchain/ragnarok"
	MimirEndpoint               = "/thorchain/mimir"
	ChainVersionEndpoint        = "/thorchain/version"
	InboundAddressesEndpoint    = "/thorchain/inbound_addresses"
	PoolsEndpoint               = "/thorchain/pools"
	THORNameEndpoint            = "/thorchain/thorname/%s"
	ReferenceMemoEndpoint       = "/thorchain/memo/%s/%s"
	ReferenceMemoByHashEndpoint = "/thorchain/memo/%s"
	TxDetailEndpoint            = "/thorchain/tx/details/%s"
)

var errThorchainTransport = errors.New("thornode transport error")

// Retry policy for bridge HTTP requests to THORNode; the defaults are
// retryablehttp's. Exposed so tests that point a bridge at an unreachable
// node can keep the retries but skip the real waiting.
var (
	BridgeRetryMax     = 4
	BridgeRetryWaitMin = 1 * time.Second
	BridgeRetryWaitMax = 30 * time.Second
)

// thorchainBridge will be used to send tx to THORChain
type thorchainBridge struct {
	logger        zerolog.Logger
	cfg           config.BifrostClientConfiguration
	keys          *Keys
	errCounter    *prometheus.CounterVec
	m             *metrics.Metrics
	blockHeight   int64
	accountNumber uint64
	seqNumber     uint64
	httpClient    *retryablehttp.Client
	broadcastLock *sync.RWMutex
	activeNodes   activeNodesMemo
}

// KeysignPolicy is thornode's signer eligibility view for one vault at one
// THORChain height.
type KeysignPolicy struct {
	PubKey                common.PubKey  `json:"pub_key"`
	Signers               common.PubKeys `json:"signers"`
	NeverSigners          common.PubKeys `json:"never_signers"`
	JailedFallbackSigners common.PubKeys `json:"jailed_fallback_signers"`
	Height                int64          `json:"height,string"`
	Threshold             int            `json:"threshold,string"`
	RequiredSigners       int            `json:"required_signers,string"`
	ActiveSignerCount     int            `json:"active_signer_count,string"`
}

type ThorchainBridge interface {
	EnsureNodeWhitelisted() error
	EnsureNodeWhitelistedWithTimeout() error
	FetchNodeStatus() (stypes.NodeStatus, error)
	FetchActiveNodes() ([]common.PubKey, error)
	GetAsgards() (stypes.Vaults, error)
	GetVault(pubkey string) (stypes.Vault, error)
	GetXMRSpentOutputRefs(vaultPubKey common.PubKey) ([]*common.OutputRef, error)
	GetXMRSpentOutputRefsUncached(ctx context.Context, vaultPubKey common.PubKey) ([]*common.OutputRef, error)
	GetConfig() config.BifrostClientConfiguration
	GetConstants() (map[string]int64, error)
	GetContext() client.Context
	GetContractAddress() ([]PubKeyContractAddressPair, error)
	GetErrataMsg(txID common.TxID, chain common.Chain) sdk.Msg
	GetKeygenStdTx(poolPubKey common.PubKey, secp256k1Signature, keysharesBackup []byte, blame []stypes.Blame, inputPks common.PubKeys, keygenType stypes.KeygenType, chains common.Chains, height, keygenTime int64, poolPubKeyEddsa common.PubKey, keysharesBackupEddsa []byte, frostExtras ...any) (sdk.Msg, error)
	GetKeysignPolicy(vaultPubKey common.PubKey, blockHeight int64) (KeysignPolicy, error)
	GetKeysignParty(vaultPubKey common.PubKey, blockHeight int64) (common.PubKeys, error)
	GetMimir(key string) (int64, error)
	GetMimirWithRef(template, ref string) (int64, error)
	GetInboundOutbound(txIns common.ObservedTxs) (common.ObservedTxs, common.ObservedTxs, error)
	GetPools() (stypes.Pools, error)
	GetOraclePrice(symbol string) (cosmos.Uint, error)
	GetPubKeys() ([]PubKeyContractAddressPair, error)
	GetAsgardPubKeys() ([]PubKeyContractAddressPair, error)
	GetSolvencyMsg(height int64, chain common.Chain, pubKey common.PubKey, coins common.Coins) *stypes.MsgSolvency
	GetTHORName(name string) (stypes.THORName, error)
	GetThorchainVersion() (semver.Version, error)
	IsCatchingUp() (bool, error)
	HasNetworkFee(chain common.Chain) (bool, error)
	GetNetworkFee(chain common.Chain) (transactionSize, transactionFeeRate uint64, err error)
	PostKeysignFailure(blame stypes.Blame, height int64, memo string, coins common.Coins, pubkey common.PubKey) (common.TxID, error)
	PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error)
	RagnarokInProgress() (bool, error)
	WaitToCatchUp() error
	GetBlockHeight() (int64, error)
	GetBlockTimestamp(height int64) (time.Time, error)
	GetLastObservedInHeight(chain common.Chain) (int64, error)
	GetLastObservedInHeightAtHeight(chain common.Chain, thorchainHeight int64) (int64, error)
	GetLastSignedOutHeight(chain common.Chain) (int64, error)
	Broadcast(msgs ...sdk.Msg) (common.TxID, error)
	BroadcastWithBlocking(msgs ...sdk.Msg) (common.TxID, error)
	GetKeysign(blockHeight int64, pk string) (types.TxOut, error)
	GetNodeAccount(string) (*stypes.NodeAccount, error)
	GetNodeAccounts() ([]*stypes.QueryNodeResponse, error)
	GetKeygenBlock(int64, string) (stypes.KeygenBlock, error)
	GetReferenceMemo(chain common.Chain, ref string) (string, error)
	GetReferenceMemoByTxHash(hash string) (string, error)
	GetObservedTxVoter(txID string) (*stypes.QueryObservedTxVoter, error)
	GetObservedTxOutVoter(txID string) (*stypes.QueryObservedTxVoter, error)
	GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, bool, error)
}

// ContextQueryBridge contains the cancellation-aware Thornode reads required
// by attestation state import. It is separate from the broad
// bridge contract so unrelated clients and test doubles do not need methods
// used only by this security-sensitive path.
type ContextQueryBridge interface {
	GetVaultWithContext(ctx context.Context, pubkey string) (stypes.Vault, error)
	GetKeysignWithContext(ctx context.Context, blockHeight int64, pk string) (types.TxOut, error)
	GetPendingOutboundsWithContext(ctx context.Context, height int64) ([]PendingOutbound, error)
	GetObservedTxVoterWithContext(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, error)
}

var _ ContextQueryBridge = (*thorchainBridge)(nil)

// httpResponseCache used for caching HTTP responses for less frequent querying
type httpResponseCache struct {
	httpResponse        []byte
	httpResponseChecked time.Time
	httpResponseLock    chan struct{}
}

var (
	httpResponseCaches   = make(map[string]*httpResponseCache) // String-to-pointer map for quicker lookup
	httpResponseCachesMu = &sync.Mutex{}
)

// NewThorchainBridge create a new instance of ThorchainBridge
func NewThorchainBridge(cfg config.BifrostClientConfiguration, m *metrics.Metrics, k *Keys) (ThorchainBridge, error) {
	// main module logger
	logger := log.With().Str("module", "thorchain_client").Logger()

	if len(cfg.ChainID) == 0 {
		return nil, errors.New("chain id is empty")
	}
	if len(cfg.ChainHost) == 0 {
		return nil, errors.New("chain host is empty")
	}

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	httpClient.RetryMax = BridgeRetryMax
	httpClient.RetryWaitMin = BridgeRetryWaitMin
	httpClient.RetryWaitMax = BridgeRetryWaitMax

	return &thorchainBridge{
		logger:        logger,
		cfg:           cfg,
		keys:          k,
		errCounter:    m.GetCounterVec(metrics.ThorchainClientError),
		httpClient:    httpClient,
		m:             m,
		broadcastLock: &sync.RWMutex{},
	}, nil
}

func MakeCodec() codec.ProtoCodecMarshaler {
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	std.RegisterInterfaces(interfaceRegistry)
	stypes.RegisterInterfaces(interfaceRegistry)
	return codec.NewProtoCodec(interfaceRegistry)
}

// GetContext return a valid context with all relevant values set
func (b *thorchainBridge) GetContext() client.Context {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		panic(err)
	}
	ctx := client.Context{}
	ctx = ctx.WithKeyring(b.keys.GetKeybase())
	ctx = ctx.WithChainID(string(b.cfg.ChainID))
	ctx = ctx.WithHomeDir(b.cfg.ChainHomeFolder)
	ctx = ctx.WithFromName(b.cfg.SignerName)
	ctx = ctx.WithFromAddress(signerAddr)
	ctx = ctx.WithBroadcastMode("sync")

	encodingConfig := app.MakeEncodingConfig()
	ctx = ctx.WithCodec(encodingConfig.Codec)
	ctx = ctx.WithInterfaceRegistry(encodingConfig.InterfaceRegistry)
	ctx = ctx.WithTxConfig(encodingConfig.TxConfig)
	ctx = ctx.WithLegacyAmino(encodingConfig.Amino)
	ctx = ctx.WithAccountRetriever(authtypes.AccountRetriever{})

	remote := b.cfg.ChainRPC
	if !strings.HasPrefix(b.cfg.ChainHost, "http") {
		remote = fmt.Sprintf("tcp://%s", remote)
	}
	ctx = ctx.WithNodeURI(remote)
	client, err := rpchttp.New(remote, "/websocket")
	if err != nil {
		panic(err)
	}
	ctx = ctx.WithClient(client)
	return ctx
}

func (b *thorchainBridge) getWithPath(path string) ([]byte, int, error) {
	return b.get(b.getThorChainURL(path))
}

// getWithPathUncached performs a fresh request without consulting or updating
// the shared one-block response cache. It is reserved for queries whose answer
// is used to make an irreversible local security decision.
func (b *thorchainBridge) getWithPathUncached(path string) ([]byte, int, error) {
	return b.getUncached(b.getThorChainURL(path))
}

// getWithPathWithContext performs a cached retryable request whose cache wait,
// request, and retries are all bounded by ctx.
func (b *thorchainBridge) getWithPathWithContext(ctx context.Context, path string) ([]byte, int, error) {
	return b.getWithContext(ctx, b.getThorChainURL(path))
}

// get handle all the low level http GET calls using retryablehttp.ThorchainBridge
func (b *thorchainBridge) get(url string) ([]byte, int, error) {
	return b.getCached(context.Background(), url, func() ([]byte, int, error) {
		return b.getRetryable(url)
	})
}

func (b *thorchainBridge) getWithContext(ctx context.Context, url string) ([]byte, int, error) {
	return b.getCached(ctx, url, func() ([]byte, int, error) {
		return b.getRetryableWithContext(ctx, url)
	})
}

func (b *thorchainBridge) getCached(
	ctx context.Context,
	url string,
	get func() ([]byte, int, error),
) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	// To reduce querying time and chance of "429 Too Many Requests",
	// do not query the same endpoint more than once per block time.
	httpResponseCachesMu.Lock()
	respCachePointer := httpResponseCaches[url]
	if respCachePointer == nil {
		respCachePointer = &httpResponseCache{httpResponseLock: make(chan struct{}, 1)}
		httpResponseCaches[url] = respCachePointer
	}
	httpResponseCachesMu.Unlock()

	// So lengthy queries don't hold up unrelated queries, serialize only callers
	// for this URL. Context-aware callers can stop waiting with their transfer.
	if err := respCachePointer.lock(ctx); err != nil {
		return nil, 0, err
	}
	defer respCachePointer.unlock()

	// When the same endpoint has been checked within the span of a single block, return the cached response.
	if time.Since(respCachePointer.httpResponseChecked) < constants.ThorchainBlockTime && respCachePointer.httpResponse != nil {
		return respCachePointer.httpResponse, http.StatusOK, nil
	}

	buf, status, err := get()
	if err != nil {
		return buf, status, err
	}
	if err := ctx.Err(); err != nil {
		// A request which races its deadline must not populate the shared cache.
		return buf, status, err
	}

	// All being well with the response, save it to the cache.
	respCachePointer.httpResponse = buf
	respCachePointer.httpResponseChecked = time.Now()

	return buf, status, nil
}

func (c *httpResponseCache) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.httpResponseLock <- struct{}{}:
		// If cancellation raced acquisition, prefer cancellation and release the
		// lock before returning.
		if err := ctx.Err(); err != nil {
			<-c.httpResponseLock
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *httpResponseCache) unlock() {
	<-c.httpResponseLock
}

func (b *thorchainBridge) getRetryable(url string) ([]byte, int, error) {
	resp, err := b.httpClient.Get(url)
	return b.readThorchainResponse(context.Background(), resp, err)
}

func (b *thorchainBridge) getRetryableWithContext(ctx context.Context, url string) ([]byte, int, error) {
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create thornode request: %w", err)
	}
	resp, err := b.httpClient.Do(req)
	return b.readThorchainResponse(ctx, resp, err)
}

// getUncached handles one low-level HTTP GET without consulting or updating
// the shared response cache. Outcome reconciliation owns durable retries, so a
// single raw request is preferable here: it preserves the body of Cosmos query
// errors, which is needed to distinguish an absent voter from a server outage.
func (b *thorchainBridge) getUncached(url string) ([]byte, int, error) {
	return b.getUncachedContext(context.Background(), url)
}

func (b *thorchainBridge) getUncachedContext(ctx context.Context, url string) ([]byte, int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, constants.ThorchainBlockTime)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create uncached thornode request: %w", err)
	}
	resp, err := b.httpClient.HTTPClient.Do(req)
	return b.readThorchainResponse(ctx, resp, err)
}

// A caller abandoning its request is not a THORNode failure. Match the error as
// well as the caller context so an unrelated failure is still counted. The HTTP
// client's timeout and the uncached query's own deadline also remain failures.
func isThorchainCallerCancellation(ctx context.Context, err error) bool {
	callerErr := ctx.Err()
	return callerErr != nil && errors.Is(err, callerErr)
}

func (b *thorchainBridge) readThorchainResponse(ctx context.Context, resp *http.Response, err error) ([]byte, int, error) {
	if err != nil {
		if !isThorchainCallerCancellation(ctx, err) {
			b.errCounter.WithLabelValues("fail_get_from_thorchain", "").Inc()
		}
		return nil, 0, fmt.Errorf("%w: failed to GET from thorchain: %w", errThorchainTransport, err)
	}
	if resp == nil {
		return nil, 0, fmt.Errorf("%w: empty response from thornode", errThorchainTransport)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			b.logger.Error().Err(closeErr).Msg("failed to close response body")
		}
	}()

	var buf []byte
	buf, err = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return buf, resp.StatusCode, errors.New("Status code: " + resp.Status + " returned")
	}
	if err != nil {
		if !isThorchainCallerCancellation(ctx, err) {
			b.errCounter.WithLabelValues("fail_read_thorchain_resp", "").Inc()
		}
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	return buf, resp.StatusCode, nil
}

// getThorChainURL with the given path
func (b *thorchainBridge) getThorChainURL(path string) string {
	if strings.HasPrefix(b.cfg.ChainHost, "http") {
		return fmt.Sprintf("%s/%s", b.cfg.ChainHost, path)
	}

	uri := url.URL{
		Scheme: "http",
		Host:   b.cfg.ChainHost,
		Path:   path,
	}
	return uri.String()
}

// getAccountNumberAndSequenceNumber returns account and Sequence number required to post into thorchain
func (b *thorchainBridge) getAccountNumberAndSequenceNumber() (uint64, uint64, error) {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get signer address: %w", err)
	}
	path := fmt.Sprintf("%s/%s", AuthAccountEndpoint, signerAddr)

	body, _, err := b.getWithPath(path)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get auth accounts: %w", err)
	}

	var resp types.AccountResp
	if err = json.Unmarshal(body, &resp); err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal account resp: %w", err)
	}
	acc := resp.Account

	return acc.AccountNumber, acc.Sequence, nil
}

// GetConfig return the configuration
func (b *thorchainBridge) GetConfig() config.BifrostClientConfiguration {
	return b.cfg
}

// PostKeysignFailure generate and  post a keysign fail tx to thorchan
func (b *thorchainBridge) PostKeysignFailure(blame stypes.Blame, height int64, memo string, coins common.Coins, pubkey common.PubKey) (common.TxID, error) {
	start := time.Now()
	defer func() {
		b.m.GetHistograms(metrics.SignToThorchainDuration).Observe(time.Since(start).Seconds())
	}()

	if blame.IsEmpty() {
		// MsgTssKeysignFail will fail validation if having no FailReason.
		blame.FailReason = "no fail reason available"
	}
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return common.BlankTxID, fmt.Errorf("failed to get signer address: %w", err)
	}
	msg, err := stypes.NewMsgTssKeysignFail(height, blame, memo, coins, signerAddr, pubkey)
	if err != nil {
		return common.BlankTxID, fmt.Errorf("fail to create keysign fail message: %w", err)
	}
	return b.Broadcast(msg)
}

// PostSolNonceAttest broadcasts this validator's re-attestation of a Solana vault's
// durable-nonce account (see MsgSolNonceAttest).
func (b *thorchainBridge) PostSolNonceAttest(vaultPubKey common.PubKey, nonceAccount, nonce string, slot int64, lastTx common.TxID) (common.TxID, error) {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return common.BlankTxID, fmt.Errorf("failed to get signer address: %w", err)
	}
	msg := stypes.NewMsgSolNonceAttest(vaultPubKey, nonceAccount, nonce, slot, lastTx, signerAddr)
	if err := msg.ValidateBasic(); err != nil {
		return common.BlankTxID, fmt.Errorf("invalid sol nonce attestation: %w", err)
	}
	return b.Broadcast(msg)
}

// GetErrataMsg get errata tx from params
func (b *thorchainBridge) GetErrataMsg(txID common.TxID, chain common.Chain) sdk.Msg {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		panic(err)
	}
	return stypes.NewMsgErrataTx(txID, chain, signerAddr)
}

// GetSolvencyMsg create MsgSolvency from the given parameters
func (b *thorchainBridge) GetSolvencyMsg(height int64, chain common.Chain, pubKey common.PubKey, coins common.Coins) *stypes.MsgSolvency {
	// To prevent different MsgSolvency ID incompatibility between nodes with different coin-observation histories,
	// only report coins for which the amounts are not currently 0.
	coins = coins.NoneEmpty()
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		panic(err)
	}
	msg, err := stypes.NewMsgSolvency(chain, pubKey, coins, height, signerAddr)
	if err != nil {
		b.logger.Err(err).Msg("fail to create MsgSolvency")
		return nil
	}
	return msg
}

// GetKeygenStdTx get keygen tx from params
func (b *thorchainBridge) GetKeygenStdTx(poolPubKey common.PubKey, secp256k1Signature, keysharesBackup []byte, blame []stypes.Blame, inputPks common.PubKeys, keygenType stypes.KeygenType, chains common.Chains, height, keygenTime int64, poolPubKeyEddsa common.PubKey, keysharesBackupEddsa []byte, frostExtras ...any) (sdk.Msg, error) {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return nil, fmt.Errorf("failed to get signer address: %w", err)
	}

	var keysharesBackupFrost []byte
	poolPubKeyFrost := common.EmptyPubKey
	var observedMoneroHeight int64
	switch len(frostExtras) {
	case 0:
	case 3:
		var ok bool
		keysharesBackupFrost, ok = frostExtras[0].([]byte)
		if !ok && frostExtras[0] != nil {
			return nil, fmt.Errorf("invalid frost keyshares backup type %T", frostExtras[0])
		}
		poolPubKeyFrost, ok = frostExtras[1].(common.PubKey)
		if !ok {
			return nil, fmt.Errorf("invalid frost pubkey type %T", frostExtras[1])
		}
		switch height := frostExtras[2].(type) {
		case int64:
			observedMoneroHeight = height
		case int:
			observedMoneroHeight = int64(height)
		default:
			return nil, fmt.Errorf("invalid observed monero height type %T", frostExtras[2])
		}
	default:
		return nil, fmt.Errorf("expected 0 or 3 frost extras, got %d", len(frostExtras))
	}

	return stypes.NewMsgTssPoolV3(inputPks.Strings(), poolPubKey, secp256k1Signature, keysharesBackup, keygenType, height, blame, chains.Strings(), signerAddr, keygenTime, poolPubKeyEddsa, keysharesBackupEddsa, keysharesBackupFrost, poolPubKeyFrost, observedMoneroHeight)
}

// GetInboundOutbound separate the txs into inbound and outbound
func (b *thorchainBridge) GetInboundOutbound(txIns common.ObservedTxs) (common.ObservedTxs, common.ObservedTxs, error) {
	if len(txIns) == 0 {
		return nil, nil, nil
	}
	inbound := common.ObservedTxs{}
	outbound := common.ObservedTxs{}

	// spilt our txs into inbound vs outbound txs
	for _, tx := range txIns {
		chain := common.EmptyChain
		if len(tx.Tx.Coins) > 0 {
			chain = tx.Tx.Coins[0].Asset.Chain
		}

		obAddr, err := tx.ObservedPubKey.GetAddress(chain)
		if err != nil {
			if chain.Equals(common.XMRChain) {
				return nil, nil, fmt.Errorf("xmr observed vault pubkey %s is invalid: %w", tx.ObservedPubKey.String(), err)
			}
			b.logger.Err(err).Msgf("fail to parse observed pool address: %s", tx.ObservedPubKey.String())
			continue
		}

		if chain.Equals(common.XMRChain) && obAddr.IsEmpty() {
			return nil, nil, fmt.Errorf("xmr observed vault pubkey %s does not map to an address", tx.ObservedPubKey.String())
		}

		vaultToAddress := tx.Tx.ToAddress.Equals(obAddr)
		vaultFromAddress := tx.Tx.FromAddress.Equals(obAddr)
		var inInboundArray, inOutboundArray bool
		if vaultToAddress {
			inInboundArray = inbound.Contains(tx)
		}
		if vaultFromAddress {
			inOutboundArray = outbound.Contains(tx)
		}

		// cancels have to/from the same vault with no memo
		isCancelTransaction := tx.Tx.ToAddress.Equals(tx.Tx.FromAddress) && tx.Tx.Memo == ""

		// for consolidate UTXO tx, both From & To address will be the asgard address
		// thus here we need to make sure that one add to inbound , the other add to outbound
		switch {
		case !vaultToAddress && !vaultFromAddress:
			// Neither ToAddress nor FromAddress matches obAddr, so drop it.
			b.logger.Error().Msgf("chain (%s) tx (%s) observedaddress (%s) does not match its toaddress (%s) or fromaddress (%s)", tx.Tx.Chain, tx.Tx.ID, obAddr, tx.Tx.ToAddress, tx.Tx.FromAddress)
		case vaultToAddress && !inInboundArray && !isCancelTransaction:
			inbound = append(inbound, tx)
		case vaultFromAddress && !inOutboundArray:
			outbound = append(outbound, tx)
		case inInboundArray && inOutboundArray:
			// It's already in both arrays, so drop it.
			b.logger.Error().Msgf("vault-to-vault chain (%s) tx (%s) is already in both inbound and outbound arrays", tx.Tx.Chain, tx.Tx.ID)
		case !vaultFromAddress && inInboundArray:
			// It's already in its only (inbound) array, so drop it.
			b.logger.Error().Msgf("observed tx in for chain (%s) tx (%s) is already in the inbound array", tx.Tx.Chain, tx.Tx.ID)
		case !vaultToAddress && inOutboundArray:
			// It's already in its only (outbound) array, so drop it.
			b.logger.Error().Msgf("observed tx out for chain (%s) tx (%s) is already in the outbound array", tx.Tx.Chain, tx.Tx.ID)
		default:
			// This should never happen; rather than dropping it, return an error.
			return nil, nil, fmt.Errorf("could not determine if chain (%s) tx (%s) was inbound or outbound", tx.Tx.Chain, tx.Tx.ID)
		}
	}

	return inbound, outbound, nil
}

// EnsureNodeWhitelistedWithTimeout loops until the node is whitelisted (any
// status other than Unknown). Unknown means no node account exists for this
// signer address: never bonded, fully unbonded, or the signer key does not
// match the bonded address. Any non-zero bond creates the account as
// Whitelisted; a below-minimum bond only prevents churning in (Standby),
// and both pass this check. Bonding happens on a human timescale, so retry
// indefinitely. (The prior 1-hour timeout was never enforced: time.After
// was re-evaluated each iteration, so the wait was already indefinite. The
// behavioral change here is the retry interval, 5s to 30s.)
func (b *thorchainBridge) EnsureNodeWhitelistedWithTimeout() error {
	for {
		err := b.EnsureNodeWhitelisted()
		if err == nil {
			return nil
		}
		b.logger.Error().Err(err).Msg("observer is not whitelisted, will retry in 30s")
		time.Sleep(time.Second * 30)
	}
}

// EnsureNodeWhitelisted will call to thorchain to check whether the observer had been whitelist or not
func (b *thorchainBridge) EnsureNodeWhitelisted() error {
	status, err := b.FetchNodeStatus()
	if err != nil {
		return fmt.Errorf("failed to get node status: %w", err)
	}
	if status == stypes.NodeStatus_Unknown {
		return fmt.Errorf("node account status %s , will not be able to forward transaction to thorchain", status)
	}
	return nil
}

func (b *thorchainBridge) FetchActiveNodes() ([]common.PubKey, error) {
	body, err := b.getNodeAccountsBody()
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}
	// sendDeck calls this every second, but the body only changes once per block
	if active, ok := b.activeNodes.get(body); ok {
		return active, nil
	}
	na, err := decodeNodeAccounts(body)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}
	active := make([]common.PubKey, 0)
	for _, item := range na {
		// QueryNodeResponse.Status is a string, compare against the string representation
		if item.Status == stypes.NodeStatus_Active.String() {
			active = append(active, item.PubKeySet.Secp256k1)
		}
	}
	b.activeNodes.set(body, active)
	return active, nil
}

// FetchNodeStatus get current node status from thorchain
func (b *thorchainBridge) FetchNodeStatus() (stypes.NodeStatus, error) {
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return stypes.NodeStatus_Unknown, fmt.Errorf("fail to get signer address: %w", err)
	}
	bepAddr := signerAddr.String()
	if len(bepAddr) == 0 {
		return stypes.NodeStatus_Unknown, errors.New("bep address is empty")
	}
	na, err := b.GetNodeAccount(bepAddr)
	if err != nil {
		return stypes.NodeStatus_Unknown, fmt.Errorf("failed to get node status: %w", err)
	}
	return na.Status, nil
}

// GetKeysignPolicy reads the party from committed state at the item's signing
// height H. signing_height alone only changes jail evaluation, not the store.
// Current safety controls are read first, together at a fresh committed T >= H.
// Errors remain local and retryable: only the explicit override may widen the
// party to full vault membership, including when H is no longer available.
func (b *thorchainBridge) GetKeysignPolicy(vaultPubKey common.PubKey, blockHeight int64) (KeysignPolicy, error) {
	if blockHeight <= 0 {
		return KeysignPolicy{}, fmt.Errorf("key sign policy requires a positive signing height: %d", blockHeight)
	}
	currentHeight, err := b.GetBlockHeightUncached()
	if err != nil {
		return KeysignPolicy{}, fmt.Errorf("fail to get current key sign control height: %w", err)
	}
	if currentHeight < blockHeight {
		return KeysignPolicy{}, fmt.Errorf("key sign control height %d is behind signing height %d", currentHeight, blockHeight)
	}

	// Resolve alternate EdDSA/FROST pubkeys to the canonical vault key before
	// looking up its compromised flag. Membership is fixed at keygen, including
	// for retiring vaults which still have recovery work.
	var vault stypes.Vault
	if err = b.getKeysignStateAtHeight(fmt.Sprintf(VaultEndpoint, vaultPubKey), currentHeight, &vault); err != nil {
		return KeysignPolicy{}, err
	}
	members := vault.GetMembership()
	if vault.PubKey.IsEmpty() || len(members) == 0 {
		return KeysignPolicy{}, fmt.Errorf("vault %s has no canonical pubkey or signer membership at height %d", vaultPubKey, currentHeight)
	}
	compromised, err := b.getKeysignMimirAtHeight("CompromisedVault-"+vault.PubKey.String(), currentHeight)
	if err != nil {
		return KeysignPolicy{}, err
	}
	if compromised > 0 {
		return KeysignPolicy{}, fmt.Errorf("vault %s is %w at height %d", vault.PubKey, common.ErrCompromisedVault, currentHeight)
	}
	disableAt, err := b.getKeysignMimirAtHeight(constants.MimirKeyDisableTSSSignerFiltering, currentHeight)
	if err != nil {
		return KeysignPolicy{}, err
	}
	threshold, err := conversion.GetThreshold(len(members))
	if err != nil {
		return KeysignPolicy{}, fmt.Errorf("fail to calculate key sign threshold: %w", err)
	}
	// Match the existing query's activation rule, evaluated at signing height.
	if disableAt > 0 && disableAt <= blockHeight {
		return KeysignPolicy{
			PubKey: vault.PubKey, Signers: members, Height: blockHeight,
			Threshold: threshold, RequiredSigners: threshold + 1, ActiveSignerCount: len(members),
		}, nil
	}

	path := fmt.Sprintf(KeysignPartyEndpoint, vaultPubKey) + "?signing_height=" + strconv.FormatInt(blockHeight, 10)
	var policy KeysignPolicy
	if err := b.getKeysignStateAtHeight(path, blockHeight, &policy); err != nil {
		// Missing history and endpoint absence must not silently choose a
		// different party. Historical compromised-vault errors also stay fatal.
		return KeysignPolicy{}, err
	}
	if policy.Height != blockHeight || !policy.PubKey.Equals(vault.PubKey) {
		return KeysignPolicy{}, fmt.Errorf("key sign policy snapshot does not match vault %s at height %d", vault.PubKey, blockHeight)
	}
	if policy.Threshold != threshold || policy.RequiredSigners != threshold+1 || len(policy.Signers) < threshold+1 {
		return KeysignPolicy{}, fmt.Errorf("key sign policy for vault %s at height %d does not satisfy the original vault threshold", vault.PubKey, blockHeight)
	}
	allowed := make(map[common.PubKey]bool, len(members))
	for _, member := range members {
		allowed[member] = true
	}
	for _, signer := range policy.Signers {
		if !allowed[signer] {
			return KeysignPolicy{}, fmt.Errorf("key sign policy for vault %s at height %d contains a duplicate or non-member signer %s", vault.PubKey, blockHeight, signer)
		}
		allowed[signer] = false
	}
	return policy, nil
}

// getKeysignStateAtHeight bypasses the shared one-block cache. Control reads
// must observe the fresh visibility barrier, and height-specific URLs must not
// accumulate forever in that cache. The gateway maps height to the SDK store.
func (b *thorchainBridge) getKeysignStateAtHeight(path string, height int64, result any) error {
	return b.getKeysignStateAtHeightWithContext(context.Background(), path, height, result)
}

func (b *thorchainBridge) getKeysignStateAtHeightWithContext(ctx context.Context, path string, height int64, result any) error {
	u, err := url.Parse(path)
	if err != nil {
		return err
	}
	query := u.Query()
	query.Set("height", strconv.FormatInt(height, 10))
	buf, status, err := b.getUncachedContext(ctx, b.getThorChainURL(u.Path)+"?"+query.Encode())
	if err != nil {
		body := responseBodySnippet(buf)
		if IsCompromisedVaultError(err) || IsCompromisedVaultError(errors.New(body)) {
			return fmt.Errorf("fail to get key sign state from %s at height %d: status %d: %w: %s", path, height, status, common.ErrCompromisedVault, body)
		}
		return fmt.Errorf("fail to get key sign state from %s at height %d: status %d: %w: %s", path, height, status, err, body)
	}
	if err := json.Unmarshal(buf, result); err != nil {
		return fmt.Errorf("fail to decode key sign state from %s at height %d: %w", path, height, err)
	}
	return nil
}

func (b *thorchainBridge) getKeysignMimirAtHeight(key string, height int64) (int64, error) {
	// A missing Mimir is encoded as -1. null is not evidence that a safety
	// control is disabled, so reject it along with other malformed responses.
	var value *int64
	if err := b.getKeysignStateAtHeight(MimirEndpoint+"/key/"+key, height, &value); err != nil {
		return 0, err
	}
	if value == nil {
		return 0, fmt.Errorf("missing key sign control %s at height %d", key, height)
	}
	return *value, nil
}

// GetKeysignParty keeps the legacy bridge helper for callers that still need
// the compatibility signers field.
func (b *thorchainBridge) GetKeysignParty(vaultPubKey common.PubKey, blockHeight int64) (common.PubKeys, error) {
	policy, err := b.GetKeysignPolicy(vaultPubKey, blockHeight)
	if err != nil {
		return nil, err
	}
	return policy.Signers, nil
}

// IsCompromisedVaultError reports whether an error represents the vault-level
// hard stop returned by thornode for keysign policy queries.
func IsCompromisedVaultError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, common.ErrCompromisedVault) || strings.Contains(err.Error(), common.CompromisedVaultErrorText)
}

// responseBodySnippet keeps non-200 response bodies useful without logging an
// unbounded HTTP payload.
func responseBodySnippet(buf []byte) string {
	const maxResponseBodySnippet = 256
	body := strings.TrimSpace(string(buf))
	if len(body) > maxResponseBodySnippet {
		return body[:maxResponseBodySnippet] + "..."
	}
	return body
}

// IsCatchingUp returns bool for if thorchain is catching up to the rest of the
// nodes. Returns yes, if it is, false if it is caught up.
func (b *thorchainBridge) IsCatchingUp() (bool, error) {
	uri := url.URL{
		Scheme: "http",
		Host:   b.cfg.ChainRPC,
		Path:   StatusEndpoint,
	}

	body, _, err := b.get(uri.String())
	if err != nil {
		return false, fmt.Errorf("failed to get status data: %w", err)
	}

	var resp struct {
		Result struct {
			SyncInfo struct {
				CatchingUp bool `json:"catching_up"`
			} `json:"sync_info"`
		} `json:"result"`
	}

	if err = json.Unmarshal(body, &resp); err != nil {
		return false, fmt.Errorf("failed to unmarshal tendermint status: %w", err)
	}
	return resp.Result.SyncInfo.CatchingUp, nil
}

// HasNetworkFee checks whether the given chain has set a network fee - determined by
// whether the `outbound_tx_size` for the inbound address response is non-zero.
func (b *thorchainBridge) HasNetworkFee(chain common.Chain) (bool, error) {
	buf, s, err := b.getWithPath(InboundAddressesEndpoint)
	if err != nil {
		return false, fmt.Errorf("fail to get inbound addresses: %w", err)
	}
	if s != http.StatusOK {
		return false, fmt.Errorf("unexpected status code: %d", s)
	}

	var resp []openapi.InboundAddress
	if err = json.Unmarshal(buf, &resp); err != nil {
		return false, fmt.Errorf("fail to unmarshal inbound addresses: %w", err)
	}

	for _, addr := range resp {
		if addr.Chain != nil && *addr.Chain == chain.String() && addr.OutboundTxSize != nil {
			var size int64
			size, err = strconv.ParseInt(*addr.OutboundTxSize, 10, 64)
			if err != nil {
				return false, fmt.Errorf("fail to parse outbound_tx_size: %w", err)
			}
			return size > 0, nil
		}
	}

	return false, fmt.Errorf("no inbound address found for chain: %s", chain)
}

// GetNetworkFee get chain's network fee from THORNode.
func (b *thorchainBridge) GetNetworkFee(chain common.Chain) (transactionSize, transactionFeeRate uint64, err error) {
	buf, s, err := b.getWithPath(InboundAddressesEndpoint)
	if err != nil {
		return 0, 0, fmt.Errorf("fail to get inbound addresses: %w", err)
	}
	if s != http.StatusOK {
		return 0, 0, fmt.Errorf("unexpected status code: %d", s)
	}
	var resp []openapi.InboundAddress
	if err = json.Unmarshal(buf, &resp); err != nil {
		return 0, 0, fmt.Errorf("fail to unmarshal to json: %w", err)
	}

	for _, addr := range resp {
		if addr.Chain != nil && *addr.Chain == chain.String() {
			// Default values if nil or unfound are 0.
			if addr.OutboundTxSize != nil {
				transactionSize, err = strconv.ParseUint(*addr.OutboundTxSize, 10, 64)
				if err != nil {
					return 0, 0, fmt.Errorf("fail to parse outbound_tx_size: %w", err)
				}
			}
			if addr.ObservedFeeRate != nil {
				transactionFeeRate, err = strconv.ParseUint(*addr.ObservedFeeRate, 10, 64)
				if err != nil {
					return 0, 0, fmt.Errorf("fail to parse observed_fee_rate: %w", err)
				}
			}
			// Having found the chain, do not continue through the remaining chains.
			break
		}
	}
	return
}

// WaitToCatchUp wait for thorchain to catch up
func (b *thorchainBridge) WaitToCatchUp() error {
	for {
		yes, err := b.IsCatchingUp()
		if err != nil {
			return err
		}
		if !yes {
			break
		}
		b.logger.Info().Msg("thorchain is not caught up... waiting...")
		time.Sleep(constants.ThorchainBlockTime)
	}
	return nil
}

// GetAsgards retrieve all the asgard vaults from thorchain
func (b *thorchainBridge) GetAsgards() (stypes.Vaults, error) {
	buf, s, err := b.getWithPath(AsgardVault)
	if err != nil {
		return nil, fmt.Errorf("fail to get asgard vaults: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", s)
	}
	var vaults stypes.Vaults
	if err = json.Unmarshal(buf, &vaults); err != nil {
		return nil, fmt.Errorf("fail to unmarshal asgard vaults from json: %w", err)
	}
	return vaults, nil
}

// GetVault retrieves a specific vault from thorchain.
func (b *thorchainBridge) GetVault(pubkey string) (stypes.Vault, error) {
	return b.getVault(pubkey, b.getWithPath)
}

// GetVaultWithContext retrieves a vault without allowing the request or its
// retries to outlive ctx.
func (b *thorchainBridge) GetVaultWithContext(ctx context.Context, pubkey string) (stypes.Vault, error) {
	return b.getVault(pubkey, func(path string) ([]byte, int, error) {
		return b.getWithPathWithContext(ctx, path)
	})
}

func (b *thorchainBridge) getVault(
	pubkey string,
	get func(string) ([]byte, int, error),
) (stypes.Vault, error) {
	buf, s, err := get(fmt.Sprintf(VaultEndpoint, pubkey))
	if err != nil {
		return stypes.Vault{}, fmt.Errorf("fail to get vault: %w", err)
	}
	if s != http.StatusOK {
		return stypes.Vault{}, fmt.Errorf("unexpected status code %d", s)
	}
	var vault stypes.Vault
	if err = json.Unmarshal(buf, &vault); err != nil {
		return stypes.Vault{}, fmt.Errorf("fail to unmarshal vault from json: %w", err)
	}
	return vault, nil
}

func (b *thorchainBridge) GetXMRSpentOutputRefs(vaultPubKey common.PubKey) ([]*common.OutputRef, error) {
	return parseXMRSpentOutputRefsResponse(b.getWithPath(fmt.Sprintf(XMRSpentOutputRefsEndpoint, vaultPubKey.String())))
}

// GetXMRSpentOutputRefsUncached bounds both the HTTP request and response body
// read by the caller's context and one block. It neither waits on nor updates
// the shared cache, so a stalled cached query cannot block committed recovery.
func (b *thorchainBridge) GetXMRSpentOutputRefsUncached(ctx context.Context, vaultPubKey common.PubKey) ([]*common.OutputRef, error) {
	endpoint := b.getThorChainURL(fmt.Sprintf(XMRSpentOutputRefsEndpoint, vaultPubKey.String()))
	return parseXMRSpentOutputRefsResponse(b.getUncachedContext(ctx, endpoint))
}

func parseXMRSpentOutputRefsResponse(buf []byte, status int, err error) ([]*common.OutputRef, error) {
	if err != nil {
		return nil, fmt.Errorf("fail to get xmr spent output refs: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", status)
	}
	var resp struct {
		SpentOutputRefs []*common.OutputRef `json:"spent_output_refs"`
	}
	if err = json.Unmarshal(buf, &resp); err != nil {
		return nil, fmt.Errorf("fail to unmarshal xmr spent output refs from json: %w", err)
	}
	return resp.SpentOutputRefs, nil
}

func (b *thorchainBridge) GetObservedTxVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	return b.getObservedTxVoter(txID, b.getThorChainURL(fmt.Sprintf(TxDetailEndpoint, txID)), b.get)
}

// GetObservedTxOutVoter selects source-side consensus even when a migration's
// destination has an inbound voter with the same ID. Its cache key is distinct.
func (b *thorchainBridge) GetObservedTxOutVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	// Append the query after URL construction: a host without a scheme uses
	// url.URL.Path, which would otherwise escape '?' as part of the path.
	return b.getObservedTxVoter(txID, b.getThorChainURL(fmt.Sprintf(TxDetailEndpoint, txID))+"?outbound=true", b.get)
}

// GetObservedTxVoterWithContext retrieves a voter without allowing the request
// or its retries to outlive ctx.
func (b *thorchainBridge) GetObservedTxVoterWithContext(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, error) {
	return b.getObservedTxVoter(txID, b.getThorChainURL(fmt.Sprintf(TxDetailEndpoint, txID)), func(url string) ([]byte, int, error) {
		return b.getWithContext(ctx, url)
	})
}

func (b *thorchainBridge) getObservedTxVoter(
	txID, endpoint string,
	get func(string) ([]byte, int, error),
) (*stypes.QueryObservedTxVoter, error) {
	buf, s, err := get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("fail to get observed tx voter: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d for tx %s", s, txID)
	}
	var voter stypes.QueryObservedTxVoter
	err = json.Unmarshal(buf, &voter)
	if err != nil {
		return nil, fmt.Errorf("fail to unmarshal observed tx voter: %w", err)
	}
	return &voter, nil
}

// GetObservedTxVoterUncached returns the current on-chain voter state from a
// fresh HTTP response. A missing voter is not an endpoint failure and is
// reported as found=false; transport and server failures remain errors.
func (b *thorchainBridge) GetObservedTxVoterUncached(txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	return b.getObservedTxVoterUncached(context.Background(), txID, b.getThorChainURL(fmt.Sprintf(TxDetailEndpoint, txID)))
}

// GetObservedTxOutVoterUncached preserves the commit-race lookup's freshness
// without allowing an inbound voter to hide a missing or pending outbound.
// The caller's context bounds both the request and response body read.
func (b *thorchainBridge) GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	return b.getObservedTxVoterUncached(ctx, txID, b.getThorChainURL(fmt.Sprintf(TxDetailEndpoint, txID))+"?outbound=true")
}

func (b *thorchainBridge) getObservedTxVoterUncached(ctx context.Context, txID, endpoint string) (*stypes.QueryObservedTxVoter, bool, error) {
	buf, status, err := b.getUncachedContext(ctx, endpoint)
	if missingObservedTxVoterResponse(buf, status, txID) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("fail to get observed tx voter: %w", err)
	}
	if status != http.StatusOK {
		return nil, false, fmt.Errorf("unexpected status code %d for tx %s", status, txID)
	}

	var voter stypes.QueryObservedTxVoter
	if err = json.Unmarshal(buf, &voter); err != nil {
		return nil, false, fmt.Errorf("fail to unmarshal observed tx voter: %w", err)
	}
	return &voter, true, nil
}

func missingObservedTxVoterResponse(body []byte, status int, txID string) bool {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError+100 {
		return false
	}
	// The tx-details query currently surfaces a missing voter as a Cosmos query
	// error rather than consistently mapping it to HTTP 404. Match only the
	// endpoint's exact transaction-specific error so genuine 4xx/5xx failures
	// remain retryable and cannot be mistaken for an authoritative absence.
	want := fmt.Sprintf("tx: %s doesn't exist", strings.ToLower(strings.TrimSpace(txID)))
	return strings.Contains(strings.ToLower(string(body)), want)
}

func (b *thorchainBridge) getVaultPubkeys() ([]byte, error) {
	buf, s, err := b.getWithPath(PubKeysEndpoint)
	if err != nil {
		return nil, fmt.Errorf("fail to get asgard vaults: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", s)
	}
	return buf, nil
}

// GetPubKeys retrieve vault pub keys and their relevant smart contracts
// Returns both active and inactive vaults
func (b *thorchainBridge) GetPubKeys() ([]PubKeyContractAddressPair, error) {
	buf, err := b.getVaultPubkeys()
	if err != nil {
		return nil, fmt.Errorf("fail to get vault pubkeys ,err: %w", err)
	}
	var result openapi.VaultPubkeysResponse
	if err = json.Unmarshal(buf, &result); err != nil {
		return nil, fmt.Errorf("fail to unmarshal pubkeys: %w", err)
	}
	var addressPairs []PubKeyContractAddressPair

	// anonymous function to process a vault and add its pubkeys to addressPairs
	processVault := func(v openapi.VaultInfo, inactive bool) {
		membership := []common.PubKey{}
		for _, item := range v.Membership {
			membership = append(membership, common.PubKey(item))
		}

		// process eddsa pubkey if present
		if v.PubKeyEddsa != nil && *v.PubKeyEddsa != "" {
			kp := PubKeyContractAddressPair{
				PubKey:     common.PubKey(*v.PubKeyEddsa),
				Contracts:  make(map[common.Chain]common.Address),
				Algo:       common.SigningAlgoEd25519,
				Membership: membership,
				Inactive:   inactive,
			}
			for _, item := range v.Routers {
				kp.Contracts[common.Chain(*item.Chain)] = common.Address(*item.Router)
			}
			addressPairs = append(addressPairs, kp)
		}

		// process FROST pubkey if present
		if v.PubKeyFrost != nil && *v.PubKeyFrost != "" {
			kp := PubKeyContractAddressPair{
				PubKey:     common.PubKey(*v.PubKeyFrost),
				Contracts:  make(map[common.Chain]common.Address),
				Algo:       common.SigningAlgoEd25519,
				Membership: membership,
				Inactive:   inactive,
			}
			for _, item := range v.Routers {
				kp.Contracts[common.Chain(*item.Chain)] = common.Address(*item.Router)
			}
			addressPairs = append(addressPairs, kp)
		}

		// process secp256k1 pubkey
		kp := PubKeyContractAddressPair{
			PubKey:     common.PubKey(v.PubKey),
			Contracts:  make(map[common.Chain]common.Address),
			Algo:       common.SigningAlgoSecp256k1,
			Membership: membership,
			Inactive:   inactive,
		}
		for _, item := range v.Routers {
			kp.Contracts[common.Chain(*item.Chain)] = common.Address(*item.Router)
		}
		addressPairs = append(addressPairs, kp)
	}

	// process active vaults
	for _, v := range result.Asgard {
		processVault(v, false)
	}

	// process inactive vaults
	for _, v := range result.Inactive {
		processVault(v, true)
	}

	return addressPairs, nil
}

// GetAsgardPubKeys retrieve asgard vaults, and it's relevant smart contracts
func (b *thorchainBridge) GetAsgardPubKeys() ([]PubKeyContractAddressPair, error) {
	buf, err := b.getVaultPubkeys()
	if err != nil {
		return nil, fmt.Errorf("fail to get vault pubkeys ,err: %w", err)
	}
	var result openapi.VaultPubkeysResponse
	if err = json.Unmarshal(buf, &result); err != nil {
		return nil, fmt.Errorf("fail to unmarshal pubkeys: %w", err)
	}
	var addressPairs []PubKeyContractAddressPair
	for _, v := range append(result.Asgard, result.Inactive...) {
		if v.PubKeyEddsa != nil {
			kp := PubKeyContractAddressPair{
				PubKey:    common.PubKey(*v.PubKeyEddsa),
				Contracts: make(map[common.Chain]common.Address),
				Algo:      common.SigningAlgoEd25519,
			}
			for _, item := range v.Routers {
				kp.Contracts[common.Chain(*item.Chain)] = common.Address(*item.Router)
			}
			addressPairs = append(addressPairs, kp)
		}
		if v.PubKeyFrost != nil && *v.PubKeyFrost != "" {
			kp := PubKeyContractAddressPair{
				PubKey:    common.PubKey(*v.PubKeyFrost),
				Contracts: make(map[common.Chain]common.Address),
				Algo:      common.SigningAlgoEd25519,
			}
			for _, item := range v.Routers {
				kp.Contracts[common.Chain(*item.Chain)] = common.Address(*item.Router)
			}
			addressPairs = append(addressPairs, kp)
		}
		kp := PubKeyContractAddressPair{
			PubKey:    common.PubKey(v.PubKey),
			Contracts: make(map[common.Chain]common.Address),
			Algo:      common.SigningAlgoSecp256k1,
		}
		for _, item := range v.Routers {
			kp.Contracts[common.Chain(*item.Chain)] = common.Address(*item.Router)
		}
		addressPairs = append(addressPairs, kp)
	}
	return addressPairs, nil
}

// PostNetworkFee send network fee message to THORNode
func (b *thorchainBridge) PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error) {
	nodeStatus, err := b.FetchNodeStatus()
	if err != nil {
		return common.BlankTxID, fmt.Errorf("failed to get node status: %w", err)
	}

	if nodeStatus != stypes.NodeStatus_Active {
		return common.BlankTxID, nil
	}
	start := time.Now()
	defer func() {
		b.m.GetHistograms(metrics.SignToThorchainDuration).Observe(time.Since(start).Seconds())
	}()
	signerAddr, err := b.keys.GetSignerInfo().GetAddress()
	if err != nil {
		return common.BlankTxID, fmt.Errorf("fail to get signer address: %w", err)
	}
	msg := stypes.NewMsgNetworkFee(height, chain, transactionSize, transactionRate, signerAddr)
	return b.Broadcast(msg)
}

// GetConstants from thornode
func (b *thorchainBridge) GetConstants() (map[string]int64, error) {
	var result struct {
		Int64Values map[string]int64 `json:"int_64_values"`
	}
	buf, s, err := b.getWithPath(ThorchainConstants)
	if err != nil {
		return nil, fmt.Errorf("fail to get constants: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", s)
	}
	if err = json.Unmarshal(buf, &result); err != nil {
		return nil, fmt.Errorf("fail to unmarshal to json: %w", err)
	}
	return result.Int64Values, nil
}

// RagnarokInProgress is to query thorchain to check whether ragnarok had been triggered
func (b *thorchainBridge) RagnarokInProgress() (bool, error) {
	buf, s, err := b.getWithPath(RagnarokEndpoint)
	if err != nil {
		return false, fmt.Errorf("fail to get ragnarok status: %w", err)
	}
	if s != http.StatusOK {
		return false, fmt.Errorf("unexpected status code: %d", s)
	}
	var ragnarok bool
	if err = json.Unmarshal(buf, &ragnarok); err != nil {
		return false, fmt.Errorf("fail to unmarshal ragnarok status: %w", err)
	}
	return ragnarok, nil
}

// GetThorchainVersion retrieve thorchain version
func (b *thorchainBridge) GetThorchainVersion() (semver.Version, error) {
	buf, s, err := b.getWithPath(ChainVersionEndpoint)
	if err != nil {
		return semver.Version{}, fmt.Errorf("fail to get THORChain version: %w", err)
	}
	if s != http.StatusOK {
		return semver.Version{}, fmt.Errorf("unexpected status code: %d", s)
	}
	var version openapi.VersionResponse
	if err = json.Unmarshal(buf, &version); err != nil {
		return semver.Version{}, fmt.Errorf("fail to unmarshal THORChain version : %w", err)
	}
	return parseThorchainVersion(version.Current)
}

// GetMimir - get mimir settings
func (b *thorchainBridge) GetMimir(key string) (int64, error) {
	buf, s, err := b.getWithPath(MimirEndpoint + "/key/" + key)
	if err != nil {
		return 0, fmt.Errorf("fail to get mimir: %w", err)
	}
	if s != http.StatusOK {
		return 0, fmt.Errorf("unexpected status code: %d", s)
	}
	var value int64
	if err = json.Unmarshal(buf, &value); err != nil {
		return 0, fmt.Errorf("fail to unmarshal mimir: %w", err)
	}
	return value, nil
}

// PendingOutbound identifies a not-yet-observed scheduled outbound. Recovery
// callers use its height to fetch the full signed keysign plan before import.
type PendingOutbound struct {
	Chain            string              `json:"chain"`
	ToAddress        string              `json:"to_address"`
	VaultPubKey      string              `json:"vault_pub_key"`
	VaultPubKeyEddsa string              `json:"vault_pub_key_eddsa"`
	Coin             PendingOutboundCoin `json:"coin"`
	Memo             string              `json:"memo"`
	OriginalMemo     string              `json:"original_memo"`
	InHash           string              `json:"in_hash"`
	Height           int64               `json:"height"`
}

// PendingOutboundCoin is the coin of a queued outbound as the queue endpoint serves it.
type PendingOutboundCoin struct {
	Asset  string `json:"asset"`
	Amount string `json:"amount"`
}

// GetPendingOutbounds returns the outbounds currently in the outbound queue that
// have NOT yet been observed (thornode's /thorchain/queue/outbound, which filters to
// items whose OutHash is empty). height>0 queries the queue AS OF that consensus
// height (?height=), so every node evaluating the same height observes the identical
// set — used by the provisioning create-skip at its boundary height (a one-shot,
// idempotent decision that must be deterministic across members). height==0 reads the
// LIVE queue, used by the SignTx serialization gate, which must see the queue shrink
// over retries to release a waiting sibling (a fixed-height snapshot would deadlock
// it). The endpoint serialises as a BARE JSON array (QueryOutboundResponse
// .MarshalJSONPB emits its TxOutItems slice directly), so the body decodes into a
// slice, not a wrapping object.
func (b *thorchainBridge) GetPendingOutbounds(height int64) ([]PendingOutbound, error) {
	return b.GetPendingOutboundsWithContext(context.Background(), height)
}

// GetPendingOutboundsWithContext bounds queue reads and retries by ctx.
func (b *thorchainBridge) GetPendingOutboundsWithContext(ctx context.Context, height int64) ([]PendingOutbound, error) {
	var items []PendingOutbound
	if height > 0 {
		// The ?height= query must be appended AFTER the path (getKeysignStateAtHeight
		// builds it that way, uncached); embedding it in the path string makes
		// getThorChainURL escape the '?' to %3F when ChainHost has no scheme.
		if err := b.getKeysignStateAtHeightWithContext(ctx, PendingOutboundEndpoint, height, &items); err != nil {
			return nil, fmt.Errorf("fail to get pending outbounds at height %d: %w", height, err)
		}
		return items, nil
	}
	buf, s, err := b.getWithPathWithContext(ctx, PendingOutboundEndpoint)
	if err != nil {
		return nil, fmt.Errorf("fail to get pending outbounds: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", s)
	}
	if err := json.Unmarshal(buf, &items); err != nil {
		return nil, fmt.Errorf("fail to unmarshal pending outbounds: %w", err)
	}
	return items, nil
}

// SolVaultNonce is a Solana vault's durable nonce as committed on THORChain by
// observer supermajority. Empty (Nonce == "") when the vault has none.
type SolVaultNonce struct {
	NonceAccount  string `json:"nonce_account"`
	Nonce         string `json:"nonce"`
	Slot          int64  `json:"slot"`
	UpdatedHeight int64  `json:"updated_height"`
	SetByTx       string `json:"set_by_tx"`
}

// IsEmpty reports whether no durable nonce is committed.
func (n SolVaultNonce) IsEmpty() bool { return n.Nonce == "" || n.NonceAccount == "" }

// SolVaultNonceEndpoint is formatted with the vault pubkey.
const SolVaultNonceEndpoint = "/thorchain/vault/%s/sol_nonce"

// GetSolVaultNonce returns a vault's committed durable nonce. height>0 reads it AS OF
// that consensus height (?height=, uncached), so every keysign party evaluating the
// same height builds from the identical value; height<=0 reads the latest committed
// value (uncached). An empty result means the vault has no committed nonce; an error
// means the value is unknown and the caller must not guess.
func (b *thorchainBridge) GetSolVaultNonce(vaultPubKey string, height int64) (SolVaultNonce, error) {
	var out SolVaultNonce
	path := fmt.Sprintf(SolVaultNonceEndpoint, vaultPubKey)
	if height > 0 {
		if err := b.getKeysignStateAtHeight(path, height, &out); err != nil {
			return SolVaultNonce{}, fmt.Errorf("fail to get sol vault nonce at height %d: %w", height, err)
		}
		return out, nil
	}
	buf, s, err := b.getWithPathUncached(path)
	if err != nil {
		return SolVaultNonce{}, fmt.Errorf("fail to get sol vault nonce: %w", err)
	}
	if s != http.StatusOK {
		return SolVaultNonce{}, fmt.Errorf("unexpected status code %d", s)
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		return SolVaultNonce{}, fmt.Errorf("fail to unmarshal sol vault nonce: %w", err)
	}
	return out, nil
}

// GetMimirAtHeight reads a mimir value as of a specific consensus height via the
// ?height= state query (bypassing the live one-block cache), so every keysign party
// evaluating the same height observes the identical value. It gates the SOL
// durable-nonce path deterministically instead of a divergent live GetMimir
// read that could fragment a keysign across a governance flip. Unlike
// getKeysignMimirAtHeight (a safety control where null must NOT read as disabled), a
// feature gate treats a missing/null mimir as 0 (off).
func (b *thorchainBridge) GetMimirAtHeight(key string, height int64) (int64, error) {
	if height <= 0 {
		return 0, fmt.Errorf("mimir-at-height requires a positive height: %d", height)
	}
	var value *int64
	if err := b.getKeysignStateAtHeight(MimirEndpoint+"/key/"+key, height, &value); err != nil {
		return 0, err
	}
	if value == nil {
		return 0, nil // unset/null mimir = feature disabled
	}
	return *value, nil
}

// GetMimirWithRef is a helper function to more readably insert references (such as Asset MimirString or Chain) into Mimir key templates.
func (b *thorchainBridge) GetMimirWithRef(template, ref string) (int64, error) {
	// 'template' should be something like "Halt%sChain" (to halt an arbitrary specified chain)
	// or "Ragnarok-%s" (to halt the pool of an arbitrary specified Asset (MimirString used for Assets to join Chain and Symbol with a hyphen).
	key := fmt.Sprintf(template, ref)
	return b.GetMimir(key)
}

// PubKeyContractAddressPair is an entry to map pubkey and contract addresses
type PubKeyContractAddressPair struct {
	PubKey     common.PubKey
	Contracts  map[common.Chain]common.Address
	Algo       common.SigningAlgo
	Membership []common.PubKey
	Inactive   bool
}

// GetContractAddress retrieve the contract address from asgard
func (b *thorchainBridge) GetContractAddress() ([]PubKeyContractAddressPair, error) {
	buf, s, err := b.getWithPath(InboundAddressesEndpoint)
	if err != nil {
		return nil, fmt.Errorf("fail to get inbound addresses: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", s)
	}
	type address struct {
		Chain   common.Chain   `json:"chain"`
		PubKey  common.PubKey  `json:"pub_key"`
		Address common.Address `json:"address"`
		Router  common.Address `json:"router"`
		Halted  bool           `json:"halted"`
	}
	var resp []address
	if err = json.Unmarshal(buf, &resp); err != nil {
		return nil, fmt.Errorf("fail to unmarshal response: %w", err)
	}
	var result []PubKeyContractAddressPair
	for _, item := range resp {
		exist := false
		for _, pair := range result {
			if item.PubKey.Equals(pair.PubKey) {
				pair.Contracts[item.Chain] = item.Router
				exist = true
				break
			}
		}
		if !exist {
			pair := PubKeyContractAddressPair{
				PubKey:    item.PubKey,
				Contracts: map[common.Chain]common.Address{},
			}
			pair.Contracts[item.Chain] = item.Router
			result = append(result, pair)
		}
	}
	return result, nil
}

// GetPools get pools from THORChain
func (b *thorchainBridge) GetPools() (stypes.Pools, error) {
	buf, s, err := b.getWithPath(PoolsEndpoint)
	if err != nil {
		return nil, fmt.Errorf("fail to get pools addresses: %w", err)
	}
	if s != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", s)
	}
	var pools stypes.Pools
	if err = json.Unmarshal(buf, &pools); err != nil {
		return nil, fmt.Errorf("fail to unmarshal pools from json: %w", err)
	}
	return pools, nil
}

// GetTHORName get THORName from THORChain
func (b *thorchainBridge) GetTHORName(name string) (stypes.THORName, error) {
	p := fmt.Sprintf(THORNameEndpoint, name)
	buf, s, err := b.getWithPath(p)
	if err != nil {
		return stypes.THORName{}, fmt.Errorf("fail to get THORName: %w", err)
	}
	if s != http.StatusOK {
		return stypes.THORName{}, fmt.Errorf("unexpected status code: %d", s)
	}
	var tn stypes.THORName
	if err = json.Unmarshal(buf, &tn); err != nil {
		return stypes.THORName{}, fmt.Errorf("fail to unmarshal THORNames from json: %w", err)
	}
	return tn, nil
}

// GetReferenceMemo takes a chain and reference id and gets the memo
func (b *thorchainBridge) GetReferenceMemo(chain common.Chain, ref string) (string, error) {
	p := fmt.Sprintf(ReferenceMemoEndpoint, chain.String(), ref)
	buf, s, err := b.getWithPath(p)
	if err != nil {
		return "", fmt.Errorf("fail to get reference memo: %w", err)
	}
	if s != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", s)
	}
	var r stypes.ReferenceMemo
	if err := json.Unmarshal(buf, &r); err != nil { // nolint
		return "", fmt.Errorf("fail to unmarshal reference memo from json: %w", err)
	}
	return r.Memo, nil
}

// GetReferenceMemoByTxHash takes a chain and reference id and gets the memo
func (b *thorchainBridge) GetReferenceMemoByTxHash(hash string) (string, error) {
	p := fmt.Sprintf(ReferenceMemoByHashEndpoint, hash)
	buf, s, err := b.getWithPath(p)
	if err != nil {
		return "", fmt.Errorf("fail to get reference memo: %w", err)
	}
	if s != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", s)
	}
	var r stypes.ReferenceMemo
	if err := json.Unmarshal(buf, &r); err != nil { // nolint
		return "", fmt.Errorf("fail to unmarshal reference memo from json: %w", err)
	}
	return r.Memo, nil
}
