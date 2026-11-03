package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type roundedLimitGasManager struct {
	GasManager
}

func (m roundedLimitGasManager) GetAssetOutboundFee(_ cosmos.Context, _ common.Asset, _ bool) (cosmos.Uint, error) {
	return cosmos.NewUint(2), nil
}

func (s AdvSwapQueueSuite) TestCheckWithFeeSwapRoundedFinalSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.gasMgr = roundedLimitGasManager{GasManager: mgr.GasMgr()}
	book := newSwapQueueAdv(mgr.Keeper())
	pool := Pool{
		Asset:        common.BTCAsset,
		BalanceRune:  cosmos.NewUint(1000),
		BalanceAsset: cosmos.NewUint(1000),
	}
	msg := types.MsgSwap{
		Tx: common.Tx{
			Coins: common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(15))),
		},
		TargetAsset:          common.BTCAsset,
		TradeTarget:          cosmos.NewUint(10),
		AffiliateBasisPoints: cosmos.ZeroUint(),
		SwapType:             types.SwapType_limit,
		State: &types.SwapState{
			Quantity:    3,
			Count:       3,
			FailedSwaps: []uint64{0},
			Deposit:     cosmos.NewUint(15),
			In:          cosmos.NewUint(3),
			Out:         cosmos.NewUint(3),
		},
	}

	// Earlier rounded fills left more input than the final five-unit chunk.
	// Its four-unit gross output meets the three-unit proportional target,
	// but the two-unit outbound fee makes the final success non-executable.
	size, target := msg.NextSize()
	c.Assert(size.String(), Equals, "5")
	c.Assert(target.String(), Equals, "3")
	c.Assert(book.checkWithFeeSwap(ctx, mgr, Pools{pool}, msg), Equals, false)

	// With only one success so far, this is not the final chunk. Failed
	// attempts must not cause discovery to apply the final outbound fee.
	msg.State.FailedSwaps = []uint64{0, 1}
	c.Assert(book.checkWithFeeSwap(ctx, mgr, Pools{pool}, msg), Equals, true)
}
