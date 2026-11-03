//go:build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type Migrate20to21GaiaSuite struct{}

var _ = Suite(&Migrate20to21GaiaSuite{})

// Mainnet state at h28,297,751: vault ...jy4h books, GAIA.ATOM pool depths and the
// vault's Hub balance (2,697.582015 ATOM, in 1e8).
const (
	gwVaultBooks = uint64(321_334_823_200)
	gwPoolAsset  = uint64(7_899_731_887_692)
	gwPoolRune   = uint64(21_220_555_008_924)
	gwVaultHub   = uint64(269_758_201_500)
	gwWritedown  = uint64(51_576_622_200)
	// R·ΔA/(A−ΔA), rounded half-up.
	gwRunePaired = uint64(139_457_555_508)
	gwReserve    = uint64(25_000_000_00000000)
)

type gaiaWritedownFixture struct {
	ctx    cosmos.Context
	mgr    *Mgrs
	pubKey common.PubKey
}

// setupGaiaWritedown20to21 seeds the state the write-down reads: the five observed
// inbounds into vault ...jy4h, the vault's books, the GAIA.ATOM pool and module RUNE.
func setupGaiaWritedown20to21(c *C, vaultBooks, poolRune, reserve uint64) gaiaWritedownFixture {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	pubKey, err := common.NewPubKey(gaiaWritedownVault)
	c.Assert(err, IsNil)

	for _, in := range gaiaPhantomInbounds {
		setGaiaWritedownInbound(c, ctx, mgr, in.txID, pubKey, in.amount)
	}

	vault := NewVault(1, ActiveVault, AsgardVault, pubKey, []string{common.GAIAChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(vaultBooks))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.ATOMAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(gwPoolAsset)
	pool.BalanceRune = cosmos.NewUint(poolRune)
	pool.LPUnits = cosmos.NewUint(12_407_276_441_639)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	FundModule(c, ctx, k, AsgardName, poolRune)
	// Set the Reserve to exactly the requested balance (the test manager pre-funds it).
	target := cosmos.NewUint(reserve)
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.GT(target) {
		excess := common.NewCoins(common.NewCoin(common.RuneAsset(), bal.Sub(target)))
		c.Assert(k.SendFromModuleToAccount(ctx, ReserveName, GetRandomBech32Addr(), excess), IsNil)
	} else if bal.LT(target) {
		FundModule(c, ctx, k, ReserveName, target.Sub(bal).Uint64())
	}
	c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(target), Equals, true)
	return gaiaWritedownFixture{ctx: ctx, mgr: mgr, pubKey: pubKey}
}

func setGaiaWritedownInbound(c *C, ctx cosmos.Context, mgr *Mgrs, txID string, pubKey common.PubKey, amount uint64) {
	tx := common.NewTx(
		common.TxID(txID),
		"cosmos17axpg4pujs2g0ydf767gurdc5dq43qy99tya06",
		"cosmos1h2aq97wafs7px4jnajnpleu0njaqmque3hgh4v",
		common.NewCoins(common.NewCoin(common.ATOMAsset, cosmos.NewUint(amount))),
		common.Gas{common.NewCoin(common.ATOMAsset, cosmos.NewUint(1))},
		"",
	)
	voter := NewObservedTxVoter(tx.ID, nil)
	voter.Tx = common.NewObservedTx(tx, 33_467_716, pubKey, 33_467_716)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
}

// gaiaWritedownState captures everything the write-down may move.
type gaiaWritedownState struct {
	vaultAtom, poolAsset, poolRune, reserve, asgard cosmos.Uint
}

func (f gaiaWritedownFixture) state(c *C) gaiaWritedownState {
	k := f.mgr.Keeper()
	vault, err := k.GetVault(f.ctx, f.pubKey)
	c.Assert(err, IsNil)
	pool, err := k.GetPool(f.ctx, common.ATOMAsset)
	c.Assert(err, IsNil)
	return gaiaWritedownState{
		vaultAtom: vault.GetCoin(common.ATOMAsset).Amount,
		poolAsset: pool.BalanceAsset,
		poolRune:  pool.BalanceRune,
		reserve:   k.GetRuneBalanceOfModule(f.ctx, ReserveName),
		asgard:    k.GetRuneBalanceOfModule(f.ctx, AsgardName),
	}
}

func (f gaiaWritedownFixture) assertUnchanged(c *C, before gaiaWritedownState) {
	after := f.state(c)
	c.Check(after.vaultAtom.Equal(before.vaultAtom), Equals, true, Commentf("vault moved: %s -> %s", before.vaultAtom, after.vaultAtom))
	c.Check(after.poolAsset.Equal(before.poolAsset), Equals, true, Commentf("pool asset moved"))
	c.Check(after.poolRune.Equal(before.poolRune), Equals, true, Commentf("pool rune moved"))
	c.Check(after.reserve.Equal(before.reserve), Equals, true, Commentf("reserve moved"))
	c.Check(after.asgard.Equal(before.asgard), Equals, true, Commentf("asgard moved"))
	for _, e := range f.ctx.EventManager().Events() {
		c.Check(e.Type, Not(Equals), "pool_balance_change", Commentf("skipped write-down leaked an event"))
	}
}

func (s *Migrate20to21GaiaSuite) TestMigrate20to21WritesDownGaiaAtom(c *C) {
	f := setupGaiaWritedown20to21(c, gwVaultBooks, gwPoolRune, gwReserve)
	before := f.state(c)

	c.Assert(NewMigrator(f.mgr).Migrate20to21(f.ctx), IsNil)

	after := f.state(c)
	// The vault's books drop by the five credited inbounds and land 5 uatom under its
	// real Hub balance, so the solvency check passes.
	c.Check(after.vaultAtom.Uint64(), Equals, gwVaultBooks-gwWritedown)
	c.Check(after.vaultAtom.LTE(cosmos.NewUint(gwVaultHub)), Equals, true)
	c.Check(after.poolAsset.Uint64(), Equals, gwPoolAsset-gwWritedown)
	c.Check(after.poolRune.Uint64(), Equals, gwPoolRune+gwRunePaired)
	c.Check(before.reserve.Sub(after.reserve).Uint64(), Equals, gwRunePaired)
	c.Check(after.asgard.Sub(before.asgard).Uint64(), Equals, gwRunePaired)

	// The constant product is preserved to within one unit of the remaining depth.
	k0 := cosmos.NewUint(gwPoolAsset).Mul(cosmos.NewUint(gwPoolRune))
	k1 := after.poolAsset.Mul(after.poolRune)
	drift := k1.Sub(k0)
	if k1.LT(k0) {
		drift = k0.Sub(k1)
	}
	c.Check(drift.LT(after.poolAsset), Equals, true, Commentf("k drift %s", drift))

	found := false
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type != "pool_balance_change" {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key == "reason" && a.Value == "gaia_atom_writedown_20to21" {
				found = true
			}
		}
	}
	c.Check(found, Equals, true, Commentf("no pool_balance_change event for the write-down"))
}

func (s *Migrate20to21GaiaSuite) TestMigrate20to21SkipsOnInboundDrift(c *C) {
	other := GetRandomPubKey()
	for name, mutate := range map[string]func(c *C, f gaiaWritedownFixture){
		"amount": func(c *C, f gaiaWritedownFixture) {
			setGaiaWritedownInbound(c, f.ctx, f.mgr, gaiaPhantomInbounds[2].txID, f.pubKey, gaiaPhantomInbounds[2].amount+1)
		},
		"vault": func(c *C, f gaiaWritedownFixture) {
			setGaiaWritedownInbound(c, f.ctx, f.mgr, gaiaPhantomInbounds[4].txID, other, gaiaPhantomInbounds[4].amount)
		},
		"missing": func(c *C, f gaiaWritedownFixture) {
			f.mgr.Keeper().SetObservedTxInVoter(f.ctx, NewObservedTxVoter(common.TxID(gaiaPhantomInbounds[0].txID), nil))
		},
	} {
		f := setupGaiaWritedown20to21(c, gwVaultBooks, gwPoolRune, gwReserve)
		mutate(c, f)
		before := f.state(c)
		c.Assert(NewMigrator(f.mgr).Migrate20to21(f.ctx), IsNil, Commentf(name))
		f.assertUnchanged(c, before)
	}
}

func (s *Migrate20to21GaiaSuite) TestMigrate20to21SkipsWhenVaultHoldsLess(c *C) {
	f := setupGaiaWritedown20to21(c, gwWritedown-1, gwPoolRune, gwReserve)
	before := f.state(c)
	c.Assert(NewMigrator(f.mgr).Migrate20to21(f.ctx), IsNil)
	f.assertUnchanged(c, before)
}

func (s *Migrate20to21GaiaSuite) TestMigrate20to21SkipsWhenReserveShort(c *C) {
	f := setupGaiaWritedown20to21(c, gwVaultBooks, gwPoolRune, gwRunePaired-1)
	before := f.state(c)
	c.Assert(NewMigrator(f.mgr).Migrate20to21(f.ctx), IsNil)
	f.assertUnchanged(c, before)
}

func (s *Migrate20to21GaiaSuite) TestMigrate20to21ClampsRunePairing(c *C) {
	// A pool pushed toward RUNE (3x the RUNE depth) needs more than the cap.
	poolRune := gwPoolRune * 3
	f := setupGaiaWritedown20to21(c, gwVaultBooks, poolRune, gwReserve)
	before := f.state(c)

	c.Assert(NewMigrator(f.mgr).Migrate20to21(f.ctx), IsNil)

	after := f.state(c)
	c.Check(after.vaultAtom.Uint64(), Equals, gwVaultBooks-gwWritedown)
	c.Check(after.poolAsset.Uint64(), Equals, gwPoolAsset-gwWritedown)
	c.Check(after.poolRune.Uint64(), Equals, poolRune+gaiaWritedownMaxRune)
	c.Check(before.reserve.Sub(after.reserve).Uint64(), Equals, gaiaWritedownMaxRune)
}

// The pinned inbounds must add up to the documented write-down.
func (s *Migrate20to21GaiaSuite) TestMigrate20to21PinnedWritedownTotal(c *C) {
	total := uint64(0)
	for _, in := range gaiaPhantomInbounds {
		total += in.amount
	}
	c.Check(total, Equals, gwWritedown)
}
