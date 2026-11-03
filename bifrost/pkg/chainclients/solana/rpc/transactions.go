package rpc

import (
	"fmt"
)

// Response struct for getSignaturesForAddress
type GetSignaturesForAddressResponse struct {
	Jsonrpc string                       `json:"jsonrpc"`
	Id      int                          `json:"id"`
	Result  []SignaturesForAddressResult `json:"result"`
	Error   *RPCError                    `json:"error"`
}

// getSignaturesForAddress response data
type SignaturesForAddressResult struct {
	Signature string `json:"signature"`
	Slot      uint64 `json:"slot"`
	Err       any    `json:"err"`
}

type GetSignaturesForAddressUntilParams struct {
	Commitment     string `json:"commitment,omitempty"` // e.g., "finalized"
	MinContextSlot uint64 `json:"minContextSlot,omitempty"`
	Before         string `json:"before,omitempty"`
	Until          string `json:"until,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

// GetSignaturesForAddressUntil - makes a getSignaturesForAddress RPC call to the Solana RPC.
func (s *SolRPC) GetSignaturesForAddressUntil(address string, params GetSignaturesForAddressUntilParams) ([]SignaturesForAddressResult, error) {
	p := []interface{}{
		address,
		params,
	}

	var response GetSignaturesForAddressResponse
	if err := s.post("getSignaturesForAddress", p, &response); err != nil {
		return nil, fmt.Errorf("failed to call getSignaturesForAddress: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}

// Response struct for getSignaturesForAddress
type GetTransactionResponse struct {
	Jsonrpc string            `json:"jsonrpc"`
	Id      int               `json:"id"`
	Result  TransactionResult `json:"result"`
	Error   *RPCError         `json:"error"`
}

// getTransaction response data
type TransactionResult struct {
	Slot        uint64     `json:"slot"`
	Meta        RPCMeta    `json:"meta"`
	Transaction RPCTxnData `json:"transaction"`
}

type GetSignatureStatusesResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Id      int    `json:"id"`
	Result  struct {
		Value []*SignatureStatusResult `json:"value"`
	} `json:"result"`
	Error *RPCError `json:"error"`
}

type SignatureStatusResult struct {
	Slot               uint64  `json:"slot"`
	Confirmations      *uint64 `json:"confirmations"`
	Err                any     `json:"err"`
	ConfirmationStatus string  `json:"confirmationStatus"`
}

// GetTransactionWithCommitment makes a getTransaction RPC call to the Solana RPC
// with the requested commitment level.
func (s *SolRPC) GetTransactionWithCommitment(signature, commitment string) (*TransactionResult, error) {
	params := []interface{}{
		signature,
		map[string]interface{}{
			"commitment":                     commitment,
			"encoding":                       "json",
			"maxSupportedTransactionVersion": 1,
		},
	}

	var response GetTransactionResponse
	if err := s.post("getTransaction", params, &response); err != nil {
		return nil, fmt.Errorf("failed to call getTransaction: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return &response.Result, nil
}

// GetTransaction makes a getTransaction RPC call to the Solana RPC using
// "finalized" commitment, which matches the production scanner behavior.
func (s *SolRPC) GetTransaction(signature string) (*TransactionResult, error) {
	return s.GetTransactionWithCommitment(signature, "finalized")
}

// GetTransactionConfirmed makes a getTransaction RPC call using "confirmed"
// commitment, which resolves as soon as the transaction has been voted on by a
// supermajority of the cluster — well before finalization (~0.4s vs ~13s).
// Use this when you need to verify a just-broadcast transaction quickly.
func (s *SolRPC) GetTransactionConfirmed(signature string) (*TransactionResult, error) {
	return s.GetTransactionWithCommitment(signature, "confirmed")
}

// GetSignatureStatus checks whether the given signature is still known to the
// cluster, even if getTransaction is temporarily returning null before the
// transaction reaches confirmed/finalized visibility.
func (s *SolRPC) GetSignatureStatus(signature string) (*SignatureStatusResult, error) {
	params := []interface{}{
		[]string{signature},
		map[string]interface{}{
			"searchTransactionHistory": true,
		},
	}

	var response GetSignatureStatusesResponse
	if err := s.post("getSignatureStatuses", params, &response); err != nil {
		return nil, fmt.Errorf("failed to call getSignatureStatuses: %w", err)
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result.Value) == 0 || response.Result.Value[0] == nil {
		return nil, nil
	}

	return response.Result.Value[0], nil
}
