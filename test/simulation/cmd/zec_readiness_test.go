package main

import (
	"errors"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestZecReadinessWaitsPastWarmupAndForHeightAdvance(t *testing.T) {
	for _, heights := range [][]int64{
		{0, 250, 399, 400, 400, 401},
		{500, 500, 501},
	} {
		now := time.Unix(0, 0)
		calls := 0
		err := waitForZecRPCReadinessWithClock("rpc", func(string) (int64, error) {
			height := heights[calls]
			calls++
			return height, nil
		}, func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }, 10*time.Second)
		if err != nil || calls != len(heights) {
			t.Fatalf("heights=%v: err=%v calls=%d", heights, err, calls)
		}
	}
}

func TestZecReadinessRemainsBounded(t *testing.T) {
	for _, height := range []int64{399, 400, 600} {
		now := time.Unix(0, 0)
		err := waitForZecRPCReadinessWithClock("rpc", func(string) (int64, error) {
			return height, nil
		}, func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }, 3*time.Second)
		if err == nil || now.Sub(time.Unix(0, 0)) > 4*time.Second {
			t.Fatalf("height=%d: err=%v elapsed=%v", height, err, now.Sub(time.Unix(0, 0)))
		}
	}
}

func TestZecReadinessRecoversAfterRPCFailureAndRewind(t *testing.T) {
	now := time.Unix(0, 0)
	heights := []int64{405, -1, 399, 400, 401}
	calls := 0
	err := waitForZecRPCReadinessWithClock("rpc", func(string) (int64, error) {
		height := heights[calls]
		calls++
		if height < 0 {
			return 0, errors.New("RPC unavailable")
		}
		return height, nil
	}, func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }, 10*time.Second)
	if err != nil || calls != len(heights) {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

type zecFundingClient struct {
	LiteChainClient
	balances []uint64
	calls    int
	err      error
}

func (c *zecFundingClient) GetAccount(*common.PubKey) (*common.Account, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	index := c.calls - 1
	if index >= len(c.balances) {
		index = len(c.balances) - 1
	}
	return &common.Account{Coins: common.NewCoins(common.NewCoin(common.ZECAsset, sdkmath.NewUint(c.balances[index])))}, nil
}

func TestZecFundingWaitsForConfirmedTarget(t *testing.T) {
	now := time.Unix(0, 0)
	client := &zecFundingClient{balances: []uint64{5, 5, 10}}
	err := waitForZecFunding(client, sdkmath.NewUint(10), func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }, 5*time.Second)
	if err != nil || client.calls != 3 {
		t.Fatalf("calls=%d err=%v", client.calls, err)
	}
	client.err = errors.New("RPC unavailable")
	if err := waitForZecFunding(client, sdkmath.NewUint(10), func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }, 3*time.Second); err == nil {
		t.Fatal("unconfirmed funding must fail on timeout")
	}
}
