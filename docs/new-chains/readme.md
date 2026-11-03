# New Chain Integrations

This section explains how to evaluate, propose, and implement a new Layer 1 (L1) blockchain integration into THORChain.

## Contents

| Page                                                           | Description                                                                                                                                                             |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [Evaluating New Chains](./evaluating-new-chains.md)            | When and why a new chain should be added. Includes decentralization, ossification, liquidity, and developer standards.                                                  |
| [New Chain Integration Process](./new-chain-process.md)        | Step-by-step process for proposing, approving, testing, and launching a new chain. Includes Node Mimir votes and stagenet requirements.                                 |
| [Chain Client Implementation Guide](./implementation-guide.md) | Technical reference for developers implementing a new `ChainClient` in Bifrost. Includes required interfaces, memo parsing, vault handling, solvency, and testing tips. |

## Claude Code Skill

The `/chain-client` Claude Code skill provides interactive, step-by-step guidance for implementing new chain clients. It covers the full lifecycle — from choosing a chain family and implementing interfaces, to registration, testing, and verification. Run `/chain-client` in Claude Code to get started.

## Related Resources

- [Chain Clients Overview](../chain-clients/README.md)
  System-level overview of how Bifrost interacts with different L1 chains via pluggable chain clients.

- [Vault Behaviors](../bifrost/vault-behaviors.md)
  How vaults are assigned, rotated, and tracked across chains.

- [How Bifrost Works](../bifrost/how-bifrost-works.md)
  Internal transaction flow, finality logic, and scanner design.

- [Sending Transactions](../concepts/sending-transactions.md)
  Wallet-facing transaction format guidance.
