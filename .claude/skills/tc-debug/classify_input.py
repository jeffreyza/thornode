#!/usr/bin/env python3
"""Classify debug input as transaction hash, vault pubkey, address, or description.

Usage: python3 classify_input.py "<input>"

Output JSON:
{
  "type": "tx_hash" | "vault_pubkey" | "thor_address" | "chain_address" | "description",
  "value": "<cleaned input>",
  "chain_hint": "BTC" | "ETH" | ... | null,
  "details": "..."
}
"""

import json
import re
import sys


def extract_from_url(inp):
    """Try to extract a tx hash or address from a block explorer URL."""
    # Common explorer URL patterns
    patterns = [
        # THORChain explorers: /tx/HASH
        r"/tx/([a-fA-F0-9]{64})",
        # Etherscan-style: /tx/0xHASH
        r"/tx/(0x[a-fA-F0-9]{64})",
        # Etherscan address: /address/0xADDR
        r"/address/(0x[a-fA-F0-9]{40})",
        # Mempool.space: /tx/HASH
        r"mempool\.space/tx/([a-fA-F0-9]{64})",
        # Viewblock/runescan: /tx/HASH
        r"/tx/([A-F0-9]{64})",
        # Generic: last path segment that looks like a hash
        r"/([a-fA-F0-9]{64})(?:\?|$|#)",
    ]
    for pat in patterns:
        m = re.search(pat, inp)
        if m:
            return m.group(1)
    return None


def classify(inp):
    inp = inp.strip()

    # If input looks like a URL, try to extract the hash/address
    if inp.startswith("http://") or inp.startswith("https://"):
        extracted = extract_from_url(inp)
        if extracted:
            inp = extracted
        else:
            return {
                "type": "description",
                "value": inp,
                "chain_hint": None,
                "details": "URL provided but could not extract tx hash or address",
            }

    # THORChain vault pubkey (tthorpub for testnet, thorpub for mainnet)
    if re.match(r"^(t?thorpub[a-zA-Z0-9]{50,})$", inp):
        return {
            "type": "vault_pubkey",
            "value": inp,
            "chain_hint": None,
            "details": "THORChain vault public key",
        }

    # THORChain address (thor1... or tthor1...)
    if re.match(r"^(t?thor1[a-z0-9]{38,})$", inp):
        return {
            "type": "thor_address",
            "value": inp,
            "chain_hint": "THOR",
            "details": "THORChain address - could be user or vault",
        }

    # Hex transaction hash (32 or 64 hex chars, with optional 0x prefix)
    hex_match = re.match(r"^(0x)?([a-fA-F0-9]{64})$", inp)
    if hex_match:
        return {
            "type": "tx_hash",
            "value": hex_match.group(2).upper(),
            "chain_hint": "ETH" if inp.startswith("0x") else None,
            "details": "64-char hex hash - likely transaction ID",
        }

    # Shorter hex (some chains use different lengths)
    hex_short = re.match(r"^(0x)?([a-fA-F0-9]{40,66})$", inp)
    if hex_short:
        val = hex_short.group(2).upper()
        if len(hex_short.group(2)) == 40 and inp.startswith("0x"):
            return {
                "type": "chain_address",
                "value": inp,
                "chain_hint": "EVM",
                "details": "EVM address (ETH/BSC/AVAX/BASE)",
            }
        return {
            "type": "tx_hash",
            "value": val,
            "chain_hint": None,
            "details": f"{len(val)}-char hex - likely transaction hash",
        }

    # Bitcoin addresses
    if re.match(r"^(bc1|tb1)[a-zA-HJ-NP-Z0-9]{25,87}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "BTC",
            "details": "Bitcoin bech32 address",
        }
    if re.match(r"^[13][a-km-zA-HJ-NP-Z1-9]{25,34}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "BTC",
            "details": "Bitcoin legacy address",
        }

    # Litecoin addresses
    if re.match(r"^(ltc1|tltc1)[a-zA-HJ-NP-Z0-9]{25,87}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "LTC",
            "details": "Litecoin bech32 address",
        }
    if re.match(r"^[LM][a-km-zA-HJ-NP-Z1-9]{25,34}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "LTC",
            "details": "Litecoin address",
        }

    # Dogecoin addresses
    if re.match(r"^D[a-km-zA-HJ-NP-Z1-9]{25,34}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "DOGE",
            "details": "Dogecoin address",
        }

    # Bitcoin Cash addresses
    if re.match(r"^(bitcoincash:)?q[a-z0-9]{41}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "BCH",
            "details": "Bitcoin Cash address",
        }

    # Cosmos/GAIA addresses
    if re.match(r"^cosmos1[a-z0-9]{38,}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "GAIA",
            "details": "Cosmos Hub address",
        }

    # XRP classic addresses: r + 24-34 base58 (no 0, O, I, l)
    if re.match(r"^r[1-9A-HJ-NP-Za-km-z]{24,34}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "XRP",
            "details": "XRP address",
        }

    # TRON addresses: T + 33 base58 (must precede the Solana catch-all)
    if re.match(r"^T[1-9A-HJ-NP-Za-km-z]{33}$", inp):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "TRON",
            "details": "TRON address",
        }

    # Solana addresses (base58, 32-44 chars)
    if re.match(r"^[1-9A-HJ-NP-Za-km-z]{32,44}$", inp) and not re.match(
        r"^[a-fA-F0-9]+$", inp
    ):
        return {
            "type": "chain_address",
            "value": inp,
            "chain_hint": "SOL",
            "details": "Possible Solana address (base58)",
        }

    # If none of the above matched, it's a description
    return {
        "type": "description",
        "value": inp,
        "chain_hint": None,
        "details": "Free-text description of issue to investigate",
    }


def main():
    if len(sys.argv) < 2:
        print(json.dumps({"error": "No input provided"}))
        sys.exit(1)

    inp = " ".join(sys.argv[1:])
    result = classify(inp)
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
