package keeperv1

import (
	"strings"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"
)

type KeeperDynamicFeeSuite struct{}

var _ = Suite(&KeeperDynamicFeeSuite{})

func (s *KeeperDynamicFeeSuite) TestDynamicFeeRecord(c *C) {
	ctx, k := setupKeeperForTest(c)

	rec := DynamicFeeRecord{
		Thorname:        "myname",
		PairAssetA:      "BTC.BTC",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      42,
		LastActiveEpoch: 7,
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	// Casing in the query should not matter for storage — Key() uppercases Thorname.
	got, err := k.GetDynamicFeeRecord(ctx, "MYNAME", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(got.DynamicBps, Equals, uint64(42))
	c.Check(got.LastActiveEpoch, Equals, uint64(7))

	// Missing record returns zero-value with Thorname/PairAssetA/PairAssetB populated and no error.
	missing, err := k.GetDynamicFeeRecord(ctx, "nope", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(missing.DynamicBps, Equals, uint64(0))
	c.Check(missing.Thorname, Equals, "nope")

	iter := k.GetDynamicFeeRecordIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	iter.Close()
	c.Check(count, Equals, 1)

	c.Assert(k.DeleteDynamicFeeRecord(ctx, "myname", "BTC.BTC", "THOR.RUNE"), IsNil)
	got, err = k.GetDynamicFeeRecord(ctx, "myname", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(got.DynamicBps, Equals, uint64(0))
}

func (s *KeeperDynamicFeeSuite) TestDynamicFeeAccumulator(c *C) {
	ctx, k := setupKeeperForTest(c)

	acc := NewDynamicFeeAccumulator("myname", "BTC.BTC", "THOR.RUNE")
	acc.VolumeTor = math.NewUint(1000)
	acc.FeesTor = math.NewUint(50)
	c.Assert(k.SetDynamicFeeAccumulator(ctx, acc), IsNil)

	got, err := k.GetDynamicFeeAccumulator(ctx, "myname", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(got.VolumeTor.Uint64(), Equals, uint64(1000))
	c.Check(got.FeesTor.Uint64(), Equals, uint64(50))

	// Missing accumulator returns a fresh zero-valued accumulator.
	missing, err := k.GetDynamicFeeAccumulator(ctx, "nope", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(missing.VolumeTor.Uint64(), Equals, uint64(0))

	iter := k.GetDynamicFeeAccumulatorIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	iter.Close()
	c.Check(count, Equals, 1)

	c.Assert(k.DeleteDynamicFeeAccumulator(ctx, "myname", "BTC.BTC", "THOR.RUNE"), IsNil)
	got, err = k.GetDynamicFeeAccumulator(ctx, "myname", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(got.VolumeTor.Uint64(), Equals, uint64(0))
}

func (s *KeeperDynamicFeeSuite) TestDeleteAllDynamicFeeStateForThorname(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Store records using mixed casing to verify case-insensitive matching.
	recA := DynamicFeeRecord{Thorname: "MyName", PairAssetA: "BTC.BTC", PairAssetB: "THOR.RUNE", DynamicBps: 10}
	recB := DynamicFeeRecord{Thorname: "myname", PairAssetA: "ETH.ETH", PairAssetB: "THOR.RUNE", DynamicBps: 20}
	recOther := DynamicFeeRecord{Thorname: "other", PairAssetA: "BTC.BTC", PairAssetB: "THOR.RUNE", DynamicBps: 30}
	c.Assert(k.SetDynamicFeeRecord(ctx, recA), IsNil)
	c.Assert(k.SetDynamicFeeRecord(ctx, recB), IsNil)
	c.Assert(k.SetDynamicFeeRecord(ctx, recOther), IsNil)

	accA := NewDynamicFeeAccumulator("MYNAME", "BTC.BTC", "THOR.RUNE")
	accA.VolumeTor = math.NewUint(100)
	accOther := NewDynamicFeeAccumulator("other", "ETH.ETH", "THOR.RUNE")
	accOther.VolumeTor = math.NewUint(200)
	c.Assert(k.SetDynamicFeeAccumulator(ctx, accA), IsNil)
	c.Assert(k.SetDynamicFeeAccumulator(ctx, accOther), IsNil)

	// Delete with lowercase input — should match both "MyName" and "myname" records.
	c.Assert(k.DeleteAllDynamicFeeStateForThorname(ctx, "myname"), IsNil)

	// Verify both of MyName's records are gone.
	gotA, err := k.GetDynamicFeeRecord(ctx, "MyName", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(gotA.DynamicBps, Equals, uint64(0))
	gotB, err := k.GetDynamicFeeRecord(ctx, "myname", "ETH.ETH", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(gotB.DynamicBps, Equals, uint64(0))

	// Other thorname's record is untouched.
	gotOther, err := k.GetDynamicFeeRecord(ctx, "other", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(gotOther.DynamicBps, Equals, uint64(30))

	// Accumulator for MYNAME removed, other's accumulator untouched.
	gotAccA, err := k.GetDynamicFeeAccumulator(ctx, "MYNAME", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(gotAccA.VolumeTor.Uint64(), Equals, uint64(0))
	gotAccOther, err := k.GetDynamicFeeAccumulator(ctx, "other", "ETH.ETH", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(gotAccOther.VolumeTor.Uint64(), Equals, uint64(200))
}

func (s *KeeperDynamicFeeSuite) TestGetDynamicFeeRecordIteratorForThorname(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Seed records for three thornames across two pairs each.
	for _, tn := range []string{"alpha", "beta", "gamma"} {
		for _, pair := range [][2]string{{"BTC.BTC", "THOR.RUNE"}, {"ETH.ETH", "THOR.RUNE"}} {
			rec := DynamicFeeRecord{Thorname: tn, PairAssetA: pair[0], PairAssetB: pair[1], DynamicBps: 10}
			c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)
		}
	}

	// Scoped iterator for "beta" (lowercase input) should yield exactly 2 records,
	// all belonging to "BETA" after uppercasing.
	iter := k.GetDynamicFeeRecordIteratorForThorname(ctx, "beta")
	defer iter.Close()

	seen := 0
	for ; iter.Valid(); iter.Next() {
		var rec DynamicFeeRecord
		c.Assert(k.cdc.Unmarshal(iter.Value(), &rec), IsNil)
		c.Check(strings.ToUpper(rec.Thorname), Equals, "BETA")
		seen++
	}
	c.Check(seen, Equals, 2)

	// Unknown thorname yields zero records.
	empty := k.GetDynamicFeeRecordIteratorForThorname(ctx, "nobody")
	defer empty.Close()
	emptyCount := 0
	for ; empty.Valid(); empty.Next() {
		emptyCount++
	}
	c.Check(emptyCount, Equals, 0)
}

func (s *KeeperDynamicFeeSuite) TestDeleteAllDynamicFeeStateForThornameRecordWithoutAccumulator(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Record exists but no accumulator (the steady state mid-epoch after a seal).
	rec := DynamicFeeRecord{Thorname: "myname", PairAssetA: "BTC.BTC", PairAssetB: "THOR.RUNE", DynamicBps: 42}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	c.Assert(k.DeleteAllDynamicFeeStateForThorname(ctx, "myname"), IsNil)

	got, err := k.GetDynamicFeeRecord(ctx, "myname", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(got.DynamicBps, Equals, uint64(0), Commentf("record should be deleted even when no accumulator exists"))
}

func (s *KeeperDynamicFeeSuite) TestGetRemovedDynamicFeeWhitelistThornames(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Seed whitelist mimirs: two active, one accrual-only, two removed (state 0, -1).
	k.SetMimir(ctx, "DYNAMICFEE-WHITELIST-ACTIVEA", 1)
	k.SetMimir(ctx, "DYNAMICFEE-WHITELIST-ACTIVEB", 1)
	k.SetMimir(ctx, "DYNAMICFEE-WHITELIST-ACCRUING", 2)
	k.SetMimir(ctx, "DYNAMICFEE-WHITELIST-REMOVED1", 0)
	k.SetMimir(ctx, "DYNAMICFEE-WHITELIST-REMOVED2", -1)

	// Seed unrelated DynamicFee mimirs that must NOT be matched by the prefix.
	k.SetMimir(ctx, "L1DynamicFeeEnabled", 1)
	k.SetMimir(ctx, "L1DynamicFeeEpochBlocks", 10)

	got := k.GetRemovedDynamicFeeWhitelistThornames(ctx)

	// Order is prefix-iterator-order (lexicographic over full key); verify
	// set membership rather than order.
	gotSet := map[string]bool{}
	for _, tn := range got {
		gotSet[tn] = true
	}
	c.Check(len(got), Equals, 2, Commentf("expected exactly the two removed entries, got %v", got))
	c.Check(gotSet["REMOVED1"], Equals, true)
	c.Check(gotSet["REMOVED2"], Equals, true)
	c.Check(gotSet["ACTIVEA"], Equals, false)
	c.Check(gotSet["ACTIVEB"], Equals, false)
	c.Check(gotSet["ACCRUING"], Equals, false)
}
