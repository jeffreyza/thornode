#!/usr/bin/env bash

# This script wraps execution of trunk when run in CI.

set -euo pipefail

SCRIPT_DIR="$(dirname "$0")"
# golangci-lint runs on its own in scripts/golangci-ci.sh, over the whole
# module. Every other linter checks all 3,300 files in about 30s, so there is no
# upstream diff: develop passes this same --all check, so on a merge request any
# issue is the branch's, and the job needs no merge base or git history.
FLAGS=(--jobs=4 --ci --all --filter=-golangci-lint)

# run trunk, retrying once if it crashed with an internal error or lost a tool
# install race. The internal-error banner means trunk died before reporting
# (2026-09-14..28: 21 lint failures, none with findings). "lockf() failed"
# comes from two lint jobs installing the same tool into the shared cache at
# once, which happens when the cache is cold or .trunk/trunk.yaml bumps a tool;
# the retry finds the tool installed. A retry reruns every linter, so it cannot
# hide a real finding.
run_trunk() {
  echo "Running: $SCRIPT_DIR/trunk check ${FLAGS[*]}"
  exec 3>&1
  OUT=$("$SCRIPT_DIR"/trunk check "${FLAGS[@]}" | tee /dev/fd/3)
  STATUS=$?
  exec 3>&-
}
set +e
run_trunk
if [ "$STATUS" -ne 0 ] && echo "$OUT" | grep -qE "Trunk Internal Error|lockf\(\) failed"; then
  echo
  echo "Trunk crashed or lost a tool install race; retrying once."
  sleep 10
  run_trunk
fi
set -e
if [ "$STATUS" -ne 0 ]; then
  exit "$STATUS"
fi
