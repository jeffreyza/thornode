"""Classify stage: sign and classify every failed job attempt, then group what is
left into per-(MR, job) failure episodes and give each an evidence tier.

Writes stage1.pkl in the data dir.
"""

import bisect
import pickle
import re
from collections import Counter, defaultdict
from datetime import datetime, timedelta

import sig
from fetch import iter_jsonl, load_jobs
from gitlab import log

# failure_reason values that mean the job's own script ran and failed; any other
# reason (runner_system_failure, stuck_or_timeout_failure, ...) is the runner
INFRA_REASONS_SCRIPTLIKE = {"script_failure", "job_execution_timeout"}
DETERMINISTIC_JOBS = re.compile(
    r"^(lint|proto-lint|proto-check-formatting|proto-check-breaking|proto-format|generate|codespell|"
    r"semgrep|secret_detection|govulncheck|fork|text-files-check|check-.*|build-.*|docs.*|pages|changelog.*)$"
)
# signatures too unspecific to rule out an environment problem
WEAK = {"unknown", "notrace", "gotest-pkg", "sim-node-down", "panic"}
DETERMINISTIC_SIGS = {"compile", "lint", "proto-breaking", "gendiff", "format", "vuln"}
PROTECTED = re.compile(
    r"^(develop|main|master|mainnet|stagenet|chainnet|testnet|release-.*|v\d+.*)$"
)


def ts(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")) if s else None


def context(j, by_branch, until):
    """(context, kind): which MR or branch a job ran for, and the ref kind."""
    ref = j["pref"] or j["ref"] or ""
    m = re.match(r"refs/merge-requests/(\d+)/(head|merge|train)", ref)
    if m:
        return ("mr", int(m[1])), m[2]
    if j.get("tag"):
        return ("tag", ref), "tag"
    if ref == "develop":
        return ("develop", ref), "develop"
    if PROTECTED.match(ref):
        return ("protected", ref), "protected"
    m = re.match(r"trigger-ci-mr-(\d+)", ref)
    if m:
        return ("mr", int(m[1])), "branch"
    t = j["created_at"]
    for mr in by_branch.get(ref, []):
        end = mr["merged_at"] or mr["closed_at"] or until
        lo = (ts(mr["created_at"]) - timedelta(days=30)).isoformat()
        hi = (ts(end) + timedelta(days=1)).isoformat()
        if lo <= ts(t).isoformat() <= hi:
            return ("mr", mr["iid"]), "branch"
    return ("branch", ref), "branch"


def head_of(j, kind):
    """(MR head commit, target commit). A merged-result commit's second parent
    is the MR head and its first the target branch."""
    p = j.get("parents") or []
    if kind in ("merge", "train") and len(p) == 2:
        return p[1], p[0]
    return j["sha"], None


def generic(k, key):
    return (
        (not key)
        or k in ("sim-node-down", "unknown", "notrace", "infra-reason")
        or (k == "sim" and key.endswith(": timeout"))
        or (k == "regression" and key.startswith(("timeout:", "notfound:", "other:")))
    )


def sig_matches(a, b):
    return a[0] == b[0] and (a[1] == b[1] or not a[1] or not b[1])


def cross_mr_key(k, key):
    """Whether a signature is specific enough for the cross-MR rule.

    "thornode unreachable" (sim-node-down) is what any mocknet that never came
    up prints, including one the MR broke; a regression diff with no field
    list says only which suite differed. Neither shows a shared cause.
    """
    if not key or k in ("infra-reason", "notrace", "unknown", "sim-node-down"):
        return False
    return not (k == "regression" and key.startswith("diff:") and "#" not in key)


def run(data_dir, since, until, flaky_window_days=30, cross_mr_window_days=3):
    """Classify the window's jobs.

    flaky_window_days: a signature that passed on unchanged code in another
      context within ±this many days of an episode makes it known-flaky.
    cross_mr_window_days: failures with the same signature in other contexts
      within ±this many days count toward shared:cross-mr.
    """
    params = {
        "since": since,
        "until": until,
        "flaky_window_days": flaky_window_days,
        "cross_mr_window_days": cross_mr_window_days,
    }
    jobs = [
        j for j in load_jobs(data_dir).values() if since <= j["created_at"] <= until
    ]
    jobs.sort(key=lambda j: j["id"])
    mrs = {}
    for m in iter_jsonl(f"{data_dir}/mrs.jsonl"):
        mrs[m["iid"]] = m
    by_branch = defaultdict(list)
    for m in mrs.values():
        by_branch[m["source_branch"]].append(m)

    for j in jobs:
        j["ctx"], j["kind"] = context(j, by_branch, until)
        j["head"], j["target"] = head_of(j, j["kind"])

    # merge-train refs are squashed onto the previous train ref (single parent), so
    # the MR head is not recoverable from the commit; use the MR's most recent
    # non-train head before the train run
    nontrain = defaultdict(list)
    for j in jobs:
        if j["ctx"][0] == "mr" and j["kind"] != "train":
            nontrain[j["ctx"]].append((j["created_at"], j["head"]))
    for v in nontrain.values():
        v.sort()
    nt_times = {k: [x[0] for x in v] for k, v in nontrain.items()}
    for j in jobs:
        if j["kind"] == "train":
            v = nontrain.get(j["ctx"])
            if v:
                i = bisect.bisect_left(nt_times[j["ctx"]], j["created_at"]) - 1
                if i >= 0:
                    j["head"] = v[i][1]
            j["target"] = None

    # group attempts on identical code
    groups = defaultdict(list)
    for j in jobs:
        groups[(j["ctx"], j["name"], j["head"])].append(j)

    # signatures for failed attempts
    failed = [j for j in jobs if j["status"] == "failed"]
    log("jobs", len(jobs), "failed", len(failed))
    for j in failed:
        if (
            j["failure_reason"] in INFRA_REASONS_SCRIPTLIKE
            or j["failure_reason"] is None
        ):
            j["sig"] = sig.signature(sig.load(j["id"], data_dir))
        else:
            j["sig"] = ("infra-reason", j["failure_reason"], [])

    # develop outcome per (job name, develop pipeline sha)
    dev_runs = defaultdict(list)  # name -> [(created, sha, outcome, sigs)]
    dev_by_sha = {}
    dg = defaultdict(list)
    for j in jobs:
        if j["ctx"][0] == "develop":
            dg[(j["name"], j["sha"])].append(j)
    for (name, sha), atts in dg.items():
        if any(a["status"] == "success" for a in atts):
            out = "pass"
        elif any(
            a["status"] == "failed"
            and a["failure_reason"] in INFRA_REASONS_SCRIPTLIKE
            and not a["sig"][2]
            for a in atts
        ):
            out = "fail"
        else:
            out = "other"
        sigs = [a["sig"] for a in atts if a["status"] == "failed" and "sig" in a]
        first = min(a["created_at"] for a in atts)
        dev_runs[name].append((first, sha, out, sigs))
        dev_by_sha[(name, sha)] = (out, sigs)
    for v in dev_runs.values():
        v.sort()
    dev_times = {n: [x[0] for x in v] for n, v in dev_runs.items()}

    def develop_state(name, t, target):
        if target and (name, target) in dev_by_sha:
            return dev_by_sha[(name, target)]
        v = dev_runs.get(name)
        if not v:
            return ("none", [])
        i = bisect.bisect_left(dev_times[name], t) - 1
        if i < 0:
            return ("none", [])
        return v[i][2], v[i][3]

    def author(j):
        """Who owns the context: the MR author, else whoever ran the pipeline."""
        if j["ctx"][0] == "mr":
            return (mrs.get(j["ctx"][1]) or {}).get("author") or j.get("user")
        return j.get("user")

    # failures on other MRs and feature branches, by signature; develop, tag
    # and release-branch runs are not "other MRs" (develop has its own rule)
    sig_index = defaultdict(list)
    for j in failed:
        if j["ctx"][0] in ("mr", "branch") and cross_mr_key(*j["sig"][:2]):
            sig_index[(j["name"],) + j["sig"][:2]].append(
                (ts(j["created_at"]), j["ctx"], author(j))
            )

    def shared_authors(j):
        """Distinct other authors whose MRs failed the same way nearby. Stacked
        MRs by one author share a cause, so they count once, and the attempt's
        own author not at all."""
        if not cross_mr_key(*j["sig"][:2]):
            return 0
        t = ts(j["created_at"])
        me = author(j)
        return len(
            {
                a or c
                for (tt, c, a) in sig_index[(j["name"],) + j["sig"][:2]]
                if abs((tt - t).total_seconds()) < cross_mr_window_days * 86400
                and c != j["ctx"]
                and (a is None or a != me)
            }
        )

    # classify each failed attempt; the first rule that fires wins
    for j in failed:
        reason = j["failure_reason"]
        k, key, env = j["sig"]
        g = groups[(j["ctx"], j["name"], j["head"])]
        passed_same = any(a["status"] == "success" for a in g)
        if reason not in INFRA_REASONS_SCRIPTLIKE and reason is not None:
            cat = "infra:runner"
        elif reason == "job_execution_timeout" or "job-timeout" in env:
            cat = "infra:timeout"
        elif "log-limit" in env:
            cat = "infra:log-limit"
        elif env and k in WEAK:
            cat = "infra:external"
        elif passed_same:
            cat = "flaky:passed-on-same-code"
        else:
            dstate, dsigs = develop_state(j["name"], j["created_at"], j["target"])
            if j["ctx"][0] in ("develop",):
                cat = "branch?"
            elif dstate == "fail" and (
                not dsigs or any(sig_matches(j["sig"], s) for s in dsigs)
            ):
                cat = "shared:develop-broken"
            elif shared_authors(j) >= 2:
                cat = "shared:cross-mr"
            else:
                cat = "branch?"
        j["cat"] = cat

    eps = build_episodes(groups)
    tier_episodes(eps, failed, flaky_window_days)
    d = {"jobs": jobs, "failed": failed, "eps": eps, "mrs": mrs, "params": params}
    with open(f"{data_dir}/stage1.pkl", "wb") as f:
        pickle.dump(d, f)
    log("episodes", len(eps))
    log(Counter(j["cat"] for j in failed))
    return d


def build_episodes(groups):
    """Episodes over successive code versions of one context+job: a run of
    versions with a branch? failure, closed by the first version that passes."""
    eps = []
    ctxjob = defaultdict(list)
    for (ctx, name, head), atts in groups.items():
        ctxjob[(ctx, name)].append((min(a["created_at"] for a in atts), head, atts))
    for (ctx, name), heads in ctxjob.items():
        heads.sort()
        cur = None
        for first, head, atts in heads:
            st = {a["status"] for a in atts}
            fails = [a for a in atts if a["status"] == "failed"]
            branch_fails = [a for a in fails if a.get("cat") == "branch?"]
            if "success" in st:
                if cur:
                    cur["fixed_by"] = head
                    cur["fixed_at"] = first
                    eps.append(cur)
                    cur = None
            elif branch_fails:
                if not cur:
                    cur = {
                        "ctx": ctx,
                        "job": name,
                        "heads": [],
                        "attempts": [],
                        "start": first,
                        "fixed_by": None,
                    }
                cur["heads"].append(head)
                cur["attempts"].extend(branch_fails)
        if cur:
            eps.append(cur)
    return eps


def tier_episodes(eps, failed, flaky_window_days):
    # signatures seen failing non-deterministically (passed on the same code)
    flaky_sigs = defaultdict(list)
    for j in failed:
        k, key, env = j["sig"]
        if key and j["cat"].startswith("flaky"):
            flaky_sigs[(j["name"], k, key)].append((ts(j["created_at"]), j["ctx"]))

    for e in eps:
        atts = e["attempts"]
        per_head = Counter(a["head"] for a in atts)
        keys = Counter((a["sig"][0], a["sig"][1]) for a in atts if a["sig"][1])
        kinds = Counter(a["sig"][0] for a in atts)
        e["kinds"] = kinds
        e["kind"] = keys.most_common(1)[0][0][0] if keys else kinds.most_common(1)[0][0]
        e["key"] = keys.most_common(1)[0][0][1] if keys else ""
        t0 = ts(e["start"])
        e["known_flaky"] = bool(e["key"]) and any(
            c != e["ctx"] and abs((tt - t0).total_seconds()) < flaky_window_days * 86400
            for tt, c in flaky_sigs.get((e["job"], e["kind"], e["key"]), [])
        )
        consistent = bool(keys) and keys.most_common(1)[0][1] >= 2
        # Rule order: a lint, compile, format, generate, proto or vuln check is
        # a function of the code, so it is tiered before the known-flaky test; a
        # matching "flaky" key there is an infra blip with the same text, and
        # checking it first sent fixed lint/compile/proto episodes to can't
        # tell. Regression export diffs are deterministic by design but do
        # flake (cross-suite leaks under parallelism), so they come after it.
        det = (
            bool(DETERMINISTIC_JOBS.match(e["job"])) or e["kind"] in DETERMINISTIC_SIGS
        ) and not (e["kind"] in ("unknown", "notrace") and len(atts) < 2)
        diff = e["kind"] == "regression" and e["key"].startswith("diff:")
        gen = generic(e["kind"], e["key"])
        if det:
            tier = "A:deterministic-check"
        elif e["known_flaky"]:
            tier = "C:known-flaky-signature"
        elif diff:
            tier = "A:deterministic-check"
        elif consistent and not gen:
            tier = "A:reproduced-same-signature"
        elif consistent and gen and len(per_head) >= 2:
            tier = "B:reproduced-generic-signature"
        elif not gen:
            tier = "B:single-specific-failure"
        else:
            tier = "C:unreproduced-generic"
        e["tier"] = tier
        e["confidence"] = tier
        e["allow_failure"] = all(a["allow_failure"] for a in atts)
