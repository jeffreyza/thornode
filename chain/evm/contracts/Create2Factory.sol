// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

/**
 * @title Create2Factory
 * @dev Factory contract for deterministic CREATE2 deployments
 */
contract Create2Factory {
    event Deployed(address indexed deployedAddress, bytes32 indexed salt);

    /**
     * @dev Deploys a contract using CREATE2
     * @param salt The salt for deterministic address generation
     * @param bytecode The contract bytecode to deploy
     * @return deployedAddress The address of the deployed contract
     */
    function deploy(bytes32 salt, bytes memory bytecode) external returns (address deployedAddress) {
        assembly {
            deployedAddress := create2(0, add(bytecode, 0x20), mload(bytecode), salt)
            if iszero(deployedAddress) { revert(0, 0) }
        }
        
        emit Deployed(deployedAddress, salt);
    }

    /**
     * @dev Computes the CREATE2 address for given parameters
     * @param salt The salt for deterministic address generation
     * @param bytecodeHash The keccak256 hash of the contract bytecode
     * @return computedAddress The computed CREATE2 address
     */
    function computeAddress(bytes32 salt, bytes32 bytecodeHash) external view returns (address computedAddress) {
        computedAddress = address(
            uint160(
                uint256(
                    keccak256(
                        abi.encodePacked(
                            bytes1(0xff),
                            address(this),
                            salt,
                            bytecodeHash
                        )
                    )
                )
            )
        );
    }

    /**
     * @dev Computes the CREATE2 address for given bytecode
     * @param salt The salt for deterministic address generation  
     * @param bytecode The contract bytecode
     * @return computedAddress The computed CREATE2 address
     */
    function computeAddressFromBytecode(bytes32 salt, bytes memory bytecode) external view returns (address computedAddress) {
        bytes32 bytecodeHash = keccak256(bytecode);
        return this.computeAddress(salt, bytecodeHash);
    }
} 