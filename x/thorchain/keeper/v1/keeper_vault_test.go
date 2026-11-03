package keeperv1

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type KeeperVaultSuite struct{}

var _ = Suite(&KeeperVaultSuite{})

func (s *KeeperVaultSuite) TestVault(c *C) {
	ctx, k := setupKeeperForTest(c)
	existVault, err := k.HasValidVaultPools(ctx)
	c.Check(err, IsNil)
	c.Check(existVault, Equals, false)

	asgards, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(asgards, HasLen, 0)
	pubKey := GetRandomPubKey()
	asgard := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, asgard), IsNil)
	asgard2 := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, asgard2), IsNil)
	asgards, err = k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(asgards, HasLen, 1)
	c.Check(asgards[0].PubKey.Equals(pubKey), Equals, true)

	c.Assert(k.DeleteVault(ctx, pubKey), IsNil)
	c.Assert(k.DeleteVault(ctx, pubKey), IsNil) // second time should also not error
	asgards, err = k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(asgards, HasLen, 0)

	vault1 := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault1.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100)),
	})
	c.Check(k.SetVault(ctx, vault1), IsNil)
	c.Check(k.DeleteVault(ctx, vault1.PubKey), NotNil)
}

func (s *KeeperVaultSuite) TestVaultV2(c *C) {
	ctx, k := setupKeeperForTest(c)
	existVault, err := k.HasValidVaultPools(ctx)
	c.Check(err, IsNil)
	c.Check(existVault, Equals, false)

	pubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	asgard := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.SOLChain}.Strings(), []ChainContract{}, eddsaPubKey)
	c.Assert(k.SetVault(ctx, asgard), IsNil)

	// check we can get vault by eddsa key
	v, err := k.GetVault(ctx, eddsaPubKey)
	c.Assert(err, IsNil)
	c.Assert(v.PubKey, Equals, pubKey)
	c.Assert(v.PubKeyEddsa, Equals, eddsaPubKey)
}

func (s *KeeperVaultSuite) TestVaultFrostIndex(c *C) {
	ctx, k := setupKeeperForTest(c)

	pubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	asgard := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.XMRChain}.Strings(), []ChainContract{}, eddsaPubKey)
	asgard.PubKeyFrost = frostPubKey
	c.Assert(k.SetVault(ctx, asgard), IsNil)

	// VaultExists must resolve all three keys.
	c.Check(k.VaultExists(ctx, pubKey), Equals, true, Commentf("ECDSA primary key"))
	c.Check(k.VaultExists(ctx, eddsaPubKey), Equals, true, Commentf("EDDSA index"))
	c.Check(k.VaultExists(ctx, frostPubKey), Equals, true, Commentf("FROST index"))

	// GetVault by frost key must resolve to the ECDSA-keyed vault.
	v, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(v.PubKey.Equals(pubKey), Equals, true)
	c.Assert(v.PubKeyEddsa.Equals(eddsaPubKey), Equals, true)
	c.Assert(v.PubKeyFrost.Equals(frostPubKey), Equals, true)

	// GetVault by eddsa key must also work (regression).
	vByEddsa, err := k.GetVault(ctx, eddsaPubKey)
	c.Assert(err, IsNil)
	c.Assert(vByEddsa.PubKey.Equals(pubKey), Equals, true)
}

func (s *KeeperVaultSuite) TestVaultFrostIndexCreatedBySetVault(c *C) {
	ctx, k := setupKeeperForTest(c)

	pubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	// Create vault without frost key (simulates pre-frost vault).
	asgard := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.XMRChain}.Strings(), []ChainContract{}, eddsaPubKey)
	c.Assert(k.SetVault(ctx, asgard), IsNil)

	// Frost lookup should fail since no frost key was set yet.
	c.Check(k.VaultExists(ctx, frostPubKey), Equals, false)

	// Now upgrade the vault to add the frost key. The next SetVault call
	// (which happens on every vault update — outbound observations, churn,
	// status changes) creates the frost index. There is no lazy migration
	// in GetVault to avoid non-deterministic state writes during read paths.
	asgard.PubKeyFrost = frostPubKey
	c.Assert(k.SetVault(ctx, asgard), IsNil)

	// Frost lookup should now resolve via the index created by SetVault.
	c.Check(k.VaultExists(ctx, frostPubKey), Equals, true)
	resolved, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(resolved.PubKey.Equals(pubKey), Equals, true)
}

func (s *KeeperVaultSuite) TestVaultFrostIndexMissingForNonFrostVault(c *C) {
	ctx, k := setupKeeperForTest(c)

	pubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()

	// Vault with no frost key — common for non-XMR vaults.
	asgard := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.SOLChain}.Strings(), []ChainContract{}, eddsaPubKey)
	c.Assert(k.SetVault(ctx, asgard), IsNil)

	// Looking up by some random pubkey must not return this vault.
	stranger := GetRandomPubKey()
	c.Check(k.VaultExists(ctx, stranger), Equals, false)
}

// TestGetPendingOutboundsIncludesUnexpiredDurableMigrateHeights proves that
// pending migrate txouts older than the rolling signing window still reserve
// funds until they reach the bounded retry window.
func (s *KeeperVaultSuite) TestGetPendingOutboundsIncludesUnexpiredDurableMigrateHeights(c *C) {
	ctx, k := setupKeeperForTest(c)
	signingPeriod := k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	c.Assert(signingPeriod > 0, Equals, true)
	ctx = ctx.WithBlockHeight(signingPeriod*2 + 10)
	staleHeight := ctx.BlockHeight() - signingPeriod - 1

	retiring := NewVault(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XRPChain}.Strings(), nil)
	// Duplicate markers model multiple migrate txouts scheduled in the same block.
	// GetPendingOutbounds must load that height once and count matching txouts,
	// not count the whole txout block once per marker.
	retiring.PendingTxBlockHeights = []int64{staleHeight, staleHeight}
	c.Assert(k.SetVault(ctx, retiring), IsNil)

	txOut := NewTxOut(staleHeight)
	// Mix valid and invalid txouts at the stale height so the test locks the
	// intended filter: same asset, same retiring vault, migrate memo, exact height.
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.XRPChain,
			ToAddress:   GetRandomTHORAddress(),
			VaultPubKey: retiring.PubKey,
			Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
			Memo:        fmt.Sprintf("MIGRATE:%d", staleHeight),
		},
		{
			Chain:       common.XRPChain,
			ToAddress:   GetRandomTHORAddress(),
			VaultPubKey: retiring.PubKey,
			Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(2*common.One)),
			Memo:        fmt.Sprintf("MIGRATE:%d", staleHeight),
		},
		{
			Chain:       common.XRPChain,
			ToAddress:   GetRandomTHORAddress(),
			VaultPubKey: retiring.PubKey,
			Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(3*common.One)),
			Memo:        fmt.Sprintf("MIGRATE:%d", staleHeight-1),
		},
		{
			Chain:       common.XRPChain,
			ToAddress:   GetRandomTHORAddress(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(4*common.One)),
			Memo:        fmt.Sprintf("MIGRATE:%d", staleHeight),
		},
		{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: retiring.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
			Memo:        fmt.Sprintf("MIGRATE:%d", staleHeight),
		},
	}
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	outbounds := k.GetPendingOutbounds(ctx, common.XRPAsset)
	// Only the two exact-height XRP MIGRATE items for the marked retiring vault
	// should be returned; mismatch, unrelated vault, and other-asset items stay out.
	c.Assert(outbounds, HasLen, 2)
	c.Assert(outbounds[0].Coin.Amount.Equal(cosmos.NewUint(common.One)), Equals, true)
	c.Assert(outbounds[1].Coin.Amount.Equal(cosmos.NewUint(2*common.One)), Equals, true)
}

// TestGetPendingOutboundsSkipsExpiredDurableMigrateHeights keeps the helper
// aligned with migrateFunds: expired stale migrate txouts are retry-eligible and
// must not keep reserving vault funds forever.
func (s *KeeperVaultSuite) TestGetPendingOutboundsSkipsExpiredDurableMigrateHeights(c *C) {
	ctx, k := setupKeeperForTest(c)
	signingPeriod := k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	c.Assert(signingPeriod > 0, Equals, true)
	ctx = ctx.WithBlockHeight(signingPeriod*(constants.StaleMigratePendingSigningPeriodMultiplier+1) + 10)
	staleHeight := ctx.BlockHeight() - signingPeriod*constants.StaleMigratePendingSigningPeriodMultiplier - 1

	retiring := NewVault(ctx.BlockHeight()-1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XRPChain}.Strings(), nil)
	// This marker points to a valid migrate txout, but it is just beyond the
	// shared stale-migrate timeout and should be left for migrateFunds to retry.
	retiring.PendingTxBlockHeights = []int64{staleHeight}
	c.Assert(k.SetVault(ctx, retiring), IsNil)

	txOut := NewTxOut(staleHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.XRPChain,
			ToAddress:   GetRandomTHORAddress(),
			VaultPubKey: retiring.PubKey,
			Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One)),
			Memo:        fmt.Sprintf("MIGRATE:%d", staleHeight),
		},
	}
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	outbounds := k.GetPendingOutbounds(ctx, common.XRPAsset)
	c.Assert(outbounds, HasLen, 0)
}

func (s *KeeperVaultSuite) TestGetPendingOutboundsRetainsExtendedClasses(c *C) {
	ctx, k := setupKeeperForTest(c)
	defaultSigningPeriod := k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	overrideSigningPeriod := defaultSigningPeriod * 2
	k.SetMimir(ctx, constants.SigningTransactionPeriod.String(), overrideSigningPeriod)
	signingPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	c.Assert(signingPeriod, Equals, overrideSigningPeriod)
	c.Assert(signingPeriod > 0, Equals, true)
	ctx = ctx.WithBlockHeight(signingPeriod*4 + 10)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	tradeUSDC := usdc.GetTradeAsset()
	vaultPubKey := GetRandomPubKey()

	addTxOut := func(height int64, items ...TxOutItem) {
		txOut := NewTxOut(height)
		txOut.TxArray = items
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}
	extendedItem := func(height int64, token bool) TxOutItem {
		item := TxOutItem{
			Chain:       common.ETHChain,
			VaultPubKey: vaultPubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(height))),
		}
		if token {
			item.Coin = common.NewCoin(usdc, cosmos.NewUint(uint64(height)))
		} else {
			item.Aggregator = "0xaggregator"
		}
		return item
	}

	justPastStandard := ctx.BlockHeight() - signingPeriod - 1
	justInsideExtended := ctx.BlockHeight() - signingPeriod*3 + 1
	atExtendedBoundary := ctx.BlockHeight() - signingPeriod*3
	justPastExtended := ctx.BlockHeight() - signingPeriod*3 - 1
	addTxOut(justPastStandard,
		extendedItem(justPastStandard, false),
		extendedItem(justPastStandard, true),
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(tradeUSDC, cosmos.NewUint(uint64(justPastStandard)))},
		TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(common.XMRAsset, cosmos.NewUint(uint64(justPastStandard)))},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.OneUint())},
	)
	addTxOut(justInsideExtended,
		extendedItem(justInsideExtended, false),
		extendedItem(justInsideExtended, true),
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(tradeUSDC, cosmos.NewUint(uint64(justInsideExtended)))},
		TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(common.XMRAsset, cosmos.NewUint(uint64(justInsideExtended)))},
	)
	addTxOut(atExtendedBoundary,
		extendedItem(atExtendedBoundary, false),
		extendedItem(atExtendedBoundary, true),
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(tradeUSDC, cosmos.NewUint(uint64(atExtendedBoundary)))},
		TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(common.XMRAsset, cosmos.NewUint(uint64(atExtendedBoundary)))},
	)
	addTxOut(justPastExtended,
		extendedItem(justPastExtended, false),
		extendedItem(justPastExtended, true),
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(tradeUSDC, cosmos.NewUint(uint64(justPastExtended)))},
		TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultPubKey, Coin: common.NewCoin(common.XMRAsset, cosmos.NewUint(uint64(justPastExtended)))},
	)

	ethOutbounds := k.GetPendingOutbounds(ctx, common.ETHAsset)
	c.Assert(ethOutbounds, HasLen, 3)
	for _, outbound := range ethOutbounds {
		c.Assert(outbound.Aggregator, Equals, "0xaggregator")
	}
	c.Assert(k.GetPendingOutbounds(ctx, usdc), HasLen, 3)
	c.Assert(k.GetPendingOutbounds(ctx, tradeUSDC), HasLen, 3)
	c.Assert(k.GetPendingOutbounds(ctx, common.XMRAsset), HasLen, 3)
}

func (s *KeeperVaultSuite) TestVaultSorBySecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Add node accounts
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(200 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(300 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)
	na4 := GetRandomValidatorNode(NodeActive)
	na4.Bond = cosmos.NewUint(400 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na4), IsNil)
	na5 := GetRandomValidatorNode(NodeActive)
	na5.Bond = cosmos.NewUint(500 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na5), IsNil)
	na6 := GetRandomValidatorNode(NodeActive)
	na6.Bond = cosmos.NewUint(600 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na6), IsNil)

	// Create Pools
	pool1 := NewPool()
	pool1.Asset = common.ETHAsset
	pool1.BalanceRune = cosmos.NewUint(common.One * 100)
	pool1.BalanceAsset = cosmos.NewUint(common.One * 100)
	c.Assert(k.SetPool(ctx, pool1), IsNil)

	// Create three vaults
	vault1 := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault1.AddFunds(common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One*200)),
	})
	vault1.Membership = []string{
		na1.PubKeySet.Secp256k1.String(),
		na6.PubKeySet.Secp256k1.String(),
	}
	c.Check(k.SetVault(ctx, vault1), IsNil)

	vault2 := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault2.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*1000)),
	})
	vault2.Membership = []string{
		na2.PubKeySet.Secp256k1.String(),
		na5.PubKeySet.Secp256k1.String(),
	}
	c.Check(k.SetVault(ctx, vault2), IsNil)

	vault3 := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault3.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100)),
	})
	vault3.Membership = []string{
		na3.PubKeySet.Secp256k1.String(),
		na4.PubKeySet.Secp256k1.String(),
	}
	c.Check(k.SetVault(ctx, vault3), IsNil)

	// test that we sort appropriately
	sorted := k.SortBySecurity(ctx, Vaults{vault1, vault2, vault3}, 300)
	c.Assert(sorted, HasLen, 3)
	c.Assert(sorted[0].PubKey.Equals(vault2.PubKey), Equals, true)
	c.Assert(sorted[1].PubKey.Equals(vault3.PubKey), Equals, true)
	c.Assert(sorted[2].PubKey.Equals(vault1.PubKey), Equals, true)
}

func (s *KeeperVaultSuite) TestGetMostSecureStrict(c *C) {
	ctx, k := setupKeeperForTest(c)

	na0 := GetRandomValidatorNode(NodeActive)
	na0.Bond = cosmos.NewUint(common.One)
	c.Assert(k.SetNodeAccount(ctx, na0), IsNil)

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(2 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(4 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)

	v0 := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	v0.Membership = []string{
		na0.PubKeySet.Secp256k1.String(),
		na1.PubKeySet.Secp256k1.String(),
	}

	v1 := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	v1.Membership = []string{
		na2.PubKeySet.Secp256k1.String(),
	}

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(common.One * 100)
	pool.BalanceAsset = cosmos.NewUint(common.One * 100)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	signingPeriod := int64(300)
	vaults := make(Vaults, 0)

	// empty vault list should not return a vault
	vault := k.GetMostSecureStrict(ctx, vaults, signingPeriod)
	c.Assert(vault.IsEmpty(), Equals, true)

	// no vault funds, lowest bond wins
	vaults = append(vaults, v0)
	vaults = append(vaults, v1)
	vault = k.GetMostSecureStrict(ctx, vaults, signingPeriod)
	c.Assert(vault.String(), Equals, vaults[0].String())

	// vaults funds being equal, lowest bond wins
	vaults[0].AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*50)),
	})
	vaults[1].AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*50)),
	})
	vault = k.GetMostSecureStrict(ctx, vaults, signingPeriod)
	c.Assert(vault.String(), Equals, vaults[0].String())

	// low bps, same result
	bps := int64(1)
	k.SetMimir(ctx, constants.MigrationVaultSecurityBps.String(), bps)
	c.Assert(vault.String(), Equals, vaults[0].String())

	// higher bps, v0 no longer secure, no vault returned
	bps = int64(1000)
	k.SetMimir(ctx, constants.MigrationVaultSecurityBps.String(), bps)
	vault = k.GetMostSecureStrict(ctx, vaults, signingPeriod)
	c.Assert(vault.IsEmpty(), Equals, true)
}
