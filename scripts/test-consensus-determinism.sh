#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

# The repository pins this version in go.mod. Ignore a host-level override.
export GOTOOLCHAIN=go1.26.10
export GOMAXPROCS=4
# Respect a caller-supplied build cache (CI sets GOCACHE to the runner's
# persistent /go-build-cache). Only fall back to a dedicated cache locally.
export GOCACHE="${GOCACHE:-/tmp/thornode-test-quality-determinism-go-cache}"

flock -w 600 /tmp/thornode-test-quality-build.lock \
  timeout 15m go test -p 2 -timeout 15m ./app \
  -run '^TestConsensusDeterminism' -count=1 -v
