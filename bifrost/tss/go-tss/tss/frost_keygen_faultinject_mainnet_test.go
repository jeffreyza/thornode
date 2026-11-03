//go:build !mocknet
// +build !mocknet

package tss

import "testing"

// TestFrostKeygenTestRound3TimeoutDisabledInProductionBuilds pins the safety
// invariant that the round-3 fault-injection hook cannot exist outside mocknet
// builds. Production binaries compile the constant-zero stub
// (frost_keygen_faultinject_mainnet.go), so setting
// XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS must have no effect. This guards
// against an accidental build-tag or refactor regression that would let the
// override shrink the round-3 wait, abandon healthy DKGs, and jail honest
// members in production.
func TestFrostKeygenTestRound3TimeoutDisabledInProductionBuilds(t *testing.T) {
	t.Setenv("XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS", "5000")
	if got := frostKeygenTestRound3TimeoutMS(); got != 0 {
		t.Fatalf("production build must ignore the round-3 timeout override, got %d", got)
	}
}
