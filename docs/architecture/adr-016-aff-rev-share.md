# ADR 16: Affiliate Rev Share

## Changelog

- 05/31/2024: Created
- 06/18/2024: Dropped Min L1 Swap Bps proposal from ADR. That measure is fairly straight forward and non-controversial, and can be voted on without any changes needed to the codebase. Affiliate rev share has been slightly more controversial, and would require changes to the code. It should be voted on separately and prior to its implementation. Thus, drop the Min L1 Swap Bps proposal from this ADR, and refocus this ADR on just the Affiliate Rev Share. Update requested by Pluto (9R) and other contributors in #economic-design Discord channel, agreed to by ADR author Orion (9R).

## Status

Proposed

## Context

This ADR proposes to add `ProtocolAffiliateFeeBasisPoints`, a new mimir, to increase fees paid via affiliates to drive revenue for Network participants.
Currently, 70-80% of the volume on THORChain is driven by arb bots (synths), with the remaining portion from swappers ([source](https://flipsidecrypto.xyz/Rayyyk/thorchain-swap-volume-insight-Dq1Qzl?tabIndex=1)).

By increasing increasing fees from affiliate swaps, the network can explore the demand surface for cross chain swaps.

Adding these fee levers is an important step towards THORChain's longterm economic success.

### Proposed New Mimir

(this mimir needs to be implemented)

`ProtocolAffiliateFeeBasisPoints` set to `1200` (12% of the affiliate Fee)
This new mimir would increase the fee a swapper pays via an affiliate to up to an xx% of the affiliate fee.

If the affiliate is collecting 20bps then the additional protocol fee would add ~2 bps (12%). The ~2 bps fee would go into the pool as additional fee revenue.

## Decision

Approved via node consensus.

## Consequences

The consequences for each network participant are considered below.

1. **Swappers** - Marginal increase in fees paid.

1. **LPer** - Potential increase in yield.

1. **Saver** - Potential increase in yield.

1. **Lending**
   marginal increase in fees to open/close loans.

1. **Node Operator/Bond Providers**
   potential increase to swap fee portion of total rewards.
1. **Affiliates**
   Affiliate's users will pay marginally more for swaps due to fee increase.
