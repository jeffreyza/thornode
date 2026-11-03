# Chain Client Interfaces

This document defines the three interfaces that every chain client must implement.

## ChainClient Interface

**File**: `bifrost/pkg/chainclients/shared/types/types.go`

```go
type ChainClient interface {
    // Start starts the chain client with the given queues.
    Start(
        globalTxsQueue chan types.TxIn,
        globalErrataQueue chan types.ErrataBlock,
        globalSolvencyQueue chan types.Solvency,
        globalNetworkFeeQueue chan common.NetworkFee,
    )

    // Stop stops the chain client.
    Stop()

    // IsBlockScannerHealthy returns true if the block scanner is healthy.
    IsBlockScannerHealthy() bool

    // SignTx returns the signed transaction, a checkpoint, and an instant observation.
    // The 3rd return value (*TxInItem) enables instant observations.
    SignTx(tx types.TxOutItem, height int64) (signedTx []byte, checkpoint []byte, observation *types.TxInItem, err error)

    // BroadcastTx broadcasts the transaction and returns the transaction hash.
    BroadcastTx(_ types.TxOutItem, _ []byte) (string, error)

    // GetHeight returns the current height of the chain.
    GetHeight() (int64, error)

    // GetAddress returns the address for the given public key.
    GetAddress(poolPubKey common.PubKey) string

    // GetAccount returns the account for the given public key.
    GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error)

    // GetAccountByAddress returns the account for the given address.
    GetAccountByAddress(address string, height *big.Int) (common.Account, error)

    // GetChain returns the chain.
    GetChain() common.Chain

    // GetConfig returns the chain configuration.
    GetConfig() config.BifrostChainConfiguration

    // OnObservedTxIn is called when a new observed tx is received.
    OnObservedTxIn(txIn types.TxInItem, blockHeight int64)

    // GetConfirmationCount returns the confirmation count for the given tx.
    GetConfirmationCount(txIn types.TxIn) int64

    // ConfirmationCountReady returns true if the confirmation count is ready.
    ConfirmationCountReady(txIn types.TxIn) bool

    // GetBlockScannerHeight returns block scanner height for chain.
    GetBlockScannerHeight() (int64, error)

    // GetLatestTxForVault returns last observed and broadcasted tx for a vault.
    GetLatestTxForVault(vault string) (string, string, error)

    // RollbackBlockScanner rolls back the block scanner to re-observe recent blocks.
    RollbackBlockScanner() error
}
```

### Method Implementation Notes

| Method                 | Key Behavior                                                                                           |
| ---------------------- | ------------------------------------------------------------------------------------------------------ |
| `Start`                | Store queue channels, start TSS keysigner, start block scanner, launch solvency runner goroutine       |
| `Stop`                 | Close stop channel, wait on WaitGroup, stop block scanner and TSS signer                               |
| `SignTx`               | Validate tx, construct chain-specific tx, sign via TSS or local key, return signed bytes + observation |
| `BroadcastTx`          | Send signed tx to chain RPC, mark as signed in signer cache, return txid                               |
| `GetConfirmationCount` | Return 0 for instant-finality chains, N for probabilistic chains                                       |
| `OnObservedTxIn`       | Record observation in signer cache for tracking                                                        |
| `GetLatestTxForVault`  | Query signer cache for latest observed/broadcast tx hashes                                             |

## BlockScannerFetcher Interface

**File**: `bifrost/blockscanner/blockscanner.go`

```go
type BlockScannerFetcher interface {
    // FetchMemPool scans the mempool at the given height.
    FetchMemPool(height int64) (types.TxIn, error)

    // FetchTxs scans a block at fetchHeight (chainHeight is the latest chain height).
    FetchTxs(fetchHeight, chainHeight int64) (types.TxIn, error)

    // GetHeight returns current block height.
    GetHeight() (int64, error)

    // GetNetworkFee returns current network fee details.
    GetNetworkFee() (transactionSize, transactionFeeRate uint64)
}
```

This interface is typically implemented by a separate scanner struct (e.g., `EVMScanner`, `CosmosBlockScanner`, `SOLScanner`) rather than the main client struct. The scanner is then passed to `blockscanner.NewBlockScanner()`.

## SolvencyCheckProvider Interface

**File**: `bifrost/pkg/chainclients/shared/runners/solvency.go`

```go
type SolvencyCheckProvider interface {
    GetHeight() (int64, error)
    ShouldReportSolvency(height int64) bool
    ReportSolvency(height int64) error
}
```

The solvency runner is started in the client's `Start()` method:

```go
func (c *Client) Start(...) {
    // ... start block scanner, etc.
    c.wg.Add(1)
    go runners.SolvencyCheckRunner(
        c.GetChain(),
        c,              // implements SolvencyCheckProvider
        c.bridge,
        c.stopchan,
        c.wg,
        constants.ThorchainBlockTime,
    )
}
```

`ShouldReportSolvency` typically checks if enough blocks have passed since the last solvency check (using `SolvencyBlocks` from config). `ReportSolvency` queries vault balances on-chain and reports them via the solvency queue.
