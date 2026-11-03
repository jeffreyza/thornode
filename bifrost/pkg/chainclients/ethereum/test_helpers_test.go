package ethereum

import (
	"sync"

	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var mocknetThorConfigOnce sync.Once

func setupMocknetThorConfig() {
	// Cosmos configuration is global and its getters do not synchronize with
	// setters. Later tests must not rewrite it while HTTP handlers read it.
	mocknetThorConfigOnce.Do(func() {
		cfg := cosmos.GetConfig()
		cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
		cfg.SetBech32PrefixForValidator("tthorv", "tthorvpub")
		cfg.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
		cfg.SetCoinType(cmd.THORChainCoinType)
		cfg.SetPurpose(cmd.THORChainCoinPurpose)
	})
}
