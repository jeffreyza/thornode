package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type HandlerObservedTxHelpersSuite struct{}

var _ = Suite(&HandlerObservedTxHelpersSuite{})

func (s *HandlerObservedTxHelpersSuite) TestGenerateReferenceMemoID(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test with 8-decimal asset (BTC)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create observed tx with specific amount
	tx := GetRandomObservedTx()
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))

	refID, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "56789") // last 5 digits of 123456789

	// Test with larger amount
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(987654321)))
	refID, err = generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "54321") // last 5 digits of 987654321

	// Test with amount less than 5 digits
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123)))
	refID, err = generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "00123") // padded to 5 digits
}

func (s *HandlerObservedTxHelpersSuite) TestGenerateReferenceMemoIDWithDifferentDecimals(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test with 6-decimal asset (simulate GAIA)
	gaiaAsset := common.Asset{Chain: common.GAIAChain, Symbol: "ATOM", Ticker: "ATOM", Synth: false}
	gaiaPool := NewPool()
	gaiaPool.Asset = gaiaAsset
	gaiaPool.Decimals = 6 // GAIA has 6 decimals
	gaiaPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	gaiaPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, gaiaPool), IsNil)

	// For 6 decimals, amount should be divided by 100 (10^(8-6))
	tx := GetRandomObservedTx()
	tx.Tx.Coins = common.NewCoins(common.NewCoin(gaiaAsset, cosmos.NewUint(123456780000))) // 1234.56780000 in 6-decimal format

	refID, err := generateReferenceMemoID(ctx, mgr, gaiaAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "67800") // 123456780000 / 100 = 1234567800, last 5 digits = 67800

	// Test with 7-decimal asset
	customAsset := common.Asset{Chain: common.ETHChain, Symbol: "CUSTOM", Ticker: "CUSTOM", Synth: false}
	customPool := NewPool()
	customPool.Asset = customAsset
	customPool.Decimals = 7
	customPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	customPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, customPool), IsNil)

	// For 7 decimals, amount should be divided by 10 (10^(8-7))
	tx.Tx.Coins = common.NewCoins(common.NewCoin(customAsset, cosmos.NewUint(123456780000)))
	refID, err = generateReferenceMemoID(ctx, mgr, customAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "78000") // 123456780000 / 10 = 12345678000, last 5 digits = 78000
}

func (s *HandlerObservedTxHelpersSuite) TestGenerateReferenceMemoIDWithZeroPoolDecimals(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// For non-gas assets, pool decimals of zero means THORChain default precision (1e8).
	asset := common.Asset{Chain: common.ETHChain, Symbol: "TOKEN", Ticker: "TOKEN", Synth: false}
	pool := NewPool()
	pool.Asset = asset
	pool.Decimals = 0
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	tx := GetRandomObservedTx()
	tx.Tx.Coins = common.NewCoins(common.NewCoin(asset, cosmos.NewUint(626500002)))

	refID, err := generateReferenceMemoID(ctx, mgr, asset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "00002")
}

func (s *HandlerObservedTxHelpersSuite) TestGenerateReferenceMemoIDErrorCases(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test with empty coins
	emptyTx := GetRandomObservedTx()
	emptyTx.Tx.Coins = common.NewCoins()
	_, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, emptyTx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*no coins.*")

	// Test with zero amount
	zeroTx := GetRandomObservedTx()
	zeroTx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.ZeroUint()))
	_, err = generateReferenceMemoID(ctx, mgr, common.BTCAsset, zeroTx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*zero amount.*")

	// Test with empty asset
	normalTx := GetRandomObservedTx()
	normalTx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))
	_, err = generateReferenceMemoID(ctx, mgr, common.EmptyAsset, normalTx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*asset is empty.*")
}

func (s *HandlerObservedTxHelpersSuite) TestGenerateReferenceMemoIDDecimalPrecision(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test various decimal combinations
	testCases := []struct {
		decimals int64
		amount   uint64
		expected string
		desc     string
	}{
		{8, 123456789, "56789", "8 decimals, no adjustment"},
		{6, 123456780000, "67800", "6 decimals, divide by 100"},
		{7, 123456780000, "78000", "7 decimals, divide by 10"},
		{5, 123456780000, "56780", "5 decimals, divide by 1000"},
		{4, 123456780000, "45678", "4 decimals, divide by 10000"},
	}

	for i, tc := range testCases {
		// Create custom asset for each test case
		asset := common.Asset{
			Chain:  common.ETHChain,
			Symbol: common.Symbol("TEST" + string(rune('A'+i))),
			Ticker: common.Ticker("TEST" + string(rune('A'+i))),
			Synth:  false,
		}

		pool := NewPool()
		pool.Asset = asset
		pool.Decimals = tc.decimals
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		tx := GetRandomObservedTx()
		tx.Tx.Coins = common.NewCoins(common.NewCoin(asset, cosmos.NewUint(tc.amount)))

		refID, err := generateReferenceMemoID(ctx, mgr, asset, tx)
		c.Assert(err, IsNil, Commentf("Test case: %s", tc.desc))
		c.Assert(refID, Equals, tc.expected, Commentf("Test case: %s", tc.desc))
	}
}

// TestExtractReferenceFromAmountERC20 exercises ExtractReferenceFromAmount directly
// (rather than through the generateReferenceMemoID wrapper) for ETH ERC20 assets at
// both the 6-decimal boundary (USDC, USDT — the "divide to native" branch) and
// 18 decimals (WETH — the "no division" branch). Direct coverage matters because
// ExtractReferenceFromAmount is also called by queryReferenceMemoPreflight, and the
// decimal-aware normalization is the pivotal piece of Path A memoless for ERC20s.
func (s *HandlerObservedTxHelpersSuite) TestExtractReferenceFromAmountERC20(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// --- ETH.USDC at 6 decimals (real mainnet contract) ---
	usdcAsset := common.Asset{
		Chain:  common.ETHChain,
		Symbol: "USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
		Ticker: "USDC",
		Synth:  false,
	}
	usdcPool := NewPool()
	usdcPool.Asset = usdcAsset
	usdcPool.Decimals = 6
	usdcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	usdcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, usdcPool), IsNil)

	// User sends 1.000042 USDC. Native 6-dec = 1_000_042; bifrost normalizes to 1e8
	// precision = 100_004_200. ExtractReferenceFromAmount divides by 100 (= 10^(8-6))
	// to recover the native value, then takes amount % 100_000 = 42 → "00042".
	refID, err := ExtractReferenceFromAmount(ctx, mgr, usdcAsset, 100_004_200)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "00042", Commentf("USDC 1.000042 → ref 00042"))

	// 50.012345 USDC: native 50_012_345, 1e8 = 5_001_234_500, /100 = 50_012_345,
	// % 100_000 = 12345 → "12345".
	refID, err = ExtractReferenceFromAmount(ctx, mgr, usdcAsset, 5_001_234_500)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "12345", Commentf("USDC 50.012345 → ref 12345"))

	// --- ETH.USDT at 6 decimals (different pool, same decimal branch) ---
	usdtAsset := common.Asset{
		Chain:  common.ETHChain,
		Symbol: "USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
		Ticker: "USDT",
		Synth:  false,
	}
	usdtPool := NewPool()
	usdtPool.Asset = usdtAsset
	usdtPool.Decimals = 6
	usdtPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	usdtPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, usdtPool), IsNil)

	// 10.099999 USDT: native 10_099_999, 1e8 = 1_009_999_900, /100 = 10_099_999,
	// % 100_000 = 99999 (max ref) → "99999".
	refID, err = ExtractReferenceFromAmount(ctx, mgr, usdtAsset, 1_009_999_900)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "99999", Commentf("USDT 10.099999 → ref 99999 (max)"))

	// --- ETH.WETH at 18 decimals (no division branch) ---
	wethAsset := common.Asset{
		Chain:  common.ETHChain,
		Symbol: "WETH-0XC02AAA39B223FE8D0A0E5C4F27EAD9083C756CC2",
		Ticker: "WETH",
		Synth:  false,
	}
	wethPool := NewPool()
	wethPool.Asset = wethAsset
	wethPool.Decimals = 18
	wethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	wethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, wethPool), IsNil)

	// For decimals >= 8 the normalization branch is skipped. Bifrost has already
	// scaled 18-dec raw values down to 1e8 precision. The last 5 digits of the
	// 1e8 amount ARE the ref directly.
	//
	// 1.00000000000042 WETH: native 1_000_000_000_000_420_000, 1e8 = 100_000_042,
	// % 100_000 = 42 → "00042".
	refID, err = ExtractReferenceFromAmount(ctx, mgr, wethAsset, 100_000_042)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "00042", Commentf("WETH 1.00000000000042 → ref 00042"))

	// 2.00012345 WETH: 1e8 = 200_012_345, % 100_000 = 12345 → "12345".
	refID, err = ExtractReferenceFromAmount(ctx, mgr, wethAsset, 200_012_345)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "12345", Commentf("WETH 2.00012345 → ref 12345"))

	// --- zero-ref rejection on both decimal branches ---

	// WETH 1.00000000 → 1e8 = 100_000_000 → 100_000_000 % 100_000 = 0 → error.
	_, err = ExtractReferenceFromAmount(ctx, mgr, wethAsset, 100_000_000)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*zero reference.*")

	// USDC 1.000000 → 1e8 = 100_000_000 → /100 = 1_000_000 → % 100_000 = 0 → error.
	_, err = ExtractReferenceFromAmount(ctx, mgr, usdcAsset, 100_000_000)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*zero reference.*")

	// --- sentinel: pool.Decimals=0 for a non-gas asset is treated as 8 ---
	fakeAsset := common.Asset{
		Chain:  common.ETHChain,
		Symbol: "FAKE-0X0000000000000000000000000000000000000000",
		Ticker: "FAKE",
		Synth:  false,
	}
	fakePool := NewPool()
	fakePool.Asset = fakeAsset
	fakePool.Decimals = 0 // sentinel → ExtractReferenceFromAmount substitutes 8
	fakePool.BalanceAsset = cosmos.NewUint(100 * common.One)
	fakePool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, fakePool), IsNil)

	// decimals=8 → no division → 42_004_242 % 100_000 = 4242 → "04242".
	refID, err = ExtractReferenceFromAmount(ctx, mgr, fakeAsset, 42_004_242)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "04242", Commentf("Decimals=0 sentinel treated as 8"))

	// --- error cases ---

	_, err = ExtractReferenceFromAmount(ctx, mgr, common.EmptyAsset, 100_004_242)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*asset is empty.*")

	_, err = ExtractReferenceFromAmount(ctx, mgr, usdcAsset, 0)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*zero amount.*")
}

func (s *HandlerObservedTxHelpersSuite) TestGenerateReferenceMemoIDModulus(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Test that modulus operation works correctly for large numbers
	tx := GetRandomObservedTx()

	// Reference of zero is invalid.
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(9999999900000)))
	refID, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, NotNil)
	c.Assert(refID, Equals, "")

	// Test with amount that results in exactly 99999
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(9999999999999)))
	refID, err = generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "99999") // 9999999999999 % 100000 = 99999
}

func (s *HandlerObservedTxHelpersSuite) TestReferenceMemoIntegration(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Create observed tx with empty memo
	tx := GetRandomObservedTx()
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))

	// Generate reference memo ID
	refID, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "56789")

	// Create ReferenceReadMemo and generate memo string
	refMemo := NewReferenceReadMemo(refID)
	memoStr := refMemo.CreateMemo()
	c.Assert(memoStr, Equals, "r:56789")

	// Create a reference memo in storage that matches our generated reference
	storedRefMemo := NewReferenceMemo(common.BTCAsset, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890", refID, 0)
	mgr.Keeper().SetReferenceMemo(ctx, storedRefMemo)

	// Test that fetchMemoFromReference can resolve our generated reference
	tx.Tx.Memo = memoStr
	resolvedMemo := fetchMemoFromReference(ctx, mgr, common.BTCAsset, tx.Tx, 1) // tx observed at height 1, memo created at height 0
	c.Assert(resolvedMemo, Equals, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890")

	// Resolution is read-only until the transaction is accepted.
	updatedRefMemo, err := mgr.Keeper().GetReferenceMemo(ctx, common.BTCAsset, refID)
	c.Assert(err, IsNil)
	c.Assert(updatedRefMemo.GetUsageCount(), Equals, int64(0))

	trackReferenceMemoUsage(ctx, mgr, common.BTCAsset, tx.Tx.Memo, tx.Tx.ID)
	updatedRefMemo, err = mgr.Keeper().GetReferenceMemo(ctx, common.BTCAsset, refID)
	c.Assert(err, IsNil)
	c.Assert(updatedRefMemo.GetUsageCount(), Equals, int64(1))
	c.Assert(updatedRefMemo.HasBeenUsedBy(tx.Tx.ID), Equals, true)
}

func (s *HandlerObservedTxHelpersSuite) TestReferenceMemoIntegrationWithExpiredMemo(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100) // Current block height

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Set TTL for reference memos
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 50) // TTL of 50 blocks

	// Create observed tx
	tx := GetRandomObservedTx()
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))

	// Generate reference memo ID
	refID, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)

	// Create an expired reference memo (created 60 blocks ago)
	expiredRefMemo := NewReferenceMemo(common.BTCAsset, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890", refID, 40) // 40 + 50 < 100, so expired
	mgr.Keeper().SetReferenceMemo(ctx, expiredRefMemo)

	// Create memo string
	refMemo := NewReferenceReadMemo(refID)
	memoStr := refMemo.CreateMemo()

	// Test that fetchMemoFromReference returns empty string for expired memo
	testTx := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, memoStr)
	resolvedMemo := fetchMemoFromReference(ctx, mgr, common.BTCAsset, testTx, 50) // tx observed at height 50, memo created at height 40
	c.Assert(resolvedMemo, Equals, "")
}

func (s *HandlerObservedTxHelpersSuite) TestReferenceMemoIntegrationWithUsageLimit(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Set usage limit
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnMaxUse.String(), 2) // Max 2 uses

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Create observed tx
	tx := GetRandomObservedTx()
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))

	// Generate reference memo ID
	refID, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)

	// Create reference memo
	storedRefMemo := NewReferenceMemo(common.BTCAsset, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890", refID, 0)
	mgr.Keeper().SetReferenceMemo(ctx, storedRefMemo)

	// Create memo string
	refMemo := NewReferenceReadMemo(refID)
	memoStr := refMemo.CreateMemo()

	// First usage should succeed
	txID1, _ := common.NewTxID("1111111111111111111111111111111111111111111111111111111111111111")
	testTx := common.NewTx(txID1, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, memoStr)
	resolvedMemo := fetchMemoFromReference(ctx, mgr, common.BTCAsset, testTx, 1) // tx observed at height 1, memo created at height 0
	c.Assert(resolvedMemo, Equals, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890")
	trackReferenceMemoUsage(ctx, mgr, common.BTCAsset, testTx.Memo, testTx.ID)

	// Second usage should succeed
	txID2, _ := common.NewTxID("2222222222222222222222222222222222222222222222222222222222222222")
	testTx2 := common.NewTx(txID2, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, memoStr)
	resolvedMemo = fetchMemoFromReference(ctx, mgr, common.BTCAsset, testTx2, 2) // tx observed at height 2, memo created at height 0
	c.Assert(resolvedMemo, Equals, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890")
	trackReferenceMemoUsage(ctx, mgr, common.BTCAsset, testTx2.Memo, testTx2.ID)

	// Third usage should fail (exceed limit)
	txID3, _ := common.NewTxID("3333333333333333333333333333333333333333333333333333333333333333")
	testTx3 := common.NewTx(txID3, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, memoStr)
	resolvedMemo = fetchMemoFromReference(ctx, mgr, common.BTCAsset, testTx3, 3) // tx observed at height 3, memo created at height 0
	c.Assert(resolvedMemo, Equals, "")

	// Only the two accepted transactions consume the reference.
	updatedRefMemo, err := mgr.Keeper().GetReferenceMemo(ctx, common.BTCAsset, refID)
	c.Assert(err, IsNil)
	c.Assert(updatedRefMemo.GetUsageCount(), Equals, int64(2))
}

func (s *HandlerObservedTxHelpersSuite) TestReferenceMemoIntegrationNonExistentReference(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Generate reference memo for non-existent reference
	refMemo := NewReferenceReadMemo("99999") // This reference doesn't exist in storage
	memoStr := refMemo.CreateMemo()

	// Test that fetchMemoFromReference returns empty string for non-existent reference
	testTx := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, memoStr)
	resolvedMemo := fetchMemoFromReference(ctx, mgr, common.BTCAsset, testTx, 1) // tx observed at height 1, reference doesn't exist
	c.Assert(resolvedMemo, Equals, "")
}

func (s *HandlerObservedTxHelpersSuite) TestReferenceMemoIntegrationFullWorkflow(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Simulate the full workflow:
	// 1. Memoless transaction comes in
	// 2. generateReferenceMemoID creates an ID
	// 3. CreateMemo creates the memo string
	// 4. fetchMemoFromReference resolves it
	// 5. trackReferenceMemoUsage tracks usage

	// Step 1: Create memoless transaction
	tx := GetRandomObservedTx()
	tx.Tx.Memo = "" // Empty memo
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(987654321)))

	// Step 2: Generate reference ID (simulating our new functionality)
	refID, err := generateReferenceMemoID(ctx, mgr, common.BTCAsset, tx)
	c.Assert(err, IsNil)
	c.Assert(refID, Equals, "54321") // last 5 digits of 987654321

	// Step 3: Create memo string (simulating CreateMemo)
	refMemo := NewReferenceReadMemo(refID)
	generatedMemo := refMemo.CreateMemo()
	c.Assert(generatedMemo, Equals, "r:54321")

	// Simulate that a reference memo exists in storage
	actualMemo := "SWAP:BTC.BTC:bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	storedRefMemo := NewReferenceMemo(common.BTCAsset, actualMemo, refID, 0)
	mgr.Keeper().SetReferenceMemo(ctx, storedRefMemo)

	// Step 4: fetchMemoFromReference resolves the generated memo
	testTx := common.NewTx(tx.Tx.ID, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, generatedMemo)
	resolvedMemo := fetchMemoFromReference(ctx, mgr, common.BTCAsset, testTx, 1) // tx observed at height 1, memo created at height 0
	c.Assert(resolvedMemo, Equals, actualMemo)

	// Step 5: Track usage after the resolved transaction is accepted
	trackReferenceMemoUsage(ctx, mgr, common.BTCAsset, generatedMemo, tx.Tx.ID)

	// Verify the full workflow worked
	updatedRefMemo, err := mgr.Keeper().GetReferenceMemo(ctx, common.BTCAsset, refID)
	c.Assert(err, IsNil)
	c.Assert(updatedRefMemo.GetUsageCount(), Equals, int64(1))
	c.Assert(updatedRefMemo.HasBeenUsedBy(tx.Tx.ID), Equals, true)
}

func (s *HandlerObservedTxHelpersSuite) TestUnfinalizedHeightPreservation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create a test transaction
	tx := GetRandomObservedTx()
	tx.Tx.Chain = common.BTCChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1000000)))

	// Create an empty voter
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{})
	c.Assert(voter.Height, Equals, int64(0))
	c.Assert(voter.UnfinalizedHeight, Equals, int64(0))

	// Mock node accounts for consensus
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	signer := nas[0].NodeAddress

	// Test processTxInAttestation - first consensus (non-finalized)
	ctx = ctx.WithBlockHeight(100)
	voter, ok := processTxInAttestation(ctx, mgr, voter, nas, tx, signer, false)

	// After first consensus, both Height and UnfinalizedHeight should be set to the same value
	if voter.HasConsensus(nas) && !tx.IsFinal() {
		c.Assert(voter.Height, Equals, int64(100))
		c.Assert(voter.UnfinalizedHeight, Equals, int64(100))
		c.Assert(ok, Equals, true)
	}

	// Test that UnfinalizedHeight is preserved when Height changes
	ctx = ctx.WithBlockHeight(200)

	// Add another observation to trigger finalization
	tx.FinaliseHeight = 150
	voter, ok = processTxInAttestation(ctx, mgr, voter, nas, tx, nas[1].NodeAddress, false)

	// After finalization, Height might change but UnfinalizedHeight should remain
	if voter.HasFinalised(nas) {
		c.Assert(voter.FinalisedHeight, Equals, int64(200))
		c.Assert(voter.UnfinalizedHeight, Equals, int64(100)) // Should preserve original consensus height
		c.Assert(ok, Equals, true)
	}
}

func (s *HandlerObservedTxHelpersSuite) TestUnfinalizedHeightUsedInFetchMemoFromReference(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set up BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Create a reference memo at height 50
	refMemo := NewReferenceMemo(common.BTCAsset, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890", "12345", 50)
	mgr.Keeper().SetReferenceMemo(ctx, refMemo)

	// Create voter with UnfinalizedHeight set to 60 but Height set to 120
	voter := NewObservedTxVoter(common.TxID("testid"), []common.ObservedTx{})
	voter.Height = 120           // Later consensus height
	voter.UnfinalizedHeight = 60 // Original consensus height when first observed
	voter.Tx.Tx.Memo = "r:12345" // Reference memo

	// Test that fetchMemoFromReference uses UnfinalizedHeight (60), not Height (120)
	// Since reference was created at height 50 and UnfinalizedHeight is 60, this should succeed
	resolvedMemo := fetchMemoFromReference(ctx, mgr, common.BTCAsset, voter.Tx.Tx, voter.UnfinalizedHeight)
	c.Assert(resolvedMemo, Equals, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890")

	// A later finalized height still represents an observation after memo creation, so it should succeed.
	resolvedMemoWithHeight := fetchMemoFromReference(ctx, mgr, common.BTCAsset, voter.Tx.Tx, voter.Height)
	c.Assert(resolvedMemoWithHeight, Equals, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890")

	// Test edge case where using Height would fail but UnfinalizedHeight succeeds
	// Create reference memo at height 70
	refMemo2 := NewReferenceMemo(common.BTCAsset, "SWAP:BTC.BTC:bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "54321", 70)
	mgr.Keeper().SetReferenceMemo(ctx, refMemo2)

	// Set voter with UnfinalizedHeight = 75 (valid) but Height = 65 (invalid - before memo creation)
	voter.Height = 65            // This would fail height validation (65 <= 70)
	voter.UnfinalizedHeight = 75 // This should pass height validation (75 > 70)
	voter.Tx.Tx.Memo = "r:54321"

	// Using UnfinalizedHeight should succeed
	resolvedMemo = fetchMemoFromReference(ctx, mgr, common.BTCAsset, voter.Tx.Tx, voter.UnfinalizedHeight)
	c.Assert(resolvedMemo, Equals, "SWAP:BTC.BTC:bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4")

	// Using Height should fail
	resolvedMemoWithHeight = fetchMemoFromReference(ctx, mgr, common.BTCAsset, voter.Tx.Tx, voter.Height)
	c.Assert(resolvedMemoWithHeight, Equals, "") // Should fail due to height validation
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundDeterministicOrdering(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set signing transaction period
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 300)

	// Create vault pubkey
	vaultPubKey := GetRandomPubKey()

	// Create two TxOutItems with identical on-chain fields but different memos/InHashes
	// These would look identical on-chain in a memoless transaction
	inHash1, _ := common.NewTxID("1111111111111111111111111111111111111111111111111111111111111111")
	inHash2, _ := common.NewTxID("2222222222222222222222222222222222222222222222222222222222222222")

	item1 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: vaultPubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
		Memo:        "OUT:" + inHash1.String(),
		InHash:      inHash1,
		GasRate:     100,
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
	}

	item2 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   item1.ToAddress, // Same ToAddress
		VaultPubKey: vaultPubKey,     // Same VaultPubKey
		Coin:        item1.Coin,      // Same Coin
		Memo:        "OUT:" + inHash2.String(),
		InHash:      inHash2,
		GasRate:     100,
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
	}

	// Compute hashes to determine which one Bifrost would sign first
	hash1 := item1.Hash()
	hash2 := item2.Hash()

	// Determine which item has the lower hash (Bifrost signs this one first)
	var firstItem, secondItem TxOutItem
	if hash1 < hash2 {
		firstItem = item1
		secondItem = item2
	} else {
		firstItem = item2
		secondItem = item1
	}

	// Set block height for context
	ctx = ctx.WithBlockHeight(100)

	// Add both TxOutItems at the same height
	txOut := NewTxOut(100)
	// Add in reverse order to ensure ordering is not just array order
	txOut.TxArray = append(txOut.TxArray, secondItem)
	txOut.TxArray = append(txOut.TxArray, firstItem)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Create an observed tx that matches both items (memoless on-chain)
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.ETHChain,
			ToAddress: item1.ToAddress,
			Coins:     common.Coins{item1.Coin},
			Memo:      "", // Empty memo (memoless)
		},
		ObservedPubKey: vaultPubKey,
	}

	// findOriginalMemoForOutbound should return the memo from the item with the lowest hash
	// (the one Bifrost would sign first)
	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, firstItem.Memo, Commentf("Expected memo from item with lower hash"))
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundSingleMatch(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set signing transaction period
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 300)

	// Create vault pubkey
	vaultPubKey := GetRandomPubKey()
	inHash, _ := common.NewTxID("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	item := TxOutItem{
		Chain:        common.ETHChain,
		ToAddress:    GetRandomETHAddress(),
		VaultPubKey:  vaultPubKey,
		Coin:         common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
		Memo:         "OUT:" + inHash.String(),
		OriginalMemo: "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890",
		InHash:       inHash,
		GasRate:      100,
		MaxGas:       common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
	}

	// Set block height for context
	ctx = ctx.WithBlockHeight(100)

	// Add TxOutItem
	txOut := NewTxOut(100)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Create an observed tx that matches the item
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.ETHChain,
			ToAddress: item.ToAddress,
			Coins:     common.Coins{item.Coin},
			Memo:      "", // Empty memo (memoless)
		},
		ObservedPubKey: vaultPubKey,
	}

	// Should return OriginalMemo when set
	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, item.OriginalMemo)
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundMatchesCurrentOutHash(c *C) {
	ctx, mgr := setupManagerForTest(c)

	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 300)

	vaultPubKey := GetRandomPubKey()
	observedTxID := GetRandomTxHash()
	inHash := GetRandomTxHash()

	item := TxOutItem{
		Chain:        common.ETHChain,
		ToAddress:    GetRandomETHAddress(),
		VaultPubKey:  vaultPubKey,
		Coin:         common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
		Memo:         "",
		OriginalMemo: "OUT:" + inHash.String(),
		InHash:       inHash,
		GasRate:      100,
		MaxGas:       common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
		OutHash:      observedTxID,
	}

	ctx = ctx.WithBlockHeight(100)
	txOut := NewTxOut(100)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	observedTx := common.ObservedTx{
		Tx: common.Tx{
			ID:        observedTxID,
			Chain:     common.ETHChain,
			ToAddress: item.ToAddress,
			Coins:     common.Coins{item.Coin},
			Memo:      "",
		},
		ObservedPubKey: vaultPubKey,
	}

	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, item.OriginalMemo)
}

func (s *HandlerObservedTxHelpersSuite) TestDuplicateCompletedOutboundDoesNotReenterHandler(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	observed := GetRandomObservedTx()
	stored := observed
	stored.Signers = []string{na.NodeAddress.String()}
	voter := NewObservedTxVoter(observed.Tx.ID, ObservedTxs{stored})
	voter.Tx = stored
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()

	voter, isQuorum := processTxOutAttestation(
		ctx,
		mgr,
		voter,
		NodeAccounts{na},
		observed,
		na.NodeAddress,
		true,
	)
	c.Assert(isQuorum, Equals, false)

	called := false
	handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		called = true
		return &cosmos.Result{}, nil
	}
	c.Assert(handleObservedTxOutQuorum(
		ctx,
		mgr,
		na.NodeAddress,
		NodeAccounts{na},
		handler,
		observed,
		voter,
		[]cosmos.AccAddress{na.NodeAddress},
		isQuorum,
	), IsNil)
	c.Assert(called, Equals, false)
}

func (s *HandlerObservedTxHelpersSuite) TestXMRMigrateMatchUsesCaseSensitiveAddresses(c *C) {
	sourcePubKey := GetRandomEd25519PubKey()
	sourceAddress, err := sourcePubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	targetPubKey := GetRandomEd25519PubKey()
	targetAddress, err := targetPubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	item := TxOutItem{
		Chain:            common.XMRChain,
		InHash:           common.BlankTxID,
		ToAddress:        targetAddress,
		VaultPubKeyFrost: sourcePubKey,
		Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000_000)),
		MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10_000))},
	}
	tx := common.Tx{
		Chain:       common.XMRChain,
		FromAddress: sourceAddress,
		ToAddress:   targetAddress,
		Coins:       common.NewCoins(item.Coin),
	}
	c.Assert(xmrMigrateInboundMatchesTxOut(tx, item), Equals, true)

	wrongSourceCase := tx
	wrongSourceCase.FromAddress = common.Address(strings.ToUpper(sourceAddress.String()))
	c.Assert(sourceAddress.Equals(wrongSourceCase.FromAddress), Equals, true)
	c.Assert(xmrMigrateInboundMatchesTxOut(wrongSourceCase, item), Equals, false)

	wrongTargetCase := tx
	wrongTargetCase.ToAddress = common.Address(strings.ToUpper(targetAddress.String()))
	c.Assert(targetAddress.Equals(wrongTargetCase.ToAddress), Equals, true)
	c.Assert(xmrMigrateInboundMatchesTxOut(wrongTargetCase, item), Equals, false)
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundUsesExtendedXMRWindow(c *C) {
	ctx, mgr := setupManagerForTest(c)

	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)

	vaultPubKey := GetRandomPubKey()
	vaultPubKeyFrost := getRandomXMRFrostPubKey(c)
	toAddress, err := vaultPubKeyFrost.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	inHash, _ := common.NewTxID("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	item := TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        toAddress,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyFrost: vaultPubKeyFrost,
		Coin:             common.NewCoin(common.XMRAsset, cosmos.NewUint(1000000)),
		Memo:             "OUT:" + inHash.String(),
		OriginalMemo:     "SWAP:XMR.XMR:" + toAddress.String(),
		InHash:           inHash,
		GasRate:          100,
		MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10000))},
	}

	ctx = ctx.WithBlockHeight(100)
	txOut := NewTxOut(75)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.XMRChain,
			ToAddress: toAddress,
			Coins:     common.Coins{item.Coin},
			Memo:      "",
		},
		ObservedPubKey: vaultPubKeyFrost,
	}

	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, item.OriginalMemo)
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundUsesExtendedEVMTokenWindow(c *C) {
	ctx, mgr := setupManagerForTest(c)

	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 10)

	vaultPubKey := GetRandomPubKey()
	inHash, _ := common.NewTxID("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	tokenAsset, err := common.NewAsset("ETH.USDT-0XA3910454BF2CB59B8B3A401589A3BACC5CA42306")
	c.Assert(err, IsNil)

	item := TxOutItem{
		Chain:        common.ETHChain,
		ToAddress:    GetRandomETHAddress(),
		VaultPubKey:  vaultPubKey,
		Coin:         common.NewCoin(tokenAsset, cosmos.NewUint(1000000)),
		Memo:         "OUT:" + inHash.String(),
		OriginalMemo: "SWAP:" + tokenAsset.String() + ":0x1234567890123456789012345678901234567890",
		InHash:       inHash,
		GasRate:      100,
		MaxGas:       common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
	}

	ctx = ctx.WithBlockHeight(100)
	txOut := NewTxOut(75)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.ETHChain,
			ToAddress: item.ToAddress,
			Coins:     common.Coins{item.Coin},
			Memo:      "",
		},
		ObservedPubKey: vaultPubKey,
	}

	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, item.OriginalMemo)
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundNoMatch(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set signing transaction period
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 300)

	// Create vault pubkey
	vaultPubKey := GetRandomPubKey()

	// Set block height for context
	ctx = ctx.WithBlockHeight(100)

	// Don't add any TxOutItems

	// Create an observed tx
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.ETHChain,
			ToAddress: GetRandomETHAddress(),
			Coins:     common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
			Memo:      "", // Empty memo (memoless)
		},
		ObservedPubKey: vaultPubKey,
	}

	// Should return empty string when no match
	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, "")
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundEmptyCoins(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create an observed tx with empty coins
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.ETHChain,
			ToAddress: GetRandomETHAddress(),
			Coins:     common.Coins{}, // Empty coins
			Memo:      "",
		},
		ObservedPubKey: GetRandomPubKey(),
	}

	// Should return empty string for empty coins
	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, "")
}

func (s *HandlerObservedTxHelpersSuite) TestFindOriginalMemoForOutboundAcrossMultipleHeights(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set signing transaction period
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 300)

	// Create vault pubkey
	vaultPubKey := GetRandomPubKey()

	// Create two TxOutItems at different heights with identical on-chain fields
	inHash1, _ := common.NewTxID("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	inHash2, _ := common.NewTxID("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	toAddr := GetRandomETHAddress()
	coin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))

	item1 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   toAddr,
		VaultPubKey: vaultPubKey,
		Coin:        coin,
		Memo:        "OUT:" + inHash1.String(),
		InHash:      inHash1,
		GasRate:     100,
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
	}

	item2 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   toAddr,
		VaultPubKey: vaultPubKey,
		Coin:        coin,
		Memo:        "OUT:" + inHash2.String(),
		InHash:      inHash2,
		GasRate:     100,
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
	}

	// Set block height for context
	ctx = ctx.WithBlockHeight(100)

	// Add item1 at height 90, item2 at height 95
	// Bifrost should sign item1 first (lower height)
	txOut90 := NewTxOut(90)
	txOut90.TxArray = append(txOut90.TxArray, item1)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut90), IsNil)

	txOut95 := NewTxOut(95)
	txOut95.TxArray = append(txOut95.TxArray, item2)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut95), IsNil)

	// Create an observed tx that matches both items
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			Chain:     common.ETHChain,
			ToAddress: toAddr,
			Coins:     common.Coins{coin},
			Memo:      "", // Empty memo (memoless)
		},
		ObservedPubKey: vaultPubKey,
	}

	// Should return memo from item at lower height (item1 at height 90)
	result := findOriginalMemoForOutbound(ctx, mgr, observedTx)
	c.Assert(result, Equals, item1.Memo)
}

func (s *HandlerObservedTxHelpersSuite) TestTxOutItemHashConsistency(c *C) {
	// Test that txOutItemHash produces consistent results matching Bifrost's formula
	inHash, _ := common.NewTxID("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000)),
		Memo:        "OUT:" + inHash.String(),
		InHash:      inHash,
	}

	// Hash should be deterministic
	hash1 := item.Hash()
	hash2 := item.Hash()
	c.Assert(hash1, Equals, hash2)

	// Hash should be 64 characters (SHA256 hex)
	c.Assert(len(hash1), Equals, 64)

	// Different memo should produce different hash
	item2 := item
	inHash2, _ := common.NewTxID("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	item2.Memo = "OUT:" + inHash2.String()
	item2.InHash = inHash2
	hash3 := item2.Hash()
	c.Assert(hash1, Not(Equals), hash3)
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionOnReorg(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create 4 active node accounts (need supermajority: 3 out of 4)
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	// Set up a vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Create the outbound tx with original gas (G1 = 22131)
	txID := GetRandomTxHash()
	originalGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         originalGas,
		Memo:        "OUT:xyz",
	}

	// Create observed tx with original gas, finalized
	obsTx := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)

	// Create empty voter
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})

	// All 4 nodes observe with the original gas (G1) - reaching consensus and finalization.
	for _, na := range nas {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, obsTx, na.NodeAddress, false)
	}

	// Voter should be finalized with G1
	c.Assert(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.Tx.Gas.Equals(originalGas), Equals, true)

	// Record vault balance before gas correction
	vault, err := mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	vaultBalanceBefore := vault.GetCoin(common.ETHAsset).Amount

	// Now simulate reorg: nodes re-observe with different gas (G2 = 23061)
	reorgGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}
	reorgTx := baseTx
	reorgTx.Gas = reorgGas
	reorgObsTx := common.NewObservedTx(reorgTx, 100, vaultPubKey, 100)

	ctx = ctx.WithBlockHeight(101) // Slightly later block

	// First 2 nodes re-observe with G2 - not enough for supermajority yet
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, reorgObsTx, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, reorgObsTx, nas[1].NodeAddress, false)

	// Gas should NOT be corrected yet (only 2 out of 4)
	c.Assert(voter.Tx.Tx.Gas.Equals(originalGas), Equals, true)

	// Third node re-observes with G2 - now we have 3/4 supermajority
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, reorgObsTx, nas[2].NodeAddress, false)

	// Gas should now be corrected
	c.Assert(voter.Tx.Tx.Gas.Equals(reorgGas), Equals, true)

	// Vault should have been debited the additional gas delta (23061 - 22131 = 930)
	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	vaultBalanceAfter := vault.GetCoin(common.ETHAsset).Amount
	expectedDelta := cosmos.NewUint(23061 - 22131)
	c.Assert(vaultBalanceBefore.Sub(vaultBalanceAfter).Equal(expectedDelta), Equals, true,
		Commentf("vault balance delta should equal gas delta: got %s, expected %s",
			vaultBalanceBefore.Sub(vaultBalanceAfter), expectedDelta))

	// Fourth node re-observes with G2 - gas correction should NOT happen again
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, reorgObsTx, nas[3].NodeAddress, false)
	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	// Balance should remain the same (no double correction)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(vaultBalanceAfter), Equals, true)
}

// The following three tests drive real manager/keeper slash accounting through
// processTxOutAttestation and assert the PERSISTED slash points (not just the
// GetObserveSigners return set), covering the facets fixed in #3081/#3104. Each
// starts from a nonzero pre-existing slash balance so any over-refund of
// unrelated points is observable.

// observeSlashOutboundTx builds an ETH outbound observed tx for these tests.
func observeSlashOutboundTx(txID common.TxID, vaultPubKey common.PubKey, gas, blockHeight int64) common.ObservedTx {
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(gas)))},
		Memo:        "OUT:" + txID.String(),
	}
	return common.NewObservedTx(baseTx, blockHeight, vaultPubKey, blockHeight)
}

func observeSlashSetup(c *C, ctx cosmos.Context, mgr *Mgrs) (NodeAccounts, common.PubKey) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	return nas, vaultPubKey
}

// TestObserveSlashExcessRefundGasSplit (#3081-B + #3104): a member's pre-broadcast
// gas-limit observation is credited gas-agnostically at consensus; its later
// gas-used scanner observation must net zero (redundant observe refunded at
// submission) and must NOT re-fire the flex branch to remove unrelated points.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashExcessRefundGasSplit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)

	member := nas[3].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, member, pre)

	txID := GetRandomTxHash()
	gasUsed := observeSlashOutboundTx(txID, vaultPubKey, 21000, 100)
	gasLimit := observeSlashOutboundTx(txID, vaultPubKey, 50000, 100)
	// Keep the non-gas tx identity identical across the two variants.
	gasLimit.Tx.FromAddress = gasUsed.Tx.FromAddress
	gasLimit.Tx.ToAddress = gasUsed.Tx.ToAddress

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasLimit, member, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, nas[1].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, nas[2].NodeAddress, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("member should be back to pre-existing after gas-agnostic credit, got %d", pts))

	// Same member's later gas-used scanner observation within the flex window.
	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, member, false)

	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("redundant scanner observation must net zero without double-refund; got %d want %d", pts, pre))
}

// TestObserveSlashLateGasLimitObserverRefunded (#3081-A): a node that missed
// consensus (took the lack penalty) then submits a late gas-limit observation
// within the flex window must be fully refunded via the gas-agnostic flex branch.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashLateGasLimitObserverRefunded(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)

	late := nas[3].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, late, pre)

	txID := GetRandomTxHash()
	gasUsed := observeSlashOutboundTx(txID, vaultPubKey, 21000, 100)
	gasLimit := observeSlashOutboundTx(txID, vaultPubKey, 50000, 100)
	gasLimit.Tx.FromAddress = gasUsed.Tx.FromAddress
	gasLimit.Tx.ToAddress = gasUsed.Tx.ToAddress

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, nas[1].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasUsed, nas[2].NodeAddress, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	lackPts := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, late)
	c.Assert(pts, Equals, pre+lackPts, Commentf("non-observer should carry the lack penalty, got %d want %d", pts, pre+lackPts))

	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, gasLimit, late, false)

	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, late)
	c.Assert(pts, Equals, pre, Commentf("honest late gas-limit observer must be fully refunded; got %d want %d", pts, pre))
}

// TestObserveSlashBlockHeightSplitRefunded (#3104): even when gas matches exactly
// (gas-deterministic chains), a member's pre-broadcast observation and its scanner
// observation split on block height into separate voter entries; the member must
// not keep an unrefundable observe point.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashBlockHeightSplitRefunded(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)

	member := nas[3].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, member, pre)

	txID := GetRandomTxHash()
	// Identical gas; the only difference is the observation block height
	// (signing tip vs inclusion), which splits them into separate voter entries.
	preBroadcast := observeSlashOutboundTx(txID, vaultPubKey, 21000, 99)
	scanner := observeSlashOutboundTx(txID, vaultPubKey, 21000, 100)
	scanner.Tx.FromAddress = preBroadcast.Tx.FromAddress
	scanner.Tx.ToAddress = preBroadcast.Tx.ToAddress

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, preBroadcast, member, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scanner, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scanner, nas[1].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scanner, nas[2].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scanner, member, false)

	c.Assert(voter.FinalisedHeight > 0, Equals, true)
	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("block-height split must net zero for the committee member; got %d want %d", pts, pre))
}

// TestObserveSlashConfCountingNoErasure guards the finality-tier boundary: on a
// confirmation-counting chain an honest node observes the same tx twice — once
// non-final (pre-confirmations) and once final — reaching consensus in each tier.
// The redundant-observation refund must NOT treat the final observation as a
// redundant variant of the non-final one (they are credited in separate tiers),
// otherwise the node loses a legitimately-accrued slash point on every such tx.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashConfCountingNoErasure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)

	honest := nas[0].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, honest, pre)

	txID := GetRandomTxHash()
	inTx := common.Tx{
		ID:          txID,
		Chain:       common.BTCChain,
		FromAddress: GetRandomBTCAddress(),
		ToAddress:   GetRandomBTCAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))),
		Gas:         common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		Memo:        "SWAP:BTC.BTC",
	}
	// Same tx observed non-final (FinaliseHeight > BlockHeight) then final.
	nonFinal := common.NewObservedTx(inTx, 100, vaultPubKey, 110)
	final := common.NewObservedTx(inTx, 100, vaultPubKey, 100)
	c.Assert(nonFinal.IsFinal(), Equals, false)
	c.Assert(final.IsFinal(), Equals, true)

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	// Non-final consensus tier.
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[0].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[1].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[2].NodeAddress, false)
	// Final consensus tier (same nodes re-observe after confirmations).
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, nas[0].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, nas[1].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, nas[2].NodeAddress, false)
	c.Assert(voter.FinalisedHeight > 0, Equals, true)

	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, honest)
	c.Assert(pts, Equals, pre, Commentf("conf-counting double-observer must net zero; a cross-finality false match erases a slash point (got %d want %d)", pts, pre))
}

// TestObserveSlashRedundantVariantSpamCapped (#3081, huginn follow-up): the
// redundant re-observation refund is capped at the signer's FIRST prior matching
// variant. A member's honest re-observation (its 2nd variant) is refunded, but any
// further self-inflicted variant (3rd+) keeps its observe slash point, so a node
// cannot file unlimited gas/height variants at zero cost to defeat the
// per-observation charge.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashRedundantVariantSpamCapped(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)

	member := nas[3].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, member, pre)
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	txID := GetRandomTxHash()
	// Three variants of the same outbound differing ONLY by gas.
	variantA := observeSlashOutboundTx(txID, vaultPubKey, 50000, 100)
	variantB := observeSlashOutboundTx(txID, vaultPubKey, 21000, 100)
	variantC := observeSlashOutboundTx(txID, vaultPubKey, 30000, 100)
	variantB.Tx.FromAddress = variantA.Tx.FromAddress
	variantB.Tx.ToAddress = variantA.Tx.ToAddress
	variantC.Tx.FromAddress = variantA.Tx.FromAddress
	variantC.Tx.ToAddress = variantA.Tx.ToAddress

	// member's original observation, then consensus forms on variantB.
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, variantA, member, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, variantB, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, variantB, nas[1].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, variantB, nas[2].NodeAddress, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("original observer credited gas-agnostic at consensus; got %d want %d", pts, pre))

	// 2nd variant (1st redundant): honest re-observation, refunded → net zero.
	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, variantB, member, false)
	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("first redundant re-observation must be refunded; got %d want %d", pts, pre))

	// 3rd variant (2nd redundant): self-inflicted spam, NOT refunded → keeps its
	// observe point. Without the cap this would net zero (spam free); with it the
	// node pays the per-observation charge for the extra variant.
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, variantC, member, false)
	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre+observeSlashPoints, Commentf("second redundant variant must NOT be refunded (spam cap); got %d want %d", pts, pre+observeSlashPoints))
}

// TestObserveSlashRedundantVariantSpamCappedTxIn (#3081, huginn follow-up): the
// same redundant-variant refund cap on the INBOUND path (processTxInAttestation).
// The gate is byte-identical to the outbound site; this guards the txin
// integration so the two paths can't silently diverge.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashRedundantVariantSpamCappedTxIn(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)

	member := nas[3].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, member, pre)
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	txID := GetRandomTxHash()
	base := common.Tx{
		ID:          txID,
		Chain:       common.BTCChain,
		FromAddress: GetRandomBTCAddress(),
		ToAddress:   GetRandomBTCAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))),
		Memo:        "SWAP:BTC.BTC",
	}
	// Three variants of the same inbound differing ONLY by gas, all final.
	mkVariant := func(gas uint64) common.ObservedTx {
		tx := base
		tx.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(gas))}
		return common.NewObservedTx(tx, 100, vaultPubKey, 100)
	}
	variantA := mkVariant(1000)
	variantB := mkVariant(2000)
	variantC := mkVariant(3000)

	// member's original observation, then consensus forms on variantB.
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variantA, member, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variantB, nas[0].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variantB, nas[1].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variantB, nas[2].NodeAddress, false)
	c.Assert(voter.FinalisedHeight > 0, Equals, true)

	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("original observer credited gas-agnostic at consensus; got %d want %d", pts, pre))

	// 2nd variant (1st redundant): honest re-observation, refunded → net zero.
	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variantB, member, false)
	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre, Commentf("first redundant re-observation must be refunded; got %d want %d", pts, pre))

	// 3rd variant (2nd redundant): self-inflicted spam, NOT refunded → keeps its point.
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variantC, member, false)
	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre+observeSlashPoints, Commentf("second redundant variant must NOT be refunded (spam cap); got %d want %d", pts, pre+observeSlashPoints))
}

// TestObserveSlashXMRConflictingRefLateMatchKeepsOriginalPoint guards the
// interaction between the transitive XMR refund class and the flex-window
// refund. A prior observation with conflicting non-empty refs must remain
// charged when a later consensus-compatible observation receives its own
// observe-point and lack-of-observation refunds.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashXMRConflictingRefLateMatchKeepsOriginalPoint(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, _ := observeSlashSetup(c, ctx, mgr)

	member := nas[3].NodeAddress
	const pre = int64(50)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, member, pre)
	observePoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackPoints := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)

	fromPubKey := GetRandomEd25519PubKey()
	fromAddress, err := fromPubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	toPubKey := GetRandomEd25519PubKey()
	toAddress, err := toPubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	txID := GetRandomTxHash()
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.XMRChain,
		FromAddress: fromAddress,
		ToAddress:   toAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000_000))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10_000))},
		Memo:        "OUT:" + txID.String(),
	}
	refA := common.NewObservedTx(baseTx, 100, fromPubKey, 100)
	refA.XmrTxKey = strings.Repeat("a", 64)
	refA.SpentOutputRefs = []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}
	refB := common.NewObservedTx(baseTx, 100, fromPubKey, 100)
	refB.XmrTxKey = strings.Repeat("a", 64)
	refB.SpentOutputRefs = []common.OutputRef{
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}
	c.Assert(NewMsgObservedTxOut(common.ObservedTxs{refA}, member).ValidateBasic(), IsNil)
	c.Assert(NewMsgObservedTxOut(common.ObservedTxs{refB}, member).ValidateBasic(), IsNil)

	// The member's refA observation conflicts with the refB observation that
	// reaches consensus, so its original observe point remains and it receives
	// the lack-of-observation penalty.
	voter := NewObservedTxVoter(txID, nil)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, refA, member, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, refB, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, refB, nas[1].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, refB, nas[2].NodeAddress, false)

	pts, _ := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre+observePoints+lackPoints)

	// The late refB observation is consensus-compatible, so the flex branch
	// refunds that submission and the lack penalty. The conflicting refA point
	// must not also be erased by the immediate redundant-observation refund.
	ctx = ctx.WithBlockHeight(101)
	updatedVoter, reachedConsensus := processTxOutAttestation(ctx, mgr, voter, nas, refB, member, false)
	c.Assert(reachedConsensus, Equals, false)
	c.Assert(updatedVoter.Tx.HasSigned(member), Equals, true)
	pts, _ = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
	c.Assert(pts, Equals, pre+observePoints)
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionFinalizedIgnoresNonFinalObservations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create 4 active node accounts (need supermajority: 3 out of 4)
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	// Set up a vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Finalized outbound tx with original gas (G1 = 22131)
	txID := GetRandomTxHash()
	originalGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         originalGas,
		Memo:        "OUT:xyz",
	}
	finalObsTx := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, finalObsTx, na.NodeAddress, false)
	}

	// Voter should be finalized with G1
	c.Assert(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.IsFinal(), Equals, true)
	c.Assert(voter.Tx.Tx.Gas.Equals(originalGas), Equals, true)

	vault, err := mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	vaultBalanceBefore := vault.GetCoin(common.ETHAsset).Amount

	// Re-observations are NON-final (FinaliseHeight > BlockHeight) with different gas (G2).
	// Even if a supermajority agrees on these non-final observations, a finalized voter
	// should not accept them for gas correction.
	reorgGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}
	reorgTx := baseTx
	reorgTx.Gas = reorgGas
	nonFinalReorgObsTx := common.NewObservedTx(reorgTx, 100, vaultPubKey, 101)

	ctx = ctx.WithBlockHeight(101)

	// 3 out of 4 nodes submit non-final re-observations (supermajority)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, nonFinalReorgObsTx, nas[0].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, nonFinalReorgObsTx, nas[1].NodeAddress, false)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, nonFinalReorgObsTx, nas[2].NodeAddress, false)

	// Gas should NOT be corrected from non-final observations once voter is finalized.
	c.Assert(voter.Tx.Tx.Gas.Equals(originalGas), Equals, true)
	c.Assert(voter.Tx.IsFinal(), Equals, true)

	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(vaultBalanceBefore), Equals, true)
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionDecrease(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create 3 active node accounts
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	// Set up a vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Create the outbound tx with original gas (G1 = 23061, higher)
	txID := GetRandomTxHash()
	originalGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         originalGas,
		Memo:        "OUT:xyz",
	}

	obsTx := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})

	// All nodes observe with G1
	for _, na := range nas {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, obsTx, na.NodeAddress, false)
	}

	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	vault, err := mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	vaultBalanceBefore := vault.GetCoin(common.ETHAsset).Amount

	// Gas DECREASED after reorg (G2 = 22131)
	reorgGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	reorgTx := baseTx
	reorgTx.Gas = reorgGas
	reorgObsTx := common.NewObservedTx(reorgTx, 100, vaultPubKey, 100)

	ctx = ctx.WithBlockHeight(101)

	// All 3 nodes re-observe with G2 (2 gives supermajority for 3 nodes)
	for _, na := range nas {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, reorgObsTx, na.NodeAddress, false)
	}

	// Gas should be corrected
	c.Assert(voter.Tx.Tx.Gas.Equals(reorgGas), Equals, true)

	// Vault should have GAINED back the gas delta (23061 - 22131 = 930)
	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	vaultBalanceAfter := vault.GetCoin(common.ETHAsset).Amount
	expectedDelta := cosmos.NewUint(23061 - 22131)
	c.Assert(vaultBalanceAfter.Sub(vaultBalanceBefore).Equal(expectedDelta), Equals, true,
		Commentf("vault should gain back over-deducted gas: got %s, expected %s",
			vaultBalanceAfter.Sub(vaultBalanceBefore), expectedDelta))
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionSkipsInactiveVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Set up an INACTIVE vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.Status = InactiveVault
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Original consensus gas and corrected (higher) gas
	oldGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	newGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}

	consensusTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.ETHChain,
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
			Gas:   oldGas,
		},
	}
	correctedTx := ObservedTx{
		Tx: common.Tx{
			ID:    consensusTx.Tx.ID,
			Chain: common.ETHChain,
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
			Gas:   newGas,
		},
		ObservedPubKey: vaultPubKey,
	}

	// Call correctOutboundGas
	correctOutboundGas(ctx, mgr, consensusTx, correctedTx)

	// Vault balance should be debited (vault accounting still happens)
	vault, err := mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	_ = vault

	// But gas manager should NOT have the additional gas (no reimbursement for inactive vault)
	gasEvents := mgr.GasMgr().GetGas()
	c.Assert(gasEvents, HasLen, 0, Commentf("inactive vault should not trigger reserve reimbursement"))
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionSkipsDuringRagnarokGasAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Set ragnarok in progress
	mgr.Keeper().SetRagnarokBlockHeight(ctx, 50)

	// Set up an active vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.Status = ActiveVault
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Original consensus gas and corrected (higher) gas
	oldGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	newGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}

	// Outbound coins contain the gas asset (ETH) - should skip reimbursement
	consensusTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.ETHChain,
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
			Gas:   oldGas,
		},
	}
	correctedTx := ObservedTx{
		Tx: common.Tx{
			ID:    consensusTx.Tx.ID,
			Chain: common.ETHChain,
			Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
			Gas:   newGas,
		},
		ObservedPubKey: vaultPubKey,
	}

	correctOutboundGas(ctx, mgr, consensusTx, correctedTx)

	// Gas manager should NOT have the additional gas (ragnarok + gas asset in coins)
	gasEvents := mgr.GasMgr().GetGas()
	c.Assert(gasEvents, HasLen, 0, Commentf("ragnarok with gas asset coins should not trigger reserve reimbursement"))
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionAllowedDuringRagnarokNonGasAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Set ragnarok in progress
	mgr.Keeper().SetRagnarokBlockHeight(ctx, 50)

	// Set up an active vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.Status = ActiveVault
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Original consensus gas and corrected (higher) gas
	oldGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	newGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}

	// Outbound coins contain a NON-gas asset (USDC token on ETH) - should still reimburse
	usdcAsset := common.Asset{Chain: common.ETHChain, Symbol: "USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", Ticker: "USDC", Synth: false}
	consensusTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.ETHChain,
			Coins: common.NewCoins(common.NewCoin(usdcAsset, cosmos.NewUint(1000000))),
			Gas:   oldGas,
		},
	}
	correctedTx := ObservedTx{
		Tx: common.Tx{
			ID:    consensusTx.Tx.ID,
			Chain: common.ETHChain,
			Coins: common.NewCoins(common.NewCoin(usdcAsset, cosmos.NewUint(1000000))),
			Gas:   newGas,
		},
		ObservedPubKey: vaultPubKey,
	}

	correctOutboundGas(ctx, mgr, consensusTx, correctedTx)

	// Gas manager SHOULD have the additional gas (ragnarok but non-gas asset in coins)
	gasEvents := mgr.GasMgr().GetGas()
	c.Assert(gasEvents, HasLen, 1, Commentf("ragnarok with non-gas-asset coins should still trigger reserve reimbursement"))
}

func (s *HandlerObservedTxHelpersSuite) TestCountMatchingSigners(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	txID := GetRandomTxHash()
	gas1 := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	gas2 := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}

	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         gas1,
		Memo:        "OUT:xyz",
	}

	vaultPubKey := GetRandomPubKey()
	obsTx1 := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)
	obsTx1.Signers = []string{nas[0].NodeAddress.String(), nas[1].NodeAddress.String()}

	reorgTx := baseTx
	reorgTx.Gas = gas2
	obsTx2 := common.NewObservedTx(reorgTx, 100, vaultPubKey, 100)
	obsTx2.Signers = []string{nas[2].NodeAddress.String()}

	obsTx3 := common.NewObservedTx(reorgTx, 101, vaultPubKey, 101)
	obsTx3.Signers = []string{nas[2].NodeAddress.String(), nas[0].NodeAddress.String()}

	voter := NewObservedTxVoter(txID, []common.ObservedTx{obsTx1, obsTx2, obsTx3})

	// Count signers matching gas2
	count := countMatchingSigners(voter, obsTx2, nas)
	c.Assert(count, Equals, 2, Commentf("duplicate signer across matching observations should only count once"))

	// Count signers matching gas1
	testTx := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)
	count = countMatchingSigners(voter, testTx, nas)
	c.Assert(count, Equals, 2)
}

func (s *HandlerObservedTxHelpersSuite) TestCountMatchingSignersScopesByObservedVault(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	txID := GetRandomTxHash()
	gas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         gas,
		Memo:        "OUT:xyz",
	}

	vaultPubKeyA := GetRandomPubKey()
	vaultPubKeyB := GetRandomPubKey()

	obsTxA := common.NewObservedTx(baseTx, 100, vaultPubKeyA, 100)
	obsTxA.Signers = []string{nas[0].NodeAddress.String(), nas[1].NodeAddress.String()}

	obsTxB := common.NewObservedTx(baseTx, 100, vaultPubKeyB, 100)
	obsTxB.Signers = []string{nas[2].NodeAddress.String(), nas[3].NodeAddress.String()}

	voter := NewObservedTxVoter(txID, []common.ObservedTx{obsTxA, obsTxB})

	countA := countMatchingSigners(voter, obsTxA, nas)
	c.Assert(countA, Equals, 2)

	countB := countMatchingSigners(voter, obsTxB, nas)
	c.Assert(countB, Equals, 2)
}

func (s *HandlerObservedTxHelpersSuite) TestCountMatchingSignersDoesNotMixXMRSpentRefs(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	txID := GetRandomTxHash()
	baseTx := GetRandomTx()
	baseTx.ID = txID
	baseTx.Chain = common.XMRChain
	baseTx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000)))
	baseTx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))}
	baseTx.Memo = "OUT:xmr-spent-refs"

	vaultPubKey := GetRandomPubKey()
	obsTxA := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)
	obsTxA.SpentOutputRefs = []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}
	obsTxA.Signers = []string{nas[0].NodeAddress.String(), nas[1].NodeAddress.String()}

	obsTxB := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)
	obsTxB.SpentOutputRefs = []common.OutputRef{
		{TxHash: "cccc", OutputIndex: 0, KeyImage: "ki_cccc", SpendTxHash: txID.String()},
		{TxHash: "dddd", OutputIndex: 1, KeyImage: "ki_dddd", SpendTxHash: txID.String()},
	}
	obsTxB.Signers = []string{nas[2].NodeAddress.String(), nas[3].NodeAddress.String()}

	voter := NewObservedTxVoter(txID, []common.ObservedTx{obsTxA, obsTxB})

	countA := countMatchingSigners(voter, obsTxA, nas)
	c.Assert(countA, Equals, 2)

	countB := countMatchingSigners(voter, obsTxB, nas)
	c.Assert(countB, Equals, 2)
}

func (s *HandlerObservedTxHelpersSuite) TestGasCorrectionRequiresFinalityParity(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Create 4 active node accounts (supermajority = 3 out of 4)
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}

	// Set up a vault with funds
	vaultPubKey := GetRandomPubKey()
	vault := GetRandomVault()
	vault.PubKey = vaultPubKey
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Create the outbound tx with original gas
	txID := GetRandomTxHash()
	originalGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(22131))}
	baseTx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(305570))),
		Gas:         originalGas,
		Memo:        "OUT:xyz",
	}

	// All 4 nodes observe with original gas as FINAL (BlockHeight == FinaliseHeight)
	obsTx := common.NewObservedTx(baseTx, 100, vaultPubKey, 100)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})

	for _, na := range nas {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, obsTx, na.NodeAddress, false)
	}

	c.Assert(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.IsFinal(), Equals, true)
	c.Assert(voter.Tx.Tx.Gas.Equals(originalGas), Equals, true)

	// Record vault balance before re-observations
	vault, err := mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	vaultBalanceBefore := vault.GetCoin(common.ETHAsset).Amount

	// Now simulate re-observations with different gas but NON-FINAL (BlockHeight != FinaliseHeight)
	reorgGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(23061))}
	reorgTx := baseTx
	reorgTx.Gas = reorgGas
	nonFinalReorgObsTx := common.NewObservedTx(reorgTx, 100, vaultPubKey, 200) // not final

	ctx = ctx.WithBlockHeight(101)

	// All 4 nodes re-observe with non-final corrected gas
	for _, na := range nas {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, nonFinalReorgObsTx, na.NodeAddress, false)
	}

	// Gas should NOT be corrected because non-final re-observations must not mutate finalized gas
	c.Assert(voter.Tx.Tx.Gas.Equals(originalGas), Equals, true,
		Commentf("non-final re-observations should not correct gas on finalized voter"))

	// Vault balance should be unchanged
	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.GetCoin(common.ETHAsset).Amount.Equal(vaultBalanceBefore), Equals, true,
		Commentf("vault balance should be unchanged when non-final re-observations are rejected"))
}

// TestDebitVaultHalts asserts that an outbound observation whose
// coin amount exceeds the vault's tracked balance halts signing + trading on
// the affected chain. The excess debit is recorded as deficit and surfaced.
func (s *HandlerObservedTxHelpersSuite) TestDebitVaultHalts(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1234)
	k := mgr.Keeper()

	vault := GetRandomVault()
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Observed outbound debits 100 ETH but the vault only tracks 10 ETH.
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	// Sanity: chain should not be halted before the call.
	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning <= 0, Equals, true)
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading <= 0, Equals, true)

	debitVault(ctx, mgr, &vault, tx, tx.Tx.Coins, vaultDeficitReasonOutbound)

	haltSigning, err = k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight(),
		Commentf("HaltSigningETH should be set to current block height when a deficit is created"))
	haltTrading, err = k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight(),
		Commentf("HaltETHTrading should be set to current block height when a deficit is created"))
}

// TestDebitVaultNoOpWhenSolvent ensures the helper is a no-op
// when the vault has sufficient balance to cover the observed outbound — the
// normal happy path that should NOT halt the chain.
func (s *HandlerObservedTxHelpersSuite) TestDebitVaultNoOpWhenSolvent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1234)
	k := mgr.Keeper()

	vault := GetRandomVault()
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	debitVault(ctx, mgr, &vault, tx, tx.Tx.Coins, vaultDeficitReasonOutbound)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning <= 0, Equals, true,
		Commentf("HaltSigningETH should remain unset when vault has sufficient balance"))
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading <= 0, Equals, true,
		Commentf("HaltETHTrading should remain unset when vault has sufficient balance"))
}

// TestDebitVaultHaltsOnGasDebit asserts the helper also catches the same
// deficit pattern when the slashed-vault gas debit underflows the vault's gas
// reserve.
func (s *HandlerObservedTxHelpersSuite) TestDebitVaultHaltsOnGasDebit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(2222)
	k := mgr.Keeper()

	vault := GetRandomVault()
	// Vault has only 100 wei of ETH for gas — far less than the observed gas spend.
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	gas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))} // 1e8 wei
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
		Gas:         gas,
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	// Pass the gas coins explicitly — this is the call shape used by the
	// vault-slash branch of handleObservedTxOutQuorum.
	gasCoins := tx.Tx.Gas.ToCoins()
	debitVault(ctx, mgr, &vault, tx, gasCoins, vaultDeficitReasonVaultSlashGas)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight(),
		Commentf("HaltSigningETH should be set when gas debit creates deficit"))
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight(),
		Commentf("HaltETHTrading should be set when gas debit creates deficit"))
}

// TestGasDeficitHaltUsesCumulativeTotal verifies repeated below-threshold
// gas-related deficits halt once the stored same-asset deficit total crosses
// the max-gas-based threshold.
func (s *HandlerObservedTxHelpersSuite) TestGasDeficitHaltUsesCumulativeTotal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(3333)
	k := mgr.Keeper()

	k.SetMimir(ctx, constants.VaultDeficitGasHaltMaxGasMultiplier.String(), 2)
	c.Assert(k.SaveNetworkFee(ctx, common.ETHChain, NewNetworkFee(common.ETHChain, 1000, 10_000)), IsNil)

	maxGasCoin, err := mgr.GasMgr().GetMaxGas(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	threshold := maxGasCoin.Amount.MulUint64(2)
	c.Assert(threshold.GT(cosmos.NewUint(1)), Equals, true)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, threshold.Sub(cosmos.NewUint(1)))},
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	debitVault(ctx, mgr, &vault, tx, tx.Tx.Gas.ToCoins(), vaultDeficitReasonOutboundGas)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(threshold.Sub(cosmos.NewUint(1))), Equals, true)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning <= 0, Equals, true)
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading <= 0, Equals, true)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	tx.Tx.ID = GetRandomTxHash()
	tx.Tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}

	debitVault(ctx, mgr, &vault, tx, tx.Tx.Gas.ToCoins(), vaultDeficitReasonOutboundGas)
	c.Assert(vault.GetDeficit(common.ETHAsset).Amount.Equal(threshold), Equals, true)

	haltSigning, err = k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight())
	haltTrading, err = k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight())
}

type zeroMaxGasManager struct {
	*DummyGasManager
}

func (zeroMaxGasManager) GetMaxGas(_ cosmos.Context, chain common.Chain) (common.Coin, error) {
	return common.NewCoin(chain.GetGasAsset(), cosmos.ZeroUint()), nil
}

func (s *HandlerObservedTxHelpersSuite) TestGasDeficitHaltSkipsZeroMaxGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.VaultDeficitGasHaltMaxGasMultiplier.String(), 2)
	mgr.gasMgr = zeroMaxGasManager{DummyGasManager: NewDummyGasManager()}

	crosses := gasDeficitCrossesHaltThreshold(ctx, mgr, k, common.NewCoin(common.ETHAsset, cosmos.OneUint()))
	c.Assert(crosses, Equals, false)
}

// TestDebitVaultPreservesManualHalt asserts that existing
// manual indefinite halts (mimir == 1) on either HaltSigning<Chain> or
// Halt<Chain>Trading are NOT downgraded to the current block height. Without
// this guard, a regular halt-height write would later be auto-unhalted, which
// is exactly what the manual-halt sentinel is meant to prevent.
func (s *HandlerObservedTxHelpersSuite) TestDebitVaultPreservesManualHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1234)
	k := mgr.Keeper()

	vault := GetRandomVault()
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Operator has set the manual indefinite-halt sentinels.
	k.SetMimir(ctx, "HaltSigningETH", 1)
	k.SetMimir(ctx, "HaltETHTrading", 1)

	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	debitVault(ctx, mgr, &vault, tx, tx.Tx.Coins, vaultDeficitReasonOutbound)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, int64(1),
		Commentf("HaltSigningETH manual indefinite halt must be preserved across deficit handling"))
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, int64(1),
		Commentf("HaltETHTrading manual indefinite halt must be preserved across deficit handling"))
}

// TestDebitVaultOverridesFutureHalt asserts that a future-scheduled halt is
// overwritten with ctx.BlockHeight() when a deficit is detected. Per Ursa's MR
// feedback: a vault debit deficit is a present-time divergence, so triggering
// the halt now is the right call. Differs from processSolvencyAttestation,
// which preserves future halts because it can also unhalt.
func (s *HandlerObservedTxHelpersSuite) TestDebitVaultOverridesFutureHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1234)
	k := mgr.Keeper()

	vault := GetRandomVault()
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Halts scheduled 1000 blocks in the future.
	futureHalt := ctx.BlockHeight() + 1000
	k.SetMimir(ctx, "HaltSigningETH", futureHalt)
	k.SetMimir(ctx, "HaltETHTrading", futureHalt)

	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	debitVault(ctx, mgr, &vault, tx, tx.Tx.Coins, vaultDeficitReasonOutbound)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight(),
		Commentf("HaltSigningETH future halt must be overwritten with current block height"))
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight(),
		Commentf("HaltETHTrading future halt must be overwritten with current block height"))
}

// TestDebitVaultSumsDuplicateCoins asserts that multiple coin
// entries for the same asset are summed before comparing against the vault
// balance. Per Ursa's MR feedback: tx.Tx.Coins should normally have one entry
// per asset, but if duplicates ever appear the SUM of amounts must be the
// thing compared, not each entry independently.
func (s *HandlerObservedTxHelpersSuite) TestDebitVaultSumsDuplicateCoins(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(3333)
	k := mgr.Keeper()

	vault := GetRandomVault()
	// Vault holds 50 ETH.
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Two coin entries of 30 ETH each — neither alone exceeds 50, but the
	// sum (60) does. A naive per-entry comparison would miss this deficit.
	coins := common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(30*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(30*common.One)),
	}
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       coins,
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "OUT:abc",
	}, ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())

	debitVault(ctx, mgr, &vault, tx, coins, vaultDeficitReasonOutbound)

	haltSigning, err := k.GetMimir(ctx, "HaltSigningETH")
	c.Assert(err, IsNil)
	c.Assert(haltSigning, Equals, ctx.BlockHeight(),
		Commentf("deficit handling must fire when summed-by-asset amount exceeds vault balance"))
	haltTrading, err := k.GetMimir(ctx, "HaltETHTrading")
	c.Assert(err, IsNil)
	c.Assert(haltTrading, Equals, ctx.BlockHeight(),
		Commentf("deficit handling must fire when summed-by-asset amount exceeds vault balance"))
}

func (s *HandlerObservedTxHelpersSuite) TestXMRSpentOutputRefsStoreAccumulation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vaultPubKey := GetRandomPubKey()
	vault := NewVaultV2(1, ActiveVault, AsgardVault, vaultPubKey, []string{common.XMRChain.String()}, nil, vaultPubKey)
	vault.PubKeyFrost = vaultPubKey
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	ref1 := common.OutputRef{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki_aaa", SpendTxHash: "spend_tx"}
	ref2 := common.OutputRef{TxHash: "bbb", OutputIndex: 1, KeyImage: "ki_bbb", SpendTxHash: "spend_tx"}

	applySpentRefs := func(refs []common.OutputRef) {
		c.Assert(mgr.Keeper().SetXMRSpentOutputRefs(ctx, vaultPubKey, common.TxID("spend_tx"), refs, ctx.BlockHeight(), ctx.BlockHeight()), IsNil)
	}

	applySpentRefs([]common.OutputRef{ref1, ref2})
	refs, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(refs), Equals, 2)

	applySpentRefs([]common.OutputRef{
		{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki_aaa", SpendTxHash: "spend_tx"},
		{TxHash: "ccc", OutputIndex: 2, KeyImage: "ki_ccc", SpendTxHash: "spend_tx"},
	})
	refs, err = mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(refs), Equals, 3, Commentf("duplicate ref should be deduped"))
}
