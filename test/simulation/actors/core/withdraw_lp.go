package core

import (
	"fmt"
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	withdrawLPBasisPoints  = 5000
	withdrawRuneDepositAmt = 200000000
)

type WithdrawLPActor struct {
	Actor

	asset common.Asset

	account      *User
	thorAddress  common.Address
	assetAddress common.Address
	unitsBefore  cosmos.Uint
	withdrawTxID string
}

func NewWithdrawLPActor(asset common.Asset, rng *rand.Rand) *Actor {
	a := &WithdrawLPActor{
		Actor: *NewActor(fmt.Sprintf("WithdrawLP-%s", asset), rng),
		asset: asset,
	}

	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.sendWithdraw)
	a.Ops = append(a.Ops, a.verifyWithdraw)

	return &a.Actor
}

func withdrawLPMemo(asset common.Asset) string {
	return fmt.Sprintf("-:%s:%d:%s", asset, withdrawLPBasisPoints, asset)
}

func findCompletedLP(
	liquidityProviders []openapi.LiquidityProvider,
	thorAddress common.Address,
	assetAddress common.Address,
) *openapi.LiquidityProvider {
	lp := findLP(liquidityProviders, thorAddress, assetAddress)
	if lp == nil || lp.PendingAsset != "0" || lp.PendingRune != "0" || lp.Units == "" || lp.Units == "0" {
		return nil
	}
	return lp
}

func (a *WithdrawLPActor) acquireUser(config *OpConfig) OpResult {
	liquidityProviders, err := thornode.GetLiquidityProviders(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get liquidity providers")
		return OpResult{Continue: false}
	}

	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}

		thorAddress, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get THOR address")
			user.Release()
			continue
		}

		assetAddress, err := user.PubKey(a.asset.Chain).GetAddress(a.asset.Chain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get asset address")
			user.Release()
			continue
		}

		lp := findCompletedLP(liquidityProviders, thorAddress, assetAddress)
		if lp == nil {
			user.Release()
			continue
		}

		a.account = user
		a.thorAddress = thorAddress
		a.assetAddress = assetAddress
		a.unitsBefore = cosmos.NewUintFromString(lp.Units)
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
		a.Log().Info().
			Stringer("thor_address", thorAddress).
			Stringer("asset_address", assetAddress).
			Str("units_before", lp.Units).
			Msg("acquired LP holder")
		return OpResult{Continue: true}
	}

	return OpResult{Continue: false}
}

func (a *WithdrawLPActor) sendWithdraw(config *OpConfig) OpResult {
	accAddr, err := a.account.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.account.Release()
		return OpResult{Finish: true, Error: err}
	}

	deposit := types.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(withdrawRuneDepositAmt))),
		withdrawLPMemo(a.asset),
		accAddr,
	)
	txid, err := a.account.Thorchain.Broadcast(deposit)
	if err != nil {
		a.account.Release()
		return OpResult{Finish: true, Error: err}
	}

	a.withdrawTxID = txid.String()
	a.Log().Info().
		Str("txid", a.withdrawTxID).
		Str("memo", withdrawLPMemo(a.asset)).
		Msg("broadcasted LP withdraw")

	return OpResult{Continue: true}
}

func (a *WithdrawLPActor) verifyWithdraw(config *OpConfig) OpResult {
	stages, err := thornode.GetTxStages(a.withdrawTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx stages")
		return OpResult{Continue: false}
	}
	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{Continue: false}
	}

	details, err := thornode.GetTxDetails(a.withdrawTxID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx details")
		return OpResult{Continue: false}
	}

	outboundFound := false
	for _, outTx := range details.OutTxs {
		if outTx.ToAddress != nil && common.Address(*outTx.ToAddress).Equals(a.assetAddress) {
			outboundFound = true
			break
		}
	}
	if !outboundFound {
		a.account.Release()
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("expected XMR outbound to %s", a.assetAddress),
		}
	}

	liquidityProviders, err := thornode.GetLiquidityProviders(a.asset)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to refresh liquidity providers")
		return OpResult{Continue: false}
	}

	lp := findCompletedLP(liquidityProviders, a.thorAddress, a.assetAddress)
	unitsAfter := cosmos.ZeroUint()
	if lp != nil {
		unitsAfter = cosmos.NewUintFromString(lp.Units)
	}
	if !unitsAfter.LT(a.unitsBefore) {
		a.Log().Info().
			Stringer("units_before", a.unitsBefore).
			Stringer("units_after", unitsAfter).
			Msg("waiting for LP units to decrease")
		return OpResult{Continue: false}
	}

	a.Log().Info().
		Stringer("units_before", a.unitsBefore).
		Stringer("units_after", unitsAfter).
		Msg("LP withdraw completed")
	a.account.Release()
	return OpResult{Finish: true}
}
