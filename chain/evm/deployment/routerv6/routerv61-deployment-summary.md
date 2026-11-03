# RouterV6.1 Deployment Summary

## Mainnet Contract Address: `0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4`

**Vanity Pattern**: `0x00DC6100`
**Deployment Date**: August 30, 2025
**Full Deployment Summary**: [Mainnet-RouterV61-Deployment.md](./chain/evm/deployment/routerv6/Mainnet-RouterV61-Deployment.md)

### Mainnet Deployment Results

- ✅ **Ethereum**: Block 23,253,245, Tx: `0x6b4995ae01de7ef82941aa3d4ea74cce925a86263a47ef8385e7997759e6017a`
- ✅ **Base**: Block 34,879,513, Tx: `0x7791e8d94a58989074af3e455527c9635ec59649fcbec700a0b4c06819c01bf5`
- ✅ **BSC**: Block 59,400,042, Detected existing CREATE2 deployment
- ✅ **Avalanche**: Block 67,877,233, Tx: `0xb59e5f2cc67d875f5a3a63d47fb36f31abd23d4201e79c05a05865b7722dfe95`

### Mainnet Verification Status

- ✅ **Ethereum**: Verified on Etherscan & Sourcify
- ✅ **Base**: Verified on BaseScan & Sourcify
- ✅ **BSC**: Verified on BscScan & Sourcify
- ✅ **Avalanche**: Verified on SnowTrace & Sourcify

---

## Stagenet Contract Address: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`

**Vanity Pattern**: `0x0DC610`
**Deployment Date**: August 30, 2025
**Full Deployment Summary**: [Stagenet-RouterV61-Deployment.md](./chain/evm/deployment/routerv6/Stagenet-RouterV61-Deployment.md)

### Stagenet Deployment Results

- ✅ **Ethereum**: Block 23,253,288, Tx: `0xf67ef4c2acd3b0db196bff28a1205d9d7711a7398779f9eb32fb67a639787217`
- ✅ **Base**: Block 34,879,771, Tx: `0x304fa2ab46b57d2db2d808e2781bae2cb3b3ce08dfc7992de69bcadd76b79ebf`
- ✅ **BSC**: Block 59,400,731, Tx: `0x164b3aa86ea76789763d5aa3b3eeb7139fe022602bd447b77b6cbc3aa6e8ae3b`
- ✅ **Avalanche**: Block 67,877,522, Tx: `0x86fa7d073e08b232583b6387ed3cb5941083135e3ead77989a27521292bc8e21`

### Stagenet Verification Status

- ✅ **Ethereum**: Verified on Etherscan & Sourcify
- ✅ **Base**: Verified on BaseScan & Sourcify
- ✅ **BSC**: Verified on BscScan & Sourcify
- ✅ **Avalanche**: Verified on SnowTrace & Sourcify

---

## Functional Testing Results

### ETH Transfer Test (Stagenet Router)

- **Test Date**: August 30, 2025
- **Transaction**: `0x4515e11b929e1694ac9ae4e61e22e22cb6296160adb6df0970f6bab6637ec471`
- **Transfer Amount**: `0.0001 ETH`
- **Target**: Gnosis Safe (`0xF1fC3B8C5316DEA698Fce1A1835F2Af3b354594F`)
- **Gas Used**: `43,409` gas
- **Status**: ✅ **PASSED** - Perfect transfer execution

### Performance Metrics

- **Grinding Time (Mainnet)**: 51.23 seconds
- **Grinding Time (Stagenet)**: 0.45 seconds
- **Rate**: ~32.2M attempts/second
- **Vanity Success**: 2.60x lucky (mainnet), 1.28x lucky (stagenet)

---

## Deployment Details

**Deployed using CREATE2** so same router address on all chains:

- **Ethereum Mainnet**
- **Base Mainnet**
- **BSC Mainnet**
- **Avalanche C-Chain**

**Factory**: Nick's CREATE2 Factory (`0x4e59b44847b379578588920ca78fbf26c0b4956c`)

---

## RouterV6.1 Features

### 1) Makes the Router Stateless (router forwards funds to Vault)

- Enables faster router updates in future
- Enables memoless ERC20 deposits
- Better separation of concerns

### 2) Supports Batching

- `batchTransferOut()` function for multiple transfers in one transaction
- Gas optimization for bulk operations

### 3) Supports `transferOutAndCallV2()` as requested by THORSwap team

- ERC20 aggregation support
- Extra parameters for advanced integrations
- Enhanced DEX integration capabilities

### 4) Remove unused functions

- Cleaner contract interface
- Reduced attack surface
- Better maintainability

### 5) Make expiration optional

- Better DevUX (Developer Experience)
- TC vaults now process refunds
- More flexible transaction handling

---

## Technical Specifications

- **Contract**: `THORChain_RouterV6.sol`
- **Version**: RouterV6.1
- **Solidity**: 0.8.30
- **Deployment Method**: CREATE2 with vanity addresses
- **Security**: Fully audited and verified
- **Gas Efficiency**: Excellent performance

---

## Issue Reference

### Closes #2204

---

## Quick Links

- [Mainnet Deployment Doc](./chain/evm/deployment/routerv6/Mainnet-RouterV61-Deployment.md)
- [Stagenet Deployment Doc](./chain/evm/deployment/routerv6/Stagenet-RouterV61-Deployment.md)
- [Test Script](./chain/evm/test-stagenet-router-transfer.js)
- [Deployment Scripts](./chain/evm/deployment/routerv6/)
