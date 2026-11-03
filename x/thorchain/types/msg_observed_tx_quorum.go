package types

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgObservedTxQuorum{}
	_ sdk.HasValidateBasic = &MsgObservedTxQuorum{}
	_ sdk.LegacyMsg        = &MsgObservedTxQuorum{}
)

// NewMsgObservedTxIn is a constructor function for MsgObservedTxQuorum
func NewMsgObservedTxQuorum(tx *common.QuorumTx, signer cosmos.AccAddress) *MsgObservedTxQuorum {
	return &MsgObservedTxQuorum{
		QuoTx:  tx,
		Signer: signer,
	}
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented.
// No versioning is used there.
func (m *MsgObservedTxQuorum) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.QuoTx == nil {
		return cosmos.ErrUnknownRequest("QuoTx cannot be nil")
	}

	tx := m.QuoTx.ObsTx

	if err := tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	obAddr, err := tx.ObservedPubKey.GetAddress(tx.Tx.Coins[0].Asset.GetChain())
	if err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.QuoTx.Inbound {
		// Match legacy inbound admission. Zero remains available to outbound
		// pre-mining observations, but cannot establish an inbound quorum.
		if tx.Tx.Chain.UsesHeightScopedErrata() {
			if _, err := tx.CanonicalMinedHeight(); err != nil {
				return cosmos.ErrUnknownRequest(err.Error())
			}
		}
		if len(tx.SpentOutputRefs) > 0 {
			return cosmos.ErrUnknownRequest("spent output refs are only valid for outbound observed transactions")
		}
		if strings.TrimSpace(tx.XmrTxKey) != "" {
			return cosmos.ErrUnknownRequest("xmr tx key is only valid for outbound observed transactions")
		}
		if !common.AddressEquals(tx.Tx.Chain, tx.Tx.ToAddress, obAddr) {
			return cosmos.ErrUnknownRequest("request is not an inbound observed transaction")
		}
	} else {
		if !common.AddressEquals(tx.Tx.Chain, tx.Tx.FromAddress, obAddr) {
			return cosmos.ErrUnknownRequest("request is not an outbound observed transaction")
		}
		if tx.Tx.Chain.Equals(common.XMRChain) && strings.TrimSpace(tx.XmrTxKey) == "" {
			return cosmos.ErrUnknownRequest("xmr outbound observations must include xmr tx key")
		}
	}
	if len(tx.Signers) > 0 {
		return cosmos.ErrUnknownRequest("signers must be empty")
	}
	if len(tx.OutHashes) > 0 {
		return cosmos.ErrUnknownRequest("out hashes must be empty")
	}
	if tx.Status != common.Status_incomplete {
		return cosmos.ErrUnknownRequest("status must be incomplete")
	}

	return nil
}

// GetSigners defines whose signature is required
func (m *MsgObservedTxQuorum) GetSigners() []cosmos.AccAddress {
	return quorumSignersCommon(m.QuoTx.Attestations)
}

func MsgObservedTxQuorumCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgObservedTxQuorum)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgObservedTxQuorum: %T", m)
	}

	return quorumSignersApiCommon(msg.QuoTx.Attestations), nil
}
