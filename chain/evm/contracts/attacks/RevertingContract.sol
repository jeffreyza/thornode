// SPDX-License-Identifier: MIT
// -------------------
// RevertingContract v1.0
// -------------------

/**
 * ATTACK DESCRIPTION:
 * This contract tests how the THORChain Router handles ETH transfers to contracts that always revert:
 * 1. It has a receive() function that always reverts when receiving ETH
 * 2. When the Router attempts to send ETH to this contract, the transaction will always fail
 * 3. This tests the Router's fallback mechanism for failed ETH transfers
 * 4. It verifies if the Router correctly returns ETH to the sender and emits TransferFailed events
 */

pragma solidity 0.8.30;

contract RevertingContract {
    receive() external payable {
        revert();
    }
}
