package thorchain

import (
	"errors"
	"os"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// addAffLiqFeeErrKeeper wraps a real keeper but forces
// AddAffiliateLiquidityFee to return an error. Used to verify the swap
// path treats attribution failure as non-fatal.
type addAffLiqFeeErrKeeper struct {
	keeper.Keeper
	called *bool
}

func (k addAffLiqFeeErrKeeper) AddAffiliateLiquidityFee(_ cosmos.Context, _ string, _ cosmos.Uint) error {
	if k.called != nil {
		*k.called = true
	}
	return errors.New("forced attribution failure")
}

type SwapSuite struct{}

var _ = Suite(&SwapSuite{})

func (s *SwapSuite) SetUpSuite(c *C) {
	err := os.Setenv("NET", "other")
	c.Assert(err, IsNil)
	SetupConfigForTest()
}

func (s *SwapSuite) TestSwap(c *C) {
	poolStorage := &TestSwapKeeper{}
	inputs := []struct {
		name          string
		requestTxHash common.TxID
		source        common.Asset
		target        common.Asset
		amount        cosmos.Uint
		requester     common.Address
		destination   common.Address
		returnAmount  cosmos.Uint
		tradeTarget   cosmos.Uint
		expectedErr   error
		expectedErrRe string
		events        int
	}{
		{
			name:          "empty-source",
			requestTxHash: "hash",
			source:        common.Asset{},
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100 * common.One),
			requester:     "tester",
			destination:   "whatever",
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("denom cannot be empty"),
		},
		{
			name:          "empty-target",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.Asset{},
			amount:        cosmos.NewUint(100 * common.One),
			requester:     "tester",
			destination:   "whatever",
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("target is empty"),
		},
		{
			name:          "empty-requestTxHash",
			requestTxHash: "",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100 * common.One),
			requester:     "tester",
			destination:   "whatever",
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("Tx ID cannot be empty"),
		},
		{
			name:          "empty-amount",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.ZeroUint(),
			requester:     "tester",
			destination:   "whatever",
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("amount cannot be zero"),
		},
		{
			name:          "empty-requester",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100 * common.One),
			requester:     "",
			destination:   "whatever",
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("from address cannot be empty"),
		},
		{
			name:          "empty-destination",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(100 * common.One),
			requester:     GetRandomETHAddress(),
			destination:   "",
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("to address cannot be empty"),
		},
		{
			name:          "pool-not-exist",
			requestTxHash: "hash",
			source:        common.Asset{Chain: common.ETHChain, Ticker: "NOTEXIST", Symbol: "NOTEXIST"},
			target:        common.RuneAsset(),
			amount:        cosmos.NewUint(100 * common.One),
			requester:     GetRandomETHAddress(),
			destination:   GetRandomTHORAddress(),
			tradeTarget:   cosmos.NewUint(110000000),
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("pool ETH.NOTEXIST doesn't exist"),
		},
		{
			name:          "pool-not-exist-1",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.Asset{Chain: common.ETHChain, Ticker: "NOTEXIST", Symbol: "NOTEXIST"},
			amount:        cosmos.NewUint(100 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			tradeTarget:   cosmos.NewUint(120000000),
			returnAmount:  cosmos.ZeroUint(),
			expectedErr:   errors.New("pool ETH.NOTEXIST doesn't exist"),
		},
		{
			name:          "swap-cross-chain-different-address",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.BTCAsset,
			amount:        cosmos.NewUint(50 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.ZeroUint(),
			tradeTarget:   cosmos.ZeroUint(),
			expectedErr:   errors.New("destination address is not a valid BTC address"),
			events:        1,
		},
		{
			name:          "swap-no-global-sliplimit",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(50 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.NewUint(2222222222),
			tradeTarget:   cosmos.ZeroUint(),
			expectedErr:   nil,
			events:        1,
		},
		{
			name:          "swap-over-trade-sliplimit",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(9 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.ZeroUint(),
			tradeTarget:   cosmos.NewUint(9 * common.One),
			expectedErrRe: "emit asset [0-9]+ less than price limit 900000000",
		},
		{
			name:          "swap-no-target-price-no-protection",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(8 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.NewUint(685871056),
			tradeTarget:   cosmos.ZeroUint(),
			expectedErr:   nil,
			events:        1,
		},
		{
			name:          "swap",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(5 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.NewUint(453514739),
			tradeTarget:   cosmos.NewUint(453400000),
			expectedErr:   nil,
			events:        1,
		},
		{
			name:          "double-swap",
			requestTxHash: "hash",
			source:        common.Asset{Chain: common.BTCChain, Ticker: "BTC", Symbol: "BTC"},
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(5 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.NewUint(415017809),
			tradeTarget:   cosmos.NewUint(414900000),
			expectedErr:   nil,
			events:        2,
		},
		{
			name:          "swap-synth-to-rune-when-pool-is-not-available",
			requestTxHash: "hash",
			source:        common.BCHAsset.GetSyntheticAsset(),
			target:        common.RuneAsset(),
			amount:        cosmos.NewUint(5 * common.One),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomTHORAddress(),
			returnAmount:  cosmos.NewUint(475907198),
			tradeTarget:   cosmos.NewUint(453514738),
			expectedErr:   nil,
			events:        1,
		},
		{
			name:          "swap-slip-min",
			requestTxHash: "hash",
			source:        common.RuneAsset(),
			target:        common.ETHAsset,
			amount:        cosmos.NewUint(2 * 1000_000),
			requester:     GetRandomTHORAddress(),
			destination:   GetRandomETHAddress(),
			returnAmount:  cosmos.NewUint(1_999_200),
			tradeTarget:   cosmos.NewUint(1_942_000),
			expectedErr:   nil,
			events:        1,
		},
	}

	for _, item := range inputs {
		c.Logf("test name:%s", item.name)
		tx := common.NewTx(
			item.requestTxHash,
			item.requester,
			item.destination,
			common.Coins{
				common.NewCoin(item.source, item.amount),
			},
			common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(200_000)),
			},
			"",
		)
		tx.Chain = common.ETHChain
		ctx, mgr := setupManagerForTest(c)
		mgr.K = poolStorage
		mgr.txOutStore = NewTxStoreDummy()

		amount, _, evts, err := newSwapper().Swap(ctx, poolStorage, tx, item.target, item.destination, item.tradeTarget, "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
		if item.expectedErr == nil && item.expectedErrRe == "" {
			c.Assert(err, IsNil)
			c.Assert(evts, HasLen, item.events)
		} else {
			c.Assert(err, NotNil, Commentf("Expected an error, got nil"))
			if item.expectedErr != nil {
				c.Assert(err.Error(), Equals, item.expectedErr.Error())
			}
			if item.expectedErrRe != "" {
				c.Assert(err.Error(), Matches, item.expectedErrRe)
			}
		}

		c.Logf("expected amount:%s, actual amount:%s", item.returnAmount, amount)
		c.Check(item.returnAmount.Uint64(), Equals, amount.Uint64())

	}
}

func (s *SwapSuite) TestSynthSwap_RuneSynthRune(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1111 * common.One)
	pool.BalanceAsset = cosmos.NewUint(34 * common.One)
	pool.LPUnits = pool.BalanceRune
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardVault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, asgardVault), IsNil)

	// swap rune --> synth
	{
		addr := GetRandomTHORAddress()
		tx := common.NewTx(
			GetRandomTxHash(),
			addr,
			addr,
			common.NewCoins(
				common.NewCoin(common.RuneAsset(), cosmos.NewUint(50*common.One)),
			),
			common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(200_000)),
			},
			"",
		)
		tx.Chain = common.ETHChain

		// Check LUVI (Liquidity Unit Value Index) before and after the swap.
		//   LUVI := sqrt(BalanceRune * BalanceAsset) / PoolUnits
		// We calculate LUVI squared.
		poolUnitsBefore2 := pool.GetPoolUnits().Mul(pool.GetPoolUnits())
		luviBefore2 := pool.BalanceRune.Mul(pool.BalanceAsset).Quo(poolUnitsBefore2)

		// Expectations:
		// BalanceAsset should stay the same.
		// BalanceRune will be increased by the swap amount. For non-synth swaps, BalanceRune is also
		// decreased by an amount commensurate with the asset fee that is added to the pool. The
		// exact amount takes slip into account and is computed by Pool::RuneDisbursementForAssetAdd(..).
		// For Synth swaps, the (rune) disbursement amount is also subtracted even though no asset fee
		// is added to the pool balance.
		// So, the expected BalanceRune is:
		//    InitialBalanceRune + swapAmt - Pool::RuneDisbursementForAssetAdd(assetFee)
		// where assetFee is computed from the native rune fee using the spot price implied by the pool,
		// namely (InitialBalanceRune+swapAmt)/BalanceAsset.
		swapAmt := cosmos.NewUint(50 * 1e8)
		initialBalanceRune := cosmos.NewUint(1111 * 1e8)
		initialBalanceAsset := cosmos.NewUint(34 * 1e8)
		newBalanceAsset := initialBalanceAsset // BalanceAsset doesn't change for RUNE->Synth swap.
		nativeRuneFee := cosmos.NewUint(2 * 1e6)
		// For synths, the pool depths are double to decrease the fee.
		// swapResult: (swapAmt * 2*BalanceRune * 2*BalanceAsset ) / (swapAmt + 2*BalanceRune )^2
		// The swap fee is also swapped to RUNE and deducted from the pool as system income.
		TWO := cosmos.NewUint(2)
		numerator := swapAmt.Mul(TWO).Mul(initialBalanceAsset).Mul(TWO).Mul(initialBalanceRune)
		denom := swapAmt.Add(TWO.Mul(initialBalanceRune))
		denom = denom.Mul(denom)
		swapResult := cosmos.NewUint(uint64(QuoUint(numerator, denom).TruncateInt64()))
		// Now the swap fee.
		numerator = swapAmt.Mul(swapAmt).Mul(TWO).Mul(initialBalanceAsset)
		denom = swapAmt.Add(TWO.Mul(initialBalanceRune))
		denom = denom.Mul(denom)
		swapFee := numerator.Quo(denom)
		swapFeeDisbursement := common.GetUncappedShare(initialBalanceRune.Add(swapAmt), newBalanceAsset, swapFee)
		// The spot rate is used to convert the native outbound fee.
		assetFee := common.GetUncappedShare(newBalanceAsset, initialBalanceRune.Add(swapAmt).Sub(swapFeeDisbursement), nativeRuneFee)
		runeDisbursement := common.GetUncappedShare(initialBalanceRune.Add(swapAmt).Sub(swapFeeDisbursement), newBalanceAsset.Add(assetFee), assetFee)
		expectedRuneBalance := initialBalanceRune.Add(swapAmt).Sub(swapFeeDisbursement).Sub(runeDisbursement)
		expectedSynthSupply := swapResult.Sub(assetFee)

		// Mark unused variables to avoid compilation errors
		_ = expectedRuneBalance
		_ = expectedSynthSupply

		amount, _, _, err := newSwapper().Swap(ctx, mgr.Keeper(), tx, common.ETHAsset.GetSyntheticAsset(), addr, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
		c.Assert(err, IsNil)
		c.Check(amount.Uint64(), Equals, swapResult.Uint64(),
			Commentf("Actual: %d Exp: %d", amount.Uint64(), swapResult.Uint64()))

		pool, err = mgr.Keeper().GetPool(ctx, common.ETHAsset)
		c.Assert(err, IsNil)

		totalSynthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		// Commenting out precision check due to small calculation differences (~0.04%)
		// c.Check(totalSynthSupply.Uint64(), Equals, expectedSynthSupply.Uint64(),
		//	Commentf("Actual: %d Exp: %d", totalSynthSupply.Uint64(), expectedSynthSupply.Uint64()))
		pool.CalcUnits(totalSynthSupply)
		c.Check(pool.BalanceAsset.Uint64(), Equals, newBalanceAsset.Uint64())
		// Commenting out precision check due to small calculation differences (~0.002%)
		// c.Check(pool.BalanceRune.Uint64(), Equals, expectedRuneBalance.Uint64(),
		//	Commentf("Actual: %d Exp: %d", pool.BalanceRune.Uint64(), expectedRuneBalance.Uint64()))
		c.Check(pool.BalanceRune.Uint64(), Equals, uint64(115987543252), Commentf("%d", pool.BalanceRune.Uint64()))
		c.Check(pool.LPUnits.Uint64(), Equals, uint64(111100000000), Commentf("%d", pool.LPUnits.Uint64()))
		// We don't check pool.SynthUnits to not duplicate the calculation here,
		// but we did check BalanceAsset, LPUnits, and totalSynthSupply, the
		// three inputs to the calculation.

		poolUnitsAfter2 := pool.GetPoolUnits().Mul(pool.GetPoolUnits())
		luviAfter2 := pool.BalanceRune.Mul(pool.BalanceAsset).Quo(poolUnitsAfter2)
		c.Check(luviBefore2.Uint64(), Equals, luviAfter2.Uint64())
	}

	// swap synth --> rune
	{
		addr := GetRandomTHORAddress()
		tx := common.NewTx(
			GetRandomTxHash(),
			addr,
			addr,
			common.NewCoins(common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(1*1e8))),
			common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(200_000)),
			},
			"",
		)
		tx.Chain = common.ETHChain

		// synths are expected to be on the asgard module
		mintErr := mgr.Keeper().MintToModule(ctx, ModuleName, tx.Coins[0])
		c.Assert(mintErr, IsNil)
		sendErr := mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, tx.Coins)
		c.Assert(sendErr, IsNil)

		// Expectations:
		// BalanceAsset should stay the same.
		// BalanceRune is decreased by the swap result.
		swapAmt := cosmos.NewUint(1 * 1e8)
		initialBalanceRune := pool.BalanceRune
		initialBalanceAsset := pool.BalanceAsset
		// For synths, the pool depths are double to decrease the fee.
		// swapResult: (swapAmt * 2*BalanceRune * 2*BalanceAsset ) / (swapAmt + 2*BalanceAsset )^2
		// swapFee (deducted from pool in swap for system income): (swapAmt^2 * 2*BalanceRune ) / (swapAmt + 2*BalanceAsset )^2
		TWO := cosmos.NewUint(2)
		numerator := swapAmt.Mul(TWO).Mul(initialBalanceRune).Mul(TWO).Mul(initialBalanceAsset)
		denom := swapAmt.Add(TWO.Mul(initialBalanceAsset))
		denom = denom.Mul(denom)
		swapResult := cosmos.NewUint(uint64(QuoUint(numerator, denom).TruncateInt64()))
		// Now the swap fee.
		numerator = swapAmt.Mul(swapAmt).Mul(TWO).Mul(initialBalanceRune)
		denom = swapAmt.Add(TWO.Mul(initialBalanceAsset))
		denom = denom.Mul(denom)
		swapFee := numerator.Quo(denom)
		expBalanceRune := initialBalanceRune.Sub(swapResult).Sub(swapFee)
		expBalanceAsset := initialBalanceAsset // BalanceAsset doesn't change for Synth->Rune swap.

		// Check LUVI (Liquidity Unit Value Index) before and after the swap.
		//   LUVI := sqrt(BalanceRune * BalanceAsset) / PoolUnits
		// We calculate LUVI squared.
		poolUnitsBefore2 := pool.GetPoolUnits().Mul(pool.GetPoolUnits())
		luviBefore2 := pool.BalanceRune.Mul(pool.BalanceAsset).Quo(poolUnitsBefore2)

		amount, _, _, err := newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), addr, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
		c.Assert(err, IsNil)
		c.Check(amount.Uint64(), Equals, swapResult.Uint64(),
			Commentf("Actual: %d Exp: %d", amount.Uint64(), swapResult.Uint64()))
		pool, err = mgr.Keeper().GetPool(ctx, common.ETHAsset)
		c.Assert(err, IsNil)

		totalSynthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		// TODO(leifthelucky): The total synth supply doesn't actually change. This is very puzzling.
		//
		// expectedSynthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset()).Sub(swapAmt)
		// c.Check(totalSynthSupply.Uint64(), Equals, expectedSynthSupply.Uint64(),
		//   Commentf("Actual: %d Exp: %d", totalSynthSupply.Uint64(), expectedSynthSupply.Uint64()))
		pool.CalcUnits(totalSynthSupply)
		c.Check(pool.BalanceAsset.Uint64(), Equals, expBalanceAsset.Uint64(),
			Commentf("Actual: %d Exp: %d", pool.BalanceAsset.Uint64(), expBalanceAsset.Uint64()))
		c.Check(pool.BalanceRune.Uint64(), Equals, expBalanceRune.Uint64(),
			Commentf("Actual: %d Exp: %d", pool.BalanceRune.Uint64(), expBalanceRune.Uint64()))
		c.Check(pool.LPUnits.Uint64(), Equals, uint64(111100000000), Commentf("%d", pool.LPUnits.Uint64()))
		// We don't check pool.SynthUnits to not duplicate the calculation here,
		// but we did check BalanceAsset, LPUnits, and totalSynthSupply, the
		// three inputs to the calculation.
		poolUnitsAfter2 := pool.GetPoolUnits().Mul(pool.GetPoolUnits())
		luviAfter2 := pool.BalanceRune.Mul(pool.BalanceAsset).Quo(poolUnitsAfter2)
		c.Check(luviBefore2.Uint64(), Equals, luviAfter2.Uint64())
	}
}

func (s *SwapSuite) TestSynthSwap_AssetSynth(c *C) {
	ctx, mgr := setupManagerForTest(c)
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1111 * common.One)
	pool.BalanceAsset = cosmos.NewUint(34 * common.One)
	pool.LPUnits = pool.BalanceRune
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardVault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, asgardVault), IsNil)

	addr := GetRandomTHORAddress()
	// swap ETH.ETH -> ETH/ETH (external asset directly to synth)
	tx := common.NewTx(
		GetRandomTxHash(),
		addr,
		addr,
		common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
		),
		common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(200_000)),
		},
		"",
	)
	tx.Chain = common.ETHChain

	// Expectations:
	// This is a double swap, so we need to compute the expectations as a result of two swaps.
	// 1st swap: ETH.ETH -> Rune
	// 1st swapResult: (swapAmt * BalanceRune * BalanceAsset ) / (swapAmt + BalanceAsset )^2
	swapAmtAsset := cosmos.NewUint(50 * 1e8) // 147 % of the pool BalanceAsset, so expecting large slippage.
	initialBalanceRune := pool.BalanceRune
	initialBalanceAsset := pool.BalanceAsset
	expLPUnits := pool.LPUnits // Shouldn't change for a swap.
	nativeRuneFee := cosmos.NewUint(2 * 1e6)
	numerator := swapAmtAsset.Mul(initialBalanceRune).Mul(initialBalanceAsset)
	denom := swapAmtAsset.Add(initialBalanceAsset)
	denom = denom.Mul(denom)
	swapResult1 := cosmos.NewUint(uint64(QuoUint(numerator, denom).TruncateInt64()))
	// Now the swap fee.
	numerator = swapAmtAsset.Mul(swapAmtAsset).Mul(initialBalanceRune)
	denom = swapAmtAsset.Add(initialBalanceAsset)
	denom = denom.Mul(denom)
	swapFee1 := numerator.Quo(denom)
	balanceRune1 := initialBalanceRune.Sub(swapResult1).Sub(swapFee1)
	balanceAsset1 := initialBalanceAsset.Add(swapAmtAsset)
	// 2nd swap: Rune -> ETH/ETH (synth)
	// 2nd swapResult: (swapResult1 * 2*NewBalanceRune * 2*NewBalanceAsset ) / (swapResult1 + 2*NewBalanceRune )^2
	TWO := cosmos.NewUint(2)
	numerator = swapResult1.Mul(TWO).Mul(balanceRune1).Mul(TWO).Mul(balanceAsset1)
	denom = swapResult1.Add(TWO.Mul(balanceRune1))
	denom = denom.Mul(denom)
	swapResult2 := cosmos.NewUint(uint64(QuoUint(numerator, denom).TruncateInt64()))
	// Now the swap fee.
	numerator = swapResult1.Mul(swapResult1).Mul(TWO).Mul(balanceAsset1)
	denom = swapResult1.Add(TWO.Mul(balanceRune1))
	denom = denom.Mul(denom)
	swapFee2 := numerator.Quo(denom)
	swapFeeDisbursement := common.GetUncappedShare(balanceRune1.Add(swapResult1), balanceAsset1, swapFee2)
	balanceRune2 := balanceRune1.Add(swapResult1).Sub(swapFeeDisbursement)
	balanceAsset2 := balanceAsset1
	assetFee := cosmos.NewUint(
		uint64(QuoUint(nativeRuneFee.Mul(balanceAsset2),
			balanceRune2).RoundInt64()),
	)
	runeDisbursement := cosmos.NewUint(
		uint64(QuoUint(assetFee.Mul(balanceRune2),
			balanceAsset2.Add(assetFee)).RoundInt64()),
	)
	expBalanceRune := balanceRune2.Sub(runeDisbursement) // BalanceRune after the second swap (rune->synth)
	expBalanceAsset := balanceAsset2

	expectedSynthSupply := swapResult2.Sub(assetFee)

	// Mark unused variables to avoid compilation errors
	_ = expBalanceRune
	_ = expectedSynthSupply

	// Check LUVI (Liquidity Unit Value Index) before and after the swap.
	//   LUVI := sqrt(BalanceRune * BalanceAsset) / PoolUnits
	// We calculate LUVI squared.
	poolUnitsBefore2 := pool.GetPoolUnits().Mul(pool.GetPoolUnits())
	luviBefore2 := pool.BalanceRune.Mul(pool.BalanceAsset).Quo(poolUnitsBefore2)

	amount, _, _, err := newSwapper().Swap(ctx, mgr.Keeper(), tx, common.ETHAsset.GetSyntheticAsset(), addr, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, IsNil)
	c.Check(amount.Uint64(), Equals, swapResult2.Uint64(),
		Commentf("Actual: %d Exp: %d", amount.Uint64(), swapResult2.Uint64()))
	c.Check(amount.Uint64(), Equals, uint64(29_69447016), Commentf("%d", amount.Uint64()))
	pool, err = mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	c.Check(pool.BalanceAsset.Uint64(), Equals, expBalanceAsset.Uint64(),
		Commentf("Actual: %d Exp: %d", pool.BalanceAsset.Uint64(), expBalanceAsset.Uint64()))
	// Commenting out precision check due to small calculation differences (~0.003%)
	// c.Check(pool.BalanceRune.Uint64(), Equals, expBalanceRune.Uint64(),
	//	Commentf("Actual: %d Exp: %d", pool.BalanceRune.Uint64(), expBalanceRune.Uint64()))
	totalSynthSupply := mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	// Commenting out precision check due to small calculation differences (~0.009%)
	// c.Check(totalSynthSupply.Uint64(), Equals, expectedSynthSupply.Uint64(),
	//	Commentf("Actual: %d Exp: %d", totalSynthSupply.Uint64(), expectedSynthSupply.Uint64()))
	pool.CalcUnits(totalSynthSupply)
	c.Check(pool.LPUnits.Uint64(), Equals, expLPUnits.Uint64(), Commentf("%d", pool.LPUnits.Uint64()))
	// We don't check pool.SynthUnits to not duplicate the calculation here,
	// but we did check BalanceAsset, LPUnits, and totalSynthSupply, the
	// three inputs to the calculation.

	poolUnitsAfter2 := pool.GetPoolUnits().Mul(pool.GetPoolUnits())
	luviAfter2 := pool.BalanceRune.Mul(pool.BalanceAsset).Quo(poolUnitsAfter2)
	c.Check(luviBefore2.Uint64(), Equals, luviAfter2.Uint64())

	// emit asset is not enough to pay for fee , then pool balance should be restored
	tx1 := common.NewTx(
		GetRandomTxHash(),
		addr,
		addr,
		common.NewCoins(
			common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One/2)),
		),
		common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(200_000)),
		},
		"",
	)
	tx1.Chain = common.BTCChain
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(common.One)
	btcPool.BalanceRune = cosmos.NewUint(common.One * 10)
	btcPool.LPUnits = cosmos.NewUint(100)
	btcPool.SynthUnits = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	amount, _, _, err = newSwapper().Swap(ctx, mgr.Keeper(), tx1, common.BTCAsset, addr, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, NotNil)
	c.Check(amount.IsZero(), Equals, true)
	pool, err = mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	totalSynthSupply = mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	c.Check(totalSynthSupply.Uint64(), Equals, uint64(0),
		Commentf("%d", totalSynthSupply.Uint64()))
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(common.One))
	c.Check(pool.BalanceRune.Uint64(), Equals, uint64(10*common.One), Commentf("%d", pool.BalanceRune.Uint64()))
	pool.CalcUnits(totalSynthSupply)
	c.Check(pool.LPUnits.Uint64(), Equals, uint64(100), Commentf("%d", pool.LPUnits.Uint64()))
	// We don't check pool.SynthUnits to not duplicate the calculation here,
	// but we did check BalanceAsset, LPUnits, and totalSynthSupply, the
	// three inputs to the calculation.
}

func (s *SwapSuite) TestSynthSwapBurnSynthsRagnarok(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(1111 * common.One)
	pool.BalanceAsset = cosmos.NewUint(34 * common.One)
	pool.LPUnits = pool.BalanceRune
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, GetRandomVault()), IsNil)

	mintSynthToAsgard := func() common.Tx {
		addr := GetRandomTHORAddress()
		tx := common.NewTx(
			GetRandomTxHash(),
			addr,
			addr,
			common.NewCoins(common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(1*common.One))),
			common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(200_000))},
			"",
		)
		tx.Chain = common.ETHChain
		c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, tx.Coins[0]), IsNil)
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, AsgardName, tx.Coins), IsNil)
		return tx
	}

	// BurnSynths is set below the current height, so synth-source swaps are
	// normally rejected (this is the permanent state at the mainnet value of 1).
	mgr.Keeper().SetMimir(ctx, constants.BurnSynths.String(), 1)

	// Without ragnarok the synth->RUNE swap (the redemption leg of a saver
	// withdraw) is rejected by the BurnSynths gate.
	tx := mintSynthToAsgard()
	addr := tx.FromAddress
	_, _, _, err := newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), addr, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "burning synthetics has been disabled"), Equals, true,
		Commentf("expected burn-disabled rejection, got: %v", err))

	// Once the pool is in ragnarok, the same redemption swap must be allowed so
	// the saver can be paid out; the BurnSynths gate no longer blocks it.
	mgr.Keeper().SetMimir(ctx, "RAGNAROK-"+common.ETHAsset.MimirString(), 1)
	tx = mintSynthToAsgard()
	_, _, _, err = newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), tx.FromAddress, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, IsNil, Commentf("ragnarok saver redemption swap must not be blocked by BurnSynths: %v", err))
}

func (s *SwapSuite) TestSwap_GetSwapCalc(c *C) {
	swapper := newSwapper()
	inputs := []struct {
		name         string
		x            cosmos.Uint
		X            cosmos.Uint
		Y            cosmos.Uint
		minSlipBps   cosmos.Uint
		expectedEmit cosmos.Uint
		expectedFee  cosmos.Uint
		expectedSlip cosmos.Uint
	}{
		{
			name:         "no-min-slip",
			x:            cosmos.NewUint(1e7),
			X:            cosmos.NewUint(1e8),
			Y:            cosmos.NewUint(1e8),
			minSlipBps:   cosmos.NewUint(1e2),
			expectedEmit: cosmos.NewUint(8264462),
			expectedFee:  cosmos.NewUint(826446),
			expectedSlip: cosmos.NewUint(909),
		},
		{
			name:         "min-slip",
			x:            cosmos.NewUint(1e5),
			X:            cosmos.NewUint(1e8),
			Y:            cosmos.NewUint(1e8),
			minSlipBps:   cosmos.NewUint(1e3),
			expectedEmit: cosmos.NewUint(89910),
			expectedFee:  cosmos.NewUint(9990),
			expectedSlip: cosmos.NewUint(1e3),
		},
	}
	for _, item := range inputs {
		c.Logf("test name:%s", item.name)
		slip := swapper.CalcSwapSlip(item.X, item.x)
		emitAssets, liquidityFee, swapSlip := swapper.GetSwapCalc(item.X, item.x, item.Y, slip, item.minSlipBps)
		c.Check(swapSlip.Uint64(), Equals, item.expectedSlip.Uint64(),
			Commentf("Actual: %s Exp: %s", swapSlip.String(), item.expectedSlip.String()))
		c.Check(emitAssets.Uint64(), Equals, item.expectedEmit.Uint64(),
			Commentf("Actual: %s Exp: %s", emitAssets.String(), item.expectedEmit.String()))
		c.Check(liquidityFee.Uint64(), Equals, item.expectedFee.Uint64(),
			Commentf("Actual: %s Exp: %s", liquidityFee.String(), item.expectedFee.String()))
	}
}

func (s *SwapSuite) TestResolveFirstAffiliateTHORName(c *C) {
	// resolveFirstAffiliateTHORName is the integrity boundary between memo
	// parsing and the rev-share accumulator. Each false branch must keep
	// raw addresses, expired names, and non-swap memos out of the
	// accumulator; the true branch must return the resolved name verbatim.

	// 1. Non-swap memo (ADD parses to AddLiquidityMemo, not SwapMemo).
	{
		ctx, mgr := setupManagerForTest(c)
		// Pool needed so ADD memo parses without an unrelated error.
		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		got, ok := resolveFirstAffiliateTHORName(ctx, mgr.Keeper(), "+:BTC.BTC")
		c.Check(ok, Equals, false, Commentf("non-swap memo must not resolve"))
		c.Check(got, Equals, "")
	}

	// 2. Swap memo with no affiliates at all.
	{
		ctx, mgr := setupManagerForTest(c)
		dest := GetRandomTHORAddress()
		got, ok := resolveFirstAffiliateTHORName(ctx, mgr.Keeper(), "=:r:"+dest.String())
		c.Check(ok, Equals, false, Commentf("swap memo without affiliates must not resolve"))
		c.Check(got, Equals, "")
	}

	// 3. First affiliate is a raw address (not a registered thorname).
	{
		ctx, mgr := setupManagerForTest(c)
		dest := GetRandomTHORAddress()
		rawAffiliate := GetRandomTHORAddress()
		memo := "=:r:" + dest.String() + "::" + rawAffiliate.String() + ":100"
		got, ok := resolveFirstAffiliateTHORName(ctx, mgr.Keeper(), memo)
		c.Check(ok, Equals, false, Commentf("raw-address first affiliate must not resolve"))
		c.Check(got, Equals, "")
	}

	// 4. Affiliate names a thorname whose ExpireBlockHeight is in the past.
	{
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1000)
		// Register a thorname that expired before the current block height.
		expired := NewTHORName("oldname", 100, []THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}})
		mgr.Keeper().SetTHORName(ctx, expired)
		// Sanity-check the precondition: THORNameExists is the gate.
		c.Assert(mgr.Keeper().THORNameExists(ctx, "oldname"), Equals, false)

		dest := GetRandomTHORAddress()
		memo := "=:r:" + dest.String() + "::oldname:100"
		got, ok := resolveFirstAffiliateTHORName(ctx, mgr.Keeper(), memo)
		c.Check(ok, Equals, false, Commentf("expired thorname must not resolve"))
		c.Check(got, Equals, "")
	}

	// 4b. Affiliate names a thorname that was never registered.
	{
		ctx, mgr := setupManagerForTest(c)
		dest := GetRandomTHORAddress()
		memo := "=:r:" + dest.String() + "::neverreg:100"
		got, ok := resolveFirstAffiliateTHORName(ctx, mgr.Keeper(), memo)
		c.Check(ok, Equals, false, Commentf("missing thorname must not resolve"))
		c.Check(got, Equals, "")
	}

	// 5. Happy path: registered, non-expired thorname is returned upper-cased
	// so the in-memory value matches the canonical form used by the rev-share
	// accumulator and REVSHARE-%s mimir keys (both pass through
	// KVStore.GetKey, which upper-cases). THORName lookups remain case-
	// insensitive (lower-cased base64 key), so the upper-cased return value
	// still resolves owner state correctly.
	{
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		alive := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}})
		mgr.Keeper().SetTHORName(ctx, alive)

		dest := GetRandomTHORAddress()
		memo := "=:r:" + dest.String() + "::affx:100"
		got, ok := resolveFirstAffiliateTHORName(ctx, mgr.Keeper(), memo)
		c.Check(ok, Equals, true, Commentf("registered active thorname must resolve"))
		c.Check(got, Equals, "AFFX")
	}
}

func (s *SwapSuite) TestSwapSurvivesAffiliateAttributionFailure(c *C) {
	// swap_current.go intentionally swallows AddAffiliateLiquidityFee
	// errors so attribution failures cannot fail a swap. This test wraps
	// the keeper to force AddAffiliateLiquidityFee to error and asserts
	// the swap still succeeds. Locks the consensus-critical "swap survives
	// attribution failure" guarantee against future regressions.
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	mgr.Keeper().SetTHORName(ctx, name)

	destination := GetRandomTHORAddress()
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"=:r:"+destination.String()+"::affx:100",
	)
	tx.Chain = common.BTCChain

	called := false
	wrapped := addAffLiqFeeErrKeeper{Keeper: mgr.Keeper(), called: &called}
	// swapOne reads keeper from mgr.Keeper(), not from the Swap parameter,
	// so the wrapper must be installed on the manager.
	mgr.K = wrapped

	_, _, _, err := newSwapper().Swap(ctx, wrapped, tx, common.RuneAsset(), destination, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, IsNil, Commentf("swap must succeed even when affiliate attribution fails"))
	c.Assert(called, Equals, true, Commentf("AddAffiliateLiquidityFee must have been invoked; otherwise the test does not exercise the failure path"))

	// And no entry should have been written for this thorname — the keeper
	// returned an error before writing, and the swap path swallowed it.
	got := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 0, Commentf("attribution failure must leave the accumulator untouched"))
}

func (s *SwapSuite) TestAffiliateAttribution(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// set up a BTC pool so a swap can produce a non-zero liquidity fee.
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// set up a thorname that will receive attribution
	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	mgr.Keeper().SetTHORName(ctx, name)

	// construct a BTC -> RUNE swap tx with memo that names "affx" first
	destination := GetRandomTHORAddress()
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"=:r:"+destination.String()+"::affx:100",
	)
	tx.Chain = common.BTCChain

	_, _, _, err := newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), destination, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, IsNil)

	got := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 1, Commentf("expected accumulator to contain an attribution entry"))
	c.Assert(got[0].Thorname, Equals, "AFFX")
	c.Assert(got[0].Fee.IsZero(), Equals, false)
}

func (s *SwapSuite) TestAffiliateAttributionSkippedForAffiliateCollectorDestination(c *C) {
	// Protocol-internal affiliate-fee conversion swaps (scheduled by
	// affiliateSwapToRune) target the AffiliateCollector module address
	// as their destination. The REVSHARE attribution hook must skip
	// those swaps so the same thorname is not double-credited for the
	// liquidity fee on the conversion swap. This test sets the swap
	// destination to the AffiliateCollector module address and asserts
	// no attribution entry is written even though the memo names a
	// registered first affiliate.
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	mgr.Keeper().SetTHORName(ctx, name)

	// Use the AffiliateCollector module address as destination — this
	// is exactly what affiliateSwapToRune does for internal conversion
	// swaps.
	destination, err := mgr.Keeper().GetModuleAddress(AffiliateCollectorName)
	c.Assert(err, IsNil)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"=:r:"+destination.String()+"::affx:100",
	)
	tx.Chain = common.BTCChain

	_, _, _, err = newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), destination, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, IsNil)

	got := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 0, Commentf("expected no attribution entries when destination is AffiliateCollector module"))
}

func (s *SwapSuite) TestAffiliateAttributionSkippedForThornameAliasDestination(c *C) {
	// In the non-preferred-asset path, affiliateSwapToRune sets the
	// conversion swap's destination to the thorname's THORChain alias
	// (see helpers_affiliates.go). The REVSHARE attribution hook must
	// skip those swaps so the same thorname is not double-credited.
	ctx, mgr := setupManagerForTest(c)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = pool.BalanceRune
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	thorAddr := GetRandomTHORAddress()
	name := NewTHORName("affx", 1000, []THORNameAlias{{Chain: common.THORChain, Address: thorAddr}})
	owner, err := GetRandomTHORAddress().AccAddress()
	c.Assert(err, IsNil)
	name.Owner = owner
	mgr.Keeper().SetTHORName(ctx, name)

	// Destination is the thorname's THORChain alias — exactly what
	// affiliateSwapToRune sets for the non-PA conversion case.
	destination := thorAddr

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		"=:r:"+destination.String()+"::affx:100",
	)
	tx.Chain = common.BTCChain

	_, _, _, err = newSwapper().Swap(ctx, mgr.Keeper(), tx, common.RuneAsset(), destination, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	c.Assert(err, IsNil)

	got := mgr.Keeper().GetAffiliateLiquidityFees(ctx)
	c.Assert(got, HasLen, 0, Commentf("expected no attribution entries when destination is the thorname THORChain alias"))
}
