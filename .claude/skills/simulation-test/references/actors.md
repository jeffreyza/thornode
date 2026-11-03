# Actors, ops, and the DAG

Read this when writing or changing an actor, op, suite, or watcher. Types live in `test/simulation/pkg/types/` (`actor.go`, `op.go`, `account.go`, `watcher.go`, `tx_retry.go`); the scheduler is `pkg/dag/dag.go`.

## Types

| Type       | Shape                                                                                                                                                                  |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Op`       | `func(config *OpConfig) OpResult`                                                                                                                                      |
| `OpConfig` | `NodeUsers []*User` (validator keys), `Users []*User` (funded test users). No client map                                                                               |
| `OpResult` | `Continue bool`, `Finish bool`, `Error error`                                                                                                                          |
| `Actor`    | `Name`, `Timeout`, `Interval`, `Ops []Op`, `Children map[*Actor]bool`; `NewActor(name, rng)`                                                                           |
| `User`     | `Thorchain` (`thorclient.ThorchainBridge`), `ChainClients map[common.Chain]LiteChainClient`, `Acquire()`, `Release()`, `IsLocked()`, `PubKey(chain)`, `Address(chain)` |
| `Watcher`  | `Name`, `Interval`, `Fn func(*OpConfig) error`                                                                                                                         |

## Op semantics (`Actor.Execute`)

- Each op is called repeatedly until it returns `Continue: true` (next op) or `Finish: true` (actor ends, returning `Error`).
- `Error` with neither flag is logged and the op is retried after `Interval`. This is the normal "not ready yet" path; do not treat a logged op error as the failure.
- `Timeout` is the budget for the whole actor, measured from its start; exceeding it returns `ErrTimeout` and fails the run.
- `Finish: true` with `Error: nil` ends the actor successfully and skips its remaining ops (use for "nothing to do" cases).

## DAG semantics

- `a.Append(b)` makes `b` a child of every current leaf of `a` (fan-in). `a.Children[b] = true` adds a direct child (fan-out; `InitRoot` wires the parent links, as in `actors/suites/bootstrap.go`). A child runs once all parents are finished or backgrounded.
- `a.Background()` (called from an op) marks the actor backgrounded: its children may start while it keeps running.
- The scheduler ticks every 100ms (`DAG_TICK_INTERVAL` overrides), and starts a random ready actor only when fewer than `PARALLELISM` are running and some `User` is unlocked.
- Any actor returning an error is fatal for the whole run.

## Actor pattern

Copy a real actor; `actors/core/mimir.go` is the smallest. Shape:

```go
type FooActor struct {
	Actor
	account *User
}

func NewFooActor(rng *rand.Rand) *Actor {
	a := &FooActor{Actor: *NewActor("Foo", rng)}
	a.Timeout = 5 * time.Minute
	a.Ops = append(a.Ops, a.acquireUser, a.sendTx, a.verify)
	return &a.Actor
}
```

Rules:

- **Lock users.** `Acquire()` returns false if another actor holds the user; return `OpResult{}` to retry. `Release()` when done (or `defer` inside a single op). Holding a user across ops is normal; release it in the last op and on every `Finish` path.
- **Broadcast through `SignAndBroadcastTx`** (`tx_retry.go`) and map errors with `SimTxErrorResult`. It stops on `ErrSimTxUncertain` instead of re-signing, and ZEC and TRON reuse the signed bytes on broadcast retry. A naive retry loop can double spend.
- **Query THORNode with `pkg/thornode`** (`GetPool`, `GetSwapQuote`, `GetTxStages`, `GetTxDetails`, `GetMimirs`, `GetInboundAddress`, ...). It retries transient GET errors.
- **Assert ranges, not exact values.** Sim state is dynamic; compare against a quote or a balance delta with tolerance.
- **Use the actor's rng** (`NewActor(name, rng)` seeds it from the parent) so `RANDOM_SEED_HEX` replays choices.
- Feature actors that fan out per chain (e.g. `actors/features/consolidate.go` over BTC, BCH, DOGE, LTC, ZEC) build children in the constructor and return the parent.

## Where things go

| Change                            | File                                                                  |
| --------------------------------- | --------------------------------------------------------------------- |
| Single flow actor                 | `actors/core/<name>.go`                                               |
| Feature scenario                  | `actors/features/<name>.go`                                           |
| Stage composition                 | `actors/suites/<stage>.go`                                            |
| New stage                         | `simulationStages()` in `cmd/main.go` (position = run order)          |
| Chains in sim, seed amounts, RPCs | `actors/common/config.go`, `cmd/mocknet.go`, `cmd/main.go`            |
| Watcher                           | `watchers/<name>.go`, registered in the watcher list in `cmd/main.go` |
