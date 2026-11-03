package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func xmrMonovaultChains() common.Chains {
	return common.Chains{common.THORChain, common.XMRChain}
}

func splitAsgardGroupCount(validatorCount int, asgardSize int64) int {
	if validatorCount <= 0 || asgardSize <= 0 {
		return 0
	}
	groupCount := validatorCount / int(asgardSize)
	if validatorCount%int(asgardSize) > 0 {
		groupCount++
	}
	return groupCount
}

func hasDedicatedXMRMonovault(vaults Vaults) bool {
	for _, vault := range vaults {
		if vault.HasChain(common.XMRChain) {
			return true
		}
	}
	return false
}

func shouldMaintainXMRMonovault(vaults Vaults, validatorCount int) bool {
	// Multi-node genesis and steady-state churn both schedule an XMR-capable
	// FROST vault. Keep maintaining it whenever validators exist or an existing
	// XMR-capable vault is already present in vault state.
	return validatorCount > 0 || hasDedicatedXMRMonovault(vaults)
}

func filterVaultsByChain(vaults Vaults, chain common.Chain) Vaults {
	filtered := make(Vaults, 0, len(vaults))
	for _, vault := range vaults {
		if vaultCanSignChain(vault, chain) {
			filtered = append(filtered, vault)
		}
	}
	return filtered
}

func vaultCanSignChain(vault Vault, chain common.Chain) bool {
	if !vault.HasChain(chain) {
		return false
	}
	if _, err := vault.GetAddress(chain); err != nil {
		return false
	}
	return true
}

func preferredActiveVaultsForChain(vaults Vaults, chain common.Chain) Vaults {
	return filterVaultsByChain(vaults, chain)
}

func filterActiveVaultsForOutboundChain(vaults Vaults, chain common.Chain) Vaults {
	filtered := preferredActiveVaultsForChain(vaults, chain)
	if chain.Equals(common.XMRChain) || len(filtered) > 0 {
		return filtered
	}
	return vaults
}

func keygenBlockHasType(keygenBlock KeygenBlock, keygenType types.KeygenType) bool {
	for _, keygen := range keygenBlock.Keygens {
		if keygen.Type == keygenType {
			return true
		}
	}
	return false
}

func keygenMatchesVault(keygen Keygen, vault Vault) bool {
	if !vault.MembershipEquals(keygen.GetMembers()) {
		return false
	}
	switch keygen.Type {
	case XMRMonovaultKeygen:
		return vault.HasChain(common.XMRChain)
	case AsgardKeygen:
		return !vault.HasChain(common.XMRChain)
	default:
		return false
	}
}

func keygenTypeReady(keygenBlock KeygenBlock, readyVaults Vaults, keygenType types.KeygenType) bool {
	seen := false
	for _, keygen := range keygenBlock.Keygens {
		if keygen.Type != keygenType {
			continue
		}
		seen = true
		matched := false
		for _, vault := range readyVaults {
			if keygenMatchesVault(keygen, vault) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return seen
}

func filterVaultsByKeygenType(vaults Vaults, keygenBlock KeygenBlock, keygenType types.KeygenType) Vaults {
	filtered := make(Vaults, 0, len(vaults))
	for _, vault := range vaults {
		for _, keygen := range keygenBlock.Keygens {
			if keygen.Type != keygenType {
				continue
			}
			if keygenMatchesVault(keygen, vault) {
				filtered = append(filtered, vault)
				break
			}
		}
	}
	return filtered
}

func selectActiveVaultForChain(ctx cosmos.Context, k keeper.Keeper, active Vaults, chain common.Chain, signingTransactionPeriod int64) (Vault, error) {
	filtered := preferredActiveVaultsForChain(active, chain)
	if chain == common.THORChain {
		// Keep native THOR routing on ordinary Asgards when they exist. Vaults
		// that also serve the single-vault XMR chain carry THOR for accounting
		// and solvency, but should not become the default THOR traffic target
		// while regular Asgards are available.
		filtered = append(Vaults(nil), active...)
		regular := make(Vaults, 0, len(filtered))
		for _, vault := range filtered {
			if !vault.HasChain(common.XMRChain) {
				regular = append(regular, vault)
			}
		}
		if len(regular) > 0 {
			filtered = regular
		}
	}
	if len(filtered) == 0 {
		return Vault{}, fmt.Errorf("no active vault for chain %s", chain)
	}
	return k.GetMostSecure(ctx, filtered, signingTransactionPeriod), nil
}
