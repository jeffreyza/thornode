// SPDX-License-Identifier: MIT

/**
 * ATTACK DESCRIPTION:
 * This contract tests how the THORChain Router handles ETH transfers to contracts that can't receive ETH:
 * 1. It intentionally lacks both receive() and fallback() functions, making it unable to receive ETH
 * 2. When Router attempts to send ETH to this contract, the transaction should fail
 * 3. This tests if the Router properly handles failed ETH transfers and has a fallback mechanism
 * 4. It verifies whether the Router correctly emits TransferFailed events and returns funds to sender
 */
 pragma solidity 0.8.30;

/**
 * @title NoFallbackContract
 * @dev A contract without receive/fallback functions to test ETH transfers to contracts without ETH receiving capability
 */
contract NoFallbackContract {
    // Intentionally has no receive() or fallback() function
    
    function doSomething() external pure returns (string memory) {
        return "This contract cannot receive ETH";
    }
}
