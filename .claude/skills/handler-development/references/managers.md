# Managers

All in `x/thorchain/`. `managers.go` defines the interfaces, the `Mgrs` container and the `GetXManager` factories; logic lives in `manager_<name>_current.go` (the `_current` suffix is historical - there are no other versions). Files without the suffix (`manager_gas.go`, `manager_network.go`, `manager_slasher.go`, `manager_swap_queue.go`, `manager_validator.go`) hold shared helpers.

## Container and lifecycle

- `Manager` interface: `GetConstants`, `GetVersion`, `Keeper`, `GasMgr`, `EventMgr`, `TxOutStore`, `NetworkMgr`, `ValidatorMgr`, `ObMgr`, `PoolMgr`, `SwapQ`, `AdvSwapQueueMgr`, `Slasher`, `TradeAccountManager`, `SecuredAssetManager`, `StableReserveManager`, `WasmManager`, `SwitchManager`, `ScheduledMigrationManager`, `DynamicFeeMgr`.
- `OracleManager()` and `VolumeManager()` exist only on `*Mgrs`, not on `Manager`; `module.go` holds a `*Mgrs` so it can call them, handlers cannot.
- `LoadManagerIfNecessary` (called at the top of `BeginBlock` and by migrations) reads `GetLowestActiveVersion`; if it changed, `recreateManagers` rebuilds everything. It builds the full set into locals and swaps them in under `mu` so concurrent gRPC queries never see a half-built set and a construction error keeps the old set. `loadMu` serializes the loader itself.
- `recreateManagers` also replaces `mgr.K` via `GetKeeper(v, ...)`, so after load `mgr.Keeper().GetVersion()` is the lowest active node version - but nothing should branch on it.
- Every `GetXManager(_ semver.Version, ...)` factory ignores its version and calls one constructor (`newGasMgr`, `newTxOutStorage`, `newSwapQueueAdv`, `newDynamicFeeMgr`, ...). There is no version switch and no `VCUR` naming.

## Block hooks (`module.go`)

`BeginBlock`: `LoadManagerIfNecessary`; panic if the network version's major/minor exceeds `constants.SWVersion` ("Unsupported Version: update your binary"); then `OracleManager`, `GasMgr`, `NetworkMgr`, `Slasher`, `ValidatorMgr` `BeginBlock`.

`EndBlock` order: `SwapQ`, `AdvSwapQueueMgr`, `PoolMgr`, `ObMgr`, `StableReserveManager`, `NetworkMgr`, `ValidatorMgr`, `TxOutStore`, `GasMgr`, `VolumeManager`, `DynamicFeeMgr`, `ScheduledMigrationManager`.

- Order is consensus. Inserting a hook or moving one changes state on every node; outbounds created after `TxOutStore().EndBlock` wait a block.
- Manager errors in `EndBlock` are logged (plus a telemetry counter) and the block continues. There is no cache context around each call, so writes made before the error stay. A manager that must be all-or-nothing wraps its own work in `ctx.CacheContext()`.
- A panic here halts the chain. Return errors.
- Both swap queues' `EndBlock` run; mainnet uses the advanced queue (`manager_adv_swap_queue_current.go`). The legacy `SwapQ` (`manager_swap_queue_current.go`) is being removed in !5084 - do not add features to it.

## Adding a method to an existing manager

1. Add it to the interface in `managers.go`.
2. Implement it in `manager_<name>_current.go`.
3. Update every other implementer or the build breaks: dummy doubles (`manager_*_dummy_test.go`, plus non-test `manager_switch_dummy.go` and `manager_wasm_dummy.go`) and any test-local fakes (`grep -rn 'func (.*) <ExistingMethod>(' x/thorchain`).

## Adding a new manager

| Step | Where                                                                                                                     |
| ---- | ------------------------------------------------------------------------------------------------------------------------- |
| 1    | `type XManager interface` in `managers.go`                                                                                |
| 2    | `manager_x_current.go`: struct + `newXMgr(...)` constructor                                                               |
| 3    | `Mgrs` field and `func (mgr *Mgrs) XMgr()` accessor (lock `mu` for reading like the others)                               |
| 4    | `GetXManager(_ semver.Version, ...)` factory next to the others                                                           |
| 5    | Build it in `recreateManagers` and assign it in the swap block                                                            |
| 6    | Add to the `Manager` interface only if handlers need it; then `DummyMgr` in `managers_dummy_test.go` needs the method too |
| 7    | Hook into `BeginBlock`/`EndBlock` in `module.go` if it has per-block work (mind the order)                                |
| 8    | `manager_x_current_test.go` gocheck suite                                                                                 |

Template: `manager_dynamic_fee_current.go` (recent, small, keeper-only, `EndBlock` hook).

## Test doubles

- `NewDummyMgr()` / `NewDummyMgrWithKeeper(k)` (`managers_dummy_test.go`) return a `*DummyMgr` with dummy gas, event, txout, network, validator, observer, pool, slasher, trade, wasm, switch, adv-swap-queue and scheduled-migration managers (secured-asset and stable-reserve are real, built on the keeper).
- `setupManagerForTest(c)` (`handler_test.go`) returns a real `*Mgrs` over a test keeper - use it when the manager interaction itself is under test.
- Override one manager in a `DummyMgr` by assigning its field before calling the handler.
