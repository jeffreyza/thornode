package evm

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// NormalizeGasMultipliers keeps Bifrost's signing allowance identical to the
// multipliers enforced by Thornode. Zero means omitted and is normalized to
// the protocol value; an explicit divergent value is rejected at startup.
func NormalizeGasMultipliers(cfg *config.BifrostChainConfiguration) error {
	if cfg == nil {
		return fmt.Errorf("EVM chain configuration is nil")
	}
	if cfg.EVM.TokenMaxGasMultiplier == 0 {
		cfg.EVM.TokenMaxGasMultiplier = constants.EVMTokenMaxGasMultiplier
	}
	if cfg.EVM.AggregatorMaxGasMultiplier == 0 {
		cfg.EVM.AggregatorMaxGasMultiplier = constants.EVMAggregatorMaxGasMultiplier
	}
	if cfg.EVM.TokenMaxGasMultiplier != constants.EVMTokenMaxGasMultiplier {
		return fmt.Errorf(
			"token max gas multiplier %d diverges from Thornode protocol value %d",
			cfg.EVM.TokenMaxGasMultiplier,
			constants.EVMTokenMaxGasMultiplier,
		)
	}
	if cfg.EVM.AggregatorMaxGasMultiplier != constants.EVMAggregatorMaxGasMultiplier {
		return fmt.Errorf(
			"aggregator max gas multiplier %d diverges from Thornode protocol value %d",
			cfg.EVM.AggregatorMaxGasMultiplier,
			constants.EVMAggregatorMaxGasMultiplier,
		)
	}
	return nil
}
