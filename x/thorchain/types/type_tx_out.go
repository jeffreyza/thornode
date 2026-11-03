package types

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
)

// ExtendedOutboundSigningPeriodMultiplier is the shared lifetime multiplier
// for XMR and multi-step EVM outbounds.
const ExtendedOutboundSigningPeriodMultiplier int64 = 3

// MaxTxOutSigningTransactionPeriod returns the longest period for which an
// unobserved TxOutItem can remain live. Pending-balance callers use this as
// their scan bound, then apply SigningTransactionPeriod to each item.
func MaxTxOutSigningTransactionPeriod(signingPeriod int64) int64 {
	return signingPeriod * ExtendedOutboundSigningPeriodMultiplier
}

// MaxTxOutSigningTransactionPeriodForChain returns the longest live outbound
// period a chain can contain. XMR outbounds and EVM token/aggregator outbounds
// use the extended period; all other chains only need the standard window.
func MaxTxOutSigningTransactionPeriodForChain(chain common.Chain, signingPeriod int64) int64 {
	if chain.Equals(common.XMRChain) || chain.IsEVM() {
		return MaxTxOutSigningTransactionPeriod(signingPeriod)
	}
	return signingPeriod
}

// SigningTransactionPeriod returns the same per-item lifetime used by
// LackSigning. XMR and EVM token/aggregator outbounds need additional time for
// their sign, broadcast, and observation paths; all balance reservation paths
// must retain them for that entire interval.
func (m TxOutItem) SigningTransactionPeriod(signingPeriod int64) int64 {
	if m.Chain.Equals(common.XMRChain) || m.isEVMAggregator() || m.isEVMToken() {
		return MaxTxOutSigningTransactionPeriod(signingPeriod)
	}
	return signingPeriod
}

func (m TxOutItem) isEVMAggregator() bool {
	return m.Chain.IsEVM() && (m.Aggregator != "" ||
		m.AggregatorTargetAsset != "" ||
		(m.AggregatorTargetLimit != nil && !m.AggregatorTargetLimit.IsZero()))
}

func (m TxOutItem) isEVMToken() bool {
	return m.Chain.IsEVM() && !m.isEVMAggregator() &&
		!m.Coin.Asset.Equals(m.Chain.GetGasAsset())
}

// Valid check whether TxOutItem hold valid information
func (m TxOutItem) Valid() error {
	if m.Chain.IsEmpty() {
		return errors.New("chain cannot be empty")
	}
	if m.InHash.IsEmpty() {
		return errors.New("In Hash cannot be empty")
	}
	if m.ToAddress.IsEmpty() {
		return errors.New("To address cannot be empty")
	}
	if m.VaultPubKey.IsEmpty() {
		return errors.New("vault pubkey cannot be empty")
	}
	if m.GasRate == 0 {
		return errors.New("gas rate is zero")
	}
	if m.Chain.GetGasAsset().IsEmpty() {
		return errors.New("invalid base asset")
	}
	if err := m.Coin.Valid(); err != nil {
		return err
	}
	if err := m.MaxGas.Valid(); err != nil {
		return err
	}

	return nil
}

// Equals compare two tx out item
func (m TxOutItem) Equals(toi2 TxOutItem) bool {
	if !m.Chain.Equals(toi2.Chain) {
		return false
	}
	if !m.ToAddress.Equals(toi2.ToAddress) {
		return false
	}
	if !m.VaultPubKey.Equals(toi2.VaultPubKey) {
		return false
	}
	if !m.Coin.Equals(toi2.Coin) {
		return false
	}
	if !m.InHash.Equals(toi2.InHash) {
		return false
	}
	if m.Memo != toi2.Memo {
		return false
	}
	if m.GasRate != toi2.GasRate {
		return false
	}
	return true
}

// String implement stringer interface
func (m TxOutItem) String() string {
	sb := strings.Builder{}
	sb.WriteString("To Address:" + m.ToAddress.String())
	sb.WriteString("Asset:" + m.Coin.Asset.String())
	sb.WriteString("Amount:" + m.Coin.Amount.String())
	sb.WriteString("Memo:" + m.Memo)
	sb.WriteString("GasRate:" + strconv.FormatInt(m.GasRate, 10))
	return sb.String()
}

func (toi TxOutItem) GetModuleName() string {
	// toi.ModuleName is frequently "", assumed to be AsgardName by default.
	if toi.ModuleName == "" {
		return AsgardName
	}
	return toi.ModuleName
}

func (toi TxOutItem) GetMemo() string {
	if toi.OriginalMemo != "" {
		return toi.OriginalMemo
	}
	return toi.Memo
}

// Hash returns a sha256 hash that uniquely represents the TxOutItem.
// This matches bifrost/thorclient/types/tx_out.go TxOutItem.Hash() to ensure
// THORNode and Bifrost use the same deterministic ordering when processing outbounds.
func (toi TxOutItem) Hash() string {
	// Bifrost uses Coins (slice) which formats as "<amount> <asset>" for single coin.
	// THORNode uses Coin (singular) which has the same String() format.
	str := fmt.Sprintf("%s|%s|%s|%s|%s|%s", toi.Chain, toi.ToAddress, toi.VaultPubKey, toi.Coin, toi.Memo, toi.InHash)
	return fmt.Sprintf("%X", sha256.Sum256([]byte(str)))
}

// NewTxOut creates a new TxOut.
func NewTxOut(height int64) *TxOut {
	return &TxOut{
		Height:  height,
		TxArray: make([]TxOutItem, 0),
	}
}

// IsEmpty to determinate whether there are txitm in this TxOut
func (m *TxOut) IsEmpty() bool {
	return len(m.TxArray) == 0
}

// Valid check every item in it's internal txarray, return an error if it is not valid
func (m *TxOut) Valid() error {
	for _, tx := range m.TxArray {
		if err := tx.Valid(); err != nil {
			return err
		}
	}
	return nil
}
