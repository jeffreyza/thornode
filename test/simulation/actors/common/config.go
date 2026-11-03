package common

import "gitlab.com/thorchain/thornode/v3/common"

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

var allSimChains = []common.Chain{
	common.AVAXChain,
	common.BASEChain,
	common.BCHChain,
	common.BTCChain,
	common.DASHChain,
	common.DOGEChain,
	common.ETHChain,
	common.GAIAChain,
	common.LTCChain,
	common.XRPChain,
	common.TRONChain,
	common.SOLChain,
	common.XMRChain,
	common.POLChain,
	common.ZECChain,
}

var SimChains = simChainsWithXMR(true)

// SetXMRChainEnabled toggles whether generic simulation suites include XMR.
func SetXMRChainEnabled(enabled bool) {
	SimChains = simChainsWithXMR(enabled)
}

// XMRChainEnabled reports whether generic simulation suites currently include XMR.
func XMRChainEnabled() bool {
	for _, chain := range SimChains {
		if chain.Equals(common.XMRChain) {
			return true
		}
	}
	return false
}

// simChainsWithXMR returns the canonical simulation chain set with XMR optionally removed.
func simChainsWithXMR(enabled bool) []common.Chain {
	chains := make([]common.Chain, 0, len(allSimChains))
	for _, chain := range allSimChains {
		if !enabled && chain.Equals(common.XMRChain) {
			continue
		}
		chains = append(chains, chain)
	}
	return chains
}
