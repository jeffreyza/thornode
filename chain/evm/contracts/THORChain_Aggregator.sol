// SPDX-License-Identifier: MIT
// -------------------
// Aggregator Version: 1.0
// -------------------
pragma solidity 0.8.30;



// ERC20 Interface
interface IERC20 {
    function balanceOf(address account) external view returns (uint256);
    function transfer(address recipient, uint256 amount) external returns (bool);
    function transferFrom(address sender, address recipient, uint256 amount) external returns (bool);
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

// Sushi Interface
interface iSWAPROUTER {
    function swapExactTokensForETH(uint256 amountIn, uint256 amountOutMin, address[] calldata path, address to, uint256 deadline) external;
    function swapExactETHForTokens(uint amountOutMin, address[] calldata path, address to, uint deadline) external payable;
    function swapExactTokensForTokens(uint amountIn, uint amountOutMin, address[] calldata path, address to, uint deadline) external returns (uint[] memory amounts);
}

// THORChain_Aggregator is permissionless
contract THORChain_Aggregator {

    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    uint256 private _status;

    address private ETH = address(0);
    iSWAPROUTER public swapRouter;

    modifier nonReentrant() {
        require(_status != _ENTERED, "ReentrancyGuard: reentrant call");
        _status = _ENTERED;
        _;
        _status = _NOT_ENTERED;
    }

    constructor(address _swapRouter) {
        _status = _NOT_ENTERED;
        swapRouter = iSWAPROUTER(_swapRouter);
    }

    receive() external payable {}

     //############################## IN ##############################

    function swapIn(
        address tcVault, 
        address tcRouter, 
        string calldata tcMemo, 
        address fromToken,
        uint amount, 
        uint amountOutMin, 
        uint256 deadline
        ) public nonReentrant {
        return _swapIn(tcVault, tcRouter, tcMemo, fromToken, ETH, amount, amountOutMin, deadline);
    }
    
    function swapInV2(
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
        uint256 _safeAmount = msg.value; // Use the sent ETH
        
        // Handle ETH to Token swap
        if(toToken != ETH) {
            // ETH to Token swap
            address[] memory path = new address[](2);
            path[0] = ETH; path[1] = toToken;
            swapRouter.swapExactETHForTokens{value: _safeAmount}(amountOutMin, path, address(this), deadline);
            
            // Get the swapped token balance
            _safeAmount = IERC20(toToken).balanceOf(address(this));
            
            // Approve router to spend the tokens
            TransferHelper.safeApprove(toToken, tcRouter, _safeAmount);
            
            // Deposit tokens to THORChain via router
            iROUTER(tcRouter).depositWithExpiry(payable(tcVault), toToken, _safeAmount, tcMemo, deadline);
        } else {
            // ETH to ETH (just forward)
            iROUTER(tcRouter).depositWithExpiry{value:_safeAmount}(payable(tcVault), ETH, _safeAmount, tcMemo, deadline);
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
        // Transfer asset from user to this contract
        TransferHelper.safeTransferFrom(fromToken, msg.sender, address(this), amount);
        uint256 _safeAmount = amount; // The amount successfully transferred to this contract
        TransferHelper.safeApprove(fromToken, address(swapRouter), _safeAmount);
        
        // Handle different swap types based on toToken
        if (toToken == ETH) {
            // Token to ETH swap
            address[] memory path = new address[](2);
            path[0] = fromToken; path[1] = ETH;
            swapRouter.swapExactTokensForETH(_safeAmount, amountOutMin, path, address(this), deadline);
            _safeAmount = address(this).balance; // Update _safeAmount to current ETH balance
            
            // Deposit ETH to THORChain via router
            iROUTER(tcRouter).depositWithExpiry{value:_safeAmount}(payable(tcVault), ETH, _safeAmount, tcMemo, deadline);
        } else {
            // Token to Token swap
            address[] memory path = new address[](2);
            path[0] = fromToken; path[1] = toToken;
            swapRouter.swapExactTokensForTokens(_safeAmount, amountOutMin, path, address(this), deadline);
            
            // Get the swapped token balance
            _safeAmount = IERC20(toToken).balanceOf(address(this));
            
            // Approve router to spend the tokens
            TransferHelper.safeApprove(toToken, tcRouter, _safeAmount);
            
            // Deposit tokens to THORChain via router
            iROUTER(tcRouter).depositWithExpiry(payable(tcVault), toToken, _safeAmount, tcMemo, deadline);
        }
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
            _swapOut(toAsset, recipient, amountOutMin);
        } else {
            // First pull tokens from the sender (router) to this contract
            TransferHelper.safeTransferFrom(fromAsset, msg.sender, address(this), fromAmount);
            _swapOutV2(fromAsset, toAsset, recipient, amountOutMin);
        }
    }

    function _swapOut(address token, address to, uint256 amountOutMin) internal {
        // If the target token is ETH (address(0)), just forward the ETH
        if (token == ETH) {
            (bool success, ) = to.call{value: msg.value}("");
            require(success, "ETH transfer failed");
            return;
        }
        
        // Otherwise swap ETH to the requested token
        address[] memory path = new address[](2);
        path[0] = ETH; path[1] = token;
        swapRouter.swapExactETHForTokens{value: msg.value}(amountOutMin, path, to, type(uint).max);
    }

    function _swapOutV2(address fromAsset, address toAsset, address recipient, uint256 amountOutMin) internal {
        uint balance = IERC20(fromAsset).balanceOf(address(this));
        TransferHelper.safeApprove(fromAsset, address(swapRouter), balance);
        // If destination is not ETH, do token to token swap
        if (toAsset != ETH) {
            // Create path from input token to output token
            address[] memory path = new address[](2);
            path[0] = fromAsset; path[1] = toAsset;
            // Execute the swap
            swapRouter.swapExactTokensForTokens(
                balance, // Use our balance as the input amount
                amountOutMin,
                path,
                recipient,
                type(uint).max
            );

        } else {
            address[] memory path = new address[](2);
            path[0] = fromAsset; path[1] = ETH;
            swapRouter.swapExactTokensForETH(
                balance,
                amountOutMin,
                path,
                recipient,
                type(uint).max
            );
        }
    }
}
