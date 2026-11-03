package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type SecuredAssetDepositMemo struct {
	MemoBase
	Address cosmos.AccAddress
}

func (m SecuredAssetDepositMemo) GetAccAddress() cosmos.AccAddress { return m.Address }

func NewSecuredAssetDepositMemo(addr cosmos.AccAddress) SecuredAssetDepositMemo {
	return SecuredAssetDepositMemo{
		MemoBase: MemoBase{TxType: TxSecuredAssetDeposit},
		Address:  addr,
	}
}

func (p *parser) ParseSecuredAssetDeposit() (SecuredAssetDepositMemo, error) {
	addr := p.getAccAddress(1, true, nil)
	return NewSecuredAssetDepositMemo(addr), p.Error()
}

type SecuredAssetWithdrawMemo struct {
	MemoBase
	Address common.Address
	Amount  cosmos.Uint
}

func (m SecuredAssetWithdrawMemo) GetAddress() common.Address { return m.Address }
func (m SecuredAssetWithdrawMemo) GetAmount() cosmos.Uint     { return m.Amount }

func NewSecuredAssetWithdrawMemo(addr common.Address) SecuredAssetWithdrawMemo {
	return SecuredAssetWithdrawMemo{
		MemoBase: MemoBase{TxType: TxSecuredAssetWithdraw},
		Address:  addr,
	}
}

func (p *parser) ParseSecuredAssetWithdraw() (SecuredAssetWithdrawMemo, error) {
	addr := p.getAddress(1, true, common.NoAddress)
	return NewSecuredAssetWithdrawMemo(addr), p.Error()
}
