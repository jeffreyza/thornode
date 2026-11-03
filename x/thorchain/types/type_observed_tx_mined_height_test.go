package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestXMRMinedHeightRequiresIndependentQuorum(t *testing.T) {
	accounts := make(NodeAccounts, 95)
	for i := range accounts {
		accounts[i] = GetRandomValidatorNode(NodeStatus_Active)
	}
	zero := common.NewObservedTx(GetRandomTx(), 110, GetRandomPubKey(), 110)
	zero.Tx.Chain = common.XMRChain
	valid := zero
	valid.XmrMinedHeight = 100
	voter := NewObservedTxVoter(zero.Tx.ID, nil)
	for i := 0; i < 63; i++ {
		require.True(t, voter.Add(zero, accounts[i].NodeAddress))
	}
	for i := 0; i < 64; i++ {
		// Reconstruct on every arrival to cover persisted variant identity.
		encoded, err := voter.Marshal()
		require.NoError(t, err)
		var reloaded ObservedTxVoter
		require.NoError(t, reloaded.Unmarshal(encoded))
		voter = reloaded
		signer := accounts[(i+63)%len(accounts)].NodeAddress
		require.True(t, voter.Add(valid, signer))
		require.False(t, voter.Add(valid, signer))
		require.Equal(t, i == 63, voter.HasFinalised(accounts))
	}
	voter.Tx = *voter.GetTx(accounts)
	require.Equal(t, int64(100), voter.Tx.XmrMinedHeight)
	require.Len(t, voter.GetConsensusSigners(), 64)
	require.Len(t, voter.GetObserveSigners(), 64)
	// Credit can include legacy inbound or pre-mining outbound observers,
	// while quorum still requires independent votes for the concrete height.
	require.Len(t, voter.GetInboundObserveSigners(), 95)
	require.Len(t, voter.GetOutboundObserveSigners(), 95)
	require.Len(t, voter.GetConsensusSigners(), 64)
}
