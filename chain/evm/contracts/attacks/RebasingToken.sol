// SPDX-License-Identifier: MIT

/**
 * ATTACK DESCRIPTION:
 * This contract tests how the THORChain Router handles tokens with elastic supply (rebasing tokens):
 * 1. It overrides the balanceOf and totalSupply functions to apply a rebase factor
 * 2. This means balances can change without any transfers occurring
 * 3. The attack tests if the Router correctly tracks token amounts when using _safeTransferFrom
 * 4. It challenges the Router's assumption that balance differences accurately reflect transfers
 * 5. This could lead to accounting errors if the Router doesn't properly measure balance changes
 */
 pragma solidity 0.8.30;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";

/**
 * @title RebasingToken
 * @dev A token that can rebase its supply, affecting all balances
 */
contract RebasingToken is ERC20, Ownable {
    // Track original balances
    mapping(address => uint256) private _balances;
    uint256 private _totalSupply;
    
    // Rebase factor (in basis points, 10000 = 100%)
    uint256 public rebaseFactor = 10000;
    
    constructor(string memory name, string memory symbol) ERC20(name, symbol) Ownable(msg.sender) {
        _mint(msg.sender, 1000000 * 10**18);
        _totalSupply = 1000000 * 10**18;
    }
    
    /**
     * @dev Rebase token supply
     * @param newRebaseFactor New rebase factor in basis points (10000 = 100%)
     */
    function rebase(uint256 newRebaseFactor) external onlyOwner {
        rebaseFactor = newRebaseFactor;
    }
    
    /**
     * @dev Override balanceOf to apply rebase factor
     */
    function balanceOf(address account) public view virtual override returns (uint256) {
        return (super.balanceOf(account) * rebaseFactor) / 10000;
    }
    
    /**
     * @dev Override totalSupply to apply rebase factor
     */
    function totalSupply() public view virtual override returns (uint256) {
        return (super.totalSupply() * rebaseFactor) / 10000;
    }
}
