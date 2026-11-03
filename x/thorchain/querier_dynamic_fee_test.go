package thorchain

import (
	"cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type QuerierDynamicFeeSuite struct{}

var _ = Suite(&QuerierDynamicFeeSuite{})

func (s *QuerierDynamicFeeSuite) TestQueryDynamicL1FeesEmpty(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	resp, err := qs.queryDynamicL1Fees(ctx, &types.QueryDynamicL1FeesRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Assert(len(resp.Entries), Equals, 0)
}

func (s *QuerierDynamicFeeSuite) TestQueryDynamicL1FeesReturnsAllRecords(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	// Two thornames, with whitelist state for only one
	registerThorname(ctx, k, "alice", 1) // active
	registerThorname(ctx, k, "bob", 0)   // not enrolled

	recA := types.DynamicFeeRecord{
		Thorname:        "alice",
		PairAssetA:      "BTC.BTC",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      42,
		LastActiveEpoch: 7,
		History: []types.DynamicFeeEpochRecord{
			{
				Epoch:      5,
				VolumeTor:  math.NewUint(1000),
				FeesTor:    math.NewUint(10),
				BpsAtClose: 40,
			},
			{
				Epoch:      7,
				VolumeTor:  math.NewUint(2000),
				FeesTor:    math.NewUint(20),
				BpsAtClose: 42,
			},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, recA), IsNil)

	recB := types.DynamicFeeRecord{
		Thorname:        "bob",
		PairAssetA:      "ETH.ETH",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      7,
		LastActiveEpoch: 3,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, recB), IsNil)

	resp, err := qs.queryDynamicL1Fees(ctx, &types.QueryDynamicL1FeesRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Assert(len(resp.Entries), Equals, 2)

	byThorname := map[string]types.DynamicL1FeeEntry{}
	for _, e := range resp.Entries {
		byThorname[e.Thorname] = e
	}

	alice, ok := byThorname["alice"]
	c.Assert(ok, Equals, true)
	c.Check(alice.Pair, Equals, "BTC.BTC|THOR.RUNE")
	c.Check(alice.DynamicBps, Equals, uint64(42))
	c.Check(alice.WhitelistState, Equals, int64(1))
	c.Check(alice.LastActiveEpoch, Equals, uint64(7))
	c.Check(alice.LatestFeesTor, Equals, "20")

	bob, ok := byThorname["bob"]
	c.Assert(ok, Equals, true)
	c.Check(bob.Pair, Equals, "ETH.ETH|THOR.RUNE")
	c.Check(bob.DynamicBps, Equals, uint64(7))
	// No mimir entry -> GetMimir returns -1, which dynamicFeeWhitelistState forwards.
	c.Check(bob.WhitelistState, Equals, int64(-1))
	c.Check(bob.LastActiveEpoch, Equals, uint64(3))
	c.Check(bob.LatestFeesTor, Equals, "")
}

func (s *QuerierDynamicFeeSuite) TestQueryDynamicL1FeesMonitorWhitelistState(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	registerThorname(ctx, k, "watcher", 2) // accrual-only

	rec := types.DynamicFeeRecord{
		Thorname:   "watcher",
		PairAssetA: "BTC.BTC",
		PairAssetB: "THOR.RUNE",
		DynamicBps: 10,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	resp, err := qs.queryDynamicL1Fees(ctx, &types.QueryDynamicL1FeesRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Entries), Equals, 1)
	c.Check(resp.Entries[0].WhitelistState, Equals, int64(2))
}

func (s *QuerierDynamicFeeSuite) TestQueryDynamicL1FeesByThornameEmpty(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	resp, err := qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{
		Thorname: "missing",
	})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Check(resp.Thorname, Equals, "missing")
	// No mimir entry -> GetMimir returns -1, which dynamicFeeWhitelistState forwards.
	c.Check(resp.WhitelistState, Equals, int64(-1))
	c.Check(len(resp.Pairs), Equals, 0)
}

func (s *QuerierDynamicFeeSuite) TestQueryDynamicL1FeesByThornameFiltersAndMapsHistory(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	registerThorname(ctx, k, "alice", 1)
	registerThorname(ctx, k, "bob", 1)

	aliceBTC := types.DynamicFeeRecord{
		Thorname:        "alice",
		PairAssetA:      "BTC.BTC",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      42,
		LastActiveEpoch: 9,
		History: []types.DynamicFeeEpochRecord{
			{
				Epoch:      8,
				VolumeTor:  math.NewUint(1000),
				FeesTor:    math.NewUint(10),
				BpsAtClose: 40,
			},
			{
				Epoch:      9,
				VolumeTor:  math.NewUint(2500),
				FeesTor:    math.NewUint(25),
				BpsAtClose: 42,
			},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, aliceBTC), IsNil)

	aliceETH := types.DynamicFeeRecord{
		Thorname:        "alice",
		PairAssetA:      "ETH.ETH",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      17,
		LastActiveEpoch: 9,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, aliceETH), IsNil)

	bobBTC := types.DynamicFeeRecord{
		Thorname:        "bob",
		PairAssetA:      "BTC.BTC",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      99,
		LastActiveEpoch: 9,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, bobBTC), IsNil)

	resp, err := qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{
		Thorname: "alice",
	})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Check(resp.Thorname, Equals, "alice")
	c.Check(resp.WhitelistState, Equals, int64(1))
	c.Assert(len(resp.Pairs), Equals, 2)

	byPair := map[string]types.DynamicL1FeePairDetail{}
	for _, p := range resp.Pairs {
		byPair[p.Pair] = p
	}

	btc, ok := byPair["BTC.BTC|THOR.RUNE"]
	c.Assert(ok, Equals, true)
	c.Check(btc.DynamicBps, Equals, uint64(42))
	c.Check(btc.LastActiveEpoch, Equals, uint64(9))
	c.Assert(len(btc.History), Equals, 2)
	c.Check(btc.History[0].Epoch, Equals, uint64(8))
	c.Check(btc.History[0].VolumeTor, Equals, "1000")
	c.Check(btc.History[0].FeesTor, Equals, "10")
	c.Check(btc.History[0].BpsAtClose, Equals, uint64(40))
	c.Check(btc.History[1].Epoch, Equals, uint64(9))
	c.Check(btc.History[1].VolumeTor, Equals, "2500")
	c.Check(btc.History[1].FeesTor, Equals, "25")
	c.Check(btc.History[1].BpsAtClose, Equals, uint64(42))

	eth, ok := byPair["ETH.ETH|THOR.RUNE"]
	c.Assert(ok, Equals, true)
	c.Check(eth.DynamicBps, Equals, uint64(17))
	c.Check(len(eth.History), Equals, 0)
}

func (s *QuerierDynamicFeeSuite) TestQueryDynamicL1FeesByThornameCaseInsensitive(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	registerThorname(ctx, k, "MyTn", 1)

	rec := types.DynamicFeeRecord{
		Thorname:   "MyTn",
		PairAssetA: "BTC.BTC",
		PairAssetB: "THOR.RUNE",
		DynamicBps: 21,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	resp, err := qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{
		Thorname: "mytn",
	})
	c.Assert(err, IsNil)
	c.Check(resp.Thorname, Equals, "mytn")
	c.Check(resp.WhitelistState, Equals, int64(1))
	c.Assert(len(resp.Pairs), Equals, 1)
	c.Check(resp.Pairs[0].DynamicBps, Equals, uint64(21))
}
