// SPDX-License-Identifier: MIT
// -------------------
// Aggregator Version: 1.0 - Uniswap V3
// -------------------
pragma solidity 0.8.30;

// ERC20 Interface
interface IERC20 {
    function balanceOf(address account) external view returns (uint256);
    function transfer(address recipient, uint256 amount) external returns (bool);
    function transferFrom(address sender, address recipient, uint256 amount) external returns (bool);
}

// WETH9 Interface
interface IWETH9 {
    function deposit() external payable;
    function withdraw(uint256) external;
}

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
    function depositWithExpiry(address payable to, address asset, uint256 amount, string calldata memo, uint256 expiration) external payable;
}

// Uniswap V3 SwapRouter Interface
interface ISwapRouter {
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

    /// @notice Swaps `amountIn` of one token for as much as possible of another token
    /// @param params The parameters necessary for the swap, encoded as `ExactInputSingleParams` in calldata
    /// @return amountOut The amount of the received token
    function exactInputSingle(ExactInputSingleParams calldata params) external payable returns (uint256 amountOut);
}

// Uniswap V3 Quoter Interface
interface IQuoter {
    /// @notice Returns the amount out received for a given exact input swap without executing the swap
    function quoteExactInput(bytes memory path, uint256 amountIn) external returns (uint256 amountOut);
    
    /// @notice Returns the amount out received for a given exact input but for a single swap
    function quoteExactInputSingle(
        address tokenIn,
        address tokenOut,
        uint24 fee,
        uint256 amountIn,
        uint160 sqrtPriceLimitX96
    ) external returns (uint256 amountOut);
}

// THORChain_AggregatorUniswap is permissionless
contract THORChain_AggregatorUniswap {

    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    uint256 private _status;

    address private constant ETH = address(0);
    address public WETH9;
    address public owner;
    
    // Uniswap V3 Contracts
    ISwapRouter public swapRouter;
    IQuoter public quoter;
    
    // Dynamic fee tiers array for Uniswap V3
    uint24[] public feeTiers;

    // Events
    event Swapped(address indexed fromToken, address indexed toToken, uint256 amountIn, uint256 amountOut);
    event Deposited(address indexed tcVault, address indexed asset, uint256 amount, string memo);
    event SwapFailed(address indexed fromToken, address indexed toToken, uint256 amount);
    event TransferFailed(address indexed token, address indexed to, uint256 amount);
    event EthReceived(uint256 amount);
    event RefundedEth(address indexed to, uint256 amount);
    event OwnershipTransferred(address indexed previousOwner, address indexed newOwner);
    event FeeTiersUpdated(uint24[] feeTiers);

    modifier nonReentrant() {
        require(_status != _ENTERED, "ReentrancyGuard: reentrant call");
        _status = _ENTERED;
        _;
        _status = _NOT_ENTERED;
    }

    modifier onlyOwner() {
        require(msg.sender == owner, "Not owner");
        _;
    }

    constructor() {
        _status = _NOT_ENTERED;
        owner = tx.origin;
        // Initialize fee tiers to common Uniswap V3 values
        feeTiers.push(3000);  // 0.3% (most common)
        feeTiers.push(500);   // 0.05%
        feeTiers.push(10000); // 1%
    }

    function setUniswapAddresses(address _swapRouter, address _quoter, address _weth) external onlyOwner {
        swapRouter = ISwapRouter(_swapRouter);
        quoter = IQuoter(_quoter);
        WETH9 = _weth;
    }

    function setFeeTiers(uint24[] calldata _feeTiers) external onlyOwner {
        require(_feeTiers.length > 0 && _feeTiers.length <= 10, "Invalid fee tiers length");
        for (uint i = 0; i < _feeTiers.length; i++) {
            require(_feeTiers[i] > 0, "Fee tier must be greater than 0");
        }
        delete feeTiers;
        for (uint i = 0; i < _feeTiers.length; i++) {
            feeTiers.push(_feeTiers[i]);
        }
        emit FeeTiersUpdated(_feeTiers);
    }

    function transferOwnership(address newOwner) external onlyOwner {
        require(newOwner != address(0), "New owner cannot be zero address");
        address oldOwner = owner;
        owner = newOwner;
        emit OwnershipTransferred(oldOwner, newOwner);
    }

    receive() external payable {
        emit EthReceived(msg.value);
    }

    // Helper function to validate ETH usage with ERC20 transactions
    function _validateEthUsage(address token) internal view {
        if(token != ETH && msg.value > 0) {
            revert("ETH sent with ERC20 operation");
        }
    }

    /**
     * @dev Gas-efficient fee discovery that tries fee tiers in order of likelihood
     * @param tokenIn Input token address
     * @param tokenOut Output token address  
     * @param amountIn Input amount
     * @return bestFee Best fee tier found (0 if none work)
     */
    function _findBestFee(address tokenIn, address tokenOut, uint256 amountIn) internal returns (uint24 bestFee) {
        // Try all configured fee tiers
        for (uint256 i = 0; i < feeTiers.length;) {
            try quoter.quoteExactInputSingle(tokenIn, tokenOut, feeTiers[i], amountIn, 0) returns (uint256 quote) {
                if (quote > 0) {
                    return feeTiers[i];
                }
            } catch {
                // Continue to next fee tier
            }
            unchecked { ++i; }
        }
        
        return 0; // No working fee tier found
    }

    /**
     * @dev Gas-efficient swap execution with fee discovery
     * @param tokenIn Input token address
     * @param tokenOut Output token address
     * @param amountIn Input amount
     * @param amountOutMin Minimum output amount
     * @param deadline Transaction deadline
     * @return amountOut Actual output amount
     */
    function _executeSwapWithFeeDiscovery(
        address tokenIn,
        address tokenOut,
        uint256 amountIn,
        uint256 amountOutMin,
        uint256 deadline
    ) internal returns (uint256 amountOut) {
        // Find best fee tier
        uint24 bestFee = _findBestFee(tokenIn, tokenOut, amountIn);
        
        // Use first fee tier as fallback if no optimal fee found
        if (bestFee == 0 && feeTiers.length > 0) {
            bestFee = feeTiers[0];
        }
        
        // Execute swap with discovered fee
        try swapRouter.exactInputSingle(ISwapRouter.ExactInputSingleParams({
            tokenIn: tokenIn,
            tokenOut: tokenOut,
            fee: bestFee,
            recipient: address(this),
            deadline: deadline,
            amountIn: amountIn,
            amountOutMinimum: amountOutMin,
            sqrtPriceLimitX96: 0
        })) returns (uint256 _amountOut) {
            return _amountOut;
        } catch {
            revert("Swap failed");
        }
    }

    //############################## IN ##############################


    function swapIn(
        address tcVault, 
        address tcRouter, 
        string calldata tcMemo, 
        address fromToken,
        address toToken,
        uint amount, 
        uint amountOutMin, 
        uint256 deadline
        ) public payable nonReentrant {
        // Check if fromToken is ETH (special case - direct ETH input)
        if(fromToken == ETH) {
            require(msg.value > 0, "TC:eth amount mismatch");
            require(amount == msg.value, "TC:eth amount mismatch");
            _swapInETH(tcVault, tcRouter, tcMemo, toToken, amountOutMin, deadline);
        } else {
            // Validate no ETH is sent with token operations
            _validateEthUsage(fromToken);
            _swapIn(tcVault, tcRouter, tcMemo, fromToken, toToken, amount, amountOutMin, deadline);
        }
    }
    
    function _swapInETH(
        address tcVault, 
        address tcRouter, 
        string calldata tcMemo, 
        address toToken, 
        uint amountOutMin, 
        uint256 deadline
        ) internal {
        uint256 _safeAmount = msg.value;

        // If target token is not ETH, perform ETH to Token swap
        if (toToken != ETH) {
            // Wrap ETH to WETH first (required for Uniswap V3)
            IWETH9(WETH9).deposit{value: _safeAmount}();
            
            // Approve the router to spend WETH
            TransferHelper.safeApprove(WETH9, address(swapRouter), _safeAmount);
            
            // Find optimal fee tier and execute swap
            _safeAmount = _executeSwapWithFeeDiscovery(WETH9, toToken, _safeAmount, amountOutMin, deadline);
            emit Swapped(ETH, toToken, msg.value, _safeAmount);
            
            // Approve router to spend the target token
            TransferHelper.safeApprove(toToken, tcRouter, _safeAmount);
            
            // Deposit tokens to THORChain via router
            iROUTER(tcRouter).depositWithExpiry(payable(tcVault), toToken, _safeAmount, tcMemo, deadline);
            emit Deposited(tcVault, toToken, _safeAmount, tcMemo);
        } else {
            // ETH to ETH (just forward)
            iROUTER(tcRouter).depositWithExpiry{value:_safeAmount}(payable(tcVault), ETH, _safeAmount, tcMemo, deadline);
            emit Deposited(tcVault, ETH, _safeAmount, tcMemo);
        }
    }
    
    function _swapIn(
        address tcVault, 
        address tcRouter, 
        string calldata tcMemo, 
        address fromToken,
        address toToken,
        uint amount, 
        uint amountOutMin, 
        uint256 deadline
        ) internal {
        // Transfer tokens from sender to this contract
        uint256 _safeAmount = amount;
        TransferHelper.safeTransferFrom(fromToken, msg.sender, address(this), _safeAmount);
        
        // Approve router to spend the fromToken
        TransferHelper.safeApprove(fromToken, address(swapRouter), _safeAmount);
        
        // Handle different swap types based on toToken
        if (toToken == ETH) {
            // Token to ETH swap - swap to WETH first then unwrap
            uint256 amountOut = _executeSwapWithFeeDiscovery(fromToken, WETH9, _safeAmount, amountOutMin, deadline);
            
            // Unwrap WETH to ETH
            IWETH9(WETH9).withdraw(amountOut);
            uint256 ethAmount = address(this).balance;
            
            // Deposit ETH to THORChain via router
            iROUTER(tcRouter).depositWithExpiry{value: ethAmount}(payable(tcVault), ETH, ethAmount, tcMemo, deadline);
            emit Swapped(fromToken, ETH, amount, ethAmount);
            emit Deposited(tcVault, ETH, ethAmount, tcMemo);
        } else {
            // Token to Token swap
            uint256 amountOut = _executeSwapWithFeeDiscovery(fromToken, toToken, _safeAmount, amountOutMin, deadline);
            
            // Approve router to spend the output tokens
            TransferHelper.safeApprove(toToken, tcRouter, amountOut);
            
            // Deposit tokens to THORChain via router
            iROUTER(tcRouter).depositWithExpiry(payable(tcVault), toToken, amountOut, tcMemo, deadline);
            emit Swapped(fromToken, toToken, amount, amountOut);
            emit Deposited(tcVault, toToken, amountOut, tcMemo);
        }
    }

    //############################## QUOTE ##############################
    
    /// @notice Get quote for swap from one token to another
    /// @param fromToken Address of the input token
    /// @param toToken Address of the output token
    /// @param amount Amount of input token
    /// @return amountOut The amount of output token that would be received
    function quoteSwapIn(
        address fromToken,
        address toToken, 
        uint256 amount
    ) public returns (uint256 amountOut) {
        // For ETH input, use WETH address
        address actualFromToken = fromToken == ETH ? WETH9 : fromToken;
        address actualToToken = toToken == ETH ? WETH9 : toToken;
        
        // Create path for single hop swap (fromToken -> toToken with first fee tier)
        uint24 quoteFee = feeTiers.length > 0 ? feeTiers[0] : 3000; // fallback to 0.3%
        bytes memory path = abi.encodePacked(actualFromToken, quoteFee, actualToToken);
        
        return quoter.quoteExactInput(path, amount);
    }

    //############################## OUT ##############################

    function swapOut(address token, address to, uint256 amountOutMin) external payable nonReentrant {
        _swapOut(token, to, amountOutMin);
    }

    // V2 version of swapOut that handles both ETH and token inputs
    function swapOutV2(
        address fromAsset,
        uint256 fromAmount,
        address toAsset,
        address recipient,
        uint256 amountOutMin,
        bytes calldata,  // payload (unused)
        string calldata  // originAddress (unused)
    ) public payable nonReentrant {
        
        // If we're receiving ETH, call swapOut
        if (fromAsset == ETH) {
            // Validate ETH amount
            require(msg.value > 0, "TC:eth amount mismatch");
            require(msg.value == fromAmount, "TC:eth amount mismatch");
            _swapOut(toAsset, recipient, amountOutMin);
        } else {
            // Validate no ETH is sent with token operations
            _validateEthUsage(fromAsset);
            // First pull tokens from the sender (router) to this contract
            TransferHelper.safeTransferFrom(fromAsset, msg.sender, address(this), fromAmount);
            _swapOutV2(fromAsset, toAsset, recipient, amountOutMin);
        }
    }

    function _swapOut(address token, address to, uint256 amountOutMin) internal {
        // If the target token is ETH (address(0)), just forward the ETH
        if (token == ETH) {
            (bool success, ) = to.call{value: msg.value}("");
            if (!success) {
                emit TransferFailed(ETH, to, msg.value);
                revert("ETH transfer failed");
            }
            return;
        }
        
        // Otherwise swap ETH to the requested token
        // First wrap ETH to WETH (required for Uniswap V3)
        IWETH9(WETH9).deposit{value: msg.value}();
        
        // Approve the router to spend WETH
        TransferHelper.safeApprove(WETH9, address(swapRouter), msg.value);
        
        // Find best fee for WETH -> token swap
        uint24 swapFee = _findBestFee(WETH9, token, msg.value);
        if (swapFee == 0 && feeTiers.length > 0) {
            swapFee = feeTiers[0]; // fallback to first fee tier
        }
        
        // Swap WETH for the target token
        ISwapRouter.ExactInputSingleParams memory params = ISwapRouter.ExactInputSingleParams({
            tokenIn: WETH9,
            tokenOut: token,
            fee: swapFee,
            recipient: to,
            deadline: type(uint256).max,
            amountIn: msg.value,
            amountOutMinimum: amountOutMin,
            sqrtPriceLimitX96: 0
        });
        
        try swapRouter.exactInputSingle(params) returns (uint256 amountOut) {
            emit Swapped(ETH, token, msg.value, amountOut);
        } catch {
            emit SwapFailed(ETH, token, msg.value);
            revert("Swap to token failed");
        }
    }

    function _swapOutV2(address fromAsset, address toAsset, address recipient, uint256 amountOutMin) internal {
        uint256 balance = IERC20(fromAsset).balanceOf(address(this));
        TransferHelper.safeApprove(fromAsset, address(swapRouter), balance);
        
        // If destination is not ETH, do token to token swap
        if (toAsset != ETH) {
            // Find best fee for token -> token swap
            uint24 swapFee = _findBestFee(fromAsset, toAsset, balance);
            if (swapFee == 0 && feeTiers.length > 0) {
                swapFee = feeTiers[0]; // fallback to first fee tier
            }
            
            // Create params for token to token swap
            ISwapRouter.ExactInputSingleParams memory params = ISwapRouter.ExactInputSingleParams({
                tokenIn: fromAsset,
                tokenOut: toAsset,
                fee: swapFee,
                recipient: recipient,
                deadline: type(uint256).max,
                amountIn: balance,
                amountOutMinimum: amountOutMin,
                sqrtPriceLimitX96: 0
            });
            
            try swapRouter.exactInputSingle(params) returns (uint256 amountOut) {
                emit Swapped(fromAsset, toAsset, balance, amountOut);
            } catch {
                emit SwapFailed(fromAsset, toAsset, balance);
                revert("Token to token swap failed");
            }
        } else {
            // Token to ETH swap requires WETH unwrapping
            // Find best fee for token -> WETH swap
            uint24 swapFee = _findBestFee(fromAsset, WETH9, balance);
            if (swapFee == 0 && feeTiers.length > 0) {
                swapFee = feeTiers[0]; // fallback to first fee tier
            }
            
            ISwapRouter.ExactInputSingleParams memory params = ISwapRouter.ExactInputSingleParams({
                tokenIn: fromAsset,
                tokenOut: WETH9,
                fee: swapFee,
                recipient: address(this), // First receive WETH here
                deadline: type(uint256).max,
                amountIn: balance,
                amountOutMinimum: amountOutMin,
                sqrtPriceLimitX96: 0
            });
            
            try swapRouter.exactInputSingle(params) returns (uint256 amountOut) {
                // Unwrap WETH to ETH and send to recipient
                IWETH9(WETH9).withdraw(amountOut);
                
                // Forward ETH to recipient
                (bool success, ) = recipient.call{value: amountOut}("");
                if (!success) {
                    emit TransferFailed(ETH, recipient, amountOut);
                    revert("ETH transfer failed");
                }
                
                emit Swapped(fromAsset, ETH, balance, amountOut);
            } catch {
                emit SwapFailed(fromAsset, ETH, balance);
                revert("Token to ETH swap failed");
            }
        }
    }
}
