const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("TC:Churning (transferAllowance)", function () {
  let routerV4, router, newRouter, tkn, usdt, rune;
  let owner, oldVault, newVault, user;

  beforeEach(async function () {
    [owner, oldVault, newVault, user] = await ethers.getSigners();

    const ERC20Token = await ethers.getContractFactory("ERC20Token");
    tkn = await ERC20Token.deploy();
    await tkn.waitForDeployment();

    rune = await ERC20Token.deploy();
    await rune.waitForDeployment();

    const USDT = await ethers.getContractFactory("TetherToken");
    usdt = await USDT.deploy(
      ethers.parseUnits("1000000", 6),
      "Tether USD",
      "USDT",
      6,
    );
    await usdt.waitForDeployment();

    await usdt.setParams(1000, ethers.parseUnits("1", 6));

    const RouterV4 = await ethers.getContractFactory("THORChain_RouterV4");
    routerV4 = await RouterV4.deploy(rune.target);
    await routerV4.waitForDeployment();

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    newRouter = await Router.deploy();
    await newRouter.waitForDeployment();

    await tkn.transfer(oldVault.address, ethers.parseEther("1000"));
    await usdt.transfer(oldVault.address, ethers.parseUnits("1000", 6));
    await rune.transfer(oldVault.address, ethers.parseEther("1000"));
  });

  describe("RouterV4 → THORChain_Router Migration", function () {
    it("should migrate TKN from RouterV4 to THORChain_Router", async function () {
      const amountTKN = ethers.parseEther("50");
      const memo = "MIGRATE:TKN:V4->V6";

      // Step 1: User deposits TKN to RouterV4
      await tkn.connect(oldVault).approve(routerV4.target, amountTKN);
      await routerV4
        .connect(oldVault)
        .deposit(newVault.address, tkn.target, amountTKN, "USER:DEPOSIT");

      // Verify RouterV4 allowance increased
      expect(
        await routerV4.vaultAllowance(newVault.address, tkn.target),
      ).to.equal(amountTKN);

      // Step 2: Vault migrates TKN from RouterV4 to THORChain_Router
      const tx = await routerV4
        .connect(newVault)
        .transferAllowance(
          router.target,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );

      // Check Deposit event was emitted on THORChain_Router
      await expect(tx)
        .to.emit(router, "Deposit")
        .withArgs(newVault.address, tkn.target, amountTKN, memo);

      // Verify tokens transferred correctly
      expect(await tkn.balanceOf(newVault.address)).to.equal(amountTKN);
      expect(
        await routerV4.vaultAllowance(newVault.address, tkn.target),
      ).to.equal(0);
    });

    it("should migrate USDT from RouterV4 to THORChain_Router, handling fees", async function () {
      const amountUSDT = ethers.parseUnits("50", 6);
      const memo = "MIGRATE:USDT:V4->V6";
      const feeBasisPoints = 1000n; // 10%

      // Step 1: User deposits USDT to RouterV4
      await usdt.connect(oldVault).approve(routerV4.target, amountUSDT);
      await routerV4
        .connect(oldVault)
        .deposit(newVault.address, usdt.target, amountUSDT, "USER:DEPOSIT");
      const vaultAllowance = await routerV4.vaultAllowance(
        newVault.address,
        usdt.target,
      );

      // Step 2: Vault migrates USDT from RouterV4 to THORChain_Router
      const tx = await routerV4
        .connect(newVault)
        .transferAllowance(
          router.target,
          newVault.address,
          usdt.target,
          vaultAllowance,
          memo,
        );

      // Parse the Deposit event to see the actual amount deposited after fees
      const receipt = await tx.wait();
      const depositLogs = receipt.logs.filter((log) => {
        try {
          const parsed = router.interface.parseLog(log);
          return parsed && parsed.name === "Deposit";
        } catch {
          return false;
        }
      });

      const depositEvent = depositLogs.map((log) =>
        router.interface.parseLog(log),
      )[0];

      expect(depositEvent).to.not.be.undefined;

      // Check Deposit event was emitted on THORChain_Router
      await expect(tx)
        .to.emit(router, "Deposit")
        .withArgs(newVault.address, usdt.target, depositEvent.args[2], memo);

      // In this scenario, there are two fee deductions with 1 USDT max fee each:
      // 1. Fee when depositing to RouterV4: 50 - 1 = 49 USDT
      // 2. Fee when transferring from RouterV4 to RouterV6: 49 - 1 = 48 USDT
      // Final amount should be 48 USDT
      const expectedFinalAmount = ethers.parseUnits("48", 6); // 50 - 1 - 1 = 48
      const actualFinalAmount = await usdt.balanceOf(newVault.address);

      expect(actualFinalAmount).to.equal(expectedFinalAmount);
    });
  });

  describe("THORChain_Router → THORChain_Router Migration", function () {
    it("should migrate TKN from THORChain_Router to new THORChain_Router", async function () {
      const amountTKN = ethers.parseEther("30");
      const memo = "MIGRATE:TKN:V6->V6";

      // Step 1: Approve and transfer to old router
      await tkn.connect(oldVault).approve(router.target, amountTKN);

      const tx = await router
        .connect(oldVault)
        .transferAllowance(
          newRouter.target,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );

      // Check Deposit event was emitted on new THORChain_Router
      await expect(tx)
        .to.emit(newRouter, "Deposit")
        .withArgs(newVault.address, tkn.target, amountTKN, memo);

      // Verify tokens transferred correctly
      expect(await tkn.balanceOf(newVault.address)).to.equal(amountTKN);
    });

    it("should migrate USDT from THORChain_Router to new THORChain_Router, handling fees", async function () {
      const amountUSDT = ethers.parseUnits("40", 6);
      const memo = "MIGRATE:USDT:V6->V6";
      const feeBasisPoints = 1000n; // 10%

      // Step 1: Approve and transfer to old router
      await usdt.connect(oldVault).approve(router.target, amountUSDT);

      const tx = await router
        .connect(oldVault)
        .transferAllowance(
          newRouter.target,
          newVault.address,
          usdt.target,
          amountUSDT,
          memo,
        );

      // Parse the Deposit event to see the actual amount deposited after fees
      const receipt = await tx.wait();
      const depositLogs = receipt.logs.filter((log) => {
        try {
          const parsed = newRouter.interface.parseLog(log);
          return parsed && parsed.name === "Deposit";
        } catch {
          return false;
        }
      });

      const depositEvent = depositLogs.map((log) =>
        newRouter.interface.parseLog(log),
      )[0];

      expect(depositEvent).to.not.be.undefined;

      // Check Deposit event was emitted on new THORChain_Router
      await expect(tx)
        .to.emit(newRouter, "Deposit")
        .withArgs(newVault.address, usdt.target, depositEvent.args[2], memo);

      // In this test, there are two fee deductions with 1 USDT max fee each:
      // 1. Fee when transferring from vault to router: 40 - 1 = 39 USDT
      // 2. Fee when transferring from router to newRouter: 39 - 1 = 38 USDT
      const expectedFinalAmount = ethers.parseUnits("38", 6); // 40 - 1 - 1 = 38

      expect(await usdt.balanceOf(newVault.address)).to.equal(
        expectedFinalAmount,
      );
    });
  });

  describe("THORChain_Router Same-Router Transfers (router == address(this))", function () {
    it("should transfer TKN to new vault using same THORChain_Router", async function () {
      const amountTKN = ethers.parseEther("25");
      const memo = "TRANSFER:TKN:SAME_ROUTER";

      // Step 1: Approve router to spend vault's tokens
      await tkn.connect(oldVault).approve(router.target, amountTKN);

      const tx = await router
        .connect(oldVault)
        .transferAllowance(
          router.target,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );

      // Check TransferAllowance event was emitted
      await expect(tx)
        .to.emit(router, "TransferAllowance")
        .withArgs(
          oldVault.address,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );

      // Verify tokens transferred correctly
      expect(await tkn.balanceOf(newVault.address)).to.equal(amountTKN);
    });

    it("should transfer USDT to new vault using same THORChain_Router, handling fees", async function () {
      const amountUSDT = ethers.parseUnits("30", 6);
      const memo = "TRANSFER:USDT:SAME_ROUTER";

      // Step 1: Approve router to spend vault's tokens
      await usdt.connect(oldVault).approve(router.target, amountUSDT);

      const tx = await router
        .connect(oldVault)
        .transferAllowance(
          router.target,
          newVault.address,
          usdt.target,
          amountUSDT,
          memo,
        );

      // Parse the TransferAllowance event to see the actual amount transferred after fees
      const receipt = await tx.wait();
      const transferAllowanceEvent = receipt.logs
        .filter((log) => {
          try {
            return router.interface.parseLog(log).name === "TransferAllowance";
          } catch {
            return false;
          }
        })
        .map((log) => router.interface.parseLog(log))[0];

      expect(transferAllowanceEvent).to.not.be.undefined;

      // Check TransferAllowance event was emitted
      await expect(tx)
        .to.emit(router, "TransferAllowance")
        .withArgs(
          oldVault.address,
          newVault.address,
          usdt.target,
          transferAllowanceEvent.args[3],
          memo,
        );

      // After fee (1 USDT max), vault should receive 29 USDT
      const expectedAmount = ethers.parseUnits("29", 6);
      expect(await usdt.balanceOf(newVault.address)).to.equal(expectedAmount);
    });
  });

  describe("Error Cases", function () {
    it("should revert when RouterV4 vault has insufficient allowance", async function () {
      const amountTKN = ethers.parseEther("10");
      const memo = "INSUFFICIENT:ALLOWANCE";

      // Don't deposit any tokens to RouterV4, so allowance is 0
      await expect(
        routerV4
          .connect(oldVault)
          .transferAllowance(
            router.target,
            newVault.address,
            tkn.target,
            amountTKN,
            memo,
          ),
      ).to.be.reverted; // RouterV4 should revert when allowance is insufficient
    });

    it("should revert when THORChain_Router vault has insufficient token approval", async function () {
      const amountTKN = ethers.parseEther("10");
      const approvedAmount = ethers.parseEther("5"); // Less than needed
      const memo = "INSUFFICIENT:APPROVAL";

      await tkn.connect(oldVault).approve(router.target, approvedAmount);

      await expect(
        router
          .connect(oldVault)
          .transferAllowance(
            newRouter.target,
            newVault.address,
            tkn.target,
            amountTKN,
            memo,
          ),
      ).to.be.revertedWith("TC:transfer failed");
    });

    it("should revert when no token approval for THORChain_Router transfer", async function () {
      const amountTKN = ethers.parseEther("10");
      const memo = "NO:APPROVAL";

      await tkn.connect(oldVault).approve(router.target, 0);

      await expect(
        router
          .connect(oldVault)
          .transferAllowance(
            router.target,
            newVault.address,
            tkn.target,
            amountTKN,
            memo,
          ),
      ).to.be.revertedWith("TC:transfer failed");
    });

    it("should handle unexpected ETH with ERC20 transfer in RouterV6", async function () {
      const amountTKN = ethers.parseEther("10");
      const memo = "UNEXPECTED_ETH";

      // Approve router to spend tokens
      await tkn.connect(oldVault).approve(router.target, amountTKN);

      // Should revert when sending ETH with ERC20 transfer
      // Note: The revert may happen earlier in the validation chain
      await expect(
        router
          .connect(oldVault)
          .transferAllowance(
            router.target,
            newVault.address,
            tkn.target,
            amountTKN,
            memo,
            {
              value: ethers.parseEther("1"),
            },
          ),
      ).to.be.reverted; // Just check that it reverts, don't check specific message
    });

    it("should handle unexpected ETH with ERC20 transfer in RouterV4", async function () {
      const amountTKN = ethers.parseEther("1");
      const memo = "UNEXPECTED_ETH";

      // First deposit some tokens into RouterV4
      await tkn.connect(oldVault).approve(routerV4.target, amountTKN);
      await routerV4
        .connect(oldVault)
        .deposit(newVault.address, tkn.target, amountTKN, "DEPOSIT");

      // Should revert when sending ETH with ERC20 transfer
      // Note: RouterV4 doesn't have the _validateEthUsage check, so it may revert for different reasons
      await expect(
        routerV4
          .connect(oldVault)
          .transferAllowance(
            router.target,
            newVault.address,
            tkn.target,
            amountTKN,
            memo,
            {
              value: ethers.parseEther("1"),
            },
          ),
      ).to.be.reverted; // Just check that it reverts, don't check specific message
    });
  });

  describe("Edge Cases", function () {
    it("should handle zero amount transfers", async function () {
      const amountTKN = ethers.parseEther("0");
      const memo = "ZERO_AMOUNT";

      // First deposit some tokens to establish allowance
      await tkn
        .connect(oldVault)
        .approve(routerV4.target, ethers.parseEther("1"));
      await routerV4
        .connect(oldVault)
        .deposit(newVault.address, tkn.target, ethers.parseEther("1"), "SETUP");

      const tx = await routerV4
        .connect(oldVault)
        .transferAllowance(
          router.target,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );

      await expect(tx)
        .to.emit(router, "Deposit")
        .withArgs(newVault.address, tkn.target, amountTKN, memo);
    });

    it("should handle empty memo", async function () {
      const amountTKN = ethers.parseEther("1");
      const memo = "";

      // Approve router to spend tokens
      await tkn.connect(oldVault).approve(router.target, amountTKN);

      const tx = await router
        .connect(oldVault)
        .transferAllowance(
          router.target,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );

      await expect(tx)
        .to.emit(router, "TransferAllowance")
        .withArgs(
          oldVault.address,
          newVault.address,
          tkn.target,
          amountTKN,
          memo,
        );
    });

    it("should handle large token amounts", async function () {
      const largeAmount = ethers.parseEther("1000");
      const memo = "LARGE_AMOUNT";

      // Give old vault more tokens and approve
      await tkn.transfer(oldVault.address, largeAmount);
      await tkn.connect(oldVault).approve(router.target, largeAmount);

      const tx = await router
        .connect(oldVault)
        .transferAllowance(
          router.target,
          newVault.address,
          tkn.target,
          largeAmount,
          memo,
        );

      await expect(tx)
        .to.emit(router, "TransferAllowance")
        .withArgs(
          oldVault.address,
          newVault.address,
          tkn.target,
          largeAmount,
          memo,
        );

      expect(await tkn.balanceOf(newVault.address)).to.equal(largeAmount);
    });
  });
});
