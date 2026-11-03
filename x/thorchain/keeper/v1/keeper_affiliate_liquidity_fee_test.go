package keeperv1

import (
	"math"
	"math/rand"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type KeeperAffiliateLiquidityFeeSuite struct{}

var _ = Suite(&KeeperAffiliateLiquidityFeeSuite{})

func (s *KeeperAffiliateLiquidityFeeSuite) TestAddGetDelete(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.NewUint(100)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.NewUint(50)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "bar", cosmos.NewUint(75)), IsNil)

	got := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 2)
	// Order is lexicographic (BAR before FOO).
	c.Assert(got[0].Thorname, Equals, "BAR")
	c.Assert(got[0].Fee.Equal(cosmos.NewUint(75)), Equals, true)
	c.Assert(got[1].Thorname, Equals, "FOO")
	c.Assert(got[1].Fee.Equal(cosmos.NewUint(150)), Equals, true)

	// Zero fee is a no-op.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.ZeroUint()), IsNil)
	got = k.GetAffiliateLiquidityFees(ctx)
	c.Assert(got[1].Fee.Equal(cosmos.NewUint(150)), Equals, true)

	k.DeleteAffiliateLiquidityFees(ctx)
	got = k.GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 0)
}

func (s *KeeperAffiliateLiquidityFeeSuite) TestAddAffiliateLiquidityFeeFeeOverflowsUint64(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Fee whose underlying big.Int does not fit in uint64 must be
	// rejected so the accumulator never silently truncates input.
	overflow := cosmos.NewUintFromBigInt(cosmos.NewUint(math.MaxUint64).BigInt()).Add(cosmos.NewUint(1))
	err := k.AddAffiliateLiquidityFee(ctx, "foo", overflow)
	c.Assert(err, NotNil, Commentf("expected overflow error for fee > MaxUint64"))

	// And no entry should have been written for this thorname.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 0)
}

func (s *KeeperAffiliateLiquidityFeeSuite) TestGetAffiliateLiquidityFeesSkipsCorruptedEntry(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Seed two valid sibling entries that bracket a corrupted entry in
	// lexicographic order: A_GOOD, B_BAD (corrupted bytes), C_GOOD.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "A_GOOD", cosmos.NewUint(100)), IsNil)
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "C_GOOD", cosmos.NewUint(300)), IsNil)

	// Write raw garbage at the B_BAD key so getUint64 will fail to
	// Unmarshal into ProtoUint64. This simulates state corruption (or a
	// future encoding skew) for a single accumulator entry.
	store := k.kvStore(ctx)
	store.Set(k.GetKey(prefixAffiliateLiquidityFee, "B_BAD"), []byte{0xff, 0xff, 0xff, 0xff})

	// One bad entry must not cause every sibling's attribution to be
	// silently discarded — the keeper must skip the bad entry, log, and
	// return the readable entries.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 2)
	c.Assert(got[0].Thorname, Equals, "A_GOOD")
	c.Assert(got[0].Fee.Equal(cosmos.NewUint(100)), Equals, true)
	c.Assert(got[1].Thorname, Equals, "C_GOOD")
	c.Assert(got[1].Fee.Equal(cosmos.NewUint(300)), Equals, true)
}

func (s *KeeperAffiliateLiquidityFeeSuite) TestGetAffiliateLiquidityFeesOrderingDeterministic(c *C) {
	// processRevShare relies on GetAffiliateLiquidityFees returning entries
	// in a stable lexicographic order across nodes — non-determinism here
	// causes a chain halt. Insert thornames in randomised order and assert
	// every adjacent pair is strictly lex-ordered, repeated across several
	// independent permutations.
	names := []string{"alpha", "BRAVO", "charlie", "delta", "Echo", "foxtrot", "golf", "hotel"}

	// GetKey upper-cases keys before storage; the keeper returns the
	// stored (upper-cased) form, so build the expected ordering from that.
	expected := make([]string, len(names))
	for i, n := range names {
		expected[i] = strings.ToUpper(n)
	}
	sort.Strings(expected)

	for _, seed := range []int64{1, 2, 3, 42, 1000} {
		ctx, k := setupKeeperForTest(c)

		shuffled := append([]string(nil), names...)
		rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic seed is the point of this test
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		for i, n := range shuffled {
			c.Assert(k.AddAffiliateLiquidityFee(ctx, n, cosmos.NewUint(uint64(i+1)*100)), IsNil)
		}

		got := k.GetAffiliateLiquidityFees(ctx)
		c.Assert(got, HasLen, len(names))

		gotNames := make([]string, len(got))
		for i, e := range got {
			gotNames[i] = e.Thorname
		}
		c.Assert(gotNames, DeepEquals, expected,
			Commentf("seed=%d insertion order=%v", seed, shuffled))

		for i := 0; i+1 < len(got); i++ {
			c.Assert(got[i].Thorname < got[i+1].Thorname, Equals, true,
				Commentf("seed=%d adjacent pair not strictly ordered: %q vs %q", seed, got[i].Thorname, got[i+1].Thorname))
		}
	}
}

func (s *KeeperAffiliateLiquidityFeeSuite) TestAddAffiliateLiquidityFeeSumOverflow(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Seed the accumulator near MaxUint64 so the next add would wrap.
	c.Assert(k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.NewUint(math.MaxUint64-10)), IsNil)

	// Adding 11 would overflow uint64 — must return an error rather
	// than silently wrapping.
	err := k.AddAffiliateLiquidityFee(ctx, "foo", cosmos.NewUint(11))
	c.Assert(err, NotNil, Commentf("expected overflow error when current+fee > MaxUint64"))

	// The seeded value must be unchanged after the rejected add.
	got := k.GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].Fee.Equal(cosmos.NewUint(math.MaxUint64-10)), Equals, true)
}
