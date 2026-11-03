package thorchain

import (
	"fmt"
	"math"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerManageTHORNameSuite struct{}

var _ = Suite(&HandlerManageTHORNameSuite{})

func (s *HandlerManageTHORNameSuite) TestValidator(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewManageTHORNameHandler(mgr)
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
	addr := GetRandomTHORAddress()
	acc, _ := addr.AccAddress()
	name := NewTHORName("hello", 50, []THORNameAlias{{Chain: common.THORChain, Address: addr}})
	mgr.Keeper().SetTHORName(ctx, name)

	// set pool for preferred asset
	pool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.Asset = common.ETHAsset
	err = mgr.Keeper().SetPool(ctx, pool)
	c.Assert(err, IsNil)

	// happy path
	msg := NewMsgManageTHORName("I-am_the_99th_walrus+", common.THORChain, addr, coin, 0, common.ETHAsset, acc, acc, 0)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// happy path: RUNE as preferred asset (sentinel for clearing) should pass validation
	msg.PreferredAsset = common.RuneAsset()
	c.Assert(handler.validate(ctx, *msg), IsNil)
	msg.PreferredAsset = common.ETHAsset // restore

	// fail: address is wrong chain
	msg.Chain = common.ETHChain
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// fail: address is wrong network
	mainnetBTCAddr, err := common.NewAddress("bc1qy0tj9fh0u6fgz0mejjp6776z6kugych0zwrkwr")
	c.Assert(err, IsNil)
	msg.Address = mainnetBTCAddr
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// restore to happy path
	msg.Chain = common.THORChain
	msg.Address = addr

	// fail: name is too long
	msg.Name = "this_name_is_way_too_long_to_be_a_valid_name"
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// fail: bad characters
	msg.Name = "i am the walrus"
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// fail: bad attempt to inflate expire block height
	msg.Name = "hello"
	msg.ExpireBlockHeight = 100
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// fail: bad auth
	msg.ExpireBlockHeight = 0
	msg.Signer = GetRandomBech32Addr()
	c.Assert(handler.validate(ctx, *msg), NotNil)

	// fail: not enough funds for new THORName
	msg.Name = "bang"
	msg.Coin.Amount = cosmos.ZeroUint()
	c.Assert(handler.validate(ctx, *msg), NotNil)
}

func (s *HandlerManageTHORNameSuite) TestHandler(c *C) {
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	feePerBlock := constAccessor.GetInt64Value(constants.TNSFeePerBlock)
	registrationFee := constAccessor.GetInt64Value(constants.TNSRegisterFee)
	ctx, mgr := setupManagerForTest(c)

	blocksPerYear := mgr.GetConstants().GetInt64Value(constants.BlocksPerYear)
	handler := NewManageTHORNameHandler(mgr)
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
	addr := GetRandomTHORAddress()
	acc, _ := addr.AccAddress()
	tnName := "hello"

	// add rune to addr for gas
	FundAccount(c, ctx, mgr.Keeper(), acc, 10*common.One)

	// happy path, register new name (RUNE as preferred asset should not set it)
	msg := NewMsgManageTHORName(tnName, common.THORChain, addr, coin, 0, common.RuneAsset(), acc, acc, 0)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err := mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.Owner.Empty(), Equals, false)
	c.Check(name.PreferredAsset.IsEmpty(), Equals, true) // RUNE sentinel clears preferred asset
	c.Check(name.ExpireBlockHeight, Equals, ctx.BlockHeight()+blocksPerYear+(int64(coin.Amount.Uint64())-registrationFee)/feePerBlock)

	// happy path, set alt chain address
	ethAddr := GetRandomETHAddress()
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.RuneAsset(), acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.GetAlias(common.ETHChain).Equals(ethAddr), Equals, true)

	// happy path, update alt chain address
	ethAddr = GetRandomETHAddress()
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.RuneAsset(), acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.GetAlias(common.ETHChain).Equals(ethAddr), Equals, true)

	// update preferred asset
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.ETHAsset, acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAsset, Equals, common.ETHAsset)

	// alias-only update should not clear the preferred asset
	ethAddr = GetRandomETHAddress()
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()), 0, common.EmptyAsset, acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.GetAlias(common.ETHChain).Equals(ethAddr), Equals, true)
	c.Check(name.PreferredAsset, Equals, common.ETHAsset)

	// clear preferred asset using RUNE sentinel
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.RuneAsset(), acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAsset.IsEmpty(), Equals, true) // RUNE sentinel clears preferred asset

	// set preferred asset again for the transfer test
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.ETHAsset, acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAsset, Equals, common.ETHAsset)

	// test custom multiplier
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.ETHAsset, acc, acc, 500)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAssetOutboundFeeMultiplier, Equals, int64(500))

	// test -1 sentinel: multiplier should remain unchanged
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.ETHAsset, acc, acc, -1)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAssetOutboundFeeMultiplier, Equals, int64(500))

	// test explicit 0: multiplier should reset to global default
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.ETHAsset, acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAssetOutboundFeeMultiplier, Equals, int64(0))

	// set multiplier to non-zero before transfer to verify the transfer actually resets it
	msg = NewMsgManageTHORName(tnName, common.ETHChain, ethAddr, coin, 0, common.ETHAsset, acc, acc, 300)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.PreferredAssetOutboundFeeMultiplier, Equals, int64(300))

	// transfer thorname to new owner, should reset preferred asset/aliases/multiplier
	addr2 := GetRandomTHORAddress()
	acc2, _ := addr2.AccAddress()
	msg = NewMsgManageTHORName(tnName, common.THORChain, addr, coin, 0, common.RuneAsset(), acc2, acc, -1)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(len(name.GetAliases()), Equals, 0) // aliases cleared on ownership transfer
	c.Check(name.PreferredAsset.IsEmpty(), Equals, true)
	c.Check(name.PreferredAssetOutboundFeeMultiplier, Equals, int64(0)) // reset despite -1 sentinel
	c.Check(name.GetOwner().Equals(acc2), Equals, true)

	// happy path, release thorname back into the wild
	msg = NewMsgManageTHORName(tnName, common.THORChain, addr, common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()), 1, common.RuneAsset(), acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	name, err = mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)
	c.Check(name.Owner.Empty(), Equals, true)
	c.Check(name.ExpireBlockHeight, Equals, int64(0))
}

func (s *HandlerManageTHORNameSuite) TestHandlerIntegerOverflow(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewManageTHORNameHandler(mgr)
	addr := GetRandomTHORAddress()
	acc, _ := addr.AccAddress()

	// add rune to addr for gas
	FundAccount(c, ctx, mgr.Keeper(), acc, 10*common.One)

	// Test coin amount exceeding int64 max
	// Create a coin with an amount larger than MaxInt64
	hugeAmount := cosmos.NewUint(math.MaxUint64)
	hugeCoin := common.NewCoin(common.RuneAsset(), hugeAmount)
	msg := NewMsgManageTHORName("overflow-test", common.THORChain, addr, hugeCoin, 0, common.RuneAsset(), acc, acc, 0)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*coin amount exceeds maximum allowed value.*")
}

func (s *HandlerManageTHORNameSuite) TestHandlerBlockHeightOverflow(c *C) {
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	feePerBlock := constAccessor.GetInt64Value(constants.TNSFeePerBlock)
	ctx, mgr := setupManagerForTest(c)

	handler := NewManageTHORNameHandler(mgr)
	addr := GetRandomTHORAddress()
	acc, _ := addr.AccAddress()
	tnName := "overflow-expire"

	// add rune to addr for gas
	FundAccount(c, ctx, mgr.Keeper(), acc, 10*common.One)

	// First, register a THORName
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
	msg := NewMsgManageTHORName(tnName, common.THORChain, addr, coin, 0, common.RuneAsset(), acc, acc, 0)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Now create a THORName with very high expire block height and try to extend it
	// This should trigger the overflow check when trying to add more blocks
	name, err := mgr.Keeper().GetTHORName(ctx, tnName)
	c.Assert(err, IsNil)

	// Set expire block height to near MaxInt64 to trigger overflow when adding blocks
	name.ExpireBlockHeight = math.MaxInt64 - 100
	mgr.Keeper().SetTHORName(ctx, name)

	// Try to extend with a large amount that would overflow
	// Calculate the amount needed to generate enough blocks to overflow
	// addBlocks = amount / feePerBlock, so we need amount > 100 * feePerBlock to overflow
	overflowAmount := cosmos.NewUint(uint64(200 * feePerBlock))
	overflowCoin := common.NewCoin(common.RuneAsset(), overflowAmount)
	msg = NewMsgManageTHORName(tnName, common.THORChain, addr, overflowCoin, 0, common.RuneAsset(), acc, acc, 0)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*block calculation overflow.*")
}

type thornameSettlementFailKeeper struct {
	keeper.Keeper
}

func (k thornameSettlementFailKeeper) SendFromModuleToAccount(ctx cosmos.Context, from string, owner cosmos.AccAddress, coins common.Coins) error {
	if err := k.Keeper.SendFromModuleToAccount(ctx, from, owner, coins); err != nil {
		return err
	}
	return fmt.Errorf("injected failure after bank transfer")
}

func (s *HandlerManageTHORNameSuite) TestNamedCollectorTransferIsolation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	owner, next := GetRandomBech32Addr(), GetRandomBech32Addr()
	for i, name := range []string{"few", "tan"} {
		tn := NewTHORName(name, 1000, nil)
		tn.Owner = owner
		k.SetTHORName(ctx, tn)
		c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: name, RuneAmount: cosmos.NewUint(uint64(200 - i*100))}), IsNil)
	}
	FundModule(c, ctx, k, AffiliateCollectorName, 300)
	handler := NewInternalHandler(mgr)
	msg := NewMsgManageTHORName("few", common.THORChain, GetRandomTHORAddress(),
		common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()), 0, common.EmptyAsset, owner, owner, -1)
	// Updating an alias without changing owner leaves the collector untouched.
	_, err := handler(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(k.GetBalance(ctx, owner).AmountOf(common.RuneAsset().Native()).IsZero(), Equals, true)

	msg.Owner = next
	_, err = handler(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(k.GetBalance(ctx, owner).AmountOf(common.RuneAsset().Native()).Uint64(), Equals, uint64(200))
	c.Assert(k.GetBalance(ctx, next).AmountOf(common.RuneAsset().Native()).IsZero(), Equals, true)
	few, err := k.GetTHORName(ctx, "few")
	c.Assert(err, IsNil)
	c.Assert(few.Owner.Equals(next), Equals, true)
	collector, err := k.GetAffiliateCollector(ctx, "few")
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.IsZero(), Equals, true)
	collector, err = k.GetAffiliateCollector(ctx, "tan")
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.Uint64(), Equals, uint64(100))
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Uint64(), Equals, uint64(100))
}

func (s *HandlerManageTHORNameSuite) TestNamedCollectorTransferRollback(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	owner, next := GetRandomBech32Addr(), GetRandomBech32Addr()
	tn := NewTHORName("few", 1000, nil)
	tn.Owner = owner
	k.SetTHORName(ctx, tn)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.NewUint(200)}), IsNil)
	FundModule(c, ctx, k, AffiliateCollectorName, 200)
	handler := NewInternalHandler(mgr)
	msg := NewMsgManageTHORName("few", common.THORChain, GetRandomTHORAddress(),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(math.MaxUint64)), 0, common.EmptyAsset, next, owner, -1)
	eventsBefore := len(ctx.EventManager().Events())
	// The payout succeeds before an expiry calculation fails. Both changes,
	// including the settlement event, must roll back.
	_, err := handler(ctx, msg)
	c.Assert(err, ErrorMatches, ".*coin amount exceeds.*")
	assertUnchanged := func() {
		after, recordErr := k.GetTHORNameRecord(ctx, "few")
		c.Assert(recordErr, IsNil)
		c.Assert(after, DeepEquals, tn)
		c.Assert(k.GetBalance(ctx, owner).AmountOf(common.RuneAsset().Native()).IsZero(), Equals, true)
		collector, collectorErr := k.GetAffiliateCollector(ctx, "few")
		c.Assert(collectorErr, IsNil)
		c.Assert(collector.RuneAmount.Uint64(), Equals, uint64(200))
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Uint64(), Equals, uint64(200))
		c.Assert(len(ctx.EventManager().Events()), Equals, eventsBefore)
	}
	assertUnchanged()

	msg.Coin.Amount = cosmos.ZeroUint()
	mgr.K = thornameSettlementFailKeeper{Keeper: k}
	_, err = handler(ctx, msg)
	c.Assert(err, ErrorMatches, ".*injected failure after bank transfer")
	assertUnchanged()
	mgr.K = &collectorFailKeeper{Keeper: k, failWrite: true}
	_, err = handler(ctx, msg)
	c.Assert(err, ErrorMatches, ".*injected collector write failure")
	assertUnchanged()

	// A retry against the unchanged state pays exactly once.
	mgr.K = k
	_, err = handler(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(k.GetBalance(ctx, owner).AmountOf(common.RuneAsset().Native()).Uint64(), Equals, uint64(200))
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).IsZero(), Equals, true)
}

func (s *HandlerManageTHORNameSuite) TestHistoricalNameRegistrationRulesUnchanged(c *C) {
	for _, name := range []string{"thorswap.fi", strings.Repeat("a", 31)} {
		for _, expired := range []bool{false, true} {
			ctx, mgr := setupManagerForTest(c)
			k := mgr.Keeper()
			owner, next := GetRandomBech32Addr(), GetRandomBech32Addr()
			msg := NewMsgManageTHORName(name, common.THORChain, GetRandomTHORAddress(),
				common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)), 0, common.EmptyAsset, next, next, -1)
			handler := NewInternalHandler(mgr)
			// The same spelling remains forbidden for a never-registered THORName.
			_, err := handler(ctx, msg)
			c.Assert(err, NotNil)
			stored, err := k.GetTHORNameRecord(ctx, name)
			c.Assert(err, IsNil)
			c.Assert(stored.Owner.Empty(), Equals, true)

			tn := NewTHORName(name, ctx.BlockHeight()+1000, nil)
			tn.Owner = owner
			if expired {
				tn.ExpireBlockHeight = ctx.BlockHeight() - 1
			}
			k.SetTHORName(ctx, tn)
			fundNamedCollector(c, ctx, k, name, 200)
			ownerBefore := k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount
			nextBefore := k.GetBalanceOf(ctx, next, common.RuneAsset()).Amount
			events := ctx.EventManager().Events()
			msg.Signer = owner
			// Keeping historical state does not relax renewal or re-registration
			// rules. A rejected transfer with a top-up must roll back settlement too.
			for _, recipient := range []cosmos.AccAddress{owner, next} {
				msg.Owner = recipient
				_, err = handler(ctx, msg)
				c.Assert(err, ErrorMatches, ".*(invalid THORName|THORName cannot exceed 30 characters).*")
				stored, err = k.GetTHORNameRecord(ctx, name)
				c.Assert(err, IsNil)
				c.Assert(stored, DeepEquals, tn)
				assertNamedAffiliateBalance(c, ctx, k, name, 200)
				c.Assert(k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount.Equal(ownerBefore), Equals, true)
				c.Assert(k.GetBalanceOf(ctx, next, common.RuneAsset()).Amount.Equal(nextBefore), Equals, true)
				c.Assert(ctx.EventManager().Events(), DeepEquals, events)
				assertAffiliateIntegrationInvariant(c, ctx, k)
			}
			if expired {
				continue
			}
			// Active THORNames can still transfer without a top-up under the old rules.
			msg.Coin.Amount = cosmos.ZeroUint()
			_, err = handler(ctx, msg)
			c.Assert(err, IsNil)
			stored, err = k.GetTHORName(ctx, name)
			c.Assert(err, IsNil)
			c.Assert(stored.Owner.Equals(next), Equals, true)
			c.Assert(stored.ExpireBlockHeight, Equals, tn.ExpireBlockHeight)
			c.Assert(k.GetBalanceOf(ctx, owner, common.RuneAsset()).Amount.String(), Equals, "200")
			assertNamedAffiliateBalance(c, ctx, k, name, 0)
			assertAffiliateIntegrationInvariant(c, ctx, k)
		}
	}
}

func (s *HandlerManageTHORNameSuite) TestNamedCollectorExpiredRegistration(c *C) {
	for _, sameOwner := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		owner, next := GetRandomBech32Addr(), GetRandomBech32Addr()
		if sameOwner {
			next = owner
		}
		tn := NewTHORName("few", ctx.BlockHeight()-1, nil)
		tn.Owner = owner
		k.SetTHORName(ctx, tn)
		c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.NewUint(200)}), IsNil)
		FundModule(c, ctx, k, AffiliateCollectorName, 200)
		expired, err := k.GetTHORName(ctx, "few")
		c.Assert(err, IsNil)
		c.Assert(expired.Owner.Empty(), Equals, true)
		msg := NewMsgManageTHORName("few", common.THORChain, GetRandomTHORAddress(),
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)), 0, common.EmptyAsset, next, next, -1)
		_, err = NewInternalHandler(mgr)(ctx, msg)
		c.Assert(err, IsNil)
		c.Assert(k.GetBalance(ctx, owner).AmountOf(common.RuneAsset().Native()).Uint64(), Equals, uint64(200))
		after, err := k.GetTHORName(ctx, "few")
		c.Assert(err, IsNil)
		c.Assert(after.Owner.Equals(next), Equals, true)
		collector, err := k.GetAffiliateCollector(ctx, "few")
		c.Assert(err, IsNil)
		c.Assert(collector.RuneAmount.IsZero(), Equals, true)
		c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).IsZero(), Equals, true)
	}
}

func (s *HandlerManageTHORNameSuite) TestNamedCollectorRejectsModuleSettlement(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	owner := k.GetModuleAccAddress(AsgardName)
	tn := NewTHORName("few", ctx.BlockHeight()-1, nil)
	tn.Owner = owner
	k.SetTHORName(ctx, tn)
	c.Assert(k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: "few", RuneAmount: cosmos.NewUint(200)}), IsNil)
	FundModule(c, ctx, k, AffiliateCollectorName, 200)
	beforeAsgard := k.GetRuneBalanceOfModule(ctx, AsgardName)
	next := GetRandomBech32Addr()
	msg := NewMsgManageTHORName("few", common.THORChain, GetRandomTHORAddress(),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)), 0, common.EmptyAsset, next, next, -1)
	_, err := NewInternalHandler(mgr)(ctx, msg)
	c.Assert(err, ErrorMatches, ".*owner cannot be a module address")
	after, err := k.GetTHORNameRecord(ctx, "few")
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, tn)
	collector, err := k.GetAffiliateCollector(ctx, "few")
	c.Assert(err, IsNil)
	c.Assert(collector.RuneAmount.Uint64(), Equals, uint64(200))
	c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Uint64(), Equals, uint64(200))
	c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(beforeAsgard), Equals, true)
}

func (s *HandlerManageTHORNameSuite) TestRejectsModuleOwnerRegistration(c *C) {
	for _, expired := range []bool{false, true} {
		for _, implicit := range []bool{false, true} {
			ctx, mgr := setupManagerForTest(c)
			k := mgr.Keeper()
			previousOwner, next := GetRandomBech32Addr(), GetRandomBech32Addr()
			if expired {
				tn := NewTHORName("few", ctx.BlockHeight()-1, nil)
				tn.Owner = previousOwner
				k.SetTHORName(ctx, tn)
				fundNamedCollector(c, ctx, k, "few", 200)
			}
			before, err := k.GetTHORNameRecord(ctx, "few")
			c.Assert(err, IsNil)
			collectorBefore, err := k.GetAffiliateCollector(ctx, "few")
			c.Assert(err, IsNil)
			balanceBefore := k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName)
			eventsBefore := len(ctx.EventManager().Events())
			module := k.GetModuleAccAddress(AsgardName)
			msg := NewMsgManageTHORName("few", common.THORChain, GetRandomTHORAddress(),
				common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)), 0, common.EmptyAsset, module, next, -1)
			if implicit {
				msg.Owner = nil
				msg.Signer = module
			}
			// Rejection must leave the previous owner's THORName and balance untouched.
			_, err = NewInternalHandler(mgr)(ctx, msg)
			c.Assert(err, ErrorMatches, "invalid THORName owner: .*module address")
			after, err := k.GetTHORNameRecord(ctx, "few")
			c.Assert(err, IsNil)
			c.Assert(after, DeepEquals, before)
			collectorAfter, err := k.GetAffiliateCollector(ctx, "few")
			c.Assert(err, IsNil)
			c.Assert(collectorAfter, DeepEquals, collectorBefore)
			c.Assert(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(balanceBefore), Equals, true)
			c.Assert(k.GetBalanceOf(ctx, previousOwner, common.RuneAsset()).Amount.IsZero(), Equals, true)
			c.Assert(len(ctx.EventManager().Events()), Equals, eventsBefore)

			// A normal owner works for both explicit and signer-default registration.
			msg.Signer = next
			if !implicit {
				msg.Owner = next
			}
			_, err = NewInternalHandler(mgr)(ctx, msg)
			c.Assert(err, IsNil)
			after, err = k.GetTHORName(ctx, "few")
			c.Assert(err, IsNil)
			c.Assert(after.Owner.Equals(next), Equals, true)
			c.Assert(k.GetBalanceOf(ctx, previousOwner, common.RuneAsset()).Amount.Uint64(), Equals, collectorBefore.RuneAmount.Uint64())
		}
	}
}

func (s *HandlerManageTHORNameSuite) TestRejectsModuleOwnerTransferWithPendingPayout(c *C) {
	ctx, mgr, few, _ := setupAffiliateCollectorPartners(c)
	k := mgr.Keeper()
	collector := fundNamedCollector(c, ctx, k, few.Name, 1000)
	c.Assert(triggerPreferredAssetSwap(ctx, mgr, few, collector, 1), IsNil)
	queuedBefore := affiliateIntegrationQueuedSwaps(c, ctx, k)
	c.Assert(queuedBefore, HasLen, 1)
	assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
	asgardBefore := k.GetRuneBalanceOfModule(ctx, AsgardName)
	eventsBefore := len(ctx.EventManager().Events())
	msg := NewMsgManageTHORName(few.Name, common.THORChain, GetRandomTHORAddress(),
		common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()), 0, common.EmptyAsset,
		k.GetModuleAccAddress(AsgardName), few.Owner, -1)
	_, err := NewInternalHandler(mgr)(ctx, msg)
	c.Assert(err, ErrorMatches, "invalid THORName owner: .*module address")
	after, err := k.GetTHORName(ctx, few.Name)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, few)
	c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), DeepEquals, queuedBefore)
	assertNamedAffiliateBalance(c, ctx, k, few.Name, 0)
	c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgardBefore), Equals, true)
	c.Assert(len(ctx.EventManager().Events()), Equals, eventsBefore)

	msg.Owner = GetRandomBech32Addr()
	_, err = NewInternalHandler(mgr)(ctx, msg)
	c.Assert(err, IsNil)
	after, err = k.GetTHORName(ctx, few.Name)
	c.Assert(err, IsNil)
	c.Assert(after.Owner.Equals(msg.Owner), Equals, true)
	c.Assert(affiliateIntegrationQueuedSwaps(c, ctx, k), DeepEquals, queuedBefore)
}

func (s *HandlerManageTHORNameSuite) TestExistingModuleOwnerAliasUpdates(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	owner := k.GetModuleAccAddress(AsgardName)
	tn := NewTHORName("few", ctx.BlockHeight()+1000, nil)
	tn.Owner = owner
	k.SetTHORName(ctx, tn)
	fundNamedCollector(c, ctx, k, tn.Name, 200)
	for _, explicit := range []bool{false, true} {
		msg := NewMsgManageTHORName(tn.Name, common.THORChain, GetRandomTHORAddress(),
			common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()), 0, common.EmptyAsset, nil, owner, -1)
		if explicit {
			msg.Owner = owner
		}
		_, err := NewInternalHandler(mgr)(ctx, msg)
		c.Assert(err, IsNil)
		after, err := k.GetTHORName(ctx, tn.Name)
		c.Assert(err, IsNil)
		c.Assert(after.Owner.Equals(owner), Equals, true)
		c.Assert(after.GetAlias(common.THORChain).Equals(msg.Address), Equals, true)
		assertNamedAffiliateBalance(c, ctx, k, tn.Name, 200)
	}
}
