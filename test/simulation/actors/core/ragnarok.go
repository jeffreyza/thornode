package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// RagnarokPoolActor
////////////////////////////////////////////////////////////////////////////////////////

type RagnarokPoolActor struct {
	Actor

	asset common.Asset
}

func NewRagnarokPoolActor(asset common.Asset, rng *rand.Rand) *Actor {
	a := &RagnarokPoolActor{
		Actor: *NewActor(fmt.Sprintf("Ragnarok-%s", asset), rng),
		asset: asset,
	}
	a.Timeout = 5 * time.Minute
	// Tighten the poll cadence between ragnarok ops (sendMimir -> verifyPoolRemoval)
	// to cut overshoot waiting for on-chain pool removal. Same ops, same checks.
	a.Interval = 500 * time.Millisecond

	// TODO: get all LPs for the asset and store in state

	// send ragnarok mimir from admin
	a.Ops = append(a.Ops, a.sendMimir)

	// TODO: verify l1 balances
	// TODO: verify rune balances

	// verify pool removal
	a.Ops = append(a.Ops, a.verifyPoolRemoval)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *RagnarokPoolActor) sendMimir(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{
			Continue: false,
		}
	}
	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}

	// send from all active nodes
	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get thor address")
			return OpResult{
				Continue: false,
			}
		}

		if !activeNodes[accAddr.String()] {
			a.Log().Info().Str("node", accAddr.String()).Msg("skipping inactive node mimir")
			continue
		}

		mimir := types.NewMsgMimir(fmt.Sprintf("RAGNAROK-%s", a.asset.MimirString()), 1, accAddr)
		txid, err := node.Thorchain.Broadcast(mimir)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to broadcast mimir")
			return OpResult{
				Continue: false,
			}
		}
		a.Log().Info().Str("txid", txid.String()).Msg("broadcasted mimir")
	}
	return OpResult{
		Continue: true,
	}
}

func (a *RagnarokPoolActor) verifyPoolRemoval(config *OpConfig) OpResult {
	// fetch pools
	pools, err := thornode.GetPools()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pools")
		return OpResult{
			Continue: false,
		}
	}

	// verify pool removal
	found := false
	for _, pool := range pools {
		if pool.Asset == a.asset.String() {
			found = true
			break
		}
	}

	if found {
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().Msg("pool removed")
	return OpResult{
		Finish: true,
	}
}
