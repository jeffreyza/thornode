package types

import (
	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgScheduleExecuteContract{}

// NewMsgScheduleExecuteContract creates a msg to create a new denom
func NewMsgScheduleExecuteContract(sender string, after uint64, msg []byte) *MsgScheduleExecuteContract {
	return &MsgScheduleExecuteContract{
		Sender: sender,
		After:  after,
		Msg:    msg,
	}
}

// ValidateStored validates the fields that must hold for a message regardless
// of when it was scheduled. It is the check applied to messages already
// persisted in the store (see GenesisState.Validate): execution is driven by
// Schedule.Height and After is only an input to AddMsg, so a message that was
// legally scheduled before the MaxAfter horizon existed must not be rejected
// retroactively on an export -> validate -> import round trip.
func (m MsgScheduleExecuteContract) ValidateStored() error {
	_, err := sdk.AccAddressFromBech32(m.Sender)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "Invalid sender address (%s)", err)
	}

	return nil
}

// ValidateBasic validates a new scheduling request: everything ValidateStored
// checks plus the MaxAfter horizon bound, which only applies at scheduling time.
func (m MsgScheduleExecuteContract) ValidateBasic() error {
	if err := m.ValidateStored(); err != nil {
		return err
	}

	if m.After > MaxAfter {
		return errors.Wrapf(ErrAfterTooLarge, "after %d exceeds max %d", m.After, MaxAfter)
	}

	return nil
}

func (m MsgScheduleExecuteContract) GetSigners() []sdk.AccAddress {
	sender, _ := sdk.AccAddressFromBech32(m.Sender)
	return []sdk.AccAddress{sender}
}
