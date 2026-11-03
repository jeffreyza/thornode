const { expect } = require("chai");
const { ethers } = require("hardhat");

async function hasEvent(tx, eventName) {
  const receipt = await tx.wait();
  return receipt.logs.some((log) => {
    try {
      return log.fragment && log.fragment.name === eventName;
    } catch (e) {
      return false;
    }
  });
}

describe("TC:Reentrancy Tests", function () {
  let router, erc20Token, owner, user, vault;
  let attacker, evilCallback, reentrancyToken, evilToken;

  beforeEach(async function () {
    [owner, user, vault] = await ethers.getSigners();

    const Token = await ethers.getContractFactory("ERC20Token");
    erc20Token = await Token.deploy();
    await erc20Token.waitForDeployment();

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    const ReentrancyAttacker =
      await ethers.getContractFactory("ReentrancyAttacker");
    attacker = await ReentrancyAttacker.deploy(await router.getAddress());
    await attacker.waitForDeployment();

    const EvilCallback = await ethers.getContractFactory("EvilCallback");
    evilCallback = await EvilCallback.deploy(await router.getAddress());
    await evilCallback.waitForDeployment();

    const ReentrancyToken = await ethers.getContractFactory("ReentrancyToken");
    reentrancyToken = await ReentrancyToken.deploy("Reentry Token", "RENTRY");
    await reentrancyToken.waitForDeployment();
    await reentrancyToken.setRouterAndTarget(
      await router.getAddress(),
      vault.address,
    );

    const EvilToken = await ethers.getContractFactory("EvilERC20Token");
    evilToken = await EvilToken.deploy();
    await evilToken.waitForDeployment();

    await erc20Token.transfer(user.address, ethers.parseEther("100"));
    await erc20Token.transfer(
      await evilCallback.getAddress(),
      ethers.parseEther("10"),
    );

    await reentrancyToken.transfer(user.address, ethers.parseEther("100"));
    await evilToken.transfer(user.address, ethers.parseEther("100"));
  });

  describe("1. Reentrancy Attack Tests", function () {
    it("1.1 Same-Function Reentrancy Attack on depositWithExpiry", async function () {
      const amount = ethers.parseEther("1");
      const memo = "ATTACK";

      const initialVaultBalance = await ethers.provider.getBalance(
        vault.address,
      );
      const initialAttackCount = await attacker.attackCount();
      const initialDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );

      const tx = await attacker.attackDeposit(vault.address, memo, {
        value: amount,
      });

      const finalDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );
      expect(finalDepositEvents.length).to.be.greaterThanOrEqual(
        initialDepositEvents.length + 1,
        "Should emit exactly one Deposit event",
      );

      const finalVaultBalance = await ethers.provider.getBalance(vault.address);
      expect(finalVaultBalance - initialVaultBalance).to.equal(amount);

      const finalAttackCount = await attacker.attackCount();
      expect(finalAttackCount - initialAttackCount).to.equal(
        0,
        "Attack count shouldn't change if reentrancy protection works",
      );

      expect(finalDepositEvents.length - initialDepositEvents.length).to.equal(
        1,
        "Should emit exactly one Deposit event",
      );
    });

    it("1.2 Callback Reentrancy Attack on transferOut", async function () {
      const amount = ethers.parseEther("1");
      const memo = "ATTACK";

      const evilCallbackAddress = await evilCallback.getAddress();

      const initialBalance =
        await ethers.provider.getBalance(evilCallbackAddress);
      const initialCallerBalance = await ethers.provider.getBalance(
        user.address,
      );
      const initialAttackCount = await evilCallback.attackCount();

      const tx = await router
        .connect(user)
        .transferOut(evilCallbackAddress, ethers.ZeroAddress, amount, memo, {
          value: amount,
        });

      const hasTransferEvent = await hasEvent(tx, "TransferOut");
      expect(hasTransferEvent).to.be.true;

      const finalBalance =
        await ethers.provider.getBalance(evilCallbackAddress);
      expect(finalBalance).to.equal(
        initialBalance,
        "Evil callback should not receive ETH due to reentrancy protection",
      );

      const receipt = await tx.wait();

      const gasUsed = receipt.gasUsed * receipt.gasPrice;
      const finalCallerBalance = await ethers.provider.getBalance(user.address);
      const expectedCallerBalance = initialCallerBalance - gasUsed;
      const balanceDifference = finalCallerBalance - expectedCallerBalance;

      expect(
        Math.abs(Number(ethers.formatEther(balanceDifference))),
      ).to.be.lessThan(
        0.001,
        "Caller's balance should be refunded except for gas fees",
      );

      const transferEvents = receipt.logs.filter((log) => {
        try {
          return (
            log.fragment &&
            (log.fragment.name === "TransferOut" ||
              log.fragment.name === "TransferFailed")
          );
        } catch (e) {
          return false;
        }
      });
      expect(transferEvents.length).to.be.greaterThan(
        0,
        "Router should emit either TransferOut or TransferFailed event",
      );

      const finalAttackCount = await evilCallback.attackCount();
      expect(finalAttackCount).to.equal(
        initialAttackCount,
        "Attack count shouldn't change",
      );
    });

    it("1.3 Callback Reentrancy Attack on batchTransferOut", async function () {
      const amount = ethers.parseEther("1");
      const memo = "ATTACK";

      const evilCallbackAddress = await evilCallback.getAddress();
      const userAddress = user.address;

      const recipients = [userAddress, evilCallbackAddress, vault.address];
      const assets = [
        ethers.ZeroAddress,
        ethers.ZeroAddress,
        ethers.ZeroAddress,
      ];
      const amounts = [amount, amount, amount];
      const memos = ["TEST1", memo, "TEST2"];

      const initialBalances = await Promise.all(
        recipients.map((addr) => ethers.provider.getBalance(addr)),
      );
      const initialAttackCount = await evilCallback.attackCount();
      const initialTransferEvents = await router.queryFilter(
        router.filters.TransferOut,
      );

      const totalAmount = amount * BigInt(3);
      const tx = await router
        .connect(owner)
        .batchTransferOut(recipients, assets, amounts, memos, {
          value: totalAmount,
        });

      const hasTransferEvent = await hasEvent(tx, "TransferOut");
      expect(hasTransferEvent).to.be.true;

      const finalTransferEvents = await router.queryFilter(
        router.filters.TransferOut,
      );
      const newEvents =
        finalTransferEvents.length - initialTransferEvents.length;

      expect(newEvents).to.be.greaterThanOrEqual(
        2,
        "Should emit at least 2 TransferOut events",
      );
      expect(newEvents).to.be.lessThanOrEqual(
        3,
        "Should emit at most 3 TransferOut events",
      );

      const finalBalances = await Promise.all(
        recipients.map((addr) => ethers.provider.getBalance(addr)),
      );

      expect(finalBalances[0] - initialBalances[0]).to.equal(
        amount,
        "User should receive 1 ETH",
      );
      expect(finalBalances[2] - initialBalances[2]).to.equal(
        amount,
        "Vault should receive 1 ETH",
      );

      const finalAttackCount = await evilCallback.attackCount();

      expect(finalAttackCount - initialAttackCount).to.be.lessThanOrEqual(
        1n,
        "Attack count shouldn't increase more than once if reentrancy protection works",
      );
    });

    it("1.4 DEX Callback Reentrancy Attack on transferOutAndCall", async function () {
      const amount = ethers.parseEther("1");
      const memo = "ATTACK";

      const evilCallbackAddress = await evilCallback.getAddress();
      const userAddress = user.address;

      const initialAttackCount = await evilCallback.attackCount();
      const initialTransferEvents = await router.queryFilter(
        router.filters.TransferOut,
      );

      const tx = await router
        .connect(owner)
        .transferOutAndCall(
          evilCallbackAddress,
          ethers.ZeroAddress,
          userAddress,
          amount,
          memo,
          { value: amount },
        );

      const hasTransferCallEvent = await hasEvent(tx, "TransferOutAndCall");
      expect(hasTransferCallEvent).to.be.true,
        "Should emit TransferOutAndCall event";

      const finalAttackCount = await evilCallback.attackCount();
      const finalTransferEvents = await router.queryFilter(
        router.filters.TransferOut,
      );

      expect(finalAttackCount - initialAttackCount).to.be.lessThanOrEqual(
        1n,
        "Attack count shouldn't increase more than once if reentrancy protection works",
      );

      const newEvents =
        finalTransferEvents.length - initialTransferEvents.length;
      expect(newEvents).to.be.lessThanOrEqual(
        1,
        "Should not emit excessive TransferOut events",
      );
    });

    it("1.5 DEX Callback Reentrancy Attack on transferOutAndCallV2", async function () {
      const amount = ethers.parseEther("1");
      const memo = "ATTACK";

      const evilCallbackAddress = await evilCallback.getAddress();
      const userAddress = user.address;
      const mockTokenAddress = await erc20Token.getAddress();

      const initialAttackCount = await evilCallback.attackCount();
      const initialTransferEvents = await router.queryFilter(
        router.filters.TransferOutAndCallV2,
      );

      const params = {
        target: evilCallbackAddress,
        fromAsset: ethers.ZeroAddress,
        fromAmount: amount,
        toAsset: mockTokenAddress,
        recipient: userAddress,
        amountOutMin: amount,
        memo: memo,
        payload: "0x",
        originAddress: "thor1...",
      };

      const tx = await router.connect(owner).transferOutAndCallV2(params, {
        value: amount,
      });

      const hasTransferCallEvent = await hasEvent(tx, "TransferOutAndCallV2");
      expect(hasTransferCallEvent).to.be.true,
        "Should emit TransferOutAndCallV2 event";

      const finalAttackCount = await evilCallback.attackCount();
      const finalTransferEvents = await router.queryFilter(
        router.filters.TransferOutAndCallV2,
      );

      expect(finalAttackCount - initialAttackCount).to.be.lessThanOrEqual(
        1n,
        "Attack count shouldn't increase more than once if reentrancy protection works",
      );

      const newEvents =
        finalTransferEvents.length - initialTransferEvents.length;
      expect(newEvents).to.equal(
        1,
        "Should emit exactly one TransferOutAndCallV2 event",
      );
    });

    it("1.6 Direct Reentrancy Attack on transferOut", async function () {
      const amount = ethers.parseEther("1");
      const memo = "ATTACK";

      const evilCallbackAddress = await evilCallback.getAddress();

      const initialCallbackBalance =
        await ethers.provider.getBalance(evilCallbackAddress);
      const initialSenderBalance = await ethers.provider.getBalance(
        owner.address,
      );
      const initialAttackCount = await evilCallback.attackCount();

      const tx = await router
        .connect(owner)
        .transferOut(evilCallbackAddress, ethers.ZeroAddress, amount, memo, {
          value: amount,
        });

      const receipt = await tx.wait();

      const transferEvents = receipt.logs.filter((log) => {
        try {
          return (
            log.fragment &&
            (log.fragment.name === "TransferOut" ||
              log.fragment.name === "TransferFailed")
          );
        } catch (e) {
          return false;
        }
      });
      expect(transferEvents.length).to.be.greaterThan(
        0,
        "Should emit at least one event",
      );

      const gasUsed = receipt.gasUsed * receipt.gasPrice;

      const finalCallbackBalance =
        await ethers.provider.getBalance(evilCallbackAddress);
      expect(finalCallbackBalance).to.equal(
        initialCallbackBalance,
        "EvilCallback shouldn't receive ETH due to reentrancy protection",
      );

      const finalSenderBalance = await ethers.provider.getBalance(
        owner.address,
      );
      const expectedSenderBalance = initialSenderBalance - gasUsed;
      const balanceDifference = finalSenderBalance - expectedSenderBalance;

      expect(
        Math.abs(Number(ethers.formatEther(balanceDifference))),
      ).to.be.lessThan(0.001, "Sender should be refunded except for gas fees");

      const finalAttackCount = await evilCallback.attackCount();
      expect(finalAttackCount - initialAttackCount).to.be.lessThanOrEqual(
        1n,
        "Attack count shouldn't increase beyond initial callback",
      );
    });

    it("2.1 Cross-Function Reentrancy Attack", async function () {
      const amount = ethers.parseEther("1");
      const memo = "CROSS_FUNCTION_ATTACK";

      const evilCallbackAddress = await evilCallback.getAddress();

      const initialAttackCount = await evilCallback.attackCount();
      const initialDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );
      const initialTransferEvents = await router.queryFilter(
        router.filters.TransferOut,
      );

      const tx = await router
        .connect(owner)
        .transferOut(evilCallbackAddress, ethers.ZeroAddress, amount, memo, {
          value: amount,
        });

      const hasTransferEvent = await hasEvent(tx, "TransferOut");
      expect(hasTransferEvent).to.be.true, "Should emit TransferOut event";

      const finalAttackCount = await evilCallback.attackCount();
      const finalDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );
      const finalTransferEvents = await router.queryFilter(
        router.filters.TransferOut,
      );

      expect(finalDepositEvents.length).to.equal(
        initialDepositEvents.length,
        "No new Deposit events should be emitted from cross-function reentrancy attempt",
      );

      expect(finalTransferEvents.length).to.equal(
        initialTransferEvents.length + 1,
        "Only one new TransferOut event should be emitted",
      );

      expect(finalAttackCount - initialAttackCount).to.be.lessThanOrEqual(
        1n,
        "Attack count shouldn't increase beyond initial callback",
      );
    });

    it("2.2 Token-Based Reentrancy Attack", async function () {
      const depositAmount = ethers.parseEther("1");
      const memo = "DEPOSIT_FROM_TOKEN";

      await reentrancyToken.setAttackMode(true);

      await reentrancyToken.transfer(user.address, depositAmount);
      await reentrancyToken
        .connect(user)
        .approve(await router.getAddress(), depositAmount);

      const initialDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );

      // The reentrancy protection should prevent the attack, but the transaction may still succeed
      await router
        .connect(user)
        .depositWithExpiry(
          vault.address,
          await reentrancyToken.getAddress(),
          depositAmount,
          memo,
          ethers.MaxUint256,
        );

      const finalDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );

      // The reentrancy protection should limit to exactly one deposit event
      expect(finalDepositEvents.length).to.equal(
        initialDepositEvents.length + 1,
        "Should emit exactly one Deposit event despite reentrancy attempt",
      );

      expect(await reentrancyToken.attackMode()).to.be.true;
    });

    it("2.3 Nested Transaction Attack", async function () {
      const depositAmount = ethers.parseEther("1");
      const memo = "DEPOSIT_WITH_EVIL_TOKEN";

      await evilToken.transfer(user.address, depositAmount);

      const initialDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );

      await evilToken
        .connect(user)
        .approve(await router.getAddress(), depositAmount);

      try {
        await router
          .connect(user)
          .depositWithExpiry(
            vault.address,
            await evilToken.getAddress(),
            depositAmount,
            memo,
            ethers.MaxUint256,
          );

        expect.fail("Deposit with evil token should have failed");
      } catch (error) {
        expect(error.message).to.include("transfer failed");
      }

      const finalDepositEvents = await router.queryFilter(
        router.filters.Deposit,
      );

      expect(finalDepositEvents.length).to.equal(
        initialDepositEvents.length,
        "No new Deposit events should be emitted since transaction reverted",
      );
    });
  });
});
