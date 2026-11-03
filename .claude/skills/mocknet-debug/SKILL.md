---
name: mocknet-debug
description: Run, inspect, and troubleshoot the local Docker mocknet (THORNode + Bifrost + L1 daemons + Midgard). Covers make targets, single-node vs 5-node cluster, ports and endpoints, the mocknet CLI and keys, env-driven constant and mimir overrides, and why blocks, observations, or churns are not happening. Use whenever the user runs make run-mocknet or a *-mocknet target, or asks why localhost:1317/26657/6040 is not responding or progressing, even if they do not say "mocknet". For simulation runs use simulation-test; for adding a chain daemon use chain-client.
allowed-tools: Read, Grep, Glob, Bash
---

# Mocknet Debug

Mocknet is the Docker Compose network in `build/docker/docker-compose.yml`, driven by `Makefile` targets. THORNode is built with the `mocknet` tag, which enables env overrides that do not exist on mainnet. Background on profiles and keys: `build/docker/README.md`.

## Related skills

| Skill             | When to use                                                                              |
| ----------------- | ---------------------------------------------------------------------------------------- |
| `simulation-test` | Running or writing `make test-simulation*` stages against mocknet                        |
| `regression-test` | Deterministic YAML suites; no Docker mocknet involved                                    |
| `chain-client`    | Adding an L1 daemon or Bifrost chain config: `chain-client/references/docker-mocknet.md` |
| `tc-debug`        | Debugging a real mainnet/stagenet transaction                                            |

## Decision tree

| Intent                             | Go to     |
| ---------------------------------- | --------- |
| Start, stop, reset, rebuild        | Section 1 |
| Which port / endpoint answers what | Section 2 |
| Send a tx or set a mimir by hand   | Section 3 |
| Change a constant or mimir default | Section 4 |
| Something is stuck                 | Section 5 |

## 1. Lifecycle

Always go through `make`: on macOS the Makefile adds `build/docker/docker-compose.darwin.yml` via `DOCKER_COMPOSE_FILES`, which a bare `docker compose -f build/docker/docker-compose.yml` misses.

| Target                                                                      | What it does                                                                   |
| --------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| `make build-mocknet`                                                        | Build images for the `mocknet` profile                                         |
| `make run-mocknet`                                                          | `up -d` with profiles `mocknet` + `midgard`                                    |
| `make halt-mocknet`                                                         | `down`, keeps volumes (resume later)                                           |
| `make stop-mocknet`                                                         | `down -v`, wipes chain state                                                   |
| `make reset-mocknet`                                                        | `stop-mocknet` + `run-mocknet` (no rebuild)                                    |
| `make ps-mocknet`                                                           | Images and container status                                                    |
| `make logs-mocknet`                                                         | Follow `thornode` and `bifrost` logs only                                      |
| `make cli-mocknet`                                                          | Shell in the `cli` container                                                   |
| `make bootstrap-mocknet`                                                    | Runs sim stages `seed,bootstrap` (pools + LPs) in the `thornode-simtest` image |
| `make reset-mocknet-fork-<chain>`                                           | `stop-mocknet`, then `tools/evm/run-mocknet-fork.sh <chain>`                   |
| `make reset-mocknet-cluster`                                                | 5-node cluster (`mocknet-cluster` profile): stop, build, run                   |
| `make halt-mocknet-cluster` / `stop-mocknet-cluster` / `ps-mocknet-cluster` | Cluster equivalents                                                            |

Steps for a clean start after code changes:

1. `make build-mocknet` (the `thornode:mocknet` image is local-only and never pulled).
2. `make reset-mocknet`.
3. `make build-test-simulation` once, then `make bootstrap-mocknet` if you need pools. `bootstrap-mocknet` has no build dependency, so on a fresh checkout it fails until the `thornode-simtest` image exists.

Other services' logs: `docker logs -f docker-<service>-1` (project name is `docker`, e.g. `docker-bitcoin-1`, `docker-ethereum-1`).

## 2. Ports and endpoints

| Port    | Service         | Notes                                                                                                          |
| ------- | --------------- | -------------------------------------------------------------------------------------------------------------- |
| `1317`  | THORNode API    | `/thorchain/...` per `openapi/openapi.yaml`                                                                    |
| `26657` | CometBFT RPC    | `/status`, `/block`, `/health` (compose healthcheck)                                                           |
| `6060`  | THORNode pprof  |                                                                                                                |
| `9090`  | THORNode gRPC   |                                                                                                                |
| `50051` | eBifrost gRPC   | `THOR_COSMOS_EBIFROST_ADDRESS`                                                                                 |
| `6040`  | Bifrost health  | `/ping /p2pid /status/p2p /status/scanner /status/provider /status/signing /version` (`cmd/bifrost/health.go`) |
| `9000`  | Bifrost metrics | Prometheus `/metrics`                                                                                          |
| `5040`  | Bifrost TSS p2p |                                                                                                                |
| `8080`  | Midgard         | `midgard` profile                                                                                              |

There is no Bifrost `/health`. Cluster Bifrosts (cat, fox, pig, frog) map host `6041`-`6044` to their `6040`; cluster validator THORNodes expose no host ports. L1 daemon ports: `chain-client/references/docker-mocknet.md`.

Useful first queries: `/thorchain/lastblock` (per-chain observed heights), `/thorchain/inbound_addresses` (halted flags), `/thorchain/mimir`, `/thorchain/tx/status/<hash>`, Bifrost `/status/scanner`.

## 3. CLI and keys

`make cli-mocknet` sources `build/docker/cli/bashrc`, which sets:

```text
TX_FLAGS='--node http://thornode:26657 --keyring-backend file --keyring-dir /cli/keys --chain-id thorchain'
```

- Chain ID is `thorchain` (compose `CHAIN_ID`), keyring passphrase is `password`, keys are `cat dog fox pig`. `dog` is the only validator, so its node-mimir vote alone takes effect (mocknet `OperationalVotesMin` 1; one node is a supermajority). There is no admin mimir.
- Addresses use the `tthor` prefix. Mnemonics are in `build/docker/README.md`.
- Tx commands live in `x/thorchain/client/cli/tx.go`: `deposit`, `send`, `mimir`, `pause-chain`, `resume-chain`, `ban`, `set-ip-address`, `set-version`, `set-node-keys`, `propose-upgrade`, `approve-upgrade`, `reject-upgrade`, `observe-tx-ins`, `observe-tx-outs`. Example: `thornode tx thorchain mimir HaltChurning 0 --from dog $TX_FLAGS`.

## 4. Env overrides (mocknet builds only)

- **Constants**: `constants/constants_mocknet.go` hard-codes mocknet values (e.g. `ChurnInterval` 60, `JailTimeKeysign` 10, `AsgardSize` 6, `EnableAdvSwapQueue` 1, `OperationalVotesMin` 1) and lets any constant be overridden by its `CamelToSnakeUpper` env name (`ChurnInterval` -> `CHURN_INTERVAL`). Read the file for the current list; do not trust a copied table.
- **Mimir defaults**: `x/thorchain/keeper/v1/keeper_mimir_mocknet.go` falls back to the env var `CamelToSnakeUpper(key)` when a mimir is unset. Compose sets `HALT_CHURNING: 1`, `ENABLE_MONOVAULT_KEYGEN: 1`, `EVM_ALLOWANCE_CHECK_*: 1`, so mocknet does not churn until a mimir sets `HaltChurning` to 0 (the sim churn stages do this).
- **Timing**: `BLOCK_TIME` (default `1s`) feeds `THOR_BLOCK_TIME`; `JAIL_TIME_KEYSIGN` (default 60) must match between THORNode and Bifrost (both read the shared anchor). Set them in the shell before `make run-mocknet`.

## 5. Troubleshooting

1. **Containers not up**: `make ps-mocknet`. THORNode's healthcheck allows a 60s `start_period`; cluster validators wait for it (`depends_on: service_healthy`). `run-mocknet-cluster` runs `up -d` a second time for that reason.
2. **No blocks**: `curl localhost:26657/status | jq .result.sync_info`; then `make logs-mocknet`. A stale volume from an older build is the common cause; `make stop-mocknet` (wipes) then `make run-mocknet`.
3. **Inbound not observed**: compare `/thorchain/lastblock` and Bifrost `/status/scanner` with the L1 daemon's height (`docker logs docker-<chain>-1`). Check `/thorchain/inbound_addresses` for `halted`/`chain_trading_paused` and `/thorchain/mimir` for `HALT*` keys.
4. **Outbound stuck**: `/status/signing` on 6040, then `/thorchain/queue/outbound`. Single-node keysign has no peers, so blame and jail paths need the cluster.
5. **No churn**: expected; see `HALT_CHURNING` in Section 4.
6. **macOS Solana**: the darwin override builds a local `solana-mocknet-2.3.8` image; first run is slow.
