package suites

import (
	rand "math/rand/v2"
	"os"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func XMRCoverage(rng *rand.Rand) *Actor {
	a := NewActor("XMRCoverage", rng)
	if !acommon.XMRChainEnabled() {
		return a
	}

	a.Append(core.NewVerifyLPActor(common.XMRAsset, rng))
	a.Append(core.NewSwapActor(common.XMRAsset, common.BTCAsset, rng))
	a.Append(core.NewSwapActor(common.BTCAsset, common.XMRAsset, rng))
	a.Append(core.NewSwapMemolessActor(common.XMRAsset, common.ETHAsset, core.MemolessTypeRef, rng))
	a.Append(core.NewWithdrawLPActor(common.XMRAsset, rng))

	return a
}

func XMRPostChurn(rng *rand.Rand) *Actor {
	a := NewActor("XMRPostChurn", rng)
	// The shared develop baseline still skips XMR keygen. Required runs (S00
	// and the dedicated readiness target) must create the first usable vault
	// even when it is absent from the pre-churn inbound list.
	if !XMRPostChurnEnabled() {
		return a
	}
	a.Append(core.NewXMRPostChurnActor(rng))
	return a
}

// XMRPostChurnEnabled preserves optional develop coverage while allowing an
// explicitly required run to wait for its first XMR vault.
func XMRPostChurnEnabled() bool {
	return acommon.XMRChainEnabled() || os.Getenv("XMR_POST_CHURN_REQUIRED") == "1"
}

func XMRKeygenRecovery(rng *rand.Rand) *Actor {
	a := NewActor("XMRKeygenRecovery", rng)
	a.Append(core.NewXMRKeygenRecoveryActor(rng))
	return a
}

func XMRSpentRefTracking(rng *rand.Rand) *Actor {
	a := NewActor("XMRSpentRefTracking", rng)
	if !acommon.XMRChainEnabled() {
		return a
	}
	a.Append(core.NewXMRSpentRefVerifyActor(rng))
	return a
}
