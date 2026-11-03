package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
)

// TestIsTestMemo pins the monitoring exemption to the mocknet simulation
// environment and exact memo equality: memos are sender-controlled input on
// live networks, so neither an embedded marker nor the exact marker on
// mainnet may suppress monitoring.
func TestIsTestMemo(t *testing.T) {
	orig := config.Get()
	defer config.Set(orig)

	cfg := orig
	cfg.Network = "mocknet"
	config.Set(cfg)

	require.True(t, isTestMemo("TEST-INACTIVE-VAULT-REFUND"))

	// exact equality — embedded or decorated markers never match
	require.False(t, isTestMemo(""))
	require.False(t, isTestMemo("test-inactive-vault-refund"))
	require.False(t, isTestMemo("TEST-INACTIVE-VAULT-REFUND:extra"))
	require.False(t, isTestMemo("SWAP:BTC.BTC:bc1q...:0/1/0:TEST-INACTIVE-VAULT-REFUND"))
	require.False(t, isTestMemo(" TEST-INACTIVE-VAULT-REFUND"))
	require.False(t, isTestMemo("TEST-INACTIVE-VAULT-REFUND "))

	// on live networks even the exact marker never matches
	for _, network := range []string{"mainnet", "stagenet", ""} {
		cfg.Network = network
		config.Set(cfg)
		require.False(t, isTestMemo("TEST-INACTIVE-VAULT-REFUND"), "network %q must not exempt test memos", network)
	}
}
