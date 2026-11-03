package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

////////////////////////////////////////////////////////////////////////////////////////
// rpc_types contains the structs for the responses from the Solana RPC
////////////////////////////////////////////////////////////////////////////////////////

type RPCMeta struct {
	ComputeUnitsConsumed uint64        `json:"computeUnitsConsumed"`
	Err                  interface{}   `json:"err"`
	Fee                  uint64        `json:"fee"`
	InnerInstructions    []interface{} `json:"innerInstructions"`
	LoadedAddresses      RPCAddrList   `json:"loadedAddresses"`
	LogMessages          []string      `json:"logMessages"`
	PostBalances         []uint64      `json:"postBalances"`
	PostTokenBalances    []interface{} `json:"postTokenBalances"`
	PreBalances          []uint64      `json:"preBalances"`
	PreTokenBalances     []interface{} `json:"preTokenBalances"`
	Rewards              interface{}   `json:"rewards"`
	Status               RPCTxnStatus  `json:"status"`
}

type RPCTxnData struct {
	Message    RPCMessage `json:"message"`
	Signatures []string   `json:"signatures"`
}

type RPCAddrList struct {
	Readonly []interface{} `json:"readonly"`
	Writable []interface{} `json:"writable"`
}

type RPCTxnStatus struct {
	Ok interface{} `json:"Ok"`
}

type RPCMessage struct {
	AccountKeys         []string                 `json:"accountKeys"`
	AddressLookupTables []RPCAddressLookupTables `json:"addressTableLookups"`
	Header              RPCHeader                `json:"header"`
	Instructions        []RPCInstruction         `json:"instructions"`
	RecentBlockhash     string                   `json:"recentBlockhash"`
}

type RPCAddressLookupTables struct {
	AccountKey      string `json:"accountKey"`
	WritableIndexes []int  `json:"writableIndexes"`
	ReadonlyIndexes []int  `json:"readonlyIndexes"`
}

type RPCHeader struct {
	NumReadonlySignedAccounts   int `json:"numReadonlySignedAccounts"`
	NumReadonlyUnsignedAccounts int `json:"numReadonlyUnsignedAccounts"`
	NumRequiredSignatures       int `json:"numRequiredSignatures"`
}

type RPCInstruction struct {
	Accounts       []int       `json:"accounts"`
	Data           string      `json:"data"`
	ProgramIdIndex int         `json:"programIdIndex"`
	StackHeight    interface{} `json:"stackHeight"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements the error interface so callers can errors.As a returned
// error back to *RPCError and inspect the Solana JSON-RPC code/message.
func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Custom type for Version to handle both string and numeric values
type RPCVersion struct {
	Value string
}

func (v *RPCVersion) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("invalid RPC version")
	}
	if b[0] == '"' {
		return json.Unmarshal(b, &v.Value)
	}
	var num int
	if err := json.Unmarshal(b, &num); err != nil {
		return err
	}
	v.Value = strconv.Itoa(num)
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func (r *RPCTxnData) GetInstructions() []RPCInstruction {
	return r.Message.Instructions
}

// GetAccountAtIndex returns the static account key at index i, or "" when i is
// out of range (including negative). It resolves ONLY the static message
// account keys; for versioned transactions whose instructions reference
// addresses loaded from address lookup tables, use ResolvedAccounts instead.
func (r *RPCTxnData) GetAccountAtIndex(i int) string {
	if i < 0 || i >= len(r.Message.AccountKeys) {
		return ""
	}
	return r.Message.AccountKeys[i]
}

// Strings returns the loaded addresses in the canonical Solana ordering that
// follows the static account keys for versioned (v0+) transactions: writable
// loaded addresses first, then readonly. A non-string entry (never emitted by
// the RPC in practice) is preserved as an empty-string placeholder rather than
// dropped, so it matches nothing yet does not shift the index of subsequent
// loaded addresses. Legacy transactions have empty loaded addresses, so this
// returns an empty slice and account resolution is unchanged for them.
func (l RPCAddrList) Strings() []string {
	out := make([]string, 0, len(l.Writable)+len(l.Readonly))
	appendAddrs := func(addrs []interface{}) {
		for _, v := range addrs {
			// Preserve position so instruction account indices stay aligned. A
			// non-string entry (the RPC always emits base58 strings, so this is
			// defensive) becomes an empty address that matches nothing, rather
			// than shifting every subsequent index by one.
			s, _ := v.(string)
			out = append(out, s)
		}
	}
	appendAddrs(l.Writable)
	appendAddrs(l.Readonly)
	return out
}

// ResolvedAccounts returns the full account list an instruction's account
// indices reference: the transaction's static account keys followed by any
// addresses loaded from address lookup tables (writable then readonly). For
// legacy transactions this equals the static account keys.
func (r *RPCTxnData) ResolvedAccounts(loaded RPCAddrList) []string {
	accounts := make([]string, 0, len(r.Message.AccountKeys)+len(loaded.Writable)+len(loaded.Readonly))
	accounts = append(accounts, r.Message.AccountKeys...)
	accounts = append(accounts, loaded.Strings()...)
	return accounts
}
