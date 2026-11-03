package thorchain

import (
	"fmt"

	"cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// SecuredAssetMgr is current implementation of SecuredAssetMgr
type SecuredAssetMgr struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newSecuredAssetMgr create a new instance of SecuredAssetMgr
func newSecuredAssetMgr(keeper keeper.Keeper, eventMgr EventManager) *SecuredAssetMgr {
	return &SecuredAssetMgr{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (s *SecuredAssetMgr) EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error {
	// TODO: implement liquidation
	return nil
}

func (s *SecuredAssetMgr) BalanceOf(
	ctx cosmos.Context,
	asset common.Asset,
	addr cosmos.AccAddress,
) cosmos.Uint {
	asset = asset.GetSecuredAsset()
	pool, err := s.keeper.GetSecuredAsset(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint()
	}

	balance := s.keeper.GetBalanceOf(ctx, addr, asset)
	shareSupply := s.keeper.GetTotalSupply(ctx, asset)

	// Proportion of total Pool Depth that the account's x/bank share tokens entitle it to:
	return common.GetSafeShare(math.Uint(balance.Amount), shareSupply, pool.Depth)
}

func (s *SecuredAssetMgr) GetShareSupply(
	ctx cosmos.Context,
	asset common.Asset,
) math.Uint {
	shareSupply := s.keeper.GetTotalSupply(ctx, asset)
	return shareSupply
}

func (s *SecuredAssetMgr) GetSecuredAssetStatus(
	ctx cosmos.Context,
	asset common.Asset,
) (keeper.SecuredAsset, math.Uint, error) {
	asset = asset.GetSecuredAsset()
	pool, err := s.keeper.GetSecuredAsset(ctx, asset)
	if err != nil {
		return keeper.SecuredAsset{}, math.ZeroUint(), err
	}

	shareSupply := s.GetShareSupply(ctx, asset)
	return pool, shareSupply, nil
}

func (s *SecuredAssetMgr) Deposit(
	ctx cosmos.Context,
	asset common.Asset,
	amount cosmos.Uint,
	owner cosmos.AccAddress,
	assetAddr common.Address,
	txID common.TxID,
) (cosmos.Coin, error) {
	if err := s.CheckHalt(ctx); err != nil {
		return cosmos.Coin{}, err
	}
	if asset.IsNative() {
		return cosmos.Coin{}, fmt.Errorf("native assets cannot be deposited")
	}

	asset = asset.GetSecuredAsset()
	pool, shareSupply, err := s.GetSecuredAssetStatus(ctx, asset)
	if err != nil {
		return cosmos.Coin{}, err
	}

	mintAmt := s.calcMintAmt(shareSupply, pool.Depth, amount)
	coin := common.NewCoin(asset, mintAmt)
	err = s.keeper.MintAndSendToAccount(ctx, owner, coin)
	if err != nil {
		return cosmos.Coin{}, err
	}

	pool.Depth = pool.Depth.Add(amount)

	s.keeper.SetSecuredAsset(ctx, pool)

	depositEvent := NewEventSecuredAssetDeposit(amount, asset, assetAddr, common.Address(owner.String()), txID)
	if err = s.eventMgr.EmitEvent(ctx, depositEvent); err != nil {
		ctx.Logger().Error("fail to emit secured asset deposit event", "error", err)
	}
	var cosmosCoin cosmos.Coin
	cosmosCoin, err = coin.Native()
	if err != nil {
		return cosmos.Coin{}, err
	}

	return cosmosCoin, nil
}

func (s *SecuredAssetMgr) calcMintAmt(oldUnits, depth, add cosmos.Uint) cosmos.Uint {
	if oldUnits.IsZero() || depth.IsZero() {
		return add
	}
	if add.IsZero() {
		return cosmos.ZeroUint()
	}
	return common.GetUncappedShare(add, depth, oldUnits)
}

func (s *SecuredAssetMgr) Withdraw(
	ctx cosmos.Context,
	asset common.Asset,
	amount cosmos.Uint,
	owner cosmos.AccAddress,
	assetAddr common.Address,
	txID common.TxID,
) (common.Coin, error) {
	if err := s.CheckHalt(ctx); err != nil {
		return common.NoCoin, err
	}

	if !asset.IsSecuredAsset() {
		return common.NoCoin, fmt.Errorf("only secured assets can be withdrawn")
	}

	pool, shareSupply, err := s.GetSecuredAssetStatus(ctx, asset)
	if err != nil {
		return common.NoCoin, err
	}

	balance := s.keeper.GetBalanceOf(ctx, owner, asset)
	shareBalance := math.Uint(balance.Amount)

	// Total balance (ownership) of underlying asset pool
	assetAvailable := common.GetSafeShare(shareBalance, shareSupply, pool.Depth)

	// Calculate share tokens to redeem (burn) as a percentage of the total account balance
	burnAmt := common.GetSafeShare(amount, assetAvailable, shareBalance)

	if burnAmt.IsZero() {
		return common.NoCoin, fmt.Errorf("insufficient secured asset balance for %s: requested %s, available %s", asset, amount, assetAvailable)
	}

	coin := common.NewCoin(asset, burnAmt)
	coins := common.NewCoins(coin)

	err = s.keeper.SendFromAccountToModule(ctx, owner, ModuleName, coins)
	if err != nil {
		return common.NoCoin, err
	}

	// Safely re-calculate withdraw amount from burnAmt
	tokensToClaim := common.GetSafeShare(burnAmt, shareBalance, assetAvailable)

	err = s.keeper.BurnFromModule(ctx, ModuleName, coin)
	if err != nil {
		return common.NoCoin, err
	}
	pool.Depth = common.SafeSub(pool.Depth, tokensToClaim)

	s.keeper.SetSecuredAsset(ctx, pool)

	withdrawEvent := NewEventSecuredAssetWithdraw(tokensToClaim, asset, assetAddr, common.Address(owner.String()), txID)
	if err := s.eventMgr.EmitEvent(ctx, withdrawEvent); err != nil {
		ctx.Logger().Error("fail to emit secured asset withdraw event", "error", err)
	}

	return common.NewCoin(asset.GetLayer1Asset(), tokensToClaim), nil
}

func (h SecuredAssetMgr) CheckHalt(ctx cosmos.Context) error {
	m, err := h.keeper.GetMimir(ctx, constants.MimirKeySecuredAssetHaltGlobal)
	if err != nil {
		return err
	}
	if m > 0 && m <= ctx.BlockHeight() {
		return fmt.Errorf("secured assets are disabled")
	}
	return nil
}
