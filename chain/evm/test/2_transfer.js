const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("TC:Transfers", function () {
  let router, standardToken, usdt, owner, user;

  beforeEach(async function () {
    [owner, user] = await ethers.getSigners();

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

    await usdt.setParams(1000, ethers.parseUnits("1", 6));

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    await standardToken.transfer(user.address, ethers.parseEther("100"));
    await usdt.transfer(user.address, ethers.parseUnits("100", 6));
  });

  describe("transferOut", function () {
    it("should transfer USDT to a recipient, handling transfer fees correctly", async function () {
      const amountUSDT = ethers.parseUnits("10", 6);
      const memo = "MEMO:USDT_FEE_TRANSFER";
      const initialUser1UsdtBalance = await usdt.balanceOf(user.address);
      const initialVaultUsdtBalance = await usdt.balanceOf(owner.address);

      const feeBasisPoints = await usdt.basisPointsRate();
      const expectedFee = (amountUSDT * BigInt(feeBasisPoints)) / 10000n;
      const expectedTransferAmount = amountUSDT - expectedFee;

      await usdt.connect(owner).approve(router.target, amountUSDT);

      const tx = await router
        .connect(owner)
        .transferOut(user.address, usdt.target, amountUSDT, memo);

      const receipt = await tx.wait();
      const transferOutEvent = receipt.logs
        .filter((log) => log.fragment && log.fragment.name === "TransferOut")
        .map((log) => router.interface.parseLog(log))[0];

      const actualAmountTransferred = transferOutEvent.args[3];

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          owner.address,
          user.address,
          usdt.target,
          actualAmountTransferred,
          memo,
        );

      const finalUser1UsdtBalance = await usdt.balanceOf(user.address);
      const finalVaultUsdtBalance = await usdt.balanceOf(owner.address);

      expect(finalUser1UsdtBalance - initialUser1UsdtBalance).to.equal(
        expectedTransferAmount,
      );
      expect(initialVaultUsdtBalance - finalVaultUsdtBalance).to.equal(
        expectedTransferAmount,
      );
      expect(actualAmountTransferred).to.be.oneOf([amountUSDT]);
    });

    it("should transfer ETH to a recipient", async function () {
      const amountETH = ethers.parseEther("1");
      const memo = "MEMO:ETH_TRANSFER";
      const initialUser1Balance = await ethers.provider.getBalance(
        user.address,
      );

      const tx = await router
        .connect(owner)
        .transferOut(user.address, ethers.ZeroAddress, amountETH, memo, {
          value: amountETH,
        });

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          owner.address,
          user.address,
          ethers.ZeroAddress,
          amountETH,
          memo,
        );

      const finalBalance = await ethers.provider.getBalance(user.address);
      expect(finalBalance - initialUser1Balance).to.equal(amountETH);
    });

    it("should transfer standard ERC20 to a recipient", async function () {
      const amountTKN = ethers.parseEther("10");
      const memo = "MEMO:TKN_TRANSFER";
      const initialUser1TknBalance = await standardToken.balanceOf(
        user.address,
      );
      const initialVaultTknBalance = await standardToken.balanceOf(
        owner.address,
      );

      await standardToken.connect(owner).approve(router.target, amountTKN);

      const tx = await router
        .connect(owner)
        .transferOut(user.address, standardToken.target, amountTKN, memo);

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          owner.address,
          user.address,
          standardToken.target,
          amountTKN,
          memo,
        );

      expect(await standardToken.balanceOf(user.address)).to.equal(
        initialUser1TknBalance + amountTKN,
      );
      expect(await standardToken.balanceOf(owner.address)).to.equal(
        initialVaultTknBalance - amountTKN,
      );
    });

    it("should transfer USDT to a recipient, handling fees if applicable", async function () {
      const amountUSDT = ethers.parseUnits("10", 6);
      const memo = "MEMO:USDT_TRANSFER";
      const initialUser1UsdtBalance = await usdt.balanceOf(user.address);
      const initialVaultUsdtBalance = await usdt.balanceOf(owner.address);

      await usdt.connect(owner).approve(router.target, 0);
      await usdt.connect(owner).approve(router.target, amountUSDT);

      const tx = await router
        .connect(owner)
        .transferOut(user.address, usdt.target, amountUSDT, memo);

      const receipt = await tx.wait();
      const transferOutEvent = receipt.logs
        .filter((log) => log.fragment && log.fragment.name === "TransferOut")
        .map((log) => router.interface.parseLog(log))[0];

      const actualAmountTransferred = transferOutEvent.args[3];

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          owner.address,
          user.address,
          usdt.target,
          actualAmountTransferred,
          memo,
        );

      const feeBasisPoints = await usdt.basisPointsRate();
      const expectedFee = (amountUSDT * BigInt(feeBasisPoints)) / 10000n;
      const expectedTransferAmount = amountUSDT - expectedFee;

      expect(await usdt.balanceOf(user.address)).to.equal(
        initialUser1UsdtBalance + expectedTransferAmount,
      );
      expect(await usdt.balanceOf(owner.address)).to.be.lessThan(
        initialVaultUsdtBalance,
      );
    });
  });

  describe("Input Validation", function () {
    it("should revert when requested amount doesn't match provided ETH", async function () {
      const providedAmount = ethers.parseEther("1");
      const requestedAmount = ethers.parseEther("2");
      const memo = "MEMO:ETH_VALIDATION";

      // Should revert when requested amount is higher than provided
      await expect(
        router
          .connect(owner)
          .transferOut(
            user.address,
            ethers.ZeroAddress,
            requestedAmount,
            memo,
            {
              value: providedAmount,
            },
          ),
      ).to.be.revertedWith("TC:eth amount mismatch");

      // Should revert when requested amount is lower than provided
      await expect(
        router
          .connect(owner)
          .transferOut(user.address, ethers.ZeroAddress, providedAmount, memo, {
            value: requestedAmount,
          }),
      ).to.be.revertedWith("TC:eth amount mismatch");
    });

    it("should revert when no token approval is provided", async function () {
      const amountTKN = ethers.parseEther("100");
      const memo = "MEMO:NO_APPROVAL";

      await standardToken.connect(owner).approve(router.target, 0);

      await expect(
        router
          .connect(owner)
          .transferOut(user.address, standardToken.target, amountTKN, memo),
      ).to.be.revertedWith("TC:transfer failed");
    });

    it("should revert when token approval is insufficient", async function () {
      const approvedAmount = ethers.parseEther("50");
      const transferAmount = ethers.parseEther("100");
      const memo = "MEMO:INSUFFICIENT_APPROVAL";

      await standardToken.connect(owner).approve(router.target, approvedAmount);

      await expect(
        router
          .connect(owner)
          .transferOut(
            user.address,
            standardToken.target,
            transferAmount,
            memo,
          ),
      ).to.be.revertedWith("TC:transfer failed");
    });
  });

  describe("Edge Cases", function () {
    it("should revert when msg.value doesn't match amount for ETH transfers", async function () {
      const amountETH = ethers.parseEther("1");
      const excessETH = ethers.parseEther("0.5");
      const totalETH = amountETH + excessETH;
      const memo = "MEMO:ETH_EXCESS";

      // Should revert when msg.value is higher than amount
      await expect(
        router
          .connect(owner)
          .transferOut(user.address, ethers.ZeroAddress, amountETH, memo, {
            value: totalETH,
          }),
      ).to.be.revertedWith("TC:eth amount mismatch");

      // Should also revert when msg.value is lower than amount
      await expect(
        router
          .connect(owner)
          .transferOut(user.address, ethers.ZeroAddress, totalETH, memo, {
            value: amountETH,
          }),
      ).to.be.revertedWith("TC:eth amount mismatch");
    });
  });
});
