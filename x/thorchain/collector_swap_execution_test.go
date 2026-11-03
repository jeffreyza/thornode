package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type CollectorSwapExecutionSuite struct{}

type collectorCleanupFailKeeper struct{ keeper.Keeper }

func (k collectorCleanupFailKeeper) SetPool(cosmos.Context, Pool) error {
	return fmt.Errorf("injected cleanup pool write failure")
}

var _ = Suite(&CollectorSwapExecutionSuite{})

func setupCollectorSwapExecution(c *C) (cosmos.Context, *Mgrs, THORName) {
	ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
	// Use actual native outbound fees and bank delivery, not the fixed-fee
	// threshold fixture: the module balance must match the ledger exactly.
	gasMgr, ok := mgr.gasMgr.(affiliateCollectorIntegrationGasManager)
	c.Assert(ok, Equals, true)
	mgr.gasMgr = gasMgr.GasManager
	vault := GetRandomVault()
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	return ctx, mgr, few
}

func collectorConversionForTest(ctx cosmos.Context, mgr Manager, name THORName, version types.SwapVersion) *MsgSwap {
	tx := common.NewTx(GetRandomTxHash(), GetRandomBTCAddress(), GetRandomBTCAddress(),
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))},
		fmt.Sprintf("=:THOR.RUNE:%s::%s:0", name.Owner, name.Name))
	msg := NewMsgSwap(tx, common.RuneAsset(), common.Address(mgr.Keeper().GetModuleAccAddress(AffiliateCollectorName).String()),
		cosmos.ZeroUint(), common.Address(name.Owner.String()), cosmos.ZeroUint(), "", "", nil, MarketSwap, 1, 0, version, name.Owner)
	msg.Index = 1
	return msg
}

func assertCollectorSwapBTCSolvency(c *C, ctx cosmos.Context, mgr Manager, expected math.Int) {
	network := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	amounts, err := network.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(amounts, HasLen, 1)
	c.Assert(amounts[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(amounts[0].Amount.Equal(expected), Equals, true,
		Commentf("expected BTC solvency %s, got %s", expected, amounts[0].Amount))
}

func (CollectorSwapExecutionSuite) TestLegacyCollectorConversionAfterAdvancedQueueEnabled(c *C) {
	ctx, mgr, few := setupCollectorSwapExecution(c)
	k := mgr.Keeper()
	msg := collectorConversionForTest(ctx, mgr, few, types.SwapVersion_v1)
	c.Assert(k.SetSwapQueueItem(ctx, *msg, int(msg.Index)), IsNil)
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	c.Assert(newSwapQueue(k).EndBlock(ctx, mgr), IsNil)
	c.Assert(k.HasSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	collector, err := k.GetAffiliateCollector(ctx, few.Name)
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.IsZero(), Equals, false)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(collector.RuneAmount), Equals, true)
	assertAffiliateIntegrationInvariant(c, ctx, k)
}

func (CollectorSwapExecutionSuite) TestLegacyCollectorPayoutAfterAdvancedQueueEnabled(c *C) {
	ctx, mgr, few := setupCollectorSwapExecution(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	collector := fundNamedCollector(c, ctx, k, few.Name, 10*common.One)
	c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, collector, 1), IsNil)
	queued := affiliateIntegrationQueuedSwaps(c, ctx, k)
	c.Assert(queued, HasLen, 1)
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	c.Assert(newSwapQueue(k).EndBlock(ctx, mgr), IsNil)
	c.Assert(k.HasSwapQueueItem(ctx, queued[0].Tx.ID, 1), Equals, false)
	voter, err := k.GetObservedTxInVoter(ctx, queued[0].Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 1)
	c.Assert(voter.Actions[0].ToAddress.Equals(few.GetAlias(common.BTCChain)), Equals, true)
	c.Assert(voter.Actions[0].Coin.Amount.IsZero(), Equals, false)
	assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
	assertAffiliateIntegrationInvariant(c, ctx, k)
}

func (CollectorSwapExecutionSuite) TestAdvancedCollectorConversionSimulationSettlesOnce(c *C) {
	ctx, mgr, few := setupCollectorSwapExecution(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	ctx = ctx.WithValue(constants.CtxSimulationMode, true)
	msg := collectorConversionForTest(ctx, mgr, few, types.SwapVersion_v2)
	_, emit, err := NewSwapHandler(mgr).RunWithEmit(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(emit.IsZero(), Equals, false)
	// Even in simulation, the V2 swap leaves proceeds in Asgard. Its single
	// settlement transfers and credits together; quote behavior cannot cause
	// one transfer during Swap and another during settlement.
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).IsZero(), Equals, true)
	assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
	msg.State.In = msg.State.Deposit
	msg.State.Out = emit
	msg.State.Count = 1
	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)
	c.Assert(settleSwap(ctx, mgr, *msg, "completed"), IsNil)
	c.Assert(settleSwap(ctx, mgr, *msg, "duplicate"), IsNil)
	collector, err := k.GetAffiliateCollector(ctx, few.Name)
	c.Assert(err, IsNil)
	fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.RuneAsset(), true)
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.Equal(emit.Sub(fee)), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(collector.RuneAmount), Equals, true)
	assertAffiliateIntegrationInvariant(c, ctx, k)
}

func (CollectorSwapExecutionSuite) TestExternalModuleDestinationStillRefunds(c *C) {
	ctx, mgr, few := setupCollectorSwapExecution(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	fundNamedCollector(c, ctx, k, few.Name, 1000)
	msg := collectorConversionForTest(ctx, mgr, few, types.SwapVersion_v2)
	// An external memo addresses the module directly. It is not an internal
	// conversion, even if it contains an affiliate THORName.
	msg.Tx.Memo = fmt.Sprintf("=:THOR.RUNE:%s::few:0", msg.Destination)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 1)
	msg.Tx.ToAddress, err = vaults[0].GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	voter := NewObservedTxVoter(msg.Tx.ID, nil)
	voter.Tx = ObservedTx{Tx: msg.Tx, ObservedPubKey: vaults[0].PubKey, BlockHeight: ctx.BlockHeight()}
	k.SetObservedTxInVoter(ctx, voter)
	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)
	c.Assert(k.SetAdvSwapQueueIndex(ctx, *msg), IsNil)
	fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, false)
	c.Assert(fee.LT(msg.State.Deposit), Equals, true)

	_, _, err = NewSwapHandler(mgr).RunWithEmit(ctx, msg)
	c.Assert(err, ErrorMatches, "external swap cannot target the affiliate collector module")
	c.Assert(settleSwap(ctx, mgr, *msg, err.Error()), IsNil)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	indexed, err := k.HasAdvSwapQueueIndex(ctx, *msg)
	c.Assert(err, IsNil)
	c.Assert(indexed, Equals, false)
	voter, err = k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 1)
	refund := voter.Actions[0]
	c.Assert(refund.ToAddress.Equals(msg.Tx.FromAddress), Equals, true)
	c.Assert(refund.Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(refund.Coin.Amount.Equal(msg.State.Deposit.Sub(fee)), Equals, true)
	c.Assert(refund.GetMemo(), Equals, NewRefundMemo(msg.Tx.ID).String())
	c.Assert(refund.VaultPubKey.Equals(vaults[0].PubKey), Equals, true)
	assertNamedAffiliateBalance(c, ctx, k, few.Name, 1000)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "1000")
	assertAffiliateIntegrationInvariant(c, ctx, k)
}

func (CollectorSwapExecutionSuite) TestConversionUsesMemoNameAfterOwnerChangeAndExpiry(c *C) {
	for _, name := range []string{"FEW", "THORSWAP.FI", strings.Repeat("a", 31)} {
		for _, version := range []types.SwapVersion{types.SwapVersion_v1, types.SwapVersion_v2} {
			ctx, mgr, few := setupCollectorSwapExecution(c)
			k := mgr.Keeper()
			few.Name = name
			msg := collectorConversionForTest(ctx, mgr, few, version)
			few.Owner = GetRandomBech32Addr()
			few.ExpireBlockHeight = ctx.BlockHeight() - 1
			k.SetTHORName(ctx, few)
			_, emit, err := NewSwapHandler(mgr).RunWithEmit(ctx, msg)
			c.Assert(err, IsNil)
			c.Assert(emit.IsZero(), Equals, false)
			if msg.IsV2() {
				assertNamedAffiliateBalance(c, ctx, k, name, 0)
				msg.State.In = msg.State.Deposit
				msg.State.Out = emit
				msg.State.Count = 1
				c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)
				c.Assert(settleSwap(ctx, mgr, *msg, "complete after expiry"), IsNil)
			}
			fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.RuneAsset(), true)
			c.Assert(err, IsNil)
			collector, err := k.GetAffiliateCollector(ctx, name)
			c.Assert(err, IsNil)
			c.Assert(collector.RuneAmount.Equal(emit.Sub(fee)), Equals, true)
			c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(collector.RuneAmount), Equals, true)
			c.Assert(k.GetBalance(ctx, few.Owner).AmountOf(common.RuneAsset().Native()).IsZero(), Equals, true)
			assertAffiliateIntegrationInvariant(c, ctx, k)
		}
	}
}

func (CollectorSwapExecutionSuite) TestConversionDeliveryFailureRollsBackBankAndRetries(c *C) {
	ctx, mgr, few := setupCollectorSwapExecution(c)
	k := mgr.Keeper()
	msg := collectorConversionForTest(ctx, mgr, few, types.SwapVersion_v2)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 1)
	// Observation has already credited the vault with the conversion's input.
	vaults[0].Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(101*common.One)))
	c.Assert(k.SetVault(ctx, vaults[0]), IsNil)
	_, emit, err := NewSwapHandler(mgr).RunWithEmit(ctx, msg)
	c.Assert(err, IsNil)
	msg.State.In = msg.State.Deposit
	msg.State.Out = emit
	msg.State.Count = 1
	c.Assert(k.SetAdvSwapQueueItem(ctx, *msg), IsNil)
	beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
	beforeReserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
	fault := &collectorFailKeeper{Keeper: k, failRead: true}
	mgr.K = fault
	// Native delivery happens before the injected ledger read error.
	c.Assert(settleSwap(ctx, mgr, *msg, "delivery failed"), NotNil)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).IsZero(), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(beforeReserve), Equals, true)
	// The entire input is already in the pool, so the retained single-swap
	// record must not subtract that BTC from solvency a second time.
	c.Assert(msg.IsStreaming(), Equals, false)
	assertCollectorSwapBTCSolvency(c, ctx, mgr, math.ZeroInt())
	fault.failRead = false
	c.Assert(settleSwap(ctx, mgr, *msg, "retry"), IsNil)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	assertCollectorSwapBTCSolvency(c, ctx, mgr, math.ZeroInt())
	c.Assert(settleSwap(ctx, mgr, *msg, "duplicate"), IsNil)
	collector, err := k.GetAffiliateCollector(ctx, "few")
	c.Assert(err, IsNil)
	fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.RuneAsset(), true)
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.Equal(emit.Sub(fee)), Equals, true)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(collector.RuneAmount), Equals, true)
	assertAffiliateIntegrationInvariant(c, ctx, k)
	assertCollectorSwapBTCSolvency(c, ctx, mgr, math.ZeroInt())
}

func (CollectorSwapExecutionSuite) TestPreferredPayoutDeliveryFailurePreservesSolvencyAndRetries(c *C) {
	ctx, mgr, few := setupCollectorSwapExecution(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 1)
	// The BTC remains in custody, but the only vault is unavailable for outbounds.
	compromisedKey := compromisedVaultMimirKeyPrefix + vaults[0].PubKey.String()
	k.SetMimir(ctx, compromisedKey, 1)
	collector := fundNamedCollector(c, ctx, k, few.Name, 10*common.One)
	c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, collector, 1), IsNil)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	queued := affiliateIntegrationQueuedSwaps(c, ctx, k)
	c.Assert(queued, HasLen, 1)
	msg := queued[0]
	c.Assert(msg.IsStreaming(), Equals, false)
	c.Assert(msg.State.In.Equal(msg.State.Deposit), Equals, true)
	c.Assert(msg.State.Out.IsZero(), Equals, false)
	c.Assert(settleSwap(ctx, mgr, msg, "delivery still unavailable"), ErrorMatches, ".*insufficient funds for outbound request.*")
	voter, err := k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 0)
	// Pool output still owed to the partner is not surplus vault BTC.
	assertCollectorSwapBTCSolvency(c, ctx, mgr, math.ZeroInt())

	k.SetMimir(ctx, compromisedKey, 0)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.BTCAsset, false)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, false)
	c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	voter, err = k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 1)
	c.Assert(voter.Actions[0].ToAddress.Equals(few.GetAlias(common.BTCChain)), Equals, true)
	payout := voter.Actions[0]
	c.Assert(payout.Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(payout.Coin.Amount.IsZero(), Equals, false)
	c.Assert(payout.Coin.Amount.Add(fee).Equal(msg.State.Out), Equals, true)
	// Scheduled outbounds reserve MaxGas in addition to the pool and net payout
	// balances. Account for that reservation after retry; retained output above
	// must still report zero before an outbound exists.
	maxGas := payout.MaxGas.ToCoins().GetCoin(common.BTCAsset).Amount
	c.Assert(maxGas.IsZero(), Equals, false)
	expected := math.NewIntFromBigInt(maxGas.BigInt()).Neg()
	assertCollectorSwapBTCSolvency(c, ctx, mgr, expected)

	// Replaying the completed settlement cannot schedule another payout.
	c.Assert(settleSwap(ctx, mgr, msg, "duplicate"), IsNil)
	after, err := k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(after.Actions, DeepEquals, voter.Actions)
	assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
	assertAffiliateIntegrationInvariant(c, ctx, k)
	assertCollectorSwapBTCSolvency(c, ctx, mgr, expected)
}

func (CollectorSwapExecutionSuite) TestPreferredPayoutBelowFeeOrDustFinishes(c *C) {
	for _, scenario := range []string{"fee", "dust", "cleanup-failure"} {
		ctx, mgr, few := setupCollectorSwapExecution(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
		collector := fundNamedCollector(c, ctx, k, few.Name, 10*common.One)
		c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, collector, 1), IsNil)
		queued := affiliateIntegrationQueuedSwaps(c, ctx, k)
		c.Assert(queued, HasLen, 1)
		msg := queued[0]
		// A completed stream can leave both a small output and unspent RUNE.
		// Model the post-swap pool state while keeping real txout/queue handling.
		unspent := cosmos.NewUint(2 * common.One)
		msg.State.In = msg.State.Deposit.Sub(unspent)
		msg.State.Count = msg.State.Quantity
		pool, err := k.GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		originalAsset := pool.BalanceAsset
		pool.BalanceRune = pool.BalanceRune.Add(msg.State.In)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		fee, err := mgr.GasMgr().GetAssetOutboundFee(ctx, common.BTCAsset, false)
		c.Assert(err, IsNil)
		c.Assert(fee.IsZero(), Equals, false)
		msg.State.Out = fee
		if scenario == "dust" {
			msg.State.Out = fee.Add(common.BTCChain.DustThreshold().QuoUint64(2))
		}
		pool.BalanceAsset = pool.BalanceAsset.Sub(msg.State.Out)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
		toi := TxOutItem{Chain: common.BTCChain, InHash: msg.Tx.ID, ToAddress: msg.Destination, Coin: common.NewCoin(common.BTCAsset, msg.State.Out)}
		_, err = mgr.TxOutStore().TryAddTxOutItem(ctx, mgr, toi, cosmos.ZeroUint())
		c.Assert(errors.Is(err, ErrNotEnoughToPayFee), Equals, true, Commentf("%s: %v", scenario, err))
		cleanupKey := refundCleanupKey(toi, "failed_outbound")
		if scenario == "cleanup-failure" {
			mgr.K = collectorCleanupFailKeeper{Keeper: k}
			c.Assert(settleSwap(ctx, mgr, msg, "complete"), NotNil)
			c.Check(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, true)
			c.Check(k.HasRefundCleanup(ctx, cleanupKey), Equals, false)
			assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
			mgr.K = k
		}
		c.Assert(settleSwap(ctx, mgr, msg, "complete"), IsNil)
		c.Check(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
		c.Check(k.HasRefundCleanup(ctx, cleanupKey), Equals, true)
		pool, err = k.GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Check(pool.BalanceAsset.Equal(originalAsset), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, few.Name, unspent.Uint64())
		assertAffiliateIntegrationInvariant(c, ctx, k)
		network := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())
		amounts, err := network.calculateNetworkSolvency(ctx, mgr)
		c.Assert(err, IsNil)
		c.Assert(amounts, HasLen, 1)
		c.Check(amounts[0].Asset.Equals(common.BTCAsset), Equals, true)
		c.Check(amounts[0].Amount.IsZero(), Equals, true)
		c.Assert(settleSwap(ctx, mgr, msg, "duplicate"), IsNil)
		for i := 0; i < 3; i++ {
			ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
			c.Assert(mgr.AdvSwapQueueMgr().EndBlock(ctx, mgr, false), IsNil)
		}
		after, err := k.GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Check(after, DeepEquals, pool)
		assertNamedAffiliateBalance(c, ctx, k, few.Name, unspent.Uint64())
	}
}
