package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// BondYieldMemo represents BONDYIELD reward payout configuration memos.
type BondYieldMemo struct {
	MemoBase
	NodeAddress  cosmos.AccAddress
	Bps          uint64
	PayoutTarget string
}

// NewBondYieldMemo builds a Bond Yield reward payout configuration memo.
func NewBondYieldMemo(
	nodeAddress cosmos.AccAddress,
	bps uint64,
	payoutTarget string,
) BondYieldMemo {
	return BondYieldMemo{
		MemoBase:     MemoBase{TxType: TxBondYield},
		NodeAddress:  nodeAddress,
		Bps:          bps,
		PayoutTarget: payoutTarget,
	}
}

// GetAccAddress returns the node account whose provider is configuring bond yield.
func (m BondYieldMemo) GetAccAddress() cosmos.AccAddress { return m.NodeAddress }

// GetBps returns the configured payout basis points.
func (m BondYieldMemo) GetBps() uint64 { return m.Bps }

// ParseBondYieldMemo parses BONDYIELD memos for reward payout configuration.
func (p *parser) ParseBondYieldMemo() (BondYieldMemo, error) {
	nodeAddress := p.getAccAddress(1, true, nil)
	bps := p.getUintWithMaxValue(2, true, 0, constants.MaxBasisPts).Uint64()
	payoutTarget := p.getString(3, false, "")
	return NewBondYieldMemo(nodeAddress, bps, payoutTarget), p.Error()
}
