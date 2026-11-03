// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

import "@openzeppelin/contracts/access/Ownable.sol";

/**
 * @title TetherToken
 * @dev A mock implementation of USDT (Tether) with fee-on-transfer functionality and no transfer return values
 * This implementation does not follow the exact IERC20 interface to better mimic real USDT behavior
 */
contract TetherToken is Ownable {
    string private _name;
    string private _symbol;
    uint8 private _decimals;
    uint256 private _totalSupply;
    uint256 public basisPointsRate = 0;
    uint256 public maximumFee = 0;
    
    mapping(address => uint256) private _balances;
    mapping(address => mapping(address => uint256)) private _allowances;
    
    // Events similar to ERC20 but we're not implementing the interface
    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);

    constructor(
        uint256 _initialSupply,
        string memory name_,
        string memory symbol_,
        uint8 decimals_
    ) Ownable(msg.sender) {
        _name = name_;
        _symbol = symbol_;
        _decimals = decimals_;
        _mint(msg.sender, _initialSupply);
    }
    
    function name() public view returns (string memory) {
        return _name;
    }
    
    function symbol() public view returns (string memory) {
        return _symbol;
    }

    function decimals() public view returns (uint8) {
        return _decimals;
    }
    
    function totalSupply() public view returns (uint256) {
        return _totalSupply;
    }
    
    function balanceOf(address account) public view returns (uint256) {
        return _balances[account];
    }
    
    function allowance(address owner, address spender) public view returns (uint256) {
        return _allowances[owner][spender];
    }

    /**
     * @dev Set fee parameters
     * @param newBasisPoints Basis points rate for the fee calculation (100 = 1%)
     * @param newMaxFee Maximum fee amount that can be charged
     */
    function setParams(uint256 newBasisPoints, uint256 newMaxFee) public onlyOwner {
        // Ensure reasonable limits to prevent abuse
        require(newBasisPoints < 20000, "Basis points too high"); // Max 200%
        require(newMaxFee < 50 * (10**_decimals), "Max fee too high"); // Max 50 tokens
        basisPointsRate = newBasisPoints;
        maximumFee = newMaxFee;
    }

    /**
     * @dev Calculate fee for a given amount
     */
    function calcFee(uint256 _amount) private view returns (uint256) {
        uint256 fee = (_amount * basisPointsRate) / 10000;
        if (fee > maximumFee) {
            fee = maximumFee;
        }
        return fee;
    }
    
    function approve(address spender, uint256 amount) public returns (bool) {
        _approve(msg.sender, spender, amount);
        return true;
    }
    
    function _approve(address owner, address spender, uint256 amount) internal {
        require(owner != address(0), "ERC20: approve from the zero address");
        require(spender != address(0), "ERC20: approve to the zero address");

        _allowances[owner][spender] = amount;
        emit Approval(owner, spender, amount);
    }
    
    function _mint(address account, uint256 amount) internal {
        require(account != address(0), "ERC20: mint to the zero address");

        _totalSupply += amount;
        _balances[account] += amount;
        emit Transfer(address(0), account, amount);
    }

    /**
     * @dev No return value for transfer to mimic real USDT behavior
     */
    function transfer(address recipient, uint256 amount) public {
        require(recipient != address(0), "ERC20: transfer to the zero address");
        require(_balances[msg.sender] >= amount, "ERC20: transfer amount exceeds balance");
        
        uint256 fee = calcFee(amount);
        uint256 sendAmount = amount - fee;
        
        _balances[msg.sender] -= amount;
        _balances[recipient] += sendAmount;
        emit Transfer(msg.sender, recipient, sendAmount);
        
        if (fee > 0) {
            _balances[owner()] += fee;
            emit Transfer(msg.sender, owner(), fee);
        }
    }

    /**
     * @dev No return value for transferFrom to mimic real USDT behavior
     */
    function transferFrom(address sender, address recipient, uint256 amount) public {
        require(sender != address(0), "ERC20: transfer from the zero address");
        require(recipient != address(0), "ERC20: transfer to the zero address");
        require(_balances[sender] >= amount, "ERC20: transfer amount exceeds balance");
        require(_allowances[sender][msg.sender] >= amount, "ERC20: transfer amount exceeds allowance");
        
        uint256 fee = calcFee(amount);
        uint256 sendAmount = amount - fee;
        
        _balances[sender] -= amount;
        _balances[recipient] += sendAmount;
        _allowances[sender][msg.sender] -= amount;
        emit Transfer(sender, recipient, sendAmount);
        
        if (fee > 0) {
            _balances[owner()] += fee;
            emit Transfer(sender, owner(), fee);
        }
    }
}
