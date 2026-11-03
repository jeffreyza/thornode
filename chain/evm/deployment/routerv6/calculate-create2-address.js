const { ethers } = require("hardhat");

// Constants
const SALT =
  "0x54484f5243484149524f55544552563600000000000000000000000000000000"; // "THORCHAINROUTERV6"
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C"; // Well-known CREATE2 factory

async function calculateCreate2Address(factoryAddress, salt, bytecode) {
  const bytecodeHash = ethers.keccak256(bytecode);

  // CREATE2 address calculation: keccak256(0xff + factory + salt + keccak256(bytecode))[12:]
  const create2Address = ethers.getCreate2Address(
    factoryAddress,
    salt,
    bytecodeHash,
  );

  return { create2Address, bytecodeHash };
}

async function main() {
  console.log(`
╔════════════════════════════════════════════════════════════════╗
║                CREATE2 Address Calculator                     ║
║                                                                ║
║  Calculate deterministic addresses for THORChain Router V6    ║
╚════════════════════════════════════════════════════════════════╝
  `);

  try {
    // Get contract bytecode
    console.log(`📄 Getting THORChain_Router bytecode...`);
    const THORChainRouter = await ethers.getContractFactory("THORChain_Router");
    const bytecode = THORChainRouter.bytecode;
    const bytecodeSize = (bytecode.length - 2) / 2; // Remove 0x and divide by 2

    console.log(`✅ Contract compiled successfully`);
    console.log(
      `📏 Bytecode size: ${bytecodeSize} bytes (${(bytecodeSize / 1024).toFixed(2)} KB)`,
    );
    console.log(`🧂 Salt: ${SALT}`);
    console.log(`📝 Salt (decoded): "THORCHAINROUTERV6"`);

    // Calculate addresses for different factory scenarios
    console.log(`\n${"=".repeat(70)}`);
    console.log(`🎯 CREATE2 ADDRESS CALCULATIONS`);
    console.log(`${"=".repeat(70)}`);

    // Scenario 1: Using Nick's well-known factory
    console.log(
      `\n1️⃣  Using Nick's CREATE2 Factory (if available on all chains):`,
    );
    console.log(`   Factory: ${NICKS_FACTORY}`);
    const nicksResult = await calculateCreate2Address(
      NICKS_FACTORY,
      SALT,
      bytecode,
    );
    console.log(
      `   📍 THORChain_Router would deploy to: ${nicksResult.create2Address}`,
    );
    console.log(
      `   ✅ This address would be IDENTICAL on all chains with Nick's factory`,
    );

    // Scenario 2: Using a hypothetical same factory address
    const hypotheticalFactory = "0x1234567890123456789012345678901234567890";
    console.log(
      `\n2️⃣  Using hypothetical factory at same address on all chains:`,
    );
    console.log(`   Factory: ${hypotheticalFactory}`);
    const hypotheticalResult = await calculateCreate2Address(
      hypotheticalFactory,
      SALT,
      bytecode,
    );
    console.log(
      `   📍 THORChain_Router would deploy to: ${hypotheticalResult.create2Address}`,
    );
    console.log(
      `   ✅ This address would be IDENTICAL if factory exists at same address`,
    );

    // Show different addresses with different factories
    const factoryAddresses = [
      "0xaaa0000000000000000000000000000000000000",
      "0xbbb0000000000000000000000000000000000000",
      "0xccc0000000000000000000000000000000000000",
      "0xddd0000000000000000000000000000000000000",
    ];

    console.log(`\n3️⃣  Different factories = Different addresses:`);
    for (let i = 0; i < factoryAddresses.length; i++) {
      const result = await calculateCreate2Address(
        factoryAddresses[i],
        SALT,
        bytecode,
      );
      console.log(
        `   Factory ${factoryAddresses[i]} → ${result.create2Address}`,
      );
    }
    console.log(`   ❌ These would be DIFFERENT addresses on each chain`);

    // Technical details
    console.log(`\n${"=".repeat(70)}`);
    console.log(`🔧 TECHNICAL DETAILS`);
    console.log(`${"=".repeat(70)}`);
    console.log(`Bytecode Hash: ${nicksResult.bytecodeHash}`);
    console.log(`Salt: ${SALT}`);
    console.log(`\nCREATE2 Formula:`);
    console.log(
      `address = keccak256(0xff + factory + salt + keccak256(bytecode))[12:]`,
    );

    // Recommendations
    console.log(`\n${"=".repeat(70)}`);
    console.log(`💡 RECOMMENDATIONS FOR IDENTICAL ADDRESSES`);
    console.log(`${"=".repeat(70)}`);
    console.log(`\n🎯 Option 1: Use Nick's CREATE2 Factory`);
    console.log(`   • Factory: ${NICKS_FACTORY}`);
    console.log(`   • Check if it exists on all your target chains`);
    console.log(`   • If yes, use it directly for identical addresses`);

    console.log(`\n🎯 Option 2: Deploy Factory to Same Address First`);
    console.log(
      `   • Use Nick's factory to deploy your factory to identical addresses`,
    );
    console.log(`   • Then use your factory to deploy THORChain_Router`);
    console.log(`   • Result: Identical addresses across all chains`);

    console.log(`\n🎯 Option 3: Use Keyless CREATE2 (Advanced)`);
    console.log(`   • Deploy factory using specific transaction nonce`);
    console.log(`   • Requires careful nonce management across chains`);

    console.log(`\n🚀 Ready to deploy with CREATE2!`);
    console.log(
      `\nFor identical addresses, ensure the same factory exists at the same address on all chains.`,
    );
  } catch (error) {
    console.error(`❌ Calculation failed:`, error);
    process.exit(1);
  }
}

main()
  .then(() => {
    console.log(`\n✨ Address calculation completed!`);
    process.exit(0);
  })
  .catch((error) => {
    console.error(`💥 Calculation failed:`, error);
    process.exit(1);
  });
