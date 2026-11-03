# REVIEW.md

What to check when reviewing a THORNode merge request. It applies to human reviewers and to Claude (`/review-mr`, the `blockchain-code-review` skill).
Each section is a gate: an MR that fails one is not ready to approve, whatever the quality of the code.

Start every review by stating the risk level (below), then work through the gates in order. Report findings with a file and line, and say which gate they fail.

## 1. Risk level

Classify the MR as high, medium or low by the riskiest file it touches, not by its size. A one-line change in a handler is high risk.

| Level  | Touches                                                                                                                                                                                                                                                                                                                                                             |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| High   | Consensus state: `x/thorchain` handlers, managers, keepers, ante, Begin/EndBlock; store migrations and `app/upgrades/`; `.proto` types that are stored or exported; `constants/` defaults; memo parsing. Funds movement: Bifrost signing, outbound construction, gas/fee calculation, vault and TSS code, solvency checks. Anything in `common/` used by the above. |
| Medium | Bifrost observation and scanning that does not sign; querier and API responses that integrators read (`openapi/openapi.yaml`); new features gated behind a Mimir key that defaults off; CI, Docker and build configuration; dependency bumps.                                                                                                                       |
| Low    | Documentation, tests only, tooling under `tools/` and `scripts/` that nothing on a node runs, log messages, comments.                                                                                                                                                                                                                                               |

What the level means for the review:

- High: run the `blockchain-code-review` skill on the diff, and require regression coverage for every behavior change (gate 4). Consider asking for a second reviewer who knows the module. Any doubt about determinism or funds safety blocks approval.
- Medium: check backward compatibility of API and config changes, and confirm the Mimir default really is off.
- Low: confirm it really is low. A "docs" MR that also edits a `.go` file is not low.

## 2. Title and description match the diff

Read the full diff before the description, then compare.

- The title names the actual change. "Fix X" must fix X, and must not also add Y.
- Every behavior change in the diff appears in the description. Anything that changes consensus, fees, memos, Mimir keys, API responses or stored state must be called out explicitly, not left for the reader to find.
- Nothing in the description is missing from the diff (such as a fix it claims but does not contain, or a test it says was added).
- Linked issues are actually resolved by this change. If the MR only partly resolves one, the description says so.
- After force-pushes and follow-up commits, check the description again; it is often written for the first revision.

## 3. Unit test coverage of 80% or more

New and changed Go code needs at least 80% statement coverage from unit tests.

- Measure the changed lines, not the package. The `unit-tests` CI job publishes `test-quality-reports/root-mocknet/changed-coverage.json`, which reports coverage of the MR's changed lines by package. Locally: `make test-coverage-sum`, then `python3 scripts/test-quality-coverage.py --profile coverage.txt --base-ref origin/develop --output changed-coverage.json`.
- Generated code (`*.pb.go`, `*_gen.go`, `openapi/gen/`) is excluded and does not count either way.
- Coverage must exercise behavior. A test that calls the function but asserts nothing does not count. Check that error paths assert the specific error, not just `err != nil`.
- Tests run with `-tags=mocknet`. If a test only builds under another tag (`mainnet`, `stagenet`), confirm a CI configuration compiles it (`SELECTIONS` in `scripts/test-quality-inventory.py`); otherwise it never runs.
- Below 80% is acceptable only with a reason in the description, such as code that needs a live chain. The reviewer decides whether the reason holds.

## 4. Regression tests cover significant happy and unhappy paths

Unit tests show that functions work. Regression tests (`test/regression/suites/`, documented in `test/regression/README.md`) show that the chain behaves correctly block by block. Every behavior change in `x/thorchain` needs them.

- Happy path: the intended flow end to end, asserting resulting state (balances, pools, vaults, events), not only that the transaction was accepted.
- Unhappy paths: each significant way the flow can fail. Typical ones are invalid or malformed memos, failed validation, insufficient funds or below-dust amounts, refunds, halted chains or trading, the feature's Mimir key off and on, and limits and boundaries (min/max values, caps, overflow).
- For each unhappy path, assert what happens to the funds: refunded, held, or rejected, with the right event.
- Export fixtures under `test/regression/mnt/exports/` must be regenerated with `EXPORT=1`, not hand-edited. Fixture changes in suites unrelated to the MR are a red flag: they mean the change has effects the description does not mention.
- Bifrost-only changes are not covered by regression tests. For those, require unit tests against the chain client and, for signing or observation changes, a simulation test (`test/simulation/`).

## 5. Scope: flag unrelated or overly complicated changes

The diff should contain the change the MR is about and nothing else.

- Flag out-of-scope edits: refactors, renames, reformatting, dependency bumps or "while I was here" fixes in code unrelated to the MR's intent. Ask for them in a separate MR, even if each one is correct. Unrelated changes hide in large diffs and make reverts dangerous.
- Flag drive-by changes to consensus code in an MR whose intent is not consensus (for example, a Bifrost MR that edits a keeper).
- Flag needless complexity: new abstractions, interfaces or config options with one user; generic solutions to a specific problem; code that reimplements a helper already in `common/` or `x/thorchain`. Ask for the simplest version that solves the stated problem.
- Flag dead code, commented-out code, debug logging, and TODOs without an issue link.
- Large MRs that mix several independent changes should be split. If splitting is impractical, the description must say why.

## 6. Documentation is accurate and complete

Documentation that disagrees with the code is worse than none. For each kind of change, check that the matching doc was updated in the same MR:

| Change                            | Documentation to check                                                 |
| --------------------------------- | ---------------------------------------------------------------------- |
| New or changed Mimir key          | `docs/mimir.md` (name, default, meaning)                               |
| Querier or API response           | `openapi/openapi.yaml`, then `make generate`                           |
| Memo format                       | `docs/concepts/memos.md`                                               |
| Fees, swaps, protocol mechanics   | the relevant page in `docs/concepts/`                                  |
| New chain or chain client change  | `docs/chain-clients/`, `docs/new-chains/`                              |
| Release or upgrade behavior       | `docs/release.md`, `docs/upgrades/`                                    |
| Design decision with alternatives | an ADR in `docs/architecture/`                                         |
| Developer workflow or commands    | `CLAUDE.md`, `test/regression/README.md`, `scripts/TEST_QUALITY_CI.md` |

Also check code-level documentation: exported identifiers have accurate comments, and comments next to changed code still describe what it does.

## 7. Specification matches the code

If the MR implements a specification, the specification and the code must agree. Specifications live in `docs/superpowers/specs/` and `docs/superpowers/plans/`, `docs/architecture/` (ADRs), or are linked from the MR or its issue.

- Every requirement in the spec is implemented, or the description lists what is deferred and why.
- The code does nothing the spec does not describe. Extra behavior is either a spec update or out of scope (gate 5).
- Names, constants, defaults, Mimir keys, memo formats and error cases in the code match the spec exactly.
- If the implementation deliberately departs from the spec, the spec is updated in the same MR. A spec that describes the old design must not be left behind.

## 8. Consensus and funds safety (high-risk MRs)

The `blockchain-code-review` skill holds the full catalogue. Key points:

- No map-order iteration, floats, wall-clock time, randomness or goroutines in `x/`, `common/` or `constants/`.
- No panics in Begin/EndBlock; division by zero and overflow are handled.
- Store migrations are registered in `x/thorchain/module.go` and tested on mainnet-shaped state.
- Proto changes keep field numbers and types compatible with stored data.
- Changes to reads or writes inside a transaction change `gas_used`, which is consensus-visible.
- Bifrost: no double-signing or double-spend paths, and failure handling never loses funds or marks an outbound done when it is not.

## Before approving

- [ ] Risk level stated
- [ ] Title and description match the diff
- [ ] Changed-line unit coverage is 80% or more, or the shortfall is justified
- [ ] Regression tests cover the happy path and each significant unhappy path
- [ ] No out-of-scope or needlessly complex changes
- [ ] Documentation updated and accurate
- [ ] Specification, if any, matches the code
- [ ] CI is green (a retried flaky job is fine; a real failure is not)
