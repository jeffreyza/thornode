package thorchain

import (
	"cosmossdk.io/math"
)

type TCYUnstakeMemo struct {
	MemoBase
	BasisPoints math.Uint
}

func NewTCYUnstakeMemo(basisPoints math.Uint) TCYUnstakeMemo {
	return TCYUnstakeMemo{
		MemoBase:    MemoBase{TxType: TxTCYUnstake},
		BasisPoints: basisPoints,
	}
}

func (p *parser) ParseTCYUnstakeMemo() (TCYUnstakeMemo, error) {
	bps := p.getUint(1, true, 0)
	return NewTCYUnstakeMemo(bps), p.Error()
}
