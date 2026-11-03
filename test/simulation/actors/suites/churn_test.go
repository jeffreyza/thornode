package suites

import (
	rand "math/rand/v2"
	"testing"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestStandaloneChurnStagesRestoreChurnIntervalAfterVerification(t *testing.T) {
	for _, tc := range []struct {
		suite  func(*rand.Rand) *Actor
		name   string
		verify string
	}{
		{suite: Churn, name: "ChurnSuite", verify: "Churn"},
		{suite: ChurnBalance, name: "ChurnBalanceSuite", verify: "ChurnBalance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.suite(rand.New(rand.NewPCG(7, 8)))
			if a.Name != tc.name {
				t.Fatalf("unexpected suite name: %s", a.Name)
			}
			a.InitRoot()

			var restores []*Actor
			a.WalkDepthFirst(func(actor *Actor) bool {
				switch actor.Name {
				case "ChurnInterval-Restore":
					restores = append(restores, actor)
				case "BondYield-Cleanup":
					t.Fatalf("%s must not reuse the Bond Yield cleanup", tc.name)
				}
				return true
			})
			if len(restores) != 1 {
				t.Fatalf("expected one CHURNINTERVAL restore in %s, got %d", tc.name, len(restores))
			}
			restore := restores[0]

			parents := map[string]bool{}
			for parent := range restore.Parents() {
				parents[parent.Name] = true
			}
			if !parents[tc.verify] || len(parents) != 1 {
				t.Fatalf("expected the CHURNINTERVAL restore to run only after %s, got parents %v", tc.verify, parents)
			}

			var mimirs []string
			for child := range restore.Children {
				mimirs = append(mimirs, child.Name)
			}
			if len(mimirs) != 1 || mimirs[0] != "Mimir(all) ChurnInterval=60" {
				t.Fatalf("expected the restore to vote only the mocknet CHURNINTERVAL, got %v", mimirs)
			}
		})
	}
}
