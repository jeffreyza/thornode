#!/usr/bin/env python3
"""Self-check for balance()'s per-chain response parsing: python3 test_check_solvency.py"""

import check_solvency as cs

CASES = [
    # (chain, canned body, expected native, expects error)
    ("TRON", '{"data": []}', 0, False),  # unactivated account
    ("TRON", '{"data": [{"address": "41ab"}]}', 0, False),  # zero balance omits field
    ("TRON", '{"data": [{"balance": 100000000}]}', 100000000, False),
    (
        "TRON",
        '{"Error": "rate limited"}',
        None,
        True,
    ),  # malformed envelope must fail loud
    ("XRP", '{"result": {"error": "actNotFound"}}', 0, False),
    ("XRP", '{"result": {"account_data": {"Balance": "42"}}}', 42, False),
    ("GAIA", '{"balance": {"amount": "7"}}', 7, False),
    ("GAIA", '{"data": []}', None, True),  # missing key must fail loud
]


def main():
    for chain, body, want, want_err in CASES:
        cs.curl = lambda *a, **k: body
        native, err = cs.balance("addr", chain)
        assert native == want, f"{chain} {body}: got {native}, want {want}"
        assert bool(err) == want_err, f"{chain} {body}: err={err}"
    print(f"ok ({len(CASES)} cases)")


if __name__ == "__main__":
    main()
