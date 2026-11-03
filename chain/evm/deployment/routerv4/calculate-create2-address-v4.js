const { ethers } = require("hardhat");

// Nick's CREATE2 Factory (exists on all major chains)
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";
const SALT =
  "0x54484f5243484149524f55544552563400000000000000000000000000000000"; // "THORCHAINROUTERV4"

// RUNE token addresses on each chain
const RUNE_ADDRESSES = {
  ethereum: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // ETH RUNE
  base: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // Use same RUNE address for identical CREATE2
  bsc: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // Use same RUNE address for identical CREATE2
  avalanche: "0x3155BA85D5F96b2d030a4966AF206230e46849cb", // Use same RUNE address for identical CREATE2
};

const networks = [
  { name: "ethereum", displayName: "Ethereum Mainnet", chainId: 1 },
  { name: "base", displayName: "Base Mainnet", chainId: 8453 },
  { name: "bsc", displayName: "BSC Mainnet", chainId: 56 },
  { name: "avalanche", displayName: "Avalanche C-Chain", chainId: 43114 },
];

async function calculateCreate2AddressV4() {
  console.log(`
╔════════════════════════════════════════════════════════════════╗
║         THORChain Router V4 CREATE2 Address Calculator        ║
║                                                                ║
║  This calculates the deterministic address for Router V4      ║
║  using Nick's CREATE2 Factory with constructor parameters     ║
╚════════════════════════════════════════════════════════════════╝
  `);

  console.log(`🏭 Nick's Factory: ${NICKS_FACTORY}`);
  console.log(`🧂 Salt: ${SALT} (THORCHAINROUTERV4)`);

  try {
    // Get the contract factory for Router V4
    const THORChainRouterV4 =
      await ethers.getContractFactory("THORChain_RouterV4");

    console.log(`\n📍 Router V4 CREATE2 Addresses per Network:`);
    console.log(`${"=".repeat(75)}`);

    const addresses = new Set();

    for (const network of networks) {
      const runeAddress = RUNE_ADDRESSES[network.name];

      // Encode constructor arguments (RUNE address)
      const constructorArgs = [runeAddress];
      const encodedArgs = ethers.AbiCoder.defaultAbiCoder().encode(
        ["address"],
        constructorArgs,
      );

      // Get bytecode with constructor
      const creationCode = THORChainRouterV4.bytecode + encodedArgs.slice(2);
      const bytecodeHash = ethers.keccak256(creationCode);

      // Calculate deterministic address
      const deterministicAddress = ethers.getCreate2Address(
        NICKS_FACTORY,
        SALT,
        bytecodeHash,
      );

      console.log(`\n🌐 ${network.displayName} (Chain ID: ${network.chainId})`);
      console.log(`   💎 RUNE Address: ${runeAddress}`);
      console.log(`   📦 Creation Code Hash: ${bytecodeHash}`);
      console.log(`   🎯 Contract Address: ${deterministicAddress}`);

      addresses.add(deterministicAddress.toLowerCase());
    }

    console.log(`\n${"=".repeat(75)}`);
    console.log(`🎯 ADDRESS VERIFICATION:`);

    if (addresses.size === 1) {
      const commonAddress = Array.from(addresses)[0];
      console.log(
        `🎉 PERFECT! All networks will have the IDENTICAL address! 🎯`,
      );
      console.log(`✅ Router V4 Address: ${commonAddress}`);
      console.log(`💎 This includes RUNE token handling logic!`);
    } else {
      console.log(
        `❌ ERROR: Different addresses calculated for different networks:`,
      );
      let i = 0;
      for (const network of networks) {
        console.log(
          `   ${network.displayName}: ${Array.from(addresses)[i] || "ERROR"}`,
        );
        i++;
      }
      console.log(
        `\n⚠️  This means CREATE2 deployment would result in DIFFERENT addresses!`,
      );
      console.log(`🔧 Check RUNE addresses and constructor encoding logic.`);
    }

    console.log(`\n📋 Deployment Summary:`);
    console.log(`• Factory: Nick's CREATE2 Factory`);
    console.log(`• Salt: THORCHAINROUTERV4`);
    console.log(`• Constructor: RUNE token address (varies per chain)`);
    console.log(`• Contract: THORChain_RouterV4.sol`);
    console.log(`• Features: RUNE token burns, legacy compatibility`);

    return Array.from(addresses)[0];
  } catch (error) {
    console.error(`❌ Failed to calculate CREATE2 address:`, error);
    process.exit(1);
  }
}

async function main() {
  const address = await calculateCreate2AddressV4();

  console.log(`\n✨ Router V4 CREATE2 address calculation completed!`);
  console.log(`🎯 Deploy to this address: ${address}`);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(`💥 Address calculation failed:`, error);
    process.exit(1);
  });
