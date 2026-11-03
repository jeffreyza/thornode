package thorchain

import (
	"bytes"

	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// dashRefundAddresses returns a custom refund address on each Dash network.
func dashRefundAddresses(c *C) (valid, wrong common.Address) {
	mainnet, err := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{1}, 20), &chaincfg.MainNetParams)
	c.Assert(err, IsNil)
	regtest, err := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{1}, 20), &chaincfg.RegressionNetParams)
	c.Assert(err, IsNil)
	if common.CurrentChainNetwork == common.MockNet {
		return common.Address(regtest.String()), common.Address(mainnet.String())
	}
	return common.Address(mainnet.String()), common.Address(regtest.String())
}

func (s TxOutStoreSuite) TestDashRefundAddressNetwork(c *C) {
	valid, wrong := dashRefundAddresses(c)
	sender, err := GetRandomPubKey().GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	for _, tc := range []struct{ refund, want common.Address }{
		{wrong, sender}, {valid, valid}, {common.NoAddress, sender},
	} {
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		mgr.gasMgr = newGasMgr(mgr.GetConstants(), k)
		txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
		mgr.txOutStore = txOutStore
		k.SetMimir(ctx, constants.MinTxOutVolumeThreshold.String(), 0)
		k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
		c.Assert(k.SaveNetworkFee(ctx, common.DASHChain, NewNetworkFee(common.DASHChain, 200, 1)), IsNil)

		pool := NewPool()
		pool.Asset = common.DASHAsset
		pool.Decimals = 8
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.DASHChain}.Strings(), nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(10*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		var vaultAddr common.Address
		vaultAddr, err = vault.PubKey.GetAddress(common.DASHChain)
		c.Assert(err, IsNil)

		memo := "=:BTC.BTC:" + GetRandomBTCAddress().String()
		if !tc.refund.IsEmpty() {
			memo += "/" + tc.refund.String()
		}
		tx := NewObservedTx(common.NewTx(GetRandomTxHash(), sender, vaultAddr,
			common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
			common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1_000))}, memo),
			ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
		voter := NewObservedTxVoter(tx.Tx.ID, nil)
		voter.Height = ctx.BlockHeight()
		voter.Tx = tx
		k.SetObservedTxInVoter(ctx, voter)

		c.Assert(refundTx(ctx, tx, mgr, CodeSwapFail, "test refund", ""), IsNil)
		var items []TxOutItem
		items, err = txOutStore.GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1)
		c.Check(items[0].ToAddress, Equals, tc.want, Commentf("refund address: %s", tc.refund))
		c.Check(items[0].Coin.Asset, Equals, common.DASHAsset)
	}
}

func (s *SlashingSuite) TestDashReverseSwapRefundAddressNetwork(c *C) {
	valid, wrong := dashRefundAddresses(c)
	sender, err := GetRandomPubKey().GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	for _, tc := range []struct{ refund, want common.Address }{
		{wrong, sender}, {valid, valid}, {common.NoAddress, sender},
	} {
		ctx, mgr := setupManagerForTest(c)
		mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
		vault := GetRandomVault()
		originalTx := ObservedTx{Tx: common.Tx{
			ID: GetRandomTxHash(), FromAddress: sender,
			Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
		}}
		toi := TxOutItem{
			Chain: common.BTCChain, InHash: originalTx.Tx.ID, VaultPubKey: vault.PubKey,
			ToAddress: GetRandomBTCAddress(), Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		}
		slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
		c.Assert(slasher.sendFailedSwapReverseSwap(ctx, mgr, toi, vault, originalTx,
			SwapMemo{RefundAddress: tc.refund}, originalTx.Tx.ID), IsNil)
		items := mgr.Keeper().GetSwapQueueIterator(ctx)
		c.Assert(items.Valid(), Equals, true)
		var swap MsgSwap
		c.Assert(mgr.Keeper().Cdc().Unmarshal(items.Value(), &swap), IsNil)
		items.Next()
		c.Check(items.Valid(), Equals, false)
		items.Close()
		c.Check(swap.TargetAsset, Equals, common.DASHAsset)
		c.Check(swap.Destination, Equals, tc.want, Commentf("refund address: %s", tc.refund))
	}
}

func (s *QuotesSuite) TestDashQuoteRefundAddressNetwork(c *C) {
	ctx, mgr := setupManagerForTest(c)
	valid, wrong := dashRefundAddresses(c)
	addr, err := quoteParseRefundAddress(ctx, mgr, valid.String(), common.DASHChain)
	c.Assert(err, IsNil)
	c.Check(addr, Equals, valid)
	_, err = quoteParseRefundAddress(ctx, mgr, wrong.String(), common.DASHChain)
	c.Check(err, ErrorMatches, ".*not valid for DASH on the current network")
}
