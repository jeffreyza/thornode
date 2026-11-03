package observer

import (
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A case-only envelope transformation preserves the original signatures. The
// voter retains that representation, and it must remain equivalent at completion.
func TestPendingXMRCertificateCompletesEquivalentSignedVoter(t *testing.T) {
	obs, _ := observerTestXMRPlan(t)
	obs.BlockHeight = obs.FinaliseHeight
	obs.XmrTxKey = strings.Repeat("ab", 32)
	upper := obs
	upper.XmrTxKey = strings.ToUpper(obs.XmrTxKey)
	require.NoError(t, upper.Valid())
	lowerBytes, err := obs.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	upperBytes, err := upper.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	require.Equal(t, lowerBytes, upperBytes)
	voter := thortypes.NewObservedTxVoter(obs.Tx.ID, nil)
	var nodes thortypes.NodeAccounts
	for i := 0; i < 4; i++ {
		key := secp256k1.GenPrivKey()
		node := thortypes.NodeAccount{NodeAddress: cosmos.AccAddress(key.PubKey().Address()), Status: thortypes.NodeStatus_Active}
		nodes = append(nodes, node)
		if i < 3 {
			sig, signErr := key.Sign(lowerBytes)
			require.NoError(t, signErr)
			require.NoError(t, verifySignature(upperBytes, sig, key.PubKey().Bytes()))
			require.True(t, voter.Add(upper, node.NodeAddress))
		}
	}
	canonical := voter.GetTx(nodes)
	require.NotNil(t, canonical)
	require.Equal(t, upper.XmrTxKey, canonical.XmrTxKey)
	require.True(t, obs.HasEquivalentXMRTxKey(*canonical))
	voter.Tx = *canonical
	encoded, err := voter.Marshal()
	require.NoError(t, err)
	var stored thortypes.ObservedTxVoter
	require.NoError(t, stored.Unmarshal(encoded))
	query := certificateCompletionTestVoter(stored.Tx)
	require.True(t, pendingXMRCertificateComplete(obs, query), "equivalent signed key casing must complete the outbox record")
	query.Tx.XmrTxKey = obs.XmrTxKey
	require.True(t, pendingXMRCertificateComplete(obs, query))
	for _, mutation := range []string{"different key", "wrong vault", "wrong chain", "missing refs", "different memo", "different refs", "not finalized", "nonfinal observation"} {
		q := certificateCompletionTestVoter(obs)
		switch mutation {
		case "different key":
			q.Tx.XmrTxKey = strings.Repeat("cd", 32)
		case "wrong vault":
			q.Tx.ObservedPubKey = common.PubKey("different-vault")
		case "wrong chain":
			q.Tx.Tx.Chain = common.BTCChain
		case "missing refs":
			q.Tx.SpentOutputRefs = nil
		case "different refs":
			q.Tx.SpentOutputRefs[0].KeyImage = strings.Repeat("ef", 32)
		case "not finalized":
			q.FinalisedHeight = 0
		case "nonfinal observation":
			q.Tx.BlockHeight = q.Tx.FinaliseHeight - 1
		case "different memo":
			q.Tx.Tx.Memo = "MIGRATE:999"
		}
		require.False(t, pendingXMRCertificateComplete(obs, q), mutation)
	}
	query.Reverted = true
	query.Tx.XmrTxKey = "different"
	require.True(t, pendingXMRCertificateComplete(obs, query), "authoritative reversion remains terminal")
}

func certificateCompletionTestVoter(obs common.ObservedTx) *thortypes.QueryObservedTxVoter {
	refs := make([]*common.OutputRef, len(obs.SpentOutputRefs))
	for i := range obs.SpentOutputRefs {
		ref := obs.SpentOutputRefs[i]
		refs[i] = &ref
	}
	return &thortypes.QueryObservedTxVoter{
		TxID: obs.Tx.ID, Height: 123, FinalisedHeight: 124,
		Tx: thortypes.QueryObservedTx{
			Tx: obs.Tx, ObservedPubKey: obs.ObservedPubKey,
			BlockHeight: obs.FinaliseHeight, FinaliseHeight: obs.FinaliseHeight,
			XmrTxKey: obs.XmrTxKey, SpentOutputRefs: refs,
		},
	}
}
