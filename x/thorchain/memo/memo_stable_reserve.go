package thorchain

import (
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type StableReserveDepositMemo struct {
	MemoBase
}

func NewStableReserveDepositMemo() StableReserveDepositMemo {
	return StableReserveDepositMemo{
		MemoBase: MemoBase{TxType: TxStableReserveDeposit},
	}
}

func (p *parser) ParseStableReserveDeposit() (StableReserveDepositMemo, error) {
	return NewStableReserveDepositMemo(), p.Error()
}

type StableReserveWithdrawMemo struct {
	MemoBase
	Amount cosmos.Uint
}

func (m StableReserveWithdrawMemo) GetAmount() cosmos.Uint { return m.Amount }

func NewStableReserveWithdrawMemo(amount cosmos.Uint) StableReserveWithdrawMemo {
	return StableReserveWithdrawMemo{
		MemoBase: MemoBase{TxType: TxStableReserveWithdraw},
		Amount:   amount,
	}
}

func (p *parser) ParseStableReserveWithdraw() (StableReserveWithdrawMemo, error) {
	amount := p.getUint(1, true, 0)
	return NewStableReserveWithdrawMemo(amount), p.Error()
}
