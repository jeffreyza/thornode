package thorchain

import (
	"encoding/json"
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *QuerierSuite) TestQuerySwapQueuePagination(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)

	for i := 1; i <= 105; i++ {
		msg := newQuerySwapQueueTestMsg(i, types.SwapType_market)
		c.Assert(k.SetSwapQueueItem(ctx, msg, 0), IsNil)
	}
	for i := 200; i <= 201; i++ {
		msg := newQuerySwapQueueTestMsg(i, types.SwapType_limit)
		c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
	}

	totalSwaps := uint64(107)

	legacyResp, err := qs.querySwapQueue(ctx, &types.QuerySwapQueueRequest{})
	c.Assert(err, IsNil)
	c.Assert(legacyResp.SwapQueue, HasLen, int(totalSwaps))

	defaultLimit := config.GetThornode().API.Pagination.DefaultPageSize
	expectedDefaultPageLen := defaultLimit
	if expectedDefaultPageLen > totalSwaps {
		expectedDefaultPageLen = totalSwaps
	}
	resp, err := qs.querySwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.SwapQueue, HasLen, int(expectedDefaultPageLen))
	c.Assert(resp.Pagination.Offset, Equals, uint64(0))
	c.Assert(resp.Pagination.Limit, Equals, defaultLimit)
	c.Assert(resp.Pagination.Total, Equals, totalSwaps)
	c.Assert(resp.Pagination.HasNext, Equals, defaultLimit < totalSwaps)
	c.Assert(resp.Pagination.HasPrev, Equals, false)

	resp, err = qs.querySwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Offset: 105, Limit: 10})
	c.Assert(err, IsNil)
	c.Assert(resp.SwapQueue, HasLen, 2)
	c.Assert(resp.SwapQueue[0].Tx.ID.Equals(querySwapQueueTestTxID(200)), Equals, true)
	c.Assert(resp.SwapQueue[1].Tx.ID.Equals(querySwapQueueTestTxID(201)), Equals, true)
	c.Assert(resp.Pagination.Offset, Equals, uint64(105))
	c.Assert(resp.Pagination.Limit, Equals, uint64(10))
	c.Assert(resp.Pagination.Total, Equals, totalSwaps)
	c.Assert(resp.Pagination.HasNext, Equals, false)
	c.Assert(resp.Pagination.HasPrev, Equals, true)

	maxLimit := config.GetThornode().API.Pagination.MaxPageSize
	resp, err = qs.querySwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Limit: maxLimit + 1})
	c.Assert(err, IsNil)
	c.Assert(resp.Pagination.Limit, Equals, maxLimit)
	c.Assert(resp.Pagination.Total, Equals, totalSwaps)

	resp, err = qs.querySwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Offset: totalSwaps, Limit: 10})
	c.Assert(err, IsNil)
	c.Assert(resp.SwapQueue, HasLen, 0)
	c.Assert(resp.Pagination.Offset, Equals, totalSwaps)
	c.Assert(resp.Pagination.Limit, Equals, uint64(10))
	c.Assert(resp.Pagination.Total, Equals, totalSwaps)
	c.Assert(resp.Pagination.HasNext, Equals, false)
	c.Assert(resp.Pagination.HasPrev, Equals, true)

	largeOffset := ^uint64(0) - 5
	resp, err = qs.querySwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Offset: largeOffset, Limit: 10})
	c.Assert(err, IsNil)
	c.Assert(resp.SwapQueue, HasLen, 0)
	c.Assert(resp.Pagination.HasNext, Equals, false)
	c.Assert(resp.Pagination.HasPrev, Equals, true)
}

func (s *QuerierSuite) TestQuerySwapQueuePaginationAdvQueueDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	qs := &queryServer{mgr: mgr}

	k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)

	for i := 1; i <= 3; i++ {
		msg := newQuerySwapQueueTestMsg(i, types.SwapType_market)
		c.Assert(k.SetSwapQueueItem(ctx, msg, 0), IsNil)
	}
	for i := 200; i <= 201; i++ {
		msg := newQuerySwapQueueTestMsg(i, types.SwapType_limit)
		c.Assert(k.SetAdvSwapQueueItem(ctx, msg), IsNil)
	}

	resp, err := qs.querySwapQueuePaginated(ctx, &types.QuerySwapQueuePaginatedRequest{Limit: 10})
	c.Assert(err, IsNil)
	c.Assert(resp.SwapQueue, HasLen, 3)
	c.Assert(resp.Pagination.Total, Equals, uint64(3))
	for _, msg := range resp.SwapQueue {
		c.Assert(msg.SwapType, Equals, types.SwapType_market)
	}
}

func (s *QuerierSuite) TestQuerySwapQueueJSONShape(c *C) {
	msg := newQuerySwapQueueTestMsg(1, types.SwapType_market)

	legacyBody, err := (&types.QuerySwapQueueResponse{
		SwapQueue: []*types.MsgSwap{&msg},
	}).MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	var legacy []any
	c.Assert(json.Unmarshal(legacyBody, &legacy), IsNil)
	c.Assert(legacy, HasLen, 1)

	paginatedBody, err := (&types.QuerySwapQueuePaginatedResponse{
		SwapQueue: []*types.MsgSwap{&msg},
		Pagination: &types.PaginationMeta{
			Offset:  0,
			Limit:   1,
			Total:   1,
			HasNext: false,
			HasPrev: false,
		},
	}).MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	var paginated map[string]any
	c.Assert(json.Unmarshal(paginatedBody, &paginated), IsNil)
	c.Assert(paginated["swap_queue"], NotNil)
	c.Assert(paginated["pagination"], NotNil)
}

func newQuerySwapQueueTestMsg(id int, swapType types.SwapType) types.MsgSwap {
	return types.MsgSwap{
		Tx: common.Tx{
			ID: querySwapQueueTestTxID(id),
			Coins: common.Coins{
				common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(id))),
			},
		},
		TargetAsset: common.RuneAsset(),
		TradeTarget: cosmos.NewUint(common.One),
		SwapType:    swapType,
	}
}

func querySwapQueueTestTxID(id int) common.TxID {
	return common.TxID(fmt.Sprintf("%064d", id))
}
