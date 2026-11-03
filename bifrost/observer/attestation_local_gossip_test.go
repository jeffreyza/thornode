package observer

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func seedConflictingPeerVote(t *testing.T, g *AttestationGossip, tx common.ObservedTx, inbound bool) {
	t.Helper()
	key := secp256k1.GenPrivKey()
	peerID, err := conversion.GetPeerIDFromSecp256PubKey(key.PubKey().Bytes())
	require.NoError(t, err)
	g.activeVals[peerID] = true
	payload, err := tx.GetSignablePayloadWithInbound(inbound)
	require.NoError(t, err)
	signature, err := key.Sign(payload)
	require.NoError(t, err)
	g.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx: tx, Inbound: inbound,
		Attestation: &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: signature},
	})
	state := g.observedTxs[observedTxKey(tx, inbound, false)]
	require.NotNil(t, state)
	require.Equal(t, 1, state.AttestationCount())
	require.Empty(t, g.batcher.observedTxBatch, "receiving a peer vote must not enqueue it as a local vote")
}

func TestLocalVoteSurvivesConflictingBucket(t *testing.T) {
	for _, chain := range []common.Chain{common.BTCChain, common.XMRChain} {
		for _, field := range []string{"memo", "gas"} {
			for _, inbound := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/inbound=%t", chain, field, inbound), func(t *testing.T) {
					_, g, _ := setupErrataTestGossip(t)
					local := errataTestObservation("conflicting-bucket", chain, 100)
					local.Tx.Memo = "=:BTC:correct-destination"
					local.Tx.Gas = common.Gas{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(100))}
					conflicting := local
					if field == "memo" {
						conflicting.Tx.Memo = "=:BTC:other-destination"
					} else {
						conflicting.Tx.Gas = common.Gas{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(101))}
					}
					k := observedTxKey(local, inbound, false)
					require.Equal(t, k, observedTxKey(conflicting, inbound, false))
					payload, err := local.GetSignablePayloadWithInbound(inbound)
					require.NoError(t, err)
					seedConflictingPeerVote(t, g, conflicting, inbound)
					for attempt := 0; attempt < 2; attempt++ {
						require.NoError(t, g.AttestObservedTx(context.Background(), &local, inbound, false))
					}
					state := g.observedTxs[k]
					require.Equal(t, conflicting, *state.Item.ObservedTx)
					require.Equal(t, 1, state.AttestationCount(), "incompatible signatures must not enter the existing bucket")
					require.Len(t, g.batcher.observedTxBatch, 2, "local votes and retries must still be queued")
					for _, queued := range g.batcher.observedTxBatch {
						require.Equal(t, local, queued.ObsTx)
						require.Equal(t, inbound, queued.Inbound)
						require.NoError(t, verifySignature(payload, queued.Attestation.Signature, queued.Attestation.PubKey))
					}

					// A peer with the correct payload can collect the delivered vote.
					_, receiver, _ := setupErrataTestGossip(t)
					receiver.activeVals[g.host.ID()] = true
					require.NoError(t, receiver.AttestObservedTx(context.Background(), &local, inbound, false))
					for _, queued := range g.batcher.observedTxBatch {
						receiver.handleObservedTxAttestation(context.Background(), *queued)
					}
					require.Equal(t, 2, receiver.observedTxs[k].AttestationCount(), "the valid vote should be collected once despite retry")
				})
			}
		}
	}
}

func TestConflictingBucketEnqueueRechecksCleanup(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprintf("permanent=%t", permanent), func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			local := errataTestObservation("conflict-cleanup", common.XMRChain, 100)
			local.Tx.Memo = "local payload"
			conflicting := local
			conflicting.Tx.Memo = "peer payload"
			seedConflictingPeerVote(t, g, conflicting, true)
			payload, err := local.GetSignablePayloadWithInbound(true)
			require.NoError(t, err)

			validating, resume := make(chan struct{}), make(chan struct{})
			var pauseOnce, resumeOnce sync.Once
			release := func() { resumeOnce.Do(func() { close(resume) }) }
			defer release()
			originalVerify := verifySignature
			defer func() { verifySignature = originalVerify }()
			verifySignature = func(signBz, signature, pubKey []byte) error {
				if bytes.Equal(signBz, payload) {
					pauseOnce.Do(func() {
						close(validating)
						<-resume
					})
				}
				return originalVerify(signBz, signature, pubKey)
			}
			attested := make(chan struct{})
			var attestErr error
			go func() {
				attestErr = g.AttestObservedTx(context.Background(), &local, true, false)
				close(attested)
			}()
			waitForObserverTest(t, validating)
			cleaned := make(chan struct{})
			var cleanupErr error
			go func() {
				if permanent {
					cleanupErr = o.handleErrataTxCommitted(common.ErrataTx{Chain: common.XMRChain, Id: local.Tx.ID}, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
					if cleanupErr == nil {
						_, cleanupErr = o.reconcileCommittedXMRErrata()
					}
				} else {
					cleanupErr = o.filterErrataTx(btypes.ErrataBlock{
						Height: local.BlockHeight,
						Txs:    []btypes.ErrataTx{{Chain: common.XMRChain, TxID: local.Tx.ID}},
					})
				}
				close(cleaned)
			}()
			waitForObserverTest(t, cleaned)
			release()
			waitForObserverTest(t, attested)
			require.NoError(t, cleanupErr)
			require.NoError(t, attestErr)
			require.Empty(t, g.observedTxs)
			require.Empty(t, g.batcher.observedTxBatch, "validated local delivery must not resurrect an observation after cleanup")
		})
	}
}
