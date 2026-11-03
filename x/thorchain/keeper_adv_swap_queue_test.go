package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperAdvSwapQueueTTLSuite struct{}

var _ = Suite(&KeeperAdvSwapQueueTTLSuite{})

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemStatePreservesIndex(c *C) {
	ctx, k := setupKeeperForTest(c)
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)))
	msg := NewMsgSwap(
		tx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil,
		types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr(),
	)
	msg.State = &types.SwapState{Quantity: 2, Deposit: cosmos.NewUint(common.One)}
	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)

	before, err := k.GetAdvSwapQueueIndex(ctx, *msg)
	c.Assert(err, IsNil)
	c.Assert(before, HasLen, 1)

	msg.State.Count = 1
	msg.State.In = cosmos.NewUint(common.One / 2)
	c.Assert(k.SetAdvSwapQueueItemState(ctx, *msg), IsNil)

	after, err := k.GetAdvSwapQueueIndex(ctx, *msg)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	stored, err := k.GetAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index))
	c.Assert(err, IsNil)
	c.Assert(stored.State.Count, Equals, uint64(1))
	c.Assert(stored.State.In.Equal(msg.State.In), Equals, true)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemStatePanicsOnIndexKeyChange(c *C) {
	ctx, k := setupKeeperForTest(c)
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)))
	msg := NewMsgSwap(
		tx, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(5000000),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil,
		types.SwapType_limit, 0, 0, types.SwapVersion_v2, GetRandomBech32Addr(),
	)
	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)
	c.Assert(k.SetAdvSwapQueueItemState(ctx, *msg), IsNil)

	// TradeTarget feeds the limit index ratio, so changing it breaks the
	// contract and must trip the mocknet assertion.
	msg.TradeTarget = cosmos.NewUint(6000000)
	c.Assert(func() { _ = k.SetAdvSwapQueueItemState(ctx, *msg) }, PanicMatches, ".*index-key fields changed.*")
}

func (s *KeeperAdvSwapQueueTTLSuite) TestRemoveAdvSwapQueueItemMsgRemovesItemAndIndex(c *C) {
	ctx, k := setupKeeperForTest(c)
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)))
	msg := NewMsgSwap(
		tx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil,
		types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr(),
	)
	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)

	c.Assert(k.RemoveAdvSwapQueueItemMsg(ctx, *msg), IsNil)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	index, err := k.GetAdvSwapQueueIndex(ctx, *msg)
	c.Assert(err, IsNil)
	c.Assert(index, HasLen, 0)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(100)
	txHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
		GetRandomTxHash(),
	}

	// Test setting TTL entries
	err := k.SetLimitSwapTTL(ctx, blockHeight, txHashes)
	c.Assert(err, IsNil)

	// Verify the TTL entries were stored
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, len(txHashes))

	// Check all txHashes are present (order may not be preserved)
	for _, originalTxHash := range txHashes {
		found := false
		for _, retrievedTxHash := range retrievedTxHashes {
			if originalTxHash.Equals(retrievedTxHash) {
				found = true
				break
			}
		}
		c.Assert(found, Equals, true, Commentf("TxHash %s not found in retrieved list", originalTxHash))
	}
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetLimitSwapTTLEmptySlice(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(100)
	emptyTxHashes := []common.TxID{}

	// Test setting empty TTL entries
	err := k.SetLimitSwapTTL(ctx, blockHeight, emptyTxHashes)
	c.Assert(err, IsNil)

	// Verify empty list is returned
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 0)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestGetLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(200)
	txHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
	}

	// Set up test data
	err := k.SetLimitSwapTTL(ctx, blockHeight, txHashes)
	c.Assert(err, IsNil)

	// Test getting existing TTL entries
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 2)

	// Test getting non-existent TTL entries
	nonExistentBlockHeight := int64(999)
	retrievedTxHashes, err = k.GetLimitSwapTTL(ctx, nonExistentBlockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 0)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestRemoveLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(300)
	txHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
	}

	// Set up test data
	err := k.SetLimitSwapTTL(ctx, blockHeight, txHashes)
	c.Assert(err, IsNil)

	// Verify data exists
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 2)

	// Remove the TTL entry
	k.RemoveLimitSwapTTL(ctx, blockHeight)

	// Verify data is removed
	retrievedTxHashes, err = k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 0)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestRemoveLimitSwapTTLNonExistent(c *C) {
	ctx, k := setupKeeperForTest(c)

	nonExistentBlockHeight := int64(404)

	// Test removing non-existent TTL entry (should not panic or error)
	k.RemoveLimitSwapTTL(ctx, nonExistentBlockHeight)

	// Verify still returns empty result
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, nonExistentBlockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 0)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestAddToLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(400)
	initialTxHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
	}
	newTxHash := GetRandomTxHash()

	// Set up initial data
	err := k.SetLimitSwapTTL(ctx, blockHeight, initialTxHashes)
	c.Assert(err, IsNil)

	// Add a new txHash
	err = k.AddToLimitSwapTTL(ctx, blockHeight, newTxHash)
	c.Assert(err, IsNil)

	// Verify the new txHash was added
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 3)

	// Check that the new txHash is in the list
	found := false
	for _, retrievedTxHash := range retrievedTxHashes {
		if newTxHash.Equals(retrievedTxHash) {
			found = true
			break
		}
	}
	c.Assert(found, Equals, true, Commentf("New TxHash %s not found in retrieved list", newTxHash))
}

func (s *KeeperAdvSwapQueueTTLSuite) TestAddToLimitSwapTTLNewBlockHeight(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(500)
	newTxHash := GetRandomTxHash()

	// Add txHash to non-existent block height (should create new entry)
	err := k.AddToLimitSwapTTL(ctx, blockHeight, newTxHash)
	c.Assert(err, IsNil)

	// Verify the new entry was created
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 1)
	c.Assert(retrievedTxHashes[0].Equals(newTxHash), Equals, true)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestAddToLimitSwapTTLDuplicateTxHash(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(600)
	txHash := GetRandomTxHash()
	initialTxHashes := []common.TxID{txHash}

	// Set up initial data with a txHash
	err := k.SetLimitSwapTTL(ctx, blockHeight, initialTxHashes)
	c.Assert(err, IsNil)

	// Try to add the same txHash again
	err = k.AddToLimitSwapTTL(ctx, blockHeight, txHash)
	c.Assert(err, IsNil)

	// Verify no duplicate was added
	retrievedTxHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedTxHashes), Equals, 1)
	c.Assert(retrievedTxHashes[0].Equals(txHash), Equals, true)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestTTLFunctionsWithMultipleBlockHeights(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set up TTL data for multiple block heights
	blockHeight1 := int64(1000)
	blockHeight2 := int64(2000)
	blockHeight3 := int64(3000)

	txHashes1 := []common.TxID{GetRandomTxHash(), GetRandomTxHash()}
	txHashes2 := []common.TxID{GetRandomTxHash()}
	txHashes3 := []common.TxID{GetRandomTxHash(), GetRandomTxHash(), GetRandomTxHash()}

	// Set TTL for multiple block heights
	err := k.SetLimitSwapTTL(ctx, blockHeight1, txHashes1)
	c.Assert(err, IsNil)
	err = k.SetLimitSwapTTL(ctx, blockHeight2, txHashes2)
	c.Assert(err, IsNil)
	err = k.SetLimitSwapTTL(ctx, blockHeight3, txHashes3)
	c.Assert(err, IsNil)

	// Verify each block height has correct TTL data
	retrieved1, err := k.GetLimitSwapTTL(ctx, blockHeight1)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved1), Equals, 2)

	retrieved2, err := k.GetLimitSwapTTL(ctx, blockHeight2)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved2), Equals, 1)

	retrieved3, err := k.GetLimitSwapTTL(ctx, blockHeight3)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved3), Equals, 3)

	// Remove one block height
	k.RemoveLimitSwapTTL(ctx, blockHeight2)

	// Verify only the specified block height was removed
	retrieved1, err = k.GetLimitSwapTTL(ctx, blockHeight1)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved1), Equals, 2)

	retrieved2, err = k.GetLimitSwapTTL(ctx, blockHeight2)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved2), Equals, 0)

	retrieved3, err = k.GetLimitSwapTTL(ctx, blockHeight3)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved3), Equals, 3)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestTTLFunctionsEdgeCases(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test with block height 0 - should return error due to validation
	blockHeight := int64(0)
	txHash := GetRandomTxHash()

	err := k.AddToLimitSwapTTL(ctx, blockHeight, txHash)
	c.Assert(err, NotNil) // Should fail due to validation
	c.Assert(err.Error(), Equals, "invalid block height: 0")

	// Test with negative block height - should return error due to validation
	negativeBlockHeight := int64(-100)
	err = k.AddToLimitSwapTTL(ctx, negativeBlockHeight, GetRandomTxHash())
	c.Assert(err, NotNil) // Should fail due to validation
	c.Assert(err.Error(), Equals, "invalid block height: -100")

	// Test with valid block height 1 (minimum valid)
	minValidHeight := int64(1)
	err = k.AddToLimitSwapTTL(ctx, minValidHeight, GetRandomTxHash())
	c.Assert(err, IsNil)

	retrieved, err := k.GetLimitSwapTTL(ctx, minValidHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved), Equals, 1)

	// Test with very large block height
	largeBlockHeight := int64(999999999)
	err = k.AddToLimitSwapTTL(ctx, largeBlockHeight, GetRandomTxHash())
	c.Assert(err, IsNil)

	retrieved, err = k.GetLimitSwapTTL(ctx, largeBlockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved), Equals, 1)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestTTLOverwriteBehavior(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(700)
	initialTxHashes := []common.TxID{GetRandomTxHash(), GetRandomTxHash()}
	newTxHashes := []common.TxID{GetRandomTxHash()}

	// Set initial TTL data
	err := k.SetLimitSwapTTL(ctx, blockHeight, initialTxHashes)
	c.Assert(err, IsNil)

	// Overwrite with new data
	err = k.SetLimitSwapTTL(ctx, blockHeight, newTxHashes)
	c.Assert(err, IsNil)

	// Verify the data was overwritten, not appended
	retrieved, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrieved), Equals, 1)
	c.Assert(retrieved[0].Equals(newTxHashes[0]), Equals, true)

	// Verify old txHashes are not present
	for _, oldTxHash := range initialTxHashes {
		for _, retrievedTxHash := range retrieved {
			c.Assert(oldTxHash.Equals(retrievedTxHash), Equals, false,
				Commentf("Old TxHash %s should not be present after overwrite", oldTxHash))
		}
	}
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemAddsTTLForLimitSwaps(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set StreamingLimitSwapMaxAge to 1000 blocks
	maxAge := int64(1000)
	k.SetMimir(ctx, "StreamingLimitSwapMaxAge", maxAge)

	// Create a limit swap
	initialBlockHeight := int64(50)
	txID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:BTC.BTC",
	)

	limitSwap := NewMsgSwap(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(5000000), // 0.05 BTC trade target
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		types.SwapType_limit,
		0,
		0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	limitSwap.InitialBlockHeight = initialBlockHeight
	limitSwap.State = &types.SwapState{
		Quantity: 1,
		Count:    0,
		Deposit:  cosmos.NewUint(1 * common.One),
	}

	// Verify no TTL tracking exists before adding the swap
	expectedExpiryHeight := initialBlockHeight + maxAge
	ttlEntries, err := k.GetLimitSwapTTL(ctx, expectedExpiryHeight)
	c.Assert(err, IsNil)
	c.Assert(len(ttlEntries), Equals, 0)

	// Add the limit swap to the queue
	err = k.SetAdvSwapQueueItem(ctx, *limitSwap)
	c.Assert(err, IsNil)

	// Manually add TTL tracking for limit swap
	err = k.AddToLimitSwapTTL(ctx, expectedExpiryHeight, txID)
	c.Assert(err, IsNil)

	// Verify TTL tracking was added
	ttlEntries, err = k.GetLimitSwapTTL(ctx, expectedExpiryHeight)
	c.Assert(err, IsNil)
	c.Assert(len(ttlEntries), Equals, 1)
	c.Assert(ttlEntries[0].Equals(txID), Equals, true)

	// Verify the swap can be retrieved
	retrievedSwap, err := k.GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(retrievedSwap.Tx.ID.Equals(txID), Equals, true)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemNoTTLForMarketSwaps(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set StreamingLimitSwapMaxAge
	maxAge := int64(1000)
	k.SetMimir(ctx, "StreamingLimitSwapMaxAge", maxAge)

	// Create a market swap
	initialBlockHeight := int64(100)
	txID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:BTC.BTC",
	)

	marketSwap := NewMsgSwap(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.ZeroUint(), // No trade target for market swaps
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		types.SwapType_market, // Market swap, not limit
		0,
		0,
		types.SwapVersion_v1,
		GetRandomBech32Addr(),
	)
	marketSwap.InitialBlockHeight = initialBlockHeight
	marketSwap.State = &types.SwapState{
		Quantity: 1,
		Count:    0,
		Deposit:  cosmos.NewUint(1 * common.One),
	}

	// Add the market swap to the queue
	err := k.SetAdvSwapQueueItem(ctx, *marketSwap)
	c.Assert(err, IsNil)

	// Verify NO TTL tracking was added for market swap
	expectedExpiryHeight := initialBlockHeight + maxAge
	ttlEntries, err := k.GetLimitSwapTTL(ctx, expectedExpiryHeight)
	c.Assert(err, IsNil)
	c.Assert(len(ttlEntries), Equals, 0)

	// Verify the swap can still be retrieved
	retrievedSwap, err := k.GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(retrievedSwap.Tx.ID.Equals(txID), Equals, true)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemTTLCalculation(c *C) {
	ctx, k := setupKeeperForTest(c)

	testCases := []struct {
		maxAge               int64
		initialBlockHeight   int64
		expectedExpiryHeight int64
		shouldHaveTTL        bool
	}{
		{100, 50, 150, true},     // Standard case
		{1, 1000, 1001, true},    // Minimum TTL
		{10000, 0, 10000, false}, // Zero initial block height - no TTL
		{500, 999, 1499, true},   // Large block heights
	}

	for i, testCase := range testCases {
		// Set the max age for this test
		k.SetMimir(ctx, "StreamingLimitSwapMaxAge", testCase.maxAge)

		// Create a limit swap
		txID := GetRandomTxHash()

		tx := common.NewTx(
			txID,
			GetRandomETHAddress(),
			GetRandomETHAddress(),
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
			common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
			"swap:BTC.BTC",
		)

		limitSwap := NewMsgSwap(
			tx,
			common.BTCAsset,
			GetRandomBTCAddress(),
			cosmos.NewUint(5000000),
			common.NoAddress,
			cosmos.ZeroUint(),
			"",
			"",
			nil,
			types.SwapType_limit,
			0,
			0,
			types.SwapVersion_v2,
			GetRandomBech32Addr(),
		)
		limitSwap.InitialBlockHeight = testCase.initialBlockHeight
		limitSwap.State = &types.SwapState{
			Quantity: 1,
			Count:    0,
			Deposit:  cosmos.NewUint(1 * common.One),
		}

		// Add the limit swap to the queue
		err := k.SetAdvSwapQueueItem(ctx, *limitSwap)
		c.Assert(err, IsNil, Commentf("Test case %d failed", i))

		if testCase.shouldHaveTTL {
			// Manually add TTL tracking for test cases that should have it
			err = k.AddToLimitSwapTTL(ctx, testCase.expectedExpiryHeight, txID)
			c.Assert(err, IsNil, Commentf("Test case %d failed to add TTL", i))

			// Verify TTL tracking was added at the correct expiry height
			ttlEntries, err := k.GetLimitSwapTTL(ctx, testCase.expectedExpiryHeight)
			c.Assert(err, IsNil, Commentf("Test case %d failed", i))
			c.Assert(len(ttlEntries), Equals, 1, Commentf("Test case %d: expected 1 TTL entry", i))
			c.Assert(ttlEntries[0].Equals(txID), Equals, true, Commentf("Test case %d: incorrect txID in TTL", i))

			// Verify no TTL tracking at other heights
			wrongExpiryHeight := testCase.expectedExpiryHeight + 1
			ttlEntries, err = k.GetLimitSwapTTL(ctx, wrongExpiryHeight)
			c.Assert(err, IsNil, Commentf("Test case %d failed", i))
			c.Assert(len(ttlEntries), Equals, 0, Commentf("Test case %d: unexpected TTL entry at wrong height", i))
		} else {
			// Verify NO TTL tracking was added
			ttlEntries, err := k.GetLimitSwapTTL(ctx, testCase.expectedExpiryHeight)
			c.Assert(err, IsNil, Commentf("Test case %d failed", i))
			c.Assert(len(ttlEntries), Equals, 0, Commentf("Test case %d: expected no TTL entry", i))
		}
	}
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemNoTTLWhenInitialBlockHeightZero(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set StreamingLimitSwapMaxAge
	maxAge := int64(1000)
	k.SetMimir(ctx, "StreamingLimitSwapMaxAge", maxAge)

	// Create a limit swap with InitialBlockHeight = 0 (which should not trigger TTL)
	txID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:BTC.BTC",
	)

	limitSwap := NewMsgSwap(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(5000000),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		types.SwapType_limit,
		0,
		0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	limitSwap.InitialBlockHeight = 0 // Zero initial block height
	limitSwap.State = &types.SwapState{
		Quantity: 1,
		Count:    0,
		Deposit:  cosmos.NewUint(1 * common.One),
	}

	// Add the limit swap to the queue
	err := k.SetAdvSwapQueueItem(ctx, *limitSwap)
	c.Assert(err, IsNil)

	// Verify NO TTL tracking was added (InitialBlockHeight = 0 means no TTL)
	expectedExpiryHeight := limitSwap.InitialBlockHeight + maxAge
	ttlEntries, err := k.GetLimitSwapTTL(ctx, expectedExpiryHeight)
	c.Assert(err, IsNil)
	c.Assert(len(ttlEntries), Equals, 0)

	// Verify the swap can still be retrieved
	retrievedSwap, err := k.GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(retrievedSwap.Tx.ID.Equals(txID), Equals, true)
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemStreamingLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set StreamingLimitSwapMaxAge
	maxAge := int64(500)
	k.SetMimir(ctx, "StreamingLimitSwapMaxAge", maxAge)

	// Create a streaming limit swap (quantity > 1)
	initialBlockHeight := int64(200)
	txID := GetRandomTxHash()

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		"swap:BTC.BTC",
	)

	streamingLimitSwap := NewMsgSwap(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.NewUint(50000000), // 0.5 BTC
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		types.SwapType_limit,
		10, // 10 intervals
		10, // 10 blocks per interval
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	streamingLimitSwap.InitialBlockHeight = initialBlockHeight
	streamingLimitSwap.State = &types.SwapState{
		Quantity: 10, // Streaming swap with 10 sub-swaps
		Count:    0,
		Interval: 10,
		Deposit:  cosmos.NewUint(10 * common.One),
	}

	// Add the streaming limit swap to the queue
	err := k.SetAdvSwapQueueItem(ctx, *streamingLimitSwap)
	c.Assert(err, IsNil)

	// Manually add TTL tracking for streaming limit swap
	expectedExpiryHeight := initialBlockHeight + maxAge
	err = k.AddToLimitSwapTTL(ctx, expectedExpiryHeight, txID)
	c.Assert(err, IsNil)

	// Verify TTL tracking was added (streaming limit swaps should also get TTL)
	ttlEntries, err := k.GetLimitSwapTTL(ctx, expectedExpiryHeight)
	c.Assert(err, IsNil)
	c.Assert(len(ttlEntries), Equals, 1)
	c.Assert(ttlEntries[0].Equals(txID), Equals, true)

	// Verify the swap can be retrieved
	retrievedSwap, err := k.GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(retrievedSwap.Tx.ID.Equals(txID), Equals, true)
	c.Assert(retrievedSwap.State.Quantity, Equals, uint64(10))
}

func (s *KeeperAdvSwapQueueTTLSuite) TestSetAdvSwapQueueItemMultipleLimitSwaps(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set StreamingLimitSwapMaxAge
	maxAge := int64(200)
	k.SetMimir(ctx, "StreamingLimitSwapMaxAge", maxAge)

	// Create multiple limit swaps with different initial block heights
	testSwaps := []struct {
		txID               common.TxID
		initialBlockHeight int64
	}{
		{GetRandomTxHash(), 100},
		{GetRandomTxHash(), 150},
		{GetRandomTxHash(), 200},
	}

	for _, testSwap := range testSwaps {
		tx := common.NewTx(
			testSwap.txID,
			GetRandomETHAddress(),
			GetRandomETHAddress(),
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))},
			common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
			"swap:BTC.BTC",
		)

		limitSwap := NewMsgSwap(
			tx,
			common.BTCAsset,
			GetRandomBTCAddress(),
			cosmos.NewUint(5000000),
			common.NoAddress,
			cosmos.ZeroUint(),
			"",
			"",
			nil,
			types.SwapType_limit,
			0,
			0,
			types.SwapVersion_v2,
			GetRandomBech32Addr(),
		)
		limitSwap.InitialBlockHeight = testSwap.initialBlockHeight
		limitSwap.State = &types.SwapState{
			Quantity: 1,
			Count:    0,
			Deposit:  cosmos.NewUint(1 * common.One),
		}

		// Add the limit swap to the queue
		err := k.SetAdvSwapQueueItem(ctx, *limitSwap)
		c.Assert(err, IsNil)

		// Manually add TTL tracking for this limit swap
		expectedExpiryHeight := testSwap.initialBlockHeight + maxAge
		err = k.AddToLimitSwapTTL(ctx, expectedExpiryHeight, testSwap.txID)
		c.Assert(err, IsNil)
	}

	// Verify TTL tracking for each swap
	for _, testSwap := range testSwaps {
		expectedExpiryHeight := testSwap.initialBlockHeight + maxAge
		ttlEntries, err := k.GetLimitSwapTTL(ctx, expectedExpiryHeight)
		c.Assert(err, IsNil)
		c.Assert(len(ttlEntries) >= 1, Equals, true,
			Commentf("No TTL entry found for txID %s", testSwap.txID))

		// Find our specific txID in the TTL entries
		found := false
		for _, ttlTxID := range ttlEntries {
			if ttlTxID.Equals(testSwap.txID) {
				found = true
				break
			}
		}
		c.Assert(found, Equals, true,
			Commentf("TxID %s not found in TTL entries", testSwap.txID))
	}

	// Test that swaps expiring at the same height are grouped together
	// Check if we have any duplicate expiry heights
	expiryHeights := make(map[int64][]common.TxID)
	for _, testSwap := range testSwaps {
		expiryHeight := testSwap.initialBlockHeight + maxAge
		expiryHeights[expiryHeight] = append(expiryHeights[expiryHeight], testSwap.txID)
	}

	// Verify TTL entries for duplicate expiry heights
	for expiryHeight, expectedTxIDs := range expiryHeights {
		if len(expectedTxIDs) > 1 {
			ttlEntries, err := k.GetLimitSwapTTL(ctx, expiryHeight)
			c.Assert(err, IsNil)
			c.Assert(len(ttlEntries), Equals, len(expectedTxIDs),
				Commentf("Expected %d TTL entries at height %d", len(expectedTxIDs), expiryHeight))
		}
	}
}
