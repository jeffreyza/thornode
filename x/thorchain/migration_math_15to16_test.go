package thorchain

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Plain (non-gocheck) table tests for the pure ADR-028 math cores. They run under
// the default mocknet CI build, where the //go:build mainnet migration body is a
// no-op stub — exactly the branches the snapshot dry-run cannot exercise at
// arbitrary inputs. Expected values are hand-computed; see migration_math_15to16.go.

func mu(n uint64) cosmos.Uint { return cosmos.NewUint(n) }

func TestAdr028SplitFreed(t *testing.T) {
	for _, freed := range []uint64{0, 1, 2, 3, 4, 5, 100, 175_184_824_163, 287_392_444_998} {
		toBurn, toReserve := adr028SplitFreed(mu(freed))
		if !toBurn.Add(toReserve).Equal(mu(freed)) {
			t.Errorf("freed=%d: burn+reserve != freed (conservation)", freed)
		}
		if toBurn.LT(toReserve) {
			t.Errorf("freed=%d: burn(%s) < reserve(%s) — odd 1e-8 unit must go to burn", freed, toBurn, toReserve)
		}
		if common.SafeSub(toBurn, toReserve).GT(mu(1)) {
			t.Errorf("freed=%d: burn-reserve gap > 1", freed)
		}
	}
	// Explicit odd case: 3 → burn 2, reserve 1.
	if b, r := adr028SplitFreed(mu(3)); !b.Equal(mu(2)) || !r.Equal(mu(1)) {
		t.Errorf("freed=3: got burn=%s reserve=%s, want 2/1", b, r)
	}
}

func TestAdr028OverflowBps(t *testing.T) {
	cases := []struct {
		name                                           string
		postL, postA, synthSupply, targetT, saverDepth uint64
		want                                           uint64
	}{
		{"zero postL → no lever", 0, 100, 80, 150, 1000, 0},
		{"zero saverDepth → no lever", 100, 100, 80, 150, 0, 0},
		{"non-clamped regime", 100, 100, 80, 150, 1000, 140},     // sTarget=200*50/150=66; b=14; 14*1e4/1000=140
		{"clamped/overhang regime", 10, 10, 100, 260, 1000, 750}, // clampedS=250/10=25≥20; b=75; 75*1e4/1000=750
		{"bps clamps at 10000", 10, 10, 225, 260, 100, 10000},    // b=200; 200*1e4/100=20000→10000
		{"target already met (==)", 10, 10, 25, 260, 1000, 0},    // synthSupply==sTarget(25)
		{"target already met (<)", 10, 10, 24, 260, 1000, 0},
	}
	for _, c := range cases {
		if got := adr028OverflowBps(mu(c.postL), mu(c.postA), mu(c.synthSupply), mu(c.targetT), mu(c.saverDepth)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAdr028PairedCut(t *testing.T) {
	cases := []struct {
		name                      string
		R, A, L, T, sPre, burnAmt uint64
		wantR, wantA              uint64
	}{
		{"degenerate R=0", 0, 100, 500, 2000, 300, 30, 0, 0},
		{"degenerate A=0", 1000, 0, 500, 2000, 300, 30, 0, 0},
		{"degenerate sPre=0", 1000, 100, 500, 2000, 0, 30, 0, 0},
		{"overhang sPre>2A", 1000, 100, 500, 2000, 300, 30, 100, 10}, // R*X/S=1000*30/300=100; A*X/S=10
		{"overhang boundary sPre==2A", 1000, 100, 500, 2000, 200, 20, 100, 10},
		{"non-overhang sPre<2A", 1000, 100, 500, 2000, 150, 30, 150, 15}, // drop=2000*80-200*500=60000; R*drop/(2AT)=1000*60000/400000=150
		{"non-overhang T=0 guard", 1000, 100, 500, 0, 150, 30, 0, 0},     // 2AT=0 → degenerate guard returns (0,0)
	}
	for _, c := range cases {
		dR, dA := adr028PairedCut(mu(c.R), mu(c.A), mu(c.L), mu(c.T), mu(c.sPre), mu(c.burnAmt))
		if !dR.Equal(mu(c.wantR)) || !dA.Equal(mu(c.wantA)) {
			t.Errorf("%s: got ΔR=%s ΔA=%s, want %d/%d", c.name, dR, dA, c.wantR, c.wantA)
		}
	}
}
