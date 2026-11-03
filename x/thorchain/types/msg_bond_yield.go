package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

var (
	_ sdk.Msg              = &MsgBondYield{}
	_ sdk.HasValidateBasic = &MsgBondYield{}
	_ sdk.LegacyMsg        = &MsgBondYield{}
)

// NewMsgBondYield constructs the message used to update auto-claim settings.
func NewMsgBondYield(
	txin common.Tx,
	nodeAddress cosmos.AccAddress,
	bps uint64,
	payoutTarget string,
	signer cosmos.AccAddress,
) *MsgBondYield {
	return &MsgBondYield{
		TxIn:         txin,
		NodeAddress:  nodeAddress,
		Bps:          bps,
		PayoutTarget: payoutTarget,
		Signer:       signer,
	}
}

// ValidateBasic performs stateless validation for bond yield messages.
func (m *MsgBondYield) ValidateBasic() error {
	if m.NodeAddress.Empty() {
		return cosmos.ErrInvalidAddress("node address cannot be empty")
	}
	if m.Bps > constants.MaxBasisPts {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("basis points cannot exceed %d", constants.MaxBasisPts))
	}
	// A zero-bps message clears any stored payout target, so target validation
	// is intentionally limited to messages that can store a target.
	if m.Bps > 0 && m.PayoutTarget != "" {
		if addr, err := common.NewAddress(m.PayoutTarget); err == nil && !addr.GetChain().IsTHORChain() {
			return cosmos.ErrInvalidAddress("payout target address must be a THORChain address")
		}
	}
	if m.TxIn.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("tx id cannot be empty")
	}
	if m.TxIn.FromAddress.IsEmpty() {
		return cosmos.ErrInvalidAddress("tx from address cannot be empty")
	}
	if !m.TxIn.Coins.IsEmpty() {
		return cosmos.ErrUnknownRequest("bond yield message cannot have a non-zero coin amount")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress("empty signer address")
	}
	return nil
}

// GetSigners returns the signer that authorized the bond yield message.
func (m *MsgBondYield) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
