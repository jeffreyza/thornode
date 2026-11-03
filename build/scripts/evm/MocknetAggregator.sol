// SPDX-License-Identifier: MIT
// Mocknet aggregator used by the simulation framework. It is a stripped down
// THORChain aggregator: swapIn forwards an ERC20 to the THORChain router via
// depositWithExpiry, and swapOut delivers a pre-funded finalToken balance to
// the recipient. Both paths exercise the contract <-> router boundary without
// requiring an on-chain DEX.
pragma solidity 0.8.30;

interface iROUTER {
    function depositWithExpiry(address payable vault, address asset, uint amount, string calldata memo, uint expiration) external payable;
}

interface iERC20 {
    function transfer(address to, uint256 amount) external returns (bool);
    function transferFrom(address from, address to, uint256 amount) external returns (bool);
    function approve(address spender, uint256 amount) external returns (bool);
    function balanceOf(address owner) external view returns (uint256);
}

contract MocknetAggregator {
    address private constant ETH = address(0);

    event SwapIn(address indexed sender, address router, address vault, address fromToken, uint256 amount, string memo);
    event SwapOut(address indexed to, address finalToken, uint256 receivedValue, uint256 amountOutMin, uint256 delivered);

    receive() external payable {}

    // swapIn pulls fromToken from the caller, approves the router and calls
    // depositWithExpiry. The contract acts as an external aggregator that
    // deposits on the user's behalf. fromToken == address(0) signals the
    // caller is sending the gas asset directly via msg.value.
    function swapIn(
        address tcRouter,
        address payable tcVault,
        string calldata tcMemo,
        address fromToken,
        uint256 amount,
        uint256 deadline
    ) external payable {
        if (fromToken == ETH) {
            require(msg.value == amount, "MocknetAggregator: eth amount mismatch");
            iROUTER(tcRouter).depositWithExpiry{value: amount}(tcVault, ETH, amount, tcMemo, deadline);
        } else {
            require(msg.value == 0, "MocknetAggregator: unexpected eth");
            require(iERC20(fromToken).transferFrom(msg.sender, address(this), amount), "MocknetAggregator: transferFrom failed");
            require(iERC20(fromToken).approve(tcRouter, amount), "MocknetAggregator: approve failed");
            iROUTER(tcRouter).depositWithExpiry(tcVault, fromToken, amount, tcMemo, deadline);
        }
        emit SwapIn(msg.sender, tcRouter, tcVault, fromToken, amount, tcMemo);
    }

    // swapOut is invoked by the router via transferOutAndCall. It delivers a
    // fixed `amountOutMin` of the pre-funded finalToken balance to the
    // recipient (or 1 wei when amountOutMin is zero) so a single deployment
    // can serve many simulation swap-outs without draining on the first call.
    function swapOut(address finalToken, address to, uint256 amountOutMin) external payable {
        require(finalToken != ETH, "MocknetAggregator: zero finalToken");
        uint256 deliver = amountOutMin == 0 ? 1 : amountOutMin;
        uint256 balance = iERC20(finalToken).balanceOf(address(this));
        require(balance >= deliver, "MocknetAggregator: insufficient finalToken balance");
        require(iERC20(finalToken).transfer(to, deliver), "MocknetAggregator: finalToken transfer failed");
        emit SwapOut(to, finalToken, msg.value, amountOutMin, deliver);
    }
}
