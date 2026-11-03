package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

////////////////////////////////////////////////////////////////////////////////////////
// MimirActor
////////////////////////////////////////////////////////////////////////////////////////

// MimirActor assumes that the mocknet was started with multiple nodes.
type MimirActor struct {
	Actor
	key   string
	value int64
}

func NewMimirActor(key string, value int64, rng *rand.Rand) *Actor {
	a := &MimirActor{
		Actor: *NewActor(fmt.Sprintf("Mimir %s=%d", key, value), rng),
		key:   key,
		value: value,
	}
	a.Timeout = time.Minute

	a.Ops = append(a.Ops, a.sendMimir)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *MimirActor) sendMimir(config *OpConfig) OpResult {
	node := config.NodeUsers[0]
	if !node.Acquire() {
		a.Log().Error().Msg("failed to acquire node lock")
		return OpResult{
			Continue: false,
		}
	}
	defer node.Release()
	accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{
			Continue: false,
		}
	}

	mimir := thorchain.NewMsgMimir(a.key, a.value, accAddr)
	txid, err := node.Thorchain.Broadcast(mimir)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast mimir")
		return OpResult{
			Continue: false,
		}
	}
	a.Log().Info().Str("txid", txid.String()).Msg("broadcasted mimir")

	return OpResult{
		Continue: true,
	}
}
