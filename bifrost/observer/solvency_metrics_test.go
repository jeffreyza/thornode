package observer

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func newTestSolvencyAttestationMetrics() *solvencyAttestationMetrics {
	return newSolvencyAttestationMetricsWithCollectors(
		prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_solvency_payloads_total",
			Help: "number of distinct exact TRON solvency payload variants observed",
		}, []string{"chain"}),
		prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_solvency_rounds_started_total",
			Help: "number of logical TRON solvency rounds observed, grouped by chain, vault, and report height internally",
		}, []string{"chain"}),
		prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_solvency_rounds_total",
			Help: "number of TRON solvency rounds that reached exact-payload quorum or expired without it",
		}, []string{"chain", "outcome"}),
		prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_solvency_round_attestations_total",
			Help: "sum of largest exact-payload attestation groups and required quorum sizes when TRON solvency rounds reach quorum or expire",
		}, []string{"chain", "outcome", "measure"}),
	)
}

func TestSolvencyAttestationMetricsRecordsFragmentedQuorumRound(t *testing.T) {
	m := newTestSolvencyAttestationMetrics()
	first := &common.Solvency{
		Chain: common.TRONChain, PubKey: "vault", Height: 85947441,
		Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(292047171700))),
	}
	second := &common.Solvency{
		Chain: common.TRONChain, PubKey: "vault", Height: 85947441,
		Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(292047172000))),
	}

	m.observePayload(first)
	m.observePayload(second)
	firstHash, err := first.Hash()
	assert.NoError(t, err)
	secondHash, err := second.Hash()
	assert.NoError(t, err)
	assert.NotEqual(t, firstHash, secondHash)
	m.observeAttestations(first, 38, 38, true)
	m.observeAttestations(second, 4, 38, false)
	// Late attestations update the largest group without counting the logical
	// round twice, matching the observed 49-versus-4 fragmentation case.
	m.observeAttestations(first, 49, 38, true)

	assert.Equal(t, float64(2), testutil.ToFloat64(m.payloads.WithLabelValues(common.TRONChain.String())))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.roundsStarted.WithLabelValues(common.TRONChain.String())))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.rounds.WithLabelValues(common.TRONChain.String(), solvencyRoundOutcomeQuorum)))
	assert.Equal(t, float64(49), testutil.ToFloat64(m.roundAttestations.WithLabelValues(
		common.TRONChain.String(), solvencyRoundOutcomeQuorum, solvencyRoundMeasureLargestGroup,
	)))
	assert.Equal(t, float64(38), testutil.ToFloat64(m.roundAttestations.WithLabelValues(
		common.TRONChain.String(), solvencyRoundOutcomeQuorum, solvencyRoundMeasureRequiredQuorum,
	)))

	m.prunePayload(first, 38)
	m.prunePayload(second, 38)
	assert.Empty(t, m.roundState)
	assert.Equal(t, float64(0), testutil.ToFloat64(m.rounds.WithLabelValues(common.TRONChain.String(), solvencyRoundOutcomeExpired)))
}

func TestSolvencyAttestationMetricsRecordsExpiredRoundOnce(t *testing.T) {
	m := newTestSolvencyAttestationMetrics()
	first := &common.Solvency{
		Chain: common.TRONChain, PubKey: "vault", Height: 85947451,
		Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(292047171700))),
	}
	second := &common.Solvency{
		Chain: common.TRONChain, PubKey: "vault", Height: 85947451,
		Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(292047172000))),
	}

	m.observePayload(first)
	m.observePayload(second)
	assert.Equal(t, float64(1), testutil.ToFloat64(m.roundsStarted.WithLabelValues(common.TRONChain.String())))
	m.observeAttestations(first, 30, 38, false)
	m.observeAttestations(second, 27, 38, false)

	m.prunePayload(first, 38)
	assert.Equal(t, float64(0), testutil.ToFloat64(m.rounds.WithLabelValues(common.TRONChain.String(), solvencyRoundOutcomeExpired)))
	m.prunePayload(second, 38)

	assert.Equal(t, float64(1), testutil.ToFloat64(m.rounds.WithLabelValues(common.TRONChain.String(), solvencyRoundOutcomeExpired)))
	assert.Equal(t, float64(30), testutil.ToFloat64(m.roundAttestations.WithLabelValues(
		common.TRONChain.String(), solvencyRoundOutcomeExpired, solvencyRoundMeasureLargestGroup,
	)))
	assert.Equal(t, float64(38), testutil.ToFloat64(m.roundAttestations.WithLabelValues(
		common.TRONChain.String(), solvencyRoundOutcomeExpired, solvencyRoundMeasureRequiredQuorum,
	)))
	assert.Empty(t, m.roundState)
}

func TestRequiredSolvencyAttestationsUsesConsensusThreshold(t *testing.T) {
	tests := map[int]int{
		0:  0,
		1:  1,
		3:  2,
		4:  3,
		57: 38,
	}
	for activeValidators, expected := range tests {
		assert.Equal(t, expected, requiredSolvencyAttestations(activeValidators))
	}
}

func TestSolvencyAttestationMetricsIgnoresNonTronRounds(t *testing.T) {
	m := newTestSolvencyAttestationMetrics()
	solvency := &common.Solvency{Chain: common.ETHChain, PubKey: "vault", Height: 1234}

	m.observePayload(solvency)
	m.observeAttestations(solvency, 3, 3, true)
	m.prunePayload(solvency, 3)

	assert.Empty(t, m.roundState)
	assert.Zero(t, testutil.CollectAndCount(m.payloads))
	assert.Zero(t, testutil.CollectAndCount(m.roundsStarted))
	assert.Zero(t, testutil.CollectAndCount(m.rounds))
	assert.Zero(t, testutil.CollectAndCount(m.roundAttestations))
}
