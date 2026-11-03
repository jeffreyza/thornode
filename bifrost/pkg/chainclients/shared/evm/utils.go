package evm

import (
	"bytes"
	"encoding/hex"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
)

// IsSmartContractCall determines if the transaction is a smart contract call and thus
// should be parsed using the SmartContractLogParser - these txs may have a DepositEvent
// from the THORChain Router. This is determined by checking if the tx data is at least 4
// bytes and there is at least one log in the receipt. It is possible for a smart
// contract call to have no logs or no data, but these cannot be THORChain deposits, so
// they can be parsed as a normal tx. On the other hand, simple ETH/ERC20 transfer CAN
// have logs & data, but again these will not be THORChain deposits or outbounds.
func IsSmartContractCall(tx *etypes.Transaction, receipt *etypes.Receipt) bool {
	data := tx.Data()
	if len(data) < 4 {
		return false
	}
	if len(receipt.Logs) == 0 {
		return false
	}
	return true
}

// HasERC20TransferSelector determines whether the transaction data starts with
// the ERC20 transfer(address,uint256) method selector.
func HasERC20TransferSelector(tx *etypes.Transaction) bool {
	if tx == nil {
		return false
	}
	data := tx.Data()
	return len(data) >= len(erc20TransferSelector) && bytes.Equal(data[:len(erc20TransferSelector)], erc20TransferSelector)
}

// IsERC20Transfer determines if the transaction is a top-level ERC20
// transfer(address,uint256) call with a matching Transfer log from tx.To().
func IsERC20Transfer(tx *etypes.Transaction, receipt *etypes.Receipt) bool {
	if !HasERC20TransferSelector(tx) || tx.To() == nil || receipt == nil || receipt.Status != 1 {
		return false
	}
	if len(receipt.Logs) == 0 {
		return false
	}

	tokenAddress := *tx.To()
	for _, l := range receipt.Logs {
		if l == nil || l.Address != tokenAddress {
			continue
		}
		if len(l.Topics) == 3 && l.Topics[0].String() == erc20TransferEvent && len(l.Data) == ecommon.HashLength {
			return true
		}
	}
	return false
}

// DepositEventRouter returns the address of the contract that emitted the Deposit event
// in the given logs. This is used to validate the router that produced the deposit,
// rather than tx.To() which may be an aggregator contract that forwarded the call.
func DepositEventRouter(logs []*etypes.Log) *ecommon.Address {
	for _, l := range logs {
		if len(l.Topics) > 0 && l.Topics[0].String() == depositEvent {
			addr := l.Address
			return &addr
		}
	}
	return nil
}

// LowerHexAddress returns strings.ToLower(addr.Hex()), the 0x-prefixed
// lowercase hex address, without computing the EIP-55 checksum that Hex and
// String derive with a keccak256 on every call.
func LowerHexAddress(addr ecommon.Address) string {
	var buf [2 + 2*ecommon.AddressLength]byte
	buf[0], buf[1] = '0', 'x'
	hex.Encode(buf[2:], addr[:])
	return string(buf[:])
}
