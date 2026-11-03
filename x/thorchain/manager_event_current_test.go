package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type EventManagerTestSuite struct{}

var _ = Suite(&EventManagerTestSuite{})

func (s *EventManagerTestSuite) TestEmitPoolEvent(c *C) {
	ctx, _ := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	c.Assert(eventMgr, NotNil)
	ctx = ctx.WithBlockHeight(1024)
	c.Assert(eventMgr.EmitEvent(ctx, NewEventPool(common.ETHAsset, PoolAvailable)), IsNil)
}

func (s *EventManagerTestSuite) TestEmitErrataEvent(c *C) {
	ctx, _ := setupKeeperForTest(c)
	eventMgr := newEventMgr()
	c.Assert(eventMgr, NotNil)
	ctx = ctx.WithBlockHeight(1024)
	errataEvent := NewEventErrata(GetRandomTxHash(), PoolMods{
		PoolMod{
			Asset:    common.ETHAsset,
			RuneAmt:  cosmos.ZeroUint(),
			RuneAdd:  false,
			AssetAmt: cosmos.NewUint(100),
			AssetAdd: true,
		},
	})
	c.Assert(eventMgr.EmitEvent(ctx, errataEvent), IsNil)
}

func (s *EventManagerTestSuite) TestEmitGasEvent(c *C) {
	ctx, _ := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	c.Assert(eventMgr, NotNil)
	ctx = ctx.WithBlockHeight(1024)
	gasEvent := NewEventGas()
	gasEvent.Pools = append(gasEvent.Pools, GasPool{
		Asset:    common.ETHAsset,
		AssetAmt: cosmos.ZeroUint(),
		RuneAmt:  cosmos.NewUint(1024),
		Count:    1,
	})
	c.Assert(eventMgr.EmitGasEvent(ctx, gasEvent), IsNil)
	c.Assert(eventMgr.EmitGasEvent(ctx, nil), IsNil)
}

func (s *EventManagerTestSuite) TestEmitFeeEvent(c *C) {
	ctx, _ := setupKeeperForTest(c)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	c.Assert(eventMgr, NotNil)
	ctx = ctx.WithBlockHeight(1024)
	fee := common.NewFee(common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1024))), cosmos.ZeroUint())
	feeEvent := NewEventFee(common.BlankTxID, fee, cosmos.ZeroUint())
	c.Assert(eventMgr.EmitFeeEvent(ctx, feeEvent), IsNil)
	// a nil fee event must not panic and should be ignored
	c.Assert(eventMgr.EmitFeeEvent(ctx, nil), IsNil)
}
