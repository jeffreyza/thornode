// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

/**
 * ATTACK DESCRIPTION: DIRECT FUNCTION REENTRANCY ATTACK
 * 
 * This contract attempts a direct same-function reentrancy attack specifically on the Router's
 * deposit functionality:
 * 
 * 1. FOCUSED ATTACK VECTOR: Unlike EvilCallback which tests multiple entry points, this contract
 *    focuses solely on the depositWithExpiry function
 * 
 * 2. SAME-FUNCTION REENTRANCY: It attempts to call the exact same function (depositWithExpiry) 
 *    recursively when receiving ETH, rather than cross-function reentrancy
 * 
 * 3. DIRECT ROUTER INTERACTION: The attackDeposit function directly calls depositWithExpiry with
 *    ETH value, then attempts to reenter it from the receive function
 * 
 * 4. SIMPLE TRACKING: It uses attackCount to track how many times reentrancy was attempted
 * 
 * This tests the Router's protection specifically against the most direct form of reentrancy where
 * a function tries to call itself recursively through an external call that sends ETH.
 *
 * USAGE IN TESTS:
 * - Deploy this contract and pass it the router address
 * - Call attackDeposit() with test parameters and ETH value
 * - Check the attackCount to see if the attacker was able to reenter
 * - Verify only one Deposit event was emitted from the Router
 */

interface IRouter {
    function depositWithExpiry(address, address, uint, string calldata, uint) external payable;
}
contract ReentrancyAttacker {
    IRouter public router;
    uint256 public attackCount = 0;
    
    constructor(address _router) {
        router = IRouter(_router);
    }
    
    // Attack function to test reentrancy in deposit
    function attackDeposit(address payable vault, string memory memo) external payable {
        router.depositWithExpiry{value: msg.value}(
            vault,
            address(0), // ETH
            msg.value,
            memo,
            type(uint).max
        );
    }
    
    // Fallback to attempt reentrancy on receiving ETH
    receive() external payable {
        if (attackCount < 5) { // Limit to prevent infinite loops
            attackCount++;
            
            // Try to reenter router's deposit function
            // Success value intentionally ignored as this is just an attack simulation
            (bool success, ) = address(router).call{value: msg.value}(
                abi.encodeWithSignature(
                    "depositWithExpiry(address,address,uint256,string,uint256)",
                    msg.sender, 
                    address(0),
                    msg.value,
                    "REENTRY",
                    type(uint).max
                )
            );
            success; // Use the variable to avoid warning
        }
    }
}
