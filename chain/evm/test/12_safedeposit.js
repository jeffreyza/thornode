const { expect } = require("chai");
const { ethers } = require("hardhat");

describe("TC:SafeDeposit - ETH transfers to GnosisSafe", function () {
  let router,
    gnosisSafe,
    gnosisSafeImplementation,
    gnosisSafeProxy,
    proxyFactory;
  let owner, user, vault, safeOwner1, safeOwner2;
  let standardToken;

  beforeEach(async function () {
    [owner, user, vault, safeOwner1, safeOwner2] = await ethers.getSigners();

    // Deploy the router
    const Router = await ethers.getContractFactory("THORChain_Router");
    router = await Router.deploy();
    await router.waitForDeployment();

    // Deploy the GnosisSafe implementation (singleton)
    const GnosisSafeImpl = await ethers.getContractFactory(
      "contracts/GnosisSafe/GnosisSafe.sol:GnosisSafe",
    );
    gnosisSafeImplementation = await GnosisSafeImpl.deploy();
    await gnosisSafeImplementation.waitForDeployment();

    // Deploy the proxy factory
    const ProxyFactory = await ethers.getContractFactory(
      "contracts/GnosisSafe/Proxy.sol:GnosisSafeProxyFactory",
    );
    proxyFactory = await ProxyFactory.deploy();
    await proxyFactory.waitForDeployment();

    // Create setup data for the Safe
    const setupData = gnosisSafeImplementation.interface.encodeFunctionData(
      "setup",
      [
        [safeOwner1.address, safeOwner2.address], // owners
        1, // threshold
        ethers.ZeroAddress, // to
        "0x", // data
        ethers.ZeroAddress, // fallbackHandler
        ethers.ZeroAddress, // paymentToken
        0, // payment
        ethers.ZeroAddress, // paymentReceiver
      ],
    );

    // Create a Safe proxy using the factory
    const tx = await proxyFactory.createProxy(
      gnosisSafeImplementation.target,
      setupData,
    );
    const receipt = await tx.wait();

    // Get the proxy address from the event
    const proxyCreationEvent = receipt.logs.find(
      (log) => log.fragment && log.fragment.name === "ProxyCreation",
    );
    const proxyAddress = proxyCreationEvent.args[0];

    // Connect to the proxy - use the implementation ABI but proxy address
    gnosisSafe = gnosisSafeImplementation.attach(proxyAddress);

    // Deploy ERC20 token for testing
    const Token = await ethers.getContractFactory(
      "contracts/mocks/ERC20.sol:ERC20Token",
    );
    standardToken = await Token.deploy();
    await standardToken.waitForDeployment();

    // Transfer some tokens to test accounts
    await standardToken.transfer(user.address, ethers.parseEther("100"));
    await standardToken.transfer(vault.address, ethers.parseEther("50"));
    await standardToken.transfer(gnosisSafe.target, ethers.parseEther("25"));
  });

  it("should successfully transfer ETH to GnosisSafe using transferOut", async function () {
    const amount = ethers.parseEther("1");
    const memo = "TRANSFER:TO:SAFE";

    // Get initial balances
    const initialSafeBalance = await ethers.provider.getBalance(
      gnosisSafe.target,
    );
    const initialVaultBalance = await ethers.provider.getBalance(vault.address);

    // Execute transfer from vault to GnosisSafe
    const tx = await router
      .connect(vault)
      .transferOut(gnosisSafe.target, ethers.ZeroAddress, amount, memo, {
        value: amount,
      });

    // Verify the transaction succeeded
    await expect(tx)
      .to.emit(router, "TransferOut")
      .withArgs(
        vault.address,
        gnosisSafe.target,
        ethers.ZeroAddress,
        amount,
        memo,
      );

    // Note: SafeReceived event is emitted by the Safe contract, not the router
    // We can verify it by checking the transaction receipt or Safe state

    // Check final balances
    const finalSafeBalance = await ethers.provider.getBalance(
      gnosisSafe.target,
    );
    expect(finalSafeBalance - initialSafeBalance).to.equal(amount);

    // Note: getNonce() may not be accessible through proxy, but balance check proves receive() executed
  });

  it("should successfully batch transfer ETH to multiple GnosisSafes", async function () {
    // Deploy a second GnosisSafe using the same implementation
    const setupData2 = gnosisSafeImplementation.interface.encodeFunctionData(
      "setup",
      [
        [safeOwner1.address], // owners
        1, // threshold
        ethers.ZeroAddress, // to
        "0x", // data
        ethers.ZeroAddress, // fallbackHandler
        ethers.ZeroAddress, // paymentToken
        0, // payment
        ethers.ZeroAddress, // paymentReceiver
      ],
    );

    const tx2 = await proxyFactory.createProxy(
      gnosisSafeImplementation.target,
      setupData2,
    );
    const receipt2 = await tx2.wait();

    const proxyCreationEvent2 = receipt2.logs.find(
      (log) => log.fragment && log.fragment.name === "ProxyCreation",
    );
    const proxyAddress2 = proxyCreationEvent2.args[0];
    const gnosisSafe2 = gnosisSafeImplementation.attach(proxyAddress2);

    const amount1 = ethers.parseEther("0.3");
    const amount2 = ethers.parseEther("0.2");
    const totalAmount = amount1 + amount2;
    const memo1 = "BATCH:SAFE1";
    const memo2 = "BATCH:SAFE2";

    // Get initial balances
    const initialSafe1Balance = await ethers.provider.getBalance(
      gnosisSafe.target,
    );
    const initialSafe2Balance = await ethers.provider.getBalance(
      gnosisSafe2.target,
    );

    // Execute batch transfer
    const tx = await router
      .connect(vault)
      .batchTransferOut(
        [gnosisSafe.target, gnosisSafe2.target],
        [ethers.ZeroAddress, ethers.ZeroAddress],
        [amount1, amount2],
        [memo1, memo2],
        {
          value: totalAmount,
        },
      );

    // Verify both transfers succeeded
    await expect(tx)
      .to.emit(router, "TransferOut")
      .withArgs(
        vault.address,
        gnosisSafe.target,
        ethers.ZeroAddress,
        amount1,
        memo1,
      );

    await expect(tx)
      .to.emit(router, "TransferOut")
      .withArgs(
        vault.address,
        gnosisSafe2.target,
        ethers.ZeroAddress,
        amount2,
        memo2,
      );

    // Note: SafeReceived events would be in transaction logs but harder to verify in batch operations

    // Check final balances
    const finalSafe1Balance = await ethers.provider.getBalance(
      gnosisSafe.target,
    );
    const finalSafe2Balance = await ethers.provider.getBalance(
      gnosisSafe2.target,
    );

    expect(finalSafe1Balance - initialSafe1Balance).to.equal(amount1);
    expect(finalSafe2Balance - initialSafe2Balance).to.equal(amount2);
  });

  it("should handle failed ETH transfer to Safe and emit TransferFailed", async function () {
    // Deploy a contract that will reject ETH transfers
    const RevertingContract =
      await ethers.getContractFactory("RevertingContract");
    const revertingContract = await RevertingContract.deploy();
    await revertingContract.waitForDeployment();

    const amount = ethers.parseEther("0.1");
    const memo = "TRANSFER:TO:REVERTING";

    // Get initial balance of vault
    const initialVaultBalance = await ethers.provider.getBalance(vault.address);

    // Execute transfer that should fail and fallback to returning ETH to sender
    const tx = await router
      .connect(vault)
      .transferOut(revertingContract.target, ethers.ZeroAddress, amount, memo, {
        value: amount,
      });

    // Verify TransferFailed event was emitted
    await expect(tx)
      .to.emit(router, "TransferFailed")
      .withArgs(
        vault.address,
        revertingContract.target,
        ethers.ZeroAddress,
        amount,
        "",
      );

    // The vault should have received the ETH back (minus gas costs)
    // We can't check exact balance due to gas costs, but it should be close
    const finalVaultBalance = await ethers.provider.getBalance(vault.address);
    const balanceDifference = initialVaultBalance - finalVaultBalance;

    // The difference should be much less than the amount (just gas costs)
    expect(balanceDifference).to.be.lessThan(ethers.parseEther("0.01"));
  });

  it("should verify GnosisSafe mock functionality", async function () {
    // Test basic Safe functionality - simplified since proxy may not expose all functions
    expect(await gnosisSafe.isOwner(safeOwner1.address)).to.be.true;
    expect(await gnosisSafe.isOwner(safeOwner2.address)).to.be.true;
    expect(await gnosisSafe.isOwner(user.address)).to.be.false;
    expect(await gnosisSafe.getThreshold()).to.equal(1);

    // Send ETH directly to Safe to test receive function
    const amount = ethers.parseEther("0.1");
    const tx = await user.sendTransaction({
      to: gnosisSafe.target,
      value: amount,
    });

    await expect(tx)
      .to.emit(gnosisSafe, "SafeReceived")
      .withArgs(user.address, amount);

    // Verify the ETH was received by checking balance
    const balance = await ethers.provider.getBalance(gnosisSafe.target);
    expect(balance).to.be.greaterThan(0);
  });

  it("should demonstrate gas consumption difference", async function () {
    // This test demonstrates that the GnosisSafe receive function
    // consumes significantly more than 2300 gas, which would fail with .send()

    const amount = ethers.parseEther("0.1");

    // Estimate gas for sending to the Safe
    const gasEstimate = await ethers.provider.estimateGas({
      to: gnosisSafe.target,
      value: amount,
      from: user.address,
    });

    // The gas estimate should be significantly more than 2300
    console.log(
      `Gas estimate for GnosisSafe receive: ${gasEstimate.toString()} gas`,
    );
    expect(gasEstimate).to.be.greaterThan(2300);

    // NOTE: Real GnosisSafe contracts consume ~27k gas, which is now within our 30k limit
    // This demonstrates that the gas limit fix is sufficient for real GnosisSafe contracts
    // showing the improvement from 2,300 gas to 30,000 gas
    console.log(`Router gas limit: 30,000 gas`);
    console.log(
      `Improvement: ${(30000 / 2300).toFixed(1)}x increase from original .send() limit`,
    );

    // The gas consumption should now be within our 30k limit
    expect(gasEstimate).to.be.lessThanOrEqual(30000);
  });

  it("should allow vault to batch transfer ETH + ERC20 to mix of EOA and Gnosis Safes", async function () {
    const ethAmount = ethers.parseEther("0.2");
    const tokenAmount = ethers.parseEther("5");
    const memo1 = "BATCH:ETH:TO:EOA";
    const memo2 = "BATCH:TOKENS:TO:SAFE";

    // Get initial balances
    const initialUserEthBalance = await ethers.provider.getBalance(
      user.address,
    );
    const initialUserTokenBalance = await standardToken.balanceOf(user.address);
    const initialSafeEthBalance = await ethers.provider.getBalance(
      gnosisSafe.target,
    );
    const initialSafeTokenBalance = await standardToken.balanceOf(
      gnosisSafe.target,
    );
    const initialVaultEthBalance = await ethers.provider.getBalance(
      vault.address,
    );
    const initialVaultTokenBalance = await standardToken.balanceOf(
      vault.address,
    );

    // Approve router to spend tokens from vault
    await standardToken.connect(vault).approve(router.target, tokenAmount);

    // Batch transfer: ETH to user (EOA) and ERC20 tokens to GnosisSafe
    const tx = await router.connect(vault).batchTransferOut(
      [user.address, gnosisSafe.target], // recipients
      [ethers.ZeroAddress, standardToken.target], // assets (ETH, then ERC20)
      [ethAmount, tokenAmount], // amounts
      [memo1, memo2], // memos
      {
        value: ethAmount, // Only ETH value, ERC20 is separate
      },
    );

    // Verify the batch transaction succeeded
    await expect(tx)
      .to.emit(router, "TransferOut")
      .withArgs(
        vault.address,
        user.address,
        ethers.ZeroAddress,
        ethAmount,
        memo1,
      );

    await expect(tx)
      .to.emit(router, "TransferOut")
      .withArgs(
        vault.address,
        gnosisSafe.target,
        standardToken.target,
        tokenAmount,
        memo2,
      );

    // Verify final balances
    const finalUserEthBalance = await ethers.provider.getBalance(user.address);
    const finalUserTokenBalance = await standardToken.balanceOf(user.address);
    const finalSafeEthBalance = await ethers.provider.getBalance(
      gnosisSafe.target,
    );
    const finalSafeTokenBalance = await standardToken.balanceOf(
      gnosisSafe.target,
    );
    const finalVaultEthBalance = await ethers.provider.getBalance(
      vault.address,
    );
    const finalVaultTokenBalance = await standardToken.balanceOf(vault.address);

    // User (EOA) should have received ETH but no tokens
    expect(finalUserEthBalance).to.equal(initialUserEthBalance + ethAmount);
    expect(finalUserTokenBalance).to.equal(initialUserTokenBalance);

    // GnosisSafe should have received tokens but no ETH
    expect(finalSafeEthBalance).to.equal(initialSafeEthBalance);
    expect(finalSafeTokenBalance).to.equal(
      initialSafeTokenBalance + tokenAmount,
    );

    // Vault should have sent ETH and tokens (accounting for gas costs)
    expect(finalVaultEthBalance).to.be.lessThan(
      initialVaultEthBalance - ethAmount,
    );
    expect(finalVaultEthBalance).to.be.greaterThan(
      initialVaultEthBalance - ethAmount - ethers.parseEther("0.01"),
    ); // Allow for gas
    expect(finalVaultTokenBalance).to.equal(
      initialVaultTokenBalance - tokenAmount,
    );
  });
});
