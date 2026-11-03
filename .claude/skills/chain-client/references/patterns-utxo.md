# UTXO Chain Client Patterns

Covers BTC, BCH, LTC, DOGE, ZEC implementations.

## Key Files

- **Client**: `bifrost/pkg/chainclients/utxo/client.go`
- **Signer**: `bifrost/pkg/chainclients/utxo/signer.go`
- **Chain-specific**: `bitcoin.go`, `litecoin.go`, `bitcoincash.go`, `dogecoin.go`, `zcash.go`
- **Client internals**: `bifrost/pkg/chainclients/utxo/client_internal.go` (scanning lives here and in `client.go`; no separate scanner file)
- **RPC client**: `bifrost/pkg/chainclients/utxo/rpc/`
- **Shared UTXO utils**: `bifrost/pkg/chainclients/shared/utxo/`
- **Tests**: `bitcoin_test.go`, `bitcoin_mainnet_test.go`, etc.

## Client Struct

```go
type Client struct {
    cfg config.BifrostChainConfiguration
    log zerolog.Logger
    m   *metrics.Metrics
    rpc *rpc.Client

    // signing
    nodePubKey         common.PubKey
    nodePrivKey        *btcec.PrivateKey
    tssKeySigner       tss.ThorchainKeyManager
    signerCacheManager *signercache.CacheManager

    // sync
    wg         *sync.WaitGroup
    signerLock *sync.Mutex
    vaultLocks map[string]*sync.Mutex

    // scanner
    blockScanner    *blockscanner.BlockScanner
    temporalStorage *utxo.TemporalStorage

    // queues
    globalErrataQueue     chan<- types.ErrataBlock
    globalSolvencyQueue   chan<- types.Solvency
    globalNetworkFeeQueue chan<- common.NetworkFee
    stopchan              chan struct{}

    // thornode
    bridge      thorclient.ThorchainBridge
    asgardCache stdatomic.Pointer[utxo.AsgardCache]
}
```

## Constructor Signature

```go
func NewClient(
    thorKeys *thorclient.Keys,
    cfg config.BifrostChainConfiguration,
    server *gotss.TssServer,
    bridge thorclient.ThorchainBridge,
    m *metrics.Metrics,
) (*Client, error)
```

Note: UTXO clients do NOT take `pubkeyMgr` or `poolMgr` — they're simpler. However, UTXO clients (except ZEC) register a `RegisterPublicKey` callback in `loadchains.go`.

## Key Patterns

### Chain-Specific Switches

UTXO clients handle multiple chains via switches on `cfg.ChainID`. When adding a new UTXO chain, audit all switch statements in the package:

- Address formats and network parameters
- Script types (P2PKH, P2SH, P2WPKH, P2WSH)
- Fee rate defaults and limits
- Dust thresholds
- Block size and mempool behavior

### Vault Locks

Each vault address gets its own mutex to prevent concurrent signing for the same vault:

```go
c.vaultLocks[vaultAddr].Lock()
defer c.vaultLocks[vaultAddr].Unlock()
```

### Temporal Storage

UTXO clients use `utxo.TemporalStorage` to cache transaction IDs in memory, avoiding repeated disk reads from the block scanner storage.

### UTXO Selection

Transaction signing involves selecting unspent outputs, calculating fees, and creating change outputs. The `TemporalStorage` helps track which UTXOs have been spent but not yet confirmed.

### Errata Queue

UTXO chains use the errata queue to report transaction corrections (e.g., when a transaction is replaced or reorganized).

## Configuration

UTXO-specific config fields in `BifrostChainConfiguration.UTXO`:

```go
UTXO struct {
    BlockCacheCount          uint64
    TransactionBatchSize     int
    MaxMempoolBatches        int
    EstimatedAverageTxSize   uint64
    DefaultMinRelayFeeSats   uint64
    DefaultSatsPerVByte      int64
    MaxSatsPerVByte          int64
}
```

## Signing Pattern

UTXO signing uses `btcec` for key operations:

- Secp256k1 ECDSA signatures
- Uses `btcec.PrivateKey` for local signing
- TSS signing for threshold operations
- Constructs raw Bitcoin-style transactions with inputs, outputs, and witness data
