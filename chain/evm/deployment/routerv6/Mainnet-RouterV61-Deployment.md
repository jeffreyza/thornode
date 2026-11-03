# THORChain RouterV6.1 Mainnet Deployment

## Deployment Information

**Router Version**: 6.1
**Deployment Type**: Mainnet (Production)
**Vanity Pattern**: 0x00DC6100
**Contract Address**: `0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4`

## Grinder Results

```json
{
  "pattern": "0x00DC6100",
  "address": "0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4",
  "salt": "0x00000000000000045b7b09fc668461dc0000000000000000000000000c55c655",
  "factory": "0x4e59b44847b379578588920ca78fbf26c0b4956c",
  "bytecode_hash": "0x3d060233ff5b244d2e00ac91ae3d9f377cb2c9934baec63a515ebe53cb3a6478",
  "attempts": 1649051222,
  "duration": 51.229536542,
  "timestamp": "2025-08-30T12:01:01.523332+02:00",
  "rate": 32189462,
  "mode": "CPU",
  "gpu_info": null
}
```

## Deployment Details

### Factory Information

- **Factory Address**: `0x4e59b44847b379578588920ca78fbf26c0b4956c` (Nick's CREATE2 Factory)
- **Salt**: `0x00000000000000045b7b09fc668461dc0000000000000000000000000c55c655`
- **Bytecode Hash**: `0x3d060233ff5b244d2e00ac91ae3d9f377cb2c9934baec63a515ebe53cb3a6478`

### Performance Statistics

- **Grinding Time**: 51.23 seconds
- **Attempts**: 1,649,051,222
- **Rate**: ~32.2M attempts/second
- **Luck Factor**: 2.60x lucky

## Target Networks

The contract will be deployed to the same deterministic address on all mainnet chains:

- **Ethereum Mainnet**: `0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4`
- **Base Mainnet**: `0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4`
- **BSC Mainnet**: `0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4`
- **Avalanche C-Chain**: `0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4`

## Deployment Results

**Deployment Date**: August 30, 2025

### Ethereum Mainnet

- **Status**: Deployed and Verified
- **Block Number**: 23,253,245
- **Transaction Hash**: `0x6b4995ae01de7ef82941aa3d4ea74cce925a86263a47ef8385e7997759e6017a`
- **Gas Used**: 1,677,370
- **Explorer**: https://etherscan.io/address/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/1/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4/

### Base Mainnet

- **Status**: Deployed and Verified
- **Block Number**: 34,879,513
- **Transaction Hash**: `0x7791e8d94a58989074af3e455527c9635ec59649fcbec700a0b4c06819c01bf5`
- **Gas Used**: 1,677,370
- **Explorer**: https://basescan.org/address/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/8453/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4/

### BSC Mainnet

- **Status**: Deployed and Verified
- **Block Number**: Detected existing CREATE2 deployment
- **Transaction Hash**: N/A (pre-existing deployment)
- **Gas Used**: 1,677,370 (for verification)
- **Explorer**: https://bscscan.com/address/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/56/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4/

### Avalanche C-Chain

- **Status**: Deployed and Verified
- **Block Number**: 67,877,233
- **Transaction Hash**: `0xb59e5f2cc67d875f5a3a63d47fb36f31abd23d4201e79c05a05865b7722dfe95`
- **Gas Used**: 1,677,370
- **Explorer**: https://snowtrace.io/address/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/43114/0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4/

## Deployment Command

```bash
# Deploy to all mainnet chains
cd chain/evm/deployment/routerv6
node deploy-all-chains.js

# Or deploy to individual chains
npx hardhat run deployment/routerv6/deploy-single-chain.js --network ethereum
npx hardhat run deployment/routerv6/deploy-single-chain.js --network base
npx hardhat run deployment/routerv6/deploy-single-chain.js --network bsc
npx hardhat run deployment/routerv6/deploy-single-chain.js --network avalanche
```

## Verification

After deployment, verify the contract on each network:

```bash
npx hardhat verify --network ethereum 0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
npx hardhat verify --network base 0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
npx hardhat verify --network bsc 0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
npx hardhat verify --network avalanche 0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4
```

## Contract Features

RouterV6.1 includes the following improvements:

- Enhanced batch transfer functionality
- Improved gas optimization
- Better error handling for failed transfers
- Support for ERC20 tokens with transfer fees
- Backward compatibility with RouterV6

## Security Notes

- Contract uses CREATE2 for deterministic deployment
- Same address across all supported chains
- No constructor arguments required
- Contract is immutable after deployment

## Deployment Checklist

- [x] Verify grinder results are correct
- [x] Confirm factory address exists on target networks
- [x] Check deployer account has sufficient funds
- [x] Run deployment script
- [x] Verify contract deployment on each network
- [x] Run contract verification
- [ ] Test basic functionality
- [x] Update documentation with actual deployment transactions
