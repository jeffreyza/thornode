package luvicheck

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	btc = "BTC.BTC"
	eth = "ETH.ETH"
)

// snap builds a pools-endpoint entry with pool units computed as the chain does.
func snap(t *testing.T, asset string, rune, assetDepth, lpUnits, synths uint64) PoolSnapshot {
	t.Helper()
	pool := types.NewPool()
	pool.Asset = common.Asset{Chain: common.BTCChain, Symbol: "BTC", Ticker: "BTC"}
	pool.BalanceRune = cosmos.NewUint(rune)
	pool.BalanceAsset = cosmos.NewUint(assetDepth)
	pool.LPUnits = cosmos.NewUint(lpUnits)
	state := types.NewLuviState(pool, cosmos.NewUint(synths))
	return PoolSnapshot{
		Asset:        asset,
		BalanceRune:  state.BalanceRune.String(),
		BalanceAsset: state.BalanceAsset.String(),
		LPUnits:      state.LPUnits.String(),
		PoolUnits:    state.PoolUnits.String(),
		SynthSupply:  state.SynthSupply.String(),
	}
}

// swapRuneIn moves a pool along x*y=k (output floored, as the chain does).
func swapRuneIn(rune, assetDepth, in uint64) (uint64, uint64) {
	k := new(big.Int).Mul(new(big.Int).SetUint64(rune), new(big.Int).SetUint64(assetDepth))
	newRune := rune + in
	newAsset := new(big.Int).Quo(k, new(big.Int).SetUint64(newRune))
	newAsset.Add(newAsset, big.NewInt(1)) // pool keeps the rounding
	return newRune, newAsset.Uint64()
}

func event(kind string, attrs ...string) map[string]string {
	e := map[string]string{"type": kind}
	for i := 0; i+1 < len(attrs); i += 2 {
		e[attrs[i]] = attrs[i+1]
	}
	return e
}

func observe(t *testing.T, m *Monitor, height int64, pools []PoolSnapshot, events ...map[string]string) []Violation {
	t.Helper()
	v, err := m.ObserveBlock(height, pools, events)
	if err != nil {
		t.Fatalf("observe at %d: %v", height, err)
	}
	return v
}

func expectNone(t *testing.T, v []Violation) {
	t.Helper()
	if len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

func expectOne(t *testing.T, v []Violation, pool string, kind ViolationKind) {
	t.Helper()
	if len(v) != 1 || v[0].Pool != pool || v[0].Kind != kind {
		t.Fatalf("expected one %s violation in %s, got %v", kind, pool, v)
	}
}

const (
	r0 = 100_000_000_000_000 // 1M RUNE
	a0 = 1_000_000_000_000   // 10k asset
	l0 = 50_000_000_000_000
)

func TestFirstObservationIsBaseline(t *testing.T) {
	m := NewMonitor()
	// no previous state, so nothing to compare against, however odd the pool
	expectNone(t, observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)}))
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)}))
}

func TestRewardAndSwapPass(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	// block reward
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0+1_000_000, a0, l0, 0)}))
	// swap along the curve
	r, a := swapRuneIn(r0+1_000_000, a0, 5_000_000_000_000)
	expectNone(t, observe(t, m, 3, []PoolSnapshot{snap(t, btc, r, a, l0, 0)}))
}

func TestTheftIsADrop(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0), snap(t, eth, r0, a0, l0, 0)})
	v := observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0, a0-a0/1000, l0, 0), snap(t, eth, r0, a0, l0, 0)})
	expectOne(t, v, btc, Drop)
	d := v[0]
	// sqrt(1 - 1e-3) - 1 ~ -5e-4
	if !d.NormalizedDeltaG.LT(cosmos.NewDecWithPrec(-49, 5)) || !d.NormalizedDeltaG.GT(cosmos.NewDecWithPrec(-51, 5)) {
		t.Fatalf("unexpected magnitude %s", d.NormalizedDeltaG)
	}
	if !strings.Contains(d.Error(), "BTC.BTC dropped at height 2") {
		t.Fatalf("unexpected message %q", d.Error())
	}
}

func TestDropJustBelowToleranceIsNoise(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	// losing one base unit of RUNE is ~5e-15 of sqrt(AR)
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0-1, a0, l0, 0)}))
	m.Tolerance = cosmos.ZeroDec()
	expectOne(t, observe(t, m, 3, []PoolSnapshot{snap(t, btc, r0-2, a0, l0, 0)}), btc, Drop)
}

func TestLedger(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})

	// symmetric deposit of 1%, with its event
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0*101/100, a0*101/100, l0*101/100, 0)},
		event("add_liquidity", "pool", btc, "liquidity_provider_units", "500000000000")))

	// symmetric withdraw, with its event
	expectNone(t, observe(t, m, 3, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)},
		event("withdraw", "pool", btc, "liquidity_provider_units", "500000000000")))

	// units minted with no event: the value check passes, the ledger does not
	v := observe(t, m, 4, []PoolSnapshot{snap(t, btc, r0*101/100, a0*101/100, l0*101/100, 0)})
	expectOne(t, v, btc, Ledger)

	// an event for a different pool doesn't count
	v = observe(t, m, 5, []PoolSnapshot{snap(t, btc, r0*102/100, a0*102/100, l0*102/100, 0)},
		event("add_liquidity", "pool", eth, "liquidity_provider_units", "500000000000"))
	expectOne(t, v, btc, Ledger)
}

func TestErrataExemptsLedgerButNotValue(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	// errata of an add: units burned without a unit event, and the depth goes
	// with them, so neither check fires
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0*99/100, a0*99/100, l0*99/100, 0)},
		event("errata", "asset", btc)))
	// errata of a swap: depth leaves with no units burned, a real loss
	v := observe(t, m, 3, []PoolSnapshot{snap(t, btc, r0*99/100, a0*98/100, l0*99/100, 0)},
		event("errata", "asset", btc))
	expectOne(t, v, btc, Drop)
}

func TestPoolLifecycleResets(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	// pool removed (a staged-pool burn withdraws every LP and suspends it),
	// then back with less depth: no comparison across the gap
	expectNone(t, observe(t, m, 2, nil,
		event("withdraw", "pool", btc, "liquidity_provider_units", fmt.Sprint(l0)),
		event("pool", "pool", btc, "pool_status", "Suspended")))
	expectNone(t, observe(t, m, 3, []PoolSnapshot{snap(t, btc, r0/2, a0/2, l0, 0)}))
	// pool emptied by its last withdrawal: forgotten, then re-seeded
	expectNone(t, observe(t, m, 4, []PoolSnapshot{snap(t, btc, 0, 0, 0, 0)},
		event("withdraw", "pool", btc, "liquidity_provider_units", fmt.Sprint(l0))))
	expectNone(t, observe(t, m, 5, []PoolSnapshot{snap(t, btc, r0/4, a0/4, l0, 0)}))
	// but a drop after re-seeding is still caught
	expectOne(t, observe(t, m, 6, []PoolSnapshot{snap(t, btc, r0/4, a0/5, l0, 0)}), btc, Drop)
}

func TestDepletionIsADrop(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rune, asst uint64
	}{{"asset", r0, 0}, {"rune", 0, a0}} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMonitor()
			observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
			// one side drained with the LPs' units untouched
			v := observe(t, m, 2, []PoolSnapshot{snap(t, btc, tc.rune, tc.asst, l0, 0)})
			expectOne(t, v, btc, Drop)
			if !v[0].NormalizedDeltaG.Equal(cosmos.NewDec(-1)) || !strings.Contains(v[0].Error(), "depleted with LP units outstanding") {
				t.Fatalf("unexpected violation %v", v[0])
			}
			// the refill is a fresh baseline
			expectNone(t, observe(t, m, 3, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)}))
			expectNone(t, observe(t, m, 4, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)}))
		})
	}

	// gas or a slash paying out the whole asset depth is covered by the
	// allowance, as on a live pool (the RUNE reimbursement isn't checked)
	for _, paid := range []map[string]string{
		event("gas", "asset", btc, "asset_amt", fmt.Sprint(a0)),
		event("slash", "pool", btc, btc, fmt.Sprintf("-%d", a0)),
	} {
		m := NewMonitor()
		observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
		expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, 2*r0, 0, l0, 0)}, paid))
	}

	// but paying out only part of it doesn't explain an empty pool
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	v := observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0, 0, l0, 0)}, event("gas", "asset", btc, "asset_amt", fmt.Sprint(a0/10)))
	expectOne(t, v, btc, Drop)

	// units minted while depleting: both checks fire
	m = NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	v = observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0, 0, 2*l0, 0)})
	if len(v) != 2 || v[0].Kind != Ledger || v[1].Kind != Drop {
		t.Fatalf("expected a ledger and a drop violation, got %v", v)
	}
}

func TestRetiredPoolLedger(t *testing.T) {
	seed := func() *Monitor {
		m := NewMonitor()
		observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
		return m
	}
	withdrawAll := event("withdraw", "pool", btc, "liquidity_provider_units", fmt.Sprint(l0))
	withdrawPart := event("withdraw", "pool", btc, "liquidity_provider_units", fmt.Sprint(l0/2))

	// last withdrawal, pool still listed with zero units or gone from the list
	expectNone(t, observe(t, seed(), 2, []PoolSnapshot{snap(t, btc, 0, 0, 0, 0)}, withdrawAll))
	expectNone(t, observe(t, seed(), 2, nil, withdrawAll))

	// units vanish beyond what the withdrawals report
	expectOne(t, observe(t, seed(), 2, []PoolSnapshot{snap(t, btc, 0, 0, 0, 0)}, withdrawPart), btc, Ledger)
	expectOne(t, observe(t, seed(), 2, nil, withdrawPart), btc, Ledger)
	expectOne(t, observe(t, seed(), 2, nil), btc, Ledger)

	// errata exempts the ledger, as for live pools, but only for the pool it names
	expectNone(t, observe(t, seed(), 2, nil, event("errata", "asset", btc)))
	expectOne(t, observe(t, seed(), 2, nil, event("errata", "asset", eth)), btc, Ledger)

	// a pool status event doesn't: removals withdraw every LP with events
	for _, status := range []string{"Staged", "Suspended", "Available"} {
		expectOne(t, observe(t, seed(), 2, nil, event("pool", "pool", btc, "pool_status", status)), btc, Ledger)
		expectOne(t, observe(t, seed(), 2, []PoolSnapshot{snap(t, btc, 0, 0, 0, 0)}, event("pool", "pool", btc, "pool_status", status)), btc, Ledger)
	}
	expectNone(t, observe(t, seed(), 2, nil, withdrawAll, event("pool", "pool", btc, "pool_status", "Suspended")))
}

func TestSynthsPriceMoveIsNotADrop(t *testing.T) {
	for _, synths := range []uint64{a0 / 5, a0, a0 * 19 / 10, a0 * 3} { // 10%, 50%, 95%, 150% of 2A
		m := NewMonitor()
		r, a := uint64(r0), uint64(a0)
		before := snap(t, btc, r, a, l0, synths)
		observe(t, m, 1, []PoolSnapshot{before})
		for i, in := range []uint64{r0 / 10, r0 / 7} {
			r, a = swapRuneIn(r, a, in)
			after := snap(t, btc, r, a, l0, synths)
			expectNone(t, observe(t, m, int64(i+2), []PoolSnapshot{after}))

			// classic LUVI reads this k-neutral swap as a loss, which is what
			// the synth-aware index fixes (at 95% the first swap crosses into
			// overhang, after which the clamped classic index is ~0 and flat)
			s0, _ := before.ToState()
			s1, _ := after.ToState()
			if !s0.IsOverhang() && !s1.ChainLuvi().LT(s0.ChainLuvi()) {
				t.Fatalf("synths %d: expected classic LUVI to fall (%s -> %s)", synths, s0.ChainLuvi(), s1.ChainLuvi())
			}
			before = after
		}
	}
}

func TestSynthMintIsNotADrop(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, a0/2)})
	// minting: RUNE in, synths out, at the post-swap price
	r, a := uint64(r0+r0/100), uint64(a0)
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r, a, l0, a0/2+a0/100-a0/100/100)}))
}

func TestOverhangRewardIsNotADrop(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 3*a0)})
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0+r0/1000, a0, l0, 3*a0)}))
	expectOne(t, observe(t, m, 3, []PoolSnapshot{snap(t, btc, r0, a0, l0, 3*a0)}), btc, Drop)
}

func TestViolationsSortedByPool(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, eth, r0, a0, l0, 0), snap(t, btc, r0, a0, l0, 0)})
	v := observe(t, m, 2, []PoolSnapshot{snap(t, eth, r0/2, a0, l0, 0), snap(t, btc, r0/2, a0, l0, 0)})
	if len(v) != 2 || v[0].Pool != btc || v[1].Pool != eth {
		t.Fatalf("unexpected order %v", v)
	}
}

func TestBadSnapshot(t *testing.T) {
	m := NewMonitor()
	p := snap(t, btc, r0, a0, l0, 0)
	p.BalanceRune = "abc"
	if _, err := m.ObserveBlock(1, []PoolSnapshot{p}, nil); err == nil {
		t.Fatal("expected a parse error")
	}
	// empty fields read as zero, which makes the pool invalid and skipped
	p = PoolSnapshot{Asset: btc}
	expectNone(t, observe(t, m, 1, []PoolSnapshot{p}))
}

func TestLossEventPools(t *testing.T) {
	pools := LossEventPools([]map[string]string{
		event("errata", "asset", "BTC.BTC"),
		event("gas", "asset", "ETH.ETH"),
		event("pool_balance_change", "asset", "AVAX.AVAX"),
		event("slash", "pool", "BSC.BNB"),
		event("pool", "pool", "DOGE.DOGE", "pool_status", "Staged"),
		event("swap", "pool", "LTC.LTC"),
		event("rewards", "BCH.BCH", "100"),
	})
	for _, p := range []string{"BTC.BTC", "ETH.ETH", "AVAX.AVAX", "BSC.BNB", "DOGE.DOGE"} {
		if !pools[p] {
			t.Errorf("expected %s to be a loss-event pool", p)
		}
	}
	for _, p := range []string{"LTC.LTC", "BCH.BCH"} {
		if pools[p] {
			t.Errorf("did not expect %s to be a loss-event pool", p)
		}
	}
}

func TestGasAllowance(t *testing.T) {
	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	// 0.25% of the asset paid as gas, reimbursed at spot: costs ~3e-6
	g := uint64(a0 / 400)
	r := g * (r0 / a0)
	gas := event("gas", "asset", btc, "asset_amt", fmt.Sprint(g), "rune_amt", fmt.Sprint(r))
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0+r, a0-g, l0, 0)}, gas))

	// the same payout without its gas event is a drop
	m = NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	expectOne(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0+r, a0-g, l0, 0)}), btc, Drop)

	// gas reimbursed at half price is a first-order loss the allowance can't cover
	m = NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	gas = event("gas", "asset", btc, "asset_amt", fmt.Sprint(g), "rune_amt", fmt.Sprint(r/2))
	expectOne(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0+r/2, a0-g, l0, 0)}, gas), btc, Drop)
}

func TestLPFlowTimingAllowance(t *testing.T) {
	// a 0.1% reward lands, then 10% of units withdraw at the new unit value, in
	// one block: G values the burn at the old unit value and reads ~-1e-4
	reward := uint64(r0 / 1000)
	r1 := r0 + reward
	w := uint64(l0 / 10)
	rOut := r1 / 10
	aOut := uint64(a0 / 10)
	withdraw := event("withdraw", "pool", btc, "liquidity_provider_units", fmt.Sprint(w))

	m := NewMonitor()
	m.Tolerance = cosmos.ZeroDec()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	expectNone(t, observe(t, m, 2, []PoolSnapshot{snap(t, btc, r1-rOut, a0-aOut, l0-w, 0)}, withdraw))

	// the allowance scales with the flow: theft alongside a small withdraw is caught
	m = NewMonitor()
	observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
	small := uint64(l0 / 1000)
	v := observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0-r0/1000-r0/100, a0-a0/1000, l0-small, 0)},
		event("withdraw", "pool", btc, "liquidity_provider_units", fmt.Sprint(small)))
	expectOne(t, v, btc, Drop)
}

func TestSlashAllowance(t *testing.T) {
	g := uint64(a0 / 400)
	for _, tc := range []struct {
		rune uint64
		drop bool
	}{
		{g * (r0 / a0), false},    // bond covers it at spot
		{g * (r0 / a0) / 2, true}, // shortfall: a first-order loss
	} {
		m := NewMonitor()
		observe(t, m, 1, []PoolSnapshot{snap(t, btc, r0, a0, l0, 0)})
		slash := event("slash", "pool", btc, btc, fmt.Sprintf("-%d", g), "THOR.RUNE", fmt.Sprint(tc.rune))
		v := observe(t, m, 2, []PoolSnapshot{snap(t, btc, r0+tc.rune, a0-g, l0, 0)}, slash)
		if tc.drop {
			expectOne(t, v, btc, Drop)
			if !strings.Contains(v[0].Error(), "asset 1000000000000->997500000000") {
				t.Fatalf("expected the states in the message, got %q", v[0].Error())
			}
		} else {
			expectNone(t, v)
		}
	}
}

func TestLPFlowRoundingAllowance(t *testing.T) {
	// a 1 BTC pool at ~1927 RUNE per BTC: a POL withdraw whose asset share
	// rounds up by one base unit pays ~1922 RUNE base units too many
	// (test/regression/suites/rune-pool/rune-pool-pol.yaml, height 13)
	prev := PoolSnapshot{
		Asset: btc, BalanceRune: "192698549398", BalanceAsset: "100000000",
		LPUnits: "100293038159", PoolUnits: "100293038159", SynthSupply: "0",
	}
	next := prev
	next.BalanceRune, next.LPUnits, next.PoolUnits = "191736295847", "100042314710", "100042314710"
	withdraw := event("withdraw", "pool", btc, "liquidity_provider_units", "250723449")

	m := NewMonitor()
	observe(t, m, 1, []PoolSnapshot{prev})
	expectNone(t, observe(t, m, 2, []PoolSnapshot{next}, withdraw))

	// the same state change with the units burned by an errata (no flow event)
	// gets no rounding allowance
	m = NewMonitor()
	observe(t, m, 1, []PoolSnapshot{prev})
	expectOne(t, observe(t, m, 2, []PoolSnapshot{next}, event("errata", "asset", btc)), btc, Drop)
}
