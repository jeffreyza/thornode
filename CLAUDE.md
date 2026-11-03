# CLAUDE.md

Guidance for Claude Code in THORNode. Keep this short; details live in `docs/` and `.claude/skills/`.

## What this is

THORChain node: a Cosmos SDK app (`x/thorchain`) plus Bifrost (`bifrost/`), the observer/signer bridge to external chains.
`x/`, `common/`, and `constants/` are consensus-critical: no map-order iteration, floats, wall-clock, or randomness there. `make lint` runs `tools/analyze` to catch these.
Consensus also covers `gas_used`: it feeds `LastResultsHash` even under an infinite gas meter, so changing KV reads/writes inside a tx is consensus-visible.

Layout:

- `x/thorchain`: handlers, managers, `memo/` parsing, `keeper/` (interface in `keeper/keeper.go`, store in `keeper/v1/`), `ebifrost/` (enshrined Bifrost: observations injected into block proposals). `x/denom` and `x/scheduler` are small extra modules.
- `bifrost/`: `observer/`, `signer/`, `blockscanner/`, `tss/`, and per-chain clients in `bifrost/pkg/chainclients/` (utxo, evm/ethereum, gaia, solana, tron, xrp, monero).
- `app/` wires the SDK app; `app-layer/` is CosmWasm contract support; `common/` holds shared types (assets, chains, addresses, `cosmos.Uint` math); `cmd/` builds `thornode` and `bifrost`.
- `test/regression/` (YAML block-by-block suites), `test/simulation/` (multi-chain mocknet), `tools/` (analyzers, perf, thorscan), `build/` and `ci/` (Docker, CI images).

## Commands

| Task             | Command                                                                                     |
| ---------------- | ------------------------------------------------------------------------------------------- |
| Build            | `make build`                                                                                |
| Unit tests       | `make test` (adds `-tags=mocknet`; also runs network-tagged tests for `common/` and utxo)   |
| One package      | `make test ./x/thorchain` or `RUN=TestName make test ./bifrost/...`                         |
| Direct `go test` | `go test -tags=mocknet ./x/thorchain/...` (without the tag ~30 tests fail spuriously)       |
| gocheck suites   | `go test -tags=mocknet ./x/thorchain -run TestPackage -check.f HandlerSwapSuite`            |
| Lint             | `make lint` (gofumpt, handler/token lints, determinism analyzer, trunk vs `origin/develop`) |
| Format           | `make format` (gofumpt, not goimports)                                                      |
| Codegen          | `make generate` after editing `.proto`, `openapi/openapi.yaml`, or `//go:generate` sources  |
| Proto format     | `make proto-format`                                                                         |
| Regression tests | `make test-regression RUN=<suite-or-test>` (Docker; suites in `test/regression/suites/`)    |
| Simulation tests | `make test-simulation` (Docker mocknet; `TEST_SIMULATION_STAGES=...` to subset)             |
| Mocknet          | `make run-mocknet`, `make logs-mocknet`, `make reset-mocknet`, `make cli-mocknet`           |

The Go toolchain is pinned in `go.mod` and `.go-version` (go1.26.10); if the system Go differs, `go env -w GOTOOLCHAIN=go1.26.10`.
Go 1.27 corrupts local regression exports (a sonic warning lands in the JSON), so stay on 1.26.x.
GNU coreutils are required (`scripts/check-env.sh` runs on every `make`).

`make lint` and CI's `make lint-ci` both run `scripts/lint.sh` (gofumpt, determinism analyzer, version check) over the whole tree. `make lint` runs trunk only on your diff against `origin/develop`, while `make lint-ci` runs trunk over the whole tree and golangci-lint as its own process (`scripts/golangci-ci.sh`).
A clean `make lint` therefore does not guarantee a green CI lint job. Traps it can miss: codespell rejects unittest's membership assertion (write `assertTrue(x in y)`), and markdownlint MD049 wants one emphasis style per file.

## Never edit generated files

`*.pb.go`, `*.pb.gw.go`, `*.pulsar.go`, `*_gen.go`, `openapi/gen/`, `api/`, and `test/regression/mnt/exports/` (regenerate exports with `EXPORT=1`).
Regenerate everything else with `make generate`.

## Conventions

- Handlers live in `x/thorchain/handler_*.go` with `validate`/`handle` methods; handler version switching was removed, so add behavior changes in place.
- Handler `Run` is already wrapped in `CacheContext` (`x/thorchain/handler.go`); do not add another inside handler methods.
- Managers are `x/thorchain/manager_*_current.go`, constructed in `x/thorchain/managers.go`.
- A new keeper method goes on the `Keeper` interface (`x/thorchain/keeper/keeper.go`), its implementation in `keeper/v1/`, and a stub in `keeper/keeper_dummy.go`.
- Constants live in `constants/constants_v1.go` plus `_mainnet`/`_stagenet`/`_mocknet`/`_chainnet` overrides. Runtime-tunable values are Mimir keys (`docs/mimir.md`), read with `Keeper().GetConfigInt64(ctx, constants.X)`, which falls back to the constant when no Mimir is set. Gate new behavior this way, default off.
- Files suffixed `_mainnet.go`/`_mocknet.go` (and `_stagenet`/`_chainnet`) are build-tag variants of each other; change every variant.
- The `version` file must match `info.version` in `openapi/openapi.yaml` (lint enforces it). Store migrations are `Migrator.MigrateNtoN+1` methods in `x/thorchain/migrations*.go` (larger bodies in `migration_*.go`), registered with `RegisterMigration` in `x/thorchain/module.go`; upgrade handlers are in `app/upgrades/`. See `docs/release.md` for when to bump.
- Querier int64 fields serialize as JSON strings; quote them in regression-test asserts.
- A new `repeated` field on a genesis-exported proto type needs `(gogoproto.jsontag) = "name,omitempty"`, or every regression export fixture changes.
- `bifrost/tss/go-tss` is vendored with no separate `go.mod`; dependency sweeps must include it.
- `test/simulation` is a separate Go module; build and lint it on its own. The simulation job is flaky, so check retried jobs before blaming a branch.
- When rebasing onto `develop`, proto descriptor blobs are usually the only real conflict: take theirs and run `make generate` once at the tip.

## GitLab

The remote is GitLab: use `glab`, not `gh`. MRs target `develop` and need three dev-team approvals.
Validate `.gitlab-ci.yml` edits with `glab ci lint`. Keep the MR title and description in sync with the diff (`glab mr update`).

## Where to look

- `REVIEW.md` is the review checklist (risk level, MR description vs diff, 80% changed-line coverage, regression happy/unhappy paths, scope, docs, spec). Apply it when reviewing or preparing an MR.

- `docs/SUMMARY.md` is the index: `docs/architecture/` (ADRs), `docs/concepts/` (protocol mechanics), `docs/bifrost/`, `docs/chain-clients/`, `docs/new-chains/`.
- `test/regression/README.md` documents the YAML test DSL; `test/simulation/` is the actor/watcher DAG harness.
- Skills in `.claude/skills/`: blockchain-code-review, chain-client, health, perf-iterate, tc-debug (shared conventions in `.claude/skills/shared/`).
