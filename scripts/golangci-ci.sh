#!/usr/bin/env bash

# Run golangci-lint over the whole module for each build, at the version trunk
# pins in .trunk/trunk.yaml. trunk runs it once per package directory with
# --concurrency 1, so each directory type-checks its dependencies again: a
# full (--all) trunk check took 23 min at p50 in CI on 2026-10-02..07, against
# about 4 min for this with a cold cache and seconds with a warm one. One
# process also sees issues that cross files, such as a helper left unused by a
# change elsewhere in its package.

set -euo pipefail

VERSION=$(sed -n 's/^ *- golangci-lint@\([0-9.]*\)$/\1/p' .trunk/trunk.yaml)
if [ -z "$VERSION" ]; then
  echo "golangci-lint version not found in .trunk/trunk.yaml"
  exit 1
fi

# go run caches the built binary in GOCACHE. The analysis cache lives under
# XDG_CACHE_HOME (/trunk-cache in CI), which concurrent jobs share safely.
CGO_ENABLED=1 go run "github.com/golangci/golangci-lint/cmd/golangci-lint@v${VERSION}" run \
  --config .trunk/configs/.golangci.yaml --timeout 20m --exclude gofmt ./...

# Keep the default build coverage and also analyze mainnet-only source and tests.
CGO_ENABLED=1 go run "github.com/golangci/golangci-lint/cmd/golangci-lint@v${VERSION}" run \
  --config .trunk/configs/.golangci.yaml --timeout 20m --exclude gofmt --build-tags mainnet ./...

# test/simulation is its own module, so ./... above stops at its go.mod. trunk
# ran golangci-lint from that directory, where it found no config file and used
# its default linters (errcheck, govet, ineffassign, staticcheck, unused).
# --no-config keeps that same coverage. The repo config reports 62 issues there
# that trunk never enforced.
(cd test/simulation && CGO_ENABLED=1 go run "github.com/golangci/golangci-lint/cmd/golangci-lint@v${VERSION}" run \
  --no-config --timeout 20m --exclude gofmt ./...)
