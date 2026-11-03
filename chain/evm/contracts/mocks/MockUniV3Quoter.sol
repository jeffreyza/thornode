// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

contract MockUniV3Quoter {
    uint256 private _quoteAmount;
    bool private _shouldFail;

    function setQuoteAmount(uint256 amount) external {
        _quoteAmount = amount;
    }

    function setShouldFail(bool shouldFail) external {
        _shouldFail = shouldFail;
    }

    function quoteExactInputSingle(
        address tokenIn,
        address tokenOut,
        uint24 fee,
        uint256 amountIn,
        uint160 sqrtPriceLimitX96
    ) external view returns (uint256 amountOut) {
        require(!_shouldFail, "Mock quoter failure");
        return _quoteAmount;
    }

    function quoteExactInput(bytes memory path, uint256 amountIn) external view returns (uint256 amountOut) {
        require(!_shouldFail, "Mock quoter failure");
        return _quoteAmount;
    }
} 