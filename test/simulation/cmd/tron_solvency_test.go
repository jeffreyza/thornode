package main

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestTRONSolvencyStageSelectionIncludesActivation(t *testing.T) {
	const stages = "seed,bootstrap,churn,tron-solvency-fragmentation"

	// Execution order comes from the registry, not the order of STAGES.
	wanted := strings.Split(stages, ",")
	enabled := make(map[string]bool, len(wanted))
	for _, name := range wanted {
		enabled[name] = true
	}
	var actual []string
	for _, stage := range simulationStages() {
		if enabled[stage.name] {
			if stage.name != "seed" && stage.constructor == nil {
				t.Fatalf("selected stage %s has no actor", stage.name)
			}
			actual = append(actual, stage.name)
		}
	}
	if !reflect.DeepEqual(actual, wanted) {
		t.Fatalf("TRON stage order: got %v, want %v", actual, wanted)
	}
}

func TestTRONSolvencyStageDependsOnChurnCompletion(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	root := NewActor("test", rng)
	// Avoid bootstrap's live RPCs: exercise the actual DAG wiring of the two
	// selected actors whose dependency prevents the missing-activation failure.
	appendSimulationStages(root, map[string]bool{
		"churn": true, "tron-solvency-fragmentation": true,
	}, rng)
	root.InitRoot()
	actors := map[string]*Actor{}
	root.WalkDepthFirst(func(actor *Actor) bool {
		actors[actor.Name] = actor
		return true
	})
	churn := actors["Churn"]
	restore := actors["ChurnInterval-Restore"]
	restoredMimir := actors["Mimir(all) ChurnInterval=60"]
	tron := actors["TRONSolvencyFragmentation"]
	if churn == nil || restore == nil || restoredMimir == nil || tron == nil {
		t.Fatal("expected churn, Mimir restoration, and TRON actors")
	}
	if len(restore.Parents()) != 1 || !restore.Parents()[churn] {
		t.Fatal("Mimir restoration must wait for churn")
	}
	if len(restoredMimir.Parents()) != 1 || !restoredMimir.Parents()[restore] {
		t.Fatal("Mimir vote must wait for the restoration stage")
	}
	if len(tron.Parents()) != 1 || !tron.Parents()[restoredMimir] {
		t.Fatal("TRON must wait for churn completion and Mimir restoration")
	}
}

func TestTRONSolvencyRunsBeforeClusterChurnLeave(t *testing.T) {
	for _, selection := range []struct {
		name    string
		enabled map[string]bool
	}{
		{name: "cluster stage", enabled: map[string]bool{"cluster-churn": true}},
		{name: "legacy alias", enabled: map[string]bool{"bond-yield": true}},
		{name: "both names", enabled: map[string]bool{"cluster-churn": true, "bond-yield": true}},
		{name: "explicit activation", enabled: map[string]bool{"cluster-churn": true, "churn": true}},
		{name: "XMR post-churn", enabled: map[string]bool{"cluster-churn": true, "xmr-post-churn": true}},
	} {
		t.Run(selection.name, func(t *testing.T) {
			if selection.enabled["xmr-post-churn"] {
				t.Setenv("XMR_POST_CHURN_REQUIRED", "1")
			}
			rng := rand.New(rand.NewPCG(3, 4))
			root := NewActor("test", rng)
			selection.enabled["tron-solvency-fragmentation"] = true
			appendSimulationStages(root, selection.enabled, rng)
			root.InitRoot()

			actors := map[string]*Actor{}
			root.WalkDepthFirst(func(actor *Actor) bool {
				if actors[actor.Name] != nil {
					t.Fatalf("actor %s was scheduled more than once", actor.Name)
				}
				actors[actor.Name] = actor
				return true
			})
			for _, edge := range [][2]string{
				{"BondYield-Setup", "Churn"},
				{"Churn", "TRONSolvencyFragmentation"},
				{"TRONSolvencyFragmentation", "BondYield-SelectTarget"},
				{"BondYield-SelectTarget", "BondYield-TargetAutoClaim"},
				{"BondYield-ChurnOut", "BondYield-Cleanup"},
			} {
				parent, child := actors[edge[0]], actors[edge[1]]
				if parent == nil || child == nil {
					t.Fatalf("missing actors for dependency %s -> %s", edge[0], edge[1])
				}
				if len(child.Parents()) != 1 || !child.Parents()[parent] {
					t.Fatalf("%s must wait for %s", child.Name, parent.Name)
				}
			}
			autoClaim := actors["BondYield-TargetAutoClaim"]
			leave := actors["BondYield-ChurnOut"]
			if selection.enabled["xmr-post-churn"] {
				capture := actors["XMRPreChurnOut"]
				if capture == nil || !capture.Parents()[autoClaim] || !leave.Parents()[capture] ||
					actors["XMRPostChurn BTC.BTC => XMR.XMR"] == nil {
					t.Fatal("XMR must still capture the vault before leave and verify its replacement")
				}
			} else if !leave.Parents()[autoClaim] {
				t.Fatal("validator leave must wait for auto-claim setup")
			}
			if actors["BondYield-ChurnIn"] != nil {
				t.Fatal("TRON should not require a validator restoration churn")
			}
		})
	}
}

func TestClusterChurnDoesNotEnableUnselectedTRONStage(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	root := NewActor("test", rng)
	appendSimulationStages(root, map[string]bool{
		"cluster-churn": true, "tron-solvency-fragmentation": false,
	}, rng)
	root.WalkDepthFirst(func(actor *Actor) bool {
		if actor.Name == "TRONSolvencyFragmentation" {
			t.Fatal("Cluster churn scheduled the unselected TRON stage")
		}
		return true
	})
}

func TestTRONSolvencyStandaloneDoesNotAddChurn(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	root := NewActor("test", rng)
	appendSimulationStages(root, map[string]bool{"tron-solvency-fragmentation": true}, rng)
	var names []string
	root.WalkDepthFirst(func(actor *Actor) bool {
		names = append(names, actor.Name)
		return true
	})
	if !reflect.DeepEqual(names, []string{"test", "TRONSolvencyFragmentation"}) {
		t.Fatalf("standalone TRON on an active cluster must not add setup or churn: %v", names)
	}
}
