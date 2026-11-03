# EVM Chain Contracts

Smart contracts for all EVM-based chains (Ethereum, BSC, Avalanche, Base) used by THORChain.

## Contracts Overview

### THORChain Router (V6)

- **Primary contract** for handling deposits and transfers
- Supports ETH and ERC-20 tokens across all EVM chains
- Handles vault allowances and batch operations
- Includes aggregator integration for swaps

### THORChain Aggregators

- **Uniswap Aggregator**: Integrates with Uniswap V2/V3 for token swaps
- **Generic Aggregator**: Supports other DEX protocols
- Enables seamless token swapping before/after THORChain operations

## THORChain Integration

### Bifrost - Observer

**ETH & ERC-20 Deposits**
Users call the router contract with appropriate memo:

- ETH: `await ROUTER.depositWithExpiry(vault, 0x0, amount, memo, expiry)` (payable)
- ERC20: `await ROUTER.depositWithExpiry(vault, asset, amount, memo, expiry)`

Bifrost parses contract events to read the `memo` for asset transfers.

_Note: ETH is represented as `0x0000000000000000000000000000000000000000`_

### Bifrost - Signer

Vault operations use the router contract:

- ETH: `await ROUTER.transferOut(to, 0x0, value, memo)` (payable)
- ERC20: `await ROUTER.transferOut(to, asset, value, memo)`
- Batch: `await ROUTER.batchTransferOut(recipients[], assets[], amounts[], memos[])`

### Vault Management

**Allowance Transfers** (for vault migrations):
`await ROUTER.transferAllowance(oldVault, newVault, asset, amount, memo)`

**Batch Vault Returns**:
`await ROUTER.returnVaultAssets(router, asgard, coins[], memo)`

## Contract Design

### Key Features

- **Reentrancy Protection**: Uses transient storage for gas-efficient reentrancy guards
- **Batch Operations**: Supports batch transfers for gas optimization
- **Aggregator Integration**: Built-in DEX swap capabilities
- **Vault Allowances**: Tracks spending allowances for different vaults
- **Expiry Support**: Time-based expiration for deposits

### Public Getters

Tracks vault allowances for each asset:

```solidity
function vaultAllowance(address vault, address token) external view returns (uint256);
```

### Key Events

```solidity
event Deposit(address indexed to, address indexed asset, uint256 amount, string memo);
event TransferOut(address indexed vault, address indexed to, address asset, uint256 amount, string memo);
event TransferAllowance(address indexed oldVault, address indexed newVault, address asset, uint256 amount, string memo);
event TransferOutAndCall(address indexed vault, address target, uint256 amount, address finalAsset, address to, uint256 amountOutMin, string memo);
```

## Development

### Testing

```bash
yarn install
yarn hardhat clean
yarn hardhat compile
yarn hardhat test
```

### Deployment

Contract artifacts are generated in `/artifacts` directory after compilation.

## Supported Chains

- **Ethereum** (ETH)
- **Binance Smart Chain** (BSC)
- **Avalanche C-Chain** (AVAX)
- **Base** (BASE)

All chains use the same contract code with chain-specific deployments.
