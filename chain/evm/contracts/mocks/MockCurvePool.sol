// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

interface IERC20 {
    function transfer(address to, uint256 amount) external returns (bool);
    function transferFrom(address from, address to, uint256 amount) external returns (bool);
    function balanceOf(address account) external view returns (uint256);
}

/**
 * @title MockCurvePool
 * @dev A mock implementation of Curve.fi pool interface for testing
 */
contract MockCurvePool {
    address[] public tokens;
    address[] public underlyingTokens;
    uint256 public outputAmount;
    bool public shouldFail;
    bool public directExchangeShouldFail;
    
    constructor() {
        // Initialize with empty arrays
        tokens = new address[](0);
        underlyingTokens = new address[](0);
        outputAmount = 1e18; // Default to 1 token output for liquidity detection
        shouldFail = false;
        directExchangeShouldFail = false;
    }
    
    // Set the tokens in the pool
    function setTokens(address[] memory _tokens) external {
        tokens = _tokens;
    }
    
    // Set the underlying tokens in the pool
    function setUnderlyingTokens(address[] memory _underlyingTokens) external {
        underlyingTokens = _underlyingTokens;
    }
    
    // Set the output amount for swaps
    function setOutputAmount(uint256 _outputAmount) external {
        outputAmount = _outputAmount;
    }
    
    // Set whether all operations should fail
    function setShouldFail(bool _shouldFail) external {
        shouldFail = _shouldFail;
    }
    
    // Set whether direct exchange should fail (to test underlying)
    function setDirectExchangeShouldFail(bool _shouldFail) external {
        directExchangeShouldFail = _shouldFail;
    }
    
    // Curve interface functions
    function exchange(int128 i, int128 j, uint256 dx, uint256 min_dy) external payable returns (uint256) {
        require(!shouldFail, "Exchange failed");
        require(!directExchangeShouldFail, "Direct exchange failed");
        
        // Calculate output amount using same logic as get_dy
        uint256 actualOutput;
        if (outputAmount != 1e18) {
            actualOutput = outputAmount;
        } else {
            actualOutput = (dx * 95) / 100;
        }
        
        require(actualOutput >= min_dy, "Insufficient output amount");
        
        // Simulate token transfer for output
        uint256 jIndex = j >= 0 ? uint256(uint128(j)) : 0;
        require(jIndex < tokens.length, "Token index out of range");
        
        address tokenOut = tokens[jIndex];
        if (tokenOut == address(0)) {
            // ETH output - send ETH
            require(address(this).balance >= actualOutput, "Insufficient ETH balance");
            payable(msg.sender).transfer(actualOutput);
        } else {
            // ERC20 output - send tokens
            require(IERC20(tokenOut).balanceOf(address(this)) >= actualOutput, "Insufficient token balance");
            IERC20(tokenOut).transfer(msg.sender, actualOutput);
        }
        
        return actualOutput;
    }
    
    function exchange_underlying(int128 i, int128 j, uint256 dx, uint256 min_dy) external payable returns (uint256) {
        require(!shouldFail, "Exchange underlying failed");
        
        // Calculate output amount using same logic as get_dy_underlying
        uint256 actualOutput;
        if (outputAmount != 1e18) {
            actualOutput = outputAmount;
        } else {
            actualOutput = (dx * 95) / 100;
        }
        
        require(actualOutput >= min_dy, "Insufficient output amount");
        
        // Simulate token transfer for output
        uint256 jIndex = j >= 0 ? uint256(uint128(j)) : 0;
        require(jIndex < underlyingTokens.length, "Underlying token index out of range");
        
        address tokenOut = underlyingTokens[jIndex];
        if (tokenOut == address(0)) {
            // ETH output - send ETH
            require(address(this).balance >= actualOutput, "Insufficient ETH balance");
            payable(msg.sender).transfer(actualOutput);
        } else {
            // ERC20 output - send tokens
            require(IERC20(tokenOut).balanceOf(address(this)) >= actualOutput, "Insufficient token balance");
            IERC20(tokenOut).transfer(msg.sender, actualOutput);
        }
        
        return actualOutput;
    }
    
    function get_dy(int128 i, int128 j, uint256 dx) external view returns (uint256) {
        require(!shouldFail, "Get dy failed");
        require(!directExchangeShouldFail, "Direct get_dy failed");
        require(i >= 0 && i < int128(uint128(tokens.length)), "Invalid token index i");
        require(j >= 0 && j < int128(uint128(tokens.length)), "Invalid token index j");
        require(i != j, "Same token swap");
        
        // If outputAmount is set to a specific value, use it
        if (outputAmount != 1e18) {
            return outputAmount;
        }
        
        // Otherwise, simulate realistic exchange rate (95% of input for stablecoins)
        return (dx * 95) / 100;
    }
    
    function get_dy_underlying(int128 i, int128 j, uint256 dx) external view returns (uint256) {
        require(!shouldFail, "Get dy underlying failed");
        
        // If outputAmount is set to a specific value, use it
        if (outputAmount != 1e18) {
            return outputAmount;
        }
        
        // Otherwise, simulate realistic exchange rate (95% of input for underlying tokens)
        return (dx * 95) / 100;
    }
    
    function coins(uint256 i) external view returns (address) {
        require(!shouldFail, "Coins failed");
        if (i < tokens.length) {
            return tokens[i];
        }
        revert("Index out of range");
    }
    
    function underlying_coins(uint256 i) external view returns (address) {
        require(!shouldFail, "Underlying coins failed");
        if (i < underlyingTokens.length) {
            return underlyingTokens[i];
        }
        revert("Index out of range");
    }
    
    // Handle incoming ETH
    receive() external payable {}
}
