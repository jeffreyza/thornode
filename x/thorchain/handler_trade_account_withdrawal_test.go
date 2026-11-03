package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerTradeAccountWithdrawal struct{}

var _ = Suite(&HandlerTradeAccountWithdrawal{})

func (HandlerTradeAccountWithdrawal) TestTradeAccountWithdrawalNetworkValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()

	// Enable trade accounts
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsDepositEnabled.String(), 1)

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
		msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(500), addr, addr, dummyTx)
		h := NewTradeAccountDepositHandler(mgr)

		_, err = h.Run(ctx, msg)
		c.Assert(err, IsNil)

		bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
		c.Check(bal.String(), Equals, "500")

		vault := GetRandomVault()
		vault.Status = ActiveVault
		vault.Coins = common.Coins{
			common.NewCoin(asset, cosmos.NewUint(500*common.One)),
		}
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	}

	// Attempt withdrawal to wrong network address should be rejected
	msg := NewMsgTradeAccountWithdrawal(asset.GetTradeAsset(), cosmos.NewUint(350), wrongNetworkAddr, addr, dummyTx)

	// Handler should reject wrong network address
	h := NewTradeAccountWithdrawalHandler(mgr)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*not same network.*")

	// Verify balance unchanged
	bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "500")

	// Verify no outbound created
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (HandlerTradeAccountWithdrawal) TestTradeAccountWithdrawalHaltWithdraw(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	bc1Addr := GetRandomBTCAddress()
	dummyTx := common.Tx{ID: "test"}

	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsDepositEnabled.String(), 1)

	{
		msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(500), addr, addr, dummyTx)
		h := NewTradeAccountDepositHandler(mgr)
		_, err := h.Run(ctx, msg)
		c.Assert(err, IsNil)

		bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
		c.Check(bal.String(), Equals, "500")
	}

	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateTradeAssetHaltWithdraw, asset.Chain), 1)

	msg := NewMsgTradeAccountWithdrawal(asset.GetTradeAsset(), cosmos.NewUint(350), bc1Addr, addr, dummyTx)
	h := NewTradeAccountWithdrawalHandler(mgr)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*trade asset withdrawals are disabled.*")

	bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "500")

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (HandlerTradeAccountWithdrawal) TestTradeAccountWithdrawal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset
	addr := GetRandomBech32Addr()
	bc1Addr := GetRandomBTCAddress()
	txID, err := common.NewTxID("A" + strings.Repeat("0", 63))
	c.Assert(err, IsNil)
	dummyTx := common.Tx{ID: txID}

	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsEnabled.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.TradeAccountsDepositEnabled.String(), 1)

	{
		msg := NewMsgTradeAccountDeposit(asset, cosmos.NewUint(500), addr, addr, dummyTx)

		h := NewTradeAccountDepositHandler(mgr)
		_, err = h.Run(ctx, msg)
		c.Assert(err, IsNil)

		bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
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

	msg := NewMsgTradeAccountWithdrawal(asset.GetTradeAsset(), cosmos.NewUint(350), bc1Addr, addr, dummyTx)

	h := NewTradeAccountWithdrawalHandler(mgr)
	_, err = h.Run(ctx, msg)
	c.Assert(err, IsNil)

	bal := mgr.TradeAccountManager().BalanceOf(ctx, asset, addr)
	c.Check(bal.String(), Equals, "150")

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].Coin.String(), Equals, "350 BTC.BTC")
	c.Check(items[0].ToAddress.String(), Equals, bc1Addr.String())
	// InHash must equal the originating tx ID so that handler_common_outbound
	// can match the observed outbound back to this TxOutItem. Using
	// sha256(ctx.TxBytes()) here would produce a wrong/unrelated hash in any
	// context where ctx.TxBytes() != the cosmos tx whose hash is msg.Tx.ID.
	c.Check(items[0].InHash.Equals(txID), Equals, true)
}
