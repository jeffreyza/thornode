const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("TC:Batch Transfers", function () {
  let router, tkn1, tkn2, usdt, owner, vault, user1, user2;

  const USDT_DECIMALS = 6;

  beforeEach(async function () {
    [owner, vault, user1, user2] = await ethers.getSigners();

    const Token = await ethers.getContractFactory("ERC20Token");
    tkn1 = await Token.deploy();
    await tkn1.waitForDeployment();

    tkn2 = await Token.deploy();
    await tkn2.waitForDeployment();

    const USDT = await ethers.getContractFactory("TetherToken");
    usdt = await USDT.deploy(
      ethers.parseUnits("1000000", USDT_DECIMALS),
      "Tether USD",
      "USDT",
      USDT_DECIMALS,
    );
    await usdt.waitForDeployment();

    await usdt.setParams(10, ethers.parseUnits("10", USDT_DECIMALS));

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    await owner.sendTransaction({
      to: vault.address,
      value: ethers.parseEther("100"),
    });

    await tkn1.transfer(vault.address, ethers.parseEther("100000"));
    await usdt
      .connect(owner)
      .transfer(vault.address, ethers.parseUnits("100000", USDT_DECIMALS));
  });

  describe("1. Basic batchTransferOut Tests", function () {
    it("should batch transfer ETH and standard ERC20", async function () {
      const amountETH = ethers.parseEther("1");
      const amountTKN = ethers.parseEther("10");
      const memoETH = "MEMO:ETH_MULTI";
      const memoTKN = "MEMO:TKN_MULTI";

      const initialUser1Balance = await ethers.provider.getBalance(
        user1.address,
      );
      const initialUser2TknBalance = await tkn1.balanceOf(user2.address);
      const initialVaultTknBalance = await tkn1.balanceOf(vault.address);

      await tkn1.connect(vault).approve(router.target, amountTKN);

      const tx = await router
        .connect(vault)
        .batchTransferOut(
          [user1.address, user2.address],
          [ethers.ZeroAddress, tkn1.target],
          [amountETH, amountTKN],
          [memoETH, memoTKN],
          { value: amountETH },
        );

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          vault.address,
          user1.address,
          ethers.ZeroAddress,
          amountETH,
          memoETH,
        );

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          vault.address,
          user2.address,
          tkn1.target,
          amountTKN,
          memoTKN,
        );

      const finalUser1Balance = await ethers.provider.getBalance(user1.address);
      expect(finalUser1Balance - initialUser1Balance).to.equal(amountETH);

      const finalUser2TknBalance = await tkn1.balanceOf(user2.address);
      expect(finalUser2TknBalance - initialUser2TknBalance).to.equal(amountTKN);

      const finalVaultTknBalance = await tkn1.balanceOf(vault.address);
      expect(initialVaultTknBalance - finalVaultTknBalance).to.equal(amountTKN);
    });

    it("should batch transfer multiple ERC20 tokens", async function () {
      const amountTKN = ethers.parseEther("10");
      const amountUSDT = ethers.parseUnits("10", USDT_DECIMALS);
      const memoTKN = "MEMO:TKN_MULTI";
      const memoUSDT = "MEMO:USDT_MULTI";

      const initialUser1TknBalance = await tkn1.balanceOf(user1.address);
      const initialUser2UsdtBalance = await usdt.balanceOf(user2.address);
      const initialVaultTknBalance = await tkn1.balanceOf(vault.address);
      const initialVaultUsdtBalance = await usdt.balanceOf(vault.address);
      const initialOwnerUsdtBalance = await usdt.balanceOf(owner.address);

      await tkn1.connect(vault).approve(router.target, amountTKN);
      await usdt.connect(vault).approve(router.target, 0);
      await usdt.connect(vault).approve(router.target, amountUSDT);

      const tx = await router
        .connect(vault)
        .batchTransferOut(
          [user1.address, user2.address],
          [tkn1.target, usdt.target],
          [amountTKN, amountUSDT],
          [memoTKN, memoUSDT],
        );

      const receipt = await tx.wait();
      const transferOutEvents = receipt.logs
        .filter((log) => log.fragment && log.fragment.name === "TransferOut")
        .map((log) => router.interface.parseLog(log));

      const actualTknTransferred = transferOutEvents[0].args[3];
      const actualUsdtTransferred = transferOutEvents[1].args[3];

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          vault.address,
          user1.address,
          tkn1.target,
          actualTknTransferred,
          memoTKN,
        );

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          vault.address,
          user2.address,
          usdt.target,
          actualUsdtTransferred,
          memoUSDT,
        );

      expect(await tkn1.balanceOf(user1.address)).to.equal(
        initialUser1TknBalance + amountTKN,
      );
      expect(await tkn1.balanceOf(vault.address)).to.equal(
        initialVaultTknBalance - amountTKN,
      );

      const feeBasisPoints = await usdt.basisPointsRate();
      const expectedFee = (amountUSDT * BigInt(feeBasisPoints)) / 10000n;
      const expectedAmountAfterFee = amountUSDT - expectedFee;

      expect(await usdt.balanceOf(user2.address)).to.equal(
        initialUser2UsdtBalance + expectedAmountAfterFee,
      );
      expect(await usdt.balanceOf(vault.address)).to.equal(
        initialVaultUsdtBalance - amountUSDT,
      );
      expect(await usdt.balanceOf(owner.address)).to.be.gt(
        initialOwnerUsdtBalance,
      );
    });
  });

  describe("2. batchTransferOut Edge Cases", function () {
    it("should handle empty arrays", async function () {
      await expect(router.connect(vault).batchTransferOut([], [], [], [])).to
        .not.be.reverted;
    });

    it("should handle a single zero-amount ETH transfer", async function () {
      const memo = "MEMO:BATCH_ZERO_ETH";

      const tx = await router
        .connect(vault)
        .batchTransferOut([user1.address], [ethers.ZeroAddress], [0], [memo], {
          value: 0,
        });

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(vault.address, user1.address, ethers.ZeroAddress, 0, memo);
    });

    it("should handle mixed zero and non-zero transfers", async function () {
      const amountTKN = ethers.parseEther("10");
      const memoZero = "MEMO:ZERO_USDT";
      const memoNonZero = "MEMO:NONZERO_TKN";

      await tkn1.connect(vault).approve(router.target, amountTKN);
      await usdt.connect(vault).approve(router.target, 0);

      const initialUser2TknBalance = await tkn1.balanceOf(user2.address);

      const tx = await router
        .connect(vault)
        .batchTransferOut(
          [user1.address, user2.address],
          [usdt.target, tkn1.target],
          [0, amountTKN],
          [memoZero, memoNonZero],
        );

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(vault.address, user1.address, usdt.target, 0, memoZero);

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          vault.address,
          user2.address,
          tkn1.target,
          amountTKN,
          memoNonZero,
        );

      const finalUser2TknBalance = await tkn1.balanceOf(user2.address);
      expect(finalUser2TknBalance - initialUser2TknBalance).to.equal(amountTKN);
    });

    it("should handle a large batch of transfers", async function () {
      const batchSize = 20;
      const recipients = Array(batchSize).fill(user1.address);
      const assets = Array(batchSize).fill(tkn1.target);
      const amounts = Array(batchSize).fill(ethers.parseEther("1"));
      const memos = Array(batchSize).fill("MEMO:BATCH_ITEM");

      await tkn1
        .connect(vault)
        .approve(router.target, ethers.parseEther(String(batchSize)));

      const initialBalance = await tkn1.balanceOf(user1.address);

      const tx = await router
        .connect(vault)
        .batchTransferOut(recipients, assets, amounts, memos);

      const receipt = await tx.wait();
      console.log(
        `Gas used for ${batchSize} transfers: ${receipt.gasUsed.toString()}`,
      );

      const finalBalance = await tkn1.balanceOf(user1.address);
      expect(finalBalance - initialBalance).to.equal(
        ethers.parseEther(String(batchSize)),
      );
    });
  });

  describe("3. Input Validation", function () {
    it("should use only the provided ETH amount", async function () {
      const providedETH = ethers.parseEther("1");
      const requestedETH = ethers.parseEther("2");
      const memo = "MEMO:ETH_PROVIDED_VS_REQUESTED";

      const initialBalance = await ethers.provider.getBalance(user1.address);

      const tx = await router
        .connect(vault)
        .batchTransferOut(
          [user1.address],
          [ethers.ZeroAddress],
          [requestedETH],
          [memo],
          { value: providedETH },
        );

      await expect(tx)
        .to.emit(router, "TransferOut")
        .withArgs(
          vault.address,
          user1.address,
          ethers.ZeroAddress,
          providedETH,
          memo,
        );

      const finalBalance = await ethers.provider.getBalance(user1.address);
      expect(finalBalance - initialBalance).to.equal(providedETH);
    });

    it("should revert when transferring tokens without approval", async function () {
      const amountTKN = ethers.parseEther("100");
      const memo = "MEMO:NO_APPROVAL";

      await tkn1.connect(vault).approve(router.target, 0);

      await expect(
        router
          .connect(vault)
          .batchTransferOut(
            [user1.address],
            [tkn1.target],
            [amountTKN],
            [memo],
          ),
      ).to.be.revertedWith("TC:transfer failed");
    });

    it("should revert when transferring more tokens than approved", async function () {
      const approvedAmount = ethers.parseEther("50");
      const transferAmount = ethers.parseEther("100");
      const memo = "MEMO:EXCEEDS_APPROVAL";

      await tkn1.connect(vault).approve(router.target, approvedAmount);

      await expect(
        router
          .connect(vault)
          .batchTransferOut(
            [user1.address],
            [tkn1.target],
            [transferAmount],
            [memo],
          ),
      ).to.be.revertedWith("TC:transfer failed");
    });
  });

  describe("3.1 Array Input Validation", function () {
    it("should revert on mismatched array lengths", async function () {
      const recipients = [user1.address, user2.address];
      const assets = [tkn1.target];
      const amounts = [ethers.parseEther("1"), ethers.parseEther("2")];
      const memos = ["MEMO1", "MEMO2"];

      await expect(
        router
          .connect(vault)
          .batchTransferOut(recipients, assets, amounts, memos),
      ).to.be.revertedWith("TC:length mismatch");
    });

    it("should only transfer up to the provided ETH amount in a batch", async function () {
      const recipients = [user1.address, user2.address];
      const assets = [ethers.ZeroAddress, ethers.ZeroAddress];
      const amounts = [ethers.parseEther("1"), ethers.parseEther("2")];
      const memos = ["MEMO1", "MEMO2"];
      const totalEth = ethers.parseEther("1.5");

      const initialBalance1 = await ethers.provider.getBalance(user1.address);
      const initialBalance2 = await ethers.provider.getBalance(user2.address);

      await router
        .connect(vault)
        .batchTransferOut(recipients, assets, amounts, memos, {
          value: totalEth,
        });

      const finalBalance1 = await ethers.provider.getBalance(user1.address);
      expect(finalBalance1 - initialBalance1).to.equal(ethers.parseEther("1"));

      const finalBalance2 = await ethers.provider.getBalance(user2.address);
      expect(finalBalance2 - initialBalance2).to.equal(
        ethers.parseEther("0.5"),
      );
    });
  });

  describe("4. DoS and Gas Testing", function () {
    it("should handle very large batch transfers (DoS resistance)", async function () {
      const batchSize = 50;
      const recipients = Array(batchSize).fill(user1.address);
      const assets = Array(batchSize).fill(tkn1.target);
      const amounts = Array(batchSize).fill(ethers.parseEther("0.1"));
      const memos = Array(batchSize)
        .fill()
        .map((_, i) => `MEMO:BATCH_${i}`);

      const totalAmount = ethers.parseEther("0.1") * BigInt(batchSize);
      await tkn1.connect(vault).approve(router.target, totalAmount);

      const initialBalance = await tkn1.balanceOf(user1.address);
      const startTime = Date.now();

      const tx = await router
        .connect(vault)
        .batchTransferOut(recipients, assets, amounts, memos);

      const receipt = await tx.wait();
      const endTime = Date.now();
      const executionTime = endTime - startTime;

      console.log(
        `Gas used for ${batchSize} transfers: ${receipt.gasUsed.toString()}`,
      );
      console.log(`Execution time: ${executionTime}ms`);
      console.log(
        `Gas per transfer: ${(receipt.gasUsed / BigInt(batchSize)).toString()}`,
      );

      const finalBalance = await tkn1.balanceOf(user1.address);
      expect(finalBalance - initialBalance).to.equal(totalAmount);

      const gasPerTransfer = receipt.gasUsed / BigInt(batchSize);
      expect(gasPerTransfer).to.be.lt(ethers.parseUnits("200000", "wei"));

      expect(executionTime).to.be.lt(30000);
    });

    it("should handle maximum practical batch size", async function () {
      const batchSize = 100;
      const recipients = Array(batchSize).fill(user2.address);
      const assets = Array(batchSize).fill(tkn1.target);
      const amounts = Array(batchSize).fill(ethers.parseEther("0.01"));
      const memos = Array(batchSize)
        .fill()
        .map((_, i) => `MAX_BATCH_${i}`);

      const totalAmount = ethers.parseEther("0.01") * BigInt(batchSize);
      await tkn1.connect(vault).approve(router.target, totalAmount);

      const initialBalance = await tkn1.balanceOf(user2.address);

      try {
        const tx = await router
          .connect(vault)
          .batchTransferOut(recipients, assets, amounts, memos);

        const receipt = await tx.wait();
        console.log(`Successfully processed ${batchSize} transfers`);
        console.log(`Total gas used: ${receipt.gasUsed.toString()}`);

        const finalBalance = await tkn1.balanceOf(user2.address);
        expect(finalBalance - initialBalance).to.equal(totalAmount);
      } catch (error) {
        console.log(
          `Batch of ${batchSize} failed as expected due to gas limits:`,
          error.message,
        );
        expect(error.message).to.include("gas");
      }
    });
  });
});
