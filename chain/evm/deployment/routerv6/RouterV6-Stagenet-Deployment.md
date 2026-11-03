# THORChain RouterV6 Multi-Chain Deployment Results

## 📋 Deployment Overview

**Date**: August 27, 2025  
**Networks**: Ethereum, BASE, BSC, Avalanche, Sepolia (Stagenet)  
**Contract Name**: THORChain RouterV6  
**Vanity Pattern**: 0xDEC999

## 🚀 Multi-Chain Deployment Results

### **✅ MAINNET DEPLOYMENTS - ALL SUCCESSFUL**

#### **1. Ethereum Mainnet**

- **Contract Address**: `0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0`
- **Transaction Hash**: `0xcfaf8a93270c07b0713c8eb5dbe199658801542e73c936d0a62f466f56046f32`
- **Block Number**: 23,233,164
- **Chain ID**: 1
- **Gas Used**: 1,667,153
- **Verification**: ✅ [Etherscan](https://etherscan.io/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)

#### **2. BASE Mainnet**

- **Contract Address**: `0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0`
- **Transaction Hash**: `0x2e5516e39afac51e33c3e4c82ab294b5194671e3aa269c62e09a55fe49a1d8db`
- **Block Number**: 34,758,321
- **Chain ID**: 8453
- **Gas Used**: 1,667,153
- **Verification**: ✅ [BaseScan](https://basescan.org/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)

#### **3. BSC Mainnet**

- **Contract Address**: `0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0`
- **Transaction Hash**: `0xb778fde8ee037cca4357b03e8c6080215abd704effdede315a478ac14ce34100`
- **Block Number**: 59,076,977
- **Chain ID**: 56
- **Gas Used**: 1,667,153
- **Verification**: ✅ [BscScan](https://bscscan.com/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)

#### **4. Avalanche Mainnet**

- **Contract Address**: `0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0`
- **Transaction Hash**: `0xa0928c91f5284a683c3859910186b5ecc7b883264609e415a80f9df584343b16`
- **Block Number**: 67,729,984
- **Chain ID**: 43114
- **Gas Used**: 1,667,153
- **Verification**: ✅ [SnowTrace](https://snowtrace.io/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)

### **Deployment Configuration**

- **Factory Contract**: Nick's CREATE2 Factory (`0x4e59b44847b379578588920cA78FbF26c0B4956C`)
- **Salt**: `0x0000000000000006bbbaf22f1aa11c6a0000000000000000000000000012be15`
- **Deployer Address**: `0x3efF38C0e1e5DD6Bd58d3fa79cAecc4Da46C8866`
- **Account Balance**: 0.01 ETH

### **Gas Usage**

- **Estimated Gas**: 1,695,952
- **Actual Gas Used**: 1,667,153
- **Gas Savings**: 28,799 (1.7%)

## 🔗 Block Explorer Links

### **Mainnet Block Explorers**

- **Ethereum**: [https://etherscan.io/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code](https://etherscan.io/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)
- **BASE**: [https://basescan.org/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code](https://basescan.org/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)
- **BSC**: [https://bscscan.com/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code](https://bscscan.com/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)
- **Avalanche**: [https://snowtrace.io/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code](https://snowtrace.io/address/0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0#code)

### **Deployer Address**

- **All Networks**: [0x3efF38C0e1e5DD6Bd58d3fa79cAecc4Da46C8866](https://etherscan.io/address/0x3efF38C0e1e5DD6Bd58d3fa79cAecc4Da46C8866)

## Mainnet Functionality Testing Results

**Test Date**: August 27, 2025  
**Network**: Ethereum Mainnet  
**Contract**: 0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0

### Deposit Function Tests

**Test Status**: PASSED (4/4 tests successful)  
**Success Rate**: 100%

| Token | Amount   | Gas Used | Transaction Hash                                                                                            | Status |
| ----- | -------- | -------- | ----------------------------------------------------------------------------------------------------------- | ------ |
| USDT  | 0.1      | 50,536   | [0x21742e8d...](https://etherscan.io/tx/0x21742e8d0fb847df56ce25b294e8d2079a779a9e5945ab6323acf190d8bf3d04) | PASSED |
| USDC  | 0.1      | 53,091   | [0xa2ae091f...](https://etherscan.io/tx/0xa2ae091f68977e92cfd29cdaa9775eb6275d54b3eff148f521f594ec82069791) | PASSED |
| WBTC  | 0.000001 | 41,900   | [0xedf68aee...](https://etherscan.io/tx/0xedf68aee810306898501e3dcb053fe9b7869386d07b4babd7244fababb0b1517) | PASSED |
| ETH   | 0.001    | 33,784   | [0xca50d9d5...](https://etherscan.io/tx/0xca50d9d53046d1ecd7231f2c1c955cf55a9864efeef4fd45000eeda9d14ca2f9) | PASSED |

**Token Addresses Tested**:

- USDT: 0xdAC17F958D2ee523a2206206994597C13D831ec7
- USDC: 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48
- WBTC: 0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599

**Key Validations**:

- ETH deposit amount validation working correctly
- ERC20 token approvals and transfers functioning
- USDT non-standard approval handling successful
- All deposit events emitted properly
- Gas usage within expected ranges

**Contract Status**: Production Ready
