package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

////////////////////////////////////////////////////////////////////////////////////////
// ChurnBalanceActor
////////////////////////////////////////////////////////////////////////////////////////

// ChurnBalanceActor verifies that ERC20 token allowances in the router contract
// migrate correctly to the new vault during a churn. It snapshots the current
// active ETH vault's ERC20 balances, triggers a second vault rotation, and
// asserts the incoming vault received equivalent allowances on-chain.
type ChurnBalanceActor struct {
	Actor

	priorAddr   common.Address
	priorRouter common.Address
	priorCoins  map[string]cosmos.Uint // asset string → pre-churn on-chain allowance
}

func NewChurnBalanceActor(rng *rand.Rand) *Actor {
	a := &ChurnBalanceActor{
		Actor: *NewActor("ChurnBalance", rng),
	}
	a.Timeout = 10 * time.Minute

	a.Ops = append(a.Ops, a.capturePreChurnBalance)
	a.Ops = append(a.Ops, a.triggerChurn)
	a.Ops = append(a.Ops, a.waitForMigration)
	a.Ops = append(a.Ops, a.verifyBalance)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

// capturePreChurnBalance snapshots the active ETH vault's ERC20 token allowances
// from the router contract. Fails hard if no ERC20 assets are present — this
// would indicate the preceding swap stages did not deposit any tokens, making
// the verification vacuous.
func (a *ChurnBalanceActor) capturePreChurnBalance(config *OpConfig) OpResult {
	addr, routerPtr, err := thornode.GetInboundAddress(common.ETHChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get ETH inbound address")
		return OpResult{Continue: false}
	}
	if routerPtr == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("no router contract for ETH chain")}
	}
	router := *routerPtr

	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get asgard vaults")
		return OpResult{Continue: false}
	}

	var activeVault *openapi.Vault
	for i := range vaults {
		if vaults[i].Status == thorchain.ActiveVault.String() && vaultHasAddress(vaults[i], common.ETHChain, addr) {
			activeVault = &vaults[i]
			break
		}
	}
	if activeVault == nil {
		a.Log().Error().Msg("no active vault found for ETH chain")
		return OpResult{Continue: false}
	}

	var user *User
	for _, u := range config.Users {
		if u.Acquire() {
			user = u
			break
		}
	}
	if user == nil {
		a.Log().Warn().Msg("no user available for chain client access, retrying")
		return OpResult{Continue: false}
	}
	defer user.Release()

	evmClient, ok := user.ChainClients[common.ETHChain].(*evm.Client)
	if !ok {
		return OpResult{Finish: true, Error: fmt.Errorf("ETH chain client is not *evm.Client")}
	}

	a.priorCoins = make(map[string]cosmos.Uint)
	for _, coin := range activeVault.Coins {
		asset, err := common.NewAsset(coin.Asset)
		if err != nil {
			a.Log().Warn().Str("asset", coin.Asset).Err(err).Msg("skipping unparsable asset")
			continue
		}
		if !asset.GetChain().Equals(common.ETHChain) || asset.IsGasAsset() {
			continue
		}

		balance, err := evmClient.GetVaultAllowance(router, addr, asset)
		if err != nil {
			a.Log().Error().Err(err).Str("asset", coin.Asset).Msg("failed to get vault allowance")
			return OpResult{Continue: false}
		}
		if !balance.IsZero() {
			a.priorCoins[coin.Asset] = balance
			a.Log().Info().
				Str("asset", coin.Asset).
				Stringer("balance", balance).
				Msg("snapshotted pre-churn ERC20 allowance")
		}
	}

	if len(a.priorCoins) == 0 {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("no ERC20 assets found in active ETH vault; test would pass vacuously"),
		}
	}

	a.priorAddr = addr
	a.priorRouter = router
	return OpResult{Continue: true}
}

// triggerChurn schedules a vault rotation and clears the HALTCHURNING mimir. A
// preceding churn stage can leave CHURNINTERVAL far behind the current height,
// so unhalting alone would wait for the next ChurnRetryInterval tick, past this
// actor's timeout.
func (a *ChurnBalanceActor) triggerChurn(config *OpConfig) OpResult {
	node := config.NodeUsers[0]
	if !node.Acquire() {
		a.Log().Warn().Msg("failed to acquire node lock, retrying")
		return OpResult{Continue: false}
	}
	defer node.Release()

	accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{Continue: false}
	}

	if _, err = ScheduleChurn(a.Log(), config); err != nil {
		a.Log().Error().Err(err).Msg("failed to schedule churn")
		return OpResult{Continue: false}
	}

	mimir := thorchain.NewMsgMimir("HALTCHURNING", 0, accAddr)
	txid, err := node.Thorchain.Broadcast(mimir)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast HALTCHURNING mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().Stringer("txid", txid).Msg("triggered churn for ERC20 migration test")
	return OpResult{Continue: true}
}

// waitForMigration polls until the active ETH inbound address has rotated and
// vault migration (fund transfer) is fully complete.
func (a *ChurnBalanceActor) waitForMigration(config *OpConfig) OpResult {
	newAddr, _, err := thornode.GetInboundAddress(common.ETHChain)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get ETH inbound address")
		return OpResult{Continue: false}
	}
	if newAddr.Equals(a.priorAddr) {
		a.Log().Info().Msg("waiting for ETH vault address to rotate")
		return OpResult{Continue: false}
	}

	network, err := thornode.GetNetwork()
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get network state")
		return OpResult{Continue: false}
	}
	if network.VaultsMigrating {
		a.Log().Info().Msg("waiting for vault migration to complete")
		return OpResult{Continue: false}
	}

	a.Log().Info().
		Stringer("prior", a.priorAddr).
		Stringer("new", newAddr).
		Msg("ETH vault rotated and migration complete")
	return OpResult{Continue: true}
}

// verifyBalance checks that each snapshotted ERC20 allowance is present on the
// new vault within a 1% tolerance. Any shortfall is a hard failure.
func (a *ChurnBalanceActor) verifyBalance(config *OpConfig) OpResult {
	newAddr, newRouterPtr, err := thornode.GetInboundAddress(common.ETHChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get new ETH inbound address")
		return OpResult{Continue: false}
	}
	if newRouterPtr == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("no router contract for ETH chain after migration")}
	}
	newRouter := *newRouterPtr

	var user *User
	for _, u := range config.Users {
		if u.Acquire() {
			user = u
			break
		}
	}
	if user == nil {
		a.Log().Warn().Msg("no user available, retrying")
		return OpResult{Continue: false}
	}
	defer user.Release()

	evmClient, ok := user.ChainClients[common.ETHChain].(*evm.Client)
	if !ok {
		return OpResult{Finish: true, Error: fmt.Errorf("ETH chain client is not *evm.Client")}
	}

	failed := false
	for assetStr, priorBalance := range a.priorCoins {
		asset, err := common.NewAsset(assetStr)
		if err != nil {
			a.Log().Error().Str("asset", assetStr).Err(err).Msg("failed to parse asset")
			failed = true
			continue
		}

		newBalance, err := evmClient.GetVaultAllowance(newRouter, newAddr, asset)
		if err != nil {
			a.Log().Error().Err(err).Str("asset", assetStr).Msg("failed to get vault allowance")
			return OpResult{Continue: false}
		}

		threshold := priorBalance.MulUint64(99).QuoUint64(100)
		if newBalance.LT(threshold) {
			a.Log().Error().
				Str("asset", assetStr).
				Stringer("prior", priorBalance).
				Stringer("new", newBalance).
				Stringer("threshold", threshold).
				Msg("ERC20 allowance not fully migrated to new vault")
			failed = true
		} else {
			a.Log().Info().
				Str("asset", assetStr).
				Stringer("prior", priorBalance).
				Stringer("new", newBalance).
				Msg("ERC20 allowance migrated successfully")
		}
	}

	if failed {
		return OpResult{Finish: true, Error: fmt.Errorf("ERC20 vault migration verification failed")}
	}
	return OpResult{Continue: true}
}
