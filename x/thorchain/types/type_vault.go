package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Vaults a list of vault
type Vaults []Vault

// NewVault create a new instance of vault
func NewVault(height int64, status VaultStatus, vtype VaultType, pk common.PubKey, chains []string, routers []ChainContract) Vault {
	return Vault{
		BlockHeight: height,
		StatusSince: height,
		PubKey:      pk,
		Coins:       make(common.Coins, 0),
		Type:        vtype,
		Status:      status,
		Chains:      chains,
		Routers:     routers,
	}
}

func (m Vault) GetChains() common.Chains {
	chains := make(common.Chains, 0)
	for _, c := range m.Chains {
		chain, err := common.NewChain(c)
		if err != nil {
			continue
		}
		chains = append(chains, chain)
	}
	return chains
}

func (m Vault) HasChain(chain common.Chain) bool {
	return m.GetChains().Has(chain)
}

func (m Vault) GetMembership() common.PubKeys {
	pubkeys := make(common.PubKeys, 0)
	for _, pk := range m.Membership {
		pk, err := common.NewPubKey(pk)
		if err != nil {
			continue
		}
		pubkeys = append(pubkeys, pk)
	}
	return pubkeys
}

// IsType determine whether the vault is given type
func (m Vault) IsType(vtype VaultType) bool {
	return m.Type == vtype
}

// IsAsgard check whether the vault is Asgard vault, it returns true when it is an asgard vault
func (m Vault) IsAsgard() bool {
	return m.IsType(VaultType_AsgardVault)
}

// IsXMRMonovault is retained for legacy tests that assert the old dedicated
// THOR+XMR shape. Runtime XMR routing should use chain capability/readiness
// checks rather than this vault-topology predicate.
func (m Vault) IsXMRMonovault() bool {
	if !m.IsAsgard() {
		return false
	}
	chains := m.GetChains().Distinct()
	if !chains.Has(common.XMRChain) {
		return false
	}
	for _, chain := range chains {
		if !chain.Equals(common.THORChain) && !chain.Equals(common.XMRChain) {
			return false
		}
	}
	return true
}

// IsActive return true when the vault is in active status
func (m Vault) IsActive() bool {
	return m.Status == VaultStatus_ActiveVault
}

// IsRetiring return true when the vault is in retiring status
func (m Vault) IsRetiring() bool {
	return m.Status == VaultStatus_RetiringVault
}

// IsEmpty returns true when the vault pubkey is empty
func (m Vault) IsEmpty() bool {
	return m.PubKey.IsEmpty()
}

// Contains check whether the given pubkey is party of the originally node who create this vault
func (m Vault) Contains(pubkey common.PubKey) bool {
	// Equivalent to m.GetMembership().Contains(pubkey): PubKey equality is
	// string equality, so only a member string equal to pubkey can match, and
	// only that member needs the (amino) validity check GetMembership applies.
	for _, member := range m.Membership {
		if member != pubkey.String() {
			continue
		}
		if _, err := common.NewPubKey(member); err == nil {
			return true
		}
	}
	return false
}

// MembershipEquals check whether the vault has the same membership as the given pubkeys
func (m Vault) MembershipEquals(pks common.PubKeys) bool {
	if len(m.Membership) != len(pks) {
		return false
	}
	for _, pk := range pks {
		if !m.Contains(pk) {
			return false
		}
	}
	return true
}

// UpdateStatus set the vault to given status
func (m *Vault) UpdateStatus(s VaultStatus, height int64) {
	m.Status = s
	m.StatusSince = height
}

// Valid check whether Vault has all necessary values
func (m Vault) Valid() error {
	if m.PubKey.IsEmpty() {
		return errors.New("pubkey cannot be empty")
	}
	return nil
}

// HasFunds check whether the vault pool has fund
func (m Vault) HasFunds() bool {
	for _, coin := range m.Coins {
		if !coin.IsRune() { // non-native rune is omitted from the calculation
			if !coin.Amount.IsZero() {
				return true
			}
		}
	}
	return false
}

// HasFundsAboveDust checks whether the vault has funds that still require
// migration. Only gas-asset amounts at or below their chain's dust threshold
// are terminal dust; any positive non-gas balance remains actionable.
func (m Vault) HasFundsAboveDust() bool {
	for _, coin := range m.coinAmounts() {
		if coin.IsRune() { // native RUNE is omitted from the calculation
			continue
		}
		if !coin.Asset.IsGasAsset() || coin.Amount.GT(coin.Asset.GetChain().DustThreshold()) {
			return true
		}
	}
	return false
}

// GetDeficit returns the unsettled deficit for an asset.
func (m Vault) GetDeficit(asset common.Asset) common.Coin {
	amount := cosmos.ZeroUint()
	for _, coin := range m.Deficit {
		if coin.Asset.Equals(asset) {
			amount = amount.Add(coin.Amount)
		}
	}
	return common.NewCoin(asset, amount)
}

// HasDeficit check whether the vault has an unsettled deficit.
func (m Vault) HasDeficit() bool {
	for _, coin := range m.Deficit {
		if !coin.IsEmpty() {
			return true
		}
	}
	return false
}

// HasDeficitForAsset check whether the vault has an unsettled deficit for an asset.
func (m Vault) HasDeficitForAsset(asset common.Asset) bool {
	return !m.GetDeficit(asset).Amount.IsZero()
}

// HasFundsOrDeficit check whether the vault still has an accounting position.
func (m Vault) HasFundsOrDeficit() bool {
	return m.HasFunds() || m.HasDeficit()
}

// HasFundsForChain check whether the vault pool has funds for a specific chain
func (m Vault) HasFundsForChain(chain common.Chain) bool {
	for _, coin := range m.Coins {
		if coin.Asset.GetChain().Equals(chain) && !coin.Amount.IsZero() {
			return true
		}
	}
	return false
}

// CoinLength - counts the number of coins this vault has
func (m Vault) CoinLength() (count int) {
	for _, coin := range m.Coins {
		if !coin.Amount.IsZero() {
			count++
		}
	}
	return
}

// CoinLengthByChain - count the number of coins this vault has for the given chain
func (m Vault) CoinLengthByChain(c common.Chain) int {
	total := 0
	for _, coin := range m.Coins {
		if coin.Asset.GetChain().Equals(c) && !coin.Amount.IsZero() {
			total++
		}
	}
	return total
}

// HasAsset Check if this vault has a particular asset
func (m Vault) HasAsset(asset common.Asset) bool {
	return !m.GetCoin(asset).Amount.IsZero()
}

// GetCoin return coin type of given asset
func (m Vault) GetCoin(asset common.Asset) common.Coin {
	for _, coin := range m.Coins {
		if coin.Asset.Equals(asset) {
			return coin
		}
	}
	return common.NewCoin(asset, cosmos.ZeroUint())
}

// GetMembers return members who's address exist in the given list
func (m Vault) GetMembers(activeObservers []cosmos.AccAddress) (common.PubKeys, error) {
	signers := common.PubKeys{}
	for _, k := range m.GetMembership() {
		addr, err := k.GetThorAddress()
		if err != nil {
			return common.PubKeys{}, fmt.Errorf("fail to get thor address: %w", err)
		}
		for _, item := range activeObservers {
			if item.Equals(addr) {
				signers = append(signers, k)
			}
		}
	}
	return signers, nil
}

// GetContract return the contract that match the request chain
func (v Vault) GetContract(chain common.Chain) ChainContract {
	for _, item := range v.Routers {
		if item.Chain.Equals(chain) {
			return item
		}
	}
	return ChainContract{}
}

// UpdateContract update the chain contract
func (v *Vault) UpdateContract(chainContract ChainContract) {
	exist := false
	for i, item := range v.Routers {
		if item.Chain.Equals(chainContract.Chain) {
			v.Routers[i] = chainContract
			exist = true
		}
	}
	if !exist {
		v.Routers = append(v.Routers, chainContract)
	}
}

// AddFunds add given coins into vault
func (m *Vault) AddFunds(coins common.Coins) {
	for _, coin := range coins {
		if coin.Asset.IsEmpty() {
			continue
		}
		// Preserve the existing side effect of AddFunds: observing a coin for a
		// new chain also adds that chain to the vault.
		if !m.GetChains().Has(coin.Asset.GetChain()) {
			m.Chains = append(m.Chains, coin.Asset.GetChain().String())
		}

		if coin.Amount.IsZero() {
			// Zero-amount coins are used as scheduling markers, not real
			// credits, so they must not settle same-asset deficit.
			m.addFund(coin)
			continue
		}

		deficit := m.GetDeficit(coin.Asset).Amount
		if deficit.IsZero() {
			m.addFund(coin)
			continue
		}

		// A same-asset credit first settles any recorded deficit. Only the
		// remainder, if any, becomes settled vault balance.
		if coin.Amount.GTE(deficit) {
			m.Deficit = vaultDeficitWithAmount(m.Deficit, coin, cosmos.ZeroUint())
			remainder := coin.Amount.Sub(deficit)
			if !remainder.IsZero() {
				remainderCoin := coin
				remainderCoin.Amount = remainder
				m.addFund(remainderCoin)
			}
			continue
		}

		// Partial settlement leaves the vault with less deficit and no new
		// settled balance for this asset.
		m.Deficit = vaultDeficitWithAmount(m.Deficit, coin, deficit.Sub(coin.Amount))
	}
}

// addFund adds one coin to the vault without settling deficit.
func (m *Vault) addFund(coin common.Coin) {
	for i, ycoin := range m.Coins {
		if ycoin.Asset.Equals(coin.Asset) {
			m.Coins[i].Amount = ycoin.Amount.Add(coin.Amount)
			return
		}
	}

	if !m.GetChains().Has(coin.Asset.GetChain()) {
		m.Chains = append(m.Chains, coin.Asset.GetChain().String())
	}

	m.Coins = append(m.Coins, coin)
}

// SubFundsWithClamping subtracts coins from the vault using legacy clamping semantics.
// Callers that need underflow accounting should use SubFundsWithDeficit.
func (m *Vault) SubFundsWithClamping(coins common.Coins) {
	for _, coin := range coins {
		m.subFund(coin)
	}
}

// SubFundsWithDeficit subtracts coins and records any underflow as unsettled deficit.
func (m *Vault) SubFundsWithDeficit(coins common.Coins) common.Coins {
	missed := common.Coins{}
	// Aggregate by asset before comparing against vault balance. tx.Tx.Coins
	// should only ever have one entry per asset in practice, but if duplicates
	// ever appear, the SUM must be compared against the vault balance.
	aggregated := common.Coins{}.Add(coins...).NoneEmpty()
	for _, coin := range aggregated {
		available := m.coinAmount(coin.Asset)
		if available.GTE(coin.Amount) {
			m.Coins = vaultCoinsWithAmount(m.Coins, coin, available.Sub(coin.Amount))
			continue
		}

		deficitDelta := coin.Amount.Sub(available)
		m.Coins = vaultCoinsWithAmount(m.Coins, coin, cosmos.ZeroUint())
		m.Deficit = vaultDeficitWithAmount(m.Deficit, coin, m.GetDeficit(coin.Asset).Amount.Add(deficitDelta))
		missedCoin := coin
		missedCoin.Amount = deficitDelta
		missed = missed.Add(missedCoin)
	}
	return missed.NoneEmpty()
}

// subFund subtracts one coin from the vault without recording deficit.
// Underflows clamp to zero to preserve SubFundsWithClamping's behavior.
func (m *Vault) subFund(coin common.Coin) {
	for i, ycoin := range m.Coins {
		if coin.Asset.Equals(ycoin.Asset) {
			m.Coins[i].Amount = common.SafeSub(ycoin.Amount, coin.Amount)
			return
		}
	}
}

// ValidateDeficitInvariant checks that no asset has both settled coins and deficit.
func (m Vault) ValidateDeficitInvariant() error {
	for _, coin := range m.coinAmounts() {
		deficitAmount := m.GetDeficit(coin.Asset).Amount
		if deficitAmount.IsZero() {
			continue
		}
		return fmt.Errorf("vault %s has both coins and deficit for %s: coins=%s deficit=%s", m.PubKey, coin.Asset, coin.Amount, deficitAmount)
	}
	return nil
}

// coinAmounts returns non-empty vault coins aggregated by asset in first-seen order.
func (m Vault) coinAmounts() common.Coins {
	return common.Coins{}.Add(m.Coins...).NoneEmpty()
}

// coinAmount returns the aggregate vault coin amount for an asset.
func (m Vault) coinAmount(asset common.Asset) cosmos.Uint {
	amount := cosmos.ZeroUint()
	for _, coin := range m.Coins {
		if coin.Asset.Equals(asset) {
			amount = amount.Add(coin.Amount)
		}
	}
	return amount
}

// vaultCoinsWithAmount returns coins with one normalized amount for an asset,
// preserving an existing zero coin entry to match vault balance semantics.
func vaultCoinsWithAmount(coins common.Coins, coin common.Coin, amount cosmos.Uint) common.Coins {
	return coinsWithAmount(coins, coin, amount, true)
}

// vaultDeficitWithAmount returns deficits with zero amounts removed once settled.
// Unlike vaultCoinsWithAmount, it does not preserve zero amount entries.
func vaultDeficitWithAmount(deficit common.Coins, coin common.Coin, amount cosmos.Uint) common.Coins {
	return coinsWithAmount(deficit, coin, amount, false)
}

// coinsWithAmount returns a copy of coins with exactly one entry for coin.Asset set
// to amount. Duplicate entries for the same asset are collapsed into the first
// matching position, preserving the relative order of all other assets.
// keepZeroAmount controls whether setting amount to zero keeps that first
// matching entry or removes the asset entirely.
func coinsWithAmount(coins common.Coins, target common.Coin, amount cosmos.Uint, keepZeroAmount bool) common.Coins {
	found := false
	result := make(common.Coins, 0, len(coins)+1)
	for _, coin := range coins {
		if !coin.Asset.Equals(target.Asset) {
			result = append(result, coin)
			continue
		}
		if found {
			// Drop duplicate entries for the asset being normalized.
			continue
		}
		found = true
		if amount.IsZero() && !keepZeroAmount {
			// Deficits use zero as removal, while vault coins may keep a zero
			// balance entry to match existing vault accounting behavior.
			continue
		}
		coin.Amount = amount
		result = append(result, coin)
	}
	if !found && !amount.IsZero() {
		// Only append a missing asset when there is a non-zero balance to store,
		// preserving metadata such as the incoming coin's external decimals.
		target.Amount = amount
		result = append(result, target)
	}
	return result
}

func (m *Vault) DeductVaultPendingOutbounds(pendingOutbounds []TxOutItem) {
	for _, txOutItem := range pendingOutbounds {
		if !txOutItem.VaultPubKey.Equals(m.PubKey) {
			continue
		}
		// only still outstanding txout will be considered
		if !txOutItem.OutHash.IsEmpty() {
			continue
		}
		// deduct the gas asset from the vault as well
		var gasCoin common.Coin
		if !txOutItem.MaxGas.IsEmpty() {
			gasCoin = txOutItem.MaxGas.ToCoins().GetCoin(txOutItem.Chain.GetGasAsset())
		}
		for i := range m.Coins {
			if m.Coins[i].Asset.Equals(txOutItem.Coin.Asset) {
				m.Coins[i].Amount = common.SafeSub(m.Coins[i].Amount, txOutItem.Coin.Amount)
			}
			if m.Coins[i].Asset.Equals(gasCoin.Asset) {
				m.Coins[i].Amount = common.SafeSub(m.Coins[i].Amount, gasCoin.Amount)
			}
		}
	}
}

// AppendPendingTxBlockHeights adds the current block height into the list.
// Heights are removed when the corresponding migrate outbound is observed.
func (m *Vault) AppendPendingTxBlockHeights(blockHeight int64, _ constants.ConstantValues) {
	// Do not prune by SigningTransactionPeriod here. Migrate scheduling uses these
	// heights to find outstanding txouts until the matching observation removes them.
	heights := []int64{blockHeight}
	heights = append(heights, m.PendingTxBlockHeights...)
	m.PendingTxBlockHeights = heights
}

// RemovePendingTxBlockHeights remove the given block height from internal pending tx block height
func (m *Vault) RemovePendingTxBlockHeights(blockHeight int64) {
	idxToRemove := -1
	for idx, item := range m.PendingTxBlockHeights {
		if item == blockHeight {
			idxToRemove = idx
			break
		}
	}
	if idxToRemove != -1 {
		m.PendingTxBlockHeights = append(m.PendingTxBlockHeights[:idxToRemove], m.PendingTxBlockHeights[idxToRemove+1:]...)
	}
}

func (m *Vault) AlgoPubKey(chain common.Chain) (common.PubKey, error) {
	switch chain.GetSigningAlgo() {
	case common.SigningAlgoSecp256k1:
		return m.PubKey, nil
	case common.SigningAlgoEd25519:
		if chain == common.XMRChain {
			if m.PubKeyFrost.IsEmpty() {
				return "", fmt.Errorf("xmr vault has no frost public key")
			}
			return m.PubKeyFrost, nil
		}
		if m.PubKeyEddsa.IsEmpty() {
			return "", fmt.Errorf("vault has no eddsa public key")
		}
		return m.PubKeyEddsa, nil
	}
	return "", fmt.Errorf("unsupported signing algorithm: %s", chain.GetSigningAlgo())
}

// GetAddress return the address of the vault, depends on the chain's signing algorithm
// if the chain is using secp256k1 , it will return the secp256k1 address
// if the chain is using eddsa , it will return the eddsa address
// avoid calling Vault.PubKey.GetAddress directly , as it will only return the secp256k1 address
func (m *Vault) GetAddress(chain common.Chain) (common.Address, error) {
	pubKey, err := m.AlgoPubKey(chain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to get vault address: %w", err)
	}
	return pubKey.GetAddress(chain)
}

// SortBy order coins by the given asset
func (vs Vaults) SortBy(sortBy common.Asset) Vaults {
	// use the vault pool with the highest quantity of our coin
	sort.SliceStable(vs[:], func(i, j int) bool {
		return vs[i].GetCoin(sortBy).Amount.GT(vs[j].GetCoin(sortBy).Amount)
	})
	return vs
}

// Has return vaults that have that amount of coins in it
func (vs Vaults) Has(target common.Coin) Vaults {
	have := make(Vaults, 0)
	for _, v := range vs {
		coin := v.GetCoin(target.Asset)
		if coin.Amount.GTE(target.Amount) {
			have = append(have, v)
		}
	}
	return have
}

// SelectByMinCoin return the vault that has least of given asset
func (vs Vaults) SelectByMinCoin(asset common.Asset) (vault Vault) {
	if len(vs) == 1 {
		vault = vs[0]
		return
	}
	for _, v := range vs {
		if vault.IsEmpty() || v.GetCoin(asset).Amount.LT(vault.GetCoin(asset).Amount) {
			vault = v
		}
	}
	return
}

// SelectByMaxCoin return the vault that has most of given asset
func (vs Vaults) SelectByMaxCoin(asset common.Asset) (vault Vault) {
	if len(vs) == 1 {
		vault = vs[0]
		return
	}
	for _, v := range vs {
		if vault.IsEmpty() || v.GetCoin(asset).Amount.GT(vault.GetCoin(asset).Amount) {
			vault = v
		}
	}
	return
}

// HasAddress will go through the vaults to determinate whether any of the
// vault match the given address on the given chain
func (vs Vaults) HasAddress(chain common.Chain, address common.Address) (bool, error) {
	for _, item := range vs {
		addr, err := item.GetAddress(chain)
		if err != nil {
			return false, fmt.Errorf("fail to get address from vault(%s) for chain(%s): %w", item.PubKey, chain, err)
		}
		if addr.Equals(address) {
			return true, nil
		}
	}
	return false, nil
}

// MarshalJSON marshal vault status to string
func (x VaultStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(x.String())
}

// UnmarshalJSON parse string type back to VaultStatus
func (x *VaultStatus) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if val, ok := VaultStatus_value[s]; ok {
		*x = VaultStatus(val)
		return nil
	}
	return fmt.Errorf("invalid vault status value: %s", s)
}

// MarshalJSON marshal vault type to string
func (x VaultType) MarshalJSON() ([]byte, error) {
	return json.Marshal(x.String())
}

// UnmarshalJSON parse string type back to VaultType
func (x *VaultType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if val, ok := VaultType_value[s]; ok {
		*x = VaultType(val)
		return nil
	}
	return fmt.Errorf("invalid vault type value: %s", s)
}
