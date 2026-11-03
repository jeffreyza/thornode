// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;



// helper methods for interacting with ERC20 tokens and sending ETH that do not consistently return true/false
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

pragma solidity 0.8.30;

// lightweight copy of router
contract SushiRouterSmol {
    modifier ensure(uint deadline) {
        require(deadline >= block.timestamp, 'UniswapV2Router: EXPIRED');
        _;
    }

    uint256 one = 1*10**18;

    constructor() {
    }

    receive() external payable {}

    function swapExactTokensForETH(uint amountIn, uint amountOutMin, address[] calldata path, address to, uint) // deadline (unused)
        external
        virtual
    {
        // Simplified for testing - directly handle token transfers to match brief
        uint256[1] memory amounts = [one];
        require(amounts[amounts.length - 1] >= amountOutMin, 'UniswapV2Router: INSUFFICIENT_OUTPUT_AMOUNT');
        
        // Take tokens from sender
        TransferHelper.safeTransferFrom(
            path[0], msg.sender, address(this), amountIn
        );
        
        // Send ETH directly to recipient
        TransferHelper.safeTransferETH(to, amounts[amounts.length - 1]);
    }

    function swapExactETHForTokens(uint amountOutMin, address[] calldata path, address to, uint /* deadline */)
        external
        virtual
        payable
    {
        // Simplified for testing - directly handle token transfers to match brief
        uint256[1] memory amounts = [one];
        require(amounts[amounts.length - 1] >= amountOutMin, 'UniswapV2Router: INSUFFICIENT_OUTPUT_AMOUNT');
        
        // Send token directly to recipient - no WETH involvement
        TransferHelper.safeTransfer(path[1], to, one);
    }

    function swapExactTokensForTokens(
        uint amountIn,
        uint amountOutMin,
        address[] calldata path,
        address to,
        uint deadline
    ) external virtual ensure(deadline) returns (uint[] memory) {
        uint256[1] memory amounts = [one];
        require(amounts[amounts.length - 1] >= amountOutMin, 'UniswapV2Router: INSUFFICIENT_OUTPUT_AMOUNT');
        
        // Take token1 (path[0]) from sender
        TransferHelper.safeTransferFrom(
            path[0], msg.sender, address(this), amountIn
        );
        
        // Send token2 (path[1]) to recipient
        TransferHelper.safeTransfer(path[1], to, one);

        // Create return array for compatibility with standard Uniswap interface
        uint[] memory returnAmounts = new uint[](2);
        returnAmounts[0] = one;
        returnAmounts[1] = 0;
        
        return returnAmounts;
    }

}
