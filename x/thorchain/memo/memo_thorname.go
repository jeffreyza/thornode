package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type ManageTHORNameMemo struct {
	MemoBase
	Name                                string
	Chain                               common.Chain
	Address                             common.Address
	PreferredAsset                      common.Asset
	Expire                              int64
	Owner                               cosmos.AccAddress
	PreferredAssetOutboundFeeMultiplier int64
}

func (m ManageTHORNameMemo) GetName() string            { return m.Name }
func (m ManageTHORNameMemo) GetChain() common.Chain     { return m.Chain }
func (m ManageTHORNameMemo) GetAddress() common.Address { return m.Address }
func (m ManageTHORNameMemo) GetBlockExpire() int64      { return m.Expire }

func NewManageTHORNameMemo(name string, chain common.Chain, addr common.Address, expire int64, asset common.Asset, owner cosmos.AccAddress, multiplier int64) ManageTHORNameMemo {
	return ManageTHORNameMemo{
		MemoBase:                            MemoBase{TxType: TxTHORName},
		Name:                                name,
		Chain:                               chain,
		Address:                             addr,
		PreferredAsset:                      asset,
		Expire:                              expire,
		Owner:                               owner,
		PreferredAssetOutboundFeeMultiplier: multiplier,
	}
}

func (p *parser) ParseManageTHORNameMemo() (ManageTHORNameMemo, error) {
	chain := p.getChain(2, true, common.EmptyChain)
	addr := p.getAddress(3, true, common.NoAddress)
	owner := p.getAccAddress(4, false, nil)
	preferredAsset := p.getAsset(5, false, common.EmptyAsset)
	expire := p.getInt64(6, false, 0)
	multiplier := p.getInt64(7, false, -1)
	return NewManageTHORNameMemo(p.get(1), chain, addr, expire, preferredAsset, owner, multiplier), p.Error()
}
