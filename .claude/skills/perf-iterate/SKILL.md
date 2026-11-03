---
name: perf-iterate
description: Run a round of the THORNode performance-improvement loop - refresh the mainnet usage mix, profile the mainnet-weighted benchmarks, propose 10 behavior-preserving speedups, A/B each one, guard kept ones with regtest, and commit the winners. Use whenever the user asks to make thornode faster, run a perf round, optimize hot paths, speed up block processing or the test suites, or iterate on performance.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# Perf iteration

The loop makes THORNode faster **without changing behavior**. Every idea is measured on its own,
kept only if it clears the bar, and committed one per commit with its numbers.

Tooling (all under `scripts/perf/`, `make perf-*` wrappers, results in `test/performance/results/<label>/`, gitignored):

| Step        | Command                                                             | Output                                                                    |
| ----------- | ------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| Mainnet mix | `LIQUIFY_API_KEY=... make perf-mix`                                 | `test/performance/mainnet-mix.json`, `testdata/mainnet-shape.json`        |
| Profile     | `scripts/perf/profile.sh <label> [bench-re]`                        | `<bench>.top.txt` flat/cum/alloc tables (measured loop only)              |
| A/B         | `PERF_TARGET=<re> scripts/perf/ab.sh <label> [bench-re] [base-ref]` | interleaved base/candidate, `benchstat.txt`, `verdict.txt`, exit 0 = KEEP |
| Guard       | `scripts/perf/guard.sh <label> [base-ref]`                          | vet + unit tests of touched pkgs + unfiltered regtest diff vs base        |
| Test times  | `scripts/perf/test-times.sh <label> [unit regtest sim]`             | `times.json`                                                              |

Benchmarks live in `x/thorchain/perf_bench_test.go` (`BenchmarkPerf*`). The fixture is seeded
from the mainnet shape (44 pools, mimir, ~100 active validators with real keys, 6 asgard vaults)
and is deterministic, so `gas/op` is stable and any change to it is real.

## Build tags matter

Chain benchmarks use `PERF_TAGS=mainnet` (the release build, default). Unit tests, regtest and the
simulation use `mocknet`, which compiles extra code that mainnet never runs (e.g. env-var mimir
overrides in `keeper_mimir_mocknet.go`). An idea that only touches mocknet code is a **test-speed**
idea: A/B it with `PERF_TAGS=mocknet` and measure it with `test-times.sh`, never claim it as a chain win.

## The round

1. **Refresh the mix** if the committed one is older than ~2 weeks (`make perf-mix`; key in env,
   never committed). Note shifts in the top message types.
2. **Profile**: `scripts/perf/profile.sh r<N>-prof`. Rank functions by cumulative CPU in the
   handlers/EndBlock, weighted by the mix (MsgObservedTxQuorum ~68% of tx gas, then
   MsgTssKeysignFail, MsgPriceFeedQuorumBatch; streaming swaps dominate EndBlock).
3. **Write 10 ideas** into `test/performance/results/rounds/round-<N>.md` (scratch, not committed). Each:
   target function, target benchmark regex, why it is faster, why behavior is identical,
   whether it changes store access inside a tx (=> gas_used changes, see below).
4. **For each idea, one at a time, on a clean tree at the current base commit:**
   1. implement it, minimal diff, matching surrounding style;
   2. `go build ./... && go vet -tags=mocknet <pkg>`;
   3. `PERF_TARGET='<target-re>' scripts/perf/ab.sh r<N>-i<k> '<bench-re>'` (include at least
      one non-target heavy benchmark, e.g. `EndBlock`, to catch regressions);
   4. REVERT verdict -> `git checkout -- . && git clean -fd -- <new files>`; log why;
   5. KEEP verdict -> `scripts/perf/guard.sh r<N>-i<k>`; run the `blockchain-code-review` skill
      on the diff; then commit:
      `perf(<area>): <idea>` with the benchstat sec/op + allocs/op table in the body. A verdict or
      guard that flags tx gas is a REVERT, never a commit.
   6. the new commit is the base for the next idea.
5. **Round end**: cumulative `CONFIRM=0 scripts/perf/ab.sh r<N>-total '^BenchmarkPerf' <round-base>` on
   both `PERF_TAGS=mainnet` and `mocknet`; `scripts/perf/test-times.sh` for unit (run the base side
   from its cached worktree); then one simulation on the final commit. If the `thornode-simtest`
   image build fails fetching tss-lib ("no secure protocol found": the final RUN's /go/pkg/mod cache
   mount hides the modules the authenticated stages downloaded), run it natively instead:
   `GO_PRIVATE_GIT_CREDENTIAL=oauth2:$(glab config get token --host gitlab.com) make build-mocknet reset-mocknet`,
   `docker stop docker-midgard-1 docker-midgard-db-1`, then from `test/simulation`:
   `go build -tags mocknet -o <scratch>/simtest ./cmd && CI=1 PARALLELISM=1 STAGES=<TEST_SIMULATION_STAGES> <scratch>/simtest`,
   and `make stop-mocknet` afterwards. Summarize: idea -> delta sec/op, delta allocs, kept/reverted.

   Regtest wall time is bound by consensus pacing (~1.75s/block from timeout_commit), and total
   unit wall time by bifrost/p2p and other timer-bound packages, so CPU wins show up in the
   per-package times (x/thorchain) and `block-times.csv`, not the totals.

## Mainnet replay (real blocks)

`scripts/perf/mainnet-replay.sh` replays a window of real mainnet blocks through two release
(`TAG=mainnet`) binaries from the same restored state; CometBFT checks every block's app hash
against mainnet, so it measures the real effect and proves consensus identity at once. Build the
candidate as the deployed release tag plus the commits under test (gas-only changes replay fine:
the handshake checks app hashes, not LastResultsHash). State setup is slow (hours; see the script header), so reuse `~/.cache/thornode-perf/mainnet-replay`
when it exists. Round 1+2 non-gas commits: 2000 blocks, 820s -> 455s (-44.5%), p99 1032 -> 477 ms.

## Keep bar

`verdict.py`: at least one target benchmark improves >= 5% in sec/op or allocs/op with p < 0.05,
and no benchmark's sec/op regresses significantly by > 5%, and the KEEP must reproduce on an
independent confirmation pass (`ab.sh` does this; identical code shows chance "significant" ±5%
results across ~20 benchmarks). Use `ROUNDS=10` (default). Micro
benchmarks are noisier than handler ones; if a micro win does not show up in any handler or
EndBlock benchmark, it is not worth the churn.

## Behavior must not change

The guard fails on any difference in regtest exports, block events, or app_hash. Rules for ideas:

- Never change protobuf/amino wire formats, store keys, event types/attributes or their order,
  error strings that reach events, or iteration order of anything that feeds state or events.
- No map iteration feeding state, no floats, no wall-clock time, no goroutines in the state machine.
- Caches must be pure (input -> output, e.g. bech32 decode, constant-name -> key) or scoped to a
  context that is discarded with it. A cache over keeper state must be invalidated on writes and on
  cache-context discard; prefer not to.
- **Gas**: the ante handler installs an infinite gas meter, but gas is still counted and
  `gas_used` is hashed into the next header's LastResultsHash. Fewer/more store reads, writes,
  iterator steps or bytes inside a **tx** changes gas_used, which changes behavior: not allowed.
  Keep every store access on tx paths (handlers, ante, and anything a CosmWasm query reaches);
  share only the decoding of the bytes read (e.g. memoize by the exact bytes). verdict.py
  REVERTs on tx gas/op changes and guard.sh FAILs on any gas_used difference.
  BeginBlock/EndBlock store access does not affect gas_used, nor do API queries; contract
  queries (`CtxWASMQuery`, the `wasm/` quote benchmarks) run inside a tx and do.
- Version-gated or mainnet-only behavior stays exactly as is.
