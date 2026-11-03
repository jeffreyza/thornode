package thorchain

import (
	"errors"
	"fmt"
	"math/big"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Tests for the overflow-safe calculatePoolUnits and the LUVIOverhangGuard,
// which rejects liquidity adds (user and POL) into a pool in synth overhang.

type LuviOverhangGuardSuite struct{}

var _ = Suite(&LuviOverhangGuardSuite{})

// calculatePoolUnitsReference is calculatePoolUnits as it was before the
// overflow checks, with its panics turned into a flag. The checked version
// must match it bit for bit wherever it does not panic.
func calculatePoolUnitsReference(oldPoolUnits, poolRune, poolAsset, addRune, addAsset cosmos.Uint) (pUnits, sUnits cosmos.Uint, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	if addRune.Add(poolRune).IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("total RUNE in the pool is zero"), false
	}
	if addAsset.Add(poolAsset).IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("total asset in the pool is zero"), false
	}
	if poolRune.IsZero() || poolAsset.IsZero() || oldPoolUnits.IsZero() {
		s := addRune
		if addRune.IsZero() {
			if !poolAsset.IsZero() {
				s = addAsset.Mul(poolRune).Quo(poolAsset)
			} else {
				s = addAsset
			}
		}
		return oldPoolUnits.Add(s), s, nil, false
	}
	P := cosmos.NewDecFromBigInt(oldPoolUnits.BigInt())
	R := cosmos.NewDecFromBigInt(poolRune.BigInt())
	A := cosmos.NewDecFromBigInt(poolAsset.BigInt())
	r := cosmos.NewDecFromBigInt(addRune.BigInt())
	a := cosmos.NewDecFromBigInt(addAsset.BigInt())
	cross := r.Mul(A).Add(a.Mul(R))
	numerator := P.Mul(cross.Add(cosmos.NewDec(2).Mul(r).Mul(a)))
	denominator := cross.Add(cosmos.NewDec(2).Mul(A).Mul(R))
	if denominator.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("denominator is zero"), false
	}
	liquidityUnits := numerator.Quo(denominator)
	newPoolUnit := P.Add(liquidityUnits)
	return cosmos.NewUintFromBigInt(newPoolUnit.TruncateInt().BigInt()),
		cosmos.NewUintFromBigInt(liquidityUnits.TruncateInt().BigInt()), nil, false
}

func pow2(n uint) cosmos.Uint {
	return cosmos.NewUintFromBigInt(new(big.Int).Lsh(big.NewInt(1), n))
}

func maxUint256() cosmos.Uint {
	return cosmos.NewUintFromBigInt(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)))
}

func (s *LuviOverhangGuardSuite) TestCalculatePoolUnitsOverflowReturnsError(c *C) {
	one := cosmos.OneUint()
	zero := cosmos.ZeroUint()
	maxU := maxUint256()
	cases := []struct {
		name                    string
		P, R, A, r, a           cosmos.Uint
		referencePanicsExpected bool
	}{
		{"numerator P*(rA+aR+2ra)", pow2(200), pow2(40), pow2(30), pow2(40), pow2(30), true},
		{"denominator 2AR", one, pow2(130), pow2(130), one, one, true},
		{"cross r*A", one, one, pow2(200), pow2(100), zero, true},
		{"2*r", one, one, one, pow2(255), zero, true},
		{"P + liquidity units", maxU, one, one, one, zero, true},
		{"empty pool asset*rune", zero, pow2(200), one, zero, pow2(100), true},
		{"empty pool units + added", maxU, zero, one, one, zero, true},
	}
	for _, tc := range cases {
		_, _, _, panicked := calculatePoolUnitsReference(tc.P, tc.R, tc.A, tc.r, tc.a)
		c.Check(panicked, Equals, tc.referencePanicsExpected, Commentf("%s: reference panic", tc.name))
		_, _, err := calculatePoolUnits(tc.P, tc.R, tc.A, tc.r, tc.a)
		c.Check(errors.Is(err, errPoolUnitsOverflow), Equals, true, Commentf("%s: %v", tc.name, err))
	}
}

func (s *LuviOverhangGuardSuite) TestCalculatePoolUnitsMatchesReference(c *C) {
	// random operands of every size up to 256 bits, biased toward the overflow
	// boundary: wherever the old code did not panic the new one must return the
	// same units bit for bit, and wherever it panicked the new one must error
	rng := newDetRand(7)
	randUint := func() cosmos.Uint {
		switch rng.next() % 5 {
		case 0:
			return cosmos.ZeroUint()
		case 1:
			return cosmos.NewUint(rng.next() % 1_000_000)
		}
		bits := uint(rng.next() % 257)
		v := new(big.Int)
		for v.BitLen() < int(bits) {
			v.Lsh(v, 31)
			v.Or(v, new(big.Int).SetUint64(rng.next()))
		}
		if extra := v.BitLen() - int(bits); extra > 0 {
			v.Rsh(v, uint(extra))
		}
		return cosmos.NewUintFromBigInt(v)
	}
	matched, overflowed := 0, 0
	for i := 0; i < 20_000; i++ {
		P, R, A, r, a := randUint(), randUint(), randUint(), randUint(), randUint()
		wantP, wantS, wantErr, panicked := calculatePoolUnitsReference(P, R, A, r, a)
		gotP, gotS, gotErr := calculatePoolUnits(P, R, A, r, a)
		in := fmt.Sprintf("P=%s R=%s A=%s r=%s a=%s", P, R, A, r, a)
		if panicked {
			overflowed++
			c.Check(errors.Is(gotErr, errPoolUnitsOverflow), Equals, true, Commentf("%s: %v", in, gotErr))
			continue
		}
		c.Check(gotErr == nil, Equals, wantErr == nil, Commentf("%s: %v vs %v", in, gotErr, wantErr))
		if wantErr == nil && gotErr == nil {
			matched++
			c.Check(gotP.Equal(wantP), Equals, true, Commentf("%s: pool units %s vs %s", in, gotP, wantP))
			c.Check(gotS.Equal(wantS), Equals, true, Commentf("%s: units %s vs %s", in, gotS, wantS))
		}
	}
	c.Logf("%d matched, %d overflowed", matched, overflowed)
	c.Check(matched > 1_000, Equals, true)
	c.Check(overflowed > 1_000, Equals, true)
}

func (s *LuviOverhangGuardSuite) TestOverhangAddRejectedByDefault(c *C) {
	for _, r := range []luviRegime{{"boundary S=2A", 200}, {"overhang", 300}, {"deep overhang", 700}} {
		e := newLuviEnv(c, r)
		c.Assert(e.state().IsOverhang(), Equals, true)
		for _, add := range [][2]uint64{{luviPoolRune / 100, luviPoolAsset / 100}, {luviPoolRune / 100, 0}, {0, luviPoolAsset / 100}} {
			_, err := e.tryAddLiquidity(add[0], add[1])
			c.Check(errors.Is(err, errAddLiquidityPoolOverhang), Equals, true, Commentf("%s %v: %v", r.name, add, err))
			c.Check(e.state(), DeepEquals, e.baseline, Commentf(r.name))
		}
	}
}

func (s *LuviOverhangGuardSuite) TestOverhangAddRejectedThroughHandlerRun(c *C) {
	// the full handler (validate + handle), as POL and the swap queue call it
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	polAddr, err := e.mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	asgardAddr, err := e.mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	nas, err := e.mgr.Keeper().ListActiveValidators(e.ctx)
	c.Assert(err, IsNil)
	amt := cosmos.NewUint(luviPoolRune / 100)
	tx := common.NewTx(common.BlankTxID, polAddr, asgardAddr, common.NewCoins(common.NewCoin(common.RuneAsset(), amt)), nil, "THOR-POL-ADD")
	msg := NewMsgAddLiquidity(tx, e.asset, amt, cosmos.ZeroUint(), polAddr, common.NoAddress, common.NoAddress, cosmos.ZeroUint(), nas[0].NodeAddress)

	_, err = NewAddLiquidityHandler(e.mgr).Run(e.ctx, msg)
	c.Check(errors.Is(err, errAddLiquidityPoolOverhang), Equals, true, Commentf("%v", err))
	c.Check(e.state(), DeepEquals, e.baseline)

	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
	_, err = NewAddLiquidityHandler(e.mgr).Run(e.ctx, msg)
	c.Check(err, IsNil)
	c.Check(e.pool().LPUnits.GT(e.baseline.LPUnits), Equals, true)
}

func (s *LuviOverhangGuardSuite) TestOverhangPendingAddRejected(c *C) {
	// a staged half (rune with a paired asset address) is rejected too, so it
	// never sits as pending liquidity in an overhang pool
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	h := NewAddLiquidityHandler(e.mgr)
	runeAddr, assetAddr := GetRandomRUNEAddress(), GetRandomETHAddress()
	err := h.addLiquidity(e.ctx, e.asset, cosmos.NewUint(luviPoolRune/100), cosmos.ZeroUint(),
		runeAddr, assetAddr, GetRandomTxHash(), true, constants.GetConstantValues(GetCurrentVersion()))
	c.Check(errors.Is(err, errAddLiquidityPoolOverhang), Equals, true, Commentf("%v", err))
	c.Check(e.pool().PendingInboundRune.IsZero(), Equals, true)

	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
	err = h.addLiquidity(e.ctx, e.asset, cosmos.NewUint(luviPoolRune/100), cosmos.ZeroUint(),
		runeAddr, assetAddr, GetRandomTxHash(), true, constants.GetConstantValues(GetCurrentVersion()))
	c.Check(err, IsNil)
	c.Check(e.pool().PendingInboundRune.Equal(cosmos.NewUint(luviPoolRune/100)), Equals, true)
}

func (s *LuviOverhangGuardSuite) TestOverhangAddAllowedWhenGuardOff(c *C) {
	// 0 turns the guard off (a negative mimir is unset and falls back to 1)
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
	units, err := e.tryAddLiquidity(luviPoolRune/100, luviPoolAsset/100)
	c.Check(err, IsNil)
	c.Check(units.IsZero(), Equals, false)
}

func (s *LuviOverhangGuardSuite) TestOverhangWithdrawAllowed(c *C) {
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	before := e.pool().LPUnits
	burned := e.withdraw(1_000, common.EmptyAsset)
	c.Check(burned.IsZero(), Equals, false)
	c.Check(e.pool().LPUnits.LT(before), Equals, true)
}

func (s *LuviOverhangGuardSuite) TestNonOverhangAddUnchanged(c *C) {
	// below overhang (and with no synths at all, including a first deposit into
	// an empty pool) the guard is not involved: the add mints the same units
	// with the guard on and off
	for _, r := range []luviRegime{{"no synths", 0}, {"light synths", 40}, {"heavy synths", 199}} {
		on := newLuviEnv(c, r)
		off := newLuviEnv(c, r)
		off.mgr.Keeper().SetMimir(off.ctx, constants.LUVIOverhangGuard.String(), 0)
		c.Assert(on.state().IsOverhang(), Equals, false)
		uOn := on.addLiquidity(luviPoolRune/100, luviPoolAsset/50)
		uOff := off.addLiquidity(luviPoolRune/100, luviPoolAsset/50)
		c.Check(uOn.Equal(uOff), Equals, true, Commentf("%s: %s vs %s", r.name, uOn, uOff))
		c.Check(on.state(), DeepEquals, off.state(), Commentf(r.name))
	}

	e := newLuviEnvFor(c, luviRegime{"no synths", 0}, common.BTCAsset)
	e.setPool(func(p *Pool) {
		p.BalanceRune, p.BalanceAsset, p.LPUnits = cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()
	})
	c.Assert(e.state().IsOverhang(), Equals, true) // 0 >= 2*0, but no synths
	h := NewAddLiquidityHandler(e.mgr)
	err := h.addLiquidity(e.ctx, e.asset, cosmos.NewUint(luviPoolRune), cosmos.NewUint(luviPoolAsset),
		GetRandomRUNEAddress(), GetRandomBTCAddress(), GetRandomTxHash(), false, constants.GetConstantValues(GetCurrentVersion()))
	c.Check(err, IsNil)
	c.Check(e.pool().LPUnits.IsZero(), Equals, false)
}

// enablePOL turns on POL for the test pool with a 1% movement per cycle.
func (e *luviEnv) enablePOL() {
	k := e.mgr.Keeper()
	k.SetMimir(e.ctx, "POL-"+e.asset.MimirString(), 1)
	k.SetMimir(e.ctx, constants.POLTargetSynthPerPoolDepth.String(), 1_000)
	k.SetMimir(e.ctx, constants.POLMaxNetworkDeposit.String(), int64(100*luviPoolRune))
	k.SetMimir(e.ctx, constants.POLMaxPoolMovement.String(), 10_000)
	k.SetMimir(e.ctx, constants.POLBuffer.String(), 0)
}

func (s *LuviOverhangGuardSuite) TestPOLCycleSkipsOverhangPool(c *C) {
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	e.enablePOL()
	nm := newNetworkMgr(e.mgr.Keeper(), e.mgr.TxOutStore(), e.mgr.EventMgr())
	reserve := e.mgr.Keeper().GetRuneBalanceOfModule(e.ctx, ReserveName)
	for i := 0; i < 50; i++ {
		c.Check(nm.POLCycle(e.ctx, e.mgr), IsNil, Commentf("cycle %d", i))
	}
	c.Check(e.state(), DeepEquals, e.baseline)
	c.Check(e.mgr.Keeper().GetRuneBalanceOfModule(e.ctx, ReserveName).Equal(reserve), Equals, true)
	pol, err := e.mgr.Keeper().GetPOL(e.ctx)
	c.Assert(err, IsNil)
	c.Check(pol.RuneDeposited.IsZero(), Equals, true)
}

func (s *LuviOverhangGuardSuite) TestPOLCycleDepositsBelowOverhang(c *C) {
	e := newLuviEnv(c, luviRegime{"heavy synths", 180})
	e.enablePOL()
	nm := newNetworkMgr(e.mgr.Keeper(), e.mgr.TxOutStore(), e.mgr.EventMgr())
	c.Assert(nm.POLCycle(e.ctx, e.mgr), IsNil)
	pol, err := e.mgr.Keeper().GetPOL(e.ctx)
	c.Assert(err, IsNil)
	c.Check(pol.RuneDeposited.IsZero(), Equals, false)
}

func (s *LuviOverhangGuardSuite) TestPOLCycleOverhangWithGuardOffNeverPanics(c *C) {
	// The halt: every POL deposit into an overhang pool multiplies LP units by
	// ~1+(1+S)f until the unit math leaves the 256-bit range. With the guard off
	// the deposit must start failing cleanly (rune back in the reserve, pool
	// untouched) instead of panicking in EndBlock.
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	e.enablePOL()
	e.mgr.Keeper().SetMimir(e.ctx, constants.LUVIOverhangGuard.String(), 0)
	nm := newNetworkMgr(e.mgr.Keeper(), e.mgr.TxOutStore(), e.mgr.EventMgr())
	stalled := -1
	for i := 0; i < 50; i++ {
		before := e.pool()
		reserve := e.mgr.Keeper().GetRuneBalanceOfModule(e.ctx, ReserveName)
		c.Check(nm.POLCycle(e.ctx, e.mgr), IsNil, Commentf("cycle %d", i))
		after := e.pool()
		if after.LPUnits.Equal(before.LPUnits) {
			c.Check(after.BalanceRune.Equal(before.BalanceRune), Equals, true, Commentf("cycle %d", i))
			c.Check(e.mgr.Keeper().GetRuneBalanceOfModule(e.ctx, ReserveName).Equal(reserve), Equals, true, Commentf("cycle %d", i))
			if stalled < 0 {
				stalled = i
			}
		}
	}
	c.Logf("POL deposits stopped at cycle %d", stalled)
	c.Check(stalled > 0, Equals, true, Commentf("POL never deposited or never hit the overflow"))

	// the direct add now reports the overflow
	polAddr, err := e.mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	asgardAddr, err := e.mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)
	nas, err := e.mgr.Keeper().ListActiveValidators(e.ctx)
	c.Assert(err, IsNil)
	err = nm.addPOLLiquidity(e.ctx, e.pool(), polAddr, asgardAddr, nas[0].NodeAddress, cosmos.NewUint(10_000), cosmos.NewUint(10_000), cosmos.ZeroUint(), e.mgr, &polValueMemo{})
	c.Check(errors.Is(err, errPoolUnitsOverflow), Equals, true, Commentf("%v", err))
}

func (s *LuviOverhangGuardSuite) TestPOLReserveCycleSkipsOverhangPool(c *C) {
	// the overhang pool has the best fee score, so without the guard the
	// reserve would deploy into it
	e := newLuviEnv(c, luviRegime{"overhang", 300})
	k := e.mgr.Keeper()
	btc := NewPool()
	btc.Asset = common.BTCAsset
	btc.Status = PoolAvailable
	btc.BalanceRune = cosmos.NewUint(luviPoolRune)
	btc.BalanceAsset = cosmos.NewUint(luviPoolAsset)
	btc.LPUnits = cosmos.NewUint(luviPoolRune)
	c.Assert(k.SetPool(e.ctx, btc), IsNil)
	c.Assert(k.AddToLiquidityFees(e.ctx, e.asset, cosmos.NewUint(1_000_000)), IsNil)
	c.Assert(k.AddToLiquidityFees(e.ctx, common.BTCAsset, cosmos.NewUint(1)), IsNil)
	FundModule(c, e.ctx, k, POLReserveName, 50*common.One)
	k.SetMimir(e.ctx, constants.POLReserveMaxDeployment.String(), int64(100*common.One))
	nm := newNetworkMgr(k, e.mgr.TxOutStore(), e.mgr.EventMgr())

	c.Assert(nm.POLReserveCycle(e.ctx, e.mgr), IsNil)
	dEth, err := k.GetPOLReserveDeposit(e.ctx, e.asset)
	c.Assert(err, IsNil)
	dBtc, err := k.GetPOLReserveDeposit(e.ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(dEth.RuneDeposited.IsZero(), Equals, true)
	c.Check(dBtc.RuneDeposited.Equal(cosmos.NewUint(50*common.One)), Equals, true)
	c.Check(e.state(), DeepEquals, e.baseline)
}
