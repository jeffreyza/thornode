#!/usr/bin/env python3
"""Parse /thorchain/vaults/asgard for health diagnostics.

Usage: curl -s <url>/thorchain/vaults/asgard | python3 parse_vaults.py <current_height>

Vaults identified by last 4 chars of pub_key.
"""

import json
import sys


def main():
    data = json.load(sys.stdin)
    current_height = int(sys.argv[1]) if len(sys.argv) > 1 else 0
    vaults = data if isinstance(data, list) else []

    by_status = {}
    for v in vaults:
        by_status.setdefault(v.get("status", "Unknown"), []).append(v)

    print("=== VAULTS BY STATUS ===")
    for s, vs in sorted(by_status.items()):
        print(f"  {s}: {len(vs)}")

    for status in ("RetiringVault", "ActiveVault"):
        vs = by_status.get(status, [])
        if not vs:
            continue
        print(f"\n=== {status.upper()} DETAILS ===")
        for v in vs:
            pk4 = v.get("pub_key", "")[-4:]
            since = int(v.get("status_since", 0))
            blocks_ago = current_height - since if current_height else 0
            hours_ago = blocks_ago * 6 / 3600 if blocks_ago else 0

            chains = [a.get("chain", "") for a in v.get("addresses", [])]
            nonzero = sorted(
                [
                    (c["asset"], int(c["amount"]))
                    for c in v.get("coins", [])
                    if int(c.get("amount", 0)) > 0
                ],
                key=lambda x: -x[1],
            )

            print(f"\n  Vault ...{pk4}:")
            print(
                f"    Since: block {since}"
                + (
                    f" ({blocks_ago} blocks / {hours_ago:.1f}h ago)"
                    if blocks_ago
                    else ""
                )
            )
            print(f"    Chains: {', '.join(chains)}")

            if nonzero:
                print(f"    Balances ({len(nonzero)} assets):")
                for asset, amount in nonzero[:20]:
                    print(f"      {asset}: {amount / 1e8:,.8f} ({amount} base)")
                if len(nonzero) > 20:
                    print(f"      ... and {len(nonzero) - 20} more")
            else:
                print("    Balances: empty")

            # L1 addresses for retiring vaults with remaining balances
            if status == "RetiringVault" and nonzero:
                remaining = {a.split(".")[0] for a, _ in nonzero if "." in a}
                if remaining:
                    print("    L1 addresses:")
                    for a in v.get("addresses", []):
                        if a.get("chain") in remaining:
                            print(f"      {a['chain']}: {a.get('address', '')}")


if __name__ == "__main__":
    main()
