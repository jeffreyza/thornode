# RUNEPool

RUNEPool aims to enhance liquidity provision, attract a broader market, and maintain dominance in TVL across the crypto ecosystem.

## Overview

### What is RUNEPool?

RUNEPool is a feature of THORChain designed to optimise the utilisation of RUNE by depositing it into every POL-enabled liquidity pool in the network. By pooling RUNE and distributing it across all Protocol-Owned Liquidity [(PoL)-enabled pools](./rune-pool.md#pol-enabled-pools), RUNEPool allows participants to earn the net Annual Percentage Yield (APY) of these liquidity pools, but they are also exposed to the IL of the aggregate position. Poolers do not choose individual pools but share in the aggregate performance. Users are exposed to RUNE and all PoL-enabled assets within the network; therefore, participating in RUNEPool is effectively analogous to purchasing an index of RUNE and all PoL-enabled assets within THORChain. This approach simplifies the process of liquidity provision, reducing individual risk and the cognitive burden associated with managing multiple liquidity positions.

## RUNEPool Specifics

1. **Minimum RUNEPools Term**: There is a minimum term for RUNEPool participant defined by the config `RUNEPoolDepositMaturityBlocks`. This is the number of blocks from the last deposit that a withdraw is allowed.
2. **Impermanent Loss Management (IL)**: Users experience aggregate IL across [PoL-Enabled](./rune-pool.md#pol-enabled-pools) [pools](https://thorchain.net/pools) instead of individual pools. Aggregate IL is less than the IL from any one pool, reducing the risk. However, there is still a risk of IL resulting in negative yield.
3. **Volume and Fees**: Volume currently drives fees due to arbitrage. If RUNE volatility decreases, fees will primarily come from cross-chain swaps, potentially resulting in fewer fees but also reduced IL.
4. **Idle/Standby RUNE**: RUNE within the RUNEPool that is not deployed is shared by all participants relative to their Pool Units. While this static RUNE may reduce yield due to non-deployment, it also reduces exposure to impermanent loss (IL). Additionally, it allows for future demand from savers to be met more efficiently.

## Usage

The RUNEPool is utilised by creating transactions with specific memos. MsgDeposit must be used, and RUNEPool only works with native RUNE. Positions within the RUNEPool can be queried using [endpoints](./connecting-to-thorchain.md#thornode).

- **Add to the RUNEPool**: Use a MsgDeposit with the memo `pool+`. For detailed instructions, refer to the [Add to the RUNEPool](./memos.md#add-runepool) section.
- **Withdraw from the RUNEPool**: Use a MsgDeposit with the memo `pool-:<basis-points>:<affiliate>:<affiliate-basis-points>`. For detailed instructions, refer to the [Withdraw from the RUNEPool](./memos.md#withdraw-runepool) section.
- **View all RUNEPool holders**: Use the endpoint `rune_providers` to see a list of all RUNEPool holders.
- **View a specific RUNEPool holder's position**: Use the endpoint `rune_providers/{thor owner address}` to view the position of a specific RUNEPool holder.

## How It Works

### PoL-Enabled Pools

POL-Enabled via [mimir](../mimir.md) key `POL-{Asset}`. Currently all (eight) native assets and five USD stable coins are enabled for POL. Exact list is within the mimir [endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir).

### RUNEPool Units (RPU)

Ownership in RUNEPool is tracked through RUNEPool Units (RPU). These units represent a Pooler’s share in the pool. When a Pooler redeems their units, the total PoL size in RUNE is assessed, and the holder's share is distributed, which may be more or less than their initial contribution.

```go
RUNEPool.ReserveUnits     // Start state is RUNE value of current POL
RUNEPool.PoolUnits        // Deployed RUNEPool provider units
RUNEPool.PendingPoolUnits // aka "static", undeployed RUNEPool provider units
```

- `reserveExitRUNEPool` function: Transfers ownership units from the RESERVE to poolers, reducing RESERVE's units
- `reserveEnterRUNEPool` function: Transfers ownership units from poolers to the RESERVE, increasing RESERVE's units

### RUNEPool Deposit

1. Upon a user's deposit, the balance is moved to `runepool` module.
1. Corresponding units are added to `PendingPoolUnits`.
1. The `reserveExitRUNEPool` function moves `PendingPoolUnits` to `PoolUnits`, corresponding amount deducted is from `ReserveUnits`, RUNE is then moved from `runepool` module to the `reserve` module.

### RUNEPool Withdraw

1. If `PendingPoolUnits` is insufficient for the withdraw, the `reserveEnterRUNEPool` function moves RUNE from the `reserve` for the difference, `ReserveUnits` increased, `PoolUnits` moved to `PendingPoolUnits`.
1. The withdraw is processed from `PendingPoolUnits`.
1. If a withdraw would result in a reserve deposit that is greater than `POLMaxNetworkDeposit + RUNEPoolMaxReserveBackstop` the withdraw will not be allowed.

## Showing PnL

### Global PnL

The `/thorchain/runepool` endpoint returns the global Pnl of RunePool, as well as of the two RunePool participants: the reserve, and independent providers. The `value` and `pnl` properties are in units of RUNE. `current_deposit` equals `rune_deposited - rune_withdrawn` and can be negative.

```json
{
  "pol": {
    "rune_deposited": "408589258319",
    "rune_withdrawn": "208496086616",
    "value": "206166561256",
    "pnl": "6073389553",
    "current_deposit": "200093171703"
  },
  "providers": {
    "units": "232440861",
    "pending_units": "0",
    "pending_rune": "0",
    "value": "319161454",
    "pnl": "56394430",
    "current_deposit": "262767024"
  },
  "reserve": {
    "units": "149915806863",
    "value": "205847399802",
    "pnl": "6016995123",
    "current_deposit": "199830404679"
  }
}
```

The `/thorchain/rune_provider/{thor_addr}` endpoint will return a single providers position information including pnl:

```json
{
  "rune_address": "thor19phfqh3ce3nnjhh0cssn433nydq9shx76s8qgg",
  "units": "232440861",
  "value": "319161517",
  "pnl": "56394493",
  "deposit_amount": "3500000000",
  "withdraw_amount": "3237232976",
  "last_deposit_height": 14357483,
  "last_withdraw_height": 14358846
}
```

## References

- [RUNEPool Dashboard](https://thorchain.network/runepool/)
- [Original Issue](https://gitlab.com/thorchain/thornode/-/issues/1841)
- [[ADD] RUNEPool MR](https://gitlab.com/thorchain/thornode/-/merge_requests/3612/)
- [RUNEPool Implementation MR](https://gitlab.com/thorchain/thornode/-/merge_requests/3631)
