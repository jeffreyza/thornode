# THORChain RouterV6 - Custom Grinder Salt Deployment Results

## 📋 **RouterV6 Custom Vanity Address - PRODUCTION DEPLOYED**

**Deployment Date**: 22 August, 2025  
**Contract Address**: `0xdEcDECdEc7577852D643f355544E7a4ddDB90659`  
**Salt**: `0x000000000000000368ee5a0a6488bd2f00000000000000000000000002566f1c`  
**Pattern**: `0xDECDECDEC` (Custom grinder result)  
**Factory**: Nick's CREATE2 Factory (`0x4e59b44847b379578588920cA78FbF26c0B4956C`)

---

## 🌐 **Multi-Chain Deployment Status**

| Network       | Chain ID | Status                 | Block      | Tx Hash                                                                                                     | Explorer                                                                                 |
| ------------- | -------- | ---------------------- | ---------- | ----------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| **Ethereum**  | 1        | ✅ Deployed & Verified | 23,197,887 | [0x2675d9bc...](https://etherscan.io/tx/0x2675d9bcebd3e8b93772dec2c3e609a156fb015e77821fbd0ad48df7f319b76f) | [View Contract](https://etherscan.io/address/0xdEcDECdEc7577852D643f355544E7a4ddDB90659) |
| **Base**      | 8453     | ✅ Deployed & Verified | 34,545,843 | [0xb40cc9eb...](https://basescan.org/tx/0xb40cc9eb3dc061d3e2c7997e1351d01eb88ca408eef8859d211c84f75421a9ce) | [View Contract](https://basescan.org/address/0xdEcDECdEc7577852D643f355544E7a4ddDB90659) |
| **BSC**       | 56       | ✅ Deployed & Verified | 58,510,470 | [0x73440acb...](https://bscscan.com/tx/0x73440acb2b17cbd1870f9c9ef41a701ffd60cc1ebd6a7452bef95dd55bb5ab2c)  | [View Contract](https://bscscan.com/address/0xdEcDECdEc7577852D643f355544E7a4ddDB90659)  |
| **Avalanche** | 43114    | ✅ Deployed & Verified | 67,475,346 | [0x0f2ee06d...](https://snowtrace.io/tx/0x0f2ee06d8cc22fe150cbc7e0f3b33ac1b1f47814e8e38af6f96a2bfb824e9368) | [View Contract](https://snowtrace.io/address/0xdEcDECdEc7577852D643f355544E7a4ddDB90659) |

**Status**: ✅ **4/4 chains deployed successfully, all verified**

---

## 🔧 **Technical Details**

### **Grinder Configuration**

- **Pattern**: `0xDECDECDEC`
- **Salt**: `0x000000000000000368ee5a0a6488bd2f00000000000000000000000002566f1c`
- **Attempts**: 317,996,829
- **Duration**: 8.6 seconds
- **Rate**: 36,964,399 attempts/second
- **Timestamp**: 2025-08-22T11:05:18.167898+02:00

### **Deployment Configuration**

```javascript
// In deploy-single-chain.js:
const ROUTER_CONTRACT_NAME = "THORChain_Router";
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";
const SALT =
  "0x000000000000000368ee5a0a6488bd2f00000000000000000000000002566f1c"; // Custom grinder salt
```

### **Gas Usage**

| Network       | Gas Estimate | Gas Used  | Efficiency |
| ------------- | ------------ | --------- | ---------- |
| **Ethereum**  | 1,695,964    | 1,667,165 | 98.3%      |
| **Base**      | 1,695,964    | 1,667,165 | 98.3%      |
| **BSC**       | 1,695,964    | 1,667,165 | 98.3%      |
| **Avalanche** | 1,695,964    | 1,667,165 | 98.3%      |
| **Total**     | 6,783,856    | 6,668,660 | 98.3%      |

---

## 🔍 **Verification Status**

### **Block Explorer Verification**

- ✅ **Ethereum**: Verified on Etherscan
- ✅ **Base**: Verified on BaseScan
- ✅ **BSC**: Verified on BscScan
- ✅ **Avalanche**: Verified on SnowTrace

### **Sourcify Verification**

- ✅ **Ethereum**: [View on Sourcify](https://repo.sourcify.dev/contracts/full_match/1/0xdEcDECdEc7577852D643f355544E7a4ddDB90659/)
- ✅ **Base**: [View on Sourcify](https://repo.sourcify.dev/contracts/full_match/8453/0xdEcDECdEc7577852D643f355544E7a4ddDB90659/)
- ✅ **BSC**: [View on Sourcify](https://repo.sourcify.dev/contracts/full_match/56/0xdEcDECdEc7577852D643f355544E7a4ddDB90659/)
- ✅ **Avalanche**: [View on Sourcify](https://repo.sourcify.dev/contracts/full_match/43114/0xdEcDECdEc7577852D643f355544E7a4ddDB90659/)

---

## 🚀 **Deployment Commands Used**

### **Single Chain Deployment**

```bash
npx hardhat run deployment/routerv6/deploy-single-chain.js --network ethereum
npx hardhat run deployment/routerv6/deploy-single-chain.js --network base
npx hardhat run deployment/routerv6/deploy-single-chain.js --network bsc
npx hardhat run deployment/routerv6/deploy-single-chain.js --network avalanche
```

### **Multi-Chain Deployment**

```bash
node deployment/routerv6/deploy-all-chains.js
```

### **Manual Verification**

```bash
npx hardhat verify --network ethereum 0xdEcDECdEc7577852D643f355544E7a4ddDB90659 --contract contracts/THORChain_RouterV6.sol:THORChain_Router
npx hardhat verify --network base 0xdEcDECdEc7577852D643f355544E7a4ddDB90659 --contract contracts/THORChain_RouterV6.sol:THORChain_Router
npx hardhat verify --network bsc 0xdEcDECdEc7577852D643f355544E7a4ddDB90659 --contract contracts/THORChain_RouterV6.sol:THORChain_Router
npx hardhat verify --network avalanche 0xdEcDECdEc7577852D643f355544E7a4ddDB90659 --contract contracts/THORChain_RouterV6.sol:THORChain_Router
```

---

## 📝 **Key Contract Functions**

The deployed RouterV6 contract includes the following key functions:

- `depositWithExpiry(vault, asset, amount, memo, expiration)`
- `transferOut(to, asset, value, memo)`
- `batchTransferOut(aggregatedData)`
- `vaultAllowance(vault, token)`

---

## 🎯 **Deployment Summary**

### **Success Metrics**

- ✅ **Deployment Success**: 4/4 chains (100%)
- ✅ **Verification Success**: 4/4 chains (100%)
- ✅ **Address Consistency**: Identical across all chains
- ✅ **Gas Efficiency**: 98.3% average efficiency
- ✅ **Vanity Pattern**: Successfully achieved `0xDECDECDEC` pattern

### **Key Achievements**

1. **Custom Vanity Address**: Successfully deployed with memorable `0xDECDECDEC` pattern
2. **Multi-Chain Consistency**: Same address across all 4 target networks
3. **Full Verification**: All contracts verified on both block explorers and Sourcify
4. **Production Ready**: All deployments completed successfully with proper verification

---

## 🏆 **Status: DEPLOYMENT COMPLETE**

✅ **RouterV6 Custom Salt**: 4/4 chains deployed and verified  
✅ **Vanity Address**: `0xdEcDECdEc7577852D643f355544E7a4ddDB90659`  
✅ **Production Ready**: All networks operational

**The RouterV6 contract with custom grinder salt is now live and fully verified across all target networks!** 🚀
