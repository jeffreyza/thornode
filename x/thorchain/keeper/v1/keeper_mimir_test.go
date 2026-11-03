package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/constants"
	keeperpkg "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"

	. "gopkg.in/check.v1"
)

type KeeperMimirSuite struct{}

var _ = Suite(&KeeperMimirSuite{})

func (s *KeeperMimirSuite) TestMimir(c *C) {
	ctx, k := setupKeeperForTest(c)

	k.SetMimir(ctx, "foo", 14)

	val, err := k.GetMimir(ctx, "foo")
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(14))

	val, err = k.GetMimir(ctx, "bogus")
	c.Assert(err, IsNil)
	c.Check(val, Equals, int64(-1))
}

func (s *KeeperMimirSuite) TestScopedMimirCacheIsSnapshot(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "foo", 14)
	cachedCtx := keeperpkg.WithMimirCache(ctx)

	value, err := k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(14))

	// The scoped context retains its first read, while an uncached context sees
	// the updated store value.
	k.SetMimir(ctx, "foo", 15)
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(14))
	value, err = k.GetMimir(ctx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(15))
}

func (s *KeeperMimirSuite) TestScopedMimirCacheDisabledByWrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "HaltTrading", 0)
	k.SetMimir(ctx, "foo", 7)
	cachedCtx := keeperpkg.WithMimirCache(ctx)

	value, err := k.GetMimir(cachedCtx, "HaltTrading")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(0))

	// A write through a context carrying the cache (as the MaxRuneSupply circuit
	// breaker does mid-EndBlock) must be visible to every later read.
	k.SetMimir(cachedCtx, "HaltTrading", 1)
	value, err = k.GetMimir(cachedCtx, "HaltTrading")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(1))

	// A write inside a discarded cache context must not be snapshotted into the
	// shared cache and leak to the parent context.
	childCtx, _ := cachedCtx.CacheContext()
	k.SetMimir(childCtx, "foo", 8)
	value, err = k.GetMimir(childCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(8))
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(7))

	// DeleteMimir disables a fresh cache too.
	cachedCtx = keeperpkg.WithMimirCache(ctx)
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(7))
	c.Assert(k.DeleteMimir(cachedCtx, "foo"), IsNil)
	value, err = k.GetMimir(cachedCtx, "foo")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(-1))
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveBlackList(c *C) {
	_, k := setupKeeperForTest(c)

	// Per-pool POL reserve blacklist must be operational so operators can
	// quickly disable a misbehaving pool without full governance consensus.
	c.Check(k.IsOperationalMimir("POLReserveBlacklist-BTC-BTC"), Equals, true)
	c.Check(
		k.IsOperationalMimir("POLReserveBlacklist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
		Equals, true,
	)

	// The partial match is case-insensitive (IsOperationalMimir uppercases the
	// key before comparing), so operator-set keys in any case still register.
	c.Check(k.IsOperationalMimir("POLRESERVEBLACKLIST-BTC-BTC"), Equals, true)
	c.Check(k.IsOperationalMimir("polreserveblacklist-btc-btc"), Equals, true)

	// Similarly-prefixed but unrelated keys must NOT be flagged operational.
	c.Check(k.IsOperationalMimir("POLReserveFoo-BTC-BTC"), Equals, false)
	c.Check(k.IsOperationalMimir("POLReserveDeposit-BTC-BTC"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveSystemIncomeBps(c *C) {
	_, k := setupKeeperForTest(c)

	key := constants.POLReserveSystemIncomeBps.String()
	c.Check(k.IsOperationalMimir(key), Equals, true)
	c.Check(k.IsOperationalMimir(strings.ToUpper(key)), Equals, true)
	c.Check(k.IsOperationalMimir(strings.ToLower(key)), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPOLReserveWhitelist(c *C) {
	_, k := setupKeeperForTest(c)

	c.Check(k.IsOperationalMimir("POLReserveWhitelist-BTC-BTC"), Equals, true)
	c.Check(
		k.IsOperationalMimir("POLReserveWhitelist-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
		Equals, true,
	)
	c.Check(k.IsOperationalMimir("POLRESERVEWHITELIST-BTC-BTC"), Equals, true)
	c.Check(k.IsOperationalMimir("polreservewhitelist-btc-btc"), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirPreflightChainSetDisabled(c *C) {
	_, k := setupKeeperForTest(c)

	// A kill switch for a check that holds nodes out of the Ready set must be
	// reachable at operational threshold if the check misfires during a churn.
	c.Check(k.IsOperationalMimir(constants.MimirKeyPreflightChainSetDisabled), Equals, true)
	c.Check(k.IsOperationalMimir(strings.ToUpper(constants.MimirKeyPreflightChainSetDisabled)), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirCompromisedVault(c *C) {
	_, k := setupKeeperForTest(c)

	c.Check(k.IsOperationalMimir("CompromisedVault-tthorpub1addwnpepq0"), Equals, true)
	c.Check(k.IsOperationalMimir("COMPROMISEDVAULT-TTHORPUB1ADDWNPEPQ0"), Equals, true)
	c.Check(k.IsOperationalMimir("compromisedvault-tthorpub1addwnpepq0"), Equals, true)
	c.Check(k.IsOperationalMimir("CompromisedVault"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirOverSolvencyToTreasuryBps(c *C) {
	_, k := setupKeeperForTest(c)

	c.Check(k.IsOperationalMimir("OverSolvencyToTreasuryBps-BTC-BTC"), Equals, true)
	c.Check(
		k.IsOperationalMimir("OverSolvencyToTreasuryBps-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"),
		Equals, true,
	)
	c.Check(k.IsOperationalMimir("OVERSOLVENCYTOTREASURYBPS-ETH-ETH"), Equals, true)
	c.Check(k.IsOperationalMimir("oversolvencytotreasurybps-btc-btc"), Equals, true)
	c.Check(k.IsOperationalMimir("OverSolvencyToTreasuryBps"), Equals, true)
	c.Check(k.IsOperationalMimir("OverSolvencyToTreasuryFoo-BTC-BTC"), Equals, false)
}

func (s *KeeperMimirSuite) TestDisableTSSSignerFilteringIsOperationalMimir(c *C) {
	_, k := setupKeeperForTest(c)

	key := constants.MimirKeyDisableTSSSignerFiltering
	c.Check(k.IsOperationalMimir(key), Equals, true)
	c.Check(k.IsOperationalMimir(strings.ToUpper(key)), Equals, true)
	c.Check(k.IsOperationalMimir(strings.ToLower(key)), Equals, true)
	c.Check(k.IsOperationalMimir(key+"-BTC"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirSkipSignerCache(c *C) {
	_, k := setupKeeperForTest(c)

	// SkipSignerCache<Chain> is the per-chain bifrost gate that authorizes the
	// expired-cache verification path (and a possible cache clear). Every node
	// in the network MUST observe the same gate value at the same height —
	// otherwise some bifrosts re-broadcast a tx while others don't, splitting
	// observation consensus. Marking it operational forces consensus storage,
	// closing the operator-misconfiguration divergence vector.
	c.Check(k.IsOperationalMimir("SkipSignerCacheBTC"), Equals, true)
	c.Check(k.IsOperationalMimir("SkipSignerCacheETH"), Equals, true)
	c.Check(k.IsOperationalMimir("SkipSignerCacheXRP"), Equals, true)

	// Case-insensitive (partial match uppercases the key).
	c.Check(k.IsOperationalMimir("skipsignercacheBTC"), Equals, true)
	c.Check(k.IsOperationalMimir("SKIPSIGNERCACHEBTC"), Equals, true)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirAllowNoVault(c *C) {
	_, k := setupKeeperForTest(c)

	// AllowNoVault-<CHAIN> gates noop:novault debits for a short correction
	// window; operators must be able to open it by plurality, not 2/3. Derive
	// the key from the template so a renamed constant fails here, not on mainnet.
	c.Check(k.IsOperationalMimir(fmt.Sprintf(constants.MimirTemplateAllowNoVault, "TRON")), Equals, true)
	c.Check(k.IsOperationalMimir("ALLOWNOVAULT-BSC"), Equals, true)
	c.Check(k.IsOperationalMimir("allownovault-eth"), Equals, true)

	// Unrelated keys stay non-operational.
	c.Check(k.IsOperationalMimir("NoVaultAllowed-TRON"), Equals, false)
	c.Check(k.IsOperationalMimir("AllowNoVault"), Equals, false)
}

func (s *KeeperMimirSuite) TestIsOperationalMimirXMRConsolidation(c *C) {
	_, k := setupKeeperForTest(c)

	// Keys read by bifrost's Monero client (bifrost/pkg/chainclients/monero/client.go).
	// Consolidation locks vault liquidity for Monero's 10-block spend lock, so
	// operators must be able to tune or disable it by plurality, not 2/3.
	keys := []string{
		"XMRConsolidationEnabled",
		"XMRConsolidationCheckIntervalBlocks",
		"XMRConsolidationCooldownBlocks",
		"XMRConsolidationMinUnlockedOutputs",
		"XMRConsolidationTargetOutputs",
		"XMRConsolidationMinUnlockedAtomic",
		"XMRConsolidationReserveBps",
		"XMRConsolidationChurnLookaheadBlocks",
		"XMRConsolidationMaxRetries",
		"XMRConsolidationMaxInputs",
		"XMRConsolidationMaxLockBps",
	}
	for _, key := range keys {
		c.Check(k.IsOperationalMimir(key), Equals, true, Commentf("key=%s", key))
		c.Check(k.IsOperationalMimir(strings.ToUpper(key)), Equals, true, Commentf("key=%s", key))
		c.Check(k.IsOperationalMimir(strings.ToLower(key)), Equals, true, Commentf("key=%s", key))
	}
	c.Check(k.IsOperationalMimir("xmrCONSOLIDATIONmaxLOCKbps"), Equals, true)

	// The UTXO input cap shared with the other UTXO chains stays economic, as do
	// keys that merely mention XMR or consolidation.
	c.Check(k.IsOperationalMimir("MaxUTXOsToSpend"), Equals, false)
	c.Check(k.IsOperationalMimir("XMRConsolidatio"), Equals, false)
	c.Check(k.IsOperationalMimir("ConsolidationXMREnabled"), Equals, false)
	c.Check(k.IsOperationalMimir("BTCConsolidationEnabled"), Equals, false)
}
