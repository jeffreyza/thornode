// Package sidecar is the HTTP boundary between Bifrost and the
// `xmr-frost-signer` Rust process. All DKG/sign/broadcast calls plus the
// wallet-scan and chain-query reads flow through the exported Client here.
package sidecar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

const (
	// Retrying combine is only safe against a duplicate Monero broadcast if the
	// signer durably replays combine by (msg_id, session_id, all_shares). The
	// sidecar image digest is unpinned, so a rollback to a signer without that
	// contract must not silently double-broadcast: default to single-shot and
	// only raise the bound for a signer that proves the contract by advertising
	// CapabilityIdempotentCombine. See SetSignCombineIdempotent.
	defaultSignCombineMaxAttempts    = 1
	idempotentSignCombineMaxAttempts = 3
	defaultSignCombineInitialBackoff = 200 * time.Millisecond
	defaultSignCombineMaxBackoff     = time.Second
	// A 429 from sign/partial is an admission rejection: the signer produced no
	// share for it. Peers keep waiting for this node's share for the keysign
	// timeout, so a short bounded replay of the same request keeps the round
	// alive instead of failing it after the other shares were already sent.
	defaultSignPartialMaxAttempts    = 5
	defaultSignPartialInitialBackoff = 250 * time.Millisecond
	defaultSignPartialMaxBackoff     = 2 * time.Second
	scannerStatePath                 = "/v1/monero/scanner/state"
	scannerRewindPath                = "/v1/monero/scanner/rewind"
	maxScannerReorgRewind            = uint64(730)
)

var errSignerRedirect = errors.New("sidecar redirects are not allowed")

type scannerContinuityCache struct {
	resetEpoch       uint64
	nextHeight       uint64
	strictAfterReset bool

	hasStateDetails bool
	hasLastHeight   bool
	lastHeight      uint64
	lastBlockHash   string
	lastParentHash  string
}

// Client is the HTTP client for the xmr-frost-signer sidecar.
type Client struct {
	endpoint string

	daemonEndpoint string

	dkgRound1Path     string
	dkgRound2Path     string
	dkgRound3Path     string
	dkgFinalizePath   string
	signPreparePath   string
	signPartialPath   string
	signCombinePath   string
	clearSessionsPath string
	broadcastPath     string
	balancePath       string
	transfersPath     string
	scannerSyncPath   string
	addressesPath     string
	outputsPath       string
	markObservedPath  string
	registerPath      string
	scannerStatePath  string
	scannerRewindPath string
	feeRatePath       string
	chainHeightPath   string
	chainBlockPath    string
	chainTxPath       string

	client                    *http.Client
	daemonClient              *http.Client
	signClient                *http.Client
	signerUsername            string
	signerPassword            string
	signerBearer              string
	daemonUsername            string
	daemonPassword            string
	expectedNetwork           string
	expectedMoneroNettype     string
	expectedMoneroGenesisHash string
	// Sync calls remain serialized, while registration/reset responses may
	// update the continuity cache as soon as they arrive. Keeping those locks
	// separate lets a completed registration rewind stop a stale sync before
	// its first POST.
	scannerSyncMu       sync.Mutex
	scannerContinuityMu sync.Mutex
	scannerContinuity   scannerContinuityCache
	continuityCached    bool
	// Kept only across an ambiguous response so the exact compare-and-set can
	// be replayed through the signer's idempotent post-commit path.
	pendingScannerRewind *ScannerRewindReq

	// A combine may have durably completed even when its HTTP response is
	// lost. Retrying the exact same idempotent request lets the signer replay
	// that result without creating a second transaction or signing session.
	signCombineMaxAttempts    int
	signCombineInitialBackoff time.Duration
	signCombineMaxBackoff     time.Duration
	signPartialMaxAttempts    int
	signPartialInitialBackoff time.Duration
	signPartialMaxBackoff     time.Duration
}

// NewClient wires a sidecar Client from a Bifrost chain configuration.
func NewClient(cfg config.BifrostChainConfiguration) (*Client, error) {
	endpoint := strings.TrimSpace(cfg.SignerEndpoint)
	return NewClientWithEndpoint(cfg, endpoint)
}

// NewClientWithEndpoint overrides the endpoint read from cfg.
func NewClientWithEndpoint(cfg config.BifrostChainConfiguration, endpoint string) (*Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("sidecar endpoint is empty")
	}

	parsedEndpoint, err := parseSidecarEndpointURL(endpoint)
	if err != nil {
		return nil, err
	}
	if parsedEndpoint.Scheme == "http" &&
		!IsLoopbackHost(parsedEndpoint.Hostname()) &&
		!AllowInsecureHTTP(cfg) {
		return nil, fmt.Errorf("sidecar endpoint %q must use https when not loopback", endpoint)
	}

	signerUsername := strings.TrimSpace(cfg.SignerUserName)
	signerPassword := strings.TrimSpace(cfg.SignerPassword)
	signerBearer := strings.TrimSpace(cfg.SignerAuthorizationBearer)
	if (signerUsername == "") != (signerPassword == "") {
		return nil, fmt.Errorf("sidecar requires both username and password when basic auth is configured")
	}
	if signerBearer != "" && signerUsername != "" {
		return nil, fmt.Errorf("sidecar bearer and basic authentication are mutually exclusive")
	}
	if !IsLoopbackHost(parsedEndpoint.Hostname()) && signerBearer == "" && signerUsername == "" {
		return nil, fmt.Errorf("sidecar endpoint %q requires signer_authorization_bearer or signer_username/signer_password", endpoint)
	}

	daemonUsername := strings.TrimSpace(cfg.UserName)
	daemonPassword := strings.TrimSpace(cfg.Password)
	if (daemonUsername == "") != (daemonPassword == "") {
		return nil, fmt.Errorf("monero daemon requires both username and password when basic auth is configured")
	}

	endpoint = strings.TrimSuffix(parsedEndpoint.String(), "/")
	daemonEndpoint := ""
	if strings.TrimSpace(cfg.RPCHost) != "" {
		parsedDaemon, daemonParseErr := parseDaemonEndpointURL(cfg.RPCHost)
		if daemonParseErr != nil {
			return nil, daemonParseErr
		}
		daemonEndpoint = normalizeDaemonEndpoint(parsedDaemon)
	}

	timeout := cfg.BlockScanner.HTTPRequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	signTimeout := timeout
	if signTimeout < 120*time.Second {
		signTimeout = 120 * time.Second
	}
	expectedMoneroNettype := ""
	expectedMoneroGenesisHash := ""
	if strings.TrimSpace(cfg.ChainNetwork) != "" {
		expectedMoneroNettype, expectedMoneroGenesisHash, err = expectedMoneroIdentity(cfg.ChainNetwork)
		if err != nil {
			return nil, err
		}
	}

	// Signer routes are fixed. Redirects must not bypass endpoint policy or
	// be accepted as successful responses by the JSON helpers.
	rejectRedirect := func(_ *http.Request, _ []*http.Request) error {
		return errSignerRedirect
	}

	return &Client{
		endpoint:       endpoint,
		daemonEndpoint: daemonEndpoint,

		dkgRound1Path:     "/v1/monero/dkg/round1",
		dkgRound2Path:     "/v1/monero/dkg/round2",
		dkgRound3Path:     "/v1/monero/dkg/round3",
		dkgFinalizePath:   "/v1/monero/dkg/finalize",
		signPreparePath:   "/v1/monero/sign/prepare",
		signPartialPath:   "/v1/monero/sign/partial",
		signCombinePath:   "/v1/monero/sign/combine",
		clearSessionsPath: "/v1/monero/sessions/clear",
		broadcastPath:     "/v1/monero/broadcast",
		balancePath:       "/v1/monero/balance",
		transfersPath:     "/v1/monero/transfers",
		scannerSyncPath:   "/v1/monero/scanner/sync",
		addressesPath:     "/v1/monero/addresses",
		outputsPath:       "/v1/monero/outputs",
		markObservedPath:  "/v1/monero/outputs/mark-observed",
		registerPath:      "/v1/monero/vaults/register",
		scannerStatePath:  "/v1/monero/scanner/state",
		scannerRewindPath: "/v1/monero/scanner/rewind",
		feeRatePath:       "/v1/monero/fee-rate",
		chainHeightPath:   "/v1/monero/chain/height",
		chainBlockPath:    "/v1/monero/chain/block",
		chainTxPath:       "/v1/monero/chain/tx",

		client:                    &http.Client{Timeout: timeout, CheckRedirect: rejectRedirect},
		daemonClient:              &http.Client{Timeout: timeout},
		signClient:                &http.Client{Timeout: signTimeout, CheckRedirect: rejectRedirect},
		signerUsername:            signerUsername,
		signerPassword:            signerPassword,
		signerBearer:              signerBearer,
		daemonUsername:            daemonUsername,
		daemonPassword:            daemonPassword,
		expectedNetwork:           strings.TrimSpace(cfg.SignerExpectedNetwork),
		expectedMoneroNettype:     expectedMoneroNettype,
		expectedMoneroGenesisHash: expectedMoneroGenesisHash,
		signCombineMaxAttempts:    defaultSignCombineMaxAttempts,
		signCombineInitialBackoff: defaultSignCombineInitialBackoff,
		signCombineMaxBackoff:     defaultSignCombineMaxBackoff,
		signPartialMaxAttempts:    defaultSignPartialMaxAttempts,
		signPartialInitialBackoff: defaultSignPartialInitialBackoff,
		signPartialMaxBackoff:     defaultSignPartialMaxBackoff,
	}, nil
}

// expectedMoneroIdentity maps the configured XMR chain network to the
// daemon identity the signer must derive from monerod. Checking both fields
// prevents a deployment label from hiding a signer attached to another chain.
func expectedMoneroIdentity(chainNetwork string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(chainNetwork)) {
	case "mocknet":
		// Monero's FAKECHAIN mode deliberately reuses the mainnet genesis block.
		return "fakechain", MoneroMainnetGenesisHash, nil
	case "mainnet":
		return "mainnet", MoneroMainnetGenesisHash, nil
	case "stagenet":
		return "stagenet", MoneroStagenetGenesisHash, nil
	case "testnet":
		return "testnet", MoneroTestnetGenesisHash, nil
	default:
		return "", "", fmt.Errorf(
			"unsupported xmr chain_network %q (accepted values: mocknet, mainnet, stagenet, testnet)",
			strings.TrimSpace(chainNetwork),
		)
	}
}

func parseSidecarEndpointURL(endpoint string) (*url.URL, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("sidecar endpoint is empty")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		// url.Error includes the full input URL, which may contain a mistakenly
		// embedded credential. Keep configuration errors secret-free.
		return nil, errors.New("invalid sidecar endpoint URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid sidecar endpoint scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, fmt.Errorf("invalid sidecar endpoint host")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("sidecar endpoint must not contain URL credentials; use signer authentication fields")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return nil, fmt.Errorf("sidecar endpoint must not contain a query or fragment")
	}
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return nil, fmt.Errorf("sidecar endpoint must not contain a path")
	}
	parsed.Path = ""
	return parsed, nil
}

// IsLoopbackHost reports whether host resolves to a loopback address or the
// "localhost" alias. Exported for reuse by the server-side component.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AllowInsecureHTTP decides whether a non-loopback http:// signer endpoint is
// accepted. It is an explicit development-only escape hatch and is never
// honored for a signer that claims mainnet.
func AllowInsecureHTTP(cfg config.BifrostChainConfiguration) bool {
	return cfg.SignerAllowInsecureHTTP &&
		!strings.EqualFold(strings.TrimSpace(cfg.SignerExpectedNetwork), "mainnet")
}

// --- FROST DKG ---

func (c *Client) DKGRound1(req DKGRound1Req) (DKGRound1Resp, error) {
	var out DKGRound1Resp
	if err := c.call(http.MethodPost, c.dkgRound1Path, req, &out); err != nil {
		return DKGRound1Resp{}, fmt.Errorf("fail to call dkg round1 endpoint: %w", err)
	}
	if strings.TrimSpace(out.Commitments) == "" {
		return DKGRound1Resp{}, fmt.Errorf("dkg round1 endpoint returned empty commitments")
	}
	return out, nil
}

func (c *Client) DKGRound2(req DKGRound2Req) (DKGRound2Resp, error) {
	var out DKGRound2Resp
	if err := c.call(http.MethodPost, c.dkgRound2Path, req, &out); err != nil {
		return DKGRound2Resp{}, fmt.Errorf("fail to call dkg round2 endpoint: %w", err)
	}
	// A single-participant FROST DKG has nobody else to encrypt shares for, so
	// the sidecar legitimately returns an empty map here.
	if len(out.EncryptedShares) == 0 && len(req.AllCommitments) > 1 {
		return DKGRound2Resp{}, fmt.Errorf("dkg round2 endpoint returned empty encrypted_shares")
	}
	return out, nil
}

func (c *Client) DKGRound3(req DKGRound3Req) (DKGRound3Resp, error) {
	var out DKGRound3Resp
	if err := c.call(http.MethodPost, c.dkgRound3Path, req, &out); err != nil {
		return DKGRound3Resp{}, fmt.Errorf("fail to call dkg round3 endpoint: %w", err)
	}
	if strings.TrimSpace(out.Address) == "" {
		return DKGRound3Resp{}, fmt.Errorf("dkg round3 endpoint returned empty address")
	}
	return out, nil
}

func (c *Client) DKGFinalize(req DKGFinalizeReq) (DKGFinalizeResp, error) {
	var out DKGFinalizeResp
	if err := c.call(http.MethodPost, c.dkgFinalizePath, req, &out); err != nil {
		return DKGFinalizeResp{}, fmt.Errorf("fail to call dkg finalize endpoint: %w", err)
	}
	if !out.Success {
		return DKGFinalizeResp{}, fmt.Errorf("dkg finalize endpoint returned unsuccessful response")
	}
	return out, nil
}

// --- FROST signing ---

func (c *Client) SignPrepare(req SignPrepareReq) (SignPrepareResp, error) {
	var out SignPrepareResp
	if err := c.call(http.MethodPost, c.signPreparePath, req, &out); err != nil {
		return SignPrepareResp{}, fmt.Errorf("fail to call sign prepare endpoint: %w", err)
	}
	if strings.TrimSpace(out.SessionID) == "" {
		return SignPrepareResp{}, fmt.Errorf("sign prepare endpoint returned empty session_id")
	}
	if strings.TrimSpace(out.Preprocess) == "" {
		return SignPrepareResp{}, fmt.Errorf("sign prepare endpoint returned empty preprocess")
	}
	return out, nil
}

func (c *Client) SignPartial(req SignPartialReq) (SignPartialResp, error) {
	return c.SignPartialContext(context.Background(), req)
}

// SignPartialContext retries only HTTP 429, which the signer returns before it
// processes the request. Every attempt reuses the exact serialized msg_id,
// session_id and all_preprocesses, so a retry stays inside the same FROST
// session and can never start a second signing ceremony.
func (c *Client) SignPartialContext(ctx context.Context, req SignPartialReq) (SignPartialResp, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return SignPartialResp{}, fmt.Errorf("fail to marshal sign partial request: %w", err)
	}

	maxAttempts, backoff, maxBackoff := c.signPartialRetryPolicy()
	var out SignPartialResp
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out = SignPartialResp{}
		err = c.callJSONContext(ctx, http.MethodPost, c.signPartialPath, body, &out)
		if err == nil {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SignPartialResp{}, fmt.Errorf("sign partial canceled: %w", ctxErr)
		}
		if attempt == maxAttempts || !isRateLimitedError(err) {
			return SignPartialResp{}, fmt.Errorf("fail to call sign partial endpoint: %w", err)
		}
		if waitErr := waitForSignCombineRetry(ctx, backoff); waitErr != nil {
			return SignPartialResp{}, fmt.Errorf("sign partial retry canceled: %w", waitErr)
		}
		backoff = nextSignCombineBackoff(backoff, maxBackoff)
	}
	if strings.TrimSpace(out.SignatureShare) == "" {
		return SignPartialResp{}, fmt.Errorf("sign partial endpoint returned empty signature_share")
	}
	return out, nil
}

func (c *Client) SignCombine(req SignCombineReq) (SignCombineResp, error) {
	return c.SignCombineContext(context.Background(), req)
}

// SignCombineContext retries only failures for which the signer may not have
// returned a definitive application result. The request is serialized once
// and every attempt reuses those exact bytes, preserving msg_id, session_id,
// and all_shares for the signer's idempotent durable replay path.
func (c *Client) SignCombineContext(ctx context.Context, req SignCombineReq) (SignCombineResp, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return SignCombineResp{}, fmt.Errorf("fail to marshal sign combine request: %w", err)
	}

	maxAttempts, backoff, maxBackoff := c.signCombineRetryPolicy()
	var out SignCombineResp
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out = SignCombineResp{}
		err = c.callJSONContext(ctx, http.MethodPost, c.signCombinePath, body, &out)
		if err == nil {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SignCombineResp{}, fmt.Errorf("sign combine canceled: %w", ctxErr)
		}
		if attempt == maxAttempts || !isRetryableSignCombineError(ctx, err) {
			return SignCombineResp{}, fmt.Errorf("fail to call sign combine endpoint: %w", err)
		}
		if waitErr := waitForSignCombineRetry(ctx, backoff); waitErr != nil {
			return SignCombineResp{}, fmt.Errorf("sign combine retry canceled: %w", waitErr)
		}
		backoff = nextSignCombineBackoff(backoff, maxBackoff)
	}
	if strings.TrimSpace(out.TxHash) == "" {
		return SignCombineResp{}, fmt.Errorf("sign combine endpoint returned empty tx_hash")
	}
	if strings.TrimSpace(out.TxKey) == "" {
		return SignCombineResp{}, fmt.Errorf("sign combine endpoint returned empty tx_key")
	}
	return out, nil
}

// SetSignCombineIdempotent enables bounded combine retries, and must be called
// only with a signer that advertises CapabilityIdempotentCombine. Retrying a
// combine against a signer without the durable replay contract can broadcast the
// same Monero transaction twice, so an unproven signer stays single-shot.
func (c *Client) SetSignCombineIdempotent(idempotent bool) {
	if idempotent {
		c.signCombineMaxAttempts = idempotentSignCombineMaxAttempts
		return
	}
	c.signCombineMaxAttempts = defaultSignCombineMaxAttempts
}

func (c *Client) signCombineRetryPolicy() (int, time.Duration, time.Duration) {
	maxAttempts := c.signCombineMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultSignCombineMaxAttempts
	}
	backoff := c.signCombineInitialBackoff
	if backoff < 0 {
		backoff = 0
	} else if backoff == 0 {
		backoff = defaultSignCombineInitialBackoff
	}
	maxBackoff := c.signCombineMaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = defaultSignCombineMaxBackoff
	}
	if maxBackoff < backoff {
		maxBackoff = backoff
	}
	return maxAttempts, backoff, maxBackoff
}

func (c *Client) signPartialRetryPolicy() (int, time.Duration, time.Duration) {
	maxAttempts := c.signPartialMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultSignPartialMaxAttempts
	}
	backoff := c.signPartialInitialBackoff
	if backoff < 0 {
		backoff = 0
	} else if backoff == 0 {
		backoff = defaultSignPartialInitialBackoff
	}
	maxBackoff := c.signPartialMaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = defaultSignPartialMaxBackoff
	}
	if maxBackoff < backoff {
		maxBackoff = backoff
	}
	return maxAttempts, backoff, maxBackoff
}

func isRateLimitedError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == http.StatusTooManyRequests
}

func isRetryableSignCombineError(ctx context.Context, err error) bool {
	if err == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status >= http.StatusInternalServerError ||
			httpErr.Status == http.StatusRequestTimeout ||
			httpErr.Status == http.StatusTooEarly ||
			httpErr.Status == http.StatusTooManyRequests
	}

	// HTTP transport failures are ambiguous: the signer may have committed
	// the transaction before the connection failed. A bounded retry of the
	// same request is safe because combine is idempotent by msg/session/body.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func waitForSignCombineRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextSignCombineBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum {
		return maximum
	}
	next := current * 2
	if next < current || next > maximum {
		return maximum
	}
	return next
}

// ClearSessions drops any cached per-session state on the sidecar. Returns the
// count of sessions cleared for logging.
func (c *Client) ClearSessions() (int, error) {
	if c == nil || strings.TrimSpace(c.clearSessionsPath) == "" {
		return 0, nil
	}
	var out struct {
		ClearedSessions int    `json:"cleared_sessions"`
		Status          string `json:"status"`
	}
	if err := c.call(http.MethodPost, c.clearSessionsPath, struct{}{}, &out); err != nil {
		return 0, fmt.Errorf("fail to call sessions clear endpoint: %w", err)
	}
	return out.ClearedSessions, nil
}

func (c *Client) Broadcast(req BroadcastReq) (BroadcastResp, error) {
	req.TxKey = strings.ToLower(strings.TrimSpace(req.TxKey))
	if len(req.TxKey) != 64 {
		return BroadcastResp{}, fmt.Errorf("broadcast tx_key must be canonical 32-byte hex")
	}
	if _, err := hex.DecodeString(req.TxKey); err != nil {
		return BroadcastResp{}, fmt.Errorf("broadcast tx_key must be canonical 32-byte hex")
	}
	var out BroadcastResp
	if err := c.call(http.MethodPost, c.broadcastPath, req, &out); err != nil {
		return BroadcastResp{}, fmt.Errorf("fail to call broadcast endpoint: %w", err)
	}
	out.TxKey = strings.ToLower(strings.TrimSpace(out.TxKey))
	if out.TxKey != req.TxKey {
		return BroadcastResp{}, fmt.Errorf("broadcast endpoint returned mismatched tx_key")
	}
	return out, nil
}

// --- Reads ---

func (c *Client) Balance(vaultPubKey string) (Balance, error) {
	var out Balance
	path := c.balancePath + "/" + url.PathEscape(vaultPubKey)
	if err := c.callGet(path, &out); err != nil {
		return Balance{}, fmt.Errorf("fail to call balance endpoint: %w", err)
	}
	return out, nil
}

func (c *Client) RegisterVault(req RegisterVaultReq) (RegisterVaultResp, error) {
	return c.RegisterVaultContext(context.Background(), req)
}

func (c *Client) RegisterVaultContext(ctx context.Context, req RegisterVaultReq) (RegisterVaultResp, error) {
	if req.VaultCreationHeight < 0 {
		return RegisterVaultResp{}, fmt.Errorf("vault creation height must be non-negative")
	}
	var out RegisterVaultResp
	if err := c.callContext(ctx, http.MethodPost, c.registerPath, req, &out); err != nil {
		return RegisterVaultResp{}, fmt.Errorf("fail to call register vault endpoint: %w", err)
	}
	if out.EffectiveBirthday > math.MaxInt64 {
		return RegisterVaultResp{}, fmt.Errorf("register vault endpoint returned effective birthday outside the int64 scanner range")
	}
	// A positive candidate is the newest safe enrollment height. The sidecar
	// may retain an earlier birthday on retry, but moving it later could skip
	// vault history that Bifrost has not scanned.
	if req.VaultCreationHeight > 0 {
		if out.EffectiveBirthday == 0 {
			return RegisterVaultResp{}, fmt.Errorf("register vault endpoint returned zero effective birthday for requested birthday %d", req.VaultCreationHeight)
		}
		if out.EffectiveBirthday > uint64(req.VaultCreationHeight) {
			return RegisterVaultResp{}, fmt.Errorf(
				"register vault endpoint returned effective birthday %d after requested birthday %d",
				out.EffectiveBirthday,
				req.VaultCreationHeight,
			)
		}
	}
	// A key-recovery registration with no birthday does not enroll the scanner.
	// Once a birthday or reset exists, durable cursor continuity is mandatory.
	if out.ExpectedNextHeight == nil && out.EffectiveBirthday == 0 && out.ResetEpoch == 0 {
		return out, nil
	}
	// New sidecars return their cursor on every registration, including the
	// idempotent already-registered case. A newly discovered older vault may
	// have rewound it, so publish that result to the scanner immediately.
	continuity, err := scannerContinuityFromRegistration(out)
	if err != nil {
		return RegisterVaultResp{}, fmt.Errorf("register vault endpoint returned invalid scanner continuity: %w", err)
	}
	c.mergeScannerContinuity(continuity)
	return out, nil
}

// setScannerCursor publishes a cursor observed in a sidecar response into the
// single continuity cache. It is deliberately lenient about optional fields;
// the epoch-then-height ordering inside mergeScannerContinuity is what stops a
// delayed response from lowering the cursor.
func (c *Client) setScannerCursor(state ScannerState) {
	candidate := scannerContinuityCache{
		resetEpoch:       state.ResetEpoch,
		strictAfterReset: state.StrictAfterReset,
	}
	if state.NextHeight != nil {
		candidate.nextHeight = *state.NextHeight
	}
	if state.LastHeight != nil {
		candidate.hasStateDetails = true
		candidate.hasLastHeight = true
		candidate.lastHeight = *state.LastHeight
		// Stored lowercased because every continuity comparison is made against
		// hashes normalized by normalizeScannerHash.
		candidate.lastBlockHash = strings.ToLower(strings.TrimSpace(state.LastBlockHash))
		candidate.lastParentHash = strings.ToLower(strings.TrimSpace(state.LastParentHash))
	}
	c.mergeScannerContinuity(candidate)
}

// CachedScannerState returns the last cursor authenticated by a sidecar
// response. It is a hint only; ScannerState should be fetched before a CAS
// rewind.
func (c *Client) CachedScannerState() (ScannerState, bool) {
	continuity, ok := c.cachedScannerContinuity()
	if !ok {
		return ScannerState{}, false
	}
	nextHeight := continuity.nextHeight
	state := ScannerState{
		ResetEpoch:       continuity.resetEpoch,
		NextHeight:       &nextHeight,
		StrictAfterReset: continuity.strictAfterReset,
	}
	if continuity.hasLastHeight {
		lastHeight := continuity.lastHeight
		state.LastHeight = &lastHeight
		state.LastBlockHash = continuity.lastBlockHash
		state.LastParentHash = continuity.lastParentHash
	}
	return state, true
}

// ScannerState fetches the sidecar's authoritative durable scanner cursor. The
// response is validated before it is allowed to seed the continuity cache, so
// a malformed or non-contiguous state can never become a scan-binding cursor.
func (c *Client) ScannerState() (ScannerState, error) {
	var out ScannerState
	if err := c.callGet(c.scannerStatePath, &out); err != nil {
		return ScannerState{}, fmt.Errorf("fail to call scanner state endpoint: %w", err)
	}
	continuity, err := scannerContinuityFromState(out)
	if err != nil {
		return ScannerState{}, fmt.Errorf("scanner state endpoint returned invalid continuity: %w", err)
	}
	c.mergeScannerContinuity(continuity)
	return out, nil
}

// RewindScanner atomically rolls the sidecar back to a daemon-authenticated
// common ancestor. The epoch/cursor pair is a compare-and-set guard and the
// endpoint is idempotent for one lost response.
func (c *Client) RewindScanner(req ScannerRewindReq) (ScannerRewindResp, error) {
	c.scannerSyncMu.Lock()
	defer c.scannerSyncMu.Unlock()
	expectedNetwork := strings.ToLower(strings.TrimSpace(c.expectedNetwork))
	if expectedNetwork == "" {
		return ScannerRewindResp{}, fmt.Errorf("expected sidecar network is not configured")
	}
	requestNetwork := strings.ToLower(strings.TrimSpace(req.Network))
	if requestNetwork != "" && requestNetwork != expectedNetwork {
		return ScannerRewindResp{}, fmt.Errorf(
			"scanner rewind network %q does not match expected sidecar network %q",
			requestNetwork,
			expectedNetwork,
		)
	}
	// The rewind compare-and-set token is the THOR deployment identity exposed
	// by /info and XMR_FROST_CHAIN_NETWORK (chainnet/mocknet/etc.), not the
	// Monero daemon network configured in ChainNetwork. Derive it here so every
	// ordinary scanner recovery path uses the same fail-closed identity as the
	// bounded reorg helper.
	req.Network = expectedNetwork
	var out ScannerRewindResp
	if err := c.call(http.MethodPost, c.scannerRewindPath, req, &out); err != nil {
		return ScannerRewindResp{}, fmt.Errorf("fail to call scanner rewind endpoint: %w", err)
	}
	// Without this the response is fed straight into the epoch-ordered cache, so
	// a stale epoch or a cursor that never moved is silently dropped by
	// mergeScannerContinuity while this call still reports success. Hold the
	// rewind response to the same compare-and-set contract as the reorg path.
	if err := validateScannerRewindResponse(req, out); err != nil {
		return ScannerRewindResp{}, fmt.Errorf("scanner rewind returned invalid continuity: %w", err)
	}
	nextHeight := out.NextHeight
	lastHeight := req.AnchorHeight
	c.setScannerCursor(ScannerState{
		ResetEpoch:       out.ResetEpoch,
		NextHeight:       &nextHeight,
		LastHeight:       &lastHeight,
		LastBlockHash:    req.AnchorHash,
		StrictAfterReset: true,
	})
	return out, nil
}

// TransfersAt returns the sidecar's diagnostic view of transfers at a height
// (read-only; scanner sync is the canonical drive).
func (c *Client) TransfersAt(height int64) (Transfers, error) {
	var out Transfers
	path := fmt.Sprintf("%s?height=%d", c.transfersPath, height)
	if err := c.callGet(path, &out); err != nil {
		return Transfers{}, fmt.Errorf("fail to call transfers endpoint: %w", err)
	}
	return out, nil
}

// SyncAt is the canonical Bifrost-driven block scan call.
func (c *Client) SyncAt(height, tipHeight int64, blockHash, parentHash string, reorgWindow int64) (Transfers, error) {
	if height < 0 {
		return Transfers{}, fmt.Errorf("height must be >= 0")
	}
	if height == math.MaxInt64 {
		return Transfers{}, fmt.Errorf("height cannot advance beyond the int64 scanner range")
	}
	if tipHeight < 0 {
		tipHeight = 0
	}
	if reorgWindow < 0 {
		reorgWindow = 0
	}
	normalizedBlockHash, err := normalizeScannerHash("block_hash", blockHash)
	if err != nil {
		return Transfers{}, err
	}
	normalizedParentHash, err := normalizeScannerHash("parent_hash", parentHash)
	if err != nil {
		return Transfers{}, err
	}

	c.scannerSyncMu.Lock()
	defer c.scannerSyncMu.Unlock()

	requestedHeight := uint64(height)
	req := SyncReq{
		Height:      height,
		TipHeight:   tipHeight,
		BlockHash:   normalizedBlockHash,
		ParentHash:  normalizedParentHash,
		ReorgWindow: reorgWindow,
	}
	if continuity, ok := c.cachedScannerContinuity(); ok {
		if continuity.nextHeight < requestedHeight {
			return Transfers{}, scannerRewindError(height, continuity)
		}
		bindScannerContinuity(&req, continuity.resetEpoch, requestedHeight)
	}

	var out Transfers
	if err = c.call(http.MethodPost, c.scannerSyncPath, req, &out); err == nil {
		c.cacheSuccessfulScannerSync(req)
		return out, nil
	}
	if !IsHTTPStatus(err, http.StatusConflict) {
		return Transfers{}, fmt.Errorf("fail to call scanner sync endpoint: %w", err)
	}
	originalErr := err

	continuity, stateErr := c.fetchScannerContinuity(context.Background())
	if stateErr != nil {
		return Transfers{}, fmt.Errorf("scanner sync conflicted and authenticated continuity refresh failed: %w", stateErr)
	}
	continuity = c.mergeScannerContinuity(continuity)
	if continuity.nextHeight < requestedHeight {
		return Transfers{}, scannerRewindError(height, continuity)
	}

	canRetry := scannerContinuityIsExactReplay(
		continuity,
		requestedHeight,
		normalizedBlockHash,
		normalizedParentHash,
	) || (continuity.strictAfterReset && continuity.nextHeight == requestedHeight)
	if !canRetry {
		return Transfers{}, fmt.Errorf("fail to call scanner sync endpoint: %w", originalErr)
	}

	bindScannerContinuity(&req, continuity.resetEpoch, requestedHeight)
	out = Transfers{}
	if retryErr := c.call(http.MethodPost, c.scannerSyncPath, req, &out); retryErr != nil {
		return Transfers{}, fmt.Errorf("fail to call scanner sync endpoint after continuity refresh: %w", retryErr)
	}
	c.cacheSuccessfulScannerSync(req)
	return out, nil
}

// PreflightScannerHeight reads the authenticated sidecar cursor before a
// caller mutates its local reorg state. A cursor behind the requested height
// requires a local rewind. A cursor ahead is accepted only for the one exact
// last-block replay which SyncAt itself permits.
func (c *Client) PreflightScannerHeight(requestedHeight int64, blockHash, parentHash string) error {
	if requestedHeight < 0 {
		return fmt.Errorf("height must be >= 0")
	}
	if requestedHeight == math.MaxInt64 {
		return fmt.Errorf("height cannot advance beyond the int64 scanner range")
	}
	normalizedBlockHash, err := normalizeScannerHash("block_hash", blockHash)
	if err != nil {
		return err
	}
	normalizedParentHash, err := normalizeScannerHash("parent_hash", parentHash)
	if err != nil {
		return err
	}

	c.scannerSyncMu.Lock()
	defer c.scannerSyncMu.Unlock()

	continuity, err := c.fetchScannerContinuity(context.Background())
	if err != nil {
		return fmt.Errorf("fail to preflight authenticated scanner continuity: %w", err)
	}
	continuity = c.mergeScannerContinuity(continuity)
	requested := uint64(requestedHeight)
	if continuity.nextHeight < requested {
		return scannerRewindError(requestedHeight, continuity)
	}
	if continuity.nextHeight == requested {
		return nil
	}
	if scannerContinuityIsExactReplay(
		continuity,
		requested,
		normalizedBlockHash,
		normalizedParentHash,
	) {
		return nil
	}
	return &ScannerRecoveryRequiredError{
		RequestedHeight: requestedHeight,
		NextHeight:      int64(continuity.nextHeight),
		ResetEpoch:      continuity.resetEpoch,
	}
}

// RewindScannerForReorg performs a bounded, authenticated compare-and-set on
// the sidecar's durable global cursor. It is serialized with SyncAt and
// PreflightScannerHeight so no scan can race between the state read and rewind
// commit. The exact request is retained across ambiguous transport failures so
// the signer's post-commit idempotency path can prove a lost response.
func (c *Client) RewindScannerForReorg(
	targetNextHeight,
	anchorHeight int64,
	anchorHash string,
) (ScannerRewindResp, error) {
	if targetNextHeight <= 0 {
		return ScannerRewindResp{}, fmt.Errorf("scanner reorg target_next_height must be positive")
	}
	if anchorHeight < 0 || anchorHeight == math.MaxInt64 {
		return ScannerRewindResp{}, fmt.Errorf("scanner reorg anchor_height is outside the int64 scanner range")
	}
	if targetNextHeight != anchorHeight+1 {
		return ScannerRewindResp{}, fmt.Errorf("scanner reorg target_next_height must equal anchor_height plus one")
	}
	normalizedAnchorHash, err := normalizeScannerHash("anchor_hash", anchorHash)
	if err != nil {
		return ScannerRewindResp{}, err
	}
	network := strings.ToLower(strings.TrimSpace(c.expectedNetwork))
	if network == "" {
		return ScannerRewindResp{}, fmt.Errorf("expected sidecar network is not configured")
	}

	c.scannerSyncMu.Lock()
	defer c.scannerSyncMu.Unlock()

	continuity, err := c.fetchScannerContinuity(context.Background())
	if err != nil {
		return ScannerRewindResp{}, fmt.Errorf("fail to read authenticated scanner continuity before reorg rewind: %w", err)
	}
	continuity = c.mergeScannerContinuity(continuity)
	if c.pendingScannerRewind == nil && scannerContinuityIsDurableRewindTarget(
		continuity,
		uint64(targetNextHeight),
		uint64(anchorHeight),
		normalizedAnchorHash,
	) {
		// A process restart can lose the in-memory request receipt after the
		// sidecar durably committed. The authenticated exact anchor is itself
		// sufficient proof that no rewind mutation remains to be performed.
		return ScannerRewindResp{
			Rewound:            false,
			PreviousNextHeight: continuity.nextHeight,
			NextHeight:         continuity.nextHeight,
			ResetEpoch:         continuity.resetEpoch,
		}, nil
	}
	req, err := c.scannerRewindRequestLocked(
		network,
		uint64(targetNextHeight),
		uint64(anchorHeight),
		normalizedAnchorHash,
		continuity,
	)
	if err != nil {
		return ScannerRewindResp{}, err
	}

	var out ScannerRewindResp
	err = c.call(http.MethodPost, scannerRewindPath, req, &out)
	if err != nil && isAmbiguousScannerRewindError(err) {
		// A dropped response may follow a durable commit. Replay the byte-for-byte
		// equivalent compare-and-set once; the server returns rewound=false only
		// after proving the already-committed anchor.
		out = ScannerRewindResp{}
		err = c.call(http.MethodPost, scannerRewindPath, req, &out)
	}
	if err != nil {
		if scannerRewindErrorIsDefinitive(err) {
			c.pendingScannerRewind = nil
		}
		return ScannerRewindResp{}, fmt.Errorf("fail to call scanner reorg rewind endpoint: %w", err)
	}
	if err = validateScannerRewindResponse(req, out); err != nil {
		return ScannerRewindResp{}, fmt.Errorf("scanner reorg rewind returned invalid continuity: %w", err)
	}

	if out.Rewound {
		c.mergeScannerContinuity(scannerContinuityCache{
			resetEpoch:       out.ResetEpoch,
			nextHeight:       out.NextHeight,
			strictAfterReset: true,
			hasStateDetails:  true,
			hasLastHeight:    true,
			lastHeight:       req.AnchorHeight,
			lastBlockHash:    req.AnchorHash,
		})
	} else {
		// Never trust rewound=false as a success by itself. Refresh the
		// authenticated state and prove the exact durable anchor and epoch.
		refreshed, refreshErr := c.fetchScannerContinuity(context.Background())
		if refreshErr != nil {
			return ScannerRewindResp{}, fmt.Errorf("fail to verify idempotent scanner rewind: %w", refreshErr)
		}
		if !scannerContinuityMatchesRewindAnchor(refreshed, req, out.ResetEpoch) {
			return ScannerRewindResp{}, fmt.Errorf("idempotent scanner rewind did not prove the requested anchor")
		}
		c.mergeScannerContinuity(refreshed)
	}

	c.pendingScannerRewind = nil
	return out, nil
}

func (c *Client) scannerRewindRequestLocked(
	network string,
	targetNextHeight,
	anchorHeight uint64,
	anchorHash string,
	continuity scannerContinuityCache,
) (ScannerRewindReq, error) {
	if pending := c.pendingScannerRewind; pending != nil {
		postEpoch, ok := checkedNextResetEpoch(pending.ExpectedResetEpoch)
		if !ok {
			return ScannerRewindReq{}, fmt.Errorf("pending scanner rewind reset epoch is exhausted")
		}
		// A committed rewind advances the reset epoch, so authenticated continuity
		// still sitting on the pending rewind's original epoch and height proves it
		// never committed, and matching its anchor under the post-commit epoch proves
		// it did. Either way the outcome is known and the latch has served its purpose.
		atOriginalCursor := continuity.resetEpoch == pending.ExpectedResetEpoch &&
			continuity.nextHeight == pending.ExpectedNextHeight
		atCommittedAnchor := scannerContinuityMatchesRewindAnchor(continuity, *pending, postEpoch)

		if pending.Network == network &&
			pending.TargetNextHeight == targetNextHeight &&
			pending.AnchorHeight == anchorHeight &&
			pending.AnchorHash == anchorHash {
			if !atOriginalCursor && !atCommittedAnchor {
				return ScannerRewindReq{}, fmt.Errorf("pending scanner rewind no longer matches authenticated continuity")
			}
			return *pending, nil
		}

		// A deeper reorg superseded the ambiguous rewind. Retain the latch only while
		// its outcome is genuinely unknown; otherwise resolving it here is what keeps
		// reorg recovery alive, since the superseding rewind can never replay it.
		if !atOriginalCursor && !atCommittedAnchor {
			return ScannerRewindReq{}, fmt.Errorf("a different scanner rewind has an ambiguous pending result")
		}
		c.pendingScannerRewind = nil
	}

	if targetNextHeight >= continuity.nextHeight {
		return ScannerRewindReq{}, fmt.Errorf(
			"scanner reorg target must be behind current cursor: target %d, current %d",
			targetNextHeight,
			continuity.nextHeight,
		)
	}
	if continuity.nextHeight-targetNextHeight > maxScannerReorgRewind {
		return ScannerRewindReq{}, fmt.Errorf(
			"scanner reorg rewind exceeds %d-block safety bound",
			maxScannerReorgRewind,
		)
	}
	if _, ok := checkedNextResetEpoch(continuity.resetEpoch); !ok {
		return ScannerRewindReq{}, fmt.Errorf("scanner reset epoch is exhausted")
	}

	req := ScannerRewindReq{
		Network:            network,
		ExpectedResetEpoch: continuity.resetEpoch,
		ExpectedNextHeight: continuity.nextHeight,
		TargetNextHeight:   targetNextHeight,
		AnchorHeight:       anchorHeight,
		AnchorHash:         anchorHash,
	}
	c.pendingScannerRewind = &req
	return req, nil
}

func validateScannerRewindResponse(req ScannerRewindReq, resp ScannerRewindResp) error {
	expectedEpoch, ok := checkedNextResetEpoch(req.ExpectedResetEpoch)
	if !ok {
		return fmt.Errorf("expected reset epoch cannot advance")
	}
	if resp.PreviousNextHeight != req.ExpectedNextHeight {
		return fmt.Errorf("previous_next_height does not match authenticated pre-rewind cursor")
	}
	if resp.NextHeight != req.TargetNextHeight {
		return fmt.Errorf("next_height does not match requested target")
	}
	if resp.ResetEpoch != expectedEpoch {
		return fmt.Errorf("reset_epoch did not advance exactly once")
	}
	return nil
}

func scannerContinuityMatchesRewindAnchor(
	continuity scannerContinuityCache,
	req ScannerRewindReq,
	resetEpoch uint64,
) bool {
	return continuity.resetEpoch == resetEpoch &&
		continuity.nextHeight == req.TargetNextHeight &&
		continuity.hasStateDetails &&
		continuity.hasLastHeight &&
		continuity.lastHeight == req.AnchorHeight &&
		continuity.lastBlockHash == req.AnchorHash
}

func scannerContinuityIsDurableRewindTarget(
	continuity scannerContinuityCache,
	targetNextHeight,
	anchorHeight uint64,
	anchorHash string,
) bool {
	return continuity.strictAfterReset &&
		continuity.resetEpoch > 0 &&
		continuity.nextHeight == targetNextHeight &&
		continuity.hasStateDetails &&
		continuity.hasLastHeight &&
		continuity.lastHeight == anchorHeight &&
		continuity.lastBlockHash == anchorHash
}

func checkedNextResetEpoch(epoch uint64) (uint64, bool) {
	if epoch == ^uint64(0) {
		return 0, false
	}
	return epoch + 1, true
}

func isAmbiguousScannerRewindError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func scannerRewindErrorIsDefinitive(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.Status >= http.StatusBadRequest &&
		httpErr.Status < http.StatusInternalServerError
}

func (c *Client) fetchScannerContinuity(ctx context.Context) (scannerContinuityCache, error) {
	var state ScannerState
	if err := c.callGetContext(ctx, scannerStatePath, &state); err != nil {
		return scannerContinuityCache{}, err
	}
	return scannerContinuityFromState(state)
}

func scannerContinuityFromRegistration(resp RegisterVaultResp) (scannerContinuityCache, error) {
	if resp.ExpectedNextHeight == nil {
		return scannerContinuityCache{}, fmt.Errorf("expected_next_height is nil")
	}
	if *resp.ExpectedNextHeight > math.MaxInt64 {
		return scannerContinuityCache{}, fmt.Errorf("expected_next_height exceeds the int64 scanner range")
	}
	return scannerContinuityCache{
		resetEpoch:       resp.ResetEpoch,
		nextHeight:       *resp.ExpectedNextHeight,
		strictAfterReset: resp.ResetEpoch > 0,
	}, nil
}

func scannerContinuityFromState(state ScannerState) (scannerContinuityCache, error) {
	if state.NextHeight == nil {
		return scannerContinuityCache{}, fmt.Errorf("scanner state next_height is nil")
	}
	if *state.NextHeight > math.MaxInt64 {
		return scannerContinuityCache{}, fmt.Errorf("scanner state next_height exceeds the int64 scanner range")
	}
	if state.StrictAfterReset != (state.ResetEpoch > 0) {
		return scannerContinuityCache{}, fmt.Errorf("scanner state reset epoch and strict mode are inconsistent")
	}

	continuity := scannerContinuityCache{
		resetEpoch:       state.ResetEpoch,
		nextHeight:       *state.NextHeight,
		strictAfterReset: state.StrictAfterReset,
		hasStateDetails:  true,
	}
	if state.LastHeight == nil {
		if strings.TrimSpace(state.LastBlockHash) != "" || strings.TrimSpace(state.LastParentHash) != "" {
			return scannerContinuityCache{}, fmt.Errorf("scanner state has hashes without last_height")
		}
		return continuity, nil
	}
	if *state.LastHeight >= math.MaxInt64 {
		return scannerContinuityCache{}, fmt.Errorf("scanner state last_height cannot advance within the int64 scanner range")
	}
	if *state.NextHeight != *state.LastHeight+1 {
		return scannerContinuityCache{}, fmt.Errorf("scanner state next_height is not contiguous with last_height")
	}
	lastBlockHash, err := normalizeScannerHash("last_block_hash", state.LastBlockHash)
	if err != nil {
		return scannerContinuityCache{}, fmt.Errorf("invalid scanner state: %w", err)
	}
	lastParentHash, err := normalizeScannerHash("last_parent_hash", state.LastParentHash)
	if err != nil {
		return scannerContinuityCache{}, fmt.Errorf("invalid scanner state: %w", err)
	}

	continuity.hasLastHeight = true
	continuity.lastHeight = *state.LastHeight
	continuity.lastBlockHash = lastBlockHash
	continuity.lastParentHash = lastParentHash
	return continuity, nil
}

// normalizeScannerHash validates every hash crossing the sidecar boundary, in
// both directions. Outbound, anchor_hash is the compare-and-set token binding a
// durable cursor mutation to a specific chain and block_hash/parent_hash are what
// the sidecar uses to detect a continuity break, so they are checked here rather
// than trusting the sidecar to reject garbage.
func normalizeScannerHash(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return "", fmt.Errorf("%s must be a 32-byte hexadecimal hash", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("%s must be a 32-byte hexadecimal hash", field)
	}
	return strings.ToLower(value), nil
}

func (c *Client) cachedScannerContinuity() (scannerContinuityCache, bool) {
	c.scannerContinuityMu.Lock()
	defer c.scannerContinuityMu.Unlock()
	return c.scannerContinuity, c.continuityCached
}

// mergeScannerContinuity orders cursors by epoch first and height second. A
// higher epoch may legitimately move the height backwards; within one epoch,
// however, a delayed registration/state response may never lower the cursor.
func (c *Client) mergeScannerContinuity(candidate scannerContinuityCache) scannerContinuityCache {
	c.scannerContinuityMu.Lock()
	defer c.scannerContinuityMu.Unlock()

	if !c.continuityCached ||
		candidate.resetEpoch > c.scannerContinuity.resetEpoch ||
		(candidate.resetEpoch == c.scannerContinuity.resetEpoch &&
			candidate.nextHeight > c.scannerContinuity.nextHeight) {
		c.scannerContinuity = candidate
		c.continuityCached = true
		return c.scannerContinuity
	}
	if candidate.resetEpoch == c.scannerContinuity.resetEpoch &&
		candidate.nextHeight == c.scannerContinuity.nextHeight {
		c.scannerContinuity.strictAfterReset = c.scannerContinuity.strictAfterReset ||
			candidate.strictAfterReset
		if candidate.hasStateDetails {
			c.scannerContinuity.hasStateDetails = true
			c.scannerContinuity.hasLastHeight = candidate.hasLastHeight
			c.scannerContinuity.lastHeight = candidate.lastHeight
			c.scannerContinuity.lastBlockHash = candidate.lastBlockHash
			c.scannerContinuity.lastParentHash = candidate.lastParentHash
		}
	}
	return c.scannerContinuity
}

func bindScannerContinuity(req *SyncReq, resetEpoch, expectedHeight uint64) {
	epoch := resetEpoch
	expected := expectedHeight
	req.ResetEpoch = &epoch
	req.ExpectedNextHeight = &expected
}

func (c *Client) cacheSuccessfulScannerSync(req SyncReq) {
	resetEpoch := uint64(0)
	if req.ResetEpoch != nil {
		resetEpoch = *req.ResetEpoch
	}
	height := uint64(req.Height)
	c.mergeScannerContinuity(scannerContinuityCache{
		resetEpoch:       resetEpoch,
		nextHeight:       height + 1,
		strictAfterReset: resetEpoch > 0,
		hasStateDetails:  true,
		hasLastHeight:    true,
		lastHeight:       height,
		lastBlockHash:    req.BlockHash,
		lastParentHash:   req.ParentHash,
	})
}

func scannerContinuityIsExactReplay(
	continuity scannerContinuityCache,
	requestedHeight uint64,
	blockHash,
	parentHash string,
) bool {
	return continuity.hasStateDetails &&
		continuity.hasLastHeight &&
		continuity.nextHeight == requestedHeight+1 &&
		continuity.lastHeight == requestedHeight &&
		continuity.lastBlockHash == blockHash &&
		continuity.lastParentHash == parentHash
}

func scannerRewindError(requestedHeight int64, continuity scannerContinuityCache) error {
	return &ScannerRewindRequiredError{
		RequestedHeight: requestedHeight,
		NextHeight:      int64(continuity.nextHeight),
		ResetEpoch:      continuity.resetEpoch,
	}
}

func (c *Client) Mempool() (Transfers, error) {
	var out Transfers
	path := fmt.Sprintf("%s?mempool=true", c.transfersPath)
	if err := c.callGet(path, &out); err != nil {
		return Transfers{}, fmt.Errorf("fail to call mempool transfers endpoint: %w", err)
	}
	return out, nil
}

func (c *Client) Addresses(vaultPubKey string) ([]string, error) {
	var out Addresses
	path := c.addressesPath + "/" + url.PathEscape(vaultPubKey)
	if err := c.callGet(path, &out); err != nil {
		return nil, fmt.Errorf("fail to call addresses endpoint: %w", err)
	}
	return out.Addresses, nil
}

func (c *Client) Outputs(vaultPubKey string) ([]Output, error) {
	var out Outputs
	path := c.outputsPath + "/" + url.PathEscape(vaultPubKey)
	if err := c.callGet(path, &out); err != nil {
		return nil, fmt.Errorf("fail to call outputs endpoint: %w", err)
	}
	return out.Outputs, nil
}

func (c *Client) FeeRate() (uint64, error) {
	var out FeeRate
	if err := c.callGet(c.feeRatePath, &out); err != nil {
		return 0, fmt.Errorf("fail to call fee-rate endpoint: %w", err)
	}
	return out.FeeRatePerWeight, nil
}

func (c *Client) ResetScanner(vaultPubKey string, birthdayHeight int64) error {
	if birthdayHeight < 0 {
		return fmt.Errorf("scanner reset birthday height must be non-negative")
	}
	req := struct {
		VaultPubKey    string `json:"vault_pub_key"`
		BirthdayHeight int64  `json:"birthday_height"`
	}{vaultPubKey, birthdayHeight}
	var out ScannerResetResp
	if err := c.call(http.MethodPost, "/v1/monero/scanner/reset", req, &out); err != nil {
		return err
	}
	if !out.Reset || out.ResetEpoch == 0 || out.ExpectedNextHeight != uint64(birthdayHeight) ||
		out.ExpectedNextHeight > math.MaxInt64 {
		return fmt.Errorf("scanner reset returned an invalid continuity cursor")
	}
	c.mergeScannerContinuity(scannerContinuityCache{
		resetEpoch:       out.ResetEpoch,
		nextHeight:       out.ExpectedNextHeight,
		strictAfterReset: true,
	})
	return nil
}

func (c *Client) ChainHeight() (int64, error) {
	var daemonErr error
	if c.hasDaemonRPC() {
		height, err := c.daemonHeight()
		if err == nil {
			return height, nil
		}
		daemonErr = fmt.Errorf("fail to call monero daemon chain-height rpc: %w", err)
	}

	var out ChainHeightResp
	if err := c.callGet(c.chainHeightPath, &out); err != nil {
		if daemonErr != nil {
			return 0, fmt.Errorf("%v; fail to call sidecar chain-height endpoint: %w", daemonErr, err)
		}
		return 0, fmt.Errorf("fail to call sidecar chain-height endpoint: %w", err)
	}
	return out.Height, nil
}

func (c *Client) ChainBlock(height int64) (ChainBlock, error) {
	if height < 0 {
		return ChainBlock{}, fmt.Errorf("height must be >= 0")
	}

	var daemonErr error
	if c.hasDaemonRPC() {
		block, err := c.daemonBlock(height)
		if err == nil {
			return validateChainBlockHeight(height, block)
		}
		if isUnavailableBlock(err) {
			return ChainBlock{}, err
		}
		daemonErr = fmt.Errorf("fail to call monero daemon chain-block rpc: %w", err)
	}

	var out ChainBlockResp
	path := fmt.Sprintf("%s/%d", c.chainBlockPath, height)
	if err := c.callGet(path, &out); err != nil {
		if IsHTTPStatus(err, http.StatusConflict) {
			return ChainBlock{}, fmt.Errorf("%w: %d", ErrUnavailableBlock, height)
		}
		if daemonErr != nil {
			return ChainBlock{}, fmt.Errorf("%v; fail to call sidecar chain-block endpoint: %w", daemonErr, err)
		}
		return ChainBlock{}, fmt.Errorf("fail to call sidecar chain-block endpoint: %w", err)
	}
	return validateChainBlockHeight(height, ChainBlock(out))
}

func validateChainBlockHeight(requestedHeight int64, block ChainBlock) (ChainBlock, error) {
	if block.Height != requestedHeight {
		return ChainBlock{}, &ChainBlockHeightMismatchError{
			RequestedHeight: requestedHeight,
			ReturnedHeight:  block.Height,
		}
	}
	return block, nil
}

func (c *Client) TxExists(txHash string) (bool, error) {
	normalized := strings.TrimSpace(txHash)
	if normalized == "" {
		return false, fmt.Errorf("tx hash is empty")
	}

	var out TxExistsResp
	path := fmt.Sprintf("%s/%s/exists", c.chainTxPath, url.PathEscape(normalized))
	if err := c.callGet(path, &out); err != nil {
		return false, fmt.Errorf("fail to call chain tx-exists endpoint: %w", err)
	}
	return out.Exists, nil
}

// CanonicalTxLocation asks the authenticated sidecar to locate one mined
// transaction and bind it to a daemon-verified canonical block. It is used to
// recover from a stale observation height after a Monero reorg without a
// caller-selected historical scan.
func (c *Client) CanonicalTxLocation(txHash string) (CanonicalTxLocationResp, error) {
	normalized := strings.ToLower(strings.TrimSpace(txHash))
	if len(normalized) != 64 {
		return CanonicalTxLocationResp{}, fmt.Errorf("canonical tx location hash must be 32-byte hex")
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return CanonicalTxLocationResp{}, fmt.Errorf("canonical tx location hash must be 32-byte hex")
	}

	var out CanonicalTxLocationResp
	path := fmt.Sprintf("%s/%s/location", c.chainTxPath, url.PathEscape(normalized))
	if err := c.callGet(path, &out); err != nil {
		return CanonicalTxLocationResp{}, fmt.Errorf("fail to locate canonical monero transaction: %w", err)
	}
	if normalizeHex := strings.ToLower(strings.TrimSpace(out.TxHash)); normalizeHex != normalized {
		return CanonicalTxLocationResp{}, fmt.Errorf("canonical tx location returned a different transaction")
	}
	if out.Height <= 0 {
		return CanonicalTxLocationResp{}, fmt.Errorf("canonical tx location returned a non-positive height")
	}
	for label, value := range map[string]string{
		"block hash":  out.BlockHash,
		"parent hash": out.PreviousHash,
	} {
		value = strings.ToLower(strings.TrimSpace(value))
		if len(value) != 64 {
			return CanonicalTxLocationResp{}, fmt.Errorf("canonical tx location returned invalid %s", label)
		}
		if _, err := hex.DecodeString(value); err != nil {
			return CanonicalTxLocationResp{}, fmt.Errorf("canonical tx location returned invalid %s", label)
		}
		switch label {
		case "block hash":
			out.BlockHash = value
		case "parent hash":
			out.PreviousHash = value
		}
	}
	out.TxHash = normalized
	return out, nil
}

// MarkSpent records that outputs were consumed by a particular spend tx.
func (c *Client) MarkSpent(vaultPubKey, txHash string, refs []SpentRef, skipVerification bool, outbound *OutboundMeta) (MarkSpentResp, error) {
	var out MarkSpentResp
	req := MarkSpentReq{
		VaultPubKey:      vaultPubKey,
		TxHash:           txHash,
		SpentRefs:        refs,
		SkipVerification: skipVerification,
		// Imported records remain accounting-only until scanner observation or
		// the canonical mark-observed endpoint independently proves them mined.
		Origin: OutgoingOriginChainCommitted,
	}
	if outbound != nil {
		req.FromAddress = outbound.FromAddress
		req.ToAddress = outbound.ToAddress
		req.TxKey = outbound.TxKey
		req.Amount = outbound.Amount
		req.Fee = outbound.Fee
		req.Memo = outbound.Memo
		req.Origin = outbound.Origin
		req.CertificateHash = outbound.CertificateHash
	}
	if err := c.call(http.MethodPost, "/v1/monero/outputs/mark-spent", req, &out); err != nil {
		return out, fmt.Errorf("fail to call mark-spent endpoint: %w", err)
	}
	return out, nil
}

// MarkObserved reconciles an existing MarkSpent-authenticated outgoing against
// an exact canonical Monero block and returns the durable outgoing proof. The
// endpoint is idempotent only for the same height and independently checks the
// supplied block hash and transaction membership on every call. Deployment of
// this Thornode path therefore requires a sidecar image advertising
// canonical-outgoing-observation-v1 (introduced with the sidecar !62 follow-up).
func (c *Client) MarkObserved(vaultPubKey, txHash string, height int64, blockHash string) (Transfer, error) {
	if strings.TrimSpace(vaultPubKey) == "" {
		return Transfer{}, fmt.Errorf("mark-observed vault pubkey is empty")
	}
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	blockHash = strings.ToLower(strings.TrimSpace(blockHash))
	if height <= 0 {
		return Transfer{}, fmt.Errorf("mark-observed height must be positive")
	}
	if len(txHash) != 64 {
		return Transfer{}, fmt.Errorf("mark-observed tx hash must be canonical 32-byte hex")
	}
	if _, err := hex.DecodeString(txHash); err != nil {
		return Transfer{}, fmt.Errorf("mark-observed tx hash must be canonical 32-byte hex")
	}
	if len(blockHash) != 64 {
		return Transfer{}, fmt.Errorf("mark-observed block hash must be canonical 32-byte hex")
	}
	if _, err := hex.DecodeString(blockHash); err != nil {
		return Transfer{}, fmt.Errorf("mark-observed block hash must be canonical 32-byte hex")
	}

	var out Transfer
	req := MarkObservedReq{
		VaultPubKey: strings.TrimSpace(vaultPubKey),
		TxHash:      txHash,
		Height:      uint64(height),
		BlockHash:   blockHash,
	}
	if err := c.call(http.MethodPost, c.markObservedPath, req, &out); err != nil {
		return Transfer{}, fmt.Errorf("fail to call mark-observed endpoint: %w", err)
	}
	return out, nil
}

// Vaults lists every vault the sidecar knows about and whether it holds a
// threshold share for it.
func (c *Client) Vaults() ([]VaultKey, error) {
	return c.VaultsContext(context.Background())
}

func (c *Client) VaultsContext(ctx context.Context) ([]VaultKey, error) {
	var out struct {
		Vaults []VaultKey `json:"vaults"`
	}
	if err := c.callGetContext(ctx, "/v1/monero/keys", &out); err != nil {
		return nil, fmt.Errorf("fail to list vault keys: %w", err)
	}
	return out.Vaults, nil
}

// Info returns the signer's authenticated compatibility contract.
func (c *Client) Info() (Info, error) {
	var out Info
	if err := c.callGet("/v1/monero/info", &out); err != nil {
		return Info{}, fmt.Errorf("fail to get sidecar compatibility info: %w", err)
	}
	return out, nil
}

// RequireCompatibility fails closed when the local signer is not the reviewed
// protocol/schema/capability shape. The immutable image digest and provenance
// are still verified by deployment tooling rather than this self-report.
func (c *Client) RequireCompatibility(requiredCapabilities ...string) (Info, error) {
	if c.expectedNetwork == "" {
		return Info{}, fmt.Errorf("expected sidecar network is not configured")
	}
	if c.expectedMoneroNettype == "" || c.expectedMoneroGenesisHash == "" {
		return Info{}, fmt.Errorf("expected Monero chain network is not configured")
	}
	info, err := c.Info()
	if err != nil {
		return Info{}, err
	}
	if info.Protocol != ProtocolName {
		return Info{}, fmt.Errorf("unexpected sidecar protocol %q", info.Protocol)
	}
	if info.APIMajor != APIMajorV2 {
		return Info{}, fmt.Errorf("unsupported sidecar api major %d", info.APIMajor)
	}
	if info.StateSchema != StateSchemaV4 {
		return Info{}, fmt.Errorf("unsupported sidecar state schema %d", info.StateSchema)
	}
	if info.Network != c.expectedNetwork {
		return Info{}, fmt.Errorf("sidecar network %q does not match expected %q", info.Network, c.expectedNetwork)
	}
	actualNettype := strings.ToLower(strings.TrimSpace(info.MoneroNettype))
	if actualNettype != c.expectedMoneroNettype {
		return Info{}, fmt.Errorf(
			"sidecar Monero nettype %q does not match expected %q",
			actualNettype,
			c.expectedMoneroNettype,
		)
	}
	actualGenesisHash := strings.ToLower(strings.TrimSpace(info.MoneroGenesisHash))
	decodedGenesisHash, decodeGenesisErr := hex.DecodeString(actualGenesisHash)
	if decodeGenesisErr != nil || len(decodedGenesisHash) != 32 {
		return Info{}, fmt.Errorf("sidecar Monero genesis hash must be 64 hex characters")
	}
	if actualGenesisHash != c.expectedMoneroGenesisHash {
		return Info{}, fmt.Errorf(
			"sidecar Monero genesis hash %q does not match expected %q",
			actualGenesisHash,
			c.expectedMoneroGenesisHash,
		)
	}
	commit := strings.TrimSpace(info.GitCommit)
	decodedCommit, decodeErr := hex.DecodeString(commit)
	if decodeErr != nil || len(decodedCommit) != 20 {
		return Info{}, fmt.Errorf("sidecar source revision must be a full 40-hex commit")
	}
	for _, capability := range requiredCapabilities {
		if !info.HasCapability(capability) {
			return Info{}, fmt.Errorf("sidecar missing required capability %q", capability)
		}
	}
	return info, nil
}

// ExportKeys returns a recovery blob for the given vault's threshold share.
func (c *Client) ExportKeys(vaultPubKey string) (string, error) {
	var out struct {
		KeyBytesHex string `json:"key_bytes_hex"`
	}
	path := fmt.Sprintf("/v1/monero/keys/%s/export", url.PathEscape(vaultPubKey))
	if err := c.callGet(path, &out); err != nil {
		return "", fmt.Errorf("fail to export keys: %w", err)
	}
	return out.KeyBytesHex, nil
}

// RestoreKeys imports a recovery blob back into the sidecar. The blob is the
// raw on-disk recovery file, which may be at-rest encrypted (binary). It is
// base64-encoded for transport because raw bytes are not valid UTF-8 and would
// be mangled by JSON string encoding.
func (c *Client) RestoreKeys(vaultPubKey string, keyBlob []byte) (bool, error) {
	return c.RestoreKeysContext(context.Background(), vaultPubKey, keyBlob)
}

func (c *Client) RestoreKeysContext(ctx context.Context, vaultPubKey string, keyBlob []byte) (bool, error) {
	var out struct {
		Restored *bool `json:"restored"`
	}
	req := struct {
		VaultPubKey string `json:"vault_pub_key"`
		KeyBlobB64  string `json:"key_blob_b64"`
	}{
		VaultPubKey: vaultPubKey,
		KeyBlobB64:  base64.StdEncoding.EncodeToString(keyBlob),
	}
	if err := c.callContext(ctx, http.MethodPost, "/v1/monero/keys/restore", req, &out); err != nil {
		return false, fmt.Errorf("fail to restore keys: %w", err)
	}
	if out.Restored == nil {
		return false, fmt.Errorf("restore response omitted required restored state")
	}
	if !*out.Restored {
		return false, fmt.Errorf("sidecar did not restore keys for vault %s", vaultPubKey)
	}
	return true, nil
}

// --- HTTP helpers ---

func sidecarCallError(path string, err error) error {
	if errors.Is(err, errSignerRedirect) {
		// net/http wraps redirect rejection with the untrusted Location value,
		// which may contain credentials. Keep only the local rejection reason.
		err = errSignerRedirect
	}
	return fmt.Errorf("fail to call sidecar endpoint %s: %w", path, err)
}

func (c *Client) callGet(path string, out any) error {
	return c.callGetContext(context.Background(), path, out)
}

func (c *Client) callGetContext(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return fmt.Errorf("fail to create sidecar request: %w", err)
	}
	c.applySignerAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return sidecarCallError(path, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// net/http skips CheckRedirect when a redirect has no Location header.
	if resp.StatusCode >= http.StatusMultipleChoices {
		respBody, truncated, readErr := readBoundedErrorBody(resp.Body)
		if readErr != nil {
			return fmt.Errorf("fail to read sidecar error response: %w", readErr)
		}
		return newHTTPError(path, resp.StatusCode, respBody, truncated)
	}
	respBody, err := readBoundedResponseBody(resp.Body, maxResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read sidecar response: %w", err)
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("sidecar endpoint %s returned empty response body", path)
	}

	var env envelope
	if err = json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("fail to unmarshal sidecar response: %w", err)
	}
	if env.Error != "" || env.Message != "" {
		msg := strings.TrimSpace(env.Error)
		if msg == "" {
			msg = strings.TrimSpace(env.Message)
		}
		return remoteApplicationError(path, msg)
	}
	if len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return json.Unmarshal(respBody, out)
}

func (c *Client) call(method, path string, payload any, out any) error {
	return c.callContext(context.Background(), method, path, payload, out)
}

func (c *Client) callContext(ctx context.Context, method, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fail to marshal sidecar request payload: %w", err)
	}
	return c.callJSONContext(ctx, method, path, body, out)
}

func (c *Client) callJSONContext(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fail to create sidecar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.applySignerAuth(req)

	httpClient := c.client
	if (path == c.signPreparePath || path == c.signPartialPath || path == c.signCombinePath) && c.signClient != nil {
		httpClient = c.signClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return sidecarCallError(path, err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// net/http skips CheckRedirect when a redirect has no Location header.
	if resp.StatusCode >= http.StatusMultipleChoices {
		respBody, truncated, readErr := readBoundedErrorBody(resp.Body)
		if readErr != nil {
			return fmt.Errorf("fail to read sidecar error response: %w", readErr)
		}
		return newHTTPError(path, resp.StatusCode, respBody, truncated)
	}
	respBody, err := readBoundedResponseBody(resp.Body, maxResponseBytes)
	if err != nil {
		return fmt.Errorf("fail to read sidecar response: %w", err)
	}
	if out == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("sidecar endpoint %s returned empty response body", path)
	}

	// Try envelope first: json.Unmarshal succeeds on any valid JSON regardless
	// of field matches, so we cannot rely on direct-unmarshal failure to detect
	// envelope-style responses.
	var env envelope
	if err = json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("fail to unmarshal sidecar response: %w", err)
	}
	if env.Error != "" || env.Message != "" {
		msg := strings.TrimSpace(env.Error)
		if msg == "" {
			msg = strings.TrimSpace(env.Message)
		}
		return remoteApplicationError(path, msg)
	}
	if len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return json.Unmarshal(respBody, out)
}

func (c *Client) applySignerAuth(req *http.Request) {
	if c == nil || req == nil {
		return
	}
	if c.signerBearer != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.signerBearer))
		return
	}
	if c.signerUsername != "" {
		req.SetBasicAuth(c.signerUsername, c.signerPassword)
	}
}
