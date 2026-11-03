package suites

import (
	"fmt"
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// MemolessSwaps
////////////////////////////////////////////////////////////////////////////////////////

func MemolessSwaps(rng *rand.Rand) *Actor {
	a := NewActor("Memoless Swaps", rng)
	directERC20Flags := NewActor("Direct ERC20 Inbound Flags", rng)
	swaps := NewActor("Memoless Swap Actors", rng)
	directERC20Chains := make(map[common.Chain]bool)

	// memoless swaps
	for _, t := range []core.MemolessType{core.MemolessTypeRef, core.MemolessTypeAmount} {
		for _, chain := range acommon.SimChains {
			// choose a random (other) pool to swap to
			j := rng.IntN(len(acommon.SimChains))
			for chain.Equals(acommon.SimChains[j]) {
				j = rng.IntN(len(acommon.SimChains))
			}
			swaps.Children[core.NewSwapMemolessActor(chain.GetGasAsset(), acommon.SimChains[j].GetGasAsset(), t, rng)] = true
		}
		// Add non-gas EVM token sources to exercise memoless router flow.
		for _, chain := range acommon.SimChains {
			if !chain.IsEVM() {
				continue
			}
			for asset := range evm.Tokens(chain) {
				if asset.IsGasAsset() {
					continue
				}
				toAsset := acommon.SimChains[rng.IntN(len(acommon.SimChains))].GetGasAsset()
				swaps.Children[core.NewSwapMemolessActor(asset, toAsset, t, rng)] = true
				if t == core.MemolessTypeAmount {
					swaps.Children[core.NewDirectERC20SwapMemolessActor(asset, toAsset, rng)] = true
					directERC20Chains[chain] = true
				}
			}
		}

	}

	for chain := range directERC20Chains {
		key := fmt.Sprintf(constants.MimirTemplateEVMDirectERC20Inbound, chain)
		directERC20Flags.Children[core.NewMimirActor(key, 1, rng)] = true
	}
	a.Append(directERC20Flags)
	a.Append(swaps)

	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// Swaps
////////////////////////////////////////////////////////////////////////////////////////

func Swaps(rng *rand.Rand) *Actor {
	a := NewActor("Swaps", rng)

	// gather all pools we expect to swap through
	swapPools := []common.Asset{}
	for _, chain := range acommon.SimChains {
		swapPools = append(swapPools, chain.GetGasAsset())

		// add tokens to swap pools
		if !chain.IsEVM() {
			continue
		}
		for asset := range evm.Tokens(chain) {
			swapPools = append(swapPools, asset)
		}
	}

	// aggregator swap-out: various source assets → EVM gas asset via aggregator.
	// The simulation aggregator contract is deployed deterministically by
	// build/scripts/evm/evm-tool.py on every EVM mocknet chain at the address
	// referenced by evm.MocknetAggregatorAddress. The TKN ERC20 (deployed at
	// admin nonce 1) is used as the finalToken target so the swap-out actor
	// can verify that the user actually received finalToken via the
	// aggregator's swapOut call from router.transferOutAndCall.
	type aggConfig struct {
		chain  common.Chain
		suffix string // aggregator contract address suffix
		target string // aggregator target ERC20 address (finalToken)
		limit  string // aggregator target limit (minimum output from the DEX leg)
	}
	const aggLimit = "1"
	aggConfigs := []aggConfig{
		{common.ETHChain, evm.MocknetAggregatorSuffix, "0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25", aggLimit},
		{common.AVAXChain, evm.MocknetAggregatorSuffix, "0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25", aggLimit},
	}
	for _, cfg := range aggConfigs {
		gasAsset := cfg.chain.GetGasAsset()

		// non-EVM, non-UTXO source → EVM gas asset via aggregator.
		// UTXO chains (BTC, DOGE, LTC) cannot be used here because the aggregator
		// memo fields exceed the 80-byte OP_RETURN limit.
		a.Children[core.NewAggregatorSwapActor(common.ATOMAsset, gasAsset, cfg.suffix, cfg.target, cfg.limit, rng)] = true
		a.Children[core.NewAggregatorSwapActor(common.SOLAsset, gasAsset, cfg.suffix, cfg.target, cfg.limit, rng)] = true

		// EVM token → same-chain gas asset via aggregator (e.g. ETH.TKN → ETH.ETH via aggregator)
		for asset := range evm.Tokens(cfg.chain) {
			if asset.IsGasAsset() {
				continue
			}
			a.Children[core.NewAggregatorSwapActor(asset, gasAsset, cfg.suffix, cfg.target, cfg.limit, rng)] = true
		}

		// cross-chain EVM → different EVM gas asset via aggregator
		for _, otherChain := range acommon.SimChains {
			if !otherChain.IsEVM() || otherChain.Equals(cfg.chain) {
				continue
			}
			a.Children[core.NewAggregatorSwapActor(otherChain.GetGasAsset(), gasAsset, cfg.suffix, cfg.target, cfg.limit, rng)] = true
			break // one cross-chain pair per aggregator is sufficient
		}
	}

	// aggregator swap-in: an external contract calls deposit on the THORChain
	// router via MocknetAggregator.swapIn. Cover both the gas asset path
	// (msg.value) and the ERC20 path (transferFrom + approve) on each EVM
	// mocknet chain so bifrost observes router.Deposit events triggered by
	// contract callers. The destination is BTC to share regression coverage
	// with normal swaps.
	for _, chain := range []common.Chain{common.ETHChain, common.AVAXChain} {
		a.Children[core.NewAggregatorSwapInActor(chain.GetGasAsset(), common.BTCAsset, rng)] = true
		for asset := range evm.Tokens(chain) {
			if asset.IsGasAsset() {
				continue
			}
			a.Children[core.NewAggregatorSwapInActor(asset, common.BTCAsset, rng)] = true
		}
	}

	// swap from each pool to one random one
	for i, pool := range swapPools {
		// always swap SOL <> BTC
		if pool.Chain == common.SOLChain {
			a.Children[core.NewSwapActor(pool, common.BTCAsset, rng)] = true
			a.Children[core.NewSwapActor(common.BTCAsset, pool, rng)] = true
		}

		// always swap to/from GAIA and TRON to ensure memoless outbound coverage
		if pool.Chain == common.GAIAChain {
			a.Children[core.NewSwapActor(common.BTCAsset, pool, rng)] = true
			a.Children[core.NewSwapActor(pool, common.ETHAsset, rng)] = true
		}
		if pool.Chain == common.TRONChain {
			a.Children[core.NewSwapActor(common.ETHAsset, pool, rng)] = true
			a.Children[core.NewSwapActor(pool, common.BTCAsset, rng)] = true
		}

		// choose a random (other) pool to swap to
		j := rng.IntN(len(swapPools))
		for j == i {
			j = rng.IntN(len(swapPools))
		}
		a.Children[core.NewSwapActor(pool, swapPools[j], rng)] = true

		// choose a new random (other) pool to swap from
		j = rng.IntN(len(swapPools))
		for j == i {
			j = rng.IntN(len(swapPools))
		}
		if pool.Equals(common.ZECAsset) {
			// Always exercise both ZEC address encodings.
			a.Children[core.NewZECSwapActor(swapPools[j], false, rng)] = true
			a.Children[core.NewZECSwapActor(swapPools[j], true, rng)] = true
		} else {
			a.Children[core.NewSwapActor(swapPools[j], pool, rng)] = true
		}
	}

	return a
}
