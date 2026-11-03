# Simulation Test Guide

Simulation tests run end-to-end scenarios against a local mocknet, testing real chain interactions including swaps, liquidity operations, and solvency verification.

## Required Scenarios for New Chains

Every new chain must have simulation test coverage for:

1. **Swap native token to another token** — e.g., MYCHAIN.MYTOKEN → BTC.BTC
2. **Swap another token to native token** — e.g., BTC.BTC → MYCHAIN.MYTOKEN
3. **Add liquidity** — deposit both RUNE and the chain's native token into a pool
4. **Withdraw liquidity** — remove liquidity from the chain's pool

These are automatically covered when you register your chain in `allSimChains` — the bootstrap and swap suites include all registered chains.

## Step-by-Step: Adding a New Chain

### 1. Implement the LiteChainClient Interface

**File**: `test/simulation/pkg/types/lite_chain_client.go`

```go
type LiteChainClient interface {
    // GetAccount returns the account for the given public key.
    // If the key is nil, returns the account for the client's configured key.
    GetAccount(_ *common.PubKey) (*common.Account, error)

    // SignTx returns the signed transaction.
    SignTx(tx SimTx) ([]byte, error)

    // BroadcastTx broadcasts the transaction and returns the hash.
    BroadcastTx([]byte) (string, error)
}

type LiteChainClientConstructor func(_ common.Chain, _ *thorclient.Keys) (LiteChainClient, error)
```

Create your client at `test/simulation/pkg/<chain>/<chain>.go`:

```go
package mychain

import (
    "gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
    "gitlab.com/thorchain/thornode/v3/common"
    . "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type Client struct {
    chain   common.Chain
    rpc     *myChainRPC
    keys    *thorclient.Keys
    address common.Address
}

func NewConstructor(host string) LiteChainClientConstructor {
    return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
        // 1. Extract private key from keys
        // 2. Create RPC client connected to host
        // 3. Derive chain address from key
        return &Client{chain: chain, rpc: rpc, keys: keys, address: addr}, nil
    }
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
    addr := c.address
    if pk != nil {
        addr, _ = pk.GetAddress(c.chain)
    }
    balance, err := c.rpc.GetBalance(addr.String())
    if err != nil {
        return nil, err
    }
    return &common.Account{
        Coins: common.Coins{common.NewCoin(c.chain.GetGasAsset(), balance)},
    }, nil
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
    // Construct and sign a transaction for your chain
    // tx.Chain, tx.ToAddress, tx.Coin, tx.Memo
    return signedBytes, nil
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
    // Broadcast and return transaction ID
    return txHash, nil
}
```

### 2. Register the Constructor

In `test/simulation/cmd/main.go`, add to the `liteClientConstructors` map:

```go
import "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/mychain"

var liteClientConstructors = map[common.Chain]LiteChainClientConstructor{
    // ... existing chains
    common.MYCHAINChain: mychain.NewConstructor(chainRPCs[common.MYCHAINChain]),
}
```

### 3. Add RPC Endpoint

In `test/simulation/cmd/mocknet.go`, add to the `chainRPCs` map:

```go
var chainRPCs = map[common.Chain]string{
    // ... existing chains
    common.MYCHAINChain: "http://localhost:12345",
}
```

### 4. Add Seed Funding

In `test/simulation/cmd/mocknet.go` `InitConfig()`, add a case for your chain's seed amount:

```go
switch chain {
case common.MYCHAINChain:
    chainSeedAmount = sdkmath.NewUint(100 * common.One)  // adjust for chain's gas costs
}
```

### 5. Add Default Network Fee

In `test/simulation/cmd/mocknet.go`, add to the default fees list:

```go
defaultFees := []struct {
    chain common.Chain
    size  uint64
    rate  uint64
}{
    // ... existing chains
    {common.MYCHAINChain, 1, 10_000},  // size and rate appropriate for chain
}
```

### 6. Add Chain to allSimChains

In `test/simulation/actors/common/config.go`, add your chain to the `allSimChains` array
(`SimChains` is derived from it via `simChainsWithXMR`, and is reassigned by
`SetXMRChainEnabled` — do not edit `SimChains` directly):

```go
var allSimChains = []common.Chain{
    // ... existing chains
    common.MYCHAINChain,
}
```

Once added to `allSimChains`, the chain is automatically included in:

- **Bootstrap** (`test/simulation/actors/suites/bootstrap.go`): creates dual LP pools
- **Swaps** (`test/simulation/actors/suites/swaps.go`): tests all pair combinations
- **Memoless swaps**: tests outbound paths without memos
- **Solvency checks**: verifies vault balances

### 6b. Add Explicit Swap Entries in Swaps Suite

The random swap pair selection in `Swaps()` does not guarantee your chain is tested on every run. Add explicit swap entries in `test/simulation/actors/suites/swaps.go` to ensure deterministic coverage, matching the pattern used for SOL, GAIA, and TRON:

```go
// always swap MYCHAIN <> ETH to ensure coverage
if pool.Chain == common.MYCHAINChain {
    a.Children[core.NewSwapActor(pool, common.ETHAsset, rng)] = true
    a.Children[core.NewSwapActor(common.ETHAsset, pool, rng)] = true
}
```

Add this inside the `for i, pool := range swapPools` loop alongside the existing SOL, GAIA, and TRON blocks.

### 7. Run Simulation Tests

```bash
# Reset mocknet and run
make reset-mocknet
TEST_SIMULATION_STAGES=bootstrap,swaps make test-simulation

# Run all stages
TEST_SIMULATION_STAGES=all make test-simulation

# Run with specific parallelism
PARALLELISM=4 TEST_SIMULATION_STAGES=bootstrap,swaps make test-simulation
```

## Transaction Types

```go
// Simple transfer (native/gas asset)
type SimTx struct {
    Chain     common.Chain
    ToAddress common.Address
    Coin      common.Coin
    Memo      string
}

// Contract call (EVM tokens only)
type SimContractTx struct {
    Chain    common.Chain
    Contract common.Address
    ABI      abi.ABI
    Method   string
    Args     []interface{}
}
```

## Actor/Op Pattern

If your chain needs custom test scenarios beyond the standard swap/LP coverage:

```go
type MyChainTestActor struct {
    types.Actor
}

func NewMyChainTestActor(rng *rand.Rand) *types.Actor {
    a := &MyChainTestActor{
        Actor: *types.NewActor("MyChain Custom Test", rng),
    }
    a.Ops = append(a.Ops, a.step1, a.step2, a.step3)
    return &a.Actor
}

func (a *MyChainTestActor) step1(config *types.OpConfig) types.OpResult {
    // Return OpResult{Continue: true} to move to next step
    // Return OpResult{Continue: false} to retry (with Interval delay)
    // Return OpResult{Finish: true, Error: err} to stop with error
}
```

## Existing Simulation Clients for Reference

| Chain Type | Package                       | Key Pattern                                 |
| ---------- | ----------------------------- | ------------------------------------------- |
| UTXO       | `test/simulation/pkg/utxo/`   | UTXO selection, change outputs, secp256k1   |
| EVM        | `test/simulation/pkg/evm/`    | ethclient, ERC20 approvals, router contract |
| Cosmos     | `test/simulation/pkg/cosmos/` | gRPC, MsgSend, native denoms                |
| Solana     | `test/simulation/pkg/solana/` | Ed25519, base64, lamport scaling            |
| XRP        | `test/simulation/pkg/xrp/`    | Binary codec, account sequence              |
| Tron       | `test/simulation/pkg/tron/`   | TRON API, TRX scaling                       |
