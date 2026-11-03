package observer

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	solvencyRoundOutcomeQuorum  = "quorum"
	solvencyRoundOutcomeExpired = "expired"

	solvencyRoundMeasureLargestGroup   = "largest_group"
	solvencyRoundMeasureRequiredQuorum = "required_quorum"
)

type solvencyRoundKey struct {
	chain  common.Chain
	pubKey common.PubKey
	height int64
}

type solvencyRoundObservation struct {
	activePayloads       int
	largestGroup         int
	recordedLargestGroup int
	requiredQuorum       int
	completed            bool
}

// solvencyAttestationMetrics provides a TRON-only operational mitigation for
// #3098: it makes latest-state balance fragmentation and failed exact-payload
// quorum visible. It does not make TRON balance reads historical or otherwise
// change solvency consensus. Vault keys, report heights, and payload hashes are
// deliberately excluded from Prometheus labels, and the in-memory round state
// is bounded by the existing solvency-attestation lifecycle.
type solvencyAttestationMetrics struct {
	mu sync.Mutex

	payloads          *prometheus.CounterVec
	roundsStarted     *prometheus.CounterVec
	rounds            *prometheus.CounterVec
	roundAttestations *prometheus.CounterVec
	roundState        map[solvencyRoundKey]*solvencyRoundObservation
}

func newSolvencyAttestationMetrics(m *metrics.Metrics) *solvencyAttestationMetrics {
	if m == nil {
		return nil
	}
	return newSolvencyAttestationMetricsWithCollectors(
		m.GetCounterVec(metrics.SolvencyPayloads),
		m.GetCounterVec(metrics.SolvencyRoundsStarted),
		m.GetCounterVec(metrics.SolvencyRounds),
		m.GetCounterVec(metrics.SolvencyRoundAttestations),
	)
}

func newSolvencyAttestationMetricsWithCollectors(
	payloads *prometheus.CounterVec,
	roundsStarted *prometheus.CounterVec,
	rounds *prometheus.CounterVec,
	roundAttestations *prometheus.CounterVec,
) *solvencyAttestationMetrics {
	return &solvencyAttestationMetrics{
		payloads:          payloads,
		roundsStarted:     roundsStarted,
		rounds:            rounds,
		roundAttestations: roundAttestations,
		roundState:        make(map[solvencyRoundKey]*solvencyRoundObservation),
	}
}

func solvencyRoundKeyFor(solvency *common.Solvency) solvencyRoundKey {
	return solvencyRoundKey{
		chain:  solvency.Chain,
		pubKey: solvency.PubKey,
		height: solvency.Height,
	}
}

// shouldTrackSolvencyMetrics keeps the #3098 instrumentation scoped to TRON.
// Other chains can opt in later if the same unpinned-balance failure mode is
// demonstrated and there is an operational consumer for the additional data.
func shouldTrackSolvencyMetrics(solvency *common.Solvency) bool {
	return solvency != nil && !solvency.Chain.SupportsSolvencyObservationSlashing()
}

func (m *solvencyAttestationMetrics) observePayload(solvency *common.Solvency) {
	if m == nil || !shouldTrackSolvencyMetrics(solvency) {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := solvencyRoundKeyFor(solvency)
	round, ok := m.roundState[key]
	if !ok {
		round = &solvencyRoundObservation{}
		m.roundState[key] = round
		if m.roundsStarted != nil {
			m.roundsStarted.WithLabelValues(solvency.Chain.String()).Inc()
		}
	}
	round.activePayloads++
	if m.payloads != nil {
		m.payloads.WithLabelValues(solvency.Chain.String()).Inc()
	}
}

func (m *solvencyAttestationMetrics) observeAttestations(
	solvency *common.Solvency,
	attestations,
	requiredQuorum int,
	hasQuorum bool,
) {
	if m == nil || !shouldTrackSolvencyMetrics(solvency) {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := solvencyRoundKeyFor(solvency)
	round, ok := m.roundState[key]
	if !ok {
		// Test fixtures can insert attestation state directly. Treat an
		// untracked live payload as the first payload in its logical round.
		round = &solvencyRoundObservation{activePayloads: 1}
		m.roundState[key] = round
		if m.roundsStarted != nil {
			m.roundsStarted.WithLabelValues(solvency.Chain.String()).Inc()
		}
		if m.payloads != nil {
			m.payloads.WithLabelValues(solvency.Chain.String()).Inc()
		}
	}
	if attestations > round.largestGroup {
		round.largestGroup = attestations
	}
	if requiredQuorum > 0 {
		round.requiredQuorum = requiredQuorum
	}
	if hasQuorum && !round.completed {
		m.completeRound(solvency.Chain, solvencyRoundOutcomeQuorum, round)
		round.completed = true
	}
	if round.completed && round.largestGroup > round.recordedLargestGroup {
		if m.roundAttestations != nil {
			m.roundAttestations.WithLabelValues(
				solvency.Chain.String(),
				solvencyRoundOutcomeQuorum,
				solvencyRoundMeasureLargestGroup,
			).Add(float64(round.largestGroup - round.recordedLargestGroup))
		}
		round.recordedLargestGroup = round.largestGroup
	}
}

func (m *solvencyAttestationMetrics) prunePayload(solvency *common.Solvency, requiredQuorum int) {
	if m == nil || !shouldTrackSolvencyMetrics(solvency) {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := solvencyRoundKeyFor(solvency)
	round, ok := m.roundState[key]
	if !ok {
		return
	}
	if requiredQuorum > 0 {
		round.requiredQuorum = requiredQuorum
	}
	round.activePayloads--
	if round.activePayloads > 0 {
		return
	}
	if !round.completed {
		m.completeRound(solvency.Chain, solvencyRoundOutcomeExpired, round)
	}
	delete(m.roundState, key)
}

func (m *solvencyAttestationMetrics) completeRound(
	chain common.Chain,
	outcome string,
	round *solvencyRoundObservation,
) {
	chainLabel := chain.String()
	if m.rounds != nil {
		m.rounds.WithLabelValues(chainLabel, outcome).Inc()
	}
	if m.roundAttestations != nil {
		m.roundAttestations.WithLabelValues(chainLabel, outcome, solvencyRoundMeasureLargestGroup).
			Add(float64(round.largestGroup))
		m.roundAttestations.WithLabelValues(chainLabel, outcome, solvencyRoundMeasureRequiredQuorum).
			Add(float64(round.requiredQuorum))
	}
	round.recordedLargestGroup = round.largestGroup
}

func requiredSolvencyAttestations(activeValidators int) int {
	for attestations := 1; attestations <= activeValidators; attestations++ {
		if thortypes.HasSuperMajority(attestations, activeValidators) {
			return attestations
		}
	}
	return 0
}
