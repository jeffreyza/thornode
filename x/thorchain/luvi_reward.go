package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// The LUVI reward check runs around the pool-reward payout in
// NetworkMgr.UpdateNetwork. For every pool paid r > 0 RUNE it compares the
// pool's change in G from just before the payout to the end of the reward
// step with the expected sqrt(A(R+r)) - sqrt(AR) (types.MeasureLuviReward has
// the derivation and the LP share 1 - S/(2A)), and it checks that the pool
// rewards sum to what the rewards event reports and to the RUNE Asgard
// received. It is report-only: a failure logs an error and emits an
// EventLuviReward, nothing else.
//
// Event volume: rewards go to every available pool every block, so one event
// per pool per block would add tens of events to every block forever. The
// check emits only failures, plus every rewarded pool's measurement once
// every luviRewardSampleBlocks blocks as a periodic sample of the LP share.
// A running summary would need a store write per pool per block; the sample
// needs none.
const luviRewardSampleBlocks = 600

type luviRewardEntry struct {
	asset   common.Asset
	reward  cosmos.Uint
	tracked bool
	pre     types.LuviState
}

type luviRewardCheck struct {
	entries   []luviRewardEntry
	total     cosmos.Uint
	asgardPre cosmos.Uint
}

// beginLuviRewardCheck captures the pre-payout state of every pool about to
// be paid. It returns nil (and reads nothing more) when the LUVIGuard Mimir
// is off or no pool is being paid.
func beginLuviRewardCheck(ctx cosmos.Context, k keeper.Keeper, rewards []cosmos.Uint, pools Pools) *luviRewardCheck {
	if len(rewards) == 0 || !luviGuardEnabled(ctx, k) {
		return nil
	}
	c := &luviRewardCheck{total: cosmos.ZeroUint()}
	for i, r := range rewards {
		if i >= len(pools) {
			break
		}
		e := luviRewardEntry{asset: pools[i].Asset, reward: r}
		if _, ok := keeper.LuviTrackedAsset(e.asset); ok && !r.IsZero() {
			e.pre = luviPoolState(ctx, k, e.asset)
			e.tracked = e.pre.IsValid()
		}
		c.entries = append(c.entries, e)
		c.total = c.total.Add(r)
	}
	c.asgardPre = k.GetRuneBalanceOfModule(ctx, AsgardName)
	return c
}

// end measures every paid pool against its pre-payout state and checks the
// totals against the rewards event, emitting failures (and periodic samples).
func (c *luviRewardCheck) end(ctx cosmos.Context, k keeper.Keeper, eventMgr EventManager, evt *types.EventRewards) {
	if c == nil {
		return
	}
	sample := ctx.BlockHeight()%luviRewardSampleBlocks == 0
	reported := cosmos.ZeroUint()
	if evt != nil {
		for _, pa := range evt.PoolRewards {
			if pa.Amount > 0 {
				reported = reported.Add(cosmos.NewUint(uint64(pa.Amount)))
			}
		}
	}

	var out []*types.EventLuviReward
	for _, e := range c.entries {
		if e.reward.IsZero() {
			continue
		}
		ev := &types.EventLuviReward{
			Pool:           e.asset,
			Kind:           "pool",
			RewardRune:     e.reward,
			ReportedRune:   luviReportedPoolReward(evt, e.asset),
			DeltaG:         cosmos.ZeroDec(),
			ExpectedDeltaG: cosmos.ZeroDec(),
			Ok:             true,
		}
		if e.tracked {
			m, err := types.MeasureLuviReward(e.pre, luviPoolState(ctx, k, e.asset), e.reward)
			if err != nil {
				ctx.Logger().Error("luvi: fail to measure pool reward", "pool", e.asset, "error", err)
				continue
			}
			ev.DeltaG = m.DeltaG
			ev.ExpectedDeltaG = m.ExpectedDeltaG
			ev.LpShareBps = m.LPShareBps
			ev.ExpectedLpShareBps = m.ExpectedLPShareBps
			if !m.OK {
				ev.Ok = false
				ev.Reason = "delta_g_mismatch"
			}
		}
		if ev.Ok && !ev.ReportedRune.Equal(e.reward) {
			ev.Ok = false
			ev.Reason = "event_pool_mismatch"
		}
		if !ev.Ok || (sample && e.tracked) {
			out = append(out, ev)
		}
	}

	asgardDelta := common.SafeSub(k.GetRuneBalanceOfModule(ctx, AsgardName), c.asgardPre)
	reason := ""
	switch {
	case !reported.Equal(c.total):
		reason = "event_total_mismatch"
	case !asgardDelta.Equal(c.total):
		reason = "asgard_balance_mismatch"
	}
	if reason != "" {
		out = append(out, &types.EventLuviReward{
			Kind:           "total",
			RewardRune:     c.total,
			ReportedRune:   reported,
			DeltaG:         cosmos.ZeroDec(),
			ExpectedDeltaG: cosmos.ZeroDec(),
			Reason:         reason,
		})
	}

	for _, ev := range out {
		if !ev.Ok {
			ctx.Logger().Error("luvi: pool reward allocation check failed",
				"pool", ev.Pool, "kind", ev.Kind, "reason", ev.Reason,
				"reward_rune", ev.RewardRune, "reported_rune", ev.ReportedRune,
				"delta_g", ev.DeltaG, "expected_delta_g", ev.ExpectedDeltaG)
		}
		if err := eventMgr.EmitEvent(ctx, ev); err != nil {
			ctx.Logger().Error("fail to emit luvi reward event", "error", err)
		}
	}
}

// luviReportedPoolReward returns the RUNE the rewards event reports for a pool.
func luviReportedPoolReward(evt *types.EventRewards, asset common.Asset) cosmos.Uint {
	total := cosmos.ZeroUint()
	if evt == nil {
		return total
	}
	for _, pa := range evt.PoolRewards {
		if pa.Asset.Equals(asset) && pa.Amount > 0 {
			total = total.Add(cosmos.NewUint(uint64(pa.Amount)))
		}
	}
	return total
}
