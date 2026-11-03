# Chain Clients

THORChain supports multiple Layer 1 (L1) blockchains via **chain clients**, which are pluggable modules under the Bifrost subsystem. Each client enables THORChain to:

- Detect inbound transactions
- Construct and sign outbound transactions
- Track balances and confirm vault solvency
- Handle chain-specific logic like mempool handling, gas tracking, re-orgs, and finality

Chain clients are located in:

```text
/bifrost/pkg/chainclients
```

## Structure and Composition

Each chain lives in its own folder and typically inherits logic from a shared implementation:

| Client Folder | Purpose                 | Used For                       |
| ------------- | ----------------------- | ------------------------------ |
| `ethereum/`   | Ethereum-specific logic | ETH                            |
| `evm/`        | Shared EVM logic        | AVAX, BSC, BASE, POL           |
| `utxo/`       | Shared UTXO logic       | BTC, LTC, BCH, DOGE, ZEC, DASH |
| `gaia/`       | Cosmos SDK-based chains | GAIA, NOBLE                    |
| `solana/`     | Solana-specific logic   | SOL                            |
| `tron/`       | Tron-specific logic     | TRON                           |
| `xrp/`        | XRP-specific logic      | XRP                            |
| `monero/`     | Monero-specific logic   | XMR                            |

Chains that do not fit a shared model can implement their own logic from scratch. For UTXO-based chains, the `Client` is constructed using the shared implementation in `utxo/client.go`, which handles block scanning, mempool fetches, reorg tracking, and solvency reporting. Each specific chain (BTC, LTC, DOGE, etc.) has a per-chain file (e.g., `utxo/bitcoin.go`, `utxo/litecoin.go`) that provides chain-specific configuration.

For EVM-based chains, shared functionality is implemented in `evm/client.go` and reused by non-Ethereum EVM chains (AVAX, BSC, BASE, POL). Ethereum has its own dedicated client in `ethereum/ethereum.go` with custom logic for gas estimation, smart contract detection, and confirmation counting.

Cosmos SDK chains (GAIA, NOBLE) share a single `CosmosClient` implementation in `gaia/cosmos_client.go`, differentiated by chain ID configuration.

Chains that share no common model — XRP, Tron, Solana, and Monero — implement the `ChainClient` interface directly in their own folder. See [Standalone Chains](./standalone.md).

## ChainClient Interface

Every chain client in Bifrost must implement the `ChainClient` interface defined in [`types/types.go`](https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/shared/types/types.go). It encapsulates the lifecycle of chain interactions — scanning, observation, signing, broadcasting, solvency reporting, and account inspection. Clients may override additional chain-specific behavior using optional interfaces or embedded logic.

```go
// ChainClient defines the behavior a chain client must implement.
type ChainClient interface {
	// Start begins the chain client with queues for txs, errata, solvency, and network fees.
	Start(
		globalTxsQueue chan types.TxIn,
		globalErrataQueue chan types.ErrataBlock,
		globalSolvencyQueue chan types.Solvency,
		globalNetworkFeeQueue chan common.NetworkFee,
	)

	// Stop halts the chain client and any background processes.
	Stop()
	// IsBlockScannerHealthy reports if the block scanner is functioning normally.
	IsBlockScannerHealthy() bool
	// SignTx prepares and returns the signed transaction bytes.
	SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error)
	// BroadcastTx sends the signed transaction to the chain.
	BroadcastTx(tx types.TxOutItem, rawTx []byte) (string, error)
	// GetHeight returns the current on-chain block height.
	GetHeight() (int64, error)
	// GetAddress derives an address from a given vault public key.
	GetAddress(poolPubKey common.PubKey) string
	// GetAccount returns the on-chain account details at a specific block height.
	GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error)
	// GetAccountByAddress returns account details by address.
	GetAccountByAddress(address string, height *big.Int) (common.Account, error)
	// GetChain returns the chain this client handles (e.g., BTC, ETH).
	GetChain() common.Chain
	// GetConfig returns chain-specific Bifrost configuration.
	GetConfig() config.BifrostChainConfiguration
	// OnObservedTxIn processes inbound transactions observed from the scanner.
	OnObservedTxIn(txIn types.TxInItem, blockHeight int64)
	// GetConfirmationCount returns how many confirmations a transaction has.
	GetConfirmationCount(txIn types.TxIn) int64
	// ConfirmationCountReady determines if a transaction has reached finality.
	ConfirmationCountReady(txIn types.TxIn) bool
	// GetBlockScannerHeight returns the block height tracked by the scanner.
	GetBlockScannerHeight() (int64, error)
	// GetLatestTxForVault fetches the last inbound and outbound tx for a vault.
	GetLatestTxForVault(vault string) (string, string, error)
	// RollbackBlockScanner rewinds the block scanner to reprocess recent blocks.
	RollbackBlockScanner() error
}
```

### Solvency Reporter

Bifrost also defines a solvency check function type:

```go
// SolvencyReporter reports the solvency of the chain at the given height.
type SolvencyReporter func(height int64) error
```

Solvency reports ensure THORChain’s view of vault balances matches on-chain state, helping detect drift or failures in observation.

## Observer

The Observer component is responsible for detecting relevant inbound transactions to THORChain vaults. Each Observer implementation runs continuously and:

1. Monitors new blocks from the connected full node.
2. Extracts transactions involving THORChain vault addresses.
3. Constructs `ObservedTx` structs.
4. Broadcasts valid observations for consensus.

Observer accuracy is critical for:

- Preventing missed deposits
- Triggering appropriate finality logic (pre-confirmation)
- Tracking aggregator metadata (e.g., transferOutAndCall)

Inbound transactions must be:

- Sent to an `Active` or `Retiring` vault
- Observed by at least 67% of nodes to be finalized
- Filtered for chain-specific rules (e.g., XRP tags, Cosmos memos)

## BlockScanner

The BlockScanner component is responsible for scanning the blockchain to detect inbound activity. It is used on **most chains**, and is required for UTXO chains like Bitcoin, Litecoin, and Dogecoin, where mempool visibility and historical block scanning are essential for:

- Detecting missed or orphaned transactions
- Reorg recovery and ErrataTx detection
- Solvency reporting and block-level metadata
- Fetching mempool transactions to speed up inbound reporting

### Required Interface

The `BlockScannerFetcher` interface is defined in `bifrost/blockscanner/blockscanner.go`:

```go
// BlockScannerFetcher defines the methods a block scanner must implement
type BlockScannerFetcher interface {
    // FetchMemPool scan the mempool
    FetchMemPool(height int64) (types.TxIn, error)
    // FetchTxs scan block with the given height
    FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error)
    // GetHeight return current block height
    GetHeight() (int64, error)
    // GetNetworkFee returns current network fee details
    GetNetworkFee() (transactionSize, transactionFeeRate uint64)
}
```

Chains that support mempool visibility (e.g., Cosmos SDK, Solana) also use this interface for near-instant detection of new txs. For EVM chains, where event subscriptions are the primary mechanism, BlockScanner is optional but sometimes included for fallback and deep scan support.

## Vault Address Derivation

Each chain client must derive vault addresses from TSS public keys using the appropriate signature scheme:

| Chain Type | Algorithm | Derivation Example                        |
| ---------- | --------- | ----------------------------------------- |
| Bitcoin    | ECDSA     | P2WPKH from compressed pubkey             |
| Ethereum   | ECDSA     | `keccak256(pubkey)[12:]`, EIP-55 checksum |
| Solana     | EDDSA     | Base58 from ed25519                       |

THORChain provides both `PubKey` and `PubKeyEddsa` for each vault. Clients derive addresses based on the chain's native format.

See the full list of [support address formats here](../concepts/querying-thorchain.md#supported-address-formats).

## Shared Responsibilities

All chain clients must handle the following responsibilities consistently:

### Observing Inbound Transactions

- Clients scan new blocks and detect transactions to THORChain-controlled vaults.
- Detected transactions are wrapped in `ObservedTx` structs and submitted via `MsgObservedTxIn`.
- Finality is enforced based on chain type (instant vs delayed finality).

### Signing and Broadcasting Outbound Transactions

- Clients receive `TxOut` instructions from THORChain to spend funds from vaults.
- Bifrost handles TSS signing.
- Clients broadcast the transaction and submit `MsgObservedTxOut`.
- Outbound transactions are also instantly observed using [AutoObserve](../bifrost/how-bifrost-works.md#autoobserve)

### Confirmation Counting

- For delayed-finality chains (UTXO, Ethereum), transactions require dynamic confirmations based on transaction value relative to block rewards.

- **UTXO chains** (BTC, BCH, LTC, DOGE, ZEC — see `utxo/client_internal.go`) compute a confirmation value from `ConfMultiplier` (a basis-points parameter) and block reward, then divide the transaction value by it:

```text
  confValue = (ConfMultiplier × BlockReward) / MaxBasisPts
  confirm   = TxValue / confValue
  confirm   = min(confirm, MAXCONFIRMATIONS)
  confirm   = max(confirm, MinConfirmations)
```

- **Ethereum** (see `ethereum/ethereum.go`) uses the same shape but applies an additional `×2` safety multiplier to `TxValue` and enforces a hard floor of 2 confirmations to defend against post-Merge reorgs:

```text
  confValue = (ConfMultiplier × BlockReward) / MaxBasisPts
  confirm   = (TxValue × 2) / confValue
  confirm   = min(confirm, MAXCONFIRMATIONS)
  confirm   = max(confirm, 2)
```

- Finality is only reached after this threshold is met.

- Chains with instant finality (Cosmos, Solana, XRP, Avalanche) return 0 confirmations.

- Some EVM chains use hard-coded confirmation counts: BSC (3), BASE (12), POL (15).

`MAXCONFIRMATIONS` values for dynamic chains are defined per chain in Mimir (e.g., `MAXCONFIRMATIONS-BTC`, `MAXCONFIRMATIONS-ETH`).

### Re-org Detection

- Clients must detect chain reorganizations.
- Transactions removed by a re-org are reported as `ErrataTx`.

### Stuck or Dropped Transactions

- EVM clients must handle nonce gaps by replacing stuck transactions with a higher gas self-send (the "unstuck tx").
- UTXO clients should support CPFP (Child Pays For Parent) and RBF (Replace-by-Fee) to resolve stuck sends.

## Token Whitelists

For chains that support tokens (EVM, Solana), THORChain maintains per-chain whitelists to reduce attack surface. These lists are JSON files stored in the Thornode repository and periodically fetched by clients.

See [token-lists.md](./token-lists.md) for details.

## Claude Code Skill

A Claude Code skill is available to assist with building, modifying, and verifying chain clients. The `/chain-client` skill provides:

- **Decision tree** for choosing the right workflow (new chain, modify existing, or verify)
- **Step-by-step guidance** for implementing all required interfaces (`ChainClient`, `Observer`, `Signer`, `BlockScanner`)
- **Chain family patterns** with reference docs for UTXO, EVM, Cosmos, and custom chain types
- **Known bugs database** covering 80+ historical mainnet bugs organized by category
- **Testing and simulation** checklists for unit tests, regression tests, and mocknet verification

To use it, run `/chain-client` in Claude Code or describe a chain client task (e.g., "add support for chain X", "review the Solana chain client", "fix stuck outbounds on ETH").

## Related Pages

- [How Bifrost Works](../bifrost/how-bifrost-works.md)
- [Vault Behaviors](../bifrost/vault-behaviors.md)
- [Integrating New Chains](../new-chains/implementation-guide.md)
- [Sending Transactions](../concepts/sending-transactions.md)
- [Standalone Chains](./standalone.md)
