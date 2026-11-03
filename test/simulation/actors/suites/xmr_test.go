package suites

import (
	rand "math/rand/v2"
	"testing"

	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestXMRCoverageIncludesExpectedActors(t *testing.T) {
	names := actorNames(XMRCoverage(rand.New(rand.NewPCG(1, 2))))

	for _, want := range []string{
		"VerifyLP-XMR.XMR",
		"Swap XMR.XMR => BTC.BTC",
		"Swap BTC.BTC => XMR.XMR",
		"SwapMemoless XMR.XMR => ETH.ETH",
		"WithdrawLP-XMR.XMR",
	} {
		if !names[want] {
			t.Fatalf("expected actor %q in XMR coverage suite", want)
		}
	}
}

func TestXMRPostChurnRespectsCoverageAndRequiredMode(t *testing.T) {
	previous := acommon.XMRChainEnabled()
	defer acommon.SetXMRChainEnabled(previous)

	for _, tc := range []struct {
		name     string
		enabled  bool
		required string
		want     bool
	}{
		{name: "no inbound skips optional coverage", required: "0"},
		{name: "unset requirement skips optional coverage"},
		{name: "inbound enables coverage", enabled: true, want: true},
		{name: "required creates first vault", required: "1", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acommon.SetXMRChainEnabled(tc.enabled)
			t.Setenv("XMR_POST_CHURN_REQUIRED", tc.required)
			names := actorNames(XMRPostChurn(rand.New(rand.NewPCG(3, 4))))
			if got := names["XMRPostChurn BTC.BTC => XMR.XMR"]; got != tc.want {
				t.Fatalf("post-churn actor present = %v, want %v", got, tc.want)
			}
		})
	}
}

func actorNames(root interface{ WalkDepthFirst(func(*Actor) bool) bool }) map[string]bool {
	names := map[string]bool{}
	root.WalkDepthFirst(func(a *Actor) bool {
		names[a.Name] = true
		return true
	})
	return names
}
