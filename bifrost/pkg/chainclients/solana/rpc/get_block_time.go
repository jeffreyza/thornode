package rpc

import (
	"fmt"
)

// GetBlockTimeResponse is the response for a getBlockTime call. Result is the
// estimated production Unix timestamp (seconds) of the slot, or JSON null when
// the node has no block for that slot (skipped, or below its available history).
// It is a *int64 on purpose: a bare int64 would silently unmarshal null to 0
// (1970), which always compares <= any target time and would diverge one node
// from another whenever a provider nulls a slot others serve.
type GetBlockTimeResponse struct {
	Jsonrpc string    `json:"jsonrpc"`
	Id      int       `json:"id"`
	Result  *int64    `json:"result"`
	Error   *RPCError `json:"error"`
}

// GetBlockTime returns the estimated production Unix time (seconds) of a slot as
// a *int64. A nil result with nil error means the RPC reported null (no block at
// that slot) — callers MUST treat that as "not available", never as an ancient
// timestamp. getBlockTime takes no commitment parameter, so it is only
// meaningful for slots already known to be present and finalized (e.g. slots
// returned by getBlocks); do not probe raw/unvetted slot numbers with it.
func (s *SolRPC) GetBlockTime(slot uint64) (*int64, error) {
	params := []interface{}{slot}

	var response GetBlockTimeResponse
	if err := s.post("getBlockTime", params, &response); err != nil {
		return nil, fmt.Errorf("failed to call getBlockTime: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}
