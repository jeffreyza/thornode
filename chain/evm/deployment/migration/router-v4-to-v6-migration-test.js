const { ethers } = require("hardhat");
const path = require("path");

require("dotenv").config({ path: path.join(__dirname, "..", ".env") });

// Deployed Router Addresses on Ethereum Mainnet
const ROUTER_V4_ADDRESS = "0x33c630409883269bc281Dd40824562B066a70512"; // RouterV4 (correct address from deployment summary)
const ROUTER_V6_ADDRESS = "0xd5976E83F160B84BE90510b04C27657F240c7049"; // RouterV6 (currently deployed)

// Token addresses on Ethereum mainnet
const TOKENS = {
  USDC: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", // 6 decimals
  USDT: "0xdAC17F958D2ee523a2206206994597C13D831ec7", // 6 decimals
  WBTC: "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599", // 8 decimals
};

// Test amounts (conservative amounts for testing)
const TEST_AMOUNTS = {
  USDC: ethers.parseUnits("5", 6), // 5 USDC
  USDT: ethers.parseUnits("5", 6), // 5 USDT
  WBTC: ethers.parseUnits("0.00005", 8), // 0.00005 WBTC
};

// RouterV4 ABI (key functions)
const RouterV4_ABI = [
  "function depositWithExpiry(address payable vault, address asset, uint amount, string memory memo, uint expiration) external payable",
  "function vaultAllowance(address vault, address token) external view returns (uint amount)",
  "function transferAllowance(address router, address newVault, address asset, uint amount, string memory memo) external",
];

// RouterV6 ABI (key functions)
const RouterV6_ABI = [
  "function depositWithExpiry(address payable vault, address asset, uint amount, string memory memo, uint expiration) external payable",
  "function vaultAllowance(address vault, address token) external view returns (uint amount)",
];

// ERC20 ABI
const ERC20_ABI = [
  "function approve(address spender, uint256 amount) external returns (bool)",
  "function allowance(address owner, address spender) external view returns (uint256)",
  "function balanceOf(address account) external view returns (uint256)",
  "function transfer(address to, uint256 amount) external returns (bool)",
  "function transferFrom(address from, address to, uint256 amount) external returns (bool)",
  "function decimals() external view returns (uint8)",
];

async function main() {
  console.log("🚀 THORChain RouterV4 to RouterV6 Migration Test");
  console.log("=".repeat(60));

  // Check network connection
  const network = await ethers.provider.getNetwork();
  console.log(
    `🌐 Connected to: ${network.name} (Chain ID: ${network.chainId})`,
  );

  if (network.chainId !== 1n) {
    console.warn(
      `⚠️  Warning: Not connected to Ethereum mainnet (expected Chain ID: 1)`,
    );
  }

  // Get environment variables
  const userPrivateKey =
    process.env.USER_PRIVATE_KEY || process.env.PRIVATE_KEY;
  const vaultPrivateKey = process.env.VAULT_PRIVATE_KEY;
  const userAddress = process.env.USER_ADDRESS || process.env.address;
  const vaultAddress = process.env.VAULT_ADDRESS;

  if (!userPrivateKey || !vaultPrivateKey || !userAddress || !vaultAddress) {
    console.error("❌ Missing environment variables.");
    console.log("💡 Please check your .env file has:");
    console.log("   PRIVATE_KEY (or USER_PRIVATE_KEY) - user private key");
    console.log("   VAULT_PRIVATE_KEY - vault private key");
    console.log("   address (or USER_ADDRESS) - user address");
    console.log("   VAULT_ADDRESS - vault address");
    console.log("   ETHEREUM_RPC_URL - RPC endpoint");
    console.log("");
    console.log("Current .env location: deployment/.env");
    return;
  }

  // Create signers from private keys
  const userSigner = new ethers.Wallet(userPrivateKey, ethers.provider);
  const vaultSigner = new ethers.Wallet(vaultPrivateKey, ethers.provider);

  console.log(`👤 User: ${userAddress}`);
  console.log(`🏦 Vault: ${vaultAddress}`);
  console.log(`🔗 RouterV4: ${ROUTER_V4_ADDRESS}`);
  console.log(`🔗 RouterV6: ${ROUTER_V6_ADDRESS}`);
  console.log("");

  // Check balances
  await checkBalances(userSigner, vaultSigner, userAddress, vaultAddress);

  // Connect to contracts
  const routerV4 = new ethers.Contract(
    ROUTER_V4_ADDRESS,
    RouterV4_ABI,
    userSigner,
  );
  const routerV6 = new ethers.Contract(
    ROUTER_V6_ADDRESS,
    RouterV6_ABI,
    userSigner,
  );

  console.log("✅ Connected to deployed routers");
  console.log("");

  // ============================================================================
  // STEP 1: Check existing vault allowances in RouterV4
  // ============================================================================
  console.log("1️⃣  Checking existing vault allowances in RouterV4...");

  const existingAllowances = {};
  let hasExistingAllowances = false;

  for (const [symbol, tokenAddress] of Object.entries(TOKENS)) {
    const allowance = await routerV4.vaultAllowance(vaultAddress, tokenAddress);
    const decimals = symbol === "WBTC" ? 8 : 6;
    const formattedAllowance = ethers.formatUnits(allowance, decimals);

    console.log(
      `   📊 ${symbol}: ${formattedAllowance} (${allowance.toString()}) `,
    );

    if (allowance > 0) {
      existingAllowances[symbol] = {
        address: tokenAddress,
        amount: allowance,
        decimals,
      };
      hasExistingAllowances = true;
    }
  }

  if (!hasExistingAllowances) {
    console.log("\n   ⚠️  No existing allowances found in RouterV4.");
    console.log("   💡 Need to create test deposits first...");

    // Proceed with creating test deposits
    await createTestDeposits(routerV4, userSigner, vaultAddress);
  } else {
    console.log(
      `\n   ✅ Found ${Object.keys(existingAllowances).length} existing allowances to migrate`,
    );
  }

  // ============================================================================
  // STEP 2: Migrate allowances from RouterV4 to RouterV6
  // ============================================================================
  console.log("\n2️⃣  Migrating allowances from RouterV4 to RouterV6...");

  // Get final allowances (either existing or newly created)
  const finalAllowances = hasExistingAllowances
    ? existingAllowances
    : await getFinalAllowances(routerV4, vaultAddress);

  const migrationResults = {};

  for (const [symbol, info] of Object.entries(finalAllowances)) {
    console.log(`\n   🔄 Migrating ${symbol}...`);

    try {
      const { address: tokenAddress, amount, decimals } = info;

      console.log(
        `      📊 Current allowance: ${ethers.formatUnits(amount, decimals)} ${symbol}`,
      );

      // Get user balance before migration
      const token = new ethers.Contract(tokenAddress, ERC20_ABI, userSigner);
      const userBalanceBefore = await token.balanceOf(userAddress);

      // Perform migration using RouterV4's transferAllowance function
      console.log(`      🔄 Executing transferAllowance...`);
      const memo = `MIGRATE:${symbol}:V4_TO_V6`;

      const migrateTx = await routerV4.connect(vaultSigner).transferAllowance(
        ROUTER_V6_ADDRESS, // new router
        userAddress, // recipient (user gets tokens back)
        tokenAddress, // asset
        amount, // amount to migrate
        memo, // memo
      );

      console.log(`      ⏳ Transaction submitted: ${migrateTx.hash}`);
      const receipt = await migrateTx.wait();
      console.log(
        `      ✅ Migration confirmed in block ${receipt.blockNumber}`,
      );

      // Check user balance after migration
      const userBalanceAfter = await token.balanceOf(userAddress);
      const tokensReceived = userBalanceAfter - userBalanceBefore;

      console.log(
        `      📈 Tokens received: ${ethers.formatUnits(tokensReceived, decimals)} ${symbol}`,
      );
      console.log(
        `      📊 Final user balance: ${ethers.formatUnits(userBalanceAfter, decimals)} ${symbol}`,
      );

      migrationResults[symbol] = {
        success: true,
        tokensReceived,
        txHash: migrateTx.hash,
        blockNumber: receipt.blockNumber,
      };
    } catch (error) {
      console.error(`      ❌ Migration failed for ${symbol}:`, error.message);
      migrationResults[symbol] = {
        success: false,
        error: error.message,
      };
    }
  }

  // ============================================================================
  // STEP 3: Verify migration completion
  // ============================================================================
  console.log("\n3️⃣  Verifying migration completion...");

  let totalRemainingAllowances = 0n;
  let successfulMigrations = 0;

  for (const [symbol, tokenAddress] of Object.entries(TOKENS)) {
    const remainingAllowance = await routerV4.vaultAllowance(
      vaultAddress,
      tokenAddress,
    );
    const decimals = symbol === "WBTC" ? 8 : 6;
    totalRemainingAllowances += remainingAllowance;

    console.log(
      `   📊 ${symbol} remaining in V4: ${ethers.formatUnits(remainingAllowance, decimals)}`,
    );

    if (migrationResults[symbol]?.success) {
      successfulMigrations++;
    }
  }

  console.log(`\n   📈 Migration Summary:`);
  console.log(`      • Successful migrations: ${successfulMigrations}`);
  console.log(
    `      • Total tokens attempted: ${Object.keys(finalAllowances || {}).length}`,
  );
  console.log(
    `      • Remaining allowances in V4: ${totalRemainingAllowances === 0n ? "None ✅" : "Some remain ⚠️"}`,
  );

  // ============================================================================
  // STEP 4: Test RouterV6 functionality
  // ============================================================================
  console.log("\n4️⃣  Testing RouterV6 functionality...");

  console.log("   🧪 Testing small deposit to RouterV6...");

  try {
    const testToken = TOKENS.USDC;
    const testAmount = ethers.parseUnits("1", 6); // 1 USDC

    // Calculate expiry: current time + 15 minutes (900 seconds)
    const currentTime = Math.floor(Date.now() / 1000);
    const expiry = currentTime + 900; // 15 minutes from now

    const token = new ethers.Contract(testToken, ERC20_ABI, userSigner);
    const userBalance = await token.balanceOf(userAddress);

    if (userBalance >= testAmount) {
      // Approve RouterV6
      console.log("      📋 Approving RouterV6...");
      const approveTx = await token.approve(ROUTER_V6_ADDRESS, testAmount);
      await approveTx.wait();

      // Deposit to RouterV6 with proper expiry
      console.log("      💰 Making test deposit...");
      const depositTx = await routerV6.connect(userSigner).depositWithExpiry(
        userAddress, // vault (deposit to self for testing)
        testToken, // asset
        testAmount, // amount
        "TEST:V6:DEPOSIT", // memo
        expiry, // expiry timestamp (+15 minutes)
      );
      await depositTx.wait();

      console.log(
        `      ✅ RouterV6 test deposit successful: ${depositTx.hash}`,
      );
    } else {
      console.log("      ⚠️  Insufficient USDC for RouterV6 test");
    }
  } catch (error) {
    console.error("      ❌ RouterV6 test failed:", error.message);
  }

  // ============================================================================
  // Final Summary
  // ============================================================================
  console.log("\n" + "=".repeat(60));
  console.log("🏁 Migration Test Completed!");
  console.log("");
  console.log("📋 Contract Addresses:");
  console.log(`   • RouterV4: ${ROUTER_V4_ADDRESS}`);
  console.log(`   • RouterV6: ${ROUTER_V6_ADDRESS}`);
  console.log("");
  console.log("👤 Test Accounts:");
  console.log(`   • User: ${userAddress}`);
  console.log(`   • Vault: ${vaultAddress}`);
  console.log("");
  console.log("📊 Results:");
  for (const [symbol, result] of Object.entries(migrationResults)) {
    if (result.success) {
      console.log(`   ✅ ${symbol}: Migrated successfully (${result.txHash})`);
    } else {
      console.log(`   ❌ ${symbol}: Migration failed - ${result.error}`);
    }
  }

  if (totalRemainingAllowances === 0n && successfulMigrations > 0) {
    console.log("\n🎉 Migration completed successfully!");
    console.log("💡 All tokens have been returned to users");
    console.log("🔄 RouterV6 is ready for production use");
  } else if (successfulMigrations > 0) {
    console.log("\n⚠️  Migration partially completed");
    console.log("🔧 Some tokens may need manual intervention");
  } else {
    console.log("\n❌ Migration test failed");
    console.log("🔧 Check error messages above for troubleshooting");
  }

  console.log("");
  console.log("🔗 Verify transactions on Etherscan:");
  console.log(`   RouterV4: https://etherscan.io/address/${ROUTER_V4_ADDRESS}`);
  console.log(`   RouterV6: https://etherscan.io/address/${ROUTER_V6_ADDRESS}`);
}

// Helper function to check balances
async function checkBalances(
  userSigner,
  vaultSigner,
  userAddress,
  vaultAddress,
) {
  console.log("💰 Checking account balances...");

  const userEthBalance = await ethers.provider.getBalance(userAddress);
  const vaultEthBalance = await ethers.provider.getBalance(vaultAddress);

  console.log(`   User ETH: ${ethers.formatEther(userEthBalance)} ETH`);
  console.log(`   Vault ETH: ${ethers.formatEther(vaultEthBalance)} ETH`);

  // Check token balances
  for (const [symbol, tokenAddress] of Object.entries(TOKENS)) {
    const token = new ethers.Contract(tokenAddress, ERC20_ABI, userSigner);
    const userBalance = await token.balanceOf(userAddress);
    const decimals = symbol === "WBTC" ? 8 : 6;
    console.log(
      `   User ${symbol}: ${ethers.formatUnits(userBalance, decimals)}`,
    );
  }

  if (userEthBalance < ethers.parseEther("0.005")) {
    console.warn("   ⚠️  User has low ETH balance for gas fees");
  }
  if (vaultEthBalance < ethers.parseEther("0.005")) {
    console.warn("   ⚠️  Vault has low ETH balance for gas fees");
  }
  console.log("");
}

// Helper function to create test deposits if none exist
async function createTestDeposits(routerV4, userSigner, vaultAddress) {
  console.log("\n   📝 Creating test deposits to RouterV4...");

  // Calculate expiry: current time + 15 minutes (900 seconds)
  const currentTime = Math.floor(Date.now() / 1000);
  const expiry = currentTime + 900; // 15 minutes from now
  console.log(
    `   ⏰ Using expiry: ${expiry} (${new Date(expiry * 1000).toISOString()})`,
  );

  for (const [symbol, tokenAddress] of Object.entries(TOKENS)) {
    try {
      const amount = TEST_AMOUNTS[symbol];
      const decimals = symbol === "WBTC" ? 8 : 6;
      const token = new ethers.Contract(tokenAddress, ERC20_ABI, userSigner);

      // Check user balance
      const userBalance = await token.balanceOf(userSigner.address);

      if (userBalance < amount) {
        console.log(`      ⚠️  Insufficient ${symbol} balance. Skipping...`);
        continue;
      }

      // Handle USDT's non-standard approve behavior
      if (symbol === "USDT") {
        const currentAllowance = await token.allowance(
          userSigner.address,
          ROUTER_V4_ADDRESS,
        );
        if (currentAllowance > 0) {
          console.log(`      🔄 Resetting ${symbol} allowance to 0 first...`);
          const resetTx = await token.approve(ROUTER_V4_ADDRESS, 0);
          await resetTx.wait();
        }
      }

      // Approve RouterV4
      console.log(`      📋 Approving ${symbol}...`);
      const approveTx = await token.approve(ROUTER_V4_ADDRESS, amount);
      await approveTx.wait();

      // Deposit to RouterV4 with proper expiry
      console.log(
        `      💰 Depositing ${ethers.formatUnits(amount, decimals)} ${symbol}...`,
      );
      const memo = `TEST:DEPOSIT:${symbol}`;
      const depositTx = await routerV4.depositWithExpiry(
        vaultAddress, // vault
        tokenAddress, // asset
        amount, // amount
        memo, // memo
        expiry, // expiry timestamp (+15 minutes)
      );
      await depositTx.wait();

      console.log(`      ✅ ${symbol} deposit confirmed: ${depositTx.hash}`);
    } catch (error) {
      console.error(`      ❌ Failed to deposit ${symbol}:`, error.message);
    }
  }
}

// Helper function to get final allowances after deposits
async function getFinalAllowances(routerV4, vaultAddress) {
  const allowances = {};

  for (const [symbol, tokenAddress] of Object.entries(TOKENS)) {
    const amount = await routerV4.vaultAllowance(vaultAddress, tokenAddress);
    if (amount > 0) {
      allowances[symbol] = {
        address: tokenAddress,
        amount,
        decimals: symbol === "WBTC" ? 8 : 6,
      };
    }
  }

  return allowances;
}

// Run the migration test
if (require.main === module) {
  main()
    .then(() => process.exit(0))
    .catch((error) => {
      console.error("💥 Migration test failed:", error);
      process.exit(1);
    });
}

module.exports = { main };
