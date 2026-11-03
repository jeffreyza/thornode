package types

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

// ErrSimTxUncertain means transaction inclusion could not be established
// within the client's polling budget. Callers must stop, not sign another tx.
var ErrSimTxUncertain = errors.New("simulation transaction outcome remains uncertain")

// SimTxErrorResult stops an actor if retrying would discard an unresolved
// signed transaction. Other failures retain the actor's normal retry behavior.
func SimTxErrorResult(err error) OpResult {
	return OpResult{Finish: errors.Is(err, ErrSimTxUncertain), Error: err}
}

var (
	tronSimTxMaxBroadcastAttempts   = 4
	tronSimTxBroadcastRetryDelay    = 2 * time.Second
	solanaSimTxMaxBroadcastAttempts = 4
	solanaSimTxBroadcastRetryDelay  = 2 * time.Second
	zecSimTxMaxBroadcastAttempts    = 4
	zecSimTxBroadcastRetryDelay     = 2 * time.Second
)

// SignAndBroadcastTx retries transient failures with each chain's payload policy.
// ZEC and TRON reuse signed bytes. A TRON transaction ID with an error stops
// the operation: confirmation belongs to the client, not another signing cycle.
func SignAndBroadcastTx(client LiteChainClient, tx SimTx) (string, error) {
	attempts, retryDelay := simTxRetryConfig(tx.Chain)

	var lastErr error
	var signed []byte
	for attempt := 1; attempt <= attempts; attempt++ {
		var err error
		if signed == nil || (!tx.Chain.Equals(common.ZECChain) && !tx.Chain.Equals(common.TRONChain)) {
			signed, err = client.SignTx(tx)
		}
		if err != nil {
			lastErr = err
			if attempt < attempts && shouldRetrySimTxSign(tx.Chain, err) {
				time.Sleep(retryDelay)
				continue
			}
			return "", err
		}

		txid, err := client.BroadcastTx(signed)
		if err == nil {
			return txid, nil
		}

		lastErr = err
		if errors.Is(err, ErrSimTxUncertain) {
			return txid, err
		}
		if tx.Chain.Equals(common.TRONChain) && txid != "" {
			// The TRON client owns confirmation polling. Never discard a known
			// transaction with an unresolved outcome or report it as confirmed.
			return txid, fmt.Errorf("%w: %w", ErrSimTxUncertain, err)
		}
		// A returned txid plus a known submitted-but-unconfirmed error is the
		// safety condition here. This deliberately applies beyond OneShotMemo:
		// once the chain client has submitted a tx, replacing it can create a
		// duplicate inbound when bifrost later observes the original.
		if txid != "" && isSubmittedButUnconfirmedSimTxBroadcast(tx.Chain, err) {
			return txid, nil
		}
		if attempt < attempts && shouldRetrySimTxBroadcast(tx.Chain, err) {
			time.Sleep(retryDelay)
			continue
		}

		return "", err
	}

	return "", lastErr
}

func simTxRetryConfig(chain common.Chain) (int, time.Duration) {
	switch {
	case chain.Equals(common.TRONChain):
		return tronSimTxMaxBroadcastAttempts, tronSimTxBroadcastRetryDelay
	case chain.Equals(common.SOLChain):
		return solanaSimTxMaxBroadcastAttempts, solanaSimTxBroadcastRetryDelay
	case chain.Equals(common.ZECChain):
		return zecSimTxMaxBroadcastAttempts, zecSimTxBroadcastRetryDelay
	default:
		return 1, 0
	}
}

func shouldRetrySimTxSign(chain common.Chain, err error) bool {
	switch {
	case chain.Equals(common.SOLChain):
		return shouldRetrySolanaSimTxSign(err)
	default:
		return false
	}
}

func shouldRetrySimTxBroadcast(chain common.Chain, err error) bool {
	switch {
	case chain.Equals(common.TRONChain):
		return shouldRetryTronSimTxBroadcast(err)
	case chain.Equals(common.SOLChain):
		return shouldRetrySolanaSimTxBroadcast(err)
	case chain.Equals(common.ZECChain):
		return shouldRetryZecSimTxBroadcast(err)
	default:
		return false
	}
}

func isSubmittedButUnconfirmedSimTxBroadcast(chain common.Chain, err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	switch {
	case chain.Equals(common.SOLChain):
		return strings.Contains(message, "not confirmed on solana node")
	default:
		return false
	}
}

func shouldRetryTronSimTxBroadcast(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not confirmed on tron node") ||
		strings.Contains(message, "not observed on tron node") ||
		isTransientSimTxRPCError(message)
}

func shouldRetrySolanaSimTxSign(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "fail to get latest blockhash") ||
		isTransientSimTxRPCError(message)
}

func shouldRetrySolanaSimTxBroadcast(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not confirmed on solana node") ||
		isTransientSimTxRPCError(message)
}

func shouldRetryZecSimTxBroadcast(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "verification cancelled") ||
		strings.Contains(message, "timeout waiting for verification result") ||
		strings.Contains(message, "already queued for download") ||
		isTransientSimTxRPCError(message)
}

func isTransientSimTxRPCError(message string) bool {
	return strings.Contains(message, "connect: connection refused") ||
		strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "i/o timeout") ||
		strings.Contains(message, "channel closed") ||
		strings.Contains(message, "eof")
}
