# Affiliate Fee Guide

Affiliate fees provide a way for interfaces to generate revenue through THORChain. These fees are collected from select transactions, sent to an affiliate collector module, and periodically distributed in a gas-efficient manner using the interface's preferred asset. Affiliate fees range from 0 to 1,000 basis points (10%) total, where 100 bps equals 1%. When using multiple affiliates, the total across all affiliates cannot exceed 1,000 basis points (10%).

## Overview

- **Fee Structure**: Affiliate fees are calculated in basis points and deducted from the transaction amount.
- **Address or THORName**: A valid THORChain address or THORName can be used to collect affiliate fees. A THORName is only required for preferred asset payouts. Learn how to create a THORName [here](./thorname-guide.md).
- **Preferred Asset**: By default, affiliates are paid in $RUNE, but a preferred asset can be specified using a THORName.
- **Multiple Affiliates for Swaps**: Swaps can support multiple affiliate fees, while single affiliate fees apply to savings deposits and RUNE pool withdrawals.
- **Automatic Distribution**: Affiliate fees are automatically distributed to the affiliate address within your THORName based on accumulated thresholds. No manual processing is required.
- **Revenue Share**: Beyond the user-side fee, a THORName can be configured to earn a share of the protocol's own swap liquidity fees. See [Revenue Share (REVSHARE)](#revenue-share-revshare).
- **Dynamic L1 Minimum Fee**: An enrolled THORName can set the minimum swap fee its users pay on L1 pairs. See [Dynamic L1 Minimum Fee](#dynamic-l1-minimum-fee).

## Getting Started

1. **Create a THORName:** See the [THORName Guide](./thorname-guide.md) for instructions.
1. **Set Up Your Preferred Asset:** Configure your [preferred asset](./thorname-guide.md#preferred-asset-for-affiliate-fees), such as USDC or BTC. By default, the affiliate asset is $RUNE.
1. **Enable Multiple Affiliates:** Set up [multiple affiliate](#multiple-affiliates) addresses for swap transactions.
1. **Set the Affiliate Fee Amount:** Specify the affiliate fee in basis points (0–10,000) in your transactions. See [Transaction Memos](../concepts/memos.md) for details.
1. **Start Using THORChain:** Let the affiliate collector module handle the periodic distribution of fees.

## Where the Affiliate Fee is Taken

- If the inbound swap asset is a native THORChain asset ($RUNE, synth, or trade asset), the affiliate fee amount will be deducted directly from the transaction amount.
- If the inbound swap asset is on any other chain, the network will submit a swap to $RUNE.
- If the affiliate is added to an `ADDLP` transaction, then the affiliate is included in the network as an LP.
- If the affiliate is added to a RUNEPool withdrawal, it is deducted from the profit amount (positive PnL), not the principal.

## How it Works

The affiliate fee system operates automatically, following these steps:

1. **Transaction Execution**: When a transaction includes an affiliate fee, the specified amount (in basis points) is deducted from the inbound or outbound transaction amount as described above.
1. **Fee Collection**: The deducted fee is collected in the [AffiliateCollector module](https://gateway.liquify.com/chain/thorchain_api/thorchain/balance/module/affiliate_collector), where it is held in $RUNE by default.
1. **Threshold Check**: The network continuously monitors the accumulated $RUNE for each affiliate. Once the balance exceeds [`PreferredAssetOutboundFeeMultiplier`](../mimir.md#fee-management) multiplied by the chain's outbound fee, the system initiates a swap to the preferred asset.
   - Example: If the outbound fee for BTC is 0.00005 BTC and the multiplier is 200, the swap will occur when the collected $RUNE is worth 0.01 BTC.
1. **Preferred Asset Swap**: If the affiliate's THORName specifies a preferred asset (e.g., BTC, USDC), the network swaps $RUNE to that asset before distribution. If no preferred asset is set, $RUNE is distributed directly.
1. **Automatic Distribution**: The converted (or original) amount is sent to the affiliate's address without requiring any manual intervention.

```admonish warning
If the affiliate fee amount is too small and the resulting swap to RUNE would not cover outbound fees, the affiliate fee will be silently skipped. This can occur with very small swap amounts or very low affiliate basis points.
```

### Benefits of Automatic Distribution

- **Gas Efficiency**: By waiting until the threshold is met, the network minimizes gas costs associated with frequent small transactions.
- **Asset Flexibility**: Affiliates can specify any supported asset as their preferred payout, ensuring they receive fees in the most convenient form.

## Multiple Affiliates

Interfaces can define up to a limit set by [MultipleAffiliatesMaxCount](../mimir.md) for valid affiliate and affiliate basis points pairs in a swap memo. This is used if more than one affiliate needs to be paid, e.g. an interface and implementation partner.
The network deducts a fee for each valid affiliate defined in the memo. Alternatively, up to 5 valid affiliates and exactly one valid basis point value can be defined, and the network will attempt to skim the same basis point fee for each affiliate.

Each THORName has its own collector, even when several THORNames share an owner. Its collected affiliate fees and REVSHARE remain separate from other THORNames' balances, and its preferred-asset payouts use its own alias. The [collector upgrade](./thorname-guide.md#upgrade-from-shared-owner-collectors) moves an owner's legacy balance to their THORName's collector when they have exactly one stored THORName that meets current THORName validation. Otherwise, it pays the balance directly to the owner in RUNE. Module-owned legacy balances go to Reserve.

```admonish warning
The total affiliate fee across all affiliates cannot exceed 1,000 basis points (10%). If the sum of all affiliate fees exceeds this limit, the transaction will be rejected.
```

### Valid Memo Examples

- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5:10`  
  _(Will skim 10 basis points for each of the affiliates)_

- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/thor1t2hav42urasnsvwa6x6fyezaex9f953plh72pq/t3:10/20/30`  
  _(Will skim 10 basis points for `t1`, 20 basis points for `thor1t2hav42urasnsvwa6x6fyezaex9f953plh72pq`, and 30 basis points for `t3`)_

### Invalid Memo Examples

- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5:10/20`  
  _(5 affiliates defined, but only 2 affiliate basis point values)_

- `=:ETH.ETH:0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430::t1/t2/t3/t4/t5/t6:10`  
  _(Too many affiliates defined)_

## Revenue Share (REVSHARE)

In addition to the user-side affiliate fee described above, an affiliate's THORName may also be configured to receive a share of the **protocol's own swap liquidity fees** for swaps it routes. This is paid out of the reserve at end of block, on top of any user-side affiliate fee, and does not change what the user pays.

- The dial is the operational mimir `REVSHARE-<thorname>`, in basis points, capped at **5000 (50%)**.
- Attribution is **strict-first-affiliate**: only the **first** entry in the swap memo's affiliate list earns rev share, and only if it resolves to an active THORName (raw addresses and expired thornames are ignored).
- Liquidity fees attributable to the affiliate accumulate across **streaming sub-swaps** within the same block.
- At end of block the network pays `bps × accrued_liquidity_fee / 10,000` from the reserve to the THORName's `AffiliateCollector` balance, and emits a `rev_share` event with attributes `thorname`, `owner`, `accrued_fee`, `bps`, `payout`. The event is emitted for every attributed thorname even when `bps == 0`, so indexers can track gross attribution independently of payout configuration.
- Payouts flow through the same `AffiliateCollector` mechanics as the user-side fee, so any preferred-asset configuration on the THORName applies to rev-share payouts as well.

### Eligibility

REVSHARE is opt-in by node operators on a per-affiliate basis (low-quorum operational mimir, churned out like other operational keys). To be eligible:

1. Register a THORName whose name uses only `[a-zA-Z0-9-]`. Thornames containing `_` or `+` register cleanly but cannot have a `REVSHARE-<thorname>` mimir set, because mimir keys do not accept those characters.
2. Coordinate with node operators to set `REVSHARE-<thorname>` to your agreed basis points.

See [ADR 027](../architecture/adr-027-affiliate-revshare.md) for the full rationale and design.

## Dynamic L1 Minimum Fee

Enrolling your THORName lets your users pay a **lower minimum swap fee** on L1 pairs. This is the protocol swap fee, whitelisted per THORName by node operators — it does not change your affiliate fee or payouts. See [Dynamic L1 Minimum Fee](../concepts/fees.md#dynamic-l1-minimum-fee) for how the floor works, enrollment and current values.
