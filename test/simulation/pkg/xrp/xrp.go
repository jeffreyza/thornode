package xrp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/keymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"

	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/Peersyst/xrpl-go/xrpl/hash"
	"github.com/Peersyst/xrpl-go/xrpl/queries/account"
	qcommon "github.com/Peersyst/xrpl-go/xrpl/queries/common"
	txqueries "github.com/Peersyst/xrpl-go/xrpl/queries/transactions"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	transactions "github.com/Peersyst/xrpl-go/xrpl/transaction"
	txtypes "github.com/Peersyst/xrpl-go/xrpl/transaction/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// XRP
////////////////////////////////////////////////////////////////////////////////////////

const (
	xrpRPCTimeout             = 10 * time.Second
	xrpValidationPollInterval = time.Second
	xrpValidationTimeout      = 90 * time.Second
)

type Client struct {
	chain     common.Chain
	keys      *thorclient.Keys
	localKm   *keymanager.KeyManager
	rpcClient *rpc.Client
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	// extract the private key
	privKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	localKm, err := keymanager.NewKeyManager(privKey)
	if err != nil {
		return nil, fmt.Errorf("fail to create key manager: %w", err)
	}
	rpcConfig, err := rpc.NewClientConfig(host, rpc.WithHTTPClient(&http.Client{Timeout: xrpRPCTimeout}))
	if err != nil {
		return nil, fmt.Errorf("unable to create rpc config for client, %w", err)
	}
	rpcClient := rpc.NewClient(rpcConfig)
	rpcClient.NetworkID = 1234

	return &Client{
		chain:     chain,
		keys:      keys,
		localKm:   localKm,
		rpcClient: rpcClient,
	}, nil
}

// GetHeight returns the current XRPL ledger so mocknet does not seed
// XRP's last-chain-height at ledger 1, which the standalone daemon cannot
// necessarily replay for late-starting Bifrost scanners.
func (c *Client) GetHeight() (int64, error) {
	ledgerIndex, err := c.rpcClient.GetLedgerIndex()
	if err != nil {
		return 0, err
	}

	return int64(ledgerIndex.Int()), nil
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	address := c.localKm.AccountID
	if pk != nil {
		addr, err := pk.GetAddress(common.XRPChain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
		address = addr.String()
	}

	aiReq := account.InfoRequest{
		Account:     txtypes.Address(address),
		LedgerIndex: qcommon.Current, // Query current/non-closed/non-validated ledger
	}
	aiResp, err := c.rpcClient.GetAccountInfo(&aiReq)
	if err != nil {
		if isAccountNotFoundXRPError(err) {
			return &common.Account{
				Sequence:      0,
				AccountNumber: 0,
				Coins: common.NewCoins(
					common.NewCoin(common.XRPAsset, sdkmath.ZeroUint()),
				),
			}, nil
		}
		return nil, err
	}

	balance := sdkmath.NewUint(aiResp.AccountData.Balance.Uint64()).MulUint64(100) // 1e6 -> 1e8
	coins := common.NewCoins(common.NewCoin(common.XRPAsset, balance))

	return &common.Account{
		Sequence:      int64(aiResp.AccountData.Sequence),
		AccountNumber: 0,
		Coins:         coins,
	}, nil
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// get account
	account, err := c.GetAccount(nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get account: %w", err)
	}

	// create message
	payment := transactions.Payment{
		BaseTx: transactions.BaseTx{
			Account: txtypes.Address(c.localKm.AccountID),
			// NetworkID:     c.chainID, // Currently doesn't do anything, but the client with populate
			SigningPubKey: c.localKm.PublicKeyHex,
			Sequence:      uint32(account.Sequence),
			Fee:           txtypes.XRPCurrencyAmount(10),
		},
		Amount:      txtypes.XRPCurrencyAmount(tx.Coin.Amount.QuoUint64(100).Uint64()), // 1e8 -> 1e6
		Destination: txtypes.Address(tx.ToAddress),
	}
	if tx.Memo != "" {
		payment.BaseTx.Memos = []txtypes.MemoWrapper{
			{
				Memo: txtypes.Memo{
					MemoData: hex.EncodeToString([]byte(tx.Memo)),
				},
			},
		}
	}

	// sign transaction
	flatTx := payment.Flatten()
	if err := c.rpcClient.Autofill(&flatTx); err != nil {
		return nil, err
	}
	encodedTx, err := binarycodec.EncodeForSigning(flatTx)
	if err != nil {
		return nil, err
	}
	signBytes, err := hex.DecodeString(encodedTx)
	if err != nil {
		return nil, err
	}
	derSignature, err := c.localKm.Sign(signBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to sign using localKeyManager: %w", err)
	}
	flatTx["TxnSignature"] = hex.EncodeToString(derSignature) // use flatTx so we don't need to call autofill again

	// Ensure the signature is valid
	verified, err := c.localKm.Keys.Verify(signBytes, derSignature)
	if err != nil {
		return nil, fmt.Errorf("error verifying signature: %w", err)
	}
	if !verified {
		return nil, fmt.Errorf("unable to verify signature with secpPubKey")
	}

	txHex, err := binarycodec.Encode(flatTx)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(txHex)
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	txBlob := hex.EncodeToString(signed)
	tx, err := binarycodec.Decode(txBlob)
	if err != nil {
		return "", err
	}

	lastLedgerSequence, ok := tx["LastLedgerSequence"].(uint32)
	if !ok {
		return "", fmt.Errorf("xrp tx missing LastLedgerSequence: %T", tx["LastLedgerSequence"])
	}

	txHash, err := hash.SignTxBlob(txBlob)
	if err != nil {
		return "", err
	}

	if err := c.submitTx(txBlob, txHash, lastLedgerSequence); err != nil {
		return "", err
	}

	if err := c.waitForValidation(txHash, lastLedgerSequence); err != nil {
		return "", err
	}

	return txHash, nil
}

func (c *Client) submitTx(txBlob, txHash string, lastLedgerSequence uint32) error {
	deadline := time.Now().Add(xrpValidationTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := c.rpcClient.Submit(txBlob, true)
		switch {
		case err == nil && isAcceptedXRPSubmitResponse(resp):
			return nil
		case err == nil:
			return fmt.Errorf(
				"transaction failed to submit with engine result: %s (%s)",
				resp.EngineResult,
				resp.EngineResultMessage,
			)
		case isRetryableXRPError(err):
			lastErr = err

			txResp, txErr := c.getTx(txHash)
			switch {
			case txErr == nil && txResp != nil:
				return nil
			case txErr != nil && !isRetryableXRPError(txErr):
				return txErr
			case txErr != nil:
				lastErr = txErr
			}
		default:
			return err
		}

		currentLedger, err := c.rpcClient.GetLedgerIndex()
		switch {
		case err == nil && currentLedger.Int() > int(lastLedgerSequence):
			if lastErr != nil {
				return fmt.Errorf(
					"transaction %s was not accepted before ledger %d: %w",
					txHash,
					lastLedgerSequence,
					lastErr,
				)
			}
			return fmt.Errorf("transaction %s was not accepted before ledger %d", txHash, lastLedgerSequence)
		case err != nil && !isRetryableXRPError(err):
			return err
		case err != nil:
			lastErr = err
		}

		time.Sleep(xrpValidationPollInterval)
	}

	if lastErr != nil {
		return fmt.Errorf("timed out submitting XRP tx %s: %w", txHash, lastErr)
	}

	return fmt.Errorf("timed out submitting XRP tx %s", txHash)
}

func (c *Client) waitForValidation(txHash string, lastLedgerSequence uint32) error {
	deadline := time.Now().Add(xrpValidationTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		txResp, err := c.getTx(txHash)
		switch {
		case err == nil && txResp.Validated:
			return nil
		case err == nil:
			lastErr = fmt.Errorf("transaction %s not yet validated", txHash)
		case isRetryableXRPError(err):
			lastErr = err
		default:
			return err
		}

		currentLedger, err := c.rpcClient.GetLedgerIndex()
		switch {
		case err == nil && currentLedger.Int() > int(lastLedgerSequence):
			if lastErr != nil {
				return fmt.Errorf(
					"transaction %s was not validated before ledger %d: %w",
					txHash,
					lastLedgerSequence,
					lastErr,
				)
			}
			return fmt.Errorf("transaction %s was not validated before ledger %d", txHash, lastLedgerSequence)
		case err != nil && !isRetryableXRPError(err):
			return err
		case err != nil:
			lastErr = err
		}

		time.Sleep(xrpValidationPollInterval)
	}

	if lastErr != nil {
		return fmt.Errorf("timed out waiting for XRP tx %s validation: %w", txHash, lastErr)
	}

	return fmt.Errorf("timed out waiting for XRP tx %s validation", txHash)
}

func (c *Client) getTx(txHash string) (*txqueries.TxResponse, error) {
	res, err := c.rpcClient.Request(&txqueries.TxRequest{Transaction: txHash})
	if err != nil {
		return nil, err
	}

	var txResp txqueries.TxResponse
	if err := res.GetResult(&txResp); err != nil {
		return nil, err
	}

	return &txResp, nil
}

func isAcceptedXRPSubmitResponse(resp *txqueries.SubmitResponse) bool {
	if resp == nil {
		return false
	}

	return resp.EngineResult == "tesSUCCESS" ||
		resp.EngineResult == "tefPAST_SEQ" ||
		resp.Queued
}

func isRetryableXRPError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "client.timeout") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "txnnotfound") ||
		strings.Contains(errStr, "transaction not found")
}

func isAccountNotFoundXRPError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "actnotfound") ||
		strings.Contains(errStr, "account not found")
}
