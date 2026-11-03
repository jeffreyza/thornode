#!/usr/bin/env python3
"""Compare THORChain vault balances vs L1 on-chain at full 1e8 precision.

Usage: echo '<json>' | python3 check_solvency.py <chain>

Input: JSON array of {address, thorchain_amount (1e8 base units), vault_key}
Supported: SOL, BTC, ETH, AVAX, BSC, BASE, DOGE, LTC, BCH, GAIA, XRP, TRON
"""

import json
import subprocess
import sys
import time

TC_DEC = 8

CHAINS = {
    "SOL": {"method": "sol", "rpc": "https://api.mainnet-beta.solana.com", "dec": 9},
    "BTC": {
        "method": "int",
        "url": "https://blockchain.info/q/addressbalance/{a}?confirmations=0",
        "dec": 8,
    },
    "ETH": {"method": "evm", "rpc": "https://ethereum-rpc.publicnode.com", "dec": 18},
    "AVAX": {
        "method": "evm",
        "rpc": "https://avalanche-c-chain-rpc.publicnode.com",
        "dec": 18,
    },
    "BSC": {"method": "evm", "rpc": "https://bsc-rpc.publicnode.com", "dec": 18},
    "BASE": {"method": "evm", "rpc": "https://base-rpc.publicnode.com", "dec": 18},
    "DOGE": {
        "method": "path",
        "url": "https://dogechain.info/api/v1/address/balance/{a}",
        "path": ["balance"],
        "dec": 8,
        "float": True,
    },
    "LTC": {
        "method": "utxo",
        "url": "https://litecoinspace.org/api/address/{a}",
        "dec": 8,
    },
    "BCH": {
        "method": "chair",
        "url": "https://api.blockchair.com/bitcoin-cash/dashboards/address/{a}",
        "dec": 8,
    },
    "GAIA": {
        "method": "path",
        "url": "https://cosmos-rest.publicnode.com/cosmos/bank/v1beta1/balances/{a}/by_denom?denom=uatom",
        "path": ["balance", "amount"],
        "dec": 6,
    },
    "XRP": {"method": "xrp", "rpc": "https://xrplcluster.com", "dec": 6},
    "TRON": {
        "method": "tron",
        "url": "https://api.trongrid.io/v1/accounts/{a}",
        "dec": 6,
        "delay": 1.1,  # trongrid allows 1 req/s without a key
    },
}

ASSET_NAME = {
    "GAIA": "GAIA.ATOM",
    "BSC": "BSC.BNB",
    "BASE": "BASE.ETH",
    "TRON": "TRON.TRX",
}


def curl(url, data=None):
    cmd = ["curl", "-s", "--max-time", "10"]
    if data:
        cmd += ["-X", "POST", "-H", "Content-Type: application/json", "-d", data]
    cmd.append(url)
    return subprocess.run(cmd, capture_output=True, text=True).stdout


def rpc(url, method, params):
    return json.loads(
        curl(
            url,
            json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}),
        )
    )


def balance(addr, chain):
    c = CHAINS[chain]
    m = c["method"]
    try:
        if m == "sol":
            return rpc(c["rpc"], "getBalance", [addr])["result"]["value"], None
        if m == "evm":
            return (
                int(rpc(c["rpc"], "eth_getBalance", [addr, "latest"])["result"], 16),
                None,
            )
        if m == "int":
            return int(curl(c["url"].format(a=addr)).strip()), None
        if m == "path":
            d = json.loads(curl(c["url"].format(a=addr)))
            for k in c["path"]:
                d = d[k]
            return (int(float(d) * 10 ** c["dec"]) if c.get("float") else int(d)), None
        if m == "utxo":
            d = json.loads(curl(c["url"].format(a=addr)))
            s = d.get("chain_stats", {})
            return (
                int(s.get("funded_txo_sum", 0)) - int(s.get("spent_txo_sum", 0)),
                None,
            )
        if m == "xrp":
            r = rpc(
                c["rpc"],
                "account_info",
                [{"account": addr, "ledger_index": "validated"}],
            )
            if r["result"].get("error") == "actNotFound":
                return 0, None
            return int(r["result"]["account_data"]["Balance"]), None
        if m == "tron":
            # an unactivated account returns {"data": []} and a zero balance omits
            # the field entirely, both of which mean 0 -- a real deficit, not an error.
            # A malformed envelope has no "data" key at all and still raises.
            d = json.loads(curl(c["url"].format(a=addr)))["data"]
            return (int(d[0].get("balance", 0)) if d else 0), None
        if m == "chair":
            d = json.loads(curl(c["url"].format(a=addr)))
            return int(d["data"][addr]["address"]["balance"]), None
    except Exception as e:
        return None, str(e)
    return None, "unsupported"


def to_tc(native, dec):
    if dec == TC_DEC:
        return native
    if dec > TC_DEC:
        return native // 10 ** (dec - TC_DEC)
    return native * 10 ** (TC_DEC - dec)


def main():
    chain = sys.argv[1] if len(sys.argv) > 1 else "SOL"
    vaults = json.load(sys.stdin)
    if chain not in CHAINS:
        print(f"Unsupported: {chain}. Use: {', '.join(CHAINS)}")
        sys.exit(1)

    dec = CHAINS[chain]["dec"]
    asset = ASSET_NAME.get(chain, f"{chain}.{chain}")

    hdr = f"{'Vault':<14} {'THORChain (1e8)':>18} {f'L1 (1e{dec})':>20} {'L1→1e8':>18} {'Diff':>14} {'Status'}"
    sep = "-" * len(hdr)
    print(f"=== {chain} SOLVENCY (1e8) ===")
    print(hdr)
    print(sep)

    tot_tc = tot_l1 = errors = 0
    issue = False

    for i, v in enumerate(vaults):
        addr, tc, vk = v["address"], int(v["thorchain_amount"]), v["vault_key"]
        if i and CHAINS[chain].get("delay"):
            time.sleep(CHAINS[chain]["delay"])
        native, err = balance(addr, chain)
        if err:
            # ponytail: errored vaults are left out of both totals so the
            # verdict compares like with like instead of reporting a phantom deficit
            print(f"...{vk:<10} {tc:>18,} {'ERROR':>20} {'':>18} {'':>14} {err}")
            errors += 1
            continue
        l1 = to_tc(native, dec)
        d = l1 - tc
        st = "OK" if d == 0 else (f"+{d}" if d > 0 else f"{d}")
        if d != 0:
            issue = True
        tot_tc += tc
        tot_l1 += l1
        print(f"...{vk:<10} {tc:>18,} {native:>20,} {l1:>18,} {d:>+14,} {st}")

    td = tot_l1 - tot_tc
    verdict = (
        "NO DATA"
        if errors and errors == len(vaults)
        else ("OK" if td >= 0 else "INSOLVENT")
    )
    print(sep)
    print(f"{'TOTAL':<14} {tot_tc:>18,} {'':>20} {tot_l1:>18,} {td:>+14,} {verdict}")
    print(
        f"\n  TC: {tot_tc/1e8:.8f}  L1: {tot_l1/1e8:.8f}  Diff: {td/1e8:.8f} {asset} ({td} base)"
    )
    if errors and errors == len(vaults):
        print(f"  NO DATA: all {errors} vault fetches failed; no verdict")
        return
    if issue and td < 0:
        print(f"  !!! INSOLVENCY: L1 short by {abs(td)} base units")
    elif issue:
        print("  Base-unit discrepancies (all surplus)")
    else:
        print("  Exact match at 1e8")
    if errors:
        print(
            f"  INCOMPLETE: {errors} of {len(vaults)} vaults could not be fetched;"
            " totals exclude them, so the verdict above covers only the rest"
        )


if __name__ == "__main__":
    main()
