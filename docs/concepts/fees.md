# Fees

## Overview

There are 4 different fees the user should know about.

1. Inbound Fee (sourceChain: gasRate \* txSize)
2. Affiliate Fee (affiliateFee \* swapAmount)
3. Liquidity Fee (swapSlip \* swapAmount)
4. Outbound Fee (destinationChain: gasRate \* txSize)

## Key Terms

| Term                  | Description                                   |
| --------------------- | --------------------------------------------- |
| **Source Chain**      | Chain the user is sending funds from          |
| **Destination Chain** | Chain the user is receiving funds on          |
| **txSize**            | Size of the transaction in bytes or gas units |
| **gasRate**           | Gas price (e.g. sats/byte, gwei) for a chain  |
| **swapAmount**        | Amount sent by the user                       |
| **swapSlip**          | Pool-based slip incurred by the swap          |
| **affiliateFee**      | Optional fee taken as a basis point skim      |

## Fee Ordering for Swaps

Fees are taken in the following order when conducting a swap.

1. **Inbound Fee** — paid directly on the source chain
2. **Swap Fee** — deducted by THORChain during the swap (in output asset)
3. **Affiliate Fee** — skimmed post-swap (in output asset)
4. **Outbound Fee** — charged from the swap output to cover final delivery

```admonish info
With **streaming swaps**, the **Affiliate Fee is applied *before* the Swap Fee**.
This ensures that if the swap is later refunded, the full amount (including the affiliate portion) is returned to the user. It prevents cases where an affiliate fee is deducted even when the swap does not complete.
```

To work out the total fees, fees should be converted to a common asset (e.g. RUNE or USD) then added up. Total fees should be less than the input else it is likely to result in a refund.

## 1. Inbound Fee

This is the fee the user pays to make a transaction on the source chain, which the user pays directly themselves. The gas rate recommended to use is `fast` where the tx is guaranteed to be committed in the next block. Any longer and the user will be waiting a long time for their swap and their price will be invalid (thus they may get an unnecessary refund).

$$
inboundFee = txSize * gasRate
$$

```admonish info
THORChain publishes current gas rates at [`/thorchain/inbound_addresses`](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses)
```

- `gasRate` is derived from the **max of the last 10 blocks**, then scaled via OFM
- `txSize` is estimated per chain (e.g. 250 bytes for UTXO, 21,000 gas for ETH)

```admonish warning
Always use a "fast" or "fastest" fee, if the transaction is not confirmed in time, it could be abandoned by the network or failed due to old prices. You should allow your users to cancel or re-try with higher fees.
```

## 2. Liquidity Fee

This is simply the slip created by the transaction multiplied by its amount. It is priced and deducted from the destination amount automatically by the protocol.

$$
slip = \frac{swapAmount}{swapAmount + poolDepth}
$$

$$
fee = slip * swapAmount
$$

See more information in the [Liquidity Section](https://docs.thorchain.org/technical-documentation/thorchain-finance/continuous-liquidity-pools)

```admonish warning
A minimum swap fee in basis points applies for different asset types, governed by the [mimir network settings](../mimir.md#swapping).
```

### Minimum Swap Fee

When a swap generates less slip than the applicable minimum, the liquidity fee is charged at that minimum instead. The network selects the floor in the following order, first match wins ([`getMinSlipBps`](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/helpers.go)):

1. The swap originates from the WASM arbitrage contract — `WasmArbSlipMinBps`.
2. The swap is an L1 pair carrying an active whitelisted affiliate THORName — that THORName's [dynamic floor](#dynamic-l1-minimum-fee).
3. Both assets are stablecoin [TOR](https://docs.thorchain.org/technical-documentation/thorchain-finance/tor) anchors and neither side is RUNE — `StableSlipMinBps`. Unlike the floors below, this one is selected by the pair rather than the asset type, so it overrides whichever class floor would otherwise apply.
4. Otherwise the asset-class floor — `L1SlipMinBps`, `TradeAccountsSlipMinBps`, `SecuredAssetSlipMinBps`, `SynthSlipMinBps` or `DerivedSlipMinBps`.

### Dynamic L1 Minimum Fee

For L1 pairs carrying an enrolled THORName affiliate, the minimum is set per THORName and trading pair rather than by the network-wide `L1SlipMinBps`, and increases or decreases with the fee revenue that affiliate generates on the pair.

At the end of each epoch (~24h) the network totals the fee income that THORName produced on the pair and compares it with the period before its last floor change. Income up, the floor continues in the same direction; income down, it reverses. Each step is one basis point, so the floor settles higher where THORChain is the only venue and lower where it competes. See [ADR 026](../architecture/adr-026-dynamic-l1-min-fee-per-thorname.md) for the rule, bounds and events.

This only affects the liquidity fee charged to the swapper. The affiliate's own basis points and the existing fee accounting path (extraction to the Reserve, then distribution via the [Incentive Pendulum](./incentive-pendulum.md)) are unchanged. The THORName is only the key that selects the floor.

Enrollment is per-THORName, set by node operators via the operational mimir `DYNAMICFEE-WHITELIST-<thorname>`:

| Value        | State        | Effect                                                                             |
| ------------ | ------------ | ---------------------------------------------------------------------------------- |
| `1`          | Active       | The dynamic floor is applied to the THORName's swaps.                              |
| `2`          | Monitor      | The floor is calculated and stored but **not** applied — swaps use `L1SlipMinBps`. |
| `0` / absent | Not enrolled | Swaps use `L1SlipMinBps`.                                                          |

THORNames containing `_` or `+` cannot currently be enrolled via `DYNAMICFEE-WHITELIST-<thorname>` because of mimir key charset restrictions. ([REVSHARE](../affiliate-guide/affiliate-fee-guide.md#revenue-share-revshare) solved the same issue with an explicit alias; dynamic fee does not.)

A new record starts at 1 bps and ratchets up, so enrollment begins below `L1SlipMinBps`; the ceiling is not capped at `L1SlipMinBps`, so a THORName whose fees fall can rise above it until the next epoch moves it back down. When a memo lists several affiliates, the protocol first filters to Active enrolled THORNames that carry a non-zero affiliate fee, then selects the largest affiliate basis-point split among those remaining — that entry's floor applies. Raw addresses, expired THORNames, Monitor-mode, unenrolled, and zero-bps entries are skipped; if none remain eligible the swap uses `L1SlipMinBps`. See the [THORName guide](../affiliate-guide/thorname-guide.md) for validity and expiry rules.

Current state is exposed by [`/thorchain/dynamic_l1_fees`](https://gateway.liquify.com/chain/thorchain_api/thorchain/dynamic_l1_fees), `/{thorname}` and `/dynamic_l1_fees_current`.
Example output:

| Pair              | `ss`  | `symbiosis` | Unenrolled |
| ----------------- | ----- | ----------- | ---------- |
| `BTC.BTC`↔RUNE   | 4 bps | 4 bps       | 10 bps     |
| `ETH.ETH`↔RUNE   | 3 bps | —           | 10 bps     |
| `LTC.LTC`↔RUNE   | 3 bps | 2 bps       | 10 bps     |
| `GAIA.ATOM`↔RUNE | 2 bps | —           | 10 bps     |

```admonish info
Quotes account for this. The [Swap Quote](../swap-guide/quickstart-guide.md#2-query-for-a-swap-quote) endpoint simulates the real swap, so a quote carrying your `affiliate` **and a non-zero `affiliate_bps`** returns your dynamic floor. Omit either and the quote is priced at `L1SlipMinBps` and will not match the executed swap.
```

## 3. Affiliate Fee

Within the transactions you build for your users you can include an affiliate for your exchange.

- Affiliate fees are possible for: swaps, saving deposit, lending addition, RUNEPool withdrawal.
- The affiliate fee is in basis points (0-10,000) and will be deducted from the inbound or outbound transaction amount.
- A THORName is required to collect affiliate address. See a guide on creating THORNames [here](../affiliate-guide/thorname-guide.md).
- Affiliates are paid in $RUNE by default however a [preferred asset](../affiliate-guide/thorname-guide.md#preferred-asset-for-affiliate-fees) can be specified within the THORName.
- Multiple Affiliates are possible for swaps.

$$
affiliateFee = \frac{feeInBasisPoints * txAmount}{10000}
$$

See the [Affiliate Fee Guide](../affiliate-guide/affiliate-fee-guide.md) for more information.

```admonish info
Affiliates may also earn a share of the protocol's own swap liquidity fees, paid from the Reserve, via the per-thorname [`REVSHARE-<thorname>`](../mimir.md#fee-management) operational mimir. This is on top of the user-side affiliate fee and does not change what the user pays. See [Revenue Share (REVSHARE)](../affiliate-guide/affiliate-fee-guide.md#revenue-share-revshare).
```

## 4. Outbound Fee

The Outbound Fee is what THORChain charges to deliver the final asset to the user on the destination chain. It covers the actual L1 gas costs (paid by nodes), and helps sustain the network's infrastructure — including TSS signing, compute, and state management.

To ensure long-term sustainability, the outbound fee is marked up by a dynamic value called the **Outbound Fee Multiplier (OFM)**. This allows the protocol, specifically the Reserve, to collect slightly more than it spends, building a buffer (or "surplus") of RUNE to cover fluctuations in network cost. See [Reserve Outflows](./economic-model.md#reserve-outflows) for more information.

### Why It's Dynamic

The Outbound Fee Multiplier moves between a **maximum** and **minimum** value, depending on the current fee "surplus" for a chain:

- **Surplus = Fees Withheld – Fees Spent**
- If the surplus is **above target**, the OFM **decreases** (users pay closer to actual gas costs)
- If the surplus is **below target**, the OFM **increases** (users pay more to replenish reserves)

This dynamic model ensures users pay fair outbound fees while keeping the protocol solvent.

The outbound fee "surplus" is the cumulative difference (in $RUNE) between what the users are charged for outbound fees and what the nodes actually pay. As the network books a "surplus" the OFM slowly decreases from the Max to the Min.

### Outbound Fee Formula

```math
outboundFee = txSize × gasRate × Outbound Fee Multiplier (OFM)
```

- The `gasRate` and `txSize` come from live on-chain data
- The **OFM** ranges from **1.0x to 3.0x** and is dynamically calculated using the following [Mimir values](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir):

| Mimir Key                             | Description                        | Default        |
| ------------------------------------- | ---------------------------------- | -------------- |
| `TARGETOUTBOUNDFEESURPLUSRUNE`        | Target surplus (in RUNE) per asset | `10,000 RUNE`  |
| `MAXOUTBOUNDFEEMULTIPLIERBASISPOINTS` | Max fee multiplier in basis points | `30000` = 3.0x |
| `MINOUTBOUNDFEEMULTIPLIERBASISPOINTS` | Min fee multiplier in basis points | `100` = 0.01x  |

```admonish info
Values can change. Current OFM values are published at [`/thorchain/network`](https://gateway.liquify.com/chain/thorchain_api/thorchain/network)
```

### How the OFM works

- THORChain tracks the **gas fees spent** and **fees withheld** per asset
- The **surplus** is defined as:

  ```text
  surplus = feesWithheld - feesSpent
  ```

- The surplus is tracked independently per asset
- If the surplus exceeds the target, the multiplier drops toward the min
- If below target, the multiplier rises toward the max
- This ensures the network collects enough to cover real costs without overcharging users

```math
OFM = Max - ((surplus / targetSurplus) × (Max - Min))
```

It is recalculated every block, per asset.

- Final fee is returned by:

  - [`/thorchain/inbound_addresses`](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) → `outbound_fee`
  - [`/thorchain/network`](https://gateway.liquify.com/chain/thorchain_api/thorchain/network) → `outbound_fee_multiplier`

### Refunds and Minimum Swappable Amount

If a transaction fails, it is refunded, thus it will pay the `outboundFee` for the **SourceChain** not the DestinationChain. Thus devs should always swap an amount that is a maximum of the following, multiplier by a buffer of at least 4x to allow for sudden gas spikes:

1. The Destination Chain outbound_fee
2. The Source Chain outbound_fee
3. $1.00 (the minimum)

The outbound_fee for each chain is returned on the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint, priced in the gas asset.

It is strongly recommended to use the `recommended_min_amount_in` value that is included on the [Swap Quote](../swap-guide/quickstart-guide.md#2-query-for-a-swap-quote) endpoint, which is the calculation described above. This value is priced in the inbound asset of the quote request (in 1e8). This should be the minimum-allowed swap amount for the requested quote. The swap quote endpoint will return a helpful error message including this value if the swap amount is insufficient.

```admonish info
Remember, if the swap limit is not met or the swap is otherwise refunded the outbound_fee of the Source Chain will be deducted from the input amount, so give your users enough room.
```

### Understanding gas_rate

THORNode keeps track of current gas prices. Access these at the `/inbound_addresses` endpoint of the [THORNode API](./connecting-to-thorchain.md#thornode). The response is an array of objects like this:

```json
{
    "chain": "ETH",
    "pub_key": "thorpub1addwnpepqfzafst6y2f33pdvheq6qe25xyzrwy542m4tq0nfnh6cn67d56n3g3lfwej",
    "address": "0x215520b3943c89e4fa501902ef7b76fdd199023b",
    "router": "0xD37BbE5744D730a1d98d8DC97c42F0Ca46aD7146",
    "halted": false,
    "global_trading_paused": false,
    "chain_trading_paused": false,
    "chain_lp_actions_paused": false,
    "gas_rate": "60",
    "gas_rate_units": "gwei",
    "outbound_tx_size": "100000",
    "outbound_fee": "180200",
    "dust_threshold": "0
}
```

The `gas_rate` property can be used to estimate network fees for each chain the swap interacts with. For example, if the swap is `BTC -> ETH` the swap will incur fees on the bitcoin network and Ethereum network. The `gas_rate` property works differently on each chain "type" (e.g. EVM, UTXO, BFT).

The `gas_rate_units` explain what the rate is for chain, as a prompt to the developer.

The `outbound_tx_size` is what THORChain internally budgets as a typical transaction size for each chain.

The `outbound_fee` is `gas_rate * outbound_tx_size * OFM` and developers can use this to budget for the fee to be charged to the user. The current Outbound Fee Multiplier (OFM) can be found on the [Network Endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/network).

Keep in mind the `outbound_fee` is priced in the gas asset of each chain. For chains with tokens, be sure to convert the `outbound_fee` to the outbound token to determine how much will be taken from the outbound amount. To do this, use the `getValueOfAsset1InAsset2` formula described in the [`Math`](./math.md#example) section.

## Fee Calculation by Chain

### **THORChain (Native Rune)**

The THORChain blockchain has a set 0.02 RUNE fee. This is set within the THORChain [Constants](https://gateway.liquify.com/chain/thorchain_api/thorchain/constants) by `NativeTransactionFee`. As THORChain is 1e8, `2000000 TOR = 0.02 RUNE`

### UTXO Chains like Bitcoin

For UXTO chains link Bitcoin, `gas_rate`is denoted in Satoshis. The `gas_rate` is calculated by looking at the average previous block fee seen by the THORNodes.

All THORChain transactions use BECH32 so a standard tx size of 250 bytes can be used. The standard UTXO fee is then `gas_rate`\* 250.

### EVM Chains like Ethereum

For EVM chains like Ethereum, `gas_rate`is denoted in GWEI. The `gas_rate` is calculated by looking at the average previous block fee seen by the THORNodes

An Ether Tx fee is: `gasRate * 10^9 (GWEI) * 21000 (units).`

An ERC20 Tx is larger: `gasRate * 10^9 (GWEI) * 70000 (units)`

```admonish success
THORChain calculates and posts gas fee rates at [`https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses`](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses)
```
