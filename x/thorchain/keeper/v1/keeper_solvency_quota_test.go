package keeperv1

import (
	"cosmossdk.io/log"
	"cosmossdk.io/store"
	storemetrics "cosmossdk.io/store/metrics"
	dbm "github.com/cosmos/cosmos-db"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type SolvencyQuotaKeeperSuite struct{}

var _ = Suite(&SolvencyQuotaKeeperSuite{})

func (s *SolvencyQuotaKeeperSuite) TestPersistsAndOverwritesOneRecord(c *C) {
	ctx, k := setupKeeperForTest(c)
	// The general fixture shares one backing DB across multiple mounted stores
	// and is only used without commits. Use a dedicated store for restart QA.
	db := dbm.NewMemDB()
	key := cosmos.NewKVStoreKey(StoreKey)
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), storemetrics.NewNoOpMetrics())
	ms.MountStoreWithDB(key, cosmos.StoreTypeIAVL, nil)
	c.Assert(ms.LoadLatestVersion(), IsNil)
	ctx = ctx.WithMultiStore(ms)
	k.storeKey = key
	signer := GetRandomBech32Addr()
	window, used, err := k.GetSolvencyVoterQuota(ctx, common.TRONChain, signer)
	c.Assert(err, IsNil)
	c.Assert(window, Equals, uint64(0))
	c.Assert(used, Equals, uint64(0))
	for nextWindow := uint64(0); nextWindow < 100; nextWindow++ {
		k.SetSolvencyVoterQuota(ctx, common.TRONChain, signer, nextWindow, 12)
	}
	iter := k.getIterator(ctx, prefixSolvencyVoterQuota)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
		c.Check(len(iter.Value()), Equals, 16)
	}
	c.Assert(iter.Close(), IsNil)
	c.Assert(count, Equals, 1)

	// Commit/reload the store and instantiate a fresh keeper, as on restart.
	ms.Commit()
	reloaded := store.NewCommitMultiStore(db, log.NewNopLogger(), storemetrics.NewNoOpMetrics())
	reloaded.MountStoreWithDB(key, cosmos.StoreTypeIAVL, nil)
	c.Assert(reloaded.LoadLatestVersion(), IsNil)
	ctx = ctx.WithMultiStore(reloaded)
	fresh := NewKVStore(k.cdc, k.storeKey, k.coinKeeper, k.accountKeeper, k.upgradeKeeper, k.version)
	window, used, err = fresh.GetSolvencyVoterQuota(ctx, common.TRONChain, signer)
	c.Assert(err, IsNil)
	c.Assert(window, Equals, uint64(99))
	c.Assert(used, Equals, uint64(12))
	window, used, err = fresh.GetSolvencyVoterQuota(ctx, common.TRONChain, GetRandomBech32Addr())
	c.Assert(err, IsNil)
	c.Assert(window, Equals, uint64(0))
	c.Assert(used, Equals, uint64(0))
}

func (s *SolvencyQuotaKeeperSuite) TestIndependentChainsAndValidatorsWithCanonicalCasing(c *C) {
	ctx, k := setupKeeperForTest(c)
	signer, otherSigner := GetRandomBech32Addr(), GetRandomBech32Addr()
	k.SetSolvencyVoterQuota(ctx, common.ETHChain, signer, 10, 3)
	k.SetSolvencyVoterQuota(ctx, common.BTCChain, signer, 20, 7)
	k.SetSolvencyVoterQuota(ctx, common.ETHChain, otherSigner, 30, 9)
	// A case variant updates the same allowance instead of creating a fresh one.
	k.SetSolvencyVoterQuota(ctx, common.Chain("eth"), signer, 11, 4)
	for _, tc := range []struct {
		chain        common.Chain
		signer       cosmos.AccAddress
		window, used uint64
	}{
		{common.ETHChain, signer, 11, 4},
		{common.Chain("eth"), signer, 11, 4},
		{common.BTCChain, signer, 20, 7},
		{common.ETHChain, otherSigner, 30, 9},
		{common.BTCChain, otherSigner, 0, 0},
	} {
		window, used, err := k.GetSolvencyVoterQuota(ctx, tc.chain, tc.signer)
		c.Assert(err, IsNil)
		c.Check(window, Equals, tc.window)
		c.Check(used, Equals, tc.used)
	}
	iter := k.getIterator(ctx, prefixSolvencyVoterQuota)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Assert(count, Equals, 3)
}

func (s *SolvencyQuotaKeeperSuite) TestCorruptRecordFailsClosed(c *C) {
	ctx, k := setupKeeperForTest(c)
	signer := GetRandomBech32Addr()
	kv := k.kvStore(ctx)
	for _, size := range []int{1, 15, 17} {
		kv.Set(k.GetKey(prefixSolvencyVoterQuota, common.TRONChain.String()+"-"+signer.String()), make([]byte, size))
		_, _, err := k.GetSolvencyVoterQuota(ctx, common.TRONChain, signer)
		c.Assert(err, NotNil)
	}
}

func (s *SolvencyQuotaKeeperSuite) TestQuotaSharesVoterTransactionCache(c *C) {
	ctx, k := setupKeeperForTest(c)
	signer := GetRandomBech32Addr()
	cache, _ := ctx.CacheContext()
	k.SetSolvencyVoterQuota(cache, common.TRONChain, signer, 10, 1)
	_, used, err := k.GetSolvencyVoterQuota(ctx, common.TRONChain, signer)
	c.Assert(err, IsNil)
	c.Assert(used, Equals, uint64(0))
	cache, commit := ctx.CacheContext()
	k.SetSolvencyVoterQuota(cache, common.TRONChain, signer, 10, 1)
	commit()
	_, used, err = k.GetSolvencyVoterQuota(ctx, common.TRONChain, signer)
	c.Assert(err, IsNil)
	c.Assert(used, Equals, uint64(1))
}
