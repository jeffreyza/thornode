# THORChain RouterV6.1 Stagenet Deployment

## Deployment Information

**Router Version**: 6.1
**Deployment Type**: Stagenet (Testnet)
**Vanity Pattern**: 0x0DC610
**Contract Address**: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`

## Grinder Results

```json
{
  "pattern": "0x0DC610",
  "address": "0x0DC6108C9225Ce93Da589B4CE83c104b34693117",
  "salt": "0x00000000000000015aab67962d1882520000000000000000000000000016079c",
  "factory": "0x4e59b44847b379578588920ca78fbf26c0b4956c",
  "bytecode_hash": "0x3d060233ff5b244d2e00ac91ae3d9f377cb2c9934baec63a515ebe53cb3a6478",
  "attempts": 13059997,
  "duration": 0.4454675,
  "timestamp": "2025-08-30T12:01:08.333642+02:00",
  "rate": 29317508,
  "mode": "CPU",
  "gpu_info": null
}
```

## Deployment Details

### Factory Information

- **Factory Address**: `0x4e59b44847b379578588920ca78fbf26c0b4956c` (Nick's CREATE2 Factory)
- **Salt**: `0x00000000000000015aab67962d1882520000000000000000000000000016079c`
- **Bytecode Hash**: `0x3d060233ff5b244d2e00ac91ae3d9f377cb2c9934baec63a515ebe53cb3a6478`

### Performance Statistics

- **Grinding Time**: 0.45 seconds
- **Attempts**: 13,059,997
- **Rate**: ~29.3M attempts/second
- **Luck Factor**: 1.28x lucky

## Target Networks

The contract will be deployed to the same deterministic address on all mainnet chains (stagenet pattern):

- **Ethereum Mainnet**: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
- **Base Mainnet**: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
- **BSC Mainnet**: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`
- **Avalanche C-Chain**: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117`

## Deployment Results

**Deployment Date**: August 30, 2025

### Ethereum Mainnet

- **Status**: Deployed and Verified
- **Block Number**: 23,253,288
- **Transaction Hash**: `0xf67ef4c2acd3b0db196bff28a1205d9d7711a7398779f9eb32fb67a639787217`
- **Gas Used**: 1,677,358
- **Explorer**: https://etherscan.io/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/1/0x0DC6108C9225Ce93Da589B4CE83c104b34693117/

### Base Mainnet

- **Status**: Deployed and Verified
- **Block Number**: 34,879,771
- **Transaction Hash**: `0x304fa2ab46b57d2db2d808e2781bae2cb3b3ce08dfc7992de69bcadd76b79ebf`
- **Gas Used**: 1,677,358
- **Explorer**: https://basescan.org/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/8453/0x0DC6108C9225Ce93Da589B4CE83c104b34693117/

### BSC Mainnet

- **Status**: Deployed and Verified
- **Block Number**: 59,400,731
- **Transaction Hash**: `0x164b3aa86ea76789763d5aa3b3eeb7139fe022602bd447b77b6cbc3aa6e8ae3b`
- **Gas Used**: 1,677,358
- **Explorer**: https://bscscan.com/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/56/0x0DC6108C9225Ce93Da589B4CE83c104b34693117/

### Avalanche C-Chain

- **Status**: Deployed and Verified
- **Block Number**: 67,877,522
- **Transaction Hash**: `0x86fa7d073e08b232583b6387ed3cb5941083135e3ead77989a27521292bc8e21`
- **Gas Used**: 1,677,358
- **Explorer**: https://snowtrace.io/address/0x0DC6108C9225Ce93Da589B4CE83c104b34693117
- **Sourcify**: https://repo.sourcify.dev/contracts/full_match/43114/0x0DC6108C9225Ce93Da589B4CE83c104b34693117/

## Deployment Command

```bash
# Deploy to all mainnet chains with stagenet pattern
cd chain/evm
node deployment/routerv6/deploy-all-chains.js --salt 0x00000000000000015aab67962d1882520000000000000000000000000016079c --address 0x0DC6108C9225Ce93Da589B4CE83c104b34693117 --pattern 0x0DC610

# Or deploy to individual chains
npx hardhat run deployment/routerv6/deploy-single-chain.js --network ethereum
npx hardhat run deployment/routerv6/deploy-single-chain.js --network base
npx hardhat run deployment/routerv6/deploy-single-chain.js --network bsc
npx hardhat run deployment/routerv6/deploy-single-chain.js --network avalanche
```

## Verification

After deployment, verify the contract on each network:

```bash
npx hardhat verify --network ethereum 0x0DC6108C9225Ce93Da589B4CE83c104b34693117
npx hardhat verify --network base 0x0DC6108C9225Ce93Da589B4CE83c104b34693117
npx hardhat verify --network bsc 0x0DC6108C9225Ce93Da589B4CE83c104b34693117
npx hardhat verify --network avalanche 0x0DC6108C9225Ce93Da589B4CE83c104b34693117
```

## Contract Features

RouterV6.1 includes the following improvements:

- Enhanced batch transfer functionality
- Improved gas optimization
- Better error handling for failed transfers
- Support for ERC20 tokens with transfer fees
- Backward compatibility with RouterV6

## Testing Notes

- This is a mainnet deployment using stagenet vanity pattern for testing purposes
- Contract functionality should be thoroughly tested before full production use
- Test transactions should use small amounts initially
- Monitor gas usage and transaction success rates

## Security Notes

- Contract uses CREATE2 for deterministic deployment
- Same address across all supported mainnet chains
- No constructor arguments required
- Contract is immutable after deployment

## Functional Testing

**Test Date**: August 30, 2025

### ETH Transfer Test Results

A comprehensive test was performed to verify the RouterV6.1 contract functionality:

#### Test Configuration

- **Router Contract**: `0x0DC6108C9225Ce93Da589B4CE83c104b34693117` (Ethereum Mainnet)
- **Target Address**: `0xF1fC3B8C5316DEA698Fce1A1835F2Af3b354594F` (Gnosis Safe)
- **Transfer Amount**: `0.0001 ETH`
- **Function**: `transferOut()`
- **Memo**: "Test transfer from RouterV6.1 Stagenet"

#### Test Results

- **Status**: ✅ **PASSED** - Transfer completed successfully
- **Transaction Hash**: `0x4515e11b929e1694ac9ae4e61e22e22cb6296160adb6df0970f6bab6637ec471`
- **Block Number**: `23,253,314`
- **Transaction Link**: https://etherscan.io/tx/0x4515e11b929e1694ac9ae4e61e22e22cb6296160adb6df0970f6bab6637ec471

#### Gas Usage Analysis

- **Gas Used**: `43,409` gas
- **Gas Price**: `29.24 Gwei` (`292,422,626 wei`)
- **Gas Cost**: `0.000012694 ETH`
- **Total Transaction Cost**: `0.000112694 ETH` (gas + 0.0001 ETH transfer)
- **Efficiency Rating**: Excellent (very low gas usage for RouterV6.1)

#### Balance Verification

- **Deployer Balance Before**: `1.005275350547429125 ETH`
- **Deployer Balance After**: `1.005162656773657091 ETH`
- **Deployer Change**: `-0.000112694 ETH` (correct: gas cost + transfer amount)
- **Gnosis Safe Before**: `93.3500642660472 ETH`
- **Gnosis Safe After**: `93.3501642660472 ETH`
- **Gnosis Safe Change**: `+0.0001 ETH` (exact transfer amount)

#### Test Conclusions

- ✅ **Contract Functionality**: RouterV6.1 transferOut() works perfectly
- ✅ **ETH Transfer Accuracy**: Exact amount transferred to Gnosis Safe
- ✅ **Gas Efficiency**: Very efficient gas usage (43,409 gas)
- ✅ **Balance Calculations**: All balances updated correctly
- ✅ **Gnosis Safe Compatibility**: Successfully transfers to Gnosis Safe
- ✅ **Transaction Success**: Fully confirmed and executed

### Test Script

The test was performed using the script `test-stagenet-router-transfer.js` which:

- Connects to the deployed RouterV6.1 contract
- Calls `transferOut()` with ETH transfer parameters
- Verifies balance changes before and after
- Logs detailed gas usage and transaction information

## Deployment Checklist

- [x] Verify grinder results are correct
- [x] Confirm factory address exists on target networks
- [x] Check deployer account has sufficient funds
- [x] Run deployment script
- [x] Verify contract deployment on each network
- [x] Run contract verification
- [x] Test basic functionality
- [x] Update documentation with actual deployment transactions
