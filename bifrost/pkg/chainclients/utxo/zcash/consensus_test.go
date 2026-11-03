package zcash

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestConsensusStatePaymentState checks upgrade selection using arbitrary RPC branch IDs.
func TestConsensusStatePaymentState(t *testing.T) {
	for _, test := range []struct {
		name, upgrades         string
		height, branch, expiry uint32
	}{
		{"active branch", `{"abcdef12":{"activationheight":1}}`, 100, 0xabcdef12, 140},
		{"before upgrade window", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":120}}`, 79, 0xabcdef12, 119},
		{"upgrade at expiry", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":120}}`, 80, 0xabcdef12, 119},
		{"before upgrade", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":120}}`, 118, 0xabcdef12, 119},
		{"upgrade next block", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":120}}`, 119, 0x01020304, 159},
		{"after upgrade", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":120}}`, 120, 0x01020304, 160},
		{"nearest future upgrade", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":120},"fedcba98":{"activationheight":130}}`, 100, 0xabcdef12, 119},
		{"disabled upgrade", `{"abcdef12":{"activationheight":1},"01020304":{"activationheight":-1}}`, 100, 0xabcdef12, 140},
		{"zero branch", `{"00000000":{"activationheight":1}}`, 100, 0, 0},
		{"malformed branch", `{"abcdefgh":{"activationheight":1}}`, 100, 0, 0},
		{"short branch", `{"1234":{"activationheight":1}}`, 100, 0, 0},
		{"no active branch", `{"abcdef12":{"activationheight":120}}`, 100, 0, 0},
		{"missing schedule", `{}`, 100, 0, 0},
		{"invalid height", `{"abcdef12":{"activationheight":1}}`, 499999960, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state ConsensusState
			if err := json.Unmarshal([]byte(`{"upgrades":`+test.upgrades+`}`), &state); err != nil {
				t.Fatal(err)
			}
			branch, expiry, err := state.PaymentState(test.height, 40)
			if test.branch == 0 {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil || branch != test.branch || expiry != test.expiry {
				t.Fatal(branch, expiry, err)
			}
		})
	}
}

func TestConsensusStateSharedActivation(t *testing.T) {
	for _, test := range []struct {
		name, chainTip, nextBlock string
		height, branch, expiry    uint32
	}{
		{"same epoch", "01020304", "01020304", 100, 0x01020304, 119},
		{"tip before next upgrade", "01020304", "fedcba98", 100, 0x01020304, 119},
		{"tip before shared activation", "00000000", "01020304", 0, 0x01020304, 40},
		{"target after next upgrade", "01020304", "fedcba98", 119, 0xfedcba98, 159},
		{"tip has passed target epoch", "fedcba98", "fedcba98", 100, 0, 0},
		{"missing consensus", "", "", 100, 0, 0},
		{"conflicting consensus", "abcdef12", "01020304", 100, 0, 0},
		{"malformed consensus", "invalid", "invalid", 100, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state ConsensusState
			response := fmt.Sprintf(`{"upgrades":{"abcdef12":{"activationheight":1},"01020304":{"activationheight":1},"fedcba98":{"activationheight":120}},"consensus":{"chaintip":%q,"nextblock":%q}}`, test.chainTip, test.nextBlock)
			if err := json.Unmarshal([]byte(response), &state); err != nil {
				t.Fatal(err)
			}
			branch, expiry, err := state.PaymentState(test.height, 40)
			if test.branch == 0 {
				if err == nil {
					t.Fatal("expected ambiguous schedule rejection")
				}
				return
			}
			if err != nil || branch != test.branch || expiry != test.expiry {
				t.Fatal(branch, expiry, err)
			}
		})
	}
}

// TestConsensusStateV6Active checks that v6 is reported active only at or past NU6.3's
// activation (branch id 0x37a5165b) on the daemon's own schedule.
func TestConsensusStateV6Active(t *testing.T) {
	for _, test := range []struct {
		name, upgrades string
		height         uint32
		want           bool
	}{
		{"nu6.3 active from genesis", `{"37a5165b":{"activationheight":1}}`, 100, true},
		{"before nu6.3 activation", `{"c8e71055":{"activationheight":1},"37a5165b":{"activationheight":200}}`, 100, false},
		{"one block before activation", `{"37a5165b":{"activationheight":200}}`, 199, false},
		{"exactly at activation", `{"37a5165b":{"activationheight":200}}`, 200, true},
		{"after activation", `{"37a5165b":{"activationheight":200}}`, 201, true},
		{"nu6.3 absent (older daemon)", `{"c8e71055":{"activationheight":1}}`, 1000000, false},
		{"nu6.3 disabled", `{"37a5165b":{"activationheight":-1}}`, 1000000, false},
		{"uppercase branch key", `{"37A5165B":{"activationheight":1}}`, 100, true},
		{"empty schedule", `{}`, 100, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state ConsensusState
			if err := json.Unmarshal([]byte(`{"upgrades":`+test.upgrades+`}`), &state); err != nil {
				t.Fatal(err)
			}
			if got := state.V6Active(test.height); got != test.want {
				t.Fatalf("V6Active(%d) = %v, want %v", test.height, got, test.want)
			}
		})
	}
}
