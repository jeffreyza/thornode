package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HelperAffiliateSuite struct{}

var _ = Suite(&HelperAffiliateSuite{})

func (HelperAffiliateSuite) TestSkimAffiliateFees(c *C) {
	ctx, mgr := setupManagerForTest(c)
	affAddr1 := GetRandomTHORAddress()
	affAddr2 := GetRandomTHORAddress()
	tx := GetRandomTx()
	signer, _ := GetRandomTHORAddress().AccAddress()

	// Check affiliate balances before skimming fee
	affAcctAddr1, err := affAddr1.AccAddress()
	c.Assert(err, IsNil)
	acct := mgr.Keeper().GetBalance(ctx, affAcctAddr1)
	c.Assert(acct.AmountOf(common.RuneNative.Native()).String(), Equals, "0")

	affAcctAddr2, err := affAddr2.AccAddress()
	c.Assert(err, IsNil)
	acct2 := mgr.Keeper().GetBalance(ctx, affAcctAddr2)
	c.Assert(acct2.AmountOf(common.RuneNative.Native()).String(), Equals, "0")

	memo := fmt.Sprintf("=:THOR.RUNE:%s::%s/%s:100/50", GetRandomTHORAddress(), affAddr1, affAddr2)
	coin := common.NewCoin(common.RuneNative, cosmos.NewUint(10*common.One))

	feeSkimmed, err := skimAffiliateFees(ctx, mgr, tx, signer, coin, memo)
	c.Assert(err, IsNil)
	c.Assert(feeSkimmed.String(), Equals, "15000000") // 150 basis points of 10 RUNE

	// Check affiliate balances after skimming fee
	acct = mgr.Keeper().GetBalance(ctx, affAcctAddr1)
	c.Assert(acct.AmountOf(common.RuneNative.Native()).String(), Equals, "10000000")
	acct2 = mgr.Keeper().GetBalance(ctx, affAcctAddr2)
	c.Assert(acct2.AmountOf(common.RuneNative.Native()).String(), Equals, "5000000")

	// Use one thorname and one rune address
	tn := types.THORName{Name: "t", Owner: affAcctAddr1, ExpireBlockHeight: 10000000, Aliases: []types.THORNameAlias{{Chain: common.THORChain, Address: affAddr1}}}
	mgr.Keeper().SetTHORName(ctx, tn)
	memo = fmt.Sprintf("=:THOR.RUNE:%s::t/%s:100/50", GetRandomTHORAddress(), affAddr2)

	feeSkimmed, err = skimAffiliateFees(ctx, mgr, tx, signer, coin, memo)
	c.Assert(err, IsNil)
	c.Assert(feeSkimmed.String(), Equals, "15000000")

	// Check affiliate balances after skimming fee
	acct = mgr.Keeper().GetBalance(ctx, affAcctAddr1)
	c.Assert(acct.AmountOf(common.RuneNative.Native()).String(), Equals, "20000000")
	acct2 = mgr.Keeper().GetBalance(ctx, affAcctAddr2)
	c.Assert(acct2.AmountOf(common.RuneNative.Native()).String(), Equals, "10000000")

	// Set a preferred asset, make sure affiliate collector is updated
	tn.PreferredAsset = common.BTCAsset
	mgr.Keeper().SetTHORName(ctx, tn)
	tn, err = mgr.Keeper().GetTHORName(ctx, "t")
	c.Assert(err, IsNil)
	c.Assert(tn.PreferredAsset.String(), Equals, "BTC.BTC")
	c.Assert(mgr.Keeper().THORNameExists(ctx, "t"), Equals, true)
	// Must have BTC alias
	c.Assert(tn.CanReceiveAffiliateFee(), Equals, false)
	tn.Aliases = append(tn.Aliases, types.THORNameAlias{Chain: common.BTCChain, Address: GetRandomBTCAddress()})
	mgr.Keeper().SetTHORName(ctx, tn)
	c.Assert(mgr.Keeper().THORNameExists(ctx, "t"), Equals, true)
	c.Assert(tn.CanReceiveAffiliateFee(), Equals, true)

	feeSkimmed, err = skimAffiliateFees(ctx, mgr, tx, signer, coin, memo)
	c.Assert(err, IsNil)
	c.Assert(feeSkimmed.String(), Equals, "15000000")

	// Check affiliate balances after skimming fee, affAcctAddr1's balance should be same
	// as before + affiliate collector module updated
	acct = mgr.Keeper().GetBalance(ctx, affAcctAddr1)
	c.Assert(acct.AmountOf(common.RuneNative.Native()).String(), Equals, "20000000")

	// ac, err := mgr.Keeper().GetAffiliateCollector(ctx, affAcctAddr1)
	// c.Assert(err, IsNil)
	// c.Assert(ac.RuneAmount.String(), Equals, "10000000")

	// affAcctAddr2's balance should be updated as normal
	acct2 = mgr.Keeper().GetBalance(ctx, affAcctAddr2)
	c.Assert(acct2.AmountOf(common.RuneNative.Native()).String(), Equals, "15000000")
}

// fixes: https://gitlab.com/thorchain/thornode/-/issues/2238
func (HelperAffiliateSuite) TestEnsureAffiliateFromAddressSecuredAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// External address should be rewritten for secured assets
	externalAddr := common.Address("0xe140683571184092a252116918abd7c6f18451a1")
	securedAsset, err := common.NewAsset("ETH-BTC")
	c.Assert(err, IsNil)
	c.Assert(securedAsset.IsSecuredAsset(), Equals, true)

	tx := common.NewTx(
		GetRandomTxHash(),
		externalAddr,
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(securedAsset, cosmos.NewUint(100*common.One))),
		common.Gas{},
		"",
	)

	var asgard common.Address
	asgard, err = mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)

	c.Assert(ensureAffiliateFromAddress(ctx, mgr, &tx), IsNil)
	c.Assert(tx.FromAddress.String(), Equals, asgard.String())
}

func (HelperAffiliateSuite) TestEnsureAffiliateFromAddressTradeAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)

	externalAddr := common.Address("0xe140683571184092a252116918abd7c6f18451a1")
	tradeAsset, err := common.NewAsset("ETH~BTC")
	c.Assert(err, IsNil)
	c.Assert(tradeAsset.IsTradeAsset(), Equals, true)

	tx := common.NewTx(
		GetRandomTxHash(),
		externalAddr,
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(tradeAsset, cosmos.NewUint(100*common.One))),
		common.Gas{},
		"",
	)

	var asgard common.Address
	asgard, err = mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)

	c.Assert(ensureAffiliateFromAddress(ctx, mgr, &tx), IsNil)
	c.Assert(tx.FromAddress.String(), Equals, asgard.String())
}

func (HelperAffiliateSuite) TestEnsureAffiliateFromAddressPreservesTHORChainAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Get Asgard address (a valid THORChain address)
	asgardAddr, err := mgr.Keeper().GetModuleAddress(AsgardName)
	c.Assert(err, IsNil)

	securedAsset, err := common.NewAsset("ETH-BTC")
	c.Assert(err, IsNil)
	c.Assert(securedAsset.IsSecuredAsset(), Equals, true)

	// Create a transaction with a THORChain address as FromAddress
	tx := common.NewTx(
		GetRandomTxHash(),
		asgardAddr, // Already a valid THORChain address
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(securedAsset, cosmos.NewUint(100*common.One))),
		common.Gas{},
		"",
	)

	originalAddr := tx.FromAddress.String()

	// Call ensureAffiliateFromAddress - it should NOT change the address
	c.Assert(ensureAffiliateFromAddress(ctx, mgr, &tx), IsNil)

	// Verify the address was preserved (not changed)
	c.Assert(tx.FromAddress.String(), Equals, originalAddr)
	c.Assert(tx.FromAddress.String(), Equals, asgardAddr.String())
}

func (HelperAffiliateSuite) TestEnsureAffiliateFromAddressNoOpForRegularAssets(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// External address with a regular (non-secured, non-trade) asset
	externalAddr := common.Address("0xe140683571184092a252116918abd7c6f18451a1")
	regularAsset := common.ETHAsset // Regular layer 1 asset

	tx := common.NewTx(
		GetRandomTxHash(),
		externalAddr,
		GetRandomTHORAddress(),
		common.NewCoins(common.NewCoin(regularAsset, cosmos.NewUint(100*common.One))),
		common.Gas{},
		"",
	)

	originalAddr := tx.FromAddress.String()

	// Call ensureAffiliateFromAddress - should be a no-op for regular assets
	c.Assert(ensureAffiliateFromAddress(ctx, mgr, &tx), IsNil)

	// Verify the address was NOT changed (even though it's an external address)
	c.Assert(tx.FromAddress.String(), Equals, originalAddr)
	c.Assert(tx.FromAddress.String(), Equals, externalAddr.String())
}

func (HelperAffiliateSuite) TestGetEffectiveMultiplier(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Global default is 100 (from constants_mocknet.go)
	tn := types.THORName{Name: "test"}

	// THORName multiplier 0 (unset) → use global default (100)
	tn.PreferredAssetOutboundFeeMultiplier = 0
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(100))

	// THORName multiplier >= 1 → use custom
	tn.PreferredAssetOutboundFeeMultiplier = 500
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(500))

	// THORName multiplier exactly 1 → use custom (boundary)
	tn.PreferredAssetOutboundFeeMultiplier = 1
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(1))

	// THORName multiplier 10000 → use custom (upper boundary)
	tn.PreferredAssetOutboundFeeMultiplier = 10000
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(10000))

	// Negative mimir falls back to constant default (100) via GetConfigInt64
	mgr.Keeper().SetMimir(ctx, "PreferredAssetOutboundFeeMultiplier", -5)
	tn.PreferredAssetOutboundFeeMultiplier = 0 // fall through to global
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(100))

	// Zero mimir → GetConfigInt64 returns 0 → clamp to 1
	mgr.Keeper().SetMimir(ctx, "PreferredAssetOutboundFeeMultiplier", 0)
	tn.PreferredAssetOutboundFeeMultiplier = 0
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(1))

	// Custom multiplier overrides even when global config is zero
	mgr.Keeper().SetMimir(ctx, "PreferredAssetOutboundFeeMultiplier", 0)
	tn.PreferredAssetOutboundFeeMultiplier = 200
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(200))

	// Mimir override with positive value
	mgr.Keeper().SetMimir(ctx, "PreferredAssetOutboundFeeMultiplier", 50)
	tn.PreferredAssetOutboundFeeMultiplier = 0
	c.Check(getEffectiveMultiplier(ctx, mgr, tn), Equals, int64(50))
}

type AffiliateCollectorQueueSuite struct{}

var _ = Suite(&AffiliateCollectorQueueSuite{})

type collectorFailKeeper struct {
	keeper.Keeper
	failQueue    bool
	failTransfer bool
	failRead     bool
	failWrite    bool
}

type collectorOutputStore struct {
	TxOutStore
	outputs []TxOutItem
	reject  bool
}

func (s *collectorOutputStore) TryAddTxOutItem(_ cosmos.Context, _ Manager, item TxOutItem, _ cosmos.Uint) (bool, error) {
	if s.reject {
		return false, nil
	}
	s.outputs = append(s.outputs, item)
	return true, nil
}

func (k *collectorFailKeeper) SetSwapQueueItem(ctx cosmos.Context, msg MsgSwap, index int) error {
	if err := k.Keeper.SetSwapQueueItem(ctx, msg, index); err != nil {
		return err
	}
	if k.failQueue {
		return fmt.Errorf("injected queue failure after write")
	}
	return nil
}

func (k *collectorFailKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	if k.failTransfer {
		return fmt.Errorf("injected module transfer failure")
	}
	return k.Keeper.SendFromModuleToModule(ctx, from, to, coins)
}

func (k *collectorFailKeeper) GetAffiliateCollector(ctx cosmos.Context, name string) (AffiliateFeeCollector, error) {
	collector, err := k.Keeper.GetAffiliateCollector(ctx, name)
	if k.failRead {
		return collector, fmt.Errorf("injected collector read failure")
	}
	return collector, err
}

func (k *collectorFailKeeper) SetAffiliateCollector(ctx cosmos.Context, collector AffiliateFeeCollector) error {
	if err := k.Keeper.SetAffiliateCollector(ctx, collector); err != nil {
		return err
	}
	if k.failWrite {
		return fmt.Errorf("injected collector write failure")
	}
	return nil
}

func fundNamedCollector(c *C, ctx cosmos.Context, k keeper.Keeper, name string, amount uint64) AffiliateFeeCollector {
	FundModule(c, ctx, k, AffiliateCollectorName, amount)
	col := AffiliateFeeCollector{Name: name, RuneAmount: cosmos.NewUint(amount)}
	c.Assert(k.SetAffiliateCollector(ctx, col), IsNil)
	return col
}

func (AffiliateCollectorQueueSuite) TestPayoutCreationRollsBackVoterQueueAndFunds(c *C) {
	for _, failure := range []string{"queue", "transfer", "write"} {
		ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
		col := fundNamedCollector(c, ctx, k, "few", 1000)
		beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
		fault := &collectorFailKeeper{Keeper: k, failQueue: failure == "queue", failTransfer: failure == "transfer", failWrite: failure == "write"}
		mgr.K = fault
		c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 3), NotNil)
		assertNamedAffiliateBalance(c, ctx, k, "few", 1000)
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "1000")
		c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
		c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 0)
		// The same hash can be retried only if the failed voter write rolled back.
		fault.failQueue, fault.failTransfer, fault.failWrite = false, false, false
		c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 3), IsNil)
		c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 1)
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}

func (AffiliateCollectorQueueSuite) TestCollectorCreditRejectsInvalidStateWithoutMovingFunds(c *C) {
	for _, failure := range []string{"empty-thorname", "write"} {
		ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		fundNamedCollector(c, ctx, k, "few", 20)
		beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
		if failure == "empty-thorname" {
			few.Name = ""
		}
		fault := &collectorFailKeeper{Keeper: k, failWrite: failure == "write"}
		mgr.K = fault
		index := 0
		coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(10))
		c.Assert(addRuneToAffiliateCollector(ctx, mgr, coin, &few, &index), NotNil)
		c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, "few", 20)
		assertAffiliateIntegrationInvariant(c, ctx, k)
		collectors, err := k.GetAffiliateCollectors(ctx)
		c.Assert(err, IsNil)
		c.Assert(collectors, HasLen, 1)
		c.Assert(collectors[0].Name, Equals, "few")

		fault.failWrite = false
		few.Name = "few"
		c.Assert(addRuneToAffiliateCollector(ctx, mgr, coin, &few, &index), IsNil)
		assertNamedAffiliateBalance(c, ctx, k, "few", 30)
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}

func (AffiliateCollectorQueueSuite) TestAdvancedRestorationWriteFailureRetries(c *C) {
	ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	col := fundNamedCollector(c, ctx, k, "few", 1000)
	c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 3), IsNil)
	queued := affiliateIntegrationQueuedSwaps(c, ctx, k)
	c.Assert(queued, HasLen, 1)
	msg := queued[0]
	beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
	fault := &collectorFailKeeper{Keeper: k, failWrite: true}
	mgr.K = fault
	c.Assert(settleSwap(ctx, mgr, msg, "failed"), ErrorMatches, ".*injected collector write failure.*")
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
	assertNamedAffiliateBalance(c, ctx, k, "few", 0)
	assertAffiliateIntegrationInvariant(c, ctx, k)
	fault.failWrite = false
	c.Assert(settleSwap(ctx, mgr, msg, "retry"), IsNil)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
	assertNamedAffiliateBalance(c, ctx, k, "few", 1000)
	assertAffiliateIntegrationInvariant(c, ctx, k)
}

func (AffiliateCollectorQueueSuite) TestLegacyQueueFailureRestoresOnceAndRetainsFailedSettlement(c *C) {
	for _, name := range []string{"few", "THORSWAP.FI"} {
		ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		few.Name = name
		k.SetTHORName(ctx, few)
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
		col := fundNamedCollector(c, ctx, k, name, 1000)
		fundNamedCollector(c, ctx, k, "tan", 200)
		beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
		beforeReserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
		c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 3), IsNil)
		// The THORName may change owner and expire while the payout is queued.
		few.Owner = GetRandomBech32Addr()
		few.ExpireBlockHeight = ctx.BlockHeight() - 1
		k.SetTHORName(ctx, few)
		fault := &collectorFailKeeper{Keeper: k, failTransfer: true}
		mgr.K = fault
		queue := newSwapQueue(fault)
		queue.handler = func(Manager) cosmos.Handler {
			return func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
				return nil, fmt.Errorf("injected swap failure")
			}
		}
		c.Assert(queue.EndBlock(ctx, mgr), IsNil)
		c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 1)
		assertNamedAffiliateBalance(c, ctx, k, name, 0)
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "200")
		fault.failTransfer = false
		// A fresh queue manager retries from the persisted record.
		restarted := newSwapQueue(fault)
		restarted.handler = queue.handler
		c.Assert(restarted.EndBlock(ctx, mgr), IsNil)
		c.Assert(restarted.EndBlock(ctx, mgr), IsNil)
		c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), HasLen, 0)
		assertNamedAffiliateBalance(c, ctx, k, name, 1000)
		assertNamedAffiliateBalance(c, ctx, k, "tan", 200)
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "1200")
		c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
		c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(beforeReserve), Equals, true)
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}

func (AffiliateCollectorQueueSuite) TestAdvancedRestorationConservesRUNEAndRetries(c *C) {
	for _, name := range []string{"few", "THORSWAP.FI"} {
		ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
		k := mgr.Keeper()
		few.Name = name
		k.SetTHORName(ctx, few)
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
		col := fundNamedCollector(c, ctx, k, name, 1000)
		beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
		beforeReserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
		c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 3), IsNil)
		queued := affiliateIntegrationQueuedSwaps(c, ctx, k)
		c.Assert(queued, HasLen, 1)
		msg := queued[0]
		fault := &collectorFailKeeper{Keeper: k, failTransfer: true}
		mgr.K = fault
		c.Assert(settleSwap(ctx, mgr, msg, "failed"), NotNil)
		c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, name, 0)
		fault.failTransfer = false
		c.Assert(settleSwap(ctx, mgr, msg, "failed"), IsNil)
		c.Assert(settleSwap(ctx, mgr, msg, "duplicate stale settlement"), IsNil)
		c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, false)
		assertNamedAffiliateBalance(c, ctx, k, name, 1000)
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).String(), Equals, "1000")
		c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
		c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(beforeReserve), Equals, true)
		// The fixture gas manager charges a positive native outbound fee. Internal
		// restoration must still return the entire principal without consulting it.
		assertAffiliateIntegrationInvariant(c, ctx, k)
	}
}

func (AffiliateCollectorQueueSuite) TestPartialPayoutRestoresOnlyUnspentRUNE(c *C) {
	ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
	col := fundNamedCollector(c, ctx, k, "few", 1000)
	c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, col, 3), IsNil)
	msg := affiliateIntegrationQueuedSwaps(c, ctx, k)[0]
	msg.State.In = cosmos.NewUint(300)
	msg.State.Out = cosmos.NewUint(200)
	c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
	// New earnings must survive restoration, and the already executed output
	// keeps the saved destination even if the THORName has a different alias now.
	fundNamedCollector(c, ctx, k, "few", 17)
	few.Owner = GetRandomBech32Addr()
	few.SetAlias(common.BTCChain, GetRandomBTCAddress())
	k.SetTHORName(ctx, few)
	outputs := &collectorOutputStore{TxOutStore: mgr.TxOutStore(), reject: true}
	mgr.txOutStore = outputs
	before := k.GetRuneBalanceOfModule(ctx, AsgardName)
	c.Assert(settleSwap(ctx, mgr, msg, "outbound not accepted"), NotNil)
	assertNamedAffiliateBalance(c, ctx, k, "few", 17)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(before), Equals, true)
	c.Assert(k.HasAdvSwapQueueItem(ctx, msg.Tx.ID, int(msg.Index)), Equals, true)
	outputs.reject = false
	c.Assert(settleSwap(ctx, mgr, msg, "partially executed"), IsNil)
	c.Assert(outputs.outputs, HasLen, 1)
	c.Assert(outputs.outputs[0].Coin.Amount.String(), Equals, "200")
	c.Assert(outputs.outputs[0].ToAddress.Equals(msg.Destination), Equals, true)
	c.Assert(outputs.outputs[0].ToAddress.Equals(few.GetAlias(common.BTCChain)), Equals, false)
	assertNamedAffiliateBalance(c, ctx, k, "few", 717)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(before.SubUint64(700)), Equals, true)
	assertAffiliateIntegrationInvariant(c, ctx, k)
}
