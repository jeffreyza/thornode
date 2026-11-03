package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Pure arithmetic cores of the ADR-028 (Migrate15to16) strict-waterfall math.
// They live in this build-tag-agnostic file — the migration BODY is //go:build
// mainnet — so their regime switches and rounding edges are unit-testable under
// the default (mocknet) CI build, where the migration itself is a no-op stub.
// The tagged migration fetches the relevant state and delegates the arithmetic
// here; see TestMigrate15to16Math in migration_math_15to16_test.go.

// adr028OverflowBps is the pure core of the step-3 savers-overflow solve: given the
// post-step-1/2 pool LP units (postL) and asset depth (postA), the current synth
// supply, the DLP-neutral target total pool units (targetT), and the savers pool's
// L1 depth, return the savers-haircut bps that lands total pool units at targetT via
// the SAME clamp-aware CalcUnits the chain uses. Returns 0 when there is no overflow
// lever (no savers depth, zero LP) or the target is already met; clamps at 10000
// (never burn more synth than the savers pool holds). adr028SolveOverflowBps fetches
// saverDepth and delegates here.
func adr028OverflowBps(postL, postA, synthSupply, targetT, saverDepth cosmos.Uint) uint64 {
	if postL.IsZero() || saverDepth.IsZero() {
		return 0
	}
	twoPostA := postA.MulUint64(2)
	suTarget := common.SafeSub(targetT, postL)
	clampedS := suTarget.Quo(postL)
	// postL > 0 here (early return above) ⇒ postL+suTarget > 0, so the Quo is safe.
	var sTarget cosmos.Uint
	if !clampedS.IsZero() && clampedS.GTE(twoPostA) {
		sTarget = clampedS // overhang/clamped regime — SU = postL·S, linear
	} else {
		sTarget = twoPostA.Mul(suTarget).Quo(postL.Add(suTarget)) // non-clamped
	}
	if synthSupply.LTE(sTarget) {
		return 0 // layer-1 already met the target; no savers burn needed
	}
	b := common.SafeSub(synthSupply, sTarget)
	bps := b.MulUint64(10000).Quo(saverDepth)
	if bps.GT(cosmos.NewUint(10000)) {
		bps = cosmos.NewUint(10000)
	}
	return bps.BigInt().Uint64()
}

// adr028SplitFreed splits RUNE freed by a pool-depth cut into the burned half and the
// half retained in the Reserve. The odd 1e-8 unit goes to the burn, so toBurn ≥
// toReserve and toBurn+toReserve == freed exactly. Pure core of splitFreedRune (which
// performs the module sends/burn).
func adr028SplitFreed(freed cosmos.Uint) (toBurn, toReserve cosmos.Uint) {
	toReserve = freed.QuoUint64(2)
	toBurn = common.SafeSub(freed, toReserve)
	return toBurn, toReserve
}

// adr028PairedCut is the pure closed-form depth cut paired with a reserve synth burn
// so the reserve absorbs its share position-neutrally (no other holder's value-per-
// unit moves). Given pool RUNE (R), asset (A), LP units (L), the pre-burn total pool
// units (T = L + CalcUnits(sPre)), the synth supply before the burn (sPre) and the
// burned synth amount (burnAmt = X), return the RUNE/asset depth to cut:
//
//	overhang     (sPre ≥ 2A): ΔR = R·X/sPre,  ΔA = A·X/sPre              (CalcUnits clamp → SU = L·S)
//	non-overhang (sPre < 2A):  drop = T·(2A−(sPre−X)) − 2A·L ; ΔR = R·drop/(2AT), ΔA = A·drop/(2AT)
//
// Returns (0,0) on degenerate input. The caller computes T via the real pool's
// CalcUnits (itself already CI-testable in common); this isolates the regime branch.
//
//nolint:gocritic // captLocal: param names mirror math notation in the doc comment above
func adr028PairedCut(R, A, L, T, sPre, burnAmt cosmos.Uint) (dRpair, dApair cosmos.Uint) {
	if R.IsZero() || A.IsZero() || sPre.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint()
	}
	twoA := A.MulUint64(2)
	if sPre.GTE(twoA) {
		return common.GetUncappedShare(burnAmt, sPre, R), common.GetUncappedShare(burnAmt, sPre, A)
	}
	twoAT := twoA.Mul(T)
	if twoAT.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint()
	}
	sMinusX := common.SafeSub(sPre, burnAmt)
	dropNum := common.SafeSub(T.Mul(common.SafeSub(twoA, sMinusX)), twoA.Mul(L))
	return common.GetUncappedShare(dropNum, twoAT, R), common.GetUncappedShare(dropNum, twoAT, A)
}
