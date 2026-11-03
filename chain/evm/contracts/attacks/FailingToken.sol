// SPDX-License-Identifier: MIT

/**
 * ATTACK DESCRIPTION:
 * This contract tests how the THORChain Router handles tokens that can fail transfers:
 * 1. It has a failTransfers flag that can be toggled by the owner
 * 2. When failTransfers is true, all transfer and transferFrom operations revert
 * 3. This tests if the Router has proper error handling for failed token transfers
 * 4. It challenges the Router's ability to detect and handle tokens that initially work
 *    but later start failing (perhaps maliciously)
 */
 pragma solidity 0.8.30;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";

/**
 * @title FailingToken
 * @dev A token that can be configured to fail transfers
 */
contract FailingToken is ERC20, Ownable {
    bool public failTransfers = false;
    
    constructor(string memory name, string memory symbol) ERC20(name, symbol) Ownable(msg.sender) {
        _mint(msg.sender, 1000000 * 10**18);
    }
    
    function setFailTransfers(bool _failTransfers) external onlyOwner {
        failTransfers = _failTransfers;
    }
    
    function transfer(address recipient, uint256 amount) public virtual override returns (bool) {
        require(!failTransfers, "Transfers are disabled");
        return super.transfer(recipient, amount);
    }
    
    function transferFrom(address sender, address recipient, uint256 amount) public virtual override returns (bool) {
        require(!failTransfers, "Transfers are disabled");
        return super.transferFrom(sender, recipient, amount);
    }
}
