#!/usr/bin/env python3
"""Compare two unfiltered regtest runs (scripts/perf/lib.sh perf_regtest).

    regtest_diff.py <base-run-dir> <candidate-run-dir>

PASS when every suite's exports and blocks are identical. Any difference FAILS,
tx gas_used and header last_results_hash (which hashes gas_used) included: those
are consensus-visible (old and new binaries would disagree on the next header),
and the perf work must not change behavior. The same goes for a suite missing or
failing on one side, a run that did not complete
(no results summary, or an exit status other than 0/1 matching it) or a
comparison of no files.

Exit status: 0 pass, 1 fail.
"""

import json
import os
import re
import sys

GAS_KEYS = {"gas_used", "last_results_hash"}


def load(path):
    with open(path) as f:
        return json.load(f)


def diff(a, b, path, out):
    if type(a) is not type(b):
        out.append((path, a, b))
    elif isinstance(a, dict):
        for k in sorted(set(a) | set(b)):
            if k not in a or k not in b:
                out.append((f"{path}.{k}", a.get(k), b.get(k)))
            else:
                diff(a[k], b[k], f"{path}.{k}", out)
    elif isinstance(a, list):
        if len(a) != len(b):
            out.append((f"{path}[len]", len(a), len(b)))
        for i, (x, y) in enumerate(zip(a, b)):
            diff(x, y, f"{path}[{i}]", out)
    elif a != b:
        out.append((path, a, b))


def files(root):
    res = {}
    for sub in ("exports", "blocks"):
        d = os.path.join(root, "mnt", sub)
        for dirpath, _, names in os.walk(d):
            for n in names:
                if n.endswith(".json"):
                    full = os.path.join(dirpath, n)
                    res[os.path.relpath(full, os.path.join(root, "mnt"))] = full
    return res


ANSI = re.compile(r"\x1b\[[0-9;]*m")


def summary(root):
    """Failed suites from the run's final results summary, or None if the run
    did not complete (no summary, or an exit status inconsistent with it)."""
    try:
        log = open(os.path.join(root, "regtest.log"), errors="replace").read()
        rc = open(os.path.join(root, "rc")).read().strip()
    except OSError:
        return None
    lines = ANSI.sub("", log).splitlines()
    idx = [i for i, line in enumerate(lines) if re.fullmatch(r"Failed: \d+", line)]
    if not idx:
        return None
    i = idx[-1]
    n = int(lines[i].split()[1])
    failed = set()
    start, end = i + 1, i + 1 + n
    for line in lines[start:end]:
        if not line.startswith("- "):
            return None
        failed.add(line[2:].strip())
    if len(failed) != n or rc != ("1" if n else "0"):
        return None
    return failed


def main():
    base, cand = sys.argv[1], sys.argv[2]
    bfail, cfail = summary(base), summary(cand)
    incomplete = [n for n, f in (("base", bfail), ("candidate", cfail)) if f is None]
    if incomplete:
        print(f"GUARD: FAIL (incomplete regtest run: {', '.join(incomplete)})")
        return 1
    bf, cf = files(base), files(cand)
    if not set(bf) & set(cf):
        print("GUARD: FAIL (no exported files to compare)")
        return 1
    fails, gas = [], []
    for rel in sorted(set(bf) | set(cf)):
        if rel not in bf or rel not in cf:
            fails.append(f"{rel}: only in {'base' if rel in bf else 'candidate'}")
            continue
        out = []
        diff(load(bf[rel]), load(cf[rel]), "", out)
        for path, a, b in out:
            leaf = path.rsplit(".", 1)[-1]
            if leaf in GAS_KEYS:
                gas.append(f"{rel}{path}: {a} -> {b}")
            else:
                fails.append(
                    f"{rel}{path}: {json.dumps(a)[:120]} -> {json.dumps(b)[:120]}"
                )

    for s in sorted(cfail - bfail):
        fails.append(f"suite failed only on candidate: {s}")
    for s in sorted(bfail):
        print(f"note: suite failed on base too: {s}")

    rcs = {}
    for name, root in (("base", base), ("candidate", cand)):
        try:
            rcs[name] = open(os.path.join(root, "rc")).read().strip()
        except OSError:
            rcs[name] = "?"
    print(
        f"regtest rc: base={rcs['base']} candidate={rcs['candidate']}; compared {len(set(bf) & set(cf))} files"
    )

    if gas:
        print(f"CONSENSUS-VISIBLE: {len(gas)} gas_used/last_results_hash differences")
        for g in gas[:50]:
            print("  ", g)
        if len(gas) > 50:
            print(f"   ... {len(gas) - 50} more")
    if fails or gas:
        suites = set()
        for f in fails + gas:
            rel = f.split(":", 1)[0]
            m = re.match(r"(?:exports|blocks)/(.+?)(?:/\d+)?\.json", rel)
            if m:
                suites.add(m.group(1))
            m = re.match(r"suite failed only on candidate: suites/(.+)\.yaml", f)
            if m:
                suites.add(m.group(1))
        print("FAILED_SUITES:", " ".join(sorted(suites)))
        print(
            f"GUARD: FAIL ({len(fails)} behavior differences, {len(gas)} gas differences)"
        )
        for f in fails[:50]:
            print("  ", f)
        if len(fails) > 50:
            print(f"   ... {len(fails) - 50} more")
        return 1
    print("GUARD: PASS (state, events and gas_used identical)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
