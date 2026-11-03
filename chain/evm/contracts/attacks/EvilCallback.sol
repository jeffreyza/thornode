// SPDX-License-Identifier: MIT
// -------------------
// EvilCallback™ v1.0
// -------------------
pragma solidity 0.8.30;

/**
 * @title EvilCallback
 * @dev Contract designed to test reentrancy vulnerabilities in THORChain Router's functions
 * that have external calls, especially those protected by nonReentrant modifier
 * ATTACK DESCRIPTION: COMPREHENSIVE REENTRANCY ATTACK
 * This contract is the most comprehensive reentrancy test, attacking from multiple entry points:
 * 
 * 1. CALLBACK REENTRANCY: When it receives ETH (via receive function), it tries to call back into 
 *    the Router's transferOut function
 * 
 * 2. CROSS-FUNCTION REENTRANCY: In depositWithExpiry, it attempts to call back into the Router 
 *    with a different function to test cross-function protection
 * 
 * 3. DEX CALLBACK REENTRANCY: In swapOut and swapOutV2, it tries to reenter Router functions during
 *    what should be normal DEX integration callbacks
 * 
 * This specifically targets the Router's transferOutAndCall and transferOutAndCallV2 functions,
 * which interact with external DEXes and are potential reentrancy vectors.
 * 
 * The nonReentrant modifier in the Router should prevent these attacks if implemented correctly.
 * The contract tracks successful attacks with the attackCount variable.
 * - Check that only the expected number of events were emitted from the Router
 */

interface iROUTER {
    function transferOut(address payable to, address asset, uint amount, string memory memo) external payable;
    function depositWithExpiry(address payable vault, address asset, uint amount, string memory memo, uint expiry) external payable;
}

contract EvilCallback {
    address public ROUTER;
    uint256 public attackCount = 0;
    
    constructor(address router) {
        ROUTER = router;
    }
    
    /**
     * @dev When THORChain Router sends ETH to this contract, this function is executed.
     * It immediately attempts to call back into the router to test for reentrancy vulnerabilities.
     * This version consumes significant gas to exceed the 30k gas limit.
     */
    receive() external payable {
        if (attackCount < 5) { // Limit to prevent infinite loops
            attackCount++;

            // Consume significant gas before attempting reentrancy
            // This will exceed the 30k gas limit provided by _sendEth
            uint256 gasWaster = 0;
            for (uint256 i = 0; i < 1000; i++) {
                gasWaster = gasWaster + i;
                gasWaster = gasWaster * gasWaster; // Expensive multiplication
                gasWaster = gasWaster % 1000000; // Prevent overflow
            }

            // Try to reenter router by calling transferOut
            // This should be blocked by the nonReentrant modifier
            (bool success, ) = msg.sender.call{
                value: msg.value > 0 ? msg.value / 2 : 0
            }(
                abi.encodeWithSignature(
                    "transferOut(address,address,uint256,string)",
                    payable(address(this)),
                    address(0),
                    msg.value > 0 ? msg.value / 2 : 0,
                    "REENTRANCY_ATTACK"
                )
            );

            // Use the success variable to avoid compiler warning
            if (success) {
                attackCount += 100; // This helps identify if the attack succeeded (should never happen)
            }
        }
    }
    
    /**
     * @dev Fake depositWithExpiry that attempts cross-function reentrancy
     * This is called by the Router during transferOutAndCall tests
     */
    function depositWithExpiry(address payable vault, address asset, uint amount, /*string memory memo, */ uint expiry) external payable {
        // Attempt to call back into the router with a different function
        // to test cross-function reentrancy protection
        (bool success, ) = ROUTER.call{
            value: msg.value > 0 ? msg.value / 2 : 0
        }(
            abi.encodeWithSignature(
                "depositWithExpiry(address,address,uint256,string,uint256)",
                vault,
                asset,
                amount,
                "CROSS_FUNCTION_ATTACK",
                expiry
            )
        );
        
        // Use the success variable to avoid compiler warning
        if (success) {
            attackCount += 100; // This helps identify if the attack succeeded (should never happen)
        }
    }
    
    /**
     * @dev Function to support the Router's swapOut call during transferOutAndCall tests
     */
    function swapOut(address token, address to, uint256 amountOutMin) external payable {
        // Attempt reentrancy when called via transferOutAndCall
        (bool success, ) = msg.sender.call{
            value: msg.value > 0 ? msg.value / 2 : 0
        }(
            abi.encodeWithSignature(
                "transferOut(address,address,uint256,string)",
                payable(to),
                token,
                amountOutMin,
                "SWAP_REENTRANCY_ATTACK"
            )
        );
        
        if (success) {
            attackCount += 100;
        }
    }
    
    /**
     * @dev Function to support the Router's swapOutV2 call during transferOutAndCallV2 tests
     */
    function swapOutV2(/*address fromAsset*/ /*uint256 fromAmount*/ address toAsset, address recipient, uint256 amountOutMin, /*bytes memory payload*/ string memory originAddress) external payable {
        // Attempt reentrancy when called via transferOutAndCallV2
        (bool success, ) = msg.sender.call{
            value: msg.value > 0 ? msg.value / 2 : 0
        }(
            abi.encodeWithSignature(
                "transferOut(address,address,uint256,string)",
                payable(recipient),
                toAsset,
                amountOutMin,
                originAddress
            )
        );
        
        if (success) {
            attackCount += 100;
        }
    }
}
