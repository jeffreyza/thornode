package thorchain

import (
	"bytes"
	"encoding/hex"

	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
	"github.com/dashpay/dashd-go/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

// TestDashExtendedQuote verifies that a wallet can reconstruct the complete memo
// from the quoted OP_RETURN and network-specific P2PKH continuation outputs.
func (s *QuotesSuite) TestDashExtendedQuote(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := queryServer{mgr: mgr}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	vault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.DASHChain, common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	vault.PubKeyFrost = GetRandomEd25519PubKey()
	vault.AddFunds(common.Coins{
		common.NewCoin(common.DASHAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.XMRAsset, common.XMRChain.ThorchainToNativeGas(cosmos.NewUint(100*common.One))),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	for _, asset := range []common.Asset{common.DASHAsset, common.XMRAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
		pool.BalanceRune = cosmos.NewUint(10_000_000_000_000)
		pool.LPUnits = cosmos.NewUint(100)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	}
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.DASHChain, NewNetworkFee(common.DASHChain, 2000, 2)), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 5000, 10_000)), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.L1SlipMinBps.String(), 1)
	refund, wrongNetwork := dashRefundAddresses(c)
	req := &types.QueryQuoteSwapRequest{
		FromAsset: common.DASHAsset.String(), ToAsset: common.XMRAsset.String(), Amount: "100000000",
		Destination:   common.XMRSeededDummyAddress('4', []byte("dash-extended-quote")),
		RefundAddress: refund.String(),
	}
	_, err := qs.queryQuoteSwap(ctx, req)
	c.Assert(err, ErrorMatches, ".*generated memo too long for source chain.*")
	req.Extended = true
	quote, err := qs.queryQuoteSwap(ctx, req)
	c.Assert(err, IsNil)
	c.Assert(quote.InboundConfirmationBlocks, Equals, int64(1))
	c.Assert(quote.InboundConfirmationSeconds, Equals, int64(150))
	c.Assert(len(quote.Memo) > 80, Equals, true)
	c.Assert(len(quote.Vout), Equals, 1+(len(quote.Memo)-79+19)/20)
	c.Assert(quote.Vout[0].Type, Equals, "op_return")
	c.Assert(quote.Vout[0].Amount, Equals, int64(0))
	memo, err := hex.DecodeString(quote.Vout[0].Data)
	c.Assert(err, IsNil)
	c.Assert(memo, HasLen, 80)
	c.Assert(memo[79], Equals, byte('^'))
	c.Assert(string(memo[:79]), Equals, quote.Memo[:79])
	memo = memo[:79] // the continuation marker is not part of the memo
	params := &chaincfg.MainNetParams
	if common.CurrentChainNetwork == common.MockNet {
		params = &chaincfg.RegressionNetParams
	}
	for _, output := range quote.Vout[1:] {
		c.Assert(output.Type, Equals, "address")
		c.Assert(output.Amount, Equals, int64(546))
		var address btcutil.Address
		address, err = btcutil.DecodeAddress(output.Data, params)
		c.Assert(err, IsNil)
		c.Assert(address.IsForNet(params), Equals, true)
		_, ok := address.(*btcutil.AddressPubKeyHash)
		c.Assert(ok, Equals, true)
		var script []byte
		script, err = txscript.PayToAddrScript(address)
		c.Assert(err, IsNil)
		c.Assert(txscript.GetScriptClass(script), Equals, txscript.PubKeyHashTy)
		memo = append(memo, address.ScriptAddress()...)
	}
	c.Assert(string(bytes.TrimRight(memo, "\x00")), Equals, quote.Memo)
	req.RefundAddress = wrongNetwork.String()
	_, err = qs.queryQuoteSwap(ctx, req)
	c.Assert(err, ErrorMatches, ".*not valid for DASH on the current network.*")
}
