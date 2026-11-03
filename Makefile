########################################################################################
# Environment Checks
########################################################################################

CHECK_ENV:=$(shell ./scripts/check-env.sh)
ifneq ($(CHECK_ENV),)
$(error Check environment dependencies.)
endif

########################################################################################
# Config
########################################################################################

.PHONY: build test test-xmr-frost-e2e tools export healthcheck run-mocknet build-mocknet stop-mocknet halt-mocknet ps-mocknet reset-mocknet logs-mocknet openapi test-performance test-performance-quick test-performance-all test-performance-analyze test-performance-profile-cpu test-performance-profile-mem test-performance-trace test-performance-clean test-xmr-e2e perf-mix perf-bench perf-ab perf-profile perf-guard perf-times ci-health test-ci-health

# mocknet docker compose files and overrides for macOS
DOCKER_COMPOSE_FILES ?= -f build/docker/docker-compose.yml
ifeq ($(shell uname -s),Darwin)
DOCKER_COMPOSE_FILES += -f build/docker/docker-compose.darwin.yml
endif

# pull branch name from CI if unset and available
ifdef CI_COMMIT_BRANCH
BRANCH?=${CI_COMMIT_BRANCH}
BUILDTAG?=${CI_COMMIT_BRANCH}
endif

# image build settings
COMMIT?=$(shell git log -1 --format='%H' 2>/dev/null)
BRANCH?=$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
GITREF?=$(shell git rev-parse --short HEAD 2>/dev/null)
BUILDTAG?=$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)

# Forward a gitlab.com credential ("user:token") into docker builds as the
# go_private_auth BuildKit secret so the in-image `go mod download` can fetch
# tss-lib-private. CI picks it with scripts/ci-go-private-auth.sh; without that,
# fall back to CI_JOB_TOKEN. Empty when running locally without either
# (Dockerfile mount is required=false).
ifeq ($(strip $(GO_PRIVATE_GIT_CREDENTIAL)),)
ifneq ($(strip $(CI_JOB_TOKEN)),)
GO_PRIVATE_GIT_CREDENTIAL := gitlab-ci-token:$(CI_JOB_TOKEN)
endif
endif
export GO_PRIVATE_GIT_CREDENTIAL
ifneq ($(strip $(GO_PRIVATE_GIT_CREDENTIAL)),)
DOCKER_BUILD_SECRETS := --secret id=go_private_auth,env=GO_PRIVATE_GIT_CREDENTIAL
else
DOCKER_BUILD_SECRETS :=
endif

# compiler flags
VERSION:=$(shell cat version)
TAG?=mocknet
ldflags = -X gitlab.com/thorchain/thornode/v3/constants.Version=$(VERSION) \
      -X gitlab.com/thorchain/thornode/v3/constants.GitCommit=$(COMMIT) \
      -X github.com/cosmos/cosmos-sdk/version.Name=THORChain \
      -X github.com/cosmos/cosmos-sdk/version.AppName=thornode \
      -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
      -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT) \
      -X github.com/cosmos/cosmos-sdk/version.BuildTags=$(TAG) \
      -buildid=

# golang settings
TEST_PATHS=$(shell go list ./... | grep -v bifrost/tss/go-tss) # Skip compute-intensive tests by default

# Parse test path argument from command line (e.g., "make test ./path")
ifeq (test,$(firstword $(MAKECMDGOALS)))
  TEST_PATH_ARG := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
  ifneq ($(TEST_PATH_ARG),)
    # Check if the argument looks like a path (contains /)
    ifneq ($(findstring /,$(TEST_PATH_ARG)),)
      # Ensure path starts with ./
      ifeq ($(filter ./%,$(TEST_PATH_ARG)),)
        TEST_DIR := ./$(TEST_PATH_ARG)
      else
        TEST_DIR := $(TEST_PATH_ARG)
      endif
      # Prevent make from treating the path as a target
      .PHONY: $(TEST_PATH_ARG)
      $(eval $(TEST_PATH_ARG):;@:)
    endif
  endif
endif

TEST_DIR?=${TEST_PATHS}
# -buildmode=pie builds a position-independent executable (ET_DYN) so host ASLR can
# randomize the binary's own text/data, not just the shared libraries it loads. It
# needs external linking, which the cgo build (wasmvm) already uses, and the release
# toolchain (golang bookworm image) ships gcc. Covers the shipped
# thornode/bifrost/recover-keyshare-backup (BINARIES) via the build/install targets.
BUILD_FLAGS := -ldflags '$(ldflags)' -tags ${TAG} -trimpath -buildmode=pie
PARALLELISM ?= 1
TEST_BUILD_FLAGS := -parallel=$(PARALLELISM) -tags=mocknet
ifdef RUN
RUN_FLAG := -run=$(RUN)
endif
# Monero scanner attribution + solvency gates (bifrost/pkg/chainclients/monero/e2e_gates_test.go).
XMR_E2E_RUN := ^(TestXMRScannerInboundNoteMemoAttribution|TestXMRScannerInboundMemolessDeferredAttribution|TestXMRScannerOutgoingMemolessDeferredAttribution|TestXMRReportSolvencyMultiVaultAccounting)$$
BINARIES?=./cmd/thornode ./cmd/bifrost ./tools/recover-keyshare-backup
GOVERSION=$(shell awk '($$1 == "go") { print $$2 }' go.mod)

# docker tty args are disabled in CI and non-interactive shells
ifndef CI
ifneq ($(shell test -t 0 && test -t 1 && echo tty),)
DOCKER_TTY_ARGS=-it
endif
endif

HTTPS_GIT := https://gitlab.com/thorchain/thornode.git

# Compare stacked MRs with their target branch; other runs keep the develop baseline.
PROTO_BREAKING_BRANCH ?= $(or $(CI_MERGE_REQUEST_TARGET_BRANCH_NAME),develop)
export PROTO_BREAKING_BRANCH

########################################################################################
# Targets
########################################################################################

# ------------------------------ Generate ------------------------------

generate: go-generate generate-abi openapi proto-gen
	@./scripts/generate.py
	@cd test/simulation && go mod tidy

go-generate:
	@go install golang.org/x/tools/cmd/stringer@v0.28.0
	@go generate ./...

# mirror router abi from single source of truth to reference locations
generate-abi:
	@cp chain/evm/router-abi.json bifrost/pkg/chainclients/evm/abi/router.json
	@cp chain/evm/router-abi.json bifrost/pkg/chainclients/ethereum/abi/router.json
	@cp chain/evm/router-abi.json build/scripts/evm/router-abi.json
	@cp chain/evm/router-abi.json test/simulation/pkg/evm/abi/router.json

openapi:
	@image='$(DOCKER_MIRROR)openapitools/openapi-generator-cli:v6.0.0@sha256:310bd0353c11863c0e51e5cb46035c9e0778d4b9c6fe6a7fc8307b3b41997a35'; \
	if ! docker image inspect "$$image" >/dev/null 2>&1 && ! docker pull -q "$$image" >/dev/null; then \
		if [ -z '$(DOCKER_MIRROR)' ]; then exit 1; fi; \
		image='openapitools/openapi-generator-cli:v6.0.0@sha256:310bd0353c11863c0e51e5cb46035c9e0778d4b9c6fe6a7fc8307b3b41997a35'; \
		docker pull -q "$$image" >/dev/null || exit 1; \
	fi; \
	docker run --rm \
		--user $(shell id -u):$(shell id -g) \
		-v $$PWD/openapi:/mnt \
		"$$image" \
		generate -i /mnt/openapi.yaml -g go -o /mnt/gen
	@rm openapi/gen/go.mod openapi/gen/go.sum
	@find ./openapi/gen -type f -print0 | xargs -0 sed -i.bak \
		-e '/^[- ]*API version.*$(shell cat version)/d' \
		-e '/APIClient.*$(shell cat version)/d'
	@find ./openapi/gen -type f -name '*.bak' -delete
	@find ./openapi/gen -type f -name '*model*' -print0 | xargs -0 sed -i.bak \
		-e 's/MarshalJSON(/MarshalJSON_deprecated(/'
	@find ./openapi/gen -type f -name '*.bak' -delete

protoVer=0.13.2
# Index digest of ghcr.io/cosmos/proto-builder:$(protoVer); bump it with protoVer.
protoDigest=sha256:05f5ccdc37d6ac6856d035d37bde91d31d36cfebe3c82ac771a40c7ff2a5c4c6
# CI sets PROTO_BUILDER_REPO to its registry mirror. The digest pins the image
# from either repository, so a tag replaced in the mirror fails to pull rather
# than running.
protoImageName=$(or $(PROTO_BUILDER_REPO),ghcr.io/cosmos/proto-builder):$(protoVer)@$(protoDigest)

# buf resolves its BSR dependencies from buf.build at generate/lint/breaking
# time. When the machine running these targets is blocked by buf.build (the
# Finland CI runner's egress IP is served HTTP 403 at their edge), set BUF_PROXY
# to a proxy with clean egress and it is forwarded into the container. Unset by
# default, so local development and unaffected runners are untouched. In CI,
# .buf-egress drops BUF_PROXY on runners that can reach buf.build directly.
BUF_PROXY ?=
ifneq ($(strip $(BUF_PROXY)),)
protoProxyEnv := -e HTTPS_PROXY=$(BUF_PROXY) -e HTTP_PROXY=$(BUF_PROXY) -e NO_PROXY=localhost,127.0.0.1
else
protoProxyEnv :=
endif

protoImage=docker run --rm -v $(CURDIR):/workspace $(protoProxyEnv) --workdir /workspace $(protoImageName)

proto-all: proto-format proto-lint proto-gen format

proto-gen:
	@echo "Generating Protobuf files"
	@$(protoImage) sh ./scripts/protocgen.sh

proto-format:
	@echo "Formatting Protobuf files"
	@$(protoImage) find ./ -name "*.proto" -exec clang-format -i {} \;

proto-format-check:
	@echo "Checking Protobuf formatting"
	@$(protoImage) find ./ -name "*.proto" -exec clang-format --dry-run -Werror {} +

proto-lint:
	@$(protoImage) sh -c "cd proto/thorchain/v1 && buf lint --error-format=json"

proto-check-breaking:
	@$(protoImage) sh -c 'cd proto/thorchain/v1 && buf breaking --against "$$1"' sh \
		"$(HTTPS_GIT)#branch=$$PROTO_BREAKING_BRANCH,subdir=proto/thorchain/v1"

# ------------------------------ Docs ------------------------------

docs-init:
	@cargo install mdbook --version 0.4.52
	@cargo install mdbook-admonish --version 1.18.0
	@cargo install mdbook-katex --version 0.9.4
	@cargo install mdbook-embed --version 0.2.0
	@cargo install mdbook-mermaid --version 0.14.0
	@cd docs && mdbook-admonish install --css-dir theme
	@cd docs && mdbook-mermaid install

docs-generate: docs-init
	@cd docs && mdbook build -d ../public

docs-dev: docs-init
	@cd docs && mdbook serve -d ../public --open

# ------------------------------ Build ------------------------------

build:
	go build ${BUILD_FLAGS} ${BINARIES}

test-xmr-frost-e2e:
	go test -count=1 -tags=mocknet ./bifrost/pkg/chainclients/monero/... -run '$${XMR_FROST_E2E_RUN}'

install:
	go install ${BUILD_FLAGS} ${BINARIES}

tools:
	go install -tags ${TAG} ./tools/pubkey2address
	go install -tags ${TAG} ./tools/p2p-check
	go install -tags ${TAG} ./tools/recover-keyshare-backup

# ------------------------------ Gitlab CI ------------------------------

gitlab-trigger-ci:
	@./scripts/gitlab-trigger-ci.sh

# ------------------------------ Housekeeping ------------------------------

format:
	@git ls-files '*.go' | grep -v -e '^docs/' -e '^api/' -e '^openapi/gen/' -e '.pb.go$$' -e '.pb.gw.go$$' -e '_gen.go$$' -e 'wire_gen.go$$' |\
		xargs gofumpt -w

lint:
	@./scripts/lint.sh
	@./scripts/trunk check --no-fix --show-existing --upstream origin/develop

.PHONY: test-lint-tokens
test-lint-tokens:
	@./scripts/tests/test-lint-tokens.bash

# The three checks are independent; run them at once, each one's output kept together.
lint-ci: test-lint-tokens
	@$(MAKE) --no-print-directory -j3 --output-sync=target lint-ci-scripts lint-ci-golangci lint-ci-trunk

lint-ci-scripts:
	@./scripts/lint.sh

lint-ci-golangci:
	@./scripts/golangci-ci.sh

lint-ci-trunk:
	@./scripts/trunk-ci.sh

# Install a pre-push hook that runs CI's lint (and, opt-in, generate) checks;
# see scripts/git-hooks/pre-push. It goes in the clone's shared hooks directory,
# so every worktree gets it, and it runs the checked-out copy of the script, so
# it follows the branch. Refuses to replace a different pre-push hook.
define PRE_PUSH_HOOK
#!/bin/sh
# Installed by `make install-git-hooks`.
hook="$$(git rev-parse --show-toplevel)/scripts/git-hooks/pre-push"
if [ -x "$$hook" ]; then exec "$$hook" "$$@"; fi
endef

install-git-hooks: export PRE_PUSH_HOOK := $(PRE_PUSH_HOOK)
install-git-hooks:
	@dest="$$(git rev-parse --path-format=absolute --git-common-dir)/hooks/pre-push"; \
	if [ -e "$$dest" ] && [ "$$(cat "$$dest")" != "$$PRE_PUSH_HOOK" ]; then \
		echo "$$dest is a different hook; not replacing it. Remove it or call scripts/git-hooks/pre-push from it."; \
		exit 1; \
	fi; \
	mkdir -p "$$(dirname "$$dest")"; \
	printf '%s\n' "$$PRE_PUSH_HOOK" >"$$dest"; \
	chmod +x "$$dest"; \
	echo "Installed $$dest"; \
	if hooks_path=$$(git config core.hooksPath); then \
		echo "Warning: core.hooksPath is $$hooks_path, so git does not run hooks from $$(dirname "$$dest")."; \
	fi

# ------------------------------ Unit Tests ------------------------------

test-coverage: test-network-specific
	@go test ${TEST_BUILD_FLAGS} -v -coverprofile=coverage.txt -covermode count ${TEST_DIR}
	sed -i.bak '/\.pb\.go:/d' coverage.txt
	@rm -f coverage.txt.bak

coverage-report: test-coverage
	@go tool cover -html=coverage.txt

# only print the coverage total: the full per-function dump pushed the CI job log
# past GitLab's 4MB cap, truncating the "total:" line the coverage regex reads
# test-network-specific runs alongside the main suite: its five sequential
# `go test` runs took 95-130s in a 6-CPU CI slot, mostly waiting on tests, and
# no longer hold up the main suite. Its output is printed when both finish, so
# the main suite's output still streams.
test-coverage-sum:
	@$(MAKE) --no-print-directory test-network-specific > network-specific.log 2>&1 & ns=$$!; \
	go run gotest.tools/gotestsum@v1.13.0 --junitfile report.xml --format testname -- ${TEST_BUILD_FLAGS} -v -coverprofile=coverage.txt -covermode count ${TEST_DIR}; main=$$?; \
	wait $$ns; ns=$$?; \
	echo "--- test-network-specific (exit $$ns)"; cat network-specific.log; \
	[ $$main -eq 0 ] && [ $$ns -eq 0 ]
	sed -i.bak '/\.pb\.go:/d' coverage.txt
	@rm -f coverage.txt.bak
	@GOFLAGS='${TEST_BUILD_FLAGS}' go run github.com/boumenot/gocover-cobertura@v1.4.0 < coverage.txt > coverage.xml
	@go tool cover -func=coverage.txt | tail -n 1
	@go tool cover -html=coverage.txt -o coverage.html

test:
ifneq ($(filter test,$(MAKECMDGOALS)),)
ifeq ($(TEST_DIR),$(TEST_PATHS))
	@$(MAKE) test-network-specific
endif
endif
	@go test ${TEST_BUILD_FLAGS} ${RUN_FLAG} ${TEST_DIR}
	@TAG=stagenet $(MAKE) build

test-all: test-network-specific
	@go test ${TEST_BUILD_FLAGS} ${RUN_FLAG} "./..."

test-go-tss:
	@python3 scripts/test-quality-run.py --tags mocknet --race --parallel $(PARALLELISM) --run '$(RUN)' --max-skips 0 --report-dir test-quality-reports/root-mocknet-go-tss ./bifrost/tss/go-tss/...

test-xmr-e2e:
	@go test -count=1 ./bifrost/pkg/chainclients/monero -run '${XMR_E2E_RUN}'

test-network-specific:
	@python3 scripts/test-quality-run.py --tags stagenet --run '$(RUN)' --report-dir test-quality-reports/network-stagenet ./common
	@python3 scripts/test-quality-run.py --tags chainnet --run '$(RUN)' --report-dir test-quality-reports/network-chainnet ./common
	@python3 scripts/test-quality-run.py --tags mainnet --run '$(RUN)' --report-dir test-quality-reports/network-mainnet ./common ./bifrost/pkg/chainclients/utxo/...
	@python3 scripts/test-quality-run.py --tags mainnet --run TestPackage --test-arg=-check.f --test-arg='Migrate|Migration19to20Suite|EthThorRestoreSuite' --report-dir test-quality-reports/network-mainnet-migration ./x/thorchain # mainnet store-migration tests run under the gocheck TestPackage entrypoint (CI unit-tests is mocknet-only). The suites Migration19to20Suite/EthThorRestoreSuite lack the substring "Migrate", so -check.f names them explicitly; this filter must stay in sync with scripts/test-quality-inventory.py SELECTIONS (enforced by test_selection_filters_match_the_makefile).
	@python3 scripts/test-quality-run.py --tags mocknet --run '$(RUN)' --report-dir test-quality-reports/network-mocknet ./common ./bifrost/pkg/chainclients/utxo/...

test-race:
	@go test -race ${TEST_BUILD_FLAGS} ${RUN_FLAG} ${TEST_DIR}

# bifrost is the concurrent half of the system; go-tss is covered by test-go-tss.
# ebifrost processes concurrent Bifrost callbacks on the thornode side.
test-race-bifrost:
	@python3 scripts/test-quality-run.py --tags mocknet --race --parallel $(PARALLELISM) --run '$(RUN)' --timeout 20m $(if $(RUN),,--require-package-tests /x/thorchain/ebifrost) --forbid-skip-package /x/thorchain/ebifrost --exclude-prefix gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss --report-dir test-quality-reports/root-mocknet-race-bifrost ./bifrost/... ./x/thorchain/ebifrost

# Each command emits inventory, JSON events, JUnit, and filtered coverage in a
# separate directory so a skipped security test cannot pass unnoticed.
.PHONY: test-quality-simulation test-quality-mainnet
test-quality-simulation:
	@python3 scripts/test-quality-run.py --module test/simulation --tags mocknet --fresh \
		--allow-skip-test TestDogeStalledBroadcastIsNotDuplicated \
		--allow-skip-test TestZecIsolatedFunding \
		--require-gocheck /actors/core:1 --require-gocheck /pkg/solana:1 \
		--require-gocheck /pkg/types:7 \
		--report-dir test-quality-reports/simulation-mocknet ./...

test-quality-mainnet:
	@python3 scripts/test-quality-run.py --tags mainnet --fresh --run '^TestNewClientRejectsNonMainnetDaemonOnMainnet$$' --require-test TestNewClientRejectsNonMainnetDaemonOnMainnet --report-dir test-quality-reports/root-mainnet-monero ./bifrost/pkg/chainclients/monero

# Tests that need a real L1 daemon. Each daemon must be fresh and used by no
# simulation, because the tests spend from the mocknet master address. Set
# ZEC_DAEMON to an official zebrad binary, and DOGE_SIM_INTEGRATION_RPC_URL and
# ZEC_SIM_INTEGRATION_RPC_URL to the mocknet dogecoind and Zebra RPC endpoints.
# A skipped test fails the run, so an unset variable cannot pass unnoticed.
.PHONY: test-utxo-daemons
test-utxo-daemons:
	@python3 scripts/test-quality-run.py --tags mocknet --fresh --run '^TestV[56]Zebra$$' \
		--require-test TestV5Zebra --require-test TestV6Zebra --max-skips 0 \
		--report-dir test-quality-reports/utxo-daemons-zebra ./bifrost/pkg/chainclients/utxo/zcash
	@python3 scripts/test-quality-run.py --module test/simulation --tags mocknet --fresh \
		--run '^(TestDogeStalledBroadcastIsNotDuplicated|TestZecIsolatedFunding)$$' \
		--require-test TestDogeStalledBroadcastIsNotDuplicated --require-test TestZecIsolatedFunding --max-skips 0 \
		--report-dir test-quality-reports/utxo-daemons-simulation ./pkg/utxo

# ------------------------------ Performance Tests ------------------------------

# Run comprehensive benchmarks with profiling (recommended)
test-performance:
	@echo "Running comprehensive performance benchmarks with profiling..."
	@./test/performance/run_quote_benchmarks.sh

# Quick benchmark comparison (Queue Disabled vs Enabled)
test-performance-quick:
	@echo "Running quick performance comparison..."
	@cd x/thorchain && go test -run='^$$' -bench='^BenchmarkQuoteSwapComparison$$' -benchtime=10x -benchmem

# Run all benchmarks without profiling
test-performance-all:
	@echo "Running all benchmark tests..."
	@cd x/thorchain && go test -run='^$$' -bench=. -benchtime=5x -benchmem

# Analyze latest performance results
test-performance-analyze:
	@echo "Analyzing latest performance results..."
	@LATEST=$$(ls -td test/performance/results/*/ 2>/dev/null | head -1); \
	if [ -n "$$LATEST" ]; then \
		./test/performance/analyze_profiles.sh "$$LATEST"; \
	else \
		echo "No performance results found. Run 'make test-performance' first."; \
		exit 1; \
	fi

# Open interactive CPU profile viewer
test-performance-profile-cpu:
	@LATEST=$$(ls -td test/performance/results/*/ 2>/dev/null | head -1); \
	if [ -n "$$LATEST" ] && [ -f "$$LATEST/cpu.prof" ]; then \
		echo "Opening CPU profile at http://localhost:8080..."; \
		go tool pprof -http=:8080 "$$LATEST/cpu.prof"; \
	else \
		echo "No CPU profile found. Run 'make test-performance' first."; \
		exit 1; \
	fi

# Open interactive memory profile viewer
test-performance-profile-mem:
	@LATEST=$$(ls -td test/performance/results/*/ 2>/dev/null | head -1); \
	if [ -n "$$LATEST" ] && [ -f "$$LATEST/mem.prof" ]; then \
		echo "Opening memory profile at http://localhost:8080..."; \
		go tool pprof -http=:8080 "$$LATEST/mem.prof"; \
	else \
		echo "No memory profile found. Run 'make test-performance' first."; \
		exit 1; \
	fi

# View execution trace
test-performance-trace:
	@LATEST=$$(ls -td test/performance/results/*/ 2>/dev/null | head -1); \
	if [ -n "$$LATEST" ] && [ -f "$$LATEST/trace.out" ]; then \
		echo "Opening execution trace viewer..."; \
		go tool trace "$$LATEST/trace.out"; \
	else \
		echo "No trace found. Run 'make test-performance' first."; \
		exit 1; \
	fi

# Clean performance test results
test-performance-clean:
	@echo "Cleaning performance test results..."
	@rm -rf test/performance/results/*
	@echo "Performance results cleaned."

# ------------------------------ Perf Iteration ------------------------------
# Mainnet-weighted perf framework; see test/performance/README.md "Perf iteration".
# LABEL names the results dir under test/performance/results; BENCH filters benchmarks.

LABEL ?= $(shell git rev-parse --short HEAD)
BENCH ?= ^BenchmarkPerf

perf-mix:
	@go run ./tools/perf/mainnet-mix

perf-bench:
	@./scripts/perf/bench.sh $(LABEL) '$(BENCH)'

perf-ab:
	@./scripts/perf/ab.sh $(LABEL) '$(BENCH)' $(BASE)

perf-profile:
	@./scripts/perf/profile.sh $(LABEL) '$(BENCH)'

perf-guard:
	@./scripts/perf/guard.sh $(LABEL) $(BASE)

perf-times:
	@./scripts/perf/test-times.sh $(LABEL)

# ------------------------------ CI Health ------------------------------
# Why MR pipeline jobs fail; see tools/ci-health/README.md.
# Pass options with CI_HEALTH_ARGS, e.g. CI_HEALTH_ARGS="--since 2026-01-01".

ci-health:
	@python3 tools/ci-health/run.py all $(CI_HEALTH_ARGS)

test-ci-health:
	@python3 -m unittest discover -s tools/ci-health -v

# ------------------------------ Regression Tests ------------------------------

REGTEST_BIN_DIR ?= /regtest
REGTEST_MNT_DIR ?= /mnt

test-regression: build-test-regression
	@docker run --rm ${DOCKER_TTY_ARGS} \
		-e DEBUG -e RUN -e EXPORT -e TIME_FACTOR -e PARALLELISM -e FAIL_FAST \
		-e AUTO_UPDATE -e IGNORE_FAILURES -e CI -e UNFILTER_EXPORT \
		-e UID=$(shell id -u) -e GID=$(shell id -g) \
		-p 1317:1317 -p 26657:26657 \
		-v $(shell pwd)/test/regression/mnt:/mnt \
		-v $(shell pwd)/test/regression/suites:/app/test/regression/suites \
		-v $(shell pwd)/test/regression/templates:/app/test/regression/templates \
		-w /app thornode-regtest sh -c 'make _test-regression'

build-test-regression:
	@DOCKER_BUILDKIT=1 docker build . \
		-t thornode-regtest \
		-f ci/Dockerfile.regtest \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DOCKER_MIRROR \
		$(DOCKER_BUILD_SECRETS)

test-regression-coverage:
	@go tool cover -html=test/regression/mnt/coverage/coverage.txt

# internal target used in docker build - version pinned for consistent app hashes
_build-test-regression:
	@go install -ldflags '$(ldflags)' -tags=mocknet,regtest ./cmd/thornode
	@go build -ldflags '$(ldflags) -X gitlab.com/thorchain/thornode/v3/constants.Version=9.999.0' \
		-cover -tags=mocknet,regtest -o $(REGTEST_BIN_DIR)/cover-thornode ./cmd/thornode
	@go build -ldflags '$(ldflags) -X gitlab.com/thorchain/thornode/v3/constants.Version=9.999.0' \
		-tags mocknet -o $(REGTEST_BIN_DIR)/regtest ./test/regression/cmd

# internal target used in test run
_test-regression:
	@rm -rf $(REGTEST_MNT_DIR)/coverage && mkdir -p $(REGTEST_MNT_DIR)/coverage
	@cd test/regression && REGTEST_BIN_DIR=$(REGTEST_BIN_DIR) REGTEST_MNT_DIR=$(REGTEST_MNT_DIR) $(REGTEST_BIN_DIR)/regtest
	@go tool covdata textfmt -i $(REGTEST_MNT_DIR)/coverage -o $(REGTEST_MNT_DIR)/coverage/coverage.txt
	@grep -v -E -e archive.go -e 'v[0-9]+.go' -e openapi/gen $(REGTEST_MNT_DIR)/coverage/coverage.txt > $(REGTEST_MNT_DIR)/coverage/coverage-filtered.txt
	@go tool cover -func $(REGTEST_MNT_DIR)/coverage/coverage-filtered.txt > $(REGTEST_MNT_DIR)/coverage/func-coverage.txt
	@awk '/^total:/ {print "Regression Coverage: " $$3}' $(REGTEST_MNT_DIR)/coverage/func-coverage.txt
	@chown -R ${UID}:${GID} $(REGTEST_MNT_DIR)

# ------------------------------ Simulation Tests ------------------------------

test-simulation: build-mocknet reset-mocknet test-simulation-no-reset

TEST_SIMULATION_STAGES ?= seed,bootstrap,xmr-coverage,arb,swaps,memoless-swaps,consolidate,memoless-refunds,solvency,ragnarok
TEST_SIMULATION_CLUSTER_STAGES ?= seed,bootstrap,xmr-coverage,xmr-spent-refs,swaps,memoless-swaps,dash-instantsend,inactive-vault-refunds,memoless-refunds,cluster-churn,tron-solvency-fragmentation,solvency,xmr-post-churn,ragnarok
# A full cluster run must fail rather than silently omit unavailable XMR coverage.
TEST_SIMULATION_REQUIRE_XMR ?= 1

.PHONY: test-mocknet-startup test-xmr-readiness-config test-simulation-xmr-readiness
test-mocknet-startup:
	@python3 -m unittest discover -s build/scripts/tests -v

# Render every XMR profile and check deployment contracts and reset authentication.
test-xmr-readiness-config:
	@python3 -m unittest discover -s build/docker/tests -p 'test_xmr_readiness.py' -v

# Exercise initial-vault creation, churn, funding, and a real BTC-to-XMR swap.
# Requires enabled XMR keygen; see docs/xmr-readiness-baseline.md.
test-simulation-xmr-readiness: TEST_SIMULATION_CLUSTER_STAGES=seed,bootstrap,xmr-post-churn
test-simulation-xmr-readiness: override TEST_SIMULATION_REQUIRE_XMR=1
test-simulation-xmr-readiness: test-simulation-cluster

test-simulation-cluster: test-mocknet-startup test-xmr-readiness-config prepare-mocknet-cluster
	@docker stop docker-midgard-1 docker-midgard-db-1 >/dev/null 2>&1 || true
	@STAGES=$(TEST_SIMULATION_CLUSTER_STAGES) docker run --rm ${DOCKER_TTY_ARGS} \
		-e PARALLELISM=$(PARALLELISM) -e STAGES \
		-e XMR_POST_CHURN_REQUIRED=$(TEST_SIMULATION_REQUIRE_XMR) \
		-e RANDOM_SEED_HEX \
		-e SIMULATION_PPROF_ADDRESS \
		-e XMR_SIM_HELPER_ENDPOINT -e XMR_SIM_HELPER_AUTH_BEARER \
		-e XMR_FROST_ENDPOINT -e XMR_FROST_AUTH_BEARER \
		--network host -w /app \
		thornode-simtest sh -c 'make _test-simulation'

# Force pig to time out after sending its round-3 payload, then require
# same-ceremony quorum adoption with a locally finalized share and four votes.
# Bootstrap must finish before netem so the P2P mesh can form. The 100ms wait
# is shorter than one 500ms network delay; transcript confirmation now has its
# own deadline, so the old two-hops-in-one-deadline assumption no longer holds.
# Keep netem modest enough for the regular Asgard keygen to finish too.
XMR_KEYGEN_RECOVERY_ROUND3_TIMEOUT_MS ?= 100
XMR_KEYGEN_RECOVERY_NETEM_DELAY_MS ?= 500

.PHONY: test-simulation-xmr-keygen-recovery
test-simulation-xmr-keygen-recovery: export XMR_KEYGEN_RECOVERY_ROUND3_TIMEOUT_MS := $(XMR_KEYGEN_RECOVERY_ROUND3_TIMEOUT_MS)
test-simulation-xmr-keygen-recovery: test-mocknet-startup test-xmr-readiness-config prepare-mocknet-cluster
	@docker compose $(DOCKER_COMPOSE_FILES) stop midgard midgard-db >/dev/null 2>&1 || true
	@echo "running seed+bootstrap BEFORE latency injection so the bifrost p2p mesh can form"
	@echo "(netem-from-start makes the multi-round-trip libp2p handshake exceed the dial"
	@echo " timeout, so no p2p connection ever forms and every churn keygen times out)"
	@STAGES=seed,bootstrap docker run --rm ${DOCKER_TTY_ARGS} \
		-e PARALLELISM=$(PARALLELISM) -e STAGES -e XMR_POST_CHURN_REQUIRED=1 \
		-e RANDOM_SEED_HEX -e SIMULATION_PPROF_ADDRESS \
		-e XMR_SIM_HELPER_ENDPOINT -e XMR_SIM_HELPER_AUTH_BEARER \
		-e XMR_FROST_ENDPOINT -e XMR_FROST_AUTH_BEARER \
		--network host -w /app \
		thornode-simtest sh -c 'make _test-simulation'
	@echo "applying netem delay $(XMR_KEYGEN_RECOVERY_NETEM_DELAY_MS)ms to bifrost p2p (port 5040)"
	@for service in bifrost bifrost-cat bifrost-fox bifrost-pig bifrost-frog; do \
		c=$$(docker compose $(DOCKER_COMPOSE_FILES) ps -q $$service); \
		[ -n "$$c" ] || exit 1; \
		docker run --rm --net container:"$$c" --cap-add NET_ADMIN alpine:3.20 sh -c "\
			apk add -q iproute2 && \
			tc qdisc replace dev eth0 root handle 1: prio bands 4 priomap 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 && \
			tc qdisc add dev eth0 parent 1:4 handle 40: netem delay $(XMR_KEYGEN_RECOVERY_NETEM_DELAY_MS)ms && \
			tc filter add dev eth0 parent 1: protocol ip u32 match ip dport 5040 0xffff flowid 1:4 && \
			tc filter add dev eth0 parent 1: protocol ip u32 match ip sport 5040 0xffff flowid 1:4" \
			|| exit 1; \
	done
	@log_dir=$$(mktemp -d); \
	touch "$$log_dir/pig.log"; \
	docker compose $(DOCKER_COMPOSE_FILES) logs --no-color --no-log-prefix --follow bifrost-pig > "$$log_dir/pig.log" 2>&1 & log_pid=$$!; \
	trap 'kill "$$log_pid" 2>/dev/null || true; wait "$$log_pid" 2>/dev/null || true; rm -f "$$log_dir/pig.log"; rmdir "$$log_dir"' EXIT; \
	STAGES=xmr-keygen-recovery docker run --rm ${DOCKER_TTY_ARGS} \
		-v "$$log_dir:/keygen-recovery:ro" -e XMR_KEYGEN_RECOVERY_LOG=/keygen-recovery/pig.log \
		-e PARALLELISM=$(PARALLELISM) -e STAGES -e XMR_POST_CHURN_REQUIRED=1 \
		-e RANDOM_SEED_HEX -e SIMULATION_PPROF_ADDRESS \
		-e XMR_SIM_HELPER_ENDPOINT -e XMR_SIM_HELPER_AUTH_BEARER \
		-e XMR_FROST_ENDPOINT -e XMR_FROST_AUTH_BEARER \
		--network host -w /app \
		thornode-simtest sh -c 'make _test-simulation'

test-simulation-no-reset: build-test-simulation
	@docker stop docker-midgard-1 docker-midgard-db-1 >/dev/null 2>&1 || true
	@STAGES=$(TEST_SIMULATION_STAGES) docker run --rm ${DOCKER_TTY_ARGS} \
		-e PARALLELISM=$(PARALLELISM) -e STAGES \
		-e RANDOM_SEED_HEX \
		-e SIMULATION_PPROF_ADDRESS \
		-e XMR_SIM_HELPER_ENDPOINT -e XMR_SIM_HELPER_AUTH_BEARER \
		-e XMR_FROST_ENDPOINT -e XMR_FROST_AUTH_BEARER \
		--network host -w /app \
		thornode-simtest sh -c 'make _test-simulation'

build-test-simulation:
	@DOCKER_BUILDKIT=1 docker build . \
		-t thornode-simtest \
		-f ci/Dockerfile.simtest \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DOCKER_MIRROR \
		$(DOCKER_BUILD_SECRETS)

test-simulation-events:
	@docker compose $(DOCKER_COMPOSE_FILES) run --rm events

# internal target used in docker build
_build-test-simulation:
	@cd test/simulation && \
		go build -ldflags '$(ldflags)' -tags mocknet -o /simtest/simtest ./cmd

# internal target used in test run
_test-simulation:
	@cd test/simulation && /simtest/simtest

# ------------------------------ Single Node Mocknet ------------------------------

cli-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) run --rm cli

run-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) \
		--profile mocknet --profile midgard up -d

stop-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet --profile midgard down -v

# Halt the Mocknet without erasing the blockchain history, so it can be resumed later.
halt-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet --profile midgard down

build-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet build \
		--build-arg COMMIT=$(COMMIT)

bootstrap-mocknet:
	@docker run --rm ${DOCKER_TTY_ARGS} \
		-e PARALLELISM=$(PARALLELISM) -e STAGES=seed,bootstrap \
		-e XMR_SIM_HELPER_ENDPOINT -e XMR_SIM_HELPER_AUTH_BEARER \
		-e XMR_FROST_ENDPOINT -e XMR_FROST_AUTH_BEARER \
		--network host -w /app \
		thornode-simtest sh -c 'make _test-simulation'

ps-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet --profile midgard images
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet --profile midgard ps

logs-mocknet:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet logs -f thornode bifrost

reset-mocknet: stop-mocknet run-mocknet

# ------------------------------ Mocknet EVM Fork ------------------------------

reset-mocknet-fork-%: stop-mocknet
	@./tools/evm/run-mocknet-fork.sh $*

# ------------------------------ Multi Node Mocknet ------------------------------

# test-simulation-cluster stops midgard before the sim starts; the CI path
# (test-simulation-cluster-native) empties this so midgard is never pulled.
MOCKNET_CLUSTER_MIDGARD ?= --profile midgard

run-mocknet-cluster:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster \
		$(MOCKNET_CLUSTER_MIDGARD) up -d
	@# Compose can leave dependent validator thornodes in Created under detached
	@# startup when the primary thornode healthcheck is not yet satisfied.
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster \
		$(MOCKNET_CLUSTER_MIDGARD) up -d thornode-cat thornode-fox thornode-pig thornode-frog

stop-mocknet-cluster:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster --profile midgard down -v

halt-mocknet-cluster:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster --profile midgard down

build-mocknet-cluster:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster build

ps-mocknet-cluster:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster --profile midgard images
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster --profile midgard ps

reset-mocknet-cluster: stop-mocknet-cluster build-mocknet-cluster run-mocknet-cluster

# L1 chains of the cluster that run a released image, so they can start before
# the thornode and simtest images are built.
MOCKNET_CLUSTER_L1_SERVICES := gaia noble bitcoin bitcoin-cash dogecoin litecoin \
	ethereum base xrp binance-smart avalanche tron-supernode tron-fullnode solana \
	polygon zcash

run-mocknet-cluster-l1:
	@docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster \
		up -d $(MOCKNET_CLUSTER_L1_SERVICES)

# reset-mocknet-cluster plus the simtest image, with the L1 chains starting in
# the background while the images build. Their image pulls and warm-up (the sim
# waits for tron and solana to advance blocks) then overlap the build instead of
# following it. run-mocknet-cluster finds them running and leaves them as is.
prepare-mocknet-cluster: stop-mocknet-cluster
	@$(MAKE) --no-print-directory run-mocknet-cluster-l1 & l1_pid=$$!; \
	$(MAKE) --no-print-directory build-test-simulation build-mocknet-cluster; build_rc=$$?; \
	wait $$l1_pid; l1_rc=$$?; \
	[ $$build_rc -eq 0 ] && [ $$l1_rc -eq 0 ]
	@$(MAKE) --no-print-directory run-mocknet-cluster

# ------------------------------ CI: native sim build ------------------------------
#
# CI's test-simulation builds the Go binaries in the job container, with the
# runner's warm Go caches, and only copies them into thin images
# (build/docker/Dockerfile.native). The in-image builds started from an empty
# dind every run: `go mod download` (the tss-lib-private fetch) plus a cold
# compile of simtest, thornode and events, 6-8 min before the cluster could
# start. The job must run on golang:1.26.4-bookworm, the build stage of
# build/docker/Dockerfile, so the binaries link against the same glibc as the
# bookworm runtime. Local `make test-simulation-cluster` is unchanged.
NATIVE_SIM_DIR ?= $(CURDIR)/tmp/native-sim

# Same flags as `make install` in build/docker/Dockerfile. That build has no .git
# and takes COMMIT from its `ARG COMMIT=unknown`, so match it.
_build-native-thornode: COMMIT=unknown
_build-native-thornode: TAG=mocknet
_build-native-thornode:
	@go build -ldflags '$(ldflags)' -tags mocknet -trimpath -buildmode=pie \
		-o $(NATIVE_SIM_DIR)/ $(BINARIES)
	@# Same as tools/events/Dockerfile: `go install -tags $$TAG ./tools/events`.
	@go build -tags mocknet -o $(NATIVE_SIM_DIR)/events ./tools/events

# Same as _build-test-simulation in ci/Dockerfile.simtest, whose context has no
# .git, so COMMIT is empty there.
_build-native-simtest: COMMIT=
_build-native-simtest: TAG=mocknet
_build-native-simtest:
	@cd test/simulation && \
		go build -ldflags '$(ldflags)' -tags mocknet -o $(NATIVE_SIM_DIR)/simtest ./cmd

# XMR signer services of the cluster, which run a released image.
MOCKNET_CLUSTER_XMR_SIGNER_SERVICES := xmr-frost-signer xmr-frost-signer-cat \
	xmr-frost-signer-fox xmr-frost-signer-frog xmr-frost-signer-pig xmr-sim-helper

# Images that do not need the Go binaries. The runtime image (apt and pip in an
# empty dind), the monero image and the signer pulls took ~1.5 min after the Go
# build; build-mocknet-cluster-native runs them alongside it, and each other.
_build-native-base-images:
	@DOCKER_BUILDKIT=1 docker build build/docker -f build/docker/Dockerfile \
		--target runtime -t thornode-runtime:mocknet --build-arg DOCKER_MIRROR & runtime_pid=$$!; \
	docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster build monero-daemon & monero_pid=$$!; \
	docker compose $(DOCKER_COMPOSE_FILES) --profile mocknet-cluster \
		pull $(MOCKNET_CLUSTER_XMR_SIGNER_SERVICES); pull_rc=$$?; \
	wait $$runtime_pid; runtime_rc=$$?; wait $$monero_pid; monero_rc=$$?; \
	[ $$pull_rc -eq 0 ] && [ $$runtime_rc -eq 0 ] && [ $$monero_rc -eq 0 ]

build-mocknet-cluster-native:
	@rm -rf $(NATIVE_SIM_DIR) && mkdir -p $(NATIVE_SIM_DIR)
	@$(MAKE) --no-print-directory _build-native-base-images & images_pid=$$!; \
	$(MAKE) --no-print-directory -j2 _build-native-thornode _build-native-simtest; build_rc=$$?; \
	wait $$images_pid; images_rc=$$?; \
	[ $$build_rc -eq 0 ] && [ $$images_rc -eq 0 ]
	@cp $$(go list -m -f '{{.Dir}}' github.com/CosmWasm/wasmvm/v2)/internal/api/libwasmvm.*.so $(NATIVE_SIM_DIR)/
	@chmod u+w $(NATIVE_SIM_DIR)/libwasmvm.*.so
	@cp -r build/scripts $(NATIVE_SIM_DIR)/scripts
	@pids=; for t in thornode:thornode:mocknet events:docker-events simtest:thornode-simtest-native; do \
		DOCKER_BUILDKIT=1 docker build $(NATIVE_SIM_DIR) -f build/docker/Dockerfile.native \
			--target $${t%%:*} -t $${t#*:} & pids="$$pids $$!"; \
	done; rc=0; for p in $$pids; do wait $$p || rc=1; done; exit $$rc

# Base images of the runtime and monero builds, read from their Dockerfiles.
NATIVE_SIM_BASE_IMAGES = \
	$(shell sed -n 's/^FROM \$${DOCKER_MIRROR}\(\S*\) AS runtime$$/\1/p' build/docker/Dockerfile) \
	$(shell sed -n 's/^FROM \$${DOCKER_MIRROR}\(\S*\)$$/\1/p' build/docker/mocknet/monero/Dockerfile)

# Pulled before the L1 chains start, so they do not share bandwidth with ~16 L1
# image pulls: the debian base took ~60s on the critical path that way, against
# ~4s on its own. A failed pull is left to the build, which pulls it again.
_pull-native-base-images:
	@for ref in $(NATIVE_SIM_BASE_IMAGES); do \
		docker pull -q "$${DOCKER_MIRROR}$$ref" >/dev/null & \
	done; wait; true

# prepare-mocknet-cluster with the native build, and without midgard.
prepare-mocknet-cluster-native: stop-mocknet-cluster
	@$(MAKE) --no-print-directory _pull-native-base-images
	@$(MAKE) --no-print-directory run-mocknet-cluster-l1 & l1_pid=$$!; \
	$(MAKE) --no-print-directory build-mocknet-cluster-native; build_rc=$$?; \
	wait $$l1_pid; l1_rc=$$?; \
	[ $$build_rc -eq 0 ] && [ $$l1_rc -eq 0 ]
	@$(MAKE) --no-print-directory run-mocknet-cluster MOCKNET_CLUSTER_MIDGARD=

# test-simulation-cluster for CI: same stages, PARALLELISM and environment. The
# mocknet startup unit tests are left to CI's test-mocknet-scripts job, which
# runs the same suite; the XMR readiness config test still runs here.
test-simulation-cluster-native: test-xmr-readiness-config prepare-mocknet-cluster-native
	@STAGES=$(TEST_SIMULATION_CLUSTER_STAGES) docker run --rm ${DOCKER_TTY_ARGS} \
		-e PARALLELISM=$(PARALLELISM) -e STAGES \
		-e XMR_POST_CHURN_REQUIRED=$(TEST_SIMULATION_REQUIRE_XMR) \
		-e RANDOM_SEED_HEX \
		-e SIMULATION_PPROF_ADDRESS \
		-e XMR_SIM_HELPER_ENDPOINT -e XMR_SIM_HELPER_AUTH_BEARER \
		-e XMR_FROST_ENDPOINT -e XMR_FROST_AUTH_BEARER \
		--network host \
		thornode-simtest-native

# ------------------------------ Test Sync ------------------------------

test-sync-mainnet:
	@./scripts/test-sync.sh mainnet

test-sync-stagenet:
	@./scripts/test-sync.sh stagenet

# ------------------------------ Docker Build ------------------------------

docker-gitlab-login:
	docker login -u ${CI_REGISTRY_USER} -p ${CI_REGISTRY_PASSWORD} ${CI_REGISTRY}

docker-gitlab-push:
	./build/docker/semver_tags.sh registry.gitlab.com/thorchain/thornode ${BRANCH} $(shell cat version) \
		| xargs -n1 | grep registry | xargs -n1 docker push
	docker push registry.gitlab.com/thorchain/thornode:${GITREF}

docker-gitlab-build:
	DOCKER_BUILDKIT=1 docker build . \
		-f build/docker/Dockerfile \
		$(shell sh ./build/docker/semver_tags.sh registry.gitlab.com/thorchain/thornode ${BRANCH} $(shell cat version)) \
		-t registry.gitlab.com/thorchain/thornode:${GITREF} \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg TAG=$(BUILDTAG) \
		--build-arg DOCKER_MIRROR \
		$(DOCKER_BUILD_SECRETS)

########################################################################################
# Tools
########################################################################################

thorscan-build:
	@docker build tools/thorscan -f tools/thorscan/Dockerfile \
		$(shell sh ./build/docker/semver_tags.sh registry.gitlab.com/thorchain/thornode thorscan-${BRANCH} $(shell cat version))

thorscan-gitlab-push: docker-gitlab-login
	@./build/docker/semver_tags.sh registry.gitlab.com/thorchain/thornode thorscan-${BRANCH} $(shell cat version) \
		| xargs -n1 | grep registry | xargs -n1 docker push

events-build:
	@DOCKER_BUILDKIT=1 docker build . -f tools/events/Dockerfile \
		$(shell sh ./build/docker/semver_tags.sh registry.gitlab.com/thorchain/thornode events-${BRANCH} $(shell cat version)) \
		--build-arg TAG=$(BUILDTAG) \
		--build-arg DOCKER_MIRROR \
		$(DOCKER_BUILD_SECRETS)

events-gitlab-push: docker-gitlab-login
	@./build/docker/semver_tags.sh registry.gitlab.com/thorchain/thornode events-${BRANCH} $(shell cat version) \
		| xargs -n1 | grep registry | xargs -n1 docker push
