package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	apicommon "gitlab.com/thorchain/thornode/v3/api/common"
	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestErrataOutcomeWireAndIdentity(t *testing.T) {
	er := &common.ErrataTx{Id: common.TxID("tx-id"), Chain: common.XMRChain}
	before, err := er.GetSignablePayload()
	require.NoError(t, err)
	for _, outcome := range []common.ErrataTxOutcome{
		common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED,
		common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED,
		common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION,
	} {
		quorum := &common.QuorumErrataTx{ErrataTx: er, Outcome: outcome}
		wire, err := quorum.Marshal()
		require.NoError(t, err)
		var decoded apicommon.QuorumErrataTx
		require.NoError(t, protov2.Unmarshal(wire, &decoded))
		require.Equal(t, int32(outcome), int32(decoded.Outcome))
		wire, err = protov2.Marshal(&decoded)
		require.NoError(t, err)
		var back common.QuorumErrataTx
		require.NoError(t, back.Unmarshal(wire))
		require.Equal(t, outcome, back.Outcome)
		payload, err := back.ErrataTx.GetSignablePayload()
		require.NoError(t, err)
		require.Equal(t, before, payload)
		require.True(t, back.Equals(&common.QuorumErrataTx{ErrataTx: er}), "outcome never changes errata identity")
		voter := NewErrataTxVoter(er.Id, er.Chain)
		voter.BlockHeight, voter.Outcome = 100, outcome
		wire, err = voter.Marshal()
		require.NoError(t, err)
		var apiVoter apitypes.ErrataTxVoter
		require.NoError(t, protov2.Unmarshal(wire, &apiVoter))
		require.Equal(t, int32(outcome), int32(apiVoter.Outcome))
		wire, err = protov2.Marshal(&apiVoter)
		require.NoError(t, err)
		var restored ErrataTxVoter
		require.NoError(t, restored.Unmarshal(wire))
		require.Equal(t, voter, restored)
		require.Equal(t, "XMR-tx-id", restored.String(), "voter key remains TxID-based")
	}
	for _, chain := range []common.Chain{common.BTCChain, common.ETHChain, common.XMRChain} {
		require.Equal(t, chain.Equals(common.XMRChain), chain.RequiresTerminalErrataOutcome())
	}
}
