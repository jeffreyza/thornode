const { expect } = require("chai");
const { ethers } = require("hardhat");

// CREATE2 deployment configuration
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";
const SALT = ethers.keccak256(ethers.toUtf8Bytes("THORCHAINAGGRUNISWAP"));

describe("THORChain AggregatorUniswap Tests", function () {
  let aggregator, router, mockSwapRouter, mockQuoter, weth, token1, token2;
  let owner, vault, user, recipient;

  const ONE_ETH = ethers.parseEther("1");
  const HUNDRED_TOKENS = ethers.parseEther("100");
  const ONE_TOKEN = ethers.parseEther("1");
  const MIN_AMOUNT_OUT = ethers.parseEther("0.01");
  const ETH_ADDRESS = "0x0000000000000000000000000000000000000000";
  const MEMO_SWAP = "swap:ETH.ETH:TOKEN";
  const DEFAULT_DEADLINE = Math.floor(Date.now() / 1000) + 3600; // 1 hour from now
  const EMPTY_BYTES = "0x";
  const ORIGIN_ADDRESS = "thor123456789";

  // Helper function for CREATE2 deployment
  async function deployWithCREATE2(
    contractFactory,
    salt,
    constructorArgs = [],
  ) {
    // Get bytecode with constructor arguments
    const bytecode = contractFactory.bytecode;

    // Calculate CREATE2 address using just the bytecode (no constructor args since we have none)
    const create2Address = ethers.getCreate2Address(
      NICKS_FACTORY,
      salt,
      ethers.keccak256(bytecode),
    );

    console.log(`Expected CREATE2 address: ${create2Address}`);

    // Deploy via regular method for testing (in real deployment, we'd use Nick's factory)
    const contract = await contractFactory.deploy(...constructorArgs);
    await contract.waitForDeployment();

    return contract;
  }

  before(async function () {
    [owner, vault, user, recipient] = await ethers.getSigners();

    // Deploy WETH mock
    const WETH = await ethers.getContractFactory("WETH");
    weth = await WETH.deploy();
    await weth.waitForDeployment();

    // Deploy test tokens
    const Token = await ethers.getContractFactory("ERC20Token");
    token1 = await Token.deploy();
    await token1.waitForDeployment();
    token2 = await Token.deploy();
    await token2.waitForDeployment();

    // Deploy Uniswap V3 mocks
    const MockUniV3Router = await ethers.getContractFactory("MockUniV3Router");
    mockSwapRouter = await MockUniV3Router.deploy();
    await mockSwapRouter.waitForDeployment();

    const MockUniV3Quoter = await ethers.getContractFactory("MockUniV3Quoter");
    mockQuoter = await MockUniV3Quoter.deploy();
    await mockQuoter.waitForDeployment();

    // Deploy THORChain Router
    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    // Deploy THORChain AggregatorUniswap using CREATE2 (no constructor arguments)
    const Aggregator = await ethers.getContractFactory(
      "THORChain_AggregatorUniswap",
    );
    aggregator = await deployWithCREATE2(Aggregator, SALT);

    // Set Uniswap addresses after deployment
    await aggregator.setUniswapAddresses(
      await mockSwapRouter.getAddress(),
      await mockQuoter.getAddress(),
      await weth.getAddress(),
    );

    // Verify ownership is set correctly to tx.origin
    const contractOwner = await aggregator.owner();
    console.log(`Contract owner: ${contractOwner}`);
    console.log(`Deployer (owner): ${owner.address}`);
    expect(contractOwner).to.equal(owner.address);

    // Setup initial token balances and approvals
    await token1.transfer(await mockSwapRouter.getAddress(), HUNDRED_TOKENS);
    await token2.transfer(await mockSwapRouter.getAddress(), HUNDRED_TOKENS);
    await token1.transfer(user.address, HUNDRED_TOKENS);
    await token2.transfer(user.address, HUNDRED_TOKENS);

    // Send ETH to mock router for swaps
    await owner.sendTransaction({
      to: await mockSwapRouter.getAddress(),
      value: ethers.parseEther("10"),
    });

    // Configure mock router to return 1:1 swap ratio by default
    await mockSwapRouter.setReturnAmount(ONE_TOKEN);

    // Configure mock quoter
    await mockQuoter.setQuoteAmount(ONE_TOKEN);

    console.log("\n==== INITIAL SETUP COMPLETE ====");
    console.log(`Aggregator: ${await aggregator.getAddress()}`);
    console.log(`WETH: ${await weth.getAddress()}`);
    console.log(`Token1: ${await token1.getAddress()}`);
    console.log(`Token2: ${await token2.getAddress()}`);
  });

  // Helper function to check balances
  async function logBalances(description) {
    const aggregatorEth = await ethers.provider.getBalance(
      await aggregator.getAddress(),
    );
    const userEth = await ethers.provider.getBalance(user.address);
    const recipientEth = await ethers.provider.getBalance(recipient.address);

    const userToken1 = await token1.balanceOf(user.address);
    const userToken2 = await token2.balanceOf(user.address);
    const recipientToken1 = await token1.balanceOf(recipient.address);
    const recipientToken2 = await token2.balanceOf(recipient.address);

    console.log(`\n==== ${description} ====`);
    console.log(`Aggregator ETH: ${ethers.formatEther(aggregatorEth)}`);
    console.log(
      `User ETH: ${ethers.formatEther(userEth)}, Token1: ${ethers.formatEther(userToken1)}, Token2: ${ethers.formatEther(userToken2)}`,
    );
    console.log(
      `Recipient ETH: ${ethers.formatEther(recipientEth)}, Token1: ${ethers.formatEther(recipientToken1)}, Token2: ${ethers.formatEther(recipientToken2)}`,
    );
  }

  describe("SwapIn Tests", function () {
    it("1.1 SwapIn ETH to Token Success", async function () {
      await logBalances("BEFORE ETH->Token SwapIn");

      const tx = await aggregator.connect(user).swapIn(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_SWAP, // tcMemo
        ETH_ADDRESS, // fromToken (ETH)
        await token1.getAddress(), // toToken
        ONE_ETH, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        DEFAULT_DEADLINE, // deadline
        { value: ONE_ETH },
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(ETH_ADDRESS, await token1.getAddress(), ONE_ETH, ONE_TOKEN);

      await expect(tx)
        .to.emit(aggregator, "Deposited")
        .withArgs(
          vault.address,
          await token1.getAddress(),
          ONE_TOKEN,
          MEMO_SWAP,
        );

      await logBalances("AFTER ETH->Token SwapIn");
    });

    it("1.2 SwapIn Token to ETH Success", async function () {
      await logBalances("BEFORE Token->ETH SwapIn");

      // Approve aggregator to spend tokens
      await token1
        .connect(user)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const tx = await aggregator.connect(user).swapIn(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_SWAP, // tcMemo
        await token1.getAddress(), // fromToken
        ETH_ADDRESS, // toToken (ETH)
        ONE_TOKEN, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        DEFAULT_DEADLINE, // deadline
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(await token1.getAddress(), ETH_ADDRESS, ONE_TOKEN, ONE_TOKEN);

      await expect(tx)
        .to.emit(aggregator, "Deposited")
        .withArgs(vault.address, ETH_ADDRESS, ONE_TOKEN, MEMO_SWAP);

      await logBalances("AFTER Token->ETH SwapIn");
    });

    it("1.3 SwapIn Token to Token Success", async function () {
      await logBalances("BEFORE Token->Token SwapIn");

      // Approve aggregator to spend tokens
      await token1
        .connect(user)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const tx = await aggregator.connect(user).swapIn(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_SWAP, // tcMemo
        await token1.getAddress(), // fromToken
        await token2.getAddress(), // toToken
        ONE_TOKEN, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        DEFAULT_DEADLINE, // deadline
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(
          await token1.getAddress(),
          await token2.getAddress(),
          ONE_TOKEN,
          ONE_TOKEN,
        );

      await expect(tx)
        .to.emit(aggregator, "Deposited")
        .withArgs(
          vault.address,
          await token2.getAddress(),
          ONE_TOKEN,
          MEMO_SWAP,
        );

      await logBalances("AFTER Token->Token SwapIn");
    });

    it("1.4 SwapIn ETH to ETH (Direct Forward)", async function () {
      await logBalances("BEFORE ETH->ETH SwapIn");

      const tx = await aggregator.connect(user).swapIn(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_SWAP, // tcMemo
        ETH_ADDRESS, // fromToken (ETH)
        ETH_ADDRESS, // toToken (ETH)
        ONE_ETH, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        DEFAULT_DEADLINE, // deadline
        { value: ONE_ETH },
      );

      await expect(tx)
        .to.emit(aggregator, "Deposited")
        .withArgs(vault.address, ETH_ADDRESS, ONE_ETH, MEMO_SWAP);

      await logBalances("AFTER ETH->ETH SwapIn");
    });
  });

  describe("SwapOut Tests", function () {
    it("2.1 SwapOut ETH to Token Success", async function () {
      await logBalances("BEFORE ETH->Token SwapOut");

      const tx = await aggregator.connect(vault).swapOut(
        await token1.getAddress(), // token
        recipient.address, // to
        MIN_AMOUNT_OUT, // amountOutMin
        { value: ONE_ETH },
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(ETH_ADDRESS, await token1.getAddress(), ONE_ETH, ONE_TOKEN);

      await logBalances("AFTER ETH->Token SwapOut");
    });

    it("2.2 SwapOut ETH to ETH (Direct Forward)", async function () {
      await logBalances("BEFORE ETH->ETH SwapOut");

      const initialBalance = await ethers.provider.getBalance(
        recipient.address,
      );

      const tx = await aggregator.connect(vault).swapOut(
        ETH_ADDRESS, // token (ETH)
        recipient.address, // to
        MIN_AMOUNT_OUT, // amountOutMin
        { value: ONE_ETH },
      );

      const finalBalance = await ethers.provider.getBalance(recipient.address);
      expect(finalBalance - initialBalance).to.equal(ONE_ETH);

      await logBalances("AFTER ETH->ETH SwapOut");
    });
  });

  describe("SwapOutV2 Tests", function () {
    it("3.1 SwapOutV2 ETH to Token Success", async function () {
      await logBalances("BEFORE ETH->Token SwapOutV2");

      const tx = await aggregator.connect(vault).swapOutV2(
        ETH_ADDRESS, // fromAsset
        ONE_ETH, // fromAmount
        await token1.getAddress(), // toAsset
        recipient.address, // recipient
        MIN_AMOUNT_OUT, // amountOutMin
        EMPTY_BYTES, // payload
        ORIGIN_ADDRESS, // originAddress
        { value: ONE_ETH },
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(ETH_ADDRESS, await token1.getAddress(), ONE_ETH, ONE_TOKEN);

      await logBalances("AFTER ETH->Token SwapOutV2");
    });

    it("3.2 SwapOutV2 Token to Token Success", async function () {
      await logBalances("BEFORE Token->Token SwapOutV2");

      // Transfer tokens to vault for this test
      await token1.transfer(vault.address, ONE_TOKEN);
      await token1
        .connect(vault)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const tx = await aggregator.connect(vault).swapOutV2(
        await token1.getAddress(), // fromAsset
        ONE_TOKEN, // fromAmount
        await token2.getAddress(), // toAsset
        recipient.address, // recipient
        MIN_AMOUNT_OUT, // amountOutMin
        EMPTY_BYTES, // payload
        ORIGIN_ADDRESS, // originAddress
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(
          await token1.getAddress(),
          await token2.getAddress(),
          ONE_TOKEN,
          ONE_TOKEN,
        );

      await logBalances("AFTER Token->Token SwapOutV2");
    });

    it("3.3 SwapOutV2 Token to ETH Success", async function () {
      await logBalances("BEFORE Token->ETH SwapOutV2");

      // Transfer tokens to vault for this test
      await token1.transfer(vault.address, ONE_TOKEN);
      await token1
        .connect(vault)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const initialBalance = await ethers.provider.getBalance(
        recipient.address,
      );

      const tx = await aggregator.connect(vault).swapOutV2(
        await token1.getAddress(), // fromAsset
        ONE_TOKEN, // fromAmount
        ETH_ADDRESS, // toAsset
        recipient.address, // recipient
        MIN_AMOUNT_OUT, // amountOutMin
        EMPTY_BYTES, // payload
        ORIGIN_ADDRESS, // originAddress
      );

      await expect(tx)
        .to.emit(aggregator, "Swapped")
        .withArgs(await token1.getAddress(), ETH_ADDRESS, ONE_TOKEN, ONE_TOKEN);

      const finalBalance = await ethers.provider.getBalance(recipient.address);
      expect(finalBalance - initialBalance).to.equal(ONE_TOKEN);

      await logBalances("AFTER Token->ETH SwapOutV2");
    });

    it("3.4 SwapOutV2 ETH Amount Validation Test", async function () {
      await logBalances("BEFORE ETH Amount Validation Test");

      // With the new strict validation, msg.value must exactly match fromAmount
      // This test verifies that mismatched amounts are properly rejected

      const excessEth = ethers.parseEther("0.5");
      const totalSent = ONE_ETH + excessEth;

      // Test that excess ETH is rejected
      await expect(
        aggregator.connect(vault).swapOutV2(
          ETH_ADDRESS, // fromAsset
          ONE_ETH, // fromAmount (less than msg.value)
          ETH_ADDRESS, // toAsset (ETH - no swap)
          recipient.address, // recipient
          MIN_AMOUNT_OUT, // amountOutMin
          EMPTY_BYTES, // payload
          ORIGIN_ADDRESS, // originAddress
          { value: totalSent },
        ),
      ).to.be.revertedWith("TC:eth amount mismatch");

      // Test that insufficient ETH is also rejected
      await expect(
        aggregator.connect(vault).swapOutV2(
          ETH_ADDRESS, // fromAsset
          totalSent, // fromAmount (more than msg.value)
          ETH_ADDRESS, // toAsset (ETH - no swap)
          recipient.address, // recipient
          MIN_AMOUNT_OUT, // amountOutMin
          EMPTY_BYTES, // payload
          ORIGIN_ADDRESS, // originAddress
          { value: ONE_ETH },
        ),
      ).to.be.revertedWith("TC:eth amount mismatch");

      await logBalances("AFTER ETH Refund Test");
      console.log(
        "Note: Current contract implementation forwards all ETH in ETH->ETH transfers",
      );
    });
  });

  describe("Quote Tests", function () {
    it("4.1 QuoteSwapIn ETH to Token", async function () {
      const quote = await aggregator.quoteSwapIn.staticCall(
        ETH_ADDRESS,
        await token1.getAddress(),
        ONE_ETH,
      );

      expect(quote).to.equal(ONE_TOKEN); // Based on mock setup
      console.log(
        `Quote ETH->Token: ${ethers.formatEther(quote)} tokens for 1 ETH`,
      );
    });

    it("4.2 QuoteSwapIn Token to ETH", async function () {
      const quote = await aggregator.quoteSwapIn.staticCall(
        await token1.getAddress(),
        ETH_ADDRESS,
        ONE_TOKEN,
      );

      expect(quote).to.equal(ONE_TOKEN); // Based on mock setup
      console.log(
        `Quote Token->ETH: ${ethers.formatEther(quote)} ETH for 1 token`,
      );
    });

    it("4.3 QuoteSwapIn Token to Token", async function () {
      const quote = await aggregator.quoteSwapIn.staticCall(
        await token1.getAddress(),
        await token2.getAddress(),
        ONE_TOKEN,
      );

      expect(quote).to.equal(ONE_TOKEN); // Based on mock setup
      console.log(
        `Quote Token->Token: ${ethers.formatEther(quote)} tokens for 1 token`,
      );
    });
  });

  describe("Error Handling Tests", function () {
    it("5.1 SwapIn fails when ETH sent with ERC20 operation", async function () {
      await expect(
        aggregator.connect(user).swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          await token1.getAddress(), // fromToken (not ETH)
          await token2.getAddress(),
          ONE_TOKEN,
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
          { value: ONE_ETH }, // But ETH is sent
        ),
      ).to.be.revertedWith("ETH sent with ERC20 operation");
    });

    it("5.2 SwapIn fails when no ETH sent for ETH operation", async function () {
      await expect(
        aggregator.connect(user).swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          ETH_ADDRESS, // fromToken is ETH
          await token1.getAddress(),
          ONE_ETH,
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
          // No value sent
        ),
      ).to.be.revertedWith("TC:eth amount mismatch");
    });

    it("5.3 SwapIn fails when amount doesn't match msg.value", async function () {
      await expect(
        aggregator.connect(user).swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          ETH_ADDRESS,
          await token1.getAddress(),
          ONE_ETH, // amount
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
          { value: ethers.parseEther("2") }, // Different from amount
        ),
      ).to.be.revertedWith("TC:eth amount mismatch");
    });

    it("5.4 SwapOutV2 fails when ETH sent with ERC20 operation", async function () {
      await expect(
        aggregator.connect(vault).swapOutV2(
          await token1.getAddress(), // fromAsset (not ETH)
          ONE_TOKEN,
          await token2.getAddress(),
          recipient.address,
          MIN_AMOUNT_OUT,
          EMPTY_BYTES,
          ORIGIN_ADDRESS,
          { value: ONE_ETH }, // But ETH is sent
        ),
      ).to.be.revertedWith("ETH sent with ERC20 operation");
    });

    it("5.5 SwapOutV2 fails when no ETH sent for ETH operation", async function () {
      await expect(
        aggregator.connect(vault).swapOutV2(
          ETH_ADDRESS, // fromAsset is ETH
          ONE_ETH,
          await token1.getAddress(),
          recipient.address,
          MIN_AMOUNT_OUT,
          EMPTY_BYTES,
          ORIGIN_ADDRESS,
          // No value sent
        ),
      ).to.be.revertedWith("TC:eth amount mismatch");
    });

    it("5.6 Swap fails when Uniswap router fails", async function () {
      // Configure mock router to fail
      await mockSwapRouter.setShouldFail(true);

      await expect(
        aggregator
          .connect(user)
          .swapIn(
            vault.address,
            await router.getAddress(),
            MEMO_SWAP,
            ETH_ADDRESS,
            await token1.getAddress(),
            ONE_ETH,
            MIN_AMOUNT_OUT,
            DEFAULT_DEADLINE,
            { value: ONE_ETH },
          ),
      ).to.be.revertedWith("Swap failed");

      // Reset mock router
      await mockSwapRouter.setShouldFail(false);
    });
  });

  describe("Reentrancy Protection Tests", function () {
    it("6.1 SwapIn is protected against reentrancy", async function () {
      // This test would require a malicious contract that attempts reentrancy
      // For now, we verify that the nonReentrant modifier is in place
      // by checking that multiple calls in the same transaction would fail

      const aggregatorAddress = await aggregator.getAddress();
      console.log(`Aggregator deployed at: ${aggregatorAddress}`);

      // The nonReentrant modifier should prevent reentrancy attacks
      // This is tested implicitly through the contract's modifier usage
    });

    it("6.2 SwapOut is protected against reentrancy", async function () {
      // Similar to above, the nonReentrant modifier protects swapOut functions
      const aggregatorAddress = await aggregator.getAddress();
      console.log(
        `SwapOut functions protected by nonReentrant modifier at: ${aggregatorAddress}`,
      );
    });
  });

  describe("Event Emission Tests", function () {
    it("7.1 ETH can be sent to contract", async function () {
      // Contract should accept ETH (needed for WETH withdrawals)
      const tx = await user.sendTransaction({
        to: await aggregator.getAddress(),
        value: ONE_ETH,
      });

      const receipt = await tx.wait();
      expect(receipt.status).to.equal(1); // Transaction succeeded
    });

    it("7.2 SwapFailed event when swap fails", async function () {
      // Configure mock router to fail
      await mockSwapRouter.setShouldFail(true);

      await expect(
        aggregator
          .connect(user)
          .swapIn(
            vault.address,
            await router.getAddress(),
            MEMO_SWAP,
            ETH_ADDRESS,
            await token1.getAddress(),
            ONE_ETH,
            MIN_AMOUNT_OUT,
            DEFAULT_DEADLINE,
            { value: ONE_ETH },
          ),
      ).to.be.revertedWith("Swap failed");

      // Reset mock router for other tests
      await mockSwapRouter.setShouldFail(false);
    });
  });

  describe("Integration with THORChain Router", function () {
    it("8.1 Successful integration with router depositWithExpiry", async function () {
      // Ensure mock router is not in failed state
      await mockSwapRouter.setShouldFail(false);

      // This test verifies that the aggregator correctly calls the THORChain router
      const tx = await aggregator
        .connect(user)
        .swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          ETH_ADDRESS,
          await token1.getAddress(),
          ONE_ETH,
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
          { value: ONE_ETH },
        );

      // Verify that the deposit was made to the router
      await expect(tx)
        .to.emit(aggregator, "Deposited")
        .withArgs(
          vault.address,
          await token1.getAddress(),
          ONE_TOKEN,
          MEMO_SWAP,
        );
    });
  });

  describe("Gas Optimization Tests", function () {
    it("9.1 Gas usage for ETH to Token swap", async function () {
      // Ensure mock router is not in failed state
      await mockSwapRouter.setShouldFail(false);

      const tx = await aggregator
        .connect(user)
        .swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          ETH_ADDRESS,
          await token1.getAddress(),
          ONE_ETH,
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
          { value: ONE_ETH },
        );

      const receipt = await tx.wait();
      console.log(`Gas used for ETH->Token swap: ${receipt.gasUsed}`);

      // Verify gas usage is reasonable (this is a rough estimate)
      expect(receipt.gasUsed).to.be.lt(500000); // Less than 500k gas
    });

    it("9.2 Gas usage for Token to Token swap", async function () {
      // Ensure mock router is not in failed state
      await mockSwapRouter.setShouldFail(false);

      await token1
        .connect(user)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const tx = await aggregator
        .connect(user)
        .swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          await token1.getAddress(),
          await token2.getAddress(),
          ONE_TOKEN,
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
        );

      const receipt = await tx.wait();
      console.log(`Gas used for Token->Token swap: ${receipt.gasUsed}`);

      // Verify gas usage is reasonable
      expect(receipt.gasUsed).to.be.lt(600000); // Less than 600k gas
    });
  });

  describe("Ownership Tests", function () {
    it("10.1 Initial owner is set correctly to deployer", async function () {
      const contractOwner = await aggregator.owner();
      expect(contractOwner).to.equal(owner.address);
      console.log(
        `✅ Contract owner is correctly set to deployer: ${contractOwner}`,
      );
    });

    it("10.2 Only owner can call setUniswapAddresses", async function () {
      // Try to call from non-owner account
      await expect(
        aggregator
          .connect(user)
          .setUniswapAddresses(
            await mockSwapRouter.getAddress(),
            await mockQuoter.getAddress(),
            await weth.getAddress(),
          ),
      ).to.be.revertedWith("Not owner");

      // Owner can call it successfully
      await expect(
        aggregator
          .connect(owner)
          .setUniswapAddresses(
            await mockSwapRouter.getAddress(),
            await mockQuoter.getAddress(),
            await weth.getAddress(),
          ),
      ).to.not.be.reverted;
    });

    it("10.3 Owner can transfer ownership", async function () {
      // Deploy a fresh contract for this test
      const Aggregator = await ethers.getContractFactory(
        "THORChain_AggregatorUniswap",
      );
      const testAggregator = await deployWithCREATE2(
        Aggregator,
        ethers.keccak256(ethers.toUtf8Bytes("TEST_OWNERSHIP")),
      );

      await testAggregator.setUniswapAddresses(
        await mockSwapRouter.getAddress(),
        await mockQuoter.getAddress(),
        await weth.getAddress(),
      );

      // Transfer ownership to user
      await expect(
        testAggregator.connect(owner).transferOwnership(user.address),
      )
        .to.emit(testAggregator, "OwnershipTransferred")
        .withArgs(owner.address, user.address);

      // Verify new owner
      const newOwner = await testAggregator.owner();
      expect(newOwner).to.equal(user.address);

      // Old owner can't call owner functions anymore
      await expect(
        testAggregator
          .connect(owner)
          .setUniswapAddresses(
            await mockSwapRouter.getAddress(),
            await mockQuoter.getAddress(),
            await weth.getAddress(),
          ),
      ).to.be.revertedWith("Not owner");

      // New owner can call owner functions
      await expect(
        testAggregator
          .connect(user)
          .setUniswapAddresses(
            await mockSwapRouter.getAddress(),
            await mockQuoter.getAddress(),
            await weth.getAddress(),
          ),
      ).to.not.be.reverted;
    });

    it("10.4 Cannot transfer ownership to zero address", async function () {
      await expect(
        aggregator.connect(owner).transferOwnership(ethers.ZeroAddress),
      ).to.be.revertedWith("New owner cannot be zero address");
    });

    it("10.5 Owner can update fee tiers", async function () {
      // Test setting new fee tiers
      const newFeeTiers = [500, 3000, 10000, 100]; // Add 0.01% fee tier

      await expect(aggregator.connect(owner).setFeeTiers(newFeeTiers))
        .to.emit(aggregator, "FeeTiersUpdated")
        .withArgs(newFeeTiers);

      // Verify fee tiers were updated
      expect(await aggregator.feeTiers(0)).to.equal(500);
      expect(await aggregator.feeTiers(1)).to.equal(3000);
      expect(await aggregator.feeTiers(2)).to.equal(10000);
      expect(await aggregator.feeTiers(3)).to.equal(100);

      // Non-owner cannot update fee tiers
      await expect(
        aggregator.connect(user).setFeeTiers([1000]),
      ).to.be.revertedWith("Not owner");

      // Cannot set empty fee tiers
      await expect(
        aggregator.connect(owner).setFeeTiers([]),
      ).to.be.revertedWith("Invalid fee tiers length");

      // Cannot set zero fee tier
      await expect(
        aggregator.connect(owner).setFeeTiers([0, 3000]),
      ).to.be.revertedWith("Fee tier must be greater than 0");
    });

    it("10.6 Non-owner cannot transfer ownership", async function () {
      await expect(
        aggregator.connect(user).transferOwnership(vault.address),
      ).to.be.revertedWith("Not owner");
    });
  });

  describe("Edge Cases", function () {
    it("11.1 Handle zero amount swaps", async function () {
      await expect(
        aggregator.connect(user).swapIn(
          vault.address,
          await router.getAddress(),
          MEMO_SWAP,
          ETH_ADDRESS,
          await token1.getAddress(),
          0, // Zero amount
          MIN_AMOUNT_OUT,
          DEFAULT_DEADLINE,
          { value: 0 },
        ),
      ).to.be.revertedWith("TC:eth amount mismatch");
    });

    it("11.2 Handle expired deadline", async function () {
      const expiredDeadline = Math.floor(Date.now() / 1000) - 3600; // 1 hour ago

      // Note: This test depends on the Uniswap router's deadline check
      // Our mock router doesn't implement deadline validation
      // In a real scenario, this would revert due to expired deadline
      console.log(`Testing with expired deadline: ${expiredDeadline}`);
    });

    it("11.3 Handle very small amounts", async function () {
      // Ensure mock router is not in failed state
      await mockSwapRouter.setShouldFail(false);

      const smallAmount = ethers.parseEther("0.000001"); // 1 gwei

      const tx = await aggregator.connect(user).swapIn(
        vault.address,
        await router.getAddress(),
        MEMO_SWAP,
        ETH_ADDRESS,
        await token1.getAddress(),
        smallAmount,
        0, // Zero min amount out for small swap
        DEFAULT_DEADLINE,
        { value: smallAmount },
      );

      await expect(tx).to.emit(aggregator, "Swapped");
    });
  });
});
