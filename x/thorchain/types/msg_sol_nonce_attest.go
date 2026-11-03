package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgSolNonceAttest{}
	_ sdk.HasValidateBasic = &MsgSolNonceAttest{}
	_ sdk.LegacyMsg        = &MsgSolNonceAttest{}
)

// NewMsgSolNonceAttest creates a new MsgSolNonceAttest.
func NewMsgSolNonceAttest(vaultPubKey common.PubKey, nonceAccount, nonce string, slot int64, lastTx common.TxID, signer cosmos.AccAddress) *MsgSolNonceAttest {
	return &MsgSolNonceAttest{
		VaultPubKey:  vaultPubKey,
		NonceAccount: nonceAccount,
		Nonce:        nonce,
		Slot:         slot,
		LastTx:       lastTx,
		Signer:       signer,
	}
}

// ValidateBasic implements HasValidateBasic
func (m *MsgSolNonceAttest) ValidateBasic() error {
	if m.VaultPubKey.IsEmpty() {
		return cosmos.ErrUnknownRequest("vault pubkey is empty")
	}
	if _, err := common.NewTxID(m.LastTx.String()); err != nil {
		return cosmos.ErrUnknownRequest("invalid last tx: " + err.Error())
	}
	if err := common.ValidateSolNonceUpdate(common.SOLChain, &common.SolNonceUpdate{
		NonceAccount: m.NonceAccount, Nonce: m.Nonce, Slot: m.Slot,
	}); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.Signer.Empty() {
		return cosmos.ErrUnauthorized("invalid sender")
	}
	return nil
}

// GetSigners Implements Msg.
func (m *MsgSolNonceAttest) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgSolNonceAttestCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgSolNonceAttest)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgSolNonceAttest: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}
