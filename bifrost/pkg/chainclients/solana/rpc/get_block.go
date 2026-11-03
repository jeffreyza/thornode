package rpc

import (
	"fmt"
)

// Response struct for getBlock
type GetBlockResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	Id      int         `json:"id"`
	Result  BlockResult `json:"result"`
	Error   *RPCError   `json:"error"`
}

// Structs for the response data
type BlockResult struct {
	Blockhash         string               `json:"blockhash"`
	PreviousBlockhash string               `json:"previousBlockhash"`
	ParentSlot        uint64               `json:"parentSlot"`
	Transactions      []*TransactionResult `json:"transactions"`
}

// GetBlock - makes a getBlock RPC call to the Solana RPC. Will only return a
// "finalized" block.
func (s *SolRPC) GetBlock(height uint64) (*BlockResult, error) {
	params := []interface{}{
		height,
		map[string]interface{}{
			"encoding":                       "json",
			"transactionDetails":             "full",
			"rewards":                        false,
			"commitment":                     "finalized",
			"maxSupportedTransactionVersion": 1,
		},
	}

	var response GetBlockResponse
	if err := s.post("getBlock", params, &response); err != nil {
		return nil, fmt.Errorf("failed to call getBlock: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		// Sometimes solana skips a slot, so in that case return an empty response to skip the slot
		if response.Error.Message == fmt.Sprintf("Slot %d was skipped, or missing due to ledger jump to recent snapshot", height) {
			fmt.Printf("Slot %d was skipped or missing. Returning empty result.\n", height)
			return nil, nil
		}
		return nil, fmt.Errorf("RPC Error: %s", response.Error.Message)
	}

	return &response.Result, nil
}

// GetBlockhashAtSlot returns only the blockhash of a finalized slot, without
// downloading the block's transaction list or rewards. Used by the
// consensus-blockhash derivation on the sign path, where the full block body
// (megabytes of transaction JSON on mainnet) is pure overhead spent inside the
// ~60s blockhash-liveness budget. Unlike GetBlock, a skipped/missing slot is an
// error rather than an empty result: callers only ask for slots from a finalized
// getBlocks listing, so any failure is unexpected and should surface for retry.
func (s *SolRPC) GetBlockhashAtSlot(slot uint64) (string, error) {
	params := []interface{}{
		slot,
		map[string]interface{}{
			"encoding":                       "json",
			"transactionDetails":             "none",
			"rewards":                        false,
			"commitment":                     "finalized",
			"maxSupportedTransactionVersion": 1,
		},
	}

	var response GetBlockResponse
	if err := s.post("getBlock", params, &response); err != nil {
		return "", fmt.Errorf("failed to call getBlock: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return "", fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result.Blockhash, nil
}

// Get a list of confirmed blocks in a range
func (s *SolRPC) GetBlockHeights(startSlot, endSlot uint64) ([]uint64, error) {
	params := []interface{}{
		startSlot,
		endSlot,
		map[string]interface{}{
			"commitment": "finalized",
		},
	}

	var response struct {
		Result []uint64  `json:"result"`
		Error  *RPCError `json:"error"`
	}
	if err := s.post("getBlocks", params, &response); err != nil {
		return nil, fmt.Errorf("failed to call getBlocks: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}

// GetFirstAvailableBlock returns the slot of the lowest confirmed block the node has
// not purged from its ledger — its retained-history floor. The consensus-blockhash
// search uses it as the ONLY proof that widening the lookback is pointless: getBlocks
// omitting a range cannot distinguish pruned history from a run of skipped slots, but
// this floor says where the ledger genuinely begins.
func (s *SolRPC) GetFirstAvailableBlock() (uint64, error) {
	var response struct {
		Result uint64    `json:"result"`
		Error  *RPCError `json:"error"`
	}
	if err := s.post("getFirstAvailableBlock", []interface{}{}, &response); err != nil {
		return 0, fmt.Errorf("failed to call getFirstAvailableBlock: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return 0, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}
