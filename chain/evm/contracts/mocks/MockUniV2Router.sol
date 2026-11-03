// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

contract MockUniV2Router {
    uint256[] private _amountsOut;
    bool private _shouldFail;

    function setAmountsOut(uint256[] memory amounts) external {
        _amountsOut = amounts;
    }

    function setShouldFail(bool shouldFail) external {
        _shouldFail = shouldFail;
    }

    function getAmountsOut(uint amountIn, address[] calldata path) 
        external 
        view 
        returns (uint[] memory amounts) 
    {
        require(!_shouldFail, "Mock router failure");
        return _amountsOut;
    }

    function swapExactTokensForTokens(
        uint amountIn,
        uint amountOutMin,
        address[] calldata path,
        address to,
        uint deadline
    ) external returns (uint[] memory amounts) {
        require(!_shouldFail, "Mock router failure");
        
        // Simulate successful swap
        amounts = new uint[](2);
        amounts[0] = amountIn;
        amounts[1] = _amountsOut.length > 1 ? _amountsOut[1] : amountIn;
        
        return amounts;
    }

    function swapExactETHForTokens(
        uint amountOutMin,
        address[] calldata path,
        address to,
        uint deadline
    ) external payable returns (uint[] memory amounts) {
        require(!_shouldFail, "Mock router failure");
        
        // Simulate successful swap
        amounts = new uint[](2);
        amounts[0] = msg.value;
        amounts[1] = _amountsOut.length > 1 ? _amountsOut[1] : msg.value;
        
        return amounts;
    }

    function swapExactTokensForETH(
        uint amountIn,
        uint amountOutMin,
        address[] calldata path,
        address to,
        uint deadline
    ) external returns (uint[] memory amounts) {
        require(!_shouldFail, "Mock router failure");
        
        // Simulate successful swap
        amounts = new uint[](2);
        amounts[0] = amountIn;
        amounts[1] = _amountsOut.length > 1 ? _amountsOut[1] : amountIn;
        
        // Send ETH to recipient
        payable(to).transfer(amounts[1]);
        
        return amounts;
    }

    // Allow contract to receive ETH for testing
    receive() external payable {}
} 