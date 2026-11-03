#!/usr/bin/env python3
"""Fetch /thorchain/logs over a block range as JSONL, one entry per line.

Usage: fetch_logs.py <start> <end> [level] [module ...]
Env:   THOR_API (default https://gateway.liquify.com/chain/thorchain_api)

The endpoint caps one response (8MiB) and one scan (64MiB), so wide ranges are
walked in chunks and any chunk that still trips a cap is split in half.
Downstream filtering is jq/grep on the JSONL -- there is no server-side regex.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = os.environ.get("THOR_API", "https://gateway.liquify.com/chain/thorchain_api")
CHUNK = 200  # ponytail: fixed; halving on error handles the busy-block case


def _get(start, end, level, modules):
    q = [("start", start), ("end", end), ("level", level)] + [
        ("modules", m) for m in modules
    ]
    url = f"{API}/thorchain/logs?" + urllib.parse.urlencode(q)
    for attempt in range(4):
        try:
            with urllib.request.urlopen(url, timeout=60) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", "replace")[:500]
            # 503 means both read slots are busy -- the node is telling us to
            # come back, not that the request was wrong.
            if e.code == 503 and attempt < 3:
                time.sleep(int(e.headers.get("Retry-After") or 1))
                continue
            raise RuntimeError(body) from None
    raise RuntimeError("node stayed busy")


def fetch(start, end, level="info", modules=()):
    """Yield log entries for [start, end], splitting any range the node rejects."""
    if start > end:
        return
    try:
        yield from _get(start, end, level, modules)
    except RuntimeError as e:
        # "range exceeds N bytes" / "range spans N blocks": halve and retry
        if start == end or not any(w in str(e) for w in ("exceeds", "spans")):
            raise
        mid = (start + end) // 2
        yield from fetch(start, mid, level, modules)
        yield from fetch(mid + 1, end, level, modules)


def main():
    start, end = max(1, int(sys.argv[1])), int(sys.argv[2])  # height 0 is a 400
    level = sys.argv[3] if len(sys.argv) > 3 else "info"
    modules = sys.argv[4:]
    lowest = None
    n = 0
    for lo in range(start, end + 1, CHUNK):
        for e in fetch(lo, min(lo + CHUNK - 1, end), level, modules):
            lowest = e["height"] if lowest is None else min(lowest, e["height"])
            n += 1
            print(json.dumps(e))
    # The node silently clamps start to its retention floor: report what came back.
    print(f"{n} entries, oldest height {lowest} (asked for {start})", file=sys.stderr)


def demo():
    global _get
    calls = []

    def fake(start, end, level, modules):
        calls.append((start, end))
        if end - start > 1:
            # the node names two different caps; both mean "narrow the range"
            msg = (
                "range exceeds 67108864 bytes of log at height %d" % end
                if start % 2 == 0
                else "range spans %d blocks, at most 10000 per request"
                % (end - start + 1)
            )
            raise RuntimeError(msg)
        return [{"height": h, "msg": "x"} for h in range(start, end + 1)]

    _get = fake
    got = [e["height"] for e in fetch(10, 13)]
    assert got == [10, 11, 12, 13], got
    assert (10, 13) in calls and (10, 11) in calls and (12, 13) in calls, calls

    calls.clear()
    got = [e["height"] for e in fetch(11, 14)]  # odd start -> the "spans" wording
    assert got == [11, 12, 13, 14], got
    print("ok")


if __name__ == "__main__":
    demo() if sys.argv[1:2] == ["--demo"] else main()
