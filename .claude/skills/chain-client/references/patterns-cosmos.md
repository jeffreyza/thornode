# Cosmos Chain Client Patterns

Covers GAIA (Cosmos Hub) and NOBLE implementations.

## Key Files

- **Client**: `bifrost/pkg/chainclients/gaia/cosmos_client.go`
- **Scanner**: `bifrost/pkg/chainclients/gaia/cosmos_block_scanner.go`
- **Account store**: `bifrost/pkg/chainclients/gaia/metadata.go`
- **Mock services**: `cosmos_mock_bankservice.go`, `cosmos_mock_accountservice.go`
- **Tests**: `cosmos_client_test.go`, `cosmos_block_scanner_test.go`

## Client Struct

```go
type CosmosClient struct {
    logger              zerolog.Logger
    cfg                 config.BifrostChainConfiguration
    chainID             string
    txConfig            client.TxConfig
    txClient            txtypes.ServiceClient
    bankClient          btypes.QueryClient
    accountClient       atypes.QueryClient
    accts               *CosmosMetaDataStore
    tssKeyManager       *tss.KeySign
    localKeyManager     *keyManager
    thorchainBridge     thorclient.ThorchainBridge
    storage             *blockscanner.BlockScannerStorage
    blockScanner        *blockscanner.BlockScanner
    signerCacheManager  *signercache.CacheManager
    cosmosScanner       *CosmosBlockScanner
    globalSolvencyQueue chan stypes.Solvency
    wg                  *sync.WaitGroup
    stopchan            chan struct{}
}
```

## Constructor Signature

```go
func NewCosmosClient(
    thorKeys *thorclient.Keys,
    cfg config.BifrostChainConfiguration,
    server *tssp.TssServer,
    thorchainBridge thorclient.ThorchainBridge,
    m *metrics.Metrics,
    pubkeyMgr pubkeymanager.PubKeyValidator,
) (*CosmosClient, error)
```

Note: Cosmos clients take `pubkeyMgr` but NOT `poolMgr`.

## Key Patterns

### Dual Transport (gRPC + CometBFT RPC)

Cosmos clients talk to the node over **two** endpoints — omitting either breaks the client:

gRPC (`CosmosGRPCHost`, `CosmosGRPCTLS`), dialed in `NewCosmosClient`:

- Bank query service for balance checks
- Account query service for sequence/account number
- Tx service for broadcasting and tx lookup

CometBFT RPC (`RPCHost`), via `rpcclienthttp.New(rpcHost, "/websocket")`:

- `NewCosmosBlockScanner` uses `Block` / `BlockResults` for `GetHeight`, `GetBlock`, and block results
- The client keeps its own CometBFT client for `UnconfirmedTxs` mempool lookups

### Account Sequence Tracking

The `CosmosMetaDataStore` tracks account sequences (nonces) to avoid sequence mismatches:

```go
type CosmosMetaDataStore struct {
    accts map[common.PubKey]*CosmosMetaData
    lock  *sync.Mutex
}
```

### Transaction Signing

Cosmos SDK signing uses:

- Protobuf-encoded transactions
- `SIGN_MODE_DIRECT` for signing
- Amino codec for legacy compatibility
- Secp256k1 keys (same as EVM/UTXO)

### Multi-Chain Support

The same `CosmosClient` handles both GAIA and NOBLE chains, differentiated by:

- `cfg.ChainID` — the THORChain chain identifier
- `cfg.CosmosChainID` — the native Cosmos chain-id (e.g., "cosmoshub-4", "noble-1")
- Different native denoms (uatom for GAIA, uusdc for NOBLE)

### Success Codes

Cosmos transactions can return multiple "success" codes:

```go
var CosmosSuccessCodes = map[uint32]bool{
    SuccessABCICode:               true,
    ErrTxInMempoolCache.ABCICode(): true,
    ErrWrongSequence.ABCICode():   true,
}
```

## Configuration

Cosmos-specific config fields:

```go
CosmosGRPCHost  string  // gRPC endpoint (e.g., "localhost:9090")
CosmosGRPCTLS   bool    // Enable TLS for gRPC
CosmosChainID   string  // Native chain-id (e.g., "cosmoshub-4")
```

## Confirmation Strategy

Cosmos chains have instant finality (Tendermint/CometBFT consensus), so `GetConfirmationCount` returns 0.
