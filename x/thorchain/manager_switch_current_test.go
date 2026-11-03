package thorchain

import (
	"errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	. "gopkg.in/check.v1"
)

type SwitchMgrSuite struct{}

var _ = Suite(&SwitchMgrSuite{})

func (s *SwitchMgrSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// --- Test helpers ---

// switchMgrTestKeeper embeds KVStoreDummy but overrides MintAndSendToAccount
type switchMgrTestKeeper struct {
	keeper.KVStoreDummy
	mintErr error
}

func (k *switchMgrTestKeeper) MintAndSendToAccount(ctx cosmos.Context, to cosmos.AccAddress, coin common.Coin) error {
	return k.mintErr
}

// failEmitEventMgr is an EventManager that fails on EmitEvent
type failEmitEventMgr struct {
	DummyEventMgr
}

func (m *failEmitEventMgr) EmitEvent(ctx cosmos.Context, evt EmitEventItem) error {
	return errors.New("fail to emit event")
}

// --- Tests ---

func (s *SwitchMgrSuite) TestNewSwitchMgr(c *C) {
	k := &switchMgrTestKeeper{}
	eventMgr := NewDummyEventMgr()
	mgr := newSwitchMgr(k, eventMgr)
	c.Assert(mgr, NotNil)
	c.Assert(mgr.keeper, Equals, keeper.Keeper(k))
	c.Assert(mgr.eventMgr, Equals, EventManager(eventMgr))
}

func (s *SwitchMgrSuite) TestIsSwitch(c *C) {
	k := &switchMgrTestKeeper{}
	eventMgr := NewDummyEventMgr()
	mgr := newSwitchMgr(k, eventMgr)
	ctx, _ := setupManagerForTest(c)

	// All assets in switchMap should return true
	for assetStr := range switchMap {
		asset, err := common.NewAsset(assetStr)
		c.Assert(err, IsNil)
		c.Assert(mgr.IsSwitch(ctx, asset), Equals, true, Commentf("expected %s to be a switch asset", assetStr))
	}

	// Non-switch assets should return false
	c.Assert(mgr.IsSwitch(ctx, common.ETHAsset), Equals, false)
	c.Assert(mgr.IsSwitch(ctx, common.BTCAsset), Equals, false)
	c.Assert(mgr.IsSwitch(ctx, common.RuneNative), Equals, false)

	// An asset not in the map
	randomAsset := common.Asset{Chain: common.GAIAChain, Symbol: "NOTEXIST", Ticker: "NOTEXIST"}
	c.Assert(mgr.IsSwitch(ctx, randomAsset), Equals, false)
}

func (s *SwitchMgrSuite) TestSwitchSuccess(c *C) {
	k := &switchMgrTestKeeper{mintErr: nil}
	eventMgr := NewDummyEventMgr()
	mgr := newSwitchMgr(k, eventMgr)
	ctx, _ := setupManagerForTest(c)

	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}
	owner := GetRandomBech32Addr()
	assetAddr := common.Address("cosmos1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	txID := GetRandomTxHash()

	addr, err := mgr.Switch(ctx, asset, cosmos.NewUint(100), owner, assetAddr, txID)
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, common.GaiaZeroAddress)
}

func (s *SwitchMgrSuite) TestSwitchAllMapEntries(c *C) {
	k := &switchMgrTestKeeper{mintErr: nil}
	eventMgr := NewDummyEventMgr()
	mgr := newSwitchMgr(k, eventMgr)
	ctx, _ := setupManagerForTest(c)

	owner := GetRandomBech32Addr()
	assetAddr := common.Address("cosmos1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	txID := GetRandomTxHash()

	for assetStr, expectedAddr := range switchMap {
		asset, err := common.NewAsset(assetStr)
		c.Assert(err, IsNil)
		addr, err := mgr.Switch(ctx, asset, cosmos.NewUint(100), owner, assetAddr, txID)
		c.Assert(err, IsNil, Commentf("Switch failed for %s", assetStr))
		c.Assert(addr, Equals, expectedAddr, Commentf("wrong address for %s", assetStr))
	}
}

func (s *SwitchMgrSuite) TestSwitchNotAuthorized(c *C) {
	k := &switchMgrTestKeeper{mintErr: nil}
	eventMgr := NewDummyEventMgr()
	mgr := newSwitchMgr(k, eventMgr)
	ctx, _ := setupManagerForTest(c)

	// Use an asset not in the switchMap
	asset := common.ETHAsset
	owner := GetRandomBech32Addr()
	assetAddr := common.Address("0x1234567890")
	txID := GetRandomTxHash()

	addr, err := mgr.Switch(ctx, asset, cosmos.NewUint(100), owner, assetAddr, txID)
	c.Assert(err, NotNil)
	c.Assert(err, Equals, errNotAuthorized)
	c.Assert(addr, Equals, common.NoAddress)
}

func (s *SwitchMgrSuite) TestSwitchMintAndSendFails(c *C) {
	mintErr := errors.New("mint failed")
	k := &switchMgrTestKeeper{mintErr: mintErr}
	eventMgr := NewDummyEventMgr()
	mgr := newSwitchMgr(k, eventMgr)
	ctx, _ := setupManagerForTest(c)

	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}
	owner := GetRandomBech32Addr()
	assetAddr := common.Address("cosmos1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	txID := GetRandomTxHash()

	addr, err := mgr.Switch(ctx, asset, cosmos.NewUint(100), owner, assetAddr, txID)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "mint failed")
	c.Assert(addr, Equals, common.NoAddress)
}

func (s *SwitchMgrSuite) TestSwitchEmitEventFails(c *C) {
	k := &switchMgrTestKeeper{mintErr: nil}
	eventMgr := &failEmitEventMgr{}
	mgr := newSwitchMgr(k, eventMgr)
	ctx, _ := setupManagerForTest(c)

	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}
	owner := GetRandomBech32Addr()
	assetAddr := common.Address("cosmos1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	txID := GetRandomTxHash()

	// EmitEvent failure should be logged but not returned as error
	addr, err := mgr.Switch(ctx, asset, cosmos.NewUint(100), owner, assetAddr, txID)
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, common.GaiaZeroAddress)
}
