# CI health

How often does a red job on a merge request mean the MR broke something, and how
often is it noise? This tool pulls every THORNode CI job from GitLab, reads the
logs of the failed ones, and sorts each failed attempt into infrastructure,
flaky, not-this-MR, MR-caused, can't-tell or unresolved. It then counts the
"catches": jobs that went red because of the MR's own change and went green
after the MR changed.

It uses only the Python 3.10+ standard library, plus `git` and (for the token)
`glab`.

## Running it

```bash
python3 tools/ci-health/run.py fetch      # pull new jobs, MRs and failed-job logs
python3 tools/ci-health/run.py classify   # sign, classify, episode, interdiff
python3 tools/ci-health/run.py report     # print the report; also <data>/report.txt
python3 tools/ci-health/run.py all        # all three; same as `make ci-health`
```

The first `fetch` of a two-year window reads about 120k jobs and 13k logs and
takes about an hour. Later runs are incremental: they read jobs back to three days
before the newest stored job, MRs updated since the last fetch, and logs of
failed jobs that have none yet. A monthly rerun takes minutes. `classify` takes
a few minutes, plus `git fetch` time for commits the mirror lacks.

| Option                   | Default                                     | Meaning                                                      |
| ------------------------ | ------------------------------------------- | ------------------------------------------------------------ |
| `--data-dir`             | `$CI_HEALTH_DATA` or `tools/ci-health/data` | everything fetched and computed (git-ignored)                |
| `--since`, `--until`     | two years ago, today                        | job window, `YYYY-MM-DD` or ISO time                         |
| `--project`              | `13422983`                                  | GitLab project id or path                                    |
| `--gitlab-url`           | `https://gitlab.com`                        |                                                              |
| `--token-env`            | `GITLAB_TOKEN`                              | env var with an API token; else `glab config get token`      |
| `--git-mirror`           | `<data-dir>/gitmirror`                      | bare mirror used for interdiffs; created on first use        |
| `--git-remote`           | thornode on gitlab.com                      | where the mirror fetches commits from                        |
| `--develop-ref`          | `refs/heads/develop`                        | merge-base reference for interdiffs                          |
| `--no-git-fetch`         | off                                         | never fetch; pairs the mirror cannot answer are left unknown |
| `--overlap-days`         | `3`                                         | how far back `fetch` rereads jobs to catch status changes    |
| `--deep-jobs`            | `^test-simulation$`                         | jobs whose whole log is always scanned, not only the tail    |
| `--flaky-window-days`    | `30`                                        | known-flaky window: ±days around an episode (tiers)          |
| `--cross-mr-window-days` | `3`                                         | cross-MR window: ±days around a failed attempt (classify)    |
| `--workers`              | `10`                                        | parallel log downloads                                       |

Pass options after the stage, or through `make ci-health CI_HEALTH_ARGS="..."`.

The data directory holds `jobs.jsonl`, `mrs.jsonl`, `traces/` (last 96KB of
each failed job's log), `deep/` (failure-bearing lines of the whole log),
`gitmirror/`, `state.json` (fetch cursors), `stage1.pkl`, `interdiff.jsonl` (a
cache keyed by commit pair), `stage2.pkl` and `report.txt`. Delete
`interdiff.jsonl` to recompute interdiffs against a newer develop.

Point `--git-mirror` at a dedicated bare repository, not a working clone:
`classify` fetches commits into it and moves its develop ref.

`make test-ci-health` runs the signature tests.

## Method

1. **Fetch.** Jobs come from the keyset-paginated jobs API, retried attempts
   included. For each failed job the last 96KB of its log is read with an HTTP
   `Range` header. For test-simulation, whose failing actor is logged long
   before the end, and for any failure whose tail shows nothing specific, the
   whole log is streamed and only failure-bearing lines are kept.
2. **Map jobs to code.** Each job maps to a context (an MR, develop, a tag, a
   protected branch or an unmatched branch) and to the MR head commit it
   tested. For a merged-result pipeline that is the merge commit's second
   parent; a merge-train commit has one parent, so it maps to the MR's latest
   non-train head before it.
3. **Sign each failure.** `sig.py` reads the log and returns a kind (compile,
   lint, gotest, regression, sim, ...), a normalised key (failing tests, files,
   suites, actor) and any environment patterns that matched (registry, DNS,
   git fetch, OOM, ...).
4. **Classify each failed attempt.** The first rule that fires wins:
   1. `infra:runner`: GitLab gave a failure reason other than a script failure
      or a job timeout (system failure, stuck, ...).
   2. `infra:timeout`: the job hit its time limit.
   3. `infra:log-limit`: the runner stopped the job because its log passed
      GitLab's size limit ("Job's log exceeded limit"). Reported apart from
      timeouts: it means runaway output, which the MR itself may cause.
   4. `infra:external`: an environment pattern matched and the signature is
      too weak (unknown, panic, package-level FAIL, thornode unreachable) to
      say anything else failed. The tss-lib-private fetch error ("no secure
      protocol found") counts only when it is within 60 lines of the end of
      the log: since May 2026 most simulation runs print it from `go list`
      and keep running for ten minutes or more, and only a failed
      `go mod download` ends the job right after it.
   5. `flaky:passed-on-same-code`: the same job passed on the same MR and the
      same head commit.
   6. `shared:develop-broken`: develop failed this job (not for environment
      reasons) on the target commit, or on the latest develop run before, with
      a matching signature.
   7. `shared:cross-mr`: the same job failed with the same kind and key on MRs
      or feature branches of 2+ other authors within ±3 days
      (`--cross-mr-window-days`). Develop, tag and release-branch runs do not
      count (develop has rule 6). Several MRs by one author count once, since
      stacked MRs share a cause, and the attempt's own author does not count.
      Keys that cannot show a shared cause are left out: "thornode
      unreachable" (any mocknet that never came up, including one the MR
      broke) and a regression diff without its field list. A regression diff
      key names the suites and the export fields the diff adds or removes
      (`diff:suites/x.yaml#+new_field,-old_field`).
   8. Otherwise `branch?`: possibly the MR's fault.
5. **Episodes.** For each (context, job), successive head commits are walked in
   order. A run of heads with `branch?` failures is one episode, closed by the
   first head on which the job passed. One episode is one "catch", however
   many pushes it took.
6. **Interdiff.** For a resolved episode, the last red head and the first green
   head are compared rebase-insensitively: each head's own patch is its diff
   from its merge base with develop, and only files whose own patch differs
   count (after a rebase, without context lines, which would pick up develop's
   edits next to the MR's). For an MR that has since merged, develop already
   contains both heads, so the base is the develop commit just before the merge; after a
   fast-forward merge there is none and the fix type is unknown. No difference, or identical trees, means the MR did not change
   anything that matters: "passed after rebase/no-op push". Otherwise the
   changed files are classed as code, test, golden, generated, CI/build or
   docs.
7. **Tiers.** Each episode gets an evidence tier; the first that applies wins:
   - `A:deterministic-check`: a deterministic job (lint, generate, proto, build,
     govulncheck, ...) or signature (compile, lint, gendiff, format, vuln,
     proto-breaking). These are functions of the code, so they come before the
     known-flaky test: a "flaky" match there is an infra blip with the same
     text.
   - `C:known-flaky-signature`: the same job failed with the same key on code
     that later passed unchanged, in another context within ±30 days
     (`--flaky-window-days`).
   - `A:deterministic-check` for a regression export diff. Exports are
     deterministic by design, but about one diff failure in ten passed on
     identical code (cross-suite leaks under parallel runs), so a known-flaky
     match wins.
   - `A:reproduced-same-signature`: the same specific key on 2+ attempts.
   - `B:reproduced-generic-signature`: a generic key on 2+ heads.
   - `B:single-specific-failure`: one specific failure.
   - `C:unreproduced-generic`: one generic failure.
8. **Report.** A resolved episode with an A or B tier is REAL; a C tier is
   can't-tell.

## Categories in the report

| Report line                                  | Meaning                                                           |
| -------------------------------------------- | ----------------------------------------------------------------- |
| REAL: MR's own change, fixed by a later push | `branch?`, went green after the MR changed, tier A or B           |
| infrastructure / external service            | `infra:*`                                                         |
| flaky: passed on identical code              | `flaky:passed-on-same-code`                                       |
| not this MR: develop broken, or same failure | `shared:*`                                                        |
| not this MR: passed after rebase/no-op push  | `branch?`, went green with no change of the MR's own              |
| can't tell                                   | `branch?`, went green after a change, but tier C                  |
| unresolved                                   | `branch?`, never went green (abandoned, still open, or no commit) |

## Caveats

- An independent hand audit of 132 items from the October 2026 run found the
  REAL sample 36/36 MR-caused and the noise sample 28/30 correct; the can't-tell
  bucket split about 50/50, so treat can't-tell as half MR-caused. The audit's
  findings led to the rule fixes below; the fixed rules have not been
  re-audited.
- Most of the "thornode unreachable" simulation failures that no longer count
  as cross-MR land in can't-tell: the signature is generic, so one red version
  is not evidence either way.
- `infra:log-limit` stays in the infrastructure line, but runaway output is
  often the MR's own.
- An unmatched feature branch's "author" is whoever ran its pipeline.
- "The fix touched production code" is not "CI caught a production bug": lint
  fixes, `go.mod` syncs and rebases that drop stacked commits land there too.
  Only a hand read of the interdiff tells a bug from an intended change.
- A log that GitLab erased, or that is longer than 96KB and was not deep-read,
  can hide the failure; those attempts sign as `unknown` or `notrace`.
- Interdiffs are cached by commit pair. If an MR's base commits later land on
  develop, a fresh interdiff can differ from the cached one.
- Commits that GitLab no longer serves (very old force-pushes) cannot be
  fetched; their episodes count as unresolved.

## Changes since the October 2026 report

The first commit of this tool ports the October 2026 analysis unchanged and
reproduces its report. The later commits fix rules the hand audit found weak.
On the same data (2024-10-17 to 2026-10-17, 11,547 failed blocking MR
attempts), cumulatively:

| Rule change                            | REAL  | infra | flaky | not this MR | can't tell | unresolved | catches |
| -------------------------------------- | ----- | ----- | ----- | ----------- | ---------- | ---------- | ------- |
| October 2026 report (port)             | 3,560 | 2,535 | 2,426 | 1,261       | 1,048      | 717        | 1,452   |
| explicit windows, log-limit split      | 3,560 | 2,535 | 2,426 | 1,261       | 1,048      | 717        | 1,452   |
| tss-lib error only if it ended job     | 3,564 | 2,344 | 2,492 | 1,305       | 1,119      | 723        | 1,452   |
| deterministic tier before known-flaky  | 3,598 | 2,344 | 2,492 | 1,305       | 1,085      | 723        | 1,461   |
| cross-MR: specific keys, other authors | 3,789 | 2,344 | 2,492 | 910         | 1,264      | 748        | 1,524   |

"Not this MR" adds develop-broken, cross-MR and passed-after-rebase. The
log-limit split moved 35 of the 187 "timeouts" to `infra:log-limit`.
