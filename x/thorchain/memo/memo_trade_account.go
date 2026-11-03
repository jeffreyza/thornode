package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type TradeAccountDepositMemo struct {
	MemoBase
	Address cosmos.AccAddress
}

func (m TradeAccountDepositMemo) GetAccAddress() cosmos.AccAddress { return m.Address }

func NewTradeAccountDepositMemo(addr cosmos.AccAddress) TradeAccountDepositMemo {
	return TradeAccountDepositMemo{
		MemoBase: MemoBase{TxType: TxTradeAccountDeposit},
		Address:  addr,
	}
}

func (p *parser) ParseTradeAccountDeposit() (TradeAccountDepositMemo, error) {
	addr := p.getAccAddress(1, true, nil)
	return NewTradeAccountDepositMemo(addr), p.Error()
}

type TradeAccountWithdrawalMemo struct {
	MemoBase
	Address common.Address
	Amount  cosmos.Uint
}

func (m TradeAccountWithdrawalMemo) GetAddress() common.Address { return m.Address }
func (m TradeAccountWithdrawalMemo) GetAmount() cosmos.Uint     { return m.Amount }

func NewTradeAccountWithdrawalMemo(addr common.Address) TradeAccountWithdrawalMemo {
	return TradeAccountWithdrawalMemo{
		MemoBase: MemoBase{TxType: TxTradeAccountWithdrawal},
		Address:  addr,
	}
}

func (p *parser) ParseTradeAccountWithdrawal() (TradeAccountWithdrawalMemo, error) {
	addr := p.getAddress(1, true, common.NoAddress)
	return NewTradeAccountWithdrawalMemo(addr), p.Error()
}
