package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type NetworkMgrDummy struct {
	nas     NodeAccounts
	xmrNas  NodeAccounts
	vault   Vault
	rotated Vaults
}

func NewNetworkMgrDummy() *NetworkMgrDummy {
	return &NetworkMgrDummy{}
}

func (vm *NetworkMgrDummy) CalcAnchor(_ cosmos.Context, _ Manager, _ common.Asset) (cosmos.Uint, cosmos.Uint, cosmos.Uint) {
	return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()
}

func (vm *NetworkMgrDummy) SpawnDerivedAsset(ctx cosmos.Context, asset common.Asset, mgr Manager) {}

func (vm *NetworkMgrDummy) BeginBlock(ctx cosmos.Context, mgr Manager) error {
	return nil
}

func (vm *NetworkMgrDummy) EndBlock(ctx cosmos.Context, mgr Manager) error {
	return nil
}

func (vm *NetworkMgrDummy) TriggerKeygen(_ cosmos.Context, nas NodeAccounts) error {
	vm.nas = nas
	return nil
}

func (vm *NetworkMgrDummy) TriggerXMRKeygen(_ cosmos.Context, nas NodeAccounts) error {
	vm.xmrNas = nas
	return nil
}

func (vm *NetworkMgrDummy) RotateVault(ctx cosmos.Context, vault Vault) error {
	vm.vault = vault
	vm.rotated = append(vm.rotated, vault)
	return nil
}

func (vm *NetworkMgrDummy) UpdateNetwork(ctx cosmos.Context, mgr Manager) error {
	return nil
}

func (vm *NetworkMgrDummy) RecallChainFunds(ctx cosmos.Context, chain common.Chain, mgr Manager, excludeNodeKeys common.PubKeys) error {
	return nil
}

func (vm *NetworkMgrDummy) SwapToOverSolvencyIncome(ctx cosmos.Context, mgr Manager, coin common.Coin) error {
	return nil
}
