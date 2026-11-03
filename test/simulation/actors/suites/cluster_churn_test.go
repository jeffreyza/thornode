package suites

import (
	rand "math/rand/v2"
	"slices"
	"testing"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestClusterChurnRestoresMimirsAfterChurnOut(t *testing.T) {
	a := ClusterChurn(rand.New(rand.NewPCG(5, 6)))
	if a.Name != "ClusterChurn" {
		t.Fatalf("unexpected suite name: %s", a.Name)
	}
	a.InitRoot()

	var cleanup *Actor
	a.WalkDepthFirst(func(actor *Actor) bool {
		if actor.Name == "BondYield-Cleanup" {
			cleanup = actor
		}
		return true
	})
	if cleanup == nil {
		t.Fatal("expected ClusterChurn suite to include Mimir cleanup")
	}

	for parent := range cleanup.Parents() {
		if parent.Name == "BondYield-ChurnOut" {
			return
		}
	}
	t.Fatal("expected Bond Yield Mimir cleanup to run after churn-out verification")
}

func TestClusterChurnCompletesCleanupBeforeNextStage(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	root := NewActor("root", rng)
	root.Append(ClusterChurn(rng))
	nextStage := NewActor("next-stage", rng)
	root.Append(nextStage)
	root.InitRoot()

	var stages []string
	root.WalkDepthFirst(func(actor *Actor) bool {
		stages = append(stages, actor.Name)
		return true
	})
	churnOut := slices.Index(stages, "BondYield-ChurnOut")
	cleanup := slices.Index(stages, "BondYield-Cleanup")
	if churnOut < 0 || cleanup <= churnOut {
		t.Fatalf("expected payout verification followed by cleanup: %v", stages)
	}
	if slices.Contains(stages, "BondYield-ChurnIn") {
		t.Fatalf("unexpected extra validator restoration churn: %v", stages)
	}
	if len(nextStage.Parents()) != 1 {
		t.Fatalf("expected one cleanup dependency, got %d", len(nextStage.Parents()))
	}
	for parent := range nextStage.Parents() {
		if parent.Name != "Mimir(all) ChurnInterval=60" {
			t.Fatalf("next stage can run before the final Mimir is restored: parent=%s", parent.Name)
		}
	}
}
