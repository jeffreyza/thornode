package observer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

func stateImportValidationBatch(t *testing.T, category string, att *common.Attestation) (common.QuorumState, AttestableItem) {
	t.Helper()
	atts := []*common.Attestation{att}
	switch category {
	case "transaction":
		tx := common.ObservedTx{Tx: common.Tx{Chain: common.BTCChain, ID: "import-validation"}, BlockHeight: 100}
		return common.QuorumState{QuoTxs: []*common.QuorumTx{{ObsTx: tx, Attestations: atts}}}, &attestableObservedTx{ObservedTx: &tx}
	case "network fee":
		fee, _ := stateSyncSignedFee(t, 150, 1)
		return common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}}, fee
	case "solvency":
		solvency := &common.Solvency{Chain: common.BTCChain, Height: 150}
		return common.QuorumState{QuoSolvencies: []*common.QuorumSolvency{{Solvency: solvency, Attestations: atts}}}, solvency
	default:
		errata := &common.ErrataTx{Chain: common.BTCChain, Id: "import-validation"}
		return common.QuorumState{QuoErrataTxs: []*common.QuorumErrataTx{{ErrataTx: errata, Attestations: atts}}}, errata
	}
}

func requireRejectedStateImportThenRecovery(t *testing.T, g *AttestationGossip, h *MockHost, rejected, valid []byte) {
	t.Helper()
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		return stateSyncImportStream(t, id, rejected), nil
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 2)
	g.tickAttestationStateSync(context.Background(), true, false, now)
	settleStartupRetry(t, g, now)
	require.False(t, g.startupState.finished, "a rejected-only snapshot must not stop retries")
	require.Equal(t, attestationStateGroupResult{empty: 1}, g.startupState.result)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		return stateSyncImportStream(t, id, valid), nil
	}
	dispatch := g.startupState.next
	g.tickAttestationStateSync(context.Background(), true, false, dispatch)
	settleStartupRetry(t, g, dispatch)
	require.True(t, g.startupState.finished)
	require.Equal(t, 2, g.startupState.requests)
	require.Equal(t, attestationStateGroupResult{empty: 1, nonEmpty: 1}, g.startupState.result)
}

func TestStateImportDuplicateRequiresRetainedSigner(t *testing.T) {
	for _, category := range []string{"transaction", "network fee", "solvency", "errata"} {
		t.Run(category, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			key := secp256k1.GenPrivKey()
			att := &common.Attestation{PubKey: key.PubKey().Bytes()}
			batch, item := stateImportValidationBatch(t, category, att)
			payload, err := item.GetSignablePayload()
			require.NoError(t, err)
			att.Signature, err = key.Sign(payload)
			require.NoError(t, err)
			valid, err := batch.Marshal()
			require.NoError(t, err)
			for range 2 {
				result := g.askStreamForState(stateSyncImportStream(t, "peer", valid))
				require.True(t, result.completed)
				require.NoError(t, result.err)
				require.Equal(t, 1, result.items, "a valid retained duplicate counts")
			}
			att.PubKey = secp256k1.GenPrivKey().PubKey().Bytes()
			require.Error(t, verifySignature(payload, att.Signature, att.PubKey))
			forged, err := batch.Marshal()
			require.NoError(t, err)
			stream := stateSyncImportStream(t, "peer", forged)
			result := g.askStreamForState(stream)
			require.True(t, result.completed)
			require.NoError(t, result.err)
			require.Zero(t, result.items)
			require.EqualValues(t, 1, stream.closes.Load(), "definitive rejection still completes the wire exchange")
			requireRejectedStateImportThenRecovery(t, g, h, forged, valid)
		})
	}
}

func TestStateImportDuplicateRequiresRetainedPayload(t *testing.T) {
	for _, category := range []string{"transaction", "solvency"} {
		for _, resign := range []bool{false, true} {
			name := category + "/retained signature"
			if resign {
				name = category + "/new signature"
			}
			t.Run(name, func(t *testing.T) {
				g, h := startupRetryGossip(t, "peer")
				key := secp256k1.GenPrivKey()
				att := &common.Attestation{PubKey: key.PubKey().Bytes()}
				batch, item := stateImportValidationBatch(t, category, att)
				payload, err := item.GetSignablePayload()
				require.NoError(t, err)
				att.Signature, err = key.Sign(payload)
				require.NoError(t, err)
				valid, err := batch.Marshal()
				require.NoError(t, err)
				require.Equal(t, 1, g.askStreamForState(stateSyncImportStream(t, "peer", valid)).items)
				var conflicting AttestableItem
				if category == "transaction" {
					before := observedTxKey(batch.QuoTxs[0].ObsTx, false, false)
					batch.QuoTxs[0].ObsTx.ObservedPubKey = "different-vault"
					require.Equal(t, before, observedTxKey(batch.QuoTxs[0].ObsTx, false, false))
					conflicting = &attestableObservedTx{ObservedTx: &batch.QuoTxs[0].ObsTx}
				} else {
					before, hashErr := batch.QuoSolvencies[0].Solvency.Hash()
					require.NoError(t, hashErr)
					batch.QuoSolvencies[0].Solvency.Id = common.TxID(strings.Repeat("B", 64))
					after, hashErr := batch.QuoSolvencies[0].Solvency.Hash()
					require.NoError(t, hashErr)
					require.Equal(t, before, after)
					conflicting = batch.QuoSolvencies[0].Solvency
				}
				changed, err := conflicting.GetSignablePayload()
				require.NoError(t, err)
				require.NotEqual(t, payload, changed)
				if resign {
					att.Signature, err = key.Sign(changed)
					require.NoError(t, err)
				} else {
					require.Error(t, verifySignature(changed, att.Signature, att.PubKey))
				}
				rejected, err := batch.Marshal()
				require.NoError(t, err)
				requireRejectedStateImportThenRecovery(t, g, h, rejected, valid)
			})
		}
	}
}

func TestStateImportPriceFeedFieldsBeforeRetention(t *testing.T) {
	for _, invalid := range []string{"empty rates", "zero time", "negative time", "invalid version"} {
		t.Run(invalid, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			g.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
			g.priceFeedsPool = NewAttestationStatePool[*common.PriceFeed]()
			g.priceFeedsDelay = NewDelay()
			g.priceFeedsDelay.Start()
			key := secp256k1.GenPrivKey()
			feed := &common.PriceFeed{Time: 151, Rates: []*common.OraclePrice{{Amount: 1, Decimals: 8}}, Version: make([]byte, 8)}
			encode := func() []byte {
				t.Helper()
				payload, err := feed.GetSignablePayload()
				require.NoError(t, err)
				sig, err := key.Sign(payload)
				require.NoError(t, err)
				require.NoError(t, verifySignature(payload, sig, key.PubKey().Bytes()))
				data, err := (&common.QuorumState{QuoPriceFeeds: []*common.QuorumPriceFeed{{
					PriceFeed: feed, Attestations: []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: sig}},
				}}}).Marshal()
				require.NoError(t, err)
				return data
			}
			require.NoError(t, feed.Valid())
			valid := encode()
			switch invalid {
			case "empty rates":
				feed.Rates = nil
			case "zero time":
				feed.Time = 0
			case "negative time":
				feed.Time = -1
			case "invalid version":
				feed.Version = nil
			}
			require.Error(t, feed.Valid())
			rejected := encode()
			result := g.askStreamForState(stateSyncImportStream(t, "peer", rejected))
			require.True(t, result.completed)
			require.NoError(t, result.err)
			require.Zero(t, result.items)
			require.Empty(t, g.priceFeeds, "invalid fields must not enter retained state")
			requireRejectedStateImportThenRecovery(t, g, h, rejected, valid)
			require.Len(t, g.priceFeeds, 1)
			duplicate := g.askStreamForState(stateSyncImportStream(t, "peer", valid))
			require.Equal(t, 1, duplicate.items)
			client, ok := g.grpcClient.(*MockGRPCClient)
			require.True(t, ok)
			called := false
			client.sendQuorumPriceFeedBatchFunc = func(context.Context, *common.QuorumPriceFeedBatch, ...grpc.CallOption) (*ebifrost.SendQuorumPriceFeedBatchResult, error) {
				called = true
				return nil, errors.New("temporary downstream failure")
			}
			g.sendPriceFeedAttestationsToThornode(context.Background())
			require.True(t, called)
			require.Len(t, g.priceFeeds, 1, "later delivery failure does not undo valid retention")
		})
	}
}
