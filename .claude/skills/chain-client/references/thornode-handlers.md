# THORNode Handler Changes for New Chains

Adding a new chain isn't just about the bifrost client — THORNode's core logic also needs updates. These changes are in `x/thorchain/` and affect outbound handling, gas management, and vault operations.

## Required Updates

### 1. Support Chains Array

**File**: `x/thorchain/manager_network_current.go` (lines ~65-82)

Add the chain to the `supportChains` array in the genesis/vault initialization:

```go
supportChains := common.Chains{
    common.THORChain, common.BTCChain, common.LTCChain, common.BCHChain,
    common.ETHChain, common.DOGEChain, common.AVAXChain, common.GAIAChain,
    common.NOBLEChain, common.BSCChain, common.BASEChain, common.TRONChain,
    common.XRPChain, common.SOLChain, common.POLChain, common.ZECChain,
    common.NEWCHAINChain,  // <-- add here
}
```

### 2. EVM Max Gas Constants (EVM chains only)

**File**: `x/thorchain/handler_common_outbound.go` (lines ~342-356)

Add a case to `maxEVMGasForChain()`:

```go
case common.NEWCHAINChain:
    return cosmos.NewUint(constants.MaxNEWCHAINGas), true
```

Also define the constant in `constants/`:

```go
MaxNEWCHAINGas = 80000
```

### 3. Router Contract Registration (EVM chains only)

**File**: `x/thorchain/router_upgrade_controller.go` (lines ~33-50)

Add old/new router addresses in `getChainOldAndNewRouters()`:

```go
case common.NEWCHAINChain:
    return newchainOldRouter, newchainNewRouter, nil
```

Also add to `getRouterChains()` to include the chain in router upgrade processing.

### 4. Quote Address Generation (UTXO chains only)

**File**: `x/thorchain/querier_quotes.go` (lines ~1193-1240)

Add a case for UTXO address generation from public keys:

```go
case common.NEWCHAINChain:
    // Chain-specific address derivation for quote endpoint
```

### 5. Mimir Keys (automatic)

Mimir keys are generated from format strings, so most work automatically:

- `HaltNEWCHAINChain` — halt the chain
- `HaltNEWCHAINTrading` — halt trading for the chain
- `PauseLPNEWCHAIN` — pause LP operations
- `SolvencyHaltNEWCHAINChain` — solvency-based halt
- `MaxGas-NEWCHAIN` — override max gas
- `ConfMultiplierBasisPoints-NEWCHAIN` — confirmation multiplier
- `MaxConfirmations-NEWCHAIN` — max confirmations

These are defined as templates in `constants/mimir_strings.go` and `x/thorchain/keeper/v1/keeper_halt.go`.

### 6. Halt/Pause Logic (automatic)

**File**: `x/thorchain/keeper/v1/keeper_halt.go`

The halt checking functions use format strings with the chain name:

- `IsChainHalted()` (lines ~83-109) — checks `HaltChainGlobal`, `Halt{Chain}Chain`, admin halt, and solvency halt
- `IsChainTradingHalted()` (lines ~72-81) — checks chain-specific trading halt
- `IsLPPaused()` (lines ~114-127) — checks LP pause per chain

These work automatically for any chain in `AllChains` — no code changes needed.

## Files Summary

| File                           | What to Update      | Chain Types |
| ------------------------------ | ------------------- | ----------- |
| `manager_network_current.go`   | supportChains array | All         |
| `handler_common_outbound.go`   | maxEVMGasForChain() | EVM only    |
| `router_upgrade_controller.go` | Router addresses    | EVM only    |
| `querier_quotes.go`            | Address generation  | UTXO only   |
| `constants/`                   | MaxGas constant     | EVM only    |
