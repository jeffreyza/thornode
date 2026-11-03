const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("THORChain Router Aggregation V2 Edge Cases", function () {
  let router, aggregator, revertingAggregator, token1, token2, sushiRouter;
  let vault, user;

  const ONE_ETH = ethers.parseEther("1");
  const ONE_TOKEN = ethers.parseEther("1");
  const HUNDRED_TOKENS = ethers.parseEther("100");
  const MIN_AMOUNT_OUT = ethers.parseEther("0.01");
  const ETH_ADDRESS = "0x0000000000000000000000000000000000000000";
  const MEMO_ETH_TO_TOKEN = "swap:ETH.ETH:TOKEN";
  const MEMO_TOKEN_TO_TOKEN = "swap:ETH.TOKEN:TOKEN";
  const EMPTY_BYTES = "0x";
  const ORIGIN_ADDRESS = "thor123456789";

  beforeEach(async function () {
    [vault, user] = await ethers.getSigners();

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

    const RevertingAggregator = await ethers.getContractFactory(
      "Reverting_Aggregator",
    );
    revertingAggregator = await RevertingAggregator.deploy(
      await sushiRouter.getAddress(),
    );
    await revertingAggregator.waitForDeployment();

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
    const userToken1Bal = await token1.balanceOf(user.address);
    const userToken2Bal = await token2.balanceOf(user.address);

    console.log("\n==== INITIAL SETUP BALANCES ====");
    const vaultEthBalance = await ethers.provider.getBalance(vault.address);
    const sushiEthBalance = await ethers.provider.getBalance(
      await sushiRouter.getAddress(),
    );
    const userEthBalance = await ethers.provider.getBalance(user.address);

    console.log(
      `Vault: ${ethers.formatEther(vaultEthBalance)} ETH, ${ethers.formatEther(vaultToken1Bal)} token1, ${ethers.formatEther(vaultToken2Bal)} token2`,
    );
    console.log(
      `SushiRouter: ${ethers.formatEther(sushiEthBalance)} ETH, ${ethers.formatEther(sushiRouterToken1Bal)} token1, ${ethers.formatEther(sushiRouterToken2Bal)} token2`,
    );
    console.log(
      `User: ${ethers.formatEther(userEthBalance)} ETH, ${ethers.formatEther(userToken1Bal)} token1, ${ethers.formatEther(userToken2Bal)} token2`,
    );

    expect(vaultToken1Bal).to.be.gte(HUNDRED_TOKENS);
    expect(vaultToken2Bal).to.be.gte(HUNDRED_TOKENS);
    expect(sushiRouterToken1Bal).to.be.gte(HUNDRED_TOKENS);
    expect(sushiRouterToken2Bal).to.be.gte(HUNDRED_TOKENS);
  });

  async function checkBalances(
    expectedVaultEth,
    expectedVaultToken1,
    expectedVaultToken2,
    expectedSushiRouterEth,
    expectedSushiRouterToken1,
    expectedSushiRouterToken2,
    expectedUserToken1,
    expectedUserToken2,
  ) {
    const vaultEthBalance = await ethers.provider.getBalance(vault.address);
    const vaultToken1Balance = await token1.balanceOf(vault.address);
    const vaultToken2Balance = await token2.balanceOf(vault.address);

    const sushiRouterEthBalance = await ethers.provider.getBalance(
      await sushiRouter.getAddress(),
    );
    const sushiRouterToken1Balance = await token1.balanceOf(
      await sushiRouter.getAddress(),
    );
    const sushiRouterToken2Balance = await token2.balanceOf(
      await sushiRouter.getAddress(),
    );

    const userEthBalance = await ethers.provider.getBalance(user.address);
    const userToken1Balance = await token1.balanceOf(user.address);
    const userToken2Balance = await token2.balanceOf(user.address);

    console.log(
      `Vault: ${ethers.formatEther(vaultEthBalance)} ETH, ${ethers.formatEther(vaultToken1Balance)} token1, ${ethers.formatEther(vaultToken2Balance)} token2`,
    );
    console.log(
      `SushiRouter: ${ethers.formatEther(sushiRouterEthBalance)} ETH, ${ethers.formatEther(sushiRouterToken1Balance)} token1, ${ethers.formatEther(sushiRouterToken2Balance)} token2`,
    );
    console.log(
      `User: ${ethers.formatEther(userEthBalance)} ETH, ${ethers.formatEther(userToken1Balance)} token1, ${ethers.formatEther(userToken2Balance)} token2`,
    );

    expect(vaultEthBalance).to.be.lessThanOrEqual(expectedVaultEth);
    expect(vaultToken1Balance).to.equal(expectedVaultToken1);
    expect(vaultToken2Balance).to.equal(expectedVaultToken2);

    expect(sushiRouterEthBalance).to.equal(expectedSushiRouterEth);
    expect(sushiRouterToken1Balance).to.equal(expectedSushiRouterToken1);
    expect(sushiRouterToken2Balance).to.equal(expectedSushiRouterToken2);

    expect(userToken1Balance).to.equal(expectedUserToken1);
    expect(userToken2Balance).to.equal(expectedUserToken2);
  }

  describe("Failure Path Tests", function () {
    it("1.1 ETH Transfer to Invalid Target Contract", async function () {
      const userInitialEthBalance = await ethers.provider.getBalance(
        user.address,
      );

      const params = {
        target: await token1.getAddress(),
        fromAsset: ETH_ADDRESS,
        fromAmount: ONE_ETH,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_ETH_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router
        .connect(vault)
        .transferOutAndCallV2(params, { value: ONE_ETH });

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          ETH_ADDRESS,
          ONE_ETH,
          params.memo,
        );

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

      const userFinalEthBalance = await ethers.provider.getBalance(
        user.address,
      );
      expect(userFinalEthBalance - userInitialEthBalance).to.equal(ONE_ETH);

      console.log(
        "Test 1.1 completed - ETH transfer to invalid target handled correctly",
      );
    });

    it("1.2 ERC20 Transfer to Invalid Target Contract", async function () {
      const userInitialToken1Balance = await token1.balanceOf(user.address);

      await token1.connect(vault).approve(await router.getAddress(), ONE_TOKEN);

      const params = {
        target: await token2.getAddress(),
        fromAsset: await token1.getAddress(),
        fromAmount: ONE_TOKEN,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_TOKEN_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router.connect(vault).transferOutAndCallV2(params);

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          await token1.getAddress(),
          ONE_TOKEN,
          params.memo,
        );

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

      const userFinalToken1Balance = await token1.balanceOf(user.address);
      expect(userFinalToken1Balance - userInitialToken1Balance).to.equal(
        ONE_TOKEN,
      );

      // Skip checkBalances for token transfer tests due to accumulation
      console.log(
        "Test 1.2 completed - ERC20 transfer to invalid target handled correctly",
      );
    });

    it("1.3 ETH Target Function Reverts", async function () {
      const userInitialEthBalance = await ethers.provider.getBalance(
        user.address,
      );

      const params = {
        target: await revertingAggregator.getAddress(),
        fromAsset: ETH_ADDRESS,
        fromAmount: ONE_ETH,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_ETH_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router
        .connect(vault)
        .transferOutAndCallV2(params, { value: ONE_ETH });

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          ETH_ADDRESS,
          ONE_ETH,
          params.memo,
        );

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

      const userFinalEthBalance = await ethers.provider.getBalance(
        user.address,
      );
      expect(userFinalEthBalance - userInitialEthBalance).to.equal(ONE_ETH);

      console.log(
        "Test 1.3 completed - ETH target function revert handled correctly",
      );
    });

    it("1.4 ERC20 Target Function Reverts", async function () {
      const userInitialToken1Balance = await token1.balanceOf(user.address);

      await token1.connect(vault).approve(await router.getAddress(), ONE_TOKEN);

      const params = {
        target: await revertingAggregator.getAddress(),
        fromAsset: await token1.getAddress(),
        fromAmount: ONE_TOKEN,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_TOKEN_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const tx = await router.connect(vault).transferOutAndCallV2(params);

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          await token1.getAddress(),
          ONE_TOKEN,
          params.memo,
        );

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

      const userFinalToken1Balance = await token1.balanceOf(user.address);
      expect(userFinalToken1Balance - userInitialToken1Balance).to.equal(
        ONE_TOKEN,
      );

      console.log(
        "Test 1.4 completed - ERC20 target function revert handled correctly",
      );
    });

    it("1.5 Unexpected ETH with ERC20 Transfer", async function () {
      await token1.connect(vault).approve(await router.getAddress(), ONE_TOKEN);

      const params = {
        target: await aggregator.getAddress(),
        fromAsset: await token1.getAddress(),
        fromAmount: ONE_TOKEN,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: MEMO_TOKEN_TO_TOKEN,
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      await expect(
        router.connect(vault).transferOutAndCallV2(params, { value: ONE_ETH }),
      ).to.be.revertedWith("TC:unexpected eth");

      console.log("\n==== TEST 1.5: Transaction reverted as expected ====");
    });

    it("1.6 Zero Approval Verification on ERC20 Failure", async function () {
      const amount = ethers.parseEther("10");

      await token1.transfer(vault.address, amount);
      await token1.connect(vault).approve(await router.getAddress(), amount);

      const initialVaultApproval = await token1.allowance(
        vault.address,
        await router.getAddress(),
      );
      expect(initialVaultApproval).to.equal(amount);

      const params = {
        target: await revertingAggregator.getAddress(),
        fromAsset: await token1.getAddress(),
        fromAmount: amount,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: "APPROVAL_ZERO_TEST",
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const initialRouterToTargetApproval = await token1.allowance(
        await router.getAddress(),
        await revertingAggregator.getAddress(),
      );
      expect(initialRouterToTargetApproval).to.equal(0);

      const tx = await router.connect(vault).transferOutAndCallV2(params);

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          await token1.getAddress(),
          amount,
          params.memo,
        );

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

      const finalRouterToTargetApproval = await token1.allowance(
        await router.getAddress(),
        await revertingAggregator.getAddress(),
      );
      expect(finalRouterToTargetApproval).to.equal(
        0,
        "Router should have zeroed out approval to target after failed call",
      );

      const userFinalBalance = await token1.balanceOf(user.address);
      expect(userFinalBalance).to.equal(
        amount,
        "User should have received tokens as fallback",
      );

      const routerBalance = await token1.balanceOf(await router.getAddress());
      expect(routerBalance).to.equal(0, "Router should not hold any tokens");

      console.log(
        "\n==== TEST 1.6: Approval correctly zeroed on target call failure ====",
      );
    });

    it("1.7 Rebasing Token in V2 Context", async function () {
      const RebasingToken = await ethers.getContractFactory(
        "contracts/attacks/RebasingToken.sol:RebasingToken",
      );
      const rebasingToken = await RebasingToken.deploy(
        "Rebasing Token V2",
        "REBASE2",
      );
      await rebasingToken.waitForDeployment();

      const amount = ethers.parseEther("10");

      const vaultTokenAmount = ethers.parseEther("20");
      await rebasingToken.transfer(vault.address, vaultTokenAmount);
      await rebasingToken
        .connect(vault)
        .approve(await router.getAddress(), amount);

      const params = {
        target: await aggregator.getAddress(),
        fromAsset: await rebasingToken.getAddress(),
        fromAmount: amount,
        toAsset: await token2.getAddress(),
        recipient: user.address,
        amountOutMin: MIN_AMOUNT_OUT,
        memo: "REBASING_V2_TEST",
        payload: EMPTY_BYTES,
        originAddress: ORIGIN_ADDRESS,
      };

      const initialUserToken2Balance = await token2.balanceOf(user.address);
      const initialVaultRebasingBalance = await rebasingToken.balanceOf(
        vault.address,
      );

      const tx = await router.connect(vault).transferOutAndCallV2(params);

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

      const finalUserToken2Balance = await token2.balanceOf(user.address);
      const finalVaultRebasingBalance = await rebasingToken.balanceOf(
        vault.address,
      );

      expect(finalUserToken2Balance).to.be.gt(
        initialUserToken2Balance,
        "User should receive token2",
      );
      expect(finalVaultRebasingBalance).to.equal(
        initialVaultRebasingBalance - amount,
        "Vault should lose rebasing tokens",
      );

      await rebasingToken.rebase(12000);

      const afterRebaseVaultBalance = await rebasingToken.balanceOf(
        vault.address,
      );
      if (finalVaultRebasingBalance > 0) {
        expect(afterRebaseVaultBalance).to.be.gt(
          finalVaultRebasingBalance,
          "Rebasing should affect vault balance",
        );
      } else {
        expect(afterRebaseVaultBalance).to.equal(0, "No tokens to rebase");
      }

      const routerBalance = await rebasingToken.balanceOf(
        await router.getAddress(),
      );
      expect(routerBalance).to.equal(
        0,
        "Router should not hold rebasing tokens",
      );

      console.log("\n==== TEST 1.7: Rebasing token handled in V2 context ====");
    });
  });
});
