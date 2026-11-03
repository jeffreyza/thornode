# Test quality CI

The root Go module and `test/simulation` are independent modules. Run the fast
checks with the Go version specified by their `go.mod` files. The root module
currently pins Go 1.26.10; the nested module requires 1.26.10. Limit concurrent
builds on a shared host with `flock /tmp/thornode-test-quality-build.lock` and
`GOMAXPROCS=4 GOFLAGS=-p=2`. Use a writable `GOCACHE` if the shared cache is
read only. Put the Go 1.26.10 toolchain's `bin` directory first on `PATH` and
set `GOTOOLCHAIN=local`; verify `go version` reports `go1.26.10` before testing.

Fast MR commands, in dependency order (run each `make` command under the shared
lock on a shared host):

```sh
python3 -m unittest discover -s scripts/tests -p 'test_quality_*_test.py' -v
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-quality-mainnet
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-quality-simulation
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-coverage-sum
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-race-bifrost
```

`test-quality-mainnet` requires the Monero fail-closed test to pass. The runner
also fails if no top-level tests execute. Each run saves selected packages,
JSON events, a JUnit report, skipped test counts, and coverage under
`test-quality-reports/<module>-<configuration>/`. Generated protobuf files are
excluded from the filtered profile, matching the root coverage target.
The root unit job also saves a source inventory of modules, build constraints,
and test-file directories, and fails if any `*_test.go` file is compiled by no
CI test configuration (`SELECTIONS` in `scripts/test-quality-inventory.py`).
Known gaps are listed with a reason in `KNOWN_UNSELECTED`; an entry that becomes
selected or is deleted also fails, so the list cannot go stale. A selection
that mirrors a gocheck `-check.f` filter, such as the mainnet migration run,
records that filter. The check fails if a gocheck test in a file compiled only
under such filters matches none of them; otherwise a new suite could compile
cleanly and never run. The
`unit-tests` job runs the network-tagged Go matrix through
`make test-coverage-sum`; its stagenet, chainnet, mainnet, mainnet migration,
and mocknet subruns each publish their own JUnit and coverage artifacts. The
`test-mocknet-scripts` job runs the Python tests for these scripts (the first
fast command above).

`scripts/test-quality-coverage.py --profile coverage.txt --base-ref <base-sha>
--output test-quality-reports/root-mocknet/changed-coverage.json` reports
statement coverage for changed Go lines by package. Add
`--baseline-profile <base-coverage.txt>` to compare overall coverage in changed
packages against the latest successful target-branch coverage artifact. Without
a baseline, the report explicitly says that a regression comparison is
unavailable. CI uses the MR diff base when the commit is present in its checkout
and attempts to fetch the baseline with its job token. This report is
diagnostic; it has no arbitrary coverage threshold.

Deeper runs on isolated infrastructure:

```sh
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=2 make test-regression
GOTOOLCHAIN=go1.26.10 GOMAXPROCS=4 GOFLAGS=-p=2 make test-simulation-cluster-native TEST_SIMULATION_REQUIRE_XMR=1
```

Simulation records `RANDOM_SEED_HEX` as an artifact before
starting. Both integration jobs retry runner failures and any `script_failure`,
including an assertion failure: `test-simulation` once, `test-regression` up to
twice. GitLab retries when either the failure reason or the exit code matches,
so their `exit_codes: [75]` adds nothing beyond `script_failure`. A simulation
failure that passes on retry is still reproducible from the recorded seed. The
full simulation's container and mocknet setup must be isolated from other
concurrent test work.

The curated mutation runner is committed with this branch. Its fast mode runs
for relevant MRs with a 30 minute runner budget, preserves a JSON summary, and
fails for unexpected surviving mutations. Every mutant is in the fast set, so
there is no separate deep job. Run it locally with
`python3 scripts/test-security-mutations.py --mode fast --summary /tmp/mutations-fast.json`;
the runner owns the build lock. The determinism and fuzz workspaces still own their standalone runners.
Wire those jobs only after their runner, corpus or seed persistence, and failure
semantics are committed and verified.

Merge the `test-quality-sim-unit` fixture changes before enabling this CI
configuration on the default branch. On the current base, live-service tests
in `pkg/dag`, `pkg/solana`, and `pkg/types` fail in an isolated unit job. The
CI job must retain those failures until the fixtures are made self-contained.
