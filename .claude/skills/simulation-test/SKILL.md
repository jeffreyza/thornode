---
name: simulation-test
description: Run, debug, and write THORNode simulation tests (test/simulation, its own Go module) that drive L1 daemons, Bifrost, and THORNode on mocknet via a randomized actor DAG. Covers make test-simulation and cluster targets, TEST_SIMULATION_STAGES, stage order, RANDOM_SEED_HEX replay, watchers, and adding actors or stages. Use whenever the user mentions simulation, simtest, a sim stage name (swaps, cluster-churn, ragnarok, memoless-refunds...), or a flaky simulation CI job, even if they do not say "simulation". For deterministic state-machine tests use regression-test; for mocknet itself use mocknet-debug.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# Simulation Tests

`test/simulation` is its own Go module (root `go test ./...` skips it). The binary (`cmd/main.go`, built `-tags mocknet` in `ci/Dockerfile.simtest`) runs in a host-network container against a running mocknet. Design background: `test/simulation/README.md`.

## Related skills

| Skill             | When to use                                                                      |
| ----------------- | -------------------------------------------------------------------------------- |
| `mocknet-debug`   | Mocknet lifecycle, ports, CLI, env overrides; inspecting a run in progress       |
| `regression-test` | Deterministic single-node tests without Bifrost                                  |
| `chain-client`    | Adding a new chain to the sim: `chain-client/references/simulation.md`           |
| `tc-debug`        | Live mainnet tx investigation; stage map in `tc-debug/references/tx-pipeline.md` |

## Decision tree

| Intent                            | Go to                                   |
| --------------------------------- | --------------------------------------- |
| Run all or some stages            | Section 1                               |
| A run failed or is flaky          | Section 2                               |
| Add an actor or a stage           | Section 3, `references/actors.md`       |
| Add a new chain                   | `chain-client/references/simulation.md` |
| Unit tests of the sim code itself | Section 4                               |

## 1. Running

```bash
make test-simulation                                         # build-mocknet + reset-mocknet + run default stages
make test-simulation TEST_SIMULATION_STAGES=seed,bootstrap,swaps
make test-simulation-no-reset TEST_SIMULATION_STAGES=...     # reuse running mocknet; rebuilds only thornode-simtest
```

- **Select stages with `TEST_SIMULATION_STAGES`, not `STAGES`.** The recipe runs `STAGES=$(TEST_SIMULATION_STAGES) docker run ...`, so an exported `STAGES` is overwritten. `DEBUG` and `TIMEOUT` are not forwarded at all.
- Forwarded env: `PARALLELISM` (Makefile default 1; binary default 8 when unset), `RANDOM_SEED_HEX`, `SIMULATION_PPROF_ADDRESS` (default `:6061`, `off` disables), `XMR_SIM_HELPER_ENDPOINT`/`_AUTH_BEARER`, `XMR_FROST_ENDPOINT`/`_AUTH_BEARER`.
- Default `TEST_SIMULATION_STAGES`: `seed,bootstrap,xmr-coverage,arb,swaps,memoless-swaps,consolidate,memoless-refunds,solvency,ragnarok`. `all` enables every stage except `xmr-historical-refund` (explicit only). `bond-yield` is an alias for `cluster-churn`.
- **Order comes from `simulationStages()` in `cmd/main.go`, not from the order you list.** Stages are appended to `Root` in slice order, each fanning in on the previous. `seed` has no actor; it funds users in `InitConfig`. `churn` is dropped when `cluster-churn` is selected; `cluster-churn` + `xmr-post-churn` become `suites.ClusterChurnWithXMRPostChurn`.
- `make` always sets `STAGES`. Running the binary directly with `STAGES` unset shows a tview stage picker on a TTY, otherwise runs the `defaultOn` stages. `CI=1` drops `-it` from `docker run` (`DOCKER_TTY_ARGS`).
- Midgard containers are stopped before the run.

Cluster (5 THORNodes, `mocknet-cluster` profile) targets:

| Target                                | Purpose                                                                                                                                                                   |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make test-simulation-cluster`        | `prepare-mocknet-cluster` then `TEST_SIMULATION_CLUSTER_STAGES` (adds churn, XMR, inactive-vault refunds); `TEST_SIMULATION_REQUIRE_XMR=1` sets `XMR_POST_CHURN_REQUIRED` |
| `make test-simulation-xmr-readiness`  | Cluster with `seed,bootstrap,xmr-post-churn`, XMR required                                                                                                                |
| `make test-simulation-keygen-jail`    | Cluster XMR keygen-jail scenario (netem)                                                                                                                                  |
| `make test-simulation-cluster-native` | CI path: binaries built in the job container                                                                                                                              |
| `make bootstrap-mocknet`              | Just `seed,bootstrap` against the running mocknet                                                                                                                         |

Stage-specific setup (e.g. `JAIL_TIME_KEYSIGN=600` for `xmr-historical-refund`) is in `test/simulation/README.md`.

## 2. Debugging a failure

1. **Find the failing actor.** On actor error the DAG logs `actor execution failed` (fatal), dumps that actor's log and the still-running actors. An op returning `Error` without `Finish` is only logged and retried; the real failure is usually the `ErrTimeout` (`actor timed out`, with `op`, `op_elapsed`, `timeout`).
2. **Replay the same schedule.** The run logs ``Random seed set to `<hex>` ``; rerun with `RANDOM_SEED_HEX=<hex>` and the same stages and parallelism. Interleaving still varies with chain timing.
3. **Narrow.** `make test-simulation-no-reset TEST_SIMULATION_STAGES=seed,bootstrap,<stage>` after `make reset-mocknet`. `PARALLELISM=1` serializes actors.
4. **Watchers** (`watchers/`, started in `cmd/main.go`): `Invariants` (10s), `SolvencyHalt`, `SecurityEvents`, `Luvi` (1s). Any watcher error fails the run. Invariants are also checked once before and after the DAG (`runWithInvariantChecks`), and failing to fetch them is a failure, not a skip.
5. **Readiness hangs** before the DAG: the binary waits for Bifrost and for TRON, SOL, ZEC, and EVM RPCs to advance. Check those containers (`mocknet-debug`).
6. **Inspect live state** while it runs: `localhost:1317`, Bifrost `localhost:6040/status/scanner`, pprof on `:6061`.
7. **Tx retries**: `pkg/types/tx_retry.go` (`SignAndBroadcastTx`, `SimTxErrorResult`) stops on `ErrSimTxUncertain`, and ZEC and TRON retry broadcasts with the same signed bytes instead of re-signing, to avoid double spends.
8. **XMR**: `xmr-coverage` only asserts once an XMR inbound appears unless `XMR_POST_CHURN_REQUIRED=1`.

The simulation CI job is known to be flaky; check whether the same stage fails on `develop` before chasing it.

## 3. Adding an actor or stage

1. Find the closest existing actor: `actors/core/` (single flows: swaps, LPs, churn, mimir, ragnarok, solvency), `actors/features/` (feature scenarios: consolidate, memoless refunds, inactive-vault refunds, bond yield, churn interval), `actors/suites/` (compositions per stage).
2. Write the actor following `references/actors.md`: embed `Actor`, append ops, acquire/release users, set `Timeout`.
3. Wire it into an existing suite with `Append` / `Children`, or add a stage: one `simulationStage{name, defaultOn, constructor}` entry in `simulationStages()` at the position it must run. There is no switch to edit. If it should run in CI, add it to `TEST_SIMULATION_STAGES` or `TEST_SIMULATION_CLUSTER_STAGES` in the `Makefile`.
4. Run it in isolation (Section 2, step 3), then with the default stage list.
5. Add unit tests next to the code and run Section 4.

## 4. Unit tests of the sim module

```bash
scripts/test-simulation-unit.sh /tmp/simulation-unit.jsonl
```

Runs every package with `-tags mocknet` and the pinned Go toolchain, rejects unexpected skips, and never touches a live chain. Shuffle/isolation commands are in `test/simulation/README.md`.
