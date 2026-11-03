package observer

import (
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func setupMocknetThorConfig() {
	cfg := cosmos.GetConfig()
	cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg.SetBech32PrefixForValidator("tthorv", "tthorvpub")
	cfg.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
	cfg.SetCoinType(cmd.THORChainCoinType)
	cfg.SetPurpose(cmd.THORChainCoinPurpose)
}
