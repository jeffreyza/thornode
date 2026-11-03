package thorchain

type TCYStakeMemo struct {
	MemoBase
}

func NewTCYStakeMemo() TCYStakeMemo {
	return TCYStakeMemo{
		MemoBase: MemoBase{TxType: TxTCYStake},
	}
}

func (p *parser) ParseTCYStakeMemo() (TCYStakeMemo, error) {
	return NewTCYStakeMemo(), p.Error()
}
