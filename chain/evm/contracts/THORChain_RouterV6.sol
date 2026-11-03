// SPDX-License-Identifier: MIT
// -------------------
// Router Version: 6.1
// -------------------
pragma solidity 0.8.30;

// ERC20 Interface
interface iERC20 {
    function balanceOf(address) external view returns (uint256);
}
// ROUTER Interface
interface iROUTER {
    function depositWithExpiry(address, address, uint, string calldata, uint) external;
}

// THORChain_Router
contract THORChain_Router {

    bytes4 private constant TRANSFER_SIG = 0xa9059cbb; // transfer(address,uint256)
    bytes4 private constant TRANSFER_FROM_SIG = 0x23b872dd; // transferFrom(address,address,uint256)
    bytes4 private constant APPROVE_SIG = 0x095ea7b3; // approve(address,uint256)
    bytes4 private constant SWAP_OUT_SIG = 0x48c314f4; // swapOut(address,address,uint256)
    bytes4 private constant SWAP_OUT_V2_SIG = 0x486e77ba; // swapOutV2(address,uint256,address,address,uint256,bytes,string)

    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    address private constant ETH_ASSET = address(0);
    uint256 private _status = _NOT_ENTERED;  // Initialize explicitly for better test compatibility

    struct Params {
        address payable target;
        address fromAsset;
        uint256 fromAmount;
        address toAsset;
        address recipient;
        uint256 amountOutMin;
        string memo;
        bytes payload;
        string originAddress;
      }

    // Emitted for deposits
    event Deposit(address indexed to, address indexed asset, uint amount, string memo);

    // Emitted for outgoing transfers
    event TransferOut(address indexed vault, address indexed to, address asset, uint amount, string memo);

    // Emitted for outgoing transferAndCalls
    event TransferOutAndCall(address indexed vault, address target, uint amount, address finalAsset, address to, uint256 amountOutMin, string memo);
    event TransferOutAndCallV2(address indexed vault, address target, address fromAsset, uint fromAmount, address toAsset, address recipient, uint256 amountOutMin, string memo, bytes payload, string originAddress);

    // Emitted for failed transfers
    event TransferFailed(address indexed vault, address indexed to, address asset, uint amount, string memo);

    // Tracks balance changes between vaults using old router function
    event TransferAllowance(address indexed oldVault, address indexed newVault, address asset, uint amount, string memo);

    modifier nonReentrant() {
        require(_status != _ENTERED, "TC:reentrant");
        uint256 oldStatus = _status;
        _status = _ENTERED;
        _;
        _status = oldStatus;
    }

    //############################## DEPOSITS ##############################

    // User Deposit with Expiry 
    function depositWithExpiry(address payable vault, address asset, uint amount, string calldata memo, uint expiration) external payable nonReentrant {
        if(expiration != 0){ // Skip if setting 0 for no expiration
            require(block.timestamp < expiration, "TC:expired");
        }
        require(vault != address(this), "TC:vault!=router");
        _validateEthUsage(asset);
        uint safeAmount = _executeTransfer(vault, asset, amount);
        emit Deposit(vault, asset, safeAmount, memo);
    }

    //############################## TRANSFERS ##############################

    // Any vault calls to transfer any asset to any recipient.
    function transferOut(address payable to, address asset, uint amount, string calldata memo) external payable nonReentrant {
        _validateEthUsage(asset);
        _executeTransfer(to, asset, amount);
        emit TransferOut(msg.sender, to, asset, amount, memo);
    }

    // Batch transfer multiple assets in a single transaction
    function batchTransferOut(address payable[] calldata to, address[] calldata assets, uint[] calldata amounts, string[] calldata memos) external payable nonReentrant {
        require(
            to.length == assets.length && 
            to.length == amounts.length && 
            to.length == memos.length,
            "TC:length mismatch"
        );
        
        // Track total ETH needed for transfers
        uint256 ethRemaining = msg.value;
        
        // If the total ETH to transfer exceeds msg.value, the last one will be reduced
        for (uint i = 0; i < to.length;) {
            // If ETH transfer, track remaining ETH
            if (assets[i] == ETH_ASSET) {
                uint256 safeAmount = amounts[i] > ethRemaining ? ethRemaining : amounts[i];
                unchecked { ethRemaining -= safeAmount; }
                _sendEth(to[i], safeAmount);
                emit TransferOut(msg.sender, to[i], assets[i], safeAmount, memos[i]);
            } else {
                // For ERC20, just do the transfer
                _safeTransferFrom(assets[i], amounts[i], to[i]);
                emit TransferOut(msg.sender, to[i], assets[i], amounts[i], memos[i]);
            }
            unchecked {
                ++i;
            }
        }
        require(ethRemaining == 0, "TC:eth amount mismatch");
    }

    function _executeTransfer(address payable to, address asset, uint amount) internal returns (uint transferredAmount) {
        if (asset == ETH_ASSET) {
            // Handle ETH transfer
            require(msg.value == amount, "TC:eth amount mismatch");
            _sendEth(to, msg.value);
            transferredAmount = msg.value;
        } else {
            // Handle ERC20 transfer
            transferredAmount = _safeTransferFrom(asset, amount, to);
        }
        return transferredAmount;
    }

    //############################## AGGREGATION ##############################
    
    // Transfer and Call() with ETH
    function transferOutAndCall(address payable target, address finalToken, address to, uint256 amountOutMin, string calldata memo) external payable nonReentrant {
        (bool success, ) = target.call{value:msg.value}(abi.encodeWithSelector(SWAP_OUT_SIG, finalToken, to, amountOutMin));
        if (!success) {
            _sendEth(payable(to), msg.value); // If can't swap, just send to recipient
            emit TransferFailed(msg.sender, to, finalToken, msg.value, memo);
        }
        emit TransferOutAndCall(msg.sender, target, msg.value, finalToken, to, amountOutMin, memo);
    }

    // Transfer and Call() with ETH or ERC20
    function transferOutAndCallV2(Params calldata params) external payable nonReentrant {
        if (params.fromAsset == ETH_ASSET) {
            // Try to call, if fail, send ETH to recipient
            (bool success, ) = params.target.call{value:msg.value}(abi.encodeWithSelector(SWAP_OUT_V2_SIG, params.fromAsset, params.fromAmount, params.toAsset, params.recipient, params.amountOutMin, params.payload, params.originAddress));
            if (!success) {
                _sendEth(payable(params.recipient), msg.value); // If can't swap, just send to recipient
                emit TransferFailed(msg.sender, params.recipient, params.fromAsset, msg.value, params.memo);
            }
        } else {
            // Try to call, if fail, send tokens to recipient
            _validateEthUsage(params.fromAsset);
            uint safeAmount = _safeTransferFrom(params.fromAsset, params.fromAmount, address(this));
            _safeApprove(params.fromAsset, params.target, safeAmount);
            (bool success, ) = params.target.call(abi.encodeWithSelector(SWAP_OUT_V2_SIG, params.fromAsset, safeAmount, params.toAsset, params.recipient, params.amountOutMin, params.payload, params.originAddress));
            if (!success) {
                // If swap fails, zero out the approval
                _safeApprove(params.fromAsset, params.target, 0); 
                // Then send directly to recipient ignoring warnings
                // If we failed this, the Bifrost will infinite loop and use up gas
                params.fromAsset.call(abi.encodeWithSelector(TRANSFER_SIG, params.recipient, safeAmount));
                emit TransferFailed(msg.sender, params.recipient, params.fromAsset, params.fromAmount, params.memo);
            }
        }
        emit TransferOutAndCallV2(msg.sender, params.target, params.fromAsset, params.fromAmount, params.toAsset, params.recipient, params.amountOutMin, params.memo, params.payload, params.originAddress);
    }

    //##################### OLD ROUTER FUNCTIONS ######################

    // These two functions are backwards compatible with old router version
    // They are can be deprecated by a Bifrost upgrade

    // Get the token balance of a vault by wrapping the ERC20 method
    // Can be deprecated if Bifrost calls balanceOf directly on the vault
    function vaultAllowance(address vault, address token) external view returns(uint amount){
        return iERC20(token).balanceOf(vault);
    }

    // Use for churning ERC20s (not ETH) to a new vault, as well as router migrations
    // A router is needed to emit a txOut Memo at some point
    // Can be deprecated if Bifrost transfers assets directly to the new vault with no memo
    function transferAllowance(address router, address newVault, address asset, uint amount, string calldata memo) external nonReentrant {
        require(asset != ETH_ASSET, "TC:ETH unsupported");
        if (router == address(this)){
            // Retrieve asset from vault, send to new vault
            // TransferAllowance event is monitored by Bifrost
            _safeTransferFrom(asset, amount, payable(newVault));
            emit TransferAllowance(msg.sender, newVault, asset, amount, memo);
        } else {
            // Else transfer assets to new router/vault using deposit function to emit the memo
            // Bifrost will be monitoring the new router at this point to parse the deposit event (and memo)
            uint safeAmount = _safeTransferFrom(asset, amount, address(this)); // First get tokens from vault
            _safeApprove(asset, router, safeAmount); // Then approve new router
            iROUTER(router).depositWithExpiry(newVault, asset, safeAmount, memo, type(uint).max); // Transfer by depositing
        }
    }

    //############################## HELPERS ##############################

    // Safe transferFrom in case asset charges transfer fees
    function _safeTransferFrom(address _asset, uint _amount, address _to) internal returns(uint amount) {
        uint _startBal = iERC20(_asset).balanceOf(_to);
        (bool success, bytes memory data) = _asset.call(abi.encodeWithSelector(TRANSFER_FROM_SIG, msg.sender, _to, _amount));
        require(success && (data.length == 0 || abi.decode(data, (bool))), "TC:transfer failed");
        return (iERC20(_asset).balanceOf(_to) - _startBal);
    }
    
    // Safe approve for ERC20 tokens
    function _safeApprove(address _asset, address _spender, uint _amount) internal {
        (bool success, bytes memory data) = _asset.call(abi.encodeWithSelector(APPROVE_SIG, _spender, _amount));
        require(success && (data.length == 0 || abi.decode(data, (bool))), "TC:approve failed");
    }

    // Recipients of ETH are given 30,000 Gas to complete execution (enough for GnosisSafe and other complex multisigs).
    function _sendEth(address payable to, uint256 amount) internal {
        (bool success, ) = to.call{value: amount, gas: 30000}("");
        if (!success) {
            payable(msg.sender).transfer(amount); // If failure send back
            emit TransferFailed(msg.sender, to, ETH_ASSET, amount, "");
        }
    }
    
    // Helper function to validate ETH usage for ERC20 transfers
    function _validateEthUsage(address asset) internal view {
        if (asset != ETH_ASSET) {
            require(msg.value == 0, "TC:unexpected eth");
        }
    }
}