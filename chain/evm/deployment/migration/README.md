# THORChain RouterV4 to RouterV6 Migration Test

This script tests the migration path from RouterV4 to RouterV6 on Ethereum mainnet using **real deployed contracts**.

## 📋 Contract Addresses

- **RouterV4**: [`0x33c630409883269bc281Dd40824562B066a70512`](https://etherscan.io/address/0x33c630409883269bc281Dd40824562B066a70512)
- **RouterV6**: [`0xd5976E83F160B84BE90510b04C27657F240c7049`](https://etherscan.io/address/0xd5976E83F160B84BE90510b04C27657F240c7049)

## 🎯 Migration Test Process

### What This Test Does

1. **Check Existing Allowances** - Inspects current vault allowances in RouterV4
2. **Create Test Deposits** (if needed) - User deposits tokens to RouterV4, crediting the vault
3. **Execute Migration** - Vault uses RouterV4's `transferAllowance` to migrate to RouterV6
4. **Return Tokens** - Migration returns tokens directly to users via RouterV6
5. **Verify Results** - Confirms migration completion and RouterV6 functionality

## 🚀 Quick Start

### Prerequisites

1. **Environment Setup**: Create `.env` file in `chain/evm/scripts/`:

```bash
# User account (holds tokens for testing)
USER_PRIVATE_KEY=your_user_private_key_without_0x
USER_ADDRESS=0xYourUserAddress

# Vault account (manages migrations)
VAULT_PRIVATE_KEY=your_vault_private_key_without_0x
VAULT_ADDRESS=0xYourVaultAddress

# Ethereum RPC endpoint
ETHEREUM_RPC_URL=https://mainnet.infura.io/v3/your-project-id
# Or: https://eth-mainnet.alchemyapi.io/v2/your-api-key

# Optional: Etherscan API for verification
ETHERSCAN_API_KEY=your_etherscan_api_key
```

2. **Token Requirements** (if no existing allowances):

   - 5 USDC minimum
   - 5 USDT minimum
   - 0.00005 WBTC minimum

3. **ETH for Gas**:
   - User account: ~0.01 ETH
   - Vault account: ~0.01 ETH

### Run Migration Test

#### Option 1: Direct Node.js execution

```bash
# From chain/evm directory
cd chain/evm
npm install
node deployment/migration/router-v4-to-v6-migration-test.js
```

#### Option 2: Using Hardhat

```bash
# From chain/ethereum directory
npx hardhat run deployment/migration/router-v4-to-v6-migration-test.js --network ethereum
```

## 🧪 Test Scenarios

### Scenario A: Existing Allowances

If vault already has allowances in RouterV4:

- ✅ Detects and migrates existing allowances
- ✅ Returns tokens directly to original users
- ✅ Perfect for testing with real production data

### Scenario B: No Existing Allowances

If vault has no allowances:

- 🔄 Creates small test deposits from user to RouterV4
- 🔄 Migrates the newly created allowances
- ✅ Returns tokens to user

## 📊 Expected Output

```bash
🚀 THORChain RouterV4 to RouterV6 Migration Test
============================================================
👤 Default signer: 0x...
👤 User: 0x742F4...
🏦 Vault: 0x8B4e5...
🔗 RouterV4: 0x3624525075b88B24ecc29CE226b0CEc1fFcB6976
🔗 RouterV6: 0xd5976E83F160B84BE90510b04C27657F240c7049

💰 Checking account balances...
   User ETH: 0.05 ETH
   Vault ETH: 0.02 ETH
   User USDC: 100.5
   User USDT: 50.0
   User WBTC: 0.001

1️⃣  Checking existing vault allowances in RouterV4...
   📊 USDC: 10.5 (10500000)
   📊 USDT: 5.0 (5000000)
   📊 WBTC: 0.0001 (10000)

   ✅ Found 3 existing allowances to migrate

2️⃣  Migrating allowances from RouterV4 to RouterV6...

   🔄 Migrating USDC...
      📊 Current allowance: 10.5 USDC
      🔄 Executing transferAllowance...
      ⏳ Transaction submitted: 0x123abc...
      ✅ Migration confirmed in block 18501234
      📈 Tokens received: 10.5 USDC
      📊 Final user balance: 111.0 USDC

[... similar for other tokens ...]

3️⃣  Verifying migration completion...
   📊 USDC remaining in V4: 0.0
   📊 USDT remaining in V4: 0.0
   📊 WBTC remaining in V4: 0.0

   📈 Migration Summary:
      • Successful migrations: 3
      • Total tokens attempted: 3
      • Remaining allowances in V4: None ✅

4️⃣  Testing RouterV6 functionality...
   🧪 Testing small deposit to RouterV6...
      📋 Approving RouterV6...
      💰 Making test deposit...
      ✅ RouterV6 test deposit successful: 0x456def...

============================================================
🏁 Migration Test Completed!

📋 Contract Addresses:
   • RouterV4: 0x3624525075b88B24ecc29CE226b0CEc1fFcB6976
   • RouterV6: 0xd5976E83F160B84BE90510b04C27657F240c7049

👤 Test Accounts:
   • User: 0x742F4...
   • Vault: 0x8B4e5...

📊 Results:
   ✅ USDC: Migrated successfully (0x123abc...)
   ✅ USDT: Migrated successfully (0x789ghi...)
   ✅ WBTC: Migrated successfully (0xdefxyz...)

🎉 Migration completed successfully!
💡 All tokens have been returned to users
🔄 RouterV6 is ready for production use

🔗 Verify transactions on Etherscan:
   RouterV4: https://etherscan.io/address/0x3624525075b88B24ecc29CE226b0CEc1fFcB6976
   RouterV6: https://etherscan.io/address/0xd5976E83F160B84BE90510b04C27657F240c7049
```

## 🔒 Security Notes

⚠️ **IMPORTANT**:

- This test uses **MAINNET** with **real funds**
- Start with **small amounts** for initial testing
- **Verify all addresses** before running
- **Never commit private keys** to version control
- Consider testing on **testnet** first

## 🧩 How Migration Works

The migration uses RouterV4's built-in `transferAllowance` function:

```solidity
// RouterV4 transfers tokens via RouterV6 back to users
routerV4.transferAllowance(
    routerV6Address,  // new router
    userAddress,      // recipient (user gets tokens back)
    tokenAddress,     // asset to migrate
    amount,          // vault's allowance amount
    memo            // migration memo
);
```

This process:

1. **Debits** the vault's allowance in RouterV4
2. **Transfers** tokens to RouterV6
3. **Credits** tokens directly to the user via RouterV6's deposit function
4. **Emits** proper events for THORChain monitoring

## 🔧 Troubleshooting

### Common Issues

- **"Insufficient balance" errors**: Ensure user has enough tokens
- **"Out of gas" errors**: Ensure both accounts have sufficient ETH
- **"transferAllowance failed"**: Verify vault has permission
- **Connection errors**: Check RPC URL and network connectivity

### Getting Help

1. Review error messages carefully
2. Check account balances and permissions
3. Verify contract addresses are correct
4. Test on smaller amounts first

## 🧪 Production Migration

This test is suitable for real production migration:

1. **Works with existing data**: Safely migrates real vault allowances
2. **Returns funds to users**: Tokens go back to original depositors
3. **Comprehensive verification**: Confirms migration completion
4. **Transaction tracking**: Provides Etherscan links for audit

After successful testing, the same process can be used for the actual production migration.

## 📋 Files Structure

```bash
chain/evm/deployment/migration/
├── router-v4-to-v6-migration-test.js  # Main test script (JavaScript)
└── README.md                          # This documentation
```
