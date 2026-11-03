const { ethers } = require("hardhat");

// Nick's CREATE2 factory address (same on all chains)
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";

// Expected factory bytecode (partial check)
const EXPECTED_BYTECODE_PREFIX =
  "0x7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffe03601600081602082378035828234f58015156039578182fd5b8082525050506014600cf3";

async function checkNicksFactory() {
  const networkName = hre.network.name;

  console.log(
    `\n🔍 Checking Nick's CREATE2 Factory on ${networkName.toUpperCase()}`,
  );
  console.log("======================================================");
  console.log(`Factory Address: ${NICKS_FACTORY}`);

  try {
    // Check if factory exists
    const factoryCode = await ethers.provider.getCode(NICKS_FACTORY);

    if (factoryCode === "0x") {
      console.log(`❌ Nick's CREATE2 factory NOT found on ${networkName}`);
      console.log(`   No bytecode at address ${NICKS_FACTORY}`);
      return false;
    }

    console.log(`✅ Nick's CREATE2 factory found on ${networkName}!`);
    console.log(`   Bytecode length: ${factoryCode.length} chars`);

    // Verify it starts with expected bytecode
    if (factoryCode.startsWith(EXPECTED_BYTECODE_PREFIX)) {
      console.log(`✅ Factory bytecode verification passed`);
    } else {
      console.log(`⚠️  Factory bytecode doesn't match expected pattern`);
      console.log(
        `   Expected prefix: ${EXPECTED_BYTECODE_PREFIX.substring(0, 50)}...`,
      );
      console.log(`   Actual prefix:   ${factoryCode.substring(0, 50)}...`);
    }

    // Get network info
    const network = await ethers.provider.getNetwork();
    console.log(`   Network: ${network.name} (Chain ID: ${network.chainId})`);

    // Check balance (should be 0 for a factory)
    const balance = await ethers.provider.getBalance(NICKS_FACTORY);
    console.log(`   Balance: ${ethers.formatEther(balance)} ETH`);

    return true;
  } catch (error) {
    console.error(
      `❌ Error checking factory on ${networkName}:`,
      error.message,
    );
    return false;
  }
}

async function checkAllNetworks() {
  console.log("\n🌐 Checking Nick's CREATE2 Factory across all networks");
  console.log("=".repeat(60));

  const networks = ["ethereum", "base", "bsc", "avalanche"];
  const results = {};

  for (const network of networks) {
    try {
      // This is a simplified check - in practice, you'd need to switch networks
      console.log(`\n📡 Checking ${network.toUpperCase()}...`);
      console.log(
        `   Run: npx hardhat run deployment/routerv6/utils-check-factory.js --network ${network}`,
      );
      results[network] = "pending";
    } catch (error) {
      console.error(`❌ ${network} check failed:`, error.message);
      results[network] = false;
    }
  }

  console.log("\n📊 Summary:");
  console.log("To check all networks, run these commands:");
  networks.forEach((network) => {
    console.log(
      `npx hardhat run deployment/routerv6/utils-check-factory.js --network ${network}`,
    );
  });

  return results;
}

// Run check if called directly
if (require.main === module) {
  checkNicksFactory()
    .then((exists) => {
      if (exists) {
        console.log(
          `\n🎉 Nick's CREATE2 factory is ready for RouterV6 deployment!`,
        );
        process.exit(0);
      } else {
        console.log(
          `\n❌ Nick's CREATE2 factory is NOT available on this network`,
        );
        console.log(`   RouterV6 deployment will fail without the factory`);
        process.exit(1);
      }
    })
    .catch((error) => {
      console.error("❌ Factory check failed:", error);
      process.exit(1);
    });
}

module.exports = {
  checkNicksFactory,
  checkAllNetworks,
  NICKS_FACTORY,
};
