package rpc

import (
	"encoding/json"
	"fmt"
	"math/big"
)

var solanaTokenProgramIDs = []string{
	"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
	"TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb",
}

type tokenAccountsByOwnerResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Id      int    `json:"id"`
	Result  struct {
		Value []struct {
			Account struct {
				Lamports json.Number `json:"lamports"`
			} `json:"account"`
		} `json:"value"`
	} `json:"result"`
	Error *RPCError `json:"error"`
}

// GetOwnedTokenAccountLamports returns the lamports currently held by token
// accounts owned by the given wallet. These lamports remain spendable by the
// wallet when the token accounts are closed, so solvency checks should include
// them alongside the base wallet balance.
func (s *SolRPC) GetOwnedTokenAccountLamports(owner, commitment string) (*big.Int, error) {
	total := big.NewInt(0)

	for _, programID := range solanaTokenProgramIDs {
		params := []interface{}{
			owner,
			map[string]interface{}{
				"programId": programID,
			},
			map[string]interface{}{
				"encoding":   "jsonParsed",
				"commitment": commitment,
			},
		}

		var response tokenAccountsByOwnerResponse
		if err := s.post("getTokenAccountsByOwner", params, &response); err != nil {
			return nil, fmt.Errorf("failed to call getTokenAccountsByOwner for %s: %w", programID, err)
		}
		if response.Error != nil && response.Error.Code != 0 {
			return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
		}

		for _, item := range response.Result.Value {
			lamports := new(big.Int)
			if _, ok := lamports.SetString(item.Account.Lamports.String(), 10); !ok {
				return nil, fmt.Errorf("failed to convert token account lamports to big.Int: %s", item.Account.Lamports.String())
			}
			total.Add(total, lamports)
		}
	}

	return total, nil
}
