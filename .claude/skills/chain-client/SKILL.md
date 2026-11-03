---
name: chain-client
description: Build, modify, or verify bifrost chain clients for THORChain. Use this skill whenever the user mentions adding a new blockchain, modifying an existing chain client, reviewing chain client code, writing chain client tests, implementing the ChainClient interface, working in bifrost/pkg/chainclients/, debugging stuck outbounds, fixing double spends, investigating solvency issues, setting up mocknet for a chain, or working with block scanning, transaction signing/broadcasting, solvency reporting, instant observations, or EVM token handling. Also trigger when the user asks about chain integration patterns, bifrost architecture, or historical chain client bugs.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write
---

# Bifrost Chain Client Skill

This skill guides you through building, modifying, or verifying chain clients in the THORChain bifrost. Chain clients are the bridge between external blockchains and THORChain — they observe inbound transactions, sign outbound transactions, and report solvency.

## Related Skills

| Skill             | When to Use                                                                                                                                  |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `tc-debug`        | A live tx, address, or vault is stuck; its `references/tx-pipeline.md` maps observer -> attestation quorum -> thornode -> signer by function |
| `mocknet-debug`   | For testing chain clients locally                                                                                                            |
| `simulation-test` | For end-to-end chain client testing                                                                                                          |

## Decision Tree

Determine the workflow based on the user's intent:

1. **Building a new chain client** — the user wants to add support for a new blockchain
   - Follow the [New Chain Client Workflow](#new-chain-client-workflow)
2. **Modifying an existing chain client** — the user wants to extend or fix an existing chain client
   - Follow the [Modify Existing Workflow](#modify-existing-workflow)
3. **Verifying a chain client** — the user wants a code review or test coverage audit
   - Follow the [Verification Checklist](#verification-checklist)

---

## New Chain Client Workflow

### Phase 1: Design

#### Step 1: Identify the Chain Family

Read the appropriate reference file based on the chain's architecture:

| Chain Type     | Examples                  | Reference                                     |
| -------------- | ------------------------- | --------------------------------------------- |
| **UTXO**       | BTC, BCH, LTC, DOGE, ZEC  | `references/patterns-utxo.md`                 |
| **EVM**        | ETH, AVAX, BSC, BASE, POL | `references/patterns-evm.md`                  |
| **Cosmos SDK** | GAIA, NOBLE               | `references/patterns-cosmos.md`               |
| **Other**      | SOL, XRP, TRON            | `references/patterns-other.md`                |
| **Monero**     | XMR                       | `bifrost/pkg/chainclients/monero/` (see note) |

If the chain doesn't fit neatly into a family, read `references/patterns-other.md` and pick the closest existing client as a template.

Monero is the outlier: it uses a single "monovault" with a wallet-RPC sidecar under `monero/sidecar/`, view-key scanning, and its own spent-ref, reorg, and signed-payload stores, so almost nothing in the other families transfers. Read `monero/client.go`, `monero/scanner.go`, and `monero/XMR_TSS_SIGNER_API.md` directly rather than a pattern file; the shared interface, common-package, and handler steps below still apply.

#### Step 2: Review Known Bug Patterns

Read `references/known-bugs.md` **before writing code**. This catalog of historical mainnet bugs (double spends, stuck funds, outages) is organized by category; see its table of contents. Understanding these patterns upfront prevents repeating past mistakes. Key categories: double-spend prevention, signer cache management, nonce safety, reorg handling, gas calculation, race conditions, nil safety, and integer overflow.

### Phase 2: Implement Bifrost Client

#### Step 3: Create the Package

Create a new package under `bifrost/pkg/chainclients/<chain>/`. **File naming conventions vary by chain family** — match the convention of the closest existing client rather than inventing new names.

| Family                        | Client file                        | Scanner file                                                                    | Signer file                       | Example                                               |
| ----------------------------- | ---------------------------------- | ------------------------------------------------------------------------------- | --------------------------------- | ----------------------------------------------------- |
| **UTXO** (shared impl)        | `client.go` + `<coin>.go` per coin | Scanning lives in `client.go` / `client_internal.go` (no separate scanner file) | `signer.go` + `<coin>_signer*.go` | `utxo/bitcoin.go`, `utxo/client.go`, `utxo/signer.go` |
| **Generic EVM** (shared impl) | `client.go`                        | `evm_block_scanner.go` (+ `_mainnet.go` / `_mocknet.go` variants)               | In `client.go`                    | `evm/client.go`, `evm/evm_block_scanner.go`           |
| **Ethereum** (dedicated)      | `ethereum.go`                      | `ethereum_block_scanner.go`                                                     | In `ethereum.go`                  | `ethereum/ethereum.go`                                |
| **Cosmos SDK**                | `cosmos_client.go`                 | `cosmos_block_scanner.go`                                                       | In `cosmos_client.go`             | `gaia/cosmos_client.go`                               |
| **Solana**                    | `sol_client.go`                    | `sol_scanner.go`                                                                | In `sol_client.go`                | `solana/sol_client.go`                                |
| **TRON**                      | `tron_client.go`                   | `tron_block_scanner.go` (+ `_mainnet.go` / `_mocknet.go`)                       | In `tron_client.go`               | `tron/tron_client.go`                                 |
| **XRP**                       | `client.go`                        | `block_scanner.go`                                                              | In `client.go`                    | `xrp/client.go`                                       |
| **Monero**                    | `client.go`                        | `scanner.go`                                                                    | In `client.go` (+ `sidecar/`)     | `monero/client.go`                                    |

Optional sub-packages (per chain):

- `rpc/` — wraps the chain's native API (JSON-RPC, gRPC, REST, WebSocket). Examples: `utxo/rpc/`, `solana/rpc/`, `tron/rpc/`.
- `api/` — alternative HTTP API wrapper (e.g. `tron/api/`).
- `abi/` — generated Go bindings for EVM contracts (e.g. `evm/abi/`, `ethereum/abi/`, `tron/abi/`).
- `keymanager/` — chain-specific key management (e.g. `xrp/keymanager/`).

Always add unit tests alongside each file (`_test.go` siblings). Do **not** create a `<chain>_scanner.go` file unless the chain family already uses that convention — copy the file layout of the closest existing client.

#### Step 4: Implement the Core Interfaces

Read `references/interface.md` for the full interface definitions. You must implement:

1. **`ChainClient`** (17 methods) — defined in `bifrost/pkg/chainclients/shared/types/types.go`
2. **`BlockScannerFetcher`** (4 methods) — defined in `bifrost/blockscanner/blockscanner.go`
3. **`SolvencyCheckProvider`** (3 methods) — defined in `bifrost/pkg/chainclients/shared/runners/solvency.go`

#### Step 5: Constructor Pattern

Every chain client constructor follows this pattern:

```go
func NewClient(
    thorKeys *thorclient.Keys,
    cfg config.BifrostChainConfiguration,
    server *tssp.TssServer,
    bridge thorclient.ThorchainBridge,
    m *metrics.Metrics,
    // Optional depending on chain:
    pubkeyMgr pubkeymanager.PubKeyValidator,
    poolMgr thorclient.PoolManager,
) (*Client, error) {
    // 1. Validate required arguments (thorKeys, bridge, etc.)
    // 2. Create RPC client for the chain
    // 3. Set up TSS key signer
    // 4. Extract node private key from thorKeys
    // 5. Create block scanner storage (LevelDB with in-memory fallback)
    // 6. Initialize block scanner with chain-specific scanner
    // 7. Create signer cache manager
    // 8. Return client
}
```

#### Step 6: Implement Instant Observations

Read `references/instant-observations.md` for details. The `SignTx()` method must return a `*types.TxInItem` as its 3rd return value. This enables instant observation — the signed transaction is observed immediately after signing, before broadcast confirmation.

#### Step 7: Configure Confirmation Strategy

Implement `GetConfirmationCount()` and `ConfirmationCountReady()` based on the chain's finality model:

- **Instant finality** (Solana, Cosmos, XRP, AVAX): return 0
- **Probabilistic finality** (ETH, BSC, Polygon, Base): return the required block confirmations

#### Step 8: Integrate Shared Utilities

Every chain client should use these shared components:

- **Signer Cache Manager** (`shared/signercache/`): prevents double-signing, tracks broadcast/observed transactions
- **Solvency Check Runner** (`shared/runners/solvency.go`): periodic solvency verification, runs even when chain is halted
- **Block Scanner** (`bifrost/blockscanner/`): handles block scanning lifecycle, height tracking, and queue management

#### Step 9: Token Support (if applicable)

If the chain supports tokens (ERC20, TRC20, SPL, etc.), additional work is needed:

- **Token whitelist**: Create token list files in `common/tokenlist/` with embedded JSON (see `common/tokenlist/eth.go` pattern)
- **Update `GetEVMTokenList()`** in `common/tokenlist/token_list.go` (EVM chains)
- **Approval flows**: EVM tokens require router approval before transfer via `depositWithExpiry`
- **Token-specific gas multipliers**: Configure `TokenMaxGasMultiplier` in EVM config
- **Scanner token detection**: Parse token transfer events from transaction receipts/logs
- **Solvency for tokens**: Include token balances in solvency reporting (not just gas asset)

#### Step 10: Add Configuration

Add chain-specific configuration fields if needed in `config/config.go` under `BifrostChainConfiguration`. Existing fields cover most needs (ChainHost, RPCHost, MinConfirmations, etc.). Add chain-specific sub-structs only for unique requirements (like `EVM` and `UTXO` sub-configs).

### Phase 3: Register and Configure

#### Step 11: Register in Bifrost

In `bifrost/pkg/chainclients/loadchains.go`, add your chain to the `loadChain` switch statement:

```go
case common.MYCHAINChain:
    return mychain.NewClient(thorKeys, chain, server, thorchainBridge, m)
```

If your chain is UTXO-based and uses `listunspent`, also register the pubkey callback:

```go
case common.MYCHAINChain:
    pubKeyValidator.RegisterCallback(client.(*mychain.Client).RegisterPublicKey)
```

#### Step 12: Update the Common Package

This is one of the most critical steps — missing a single switch case causes runtime panics or silent failures. Read `references/common-package.md` for the complete checklist (30+ locations across 5 files).

At minimum, you must update:

- **`common/chain.go`** (14+ locations): chain constant, AllChains, signing algo, gas asset, gas units, gas decimals, address prefix, dust threshold, block time, notes
- **`common/asset.go`** (5 locations): asset constant, ShortCode, short codes map
- **`common/address.go`** (5 locations): IsValidXxxAddress() validator, NewAddress, IsChain, GetChain, GetNetwork
- **`common/pubkey.go`** (1 location): GetAddress() — chain-specific key→address derivation
- **`common/tokenlist/`** (EVM only): token list file and data directory

#### Step 13: Update THORNode Handlers

Read `references/thornode-handlers.md` for the full guide. Key updates:

- **`manager_network_current.go`**: add chain to `supportChains` array
- **`handler_common_outbound.go`**: add `maxEVMGasForChain()` case (EVM only)
- **`router_upgrade_controller.go`**: add router addresses (EVM only)
- **`querier_quotes.go`**: add address generation case (UTXO only)
- **`constants/`**: add `MaxGas` constant (EVM only)

#### Step 14: Set Up Docker/Mocknet

Read `references/docker-mocknet.md` for the full guide. You need:

- Docker service definition in `build/docker/docker-compose.yml`
- Initialization script in `build/docker/mocknet/`
- Bifrost environment variables (`BIFROST_CHAINS_XXX_*`)
- Default config in `config/default.yaml`
- EVM contract deployment (EVM chains only)

### Phase 4: Test and Verify

#### Step 15: Write Unit Tests

Read `references/testing.md` for the full testing guide. At minimum, test:

- Constructor with valid/invalid inputs
- `SignTx` with various transaction types and edge cases
- `BroadcastTx` with success and failure scenarios
- `GetAccount` and `GetAddress`
- Block scanner `FetchTxs` and `FetchMemPool`
- Solvency reporting

#### Step 16: Write Simulation Tests

Read `references/simulation.md` for the full guide. New chains must have simulation tests that cover:

- Swap from the chain's native token to another token
- Swap from another token to the chain's native token
- Adding liquidity with the chain's native token
- Withdrawing liquidity from the chain's pool

Adding the chain to `allSimChains` provides random coverage, but you must also add **explicit swap entries** in `test/simulation/actors/suites/swaps.go` to guarantee deterministic coverage on every run (see Step 6b in `references/simulation.md`).

#### Step 17: Verify Against Known Bugs

Read `references/known-bugs.md` and run through the checklist in the [Known Bug Pattern Review](#known-bug-pattern-review) section below.

---

## Modify Existing Workflow

When modifying an existing chain client:

1. **Read the existing implementation first** — understand the current code before changing it
2. **Check shared utilities** — look in `shared/` before writing new code. Common patterns already exist for signing, caching, solvency, and EVM/UTXO operations
3. **Maintain interface compliance** — ensure your changes don't break the `ChainClient` interface contract
4. **Update tests** — add test cases for the new behavior. Don't just modify existing tests — add new ones that specifically cover your change
5. **Check for cross-chain impact** — changes to shared utilities affect all chains. Changes to chain-specific code should be isolated to that chain's package
6. **Run tests** — `go test -tags=mocknet -parallel=1 ./bifrost/pkg/chainclients/<chain>/...`

---

## Verification Checklist

Use this when reviewing a chain client for correctness and completeness.

### Code Review

- [ ] **Interface compliance**: All 17 `ChainClient` methods implemented correctly
- [ ] **BlockScannerFetcher**: All 4 methods implemented (`FetchMemPool`, `FetchTxs`, `GetHeight`, `GetNetworkFee`)
- [ ] **SolvencyCheckProvider**: All 3 methods implemented (`GetHeight`, `ShouldReportSolvency`, `ReportSolvency`)
- [ ] **Instant observations**: `SignTx` returns a `*TxInItem` with correct txid, memo, addresses, coins, gas, vault pubkey
- [ ] **Confirmation strategy**: `GetConfirmationCount` and `ConfirmationCountReady` appropriate for chain finality
- [ ] **Signer cache**: Uses `signercache.CacheManager` for double-sign prevention
- [ ] **Solvency runner**: Starts solvency check runner in `Start()` method
- [ ] **Queue communication**: Properly sends to `globalErrataQueue`, `globalSolvencyQueue`, `globalNetworkFeeQueue`
- [ ] **Concurrency safety**: Mutexes for signing, vault locks, WaitGroups for goroutine lifecycle, atomic values for health/heights
- [ ] **Graceful shutdown**: `Stop()` closes stop channel, waits for WaitGroup, stops block scanner and TSS signer
- [ ] **Error handling**: Errors are wrapped with context, not swallowed silently
- [ ] **Configuration**: All required config fields are used and validated in constructor
- [ ] **Registration**: Chain registered in `loadchains.go` with correct constructor signature
- [ ] **Common package completeness**: All switch cases in `chain.go`, `asset.go`, `address.go`, `pubkey.go` include the chain (see `references/common-package.md`)
- [ ] **THORNode handler updates**: supportChains array, MaxGas constant, router addresses (see `references/thornode-handlers.md`)
- [ ] **Docker/mocknet setup**: Chain daemon service, Bifrost env vars, init script (see `references/docker-mocknet.md`)

### Deep Code Audit

After completing the checklist above, do a focused code audit of the critical paths. The checklist ensures structural completeness but can miss subtle logic bugs. Trace each path for edge cases:

- [ ] **SignTx path**: Trace from input validation through tx construction, signing, and observation return. Check type assertions, error handling on each branch, and that no non-deterministic values (time.Now, rand) are used.
- [ ] **BroadcastTx path**: Verify errors are always returned on failure (never `return "", nil`). Check that signer cache and nonce/sequence are only updated on confirmed success.
- [ ] **FetchTxs path**: Trace from block fetch through transaction parsing. Check for float math, unsigned subtraction without guards, and that balance/amount derivation uses canonical instruction data (not balance deltas on multi-instruction chains).

### Test Coverage

Read `references/testing.md` for details on expected test patterns.

- [ ] **Mock RPC servers**: Uses `httptest.NewServer` to mock chain RPC responses
- [ ] **Fixture files**: Chain-specific fixtures in `test/fixtures/<chain>/`
- [ ] **Build tags**: Network-specific tests use correct build tags (`mocknet`, `stagenet`, etc.)
- [ ] **Signing scenarios**: Tests for valid transactions, invalid chains, empty addresses, empty vaults, bad memos
- [ ] **Observing scenarios**: Tests for `OnObservedTxIn`, `FetchTxs`, block scanning
- [ ] **Broadcasting scenarios**: Tests for successful broadcast, failed broadcast, retry behavior
- [ ] **Solvency tests**: Tests for `ShouldReportSolvency` and `ReportSolvency`
- [ ] **Account tests**: Tests for `GetAccount`, `GetAccountByAddress`, `GetAddress`
- [ ] **Confirmation tests**: Tests for `GetConfirmationCount` and `ConfirmationCountReady`
- [ ] **Instant observation tests**: Tests that `SignTx` returns correct `TxInItem`
- [ ] **Simulation tests**: Swaps in/out and liquidity add/withdraw for the chain

### Known Bug Pattern Review

Read `references/known-bugs.md` and verify the chain client avoids the historical bug patterns:

- [ ] **No re-signing**: Broadcast retries use the same signed bytes, never re-sign
- [ ] **Scanner health gating**: Signing is blocked when block scanner is unhealthy
- [ ] **Signer cache correctness**: Checked before signing, only written on broadcast code 0, cleared on failure, per-chain, keys include InHash
- [ ] **Pending liabilities**: Local in-memory tracking of signed-but-unconfirmed tx amounts, not just RPC pending balance
- [ ] **Nonce safety**: Incremented only after broadcast under mutex, local never overwritten by lower on-chain value, pending gap capped
- [ ] **Reorg handling**: Implemented for non-instant-finality chains with errata + rescan of replacement blocks
- [ ] **Instant observation**: SignTx returns TxInItem, only auto-observed on successful broadcast
- [ ] **Error propagation**: BroadcastTx returns non-nil error on failure, error matching uses prefix/contains, duplicate-tx errors treated as success
- [ ] **Gas accuracy**: L1 fees included for L2 chains, multiply before decimal conversion, per-chain MaxGas constants, never lower gas rate
- [ ] **No float math**: All external float values converted to integers individually before arithmetic
- [ ] **Integer safety**: All narrowing/sign-changing casts validated, unsigned subtraction guarded
- [ ] **Deterministic construction**: No time.Now() or non-deterministic values in signed tx fields
- [ ] **Race condition safety**: Per-vault locks for sign+broadcast, sequence under mutex, no lock-free atomics for sequences, all shared state locked
- [ ] **Nil safety**: All maps initialized in constructors, all RPC responses nil-checked, no panics on timeouts
- [ ] **Address validation**: Roundtrip-verified, network-checked, null/burn addresses blocked, chain IDs not hardcoded
- [ ] **Observation robustness**: Invalid txs skipped not batch-failed, observation cache is write-once, mempool txs marked non-final
- [ ] **Chain reserves**: Minimum balance requirements subtracted from available vault balance
- [ ] **Stuck tx recovery**: Background process to detect and recover unconfirmed transactions
- [ ] **RPC resilience**: Comprehensive retryable error list with exponential backoff
- [ ] **Failed outbound observation**: Failed vault outbounds observed for gas accounting
- [ ] **Restart safety**: Scan start height accounts for blocks during restart, uses consensus heights
