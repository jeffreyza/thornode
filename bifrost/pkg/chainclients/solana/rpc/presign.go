package rpc

import (
	"encoding/json"
	"fmt"
)

// AccountMeta is the part of an account's state the pre-sign filters need.
type AccountMeta struct {
	Exists     bool
	Executable bool
	Owner      string
	Lamports   uint64
	Space      uint64
}

// GetAccountMeta reads an account's existence, owner, executable flag, lamports and
// data size (no data: a zero-length dataSlice) at confirmed commitment.
func (s *SolRPC) GetAccountMeta(address string) (AccountMeta, error) {
	params := []interface{}{
		address,
		map[string]interface{}{
			"encoding":   "base64",
			"commitment": "confirmed",
			"dataSlice":  map[string]int{"offset": 0, "length": 0},
		},
	}
	var resp struct {
		Result *struct {
			Value *struct {
				Owner      string `json:"owner"`
				Executable bool   `json:"executable"`
				Lamports   uint64 `json:"lamports"`
				Space      uint64 `json:"space"`
			} `json:"value"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := s.post("getAccountInfo", params, &resp); err != nil {
		return AccountMeta{}, fmt.Errorf("failed to call getAccountInfo: %w", err)
	}
	if resp.Error != nil {
		return AccountMeta{}, fmt.Errorf("getAccountInfo error: %d %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		return AccountMeta{}, fmt.Errorf("getAccountInfo: empty result")
	}
	if resp.Result.Value == nil {
		return AccountMeta{}, nil
	}
	v := resp.Result.Value
	return AccountMeta{Exists: true, Executable: v.Executable, Owner: v.Owner, Lamports: v.Lamports, Space: v.Space}, nil
}

// GetMinimumBalanceForRentExemption returns the lamports an account with dataLen
// bytes of data must hold to be rent exempt.
func (s *SolRPC) GetMinimumBalanceForRentExemption(dataLen uint64) (uint64, error) {
	var resp struct {
		Result *uint64   `json:"result"`
		Error  *RPCError `json:"error"`
	}
	if err := s.post("getMinimumBalanceForRentExemption", []interface{}{dataLen}, &resp); err != nil {
		return 0, fmt.Errorf("failed to call getMinimumBalanceForRentExemption: %w", err)
	}
	if resp.Error != nil {
		return 0, fmt.Errorf("getMinimumBalanceForRentExemption error: %d %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		return 0, fmt.Errorf("getMinimumBalanceForRentExemption: empty result")
	}
	return *resp.Result, nil
}

// SimulateTransaction simulates a base64-encoded transaction without verifying its
// signatures and without replacing its blockhash (a durable-nonce tx is simulated
// against the nonce it carries). execErr is the runtime error the transaction would
// fail with, or nil when it would succeed; err is a transport/RPC failure.
func (s *SolRPC) SimulateTransaction(base64Tx string) (execErr json.RawMessage, err error) {
	params := []interface{}{
		base64Tx,
		map[string]interface{}{
			"encoding":               "base64",
			"sigVerify":              false,
			"replaceRecentBlockhash": false,
			"commitment":             "confirmed",
		},
	}
	var resp struct {
		Result *struct {
			Value struct {
				Err json.RawMessage `json:"err"`
			} `json:"value"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := s.post("simulateTransaction", params, &resp); err != nil {
		return nil, fmt.Errorf("failed to call simulateTransaction: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("simulateTransaction error: %d %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		return nil, fmt.Errorf("simulateTransaction: empty result")
	}
	if e := resp.Result.Value.Err; len(e) > 0 && string(e) != "null" {
		return e, nil
	}
	return nil, nil
}
