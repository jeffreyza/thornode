# x/thorchain Store Migrations

## Where things live

| File                                   | Build tag                            | Holds                                                                                      |
| -------------------------------------- | ------------------------------------ | ------------------------------------------------------------------------------------------ |
| `x/thorchain/module.go`                | none                                 | `ConsensusVersion()`, `RegisterMigration` calls in `RegisterServices`                      |
| `x/thorchain/migrations_common.go`     | none                                 | Steps identical on every network (`Migrate18to19`, `Migrate19to20`, `ClearObsoleteMimirs`) |
| `x/thorchain/migrations.go`            | `!stagenet && !chainnet && !mainnet` | Mocknet/default bodies                                                                     |
| `x/thorchain/migrations_mainnet.go`    | `mainnet`                            | Mainnet bodies and pinned data specs                                                       |
| `x/thorchain/migrations_stagenet.go`   | `stagenet`                           | Stagenet bodies                                                                            |
| `x/thorchain/migrations_chainnet.go`   | `chainnet`                           | Chainnet bodies                                                                            |
| `x/thorchain/migration_<N>to<N+1>*.go` | none                                 | Large step logic split out (`migration_19to20.go`, `_sol.go`, `_eththor.go`, `_synth.go`)  |
| `x/thorchain/migration_helpers.go`     | none                                 | Reusable fixers (`unsafeAddRefundOutbound`, `requeueDanglingSettlementOutbound`, ...)      |
| `x/thorchain/migrations/v2`, `v3`      | none                                 | Old raw-store `MigrateStore` packages                                                      |

Each tagged file defines its own `Migrator` struct and `NewMigrator`, so adding a `Migrator` field means four edits.

`RegisterServices` only registers migrations from version 15 onwards; earlier `MigrateNtoN+1` methods are still defined but unregistered.

## Shapes

**Same everywhere:** define `Migrate<N>to<N+1>` once in `migrations_common.go`. Do not also define it in a tagged file (duplicate method).

**Per network:** define it in each of the four tagged files; non-target networks return `nil`.

**Common step plus network-specific tail** (current `Migrate19to20`): the common method does the shared work, then calls an unexported `m.migrate19to20Mainnet(ctx)` that every tagged file defines (`return nil` off mainnet).

Each body that touches managers starts with `m.mgr.LoadManagerIfNecessary(ctx)`: migrations run inside the x/upgrade pre-block, before `BeginBlock` has loaded managers for the block.

## Mainnet data fixes (refunds, slash reversals, donations)

- **run/apply split.** `runX(ctx, mgr, spec)` opens `ctx.CacheContext()`, calls `applyX(cacheCtx, ...)`, commits only on success, and on error logs and returns (soft skip). `applyX` validates every record against the pinned spec and returns an error on drift. Template: `runDonationMigration` / `applyDonationMigration` in `migration_19to20.go`. One drifting step must not drop the others, so each sub-step gets its own run/apply.
- **Never halt the upgrade on data drift.** Returning an error from a `MigrateNtoN+1` fails the upgrade handler and halts the chain at the upgrade height.
- **Pin values from live state at the release cut,** bounded (e.g. pay `min(pinned, live)`), never over-pay.
- **Guard-flag footgun.** `const mainnetDonationSpecComplete = false` (`migrations_mainnet.go`) makes `migrate19to20Mainnet` log and skip. The module version still advances to 20, so shipping with the flag `false` drops the migration permanently; the fix would need a new 20 -> 21 migration. `migration_19to20_mainnet_test.go` asserts the flag is `true` to force the flip before the cut. Use the same pattern (flag + asserting test) for any data spec that is finalised late.
- **Determinism.** Iterate store iterators or sorted slices, not maps; copy iterator keys before deleting (`append([]byte(nil), iter.Key()...)`, see `Migrate19to20`).

## Testing

- Untagged tests (run under the default `mocknet` tag): `migrations_common_test.go`, `migration_19to20_test.go`, `migration_helpers_test.go`.
- Mainnet-tag tests (`//go:build mainnet`): `migrations_mainnet_test.go`, `migration_19to20_mainnet_test.go`, `_eththor_test.go`, `_sol_test.go`, `_slash_test.go`. CI unit tests run mocknet only; these run via `make test-network-specific`, which filters `TestPackage` with `-check.f 'Migrate|Migration19to20Suite|EthThorRestoreSuite'`.
- **New mainnet suite?** If its name does not contain `Migrate`, add it to that `-check.f` filter in the Makefile **and** to `SELECTIONS` in `scripts/test-quality-inventory.py` (a test enforces they match). Otherwise it silently never runs.
- Run one mainnet suite directly:

  ```bash
  go test -tags mainnet ./x/thorchain/ -run TestPackage -check.f 'Migration19to20Suite'
  ```

- End to end: `make test-regression RUN=upgrade` exercises propose/approve and the upgrade handler on mocknet.
