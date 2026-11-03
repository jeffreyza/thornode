package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNonceAccountNotProvisioned marks the cases where a durable-nonce account does
// not yet exist in a usable form (missing, uninitialized, wrong program, an empty
// stored nonce, or a plain non-nonce account at the address). These are RECOVERABLE
// — the caller must treat them as "no nonce yet, fall through to the recent-blockhash
// path" — as distinct from a transport/RPC error, which fails closed. Wrap with %w
// and test with errors.Is.
var ErrNonceAccountNotProvisioned = errors.New("durable-nonce account not provisioned")

// getAccountInfoResponse is the response for a getAccountInfo call made with the
// jsonParsed encoding. Value.Data is kept as RawMessage because jsonParsed returns
// an OBJECT ({program,parsed}) only for an account the runtime can parse (an
// initialized nonce account); for ANY other account — including a plain System
// account holding lamports at the address (e.g. a dusted seed address) — it returns
// an ARRAY (["<base64>","base64"]). Typing Data as a struct would make the whole
// response fail to decode on that array and surface as a (fail-closed) transport
// error, letting anyone halt a vault's SOL outbounds by sending it 1 lamport.
type getAccountInfoResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Id      int    `json:"id"`
	Result  *struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value *struct {
			Owner string          `json:"owner"`
			Data  json.RawMessage `json:"data"`
		} `json:"value"`
	} `json:"result"`
	Error *RPCError `json:"error"`
}

// nonceAccountData is the jsonParsed object shape for an initialized nonce account.
// The blockhash field is the STORED nonce value.
type nonceAccountData struct {
	Program string `json:"program"`
	Parsed  struct {
		Type string `json:"type"`
		Info struct {
			Authority string `json:"authority"`
			Blockhash string `json:"blockhash"`
		} `json:"info"`
	} `json:"parsed"`
}

// GetNonceAccount reads a SystemProgram durable-nonce account and returns its
// stored nonce and its nonce authority. The stored nonce is what a durable-nonce
// transaction bakes into the message's recentBlockhash field so the transaction
// does not expire during the multi-party TSS keysign+broadcast window.
//
// jsonParsed encoding at finalized commitment. Fails CLOSED only on a genuine
// transport/RPC error (a wrong or empty nonce would produce a transaction the
// keysign party can never land). A missing account, a non-nonce/plain account, an
// uninitialized account, or an empty stored nonce all return the RECOVERABLE
// ErrNonceAccountNotProvisioned so the caller falls through to the recent-blockhash
// path instead of halting the vault.
// minContextSlot > 0 is a LOWER BOUND: getAccountInfo errors on a node still behind
// that slot, but a node at or past it answers at its OWN slot, not at minContextSlot.
// So it bounds staleness; it does not make two nodes read the same account state.
// Around an account's creation or a nonce advance, parties can still disagree (see
// the SOLClient durableNonceForVault doc). Pass 0 to read the current finalized state.
func (s *SolRPC) GetNonceAccount(pubkey string, minContextSlot uint64) (nonce string, authority string, err error) {
	cfg := map[string]interface{}{"encoding": "jsonParsed", "commitment": "finalized"}
	if minContextSlot > 0 {
		cfg["minContextSlot"] = minContextSlot
	}
	params := []interface{}{pubkey, cfg}

	var response getAccountInfoResponse
	if err := s.post("getAccountInfo", params, &response); err != nil {
		return "", "", fmt.Errorf("failed to call getAccountInfo: %w", err)
	}
	if response.Error != nil && response.Error.Code != 0 {
		return "", "", fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}
	if response.Result == nil || response.Result.Value == nil {
		return "", "", fmt.Errorf("%w: account %s not found", ErrNonceAccountNotProvisioned, pubkey)
	}

	// A parseable nonce account returns an OBJECT; anything else (an array for a
	// plain/dusted account, null, or a different parsed program) is NOT a transport
	// error — it means no usable nonce account exists, so fall through recoverably.
	raw := response.Result.Value.Data
	var data nonceAccountData
	if len(raw) == 0 || json.Unmarshal(raw, &data) != nil {
		return "", "", fmt.Errorf("%w: account %s has no parsed nonce object", ErrNonceAccountNotProvisioned, pubkey)
	}
	if data.Program != "nonce" {
		return "", "", fmt.Errorf("%w: account %s is not a nonce account (program=%q)", ErrNonceAccountNotProvisioned, pubkey, data.Program)
	}
	if data.Parsed.Type != "initialized" {
		return "", "", fmt.Errorf("%w: account %s not initialized (type=%q)", ErrNonceAccountNotProvisioned, pubkey, data.Parsed.Type)
	}
	if data.Parsed.Info.Blockhash == "" {
		return "", "", fmt.Errorf("%w: account %s has empty stored nonce", ErrNonceAccountNotProvisioned, pubkey)
	}
	return data.Parsed.Info.Blockhash, data.Parsed.Info.Authority, nil
}
