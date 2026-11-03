# Delays

## Overview

There are five phases a transaction goes through when interacting with THORChain:

1. [Inbound Confirmation](#1-inbound-confirmation)
2. [Observation & Consensus](#2-observation--consensus)
3. [Finality & Confirmation Counting](#3-finality--confirmation-counting)
4. [Outbound Delay (Queueing)](#4-outbound-delay-queueing)
5. [Outbound Confirmation](#5-outbound-confirmation)

Wait times can be between a few seconds to several hours. The assets being swapped, the size of the swap and the current network traffic within THORChain will determine the wait time.

## 1. Inbound Confirmation

This depends purely on the host chain and is out of the control of THORChain.

Typical confirmation times:

| Chain      | Time to First Confirmation |
| ---------- | -------------------------- |
| Bitcoin    | \~10 minutes               |
| Litecoin   | \~2.5 minutes              |
| Dogecoin   | \~60 seconds               |
| Ethereum   | \~15 seconds               |
| Cosmos SDK | \~6 seconds                |

## 2. Observation & Consensus

Once a transaction is confirmed on its host chain, THORNodes must _observe_ it. Each THORNode scans external chains for relevant vault activity. Once a supermajority (**2/3+** of active nodes) independently observe the same transaction, it reaches consensus and is marked as `observed`. This process could be seconds to minutes depending on how fast nodes can scan their blockchains to find transactions.

You can inspect this process using the `/thorchain/tx` endpoint. For example:

[View example Tx](https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/b9d70ea425c44ce0a2f78ea0bdb2fc861e214d93752bc9a1f811000dc2b44d4b)

```json
"external_observed_height": 874437,
"external_confirmation_delay_height": 874437
```

> In this example, Bitcoin block 874437 is when the transaction was first observed and when confirmation counting begins.

You can track the number of nodes that have seen a Tx by checking the `signers` array or looking at the `status` field on the `/tx` endpoint.

See [Inbound Transactions](../bifrost/how-bifrost-works.md#inbound-transactions) for more information.

## 3. Finality & Confirmation Counting

To protect against reorgs, THORChain requires some chains (like Bitcoin and Ethereum) to wait for _economic finality_ — enough confirmations to make a reorg economically infeasible. It tracks both, then computes the number of blocks to wait. It then populates this on the `/tx` endpoint.

Example: [https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/b9d70ea425c44ce0a2f78ea0bdb2fc861e214d93752bc9a1f811000dc2b44d4b](https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/b9d70ea425c44ce0a2f78ea0bdb2fc861e214d93752bc9a1f811000dc2b44d4b)

- BFT chains like Cosmos **do not require** additional confirmation counting.
- UTXO and EVM chains **do**.

This is reflected in these fields:

```json
"consensus_height": 18972084,
"finalised_height": 18972084,
"outbound_height": 18972085,
```

- `block_height` = first seen external block height
- `finalised_height` = block height required to mark tx as final
- `outbound_height` = when outbound is eligible for scheduling

```admonish warning
An event is not emitted until the external chain’s height passes `finalised_height`.
Midgard will not show the transaction until this point.
```

Finality is dynamically calculated using:

```text
RequiredConfirmations = min(
    (TxValue / BlockReward) × ConfMultiplier,
    MAXCONFIRMATIONS
)
```

- The confirmation multiplier and cap are set via Mimir per chain
- Larger transactions generally require more confirmations

See [Finality & Pre-Confirmation](../bifrost/how-bifrost-works.md#finality--pre-confirmation) for deeper explanation and code references.

## 4. Outbound Delay (Queueing)

After finality, the transaction is ready for outbound processing. THORChain throttles all outputs to prevent fund loss attacks. The maximum delay is 720 blocks which is approx 1 hour. Outbound delay worked out by computing the value of the outbound transaction in RUNE then applying an artificial delay. If the tx is in "scheduled", it will be delayed by a number of blocks.

A transaction in `"scheduled"` state is delayed. Once it reaches `"outbound"`, it’s in the signing queue and about to be broadcast.

```admonish info
Arbs and Traders who have trade history can have a reduced wait time due to [Swapper Clout.](./swapper-clout.md)
```

See more information [here](https://docs.thorchain.org/technical-documentation/technical-deep-dive/security#outbound-transaction-throttling).

### Swapper Clout Reduces Delay

[Clout](./swapper-clout.md) is the cumulative total fees paid (in RUNE) for a given address. Swappers with a high clout have proven themselves to be highly aligned to the project and therefore can reap the rewards by getting faster trade execution.
Frequent swappers who pay more fees earn **Swapper Clout**. This is used to **reduce or eliminate** outbound delay.

- For each swap, 50% of fees (in RUNE) are attributed to the sender and recipient
- Clout reduces the _effective value_ of the outbound when calculating delay
- The formula:

```text
delay = delayCalc(outboundValue - (cloutScore - cloutUsed))
```

- Clout is applied proportionally across all active outbounds from the same address
- Clout is _restored_ once the outbound achieves observation consensus

[See full implementation](./swapper-clout.md)

### Monitoring the Outbound Queue

- [All Queued TxOuts](https://gateway.liquify.com/chain/thorchain_api/thorchain/queue)
- [Scheduled (Delayed) Outbounds](https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/scheduled)
- [Ready-to-Send Outbounds](https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/outbound)

## 5. Outbound Confirmation

This depends purely on the host chain and is out of the control of THORChain.

Typical confirmation times:

| Chain      | Time to Confirm Outbound |
| ---------- | ------------------------ |
| Bitcoin    | \~10 minutes             |
| Litecoin   | \~2.5 minutes            |
| Dogecoin   | \~60 seconds             |
| Ethereum   | \~15 seconds             |
| Cosmos SDK | \~6 seconds              |

## UX Guidelines: Handling Delays

Apps and frontends should:

1. **Use the [Quote endpoint](../concepts/querying-thorchain.md#querying-thorchain)** to estimate gas fees and inform the user.
1. **Avoid leaving users staring at a spinner** — move swaps to a "pending" state with a countdown.
1. **Notify the user** once their swap completes — especially if it's faster than expected.
1. Let users close the app while waiting — no need to keep them locked in.
