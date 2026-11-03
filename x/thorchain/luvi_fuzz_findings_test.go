package thorchain

import (
	"errors"
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Findings from FuzzLuviPoolOps (luvi_fuzz_test.go). Each test pins the
// behavior as found, so a fix shows up as a failure here: invert the test
// then, and widen the fuzz domain to match. None is live on mainnet today
// (VirtualMultSynthsBasisPoints is 10,000 and MINTSYNTHS/BURNSYNTHS are set),
// see docs/concepts/luvi-invariant.md.

// FINDING: with a virtual depth multiplier m > 1 (mocknet uses m = 2) synth
// swaps are not G-neutral. A burn of e = s/A pays out s*R/(A + s/m) RUNE
// (output plus liquidity fee, both leave the pool) where dG = 0 needs
// s*R/(1 + e/4)^2, so it overpays whenever 1/2 + e/16 > 1/m and e < 4: for
// every burn at m = 2 (a third-order loss, dG/sqrt(AR) ~ -e^3/32), for
// e > 8/3 at m = 1.5, never at m = 1. Large mints lose too: the liquidity fee
// is computed against the virtual depth but converted to RUNE against the
// real one, so it removes up to m times too much RUNE from the pool.
func (s *LuviInvariantSuite) TestLuviFindingSynthSwapsWithVirtualDepth(c *C) {
	for _, r := range []luviRegime{{"light synths", 40}, {"heavy synths", 180}} {
		// m = 1 (mainnet): mints and burns never lower G
		e := newLuviEnv(c, r)
		var minted cosmos.Uint
		e.assertNotDown(e.step(func() { minted = e.mintSynth(luviPoolRune * 8 / 10) }), r.name+" m=1 mint 0.8R")
		e.assertNotDown(e.step(func() { e.burnSynth(minted) }), r.name+" m=1 burn")

		// m = 2: a 1% burn reads -e^3/32
		e = newLuviEnv(c, r)
		e.mult = 20_000
		eps := int64(100)
		step := e.step(func() { e.burnSynth(cosmos.NewUint(luviPoolAsset / uint64(eps))) })
		want := cosmos.NewDec(-1).QuoInt64(32 * eps * eps * eps)
		e.assertBetween(step, want.MulInt64(11).QuoInt64(10), want.MulInt64(9).QuoInt64(10), r.name+" m=2 burn 1% of A")

		// m = 2: a mint of 0.8R loses
		e = newLuviEnv(c, r)
		e.mult = 20_000
		e.assertDown(e.step(func() { e.mintSynth(luviPoolRune * 8 / 10) }), r.name+" m=2 mint 0.8R")
	}

	// m = 1.5: a burn of 3A overpays, 2A does not (needs S >= 3A)
	for _, burn := range []struct {
		times uint64
		down  bool
	}{{2, false}, {3, true}} {
		e := newLuviEnv(c, luviRegime{"deep overhang", 700})
		e.mult = 15_000
		step := e.step(func() { e.burnSynth(cosmos.NewUint(luviPoolAsset * burn.times)) })
		name := fmt.Sprintf("m=1.5 burn %dA", burn.times)
		if burn.down {
			e.assertDown(step, name)
		} else {
			e.assertNotDown(step, name)
		}
	}
}

// FINDING (fixed): deposits into a pool in overhang mint units against the
// clamped pool units P = L*(1+S) (TestOverhangDepositMultipliesUnits), so each
// one multiplies L by about 1+S*f. After a few deposits P*(r*A + a*R) no
// longer fit a LegacyDec and calculatePoolUnits panicked ("Int overflow"),
// which halts the chain from POLCycle. The overhang guard now rejects such
// deposits, and with the guard off calculatePoolUnits returns an error
// instead of panicking.
func (s *LuviInvariantSuite) TestLuviFindingOverhangAddOverflowPanics(c *C) {
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	c.Assert(e.state().IsOverhang(), Equals, true)
	_, err := e.tryAddLiquidity(luviPoolRune/100, luviPoolAsset/100)
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "overhang"), Equals, true, Commentf("%s", err))

	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
	var failed error
	adds := 0
	for ; adds < 10 && failed == nil; adds++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.Fatalf("deposit %d panicked: %v", adds+1, r)
				}
			}()
			_, failed = e.tryAddLiquidity(luviPoolRune/100, luviPoolAsset/100)
		}()
	}
	c.Assert(failed, NotNil, Commentf("no overflow after %d deposits", adds))
	c.Check(errors.Is(failed, errPoolUnitsOverflow), Equals, true, Commentf("%s", failed))
	c.Logf("deposit %d failed cleanly: %v", adds, failed)
}
