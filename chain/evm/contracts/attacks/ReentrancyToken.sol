// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/**
 * @title ReentrancyToken
 * @dev ERC20-like token that attempts to trigger router reentrancy during transfers
 */
contract ReentrancyToken is ERC20 {
    address public router;
    address public target;
    bool public attackMode;

    constructor(string memory name_, string memory symbol_) ERC20(name_, symbol_) {
        _mint(msg.sender, 1_000_000 ether);
    }

    function setRouterAndTarget(address _router, address _target) external {
        router = _router;
        target = _target;
    }

    function setAttackMode(bool enabled) external {
        attackMode = enabled;
    }

    function _update(address from, address to, uint256 amount) internal override {
        super._update(from, to, amount);
        if (attackMode && to == router && target != address(0) && amount > 0) {
            // Attempt to call back into router during token transfer flow
            (bool success, ) = router.call(
                abi.encodeWithSignature(
                    "depositWithExpiry(address,address,uint256,string,uint256)",
                    target,
                    address(this),
                    amount,
                    "REENTRANCY",
                    type(uint256).max
                )
            );
            // Use success to avoid compiler warning
            success;
        }
    }
}
