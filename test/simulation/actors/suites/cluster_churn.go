package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// ClusterChurn
////////////////////////////////////////////////////////////////////////////////////////

// ClusterChurn activates the cluster, checks Bond Yield, and replaces a validator.
func ClusterChurn(rng *rand.Rand) *Actor {
	return clusterChurn(rng, false)
}

// ClusterChurnWithXMRPostChurn checks the same vault replacement requested by the
// Bond Yield scenario instead of waiting for a later independent rotation.
func ClusterChurnWithXMRPostChurn(rng *rand.Rand) *Actor {
	return ClusterChurnWithXMRPostChurnActors(rng)
}

// ClusterChurnWithPostChurnActors runs checks with the full active validator set
// before Bond Yield selects a target and requests its leave.
func ClusterChurnWithPostChurnActors(rng *rand.Rand, actors ...*Actor) *Actor {
	return clusterChurn(rng, false, actors...)
}

// ClusterChurnWithXMRPostChurnActors also checks the same vault replacement
// requested by Bond Yield after the additional activation checks.
func ClusterChurnWithXMRPostChurnActors(rng *rand.Rand, actors ...*Actor) *Actor {
	return clusterChurn(rng, XMRPostChurnEnabled(), actors...)
}

func clusterChurn(rng *rand.Rand, verifyXMR bool, actors ...*Actor) *Actor {
	state := features.NewBondYieldState()
	a := NewActor("ClusterChurn", rng)

	a.Append(features.NewBondYieldSetupActor(state, rng))
	a.Append(core.NewChurnActorWithSkip(rng, state.SkipReason))
	for _, actor := range actors {
		a.Append(actor)
	}
	a.Append(features.NewBondYieldSelectTargetActor(state, rng))
	a.Append(features.NewBondYieldTargetAutoClaimActor(state, rng))
	var verify *Actor
	if verifyXMR {
		capture, after := core.NewXMRChurnOutChecks(rng)
		a.Append(capture)
		verify = after
	}
	a.Append(features.NewBondYieldChurnOutActor(state, rng))
	a.Append(features.NewBondYieldCleanupActor(rng))
	if verify != nil {
		a.Append(verify)
	}

	return a
}
