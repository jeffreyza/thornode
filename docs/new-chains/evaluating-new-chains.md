# Evaluating New Chains

Before integrating a new Layer 1 blockchain into THORChain, contributors must evaluate its **economic value**, **technical risk**, and **long-term viability**. This guide outlines the criteria used to assess whether a chain is a good fit for the protocol.

## Why Chain Selection Matters

Connecting to an L1 exposes THORChain to:

- Economic risk (e.g. infinite mint bugs, poor LP performance)
- Technical instability (e.g. sync issues, halts, invalid outbound behavior)
- Ongoing costs (dev time, node ops, subsidy maintenance)

To protect the network, only high-quality chains with sustainable adoption and technical maturity should be considered.

## Evaluation Criteria

All chains should be assessed across the following five dimensions:

### 1. Decentralization

- ❌ Must not be controlled by a single entity
- ❌ Must not be pausable by a multisig of <10 signers
- ✅ If PoS, should have >10 active validators

### 2. Ossification

- ✅ Must be at least 2 years old (from genesis)
- ✅ Must not hard fork more than once every 6 months

### 3. Economic Value

- ✅ FDV ≥ 10% of THORChain's FDV
- ✅ 24h volume ≥ 10% of \$RUNE's volume
- ⏱ If PoW, a \$1,000 swap must not require more than 1 hour for confirmations

### 4. Developer and Node Support

- ✅ Must have a working node implementation
- ✅ Must have an open-source JS/TS client or SDK
- ✅ Must demonstrate organic developer activity

### 5. Community Reach

- ✅ Must serve a user base ≥10% of THORChain’s active user count
- ✅ Should be integrated into existing DEXs or DeFi protocols

## Risks of Poor Chain Selection

A low-quality or unstable chain can:

- Expose LPs to asset loss via protocol bugs
- Disrupt THORNode operations (e.g. sync issues, outbound halts)
- Require disproportionate developer maintenance
- Provide little fee revenue relative to cost
- Eventually require **Ragnarok** (chain removal)

## Ongoing Chain Health

Chains must continue to meet the above standards after integration. If a chain becomes too centralized, inactive, or unprofitable, it may be removed.

### Removal Criteria (must persist for ≥6 months)

- Fails any of the standards listed above
- Base asset pool depth drops below `MINRUNEPOOLDEPTH`
- 24h swap volume < \$1,000 for a full `POOLCYCLE`
- LP count drops below 100

See [Vault Behaviors](../bifrost/vault-behaviors.md) and [New Chain Process](./new-chain-process.md) for how this is handled.
