# EVM Chain Client Patterns

Covers ETH, AVAX, BSC, BASE, POL implementations.

## Key Files

- **Generic EVM client**: `bifrost/pkg/chainclients/evm/client.go`
- **Ethereum-specific client**: `bifrost/pkg/chainclients/ethereum/ethereum.go`
- **EVM scanner**: `bifrost/pkg/chainclients/evm/evm_block_scanner.go`
- **Shared EVM utils**: `bifrost/pkg/chainclients/shared/evm/`
  - `KeySignWrapper`: wraps TSS signing for EVM transactions
  - `EthRPC`: Ethereum RPC client abstraction
  - `TokenManager`: ERC20 token management
  - `SmartContractLogParser`: ABI parsing and event decoding
  - `BlockMetaAccessor`: block metadata caching (LevelDB)
- **Tests**: `client_test.go`, `evm_block_scanner_test.go`

## Two EVM Implementations

1. **`evm.EVMClient`** — Generic EVM client used by AVAX, BSC, BASE, POL
2. **`ethereum.Client`** — Ethereum-only client, a fully independent implementation (it does _not_ embed or wrap `EVMClient`)

Both implement `ChainClient`, and both import the helpers in `bifrost/pkg/chainclients/shared/evm/`, but neither
imports the other — `loadchains.go` dispatches ETH to `ethereum.NewClient` and AVAX/BSC/BASE/POL to `evm.NewEVMClient`.
ETH has its own package because it was the first EVM chain and has historical differences. A fix in `evm/client.go`
therefore does **not** reach ETH; the two must be patched separately.

## Client Struct (Generic EVM)

```go
type EVMClient struct {
    logger             zerolog.Logger
    cfg                config.BifrostChainConfiguration
    localPubKey        common.PubKey
    kw                 *evm.KeySignWrapper
    ethClient          *ethclient.Client
    evmScanner         *EVMScanner
    bridge             thorclient.ThorchainBridge
    blockScanner       *blockscanner.BlockScanner
    vaultABI           *abi.ABI
    pubkeyMgr          pubkeymanager.PubKeyValidator
    poolMgr            thorclient.PoolManager
    tssKeySigner       *tss.KeySign
    wg                 *sync.WaitGroup
    stopchan           chan struct{}
    globalSolvencyQueue chan stypes.Solvency
    signerCacheManager *signercache.CacheManager

    // Pending liabilities: tracks signed but unconfirmed tx amounts
    pendingLiabilities map[string]map[uint64]*big.Int
    liabilityMu        sync.Mutex

    // Signing readiness flag — prevents overspending
    signingReady atomic.Bool
}
```

## Constructor Signature

```go
func NewEVMClient(
    thorKeys *thorclient.Keys,
    cfg config.BifrostChainConfiguration,
    server *tssp.TssServer,
    bridge thorclient.ThorchainBridge,
    m *metrics.Metrics,
    pubkeyMgr pubkeymanager.PubKeyValidator,
    poolMgr thorclient.PoolManager,
) (*EVMClient, error)
```

## Key Patterns

### Pending Liabilities

EVM clients track signed-but-unconfirmed transactions to prevent overspending from a vault. The `signingReady` flag is only set to `true` after pending liabilities have been reconstructed on startup.

### Vault ABI

EVM chains interact with a THORChain router smart contract. The vault ABI is embedded in the binary and used for:

- `depositWithExpiry()` — inbound deposits
- Token approvals
- Aggregator calls

### KeySignWrapper

Wraps TSS signing to produce EVM-compatible signatures (v, r, s format).

### Gas Estimation

EVM chains have complex gas estimation:

- Base fee + priority fee (EIP-1559)
- Gas multipliers for tokens and aggregators
- L1 gas fee estimation for L2 chains (Base)
- `MaxGasTipPercentage` configuration

### Token Handling

ERC20 tokens require:

- Router approval before transfer
- `depositWithExpiry` contract call
- Token decimals conversion
- Whitelist validation

## Configuration

EVM-specific config in `BifrostChainConfiguration.EVM`:

```go
EVM struct {
    MaxGasTipPercentage          int
    TokenMaxGasMultiplier        int64
    AggregatorMaxGasMultiplier   int64
    ExtraL1GasFee               int64
    ReconstructLiabilitiesConcurrency int
}
```

## Confirmation Strategy

Varies by chain:

- **AVAX**: 0 (instant finality via Avalanche consensus)
- **ETH**: ~2 blocks
- **BSC**: 3 blocks
- **POL**: 15 blocks
- **BASE**: ~12 blocks (L1 finalization)
