package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type VaultSuite struct{}

var _ = Suite(&VaultSuite{})

func (s *VaultSuite) TestVault(c *C) {
	pk := GetRandomPubKey()

	vault := Vault{}
	c.Check(vault.IsEmpty(), Equals, true)
	c.Check(vault.Valid(), NotNil)

	vault = NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Check(vault.PubKey.Equals(pk), Equals, true)
	c.Check(vault.HasFunds(), Equals, false)
	c.Check(vault.IsEmpty(), Equals, false)
	c.Check(vault.Valid(), IsNil)

	coins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(500*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(400*common.One)),
	}

	vault.AddFunds(coins)
	c.Check(vault.HasFunds(), Equals, true)
	c.Check(vault.HasFundsForChain(common.ETHChain), Equals, true)
	c.Check(vault.HasFundsForChain(common.GAIAChain), Equals, false)
	c.Check(vault.Coins, HasLen, 2)
	c.Check(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(500*common.One)), Equals, true)
	c.Check(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(400*common.One)), Equals, true)
	c.Check(vault.HasAsset(common.ETHAsset), Equals, true)
	vault.AddFunds(coins)
	c.Check(vault.Coins, HasLen, 2)
	c.Check(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
	c.Check(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(800*common.One)), Equals, true, Commentf("%+v", vault.GetCoin(common.BTCAsset).Amount))
	vault.SubFundsWithClamping(coins)
	c.Check(vault.Coins, HasLen, 2)
	c.Check(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(500*common.One)), Equals, true)
	c.Check(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(400*common.One)), Equals, true)
	vault.SubFundsWithClamping(coins)
	c.Check(vault.Coins, HasLen, 2)
	c.Check(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(vault.HasFunds(), Equals, false)
	vault.SubFundsWithClamping(coins)
	c.Check(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(vault.HasFunds(), Equals, false)
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(vault.GetChains().Has(common.ETHChain), Equals, true)
	c.Assert(vault.GetChains().Has(common.ETHChain), Equals, true)
	c.Assert(vault.GetChains().Has(common.BTCChain), Equals, true)

	vault1 := NewVault(1024, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, common.Chains{common.ETHChain, common.BTCChain, common.ETHChain}.Strings(), []ChainContract{})
	vault1.Membership = append(vault.Membership, pk.String()) // nolint
	c.Check(vault1.IsType(VaultType_AsgardVault), Equals, true)
	c.Check(vault1.IsAsgard(), Equals, true)
	c.Check(vault1.Contains(pk), Equals, true)
	vault1.UpdateStatus(VaultStatus_RetiringVault, 10000)
	c.Check(vault1.CoinLength(), Equals, 0)
	c.Check(vault1.HasAsset(common.ETHAsset), Equals, false)
}

func (s *VaultSuite) TestHasFundsAboveDust(c *C) {
	pk := GetRandomPubKey()
	vault := NewVault(12, VaultStatus_RetiringVault, VaultType_AsgardVault, pk, common.Chains{common.XMRChain}.Strings(), []ChainContract{})
	c.Check(vault.HasFundsAboveDust(), Equals, false)

	dust := common.XMRChain.DustThreshold()

	// a sub-dust remainder (e.g. the XMR final-round migration buffer) counts
	// for HasFunds but not for HasFundsAboveDust
	vault.AddFunds(common.Coins{common.NewCoin(common.XMRAsset, dust)})
	c.Check(vault.HasFunds(), Equals, true)
	c.Check(vault.HasFundsAboveDust(), Equals, false)

	// anything above the chain dust threshold counts
	vault.AddFunds(common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))})
	c.Check(vault.HasFundsAboveDust(), Equals, true)

	// Duplicate gas-asset entries are considered as one aggregate balance.
	// Neither entry is above dust by itself, but their sum is.
	halfDust := dust.QuoUint64(2)
	vault.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, halfDust),
		common.NewCoin(common.XMRAsset, dust.Sub(halfDust)),
	}
	c.Check(vault.HasFundsAboveDust(), Equals, false)
	vault.Coins[1].Amount = vault.Coins[1].Amount.Add(cosmos.OneUint())
	c.Check(vault.HasFundsAboveDust(), Equals, true)

	// Non-gas assets remain actionable even when below the chain's gas-asset
	// dust threshold.
	tokenAsset := common.Asset{Chain: common.ETHChain, Symbol: "TOKEN", Ticker: "TOKEN"}
	vault.Coins = common.NewCoins(common.NewCoin(tokenAsset, cosmos.OneUint()))
	c.Assert(tokenAsset.IsGasAsset(), Equals, false)
	c.Assert(cosmos.OneUint().LT(common.ETHChain.DustThreshold()), Equals, true)
	c.Check(vault.HasFundsAboveDust(), Equals, true)

	// RUNE is omitted from the calculation, matching HasFunds
	vault1 := NewVault(12, VaultStatus_RetiringVault, VaultType_AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault1.AddFunds(common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))})
	c.Check(vault1.HasFunds(), Equals, false)
	c.Check(vault1.HasFundsAboveDust(), Equals, false)

	// chains without a dust threshold treat any non-zero amount as funds
	fakeAsset := common.Asset{Chain: common.THORChain, Symbol: "FOO", Ticker: "FOO"}
	vault2 := NewVault(12, VaultStatus_RetiringVault, VaultType_AsgardVault, pk, common.Chains{common.THORChain}.Strings(), []ChainContract{})
	vault2.AddFunds(common.Coins{common.NewCoin(fakeAsset, cosmos.NewUint(1))})
	c.Check(vault2.HasFundsAboveDust(), Equals, true)
}

func (s *VaultSuite) TestHasFundsAboveDustWithDeficit(c *C) {
	vault := NewVault(12, VaultStatus_RetiringVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain, common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, common.XMRChain.DustThreshold()))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.OneUint()))

	// Deficit is retained as an accounting position, but does not turn terminal
	// gas dust into funds that should block churn.
	c.Assert(vault.HasFundsAboveDust(), Equals, false)
	c.Assert(vault.HasDeficit(), Equals, true)
	c.Assert(vault.HasFundsOrDeficit(), Equals, true)
}

func (s *VaultSuite) TestHasDeficitIncludesRune(c *C) {
	vault := NewVault(12, VaultStatus_InactiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), []ChainContract{})
	c.Assert(vault.HasDeficit(), Equals, false)

	vault.Deficit = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.OneUint()))
	c.Assert(vault.HasDeficit(), Equals, true)
	c.Assert(vault.HasDeficitForAsset(common.RuneAsset()), Equals, true)
	c.Assert(vault.HasFundsOrDeficit(), Equals, true)
}

// TestVaultSubFundsWithDeficit verifies exact debits and underflow deficit accounting.
func (s *VaultSuite) TestVaultSubFundsWithDeficit(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))))

	missed := vault.SubFundsWithDeficit(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))))
	c.Assert(missed.IsEmpty(), Equals, true)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)

	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10))))
	missed = vault.SubFundsWithDeficit(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))))
	c.Assert(missed.EqualsEx(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(90)))), Equals, true)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(90)), Equals, true)
	c.Assert(vault.HasDeficit(), Equals, true)
	c.Assert(vault.HasDeficitForAsset(common.ETHAsset), Equals, true)
	c.Assert(vault.HasFundsOrDeficit(), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
}

// TestVaultSubFundsWithDeficitAggregatesDuplicateDebits verifies duplicate debit entries are summed.
func (s *VaultSuite) TestVaultSubFundsWithDeficitAggregatesDuplicateDebits(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(50))))

	missed := vault.SubFundsWithDeficit(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(30)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(30)),
	})

	c.Assert(missed.EqualsEx(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))), Equals, true)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(10)), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
}

// TestVaultFundTransitionsPreserveDecimals verifies that creating a deficit
// and later creating a settled remainder retain external precision metadata.
func (s *VaultSuite) TestVaultFundTransitionsPreserveDecimals(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	debit := common.NewCoin(common.ETHAsset, cosmos.NewUint(100)).WithDecimals(18)

	missed := vault.SubFundsWithDeficit(common.NewCoins(debit))
	c.Assert(missed.GetCoin(common.ETHAsset).Decimals, Equals, int64(18))
	c.Assert(vault.Deficit.GetCoin(common.ETHAsset).Decimals, Equals, int64(18))

	credit := common.NewCoin(common.ETHAsset, cosmos.NewUint(150)).WithDecimals(18)
	vault.AddFunds(common.NewCoins(credit))
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.IsZero(), Equals, true)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(50)), Equals, true)
	c.Assert(vault.GetCoin(common.ETHAsset).Decimals, Equals, int64(18))
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
}

// TestVaultAddFundsSettlesDeficit verifies same-asset credits settle deficit first.
func (s *VaultSuite) TestVaultAddFundsSettlesDeficit(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain, common.BTCChain}.Strings(), []ChainContract{})
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(152)))
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))))
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(52)), Equals, true)

	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(7))))
	c.Assert(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(7)), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(52)), Equals, true)

	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(300))))
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(248)), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(7)), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
}

// TestVaultAddFundsPreservesZeroMarkersWithDeficit verifies zero-amount coins
// keep their scheduling marker behavior and do not settle same-asset deficit.
func (s *VaultSuite) TestVaultAddFundsPreservesZeroMarkersWithDeficit(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))

	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.ZeroUint())))
	c.Assert(vault.Coins.Contains(common.NewCoin(common.ETHAsset, cosmos.ZeroUint())), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(10)), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)

	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(15))))
	c.Assert(vault.Coins, HasLen, 1)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(5)), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
}

// TestVaultDeficitIncidentShape covers the observed underflow and later settlement shape.
func (s *VaultSuite) TestVaultDeficitIncidentShape(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(166*common.One/10))))

	missed := vault.SubFundsWithDeficit(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(169*common.One))))
	c.Assert(missed.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1524*common.One/10)), Equals, true)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(1524*common.One/10)), Equals, true)

	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(300*common.One))))
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1476*common.One/10)), Equals, true)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)
}

// TestVaultDeficitInvariant verifies the per-asset coins and deficit invariant.
func (s *VaultSuite) TestVaultDeficitInvariant(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(vault.ValidateDeficitInvariant(), NotNil)

	vault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	c.Assert(vault.ValidateDeficitInvariant(), IsNil)

	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(4)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(6)),
	}
	vault.Deficit = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(0)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
	}
	c.Assert(vault.ValidateDeficitInvariant(), NotNil)
}

func (s *VaultSuite) TestGetTssSigners(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	nodeAccounts := NodeAccounts{}
	memberShip := make([]string, 0)
	for i := 0; i < 10; i++ {
		na := GetRandomValidatorNode(NodeStatus_Active)
		nodeAccounts = append(nodeAccounts, na)
		memberShip = append(memberShip, na.PubKeySet.Secp256k1.String())
	}
	vault.Membership = memberShip
	addrs := []cosmos.AccAddress{
		nodeAccounts[0].NodeAddress,
		nodeAccounts[1].NodeAddress,
	}
	keys, err := vault.GetMembers(addrs)
	c.Assert(err, IsNil)
	c.Assert(keys, HasLen, 2)
	c.Assert(keys[0].Equals(nodeAccounts[0].PubKeySet.Secp256k1), Equals, true)
	c.Assert(keys[1].Equals(nodeAccounts[1].PubKeySet.Secp256k1), Equals, true)
}

func (s *VaultSuite) TestPendingTxBlockHeights(c *C) {
	vault := NewVault(12, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})

	version := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(version)
	vault.AppendPendingTxBlockHeights(1, constAccessor)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{1})
	vault.AppendPendingTxBlockHeights(2, constAccessor)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{2, 1})
	vault.AppendPendingTxBlockHeights(2, constAccessor)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{2, 2, 1})
	vault.AppendPendingTxBlockHeights(1000, constAccessor)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{1000, 2, 2, 1})
	vault.RemovePendingTxBlockHeights(1000)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{2, 2, 1})
	vault.RemovePendingTxBlockHeights(2)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{2, 1})
	vault.RemovePendingTxBlockHeights(1001)
	c.Assert(vault.PendingTxBlockHeights, DeepEquals, []int64{2, 1})
}

func (s *VaultSuite) TestVaultSort(c *C) {
	vault := NewVault(1024, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(),
		common.Chains{
			common.BTCChain,
			common.ETHChain,
			common.GAIAChain,
		}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One*50)),
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One*10)),
	})
	vault1 := NewVault(1024, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(),
		common.Chains{
			common.BTCChain,
			common.ETHChain,
			common.GAIAChain,
		}.Strings(), []ChainContract{})
	vault1.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*90)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One*90)),
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(common.One*90)),
	})
	vaults := Vaults{
		vault, vault1,
	}
	vaults1 := vaults.SortBy(common.BTCAsset)
	c.Check(vaults1[0].PubKey.Equals(vault1.PubKey), Equals, true)
	atomVault := vaults.SelectByMaxCoin(common.ATOMAsset)
	c.Check(atomVault.PubKey.Equals(vault1.PubKey), Equals, true)

	ethVault := vaults.SelectByMinCoin(common.ETHAsset)
	c.Check(ethVault.PubKey.Equals(vault1.PubKey), Equals, true)
	addr, err := vault.PubKey.GetAddress(common.ETHChain)
	c.Check(err, IsNil)
	result, err := vaults.HasAddress(common.ETHChain, addr)
	c.Check(err, IsNil)
	c.Check(result, Equals, true)
	result1, err1 := vaults.HasAddress(common.BTCChain, GetRandomBTCAddress())
	c.Check(err1, IsNil)
	c.Check(result1, Equals, false)

	// Ensure XMR uses the vault FROST pubkey when deriving addresses.
	xmrVault := NewVaultV2(
		1024,
		VaultStatus_ActiveVault,
		VaultType_AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.XMRChain}.Strings(),
		[]ChainContract{},
		common.EmptyPubKey,
	)
	xmrVault.PubKeyFrost = GetRandomEd25519PubKey()
	xmrAddr, err := xmrVault.PubKeyFrost.GetAddress(common.XMRChain)
	c.Check(err, IsNil)
	hasXMRAddr, err := Vaults{xmrVault}.HasAddress(common.XMRChain, xmrAddr)
	c.Check(err, IsNil)
	c.Check(hasXMRAddr, Equals, true)
}
