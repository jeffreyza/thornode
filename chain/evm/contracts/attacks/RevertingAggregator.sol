// SPDX-License-Identifier: MIT
// -------------------
// Aggregator Version: 1.0
// -------------------

/**
 * ATTACK DESCRIPTION:
 * This contract tests how the THORChain Router handles failing DEX/aggregator interactions:
 * 1. It provides swapOut and swapOutV2 functions that always revert after logging parameters
 * 2. This tests the Router's error handling for failed swaps in transferOutAndCall functions
 * 3. The Router should correctly handle these failures by sending tokens directly to the recipient
 * 4. It verifies whether TransferFailed events are properly emitted
 * 5. It ensures funds aren't locked in the Router when external calls fail
 */
pragma solidity 0.8.30;

// Import console for logging
import "hardhat/console.sol";

// library for transfer helper functions
library TransferHelper {
    function safeApprove(address token, address to, uint value) internal {
        // bytes4(keccak256(bytes('approve(address,uint256)')));
        (bool success, bytes memory data) = token.call(abi.encodeWithSelector(0x095ea7b3, to, value));
        require(success && (data.length == 0 || abi.decode(data, (bool))), 'TransferHelper: APPROVE_FAILED');
    }

    function safeTransfer(address token, address to, uint value) internal {
        // bytes4(keccak256(bytes('transfer(address,uint256)')));
        (bool success, bytes memory data) = token.call(abi.encodeWithSelector(0xa9059cbb, to, value));
        require(success && (data.length == 0 || abi.decode(data, (bool))), 'TransferHelper: TRANSFER_FAILED');
    }

    function safeTransferFrom(address token, address from, address to, uint value) internal {
        // bytes4(keccak256(bytes('transferFrom(address,address,uint256)')));
        (bool success, bytes memory data) = token.call(abi.encodeWithSelector(0x23b872dd, from, to, value));
        require(success && (data.length == 0 || abi.decode(data, (bool))), 'TransferHelper: TRANSFER_FROM_FAILED');
    }

    function safeTransferETH(address to, uint value) internal {
        (bool success,) = to.call{value:value}(new bytes(0));
        require(success, 'TransferHelper: ETH_TRANSFER_FAILED');
    }
}

// THORChain Router Interface
interface iROUTER {
    function depositWithExpiry(address payable to, address asset, uint256 amount, string memory memo, uint256 expiration) external payable;
}

// Sushi Interface
interface iSWAPROUTER {
    function swapExactTokensForETH(uint256 amountIn, uint256 amountOutMin, address[] calldata path, address to, uint256 deadline) external;
    function swapExactETHForTokens(uint amountOutMin, address[] calldata path, address to, uint deadline) external payable;
    function swapExactTokensForTokens(uint amountIn, uint amountOutMin, address[] calldata path, address to, uint deadline) external returns (uint[] memory amounts);
}

// Reverting_Aggregator is permissionless
contract Reverting_Aggregator {

    address private ETH = address(0);
    iSWAPROUTER public swapRouter;

    constructor(address _swapRouter) {
        swapRouter = iSWAPROUTER(_swapRouter);
    }

    receive() external payable {}

     //############################## IN ##############################

    function swapIn(
        address tcVault, 
        address tcRouter, 
        string calldata tcMemo, 
        address token,
        uint amount, 
        uint amountOutMin, 
        uint256 deadline
        ) public {
        TransferHelper.safeTransferFrom(token, msg.sender, address(this), amount); // Transfer asset from user to this contract
        uint256 _safeAmount = amount; // The amount successfully transferred to this contract
        TransferHelper.safeApprove(token, address(swapRouter), _safeAmount);
        address[] memory path = new address[](2);
        path[0] = token; path[1] = ETH;
        swapRouter.swapExactTokensForETH(_safeAmount, amountOutMin, path, address(this), deadline);
        _safeAmount = address(this).balance;
        _safeAmount = address(this).balance; // Update _safeAmount to current ETH balance of the contract for deposit
        iROUTER(tcRouter).depositWithExpiry{value:_safeAmount}(payable(tcVault), ETH, _safeAmount, tcMemo, deadline);
    }

    //############################## OUT ##############################

    function swapOut(address token, address to, uint256 amountOutMin) public payable {
        // Log all parameters before reverting
        console.log("token: %s", token);
        console.log("to: %s", to);
        console.log("amountOutMin: %d", amountOutMin);
        revert("Always fails");
    }

    // V2 version of swapOut that also always reverts
    function swapOutV2(
        address fromAsset,
        uint256 fromAmount,
        address toAsset,
        address recipient,
        uint256 amountOutMin,
        bytes memory payload,
        string memory originAddress
    ) public payable {
        // Log all parameters before reverting
        console.log("fromAsset: %s", fromAsset);
        console.log("fromAmount: %d", fromAmount);
        console.log("toAsset: %s", toAsset);
        console.log("recipient: %s", recipient);
        console.log("amountOutMin: %d", amountOutMin);
        console.log("payload length: %d", payload.length);
        console.log("originAddress: %s", originAddress);
        console.log("msg.value: %d", msg.value);
        
        revert("Always fails V2");
    }
    
}
