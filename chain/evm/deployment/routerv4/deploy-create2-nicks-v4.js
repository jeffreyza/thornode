const { ethers, network } = require("hardhat");
const fs = require("fs");
const path = require("path");

// Load environment variables from scripts/.env
require("dotenv").config({ path: path.join(__dirname, ".env") });

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

// Network configurations
const networkConfigs = {
  ethereum: {
    name: "Ethereum Mainnet",
    chainId: 1,
    explorer: "https://etherscan.io",
    hasApiKey: true,
  },
  base: {
    name: "Base Mainnet",
    chainId: 8453,
    explorer: "https://basescan.org",
    hasApiKey: true,
  },
  bsc: {
    name: "BSC Mainnet",
    chainId: 56,
    explorer: "https://bscscan.com",
    hasApiKey: true,
  },
  avalanche: {
    name: "Avalanche C-Chain",
    chainId: 43114,
    explorer: "https://snowtrace.io",
    hasApiKey: true,
  },
};

async function deployWithNicksFactory() {
  const networkName = network.name;
  const config = networkConfigs[networkName];
  const runeAddress = RUNE_ADDRESSES[networkName];

  console.log(
    `\n🚀 CREATE2 Deployment of THORChain Router V4 to ${config?.name || networkName}...`,
  );
  console.log(`📍 Network: ${networkName}`);
  console.log(`🔗 Chain ID: ${config?.chainId || "Unknown"}`);
  console.log(`🏭 Using Nick's Factory: ${NICKS_FACTORY}`);
  console.log(`🧂 Salt: ${SALT}`);
  console.log(`💎 RUNE Address: ${runeAddress}`);

  // Get the deployer account
  const [deployer] = await ethers.getSigners();
  console.log(`💰 Deploying with account: ${deployer.address}`);

  // Get the account balance
  const balance = await ethers.provider.getBalance(deployer.address);
  console.log(`💵 Account balance: ${ethers.formatEther(balance)} ETH`);

  try {
    // Step 1: Get THORChain_RouterV4 contract and constructor args
    console.log(`\n📄 Getting THORChain_RouterV4 bytecode...`);
    const THORChainRouterV4 =
      await ethers.getContractFactory("THORChain_RouterV4");

    // Encode constructor arguments (RUNE address)
    const constructorArgs = [runeAddress];
    const encodedArgs = ethers.AbiCoder.defaultAbiCoder().encode(
      ["address"],
      constructorArgs,
    );
    console.log(`🔧 Constructor args: ${constructorArgs}`);
    console.log(`📦 Encoded args: ${encodedArgs}`);

    // Get bytecode with constructor
    const creationCode = THORChainRouterV4.bytecode + encodedArgs.slice(2);
    const bytecodeHash = ethers.keccak256(creationCode);
    console.log(`📏 Bytecode size: ${(creationCode.length - 2) / 2} bytes`);

    // Step 2: Compute deterministic address
    const deterministicAddress = ethers.getCreate2Address(
      NICKS_FACTORY,
      SALT,
      bytecodeHash,
    );
    console.log(`\n🎯 Deterministic address: ${deterministicAddress}`);
    console.log(`✨ This will be IDENTICAL on all chains! 🎯`);

    // Step 3: Check if contract already exists
    const existingCode = await ethers.provider.getCode(deterministicAddress);
    if (existingCode !== "0x") {
      console.log(`\n⚠️  Contract already exists at ${deterministicAddress}`);
      console.log(`📏 Existing code length: ${existingCode.length} characters`);
      console.log(
        `✅ THORChain Router V4 already deployed to the deterministic address!`,
      );
      return { address: deterministicAddress, alreadyDeployed: true };
    }

    // Step 4: Deploy using Nick's factory
    console.log(
      `\n📦 Deploying THORChain Router V4 via Nick's CREATE2 Factory...`,
    );

    // Nick's factory expects raw calldata: salt (32 bytes) + bytecode
    const deployData = ethers.concat([SALT, creationCode]);

    const tx = await deployer.sendTransaction({
      to: NICKS_FACTORY,
      data: deployData,
      gasLimit: networkName === "avalanche" ? 1500000 : 3000000, // Lower gas limit for AVAX
    });

    console.log(`📤 Transaction sent: ${tx.hash}`);
    console.log(`⏳ Waiting for deployment transaction to be mined...`);

    const receipt = await tx.wait();
    console.log(`✅ Transaction mined in block ${receipt.blockNumber}`);

    // Verify the contract was deployed to the expected address
    const deployedCode = await ethers.provider.getCode(deterministicAddress);
    if (deployedCode === "0x") {
      throw new Error(
        "Contract deployment failed - no code at expected address",
      );
    }

    console.log(`✅ THORChain Router V4 deployed successfully!`);
    console.log(`📍 Contract address: ${deterministicAddress}`);
    console.log(
      `🔍 Explorer: ${config?.explorer}/address/${deterministicAddress}`,
    );

    // Step 5: Save deployment info
    const deploymentInfo = {
      network: networkName,
      chainId: config?.chainId,
      contractName: "THORChain_RouterV4",
      contractAddress: deterministicAddress,
      factoryAddress: NICKS_FACTORY,
      factoryType: "Nicks",
      salt: SALT,
      constructorArgs: constructorArgs,
      runeAddress: runeAddress,
      deployerAddress: deployer.address,
      deploymentTimestamp: new Date().toISOString(),
      transactionHash: tx.hash,
      explorer: `${config?.explorer}/address/${deterministicAddress}`,
      create2: true,
      bytecodeHash: bytecodeHash,
      gasUsed: receipt.gasUsed.toString(),
    };

    // Create deployments directory if it doesn't exist
    const deploymentsDir = path.join(
      __dirname,
      "..",
      "deployments-create2-nicks-v4",
    );
    if (!fs.existsSync(deploymentsDir)) {
      fs.mkdirSync(deploymentsDir, { recursive: true });
    }

    // Save deployment info to file
    const deploymentFile = path.join(deploymentsDir, `${networkName}.json`);
    fs.writeFileSync(deploymentFile, JSON.stringify(deploymentInfo, null, 2));
    console.log(`💾 CREATE2 V4 deployment info saved to: ${deploymentFile}`);

    // Step 6: Verify the contract
    if (config?.hasApiKey) {
      console.log(`\n⏳ Waiting 45 seconds before verification...`);
      await new Promise((resolve) => setTimeout(resolve, 45000));

      console.log(
        `\n🔍 Verifying THORChain Router V4 on ${config?.name || networkName}...`,
      );
      try {
        await hre.run("verify:verify", {
          address: deterministicAddress,
          constructorArguments: constructorArgs,
        });
        console.log(`✅ Contract verified successfully!`);
      } catch (error) {
        if (error.message.includes("Already Verified")) {
          console.log(`✅ Contract is already verified!`);
        } else {
          console.error(`❌ Verification failed:`, error.message);
          console.log(`\n🔧 You can manually verify later using:`);
          console.log(
            `npx hardhat verify --network ${networkName} ${deterministicAddress} "${runeAddress}"`,
          );
        }
      }
    }

    console.log(`\n🎉 CREATE2 Router V4 Deployment Summary:`);
    console.log(`Network: ${config?.name || networkName}`);
    console.log(`Factory: ${NICKS_FACTORY} (Nick's)`);
    console.log(`Contract: ${deterministicAddress}`);
    console.log(`RUNE Address: ${runeAddress}`);
    console.log(`Salt: ${SALT}`);
    console.log(
      `Explorer: ${config?.explorer}/address/${deterministicAddress}`,
    );
    console.log(`Transaction: ${tx.hash}`);
    console.log(`Gas Used: ${receipt.gasUsed.toString()}`);

    console.log(`\n✨ This is the SAME address on ALL chains! 🎯`);

    return {
      address: deterministicAddress,
      transactionHash: tx.hash,
      alreadyDeployed: false,
    };
  } catch (error) {
    console.error(`❌ CREATE2 Router V4 Deployment failed:`, error);
    process.exit(1);
  }
}

async function main() {
  console.log(`
╔════════════════════════════════════════════════════════════════╗
║          THORChain Router V4 CREATE2 Deployment               ║  
║          Using Nick's Factory for Identical Addresses         ║
║                                                                ║
║  Salt: THORCHAINROUTERV4                                       ║
║  Factory: Nick's CREATE2 Factory                               ║
║  Constructor: RUNE Token Address                               ║
╚════════════════════════════════════════════════════════════════╝
  `);

  await deployWithNicksFactory();
}

main()
  .then(() => {
    console.log(`\n✨ CREATE2 Router V4 Deployment completed successfully!`);
    process.exit(0);
  })
  .catch((error) => {
    console.error(`\n💥 CREATE2 Router V4 Deployment failed:`, error);
    process.exit(1);
  });
