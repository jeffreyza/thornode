package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

var switchMap = map[string]common.Address{
	"GAIA.KUJI":  common.GaiaZeroAddress,
	"GAIA.RKUJI": common.GaiaZeroAddress,
	"GAIA.FUZN":  common.GaiaZeroAddress,
	"GAIA.LVN":   common.GaiaZeroAddress,
	"GAIA.WINK":  common.GaiaZeroAddress,
	"GAIA.NAMI":  common.GaiaZeroAddress,
	"GAIA.AUTO":  common.GaiaZeroAddress,
	"GAIA.LQDY":  common.GaiaZeroAddress,
	"GAIA.NSTK":  common.GaiaZeroAddress,
	"GAIA.XUSK":  common.GaiaZeroAddress,
}

// SwitchMgr is current implementation of SwitchManager
type SwitchMgr struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newSwitchMgr creates a new instance of SwitchMgr
func newSwitchMgr(keeper keeper.Keeper, eventMgr EventManager) *SwitchMgr {
	return &SwitchMgr{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (s *SwitchMgr) IsSwitch(
	ctx cosmos.Context,
	asset common.Asset,
) bool {
	_, exists := switchMap[asset.String()]
	return exists
}

func (s *SwitchMgr) Switch(
	ctx cosmos.Context,
	asset common.Asset,
	amount cosmos.Uint,
	owner cosmos.AccAddress,
	assetAddr common.Address,
	txID common.TxID,
) (common.Address, error) {
	addr, exists := switchMap[asset.String()]
	if !exists {
		return common.NoAddress, errNotAuthorized
	}

	asset = common.Asset{
		Chain:  common.THORChain,
		Symbol: asset.Symbol,
		Ticker: asset.Ticker,
	}
	coin := common.NewCoin(asset, amount)

	err := s.keeper.MintAndSendToAccount(ctx, owner, coin)
	if err != nil {
		return common.NoAddress, err
	}

	switchEvent := NewEventSwitch(amount, asset, assetAddr, common.Address(owner.String()), txID)
	if err = s.eventMgr.EmitEvent(ctx, switchEvent); err != nil {
		ctx.Logger().Error("fail to emit switch event", "error", err)
	}
	_, err = coin.Native()
	if err != nil {
		return common.NoAddress, err
	}

	return addr, nil
}
