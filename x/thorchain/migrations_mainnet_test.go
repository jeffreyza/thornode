//go:build mainnet
// +build mainnet

package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// TestMigrate16to17DanglingSettlementSpecs mirrors the migration's hard-coded
// mainnet rows in synthetic state so reviewers can see the full migration flow
// exercised for every settlement spec we intend to resolve on-chain.
func (s *MigrationCommonSuite) TestMigrate16to17DanglingSettlementSpecs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	specs, err := mainnetDanglingSettlementSpecs16to17()
	c.Assert(err, IsNil)
	c.Assert(specs, HasLen, 26)

	saveMigrationNetworkFee(c, ctx, mgr, common.ETHChain)
	saveMigrationNetworkFee(c, ctx, mgr, common.GAIAChain)
	saveMigrationNetworkFee(c, ctx, mgr, common.BTCChain)
	saveMigrationNetworkFee(c, ctx, mgr, common.LTCChain)

	ethUSDT := mustMigrationAsset(c, "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	ethWBTC := mustMigrationAsset(c, "ETH.WBTC-0X2260FAC5E5542A773AA44FBCFEDF7C193BC2C599")

	activeVault := GetRandomVault()
	activeVault.PubKeyFrost = GetRandomEd25519PubKey()
	activeVault.Chains = common.Chains{common.ETHChain, common.GAIAChain, common.BTCChain, common.LTCChain}.Strings()
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1_000*common.One)),
		common.NewCoin(ethUSDT, cosmos.NewUint(10_000_000*common.One)),
		common.NewCoin(ethUSDC, cosmos.NewUint(10_000_000*common.One)),
		common.NewCoin(ethWBTC, cosmos.NewUint(10*common.One)),
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(1_000_000*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000*common.One)),
		common.NewCoin(common.LTCAsset, cosmos.NewUint(1_000_000*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	originalActions := make(map[string]TxOutItem, len(specs))
	for i, spec := range specs {
		oldVault := GetRandomVault()
		oldVault.Status = RetiringVault
		oldVault.PubKeyFrost = GetRandomEd25519PubKey()
		oldVault.Chains = common.Chains{spec.Chain}.Strings()
		c.Assert(mgr.Keeper().SetVault(ctx, oldVault), IsNil)

		action := TxOutItem{
			Chain:            spec.Chain,
			InHash:           spec.TxID,
			ToAddress:        migrationTestAddressForChain(c, spec.Chain),
			VaultPubKey:      oldVault.PubKey,
			VaultPubKeyEddsa: txOutVaultPubKeyEddsa(spec.Chain, oldVault.PubKeyEddsa),
			VaultPubKeyFrost: oldVault.PubKeyFrost,
			Coin:             common.NewCoin(spec.Asset, spec.Amount),
			MaxGas:           common.Gas{common.NewCoin(spec.Chain.GetGasAsset(), cosmos.OneUint())},
			GasRate:          1,
			Memo:             fmt.Sprintf("MIGRATION-TEST:%02d", i),
		}
		voter := NewObservedTxVoter(spec.TxID, nil)
		voter.Actions = []TxOutItem{action}
		voter.FinalisedHeight = 1
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		originalActions[spec.TxID.String()] = action
	}

	err = NewMigrator(mgr).Migrate16to17(ctx)
	c.Assert(err, IsNil)

	outboundHeight := ctx.BlockHeight() + 300
	txOut, err := mgr.Keeper().GetTxOut(ctx, outboundHeight)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, len(specs))

	queued := make(map[string]TxOutItem, len(txOut.TxArray))
	for _, item := range txOut.TxArray {
		queued[item.InHash.String()] = item
	}

	for _, spec := range specs {
		key := spec.TxID.String()
		original := originalActions[key]
		item, ok := queued[key]
		c.Assert(ok, Equals, true, Commentf("missing queued item for %s", key))

		c.Assert(item.Chain.Equals(spec.Chain), Equals, true, Commentf(key))
		c.Assert(item.ToAddress.Equals(original.ToAddress), Equals, true, Commentf(key))
		c.Assert(item.Coin.Equals(original.Coin), Equals, true, Commentf(key))
		c.Assert(item.Memo, Equals, original.Memo, Commentf(key))
		c.Assert(item.VaultPubKey.Equals(activeVault.PubKey), Equals, true, Commentf(key))
		c.Assert(item.VaultPubKeyEddsa.Equals(txOutVaultPubKeyEddsa(spec.Chain, activeVault.PubKeyEddsa)), Equals, true, Commentf(key))
		c.Assert(item.VaultPubKeyFrost.Equals(activeVault.PubKeyFrost), Equals, true, Commentf(key))
		c.Assert(item.GasRate, Equals, int64(mgr.GasMgr().GetGasRate(ctx, spec.Chain).Uint64()), Commentf(key))

		maxGas, err := mgr.GasMgr().GetMaxGas(ctx, spec.Chain)
		c.Assert(err, IsNil, Commentf(key))
		c.Assert(item.MaxGas.Equals(common.Gas{maxGas}), Equals, true, Commentf(key))

		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, spec.TxID)
		c.Assert(err, IsNil, Commentf(key))
		c.Assert(voter.FinalisedHeight, Equals, ctx.BlockHeight(), Commentf(key))
		c.Assert(voter.Actions, HasLen, 1, Commentf(key))
		c.Assert(voter.Actions[0].VaultPubKey.Equals(item.VaultPubKey), Equals, true, Commentf(key))
		c.Assert(voter.Actions[0].VaultPubKeyEddsa.Equals(item.VaultPubKeyEddsa), Equals, true, Commentf(key))
		c.Assert(voter.Actions[0].VaultPubKeyFrost.Equals(item.VaultPubKeyFrost), Equals, true, Commentf(key))
		c.Assert(voter.Actions[0].MaxGas.Equals(item.MaxGas), Equals, true, Commentf(key))
		c.Assert(voter.Actions[0].GasRate, Equals, item.GasRate, Commentf(key))
	}
}

// adr028SeededPoolKeys are the four pools re-seeded by Migrate17to18 rather than
// written down to live backing.
func adr028SeededPoolKeys() map[string]bool {
	return map[string]bool{
		"BASE.ETH": true,
		"BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D": true,
		"BSC.USDT-0X55D398326F99059FF775485246999027B3197955": true,
		"ETH.THOR-0XA5F2211B9B8170F694421F2046281775E8468044": true,
	}
}

// TestMigrate17to18WritedownSpecs guards the hard-coded ADR-028 phantom-depth
// writedown table: it must parse, carry no duplicates, exclude THOR-native pools
// (backed by bank supply, not vault custody), carry only positive targets, and
// exclude the four re-seeded pools. Entries are DELTAS, not absolute targets.
func (s *MigrationCommonSuite) TestMigrate17to18WritedownSpecs(c *C) {
	specs, err := mainnetADR028WritedownSpecs17to18()
	c.Assert(err, IsNil)
	c.Assert(specs, HasLen, 26)

	seeded := adr028SeededPoolKeys()
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		key := spec.asset.String()
		c.Assert(seen[key], Equals, false, Commentf("duplicate %s", key))
		seen[key] = true
		c.Assert(spec.asset.Chain.Equals(common.THORChain), Equals, false, Commentf(key))
		c.Assert(seeded[key], Equals, false, Commentf("%s is re-seeded, must not be written down", key))
		c.Assert(spec.deltaAsset.IsZero(), Equals, false, Commentf("%s delta must be > 0", key))
	}
}

// TestMigrate17to18SeedSpecs guards the $5k-per-side seed table: exactly the four
// re-seeded pools, no duplicates, a uniform $5k RUNE seed, and a non-zero asset
// seed for each.
func (s *MigrationCommonSuite) TestMigrate17to18SeedSpecs(c *C) {
	specs, err := mainnetADR028SeedSpecs17to18()
	c.Assert(err, IsNil)
	c.Assert(specs, HasLen, 4)

	want := adr028SeededPoolKeys()
	for _, spec := range specs {
		key := spec.asset.String()
		c.Assert(want[key], Equals, true, Commentf("unexpected seeded pool %s", key))
		delete(want, key)
		c.Assert(spec.seedRune.String(), Equals, cosmos.NewUint(1232884481190).String(), Commentf(key))
		c.Assert(spec.seedAsset.IsZero(), Equals, false, Commentf("%s asset seed must be > 0", key))
	}
	c.Assert(want, HasLen, 0, Commentf("seeded pools missing from specs: %v", want))
}

// TestMigrate17to18SeedFreedRuneToReserveNoBurn pins that the RUNE displaced by
// re-seeding a pool is moved to the Reserve in FULL — none of it is burned. It
// fails against the old splitFreedRune behaviour, which routes only half to the
// Reserve and burns the other half.
func (s *MigrationCommonSuite) TestMigrate17to18SeedFreedRuneToReserveNoBurn(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	bscUSDC := mustMigrationAsset(c, "BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")
	seedRune := cosmos.NewUint(1232884481190)

	// Pool RUNE side well above the seed, so the excess is freed to the Reserve.
	oldRune := cosmos.NewUint(500_000 * common.One)
	pool := NewPool()
	pool.Asset = bscUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100_000 * common.One)
	pool.BalanceRune = oldRune
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Asgard must physically hold the pool RUNE for the Asgard→Reserve move.
	fund := common.NewCoin(common.RuneNative, oldRune)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	got, err := mgr.Keeper().GetPool(ctx, bscUSDC)
	c.Assert(err, IsNil)
	c.Assert(got.BalanceRune.String(), Equals, seedRune.String())

	freed := common.SafeSub(oldRune, seedRune)
	gained := common.SafeSub(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName), reserveBefore)
	// Migrate17to18 also sends the fixed Secured-Ops coverage out of the Reserve,
	// so the net Reserve gain is the full freed RUNE minus that coverage.
	// splitFreedRune would route only half the freed amount in, so this still
	// bites: gained would be freed/2 − coverage, not freed − coverage.
	netExpected := common.SafeSub(freed, cosmos.NewUint(adr028SecuredOpsCoverageRune))
	c.Assert(gained.String(), Equals, netExpected.String(),
		Commentf("Reserve net %s, expected full freed − coverage %s (half-burn is what we are avoiding)", gained, netExpected))
}

// TestMigrate17to18WritedownFreedRuneToReserveNoBurn pins that the ΔR freed by
// WRITING DOWN a phantom-depth pool is moved to the Reserve in FULL — none burned
// (all cut RUNE goes to Reserve). Mirrors the seed test but for the writedown
// path; it fails against the old splitFreedRune behaviour, which routes only half
// to the Reserve and burns the other half.
func (s *MigrationCommonSuite) TestMigrate17to18WritedownFreedRuneToReserveNoBurn(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	// Only ETH.USDC exists, so the sole writedown ΔR freed is this pool's; every
	// other writedown/seed pool is absent and skipped.
	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	ethDelta := cosmos.NewUint(106612841513705)
	ethSnapshotBA := cosmos.NewUint(281279011777800)

	pool := NewPool()
	pool.Asset = ethUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = ethSnapshotBA
	pool.BalanceRune = cosmos.NewUint(2_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	deltaR := common.GetUncappedShare(ethDelta, pool.BalanceAsset, pool.BalanceRune)

	// Asgard must physically hold the freed ΔR for the Asgard→Reserve move.
	fund := common.NewCoin(common.RuneNative, deltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	reserveBefore := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// Migrate17to18 also sends the fixed Secured-Ops coverage out of the Reserve,
	// so the net Reserve gain is the full ΔR minus that coverage. splitFreedRune
	// would route only half the freed amount in, so this bites: gained would be
	// ΔR/2 − coverage, not ΔR − coverage.
	gained := common.SafeSub(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName), reserveBefore)
	netExpected := common.SafeSub(deltaR, cosmos.NewUint(adr028SecuredOpsCoverageRune))
	c.Assert(gained.String(), Equals, netExpected.String(),
		Commentf("Reserve net %s, expected full ΔR − coverage %s (half-burn is what we are avoiding)", gained, netExpected))
}

// TestMigrate17to18SecuredOpsCoverageFromReserve pins the coverage settlement:
// the migration sends exactly the fixed coverage RUNE from the Reserve to the TC
// Secured-Ops account.
func (s *MigrationCommonSuite) TestMigrate17to18SecuredOpsCoverageFromReserve(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	to, err := cosmos.AccAddressFromBech32(adr028SecuredOpsAddr)
	c.Assert(err, IsNil)
	runeDenom := common.RuneNative.Native()
	before := mgr.Keeper().GetBalance(ctx, to).AmountOf(runeDenom)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	after := mgr.Keeper().GetBalance(ctx, to).AmountOf(runeDenom)
	gained := after.Sub(before)
	c.Assert(gained.String(), Equals, cosmos.NewUint(adr028SecuredOpsCoverageRune).String(),
		Commentf("Secured-Ops account gained %s, expected the coverage %d", gained, uint64(adr028SecuredOpsCoverageRune)))
}

// TestMigrate17to18PreflightAbortsOnReserveShortfall pins the pre-flight guard:
// when the Reserve cannot fund the Secured-Ops coverage, the migration aborts
// BEFORE mutating any pool — turning what would otherwise be a mid-loop send
// failure (RunMigrations error → upgrade panic → chain halt) into a clean,
// diagnosable abort.
func (s *MigrationCommonSuite) TestMigrate17to18PreflightAbortsOnReserveShortfall(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	// Set up a writedown pool so a real mutation would follow if we let it.
	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	snapshotBA := cosmos.NewUint(281279011777800)
	pool := NewPool()
	pool.Asset = ethUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = snapshotBA
	pool.BalanceRune = cosmos.NewUint(2_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Drain the Reserve below the fixed Secured-Ops coverage.
	if rbal := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName); !rbal.IsZero() {
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ReserveName, ModuleName,
			common.NewCoins(common.NewCoin(common.RuneNative, rbal))), IsNil)
	}

	err := NewMigrator(mgr).Migrate17to18(ctx)
	c.Assert(err, ErrorMatches, ".*Reserve RUNE.*aborting before any mutation.*")

	// Nothing was mutated: the pool is untouched.
	got, gerr := mgr.Keeper().GetPool(ctx, ethUSDC)
	c.Assert(gerr, IsNil)
	c.Assert(got.BalanceAsset.String(), Equals, snapshotBA.String())
}

// TestMigrate17to18PreflightAbortsOnAsgardShortfall verifies the Asgard-side
// preflight guard: when Asgard cannot fund ΣΔR + seedFreed, the migration aborts
// BEFORE any state mutation. Complements TestMigrate17to18PreflightAbortsOnReserveShortfall.
func (s *MigrationCommonSuite) TestMigrate17to18PreflightAbortsOnAsgardShortfall(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	snapshotBA := cosmos.NewUint(281279011777800)
	pool := NewPool()
	pool.Asset = ethUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = snapshotBA
	pool.BalanceRune = cosmos.NewUint(2_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Drain Asgard so it cannot fund the ΔR send.
	if abal := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName); !abal.IsZero() {
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ModuleName,
			common.NewCoins(common.NewCoin(common.RuneNative, abal))), IsNil)
	}

	err := NewMigrator(mgr).Migrate17to18(ctx)
	c.Assert(err, ErrorMatches, ".*Asgard RUNE.*aborting before any mutation.*")

	// Nothing was mutated: the pool is untouched.
	got, gerr := mgr.Keeper().GetPool(ctx, ethUSDC)
	c.Assert(gerr, IsNil)
	c.Assert(got.BalanceAsset.String(), Equals, snapshotBA.String())
}

// TestMigrate17to18POLReSolveWithNonzeroPoolUnits exercises the POL re-solve
// branch that adjusts ReserveUnits when PoolUnits > 0. All other migration tests
// run with PoolUnits == 0 (the default), so this is the only test that reaches
// the re-solve's default branch (the one pratikasr flagged as untested).
func (s *MigrationCommonSuite) TestMigrate17to18POLReSolveWithNonzeroPoolUnits(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	// Set up a writedown pool with a Reserve LP, so polPoolValue > 0 and the
	// re-solve has something to adjust.
	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	pool := NewPool()
	pool.Asset = ethUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(281279011777800)
	pool.BalanceRune = cosmos.NewUint(2_000_000 * common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Give the Reserve module a real LP position in this pool.
	polAddr, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	polLP := LiquidityProvider{
		Asset:       ethUSDC,
		RuneAddress: polAddr,
		Units:       cosmos.NewUint(100_000 * common.One),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, polLP)

	// Set RUNEPool with nonzero PoolUnits + large ReserveUnits. The re-solve's
	// default branch fires only when providerValuePre < vPost, i.e. when the
	// external provider share (PoolUnits / TotalUnits) is small enough that the
	// writedown doesn't exhaust it.
	rp := RUNEPool{
		PoolUnits:    cosmos.NewUint(50_000 * common.One),
		ReserveUnits: cosmos.NewUint(950_000 * common.One),
	}
	mgr.Keeper().SetRUNEPool(ctx, rp)

	// Fund Asgard to cover the ΔR send.
	ethDelta := cosmos.NewUint(106612841513705)
	deltaR := common.GetUncappedShare(ethDelta, pool.BalanceAsset, pool.BalanceRune)
	fund := common.NewCoin(common.RuneNative, deltaR.MulUint64(2))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// The re-solve must have adjusted ReserveUnits (not left it at 200k or zeroed it).
	rpPost, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(rpPost.PoolUnits.String(), Equals, rp.PoolUnits.String(),
		Commentf("PoolUnits must be unchanged"))
	c.Assert(rpPost.ReserveUnits.GT(cosmos.ZeroUint()), Equals, true,
		Commentf("ReserveUnits should be nonzero after a writedown that shrinks pool value"))
	c.Assert(rpPost.ReserveUnits.String() != rp.ReserveUnits.String(), Equals, true,
		Commentf("ReserveUnits should have been re-solved, not left at the original value"))
}

// TestMigrate17to18POLReSolveSkipsWhenLossExceedsProviders verifies that when
// PoolUnits > 0 and providerValuePre > vPost (i.e. the writedown loss exceeds
// the total pool value left for providers), the re-solve is skipped (not an
// abort) — ReserveUnits is left unchanged, and the migration succeeds.
func (s *MigrationCommonSuite) TestMigrate17to18POLReSolveSkipsWhenLossExceedsProviders(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	pool := NewPool()
	pool.Asset = ethUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(281279011777800)
	pool.BalanceRune = cosmos.NewUint(2_000_000 * common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	polAddr, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	polLP := LiquidityProvider{
		Asset:       ethUSDC,
		RuneAddress: polAddr,
		Units:       cosmos.NewUint(100_000 * common.One),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, polLP)

	// Large PoolUnits relative to ReserveUnits → providerValuePre > vPost after
	// the writedown → the re-solve cannot hold providers whole → skips.
	rp := RUNEPool{
		PoolUnits:    cosmos.NewUint(500_000 * common.One),
		ReserveUnits: cosmos.NewUint(200_000 * common.One),
	}
	mgr.Keeper().SetRUNEPool(ctx, rp)

	ethDelta := cosmos.NewUint(106612841513705)
	deltaR := common.GetUncappedShare(ethDelta, pool.BalanceAsset, pool.BalanceRune)
	fund := common.NewCoin(common.RuneNative, deltaR.MulUint64(2))
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	// Migration succeeds — re-solve is skipped, not aborted.
	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// ReserveUnits left unchanged (skip, not zeroed).
	rpPost, err := mgr.Keeper().GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(rpPost.ReserveUnits.String(), Equals, rp.ReserveUnits.String(),
		Commentf("ReserveUnits should be unchanged when re-solve is skipped"))
}

// TestMigrate17to18WritedownTracksLiveDepth pins the delta semantics: the
// migration removes the phantom slice from whatever depth the pool holds at
// EXECUTION, so growth accrued since the snapshot survives the writedown.
//
// This test fails against an absolute-target implementation, which would cut the
// pool back to the frozen snapshot target and destroy the drift along with it.
func (s *MigrationCommonSuite) TestMigrate17to18WritedownTracksLiveDepth(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	ethDelta := cosmos.NewUint(106612841513705)
	snapshotBA := cosmos.NewUint(281279011777800)

	// The pool has traded up since the snapshot was taken.
	drift := cosmos.NewUint(100_000 * common.One)
	liveBA := snapshotBA.Add(drift)
	runeSide := cosmos.NewUint(2_000_000 * common.One)

	pool := NewPool()
	pool.Asset = ethUSDC
	pool.Status = PoolAvailable
	pool.BalanceAsset = liveBA
	pool.BalanceRune = runeSide
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	deltaR := common.GetUncappedShare(ethDelta, liveBA, runeSide)
	freed := common.NewCoin(common.RuneNative, deltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, freed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(freed)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	got, err := mgr.Keeper().GetPool(ctx, ethUSDC)
	c.Assert(err, IsNil)

	// Exactly the phantom slice is removed, no more: the drift survives.
	c.Assert(got.BalanceAsset.String(), Equals, common.SafeSub(liveBA, ethDelta).String())
	c.Assert(common.SafeSub(liveBA, got.BalanceAsset).String(), Equals, ethDelta.String())

	// An absolute-target implementation lands here instead, eating the drift.
	frozenTarget := common.SafeSub(snapshotBA, ethDelta)
	c.Assert(got.BalanceAsset.String(), Not(Equals), frozenTarget.String(),
		Commentf("pool cut to the frozen snapshot target; %s of live depth destroyed", drift))
}

// TestMigrate17to18AppliesWritedown exercises both paths: a written-down pool has
// its phantom slice removed with a symmetric RUNE reduction, a re-seeded pool is
// set to its $5k asset seed with the RUNE shortfall drawn Reserve→Asgard, and a
// pool in neither table is untouched.
func (s *MigrationCommonSuite) TestMigrate17to18AppliesWritedown(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	bscUSDC := mustMigrationAsset(c, "BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")

	// ETH.USDC phantom slice from the hard-coded table (a DELTA, not a target),
	// and the pool sitting at exactly its snapshot depth.
	ethDelta := cosmos.NewUint(106612841513705)
	ethSnapshotBA := cosmos.NewUint(281279011777800)
	ethTarget := common.SafeSub(ethSnapshotBA, ethDelta)

	ethRune := cosmos.NewUint(2_000_000 * common.One)
	ethPool := NewPool()
	ethPool.Asset = ethUSDC
	ethPool.Status = PoolAvailable
	ethPool.BalanceAsset = ethSnapshotBA
	ethPool.BalanceRune = ethRune
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)
	ethDeltaR := common.GetUncappedShare(ethDelta, ethPool.BalanceAsset, ethPool.BalanceRune)

	// Re-seeded pool: both sides are set to the $5k seed. This RUNE side sits below
	// the seed, so the shortfall is drawn from the Reserve.
	bscPool := NewPool()
	bscPool.Asset = bscUSDC
	bscPool.Status = PoolAvailable
	bscPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	bscPool.BalanceRune = cosmos.NewUint(80 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, bscPool), IsNil)

	bscSeedAsset := cosmos.NewUint(500000000000)
	bscSeedRune := cosmos.NewUint(1232884481190)
	bscReserveDraw := common.SafeSub(bscSeedRune, bscPool.BalanceRune)

	// Pool absent from the table must be left untouched.
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(500 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// splitFreedRune moves the freed RUNE out of Asgard; fund it to cover ΔR, and
	// fund the Reserve for the seed draw. Only the thorchain module may mint, so
	// mint there then send on.
	freed := common.NewCoin(common.RuneNative, ethDeltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, freed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(freed)), IsNil)
	draw := common.NewCoin(common.RuneNative, bscReserveDraw)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, draw), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(draw)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	gotEth, err := mgr.Keeper().GetPool(ctx, ethUSDC)
	c.Assert(err, IsNil)
	c.Assert(gotEth.BalanceAsset.String(), Equals, ethTarget.String())
	c.Assert(gotEth.BalanceRune.String(), Equals, common.SafeSub(ethRune, ethDeltaR).String())

	gotBsc, err := mgr.Keeper().GetPool(ctx, bscUSDC)
	c.Assert(err, IsNil)
	c.Assert(gotBsc.BalanceAsset.String(), Equals, bscSeedAsset.String())
	c.Assert(gotBsc.BalanceRune.String(), Equals, bscSeedRune.String())

	gotBtc, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(gotBtc.BalanceAsset.String(), Equals, cosmos.NewUint(10*common.One).String())
	c.Assert(gotBtc.BalanceRune.String(), Equals, cosmos.NewUint(500*common.One).String())
}

// TestMigrate17to18SaversHaircutUnderbacked exercises the UNDERBACKED path:
// a writedown pool whose Asgard vault holds free backing > 0 gets the step-3
// savers haircut instead of BurnEntireSynthSupply. Savers are settled at the
// real free backing; the excess synth (saver portion + non-saver residuals) is
// burned from Asgard.
func (s *MigrationCommonSuite) TestMigrate17to18SaversHaircutUnderbacked(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	// Use ETH.USDC — the only pool in the haircut set.
	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	synthUSDC, err := common.NewAsset("ETH/USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	specs, err := mainnetADR028WritedownSpecs17to18()
	c.Assert(err, IsNil)
	var usdcDelta cosmos.Uint
	for _, sp := range specs {
		if sp.asset.Equals(ethUSDC) {
			usdcDelta = sp.deltaAsset
			break
		}
	}
	c.Assert(usdcDelta.IsZero(), Equals, false)

	poolBA := usdcDelta.Add(cosmos.NewUint(500 * common.One))
	poolBR := cosmos.NewUint(5_000_000 * common.One)
	target := common.SafeSub(poolBA, usdcDelta)

	usdcPool := NewPool()
	usdcPool.Asset = ethUSDC
	usdcPool.Status = PoolAvailable
	usdcPool.BalanceAsset = poolBA
	usdcPool.BalanceRune = poolBR
	usdcPool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, usdcPool), IsNil)

	tradeDepth := cosmos.NewUint(50 * common.One)
	securedDepth := cosmos.NewUint(30 * common.One)
	mgr.Keeper().SetTradeUnit(ctx, TradeUnit{Asset: ethUSDC.GetTradeAsset(), Units: cosmos.ZeroUint(), Depth: tradeDepth})
	mgr.Keeper().SetSecuredAsset(ctx, SecuredAsset{Asset: ethUSDC.GetSecuredAsset(), Depth: securedDepth})

	freeBacking := cosmos.NewUint(200 * common.One)
	vaultCoins := target.Add(tradeDepth).Add(securedDepth).Add(freeBacking)
	activeVault := GetRandomVault()
	activeVault.Chains = common.Chains{common.ETHChain}.Strings()
	activeVault.Coins = common.Coins{common.NewCoin(ethUSDC, vaultCoins)}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	saversDepth := cosmos.NewUint(800 * common.One)
	reserveSynth := cosmos.NewUint(300 * common.One)
	totalSynth := saversDepth.Add(reserveSynth)
	c.Assert(target.MulUint64(2).LTE(totalSynth), Equals, true,
		Commentf("test setup: clamp must fire; 2×target=%s synth=%s", target.MulUint64(2), totalSynth))

	// Mint synth coins: saversDepth to Asgard, reserveSynth to Reserve.
	synthCoin := common.NewCoin(synthUSDC, saversDepth)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoin)), IsNil)
	resCoin := common.NewCoin(synthUSDC, reserveSynth)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, resCoin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(resCoin)), IsNil)

	// Saver pool (ETH/USDC) with two saver LPs.
	saverPool := NewPool()
	saverPool.Asset = synthUSDC
	saverPool.Status = PoolAvailable
	saverPool.BalanceAsset = saversDepth
	saverPool.LPUnits = cosmos.NewUint(800 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, saverPool), IsNil)

	saver1Addr := GetRandomTHORAddress()
	saver2Addr := GetRandomTHORAddress()
	lp1 := LiquidityProvider{
		Asset: synthUSDC, RuneAddress: saver1Addr,
		Units:       cosmos.NewUint(500 * common.One),
		PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint(),
	}
	lp2 := LiquidityProvider{
		Asset: synthUSDC, RuneAddress: saver2Addr,
		Units:       cosmos.NewUint(300 * common.One),
		PendingRune: cosmos.ZeroUint(), PendingAsset: cosmos.ZeroUint(),
		RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint(),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp1)
	mgr.Keeper().SetLiquidityProvider(ctx, lp2)

	// Fund Asgard RUNE for the freed-RUNE transfer.
	deltaR := common.GetUncappedShare(usdcDelta, poolBA, poolBR)
	freed := common.NewCoin(common.RuneNative, deltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, freed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(freed)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// Post-migration checks.
	remainingSynth := mgr.Keeper().GetTotalSupply(ctx, synthUSDC)
	c.Assert(remainingSynth.String(), Equals, freeBacking.String(),
		Commentf("remaining synth should equal freeBacking"))

	gotSaverPool, err := mgr.Keeper().GetPool(ctx, synthUSDC)
	c.Assert(err, IsNil)
	c.Assert(gotSaverPool.BalanceAsset.String(), Equals, freeBacking.String())

	// saverBurnAmt = 800 - 200 = 600; lp1: 500-375=125, lp2: 300-225=75.
	gotLP1, err := mgr.Keeper().GetLiquidityProvider(ctx, synthUSDC, saver1Addr)
	c.Assert(err, IsNil)
	c.Assert(gotLP1.Units.String(), Equals, cosmos.NewUint(125*common.One).String())

	gotLP2, err := mgr.Keeper().GetLiquidityProvider(ctx, synthUSDC, saver2Addr)
	c.Assert(err, IsNil)
	c.Assert(gotLP2.Units.String(), Equals, cosmos.NewUint(75*common.One).String())

	c.Assert(gotSaverPool.LPUnits.String(), Equals, cosmos.NewUint(200*common.One).String())

	gotUSDCPool, err := mgr.Keeper().GetPool(ctx, ethUSDC)
	c.Assert(err, IsNil)
	c.Assert(gotUSDCPool.BalanceAsset.MulUint64(2).GT(remainingSynth), Equals, true,
		Commentf("pool should be out of clamp after haircut"))

	// F1 invariant: Asgard synth balance ≥ saverPool.BalanceAsset.
	asgardSynthPost := mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, synthUSDC.Native())
	c.Assert(asgardSynthPost.GTE(gotSaverPool.BalanceAsset), Equals, true,
		Commentf("Asgard must back the saver pool; got %s < %s", asgardSynthPost, gotSaverPool.BalanceAsset))

	// Reserve synths fully burned.
	reserveSynthPost := mgr.Keeper().GetBalanceOfModule(ctx, ReserveName, synthUSDC.Native())
	c.Assert(reserveSynthPost.IsZero(), Equals, true,
		Commentf("Reserve synths should be burned; got %s", reserveSynthPost))
}

// TestMigrate17to18SafeToClampWithUserSynths verifies that the migration
// completes (does not abort) when user-held synths surviving the haircut keep
// the pool clamped after writedown. Previously this returned an error, which
// would deterministically halt the chain at the upgrade height.
func (s *MigrationCommonSuite) TestMigrate17to18SafeToClampWithUserSynths(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	ethUSDC := mustMigrationAsset(c, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	synthUSDC, err := common.NewAsset("ETH/USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	specs, err := mainnetADR028WritedownSpecs17to18()
	c.Assert(err, IsNil)
	var usdcDelta cosmos.Uint
	for _, sp := range specs {
		if sp.asset.Equals(ethUSDC) {
			usdcDelta = sp.deltaAsset
			break
		}
	}
	c.Assert(usdcDelta.IsZero(), Equals, false)

	// Pool state: BA = delta + 20 USDC so target = 20 USDC. A small target makes
	// it easy for user-held synths to keep the pool clamped post-writedown
	// (2×20 = 40 USDC of synth is enough).
	poolBA := usdcDelta.Add(cosmos.NewUint(20 * common.One))
	poolBR := cosmos.NewUint(5_000_000 * common.One)
	target := common.SafeSub(poolBA, usdcDelta) // 20 USDC

	usdcPool := NewPool()
	usdcPool.Asset = ethUSDC
	usdcPool.Status = PoolAvailable
	usdcPool.BalanceAsset = poolBA
	usdcPool.BalanceRune = poolBR
	usdcPool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, usdcPool), IsNil)

	mgr.Keeper().SetTradeUnit(ctx, TradeUnit{Asset: ethUSDC.GetTradeAsset(), Units: cosmos.ZeroUint(), Depth: cosmos.ZeroUint()})
	mgr.Keeper().SetSecuredAsset(ctx, SecuredAsset{Asset: ethUSDC.GetSecuredAsset(), Depth: cosmos.ZeroUint()})

	// Vault holds target + freeBacking. freeBacking is small so savers keep
	// little and user-held synths dominate post-haircut supply.
	vaultFreeBacking := cosmos.NewUint(5 * common.One)
	vaultCoins := target.Add(vaultFreeBacking)
	activeVault := GetRandomVault()
	activeVault.Chains = common.Chains{common.ETHChain}.Strings()
	activeVault.Coins = common.Coins{common.NewCoin(ethUSDC, vaultCoins)}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// Synth supply: split into Asgard (savers), Reserve, and a plain user account.
	// User-held synths survive the haircut and keep the pool clamped.
	saversDepth := cosmos.NewUint(10 * common.One)
	reserveSynth := cosmos.NewUint(5 * common.One)
	userHeld := cosmos.NewUint(50 * common.One)
	totalSynth := saversDepth.Add(reserveSynth).Add(userHeld) // 65 USDC

	// Pre-mutation: pool must be SAFE (2×BA > synthSupply).
	c.Assert(poolBA.MulUint64(2).GT(totalSynth), Equals, true,
		Commentf("pre-mutation pool must be safe; 2×BA=%s synth=%s", poolBA.MulUint64(2), totalSynth))

	// Post-haircut: synth ≈ freeBacking + userHeld = 5 + 50 = 55. 2×target = 40 ≤ 55 → clamped.
	postSynthEstimate := vaultFreeBacking.Add(userHeld)
	c.Assert(target.MulUint64(2).LTE(postSynthEstimate), Equals, true,
		Commentf("post-haircut pool must remain clamped; 2×target=%s estimate=%s", target.MulUint64(2), postSynthEstimate))

	// Mint synths.
	synthCoinAsgard := common.NewCoin(synthUSDC, saversDepth)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoinAsgard), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoinAsgard)), IsNil)

	synthCoinReserve := common.NewCoin(synthUSDC, reserveSynth)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoinReserve), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(synthCoinReserve)), IsNil)

	// Mint user-held synths to a plain address (not a module).
	userAddr := GetRandomBech32Addr()
	synthCoinUser := common.NewCoin(synthUSDC, userHeld)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoinUser), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToAccount(ctx, ModuleName, userAddr, common.NewCoins(synthCoinUser)), IsNil)

	// Saver pool with one LP.
	saverPool := NewPool()
	saverPool.Asset = synthUSDC
	saverPool.Status = PoolAvailable
	saverPool.BalanceAsset = saversDepth
	saverPool.LPUnits = cosmos.NewUint(10 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, saverPool), IsNil)

	saverAddr := GetRandomTHORAddress()
	lp := LiquidityProvider{
		Asset: synthUSDC, RuneAddress: saverAddr,
		Units:             cosmos.NewUint(10 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
	}
	mgr.Keeper().SetLiquidityProvider(ctx, lp)

	// Fund Asgard RUNE for the freed-RUNE transfer.
	deltaR := common.GetUncappedShare(usdcDelta, poolBA, poolBR)
	freed := common.NewCoin(common.RuneNative, deltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, freed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(freed)), IsNil)

	// The migration must complete — not abort.
	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// User-held synths survive.
	userBals := mgr.Keeper().GetBalance(ctx, userAddr)
	userSynthPost := cosmos.NewUintFromBigInt(userBals.AmountOf(synthUSDC.Native()).BigInt())
	c.Assert(userSynthPost.String(), Equals, userHeld.String(),
		Commentf("user synths must survive the haircut"))

	// Pool is still clamped post-migration (the point of this test).
	gotPool, err := mgr.Keeper().GetPool(ctx, ethUSDC)
	c.Assert(err, IsNil)
	postSynth := mgr.Keeper().GetTotalSupply(ctx, synthUSDC)
	c.Assert(gotPool.BalanceAsset.MulUint64(2).LTE(postSynth), Equals, true,
		Commentf("pool should remain clamped; 2×BA=%s synth=%s", gotPool.BalanceAsset.MulUint64(2), postSynth))
}

// TestMigrate17to18BurnEntireUnbacked exercises the UNBACKED path: a writedown
// pool whose Asgard vault holds less than or equal to pool.BA (free backing = 0)
// gets BurnEntireSynthSupply — all synths are zeroed.
func (s *MigrationCommonSuite) TestMigrate17to18BurnEntireUnbacked(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	// Use GAIA.ATOM — it's in the writedown table.
	atom := mustMigrationAsset(c, "GAIA.ATOM")
	synthAtom, err := common.NewAsset("GAIA/ATOM")
	c.Assert(err, IsNil)

	specs, err := mainnetADR028WritedownSpecs17to18()
	c.Assert(err, IsNil)
	var atomDelta cosmos.Uint
	for _, sp := range specs {
		if sp.asset.Equals(atom) {
			atomDelta = sp.deltaAsset
			break
		}
	}
	c.Assert(atomDelta.IsZero(), Equals, false)

	poolBA := atomDelta.Add(cosmos.NewUint(500 * common.One))
	poolBR := cosmos.NewUint(5_000_000 * common.One)
	target := common.SafeSub(poolBA, atomDelta) // 500 ATOM

	atomPool := NewPool()
	atomPool.Asset = atom
	atomPool.Status = PoolAvailable
	atomPool.BalanceAsset = poolBA
	atomPool.BalanceRune = poolBR
	atomPool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, atomPool), IsNil)

	// UNBACKED: vault holds LESS than target → freeBacking = 0.
	vaultCoins := common.SafeSub(target, cosmos.NewUint(100*common.One))
	activeVault := GetRandomVault()
	activeVault.Chains = common.Chains{common.GAIAChain}.Strings()
	activeVault.Coins = common.Coins{common.NewCoin(atom, vaultCoins)}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// Synth supply that triggers clamp (3× target, well above 2×).
	totalSynth := target.MulUint64(3)
	synthCoin := common.NewCoin(synthAtom, totalSynth)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoin)), IsNil)

	// Fund Asgard RUNE.
	deltaR := common.GetUncappedShare(atomDelta, poolBA, poolBR)
	freed := common.NewCoin(common.RuneNative, deltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, freed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(freed)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// All synths should be burned.
	remainingSynth := mgr.Keeper().GetTotalSupply(ctx, synthAtom)
	c.Assert(remainingSynth.IsZero(), Equals, true,
		Commentf("UNBACKED path should burn all synths; got %s", remainingSynth))
}

// TestMigrate17to18ZeroSaversOnBurn exercises the saver-zeroing path for
// UNBACKED pools in adr028ZeroSaversPools (ETH.USDT, AVAX.USDC): after
// BurnEntireSynthSupply, saver LP records and the saver pool are zeroed.
func (s *MigrationCommonSuite) TestMigrate17to18ZeroSaversOnBurn(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	ethUSDT := mustMigrationAsset(c, "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	synthUSDT, err := common.NewAsset("ETH/USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)

	specs, err := mainnetADR028WritedownSpecs17to18()
	c.Assert(err, IsNil)
	var usdtDelta cosmos.Uint
	for _, sp := range specs {
		if sp.asset.Equals(ethUSDT) {
			usdtDelta = sp.deltaAsset
			break
		}
	}
	c.Assert(usdtDelta.IsZero(), Equals, false)

	poolBA := usdtDelta.Add(cosmos.NewUint(500 * common.One))
	poolBR := cosmos.NewUint(5_000_000 * common.One)
	target := common.SafeSub(poolBA, usdtDelta)

	usdtPool := NewPool()
	usdtPool.Asset = ethUSDT
	usdtPool.Status = PoolAvailable
	usdtPool.BalanceAsset = poolBA
	usdtPool.BalanceRune = poolBR
	usdtPool.LPUnits = cosmos.NewUint(1_000_000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, usdtPool), IsNil)

	// UNBACKED vault.
	vaultCoins := common.SafeSub(target, cosmos.NewUint(100*common.One))
	activeVault := GetRandomVault()
	activeVault.Chains = common.Chains{common.ETHChain}.Strings()
	activeVault.Coins = common.Coins{common.NewCoin(ethUSDT, vaultCoins)}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// Synth supply + saver pool + LP records.
	saversDepth := cosmos.NewUint(400 * common.One)
	totalSynth := target.MulUint64(3)
	synthCoin := common.NewCoin(synthUSDT, totalSynth)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoin), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(synthCoin)), IsNil)

	saverPool := NewPool()
	saverPool.Asset = synthUSDT
	saverPool.Status = PoolAvailable
	saverPool.BalanceAsset = saversDepth
	saverPool.LPUnits = cosmos.NewUint(400 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, saverPool), IsNil)

	saverAddr := GetRandomTHORAddress()
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset: synthUSDT, RuneAddress: saverAddr,
		Units:             cosmos.NewUint(400 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
	})

	// Fund Asgard RUNE.
	deltaR := common.GetUncappedShare(usdtDelta, poolBA, poolBR)
	freed := common.NewCoin(common.RuneNative, deltaR)
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, freed), IsNil)
	c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(freed)), IsNil)

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	// Synths burned.
	c.Assert(mgr.Keeper().GetTotalSupply(ctx, synthUSDT).IsZero(), Equals, true)

	// Saver pool zeroed.
	gotSaverPool, err := mgr.Keeper().GetPool(ctx, synthUSDT)
	c.Assert(err, IsNil)
	c.Assert(gotSaverPool.BalanceAsset.IsZero(), Equals, true,
		Commentf("saver pool BA should be zero; got %s", gotSaverPool.BalanceAsset))
	c.Assert(gotSaverPool.LPUnits.IsZero(), Equals, true,
		Commentf("saver pool LPUnits should be zero; got %s", gotSaverPool.LPUnits))

	// Saver LP units zeroed.
	gotLP, err := mgr.Keeper().GetLiquidityProvider(ctx, synthUSDT, saverAddr)
	c.Assert(err, IsNil)
	c.Assert(gotLP.Units.IsZero(), Equals, true,
		Commentf("saver LP units should be zero; got %s", gotLP.Units))
}

func migrationTestAddressForChain(c *C, chain common.Chain) common.Address {
	switch {
	case chain.Equals(common.ETHChain):
		return GetRandomETHAddress()
	case chain.Equals(common.GAIAChain):
		return GetRandomGAIAAddress()
	case chain.Equals(common.BTCChain):
		return GetRandomBTCAddress()
	case chain.Equals(common.LTCChain):
		return GetRandomLTCAddress()
	default:
		c.Fatalf("unsupported migration test chain: %s", chain.String())
		return common.NoAddress
	}
}
