# Consensus determinism replay

Run `scripts/test-consensus-determinism.sh` from any directory. The runner uses the
Go 1.26.10 version pinned by `go.mod`, the caller's `GOCACHE` (CI's shared
`/go-build-cache`; a dedicated `/tmp` cache when unset), a 15 minute limit on both
the `go test` process and the test binary (`-timeout 15m`), limited build
parallelism, and the shared build lock. It selects only
`TestConsensusDeterminism*` in `./app`; none of those tests depend on the SDK
simulation flag or skip when it is unset. CI runs this script in the
`consensus-determinism` job (`long-test` stage of `.gitlab-ci.yml`) and keeps
`test-quality-reports/consensus-determinism.log` as an artifact.

The SDK simulation manager is built from modules implementing
`module.AppModuleSimulation`. The registration test found `auth`, `bank`,
`denom`, `params`, `scheduler`, and `wasm`. THORChain's `AppModule` does not
implement that interface or register weighted operations, so
`TestAppStateDeterminism` does not exercise THORChain swaps, observations, or
outbounds. This suite uses a sanitized, fixed-time THORChain genesis fixture
containing four funded pools (BTC, ETH, DOGE, LTC), two
observed inbound transactions, a swap queue item, a scheduled outbound item,
network fees, and a nonzero Reserve. Independent app instances import and commit
that state eight times per seed. A fixed active validator supplies the required
genesis validator update. Each run finalizes a block before committing and
compares both the app hash and exported THORChain state. The app hash is the
root of every module store, so a divergent write in any store fails the
comparison; the exported THORChain JSON only makes a mismatch easier to read.
Every second run reverses pool order, which should have no effect on keeper
state. Go randomizes map iteration, but a small map takes some orders far more
often than others. With two pools and six runs, a local mutation that wrote the
last pool of a map iteration to a mimir escaped 1 in 5 suite runs; with four
pools and eight runs per seed it failed 20 of 20. A one-unit pool change
must produce a different committed app hash, proving the comparator detects
divergence.

The suite also exports the THORChain state, reopens the committed database, and
imports the export into a fresh app. At import, it checks that the THORChain
records, the Reserve balance, and the BTC pool match the committed source state.
It then finalizes and commits another block on two independent imports and
compares their app hashes and exported THORChain state. Balances are not
re-checked after that commit, because the block may legitimately spend from
Reserve.

The migration test invokes the standard 3.20.0 upgrade handler with THORChain's
module version map at 18 and checks that it reaches the current module
version (`AppModule.ConsensusVersion()`), so later version bumps run their
migrations too while the 18->19 effects are still checked. The handler
runs on the root store's working state after `FinalizeBlock`, so `Commit`
persists its writes. After the commit, the test reads committed state to check
that the active validator version is 3.20.0 and the Reserve and BTC pool
balances are unchanged. Before the upgrade it writes two legacy owner-keyed
affiliate collector records in their pre-19 wire format, one for an owner with a
THORName and one for an owner without, and funds the collector module to match.
After the commit the named collector holds the first balance, the second owner
received its RUNE, and both legacy keys are gone. Without those records the
migration would return before writing anything. Two independent runs must
produce the same post-migration hash and exported THORChain state.
The hashes across software versions or across export/import are not assumed to
match: only same-version, same-sequence committed hashes are compared.

The fixture imports persisted domain records through `InitGenesis` and runs
ordinary block begin/end handlers. It does not submit signed user transactions,
so it does not prove swap execution or outbound signing. Those paths need a
separate transaction-level replay with validator and vault fixtures.

Validated on this workspace with `scripts/test-consensus-determinism.sh`:
five tests passed, no skips (Go 1.26.9, `go test -p 2 -timeout 15m ./app -run
'^TestConsensusDeterminism' -count=1 -v`).
