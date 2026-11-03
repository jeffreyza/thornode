---
name: protocol-upgrade
description: Ship THORNode consensus changes safely - release version bumps (`version`, openapi, app/upgrades.go), x/thorchain store migrations (ConsensusVersion, migrations*.go per build tag), store key add/remove packages under app/upgrades/<semver>, mimir-scheduled migrations, and the validator propose/approve upgrade flow. Use whenever the user asks how a change activates on mainnet, whether to version-gate something, how to fix or backfill on-chain state, or how to cut a release, even if they do not say "upgrade". For the handler or manager code itself use handler-development.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# THORNode Protocol Upgrades

Consensus changes on develop ship **ungated**. Validators propose and approve an upgrade name and height on-chain; the chain halts at that height, every node restarts on the new binary, the x/upgrade handler runs module migrations, and the new code is live from the next block. There is no per-feature activation by version.

## Decision Tree

1. **"Should I gate this behind a version?"** - No. See [No gates](#no-gates).
2. **Cutting a consensus-breaking release** - [Release](#1-consensus-breaking-release).
3. **Change or backfill existing on-chain state** at upgrade time - [Store migration](#2-store-migration), then `references/migrations.md`.
4. **Add or delete a module store key** (new SDK module, removed module) - [Store keys](#3-store-key-add-or-remove).
5. **Run a one-off state fix at a chosen height without a release** - [Scheduled migration](#4-scheduled-migration).
6. **Bifrost-only / non-consensus patch release** - [Patch release](#5-non-consensus-patch-release).
7. **Testing an upgrade** - [Test](#6-test).

## No gates

- `grep -rn 'semver.MustParse' x/thorchain --include='*.go' | grep -v _test` returns no feature gates; consensus fixes have shipped ungated since validator-scheduled upgrades replaced in-band version switching.
- `app.ThorchainKeeper` is built with a hard-coded `0.0.0` (`x/thorchain/keeper/v1/keeper.go:164`); `GetXManager(_ semver.Version, ...)` factories and `constants.GetConstantValues(_ semver.Version)` ignore their argument. A gate would either never fire or be dead code.
- What is still version-aware: `module.go` `BeginBlock` panics with "Unsupported Version: update your binary" when the network's lowest active node version (major/minor) is ahead of `constants.SWVersion`, and validator churn compares node versions (`manager_validator_current.go`). Neither is a feature switch.
- If old state must look different under new code, write a store migration. If a parameter should be tunable, use mimir (`mimir-parameter`).

## 1. Consensus-breaking release

1. Bump the root `version` file (e.g. `3.20.0` -> `3.21.0`). It feeds `constants.Version` / `SWVersion` via Makefile ldflags; there is no `version/version.go`.
2. Bump `version:` in `openapi/openapi.yaml` to match (and regenerate `openapi/gen`). `scripts/lint.sh` fails otherwise.
3. `app/upgrades.go` `Upgrades`: the upgrade name validators will propose must be registered. Either leave the list empty (then `RegisterUpgradeHandlers` auto-registers `standard.NewUpgrade(app.Version())`) or set it to `standard.NewUpgrade("<new version>")` (repo practice, e.g. `ab9e9b3e6`). **Gotcha:** if the list is non-empty, nothing is auto-appended - a stale `standard.NewUpgrade("<old>")` left from a patch release means no handler for the new name and the chain panics at the upgrade height.
4. If state must change, bump `ConsensusVersion()` and add a migration ([section 2](#2-store-migration)).
5. Every upgrade handler runs `keeperv1.UpdateActiveValidatorVersions` before `mm.RunMigrations` (`app/upgrades/standard/upgrades.go`). Custom handlers must do the same or the chain cannot resume.
6. On-chain flow (for reference): `MsgProposeUpgrade` / `MsgApproveUpgrade` / `MsgRejectUpgrade` (`x/thorchain/handler_upgrade.go`; CLI subcommands `propose-upgrade [name] [height]`, `approve-upgrade [name]`, `reject-upgrade [name]` in `x/thorchain/client/cli/tx.go`). `UpgradeApprovedByMajority` (`keeper/v1/keeper_upgrade.go`) needs 2/3 of active validators; `scheduleUpgradeIfNecessary` then schedules the x/upgrade plan.

## 2. Store migration

Runs once inside the x/upgrade handler at the upgrade height. Steps (detail and footguns in `references/migrations.md`):

1. Bump `func (AppModule) ConsensusVersion()` in `x/thorchain/module.go` (read the current value; do not trust a copied number).
2. `cfg.RegisterMigration(types.ModuleName, <N>, m.Migrate<N>to<N+1>)` in `AppModule.RegisterServices` (same file).
3. Implement `Migrate<N>to<N+1>` for **every** build tag: either once in untagged `migrations_common.go`, or in each of `migrations.go` (mocknet / default), `migrations_mainnet.go`, `migrations_stagenet.go`, `migrations_chainnet.go`. A missing tag fails only that network's build.
4. Start with `m.mgr.LoadManagerIfNecessary(ctx)` - migrations run before `BeginBlock`.
5. Mainnet data fixes: wrap in a run/apply pair (`runDonationMigration` / `applyDonationMigration` in `migration_19to20.go`) so drift soft-skips and logs instead of halting the upgrade.
6. Test with `-tags mainnet` too (Makefile `test-network-specific`).

## 3. Store key add or remove

1. Create `app/upgrades/<version>/` (directory name must equal the `version` file; `scripts/lint.sh` rejects any other non-`standard` dir).
2. Return an `upgrades.Upgrade` with `UpgradeName: "<version>"`, `CreateUpgradeHandler: standard.CreateUpgradeHandler` (or a custom one that still calls `UpdateActiveValidatorVersions`), and `StoreUpgrades{Added: [...], Deleted: [...]}`. Template: `git show c17452b9d:app/upgrades/3.4.0/denom.go` (added the `x/denom` store). Do not edit `StoreUpgrades` in `standard.NewUpgrade`.
3. Reference it from `app/upgrades.go` (`v3_4_0.NewUpgrade()` style) in place of the `standard` entry.
4. `RegisterUpgradeHandlers` sets `UpgradeStoreLoader` only when the on-disk upgrade info name matches, so the key change applies exactly at the upgrade height.
5. After the release, delete the package and restore the `standard` entry (e.g. `48b453968` "post v3.4.0 upgrade"); the lint will force this once `version` moves on.

## 4. Scheduled migration

For a state fix at a height chosen by mimir, without waiting for a release:

- `ScheduledMigrationMgr.EndBlock` (`x/thorchain/manager_scheduled_migration.go`) runs `processMigration` when block height equals mimir `ScheduledMigration` (`constants.MimirKeyScheduledMigration`).
- `processMigration` is per build tag: `manager_scheduled_migration_{mainnet,stagenet,mocknet,chainnet}.go`. Mainnet is currently a no-op; the mocknet one is the example used by `test/regression/suites/scheduled-migration/scheduled-migration.yaml`.
- Still a consensus change: the code ships in a release; only the trigger height is mimir. Clear the code after it has run.

## 5. Non-consensus patch release

Bifrost-only or query-only fixes (e.g. `3.19.0` -> `3.19.1`, commit `625bcff14`): bump `version` and `openapi/openapi.yaml`, and add `standard.NewUpgrade("<current network version>")` to `app/upgrades.go` so the patched binary still has a handler for the upgrade name the network already ran. No `ConsensusVersion` bump, no migration. Anything that changes state-machine output is not a patch release.

## 6. Test

- Unit: migration suites live next to the code (`migrations_common_test.go`, `migration_19to20_test.go`, `migrations_mainnet_test.go`). Default tag is `mocknet`:

  ```bash
  go test -tags mocknet ./x/thorchain/ -run TestPackage -check.f 'Migrate'
  make test-network-specific   # includes the mainnet-tag migration suites
  ```

- Regression: `make test-regression RUN=upgrade` (`test/regression/suites/upgrade/upgrade.yaml`, drives `tx-propose-upgrade` / `tx-approve-upgrade`) and `RUN=scheduled-migration`.
- Before pushing: `make lint` (version file, openapi, `app/upgrades/` dirs, handler lint).

## References

| File                       | Read this when                                                                             |
| -------------------------- | ------------------------------------------------------------------------------------------ |
| `references/migrations.md` | Writing a store migration: build-tag layout, run/apply pattern, guard flags, mainnet tests |
