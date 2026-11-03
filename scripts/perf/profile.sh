#!/usr/bin/env bash
# CPU + alloc profiles of the mainnet-weighted benchmarks, to rank hot functions.
#
#   scripts/perf/profile.sh <label> [bench-regex]
#
# One profile per benchmark (pprof merges sub-benchmarks of one run), written to
# test/performance/results/<label>/<bench>.{cpu,mem}.pprof with top tables in
# <bench>.top.txt. Open interactively with:
#   go tool pprof -http=: test/performance/results/<label>/thorchain.test <bench>.cpu.pprof
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
bench=${2:-$PERF_BENCH}
out="$PERF_RESULTS/$label"
mkdir -p "$out"
perf_env_check
perf_build_test_binary "$PERF_ROOT" "$out/thorchain.test"

# list the top-level benchmarks matching the regex
mapfile -t benches < <(cd "$PERF_ROOT/$PERF_PKG" && "$out/thorchain.test" -test.list="$bench" | grep '^Benchmark')
for b in "${benches[@]}"; do
  perf_log "profiling $b"
  PERF_BENCHTIME=${PERF_BENCHTIME_SET:-3s} perf_run_bench "$PERF_ROOT" "$out/thorchain.test" "^$b\$" 1 \
    -test.cpuprofile="$out/$b.cpu.pprof" -test.memprofile="$out/$b.mem.pprof" >"$out/$b.bench.txt"
  {
    echo "### $b"
    cat "$out/$b.bench.txt"
    echo
    echo "#### CPU flat"
    go tool pprof -tagfocus=perf=measured -top -nodecount=40 "$out/thorchain.test" "$out/$b.cpu.pprof" 2>/dev/null | sed -n '1,50p'
    echo
    echo "#### CPU cumulative"
    go tool pprof -tagfocus=perf=measured -top -cum -nodecount=60 "$out/thorchain.test" "$out/$b.cpu.pprof" 2>/dev/null | sed -n '1,70p'
    echo
    echo "#### alloc_space"
    go tool pprof -top -sample_index=alloc_space -nodecount=30 "$out/thorchain.test" "$out/$b.mem.pprof" 2>/dev/null | sed -n '1,40p'
  } >"$out/$b.top.txt"
done
perf_log "wrote $out/*.top.txt"
