package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// lpDonation removes one liquidity provider record's units from its pool. The
// pool's asset and RUNE stay, so their value accrues to the remaining LPs.
type lpDonation struct {
	Asset   common.Asset
	Address common.Address // the key the LP record is stored under (RUNE or asset address)
	// Units is the pinned expected LP unit count. It is a price-INDEPENDENT integer that
	// mutates on every genuine change to the position (add, withdraw, ragnarok, record
	// removal) and on nothing else, so applyDonationMigration aborts on an EXACT per-record
	// mismatch — a drift gate that a market price move cannot trip. Re-pin at the cut.
	Units cosmos.Uint
}

// reservePayment sends RUNE from the Reserve to an account.
type reservePayment struct {
	To     string // thor1... address
	Amount cosmos.Uint
	Reason string
	// ValueOf, when set, binds Amount to the donations it repays: the Reserve pays
	// min(Amount, their combined live RUNE value) (twice the RUNE share, at the pool price).
	// The binding is ONE-SIDED — the Reserve never over-pays. Drift of the donated POSITIONS
	// is caught separately by the per-record Units check in applyDonationMigration (which is
	// price-independent), so this binding no longer aborts on a market price move.
	ValueOf []lpDonation
}

// donatedRuneValue is twice the RUNE side of an LP record at the pool price.
func donatedRuneValue(ctx cosmos.Context, mgr *Mgrs, d lpDonation) (cosmos.Uint, error) {
	k := mgr.Keeper()
	lp, err := k.GetLiquidityProvider(ctx, d.Asset, d.Address)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get LP %s %s: %w", d.Asset, d.Address, err)
	}
	pool, err := k.GetPool(ctx, d.Asset)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get pool %s: %w", d.Asset, err)
	}
	// Value the LP share against total pool units (LP + synth), as withdraw does
	// via pool.CalcUnits / GetPoolUnits — dividing by LPUnits alone overstates
	// the share when the pool carries synth units.
	pool.CalcUnits(k.GetTotalSupply(ctx, d.Asset.GetSyntheticAsset()))
	poolUnits := pool.GetPoolUnits()
	if poolUnits.IsZero() {
		return cosmos.ZeroUint(), fmt.Errorf("pool %s has no units", d.Asset)
	}
	return common.GetSafeShare(lp.Units, poolUnits, pool.BalanceRune).MulUint64(2), nil
}

// effectivePayment enforces a payment's ValueOf binding and returns the amount to
// pay. The binding is ONE-SIDED, like the ETH.THOR capital refund: the Reserve pays
// min(pinned, live donated value) so it can never over-pay. It does NOT abort on an
// upper value drift — that moved with the donated assets' market price and is not a
// state change; genuine position drift is caught by the per-record Units check in
// applyDonationMigration. An unbound payment pays its pinned amount.
func effectivePayment(ctx cosmos.Context, mgr *Mgrs, p reservePayment) (cosmos.Uint, error) {
	if len(p.ValueOf) == 0 {
		return p.Amount, nil
	}
	value := cosmos.ZeroUint()
	for _, d := range p.ValueOf {
		v, err := donatedRuneValue(ctx, mgr, d)
		if err != nil {
			return cosmos.ZeroUint(), err
		}
		value = value.Add(v)
	}
	// Pay min(pinned, live): the Reserve never over-pays. There is no upper drift abort here
	// any more — the live value moves with the donated assets' market price, which is not a
	// state change; genuine position drift is caught by the per-record Units check in
	// applyDonationMigration (price-independent). A pinned Amount set too LOW only under-pays
	// the treasury (Reserve-safe, recoverable in a later migration) and is guarded by a CI
	// recompute test rather than a runtime band.
	if value.LT(p.Amount) {
		return value, nil
	}
	return p.Amount, nil
}

// vaultAssetPairing adds asset already held by the Asgard vaults (and not yet
// counted in the pool) to its pool, paired with RUNE from the Reserve at the pool's
// current price. No LP units are minted, so the value accrues to existing LPs.
type vaultAssetPairing struct {
	Asset  common.Asset
	Amount cosmos.Uint
}

type donationMigrationSpec struct {
	Donations []lpDonation
	Payments  []reservePayment
	Pairings  []vaultAssetPairing
}

// runDonationMigration applies spec atomically, or not at all. When state no
// longer matches the spec (a record moved, a value left its band), the
// migration is skipped and logged rather than halting the upgrade; the
// donations and payments can be redone in a later migration.
func runDonationMigration(ctx cosmos.Context, mgr *Mgrs, spec donationMigrationSpec) {
	cacheCtx, commit := ctx.CacheContext()
	if err := applyDonationMigration(cacheCtx, mgr, spec); err != nil {
		ctx.Logger().Error("donation migration skipped: state does not match the spec", "error", err)
		return
	}
	commit()
}

// applyDonationMigration applies spec. It returns an error, and changes nothing the
// caller commits, if any record, balance or bound does not match the spec.
func applyDonationMigration(ctx cosmos.Context, mgr *Mgrs, spec donationMigrationSpec) error {
	k := mgr.Keeper()

	// Value the repaid positions before their units are removed, and resolve each
	// payment to min(pinned, live) (one-sided, never over-pay).
	payAmounts := make([]cosmos.Uint, len(spec.Payments))
	for i, p := range spec.Payments {
		amt, err := effectivePayment(ctx, mgr, p)
		if err != nil {
			return err
		}
		payAmounts[i] = amt
	}

	for _, d := range spec.Donations {
		lp, err := k.GetLiquidityProvider(ctx, d.Asset, d.Address)
		if err != nil {
			return fmt.Errorf("get LP %s %s: %w", d.Asset, d.Address, err)
		}
		if lp.Units.IsZero() {
			return fmt.Errorf("LP %s %s has no units", d.Asset, d.Address)
		}
		// Per-record drift gate: the position's unit count must EXACTLY match the pin.
		// Units are price-independent (they move only on this LP's own add/withdraw, a
		// ragnarok payout, or record removal — never on market price, pool depth or other
		// LPs), so this aborts on a genuine state change while a price move passes. Checked
		// PER record, never summed: the three pools' unit scales are incommensurable, and a
		// sum would let a withdrawal in one pool mask a top-up in another. A removed record
		// reads as zero units (caught above), and a mistyped address/asset likewise.
		if !lp.Units.Equal(d.Units) {
			return fmt.Errorf("LP %s %s units %s != pinned %s (state changed; re-pin at the cut)", d.Asset, d.Address, lp.Units, d.Units)
		}
		// Pending deposits are not pool depth; donating them is a separate decision.
		if !lp.PendingRune.IsZero() || !lp.PendingAsset.IsZero() {
			return fmt.Errorf("LP %s %s has pending liquidity", d.Asset, d.Address)
		}
		pool, err := k.GetPool(ctx, d.Asset)
		if err != nil {
			return fmt.Errorf("get pool %s: %w", d.Asset, err)
		}
		if pool.LPUnits.LT(lp.Units) {
			return fmt.Errorf("pool %s has fewer units (%s) than LP %s (%s)", d.Asset, pool.LPUnits, d.Address, lp.Units)
		}
		pool.LPUnits = pool.LPUnits.Sub(lp.Units)
		if pool.LPUnits.IsZero() {
			// Donating the pool's only LP would strand BalanceAsset/BalanceRune with
			// zero units: calculatePoolUnits treats zero pool units as the
			// first-depositor case, so the next (even dust) AddLiquidity would mint
			// 100% of the units and claim the whole stranded depth. Abort — matches
			// runEthThorRestore19to20's guard.
			return fmt.Errorf("donation of %s %s would leave the pool with no LP units", d.Asset, d.Address)
		}
		if err := k.SetPool(ctx, pool); err != nil {
			return fmt.Errorf("set pool %s: %w", d.Asset, err)
		}
		// Emit a withdraw event for the full unit removal so Midgard's pool + member
		// unit accounting (units = Σstake − Σwithdraw) tracks the donation — SetPool +
		// RemoveLiquidityProvider change units silently. Full removal (10000 bps), no
		// payout (the value accrues to the remaining LPs). Fields mirror
		// PoolMgr.removeLiquidityProviders / the ETH.THOR restore path.
		withdrawEvt := NewEventWithdraw(d.Asset, lp.Units, int64(10000), cosmos.ZeroDec(), common.Tx{
			ID:          common.BlankTxID,
			FromAddress: lp.GetAddress(),
			ToAddress:   common.NoAddress,
			Coins:       common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())),
			Chain:       common.THORChain,
		}, cosmos.ZeroUint(), cosmos.ZeroUint())
		if err := mgr.EventMgr().EmitEvent(ctx, withdrawEvt); err != nil {
			return fmt.Errorf("emit donation withdraw event %s %s: %w", d.Asset, d.Address, err)
		}
		k.RemoveLiquidityProvider(ctx, lp)
		ctx.Logger().Info("donated LP position", "asset", d.Asset, "address", d.Address, "units", lp.Units)
	}

	// RUNE the pairings need, at each pool's current price.
	type pairing struct {
		spec vaultAssetPairing
		rune cosmos.Uint
	}
	pairings := make([]pairing, 0, len(spec.Pairings))
	reserveNeed := cosmos.ZeroUint()
	// Each pairing is checked against the live unaccounted surplus independently and
	// none is credited until after the loop, so two entries for the same asset would
	// each pass against the same surplus and together credit up to twice what the
	// vaults hold. Reject duplicate pairing assets (reject, not sum — a duplicate is
	// a spec error, not an intent to double).
	seenPairing := make(map[string]struct{})
	for _, p := range spec.Pairings {
		if _, dup := seenPairing[p.Asset.String()]; dup {
			return fmt.Errorf("duplicate pairing asset %s", p.Asset)
		}
		seenPairing[p.Asset.String()] = struct{}{}
		if p.Amount.IsZero() {
			return fmt.Errorf("pairing %s has zero amount", p.Asset)
		}
		pool, err := k.GetPool(ctx, p.Asset)
		if err != nil {
			return fmt.Errorf("get pool %s: %w", p.Asset, err)
		}
		if pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() {
			return fmt.Errorf("pool %s has no price", p.Asset)
		}
		// The asset must already be in the vaults beyond every liability the
		// network-solvency accounting recognises (pool, trade/secured, queued/
		// streaming swaps, deficits, scheduled outbounds), so a pairing cannot
		// spend asset that is really owed elsewhere — see unaccountedVaultAsset19to20.
		unaccounted, err := unaccountedVaultAsset19to20(ctx, mgr, p.Asset)
		if err != nil {
			return fmt.Errorf("unaccounted %s: %w", p.Asset, err)
		}
		if p.Amount.GT(unaccounted) {
			return fmt.Errorf("pairing %s %s exceeds unaccounted vault balance %s", p.Amount, p.Asset, unaccounted)
		}
		// RUNE to add = p.Amount · poolRune/poolAsset, so the pairing keeps the pool price.
		// Use the UNCAPPED share: GetSafeShare clamps part to total, so a pairing larger than
		// the pool's current asset depth would be under-funded with RUNE and shift the price,
		// breaking the pairing's price-preserving contract. The amount is already bounded by
		// the unaccounted-vault solvency guard above.
		rune := common.GetUncappedShare(p.Amount, pool.BalanceAsset, pool.BalanceRune)
		pairings = append(pairings, pairing{p, rune})
		reserveNeed = reserveNeed.Add(rune)
	}
	for i, p := range spec.Payments {
		if p.Amount.IsZero() {
			return fmt.Errorf("payment to %s has zero amount", p.To)
		}
		// Reserve must cover what is ACTUALLY transferred (payAmounts = min(pinned,
		// live)), not the pinned amount — otherwise a Reserve that covers the resolved
		// payments but not the larger pinned total would wrongly abort the whole step.
		reserveNeed = reserveNeed.Add(payAmounts[i])
	}
	if reserve := k.GetRuneBalanceOfModule(ctx, ReserveName); reserve.LT(reserveNeed) {
		return fmt.Errorf("reserve %s below required %s", reserve, reserveNeed)
	}

	for _, p := range pairings {
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), p.rune))
		if err := k.SendFromModuleToModule(ctx, ReserveName, AsgardName, coins); err != nil {
			return fmt.Errorf("move reserve RUNE for %s: %w", p.spec.Asset, err)
		}
		pool, err := k.GetPool(ctx, p.spec.Asset)
		if err != nil {
			return fmt.Errorf("get pool %s: %w", p.spec.Asset, err)
		}
		pool.BalanceAsset = pool.BalanceAsset.Add(p.spec.Amount)
		pool.BalanceRune = pool.BalanceRune.Add(p.rune)
		if err := k.SetPool(ctx, pool); err != nil {
			return fmt.Errorf("set pool %s: %w", p.spec.Asset, err)
		}
		// Emit the depth change so Midgard (event-sourced) tracks the credited
		// asset + paired RUNE, like the slash step's event.
		emitPoolBalanceChangedEvent(ctx, NewPoolMod(p.spec.Asset, p.rune, true, p.spec.Amount, true), "donation_pairing_19to20", mgr)
		ctx.Logger().Info("paired vault asset with reserve RUNE", "asset", p.spec.Asset, "amount", p.spec.Amount, "rune", p.rune)
	}

	for i, p := range spec.Payments {
		to, err := cosmos.AccAddressFromBech32(p.To)
		if err != nil {
			return fmt.Errorf("payment address %q: %w", p.To, err)
		}
		// Skip a RESOLVED-zero payment (min(pinned,live) == 0 when the bound value rounds to
		// 0): the donations still execute and the recipient is paid its current value of 0 —
		// the accepted one-sided outcome. A zero-coin bank send would otherwise error and
		// soft-skip the whole otherwise-valid migration. The pinned-amount reject at :239
		// stays as a spec well-formedness check.
		if payAmounts[i].IsZero() {
			ctx.Logger().Info("payment resolved to zero (live value 0); donations applied, nothing sent", "to", p.To, "pinned", p.Amount)
			continue
		}
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), payAmounts[i]))
		if err := k.SendFromModuleToAccount(ctx, ReserveName, to, coins); err != nil {
			return fmt.Errorf("pay %s: %w", p.To, err)
		}
		ctx.Logger().Info("paid RUNE from reserve", "to", p.To, "amount", payAmounts[i], "pinned", p.Amount, "reason", p.Reason)
	}
	return nil
}

// unaccountedVaultAsset19to20 returns the Asgard-held amount of asset that is NOT
// already accounted for by the pool, trade/secured accounts, queued/streaming
// swaps, deficits, or scheduled outbounds — i.e. the free surplus a migration
// step may credit or pair without making any of those insolvent.
//
// It delegates to the canonical calculateNetworkSolvency rather than
// re-implementing the accounting: the live network-solvency amount for the asset
// IS the free (positive) or missing (negative) surplus. That function errors with
// "churn in progress" if any Asgard vault is RetiringVault, which propagates here
// so the caller soft-skips the step (safer than crediting against a mid-churn
// snapshot). A negative solvency (under-solvent) or an untracked asset yields zero
// free surplus.
func unaccountedVaultAsset19to20(ctx cosmos.Context, mgr *Mgrs, asset common.Asset) (cosmos.Uint, error) {
	vm, ok := mgr.NetworkMgr().(*NetworkMgr)
	if !ok {
		return cosmos.ZeroUint(), fmt.Errorf("network manager is %T, not *NetworkMgr", mgr.NetworkMgr())
	}
	sol, err := vm.calculateNetworkSolvency(ctx, mgr)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("network solvency: %w", err)
	}
	for _, a := range sol {
		if a.Asset.Equals(asset) {
			if a.Amount.IsNegative() {
				return cosmos.ZeroUint(), nil
			}
			return cosmos.NewUintFromBigInt(a.Amount.BigInt()), nil
		}
	}
	return cosmos.ZeroUint(), nil
}
