# Other Chain Client Patterns

Covers Solana (SOL), XRP, and Tron (TRON) — chains that don't fit into UTXO, EVM, or Cosmos families.

---

## Solana (SOL)

### Key Files

- **Client**: `bifrost/pkg/chainclients/solana/sol_client.go`
- **Scanner**: `bifrost/pkg/chainclients/solana/sol_scanner.go`
- **RPC**: `bifrost/pkg/chainclients/solana/rpc/`
- **Types**: `bifrost/pkg/chainclients/solana/types/`
- **Tests**: `sol_client_test.go`

### Client Struct

```go
type SOLClient struct {
    logger               zerolog.Logger
    cfg                  config.BifrostChainConfiguration
    localPubKey          string
    k                    *cosmoscryptoed25519.PrivKey  // Ed25519!
    solScanner           *SOLScanner
    bridge               thorclient.ThorchainBridge
    poolMgr              thorclient.PoolManager
    tssKeyManager        tss.ThorchainKeyManager
    wg                   *sync.WaitGroup
    stopchan             chan struct{}
    globalSolvencyQueue  chan stypes.Solvency
    signerCacheManager   *signercache.CacheManager
    rpcClient            *rpc.SolRPC
    solvencySlotMultiple int64
}
```

### Key Differences

- **Ed25519 keys** instead of secp256k1 — uses `cosmoscryptoed25519.PrivKey`
- **Base58 encoding** for addresses and transaction IDs
- **Slot-based** height tracking instead of block numbers
- **"Finalized" commitment** level for queries — gives instant finality (`GetConfirmationCount` returns 0)
- **Broadcast lookup delay** — waits 3 seconds after broadcast error before checking if tx landed

### Constructor

```go
func NewSOLClient(
    thorKeys *thorclient.Keys,
    cfg config.BifrostChainConfiguration,
    server *tssp.TssServer,
    bridge thorclient.ThorchainBridge,
    m *metrics.Metrics,
    pubkeyMgr pubkeymanager.PubKeyValidator,
    poolMgr thorclient.PoolManager,
) (*SOLClient, error)
```

---

## XRP

### XRP Key Files

- **Client**: `bifrost/pkg/chainclients/xrp/client.go`
- **Scanner**: `bifrost/pkg/chainclients/xrp/block_scanner.go`
- **Key manager**: `bifrost/pkg/chainclients/xrp/keymanager/` (secp256k1-based)
- **Tests**: `client_test.go`, `block_scanner_test.go`

### XRP Client Struct

```go
type Client struct {
    logger              zerolog.Logger
    cfg                 config.BifrostChainConfiguration
    accts               *XrpMetaDataStore
    tssKeyManager       *tss.KeySign
    localKeyManager     *keymanager.KeyManager
    thorchainBridge     thorclient.ThorchainBridge
    storage             *blockscanner.BlockScannerStorage
    blockScanner        *blockscanner.BlockScanner
    signerCacheManager  *signercache.CacheManager
    xrpScanner          *XrpBlockScanner
    globalSolvencyQueue chan stypes.Solvency
    wg                  *sync.WaitGroup
    stopchan            chan struct{}
    rpcClient           *rpc.Client
    networkID           uint32
}
```

### XRP Key Differences

- **Secp256k1** keys but with XRP-specific key manager (`keymanager/secp256k1/`)
- **Account sequence tracking** via `XrpMetaDataStore` (similar to Cosmos)
- **Binary codec** for transaction serialization (`github.com/Peersyst/xrpl-go/binary-codec`)
- **Network ID** configuration for mainnet/testnet
- **Instant finality** — `GetConfirmationCount` returns 0

### XRP Constructor

```go
func NewClient(
    thorKeys *thorclient.Keys,
    cfg config.BifrostChainConfiguration,
    server *tssp.TssServer,
    bridge thorclient.ThorchainBridge,
    m *metrics.Metrics,
) (*Client, error)
```

Note: XRP's constructor is simpler — no `pubkeyMgr` or `poolMgr`.

---

## Tron (TRON)

### Tron Key Files

- **Client**: `bifrost/pkg/chainclients/tron/tron_client.go`
- **Scanner**: `bifrost/pkg/chainclients/tron/tron_block_scanner.go`
- **API client**: `bifrost/pkg/chainclients/tron/api/`
- **RPC client**: `bifrost/pkg/chainclients/tron/rpc/`
- **ABI**: `bifrost/pkg/chainclients/tron/abi/trc20.json`
- **Mock servers**: `bifrost/pkg/chainclients/tron/api/mock_server.go`, `bifrost/pkg/chainclients/tron/rpc/mock_server.go` (using `//go:embed`)
- **Tests**: `tron_client_test.go`, `tron_block_scanner_test.go`

### Tron Client Struct

```go
type TronClient struct {
    logger             zerolog.Logger
    config             tcconfig.BifrostChainConfiguration
    chainId            string
    blockScanner       *blockscanner.BlockScanner
    storage            *blockscanner.BlockScannerStorage
    signerCacheManager *signercache.CacheManager
    tssKeyManager      *tss.KeySign
    localKeyManager    *KeyManager
    tronScanner        *TronBlockScanner
    api                *api.TronApi
    rpc                *rpc.TronRpc
    abi                abi.ABI
    whitelist          map[string]tokenlist.ERC20Token
    // ...
}
```

### Tron Key Differences

- **Dual client architecture** — both API (`TronApi`) and RPC (`TronRpc`) clients
- **ECDSA signing** (secp256k1, similar to EVM) but with Tron-specific transaction format
- **TRC20 token whitelist** — only whitelisted tokens are processed
- **20-minute transaction validity** — `TimestampValidity = 20 * time.Minute`
- **Embedded ABI** — `//go:embed abi/trc20.json` for TRC20 contract interaction
- **Embedded test fixtures** — mock servers use `//go:embed test-tron/*`
- **Confirmation strategy**: 1 confirmation block + 19 finality blocks

### Tron Constructor

```go
func NewTronClient(
    thorKeys *thorclient.Keys,
    cfg tcconfig.BifrostChainConfiguration,
    server *tctss.TssServer,
    bridge thorclient.ThorchainBridge,
    m *tcmetrics.Metrics,
    pubkeyMgr pubkeymanager.PubKeyValidator,
    poolMgr thorclient.PoolManager,
) (*TronClient, error)
```

---

## Common Pattern Across All "Other" Chains

Despite their differences, all these chains follow the same structural pattern:

1. Constructor validates args, creates RPC client, sets up TSS, creates scanner
2. `Start()` starts block scanner and solvency runner
3. `Stop()` closes stop channel and waits on WaitGroup
4. `SignTx()` validates tx, constructs chain-specific tx, signs, returns observation
5. `BroadcastTx()` sends to chain RPC and records in signer cache
