package thorchain

import (
	"encoding/json"

	sdkmath "cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var fairValueJSONKeys = []string{
	"lp_unit_fair_value_rune", "lp_unit_fair_value_asset", "lp_unit_spot_value_rune",
	"lp_fair_spot_deviation_bps", "fair_value_oracle_price_rune", "lp_fair_value_overhang",
}

func poolJSONKeys(c *C, p *types.QueryPoolResponse) map[string]any {
	raw, err := p.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	var m map[string]any
	c.Assert(json.Unmarshal(raw, &m), IsNil)
	return m
}

func (s *QuerierSuite) TestQueryPoolFairValueFields(c *C) {
	type want struct {
		fairRune, fairAsset, spotRune, devBps, price string
		overhang                                     bool
	}
	// A = 1e12, R = 4e12, L = 1e12: spot price 4 RUNE per ETH
	cases := []struct {
		name      string
		synth     uint64
		ethUSD    string
		runeUSD   string
		want      want
		wantEmpty bool
	}{
		{name: "no oracle prices", wantEmpty: true},
		{name: "no RUNE price", ethUSD: "8", wantEmpty: true},
		{name: "no asset price", runeUSD: "2", wantEmpty: true},
		{name: "malformed price", ethUSD: "8e0", runeUSD: "2", wantEmpty: true},
		{name: "zero price", ethUSD: "0", runeUSD: "2", wantEmpty: true},
		{name: "oracle at spot", ethUSD: "8", runeUSD: "2", want: want{"8.000000000000000000", "2.000000000000000000", "8.000000000000000000", "0", "4.000000000000000000", false}},
		{name: "oracle at spot with synths", synth: 500_000_000_000, ethUSD: "8", runeUSD: "2", want: want{"6.000000000000000000", "1.500000000000000000", "6.000000000000000000", "0", "4.000000000000000000", false}},
		// P = 16: fair = 2*2e12*4 = 16e12, spot = 8e12
		{name: "oracle above spot", ethUSD: "32", runeUSD: "2", want: want{"16.000000000000000000", "1.000000000000000000", "8.000000000000000000", "-5000", "16.000000000000000000", false}},
		// S = 3A: liability exceeds the pool, values clamp to zero
		{name: "overhang", synth: 3_000_000_000_000, ethUSD: "8", runeUSD: "2", want: want{"0.000000000000000000", "0.000000000000000000", "0.000000000000000000", "", "4.000000000000000000", true}},
	}
	for _, tc := range cases {
		ctx, mgr := setupManagerForTest(c)
		txConfig, err := params.TxConfig(s.mgr.cdc, nil)
		c.Assert(err, IsNil)
		queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
		asgard := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
		c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
		pool.BalanceRune = cosmos.NewUint(4_000_000_000_000)
		pool.LPUnits = cosmos.NewUint(1_000_000_000_000)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		if tc.synth > 0 {
			coin := common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(tc.synth))
			c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
		}
		if tc.ethUSD != "" {
			c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: "ETH", Price: tc.ethUSD}), IsNil)
		}
		if tc.runeUSD != "" {
			c.Assert(mgr.Keeper().SetPrice(ctx, OraclePrice{Symbol: "RUNE", Price: tc.runeUSD}), IsNil)
		}

		one, err := queryServer.Pool(ctx, &types.QueryPoolRequest{Asset: "ETH.ETH"})
		c.Assert(err, IsNil)
		all, err := queryServer.Pools(ctx, &types.QueryPoolsRequest{})
		c.Assert(err, IsNil)
		c.Assert(all.Pools, HasLen, 1)

		for _, p := range []*types.QueryPoolResponse{one, all.Pools[0]} {
			cm := Commentf("%s: %+v", tc.name, p)
			m := poolJSONKeys(c, p)
			if tc.wantEmpty {
				for _, k := range fairValueJSONKeys {
					_, ok := m[k]
					c.Check(ok, Equals, false, Commentf("%s: unexpected %s", tc.name, k))
				}
				continue
			}
			c.Check(p.LpUnitFairValueRune, Equals, tc.want.fairRune, cm)
			c.Check(p.LpUnitFairValueAsset, Equals, tc.want.fairAsset, cm)
			c.Check(p.LpUnitSpotValueRune, Equals, tc.want.spotRune, cm)
			c.Check(p.LpFairSpotDeviationBps, Equals, tc.want.devBps, cm)
			c.Check(p.FairValueOraclePriceRune, Equals, tc.want.price, cm)
			c.Check(p.LpFairValueOverhang, Equals, tc.want.overhang, cm)
			for _, k := range []string{"lp_unit_fair_value_rune", "lp_unit_fair_value_asset", "lp_unit_spot_value_rune", "fair_value_oracle_price_rune"} {
				_, ok := m[k]
				c.Check(ok, Equals, true, Commentf("%s: missing %s", tc.name, k))
			}
		}
	}
}

// A sandwich through the real swapper moves the spot LP unit value but leaves
// the fair (oracle-priced) one unchanged up to rounding: the liquidity fee goes
// to the Reserve, so sqrt(A*R) does not move.
func (s *QuerierSuite) TestQueryPoolFairValueIgnoresSandwich(c *C) {
	for _, r := range []luviRegime{luviTestRegimes[0], luviTestRegimes[1], luviTestRegimes[2]} {
		e := newLuviEnv(c, r)
		txConfig, err := params.TxConfig(s.mgr.cdc, nil)
		c.Assert(err, IsNil)
		qs := NewQueryServerImpl(e.mgr, txConfig, s.kb)
		// spot price R/A = 100 RUNE per ETH; the oracle agrees
		c.Assert(e.mgr.Keeper().SetPrice(e.ctx, OraclePrice{Symbol: "ETH", Price: "250"}), IsNil)
		c.Assert(e.mgr.Keeper().SetPrice(e.ctx, OraclePrice{Symbol: "RUNE", Price: "2.5"}), IsNil)

		query := func() *types.QueryPoolResponse {
			p, qerr := qs.Pool(e.ctx, &types.QueryPoolRequest{Asset: e.asset.String()})
			c.Assert(qerr, IsNil)
			c.Assert(p.LpUnitFairValueRune, Not(Equals), "")
			return p
		}
		dec := sdkmath.LegacyMustNewDecFromStr
		closeTo := func(a, b string, what string) {
			diff := dec(a).Sub(dec(b)).Abs()
			c.Check(diff.LTE(dec(b).Quo(cosmos.NewDec(1_000_000_000))), Equals, true,
				Commentf("%s %s: %s vs %s", r.name, what, a, b))
		}

		before := query()
		c.Check(before.LpFairSpotDeviationBps, Equals, "0", Commentf(r.name))

		// front-run: push the pool price up ~44%
		e.swapRuneToAsset(luviPoolRune / 5)
		during := query()
		c.Check(during.LpUnitSpotValueRune, Not(Equals), before.LpUnitSpotValueRune, Commentf(r.name))
		closeTo(during.LpUnitFairValueRune, before.LpUnitFairValueRune, "fair value during")
		dev, ok := sdkmath.NewIntFromString(during.LpFairSpotDeviationBps)
		c.Assert(ok, Equals, true)
		c.Check(dev.Abs().GT(sdkmath.NewInt(100)), Equals, true, Commentf("%s: deviation %s", r.name, dev))

		// back-run: sell the asset back
		e.swapAssetToRune(luviPoolAsset / 6)
		after := query()
		closeTo(after.LpUnitFairValueRune, before.LpUnitFairValueRune, "fair value after")
	}
}
