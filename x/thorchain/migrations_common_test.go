package thorchain

import (
	"bytes"
	"sort"
	"strings"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/protobuf/encoding/protowire"
	. "gopkg.in/check.v1"
)

type MigrationCommonSuite struct{}

var _ = Suite(&MigrationCommonSuite{})

func (s *MigrationCommonSuite) TestMigrate7to8Debug(c *C) {
	ctx, mgr := setupManagerForTest(c)
	migrator := NewMigrator(mgr)

	err := migrator.Migrate7to8(ctx)
	c.Assert(err, IsNil)
}

func (s *MigrationCommonSuite) TestMigrate8to9OverflowFix(c *C) {
	// Test the overflow fix logic directly

	// Scenario 1: Normal case where withheld >= spent
	withheld1 := cosmos.NewUint(2000)
	spent1 := cosmos.NewUint(1000)
	var surplus1 cosmos.Uint

	if withheld1.GTE(spent1) {
		surplus1 = withheld1.Sub(spent1)
	} else {
		surplus1 = cosmos.ZeroUint()
	}

	c.Assert(surplus1.Equal(cosmos.NewUint(1000)), Equals, true, Commentf("Expected surplus of 1000, got %s", surplus1.String()))

	// Scenario 2: Overflow case where spent > withheld (this would have panicked before the fix)
	withheld2 := cosmos.NewUint(1000)
	spent2 := cosmos.NewUint(2000)
	var surplus2 cosmos.Uint

	if withheld2.GTE(spent2) {
		surplus2 = withheld2.Sub(spent2)
	} else {
		surplus2 = cosmos.ZeroUint()
	}

	c.Assert(surplus2.IsZero(), Equals, true, Commentf("Expected surplus of 0, got %s", surplus2.String()))

	// Scenario 3: Edge case where withheld == spent
	withheld3 := cosmos.NewUint(1000)
	spent3 := cosmos.NewUint(1000)
	var surplus3 cosmos.Uint

	if withheld3.GTE(spent3) {
		surplus3 = withheld3.Sub(spent3)
	} else {
		surplus3 = cosmos.ZeroUint()
	}

	c.Assert(surplus3.IsZero(), Equals, true, Commentf("Expected surplus of 0, got %s", surplus3.String()))
}

func (s *MigrationCommonSuite) TestBurnReserveAndReduceMaxSupply(c *C) {
	ctx, mgr := setupManagerForTest(c)
	migrator := NewMigrator(mgr)
	k := mgr.Keeper()

	// Verify pre-state: reserve > 9.3M and total supply > 360M
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	supplyBefore := k.GetTotalSupply(ctx, common.RuneAsset())
	c.Assert(reserveBefore.GT(cosmos.NewUint(9_300_000_00000000)), Equals, true,
		Commentf("reserve should be > 9.3M, got %s", reserveBefore.String()))
	c.Assert(supplyBefore.GT(cosmos.NewUint(360_000_000_00000000)), Equals, true,
		Commentf("total supply should be > 360M, got %s", supplyBefore.String()))

	// Run migration
	err := migrator.BurnReserveAndReduceMaxSupply(ctx)
	c.Assert(err, IsNil)

	// Post-burn reserve should be 9.3M
	reserveAfter := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reserveAfter.Equal(cosmos.NewUint(9_300_000_00000000)), Equals, true,
		Commentf("reserve should be 9.3M, got %s", reserveAfter.String()))

	// MaxRuneSupply must equal post-burn total supply (no state drift)
	postBurnSupply := k.GetTotalSupply(ctx, common.RuneAsset())
	maxSupplyMimir, _ := k.GetMimir(ctx, "MaxRuneSupply")
	c.Assert(maxSupplyMimir > 0, Equals, true)
	c.Assert(postBurnSupply.Equal(cosmos.NewUint(uint64(maxSupplyMimir))), Equals, true,
		Commentf("MaxRuneSupply (%d) should equal post-burn total supply (%s)", maxSupplyMimir, postBurnSupply.String()))

	// Total supply must be <= MaxRuneSupply
	c.Assert(postBurnSupply.LTE(cosmos.NewUint(uint64(maxSupplyMimir))), Equals, true,
		Commentf("total supply (%s) should be <= MaxRuneSupply (%d)", postBurnSupply.String(), maxSupplyMimir))
}

// makeClampFixVault returns a saved vault with a single ETH coin balance and
// the given pubkey. Used by the fixVaultClamps_v3_18_0 tests.
func makeClampFixVault(c *C, ctx cosmos.Context, mgr *Mgrs, pubKey common.PubKey, ethAmount cosmos.Uint) Vault {
	v := GetRandomVault()
	v.PubKey = pubKey
	v.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, ethAmount))
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	return v
}

func (s *MigrationCommonSuite) TestFixVaultClamps_v3_18_0_HappyPath(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Three random pubkeys (the production vaults won't exist on mocknet,
	// and the data table is keyed to mainnet bech32 — for the test we pass
	// a custom fixes slice keyed to vaults we actually save here).
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()

	const headroom uint64 = 1_000_000_000
	const a1 uint64 = 15262957100
	const a2 uint64 = 4392790000
	const a3 uint64 = 7387020000

	v1 := makeClampFixVault(c, ctx, mgr, pk1, cosmos.NewUint(a1+headroom))
	v2 := makeClampFixVault(c, ctx, mgr, pk2, cosmos.NewUint(a2+headroom))
	v3 := makeClampFixVault(c, ctx, mgr, pk3, cosmos.NewUint(a3+headroom))

	fixes := []vaultClampFix{
		{pubKey: pk1.String(), amount: a1},
		{pubKey: pk2.String(), amount: a2},
		{pubKey: pk3.String(), amount: a3},
	}
	fixVaultClamps_v3_18_0(ctx, mgr, fixes)

	// Each vault's ETH balance should be exactly headroom after the fix.
	for _, pk := range []common.PubKey{v1.PubKey, v2.PubKey, v3.PubKey} {
		got, err := mgr.Keeper().GetVault(ctx, pk)
		c.Assert(err, IsNil)
		c.Assert(got.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, headroom,
			Commentf("vault %s: expected %d after fix, got %s", pk.String(), headroom, got.GetCoin(common.ETHAsset).Amount.String()))
	}
}

func (s *MigrationCommonSuite) TestFixVaultClamps_v3_18_0_PreFlightSkip(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Vault has insufficient ETH for the fix — pre-flight should skip and
	// leave the vault state unchanged.
	pk := GetRandomPubKey()
	const fixAmt uint64 = 15262957100
	const beforeAmt uint64 = 5_000_000_000 // < fixAmt, would clamp again

	makeClampFixVault(c, ctx, mgr, pk, cosmos.NewUint(beforeAmt))

	fixes := []vaultClampFix{
		{pubKey: pk.String(), amount: fixAmt},
	}
	fixVaultClamps_v3_18_0(ctx, mgr, fixes)

	// Vault state must be unchanged.
	got, err := mgr.Keeper().GetVault(ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(got.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, beforeAmt,
		Commentf("vault %s: expected unchanged %d, got %s", pk.String(), beforeAmt, got.GetCoin(common.ETHAsset).Amount.String()))
}

func (s *MigrationCommonSuite) TestFixVaultClamps_v3_18_0_Mixed(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// One vault has enough; the other doesn't. The first should be debited,
	// the second untouched, and the loop must continue past the skip.
	pkOK := GetRandomPubKey()
	pkSkip := GetRandomPubKey()

	const fixA uint64 = 4392790000
	const fixB uint64 = 7387020000
	const headroom uint64 = 1_000_000_000

	makeClampFixVault(c, ctx, mgr, pkOK, cosmos.NewUint(fixA+headroom))
	const skipBefore uint64 = 1_000 // way below fixB
	makeClampFixVault(c, ctx, mgr, pkSkip, cosmos.NewUint(skipBefore))

	fixes := []vaultClampFix{
		{pubKey: pkSkip.String(), amount: fixB}, // skip first to prove loop continues
		{pubKey: pkOK.String(), amount: fixA},
	}
	fixVaultClamps_v3_18_0(ctx, mgr, fixes)

	gotOK, err := mgr.Keeper().GetVault(ctx, pkOK)
	c.Assert(err, IsNil)
	c.Assert(gotOK.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, headroom,
		Commentf("OK vault: expected %d, got %s", headroom, gotOK.GetCoin(common.ETHAsset).Amount.String()))

	gotSkip, err := mgr.Keeper().GetVault(ctx, pkSkip)
	c.Assert(err, IsNil)
	c.Assert(gotSkip.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, skipBefore,
		Commentf("Skipped vault: expected unchanged %d, got %s", skipBefore, gotSkip.GetCoin(common.ETHAsset).Amount.String()))
}

func (s *MigrationCommonSuite) TestFixVaultClamps_v3_18_0_RetiringVaultStillReachable(c *C) {
	// Confirm GetVault returns the vault regardless of status — the
	// production vaults may have transitioned to InactiveVault by 3.18.0.
	ctx, mgr := setupManagerForTest(c)

	pk := GetRandomPubKey()
	const fixAmt uint64 = 4392790000
	const headroom uint64 = 1_000_000_000

	v := GetRandomVault()
	v.PubKey = pk
	v.Status = InactiveVault
	v.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(fixAmt+headroom)))
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)

	fixes := []vaultClampFix{{pubKey: pk.String(), amount: fixAmt}}
	fixVaultClamps_v3_18_0(ctx, mgr, fixes)

	got, err := mgr.Keeper().GetVault(ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(got.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, headroom)
}

// Encode the actual pre-upgrade wire format, including the deprecated owner field.
func seedOwnerCollector(c *C, ctx cosmos.Context, mgr *Mgrs, owner cosmos.AccAddress, amount string) []byte {
	bz := protowire.AppendTag(nil, 1, protowire.BytesType)
	bz = protowire.AppendBytes(bz, owner)
	bz = protowire.AppendTag(bz, 2, protowire.BytesType)
	bz = protowire.AppendString(bz, amount)
	key := mgr.Keeper().GetKey("affcol/", owner.String())
	c.Assert(mgr.storeService.OpenKVStore(ctx).Set(key, bz), IsNil)
	return key
}

func (s *MigrationCommonSuite) TestNamedCollectorMigration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	owner, expiredOwner, singleOwner, orphan := GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()
	// Count expired THORNames too: one active and one expired THORName still share a
	// collector. Owners with just one stored THORName keep their funds in that THORName.
	var originalNames []THORName
	for _, name := range []string{"tan", "few", "ALPHA", "SOLO"} {
		tn := NewTHORName(name, 1000, []THORNameAlias{{Chain: common.BTCChain, Address: GetRandomBTCAddress()}})
		tn.Owner = owner
		switch name {
		case "ALPHA":
			tn.ExpireBlockHeight = ctx.BlockHeight() - 1
			tn.Owner = expiredOwner
		case "tan":
			tn.ExpireBlockHeight = ctx.BlockHeight() - 1
		case "SOLO":
			tn.Owner = singleOwner
		}
		tn.PreferredAsset = common.BTCAsset
		tn.PreferredAssetOutboundFeeMultiplier = 100
		k.SetTHORName(ctx, tn)
		originalNames = append(originalNames, tn)
	}
	ownerKey := seedOwnerCollector(c, ctx, mgr, owner, "12345")
	expiredKey := seedOwnerCollector(c, ctx, mgr, expiredOwner, "25")
	singleKey := seedOwnerCollector(c, ctx, mgr, singleOwner, "100")
	orphanKey := seedOwnerCollector(c, ctx, mgr, orphan, "6789")
	// Empty legacy collectors need no payment, even for an unpayable owner.
	zeroKey := seedOwnerCollector(c, ctx, mgr, k.GetModuleAccAddress(ReserveName), "0")
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "solo", RuneAmount: cosmos.NewUint(42)}), IsNil)
	FundModule(c, ctx, k, AffiliateCollectorName, 12345+25+100+6789+42)
	ownerBefore := k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount
	expiredBefore := k.GetBalanceOf(ctx, expiredOwner, common.RuneAsset()).Amount
	singleBefore := k.GetBalanceOf(ctx, singleOwner, common.RuneAsset()).Amount
	orphanBefore := k.GetBalanceOf(ctx, orphan, common.RuneAsset()).Amount
	beforeSupply := k.GetTotalSupply(ctx, common.RuneAsset())

	c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
	// The collector upgrade preserves paid registration time and payout settings.
	for _, original := range originalNames {
		stored, err := k.GetTHORNameRecord(ctx, original.Name)
		c.Assert(err, IsNil)
		c.Check(stored, DeepEquals, original)
	}
	assertNamedAffiliateBalance(c, ctx, k, "few", 0)
	assertNamedAffiliateBalance(c, ctx, k, "tan", 0)
	assertNamedAffiliateBalance(c, ctx, k, "ALPHA", 25)
	assertNamedAffiliateBalance(c, ctx, k, "SOLO", 142)
	c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(cosmos.NewUint(167)), Equals, true)
	c.Check(k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount.Sub(ownerBefore).Equal(cosmos.NewInt(12345)), Equals, true)
	c.Check(k.GetBalanceOf(ctx, expiredOwner, common.RuneAsset()).Amount.Equal(expiredBefore), Equals, true)
	c.Check(k.GetBalanceOf(ctx, singleOwner, common.RuneAsset()).Amount.Equal(singleBefore), Equals, true)
	c.Check(k.GetBalanceOf(ctx, orphan, common.RuneAsset()).Amount.Sub(orphanBefore).Equal(cosmos.NewInt(6789)), Equals, true)
	c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(beforeSupply), Equals, true)
	for _, key := range [][]byte{ownerKey, expiredKey, singleKey, orphanKey, zeroKey} {
		has, err := mgr.storeService.OpenKVStore(ctx).Has(key)
		c.Assert(err, IsNil)
		c.Check(has, Equals, false)
	}
	collectors, err := k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Assert(collectors, HasLen, 2)
	c.Check(collectors[0].Name, Equals, "alpha")
	c.Check(collectors[1].Name, Equals, "solo")
	assertAffiliateIntegrationInvariant(c, ctx, k)
	events := ctx.EventManager().Events()
	migrationEvents := 0
	migrated := make(map[string]string)
	names := make(map[string]string)
	for _, event := range events {
		if event.Type == "affiliate_collector_migration" {
			migrationEvents++
			attributes := make(map[string]string)
			for _, attribute := range event.Attributes {
				attributes[attribute.Key] = attribute.Value
			}
			migrated[attributes["owner"]] = attributes["rune_amount"]
			names[attributes["owner"]] = attributes["thorname"]
		}
	}
	c.Check(migrationEvents, Equals, 4)
	c.Check(migrated, DeepEquals, map[string]string{owner.String(): "12345", expiredOwner.String(): "25", singleOwner.String(): "100", orphan.String(): "6789"})
	c.Check(names, DeepEquals, map[string]string{owner.String(): "", expiredOwner.String(): "alpha", singleOwner.String(): "solo", orphan.String(): ""})
	// Re-running after the committed upgrade cannot pay or credit twice.
	c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
	after, err := k.GetAffiliateCollectors(ctx)
	c.Assert(err, IsNil)
	c.Check(after, DeepEquals, collectors)
	c.Check(ctx.EventManager().Events(), DeepEquals, events)
	c.Check(k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount.Sub(ownerBefore).Equal(cosmos.NewInt(12345)), Equals, true)
	c.Check(k.GetBalanceOf(ctx, expiredOwner, common.RuneAsset()).Amount.Equal(expiredBefore), Equals, true)
	c.Check(k.GetBalanceOf(ctx, singleOwner, common.RuneAsset()).Amount.Equal(singleBefore), Equals, true)
	c.Check(k.GetBalanceOf(ctx, orphan, common.RuneAsset()).Amount.Sub(orphanBefore).Equal(cosmos.NewInt(6789)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(cosmos.NewUint(167)), Equals, true)
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationReconciliation(c *C) {
	const total = 116 * common.One
	for _, funded := range []uint64{total - 84011399, total - 1, total, total + 1, total + common.One} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		owner, orphan := GetRandomBech32Addr(), GetRandomBech32Addr()
		tn := NewTHORName("few", 1000, nil)
		tn.Owner = owner
		k.SetTHORName(ctx, tn)
		seedOwnerCollector(c, ctx, mgr, owner, "7000000000")
		seedOwnerCollector(c, ctx, mgr, orphan, "3000000000")
		seedOwnerCollector(c, ctx, mgr, k.GetModuleAccAddress(AffiliateCollectorName), "500000000")
		// Existing THORName claims must stay backed as well as migrated claims.
		c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: tn.Name, RuneAmount: cosmos.NewUint(11 * common.One)}), IsNil)
		FundModule(c, ctx, k, AffiliateCollectorName, funded)
		reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
		orphanBalance := k.GetBalanceOf(ctx, orphan, common.RuneAsset()).Amount
		supply := k.GetTotalSupply(ctx, common.RuneAsset())
		deficit := common.SafeSub(cosmos.NewUint(total), cosmos.NewUint(funded))
		surplus := common.SafeSub(cosmos.NewUint(funded), cosmos.NewUint(total))

		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
		// The full module-owned legacy balance still goes to Reserve separately.
		reserveAfter := reserve.Sub(deficit).Add(surplus).Add(cosmos.NewUint(5 * common.One))
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveAfter), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(cosmos.NewUint(81*common.One)), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, tn.Name, 81*common.One)
		c.Check(k.GetBalanceOf(ctx, orphan, common.RuneAsset()).Amount.Sub(orphanBalance).String(), Equals, "3000000000")
		c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(supply), Equals, true)
		assertAffiliateIntegrationInvariant(c, ctx, k)
		var adjustments cosmos.Events
		for _, event := range ctx.EventManager().Events() {
			if event.Type == "affiliate_collector_migration_reconcile" {
				adjustments = append(adjustments, event)
			}
		}
		if funded == total {
			c.Check(adjustments, HasLen, 0)
		} else {
			from, to := ReserveName, AffiliateCollectorName
			if funded > total {
				from, to = AffiliateCollectorName, ReserveName
			}
			c.Assert(adjustments, HasLen, 1)
			c.Check(adjustments[0], DeepEquals, cosmos.NewEvent("affiliate_collector_migration_reconcile",
				cosmos.NewAttribute("from_module", from),
				cosmos.NewAttribute("to_module", to),
				cosmos.NewAttribute("rune_amount", deficit.Add(surplus).String()),
			))
		}
		events := ctx.EventManager().Events()
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
		c.Check(ctx.EventManager().Events(), DeepEquals, events)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveAfter), Equals, true)

		// A later surplus must not trigger another reconciliation on a rerun.
		FundModule(c, ctx, k, AffiliateCollectorName, 1)
		events = ctx.EventManager().Events()
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveAfter), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(cosmos.NewUint(81*common.One+1)), Equals, true)
		c.Check(ctx.EventManager().Events(), DeepEquals, events)
	}
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationReconciliationFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	owner := GetRandomBech32Addr()
	tn := NewTHORName("few", 1000, nil)
	tn.Owner = owner
	k.SetTHORName(ctx, tn)
	key := seedOwnerCollector(c, ctx, mgr, owner, "100")
	FundModule(c, ctx, k, AffiliateCollectorName, 50)
	reserve := common.NewCoins(common.NewCoin(common.RuneAsset(), k.GetRuneBalanceOfModule(ctx, ReserveName)))
	c.Assert(k.SendFromModuleToModule(ctx, ReserveName, AsgardName, reserve), IsNil)
	store := mgr.storeService.OpenKVStore(ctx)
	before, err := store.Get(key)
	c.Assert(err, IsNil)
	events := ctx.EventManager().Events()
	c.Assert(NewMigrator(mgr).Migrate18to19(ctx), ErrorMatches, ".*reconcile affiliate collector balance from reserve to affiliate_collector.*")
	after, err := store.Get(key)
	c.Assert(err, IsNil)
	c.Check(after, DeepEquals, before)
	c.Check(ctx.EventManager().Events(), DeepEquals, events)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "50")
	assertNamedAffiliateBalance(c, ctx, k, tn.Name, 0)
	FundModule(c, ctx, k, ReserveName, 50)
	c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true)
	assertNamedAffiliateBalance(c, ctx, k, tn.Name, 100)
	assertAffiliateIntegrationInvariant(c, ctx, k)

	// A later deficit cannot trigger another Reserve donation after migration.
	c.Assert(k.SendFromModuleToModule(ctx, AffiliateCollectorName, AsgardName,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)))), IsNil)
	FundModule(c, ctx, k, ReserveName, 10)
	events = ctx.EventManager().Events()
	c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).String(), Equals, "10")
	c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "99")
	c.Check(ctx.EventManager().Events(), DeepEquals, events)
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationRollback(c *C) {
	for _, funded := range []uint64{300, 900} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		owners := []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}
		sort.Slice(owners, func(i, j int) bool { return owners[i].String() < owners[j].String() })
		tn := NewTHORName("few", 1000, nil)
		tn.Owner = owners[0]
		k.SetTHORName(ctx, tn)
		// The second owner's payout belongs to a sole historical THORName that cannot be
		// renewed under current rules. Its failed transfer must roll back too.
		historical := NewTHORName("thorswap.fi", 1000, nil)
		historical.Owner = owners[1]
		k.SetTHORName(ctx, historical)
		firstKey := seedOwnerCollector(c, ctx, mgr, owners[0], "100")
		secondKey := seedOwnerCollector(c, ctx, mgr, owners[1], "200")
		thirdKey := seedOwnerCollector(c, ctx, mgr, owners[2], "300")
		store := mgr.storeService.OpenKVStore(ctx)
		firstBefore, err := store.Get(firstKey)
		c.Assert(err, IsNil)
		secondBefore, err := store.Get(secondKey)
		c.Assert(err, IsNil)
		thirdBefore, err := store.Get(thirdKey)
		c.Assert(err, IsNil)
		FundModule(c, ctx, k, AffiliateCollectorName, funded)
		reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
		firstBalance := k.GetBalanceOf(ctx, owners[0], common.RuneAsset()).Amount
		secondBalance := k.GetBalanceOf(ctx, owners[1], common.RuneAsset()).Amount
		thirdBalance := k.GetBalanceOf(ctx, owners[2], common.RuneAsset()).Amount
		oldVersion := semver.MustParse("3.0.0")
		k.SetVersionWithCtx(ctx, oldVersion)
		events := ctx.EventManager().Events()
		// Reconcile in either direction, credit the first THORName, then fail after the
		// second owner's bank transfer. All transfers and migration writes roll back.
		mgr.K = thornameSettlementFailKeeper{Keeper: k}
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), ErrorMatches, ".*pay affiliate collector to "+owners[1].String()+".*injected failure after bank transfer")
		firstAfter, err := store.Get(firstKey)
		c.Assert(err, IsNil)
		secondAfter, err := store.Get(secondKey)
		c.Assert(err, IsNil)
		thirdAfter, err := store.Get(thirdKey)
		c.Assert(err, IsNil)
		c.Check(firstAfter, DeepEquals, firstBefore)
		c.Check(secondAfter, DeepEquals, secondBefore)
		c.Check(thirdAfter, DeepEquals, thirdBefore)
		c.Check(k.GetBalanceOf(ctx, owners[0], common.RuneAsset()).Amount.Equal(firstBalance), Equals, true)
		c.Check(k.GetBalanceOf(ctx, owners[1], common.RuneAsset()).Amount.Equal(secondBalance), Equals, true)
		c.Check(k.GetBalanceOf(ctx, owners[2], common.RuneAsset()).Amount.Equal(thirdBalance), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(cosmos.NewUint(funded)), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve), Equals, true)
		collector, err := k.GetAffiliateCollector(ctx, tn.Name)
		c.Assert(err, IsNil)
		c.Check(collector.RuneAmount.IsZero(), Equals, true)
		c.Check(ctx.EventManager().Events(), DeepEquals, events)
		storedVersion, _ := k.GetVersionWithCtx(ctx)
		c.Check(storedVersion.Equals(oldVersion), Equals, true)

		// Retrying adjusts Reserve once, just like an uninterrupted run.
		mgr.K = k
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
		collector, err = k.GetAffiliateCollector(ctx, tn.Name)
		c.Assert(err, IsNil)
		c.Check(collector.RuneAmount.Equal(cosmos.NewUint(100)), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(cosmos.NewUint(100)), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve.Add(cosmos.NewUint(funded)).Sub(cosmos.NewUint(600))), Equals, true)
		c.Check(k.GetBalanceOf(ctx, owners[0], common.RuneAsset()).Amount.Equal(firstBalance), Equals, true)
		c.Check(k.GetBalanceOf(ctx, owners[1], common.RuneAsset()).Amount.Sub(secondBalance).Equal(cosmos.NewInt(200)), Equals, true)
		c.Check(k.GetBalanceOf(ctx, owners[2], common.RuneAsset()).Amount.Sub(thirdBalance).Equal(cosmos.NewInt(300)), Equals, true)
		for _, key := range [][]byte{firstKey, secondKey, thirdKey} {
			has, hasErr := store.Has(key)
			c.Assert(hasErr, IsNil)
			c.Check(has, Equals, false)
		}
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationLegacyNames(c *C) {
	for _, expired := range []bool{false, true} {
		for _, names := range [][]string{{"THORSWAP.FI"}, {strings.Repeat("a", 31)}, {"few", "shapeshift.com"}, {"keepkey.com", "portis.io"}} {
			ctx, mgr := setupManagerForTest(c)
			k := mgr.Keeper()
			owner := GetRandomBech32Addr()
			var originals []THORName
			for _, name := range names {
				tn := NewTHORName(name, ctx.BlockHeight()+1000, []THORNameAlias{{Chain: common.BTCChain, Address: GetRandomBTCAddress()}})
				if expired {
					tn.ExpireBlockHeight = ctx.BlockHeight() - 1
				}
				tn.Owner = owner
				tn.PreferredAsset = common.BTCAsset
				tn.PreferredAssetOutboundFeeMultiplier = 100
				k.SetTHORName(ctx, tn)
				originals = append(originals, tn)
			}
			key := seedOwnerCollector(c, ctx, mgr, owner, "100")
			FundModule(c, ctx, k, AffiliateCollectorName, 100)
			before := k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount
			supplyBefore := k.GetTotalSupply(ctx, common.RuneAsset())
			c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
			for _, original := range originals {
				stored, err := k.GetTHORNameRecord(ctx, original.Name)
				c.Assert(err, IsNil)
				c.Check(stored, DeepEquals, original)
			}
			collectors, err := k.GetAffiliateCollectors(ctx)
			c.Assert(err, IsNil)
			c.Check(collectors, HasLen, 0)
			c.Check(k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount.Sub(before).String(), Equals, "100")
			c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).IsZero(), Equals, true)
			c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(supplyBefore), Equals, true)
			has, err := mgr.storeService.OpenKVStore(ctx).Has(key)
			c.Assert(err, IsNil)
			c.Check(has, Equals, false)
			assertAffiliateIntegrationInvariant(c, ctx, k)

			// A rerun must not pay the legacy balance twice.
			events := ctx.EventManager().Events()
			c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
			c.Check(k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount.Sub(before).String(), Equals, "100")
			c.Check(ctx.EventManager().Events(), DeepEquals, events)

			// The in-place migration preserves THORNames without changing the
			// pre-existing genesis validation rules for their spelling.
			c.Assert(ValidateGenesis(ExportGenesis(ctx, k)), ErrorMatches, "invalid THORName|THORName cannot exceed 30 characters")
		}
	}
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationModuleOwnerAfterQueuedConversion(c *C) {
	for _, module := range []string{AffiliateCollectorName, ReserveName, StableReserveName} {
		ctx, mgr, few := setupCollectorSwapExecution(c)
		k := mgr.Keeper()
		previousOwner := few.Owner
		msg := collectorConversionForTest(ctx, mgr, few, types.SwapVersion_v1)
		c.Assert(k.SetSwapQueueItem(ctx, *msg, int(msg.Index)), IsNil)
		// Legacy registration allowed this transfer while a conversion was queued.
		// Model its stored result because the new handler rejects module owners.
		few.Owner = k.GetModuleAccAddress(module)
		k.SetTHORName(ctx, few)
		c.Assert(newSwapQueue(k).EndBlock(ctx, mgr), IsNil)
		c.Assert(k.HasSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
		collector, err := k.GetAffiliateCollector(ctx, few.Name)
		c.Assert(err, IsNil)
		amount := collector.RuneAmount
		c.Assert(amount.IsZero(), Equals, false)
		// Represent the real conversion's earnings in the pre-upgrade format:
		// the old handler credited the THORName's owner at execution, not admission.
		collector.RuneAmount = cosmos.ZeroUint()
		c.Assert(k.SetAffiliateCollector(ctx, collector), IsNil)
		key := seedOwnerCollector(c, ctx, mgr, few.Owner, amount.String())
		reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
		previousBalance := k.GetBalanceOf(ctx, previousOwner, common.RuneAsset()).Amount
		supply := k.GetTotalSupply(ctx, common.RuneAsset())
		before, err := mgr.storeService.OpenKVStore(ctx).Get(key)
		c.Assert(err, IsNil)
		beforeEvents := ctx.EventManager().Events()
		mgr.K = &collectorFailKeeper{Keeper: k, failTransfer: true}
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), ErrorMatches, ".*injected module transfer failure")
		after, err := mgr.storeService.OpenKVStore(ctx).Get(key)
		c.Assert(err, IsNil)
		c.Check(after, DeepEquals, before)
		c.Check(ctx.EventManager().Events(), DeepEquals, beforeEvents)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(amount), Equals, true)
		mgr.K = k
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Sub(reserve).Equal(amount), Equals, true)
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).IsZero(), Equals, true)
		c.Check(k.GetBalanceOf(ctx, previousOwner, common.RuneAsset()).Amount.Equal(previousBalance), Equals, true)
		c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(supply), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
		has, err := mgr.storeService.OpenKVStore(ctx).Has(key)
		c.Assert(err, IsNil)
		c.Check(has, Equals, false)
		events := ctx.EventManager().Events()
		var migrationEvents cosmos.Events
		for _, event := range events[len(beforeEvents):] {
			if event.Type == "affiliate_collector_migration" {
				migrationEvents = append(migrationEvents, event)
			}
		}
		c.Assert(migrationEvents, HasLen, 1)
		c.Check(migrationEvents[0], DeepEquals, cosmos.NewEvent("affiliate_collector_migration",
			cosmos.NewAttribute("owner", few.Owner.String()),
			cosmos.NewAttribute("rune_amount", amount.String()),
			cosmos.NewAttribute("destination", k.GetModuleAccAddress(ReserveName).String()),
		))
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
		c.Check(ctx.EventManager().Events(), DeepEquals, events)
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Sub(reserve).Equal(amount), Equals, true)
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationRejectsUnsafeState(c *C) {
	for _, scenario := range []string{"malformed-value", "malformed-key", "malformed-prefix-key", "nil-amount", "named-legacy-record", "malformed-thorname", "malformed-named-collector", "nil-named-amount"} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		owner := GetRandomBech32Addr()
		tn := NewTHORName("few", 1000, nil)
		tn.Owner = owner
		k.SetTHORName(ctx, tn)
		key := seedOwnerCollector(c, ctx, mgr, owner, "10")
		store := mgr.storeService.OpenKVStore(ctx)
		switch scenario {
		case "malformed-value":
			c.Assert(store.Set(key, []byte{0xff}), IsNil)
		case "malformed-key":
			c.Assert(store.Delete(key), IsNil)
			key = k.GetKey("affcol/", "invalid-owner")
			c.Assert(store.Set(key, []byte{}), IsNil)
		case "malformed-prefix-key":
			c.Assert(store.Delete(key), IsNil)
			key = []byte("affcol/invalid-owner")
			c.Assert(store.Set(key, []byte{}), IsNil)
		case "nil-amount":
			bz := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), owner)
			c.Assert(store.Set(key, bz), IsNil)
		case "named-legacy-record":
			bz := k.Cdc().MustMarshal(&AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.NewUint(10)})
			c.Assert(store.Set(key, bz), IsNil)
		case "malformed-thorname":
			c.Assert(store.Set(k.GetKey("thorname/", tn.Key()), []byte{0xff}), IsNil)
		case "malformed-named-collector":
			c.Assert(store.Set(k.GetKey("affcol_name/", tn.Name), []byte{0xff}), IsNil)
		case "nil-named-amount":
			bz := protowire.AppendString(protowire.AppendTag(nil, 3, protowire.BytesType), tn.Name)
			c.Assert(store.Set(k.GetKey("affcol_name/", tn.Name), bz), IsNil)
		}
		FundModule(c, ctx, k, AffiliateCollectorName, 15)
		before, err := store.Get(key)
		c.Assert(err, IsNil)
		balance := k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName)
		c.Assert(NewMigrator(mgr).Migrate18to19(ctx), NotNil, Commentf(scenario))
		after, err := store.Get(key)
		c.Assert(err, IsNil)
		c.Check(bytes.Equal(before, after), Equals, true, Commentf(scenario))
		c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(balance), Equals, true)
	}
}

func (s *MigrationCommonSuite) TestNamedCollectorGenesisValidation(c *C) {
	gs := DefaultGenesisState()
	gs.AffiliateCollectors = []AffiliateFeeCollector{{Name: "Few", RuneAmount: cosmos.NewUint(123)}}
	c.Assert(ValidateGenesis(gs), IsNil)
	gs.AffiliateCollectors = append(gs.AffiliateCollectors, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.ZeroUint()})
	c.Assert(ValidateGenesis(gs), ErrorMatches, ".*duplicate affiliate collector.*")
	gs.AffiliateCollectors = []AffiliateFeeCollector{{Name: "", RuneAmount: cosmos.ZeroUint()}}
	c.Assert(ValidateGenesis(gs), ErrorMatches, ".*invalid affiliate collector.*")
	gs.AffiliateCollectors = []AffiliateFeeCollector{{Name: "few"}}
	c.Assert(ValidateGenesis(gs), ErrorMatches, ".*invalid affiliate collector.*")
	// Compare actual collector keys, including Unicode case folding.
	gs.AffiliateCollectors = []AffiliateFeeCollector{{Name: "s", RuneAmount: cosmos.NewUint(1)}, {Name: "ſ", RuneAmount: cosmos.NewUint(2)}}
	c.Assert(ValidateGenesis(gs), ErrorMatches, ".*duplicate affiliate collector.*")
	gs.AffiliateCollectors = nil
	gs.THORNames = []THORName{{Name: ""}}
	c.Assert(ValidateGenesis(gs), ErrorMatches, "invalid THORName")
	gs.THORNames = []THORName{{Name: "thorswap.fi"}}
	c.Assert(ValidateGenesis(gs), ErrorMatches, "invalid THORName")
	gs.THORNames = []THORName{{Name: strings.Repeat("a", 31)}}
	c.Assert(ValidateGenesis(gs), ErrorMatches, "THORName cannot exceed 30 characters")
}

func (s *MigrationCommonSuite) TestNamedCollectorGenesisRoundTrip(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "FEW", RuneAmount: cosmos.NewUint(123)}), IsNil)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "tan", RuneAmount: cosmos.NewUint(456)}), IsNil)
	// Collector validation accepts historical keys independently of the
	// unchanged validation rules for THORName registration records.
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "THORSWAP.FI", RuneAmount: cosmos.NewUint(7)}), IsNil)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: strings.Repeat("a", 31), RuneAmount: cosmos.NewUint(8)}), IsNil)
	exported := ExportGenesis(ctx, k)
	bz, err := ModuleCdc.MarshalJSON(&exported)
	c.Assert(err, IsNil)
	var imported GenesisState
	c.Assert(ModuleCdc.UnmarshalJSON(bz, &imported), IsNil)
	c.Assert(ValidateGenesis(imported), IsNil)
	ctx, mgr = setupManagerForTest(c)
	InitGenesis(ctx, mgr.Keeper(), imported)
	c.Check(ExportGenesis(ctx, mgr.Keeper()).AffiliateCollectors, DeepEquals, exported.AffiliateCollectors)

	// Old genesis JSON cannot be imported as collectors with empty THORNames. The
	// supported upgrade path migrates the existing KV store, not genesis JSON.
	oldJSON := []byte(`{"affiliate_collectors":[{"owner_address":"` + GetRandomBech32Addr().String() + `","rune_amount":"123"}]}`)
	var old GenesisState
	if err := ModuleCdc.UnmarshalJSON(oldJSON, &old); err == nil {
		c.Assert(ValidateGenesis(old), ErrorMatches, ".*invalid affiliate collector.*")
	}
}

func (s *MigrationCommonSuite) TestNamedCollectorMigrationPendingConversions(c *C) {
	for _, singleName := range []bool{false, true} {
		for _, version := range []types.SwapVersion{types.SwapVersion_v1, types.SwapVersion_v2} {
			ctx, mgr, few := setupCollectorSwapExecution(c)
			k := mgr.Keeper()
			if singleName {
				c.Assert(k.DeleteTHORName(ctx, "tan"), IsNil)
			}
			msg := collectorConversionForTest(ctx, mgr, few, version)
			// The available legacy balance migrates separately from this pending swap.
			legacyAmount := cosmos.NewUint(2 * common.One)
			key := seedOwnerCollector(c, ctx, mgr, few.Owner, legacyAmount.String())
			FundModule(c, ctx, k, AffiliateCollectorName, legacyAmount.Uint64())
			if msg.IsV2() {
				c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)
				c.Assert(k.SetAdvSwapQueueIndex(ctx, *msg), IsNil)
			} else {
				c.Assert(k.SetSwapQueueItem(ctx, *msg, int(msg.Index)), IsNil)
			}
			store := mgr.storeService.OpenKVStore(ctx)
			ownerBalance := k.GetBalanceOf(ctx, few.Owner, common.RuneAsset()).Amount
			c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
			migrated := cosmos.ZeroUint()
			if singleName {
				migrated = legacyAmount
			}
			c.Check(k.GetBalanceOf(ctx, few.Owner, common.RuneAsset()).Amount.Sub(ownerBalance).String(), Equals, legacyAmount.Sub(migrated).String())
			c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(migrated), Equals, true)
			assertNamedAffiliateBalance(c, ctx, k, few.Name, migrated.Uint64())
			has, err := store.Has(key)
			c.Assert(err, IsNil)
			c.Check(has, Equals, false)
			assertAffiliateIntegrationInvariant(c, ctx, k)

			// Neither migration nor a rerun modifies the conversion or its queue index.
			events := ctx.EventManager().Events()
			c.Assert(NewMigrator(mgr).Migrate18to19(ctx), IsNil)
			c.Check(ctx.EventManager().Events(), DeepEquals, events)
			if msg.IsV2() {
				queued, queueErr := k.GetAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index))
				c.Assert(queueErr, IsNil)
				c.Check(queued, DeepEquals, *msg)
				indexed, indexErr := k.HasAdvSwapQueueIndex(ctx, *msg)
				c.Assert(indexErr, IsNil)
				c.Check(indexed, Equals, true)
				c.Assert(newSwapQueueAdv(k).EndBlock(ctx, mgr, false), IsNil)
				c.Check(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
				indexed, indexErr = k.HasAdvSwapQueueIndex(ctx, *msg)
				c.Assert(indexErr, IsNil)
				c.Check(indexed, Equals, false)
			} else {
				queued, queueErr := k.GetSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index))
				c.Assert(queueErr, IsNil)
				c.Check(queued, DeepEquals, *msg)
				c.Assert(newSwapQueue(k).EndBlock(ctx, mgr), IsNil)
				c.Check(k.HasSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
			}
			// Its new earnings are credited on delivery to FEW alone, after migration.
			collector, err := k.GetAffiliateCollector(ctx, few.Name)
			c.Assert(err, IsNil)
			c.Check(collector.RuneAmount.GT(migrated), Equals, true)
			c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(collector.RuneAmount), Equals, true)
			assertNamedAffiliateBalance(c, ctx, k, "tan", 0)
			assertAffiliateIntegrationInvariant(c, ctx, k)
		}
	}
}

func (s *MigrationCommonSuite) TestMigrate19to20MergesZECTexClout(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()
	transparent, tex, _, _ := zecTxOutAddresses()
	other := GetRandomBTCAddress()

	existing := NewSwapperClout(transparent)
	existing.Score = cosmos.NewUint(100)
	existing.Spent = cosmos.NewUint(40)
	existing.Reclaimed = cosmos.NewUint(10)
	existing.LastSpentHeight = 7 // stale, so its outstanding spend is restored
	existing.LastReclaimHeight = 9
	c.Assert(k.SetSwapperClout(ctx, existing), IsNil)
	otherClout := NewSwapperClout(other)
	otherClout.Score = cosmos.NewUint(5)
	c.Assert(k.SetSwapperClout(ctx, otherClout), IsNil)

	// Seed a legacy record directly, since the keeper now normalizes TEX.
	legacy := NewSwapperClout(tex)
	legacy.Score = cosmos.NewUint(1000)
	legacy.Spent = cosmos.NewUint(300)
	legacy.Reclaimed = cosmos.NewUint(100)
	legacy.LastSpentHeight = 900
	legacy.LastReclaimHeight = 3
	texKey := k.GetKey("sclout/", tex.String())
	c.Assert(mgr.storeService.OpenKVStore(ctx).Set(texKey, k.Cdc().MustMarshal(&legacy)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate19to20(ctx), IsNil)

	has, err := mgr.storeService.OpenKVStore(ctx).Has(texKey)
	c.Assert(err, IsNil)
	c.Check(has, Equals, false)
	merged, err := k.GetSwapperClout(ctx, transparent)
	c.Assert(err, IsNil)
	c.Check(merged.Address, Equals, transparent)
	c.Check(merged.Score.String(), Equals, "1100")
	c.Check(merged.Spent.String(), Equals, "340")
	c.Check(merged.Reclaimed.String(), Equals, "140")
	c.Check(merged.LastSpentHeight, Equals, int64(900))
	c.Check(merged.LastReclaimHeight, Equals, int64(9))
	c.Check(merged.Available().String(), Equals, "900")
	otherAfter, err := k.GetSwapperClout(ctx, other)
	c.Assert(err, IsNil)
	c.Check(otherAfter, DeepEquals, otherClout)
}
