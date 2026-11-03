package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ManagerSwapQueueSuite struct{}

var _ = Suite(&ManagerSwapQueueSuite{})

// TestGenTradePair tests the genTradePair helper function
func (s *ManagerSwapQueueSuite) TestGenTradePair(c *C) {
	// Test creating a trade pair
	source := common.BTCAsset
	target := common.ETHAsset

	pair := genTradePair(source, target)

	c.Assert(pair.source.Equals(source), Equals, true)
	c.Assert(pair.target.Equals(target), Equals, true)
}

func (s *ManagerSwapQueueSuite) TestSwapItemsSortEqualFeeTieBreakers(c *C) {
	txA := common.TxID("AA00000000000000000000000000000000000000000000000000000000000000")
	txB := common.TxID("BB00000000000000000000000000000000000000000000000000000000000000")
	txC := common.TxID("CC00000000000000000000000000000000000000000000000000000000000000")
	fee := cosmos.NewUint(100)

	items := swapItems{
		{index: 2, msg: MsgSwap{Tx: common.Tx{ID: txB}}, fee: fee},
		{index: 3, msg: MsgSwap{Tx: common.Tx{ID: txA}}, fee: fee},
		{index: 1, msg: MsgSwap{Tx: common.Tx{ID: txA}}, fee: fee},
		{index: 0, msg: MsgSwap{Tx: common.Tx{ID: txC}}, fee: fee},
		{index: 0, msg: MsgSwap{Tx: common.Tx{ID: txA}}, fee: fee},
	}

	sorted := items.Sort()

	expected := []struct {
		txID  common.TxID
		index int
	}{
		{txID: txA, index: 0},
		{txID: txA, index: 1},
		{txID: txA, index: 3},
		{txID: txB, index: 2},
		{txID: txC, index: 0},
	}
	c.Assert(sorted, HasLen, len(expected))
	for i, exp := range expected {
		c.Check(sorted[i].msg.Tx.ID.Equals(exp.txID), Equals, true, Commentf("index %d", i))
		c.Check(sorted[i].index, Equals, exp.index, Commentf("index %d", i))
	}
	c.Check(items[0].msg.Tx.ID.Equals(txB), Equals, true, Commentf("Sort should return a copy"))
}

// TestTradePairString tests the String method of tradePair
func (s *ManagerSwapQueueSuite) TestTradePairString(c *C) {
	// Test various trade pair string representations
	testCases := []struct {
		source   common.Asset
		target   common.Asset
		expected string
	}{
		{common.BTCAsset, common.ETHAsset, "BTC.BTC>ETH.ETH"},
		{common.RuneAsset(), common.BTCAsset, "THOR.RUNE>BTC.BTC"},
		{common.ETHAsset, common.RuneAsset(), "ETH.ETH>THOR.RUNE"},
		{common.BTCAsset.GetSyntheticAsset(), common.ETHAsset, "BTC/BTC>ETH.ETH"},
	}

	for _, tc := range testCases {
		pair := genTradePair(tc.source, tc.target)
		c.Assert(pair.String(), Equals, tc.expected)
	}
}

// TestTradePairHasRune tests the HasRune method of tradePair
func (s *ManagerSwapQueueSuite) TestTradePairHasRune(c *C) {
	// Test pairs with RUNE
	runeToEth := genTradePair(common.RuneAsset(), common.ETHAsset)
	c.Assert(runeToEth.HasRune(), Equals, true)

	ethToRune := genTradePair(common.ETHAsset, common.RuneAsset())
	c.Assert(ethToRune.HasRune(), Equals, true)

	// Test pairs without RUNE
	btcToEth := genTradePair(common.BTCAsset, common.ETHAsset)
	c.Assert(btcToEth.HasRune(), Equals, false)

	// Test with synthetic assets (should not be considered RUNE)
	synthBtcToEth := genTradePair(common.BTCAsset.GetSyntheticAsset(), common.ETHAsset)
	c.Assert(synthBtcToEth.HasRune(), Equals, false)
}

// TestTradePairEquals tests the Equals method of tradePair
func (s *ManagerSwapQueueSuite) TestTradePairEquals(c *C) {
	// Test equal pairs
	pair1 := genTradePair(common.BTCAsset, common.ETHAsset)
	pair2 := genTradePair(common.BTCAsset, common.ETHAsset)
	c.Assert(pair1.Equals(pair2), Equals, true)

	// Test unequal pairs - different source
	pair3 := genTradePair(common.RuneAsset(), common.ETHAsset)
	c.Assert(pair1.Equals(pair3), Equals, false)

	// Test unequal pairs - different target
	pair4 := genTradePair(common.BTCAsset, common.RuneAsset())
	c.Assert(pair1.Equals(pair4), Equals, false)

	// Test unequal pairs - both different
	pair5 := genTradePair(common.RuneAsset(), common.RuneAsset())
	c.Assert(pair1.Equals(pair5), Equals, false)

	// Test with synthetic assets
	pair6 := genTradePair(common.BTCAsset.GetSyntheticAsset(), common.ETHAsset)
	pair7 := genTradePair(common.BTCAsset.GetSyntheticAsset(), common.ETHAsset)
	c.Assert(pair6.Equals(pair7), Equals, true)
	c.Assert(pair1.Equals(pair6), Equals, false) // BTC != synth BTC
}

// TestAppendNewPair tests appending a new pair to an empty list
func (s *ManagerSwapQueueSuite) TestAppendNewPair(c *C) {
	// Start with empty tradePairs
	pairs := tradePairs{}
	c.Assert(len(pairs), Equals, 0)

	// Append a new pair
	newPair := genTradePair(common.BTCAsset, common.ETHAsset)
	pairs = pairs.Append(newPair)

	// Verify the pair was added
	c.Assert(len(pairs), Equals, 1)
	c.Assert(pairs[0].Equals(newPair), Equals, true)
}

// TestAppendDuplicatePair tests that duplicate pairs are not added
func (s *ManagerSwapQueueSuite) TestAppendDuplicatePair(c *C) {
	// Create tradePairs with one pair
	pair := genTradePair(common.BTCAsset, common.ETHAsset)
	pairs := tradePairs{pair}
	c.Assert(len(pairs), Equals, 1)

	// Try to append the same pair
	pairs = pairs.Append(pair)

	// Verify list length remains 1 (no duplicate added)
	c.Assert(len(pairs), Equals, 1)
	c.Assert(pairs[0].Equals(pair), Equals, true)
}

// TestAppendMultiplePairs tests appending multiple different pairs
func (s *ManagerSwapQueueSuite) TestAppendMultiplePairs(c *C) {
	// Start with empty tradePairs
	pairs := tradePairs{}

	// Define test pairs
	pair1 := genTradePair(common.BTCAsset, common.ETHAsset)
	pair2 := genTradePair(common.ETHAsset, common.RuneAsset())
	pair3 := genTradePair(common.RuneAsset(), common.BTCAsset)
	pair4 := genTradePair(common.BTCAsset.GetSyntheticAsset(), common.ETHAsset)

	// Append all pairs
	pairs = pairs.Append(pair1)
	pairs = pairs.Append(pair2)
	pairs = pairs.Append(pair3)
	pairs = pairs.Append(pair4)

	// Verify all pairs were added
	c.Assert(len(pairs), Equals, 4)
	c.Assert(pairs[0].Equals(pair1), Equals, true)
	c.Assert(pairs[1].Equals(pair2), Equals, true)
	c.Assert(pairs[2].Equals(pair3), Equals, true)
	c.Assert(pairs[3].Equals(pair4), Equals, true)
}

// TestAppendWithExistingPairs tests appending to a non-empty list with duplicates
func (s *ManagerSwapQueueSuite) TestAppendWithExistingPairs(c *C) {
	// Start with some existing pairs
	pair1 := genTradePair(common.BTCAsset, common.ETHAsset)
	pair2 := genTradePair(common.ETHAsset, common.RuneAsset())
	pairs := tradePairs{pair1, pair2}
	c.Assert(len(pairs), Equals, 2)

	// Try to append duplicate of pair1
	pairs = pairs.Append(pair1)
	c.Assert(len(pairs), Equals, 2) // Should remain 2

	// Append a new pair
	pair3 := genTradePair(common.RuneAsset(), common.BTCAsset)
	pairs = pairs.Append(pair3)
	c.Assert(len(pairs), Equals, 3)

	// Try to append duplicate of pair2
	pairs = pairs.Append(pair2)
	c.Assert(len(pairs), Equals, 3) // Should remain 3

	// Verify the final state
	c.Assert(pairs[0].Equals(pair1), Equals, true)
	c.Assert(pairs[1].Equals(pair2), Equals, true)
	c.Assert(pairs[2].Equals(pair3), Equals, true)
}

// TestAppendEdgeCases tests various edge cases
func (s *ManagerSwapQueueSuite) TestAppendEdgeCases(c *C) {
	pairs := tradePairs{}

	// Test with RUNE pairs
	runeToBtc := genTradePair(common.RuneAsset(), common.BTCAsset)
	btcToRune := genTradePair(common.BTCAsset, common.RuneAsset())
	pairs = pairs.Append(runeToBtc)
	pairs = pairs.Append(btcToRune)
	c.Assert(len(pairs), Equals, 2)

	// Test with synthetic assets
	synthBtcToEth := genTradePair(common.BTCAsset.GetSyntheticAsset(), common.ETHAsset)
	ethToSynthBtc := genTradePair(common.ETHAsset, common.BTCAsset.GetSyntheticAsset())
	pairs = pairs.Append(synthBtcToEth)
	pairs = pairs.Append(ethToSynthBtc)
	c.Assert(len(pairs), Equals, 4)

	// Test pairs with same source but different targets
	btcToEth := genTradePair(common.BTCAsset, common.ETHAsset)
	btcToAtom := genTradePair(common.BTCAsset, common.ATOMAsset)
	pairs = pairs.Append(btcToEth)
	pairs = pairs.Append(btcToAtom)
	c.Assert(len(pairs), Equals, 6)

	// Test pairs with same target but different sources
	ethToBtc := genTradePair(common.ETHAsset, common.BTCAsset)
	atomToBtc := genTradePair(common.ATOMAsset, common.BTCAsset)
	pairs = pairs.Append(ethToBtc)
	pairs = pairs.Append(atomToBtc)
	c.Assert(len(pairs), Equals, 8)
}

// TestAppendPreservesOrder tests that append maintains order
func (s *ManagerSwapQueueSuite) TestAppendPreservesOrder(c *C) {
	pairs := tradePairs{}

	// Append pairs in specific order
	pair1 := genTradePair(common.BTCAsset, common.ETHAsset)
	pair2 := genTradePair(common.ETHAsset, common.ATOMAsset)
	pair3 := genTradePair(common.ATOMAsset, common.RuneAsset())
	pair4 := genTradePair(common.RuneAsset(), common.BTCAsset)

	pairs = pairs.Append(pair1)
	pairs = pairs.Append(pair2)
	pairs = pairs.Append(pair3)
	pairs = pairs.Append(pair4)

	// Verify order is maintained
	c.Assert(len(pairs), Equals, 4)
	c.Assert(pairs[0].Equals(pair1), Equals, true)
	c.Assert(pairs[1].Equals(pair2), Equals, true)
	c.Assert(pairs[2].Equals(pair3), Equals, true)
	c.Assert(pairs[3].Equals(pair4), Equals, true)

	// Try to append duplicates in different order
	pairs = pairs.Append(pair3)
	pairs = pairs.Append(pair1)

	// Order should remain the same, no new items added
	c.Assert(len(pairs), Equals, 4)
	c.Assert(pairs[0].Equals(pair1), Equals, true)
	c.Assert(pairs[1].Equals(pair2), Equals, true)
	c.Assert(pairs[2].Equals(pair3), Equals, true)
	c.Assert(pairs[3].Equals(pair4), Equals, true)
}

// TestAppendWithEmptyAssets tests behavior with empty assets
func (s *ManagerSwapQueueSuite) TestAppendWithEmptyAssets(c *C) {
	pairs := tradePairs{}

	// Create pairs with empty assets
	emptyAsset := common.Asset{}

	// Empty source, valid target
	pair1 := genTradePair(emptyAsset, common.BTCAsset)
	pairs = pairs.Append(pair1)
	c.Assert(len(pairs), Equals, 1)

	// Valid source, empty target
	pair2 := genTradePair(common.ETHAsset, emptyAsset)
	pairs = pairs.Append(pair2)
	c.Assert(len(pairs), Equals, 2)

	// Both empty
	pair3 := genTradePair(emptyAsset, emptyAsset)
	pairs = pairs.Append(pair3)
	c.Assert(len(pairs), Equals, 3)

	// Try to add duplicate empty pair
	pairs = pairs.Append(pair3)
	c.Assert(len(pairs), Equals, 3) // Should remain 3

	// Verify the pairs
	c.Assert(pairs[0].source.IsEmpty(), Equals, true)
	c.Assert(pairs[0].target.Equals(common.BTCAsset), Equals, true)
	c.Assert(pairs[1].source.Equals(common.ETHAsset), Equals, true)
	c.Assert(pairs[1].target.IsEmpty(), Equals, true)
	c.Assert(pairs[2].source.IsEmpty(), Equals, true)
	c.Assert(pairs[2].target.IsEmpty(), Equals, true)
}

// TestAppendConcurrentScenario simulates a scenario where multiple pairs might be added
func (s *ManagerSwapQueueSuite) TestAppendConcurrentScenario(c *C) {
	// This tests that Append returns a new slice rather than modifying in place
	originalPairs := tradePairs{
		genTradePair(common.BTCAsset, common.ETHAsset),
		genTradePair(common.ETHAsset, common.RuneAsset()),
	}

	// Make a copy of the original length
	originalLen := len(originalPairs)

	// Append to create a new slice
	newPairs := originalPairs.Append(genTradePair(common.RuneAsset(), common.BTCAsset))

	// Verify original is unchanged
	c.Assert(len(originalPairs), Equals, originalLen)
	c.Assert(len(newPairs), Equals, originalLen+1)

	// Verify the new pair is only in newPairs
	c.Assert(newPairs[2].source.Equals(common.RuneAsset()), Equals, true)
	c.Assert(newPairs[2].target.Equals(common.BTCAsset), Equals, true)
}

// TestSwapItemGetHash tests the GetHash method of swapItem
func (s *ManagerSwapQueueSuite) TestSwapItemGetHash(c *C) {
	// Create test transaction with known hash
	tx := GetRandomTx()
	expectedHash := tx.ID

	// Create a swap message
	msg := NewMsgSwap(
		tx,
		common.ETHAsset,
		GetRandomETHAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		0, 0,
		types.SwapVersion_v1,
		GetRandomBech32Addr(),
	)

	// Create swapItem
	item := swapItem{
		msg:   *msg,
		index: 0,
		fee:   cosmos.ZeroUint(),
	}

	// Test GetHash returns the transaction ID
	actualHash := item.GetHash()
	c.Assert(actualHash.Equals(expectedHash), Equals, true)
	c.Assert(actualHash.String(), Equals, expectedHash.String())
}

// findMatchingTradesRef is the pre-optimization implementation. It is kept as
// a behavior oracle because the todo slice order affects limit-swap discovery.
func (p tradePairs) findMatchingTradesRef(trade tradePair, pairs tradePairs) tradePairs {
	var comp func(pair tradePair) bool
	switch {
	case trade.source.IsRune():
		comp = func(pair tradePair) bool { return pair.source.Equals(trade.target) }
	case trade.target.IsRune():
		comp = func(pair tradePair) bool { return pair.target.Equals(trade.source) }
	default:
		comp = func(pair tradePair) bool {
			return pair.source.Equals(trade.target) || pair.target.Equals(trade.source)
		}
	}
	for _, pair := range pairs {
		if comp(pair) {
			p = p.Append(pair)
		}
	}
	return p
}

func (s *ManagerSwapQueueSuite) TestFindMatchingTradesMatchesReference(c *C) {
	assets := benchAssets(8)
	pairs := benchPairs(assets)
	index := newTradePairIndex(pairs)
	todo := make(tradePairs, 0)
	seen := make(tradePairSet)
	indexed := make(tradePairs, 0)
	indexedSeen := make(tradePairSet)
	ref := make(tradePairs, 0)

	for i := 0; i < 40; i++ {
		source := assets[i%len(assets)]
		target := assets[(i*3+1)%len(assets)]
		if source.Equals(target) {
			target = assets[(i*3+2)%len(assets)]
		}
		trade := genTradePair(source, target)
		todo = todo.findMatchingTrades(trade, pairs, seen)
		indexed = indexed.findMatchingTradesIndexed(trade, index, indexedSeen)
		ref = ref.findMatchingTradesRef(trade, pairs)

		c.Assert(todo, DeepEquals, ref, Commentf("swap %d", i))
		c.Assert(indexed, DeepEquals, ref, Commentf("indexed swap %d", i))
		c.Assert(len(seen), Equals, len(todo), Commentf("swap %d: seen out of sync", i))
		c.Assert(len(indexedSeen), Equals, len(indexed), Commentf("indexed swap %d: seen out of sync", i))
	}

	c.Assert(len(todo) < 40*len(pairs), Equals, true)
	c.Assert(len(todo) > 0, Equals, true)

	// Exact-key lookup intentionally falls back to Asset.Equals for any
	// non-canonical historical value.
	lowerETH := common.ETHAsset
	lowerETH.Chain = common.Chain(strings.ToLower(lowerETH.Chain.String()))
	lowerETH.Symbol = common.Symbol(strings.ToLower(lowerETH.Symbol.String()))
	lowerETH.Ticker = common.Ticker(strings.ToLower(lowerETH.Ticker.String()))
	trade := genTradePair(common.RuneAsset(), lowerETH)
	indexed = nil
	ref = nil
	indexed = indexed.findMatchingTradesIndexed(trade, index, make(tradePairSet))
	ref = ref.findMatchingTradesRef(trade, pairs)
	c.Assert(indexed, DeepEquals, ref)
}
