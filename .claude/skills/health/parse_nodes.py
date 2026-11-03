#!/usr/bin/env python3
"""Parse /thorchain/nodes for health diagnostics.

Usage: curl -s <url>/thorchain/nodes | python3 parse_nodes.py <major.minor> <current_height>

Outputs:
  - /tmp/tc_pubkey_map.json  (pubkey → operator:address identity map)
  - /tmp/tc_active_ips.json  (active node IPs for bifrost checks)
  - Structured text summary to stdout
"""

import json
import sys


def node_id(n):
    """Format node as <operator_last4>:<address_last4>."""
    return f"{n.get('node_operator_address', '')[-4:]}:{n.get('node_address', '')[-4:]}"


def median(values):
    s = sorted(values)
    return s[len(s) // 2] if s else 0


def main():
    data = json.load(sys.stdin)
    version_filter = sys.argv[1] if len(sys.argv) > 1 else None
    current_height = int(sys.argv[2]) if len(sys.argv) > 2 else 0
    nodes = data if isinstance(data, list) else []

    # Build and save pubkey → identity map
    pk_map = {}
    for n in nodes:
        nid = node_id(n)
        for key_type in ("secp256k1", "ed25519"):
            pk = n.get("pub_key_set", {}).get(key_type, "")
            if pk:
                pk_map[pk] = nid
                pk_map[pk[-4:]] = nid
    with open("/tmp/tc_pubkey_map.json", "w") as f:
        json.dump(pk_map, f)
    print(f"=== PUBKEY MAP: {len(pk_map)} entries → /tmp/tc_pubkey_map.json ===")

    # Status counts (all nodes)
    by_status = {}
    for n in nodes:
        by_status.setdefault(n.get("status", "Unknown"), []).append(n)
    print("\n=== ALL NODES BY STATUS ===")
    for s, ns in sorted(by_status.items()):
        print(f"  {s}: {len(ns)}")
    print(f"  TOTAL: {len(nodes)}")

    # Filter to current major.minor
    if version_filter:
        filtered = [n for n in nodes if n.get("version", "").startswith(version_filter)]
        print(f"\n=== FILTERING TO {version_filter}.x ===")
        print(
            f"  Relevant: {len(filtered)}, Dead/outdated: {len(nodes) - len(filtered)}"
        )
    else:
        filtered = nodes

    active = [n for n in filtered if n.get("status") == "Active"]
    standby = [n for n in filtered if n.get("status") == "Standby"]

    # Standby eligibility (bond >= active minimum)
    active_bonds = [int(n.get("total_bond", 0)) for n in active]
    min_bond = min(active_bonds) if active_bonds else 0
    eligible = [n for n in standby if int(n.get("total_bond", 0)) >= min_bond]

    print("\n=== RELEVANT NODES ===")
    print(
        f"  Active: {len(active)}, Standby eligible: {len(eligible)} (of {len(standby)}, min bond {min_bond / 1e8:,.0f} RUNE)"
    )

    # Version spread
    print("\n=== ACTIVE BY VERSION ===")
    ver = {}
    for n in active:
        v = n.get("version", "?")
        ver[v] = ver.get(v, 0) + 1
    for v, c in sorted(ver.items()):
        print(f"  {v}: {c}")

    # Leaving
    leaving = [
        n for n in active if n.get("requested_to_leave") or n.get("forced_to_leave")
    ]
    if leaving:
        print("\n=== LEAVING ===")
        for n in leaving:
            print(
                f"  {node_id(n)}: req={n.get('requested_to_leave')}, forced={n.get('forced_to_leave')}"
            )

    # Jails (only active, only currently jailed)
    print(f"\n=== JAILED (release_height > {current_height}) ===")
    jailed = []
    for n in active:
        rh = int(n.get("jail", {}).get("release_height", 0))
        if rh > current_height > 0:
            jailed.append((node_id(n), rh, n.get("jail", {}).get("reason", "")))
    print(f"  {len(jailed)} of {len(active)} active")
    for nid, rh, reason in sorted(jailed, key=lambda x: -x[1])[:10]:
        left = rh - current_height
        print(f"  {nid}: {left} blocks / {left * 6 / 60:.0f}min left — {reason}")

    # Slash points
    print("\n=== SLASH POINTS (top 10) ===")
    slashes = [int(n.get("slash_points", 0)) for n in active]
    med = median(slashes)
    print(
        f"  Median: {med}, Avg: {sum(slashes) / len(slashes):.0f}"
        if slashes
        else "  No data"
    )
    for n in sorted(active, key=lambda n: -int(n.get("slash_points", 0)))[:10]:
        sp = int(n.get("slash_points", 0))
        ratio = sp / med if med else 0
        flag = " ***" if ratio > 3 else ""
        print(f"  {node_id(n)}: {sp} ({ratio:.1f}x){flag}")

    # Bond stats
    print("\n=== BOND (active) ===")
    if active_bonds:
        print(
            f"  Min: {min(active_bonds) / 1e8:>12,.0f}  Median: {median(active_bonds) / 1e8:>12,.0f}  Max: {max(active_bonds) / 1e8:>12,.0f}  Total: {sum(active_bonds) / 1e8:>12,.0f} RUNE"
        )

    # Save IPs for bifrost checks
    ips = [
        {"id": node_id(n), "ip": n["ip_address"], "version": n.get("version", "")}
        for n in active
        if n.get("ip_address")
    ]
    with open("/tmp/tc_active_ips.json", "w") as f:
        json.dump(ips, f)


if __name__ == "__main__":
    main()
