#!/usr/bin/env bash
# Interleaved A/B benchmark: <base-ref> (default HEAD) vs the working tree.
#
#   scripts/perf/ab.sh <label> [bench-regex] [base-ref]
#
# The base is built from a cached detached worktree, the candidate from the
# working tree. Runs alternate base/candidate ROUNDS times (default 10) with one
# sample each, so slow drift on the box hits both sides equally. Ends with the
# KEEP/REVERT verdict from verdict.py (exit 0 = KEEP, 1 = REVERT/inconclusive).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
bench=${2:-$PERF_BENCH}
base_ref=${3:-HEAD}
rounds=${ROUNDS:-10}
out="$PERF_RESULTS/$label"
mkdir -p "$out" "$PERF_CACHE"

perf_env_check

base_sha=$(git -C "$PERF_ROOT" rev-parse "$base_ref")
base_tree="$PERF_CACHE/tree-$base_sha"
base_bin="$PERF_CACHE/$(echo "$PERF_PKG" | tr '/.' '__')-$base_sha-$PERF_TAGS.test"
if [[ ! -x $base_bin ]]; then
  if [[ ! -d $base_tree ]]; then
    perf_log "creating base worktree at $base_sha"
    git -C "$PERF_ROOT" worktree add --detach "$base_tree" "$base_sha" >/dev/null
  fi
  perf_log "building base test binary"
  perf_build_test_binary "$base_tree" "$base_bin"
fi
perf_log "building candidate test binary"
perf_build_test_binary "$PERF_ROOT" "$out/candidate.test"

echo "$base_sha" >"$out/base-commit"
git -C "$PERF_ROOT" diff "$base_sha" >"$out/diff.patch"
# ab_pass <suffix>: ROUNDS interleaved base/candidate samples, benchstat, verdict
ab_pass() {
  local sfx=$1
  : >"$out/base$sfx.txt"
  : >"$out/new$sfx.txt"
  for ((i = 1; i <= rounds; i++)); do
    perf_log "pass${sfx:-1} round $i/$rounds"
    # explicit checks: callers run ab_pass under `|| rc=$?`, which turns off
    # errexit inside it, so a failed run would otherwise be benchstat input
    if ! perf_run_bench "$base_tree" "$base_bin" "$bench" 1 >>"$out/base$sfx.txt"; then
      perf_log "base benchmark run failed; see $out/base$sfx.txt"
      exit 2
    fi
    if ! perf_run_bench "$PERF_ROOT" "$out/candidate.test" "$bench" 1 >>"$out/new$sfx.txt"; then
      perf_log "candidate benchmark run failed; see $out/new$sfx.txt"
      exit 2
    fi
  done
  if grep -qE '^(--- FAIL|FAIL|panic)' "$out/base$sfx.txt" "$out/new$sfx.txt"; then
    perf_log "benchmark failures; see $out/{base,new}$sfx.txt"
    exit 2
  fi
  if ! (cd "$out" && $BENCHSTAT "base$sfx.txt" "new$sfx.txt" >"benchstat$sfx.txt"); then
    perf_log "benchstat failed"
    exit 2
  fi
  cat "$out/benchstat$sfx.txt"
  if ! (cd "$out" && $BENCHSTAT -format csv "base$sfx.txt" "new$sfx.txt" >"benchstat$sfx.csv" 2>/dev/null); then
    perf_log "benchstat (csv) failed"
    exit 2
  fi
  python3 "$(dirname "$0")/verdict.py" "$out/benchstat$sfx.csv" ${PERF_TARGET:+--target "$PERF_TARGET"} | tee "$out/verdict$sfx.txt"
  return "${PIPESTATUS[0]}"
}

# With ~20 benchmarks per run some comparisons come out "significant" by chance
# (seen at +/-5% on identical code), so a KEEP must reproduce on a second,
# independent pass. CONFIRM=0 skips it.
rc=0
ab_pass "" || rc=$?
if [[ $rc -eq 0 && ${CONFIRM:-1} != 0 ]]; then
  perf_log "KEEP on pass 1; confirming with an independent pass"
  ab_pass "-confirm" || rc=$?
  if [[ $rc -ne 0 ]]; then
    perf_log "pass 1 KEEP did not reproduce: REVERT"
  fi
fi
exit "$rc"
