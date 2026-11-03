# Feature Guide

This section provides in-depth guides to key features within the THORChain protocol. These features expand the core capabilities of THORChain, enabling developers and advanced users to build richer cross-chain applications, automate complex workflows, and interact with new primitives.

Each guide covers:

- The purpose and motivation behind the feature
- How it works at the protocol level
- How to use it correctly (memos, limits, API behavior)
- Best practices and edge cases to be aware of

## Included Guides

- **[TCY Guide](tcy.md)** — How TCY units are calculated and used for tracking swapper volume and staking weight.
- **[Streaming Swaps](../swap-guide/streaming-swaps.md)** — Break a swap into multiple parts over time to get a more consistent price (TWAP-style execution).
- **[Trade Accounts](trade-accounts.md)** — Provides professional traders (mostly arbitrage bots) a method to execute instant trades on THORChain without involving Layer1 transactions on external blockchains.
- **[Secured Assets](secured-assets.md)** — Allows L1 tokens to be deposited to THORChain, creating a new native asset, which can be transferred between accounts and integrated with CosmWasm smart contracts using standard Cosmos SDK messages.
- **[RUNE Pool](rune-pool.md)** — Deep dive into the RUNE-only liquidity pool and its implications for bonding and liquidity management.
- **[Swapper Clout](swapper-clout.md)** — A score that reflects a user's swap volume over time and can influence incentives.

> These guides are ideal for developers building THORChain-integrated applications or validators who want to understand feature-level behavior.
