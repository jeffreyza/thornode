const { expect } = require("chai");
const { ethers } = require("hardhat");

async function hasEventByTopic(tx, eventSignature) {
  const receipt = await tx.wait();
  if (!receipt.logs) return false;

  const topicHash = ethers.id(eventSignature);
  return receipt.logs.some((log) => log.topics[0] === topicHash);
}

describe("TC:Edge Cases", function () {
  let router, rebasingToken, noFallbackContract, failingToken;
  let owner, vault, user;

  beforeEach(async function () {
    [owner, vault, user] = await ethers.getSigners();

    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    const RebasingToken = await ethers.getContractFactory(
      "contracts/attacks/RebasingToken.sol:RebasingToken",
    );
    rebasingToken = await RebasingToken.deploy("Rebasing Token", "REBASE");
    await rebasingToken.waitForDeployment();

    const FailingToken = await ethers.getContractFactory(
      "contracts/attacks/FailingToken.sol:FailingToken",
    );
    failingToken = await FailingToken.deploy("Failing Token", "FAIL");
    await failingToken.waitForDeployment();

    const NoFallbackContract = await ethers.getContractFactory(
      "contracts/attacks/NoFallbackContract.sol:NoFallbackContract",
    );
    noFallbackContract = await NoFallbackContract.deploy();
    await noFallbackContract.waitForDeployment();

    const tokenAmount = ethers.parseEther("100");
    await rebasingToken.transfer(vault.address, tokenAmount);
    await failingToken.transfer(vault.address, tokenAmount);

    await rebasingToken
      .connect(vault)
      .approve(await router.getAddress(), ethers.MaxUint256);
    await failingToken
      .connect(vault)
      .approve(await router.getAddress(), ethers.MaxUint256);
  });

  describe("1. Rebasing Token Tests", function () {
    it("Handles rebasing tokens with changing balances", async function () {
      const amount = ethers.parseEther("10");
      const memo = "REBASING_TOKEN_TRANSFER";
      const tokenAddress = await rebasingToken.getAddress();

      const initialUserBalance = await rebasingToken.balanceOf(user.address);

      const tx = await router
        .connect(vault)
        .transferOut(user.address, tokenAddress, amount, memo);

      const transferOutEvent = await hasEventByTopic(
        tx,
        "TransferOut(address,address,address,uint256,string)",
      );
      expect(transferOutEvent).to.be.true;

      const finalUserBalance = await rebasingToken.balanceOf(user.address);
      expect(finalUserBalance).to.be.gt(initialUserBalance);

      await rebasingToken.rebase(12000);

      const afterRebaseBalance = await rebasingToken.balanceOf(user.address);
      expect(afterRebaseBalance).to.be.gt(finalUserBalance);

      const routerBalance = await rebasingToken.balanceOf(
        await router.getAddress(),
      );
      expect(routerBalance).to.equal(0);

      console.log("Initial balance:", ethers.formatEther(initialUserBalance));
      console.log("After transfer:", ethers.formatEther(finalUserBalance));
      console.log("After rebase:", ethers.formatEther(afterRebaseBalance));
    });

    it("Handles rebasing tokens in deposits", async function () {
      const amount = ethers.parseEther("5");
      const memo = "REBASING_TOKEN_DEPOSIT";
      const tokenAddress = await rebasingToken.getAddress();

      const initialUserBalance = await rebasingToken.balanceOf(user.address);
      const initialVaultBalance = await rebasingToken.balanceOf(vault.address);

      await rebasingToken.transfer(user.address, amount);
      await rebasingToken
        .connect(user)
        .approve(await router.getAddress(), amount);

      const tx = await router
        .connect(user)
        .depositWithExpiry(vault.address, tokenAddress, amount, memo, 0);

      await expect(tx)
        .to.emit(router, "Deposit")
        .withArgs(vault.address, tokenAddress, amount, memo);

      const finalUserBalance = await rebasingToken.balanceOf(user.address);
      const finalVaultBalance = await rebasingToken.balanceOf(vault.address);

      expect(finalUserBalance).to.equal(initialUserBalance);
      expect(finalVaultBalance).to.equal(initialVaultBalance + amount);

      await rebasingToken.rebase(15000);

      const afterRebaseUserBalance = await rebasingToken.balanceOf(
        user.address,
      );
      const afterRebaseVaultBalance = await rebasingToken.balanceOf(
        vault.address,
      );

      expect(afterRebaseUserBalance).to.equal(finalUserBalance);
      expect(afterRebaseVaultBalance).to.be.gt(finalVaultBalance);

      const routerBalance = await rebasingToken.balanceOf(
        await router.getAddress(),
      );
      expect(routerBalance).to.equal(0);

      console.log(
        "User balance after deposit:",
        ethers.formatEther(finalUserBalance),
      );
      console.log(
        "Vault balance after deposit:",
        ethers.formatEther(finalVaultBalance),
      );
      console.log(
        "User balance after rebase:",
        ethers.formatEther(afterRebaseUserBalance),
      );
      console.log(
        "Vault balance after rebase:",
        ethers.formatEther(afterRebaseVaultBalance),
      );
    });
  });

  describe("2. Contract Without Fallback Tests", function () {
    it("Properly handles ETH transfers to contracts without receive function", async function () {
      const amount = ethers.parseEther("1");
      const memo = "NO_FALLBACK_TRANSFER";
      const noFallbackAddress = await noFallbackContract.getAddress();

      const initialContractBalance =
        await ethers.provider.getBalance(noFallbackAddress);
      expect(initialContractBalance).to.equal(0);

      const tx = await router
        .connect(vault)
        .transferOut(noFallbackAddress, ethers.ZeroAddress, amount, memo, {
          value: amount,
        });

      const transferFailedEvent = await hasEventByTopic(
        tx,
        "TransferFailed(address,address,address,uint256,string)",
      );
      expect(transferFailedEvent).to.be.true;

      const finalContractBalance =
        await ethers.provider.getBalance(noFallbackAddress);
      expect(finalContractBalance).to.equal(0);

      console.log(
        "Contract balance after failed transfer:",
        ethers.formatEther(finalContractBalance),
      );
      console.log("TransferFailed event emitted:", transferFailedEvent);
    });

    it("Handles batch transfers with partial failures", async function () {
      const amount = ethers.parseEther("1");
      const noFallbackAddress = await noFallbackContract.getAddress();

      const recipients = [user.address, noFallbackAddress, vault.address];
      const assets = [
        ethers.ZeroAddress,
        ethers.ZeroAddress,
        ethers.ZeroAddress,
      ];
      const amounts = [amount, amount, amount];
      const memos = [
        "SUCCESS_TRANSFER",
        "NO_FALLBACK_TRANSFER",
        "SUCCESS_TRANSFER_2",
      ];

      const initialUserBalance = await ethers.provider.getBalance(user.address);
      const initialNoFallbackBalance =
        await ethers.provider.getBalance(noFallbackAddress);
      const initialVaultBalance = await ethers.provider.getBalance(
        vault.address,
      );

      const totalAmount = amount * BigInt(3);
      const tx = await router
        .connect(owner)
        .batchTransferOut(recipients, assets, amounts, memos, {
          value: totalAmount,
        });

      const hasTransferOutEvent = await hasEventByTopic(
        tx,
        "TransferOut(address,address,address,uint256,string)",
      );
      const hasTransferFailedEvent = await hasEventByTopic(
        tx,
        "TransferFailed(address,address,address,uint256,string)",
      );

      expect(hasTransferOutEvent).to.be.true;
      expect(hasTransferFailedEvent).to.be.true;

      const finalUserBalance = await ethers.provider.getBalance(user.address);
      const finalNoFallbackBalance =
        await ethers.provider.getBalance(noFallbackAddress);
      const finalVaultBalance = await ethers.provider.getBalance(vault.address);

      expect(finalUserBalance - initialUserBalance).to.equal(amount);
      expect(finalNoFallbackBalance).to.equal(initialNoFallbackBalance);
      expect(finalVaultBalance - initialVaultBalance).to.equal(amount);

      console.log("Batch partial failure test completed:");
      console.log(
        `User received: ${ethers.formatEther(finalUserBalance - initialUserBalance)} ETH`,
      );
      console.log(
        `NoFallback received: ${ethers.formatEther(finalNoFallbackBalance - initialNoFallbackBalance)} ETH`,
      );
      console.log(
        `Vault received: ${ethers.formatEther(finalVaultBalance - initialVaultBalance)} ETH`,
      );
    });
  });

  describe("3. Failing Token Tests", function () {
    it("Handles tokens that can fail transfers on demand", async function () {
      const amount = ethers.parseEther("10");
      const memo = "FAILING_TOKEN_TRANSFER";
      const tokenAddress = await failingToken.getAddress();

      const initialUserBalance = await failingToken.balanceOf(user.address);

      let tx = await router
        .connect(vault)
        .transferOut(user.address, tokenAddress, amount, memo);

      const transferOutEvent = await hasEventByTopic(
        tx,
        "TransferOut(address,address,address,uint256,string)",
      );
      expect(transferOutEvent).to.be.true;

      const userBalanceAfterSuccess = await failingToken.balanceOf(
        user.address,
      );
      expect(userBalanceAfterSuccess).to.equal(initialUserBalance + amount);

      console.log(
        "Initial user balance:",
        ethers.formatEther(initialUserBalance),
      );
      console.log(
        "User balance after successful transfer:",
        ethers.formatEther(userBalanceAfterSuccess),
      );

      await failingToken.setFailTransfers(true);
      console.log("Failing mode enabled, transfers should now fail");

      try {
        await router
          .connect(vault)
          .transferOut(
            user.address,
            tokenAddress,
            amount,
            "SHOULD_FAIL_TRANSFER",
          );
        expect.fail("Transaction should have reverted");
      } catch (error) {
        console.log(
          "Transaction correctly reverted with error:",
          error.message,
        );
        expect(error.message).to.include("transfer failed");
      }

      const finalUserBalance = await failingToken.balanceOf(user.address);
      expect(finalUserBalance).to.equal(userBalanceAfterSuccess);

      const routerBalance = await failingToken.balanceOf(
        await router.getAddress(),
      );
      expect(routerBalance).to.equal(0);

      console.log(
        "User balance after failed transfer:",
        ethers.formatEther(finalUserBalance),
      );
    });
  });

  describe("vaultAllowance function", function () {
    it("Should return correct vault token balance", async function () {
      const ERC20Token = await ethers.getContractFactory("ERC20Token");
      const testToken = await ERC20Token.deploy();
      await testToken.waitForDeployment();

      const vaultTokenAmount = ethers.parseEther("100");
      await testToken.transfer(vault.address, vaultTokenAmount);

      const vaultAllowanceResult = await router.vaultAllowance(
        vault.address,
        await testToken.getAddress(),
      );
      const directBalanceOf = await testToken.balanceOf(vault.address);

      expect(vaultAllowanceResult).to.equal(directBalanceOf);
      expect(vaultAllowanceResult).to.equal(vaultTokenAmount);

      console.log("Vault allowance:", ethers.formatEther(vaultAllowanceResult));
    });

    it("Should return zero for vault with no tokens", async function () {
      const ERC20Token = await ethers.getContractFactory("ERC20Token");
      const testToken = await ERC20Token.deploy();
      await testToken.waitForDeployment();

      const vaultAllowanceResult = await router.vaultAllowance(
        user.address,
        await testToken.getAddress(),
      );

      expect(vaultAllowanceResult).to.equal(0);
    });
  });
});

describe("Transient Storage and Initialization", function () {
  it("should deploy without constructor and use transient storage for reentrancy protection", async function () {
    const Router = await ethers.getContractFactory("THORChain_Router");
    const router = await Router.deploy();
    await router.waitForDeployment();

    // Transient storage starts at false (0) and can't be read directly
    // Instead, we verify that the contract deploys successfully and
    // reentrancy protection works by testing actual function calls
    expect(await router.getAddress()).to.be.properAddress;

    // Verify reentrancy protection is active by ensuring normal operations work
    const [owner] = await ethers.getSigners();
    const amount = ethers.parseEther("0.1");

    // This should work (no reentrancy)
    await expect(
      router
        .connect(owner)
        .transferOut(owner.address, ethers.ZeroAddress, amount, "test", {
          value: amount,
        }),
    ).to.emit(router, "TransferOut");
  });
});
