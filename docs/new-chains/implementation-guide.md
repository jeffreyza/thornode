# Chain Client Implementation Guide

This guide explains how to implement a new `ChainClient` in THORChain's Bifrost module. It assumes the chain has passed the [evaluation process](./evaluating-new-chains.md) and received [Node Mimir approval](./new-chain-process.md).

Chain clients enable THORChain to:

- Observe inbound L1 transactions
- Sign and broadcast outbound vault transactions
- Track vault balances and emit solvency reports
- Handle chain-specific behaviors like mempool handling, reorgs, and gas estimation

> Most chains extend existing clients — see [EVM](../chain-clients/evm.md), [UTXO](../chain-clients/utxo.md), or [BFT](../chain-clients/bft.md).

## Directory Layout

All chain clients live under:

```text
/bifrost/pkg/chainclients
```

If your chain fits an existing type (EVM, UTXO, Cosmos), you **should place your client in the corresponding shared folder** and extend the base implementation. This allows you to reuse common logic and minimize custom code.

| Folder      | Type   | Purpose / Notes                            | Example Chains           |
| ----------- | ------ | ------------------------------------------ | ------------------------ |
| `ethereum/` | Custom | Ethereum-specific EVM client               | ETH                      |
| `evm/`      | Shared | Generic EVM client for non-Ethereum chains | AVAX, BSC, BASE, POL     |
| `utxo/`     | Shared | For Bitcoin-style UTXO chains              | BTC, LTC, BCH, DOGE, ZEC |
| `gaia/`     | Shared | For Cosmos SDK / Tendermint chains         | GAIA, NOBLE              |
| `solana/`   | Custom | Custom Solana client                       | SOL                      |
| `tron/`     | Custom | Custom TRON client                         | TRON                     |
| `xrp/`      | Custom | Custom Ripple client                       | XRP                      |

### How to Choose

- Use an existing shared type folder (`evm/`, `utxo/`, `gaia/`) if your chain's architecture matches.
- Use a new folder (e.g. `solana/`, `xrp/`, `tron/`) only if the chain requires custom observation, signing, or RPC handling that doesn't align with existing types.

Example:

- `utxo/bitcoin.go` – Bitcoin-specific config using shared UTXO logic in `utxo/client.go`
- `evm/client.go` – Generic EVM client used by AVAX, BSC, BASE, POL
- `ethereum/ethereum.go` – Ethereum-specific client with custom gas and confirmation logic
- `gaia/cosmos_client.go` – Cosmos client used by GAIA and NOBLE
- `xrp/client.go` – XRP-specific logic using custom implementation

## Required Interfaces

You must implement the following core interfaces:

| Interface               | Defined In                             | Purpose                                            |
| ----------------------- | -------------------------------------- | -------------------------------------------------- |
| `ChainClient`           | `shared/types/types.go`                | Main interface for observation, signing, solvency  |
| `BlockScannerFetcher`   | `bifrost/blockscanner/blockscanner.go` | Scans blocks, mempool, and reports network fees    |
| `SolvencyCheckProvider` | `shared/runners/solvency.go`           | Solvency reporting (height, should-report, report) |

See the [`ChainClient` interface](../chain-clients/README.md#chainclient-interface) and [`BlockScannerFetcher` interface](../chain-clients/README.md#required-interface) for method-level detail.

## Vault Address Derivation

Every client must derive vault addresses from the TSS public key:

| Chain    | Algo  | Method                                |
| -------- | ----- | ------------------------------------- |
| Bitcoin  | ECDSA | P2WPKH from compressed pubkey         |
| Ethereum | ECDSA | `keccak256(pubkey)[12:]`, checksummed |
| Solana   | EDDSA | Base58-encoded `ed25519`              |

Use:

```go
btcec.PublicKey.SerializeCompressed()     // ECDSA
edwards25519.PublicKey.Bytes()            // EDDSA
```

Implement:

```go
func (c *YourChain) GetAddress(pubkey common.PubKey) string
```

- Handle both `PubKey` and `PubKeyEddsa`
- Ensure address format is deterministic

## Observation Logic

Each client must observe inbound txs and forward them to THORChain.

Implement the `BlockScannerFetcher` interface:

```go
func (c *YourScanner) FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error)
func (c *YourScanner) FetchMemPool(height int64) (types.TxIn, error)
func (c *YourScanner) GetHeight() (int64, error)
func (c *YourScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64)
```

Inbound txs must:

- Be directed to active or retiring vaults
- Include a valid THORChain [memo](../concepts/memos.md)
- Be pushed to the global tx queue:

```go
globalTxsQueue <- types.TxIn
```

### Dust Threshold

Prevent spam by defining a dust threshold for your chain in `common/chain.go` via the `DustThreshold()` method. This determines the minimum inbound amount considered valid.

## Memo Parsing

Memos must be:

- Present
- Decoded and parsed via `x/thorchain/memo`
- Rejected if invalid or missing

## Confirmation Counting

Chains with delayed finality (EVM, UTXO) must track confirmations:

```go
func (c *YourChain) GetConfirmationCount(txIn types.TxIn) int64
```

- For chains with instant finality (Cosmos, Solana, XRP, Avalanche), return `0`.
- For chains requiring dynamic confirmations (UTXO, Ethereum), compute based on transaction value relative to block reward using `ConfMultiplier` and cap with `MAXCONFIRMATIONS-<CHAIN>` (set via Mimir).
- Some EVM chains use hard-coded confirmation counts (e.g., BSC=3, BASE=12, POL=15).

## Outbound Signing

Outbound txs are signed by the TSS and passed to your `ChainClient`.

Implement:

```go
SignTx(txOut types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error)
BroadcastTx(txOut types.TxOutItem, rawTx []byte) (string, error)
```

- Encode signature correctly (ECDSA `r,s,v` or EDDSA)
- Return the tx hash
- Submit outbound observation via `MsgOutboundTx`

> THORChain does not verify the tx on-chain — incorrect hashes are slashable.

## Gas Estimation

Clients must report network fees via the `BlockScannerFetcher.GetNetworkFee()` method, which returns the transaction size and fee rate. This data is submitted to THORChain via the network fee queue and used to calculate outbound gas costs.

See [Gas Tracking](../bifrost/how-bifrost-works.md#gas-tracking) for details.

## Reorg Handling

If a previously observed tx disappears due to a reorg:

- Emit an `ErrataTx`
- Allow THORChain to revert state

Ensure short-range reorgs are gracefully handled in block polling logic.

## Solvency Reporting

Clients must emit vault balance data periodically:

```go
globalSolvencyQueue <- types.Solvency
```

Missing funds (beyond [`PermittedSolvencyGap`](../mimir.md)) will halt the chain.

## Stuck Transactions

Clients must detect stuck outbound txs due to:

- Low gas
- Dust
- Mempool eviction

EVM chains must implement the **unstuck logic**:

- Reuse the same nonce
- Send a 0-value tx to self
- Use max(gasPrice × 1.1, 2 × current median gas)

## Chain Registration

New chain clients must be registered in `bifrost/pkg/chainclients/loadchains.go` within the `LoadChains()` function. Add a case to the switch statement mapping your chain constant to its constructor:

```go
case common.YOURChain:
    return yourchain.NewClient(thorKeys, chain, server, thorchainBridge, m, ...)
```

You must also register the chain in `x/thorchain/manager_network_current.go` in the `supportChains` array, and update 30+ locations in the `common/` package (chain constant, gas asset, address derivation, signing algorithm, dust threshold, etc.). See the [Chain Clients Overview](../chain-clients/README.md) for the full list of shared responsibilities.

## Claude Code Skill

The `/chain-client` Claude Code skill can guide you through the full implementation process interactively. It includes decision trees, chain-family-specific patterns, registration checklists, and a database of 80+ historical bugs to avoid. Run `/chain-client` in Claude Code to get started.

## Testing & Simulation

Before submitting your PR:

- Launch the chain on stagenet or testnet
- Test:

  - Inbound observation
  - Outbound signing
  - Vault churn
  - Memo parsing
  - Fee behavior
  - Errata + solvency

- Use:

  - [`thornode/test`](https://gitlab.com/thorchain/thornode/-/tree/develop/test)
  - Local regnet if available

Also see [New Chain Process](./new-chain-process.md#phase-ii-development-and-stagenet-testing) for required test volumes.
