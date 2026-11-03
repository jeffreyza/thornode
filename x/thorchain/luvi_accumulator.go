package thorchain

import (
	"math/big"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// advanceLuviAccumulators steps every layer-1 pool's LUVI accumulator
// (types.PoolLuvi) to the pool's state at the end of the block. It runs last
// in EndBlock, so each step covers exactly one block. A pool with no valid
// state (empty, or without LP units) loses its accumulator; it starts again
// from zero once the pool is valid again.
func advanceLuviAccumulators(ctx cosmos.Context, k keeper.Keeper) {
	iterator := k.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			ctx.Logger().Error("luvi: fail to unmarshal pool", "error", err)
			continue
		}
		if _, ok := keeper.LuviTrackedAsset(pool.Asset); !ok || pool.Asset.IsSyntheticAsset() {
			continue
		}
		if err := advanceLuviAccumulator(ctx, k, pool); err != nil {
			ctx.Logger().Error("luvi: fail to advance accumulator", "pool", pool.Asset, "error", err)
		}
	}
}

func advanceLuviAccumulator(ctx cosmos.Context, k keeper.Keeper, pool Pool) error {
	state := types.NewLuviState(pool, k.GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset()))
	record, found, err := k.GetPoolLuviAccumulator(ctx, pool.Asset)
	if err != nil {
		return err
	}
	if !state.IsValid() {
		if found {
			k.RemovePoolLuviAccumulator(ctx, pool.Asset)
		}
		return nil
	}

	var tracker *types.LuviTracker
	prev := luviStateFromRecord(record)
	step := types.LuviStep{NormalizedDeltaG: cosmos.ZeroDec()}
	if found && prev.IsValid() {
		if luviStatesEqual(prev, state) {
			return nil
		}
		tracker, err = types.RestoreLuviTracker(prev, luviRaw(record.RawC), luviRaw(record.RawG))
		if err != nil {
			return err
		}
		step, err = tracker.Observe(state)
		if err != nil {
			return err
		}
	} else {
		tracker, err = types.NewLuviTracker(state)
		if err != nil {
			return err
		}
	}

	k.SetPoolLuviAccumulator(ctx, types.PoolLuvi{
		Asset:                pool.Asset,
		Height:               ctx.BlockHeight(),
		BalanceRune:          state.BalanceRune,
		BalanceAsset:         state.BalanceAsset,
		LpUnits:              state.LPUnits,
		SynthSupply:          state.SynthSupply,
		PoolUnits:            state.PoolUnits,
		RawC:                 cosmos.NewIntFromBigInt(tracker.RawC()),
		RawG:                 cosmos.NewIntFromBigInt(tracker.RawG()),
		LastNormalizedDeltaG: step.NormalizedDeltaG,
	})
	return nil
}

// luviStatesEqual compares two states; a nil value only equals a nil value.
func luviStatesEqual(a, b types.LuviState) bool {
	eq := func(x, y cosmos.Uint) bool {
		if x.IsNil() || y.IsNil() {
			return x.IsNil() == y.IsNil()
		}
		return x.Equal(y)
	}
	return eq(a.BalanceRune, b.BalanceRune) && eq(a.BalanceAsset, b.BalanceAsset) &&
		eq(a.LPUnits, b.LPUnits) && eq(a.SynthSupply, b.SynthSupply) && eq(a.PoolUnits, b.PoolUnits)
}

func luviRaw(i cosmos.Int) *big.Int {
	if i.IsNil() {
		return new(big.Int)
	}
	return i.BigInt()
}

func luviStateFromRecord(r types.PoolLuvi) types.LuviState {
	orZero := func(u cosmos.Uint) cosmos.Uint {
		if u.BigInt() == nil {
			return cosmos.ZeroUint()
		}
		return u
	}
	return types.LuviState{
		BalanceRune:  orZero(r.BalanceRune),
		BalanceAsset: orZero(r.BalanceAsset),
		LPUnits:      orZero(r.LpUnits),
		SynthSupply:  orZero(r.SynthSupply),
		PoolUnits:    orZero(r.PoolUnits),
	}
}

// setPoolLuviAccumulatorFields fills a pool response's LUVI accumulator
// fields, all as of the last accumulator step (luvi_height).
func setPoolLuviAccumulatorFields(ctx cosmos.Context, k keeper.Keeper, p *types.QueryPoolResponse, pool Pool) {
	// CosmWasm contracts query pools too: leave their response, and the gas
	// they pay for it, as it was before the accumulator existed
	if wasm, _ := ctx.Value(constants.CtxWASMQuery).(bool); wasm {
		return
	}
	record, found, err := k.GetPoolLuviAccumulator(ctx, pool.Asset)
	if err != nil || !found {
		return
	}
	prev := luviStateFromRecord(record)
	tracker, err := types.RestoreLuviTracker(prev, luviRaw(record.RawC), luviRaw(record.RawG))
	if err != nil {
		return
	}
	p.LuviG = tracker.G().String()
	p.LuviC = tracker.C().String()
	p.LuviLastNormalizedDeltaG = record.LastNormalizedDeltaG.String()
	p.LuviHeight = record.Height
	p.LuviStar = tracker.LuviStar().String()
}
