---
name: handler-development
description: Add or change THORNode state-machine logic in x/thorchain - message handlers (handler_*.go), the memo-to-msg path, node-signed msgs (msg_server.go, ante.go), and the managers handlers call (managers.go, manager_*_current.go). Use whenever the user wants a new memo or tx type, a new MsgX, a fix inside a handler or manager, a new manager method, or asks how a msg flows from an observed tx to state, even if they do not say "handler". For releases, store migrations and upgrade handlers use protocol-upgrade; for reviewing a diff use blockchain-code-review.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# THORNode Handler and Manager Development

Handlers are the per-msg entry points in `x/thorchain/handler_*.go`; managers (`x/thorchain/managers.go`, `manager_*_current.go`) hold the logic shared by handlers and by `BeginBlock`/`EndBlock` in `x/thorchain/module.go`. Read the closest existing handler before writing a new one; this skill only lists what the code does not make obvious.

## Decision Tree

1. **New memo-driven action** (users send an L1 tx or `MsgDeposit` with a memo) - [Internal msg](#1-internal-msg-memo-driven), then `references/wiring.md`.
2. **New node-signed or user-signed cosmos msg** (Ban, Mimir, Observed\*, Solvency...) - [External msg](#2-external-msg-signed-cosmos-tx), then `references/wiring.md`.
3. **Change behaviour of an existing handler** - [Modify](#3-modify-an-existing-handler).
4. **New manager or manager method, or BeginBlock/EndBlock work** - `references/managers.md`.
5. **Tests** - [Test](#5-test).

## No version gates

develop has no `version.GTE(semver...)` / `GetVersion().LT(...)` behaviour gates anywhere in `x/thorchain` (`grep -rn 'semver.MustParse' x/thorchain --include='*.go' | grep -v _test` finds only `ladder.go`, `module.go`, `querier.go` and keeper plumbing). Behaviour changes ship ungated: a consensus-breaking release activates at a validator-scheduled upgrade height where every node restarts on the new binary (see `protocol-upgrade`). So:

- Edit the handler or manager in place. Do not add `validateV3_x_0` / `handleV3_x_0` variants, a version `switch`, or an `errBadVersion` default case.
- Do not gate on `h.mgr.Keeper().GetVersion()` or `app.ThorchainKeeper`: the latter is built by `NewKeeper` with a hard-coded `0.0.0` (`x/thorchain/keeper/v1/keeper.go:164`), so a gate on it never turns on.
- The `GetXManager(version, ...)` factories in `managers.go` and `constants.GetConstantValues` all ignore `version`.
- The generic `BaseHandler[M]` / `LadderDispatch` (`basehandler.go`, `ladder.go`) has one user, `handler_send.go`, registered once at `"3.0.0"`. It is not the template for new handlers.
- Remaining `GetVersion()` comparisons are about node software versions (churning out old nodes, `manager_validator_current.go`), not feature activation.
- If state written before the upgrade must change shape, that is a store migration (`protocol-upgrade`), not a gate.

## 1. Internal msg (memo-driven)

Flow: bifrost observes an inbound (or a user sends `MsgDeposit`) -> `processOneTxIn` (`x/thorchain/handler.go`) parses the memo and builds the msg -> `NewInternalHandler` looks it up in `getInternalHandlerMapping` and runs it inside a cache context. Callers include `handler_deposit.go`, `handler_observed_txin.go`, `handler_observed_txout.go`, `handler_observed_tx_quorum.go`, the legacy swap queue and several `manager_*_current.go` files.

1. Proto message in `proto/thorchain/v1/types/msg_<name>.proto`; `make proto-gen`.
2. Go side in `x/thorchain/types/msg_<name>.go`: constructor, `ValidateBasic`, `GetSigners`. Template: `types/msg_rotate.go`.
3. Memo: `TxType` const + `stringToTxTypeMap` entry + the relevant predicate lists in `x/thorchain/memo/memo.go`, a `memo_<name>.go` parser, and a case in `memo_parser.go`. Use `memo-parser` for grammar.
4. Handler `x/thorchain/handler_<name>.go`: `NewXHandler(mgr Manager)`, `Run` that type-asserts, then `validate`, then `handle`. Template: `handler_operator_rotate.go` (memo-driven) or `handler_donate.go` (small).
5. Register in `getInternalHandlerMapping` and add the memo -> msg case in `processOneTxIn` (both in `handler.go`).
6. Event in `x/thorchain/types/type_event.go` if Midgard or clients need it.

## 2. External msg (signed cosmos tx)

Flow: tx -> `AnteDecorator.anteHandleMessage` (`x/thorchain/ante.go`) -> `msgServer` method (`x/thorchain/msg_server.go`) -> `externalHandler` -> `handler.Run`. No `NewInternalHandler` wrap here; the SDK discards state of a failed tx.

Steps 1, 2, 4 and 6 above, plus: an `rpc` in `proto/thorchain/v1/types/tx.proto`, a `msgServer` method, a `<Name>AnteHandler` next to the handler (`BanAnteHandler` in `handler_ban.go`) wired into the `anteHandleMessage` switch, and codec registration in `x/thorchain/types/codec.go`. Exact touch-points: `references/wiring.md`.

Node-only msgs authorize in `validate` with `isSignedByActiveNodeAccounts` (`helpers.go`) and in the ante handler; see `handler_ban.go`.

## 3. Modify an existing handler

1. Find the handler from the msg: `grep -n 'MsgX{}' x/thorchain/handler.go x/thorchain/msg_server.go`.
2. Read `validate` and `handle` and the managers they call. Much of the logic lives in managers or `helpers.go`, not the handler file.
3. Change it in place (see [No version gates](#no-version-gates)). If the change alters stored data layout, stop and plan a migration with `protocol-upgrade`.
4. Swaps: new work goes in the advanced queue (`manager_adv_swap_queue_current.go`). The legacy `SwapQ` (`manager_swap_queue_current.go`) is being removed (!5084); do not extend it.

## 4. Invariants and gotchas

- **Cache context.** `NewInternalHandler` runs `h.Run` on `ctx.CacheContext()` and commits only on `err == nil` (`handler.go`). Return an error to roll back. A nested cache context is only for a sub-step whose failure you swallow while continuing (`handler_switch.go`).
- **Errors are consensus.** Error strings end up in tx results and events; changing one is a visible change. Wrap internal errors with `ErrInternal(err, "...")` / `wrapError(ctx, err, "...")` (`errors.go`, `helpers.go`). `externalHandler` re-wraps code-1 errors as `errInternal` and rejects a non-empty `*cosmos.Result`.
- **Determinism.** No map iteration without sorted keys, no floats, no wall-clock time, no goroutines in state code. `scripts/lint.sh` runs `tools/analyze` for map iteration and float comparison. `blockchain-code-review` has the full list.
- **No panics** in anything reachable from `BeginBlock`/`EndBlock`; `externalHandler` recovers panics only for external msgs.
- **`scripts/lint-handlers.bash`** (run by `make lint` via `scripts/lint.sh`) fails if a `validateV<N>`/`handleV<N>` in a `handler_*.go` is defined but not called (or the reverse) - a leftover of the removed versioned-handler pattern.
- **Managers from handlers:** use `h.mgr` accessors (`Keeper()`, `TxOutStore()`, `EventMgr()`...). Do not construct managers in a handler. Outbounds go through `TxOutStore().TryAddTxOutItem` (fails softly) or `UnSafeAddTxOutItem`.
- **Constants and mimir** are read via `h.mgr.Keeper().GetConfigInt64(ctx, constants.X)` (mimir override, then constant). Adding one: `mimir-parameter`.

## 5. Test

- Unit tests are gocheck suites under `TestPackage` (`x/thorchain/thorchain_test.go`) and need the `mocknet` tag:

  ```bash
  go test -tags mocknet ./x/thorchain/ -run TestPackage -check.f 'HandlerOperatorRotateSuite'
  ```

- `setupManagerForTest(c)` (`handler_test.go`) gives a real keeper and `*Mgrs`; `NewDummyMgr()` / `NewDummyMgrWithKeeper(k)` (`managers_dummy_test.go`) plus the `manager_*_dummy_test.go` doubles isolate a handler.
- Cover: wrong msg type returns `errInvalidMessage`; each `validate` rejection; the happy path's state and emitted events; rollback on error.
- Add a regression suite under `test/regression/suites/<area>/` (`regression-test` skill; e.g. `suites/operator-rotate/rotate.yaml`) and run `make test-regression RUN=<suite>`.
- Before pushing: `make format`, `make lint`, `make test`.

## Related Skills

| Skill                    | When to Use                                            |
| ------------------------ | ------------------------------------------------------ |
| `memo-parser`            | Memo grammar, abbreviations, parser helpers            |
| `protocol-upgrade`       | Store migrations, `ConsensusVersion`, upgrade handlers |
| `mimir-parameter`        | Adding a constant or mimir the handler reads           |
| `regression-test`        | Writing the `test/regression/suites/` case             |
| `blockchain-code-review` | Consensus-safety review of the finished change         |

## References

| File                     | Read this when                                                              |
| ------------------------ | --------------------------------------------------------------------------- |
| `references/wiring.md`   | Adding a new msg or memo type: every file and registry to touch, both paths |
| `references/managers.md` | Adding a manager or method, BeginBlock/EndBlock order, manager test doubles |
