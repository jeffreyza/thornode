#!/usr/bin/env python3
"""Stop a merge request that deletes tests or changes regression goldens.

Branches have gone green by making the tests agree with a bug: !4459 deleted a
unit test that would have failed, then a bot commit deleted the regression
exports that caught the same bug and test-regression wrote new ones; !3842
deleted ETH scanner tests that would have failed; !4602 rewrote about 134
regression goldens, a consensus-visible change with no version gate, and
nobody flagged it. Each of these is fine when a human means it, so this fails
until a reviewer adds the goldens-reviewed label.

It checks the MR's own diff, from its merge base to its head, never the merged
result, so develop moving on does not trip it. It fails on:
  1. a deleted *_test.go file, including one renamed to a path the go tool
     does not run: not *_test.go, or under testdata or a _ or . directory;
  2. a *_test.go file that ends the MR with fewer tests (`func TestX(` and
     gocheck/testify suite methods `func (s *S) TestX(`) than it started with.
     A test that leaves one file and arrives in another is a move and counts
     for neither, so renaming a test or a test file, or moving tests, passes;
  3. a deleted regression suite (test/regression/suites/**/*.yaml), including
     one renamed to a path the harness does not run, such as *.yaml.disabled;
  4. a regression golden (the exports and blocks under test/regression/mnt/
     that the harness compares against) that is changed, deleted, or added for
     a suite that already existed. Goldens added with a new suite pass.

In CI it reads the GitLab merge request variables. Locally it defaults to the
merge base of HEAD with origin/develop, and HEAD.
"""

import argparse
import os
import re
import subprocess
import sys
from collections import Counter

LABEL = "goldens-reviewed"
SUITES = "test/regression/suites/"
GOLDENS = ("test/regression/mnt/exports/", "test/regression/mnt/blocks/")
TEST_FUNC = re.compile(r"^func\s*(\([^)]*\))?\s*(Test\w*)\s*\(", re.M)
# Exit code the default CI retry treats as an infrastructure failure.
EX_TEMPFAIL = 75
# -l0 lifts the rename limit, so a renamed test file in a big MR stays a rename.
DIFF = ("diff", "--no-ext-diff", "--no-color", "-M", "-l0")


class FetchError(Exception):
    pass


def git(*args, check=True, timeout=None):
    return subprocess.run(
        ["git", *args],
        capture_output=True,
        check=check,
        encoding="utf-8",
        errors="replace",
        timeout=timeout,
    )


def fetch(*args, timeout=300):
    try:
        git("fetch", "--quiet", "--no-tags", *args, timeout=timeout)
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        raise FetchError(f"git fetch {' '.join(args)} failed: {exc}") from exc


def shallow():
    return git("rev-parse", "--is-shallow-repository").stdout.strip() == "true"


def ensure_commit(sha):
    # CI clones shallow, so the MR's base commit is usually not there. --depth
    # would turn a full local clone shallow, so it is only used on a shallow one.
    if git("cat-file", "-e", f"{sha}^{{commit}}", check=False).returncode != 0:
        fetch(*(["--depth=1"] if shallow() else []), "origin", sha)


def merge_base(head, target):
    """Merge base of head and origin/<target>, deepening a shallow clone as needed."""
    ref = f"refs/remotes/origin/{target}"
    for depth in (0, 200, 2000, -1):
        if depth and not shallow():
            break
        if depth > 0:
            fetch(f"--depth={depth}", "origin", head, f"+refs/heads/{target}:{ref}")
        elif depth < 0:
            fetch("--unshallow", "origin", timeout=900)
        result = git("merge-base", head, ref, check=False)
        if result.returncode == 0:
            return result.stdout.strip()
    raise FetchError(f"no merge base between {head} and origin/{target}")


def changed_files(base, head):
    """(status, path, new path for renames) for every file the diff touches."""
    fields = git(*DIFF, "--name-status", "-z", base, head).stdout.split("\0")
    changes = []
    i = 0
    while i < len(fields) and fields[i]:
        status = fields[i][0]
        if status in "RC":
            changes.append((status, fields[i + 1], fields[i + 2]))
            i += 3
        else:
            changes.append((status, fields[i + 1], None))
            i += 2
    return changes


def is_test_file(path):
    """A *_test.go file the go tool runs: it skips testdata and _ or . directories."""
    *dirs, name = path.split("/")
    return name.endswith("_test.go") and not any(
        d == "testdata" or d.startswith((".", "_")) for d in dirs
    )


def test_funcs(rev, path):
    """(name, signature) of each test in path at rev. TestMain is setup, not a test."""
    if not path or not is_test_file(path):
        return []
    source = git("show", f"{rev}:{path}").stdout
    return [
        (m[2], f"func {m[1]} {m[2]}" if m[1] else f"func {m[2]}")
        for m in TEST_FUNC.finditer(source)
        if m[2] != "TestMain"
    ]


def shrunk_test_files(base, head, changes):
    """Test files that end the MR with fewer tests than they started with."""
    files = []
    for status, path, new in changes:
        old = None if status == "A" else path
        cur = None if status == "D" else new or path
        before, after = test_funcs(base, old), test_funcs(head, cur)
        if before or after:
            gone = Counter(n for n, _ in before) - Counter(n for n, _ in after)
            came = Counter(n for n, _ in after) - Counter(n for n, _ in before)
            files.append((old, cur, before, after, gone, came))
    all_gone = sum((f[4] for f in files), Counter())
    all_came = sum((f[5] for f in files), Counter())
    shrunk = []
    for old, cur, before, after, gone, came in files:
        # An added file cannot shrink, and a deleted one is reported as deleted.
        if not old or not cur or not is_test_file(cur):
            continue
        # A test that leaves this file and arrives in another, or the reverse,
        # is a move: count it for neither file.
        moved_out = gone & (all_came - came)
        moved_in = came & (all_gone - gone)
        kept = len(after) - sum(moved_in.values()) + sum(moved_out.values())
        if kept < len(before):
            lost = [sig for name, sig in before if name in gone - moved_out]
            shown = cur if old == cur else f"{old} -> {cur}"
            shrunk.append(
                f"{shown}: {len(before)} -> {len(after)} tests, gone: {', '.join(lost)}"
            )
    return shrunk


def is_suite(path):
    return path.startswith(SUITES) and path.endswith((".yaml", ".yml"))


def golden_suites(path):
    """The suite paths a golden belongs to: blocks/x/y.json is suites/x/y.yaml."""
    stem = path.split("/", 4)[4].removesuffix(".json")
    return (f"{SUITES}{stem}.yaml", f"{SUITES}{stem}.yml")


def find_violations(base, head):
    """[(title, items)] for each rule, and the goldens added with new suites."""
    changes = changed_files(base, head)
    new_suites = {
        path for status, path, _ in changes if status == "A" and is_suite(path)
    }
    deleted_tests, deleted_suites, goldens, new_goldens = [], [], [], []
    for status, path, new in changes:
        shown = f"{path} -> {new}" if new else path
        if is_test_file(path) and (
            status == "D" or (status == "R" and not is_test_file(new))
        ):
            deleted_tests.append(shown)
        if is_suite(path) and (status == "D" or (status == "R" and not is_suite(new))):
            deleted_suites.append(shown)
        if path.startswith(GOLDENS) or (new or "").startswith(GOLDENS):
            if status == "A" and new_suites.intersection(golden_suites(path)):
                new_goldens.append(path)
            else:
                goldens.append(f"{status} {shown}")
    return [
        ("Deleted test files (*_test.go)", deleted_tests),
        ("Test files left with fewer tests", shrunk_test_files(base, head, changes)),
        ("Deleted regression suites", deleted_suites),
        ("Regression goldens not added with a new suite", goldens),
    ], new_goldens


def main():
    env = os.environ
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--base", help="diff base (default: the MR's diff base SHA)")
    parser.add_argument("--head", help="MR head (default: the MR's source branch SHA)")
    parser.add_argument(
        "--labels",
        default=env.get("CI_MERGE_REQUEST_LABELS", ""),
        help="comma-separated MR labels (default: CI_MERGE_REQUEST_LABELS)",
    )
    args = parser.parse_args()

    target = env.get("CI_MERGE_REQUEST_TARGET_BRANCH_NAME") or "develop"
    # CI_MERGE_REQUEST_SOURCE_BRANCH_SHA is set only in merged-results and
    # merge train pipelines, where CI_COMMIT_SHA is the merge commit. In a plain
    # MR pipeline CI_COMMIT_SHA is the source branch head.
    head = (
        args.head
        or env.get("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA")
        or env.get("CI_COMMIT_SHA")
        or "HEAD"
    )
    try:
        ensure_commit(head)
        head = git("rev-parse", f"{head}^{{commit}}").stdout.strip()
        base = args.base or env.get("CI_MERGE_REQUEST_DIFF_BASE_SHA")
        if base:
            ensure_commit(base)
        else:
            base = merge_base(head, target)
            print(f"No MR diff base given; using the merge base with origin/{target}")
    except FetchError as exc:
        print(f"test-integrity: {exc}")
        return EX_TEMPFAIL

    print(f"Checking the MR's own diff {base[:12]}..{head[:12]}")
    rules, new_goldens = find_violations(base, head)
    if new_goldens:
        print(f"{len(new_goldens)} goldens added with new regression suites; allowed.")
    violations = [(title, items) for title, items in rules if items]
    if not violations:
        print("No lost tests, deleted regression suites or changed regression goldens.")
        return 0

    print()
    for title, items in violations:
        print(f"{title}:")
        for item in items:
            print(f"  {item}")
        print()
    if LABEL in (label.strip() for label in args.labels.split(",")):
        print(
            f"The MR has the {LABEL} label; a reviewer confirmed these changes are intended."
        )
        return 0
    print(
        f"A human reviewer must confirm these changes are intended, add the label {LABEL}, "
        "then run a new pipeline (labels are read when the pipeline is created)."
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
