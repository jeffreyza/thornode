#!/usr/bin/env python3
"""KEEP/REVERT verdict from `benchstat -format csv base.txt new.txt`.

KEEP when, for at least one target benchmark, sec/op or allocs/op improves by at
least --min-gain (default 5%) with p < --alpha (benchstat's own significance
test, default 0.05), and no benchmark's sec/op regresses significantly by more
than --max-regress (default 5%).

A gas/op difference in a tx-level benchmark forces REVERT: gas_used is hashed
into LastResultsHash, so it would change behavior (old and new binaries would
disagree on the next header). In block-level benchmarks (--block-bench, EndBlock
and BeginBlock) gas is not reported in any tx result, so it is informational.
So is gas in API query benchmarks (--query-bench): a query served over the API
runs outside any tx. A contract runs the same queries inside a tx (CtxWASMQuery),
so the contract variants (--contract-bench, the wasm/ sub-benchmarks) are tx gas.

Exit status: 0 KEEP, 1 REVERT.
"""

import argparse
import csv
import re
import sys


def parse(path):
    """Return {unit: {bench: (base, new, delta_pct or None, p)}}."""
    out = {}
    unit = None
    with open(path) as f:
        for row in csv.reader(f):
            if not row or not row[0] and len(row) > 1 and row[1] in ("",):
                continue
            if row[0] == "" and len(row) > 1 and row[1] and "CI" in row:
                unit = row[1]
                out.setdefault(unit, {})
                continue
            if unit is None or row[0] in ("", "geomean") or len(row) < 7:
                continue
            name, base, _, new, _, delta, p = row[:7]
            m = re.search(r"p=([0-9.]+)", p)
            pval = float(m.group(1)) if m else 1.0
            # "~": not significant; "?": no percentage (zero baseline), so
            # no win or regression by delta; base/new still feed the gas check
            d = None
            if delta not in ("~", "", "?"):
                try:
                    d = float(delta.rstrip("%"))
                except ValueError:
                    d = None
            try:
                out[unit][name] = (float(base or 0), float(new or 0), d, pval)
            except ValueError:
                continue
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("csv")
    ap.add_argument(
        "--target",
        default="",
        help="regex of benchmarks the idea targets (default: all)",
    )
    ap.add_argument("--min-gain", type=float, default=5.0)
    ap.add_argument("--max-regress", type=float, default=5.0)
    ap.add_argument("--alpha", type=float, default=0.05)
    ap.add_argument(
        "--block-bench",
        default="EndBlock|BeginBlock",
        help="regex of block-level benchmarks whose gas is not part of gas_used",
    )
    ap.add_argument(
        "--query-bench",
        default=r"^Perf(QuoteSwap|QueryBlock)\b",
        help="regex of API query benchmarks whose gas is not part of gas_used",
    )
    ap.add_argument(
        "--contract-bench",
        default=r"/wasm/",
        help="regex of query benchmarks run as a contract (gas is tx gas_used)",
    )
    args = ap.parse_args()

    data = parse(args.csv)
    target = re.compile(args.target) if args.target else None
    block = re.compile(args.block_bench)
    query = re.compile(args.query_bench)
    contract = re.compile(args.contract_bench)
    wins, regressions, gas, block_gas, query_gas = [], [], [], [], []

    for unit, rows in data.items():
        for name, (base, new, d, p) in rows.items():
            is_target = target is None or target.search(name)
            if unit == "gas/op":
                if base != new:
                    line = f"{name}: gas/op {base:.0f} -> {new:.0f}"
                    if block.search(name):
                        block_gas.append(line)
                    elif query.search(name) and not contract.search(name):
                        query_gas.append(line)
                    else:
                        gas.append(line)
                continue
            if d is None or p >= args.alpha:
                continue
            if unit in ("sec/op", "allocs/op") and is_target and d <= -args.min_gain:
                wins.append(f"{name}: {unit} {d:+.2f}% (p={p:.3f})")
            if unit == "sec/op" and d >= args.max_regress:
                regressions.append(f"{name}: sec/op {d:+.2f}% (p={p:.3f})")

    keep = bool(wins) and not regressions and not gas
    print("VERDICT:", "KEEP" if keep else "REVERT")
    for w in wins:
        print("  win:       ", w)
    for r in regressions:
        print("  regression:", r)
    if not wins:
        print(
            f"  no target benchmark improved >= {args.min_gain}% with p < {args.alpha}"
        )
    if gas:
        print(
            "  CONSENSUS-VISIBLE: tx gas/op changed (REVERT: gas_used must not change):"
        )
        for g in gas:
            print("   ", g)
    else:
        print("  tx gas/op unchanged")
    if block_gas:
        print("  block-level store access changed (not in gas_used, informational):")
        for g in block_gas:
            print("   ", g)
    if query_gas:
        print("  API query store access changed (not in gas_used, informational):")
        for g in query_gas:
            print("   ", g)
    return 0 if keep else 1


if __name__ == "__main__":
    sys.exit(main())
