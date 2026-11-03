package features

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/constants"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// NewChurnIntervalRestoreActor restores the mocknet CHURNINTERVAL that
// core.ScheduleChurn changes, voting from every currently active simulation
// node. The standalone churn stages run it after they finish, so a later stage
// does not inherit an interval that can exceed 1,000 blocks.
func NewChurnIntervalRestoreActor(rng *rand.Rand) *Actor {
	a := NewActor("ChurnInterval-Restore", rng)
	a.Append(NewMultiNodeMimirActor(
		constants.ChurnInterval.String(),
		constants.NewConstantValue().GetInt64Value(constants.ChurnInterval),
		rng,
	))
	return a
}
