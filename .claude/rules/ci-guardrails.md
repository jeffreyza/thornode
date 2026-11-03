# CI guardrails for bots and agents

These rules apply to every bot and AI agent that works in this repository, huginn-bot included.

## Tests and regression goldens

The `test-integrity` CI job (`scripts/test-integrity-guard.py`) fails a merge request that deletes a `*_test.go` file or a regression suite, leaves a test file with fewer tests, or changes or deletes the exports or blocks under `test/regression/mnt/` (adding them with a new suite is fine). Only a human reviewer clears it, with the `goldens-reviewed` label.

Bots and agents must never:

- add the `goldens-reviewed` label;
- delete, skip or weaken a failing test, or regenerate or delete goldens (`EXPORT=1`, removing files under `test/regression/mnt/`), to make CI pass;
- enable auto-merge, or add the MR to a merge train, on an MR where `test-integrity` tripped; the human who reviews it merges it.

When `test-integrity` trips, or the only way to make CI pass is to change a test or a golden rather than the code, stop and ask a human. Say what changed and why, and let them decide.

## Before every push

Run the checks of CI's `lint` and `generate` jobs before every push, and push only when they pass:

- `make lint-ci`;
- `make generate` (needs Docker), after which `git status --porcelain` must show nothing new.

`make install-git-hooks` installs a pre-push hook that runs `make lint-ci`, and `make generate` too with `PRE_PUSH_GENERATE=1`. Do not bypass it with `SKIP_PRE_PUSH=1` or `git push --no-verify`. If a check cannot run, for example without Docker, say so in the MR.
