"""Fetch stage: GitLab jobs, merge requests and failed-job logs into the data dir.

Every step is incremental, so a monthly rerun only pulls what is new:

- jobs: walk the keyset-paginated job list from the newest job down to
  --overlap-days before the newest job already stored (re-reading those picks up
  jobs that were still running last time), then backfill down to --since if
  the store does not reach that far. The newest-first walk measures its stop
  from the newest job of the last walk that finished (state.json), so an
  interrupted one is redone; an interrupted backfill resumes from its cursor.
- MRs: only MRs updated since the previous fetch, merged into mrs.jsonl, plus
  older ones when --since moves before the window already stored.
- logs: the last 96KB of each failed job's log (traces/), and, for selected
  jobs, the whole log filtered to failure-bearing lines (deep/). Jobs that
  already have a file are skipped.
"""

import base64
import gzip
import http.client
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone

import sig
from gitlab import RETRY_STATUS, log

TAIL = 96 * 1024
DEEP_MAX_LINES = 3000
# lines worth keeping from a whole log: test/lint/build failures and sim errors;
# the anchored ones allow GitLab's timestamp prefix (sig.TS strips it later)
DEEP_KEEP = re.compile(
    rb"^(?:\d{4}-\d\d-\d\dT[\d:.]+Z \d\d[OE][+ ]?)?"
    rb"([\w./-]+\.\w+:\d+:\d+\s*$|\s*\d+:\d+\s+(high|medium|low)|\s*FAIL[: ])|"
    rb"OOPS|ERR: |\xe2\x9c\x96|ISSUES|FTL|actor execution failed|op failed|--- FAIL|panic:|DATA RACE|Error Trace|Error:\s|timed out|timeout waiting|"
    rb"SolvencyCheck|invariant|assert|expected .* got|mismatch|Previously present|Vulnerability #|"
    rb"ERROR: Job failed|exit status|\.go:\d+:\d+: "
)
# a tail with one of these signatures probably lost the failure off its top
WEAK_TAIL = {"unknown", "notrace", "gotest-pkg", "sim-node-down", "panic"}
MR_FIELDS = (
    "iid",
    "title",
    "state",
    "created_at",
    "merged_at",
    "closed_at",
    "source_branch",
    "target_branch",
    "sha",
    "merge_commit_sha",
    "squash_commit_sha",
    "draft",
    "labels",
    "source_project_id",
    "web_url",
)


def parse_ts(s):
    if len(s) == 10:
        s += "T00:00:00Z"
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def iso(dt):
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_state(data_dir):
    try:
        with open(os.path.join(data_dir, "state.json")) as f:
            return json.load(f)
    except FileNotFoundError:
        return {}


def save_state(data_dir, state):
    p = os.path.join(data_dir, "state.json")
    with open(p + ".tmp", "w") as f:
        json.dump(state, f, indent=1)
    os.replace(p + ".tmp", p)


def iter_jsonl(path):
    if not os.path.exists(path):
        return
    with open(path) as f:
        for line in f:
            yield json.loads(line)


def load_jobs(data_dir):
    """Jobs by id; a later line for the same id (a refetch) wins."""
    return {j["id"]: j for j in iter_jsonl(os.path.join(data_dir, "jobs.jsonl"))}


def compact(j):
    c = j.get("commit") or {}
    p = j.get("pipeline") or {}
    r = j.get("runner") or {}
    tr = next(
        (a["size"] for a in j.get("artifacts") or [] if a.get("file_type") == "trace"),
        None,
    )
    return {
        "id": j["id"],
        "status": j["status"],
        "stage": j.get("stage"),
        "name": j["name"],
        "ref": j.get("ref"),
        "tag": j.get("tag"),
        "allow_failure": j.get("allow_failure"),
        "created_at": j.get("created_at"),
        "started_at": j.get("started_at"),
        "finished_at": j.get("finished_at"),
        "duration": j.get("duration"),
        "queued_duration": j.get("queued_duration"),
        "failure_reason": j.get("failure_reason"),
        "user": (j.get("user") or {}).get("username"),
        "sha": c.get("id"),
        "parents": c.get("parent_ids"),
        "title": c.get("title"),
        "author_email": c.get("author_email"),
        "pid": p.get("id"),
        "psha": p.get("sha"),
        "pref": p.get("ref"),
        "psource": p.get("source"),
        "pstatus": p.get("status"),
        "pcreated": p.get("created_at"),
        "runner_id": r.get("id"),
        "runner": r.get("description"),
        "trace_size": tr,
        "erased_at": j.get("erased_at"),
    }


def _keyset_cursor(job_id):
    # GitLab's keyset cursor is base64 JSON; "_kd": "n" pages to ids below job_id
    raw = json.dumps({"id": str(job_id), "_kd": "n"}, separators=(",", ":"))
    return urllib.parse.quote(base64.b64encode(raw.encode()).decode())


def _walk_jobs(gl, url, stop, out, data_dir, state, resumable):
    """Write pages of jobs until one reaches a job created before stop."""
    n = 0
    newest = oldest = None
    while url:
        r = gl.get(url)
        if r.status != 200:
            sys.exit(f"jobs: HTTP {r.status}: {r.body[:300]!r}")
        data = r.json()
        for j in data:
            out.write(json.dumps(compact(j)) + "\n")
            oldest = j.get("created_at")
            newest = newest or oldest  # ids descend: the first job is the newest
        out.flush()
        n += len(data)
        nxt = r.next_url()
        if resumable:
            state["jobs_cursor"] = nxt
            save_state(data_dir, state)
        if n % 5000 < 100:
            log("jobs", n, oldest)
        if not nxt or not data or (oldest and oldest < stop):
            break
        url = nxt
    if resumable:
        state["jobs_cursor"] = None
        save_state(data_dir, state)
    return n, newest


def fetch_jobs(gl, data_dir, since, overlap_days):
    path = os.path.join(data_dir, "jobs.jsonl")
    state = load_state(data_dir)
    newest = oldest = oldest_id = None
    for j in iter_jsonl(path):
        if newest is None or j["created_at"] > newest:
            newest = j["created_at"]
        if oldest_id is None or j["id"] < oldest_id:
            oldest, oldest_id = j["created_at"], j["id"]
    first = f"{gl.api}/jobs?per_page=100&pagination=keyset&order_by=id&sort=desc"
    n = 0
    with open(path, "a") as out:
        if newest is None:
            n, top = _walk_jobs(gl, first, since, out, data_dir, state, True)
            state["jobs_newest"] = top
            save_state(data_dir, state)
        else:
            # an interrupted walk stored jobs above a gap it never reached, so
            # measure from the last walk that finished
            mark = state.get("jobs_newest") or newest
            stop = max(since, iso(parse_ts(mark) - timedelta(days=overlap_days)))
            log("jobs: new since", stop)
            n, top = _walk_jobs(gl, first, stop, out, data_dir, state, False)
            state["jobs_newest"] = max(mark, top or mark)
            save_state(data_dir, state)
            resume = state.get("jobs_cursor")
            if not resume and since < oldest:
                resume = f"{first}&cursor={_keyset_cursor(oldest_id)}"
            if resume:
                log("jobs: backfilling to", since)
                n += _walk_jobs(gl, resume, since, out, data_dir, state, True)[0]
    # drop the rows the overlap re-read; newest first, like the API
    jobs = load_jobs(data_dir)
    with open(path + ".tmp", "w") as f:
        for jid in sorted(jobs, reverse=True):
            f.write(json.dumps(jobs[jid]) + "\n")
    os.replace(path + ".tmp", path)
    log("jobs: fetched", n, "stored", len(jobs))


def _walk_mrs(gl, url, mrs):
    n = 0
    while url:
        r = gl.get(url)
        if r.status != 200:
            sys.exit(f"merge_requests: HTTP {r.status}: {r.body[:300]!r}")
        for m in r.json():
            row = {k: m.get(k) for k in MR_FIELDS}
            row["author"] = (m.get("author") or {}).get("username")
            mrs[row["iid"]] = row
            n += 1
        url = r.next_url()
    return n


def fetch_mrs(gl, data_dir, since):
    path = os.path.join(data_dir, "mrs.jsonl")
    state = load_state(data_dir)
    mrs = {m["iid"]: m for m in iter_jsonl(path)}
    started = iso(datetime.now(timezone.utc))
    # MRs opened a little before the window still run CI inside it
    want = iso(parse_ts(since) - timedelta(days=60))
    # mrs_updated_after: the store holds every MR updated after it
    covered = state.get("mrs_updated_after")
    base = (
        f"{gl.api}/merge_requests?state=all&per_page=100"
        "&order_by=created_at&sort=desc"
    )
    n = 0
    if mrs and covered and state.get("mrs_fetched_at"):
        after = iso(parse_ts(state["mrs_fetched_at"]) - timedelta(days=1))
        n += _walk_mrs(gl, f"{base}&updated_after={after}", mrs)
        if want < covered:
            # --since moved earlier: MRs last updated before the old boundary
            # were never read
            before = iso(parse_ts(covered) + timedelta(days=1))
            log("mrs: backfilling updated", want, "to", before)
            url = f"{base}&updated_after={want}&updated_before={before}"
            n += _walk_mrs(gl, url, mrs)
            covered = want
    else:
        after = covered = want
        n += _walk_mrs(gl, f"{base}&updated_after={after}", mrs)
    # classify maps a branch to the first matching MR in file order: keep it
    # newest first, as the API returns them
    rows = sorted(mrs.values(), key=lambda m: (m["created_at"], m["iid"]), reverse=True)
    with open(path + ".tmp", "w") as f:
        for m in rows:
            f.write(json.dumps(m) + "\n")
    os.replace(path + ".tmp", path)
    state = load_state(data_dir)
    state["mrs_fetched_at"] = started
    state["mrs_updated_after"] = covered
    save_state(data_dir, state)
    log("mrs: fetched", n, "updated after", after, "stored", len(rows))


def _write_gz(path, body):
    with gzip.open(path + ".tmp", "wb") as f:
        f.write(body)
    os.replace(path + ".tmp", path)


def _failed_in_window(data_dir, since, until):
    for j in load_jobs(data_dir).values():
        if (
            j["status"] == "failed"
            and j.get("trace_size")
            and since <= j["created_at"] <= until
        ):
            yield j


def _fetch_tail(gl, tdir, jid):
    url = f"{gl.api}/jobs/{jid}/trace"
    try:
        r = gl.get(url, headers={"Range": f"bytes=-{TAIL}"}, attempts=6)
        if r.status == 416:  # log shorter than TAIL
            r = gl.get(url, attempts=6)
    except RuntimeError as e:
        log("failed", jid, e)
        return
    if r.status == 404:  # erased
        body = b""
    elif r.status in (200, 206):
        body = r.body[-TAIL:]
    else:
        log("failed", jid, "HTTP", r.status)
        return
    _write_gz(os.path.join(tdir, f"{jid}.gz"), body)


def fetch_traces(gl, data_dir, since, until, workers):
    tdir = os.path.join(data_dir, "traces")
    os.makedirs(tdir, exist_ok=True)
    have = set(os.listdir(tdir))
    todo = sorted(
        {
            j["id"]
            for j in _failed_in_window(data_dir, since, until)
            if f"{j['id']}.gz" not in have
        },
        reverse=True,
    )
    log("traces: todo", len(todo))
    _run_pool(workers, lambda jid: _fetch_tail(gl, tdir, jid), todo, "traces")


def deep_candidates(data_dir, since, until, deep_jobs):
    """Failed jobs whose whole log is worth reading, not only the tail.

    Jobs named by deep_jobs (test-simulation: the failing actor is logged long
    before the end), and any other failure whose log is longer than the tail
    and whose tail shows nothing specific.
    """
    ddir = os.path.join(data_dir, "deep")
    out = []
    for j in _failed_in_window(data_dir, since, until):
        if j["failure_reason"] != "script_failure":
            continue
        if os.path.exists(os.path.join(ddir, f"{j['id']}.gz")):
            continue
        if deep_jobs.search(j["name"]):
            out.append(j["id"])
        elif j["trace_size"] > TAIL:
            kind, key, _ = sig.signature(sig.load(j["id"], data_dir))
            if kind in WEAK_TAIL or not key:
                out.append(j["id"])
    return sorted(out, reverse=True)


def _fetch_deep(gl, ddir, jid):
    url = f"{gl.api}/jobs/{jid}/trace"
    for attempt in range(6):
        kept = []
        try:
            for line in gl.iter_lines(url):
                if len(kept) < DEEP_MAX_LINES and DEEP_KEEP.search(line):
                    kept.append(line[:600])
        except urllib.error.HTTPError as e:
            if e.code in RETRY_STATUS:
                time.sleep(2**attempt)
                continue
            if e.code != 404:
                # no file, so a later run retries once access is fixed
                log("failed", jid, "HTTP", e.code)
                return
            kept = []  # erased: record that there is nothing to read
        except (OSError, http.client.HTTPException):
            time.sleep(2**attempt)
            continue
        _write_gz(os.path.join(ddir, f"{jid}.gz"), b"\n".join(kept))
        return
    log("failed", jid)


def fetch_deep(gl, data_dir, since, until, workers, deep_jobs):
    ddir = os.path.join(data_dir, "deep")
    os.makedirs(ddir, exist_ok=True)
    todo = deep_candidates(data_dir, since, until, deep_jobs)
    log("deep: todo", len(todo))
    _run_pool(workers, lambda jid: _fetch_deep(gl, ddir, jid), todo, "deep")


def _run_pool(workers, fn, items, label):
    done = 0
    with ThreadPoolExecutor(workers) as ex:
        for _ in ex.map(fn, items):
            done += 1
            if done % 1000 == 0:
                log(label, done)
    log(label, "finished", done)
