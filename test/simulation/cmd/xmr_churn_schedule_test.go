package main

import (
	rand "math/rand/v2"
	"testing"

	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestSimulationSharesClusterChurnWithXMR(t *testing.T) {
	previous := acommon.XMRChainEnabled()
	t.Cleanup(func() { acommon.SetXMRChainEnabled(previous) })
	for _, tc := range []struct {
		name                       string
		coverage                   bool
		required                   string
		stages                     map[string]bool
		integrated                 bool
		clusters, churns, verifies int
	}{
		{"optional develop", false, "0", map[string]bool{"cluster-churn": true, "churn": true, "xmr-post-churn": true}, false, 1, 1, 0},
		{"enabled XMR", true, "0", map[string]bool{"cluster-churn": true, "churn": true, "xmr-post-churn": true}, true, 1, 1, 1},
		{"required without initial XMR", false, "1", map[string]bool{"cluster-churn": true, "xmr-post-churn": true}, true, 1, 1, 1},
		{"standalone readiness", false, "1", map[string]bool{"xmr-post-churn": true}, false, 0, 0, 1},
		{"ordinary churn", false, "0", map[string]bool{"churn": true}, false, 0, 1, 0},
		{"legacy selector", false, "0", map[string]bool{"bond-yield": true, "churn": true, "xmr-post-churn": true}, false, 1, 1, 0},
		{"legacy selector required XMR", false, "1", map[string]bool{"bond-yield": true, "churn": true, "xmr-post-churn": true}, true, 1, 1, 1},
		{"both selectors required XMR", false, "1", map[string]bool{"bond-yield": true, "cluster-churn": true, "churn": true, "xmr-post-churn": true}, true, 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XMR_POST_CHURN_REQUIRED", tc.required)
			acommon.SetXMRChainEnabled(tc.coverage)
			rng := rand.New(rand.NewPCG(5, 6))
			root := NewActor("Root", rng)
			appendSimulationStages(root, tc.stages, rng)
			root.InitRoot()
			actors := map[string][]*Actor{}
			root.WalkDepthFirst(func(a *Actor) bool {
				actors[a.Name] = append(actors[a.Name], a)
				return true
			})
			if len(actors["ClusterChurn"]) != tc.clusters || len(actors["BondYield"]) != 0 {
				t.Fatalf("unexpected cluster suite counts: %d/%d", len(actors["ClusterChurn"]), len(actors["BondYield"]))
			}
			verifyName := "XMRPostChurn BTC.BTC => XMR.XMR"
			if len(actors["Churn"]) != tc.churns || len(actors[verifyName]) != tc.verifies {
				t.Fatalf("unexpected churn/verification counts: %d/%d", len(actors["Churn"]), len(actors[verifyName]))
			}
			if !tc.integrated {
				if len(actors["XMRPreChurnOut"]) != 0 {
					t.Fatal("unexpected integrated XMR capture")
				}
				return
			}
			for _, name := range []string{"XMRPreChurnOut", "BondYield-ChurnOut", "BondYield-Cleanup", verifyName} {
				if len(actors[name]) != 1 {
					t.Fatalf("expected exactly one %s", name)
				}
			}
			capture := actors["XMRPreChurnOut"][0]
			leave := actors["BondYield-ChurnOut"][0]
			cleanup := actors["BondYield-Cleanup"][0]
			verify := actors[verifyName][0]
			if !leave.Parents()[capture] || !cleanup.Parents()[leave] {
				t.Fatal("capture must precede leave, then Mimir cleanup")
			}
			// Append must join after all Mimir restoration children, not just
			// after the empty cleanup suite actor.
			reached := false
			cleanup.WalkDepthFirst(func(a *Actor) bool {
				if a == verify {
					reached = true
					return false
				}
				if len(a.Children) == 1 && a.Children[verify] && !verify.Parents()[a] {
					t.Fatal("verification did not depend on cleanup completion")
				}
				return true
			})
			if !reached || verify.Parents()[cleanup] || len(verify.Parents()) == 0 {
				t.Fatal("XMR verification must wait for the cleanup operations")
			}
		})
	}
}

func TestAllSimulationStagesShareClusterChurn(t *testing.T) {
	counts := map[string]int{}
	for _, stage := range selectedSimulationStages(map[string]bool{"all": true}) {
		counts[stage.name]++
	}
	if counts["cluster-churn"] != 1 || counts["bond-yield"] != 0 || counts["churn"] != 0 || counts["xmr-post-churn"] != 0 {
		t.Fatalf("all must share the ClusterChurn suite: %v", counts)
	}
	if counts["bootstrap"] != 1 || counts["ragnarok"] != 1 {
		t.Fatal("unrelated stages must remain selected")
	}
}

func TestSimulationStageOptionsUseClusterChurn(t *testing.T) {
	canonical := 0
	for _, option := range simulationStageOptions() {
		switch option.Name {
		case "cluster-churn":
			canonical++
		case "bond-yield":
			t.Fatal("the legacy selector must not appear as a separate interactive stage")
		}
	}
	if canonical != 1 {
		t.Fatalf("expected one cluster-churn option, got %d", canonical)
	}
}

func TestXMRVerificationExecutionOrder(t *testing.T) {
	previous := acommon.XMRChainEnabled()
	t.Cleanup(func() { acommon.SetXMRChainEnabled(previous) })
	acommon.SetXMRChainEnabled(false)
	t.Setenv("XMR_POST_CHURN_REQUIRED", "1")
	for _, shared := range []bool{false, true} {
		name := "standalone"
		if shared {
			name = "shared cluster churn"
		}
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(5, 6))
			root := NewActor("Root", rng)
			appendSimulationStages(root, map[string]bool{
				"cluster-churn": shared, "churn": true,
				"solvency": true, "xmr-post-churn": true, "ragnarok": true,
			}, rng)
			root.InitRoot()
			actors := map[string]*Actor{}
			root.WalkDepthFirst(func(a *Actor) bool {
				actors[a.Name] = a
				return true
			})
			verify := actors["XMRPostChurn BTC.BTC => XMR.XMR"]
			solvency := actors["SolvencyCheck"]
			churn := actors["Churn"]
			ragnarok := actors["Ragnarok"]
			if verify == nil || solvency == nil || churn == nil || ragnarok == nil {
				t.Fatalf("missing execution actors: %v", actors)
			}
			if !actorDependsOn(verify, churn) || !actorDependsOn(ragnarok, verify) || !actorDependsOn(ragnarok, solvency) {
				t.Fatal("churn must precede verification; Ragnarok must follow verification and solvency")
			}
			if shared {
				// Shared verification runs inside ClusterChurn, so solvency
				// checks the state after its verification swap.
				if !actorDependsOn(solvency, verify) || actorDependsOn(verify, solvency) {
					t.Fatal("shared XMR verification must finish before solvency")
				}
			} else if !actorDependsOn(verify, solvency) || actorDependsOn(solvency, verify) {
				t.Fatal("standalone XMR verification must retain its later stage position")
			}
		})
	}
}

func actorDependsOn(after, before *Actor) bool {
	seen := map[*Actor]bool{}
	pending := []*Actor{after}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for parent := range current.Parents() {
			if parent == before {
				return true
			}
			if !seen[parent] {
				seen[parent] = true
				pending = append(pending, parent)
			}
		}
	}
	return false
}

func TestStandaloneChurnStagesRestoreChurnInterval(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	root := NewActor("Root", rng)
	appendSimulationStages(root, map[string]bool{"churn": true, "churn-balance": true}, rng)
	root.InitRoot()
	actors := map[string][]*Actor{}
	root.WalkDepthFirst(func(a *Actor) bool {
		actors[a.Name] = append(actors[a.Name], a)
		return true
	})
	if len(actors["Churn"]) != 1 || len(actors["ChurnBalance"]) != 1 || len(actors["BondYield-Cleanup"]) != 0 {
		t.Fatalf("unexpected churn suite counts: %d/%d/%d",
			len(actors["Churn"]), len(actors["ChurnBalance"]), len(actors["BondYield-Cleanup"]))
	}
	cleanups := actors["ChurnInterval-Restore"]
	if len(cleanups) != 2 {
		t.Fatalf("expected a CHURNINTERVAL cleanup after each churn stage, got %d", len(cleanups))
	}
	churn, balance := actors["Churn"][0], actors["ChurnBalance"][0]
	var churnCleanup, balanceCleanup *Actor
	for _, cleanup := range cleanups {
		switch {
		case cleanup.Parents()[churn]:
			churnCleanup = cleanup
		case cleanup.Parents()[balance]:
			balanceCleanup = cleanup
		}
	}
	if churnCleanup == nil || balanceCleanup == nil {
		t.Fatal("each cleanup must run after its churn stage's verification")
	}

	// The churn-balance rotation must start only after the first cleanup's
	// vote, or the restore could overwrite the interval it schedules.
	for vote := range churnCleanup.Children {
		if !actorDependsOn(balance, vote) {
			t.Fatal("churn-balance must wait for the churn stage's CHURNINTERVAL vote")
		}
	}
}

func TestClusterChurnKeepsBondYieldCleanup(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	root := NewActor("Root", rng)
	appendSimulationStages(root, map[string]bool{"cluster-churn": true, "churn": true}, rng)
	root.InitRoot()
	counts := map[string]int{}
	root.WalkDepthFirst(func(a *Actor) bool {
		counts[a.Name]++
		return true
	})
	if counts["BondYield-Cleanup"] != 1 || counts["ChurnInterval-Restore"] != 0 {
		t.Fatalf("cluster-churn must keep its own cleanup: %v", counts)
	}
}
