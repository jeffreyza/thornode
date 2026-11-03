package suites

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Bootstrap
////////////////////////////////////////////////////////////////////////////////////////

func Bootstrap(rng *rand.Rand) *Actor {
	a := NewActor("Bootstrap", rng)
	a.Timeout = 10 * time.Minute
	a.Ops = append(a.Ops, waitForInitialXMRInbound)

	pools, err := thornode.GetPools()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get pools")
	}

	// bootstrap pools for all chains
	count := 0
	for _, chain := range acommon.SimChains {
		count++
		a.Children[core.NewDualLPActor(chain.GetGasAsset(), rng)] = true
	}

	// create token pools
	tokenPools := NewActor("Bootstrap-TokenPools", rng)
	for _, chain := range acommon.SimChains {
		if !chain.IsEVM() {
			continue
		}
		// BSC not compatible with sim tests
		if chain.Equals(common.BSCChain) {
			continue
		}
		count++

		for asset := range evm.Tokens(chain) {
			tokenPools.Children[core.NewDualLPActor(asset, rng)] = true
		}
	}
	a.Append(tokenPools)

	// verify pools
	verify := NewActor("Bootstrap-Verify", rng)
	verify.Ops = append(
		verify.Ops, func(config *OpConfig) OpResult {
			pools, err = thornode.GetPools()
			if err != nil {
				return OpResult{Finish: true, Error: err}
			}

			// all pools should be available
			for _, pool := range pools {
				if pool.Status != "Available" {
					return OpResult{
						Finish: true,
						Error:  fmt.Errorf("pool %s not available", pool.Asset),
					}
				}
			}

			// all chains should have pools
			if len(pools) != count {
				return OpResult{
					Finish: true,
					Error:  fmt.Errorf("expected %d pools, got %d", count, len(pools)),
				}
			}

			return OpResult{Finish: true}
		},
	)
	a.Append(verify)

	return a
}

func waitForInitialXMRInbound(config *OpConfig) OpResult {
	for _, chain := range acommon.SimChains {
		if !chain.Equals(common.XMRChain) {
			continue
		}

		inboundAddress, _, err := thornode.GetInboundAddress(common.XMRChain)
		if err != nil {
			log.Info().Err(err).Msg("waiting for XMR inbound address before bootstrap")
			return OpResult{}
		}
		if inboundAddress.IsEmpty() {
			log.Info().Msg("waiting for non-empty XMR inbound address before bootstrap")
			return OpResult{}
		}
		log.Info().Stringer("inbound_address", inboundAddress).Msg("XMR inbound address ready for bootstrap")
		return OpResult{Continue: true}
	}

	return OpResult{Continue: true}
}
