package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerSecuredAssetWithdraw struct{}

var _ = Suite(&HandlerSecuredAssetWithdraw{})

func (HandlerSecuredAssetWithdraw) TestSecuredAssetWithdrawNetworkValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()

	// Test that addresses from a different network are rejected
	// MainNet and StageNet both use bc1 (mainnet format), MockNet uses tb1 (testnet format)
	var wrongNetworkAddr common.Address
	var err error
	if common.CurrentChainNetwork == common.MockNet {
		// On mocknet, mainnet address is wrong
		wrongNetworkAddr, err = common.NewAddress("bc1pywlmh5y7rml8qley0pgjrczqdtfxh09d99kgykk4cqcvg6rudjws3cfspm")
	} else {
		// On mainnet/stagenet, testnet address is wrong
		wrongNetworkAddr, err = common.NewAddress("tb1pywlmh5y7rml8qley0pgjrczqdtfxh09d99kgykk4cqcvg6rudjwsxsllm5")
	}
	c.Assert(err, IsNil)

	dummyTx := common.Tx{ID: "test"}

	// Setup: Deposit some assets first
	{
		msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(500), addr, addr, dummyTx)
		h := NewSecuredAssetDepositHandler(mgr)

		pool, poolErr := mgr.K.GetPool(ctx, asset)
		c.Assert(poolErr, IsNil)
		pool.Asset = asset
		poolErr = mgr.K.SetPool(ctx, pool)
		c.Assert(poolErr, IsNil)

		_, runErr := h.Run(ctx, msg)
		c.Assert(runErr, IsNil)

		bal := mgr.SecuredAssetManager().BalanceOf(ctx, asset, addr)
		c.Check(bal.String(), Equals, "500")
	}

	// Attempt withdrawal to wrong network address should be rejected
	msg := NewMsgSecuredAssetWithdraw(asset.GetSecuredAsset(), cosmos.NewUint(350), wrongNetworkAddr, addr, dummyTx)

	// Handler should reject wrong network address
	h := NewSecuredAssetWithdrawHandler(mgr)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*not same network.*")

	// Verify balance unchanged
	bal := mgr.SecuredAssetManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "500")

	// Verify no outbound created
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (HandlerSecuredAssetWithdraw) TestSecuredAssetWithdraw(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	bc1Addr := GetRandomBTCAddress()
	dummyTx := common.Tx{ID: GetRandomTxHash()}
	// If the ID were "test" rather than a valid hash, the TxOutItem's Memo would be "OUT:test",
	// the txout manager's ParseMemoWithTHORNames to determine whether a Ragnarok or not would fail,
	// and by default the txout manager would deduct MaxGas like a Ragnarok rather than attempting outbound fee calculation.

	{
		msg := NewMsgSecuredAssetDeposit(asset, cosmos.NewUint(500), addr, addr, dummyTx)

		h := NewSecuredAssetDepositHandler(mgr)

		pool, err := mgr.K.GetPool(ctx, asset)
		c.Assert(err, IsNil)
		pool.Asset = asset
		err = mgr.K.SetPool(ctx, pool)
		c.Assert(err, IsNil)

		_, err = h.Run(ctx, msg)
		c.Assert(err, IsNil)

		bal := mgr.SecuredAssetManager().BalanceOf(ctx, asset, addr)
		c.Check(bal.String(), Equals, "500")

		vault := GetRandomVault()
		vault.Status = ActiveVault
		vault.Coins = common.Coins{
			common.NewCoin(asset, cosmos.NewUint(500*common.One)),
		}
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	}

	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.BTCChain, NetworkFee{
		Chain: common.BTCChain, TransactionSize: 80000, TransactionFeeRate: 30,
	}), IsNil)

	msg := NewMsgSecuredAssetWithdraw(asset.GetSecuredAsset(), cosmos.NewUint(350), bc1Addr, addr, dummyTx)

	h := NewSecuredAssetWithdrawHandler(mgr)
	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)

	bal := mgr.SecuredAssetManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "150")

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].Coin.String(), Equals, "350 BTC.BTC")
	c.Check(items[0].ToAddress.String(), Equals, bc1Addr.String())

	bankBals := mgr.coinKeeper.GetAllBalances(ctx, addr)
	c.Check(bankBals.String(), Equals, "150btc-btc")
}
