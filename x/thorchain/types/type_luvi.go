package types

import (
	"fmt"
	"math/big"
	"strings"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// LUVI (Liquidity Unit Value Index) measures pool value per liquidity unit with
// price removed: sqrt(A*R) / units. Classic LUVI breaks once a pool has synths,
// because synths are a liability the LPs owe that is repriced by every swap.
//
// This file implements the synth-aware family of indices:
//
//	Q      = sqrt(A*R) - S*sqrt(p)/2 = sqrt(A*R) * (2A - S) / (2A)   (signed LP equity)
//	LUVI*  = Q/L + C,  dC = ((S_prev + S_new) / 2) * (sqrt(p_new) - sqrt(p_prev)) / (2 * L_new)
//	dG     = dQ + ((S_prev + S_new) / 4) * (sqrt(p_new) - sqrt(p_prev)) - dL * LUVI_chain_prev
//
// where p = R/A and LUVI_chain = sqrt(A*R)/PoolUnits is the classic index as
// the chain computes it (synth-unit clamp included). G is valid in every
// regime, including overhang (S >= 2A); LUVI* is only meaningful below it.
// Below overhang dG == L_new * dLUVI*; with no synths dG == L_new * dLUVI.
//
// Everything is computed with big.Int fixed-point arithmetic so results are
// deterministic. The functions here are read-only: nothing is stored on chain.

// LuviPrecision is the number of decimal places used by the internal
// fixed-point representation. Values exported as cosmos.Dec are truncated to
// that type's 18 decimal places.
const LuviPrecision = 36

var (
	luviScale   = new(big.Int).Exp(big.NewInt(10), big.NewInt(LuviPrecision), nil)
	luviScaleSq = new(big.Int).Mul(luviScale, luviScale)
	luviDecDiv  = new(big.Int).Exp(big.NewInt(10), big.NewInt(LuviPrecision-18), nil)
)

// LuviState is a snapshot of the pool quantities the LUVI indices depend on.
// PoolUnits must be the on-chain value (LPUnits + SynthUnits after CalcUnits),
// clamp included.
type LuviState struct {
	BalanceRune  cosmos.Uint
	BalanceAsset cosmos.Uint
	LPUnits      cosmos.Uint
	SynthSupply  cosmos.Uint
	PoolUnits    cosmos.Uint
}

// NewLuviState builds a snapshot from a pool and its synth supply. The pool is
// copied, so the caller's SynthUnits are left untouched.
func NewLuviState(pool Pool, synthSupply cosmos.Uint) LuviState {
	p := pool
	p.CalcUnits(synthSupply)
	return LuviState{
		BalanceRune:  pool.BalanceRune,
		BalanceAsset: pool.BalanceAsset,
		LPUnits:      pool.LPUnits,
		SynthSupply:  synthSupply,
		PoolUnits:    p.GetPoolUnits(),
	}
}

// EmptyLuviState is the state of an empty or unreadable pool: every field is
// zero (not nil), so IsValid and the other methods are safe to call.
func EmptyLuviState() LuviState {
	zero := cosmos.ZeroUint()
	return LuviState{BalanceRune: zero, BalanceAsset: zero, LPUnits: zero, SynthSupply: zero, PoolUnits: zero}
}

// IsValid reports whether every index is defined for this state: both depths
// and the LP units must be non-zero.
func (s LuviState) IsValid() bool {
	return !s.BalanceRune.IsZero() && !s.BalanceAsset.IsZero() && !s.LPUnits.IsZero() && !s.PoolUnits.IsZero()
}

// IsOverhang reports whether synths are at or above twice the asset depth,
// where on-chain synth units are clamped and LP equity is non-positive.
func (s LuviState) IsOverhang() bool {
	return s.SynthSupply.GTE(s.BalanceAsset.MulUint64(2))
}

// SynthPerPoolDepthBps returns S / (2A) in basis points (0 when A is zero).
func (s LuviState) SynthPerPoolDepthBps() cosmos.Uint {
	if s.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	return s.SynthSupply.MulUint64(10_000).Quo(s.BalanceAsset.MulUint64(2))
}

// sqrtAR returns floor(sqrt(A*R) * 10^LuviPrecision).
func (s LuviState) sqrtAR() *big.Int {
	x := new(big.Int).Mul(s.BalanceAsset.BigInt(), s.BalanceRune.BigInt())
	x.Mul(x, luviScaleSq)
	return x.Sqrt(x)
}

// sqrtPrice returns sqrt(R/A) scaled by 10^LuviPrecision (0 when A is zero).
func (s LuviState) sqrtPrice() *big.Int {
	if s.BalanceAsset.IsZero() {
		return new(big.Int)
	}
	return new(big.Int).Quo(s.sqrtAR(), s.BalanceAsset.BigInt())
}

// lpEquity returns the signed LP equity Q = sqrt(A*R)*(2A - S)/(2A), scaled.
func (s LuviState) lpEquity() *big.Int {
	if s.BalanceAsset.IsZero() {
		return new(big.Int)
	}
	twoA := new(big.Int).Lsh(s.BalanceAsset.BigInt(), 1)
	num := new(big.Int).Sub(twoA, s.SynthSupply.BigInt())
	num.Mul(num, s.sqrtAR())
	return num.Quo(num, twoA)
}

// chainLuvi returns the classic on-chain index sqrt(A*R)/PoolUnits, scaled.
func (s LuviState) chainLuvi() *big.Int {
	if s.PoolUnits.IsZero() {
		return new(big.Int)
	}
	return new(big.Int).Quo(s.sqrtAR(), s.PoolUnits.BigInt())
}

// lpEquityPerUnit returns Q/L, scaled (0 when L is zero).
func (s LuviState) lpEquityPerUnit() *big.Int {
	if s.LPUnits.IsZero() {
		return new(big.Int)
	}
	return new(big.Int).Quo(s.lpEquity(), s.LPUnits.BigInt())
}

// SqrtAR returns sqrt(A*R) as a Dec.
func (s LuviState) SqrtAR() cosmos.Dec { return luviToDec(s.sqrtAR()) }

// LPEquity returns the signed LP equity Q. It is negative in overhang.
func (s LuviState) LPEquity() cosmos.Dec { return luviToDec(s.lpEquity()) }

// LPEquityPerUnit returns Q/L. Below overhang this equals ChainLuvi (up to
// on-chain rounding of synth units); in overhang it is negative.
func (s LuviState) LPEquityPerUnit() cosmos.Dec { return luviToDec(s.lpEquityPerUnit()) }

// ChainLuvi returns the classic index sqrt(A*R)/PoolUnits as the chain values
// a pool unit, clamp included.
func (s LuviState) ChainLuvi() cosmos.Dec { return luviToDec(s.chainLuvi()) }

func luviToDec(x *big.Int) cosmos.Dec {
	return sdkmath.LegacyNewDecFromBigIntWithPrec(new(big.Int).Quo(x, luviDecDiv), 18)
}

// LuviStep describes the change between two consecutive observations.
type LuviStep struct {
	// DeltaG is the change in hedged LP-side equity, in sqrt(asset*RUNE) units.
	DeltaG cosmos.Dec
	// DeltaC is the change in the per-unit synth repricing term of LUVI*.
	DeltaC cosmos.Dec
	// NormalizedDeltaG is DeltaG / sqrt(A*R) of the previous state. Alert on this.
	NormalizedDeltaG cosmos.Dec
	// LuviStar is LUVI* after the step (only meaningful below overhang).
	LuviStar cosmos.Dec
	// Overhang reports whether either end of the step is in overhang.
	Overhang bool
}

// LuviTracker accumulates C and G across consecutive observations of a pool.
// Observe must be called after every change to the pool's depths, synth supply
// or LP units (or at least once per block, and every block in which the synth
// supply or LP units change), in order.
type LuviTracker struct {
	prev LuviState
	c    *big.Int
	g    *big.Int
}

// NewLuviTracker starts tracking from an initial state.
func NewLuviTracker(initial LuviState) (*LuviTracker, error) {
	if !initial.IsValid() {
		return nil, fmt.Errorf("luvi: invalid initial state")
	}
	return &LuviTracker{prev: initial, c: new(big.Int), g: new(big.Int)}, nil
}

// Observe records the next state and returns the step since the previous one.
func (t *LuviTracker) Observe(next LuviState) (LuviStep, error) {
	if !next.IsValid() {
		return LuviStep{}, fmt.Errorf("luvi: invalid state")
	}
	prev := t.prev
	sumS := new(big.Int).Add(prev.SynthSupply.BigInt(), next.SynthSupply.BigInt())
	dSqrtP := new(big.Int).Sub(next.sqrtPrice(), prev.sqrtPrice())
	reprice := new(big.Int).Mul(sumS, dSqrtP) // (S_prev + S_new) * dsqrt(p), scaled

	// dC = reprice / (4 * L_new)
	dC := new(big.Int).Quo(reprice, new(big.Int).Lsh(next.LPUnits.BigInt(), 2))

	// dG = dQ + reprice/4 - dL * LUVI_chain(prev)
	dG := new(big.Int).Sub(next.lpEquity(), prev.lpEquity())
	dG.Add(dG, new(big.Int).Quo(reprice, big.NewInt(4)))
	// dL * sqrt(AR)_prev / P_prev, multiplied before dividing: in overhang the
	// pool units can reach ~1e38, which would truncate LUVI_chain to few digits.
	dLv := new(big.Int).Sub(next.LPUnits.BigInt(), prev.LPUnits.BigInt())
	dLv.Mul(dLv, prev.sqrtAR())
	dG.Sub(dG, dLv.Quo(dLv, prev.PoolUnits.BigInt()))

	t.c.Add(t.c, dC)
	t.g.Add(t.g, dG)
	t.prev = next

	norm := new(big.Int).Mul(dG, luviScale)
	norm.Quo(norm, prev.sqrtAR())

	star := new(big.Int).Add(next.lpEquityPerUnit(), t.c)
	return LuviStep{
		DeltaG:           luviToDec(dG),
		DeltaC:           luviToDec(dC),
		NormalizedDeltaG: luviToDec(norm),
		LuviStar:         luviToDec(star),
		Overhang:         prev.IsOverhang() || next.IsOverhang(),
	}, nil
}

// C returns the accumulated synth repricing term of LUVI*.
func (t *LuviTracker) C() cosmos.Dec { return luviToDec(t.c) }

// G returns the accumulated raw hedged equity change. Its unit is worth a
// different amount of RUNE as price and pool size change, so alert on each
// step's NormalizedDeltaG rather than reading G as a return.
func (t *LuviTracker) G() cosmos.Dec { return luviToDec(t.g) }

// LuviStar returns LUVI* = Q/L + C for the most recent state.
func (t *LuviTracker) LuviStar() cosmos.Dec {
	return luviToDec(new(big.Int).Add(t.prev.lpEquityPerUnit(), t.c))
}

// CheckLPUnitLedger verifies that the change in LP units between two
// observations equals the units reported by that block's add-liquidity and
// withdraw events. G cannot see units over-minted while a pool is in overhang
// (they are nearly worthless until it leaves overhang), so run this alongside.
func CheckLPUnitLedger(prevUnits, nextUnits cosmos.Uint, added, withdrawn []cosmos.Uint) error {
	expected := new(big.Int).Set(prevUnits.BigInt())
	for _, u := range added {
		expected.Add(expected, u.BigInt())
	}
	for _, u := range withdrawn {
		expected.Sub(expected, u.BigInt())
	}
	if expected.Cmp(nextUnits.BigInt()) != 0 {
		return fmt.Errorf("luvi: LP units %s do not match events (expected %s)", nextUnits, expected)
	}
	return nil
}

// RestoreLuviTracker resumes tracking from a stored snapshot: the last observed
// state and the raw accumulators as returned by RawC and RawG.
func RestoreLuviTracker(prev LuviState, c, g *big.Int) (*LuviTracker, error) {
	if !prev.IsValid() {
		return nil, fmt.Errorf("luvi: invalid stored state")
	}
	return &LuviTracker{prev: prev, c: new(big.Int).Set(c), g: new(big.Int).Set(g)}, nil
}

// Prev returns the most recent observed state.
func (t *LuviTracker) Prev() LuviState { return t.prev }

// RawC returns the accumulated C at full LuviPrecision, for storage.
func (t *LuviTracker) RawC() *big.Int { return new(big.Int).Set(t.c) }

// RawG returns the accumulated G at full LuviPrecision, for storage.
func (t *LuviTracker) RawG() *big.Int { return new(big.Int).Set(t.g) }

// LuviDefaultTolerance is the largest normalized drop in G, beyond the computed
// allowance, that is treated as noise. Integer rounding in swaps and synth
// units moves G by well under 1e-15.
var LuviDefaultTolerance = cosmos.NewDecWithPrec(1, 12)

// LuviAllowance returns the normalized drop in G a step can show without any
// value leaving LPs unexpectedly, beyond the tolerance:
//
//   - Gas and vault slashes: the pool pays g asset and is reimbursed in RUNE
//     at spot price, which costs 1 - sqrt(1 - (g/A)^2) ~ (g/A)^2/2 of
//     sqrt(A*R). The allowance is (g/A)^2, about twice that, as the spot
//     price moves within the step. It ignores the RUNE the event reports, so
//     an under-reimbursement (such as a slash the bond can't cover) is a drop.
//   - LP flow rounding: each add or withdraw rounds its share of each side to
//     the nearest base unit (common.GetSafeShare), in either direction, which
//     is worth up to 1/A + 1/R of sqrt(A*R) per flow. In a small pool one
//     base unit of the asset can be worth thousands of RUNE base units.
//   - LP flow timing: G values the step's LP unit change dL at the previous
//     unit value v_prev, but a flow that runs after the step's other changes
//     happens at a value between v_prev and v_new. The error is at most
//     |dL| * |v_new - v_prev|, a fraction dL/L of the step's value change, so
//     it can only hide a proportional share of a real loss.
//   - Synth mints and burns: G integrates the synth repricing with the
//     trapezoid rule over the step's end points, which is exact only when the
//     synth supply moves linearly in sqrt(p). A mint at the swap formula's
//     price is not, and reads as a drop of about y^3/30 for y = |dS|/A (cubic,
//     independent of the existing supply, rising to y^3/4 at y = 0.12). The
//     allowance is y^3 * (1/16 + 2y), about twice what was measured. Hiding a
//     1e-4 loss behind it needs a mint of about 6% of the asset depth.
func LuviAllowance(prev, next LuviState, assetPaid cosmos.Uint, flows int) cosmos.Dec {
	total := cosmos.ZeroDec()
	if prev.BalanceAsset.IsZero() || prev.BalanceRune.IsZero() {
		return total
	}
	if !assetPaid.IsZero() {
		e := luviUintToDec(assetPaid).Quo(luviUintToDec(prev.BalanceAsset))
		if e.GT(cosmos.NewDec(1)) {
			e = cosmos.NewDec(1)
		}
		// 1 - sqrt(1 - e^2) <= e^2, and ~e^2/2 for small e
		total = total.Add(e.Mul(e))
	}
	perFlow := cosmos.NewDec(1).Quo(luviUintToDec(prev.BalanceAsset)).Add(cosmos.NewDec(1).Quo(luviUintToDec(prev.BalanceRune)))
	total = total.Add(perFlow.MulInt64(int64(flows)))

	if !prev.SynthSupply.Equal(next.SynthSupply) {
		minA := prev.BalanceAsset
		if !next.BalanceAsset.IsZero() && next.BalanceAsset.LT(minA) {
			minA = next.BalanceAsset
		}
		dS := luviUintToDec(prev.SynthSupply).Sub(luviUintToDec(next.SynthSupply)).Abs()
		y := dS.Quo(luviUintToDec(minA))
		if y.GT(cosmos.NewDec(1)) {
			y = cosmos.NewDec(1)
		}
		coef := cosmos.NewDecWithPrec(625, 4).Add(y.MulInt64(2))
		total = total.Add(y.Mul(y).Mul(y).Mul(coef))
	}

	sqrtAR := prev.SqrtAR()
	if !sqrtAR.IsPositive() {
		return total
	}
	dL := luviUintToDec(next.LPUnits).Sub(luviUintToDec(prev.LPUnits)).Abs()
	dV := next.ChainLuvi().Sub(prev.ChainLuvi()).Abs()
	return total.Add(dL.Mul(dV).Quo(sqrtAR))
}

func luviUintToDec(u cosmos.Uint) cosmos.Dec {
	return cosmos.NewDecFromBigInt(u.BigInt())
}

// LuviStepEvents summarizes, per pool, the events of one step that the LUVI
// checks need: asset paid out by gas and vault slashes, LP units added and
// withdrawn, and whether an errata touched the pool.
type LuviStepEvents struct {
	AssetPaid map[string]cosmos.Uint
	Added     map[string][]cosmos.Uint
	Withdrawn map[string][]cosmos.Uint
	Errata    map[string]bool
	Swaps     map[string]int
	// Reported and ReportedExcess name the pools an inner scope already
	// reported a luvi_loss for (excess: beyond its allowance).
	Reported       map[string]bool
	ReportedExcess map[string]bool
}

// NewLuviStepEvents parses flattened events (type under "type", attributes by
// key; the last value wins for repeated keys).
func NewLuviStepEvents(events []map[string]string) LuviStepEvents {
	out := LuviStepEvents{
		AssetPaid: map[string]cosmos.Uint{},
		Added:     map[string][]cosmos.Uint{},
		Withdrawn: map[string][]cosmos.Uint{},
		Errata:    map[string]bool{},
		Swaps:     map[string]int{},

		Reported:       map[string]bool{},
		ReportedExcess: map[string]bool{},
	}
	for _, e := range events {
		switch e["type"] {
		case GasEventType, SlashEventType:
			var pool, amt string
			if e["type"] == GasEventType {
				pool, amt = e["asset"], e["asset_amt"]
			} else {
				// a slash event lists each pool's change under its asset,
				// negative when the pool pays
				pool = e["pool"]
				var ok bool
				if amt, ok = strings.CutPrefix(e[pool], "-"); !ok {
					continue
				}
			}
			g, err := cosmos.ParseUint(amt)
			if err != nil {
				continue
			}
			if prev, ok := out.AssetPaid[pool]; ok {
				g = g.Add(prev)
			}
			out.AssetPaid[pool] = g
		case AddLiquidityEventType, WithdrawEventType:
			u, err := cosmos.ParseUint(e["liquidity_provider_units"])
			if err != nil {
				continue
			}
			if e["type"] == AddLiquidityEventType {
				out.Added[e["pool"]] = append(out.Added[e["pool"]], u)
			} else {
				out.Withdrawn[e["pool"]] = append(out.Withdrawn[e["pool"]], u)
			}
		case ErrataEventType:
			out.Errata[e["asset"]] = true
		case SwapEventType:
			out.Swaps[e["pool"]]++
		case LuviLossEventType:
			out.Reported[e["pool"]] = true
			if e["excess"] == "true" {
				out.ReportedExcess[e["pool"]] = true
			}
		}
	}
	return out
}

// AssetPaidOf returns the asset paid by gas and slashes for a pool.
func (e LuviStepEvents) AssetPaidOf(pool string) cosmos.Uint {
	if g, ok := e.AssetPaid[pool]; ok {
		return g
	}
	return cosmos.ZeroUint()
}

// Flows returns the number of LP flows (adds and withdraws) for a pool.
func (e LuviStepEvents) Flows(pool string) int {
	return len(e.Added[pool]) + len(e.Withdrawn[pool])
}

// RoundingOps returns the number of operations in the step that round a
// pool's balances to whole base units: LP flows, swap legs, and one for the
// step's other writes. Each can move G by up to 1/A + 1/R of sqrt(A*R).
func (e LuviStepEvents) RoundingOps(pool string) int {
	return e.Flows(pool) + e.Swaps[pool] + 1
}

// FlattenLuviEvents converts SDK events to the flattened form
// NewLuviStepEvents takes.
func FlattenLuviEvents(events cosmos.Events) []map[string]string {
	out := make([]map[string]string, 0, len(events))
	for _, ev := range events {
		m := make(map[string]string, len(ev.Attributes)+1)
		m["type"] = ev.Type
		for _, a := range ev.Attributes {
			m[a.Key] = a.Value
		}
		out = append(out, m)
	}
	return out
}

// LuviRewardMeasurement is the LUVI view of one pool's block reward: r RUNE
// added to the pool's depth with its asset depth, synth supply and units
// unchanged.
type LuviRewardMeasurement struct {
	// DeltaG is the measured change in G from the pre- to the post-reward state.
	DeltaG cosmos.Dec
	// ExpectedDeltaG is the change in G a reward of r RUNE should cause.
	ExpectedDeltaG cosmos.Dec
	// LPShareBps is the share of the reward the LPs kept, in basis points: the
	// change in LP equity Q valued in RUNE, over r. It is signed (negative in
	// overhang, where LP equity is).
	LPShareBps int64
	// ExpectedLPShareBps is 10000 * (2A - S) / (2A), the share the LPs keep.
	ExpectedLPShareBps int64
	// OK reports whether DeltaG is positive and matches ExpectedDeltaG within
	// rounding.
	OK bool
}

// MeasureLuviReward measures a reward of r RUNE paid to a pool, given the
// pool's state just before and just after the payment.
//
// Adding r RUNE to a pool with depths A, R and synth supply S (A, S and the
// LP units L unchanged, so dL = 0):
//
//	dQ = (sqrt(A(R+r)) - sqrt(AR)) * (2A - S) / (2A)
//	reprice term = (2S/4) * (sqrt((R+r)/A) - sqrt(R/A))
//	             = (sqrt(A(R+r)) - sqrt(AR)) * S / (2A)
//	expected dG  = dQ + reprice term = sqrt(A(R+r)) - sqrt(AR)
//
// G is hedged against the synth repricing, so the expected dG does not
// depend on S. Valued in RUNE at the trapezoid of the two prices,
// dG * (sqrt(p_pre) + sqrt(p_post)) = sqrt(A)(sqrt(R+r) - sqrt(R)) *
// (sqrt(R) + sqrt(R+r)) / sqrt(A) = r exactly: the whole reward lands in the
// pool. The LPs' own equity rises only by dQ, worth r * (2A - S) / (2A) the
// same way; the rest, r * S / (2A), accrues to synth holders as the higher
// price raises the value of the synths the pool owes. That is the LP share:
//
//	LP share = dQ * (sqrt(p_pre) + sqrt(p_post)) / r = 1 - S / (2A)
//
// so a synth-free pool keeps 10000 bps and one at S = A keeps 5000.
//
// A dG that differs from the expected value by more than the integer
// rounding of the fixed-point arithmetic (at most about S + 8 raw units at
// LuviPrecision, plus a term for the (2A - S)/(2A) factor in deep overhang)
// means the reward did not land in the pool's depth as accounted: it went
// elsewhere, another pool got it, or a later write clobbered it.
func MeasureLuviReward(pre, post LuviState, reward cosmos.Uint) (LuviRewardMeasurement, error) {
	if !pre.IsValid() {
		return LuviRewardMeasurement{}, fmt.Errorf("luvi: invalid pre-reward state")
	}
	m := LuviRewardMeasurement{
		DeltaG:             cosmos.ZeroDec(),
		ExpectedDeltaG:     cosmos.ZeroDec(),
		ExpectedLPShareBps: luviExpectedLPShareBps(pre),
	}

	expState := pre
	expState.BalanceRune = pre.BalanceRune.Add(reward)
	expected := new(big.Int).Sub(expState.sqrtAR(), pre.sqrtAR())
	m.ExpectedDeltaG = luviToDec(expected)

	if !post.IsValid() {
		// the reward's pool lost a depth or its units: nothing landed
		return m, nil
	}
	tracker, err := NewLuviTracker(pre)
	if err != nil {
		return m, err
	}
	if _, err = tracker.Observe(post); err != nil {
		return m, err
	}
	dG := tracker.RawG()
	m.DeltaG = luviToDec(dG)

	if !reward.IsZero() {
		// share = dQ * (sqrtP_pre + sqrtP_post) / r; both factors carry the scale
		dQ := new(big.Int).Sub(post.lpEquity(), pre.lpEquity())
		v := new(big.Int).Add(pre.sqrtPrice(), post.sqrtPrice())
		v.Mul(v, dQ)
		v.Mul(v, big.NewInt(int64(constants.MaxBasisPts)))
		// round to the nearest basis point (Quo truncates toward zero)
		d := new(big.Int).Mul(reward.BigInt(), luviScaleSq)
		half := new(big.Int).Rsh(d, 1)
		if v.Sign() < 0 {
			half.Neg(half)
		}
		v.Add(v, half).Quo(v, d)
		if v.IsInt64() {
			m.LPShareBps = v.Int64()
		}
	}

	diff := new(big.Int).Sub(dG, expected)
	diff.Abs(diff)
	m.OK = dG.Sign() > 0 && diff.Cmp(luviRewardTolerance(pre)) <= 0
	return m, nil
}

// luviExpectedLPShareBps returns 10000 * (2A - S) / (2A), signed.
func luviExpectedLPShareBps(s LuviState) int64 {
	if s.BalanceAsset.IsZero() {
		return 0
	}
	twoA := new(big.Int).Lsh(s.BalanceAsset.BigInt(), 1)
	num := new(big.Int).Sub(twoA, s.SynthSupply.BigInt())
	num.Mul(num, big.NewInt(int64(constants.MaxBasisPts)))
	num.Quo(num, twoA)
	if !num.IsInt64() {
		return 0
	}
	return num.Int64()
}

// luviRewardTolerance bounds the rounding of a reward step's dG, in raw units
// at LuviPrecision: each Q floors its sqrt and its division (up to |2A-S|/(2A)
// + 1 raw units each), each sqrt(p) floors twice (2 raw units, times 2S in
// the repricing term, which is then divided by 4), and the expected value
// floors two square roots. Doubling that gives 2S + 4|2A-S|/(2A) + 16.
func luviRewardTolerance(s LuviState) *big.Int {
	tol := new(big.Int).Lsh(s.SynthSupply.BigInt(), 1)
	twoA := new(big.Int).Lsh(s.BalanceAsset.BigInt(), 1)
	f := new(big.Int).Sub(twoA, s.SynthSupply.BigInt())
	f.Abs(f)
	f.Lsh(f, 2)
	f.Quo(f, twoA)
	tol.Add(tol, f)
	return tol.Add(tol, big.NewInt(16))
}
