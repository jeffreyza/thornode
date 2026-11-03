# 🚀 THORChain Router Deployment Suite

This directory contains all deployment scripts, tests, and utilities for THORChain Router smart contracts across multiple EVM networks.

## 📁 Directory Structure

```bash
deployment/
├── routerv4/                   # RouterV4 deployment and utilities
│   ├── deploy-create2-nicks-v4.js
│   ├── deploy-create2-nicks-v4-all.js
│   ├── calculate-create2-address-v4.js
│   ├── deployment-summary-final.md
│   └── *.json                  # Network deployment results
├── routerv6/                   # RouterV6 deployment and utilities
│   ├── deploy-single-chain.js
│   ├── deploy-all-chains.js
│   ├── test-mainnet-functionality.js
│   ├── test-results-mainnet.md
│   ├── utils-check-factory.js
│   ├── utils-check-balances.js
│   ├── calculate-create2-address.js
│   └── deployment-summary.md
├── migration/                  # RouterV4 → RouterV6 migration tools
│   ├── router-v4-to-v6-migration-test.js
│   ├── run-migration-test.sh
│   └── README.md
├── shared/                     # Shared configuration and utilities
│   └── env.sample             # Environment template
└── README.md                  # This file
```

## 🎯 Quick Start

### 1. **Environment Setup**

```bash
# Copy environment template
cp deployment/shared/env.sample .env

# Edit with your actual values
nano .env
```

### 2. **RouterV6 Deployment (Latest)**

```bash
# Deploy to all networks
npm run v6:deploy

# Deploy to specific network
npm run v6:deploy:ethereum
npm run v6:deploy:base
npm run v6:deploy:bsc
npm run v6:deploy:avalanche

# Check deployment addresses
npm run v6:address
```

### 3. **RouterV6 Testing**

```bash
# Test mainnet functionality
npm run v6:test

# Check account balances before testing
npm run v6:balances
```

### 4. **Migration Testing**

```bash
# Test RouterV4 → RouterV6 migration
npm run migration:test

# Or use the shell script
./deployment/migration/run-migration-test.sh
```

## 🌐 Network Support

### **RouterV6 (Current)**

| Network   | Chain ID | Router Address                               |
| --------- | -------- | -------------------------------------------- |
| Ethereum  | 1        | `0xd5976E83F160B84BE90510b04C27657F240c7049` |
| Base      | 8453     | `0xd5976E83F160B84BE90510b04C27657F240c7049` |
| BSC       | 56       | `0xd5976E83F160B84BE90510b04C27657F240c7049` |
| Avalanche | 43114    | `0xd5976E83F160B84BE90510b04C27657F240c7049` |

### **RouterV4 (Legacy)**

| Network   | Chain ID | Router Address                              |
| --------- | -------- | ------------------------------------------- |
| Ethereum  | 1        | `0x3624525075b88B24ecc29CE226b0CaE233b0c9D` |
| Base      | 8453     | `0x3624525075b88B24ecc29CE226b0CaE233b0c9D` |
| BSC       | 56       | `0x3624525075b88B24ecc29CE226b0CaE233b0c9D` |
| Avalanche | 43114    | `0x3624525075b88B24ecc29CE226b0CaE233b0c9D` |

## 📚 Available Scripts

### **RouterV4 Commands**

```bash
npm run v4:deploy                # Deploy RouterV4 to all networks
npm run v4:deploy:ethereum       # Deploy RouterV4 to Ethereum
npm run v4:deploy:base           # Deploy RouterV4 to Base
npm run v4:deploy:bsc            # Deploy RouterV4 to BSC
npm run v4:deploy:avalanche      # Deploy RouterV4 to Avalanche
npm run v4:address               # Calculate RouterV4 CREATE2 address
```

### **RouterV6 Commands**

```bash
npm run v6:deploy                # Deploy RouterV6 to all networks
npm run v6:deploy:ethereum       # Deploy RouterV6 to Ethereum
npm run v6:deploy:base           # Deploy RouterV6 to Base
npm run v6:deploy:bsc            # Deploy RouterV6 to BSC
npm run v6:deploy:avalanche      # Deploy RouterV6 to Avalanche
npm run v6:address               # Calculate RouterV6 CREATE2 address
npm run v6:test                  # Test RouterV6 on mainnet
npm run v6:balances              # Check test account balances
```

### **Migration Commands**

```bash
npm run migration:test           # Test RouterV4 → RouterV6 migration
```

### **Utility Commands**

```bash
npm run check-factory            # Check Nick's CREATE2 factory (all networks)
npm run check-factory:ethereum   # Check factory on Ethereum
npm run check-factory:base       # Check factory on Base
npm run check-factory:bsc        # Check factory on BSC
npm run check-factory:avalanche  # Check factory on Avalanche
```

### **Legacy Commands (RouterV6)**

```bash
npm run deploy                   # Alias for v6:deploy
npm run deploy:ethereum          # Alias for v6:deploy:ethereum
npm run test-mainnet             # Alias for v6:test
npm run calculate-address        # Alias for v6:address
```

## 🔧 Technical Implementation

### **CREATE2 Deterministic Deployment**

Both RouterV4 and RouterV6 use CREATE2 for deterministic addresses across all chains:

- **Factory**: Nick's CREATE2 Factory (`0x4e59b44847b379578588920cA78FbF26c0B4956C`)
- **RouterV4 Salt**: `THORCHAINROUTERV4` (ASCII encoded)
- **RouterV6 Salt**: `THORCHAINROUTERV6` (ASCII encoded)

### **Contract Verification**

Automatic verification on all supported block explorers:

- **Etherscan v2 API** for Ethereum, Base, BSC
- **Sourcify** for Avalanche (fallback)

### **Multi-Chain Support**

- **RPC Providers**: Infura (Ethereum), Native endpoints (others)
- **Gas Optimization**: Network-specific gas configurations
- **Error Handling**: Graceful failures with detailed error messages

## 🧪 Testing Strategy

### **RouterV6 Mainnet Testing**

Comprehensive testing suite includes:

- ✅ **ETH Deposits**: Basic and with expiry validation
- ✅ **Token Deposits**: USDT, USDC, WBTC with approval handling
- ✅ **Transfers**: Single and batch operations
- ✅ **Edge Cases**: Zero amounts, insufficient funds, error scenarios
- ✅ **Gas Analysis**: Efficiency metrics and batch optimization

### **Migration Testing**

RouterV4 → RouterV6 compatibility verification:

- ✅ **Function Mapping**: `deposit()` → `depositWithExpiry()`
- ✅ **Gas Comparison**: Performance impact analysis
- ✅ **Batch Operations**: Unchanged functionality
- ✅ **Security Features**: New expiry validation

## 📊 Performance Metrics

### **RouterV6 Efficiency**

- **Batch Transfers**: ~21,283 gas per transfer
- **Single Deposits**: ~47K-55K gas (varies by token)
- **Large Batches**: Linear scaling up to 10+ transfers
- **Error Handling**: Graceful failures, no reverts

### **Migration Impact**

- **Gas Overhead**: ~5-8% increase due to expiry validation
- **Functional Changes**: New `expiration` parameter required
- **Compatibility**: 100% backward compatible for batch transfers

## 🔒 Security Features

### **RouterV6 Enhancements**

- ✅ **Expiry Validation**: Time-based deposit expiration
- ✅ **Array Validation**: Batch operation input checking
- ✅ **Vault Protection**: Router self-reference prevention
- ✅ **Safe Transfers**: ERC20 failure handling
- ✅ **ETH Recovery**: Excess ETH return to sender

### **Deployment Security**

- ✅ **Deterministic Addresses**: Same across all chains
- ✅ **Verified Source Code**: Auto-verification on explorers
- ✅ **Factory Security**: Trusted Nick's CREATE2 factory
- ✅ **Salt Uniqueness**: Prevents address collisions

## 🎯 Production Readiness

### **RouterV6 Status: ✅ PRODUCTION READY**

- **Multi-chain deployed**: Ethereum, Base, BSC, Avalanche
- **Thoroughly tested**: 30+ successful mainnet transactions
- **Gas optimized**: Efficient batch processing
- **Security validated**: Comprehensive edge case testing
- **Documentation complete**: Full deployment and testing docs

### **Integration Guide**

1. **Contract Address**: `0xd5976E83F160B84BE90510b04C27657F240c7049`
2. **Key Functions**:
   - `depositWithExpiry(vault, asset, amount, memo, expiration)`
   - `transferOut(to, asset, amount, memo)`
   - `batchTransferOut(recipients, assets, amounts, memos)`
3. **Migration**: Update `deposit()` calls to `depositWithExpiry()`
4. **Networks**: Same address across all supported chains

## 📞 Support & Troubleshooting

### **Common Issues**

- **Environment**: Check `.env` file configuration
- **Balances**: Ensure sufficient ETH and tokens for testing
- **Network**: Verify RPC endpoints and API keys
- **Gas**: Check gas limits for large batch operations

### **Documentation Links**

- **RouterV4**: `routerv4/deployment-summary-final.md`
- **RouterV6**: `routerv6/deployment-summary.md`
- **Migration**: `migration/README.md`
- **Environment**: `shared/env.sample`

### **Quick Diagnostics**

```bash
# Check contract deployments
npm run check-factory:ethereum

# Verify account balances
npm run v6:balances

# Test basic functionality
npm run v6:test

# Run migration compatibility
npm run migration:test
```

---

## 🏆 THORChain Router Deployment Suite - Production Ready

_Comprehensive multi-chain deployment and testing infrastructure for THORChain Router smart contracts._
