package solana

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

// TestNonceProvisionCadence: the provisioning cadence is below the keep-alive cadence
// (C) and the boundary advances deterministically from the shared THORChain height.
func TestNonceProvisionCadence(t *testing.T) {
	require.Less(t, int64(nonceProvisionThorBlockCadence), int64(keepAliveThorBlockCadence),
		"provisioning cadence must be below keep-alive's 50")
	require.EqualValues(t, 20, nonceProvisionThorBlockCadence)

	c := &SOLClient{}
	// Fresh node (multiple 0) jumps straight to the current boundary.
	require.EqualValues(t, 5, c.nextNonceProvisionThorMultiple(105)) // 105/20 = 5
	require.EqualValues(t, 4, c.nextNonceProvisionThorMultiple(100)) // 100/20=5, exact -> 4

	// After acting on boundary 5, it does not re-fire until the next boundary, then
	// advances one at a time (members with slightly different tips converge).
	c.setNonceProvisionThorMultiple(5)
	require.EqualValues(t, 5, c.nextNonceProvisionThorMultiple(110)) // still in boundary 5
	require.EqualValues(t, 5, c.nextNonceProvisionThorMultiple(119))
	require.EqualValues(t, 6, c.nextNonceProvisionThorMultiple(121)) // crossed into 6
}

// TestVaultHasPendingSOLOutbound: the create-skip decision (D) — skip a vault whose
// SOL outbound is pending; fire when its queue is empty; ignore other vaults/chains.
func TestVaultHasPendingSOLOutbound(t *testing.T) {
	sol := common.SOLChain.String()
	pend := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", VaultPubKeyEddsa: "vaultAe", Height: 100},
		{Chain: "ETH", VaultPubKey: "vaultB", Height: 101},
	}
	// vault A has a pending SOL outbound (match by either pubkey) -> skip create.
	require.True(t, vaultHasPendingSOLOutbound(pend, "vaultA", "x"))
	require.True(t, vaultHasPendingSOLOutbound(pend, "x", "vaultAe"))
	// vault B's pending item is ETH, not SOL -> does not block a SOL create.
	require.False(t, vaultHasPendingSOLOutbound(pend, "vaultB", "vaultBe"))
	// vault C has nothing pending -> fire the create.
	require.False(t, vaultHasPendingSOLOutbound(pend, "vaultC", "vaultCe"))
	// empty queue -> fire.
	require.False(t, vaultHasPendingSOLOutbound(nil, "vaultA", "vaultAe"))
}
