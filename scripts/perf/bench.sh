#!/usr/bin/env bash
# Benchmark the working tree.
#
#   scripts/perf/bench.sh <label> [bench-regex]
#
# Writes test/performance/results/<label>/bench.txt. COUNT (default 10) runs.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
bench=${2:-$PERF_BENCH}
count=${COUNT:-10}
out="$PERF_RESULTS/$label"
mkdir -p "$out"

perf_env_check
git -C "$PERF_ROOT" rev-parse HEAD >"$out/commit"
git -C "$PERF_ROOT" diff >"$out/diff.patch"

perf_log "building test binary"
perf_build_test_binary "$PERF_ROOT" "$out/thorchain.test"
perf_log "running $bench x$count"
perf_run_bench "$PERF_ROOT" "$out/thorchain.test" "$bench" "$count" | tee "$out/bench.txt"
if grep -qE '^(--- FAIL|FAIL|panic)' "$out/bench.txt"; then
  perf_log "benchmark failures; see $out/bench.txt"
  exit 1
fi
