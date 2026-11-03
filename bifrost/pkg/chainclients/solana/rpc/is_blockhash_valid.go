package rpc

import (
	"fmt"
)

// IsBlockhashValidResponse is the response for a isBlockhashValid call. result
// is {context:{slot}, value:bool}; value is false when the blockhash is no longer
// usable at the requested commitment. context.slot is the slot of the bank that
// answered, used to detect a backend lagging behind the requested minimum context.
type IsBlockhashValidResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Id      int    `json:"id"`
	Result  *struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value bool `json:"value"`
	} `json:"result"`
	Error *RPCError `json:"error"`
}

// IsBlockhashValid reports whether blockhash is still usable at the given
// commitment. Pass "processed" to check against the PROCESSING bank — the tip a
// broadcast tx is actually validated against. The "finalized" slot lags ~13s
// (~32 slots) behind processed, so measuring a blockhash's age against finalized
// understates it and can pass a hash already past Solana's ~151-block
// MAX_PROCESSING_AGE (i.e. a tx that can never land).
//
// minContextSlot guards against a LAGGING backend: a node whose processing bank
// is behind the network can answer value:false for a blockhash that is still live
// on the real tip, which would make the caller wrongly treat the hash as expired.
// Passing minContextSlot (the anchor slot the hash was derived from) makes a
// lagging backend REJECT the request (Agave returns an RPC error when its bank is
// below the requested minimum context), so the caller sees an error and can retain
// + retry rather than dropping a still-valid outbound. Pass 0 to disable the guard.
func (s *SolRPC) IsBlockhashValid(blockhash, commitment string, minContextSlot uint64) (bool, error) {
	cfg := map[string]interface{}{"commitment": commitment}
	if minContextSlot > 0 {
		cfg["minContextSlot"] = minContextSlot
	}
	params := []interface{}{blockhash, cfg}

	var response IsBlockhashValidResponse
	if err := s.post("isBlockhashValid", params, &response); err != nil {
		return false, fmt.Errorf("failed to call isBlockhashValid: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return false, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	if response.Result == nil {
		return false, fmt.Errorf("isBlockhashValid: nil result")
	}

	// Defensive: a well-behaved backend rejects a below-min-context request (handled
	// by the Error branch above), but not all do. If one answered anyway from a bank
	// below the requested minimum, treat it as lagging -> error, so the caller retains
	// rather than trusting a possibly-stale false.
	if minContextSlot > 0 && response.Result.Context.Slot < minContextSlot {
		return false, fmt.Errorf("isBlockhashValid: backend context slot %d below minimum %d (lagging backend)", response.Result.Context.Slot, minContextSlot)
	}

	return response.Result.Value, nil
}
