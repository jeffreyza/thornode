const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("TC:depositWithExpiry", function () {
  let router, standardToken, usdt, owner, user, vault;
  let expiryFuture;

  beforeEach(async function () {
    [owner, user, vault] = await ethers.getSigners();

    const Token = await ethers.getContractFactory("ERC20Token");
    standardToken = await Token.deploy();
    await standardToken.waitForDeployment();

    const USDT = await ethers.getContractFactory("TetherToken");
    usdt = await USDT.deploy(
      ethers.parseUnits("1000", 6),
      "Tether USD",
      "USDT",
      6,
    );
    await usdt.waitForDeployment();

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    await standardToken.transfer(user.address, ethers.parseEther("100"));
    await usdt.transfer(user.address, ethers.parseUnits("100", 6));

    await usdt.setParams(1000, ethers.parseUnits("1", 6));

    const latestBlock = await ethers.provider.getBlock("latest");
    expiryFuture = latestBlock.timestamp + 1000;
  });

  it("should allow ETH deposit", async function () {
    const amount = ethers.parseEther("1");
    const memo = "TEST:ETH";

    const initialBalance = await ethers.provider.getBalance(vault.address);

    const tx = await router
      .connect(user)
      .depositWithExpiry(
        vault.address,
        ethers.ZeroAddress,
        amount,
        memo,
        expiryFuture,
        {
          value: amount,
        },
      );

    await expect(tx)
      .to.emit(router, "Deposit")
      .withArgs(vault.address, ethers.ZeroAddress, amount, memo);

    const finalBalance = await ethers.provider.getBalance(vault.address);
    expect(finalBalance - initialBalance).to.equal(amount);
  });

  it("should allow standard ERC20 deposit with no expiration", async function () {
    const amount = ethers.parseEther("10");
    const memo = "TEST:TKN";

    await standardToken.connect(user).approve(router.target, amount);

    const tx = await router
      .connect(user)
      .depositWithExpiry(vault.address, standardToken.target, amount, memo, 0);

    await expect(tx)
      .to.emit(router, "Deposit")
      .withArgs(vault.address, standardToken.target, amount, memo);

    expect(await standardToken.balanceOf(vault.address)).to.equal(amount);
    expect(await standardToken.balanceOf(user.address)).to.equal(
      ethers.parseEther("90"),
    );
  });

  it("should allow USDT deposit, handling fees", async function () {
    const amount = ethers.parseUnits("10", 6);
    const memo = "TEST:USDT";
    await usdt.connect(user).approve(router.target, 0);
    await usdt.connect(user).approve(router.target, amount);

    const tx = await router
      .connect(user)
      .depositWithExpiry(
        vault.address,
        usdt.target,
        amount,
        memo,
        expiryFuture,
      );

    const receipt = await tx.wait();
    const depositEvent = receipt.logs
      .filter((log) => log.fragment && log.fragment.name === "Deposit")
      .map((log) => router.interface.parseLog(log))[0];

    const actualAmountInEvent = depositEvent.args[2];

    await expect(tx).to.emit(router, "Deposit");

    expect(depositEvent.args[0]).to.equal(vault.address);
    expect(depositEvent.args[1]).to.equal(usdt.target);
    expect(actualAmountInEvent).to.equal(ethers.parseUnits("9", 6));
    expect(depositEvent.args[3]).to.equal(memo);

    expect(await usdt.balanceOf(vault.address)).to.equal(
      ethers.parseUnits("9", 6),
    );
    expect(await usdt.balanceOf(user.address)).to.equal(
      ethers.parseUnits("90", 6),
    );
  });

  it("should revert when depositWithExpiry is called with expired timestamp", async function () {
    const amount = ethers.parseEther("1");
    const memo = "TEST:EXPIRED";

    const latestBlock = await ethers.provider.getBlock("latest");
    const expiryPast = latestBlock.timestamp - 100;

    await expect(
      router
        .connect(user)
        .depositWithExpiry(
          vault.address,
          ethers.ZeroAddress,
          0,
          memo,
          expiryPast,
          { value: amount },
        ),
    ).to.be.revertedWith("TC:expired");
  });

  it("should revert when sending ETH with ERC20 deposit", async function () {
    const tokenAmount = ethers.parseEther("1");
    const ethAmount = ethers.parseEther("0.1");
    const memo = "TEST:INVALID:ETH";

    await standardToken.connect(user).approve(router.target, tokenAmount);

    await expect(
      router
        .connect(user)
        .depositWithExpiry(
          vault.address,
          standardToken.target,
          tokenAmount,
          memo,
          expiryFuture,
          { value: ethAmount },
        ),
    ).to.be.revertedWith("TC:unexpected eth");
  });

  it("should revert when ERC20 transferFrom fails (no approval)", async function () {
    const amount = ethers.parseEther("5");
    const memo = "TEST:NOAPPROVAL";

    await standardToken.connect(user).approve(router.target, 0);

    await expect(
      router
        .connect(user)
        .depositWithExpiry(
          vault.address,
          standardToken.target,
          amount,
          memo,
          expiryFuture,
        ),
    ).to.be.reverted;
  });

  it("should revert when vault is the router contract itself", async function () {
    const amount = ethers.parseEther("1");
    const memo = "TEST:ROUTER:VAULT";

    await expect(
      router
        .connect(user)
        .depositWithExpiry(
          router.target,
          ethers.ZeroAddress,
          0,
          memo,
          expiryFuture,
          { value: amount },
        ),
    ).to.be.revertedWith("TC:vault!=router");

    await standardToken.connect(user).approve(router.target, amount);

    await expect(
      router
        .connect(user)
        .depositWithExpiry(
          router.target,
          standardToken.target,
          amount,
          memo,
          expiryFuture,
        ),
    ).to.be.revertedWith("TC:vault!=router");
  });
});
