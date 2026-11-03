package observer

import (
	"context"
	"testing"
	"time"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

func signedSolvencyAttestation(t *testing.T, key cryptotypes.PrivKey, solvency *common.Solvency) common.AttestSolvency {
	t.Helper()
	payload, err := solvency.GetSignablePayload()
	require.NoError(t, err)
	signature, err := key.Sign(payload)
	require.NoError(t, err)
	return common.AttestSolvency{
		Solvency: solvency,
		Attestation: &common.Attestation{
			PubKey: key.PubKey().Bytes(), Signature: signature,
		},
	}
}

func TestSolvencyMetricsHandlerRegistersOnlyAcceptedPayloads(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.solvencyMetrics = newTestSolvencyAttestationMetrics()
	m := ag.solvencyMetrics
	solvency := &common.Solvency{
		Chain: common.TRONChain, PubKey: "vault", Height: 100,
		Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(100))),
	}
	valid := signedSolvencyAttestation(t, ag.privKey, solvency)
	invalid := common.AttestSolvency{
		Solvency: solvency,
		Attestation: &common.Attestation{
			PubKey: ag.pubKey, Signature: make([]byte, len(valid.Attestation.Signature)),
		},
	}

	ag.handleSolvencyAttestation(context.Background(), invalid)
	require.Zero(t, ag.solvencies[solvency.Id].AttestationCount())
	require.Empty(t, m.roundState)
	require.Zero(t, testutil.ToFloat64(m.payloads.WithLabelValues(common.TRONChain.String())))
	require.Zero(t, testutil.ToFloat64(m.roundsStarted.WithLabelValues(common.TRONChain.String())))

	// A rejected first signature must not prevent a later accepted signature
	// from registering this existing, previously empty state.
	ag.handleSolvencyAttestation(context.Background(), valid)
	ag.handleSolvencyAttestation(context.Background(), valid)
	ag.handleSolvencyAttestation(context.Background(), invalid)
	require.Equal(t, 1, ag.solvencies[solvency.Id].AttestationCount())
	require.Equal(t, 1, m.roundState[solvencyRoundKeyFor(solvency)].activePayloads)
	require.Equal(t, float64(1), testutil.ToFloat64(m.payloads.WithLabelValues(common.TRONChain.String())))
	require.Equal(t, float64(1), testutil.ToFloat64(m.roundsStarted.WithLabelValues(common.TRONChain.String())))
}

func TestSolvencyMetricsHandlerCleansUpFragmentedRound(t *testing.T) {
	for _, committedRejectedPayload := range []bool{false, true} {
		name := "rejected payload remains pending"
		if committedRejectedPayload {
			name = "rejected payload is committed elsewhere"
		}
		t.Run(name, func(t *testing.T) {
			ag, _, _, _, _, _ := setupTestGossip(t)
			ag.solvencyMetrics = newTestSolvencyAttestationMetrics()
			ag.config.ObserveReconcileInterval = time.Millisecond
			m := ag.solvencyMetrics

			var variants []*common.Solvency
			for amount := uint64(100); amount < 103; amount++ {
				variants = append(variants, &common.Solvency{
					Chain: common.TRONChain, PubKey: "vault", Height: 100,
					Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(amount))),
				})
			}
			for _, solvency := range variants[:2] {
				ag.handleSolvencyAttestation(context.Background(), signedSolvencyAttestation(t, ag.privKey, solvency))
			}
			rejected := signedSolvencyAttestation(t, ag.privKey, variants[2])
			rejected.Attestation.Signature = make([]byte, len(rejected.Attestation.Signature))
			ag.handleSolvencyAttestation(context.Background(), rejected)
			require.Zero(t, ag.solvencies[variants[2].Id].AttestationCount())
			require.Equal(t, 2, m.roundState[solvencyRoundKeyFor(variants[0])].activePayloads)

			if committedRejectedPayload {
				// Another node can commit this payload even though this observer
				// has only rejected a signature for it. Its empty state then expires.
				committed := signedSolvencyAttestation(t, ag.privKey, variants[2])
				quorum := common.QuorumSolvency{
					Solvency: variants[2], Attestations: []*common.Attestation{committed.Attestation},
				}
				payload, err := quorum.Marshal()
				require.NoError(t, err)
				ag.handleQuorumSolvencyCommitted(&ebifrost.EventNotification{Payload: payload})
				ag.solvencies[variants[2].Id].lastCommittedAttestation = time.Now().Add(-2 * ag.config.LateObserveTimeout)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				ag.Start(ctx)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("gossip did not stop after cancellation")
				}
			})

			waitForPrune := func(solvency *common.Solvency) {
				t.Helper()
				require.Eventually(t, func() bool {
					ag.mu.Lock()
					defer ag.mu.Unlock()
					_, exists := ag.solvencies[solvency.Id]
					return !exists
				}, 2*time.Second, time.Millisecond)
			}
			if committedRejectedPayload {
				waitForPrune(variants[2])
				m.mu.Lock()
				activePayloads := m.roundState[solvencyRoundKeyFor(variants[0])].activePayloads
				m.mu.Unlock()
				require.Equal(t, 2, activePayloads, "pruning an unregistered payload must not decrement the round")
			}

			for i, solvency := range variants[:2] {
				ag.mu.Lock()
				state := ag.solvencies[solvency.Id]
				state.mu.Lock()
				state.MarkAttestationsSent(false)
				state.lastAttestationsSent = time.Now().Add(-2 * ag.config.NonQuorumTimeout)
				state.mu.Unlock()
				ag.mu.Unlock()
				waitForPrune(solvency)
				require.Equal(t, float64(i), testutil.ToFloat64(m.rounds.WithLabelValues(common.TRONChain.String(), solvencyRoundOutcomeExpired)))
			}

			m.mu.Lock()
			remainingRounds := len(m.roundState)
			m.mu.Unlock()
			require.Zero(t, remainingRounds)
			require.Equal(t, float64(2), testutil.ToFloat64(m.payloads.WithLabelValues(common.TRONChain.String())))
			require.Equal(t, float64(1), testutil.ToFloat64(m.roundsStarted.WithLabelValues(common.TRONChain.String())))
			require.Zero(t, testutil.ToFloat64(m.rounds.WithLabelValues(common.TRONChain.String(), solvencyRoundOutcomeQuorum)))
		})
	}
}
