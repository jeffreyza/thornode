package suites

import (
	"fmt"
	rand "math/rand/v2"
	"strings"

	"gitlab.com/thorchain/thornode/v3/constants"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Ragnarok
////////////////////////////////////////////////////////////////////////////////////////

// ragnarokFundMigrationInterval is the FundMigrationInterval the stage votes
// before ragnarok. NetworkMgr.checkPoolRagnarok only processes pools on
// FundMigrationInterval boundaries, so every pool waited up to a full compose
// interval (~39s at 30 blocks) and a gas pool behind its token pool twice that.
// Vault migration, which that interval paces, has finished before this stage.
const ragnarokFundMigrationInterval = 5

func Ragnarok(rng *rand.Rand) *Actor {
	a := NewActor("Ragnarok", rng)

	key := constants.FundMigrationInterval.String()
	interval := features.NewMultiNodeMimirActor(key, ragnarokFundMigrationInterval, rng)
	interval.Ops = append(interval.Ops, func(config *OpConfig) OpResult {
		mimirs, err := thornode.GetMimirs()
		if err != nil {
			return OpResult{Continue: false, Error: err}
		}
		if mimirs[strings.ToUpper(key)] != ragnarokFundMigrationInterval {
			interval.Log().Info().Int64("value", mimirs[strings.ToUpper(key)]).Msg("waiting for mimir")
			return OpResult{Continue: false}
		}
		return OpResult{Continue: true}
	})
	a.Children[interval] = true

	// ragnarok all gas asset pools (should apply to tokens implicitly)
	for _, chain := range acommon.SimChains {
		interval.Children[core.NewRagnarokPoolActor(chain.GetGasAsset(), rng)] = true
	}

	// verify pool removals
	verify := NewActor("Ragnarok-Verify", rng)
	verify.Ops = append(verify.Ops, func(config *OpConfig) OpResult {
		pools, err := thornode.GetPools()
		if err != nil {
			return OpResult{Finish: true, Error: err}
		}

		// no chains should have pools
		if len(pools) != 0 {
			return OpResult{
				Finish: true,
				Error:  fmt.Errorf("found %d pools after ragnarok", len(pools)),
			}
		}

		return OpResult{Finish: true}
	})
	a.Append(verify)

	return a
}
