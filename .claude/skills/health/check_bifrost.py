#!/usr/bin/env python3
"""Check bifrost scanner lag across active nodes via port 6040.

Usage: python3 check_bifrost.py [num_nodes] [timeout_secs]

Reads /tmp/tc_active_ips.json (written by parse_nodes.py).
Probes /status/scanner directly (skips /p2pid to avoid rate limiting).
Most nodes (60-80%+) expose scanner data.
"""

import json
import subprocess
import sys
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed

NUM = int(sys.argv[1]) if len(sys.argv) > 1 else 100
TMO = int(sys.argv[2]) if len(sys.argv) > 2 else 10


def curl(url):
    try:
        r = subprocess.run(
            ["curl", "-s", "--max-time", str(TMO), url],
            capture_output=True,
            text=True,
            timeout=TMO + 5,
        )
        return r.stdout.strip() if r.returncode == 0 else None
    except Exception:
        return None


def probe(node):
    nid, ip = node["id"], node["ip"]
    raw = curl(f"http://{ip}:6040/status/scanner")
    if not raw:
        return nid, None, "unreachable"
    try:
        return nid, json.loads(raw), "ok"
    except (json.JSONDecodeError, ValueError):
        return nid, None, "unreachable"


def med(vals):
    s = sorted(vals)
    return s[len(s) // 2] if s else 0


def main():
    with open("/tmp/tc_active_ips.json") as f:
        all_nodes = json.load(f)
    step = max(1, len(all_nodes) // NUM)
    nodes = all_nodes[::step][:NUM]

    print(f"=== BIFROST SCANNER ({len(nodes)} of {len(all_nodes)} nodes) ===")

    results = {}
    with ThreadPoolExecutor(max_workers=min(len(nodes), 10)) as pool:
        futs = {pool.submit(probe, n): n for n in nodes}
        for f in as_completed(futs):
            nid, data, st = f.result()
            results[nid] = (data, st)

    bad = [n for n, (_, s) in results.items() if s == "unreachable"]
    ok = {n: d for n, (d, s) in results.items() if s == "ok" and d}

    print(
        f"  Reachable: {len(results) - len(bad)}/{len(nodes)}, Scanner: {len(ok)}, Unreachable: {len(bad)}"
    )
    if bad:
        print(f"  Unreachable: {', '.join(bad)}")
    if not ok:
        print("  No scanner data")
        return

    chains = defaultdict(list)
    for nid, scanner in ok.items():
        if not isinstance(scanner, dict):
            continue
        for key, info in scanner.items():
            if not isinstance(info, dict) or int(info.get("chain_height", 0)) <= 0:
                continue
            chains[info.get("chain", key)].append(
                (
                    nid,
                    int(info.get("chain_height", 0)),
                    int(info.get("block_scanner_height", 0)),
                    int(info.get("scanner_height_diff", 0)),
                )
            )

    print(
        f"\n  {'Chain':<8} {'Nodes':>5} {'Min':>8} {'Med':>8} {'Max':>8} {'Worst':<20}"
    )
    print(f"  {'-' * 63}")
    for ch in sorted(chains):
        diffs = [d for _, _, _, d in chains[ch] if d >= 0]
        if not diffs:
            print(
                f"  {ch:<8} {len(chains[ch]):>5} {'N/A':>8} {'N/A':>8} {'N/A':>8} (not scanning)"
            )
            continue
        worst = max(chains[ch], key=lambda e: e[3])
        flag = " ***" if max(diffs) > 100 else ""
        print(
            f"  {ch:<8} {len(diffs):>5} {min(diffs):>8} {med(diffs):>8} {max(diffs):>8} {worst[0]:<20}{flag}"
        )

    print("\n  OUTLIERS (>50 or >3x median):")
    found = False
    for ch in sorted(chains):
        diffs = [d for _, _, _, d in chains[ch] if d >= 0]
        if not diffs:
            continue
        thresh = max(50, med(diffs) * 3)
        for nid, c_h, s_h, diff in chains[ch]:
            if diff > thresh:
                found = True
                print(f"    {nid}: {ch} lag={diff} (chain={c_h}, scanner={s_h})")
    if not found:
        print("    None")


if __name__ == "__main__":
    main()
