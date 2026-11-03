package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type ModifyLimitSwapMemo struct {
	MemoBase
	Source               common.Coin
	Target               common.Coin
	ModifiedTargetAmount cosmos.Uint
}

func (m ModifyLimitSwapMemo) GetSource() common.Coin               { return m.Source }
func (m ModifyLimitSwapMemo) GetTarget() common.Coin               { return m.Target }
func (m ModifyLimitSwapMemo) GetModifiedTargetAmount() cosmos.Uint { return m.ModifiedTargetAmount }

func NewModifyLimitSwapMemo(source, target common.Coin, mod cosmos.Uint) ModifyLimitSwapMemo {
	return ModifyLimitSwapMemo{
		MemoBase:             MemoBase{TxType: TxModifyLimitSwap},
		Source:               source,
		Target:               target,
		ModifiedTargetAmount: mod,
	}
}

func (p *parser) ParseModifyLimitSwap() (ModifyLimitSwapMemo, error) {
	source := p.getCoin(1, true, common.NoCoin)
	target := p.getCoin(2, true, common.NoCoin)
	mod := p.getUint(3, true, 0)
	return NewModifyLimitSwapMemo(source, target, mod), p.Error()
}
