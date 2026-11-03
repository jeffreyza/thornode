# Chain Client Testing Guide

## Test Framework

All chain client tests use `gopkg.in/check.v1` (Gocheck) with `net/http/httptest` for mock servers.

### Test Suite Structure

```go
package mypkg

import (
    "testing"
    . "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type MySuite struct {
    thordir  string
    thorKeys *thorclient.Keys
    bridge   thorclient.ThorchainBridge
    m        *metrics.Metrics
    server   *httptest.Server  // Mock chain RPC
}

var _ = Suite(&MySuite{})

func (s *MySuite) SetUpSuite(c *C) {
    // 1. Initialize Cosmos SDK config
    cosmoscrypto.SetNetworkForTestSuite()

    // 2. Create temp directory for keyring
    s.thordir = c.MkDir()

    // 3. Create keys with test mnemonic
    kb := ckeys.NewInMemory()
    _, err := kb.NewMnemonic("bob", ckeys.English, cmd.THORChainHDPath, "password", hd.Secp256k1)
    c.Assert(err, IsNil)
    s.thorKeys = thorclient.NewKeysWithKeybase(kb, "bob", "password")

    // 4. Set up metrics
    s.m = GetMetricForTest(c)

    // 5. Create mock RPC server
    s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Handle RPC requests
    }))

    // 6. Create THORChain bridge
    s.bridge, _ = thorclient.NewThorchainBridge(...)
}

func (s *MySuite) TearDownSuite(c *C) {
    s.server.Close()
}
```

## Mock RPC Server Patterns

### HTTP Mock Server (most chains)

```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    switch {
    case r.RequestURI == "/block":
        w.Write(loadFixture("block.json"))
    case r.RequestURI == "/tx":
        w.Write(loadFixture("tx.json"))
    default:
        // Handle JSON-RPC by parsing method from body
        var req struct{ Method string }
        json.NewDecoder(r.Body).Decode(&req)
        switch req.Method {
        case "eth_blockNumber":
            fmt.Fprint(w, `{"result":"0x1"}`)
        case "eth_getBalance":
            fmt.Fprint(w, `{"result":"0xde0b6b3a7640000"}`)
        }
    }
}))
```

### Shared Mock Utilities

Located in `bifrost/pkg/chainclients/shared/mock/`:

```go
// Mock chain RPC — loads responses from test/fixtures/
server := mock.NewChainRpc(common.BTCChain)

// Mock THORNode API — loads from test/fixtures/endpoints/
thorApi := mock.NewThornodeApi()
```

### Embedded Fixture Mocks (Tron pattern)

```go
//go:embed test-data/*
var testData embed.FS

func NewMockServer() *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        data, _ := testData.ReadFile("test-data/" + r.URL.Path)
        w.Write(data)
    }))
}
```

### gRPC Mock Services (Cosmos pattern)

```go
type MockBankServiceClient struct{}

func (m *MockBankServiceClient) Balance(ctx context.Context, in *btypes.QueryBalanceRequest, opts ...grpc.CallOption) (*btypes.QueryBalanceResponse, error) {
    return &btypes.QueryBalanceResponse{
        Balance: &ctypes.Coin{Denom: "uatom", Amount: sdkmath.NewInt(1000000)},
    }, nil
}
```

## Fixture Files

### Directory Structure

```text
test/fixtures/
├── btc/                    # BTC RPC responses
│   ├── getnetworkinfo.json
│   ├── blockhash.json
│   ├── block_verbose.json
│   ├── listunspent.json
│   └── tx-*.json
├── eth/                    # ETH RPC responses
├── endpoints/              # THORNode API responses
│   ├── vaults/
│   │   ├── pubKeys.json
│   │   └── asgard.json
│   ├── inbound_addresses/
│   ├── lastblock/
│   └── nodeaccount/
└── ...
```

### Loading Fixtures

```go
func loadFixture(name string) []byte {
    data, err := os.ReadFile(filepath.Join("../../../../test/fixtures/btc", name))
    if err != nil {
        panic(err)
    }
    return data
}
```

## Build Tags

Network-specific tests use build tags:

```go
//go:build !stagenet && !chainnet && !mocknet
// +build !stagenet,!chainnet,!mocknet

// This test runs only on mainnet
func (s *MySuite) TestMainnetAddress(c *C) { ... }
```

Common tag files:

- `<chain>_mainnet_test.go` — `//go:build !stagenet && !chainnet && !mocknet`
- `<chain>_testnet_test.go` — `//go:build testnet`
- Standard tests (no tag) — run with `go test -tags=mocknet`

## Shared Test Helpers

### Metrics Singleton

```go
var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
    if m == nil {
        var err error
        m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
            Enabled:      false,
            ListenPort:   9000,
            ReadTimeout:  time.Second,
            WriteTimeout: time.Second,
            Chains:       common.Chains{common.BTCChain, common.ETHChain, ...},
        })
        c.Assert(err, IsNil)
    }
    return m
}
```

## Required Test Scenarios

### Constructor Tests

- Valid construction with all required args
- Nil thorKeys, nil bridge, nil server → errors

### SignTx Tests

- Valid outbound transaction → signed bytes returned
- Wrong chain → error
- Empty destination address → error
- Empty vault pubkey → error
- Unparsable or inbound memo → error
- Gas limit validation
- Instant observation: verify returned `*TxInItem` has correct fields

### BroadcastTx Tests

- Successful broadcast → txid returned
- RPC error → error propagated
- Already-in-mempool → treated as success

### GetAccount / GetAddress Tests

- Valid pubkey → correct address/balance
- Invalid pubkey → error

### Block Scanner Tests

- `FetchTxs` with valid block → parsed transactions
- `FetchTxs` with empty block → empty TxIn
- `FetchMemPool` → mempool transactions
- `GetHeight` → current chain height
- `GetNetworkFee` → fee rate and size

### Solvency Tests

- `ShouldReportSolvency` → respects block interval
- `ReportSolvency` → constructs correct solvency message

### Running Tests

```bash
# Run all tests for a chain client
go test -tags=mocknet -parallel=1 ./bifrost/pkg/chainclients/<chain>/...

# Run a specific test
go test -tags=mocknet -parallel=1 -run TestSignTx ./bifrost/pkg/chainclients/<chain>/...

# Run with the project Makefile
make test

# Run with race detector (catches data races)
go test -tags=mocknet -parallel=1 -race ./bifrost/pkg/chainclients/<chain>/...
```

## Simulation Tests

For end-to-end chain client testing (swaps, liquidity, solvency), use simulation tests instead of regression tests. See `references/simulation.md` for the full guide on adding a new chain to the simulation framework.
