//go:build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// TestSolSlashRefund19to20SumMatchesTotal pins the SOL phantom-insolvency slash-refund data:
// the 208 per-bond-provider rows must sum exactly to the reversed bond slash total.
func (s *EthThorRestoreSuite) TestSolSlashRefund19to20SumMatchesTotal(c *C) {
	var sum uint64
	for _, r := range mainnetSolSlashRefunds19to20 {
		sum += r.amount
	}
	c.Assert(len(mainnetSolSlashRefunds19to20), Equals, 208)
	c.Assert(sum, Equals, solSlashBondRefundTotal)
	c.Assert(sum, Equals, uint64(1228170332541))                    // 12,281.70332541 RUNE
	c.Assert(solSlashPoolRuneCredit, Equals, uint64(614085166271))  // 6,140.85166271 RUNE
	c.Assert(solSlashPoolAssetMarkdown, Equals, uint64(3064000109)) // 30.64000109 SOL
}

// solSlashFirstNode gathers the first node in the pinned table and its provider rows.
func solSlashFirstNode(c *C) (cosmos.AccAddress, []struct {
	addr   cosmos.AccAddress
	amount cosmos.Uint
}, cosmos.Uint,
) {
	nodeStr := mainnetSolSlashRefunds19to20[0].node
	nodeAddr, err := cosmos.AccAddressFromBech32(nodeStr)
	c.Assert(err, IsNil)
	var provs []struct {
		addr   cosmos.AccAddress
		amount cosmos.Uint
	}
	total := cosmos.ZeroUint()
	for _, r := range mainnetSolSlashRefunds19to20 {
		if r.node != nodeStr {
			continue
		}
		a, aerr := cosmos.AccAddressFromBech32(r.provider)
		c.Assert(aerr, IsNil)
		provs = append(provs, struct {
			addr   cosmos.AccAddress
			amount cosmos.Uint
		}{a, cosmos.NewUint(r.amount)})
		total = total.Add(cosmos.NewUint(r.amount))
	}
	c.Assert(len(provs) > 0, Equals, true)
	return nodeAddr, provs, total
}

// solSlashPoolSetup seeds a SOL pool carrying the phantom RUNE credit, the Asgard-module RUNE
// that backs it, and an active vault holding the pool asset PLUS the unaccounted 30.64 SOL
// (so the solvency gate sees exactly the markdown as free). HaltSigningSOL is set when halt is
// true. Returns the SOL asset and the seeded pool.
func solSlashPoolSetup(c *C, ctx cosmos.Context, mgr *Mgrs, halt bool) (common.Asset, Pool) {
	k := mgr.Keeper()
	sol, err := common.NewAsset(solSlashAsset)
	c.Assert(err, IsNil)
	base := cosmos.NewUint(common.One)
	pool := NewPool()
	pool.Asset = sol
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(solSlashPoolRuneCredit).Add(base)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Asgard module RUNE backs the pool RUNE (so the Asgard->Reserve reclaim can send).
	asgardFund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(solSlashPoolRuneCredit).Add(base))
	c.Assert(k.MintToModule(ctx, ModuleName, asgardFund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(asgardFund)), IsNil)

	// Active vault holding pool depth + the unaccounted markdown SOL (the SOL the vault still
	// holds that the phantom slash marked the pool down for).
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Coins = common.NewCoins(common.NewCoin(sol, pool.BalanceAsset.AddUint64(solSlashPoolAssetMarkdown)))
	c.Assert(k.SetVault(ctx, v), IsNil)

	if halt {
		k.SetMimir(ctx, "HaltSigningSOL", 1)
	}
	return sol, pool
}

// TestSolSlashRefundFullReversal exercises the chosen default (poolLegFullReversal, B): the
// first node's bond is restored per provider (Reserve->Bond), AND the pool leg is fully
// reversed -- the 30.64 SOL markdown is credited back (solvency-gated) and the 6,140.85
// phantom RUNE is debited (Asgard->Reserve). The other 18 nodes are unseeded and soft-skip.
func (s *EthThorRestoreSuite) TestSolSlashRefundFullReversal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	nodeAddr, provs, nodeTotal := solSlashFirstNode(c)
	na := GetRandomValidatorNode(NodeActive)
	na.NodeAddress = nodeAddr
	base := cosmos.NewUint(common.One)
	bp := NewBondProviders(nodeAddr)
	bp.Providers = append(bp.Providers, NewBondProvider(nodeAddr)) // operator
	bp.Bond(base, nodeAddr)
	seeded := base
	baseByProv := map[string]cosmos.Uint{nodeAddr.String(): base}
	for _, p := range provs {
		if p.addr.Equals(nodeAddr) {
			continue
		}
		bp.Providers = append(bp.Providers, NewBondProvider(p.addr))
		bp.Bond(base, p.addr)
		baseByProv[p.addr.String()] = base
		seeded = seeded.Add(base)
	}
	na.Bond = seeded
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	c.Assert(k.SetBondProviders(ctx, bp), IsNil)

	sol, pool := solSlashPoolSetup(c, ctx, mgr, true)

	rFund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(solSlashBondRefundTotal).Add(base))
	c.Assert(k.MintToModule(ctx, ModuleName, rFund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(rFund)), IsNil)

	bondBefore := k.GetRuneBalanceOfModule(ctx, BondName)
	asgardBefore := k.GetRuneBalanceOfModule(ctx, AsgardName)

	runSolSlashRefund19to20(ctx, mgr)

	// bond restored per provider + node total.
	naAfter, err := k.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Check(naAfter.Bond.Uint64(), Equals, seeded.Add(nodeTotal).Uint64())
	bpAfter, err := k.GetBondProviders(ctx, nodeAddr)
	c.Assert(err, IsNil)
	for _, p := range provs {
		got := bpAfter.Get(p.addr)
		want := baseByProv[p.addr.String()].Add(p.amount)
		c.Check(got.Bond.Uint64(), Equals, want.Uint64(), Commentf("provider %s", p.addr))
	}

	// pool fully reversed: RUNE -= credit, asset += markdown.
	pAfter, err := k.GetPool(ctx, sol)
	c.Assert(err, IsNil)
	c.Check(common.SafeSub(pool.BalanceRune, pAfter.BalanceRune).Uint64(), Equals, solSlashPoolRuneCredit, Commentf("pool rune debited"))
	c.Check(common.SafeSub(pAfter.BalanceAsset, pool.BalanceAsset).Uint64(), Equals, solSlashPoolAssetMarkdown, Commentf("pool SOL restored"))
	// the clawed RUNE left the Asgard module; Bond module rose by the node sum.
	c.Check(common.SafeSub(asgardBefore, k.GetRuneBalanceOfModule(ctx, AsgardName)).Uint64(), Equals, solSlashPoolRuneCredit)
	c.Check(common.SafeSub(k.GetRuneBalanceOfModule(ctx, BondName), bondBefore).Uint64(), Equals, nodeTotal.Uint64())
}

// TestSolSlashFullReversalSkipsWhenSigningNotHalted: like the ZEC/TRON S1 guard, the SOL
// full-reversal pool leg skips (errors) when HaltSigningSOL is not set, so the whole step
// soft-skips rather than credit pool depth an unobserved retry could leave unbacked.
func (s *EthThorRestoreSuite) TestSolSlashFullReversalSkipsWhenSigningNotHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	solSlashPoolSetup(c, ctx, mgr, false) // HaltSigningSOL NOT set
	err := applySolSlashPoolLeg19to20(ctx, mgr, poolLegFullReversal)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*HaltSigningSOL not set.*")
}

// TestSolSlashPoolLegFullReversalReverses covers the B pool leg directly: with HaltSigningSOL
// set and the backing SOL present, it debits the phantom RUNE and credits the markdown SOL.
func (s *EthThorRestoreSuite) TestSolSlashPoolLegFullReversalReverses(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	sol, pool := solSlashPoolSetup(c, ctx, mgr, true)
	asgardBefore := k.GetRuneBalanceOfModule(ctx, AsgardName)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	c.Assert(applySolSlashPoolLeg19to20(ctx, mgr, poolLegFullReversal), IsNil)

	pAfter, err := k.GetPool(ctx, sol)
	c.Assert(err, IsNil)
	c.Check(common.SafeSub(pool.BalanceRune, pAfter.BalanceRune).Uint64(), Equals, solSlashPoolRuneCredit)
	c.Check(common.SafeSub(pAfter.BalanceAsset, pool.BalanceAsset).Uint64(), Equals, solSlashPoolAssetMarkdown)
	c.Check(common.SafeSub(asgardBefore, k.GetRuneBalanceOfModule(ctx, AsgardName)).Uint64(), Equals, solSlashPoolRuneCredit)
	c.Check(common.SafeSub(k.GetRuneBalanceOfModule(ctx, ReserveName), reserveBefore).Uint64(), Equals, solSlashPoolRuneCredit)
}

// TestSolSlashFullReversalSkipsWhenVaultLacksSOL: the solvency gate fails if the vault does
// not hold the unaccounted SOL that would back the restored depth.
func (s *EthThorRestoreSuite) TestSolSlashFullReversalSkipsWhenVaultLacksSOL(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	sol, pool := solSlashPoolSetup(c, ctx, mgr, true)
	// Strip the vault's unaccounted SOL (vault now holds only pool depth).
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(len(vaults) >= 1, Equals, true)
	v := vaults[0]
	v.Coins = common.NewCoins(common.NewCoin(sol, pool.BalanceAsset))
	c.Assert(k.SetVault(ctx, v), IsNil)

	err = applySolSlashPoolLeg19to20(ctx, mgr, poolLegFullReversal)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*unaccounted SOL.*below markdown.*")
}

// TestSolSlashPoolLegNoneIsNoop: the A variant leaves the SOL pool and Reserve untouched.
func (s *EthThorRestoreSuite) TestSolSlashPoolLegNoneIsNoop(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	sol, pool := solSlashPoolSetup(c, ctx, mgr, true)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	c.Assert(applySolSlashPoolLeg19to20(ctx, mgr, poolLegNone), IsNil)

	pAfter, err := k.GetPool(ctx, sol)
	c.Assert(err, IsNil)
	c.Check(pAfter.BalanceRune.Equal(pool.BalanceRune), Equals, true, Commentf("None must not touch the pool"))
	c.Check(pAfter.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

// TestSolSlashPoolLegReclaimReclaimsRune covers the C leg directly: the phantom pool RUNE is
// reclaimed Asgard->Reserve (pool RUNE drops by the credit) and the pool asset is untouched.
func (s *EthThorRestoreSuite) TestSolSlashPoolLegReclaimReclaimsRune(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	sol, pool := solSlashPoolSetup(c, ctx, mgr, true)
	asgardBefore := k.GetRuneBalanceOfModule(ctx, AsgardName)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

	c.Assert(applySolSlashPoolLeg19to20(ctx, mgr, poolLegReclaimRune), IsNil)

	pAfter, err := k.GetPool(ctx, sol)
	c.Assert(err, IsNil)
	c.Check(common.SafeSub(pool.BalanceRune, pAfter.BalanceRune).Uint64(), Equals, solSlashPoolRuneCredit, Commentf("pool rune reclaimed"))
	c.Check(pAfter.BalanceAsset.Equal(pool.BalanceAsset), Equals, true, Commentf("asset untouched under C"))
	c.Check(common.SafeSub(asgardBefore, k.GetRuneBalanceOfModule(ctx, AsgardName)).Uint64(), Equals, solSlashPoolRuneCredit)
	c.Check(common.SafeSub(k.GetRuneBalanceOfModule(ctx, ReserveName), reserveBefore).Uint64(), Equals, solSlashPoolRuneCredit)
}

// TestSolSlashPoolLegReclaimSkipsOnDrift: if the SOL pool no longer holds the phantom credit,
// the reclaim aborts so the whole step soft-skips rather than over-pull.
func (s *EthThorRestoreSuite) TestSolSlashPoolLegReclaimSkipsOnDrift(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	sol, err := common.NewAsset(solSlashAsset)
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = sol
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(solSlashPoolRuneCredit - 1) // below the credit
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(1_000_000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	err = applySolSlashPoolLeg19to20(ctx, mgr, poolLegReclaimRune)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*below phantom credit.*")
}
