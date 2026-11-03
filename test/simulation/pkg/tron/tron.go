package tron

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/math"
	"github.com/ethereum/go-ethereum/crypto"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Tron
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain   common.Chain
	api     *api.TronApi
	keys    *thorclient.Keys
	privKey *ecdsa.PrivateKey
	address common.Address
	txMu    sync.Mutex
}

var _ LiteChainClient = &Client{}

const (
	tronAPITimeout                     = 10 * time.Second
	tronCreateTransactionWarmupTimeout = 60 * time.Second
)

var (
	tronBroadcastObservationTimeout      = 15 * time.Second
	tronBroadcastObservationPollInterval = 250 * time.Millisecond
	tronBroadcastRebroadcastInterval     = 3 * time.Second
	tronBroadcastConfirmationGrace       = 5 * time.Second
)

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(
	chain common.Chain,
	host string,
	keys *thorclient.Keys,
) (LiteChainClient, error) {
	// extract the private key
	privKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	// derive the public key
	pk, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to get tm pub key: %w", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to get pub key: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	client := Client{
		chain:   chain,
		api:     api.NewTronApi(host, tronAPITimeout),
		keys:    keys,
		address: address,
	}

	priv, err := keys.GetPrivateKey()
	if err != nil {
		return nil, err
	}

	client.privKey, err = evm.GetPrivateKey(priv)
	if err != nil {
		return nil, err
	}

	return &client, nil
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	address := c.address

	if pk != nil {
		var err error
		address, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	balance, err := c.api.GetBalance(address.String())
	if err != nil {
		return nil, err
	}

	amount := math.NewUint(balance).Mul(math.NewUint(100)) // 1e6 -> 1e8
	coins := []common.Coin{
		common.NewCoin(common.TRXAsset, amount),
	}

	return &common.Account{
		Coins: coins,
	}, nil
}

func (c *Client) SignContractTx(_ SimContractTx) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	c.txMu.Lock()
	defer c.txMu.Unlock()

	deadline := time.Now().Add(tronCreateTransactionWarmupTimeout)
	var tronTx api.Transaction
	var err error
	for {
		tronTx, err = c.api.CreateTransaction(
			c.address.String(),
			tx.ToAddress.String(),
			tx.Coin.Amount.Quo(math.NewUint(100)).Uint64(),
			tx.Memo,
		)
		if err != nil {
			return nil, err
		}
		if tronTx.TxId == "" && tronTx.RawDataHex != "" {
			_ = tronTx.Rehash()
		}
		if tronTx.TxId != "" {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("create transaction returned empty txID")
		}
		time.Sleep(100 * time.Millisecond)
	}

	hash, err := hex.DecodeString(tronTx.TxId)
	if err != nil {
		return nil, err
	}

	signature, err := crypto.Sign(hash, c.privKey)
	if err != nil {
		return nil, err
	}

	tronTx.Signature = append(tronTx.Signature, hex.EncodeToString(signature))

	txBytes, err := json.Marshal(tronTx)
	if err != nil {
		return nil, err
	}

	return txBytes, nil
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	var signedTx api.Transaction
	if err := json.Unmarshal(signed, &signedTx); err != nil {
		return "", fmt.Errorf("failed to decode signed tx: %w", err)
	}

	if signedTx.TxId == "" {
		return "", fmt.Errorf("signed TRON transaction has no txID")
	}
	response, err := c.api.BroadcastTransaction(signed)
	// Only a known rejection of this first submission can release its identity.
	// All other replies (including connectivity failures after pool insertion)
	// leave the original payload potentially executable.
	if err == nil && !response.Result && isDefiniteBroadcastRejection(response.Code) &&
		(response.TxId == "" || strings.EqualFold(response.TxId, signedTx.TxId)) {
		return "", fmt.Errorf("failed to broadcast tx: %s - %s", response.Code, response.Message)
	}
	if err := c.waitForConfirmedBroadcast(signedTx.TxId, signed, signedTx.RawData.Expiration); err != nil {
		return signedTx.TxId, err
	}
	return signedTx.TxId, nil
}

// These java-tron 4.8.1 codes are returned before pending-pool insertion.
// NOT_ENOUGH_EFFECTIVE_CONNECTION also occurs after insertion, so is excluded.
// This classification applies only before any earlier ambiguous submission.
func isDefiniteBroadcastRejection(code string) bool {
	switch code {
	case "BLOCK_UNSOLIDIFIED", "NO_CONNECTION", "SERVER_BUSY",
		"SIGERROR", "CONTRACT_VALIDATE_ERROR", "CONTRACT_EXE_ERROR",
		"BANDWITH_ERROR", "TAPOS_ERROR", "TOO_BIG_TRANSACTION_ERROR",
		"TRANSACTION_EXPIRATION_ERROR":
		return true
	default:
		return false
	}
}

func (c *Client) waitForConfirmedBroadcast(txID string, signed []byte, expirationMs int64) error {
	deadline := time.Now().Add(tronBroadcastObservationTimeout)
	if expirationMs > 0 {
		expiration := time.UnixMilli(expirationMs).Add(tronBroadcastConfirmationGrace)
		if expiration.After(deadline) {
			deadline = expiration
		}
	}

	var lastErr error
	observed := false
	lastBroadcast := time.Now()

	for {
		info, err := c.api.GetTransactionInfo(txID)
		if err == nil && strings.EqualFold(info.ID, txID) && info.BlockNumber > 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		}

		tx, err := c.api.GetTransaction(txID)
		if err == nil && strings.EqualFold(tx.TxId, txID) {
			observed = true
		} else if err != nil {
			lastErr = err
		}

		// A local deadline or expiry cannot prove the original was never mined.
		if time.Now().After(deadline) {
			if lastErr != nil {
				if observed {
					return fmt.Errorf("%w: broadcasted tx %s not confirmed on tron node: %w", ErrSimTxUncertain, txID, lastErr)
				}
				return fmt.Errorf("%w: broadcasted tx %s not observed on tron node: %w", ErrSimTxUncertain, txID, lastErr)
			}
			if observed {
				return fmt.Errorf("%w: broadcasted tx %s not confirmed on tron node", ErrSimTxUncertain, txID)
			}
			return fmt.Errorf("%w: broadcasted tx %s not observed on tron node", ErrSimTxUncertain, txID)
		}

		if time.Since(lastBroadcast) >= tronBroadcastRebroadcastInterval &&
			(expirationMs == 0 || time.Now().Before(time.UnixMilli(expirationMs))) {
			response, err := c.api.BroadcastTransaction(signed)
			if err != nil {
				lastErr = err
			} else {
				lastBroadcast = time.Now()
				if !response.Result && response.Code != "DUP_TRANSACTION_ERROR" {
					if response.Message != "" {
						lastErr = fmt.Errorf("failed to rebroadcast tx %s: %s - %s", txID, response.Code, response.Message)
					} else if response.Code != "" {
						lastErr = fmt.Errorf("failed to rebroadcast tx %s: %s", txID, response.Code)
					} else {
						lastErr = fmt.Errorf("failed to rebroadcast tx %s", txID)
					}
				}
			}
		}

		time.Sleep(tronBroadcastObservationPollInterval)
	}
}
