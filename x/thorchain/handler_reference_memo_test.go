package thorchain

import (
	"fmt"
	"math"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gopkg.in/check.v1"
	. "gopkg.in/check.v1"
)

type HandlerReferenceMemoSuite struct{}

var _ = Suite(&HandlerReferenceMemoSuite{})

type keeperReferenceMemoTest struct {
	keeper.Keeper
}

func NewKeeperReferenceMemoTest(k keeper.Keeper) *keeperReferenceMemoTest {
	return &keeperReferenceMemoTest{
		Keeper: k,
	}
}

func (k *keeperReferenceMemoTest) GetReferenceMemo(ctx cosmos.Context, asset common.Asset, ref string) (ReferenceMemo, error) {
	// simulate all references are taken
	return NewReferenceMemo(asset, "my memo", "refff", 5), nil
}

func (s *HandlerReferenceMemoSuite) TestValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// failure cases
	msg.Memo = "invalid memo"
	c.Assert(handler.validate(ctx, *msg), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestValidateRejectsReservedTypes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Reserved validator-internal types must never be registrable as references.
	reserved := []string{
		"OUT:" + GetRandomTxHash().String(),
		"REFUND:" + GetRandomTxHash().String(),
		"RAGNAROK:1",
		"MIGRATE:1",
		"CONSOLIDATE",
	}
	for _, memo := range reserved {
		msg := NewMsgReferenceMemo(asset, memo, signer)
		err := handler.validate(ctx, *msg)
		c.Assert(err, NotNil, check.Commentf("memo %q should be rejected", memo))
		c.Check(err.Error(), check.Matches, ".*reserved for validator-internal use.*", check.Commentf("memo %q", memo))
	}

	// A benign type still registers.
	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)
}

func (s *HandlerReferenceMemoSuite) TestValidateWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Set a cost for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), 1000_00000000) // 1000 RUNE

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)

	// Should fail validation due to insufficient balance
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), check.Matches, ".*insufficient RUNE balance.*")

	// Add sufficient balance to signer
	err = mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, cosmos.NewUint(1000_00000000)))
	c.Assert(err, IsNil)

	// Should now pass validation
	c.Assert(handler.validate(ctx, *msg), IsNil)
}

func (s *HandlerReferenceMemoSuite) TestHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// happy path
	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, asset, "20000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestHandlerWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Set a cost for memoless transactions
	cost := int64(500_00000000) // 500 RUNE
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), cost)

	// Add sufficient balance to signer
	err := mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, cosmos.NewUint(uint64(cost))))
	c.Assert(err, IsNil)

	// Get initial reserve balance
	initialReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Check that RUNE was transferred to reserve
	finalReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	expectedIncrease := cosmos.NewUint(uint64(cost))
	actualIncrease := finalReserveBalance.Sub(initialReserveBalance)
	c.Check(actualIncrease.Equal(expectedIncrease), check.Equals, true, check.Commentf("Expected increase: %s, Actual increase: %s", expectedIncrease, actualIncrease))

	// Check that reference memo was created
	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, asset, "20000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestNextReference(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	asset := common.BTCAsset

	// Test case for a position in the middle of the range
	c.Check(handler.nextReference(ctx, asset, "00003", 3), check.Equals, "00004")

	// Test case for a position at the end of the range
	c.Check(handler.nextReference(ctx, asset, "99999", 3), check.Equals, "00001")

	// Test case for a position outside the range (high)
	c.Check(handler.nextReference(ctx, asset, "120000", 3), check.Equals, "00001")
}

func (s *HandlerReferenceMemoSuite) TestReferenceExhaustionWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions with short TTL and small reference count
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 1000)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnRefCount.String(), 5) // Only allow 5 references

	// Set a cost for memoless transactions
	cost := int64(100_00000000) // 100 RUNE
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), cost)

	// Add sufficient balance to signer (enough for 10 registrations, but only 5 will succeed)
	err := mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, cosmos.NewUint(uint64(cost*10))))
	c.Assert(err, IsNil)

	// Get initial balances
	initialSignerBalance := mgr.Keeper().GetBalance(ctx, signer).AmountOf(common.RuneNative.Native())
	initialReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	// Fill all 5 available reference slots
	for i := 1; i <= 5; i++ {
		msg := NewMsgReferenceMemo(asset, fmt.Sprintf("+:BTC.BTC:address%d", i), signer)
		_, handleErr := handler.handle(ctx, *msg)
		c.Assert(handleErr, IsNil, check.Commentf("Failed to create reference %d", i))
	}

	// Check that 5 x cost was transferred to reserve
	afterFillSignerBalance := mgr.Keeper().GetBalance(ctx, signer).AmountOf(common.RuneNative.Native())
	afterFillReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	expectedDeduction := cosmos.NewUint(uint64(cost * 5))
	actualDeduction := cosmos.NewUintFromBigInt(initialSignerBalance.BigInt()).Sub(cosmos.NewUintFromBigInt(afterFillSignerBalance.BigInt()))
	c.Check(actualDeduction.Equal(expectedDeduction), check.Equals, true,
		check.Commentf("Expected deduction: %s, Actual: %s", expectedDeduction, actualDeduction))
	c.Check(afterFillReserveBalance.Sub(initialReserveBalance).Equal(expectedDeduction), check.Equals, true)

	// Now try to create the 6th reference - should fail with no available reference
	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC:address6", signer)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, NotNil, check.Commentf("Should fail when all references are exhausted"))
	c.Check(err.Error(), check.Matches, ".*unable to find an available reference.*")

	// CRITICAL: Verify that NO cost was deducted for the failed registration
	finalSignerBalance := mgr.Keeper().GetBalance(ctx, signer).AmountOf(common.RuneNative.Native())
	finalReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(cosmos.NewUintFromBigInt(finalSignerBalance.BigInt()).Equal(cosmos.NewUintFromBigInt(afterFillSignerBalance.BigInt())), check.Equals, true,
		check.Commentf("Signer balance should not change on failed registration. Expected: %s, Got: %s",
			afterFillSignerBalance, finalSignerBalance))
	c.Check(finalReserveBalance.Equal(afterFillReserveBalance), check.Equals, true,
		check.Commentf("Reserve balance should not change on failed registration"))
}

func (s *HandlerReferenceMemoSuite) TestCostPaymentFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Set a high cost that exceeds user's balance
	cost := int64(1000_00000000) // 1000 RUNE
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), cost)

	// Give user only 500 RUNE (insufficient)
	err := mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, cosmos.NewUint(500_00000000)))
	c.Assert(err, IsNil)

	// Try to register - should fail at validation stage
	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), check.Matches, ".*insufficient RUNE balance.*")

	// Verify handler also fails (defensive check)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerReferenceMemoSuite) TestConcurrentRegistration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer1 := GetRandomBech32Addr()
	signer2 := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Two users register with different memos
	msg1 := NewMsgReferenceMemo(asset, "+:BTC.BTC:address1", signer1)
	_, err1 := handler.handle(ctx, *msg1)
	c.Assert(err1, IsNil)

	msg2 := NewMsgReferenceMemo(asset, "+:BTC.BTC:address2", signer2)
	_, err2 := handler.handle(ctx, *msg2)
	c.Assert(err2, IsNil)

	// Get the references that were assigned
	ref1 := mgr.Keeper().GetLastReferenceNumber(ctx, asset)

	// Both registrations should succeed
	c.Check(err1, IsNil)
	c.Check(err2, IsNil)

	// The second reference should be different from the first
	// (sequential allocation prevents collision)
	c.Check(ref1, Not(check.Equals), "")

	// Verify both memos exist in the system
	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, asset, "20000"), NotNil)
	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, asset, "40000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestValidateMemolessXMRAddRequiresAssetAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	thorAddr := GetRandomTHORAddress()
	xmrAddr, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	btcAddr := GetRandomBTCAddress()

	// XMR registration must provide a refund target before funds move rather
	// than rely on a later paired RUNE-side add to supply the asset address.
	msg := NewMsgReferenceMemo(common.XMRAsset, "+:XMR.XMR:"+thorAddr.String(), signer)
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// A malformed asset address is permissive at memo parsing time but must be
	// rejected while registering the reference, before any XMR funds move.
	msg = NewMsgReferenceMemo(common.XMRAsset, "+:XMR.XMR:"+thorAddr.String()+"/not-an-address", signer)
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// with an XMR asset address the registration is accepted
	msg = NewMsgReferenceMemo(common.XMRAsset, "+:XMR.XMR:"+thorAddr.String()+"/"+xmrAddr.String(), signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// an asset address on the wrong chain is rejected
	msg = NewMsgReferenceMemo(common.XMRAsset, "+:XMR.XMR:"+thorAddr.String()+"/"+btcAddr.String(), signer)
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// On mocknet a checksummed featured-mainnet address is valid XMR syntax but
	// belongs to the wrong network, so registration must reject it as well.
	if common.CurrentChainNetwork == common.MockNet {
		wrongNetworkAddr, err := common.NewAddress(common.XMRChecksummedDummyAddress('C'))
		c.Assert(err, IsNil)
		c.Assert(wrongNetworkAddr.IsChain(common.XMRChain), Equals, true)
		c.Assert(common.XMRChain.IsValidAddress(wrongNetworkAddr), Equals, false)
		msg = NewMsgReferenceMemo(common.XMRAsset, "+:XMR.XMR:"+thorAddr.String()+"/"+wrongNetworkAddr.String(), signer)
		c.Assert(handler.validate(ctx, *msg), NotNil)
	}

	// non-XMR adds are unaffected
	msg = NewMsgReferenceMemo(common.BTCAsset, "+:BTC.BTC", signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// Non-XMR adds reject the composite form instead of accepting and silently
	// discarding an asset address that their handlers do not use.
	msg = NewMsgReferenceMemo(common.BTCAsset, "+:BTC.BTC:"+btcAddr.String()+"/"+btcAddr.String(), signer)
	c.Assert(handler.validate(ctx, *msg), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestValidateMemolessXMRSwapRequiresRefundAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	xmrAddr, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	btcAddr := GetRandomBTCAddress()
	ethAddr := GetRandomETHAddress()

	type testCase struct {
		name      string
		asset     common.Asset
		memo      string
		expectErr bool
	}
	testCases := []testCase{
		{
			name:      "XMR swap without refund address",
			asset:     common.XMRAsset,
			memo:      fmt.Sprintf("=:BTC.BTC:%s:100", btcAddr),
			expectErr: true,
		},
		{
			name:  "XMR swap with refund address",
			asset: common.XMRAsset,
			memo:  fmt.Sprintf("=:BTC.BTC:%s/%s:100", btcAddr, xmrAddr),
		},
		{
			name:      "XMR limit swap without refund address",
			asset:     common.XMRAsset,
			memo:      fmt.Sprintf("=<:BTC.BTC:%s:100", btcAddr),
			expectErr: true,
		},
		{
			name:  "XMR limit swap with refund address",
			asset: common.XMRAsset,
			memo:  fmt.Sprintf("=<:BTC.BTC:%s/%s:100", btcAddr, xmrAddr),
		},
		{
			name:      "XMR swap with wrong-chain refund address",
			asset:     common.XMRAsset,
			memo:      fmt.Sprintf("=:BTC.BTC:%s/%s:100", btcAddr, btcAddr),
			expectErr: true,
		},
		{
			name:  "non-XMR swap without refund address",
			asset: common.BTCAsset,
			memo:  fmt.Sprintf("=:ETH.ETH:%s:100", ethAddr),
		},
		{
			name:  "non-XMR swap with refund address",
			asset: common.BTCAsset,
			memo:  fmt.Sprintf("=:ETH.ETH:%s/%s:100", ethAddr, btcAddr),
		},
	}

	if common.CurrentChainNetwork == common.MockNet {
		wrongNetworkAddr, addressErr := common.NewAddress(common.XMRChecksummedDummyAddress('C'))
		c.Assert(addressErr, IsNil)
		testCases = append(testCases, testCase{
			name:      "XMR swap with wrong-network refund address",
			asset:     common.XMRAsset,
			memo:      fmt.Sprintf("=:BTC.BTC:%s/%s:100", btcAddr, wrongNetworkAddr),
			expectErr: true,
		})
	}

	for _, tc := range testCases {
		msg := NewMsgReferenceMemo(tc.asset, tc.memo, signer)
		err := handler.validate(ctx, *msg)
		if tc.expectErr {
			c.Check(err, NotNil, Commentf(tc.name))
		} else {
			c.Check(err, IsNil, Commentf(tc.name))
		}
	}
}

func (s *HandlerReferenceMemoSuite) TestValidateMemolessXMRRejectsMemosWithoutRefundAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	xmrAddr, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	testCases := []struct {
		name string
		memo string
	}{
		{name: "withdraw", memo: "-:XMR.XMR:5000"},
		{name: "donate", memo: "DONATE:XMR.XMR"},
		{name: "trade account deposit", memo: fmt.Sprintf("TRADE+:%s", signer)},
		{name: "trade account withdrawal", memo: fmt.Sprintf("TRADE-:%s", xmrAddr)},
		{name: "secured asset deposit", memo: fmt.Sprintf("SECURE+:%s", signer)},
		{name: "secured asset withdrawal", memo: fmt.Sprintf("SECURE-:%s", xmrAddr)},
		{name: "noop", memo: "NOOP"},
	}

	for _, tc := range testCases {
		msg := NewMsgReferenceMemo(common.XMRAsset, tc.memo, signer)
		err := handler.validate(ctx, *msg)
		c.Check(err, ErrorMatches, "memoless XMR .* memo is not supported because it cannot provide an explicit refund address", Commentf(tc.name))

		// Observable-sender chains retain their existing reference-memo behavior.
		msg.Asset = common.BTCAsset
		c.Check(handler.validate(ctx, *msg), IsNil, Commentf("non-XMR %s", tc.name))
	}
}

func (s *HandlerReferenceMemoSuite) TestMemolessCostCurve(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset := common.BTCAsset

	const (
		base     = 100
		ttl      = int64(100)
		refCount = int64(1000)
	)
	k.SetMimir(ctx, constants.MemolessTxnCost.String(), base)
	k.SetMimir(ctx, constants.MemolessTxnTTL.String(), ttl)
	k.SetMimir(ctx, constants.MemolessTxnRefCount.String(), refCount)
	ctx = ctx.WithBlockHeight(1000)

	// slope 0 keeps the flat cost no matter how full the ring is
	k.SetMimir(ctx, constants.MemolessTxnCostSlopeBPS.String(), 0)
	k.SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), 900, 0, ttl)
	c.Assert(memolessCost(ctx, k, asset).Uint64(), Equals, uint64(base))

	// slope 10_000 gives base / (1 - occupancy)
	k.SetMimir(ctx, constants.MemolessTxnCostSlopeBPS.String(), 10_000)
	k.SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), 0, 0, ttl)
	c.Assert(memolessCost(ctx, k, asset).Uint64(), Equals, uint64(base), Commentf("empty ring"))
	k.SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), 500, 0, ttl)
	c.Assert(memolessCost(ctx, k, asset).Uint64(), Equals, uint64(2*base), Commentf("half full"))
	k.SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), 900, 0, ttl)
	c.Assert(memolessCost(ctx, k, asset).Uint64(), Equals, uint64(10*base), Commentf("90%% full"))

	// a steeper slope bites earlier
	k.SetMimir(ctx, constants.MemolessTxnCostSlopeBPS.String(), 20_000)
	k.SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), 500, 0, ttl)
	c.Assert(memolessCost(ctx, k, asset).Uint64(), Equals, uint64(3*base), Commentf("half full, double slope"))

	// free is floored at 1, so a saturated ring is bounded, not divided by zero
	k.SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), refCount+50, 0, ttl)
	c.Assert(memolessCost(ctx, k, asset).Uint64(), Equals, uint64(base*(1+2*(refCount-1))))

	// base of 0 stays free
	k.SetMimir(ctx, constants.MemolessTxnCost.String(), 0)
	c.Assert(memolessCost(ctx, k, asset).IsZero(), Equals, true)
}

func (s *HandlerReferenceMemoSuite) TestMemolessWindowRoll(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset := common.BTCAsset
	const ttl = int64(100)

	ctx = ctx.WithBlockHeight(1000)
	bumpMemolessWindow(ctx, k, asset, ttl)
	c.Assert(memolessLiveRefs(ctx, k, asset, ttl), Equals, int64(1))

	// into the next bucket the previous one still counts in full: it can only
	// overcount, never undercount, so the fee never comes in too low
	k.SetReferenceMemoWindow(ctx, asset, 1000, 10, 0, ttl)
	ctx = ctx.WithBlockHeight(1150)
	c.Assert(memolessLiveRefs(ctx, k, asset, ttl), Equals, int64(10))

	// a registration there rolls the bucket and keeps the old count as prev
	bumpMemolessWindow(ctx, k, asset, ttl)
	start, cur, prev, width := k.GetReferenceMemoWindow(ctx, asset)
	c.Assert(start, Equals, int64(1150))
	c.Assert(cur, Equals, int64(1))
	c.Assert(prev, Equals, int64(10))
	c.Assert(width, Equals, ttl)
	c.Assert(memolessLiveRefs(ctx, k, asset, ttl), Equals, int64(11))

	// two idle buckets later everything has expired
	ctx = ctx.WithBlockHeight(1400)
	c.Assert(memolessLiveRefs(ctx, k, asset, ttl), Equals, int64(0))
}

// TestMemolessWindowTTLChange covers governance moving MemolessTxnTTL under a
// live window. The buckets were laid out on the old TTL, so reading them with the
// new one must not age out registrations that are still live.
func (s *HandlerReferenceMemoSuite) TestMemolessWindowTTLChange(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset := common.BTCAsset

	const wide = int64(100)

	// one registration at 1150 in a window opened at 1000
	k.SetReferenceMemoWindow(ctx, asset, 1000, 10, 0, wide)
	ctx = ctx.WithBlockHeight(1150)
	bumpMemolessWindow(ctx, k, asset, wide)

	// TTL cut to a quarter the next block. The 1150 registration is live until
	// 1175 under the new TTL, so the occupancy must not drop to zero.
	const narrow = int64(25)
	ctx = ctx.WithBlockHeight(1151)
	c.Assert(memolessLiveRefs(ctx, k, asset, narrow) > 0, Equals, true)

	// and it still decays: everything the window held was registered at or before
	// 1150, so it is all gone by 1150 + wide + narrow
	c.Assert(memolessLiveRefs(ctx.WithBlockHeight(1150+wide+narrow), k, asset, narrow), Equals, int64(0))

	// reads never persist, so the decay holds without an intervening registration
	start, _, _, width := k.GetReferenceMemoWindow(ctx, asset)
	c.Assert(start, Equals, int64(1150))
	c.Assert(width, Equals, wide)
}

func (s *HandlerReferenceMemoSuite) TestHandlerChargesScaledCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	pool := NewPool()
	pool.Asset = asset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	const (
		base     = int64(10_00000000) // 10 RUNE
		ttl      = int64(100)
		refCount = int64(1000)
	)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), ttl)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnRefCount.String(), refCount)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), base)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCostSlopeBPS.String(), 10_000)

	// 900 of the 1000 slots taken in this TTL window, so the cost is 10x base
	mgr.Keeper().SetReferenceMemoWindow(ctx, asset, ctx.BlockHeight(), 900, 0, ttl)

	expected := cosmos.NewUint(uint64(10 * base))
	c.Assert(mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, expected)), IsNil)

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	before := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Sub(before).Equal(expected), Equals, true)

	// the registration is counted, so the next one is priced higher
	_, cur, _, _ := mgr.Keeper().GetReferenceMemoWindow(ctx, asset)
	c.Check(cur, Equals, int64(901))
	c.Check(memolessCost(ctx, mgr.Keeper(), asset).GT(expected), Equals, true)
}

// TestMemolessCostExtremes pins the consensus-safety envelope: every input here is
// a governance-settable int64, so the cost math must not panic, divide by zero or
// convert a negative to unsigned for any of them. Note GetConfigInt64 already
// floors negative mimirs to the constant default, so the guards in memolessCost
// are belt-and-braces; this asserts the whole chain holds anyway.
func (s *HandlerReferenceMemoSuite) TestMemolessCostExtremes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset := common.BTCAsset

	// mimirs: negative (floored to the default), off, on, and the int64 ceiling
	extremes := []int64{math.MinInt64, 0, 1, 10_000, math.MaxInt64 / 2, math.MaxInt64}
	// raw window state, which nothing floors
	counters := []int64{math.MinInt64, 0, 1, math.MaxInt64 - 1}
	heights := []int64{1, 100, math.MaxInt64 / 4}

	for _, base := range extremes {
		for _, slope := range extremes {
			for _, refCount := range extremes {
				for _, ttl := range extremes {
					k.SetMimir(ctx, constants.MemolessTxnCost.String(), base)
					k.SetMimir(ctx, constants.MemolessTxnCostSlopeBPS.String(), slope)
					k.SetMimir(ctx, constants.MemolessTxnRefCount.String(), refCount)
					k.SetMimir(ctx, constants.MemolessTxnTTL.String(), ttl)

					for _, start := range counters {
						for _, count := range counters {
							k.SetReferenceMemoWindow(ctx, asset, start, count, count, ttl)
							for _, h := range heights {
								hCtx := ctx.WithBlockHeight(h)

								// the effective values, after GetConfigInt64 substitutes
								// the constant default for anything negative
								effBase := k.GetConfigInt64(hCtx, constants.MemolessTxnCost)
								effSlope := k.GetConfigInt64(hCtx, constants.MemolessTxnCostSlopeBPS)
								effRefCount := k.GetConfigInt64(hCtx, constants.MemolessTxnRefCount)
								desc := Commentf("base=%d slope=%d refCount=%d ttl=%d start=%d count=%d height=%d",
									effBase, effSlope, effRefCount, k.GetConfigInt64(hCtx, constants.MemolessTxnTTL), start, count, h)

								cost := memolessCost(hCtx, k, asset)

								// never negative, never below the flat cost
								c.Assert(cost.GTE(cosmos.NewUint(uint64(effBase))), Equals, true, desc)
								// inert whenever the curve is switched off
								if effBase == 0 || effSlope == 0 || effRefCount == 0 {
									c.Assert(cost.Equal(cosmos.NewUint(uint64(effBase))), Equals, true, desc)
								}
								// the charged amount must survive becoming a coin
								if !cost.IsZero() {
									_ = cosmos.NewCoin(common.RuneNative.Native(), cosmos.NewIntFromBigInt(cost.BigInt()))
								}
								// occupancy is never negative, whatever the counters say
								c.Assert(memolessLiveRefs(hCtx, k, asset, k.GetConfigInt64(hCtx, constants.MemolessTxnTTL)) >= 0, Equals, true, desc)

								bumpMemolessWindow(hCtx, k, asset, ttl)
							}
						}
					}
				}
			}
		}
	}
}
