package types

import (
	"math/big"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// FuzzLuviTracker checks the pure math of type_luvi.go on random sequences of
// pool states, in every regime including deep overhang with ~1e38 units.
// It works on the tracker's internal fixed-point values (10^-36 resolution),
// so every tolerance below is a count of fixed-point rounding steps. The
// invariants are stated in docs/concepts/luvi-invariant.md:
//
//   - Observe never panics and is deterministic;
//   - an unchanged state is an exact zero step;
//   - going to a state and back sums to dL * (v_next - v_prev) (exactly zero
//     with constant LP units), so mint-then-burn at one price returns G;
//   - with S and L constant, dG == d sqrt(A*R) in every regime;
//   - a synth supply change at constant depths is dG == dQ exactly;
//   - below overhang dG == L_new * dLUVI*, and with S = 0, dG == L_new * dLUVI.
//
// Run: go test -run='^$' -fuzz=FuzzLuviTracker ./x/thorchain/types/
func FuzzLuviTracker(f *testing.F) {
	for _, s := range luviTrackerSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		states := decodeLuviStates(data)
		if len(states) < 2 {
			return
		}
		g1, c1 := checkLuviSequence(t, states)
		// determinism: a second run over the same states agrees exactly
		g2, c2 := checkLuviSequence(t, states)
		if g1.Cmp(g2) != 0 || c1.Cmp(c2) != 0 {
			t.Fatalf("non-deterministic: G %s vs %s, C %s vs %s", g1, g2, c1, c2)
		}
	})
}

const (
	luviFuzzStepLen  = 9
	luviFuzzMaxSteps = 48
)

var luviFuzzMaxValue = new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)

// luviFuzzValue decodes a value in [1, 1e38]: a 16-bit mantissa times a power
// of ten.
func luviFuzzValue(hi, lo, exp byte) *big.Int {
	m := big.NewInt(int64(hi)<<8 | int64(lo) + 1)
	v := m.Mul(m, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp%35)), nil))
	if v.Cmp(luviFuzzMaxValue) > 0 {
		v.Set(luviFuzzMaxValue)
	}
	return v
}

// luviFuzzState builds a state the way the chain does, pool units included.
func luviFuzzState(runeDepth, assetDepth, lpUnits, synths *big.Int) LuviState {
	p := NewPool()
	p.BalanceRune = cosmos.NewUintFromBigInt(runeDepth)
	p.BalanceAsset = cosmos.NewUintFromBigInt(assetDepth)
	p.LPUnits = cosmos.NewUintFromBigInt(lpUnits)
	return NewLuviState(p, cosmos.NewUintFromBigInt(synths))
}

// decodeLuviStates turns the input into a sequence of valid states. The first
// 12 bytes are the starting state; each following 9-byte step is an opcode
// and parameters that derive the next state from the current one:
//
//	0: a fresh arbitrary state
//	1: a move along the hyperbola A*R = k (a fee-free swap), S and L fixed
//	2: a new synth supply at fixed depths and units (mint or burn for free)
//	3: new LP units only
//	4: everything scaled by n/d (a proportional LP flow, synths included)
func decodeLuviStates(data []byte) []LuviState {
	if len(data) < 12 {
		return nil
	}
	cur := [4]*big.Int{
		luviFuzzValue(data[0], data[1], data[2]),
		luviFuzzValue(data[3], data[4], data[5]),
		luviFuzzValue(data[6], data[7], data[8]),
		new(big.Int).Sub(luviFuzzValue(data[9], data[10], data[11]), big.NewInt(1)), // S may be 0
	}
	if data[11]%5 == 0 {
		cur[3].SetInt64(0)
	}
	states := []LuviState{luviFuzzState(cur[0], cur[1], cur[2], cur[3])}
	data = data[12:]
	for i := 0; i+luviFuzzStepLen <= len(data) && len(states) <= luviFuzzMaxSteps; i += luviFuzzStepLen {
		b := data[i : i+luviFuzzStepLen]
		next := [4]*big.Int{new(big.Int).Set(cur[0]), new(big.Int).Set(cur[1]), new(big.Int).Set(cur[2]), new(big.Int).Set(cur[3])}
		switch b[0] % 5 {
		case 0:
			next[0] = luviFuzzValue(b[1], b[2], b[3])
			next[1] = luviFuzzValue(b[4], b[5], b[6])
			next[2] = luviFuzzValue(b[7], b[8], b[1]^b[4])
			next[3] = new(big.Int).Sub(luviFuzzValue(b[2], b[5], b[8]^b[3]), big.NewInt(1))
		case 1:
			// A' = A * n / d, R' = ceil(A*R / A')
			n, d := big.NewInt(int64(b[1])<<8|int64(b[2])+1), big.NewInt(int64(b[3])<<8|int64(b[4])+1)
			k := new(big.Int).Mul(cur[0], cur[1])
			next[1] = new(big.Int).Quo(new(big.Int).Mul(cur[1], n), d)
			if next[1].Sign() == 0 {
				next[1].SetInt64(1)
			}
			next[0] = new(big.Int).Quo(new(big.Int).Add(k, new(big.Int).Sub(next[1], big.NewInt(1))), next[1])
		case 2:
			next[3] = new(big.Int).Sub(luviFuzzValue(b[1], b[2], b[3]), big.NewInt(1))
			if b[4]%4 == 0 {
				next[3].SetInt64(0)
			}
		case 3:
			next[2] = luviFuzzValue(b[1], b[2], b[3])
		case 4:
			n, d := big.NewInt(int64(b[1])<<8|int64(b[2])+1), big.NewInt(int64(b[3])<<8|int64(b[4])+1)
			for j := range next {
				next[j] = new(big.Int).Quo(new(big.Int).Mul(cur[j], n), d)
			}
		}
		ok := true
		for j := 0; j < 3; j++ {
			if next[j].Sign() <= 0 || next[j].Cmp(luviFuzzMaxValue) > 0 {
				ok = false
			}
		}
		if next[3].Cmp(luviFuzzMaxValue) > 0 {
			ok = false
		}
		if !ok {
			continue
		}
		cur = next
		states = append(states, luviFuzzState(cur[0], cur[1], cur[2], cur[3]))
	}
	return states
}

// checkLuviSequence observes states in order, checking every step, and
// returns the final G and C (fixed point).
func checkLuviSequence(t *testing.T, states []LuviState) (*big.Int, *big.Int) {
	t.Helper()
	tr, err := NewLuviTracker(states[0])
	if err != nil {
		t.Fatalf("initial state %+v: %s", states[0], err)
	}
	for i := 1; i < len(states); i++ {
		prev, next := states[i-1], states[i]
		g0 := new(big.Int).Set(tr.g)
		star0 := new(big.Int).Add(prev.lpEquityPerUnit(), tr.c)

		// round trip on a copy: prev -> next -> prev
		cp := &LuviTracker{prev: tr.prev, c: new(big.Int).Set(tr.c), g: new(big.Int).Set(tr.g)}
		if _, err = cp.Observe(next); err != nil {
			t.Fatalf("step %d: %s", i, err)
		}
		if _, err = cp.Observe(prev); err != nil {
			t.Fatalf("step %d back: %s", i, err)
		}
		// expected: dL * (v_next - v_prev), with each v rounded like Observe
		dL := new(big.Int).Sub(next.LPUnits.BigInt(), prev.LPUnits.BigInt())
		want := new(big.Int).Mul(dL, next.sqrtAR())
		want.Quo(want, next.PoolUnits.BigInt())
		w2 := new(big.Int).Mul(dL, prev.sqrtAR())
		want.Sub(want, w2.Quo(w2, prev.PoolUnits.BigInt()))
		got := new(big.Int).Sub(cp.g, g0)
		if !luviWithin(got, want, big.NewInt(2)) {
			t.Fatalf("step %d: round trip G %s, want %s\nprev %+v\nnext %+v", i, got, want, prev, next)
		}

		// an unchanged state is an exact zero step
		same := &LuviTracker{prev: tr.prev, c: new(big.Int).Set(tr.c), g: new(big.Int).Set(tr.g)}
		if _, err = same.Observe(prev); err != nil || same.g.Cmp(g0) != 0 || same.c.Cmp(tr.c) != 0 {
			t.Fatalf("step %d: unchanged state moved G to %s (err %v)", i, same.g, err)
		}

		if _, err = tr.Observe(next); err != nil {
			t.Fatalf("step %d: %s", i, err)
		}
		dG := new(big.Int).Sub(tr.g, g0)
		sumS := new(big.Int).Add(prev.SynthSupply.BigInt(), next.SynthSupply.BigInt())
		constS := prev.SynthSupply.Equal(next.SynthSupply)
		constL := prev.LPUnits.Equal(next.LPUnits)
		constDepths := prev.BalanceRune.Equal(next.BalanceRune) && prev.BalanceAsset.Equal(next.BalanceAsset)

		// S and L fixed: dG == d sqrt(A*R), whatever the regime. Each sqrt
		// price is floored (error < 1 per unit of S), and Q is floored.
		if constS && constL {
			want = new(big.Int).Sub(next.sqrtAR(), prev.sqrtAR())
			tol := new(big.Int).Add(sumS, big.NewInt(4))
			if !luviWithin(dG, want, tol) {
				t.Fatalf("step %d: S, L fixed: dG %s, want d sqrt(AR) %s (tol %s)\nprev %+v\nnext %+v", i, dG, want, tol, prev, next)
			}
		}
		// depths and L fixed: dG == dQ exactly (no repricing, no flow)
		if constDepths && constL {
			want = new(big.Int).Sub(next.lpEquity(), prev.lpEquity())
			if dG.Cmp(want) != 0 {
				t.Fatalf("step %d: depths fixed: dG %s, want dQ %s", i, dG, want)
			}
		}
		// below overhang at both ends: dG == L_new * dLUVI*. Errors: three
		// floored per-unit values times L_new, the floored dC times L_new,
		// and the chain's floored synth units inside P_prev.
		if !prev.IsOverhang() && !next.IsOverhang() {
			star1 := new(big.Int).Add(next.lpEquityPerUnit(), tr.c)
			want = new(big.Int).Mul(next.LPUnits.BigInt(), new(big.Int).Sub(star1, star0))
			tol := luviIdentityTol(prev, next)
			if !luviWithin(dG, want, tol) {
				t.Fatalf("step %d: dG %s != L_new*dLUVI* %s (tol %s)\nprev %+v\nnext %+v", i, dG, want, tol, prev, next)
			}
		}
		// no synths at either end: dG == L_new * dLUVI (classic)
		if prev.SynthSupply.IsZero() && next.SynthSupply.IsZero() {
			want = new(big.Int).Mul(next.LPUnits.BigInt(), new(big.Int).Sub(next.chainLuvi(), prev.chainLuvi()))
			tol := new(big.Int).Add(new(big.Int).Mul(next.LPUnits.BigInt(), big.NewInt(2)), new(big.Int).Abs(dL))
			tol.Add(tol, big.NewInt(8))
			if !luviWithin(dG, want, tol) {
				t.Fatalf("step %d: no synths: dG %s != L_new*dLUVI %s (tol %s)", i, dG, want, tol)
			}
		}
	}
	return tr.g, tr.c
}

// luviIdentityTol bounds |dG - L_new*dLUVI*| in fixed-point units.
func luviIdentityTol(prev, next LuviState) *big.Int {
	l1 := next.LPUnits.BigInt()
	tol := new(big.Int).Mul(l1, big.NewInt(4))
	tol.Add(tol, new(big.Int).Add(prev.SynthSupply.BigInt(), next.SynthSupply.BigInt()))
	tol.Add(tol, big.NewInt(8))
	// synth units are floored on chain, so P_prev is up to 1 unit low and the
	// flow is valued up to |dL| * sqrt(AR)/P^2 high
	dL := new(big.Int).Abs(new(big.Int).Sub(l1, prev.LPUnits.BigInt()))
	p := prev.PoolUnits.BigInt()
	e := new(big.Int).Mul(dL, prev.sqrtAR())
	e.Quo(e, new(big.Int).Mul(p, p))
	tol.Add(tol, e.Add(e, dL))
	return tol
}

func luviWithin(got, want, tol *big.Int) bool {
	d := new(big.Int).Sub(got, want)
	return d.CmpAbs(tol) <= 0
}

func luviTrackerSeeds() [][]byte {
	step := func(op byte, p ...byte) []byte {
		b := make([]byte, luviFuzzStepLen)
		b[0] = op
		copy(b[1:], p)
		return b
	}
	var seeds [][]byte
	starts := [][]byte{
		{0x27, 0x10, 10, 0x27, 0x10, 6, 0x27, 0x10, 10, 0, 0, 5},        // ETH-like, no synths
		{0x27, 0x10, 10, 0x27, 0x10, 6, 0x27, 0x10, 10, 0x46, 0x50, 6},  // heavy synths (~180%)
		{0x27, 0x10, 10, 0x27, 0x10, 6, 0x27, 0x10, 10, 0x75, 0x30, 6},  // overhang (300%)
		{0x27, 0x10, 10, 0x27, 0x10, 6, 0xff, 0xff, 33, 0x75, 0x30, 6},  // overhang, ~6e37 LP units
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0},                            // 1-unit pool
		{0xff, 0xff, 34, 0xff, 0xff, 34, 0xff, 0xff, 34, 0xff, 0xff, 1}, // 1e38 everything
	}
	ops := [][]byte{
		step(1, 0x01, 0x00, 0x01, 0x10), // price move
		step(2, 0x10, 0x00, 7),          // synth change
		step(1, 0x01, 0x10, 0x01, 0x00), // price move back
		step(4, 0x01, 0x10, 0x01, 0x00), // proportional deposit
		step(3, 0x30, 0x00, 10),         // LP units change
		step(2, 0, 0, 0, 0),             // all synths burned
		step(0, 0x12, 0x34, 9, 0x56, 0x78, 5, 0x9a, 0xbc),
		step(4, 0x01, 0x00, 0x01, 0x40), // proportional withdrawal
	}
	for _, s := range starts {
		seq := append([]byte{}, s...)
		for _, o := range ops {
			seq = append(seq, o...)
		}
		seeds = append(seeds, seq)
	}
	return seeds
}
