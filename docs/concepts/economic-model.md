# Economic Model

THORChain's economic model is designed to maintain network security, ensure liquidity efficiency, and sustain long-term protocol viability. It does this through a combination of the Incentive Pendulum, an emission schedule from the Reserve, and defined inflows and outflows.

## Incentive Pendulum

The Incentive Pendulum automatically adjusts reward distribution between node operators and liquidity providers to maintain the correct balance between security and liquidity.

The system monitors the ratio of bonded RUNE (from node operators) to pooled assets:

- If there is too much liquidity relative to bonded security, the network is unsafe, so rewards shift toward nodes.
- If there is too much bonded capital relative to liquidity, the network is inefficient, so rewards shift toward liquidity providers.

This creates a self-balancing system that maintains a target of **2:1 bond-to-stake ratio**, which is central to THORChain's security model. Detailed documentation on the Incentive Pendulum can be found in [Incentive Pendulum](./incentive-pendulum.md).

## Emission Schedule

### Token Distribution

There are a maximum of 360M RUNE (reduced from 500M per ADR-023). All supply was created at genesis and distributed as follows:

- 5% (SEED) and 16% (IDO) sold for capital to start the network and give it value.
- 10% allocated to early developers who worked since 2018.
- 24% given to users who participated in network bootstrapping.
- 44% placed in the Protocol Reserve to pay out to nodes and LPs for the next 10+ years.

```admonish success
All vesting has been completed.
```

The [Reserve module](https://runescan.io/address/thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt) and other modules can be viewed [here](https://runescan.io/addresses).

### Block Rewards

Block rewards are calculated as:

```text
blockReward = (reserve / emissionCurve) / blocksPerYear
```

```admonish info
The [emission curve](https://dev.thorchain.org/mimir.html#economics) is currently set to 100,000, meaning block rewards are minimal, approx. 740 RUNE per year.
```

### Reserve Maximum Cap

The [**ReserveMaxCap**](../mimir.md#economics) is a mechanism that allows increased emissions when the Reserve balance becomes too large. When enabled, it automatically overrides the EmissionCurve mimir value to the default constants value of 6.

**How it Works:**

1. If `ReserveMaxCap > 0` (enabled)
2. And `totalReserve > ReserveMaxCap`
3. Then `EmissionCurve` is automatically set to `6` (default)

**Purpose:**

Once the reserve reaches the target balance, any additional income to the reserve is emitted to nodes and pools as additional yield instead of accumulating excess in reserve.

**Configuration:**

- **Mimir Setting**: `ReserveMaxCap`
- **Default**: 0 (disabled)
- **Units**: 1e8 notation (e.g., `20000000000000000` = 200,000,000 RUNE)

**Example Scenario:**

```text
ReserveMaxCap = 20000000000000000  (200M RUNE cap)
totalReserve = 25000000000000000   (250M RUNE actual)
emissionCurve = 100,000            (mimir value)

Because totalReserve > ReserveMaxCap:
  → emissionCurve is overridden to 6 (constants)
  → Emissions are increased to prevent the Reserve retaining too much RUNE
```

When the totalReserve falls below the ReserveMaxCap, the mimir override is removed and the mimir emissionCurve is used. At no stage is the mimir value changed.

See [Constants and Mimirs](../mimir.md#economics) for current network settings.

## Reserve Inflows and Outflows

### Reserve Inflows

1. **Native Transaction Fee**: A 0.02 RUNE fee applies to transactions made on the THORChain blockchain (RUNE, Synthetic Assets, Secure Assets). This is separate from outbound fees, which only apply to external-chain transactions.
2. **Outbound Fees**: Fees collected from all outbound transactions, varying by asset type:
   - **Native Outbound Transaction Fee**: Fixed 0.02 RUNE on RUNE and native asset transactions.
   - **Layer 1 Outbound Fee**: For external-chain assets (e.g., BTC, ETH), this bundles the external gas cost, gas pool swap fee, and THORChain network fee into a single charge. Fee levels are determined by the chain's gas rate and [`dynamic_multiplier_basis_points`](https://gateway.liquify.com/chain/thorchain_api/thorchain/outbound_fees).
3. **Withdrawal of Reserve POL**: Occurs when RUNEPool additions replace Reserve-backed POL or when POL requirements are reduced.
4. **Slashing Income**: From node bond slashes, particularly for keygen failures or other operational breaches.
5. **Staged Pool Costs**: Deductions from the staged pool to cover churn-related costs. Controlled by the Mimir variable [`StagedPoolCost`](../mimir.md#economics).

### Reserve Outflows

1. **Gas Reimbursement**:
   - **Churn Gas Reimbursements**: Covers migration gas costs during vault churns. Reimbursed by the Reserve and factored into outbound fee adjustments.
   - **Non-Churn Gas Reimbursements**: Reimburses gas for external-chain outbound transactions. Over time, total outbound fees collected for a given coin are designed to equal total reimbursements for that coin.
2. **Reserve Adding to POL**: If undeployed RUNE in RUNEPool is insufficient to cover a withdrawal, the Reserve contributes. If RUNEPool has enough, no Reserve contribution is needed.
3. **Block Rewards**: Paid to node operators and liquidity providers.
4. **Affiliate Revenue Share (REVSHARE)**: A configurable share of the swap liquidity fees attributed to a given affiliate THORName is paid from the Reserve to that affiliate's `AffiliateCollector` balance at end of block. The deduction is taken off the top of `systemIncome` **before** any other split (dev fund, burn, TCY, marketing, POL, bond/LP pendulum) runs. The dial is the per-thorname operational mimir [`REVSHARE-<thorname>`](../mimir.md#fee-management) (basis points, capped at 5000). See [ADR 027](../architecture/adr-027-affiliate-revshare.md).

### Additional Points

- Gas reimbursements and outbound fees generally balance each other over time, ensured by the dynamic [Outbound Fee Multiplier (OFM)](./fees.md#4-outbound-fee).
- When enabled by Mimir, bond provider yield auto-claim pays the configured share of each reward distribution and rebonds the remainder.
- POL funding prioritises RUNEPool, with the Reserve acting only as fallback.
- System income (swap fees) is distributed immediately to developers, burns, pools, and nodes, rather than being retained by the Reserve.

## Monitoring

You can monitor THORChain's economic state through:

- **Reserve Balance**: [Midgard Network Endpoint](https://gateway.liquify.com/chain/thorchain_midgard/v2/network)
- **Current Constants**: [Constants Endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/constants)
- **Active Mimirs**: [Mimir Endpoint](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir)
- **Incentive Pendulum**: [RUNETools Pendulum](https://rune.tools/pendulum)

## Related Documentation

- [Incentive Pendulum](./incentive-pendulum.md) - Reward distribution algorithm
- [Constants and Mimirs](../mimir.md) - Network parameters
- [Fees](./fees.md) - Fee structure and revenue
- [RUNE Pool](./rune-pool.md) - RUNE-only liquidity provision
