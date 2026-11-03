#!/usr/bin/env python3
"""Find which tx or EndBlock step lowered a pool's LP value, from luvi_loss events.

THORNode emits a `luvi_loss` event when a handler run or EndBlock step lowers a
pool's hedged LP equity G (x/thorchain/luvi_guard.go) beyond the computed
allowance (rejected=true when the guard reverted it), or moves LP units
without matching add/withdraw events. This script scans
CometBFT block results for those events and attributes each one to its tx hash
(for tx-level events) or to the EndBlock step named in `scope`.

Usage:
  luvi_forensics.py <rpc_url> <from_height> [to_height] [--pool ASSET] [--tx TXID]
  luvi_forensics.py --parse <block_results.json> [<block.json>] [--pool ASSET]

Output: one line per event, then a JSON summary on stderr.
"""

import argparse
import base64
import hashlib
import json
import sys
import urllib.request

LOSS_TYPE = "luvi_loss"


def _attrs(event):
    """Event attributes as a dict (CometBFT 0.38 returns them as plain strings)."""
    return {a.get("key", ""): a.get("value", "") for a in event.get("attributes") or []}


def tx_hashes(block):
    """Upper-case SHA-256 hashes of a /block result's txs, in block order."""
    txs = ((block.get("result") or block).get("block") or {}).get("data", {}).get(
        "txs"
    ) or []
    return [hashlib.sha256(base64.b64decode(t)).hexdigest().upper() for t in txs]


def extract_luvi_events(height, block_results, hashes=None):
    """luvi_loss events of one block, each tagged with where it was emitted."""
    res = block_results.get("result") or block_results
    found = []
    for i, txr in enumerate(res.get("txs_results") or []):
        for ev in txr.get("events") or []:
            if ev.get("type") == LOSS_TYPE:
                a = _attrs(ev)
                a["height"] = height
                a["source"] = "tx"
                a["tx_hash"] = hashes[i] if hashes and i < len(hashes) else f"tx#{i}"
                found.append(a)
    for ev in res.get("finalize_block_events") or res.get("end_block_events") or []:
        if ev.get("type") == LOSS_TYPE:
            a = _attrs(ev)
            a["height"] = height
            a["source"] = "block"
            a["tx_hash"] = ""
            found.append(a)
    return found


def matches(ev, pool=None, txid=None):
    if pool and ev.get("pool", "").upper() != pool.upper():
        return False
    if txid and txid.upper() not in (
        ev.get("tx_id", "").upper(),
        ev.get("tx_hash", "").upper(),
    ):
        return False
    return True


def describe(ev):
    flags = []
    if ev.get("excess") == "true":
        flags.append("EXCESS")
    if ev.get("rejected") == "true":
        flags.append("REJECTED")
    where = ev["tx_hash"] or "endblock"
    inbound = f" inbound={ev['tx_id']}" if ev.get("tx_id") else ""
    return (
        f"h={ev['height']} pool={ev.get('pool')} scope={ev.get('scope')} kind={ev.get('kind')} "
        f"dG/sqrt(AR)={ev.get('normalized_delta_g')} allowed=-{ev.get('allowed')} "
        f"LP units {ev.get('lp_units_before')}->{ev.get('lp_units_after')} at={where}{inbound}"
        + (f" [{' '.join(flags)}]" if flags else "")
    )


def summarize(events):
    by_scope = {}
    for ev in events:
        k = f"{ev.get('pool')} {ev.get('scope')}"
        s = by_scope.setdefault(
            k, {"count": 0, "excess": 0, "rejected": 0, "heights": []}
        )
        s["count"] += 1
        s["excess"] += ev.get("excess") == "true"
        s["rejected"] += ev.get("rejected") == "true"
        s["heights"].append(ev["height"])
    return {"events": len(events), "by_pool_scope": by_scope}


def _get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "tc-debug"})
    with urllib.request.urlopen(
        req, timeout=30
    ) as r:  # noqa: S310 (fixed https endpoints)
        return json.load(r)


def scan(rpc, start, end, pool=None, txid=None):
    events = []
    for h in range(start, end + 1):
        results = _get(f"{rpc}/block_results?height={h}")
        hashes = tx_hashes(_get(f"{rpc}/block?height={h}"))
        events.extend(
            e for e in extract_luvi_events(h, results, hashes) if matches(e, pool, txid)
        )
    return events


def main(argv):
    p = argparse.ArgumentParser(description="luvi_loss forensics")
    p.add_argument("rpc", nargs="?", help="CometBFT RPC URL")
    p.add_argument("start", nargs="?", type=int)
    p.add_argument("end", nargs="?", type=int)
    p.add_argument("--pool")
    p.add_argument("--tx")
    p.add_argument(
        "--parse",
        nargs="+",
        metavar="FILE",
        help="offline: block_results.json [block.json]",
    )
    a = p.parse_args(argv)

    if a.parse:
        with open(a.parse[0]) as f:
            results = json.load(f)
        hashes = None
        if len(a.parse) > 1:
            with open(a.parse[1]) as f:
                hashes = tx_hashes(json.load(f))
        height = int((results.get("result") or results).get("height", 0))
        events = [
            e
            for e in extract_luvi_events(height, results, hashes)
            if matches(e, a.pool, a.tx)
        ]
    else:
        if not a.rpc or a.start is None:
            p.print_usage(sys.stderr)
            return 2
        end = a.end if a.end is not None else a.start
        if end - a.start > 500:
            print(
                "refusing to scan more than 500 blocks; narrow the range",
                file=sys.stderr,
            )
            return 2
        events = scan(a.rpc.rstrip("/"), a.start, end, a.pool, a.tx)
    for ev in events:
        print(describe(ev))
    if not events:
        print("no luvi_loss events in range")
    print(json.dumps(summarize(events), indent=2), file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
