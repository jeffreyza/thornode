package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerSwitchSuite struct{}

var _ = Suite(&HandlerSwitchSuite{})

func (s *HandlerSwitchSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerSwitchSuite) TestSwitchHandler_InvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerSwitchSuite) TestSwitchHandler_ValidateBasicFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()

	// Empty asset should fail ValidateBasic
	msg := NewMsgSwitch(common.EmptyAsset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Zero amount should fail
	msg = NewMsgSwitch(common.ETHAsset, cosmos.ZeroUint(), signer, signer, tx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Native asset should fail
	msg = NewMsgSwitch(common.RuneNative, cosmos.NewUint(100), signer, signer, tx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty address should fail
	msg = NewMsgSwitch(common.ETHAsset, cosmos.NewUint(100), cosmos.AccAddress{}, signer, tx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty signer should fail
	msg = NewMsgSwitch(common.ETHAsset, cosmos.NewUint(100), signer, cosmos.AccAddress{}, tx)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// Empty txID should fail
	txEmpty := tx
	txEmpty.ID = ""
	msg = NewMsgSwitch(common.ETHAsset, cosmos.NewUint(100), signer, signer, txEmpty)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerSwitchSuite) TestSwitchHandler_CheckEnabledNotSet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// No mimir set, so checkEnabled should fail (m will be -1 which is <= 0)
	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, fmt.Sprintf("%s switching is not enabled", asset.String()))
}

func (s *HandlerSwitchSuite) TestSwitchHandler_CheckEnabledZeroValue(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Set mimir to 0 (disabled)
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 0)

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, fmt.Sprintf("%s switching is not enabled", asset.String()))
}

func (s *HandlerSwitchSuite) TestSwitchHandler_CheckEnabledFutureBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Set mimir to a value >= ctx.BlockHeight() (18) — switching not yet enabled
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 18) // Equal to block height, should fail (m >= ctx.BlockHeight())

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, fmt.Sprintf("%s switching is not enabled", asset.String()))

	// Set to future block height
	mgr.Keeper().SetMimir(ctx, mimirKey, 100)
	_, err = h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, fmt.Sprintf("%s switching is not enabled", asset.String()))
}

func (s *HandlerSwitchSuite) TestSwitchHandler_CheckEnabledNegativeValue(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Set mimir to negative value (disabled)
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, -1)

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, fmt.Sprintf("%s switching is not enabled", asset.String()))
}

// switchMgrFailDummy is a SwitchManager that fails on Switch
type switchMgrFailDummy struct{}

func (s *switchMgrFailDummy) IsSwitch(_ cosmos.Context, _ common.Asset) bool {
	return true
}

func (s *switchMgrFailDummy) Switch(
	_ cosmos.Context,
	_ common.Asset,
	_ cosmos.Uint,
	_ cosmos.AccAddress,
	_ common.Address,
	_ common.TxID,
) (common.Address, error) {
	return common.NoAddress, errors.New("fail to switch")
}

func (s *HandlerSwitchSuite) TestSwitchHandler_HandleSwitchFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Enable switching
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 1)

	// Use a failing switch manager
	mgr.switchManager = &switchMgrFailDummy{}

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "fail to switch")
}

func (s *HandlerSwitchSuite) TestSwitchHandler_TxOutStoreFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Enable switching
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 1)

	// Use a failing tx out store
	mgr.txOutStore = NewTxStoreFailDummy()

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

// switchMgrReturnNoAddressDummy returns NoAddress from Switch, causing TryAddTxOutItem to return false
type switchMgrReturnNoAddressDummy struct{}

func (s *switchMgrReturnNoAddressDummy) IsSwitch(_ cosmos.Context, _ common.Asset) bool {
	return true
}

func (s *switchMgrReturnNoAddressDummy) Switch(
	_ cosmos.Context,
	_ common.Asset,
	_ cosmos.Uint,
	_ cosmos.AccAddress,
	_ common.Address,
	_ common.TxID,
) (common.Address, error) {
	return common.NoAddress, nil
}

// txOutStoreReturnFalseDummy returns false from TryAddTxOutItem without error
type txOutStoreReturnFalseDummy struct {
	TxOutStoreDummy
}

func (tos *txOutStoreReturnFalseDummy) TryAddTxOutItem(_ cosmos.Context, _ Manager, _ TxOutItem, _ cosmos.Uint) (bool, error) {
	return false, nil
}

func (s *HandlerSwitchSuite) TestSwitchHandler_TxOutReturnsFalseNoError(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Enable switching
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 1)

	// Use switch manager that returns NoAddress, and txOutStore that returns false
	mgr.switchManager = &switchMgrReturnNoAddressDummy{}
	mgr.txOutStore = &txOutStoreReturnFalseDummy{}

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, errFailAddOutboundTx), Equals, true)
}

// mintedSwitchAsset returns the THORChain-native asset that the switch manager
// mints for the given L1 switch asset.
func mintedSwitchAsset(asset common.Asset) common.Asset {
	return common.Asset{Chain: common.THORChain, Symbol: asset.Symbol, Ticker: asset.Ticker}
}

// TestSwitchHandler_MintRolledBackOnTxOutFailure ensures the native mint is
// discarded when the burn outbound cannot be scheduled, so supply is never
// inflated without a corresponding L1 burn (#3033).
func (s *HandlerSwitchSuite) TestSwitchHandler_MintRolledBackOnTxOutFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}
	native := mintedSwitchAsset(asset)

	// Enable switching
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 1)

	// Use the real switch manager (which mints) with a tx out store that always fails.
	mgr.txOutStore = NewTxStoreFailDummy()

	// No native tokens minted to the owner yet.
	c.Assert(mgr.Keeper().GetBalanceOf(ctx, signer, native).Amount.IsZero(), Equals, true)

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)

	// The mint must have been rolled back along with the failed outbound.
	c.Assert(mgr.Keeper().GetBalanceOf(ctx, signer, native).Amount.IsZero(), Equals, true)
}

// TestSwitchHandler_MintRolledBackWhenOutboundDropped ensures that when the burn
// outbound is silently dropped (TryAddTxOutItem returns false without an error)
// the native mint is discarded rather than left un-burned (#3033).
func (s *HandlerSwitchSuite) TestSwitchHandler_MintRolledBackWhenOutboundDropped(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}
	native := mintedSwitchAsset(asset)

	// Enable switching
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 1)

	// Real switch manager mints; tx out store drops the output (false, nil).
	mgr.txOutStore = &txOutStoreReturnFalseDummy{}

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, errFailAddOutboundTx), Equals, true)

	// The mint must have been rolled back since no outbound was scheduled.
	c.Assert(mgr.Keeper().GetBalanceOf(ctx, signer, native).Amount.IsZero(), Equals, true)
}

func (s *HandlerSwitchSuite) TestSwitchHandler_HappyPath(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewSwitchHandler(mgr)

	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	asset := common.Asset{Chain: common.GAIAChain, Symbol: "KUJI", Ticker: "KUJI"}

	// Enable switching (set to a past block height, m > 0 && m < ctx.BlockHeight())
	mimirKey := fmt.Sprintf(constants.MimirTemplateSwitch, asset.Chain.String(), asset.Symbol.String())
	mgr.Keeper().SetMimir(ctx, mimirKey, 1)

	// Use dummy switch manager and txOutStore (real ones require full vault setup)
	mgr.switchManager = &switchMgrReturnNoAddressDummy{}
	mgr.txOutStore = NewTxStoreDummy()

	msg := NewMsgSwitch(asset, cosmos.NewUint(100), signer, signer, tx)
	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}
