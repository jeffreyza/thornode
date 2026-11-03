#!/usr/bin/env bash
# Wall-clock time of the test suites, the secondary perf metric.
#
#   scripts/perf/test-times.sh <label> [unit|regtest|sim ...]   (default: unit regtest)
#
# unit:    go test -tags=mocknet over TEST_PATHS (as `make test`), uncached, with
#          per-package elapsed from -json -> <label>/unit-packages.tsv
# regtest: perf_regtest of the working tree (cached by tree hash), seconds from
#          the run plus per-block latency stats from block-times.csv
# sim:     make test-simulation (slow; opt in)
# Results: test/performance/results/<label>/times.json
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
shift
parts=("$@")
((${#parts[@]})) || parts=(unit regtest)
out="$PERF_RESULTS/$label"
mkdir -p "$out"
declare -A secs

# only this run's regtest may feed the block stats below
rm -f "$out/block-times.csv"
for part in "${parts[@]}"; do
  case $part in
  unit)
    perf_log "unit tests (uncached)"
    mapfile -t pkgs < <(cd "$PERF_ROOT" && go list ./... | grep -v bifrost/tss/go-tss)
    # warm the build cache so compile time is not counted
    if ! (cd "$PERF_ROOT" && go test -tags=mocknet -count=1 -run='^$' "${pkgs[@]}" >/dev/null 2>&1); then
      perf_log "test build had failures"
    fi
    start=$(date +%s)
    if ! (cd "$PERF_ROOT" && go test -tags=mocknet -count=1 -json "${pkgs[@]}" >"$out/unit.json" 2>"$out/unit.stderr"); then
      perf_log "unit tests had failures (timing still recorded)"
    fi
    secs[unit]=$(($(date +%s) - start))
    python3 - "$out/unit.json" >"$out/unit-packages.tsv" <<'PY'
import json, sys
el = {}
for line in open(sys.argv[1]):
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("Test") is None and e.get("Action") in ("pass", "fail") and "Elapsed" in e:
        el[e["Package"]] = (e["Elapsed"], e["Action"])
for pkg, (s, a) in sorted(el.items(), key=lambda kv: -kv[1][0]):
    print(f"{s:.2f}\t{a}\t{pkg}")
PY
    head -15 "$out/unit-packages.tsv" >&2
    ;;
  regtest)
    hash=$(perf_tree_hash "$PERF_ROOT")
    run=$(perf_regtest_dir "$hash")
    perf_regtest "$PERF_ROOT" "$run"
    secs[regtest]=$(cat "$run/seconds")
    cp "$run/block-times.csv" "$out/block-times.csv" 2>/dev/null || true
    ;;
  sim)
    start=$(date +%s)
    (cd "$PERF_ROOT" && CI=1 make test-simulation) >"$out/sim.log" 2>&1 || perf_log "simulation failed (timing still recorded)"
    secs[sim]=$(($(date +%s) - start))
    ;;
  *)
    perf_log "unknown part $part"
    exit 2
    ;;
  esac
done

{
  echo "{"
  echo "  \"commit\": \"$(git -C "$PERF_ROOT" rev-parse HEAD)\","
  echo "  \"dirty\": $([[ -n $(git -C "$PERF_ROOT" status --porcelain) ]] && echo true || echo false),"
  first=1
  for k in "${!secs[@]}"; do
    ((first)) || echo ","
    first=0
    printf '  "%s_seconds": %s' "$k" "${secs[$k]}"
  done
  if [[ -f $out/block-times.csv ]]; then
    echo ","
    python3 - "$out/block-times.csv" <<'PY'
import sys, statistics
us = [int(l.rsplit(",", 1)[1]) for l in open(sys.argv[1]) if l.strip()]
us.sort()
q = lambda p: us[min(len(us) - 1, int(p * len(us)))]
print(f'  "regtest_blocks": {len(us)}, "regtest_block_us_p50": {q(.5)}, "regtest_block_us_p90": {q(.9)}, "regtest_block_us_mean": {int(statistics.mean(us))}', end="")
PY
  fi
  echo
  echo "}"
} | tee "$out/times.json"
