// Package signing provides shared HALTSIGNING / HaltSigning<Chain> gating
// for every bifrost-internal signing entry point.
//
// The bifrost outbound queue path applies HALTSIGNING in signer/sign.go
// (signAndBroadcast). Several flows bypass that path — UTXO and Monero
// consolidation build TxOutItems locally, and EVM/ETH unstuck rebuilds and
// re-signs cancel transactions inline. The two functions below let those
// flows enforce the same halt switches as the normal outbound signer.
package signing

import (
	"fmt"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// HaltChecker is the minimal slice of thorclient.ThorchainBridge that the
// halt-check helpers need. Defined locally so callers can pass any concrete
// bridge implementation without importing the full thorclient surface.
type HaltChecker interface {
	GetBlockHeight() (int64, error)
	GetMimir(key string) (int64, error)
}

// CheckSigningHalt is the core check: it reports whether HALTSIGNING (global)
// or HaltSigning<Chain> is active at the given THORChain block height. It does
// no logging and applies no error policy — the caller decides what a mimir
// fetch error means. The `global` return distinguishes the two halts so the
// caller can keep its own log wording.
func CheckSigningHalt(bridge HaltChecker, chain common.Chain, height int64) (halted, global bool, err error) {
	haltGlobal, err := bridge.GetMimir("HALTSIGNING")
	if err != nil {
		return false, false, fmt.Errorf("fail to get HALTSIGNING mimir: %w", err)
	}
	if haltGlobal > 0 && haltGlobal <= height {
		return true, true, nil
	}
	chainKey := fmt.Sprintf(constants.MimirTemplateHaltSigning, chain)
	haltChain, err := bridge.GetMimir(chainKey)
	if err != nil {
		return false, false, fmt.Errorf("fail to get %s mimir: %w", chainKey, err)
	}
	if haltChain > 0 && haltChain <= height {
		return true, false, nil
	}
	return false, false, nil
}

// IsSigningHalted wraps CheckSigningHalt for retry loops such as consolidation
// and EVM/ETH unstuck. It fetches the current THORChain height, logs, and
// treats any fetch error as "skip this round" (returns true). These loops
// re-run every block, so swallowing the error is safe — the next pass will
// retry the lookup.
func IsSigningHalted(bridge HaltChecker, chain common.Chain, logger zerolog.Logger) bool {
	height, err := bridge.GetBlockHeight()
	if err != nil {
		logger.Err(err).Stringer("chain", chain).Msg("fail to get thorchain block height, treating signing as halted")
		return true
	}
	halted, global, err := CheckSigningHalt(bridge, chain, height)
	if err != nil {
		logger.Err(err).Stringer("chain", chain).Msg("fail to check signing halt, treating signing as halted")
		return true
	}
	switch {
	case halted && global:
		logger.Info().Stringer("chain", chain).Msg("signing skipped: HALTSIGNING is active")
	case halted:
		logger.Info().Stringer("chain", chain).Msgf("signing skipped: %s is halted", chain)
	}
	return halted
}
