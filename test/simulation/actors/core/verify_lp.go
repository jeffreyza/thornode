package core

import (
	"fmt"
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type VerifyLPActor struct {
	Actor

	asset common.Asset
}

func NewVerifyLPActor(asset common.Asset, rng *rand.Rand) *Actor {
	a := &VerifyLPActor{
		Actor: *NewActor(fmt.Sprintf("VerifyLP-%s", asset), rng),
		asset: asset,
	}

	a.Ops = append(a.Ops, a.verifyLP)

	return &a.Actor
}

func (a *VerifyLPActor) verifyLP(config *OpConfig) OpResult {
	lps, err := thornode.GetLiquidityProviders(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get liquidity providers")
		return OpResult{Continue: false}
	}

	for _, lp := range lps {
		if lp.PendingAsset != "0" || lp.PendingRune != "0" || lp.Units == "" || lp.Units == "0" {
			continue
		}

		a.Log().Info().Str("units", lp.Units).Msg("found completed liquidity provider position")
		return OpResult{Finish: true}
	}

	return OpResult{Continue: false}
}
