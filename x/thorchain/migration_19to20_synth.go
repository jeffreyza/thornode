//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// Retire the residual synths from a fixed set of pools whose synth
// percentage (synth_supply / 2·balance_asset) is below 1%. The entire synth
// supply of each is burned (reclaimed to the module and retired; nothing paid to holders), and
// the freed backing accrues to each pool's LPs on the next unit recompute. All
// of these pools have zero savers depth, so there are no saver records to settle.
// The list + contract addresses are pinned from on-chain state; re-verify at the
// release cut.
var lowSynthPctPools19to20 = []string{
	"BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D",
	"BSC.USDT-0X55D398326F99059FF775485246999027B3197955",
	"ETH.LUSD-0X5F98805A4E8BE255A32880FDEC7F6728C6568BA0",
	"ETH.USDP-0X8E870D67F660D95D5BE530380D0EC0BD388289E1",
	"ETH.WBTC-0X2260FAC5E5542A773AA44FBCFEDF7C193BC2C599",
	"ETH.XRUNE-0X69FA0FEE221AD11012BAB0FDB45D444D3D2CE71C",
	"ETH.YFI-0X0BC529C00C6401AEF6D220BE8C6EA1667F6AD93E",
}

// lowSynthMaxPctBps is the eligibility bound: a pool is retired only while its
// LIVE synth utilization (synth_supply in bps of 2× asset depth, the
// MaxSynthPerPoolDepth basis) is at or below this. Supply can grow between the
// pin and the upgrade via completed synth purchases, which leave no saver record
// or queued swap for synthLiability19to20 to detect, so a pool that has drifted
// above the bound is skipped for a re-pin rather than burning the newly minted,
// user-owned balance.
const lowSynthMaxPctBps = uint64(100) // 1%

// lowSynthSupplyDivergenceBps bounds how far the LIVE synth supply may exceed the
// pinned per-pool expectation before the burn is skipped (never burn more than
// pinned + tolerance). A smaller live supply, e.g. from a redemption, is burned
// as-is. One-directional, like the ETH.THOR capital tripwires.
const lowSynthSupplyDivergenceBps = uint64(100) // 1%

// lowSynthPinnedSupply19to20 is the expected synth supply per pool at the pin
// (verified on-chain: all 7 well below the 1% utilization bound, zero savers,
// zero queued swaps). Every entry in lowSynthPctPools19to20 MUST have one — a
// missing pin is a spec gap and fails loud. Re-pin at the release cut.
var lowSynthPinnedSupply19to20 = map[string]uint64{
	"BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D":  17_932_761_109,
	"BSC.USDT-0X55D398326F99059FF775485246999027B3197955":  9_340_596_826,
	"ETH.LUSD-0X5F98805A4E8BE255A32880FDEC7F6728C6568BA0":  4_795_311_801,
	"ETH.USDP-0X8E870D67F660D95D5BE530380D0EC0BD388289E1":  80_632_681,
	"ETH.WBTC-0X2260FAC5E5542A773AA44FBCFEDF7C193BC2C599":  9_121_528,
	"ETH.XRUNE-0X69FA0FEE221AD11012BAB0FDB45D444D3D2CE71C": 22_833_812_240_118,
	"ETH.YFI-0X0BC529C00C6401AEF6D220BE8C6EA1667F6AD93E":   67_136,
}

// runLowSynthBurn19to20 retires each eligible pool's synths. Hard errors (parse / missing
// pin / GetPool / burn / SetPool) discard the whole step's cache; per-pool drift (liability
// / non-Available / ragnarok-marker / utilization / supply-ceiling) soft-skips THAT pool
// while the committed burns for the rest still land. Each pool's burn is independent (no
// cross-pool invariant), so a partial set is correct, not a half-applied operation.
func runLowSynthBurn19to20(ctx cosmos.Context, mgr *Mgrs) {
	cacheCtx, commit := ctx.CacheContext()
	if err := applyLowSynthBurn19to20(cacheCtx, mgr); err != nil {
		ctx.Logger().Error("low-synth retirement skipped", "error", err)
		return
	}
	commit()
}

func applyLowSynthBurn19to20(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()
	for _, s := range lowSynthPctPools19to20 {
		asset, err := common.NewAsset(s)
		if err != nil {
			return fmt.Errorf("parse %s: %w", s, err)
		}
		synthAsset := asset.GetSyntheticAsset()
		// Never burn a pool that still carries an outstanding synth claim: a
		// saver record or a queued swap sourced-from / targeted-at the synth
		// would be left with its backing destroyed. All 7 are verified clear at
		// pin time; this skips any that drifted before the upgrade.
		if reason, has := synthLiability19to20(ctx, k, asset); has {
			ctx.Logger().Error("low-synth retirement skipped: pool has an outstanding synth claim",
				"asset", asset, "reason", reason)
			continue
		}
		// Only retire pools still within the pinned below-1% scope. Both the live
		// utilization AND the live supply are bounded: a pool that has drifted above
		// the eligibility bound, or whose supply has grown beyond the pinned ceiling
		// (a completed purchase leaves no saver/swap trace), is skipped for a re-pin
		// rather than burning user-owned balance beyond the stated retirement scope.
		pinned, ok := lowSynthPinnedSupply19to20[s]
		if !ok {
			return fmt.Errorf("no pinned supply for %s (spec gap)", s)
		}
		pool, err := k.GetPool(ctx, asset)
		if err != nil {
			return fmt.Errorf("get pool %s before burn: %w", asset, err)
		}
		// Only retire synths of a pool that is Available. A non-Available pool
		// (e.g. Staged ETH.YFI) is excluded by policy — burning a staged/suspended
		// pool's supply would retire balances outside the stated below-1%
		// Available-pool scope. Status is a live check, re-verified at the upgrade.
		if pool.Status != PoolAvailable {
			ctx.Logger().Info("low-synth retirement skipped: pool not Available (status-gated)",
				"asset", asset, "status", pool.Status)
			continue
		}
		// GetTotalSupply reads zero while a pool carries a ragnarok marker
		// (GetPoolRagnarokStart > 0), but BurnEntireSynthSupply destroys the real
		// bank balances — so the supply + utilization guards below would pass against
		// zero and a pool whose actual supply exceeds the pinned ceiling could still
		// be burned. A pool can be Available yet still carry the marker (cyclePools
		// can promote a Staged pool whose ragnarok was cancelled mid-flight). Skip it:
		// the supply cannot be validated against the pinned scope.
		if ragStart, _ := k.GetPoolRagnarokStart(ctx, asset); ragStart > 0 {
			ctx.Logger().Info("low-synth retirement skipped: pool carries a ragnarok marker (supply unreadable)",
				"asset", asset, "ragnarok_start", ragStart)
			continue
		}
		if pool.BalanceAsset.IsZero() {
			ctx.Logger().Error("low-synth retirement skipped: pool has no asset depth", "asset", asset)
			continue
		}
		supply := k.GetTotalSupply(ctx, synthAsset)
		// Exact comparison, no truncation: skip if synth_supply / (2·balance_asset) strictly
		// exceeds the bound. Dividing first (Quo) FLOORS the bps, so a live utilization in
		// (bound, bound+1) bps — e.g. 100.7 — would floor to the bound and wrongly PASS,
		// burning a pool above the stated 1%. Cross-multiply instead:
		// supply·10000 > 2·balance_asset·bound.
		if supply.MulUint64(10_000).GT(pool.BalanceAsset.MulUint64(2).MulUint64(lowSynthMaxPctBps)) {
			ctx.Logger().Error("low-synth retirement skipped: synth utilization above eligibility bound (drifted)",
				"asset", asset, "supply", supply, "two_asset_depth", pool.BalanceAsset.MulUint64(2), "bound_bps", lowSynthMaxPctBps)
			continue
		}
		pinnedCeil := cosmos.NewUint(pinned)
		pinnedCeil = pinnedCeil.Add(pinnedCeil.MulUint64(lowSynthSupplyDivergenceBps).QuoUint64(10_000))
		if supply.GT(pinnedCeil) {
			ctx.Logger().Error("low-synth retirement skipped: live supply above pinned ceiling (drifted)",
				"asset", asset, "supply", supply, "pinned", pinned)
			continue
		}
		burned, err := k.BurnEntireSynthSupply(ctx, synthAsset)
		if err != nil {
			return fmt.Errorf("burn %s synth: %w", synthAsset, err)
		}
		if burned.IsZero() {
			continue
		}
		// The synth coins are gone; clear the pool's synth units so
		// GetPoolUnits() does not count phantom synth units until the next
		// CalcUnits. A GetPool error here must abort (return) — the burn is
		// already written to this step's cache, so swallowing the error would
		// leave the pool with stale synth units against a burned supply.
		p, perr := k.GetPool(ctx, asset)
		if perr != nil {
			return fmt.Errorf("get pool %s after burn: %w", asset, perr)
		}
		if !p.SynthUnits.IsZero() {
			p.SynthUnits = cosmos.ZeroUint()
			if serr := k.SetPool(ctx, p); serr != nil {
				return fmt.Errorf("zero synth units %s: %w", asset, serr)
			}
		}
		evt := NewEventMintBurn(BurnSupplyType, synthAsset.Native(), burned, "retire_low_synth_pool")
		if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("emit synth retire event", "error", err, "asset", synthAsset)
		}
		ctx.Logger().Info("retired low-synth-pool synths", "asset", asset, "burned", burned)
	}
	return nil
}

// synthLiability19to20 reports whether a pool's synthetic asset still has an
// outstanding claim that burning its supply would strand: a saver record (a
// liquidity provider on the synth asset), or a queued swap (base or advanced)
// sourced from or targeted at the synth.
func synthLiability19to20(ctx cosmos.Context, k keeper.Keeper, asset common.Asset) (string, bool) {
	synthAsset := asset.GetSyntheticAsset()

	savers := k.GetLiquidityProviderIterator(ctx, synthAsset)
	defer savers.Close()
	if savers.Valid() {
		return "saver record present", true
	}

	touchesSynth := func(msg MsgSwap) bool {
		if msg.TargetAsset.Equals(synthAsset) {
			return true
		}
		for _, c := range msg.Tx.Coins {
			if c.Asset.Equals(synthAsset) {
				return true
			}
		}
		return false
	}

	// An unparsable queue entry is treated as a liability (fail-safe): we cannot
	// prove it does not touch this synth, so we must not burn its backing.
	sq := k.GetSwapQueueIterator(ctx)
	defer sq.Close()
	for ; sq.Valid(); sq.Next() {
		var msg MsgSwap
		if err := k.Cdc().Unmarshal(sq.Value(), &msg); err != nil {
			return "unreadable queued swap", true
		}
		if touchesSynth(msg) {
			return "queued swap", true
		}
	}

	aq := k.GetAdvSwapQueueItemIterator(ctx)
	defer aq.Close()
	for ; aq.Valid(); aq.Next() {
		var msg MsgSwap
		if err := k.Cdc().Unmarshal(aq.Value(), &msg); err != nil {
			return "unreadable queued advanced swap", true
		}
		if touchesSynth(msg) {
			return "queued advanced swap", true
		}
	}
	return "", false
}
