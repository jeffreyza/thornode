package rpc

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// TestRPCVersion_UnmarshalJSON tests the UnmarshalJSON method for RPCVersion.
// It verifies that both string and numeric JSON values are correctly unmarshaled
// into the RPCVersion.Value field as a string.
func TestRPCVersion_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "String input",
			input:    `"1.2.3"`,
			expected: "1.2.3",
			wantErr:  false,
		},
		{
			name:     "Numeric input",
			input:    `123`,
			expected: "123",
			wantErr:  false,
		},
		{
			name:     "Invalid JSON",
			input:    `invalid`,
			expected: "",
			wantErr:  true,
		},
		{
			name:     "Empty string",
			input:    `""`,
			expected: "",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v RPCVersion
			err := json.Unmarshal([]byte(tt.input), &v)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && v.Value != tt.expected {
				t.Errorf("UnmarshalJSON() got = %v, want %v", v.Value, tt.expected)
			}
		})
	}
}

// TestRPCAddrList_Strings covers the load-bearing index-alignment invariant:
// writable-before-readonly ordering, and non-string entries preserved as empty
// placeholders (never dropped) so subsequent indices do not shift.
func TestRPCAddrList_Strings(t *testing.T) {
	// writable then readonly
	got := RPCAddrList{Writable: []interface{}{"W1", "W2"}, Readonly: []interface{}{"R1"}}.Strings()
	if want := []string{"W1", "W2", "R1"}; !slices.Equal(got, want) {
		t.Fatalf("ordering: got %v want %v", got, want)
	}

	// a non-string entry must become "" in place, not shift the rest
	got = RPCAddrList{Writable: []interface{}{"W1", 42, "W3"}}.Strings()
	if want := []string{"W1", "", "W3"}; !slices.Equal(got, want) {
		t.Fatalf("placeholder alignment: got %v want %v", got, want)
	}

	// empty list yields empty slice
	if got := (RPCAddrList{}).Strings(); len(got) != 0 {
		t.Fatalf("empty: got %v want []", got)
	}
}

// TestRPCTxnData_ResolvedAccounts verifies the full account list is static keys
// ++ writable-loaded ++ readonly-loaded, and that the legacy (no loaded
// addresses) path is byte-identical to the static account keys.
func TestRPCTxnData_ResolvedAccounts(t *testing.T) {
	txn := &RPCTxnData{Message: RPCMessage{AccountKeys: []string{"S0", "S1"}}}

	got := txn.ResolvedAccounts(RPCAddrList{Writable: []interface{}{"W0"}, Readonly: []interface{}{"R0", "R1"}})
	if want := []string{"S0", "S1", "W0", "R0", "R1"}; !slices.Equal(got, want) {
		t.Fatalf("ordering: got %v want %v", got, want)
	}

	if legacy := txn.ResolvedAccounts(RPCAddrList{}); !slices.Equal(legacy, []string{"S0", "S1"}) {
		t.Fatalf("legacy path must equal static keys: got %v", legacy)
	}
}

// TestGetAccountAtIndex_Bounds verifies the out-of-range guard, including the
// negative-index case that previously indexed AccountKeys[-1] and panicked.
func TestGetAccountAtIndex_Bounds(t *testing.T) {
	r := &RPCTxnData{Message: RPCMessage{AccountKeys: []string{"A", "B"}}}
	if got := r.GetAccountAtIndex(0); got != "A" {
		t.Fatalf("index 0: got %q want A", got)
	}
	if got := r.GetAccountAtIndex(-1); got != "" {
		t.Fatalf("negative index must return empty, not panic: got %q", got)
	}
	if got := r.GetAccountAtIndex(2); got != "" {
		t.Fatalf("out-of-range: got %q want empty", got)
	}
}

// TestResolvedAccounts_RealV0ALTFixture locks ResolvedAccounts and the RPC
// struct JSON tags against real getBlock wire data: a mainnet v0 transaction
// with 3 address lookup tables (slot 447349147, sig 8JAeYMEif...), whose
// instruction references accounts loaded from the tables. 16 static keys + 22
// writable + 13 readonly = 51 accounts in canonical static->writable->readonly
// order. Before this fix the loaded indices exceeded len(accountKeys) and the
// deposit was dropped.
func TestResolvedAccounts_RealV0ALTFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/v0_alt_tx.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Meta        RPCMeta    `json:"meta"`
		Transaction RPCTxnData `json:"transaction"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	resolved := fx.Transaction.ResolvedAccounts(fx.Meta.LoadedAddresses)
	if len(resolved) != 51 {
		t.Fatalf("resolved len: got %d want 51", len(resolved))
	}
	// static keys first; index 16 is the first writable-loaded address
	if want := "Df5uqevTGHajSLVJ8KziJEBKm88UPrkiT56TBpGPHquD"; resolved[16] != want {
		t.Fatalf("resolved[16] (first writable-loaded): got %s want %s", resolved[16], want)
	}
	// index 50 is the last readonly-loaded address (writable precede readonly)
	if want := "GbUQUoJyDbsgiNPaqJC9ruXzKYpbtts9cJceehSuc1a7"; resolved[50] != want {
		t.Fatalf("resolved[50] (last readonly-loaded): got %s want %s", resolved[50], want)
	}
	// the fixture instruction references a loaded index beyond the static keys;
	// it must resolve rather than fall off the static-only end (the fixed bug)
	ins := fx.Transaction.Message.Instructions[0]
	maxIdx := 0
	for _, a := range ins.Accounts {
		if a > maxIdx {
			maxIdx = a
		}
	}
	if maxIdx < len(fx.Transaction.Message.AccountKeys) {
		t.Fatalf("fixture instruction should reference a loaded index beyond %d static keys, got max %d", len(fx.Transaction.Message.AccountKeys), maxIdx)
	}
	if maxIdx >= len(resolved) || resolved[maxIdx] == "" {
		t.Fatalf("loaded index %d did not resolve against the full account list", maxIdx)
	}
}
