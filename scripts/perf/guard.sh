#!/usr/bin/env bash
# Behavior guard for a perf candidate (the working tree) against <base-ref>.
#
#   scripts/perf/guard.sh <label> [base-ref]
#
# 1. builds thornode and vets the touched packages
# 2. unit tests (-tags=mocknet) for touched packages
# 3. regtest, unfiltered, for base and candidate (cached by tree hash), then
#    regtest_diff.py: any state/event/app_hash difference FAILS, and so does
#    any gas_used or last_results_hash difference (consensus-visible: the perf
#    work must not change behavior).
#
# Env: SKIP_REGTEST=1, RUN=<suite-regex>, REGTEST_PARALLELISM (default 8), REGTEST_TIMEOUT (default 2400s).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
base_ref=${2:-HEAD}
out="$PERF_RESULTS/$label"
mkdir -p "$out" "$PERF_CACHE"
base_sha=$(git -C "$PERF_ROOT" rev-parse "$base_ref")

mapfile -t changed < <(
  git -C "$PERF_ROOT" diff --name-only "$base_sha" -- '*.go'
  git -C "$PERF_ROOT" ls-files --others --exclude-standard -- '*.go'
)
# touched packages plus the two that exercise nearly all consensus code (~1 min)
mapfile -t pkgs < <({
  printf '%s\n' "${changed[@]}" | grep -v '^$' | xargs -r -n1 dirname | sed 's|^|./|'
  if ((${#changed[@]})); then printf '%s\n' ./x/thorchain ./common/...; fi
} | sort -u)
perf_log "touched packages: ${pkgs[*]:-none}"

perf_log "build + vet"
(cd "$PERF_ROOT" && go build -tags=mocknet ./cmd/thornode && rm -f thornode)
if ((${#pkgs[@]})); then
  (cd "$PERF_ROOT" && go vet -tags=mocknet "${pkgs[@]}")
  perf_log "unit tests"
  if ! (cd "$PERF_ROOT" && go test -tags=mocknet -count=1 "${pkgs[@]}" >"$out/unit.log" 2>&1); then
    perf_log "unit tests FAILED; see $out/unit.log"
    grep -E '^(--- FAIL|FAIL|ok )' "$out/unit.log" | head -40 >&2
    exit 1
  fi
fi

if [[ -n ${SKIP_REGTEST-} ]]; then
  perf_log "SKIP_REGTEST set; not running regtest"
  exit 0
fi

base_tree="$PERF_CACHE/tree-$base_sha"
if [[ ! -d $base_tree ]]; then
  git -C "$PERF_ROOT" worktree add --detach "$base_tree" "$base_sha" >/dev/null
fi
base_hash=$(perf_tree_hash "$base_tree")
cand_hash=$(perf_tree_hash "$PERF_ROOT")
base_run=$(perf_regtest_dir "$base_hash")
cand_run=$(perf_regtest_dir "$cand_hash")
perf_regtest "$base_tree" "$base_run"
if [[ $cand_hash == "$base_hash" ]]; then
  perf_log "candidate tree == base tree"
fi
perf_regtest "$PERF_ROOT" "$cand_run"

rc=0
python3 "$(dirname "$0")/regtest_diff.py" "$base_run" "$cand_run" >"$out/guard.txt" || rc=$?
cat "$out/guard.txt"
if [[ $rc -eq 0 || ${RETRY_FLAKES:-1} == 0 ]]; then
  exit "$rc"
fi

# Full parallel runs occasionally leak an injected tx from a previous suite on
# the same harness routine into the next suite's blocks. Re-run only the
# differing suites, fresh and in isolation, for base and candidate; the
# candidate passes only if those runs match.
read -r -a suites < <(sed -n 's/^FAILED_SUITES: //p' "$out/guard.txt")
if ((${#suites[@]} == 0 || ${#suites[@]} > 10)); then
  exit "$rc"
fi
run_re="suites/($(
  IFS='|'
  echo "${suites[*]}"
))\.yaml"
perf_log "re-running ${#suites[@]} differing suite(s) in isolation: $run_re"
rm -rf "$PERF_CACHE/recheck-base" "$PERF_CACHE/recheck-cand"
RUN=$run_re REGTEST_PARALLELISM=${#suites[@]} perf_regtest "$base_tree" "$PERF_CACHE/recheck-base"
RUN=$run_re REGTEST_PARALLELISM=${#suites[@]} perf_regtest "$PERF_ROOT" "$PERF_CACHE/recheck-cand"
python3 "$(dirname "$0")/regtest_diff.py" "$PERF_CACHE/recheck-base" "$PERF_CACHE/recheck-cand" | tee "$out/guard-recheck.txt"
exit "${PIPESTATUS[0]}"
