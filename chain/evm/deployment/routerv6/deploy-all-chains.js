const { spawn } = require("child_process");
const path = require("path");

// Usage: node deploy-all-chains.js --salt <salt> --address <address> [--pattern <pattern>]
//
// Examples:
//   # Deploy with 0x0DC610 pattern (stagenet)
//   node deploy-all-chains.js --salt 0x00000000000000015aab67962d1882520000000000000000000000000016079c --address 0x0DC6108C9225Ce93Da589B4CE83c104b34693117 --pattern 0x0DC610
//
//   # Deploy with 0x00DC6100 pattern (mainnet)
//   node deploy-all-chains.js --salt 0x00000000000000045b7b09fc668461dc0000000000000000000000000c55c655 --address 0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4 --pattern 0x00DC6100

// Networks to deploy to
const NETWORKS = ["ethereum", "base", "bsc", "avalanche"];

// Parse command line arguments
const args = process.argv.slice(2);
let customSalt = null;
let expectedAddress = null;
let patternName = "custom";

// Parse arguments
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--salt" && i + 1 < args.length) {
    customSalt = args[i + 1];
    i++; // Skip the next argument
  } else if (args[i] === "--address" && i + 1 < args.length) {
    expectedAddress = args[i + 1];
    i++; // Skip the next argument
  } else if (args[i] === "--pattern" && i + 1 < args.length) {
    patternName = args[i + 1];
    i++; // Skip the next argument
  }
}

// Validate required parameters
if (!customSalt) {
  console.error("❌ Error: --salt parameter is required");
  console.log(
    "Usage: node deploy-all-chains.js --salt <salt> --address <address> [--pattern <pattern>]",
  );
  process.exit(1);
}

if (!expectedAddress) {
  console.error("❌ Error: --address parameter is required");
  console.log(
    "Usage: node deploy-all-chains.js --salt <salt> --address <address> [--pattern <pattern>]",
  );
  process.exit(1);
}

// RouterV6.1 Custom Salt Configuration - Using grinder results
const ROUTER_V6_CUSTOM_DESCRIPTION = `RouterV6.1 (with custom grinder salt for ${patternName} pattern)`;

function runCommand(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    console.log(`\n🚀 Running: ${command} ${args.join(" ")}`);

    const child = spawn(command, args, {
      stdio: "inherit",
      ...options,
    });

    child.on("close", (code) => {
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(`Command failed with exit code ${code}`));
      }
    });

    child.on("error", (error) => {
      reject(error);
    });
  });
}

async function deployRouterV6AllChains() {
  console.log(`
🌐 THORChain RouterV6.1 Custom Salt Multi-Chain Deployment
======================================================
Description: ${ROUTER_V6_CUSTOM_DESCRIPTION}
Expected Address: ${expectedAddress}
Networks: ${NETWORKS.join(", ")}
Custom Salt: ${customSalt}
`);

  const results = {
    successful: [],
    failed: [],
    skipped: [],
    addresses: {},
  };

  // Deploy to each network
  for (const network of NETWORKS) {
    try {
      console.log(`\n📡 Deploying to ${network.toUpperCase()}...`);
      console.log("=".repeat(50));

      await runCommand(
        "npx",
        [
          "hardhat",
          "run",
          "deployment/routerv6/deploy-single-chain.js",
          "--network",
          network,
        ],
        {
          env: {
            ...process.env,
            CUSTOM_SALT: customSalt,
            EXPECTED_ADDRESS: expectedAddress,
            PATTERN_NAME: patternName,
          },
        },
      );

      results.successful.push(network);
      console.log(
        `✅ ${network.toUpperCase()} deployment completed successfully!`,
      );
    } catch (error) {
      console.error(
        `❌ ${network.toUpperCase()} deployment failed:`,
        error.message,
      );
      results.failed.push({ network, error: error.message });
    }
  }

  // Print final summary
  console.log(`\n
🏆 DEPLOYMENT SUMMARY
====================`);

  if (results.successful.length > 0) {
    console.log(`✅ SUCCESSFUL (${results.successful.length}):`);
    results.successful.forEach((network) => {
      console.log(
        `   • ${network.toUpperCase()}: RouterV6.1 deployed successfully to ${expectedAddress}`,
      );
    });
  }

  if (results.failed.length > 0) {
    console.log(`\n❌ FAILED (${results.failed.length}):`);
    results.failed.forEach(({ network, error }) => {
      console.log(`   • ${network.toUpperCase()}: ${error}`);
    });
  }

  if (results.skipped.length > 0) {
    console.log(`\n⏭️  SKIPPED (${results.skipped.length}):`);
    results.skipped.forEach((network) => {
      console.log(`   • ${network.toUpperCase()}`);
    });
  }

  console.log(`\n🎯 RouterV6.1 Custom Salt Deployment:`);
  console.log(`   Expected Address: ${expectedAddress}`);
  console.log(`   Salt: ${customSalt}`);
  console.log(`   Pattern: ${patternName} (custom grinder result)`);
  console.log(`   Factory: Nick's CREATE2 Factory`);
  console.log(
    `\n📊 Note: This deployment uses a custom vanity address with ${patternName} pattern`,
  );
  console.log(`🔗 All chains will deploy to the same deterministic address`);

  // Exit with error if any deployments failed
  if (results.failed.length > 0) {
    console.log(`\n❌ ${results.failed.length} deployment(s) failed!`);
    process.exit(1);
  } else {
    console.log(
      `\n🎉 All RouterV6.1 custom salt deployments completed successfully!`,
    );
    console.log(
      `💡 RouterV6.1 with vanity address 0x00dc6100103BC402d490aEE3F9a5560cBd91f1d4 is now deployed!`,
    );
    process.exit(0);
  }
}

// Run if called directly
if (require.main === module) {
  deployRouterV6AllChains().catch((error) => {
    console.error("❌ Multi-chain deployment failed:", error);
    process.exit(1);
  });
}

module.exports = { deployRouterV6AllChains };
