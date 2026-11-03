#!/usr/bin/env python3
"""THORNode CI health: why do merge-request jobs fail, and how often is it the MR?

  python3 tools/ci-health/run.py fetch      # pull new jobs, MRs and failed-job logs
  python3 tools/ci-health/run.py classify   # sign, classify, episode, interdiff
  python3 tools/ci-health/run.py report     # print the report (also <data>/report.txt)
  python3 tools/ci-health/run.py all        # all three

See tools/ci-health/README.md for the method, the categories and the caveats.
"""

import argparse
import os
import pickle
import re
import sys
import urllib.parse
from datetime import datetime, timedelta, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import classify  # noqa: E402
import fetch  # noqa: E402
import gitlab  # noqa: E402
import interdiff  # noqa: E402
import report  # noqa: E402


def day(s, end=False):
    """A date or ISO timestamp, as the string jobs' created_at compares against."""
    if len(s) == 10:
        return s + ("T23:59:59Z" if end else "")
    return s


def parse_args(argv):
    today = datetime.now(timezone.utc).date()
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    p.add_argument("stage", choices=("fetch", "classify", "report", "all"))
    p.add_argument(
        "--data-dir",
        default=os.environ.get("CI_HEALTH_DATA", os.path.join(HERE, "data")),
        help="where fetched data and stage outputs live (default: $CI_HEALTH_DATA "
        "or tools/ci-health/data)",
    )
    p.add_argument("--gitlab-url", default="https://gitlab.com")
    p.add_argument("--project", default="13422983", help="GitLab project id or path")
    p.add_argument(
        "--since",
        default=str(today - timedelta(days=730)),
        help="first day of the window, YYYY-MM-DD or ISO time (default: two years ago)",
    )
    p.add_argument(
        "--until",
        default=str(today),
        help="last day of the window, YYYY-MM-DD or ISO time (default: today)",
    )
    p.add_argument(
        "--token-env",
        default="GITLAB_TOKEN",
        help="env var holding the API token; if unset, `glab config get token` is used",
    )
    p.add_argument(
        "--git-mirror",
        help="bare git mirror for interdiffs (default: <data-dir>/gitmirror)",
    )
    p.add_argument(
        "--git-remote",
        default="https://gitlab.com/thorchain/thornode.git",
        help="remote the mirror fetches from",
    )
    p.add_argument("--develop-ref", default="refs/heads/develop")
    p.add_argument(
        "--no-git-fetch",
        action="store_true",
        help="never fetch into the mirror; pairs it cannot answer are left unknown",
    )
    p.add_argument(
        "--overlap-days",
        type=float,
        default=3,
        help="refetch jobs this far before the newest stored job (fetch)",
    )
    p.add_argument(
        "--deep-jobs",
        default=r"^test-simulation$",
        help="regex of job names whose whole log is always scanned (fetch)",
    )
    p.add_argument(
        "--flaky-window-days",
        type=float,
        default=30,
        help="an episode is known-flaky if its signature passed on unchanged code "
        "in another context within ±this many days (classify)",
    )
    p.add_argument(
        "--cross-mr-window-days",
        type=float,
        default=3,
        help="other MRs failing the same way within ±this many days make a "
        "failure shared:cross-mr (classify)",
    )
    p.add_argument("--workers", type=int, default=10)
    a = p.parse_args(argv)
    a.since = day(a.since)
    a.until = day(a.until, end=True)
    a.git_mirror = a.git_mirror or os.path.join(a.data_dir, "gitmirror")
    a.host = urllib.parse.urlparse(a.gitlab_url).hostname
    return a


def client(a):
    token = gitlab.get_token(a.token_env, a.host)
    project = (
        a.project if a.project.isdigit() else urllib.parse.quote(a.project, safe="")
    )
    return gitlab.GitLab(a.gitlab_url, project, token)


def do_fetch(a):
    gl = client(a)
    fetch.fetch_mrs(gl, a.data_dir, a.since)
    fetch.fetch_jobs(gl, a.data_dir, a.since, a.overlap_days)
    fetch.fetch_traces(gl, a.data_dir, a.since, a.until, a.workers)
    fetch.fetch_deep(
        gl, a.data_dir, a.since, a.until, a.workers, re.compile(a.deep_jobs)
    )


def do_classify(a):
    d = classify.run(
        a.data_dir, a.since, a.until, a.flaky_window_days, a.cross_mr_window_days
    )
    token = None
    if not a.no_git_fetch:  # thornode is public: fetch anonymously without one
        token = gitlab.get_token(a.token_env, a.host, required=False) or None
    mirror = interdiff.Mirror(a.git_mirror, a.develop_ref, a.git_remote, token)
    idf = interdiff.run(
        a.data_dir, d["eps"], mirror, not a.no_git_fetch, max(a.workers, 12)
    )
    with open(os.path.join(a.data_dir, "stage2.pkl"), "wb") as f:
        pickle.dump(idf, f)


def do_report(a):
    with open(os.path.join(a.data_dir, "stage1.pkl"), "rb") as f:
        d = pickle.load(f)
    with open(os.path.join(a.data_dir, "stage2.pkl"), "rb") as f:
        idf = pickle.load(f)
    report.enrich(d, idf)
    text = report.render(d)
    with open(os.path.join(a.data_dir, "report.txt"), "w") as f:
        f.write(text)
    sys.stdout.write(text)


def main(argv=None):
    a = parse_args(argv)
    os.makedirs(a.data_dir, exist_ok=True)
    if a.stage in ("fetch", "all"):
        do_fetch(a)
    if a.stage in ("classify", "all"):
        do_classify(a)
    if a.stage in ("report", "all"):
        do_report(a)


if __name__ == "__main__":
    main()
