package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgSwapSuite struct{}

var _ = Suite(&MsgSwapSuite{})

func (MsgSwapSuite) TestMsgSwap(c *C) {
	addr := GetRandomBech32Addr()
	c.Check(addr.Empty(), Equals, false)
	ethAddress := GetRandomETHAddress()
	txID := GetRandomTxHash()
	c.Check(txID.IsEmpty(), Equals, false)

	tx := common.NewTx(
		txID,
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1)),
		},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		"SWAP:BTC.BTC",
	)

	m := NewMsgSwap(tx, common.ETHAsset, ethAddress, cosmos.NewUint(200000000), common.NoAddress, cosmos.ZeroUint(), "", "", nil, SwapType_market, 0, 0, SwapVersion_v1, addr)
	EnsureMsgBasicCorrect(m, c)

	inputs := []struct {
		requestTxHash         common.TxID
		source                common.Asset
		target                common.Asset
		amount                cosmos.Uint
		requester             common.Address
		destination           common.Address
		targetPrice           cosmos.Uint
		signer                cosmos.AccAddress
		aggregator            common.Address
		aggregatorTarget      common.Address
		aggregatorTargetLimit cosmos.Uint
	}{
		{
			requestTxHash: common.TxID(""),
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100000000),
			requester:     ethAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.Asset{},
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100000000),
			requester:     ethAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.ETHAsset,
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100000000),
			requester:     ethAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.RuneAsset(),
			target:        common.Asset{},
			amount:        cosmos.NewUint(100000000),
			requester:     ethAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.ZeroUint(),
			requester:     ethAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100000000),
			requester:     common.NoAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100000000),
			requester:     ethAddress,
			destination:   common.NoAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        addr,
		},
		{
			requestTxHash: txID,
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100000000),
			requester:     ethAddress,
			destination:   ethAddress,
			targetPrice:   cosmos.NewUint(200000000),
			signer:        cosmos.AccAddress{},
		},
	}
	for _, item := range inputs {
		tx = common.NewTx(
			item.requestTxHash,
			item.requester,
			GetRandomETHAddress(),
			common.Coins{
				common.NewCoin(item.source, item.amount),
			},
			common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
			"SWAP:BTC.BTC",
		)

		m = NewMsgSwap(tx, item.target, item.destination, item.targetPrice, common.NoAddress, cosmos.ZeroUint(), "", "", nil, SwapType_market, 0, 0, SwapVersion_v1, item.signer)
		c.Assert(m.ValidateBasic(), NotNil)
	}

	// happy path
	m = NewMsgSwap(tx, common.ETHAsset, GetRandomETHAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "123", "0x123456", nil, SwapType_market, 10, 20, SwapVersion_v1, addr)
	c.Assert(m.ValidateBasic(), IsNil)
	c.Check(m.Aggregator, Equals, "123")
	c.Check(m.AggregatorTargetAddress, Equals, "0x123456")
	c.Check(m.AggregatorTargetLimit, IsNil)

	// test address and synth swapping fails when appropriate
	m = NewMsgSwap(tx, common.ETHAsset, GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, SwapType_market, 0, 0, SwapVersion_v1, addr)
	c.Assert(m.ValidateBasic(), NotNil)
	m = NewMsgSwap(tx, common.ETHAsset.GetSyntheticAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, SwapType_market, 0, 0, SwapVersion_v1, addr)
	c.Assert(m.ValidateBasic(), IsNil)
	m = NewMsgSwap(tx, common.ETHAsset.GetSyntheticAsset(), GetRandomETHAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, SwapType_market, 0, 0, SwapVersion_v1, addr)
	c.Assert(m.ValidateBasic(), NotNil)

	// affiliate fee basis point larger than 1000 should be rejected
	m = NewMsgSwap(tx, common.ETHAsset, GetRandomETHAddress(), cosmos.ZeroUint(), GetRandomTHORAddress(), cosmos.NewUint(1024), "", "", nil, SwapType_market, 0, 0, SwapVersion_v1, addr)
	c.Assert(m.ValidateBasic(), NotNil)
}

func (MsgSwapSuite) TestSwapState_IsDone(c *C) {
	// Test market swap - not done
	marketSwap := &MsgSwap{
		SwapType: SwapType_market,
		State: &SwapState{
			Quantity: 10,
			Count:    5,
		},
	}
	c.Assert(marketSwap.IsDone(), Equals, false)

	// Test market swap - exactly done
	marketSwap.State.Count = 10
	c.Assert(marketSwap.IsDone(), Equals, true)

	// Test market swap - overdone
	marketSwap.State.Count = 11
	c.Assert(marketSwap.IsDone(), Equals, true)

	// Test market swap - zero quantity and count
	marketSwapZero := &MsgSwap{
		SwapType: SwapType_market,
		State: &SwapState{
			Quantity: 0,
			Count:    0,
		},
	}
	c.Assert(marketSwapZero.IsDone(), Equals, true)

	// Test market swap - count > 0, quantity = 0 (edge case)
	marketSwapEdge := &MsgSwap{
		SwapType: SwapType_market,
		State: &SwapState{
			Quantity: 0,
			Count:    5,
		},
	}
	c.Assert(marketSwapEdge.IsDone(), Equals, true)

	// Test limit swap - not done
	limitSwap := &MsgSwap{
		SwapType: SwapType_limit,
		State: &SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(500),
		},
	}
	c.Assert(limitSwap.IsDone(), Equals, false)

	// Test limit swap - exactly done
	limitSwap.State.In = cosmos.NewUint(1000)
	c.Assert(limitSwap.IsDone(), Equals, true)

	// Test limit swap - overdone (edge case, shouldn't happen in practice)
	limitSwapOver := &MsgSwap{
		SwapType: SwapType_limit,
		State: &SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(1100),
		},
	}
	c.Assert(limitSwapOver.IsDone(), Equals, false) // Uses Equal, not GTE

	// Test limit swap - zero deposit and in
	limitSwapZero := &MsgSwap{
		SwapType: SwapType_limit,
		State: &SwapState{
			Deposit: cosmos.ZeroUint(),
			In:      cosmos.ZeroUint(),
		},
	}
	c.Assert(limitSwapZero.IsDone(), Equals, true)

	// Test limit swap - almost done
	limitSwapAlmost := &MsgSwap{
		SwapType: SwapType_limit,
		State: &SwapState{
			Deposit: cosmos.NewUint(1000),
			In:      cosmos.NewUint(999),
		},
	}
	c.Assert(limitSwapAlmost.IsDone(), Equals, false)

	// Rounded account withdrawals may leave input after all planned successes.
	// Failed attempts must not cause early settlement.
	roundedLimitSwap := &MsgSwap{
		SwapType: SwapType_limit,
		State: &SwapState{
			Quantity:    2,
			Count:       2,
			FailedSwaps: []uint64{0},
			Deposit:     cosmos.NewUint(10),
			In:          cosmos.NewUint(3),
		},
	}
	c.Assert(roundedLimitSwap.IsDone(), Equals, false)
	roundedLimitSwap.State.Count = 3
	roundedLimitSwap.State.In = cosmos.NewUint(6)
	c.Assert(roundedLimitSwap.IsDone(), Equals, true)

	// Orders already over the successful-swap allowance also need settlement.
	roundedLimitSwap.State.Count = 4
	c.Assert(roundedLimitSwap.IsDone(), Equals, true)

	// Test unknown swap type (should default to false)
	unknownSwap := &MsgSwap{
		SwapType: SwapType(99),
		State: &SwapState{
			Quantity: 0,
			Count:    0,
			Deposit:  cosmos.ZeroUint(),
			In:       cosmos.ZeroUint(),
		},
	}
	c.Assert(unknownSwap.IsDone(), Equals, false)

	// Test uninitialized swap type (0 = market)
	uninitializedSwap := &MsgSwap{
		State: &SwapState{
			Quantity: 1,
			Count:    1,
		},
	}
	c.Assert(uninitializedSwap.IsDone(), Equals, true)
}

func (MsgSwapSuite) TestSwapState_NextSize(c *C) {
	// Test basic division with no remainder
	swap := &MsgSwap{
		TradeTarget: cosmos.NewUint(900),
		State: &SwapState{
			Quantity: 5,
			Deposit:  cosmos.NewUint(1000),
			In:       cosmos.ZeroUint(),
			Out:      cosmos.ZeroUint(),
		},
	}
	size, target := swap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(200)), Equals, true)
	// Target = (200/1000) * 900 = 180
	c.Assert(target.Equal(cosmos.NewUint(180)), Equals, true)

	// Test with remainder distribution
	swap.State.Quantity = 3
	swap.State.Deposit = cosmos.NewUint(100)
	swap.State.In = cosmos.ZeroUint()
	swap.State.Out = cosmos.ZeroUint()
	swap.State.Count = 0
	swap.TradeTarget = cosmos.NewUint(90)
	size, target = swap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(34)), Equals, true) // 100/3 = 33 + 1 for remainder
	// Target = 90 / (100 / 34) = 90 / 2.94 = 30.6 rounds to 31
	c.Assert(target.Equal(cosmos.NewUint(31)), Equals, true)

	// Update state to simulate first swap completed with success
	swap.State.In = cosmos.NewUint(34)
	swap.State.Out = cosmos.NewUint(31)
	swap.State.Count = 1
	swap.State.FailedSwaps = []uint64{} // No failures
	size, target = swap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(33)), Equals, true) // Second swap gets 33 (still has remainder)
	// Target = 59 / (66 / 33) = 59 / 2 = 29.5 rounds to 30
	c.Assert(target.Equal(cosmos.NewUint(30)), Equals, true)

	// Third swap should get the last part
	swap.State.In = cosmos.NewUint(67)
	swap.State.Out = cosmos.NewUint(61)
	swap.State.Count = 2
	size, target = swap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(33)), Equals, true) // Last swap gets 33
	// Target = 29 / (33 / 33) = 29 / 1 = 29
	c.Assert(target.Equal(cosmos.NewUint(29)), Equals, true)

	// Test sanity check - prevent exceeding deposit
	swap.State.Deposit = cosmos.NewUint(100)
	swap.State.In = cosmos.NewUint(99)
	swap.State.Out = cosmos.NewUint(89)
	swap.State.Quantity = 10
	swap.TradeTarget = cosmos.NewUint(90)
	size, target = swap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(1)), Equals, true) // Only 1 left
	// Target = 1 / (1 / 1) = 1
	c.Assert(target.Equal(cosmos.NewUint(1)), Equals, true)

	// Test with zero quantity (edge case)
	swap.State.Quantity = 0
	size, target = swap.NextSize()
	c.Assert(size.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(target.Equal(cosmos.ZeroUint()), Equals, true)

	// Test when already at deposit limit
	swap.State.Quantity = 5
	swap.State.Deposit = cosmos.NewUint(100)
	swap.State.In = cosmos.NewUint(100)
	swap.State.Out = cosmos.NewUint(90)
	swap.TradeTarget = cosmos.NewUint(90)
	size, target = swap.NextSize()
	c.Assert(size.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(target.Equal(cosmos.ZeroUint()), Equals, true)

	// Test with failed swaps affecting remainder calculation
	failedSwap := &MsgSwap{
		TradeTarget: cosmos.NewUint(100),
		State: &SwapState{
			Quantity:    4,
			Deposit:     cosmos.NewUint(103), // 103/4 = 25.75, so 3 swaps get 26, 1 gets 25
			In:          cosmos.ZeroUint(),
			Out:         cosmos.ZeroUint(),
			Count:       0,
			FailedSwaps: []uint64{},
		},
	}
	size, target = failedSwap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(26)), Equals, true) // First swap gets 26
	// Target = (26/103) * 100 = 25.24... rounds to 25
	c.Assert(target.Equal(cosmos.NewUint(25)), Equals, true)

	// Add a failed swap
	failedSwap.State.Count = 2
	failedSwap.State.FailedSwaps = []uint64{0} // First swap failed
	failedSwap.State.In = cosmos.NewUint(26)   // Only the successful swap contributed
	failedSwap.State.Out = cosmos.NewUint(25)
	size, target = failedSwap.NextSize()
	// SuccessCount = 1, remainder = 3, so this swap should get 26
	c.Assert(size.Equal(cosmos.NewUint(26)), Equals, true)
	// remainingIn = 103 - 26 = 77, remainingOut = 100 - 25 = 75
	// Target = (26/77) * 75 = 25.32... rounds to 25
	c.Assert(target.Equal(cosmos.NewUint(25)), Equals, true)

	// Test large numbers
	largeSwap := &MsgSwap{
		TradeTarget: cosmos.NewUint(1000000000000), // 1 trillion
		State: &SwapState{
			Quantity: 1000,
			Deposit:  cosmos.NewUint(1000000000000), // 1 trillion
			In:       cosmos.ZeroUint(),
			Out:      cosmos.ZeroUint(),
		},
	}
	size, target = largeSwap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(1000000000)), Equals, true) // 1 billion per swap
	c.Assert(target.Equal(cosmos.NewUint(1000000000)), Equals, true)

	// Test when TradeTarget is zero
	zeroTargetSwap := &MsgSwap{
		TradeTarget: cosmos.ZeroUint(),
		State: &SwapState{
			Quantity: 5,
			Deposit:  cosmos.NewUint(1000),
			In:       cosmos.ZeroUint(),
			Out:      cosmos.ZeroUint(),
		},
	}
	size, target = zeroTargetSwap.NextSize()
	c.Assert(size.Equal(cosmos.NewUint(200)), Equals, true)
	c.Assert(target.Equal(cosmos.ZeroUint()), Equals, true) // Zero target
}

func (MsgSwapSuite) TestSwapState_SuccessCount(c *C) {
	// Test with some failed swaps
	swap := &MsgSwap{
		State: &SwapState{
			Count:       10,
			FailedSwaps: []uint64{1, 3, 5},
		},
	}
	c.Assert(swap.SuccessCount(), Equals, uint64(7))
	c.Assert(swap.FailCount(), Equals, uint64(3))

	// Test with no failed swaps
	swap.State.FailedSwaps = []uint64{}
	c.Assert(swap.SuccessCount(), Equals, uint64(10))
	c.Assert(swap.FailCount(), Equals, uint64(0))

	// Test with all failed swaps
	swap.State.FailedSwaps = []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	c.Assert(swap.SuccessCount(), Equals, uint64(0))
	c.Assert(swap.FailCount(), Equals, uint64(10))

	// Test with nil FailedSwaps slice
	nilFailedSwap := &MsgSwap{
		State: &SwapState{
			Count:       5,
			FailedSwaps: nil,
		},
	}
	c.Assert(nilFailedSwap.SuccessCount(), Equals, uint64(5))
	c.Assert(nilFailedSwap.FailCount(), Equals, uint64(0))

	// Test with Count = 0
	zeroCountSwap := &MsgSwap{
		State: &SwapState{
			Count:       0,
			FailedSwaps: []uint64{},
		},
	}
	c.Assert(zeroCountSwap.SuccessCount(), Equals, uint64(0))
	c.Assert(zeroCountSwap.FailCount(), Equals, uint64(0))

	// Test edge case: more failed swaps than count (shouldn't happen in practice)
	edgeSwap := &MsgSwap{
		State: &SwapState{
			Count:       3,
			FailedSwaps: []uint64{0, 1, 2, 3, 4},
		},
	}
	// SuccessCount would be negative if using signed int, but uint64 wraps around
	// This is an edge case that shouldn't happen in practice
	c.Assert(edgeSwap.FailCount(), Equals, uint64(5))

	// Test with single swap
	singleSwap := &MsgSwap{
		State: &SwapState{
			Count:       1,
			FailedSwaps: []uint64{},
		},
	}
	c.Assert(singleSwap.SuccessCount(), Equals, uint64(1))
	c.Assert(singleSwap.FailCount(), Equals, uint64(0))

	// Test with single failed swap
	singleFailedSwap := &MsgSwap{
		State: &SwapState{
			Count:       1,
			FailedSwaps: []uint64{0},
		},
	}
	c.Assert(singleFailedSwap.SuccessCount(), Equals, uint64(0))
	c.Assert(singleFailedSwap.FailCount(), Equals, uint64(1))

	// Test with non-sequential failed swap indices
	nonSeqSwap := &MsgSwap{
		State: &SwapState{
			Count:       20,
			FailedSwaps: []uint64{2, 7, 11, 19},
		},
	}
	c.Assert(nonSeqSwap.SuccessCount(), Equals, uint64(16))
	c.Assert(nonSeqSwap.FailCount(), Equals, uint64(4))

	// Test with duplicate entries in FailedSwaps (if possible)
	// The length of the slice is what matters, not the values
	dupSwap := &MsgSwap{
		State: &SwapState{
			Count:       10,
			FailedSwaps: []uint64{1, 1, 2, 2}, // Duplicates
		},
	}
	c.Assert(dupSwap.SuccessCount(), Equals, uint64(6))
	c.Assert(dupSwap.FailCount(), Equals, uint64(4)) // Still counts as 4 failures
}

func (MsgSwapSuite) TestSwapVersioning(c *C) {
	// Test V1 swap
	v1Swap := &MsgSwap{
		Version: SwapVersion_v1,
	}
	c.Assert(v1Swap.IsV1(), Equals, true)
	c.Assert(v1Swap.IsV2(), Equals, false)

	// Test V2 swap
	v2Swap := &MsgSwap{
		Version: SwapVersion_v2,
	}
	c.Assert(v2Swap.IsV1(), Equals, false)
	c.Assert(v2Swap.IsV2(), Equals, true)

	// Test uninitialized version (defaults to 0 which is v1)
	uninitializedSwap := &MsgSwap{}
	c.Assert(uninitializedSwap.IsV1(), Equals, true)
	c.Assert(uninitializedSwap.IsV2(), Equals, false)

	// Test with explicit zero value
	zeroSwap := &MsgSwap{
		Version: 0,
	}
	c.Assert(zeroSwap.IsV1(), Equals, true)
	c.Assert(zeroSwap.IsV2(), Equals, false)
}

func (MsgSwapSuite) TestSwapTypes(c *C) {
	// Test market swap
	marketSwap := &MsgSwap{
		SwapType: SwapType_market,
	}
	c.Assert(marketSwap.IsMarketSwap(), Equals, true)
	c.Assert(marketSwap.IsLimitSwap(), Equals, false)

	// Test limit swap
	limitSwap := &MsgSwap{
		SwapType: SwapType_limit,
	}
	c.Assert(limitSwap.IsMarketSwap(), Equals, false)
	c.Assert(limitSwap.IsLimitSwap(), Equals, true)

	// Test uninitialized swap type (defaults to 0 which is market)
	uninitializedSwap := &MsgSwap{}
	c.Assert(uninitializedSwap.IsMarketSwap(), Equals, true)
	c.Assert(uninitializedSwap.IsLimitSwap(), Equals, false)

	// Test with explicit zero value
	zeroSwap := &MsgSwap{
		SwapType: 0,
	}
	c.Assert(zeroSwap.IsMarketSwap(), Equals, true)
	c.Assert(zeroSwap.IsLimitSwap(), Equals, false)
}

func (MsgSwapSuite) TestIsStreaming(c *C) {
	// Test non-streaming swap with quantity = 1
	singleSwap := &MsgSwap{
		State: &SwapState{
			Quantity: 1,
		},
	}
	c.Assert(singleSwap.IsStreaming(), Equals, false)

	// Test streaming swap with quantity > 1
	streamingSwap := &MsgSwap{
		State: &SwapState{
			Quantity: 10,
		},
	}
	c.Assert(streamingSwap.IsStreaming(), Equals, true)

	// Test with quantity = 0 (edge case)
	zeroQuantitySwap := &MsgSwap{
		State: &SwapState{
			Quantity: 0,
		},
	}
	c.Assert(zeroQuantitySwap.IsStreaming(), Equals, false)

	// Test with quantity = 2 (minimal streaming)
	minimalStreamingSwap := &MsgSwap{
		State: &SwapState{
			Quantity: 2,
		},
	}
	c.Assert(minimalStreamingSwap.IsStreaming(), Equals, true)

	// Test with nil State (edge case)
	// This would panic in the actual implementation, but we can't test panics with check.v1
	// So we skip this test case
}

func (MsgSwapSuite) TestNewMsgSwap(c *C) {
	// Test with all parameters
	addr := GetRandomBech32Addr()
	txID := GetRandomTxHash()
	ethAddr := GetRandomETHAddress()
	thorAddr := GetRandomTHORAddress()
	aggLimit := cosmos.NewUint(5000)

	tx := common.NewTx(
		txID,
		ethAddr,
		ethAddr,
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(100000000)),
		},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(2000))},
		"SWAP:ETH.ETH",
	)

	// Test complete initialization
	msg := NewMsgSwap(
		tx,
		common.ETHAsset,
		ethAddr,
		cosmos.NewUint(200000000),
		thorAddr,
		cosmos.NewUint(100),
		"aggregator123",
		"0xaggTarget",
		&aggLimit,
		SwapType_limit,
		10,
		5,
		SwapVersion_v2,
		addr,
	)

	// Verify all fields are set correctly
	c.Assert(msg.Tx.ID.Equals(txID), Equals, true)
	c.Assert(msg.TargetAsset.Equals(common.ETHAsset), Equals, true)
	c.Assert(msg.Destination.Equals(ethAddr), Equals, true)
	c.Assert(msg.TradeTarget.Equal(cosmos.NewUint(200000000)), Equals, true)
	c.Assert(msg.AffiliateAddress.Equals(thorAddr), Equals, true)
	c.Assert(msg.AffiliateBasisPoints.Equal(cosmos.NewUint(100)), Equals, true)
	c.Assert(msg.Signer.Equals(addr), Equals, true)
	c.Assert(msg.Aggregator, Equals, "aggregator123")
	c.Assert(msg.AggregatorTargetAddress, Equals, "0xaggTarget")
	c.Assert(msg.AggregatorTargetLimit.Equal(cosmos.NewUint(5000)), Equals, true)
	c.Assert(msg.SwapType, Equals, SwapType_limit)
	c.Assert(msg.StreamQuantity, Equals, uint64(10))
	c.Assert(msg.StreamInterval, Equals, uint64(5))

	// Verify State initialization
	c.Assert(msg.State, NotNil)
	c.Assert(msg.State.Quantity, Equals, uint64(10))
	c.Assert(msg.State.Interval, Equals, uint64(5))
	c.Assert(msg.State.Deposit.Equal(cosmos.NewUint(100000000)), Equals, true)
	c.Assert(msg.State.Withdrawn.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(msg.State.In.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(msg.State.Out.Equal(cosmos.ZeroUint()), Equals, true)

	// Test with minimal parameters (zero values)
	msgMinimal := NewMsgSwap(
		tx,
		common.RuneAsset(),
		GetRandomTHORAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		SwapType_market,
		0,
		0,
		SwapVersion_v1,
		addr,
	)

	// Verify minimal initialization
	c.Assert(msgMinimal.AffiliateAddress.Equals(common.NoAddress), Equals, true)
	c.Assert(msgMinimal.AffiliateBasisPoints.Equal(cosmos.ZeroUint()), Equals, true)
	c.Assert(msgMinimal.Aggregator, Equals, "")
	c.Assert(msgMinimal.AggregatorTargetAddress, Equals, "")
	c.Assert(msgMinimal.AggregatorTargetLimit, IsNil)
	c.Assert(msgMinimal.SwapType, Equals, SwapType_market)
	c.Assert(msgMinimal.StreamQuantity, Equals, uint64(0))
	c.Assert(msgMinimal.StreamInterval, Equals, uint64(0))

	// Test with multiple coins (should still use first coin for deposit)
	txMultiCoin := common.NewTx(
		txID,
		ethAddr,
		ethAddr,
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(50000000)),
			common.NewCoin(common.ETHAsset, cosmos.NewUint(75000000)),
		},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(2000))},
		"SWAP:ETH.ETH",
	)

	msgMulti := NewMsgSwap(
		txMultiCoin,
		common.ETHAsset,
		ethAddr,
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		SwapType_market,
		1,
		0,
		SwapVersion_v1,
		addr,
	)

	// Should use first coin amount for deposit
	c.Assert(msgMulti.State.Deposit.Equal(cosmos.NewUint(50000000)), Equals, true)
}

func (MsgSwapSuite) TestIsLegacyStreaming(c *C) {
	// Test V1 with StreamInterval = 0
	v1NoStream := &MsgSwap{
		Version:        SwapVersion_v1,
		StreamInterval: 0,
	}
	c.Assert(v1NoStream.IsLegacyStreaming(), Equals, false)

	// Test V1 with StreamInterval > 0
	v1Stream := &MsgSwap{
		Version:        SwapVersion_v1,
		StreamInterval: 10,
	}
	c.Assert(v1Stream.IsLegacyStreaming(), Equals, true)

	// Test V2 with StreamInterval > 0 (should return false as it only works for V1)
	v2Stream := &MsgSwap{
		Version:        SwapVersion_v2,
		StreamInterval: 10,
	}
	c.Assert(v2Stream.IsLegacyStreaming(), Equals, false)

	// Test V2 with StreamInterval = 0
	v2NoStream := &MsgSwap{
		Version:        SwapVersion_v2,
		StreamInterval: 0,
	}
	c.Assert(v2NoStream.IsLegacyStreaming(), Equals, false)

	// Test uninitialized version (defaults to 0 which is v1)
	uninitialized := &MsgSwap{
		StreamInterval: 10,
	}
	c.Assert(uninitialized.IsLegacyStreaming(), Equals, true)
}
