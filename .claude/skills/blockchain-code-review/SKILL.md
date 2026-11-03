---
name: blockchain-code-review
description: Consensus-safety review of THORNode Go changes - non-determinism (map iteration, floats, wall-clock time, goroutines), panics in BeginBlock/EndBlock, division by zero, protobuf and WASM query compatibility. Use this whenever reviewing a diff, branch, or merge request that touches x/thorchain, app, common, constants, or .proto files, even if the user only asks for a "review" or "second look" and does not mention consensus. Not for bifrost-only changes - use chain-client for those.
allowed-tools: Read, Grep, Glob, Bash
---

# Consensus-Safety Review

THORNode is a Cosmos SDK chain: every validator must compute byte-identical state from the same block. Anything that can differ between two correct nodes is a consensus failure, and any panic reached in `BeginBlock`/`EndBlock` halts the chain. This skill is the pattern catalogue for that review. The `/review-mr` command owns the workflow around it (fetching the MR, CI, posting comments); when invoked directly, get the diff with `git diff origin/develop...HEAD` or `glab mr diff <id>`.

## Where consensus code lives

Only code reachable from a message handler, keeper, manager, or `BeginBlock`/`EndBlock` is consensus-critical. That is `x/thorchain/`, `app/`, `common/`, `constants/`, and `proto/`. Bifrost and CLI run off-chain, so the same patterns there are ordinary bugs, not consensus bugs. Queriers (`querier_*.go`) are read-only and mostly off-chain — with one exception: the routes listed in `wasmAcceptedQueries` (`app/wasm.go`) are registered on the same gRPC query router (`x/thorchain/module.go`) and run inside a transaction when a WASM contract calls them, so their implementations are fully consensus-critical. Decide which side each hunk is on before rating it.

## Patterns

Grep cannot find most of these. `for k, v := range x` does not say whether `x` is a map, and `a / b` does not say whether `b` can be zero. Read every changed loop, division, and cast in consensus code and check the operand types yourself.

**Map iteration (CRITICAL).** Go randomises map order. Iterating a map to mutate state, emit events, or build a slice that is later used in order is non-deterministic. Safe uses: collecting keys to sort, counting, clearing. Fix by sorting keys first, or by using a slice or KVStore iterator, which are ordered.

**Floating point (CRITICAL).** `float32`/`float64`, `math.*` on floats, `strconv.ParseFloat`, and `json` decoding of numbers into `float64` can round differently per platform. Use `cosmos.Uint`, `sdkmath.Int`, or `sdkmath.LegacyDec`. In bifrost, external APIs often return floats: convert each one to an integer before any arithmetic.

**Wall-clock time and randomness (CRITICAL).** `time.Now()`, `time.Since()`, `rand.*`, and `uuid.New()` differ per node. Use `ctx.BlockTime()`, `ctx.BlockHeight()`, or a hash of block data.

**Goroutines and shared mutable state (CRITICAL).** Scheduling order is not deterministic. Any `go func`, channel, or `sync` primitive in a handler, keeper, or manager needs a specific justification.

**Panics in block lifecycle (HIGH).** Slice indexing, type assertions without `ok`, nil dereferences, `MustXxx` helpers, and unchecked `Quo`/`Div` on a zero divisor all panic. A panic in a handler fails one tx; the same panic in `BeginBlock`/`EndBlock` or a manager called from them halts the chain. Check that divisors are guarded and that managers recover or skip bad items rather than propagate.

**Integer arithmetic (HIGH).** Raw `int64`/`uint64` multiplication overflows silently, unsigned subtraction wraps, and narrowing casts truncate. Prefer `cosmos.Uint` arithmetic; when raw ints are used, look for the guard.

**Version activation and migrations (HIGH).** A consensus-breaking release activates at the height of a scheduled upgrade plan: nodes vote it through `MsgProposeUpgrade`, `scheduleUpgradeIfNecessary` (`x/thorchain/handler_upgrade.go`) calls `ScheduleUpgrade` at the proposal's height, and every node halts and restarts on the new binary there. Non-consensus-breaking patches roll out without a proposal and are listed in `app/upgrades.go`. Because the switch is a coordinated restart, behaviour changes ship ungated, so question any new version comparison. If one is genuinely needed it must read the manager's version (`mgr.GetVersion()`, the lowest active node version); `app.ThorchainKeeper` is built by `NewKeeper` with a hard-coded `0.0.0` (`x/thorchain/keeper/v1/keeper.go`), so a gate on it never activates. What does need care is state: a change to how something is stored needs a store migration (`x/thorchain/migrations*.go`, bumping `ConsensusVersion()` in `module.go`), or nodes will misread data written before the upgrade height.

**Protobuf (MEDIUM).** Changing a field's number or type, or removing a field, breaks decoding of existing state. New fields must use fresh numbers. Generated files must be regenerated in the same MR (`make proto-gen`, or `make generate` for everything).

**WASM queries (MEDIUM).** Contracts on-chain decode the responses listed in `wasmAcceptedQueries` in `app/wasm.go`. Read that map in the diff's base commit, then check whether any response type it names had fields added, removed, renamed, or retyped. Adding or removing an entry is itself a consensus change.

**External input (MEDIUM).** `http.*`, `os.Getenv`, and file reads inside consensus code return different values per node. Startup config is fine; anything on a tx or block path is not.

**Tests (LOW).** Consensus changes normally need a regression suite under `test/regression/suites/`, and behaviour changes need a unit test that would have failed before the change.

## Report

Rate each finding CRITICAL / HIGH / MEDIUM / LOW using the tiers above, with `file.go:line`, what can differ between nodes or panic, and the fix. State explicitly which changed files you judged to be outside consensus and why. If nothing was found, say which patterns you checked rather than "looks good".
