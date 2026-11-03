package keeper_test

import (
	"errors"
	"strings"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/scheduler"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/keeper"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/types"
)

func setupKeeperWithWasm(s *KeeperTestSuite, wk types.WasmKeeper) keeper.Keeper {
	return keeper.NewKeeper(
		s.encCfg.Codec,
		runtime.NewKVStoreService(s.storeKeys[types.StoreKey]),
		wk,
		authAcc.String(),
	)
}

// ErrorWasmKeeper is a mock that returns an error on Execute.
type ErrorWasmKeeper struct {
	err error
}

func (m *ErrorWasmKeeper) Execute(ctx sdk.Context, contractAddress, caller sdk.AccAddress, msg []byte, coins sdk.Coins) ([]byte, error) {
	return nil, m.err
}

type EventfulErrorWasmKeeper struct {
	storeKey *storetypes.KVStoreKey
	err      error
}

var failedWasmCommitKey = []byte("failed-wasm-commit")

func (m *EventfulErrorWasmKeeper) Execute(ctx sdk.Context, contractAddress, caller sdk.AccAddress, msg []byte, coins sdk.Coins) ([]byte, error) {
	ctx.KVStore(m.storeKey).Set(failedWasmCommitKey, []byte("committed"))
	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			"wasm",
			sdk.NewAttribute("_contract_address", contractAddress.String()),
			sdk.NewAttribute("action", "before_failure"),
		),
		sdk.NewEvent(
			"contract_event",
			sdk.NewAttribute("_contract_address", contractAddress.String()),
			sdk.NewAttribute("phase", "before_failure"),
		),
	})
	return nil, m.err
}

// SelectiveWasmKeeper guzzles past the per-execution gas cap when the msg
// contains "guzzle"; otherwise it writes a marker to the store and succeeds.
type SelectiveWasmKeeper struct {
	storeKey *storetypes.KVStoreKey
}

var committedKey = []byte("selective-committed")

func (m *SelectiveWasmKeeper) Execute(ctx sdk.Context, contractAddress, caller sdk.AccAddress, msg []byte, coins sdk.Coins) ([]byte, error) {
	if strings.Contains(string(msg), "guzzle") {
		// Exceeds MaxScheduleExecGas -> OutOfGas panic, caught by ExecuteSchedule.
		ctx.GasMeter().ConsumeGas(types.MaxScheduleExecGas+1, "guzzle")
		return nil, nil // unreachable
	}
	ctx.KVStore(m.storeKey).Set(committedKey, []byte("committed"))
	return nil, nil
}

func (s *KeeperTestSuite) TestLogger(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)
	logger := s.keeper.Logger(sdkCtx)
	c.Assert(logger, NotNil)
}

func (s *KeeperTestSuite) TestExecuteSchedule_NoMsgs(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// ExecuteSchedule on an empty schedule should succeed
	err := s.keeper.ExecuteSchedule(sdkCtx)
	c.Assert(err, IsNil)
}

func (s *KeeperTestSuite) TestExecuteSchedule_WithMsgs(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Add a msg scheduled for the current block height
	height := uint64(sdkCtx.BlockHeight()) + 1
	err := s.keeper.SetSchedule(sdkCtx, types.Schedule{
		Height: height,
		Msgs: []types.MsgScheduleExecuteContract{
			{
				Sender: accAddrs[0].String(),
				Msg:    []byte(`{"do":"something"}`),
			},
		},
	})
	c.Assert(err, IsNil)

	// Advance context to the scheduled height
	sdkCtx = sdkCtx.WithBlockHeight(int64(height))
	err = s.keeper.ExecuteSchedule(sdkCtx)
	c.Assert(err, IsNil)

	// Schedule should be removed after execution
	res, err := s.keeper.GetSchedule(sdkCtx, height)
	c.Assert(err, IsNil)
	c.Assert(res.Msgs, HasLen, 0)
}

func (s *KeeperTestSuite) TestExecuteSchedule_WasmErrorLogsAndRemovesSchedule(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	errKeeper := setupKeeperWithWasm(s, &ErrorWasmKeeper{err: errors.New("contract execution failed")})

	height := uint64(sdkCtx.BlockHeight()) + 1
	err := errKeeper.SetSchedule(sdkCtx, types.Schedule{
		Height: height,
		Msgs: []types.MsgScheduleExecuteContract{
			{
				Sender: accAddrs[0].String(),
				Msg:    []byte(`{"do":"fail"}`),
			},
		},
	})
	c.Assert(err, IsNil)

	sdkCtx = sdkCtx.WithBlockHeight(int64(height))
	err = errKeeper.ExecuteSchedule(sdkCtx)
	c.Assert(err, IsNil) // errors are logged, not returned

	// Check that the schedule was removed
	res, err := errKeeper.GetSchedule(sdkCtx, height)
	c.Assert(err, IsNil)
	c.Assert(res.Msgs, HasLen, 0)
}

func (s *KeeperTestSuite) TestExecuteSchedule_WasmErrorDiscardsEventsAndState(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)
	errKeeper := setupKeeperWithWasm(s, &EventfulErrorWasmKeeper{
		storeKey: s.storeKeys[types.StoreKey],
		err:      errors.New("contract dispatch failed"),
	})

	height := uint64(sdkCtx.BlockHeight()) + 1
	err := errKeeper.SetSchedule(sdkCtx, types.Schedule{
		Height: height,
		Msgs: []types.MsgScheduleExecuteContract{
			{
				Sender: accAddrs[0].String(),
				Msg:    []byte(`{"do":"fail_after_events"}`),
			},
		},
	})
	c.Assert(err, IsNil)

	sdkCtx = sdkCtx.WithBlockHeight(int64(height))
	err = errKeeper.ExecuteSchedule(sdkCtx)
	c.Assert(err, IsNil)

	// Failed wasm execution must not commit contract events or state writes, but
	// the scheduler bookkeeping still completes on the parent context.
	c.Assert(sdkCtx.KVStore(s.storeKeys[types.StoreKey]).Get(failedWasmCommitKey), IsNil)
	res, err := errKeeper.GetSchedule(sdkCtx, height)
	c.Assert(err, IsNil)
	c.Assert(res.Msgs, HasLen, 0)
}

func (s *KeeperTestSuite) TestExecuteSchedule_PerExecGasCap(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)
	gasKeeper := setupKeeperWithWasm(s, &SelectiveWasmKeeper{storeKey: s.storeKeys[types.StoreKey]})

	height := uint64(sdkCtx.BlockHeight()) + 1
	// A gas-guzzling execution alongside an in-budget sibling at the same height.
	err := gasKeeper.SetSchedule(sdkCtx, types.Schedule{
		Height: height,
		Msgs: []types.MsgScheduleExecuteContract{
			{Sender: accAddrs[0].String(), Msg: []byte(`{"do":"guzzle"}`)},
			{Sender: accAddrs[1].String(), Msg: []byte(`{"do":"work"}`)},
		},
	})
	c.Assert(err, IsNil)

	sdkCtx = sdkCtx.WithBlockHeight(int64(height))
	// Must not halt: the over-budget run is OutOfGas-skipped, not propagated.
	err = gasKeeper.ExecuteSchedule(sdkCtx)
	c.Assert(err, IsNil)

	// The in-budget sibling still committed its state write.
	c.Assert(sdkCtx.KVStore(s.storeKeys[types.StoreKey]).Get(committedKey), DeepEquals, []byte("committed"))

	// Schedule drained.
	res, err := gasKeeper.GetSchedule(sdkCtx, height)
	c.Assert(err, IsNil)
	c.Assert(res.Msgs, HasLen, 0)
}

func (s *KeeperTestSuite) TestAddMsg_PerHeightCap(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// The first MaxMsgsPerHeight messages for a height succeed.
	for i := 0; i < types.MaxMsgsPerHeight; i++ {
		c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
			After:  10,
			Sender: accAddrs[0].String(),
			Msg:    []byte(`{"a":"1"}`),
		}), IsNil)
	}

	// The next one is rejected.
	err := s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  10,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	})
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, types.ErrScheduleHeightFull), Equals, true)

	// The cap is per-height: a different target height still accepts messages.
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  11,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	}), IsNil)
}

func (s *KeeperTestSuite) TestAddMsg_AfterCap(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// After at the max is accepted.
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  types.MaxAfter,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	}), IsNil)

	// After beyond the max is rejected at the keeper, independent of ValidateBasic.
	err := s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  types.MaxAfter + 1,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	})
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, types.ErrAfterTooLarge), Equals, true)
}

func (s *KeeperTestSuite) TestRemoveSchedule(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Set a schedule
	err := s.keeper.SetSchedule(sdkCtx, types.Schedule{
		Height: 500,
		Msgs: []types.MsgScheduleExecuteContract{
			{Sender: accAddrs[0].String(), Msg: []byte(`{"x":"y"}`)},
		},
	})
	c.Assert(err, IsNil)

	// Verify it exists
	res, err := s.keeper.GetSchedule(sdkCtx, 500)
	c.Assert(err, IsNil)
	c.Assert(res.Msgs, HasLen, 1)

	// Remove it
	err = s.keeper.RemoveSchedule(sdkCtx, 500)
	c.Assert(err, IsNil)

	// Verify it's gone (returns empty schedule)
	res, err = s.keeper.GetSchedule(sdkCtx, 500)
	c.Assert(err, IsNil)
	c.Assert(res.Msgs, HasLen, 0)
}

func (s *KeeperTestSuite) TestListSchedules(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Add multiple schedules
	for i := uint64(100); i < 103; i++ {
		err := s.keeper.SetSchedule(sdkCtx, types.Schedule{
			Height: i,
			Msgs: []types.MsgScheduleExecuteContract{
				{Sender: accAddrs[0].String(), Msg: []byte(`{"i":"test"}`)},
			},
		})
		c.Assert(err, IsNil)
	}

	// List all
	schedules, pageRes, err := s.keeper.ListSchedules(sdkCtx, &query.PageRequest{
		Limit: 10,
	})
	c.Assert(err, IsNil)
	c.Assert(pageRes, NotNil)
	c.Assert(schedules, HasLen, 3)
}

func (s *KeeperTestSuite) TestListSchedules_Empty(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	schedules, pageRes, err := s.keeper.ListSchedules(sdkCtx, &query.PageRequest{
		Limit: 10,
	})
	c.Assert(err, IsNil)
	c.Assert(pageRes, NotNil)
	c.Assert(schedules, HasLen, 0)
}

func (s *KeeperTestSuite) TestGRPCSchedule(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Set a schedule
	err := s.keeper.SetSchedule(sdkCtx, types.Schedule{
		Height: 42,
		Msgs: []types.MsgScheduleExecuteContract{
			{Sender: accAddrs[0].String(), Msg: []byte(`{"q":"test"}`)},
		},
	})
	c.Assert(err, IsNil)

	// Query via gRPC
	resp, err := s.queryClient.Schedule(s.ctx, &types.QueryScheduleRequest{Height: 42})
	c.Assert(err, IsNil)
	c.Assert(resp.Schedule, NotNil)
	c.Assert(resp.Schedule.Height, Equals, uint64(42))
	c.Assert(resp.Schedule.Msgs, HasLen, 1)
}

func (s *KeeperTestSuite) TestGRPCSchedule_NotFound(c *C) {
	// Query a non-existent schedule returns empty schedule
	resp, err := s.queryClient.Schedule(s.ctx, &types.QueryScheduleRequest{Height: 999})
	c.Assert(err, IsNil)
	c.Assert(resp.Schedule, NotNil)
	c.Assert(resp.Schedule.Msgs, HasLen, 0)
}

func (s *KeeperTestSuite) TestGRPCSchedules(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Add multiple schedules
	for i := uint64(10); i < 13; i++ {
		err := s.keeper.SetSchedule(sdkCtx, types.Schedule{
			Height: i,
			Msgs: []types.MsgScheduleExecuteContract{
				{Sender: accAddrs[0].String(), Msg: []byte(`{"q":"all"}`)},
			},
		})
		c.Assert(err, IsNil)
	}

	// Query all via gRPC
	resp, err := s.queryClient.Schedules(s.ctx, &types.QuerySchedulesRequest{
		Pagination: query.PageRequest{Limit: 10},
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Schedules, HasLen, 3)
}

func (s *KeeperTestSuite) TestGetSchedulesBySender(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Add msgs from two different senders at different heights
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  100,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	}), IsNil)
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  200,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"2"}`),
	}), IsNil)
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  100,
		Sender: accAddrs[1].String(),
		Msg:    []byte(`{"b":"1"}`),
	}), IsNil)

	// Query sender 0 - should get 2 schedules
	res, err := s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[0].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Pagination, NotNil)
	c.Assert(res.Schedules, HasLen, 2)
	c.Assert(res.Schedules[0].Height, Equals, uint64(100))
	c.Assert(res.Schedules[0].Msgs[0].Msg, DeepEquals, []byte(`{"a":"1"}`))
	c.Assert(res.Schedules[0].Msgs[0].Sender, Equals, accAddrs[0].String())
	c.Assert(res.Schedules[1].Height, Equals, uint64(200))
	c.Assert(res.Schedules[1].Msgs[0].Msg, DeepEquals, []byte(`{"a":"2"}`))
	c.Assert(res.Schedules[1].Msgs[0].Sender, Equals, accAddrs[0].String())

	// Query sender 1 - should get 1 schedule
	res, err = s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[1].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Pagination, NotNil)
	c.Assert(res.Schedules, HasLen, 1)
	c.Assert(res.Schedules[0].Height, Equals, uint64(100))
	c.Assert(res.Schedules[0].Msgs[0].Msg, DeepEquals, []byte(`{"a":"1"}`))
	c.Assert(res.Schedules[0].Msgs[0].Sender, Equals, accAddrs[0].String())
	c.Assert(res.Schedules[0].Msgs[1].Msg, DeepEquals, []byte(`{"b":"1"}`))
	c.Assert(res.Schedules[0].Msgs[1].Sender, Equals, accAddrs[1].String())

	// Query unknown sender - should get 0
	res, err = s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: sdk.AccAddress("addr?_______________").String()})
	c.Assert(err, IsNil)
	c.Assert(res.Schedules, HasLen, 0)

	// Query invalid sender - should error
	res, err = s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: "abc123"})
	c.Assert(err, NotNil)
	c.Assert(res, IsNil)
}

func (s *KeeperTestSuite) TestRemoveScheduleCleansIndex(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  100,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	}), IsNil)
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  200,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"2"}`),
	}), IsNil)

	// Verify both are indexed
	res, err := s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[0].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Schedules, HasLen, 2)

	// Remove only the first schedule
	c.Assert(s.keeper.RemoveSchedule(sdkCtx, 100), IsNil)

	// Only the second schedule should remain
	res, err = s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[0].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Schedules, HasLen, 1)
	c.Assert(res.Schedules[0].Height, Equals, uint64(200))
	c.Assert(res.Schedules[0].Msgs[0].Msg, DeepEquals, []byte(`{"a":"2"}`))
}

func (s *KeeperTestSuite) TestSetScheduleUpdatesIndex(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	// Add a msg via AddMsg (which maintains the index)
	c.Assert(s.keeper.AddMsg(sdkCtx, types.MsgScheduleExecuteContract{
		After:  100,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"a":"1"}`),
	}), IsNil)

	// Verify sender 0 is indexed
	res, err := s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[0].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Schedules, HasLen, 1)

	// Overwrite the schedule via SetSchedule, replacing sender 0 with sender 1
	c.Assert(s.keeper.SetSchedule(sdkCtx, types.Schedule{
		Height: 100,
		Msgs: []types.MsgScheduleExecuteContract{
			{Sender: accAddrs[1].String(), Msg: []byte(`{"b":"1"}`)},
		},
	}), IsNil)

	// Sender 0 should no longer be indexed
	res, err = s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[0].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Schedules, HasLen, 0)

	// Sender 1 should now be indexed
	res, err = s.keeper.Schedules(s.ctx, &types.QuerySchedulesRequest{Sender: accAddrs[1].String()})
	c.Assert(err, IsNil)
	c.Assert(res.Schedules, HasLen, 1)
	c.Assert(res.Schedules[0].Height, Equals, uint64(100))
	c.Assert(res.Schedules[0].Msgs[0].Msg, DeepEquals, []byte(`{"b":"1"}`))
	c.Assert(res.Schedules[0].Msgs[0].Sender, Equals, accAddrs[1].String())
}

func (s *KeeperTestSuite) TestMsgServerScheduleExecuteContract(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	resp, err := s.msgServer.ScheduleExecuteContract(s.ctx, &types.MsgScheduleExecuteContract{
		After:  5,
		Sender: accAddrs[0].String(),
		Msg:    []byte(`{"action":"test"}`),
	})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)

	// Verify the schedule was stored at block height + after
	expectedHeight := uint64(sdkCtx.BlockHeight()) + 5
	schedule, err := s.keeper.GetSchedule(sdkCtx, expectedHeight)
	c.Assert(err, IsNil)
	c.Assert(schedule.Msgs, HasLen, 1)
	c.Assert(schedule.Msgs[0].Sender, Equals, accAddrs[0].String())
}

// TestExportGenesis_LegacyAfterSurvivesValidation guards the export -> validate
// round trip for schedules persisted before the MaxAfter horizon existed. The
// horizon is enforced on new scheduling (AddMsg / ValidateBasic) only; stored
// messages keep their historical After and must still pass genesis validation.
func (s *KeeperTestSuite) TestExportGenesis_LegacyAfterSurvivesValidation(c *C) {
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	legacy := types.MsgScheduleExecuteContract{
		Sender: accAddrs[0].String(),
		After:  types.MaxAfter + 1,
		Msg:    []byte(`{"run":{}}`),
	}

	// New scheduling beyond the horizon is still rejected.
	err := s.keeper.AddMsg(sdkCtx, legacy)
	c.Assert(errors.Is(err, types.ErrAfterTooLarge), Equals, true)

	// Simulate pre-upgrade state: the message was stored before the bound existed.
	height := uint64(sdkCtx.BlockHeight()) + 10
	err = s.keeper.SetSchedule(sdkCtx, types.Schedule{Height: height, Msgs: []types.MsgScheduleExecuteContract{legacy}})
	c.Assert(err, IsNil)

	// Export preserves the historical field verbatim...
	exported := scheduler.ExportGenesis(sdkCtx, s.keeper)
	c.Assert(exported.Schedules, HasLen, 1)
	c.Assert(exported.Schedules[0].Height, Equals, height)
	c.Assert(exported.Schedules[0].Msgs, HasLen, 1)
	c.Assert(exported.Schedules[0].Msgs[0].After, Equals, uint64(types.MaxAfter+1))

	// ...and the exported genesis still validates, both in memory and after the
	// JSON round trip that AppModuleBasic.ValidateGenesis performs.
	c.Assert(exported.Validate(), IsNil)
	bz, err := s.encCfg.Codec.MarshalJSON(exported)
	c.Assert(err, IsNil)
	var decoded types.GenesisState
	c.Assert(s.encCfg.Codec.UnmarshalJSON(bz, &decoded), IsNil)
	c.Assert(decoded.Validate(), IsNil)
}
