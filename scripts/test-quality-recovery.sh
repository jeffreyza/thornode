#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export GOMAXPROCS=4
export GOTOOLCHAIN=go1.26.10

# A missing pinned toolchain or inaccessible dependencies must fail the run.
flock -w 1800 /tmp/thornode-test-quality-build.lock bash -c '
  set -e
  timeout 20m go test -race -p 2 ./bifrost/signer \
    -run "^TestPackage$" -check.f TestRecovery -count=1
  timeout 20m go test -race -p 2 ./bifrost/observer \
    -run "^TestRecoveryObservationErrataCrashMatrix$" -count=1
'
