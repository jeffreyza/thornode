package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Churn, ChurnBalance
////////////////////////////////////////////////////////////////////////////////////////

// Churn runs the standalone churn and then restores CHURNINTERVAL, which the
// churn changes through core.ScheduleChurn. ClusterChurn restores it through
// the Bond Yield cleanup instead.
func Churn(rng *rand.Rand) *Actor {
	a := NewActor("ChurnSuite", rng)
	a.Append(core.NewChurnActor(rng))
	a.Append(features.NewChurnIntervalRestoreActor(rng))
	return a
}

// ChurnBalance runs the ERC20 migration check, which schedules its own churn,
// and then restores CHURNINTERVAL once the migration has been verified.
func ChurnBalance(rng *rand.Rand) *Actor {
	a := NewActor("ChurnBalanceSuite", rng)
	a.Append(core.NewChurnBalanceActor(rng))
	a.Append(features.NewChurnIntervalRestoreActor(rng))
	return a
}
