package thorchain

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

// Pending economic retries must not count the same settled quorum again.
func TestErrataConsensusTelemetryCountsSettlementOnce(t *testing.T) {
	for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
		t.Run(chain.String(), func(t *testing.T) {
			ctx, mgr := setupBenchmarkManager(t)
			sink := installRecordingSink(t)
			nodes := NodeAccounts{GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive)}
			for _, node := range nodes {
				require.NoError(t, mgr.Keeper().SetNodeAccount(ctx, node))
			}
			er := &common.ErrataTx{Id: GetRandomTxHash(), Chain: chain}
			// A partial inbound leaves the economic outcome pending after quorum.
			observed := NewObservedTxVoter(er.Id, []common.ObservedTx{{Tx: common.Tx{ID: er.Id, Chain: chain}}})
			mgr.Keeper().SetObservedTxInVoter(ctx, observed)
			voter := NewErrataTxVoter(er.Id, chain)
			metric := "thornode.errata.consensus{chain=" + chain.String() + "}"
			require.NoError(t, processErrataTxAttestation(ctx, mgr, &voter, nodes[0].NodeAddress, nodes, er, false))
			require.Zero(t, sink.counters[metric])
			require.NoError(t, processErrataTxAttestation(ctx, mgr, &voter, nodes[1].NodeAddress, nodes, er, false))
			require.Equal(t, float32(1), sink.counters[metric])
			require.Equal(t, ctx.BlockHeight(), voter.BlockHeight)
			require.Equal(t, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, voter.Outcome)
			for _, node := range nodes {
				require.NoError(t, processErrataTxAttestation(ctx.WithBlockHeight(ctx.BlockHeight()+1), mgr, &voter, node.NodeAddress, nodes, er, false))
			}
			require.Equal(t, float32(1), sink.counters[metric])
		})
	}
}
