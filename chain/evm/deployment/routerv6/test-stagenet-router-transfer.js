const { ethers } = require("hardhat");

async function testStagenetRouterTransfer() {
  console.log("🚀 Testing Stagenet RouterV6.1 transferOut() to Gnosis Safe");
  console.log("========================================================");

  // Configuration
  const ROUTER_ADDRESS = "0x0DC6108C9225Ce93Da589B4CE83c104b34693117"; // Stagenet router
  const GNOSIS_SAFE = "0xF1fC3B8C5316DEA698Fce1A1835F2Af3b354594F"; // Target Gnosis Safe
  const TRANSFER_AMOUNT = ethers.parseEther("0.0001"); // 0.0001 ETH

  try {
    // Get signer
    const [deployer] = await ethers.getSigners();
    console.log(`📝 Using account: ${deployer.address}`);

    // Check balance before transfer
    const balanceBefore = await ethers.provider.getBalance(deployer.address);
    console.log(`💰 Account balance: ${ethers.formatEther(balanceBefore)} ETH`);

    // Get router contract instance
    const router = await ethers.getContractAt(
      "THORChain_Router",
      ROUTER_ADDRESS,
    );
    console.log(`🔗 Router contract: ${ROUTER_ADDRESS}`);
    console.log(`🎯 Target Gnosis Safe: ${GNOSIS_SAFE}`);
    console.log(
      `💸 Transfer amount: ${ethers.formatEther(TRANSFER_AMOUNT)} ETH`,
    );

    // Check Gnosis Safe balance before
    const safeBalanceBefore = await ethers.provider.getBalance(GNOSIS_SAFE);
    console.log(
      `🔍 Gnosis Safe balance before: ${ethers.formatEther(safeBalanceBefore)} ETH`,
    );

    // Prepare transfer parameters
    const to = GNOSIS_SAFE;
    const asset = ethers.ZeroAddress; // ETH
    const amount = TRANSFER_AMOUNT;
    const memo = "Test transfer from RouterV6.1 Stagenet";

    console.log(`\n🔨 Executing transferOut()...`);
    console.log(`   to: ${to}`);
    console.log(`   asset: ${asset} (ETH)`);
    console.log(`   amount: ${amount.toString()} wei`);
    console.log(`   memo: ${memo}`);

    // Execute transfer with ETH value
    const tx = await router.transferOut(to, asset, amount, memo, {
      value: TRANSFER_AMOUNT, // Send ETH as msg.value
    });
    console.log(`📤 Transaction sent: ${tx.hash}`);

    // Wait for confirmation
    console.log(`⏳ Waiting for confirmation...`);
    const receipt = await tx.wait();
    console.log(`✅ Transaction confirmed in block ${receipt.blockNumber}`);

    // Check gas usage
    console.log(`⛽ Gas used: ${receipt.gasUsed.toString()}`);
    console.log(`💵 Gas price: ${receipt.gasPrice.toString()} wei`);
    const gasCost = receipt.gasUsed * receipt.gasPrice;
    console.log(`💰 Gas cost: ${ethers.formatEther(gasCost)} ETH`);
    console.log(
      `💸 Total transaction cost: ${ethers.formatEther(gasCost + TRANSFER_AMOUNT)} ETH (gas + transfer)`,
    );

    // Check balances after transfer
    const balanceAfter = await ethers.provider.getBalance(deployer.address);
    const safeBalanceAfter = await ethers.provider.getBalance(GNOSIS_SAFE);

    console.log(`\n📊 Balance Summary:`);
    console.log(
      `   Deployer balance before: ${ethers.formatEther(balanceBefore)} ETH`,
    );
    console.log(
      `   Deployer balance after:  ${ethers.formatEther(balanceAfter)} ETH`,
    );
    console.log(
      `   Balance change: ${ethers.formatEther(balanceAfter - balanceBefore)} ETH`,
    );

    console.log(
      `   Gnosis Safe balance before: ${ethers.formatEther(safeBalanceBefore)} ETH`,
    );
    console.log(
      `   Gnosis Safe balance after:  ${ethers.formatEther(safeBalanceAfter)} ETH`,
    );
    console.log(
      `   Safe balance change: ${ethers.formatEther(safeBalanceAfter - safeBalanceBefore)} ETH`,
    );

    // Verify the transfer worked
    const expectedSafeBalance = safeBalanceBefore + TRANSFER_AMOUNT;
    if (safeBalanceAfter >= expectedSafeBalance) {
      console.log(`\n✅ SUCCESS: Transfer completed successfully!`);
      console.log(
        `   Expected Gnosis Safe balance: ${ethers.formatEther(expectedSafeBalance)} ETH`,
      );
      console.log(
        `   Actual Gnosis Safe balance:   ${ethers.formatEther(safeBalanceAfter)} ETH`,
      );
    } else {
      console.log(`\n⚠️  WARNING: Transfer may not have completed as expected`);
      console.log(
        `   Expected Gnosis Safe balance: ${ethers.formatEther(expectedSafeBalance)} ETH`,
      );
      console.log(
        `   Actual Gnosis Safe balance:   ${ethers.formatEther(safeBalanceAfter)} ETH`,
      );
    }

    // Final summary
    console.log(`\n🏆 Test Summary:`);
    console.log(`   Network: ${hre.network.name}`);
    console.log(`   Router: ${ROUTER_ADDRESS}`);
    console.log(`   Target: ${GNOSIS_SAFE}`);
    console.log(`   Amount: ${ethers.formatEther(TRANSFER_AMOUNT)} ETH`);
    console.log(`   Gas Used: ${receipt.gasUsed.toString()}`);
    console.log(`   Transaction: ${tx.hash}`);
    console.log(`   Block: ${receipt.blockNumber}`);
  } catch (error) {
    console.error("❌ Test failed:", error);
    process.exit(1);
  }
}

// Run the test
if (require.main === module) {
  testStagenetRouterTransfer()
    .then(() => {
      console.log("\n🎉 Test completed successfully!");
      process.exit(0);
    })
    .catch((error) => {
      console.error("❌ Test failed:", error);
      process.exit(1);
    });
}

module.exports = { testStagenetRouterTransfer };
