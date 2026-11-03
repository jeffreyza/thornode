const { ethers } = require("hardhat");

// Token addresses (Ethereum mainnet)
const TOKENS = {
  USDT: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
  USDC: "0xA0b86a33E6441e03205c64cb6b7d4B8aEb865Ba4", // Note: This might be old USDC, check current
  WBTC: "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599",
};

// Minimum amounts required for testing
const MIN_AMOUNTS = {
  ETH: ethers.parseEther("0.1"), // Need at least 0.1 ETH for gas + testing
  USDT: ethers.parseUnits("10.0", 6), // 10 USDT
  USDC: ethers.parseUnits("10.0", 6), // 10 USDC
  WBTC: ethers.parseUnits("0.0001", 8), // 0.0001 WBTC
};

// ERC20 ABI (minimal)
const ERC20_ABI = [
  "function balanceOf(address account) external view returns (uint256)",
  "function symbol() external view returns (string)",
  "function decimals() external view returns (uint8)",
];

async function checkBalances() {
  console.log("💰 Checking RouterV6 Test Account Balances");
  console.log("===========================================");

  const [deployer] = await ethers.getSigners();
  console.log(`👤 Account: ${deployer.address}`);

  const network = await ethers.provider.getNetwork();
  console.log(`🌐 Network: ${network.name} (Chain ID: ${network.chainId})`);

  let allSufficient = true;
  const results = {};

  // Check ETH balance
  try {
    const ethBalance = await ethers.provider.getBalance(deployer.address);
    const ethFormatted = ethers.formatEther(ethBalance);
    const minEthFormatted = ethers.formatEther(MIN_AMOUNTS.ETH);

    const sufficient = ethBalance >= MIN_AMOUNTS.ETH;
    const status = sufficient ? "✅" : "❌";

    console.log(`\n${status} ETH: ${ethFormatted} (min: ${minEthFormatted})`);

    if (!sufficient) {
      console.log(
        `   ⚠️  Need ${ethers.formatEther(MIN_AMOUNTS.ETH - ethBalance)} more ETH`,
      );
      allSufficient = false;
    }

    results.ETH = {
      balance: ethFormatted,
      minimum: minEthFormatted,
      sufficient,
      raw: ethBalance,
    };
  } catch (error) {
    console.log(`❌ ETH: Error checking balance - ${error.message}`);
    results.ETH = { error: error.message, sufficient: false };
    allSufficient = false;
  }

  // Check token balances
  for (const [symbol, address] of Object.entries(TOKENS)) {
    try {
      const token = new ethers.Contract(address, ERC20_ABI, deployer);

      const balance = await token.balanceOf(deployer.address);
      const decimals = symbol === "WBTC" ? 8 : 6;
      const balanceFormatted = ethers.formatUnits(balance, decimals);
      const minFormatted = ethers.formatUnits(MIN_AMOUNTS[symbol], decimals);

      const sufficient = balance >= MIN_AMOUNTS[symbol];
      const status = sufficient ? "✅" : "❌";

      console.log(
        `${status} ${symbol}: ${balanceFormatted} (min: ${minFormatted})`,
      );
      console.log(`   Address: ${address}`);

      if (!sufficient) {
        const needed = MIN_AMOUNTS[symbol] - balance;
        console.log(
          `   ⚠️  Need ${ethers.formatUnits(needed, decimals)} more ${symbol}`,
        );
        allSufficient = false;
      }

      results[symbol] = {
        balance: balanceFormatted,
        minimum: minFormatted,
        sufficient,
        address,
        raw: balance,
      };
    } catch (error) {
      console.log(`❌ ${symbol}: Error checking balance - ${error.message}`);
      results[symbol] = { error: error.message, sufficient: false, address };
      allSufficient = false;
    }
  }

  // Summary
  console.log("\n📊 BALANCE CHECK SUMMARY");
  console.log("========================");

  if (allSufficient) {
    console.log("✅ All balances are sufficient for RouterV6 testing!");
    console.log("🚀 Ready to run mainnet tests");
  } else {
    console.log("❌ Some balances are insufficient for testing");
    console.log("💸 Please fund the account with the required tokens");

    console.log("\n🔗 Where to get tokens:");
    console.log("   • ETH: Ethereum mainnet (for gas and testing)");
    console.log("   • USDT: Uniswap, Coinbase, or other DEX/CEX");
    console.log("   • USDC: Uniswap, Coinbase, or other DEX/CEX");
    console.log("   • WBTC: Uniswap or wrapped BTC services");
  }

  return {
    allSufficient,
    results,
    account: deployer.address,
    network: network.name,
    chainId: network.chainId,
  };
}

async function checkMinimumForTest() {
  const { allSufficient, results } = await checkBalances();

  if (!allSufficient) {
    console.log("\n❌ Cannot proceed with RouterV6 testing");
    console.log("Please ensure all token balances meet minimum requirements");
    process.exit(1);
  }

  console.log("\n✅ All checks passed - ready for RouterV6 testing!");
  return results;
}

// Run check if called directly
if (require.main === module) {
  checkMinimumForTest()
    .then(() => {
      console.log("\n🎉 Balance check completed successfully!");
      process.exit(0);
    })
    .catch((error) => {
      console.error("❌ Balance check failed:", error);
      process.exit(1);
    });
}

module.exports = {
  checkBalances,
  checkMinimumForTest,
  TOKENS,
  MIN_AMOUNTS,
};
