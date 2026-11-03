---
name: regression-test
description: Write, run, debug, and update THORNode regression tests (test/regression YAML suites run by make test-regression). Covers the op DSL, templates and genesis merging, jq asserts, invariants and LUVI checks, RUN/DEBUG/EXPORT/AUTO_UPDATE flags, and export/blocks fixture diffs under test/regression/mnt. Use whenever a change touches handler, manager, keeper, or genesis behaviour, a regression suite fails, or export fixtures changed, even if the user does not say "regression". For multi-chain end-to-end flows use simulation-test.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# Regression Tests

A regression suite is a YAML file of ops separated by `---`, run against a real `thornode` binary (built with `-tags mocknet`, version `9.999.0`) with no Bifrost and no L1s. Each file gets its own node, home dir, and port offset. The DSL basics are documented in `test/regression/README.md`; this skill adds the rules and gotchas that are not obvious from it.

## Related skills

| Skill                 | When to use                                                   |
| --------------------- | ------------------------------------------------------------- |
| `simulation-test`     | Dynamic multi-chain flows through Bifrost and real L1 daemons |
| `mocknet-debug`       | Poking a running Docker mocknet by hand                       |
| `handler-development` | Writing the handler the suite is testing                      |

## Decision tree

| Intent                               | Go to                               |
| ------------------------------------ | ----------------------------------- |
| Run one suite / understand a flag    | Section 1                           |
| Write a new suite                    | Section 2, then `references/dsl.md` |
| A suite fails                        | Section 3                           |
| Export or blocks fixture diff        | Section 4                           |
| Op fields, template funcs, mnemonics | `references/dsl.md`                 |

## 1. Running

```bash
make test-regression RUN=swaps/affiliates            # or: RUN=swaps/affiliates make test-regression
make test-regression RUN='mimir,adv-swap-queue/limit' DEBUG=1
```

Make exports command-line vars to the recipe, so both placements work. Env vars are forwarded explicitly in the `test-regression` recipe (`Makefile`); anything else is dropped.

| Var               | Effect (source in `test/regression/cmd/`)                                                                                        |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `RUN`             | Comma-separated regexes matched against the path under `suites/` (`main.go`)                                                     |
| `DEBUG`           | Becomes `THOR_TENDERMINT_LOG_LEVEL`, streams node logs; on failure keeps the node up for curl (`run.go`). Forces `PARALLELISM=1` |
| `PARALLELISM`     | Suites in parallel (default 1); files are sorted by `create-blocks` count, longest first                                         |
| `EXPORT`          | Rewrite `mnt/exports` and `mnt/blocks` fixtures instead of diffing                                                               |
| `AUTO_UPDATE`     | Rewrites failing `==` asserts in the YAML with the observed value, then re-renders later ops                                     |
| `IGNORE_FAILURES` | Keep going past failed asserts to see them all                                                                                   |
| `FAIL_FAST`       | Stop at the first failing suite                                                                                                  |
| `TIME_FACTOR`     | Integer multiplier on block time and the post-block sleep, for slow machines (`util.go`)                                         |
| `CI`              | No TTY; failed suites (and `context deadline exceeded`) retried up to 3 attempts (`test.go`)                                     |
| `UNFILTER_EXPORT` | Keep `app_hash`/`gas_used` etc. in exports                                                                                       |

`make test-regression-coverage` opens the coverage from the last run. Use Go 1.26.x: Go 1.27 corrupts exports.

## 2. Writing a suite

1. Pick a home: `test/regression/suites/<feature>/<name>.yaml`. Copy the closest existing suite; 192 suites cover most features (`ls test/regression/suites`).
2. Start from a template in `test/regression/templates/`. Most suites begin:

   ```text
   {{ template "default-state.yaml" }}
   ---
   {{ template "btc-eth-pool-state.yaml" }}
   ---
   type: create-blocks
   count: 1
   ```

   `5-validators-*` and `6-validators-*` templates replace `default-state.yaml`; do not stack them.

3. Op order is enforced: `env` ops first, then `state` ops, then everything else. `env` sets a process env var for the node: a SNAKE_CASE constant override (`key: TNS_REGISTER_FEE`, via `constants/constants_mocknet.go`), a default for an unset mimir (via `x/thorchain/keeper/v1/keeper_mimir_mocknet.go`), or any other env the node reads.
4. Drive the chain with `tx-*` ops and `create-blocks`; assert with `check` (jq `-e` expressions against `http://localhost:1317/...` or `:26657`).
5. Run with `RUN=`, then commit the generated `mnt/exports/<path>.json` and `mnt/blocks/<path>.json`.

Rules that bite:

- **Unknown fields fail.** Ops decode with `ErrorUnused: true` (`operations.go`); a typo in a field name is an error, not a no-op.
- **Genesis deep-merge** (`run.go`, `deepMerge(..., "address", "denom")`): arrays are merged element-wise only when items share an `address` or `denom` key; every other array (pools, LPs, vaults, node accounts) is **appended**. Redefining a pool from a template adds a duplicate.
- **Integers are strings.** Querier int64/uint64 fields serialize as JSON strings: assert `.balance_rune == "100"`, or `(.balance_rune|tonumber) > 0`.
- **Observations need consensus.** With `default-state.yaml` only `dog` is a validator and the pool templates make the asgard vault from its key, so one `tx-observed-in` from `dog` suffices, with `observed_pub_key: {{ pubkey_dog }}` and `to_address` the dog vault address. Multi-validator templates need a supermajority of signers each sending the same tx.
- **`sequence`** sets the absolute account sequence (`txf.WithSequence` in `sendMsg`). Omit it normally; when one signer sends several txs before a `create-blocks`, give consecutive values starting at the account's current sequence (e.g. `suites/pol/pol.yaml` uses 0, 1).
- **Mocknet constants differ from mainnet**: e.g. `EnableAdvSwapQueue` 1 and `AdvSwapQueueRapidSwapMax` 1, so swaps run through the advanced queue; `OperationalVotesMin` 1, so one validator's `tx-mimir` takes effect. Read `constants/constants_mocknet.go` before reasoning about timings.
- **Invariants run every block** (`/thorchain/invariant/<name>`), plus the LUVI monitor (`luvi.go`: LP value must not fall, LP units change only via add/withdraw). Use `skip_invariants: true` or `luvi_drop: [<pool>]` on `create-blocks` only when the drop is the behaviour under test.
- **Expected panics**: `create-blocks` with `exit: <code>` asserts the node exits; without it any exit fails the suite.
- **Empty `check`** (no endpoint) is a breakpoint; with `DEBUG` the node stays up after a failure for manual curl.

Templated YAML: in Markdown, put `{{ }}` examples in `text` fences; trunk/prettier rewrites them to `{ { } }` inside `yaml` fences.

## 3. Debugging a failure

1. Re-run alone: `make test-regression RUN=<path-regex> DEBUG=1`. The failing op, its line, and the full endpoint response are printed.
2. Add `asserts: ["false"]` (or an empty `check`) after the op you want to inspect; the response is dumped.
3. `404` on a `check` is retried up to 10 times before failing; a persistent 404 is a wrong path or a missing query param.
4. If many `==` values shift from an intended logic change, `AUTO_UPDATE=1`, then review the YAML diff by hand; it does not fix `>`/`<` or computed asserts.
5. Flaky only on slow hardware: `TIME_FACTOR=2`. Flaky in CI: it already retries; a suite that needs retries usually races a query against block commit.
6. Invariant failure: the message names the invariant; find its route in `x/thorchain/keeper/v1/invariants.go` before suppressing it.

## 4. Export and blocks fixtures

- After the last op the harness exports state to `test/regression/mnt/exports/<suite-path>.json` and concatenated block results to `test/regression/mnt/blocks/<suite-path>.json`. A missing fixture is written; an existing one is diffed and a mismatch fails the suite. Orphaned fixtures for deleted suites are removed.
- Export invariants are checked on the exported genesis. A suite whose last op is `type: fail-export-invariants` expects that check to fail.
- Fixture churn across many suites usually means a state/genesis shape change. A new repeated proto field in genesis needs `(gogoproto.jsontag) = "<name>,omitempty"`, or every export changes.
- Regenerate with `EXPORT=1` (scoped by `RUN` when possible) and review the diff; never regenerate to make an unexplained diff disappear.
- Per-block files `mnt/blocks/<path>/NNN.json` written during a run are gitignored scratch.
