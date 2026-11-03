package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type KeeperAdvSwapQueueSuite struct{}

var _ = Suite(&KeeperAdvSwapQueueSuite{})

func (s *KeeperAdvSwapQueueSuite) TestKeeperAdvSwapQueue(c *C) {
	ctx, k := setupKeeperForTest(c)

	// not found
	_, err := k.GetAdvSwapQueueItem(ctx, GetRandomTxHash(), 0)
	c.Assert(err, NotNil)

	msg1 := MsgSwap{
		Tx:          GetRandomTx(),
		TradeTarget: cosmos.NewUint(10 * common.One),
		SwapType:    types.SwapType_limit,
	}
	msg2 := MsgSwap{
		Tx:          GetRandomTx(),
		TradeTarget: cosmos.NewUint(10 * common.One),
		SwapType:    types.SwapType_limit,
	}

	c.Assert(k.SetAdvSwapQueueItem(ctx, msg1), IsNil)
	c.Assert(k.SetAdvSwapQueueItem(ctx, msg2), IsNil)
	msg3, err := k.GetAdvSwapQueueItem(ctx, msg1.Tx.ID, int(msg1.Index))
	c.Assert(err, IsNil)
	c.Check(msg3.Tx.ID.Equals(msg1.Tx.ID), Equals, true)

	c.Check(k.HasAdvSwapQueueItem(ctx, msg1.Tx.ID, int(msg1.Index)), Equals, true)
	ok, err := k.HasAdvSwapQueueIndex(ctx, msg1)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)

	iter := k.GetAdvSwapQueueItemIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var m MsgSwap
		k.Cdc().MustUnmarshal(iter.Value(), &m)
		c.Check(m.Tx.ID.Equals(msg1.Tx.ID) || m.Tx.ID.Equals(msg2.Tx.ID), Equals, true)
	}
	iter.Close()

	iter = k.GetAdvSwapQueueIndexIterator(ctx, msg1.SwapType, msg1.Tx.Coins[0].Asset, msg1.TargetAsset)
	for ; iter.Valid(); iter.Next() {
		hashes := make([]string, 0)
		ok, err = k.getStrings(ctx, iter.Key(), &hashes)
		c.Assert(err, IsNil)
		c.Check(ok, Equals, true)
		c.Check(hashes, HasLen, 2)
		c.Check(hashes[0], Equals, msg1.Tx.ID.String()+"-0")
		c.Check(hashes[1], Equals, msg2.Tx.ID.String()+"-0")
	}
	iter.Close()

	// test remove
	c.Assert(k.RemoveAdvSwapQueueItem(ctx, msg1.Tx.ID, int(msg1.Index)), IsNil)
	_, err = k.GetAdvSwapQueueItem(ctx, msg1.Tx.ID, int(msg1.Index))
	c.Check(err, NotNil)
	c.Check(k.HasAdvSwapQueueItem(ctx, msg1.Tx.ID, int(msg1.Index)), Equals, false)
	ok, err = k.HasAdvSwapQueueIndex(ctx, msg1)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, false)
}

func (s *KeeperAdvSwapQueueSuite) TestGetAdvSwapQueueIndexKey(c *C) {
	ctx, k := setupKeeperForTest(c)
	msg := MsgSwap{
		SwapType: types.SwapType_limit,
		Tx: common.Tx{
			Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10000))),
		},
		TargetAsset: common.RuneAsset(),
		TradeTarget: cosmos.NewUint(1239585),
	}
	c.Check(string(k.getAdvSwapQueueIndexKey(ctx, msg)), Equals, "aqlim//BTC.BTC>THOR.RUNE/000000000000806721/")
}

func (s *KeeperAdvSwapQueueSuite) TestRewriteRatio(c *C) {
	c.Check(rewriteRatio(3, "5"), Equals, "005")    // smaller
	c.Check(rewriteRatio(3, "5000"), Equals, "500") // larger
	c.Check(rewriteRatio(3, "500"), Equals, "500")  // just right
}

func (s *KeeperAdvSwapQueueSuite) TestRemoveSlice(c *C) {
	c.Check(removeString([]string{"foo", "bar", "baz"}, 0), DeepEquals, []string{"baz", "bar"})
	c.Check(removeString([]string{"foo", "bar", "baz"}, 1), DeepEquals, []string{"foo", "baz"})
	c.Check(removeString([]string{"foo", "bar", "baz"}, 2), DeepEquals, []string{"foo", "bar"})
	c.Check(removeString([]string{"foo", "bar", "baz"}, 3), DeepEquals, []string{"foo", "bar", "baz"})
	c.Check(removeString([]string{"foo", "bar", "baz"}, -1), DeepEquals, []string{"foo", "bar", "baz"})
}

func (s *KeeperAdvSwapQueueSuite) TestAdvSwapQueueIndexParsing(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test with standard 64-character hex transaction ID
	standardTxID := "A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49"
	standardTx := GetRandomTx()
	standardTx.ID = common.TxID(standardTxID)
	standardTx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100)))
	standardMsg := MsgSwap{
		Tx:          standardTx,
		TradeTarget: cosmos.NewUint(10 * common.One),
		SwapType:    types.SwapType_limit,
		Index:       5,
	}

	// Test with Cosmos indexed transaction ID (contains hyphen)
	cosmosIndexedTxID := "A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49-1"
	cosmosIndexedTx := GetRandomTx()
	cosmosIndexedTx.ID = common.TxID(cosmosIndexedTxID)
	cosmosIndexedTx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100)))
	cosmosIndexedMsg := MsgSwap{
		Tx:          cosmosIndexedTx,
		TradeTarget: cosmos.NewUint(10 * common.One),
		SwapType:    types.SwapType_limit,
		Index:       3,
	}

	// Set both items in the queue
	c.Assert(k.SetAdvSwapQueueItem(ctx, standardMsg), IsNil)
	c.Assert(k.SetAdvSwapQueueItem(ctx, cosmosIndexedMsg), IsNil)

	// Verify both can be retrieved correctly
	retrievedStandard, err := k.GetAdvSwapQueueItem(ctx, standardMsg.Tx.ID, int(standardMsg.Index))
	c.Assert(err, IsNil)
	c.Check(retrievedStandard.Tx.ID.Equals(standardMsg.Tx.ID), Equals, true)
	c.Check(retrievedStandard.Index, Equals, standardMsg.Index)

	retrievedCosmos, err := k.GetAdvSwapQueueItem(ctx, cosmosIndexedMsg.Tx.ID, int(cosmosIndexedMsg.Index))
	c.Assert(err, IsNil)
	c.Check(retrievedCosmos.Tx.ID.Equals(cosmosIndexedMsg.Tx.ID), Equals, true)
	c.Check(retrievedCosmos.Index, Equals, cosmosIndexedMsg.Index)

	// Test the index retrieval and parsing
	index, err := k.GetAdvSwapQueueIndex(ctx, standardMsg)
	c.Assert(err, IsNil)
	c.Assert(len(index), Equals, 2) // Should have both items

	// Verify that parsing works correctly for both transaction ID types
	foundStandard := false
	foundCosmos := false
	for _, item := range index {
		if item.TxID.Equals(standardMsg.Tx.ID) && item.Index == int(standardMsg.Index) {
			foundStandard = true
		}
		if item.TxID.Equals(cosmosIndexedMsg.Tx.ID) && item.Index == int(cosmosIndexedMsg.Index) {
			foundCosmos = true
		}
	}
	c.Check(foundStandard, Equals, true, Commentf("Standard transaction ID should be parsed correctly"))
	c.Check(foundCosmos, Equals, true, Commentf("Cosmos indexed transaction ID should be parsed correctly"))
}

func (s *KeeperAdvSwapQueueSuite) TestLastIndexParsing(c *C) {
	// Test the parsing logic directly by simulating swap queue index records
	testRecords := []string{
		"A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49-0",   // Standard TxID with swap index 0
		"A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49-1-0", // Cosmos indexed TxID (HASH-1) with swap index 0
		"A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49-1-5", // Cosmos indexed TxID (HASH-1) with swap index 5
	}

	// Manually test the parsing logic used in GetAdvSwapQueueIndex
	for _, rec := range testRecords {
		lastHyphenIndex := len(rec) - 1
		for i := len(rec) - 1; i >= 0; i-- {
			if rec[i] == '-' {
				lastHyphenIndex = i
				break
			}
		}
		c.Assert(lastHyphenIndex, Not(Equals), len(rec)-1, Commentf("Should find hyphen in: %s", rec))

		parts := []string{rec[:lastHyphenIndex], rec[lastHyphenIndex+1:]}
		c.Assert(len(parts), Equals, 2, Commentf("Should split into 2 parts: %s", rec))
		c.Check(parts[0], Not(Equals), "", Commentf("TxID part should not be empty: %s", rec))
		c.Check(parts[1], Not(Equals), "", Commentf("Index part should not be empty: %s", rec))

		// Verify the TxID can be parsed (this will work for both standard and Cosmos indexed TxIDs)
		_, err := common.NewTxID(parts[0])
		c.Check(err, IsNil, Commentf("Should parse TxID from: %s -> %s", rec, parts[0]))
	}
}

func (s *KeeperAdvSwapQueueSuite) TestLimitSwapTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test SetLimitSwapTTL and GetLimitSwapTTL
	blockHeight := int64(1000)
	txHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
		GetRandomTxHash(),
	}

	// Test setting TTL entries
	err := k.SetLimitSwapTTL(ctx, blockHeight, txHashes)
	c.Assert(err, IsNil)

	// Test getting TTL entries
	retrievedHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedHashes), Equals, 3)

	// Verify all hashes are present
	for i, originalHash := range txHashes {
		c.Assert(retrievedHashes[i].Equals(originalHash), Equals, true,
			Commentf("Hash %d should match", i))
	}

	// Test getting non-existent TTL entry (should return empty slice, not error)
	nonExistentHashes, err := k.GetLimitSwapTTL(ctx, blockHeight+1)
	c.Assert(err, IsNil, Commentf("Should not return error for non-existent TTL entry"))
	c.Assert(len(nonExistentHashes), Equals, 0, Commentf("Should return empty slice for non-existent TTL entry"))

	// Test updating existing TTL entry
	newTxHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
	}
	err = k.SetLimitSwapTTL(ctx, blockHeight, newTxHashes)
	c.Assert(err, IsNil)

	updatedHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(updatedHashes), Equals, 2)
	c.Assert(updatedHashes[0].Equals(newTxHashes[0]), Equals, true)
	c.Assert(updatedHashes[1].Equals(newTxHashes[1]), Equals, true)
}

func (s *KeeperAdvSwapQueueSuite) TestLimitSwapTTLValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test invalid block height (negative)
	err := k.SetLimitSwapTTL(ctx, -1, []common.TxID{GetRandomTxHash()})
	c.Assert(err, NotNil, Commentf("Should reject negative block height"))

	// Test invalid block height (zero)
	err = k.SetLimitSwapTTL(ctx, 0, []common.TxID{GetRandomTxHash()})
	c.Assert(err, NotNil, Commentf("Should reject zero block height"))

	// Test empty tx hash list
	err = k.SetLimitSwapTTL(ctx, 100, []common.TxID{})
	c.Assert(err, IsNil, Commentf("Should allow empty hash list"))

	// Test nil tx hash list
	err = k.SetLimitSwapTTL(ctx, 100, nil)
	c.Assert(err, IsNil, Commentf("Should allow nil hash list"))

	// Test getting with invalid block height
	_, err = k.GetLimitSwapTTL(ctx, -1)
	c.Assert(err, NotNil, Commentf("Should reject negative block height for get"))

	_, err = k.GetLimitSwapTTL(ctx, 0)
	c.Assert(err, NotNil, Commentf("Should reject zero block height for get"))
}

func (s *KeeperAdvSwapQueueSuite) TestLimitSwapTTLRemoval(c *C) {
	ctx, k := setupKeeperForTest(c)

	blockHeight := int64(500)
	txHashes := []common.TxID{
		GetRandomTxHash(),
		GetRandomTxHash(),
		GetRandomTxHash(),
	}

	// Set TTL entries
	err := k.SetLimitSwapTTL(ctx, blockHeight, txHashes)
	c.Assert(err, IsNil)

	// Verify they exist
	retrievedHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(retrievedHashes), Equals, 3)

	// Test removing one hash
	remainingHashes := []common.TxID{txHashes[0], txHashes[2]} // Remove middle hash
	err = k.SetLimitSwapTTL(ctx, blockHeight, remainingHashes)
	c.Assert(err, IsNil)

	updatedHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(updatedHashes), Equals, 2)
	c.Assert(updatedHashes[0].Equals(txHashes[0]), Equals, true)
	c.Assert(updatedHashes[1].Equals(txHashes[2]), Equals, true)

	// Test removing all hashes (set empty list)
	err = k.SetLimitSwapTTL(ctx, blockHeight, []common.TxID{})
	c.Assert(err, IsNil)

	emptyHashes, err := k.GetLimitSwapTTL(ctx, blockHeight)
	c.Assert(err, IsNil)
	c.Assert(len(emptyHashes), Equals, 0)
}

// TestSetAdvSwapQueueItemStateSkipsIndex verifies enqueue (SetAdvSwapQueueItem)
// creates the market index while the EndBlock re-write path
// (SetAdvSwapQueueItemState) persists the item blob WITHOUT touching the index.
func (s *KeeperAdvSwapQueueSuite) TestSetAdvSwapQueueItemStateSkipsIndex(c *C) {
	ctx, k := setupKeeperForTest(c)

	// enqueue indexes the item
	enq := MsgSwap{
		Tx:          GetRandomTx(),
		TradeTarget: cosmos.NewUint(10 * common.One),
		SwapType:    types.SwapType_limit,
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, enq), IsNil)
	ok, err := k.HasAdvSwapQueueIndex(ctx, enq)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)

	// re-writing the same item's state leaves the index in place
	c.Assert(k.SetAdvSwapQueueItemState(ctx, enq), IsNil)
	ok, err = k.HasAdvSwapQueueIndex(ctx, enq)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)

	// a state write on an item that was never enqueued has no index entry under
	// its current key. The re-write path is for mutable-state updates only, so
	// mocknet builds (all unit tests) deliberately panic on it rather than
	// silently persist a de-indexed blob; assert the guard fires.
	rw := MsgSwap{
		Tx:          GetRandomTx(),
		TradeTarget: cosmos.NewUint(10 * common.One),
		SwapType:    types.SwapType_limit,
	}
	c.Assert(func() { _ = k.SetAdvSwapQueueItemState(ctx, rw) }, PanicMatches,
		`SetAdvSwapQueueItemState: .* not indexed under its current key.*`)
	// the guard fires before the write, so no blob is persisted
	c.Check(k.HasAdvSwapQueueItem(ctx, rw.Tx.ID, int(rw.Index)), Equals, false)
}
