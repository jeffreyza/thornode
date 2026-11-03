package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dashjson "github.com/dashpay/dashd-go/btcjson"
)

// GetBestChainLockResult describes DASH's best ChainLock.
type GetBestChainLockResult struct {
	BlockHash  string `json:"blockhash"`
	Height     int64  `json:"height"`
	Signature  string `json:"signature"`
	KnownBlock bool   `json:"known_block"`
}

// GetRawMempoolVerboseResult describes DASH's lock-aware mempool entries.
type GetRawMempoolVerboseResult struct {
	// Dash Core returns "true", "false", or "unknown", rather than a JSON boolean.
	InstantLock      string      `json:"instantlock"`
	Size             int32       `json:"size"`
	VSize            int32       `json:"vsize"`
	Weight           int32       `json:"weight"`
	Fee              float64     `json:"fee"`
	Fees             MempoolFees `json:"fees"`
	Time             int64       `json:"time"`
	Height           int64       `json:"height"`
	StartingPriority float64     `json:"startingpriority"`
	CurrentPriority  float64     `json:"currentpriority"`
	Depends          []string    `json:"depends"`
}

// MempoolFees holds DASH's nested mempool fee fields.
type MempoolFees = dashjson.MempoolFees

// QuorumLocked reports DASH's daemon-validated InstantSend or ChainLock finality.
// Bifrost's observation policy is applied separately by the chain client.
func (tx *TxResult) QuorumLocked() bool {
	return tx.InstantLock || tx.InstantLockInternal || tx.ChainLock
}

// InstantSendLocked interprets DASH's internal and combined InstantSend flags.
func (tx *TxResult) InstantSendLocked() bool {
	return tx.InstantLockInternal || (tx.InstantLock && !tx.ChainLock)
}

// ISLockInput identifies a locked transaction input for DASH.
type ISLockInput struct {
	TxID string `json:"txid"`
	Vout uint32 `json:"vout"`
}

// ISLockResult describes DASH's InstantSend lock.
type ISLockResult struct {
	TxID      string        `json:"txid"`
	Inputs    []ISLockInput `json:"inputs"`
	ID        string        `json:"id,omitempty"`
	CycleHash string        `json:"cycleHash"`
	Signature string        `json:"signature"`
	Hex       string        `json:"hex"`
}

// GetBestChainLock retrieves DASH's best ChainLock.
func (c *Client) GetBestChainLock() (*GetBestChainLockResult, error) {
	var result GetBestChainLockResult
	err := c.Call(&result, "getbestchainlock")
	return &result, extractBTCError(err)
}

// GetRawMempoolVerbose retrieves DASH's lock-aware mempool entries.
func (c *Client) GetRawMempoolVerbose() (map[string]GetRawMempoolVerboseResult, error) {
	result := make(map[string]GetRawMempoolVerboseResult)
	err := c.Call(&result, "getrawmempool", true)
	return result, extractBTCError(err)
}

// GetISLocks retrieves DASH's InstantSend locks and preserves input order,
// including missing locks. Failed chunks remain nil and are reported in the
// error; callers may still use successful chunks.
func (c *Client) GetISLocks(txIDs []string) ([]*ISLockResult, error) {
	locks := make([]*ISLockResult, len(txIDs))
	var failures error
	for start := 0; start < len(txIDs); start += 100 {
		end := min(start+100, len(txIDs))
		batch, err := c.getISLocksBatch(txIDs[start:end])
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("getislocks batch %d: %w", start/100, err))
			continue
		}
		copy(locks[start:end], batch)
	}
	return locks, failures
}

// getISLocksBatch decodes one DASH getislocks response.
func (c *Client) getISLocksBatch(txIDs []string) ([]*ISLockResult, error) {
	var rawLocks []json.RawMessage
	err := c.Call(&rawLocks, "getislocks", txIDs)
	if err != nil {
		return nil, extractBTCError(err)
	}
	if len(rawLocks) != len(txIDs) {
		return nil, fmt.Errorf("getislocks returned %d results for %d transactions", len(rawLocks), len(txIDs))
	}
	locks := make([]*ISLockResult, len(txIDs))
	for i, raw := range rawLocks {
		if string(raw) == "null" {
			continue
		}
		var none string
		err = json.Unmarshal(raw, &none)
		if err == nil && none == "None" {
			continue
		}
		var lock ISLockResult
		err = json.Unmarshal(raw, &lock)
		if err != nil {
			return nil, fmt.Errorf("fail to decode getislocks result %d: %w", i, err)
		}
		if !strings.EqualFold(lock.TxID, txIDs[i]) {
			return nil, fmt.Errorf("getislocks result %d: txid %q, want %q", i, lock.TxID, txIDs[i])
		}
		locks[i] = &lock
	}
	return locks, nil
}
