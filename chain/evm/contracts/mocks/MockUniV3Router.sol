// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

// Import required interfaces and libraries
interface IERC20 {
    function transfer(address to, uint256 amount) external returns (bool);
    function transferFrom(address from, address to, uint256 amount) external returns (bool);
    function balanceOf(address account) external view returns (uint256);
}

interface IWETH {
    function deposit() external payable;
    function withdraw(uint256) external;
    function transfer(address, uint256) external returns (bool);
    function balanceOf(address) external view returns (uint256);
}

contract MockUniV3Router {
    uint256 private _returnAmount;
    bool private _shouldFail;
    address private _returnToken; // Add return token configuration
    address private constant WETH = 0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2;

    struct ExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint24 fee;
        address recipient;
        uint256 deadline;
        uint256 amountIn;
        uint256 amountOutMinimum;
        uint160 sqrtPriceLimitX96;
    }

    struct ExactInputParams {
        bytes path;
        address recipient;
        uint256 deadline;
        uint256 amountIn;
        uint256 amountOutMinimum;
    }

    function setReturnAmount(uint256 amount) external {
        _returnAmount = amount;
    }

    function setShouldFail(bool shouldFail) external {
        _shouldFail = shouldFail;
    }

    function setReturnToken(address token) external {
        _returnToken = token;
    }

    function exactInputSingle(ExactInputSingleParams calldata params) 
        external 
        payable 
        returns (uint256 amountOut) 
    {
        require(!_shouldFail, "Mock V3 router failure");
        
        amountOut = _returnAmount > 0 ? _returnAmount : params.amountIn;
        
        // Handle actual token transfers for better test realism
        if (params.tokenIn != address(0)) {
            // If tokenIn is an actual ERC20, try to transfer it from sender
            (bool success,) = params.tokenIn.call(
                abi.encodeWithSignature("transferFrom(address,address,uint256)", msg.sender, address(this), params.amountIn)
            );
            // Continue regardless of success for testing purposes
        }
        
        // Use configured return token if set, otherwise use params.tokenOut
        address actualTokenOut = _returnToken != address(0) ? _returnToken : params.tokenOut;
        
        if (actualTokenOut != address(0) && params.recipient != address(this)) {
            // If tokenOut is an ERC20, try to transfer tokens to recipient
            IERC20(actualTokenOut).transfer(params.recipient, amountOut);
        } else if (actualTokenOut == address(0) && params.recipient != address(this) && address(this).balance >= amountOut) {
            // If tokenOut is ETH, send ETH to recipient
            (bool success,) = payable(params.recipient).call{value: amountOut}("");
            // Continue regardless of success for testing purposes
        }
        
        return amountOut;
    }

    function exactInput(ExactInputParams calldata params) 
        external 
        payable 
        returns (uint256 amountOut) 
    {
        require(!_shouldFail, "Mock V3 router failure");
        
        amountOut = _returnAmount > 0 ? _returnAmount : params.amountIn;
        
        return amountOut;
    }

    // Allow contract to receive ETH for testing
    receive() external payable {}
} 