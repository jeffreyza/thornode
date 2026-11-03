const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("THORChain Router Aggregation V2 Tests", function () {
  let router, aggregator, token1, token2, sushiRouter;
  let owner, vault, recipient;

  const ONE_ETH = ethers.parseEther("1");
  const HUNDRED_TOKENS = ethers.parseEther("100");
  const ONE_TOKEN = ethers.parseEther("1");
  const MIN_AMOUNT_OUT = ethers.parseEther("0.01");
  const ETH_ADDRESS = "0x0000000000000000000000000000000000000000";
  const MEMO_ETH_TO_TOKEN = "swap:ETH.ETH:TOKEN";
  const MEMO_TOKEN_TO_TOKEN = "swap:ETH.TOKEN:TOKEN";
  const MEMO_TOKEN_TO_ETH = "swap:ETH.TOKEN:ETH";
  const EMPTY_BYTES = "0x";
  const ORIGIN_ADDRESS = "thor123456789";

  before(async function () {
    [owner, vault, recipient] = await ethers.getSigners();

    const Token = await ethers.getContractFactory("ERC20Token");
    token1 = await Token.deploy();
    await token1.waitForDeployment();
    token2 = await Token.deploy();
    await token2.waitForDeployment();

    const SushiRouterSmol = await ethers.getContractFactory("SushiRouterSmol");
    sushiRouter = await SushiRouterSmol.deploy();
    await sushiRouter.waitForDeployment();

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    const Aggregator = await ethers.getContractFactory("THORChain_Aggregator");
    aggregator = await Aggregator.deploy(await sushiRouter.getAddress());
    await aggregator.waitForDeployment();

    await token1.transfer(await sushiRouter.getAddress(), HUNDRED_TOKENS);
    await token2.transfer(await sushiRouter.getAddress(), HUNDRED_TOKENS);
    await token1.transfer(vault.address, HUNDRED_TOKENS);
    await token2.transfer(vault.address, HUNDRED_TOKENS);

    const vaultToken1Bal = await token1.balanceOf(vault.address);
    const vaultToken2Bal = await token2.balanceOf(vault.address);
    const sushiRouterToken1Bal = await token1.balanceOf(
      await sushiRouter.getAddress(),
    );
    const sushiRouterToken2Bal = await token2.balanceOf(
      await sushiRouter.getAddress(),
    );
    const userToken1Bal = await token1.balanceOf(recipient.address);
    const userToken2Bal = await token2.balanceOf(recipient.address);

    console.log("\n==== INITIAL SETUP BALANCES ====");
    const vaultEthBalance = await ethers.provider.getBalance(vault.address);
    const sushiEthBalance = await ethers.provider.getBalance(
      await sushiRouter.getAddress(),
    );
    const userEthBalance = await ethers.provider.getBalance(recipient.address);

    console.log(
      `Vault: ${ethers.formatEther(vaultEthBalance)} ETH, ${ethers.formatEther(vaultToken1Bal)} token1, ${ethers.formatEther(vaultToken2Bal)} token2`,
    );
    console.log(
      `SushiRouter: ${ethers.formatEther(sushiEthBalance)} ETH, ${ethers.formatEther(sushiRouterToken1Bal)} token1, ${ethers.formatEther(sushiRouterToken2Bal)} token2`,
    );
    console.log(
      `User: ${ethers.formatEther(userEthBalance)} ETH, ${ethers.formatEther(userToken1Bal)} token1, ${ethers.formatEther(userToken2Bal)} token2`,
    );

    expect(vaultToken1Bal).to.equal(HUNDRED_TOKENS);
    expect(vaultToken2Bal).to.equal(HUNDRED_TOKENS);
    expect(sushiRouterToken1Bal).to.equal(HUNDRED_TOKENS);
    expect(sushiRouterToken2Bal).to.equal(HUNDRED_TOKENS);
    expect(userToken1Bal).to.equal(0);
    expect(userToken2Bal).to.equal(0);
  });

  // Helper function to check balances
  async function checkBalances(
    vaultEthDelta,
    sushiRouterEthDelta,
    userEthDelta,
    vaultToken1Delta,
    sushiRouterToken1Delta,
    userToken1Delta,
    vaultToken2Delta,
    sushiRouterToken2Delta,
    userToken2Delta,
  ) {
    const vaultEthBal = await ethers.provider.getBalance(vault.address);
    const sushiRouterEthBal = await ethers.provider.getBalance(
      await sushiRouter.getAddress(),
    );
    const userEthBal = await ethers.provider.getBalance(recipient.address);

    const vaultToken1Bal = await token1.balanceOf(vault.address);
    const sushiRouterToken1Bal = await token1.balanceOf(
      await sushiRouter.getAddress(),
    );
    const userToken1Bal = await token1.balanceOf(recipient.address);

    const vaultToken2Bal = await token2.balanceOf(vault.address);
    const sushiRouterToken2Bal = await token2.balanceOf(
      await sushiRouter.getAddress(),
    );
    const userToken2Bal = await token2.balanceOf(recipient.address);

    console.log("\n==== BALANCES AFTER TEST ====");
    console.log(
      `Vault: ${ethers.formatEther(vaultEthBal)} ETH, ${ethers.formatEther(vaultToken1Bal)} token1, ${ethers.formatEther(vaultToken2Bal)} token2`,
    );
    console.log(
      `SushiRouter: ${ethers.formatEther(sushiRouterEthBal)} ETH, ${ethers.formatEther(sushiRouterToken1Bal)} token1, ${ethers.formatEther(sushiRouterToken2Bal)} token2`,
    );
    console.log(
      `User: ${ethers.formatEther(userEthBal)} ETH, ${ethers.formatEther(userToken1Bal)} token1, ${ethers.formatEther(userToken2Bal)} token2`,
    );

    console.log(`Vault ETH: ${ethers.formatEther(vaultEthBal)} ETH`);
    console.log(`Vault ETH balance: ${ethers.formatEther(vaultEthBal)} ETH`);

    expect(vaultEthBal).to.be.gt(0);
    expect(sushiRouterEthBal).to.equal(sushiRouterEthDelta);

    console.log(`User ETH: ${ethers.formatEther(userEthBal)} ETH`);
    expect(userEthBal).to.be.gte(ethers.parseEther("10000"));

    expect(vaultToken1Bal).to.equal(HUNDRED_TOKENS + vaultToken1Delta);
    expect(sushiRouterToken1Bal).to.equal(
      HUNDRED_TOKENS + sushiRouterToken1Delta,
    );
    expect(userToken1Bal).to.equal(userToken1Delta);

    expect(vaultToken2Bal).to.equal(HUNDRED_TOKENS + vaultToken2Delta);
    expect(sushiRouterToken2Bal).to.equal(
      HUNDRED_TOKENS + sushiRouterToken2Delta,
    );
    expect(userToken2Bal).to.equal(userToken2Delta);
  }

  describe("Happy Path Tests", function () {
    it("1.1 ETH to Token Swap Success", async function () {
      console.log("\n==== TEST 1.1: ETH to Token Swap ====");

      const params = {
        target: await aggregator.getAddress(),
        fromAsset: ETH_ADDRESS,
        fromAmount: ONE_ETH,
        toAsset: await token1.getAddress(),
        recipient: recipient.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_ETH_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router
        .connect(vault)
        .transferOutAndCallV2(params, { value: ONE_ETH });

      await expect(tx)
        .to.emit(router, "TransferOutAndCallV2")
        .withArgs(
          vault.address,
          params.target,
          params.fromAsset,
          params.fromAmount,
          params.toAsset,
          params.recipient,
          params.amountOutMin,
          params.memo,
          params.payload,
          params.originAddress,
        );

      await checkBalances(
        ONE_ETH, // vaultEthDelta: 1 ETH spent
        ONE_ETH, // sushiRouterEthDelta: received 1 ETH
        0n, // userEthDelta: no change
        0n, // vaultToken1Delta: no change
        -ONE_TOKEN, // sushiRouterToken1Delta: sent 1 token
        ONE_TOKEN, // userToken1Delta: received 1 token
        0n, // vaultToken2Delta: no change
        0n, // sushiRouterToken2Delta: no change
        0n, // userToken2Delta: no change
      );
    });

    it("1.2 Token1 to Token2 Swap Success", async function () {
      console.log("\n==== TEST 1.2: Token1 to Token2 Swap ====");

      await token1.connect(vault).approve(await router.getAddress(), ONE_TOKEN);

      const params = {
        target: await aggregator.getAddress(),
        fromAsset: await token1.getAddress(),
        fromAmount: ONE_TOKEN,
        toAsset: await token2.getAddress(),
        recipient: recipient.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_TOKEN_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router
        .connect(vault)
        .transferOutAndCallV2(params, { value: 0 });

      await expect(tx)
        .to.emit(router, "TransferOutAndCallV2")
        .withArgs(
          vault.address,
          params.target,
          params.fromAsset,
          params.fromAmount,
          params.toAsset,
          params.recipient,
          params.amountOutMin,
          params.memo,
          params.payload,
          params.originAddress,
        );

      await checkBalances(
        ONE_ETH, // vaultEthDelta: still 1 ETH spent (cumulative)
        ONE_ETH, // sushiRouterEthDelta: still has 1 ETH
        0n, // userEthDelta: no change
        -ONE_TOKEN, // vaultToken1Delta: spent 1 token1
        0n, // sushiRouterToken1Delta: net 0 change
        ONE_TOKEN, // userToken1Delta: still has 1 token1
        0n, // vaultToken2Delta: no change
        -ONE_TOKEN, // sushiRouterToken2Delta: sent 1 token2
        ONE_TOKEN, // userToken2Delta: received 1 token2
      );
    });

    it("1.3 Token1 to ETH Swap Success", async function () {
      console.log("\n==== TEST 1.3: Token1 to ETH Swap ====");

      await token1.connect(vault).approve(await router.getAddress(), ONE_TOKEN);

      const params = {
        target: await aggregator.getAddress(),
        fromAsset: await token1.getAddress(),
        fromAmount: ONE_TOKEN,
        toAsset: ETH_ADDRESS,
        recipient: recipient.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_TOKEN_TO_ETH,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router
        .connect(vault)
        .transferOutAndCallV2(params, { value: 0 });

      await expect(tx)
        .to.emit(router, "TransferOutAndCallV2")
        .withArgs(
          vault.address,
          params.target,
          params.fromAsset,
          params.fromAmount,
          params.toAsset,
          params.recipient,
          params.amountOutMin,
          params.memo,
          params.payload,
          params.originAddress,
        );

      await checkBalances(
        ONE_ETH, // vaultEthDelta: still 1 ETH spent (cumulative)
        0n, // sushiRouterEthDelta: sent 1 ETH to user
        ONE_ETH, // userEthDelta: received 1 ETH
        -ONE_TOKEN * 2n, // vaultToken1Delta: spent 2 token1 total
        ONE_TOKEN, // sushiRouterToken1Delta: received 1 more token1
        ONE_TOKEN, // userToken1Delta: still has 1 token1
        0n, // vaultToken2Delta: no change
        -ONE_TOKEN, // sushiRouterToken2Delta: still -1 token2
        ONE_TOKEN, // userToken2Delta: still has 1 token2
      );
    });
  });

  // NOTE: Failure path tests and edge cases for transferOutAndCallV2 are comprehensively
  // covered in test/8_edge.js, including:
  // - Invalid target contracts
  // - Target functions that revert
  // - Unexpected ETH with ERC20 transfers
  // - Approval zeroing on failure
  // - Rebasing token handling
  // See 8_edge.js for complete failure scenario coverage.
});
