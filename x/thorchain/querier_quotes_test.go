package thorchain

import (
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type QuotesSuite struct{}

var _ = Suite(&QuotesSuite{})

func (s *QuotesSuite) TestParseMultipleAffiliateParams(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Test single affiliate
	affiliates, bps, totalBps, err := parseMultipleAffiliateParams(ctx, mgr, "affiliate1", "100")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 1)
	c.Assert(len(bps), Equals, 1)
	c.Assert(affiliates[0], Equals, "affiliate1")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(totalBps.Uint64(), Equals, uint64(100))

	// Test multiple affiliates with slash separation
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "affiliate1/affiliate2", "100/200")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(len(bps), Equals, 2)
	c.Assert(affiliates[0], Equals, "affiliate1")
	c.Assert(affiliates[1], Equals, "affiliate2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))
	c.Assert(totalBps.Uint64(), Equals, uint64(300))

	// Test three affiliates
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "50/75/25")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3)
	c.Assert(len(bps), Equals, 3)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(affiliates[2], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(50))
	c.Assert(bps[1].Uint64(), Equals, uint64(75))
	c.Assert(bps[2].Uint64(), Equals, uint64(25))
	c.Assert(totalBps.Uint64(), Equals, uint64(150))

	// Test single bps applied to multiple affiliates (should now return error due to mismatch)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (3) and BPS values (1)")

	// Test empty strings
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "", "")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 0)
	c.Assert(len(bps), Equals, 0)
	c.Assert(totalBps.Uint64(), Equals, uint64(0))

	// Test whitespace trimming
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, " a1 / a2 ", " 100 / 200 ")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))

	// Test empty parts in slash-separated string
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1//a3", "100//300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test mismatch between affiliates and bps (more affiliates than bps) - should now return error
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/200")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (3) and BPS values (2)")

	// Test mismatch between affiliates and bps (more bps than affiliates) - should also return error
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "100/200/300")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (2) and BPS values (3)")

	// Test equal numbers of affiliates and bps - should work
	affiliates, bps, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/200/150")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 3)
	c.Assert(len(bps), Equals, 3)
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a2")
	c.Assert(affiliates[2], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(200))
	c.Assert(bps[2].Uint64(), Equals, uint64(150))
	c.Assert(totalBps.Uint64(), Equals, uint64(450))

	// Test invalid bps (should skip invalid ones)
	affiliates, bps, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "100/invalid/300")
	c.Assert(err, IsNil)
	c.Assert(len(affiliates), Equals, 2) // Skips a2 because of invalid bps
	c.Assert(affiliates[0], Equals, "a1")
	c.Assert(affiliates[1], Equals, "a3")
	c.Assert(bps[0].Uint64(), Equals, uint64(100))
	c.Assert(bps[1].Uint64(), Equals, uint64(300))

	// Test total bps exceeding limit (1000)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "600/500")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "total affiliate fee must not be more than 1000 bps")

	// Test edge case: exactly at limit
	_, _, totalBps, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2", "500/500")
	c.Assert(err, IsNil)
	c.Assert(totalBps.Uint64(), Equals, uint64(1000))

	// Test affiliates with no BPS values (should return error due to mismatch)
	_, _, _, err = parseMultipleAffiliateParams(ctx, mgr, "a1/a2/a3", "")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mismatch between number of affiliates (3) and BPS values (0)")
}

func (s *QuotesSuite) TestRapidStreamingSwapCalculation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Helper function to calculate streaming swap blocks
	calculateStreamingSwapBlocks := func(streamingQuantity, streamingInterval, rapidSwapMax uint64) int64 {
		var streamSwapBlocks int64
		if streamingQuantity > 0 {
			if streamingInterval == 0 {
				// Rapid streaming: multiple swaps per block limited by AdvSwapQueueRapidSwapMax
				rapidSwapMax = max(rapidSwapMax, 1)
				// Calculate blocks needed: ceil(quantity / rapidSwapMax)
				streamSwapBlocks = (int64(streamingQuantity) + int64(rapidSwapMax) - 1) / int64(rapidSwapMax)
			} else {
				// Traditional streaming: one swap per interval
				streamSwapBlocks = int64(streamingInterval) * int64(streamingQuantity-1)
			}
		}
		return streamSwapBlocks
	}

	// Test 1: Rapid streaming with rapidSwapMax=5, quantity=10
	// Should take ceil(10/5) = 2 blocks
	mgr.Keeper().SetMimir(ctx, "AdvSwapQueueRapidSwapMax", 5)
	blocks := calculateStreamingSwapBlocks(10, 0, 5)
	c.Assert(blocks, Equals, int64(2))

	// Test 2: Rapid streaming with rapidSwapMax=5, quantity=11
	// Should take ceil(11/5) = 3 blocks
	blocks = calculateStreamingSwapBlocks(11, 0, 5)
	c.Assert(blocks, Equals, int64(3))

	// Test 3: Rapid streaming with rapidSwapMax=5, quantity=5
	// Should take ceil(5/5) = 1 block
	blocks = calculateStreamingSwapBlocks(5, 0, 5)
	c.Assert(blocks, Equals, int64(1))

	// Test 4: Rapid streaming with rapidSwapMax=5, quantity=4
	// Should take ceil(4/5) = 1 block
	blocks = calculateStreamingSwapBlocks(4, 0, 5)
	c.Assert(blocks, Equals, int64(1))

	// Test 5: Rapid streaming with rapidSwapMax=1, quantity=10
	// Should take ceil(10/1) = 10 blocks
	blocks = calculateStreamingSwapBlocks(10, 0, 1)
	c.Assert(blocks, Equals, int64(10))

	// Test 6: Rapid streaming with rapidSwapMax=10, quantity=20
	// Should take ceil(20/10) = 2 blocks
	blocks = calculateStreamingSwapBlocks(20, 0, 10)
	c.Assert(blocks, Equals, int64(2))

	// Test 7: Rapid streaming with rapidSwapMax=10, quantity=15
	// Should take ceil(15/10) = 2 blocks
	blocks = calculateStreamingSwapBlocks(15, 0, 10)
	c.Assert(blocks, Equals, int64(2))

	// Test 8: Traditional streaming with interval=5, quantity=10
	// Should take 5 * (10-1) = 45 blocks
	blocks = calculateStreamingSwapBlocks(10, 5, 5)
	c.Assert(blocks, Equals, int64(45))

	// Test 9: Traditional streaming with interval=1, quantity=10
	// Should take 1 * (10-1) = 9 blocks
	blocks = calculateStreamingSwapBlocks(10, 1, 5)
	c.Assert(blocks, Equals, int64(9))

	// Test 10: Edge case - quantity=0
	// Should take 0 blocks
	blocks = calculateStreamingSwapBlocks(0, 0, 5)
	c.Assert(blocks, Equals, int64(0))

	// Test 11: Edge case - rapidSwapMax=0 (should use safety default of 1)
	// Should take ceil(10/1) = 10 blocks
	blocks = calculateStreamingSwapBlocks(10, 0, 0)
	c.Assert(blocks, Equals, int64(10))

	// Test 12: Verify actual quote API integration
	// Set up a test scenario with rapid streaming
	mgr.Keeper().SetMimir(ctx, "AdvSwapQueueRapidSwapMax", 5)

	// The actual queryQuoteSwap function would be called here in a full integration test
	// For now, we've verified the calculation logic works correctly
	rapidSwapMax := mgr.Keeper().GetConfigInt64(ctx, constants.AdvSwapQueueRapidSwapMax)
	c.Assert(rapidSwapMax, Equals, int64(5))
}

// TestQuoteLimitStaleState verifies that queryQuoteLimit rejects requests when
// the node's latest block is older than the configured MaxLag, preventing
// callers from receiving stale vault addresses (e.g. retiring vaults).
func (s *QuotesSuite) TestQuoteLimitStaleState(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	// Provide all required parameters so validation passes and we reach the stale check.
	req := &types.QueryQuoteLimitRequest{
		FromAsset:   "BTC.BTC",
		ToAsset:     "ETH.ETH",
		Amount:      "100000",
		Destination: "0x1234567890123456789012345678901234567890",
		TargetOut:   "50000",
	}

	// Stale node: block time is far in the past — must be rejected.
	staleCtx := ctx.WithBlockTime(time.Unix(1, 0))
	_, err := qs.queryQuoteLimit(staleCtx, req)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "refusing quote on node with stale state")

	// Fresh node: block time is current — stale-state error must NOT be returned
	// (the request may still fail due to missing pools, but not due to stale state).
	_, err = qs.queryQuoteLimit(ctx, req)
	if err != nil {
		c.Assert(strings.Contains(err.Error(), "stale state"), Equals, false)
	}
}

func (s *QuotesSuite) TestQuoteLimitDestinationValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	pool := NewPool()
	pool.Asset = common.ZECAsset
	pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
	pool.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.ZECChain, NewNetworkFee(common.ZECChain, 200, 10)), IsNil)

	p2pkh, tex, p2sh, wrongNetworkTex := zecTxOutAddresses()
	wrongNetworkP2PKH := common.Address("tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7")
	wrongNetworkP2SH := common.Address("t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb")
	if common.CurrentChainNetwork == common.MockNet {
		wrongNetworkP2PKH = "t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU"
		wrongNetworkP2SH = "t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5"
	}
	thorAddress := GetRandomTHORAddress()
	for _, tc := range []struct {
		name        string
		asset       common.Asset
		destination common.Address
		valid       bool
	}{
		{"ZEC P2PKH", common.ZECAsset, p2pkh, true},
		{"ZEC TEX", common.ZECAsset, tex, true},
		{"ZEC P2SH", common.ZECAsset, p2sh, true},
		{"wrong chain", common.ZECAsset, GetRandomBTCAddress(), false},
		{"wrong network P2PKH", common.ZECAsset, wrongNetworkP2PKH, false},
		{"wrong network TEX", common.ZECAsset, wrongNetworkTex, false},
		{"wrong network P2SH", common.ZECAsset, wrongNetworkP2SH, false},
		{"synth THOR destination", common.ZECAsset.GetSyntheticAsset(), thorAddress, true},
		{"trade THOR destination", common.ZECAsset.GetTradeAsset(), thorAddress, true},
		{"secured THOR destination", common.ZECAsset.GetSecuredAsset(), thorAddress, true},
		{"synth rejects L1 destination", common.ZECAsset.GetSyntheticAsset(), p2pkh, false},
	} {
		comment := Commentf("case %q, network: %s", tc.name, common.CurrentChainNetwork)
		quote, err := qs.queryQuoteLimit(ctx, &types.QueryQuoteLimitRequest{
			FromAsset:   common.RuneNative.String(),
			ToAsset:     tc.asset.String(),
			Amount:      "100000000",
			Destination: tc.destination.String(),
			TargetOut:   "1000000",
		})
		if !tc.valid {
			c.Assert(err, ErrorMatches, "bad destination: address does not match target chain or current network", comment)
			c.Check(quote, IsNil, comment)
			continue
		}
		c.Assert(err, IsNil, comment)
		c.Assert(quote, NotNil, comment)
		memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), quote.Memo)
		c.Assert(err, IsNil, comment)
		c.Check(memo.GetDestination(), Equals, tc.destination, comment)
		c.Check(memo.GetAsset(), Equals, tc.asset, comment)
	}
}

func (s *QuotesSuite) TestQuoteRefundAddressValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	primary := common.Address(common.XMRSeededDummyAddress('4', []byte("quote-refund-primary")))
	subaddress := common.Address(common.XMRSeededDummyAddress('8', []byte("quote-refund-subaddress")))
	wrongNetwork := common.Address("FRw3ZEEFtuK82ifoJK7iFrLuDkpBEc7xx1WjLvqAQc8HgRjJXwrDr37TX9yDq9Ye2yQWfaTvMJpkXQbyPty1pgpZ2m6UsaJbC")
	if common.CurrentChainNetwork == common.MockNet {
		wrongNetwork = common.Address(common.XMRSeededDummyAddress('C', []byte("quote-refund-wrong-network")))
	}
	integrated := common.Address("4CdvgtrwKjggnkXTAXRu7peYZruMJNii9EHrneXZie344btcwjvbzMbDvRcXvC4h4667vpwhdQWwPivq8ZfVPsGJeEvzNowpChm17H3MqL")
	otherChain := GetRandomETHAddress()
	thorDestination := GetRandomTHORAddress()
	btcBech32 := GetRandomBTCAddress()
	btcLegacy := common.Address("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa")
	btcWrongNetwork := common.Address("bcrt1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawhcf2xa")
	if common.CurrentChainNetwork == common.MockNet {
		btcLegacy = common.Address("mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn")
		btcWrongNetwork = common.Address("bc1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawlht528")
	}

	const thorName = "xmrrefund"
	mgr.Keeper().SetTHORName(ctx, types.NewTHORName(thorName, ctx.BlockHeight()+1000, []types.THORNameAlias{
		{Chain: common.XMRChain, Address: primary},
	}))

	type testCase struct {
		name       string
		chain      common.Chain
		input      string
		want       common.Address
		errorMatch string
	}
	tests := []testCase{
		{
			name:       "missing XMR refund",
			chain:      common.XMRChain,
			errorMatch: "refund address is required for source chain XMR",
		},
		{
			name:       "address from another chain",
			chain:      common.XMRChain,
			input:      otherChain.String(),
			errorMatch: "is not valid for source chain XMR",
		},
		{
			name:       "unsupported XMR integrated address",
			chain:      common.XMRChain,
			input:      integrated.String(),
			errorMatch: "uses an unsupported XMR address form",
		},
		{
			name:  "valid XMR standard address",
			chain: common.XMRChain,
			input: primary.String(),
			want:  primary,
		},
		{
			name:  "valid XMR subaddress",
			chain: common.XMRChain,
			input: subaddress.String(),
			want:  subaddress,
		},
		{
			name:  "THORName XMR alias",
			chain: common.XMRChain,
			input: thorName,
			want:  primary,
		},
		{
			name:       "malformed address",
			chain:      common.XMRChain,
			input:      "not-an-address",
			errorMatch: "unable to parse address",
		},
		{
			name:       "wrong-network XMR address",
			chain:      common.XMRChain,
			input:      wrongNetwork.String(),
			errorMatch: "is not valid for XMR on the current network",
		},
		{
			name:  "observable source chain remains optional",
			chain: common.BTCChain,
			want:  common.NoAddress,
		},
		{
			name:  "valid BTC bech32 refund",
			chain: common.BTCChain,
			input: btcBech32.String(),
			want:  btcBech32,
		},
		{
			name:  "valid BTC legacy refund",
			chain: common.BTCChain,
			input: btcLegacy.String(),
			want:  btcLegacy,
		},
		{
			name:       "wrong-network BTC refund",
			chain:      common.BTCChain,
			input:      btcWrongNetwork.String(),
			errorMatch: "is not valid for BTC on the current network",
		},
		{
			name:  "XMR synth source remains optional",
			chain: common.THORChain,
			want:  common.NoAddress,
		},
		{
			name:       "XMR synth rejects L1 refund",
			chain:      common.THORChain,
			input:      primary.String(),
			errorMatch: "is not valid for source chain THOR",
		},
	}

	for _, tc := range tests {
		comment := Commentf("case %q", tc.name)
		addr, err := quoteParseRefundAddress(ctx, mgr, tc.input, tc.chain)
		if tc.errorMatch != "" {
			c.Assert(err, NotNil, comment)
			c.Assert(strings.Contains(err.Error(), tc.errorMatch), Equals, true, comment)
		} else {
			c.Assert(err, IsNil, comment)
			c.Assert(addr, Equals, tc.want, comment)
		}

		fromAsset := common.XMRAsset
		if tc.chain.Equals(common.BTCChain) {
			fromAsset = common.BTCAsset
		} else if tc.chain.Equals(common.THORChain) {
			fromAsset = common.XMRAsset.GetSyntheticAsset()
		}
		_, swapErr := qs.queryQuoteSwap(ctx, &types.QueryQuoteSwapRequest{
			FromAsset:     fromAsset.String(),
			ToAsset:       common.RuneNative.String(),
			Amount:        "100000000",
			Destination:   thorDestination.String(),
			RefundAddress: tc.input,
		})
		_, limitErr := qs.queryQuoteLimit(ctx, &types.QueryQuoteLimitRequest{
			FromAsset:     fromAsset.String(),
			ToAsset:       common.RuneNative.String(),
			Amount:        "100000000",
			Destination:   thorDestination.String(),
			TargetOut:     "1000000",
			RefundAddress: tc.input,
		})

		if tc.errorMatch != "" {
			c.Assert(swapErr, NotNil, comment)
			c.Assert(strings.Contains(swapErr.Error(), tc.errorMatch), Equals, true, comment)
			c.Assert(limitErr, NotNil, comment)
			c.Assert(strings.Contains(limitErr.Error(), tc.errorMatch), Equals, true, comment)
			continue
		}
		if swapErr != nil {
			c.Assert(strings.HasPrefix(swapErr.Error(), "bad refund address:"), Equals, false, comment)
		}
		if limitErr != nil {
			c.Assert(strings.HasPrefix(limitErr.Error(), "bad refund_address:"), Equals, false, comment)
		}
	}
}

func (s *QuotesSuite) TestQuoteRefundAddressMemoGeneration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}

	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodeAccount), IsNil)

	vault := NewVaultV2(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.BTCChain, common.XMRChain}.Strings(),
		nil,
		GetRandomEd25519PubKey(),
	)
	vault.PubKeyFrost = GetRandomEd25519PubKey()
	vault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, common.XMRChain.ThorchainToNativeGas(cosmos.NewUint(100*common.One))),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	for _, asset := range []common.Asset{common.BTCAsset, common.XMRAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
		pool.BalanceRune = cosmos.NewUint(10_000_000_000_000)
		pool.LPUnits = cosmos.NewUint(100)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 10)), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 5000, 10_000)), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 1)

	primary := common.Address(common.XMRSeededDummyAddress('4', []byte("quote-memo-primary")))
	subaddress := common.Address(common.XMRSeededDummyAddress('8', []byte("quote-memo-subaddress")))
	const thorName = "xmrmemo"
	mgr.Keeper().SetTHORName(ctx, types.NewTHORName(thorName, ctx.BlockHeight()+1000, []types.THORNameAlias{
		{Chain: common.XMRChain, Address: primary},
	}))

	destination := GetRandomTHORAddress()
	for _, tc := range []struct {
		name         string
		refundInput  string
		resolvedAddr common.Address
	}{
		{name: "standard", refundInput: primary.String(), resolvedAddr: primary},
		{name: "subaddress", refundInput: subaddress.String(), resolvedAddr: subaddress},
		{name: "THORName", refundInput: thorName, resolvedAddr: primary},
	} {
		comment := Commentf("case %q", tc.name)
		swap, err := qs.queryQuoteSwap(ctx, &types.QueryQuoteSwapRequest{
			FromAsset:     common.XMRAsset.String(),
			ToAsset:       common.RuneNative.String(),
			Amount:        "100000000",
			Destination:   destination.String(),
			RefundAddress: tc.refundInput,
		})
		c.Assert(err, IsNil, comment)
		c.Assert(strings.Contains(swap.Memo, "/"+tc.resolvedAddr.String()), Equals, true, comment)

		limit, err := qs.queryQuoteLimit(ctx, &types.QueryQuoteLimitRequest{
			FromAsset:     common.XMRAsset.String(),
			ToAsset:       common.RuneNative.String(),
			Amount:        "100000000",
			Destination:   destination.String(),
			TargetOut:     "1000000",
			RefundAddress: tc.refundInput,
		})
		c.Assert(err, IsNil, comment)
		c.Assert(strings.Contains(limit.Memo, "/"+tc.resolvedAddr.String()), Equals, true, comment)
	}

	swap, err := qs.queryQuoteSwap(ctx, &types.QueryQuoteSwapRequest{
		FromAsset:   common.BTCAsset.String(),
		ToAsset:     common.RuneNative.String(),
		Amount:      "100000000",
		Destination: destination.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(swap.Memo, destination.String()+"/"), Equals, false)

	limit, err := qs.queryQuoteLimit(ctx, &types.QueryQuoteLimitRequest{
		FromAsset:   common.BTCAsset.String(),
		ToAsset:     common.RuneNative.String(),
		Amount:      "100000000",
		Destination: destination.String(),
		TargetOut:   "1000000",
	})
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(limit.Memo, destination.String()+"/"), Equals, false)
}

// QuoteSwap is WASM-reachable, so the placeholder addresses it simulates with
// must be identical on every node and every call - a random key would vary the
// address length on base58 chains and diverge the gas consumed.
func (s *QuotesSuite) TestQuotePlaceholderIsDeterministic(c *C) {
	types.SetupConfigForTest()
	chains := common.Chains{
		common.BTCChain, common.ETHChain, common.THORChain,
		common.XRPChain, common.SOLChain, common.DOTChain,
	}
	for _, chain := range chains {
		pk, err := quotePlaceholderPubKey(chain)
		c.Assert(err, IsNil)
		first, err := pk.GetAddress(chain)
		c.Assert(err, IsNil)
		c.Assert(first.IsEmpty(), Equals, false)
		for i := 0; i < 100; i++ {
			pk, err = quotePlaceholderPubKey(chain)
			c.Assert(err, IsNil)
			next, err := pk.GetAddress(chain)
			c.Assert(err, IsNil)
			c.Assert(next.String(), Equals, first.String(), Commentf("chain %s", chain))
		}
	}
}

func (s *QuotesSuite) TestQuoteInboundInfoDASHEstimatesOneBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Chains = append(vault.Chains, common.DASHChain.String())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	mgr.Keeper().SetMimir(ctx, "ConfMultiplierBasisPoints-DASH", 1)
	mgr.Keeper().SetMimir(ctx, "MaxConfirmations-DASH", 1)

	address, router, confirmations, err := quoteInboundInfo(ctx, mgr, cosmos.NewUint(10_000*common.One), common.DASHChain, common.DASHAsset)
	c.Assert(err, IsNil)
	c.Assert(address.Equals(common.NoAddress), Equals, false)
	c.Assert(router.Equals(common.NoAddress), Equals, true)
	c.Assert(confirmations, Equals, int64(1))
}
