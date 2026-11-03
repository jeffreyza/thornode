"""Report stage: attach verdicts to episodes and failed attempts, print the report."""

import re
from collections import Counter, defaultdict
from datetime import datetime

HYGIENE = re.compile(
    r"^(lint|proto-lint|proto-check-formatting|proto-format|generate|secret_detection|semgrep|codespell|fork|text[-_]files.*|check-.*|docs.*|pages|wasm-permissions-validation)$"
)
VULN = re.compile(r"^govulncheck$")
APICOMPAT = re.compile(r"^proto-check-breaking$")
BUILD = re.compile(r"^(build-.*|docker-.*|mirror-ci-images)$")

REAL = "REAL: MR's own change, fixed by a later push"
UNRESOLVED = "unresolved: never went green (abandoned/open)"


def job_group(job, kind):
    if kind == "compile":
        return "build break"
    if HYGIENE.match(job):
        return "lint / format / generated files"
    if VULN.match(job):
        return "dependency vulns (govulncheck)"
    if APICOMPAT.match(job):
        return "proto API breaking change"
    if BUILD.match(job):
        return "build / image"
    return "tests"


def fix_type(e, idf):
    """What the MR changed between its last red and first green version."""
    if not e["fixed_by"]:
        return "unresolved"
    r = idf.get((e["heads"][-1], e["fixed_by"]))
    if not r or r["status"] != "ok":
        return "unknown"
    if not r["files"] or r["tree_same"]:
        return "no-branch-change"
    cl = set(r["classes"])
    if "code" in cl:
        return "production code"
    if "generated" in cl:
        return "generated code"
    if cl <= {"golden"}:
        return "goldens only"
    if cl <= {"golden", "test"}:
        return "tests/goldens only"
    if "ci/build" in cl:
        return "CI/build config"
    return "docs only"


def windows(d):
    """(known-flaky window, cross-MR window) in days, as the classify run used."""
    p = d.get("params", {})
    return p.get("flaky_window_days", 30), p.get("cross_mr_window_days", 3)


def att_cat(j, att_ep, shared_label):
    c = j["cat"]
    if c.startswith("infra"):
        return "infrastructure / external service"
    if c.startswith("flaky"):
        return "flaky: passed on identical code"
    if c.startswith("shared"):
        return shared_label
    e = att_ep.get(j["id"])
    if not e:
        return "other"
    if e["fix"] == "no-branch-change":
        return "not this MR: passed after rebase/no-op push"
    if e["fix"] in ("unresolved", "unknown"):
        return UNRESOLVED
    if e["tier"].startswith("C"):
        return "can't tell: went green after a push, but failure looks flaky/generic"
    return REAL


def enrich(d, idf):
    for e in d["eps"]:
        e["fix"] = fix_type(e, idf)
        e["group"] = job_group(e["job"], e["kind"])
        e["resolved"] = e["fix"] not in ("no-branch-change", "unresolved", "unknown")
        e["real"] = e["resolved"] and not e["tier"].startswith("C")
        if e["real"]:
            e["verdict"] = "REAL"
        elif e["fix"] == "no-branch-change":
            e["verdict"] = "not-this-MR(no-change)"
        elif not e["resolved"]:
            e["verdict"] = "unresolved"
        else:
            e["verdict"] = "cant-tell"
    att_ep = {}
    for e in d["eps"]:
        for a in e["attempts"]:
            att_ep[a["id"]] = e
    shared = (
        "not this MR: develop broken, or same failure on MRs by 2+ other "
        f"authors within ±{windows(d)[1]:g}d"
    )
    for j in d["failed"]:
        j["verdict"] = att_cat(j, att_ep, shared)


def pct(a, b):
    return f"{100 * a / b:5.1f}%" if b else "   - "


def quarter(s):
    return f"{s[:4]}Q{(int(s[5:7]) - 1) // 3 + 1}"


def _ts(x):
    return datetime.fromisoformat(x.replace("Z", "+00:00"))


def render(d):
    jobs, failed, eps, mrs = d["jobs"], d["failed"], d["eps"], d["mrs"]
    out = []
    W = out.append
    mrjobs = [j for j in jobs if j["ctx"][0] == "mr"]
    mrfail = [j for j in failed if j["ctx"][0] == "mr" and not j["allow_failure"]]
    first = min(j["created_at"] for j in jobs)[:10]
    last = max(j["created_at"] for j in jobs)[:10]
    pipes = {j["pid"] for j in jobs}
    mrpipes = {j["pid"] for j in mrjobs}
    mr_with_ci = {j["ctx"][1] for j in mrjobs}
    finished = sum(
        1
        for j in mrjobs
        if not j["allow_failure"] and j["status"] in ("success", "failed")
    )

    W(
        f"\nTHORNode CI: how often does a red job mean a real problem?   {first} -> {last}\n"
    )
    W("=" * 78 + "\n")
    W(
        f"{len(jobs):,} job attempts in {len(pipes):,} pipelines; {len(mrjobs):,} attempts / "
        f"{len(mrpipes):,} pipelines on {len(mr_with_ci):,} MRs\n"
    )
    W(
        f"Failed blocking attempts on MRs: {len(mrfail):,} ({pct(len(mrfail), finished)} "
        "of finished blocking attempts)\n\n"
    )

    W("1. WHY MR JOB ATTEMPTS FAILED (blocking jobs only)\n")
    c = Counter(j["verdict"] for j in mrfail)
    for k, v in c.most_common():
        W(f"   {v:7,}  {pct(v, len(mrfail))}  {k}\n")
    hours = Counter()
    for j in mrfail:
        hours[j["verdict"]] += (j["duration"] or 0) / 3600
    W(
        "   runner hours burned by failed attempts: "
        + ", ".join(f"{k.split(':')[0]} {v:,.0f}h" for k, v in hours.most_common())
        + "\n"
    )
    red = defaultdict(set)
    for j in mrfail:
        red[j["pid"]].add(j["verdict"] == REAL)
    n_real = sum(1 for v in red.values() if True in v)
    W(
        f"   MR pipelines with a failed blocking attempt: {len(red):,}; with at least one "
        f"REAL failure: {n_real:,} ({pct(n_real, len(red)).strip()})\n"
    )
    sub = Counter(j["cat"] for j in mrfail)
    W(
        "   infra detail: "
        + ", ".join(
            f"{k.split(':')[1]} {v:,}" for k, v in sub.items() if k.startswith("infra")
        )
        + "\n"
    )
    W(
        "   shared detail: "
        + ", ".join(
            f"{k.split(':')[1]} {v:,}" for k, v in sub.items() if k.startswith("shared")
        )
        + "\n\n"
    )

    mreps = [e for e in eps if e["ctx"][0] == "mr" and not e["allow_failure"]]
    real = [e for e in mreps if e["real"]]
    W(
        "2. REAL ISSUES CAUGHT (one 'catch' = a job going red on an MR because of its own change,\n"
        "   counted once however many pushes it took to go green again)\n"
    )
    n_unres = sum(1 for e in mreps if e["fix"] in ("unresolved", "unknown"))
    n_dism = sum(1 for e in mreps if e["fix"] == "no-branch-change")
    W(
        f"   {len(real):,} catches on {len({e['ctx'][1] for e in real}):,} MRs  "
        f"(+{n_unres:,} unresolved, {n_dism:,} dismissed: went green with no change by the MR)\n\n"
    )
    W(
        f"   {'what caught it':34} {'catches':>8} {'prod code':>10} {'tests/golden':>13} "
        f"{'CI/build':>9} {'other':>6}\n"
    )
    gc = defaultdict(Counter)
    for e in real:
        gc[e["group"]][e["fix"]] += 1
    for g, cc in sorted(gc.items(), key=lambda x: -sum(x[1].values())):
        n = sum(cc.values())
        W(
            f"   {g:34} {n:8,} {cc['production code']:10,} "
            f"{cc['tests/goldens only'] + cc['goldens only']:13,} {cc['CI/build config']:9,} "
            f"{cc['generated code'] + cc['docs only']:6,}\n"
        )
    W(
        "   (fix columns = what the MR changed between the red and the next green version; 'prod code'\n"
        "    means the fix touched non-test source, possibly alongside other changes)\n"
    )
    conf = Counter(e["tier"] for e in real)
    W("   evidence: " + "; ".join(f"{k} {v:,}" for k, v in sorted(conf.items())) + "\n")
    amb = [e for e in mreps if e["resolved"] and e["tier"].startswith("C")]
    W(
        f"   not counted (can't tell): {len(amb):,} episodes - "
        + ", ".join(
            f"{k} {v}" for k, v in Counter(e["job"] for e in amb).most_common(5)
        )
        + "\n"
    )
    W(
        "   (can't tell = tier C: the same job and signature passed on unchanged code in another\n"
        f"    context within ±{windows(d)[0]:g}d, or a generic signature seen on one version only)\n\n"
    )

    W("3. PER JOB (MR pipelines, blocking attempts)\n")
    W(
        f"   {'job':30} {'runs':>7} {'failed':>7} {'fail%':>6} {'real':>6} {'catches':>8} "
        f"{'signal%':>8} {'noise':>6} {'runs/catch':>10}\n"
    )
    runs = Counter(
        j["name"]
        for j in mrjobs
        if j["status"] in ("success", "failed") and not j["allow_failure"]
    )
    fails = Counter(j["name"] for j in mrfail)
    realatt = Counter(j["name"] for j in mrfail if j["verdict"] == REAL)
    noise = Counter(j["name"] for j in mrfail if j["verdict"] not in (REAL, UNRESOLVED))
    catches = Counter(e["job"] for e in real)
    for n, r in runs.most_common():
        if r < 200:
            continue
        per = f"{r / catches[n]:10.0f}" if catches[n] else "         -"
        W(
            f"   {n[:30]:30} {r:7,} {fails[n]:7,} {pct(fails[n], r)} {realatt[n]:6,} "
            f"{catches[n]:8,} {pct(realatt[n], fails[n]):>8} {noise[n]:6,} {per}\n"
        )
    W(
        "   real = failed attempts attributed to the MR's own change; signal% = real / failed\n\n"
    )

    W("4. PER MERGE REQUEST (MRs merged in the window that ran CI)\n")
    merged = [
        m
        for m in mrs.values()
        if m["state"] == "merged"
        and m["merged_at"]
        and m["merged_at"][:10] >= first
        and m["iid"] in mr_with_ci
    ]
    by_mr = defaultdict(list)
    for e in real:
        by_mr[e["ctx"][1]].append(e)
    any_real = [m for m in merged if by_mr.get(m["iid"])]
    test_real = [
        m for m in merged if any(e["group"] == "tests" for e in by_mr.get(m["iid"], []))
    ]
    bug_real = [
        m
        for m in merged
        if any(
            e["group"] == "tests" and e["fix"] == "production code"
            for e in by_mr.get(m["iid"], [])
        )
    ]
    mr_noise = Counter()
    for j in mrfail:
        if j["verdict"] not in (REAL, UNRESOLVED):
            mr_noise[j["ctx"][1]] += 1
    n_noisy = sum(1 for m in merged if mr_noise[m["iid"]])
    nm = len(merged)
    W(f"   merged MRs with CI:                                   {nm:,}\n")
    W(
        f"   ... CI caught anything real (incl. lint/format):      {len(any_real):,} "
        f"({pct(len(any_real), nm).strip()})\n"
    )
    W(
        f"   ... a test job caught something real:                 {len(test_real):,} "
        f"({pct(len(test_real), nm).strip()})\n"
    )
    W(
        f"   ... a test job caught it AND the fix changed prod code: {len(bug_real):,} "
        f"({pct(len(bug_real), nm).strip()})\n"
    )
    W(
        f"   ... hit at least one noise failure (infra/flaky/shared): {n_noisy:,} "
        f"({pct(n_noisy, nm).strip()})\n"
    )
    W(
        f"   noise failures per merged MR: {sum(mr_noise[m['iid']] for m in merged) / max(1, nm):.2f}   "
        f"real catches per merged MR: {sum(len(by_mr.get(m['iid'], [])) for m in merged) / max(1, nm):.2f}\n\n"
    )

    W("5. BY QUARTER (MR pipelines)\n")
    W(
        f"   {'quarter':8} {'MR pipes':>9} {'failed':>7} {'infra':>6} {'flaky':>6} {'shared':>7} "
        f"{'real':>6} {'signal%':>8} {'catches':>8} {'bugs*':>6}\n"
    )
    qp = Counter(
        quarter(j["created_at"]) for j in {j["pid"]: j for j in mrjobs}.values()
    )
    qc = defaultdict(Counter)
    for j in mrfail:
        q = quarter(j["created_at"])
        qc[q]["failed"] += 1
        qc[q][j["verdict"].split(":")[0].split(" ")[0]] += 1
    qe = Counter(quarter(e["start"]) for e in real)
    qb = Counter(
        quarter(e["start"])
        for e in real
        if e["group"] == "tests" and e["fix"] == "production code"
    )
    for k in sorted(qp):
        cc = qc[k]
        W(
            f"   {k:8} {qp[k]:9,} {cc['failed']:7,} {cc['infrastructure']:6,} {cc['flaky']:6,} "
            f"{cc['not']:7,} {cc['REAL']:6,} {pct(cc['REAL'], cc['failed']):>8} {qe[k]:8,} {qb[k]:6,}\n"
        )
    W("   bugs* = test-job catches whose fix touched production code\n\n")

    W("6. AFTER MERGE: develop pipelines\n")
    deveps = [e for e in eps if e["ctx"][0] == "develop" and not e["allow_failure"]]
    devreal = [e for e in deveps if not e["tier"].startswith("C")]
    W(
        f"   red on every attempt until a later develop commit: {len(deveps):,} episodes; "
        f"with a specific, non-flaky signature: {len(devreal):,}\n"
    )
    for n, v in Counter(e["job"] for e in devreal).most_common(8):
        W(f"      {n:30} {v}\n")
    dur = sorted(
        (_ts(e["fixed_at"]) - _ts(e["start"])).total_seconds() / 3600
        for e in devreal
        if e["fixed_by"] and e.get("fixed_at")
    )
    if dur:
        W(
            f"   time develop stayed red (those episodes): median {dur[len(dur) // 2]:.1f}h, "
            f"p90 {dur[int(len(dur) * 0.9)]:.1f}h\n"
        )

    W("\n7. COST: runner hours per job on MR pipelines vs real catches\n")
    W(
        f"   {'job':30} {'hours':>8} {'noise hrs':>10} {'catches':>8} {'hrs/catch':>10}\n"
    )
    jh = Counter()
    jn = Counter()
    for j in mrjobs:
        jh[j["name"]] += (j["duration"] or 0) / 3600
    for j in mrfail:
        if j["verdict"] not in (REAL, UNRESOLVED):
            jn[j["name"]] += (j["duration"] or 0) / 3600
    for n, h in jh.most_common(12):
        per = f"{h / catches[n]:10.1f}" if catches[n] else "         -"
        W(f"   {n[:30]:30} {h:8,.0f} {jn[n]:10,.0f} {catches[n]:8,} {per}\n")

    W("\n8. EXAMPLES: recent test-job catches whose fix changed production code\n")
    ex = sorted(
        (
            e
            for e in real
            if e["group"] == "tests"
            and e["fix"] == "production code"
            and e["tier"].startswith("A")
        ),
        key=lambda e: e["start"],
        reverse=True,
    )[:15]
    for e in ex:
        m = mrs.get(e["ctx"][1], {})
        what = (e["kind"] + ": " + e["key"])[:60]
        W(
            f"   {e['start'][:10]} !{e['ctx'][1]:<5} {e['job'][:22]:22} {what:60} | "
            f"{(m.get('title') or '')[:50]}\n"
        )
    return "".join(out)
