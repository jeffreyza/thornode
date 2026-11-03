package evm

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func TestNormalizeGasMultipliers(t *testing.T) {
	t.Run("omitted values use protocol defaults", func(t *testing.T) {
		cfg := config.BifrostChainConfiguration{}
		require.NoError(t, NormalizeGasMultipliers(&cfg))
		require.Equal(t, constants.EVMTokenMaxGasMultiplier, cfg.EVM.TokenMaxGasMultiplier)
		require.Equal(t, constants.EVMAggregatorMaxGasMultiplier, cfg.EVM.AggregatorMaxGasMultiplier)
	})

	t.Run("matching values are accepted", func(t *testing.T) {
		cfg := config.BifrostChainConfiguration{}
		cfg.EVM.TokenMaxGasMultiplier = constants.EVMTokenMaxGasMultiplier
		cfg.EVM.AggregatorMaxGasMultiplier = constants.EVMAggregatorMaxGasMultiplier
		require.NoError(t, NormalizeGasMultipliers(&cfg))
	})

	for _, tc := range []struct {
		name       string
		token      int64
		aggregator int64
	}{
		{name: "token diverges", token: constants.EVMTokenMaxGasMultiplier + 1},
		{name: "aggregator diverges", aggregator: constants.EVMAggregatorMaxGasMultiplier + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.BifrostChainConfiguration{}
			cfg.EVM.TokenMaxGasMultiplier = tc.token
			cfg.EVM.AggregatorMaxGasMultiplier = tc.aggregator
			require.Error(t, NormalizeGasMultipliers(&cfg))
		})
	}
}
