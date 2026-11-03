package features

import "testing"

// Job 17244699867 saw the target Standby only while its vault migrated; the churn
// at lastChurn+ChurnInterval then made it Ready and Active again before the actor
// could see Standby with no migration running, so it waited for Standby until its
// 35-minute timeout.
func TestBondYieldTargetChurnedOutSurvivesReadmission(t *testing.T) {
	const leaveStatusSince = 373
	for _, tc := range []struct {
		name string
		node bondYieldNode
		want bool
	}{
		{"active since before the leave", bondYieldNode{Status: "Active", StatusSince: leaveStatusSince}, false},
		{"churned out", bondYieldNode{Status: "Standby", StatusSince: 858}, true},
		{"ready for the next churn", bondYieldNode{Status: "Ready", StatusSince: 913}, true},
		{"re-admitted by the next churn", bondYieldNode{Status: "Active", StatusSince: 923}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bondYieldTargetChurnedOut(tc.node, leaveStatusSince); got != tc.want {
				t.Fatalf("bondYieldTargetChurnedOut(%+v) = %v, want %v", tc.node, got, tc.want)
			}
		})
	}
}
