package monero

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"
	"filippo.io/edwards25519"
	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	defaultHelperEndpoint = "http://localhost:8284"
	defaultFrostEndpoint  = "http://localhost:8283"

	defaultHelperBearer = "xmr-sim-helper-mocknet-token"
	defaultFrostBearer  = "xmr-frost-mocknet-token"

	xmrAtomicFactor            = uint64(10_000) // 1e12 atomic units => 1e8 simulation units
	xmrFaucetRetryAttempts     = 30
	xmrFaucetRetryDelay        = 2 * time.Second
	xmrFaucetStartupErrMarker  = "Unexpected hard fork version"
	xmrFaucetUnlockedErrMarker = "not enough unlocked money"
)

type Client struct {
	chain common.Chain

	address        common.Address
	spendKeyHex    string
	birthdayHeight *uint64

	helperEndpoint string
	helperBearer   string
	frostEndpoint  string
	frostBearer    string

	httpClient *http.Client
}

type helperBalanceRequest struct {
	SpendKeyHex    string  `json:"spend_key_hex"`
	BirthdayHeight *uint64 `json:"birthday_height,omitempty"`
}

type helperBalanceResponse struct {
	Address               string `json:"address"`
	BalanceAtomic         uint64 `json:"balance_atomic"`
	UnlockedBalanceAtomic uint64 `json:"unlocked_balance_atomic"`
}

type helperTransferRequest struct {
	SpendKeyHex    string  `json:"spend_key_hex"`
	BirthdayHeight *uint64 `json:"birthday_height,omitempty"`
	ToAddress      string  `json:"to_address"`
	AmountAtomic   uint64  `json:"amount_atomic"`
	Memo           string  `json:"memo,omitempty"`
}

type helperTransferResponse struct {
	TxHash string `json:"tx_hash"`
}

type chainHeightResponse struct {
	Height uint64 `json:"height"`
}

type helperFaucetRequest struct {
	ToAddress    string `json:"to_address"`
	AmountAtomic uint64 `json:"amount_atomic"`
}

type frostBalanceResponse struct {
	Balance          uint64  `json:"balance"`
	UnlockedBalance  uint64  `json:"unlocked_balance"`
	SpendableBalance *uint64 `json:"spendable_balance,omitempty"`
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, helperEndpoint string, keys *thorclient.Keys) (LiteChainClient, error) {
	if !chain.Equals(common.XMRChain) {
		return nil, fmt.Errorf("unsupported chain: %s", chain)
	}

	spendKeyHex, address, err := spendKeyHexAndAddress(keys)
	if err != nil {
		return nil, err
	}

	if helperEndpoint == "" {
		helperEndpoint = defaultHelperEndpoint
	}

	return &Client{
		chain:          chain,
		address:        address,
		spendKeyHex:    spendKeyHex,
		helperEndpoint: strings.TrimRight(helperEndpoint, "/"),
		helperBearer:   envOrDefault("XMR_SIM_HELPER_AUTH_BEARER", defaultHelperBearer),
		frostEndpoint:  strings.TrimRight(envOrDefault("XMR_FROST_ENDPOINT", defaultFrostEndpoint), "/"),
		frostBearer:    envOrDefault("XMR_FROST_AUTH_BEARER", defaultFrostBearer),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	if pk != nil {
		totalBalance, _, err := c.GetVaultBalance(*pk)
		if err != nil {
			return nil, err
		}
		return &common.Account{
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, totalBalance)),
		}, nil
	}

	var response helperBalanceResponse
	if err := c.postJSON(
		c.helperEndpoint+"/v1/monero/sim/balance",
		c.helperBearer,
		helperBalanceRequest{
			SpendKeyHex:    c.spendKeyHex,
			BirthdayHeight: c.birthdayHeight,
		},
		&response,
	); err != nil {
		return nil, err
	}

	if response.Address != "" && !common.Address(response.Address).Equals(c.address) {
		return nil, fmt.Errorf("unexpected helper address %s for XMR simulation account", response.Address)
	}

	return &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, fromAtomic(response.UnlockedBalanceAtomic))),
	}, nil
}

func (c *Client) GetHeight() (int64, error) {
	var response chainHeightResponse
	if err := c.getJSON(c.helperEndpoint+"/v1/monero/chain/height", c.helperBearer, &response); err != nil {
		return 0, err
	}

	const maxInt64 = uint64(1<<63 - 1)
	if response.Height > maxInt64 {
		return 0, fmt.Errorf("monero height %d exceeds int64 range", response.Height)
	}

	return int64(response.Height), nil
}

func (c *Client) SetBirthdayHeight(height uint64) {
	c.birthdayHeight = &height
}

func (c *Client) GetVaultBalance(pk common.PubKey) (sdkmath.Uint, sdkmath.Uint, error) {
	var response frostBalanceResponse
	path := fmt.Sprintf("%s/v1/monero/balance/%s", c.frostEndpoint, url.PathEscape(pk.String()))
	if err := c.getJSON(path, c.frostBearer, &response); err != nil {
		return sdkmath.ZeroUint(), sdkmath.ZeroUint(), err
	}

	return fromAtomic(response.Balance), fromAtomic(spendableBalanceAtomic(response)), nil
}

func spendableBalanceAtomic(balance frostBalanceResponse) uint64 {
	availableAtomic := balance.UnlockedBalance
	if availableAtomic > balance.Balance {
		availableAtomic = balance.Balance
	}
	if balance.SpendableBalance != nil {
		availableAtomic = *balance.SpendableBalance
		if availableAtomic > balance.Balance {
			availableAtomic = balance.Balance
		}
	}
	return availableAtomic
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	if !tx.Chain.Equals(common.XMRChain) {
		return nil, fmt.Errorf("unsupported chain: %s", tx.Chain)
	}
	if !tx.Coin.Asset.Equals(common.XMRAsset) {
		return nil, fmt.Errorf("unsupported asset: %s", tx.Coin.Asset)
	}

	return json.Marshal(helperTransferRequest{
		SpendKeyHex:    c.spendKeyHex,
		BirthdayHeight: c.birthdayHeight,
		ToAddress:      tx.ToAddress.String(),
		AmountAtomic:   toAtomic(tx.Coin.Amount),
		Memo:           tx.Memo,
	})
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	var request helperTransferRequest
	if err := json.Unmarshal(signed, &request); err != nil {
		return "", fmt.Errorf("failed to parse simulated Monero transfer request: %w", err)
	}

	var response helperTransferResponse
	if err := c.postJSON(
		c.helperEndpoint+"/v1/monero/sim/transfer",
		c.helperBearer,
		request,
		&response,
	); err != nil {
		return "", err
	}

	return response.TxHash, nil
}

func (c *Client) Faucet(toAddress common.Address, amount sdkmath.Uint) (string, error) {
	req := helperFaucetRequest{
		ToAddress:    toAddress.String(),
		AmountAtomic: toAtomic(amount),
	}

	var lastErr error
	for attempt := 1; attempt <= xmrFaucetRetryAttempts; attempt++ {
		var response helperTransferResponse
		err := c.postJSON(
			c.helperEndpoint+"/v1/monero/sim/faucet",
			c.helperBearer,
			req,
			&response,
		)
		if err == nil {
			return response.TxHash, nil
		}

		lastErr = err
		if !isRetryableFaucetError(err) || attempt == xmrFaucetRetryAttempts {
			return "", err
		}

		time.Sleep(xmrFaucetRetryDelay)
	}

	return "", lastErr
}

func (c *Client) getJSON(target, bearer string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeResponse(resp, out)
}

func (c *Client) postJSON(target, bearer string, in, out interface{}) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeResponse(resp, out)
}

func decodeResponse(resp *http.Response, out interface{}) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		method := "<unknown>"
		target := "<unknown>"
		if resp.Request != nil {
			method = resp.Request.Method
			if resp.Request.URL != nil {
				target = resp.Request.URL.Redacted()
			}
		}
		return fmt.Errorf("http %d from %s %s: %s", resp.StatusCode, method, target, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func isRetryableFaucetError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, xmrFaucetStartupErrMarker) ||
		strings.Contains(message, xmrFaucetUnlockedErrMarker)
}

func spendKeyHexAndAddress(keys *thorclient.Keys) (string, common.Address, error) {
	privKey, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return "", "", fmt.Errorf("failed to get Ed25519 private key: %w", err)
	}

	privBytes := privKey.Bytes()
	if len(privBytes) < 32 {
		return "", "", fmt.Errorf("invalid Ed25519 private key length: %d", len(privBytes))
	}
	spendKeyHex := hex.EncodeToString(privBytes[:32])

	pk, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		return "", "", fmt.Errorf("failed to derive Ed25519 public key: %w", err)
	}
	expectedPubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return "", "", fmt.Errorf("failed to derive THORChain pubkey: %w", err)
	}
	expectedAddress, err := expectedPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return "", "", fmt.Errorf("failed to derive XMR address from THORChain pubkey: %w", err)
	}

	derivedAddress, err := addressFromSpendKeyHex(spendKeyHex)
	if err != nil {
		return "", "", err
	}
	if !derivedAddress.Equals(expectedAddress) {
		return "", "", fmt.Errorf("derived XMR address %s does not match expected %s", derivedAddress, expectedAddress)
	}

	return spendKeyHex, expectedAddress, nil
}

func addressFromSpendKeyHex(spendKeyHex string) (common.Address, error) {
	spendKeyBytes, err := hex.DecodeString(spendKeyHex)
	if err != nil {
		return "", fmt.Errorf("failed to decode XMR spend key: %w", err)
	}
	if len(spendKeyBytes) != 32 {
		return "", fmt.Errorf("invalid XMR spend key length: %d", len(spendKeyBytes))
	}

	expanded := sha512.Sum512(spendKeyBytes)
	spendScalar, err := new(edwards25519.Scalar).SetBytesWithClamping(expanded[:32])
	if err != nil {
		return "", fmt.Errorf("failed to expand XMR spend key: %w", err)
	}
	pubKeyBytes := new(edwards25519.Point).ScalarBaseMult(spendScalar).Bytes()

	pubKey, err := common.NewPubKeyFromCrypto(cmted25519.PubKey(pubKeyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to derive XMR pubkey from expanded spend key: %w", err)
	}

	address, err := pubKey.GetAddress(common.XMRChain)
	if err != nil {
		return "", fmt.Errorf("failed to derive XMR address from expanded spend key: %w", err)
	}

	return address, nil
}

func toAtomic(amount sdkmath.Uint) uint64 {
	return amount.MulUint64(xmrAtomicFactor).Uint64()
}

func fromAtomic(amount uint64) sdkmath.Uint {
	return sdkmath.NewUint(amount).QuoUint64(xmrAtomicFactor)
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
