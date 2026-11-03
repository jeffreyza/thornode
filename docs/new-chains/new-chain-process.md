# New Chain Integration Process

Integrating a new L1 chain into THORChain is a structured process involving proposal, development, testing, and mainnet rollout. This guide outlines the required phases, technical standards, and contributor responsibilities.

## Overview

Each new chain poses risk and operational cost to the network. Chains must be economically meaningful, decentralised, and technically sound to be considered for integration.

The process consists of:

1. [Proposal & Node Vote](#phase-i-proposal-and-approval)
2. [Development & Stagenet Testing](#phase-ii-development-and-stagenet-testing)
3. [Mainnet Rollout](#phase-iii-mainnet-release)

## Phase I: Proposal and Approval

### Chain Requirements

Before proposing, ensure the chain meets the minimum standards defined in [Evaluating New Chains](./evaluating-new-chains.md), including:

- Sufficient decentralization and ossification
- Meaningful trading volume and user base
- Healthy developer and community support

### Proposal Submission

- Share a proposal in `#propose-a-chain` on Discord
- A new channel will be created under “Community Chains” for discussion
- Follow the [Chain Proposal Template](#chain-proposal-template)

### Node Mimir Vote

- Prompt node operators to vote using:

  ```toml
  Halt<ProposedChain>Chain = 1
  ```

- If 50%+ consensus is reached, the chain may proceed to development

## Phase II: Development and Stagenet Testing

### Build the Chain Client

Develop a `ChainClient` implementation under:

```text
/bifrost/pkg/chainclients/<yourchain>
```

Reference existing implementations:

- [UTXO](../chain-clients/utxo.md)
- [EVM](../chain-clients/evm.md)
- [BFT](../chain-clients/bft.md)

Open PRs to:

- [`thornode`](https://gitlab.com/thorchain/thornode)
- [`node-launcher`](https://gitlab.com/thorchain/node-launcher)

Follow the [Chain Client Implementation Guide](./implementation-guide.md) for detailed requirements.

### Stagenet Testing Requirements

Test the following on stagenet:

- Swapping to/from the asset
- Add/withdraw liquidity
- Synth mint/burn
- THORName registration
- Vault funding and churning
- Inbound address responses
- Handling insolvency and unauthorized txs
- `HaltSigning<Chain>` behavior

Minimum usage targets:

- 100 inbound txs
- 100 outbound txs
- 100 RUNE in total adds
- 100 RUNE in total withdrawals

### Chain Client Audit

- An expert familiar with the chain (but not the author) must independently review the implementation
- Audit must be published in the PR under:

```text
/bifrost/pkg/chainclients/<yourchain>
```

## Phase III: Mainnet Release

Steps performed by the dev team:

1. **Admin Mimir**: Halt the chain and disable trading
2. **Daemon Install**: Node operators sync the new chain daemon (`make install`)
3. **Enable Scanning**: Final `node-launcher` PR merged and deployed
4. **Admin Mimir**: Unhalt to enable scanning
5. **Admin Mimir**: Enable trading once scanning reaches chain tip

## Technical Requirements

### Thornode PR

- Add mocknet service in `build/docker/docker-compose.yml`
- ≥70% unit test coverage
- Respect `<chain>_DISABLED` env var in `bifrost.sh`
- Host a live walkthrough with dev team
- Answer key safety questions:

  - Can value be spoofed?
  - Is asset whitelisting enforced?
  - Are decimals handled correctly?
  - Is gas reporting deterministic?
  - Is solvency reporting implemented?

### Node Launcher PRs

Three sequential PRs are required:

#### 1. Image PR

- Add Dockerfile under `ci/images/<chain>/Dockerfile`
- Pin all source versions to commit hashes

#### 2. Service PR

- Use a previous chain as a template
- Slightly over-provision resources (+20%)
- Add chain to:

  - `get_node_service` in `core.sh`
  - `deploy_fullnode` in `core.sh` with default `enabled=false`
  - `bifrost/values.yaml` with `<chain>_DISABLED`

#### 3. Enable PR

- Update `bifrost/values.yaml` to enable the chain

## Chain Proposal Template

```yaml
Chain Name:
Chain Type: EVM / UTXO / Cosmos / Other
Hardware Requirements: Memory and Storage
Year Started:
Market Cap:
CoinMarketCap Rank:
24hr Volume:
Current DEX Integrations:
Other relevant dApps:
Number of previous hard forks:
```
