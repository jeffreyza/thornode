#!/usr/bin/env python3
"""Parse /thorchain/tx/status/{txid} response and produce a diagnostic summary.

Usage: curl -s <url>/thorchain/tx/status/<txid> | python3 parse_tx_status.py [current_height]

Output: Human-readable diagnostic summary + JSON analysis to stderr.
"""

import json
import sys


def parse_stage(stages, current_height):
    """Analyze transaction stages and return diagnostic info."""
    diag = {
        "phase": "unknown",
        "stuck": False,
        "stuck_at": None,
        "details": [],
    }

    if not stages:
        diag["phase"] = "not_found"
        diag["details"].append("No stage data - transaction may not exist on THORChain")
        return diag

    # Stage 1: Inbound Observed
    obs = stages.get("inbound_observed", {})
    if not obs.get("completed", False):
        pre = obs.get("pre_confirmation_count", 0)
        final = obs.get("final_count", 0)
        diag["phase"] = "inbound_observation"
        if pre == 0 and final == 0:
            diag["details"].append("NO nodes have observed this transaction yet")
            diag["stuck"] = True
            diag["stuck_at"] = "inbound_observation"
        else:
            diag["details"].append(
                f"Observation in progress: {pre} pre-confirm, {final} final signers"
            )
        return diag

    diag["details"].append("Inbound observed (consensus reached)")

    # Stage 2: Confirmation counting
    conf = stages.get("inbound_confirmation_counted", {})
    if conf and not conf.get("completed", False):
        diag["phase"] = "confirmation_counting"
        remaining = conf.get("remaining_confirmation_seconds", 0)
        chain = conf.get("chain", "?")
        ext_obs = conf.get("external_observed_height", 0)
        ext_delay = conf.get("external_confirmation_delay_height", 0)
        diag["details"].append(
            f"Waiting for {chain} confirmations: observed at {ext_obs}, "
            f"need {ext_delay}, ~{remaining}s remaining"
        )
        return diag

    if conf and conf.get("completed"):
        diag["details"].append("Confirmations counted")

    # Stage 3: Inbound finalised
    fin = stages.get("inbound_finalised", {})
    if not fin.get("completed", False):
        diag["phase"] = "inbound_finalisation"
        diag["details"].append("Inbound not yet finalised despite observations")
        return diag

    diag["details"].append("Inbound finalised")

    # Stage 4: Swap status
    swap = stages.get("swap_status", {})
    if swap and swap.get("pending", False):
        diag["phase"] = "swap_pending"
        streaming = swap.get("streaming")
        if streaming:
            count = streaming.get("count", 0)
            quantity = streaming.get("quantity", 0)
            interval = streaming.get("interval", 0)
            diag["details"].append(
                f"Streaming swap in progress: {count}/{quantity} swaps "
                f"(interval: {interval} blocks)"
            )
        else:
            diag["details"].append("Swap is pending in the queue")
        return diag

    swap_fin = stages.get("swap_finalised", {})
    if swap_fin and swap_fin.get("completed"):
        diag["details"].append("Swap completed")

    # Stage 5: Outbound delay
    delay = stages.get("outbound_delay", {})
    if delay and not delay.get("completed", False):
        diag["phase"] = "outbound_delay"
        remaining_blocks = delay.get("remaining_delay_blocks", 0)
        remaining_secs = delay.get("remaining_delay_seconds", 0)
        diag["details"].append(
            f"Outbound delayed: {remaining_blocks} blocks (~{remaining_secs}s) remaining"
        )
        return diag

    if delay and delay.get("completed"):
        diag["details"].append("Outbound delay passed")

    # Stage 6: Outbound signed
    STUCK_THRESHOLD_BLOCKS = 50  # ~5 min — signing normally takes less than this
    signed = stages.get("outbound_signed", {})
    if signed:
        if not signed.get("completed", False):
            diag["phase"] = "outbound_signing"
            sched_height = signed.get("scheduled_outbound_height", 0)
            blocks_since = signed.get("blocks_since_scheduled", 0)

            # Compute age from either source
            age = 0
            if current_height and sched_height:
                age = current_height - sched_height
            elif blocks_since:
                age = blocks_since

            if age > STUCK_THRESHOLD_BLOCKS:
                diag["stuck"] = True
                diag["stuck_at"] = "outbound_signing"
                diag["details"].append(
                    f"STUCK: Outbound scheduled at height {sched_height}, "
                    f"{age} blocks ago (~{age * 6}s). Not yet signed/broadcast."
                )
            elif age > 0:
                diag["details"].append(
                    f"Outbound scheduled {age} blocks ago (~{age * 6}s) — "
                    f"signing in progress (not yet stuck, threshold is {STUCK_THRESHOLD_BLOCKS} blocks)"
                )
            else:
                diag["details"].append("Outbound just scheduled, awaiting signing")
        else:
            diag["phase"] = "completed"
            diag["details"].append("Outbound signed and broadcast")
    else:
        # No outbound stage at all
        if swap_fin and swap_fin.get("completed"):
            diag["phase"] = "completed"
            diag["details"].append(
                "Transaction completed (no outbound needed or already done)"
            )

    return diag


def main():
    raw = sys.stdin.read()
    try:
        data = json.loads(raw)
    except (json.JSONDecodeError, ValueError):
        print(
            f"Failed to parse tx/status response. Raw input: {raw[:200]}",
            file=sys.stderr,
        )
        sys.exit(1)

    current_height = int(sys.argv[1]) if len(sys.argv) > 1 else None

    # Basic tx info
    tx = data.get("tx")
    planned = data.get("planned_out_txs")
    out_txs = data.get("out_txs")
    stages = data.get("stages", {})

    print("=" * 60)
    print("TRANSACTION STATUS ANALYSIS")
    print("=" * 60)

    if tx:
        print(f"\nInbound TX: {tx.get('id', 'N/A')}")
        print(f"  Chain:  {tx.get('chain', 'N/A')}")
        print(f"  From:   {tx.get('from_address', 'N/A')}")
        print(f"  To:     {tx.get('to_address', 'N/A')}")
        coins = tx.get("coins", [])
        for c in coins:
            print(f"  Coin:   {c.get('amount', '?')} {c.get('asset', '?')}")
        print(f"  Memo:   {tx.get('memo', 'N/A')}")
    else:
        print("\nNo consensus inbound transaction found")
        print("This transaction may not have been observed by THORChain yet")

    # Planned outbounds
    if planned:
        print(f"\nPlanned Outbounds ({len(planned)}):")
        for i, p in enumerate(planned):
            coin = p.get("coin", {})
            refund = " [REFUND]" if p.get("refund") else ""
            print(f"  {i+1}. {p.get('chain', '?')} -> {p.get('to_address', '?')}")
            print(f"     {coin.get('amount', '?')} {coin.get('asset', '?')}{refund}")

    # Actual outbounds
    if out_txs:
        print(f"\nBroadcast Outbounds ({len(out_txs)}):")
        for i, o in enumerate(out_txs):
            print(f"  {i+1}. TX: {o.get('id', 'N/A')}")
            print(f"     Chain: {o.get('chain', '?')} -> {o.get('to_address', '?')}")
            coins = o.get("coins", [])
            for c in coins:
                print(f"     {c.get('amount', '?')} {c.get('asset', '?')}")

    # Stage analysis
    diag = parse_stage(stages, current_height)

    print("\n--- Diagnostic ---")
    print(f"Phase: {diag['phase']}")
    if diag["stuck"]:
        print(f"** STUCK at: {diag['stuck_at']} **")
    for d in diag["details"]:
        print(f"  - {d}")

    # Collect all relevant tx hashes for log searching
    hashes = set()
    if tx and tx.get("id"):
        hashes.add(tx["id"])
    if out_txs:
        for o in out_txs:
            if o.get("id"):
                hashes.add(o["id"])

    if hashes:
        print(f"\nTx hashes for log search: {', '.join(hashes)}")

    # Collect destination addresses for log searching
    dest_addresses = set()
    if planned:
        for p in planned:
            if p.get("to_address"):
                dest_addresses.add(p["to_address"])
    if out_txs:
        for o in out_txs:
            if o.get("to_address"):
                dest_addresses.add(o["to_address"])

    # Output structured data to stderr for programmatic use
    analysis = {
        "phase": diag["phase"],
        "stuck": diag["stuck"],
        "stuck_at": diag["stuck_at"],
        "inbound_hash": tx.get("id") if tx else None,
        "inbound_chain": tx.get("chain") if tx else None,
        "inbound_to": tx.get("to_address") if tx else None,
        "inbound_memo": tx.get("memo") if tx else None,
        "outbound_hashes": [o.get("id") for o in (out_txs or []) if o.get("id")],
        "outbound_chains": list(
            {o.get("chain") for o in (out_txs or []) if o.get("chain")}
        ),
        "planned_chains": [p.get("chain") for p in (planned or [])],
        "dest_addresses": list(dest_addresses),
        "all_hashes": list(hashes),
        "scheduled_outbound_height": stages.get("outbound_signed", {}).get(
            "scheduled_outbound_height"
        ),
    }
    print(json.dumps(analysis), file=sys.stderr)


if __name__ == "__main__":
    main()
