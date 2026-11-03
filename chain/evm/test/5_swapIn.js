const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("THORChain Aggregator SwapIn Tests", function () {
  let router, aggregator, token1, token2, sushiRouter;
  let owner, user, vault;

  const ONE_ETH = ethers.parseEther("1");
  const MIN_AMOUNT_OUT = ethers.parseEther("0.1");
  const ETH_ADDRESS = "0x0000000000000000000000000000000000000000";
  const MEMO_TOKEN_TO_ETH = "swap:TOKEN1:ETH.ETH";
  const MEMO_TOKEN_TO_TOKEN = "swap:TOKEN1:TOKEN2";
  const INITIAL_TOKEN_AMOUNT = ethers.parseEther("100");
  const ONE_TOKEN = ethers.parseEther("1");

  beforeEach(async function () {
    [owner, user, vault] = await ethers.getSigners();

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

    await token1.transfer(await user.getAddress(), INITIAL_TOKEN_AMOUNT);
    await token1.transfer(await sushiRouter.getAddress(), INITIAL_TOKEN_AMOUNT);
    await token2.transfer(await sushiRouter.getAddress(), INITIAL_TOKEN_AMOUNT);

    await owner.sendTransaction({
      to: await sushiRouter.getAddress(),
      value: ONE_ETH * 2n,
    });

    // Ensure vault has at least 10,000 ETH for consistent testing
    const vaultBalance = await ethers.provider.getBalance(vault.address);
    if (vaultBalance < ethers.parseEther("10000")) {
      await owner.sendTransaction({
        to: vault.address,
        value: ethers.parseEther("10000") - vaultBalance,
      });
    }

    const userToken1Bal = await token1.balanceOf(user.address);
    const userToken2Bal = await token2.balanceOf(user.address);
    const sushiRouterToken1Bal = await token1.balanceOf(
      await sushiRouter.getAddress(),
    );
    const sushiRouterToken2Bal = await token2.balanceOf(
      await sushiRouter.getAddress(),
    );
    const vaultToken1Bal = await token1.balanceOf(vault.address);
    const vaultToken2Bal = await token2.balanceOf(vault.address);

    expect(userToken1Bal).to.equal(INITIAL_TOKEN_AMOUNT);
    expect(userToken2Bal).to.equal(0);
    expect(sushiRouterToken1Bal).to.equal(INITIAL_TOKEN_AMOUNT);
    expect(sushiRouterToken2Bal).to.equal(INITIAL_TOKEN_AMOUNT);
    expect(vaultToken1Bal).to.equal(0);
    expect(vaultToken2Bal).to.equal(0);
  });

  async function checkBalances(
    expectedUserToken1,
    expectedUserToken2,
    expectedSushiRouterEth,
    expectedSushiRouterToken1,
    expectedSushiRouterToken2,
    expectedVaultToken1,
    expectedVaultToken2,
  ) {
    const userToken1Bal = await token1.balanceOf(user.address);
    const userToken2Bal = await token2.balanceOf(user.address);
    const userEthBal = await ethers.provider.getBalance(user.address);
    const sushiRouterEthBal = await ethers.provider.getBalance(
      await sushiRouter.getAddress(),
    );
    const sushiRouterToken1Bal = await token1.balanceOf(
      await sushiRouter.getAddress(),
    );
    const sushiRouterToken2Bal = await token2.balanceOf(
      await sushiRouter.getAddress(),
    );
    const vaultEthBal = await ethers.provider.getBalance(vault.address);
    const vaultToken1Bal = await token1.balanceOf(vault.address);
    const vaultToken2Bal = await token2.balanceOf(vault.address);

    console.log(`User ETH: ${ethers.formatEther(userEthBal)} ETH`);
    console.log(`Vault ETH: ${ethers.formatEther(vaultEthBal)} ETH`);
    console.log(
      `SushiRouter ETH: ${ethers.formatEther(sushiRouterEthBal)} ETH`,
    );

    expect(sushiRouterEthBal).to.equal(expectedSushiRouterEth);
    expect(userEthBal).to.be.gt(0);
    expect(vaultEthBal).to.be.gt(ethers.parseEther("10000"));

    expect(userToken1Bal).to.equal(expectedUserToken1);
    expect(userToken2Bal).to.equal(expectedUserToken2);
    expect(sushiRouterToken1Bal).to.equal(expectedSushiRouterToken1);
    expect(sushiRouterToken2Bal).to.equal(expectedSushiRouterToken2);
    expect(vaultToken1Bal).to.equal(expectedVaultToken1);
    expect(vaultToken2Bal).to.equal(expectedVaultToken2);
  }

  describe("Happy Path Tests", function () {
    it("1.1 SwapIn() - Token to ETH Success", async function () {
      await token1
        .connect(user)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const deadline = Math.floor(Date.now() / 1000) + 3600;
      const tx = await aggregator.connect(user).swapIn(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_TOKEN_TO_ETH, // tcMemo
        await token1.getAddress(), // fromToken
        ONE_TOKEN, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        deadline, // deadline
      );

      await expect(tx).to.emit(router, "Deposit").withArgs(
        vault.address, // to
        ETH_ADDRESS, // asset
        ONE_ETH, // amount (should be 1 ETH after swap)
        MEMO_TOKEN_TO_ETH, // memo
      );

      await checkBalances(
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // user: 99 token1 (spent 1)
        0n, // user: 0 token2
        ONE_ETH, // sushiRouter: 1 ETH (2 initial - 1 spent)
        INITIAL_TOKEN_AMOUNT + ONE_TOKEN, // sushiRouter: 101 token1 (100 initial + 1 received)
        INITIAL_TOKEN_AMOUNT, // sushiRouter: 100 token2 (no change)
        0n, // vault: 0 token1
        0n, // vault: 0 token2
      );
    });

    it("1.2 SwapInV2() - Token to ETH Success", async function () {
      await token1
        .connect(user)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const deadline = Math.floor(Date.now() / 1000) + 3600;
      const tx = await aggregator.connect(user).swapInV2(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_TOKEN_TO_ETH, // tcMemo
        await token1.getAddress(), // fromToken
        ETH_ADDRESS, // toToken (ETH address)
        ONE_TOKEN, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        deadline, // deadline
      );

      await expect(tx).to.emit(router, "Deposit").withArgs(
        vault.address, // to
        ETH_ADDRESS, // asset
        ONE_ETH, // amount (should be 1 ETH after swap)
        MEMO_TOKEN_TO_ETH, // memo
      );

      await checkBalances(
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // user: 99 token1 (spent 1)
        0n, // user: 0 token2
        ONE_ETH, // sushiRouter: 1 ETH (2 initial - 1 spent)
        INITIAL_TOKEN_AMOUNT + ONE_TOKEN, // sushiRouter: 101 token1 (100 initial + 1 received)
        INITIAL_TOKEN_AMOUNT, // sushiRouter: 100 token2 (no change)
        0n, // vault: 0 token1
        0n, // vault: 0 token2
      );
    });

    it("1.3 SwapInV2() - Token to Token Success", async function () {
      await token1
        .connect(user)
        .approve(await aggregator.getAddress(), ONE_TOKEN);

      const deadline = Math.floor(Date.now() / 1000) + 3600;
      const tx = await aggregator.connect(user).swapInV2(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_TOKEN_TO_TOKEN, // tcMemo
        await token1.getAddress(), // fromToken
        await token2.getAddress(), // toToken
        ONE_TOKEN, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        deadline, // deadline
      );

      await expect(tx)
        .to.emit(router, "Deposit")
        .withArgs(
          vault.address, // to
          await token2.getAddress(), // asset
          ONE_TOKEN, // amount (should be 1 token2 after swap)
          MEMO_TOKEN_TO_TOKEN, // memo
        );

      await checkBalances(
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // user: 99 token1 (spent 1)
        0n, // user: 0 token2
        ONE_ETH * 2n, // sushiRouter: 2 ETH (no change in token-to-token)
        INITIAL_TOKEN_AMOUNT + ONE_TOKEN, // sushiRouter: 101 token1 (100 initial + 1 received)
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // sushiRouter: 99 token2 (100 initial - 1 sent)
        0n, // vault: 0 token1
        ONE_TOKEN, // vault: 1 token2 (received from deposit)
      );
    });

    it("1.4 SwapInV2() - ETH to Token Success", async function () {
      const userInitialEthBal = await ethers.provider.getBalance(user.address);

      const deadline = Math.floor(Date.now() / 1000) + 3600;
      const MEMO_ETH_TO_TOKEN = "swap:ETH.ETH:TOKEN2";

      const tx = await aggregator.connect(user).swapInV2(
        vault.address, // tcVault
        await router.getAddress(), // tcRouter
        MEMO_ETH_TO_TOKEN, // tcMemo
        ETH_ADDRESS, // fromToken (ETH)
        await token2.getAddress(), // toToken
        ONE_ETH, // amount
        MIN_AMOUNT_OUT, // amountOutMin
        deadline, // deadline
        { value: ONE_ETH }, // Send 1 ETH with the transaction
      );

      await expect(tx)
        .to.emit(router, "Deposit")
        .withArgs(
          vault.address, // to
          await token2.getAddress(), // asset
          ONE_TOKEN, // amount (should be 1 token2 after swap)
          MEMO_ETH_TO_TOKEN, // memo
        );

      const userFinalEthBal = await ethers.provider.getBalance(user.address);
      const ethSpent = userInitialEthBal - userFinalEthBal;

      console.log(
        `User spent ${ethers.formatEther(ethSpent)} ETH (including 1 ETH for swap and gas costs)`,
      );

      expect(ethSpent).to.be.gt(ONE_ETH);

      await checkBalances(
        INITIAL_TOKEN_AMOUNT, // user: 100 token1 (no change)
        0n, // user: 0 token2
        ONE_ETH * 3n, // sushiRouter: 3 ETH (2 initial + 1 received)
        INITIAL_TOKEN_AMOUNT, // sushiRouter: 100 token1 (no change)
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // sushiRouter: 99 token2 (100 initial - 1 sent)
        0n, // vault: 0 token1
        ONE_TOKEN, // vault: 1 token2 (received from deposit)
      );
    });
  });
});
