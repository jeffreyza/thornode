const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("THORChain Router Aggregation Tests", function () {
  let router, aggregator, mockRevertingAggregator, token, sushiRouter;
  let owner, vault, user;

  const ONE_ETH = ethers.parseEther("1");
  const MIN_AMOUNT_OUT = ethers.parseEther("0.1");
  const ETH_ADDRESS = "0x0000000000000000000000000000000000000000";
  const MEMO = "swap:ETH.ETH:TOKEN";
  const INITIAL_TOKEN_AMOUNT = ethers.parseEther("100");
  const ONE_TOKEN = ethers.parseEther("1");

  beforeEach(async function () {
    [owner, vault, user] = await ethers.getSigners();

    const Token = await ethers.getContractFactory("ERC20Token");
    token = await Token.deploy();
    await token.waitForDeployment();

    const SushiRouterSmol = await ethers.getContractFactory("SushiRouterSmol");
    sushiRouter = await SushiRouterSmol.deploy();
    await sushiRouter.waitForDeployment();

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    const Aggregator = await ethers.getContractFactory("THORChain_Aggregator");
    aggregator = await Aggregator.deploy(await sushiRouter.getAddress());
    await aggregator.waitForDeployment();

    const MockRevertingAggregator = await ethers.getContractFactory(
      "Reverting_Aggregator",
    );
    mockRevertingAggregator = await MockRevertingAggregator.deploy(
      await sushiRouter.getAddress(),
    );
    await mockRevertingAggregator.waitForDeployment();

    await token.transfer(await sushiRouter.getAddress(), INITIAL_TOKEN_AMOUNT);
    await owner.sendTransaction({
      to: vault.address,
      value: ethers.parseEther("100"),
    });
  });

  async function checkBalances(
    expectedVaultEth,
    expectedSushiRouterEth,
    expectedUserEth,
    expectedVaultToken,
    expectedSushiRouterToken,
    expectedUserToken,
  ) {
    const vaultEthBal = await ethers.provider.getBalance(vault.address);
    const sushiRouterEthBal = await ethers.provider.getBalance(
      await sushiRouter.getAddress(),
    );
    const userEthBal = await ethers.provider.getBalance(user.address);

    const vaultTokenBal = await token.balanceOf(vault.address);
    const sushiRouterTokenBal = await token.balanceOf(
      await sushiRouter.getAddress(),
    );
    const userTokenBal = await token.balanceOf(user.address);

    expect(vaultEthBal).to.be.gt(ethers.parseEther("9990"));
    expect(sushiRouterEthBal).to.equal(expectedSushiRouterEth);
    expect(userEthBal).to.be.gte(ethers.parseEther("10000"));

    expect(vaultTokenBal).to.equal(expectedVaultToken);
    expect(sushiRouterTokenBal).to.equal(expectedSushiRouterToken);
    expect(userTokenBal).to.equal(expectedUserToken);
  }

  describe("Happy Path Tests", function () {
    it("1.1 transferOutAndCall() - ETH to Token Success", async function () {
      const tx = await router
        .connect(vault)
        .transferOutAndCall(
          await aggregator.getAddress(),
          await token.getAddress(),
          user.address,
          MIN_AMOUNT_OUT,
          MEMO,
          { value: ONE_ETH },
        );

      await expect(tx)
        .to.emit(router, "TransferOutAndCall")
        .withArgs(
          vault.address,
          await aggregator.getAddress(),
          ONE_ETH,
          await token.getAddress(),
          user.address,
          MIN_AMOUNT_OUT,
          MEMO,
        );

      await checkBalances(
        0n, // expectedVaultEth
        ONE_ETH, // expectedSushiRouterEth
        0n, // expectedUserEth
        0n, // expectedVaultToken
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // expectedSushiRouterToken
        ONE_TOKEN, // expectedUserToken
      );
    });
  });

  describe("Failure Path Tests", function () {
    it("2.1 Invalid Target Contract", async function () {
      const tx = await router
        .connect(vault)
        .transferOutAndCall(
          await token.getAddress(),
          await token.getAddress(),
          user.address,
          MIN_AMOUNT_OUT,
          MEMO,
          { value: ONE_ETH },
        );

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          await token.getAddress(),
          ONE_ETH,
          MEMO,
        );

      await checkBalances(
        0n, // expectedVaultEth
        0n, // expectedSushiRouterEth
        0n, // expectedUserEth
        0n, // expectedVaultToken
        INITIAL_TOKEN_AMOUNT, // expectedSushiRouterToken
        0n, // expectedUserToken
      );
    });

    it("2.2 Target Function Reverts", async function () {
      const tx = await router
        .connect(vault)
        .transferOutAndCall(
          await mockRevertingAggregator.getAddress(),
          await token.getAddress(),
          user.address,
          MIN_AMOUNT_OUT,
          MEMO,
          { value: ONE_ETH },
        );

      await expect(tx)
        .to.emit(router, "TransferFailed")
        .withArgs(
          vault.address,
          user.address,
          await token.getAddress(),
          ONE_ETH,
          MEMO,
        );

      await checkBalances(
        0n, // expectedVaultEth
        0n, // expectedSushiRouterEth
        0n, // expectedUserEth
        0n, // expectedVaultToken
        INITIAL_TOKEN_AMOUNT, // expectedSushiRouterToken
        0n, // expectedUserToken
      );
    });
  });

  describe("Edge Case Tests", function () {
    it("should handle zero amountOutMin", async function () {
      const tx = await router
        .connect(vault)
        .transferOutAndCall(
          await aggregator.getAddress(),
          await token.getAddress(),
          user.address,
          0,
          MEMO,
          { value: ONE_ETH },
        );

      await expect(tx)
        .to.emit(router, "TransferOutAndCall")
        .withArgs(
          vault.address,
          await aggregator.getAddress(),
          ONE_ETH,
          await token.getAddress(),
          user.address,
          0,
          MEMO,
        );

      await checkBalances(
        0n, // expectedVaultEth
        ONE_ETH, // expectedSushiRouterEth
        0n, // expectedUserEth
        0n, // expectedVaultToken
        INITIAL_TOKEN_AMOUNT - ONE_TOKEN, // expectedSushiRouterToken
        ONE_TOKEN, // expectedUserToken
      );
    });
  });
});
