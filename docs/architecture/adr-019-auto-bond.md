# ADR 19: AutoBond

## Changelog

- 17/07/2024: Created

## Status

Proposed

## Context

The upcoming addition of RUNEPool gives users a minimal friction path to earn RUNE-on-RUNE yield and add value to the network by increasing liquidity. However, the current incentive pendulum is weighted towards nodes, and attracting RUNEPool providers and L1 savers to match them requires shifting the pendulum to pools.

Instead of artificially shifting the pendulum away from nodes, we can provide an AutoBond mechanism that performs the same function for bond as RUNEPool does for the pools. This provides users an equally seamless path to contribute to the network bond, and allows market forces to shift the pendulum based on respective risk premium between bond and pool, while preserving the original concept of economic security:

- RUNEPool: Users assume levered price risk.
- AutoBond: Users assume bond slash risk due to malicious nodes and protocol bugs.

## Design

### Usage

The user and integrator experience maintains parity with RUNEPool for simplicity:

- RUNEPool: `pool+`/`pool-:<basis-points>`
- AutoBond: `bond+`/`bond-:<basis-points>`

### Implementation

The AutoBond module accepts all deposits and the module balance is treated as pending. Each deposit assigns units to the provider based on total value of pending + deployed auto bond RUNE (same as RUNEPool). The withdraw requests are similarly marked pending, and processed at each churn.

Every churn following the reward distribution (in the same block), the AutoBond module withdraws its bond from all nodes to pending, processes any withdraws, and re-deploys all remaining pending bond to the nodes as an automatic bond provider. The total amount is distributed evenly across nodes relative to their share of total bond (which should eventually be roughly equal if bond-scaled rewards are removed and nodes re-organize themselves into equal-size bonds).

There is a keeper to track the total shares and value, but unlike prior features the AutoBond providers are simply issued shares as a coin with the denom `brune`. This enables usage of the auto-bonded RUNE as an LST in the app layer. Future changes may also update RUNEPool provider tracking to leverage this pattern, with a denom `prune` for similar usage akin to an LST.

### Parameters

The following default parameters are suggested:

```text
AutoBondOperatorFeeBPS = 1000       // 10%
AutoBondReserveFeeBPS  = 1500       // 15%
AutoBondMaturityBlocks = 14400 * 30 // 30 days, similar to typical staking lockup
```

The ideal path to accrue bond is for providers and operators to form a relationship of mutual trust and accountability. Most current operators with providers charge a 20% operator fee, so the 10% fee they will collect from the AutoBond provider leaves them with incentive to pursue direct relationship. The combined 25% fee leaves large long-term investors with similar incentive to pursue direct relationship, and the 15% reserve fee accrues income to the network for the convenience offered by AutoBond.

## Alternative Approaches

One other option is to allow RUNEPool to automatically deploy pending RUNE into bond. This may provide a simpler experience for users, but also adds complexity to the implementation and removes market capability to determine the respective risk premium that should be assigned to bonding and pooling.

## Decision

TBD

## Consequences

The market will shift the incentive pendulum, factoring in the respective risk premium it determines between bonding and pooling. The market presumably determines bonding to be lower risk than pooling, and adds bond to shift the pendulum toward pools, increasing incentive for RUNEPool and corresponding savers - while preserving the original concept of economic security.

The increased security budget creates capacity for new features to be built on trade assets:

- Improvements to Dual LP
- Order Books
- Perps
- Wrapping and Export to Other Chains
