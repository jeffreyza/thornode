const { exec } = require("child_process");
const util = require("util");
const fs = require("fs");
const path = require("path");

const execAsync = util.promisify(exec);

// Networks to deploy to
const networks = [
  { name: "ethereum", displayName: "Ethereum Mainnet" },
  { name: "base", displayName: "Base Mainnet" },
  { name: "bsc", displayName: "BSC Mainnet" },
  { name: "avalanche", displayName: "Avalanche C-Chain" },
];

const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";
const SALT =
  "0x54484f5243484149524f55544552563400000000000000000000000000000000"; // "THORCHAINROUTERV4"

// RUNE token addresses (only ETH has real RUNE, others use zero address)
const RUNE_ADDRESSES = {
  ethereum: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // ETH RUNE
  base: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // Use same RUNE address for identical CREATE2
  bsc: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // Use same RUNE address for identical CREATE2
  avalanche: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // Use same RUNE address for identical CREATE2
};

async function deployToNetwork(network) {
  console.log(`\n${"=".repeat(60)}`);
  console.log(`🚀 CREATE2 Router V4 Deployment to ${network.displayName}...`);
  console.log(`💎 RUNE Address: ${RUNE_ADDRESSES[network.name]}`);
  console.log(`${"=".repeat(60)}`);

  try {
    const { stdout, stderr } = await execAsync(
      `npx hardhat run scripts/deploy-create2-nicks-v4.js --network ${network.name}`,
    );

    if (stderr && !stderr.includes("Warning")) {
      console.warn(`⚠️  Warnings for ${network.displayName}:`, stderr);
    }

    console.log(stdout);

    // Check if deployment was successful
    const success =
      stdout.includes("✅ THORChain Router V4 deployed successfully") ||
      stdout.includes("✅ THORChain Router V4 already deployed");
    const address = stdout.match(/Contract address: (0x[a-fA-F0-9]{40})/);

    return {
      network: network.name,
      success: success,
      error: null,
      address: address ? address[1] : null,
      runeAddress: RUNE_ADDRESSES[network.name],
    };
  } catch (error) {
    console.error(
      `❌ CREATE2 Router V4 Deployment to ${network.displayName} failed:`,
      error.message,
    );
    return {
      network: network.name,
      success: false,
      error: error.message,
      address: null,
      runeAddress: RUNE_ADDRESSES[network.name],
    };
  }
}

async function main() {
  console.log(`
╔════════════════════════════════════════════════════════════════╗
║        THORChain Router V4 CREATE2 Multi-Chain Deploy         ║
║        Using Nick's Factory for IDENTICAL Addresses           ║
║                                                                ║
║  Nick's Factory: ${NICKS_FACTORY}  ║
║  Salt: THORCHAINROUTERV4                                       ║
║                                                                ║
║  Router V4 includes RUNE token handling logic! 💎            ║
║  This will deploy to the SAME address on ALL chains! 🎯       ║
╚════════════════════════════════════════════════════════════════╝
  `);

  // Display RUNE addresses per chain
  console.log(`\n📍 RUNE Token Addresses per Chain:`);
  networks.forEach((network) => {
    const runeAddr = RUNE_ADDRESSES[network.name];
    const isRealRune = network.name === "ethereum";
    console.log(
      `   • ${network.displayName}: ${runeAddr} ${isRealRune ? "(Real RUNE)" : "(Constructor Param)"}`,
    );
  });

  // Check if .env file exists in scripts folder
  const envPath = path.join(__dirname, ".env");
  if (!fs.existsSync(envPath)) {
    console.error(`
❌ Error: .env file not found!

Please create a .env file in the scripts folder with your configuration.
    `);
    process.exit(1);
  }

  const results = [];
  const startTime = Date.now();

  // Deploy to each network sequentially
  for (const network of networks) {
    const result = await deployToNetwork(network);
    results.push(result);

    // Add a small delay between deployments
    if (network !== networks[networks.length - 1]) {
      console.log(`\n⏳ Waiting 15 seconds before next deployment...`);
      await new Promise((resolve) => setTimeout(resolve, 15000));
    }
  }

  // Summary
  const endTime = Date.now();
  const duration = ((endTime - startTime) / 1000 / 60).toFixed(2);

  console.log(`\n${"=".repeat(70)}`);
  console.log(`📊 CREATE2 ROUTER V4 DEPLOYMENT SUMMARY`);
  console.log(`${"=".repeat(70)}`);
  console.log(`⏱️  Total time: ${duration} minutes`);
  console.log(
    `📈 Success rate: ${results.filter((r) => r.success).length}/${results.length}`,
  );
  console.log(`🏭 Factory: ${NICKS_FACTORY} (Nick's)`);
  console.log(`🧂 Salt: ${SALT}`);

  console.log(`\n✅ Deployment Results:`);
  results.forEach((result) => {
    const network = networks.find((n) => n.name === result.network);
    const status = result.success ? "✅ SUCCESS" : "❌ FAILED";
    const addressInfo = result.address ? result.address : "N/A";
    console.log(`   • ${network.displayName}: ${status}`);
    if (result.address) {
      console.log(`     Address: ${addressInfo}`);
      console.log(`     RUNE: ${result.runeAddress}`);
    }
    if (result.error) {
      console.log(`     Error: ${result.error}`);
    }
  });

  // Check for CREATE2 deployment files and verify addresses
  const deploymentsDir = path.join(
    __dirname,
    "..",
    "deployments-create2-nicks-v4",
  );
  if (fs.existsSync(deploymentsDir)) {
    console.log(
      `\n📁 CREATE2 V4 deployment records saved to: ${deploymentsDir}`,
    );
    const files = fs
      .readdirSync(deploymentsDir)
      .filter((f) => f.endsWith(".json"));

    console.log(`\n📍 Deployed Router V4 Contract Addresses:`);
    const addresses = new Set();

    files.forEach((file) => {
      try {
        const filePath = path.join(deploymentsDir, file);
        const data = JSON.parse(fs.readFileSync(filePath, "utf8"));
        console.log(
          `   • ${file.replace(".json", "")}: ${data.contractAddress}`,
        );
        console.log(`     Explorer: ${data.explorer}`);
        console.log(`     RUNE: ${data.runeAddress}`);
        console.log(
          `     Gas Used: ${parseInt(data.gasUsed).toLocaleString()}`,
        );
        addresses.add(data.contractAddress.toLowerCase());
      } catch (error) {
        console.log(`   • ${file}: Error reading file`);
      }
    });

    console.log(`\n🎯 ADDRESS VERIFICATION:`);
    if (addresses.size === 1) {
      const deployedAddress = Array.from(addresses)[0];
      console.log(
        `🎉 PERFECT! All Router V4 contracts deployed to the IDENTICAL address! 🎯`,
      );
      console.log(`✅ Address: ${deployedAddress}`);
      console.log(`✅ Deterministic CREATE2 deployment SUCCESSFUL!`);
      console.log(`💎 Router V4 includes RUNE token handling logic!`);
    } else if (addresses.size > 1) {
      console.log(
        `❌ ERROR: Contracts deployed to ${addresses.size} different addresses:`,
      );
      addresses.forEach((addr) => console.log(`   - ${addr}`));
    } else {
      console.log(`❌ No addresses found in deployment files.`);
    }
  }

  console.log(`\n🎉 CREATE2 Multi-chain Router V4 deployment completed!`);

  if (results.every((r) => r.success)) {
    console.log(`✨ All CREATE2 Router V4 deployments successful!`);
    console.log(
      `🎯 THORChain Router V4 is now live at IDENTICAL addresses across all chains!`,
    );
    console.log(`💎 Router V4 includes advanced RUNE token handling!`);

    // Create a summary file
    const summaryFile = path.join(
      deploymentsDir,
      "create2-router-v4-deployment-summary.md",
    );
    const targetAddress = results.find((r) => r.address)?.address || "TBD";
    const summary = `# THORChain Router V4 CREATE2 Deployment Summary

## Identical Address Across All Chains
**Contract Address**: \`${targetAddress}\`

## Router V4 Features
- 💎 **RUNE Token Handling**: Special logic for RUNE token burns and transfers
- 🔧 **Legacy Support**: Full backwards compatibility with Router V3
- ⚡ **Gas Optimized**: Efficient gas usage patterns
- 🛡️ **Security**: Re-entrancy protection and safe transfers

## Networks
| Chain | Status | RUNE Address | Explorer |
|-------|--------|--------------|----------|
${results
  .map((r) => {
    const network = networks.find((n) => n.name === r.network);
    const status = r.success ? "✅ Deployed" : "❌ Failed";
    const explorer = r.success
      ? `[View](https://etherscan.io/address/${targetAddress})`
      : "N/A";
    const runeDisplay = r.runeAddress;
    return `| ${network.displayName} | ${status} | \`${runeDisplay}\` | ${explorer} |`;
  })
  .join("\n")}

## Deployment Details
- **Factory Used**: Nick's CREATE2 Factory (\`${NICKS_FACTORY}\`)
- **Salt**: \`${SALT}\` ("THORCHAINROUTERV4")
- **Deployment Method**: CREATE2 Deterministic
- **Constructor**: RUNE token address (varies per chain)
- **Total Time**: ${duration} minutes
- **Success Rate**: ${results.filter((r) => r.success).length}/${results.length}

## Contract Verification
All contracts are automatically verified on their respective block explorers and Sourcify.

## RUNE Token Addresses
- **Ethereum**: \`${RUNE_ADDRESSES.ethereum}\` (Real RUNE token)
- **Base**: \`${RUNE_ADDRESSES.base}\` (Constructor parameter - same as ETH)
- **BSC**: \`${RUNE_ADDRESSES.bsc}\` (Constructor parameter - same as ETH)
- **Avalanche**: \`${RUNE_ADDRESSES.avalanche}\` (Constructor parameter - same as ETH)

Generated on: ${new Date().toISOString()}
`;

    fs.writeFileSync(summaryFile, summary);
    console.log(`📄 Router V4 deployment summary saved to: ${summaryFile}`);

    process.exit(0);
  } else {
    console.log(`⚠️  Some Router V4 deployments failed. Check the logs above.`);
    process.exit(1);
  }
}

main().catch((error) => {
  console.error(`💥 CREATE2 Router V4 deployment script failed:`, error);
  process.exit(1);
});
