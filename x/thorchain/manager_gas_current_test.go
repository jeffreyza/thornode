package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type GasManagerTestSuite struct{}

var _ = Suite(&GasManagerTestSuite{})

// resetMultiplierForAsset resets the fee multiplier for an asset to the given multiplier
func resetMultiplierForAsset(ctx cosmos.Context, k keeper.Keeper, asset common.Asset, multiplier cosmos.Uint) error {
	surplus := k.GetSurplusForTargetMultiplier(ctx, multiplier)
	spent, err := k.GetOutboundFeeSpentRune(ctx, asset)
	if err != nil {
		return err
	}
	withheld, err := k.GetOutboundFeeWithheldRune(ctx, asset)
	if err != nil {
		return err
	}
	if surplus.GT(withheld.Sub(spent)) {
		return k.AddToOutboundFeeWithheldRune(ctx, asset, surplus.Sub(withheld.Sub(spent)))
	} else {
		return k.AddToOutboundFeeSpentRune(ctx, asset, withheld.Sub(spent).Sub(surplus))
	}
}

func setOutboundFeeMultiplierMimirs(ctx cosmos.Context, k keeper.Keeper) {
	k.SetMimir(ctx, constants.TargetOutboundFeeSurplusRune.String(), 100_00000000) // 100 RUNE
	k.SetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String(), 10_000)
	k.SetMimir(ctx, constants.MaxOutboundFeeMultiplierBasisPoints.String(), 30_000)
}

func (GasManagerTestSuite) TestGasManager(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.K
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)
	gasEvent := gasMgr.gasEvent
	c.Assert(gasMgr, NotNil)
	gasMgr.BeginBlock()
	c.Assert(gasEvent != gasMgr.gasEvent, Equals, true)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	c.Assert(k.SetPool(ctx, pool), IsNil)
	pool.Asset = common.BTCAsset
	c.Assert(k.SetPool(ctx, pool), IsNil)

	gasMgr.AddGasAsset(common.EmptyAsset, common.Gas{
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(37500)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
	}, true)
	c.Assert(gasMgr.GetGas(), HasLen, 2)
	gasMgr.AddGasAsset(common.EmptyAsset, common.Gas{
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(38500)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(2000)),
	}, true)
	c.Assert(gasMgr.GetGas(), HasLen, 2)
	gasMgr.AddGasAsset(common.EmptyAsset, common.Gas{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(38500)),
	}, true)
	c.Assert(gasMgr.GetGas(), HasLen, 3)
	eventMgr := NewDummyEventMgr()
	gasMgr.EndBlock(ctx, k, eventMgr)
}

func (GasManagerTestSuite) TestProcessGasReserveExactMatch(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)
	gasMgr.BeginBlock()

	// Ensure the Reserve holds a known, non-zero RUNE balance.
	FundModule(c, ctx, k, ReserveName, 50*common.One)
	reserveRune := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(reserveRune.IsZero(), Equals, false)

	// A pool with equal RUNE and asset balances converts gas 1:1, so a gas coin of
	// exactly reserveRune asset units yields runeGas == reserveRune (an exact match
	// between the running reserve total and the gas cost of this coin).
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = reserveRune
	pool.BalanceAsset = reserveRune
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	asgardRuneBefore := k.GetRuneBalanceOfModule(ctx, AsgardName)

	gasMgr.AddGasAsset(common.BTCAsset, common.Gas{
		common.NewCoin(common.BTCAsset, reserveRune),
	}, true)

	gasMgr.ProcessGas(ctx, k)

	// The exact-match gas coin must still be reimbursed, not skipped: the reserve
	// had exactly enough RUNE to cover it.
	c.Assert(gasMgr.gasEvent.Pools, HasLen, 1)
	c.Check(gasMgr.gasEvent.Pools[0].RuneAmt.String(), Equals, reserveRune.String())

	// The full reserve was transferred to Asgard for the reimbursement.
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).String(), Equals, "0")
	asgardRuneAfter := k.GetRuneBalanceOfModule(ctx, AsgardName)
	c.Check(asgardRuneAfter.Sub(asgardRuneBefore).String(), Equals, reserveRune.String())

	// The reimbursed pool gained the RUNE and lost the gas asset.
	finalPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(finalPool.BalanceRune.String(), Equals, reserveRune.MulUint64(2).String())
	c.Check(finalPool.BalanceAsset.String(), Equals, "0")
}

func (GasManagerTestSuite) TestGetAssetOutboundFee(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)
	gasMgr.BeginBlock()
	setOutboundFeeMultiplierMimirs(ctx, k)

	// when there is no network fee available, 0 fee and nil error should be returned
	fee, err := gasMgr.GetAssetOutboundFee(ctx, common.AVAXAsset, true)
	c.Assert(fee.Uint64(), Equals, cosmos.ZeroUint().Uint64())
	c.Assert(err, IsNil)

	// should still return nil error if inRune is false
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.AVAXAsset, false)
	c.Assert(fee.Uint64(), Equals, cosmos.ZeroUint().Uint64())
	c.Assert(err, IsNil)

	// set network fee
	networkFee := NewNetworkFee(common.AVAXChain, 1, 10_000) // 10,000 nAVAX
	c.Assert(k.SaveNetworkFee(ctx, common.AVAXChain, networkFee), IsNil)

	// when there is a network fee available, but no gas asset pool, the fee should still be returned
	pool, _ := k.GetPool(ctx, common.AVAXAsset)
	c.Assert(pool.IsEmpty(), Equals, true)
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.AVAXAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(1000))
	c.Assert(err, IsNil)

	// conversion to rune requires a pool, so should return 0 and no error
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.AVAXAsset, true)
	c.Assert(fee.Uint64(), Equals, uint64(0))
	c.Assert(err, IsNil)

	// set pool
	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Asset:        common.AVAXAsset,
		Status:       PoolAvailable,
	}), IsNil)

	// conversion to rune should now work
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.AVAXAsset, true)
	c.Assert(fee.Uint64(), Equals, uint64(1000), Commentf("%d vs %d", fee.Uint64(), uint64(1000)))
	c.Assert(err, IsNil)

	// BTC chain
	networkFee = NewNetworkFee(common.BTCChain, 70, 50)
	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, networkFee), IsNil)

	// No gas pool set, but not needed if no conversion is needed, network fee should be returned
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(err, IsNil)
	c.Assert(fee.Uint64(), Equals, uint64(3500), Commentf("%d vs %d", fee.Uint64(), uint64(3500)))

	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Asset:        common.BTCAsset,
		Status:       PoolAvailable,
	}), IsNil)
	fee, _ = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(3500), Commentf("%d vs %d", fee.Uint64(), uint64(3500)))

	// Synth asset (BTC/BTC)
	sBTC, err := common.NewAsset("BTC/BTC")
	c.Assert(err, IsNil)

	// change the pool balance
	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(500 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Asset:        common.BTCAsset,
		Status:       PoolAvailable,
	}), IsNil)
	synthAssetFee, err := gasMgr.GetAssetOutboundFee(ctx, sBTC, false)
	c.Assert(synthAssetFee.Uint64(), Equals, uint64(0))
	c.Assert(err, IsNil)

	// Trade asset
	tradeAsset, err := common.NewAsset("BTC~BTC")
	c.Assert(err, IsNil)
	tradeAssetFee, err := gasMgr.GetAssetOutboundFee(ctx, tradeAsset, false)
	c.Assert(tradeAssetFee.Uint64(), Equals, uint64(0))
	c.Assert(err, IsNil)

	// A trade asset with inRune true should return the fee in RUNE, not in the trade asset.
	runeFee, err := gasMgr.GetAssetOutboundFee(ctx, tradeAsset, true)
	c.Assert(err, IsNil)
	c.Assert(runeFee.String(), Equals, "0")

	// when MinimumL1OutboundFeeUSD set to something higher, it should override the network fee
	busdAsset, err := common.NewAsset("BSC.BUSD-BD1")
	c.Assert(err, IsNil)
	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(500 * common.One),
		BalanceAsset: cosmos.NewUint(500 * common.One),
		Decimals:     8,
		Asset:        busdAsset,
		Status:       PoolAvailable,
	}), IsNil)
	k.SetMimir(ctx, constants.MinimumL1OutboundFeeUSD.String(), 1_0000_0000)
	k.SetMimir(ctx, "TorAnchor-BSC-BUSD-BD1", 1) // enable BUSD pool as a TOR anchor

	fee, _ = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(20000000), Commentf("%d", fee.Uint64()))

	// when network fee is higher than MinimumL1OutboundFeeUSD, then choose network fee
	networkFee = NewNetworkFee(common.BTCChain, 1000, 50000)
	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, networkFee), IsNil)
	fee, _ = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(50000000), Commentf("%d vs %d", fee.Uint64(), uint64(50000000)))

	// DynamicOutboundFeeMultiplier
	// set mimirs:
	// target surplus: 100 RUNE
	// min multiplier: 10_000
	// max multiplier: 30_000

	// k.SetMimir(ctx, constants.TargetOutboundFeeSurplusRune.String(), 100_00000000) // 100 $RUNE
	// k.SetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String(), 10_000)
	// k.SetMimir(ctx, constants.MaxOutboundFeeMultiplierBasisPoints.String(), 30_000)

	// No surplus to start, initial surplus is set, fee should return with 1x multiplier.
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(50000000), Commentf("%d vs %d", fee.Uint64(), uint64(50000000)))
	c.Assert(err, IsNil)

	// Add a surplus for BTC - multiplier should be 50% of max-min (i.e. 2x)
	c.Assert(resetMultiplierForAsset(ctx, k, common.BTCAsset, cosmos.NewUint(20_000)), IsNil)
	fee, err = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(100000000), Commentf("%d vs %d", fee.Uint64(), uint64(100000000)))
	c.Assert(err, IsNil)

	// Add more surplus for BTC, should be at min multiplier
	c.Assert(k.AddToOutboundFeeWithheldRune(ctx, common.BTCAsset, cosmos.NewUint(50_00000000)), IsNil)
	fee, _ = gasMgr.GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(fee.Uint64(), Equals, uint64(50000000), Commentf("%d vs %d", fee.Uint64(), uint64(50000000)))
	c.Assert(err, IsNil)

	// Add a hypothetical asset on BTC, which should have a different multiplier than BTC
	btcUsd, err := common.NewAsset("BTC.USDC")
	c.Assert(err, IsNil)
	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(500 * common.One),
		BalanceAsset: cosmos.NewUint(200 * common.One),
		Asset:        btcUsd,
		Status:       PoolAvailable,
	}), IsNil)

	fee, err = gasMgr.GetAssetOutboundFee(ctx, btcUsd, false)
	c.Assert(fee.Uint64(), Equals, uint64(100000000), Commentf("%d vs %d", fee.Uint64(), uint64(100000000))) // BTC.USDC should have the current initial multiplier path applied
	c.Assert(err, IsNil)

	// Add a surplus for BTC.USDC - multiplier should be 50% of max-min (i.e. 2x)
	c.Assert(resetMultiplierForAsset(ctx, k, btcUsd, cosmos.NewUint(20_000)), IsNil)
	fee, err = gasMgr.GetAssetOutboundFee(ctx, btcUsd, false)
	c.Assert(fee.Uint64(), Equals, uint64(200000000), Commentf("%d vs %d", fee.Uint64(), uint64(200000000)))
	c.Assert(err, IsNil)

	// Add more surplus for BTC.USDC, should be at min multiplier
	c.Assert(k.AddToOutboundFeeWithheldRune(ctx, btcUsd, cosmos.NewUint(50_00000000)), IsNil)
	fee, err = gasMgr.GetAssetOutboundFee(ctx, btcUsd, false)
	c.Assert(fee.Uint64(), Equals, uint64(100000000), Commentf("%d vs %d", fee.Uint64(), uint64(100000000)))
	c.Assert(err, IsNil)
}

func (GasManagerTestSuite) TestDifferentValidations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)
	gasMgr.BeginBlock()
	helper := newGasManagerTestHelper(k)
	eventMgr := NewDummyEventMgr()
	gasMgr.EndBlock(ctx, helper, eventMgr)

	helper.failGetNetwork = true
	gasMgr.EndBlock(ctx, helper, eventMgr)
	helper.failGetNetwork = false

	helper.failGetPool = true
	gasMgr.AddGasAsset(common.EmptyAsset, common.Gas{
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(37500)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
	}, true)
	gasMgr.EndBlock(ctx, helper, eventMgr)
	helper.failGetPool = false
	helper.failSetPool = true
	p := NewPool()
	p.Asset = common.ATOMAsset
	p.BalanceAsset = cosmos.NewUint(common.One * 100)
	p.BalanceRune = cosmos.NewUint(common.One * 100)
	p.Status = PoolAvailable
	c.Assert(helper.Keeper.SetPool(ctx, p), IsNil)
	gasMgr.AddGasAsset(common.EmptyAsset, common.Gas{
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(37500)),
	}, true)
	gasMgr.EndBlock(ctx, helper, eventMgr)
}

func (GasManagerTestSuite) TestGetMaxGas(c *C) {
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)

	gasCoin, err := gasMgr.GetMaxGas(ctx, common.GAIAChain)
	c.Assert(gasCoin.Amount.IsZero(), Equals, true)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "network fee for chain(GAIA) is invalid"), Equals, true)
	// Code relying on GetMaxGas should not proceed when not able to do a valid calculation.

	var transactionSize uint64 = 1000
	var transactionFeeRate uint64 = 127 // 127 uatom gas rate
	networkFee := NewNetworkFee(common.GAIAChain, transactionSize, transactionFeeRate)
	c.Assert(k.SaveNetworkFee(ctx, common.GAIAChain, networkFee), IsNil)
	gasCoin, err = gasMgr.GetMaxGas(ctx, common.GAIAChain)
	c.Assert(err, IsNil)
	c.Assert(gasCoin.Decimals, Equals, common.GAIAChain.GetGasAssetDecimal())
	c.Assert(gasCoin.Amount.Uint64(), Equals, uint64(1000*(127*3/2)*100))
	// GAIA max gas is converted from uatom to THORChain 1e8 accounting units.

	networkFee = NewNetworkFee(common.ETHChain, 123, 127) // 127 gwei gas rate
	c.Assert(k.SaveNetworkFee(ctx, common.ETHChain, networkFee), IsNil)
	gasCoin, err = gasMgr.GetMaxGas(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(gasCoin.Amount.String(), Equals, "2337")
	// 10 gwei (1e9) -> THORChain Amount (1e8) conversion
}

func (GasManagerTestSuite) TestOutboundFeeMultiplier(c *C) {
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)

	targetSurplus := cosmos.NewUint(100_00000000) // 100 $RUNE
	minMultiplier := cosmos.NewUint(15_000)
	maxMultiplier := cosmos.NewUint(20_000)
	gasSpent := cosmos.ZeroUint()
	gasWithheld := cosmos.ZeroUint()

	// No surplus to start, should return maxMultiplier
	m := gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, maxMultiplier.Uint64())

	// More gas spent than withheld, use maxMultiplier
	gasSpent = cosmos.NewUint(1000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, maxMultiplier.Uint64())

	gasSpent = cosmos.NewUint(100_00000000)
	gasWithheld = cosmos.NewUint(110_00000000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(19_500), Commentf("%d", m.Uint64()))

	// 50% surplus vs target, reduce multiplier by 50%
	gasSpent = cosmos.NewUint(100_00000000)
	gasWithheld = cosmos.NewUint(150_00000000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(17_500), Commentf("%d", m.Uint64()))

	// 75% surplus vs target, reduce multiplier by 75%
	gasSpent = cosmos.NewUint(100_00000000)
	gasWithheld = cosmos.NewUint(175_00000000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(16_250), Commentf("%d", m.Uint64()))

	// 99% surplus vs target, reduce multiplier by 99%
	gasSpent = cosmos.NewUint(100_00000000)
	gasWithheld = cosmos.NewUint(199_00000000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(15_050), Commentf("%d", m.Uint64()))

	// 100% surplus vs target, reduce multiplier by 100%
	gasSpent = cosmos.NewUint(100_00000000)
	gasWithheld = cosmos.NewUint(200_00000000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(15_000), Commentf("%d", m.Uint64()))

	// 110% surplus vs target, still reduce multiplier by 100%
	gasSpent = cosmos.NewUint(100_00000000)
	gasWithheld = cosmos.NewUint(210_00000000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(15_000))

	// If min multiplier somehow gets set above max multiplier, multiplier should return old default (3x)
	maxMultiplier = cosmos.NewUint(10_000)
	m = gasMgr.CalcOutboundFeeMultiplier(ctx, targetSurplus, gasSpent, gasWithheld, maxMultiplier, minMultiplier)
	c.Assert(m.Uint64(), Equals, uint64(30_000), Commentf("%d", m.Uint64()))
}

func (GasManagerTestSuite) TestGetGasDetailsLowRate(c *C) {
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)

	// test with stagenet state case with observed issue on BASE (1e12 gas rate)

	var transactionSize uint64 = 100000
	var transactionFeeRate uint64 = 1000
	networkFee := NewNetworkFee(common.BASEChain, transactionSize, transactionFeeRate)
	c.Assert(k.SaveNetworkFee(ctx, common.BASEChain, networkFee), IsNil)

	maxGasCoin, gasRate, err := gasMgr.GetGasDetails(ctx, common.BASEChain)
	c.Assert(err, IsNil)
	c.Assert(maxGasCoin.Amount.Uint64(), Equals, uint64(15000))
	c.Assert(gasRate, Equals, int64(1500))
}

func (GasManagerTestSuite) TestGetGasDetailsHighPrecisionAsset(c *C) {
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)
	setOutboundFeeMultiplierMimirs(ctx, k)

	networkFee := NewNetworkFee(common.XMRChain, 5000, 10_000)
	c.Assert(k.SaveNetworkFee(ctx, common.XMRChain, networkFee), IsNil)

	maxGasCoin, gasRate, err := gasMgr.GetGasDetails(ctx, common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(maxGasCoin.Asset.Equals(common.XMRAsset), Equals, true)
	c.Assert(maxGasCoin.Amount.Uint64(), Equals, uint64(7500))
	c.Assert(maxGasCoin.Decimals, Equals, common.XMRChain.GetGasAssetDecimal())
	c.Assert(gasRate, Equals, int64(15_000))

	fee, err := gasMgr.GetAssetOutboundFee(ctx, common.XMRAsset, false)
	c.Assert(err, IsNil)
	c.Assert(fee.Uint64(), Equals, uint64(5000))
}

func (GasManagerTestSuite) TestGetGasDetailsLowDecimalAssetDoesNotRoundToZero(c *C) {
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)

	networkFee := NewNetworkFee(common.XRPChain, 1, 10)
	c.Assert(k.SaveNetworkFee(ctx, common.XRPChain, networkFee), IsNil)

	maxGasCoin, gasRate, err := gasMgr.GetGasDetails(ctx, common.XRPChain)
	c.Assert(err, IsNil)
	c.Assert(maxGasCoin.Asset.Equals(common.XRPAsset), Equals, true)
	c.Assert(maxGasCoin.Amount.Uint64(), Equals, uint64(1500))
	c.Assert(maxGasCoin.Decimals, Equals, common.XRPChain.GetGasAssetDecimal())
	c.Assert(gasRate, Equals, int64(15))
}

func (GasManagerTestSuite) TestGetAssetOutboundFeeLowDecimalGasChains(c *C) {
	ctx, k := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	gasMgr := newGasMgr(constAccessor, k)

	testCases := []struct {
		chain common.Chain
		asset common.Asset
	}{
		{chain: common.GAIAChain, asset: common.ATOMAsset},
		{chain: common.NOBLEChain, asset: common.USDCAsset},
	}

	for _, tc := range testCases {
		networkFee := NewNetworkFee(tc.chain, 1000, 127)
		c.Assert(k.SaveNetworkFee(ctx, tc.chain, networkFee), IsNil)
		c.Assert(k.SetPool(ctx, Pool{
			BalanceRune:  cosmos.NewUint(100 * common.One),
			BalanceAsset: cosmos.NewUint(100 * common.One),
			Decimals:     tc.chain.GetGasAssetDecimal(),
			Asset:        tc.asset,
			Status:       PoolAvailable,
		}), IsNil)
		c.Assert(resetMultiplierForAsset(ctx, k, tc.asset, cosmos.NewUint(15_000)), IsNil)

		fee, err := gasMgr.GetAssetOutboundFee(ctx, tc.asset, false)
		c.Assert(err, IsNil)
		c.Assert(fee.Uint64(), Equals, uint64(19050000), Commentf("%s fee %d", tc.chain, fee.Uint64()))
	}
}
