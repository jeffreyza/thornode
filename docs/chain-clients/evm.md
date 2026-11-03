# EVM Chain Clients

THORChain supports several EVM-compatible chains: Ethereum, Avalanche, BNB Chain (BSC), Base, and Polygon (POL). There are two EVM client implementations:

- **Ethereum** has a dedicated client at `/bifrost/pkg/chainclients/ethereum/ethereum.go` with custom logic for gas estimation, smart contract detection, and dynamic confirmation counting.
- **Other EVM chains** (AVAX, BSC, BASE, POL) use the shared generic client at `/bifrost/pkg/chainclients/evm/client.go`.

## Shared Architecture

The shared EVM client (`evm/client.go`) provides:

- ABI-based decoding of inbound Router events
- EIP-55 address formatting
- Nonce caching and outbound coordination
- Router interaction for ERC-20 deposits
- Mempool scanning and reorg detection

Shared utilities used by both the Ethereum and generic EVM clients live in `bifrost/pkg/chainclients/shared/evm/`, including the `KeySignWrapper` (TSS-to-EVM signature formatting), `TokenManager`, `SmartContractLogParser`, and `BlockMetaAccessor`.

## Router Design

EVM chains rely on a **Router contract** for ERC-20 asset support. While native ETH can be sent directly to the vault, all ERC-20 token deposits must pass through the Router.

The Router enables:

- Vaults to pull tokens using `transferFrom`
- Memo inclusion via emitted Router events
- Per-vault allowances for gas efficiency and simplicity
- Unified logic without needing vault contracts

For examples, see [Sending EVM Transactions](../concepts/sending-transactions.md#evm-chains).

## Inbound Observations

Inbound transactions are detected by:

- Watching Router contracts using `eth_getLogs`
- Parsing `Memo(string)` and `Transfer` events via ABI decoding
- Tracking native ETH deposits where `to == vaultAddress`

See:

- [Inbound Transactions](../bifrost/how-bifrost-works.md#observing-inbound-transactions)
- [Sending EVM Transactions](../concepts/sending-transactions.md#evm-chains)

## Confirmation Counting

EVM transactions require **delayed finality** due to frequent re-orgs. THORChain uses dynamic confirmation logic.

### Ethereum

- Ethereum uses dynamic confirmation counting based on transaction value, similar to UTXO chains
- Pool pricing from THORChain is used to estimate value
- Maximum confirmations are governed by `MAXCONFIRMATIONS-ETH` in Mimir
- Minimum of 2 confirmations enforced (PoS)

### Other EVM Chains

The generic EVM client uses hard-coded confirmation counts per chain:

| Chain     | Confirmations | Reason                            |
| --------- | ------------- | --------------------------------- |
| Avalanche | 0             | Instant finality                  |
| BSC       | 3             | ~2.5 blocks required for finality |
| Base      | 12            | ~2 Ethereum blocks for L1 parity  |
| Polygon   | 15            | Finality threshold                |

See [Finality & Confirmation Counting](../bifrost/how-bifrost-works.md#finality--pre-confirmation).

## Stuck Transaction Recovery

Unlike UTXO chains (which support CPFP), EVM chains do **not** allow fee bumping via future transactions. Each transaction must use a **unique nonce**, and replacement is only possible with the **same nonce** and a **higher gas price**.

A stuck nonce blocks all future transactions from that vault. To recover, THORChain applies an **Unstuck Mechanism**:

### Detection

- Runs every block
- Flags transactions that have been unconfirmed for more than `SigningTransactionPeriod - RescheduleBufferBlocks` blocks

### Replacement Process

- Generates a **0-value self-to-self** transaction from the vault
- Uses the **same nonce** as the stuck transaction
- Applies a higher gas price using the following logic:

  - **Minimum**: 110% of the original transaction's gas price
  - **Target**: 2× the current median network gas price
  - **Final**: whichever is higher between the two

### Execution

- The unstuck transaction is signed and broadcast by Bifrost
- The nonce cache is updated to replace the old transaction
- Any signing errors halt the process to avoid repeated failures

> This unstuck logic is specific to EVM chains. UTXO chains use alternative mechanisms.

## Gas Estimation

Outbound EVM gas fees are estimated based on:

- A **default gas limit** (typically 200,000 units)
- A **target gas price** set to \~1.5× the network median

The gas limit is used as the actual gas cost is not known until inclusion. THORChain generally **charges users based on \~80,000 gas units** to avoid overcharging.

See [Gas Tracking](../bifrost/how-bifrost-works.md#gas-tracking).

## Reorg Handling

EVM chains are reorg-prone. Bifrost handles this by:

- Rescanning the last `N` blocks
- Emitting an `ErrataTx` if a previously observed transaction is missing
- Reverting state changes (vault balances, etc.)

See [Re-orgs & Errata](../bifrost/how-bifrost-works.md#re-orgs--errata).

## Vault Address Derivation

Vault addresses are derived from compressed ECDSA public keys using:

```go
keccak256(pubkey)[12:]
```

All outbound transactions use **EIP-55 checksummed** formatting.

## Related Pages

- [Chain Clients Overview](./README.md)
- [How Bifrost Works](../bifrost/how-bifrost-works.md)
- [Sending Transactions](../concepts/sending-transactions.md)
- [Memos](../concepts/memos.md)
