#!/usr/bin/env python3
"""Extract vault balances for a chain, output JSON for check_solvency.py.

Usage: cat vaults.json | python3 extract_vault_balances.py <chain> [vault_status]

Output: JSON array of {address, thorchain_amount, vault_key}
"""

import json
import sys

CHAIN_TO_ASSET = {
    "SOL": "SOL.SOL",
    "BTC": "BTC.BTC",
    "ETH": "ETH.ETH",
    "AVAX": "AVAX.AVAX",
    "BSC": "BSC.BNB",
    "BASE": "BASE.ETH",
    "DOGE": "DOGE.DOGE",
    "LTC": "LTC.LTC",
    "BCH": "BCH.BCH",
    "GAIA": "GAIA.ATOM",
    "XRP": "XRP.XRP",
    "TRON": "TRON.TRX",
}

chain = sys.argv[1] if len(sys.argv) > 1 else "SOL"
vault_status = sys.argv[2] if len(sys.argv) > 2 else "RetiringVault"
asset = CHAIN_TO_ASSET.get(chain, f"{chain}.{chain}")

result = []
for v in json.load(sys.stdin):
    if v.get("status") != vault_status:
        continue
    addr = next(
        (a["address"] for a in v.get("addresses", []) if a.get("chain") == chain), None
    )
    amount = next(
        (int(c["amount"]) for c in v.get("coins", []) if c.get("asset") == asset), 0
    )
    if addr and amount > 0:
        result.append(
            {
                "address": addr,
                "thorchain_amount": amount,
                "vault_key": v.get("pub_key", "")[-4:],
            }
        )

if not result:
    print(f"No {vault_status} vaults with {asset} balance > 0", file=sys.stderr)

json.dump(result, sys.stdout)
