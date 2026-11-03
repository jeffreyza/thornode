//go:build mainnet

package thorchain

import (
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ETH.THOR restore — a SEPARATE migration step so a drift-skip here does not
// drop the BSC/BASE donations or the NDT repayment.
//
// It (1) donates our Secured-Ops ETH.THOR LP units to the pool, (2) credits the
// unaccounted Asgard-held ETH.THOR surplus into the pool paired with Reserve RUNE
// at the reference THOR/USD price (so the pool lands at ~reference depth and
// price), and (3) repays New Dawn Treasury the on-chain RUNE capital of the
// position. All pinned amounts are re-pinned at the release cut.
const (
	// ethThorSurplus is the free Asgard-held ETH.THOR at the snapshot: the live
	// calculateNetworkSolvency amount for the asset — net of pool, pending inbound,
	// trade/secured, queued/streaming swaps, deficits and scheduled outbounds — the
	// exact figure the credit guard (unaccounted.LT(surplus)) checks. Re-pin at the
	// release cut FROM THAT LIVE FIGURE, not from asgard−pool−trade−secured (which
	// omits the other liabilities and would over-pin → the restore soft-skips).
	ethThorSurplus = uint64(395_937_359_950_060) // 3,959,373.5995 THOR
	// ethThorPinnedRuneTor is the pinned ETH.THOR price as a RUNE/THOR ratio (RUNE per THOR,
	// in 1e8). The restored pool is priced to this directly — no THOR/USD + live-RUNE/USD
	// indirection, so a swing in RUNE's USD price between the pin and the upgrade can no
	// longer move how much Reserve RUNE is paired. This is a FIXED recovery price,
	// 0.95 × 0.073909 (a 5% haircut on the 0.073909 ETH.THOR reference), set deliberately
	// above the current market; the Reserve outlay is bounded by the ethThorMaxRunePairing
	// cap below, NOT by a live-vs-pin price band. Re-confirm at the release cut.
	ethThorPinnedRuneTor = uint64(7_021_355) // 0.07021355 RUNE/THOR = 0.073909 × 0.95
	// ethThorRefundToNDT is the on-chain RUNE capital of the donated position
	// (rune_deposit_value, net of the earlier withdraw).
	ethThorRefundToNDT = uint64(16_334_481_856_806) // 163,344.81856806 RUNE
	ethThorNDTAddr     = "thor1lhufh0mwasa0lk9udppdegmvnkgqt08f0m9p5g"
	ethThorSecuredOps  = "thor1gkvvzfnkyqv8dfhk0593tfhgr03a3ndgjqdalf"
	ethThorAssetStr    = "ETH.THOR-0XA5F2211B9B8170F694421F2046281775E8468044"

	// Cap every later LP add at the RUNE VALUE of min(what it contributed, what it
	// could have withdrawn pre-upgrade) at the final pool price (see capLateLPs19to20),
	// so the donation+surplus value accrues to the earlier LPs rather than to a party
	// that entered later — while a two-sided LP keeps its own RUNE (an asset-only cap
	// stripped it). Per on-chain data the only later LP (besides our donated Secured-Ops
	// position) is thor1aff7, which first added at h27,693,576 (after h27,580,000).
	capPostDrainAdds        = true
	postDrainLPAddr         = "thor1aff7psteahja6lw9cwjsxt0uxq9pvtfx3mxr6r"
	postDrainLPContribAsset = uint64(15_757_968_052_103) // 157,579.68 THOR (asset_deposit_value)

	// ethThorSnapshotHeight is the height the pinned amounts were derived at;
	// an LP that first added after it (other than the known capped entrant) is
	// unexpected state and aborts the step for a re-pin.
	ethThorSnapshotHeight = int64(27_580_000)
	// ethThorDivergenceBps bounds how far the LIVE on-chain capital may exceed
	// the pinned expectation before the step aborts (never over-pay / over-cap);
	// a smaller live value, e.g. from a pre-upgrade withdrawal, is used as-is.
	ethThorDivergenceBps = uint64(100) // 1%

	// ethThorMaxRunePairing hard-caps the Reserve RUNE paired into ETH.THOR, bounding the
	// huginn "Reserve RUNE paired has no bound" concern (0c3a3071d1) DIRECTLY and
	// independently of the live pool price. The restore prices the post-surplus pool to the
	// FIXED ethThorPinnedRuneTor, set deliberately above the current market, so a live-vs-pin
	// price band is the wrong guard: it cannot tell an honest below-pin market from a pool
	// whose BalanceAsset was inflated to pull more Reserve RUNE. The cap bounds the outlay
	// regardless — runeToAdd = finalAsset×pin − poolRUNE, so inflating BalanceAsset (e.g.
	// selling THOR into the pool) only RAISES runeToAdd until it reaches this ceiling, at which
	// point the pairing is CLAMPED here (not aborted): the restore still proceeds, the pool
	// lands below the pinned price, and the Reserve pays at most the cap. Expected runeToAdd at
	// the pin is ≈337,851 RUNE; this 375,000 ceiling leaves ≈37k headroom (≈1.11×) for an
	// honest pool-depth increase between the cut and the upgrade, and bounds any manipulation
	// gain to ≈37k above the honest pairing. The one-directional 1% capital tripwires above
	// (ethThorDivergenceBps) are the defence-in-depth second line. Re-tune at the release cut.
	ethThorMaxRunePairing = uint64(37_500_000_000_000) // 375,000 RUNE (1e8)
)

// runEthThorRestore19to20 applies the ETH.THOR restore atomically or not at all.
func runEthThorRestore19to20(ctx cosmos.Context, mgr *Mgrs) {
	cacheCtx, commit := ctx.CacheContext()
	if err := applyEthThorRestore19to20(cacheCtx, mgr); err != nil {
		ctx.Logger().Error("ETH.THOR restore skipped: state does not match the spec", "error", err)
		return
	}
	commit()
}

func applyEthThorRestore19to20(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()
	asset, err := common.NewAsset(ethThorAssetStr)
	if err != nil {
		return fmt.Errorf("parse ETH.THOR: %w", err)
	}
	surplus := cosmos.NewUint(ethThorSurplus)

	// (1) Donate our Secured-Ops LP units (pool asset/RUNE stay; value accrues to
	// the remaining LPs).
	lp, err := k.GetLiquidityProvider(ctx, asset, common.Address(ethThorSecuredOps))
	if err != nil {
		return fmt.Errorf("get secured-ops LP: %w", err)
	}
	if lp.Units.IsZero() {
		return fmt.Errorf("secured-ops LP has no units")
	}
	if !lp.PendingRune.IsZero() || !lp.PendingAsset.IsZero() {
		return fmt.Errorf("secured-ops LP has pending liquidity")
	}
	pool, err := k.GetPool(ctx, asset)
	if err != nil {
		return fmt.Errorf("get ETH.THOR pool: %w", err)
	}
	// The late-LP value cap uses pool.LPUnits alone as the redemption denominator. Live
	// redemption divides by GetPoolUnits() = LPUnits + SynthUnits, so if ETH.THOR ever
	// carried synths the cap would use the wrong base. ETH.THOR has zero synth supply (it is
	// not in the low-synth wind-down) and LP/synth activity is paused, so this holds today;
	// assert it so a re-pin cut that somehow lands with synths present soft-skips rather than
	// silently mis-capping. See the sibling donatedRuneValue (migration_19to20.go), which
	// uses CalcUnits for exactly this reason.
	if synthSupply := k.GetTotalSupply(ctx, asset.GetSyntheticAsset()); !synthSupply.IsZero() {
		return fmt.Errorf("ETH.THOR carries synth supply %s; the late-LP cap denominator assumption is broken (re-pin)", synthSupply)
	}
	// Pre-migration pool depths (before the donation removes units and before the
	// surplus credit): a late LP's value cap needs its pre-upgrade redeemable
	// position, which is its share of this LIVE pool.
	preAsset, preRune, preUnits := pool.BalanceAsset, pool.BalanceRune, pool.LPUnits
	if pool.LPUnits.LT(lp.Units) {
		return fmt.Errorf("pool units %s < LP units %s", pool.LPUnits, lp.Units)
	}
	pool.LPUnits = pool.LPUnits.Sub(lp.Units)
	if pool.LPUnits.IsZero() {
		// The donated position was the only LP; crediting surplus into a pool
		// with no remaining owners has no beneficiaries and leaves later unit
		// math dividing by zero. Abort for a re-pin.
		return fmt.Errorf("secured-ops donation would leave the pool with no LP units")
	}
	// Emit a withdraw event for the donated units so Midgard subtracts them from the
	// pool's unit accounting and the member's position — SetPool + RemoveLiquidityProvider
	// change ownership silently. Full removal (10000 bps), no payout (the value accrues to
	// the remaining LPs via the surplus credit). Fields mirror PoolMgr.removeLiquidityProviders.
	withdrawEvt := NewEventWithdraw(asset, lp.Units, int64(10000), cosmos.ZeroDec(), common.Tx{
		ID:          common.BlankTxID,
		FromAddress: lp.GetAddress(),
		ToAddress:   common.NoAddress,
		Coins:       common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())),
		Chain:       common.THORChain,
	}, cosmos.ZeroUint(), cosmos.ZeroUint())
	if emitErr := mgr.EventMgr().EmitEvent(ctx, withdrawEvt); emitErr != nil {
		return fmt.Errorf("emit secured-ops withdraw event: %w", emitErr)
	}
	k.RemoveLiquidityProvider(ctx, lp)

	// (2) Credit the unaccounted Asgard surplus, solvency-guarded. The surplus is
	// the live network-solvency amount for the asset (Asgard-held net of the pool,
	// trade/secured, queued/streaming swaps, deficit, and scheduled outbounds), so
	// the credit cannot make any of those insolvent — see unaccountedVaultAsset19to20.
	unaccounted, err := unaccountedVaultAsset19to20(ctx, mgr, asset)
	if err != nil {
		return err
	}
	if unaccounted.LT(surplus) {
		return fmt.Errorf("unaccounted ETH.THOR %s below pinned surplus %s (trade/secured drifted)", unaccounted, surplus)
	}

	// Price the restored pool at the pinned ETH.THOR RUNE/THOR ratio — directly, with no
	// THOR/USD + live-RUNE/USD indirection, so RUNE's own USD price can no longer move the
	// Reserve pairing.
	if pool.BalanceAsset.IsZero() {
		return fmt.Errorf("ETH.THOR pool has zero asset depth")
	}
	const oneE8 = uint64(100_000_000)
	pinnedRuneTor := cosmos.NewUint(ethThorPinnedRuneTor)
	finalAsset := pool.BalanceAsset.Add(surplus)
	// targetRune = finalAsset (THOR) × pinned RUNE/THOR, integer math in 1e8 units.
	targetRune := finalAsset.Mul(pinnedRuneTor).QuoUint64(oneE8)
	if targetRune.LTE(pool.BalanceRune) {
		return fmt.Errorf("target RUNE depth %s <= current %s (RUNE too strong); skip", targetRune, pool.BalanceRune)
	}
	runeToAdd := targetRune.Sub(pool.BalanceRune)
	// Hard-cap the Reserve pairing (huginn 0c3a3071d1). The pin is a fixed recovery price
	// above market, so there is no live-price band; instead bound the outlay directly. A pool
	// whose BalanceAsset was inflated to pull more Reserve RUNE only pushes runeToAdd up, so
	// above the ceiling we CLAMP (not abort): the restore still proceeds — the pool lands
	// below the pinned price (under-restored) — but the Reserve outlay is hard-bounded at the
	// cap, which is exactly what "Reserve RUNE paired has no bound" requires. A manipulated or
	// grown pool thus pulls at most the cap, never an unbounded amount.
	maxPair := cosmos.NewUint(ethThorMaxRunePairing)
	if runeToAdd.GT(maxPair) {
		ctx.Logger().Info("ETH.THOR pairing RUNE clamped to cap", "uncapped", runeToAdd.String(), "cap", ethThorMaxRunePairing)
		runeToAdd = maxPair
	}

	// Repay the LIVE on-chain RUNE capital of the donated position, not a pinned
	// amount: a pre-upgrade withdrawal lowers RuneDepositValue and we must not
	// over-pay. Abort only if it has grown beyond the pinned expectation by more
	// than the divergence tolerance (unexpected state → re-pin).
	refund := lp.RuneDepositValue
	if refund.IsZero() {
		return fmt.Errorf("secured-ops LP has zero rune deposit value")
	}
	pinnedRefund := cosmos.NewUint(ethThorRefundToNDT)
	refundCeil := pinnedRefund.Add(pinnedRefund.MulUint64(ethThorDivergenceBps).QuoUint64(10_000))
	if refund.GT(refundCeil) {
		return fmt.Errorf("live NDT capital %s exceeds pinned %s beyond tolerance (state drifted); skip", refund, pinnedRefund)
	}

	// Reserve must cover the pairing RUNE plus the capital refund.
	need := runeToAdd.Add(refund)
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(need) {
		return fmt.Errorf("reserve %s below ETH.THOR restore need %s", bal, need)
	}
	pairCoin := common.NewCoin(common.RuneAsset(), runeToAdd)
	if sendErr := k.SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(pairCoin)); sendErr != nil {
		return fmt.Errorf("reserve->asgard pairing RUNE: %w", sendErr)
	}
	pool.BalanceAsset = pool.BalanceAsset.Add(surplus)
	pool.BalanceRune = pool.BalanceRune.Add(runeToAdd)
	if poolErr := k.SetPool(ctx, pool); poolErr != nil {
		return fmt.Errorf("set ETH.THOR pool: %w", poolErr)
	}
	// Emit the depth change so Midgard (event-sourced) tracks the credited surplus
	// + paired RUNE, matching the slash step's event. A skipped step's cache is
	// discarded, so no event leaks.
	emitPoolBalanceChangedEvent(ctx, NewPoolMod(asset, runeToAdd, true, surplus, true), "eth_thor_restore_19to20", mgr)

	// (2b) cap every post-snapshot LP add so the donated + surplus value accrues to
	// the pre-snapshot LPs, not to a late entrant. A late add is CAPPED, not fatal
	// (R11): a brand-new add is held to the VALUE of min(what it contributed, what
	// it could have withdrawn pre-upgrade) — so a two-sided entrant keeps its own
	// RUNE contribution and is not stripped below its real position, while still
	// taking no windfall from the donation/surplus; a pinned pre-snapshot LP that
	// topped up keeps its snapshot units. The restore hard-aborts only on real
	// inconsistency (impossible cap math / drifted pin).
	if capPostDrainAdds {
		if capErr := capLateLPs19to20(ctx, mgr, asset, ethThorPinnedLateLP, preAsset, preRune, preUnits); capErr != nil {
			return fmt.Errorf("cap late LPs: %w", capErr)
		}
	}

	// (3) Repay New Dawn Treasury the position's RUNE capital.
	ndt, err := cosmos.AccAddressFromBech32(ethThorNDTAddr)
	if err != nil {
		return fmt.Errorf("parse NDT address: %w", err)
	}
	if err := k.SendFromModuleToAccount(ctx, ReserveName, ndt, common.NewCoins(common.NewCoin(common.RuneAsset(), refund))); err != nil {
		return fmt.Errorf("repay NDT: %w", err)
	}
	ctx.Logger().Info("ETH.THOR restored",
		"donated_units", lp.Units, "surplus_credited", surplus,
		"rune_paired", runeToAdd, "ndt_refund", refund, "pinned_rune_tor", ethThorPinnedRuneTor)
	return nil
}

// ethThorLatePin pins a known post-snapshot ETH.THOR LP. floorUnits is the
// pre-snapshot units to preserve (0 = a brand-new entrant with no pre-snapshot
// position); contribCeil is the expected contributed asset for the drift tripwire
// (0 = unpinned, no tripwire).
//
// LIMITATION — the floorUnits path DONATES the top-up, it does not refund it:
// every unit above floorUnits (i.e. whatever a pre-snapshot LP minted by topping
// up after the snapshot) is removed and its backing stays in the pool, accruing to
// the remaining LPs; AssetDepositValue/RuneDepositValue are NOT reduced. This is
// deliberate and differs from the brand-new (floorUnits==0) path, which value-caps
// so the entrant keeps its own deposit. It only bites at a re-pin cut: pin floorUnits
// ONLY for an LP whose post-snapshot top-up should be donated. For a topped-up LP
// that should instead KEEP its later deposit, pin it as a brand-new entrant
// (floorUnits 0, contribCeil = its contributed asset) so the value cap applies.
type ethThorLatePin struct {
	floorUnits  uint64
	contribCeil uint64
}

// ethThorPinnedLateLP pins the known LPs that added after the snapshot. Re-pin at
// the release cut from the snapshot-height LP query for every LP with
// LastAddHeight > ethThorSnapshotHeight. Today only thor1aff7 qualifies (brand-new,
// first add h27,693,576 — zero pre-snapshot units, so floor 0); no pre-snapshot LP
// has topped up, and LP deposits are paused on ETH.THOR operationally as well.
var ethThorPinnedLateLP = map[string]ethThorLatePin{
	postDrainLPAddr: {floorUnits: 0, contribCeil: postDrainLPContribAsset},
}

// capLateLPs19to20 caps every LP that added after the snapshot so it cannot take an
// uncapped share of the donated + surplus value. This REPLACES the former
// abort-on-any-late-LP guard (R11): a known late add is capped, never fatal.
//
// The pinned map is the COMPLETE classification of every LP with LastAddHeight >
// snapshot (sealed operationally by the ETH.THOR LP-deposit pause, re-pinned at the
// cut from the snapshot-height LP query):
//   - floorUnits>0: a pre-snapshot LP that topped up → capped to its pinned snapshot
//     units, preserving its pre-snapshot position. Everything above the floor (the
//     post-snapshot top-up) is DONATED, not refunded: those units are removed and
//     their backing stays in the pool for the remaining LPs, and the record's
//     deposit values are left unchanged. Only pin this path for an LP whose top-up
//     should be donated; a topped-up LP that should keep its later deposit is pinned
//     as a brand-new entrant (floorUnits 0) instead. Matters only at a re-pin cut.
//   - floorUnits==0: a brand-new entrant → capped to the VALUE of min(what it
//     contributed, what it could have withdrawn pre-upgrade), in RUNE at the final
//     pool price. Capping on contributed ASSET alone stripped a two-sided LP's RUNE
//     contribution (it got its asset back but its RUNE share fell out at the restored,
//     RUNE-cheap price); the value cap lets such an LP keep its own RUNE while still
//     taking no windfall from the donation/surplus.
//   - UNPINNED late LP → the pin is incomplete (a real inconsistency): abort for a
//     re-pin rather than risk stripping a pre-snapshot owner or over-crediting a new one.
//
// Brand-new caps are solved JOINTLY against the FINAL pool units: each cap removes
// units, lowering the denominator and raising every other claim, so they are solved in
// closed form over the binding set (see below). A post-condition then verifies
// no capped entrant's final VALUE claim exceeds its target. All reads/sorts are
// address-ordered so the result is independent of KVStore iteration order. preAsset/
// preRune/preUnits are the LIVE pre-migration pool depths (before the donation and the
// surplus credit), used for the pre-upgrade-redeem leg of the value cap. The pinned
// map is a parameter so tests can exercise each path (production passes ethThorPinnedLateLP).
func capLateLPs19to20(ctx cosmos.Context, mgr *Mgrs, asset common.Asset, pinned map[string]ethThorLatePin, preAsset, preRune, preUnits cosmos.Uint) error {
	k := mgr.Keeper()
	pool, err := k.GetPool(ctx, asset)
	if err != nil {
		return fmt.Errorf("get pool: %w", err)
	}
	// Final (post-credit) pool depths. Capping removes units, not balances, so these
	// are stable across the joint solve. A symmetric position of u units out of L total
	// redeems to (poolAsset*u/L) asset + (poolRune*u/L) rune, worth 2*poolRune*u/L in
	// RUNE (asset side priced at poolRune/poolAsset).
	poolAsset := pool.BalanceAsset
	poolRune := pool.BalanceRune
	if poolAsset.IsZero() {
		return fmt.Errorf("pool asset depth zero")
	}
	if preUnits.IsZero() {
		return fmt.Errorf("pre-migration pool units zero")
	}
	lpUnits := pool.LPUnits // running pool LP units as caps are applied

	type lateLP struct {
		lp        LiquidityProvider
		addr      string
		origUnits cosmos.Uint // units before any cap, for the withdraw-event delta
		target    cosmos.Uint // value cap in RUNE: min(contributed, pre-upgrade redeem) at final price
		brandNew  bool
	}
	var late []lateLP
	iter := k.GetLiquidityProviderIterator(ctx, asset)
	for ; iter.Valid(); iter.Next() {
		var lp LiquidityProvider
		if err := k.Cdc().Unmarshal(iter.Value(), &lp); err != nil {
			iter.Close()
			return fmt.Errorf("unmarshal LP: %w", err)
		}
		// A zero-unit record owns no pool units (e.g. the first leg of a staged
		// symmetric deposit); it cannot take a share, so it is not a late add to cap.
		if lp.Units.IsZero() || lp.LastAddHeight <= ethThorSnapshotHeight {
			continue
		}
		late = append(late, lateLP{lp: lp, addr: lp.GetAddress().String(), origUnits: lp.Units})
	}
	iter.Close()
	sort.Slice(late, func(i, j int) bool { return late[i].addr < late[j].addr })

	// Classify + apply the denominator-independent floor caps immediately.
	for i := range late {
		pin, ok := pinned[late[i].addr]
		if !ok {
			return fmt.Errorf("late LP %s not in pinned set (re-pin required)", late[i].addr)
		}
		if pin.floorUnits > 0 {
			floor := cosmos.NewUint(pin.floorUnits)
			if late[i].lp.Units.GT(floor) {
				removed := late[i].lp.Units.Sub(floor)
				// Guard the running decrement like the donation path (migration_19to20.go):
				// if the removal exceeds the running pool units, SafeSub would CLAMP to 0 and
				// silently corrupt pool.LPUnits (zero units against live owners + depth). Abort
				// for a re-pin instead. Only reachable at a floorUnits>0 re-pin with an already
				// under-counted pool; fail-safe (cache discarded).
				if lpUnits.LT(removed) {
					return fmt.Errorf("floor cap of %s (%s over floor) exceeds running pool LP units %s (re-pin)", late[i].addr, removed, lpUnits)
				}
				lpUnits = lpUnits.Sub(removed)
				late[i].lp.Units = floor
			}
			continue
		}
		// Brand-new entrant: cap on VALUE so a two-sided LP keeps its own RUNE. A RUNE-only
		// asymmetric add (zero asset deposit value) is a VALID position, not drift, so only
		// a record with NO basis at all (both deposit values zero) aborts.
		contribAsset := late[i].lp.AssetDepositValue
		if contribAsset.IsZero() && late[i].lp.RuneDepositValue.IsZero() {
			return fmt.Errorf("late LP %s has zero asset and rune deposit value", late[i].addr)
		}
		if pin.contribCeil > 0 {
			ceil := cosmos.NewUint(pin.contribCeil)
			ceil = ceil.Add(ceil.MulUint64(ethThorDivergenceBps).QuoUint64(10_000))
			if contribAsset.GT(ceil) {
				return fmt.Errorf("late LP %s contributed %s exceeds pinned %d beyond tolerance (drifted)", late[i].addr, contribAsset, pin.contribCeil)
			}
		}
		// contributed value (RUNE) = assetDeposit * poolRune/poolAsset + runeDeposit.
		contribVal := contribAsset.Mul(poolRune).Quo(poolAsset).Add(late[i].lp.RuneDepositValue)
		// pre-upgrade redeemable value (RUNE): this LP's share of the LIVE pre-migration
		// pool, priced at the final pool price.
		u0 := late[i].lp.Units
		preShareAsset := preAsset.Mul(u0).Quo(preUnits)
		preShareRune := preRune.Mul(u0).Quo(preUnits)
		beforeVal := preShareAsset.Mul(poolRune).Quo(poolAsset).Add(preShareRune)
		target := contribVal
		if beforeVal.LT(target) {
			target = beforeVal
		}
		if target.IsZero() {
			return fmt.Errorf("late LP %s has zero cap target", late[i].addr)
		}
		// A single position cannot be worth more than the whole doubled pool (2*poolRune).
		if target.GTE(poolRune.MulUint64(2)) {
			return fmt.Errorf("late LP %s cap target %s >= doubled pool rune (inconsistent)", late[i].addr, target)
		}
		late[i].target = target
		late[i].brandNew = true
	}

	// Brand-new value caps, solved jointly in closed form. A position worth `target`
	// RUNE at the final price holds u' units where 2*poolRune*u'/L = target (L = final
	// pool units). If a set S of entrants all bind, each u'_i = target_i*K/(2R - Σ_S
	// target), where K is the pool units owned by everyone NOT in S — then L =
	// K*2R/(2R - Σ_S target) and claim_i = target_i exactly. We only ever REDUCE, so an
	// entrant whose current units are already ≤ its solved u'_i does not bind; drop it
	// (its units stay, counting toward K) and resolve. Each round drops ≥1, so it
	// settles in ≤ len(late) rounds — no iteration-to-fixpoint, no convergence bound.
	// NOTE: dropping ≥1 per round is NOT proven to keep every claim ≤ target when
	// MULTIPLE entrants drop in the SAME round — the recomputed K/denom is a mediant of
	// the old ratio and the dropped entrants' ratios, so a simultaneously-dropped entrant
	// can land above its target. That path is fail-safe (the post-condition below aborts
	// the whole restore, never over-crediting) and is not reached in the ETH.THOR restore:
	// there R'>R and A'≥A, so every entrant's current claim already ≥ its target and
	// nothing is dropped. (If the drop path ever had to be correct in general, drop the
	// single smallest-u/t entrant per round.)
	twoR := poolRune.MulUint64(2)
	binding := make([]int, 0, len(late))
	for i := range late {
		if late[i].brandNew {
			binding = append(binding, i)
		}
	}
	for len(binding) > 0 {
		sumT := cosmos.ZeroUint()
		sumU := cosmos.ZeroUint()
		for _, i := range binding {
			sumT = sumT.Add(late[i].target)
			sumU = sumU.Add(late[i].lp.Units)
		}
		if twoR.LTE(sumT) {
			return fmt.Errorf("late LP value targets %s >= doubled pool rune %s (inconsistent)", sumT, twoR)
		}
		if lpUnits.LT(sumU) {
			return fmt.Errorf("binding late units %s exceed pool units %s (inconsistent)", sumU, lpUnits)
		}
		denom := twoR.Sub(sumT)
		k := lpUnits.Sub(sumU) // units owned by non-binding LPs (incl. already-dropped entrants)
		if k.IsZero() {
			return fmt.Errorf("no non-late pool units remain; cannot cap late LPs")
		}
		kept := binding[:0:0]
		dropped := false
		for _, i := range binding {
			if late[i].target.Mul(k).Quo(denom).GTE(late[i].lp.Units) {
				dropped = true // solved cap ≥ current → would not reduce → does not bind
				continue
			}
			kept = append(kept, i)
		}
		if !dropped {
			for _, i := range binding {
				newUnits := late[i].target.Mul(k).Quo(denom)
				lpUnits = lpUnits.Sub(late[i].lp.Units.Sub(newUnits))
				late[i].lp.Units = newUnits
			}
			break
		}
		binding = kept
	}

	// Post-condition: no capped entrant's final value claim exceeds its target, within
	// an integer-rounding tolerance. Each entrant's units are floored independently, so
	// the shared final denominator L is up to nBrandNew units below the exact solve;
	// that can nudge another entrant's integer claim a hair above its target (a pure
	// rounding artifact, bounded by ~2R*nBrandNew/L). Allow that bound so a valid
	// asymmetric multi-entrant restore is not wrongly rejected; it still catches any
	// gross over-credit (the asset-cap bug this guards against).
	nBrandNew := uint64(0)
	for i := range late {
		if late[i].brandNew {
			nBrandNew++
		}
	}
	roundTol := cosmos.ZeroUint()
	if !lpUnits.IsZero() && nBrandNew > 0 {
		roundTol = twoR.MulUint64(nBrandNew).Quo(lpUnits).AddUint64(1)
	}
	for i := range late {
		if !late[i].brandNew {
			continue
		}
		if lpUnits.IsZero() {
			return fmt.Errorf("pool LP units zero after cap")
		}
		if claim := twoR.Mul(late[i].lp.Units).Quo(lpUnits); claim.GT(late[i].target.Add(roundTol)) {
			return fmt.Errorf("late LP %s value claim %s exceeds target %s (tol %s) after cap", late[i].addr, claim, late[i].target, roundTol)
		}
	}

	if lpUnits.Equal(pool.LPUnits) {
		return nil // nothing capped
	}
	for i := range late {
		k.SetLiquidityProvider(ctx, late[i].lp)
		// Emit a withdraw event for the capped unit reduction so Midgard tracks the
		// member's lowered position — SetLiquidityProvider changes it silently. su is the
		// units removed; bps is that as a fraction of the original; no payout.
		if late[i].origUnits.GT(late[i].lp.Units) {
			removed := late[i].origUnits.Sub(late[i].lp.Units)
			bps := int64(removed.MulUint64(10_000).Quo(late[i].origUnits).Uint64())
			withdrawEvt := NewEventWithdraw(asset, removed, bps, cosmos.ZeroDec(), common.Tx{
				ID:          common.BlankTxID,
				FromAddress: late[i].lp.GetAddress(),
				ToAddress:   common.NoAddress,
				Coins:       common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())),
				Chain:       common.THORChain,
			}, cosmos.ZeroUint(), cosmos.ZeroUint())
			if err := mgr.EventMgr().EmitEvent(ctx, withdrawEvt); err != nil {
				return fmt.Errorf("emit late-LP cap withdraw event %s: %w", late[i].addr, err)
			}
		}
	}
	pool.LPUnits = lpUnits
	if err := k.SetPool(ctx, pool); err != nil {
		return fmt.Errorf("set pool after cap: %w", err)
	}
	ctx.Logger().Info("capped late ETH.THOR LPs", "late_count", len(late), "pool_lp_units", lpUnits)
	return nil
}
