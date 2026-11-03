package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

type TCYClaimMemo struct {
	MemoBase
	Address common.Address
}

func (m TCYClaimMemo) GetAddress() common.Address {
	return m.Address
}

func NewTCYClaimMemo(address common.Address) TCYClaimMemo {
	return TCYClaimMemo{
		MemoBase: MemoBase{TxType: TxTCYClaim},
		Address:  address,
	}
}

func (p *parser) ParseTCYClaimMemo() (TCYClaimMemo, error) {
	address := p.getThorAddress(1, true, common.NoAddress)
	return NewTCYClaimMemo(address), p.Error()
}
