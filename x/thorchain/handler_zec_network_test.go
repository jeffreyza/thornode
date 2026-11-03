package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type ZECNetworkValidationSuite struct{}

var _ = Suite(&ZECNetworkValidationSuite{})

func (s *ZECNetworkValidationSuite) TestZECAddressNetworkValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.constAccessor = constants.NewDummyConstants(nil, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: false,
	}, nil)
	withdrawHandler := NewSecuredAssetWithdrawHandler(mgr)
	liquidityHandler := NewAddLiquidityHandler(mgr)
	nameHandler := NewManageTHORNameHandler(mgr)
	signer := GetRandomBech32Addr()
	runeAddress := GetRandomTHORAddress()
	amount := cosmos.NewUint(common.One)
	tx := common.NewTx(GetRandomTxHash(), runeAddress, GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(common.RuneAsset(), amount)), common.Gas{}, "")
	nameCoin := common.NewCoin(common.RuneAsset(), mgr.Keeper().GetTHORNameRegisterFee(ctx).Add(cosmos.OneUint()))

	for _, tc := range []struct {
		address common.Address
		mocknet bool
	}{
		{"t1h3iyd8qGvtJZ3V5CYryi5158h9RMkRhMU", false},
		{"t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5", false},
		{"tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k", false},
		{"tmEp4k7dSU8eGj32uJPi2wBSGuiNaVFBLN7", true},
		{"t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb", true},
		{"textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays", true},
	} {
		withdraw := NewMsgSecuredAssetWithdraw(common.ZECAsset.GetSecuredAsset(), amount, tc.address, signer, tx)
		liquidity := NewMsgAddLiquidity(tx, common.ZECAsset, amount, cosmos.ZeroUint(), runeAddress, tc.address,
			common.NoAddress, cosmos.ZeroUint(), signer)
		name := NewMsgManageTHORName("zecnetwork", common.ZECChain, tc.address, nameCoin, 0, common.EmptyAsset, signer, signer, 0)
		refund, refundErr := quoteParseRefundAddress(ctx, mgr, tc.address.String(), common.ZECChain)
		valid := tc.mocknet == (common.CurrentChainNetwork == common.MockNet)

		for _, check := range []struct {
			name string
			err  error
		}{
			{"secured withdrawal", withdrawHandler.validate(ctx, *withdraw)},
			{"add liquidity", liquidityHandler.validate(ctx, *liquidity)},
			{"THORName", nameHandler.validate(ctx, *name)},
			{"quote refund", refundErr},
		} {
			comment := Commentf("check: %s, address: %s, network: %s", check.name, tc.address, common.CurrentChainNetwork)
			if valid {
				c.Check(check.err, IsNil, comment)
			} else {
				c.Check(check.err, ErrorMatches, ".*(not same network|not valid for ZEC on the current network).*", comment)
			}
		}
		if valid {
			c.Check(refund, Equals, tc.address)
		} else {
			c.Check(refund, Equals, common.NoAddress)
		}
	}
}
