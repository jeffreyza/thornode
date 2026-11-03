const { ethers } = require("hardhat");

// Configuration
const ROUTER_CONTRACT_NAME = "THORChain_Router";
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";

// Use environment variables if provided, otherwise use defaults
const SALT =
  process.env.CUSTOM_SALT ||
  "0x00000000000000015aab67962d1882520000000000000000000000000016079c";
const EXPECTED_ADDRESS =
  process.env.EXPECTED_ADDRESS || "0x0DC6108C9225Ce93Da589B4CE83c104b34693117";
const PATTERN_NAME = process.env.PATTERN_NAME || "0x0DC610";

async function deployRouterV6SingleChain() {
  const networkName = hre.network.name;
  console.log(
    `\n🚀 Deploying THORChain RouterV6 to ${networkName.toUpperCase()}`,
  );
  console.log("=====================================");

  // Get deployer
  const [deployer] = await ethers.getSigners();
  console.log(`📝 Deploying from account: ${deployer.address}`);

  // Check balance
  const balance = await ethers.provider.getBalance(deployer.address);
  console.log(`💰 Account balance: ${ethers.formatEther(balance)} ETH`);

  // Get contract factory and bytecode
  const RouterFactory = await ethers.getContractFactory(ROUTER_CONTRACT_NAME);
  const bytecode = RouterFactory.bytecode;

  console.log(`📄 Contract: ${ROUTER_CONTRACT_NAME}`);
  console.log(`🏭 Factory: ${NICKS_FACTORY}`);
  console.log(`🧂 Salt: ${SALT}`);

  // Calculate deterministic address
  const expectedAddress = ethers.getCreate2Address(
    NICKS_FACTORY,
    SALT,
    ethers.keccak256(bytecode),
  );
  console.log(`🎯 Expected address: ${EXPECTED_ADDRESS}`);

  // Check if contract already exists
  const existingCode = await ethers.provider.getCode(EXPECTED_ADDRESS);
  if (existingCode !== "0x") {
    console.log(`✅ Contract already deployed at ${EXPECTED_ADDRESS}`);
    console.log(`🔍 Verifying existing contract...`);

    try {
      await hre.run("verify:verify", {
        address: EXPECTED_ADDRESS,
        constructorArguments: [],
        contract: `contracts/THORChain_RouterV6.sol:${ROUTER_CONTRACT_NAME}`,
      });
      console.log(`✅ Contract verification successful!`);
    } catch (error) {
      if (error.message.includes("Already Verified")) {
        console.log(`✅ Contract already verified!`);
      } else {
        console.log(`⚠️  Verification failed: ${error.message}`);
      }
    }
    return EXPECTED_ADDRESS;
  }

  // Check if Nick's factory exists
  const factoryCode = await ethers.provider.getCode(NICKS_FACTORY);
  if (factoryCode === "0x") {
    throw new Error(
      `❌ Nick's CREATE2 factory not found at ${NICKS_FACTORY} on ${networkName}`,
    );
  }
  console.log(`✅ Nick's CREATE2 factory found`);

  console.log(`\n🔨 Deploying contract...`);

  // Nick's factory expects raw calldata: salt (32 bytes) + bytecode
  const deployData = ethers.concat([SALT, bytecode]);

  // Estimate gas
  const gasEstimate = await deployer.estimateGas({
    to: NICKS_FACTORY,
    data: deployData,
  });
  console.log(`⛽ Estimated gas: ${gasEstimate.toString()}`);

  // Send deployment transaction
  const tx = await deployer.sendTransaction({
    to: NICKS_FACTORY,
    data: deployData,
    gasLimit: 2000000, // High gas limit for contract deployment
  });

  console.log(`📤 Transaction sent: ${tx.hash}`);
  console.log(`⏳ Waiting for confirmation...`);

  const receipt = await tx.wait();
  console.log(`✅ Transaction confirmed in block ${receipt.blockNumber}`);
  console.log(`⛽ Gas used: ${receipt.gasUsed.toString()}`);

  // Verify the contract was deployed to the expected address
  const deployedCode = await ethers.provider.getCode(EXPECTED_ADDRESS);
  if (deployedCode === "0x") {
    throw new Error(
      `❌ Contract deployment failed - no code at expected address ${EXPECTED_ADDRESS}`,
    );
  }

  console.log(`🎉 RouterV6 deployed successfully!`);
  console.log(`📍 Contract address: ${EXPECTED_ADDRESS}`);

  // Verify contract on block explorer
  console.log(`\n🔍 Verifying contract on block explorer...`);
  try {
    await hre.run("verify:verify", {
      address: EXPECTED_ADDRESS,
      constructorArguments: [],
      contract: `contracts/THORChain_RouterV6.sol:${ROUTER_CONTRACT_NAME}`,
    });
    console.log(`✅ Contract verification successful!`);
  } catch (error) {
    if (error.message.includes("Already Verified")) {
      console.log(`✅ Contract already verified!`);
    } else {
      console.log(`⚠️  Verification failed: ${error.message}`);
      console.log(`📝 Manual verification may be required`);
    }
  }

  console.log(`\n🏆 Deployment Summary:`);
  console.log(`   Network: ${networkName}`);
  console.log(`   Address: ${EXPECTED_ADDRESS}`);
  console.log(`   Gas Used: ${receipt.gasUsed.toString()}`);
  console.log(`   Block: ${receipt.blockNumber}`);
  console.log(`   Tx Hash: ${tx.hash}`);

  return EXPECTED_ADDRESS;
}

// Run deployment
if (require.main === module) {
  deployRouterV6SingleChain()
    .then(() => process.exit(0))
    .catch((error) => {
      console.error("❌ Deployment failed:", error);
      process.exit(1);
    });
}

module.exports = { deployRouterV6SingleChain };
