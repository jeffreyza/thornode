package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// TradeMgr is current implementation of slasher
type TradeMgr struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newTradeMgr create a new instance of Slasher
func newTradeMgr(keeper keeper.Keeper, eventMgr EventManager) *TradeMgr {
	return &TradeMgr{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (s *TradeMgr) EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error {
	// TODO: implement liquidation
	return nil
}

func (s *TradeMgr) BalanceOf(ctx cosmos.Context, asset common.Asset, addr cosmos.AccAddress) cosmos.Uint {
	asset = asset.GetTradeAsset()
	tu, err := s.keeper.GetTradeUnit(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint()
	}

	tr, err := s.keeper.GetTradeAccount(ctx, addr, asset)
	if err != nil {
		return cosmos.ZeroUint()
	}

	// Proportion of total Depth that the account's Units entitle it to:
	return common.GetSafeShare(tr.Units, tu.Units, tu.Depth)
}

func (s *TradeMgr) Deposit(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, txID common.TxID) (cosmos.Uint, error) {
	asset = asset.GetTradeAsset()
	tu, err := s.keeper.GetTradeUnit(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	tr, err := s.keeper.GetTradeAccount(ctx, owner, asset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	tr.LastAddHeight = ctx.BlockHeight()

	units := s.calcDepositUnits(tu.Units, tu.Depth, amount)
	tu.Units = tu.Units.Add(units)
	tr.Units = tr.Units.Add(units)
	tu.Depth = tu.Depth.Add(amount)

	s.keeper.SetTradeUnit(ctx, tu)
	s.keeper.SetTradeAccount(ctx, tr)

	depositEvent := NewEventTradeAccountDeposit(amount, asset, assetAddr, common.Address(owner.String()), txID)
	if err := s.eventMgr.EmitEvent(ctx, depositEvent); err != nil {
		ctx.Logger().Error("fail to emit trade account deposit event", "error", err)
	}

	return amount, nil
}

func (s *TradeMgr) calcDepositUnits(oldUnits, depth, add cosmos.Uint) cosmos.Uint {
	if oldUnits.IsZero() || depth.IsZero() {
		return add
	}
	if add.IsZero() {
		return cosmos.ZeroUint()
	}
	return common.GetUncappedShare(add, depth, oldUnits)
}

func (s *TradeMgr) Withdrawal(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, txID common.TxID) (cosmos.Uint, error) {
	asset = asset.GetTradeAsset()
	tu, err := s.keeper.GetTradeUnit(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	tr, err := s.keeper.GetTradeAccount(ctx, owner, asset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	tr.LastWithdrawHeight = ctx.BlockHeight()

	// assetAvailable is the same as BalanceOf:
	// Proportion of total Depth that the account's Units entitle it to:
	assetAvailable := common.GetSafeShare(tr.Units, tu.Units, tu.Depth)

	// unitsToClaim is the account's units for the specified amount to be withdrawn from assetAvailable,
	// capped at the accounts total Units.
	unitsToClaim := common.GetSafeShare(amount, assetAvailable, tr.Units)

	// tokensToClaim is the exact amount to be withdrawn from those unitsToClaim,
	// capped at the account's assetAvailable.
	tokensToClaim := common.GetSafeShare(unitsToClaim, tr.Units, assetAvailable)

	// GetSafeShare (GetUncappedShare) rounds half-up, so when an account holds few
	// units relative to its token depth the intermediate unitsToClaim can round up,
	// making tokensToClaim exceed the requested amount. Cap tokensToClaim at the
	// requested amount to enforce the user's requested maximum withdrawal and never
	// return more tokens than were asked for.
	if tokensToClaim.GT(amount) {
		tokensToClaim = amount
	}

	if tokensToClaim.IsZero() {
		return cosmos.ZeroUint(), fmt.Errorf("insufficient trade asset balance for %s: requested %s, available %s", asset, amount, assetAvailable)
	}

	tu.Units = common.SafeSub(tu.Units, unitsToClaim)
	tr.Units = common.SafeSub(tr.Units, unitsToClaim)
	tu.Depth = common.SafeSub(tu.Depth, tokensToClaim)

	s.keeper.SetTradeUnit(ctx, tu)
	s.keeper.SetTradeAccount(ctx, tr)

	withdrawEvent := NewEventTradeAccountWithdraw(tokensToClaim, asset, assetAddr, common.Address(owner.String()), txID)
	if err := s.eventMgr.EmitEvent(ctx, withdrawEvent); err != nil {
		ctx.Logger().Error("fail to emit trade account withdraw event", "error", err)
	}

	return tokensToClaim, nil
}
