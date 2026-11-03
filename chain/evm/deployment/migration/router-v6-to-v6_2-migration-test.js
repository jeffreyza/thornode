const { ethers } = require("hardhat");
const path = require("path");

require("dotenv").config({ path: path.join(__dirname, "..", ".env") });

// Deployed Router Addresses on Ethereum Mainnet
const ROUTER_V6_ADDRESS = "0xd5976E83F160B84BE90510b04C27657F240c7049"; // RouterV6 (original)
const ROUTER_V6_2_ADDRESS = "0xcAE4F95f7e2356044331E3080C8b65ae98B57c06"; // RouterV6_2 (new)

// Token addresses on Ethereum mainnet
const TOKENS = {
  USDC: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", // 6 decimals
  USDT: "0xdAC17F958D2ee523a2206206994597C13D831ec7", // 6 decimals
  WBTC: "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599", // 8 decimals
};

// Test amounts (conservative amounts for testing)
const TEST_AMOUNTS = {
  USDC: ethers.parseUnits("2", 6), // 2 USDC
  USDT: ethers.parseUnits("2", 6), // 2 USDT
  WBTC: ethers.parseUnits("0.00002", 8), // 0.00002 WBTC
};

// RouterV6 ABI (includes transferAllowance for migration)
const RouterV6_ABI = [
  "function depositWithExpiry(address payable vault, address asset, uint amount, string memory memo, uint expiration) external payable",
  "function vaultAllowance(address vault, address token) external view returns (uint amount)",
  "function transferAllowance(address router, address newVault, address asset, uint amount, string memory memo) external",
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
  console.log("🚀 THORChain RouterV6 to RouterV6_2 Migration Test");
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
  console.log(`🔗 RouterV6 (Old): ${ROUTER_V6_ADDRESS}`);
  console.log(`🔗 RouterV6_2 (New): ${ROUTER_V6_2_ADDRESS}`);
  console.log("");

  // Check balances
  await checkBalances(userSigner, vaultSigner, userAddress, vaultAddress);

  // Connect to contracts
  const routerV6 = new ethers.Contract(
    ROUTER_V6_ADDRESS,
    RouterV6_ABI,
    userSigner,
  );
  const routerV6_2 = new ethers.Contract(
    ROUTER_V6_2_ADDRESS,
    RouterV6_ABI,
    userSigner,
  );

  console.log("✅ Connected to deployed routers");
  console.log("");

  console.log("📝 IMPORTANT: RouterV6 → RouterV6_2 Migration Requirements:");
  console.log(
    "   • Tokens must be in vault's wallet (not deposited as router allowances)",
  );
  console.log(
    "   • Vault must approve RouterV6 to spend tokens from its wallet",
  );
  console.log(
    "   • ETH migration is NOT supported by transferAllowance function",
  );
  console.log("   • Only ERC20 tokens can be migrated using this method");
  console.log("");

  // ============================================================================
  // STEP 1: Check vault token balances (must have tokens to migrate)
  // ============================================================================
  console.log("📝 STEP 1: Check vault token balances");
  console.log("=".repeat(50));

  const availableTokens = await checkVaultTokenBalances(
    vaultSigner,
    vaultAddress,
  );

  if (availableTokens.length === 0) {
    console.log("❌ No tokens available in vault wallet for migration test");
    console.log(
      "💡 The vault needs tokens in its wallet (not router allowances) to test migration",
    );
    return;
  }

  // ============================================================================
  // STEP 2: Test token migrations from RouterV6 to RouterV6_2
  // ============================================================================
  console.log("📝 STEP 2: Test RouterV6 → RouterV6_2 token migrations");
  console.log("=".repeat(60));

  await testTokenMigrations(
    vaultSigner,
    routerV6,
    vaultAddress,
    availableTokens,
  );

  console.log("🎉 RouterV6 to RouterV6_2 migration test completed!");
}

async function checkBalances(
  userSigner,
  vaultSigner,
  userAddress,
  vaultAddress,
) {
  console.log("💰 Checking Account Balances");
  console.log("-".repeat(40));

  try {
    // Check ETH balances
    const userETH = await ethers.provider.getBalance(userAddress);
    const vaultETH = await ethers.provider.getBalance(vaultAddress);

    console.log(`👤 User ETH: ${ethers.formatEther(userETH)} ETH`);
    console.log(`🏦 Vault ETH: ${ethers.formatEther(vaultETH)} ETH`);

    // Check token balances
    for (const [symbol, address] of Object.entries(TOKENS)) {
      const contract = new ethers.Contract(address, ERC20_ABI, userSigner);
      const userBalance = await contract.balanceOf(userAddress);
      const vaultBalance = await contract.balanceOf(vaultAddress);
      const decimals = await contract.decimals();

      console.log(
        `👤 User ${symbol}: ${ethers.formatUnits(userBalance, decimals)} ${symbol}`,
      );
      console.log(
        `🏦 Vault ${symbol}: ${ethers.formatUnits(vaultBalance, decimals)} ${symbol}`,
      );
    }
    console.log("");
  } catch (error) {
    console.error("❌ Error checking balances:", error.message);
  }
}

async function checkVaultTokenBalances(vaultSigner, vaultAddress) {
  console.log("🔍 Checking which tokens vault has available for migration...");

  const availableTokens = [];

  for (const [symbol, address] of Object.entries(TOKENS)) {
    try {
      const contract = new ethers.Contract(address, ERC20_ABI, vaultSigner);
      const balance = await contract.balanceOf(vaultAddress);
      const decimals = await contract.decimals();

      console.log(
        `🪙 Vault ${symbol}: ${ethers.formatUnits(balance, decimals)} ${symbol}`,
      );

      // Check if vault has enough for our test amount
      if (balance >= TEST_AMOUNTS[symbol]) {
        availableTokens.push({
          symbol,
          address,
          balance,
          decimals,
          testAmount: TEST_AMOUNTS[symbol],
        });
        console.log(`   ✅ Sufficient for migration test`);
      } else if (balance > 0n) {
        // Use whatever balance is available
        availableTokens.push({
          symbol,
          address,
          balance,
          decimals,
          testAmount: balance,
        });
        console.log(
          `   ⚠️  Using available balance: ${ethers.formatUnits(balance, decimals)} ${symbol}`,
        );
      } else {
        console.log(`   ❌ No ${symbol} available for migration`);
      }
    } catch (error) {
      console.log(`⚠️  Could not check ${symbol} balance: ${error.message}`);
    }
  }

  console.log(
    `\n📊 Found ${availableTokens.length} tokens available for migration\n`,
  );
  return availableTokens;
}

async function testTokenMigrations(
  vaultSigner,
  routerV6,
  vaultAddress,
  availableTokens,
) {
  console.log("🔄 Starting RouterV6 → RouterV6_2 migrations...");

  const migrationResults = {};

  for (const tokenInfo of availableTokens) {
    const { symbol, address, testAmount, decimals } = tokenInfo;

    try {
      console.log(`\n🪙 Testing ${symbol} migration...`);
      console.log(
        `   Amount: ${ethers.formatUnits(testAmount, decimals)} ${symbol}`,
      );

      const contract = new ethers.Contract(address, ERC20_ABI, vaultSigner);

      // Step 1: Check current allowance
      const currentAllowance = await contract.allowance(
        vaultAddress,
        ROUTER_V6_ADDRESS,
      );
      console.log(
        `   Current RouterV6 allowance: ${ethers.formatUnits(currentAllowance, decimals)} ${symbol}`,
      );

      // Step 2: Approve RouterV6 if needed
      if (currentAllowance < testAmount) {
        console.log(`   📝 Approving RouterV6 to spend ${symbol}...`);

        // Special handling for USDT (needs to set allowance to 0 first)
        if (symbol === "USDT" && currentAllowance > 0n) {
          console.log(`   🔄 Resetting USDT allowance to 0 first...`);
          const resetTx = await contract
            .connect(vaultSigner)
            .approve(ROUTER_V6_ADDRESS, 0);
          await resetTx.wait();
        }

        const approveTx = await contract
          .connect(vaultSigner)
          .approve(ROUTER_V6_ADDRESS, testAmount);
        await approveTx.wait();
        console.log(`   ✅ Approval successful: ${approveTx.hash}`);
      }

      // Step 3: Execute migration using transferAllowance
      console.log(`   🔄 Executing transferAllowance...`);
      const memo = `MIGRATE:${symbol}:V6->V6_2:TEST`;

      const migrateTx = await routerV6.connect(vaultSigner).transferAllowance(
        ROUTER_V6_2_ADDRESS, // new router
        vaultAddress, // new vault (same vault in this test)
        address, // asset
        testAmount, // amount
        memo, // memo
      );

      console.log(`   ⏳ Migration transaction: ${migrateTx.hash}`);
      const receipt = await migrateTx.wait();
      console.log(`   ✅ Migration confirmed in block ${receipt.blockNumber}`);

      migrationResults[symbol] = {
        success: true,
        amount: testAmount,
        txHash: migrateTx.hash,
        blockNumber: receipt.blockNumber,
      };
    } catch (error) {
      console.log(`   ❌ Migration failed: ${error.message}`);
      migrationResults[symbol] = {
        success: false,
        error: error.message,
      };
    }
  }

  // ============================================================================
  // Summary
  // ============================================================================
  console.log("\n" + "=".repeat(60));
  console.log("📊 Migration Results Summary");
  console.log("=".repeat(60));

  let successCount = 0;
  let totalCount = 0;

  for (const [symbol, result] of Object.entries(migrationResults)) {
    totalCount++;
    if (result.success) {
      successCount++;
      console.log(`✅ ${symbol}: Migration successful`);
      console.log(`   📋 Transaction: ${result.txHash}`);
      console.log(`   📊 Block: ${result.blockNumber}`);
    } else {
      console.log(`❌ ${symbol}: Migration failed`);
      console.log(`   📋 Error: ${result.error}`);
    }
  }

  console.log("\n" + "-".repeat(60));
  console.log(
    `📈 Overall Results: ${successCount}/${totalCount} migrations successful`,
  );

  if (successCount === totalCount && totalCount > 0) {
    console.log("🎉 All migrations completed successfully!");
  } else if (successCount > 0) {
    console.log("⚠️  Partial success - some migrations failed");
  } else {
    console.log("❌ All migrations failed");
  }

  console.log("\n🔗 Verify on Etherscan:");
  console.log(`   RouterV6: https://etherscan.io/address/${ROUTER_V6_ADDRESS}`);
  console.log(
    `   RouterV6_2: https://etherscan.io/address/${ROUTER_V6_2_ADDRESS}`,
  );
}

// Execute the main function
if (require.main === module) {
  main()
    .then(() => process.exit(0))
    .catch((error) => {
      console.error("\n❌ Migration test failed:");
      console.error(error);
      process.exit(1);
    });
}

module.exports = { main };
