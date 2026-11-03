package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func (s *QuotesSuite) TestQuoteXMRDestinations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 1)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	vault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	vault.PubKeyFrost = GetRandomEd25519PubKey()
	vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, common.XMRChain.ThorchainToNativeGas(cosmos.NewUint(100*common.One)))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
	pool.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 5000, 10_000)), IsNil)

	for _, tc := range []struct {
		name      string
		address   string
		supported bool
	}{
		{"primary", common.XMRChecksummedDummyAddress('4'), true},
		{"subaddress", common.XMRChecksummedDummyAddress('8'), true},
		{"integrated", "4CdvgtrwKjggnkXTAXRu7peYZruMJNii9EHrneXZie344btcwjvbzMbDvRcXvC4h4667vpwhdQWwPivq8ZfVPsGJeEvzNowpChm17H3MqL", false},
		{"featured", common.XMRChecksummedDummyAddress('C'), false},
		{"featuredwithoutguarantee", "CjWdTpuDaZ69nTGxzm9YarR82YDYFECi1WaaREZTMy5yDsjaRX5bC3cbC3JpcrBPd7YYpjoWKuBMidgGaKBK5Jye2v3wfMHCy", false},
		{"wrongnetwork", "FRw3ZEEFtuK82ifoJK7iFrLuDkpBEc7xx1WjLvqAQc8HgRjJXwrDr37TX9yDq9Ye2yQWfaTvMJpkXQbyPty1pgpZ2m6UsaJbC", false},
		{"otherchain", GetRandomETHAddress().String(), false},
	} {
		mgr.Keeper().SetTHORName(ctx, types.NewTHORName(tc.name, ctx.BlockHeight()+1000, []types.THORNameAlias{{Chain: common.XMRChain, Address: common.Address(tc.address)}}))
		for _, input := range []string{tc.address, tc.name} {
			for _, isWasm := range []bool{false, true} {
				queryCtx := ctx.WithValue(constants.CtxWASMQuery, isWasm)
				comment := Commentf("wasm %t %s input %s", isWasm, tc.name, input)
				swapReq := &types.QueryQuoteSwapRequest{FromAsset: common.RuneNative.String(), ToAsset: common.XMRAsset.String(), Amount: "100000000", Destination: input}
				swap, swapErr := qs.queryQuoteSwap(queryCtx, swapReq)
				limit, limitErr := qs.queryQuoteLimit(queryCtx, &types.QueryQuoteLimitRequest{FromAsset: common.RuneNative.String(), ToAsset: common.XMRAsset.String(), Amount: "100000000", Destination: input, TargetOut: "1000000"})
				if tc.supported {
					c.Check(swapErr, IsNil, comment)
					c.Check(limitErr, IsNil, comment)
					if swap != nil {
						c.Check(strings.Contains(swap.Memo, tc.address), Equals, true, comment)
					}
					if limit != nil {
						c.Check(strings.Contains(limit.Memo, tc.address), Equals, true, comment)
					}
				} else {
					c.Check(swapErr, ErrorMatches, ".*bad destination.*unsupported XMR address form.*", comment)
					c.Check(limitErr, ErrorMatches, ".*bad destination.*unsupported XMR address form.*", comment)
				}
			}
		}
	}
	// The price-only swap quote still allows an omitted destination.
	for _, isWasm := range []bool{false, true} {
		swap, err := qs.queryQuoteSwap(ctx.WithValue(constants.CtxWASMQuery, isWasm), &types.QueryQuoteSwapRequest{FromAsset: common.RuneNative.String(), ToAsset: common.XMRAsset.String(), Amount: "100000000"})
		c.Assert(err, IsNil)
		c.Check(swap.Memo, Equals, "")
	}
}
