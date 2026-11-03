const { ethers } = require("hardhat");

// RouterV6 Configuration
const ROUTER_ADDRESS = "0xdEC999d968369Ce6C88E41C140F17a2Ce54e9Cb0";

// Token addresses (Ethereum mainnet)
const TOKENS = {
  USDT: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
  USDC: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", // Current USDC address
  WBTC: "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599",
};

// Test amounts - smaller amounts for focused deposit testing
const TEST_AMOUNTS = {
  ETH: ethers.parseEther("0.001"), // Smaller ETH amount
  USDT: ethers.parseUnits("0.1", 6), // 0.1 USDT
  USDC: ethers.parseUnits("0.1", 6), // 0.1 USDC
  WBTC: ethers.parseUnits("0.000001", 8), // 0.000001 WBTC
};

// RouterV6 ABI (key functions only)
const RouterABI = [
  "function depositWithExpiry(address payable vault, address asset, uint amount, string calldata memo, uint expiration) external payable",
  "function transferOut(address payable to, address asset, uint amount, string calldata memo) external payable",
  "function batchTransferOut(address[] calldata to, address[] calldata assets, uint[] calldata amounts, string[] calldata memos) external payable",
  "function transferOutAndCall(address payable target, address finalToken, address to, uint256 amountOutMin, string calldata memo) external payable",
  "function vaultAllowance(address vault, address token) external view returns (uint amount)",
];

// ERC20 ABI (minimal)
const ERC20_ABI = [
  "function approve(address spender, uint256 amount) external returns (bool)",
  "function allowance(address owner, address spender) external view returns (uint256)",
  "function balanceOf(address account) external view returns (uint256)",
  "function transfer(address to, uint256 amount) external returns (bool)",
  "function transferFrom(address from, address to, uint256 amount) external returns (bool)",
];

let deployer, router, tokens, testResults;

// Helper function to add delay between transactions
async function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function setupContracts() {
  console.log("🔗 Setting up contracts and connections...");

  [deployer] = await ethers.getSigners();
  console.log(`👤 Test account: ${deployer.address}`);

  // Connect to router
  router = new ethers.Contract(ROUTER_ADDRESS, RouterABI, deployer);
  console.log(`🤖 RouterV6 connected: ${ROUTER_ADDRESS}`);

  // Connect to tokens
  tokens = {};
  for (const [symbol, address] of Object.entries(TOKENS)) {
    tokens[symbol] = new ethers.Contract(address, ERC20_ABI, deployer);
    console.log(`🪙 ${symbol} connected: ${address}`);
  }

  console.log("✅ All contracts connected");
}

async function checkBalances() {
  console.log("\n💰 Checking account balances...");

  const ethBalance = await ethers.provider.getBalance(deployer.address);
  console.log(`ETH: ${ethers.formatEther(ethBalance)}`);

  for (const [symbol, token] of Object.entries(tokens)) {
    const balance = await token.balanceOf(deployer.address);
    const decimals = symbol === "WBTC" ? 8 : 6;
    console.log(`${symbol}: ${ethers.formatUnits(balance, decimals)}`);
  }
}

// Helper function for safe token approval (handles USDT's non-standard behavior)
async function safeApprove(token, symbol, spender, amount) {
  console.log(`   🔄 Approving ${symbol}...`);

  // USDT requires allowance to be 0 before setting a new value
  if (symbol === "USDT") {
    const currentAllowance = await token.allowance(deployer.address, spender);
    if (currentAllowance > 0) {
      console.log(`   🔄 Resetting ${symbol} allowance to 0 first...`);
      const resetTx = await token.approve(spender, 0);
      await resetTx.wait();
      console.log(`   ✅ ${symbol} allowance reset: ${resetTx.hash}`);
    }
  }

  const approveTx = await token.approve(spender, amount);
  await approveTx.wait();
  console.log(`   ✅ ${symbol} approved: ${approveTx.hash}`);
  await delay(2000); // Wait 2 seconds between transactions
  return approveTx;
}

// Test 1: Basic ETH deposit
async function testETHDeposit() {
  console.log("\n🧪 Test 1: Basic ETH Deposit");
  console.log("================================");

  try {
    const expiration = 0; // No expiration
    const tx = await router.depositWithExpiry(
      deployer.address, // vault
      ethers.ZeroAddress, // ETH
      TEST_AMOUNTS.ETH, // amount must match msg.value for ETH
      "ETH-DEPOSIT-TEST",
      expiration,
      { value: TEST_AMOUNTS.ETH },
    );

    console.log(`✅ ETH deposit transaction sent: ${tx.hash}`);
    console.log(`🔗 View on Etherscan: https://etherscan.io/tx/${tx.hash}`);

    try {
      const receipt = await tx.wait();
      console.log(`   ✅ Confirmed in block: ${receipt.blockNumber}`);
      console.log(`   Gas used: ${receipt.gasUsed.toString()}`);

      testResults.deposits.push({
        asset: "ETH",
        amount: ethers.formatEther(TEST_AMOUNTS.ETH),
        txHash: tx.hash,
        gasUsed: receipt.gasUsed.toString(),
        status: "success",
      });
    } catch (waitError) {
      console.log(
        `   ⚠️ Receipt wait failed (RPC issue): ${waitError.message}`,
      );
      console.log(`   Transaction may still be successful - check Etherscan`);

      testResults.deposits.push({
        asset: "ETH",
        amount: ethers.formatEther(TEST_AMOUNTS.ETH),
        txHash: tx.hash,
        status: "sent_rpc_issue",
        note: "Transaction sent but RPC indexing issue",
      });
    }

    await delay(3000); // Wait 3 seconds after transaction
  } catch (error) {
    console.log(`❌ ETH deposit failed: ${error.message}`);
    testResults.deposits.push({
      asset: "ETH",
      amount: ethers.formatEther(TEST_AMOUNTS.ETH),
      status: "failed",
      error: error.message,
    });
  }
}

// Test 2: Token deposits
async function testTokenDeposits() {
  console.log("\n🧪 Test: Token Deposits (WBTC, USDC, USDT)");
  console.log("===============================================");

  for (const [symbol, token] of Object.entries(tokens)) {
    try {
      console.log(`\n📤 Depositing ${symbol}...`);

      // Check current balance
      const balance = await token.balanceOf(deployer.address);
      const decimals = symbol === "WBTC" ? 8 : 6;
      console.log(
        `   💰 Current ${symbol} balance: ${ethers.formatUnits(balance, decimals)}`,
      );

      if (balance < TEST_AMOUNTS[symbol]) {
        console.log(`   ⚠️  Insufficient ${symbol} balance for test`);
        testResults.deposits.push({
          asset: symbol,
          amount: ethers.formatUnits(TEST_AMOUNTS[symbol], decimals),
          status: "skipped",
          error: "Insufficient balance",
        });
        continue;
      }

      // Approve token first
      await safeApprove(token, symbol, ROUTER_ADDRESS, TEST_AMOUNTS[symbol]);

      // Deposit token
      const expiration = 0; // No expiration
      const tx = await router.depositWithExpiry(
        deployer.address, // vault
        token.target, // token address
        TEST_AMOUNTS[symbol],
        `${symbol}-DEPOSIT-TEST-${Date.now()}`,
        expiration,
        { gasLimit: 300000 }, // Explicit gas limit
      );

      console.log(`   ✅ ${symbol} deposit transaction sent: ${tx.hash}`);
      console.log(
        `   🔗 View on Etherscan: https://etherscan.io/tx/${tx.hash}`,
      );

      try {
        const receipt = await tx.wait();
        console.log(`   ✅ Confirmed in block: ${receipt.blockNumber}`);
        console.log(`   Gas used: ${receipt.gasUsed.toString()}`);

        testResults.deposits.push({
          asset: symbol,
          amount: ethers.formatUnits(TEST_AMOUNTS[symbol], decimals),
          txHash: tx.hash,
          gasUsed: receipt.gasUsed.toString(),
          status: "success",
        });
      } catch (waitError) {
        console.log(
          `   ⚠️ Receipt wait failed (RPC issue): ${waitError.message}`,
        );
        console.log(`   Transaction may still be successful - check Etherscan`);

        testResults.deposits.push({
          asset: symbol,
          amount: ethers.formatUnits(TEST_AMOUNTS[symbol], decimals),
          txHash: tx.hash,
          status: "sent_rpc_issue",
          note: "Transaction sent but RPC indexing issue",
        });
      }

      await delay(3000); // Wait between deposits
    } catch (error) {
      console.log(`   ❌ ${symbol} deposit failed: ${error.message}`);
      const decimals = symbol === "WBTC" ? 8 : 6;
      testResults.deposits.push({
        asset: symbol,
        amount: ethers.formatUnits(TEST_AMOUNTS[symbol], decimals),
        status: "failed",
        error: error.message,
      });
    }
  }
}

// Test 3: Basic ETH transfer
async function testETHTransfer() {
  console.log("\n🧪 Test 3: Basic ETH Transfer");
  console.log("==============================");

  try {
    const tx = await router.transferOut(
      deployer.address, // to (self)
      ethers.ZeroAddress, // ETH
      TEST_AMOUNTS.ETH,
      "ETH-TRANSFEROUT-TEST",
      { value: TEST_AMOUNTS.ETH },
    );

    const receipt = await tx.wait();
    console.log(`✅ ETH transfer successful: ${tx.hash}`);
    console.log(`   Gas used: ${receipt.gasUsed.toString()}`);

    testResults.transfers.push({
      asset: "ETH",
      amount: ethers.formatEther(TEST_AMOUNTS.ETH),
      txHash: tx.hash,
      gasUsed: receipt.gasUsed.toString(),
      status: "success",
    });
  } catch (error) {
    console.log(`❌ ETH transfer failed: ${error.message}`);
    testResults.transfers.push({
      asset: "ETH",
      amount: ethers.formatEther(TEST_AMOUNTS.ETH),
      status: "failed",
      error: error.message,
    });
  }
}

// Test 4: Token transfers
async function testTokenTransfers() {
  console.log("\n🧪 Test 4: Token Transfers");
  console.log("===========================");

  for (const [symbol, token] of Object.entries(tokens)) {
    try {
      console.log(`\n📤 Transferring ${symbol}...`);

      // Approve router to spend tokens (for transferOut, the vault/deployer needs to approve)
      await safeApprove(token, symbol, ROUTER_ADDRESS, TEST_AMOUNTS[symbol]);

      const tx = await router.transferOut(
        deployer.address, // to (self)
        token.target, // token address
        TEST_AMOUNTS[symbol],
        `${symbol}-TRANSFEROUT-TEST`,
      );

      const receipt = await tx.wait();
      console.log(`   ✅ ${symbol} transfer successful: ${tx.hash}`);
      console.log(`   Gas used: ${receipt.gasUsed.toString()}`);

      const decimals = symbol === "WBTC" ? 8 : 6;
      testResults.transfers.push({
        asset: symbol,
        amount: ethers.formatUnits(TEST_AMOUNTS[symbol], decimals),
        txHash: tx.hash,
        gasUsed: receipt.gasUsed.toString(),
        status: "success",
      });
    } catch (error) {
      console.log(`   ❌ ${symbol} transfer failed: ${error.message}`);
      const decimals = symbol === "WBTC" ? 8 : 6;
      testResults.transfers.push({
        asset: symbol,
        amount: ethers.formatUnits(TEST_AMOUNTS[symbol], decimals),
        status: "failed",
        error: error.message,
      });
    }
  }
}

// Test 5: Mixed batch transfer (ETH + tokens)
async function testMixedBatchTransfer() {
  console.log("\n🧪 Test 5: Mixed Batch Transfer");
  console.log("================================");

  try {
    // Approve all tokens first
    for (const [symbol, token] of Object.entries(tokens)) {
      if (symbol === "USDT" || symbol === "USDC") {
        // Skip WBTC for this test
        await safeApprove(token, symbol, ROUTER_ADDRESS, TEST_AMOUNTS[symbol]);
      }
    }

    const recipients = [deployer.address, deployer.address, deployer.address];
    const assets = [
      ethers.ZeroAddress, // ETH
      tokens.USDC.target,
      tokens.USDT.target,
    ];
    const amounts = [TEST_AMOUNTS.ETH, TEST_AMOUNTS.USDC, TEST_AMOUNTS.USDT];
    const memos = ["BATCH-ETH", "BATCH-USDC", "BATCH-USDT"];

    const tx = await router.batchTransferOut(
      recipients,
      assets,
      amounts,
      memos,
      { value: TEST_AMOUNTS.ETH },
    );

    const receipt = await tx.wait();
    console.log(`✅ Mixed batch transfer successful: ${tx.hash}`);
    console.log(`   Gas used: ${receipt.gasUsed.toString()}`);
    console.log(`   Gas per transfer: ${Math.floor(receipt.gasUsed / 3)}`);

    testResults.batches.push({
      type: "Mixed (ETH + USDC + USDT)",
      transfers: 3,
      txHash: tx.hash,
      gasUsed: receipt.gasUsed.toString(),
      gasPerTransfer: Math.floor(receipt.gasUsed / 3),
      status: "success",
    });
  } catch (error) {
    console.log(`❌ Mixed batch transfer failed: ${error.message}`);
    testResults.batches.push({
      type: "Mixed (ETH + USDC + USDT)",
      transfers: 3,
      status: "failed",
      error: error.message,
    });
  }
}

// Test 6: Large batch transfer (10x USDC)
async function testLargeBatchTransfer() {
  console.log("\n🧪 Test 6: Large Batch Transfer (10x USDC)");
  console.log("============================================");

  try {
    // Approve large amount of USDC
    const largeAmount = TEST_AMOUNTS.USDC * 10n;
    await safeApprove(tokens.USDC, "USDC", ROUTER_ADDRESS, largeAmount);

    const recipients = Array(10).fill(deployer.address);
    const assets = Array(10).fill(tokens.USDC.target);
    const amounts = Array(10).fill(TEST_AMOUNTS.USDC);
    const memos = Array(10)
      .fill(0)
      .map((_, i) => `BATCH-USDC-${i + 1}`);

    const tx = await router.batchTransferOut(
      recipients,
      assets,
      amounts,
      memos,
    );

    const receipt = await tx.wait();
    console.log(`✅ Large batch transfer successful: ${tx.hash}`);
    console.log(`   Gas used: ${receipt.gasUsed.toString()}`);
    console.log(`   Gas per transfer: ${Math.floor(receipt.gasUsed / 10)}`);

    testResults.batches.push({
      type: "Large (10x USDC)",
      transfers: 10,
      txHash: tx.hash,
      gasUsed: receipt.gasUsed.toString(),
      gasPerTransfer: Math.floor(receipt.gasUsed / 10),
      status: "success",
    });
  } catch (error) {
    console.log(`❌ Large batch transfer failed: ${error.message}`);
    testResults.batches.push({
      type: "Large (10x USDC)",
      transfers: 10,
      status: "failed",
      error: error.message,
    });
  }
}

// Test 7: Edge cases
async function testEdgeCases() {
  console.log("\n🧪 Test 7: Edge Cases");
  console.log("======================");

  // Empty batch
  try {
    console.log("\n📤 Testing empty batch...");
    const tx = await router.batchTransferOut([], [], [], [], {
      gasLimit: 100000,
    });
    const receipt = await tx.wait();
    console.log(`✅ Empty batch successful: ${tx.hash}`);
    testResults.edgeCases.push({
      test: "Empty batch",
      txHash: tx.hash,
      status: "success",
    });
  } catch (error) {
    console.log(`❌ Empty batch failed: ${error.message}`);
    testResults.edgeCases.push({
      test: "Empty batch",
      status: "failed",
      error: error.message,
    });
  }

  // Zero amount transfers
  try {
    console.log("\n📤 Testing zero ETH transfer...");
    const tx = await router.transferOut(
      deployer.address,
      ethers.ZeroAddress,
      0,
      "ZERO-ETH-TEST",
    );
    const receipt = await tx.wait();
    console.log(`✅ Zero ETH transfer successful: ${tx.hash}`);
    testResults.edgeCases.push({
      test: "Zero ETH transfer",
      txHash: tx.hash,
      status: "success",
    });
  } catch (error) {
    console.log(`❌ Zero ETH transfer failed: ${error.message}`);
    testResults.edgeCases.push({
      test: "Zero ETH transfer",
      status: "failed",
      error: error.message,
    });
  }

  // Excess ETH handling
  try {
    console.log("\n📤 Testing excess ETH handling...");
    const requestedAmount = ethers.parseEther("0.001");
    const sentAmount = ethers.parseEther("0.002");

    const tx = await router.transferOut(
      deployer.address,
      ethers.ZeroAddress,
      requestedAmount,
      "EXCESS-ETH-TEST",
      { value: sentAmount },
    );
    const receipt = await tx.wait();
    console.log(`✅ Excess ETH handling successful: ${tx.hash}`);
    console.log(
      `   Sent: ${ethers.formatEther(sentAmount)} ETH, Requested: ${ethers.formatEther(requestedAmount)} ETH`,
    );
    testResults.edgeCases.push({
      test: "Excess ETH handling",
      txHash: tx.hash,
      status: "success",
    });
  } catch (error) {
    console.log(`❌ Excess ETH handling failed: ${error.message}`);
    testResults.edgeCases.push({
      test: "Excess ETH handling",
      status: "failed",
      error: error.message,
    });
  }
}

async function printTestSummary() {
  console.log("\n" + "=".repeat(60));
  console.log("🏆 ROUTERV6 MAINNET TEST SUMMARY");
  console.log("=".repeat(60));

  console.log(`\n📊 DEPOSITS (${testResults.deposits.length} tests):`);
  testResults.deposits.forEach((test) => {
    const status = test.status === "success" ? "✅" : "❌";
    console.log(
      `   ${status} ${test.asset}: ${test.amount} ${test.txHash ? `(${test.txHash})` : ""}`,
    );
  });

  console.log(`\n📊 TRANSFERS (${testResults.transfers.length} tests):`);
  testResults.transfers.forEach((test) => {
    const status = test.status === "success" ? "✅" : "❌";
    console.log(
      `   ${status} ${test.asset}: ${test.amount} ${test.txHash ? `(${test.txHash})` : ""}`,
    );
  });

  console.log(`\n📊 BATCH TRANSFERS (${testResults.batches.length} tests):`);
  testResults.batches.forEach((test) => {
    const status = test.status === "success" ? "✅" : "❌";
    console.log(
      `   ${status} ${test.type}: ${test.transfers} transfers ${test.txHash ? `(${test.txHash})` : ""}`,
    );
    if (test.gasPerTransfer) {
      console.log(`      Gas per transfer: ${test.gasPerTransfer}`);
    }
  });

  console.log(`\n📊 EDGE CASES (${testResults.edgeCases.length} tests):`);
  testResults.edgeCases.forEach((test) => {
    const status = test.status === "success" ? "✅" : "❌";
    console.log(
      `   ${status} ${test.test} ${test.txHash ? `(${test.txHash})` : ""}`,
    );
  });

  // Summary stats
  const allTests = [
    ...testResults.deposits,
    ...testResults.transfers,
    ...testResults.batches,
    ...testResults.edgeCases,
  ];
  const successCount = allTests.filter((t) => t.status === "success").length;
  const totalCount = allTests.length;

  console.log(`\n🎯 OVERALL RESULTS:`);
  console.log(`   Total Tests: ${totalCount}`);
  console.log(
    `   Successful: ${successCount} (${Math.round((successCount / totalCount) * 100)}%)`,
  );
  console.log(`   Failed: ${totalCount - successCount}`);

  console.log(`\n🔗 RouterV6 Contract: ${ROUTER_ADDRESS}`);
  console.log(
    `🌐 View on Etherscan: https://etherscan.io/address/${ROUTER_ADDRESS}`,
  );
}

async function runMainnetTests() {
  console.log("🚀 RouterV6 Mainnet Deposit Function Tests");
  console.log("===========================================");
  console.log("🎯 Focus: Testing WBTC, USDC, USDT deposits");
  console.log(`🔗 Router: ${ROUTER_ADDRESS}`);

  // Initialize test results
  testResults = {
    deposits: [],
    transfers: [],
    batches: [],
    edgeCases: [],
  };

  try {
    await setupContracts();
    await checkBalances();

    // Focus on deposit tests only
    console.log("\n" + "=".repeat(50));
    console.log("🧪 FOCUSED DEPOSIT TESTS");
    console.log("=".repeat(50));

    await testTokenDeposits(); // Test WBTC, USDC, USDT deposits
    await testETHDeposit(); // Also test ETH deposit

    await printTestSummary();

    console.log("\n🎉 Deposit tests completed!");
  } catch (error) {
    console.error("❌ Test suite failed:", error);
    process.exit(1);
  }
}

// Run tests if called directly
if (require.main === module) {
  runMainnetTests()
    .then(() => process.exit(0))
    .catch((error) => {
      console.error("❌ Test execution failed:", error);
      process.exit(1);
    });
}

module.exports = { runMainnetTests };
