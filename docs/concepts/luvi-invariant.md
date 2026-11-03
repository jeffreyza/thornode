# LUVI Invariant

The synth-aware LUVI index G measures the change in hedged LP equity of a
pool. It is defined in `x/thorchain/types/type_luvi.go`, checked block by
block by `test/luvicheck`, and used as an oracle by the fuzz targets
`FuzzLuviTracker` (`x/thorchain/types/type_luvi_fuzz_test.go`) and
`FuzzLuviPoolOps` (`x/thorchain/luvi_fuzz_test.go`). This page states the
invariants precisely, so a formal-verification tool or a fuzzing harness can
target them.

## Definitions

For a pool with RUNE depth `R`, asset depth `A`, LP units `L`, synth supply `S`
and on-chain pool units `P` (`L + SynthUnits`, with `SynthUnits = L*S/(2A-S)`,
the denominator clamped to 1 at or above `S = 2A`):

```text
p          = R / A                          price
q          = sqrt(p)
Q          = sqrt(A*R) * (2A - S) / (2A)    signed LP equity, negative in overhang
v          = sqrt(A*R) / P                  classic LUVI, as the chain values a unit
overhang   : S >= 2A
```

Between two consecutive observations `0 -> 1`:

```text
dG     = (Q1 - Q0) + (S0 + S1)/4 * (q1 - q0) - (L1 - L0) * v0
dC     = (S0 + S1)/4 * (q1 - q0) / L1
LUVI*  = Q/L + C
norm   = dG / sqrt(A0*R0)
```

G is valid in every regime, overhang included. `LUVI*` is meaningful only
below overhang. All arithmetic is `big.Int` fixed point with 36 decimals, so
results are deterministic.

## Identities of the index

These hold for any sequence of valid states (`A, R, L, P > 0`), up to the
stated number of fixed-point rounding steps (each `1e-36`). `FuzzLuviTracker`
checks all of them, on values up to `1e38`, including deep overhang.

| Property         | Statement                                                                                | Rounding                                                  |
| ---------------- | ---------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| Total            | `Observe` never panics on a valid state, and is deterministic                            | exact                                                     |
| Stationary       | an unchanged state gives `dG = 0`, `dC = 0`                                              | exact                                                     |
| Reversible       | observing `s0 -> s1 -> s0` changes G by `(L1 - L0) * (v1 - v0)`; zero when L is constant | 2 steps                                                   |
| Price neutrality | S and L constant: `dG = sqrt(A1*R1) - sqrt(A0*R0)`, any regime                           | `S0 + S1 + 4` steps                                       |
| Free synths      | depths and L constant: `dG = Q1 - Q0` (a mint for nothing is a loss of `s*q/2`)          | exact                                                     |
| LUVI\* identity  | neither end in overhang: `dG = L1 * (LUVI*1 - LUVI*0)`                                   | `4*L1 + S0 + S1 + 8 + abs(dL) * (1 + sqrtAR0/P0^2)` steps |
| Classic identity | `S0 = S1 = 0`: `dG = L1 * (v1 - v0)`                                                     | `2*L1 + abs(dL) + 8` steps                                |

The reversible property gives the synth round trip: a mint and a burn of the
same synths at the same price return G to where it started.

## Invariants per operation

Each operation, observed on its own (one step), must satisfy the bound below.
`norm` is `dG / sqrt(A0*R0)`. A drop larger than the bound is a loss to LPs.
`luvicheck.Monitor` checks the lower bounds: its `DefaultTolerance` is `1e-12`,
plus the computed allowances it lists in `test/luvicheck/luvicheck.go`.

| Operation                                             | Bound on `norm`                                                                                |
| ----------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| Swap RUNE <-> asset                                   | `abs(norm) <= tol + k*(1/A + 1/R)`: swaps are k-neutral, the liquidity fee goes to the Reserve |
| Synth mint or burn (virtual depth `m = 1`)            | `norm >= -(tol + k*(1/A + 1/R))`                                                               |
| Outbound fee taken in the asset                       | as a swap: the RUNE is disbursed at `R*f/(A+f)`, which keeps `A*R`                             |
| Add or withdraw liquidity (any asymmetry)             | `norm >= -(tol + (1/A + 1/R))` per flow                                                        |
| Donate, block rewards                                 | `norm > 0`                                                                                     |
| Gas reimbursement or vault slash of `g` asset         | `norm >= -(g/A)^2`: the Reserve or bond pays RUNE at spot, costing `1 - sqrt(1-(g/A)^2)`       |
| Slash the bond can't cover                            | first-order drop: a real loss                                                                  |
| Errata, ragnarok, staged pool cost, store write-downs | known loss events: excused by their events, not by a bound                                     |

`k` is a small count of base-unit roundings per swap (the harness uses 4 per
swap or fee event). On large pools `tol` alone covers it. On small pools it
does not: one base unit is `1/R` of `sqrt(A*R)`, so below about `1e12` base
units a side a single rounded unit exceeds `1e-12`, and a Monitor without a
per-swap rounding allowance reports swaps there as drops.

The LP-unit ledger must hold alongside G: for every block, the change in L
equals the units of that block's `add_liquidity` events minus those of its
`withdraw` events (`types.CheckLPUnitLedger`), except for pools named by an
`errata` event. A failed handler must leave the pool, the synth supply and LP
records unchanged (handlers and the swap queue run in a cache context).

## Blind spots

- G cannot see L1 vault drains or phantom inbound credits: it reads only the
  pool record and the synth supply.
- In overhang, over-minted LP units are nearly worthless until the pool leaves
  overhang, so G stays flat. The ledger catches them.

## Observation granularity

G is a path integral sampled at observations. When the synth supply changes
at a price other than a step's endpoints, the trapezoid term
`(S0+S1)/4 * (q1-q0)` misattributes the repricing by `s * (q_mid - q0) / 4`
for `s` synths changed at `q_mid`. A same-pool double swap (asset to synth, as
in a savers deposit) of size `e = x/A` changes S after its first leg moved the
price, and reads about `-e^2/4` when observed only before and after. Observe
at the intermediate price (the fuzz harness rebuilds it from the first swap
event) or allow `abs(dS) * abs(q_path - q_end) / 4`. A block-level monitor
sees the same error for any block that mixes price moves with mints or burns.

## Known deviations (found by fuzzing)

These are pinned by `x/thorchain/luvi_fuzz_findings_test.go`.

- Virtual depth `m > 1` (`VirtualMultSynthsBasisPoints`; mocknet uses 20,000,
  mainnet 10,000). A burn of `e = s/A` pays out `s*R/(A + s/m)` RUNE where
  `dG = 0` needs `s*R/(1 + e/4)^2` (for `e < 4`; above that any payout is
  fine). It overpays when `1/2 + e/16 > 1/m`: every burn at `m = 2`
  (`norm ~ -e^3/32`), burns above `8A/3` at `m = 1.5`, never at `m = 1`. Large
  mints lose too: the fee is computed on the virtual depth and converted to
  RUNE on the real one. The fuzz target runs at `m = 1`.
- Deposits into an overhang pool mint units against `P = L*(1+S)`, so each
  multiplies L by about `1 + S*f`. After a few deposits the intermediates
  leave the `LegacyDec` range and `calculatePoolUnits` returns
  `errPoolUnitsOverflow` (it used to panic). `LUVIOverhangGuard` (on by
  default) rejects these deposits first.

## Running the targets

```bash
go test -tags=mocknet -run='^$' -fuzz=FuzzLuviPoolOps -fuzztime=60s ./x/thorchain/
go test -tags=mocknet -run='^$' -fuzz=FuzzLuviTracker -fuzztime=60s ./x/thorchain/types/
```

Without `-fuzz` both run their seed corpus (the `f.Add` seeds and
`testdata/fuzz/`) as normal tests. They use the standard `testing.F`
signature and read no flags, so OSS-Fuzz's `compile_native_go_fuzzer_v2` can
build them; build with `-tags=mocknet`.
